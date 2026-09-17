package core

import (
	"context"
	"log/slog"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

type Deps struct {
	Config    config.Config
	Home      string
	IM        ports.IMAdapter
	Drivers   map[fsp.DriverKind]ports.Driver
	Registry  ports.Registry
	Store     ports.Store
	Outbox    ports.Outbox
	ACL       ports.ACL
	Approvals ports.Approvals
	Commands  ports.Commands
	I18n      ports.I18n
	Clock     clock.Clock
	Log       *slog.Logger
}

type Status struct {
	IMOnline  bool
	IMDetail  string
	BotID     string
	Sessions  int
	Running   int
	UpdatedAt time.Time
}

// Daemon routes inbound messages to sessions. Its fields are defined by the
// P1 T5 task.
type Daemon struct{}

func New(d Deps) *Daemon { panic("not implemented") }

// Run blocks until ctx ends. When the IM adapter returns fsp.ErrSessionExpired
// the daemon stays up: it marks the adapter offline, writes an audit entry and
// retries IM.Run every 60 seconds.
func (d *Daemon) Run(ctx context.Context) error { panic("not implemented") }

func (d *Daemon) Status() Status { panic("not implemented") }
