package handoff

import (
	"log/slog"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Options struct {
	Roots    []string
	Agents   []config.AgentConfig
	Profiles []config.HandoffProfile // configuration entries, merged over Profiles with configuration winning
	Lookback time.Duration
	Registry ports.Registry
	Drivers  map[fsp.DriverKind]ports.Driver
	Store    ports.Store
	Clock    clock.Clock
	Log      *slog.Logger
}

func New(opts Options) ports.Handoff { panic("not implemented") }
