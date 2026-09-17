package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// Operations of the P2 protocol.
const (
	OpStatus = "status"
	OpPark   = "park"
)

// Error values of Response.Error.
const (
	ErrNotFound    = "not_found"
	ErrUnsupported = "unsupported"
	ErrBusy        = "busy"
	ErrInternal    = "internal"
)

type ParkRequest struct {
	Slot int `json:"slot,omitempty"` // 0 means the foreground session of the owner endpoint
}

type ParkResponse struct {
	Agent          string   `json:"agent"`
	AgentSessionID string   `json:"agent_session_id"`
	Cwd            string   `json:"cwd"`
	BackCommand    []string `json:"back_command"` // empty when the agent cannot be resumed
}

type StatusResponse struct {
	IMOnline bool   `json:"im_online"`
	BotID    string `json:"bot_id"`
	Sessions int    `json:"sessions"`
	Running  int    `json:"running"`
}

// Handler is the daemon side of the control protocol, implemented by the core.
type Handler interface {
	Park(ctx context.Context, req ParkRequest) (ParkResponse, error)
	Status(ctx context.Context) (StatusResponse, error)
}

// errorCode maps an error onto the fixed vocabulary of Response.Error. Anything
// the protocol does not name is internal, whose text never crosses the socket.
func errorCode(err error) string {
	switch {
	case errors.Is(err, fsp.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, fsp.ErrUnsupported):
		return ErrUnsupported
	case errors.Is(err, fsp.ErrBusy), errors.Is(err, fsp.ErrLocked):
		return ErrBusy
	default:
		return ErrInternal
	}
}

// decodeError turns a Response.Error back into a sentinel the caller can test.
func decodeError(code string) error {
	switch code {
	case ErrNotFound:
		return fmt.Errorf("control: %w", fsp.ErrNotFound)
	case ErrUnsupported:
		return fmt.Errorf("control: %w", fsp.ErrUnsupported)
	case ErrBusy:
		return fmt.Errorf("control: %w", fsp.ErrBusy)
	default:
		return fmt.Errorf("control: the daemon reported %s", code)
	}
}
