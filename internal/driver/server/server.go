package server

import (
	"log/slog"

	"github.com/TomwaltW/ferry/internal/ports"
)

// New returns the driver for agents already running as a local service
// (opencode serve, qwen serve). Kind() reports fsp.DriverServer.
func New(log *slog.Logger) ports.Driver { panic("not implemented") }
