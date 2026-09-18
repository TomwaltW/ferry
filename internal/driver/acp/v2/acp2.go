package v2

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The v2 methods, in the order a session goes through them.
const (
	MethodInitialize    = "initialize"
	MethodAuthLogin     = "auth/login"
	MethodAuthLogout    = "auth/logout"
	MethodSessionNew    = "session/new"
	MethodSessionResume = "session/resume"
	MethodSessionPrompt = "session/prompt"
	MethodSessionCancel = "session/cancel"
	MethodSessionList   = "session/list"
	MethodSessionClose  = "session/close"
	// NotifySessionUpdate carries accepted messages, state updates and
	// progress. An idle state_update ends the turn and names its stop reason.
	NotifySessionUpdate = "session/update"
)

// New returns the v2 driver. Its behaviour belongs to the P3-T4 task.
func New(log *slog.Logger) ports.Driver { return &driver{log: log} }

type driver struct {
	log *slog.Logger
}

func (d *driver) Kind() fsp.DriverKind { return fsp.DriverACP }

func (d *driver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	return nil, fmt.Errorf("acp/v2: Start: %w", fsp.ErrUnsupported)
}

func (d *driver) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	return nil, fmt.Errorf("acp/v2: Resume: %w", fsp.ErrUnsupported)
}

func (d *driver) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	return nil, fmt.Errorf("acp/v2: List: %w", fsp.ErrUnsupported)
}

var _ ports.Driver = (*driver)(nil)
