# 开发派工 Spec — Ferry P0 骨架与测试替身

> 生成于 2026-09-17。本轮唯一权威文档，另须遵守 `docs/dev/00-overview.md` 第 3 节。
> 冻结后不再修改；需要变更请停下来找总管。

## 1. 问题定义

**要做什么**：建好仓库骨架，冻结协议层类型，做出两个测试替身——假 iLink 服务和假 ACP agent，并配齐双语文档框架与 CI。

**为什么**：P1–P3 的全部自动化验收都依赖这两个替身。它们必须先于业务代码存在，而且行为要贴近真实协议，否则后面的测试测的是假象。

**明确不做**：

- 任何业务逻辑：会话、路由、审批、发件箱一律不写
- 真实微信登录、真实 agent 调用
- 发布正式版本

## 2. 验收标准（DoD）

以下全部通过 = 本轮完成。

### 2.1 能跑

- **A1** `go build ./...` → 退出码 0
- **A2** `go run ./cmd/ferry version` → 输出以 `ferry ` 开头并含版本号，退出码 0
- **A3** `go test ./...` → 全部 `ok`，没有 `FAIL`

### 2.2 行为正确

- **B1** `go test ./internal/testkit/fakeilink/ -run TestLoginFlow -v` → PASS
- **B2** `go test ./internal/testkit/fakeilink/ -run TestLongPoll -v` → PASS
- **B3** `go test ./internal/testkit/fakeilink/ -run 'TestSend|TestReplyLimit|TestExpire|TestWindowTTL|TestAuth' -v` → 5 个测试全部 PASS
- **B4** `go test ./internal/testkit/fakeagent/ -v` → PASS，且至少包含 TestEcho、TestPermissionAllow、TestPermissionReject、TestCancel、TestListAndLoad、TestBurst
- **B5** `go run ./scripts/check-i18n` → 输出 `i18n ok`，退出码 0
- **B6** `go test ./scripts/check-i18n/ -v` → PASS，覆盖三种违规：缺少成对文件、标题数量不一致、locale 键不一致
- **B7** `make ci-local` → 退出码 0

### 2.3 没弄坏

- **C1** 00 总览 3.5 的 G1–G3 → 全部通过

基线说明：起点提交只有 `LICENSE` 和 `docs/dev/`，此时 `go build ./...` 会报没有 Go 文件，这是预期的起点。

## 3. 冻结契约

以下定义在本轮内不可更改。标注「签名」的函数由 T0 写出签名；纯函数（如 `EndpointID.String`）T0 可以直接实现。

### 3.1 协议层类型 `internal/fsp`

```go
package fsp

import (
	"errors"
	"time"
)

type Platform string

const (
	PlatformWeChat Platform = "wechat"
	PlatformFeishu Platform = "feishu"
	PlatformFake   Platform = "fake"
)

// EndpointID 唯一标识一个 IM 会话：平台 + bot 账号 + 对端（私聊时为对方用户 ID）。
type EndpointID struct {
	Platform Platform
	BotID    string
	PeerID   string
}

// 签名：String 返回 "<platform>:<bot>:<peer>"；ParseEndpoint 是逆操作，
// 按前两个冒号切分，PeerID 允许包含冒号。
func (e EndpointID) String() string
func ParseEndpoint(s string) (EndpointID, error)

type Lang string

const (
	LangZH Lang = "zh-CN"
	LangEN Lang = "en"
)

type Attachment struct {
	Kind     string // image | file | voice | video
	Name     string
	MIMEType string
	Path     string // 已下载并解密到本地的绝对路径；未下载时为空
	Size     int64
}

type Inbound struct {
	Endpoint    EndpointID
	SenderID    string
	MessageID   string
	Text        string
	Attachments []Attachment
	ReplyCtx    string // 平台回复上下文（微信为 context_token），核心不解析
	ReceivedAt  time.Time
}

type OutKind string

const (
	OutText     OutKind = "text"
	OutFile     OutKind = "file"
	OutApproval OutKind = "approval"
)

type Outbound struct {
	Endpoint  EndpointID
	Session   SessionID // 系统消息可为空
	Kind      OutKind
	Text      string
	FilePath  string
	DedupeKey string // 重试时保持不变；微信里映射为 client_id；为空时由发件箱生成
	Urgent    bool   // 审批请求与本轮最终结果
}

type DeliveryState string

const (
	DeliveryQueued   DeliveryState = "queued"
	DeliverySending  DeliveryState = "sending"
	DeliveryAccepted DeliveryState = "accepted"
	DeliveryUnknown  DeliveryState = "unknown"
	DeliveryRejected DeliveryState = "rejected"
	DeliveryHeld     DeliveryState = "held"
)

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
	Agent          string // 配置里的 agent 名
	Cwd            string // 绝对路径
	AgentSessionID string
	Origin         SessionOrigin
	State          SessionState
	Owner          EndpointID
	CreatedAt      time.Time
	LastActive     time.Time
}

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
	StopReason string // EvTurnEnd：end_turn | cancelled | max_tokens | refusal | error
	Err        string
	At         time.Time
}

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
	ID           string // 5 位，字母表 abcdefghijkmnopqrstuvwxyz；由审批模块分配
	Session      SessionID
	ToolName     string
	ToolKind     string // read edit delete move search execute think fetch switch_mode other
	Description  string
	InputPreview string // 不超过 2000 字符
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
	OptionID   string // 对应 agent 的选项 ID；为空表示取消
	By         string // 裁决人 sender ID，或 policy / timeout / cancel
}

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

### 3.2 iLink 线上格式 `internal/ilinkwire`

字段名与取值依据 openilink-sdk-go v0.6.0 的 `types.go`、`client.go`、`errors.go`（2026-09-17 核对）。

```go
package ilinkwire

const (
	DefaultBaseURL        = "https://ilinkai.weixin.qq.com"
	DefaultChannelVersion = "2.1.6" // 写进每个请求的 base_info.channel_version，可由配置覆盖

	PathGetBotQRCode    = "ilink/bot/get_bot_qrcode"    // GET，查询参数 bot_type=3
	PathGetQRCodeStatus = "ilink/bot/get_qrcode_status" // GET，查询参数 qrcode=<key>
	PathGetUpdates      = "ilink/bot/getupdates"        // POST，长轮询，服务端约 35 秒
	PathSendMessage     = "ilink/bot/sendmessage"       // POST，成功时响应体为 {}
	PathGetConfig       = "ilink/bot/getconfig"         // POST
	PathSendTyping      = "ilink/bot/sendtyping"        // POST
	PathGetUploadURL    = "ilink/bot/getuploadurl"      // POST，P2 才使用

	HeaderAuthType     = "AuthorizationType" // 固定值 AuthTypeValue
	AuthTypeValue      = "ilink_bot_token"
	HeaderUIN          = "X-WECHAT-UIN" // 每个请求随机生成，算法照 openilink-sdk-go 的 randomWechatUIN
	HeaderAuthorization = "Authorization" // "Bearer <bot_token>"

	ErrCodeSessionExpired = -14 // ret 或 errcode 任一等于它即会话过期
)

type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type APIError struct {
	Ret     int    `json:"ret,omitempty"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

// 签名：Failed 在 ret 或 errcode 非 0 时为 true；SessionExpired 判断 -14。
func (e APIError) Failed() bool
func (e APIError) SessionExpired() bool

type QRCodeResp struct {
	QRCode           string `json:"qrcode"`             // 轮询用的 key
	QRCodeImgContent string `json:"qrcode_img_content"` // 用于生成二维码的内容
}

type QRStatusResp struct {
	Status       string `json:"status"` // wait | scaned | confirmed | expired | scaned_but_redirect
	BotToken     string `json:"bot_token,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"` // 扫码人，即 owner
	RedirectHost string `json:"redirect_host,omitempty"`
}

type (
	MessageType  int
	MessageState int
	ItemType     int
)

const (
	MsgTypeUser MessageType = 1
	MsgTypeBot  MessageType = 2

	StateNew        MessageState = 0
	StateGenerating MessageState = 1
	StateFinish     MessageState = 2

	ItemText  ItemType = 1
	ItemImage ItemType = 2
	ItemVoice ItemType = 3
	ItemFile  ItemType = 4
	ItemVideo ItemType = 5

	EncryptAES128ECB = 1
)

type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type ImageItem struct {
	Media *CDNMedia `json:"media,omitempty"`
}

type VoiceItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	PlayTime int       `json:"playtime,omitempty"`
	Text     string    `json:"text,omitempty"` // 微信自带的语音转写
}

type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type MessageItem struct {
	Type      ItemType   `json:"type,omitempty"`
	MsgID     string     `json:"msg_id,omitempty"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
}

type Message struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"` // 非空即群消息
	MessageType  MessageType   `json:"message_type,omitempty"`
	MessageState MessageState  `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

type GetUpdatesReq struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

type GetUpdatesResp struct {
	APIError
	Msgs                 []Message `json:"msgs,omitempty"`
	GetUpdatesBuf        string    `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMs int64     `json:"longpolling_timeout_ms,omitempty"`
}

type SendMessageReq struct {
	Msg      *Message  `json:"msg,omitempty"` // 发送时 message_type=2、message_state=2，必须带 context_token 与 client_id
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type GetConfigReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	ContextToken string    `json:"context_token,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

type GetConfigResp struct {
	APIError
	TypingTicket string `json:"typing_ticket,omitempty"`
}

type SendTypingReq struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"` // 1 正在输入，2 取消
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}
```

媒体上传相关类型（`getuploadurl`）由 P2-T0 追加，本轮不定义。

### 3.3 时钟与 ID

```go
package clock

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) Ticker
}

// 签名
func Real() Clock

// Fake：时间只在 Advance/Set 时前进；前进时触发所有到期的 After 与 Ticker。
type Fake struct{ /* 由 T0 实现 */ }

func NewFake(start time.Time) *Fake
func (f *Fake) Advance(d time.Duration)
func (f *Fake) Set(t time.Time)
func (f *Fake) BlockUntil(n int) // 阻塞到至少 n 个定时器在等待，用于测试同步
```

```go
package ids

// New 返回 "<prefix>_" 加 26 位小写 base32 随机串（crypto/rand）。
func New(prefix string) string
```

### 3.4 假 iLink 服务 `internal/testkit/fakeilink`（T1 实现）

```go
package fakeilink

type ExpiredMode string

const (
	ExpiredDrop  ExpiredMode = "drop"  // 默认：静默接受但不投递
	ExpiredError ExpiredMode = "error" // 返回 ret=-2
)

type FailMode int

const (
	FailTimeout FailMode = iota + 1 // 挂起 60 秒或直到服务关闭
	FailHTTP500
)

type Options struct {
	LongPoll    time.Duration // 默认 2s（真实服务约 35s）
	ReplyLimit  int           // 每个 context_token 能成功投递的回复数，默认 10
	WindowTTL   time.Duration // context_token 有效期，默认 24h
	ExpiredMode ExpiredMode   // 默认 ExpiredDrop
	Clock       clock.Clock   // 默认 clock.Real()；窗口过期按它计算
}

type SentRecord struct {
	ClientID, ToUserID, ContextToken, Text string
	Items     []ilinkwire.MessageItem
	Duplicate bool // 同一 client_id 第二次及以后到达
	Dropped   bool // 超出上限或窗口过期被静默丢弃
	At        time.Time
}

type TypingRecord struct {
	UserID string
	Status int
	At     time.Time
}

type Server struct {
	URL string
	// 其余字段由 T1 定义
}

func New(opts Options) *Server
func (s *Server) Close()

// 登录
func (s *Server) ConfirmScan(ownerUserID string) // 当前二维码进入 confirmed，签发新 bot_token
func (s *Server) BotToken() string
func (s *Server) BotID() string

// 消息
func (s *Server) PushText(fromUserID, text string) (contextToken string)
func (s *Server) PushMessage(m ilinkwire.Message) (contextToken string)
func (s *Server) Sent() []SentRecord // 按到达顺序
func (s *Server) Typing() []TypingRecord

// 故障注入
func (s *Server) ExpireSession() // 之后所有 POST 返回 -14，直到再次 ConfirmScan
func (s *Server) FailNext(path string, mode FailMode)
func (s *Server) SetReplyLimit(n int)
```

### 3.5 假 ACP agent `internal/testkit/fakeagent` 与 `cmd/ferry-fakeagent`（T2 实现）

```go
package fakeagent

// Serve 在给定读写端上运行假 agent，直到 in 关闭或 ctx 结束。
// in 关闭时，先把已受理请求的响应写完再返回。home 为会话持久化目录。
func Serve(ctx context.Context, in io.Reader, out io.Writer, home string) error
```

`cmd/ferry-fakeagent`：

- 默认调用 `Serve(ctx, os.Stdin, os.Stdout, $FAKEAGENT_HOME)`；`FAKEAGENT_HOME` 为空时用临时目录。
- `ferry-fakeagent --resume <id>`：打印 `resumed <id>` 后退出 0（P2 测试「接回终端」用）。

能力：`initialize` 返回协议版本 1、`loadSession: true`；实现会话列表（可按 cwd 过滤）与会话加载。会话持久化为 `<home>/sessions/<id>.json`，内容包含 id、cwd、title（第一条 prompt 的前 20 个字符）、updated_at、history。加载时先把历史以 `user_message_chunk` 与 `agent_message_chunk` 重放，再返回。

按 prompt 文本前缀决定行为：

| 输入 | 行为 |
| --- | --- |
| `echo <文本>` | 把 `<文本>` 每 5 个字符一段流式返回，以 `end_turn` 结束 |
| `ask <工具种类> <路径>` | 发起 `session/request_permission`：`toolCall.kind` 为参数，`locations` 含路径，选项为 `allow-once`（allow_once）、`allow-always`（allow_always）、`reject-once`（reject_once）。选中允许类回复 `allowed`，选中拒绝类回复 `denied`，结果为取消时以 `cancelled` 结束 |
| `slow <毫秒>` | 每 100ms 发一个 `.`，到时后以 `end_turn` 结束；收到 `session/cancel` 后 200ms 内以 `cancelled` 结束 |
| `burst <n>` | 连续发送 n 段 `agent_message_chunk`，每段 60 个字符加换行，间隔 50ms |
| `plan` | 发送一个三步 plan 更新后结束 |
| `tool` | 发送 tool_call（kind=execute，title=`run tests`，pending）→ tool_call_update（completed）→ 回复 `done` |
| `fail` | 本次 `session/prompt` 返回 JSON-RPC 错误 -32603 |
| 其他 | 回复 `unknown: <文本>` |

### 3.6 i18n `internal/i18n` 与 `locales`（T3 实现）

```go
package locales // 位于仓库根目录 locales/

import "embed"

//go:embed *.toml
var FS embed.FS
```

```go
package i18n

type Bundle struct{ /* 由 T3 定义 */ }

func Load(fsys fs.FS) (*Bundle, error) // 读取 en.toml 与 zh-CN.toml
func Default() *Bundle                  // 使用 locales.FS；资源在编译期嵌入，加载失败时 panic
// T 按 lang 取模板，用 args 渲染（go-i18n 模板语法 {{.Name}}）。
// 缺键时返回 key 本身并记一条 warn；未知语言回落到 zh-CN。
func (b *Bundle) T(lang fsp.Lang, key string, args map[string]any) string
func (b *Bundle) Keys(lang fsp.Lang) []string
```

locale 文件为扁平键值，例如 `"hint.no_session" = "..."`。P0 只有两个键：`app.name`、`app.tagline`。

### 3.7 check-i18n 规则（T3 实现）

用法：`go run ./scripts/check-i18n [--root <目录>] [--strict]`

1. 成对文件必须同时存在：`README.md` 与 `README.zh-CN.md`、`CONTRIBUTING.md` 与 `CONTRIBUTING.zh-CN.md`、`SECURITY.md` 与 `SECURITY.zh-CN.md`；`spec/fsp.md` 与 `spec/fsp.zh-CN.md` 任一存在时另一个也必须存在。
2. `docs/en/` 与 `docs/zh-CN/` 下的相对路径集合完全相同。
3. 每对文件：`## ` 与 `### ` 标题数量分别相等；围栏代码块数量相等；逐行比较代码块时，先去掉每行 `#` 或 `//` 之后的注释和行尾空白，结果必须相同。
4. 英文文件前 5 行内有指向中文版的链接；中文文件前 5 行内有指向英文版的链接。
5. `locales/en.toml` 与 `locales/zh-CN.toml` 的键集合完全相同。
6. 中文文件第一行可以写 `<!-- source_commit: <hash> -->`。带 `--strict` 且 git 可用时，英文文件在该提交之后有改动即报错；不带时只打印 warn。

输出：全部通过打印 `i18n ok` 并退出 0；否则每个问题打印一行 `i18n: <文件>: <问题>`，退出 1。

### 3.8 CLI 骨架（T0 实现）

- `cmd/ferry/main.go`：`var rootCmd = &cobra.Command{Use: "ferry"}`；`main()` 执行 `rootCmd.Execute()`，出错时退出码 1。
- `cmd/ferry/version.go`：`version` 子命令，输出 `ferry <version> (<commit>)`。`version` 与 `commit` 为包级变量，默认 `dev` 与 `none`，发行时由 `-ldflags "-X main.version=... -X main.commit=..."` 注入。

### 3.9 失败面

| 场景 | 行为 |
| --- | --- |
| 假 iLink：POST 缺少 `AuthorizationType` 或 `Authorization` | HTTP 401，body `{"ret":-1,"errmsg":"unauthorized"}` |
| 假 iLink：注入会话过期后 | 所有 POST 返回 HTTP 200，body `{"ret":-14,"errcode":-14,"errmsg":"session expired"}`，直到再次 `ConfirmScan` |
| 假 iLink：`context_token` 未知 | `{"ret":-2,"errmsg":"invalid context_token"}` |
| 假 iLink：超出回复上限 | 仍返回 `{}`，该条记录 `Dropped=true`（模拟真实网关静默吞掉） |
| 假 iLink：`context_token` 超过 WindowTTL | ExpiredDrop 同上；ExpiredError 返回 `{"ret":-2,"errmsg":"context_token expired"}` |
| 假 iLink：`getuploadurl` | 本轮返回 `{"ret":-1,"errmsg":"not implemented"}` |
| 假 agent：未知会话 ID | JSON-RPC 错误 -32602 |
| 假 agent：`fail` 指令 | JSON-RPC 错误 -32603 |
| check-i18n 发现问题 | 逐条打印，退出码 1 |

### 3.10 文件归属表

每个文件只有一个 owner。不在自己白名单里的文件一律只读。

| 路径 | owner | 其他任务 |
| --- | --- | --- |
| `go.mod`、`go.sum`、`internal/deps/**` | T0 | 只读；需要新依赖 → 报告 |
| `cmd/ferry/**` | T0 | 只读 |
| `internal/fsp/**`、`internal/ilinkwire/**`、`internal/clock/**`、`internal/ids/**` | T0 | 只读，改动即任务失败 |
| 空包占位 `doc.go`：`internal/` 下的 ports、core、handoff、registry、outbox、approval、acl、command、config、store、notify、security、control、mcpmini；`internal/im/` 下的 wechat、feishu、channelhost；`internal/driver/` 下的 acp、server、proxy、claudechannel、pty | T0 | 只读 |
| `internal/testkit/fakeilink/**` | T1（T0 只放占位） | 不可改 |
| `internal/testkit/fakeagent/**`、`cmd/ferry-fakeagent/**` | T2（T0 只放占位） | 不可改 |
| `locales/**`、`internal/i18n/**`、`scripts/check-i18n/**`、`README.md`、`README.zh-CN.md`、`CONTRIBUTING*.md`、`SECURITY*.md`、`docs/en/**`、`docs/zh-CN/**`、`docs/glossary.md`、`spec/**` | T3（T0 只放占位） | 不可改 |
| `.github/**`、`Makefile`、`.goreleaser.yaml`、`scripts/install.sh`、`scripts/release/**` | T4 | 不可改 |
| `docs/dev/**`、`LICENSE` | 人工 | 所有任务只读 |

## 4. 基线

- T0 起点：`main` 上的 `docs: add build specs` 提交，工作区干净。
- 并行基线：标签 `p0-base`，T0 完成且人工复核后打。所有 worktree 从这个标签开出。

## 5. 任务表

| 任务 | 目标 | 可写路径 | 时序 |
| --- | --- | --- | --- |
| T0 | 契约与骨架 | 3.10 中 owner 为 T0 的路径，以及其他 owner 目录里的占位文件 | 先跑；打 `p0-base` 后其余才起 |
| T1 | 假 iLink 服务 | `internal/testkit/fakeilink/**` | 并行 |
| T2 | 假 ACP agent | `internal/testkit/fakeagent/**`、`cmd/ferry-fakeagent/**` | 并行 |
| T3 | 双语文档、i18n、check-i18n、FSP 规范文本 | 见 3.10 | 并行 |
| T4 | CI、Makefile、发行与安装脚本 | 见 3.10 | 并行 |
| TΩ | 合流与全量验收 | 全仓 | 最后 |

### 5.1 T1 细则：假 iLink 服务

- 基于 `httptest.NewServer`，按 3.2 的路径与字段实现；`getuploadurl` 按 3.9 返回未实现。
- 登录：`get_bot_qrcode` 每次返回新的 `qrcode` 与 `qrcode_img_content`。`ConfirmScan` 之前，`get_qrcode_status` 返回 `wait`；之后返回 `confirmed`，并带 `bot_token`、`ilink_bot_id`、`baseurl`（即 `s.URL`）、`ilink_user_id`。
- `getupdates`：游标之后有消息就立即返回；否则挂起到 LongPoll，再返回空 `msgs`。响应带 `longpolling_timeout_ms` 与新游标。游标是不透明字符串。
- `PushText` 与 `PushMessage` 为每条消息生成新的 `context_token` 和递增的 `message_id`，`message_type` 为 1。
- `sendmessage`：校验 `context_token` 已知；同一 `client_id` 再次到达记为 `Duplicate`，不重复计数；按 ReplyLimit 与 WindowTTL 处理；成功返回 `{}`。
- `getconfig` 返回固定的 `typing_ticket`；`sendtyping` 记入 `Typing()`。
- 并发安全；`Close` 释放所有挂起的请求。
- 测试至少包含：TestLoginFlow、TestLongPoll（推送后 1 秒内返回；空闲时按 LongPoll 返回空列表）、TestSend（client_id 去重）、TestReplyLimit、TestExpire、TestWindowTTL（用假时钟推进）、TestAuth。

### 5.2 T2 细则：假 ACP agent

- 用 acp-go-sdk v0.13.5 的 agent 侧连接实现 3.5 的全部行为。
- 测试在进程内用 `io.Pipe` 连接 SDK 的客户端侧。
- 测试至少包含：TestEcho、TestPermissionAllow、TestPermissionReject、TestCancel（slow 期间取消，且有未决审批时同样以 cancelled 结束）、TestListAndLoad、TestBurst、TestPlanAndTool、TestFail、TestResumeFlag（构建二进制后执行 `--resume abc`，输出 `resumed abc`）。

### 5.3 T3 细则：双语与 i18n

- `README.md` 与 `README.zh-CN.md`：首屏采用产品方案 8.3 的草稿，安装命令里的 `<owner>` 换成 `TomwaltW`。
- `docs/en/` 与 `docs/zh-CN/`：建 quickstart、wechat、agents、handoff、security、faq 六个文件，每个有一级标题和至少两个二级标题的提纲，并注明内容随 P1–P2 补充。
- `docs/glossary.md`：中英术语对照（会话、托管会话、接力会话、入口、发件箱、投递窗口、审批、审批人、驱动、配对）。
- `spec/fsp.md`（英文规范文本）与 `spec/fsp.zh-CN.md`（中文，注明仅供参考）：把 3.1 的类型写成文字规范。
- `CONTRIBUTING*.md`：摘要 00 总览第 3 节的约定，写明双语同步义务；`SECURITY*.md`：漏洞报告方式，邮箱先写占位 `security@example.com` 并注明待替换。
- `check-i18n` 按 3.7 实现，测试夹具放 `scripts/check-i18n/testdata/`。

### 5.4 T4 细则：CI 与发行

- `Makefile` 目标：`fmt-check`、`vet`、`test`、`i18n`、`cross`、`ci-local`（依次执行前五个）。`cross` 以 `CGO_ENABLED=0` 编译 darwin、linux、windows 各自的 amd64 与 arm64，共 6 个产物，放 `dist/`。
- `.github/workflows/ci.yml`：push 与 PR 触发。任务：lint（gofmt、vet）；test（ubuntu、macos、windows 三平台 `go test ./...`）；race（ubuntu，`CGO_ENABLED=1 go test -race ./...`）；i18n；cross。Go 版本 1.27.x，开启模块缓存。
- `.github/workflows/release.yml`：推送 `v*` 标签触发，用 GoReleaser（v2 配置格式）打包 6 个平台、生成 `checksums.txt`、发布到 GitHub Releases；Release 说明英文在前、中文在后。
- `.goreleaser.yaml`：注入 `main.version` 与 `main.commit`；归档名 `ferry_<版本>_<os>_<arch>`。
- `scripts/install.sh`：检测系统与架构，从 GitHub Releases 下载并校验 sha256，安装到 `~/.local/bin`（可写时）或 `/usr/local/bin`；`LANG` 以 `zh` 开头时输出中文，否则英文；环境变量 `FERRY_GITHUB_PROXY` 非空时作为下载地址前缀。
- 本任务的 worktree 里 check-i18n 还是占位实现，`make i18n` 能跑通即可。

### 5.5 TΩ 细则

- 合并顺序：T1 → T2 → T3 → T4。

## 6. 每个任务的验收

| 任务 | 必须通过 |
| --- | --- |
| T0 | G1–G3，A2，T0-1 至 T0-4 |
| T1 | B1、B2、B3，T1-1，G1–G3 |
| T2 | B4，T2-1，G1–G3 |
| T3 | B5、B6，T3-1、T3-2，G1–G3 |
| T4 | B7，T4-1、T4-2，G1–G3 |
| TΩ | 全部 A、B、C |

专属验收命令：

```bash
# T0-1 测试可执行（可以全是空测试，但不能报找不到包）
go test ./...
# T0-2 包数量不少于 25
test "$(go list ./... | wc -l)" -ge 25
# T0-3 依赖已锁定
grep -q 'github.com/coder/acp-go-sdk v0.13.5' go.mod
# T0-4 占位 check-i18n 可运行，输出 i18n ok
go run ./scripts/check-i18n

# T1-1 稳定性
go test ./internal/testkit/fakeilink/ -count=3

# T2-1 二进制冒烟：输出的第一行含 "protocolVersion":1
go build -o /tmp/ferry-fakeagent ./cmd/ferry-fakeagent
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}' | /tmp/ferry-fakeagent | head -n 1

# T3-1 i18n 包
go test ./internal/i18n/ -v
# T3-2 README 互链
head -n 5 README.md | grep -q 'README.zh-CN.md' && head -n 5 README.zh-CN.md | grep -q 'README.md'

# T4-1 安装脚本语法
sh -n scripts/install.sh
# T4-2 交叉编译产物不少于 6 个
make cross && test "$(ls dist | wc -l)" -ge 6
```

## 7. 卡住了怎么办

见 00 总览 3.8。报告 = 在回执里写清卡点、试过什么、需要什么决定，然后结束本轮，不要硬做。

## 8. 派工提示词

T0 先单独发。T0 done、你复核并打 `p0-base` 标签后，再同时发 T1–T4。全部 done 后发 TΩ。

### P0-T0

````
# 任务 P0-T0 — 契约与骨架

## 工作区
直接在 main 分支工作。开始前确认 `git status --short` 输出为空。

## 先读
- docs/dev/00-overview.md 第 3 节（全局约定）
- docs/dev/spec-p0.md 第 3 节（冻结契约）、第 6 节 T0 那一行

## 目标
把 spec-p0 第 3 节的契约写成真实代码，让整个仓库在「全是空实现」的状态下能编译、测试能执行。

## 具体要做
1. `go mod init github.com/TomwaltW/ferry`，go.mod 写 `go 1.27`。
2. 按 3.1、3.2 写出 internal/fsp 与 internal/ilinkwire（纯函数直接实现）。
3. 按 3.3 写出 internal/clock（Real 与 Fake 都要真正可用）和 internal/ids。
4. 按 3.4–3.6 写出 fakeilink、fakeagent、i18n、locales 的导出签名，函数体一律 `panic("not implemented")` 或返回零值；locales 放 embed.go 与两份只含 `app.name` 的 toml。
5. internal/deps/deps.go 用空白导入锁住依赖白名单中 P0 的 7 个模块，然后 `go mod tidy`。modernc.org/sqlite 用当时最新稳定版。
6. 按 3.10 建好全部目录，每个空包放一个 doc.go（一行英文包注释）。
7. 按 3.8 写 cmd/ferry/main.go 与 version.go。
8. scripts/check-i18n/main.go 放占位实现：打印 `i18n ok`，退出 0。cmd/ferry-fakeagent/main.go 放占位 main。

## 不要做的事
不实现任何业务逻辑，不实现测试替身的行为。你的产出应该是「能编译、测试可执行」的骨架。越界写实现会和并行任务冲突。

## 可写文件
spec-p0 第 3.10 节中 owner 为 T0 的路径，以及其他 owner 目录里的占位文件（doc.go、占位 main、占位 toml、只有签名的 .go 文件）。

## 禁改
- docs/dev/**
- LICENSE

## 验收（全部自己跑，8 项）
1. `go build ./...` → 退出码 0
2. `go vet ./...` → 无输出
3. `test -z "$(gofmt -l .)"` → 退出码 0
4. `go run ./cmd/ferry version` → 输出 `ferry dev (none)`
5. `go test ./...` → 能执行，没有 "cannot find package"
6. `test "$(go list ./... | wc -l)" -ge 25` → 退出码 0
7. `grep -q 'github.com/coder/acp-go-sdk v0.13.5' go.mod` → 退出码 0
8. `go run ./scripts/check-i18n` → 输出 `i18n ok`

## 完成后
git add -A && git commit -m "p0-t0: freeze contracts and skeleton"

输出回执，正文附 `git show --stat HEAD` 的输出。最后一行严格为：
RECEIPT P0-T0 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P0-T1

````
# 任务 P0-T1 — 假 iLink 服务

## 工作区
git fetch --tags
git worktree add -b p0-t1 ../ferry-p0-t1 p0-base
cd ../ferry-p0-t1

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.2、3.4、3.9 节，第 5.1 节，第 6 节 T1 那一行

## 目标
实现 internal/testkit/fakeilink：一个基于 httptest 的假 iLink 服务，行为严格按 3.4 与 5.1。后续所有微信相关测试都依赖它。

## 可写文件（只有这些）
- internal/testkit/fakeilink/**

## 禁改
- docs/dev/**
- internal/fsp/**、internal/ilinkwire/**、internal/clock/**、go.mod、go.sum
- 白名单以外的任何文件

契约不对就停下来报告，不要自己改。你改通了，其他并行任务会全挂，而且要到合流才发现。

## 验收（全部自己跑，7 项）
1. `go test ./internal/testkit/fakeilink/ -run TestLoginFlow -v` → PASS
2. `go test ./internal/testkit/fakeilink/ -run TestLongPoll -v` → PASS
3. `go test ./internal/testkit/fakeilink/ -run 'TestSend|TestReplyLimit|TestExpire|TestWindowTTL|TestAuth' -v` → 5 个测试全部 PASS
4. `go test ./internal/testkit/fakeilink/ -count=3` → ok
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

窗口过期用 internal/clock 的假时钟推进，不要 sleep。
验收过不了就继续改；确信验收写错了，停下来报告，不要改验收。

## 完成后
git add -A && git commit -m "p0-t1: fake ilink server"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P0-T1 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P0-T2

````
# 任务 P0-T2 — 假 ACP agent

## 工作区
git fetch --tags
git worktree add -b p0-t2 ../ferry-p0-t2 p0-base
cd ../ferry-p0-t2

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.5、3.9 节，第 5.2 节，第 6 节 T2 那一行

## 目标
用 acp-go-sdk v0.13.5 实现 internal/testkit/fakeagent 与 cmd/ferry-fakeagent：一个行为确定、可脚本化的 ACP agent，供后续所有驱动测试使用。

## 可写文件（只有这些）
- internal/testkit/fakeagent/**
- cmd/ferry-fakeagent/**

## 禁改
- docs/dev/**
- internal/fsp/**、internal/clock/**、go.mod、go.sum
- 白名单以外的任何文件

契约不对就停下来报告，不要自己改。

## 验收（全部自己跑，5 项）
1. `go test ./internal/testkit/fakeagent/ -v` → PASS，且包含 TestEcho、TestPermissionAllow、TestPermissionReject、TestCancel、TestListAndLoad、TestBurst、TestPlanAndTool、TestFail、TestResumeFlag
2. 二进制冒烟 → 输出第一行含 `"protocolVersion":1`：
   go build -o /tmp/ferry-fakeagent ./cmd/ferry-fakeagent
   printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}' | /tmp/ferry-fakeagent | head -n 1
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p0-t2: fake acp agent"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P0-T2 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P0-T3

````
# 任务 P0-T3 — 双语文档、i18n 与 check-i18n

## 工作区
git fetch --tags
git worktree add -b p0-t3 ../ferry-p0-t3 p0-base
cd ../ferry-p0-t3

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.1、3.6、3.7 节，第 5.3 节，第 6 节 T3 那一行

## 目标
建立中英双语的文档与多语言基础：README 双语首屏、文档提纲、术语表、FSP 规范文本、i18n 包，以及保证两种语言不走样的 check-i18n 工具。

## 可写文件（只有这些）
- locales/**
- internal/i18n/**
- scripts/check-i18n/**
- README.md、README.zh-CN.md
- CONTRIBUTING.md、CONTRIBUTING.zh-CN.md、SECURITY.md、SECURITY.zh-CN.md
- docs/en/**、docs/zh-CN/**、docs/glossary.md
- spec/**

## 禁改
- docs/dev/**、LICENSE
- internal/fsp/**、go.mod、go.sum
- 白名单以外的任何文件

## 验收（全部自己跑，7 项）
1. `go run ./scripts/check-i18n` → 输出 `i18n ok`，退出码 0
2. `go test ./scripts/check-i18n/ -v` → PASS，覆盖缺少成对文件、标题数量不一致、locale 键不一致三种违规
3. `go test ./internal/i18n/ -v` → PASS（覆盖正常取值、缺键回退、未知语言回落 zh-CN、模板参数）
4. `head -n 5 README.md | grep -q 'README.zh-CN.md' && head -n 5 README.zh-CN.md | grep -q 'README.md'` → 退出码 0
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p0-t3: bilingual docs and i18n"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P0-T3 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P0-T4

````
# 任务 P0-T4 — CI、Makefile 与发行脚本

## 工作区
git fetch --tags
git worktree add -b p0-t4 ../ferry-p0-t4 p0-base
cd ../ferry-p0-t4

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 5.4 节，第 6 节 T4 那一行

## 目标
配齐持续集成、本地一键检查、多平台交叉编译、标签发布与中英双语安装脚本。

## 可写文件（只有这些）
- .github/**
- Makefile
- .goreleaser.yaml
- scripts/install.sh
- scripts/release/**

## 禁改
- docs/dev/**、LICENSE、go.mod、go.sum
- 白名单以外的任何文件

## 验收（全部自己跑，6 项）
1. `sh -n scripts/install.sh` → 退出码 0
2. `make cross && test "$(ls dist | wc -l)" -ge 6` → 退出码 0
3. `make ci-local` → 退出码 0
4. `go build ./...` → 退出码 0
5. `go vet ./...` → 无输出
6. `test -z "$(gofmt -l .)"` → 退出码 0

本机装有 goreleaser 时，另跑一次 `goreleaser check` 并把输出写进回执；没有装就注明未跑，不算失败。
dist/ 不要提交（加进 .gitignore 属于 T0 的文件，若缺失请在回执中报告，不要自己改）。

## 完成后
git add -A && git commit -m "p0-t4: ci and release tooling"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P0-T4 status=<done|blocked> commit=<短hash> checks=<通过数>/6 files=<改动文件数>
````

### P0-TΩ

````
# 任务 P0-TΩ — 集成与合流

## 工作区
main 分支，当前应位于标签 p0-base。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 2 节、第 3.10 节、第 5 节

## 待合分支（按顺序）
1. p0-t1
2. p0-t2
3. p0-t3
4. p0-t4

## 做法
一次合一个：`git merge --no-ff <分支>`，合完立刻跑下面的全量验收，全绿再合下一个。不要全合完再跑，否则坏了分不清是谁。
出现文件冲突时，按 spec-p0 第 3.10 节归属表判定谁该赢；表里没写的，停下来报告。

## 允许的改动
只允许修集成缝隙——两个任务各自正确、接起来不对的地方。不在这里补任何任务漏掉的功能；漏了就在回执里写明，退回给对应任务。

## 全量验收（13 项）
1. `go build ./...` → 退出码 0
2. `go run ./cmd/ferry version` → 以 `ferry ` 开头
3. `go test ./...` → 全部 ok
4. `go test ./internal/testkit/fakeilink/ -run TestLoginFlow -v` → PASS
5. `go test ./internal/testkit/fakeilink/ -run TestLongPoll -v` → PASS
6. `go test ./internal/testkit/fakeilink/ -run 'TestSend|TestReplyLimit|TestExpire|TestWindowTTL|TestAuth' -v` → 5 个 PASS
7. `go test ./internal/testkit/fakeagent/ -v` → PASS
8. `go run ./scripts/check-i18n` → `i18n ok`
9. `go test ./scripts/check-i18n/ -v` → PASS
10. `make ci-local` → 退出码 0
11. `go vet ./...` → 无输出
12. `test -z "$(gofmt -l .)"` → 退出码 0
13. `git status --short` → 无未提交改动

## 完成后
不要推送，不要打标签，这两件事由人来做。

回执正文：每次合入后的验收结果；遇到的冲突与判定依据；还差什么。最后一行严格为：
RECEIPT P0-TΩ status=<done|blocked> merged=<合入数>/4 checks=<通过数>/13
````

## 9. 人工收尾

1. 复核 TΩ 回执，在 `main` 上亲自跑一遍 `make ci-local`。
2. `git push origin main`，确认 GitHub Actions 的 ci 工作流全绿（race 任务也要绿）。
3. `git tag p0-done && git push origin p0-done`。
4. 清理 worktree：`git worktree remove ../ferry-p0-t1`（t2、t3、t4 同理）。
