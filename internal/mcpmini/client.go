package mcpmini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
)

type ClientOptions struct {
	Info Info
	// Command and Env start the peer; an empty Command means the caller wires
	// In and Out itself.
	Command  []string
	Env      map[string]string
	In       io.Reader
	Out      io.Writer
	OnNotify NotifyFunc
	Log      *slog.Logger
	// ProtocolVersion is what initialize asks for; empty means
	// LatestHandshakeVersion.
	ProtocolVersion string
	// Capabilities the client declares; the zero value declares none.
	Capabilities Capabilities
}

// Client is the stdio client role: it starts the peer, runs the handshake, and
// then lists and calls tools while notifications flow both ways.
type Client struct {
	opts ClientOptions
	log  *slog.Logger

	mu   sync.Mutex
	c    *conn
	cmd  *exec.Cmd
	stop func()

	version atomic.Pointer[string]
	server  atomic.Pointer[InitializeResult]

	exited    chan struct{}
	exitOnce  sync.Once
	exitErr   atomic.Pointer[error]
	closeOnce sync.Once
}

func NewClient(opts ClientOptions) *Client {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{opts: opts, log: log, exited: make(chan struct{})}
}

// Start launches the peer and runs initialize followed by
// notifications/initialized.
func (c *Client) Start(ctx context.Context) error {
	in, out, err := c.streams()
	if err != nil {
		return err
	}

	conn := newConn(in, out, c.log)
	c.mu.Lock()
	c.c = conn
	c.mu.Unlock()

	// The read loop outlives Start: it serves the peer's pings and delivers
	// its notifications for as long as the session lasts.
	readCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.mu.Lock()
	c.stop = cancel
	c.mu.Unlock()
	go func() {
		conn.read(readCtx, c.handle, c.opts.OnNotify)
		c.markExited(conn.cause())
	}()

	want := c.opts.ProtocolVersion
	if want == "" {
		want = LatestHandshakeVersion
	}
	raw, err := conn.call(ctx, MethodInitialize, InitializeParams{
		ProtocolVersion: want,
		Capabilities:    c.opts.Capabilities,
		ClientInfo:      c.opts.Info,
	})
	if err != nil {
		_ = c.Close()
		return err
	}
	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		_ = c.Close()
		return fmt.Errorf("mcpmini: parse the initialize result: %w", err)
	}
	if !slices.Contains(SupportedVersions, res.ProtocolVersion) {
		_ = c.Close()
		return fmt.Errorf("mcpmini: the peer speaks %q, which this build does not: %w", res.ProtocolVersion, errUnsupportedVersion)
	}
	c.version.Store(&res.ProtocolVersion)
	c.server.Store(&res)

	if err := conn.notify(NotifyInitialized, struct{}{}); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

var errUnsupportedVersion = errors.New("mcpmini: unsupported protocol version")

// streams returns the pair the connection runs on, starting the peer process
// when the options name one.
func (c *Client) streams() (io.Reader, io.Writer, error) {
	if len(c.opts.Command) == 0 {
		if c.opts.In == nil || c.opts.Out == nil {
			return nil, nil, errors.New("mcpmini: a client needs either a command or both streams")
		}
		return c.opts.In, c.opts.Out, nil
	}

	cmd := exec.Command(c.opts.Command[0], c.opts.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range c.opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcpmini: pipe the peer's stdout: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcpmini: pipe the peer's stdin: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcpmini: pipe the peer's stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("mcpmini: start %s: %w", c.opts.Command[0], err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.mu.Unlock()

	go drainStderr(stderr, c.log, c.opts.Command[0])
	go func() {
		err := cmd.Wait()
		c.markExited(err)
	}()
	return stdout, stdin, nil
}

// drainStderr keeps the peer from blocking on a full pipe and puts whatever it
// says into the log, which is where a server that fails to start explains
// itself.
func drainStderr(r io.Reader, log *slog.Logger, name string) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			log.Info("mcpmini: peer stderr", "command", name, "text", string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	conn, err := c.conn()
	if err != nil {
		return nil, err
	}
	raw, err := conn.call(ctx, MethodToolsList, struct{}{})
	if err != nil {
		return nil, err
	}
	var res listToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcpmini: parse the tools/list result: %w", err)
	}
	return res.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args any) (ToolResult, error) {
	conn, err := c.conn()
	if err != nil {
		return ToolResult{}, err
	}
	raw, err := marshalParams(args)
	if err != nil {
		return ToolResult{}, err
	}
	out, err := conn.call(ctx, MethodToolsCall, callToolParams{Name: name, Arguments: raw})
	if err != nil {
		return ToolResult{}, err
	}
	var res ToolResult
	if err := json.Unmarshal(out, &res); err != nil {
		return ToolResult{}, fmt.Errorf("mcpmini: parse the tools/call result: %w", err)
	}
	return res, nil
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	conn, err := c.conn()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return conn.notify(method, params)
}

func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.conn()
	if err != nil {
		return err
	}
	_, err = conn.call(ctx, MethodPing, struct{}{})
	return err
}

// ProtocolVersion is what initialize settled on, empty before Start.
func (c *Client) ProtocolVersion() string {
	if v := c.version.Load(); v != nil {
		return *v
	}
	return ""
}

// Server is the peer's half of the handshake: its info, its capabilities and
// its instructions. It is nil before Start.
func (c *Client) Server() *InitializeResult { return c.server.Load() }

// Done closes once the peer is gone, which is how the caller notices a server
// that exited on its own.
func (c *Client) Done() <-chan struct{} { return c.exited }

// Err is why the peer went away, nil for a clean exit.
func (c *Client) Err() error {
	if p := c.exitErr.Load(); p != nil {
		return *p
	}
	return nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		conn, cmd, stop := c.c, c.cmd, c.stop
		c.mu.Unlock()
		if conn != nil {
			conn.close(nil)
		}
		if stop != nil {
			stop()
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		c.markExited(nil)
	})
	return nil
}

func (c *Client) conn() (*conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == nil {
		return nil, errors.New("mcpmini: the client has not started")
	}
	return c.c, nil
}

func (c *Client) markExited(err error) {
	c.exitOnce.Do(func() {
		if err != nil {
			c.exitErr.Store(&err)
		}
		close(c.exited)
	})
}

// handle answers the requests a server may send its client. Ferry's client
// role offers no tools, so ping is the whole surface.
func (c *Client) handle(_ context.Context, method string, _ json.RawMessage) (any, error) {
	switch method {
	case MethodPing:
		return struct{}{}, nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
}
