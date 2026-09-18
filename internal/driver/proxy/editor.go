package proxy

import (
	"context"

	acp "github.com/coder/acp-go-sdk"
)

// editorSide is the agent the editor thinks it started. Every method is a
// forward to the real agent; the ones that open or run a session also tell the
// daemon about it.
//
// Extension methods the SDK does not know never reach here: its dispatcher
// answers them with method not found, which is what spec-p3 3.6 asks for.
type editorSide struct{ p *proxy }

var (
	_ acp.Agent             = editorSide{}
	_ acp.AgentLoader       = editorSide{}
	_ acp.AgentExperimental = editorSide{}
)

func (e editorSide) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	resp, err := e.p.agentConn.Initialize(ctx, params)
	if err == nil {
		e.p.adoptCaps(resp.AgentCapabilities)
	}
	return resp, err
}

func (e editorSide) Authenticate(ctx context.Context, params acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return e.p.agentConn.Authenticate(ctx, params)
}

func (e editorSide) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return e.p.agentConn.Logout(ctx, params)
}

// NewSession registers the session it just opened. A daemon that will not take
// it costs the editor nothing: the session runs unmirrored.
func (e editorSide) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	resp, err := e.p.agentConn.NewSession(ctx, params)
	if err != nil {
		return resp, err
	}
	e.p.attach(ctx, resp.SessionId, params.Cwd)
	return resp, nil
}

// LoadSession replays an old conversation into the editor and registers it, so
// that a session the editor resumes is mirrored like a fresh one.
func (e editorSide) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	resp, err := e.p.agentConn.LoadSession(ctx, params)
	if err != nil {
		return resp, err
	}
	e.p.attach(ctx, params.SessionId, params.Cwd)
	return resp, nil
}

func (e editorSide) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return e.p.agentConn.ResumeSession(ctx, params)
}

func (e editorSide) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return e.p.agentConn.ListSessions(ctx, params)
}

func (e editorSide) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	resp, err := e.p.agentConn.CloseSession(ctx, params)
	if err == nil {
		e.p.session(params.SessionId).detach()
	}
	return resp, err
}

// Prompt runs one editor turn. It waits for a prompt from chat that is already
// running, tells the daemon the turn started, and reports how it ended.
//
// The turn itself runs on the proxy's own context rather than on the editor's
// request: a cancel from the editor is forwarded as session/cancel, and the
// agent then ends the turn with the cancelled stop reason, which is what the
// editor expects to see.
func (e editorSide) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	s := e.p.session(params.SessionId)
	if s != nil {
		s.turn.Lock()
		defer s.turn.Unlock()
		s.startEditorTurn()
	}

	resp, err := e.p.agentConn.Prompt(e.p.baseCtx, params)
	s.endTurn(resp, err)
	return resp, err
}

func (e editorSide) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return e.p.agentConn.Cancel(ctx, params)
}

func (e editorSide) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return e.p.agentConn.SetSessionConfigOption(ctx, params)
}

func (e editorSide) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return e.p.agentConn.SetSessionMode(ctx, params)
}

// ---------- acp.AgentExperimental ----------
//
// The unstable half of the protocol is forwarded as well: an agent that
// supports one of these answers it, and one that does not says method not
// found, which the proxy passes straight back to the editor.

func (e editorSide) UnstableDidChangeDocument(ctx context.Context, params acp.UnstableDidChangeDocumentNotification) error {
	return e.p.agentConn.UnstableDidChangeDocument(ctx, params)
}

func (e editorSide) UnstableDidCloseDocument(ctx context.Context, params acp.UnstableDidCloseDocumentNotification) error {
	return e.p.agentConn.UnstableDidCloseDocument(ctx, params)
}

func (e editorSide) UnstableDidFocusDocument(ctx context.Context, params acp.UnstableDidFocusDocumentNotification) error {
	return e.p.agentConn.UnstableDidFocusDocument(ctx, params)
}

func (e editorSide) UnstableDidOpenDocument(ctx context.Context, params acp.UnstableDidOpenDocumentNotification) error {
	return e.p.agentConn.UnstableDidOpenDocument(ctx, params)
}

func (e editorSide) UnstableDidSaveDocument(ctx context.Context, params acp.UnstableDidSaveDocumentNotification) error {
	return e.p.agentConn.UnstableDidSaveDocument(ctx, params)
}

func (e editorSide) UnstableAcceptNes(ctx context.Context, params acp.UnstableAcceptNesNotification) error {
	return e.p.agentConn.UnstableAcceptNes(ctx, params)
}

func (e editorSide) UnstableRejectNes(ctx context.Context, params acp.UnstableRejectNesNotification) error {
	return e.p.agentConn.UnstableRejectNes(ctx, params)
}

func (e editorSide) UnstableStartNes(ctx context.Context, params acp.UnstableStartNesRequest) (acp.UnstableStartNesResponse, error) {
	return e.p.agentConn.UnstableStartNes(ctx, params)
}

func (e editorSide) UnstableCloseNes(ctx context.Context, params acp.UnstableCloseNesRequest) (acp.UnstableCloseNesResponse, error) {
	return e.p.agentConn.UnstableCloseNes(ctx, params)
}

func (e editorSide) UnstableSuggestNes(ctx context.Context, params acp.UnstableSuggestNesRequest) (acp.UnstableSuggestNesResponse, error) {
	return e.p.agentConn.UnstableSuggestNes(ctx, params)
}

func (e editorSide) UnstableListProviders(ctx context.Context, params acp.UnstableListProvidersRequest) (acp.UnstableListProvidersResponse, error) {
	return e.p.agentConn.UnstableListProviders(ctx, params)
}

func (e editorSide) UnstableSetProvider(ctx context.Context, params acp.UnstableSetProviderRequest) (acp.UnstableSetProviderResponse, error) {
	return e.p.agentConn.UnstableSetProvider(ctx, params)
}

func (e editorSide) UnstableDisableProvider(ctx context.Context, params acp.UnstableDisableProviderRequest) (acp.UnstableDisableProviderResponse, error) {
	return e.p.agentConn.UnstableDisableProvider(ctx, params)
}

func (e editorSide) UnstableDeleteSession(ctx context.Context, params acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	return e.p.agentConn.UnstableDeleteSession(ctx, params)
}

func (e editorSide) UnstableForkSession(ctx context.Context, params acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	return e.p.agentConn.UnstableForkSession(ctx, params)
}
