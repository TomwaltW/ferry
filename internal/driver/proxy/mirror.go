package proxy

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
)

// injectBuffer is how many prompts from chat may wait for the running turn. A
// human types far more slowly than a turn takes, so a full buffer means the
// daemon is repeating itself rather than that the user is quick.
const injectBuffer = 32

// modeKey is the config key reserved for the agent's session mode, the same one
// the core uses when it drives an agent itself.
const modeKey = "mode"

// psession is one mirrored session: the agent's session ID on one side, a
// control stream on the other.
//
// Each session gets a control connection of its own. spec-p3 3.6 describes one
// shared connection, but control.Client.Attach is the last request/response
// pair on its socket — after it the socket belongs to the stream, and a second
// attach would have two readers on one scanner. Every message still carries
// SessionID, so a daemon that learns to multiplex needs no change here.
type psession struct {
	p       *proxy
	agentID acp.SessionId

	stream   *control.Stream
	daemonID string

	// turn is held for the whole of a turn, whoever started it: it is what
	// makes an editor prompt wait behind an injected one and the other way
	// round.
	turn sync.Mutex

	// injects carries prompts from chat to the goroutine that runs them, so
	// that reading the control stream never blocks on a turn.
	injects chan string

	mu      sync.Mutex
	pending map[string]chan fsp.Verdict
	closed  bool

	done     chan struct{}
	stopOnce sync.Once
}

// attach registers one session with the daemon. Failing to attach is not fatal:
// the session then runs like any other session of a proxy with no daemon.
func (p *proxy) attach(ctx context.Context, id acp.SessionId, cwd string) {
	if !p.daemonUp || id == "" {
		return
	}
	if p.session(id) != nil {
		// session/load of something already attached, or an agent that handed
		// out the same ID twice.
		return
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	client, err := control.Dial(p.home)
	if err != nil {
		p.log.Warn("could not reach the daemon to register the session", "session", string(id), "err", err)
		return
	}
	stream, resp, err := client.Attach(ctx, control.AttachRequest{
		Kind:           control.KindProxy,
		Agent:          p.opts.Agent,
		AgentSessionID: string(id),
		Cwd:            cwd,
		Title:          p.opts.Title,
		PID:            os.Getpid(),
		AllowInject:    p.opts.AllowInject,
		RemoteApprove:  p.opts.RemoteApprove,
	})
	if err != nil {
		client.Close()
		p.log.Warn("the daemon refused the session", "session", string(id), "err", err)
		return
	}

	s := &psession{
		p:        p,
		agentID:  id,
		stream:   stream,
		daemonID: resp.SessionID,
		injects:  make(chan string, injectBuffer),
		pending:  make(map[string]chan fsp.Verdict),
		done:     make(chan struct{}),
	}
	p.mu.Lock()
	p.sessions[id] = s
	p.mu.Unlock()

	p.log.Info("session registered with the daemon",
		"session", string(id), "daemon_session", resp.SessionID, "slot", resp.Slot)

	go s.readLoop()
	go s.injectLoop()
}

// detach tells the daemon the session is over and drops the connection under
// it. It is safe to call more than once and on a session that was never
// mirrored.
func (s *psession) detach() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		if err := s.stream.Send(control.Message{Type: control.MsgDetach, SessionID: s.daemonID}); err != nil {
			s.p.log.Debug("could not say goodbye to the daemon", "err", err)
		}
		// Closing the stream closes the connection it rode on.
		_ = s.stream.Close()
		close(s.done)

		s.p.mu.Lock()
		delete(s.p.sessions, s.agentID)
		s.p.mu.Unlock()
	})
}

// send mirrors one message, giving up quietly once the session is over: a
// daemon that went away must not stop the editor from working.
func (s *psession) send(m control.Message) {
	if s == nil {
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	if err := s.stream.Send(m); err != nil {
		s.p.log.Debug("could not mirror to the daemon", "type", m.Type, "err", err)
	}
}

func (s *psession) sendEvent(e fsp.Event) {
	if s == nil {
		return
	}
	s.send(control.Message{Type: control.MsgEvent, SessionID: s.daemonID, Event: &e})
}

// startTurn marks the editor's turn for the daemon, which localises the marker
// before showing it.
func (s *psession) startEditorTurn() {
	s.sendEvent(fsp.Event{Kind: fsp.EvChunk, Text: editorTurnMarker, At: time.Now()})
}

// endTurn reports how a turn finished. A turn that failed still ended, so it is
// a turn end with the error stop reason rather than an error event.
func (s *psession) endTurn(resp acp.PromptResponse, err error) {
	if s == nil {
		return
	}
	if err != nil {
		s.sendEvent(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "error", Err: err.Error(), At: time.Now()})
		return
	}
	s.sendEvent(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: string(resp.StopReason), At: time.Now()})
}

// readLoop hands the daemon's messages to whoever is waiting for them until the
// control connection goes away.
func (s *psession) readLoop() {
	for {
		m, err := s.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.p.log.Debug("the control stream ended", "err", err)
			}
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			return
		}

		switch m.Type {
		case control.MsgPrompt:
			s.enqueueInject(m)
		case control.MsgVerdict:
			s.deliverVerdict(m)
		case control.MsgCancel:
			s.cancelTurn()
		case control.MsgConfig:
			s.setConfig(m.Key, m.Value)
		case control.MsgClose:
			s.closeSession()
		default:
			s.p.log.Debug("ignoring a control message the proxy has no use for", "type", m.Type)
		}
	}
}

// enqueueInject queues a prompt from chat. Without --allow-inject the daemon
// answers inject.disabled itself and never sends one, so one that arrives
// anyway is dropped rather than obeyed.
func (s *psession) enqueueInject(m control.Message) {
	if !s.p.opts.AllowInject {
		s.p.log.Debug("dropping a prompt from chat: this proxy does not allow injection")
		return
	}
	if m.Text == "" {
		return
	}
	select {
	case s.injects <- m.Text:
	default:
		s.p.log.Warn("dropping a prompt from chat: too many are already waiting")
	}
}

// injectLoop runs the prompts from chat one at a time, each of them waiting for
// whatever turn is already running.
func (s *psession) injectLoop() {
	for {
		select {
		case <-s.done:
			return
		case text := <-s.injects:
			s.runInject(text)
		}
	}
}

// runInject sends one prompt from chat as if the proxy were the editor's user:
// the editor is shown the input first, then the agent runs it and its output
// reaches both ends the usual way.
func (s *psession) runInject(text string) {
	s.turn.Lock()
	defer s.turn.Unlock()

	ctx := s.p.baseCtx
	if ed, err := s.p.editor(ctx); err == nil {
		if err := ed.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: s.agentID,
			Update:    acp.UpdateUserMessageText(text),
		}); err != nil {
			s.p.log.Debug("could not show the editor what chat sent", "err", err)
		}
	}

	resp, err := s.p.agentConn.Prompt(ctx, acp.PromptRequest{
		SessionId: s.agentID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})
	s.endTurn(resp, err)
}

// deliverVerdict wakes the approval that asked for it. A verdict for a request
// nobody is waiting on is late rather than wrong, and is dropped.
func (s *psession) deliverVerdict(m control.Message) {
	if m.Verdict == nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[m.ReqID]
	s.mu.Unlock()
	if ch == nil {
		s.p.log.Debug("a verdict arrived for a request nobody is waiting on", "req", m.ReqID)
		return
	}
	select {
	case ch <- *m.Verdict:
	default:
	}
}

// askRemote puts one permission request to chat and waits for its verdict. The
// second result is false when no verdict arrived — the turn was cancelled, or
// the daemon went away — which the caller answers as cancelled.
func (s *psession) askRemote(ctx context.Context, params acp.RequestPermissionRequest) (fsp.Verdict, bool) {
	reqID := ids.New("apr")
	ch := make(chan fsp.Verdict, 1)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fsp.Verdict{}, false
	}
	s.pending[reqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, reqID)
		s.mu.Unlock()
	}()

	req := approvalRequest(params, time.Now())
	s.send(control.Message{
		Type:      control.MsgApprovalRequest,
		SessionID: s.daemonID,
		ReqID:     reqID,
		Approval:  &req,
	})

	select {
	case v := <-ch:
		return v, true
	case <-ctx.Done():
		s.send(control.Message{Type: control.MsgApprovalCancel, SessionID: s.daemonID, ReqID: reqID})
		return fsp.Verdict{}, false
	case <-s.done:
		return fsp.Verdict{}, false
	}
}

// cancelTurn passes the daemon's cancel on to the agent. The editor learns of
// it the usual way: its prompt comes back cancelled.
func (s *psession) cancelTurn() {
	ctx, stop := context.WithTimeout(s.p.baseCtx, closeGrace)
	defer stop()
	if err := s.p.agentConn.Cancel(ctx, acp.CancelNotification{SessionId: s.agentID}); err != nil {
		s.p.log.Debug("could not cancel the turn", "err", err)
	}
}

// setConfig applies one session option the daemon asked for. The reserved key
// mode maps onto the agent's session mode; anything else names a config option
// the agent offered.
func (s *psession) setConfig(key, value string) {
	if key == "" {
		return
	}
	ctx, stop := context.WithTimeout(s.p.baseCtx, closeGrace)
	defer stop()

	var err error
	if key == modeKey {
		_, err = s.p.agentConn.SetSessionMode(ctx, acp.SetSessionModeRequest{
			SessionId: s.agentID,
			ModeId:    acp.SessionModeId(value),
		})
	} else {
		_, err = s.p.agentConn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: s.agentID,
				ConfigId:  acp.SessionConfigId(key),
				Value:     acp.SessionConfigValueId(value),
			},
		})
	}
	if err != nil {
		s.p.log.Warn("the agent would not take the option", "key", key, "err", err)
	}
}

// closeSession ends one session at the daemon's request. The editor keeps its
// connection: only this conversation is over.
func (s *psession) closeSession() {
	ctx, stop := context.WithTimeout(s.p.baseCtx, closeGrace)
	defer stop()

	if err := s.p.agentConn.Cancel(ctx, acp.CancelNotification{SessionId: s.agentID}); err != nil {
		s.p.log.Debug("could not cancel the turn while closing", "err", err)
	}
	if s.p.agentCloses() {
		if _, err := s.p.agentConn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: s.agentID}); err != nil {
			s.p.log.Debug("the agent would not close the session", "err", err)
		}
	}
	s.detach()
}
