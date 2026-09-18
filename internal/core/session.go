package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// session is one managed agent session plus the state of its current turn.
// A parked session keeps its record but has no agent behind it.
type session struct {
	d    *Daemon
	slot int
	// external is set when the agent runs in another process, reached over the
	// control socket rather than through a driver (spec-p3 3.3).
	external *externalSession

	mu      sync.Mutex
	rec     fsp.Session
	as      ports.AgentSession
	buf     strings.Builder // output of the current turn, not yet sent
	tools   map[string]bool // tool call IDs already announced this turn
	pending []string        // text that arrived while the session was busy
	closing bool            // /close is in progress, do not park on the way out
}

func (s *session) id() fsp.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.ID
}

func (s *session) agent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Agent
}

func (s *session) State() fsp.SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.State
}

func (s *session) snapshot() fsp.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec
}

func (s *session) live() ports.AgentSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.as
}

func (s *session) setState(ctx context.Context, state fsp.SessionState) {
	s.mu.Lock()
	s.rec.State = state
	s.rec.LastActive = s.d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()
	s.d.persist(ctx, rec)
}

func (d *Daemon) persist(ctx context.Context, rec fsp.Session) {
	if err := d.deps.Store.PutSession(ctx, rec); err != nil {
		d.log.Error("put session", "session", rec.ID, "error", err)
	}
}

func (d *Daemon) mergeInterval() time.Duration {
	iv := d.deps.Config.Outbox.MergeInterval.Duration
	if iv <= 0 {
		iv = 30 * time.Second
	}
	return iv
}

// submit starts a turn with text, or queues it when the session is busy. It
// reports whether the agent was prompted right away.
func (s *session) submit(ctx context.Context, text string) bool {
	s.mu.Lock()
	as := s.as
	busy := s.rec.State == fsp.StateRunning || s.rec.State == fsp.StateAwaiting
	if as == nil || busy {
		s.pending = append(s.pending, text)
		s.mu.Unlock()
		return false
	}
	s.rec.State = fsp.StateRunning
	s.rec.LastActive = s.d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()

	if err := as.Prompt(ctx, text); err != nil {
		if errors.Is(err, fsp.ErrBusy) {
			s.mu.Lock()
			s.pending = append(s.pending, text)
			s.mu.Unlock()
			return false
		}
		s.d.log.Error("prompt", "session", rec.ID, "error", err)
		s.setState(ctx, fsp.StateIdle)
		s.d.send(ctx, fsp.Outbound{
			Endpoint: rec.Owner,
			Session:  rec.ID,
			Kind:     fsp.OutText,
			Text:     s.d.t(ctx, rec.Owner, "turn.failed", map[string]any{"Agent": rec.Agent, "Reason": err.Error()}),
			Urgent:   true,
		})
		return false
	}
	s.d.persist(ctx, rec)
	return true
}

// start launches the event loop of a live session.
func (s *session) start(ctx context.Context) {
	s.d.wg.Add(1)
	go func() {
		defer s.d.wg.Done()
		s.run(ctx)
	}()
}

func (s *session) run(ctx context.Context) {
	as := s.live()
	if as == nil {
		return
	}
	events := as.Events()
	ticker := s.d.deps.Clock.NewTicker(s.d.mergeInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			s.flush(ctx, false)
		case ev, ok := <-events:
			if !ok {
				s.gone(ctx)
				return
			}
			s.handleEvent(ctx, ev)
		}
	}
}

// gone is what happens when the agent behind a session disappears: a managed
// one is parked so that /use can bring it back, an external one is closed,
// because its agent lived in a process Ferry does not own.
func (s *session) gone(ctx context.Context) {
	if s.external != nil {
		s.closeExternal(ctx)
		return
	}
	s.park(ctx)
}

// allowsInject reports whether chat input reaches this session. Only external
// sessions can refuse it; a managed session exists to be written to.
func (s *session) allowsInject() bool {
	return s.external == nil || s.external.req.AllowInject
}

func (s *session) handleEvent(ctx context.Context, ev fsp.Event) {
	switch ev.Kind {
	case fsp.EvChunk:
		s.mu.Lock()
		s.buf.WriteString(ev.Text)
		s.mu.Unlock()
	case fsp.EvThought:
		// Reasoning never reaches the chat.
	case fsp.EvTool:
		if ev.Tool == nil {
			return
		}
		s.mu.Lock()
		if s.tools == nil {
			s.tools = make(map[string]bool)
		}
		first := !s.tools[ev.Tool.CallID]
		s.tools[ev.Tool.CallID] = true
		if first {
			s.appendLineLocked("▸ " + ev.Tool.Title)
		}
		s.mu.Unlock()
	case fsp.EvPlan:
		rec := s.snapshot()
		header := s.d.t(ctx, rec.Owner, "plan.header", nil)
		s.mu.Lock()
		s.appendLineLocked(header)
		for i, step := range ev.Plan {
			s.appendLineLocked(fmt.Sprintf("%d. %s", i+1, step.Title))
		}
		s.mu.Unlock()
	case fsp.EvTurnEnd:
		s.endTurn(ctx, ev)
	case fsp.EvError:
		rec := s.snapshot()
		s.d.send(ctx, fsp.Outbound{
			Endpoint: rec.Owner,
			Session:  rec.ID,
			Kind:     fsp.OutText,
			Text:     s.d.t(ctx, rec.Owner, "turn.failed", map[string]any{"Agent": rec.Agent, "Reason": ev.Err}),
			Urgent:   true,
		})
	}
}

func (s *session) endTurn(ctx context.Context, ev fsp.Event) {
	s.flush(ctx, true)

	rec := s.snapshot()
	if ev.StopReason == "cancelled" {
		s.d.send(ctx, fsp.Outbound{
			Endpoint: rec.Owner,
			Session:  rec.ID,
			Kind:     fsp.OutText,
			Text:     s.d.t(ctx, rec.Owner, "turn.stopped", nil),
			Urgent:   true,
		})
	}
	s.d.typing(ctx, rec.Owner, false)

	s.mu.Lock()
	s.tools = make(map[string]bool)
	queued := s.pending
	s.pending = nil
	as := s.as
	state := fsp.SessionState(fsp.StateIdle)
	if len(queued) != 0 && as != nil {
		state = fsp.StateRunning
	}
	s.rec.State = state
	s.rec.LastActive = s.d.deps.Clock.Now()
	rec = s.rec
	s.mu.Unlock()
	s.d.persist(ctx, rec)

	if len(queued) == 0 || as == nil {
		return
	}
	// Everything that arrived while the agent was busy goes over as one turn.
	if err := as.Prompt(ctx, strings.Join(queued, "\n\n")); err != nil {
		s.d.log.Error("prompt queued text", "session", rec.ID, "error", err)
		s.setState(ctx, fsp.StateIdle)
	}
}

// appendLineLocked adds a whole line to the turn buffer. The caller holds s.mu.
func (s *session) appendLineLocked(line string) {
	if s.buf.Len() != 0 && !strings.HasSuffix(s.buf.String(), "\n") {
		s.buf.WriteString("\n")
	}
	s.buf.WriteString(line)
	s.buf.WriteString("\n")
}

// flush queues whatever the turn has produced so far and empties the buffer.
func (s *session) flush(ctx context.Context, urgent bool) {
	s.mu.Lock()
	text := strings.TrimRight(s.buf.String(), "\n")
	s.buf.Reset()
	rec := s.rec
	s.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return
	}
	s.d.send(ctx, fsp.Outbound{
		Endpoint: rec.Owner,
		Session:  rec.ID,
		Kind:     fsp.OutText,
		Text:     text,
		Urgent:   urgent,
	})
}

// park drops the agent behind the session and tells the endpoint how to get it
// back. It is called when the agent process is gone and when the daemon stops.
func (s *session) park(ctx context.Context) {
	s.parkWith(ctx, "session.parked", "agent gone")
}

// parkWith is park with the wording and the audit detail of whatever asked for
// it: the agent going away reads differently from ferry back taking it home.
func (s *session) parkWith(ctx context.Context, key, detail string) {
	s.mu.Lock()
	if s.closing || s.rec.State == fsp.StateParked || s.rec.State == fsp.StateClosed {
		s.mu.Unlock()
		return
	}
	as := s.as
	s.as = nil
	s.pending = nil
	s.buf.Reset()
	s.rec.State = fsp.StateParked
	s.rec.LastActive = s.d.deps.Clock.Now()
	rec := s.rec
	s.mu.Unlock()

	if as != nil {
		if err := as.Close(); err != nil {
			s.d.log.Debug("close parked session", "session", rec.ID, "error", err)
		}
	}
	s.d.persist(ctx, rec)
	s.d.deps.Approvals.CancelSession(rec.ID)
	if !s.d.stopping() {
		s.d.send(ctx, fsp.Outbound{
			Endpoint: rec.Owner,
			Session:  rec.ID,
			Kind:     fsp.OutText,
			Text:     s.d.t(ctx, rec.Owner, key, map[string]any{"Slot": s.slot}),
			Urgent:   true,
		})
	}
	s.d.audit(ctx, ports.AuditEntry{
		Kind:     "session_parked",
		Endpoint: rec.Owner,
		Session:  rec.ID,
		Detail:   detail,
	})
}

// sessionApprover is the Approver handed to a driver: it labels the request
// with the Ferry session and parks the session in awaiting while the human
// thinks.
type sessionApprover struct {
	d  *Daemon
	id fsp.SessionID
}

func (a sessionApprover) Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	req.Session = a.id
	if s := a.d.lookup(a.id); s != nil {
		s.setState(ctx, fsp.StateAwaiting)
	}
	v, err := a.d.deps.Approvals.Ask(ctx, req)
	if s := a.d.lookup(a.id); s != nil {
		s.setState(ctx, fsp.StateRunning)
	}
	return v, err
}
