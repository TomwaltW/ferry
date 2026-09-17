package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

type Outbox interface {
	// OpenWindow is called after every inbound message that passed the ACL and
	// resets the reply budget for that endpoint.
	OpenWindow(ep fsp.EndpointID, replyCtx string, inboundAt time.Time)
	Enqueue(ctx context.Context, msg fsp.Outbound) error
	FlushHeld(ctx context.Context, ep fsp.EndpointID) error // no-op in P1
	Run(ctx context.Context) error                          // merge and send loop, blocks until ctx ends
}

type ACL interface {
	Allowed(ep fsp.EndpointID, senderID string) bool
	IsApprover(ep fsp.EndpointID, senderID string) bool
	SetOwner(ctx context.Context, platform fsp.Platform, botID, ownerID string) error
	OfferPairing(ctx context.Context, ep fsp.EndpointID, senderID string) (code string, err error) // over the limit yields fsp.ErrQuotaExceeded
	ConfirmPairing(ctx context.Context, code string) (ep fsp.EndpointID, senderID string, err error)
}

type Approvals interface {
	Approver
	// ParseVerdict recognises a verdict sent as a whole message, such as "y abcde".
	ParseVerdict(text string) (id string, d fsp.Decision, ok bool)
	// Resolve applies a verdict. An unknown or already finished ID returns
	// false and the caller drops the message silently.
	Resolve(ctx context.Context, id string, d fsp.Decision, by string) bool
	// CancelSession ends every pending approval of the session as cancelled.
	CancelSession(id fsp.SessionID)
}

type Command struct {
	Name string // ls new use agents stop close mode pull file lang help pair
	Args []string
}

type Commands interface {
	Parse(text string) (Command, bool)
	// Unescape strips one leading "/" from a message that starts with "//" and
	// reports true, so the rest is handled as ordinary text.
	Unescape(text string) (string, bool)
}

type I18n interface {
	T(lang fsp.Lang, key string, args map[string]any) string
}
