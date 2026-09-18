package fakeagent

import (
	"context"
	"errors"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/ids"
)

// titleRunes is how much of the first prompt becomes the session title.
const titleRunes = 20

// agent implements the ACP agent side. Every session lives on disk, so list
// and load see the same state a restarted process would.
type agent struct {
	clk   clock.Clock
	store *store

	// conn is only readable through peer: the SDK starts dispatching as soon
	// as the connection is constructed, which is before Serve can store it.
	conn   *acp.AgentSideConnection
	connUp sync.WaitGroup

	// mu serialises the read-modify-write of a session file. Turns of
	// different sessions may still run concurrently.
	mu sync.Mutex
}

func newAgent(clk clock.Clock, st *store) *agent {
	a := &agent{clk: clk, store: st}
	a.connUp.Add(1)
	return a
}

// attach hands the agent the connection it talks back over, releasing any
// handler already waiting in peer.
func (a *agent) attach(conn *acp.AgentSideConnection) {
	a.conn = conn
	a.connUp.Done()
}

// peer returns the connection to the client, waiting for attach if a handler
// managed to start first.
func (a *agent) peer() *acp.AgentSideConnection {
	a.connUp.Wait()
	return a.conn
}

var (
	_ acp.Agent       = (*agent)(nil)
	_ acp.AgentLoader = (*agent)(nil)
)

// unknownSession is the -32602 the failure table asks for when a session id
// has no file behind it.
func unknownSession(id acp.SessionId) error {
	return acp.NewInvalidParams(map[string]any{"error": "unknown session", "sessionId": string(id)})
}

func (a *agent) now() string { return a.clk.Now().UTC().Format(time.RFC3339Nano) }

func (a *agent) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	recordInitialize(a.store, int(params.ProtocolVersion))
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				List: &acp.SessionListCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{},
		AgentInfo:   &acp.Implementation{Name: "ferry-fakeagent", Version: "p0"},
	}, nil
}

func (a *agent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	rec := record{ID: ids.New("sess"), Cwd: params.Cwd, UpdatedAt: a.now(), History: []turn{}}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.save(rec); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: acp.SessionId(rec.ID)}, nil
}

// LoadSession replays the stored history as user and agent chunks before it
// returns, which is what a client reconnecting to an old session expects.
func (a *agent) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.mu.Lock()
	rec, err := a.store.load(string(params.SessionId))
	a.mu.Unlock()
	if err != nil {
		if errors.Is(err, errNoSession) {
			return acp.LoadSessionResponse{}, unknownSession(params.SessionId)
		}
		return acp.LoadSessionResponse{}, err
	}

	for _, t := range rec.History {
		update := acp.UpdateAgentMessageText(t.Text)
		if t.Role == roleUser {
			update = acp.UpdateUserMessageText(t.Text)
		}
		if err := a.peer().SessionUpdate(ctx, acp.SessionNotification{
			SessionId: params.SessionId,
			Update:    update,
		}); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	return acp.LoadSessionResponse{}, nil
}

func (a *agent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	cwd := ""
	if params.Cwd != nil {
		cwd = *params.Cwd
	}

	a.mu.Lock()
	recs, err := a.store.list(cwd)
	a.mu.Unlock()
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}

	sessions := make([]acp.SessionInfo, 0, len(recs))
	for _, r := range recs {
		info := acp.SessionInfo{SessionId: acp.SessionId(r.ID), Cwd: r.Cwd}
		if r.Title != "" {
			info.Title = acp.Ptr(r.Title)
		}
		if r.UpdatedAt != "" {
			info.UpdatedAt = acp.Ptr(r.UpdatedAt)
		}
		sessions = append(sessions, info)
	}
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

// Prompt records the user turn, runs the scripted behaviour for it, then
// records whatever the agent streamed back. The user turn is stored before the
// behaviour runs so a failing turn still names the session.
func (a *agent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	text := promptText(params.Prompt)

	if err := a.appendTurn(params.SessionId, turn{Role: roleUser, Text: text}); err != nil {
		return acp.PromptResponse{}, err
	}

	said, stop, err := a.run(ctx, params.SessionId, text)
	if said != "" {
		if saveErr := a.appendTurn(params.SessionId, turn{Role: roleAgent, Text: said}); saveErr != nil {
			return acp.PromptResponse{}, saveErr
		}
	}
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: stop}, nil
}

// appendTurn adds one entry to a session's history, setting the title from the
// first user turn.
func (a *agent) appendTurn(id acp.SessionId, t turn) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec, err := a.store.load(string(id))
	if err != nil {
		if errors.Is(err, errNoSession) {
			return unknownSession(id)
		}
		return err
	}
	if rec.Title == "" && t.Role == roleUser {
		rec.Title = truncateRunes(t.Text, titleRunes)
	}
	rec.History = append(rec.History, t)
	rec.UpdatedAt = a.now()
	return a.store.save(rec)
}

// Cancel needs no work of its own: the SDK cancels the context of the prompt
// in flight, and each scripted behaviour ends as cancelled when it sees that.
func (a *agent) Cancel(ctx context.Context, params acp.CancelNotification) error { return nil }

func (a *agent) Authenticate(ctx context.Context, params acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (a *agent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	a.mu.Lock()
	_, err := a.store.load(string(params.SessionId))
	a.mu.Unlock()
	if err != nil {
		if errors.Is(err, errNoSession) {
			return acp.ResumeSessionResponse{}, unknownSession(params.SessionId)
		}
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{}, nil
}

func (a *agent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (a *agent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}
