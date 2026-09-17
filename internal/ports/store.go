package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

type KV interface {
	GetKV(ctx context.Context, key string) (string, error) // missing key yields fsp.ErrNotFound
	SetKV(ctx context.Context, key, value string) error
}

type Binding struct {
	Endpoint   fsp.EndpointID
	Session    fsp.SessionID
	Slot       int
	Foreground bool
}

type OutboxRow struct {
	ID        int64
	Msg       fsp.Outbound
	State     fsp.DeliveryState
	Attempts  int
	CreatedAt time.Time
}

type Window struct {
	Endpoint fsp.EndpointID
	ReplyCtx string
	OpenedAt time.Time
	Used     int // replies already spent in this window
}

type Pairing struct {
	Code      string
	Endpoint  fsp.EndpointID
	SenderID  string
	ExpiresAt time.Time
}

type AuditEntry struct {
	At       time.Time
	Kind     string // inbound rejected approval_asked approval_decided outbound_failed session_started session_parked session_closed login expired
	Endpoint fsp.EndpointID
	SenderID string
	Session  fsp.SessionID
	Detail   string // never the message body
}

type Store interface {
	KV

	PutSession(ctx context.Context, s fsp.Session) error // insert or replace by ID
	GetSession(ctx context.Context, id fsp.SessionID) (fsp.Session, error)
	ListSessions(ctx context.Context, owner fsp.EndpointID) ([]fsp.Session, error) // ascending by CreatedAt
	ActiveSessions(ctx context.Context) ([]fsp.Session, error)                     // every session that is not closed

	Bind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) (slot int, err error) // slot is the lowest unused positive integer for the endpoint
	Unbind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error
	SetForeground(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error
	Bindings(ctx context.Context, ep fsp.EndpointID) ([]Binding, error) // ascending by Slot

	// SeenMessage records the message and returns false the first time it is
	// seen. Records are kept for 24 hours.
	SeenMessage(ctx context.Context, ep fsp.EndpointID, messageID string, at time.Time) (bool, error)

	AddOutbox(ctx context.Context, msg fsp.Outbound, at time.Time) (int64, error)
	UpdateOutbox(ctx context.Context, id int64, state fsp.DeliveryState, attempts int, at time.Time) error
	OutboxByState(ctx context.Context, ep fsp.EndpointID, state fsp.DeliveryState) ([]OutboxRow, error) // ascending by ID
	OutboxEndpoints(ctx context.Context, state fsp.DeliveryState) ([]fsp.EndpointID, error)
	SaveWindow(ctx context.Context, w Window) error
	LoadWindow(ctx context.Context, ep fsp.EndpointID) (Window, error) // missing window yields fsp.ErrNotFound

	PutApproval(ctx context.Context, req fsp.ApprovalRequest) error
	DecideApproval(ctx context.Context, v fsp.Verdict, at time.Time) error

	SetRole(ctx context.Context, ep fsp.EndpointID, senderID, role string) error  // owner | approver | member
	Role(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) // no record yields ""
	AddPairing(ctx context.Context, p Pairing) error
	TakePairing(ctx context.Context, code string, now time.Time) (Pairing, error) // taking deletes it; missing or expired yields fsp.ErrNotFound
	CountPairings(ctx context.Context, ep fsp.EndpointID, now time.Time) (int, error)

	Audit(ctx context.Context, e AuditEntry) error
	Close() error
}
