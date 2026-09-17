package wechat

import (
	"context"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
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

// Adapter speaks the iLink bot API. Its fields are defined by the P1 T1 task.
type Adapter struct{}

func New(opts Options) (*Adapter, error) { panic("not implemented") }

func NewLogin(cfg config.WeChatConfig, clk clock.Clock) ports.Login { panic("not implemented") }

func (a *Adapter) Platform() fsp.Platform { panic("not implemented") }

func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error { panic("not implemented") }

func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	panic("not implemented")
}

func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	panic("not implemented")
}

func (a *Adapter) Status() ports.AdapterStatus { panic("not implemented") }

var _ ports.IMAdapter = (*Adapter)(nil)
