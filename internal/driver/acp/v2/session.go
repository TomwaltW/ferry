package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// session is one agent subprocess and the v2 conversation running over it. It
// is also the client the agent calls back into, which is why the permission
// handling lives here.
type session struct {
	log        *slog.Logger
	approver   ports.Approver
	proc       *process
	conn       *conn
	events     *queue
	closeGrace time.Duration

	// baseCtx bounds everything the session does of its own accord. It ends
	// when the session is closed, never when a caller's context does.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	id   string
	caps ports.Caps

	mu      sync.Mutex
	configs []ConfigOption
	// tools remembers what each tool call was last said to be. v2 permission
	// requests only name the call they are about, so its kind, input and paths
	// have to come from the announcement that went before.
	tools map[string]ToolCallRef
	// loading is set while a resume may still be replaying history: those
	// updates describe the past and must not reach the core as new events.
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

var _ ports.AgentSession = (*session)(nil)

func (s *session) AgentSessionID() string   { return s.id }
func (s *session) Caps() ports.Caps         { return s.caps }
func (s *session) Events() <-chan fsp.Event { return s.events.out }

// login completes the authentication the agent advertised, but only when it can
// be done without a person at the keyboard. Anything interactive is the user's
// job, so startup fails and Ferry says so.
func (s *session) login(ctx context.Context, agent string, methods []AuthMethod) error {
	if len(methods) == 0 {
		return nil
	}

	var chosen *AuthMethod
	for i := range methods {
		if methods[i].Type == AuthAgent {
			chosen = &methods[i]
			break
		}
	}
	if chosen == nil {
		return fmt.Errorf("acp/v2: %s only offers an interactive login: %w", agent, ErrLoginRequired)
	}

	if err := s.conn.call(ctx, MethodAuthLogin, AuthLoginParams{MethodID: chosen.MethodID}, nil); err != nil {
		return fmt.Errorf("acp/v2: %s: log in with %s: %v: %w", agent, chosen.MethodID, err, ErrLoginRequired)
	}
	return nil
}

// adoptConfig records the config options the agent offered, which is also what
// decides whether SetConfig has anything to work with. v2 answers every change
// with the complete list, so a nil one means "unchanged" rather than "none".
func (s *session) adoptConfig(options []ConfigOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if options != nil {
		s.configs = options
	}
	s.caps.Config = len(s.configs) > 0
}

// rememberTool records what an update said about a tool call. v2 patches a call
// field by field — an omitted field means "unchanged" — so only what the update
// actually carried is written down.
func (s *session) rememberTool(u SessionUpdate) {
	if u.ToolCallID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tools == nil {
		s.tools = map[string]ToolCallRef{}
	}

	call := s.tools[u.ToolCallID]
	call.ToolCallID = u.ToolCallID
	if title := toolTitle(u); title != "" {
		call.Title = title
	}
	if u.Kind != "" {
		call.Kind = u.Kind
	}
	if len(u.RawInput) > 0 {
		call.RawInput = u.RawInput
	}
	if len(u.Locations) > 0 {
		call.Locations = u.Locations
	}
	s.tools[u.ToolCallID] = call
}

// Prompt starts a turn and returns at once. Unlike v1, the agent answers
// session/prompt as soon as it has taken the input: the turn ends later, when
// the agent reports itself idle.
func (s *session) Prompt(ctx context.Context, text string) error {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return fmt.Errorf("acp/v2: session %s is closed", s.id)
	case s.busy:
		s.mu.Unlock()
		return fsp.ErrBusy
	}
	s.busy = true
	turnCtx, cancel := context.WithCancel(s.baseCtx)
	s.turnCtx, s.cancelApprovals = turnCtx, cancel
	s.mu.Unlock()

	go s.runTurn(text)
	return nil
}

// runTurn sends the prompt and leaves. A prompt the agent refused outright never
// gets a state_update, so that is the one case the turn ends here.
func (s *session) runTurn(text string) {
	err := s.conn.call(s.baseCtx, MethodSessionPrompt, PromptParams{
		SessionID: s.id,
		Prompt:    []ContentBlock{textBlock(text)},
	}, nil)
	if err != nil {
		s.endTurn("error", err.Error())
	}
}

// endTurn reports the end of the turn in flight, exactly once: whichever of the
// idle state_update and a refused prompt arrives first wins.
func (s *session) endTurn(stop, errText string) {
	s.mu.Lock()
	if !s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = false
	cancel := s.cancelApprovals
	s.turnCtx, s.cancelApprovals = nil, nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.events.push(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: stop, Err: errText, At: time.Now()})
}

// Cancel asks the agent to stop the current turn and releases any approval it is
// waiting on. The turn itself ends when the agent reports idle with the
// cancelled stop reason.
func (s *session) Cancel(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancelApprovals
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.conn.send(MethodSessionCancel, SessionIDParams{SessionID: s.id})
}

// SetConfig sets one session option. The core's reserved key "mode" needs no
// special case: v2 dropped session/set_mode, and the mode is a config option
// whose id is exactly that.
func (s *session) SetConfig(ctx context.Context, key, value string) error {
	s.mu.Lock()
	options := append([]ConfigOption(nil), s.configs...)
	s.mu.Unlock()

	for _, opt := range options {
		if opt.ConfigID != key {
			continue
		}

		req := SetConfigOptionParams{SessionID: s.id, ConfigID: opt.ConfigID}
		switch opt.Type {
		case ConfigTypeBoolean:
			on, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("acp/v2: %s wants true or false, not %q", key, value)
			}
			req.Type, req.Value = ConfigTypeBoolean, on
		default:
			req.Type, req.Value = ConfigValueID, value
		}

		var resp SetConfigOptionResult
		if err := s.conn.call(ctx, MethodSetConfigOption, req, &resp); err != nil {
			return err
		}
		s.adoptConfig(resp.ConfigOptions)
		return nil
	}

	if key == modeKey {
		return fmt.Errorf("acp/v2: this agent has no session modes: %w", fsp.ErrUnsupported)
	}
	return fmt.Errorf("acp/v2: this agent has no %q option: %w", key, fsp.ErrUnsupported)
}

// Close ends the session: the agent is told to close it, the current turn is
// cancelled, and the process is given closeGrace to exit before it is killed.
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
	if err := s.conn.send(MethodSessionCancel, SessionIDParams{SessionID: s.id}); err != nil {
		s.log.Debug("acp/v2: could not cancel the turn while closing", "err", err)
	}
	if err := s.conn.call(ctx, MethodSessionClose, SessionIDParams{SessionID: s.id}, nil); err != nil {
		s.log.Debug("acp/v2: the agent did not close the session", "err", err)
	}
	stop()

	s.stopProcess()
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

	s.stopProcess()
	s.baseCancel()
	s.events.close()
}

// stopProcess closes the agent's stdin, which is how a stdio agent is asked to
// stop, and kills it if it will not go.
func (s *session) stopProcess() {
	s.proc.endInput()
	select {
	case <-s.proc.done:
	case <-time.After(s.closeGrace):
		s.log.Debug("acp/v2: the agent did not exit in time, killing it", "session", s.id)
		s.proc.kill()
		<-s.proc.done
	}
}

// watch reports the agent's exit. An exit nobody asked for is a failure the core
// has to hear about before the event channel goes quiet.
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

// ---------- the client side of the protocol ----------

// onNotify receives everything the agent streams. Updates that arrive while a
// resume may still be replaying are dropped: they are a transcript of a past
// conversation, not news.
func (s *session) onNotify(method string, params json.RawMessage) {
	if method != NotifySessionUpdate {
		s.log.Debug("acp/v2: ignoring a notification Ferry does not use", "method", method)
		return
	}

	var n SessionNotification
	if err := json.Unmarshal(params, &n); err != nil {
		s.log.Debug("acp/v2: unusable session update", "err", err)
		return
	}

	s.mu.Lock()
	loading := s.loading
	s.mu.Unlock()
	if loading {
		return
	}

	if n.Update.SessionUpdate == UpdateToolCall {
		s.rememberTool(n.Update)
	}

	switch n.Update.SessionUpdate {
	case UpdateStateUpdate:
		// Idle is how v2 ends a turn; running and requires_action only say what
		// the agent is busy with, which the core already knows.
		if n.Update.State == StateIdle {
			stop := n.Update.StopReason
			if stop == "" {
				stop = "end_turn"
			}
			s.endTurn(stop, "")
		}
	case UpdateConfigOptions:
		s.adoptConfig(n.Update.ConfigOptions)
	default:
		if e, ok := mapUpdate(n.Update, time.Now()); ok {
			s.events.push(e)
		}
	}
}

// onRequest answers the one request an agent sends a client: a permission
// question. A verdict with no option, an approver that fails and a cancelled
// turn all answer cancelled.
func (s *session) onRequest(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	if method != MethodRequestPermission {
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}

	var p RequestPermissionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "unusable permission request: " + err.Error()}
	}

	cancelled := RequestPermissionResult{Outcome: PermissionOutcome{Outcome: OutcomeCancelled}}
	if s.approver == nil {
		return cancelled, nil
	}

	s.mu.Lock()
	turn := s.turnCtx
	var known *ToolCallRef
	if params := p.Subject; params != nil && params.ToolCall != nil {
		if seen, ok := s.tools[params.ToolCall.ToolCallID]; ok {
			known = &seen
		}
	}
	s.mu.Unlock()

	askCtx, stop := mergeCancel(s.baseCtx, turn)
	defer stop()

	v, err := s.approver.Ask(askCtx, approvalRequest(p, known, time.Now()))
	if err != nil {
		s.log.Debug("acp/v2: the approver did not answer", "err", err)
		return cancelled, nil
	}
	if v.OptionID == "" {
		return cancelled, nil
	}
	return RequestPermissionResult{
		Outcome: PermissionOutcome{Outcome: OutcomeSelected, OptionID: v.OptionID},
	}, nil
}
