package channelhost

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// A server that exits is restarted on this cadence, and given up on after
// RestartLimit tries; the adapter then reports itself offline.
const (
	RestartInterval = 60 * time.Second
	RestartLimit    = 5
)

type Options struct {
	Config config.ChannelServerConfig
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}

// Adapter runs one third-party channel server and presents it as an IM entry
// point on platform channel-<name>. Its behaviour belongs to the P3-T3 task.
type Adapter struct {
	opts Options
}

func New(opts Options) (*Adapter, error) {
	return nil, fmt.Errorf("channelhost: New: %w", fsp.ErrUnsupported)
}

func (a *Adapter) Platform() fsp.Platform { return a.opts.Config.Platform() }

// Limits: a channel server takes a reply whenever one is ready.
func (a *Adapter) Limits() ports.DeliveryLimits {
	return ports.DeliveryLimits{NeedsWindow: false}
}

func (a *Adapter) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	panic("channelhost: Run is implemented by P3-T3")
}

func (a *Adapter) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	panic("channelhost: Send is implemented by P3-T3")
}

// Typing: the channels protocol has no typing indicator.
func (a *Adapter) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	return nil
}

func (a *Adapter) Status() ports.AdapterStatus {
	panic("channelhost: Status is implemented by P3-T3")
}

var _ ports.IMAdapter = (*Adapter)(nil)
