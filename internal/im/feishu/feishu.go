package feishu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// KeyringPlatform is the platform part of the keyring account that holds the
// app secret. The account is "feishu@" plus a hash of FERRY_HOME, one per
// setup (see security.SaveCredentials); the configuration file carries the
// app ID, never the secret.
const KeyringPlatform = "feishu"

// RatePerSec is the send rate spec-p3 3.4 fixes for Feishu; unlike WeChat it
// needs no reply window, only a ceiling on how fast messages leave.
const RatePerSec = 5

// OfflineAfter is how long the long-lived connection may stay down before the
// adapter reports itself offline and wakes the owner.
const OfflineAfter = 5 * time.Minute

// rateLimitBackoff is the wait before each retry of a send Feishu refused for
// going too fast (spec-p3 3.10): three retries, 1, 2 and 4 seconds apart.
var rateLimitBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// ErrRateLimited is what a Client reports when Feishu turned a send away for
// going too fast. It is the one error the adapter retries by itself.
var ErrRateLimited = errors.New("feishu: rate limited")

// Client is the Feishu SDK as this package uses it, so that tests can swap in
// a fake without a network.
type Client interface {
	// Start opens the long-lived connection and blocks until ctx ends,
	// handing every inbound message to onMessage and every card button click
	// to onCardAction. onCardAction answers with the toast to show the
	// clicker, or false to leave the callback unanswered.
	Start(ctx context.Context, onMessage func(Message), onCardAction func(CardAction) (toast string, ok bool)) error
	SendText(ctx context.Context, openID, text string) (messageID string, err error)
	// SendCard sends an interactive card (spec-p4-b1 3.6).
	SendCard(ctx context.Context, openID string, card Card) (messageID string, err error)
	SendFile(ctx context.Context, openID, path string) (messageID string, err error)
	// Ping checks the credentials; ferry login feishu uses it.
	Ping(ctx context.Context) error
}

// Message is one inbound Feishu message. Only direct chats ("p2p") are served
// this round; group messages are dropped.
type Message struct {
	MessageID  string
	ChatType   string // p2p | group
	OpenID     string
	Text       string
	CreateTime time.Time
}

// The two chat types Feishu reports.
const (
	ChatTypeP2P   = "p2p"
	ChatTypeGroup = "group"
)

type Options struct {
	Config config.FeishuConfig
	Secret string // app secret, read from the credential store
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
	// I18n writes the approval cards. Without it approvals go out as text.
	I18n ports.I18n
	// Lang is the bot language of an endpoint; nil means zh-CN.
	Lang func(fsp.EndpointID) fsp.Lang
}

// Adapter is the Feishu entry point. The SDK is reached only through Client,
// so everything below is exercised without a network.
type Adapter struct {
	cfg    config.FeishuConfig
	clk    clock.Clock
	log    *slog.Logger
	client Client
	i18n   ports.I18n
	lang   func(fsp.EndpointID) fsp.Lang
	// clicks numbers card clicks that came without an event ID or a token.
	clicks atomic.Uint64

	mu     sync.Mutex
	online bool
	detail string
	// downSince is when the connection first failed and has not come back
	// since. It survives Run returning, because the core calls Run again: the
	// five minutes of spec-p3 3.10 span those retries, not one of them.
	downSince time.Time
}

// New builds an adapter on the real SDK (oapi-sdk-go v3.12.0, ws package).
func New(opts Options) (*Adapter, error) {
	c, err := newSDKClient(opts)
	if err != nil {
		return nil, err
	}
	return NewWithClient(opts, c)
}

// NewWithClient builds an adapter on a Client of the caller's choosing, which
// is how the end-to-end tests drive it.
func NewWithClient(opts Options, c Client) (*Adapter, error) {
	if c == nil {
		return nil, errors.New("feishu: a client is required")
	}
	cfg := opts.Config.WithDefaults()
	if cfg.AppID == "" {
		return nil, errors.New("feishu: app_id is required")
	}
	if cfg.OwnerOpenID == "" {
		return nil, errors.New("feishu: owner_open_id is required")
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{cfg: cfg, clk: clk, log: log, client: c, i18n: opts.I18n, lang: opts.Lang}, nil
}

func (a *Adapter) Platform() fsp.Platform { return fsp.PlatformFeishu }

// Limits: Feishu accepts a message at any time, so no window is needed; the
// only ceiling is the send rate.
func (a *Adapter) Limits() ports.DeliveryLimits {
	return ports.DeliveryLimits{NeedsWindow: false, RatePerSec: RatePerSec}
}

// Run holds the long-lived connection open and writes every direct message to
// out. The SDK reconnects on its own; Start only returns once it has given up,
// and then the core calls Run again. A connection that has been down for
// OfflineAfter across those retries reports fsp.ErrSessionExpired, which is
// what makes the core mark the entry point offline and wake the owner.
func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	a.markConnecting()
	err := a.client.Start(ctx,
		func(m Message) { a.deliver(ctx, out, m) },
		func(act CardAction) (string, bool) { return a.cardAction(ctx, out, act) })
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		a.setOffline("the connection closed")
		return nil
	}

	now := a.clk.Now()
	a.mu.Lock()
	if a.downSince.IsZero() {
		a.downSince = now
	}
	down := now.Sub(a.downSince)
	a.online, a.detail = false, err.Error()
	a.mu.Unlock()

	if down >= OfflineAfter {
		return fmt.Errorf("feishu: the connection has been down for %s: %w", down, fsp.ErrSessionExpired)
	}
	return fmt.Errorf("feishu: the long-lived connection stopped: %w", err)
}

// deliver turns one Feishu message into an inbound message. Group chats are
// not served this round, so they never reach the core.
func (a *Adapter) deliver(ctx context.Context, out chan<- fsp.Inbound, m Message) {
	if m.ChatType != ChatTypeP2P {
		a.log.Debug("feishu: message outside a direct chat ignored", "chat_type", m.ChatType)
		return
	}
	if m.OpenID == "" {
		a.log.Warn("feishu: message without a sender ignored", "message", m.MessageID)
		return
	}
	// A message proves the connection works, whatever came before it.
	a.markLive()

	at := m.CreateTime
	if at.IsZero() {
		at = a.clk.Now()
	}
	in := fsp.Inbound{
		Endpoint:   a.endpoint(m.OpenID),
		SenderID:   m.OpenID,
		MessageID:  m.MessageID,
		Text:       m.Text,
		ReplyCtx:   m.MessageID,
		ReceivedAt: at,
	}
	select {
	case out <- in:
	case <-ctx.Done():
	}
}

// Send delivers one message. Feishu needs no reply window: the open ID in the
// endpoint is address enough, and replyCtx is only the message this answers.
// A message whose dedupe key names an approval goes out as a card with
// buttons; if Feishu turns the card down, the same message goes out as text,
// where the typed verdict still works.
func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	openID := msg.Endpoint.PeerID
	if openID == "" {
		return fsp.DeliveryRejected, fmt.Errorf("feishu: no open id in %s: %w", msg.Endpoint, fsp.ErrNotFound)
	}

	if card, ok := a.approvalCard(msg); ok {
		err := a.sendWithRetry(ctx, "card", func() (string, error) { return a.client.SendCard(ctx, openID, card) })
		if err == nil {
			return fsp.DeliveryAccepted, nil
		}
		if ctx.Err() != nil {
			return fsp.DeliveryUnknown, ctx.Err()
		}
		a.log.Warn("feishu: the approval card was not sent; sending it as text", "err", err)
	}

	send := func() (string, error) { return a.client.SendText(ctx, openID, msg.Text) }
	if msg.Kind == fsp.OutFile {
		send = func() (string, error) { return a.client.SendFile(ctx, openID, msg.FilePath) }
	}
	if err := a.sendWithRetry(ctx, string(msg.Kind), send); err != nil {
		if ctx.Err() != nil {
			return fsp.DeliveryUnknown, ctx.Err()
		}
		// Nothing got through, but the outcome is not a refusal either: the
		// outbox retries the same dedupe key rather than dropping the message.
		return fsp.DeliveryUnknown, fmt.Errorf("feishu: send: %w", err)
	}
	return fsp.DeliveryAccepted, nil
}

// sendWithRetry makes one send, retrying it after rateLimitBackoff while
// Feishu keeps refusing it for going too fast.
func (a *Adapter) sendWithRetry(ctx context.Context, kind string, send func() (string, error)) error {
	for try := 0; ; try++ {
		id, err := send()
		if err == nil {
			a.log.Debug("feishu: sent", "kind", kind, "message", id)
			return nil
		}
		if !errors.Is(err, ErrRateLimited) || try >= len(rateLimitBackoff) {
			return err
		}
		select {
		case <-a.clk.After(rateLimitBackoff[try]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Typing: Feishu has no typing indicator, so this is a no-op for good.
func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	return nil
}

func (a *Adapter) Status() ports.AdapterStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ports.AdapterStatus{
		Online:  a.online,
		BotID:   a.cfg.AppID,
		OwnerID: a.cfg.OwnerOpenID,
		Detail:  a.detail,
	}
}

// endpoint builds the endpoint of a direct chat with openID. The bot is the
// app, so the app ID stands in for the bot ID.
func (a *Adapter) endpoint(openID string) fsp.EndpointID {
	return fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: a.cfg.AppID, PeerID: openID}
}

// markConnecting reports the entry point as up again. It leaves downSince
// alone on purpose: the connection is not proven until something arrives
// through it, and the five minutes of spec-p3 3.10 have to span the retries.
func (a *Adapter) markConnecting() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online, a.detail = true, ""
}

// markLive is markConnecting plus the proof: a message came through, so the
// outage, if there was one, is over.
func (a *Adapter) markLive() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online, a.detail = true, ""
	a.downSince = time.Time{}
}

func (a *Adapter) setOffline(detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online, a.detail = false, detail
}

var _ ports.IMAdapter = (*Adapter)(nil)
