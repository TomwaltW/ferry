package mcpmini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// JSONRPCVersion is the only version this package speaks; frames are one JSON
// object per line over stdio.
const JSONRPCVersion = "2.0"

// The protocol versions go-sdk v1.8.0 lists in mcp/shared.go, newest first.
// Version negotiation picks the newest both sides know.
const (
	ProtocolVersion20260728 = "2026-07-28"
	ProtocolVersion20251125 = "2025-11-25"
	ProtocolVersion20250618 = "2025-06-18"
	ProtocolVersion20250326 = "2025-03-26"
	ProtocolVersion20241105 = "2024-11-05"
)

// SupportedVersions is what an initialize exchange may settle on, newest
// first.
var SupportedVersions = []string{
	ProtocolVersion20260728,
	ProtocolVersion20251125,
	ProtocolVersion20250618,
	ProtocolVersion20250326,
	ProtocolVersion20241105,
}

// The methods and notifications both roles handle.
const (
	MethodInitialize  = "initialize"
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
	MethodPing        = "ping"
	NotifyInitialized = "notifications/initialized"
	// NotifyCancelled is accepted and ignored.
	NotifyCancelled = "notifications/cancelled"
)

// Info names one side of the connection in the initialize exchange.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type Content struct {
	Type string `json:"type"` // text
	Text string `json:"text,omitempty"`
}

type ToolResult struct {
	Content []Content `json:"content,omitempty"`
	IsError bool      `json:"isError,omitempty"`
}

// NotifyFunc receives a notification the peer sent, custom ones included.
type NotifyFunc func(ctx context.Context, method string, params json.RawMessage)

// CallFunc runs one tool call. A tool error belongs in ToolResult.IsError; the
// error return is for a call that could not be made at all.
type CallFunc func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)

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

// Server is the stdio server role. Its behaviour belongs to the P3-T3 task.
type Server struct {
	opts ServerOptions
}

func NewServer(opts ServerOptions) *Server { return &Server{opts: opts} }

// Run serves until In reaches its end or ctx is done.
func (s *Server) Run(ctx context.Context) error {
	return fmt.Errorf("mcpmini: Server.Run: %w", fsp.ErrUnsupported)
}

// Notify sends a notification to the client.
func (s *Server) Notify(ctx context.Context, method string, params any) error {
	return fmt.Errorf("mcpmini: Server.Notify: %w", fsp.ErrUnsupported)
}

// ProtocolVersion is what initialize settled on, empty before the handshake.
func (s *Server) ProtocolVersion() string { return "" }

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
}

// Client is the stdio client role. Its behaviour belongs to the P3-T3 task.
type Client struct {
	opts ClientOptions
}

func NewClient(opts ClientOptions) *Client { return &Client{opts: opts} }

// Start launches the peer and runs initialize followed by
// notifications/initialized.
func (c *Client) Start(ctx context.Context) error {
	return fmt.Errorf("mcpmini: Client.Start: %w", fsp.ErrUnsupported)
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	return nil, fmt.Errorf("mcpmini: Client.ListTools: %w", fsp.ErrUnsupported)
}

func (c *Client) CallTool(ctx context.Context, name string, args any) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("mcpmini: Client.CallTool: %w", fsp.ErrUnsupported)
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return fmt.Errorf("mcpmini: Client.Notify: %w", fsp.ErrUnsupported)
}

func (c *Client) Ping(ctx context.Context) error {
	return fmt.Errorf("mcpmini: Client.Ping: %w", fsp.ErrUnsupported)
}

// ProtocolVersion is what initialize settled on, empty before Start.
func (c *Client) ProtocolVersion() string { return "" }

func (c *Client) Close() error { return nil }
