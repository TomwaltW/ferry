package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// driver drives agents that are already running as a local HTTP service. It
// starts nothing: the user runs `opencode serve` or `qwen serve` themselves,
// which is what lets the terminal and Ferry share one session.
type driver struct {
	log *slog.Logger
	clk clock.Clock
}

var _ ports.Driver = (*driver)(nil)

// New returns the driver for agents already running as a local service
// (opencode serve, qwen serve). Kind() reports fsp.DriverServer.
func New(log *slog.Logger) ports.Driver { return newDriver(log, clock.Real()) }

// newDriver is New over an injected clock, which is what lets the tests drive
// the reconnect schedule without waiting for it.
func newDriver(log *slog.Logger, clk clock.Clock) *driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if clk == nil {
		clk = clock.Real()
	}
	return &driver{log: log, clk: clk}
}

func (d *driver) Kind() fsp.DriverKind { return fsp.DriverServer }

// Start opens a fresh session on the service. A service that is not running
// fails here, which is what the core turns into server.unreachable.
func (d *driver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	be, err := newBackend(spec, d.log)
	if err != nil {
		return nil, err
	}
	id, err := be.create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return d.attach(ctx, be, id, ap)
}

// Resume picks up a session the service already has, which is the session the
// user may also have open in a terminal.
func (d *driver) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	be, err := newBackend(spec, d.log)
	if err != nil {
		return nil, err
	}
	id, err := be.attach(ctx, spec, agentSessionID)
	if err != nil {
		return nil, err
	}
	return d.attach(ctx, be, id, ap)
}

// List enumerates the sessions the service has. Handoff marks every one of them
// shared: a second-tier session may be driven from the terminal at the same
// time, so nothing here checks whether it is in use.
func (d *driver) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	be, err := newBackend(spec, d.log)
	if err != nil {
		return nil, err
	}
	return be.list(ctx)
}

// attach opens the session's event stream and starts reading it. The first
// connection is made here rather than in the reconnect loop so that a service
// that cannot be reached fails the call instead of parking a session nobody
// asked for.
func (d *driver) attach(ctx context.Context, be backend, id string, ap ports.Approver) (ports.AgentSession, error) {
	base, cancel := context.WithCancel(context.Background())

	body, err := be.openEvents(base, id)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("server: open the event stream of %s session %s: %w", be.kind(), id, err)
	}

	s := &session{
		be:         be,
		id:         id,
		ap:         ap,
		q:          newQueue(),
		clk:        d.clk,
		log:        d.log,
		baseCtx:    base,
		baseCancel: cancel,
		pending:    map[string]context.CancelFunc{},
	}
	go s.run(body)
	return s, nil
}

// Health reports whether the local service behind spec answers. The registry
// uses it to decide whether a server agent is available; the deadline is the
// caller's.
func Health(ctx context.Context, spec ports.ServerSpec) error {
	be, err := newBackend(ports.SessionSpec{Server: &spec}, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	return be.health(ctx)
}

// newBackend picks the protocol for a spec. An agent without a server block is
// not this driver's to run.
func newBackend(spec ports.SessionSpec, log *slog.Logger) (backend, error) {
	if spec.Server == nil {
		return nil, fmt.Errorf("server: agent %q has no server block: %w", spec.Agent, fsp.ErrUnsupported)
	}
	switch spec.Server.Kind {
	case "opencode":
		return newOpenCode(*spec.Server, log)
	case "qwen":
		return newQwen(*spec.Server, log)
	default:
		return nil, fmt.Errorf("server: no service named %q, only opencode and qwen: %w", spec.Server.Kind, fsp.ErrUnsupported)
	}
}
