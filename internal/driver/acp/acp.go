package acp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Timings from the failure table of spec-p1 3.10. They are fields on the driver
// rather than constants only so the tests can shorten them; New always sets
// these values.
const (
	defaultStartTimeout = 30 * time.Second
	defaultCloseGrace   = 2 * time.Second
)

// clientName and clientVersion are what Ferry calls itself in initialize.
const (
	clientName    = "ferry"
	clientVersion = "p1"
)

// driver starts agents that speak ACP over their standard streams.
type driver struct {
	log *slog.Logger
	// startTimeout covers initialize plus session/new or session/load together.
	startTimeout time.Duration
	// closeGrace is how long a closing agent is given to exit on its own.
	closeGrace time.Duration
}

var _ ports.Driver = (*driver)(nil)

// New returns a driver that speaks the Agent Client Protocol over stdio.
func New(log *slog.Logger) ports.Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &driver{
		log:          log,
		startTimeout: defaultStartTimeout,
		closeGrace:   defaultCloseGrace,
	}
}

func (d *driver) Kind() fsp.DriverKind { return fsp.DriverACP }

// Start launches the agent and opens a fresh session in spec.Cwd.
func (d *driver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	startCtx, cancel := context.WithTimeout(ctx, d.startTimeout)
	defer cancel()

	s, err := d.connect(startCtx, spec, ap)
	if err != nil {
		return nil, err
	}

	// No MCP servers: what the agent may reach is decided by its own
	// configuration, not by Ferry.
	resp, err := s.conn.NewSession(startCtx, acp.NewSessionRequest{
		Cwd:        spec.Cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		s.shutdown()
		return nil, fmt.Errorf("acp: open a session with %s: %w", spec.Agent, err)
	}

	s.id = resp.SessionId
	s.adoptConfig(resp.ConfigOptions, resp.Modes)
	go s.watch()
	return s, nil
}

// Resume reattaches to a session the agent already has. Agents that cannot load
// a session report fsp.ErrUnsupported.
func (d *driver) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	startCtx, cancel := context.WithTimeout(ctx, d.startTimeout)
	defer cancel()

	s, err := d.connect(startCtx, spec, ap)
	if err != nil {
		return nil, err
	}
	if !s.caps.Load {
		s.shutdown()
		return nil, fmt.Errorf("acp: %s cannot load a session: %w", spec.Agent, fsp.ErrUnsupported)
	}

	// Loading replays the old conversation as session updates. They are
	// history, so the session drops them until the load has returned; the SDK
	// guarantees they are all delivered by then.
	s.id = acp.SessionId(agentSessionID)
	s.mu.Lock()
	s.loading = true
	s.mu.Unlock()

	resp, err := s.conn.LoadSession(startCtx, acp.LoadSessionRequest{
		SessionId:  acp.SessionId(agentSessionID),
		Cwd:        spec.Cwd,
		McpServers: []acp.McpServer{},
	})

	s.mu.Lock()
	s.loading = false
	s.mu.Unlock()

	if err != nil {
		s.shutdown()
		return nil, fmt.Errorf("acp: load session %s from %s: %w", agentSessionID, spec.Agent, err)
	}

	s.adoptConfig(resp.ConfigOptions, resp.Modes)
	go s.watch()
	return s, nil
}

// List asks the agent which sessions it already has, narrowed to spec.Cwd when
// one is given.
func (d *driver) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	startCtx, cancel := context.WithTimeout(ctx, d.startTimeout)
	defer cancel()

	s, err := d.connect(startCtx, spec, nil)
	if err != nil {
		return nil, err
	}
	defer s.shutdown()

	if !s.caps.List {
		return nil, fmt.Errorf("acp: %s cannot list sessions: %w", spec.Agent, fsp.ErrUnsupported)
	}

	var (
		out    []ports.AgentSessionInfo
		cursor *string
	)
	for {
		req := acp.ListSessionsRequest{Cursor: cursor}
		if spec.Cwd != "" {
			req.Cwd = &spec.Cwd
		}
		resp, err := s.conn.ListSessions(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("acp: list the sessions of %s: %w", spec.Agent, err)
		}

		for _, info := range resp.Sessions {
			out = append(out, ports.AgentSessionInfo{
				AgentSessionID: string(info.SessionId),
				Title:          deref(info.Title),
				Cwd:            info.Cwd,
				UpdatedAt:      parseTime(info.UpdatedAt),
			})
		}

		if resp.NextCursor == nil || *resp.NextCursor == "" {
			return out, nil
		}
		cursor = resp.NextCursor
	}
}

// connect starts the agent and completes initialize. On any failure the process
// is torn down before the error is returned.
func (d *driver) connect(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (*session, error) {
	proc, err := spawn(spec.Command, spec.Cwd, spec.Env, d.log)
	if err != nil {
		return nil, err
	}

	base, cancel := context.WithCancel(context.Background())
	s := &session{
		log:        d.log,
		approver:   ap,
		proc:       proc,
		events:     newQueue(),
		closeGrace: d.closeGrace,
		baseCtx:    base,
		baseCancel: cancel,
	}
	// The SDK dispatches from a goroutine started inside the constructor, so
	// its SetLogger races with that goroutine and is left alone; the SDK's own
	// diagnostics go to the default slog handler.
	s.conn = acp.NewClientSideConnection(s, proc.stdin, proc.stdout)

	// Ferry declares neither fs nor terminal capabilities: the agent works in
	// its own directory and Ferry does not proxy file or shell access for it.
	resp, err := s.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: clientName, Version: clientVersion},
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		s.shutdown()
		return nil, fmt.Errorf("acp: initialize %s: %w", spec.Agent, err)
	}

	caps := resp.AgentCapabilities
	s.caps = ports.Caps{
		Load:   caps.LoadSession,
		Resume: caps.SessionCapabilities.Resume != nil,
		List:   caps.SessionCapabilities.List != nil,
		Cancel: true,
	}
	return s, nil
}

// adoptConfig records the session options an agent offered, which is also what
// decides whether SetConfig has anything to work with.
func (s *session) adoptConfig(options []acp.SessionConfigOption, modes *acp.SessionModeState) {
	s.configs = options
	s.modes = modes
	s.caps.Config = len(options) > 0 || modes != nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseTime reads a registry timestamp, giving up quietly: an agent that dates
// its sessions oddly should still be listable.
func parseTime(s *string) time.Time {
	if s == nil || *s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return time.Time{}
	}
	return t
}
