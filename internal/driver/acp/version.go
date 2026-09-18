package acp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/config"
	v2 "github.com/TomwaltW/ferry/internal/driver/acp/v2"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Versions answers which protocol version an agent speaks: config.ACPVersionV1,
// config.ACPVersionV2 or config.ACPVersionAuto. Anything else, an agent that is
// not configured included, counts as v1, which is the default of spec-p3 3.2.
type Versions func(agent string) string

// FixedVersion is a Versions that answers the same for every agent. It is what a
// caller with a single agent, or a test, wants.
func FixedVersion(version string) Versions {
	return func(string) string { return version }
}

// NewVersioned returns a driver that speaks whichever protocol version each
// agent was configured for. New stays as it was: a v1 driver and nothing else.
//
// The auto path tries v2 first. An agent that negotiates back to v1 has already
// been shut down by the v2 driver, so the retry starts it fresh.
func NewVersioned(log *slog.Logger, versions Versions) ports.Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if versions == nil {
		versions = FixedVersion(config.DefaultACPVersion)
	}
	return &versioned{log: log, versions: versions, v1: New(log), v2: v2.New(log)}
}

type versioned struct {
	log      *slog.Logger
	versions Versions
	v1       ports.Driver
	v2       ports.Driver
}

var _ ports.Driver = (*versioned)(nil)

func (d *versioned) Kind() fsp.DriverKind { return fsp.DriverACP }

func (d *versioned) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	return pick(d, spec.Agent, func(inner ports.Driver) (ports.AgentSession, error) {
		return inner.Start(ctx, spec, ap)
	})
}

func (d *versioned) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	return pick(d, spec.Agent, func(inner ports.Driver) (ports.AgentSession, error) {
		return inner.Resume(ctx, spec, agentSessionID, ap)
	})
}

func (d *versioned) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	return pick(d, spec.Agent, func(inner ports.Driver) ([]ports.AgentSessionInfo, error) {
		return inner.List(ctx, spec)
	})
}

// pick runs one driver call against the version the agent was configured for.
// The three methods differ only in what they return, which is what the type
// parameter is for.
func pick[T any](d *versioned, agent string, call func(ports.Driver) (T, error)) (T, error) {
	switch d.versions(agent) {
	case config.ACPVersionV2:
		return call(d.v2)

	case config.ACPVersionAuto:
		out, err := call(d.v2)
		if err == nil || !errors.Is(err, v2.ErrNegotiatedV1) {
			return out, err
		}
		d.log.Debug("the agent negotiated ACP v1, starting it again over v1", "agent", agent)
		return call(d.v1)

	default:
		return call(d.v1)
	}
}
