package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

type SessionSpec struct {
	Agent   string   // agent name from the configuration
	Command []string // executable and arguments
	Cwd     string   // absolute path
	Env     []string // KEY=VALUE, already filtered against the whitelist
	Server  *ServerSpec
}

// ServerSpec describes an agent that is already running as a local service.
// When it is set, Command is empty and the server driver takes over.
type ServerSpec struct {
	Kind   string // opencode | qwen
	URL    string // loopback addresses only
	Secret string // read from secret_env, may be empty
}

type Caps struct{ Load, Resume, List, Cancel, Config bool }

type AgentSessionInfo struct {
	AgentSessionID string
	Title          string
	Cwd            string
	UpdatedAt      time.Time
}

type Driver interface {
	Kind() fsp.DriverKind
	Start(ctx context.Context, spec SessionSpec, ap Approver) (AgentSession, error)
	// Resume restores an existing session; agents without support return fsp.ErrUnsupported.
	Resume(ctx context.Context, spec SessionSpec, agentSessionID string, ap Approver) (AgentSession, error)
	// List enumerates the agent's own sessions; unsupported returns fsp.ErrUnsupported (used in P2).
	List(ctx context.Context, spec SessionSpec) ([]AgentSessionInfo, error)
}

type AgentSession interface {
	AgentSessionID() string
	Caps() Caps
	// Prompt returns immediately; output and completion arrive on Events.
	// It returns fsp.ErrBusy while a turn is still running.
	Prompt(ctx context.Context, text string) error
	Cancel(ctx context.Context) error
	// SetConfig sets a session option; the key "mode" maps to the session mode.
	// Unsupported options return fsp.ErrUnsupported.
	SetConfig(ctx context.Context, key, value string) error
	// Events closes once the session is closed or the process exits; an
	// unexpected exit emits an EvError first. Drivers do not know Ferry's
	// session ID, so Event.Session is left empty for the core to fill in.
	Events() <-chan fsp.Event
	Close() error
}

// Approver is called by a driver when the agent requests a permission; it
// blocks until the verdict is in. Drivers leave req.ID and req.Session empty:
// the core's Approver fills in Session and the approval module assigns the ID.
type Approver interface {
	Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error)
}

type AgentInfo struct {
	Name      string
	Display   string
	Source    string // registry | command
	Available bool
	Reason    string // why it is unavailable
}

type Registry interface {
	// Resolve returns the launch spec with Cwd left to the caller.
	// An agent that is not configured yields fsp.ErrNotFound.
	Resolve(ctx context.Context, agent string) (SessionSpec, error)
	Agents(ctx context.Context) []AgentInfo
}
