package registry

import (
	"context"
	"os"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/driver/server"
	"github.com/TomwaltW/ferry/internal/ports"
)

// serverHealthTimeout caps the health check behind Agents. A service on this
// machine either answers at once or is not running, so waiting longer only
// makes `/agents` slow.
const serverHealthTimeout = 2 * time.Second

// sourceServer is what a server agent reports as its source, alongside the
// registry and command sources of the first tier.
const sourceServer = "server"

// reasonUnreachable explains an agent whose service is not running. Like the
// other reasons it is plain English: the registry has no i18n catalogue, and
// the core renders it.
const reasonUnreachable = "not running, start it first"

// sources counts how an agent says it should be started. Exactly one of the
// three is allowed.
func sources(ac config.AgentConfig) int {
	n := 0
	if ac.Registry != "" {
		n++
	}
	if len(ac.Command) > 0 {
		n++
	}
	if ac.Server != nil {
		n++
	}
	return n
}

// serverSpec is the launch spec of an agent that is already running as a local
// service: no command, and the secret read out of the environment variable the
// configuration named. Ferry never stores the secret itself.
func serverSpec(ac config.AgentConfig) ports.SessionSpec {
	s := ports.ServerSpec{Kind: ac.Server.Kind, URL: ac.Server.URL}
	if ac.Server.SecretEnv != "" {
		s.Secret = os.Getenv(ac.Server.SecretEnv)
	}
	return ports.SessionSpec{Agent: ac.Name, Server: &s}
}

// serverInfo reports whether a server agent can be used, which for a service
// Ferry does not start means asking it.
func (r *resolver) serverInfo(ctx context.Context, ac config.AgentConfig) ports.AgentInfo {
	info := ports.AgentInfo{Name: ac.Name, Display: ac.Name, Source: sourceServer}

	spec := serverSpec(ac)
	hctx, cancel := context.WithTimeout(ctx, serverHealthTimeout)
	defer cancel()

	if err := server.Health(hctx, *spec.Server); err != nil {
		r.log.Debug("a server agent did not answer", "agent", ac.Name, "url", ac.Server.URL, "err", err)
		info.Reason = reasonUnreachable
		return info
	}
	info.Available = true
	return info
}
