package proxy

import (
	"context"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// agentSide is the client the real agent thinks started it. Everything it
// receives goes on to the editor; session updates are mirrored to the daemon on
// the way past, and permission requests may be answered from chat instead.
type agentSide struct{ p *proxy }

var (
	_ acp.Client             = agentSide{}
	_ acp.ClientExperimental = agentSide{}
)

// SessionUpdate forwards the agent's output to the editor and mirrors it to the
// daemon. The editor comes first: it is the one waiting for the text.
func (a agentSide) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return err
	}
	if err := ed.SessionUpdate(ctx, params); err != nil {
		return err
	}
	if e, ok := mapUpdate(params.Update, time.Now()); ok {
		a.p.session(params.SessionId).sendEvent(e)
	}
	return nil
}

// RequestPermission decides who answers. By default the editor does, and the
// daemon is told that a tool is waiting there; with --remote-approve the
// request goes to chat instead and the editor never sees it.
func (a agentSide) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	s := a.p.session(params.SessionId)

	// Without a mirrored session there is nobody in chat to ask, so even
	// --remote-approve falls back to the editor.
	if a.p.opts.RemoteApprove && s != nil {
		v, ok := s.askRemote(ctx, params)
		if !ok || v.OptionID == "" {
			return cancelled, nil
		}
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(v.OptionID)),
		}, nil
	}

	s.sendEvent(toolEvent(params, statusAwaitingEditor, time.Now()))

	ed, err := a.p.editor(ctx)
	if err != nil {
		return cancelled, nil
	}
	return ed.RequestPermission(ctx, params)
}

// ---------- the rest is forwarding ----------
//
// The proxy declares no capabilities of its own: whatever the editor offered in
// initialize is what the agent sees, and every call it makes on those goes
// straight back to the editor.

func (a agentSide) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return ed.ReadTextFile(ctx, params)
}

func (a agentSide) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return ed.WriteTextFile(ctx, params)
}

func (a agentSide) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	return ed.CreateTerminal(ctx, params)
}

func (a agentSide) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	return ed.KillTerminal(ctx, params)
}

func (a agentSide) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	return ed.TerminalOutput(ctx, params)
}

func (a agentSide) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.ReleaseTerminalResponse{}, err
	}
	return ed.ReleaseTerminal(ctx, params)
}

func (a agentSide) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	return ed.WaitForTerminalExit(ctx, params)
}

// ---------- acp.ClientExperimental ----------

func (a agentSide) UnstableCreateElicitation(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	return ed.UnstableCreateElicitation(ctx, params)
}

func (a agentSide) UnstableCompleteElicitation(ctx context.Context, params acp.UnstableCompleteElicitationNotification) error {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return err
	}
	return ed.UnstableCompleteElicitation(ctx, params)
}

func (a agentSide) UnstableConnectMcp(ctx context.Context, params acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.UnstableConnectMcpResponse{}, err
	}
	return ed.UnstableConnectMcp(ctx, params)
}

func (a agentSide) UnstableDisconnectMcp(ctx context.Context, params acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	ed, err := a.p.editor(ctx)
	if err != nil {
		return acp.UnstableDisconnectMcpResponse{}, err
	}
	return ed.UnstableDisconnectMcp(ctx, params)
}
