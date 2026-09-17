package core

import (
	"context"
	"errors"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/ports"
)

// terminalTTL is how long the numbers /ls handed out for terminal sessions stay
// valid, per spec-p2 3.8.
const terminalTTL = time.Minute

// terminalList is what one /ls told an endpoint about the terminal sessions it
// could take over, keyed by the numbers the user saw.
type terminalList struct {
	at    time.Time
	items map[int]ports.TerminalSession
}

// terminalSessions asks the handoff module what the terminals are holding. A
// module that is not wired up, or that fails, simply contributes nothing: /ls
// still has to list the sessions Ferry owns.
func (d *Daemon) terminalSessions(ctx context.Context, ep fsp.EndpointID) []ports.TerminalSession {
	if d.deps.Handoff == nil {
		return nil
	}
	found, err := d.deps.Handoff.Discover(ctx, ep)
	if err != nil {
		if !errors.Is(err, fsp.ErrUnsupported) {
			d.log.Warn("discover terminal sessions", "endpoint", ep.String(), "error", err)
		}
		return nil
	}
	return found
}

// rememberTerminals records the numbers /ls printed so a later /use can resolve
// them.
func (d *Daemon) rememberTerminals(ep fsp.EndpointID, items map[int]ports.TerminalSession) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.terminals[ep.String()] = terminalList{at: d.deps.Clock.Now(), items: items}
}

// terminalBySlot resolves a number from the last /ls, as long as it is less
// than a minute old.
func (d *Daemon) terminalBySlot(ep fsp.EndpointID, n int) (ports.TerminalSession, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list, ok := d.terminals[ep.String()]
	if !ok || d.deps.Clock.Now().Sub(list.at) >= terminalTTL {
		return ports.TerminalSession{}, false
	}
	ts, ok := list.items[n]
	return ts, ok
}

// forgetTerminals drops the cached numbering, which an adoption invalidates:
// the session that was on the list now belongs to Ferry.
func (d *Daemon) forgetTerminals(ep fsp.EndpointID) {
	d.mu.Lock()
	delete(d.terminals, ep.String())
	d.mu.Unlock()
}

// adoptTerminal takes over a session that was started in a terminal. Unless the
// session is shared, or the user insisted with force, the terminal has to have
// let go of it first.
func (d *Daemon) adoptTerminal(ctx context.Context, ep fsp.EndpointID, ts ports.TerminalSession, slot int, force bool) {
	if !ts.Shared && !force {
		busy, err := d.deps.Handoff.Busy(ctx, ts)
		switch {
		case err != nil:
			d.log.Debug("terminal busy check", "agent", ts.Agent, "error", err)
			d.reply(ctx, ep, "handoff.busy_unknown", map[string]any{"Slot": slot})
			return
		case busy:
			d.reply(ctx, ep, "handoff.busy", map[string]any{"Slot": slot, "Agent": ts.Agent, "Cwd": ts.Cwd})
			return
		}
	}

	spec, err := d.deps.Registry.Resolve(ctx, ts.Agent)
	if err != nil {
		if errors.Is(err, fsp.ErrNotFound) {
			d.reply(ctx, ep, "agent.unknown", map[string]any{"Name": ts.Agent})
			return
		}
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": ts.Agent, "Reason": err.Error()})
		return
	}
	if d.countManaged() >= d.maxSessions() {
		d.reply(ctx, ep, "session.limit", map[string]any{"Max": d.maxSessions()})
		return
	}
	dr, ok := d.driverFor(spec)
	if !ok {
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": ts.Agent, "Reason": noDriver(spec)})
		return
	}
	spec.Cwd = ts.Cwd

	now := d.deps.Clock.Now()
	id := fsp.SessionID(ids.New("sess"))
	s := &session{
		d:     d,
		tools: make(map[string]bool),
		rec: fsp.Session{
			ID:             id,
			Driver:         dr.Kind(),
			Agent:          ts.Agent,
			Cwd:            ts.Cwd,
			AgentSessionID: ts.AgentSessionID,
			Origin:         fsp.OriginTerminal,
			State:          fsp.StateStarting,
			Owner:          ep,
			CreatedAt:      now,
			LastActive:     now,
		},
	}
	// Registered and bound before the driver runs, so a permission request
	// raised while the history replays already finds the session.
	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()
	d.persist(ctx, s.snapshot())

	bound, err := d.deps.Store.Bind(ctx, ep, id)
	if err != nil {
		d.log.Error("bind adopted session", "session", id, "error", err)
	}
	s.slot = bound

	as, err := dr.Resume(ctx, spec, ts.AgentSessionID, sessionApprover{d: d, id: id})
	if err != nil {
		d.forget(id)
		if uerr := d.deps.Store.Unbind(ctx, ep, id); uerr != nil {
			d.log.Error("unbind adopted session", "session", id, "error", uerr)
		}
		d.reply(ctx, ep, "session.start_failed", map[string]any{"Agent": ts.Agent, "Reason": err.Error()})
		return
	}

	s.mu.Lock()
	s.as = as
	s.rec.AgentSessionID = as.AgentSessionID()
	s.rec.State = fsp.StateIdle
	s.rec.LastActive = d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()

	d.persist(ctx, rec)
	if err := d.deps.Store.SetForeground(ctx, ep, id); err != nil {
		d.log.Error("set foreground", "session", id, "error", err)
	}
	d.forgetTerminals(ep)
	d.audit(ctx, ports.AuditEntry{Kind: "session_started", Endpoint: ep, Session: id, Detail: "adopted from terminal"})
	s.start(ctx)
	d.reply(ctx, ep, "handoff.adopted", map[string]any{"Title": ts.Title, "Slot": s.slot, "Agent": ts.Agent})
}

// ownerEndpoint is the conversation of whoever scanned the QR code, which is
// the endpoint the CLI and the wake channels speak to.
func (d *Daemon) ownerEndpoint() (fsp.EndpointID, bool) {
	st := d.deps.IM.Status()
	if st.BotID == "" || st.OwnerID == "" {
		return fsp.EndpointID{}, false
	}
	return fsp.EndpointID{Platform: d.deps.IM.Platform(), BotID: st.BotID, PeerID: st.OwnerID}, true
}

// wake nudges the operator outside chat. A daemon without a wake channel stays
// silent rather than failing.
func (d *Daemon) wake(ctx context.Context, kind string, args map[string]any) {
	if d.deps.Waker == nil {
		return
	}
	ep, ok := d.ownerEndpoint()
	if !ok {
		return
	}
	d.deps.Waker.Wake(ctx, ep, kind, args)
}

// parkTarget finds the session ferry back is about: the one at slot n, or else
// the foreground one. The owner endpoint comes first, as spec-p2 3.4 says, and
// the other endpoints that hold sessions follow, so a bot whose owner talks to
// it in a group still has something to hand back.
func (d *Daemon) parkTarget(ctx context.Context, slot int) *session {
	var endpoints []fsp.EndpointID
	seen := make(map[string]bool)
	add := func(ep fsp.EndpointID) {
		if !seen[ep.String()] {
			seen[ep.String()] = true
			endpoints = append(endpoints, ep)
		}
	}
	if ep, ok := d.ownerEndpoint(); ok {
		add(ep)
	}
	d.mu.Lock()
	for _, s := range d.sessions {
		add(s.snapshot().Owner)
	}
	d.mu.Unlock()

	for _, ep := range endpoints {
		if slot > 0 {
			if s := d.bySlot(ctx, ep, slot); s != nil {
				return s
			}
			continue
		}
		if s := d.foreground(ctx, ep); s != nil {
			return s
		}
	}
	return nil
}

// ControlHandler is the daemon behind the local control socket. It is a
// separate type because Daemon.Status already means something else.
func (d *Daemon) ControlHandler() control.Handler { return controlHandler{d: d} }

type controlHandler struct{ d *Daemon }

var _ control.Handler = controlHandler{}

func (c controlHandler) Status(ctx context.Context) (control.StatusResponse, error) {
	st := c.d.Status()
	return control.StatusResponse{
		IMOnline: st.IMOnline,
		BotID:    st.BotID,
		Sessions: st.Sessions,
		Running:  st.Running,
	}, nil
}

// Park hands a session back to the terminal: the agent process goes away, the
// session is parked, and the caller gets the command that resumes it.
func (c controlHandler) Park(ctx context.Context, req control.ParkRequest) (control.ParkResponse, error) {
	d := c.d
	s := d.parkTarget(ctx, req.Slot)
	if s == nil {
		return control.ParkResponse{}, fsp.ErrNotFound
	}
	rec := s.snapshot()
	s.parkWith(ctx, "handoff.parked_by_back", "handed back to the terminal")

	resp := control.ParkResponse{Agent: rec.Agent, AgentSessionID: rec.AgentSessionID, Cwd: rec.Cwd}
	if d.deps.Handoff == nil {
		return resp, nil
	}
	cmd, err := d.deps.Handoff.BackCommand(rec.Agent, rec.AgentSessionID)
	if err != nil {
		if !errors.Is(err, fsp.ErrUnsupported) {
			d.log.Error("back command", "agent", rec.Agent, "error", err)
		}
		return resp, nil
	}
	resp.BackCommand = cmd
	return resp, nil
}
