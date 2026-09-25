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
	FlushHeld(ctx context.Context, ep fsp.EndpointID) error
	Run(ctx context.Context) error // merge and send loop, blocks until ctx ends
	// Pull sends the next batch of held messages, or replies
	// outbox.nothing_held when there is nothing waiting.
	Pull(ctx context.Context, ep fsp.EndpointID) error
	// File resends the nth long-output file, 1 being the most recent.
	File(ctx context.Context, ep fsp.EndpointID, n int) error
	// Held reports how many messages are currently held back.
	Held(ep fsp.EndpointID) int
}

// Waker is the one way out of chat: it nudges the operator through a side
// channel when the reply window is closed.
type Waker interface {
	// Wake sends a reminder according to the configuration. Repeats of the same
	// kind to the same endpoint are rate limited by notify.min_interval.
	// kind is one of approval_pending, turn_finished, im_offline.
	// args carries only non-sensitive values such as a session number: never a
	// message body, tool argument, command or path.
	Wake(ctx context.Context, ep fsp.EndpointID, kind string, args map[string]any)
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
	// ParseVerdict recognises a verdict sent as a whole message, such as
	// "y abcde" or "同意 abcde". A decision word on its own ("同意", "y")
	// reports ok with an empty id: the caller decides, from Pending, whether
	// it may stand for the one approval waiting.
	ParseVerdict(text string) (id string, d fsp.Decision, ok bool)
	// Pending lists the approvals whose question went to ep and is still
	// unanswered, oldest first.
	Pending(ctx context.Context, ep fsp.EndpointID) []PendingApproval
	// Lapsed reports when an approval asked at ep last ended without a human
	// answer (timed out or cancelled); zero when none has.
	Lapsed(ep fsp.EndpointID) time.Time
	// Resolve applies a verdict. An unknown or already finished ID returns
	// false and the caller drops the message silently.
	Resolve(ctx context.Context, id string, d fsp.Decision, by string) bool
	// CancelSession ends every pending approval of the session as cancelled.
	CancelSession(id fsp.SessionID)
}

// PendingApproval is an approval still waiting for a human.
type PendingApproval struct {
	ID      string
	Session fsp.SessionID
	AskedAt time.Time // when the question was queued for the endpoint
	ShownAt time.Time // when the platform accepted the question; zero until it has
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
