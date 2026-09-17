package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// backend is everything that differs between the two services. The session
// above it owns the state machine; the backend only knows routes and payloads.
type backend interface {
	// kind is the service name, for error messages.
	kind() string
	// health reports whether the service answers at all.
	health(ctx context.Context) error
	// create opens a new session and returns the service's ID for it.
	create(ctx context.Context, spec ports.SessionSpec) (string, error)
	// attach makes an existing session drivable again and returns its ID.
	attach(ctx context.Context, spec ports.SessionSpec, agentSessionID string) (string, error)
	// list enumerates the sessions the service already has.
	list(ctx context.Context) ([]ports.AgentSessionInfo, error)
	// canList reports whether list does anything but refuse.
	canList() bool
	// openEvents starts the event stream carrying this session.
	openEvents(ctx context.Context, id string) (io.ReadCloser, error)
	// decode turns one frame into what the session should do about it,
	// dropping everything addressed to another session.
	decode(id string, ev sseEvent) []action
	// resync asks what the session is doing, after a reconnect has lost
	// whatever happened while the stream was down.
	resync(ctx context.Context, id string) (sessionStatus, error)
	// prompt starts a turn.
	prompt(ctx context.Context, id, text string) error
	// cancel stops the turn in flight.
	cancel(ctx context.Context, id string) error
	// reply answers a permission request.
	reply(ctx context.Context, id, requestID string, v fsp.Verdict) error
}

// sessionStatus is what a service says about a session when asked directly.
type sessionStatus struct {
	Alive   bool
	Running bool
}

// askAction is a permission the service is waiting on: requestID is how the
// answer is addressed, req is what the user is shown.
type askAction struct {
	requestID string
	req       fsp.ApprovalRequest
}

// action is one thing to do about a decoded frame. Exactly one field is set.
type action struct {
	event *fsp.Event
	ask   *askAction
	// resolved is the request ID of a permission somebody else answered, which
	// happens whenever a shared session is also open in a terminal.
	resolved string
}

// session is one session on a local service, plus the event stream that
// reports on it.
type session struct {
	be  backend
	id  string
	ap  ports.Approver
	q   *queue
	clk clock.Clock
	log *slog.Logger

	// baseCtx bounds the event stream and every approval waiting on an answer.
	// It ends when the session is closed, never when a caller's context does.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu   sync.Mutex
	busy bool
	// parked records that the session has no working event stream any more:
	// prompting it would go unanswered.
	parked bool
	closed bool
	// pending are the approvals waiting on an answer, by the service's request
	// ID, so that one answered elsewhere can be taken off the screen.
	pending map[string]context.CancelFunc
}

var _ ports.AgentSession = (*session)(nil)

func (s *session) AgentSessionID() string { return s.id }

// Caps reports what the service can do. Neither service exposes session
// options, so Config is off and SetConfig refuses.
func (s *session) Caps() ports.Caps {
	return ports.Caps{Load: true, Resume: true, List: s.be.canList(), Cancel: true}
}

func (s *session) Events() <-chan fsp.Event { return s.q.out }

// Prompt starts a turn and returns at once; the answer arrives on the event
// stream, which is a separate connection.
func (s *session) Prompt(ctx context.Context, text string) error {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return fmt.Errorf("server: session %s is closed", s.id)
	case s.parked:
		s.mu.Unlock()
		return fmt.Errorf("server: session %s has lost its event stream", s.id)
	case s.busy:
		s.mu.Unlock()
		return fsp.ErrBusy
	}
	s.busy = true
	s.mu.Unlock()

	if err := s.be.prompt(ctx, s.id, text); err != nil {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		return err
	}
	return nil
}

// Cancel asks the service to stop the turn. The turn end still arrives on the
// event stream, so nothing is reported from here.
func (s *session) Cancel(ctx context.Context) error {
	return s.be.cancel(ctx, s.id)
}

// SetConfig has nothing to set: neither service offers session options over
// its HTTP API.
func (s *session) SetConfig(ctx context.Context, key, value string) error {
	return fmt.Errorf("server: %s has no %q option: %w", s.be.kind(), key, fsp.ErrUnsupported)
}

// Close disconnects the event stream and releases whatever was waiting on an
// approval. The session on the service is left alone: a second-tier session
// belongs to the terminal just as much as to Ferry.
func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()

	for _, cancel := range pending {
		cancel()
	}
	s.baseCancel()
	s.q.close()
	return nil
}

// run keeps the event stream up for the life of the session.
func (s *session) run(first io.ReadCloser) {
	st := &streamer{
		clk:         s.clk,
		log:         s.log,
		open:        func(ctx context.Context) (io.ReadCloser, error) { return s.be.openEvents(ctx, s.id) },
		onEvent:     s.handle,
		onReconnect: s.resync,
		onGiveUp:    s.giveUp,
	}
	st.run(s.baseCtx, first)
}

// handle applies one frame. It runs on the stream's own goroutine, so the work
// it does has to be short; asking a user for an approval is not, and goes to a
// goroutine of its own.
func (s *session) handle(ev sseEvent) {
	for _, a := range s.be.decode(s.id, ev) {
		switch {
		case a.resolved != "":
			s.dropPending(a.resolved)

		case a.ask != nil:
			s.ask(*a.ask)

		case a.event != nil:
			switch a.event.Kind {
			case fsp.EvTurnEnd:
				s.mu.Lock()
				s.busy = false
				s.mu.Unlock()
			case fsp.EvError:
				s.mu.Lock()
				s.busy, s.parked = false, true
				s.mu.Unlock()
			}
			s.q.push(*a.event)
		}
	}
}

// ask puts one permission in front of the user and answers the service with
// whatever comes back. A request that is already on screen is ignored: both
// services can re-announce a permission that is still open.
func (s *session) ask(a askAction) {
	if s.ap == nil {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if _, dup := s.pending[a.requestID]; dup {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	s.pending[a.requestID] = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()

		v, err := s.ap.Ask(ctx, a.req)

		s.mu.Lock()
		if s.pending != nil {
			delete(s.pending, a.requestID)
		}
		s.mu.Unlock()

		if err != nil {
			s.log.Debug("the approver did not answer", "session", s.id, "err", err)
			return
		}
		// A cancelled context means the permission was answered somewhere
		// else, or the session is closing; either way the service is not
		// waiting for Ferry any more.
		if ctx.Err() != nil {
			return
		}
		if err := s.be.reply(s.baseCtx, s.id, a.requestID, v); err != nil {
			s.log.Warn("could not answer the permission request", "session", s.id, "err", err)
		}
	}()
}

// dropPending releases an approval that was answered outside Ferry.
func (s *session) dropPending(requestID string) {
	s.mu.Lock()
	cancel := s.pending[requestID]
	delete(s.pending, requestID)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// resync catches up after a reconnect. A turn that finished while the stream
// was down would otherwise leave the session busy for good, so a service that
// says the session is idle ends the turn here.
func (s *session) resync(ctx context.Context) {
	st, err := s.be.resync(ctx, s.id)
	if err != nil {
		s.log.Debug("could not check the session after reconnecting", "session", s.id, "err", err)
		return
	}

	if !st.Alive {
		s.mu.Lock()
		s.busy, s.parked = false, true
		s.mu.Unlock()
		s.q.push(fsp.Event{
			Kind: fsp.EvError,
			Err:  fmt.Sprintf("%s no longer has session %s", s.be.kind(), s.id),
			At:   s.clk.Now(),
		})
		return
	}

	s.mu.Lock()
	missed := s.busy && !st.Running
	if missed {
		s.busy = false
	}
	s.mu.Unlock()

	if missed {
		s.q.push(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn", At: s.clk.Now()})
	}
}

// giveUp reports a service that stayed unreachable. The core parks the session
// on this; the session object stays usable so that Close still tidies up.
func (s *session) giveUp(err error) {
	s.mu.Lock()
	s.busy, s.parked = false, true
	s.mu.Unlock()

	s.q.push(fsp.Event{Kind: fsp.EvError, Err: err.Error(), At: s.clk.Now()})
	s.q.close()
}

// queue carries events from the stream goroutine to whoever reads Events. It
// grows without bound on purpose: blocking the stream would stall the whole
// connection, so a slow reader must not be able to wedge the session.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []fsp.Event
	closed bool
	out    chan fsp.Event
}

func newQueue() *queue {
	q := &queue{out: make(chan fsp.Event)}
	q.cond = sync.NewCond(&q.mu)
	go q.pump()
	return q
}

// push queues one event. Events pushed after close are dropped.
func (q *queue) push(e fsp.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, e)
	q.cond.Signal()
}

// close stops accepting events. Whatever is already queued is still delivered,
// and out closes once it has been. Calling close twice is fine.
func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.cond.Signal()
}

func (q *queue) pump() {
	defer close(q.out)
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 {
			q.mu.Unlock()
			return
		}
		e := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()

		q.out <- e
	}
}
