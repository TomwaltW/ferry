package approval

import (
	"log/slog"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Options struct {
	Policy      config.PolicyConfig
	Roots       []string // normalised workspace roots
	DefaultLang fsp.Lang
	Store       ports.Store
	Outbox      ports.Outbox
	I18n        ports.I18n
	Clock       clock.Clock
	Log         *slog.Logger
}

func New(opts Options) ports.Approvals { panic("not implemented") }
