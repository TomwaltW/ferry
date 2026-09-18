package feishu

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// KeyringPlatform is the keyring entry holding the app secret; the
// configuration file only ever carries the app ID.
const KeyringPlatform = "feishu"

// RatePerSec is the send rate spec-p3 3.4 fixes for Feishu; unlike WeChat it
// needs no reply window, only a ceiling on how fast messages leave.
const RatePerSec = 5

// OfflineAfter is how long the long-lived connection may stay down before the
// adapter reports itself offline and wakes the owner.
const OfflineAfter = 5 * time.Minute

// Client is the Feishu SDK as this package uses it, so that tests can swap in
// a fake without a network.
type Client interface {
	// Start opens the long-lived connection and blocks until ctx ends,
	// handing every inbound message to onMessage.
	Start(ctx context.Context, onMessage func(Message)) error
	SendText(ctx context.Context, openID, text string) (messageID string, err error)
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
	Secret string // app secret, read from the keyring
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}

// Adapter is the Feishu entry point. Its behaviour belongs to the P3-T1 task;
// what stands here is the shape the rest of P3 compiles against.
type Adapter struct {
	opts   Options
	client Client
}

// New builds an adapter on the real SDK (oapi-sdk-go v3.12.0, ws package).
func New(opts Options) (*Adapter, error) {
	return nil, fmt.Errorf("feishu: New: %w", fsp.ErrUnsupported)
}

// NewWithClient builds an adapter on a Client of the caller's choosing, which
// is how the end-to-end tests drive it.
func NewWithClient(opts Options, c Client) (*Adapter, error) {
	return nil, fmt.Errorf("feishu: NewWithClient: %w", fsp.ErrUnsupported)
}

// LoginInteractive asks for the app ID, the app secret and the owner's open
// ID, verifies them with Ping, puts the secret in the keyring and writes the
// rest to the configuration.
func LoginInteractive(ctx context.Context, in io.Reader, out io.Writer, home string) error {
	return fmt.Errorf("feishu: LoginInteractive: %w", fsp.ErrUnsupported)
}

func (a *Adapter) Platform() fsp.Platform { return fsp.PlatformFeishu }

// Limits: Feishu accepts a message at any time, so no window is needed; the
// only ceiling is the send rate.
func (a *Adapter) Limits() ports.DeliveryLimits {
	return ports.DeliveryLimits{NeedsWindow: false, RatePerSec: RatePerSec}
}

func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	panic("feishu: Run is implemented by P3-T1")
}

func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	panic("feishu: Send is implemented by P3-T1")
}

// Typing: Feishu has no typing indicator, so this is a no-op for good.
func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	return nil
}

func (a *Adapter) Status() ports.AdapterStatus {
	panic("feishu: Status is implemented by P3-T1")
}

var _ ports.IMAdapter = (*Adapter)(nil)
