# FSP — Ferry Session Protocol core types

**English** · [中文版](fsp.zh-CN.md)

FSP is the vocabulary every other package in Ferry shares: how a chat endpoint
is named, what an inbound and an outbound message carry, what a session and its
events look like, and how an approval is asked and answered. This document is
the prose form of the Go declarations in `internal/fsp`.

## Scope

These declarations are frozen for P0: their names, shapes and string values do
not change within the phase, and a task that needs something absent from them
stops and reports instead of adding it. The Go package is the single source of
truth; where this text and the package disagree, the package wins.

Every string constant here is also a wire value: it is written to the store and
read back by a later version, so renaming one is a breaking change.

## Platforms and endpoints

An `EndpointID` names one IM conversation: which platform, which bot account,
and which peer. For a direct message the peer is the other user; a peer
identifier may itself contain colons, so `ParseEndpoint` splits on the first two
only.

```go
type Platform string

const (
	PlatformWeChat Platform = "wechat"
	PlatformFeishu Platform = "feishu"
	PlatformFake   Platform = "fake"
)

type EndpointID struct {
	Platform Platform
	BotID    string
	PeerID   string
}

func (e EndpointID) String() string
func ParseEndpoint(s string) (EndpointID, error)
```

`String` renders the endpoint as `platform:bot:peer`, and `ParseEndpoint` is its
inverse.

## Languages and attachments

`Lang` is the language a reply is rendered in; the bot default is `zh-CN`. An
`Attachment` describes one piece of media already associated with a message.
`Path` is an absolute path to a local file that has been downloaded and
decrypted; it is empty while the file has not been fetched, which is the normal
state for an attachment nobody asked for yet.

```go
type Lang string

const (
	LangZH Lang = "zh-CN"
	LangEN Lang = "en"
)

type Attachment struct {
	Kind     string
	Name     string
	MIMEType string
	Path     string
	Size     int64
}
```

`Kind` is one of `image`, `file`, `voice` or `video`.

## Inbound messages

An `Inbound` is one message that arrived from a chat platform. `ReplyCtx` is the
platform's own reply context — on WeChat it is the `context_token` — and the
core never parses it: it is carried along and handed back when replying, and its
meaning belongs entirely to the entry package.

```go
type Inbound struct {
	Endpoint    EndpointID
	SenderID    string
	MessageID   string
	Text        string
	Attachments []Attachment
	ReplyCtx    string
	ReceivedAt  time.Time
}
```

## Outbound messages

An `Outbound` is one message Ferry wants delivered. `Session` may be empty for a
system message that belongs to no session. `DedupeKey` stays identical across
every retry of the same message, which is what lets a gateway recognise a
duplicate; when it is empty the outbox generates one. `Urgent` marks the two
kinds of message that must not wait behind a batch: an approval request, and the
final result of a turn.

```go
type OutKind string

const (
	OutText     OutKind = "text"
	OutFile     OutKind = "file"
	OutApproval OutKind = "approval"
)

type Outbound struct {
	Endpoint  EndpointID
	Session   SessionID
	Kind      OutKind
	Text      string
	FilePath  string
	DedupeKey string
	Urgent    bool
}
```

## Delivery states

A delivery is never simply sent or failed. `accepted` means the platform took
it; `unknown` means the attempt neither clearly succeeded nor clearly failed and
must not be retried blindly; `held` means the reply window is shut and the
message is waiting for the next one.

```go
type DeliveryState string

const (
	DeliveryQueued   DeliveryState = "queued"
	DeliverySending  DeliveryState = "sending"
	DeliveryAccepted DeliveryState = "accepted"
	DeliveryUnknown  DeliveryState = "unknown"
	DeliveryRejected DeliveryState = "rejected"
	DeliveryHeld     DeliveryState = "held"
)
```

## Sessions

A `Session` is one conversation with one agent. `Origin` records who created it:
`managed` for a session Ferry started itself, `terminal` for one it took over.
`AgentSessionID` is the identifier the agent itself uses, kept so that a session
can be resumed after a restart. `Cwd` is an absolute path.

```go
type (
	SessionID     string
	SessionOrigin string
	SessionState  string
	DriverKind    string
)

const (
	OriginManaged  SessionOrigin = "managed"
	OriginTerminal SessionOrigin = "terminal"

	StateStarting SessionState = "starting"
	StateIdle     SessionState = "idle"
	StateRunning  SessionState = "running"
	StateAwaiting SessionState = "awaiting"
	StateParked   SessionState = "parked"
	StateClosed   SessionState = "closed"

	DriverACP           DriverKind = "acp"
	DriverServer        DriverKind = "server"
	DriverProxy         DriverKind = "proxy"
	DriverClaudeChannel DriverKind = "claude-channel"
	DriverPTY           DriverKind = "pty"
)

type Session struct {
	ID             SessionID
	Driver         DriverKind
	Agent          string
	Cwd            string
	AgentSessionID string
	Origin         SessionOrigin
	State          SessionState
	Owner          EndpointID
	CreatedAt      time.Time
	LastActive     time.Time
}
```

`Agent` is the preset name from the configuration, not a command line.

## Events

An `Event` is one thing an agent did during a turn. Only the fields relevant to
`Kind` are populated: `Tool` for `agent.tool`, `Plan` for `agent.plan`,
`Approval` for `agent.approval`, `StopReason` for `agent.turn_end`, and `Err`
for `agent.error`.

```go
type EventKind string

const (
	EvChunk    EventKind = "agent.chunk"
	EvThought  EventKind = "agent.thought"
	EvTool     EventKind = "agent.tool"
	EvPlan     EventKind = "agent.plan"
	EvApproval EventKind = "agent.approval"
	EvTurnEnd  EventKind = "agent.turn_end"
	EvError    EventKind = "agent.error"
)

type ToolInfo struct{ CallID, Kind, Title, Status string }

type PlanStep struct{ Title, Status string }

type Event struct {
	Session    SessionID
	Kind       EventKind
	Text       string
	Tool       *ToolInfo
	Plan       []PlanStep
	Approval   *ApprovalRequest
	StopReason string
	Err        string
	At         time.Time
}
```

`StopReason` is one of `end_turn`, `cancelled`, `max_tokens`, `refusal` or
`error`.

## Approvals

An `ApprovalRequest` is a tool call waiting on a human. Its `ID` is five
characters from the alphabet `abcdefghijkmnopqrstuvwxyz`, which drops `l` so
that a code stays unambiguous when typed on a phone; the approval module
allocates it. `InputPreview` is truncated to 2000 characters, because a chat
message is not a place to paste an entire tool payload.

```go
type ApprovalOptionKind string

const (
	OptAllowOnce    ApprovalOptionKind = "allow_once"
	OptAllowAlways  ApprovalOptionKind = "allow_always"
	OptRejectOnce   ApprovalOptionKind = "reject_once"
	OptRejectAlways ApprovalOptionKind = "reject_always"
)

type ApprovalOption struct {
	ID    string
	Kind  ApprovalOptionKind
	Label string
}

type ApprovalRequest struct {
	ID           string
	Session      SessionID
	ToolName     string
	ToolKind     string
	Description  string
	InputPreview string
	Paths        []string
	Options      []ApprovalOption
	CreatedAt    time.Time
}

type Decision string

const (
	DecisionAllowOnce   Decision = "allow_once"
	DecisionAllowAlways Decision = "allow_always"
	DecisionReject      Decision = "reject"
)

type Verdict struct {
	ApprovalID string
	Decision   Decision
	OptionID   string
	By         string
}
```

`ToolKind` is one of `read`, `edit`, `delete`, `move`, `search`, `execute`,
`think`, `fetch`, `switch_mode` or `other`. In a `Verdict`, `OptionID` names the
agent's own option and is empty when the request was cancelled, and `By` is
either the sender ID of the human who decided or one of `policy`, `timeout` and
`cancel`.

## Sentinel errors

Errors are compared with `errors.Is` against these sentinels and wrapped with
`fmt.Errorf("...: %w", err)` on the way out.

```go
var (
	ErrUnsupported     = errors.New("ferry: unsupported")
	ErrBusy            = errors.New("ferry: session busy")
	ErrNotFound        = errors.New("ferry: not found")
	ErrSessionExpired  = errors.New("ferry: platform session expired")
	ErrNoWindow        = errors.New("ferry: no reply window")
	ErrQuotaExceeded   = errors.New("ferry: quota exceeded")
	ErrWorkspaceDenied = errors.New("ferry: path outside workspace roots")
	ErrLocked          = errors.New("ferry: session held by another process")
)
```

`ErrNoWindow` and `ErrQuotaExceeded` are expected conditions that the outbox
handles, not failures to report to the user. `ErrLocked` is what a handoff
attempt meets when another process is already driving the session.
