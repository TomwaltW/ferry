package notify

import (
	"context"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Options struct {
	Config config.NotifyConfig
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}

func New(opts Options) (ports.Waker, error) { panic("not implemented") }

// Test sends one test reminder through a single channel, for `ferry notify test`.
func Test(ctx context.Context, cfg config.NotifyConfig, channel string) error {
	panic("not implemented")
}
