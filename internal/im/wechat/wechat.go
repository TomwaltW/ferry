package wechat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Options struct {
	Config config.WeChatConfig
	Creds  ports.Credentials
	KV     ports.KV
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}

// renderPlainMode is the config value that asks for Markdown to be flattened
// before it reaches WeChat, which renders none of it.
const renderPlainMode = "plain"

// Adapter speaks the iLink bot API. Its fields are defined by the P1 T1 task.
type Adapter struct {
	cfg   config.WeChatConfig
	creds ports.Credentials
	kv    ports.KV
	home  string
	clk   clock.Clock
	log   *slog.Logger
	cli   *client
	cdn   *cdnClient

	mu     sync.Mutex
	online bool
	detail string
}

func New(opts Options) (*Adapter, error) {
	if opts.KV == nil {
		return nil, errors.New("wechat: a KV store is required")
	}
	cfg := opts.Config
	if cfg.ChannelVersion == "" {
		cfg.ChannelVersion = ilinkwire.DefaultChannelVersion
	}
	if cfg.Render == "" {
		cfg.Render = renderPlainMode
	}

	base := opts.Creds.BaseURL
	if base == "" {
		base = cfg.BaseURL
	}
	if base == "" {
		base = ilinkwire.DefaultBaseURL
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	a := &Adapter{
		cfg:   cfg,
		creds: opts.Creds,
		kv:    opts.KV,
		home:  opts.Home,
		clk:   clk,
		log:   log,
		cli:   newClient(base, opts.Creds.BotToken, cfg.ChannelVersion),
		cdn:   newCDNClient(cfg.CDNBaseURL),
	}
	if opts.Creds.BotToken == "" {
		a.detail = "not logged in"
	} else {
		a.online = true
	}
	return a, nil
}

func NewLogin(cfg config.WeChatConfig, clk clock.Clock) ports.Login { return newLogin(cfg, clk) }

func (a *Adapter) Platform() fsp.Platform { return fsp.PlatformWeChat }

func (a *Adapter) Status() ports.AdapterStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ports.AdapterStatus{
		Online:  a.online,
		BotID:   a.creds.BotID,
		OwnerID: a.creds.OwnerID,
		Detail:  a.detail,
	}
}

// endpoint builds the endpoint of a conversation with peer.
func (a *Adapter) endpoint(peer string) fsp.EndpointID {
	return fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: a.creds.BotID, PeerID: peer}
}

func (a *Adapter) setOnline() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online = true
	a.detail = ""
}

func (a *Adapter) setOffline(detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online = false
	a.detail = detail
}

// cursorKey names the getupdates cursor of this bot in the KV store.
func (a *Adapter) cursorKey() string {
	return fmt.Sprintf("wechat/%s/cursor", a.creds.BotID)
}

// typingKey names the cached typing ticket of one user.
func (a *Adapter) typingKey(user string) string {
	return fmt.Sprintf("wechat/%s/typing/%s", a.creds.BotID, user)
}

func (a *Adapter) kvGet(ctx context.Context, key string) (string, bool) {
	v, err := a.kv.GetKV(ctx, key)
	if err != nil {
		if !errors.Is(err, fsp.ErrNotFound) {
			a.log.Warn("wechat: read kv", "key", key, "error", err)
		}
		return "", false
	}
	return v, true
}

func (a *Adapter) kvSet(ctx context.Context, key, value string) {
	if err := a.kv.SetKV(ctx, key, value); err != nil {
		a.log.Warn("wechat: write kv", "key", key, "error", err)
	}
}

var _ ports.IMAdapter = (*Adapter)(nil)
