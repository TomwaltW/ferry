# FSP —— Ferry 会话协议核心类型

[English](fsp.md) · **中文（仅供参考）**

本文是 [fsp.md](fsp.md) 的中文译本，**仅供参考**。两者不一致时，以英文版和
`internal/fsp` 的 Go 声明为准。

FSP 是 Ferry 其余各包共用的词汇表：聊天入口怎么命名、入站和出站消息各带什么、
会话和它的事件长什么样、审批怎么发起又怎么裁决。本文把 `internal/fsp` 里的 Go
声明写成文字。

## 适用范围

这些声明在 P0 内是冻结的：名字、结构和字符串取值都不变；任务需要的东西如果不在
里面，停下来报告，不要自己加。Go 包是唯一权威，本文与包不一致时以包为准。

这里每个字符串常量同时也是线上取值：它会被写进存储、被后续版本读回，
所以改名属于破坏性变更。

## 平台与入口

`EndpointID` 标识一个 IM 会话：哪个平台、哪个 bot 账号、对端是谁。私聊时对端就是
对方用户；对端标识本身可能含冒号，所以 `ParseEndpoint` 只按前两个冒号切分。

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

`String` 把入口渲染成 `platform:bot:peer`，`ParseEndpoint` 是它的逆操作。

## 语言与附件

`Lang` 是回复渲染用的语言，机器人默认 `zh-CN`。`Attachment` 描述随消息而来的
一份媒体。`Path` 是已下载并解密到本地的绝对路径；文件还没拉下来时它为空 ——
对一份还没人索取的附件来说，这是正常状态。

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

`Kind` 取 `image`、`file`、`voice`、`video` 之一。

## 入站消息

`Inbound` 是从聊天平台收到的一条消息。`ReplyCtx` 是平台自己的回复上下文 ——
微信上就是 `context_token` —— 核心不解析它：只是原样带着，回复时再交回去，
它的含义完全归入口包所有。

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

## 出站消息

`Outbound` 是 Ferry 想投递出去的一条消息。系统消息不属于任何会话，`Session`
可以为空。`DedupeKey` 在同一条消息的每次重试中保持不变，网关正是靠它认出重复；
为空时由发件箱生成。`Urgent` 标记两类不能排在合批后面等的消息：审批请求，
以及本轮的最终结果。

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

## 投递状态

投递从来不是「成功」或「失败」两种。`accepted` 表示平台收下了；`unknown` 表示这次
尝试既没明确成功也没明确失败，不能盲目重试；`held` 表示回复窗口关着，
消息在等下一个窗口。

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

## 会话

`Session` 是与某个 agent 的一次对话。`Origin` 记录它由谁创建：`managed` 是 Ferry
自己起的，`terminal` 是它接管过来的。`AgentSessionID` 是 agent 自己用的标识，
留着是为了重启后还能恢复会话。`Cwd` 是绝对路径。

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

`Agent` 是配置里的预设名，不是命令行。

## 事件

`Event` 是 agent 在一轮里做的一件事。只有与 `Kind` 相关的字段会被填上：
`agent.tool` 填 `Tool`，`agent.plan` 填 `Plan`，`agent.approval` 填 `Approval`，
`agent.turn_end` 填 `StopReason`，`agent.error` 填 `Err`。

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

`StopReason` 取 `end_turn`、`cancelled`、`max_tokens`、`refusal`、`error` 之一。

## 审批

`ApprovalRequest` 是一次等人裁决的工具调用。它的 `ID` 是 5 个字符，字母表为
`abcdefghijkmnopqrstuvwxyz` —— 去掉了 `l`，好让审批码在手机上打出来时不会认错；
由审批模块分配。`InputPreview` 截断到 2000 字符，因为聊天窗口不是贴整份工具
载荷的地方。

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

`ToolKind` 取 `read`、`edit`、`delete`、`move`、`search`、`execute`、`think`、
`fetch`、`switch_mode`、`other` 之一。在 `Verdict` 里，`OptionID` 指向 agent 自己
的选项，取消时为空；`By` 要么是做裁决那个人的 sender ID，要么是 `policy`、
`timeout`、`cancel` 之一。

## 哨兵错误

错误用 `errors.Is` 与下列哨兵比较，往外传时用 `fmt.Errorf("...: %w", err)` 包装。

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

`ErrNoWindow` 和 `ErrQuotaExceeded` 是发件箱负责处理的预期情况，不是要报给用户
的失败。`ErrLocked` 是接力时撞上另一个进程正在驱动该会话的情形。
