package core

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ports"
)

// defaultMaxSessions is used when the configuration leaves the limit unset.
const defaultMaxSessions = 3

func (d *Daemon) runCommand(ctx context.Context, in fsp.Inbound, cmd ports.Command) {
	ep := in.Endpoint
	switch cmd.Name {
	case "ls":
		d.cmdLs(ctx, ep)
	case "new":
		d.cmdNew(ctx, ep, cmd.Args)
	case "use":
		d.cmdUse(ctx, ep, cmd.Args)
	case "agents":
		d.cmdAgents(ctx, ep)
	case "stop":
		d.cmdStop(ctx, ep)
	case "close":
		d.cmdClose(ctx, ep, cmd.Args)
	case "mode":
		d.cmdMode(ctx, ep, cmd.Args)
	case "lang":
		d.cmdLang(ctx, ep, cmd.Args)
	case "pair":
		d.cmdPair(ctx, in, cmd.Args)
	case "pull", "file":
		d.reply(ctx, ep, "feature.coming", nil)
	case "help":
		d.reply(ctx, ep, "help.text", nil)
	default:
		d.log.Warn("unknown command", "name", cmd.Name)
	}
}

func (d *Daemon) maxSessions() int {
	if n := d.deps.Config.Limits.MaxManagedSessions; n > 0 {
		return n
	}
	return defaultMaxSessions
}

func (d *Daemon) driver() (ports.Driver, bool) {
	dr, ok := d.deps.Drivers[fsp.DriverACP]
	return dr, ok && dr != nil
}

// startSession resolves the agent, starts it in cwd and binds the result to the
// endpoint. The error is fsp.ErrNotFound for an unknown agent and
// fsp.ErrQuotaExceeded when the session limit is reached.
func (d *Daemon) startSession(ctx context.Context, ep fsp.EndpointID, agent, cwd string) (*session, error) {
	spec, err := d.deps.Registry.Resolve(ctx, agent)
	if err != nil {
		return nil, err
	}
	if d.countManaged() >= d.maxSessions() {
		return nil, fsp.ErrQuotaExceeded
	}
	dr, ok := d.driver()
	if !ok {
		return nil, errors.New("no acp driver configured")
	}
	spec.Cwd = cwd

	now := d.deps.Clock.Now()
	id := fsp.SessionID(ids.New("sess"))
	s := &session{
		d:     d,
		tools: make(map[string]bool),
		rec: fsp.Session{
			ID:         id,
			Driver:     dr.Kind(),
			Agent:      agent,
			Cwd:        cwd,
			Origin:     fsp.OriginManaged,
			State:      fsp.StateStarting,
			Owner:      ep,
			CreatedAt:  now,
			LastActive: now,
		},
	}
	// Registered before the driver runs so that a permission request raised
	// during start-up already finds the session.
	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()

	as, err := dr.Start(ctx, spec, sessionApprover{d: d, id: id})
	if err != nil {
		d.forget(id)
		return nil, err
	}

	s.mu.Lock()
	s.as = as
	s.rec.AgentSessionID = as.AgentSessionID()
	s.rec.State = fsp.StateIdle
	rec := s.rec
	s.mu.Unlock()

	d.persist(ctx, rec)
	slot, err := d.deps.Store.Bind(ctx, ep, id)
	if err != nil {
		d.log.Error("bind session", "session", id, "error", err)
	}
	s.slot = slot
	if err := d.deps.Store.SetForeground(ctx, ep, id); err != nil {
		d.log.Error("set foreground", "session", id, "error", err)
	}
	d.audit(ctx, ports.AuditEntry{Kind: "session_started", Endpoint: ep, Session: id, Detail: agent})
	s.start(ctx)
	return s, nil
}

func (d *Daemon) cmdNew(ctx context.Context, ep fsp.EndpointID, args []string) {
	if len(args) == 0 {
		d.reply(ctx, ep, "usage.new", nil)
		return
	}
	agent := args[0]
	dir := ""
	switch {
	case len(args) > 1:
		dir = strings.Join(args[1:], " ")
	case len(d.roots) != 0:
		dir = d.roots[0]
	}
	cwd, ok := d.resolveWorkspace(dir)
	if !ok {
		d.reply(ctx, ep, "workspace.denied", map[string]any{"Dir": dir})
		return
	}

	s, err := d.startSession(ctx, ep, agent, cwd)
	switch {
	case errors.Is(err, fsp.ErrNotFound):
		d.reply(ctx, ep, "agent.unknown", map[string]any{"Name": agent})
	case errors.Is(err, fsp.ErrQuotaExceeded):
		d.reply(ctx, ep, "session.limit", map[string]any{"Max": d.maxSessions()})
	case err != nil:
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": agent, "Reason": err.Error()})
	default:
		d.reply(ctx, ep, "session.created", map[string]any{"Slot": s.slot, "Agent": agent, "Cwd": cwd})
	}
}

func (d *Daemon) cmdUse(ctx context.Context, ep fsp.EndpointID, args []string) {
	if len(args) == 0 {
		d.reply(ctx, ep, "usage.use", nil)
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || n <= 0 {
		d.reply(ctx, ep, "usage.use", nil)
		return
	}
	s := d.bySlot(ctx, ep, n)
	if s == nil {
		d.reply(ctx, ep, "session.not_found", map[string]any{"Slot": n})
		return
	}
	if s.live() != nil {
		if err := d.deps.Store.SetForeground(ctx, ep, s.id()); err != nil {
			d.log.Error("set foreground", "session", s.id(), "error", err)
		}
		d.reply(ctx, ep, "session.switched", map[string]any{"Slot": n})
		return
	}
	d.resume(ctx, ep, s, n)
}

// resume brings a parked session back. An agent that cannot resume gets a fresh
// session in the same directory instead.
func (d *Daemon) resume(ctx context.Context, ep fsp.EndpointID, s *session, slot int) {
	rec := s.snapshot()
	spec, err := d.deps.Registry.Resolve(ctx, rec.Agent)
	if err != nil {
		if errors.Is(err, fsp.ErrNotFound) {
			d.reply(ctx, ep, "agent.unknown", map[string]any{"Name": rec.Agent})
			return
		}
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": rec.Agent, "Reason": err.Error()})
		return
	}
	dr, ok := d.driver()
	if !ok {
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": rec.Agent, "Reason": "no acp driver configured"})
		return
	}
	spec.Cwd = rec.Cwd

	as, err := dr.Resume(ctx, spec, rec.AgentSessionID, sessionApprover{d: d, id: rec.ID})
	if errors.Is(err, fsp.ErrUnsupported) {
		fresh, err := d.startSession(ctx, ep, rec.Agent, rec.Cwd)
		switch {
		case errors.Is(err, fsp.ErrQuotaExceeded):
			d.reply(ctx, ep, "session.limit", map[string]any{"Max": d.maxSessions()})
		case err != nil:
			d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": rec.Agent, "Reason": err.Error()})
		default:
			d.reply(ctx, ep, "session.resume_unsupported", map[string]any{"Agent": rec.Agent, "Slot": fresh.slot})
		}
		return
	}
	if err != nil {
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": rec.Agent, "Reason": err.Error()})
		return
	}

	s.mu.Lock()
	s.as = as
	s.rec.AgentSessionID = as.AgentSessionID()
	s.rec.State = fsp.StateIdle
	s.rec.LastActive = d.deps.Clock.Now()
	rec = s.rec
	s.mu.Unlock()

	d.persist(ctx, rec)
	if err := d.deps.Store.SetForeground(ctx, ep, rec.ID); err != nil {
		d.log.Error("set foreground", "session", rec.ID, "error", err)
	}
	d.audit(ctx, ports.AuditEntry{Kind: "session_started", Endpoint: ep, Session: rec.ID, Detail: "resumed"})
	s.start(ctx)
	d.reply(ctx, ep, "session.switched", map[string]any{"Slot": slot})
}

func (d *Daemon) cmdStop(ctx context.Context, ep fsp.EndpointID) {
	s := d.foreground(ctx, ep)
	if s == nil {
		d.reply(ctx, ep, "hint.no_session", nil)
		return
	}
	if as := s.live(); as != nil {
		if err := as.Cancel(ctx); err != nil {
			d.log.Error("cancel turn", "session", s.id(), "error", err)
		}
	}
	d.deps.Approvals.CancelSession(s.id())
}

func (d *Daemon) cmdClose(ctx context.Context, ep fsp.EndpointID, args []string) {
	slot := 0
	var s *session
	if len(args) != 0 {
		n, err := strconv.Atoi(strings.TrimSpace(args[0]))
		if err != nil || n <= 0 {
			d.reply(ctx, ep, "usage.use", nil)
			return
		}
		slot, s = n, d.bySlot(ctx, ep, n)
	} else if s = d.foreground(ctx, ep); s != nil {
		slot = s.slot
	}
	if s == nil {
		d.reply(ctx, ep, "session.not_found", map[string]any{"Slot": slot})
		return
	}

	s.mu.Lock()
	s.closing = true
	as := s.as
	s.as = nil
	s.pending = nil
	s.rec.State = fsp.StateClosed
	s.rec.LastActive = d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()

	if as != nil {
		if err := as.Close(); err != nil {
			d.log.Error("close session", "session", rec.ID, "error", err)
		}
	}
	d.persist(ctx, rec)
	if err := d.deps.Store.Unbind(ctx, ep, rec.ID); err != nil {
		d.log.Error("unbind session", "session", rec.ID, "error", err)
	}
	d.forget(rec.ID)
	d.deps.Approvals.CancelSession(rec.ID)
	d.audit(ctx, ports.AuditEntry{Kind: "session_closed", Endpoint: ep, Session: rec.ID})
	d.reply(ctx, ep, "session.closed", map[string]any{"Slot": slot})
}

func (d *Daemon) cmdLs(ctx context.Context, ep fsp.EndpointID) {
	bindings, err := d.deps.Store.Bindings(ctx, ep)
	if err != nil {
		d.log.Error("bindings", "endpoint", ep.String(), "error", err)
		return
	}
	if len(bindings) == 0 {
		d.reply(ctx, ep, "hint.no_session", nil)
		return
	}
	lines := []string{d.t(ctx, ep, "session.list_header", nil)}
	for _, b := range bindings {
		rec, ok := d.record(ctx, b.Session)
		if !ok {
			continue
		}
		mark := ""
		if b.Foreground {
			mark = "▶"
		}
		lines = append(lines, d.t(ctx, ep, "session.list_item", map[string]any{
			"Mark":  mark,
			"Slot":  b.Slot,
			"Agent": rec.Agent,
			"Cwd":   rec.Cwd,
			"State": d.t(ctx, ep, "session.state."+string(rec.State), nil),
		}))
	}
	d.send(ctx, fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: strings.Join(lines, "\n"), Urgent: true})
}

// record returns the live record of a session, falling back to the store.
func (d *Daemon) record(ctx context.Context, id fsp.SessionID) (fsp.Session, bool) {
	if s := d.lookup(id); s != nil {
		return s.snapshot(), true
	}
	rec, err := d.deps.Store.GetSession(ctx, id)
	if err != nil {
		d.log.Error("get session", "session", id, "error", err)
		return fsp.Session{}, false
	}
	return rec, true
}

func (d *Daemon) cmdAgents(ctx context.Context, ep fsp.EndpointID) {
	lines := []string{d.t(ctx, ep, "agents.list_header", nil)}
	for _, a := range d.deps.Registry.Agents(ctx) {
		status := d.t(ctx, ep, "agents.available", nil)
		if !a.Available {
			status = d.t(ctx, ep, "agents.unavailable", map[string]any{"Reason": a.Reason})
		}
		lines = append(lines, d.t(ctx, ep, "agents.list_item", map[string]any{
			"Name":    a.Name,
			"Display": a.Display,
			"Status":  status,
		}))
	}
	d.send(ctx, fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: strings.Join(lines, "\n"), Urgent: true})
}

func (d *Daemon) cmdMode(ctx context.Context, ep fsp.EndpointID, args []string) {
	if len(args) < 2 {
		d.reply(ctx, ep, "usage.mode", nil)
		return
	}
	s := d.foreground(ctx, ep)
	if s == nil {
		d.reply(ctx, ep, "hint.no_session", nil)
		return
	}
	as := s.live()
	if as == nil {
		d.reply(ctx, ep, "session.parked", map[string]any{"Slot": s.slot})
		return
	}
	key, value := args[0], strings.Join(args[1:], " ")
	if err := as.SetConfig(ctx, key, value); err != nil {
		if !errors.Is(err, fsp.ErrUnsupported) {
			d.log.Error("set config", "session", s.id(), "key", key, "error", err)
		}
		d.reply(ctx, ep, "mode.unsupported", nil)
		return
	}
	d.reply(ctx, ep, "mode.updated", map[string]any{"Key": key, "Value": value})
}

func (d *Daemon) cmdLang(ctx context.Context, ep fsp.EndpointID, args []string) {
	if len(args) == 0 {
		d.reply(ctx, ep, "usage.lang", nil)
		return
	}
	var lang fsp.Lang
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "zh", "zh-cn", "zh-hans", "cn", "中文":
		lang = fsp.LangZH
	case "en", "en-us", "english":
		lang = fsp.LangEN
	default:
		d.reply(ctx, ep, "usage.lang", nil)
		return
	}
	if err := d.deps.Store.SetKV(ctx, langKey(ep), string(lang)); err != nil {
		d.log.Error("set language", "endpoint", ep.String(), "error", err)
		return
	}
	d.send(ctx, fsp.Outbound{
		Endpoint: ep,
		Kind:     fsp.OutText,
		Text:     d.deps.I18n.T(lang, "lang.switched", nil),
		Urgent:   true,
	})
}

func (d *Daemon) cmdPair(ctx context.Context, in fsp.Inbound, args []string) {
	ep := in.Endpoint
	// Only an approver may confirm a pairing; anyone else is ignored.
	if !d.deps.ACL.IsApprover(ep, in.SenderID) {
		return
	}
	if len(args) == 0 {
		d.reply(ctx, ep, "usage.pair", nil)
		return
	}
	code := strings.TrimSpace(args[0])
	peer, senderID, err := d.deps.ACL.ConfirmPairing(ctx, code)
	if err != nil {
		d.reply(ctx, ep, "pair.invalid", nil)
		return
	}
	d.reply(ctx, ep, "pair.confirmed_owner", map[string]any{"Sender": senderID})
	d.reply(ctx, peer, "pair.confirmed", nil)
}
