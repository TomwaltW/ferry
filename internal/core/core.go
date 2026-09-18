package core

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Deps struct {
	Config config.Config
	Home   string
	// IMs are the entry points the daemon serves, one adapter per platform
	// (spec-p3 3.1). IM is the single-adapter form of P1 and P2; New folds it
	// into IMs, and it goes away once every caller fills IMs instead.
	IMs       []ports.IMAdapter
	IM        ports.IMAdapter
	Drivers   map[fsp.DriverKind]ports.Driver
	Registry  ports.Registry
	Store     ports.Store
	Outbox    ports.Outbox
	ACL       ports.ACL
	Approvals ports.Approvals
	Handoff   ports.Handoff
	Waker     ports.Waker
	Commands  ports.Commands
	I18n      ports.I18n
	Clock     clock.Clock
	Log       *slog.Logger
}

// normalizeAdapters is the one place that reconciles the two shapes Deps
// accepts: IMs when the caller has them, the P1 single IM otherwise.
func normalizeAdapters(d Deps) []ports.IMAdapter {
	if len(d.IMs) > 0 {
		return d.IMs
	}
	if d.IM != nil {
		return []ports.IMAdapter{d.IM}
	}
	return nil
}

type Status struct {
	IMOnline  bool
	IMDetail  string
	BotID     string
	Sessions  int
	Running   int
	UpdatedAt time.Time
}

// imRetryInterval is how long the daemon waits before calling IM.Run again
// after the platform session expired.
const imRetryInterval = 60 * time.Second

// Daemon routes inbound messages to sessions. Its fields are defined by the
// P1 T5 task.
type Daemon struct {
	deps  Deps
	log   *slog.Logger
	roots []string // workspace roots, absolute with symlinks resolved

	mu        sync.Mutex
	sessions  map[fsp.SessionID]*session // every session that is not closed
	windows   map[string]string          // endpoint -> reply context of its last inbound
	terminals map[string]terminalList    // endpoint -> the numbers its last /ls gave terminal sessions
	imFailed  bool                       // the adapter is down and a retry is pending
	imDetail  string
	stop      bool // the daemon is shutting down; parking stays silent

	wg sync.WaitGroup
}

func New(d Deps) *Daemon {
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d.IMs = normalizeAdapters(d)
	if len(d.IMs) > 0 {
		// Until the multi-entry routing of P3-T1 lands, the single-adapter
		// paths below read the first entry point, which is the owner's.
		d.IM = d.IMs[0]
	}
	dm := &Daemon{
		deps:      d,
		log:       log,
		sessions:  make(map[fsp.SessionID]*session),
		windows:   make(map[string]string),
		terminals: make(map[string]terminalList),
	}
	for _, root := range d.Config.Workspace.Roots {
		norm, err := normalizePath(root)
		if err != nil {
			log.Warn("workspace root unusable", "root", root, "error", err)
			continue
		}
		dm.roots = append(dm.roots, norm)
	}
	return dm
}

// Run blocks until ctx ends. When the IM adapter returns fsp.ErrSessionExpired
// the daemon stays up: it marks the adapter offline, writes an audit entry and
// retries IM.Run every 60 seconds.
//
// Run also drives the outbox loop, so the caller only starts the daemon.
func (d *Daemon) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := d.parkStaleSessions(runCtx); err != nil {
		return err
	}
	d.adoptOwner(runCtx)

	inbound := make(chan fsp.Inbound, 64)
	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		if err := d.deps.Outbox.Run(runCtx); err != nil && runCtx.Err() == nil {
			d.log.Error("outbox loop stopped", "error", err)
		}
	}()
	go func() {
		defer d.wg.Done()
		d.runIM(runCtx, inbound)
	}()

	for {
		select {
		case <-runCtx.Done():
			d.shutdown(context.WithoutCancel(ctx))
			cancel()
			d.wg.Wait()
			return nil
		case in := <-inbound:
			d.handleInbound(runCtx, in)
		}
	}
}

func (d *Daemon) Status() Status {
	st := d.deps.IM.Status()
	d.mu.Lock()
	online, detail := st.Online, st.Detail
	if d.imFailed {
		online, detail = false, d.imDetail
	}
	total, running := 0, 0
	for _, s := range d.sessions {
		total++
		switch s.State() {
		case fsp.StateRunning, fsp.StateAwaiting:
			running++
		}
	}
	d.mu.Unlock()
	return Status{
		IMOnline:  online,
		IMDetail:  detail,
		BotID:     st.BotID,
		Sessions:  total,
		Running:   running,
		UpdatedAt: d.deps.Clock.Now(),
	}
}

// parkStaleSessions marks every session left behind by a previous process as
// parked: its agent is gone even though the row says otherwise.
func (d *Daemon) parkStaleSessions(ctx context.Context) error {
	rows, err := d.deps.Store.ActiveSessions(ctx)
	if err != nil {
		return err
	}
	for _, rec := range rows {
		rec.State = fsp.StateParked
		if err := d.deps.Store.PutSession(ctx, rec); err != nil {
			d.log.Error("park session on startup", "session", rec.ID, "error", err)
			continue
		}
		s := &session{d: d, rec: rec, tools: make(map[string]bool)}
		s.slot = d.slotOf(ctx, rec.Owner, rec.ID)
		d.mu.Lock()
		d.sessions[rec.ID] = s
		d.mu.Unlock()
	}
	return nil
}

// adoptOwner tells the ACL who scanned the QR code for the current bot.
func (d *Daemon) adoptOwner(ctx context.Context) {
	st := d.deps.IM.Status()
	if st.BotID == "" || st.OwnerID == "" {
		return
	}
	if err := d.deps.ACL.SetOwner(ctx, d.deps.IM.Platform(), st.BotID, st.OwnerID); err != nil {
		d.log.Error("set owner", "error", err)
	}
}

// runIM keeps the inbound stream alive. An expired platform session marks the
// adapter offline and is retried every imRetryInterval instead of stopping the
// daemon.
func (d *Daemon) runIM(ctx context.Context, out chan<- fsp.Inbound) {
	for {
		d.mu.Lock()
		d.imFailed, d.imDetail = false, ""
		d.mu.Unlock()

		err := d.deps.IM.Run(ctx, out)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, fsp.ErrSessionExpired):
			d.markOffline(ctx, err)
		case err != nil:
			d.log.Error("im adapter stopped", "error", err)
			d.mu.Lock()
			d.imFailed, d.imDetail = true, err.Error()
			d.mu.Unlock()
		default:
			return
		}
		select {
		case <-d.deps.Clock.After(imRetryInterval):
		case <-ctx.Done():
			return
		}
	}
}

func (d *Daemon) markOffline(ctx context.Context, err error) {
	d.mu.Lock()
	d.imFailed, d.imDetail = true, err.Error()
	d.mu.Unlock()
	d.log.Warn("platform session expired, log in again", "error", err)
	d.audit(ctx, ports.AuditEntry{Kind: "expired", Detail: err.Error()})
	// Chat is exactly what is unavailable, so the operator is told elsewhere.
	d.wake(ctx, "im_offline", nil)
}

// shutdown closes every live session and leaves it parked. Closed sessions keep
// their record.
func (d *Daemon) shutdown(ctx context.Context) {
	d.mu.Lock()
	d.stop = true
	live := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		live = append(live, s)
	}
	d.mu.Unlock()
	for _, s := range live {
		s.park(ctx)
	}
}

func (d *Daemon) stopping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stop
}

func (d *Daemon) audit(ctx context.Context, e ports.AuditEntry) {
	e.At = d.deps.Clock.Now()
	if err := d.deps.Store.Audit(ctx, e); err != nil {
		d.log.Error("audit", "kind", e.Kind, "error", err)
	}
}

// lang returns the bot language of an endpoint, falling back to the configured
// default.
func (d *Daemon) lang(ctx context.Context, ep fsp.EndpointID) fsp.Lang {
	v, err := d.deps.Store.GetKV(ctx, langKey(ep))
	if err != nil || v == "" {
		return d.deps.Config.Lang
	}
	return fsp.Lang(v)
}

func langKey(ep fsp.EndpointID) string { return "lang:" + ep.String() }

func (d *Daemon) t(ctx context.Context, ep fsp.EndpointID, key string, args map[string]any) string {
	return d.deps.I18n.T(d.lang(ctx, ep), key, args)
}

// reply queues a localised message for the endpoint. Direct answers to a user
// action are urgent so the outbox does not hold them back for a merge window.
func (d *Daemon) reply(ctx context.Context, ep fsp.EndpointID, key string, args map[string]any) {
	d.send(ctx, fsp.Outbound{
		Endpoint: ep,
		Kind:     fsp.OutText,
		Text:     d.t(ctx, ep, key, args),
		Urgent:   true,
	})
}

func (d *Daemon) send(ctx context.Context, msg fsp.Outbound) {
	if err := d.deps.Outbox.Enqueue(ctx, msg); err != nil {
		d.log.Error("enqueue", "endpoint", msg.Endpoint.String(), "error", err)
	}
}

func (d *Daemon) setWindow(ep fsp.EndpointID, replyCtx string) {
	d.mu.Lock()
	d.windows[ep.String()] = replyCtx
	d.mu.Unlock()
}

func (d *Daemon) window(ep fsp.EndpointID) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.windows[ep.String()]
}

func (d *Daemon) typing(ctx context.Context, ep fsp.EndpointID, on bool) {
	if err := d.deps.IM.Typing(ctx, ep, d.window(ep), on); err != nil {
		d.log.Debug("typing indicator", "error", err)
	}
}

// slotOf looks up the binding slot of a session, or 0 when it has none.
func (d *Daemon) slotOf(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) int {
	bindings, err := d.deps.Store.Bindings(ctx, ep)
	if err != nil {
		d.log.Error("bindings", "endpoint", ep.String(), "error", err)
		return 0
	}
	for _, b := range bindings {
		if b.Session == id {
			return b.Slot
		}
	}
	return 0
}

// foreground returns the endpoint's foreground session, if any.
func (d *Daemon) foreground(ctx context.Context, ep fsp.EndpointID) *session {
	bindings, err := d.deps.Store.Bindings(ctx, ep)
	if err != nil {
		d.log.Error("bindings", "endpoint", ep.String(), "error", err)
		return nil
	}
	for _, b := range bindings {
		if !b.Foreground {
			continue
		}
		if s := d.lookup(b.Session); s != nil {
			return s
		}
	}
	return nil
}

// bySlot returns the session bound to slot n of the endpoint.
func (d *Daemon) bySlot(ctx context.Context, ep fsp.EndpointID, n int) *session {
	bindings, err := d.deps.Store.Bindings(ctx, ep)
	if err != nil {
		d.log.Error("bindings", "endpoint", ep.String(), "error", err)
		return nil
	}
	for _, b := range bindings {
		if b.Slot == n {
			return d.lookup(b.Session)
		}
	}
	return nil
}

func (d *Daemon) lookup(id fsp.SessionID) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessions[id]
}

func (d *Daemon) forget(id fsp.SessionID) {
	d.mu.Lock()
	delete(d.sessions, id)
	d.mu.Unlock()
}

// countManaged counts managed sessions that still hold an agent process:
// starting, idle, running or awaiting. Parked sessions do not take a slot.
func (d *Daemon) countManaged() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, s := range d.sessions {
		switch s.State() {
		case fsp.StateStarting, fsp.StateIdle, fsp.StateRunning, fsp.StateAwaiting:
			n++
		}
	}
	return n
}

// normalizePath expands a leading ~, makes the path absolute and resolves
// symlinks. A path that does not exist yields an error.
func normalizePath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// resolveWorkspace normalizes dir and requires the result to be a workspace
// root or below one.
func (d *Daemon) resolveWorkspace(dir string) (string, bool) {
	norm, err := normalizePath(dir)
	if err != nil {
		return "", false
	}
	for _, root := range d.roots {
		if norm == root || strings.HasPrefix(norm, root+string(filepath.Separator)) {
			return norm, true
		}
	}
	return "", false
}
