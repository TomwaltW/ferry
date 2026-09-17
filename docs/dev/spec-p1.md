# 开发派工 Spec — Ferry P1 微信 + 托管会话

> 生成于 2026-09-17。本轮唯一权威文档，另须遵守 `docs/dev/00-overview.md` 第 3 节。
> 前置条件：`p0-done` 已在 `main`。冻结后不再修改。

## 1. 问题定义

**要做什么**：用户在电脑上执行 `ferry init`、`ferry login wechat` 扫码、`ferry daemon` 常驻后，在微信私聊里用 `/new` 开托管会话，给任意 ACP agent 派活，依次收到受理回执、进度、审批请求和结果；陌生人必须先配对才能使用。

**为什么**：这是第一个能真实使用的版本，也是 P1 验证关卡给 20 名用户试用的版本。

**明确不做**：

- 终端接力，以及 `/ls` 里的终端会话（P2）
- 媒体收发、窗口外积压与摘要、唤醒通道；`/pull` 与 `/file` 本轮只回复「后续版本提供」（P2）
- 群聊、第二个 IM、`ferry proxy`、Claude Code 插件、ACP v2、PTY（P3）
- 系统服务安装、多用户隔离（P4）

## 2. 验收标准（DoD）

### 2.1 能跑

- **A1** `go build ./... && go test ./...` → 全部 ok
- **A2** `FERRY_HOME=$(mktemp -d) go run ./cmd/ferry doctor` → 逐项打印检查结果；缺少配置时提示运行 `ferry init`；退出码 0
- **A3** `go run ./cmd/ferry --help` → 列出 init、login、logout、daemon、pair、ls、status、doctor、version

### 2.2 行为正确

端到端测试全部基于假 iLink 服务与假 agent，由 TΩ 写在 `test/e2e/`。e2e 配置：`outbox.merge_interval = "200ms"`，`policy.approval_timeout = "2s"`，agent `fake` 的命令指向测试时构建的 `ferry-fakeagent`，工作区根目录为临时目录（下文记作 `<ws>`）。

- **B1** `go test ./test/e2e/ -run TestLoginAndOwner -v` → 扫码确认后适配器在线；扫码人被登记为 owner，并且是审批人
- **B2** `-run TestStrangerPairing` → 陌生人的消息不进 agent，只收到含 6 位配对码的回复；owner 在微信里发 `/pair <码>` 后，该陌生人的消息能进 agent；审计表有 `rejected` 记录
- **B3** `-run TestManagedSession` → `/new fake <ws>/demo` 后发 `echo hello`：先收到受理回执，再收到含 `hello` 的结果；`/ls` 列出该会话，状态为空闲
- **B4** `-run TestApprovalFlow` → `ask execute <ws>/demo/run.sh` 产生含 5 位 ID 的审批消息；回复 `Y <ID>` 后 agent 回复 `allowed`；回复 `y zzzzz` 没有任何效果；再触发一次审批且不回复，2 秒后 agent 回复 `denied`，微信收到超时提示
- **B5** `-run TestPolicy` → `ask read <ws>/demo/a.txt` 不产生审批消息，agent 回复 `allowed`；`ask delete /etc/passwd` 不产生审批消息，agent 回复 `denied`
- **B6** `-run TestStop` → `slow 5000` 执行中发 `/stop`，agent 以 cancelled 结束，微信收到「已停止」
- **B7** `-run TestBusyMerge` → `slow 1000` 执行中依次发 `echo a`、`echo b`、`echo c`；本轮结束后 agent 只再收到 1 次 prompt，内容为三条按顺序以空行连接
- **B8** `-run TestReplyBudget` → `burst 30` 对应的发送记录不超过 8 条，且假网关没有任何 `Dropped` 记录
- **B9** `-run TestSessionExpired` → 假网关注入 -14 后，守护进程状态显示微信离线；重新 `ConfirmScan` 并用新凭据重启适配器后恢复收发
- **B10** `-run TestRestartResume` → 守护进程重启后 `/ls` 仍列出会话（状态为已停放）；`/use 1` 通过 `session/load` 恢复，`echo again` 能得到回复
- **B11** `-run TestWorkspaceDenied` → `/new fake /tmp` 被拒绝，并回复目录不在允许范围内
- **B12** `go run ./scripts/check-i18n` → `i18n ok`

### 2.3 没弄坏

- **C1** `go test ./internal/testkit/... ./internal/i18n/... ./scripts/...` → 全部 ok（P0 产物仍然正确）
- **C2** 00 总览 3.5 的 G1–G3 → 全部通过

## 3. 冻结契约

以下由 T0 写成代码，本轮内不可更改。

### 3.1 模块接口 `internal/ports`

```go
package ports

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// ---------- IM 适配器 ----------

type IMAdapter interface {
	Platform() fsp.Platform
	// Run 阻塞到 ctx 结束，入站消息写入 out。平台会话过期时返回 fsp.ErrSessionExpired。
	Run(ctx context.Context, out chan<- fsp.Inbound) error
	// Send 同步发送一条消息，自身不重试。replyCtx 为空时返回 fsp.ErrNoWindow。
	Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error)
	// Typing 设置「正在输入」；平台不支持时返回 nil。
	Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error
	Status() AdapterStatus
}

type AdapterStatus struct {
	Online  bool
	BotID   string
	OwnerID string // 扫码登录者
	Detail  string // 离线原因等
}

type Credentials struct {
	BotToken string
	BotID    string
	OwnerID  string
	BaseURL  string
}

type Login interface {
	// Start 取二维码，返回用于渲染二维码的内容。
	Start(ctx context.Context) (qrContent string, err error)
	// Wait 阻塞到扫码确认、二维码过期（自动刷新至多 3 次，每次调用 onQR）或 ctx 结束。
	Wait(ctx context.Context, onQR func(qrContent string)) (Credentials, error)
}

// ---------- Agent 驱动 ----------

type SessionSpec struct {
	Agent   string   // 配置里的 agent 名
	Command []string // 可执行文件与参数
	Cwd     string   // 绝对路径
	Env     []string // KEY=VALUE，已按白名单过滤
}

type Caps struct{ Load, Resume, List, Cancel, Config bool }

type AgentSessionInfo struct {
	AgentSessionID string
	Title          string
	Cwd            string
	UpdatedAt      time.Time
}

type Driver interface {
	Kind() fsp.DriverKind
	Start(ctx context.Context, spec SessionSpec, ap Approver) (AgentSession, error)
	// Resume 恢复已有会话；agent 不支持时返回 fsp.ErrUnsupported。
	Resume(ctx context.Context, spec SessionSpec, agentSessionID string, ap Approver) (AgentSession, error)
	// List 列出 agent 已有会话；不支持时返回 fsp.ErrUnsupported（P2 使用）。
	List(ctx context.Context, spec SessionSpec) ([]AgentSessionInfo, error)
}

type AgentSession interface {
	AgentSessionID() string
	Caps() Caps
	// Prompt 立即返回；输出与结束经 Events 送出。本轮未结束时返回 fsp.ErrBusy。
	Prompt(ctx context.Context, text string) error
	Cancel(ctx context.Context) error
	// SetConfig 设置会话配置项；key 为 mode 时映射到会话模式。不支持时返回 fsp.ErrUnsupported。
	SetConfig(ctx context.Context, key, value string) error
	// Events 在会话关闭或进程退出后关闭；进程意外退出时先发一个 EvError。
	// 驱动不知道 Ferry 的会话 ID，Event.Session 留空，由核心补上。
	Events() <-chan fsp.Event
	Close() error
}

// Approver 由驱动在 agent 请求权限时调用，返回前阻塞。
// 驱动不填 req.ID 与 req.Session；核心传入的 Approver 会补上 Session，审批模块分配 ID。
type Approver interface {
	Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error)
}

type AgentInfo struct {
	Name      string
	Display   string
	Source    string // registry | command
	Available bool
	Reason    string // 不可用的原因
}

type Registry interface {
	// Resolve 返回启动规格（Cwd 由调用方填写）；agent 未配置时返回 fsp.ErrNotFound。
	Resolve(ctx context.Context, agent string) (SessionSpec, error)
	Agents(ctx context.Context) []AgentInfo
}

// ---------- 存储 ----------

type KV interface {
	GetKV(ctx context.Context, key string) (string, error) // 不存在返回 fsp.ErrNotFound
	SetKV(ctx context.Context, key, value string) error
}

type Binding struct {
	Endpoint   fsp.EndpointID
	Session    fsp.SessionID
	Slot       int
	Foreground bool
}

type OutboxRow struct {
	ID        int64
	Msg       fsp.Outbound
	State     fsp.DeliveryState
	Attempts  int
	CreatedAt time.Time
}

type Window struct {
	Endpoint fsp.EndpointID
	ReplyCtx string
	OpenedAt time.Time
	Used     int // 本窗口已消耗的回复数
}

type Pairing struct {
	Code      string
	Endpoint  fsp.EndpointID
	SenderID  string
	ExpiresAt time.Time
}

type AuditEntry struct {
	At       time.Time
	Kind     string // inbound rejected approval_asked approval_decided outbound_failed session_started session_parked session_closed login expired
	Endpoint fsp.EndpointID
	SenderID string
	Session  fsp.SessionID
	Detail   string // 不含消息正文
}

type Store interface {
	KV

	PutSession(ctx context.Context, s fsp.Session) error // 按 ID 插入或覆盖
	GetSession(ctx context.Context, id fsp.SessionID) (fsp.Session, error)
	ListSessions(ctx context.Context, owner fsp.EndpointID) ([]fsp.Session, error) // 按 CreatedAt 升序
	ActiveSessions(ctx context.Context) ([]fsp.Session, error)                     // 状态不是 closed 的全部会话

	Bind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) (slot int, err error) // slot 取该入口下最小未用正整数
	Unbind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error
	SetForeground(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error
	Bindings(ctx context.Context, ep fsp.EndpointID) ([]Binding, error) // 按 Slot 升序

	// SeenMessage 首次见到返回 false 并记录；记录保留 24 小时。
	SeenMessage(ctx context.Context, ep fsp.EndpointID, messageID string, at time.Time) (bool, error)

	AddOutbox(ctx context.Context, msg fsp.Outbound, at time.Time) (int64, error)
	UpdateOutbox(ctx context.Context, id int64, state fsp.DeliveryState, attempts int, at time.Time) error
	OutboxByState(ctx context.Context, ep fsp.EndpointID, state fsp.DeliveryState) ([]OutboxRow, error) // 按 ID 升序
	OutboxEndpoints(ctx context.Context, state fsp.DeliveryState) ([]fsp.EndpointID, error)
	SaveWindow(ctx context.Context, w Window) error
	LoadWindow(ctx context.Context, ep fsp.EndpointID) (Window, error) // 不存在返回 fsp.ErrNotFound

	PutApproval(ctx context.Context, req fsp.ApprovalRequest) error
	DecideApproval(ctx context.Context, v fsp.Verdict, at time.Time) error

	SetRole(ctx context.Context, ep fsp.EndpointID, senderID, role string) error // owner | approver | member
	Role(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) // 无记录返回 ""
	AddPairing(ctx context.Context, p Pairing) error
	TakePairing(ctx context.Context, code string, now time.Time) (Pairing, error) // 取出即删除；不存在或过期返回 fsp.ErrNotFound
	CountPairings(ctx context.Context, ep fsp.EndpointID, now time.Time) (int, error)

	Audit(ctx context.Context, e AuditEntry) error
	Close() error
}

// ---------- 核心依赖的其他模块 ----------

type Outbox interface {
	// OpenWindow 在每条已放行的入站消息之后调用，重置该入口的回复额度。
	OpenWindow(ep fsp.EndpointID, replyCtx string, inboundAt time.Time)
	Enqueue(ctx context.Context, msg fsp.Outbound) error
	FlushHeld(ctx context.Context, ep fsp.EndpointID) error // P1 为空操作
	Run(ctx context.Context) error                          // 合并与发送循环，阻塞到 ctx 结束
}

type ACL interface {
	Allowed(ep fsp.EndpointID, senderID string) bool
	IsApprover(ep fsp.EndpointID, senderID string) bool
	SetOwner(ctx context.Context, platform fsp.Platform, botID, ownerID string) error
	OfferPairing(ctx context.Context, ep fsp.EndpointID, senderID string) (code string, err error) // 超上限返回 fsp.ErrQuotaExceeded
	ConfirmPairing(ctx context.Context, code string) (ep fsp.EndpointID, senderID string, err error)
}

type Approvals interface {
	Approver
	// ParseVerdict 识别整条消息形式的裁决，如 "y abcde"。
	ParseVerdict(text string) (id string, d fsp.Decision, ok bool)
	// Resolve 应用裁决；ID 不存在或已结束返回 false，调用方静默丢弃。
	Resolve(ctx context.Context, id string, d fsp.Decision, by string) bool
	// CancelSession 以取消结束该会话的全部未决审批。
	CancelSession(id fsp.SessionID)
}

type Command struct {
	Name string // ls new use agents stop close mode pull file lang help pair
	Args []string
}

type Commands interface {
	Parse(text string) (Command, bool)
	// Unescape：以 "//" 开头的消息去掉一个 "/" 后按普通文本处理，返回 (新文本, true)。
	Unescape(text string) (string, bool)
}

type I18n interface {
	T(lang fsp.Lang, key string, args map[string]any) string
}
```

### 3.2 构造函数

各任务按此签名实现，TΩ 按此装配。T0 在各包里写出签名与占位函数体。

```go
// internal/im/wechat（T1）
type Options struct {
	Config config.WeChatConfig
	Creds  ports.Credentials
	KV     ports.KV
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}
func New(opts Options) (*Adapter, error) // *Adapter 实现 ports.IMAdapter
func NewLogin(cfg config.WeChatConfig, clk clock.Clock) ports.Login

// internal/driver/acp（T2）
func New(log *slog.Logger) ports.Driver

// internal/registry（T2）
func New(cfg config.RegistryConfig, agents []config.AgentConfig, home string, log *slog.Logger) (ports.Registry, error)

// internal/store（T3）
func Open(path string) (ports.Store, error) // 打开 <home>/ferry.db 并执行迁移

// internal/acl（T3）
func New(cfg config.WeChatConfig, st ports.Store, clk clock.Clock) ports.ACL

// internal/approval（T3）
type Options struct {
	Policy      config.PolicyConfig
	Roots       []string // 已规范化的工作区根目录
	DefaultLang fsp.Lang
	Store       ports.Store
	Outbox      ports.Outbox
	I18n        ports.I18n
	Clock       clock.Clock
	Log         *slog.Logger
}
func New(opts Options) ports.Approvals

// internal/security（T3）
func SaveCredentials(home string, platform fsp.Platform, c ports.Credentials) error
func LoadCredentials(home string, platform fsp.Platform) (ports.Credentials, error) // 不存在返回 fsp.ErrNotFound
func DeleteCredentials(home string, platform fsp.Platform) error
func MaskSecrets(s string) string
func SanitizeForIM(s string, max int) string

// internal/outbox（T4）
type Options struct {
	Config config.OutboxConfig
	IM     ports.IMAdapter
	Store  ports.Store
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}
func New(opts Options) ports.Outbox

// internal/command（T4）
func New() ports.Commands

// internal/config（T4 实现；types.go 由 T0 写）
func Home() string                // FERRY_HOME，未设置时 ~/.ferry
func Path(home string) string     // <home>/config.toml
func Default() Config
func Load(path string) (Config, error) // 文件不存在返回 fsp.ErrNotFound；校验失败的错误信息含字段名
func Save(path string, c Config) error  // 文件权限 0600
func (c Config) Validate() error
func (c Config) NormalizedRoots() ([]string, error) // 展开 ~、转绝对路径、解析符号链接

// internal/core（T5）
type Deps struct {
	Config    config.Config
	Home      string
	IM        ports.IMAdapter
	Drivers   map[fsp.DriverKind]ports.Driver
	Registry  ports.Registry
	Store     ports.Store
	Outbox    ports.Outbox
	ACL       ports.ACL
	Approvals ports.Approvals
	Commands  ports.Commands
	I18n      ports.I18n
	Clock     clock.Clock
	Log       *slog.Logger
}
type Status struct {
	IMOnline  bool
	IMDetail  string
	BotID     string
	Sessions  int
	Running   int
	UpdatedAt time.Time
}
func New(d Deps) *Daemon
// Run 阻塞到 ctx 结束。IM 适配器返回 fsp.ErrSessionExpired 时不退出：标记离线、记审计，
// 之后每 60 秒调用一次 IM.Run 重试。
func (d *Daemon) Run(ctx context.Context) error
func (d *Daemon) Status() Status
```

### 3.3 配置 `internal/config`

`types.go`（T0 写）：

```go
package config

type Duration struct{ time.Duration } // TOML 里写 "30s"、"10m"；T4 实现文本编解码

type Config struct {
	Lang         fsp.Lang        `toml:"lang"`
	DefaultAgent string          `toml:"default_agent"`
	WeChat       WeChatConfig    `toml:"wechat"`
	Workspace    WorkspaceConfig `toml:"workspace"`
	Agents       []AgentConfig   `toml:"agents"`
	Policy       PolicyConfig    `toml:"policy"`
	Outbox       OutboxConfig    `toml:"outbox"`
	Registry     RegistryConfig  `toml:"registry"`
	Limits       LimitsConfig    `toml:"limits"`
}

type WeChatConfig struct {
	Enabled        bool     `toml:"enabled"`
	BaseURL        string   `toml:"base_url"`
	ChannelVersion string   `toml:"channel_version"`
	Render         string   `toml:"render"`    // plain | raw
	Approvers      []string `toml:"approvers"` // "owner" 或具体微信用户 ID
}

type WorkspaceConfig struct {
	Roots []string `toml:"roots"`
}

type AgentConfig struct {
	Name     string            `toml:"name"`
	Registry string            `toml:"registry"` // 与 Command 二选一
	Command  []string          `toml:"command"`
	Env      map[string]string `toml:"env"`
}

type PolicyRule struct {
	Kind             []string `toml:"kind"`              // 为空表示任意种类
	OutsideWorkspace *bool    `toml:"outside_workspace"` // 不写表示不看路径
	CommandPattern   string   `toml:"command_pattern"`   // 正则，匹配 InputPreview；为空表示不看
	Action           string   `toml:"action"`            // allow | ask | deny
}

type PolicyConfig struct {
	Default         string       `toml:"default"`
	Rules           []PolicyRule `toml:"rules"`
	ApprovalTimeout Duration     `toml:"approval_timeout"`
}

type OutboxConfig struct {
	MergeInterval       Duration `toml:"merge_interval"`
	ReplyBudget         int      `toml:"reply_budget"`
	LongOutputThreshold int      `toml:"long_output_threshold"` // 按字符（rune）计
	SendTimeout         Duration `toml:"send_timeout"`
}

type RegistryConfig struct {
	SnapshotOnly bool   `toml:"snapshot_only"`
	NPMRegistry  string `toml:"npm_registry"`
	GitHubProxy  string `toml:"github_proxy"`
}

type LimitsConfig struct {
	MaxManagedSessions int `toml:"max_managed_sessions"`
}
```

默认值（`Default()`）：

| 字段 | 默认 |
| --- | --- |
| `lang` | `zh-CN` |
| `default_agent` | `qwen-code` |
| `wechat.enabled` | `true` |
| `wechat.base_url` | `ilinkwire.DefaultBaseURL` |
| `wechat.channel_version` | `ilinkwire.DefaultChannelVersion` |
| `wechat.render` | `plain` |
| `wechat.approvers` | `["owner"]` |
| `workspace.roots` | 空；`ferry init` 必须询问 |
| `agents` | 4 个注册表预设：`qwen-code`、`kimi`、`codebuddy-code`、`opencode`（name 与 registry 同名） |
| `policy.default` | `ask` |
| `policy.rules` | 空；为空时使用 3.7 的内置规则 |
| `policy.approval_timeout` | `10m` |
| `outbox.merge_interval` | `30s` |
| `outbox.reply_budget` | `8` |
| `outbox.long_output_threshold` | `1500` |
| `outbox.send_timeout` | `15s` |
| `registry.snapshot_only` | `true` |
| `registry.npm_registry`、`registry.github_proxy` | 空 |
| `limits.max_managed_sessions` | `3` |

校验规则（`Validate()`）：`lang` 只能是 `zh-CN` 或 `en`；agent 名唯一，且匹配 `^[a-z0-9][a-z0-9-]{0,31}$`；每个 agent 恰好设置 `registry` 与 `command` 之一；`default_agent` 必须在 `agents` 里；`action` 与 `default` 只能是 allow、ask、deny；`command_pattern` 能编译；`reply_budget` 不小于 2；`merge_interval` 不小于 100ms；`workspace.roots` 至少一项，且每项都是已存在的目录。

配置样例（T4 放到 `internal/config/testdata/sample.toml`，TΩ 写进文档）：

```toml
lang = "zh-CN"
default_agent = "kimi"

[wechat]
approvers = ["owner"]

[workspace]
roots = ["~/code"]

[[agents]]
name = "kimi"
registry = "kimi"

[[agents]]
name = "qwen-code"
registry = "qwen-code"
env = { OPENAI_BASE_URL = "https://example.invalid/v1" }

[[agents]]
name = "my-agent"
command = ["/usr/local/bin/my-agent", "--acp"]

[policy]
approval_timeout = "10m"

[[policy.rules]]
kind = ["execute"]
command_pattern = "^(go test|npm test)"
action = "allow"

[outbox]
merge_interval = "30s"
reply_budget = 8

[registry]
npm_registry = "https://registry.npmmirror.com"
```

### 3.4 存储结构 `internal/store/migrations/001_init.sql`（T0 写）

时间一律存 Unix 毫秒。连接参数：`journal_mode=WAL`、`busy_timeout=5000`、`foreign_keys=ON`。驱动为 modernc.org/sqlite（驱动名 `sqlite`）。

```sql
CREATE TABLE schema_version (version INTEGER NOT NULL);

CREATE TABLE sessions (
  id               TEXT PRIMARY KEY,
  driver           TEXT NOT NULL,
  agent            TEXT NOT NULL,
  cwd              TEXT NOT NULL,
  agent_session_id TEXT NOT NULL DEFAULT '',
  origin           TEXT NOT NULL,
  state            TEXT NOT NULL,
  owner            TEXT NOT NULL,          -- EndpointID.String()
  created_at       INTEGER NOT NULL,
  last_active      INTEGER NOT NULL
);

CREATE TABLE bindings (
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  slot       INTEGER NOT NULL,
  foreground INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (endpoint, session_id),
  UNIQUE (endpoint, slot)
);

CREATE TABLE dedupe (
  endpoint   TEXT NOT NULL,
  message_id TEXT NOT NULL,
  seen_at    INTEGER NOT NULL,
  PRIMARY KEY (endpoint, message_id)
);

CREATE TABLE outbox (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,
  body       TEXT NOT NULL,
  file_path  TEXT NOT NULL DEFAULT '',
  dedupe_key TEXT NOT NULL,
  urgent     INTEGER NOT NULL,
  state      TEXT NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX outbox_endpoint_state ON outbox(endpoint, state);

CREATE TABLE windows (
  endpoint  TEXT PRIMARY KEY,
  reply_ctx TEXT NOT NULL,
  opened_at INTEGER NOT NULL,
  used      INTEGER NOT NULL
);

CREATE TABLE approvals (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  request    TEXT NOT NULL,              -- ApprovalRequest 的 JSON
  decision   TEXT NOT NULL DEFAULT '',
  decided_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  decided_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE acl (
  endpoint  TEXT NOT NULL,
  sender_id TEXT NOT NULL,
  role      TEXT NOT NULL,
  PRIMARY KEY (endpoint, sender_id)
);

CREATE TABLE pairing (
  code       TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL,
  sender_id  TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE audit (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  endpoint   TEXT NOT NULL DEFAULT '',
  sender_id  TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
```

约定的 KV 键：`wechat:<bot>:cursor`（长轮询游标）、`wechat:<bot>:typing:<user>`（typing_ticket 与获取时间的 JSON）、`lang:<endpoint>`（该入口的机器人语言）。

### 3.5 命令集

只有整条消息匹配才算命令；命令名不区分大小写；中文别名同样要求整条消息匹配（「停一下」不是命令）。

| 输入 | Name | Args | 说明 |
| --- | --- | --- | --- |
| `/ls`、`会话` | ls | 无 | 列出本入口的会话 |
| `/new <agent> [目录]`、`新建 <agent> [目录]` | new | agent，目录可省略 | 目录省略时用第一个工作区根目录 |
| `/use <n>` | use | n | 切换前台会话；已停放的会话先恢复 |
| `/agents` | agents | 无 | 列出 agent 及可用状态 |
| `/stop`、`停` | stop | 无 | 取消前台会话的当前轮次 |
| `/close [n]`、`关闭 [n]` | close | n 可省略 | 省略时关闭前台会话 |
| `/mode <项> <值>` | mode | 项、值 | 调用 `SetConfig` |
| `/pull`、`收件` | pull | 无 | 本轮回复「后续版本提供」 |
| `/file <n>` | file | n | 本轮回复「后续版本提供」 |
| `/lang <zh 或 en>` | lang | zh 或 en | 切换该入口的机器人语言 |
| `/help`、`帮助` | help | 无 | 命令说明 |
| `/pair <code>` | pair | 6 位数字 | 仅审批人可用，确认配对 |

参数格式不对（如 `/use abc`）时 `Parse` 仍返回该命令，由核心回复用法提示。以 `//` 开头的消息不是命令。

### 3.6 核心交互规则（T5 实现）

**入站处理顺序**（每条 `fsp.Inbound`）：

1. `Store.SeenMessage` 为 true → 丢弃。
2. `ACL.Allowed` 为 false → `ACL.OfferPairing`，回复 `pair.code`（额度满时回复 `pair.busy`）；记审计 `rejected`；结束。陌生人发的任何内容都不进 agent，也不能自行配对。
3. `Outbox.OpenWindow(ep, ReplyCtx, ReceivedAt)`，然后 `Outbox.FlushHeld(ep)`。
4. `Approvals.ParseVerdict` 命中 → 发送者是审批人才调用 `Resolve`，否则忽略；结束。
5. `Commands.Unescape` 返回 true → 用新文本跳到第 7 步。
6. `Commands.Parse` 命中 → 执行命令（3.5），结束。
7. 文本为空且带附件 → 回复 `media.unsupported`，结束。
8. 取前台会话；没有 → 回复 `hint.no_session`，结束。
9. 生成 prompt：发送者是 owner 时用原文；否则用 `[from <sender_id>] ` 加原文。
10. 会话空闲 → `Prompt`，回复 `turn.accepted`，并调用 `IM.Typing(on)`；会话执行中或等待审批 → 追加到该会话的待发队列，不回复。
11. 记审计 `inbound`（只记长度）。

**会话事件处理**（每个会话一个 goroutine）：

- `EvChunk`：追加到本轮缓冲。
- `EvThought`：丢弃。
- `EvTool`：某个 CallID 第一次出现时，向缓冲追加一行 `▸ <title>`。
- `EvPlan`：向缓冲追加 `plan.header` 与编号步骤。
- 缓冲非空时，每隔 `outbox.merge_interval` 以非紧急消息入队一次并清空。
- `EvTurnEnd`：剩余缓冲作为紧急消息入队；stopReason 为 cancelled 时再入队 `turn.stopped`；调用 `IM.Typing(off)`；待发队列非空时，以空行连接全部内容，立即 `Prompt` 一次并清空队列。
- `EvError`：入队 `turn.failed`。
- 事件通道关闭：状态改为 parked，`Approvals.CancelSession`，入队 `session.parked`，记审计。

**核心传给驱动的 Approver**：给 `req.Session` 填上 Ferry 会话 ID，状态置为 awaiting，调用 `Approvals.Ask`，返回后状态恢复为 running。

**命令执行**：

- `new`：`Registry.Resolve`；目录经 `~` 展开、转绝对路径、解析符号链接后，必须等于某个工作区根目录或位于其下，否则回复 `workspace.denied`；运行中与空闲的托管会话数达到上限时回复 `session.limit`；调用 `Driver.Start`（P1 只有 acp 驱动）；保存会话，`Bind`，设为前台，回复 `session.created`。
- `use`：找到对应 slot；会话已停放时先 `Driver.Resume`，返回 `fsp.ErrUnsupported` 时新建一个同 agent、同目录的会话并回复 `session.resume_unsupported`；设为前台，回复 `session.switched`。
- `stop`：`AgentSession.Cancel` 与 `Approvals.CancelSession`。
- `close`：`AgentSession.Close`，状态改为 closed，`Unbind`，回复 `session.closed`。
- `ls`：回复 `session.list_header`，每个会话一行 `session.list_item`，前台会话标记 `▶`。
- `agents`：每个 agent 一行 `agents.list_item`。
- `mode`：`SetConfig`，回复 `mode.updated` 或 `mode.unsupported`。
- `lang`：写 KV `lang:<endpoint>`，用新语言回复 `lang.switched`。
- `pair`：发送者不是审批人时忽略；`ConfirmPairing` 成功时，给 owner 回复 `pair.confirmed_owner`，给对方回复 `pair.confirmed`；失败回复 `pair.invalid`。
- `pull`、`file`：回复 `feature.coming`。
- `help`：回复 `help.text`。

**启动与退出**：启动时把所有非 closed 会话置为 parked（进程已不在），然后从 `IM.Status()` 取 BotID 与 OwnerID 调用 `ACL.SetOwner`。退出时关闭所有会话（置为 parked），不关 closed 的会话记录。

**语言**：入口语言取 KV `lang:<endpoint>`，没有则用 `config.Lang`。

### 3.7 策略引擎（T3 实现）

`policy.rules` 为空时使用内置规则。规则按顺序匹配，第一条命中即生效；都不命中时用 `policy.default`。

| 顺序 | kind | 路径条件 | 动作 |
| --- | --- | --- | --- |
| 1 | read、search、think | 不限 | allow |
| 2 | delete、move、edit | 任一路径在工作区外 | deny |
| 3 | 其他 | 其他 | 落到 default（ask） |

匹配细节：

- `kind` 为空表示任意；未知的 ToolKind 按 `other` 处理。
- `outside_workspace = true`：请求里至少一个路径在工作区外才命中；请求没有路径时不命中。`false`：全部路径都在工作区内才命中。
- `command_pattern`：对 `InputPreview` 做正则匹配。
- 路径判断前先转绝对路径并解析符号链接；解析失败按「在工作区外」处理。

动作到选项的映射：

- allow：选第一个 allow_once 选项；没有时选 allow_always；都没有时按 ask 处理。
- deny：选第一个 reject_once 选项；没有时选 reject_always；都没有时返回空 OptionID（取消）。
- ask：分配 ID，保存，向会话 owner 入队紧急审批消息，等待裁决、超时或 ctx 取消。

人工裁决的映射：`y` 选 allow_once；`a` 选 allow_always，会话没有该选项时回复 `approval.option_unavailable` 并继续等待；`n` 选 reject_once，没有时选 reject_always。超时按 `n` 处理，并回复 `approval.timeout`。ctx 取消时返回空 OptionID。

裁决正则：`(?i)^\s*(y|yes|a|always|n|no)\s+([a-km-z]{5})\s*$`，匹配后 ID 转小写。ID 字母表 `abcdefghijkmnopqrstuvwxyz`（去掉 l），用 crypto/rand 生成，与未决审批重复时重新生成。

审批消息 `approval.request` 的参数：ID；Tool 为 ToolName；Description 为 `SanitizeForIM(Description, 300)`；Preview 为 `MaskSecrets(SanitizeForIM(InputPreview, 800))`；有 allow_always 选项时带上 `approval.always_hint`。

### 3.8 注册表解析（T2 实现）

- **快照**：`internal/registry/snapshot.json` 是官方注册表 `https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json` 的子集，格式相同（`version` 加 `agents` 数组，字段见 ACP Registry 仓库的 FORMAT.md），只保留 qwen-code、kimi、codebuddy-code、minimax-code、qoder、opencode、glm-acp-agent、claude-acp、codex-acp、gemini 十项。生成工具放 `internal/registry/cmd/snapshot`，由人手动运行；测试只读已提交的快照。
- **分发方式的选择顺序**：binary 且 PATH 里已有同名命令（取 `cmd` 的文件名，如 `kimi`、`opencode`）→ npx → uvx → 下载 binary。
- **npx**：命令为 `npx -y <package> <args...>`，`package` 按注册表原样（已含版本号）。配置了 `npm_registry` 时，环境变量加 `npm_config_registry=<值>`。
- **uvx**：命令为 `uvx <package> <args...>`。
- **下载 binary**：平台键为 `<os>-<arch>`，os 取 darwin、linux、windows，arch 把 amd64 映射为 x86_64、arm64 映射为 aarch64。下载到 `<home>/agents/<id>/<version>/`，校验 sha256，解压 tar.gz 或 zip；配置了 `github_proxy` 时把它作为 archive URL 的前缀。
- **环境变量白名单**：PATH、HOME、USERPROFILE、APPDATA、LOCALAPPDATA、TMPDIR、TEMP、TMP、LANG、LC_ALL、SHELL、TERM、HTTP_PROXY、HTTPS_PROXY、NO_PROXY（及其小写形式），加上注册表条目的 `env` 与配置里的 `env`。以 `FERRY_` 开头的变量一律不透传。
- **可用性**（`Agents`）：`command` 类检查可执行文件存在；registry 类按上面的顺序检查 PATH、npx、uvx，需要下载的视为可用但在 Reason 里注明「首次使用时下载」。不可用时写明原因，如「未找到 npx，请先安装 Node.js」。

### 3.9 i18n 键（T0 一次加齐，中英两份）

模板参数用 `{{.Name}}`。下表给出中文；英文由 T0 意译。

| 键 | 中文 |
| --- | --- |
| `hint.no_session` | 还没有会话。发送 /new <agent> <目录> 新建，例如 /new kimi ~/code/demo |
| `turn.accepted` | 收到，已交给 {{.Agent}}（会话 {{.Slot}}） |
| `turn.stopped` | 已停止 |
| `turn.failed` | {{.Agent}} 出错：{{.Reason}} |
| `plan.header` | 计划： |
| `session.created` | 已新建会话 {{.Slot}}：{{.Agent}} · {{.Cwd}} |
| `session.list_header` | 会话列表（▶ 为当前会话）： |
| `session.list_item` | {{.Mark}} {{.Slot}}. {{.Agent}} · {{.Cwd}} · {{.State}} |
| `session.state.idle` / `running` / `awaiting` / `parked` / `starting` | 空闲 / 执行中 / 等待审批 / 已停放 / 启动中 |
| `session.switched` | 已切换到会话 {{.Slot}} |
| `session.closed` | 已关闭会话 {{.Slot}} |
| `session.parked` | 会话 {{.Slot}} 已停放，发送 /use {{.Slot}} 可恢复 |
| `session.resume_unsupported` | {{.Agent}} 不支持恢复旧会话，已在原目录新建会话 {{.Slot}} |
| `session.limit` | 同时运行的会话已达上限（{{.Max}} 个），请先用 /close 关闭一个 |
| `session.start_failed` | 启动 {{.Agent}} 失败：{{.Reason}} |
| `session.not_found` | 没有编号为 {{.Slot}} 的会话，发送 /ls 查看 |
| `workspace.denied` | 目录不在允许范围内：{{.Dir}} |
| `agent.unknown` | 没有名为 {{.Name}} 的 agent，发送 /agents 查看 |
| `agents.list_header` | 可用的 agent： |
| `agents.list_item` | {{.Name}}（{{.Display}}）{{.Status}} |
| `agents.available` / `agents.unavailable` | 可用 / 不可用：{{.Reason}} |
| `approval.request` | 审批 {{.ID}} · {{.Tool}}（换行）{{.Description}}（换行）{{.Preview}}（换行）回复 y {{.ID}} 允许一次{{.AlwaysHint}}，回复 n {{.ID}} 拒绝（换行）预览可能包含敏感信息 |
| `approval.always_hint` | ，回复 a {{.ID}} 总是允许 |
| `approval.timeout` | 审批 {{.ID}} 超时，已按拒绝处理 |
| `approval.option_unavailable` | 该 agent 不支持这个选项，请回复 y 或 n |
| `approval.decided` | 审批 {{.ID}} 已处理：{{.Decision}} |
| `pair.code` | 你还没有使用权限。请把配对码 {{.Code}} 发给机器人的主人，1 小时内有效 |
| `pair.busy` | 待确认的配对请求太多，请稍后再试 |
| `pair.confirmed` | 配对成功，现在可以使用了 |
| `pair.confirmed_owner` | 已允许 {{.Sender}} 使用 |
| `pair.invalid` | 配对码无效或已过期 |
| `mode.updated` | 已设置 {{.Key}} = {{.Value}} |
| `mode.unsupported` | 当前 agent 不支持这个配置项 |
| `lang.switched` | 已切换为中文 |
| `media.unsupported` | 暂不支持图片、语音和文件，后续版本提供 |
| `feature.coming` | 该功能将在后续版本提供 |
| `usage.use` / `usage.new` / `usage.mode` / `usage.lang` / `usage.pair` | 对应命令的用法说明各一句 |
| `outbox.truncated` | ……（其余 {{.N}} 字未发送） |
| `help.text` | 多行命令说明，覆盖 3.5 的全部命令与审批回复格式 |
| `cli.scan_qr` | 请用微信扫描下面的二维码 |
| `cli.login_ok` | 登录成功，bot：{{.Bot}}，主人：{{.Owner}} |
| `cli.need_init` | 还没有配置，请先运行 ferry init |
| `cli.daemon_running` | 守护进程已在运行（PID {{.PID}}） |
| `cli.offline` | 微信连接已断开，请运行 ferry login wechat 重新登录 |

说明：「（换行）」在 toml 里写成 `\n`。

### 3.10 失败面

| 场景 | 行为 |
| --- | --- |
| `getupdates` 客户端超时（服务端建议值加 5 秒，缺省 40 秒） | 视为空结果，立即下一轮 |
| iLink 网络错误或 HTTP 5xx | 指数退避 1、2、4 … 60 秒，成功后归零 |
| iLink 返回 -14 | 适配器置离线，`Run` 返回 `fsp.ErrSessionExpired`；核心记审计 `expired`，终端日志提示重新登录，每 60 秒重试 |
| `sendmessage` 超时（`outbox.send_timeout`） | 结果为 unknown；发件箱用同一个 client_id 重试，间隔 2、4、8 秒，最多 3 次；仍为 unknown 则记审计 |
| `sendmessage` 返回其他错误码 | 结果为 rejected，记审计 `outbound_failed`，不重试 |
| ACP `initialize` 或 `session/new` 超过 30 秒 | 启动失败；终止子进程；回复 `session.start_failed` |
| agent 进程意外退出 | 发 EvError 后关闭事件通道；核心把会话置为 parked，未决审批以取消结束 |
| 审批超时（默认 10 分钟） | 按拒绝处理并回复 `approval.timeout` |
| `/new` 目录不在白名单 | 回复 `workspace.denied`（`fsp.ErrWorkspaceDenied`） |
| 运行中的托管会话达到上限 | 回复 `session.limit` |
| 配对码 | 6 位数字，1 小时过期；同一入口最多 3 个待确认，超出回复 `pair.busy` |
| 同一 message_id 重复到达 | 丢弃；去重记录保留 24 小时 |
| 数据库打不开 | 守护进程以退出码 2 退出，打印路径与原因 |
| 守护进程重复启动 | 第二个进程打印 `cli.daemon_running`，退出码 1 |

### 3.11 文件归属表

| 路径 | owner | 其他任务 |
| --- | --- | --- |
| `internal/ports/**`、`internal/config/types.go`、`internal/store/migrations/**`、`locales/*.toml`、`go.mod`、`go.sum`、`internal/deps/**`、`.gitignore` | T0 | 只读，改动即任务失败 |
| `internal/im/wechat/**` | T1 | 不可改 |
| `internal/driver/acp/**`、`internal/registry/**` | T2 | 不可改 |
| `internal/store/**`（migrations 除外）、`internal/acl/**`、`internal/approval/**`、`internal/security/**` | T3 | 不可改 |
| `internal/outbox/**`、`internal/command/**`、`internal/config/**`（types.go 除外） | T4 | 不可改 |
| `internal/core/**` | T5 | 不可改 |
| `cmd/ferry/**`、`test/e2e/**`、`README.md`、`README.zh-CN.md`、`docs/en/quickstart.md`、`docs/zh-CN/quickstart.md`、`docs/en/wechat.md`、`docs/zh-CN/wechat.md` | TΩ | 不可改 |
| P0 的其他产物（fsp、ilinkwire、clock、ids、testkit、i18n、scripts、.github、Makefile） | 无 | 只读；发现问题报告 |
| `docs/dev/**`、`LICENSE` | 人工 | 只读 |

## 4. 基线

- T0 起点：`main` 上的 `p0-done`，工作区干净。
- 并行基线：标签 `p1-base`，T0 完成且人工复核后打。

## 5. 任务表

| 任务 | 目标 | 时序 |
| --- | --- | --- |
| T0 | 接口、配置类型、存储结构、i18n 键、占位构造函数 | 先跑；打 `p1-base` 后其余才起 |
| T1 | 微信 iLink 适配器与扫码登录 | 并行 |
| T2 | ACP 驱动与注册表解析 | 并行 |
| T3 | 存储、权限与配对、审批与策略、凭据与净化 | 并行 |
| T4 | 发件箱 v1、命令解析、配置加载 | 并行 |
| T5 | 核心：路由、会话管理、命令执行 | 并行 |
| TΩ | 合流、命令行装配、端到端测试、使用文档 | 最后 |

本轮 TΩ 除合流外，还负责 `cmd/ferry` 装配和端到端测试：它们必须等全部模块合入后才能写，放进任何并行任务都会依赖别人的产出。

### 5.1 T0 细则

- 按 3.1 写 `internal/ports`；按 3.3 写 `internal/config/types.go`；按 3.4 写迁移文件；按 3.9 给两份 locale 加齐键。
- 在 3.2 列出的各包里写出签名，函数体为 `panic("not implemented")`；`Adapter`、`Daemon` 等类型只声明，字段留空。
- `.gitignore` 加入 `dist/`、`*.db`、`.ferry/`。
- 本轮不新增依赖。

### 5.2 T1 细则：微信适配器

- **登录**：`Start` 请求 `get_bot_qrcode?bot_type=3`，返回 `qrcode_img_content`。`Wait` 每 2 秒（经时钟）查询状态：`scaned` 记日志；`scaned_but_redirect` 改用 `https://<redirect_host>` 继续查询；`expired` 重新取码并调用 `onQR`，最多 3 次；`confirmed` 返回凭据（BotID 取 `ilink_bot_id`，OwnerID 取 `ilink_user_id`，BaseURL 优先取 `baseurl`）。
- **请求**：每个 POST 带 `AuthorizationType`、`X-WECHAT-UIN`、`Authorization` 三个请求头，请求体带 `base_info.channel_version`。
- **收消息**：游标从 KV 读取；HTTP 超时为服务端建议值加 5 秒；每批消息写入 `out` 之后再保存新游标（至少一次投递，重复由核心去重）。跳过 `message_type=2` 与 `group_id` 非空的消息。文本为全部 text_item 以换行连接；voice_item 带转写文字时并入文本，同时加一个 voice 附件；图片、文件、视频只生成附件（不下载）。MessageID 取 `message_id`，为 0 时取 `client_id`。ReceivedAt 取 `create_time_ms`。
- **发消息**：DedupeKey 为空时返回错误；构造 `message_type=2`、`message_state=2`、单个 text_item 的消息，带 `context_token` 与 `client_id`。`render=plain` 时先转纯文本：标题改为【标题】，去掉粗体斜体与行内代码标记，代码块去掉围栏并每行缩进两格，链接改为「文字 (网址)」，表格每行用「 · 」连接单元格。超时返回 unknown；-14 返回 rejected 与 `fsp.ErrSessionExpired` 并置离线；其他错误码返回 rejected。Kind 为 file 时返回 `fsp.ErrUnsupported`。
- **正在输入**：`getconfig` 取 `typing_ticket` 并按用户缓存 24 小时（KV），然后 `sendtyping`。
- 测试用 P0 的假 iLink 服务，至少包含：TestLoginConfirm、TestLoginRedirect、TestLoginRefreshExpired、TestPollDeliversText、TestPollSkipsBotAndGroup、TestCursorPersisted、TestSendUsesContextTokenAndClientID、TestSendTimeoutIsUnknown、TestSessionExpired、TestTypingTicketCached、TestRenderPlain、TestBackoff。

### 5.3 T2 细则：ACP 驱动与注册表

- **启动**：按 SessionSpec 启动子进程（工作目录为 Cwd，环境为 Env）；`initialize` 不声明 fs 与 terminal 能力；`session/new` 不带 MCP server；两步合计 30 秒超时。
- **事件映射**：agent_message_chunk → EvChunk；agent_thought_chunk → EvThought；tool_call 与 tool_call_update → EvTool；plan → EvPlan；`session/prompt` 返回 → EvTurnEnd（带 stopReason）。
- **权限**：把 `session/request_permission` 转成 `fsp.ApprovalRequest`：ToolName 与 Description 取 toolCall 标题，ToolKind 取 kind，InputPreview 取 rawInput 的 JSON（最长 2000 字符），Paths 取 locations，Options 逐项映射。调用 Approver；OptionID 为空时回复 cancelled。
- **取消**：`Cancel` 发 `session/cancel`；进行中的 Approver 调用通过 ctx 取消。
- **恢复**：agent 声明 loadSession 时用 `session/load`，加载期间的历史重放不转成事件；否则返回 `fsp.ErrUnsupported`。
- **列表**：agent 支持会话列表时实现 `List`，否则返回 `fsp.ErrUnsupported`。
- **配置**：agent 提供会话配置项时用对应方法设置；key 为 mode 时设置会话模式；都不支持时返回 `fsp.ErrUnsupported`。
- **进程**：stderr 以 debug 级别记日志（单行截断 500 字符）；进程退出时发 EvError 并关闭事件通道；`Close` 先发取消，2 秒后强制结束进程。
- **注册表**：按 3.8 实现，含快照生成工具。下载测试用 httptest 提供 tar.gz 与 zip 归档并校验 sha256。
- 测试至少包含：acp 包的 TestStartAndEcho、TestStreamingChunks、TestPermissionRoundTrip、TestPermissionCancelledOnStop、TestBusy、TestProcessExitEmitsError、TestResumeViaLoad、TestListSessions、TestInitializeTimeout；registry 包的 TestSnapshotParses、TestResolveCommand、TestResolveNpx、TestNpmMirrorEnv、TestResolveBinaryFromPath、TestResolveBinaryDownload、TestChecksumMismatch、TestGitHubProxyPrefix、TestEnvWhitelist、TestAgentsAvailability。

### 5.4 T3 细则：存储、权限、审批、安全

- **store**：按 3.1 与 3.4 实现；用 embed 读取迁移文件；迁移可重复执行；`SeenMessage` 顺带清理 24 小时前的记录。
- **acl**：`SetOwner` 给 `(wechat, bot, owner)` 入口的 owner 记角色 owner。`Allowed`：有任何角色即放行。`IsApprover`：角色为 owner 且配置含 `"owner"`，或角色为 approver，或 senderID 在配置的 approvers 列表里。配对码为 6 位随机数字，1 小时过期，每个入口最多 3 个待确认。`ConfirmPairing` 成功后给该发送者记角色 member。
- **approval**：按 3.7 实现，审批与裁决都写入 store 并记审计。未决审批放在内存里，Resolve 与超时并发安全。
- **security**：
  - `MaskSecrets` 覆盖 `sk-` 开头的密钥、`ghp_` 与 `github_pat_` 令牌、`xox` 开头的 Slack 令牌、`AKIA` 开头的访问密钥、PEM 私钥块，以及 password、secret、token、api_key 等键值对的值；保留前 4 个字符，其余替换为 `****`。
  - `SanitizeForIM` 去掉除换行和制表符外的控制字符，去掉 U+202A–U+202E 与 U+2066–U+2069，把连续 3 个以上空行压成 2 个；超过 max 个字符时保留开头 70% 与结尾 20%，中间写 `…[+N]…`。
  - 凭据先存系统钥匙串（service `ferry`，user 为平台名，值为 JSON）；钥匙串不可用时写 `<home>/credentials/<平台>.json`，目录权限 0700、文件 0600。
- 测试至少包含：TestMigrateIdempotent、TestSessionsCRUD、TestBindingsSlots、TestSeenMessage、TestOutboxRows、TestWindow、TestPairingExpiry、TestKV；TestOwnerIsApprover、TestApproverList、TestPairingLimit、TestConfirmPairing；TestParseVerdict、TestIDAlphabet、TestPolicyDefaults、TestPolicyCustomRules、TestSymlinkOutside、TestAskAndResolve、TestTimeoutRejects、TestUnknownIDIgnored、TestAlwaysUnavailable、TestCancelSession；TestMaskSecrets、TestSanitizeForIM、TestCredentialsKeyring、TestCredentialsFileFallback（钥匙串用 go-keyring 自带的 mock）。

### 5.5 T4 细则：发件箱、命令、配置

- **发件箱**：
  - `Enqueue` 写库（queued）并唤醒发送循环；循环还每秒检查一次。
  - 没有窗口的入口：消息置为 rejected，记审计。
  - 额度 = `reply_budget` 减去窗口已用数。紧急消息只要额度不小于 1 就立即发送；非紧急消息必须保留 1 个额度给紧急消息。
  - 非紧急消息按「入口 + 会话」合并，最早一条等满 `merge_interval` 后一起发；若有紧急消息要发，先把同会话的非紧急内容并入紧急消息开头。
  - 额度不足时：非紧急消息留在队列，等下一条紧急消息合并；紧急消息置为 held（P2 负责补投）。
  - 超过 `long_output_threshold` 的文本按段落或行切分；段数超过剩余额度时，最后一段改为截断文本加 `outbox.truncated`。
  - DedupeKey 为空时生成 `ob-<行ID>-<段号>`；unknown 按 3.10 用同一个键重试；成功后窗口已用数加 1 并保存。
  - `FlushHeld` 本轮为空操作。
- **命令**：按 3.5 实现 `Parse` 与 `Unescape`，表驱动测试覆盖全部命令、中文别名、大小写、多余空格与错误参数。
- **配置**：按 3.3 实现默认值、加载、保存、校验、`~` 展开与 Duration 编解码。
- 测试至少包含：TestUrgentImmediate、TestMergeInterval、TestReplyBudgetReserve、TestHeldWhenExhausted、TestSplitLongText、TestRetryUnknownSameKey、TestNoWindowRejected、TestRowsPersisted（用本包内的假 IM 与假时钟）；TestParseTable、TestUnescape、TestAliasWholeMessageOnly；TestDefaultValid、TestLoadSample、TestValidateErrors、TestTildeExpansion、TestDurationTOML、TestSavePermissions。

### 5.6 T5 细则：核心

- 按 3.6 实现。依赖全部通过 `ports` 接口使用，测试里的替身写在本包 `_test.go` 中。
- 测试至少包含：TestDedupe、TestStrangerGetsPairingCode、TestVerdictOnlyFromApprover、TestEscapedSlash、TestNoSessionHint、TestNewAndEcho、TestMediaUnsupported、TestOwnerTextNotPrefixed、TestMemberTextPrefixed、TestBusyMerge、TestStopCancels、TestToolLineOnce、TestParkOnExit、TestUseResumes、TestUseFallsBackWhenUnsupported、TestWorkspaceDenied、TestSymlinkEscapeDenied、TestSessionLimit、TestLangSwitch、TestPairCommand、TestRestartParksAll、TestIMExpiredRetries。

### 5.7 TΩ 细则

- 合并顺序：T3 → T4 → T1 → T2 → T5。
- `cmd/ferry` 子命令：
  - `init`：交互询问语言、工作区根目录、默认 agent（列出 `Registry.Agents` 中可用的），写配置；支持 `--lang`、`--root`（可多次）、`--agent`、`--yes` 非交互运行。
  - `login wechat`：用 qrterminal 在终端画二维码，成功后保存凭据并打印 bot 与 owner。`logout wechat` 删除凭据。
  - `daemon`：用 `<home>/ferry.lock`（内容为 PID，发现进程已不存在时视为陈旧锁）保证单实例；按 config → store → 凭据 → 微信适配器 → 发件箱 → acl → 审批 → 注册表 → 驱动 → 命令 → i18n → 核心的顺序装配；收到 SIGINT 或 SIGTERM 时优雅退出；每 5 秒把 `core.Status` 写入 `<home>/status.json`；日志写 `<home>/logs/ferry.log`（10MB 轮转，保留 3 份），前台运行时同时输出到 stderr。
  - `pair <code>`：直接用 acl 确认配对。
  - `ls`：从数据库列出全部会话。
  - `status`：读取 `status.json` 与凭据是否存在；状态文件超过 30 秒未更新时显示守护进程未运行。
  - `doctor`：检查配置、工作区目录、凭据、数据库、每个 agent 的可用性，以及 npx 与 uvx 是否存在。
- `test/e2e`：写一个测试夹具，启动假 iLink 服务、构建 `ferry-fakeagent`、生成临时配置，在进程内装配真实模块运行核心，并提供「推送一条文本」与「等待发送记录满足条件」的辅助函数；实现 B1–B11。
- 文档：`docs/en|zh-CN/quickstart.md` 写安装、初始化、登录、启动、第一个会话；`docs/en|zh-CN/wechat.md` 写命令表、审批、配对、回复额度说明；README 两个版本补一段最短上手步骤。保持 check-i18n 通过。

## 6. 每个任务的验收

| 任务 | 必须通过（命令见各自提示词） |
| --- | --- |
| T0 | G1–G3，C1，check-i18n，版本命令，迁移文件存在，接口包可文档化，共 8 项 |
| T1 | 微信包测试（含 5.2 列出的测试名）、重复 3 次稳定、G1–G3，共 5 项 |
| T2 | acp 与 registry 测试、acp 重复 3 次稳定、G1–G3，共 5 项 |
| T3 | store 与 acl 测试、approval 与 security 测试、G1–G3，共 5 项 |
| T4 | outbox 测试、command 与 config 测试、G1–G3，共 5 项 |
| T5 | core 测试、重复 3 次稳定、G1–G3，共 5 项 |
| TΩ | A1–A3、B1–B12、C1–C2，共 17 项 |

## 7. 卡住了怎么办

见 00 总览 3.8。

## 8. 派工提示词

### P1-T0

````
# 任务 P1-T0 — 接口、配置类型、存储结构与 i18n 键

## 工作区
直接在 main 分支工作（当前应为 p0-done）。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3 节全部、第 5.1 节

## 目标
把 spec-p1 第 3 节的契约写成代码，并在各包放好带正确签名的占位实现，使全仓在「空实现」状态下能编译。

## 具体要做
1. 按 3.1 写 internal/ports。
2. 按 3.3 写 internal/config/types.go（只写类型，不写 Default/Load 等函数的实现，它们放占位）。
3. 按 3.4 写 internal/store/migrations/001_init.sql。
4. 按 3.9 在 locales/zh-CN.toml 与 locales/en.toml 加齐全部键，英文为意译。
5. 按 3.2 在 wechat、acp、registry、store、acl、approval、security、outbox、command、config、core 各包写出签名，函数体为 panic("not implemented")。
6. .gitignore 加入 dist/、*.db、.ferry/。

## 不要做的事
不实现任何业务逻辑。不新增依赖。

## 可写文件
spec-p1 第 3.11 节中 owner 为 T0 的路径，以及 3.2 所列各包里只含签名的占位文件。

## 禁改
- docs/dev/**、LICENSE
- P0 的产物（fsp、ilinkwire、clock、ids、testkit、i18n 包代码、scripts、.github、Makefile）

## 验收（8 项）
1. `go build ./...` → 退出码 0
2. `go vet ./...` → 无输出
3. `test -z "$(gofmt -l .)"` → 退出码 0
4. `go test ./internal/testkit/... ./internal/i18n/... ./scripts/...` → 全部 ok
5. `go run ./scripts/check-i18n` → `i18n ok`
6. `go run ./cmd/ferry version` → 以 `ferry ` 开头
7. `test -f internal/store/migrations/001_init.sql` → 退出码 0
8. `go doc ./internal/ports | grep -c 'type .* interface'` → 不少于 12

## 完成后
git add -A && git commit -m "p1-t0: freeze p1 contracts"

回执正文附 `git show --stat HEAD`。最后一行严格为：
RECEIPT P1-T0 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P1-T1

````
# 任务 P1-T1 — 微信 iLink 适配器与扫码登录

## 工作区
git fetch --tags
git worktree add -b p1-t1 ../ferry-p1-t1 p1-base
cd ../ferry-p1-t1

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.2、3.4 节（线上格式与假 iLink 服务）
- docs/dev/spec-p1.md 第 3.1、3.2、3.3、3.10 节，第 5.2 节

## 目标
实现 internal/im/wechat：扫码登录、长轮询收消息、带去重键发消息、正在输入、会话过期处理与纯文本渲染，全部用 P0 的假 iLink 服务测试。

## 可写文件（只有这些）
- internal/im/wechat/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/fsp/**、internal/ilinkwire/**、internal/testkit/**、go.mod、go.sum
- 白名单以外的任何文件

契约不对就停下来报告，不要自己改。

## 验收（5 项）
1. `go test ./internal/im/wechat/... -v` → 全部 PASS，且包含 spec-p1 第 5.2 节列出的全部测试名
2. `go test ./internal/im/wechat/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p1-t1: wechat ilink adapter"

回执正文：每条验收命令的真实输出关键行（第 1 项列出实际跑过的测试名）；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P1-T1 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P1-T2

````
# 任务 P1-T2 — ACP 驱动与注册表解析

## 工作区
git fetch --tags
git worktree add -b p1-t2 ../ferry-p1-t2 p1-base
cd ../ferry-p1-t2

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.5 节（假 ACP agent 的行为）
- docs/dev/spec-p1.md 第 3.1、3.2、3.8、3.10 节，第 5.3 节

## 目标
实现 internal/driver/acp（用 acp-go-sdk v0.13.5 驱动任意 ACP agent）与 internal/registry（从注册表快照解析国产 agent 预设的启动方式，支持 npm 镜像与 GitHub 下载代理）。

## 可写文件（只有这些）
- internal/driver/acp/**
- internal/registry/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/fsp/**、internal/testkit/**、go.mod、go.sum
- 白名单以外的任何文件

生成快照需要联网：本机能访问 cdn.agentclientprotocol.com 时运行生成工具并提交结果；不能访问时，按 spec-p1 3.8 的格式手写这 10 项（命令与参数照注册表），并在回执里注明。

## 验收（5 项）
1. `go test ./internal/driver/acp/... ./internal/registry/... -v` → 全部 PASS，且包含 spec-p1 第 5.3 节列出的全部测试名
2. `go test ./internal/driver/acp/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

测试里的 agent 一律用 P0 的 ferry-fakeagent（在 TestMain 里 go build 到临时目录），不要调用真实 agent。

## 完成后
git add -A && git commit -m "p1-t2: acp driver and registry"

回执正文：每条验收命令的真实输出关键行；快照是生成的还是手写的；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P1-T2 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P1-T3

````
# 任务 P1-T3 — 存储、权限与配对、审批与策略、凭据与净化

## 工作区
git fetch --tags
git worktree add -b p1-t3 ../ferry-p1-t3 p1-base
cd ../ferry-p1-t3

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.1、3.2、3.4、3.7、3.9、3.10 节，第 5.4 节

## 目标
实现 internal/store（SQLite）、internal/acl（owner、审批人、配对码）、internal/approval（审批 ID、裁决解析、策略引擎、等待与超时）与 internal/security（密钥掩码、IM 文本净化、凭据存取）。

## 可写文件（只有这些）
- internal/store/**（migrations/ 目录除外）
- internal/acl/**
- internal/approval/**
- internal/security/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/store/migrations/**、internal/fsp/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

## 验收（5 项）
1. `go test ./internal/store/... ./internal/acl/... -v` → 全部 PASS，且包含 5.4 节列出的对应测试名
2. `go test ./internal/approval/... ./internal/security/... -v` → 全部 PASS，且包含 5.4 节列出的对应测试名
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

approval 测试里需要的 Outbox 与 I18n 用本包 _test.go 里的简单替身。

## 完成后
git add -A && git commit -m "p1-t3: store acl approval security"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P1-T3 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P1-T4

````
# 任务 P1-T4 — 发件箱 v1、命令解析与配置加载

## 工作区
git fetch --tags
git worktree add -b p1-t4 ../ferry-p1-t4 p1-base
cd ../ferry-p1-t4

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.1、3.2、3.3、3.5、3.10 节，第 5.5 节

## 目标
实现 internal/outbox（合并、回复额度、长文本切分、同键重试）、internal/command（命令与中文别名解析）与 internal/config（默认值、加载、保存、校验）。

## 可写文件（只有这些）
- internal/outbox/**
- internal/command/**
- internal/config/**（types.go 除外）

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/fsp/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

## 验收（5 项）
1. `go test ./internal/outbox/... -v` → 全部 PASS，且包含 5.5 节列出的发件箱测试名
2. `go test ./internal/command/... ./internal/config/... -v` → 全部 PASS，且包含 5.5 节列出的对应测试名
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

发件箱测试需要的 Store 用本包 _test.go 里的内存替身，时间用 internal/clock 的假时钟。

## 完成后
git add -A && git commit -m "p1-t4: outbox command config"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P1-T4 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P1-T5

````
# 任务 P1-T5 — 核心：路由、会话管理与命令执行

## 工作区
git fetch --tags
git worktree add -b p1-t5 ../ferry-p1-t5 p1-base
cd ../ferry-p1-t5

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.1、3.2、3.5、3.6、3.9、3.10 节，第 5.6 节

## 目标
实现 internal/core：入站处理顺序、会话事件处理、审批转接、全部命令、启动时停放会话与微信离线重试。只依赖 ports 接口。

## 可写文件（只有这些）
- internal/core/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/fsp/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

其他模块在你的工作区里只有占位实现，不要调用它们的具体函数；测试替身全部写在本包的 _test.go 里。

## 验收（5 项）
1. `go test ./internal/core/... -v` → 全部 PASS，且包含 5.6 节列出的全部测试名
2. `go test ./internal/core/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p1-t5: core router and session manager"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P1-T5 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P1-TΩ

````
# 任务 P1-TΩ — 合流、命令行装配与端到端测试

## 工作区
main 分支，当前应位于标签 p1-base。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 全文，重点第 2 节、第 3.2 节、第 3.11 节、第 5.7 节

## 第一步：合流
按顺序合并：p1-t3 → p1-t4 → p1-t1 → p1-t2 → p1-t5。
一次合一个：`git merge --no-ff <分支>`，合完立刻跑 `go build ./... && go test ./...`，全绿再合下一个。
冲突按 3.11 归属表判定；表里没写的，停下来报告。
合流阶段只允许修集成缝隙，不补任何任务漏掉的功能；漏了就在回执里写明。

## 第二步：装配与端到端
按 5.7 实现 cmd/ferry 的 init、login、logout、daemon、pair、ls、status、doctor，写 test/e2e（B1–B11），更新 5.7 列出的文档。

## 可写文件
- 合流时：解决冲突所需的文件
- 第二步：cmd/ferry/**、test/e2e/**、README.md、README.zh-CN.md、docs/en/quickstart.md、docs/zh-CN/quickstart.md、docs/en/wechat.md、docs/zh-CN/wechat.md

## 禁改
- docs/dev/**、LICENSE、internal/ports/**、go.mod、go.sum

## 全量验收（17 项）
1. `go build ./... && go test ./...` → 全部 ok
2. `FERRY_HOME=$(mktemp -d) go run ./cmd/ferry doctor` → 提示运行 ferry init，退出码 0
3. `go run ./cmd/ferry --help` → 列出 init、login、logout、daemon、pair、ls、status、doctor、version
4–14. `go test ./test/e2e/ -run <名称> -v`，名称依次为 TestLoginAndOwner、TestStrangerPairing、TestManagedSession、TestApprovalFlow、TestPolicy、TestStop、TestBusyMerge、TestReplyBudget、TestSessionExpired、TestRestartResume、TestWorkspaceDenied → 每个都 PASS
15. `go run ./scripts/check-i18n` → `i18n ok`
16. `go test ./internal/testkit/... ./internal/i18n/... ./scripts/...` → 全部 ok
17. `go vet ./... && test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p1-tomega: wire daemon and e2e"
不要推送，不要打标签。

回执正文：每次合入后的验收结果；冲突与判定依据；17 项验收的真实输出关键行；还差什么。最后一行严格为：
RECEIPT P1-TΩ status=<done|blocked> merged=<合入数>/5 checks=<通过数>/17
````

## 9. 人工验收清单（真实微信 + 真实 agent）

在 TΩ done 之后、打 `p1-done` 之前完成。准备：bot 用的微信小号已开通 ClawBot；本机装好 Kimi CLI 或 Qwen Code 并已登录。

| # | 操作 | 期望 | 对应 |
| --- | --- | --- | --- |
| 1 | `go install ./cmd/ferry` 后运行 `ferry init`，语言选中文，工作区填 `~/code`，默认 agent 选本机可用的那个 | 生成配置；`ferry doctor` 全部通过 | — |
| 2 | `ferry login wechat`，用 bot 小号扫码 | 打印 bot 与主人 ID | A4 |
| 3 | `ferry daemon`；另开终端 `ferry status` | 显示 wechat online | A4 |
| 4 | 主号给 bot 发 `/agents` | 列出预设和可用状态 | — |
| 5 | 发 `/new kimi ~/code/demo`，再发「列出这个目录的文件」 | 先收到受理回执，再收到结果；`ferry ls` 显示会话 | A6 |
| 6 | 发「运行 go test ./...」或其他需要执行命令的请求 | 收到带 5 位 ID 的审批；回复 `Y <ID>` 后继续执行 | A7 |
| 7 | 发一个耗时较长的任务，执行中发 `/stop` | 收到「已停止」 | A8 |
| 8 | 执行中连发 3 条消息 | 本轮结束后三条合并成一轮 | A9 |
| 9 | 用第三个微信号给 bot 发 `rm -rf ~` | 只收到配对码，agent 没有任何动作 | A5 |
| 10 | 主号发 `/pair <配对码>` | 双方都收到确认；第三个号之后的消息能进 agent | — |
| 11 | 停掉守护进程再启动；发 `/ls`、`/use 1`，再提问 | 会话显示已停放；恢复后能接着对话（agent 支持恢复时） | — |
| 12 | 实测并填写 00 总览第 7 节：回复上限、context_token 时长、群聊、语音转写 | 表格填完并提交 | — |

全部通过后：`git push origin main`，确认 CI 全绿，`git tag p1-done v0.1.0 && git push origin p1-done v0.1.0`。之后进入 P1 验证关卡（00 总览第 2 节）。
