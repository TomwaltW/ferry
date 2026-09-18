package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// DeliveryLimits is what one platform allows the outbox to do. The outbox asks
// every adapter for its own, because the entry points Ferry serves differ:
// WeChat may only answer inside the window an inbound message opens, while
// Feishu may send at any time but not faster than a few messages a second.
type DeliveryLimits struct {
	// NeedsWindow means a reply is only possible inside the window an inbound
	// message opened (WeChat).
	NeedsWindow bool
	// WindowTTL is how long that window lasts; 0 falls back to
	// outbox.window_ttl.
	WindowTTL time.Duration
	// ReplyBudget is how many messages one window carries. 0 falls back to
	// outbox.reply_budget while NeedsWindow is set, and means "no limit"
	// otherwise.
	ReplyBudget int
	// RatePerSec caps the send rate; 0 means no cap.
	RatePerSec float64
}

type IMAdapter interface {
	Platform() fsp.Platform
	// Run blocks until ctx ends, writing inbound messages to out. It returns
	// fsp.ErrSessionExpired when the platform session is no longer valid.
	Run(ctx context.Context, out chan<- fsp.Inbound) error
	// Send delivers one message synchronously and never retries by itself.
	// An empty replyCtx yields fsp.ErrNoWindow.
	Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error)
	// Typing switches the "typing" indicator. Platforms without one return nil.
	Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error
	Status() AdapterStatus
	// Limits describes what the platform allows; it is constant for the life
	// of the adapter.
	Limits() DeliveryLimits
}

type AdapterStatus struct {
	Online  bool
	BotID   string
	OwnerID string // whoever scanned the QR code
	Detail  string // reason for being offline, and the like
}

type Credentials struct {
	BotToken string
	BotID    string
	OwnerID  string
	BaseURL  string
}

type Login interface {
	// Start fetches a QR code and returns the content to render.
	Start(ctx context.Context) (qrContent string, err error)
	// Wait blocks until the code is confirmed, until it has expired (it is
	// refreshed at most 3 times, calling onQR each time), or until ctx ends.
	Wait(ctx context.Context, onQR func(qrContent string)) (Credentials, error)
}
