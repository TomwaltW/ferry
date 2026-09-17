package registry

import (
	"log/slog"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/ports"
)

// New resolves configured agents into launch specs, using the committed
// registry snapshot for agents referenced by registry name.
func New(cfg config.RegistryConfig, agents []config.AgentConfig, home string, log *slog.Logger) (ports.Registry, error) {
	panic("not implemented")
}
