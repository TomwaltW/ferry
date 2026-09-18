package v2

import "encoding/json"

// The wire shapes of ACP v2, taken from the protocol pages on
// agentclientprotocol.com (initialization, authentication, session-setup,
// prompt-lifecycle, session-list, tool-calls, session-config-options, read
// 2026-09-18). Object keys are camelCase and discriminator values snake_case,
// so every field spells its tag out rather than leaning on the Go name.

// Version is the protocol version number of v2. An agent that answers
// initialize with anything lower does not speak v2.
const Version = 2

// Implementation is the name and version either side reports in initialize.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeParams struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	Info            *Implementation    `json:"info,omitempty"`
}

// ClientCapabilities stays empty on purpose: Ferry proxies neither the file
// system nor a terminal on the agent's behalf, exactly as in v1.
type ClientCapabilities struct{}

type InitializeResult struct {
	ProtocolVersion int               `json:"protocolVersion"`
	Capabilities    AgentCapabilities `json:"capabilities"`
	Info            *Implementation   `json:"info,omitempty"`
	AuthMethods     []AuthMethod      `json:"authMethods,omitempty"`
}

// AgentCapabilities is where v2 differs most from v1: session/list,
// session/resume and session/close have no markers of their own, so an agent
// that offers session at all commits to all three.
type AgentCapabilities struct {
	Session *SessionCapabilities `json:"session,omitempty"`
}

type SessionCapabilities struct {
	Prompt map[string]json.RawMessage `json:"prompt,omitempty"`
	MCP    map[string]json.RawMessage `json:"mcp,omitempty"`
	Delete map[string]json.RawMessage `json:"delete,omitempty"`
}

// AuthMethod is one entry of the authMethods list. Type says who drives the
// login: the agent over the protocol, or the client by running something
// interactive.
type AuthMethod struct {
	MethodID    string `json:"methodId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// The auth method types v2 names. Only AuthAgent can be completed without a
// person at the keyboard, which is what decides whether Ferry logs in by itself.
const (
	AuthAgent    = "agent"
	AuthTerminal = "terminal"
)

type AuthLoginParams struct {
	MethodID string `json:"methodId"`
}

type NewSessionParams struct {
	Cwd string `json:"cwd"`
	// McpServers is always sent, and always empty: what the agent may reach is
	// its own configuration's business, not Ferry's.
	McpServers []json.RawMessage `json:"mcpServers"`
}

type NewSessionResult struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

type ResumeSessionParams struct {
	SessionID  string            `json:"sessionId"`
	Cwd        string            `json:"cwd,omitempty"`
	McpServers []json.RawMessage `json:"mcpServers"`
	// ReplayFrom asks for the history to be streamed again. Ferry leaves it
	// unset: the core already has the transcript it cares about.
	ReplayFrom *ReplayFrom `json:"replayFrom,omitempty"`
}

type ReplayFrom struct {
	Type string `json:"type"`
}

type ResumeSessionResult struct {
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

type SessionIDParams struct {
	SessionID string `json:"sessionId"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ContentBlock is the text-only slice of the content types: Ferry sends text
// and reads text, and says nothing about images or embedded context.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func textBlock(s string) ContentBlock { return ContentBlock{Type: "text", Text: s} }

type ListSessionsParams struct {
	Cwd    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type ListSessionsResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type SessionInfo struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type ConfigOption struct {
	ConfigID     string              `json:"configId"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type"`
	CurrentValue json.RawMessage     `json:"currentValue,omitempty"`
	Options      []ConfigOptionValue `json:"options,omitempty"`
}

type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// The types a config option takes, which is also the type a change names.
const (
	ConfigTypeSelect  = "select"
	ConfigTypeBoolean = "boolean"
	// ConfigValueID is the type of a change to a select option: the value is
	// the id of one of its choices.
	ConfigValueID = "id"
)

type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Type      string `json:"type"`
	Value     any    `json:"value"`
}

type SetConfigOptionResult struct {
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// SessionNotification is the envelope of every session/update.
type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// The sessionUpdate discriminators Ferry acts on. The variants it does not map
// — whole message upserts, tool call content chunks and the display-only
// terminal updates — are still parsed into this struct and then ignored.
const (
	UpdateStateUpdate       = "state_update"
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateToolCall          = "tool_call_update"
	UpdatePlan              = "plan_update"
	UpdateConfigOptions     = "config_option_update"
)

// The session states a state_update reports. Idle ends the turn in flight.
const (
	StateRunning        = "running"
	StateIdle           = "idle"
	StateRequiresAction = "requires_action"
)

// SessionUpdate is the union of every session/update variant. One struct with
// every field beats a discriminated union here: the variants overlap heavily,
// and an unknown one has to survive being decoded either way.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// state_update
	State      string `json:"state,omitempty"`
	StopReason string `json:"stopReason,omitempty"`

	// The message variants. Content is one block in the chunk variants and a
	// list of them in the whole message upserts, so it stays raw until read.
	MessageID string          `json:"messageId,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`

	// tool_call_update, which in v2 also creates the call it first names.
	ToolCallID string             `json:"toolCallId,omitempty"`
	Name       string             `json:"name,omitempty"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Status     string             `json:"status,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`

	// plan_update
	Type    string      `json:"type,omitempty"`
	PlanID  string      `json:"planId,omitempty"`
	Entries []PlanEntry `json:"entries,omitempty"`

	// config_option_update
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// RequestPermissionParams is what the agent asks Ferry, and the one request
// that travels from agent to client.
type RequestPermissionParams struct {
	SessionID   string             `json:"sessionId"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Subject     *PermissionSubject `json:"subject,omitempty"`
	Options     []PermissionOption `json:"options"`
}

type PermissionSubject struct {
	Type     string       `json:"type"`
	ToolCall *ToolCallRef `json:"toolCall,omitempty"`
}

type ToolCallRef struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome names itself twice — an "outcome" field inside the
// "outcome" object — which is what the specification's examples show.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// The outcomes a permission request has.
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

// The client method the agent calls, and the subject type it names.
const (
	MethodRequestPermission = "session/request_permission"
	SubjectToolCall         = "tool_call"
)
