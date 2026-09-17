package outbox

import (
	"log/slog"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Options struct {
	Config config.OutboxConfig
	IM     ports.IMAdapter
	Store  ports.Store
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}

func New(opts Options) ports.Outbox { panic("not implemented") }
