package v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The v2 methods, in the order a session goes through them.
const (
	MethodInitialize    = "initialize"
	MethodAuthLogin     = "auth/login"
	MethodAuthLogout    = "auth/logout"
	MethodSessionNew    = "session/new"
	MethodSessionResume = "session/resume"
	MethodSessionPrompt = "session/prompt"
	MethodSessionCancel = "session/cancel"
	MethodSessionList   = "session/list"
	MethodSessionClose  = "session/close"
	// MethodSetConfigOption replaces v1's session/set_mode: in v2 the session
	// mode is one config option among the others.
	MethodSetConfigOption = "session/set_config_option"
	// NotifySessionUpdate carries accepted messages, state updates and
	// progress. An idle state_update ends the turn and names its stop reason.
	NotifySessionUpdate = "session/update"
)

// Timings from the failure table of spec-p1 3.10, kept as fields on the driver
// only so the tests can shorten them.
const (
	defaultStartTimeout = 30 * time.Second
	defaultCloseGrace   = 2 * time.Second
)

// clientName and clientVersion are what Ferry calls itself in initialize.
const (
	clientName    = "ferry"
	clientVersion = "p3"
)

// ErrNegotiatedV1 reports an agent that answered a v2 initialize with an older
// protocol version. The acp_version = auto path closes such an agent and starts
// it again over v1; acp_version = v2 passes the error on.
var ErrNegotiatedV1 = errors.New("acp/v2: the agent negotiated protocol version 1")

// ErrLoginRequired reports an agent that will not work until somebody logs in
// at a terminal. LoginRequiredKey is the message Ferry sends back for it.
var ErrLoginRequired = errors.New("acp/v2: the agent needs an interactive login")

// LoginRequiredKey is the i18n key of the reply to ErrLoginRequired.
const LoginRequiredKey = "acp.login_required"

// modeKey is the SetConfig key the core reserves for an agent's session mode.
// In v2 it needs no special handling: the mode is a config option whose id is
// exactly this, so the generic path already finds it.
const modeKey = "mode"

// New returns a driver that speaks version 2 of the Agent Client Protocol over
// the agent's standard streams.
func New(log *slog.Logger) ports.Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &driver{log: log, startTimeout: defaultStartTimeout, closeGrace: defaultCloseGrace}
}

type driver struct {
	log *slog.Logger
	// startTimeout covers initialize, any login and session/new together.
	startTimeout time.Duration
	// closeGrace is how long a closing agent is given to exit on its own.
	closeGrace time.Duration
}

var _ ports.Driver = (*driver)(nil)

func (d *driver) Kind() fsp.DriverKind { return fsp.DriverACP }

// Start launches the agent and opens a fresh session in spec.Cwd.
func (d *driver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	startCtx, cancel := context.WithTimeout(ctx, d.startTimeout)
	defer cancel()

	s, err := d.connect(startCtx, spec, ap)
	if err != nil {
		return nil, err
	}

	var resp NewSessionResult
	err = s.conn.call(startCtx, MethodSessionNew, NewSessionParams{Cwd: spec.Cwd, McpServers: noServers()}, &resp)
	if err != nil {
		s.shutdown()
		if isAuthRequired(err) {
			return nil, fmt.Errorf("acp/v2: %s: %w", spec.Agent, ErrLoginRequired)
		}
		return nil, fmt.Errorf("acp/v2: open a session with %s: %w", spec.Agent, err)
	}

	s.id = resp.SessionID
	s.adoptConfig(resp.ConfigOptions)
	go s.watch()
	return s, nil
}

// Resume reattaches to a session the agent already has. Ferry does not ask for
// the history to be replayed: the core keeps its own transcript.
func (d *driver) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	startCtx, cancel := context.WithTimeout(ctx, d.startTimeout)
	defer cancel()

	s, err := d.connect(startCtx, spec, ap)
	if err != nil {
		return nil, err
	}
	if !s.caps.Resume {
		s.shutdown()
		return nil, fmt.Errorf("acp/v2: %s cannot resume a session: %w", spec.Agent, fsp.ErrUnsupported)
	}

	// An agent may still send history of its own accord. Anything that arrives
	// before session/resume has returned is a transcript of the past, so the
	// session drops it rather than passing it off as news.
	s.id = agentSessionID
	s.mu.Lock()
	s.loading = true
	s.mu.Unlock()

	var resp ResumeSessionResult
	err = s.conn.call(startCtx, MethodSessionResume, ResumeSessionParams{
		SessionID:  agentSessionID,
		Cwd:        spec.Cwd,
		McpServers: noServers(),
	}, &resp)

	s.mu.Lock()
	s.loading = false
	s.mu.Unlock()

	if err != nil {
		s.shutdown()
		if isAuthRequired(err) {
			return nil, fmt.Errorf("acp/v2: %s: %w", spec.Agent, ErrLoginRequired)
		}
		return nil, fmt.Errorf("acp/v2: resume session %s of %s: %w", agentSessionID, spec.Agent, err)
	}

	s.adoptConfig(resp.ConfigOptions)
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
		return nil, fmt.Errorf("acp/v2: %s cannot list sessions: %w", spec.Agent, fsp.ErrUnsupported)
	}

	var (
		out    []ports.AgentSessionInfo
		cursor string
	)
	for {
		var resp ListSessionsResult
		req := ListSessionsParams{Cwd: spec.Cwd, Cursor: cursor}
		if err := s.conn.call(ctx, MethodSessionList, req, &resp); err != nil {
			return nil, fmt.Errorf("acp/v2: list the sessions of %s: %w", spec.Agent, err)
		}

		for _, info := range resp.Sessions {
			out = append(out, ports.AgentSessionInfo{
				AgentSessionID: info.SessionID,
				Title:          info.Title,
				Cwd:            info.Cwd,
				UpdatedAt:      parseTime(info.UpdatedAt),
			})
		}

		if resp.NextCursor == "" {
			return out, nil
		}
		cursor = resp.NextCursor
	}
}

// connect starts the agent, negotiates the protocol version and logs in when
// the agent can do that by itself. On any failure the process is torn down
// before the error is returned.
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
	s.conn = newConn(proc.stdin, d.log)
	s.conn.handle = s.onRequest
	s.conn.notify = s.onNotify
	go s.conn.serve(proc.stdout)

	var resp InitializeResult
	err = s.conn.call(ctx, MethodInitialize, InitializeParams{
		ProtocolVersion: Version,
		Capabilities:    ClientCapabilities{},
		Info:            &Implementation{Name: clientName, Version: clientVersion},
	}, &resp)
	if err != nil {
		s.shutdown()
		return nil, fmt.Errorf("acp/v2: initialize %s: %w", spec.Agent, err)
	}

	// An agent that answers with an older version does not speak v2. Saying so
	// plainly is what lets the auto path start it again over v1.
	if resp.ProtocolVersion < Version {
		s.shutdown()
		return nil, fmt.Errorf("acp/v2: %s answered initialize with version %d: %w",
			spec.Agent, resp.ProtocolVersion, ErrNegotiatedV1)
	}

	if err := s.login(ctx, spec.Agent, resp.AuthMethods); err != nil {
		s.shutdown()
		return nil, err
	}

	// v2 advertises the session methods as a whole: an agent that offers
	// session at all commits to list, resume and close.
	if resp.Capabilities.Session != nil {
		s.caps = ports.Caps{Load: true, Resume: true, List: true, Cancel: true}
	} else {
		s.caps = ports.Caps{Cancel: true}
	}
	return s, nil
}

// noServers is the empty MCP server list every session is opened with: what the
// agent may reach is decided by its own configuration, not by Ferry.
func noServers() []json.RawMessage { return []json.RawMessage{} }

// parseTime reads a session's timestamp, giving up quietly: an agent that dates
// its sessions oddly should still be listable.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// mergeCancel returns a context that ends when either parent does.
func mergeCancel(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	if b == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(b, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
