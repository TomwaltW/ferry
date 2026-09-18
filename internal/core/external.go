package core

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ports"
)

// externalEvents is how many events an attached process may run ahead of the
// session loop before the reader waits for it.
const externalEvents = 32

// Attach registers a session the operator started elsewhere — ferry proxy,
// ferry channel or ferry run — and mirrors it into chat.
func (c controlHandler) Attach(ctx context.Context, req control.AttachRequest, s control.ServerStream) (control.AttachResponse, error) {
	return c.d.attachExternal(ctx, req, s)
}

// driverOf maps the kind of external process onto the driver it stands for.
func driverOf(kind string) (fsp.DriverKind, bool) {
	switch kind {
	case control.KindProxy:
		return fsp.DriverProxy, true
	case control.KindChannel:
		return fsp.DriverClaudeChannel, true
	case control.KindPTY:
		return fsp.DriverPTY, true
	}
	return "", false
}

// attachExternal binds one external session to the owner endpoint of the
// platform external.bind_platform names, and starts mirroring it.
func (d *Daemon) attachExternal(ctx context.Context, req control.AttachRequest, stream control.ServerStream) (control.AttachResponse, error) {
	kind, ok := driverOf(req.Kind)
	if !ok {
		return control.AttachResponse{}, fmt.Errorf("core: attach %q: %w", req.Kind, fsp.ErrUnsupported)
	}
	ep, err := d.externalOwner()
	if err != nil {
		return control.AttachResponse{}, err
	}

	now := d.deps.Clock.Now()
	id := fsp.SessionID(ids.New("sess"))
	es := &externalSession{
		d:      d,
		id:     id,
		req:    req,
		stream: stream,
		sender: ep.PeerID,
		events: make(chan fsp.Event, externalEvents),
		asks:   make(map[string]context.CancelFunc),
	}
	s := &session{
		d:        d,
		tools:    make(map[string]bool),
		as:       es,
		external: es,
		rec: fsp.Session{
			ID:             id,
			Driver:         kind,
			Agent:          req.Agent,
			Cwd:            req.Cwd,
			AgentSessionID: req.AgentSessionID,
			Origin:         fsp.OriginTerminal,
			State:          fsp.StateIdle,
			Owner:          ep,
			CreatedAt:      now,
			LastActive:     now,
		},
	}

	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()

	d.persist(ctx, s.snapshot())
	slot, err := d.deps.Store.Bind(ctx, ep, id)
	if err != nil {
		d.forget(id)
		return control.AttachResponse{}, fmt.Errorf("core: bind an external session: %w", err)
	}
	s.slot = slot
	if err := d.deps.Store.SetForeground(ctx, ep, id); err != nil {
		d.log.Error("set foreground", "session", id, "error", err)
	}
	d.audit(ctx, ports.AuditEntry{Kind: "session_started", Endpoint: ep, Session: id, Detail: req.Kind})

	// The session ends with the external process, and with the daemon: a
	// control connection that outlives the daemon must not keep it alive.
	sessCtx, cancel := context.WithCancel(ctx)
	stopBase := context.AfterFunc(d.base(), cancel)
	es.stop = func() {
		stopBase()
		cancel()
	}

	s.start(sessCtx)
	go es.pump(sessCtx)

	d.announceAttached(ctx, s, req)
	return control.AttachResponse{SessionID: string(id), Slot: slot, Endpoint: ep.String()}, nil
}

// externalOwner is the endpoint external sessions report to: the owner of the
// platform external.bind_platform names.
func (d *Daemon) externalOwner() (fsp.EndpointID, error) {
	bind := fsp.Platform(d.deps.Config.External.WithDefaults().BindPlatform)
	im, ok := d.adapters[bind]
	if !ok {
		if d.primary == nil {
			return fsp.EndpointID{}, fmt.Errorf("core: no entry point to attach to: %w", fsp.ErrNotFound)
		}
		im = d.primary
	}
	st := im.Status()
	if st.OwnerID == "" {
		return fsp.EndpointID{}, fmt.Errorf("core: %s has no owner yet: %w", im.Platform(), fsp.ErrNotFound)
	}
	return fsp.EndpointID{Platform: im.Platform(), BotID: st.BotID, PeerID: st.OwnerID}, nil
}

// announceAttached tells the endpoint what just showed up. A wrapped terminal
// program also says at once that its approvals stay on the computer, because
// nothing about it will reach chat later.
func (d *Daemon) announceAttached(ctx context.Context, s *session, req control.AttachRequest) {
	rec := s.snapshot()
	switch req.Kind {
	case control.KindProxy:
		d.reply(ctx, rec.Owner, "proxy.attached", map[string]any{
			"Agent": req.Agent, "Cwd": req.Cwd, "Slot": s.slot,
		})
	case control.KindChannel:
		d.reply(ctx, rec.Owner, "channel.attached", map[string]any{"Slot": s.slot})
	case control.KindPTY:
		d.reply(ctx, rec.Owner, "pty.attached", map[string]any{"Name": ptyName(req), "Slot": s.slot})
		if !req.RemoteApprove {
			d.reply(ctx, rec.Owner, "pty.no_approval", nil)
		}
	}
}

// ptyName is what a wrapped program is called in chat: its title when it has
// one, the command otherwise.
func ptyName(req control.AttachRequest) string {
	if req.Title != "" {
		return req.Title
	}
	return req.Agent
}

// externalSession is a session running in another process, dressed as an
// agent session so that the rest of the core cannot tell the difference
// (spec-p3 3.3).
type externalSession struct {
	d      *Daemon
	id     fsp.SessionID
	req    control.AttachRequest
	stream control.ServerStream
	sender string // the owner, put on every prompt the stream carries
	events chan fsp.Event
	stop   func()

	mu     sync.Mutex
	closed bool
	// asks holds the cancel of every approval waiting on a human, by the
	// request ID the external process chose. approval_cancel withdraws one.
	asks map[string]context.CancelFunc
}

func (e *externalSession) AgentSessionID() string { return e.req.AgentSessionID }

// Caps: an external session takes a turn, can be cancelled and can be
// configured. Resuming and listing belong to the process that owns the agent.
func (e *externalSession) Caps() ports.Caps { return ports.Caps{Cancel: true, Config: true} }

// Prompt forwards chat input. A session that does not take input never gets
// here: handleInbound answers inject.disabled instead.
func (e *externalSession) Prompt(ctx context.Context, text string) error {
	if !e.req.AllowInject {
		return fmt.Errorf("core: %s takes no input from chat: %w", e.req.Kind, fsp.ErrUnsupported)
	}
	return e.send(control.Message{Type: control.MsgPrompt, SessionID: string(e.id), Text: text, Sender: e.sender})
}

func (e *externalSession) Cancel(ctx context.Context) error {
	return e.send(control.Message{Type: control.MsgCancel, SessionID: string(e.id)})
}

func (e *externalSession) SetConfig(ctx context.Context, key, value string) error {
	return e.send(control.Message{Type: control.MsgConfig, SessionID: string(e.id), Key: key, Value: value})
}

func (e *externalSession) Events() <-chan fsp.Event { return e.events }

// Close asks the external process to end the session and stops reading from
// it. The process going away on its own takes the same path through finish.
func (e *externalSession) Close() error {
	err := e.send(control.Message{Type: control.MsgClose, SessionID: string(e.id)})
	e.finish()
	return err
}

// send writes one frame, unless the session is already over.
func (e *externalSession) send(m control.Message) error {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return fmt.Errorf("core: the %s session has ended: %w", e.req.Kind, fsp.ErrNotFound)
	}
	if err := e.stream.Send(m); err != nil {
		return fmt.Errorf("core: send %s to the %s process: %w", m.Type, e.req.Kind, err)
	}
	return nil
}

// finish closes the event channel exactly once, which is what tells the
// session loop the agent is gone. The session's context stays alive until that
// loop has run: cancelling here would race it into the shutdown branch and the
// session would never be closed.
func (e *externalSession) finish() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	pending := e.asks
	e.asks = nil
	e.mu.Unlock()

	for _, cancel := range pending {
		cancel()
	}
	close(e.events)
}

// pump reads the stream until the external process goes away, turning its
// frames into events and approvals.
func (e *externalSession) pump(ctx context.Context) {
	defer e.finish()
	for {
		m, err := e.stream.Recv()
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				e.d.log.Error("external stream", "session", e.id, "kind", e.req.Kind, "error", err)
			}
			return
		}
		switch m.Type {
		case control.MsgEvent:
			if m.Event == nil {
				continue
			}
			ev := *m.Event
			ev.Session = e.id
			if e.req.Kind == control.KindProxy {
				e.proxyEvent(ctx, &ev)
			}
			select {
			case e.events <- ev:
			case <-ctx.Done():
				return
			}
		case control.MsgApprovalRequest:
			go e.approve(ctx, m)
		case control.MsgApprovalCancel:
			e.withdraw(m.ReqID)
		case control.MsgDetach:
			return
		default:
			e.d.log.Debug("external stream: unknown frame", "session", e.id, "type", m.Type)
		}
	}
}

// The two conventions ferry proxy speaks through plain events (spec-p3 3.6).
const (
	// editorTurnKey is the chunk the proxy mirrors when the editor starts a
	// turn: the i18n key itself, which the daemon renders in the owner's
	// language.
	editorTurnKey = "proxy.editor_turn"
	// statusAwaitingEditor marks a tool whose permission request went to the
	// editor; chat is told it is waiting there.
	statusAwaitingEditor = "awaiting_editor"
)

// proxyEvent applies those conventions to one mirrored event: the turn marker
// is localised in place, and a tool waiting on the editor also sends the
// proxy.editor_approval notice.
func (e *externalSession) proxyEvent(ctx context.Context, ev *fsp.Event) {
	switch {
	case ev.Kind == fsp.EvChunk && ev.Text == editorTurnKey:
		if rec, ok := e.d.record(ctx, e.id); ok {
			ev.Text = e.d.t(ctx, rec.Owner, editorTurnKey, nil) + "\n"
		}
	case ev.Kind == fsp.EvTool && ev.Tool != nil && ev.Tool.Status == statusAwaitingEditor:
		if rec, ok := e.d.record(ctx, e.id); ok {
			e.d.reply(ctx, rec.Owner, "proxy.editor_approval", map[string]any{"Tool": ev.Tool.Title})
		}
	}
}

// approve routes one permission request. Where it goes is settled at attach
// time: remote_approve sends it to chat, anything else stays where the
// operator already is — the editor, or the terminal the program runs in.
func (e *externalSession) approve(ctx context.Context, m control.Message) {
	if m.Approval == nil {
		return
	}
	rec, ok := e.d.record(ctx, e.id)
	if !ok {
		return
	}
	if !e.req.RemoteApprove {
		e.d.noticeLocalApproval(ctx, rec.Owner, e.req.Kind, m)
		return
	}

	askCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		cancel()
		return
	}
	e.asks[m.ReqID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.asks, m.ReqID)
		e.mu.Unlock()
		cancel()
	}()

	req := *m.Approval
	req.Session = e.id
	v, err := sessionApprover{d: e.d, id: e.id}.Ask(askCtx, req)
	if err != nil {
		return
	}
	// The request ID belongs to the external process; it travels back
	// untouched so that it can match the verdict to what it asked.
	if err := e.send(control.Message{
		Type:      control.MsgVerdict,
		SessionID: string(e.id),
		ReqID:     m.ReqID,
		Verdict:   &v,
	}); err != nil {
		e.d.log.Error("send a verdict", "session", e.id, "error", err)
	}
}

// noticeLocalApproval says in chat that something is waiting, and where to
// answer it: in the editor for a proxied session, on the computer for a
// wrapped terminal program, which has nowhere else to ask.
func (d *Daemon) noticeLocalApproval(ctx context.Context, ep fsp.EndpointID, kind string, m control.Message) {
	if kind == control.KindPTY {
		d.reply(ctx, ep, "pty.no_approval", nil)
		return
	}
	tool := ""
	if m.Approval != nil {
		tool = m.Approval.ToolName
	}
	d.reply(ctx, ep, "proxy.editor_approval", map[string]any{"Tool": tool})
}

// withdraw cancels the approval the external process no longer wants an
// answer to.
func (e *externalSession) withdraw(reqID string) {
	e.mu.Lock()
	cancel := e.asks[reqID]
	delete(e.asks, reqID)
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var _ ports.AgentSession = (*externalSession)(nil)

// closeExternal ends a session whose process went away. Unlike a managed
// session it is not parked: there is nothing here to resume, the agent lived
// in the other process.
func (s *session) closeExternal(ctx context.Context) {
	s.mu.Lock()
	if s.rec.State == fsp.StateClosed {
		s.mu.Unlock()
		return
	}
	kind := s.external.req.Kind
	s.as = nil
	s.pending = nil
	s.buf.Reset()
	s.rec.State = fsp.StateClosed
	s.rec.LastActive = s.d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()

	s.d.persist(ctx, rec)
	if err := s.d.deps.Store.Unbind(ctx, rec.Owner, rec.ID); err != nil {
		s.d.log.Error("unbind an external session", "session", rec.ID, "error", err)
	}
	s.d.forget(rec.ID)
	s.d.deps.Approvals.CancelSession(rec.ID)
	if !s.d.stopping() {
		s.d.send(ctx, fsp.Outbound{
			Endpoint: rec.Owner,
			Session:  rec.ID,
			Kind:     fsp.OutText,
			Text:     s.d.t(ctx, rec.Owner, "session.external_closed", map[string]any{"Slot": s.slot, "Kind": kind}),
			Urgent:   true,
		})
	}
	s.d.audit(ctx, ports.AuditEntry{
		Kind:     "session_closed",
		Endpoint: rec.Owner,
		Session:  rec.ID,
		Detail:   kind + " detached",
	})
	if stop := s.external.stop; stop != nil {
		stop()
	}
}
