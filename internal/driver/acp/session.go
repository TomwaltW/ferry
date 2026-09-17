package acp

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// modeKey is the SetConfig key the core reserves for an agent's session mode.
const modeKey = "mode"

// session is one agent subprocess and the ACP conversation running over it. It
// is also the acp.Client the agent calls back into, which is why the permission
// and file system methods live here.
type session struct {
	log        *slog.Logger
	approver   ports.Approver
	proc       *process
	conn       *acp.ClientSideConnection
	events     *queue
	closeGrace time.Duration

	// baseCtx bounds everything the session does of its own accord. It ends
	// when the session is closed, never when a caller's context does.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	id      acp.SessionId
	caps    ports.Caps
	configs []acp.SessionConfigOption
	modes   *acp.SessionModeState

	mu sync.Mutex
	// loading is set while session/load replays history: those updates
	// describe the past and must not reach the core as new events.
	loading bool
	busy    bool
	// turnCtx is the context the approval requests of the turn in flight run
	// under, and cancelApprovals is what ends it.
	turnCtx         context.Context
	cancelApprovals context.CancelFunc
	// closing records that the process is being shut down on purpose, so its
	// exit is not reported as a failure.
	closing bool
	closed  bool
}

var (
	_ ports.AgentSession = (*session)(nil)
	_ acp.Client         = (*session)(nil)
)

func (s *session) AgentSessionID() string { return string(s.id) }
func (s *session) Caps() ports.Caps       { return s.caps }
func (s *session) Events() <-chan fsp.Event {
	return s.events.out
}

// Prompt starts a turn and returns at once; everything the agent says comes
// back through Events.
func (s *session) Prompt(ctx context.Context, text string) error {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return fmt.Errorf("acp: session %s is closed", s.id)
	case s.busy:
		s.mu.Unlock()
		return fsp.ErrBusy
	}
	s.busy = true
	turnCtx, cancel := context.WithCancel(s.baseCtx)
	s.turnCtx, s.cancelApprovals = turnCtx, cancel
	s.mu.Unlock()

	go s.runTurn(text, cancel)
	return nil
}

// runTurn drives one session/prompt to its end and reports how it finished. The
// prompt itself rides on baseCtx rather than on the caller's context, which is
// already gone by the time the turn runs.
func (s *session) runTurn(text string, done context.CancelFunc) {
	defer done()

	resp, err := s.conn.Prompt(s.baseCtx, acp.PromptRequest{
		SessionId: s.id,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})

	s.mu.Lock()
	s.busy = false
	s.turnCtx, s.cancelApprovals = nil, nil
	s.mu.Unlock()

	// A turn that failed still ended, so the core hears about it as a turn end
	// with the error stop reason. EvError is kept for the process dying.
	if err != nil {
		s.events.push(fsp.Event{
			Kind:       fsp.EvTurnEnd,
			StopReason: "error",
			Err:        err.Error(),
			At:         time.Now(),
		})
		return
	}
	s.events.push(fsp.Event{
		Kind:       fsp.EvTurnEnd,
		StopReason: string(resp.StopReason),
		At:         time.Now(),
	})
}

// Cancel asks the agent to stop the current turn and releases any approval it
// is waiting on.
func (s *session) Cancel(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancelApprovals
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.conn.Cancel(ctx, acp.CancelNotification{SessionId: s.id})
}

// SetConfig sets one session option. The reserved key "mode" maps onto the
// agent's session mode; anything else has to name a session config option the
// agent offered when the session was created.
func (s *session) SetConfig(ctx context.Context, key, value string) error {
	if key == modeKey {
		return s.setMode(ctx, value)
	}

	for _, opt := range s.configs {
		switch {
		case opt.Boolean != nil && string(opt.Boolean.Id) == key:
			on, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("acp: %s wants true or false, not %q", key, value)
			}
			_, err = s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
				Boolean: &acp.SetSessionConfigOptionBoolean{
					SessionId: s.id,
					ConfigId:  opt.Boolean.Id,
					Value:     on,
				},
			})
			return err

		case opt.Select != nil && string(opt.Select.Id) == key:
			_, err := s.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
				ValueId: &acp.SetSessionConfigOptionValueId{
					SessionId: s.id,
					ConfigId:  opt.Select.Id,
					Value:     acp.SessionConfigValueId(value),
				},
			})
			return err
		}
	}
	return fmt.Errorf("acp: this agent has no %q option: %w", key, fsp.ErrUnsupported)
}

func (s *session) setMode(ctx context.Context, value string) error {
	if s.modes == nil {
		return fmt.Errorf("acp: this agent has no session modes: %w", fsp.ErrUnsupported)
	}
	known := false
	for _, m := range s.modes.AvailableModes {
		if string(m.Id) == value {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("acp: this agent has no %q mode: %w", value, fsp.ErrUnsupported)
	}

	_, err := s.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: s.id,
		ModeId:    acp.SessionModeId(value),
	})
	return err
}

// Close ends the session: the current turn is cancelled, the agent is given
// closeGrace to exit on its own, and then it is killed.
func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.closing = true
	cancel := s.cancelApprovals
	s.turnCtx, s.cancelApprovals = nil, nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	ctx, stop := context.WithTimeout(context.Background(), s.closeGrace)
	if err := s.conn.Cancel(ctx, acp.CancelNotification{SessionId: s.id}); err != nil {
		s.log.Debug("could not cancel the turn while closing", "err", err)
	}
	stop()

	// Closing stdin is how a stdio agent is told to stop; the grace period is
	// for finishing whatever it was in the middle of.
	s.proc.endInput()
	select {
	case <-s.proc.done:
	case <-time.After(s.closeGrace):
		s.log.Debug("agent did not exit in time, killing it", "session", string(s.id))
		s.proc.kill()
		<-s.proc.done
	}

	s.baseCancel()
	s.events.close()
	return nil
}

// shutdown tears down a connection that never became a session, such as one
// opened only to list an agent's sessions or one whose startup failed.
func (s *session) shutdown() {
	s.mu.Lock()
	s.closed = true
	s.closing = true
	s.mu.Unlock()

	s.proc.endInput()
	select {
	case <-s.proc.done:
	case <-time.After(s.closeGrace):
		s.proc.kill()
		<-s.proc.done
	}
	s.baseCancel()
	s.events.close()
}

// watch reports the agent's exit. An exit nobody asked for is a failure the
// core has to hear about before the event channel goes quiet.
func (s *session) watch() {
	<-s.proc.done

	s.mu.Lock()
	expected := s.closing
	s.mu.Unlock()
	if !expected {
		detail := "the agent exited"
		if s.proc.err != nil {
			detail = "the agent exited: " + s.proc.err.Error()
		}
		s.events.push(fsp.Event{Kind: fsp.EvError, Err: detail, At: time.Now()})
	}
	s.events.close()
}

// ---------- acp.Client ----------

// SessionUpdate receives everything the agent streams. Updates that arrive
// while session/load is replaying history are dropped: they are a transcript of
// a past conversation, not news.
func (s *session) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	s.mu.Lock()
	loading := s.loading
	s.mu.Unlock()
	if loading {
		return nil
	}

	if e, ok := mapUpdate(params.Update, time.Now()); ok {
		s.events.push(e)
	}
	return nil
}

// RequestPermission hands the agent's question to the approver and answers with
// whatever comes back. A verdict with no option, an approver that fails and a
// cancelled turn all answer cancelled.
func (s *session) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	if s.approver == nil {
		return cancelled, nil
	}

	s.mu.Lock()
	turn := s.turnCtx
	s.mu.Unlock()

	askCtx, stop := mergeCancel(ctx, turn)
	defer stop()

	v, err := s.approver.Ask(askCtx, approvalRequest(params, time.Now()))
	if err != nil {
		s.log.Debug("the approver did not answer", "err", err)
		return cancelled, nil
	}
	if v.OptionID == "" {
		return cancelled, nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(v.OptionID)),
	}, nil
}

// The file system and terminal methods are never reachable: initialize does not
// declare either capability, so an agent that calls them is out of contract.

func (s *session) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (s *session) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (s *session) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (s *session) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}

func (s *session) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (s *session) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}

func (s *session) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}

// mergeCancel returns a context that ends when either parent does.
func mergeCancel(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	if b == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(b, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
