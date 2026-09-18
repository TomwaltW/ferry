package mcpmini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
)

type ServerOptions struct {
	Info         Info
	Instructions string
	// Experimental goes into capabilities.experimental verbatim.
	Experimental map[string]any
	Tools        []Tool
	Call         CallFunc
	OnNotify     NotifyFunc
	In           io.Reader
	Out          io.Writer
	Log          *slog.Logger
}

// Server is the stdio server role: the peer starts it, initializes it, lists
// and calls its tools, and receives whatever notifications it pushes.
type Server struct {
	opts ServerOptions
	c    *conn

	version     atomic.Pointer[string]
	initialized atomic.Bool
}

func NewServer(opts ServerOptions) *Server {
	return &Server{opts: opts, c: newConn(opts.In, opts.Out, opts.Log)}
}

// Run serves until In reaches its end or ctx is done.
func (s *Server) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.c.read(ctx, s.handle, s.notified)
	}()

	select {
	case <-done:
		return s.c.cause()
	case <-ctx.Done():
		s.c.close(ctx.Err())
		return ctx.Err()
	}
}

// Notify sends a notification to the client.
func (s *Server) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.c.notify(method, params)
}

// ProtocolVersion is what initialize settled on, empty before the handshake.
func (s *Server) ProtocolVersion() string {
	if v := s.version.Load(); v != nil {
		return *v
	}
	return ""
}

// Initialized reports whether the client has sent notifications/initialized.
func (s *Server) Initialized() bool { return s.initialized.Load() }

// Close ends the connection; a Run in flight returns.
func (s *Server) Close() error {
	s.c.close(nil)
	return nil
}

// capabilities is what the server declares. tools is present exactly when the
// server offers one, which is what makes a channel two-way.
func (s *Server) capabilities() Capabilities {
	caps := Capabilities{Experimental: s.opts.Experimental}
	if len(s.opts.Tools) > 0 {
		caps.Tools = &struct{}{}
	}
	return caps
}

func (s *Server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodInitialize:
		return s.initialize(params)
	case MethodPing:
		return struct{}{}, nil
	case MethodToolsList:
		tools := s.opts.Tools
		if tools == nil {
			tools = []Tool{}
		}
		return listToolsResult{Tools: tools}, nil
	case MethodToolsCall:
		return s.callTool(ctx, params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
}

func (s *Server) initialize(params json.RawMessage) (any, error) {
	var p InitializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "initialize: " + err.Error()}
		}
	}
	version := negotiate(p.ProtocolVersion)
	s.version.Store(&version)
	return InitializeResult{
		ProtocolVersion: version,
		Capabilities:    s.capabilities(),
		ServerInfo:      s.opts.Info,
		Instructions:    s.opts.Instructions,
	}, nil
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, error) {
	var p callToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call: " + err.Error()}
	}
	if s.opts.Call == nil {
		return nil, &rpcError{Code: codeMethodNotFound, Message: "no tool named " + p.Name}
	}
	res, err := s.opts.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		return nil, fmt.Errorf("mcpmini: tool %s: %w", p.Name, err)
	}
	return res, nil
}

// notified records the end of the handshake and passes every notification on,
// custom ones included.
func (s *Server) notified(ctx context.Context, method string, params json.RawMessage) {
	if method == NotifyInitialized {
		s.initialized.Store(true)
	}
	if s.opts.OnNotify != nil {
		s.opts.OnNotify(ctx, method, params)
	}
}
