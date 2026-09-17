package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// TerminalSession is a session an agent started in a terminal, which Ferry can
// take over.
type TerminalSession struct {
	Agent          string // agent name from the configuration
	Driver         fsp.DriverKind
	AgentSessionID string
	Title          string
	Cwd            string
	UpdatedAt      time.Time
	Shared         bool // a service-backed session: usable from chat and terminal at once, so no busy check
}

type Handoff interface {
	// Discover lists each agent's sessions inside the workspace roots over the
	// last lookback_days, minus those Ferry already tracks. Results are cached
	// for one minute.
	Discover(ctx context.Context, ep fsp.EndpointID) ([]TerminalSession, error)
	// Busy reports whether a terminal process still holds the session.
	// It returns fsp.ErrUnsupported on Windows.
	Busy(ctx context.Context, ts TerminalSession) (bool, error)
	// BackCommand returns the command that resumes the session in a terminal.
	// Agents whose command has not been verified return fsp.ErrUnsupported.
	BackCommand(agent, agentSessionID string) ([]string, error)
}

// LongOutput is a turn whose text was too long for chat and was written to a
// file instead.
type LongOutput struct {
	ID        int64
	Endpoint  fsp.EndpointID
	Session   fsp.SessionID
	Path      string
	Chars     int
	CreatedAt time.Time
}
