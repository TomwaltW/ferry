package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/registry"
)

// closeGrace is how long the agent is given to exit on its own once the editor
// has gone and its stdin has been closed.
const closeGrace = 2 * time.Second

// Options is the command line of `ferry proxy [--allow-inject]
// [--remote-approve] [--title <name>] <agent>` plus the streams the editor
// started it on.
type Options struct {
	Agent string
	Title string
	// AllowInject lets input from chat reach the agent while the editor is
	// idle; without it such input is answered with inject.disabled.
	AllowInject bool
	// RemoteApprove sends permission requests to chat instead of the editor.
	RemoteApprove bool
	Home          string
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	Log           *slog.Logger
}

// proxy is one run of the command: one editor on In/Out, one real agent
// underneath, and one control connection per session it mirrors.
type proxy struct {
	opts  Options
	log   *slog.Logger
	home  string
	lang  fsp.Lang
	texts *i18n.Bundle

	// baseCtx bounds everything the proxy does of its own accord: injected
	// turns, cancellations and the options the daemon sets. It ends with the
	// run, never with one editor request.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	proc       *process
	agentConn  *acp.ClientSideConnection
	editorConn *acp.AgentSideConnection
	// ready closes once both connections exist. The agent's side of the proxy
	// is built first, so only the handlers that call back into the editor have
	// to wait for it.
	ready chan struct{}

	// daemonUp records whether a daemon answered when the proxy started. When
	// it did not, the run forwards transparently and mirrors nothing.
	daemonUp bool

	mu       sync.Mutex
	sessions map[acp.SessionId]*psession
	// closeCap records whether the agent said it supports session/close, which
	// is the only way the daemon's close message can reach it.
	closeCap bool

	shutdownOnce sync.Once
}

// Run serves the editor on In/Out, drives the real agent underneath and mirrors
// every session to the daemon. It returns once the editor disconnects. A daemon
// that is not running is not an error: the proxy then forwards transparently
// and says so on Err.
func Run(ctx context.Context, opts Options) error {
	p, err := start(ctx, opts)
	if err != nil {
		return err
	}
	return p.serve(ctx)
}

// start resolves the agent, launches it and wires both connections up. Whatever
// fails here fails the command: an editor that cannot reach its agent is better
// off hearing about it than talking to a proxy with nothing behind it.
func start(ctx context.Context, opts Options) (*proxy, error) {
	if opts.Agent == "" {
		return nil, fmt.Errorf("proxy: no agent was named")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Err == nil {
		opts.Err = os.Stderr
	}
	home := opts.Home
	if home == "" {
		home = config.Home()
	}

	cfg, err := config.Load(config.Path(home))
	if err != nil {
		return nil, fmt.Errorf("proxy: read the configuration: %w", err)
	}
	lang := cfg.Lang
	if lang == "" {
		lang = fsp.LangZH
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("proxy: find the working directory: %w", err)
	}

	reg, err := registry.New(cfg.Registry, cfg.Agents, home, opts.Log)
	if err != nil {
		return nil, fmt.Errorf("proxy: read the agent registry: %w", err)
	}
	spec, err := reg.Resolve(ctx, opts.Agent)
	if err != nil {
		return nil, fmt.Errorf("proxy: resolve %s: %w", opts.Agent, err)
	}
	if spec.Server != nil {
		// A service the editor could talk to itself: there is no child process
		// to sit in front of, and ACP over HTTP is not what an editor starts.
		return nil, fmt.Errorf("proxy: %s runs as a server, which proxy cannot front: %w", opts.Agent, fsp.ErrUnsupported)
	}
	spec.Cwd = cwd

	proc, err := spawn(spec.Command, cwd, spec.Env, opts.Log)
	if err != nil {
		return nil, err
	}

	base, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &proxy{
		baseCtx:    base,
		baseCancel: cancel,
		opts:       opts,
		log:        opts.Log,
		home:       home,
		lang:       lang,
		texts:      i18n.Default(),
		proc:       proc,
		ready:      make(chan struct{}),
		sessions:   make(map[acp.SessionId]*psession),
	}

	// The SDK dispatches from a goroutine started inside each constructor, so
	// SetLogger would race with it and is left alone; the SDK's own diagnostics
	// go to the default slog handler.
	p.agentConn = acp.NewClientSideConnection(agentSide{p}, proc.stdin, proc.stdout)

	// Whether anything will be mirrored is settled before the editor can ask
	// for a session, so that attach never reads a half-built proxy.
	p.probeDaemon()

	p.editorConn = acp.NewAgentSideConnection(editorSide{p}, opts.Out, opts.In)
	close(p.ready)
	return p, nil
}

// probeDaemon says once, up front, whether anything will be mirrored. Each
// session attaches on a connection of its own later; this one only answers the
// question the editor's user needs answered immediately.
func (p *proxy) probeDaemon() {
	client, err := control.Dial(p.home)
	if err == nil {
		client.Close()
		p.daemonUp = true
		return
	}
	if !errors.Is(err, fsp.ErrNotFound) {
		p.log.Warn("could not reach the daemon", "err", err)
	}
	fmt.Fprintln(p.opts.Err, p.texts.T(p.lang, "cli.proxy_no_daemon", nil))
}

// serve waits for whichever end goes away first and then tears the run down.
func (p *proxy) serve(ctx context.Context) error {
	select {
	case <-p.editorConn.Done():
		p.log.Debug("the editor disconnected")
	case <-p.proc.done:
		p.log.Debug("the agent exited")
	case <-ctx.Done():
	}
	p.shutdown()
	return nil
}

// shutdown cancels whatever was running, tells the daemon each session is over
// and stops the agent. It is safe to call more than once.
func (p *proxy) shutdown() {
	p.shutdownOnce.Do(func() {
		p.mu.Lock()
		sessions := make([]*psession, 0, len(p.sessions))
		for _, s := range p.sessions {
			sessions = append(sessions, s)
		}
		p.mu.Unlock()

		ctx, stop := context.WithTimeout(context.Background(), closeGrace)
		for _, s := range sessions {
			// The editor is gone, so nobody is waiting on these turns any more.
			if err := p.agentConn.Cancel(ctx, acp.CancelNotification{SessionId: s.agentID}); err != nil {
				p.log.Debug("could not cancel a turn while closing", "session", string(s.agentID), "err", err)
			}
			s.detach()
		}
		stop()

		p.baseCancel()
		p.proc.endInput()
		select {
		case <-p.proc.done:
		case <-time.After(closeGrace):
			p.log.Debug("the agent did not exit in time, killing it")
			p.proc.kill()
			<-p.proc.done
		}
	})
}

// editor returns the connection to the editor once it exists. Only the handlers
// the agent drives need it: they can run before start has finished wiring both
// ends together.
func (p *proxy) editor(ctx context.Context) (*acp.AgentSideConnection, error) {
	select {
	case <-p.ready:
		return p.editorConn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// agentCloses reports whether the agent said it supports session/close.
func (p *proxy) agentCloses() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeCap
}

// adoptCaps records what the agent told the editor it can do. The proxy needs
// only one of them: whether the daemon's close message can reach the agent.
func (p *proxy) adoptCaps(caps acp.AgentCapabilities) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCap = caps.SessionCapabilities.Close != nil
}

// session returns the mirrored session for an agent session ID, or nil when
// that session is not mirrored — either because no daemon answered, or because
// the editor is talking about a session the proxy never saw created.
func (p *proxy) session(id acp.SessionId) *psession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}
