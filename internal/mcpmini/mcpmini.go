package mcpmini

import (
	"context"
	"encoding/json"
	"slices"
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

// Capabilities is the capabilities object of the initialize exchange. Only the
// members Ferry needs are named; experimental carries the channel extensions.
type Capabilities struct {
	Experimental map[string]any `json:"experimental,omitempty"`
	Tools        *struct{}      `json:"tools,omitempty"`
}

// InitializeParams is what the client sends.
type InitializeParams struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ClientInfo      Info         `json:"clientInfo"`
}

// InitializeResult is what the server answers with.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      Info         `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// negotiate answers an initialize with the version both sides will speak.
//
// It follows what go-sdk v1.8.0 does in negotiatedVersion: the client's
// version wins when the server knows it, but the initialize handshake was
// superseded in 2026-07-28, so a version that new never comes out of it. A
// client asking for something unknown gets the newest version this handshake
// can settle on instead, which the lifecycle spec asks for.
func negotiate(clientVersion string) string {
	if slices.Contains(SupportedVersions, clientVersion) && clientVersion < ProtocolVersion20260728 {
		return clientVersion
	}
	for _, v := range SupportedVersions {
		if v < ProtocolVersion20260728 {
			return v
		}
	}
	return ProtocolVersion20251125
}

// LatestHandshakeVersion is the newest version an initialize can settle on,
// and what a client asks for unless it says otherwise.
const LatestHandshakeVersion = ProtocolVersion20251125
