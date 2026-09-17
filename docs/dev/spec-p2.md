# 开发派工 Spec — Ferry P2 终端接力与可靠投递

> 生成于 2026-09-17。本轮唯一权威文档，另须遵守 `docs/dev/00-overview.md` 第 3 节。
> 前置条件：`p1-done` 已在 `main`；第 0 节的人工核实已完成并写回本文件。冻结后不再修改。

## 0. 开工前人工核实（T0 之前，约 30 分钟）

以下结果写回本文件第 3.3 节与第 3.5 节后再发 T0。执行者不负责核实，只照表实现。

| 项 | 方法 | 写回位置 |
| --- | --- | --- |
| 各 agent 恢复指定会话的参数 | 在装有该 agent 的电脑上查看 `<命令> --help` | 3.3「恢复命令」列 |
| 各 agent 运行时的命令行特征 | 启动 agent 后用 `ps -axo pid,command` 查看 | 3.3「进程匹配」列 |
| 终端里开的会话能否经 ACP 列出并恢复 | 在终端开一个会话，再用 Zed 或 Toad 等 ACP 客户端查看历史会话并恢复 | 3.3「一档可用」列 |
| OpenCode 服务的认证方式、事件类型名、权限回复取值 | `opencode serve --help`；OpenCode 仓库 `packages/web/src/content/docs/server.mdx` 与其 SDK 类型定义 | 3.5 |
| Qwen 守护进程的事件流格式、认证、`X-Qwen-Client-Id` 规则 | Qwen Code 仓库 `docs/developers/daemon-rest-api-reference.md` 与 `qwen-serve-protocol.md` | 3.5 |
| P1 实测值（回复上限、context_token 时长） | 00 总览第 7 节 | 3.2 的 `window_ttl` 默认值 |

### 0.1 核实结果（2026-09-17，总管填写）

| 项 | 结果 |
| --- | --- |
| claude-acp、codex-acp 的恢复参数 | ✅ 本机 `--help` 核实，见 3.3「依据」列 |
| OpenCode 认证 | ✅ HTTP Basic，见 3.5 |
| Qwen 认证、`X-Qwen-Client-Id`、事件 schema | ✅ 官方协议文档核实，见 3.5 |
| OpenCode 事件名 | ⚠️ 只有社区来源的候选名，**按未核实对待**（见 3.5 与 5.3） |
| OpenCode 权限 `response` 取值 | ❌ 仍待核实 |
| kimi、qwen-code、opencode 的进程匹配与「一档可用」 | ❌ 本机未装这三个 agent，无法核实；3.3 相应项保持待核实，`Resume` 留空、`ferry back` 返回不支持 |
| P1 实测值（回复上限、`context_token` 时长） | ❌ 无第二个微信号，P1 人工验收未做；`outbox.window_ttl` 用 3.2 规定的未实测默认值 **23h** |

未核实项不阻塞本轮：B1–B4 全部用 `ferry-fakeagent`（3.3 已定），B5/B6 用 T2 自己写的假服务。
风险在于假服务可能与真实服务不符 —— 见 5.3 对事件映射表的隔离要求。

## 1. 问题定义

**要做什么**：

1. **一档接力**：微信 `/ls` 列出工作区里 agent 最近的终端会话；`/use` 经 ACP 恢复后在微信里继续；`ferry back` 停放 Ferry 这一份，并给出该 agent 的原生恢复命令；终端仍在占用时不接管。
2. **二档同时在线**：OpenCode（`opencode serve`）与 Qwen Code（`qwen serve`）的服务驱动，终端与微信看到同一个会话。
3. **发件箱 v2**：窗口外积压与摘要、`/pull`、长输出转文件与 `/file`、唤醒通道（ntfy、Bark、Webhook、邮件）。
4. **微信媒体**：收图片、文件、语音（下载解密到本地，把路径交给 agent）；发文件与图片。
5. **本机控制接口**：命令行与守护进程之间的通信，供 `ferry back` 使用，P3 继续扩展。

**为什么**：接力和可靠投递是 Ferry 相对现有微信桥的两个核心差异点（产品方案 2.3）。

**明确不做**：

- `ferry proxy`、Claude Code 插件、第二个 IM、ACP v2、PTY（P3）
- Windows 上的终端占用检测（本轮返回「无法确认」，要求用户显式 force）
- 视频发送、语音转文字（只使用微信自带的转写）

## 2. 验收标准（DoD）

### 2.1 能跑

- **A1** `go build ./... && go test ./...` → 全部 ok
- **A2** `go run ./cmd/ferry back --help` → 显示用法，含 `--exec` 与 `--slot`
- **A3** `go run ./cmd/ferry --help` → 在 P1 的基础上多出 `back` 与 `notify`

### 2.2 行为正确

端到端测试由 TΩ 写在 `test/e2e/`，沿用 P1 的夹具。

- **B1** `go test ./test/e2e/ -run TestTerminalDiscovery -v` → 在 `FAKEAGENT_HOME` 里预置一个 cwd 为 `<ws>/demo` 的会话；微信 `/ls` 出现一行带「终端」标记的条目，标题为该会话标题
- **B2** `-run TestAdoptAndContinue` → `/use <该条编号>` 经 `session/load` 恢复；`echo x` 得到回复；恢复过程重放的历史没有发到微信
- **B3** `-run TestBackCommand` → 接着调用 `ferry back` 的命令函数：输出 `ferry-fakeagent --resume <id>`，Ferry 侧会话变为 parked；带 `--exec` 时输出 `resumed <id>`
- **B4** `-run TestBusyRefused`（仅 Linux、macOS）→ 启动一个工作目录为 `<ws>/demo` 的 `ferry-fakeagent` 进程模拟终端占用；`/use` 对应终端会话时收到占用提示且没有接管；发 `/use <n> force` 后接管成功
- **B5** `-run TestServerOpenCode` → 假 OpenCode 服务：`/new opencode-live <ws>/demo` 后发 `hello`，假服务收到 `prompt_async`，事件流推送的文本回到微信；推送一个权限事件，微信出现审批，回复 `y <ID>` 后假服务收到权限回复
- **B6** `-run TestServerQwen` → 假 Qwen 守护进程：同上，对应 `POST /session/:id/prompt`、`GET /session/:id/events`、`POST /session/:id/permission/:requestId`
- **B7** `-run TestHeldAndDigest`（产品方案 A13）→ `outbox.window_ttl = "1s"`；派 `slow 2000`；结果进入 held；之后再发任意消息，先收到含 1 条积压的摘要，再收到结果
- **B8** `-run TestPull` → 积压 12 条；发消息后收到摘要与第一批；`/pull` 收到下一批；积压清空后 `/pull` 回复「没有积压」
- **B9** `-run TestLongOutputFile` → `burst 100`（约 6000 字）后收到摘要文本和 1 条文件消息（假网关记录到 file_item）；`/file 1` 再次收到同一文件
- **B10** `-run TestWakeNotifier` → 窗口过期后触发审批：假 ntfy 服务收到 1 条提醒，正文只写「有待处理的审批」和会话编号，不含工具名、命令或路径
- **B11** `-run TestInboundImage` → 假网关推送一张加密图片：`<home>/inbox/` 下出现解密后的文件，内容与原图一致；agent 收到的 prompt 含该文件路径
- **B12** `-run TestReplyBudget`（产品方案 A14，P1 已有）→ 仍然 PASS
- **B13** `-run TestRetryDedupe`（产品方案 A15）→ 假网关让 `sendmessage` 超时一次；重试复用同一个 client_id；假网关只有 1 条非重复记录
- **B14** `go run ./scripts/check-i18n` → `i18n ok`

### 2.3 没弄坏

- **C1** `go test ./test/e2e/ -v` → P1 的 11 个端到端测试与本轮新增测试全部 PASS
- **C2** 00 总览 3.5 的 G1–G3 → 全部通过

## 3. 冻结契约

### 3.1 接口变更 `internal/ports`

P1 的接口全部保留，以下为变更与新增。

```go
// SessionSpec 新增 Server 字段：非空时由服务驱动处理，Command 为空。
type SessionSpec struct {
	Agent   string
	Command []string
	Cwd     string
	Env     []string
	Server  *ServerSpec
}

type ServerSpec struct {
	Kind   string // opencode | qwen
	URL    string // 只允许回环地址
	Secret string // 从 secret_env 读出的值，可空
}

// Outbox 新增三个方法；FlushHeld 在 P2 有真实行为（见 3.6）。
type Outbox interface {
	OpenWindow(ep fsp.EndpointID, replyCtx string, inboundAt time.Time)
	Enqueue(ctx context.Context, msg fsp.Outbound) error
	FlushHeld(ctx context.Context, ep fsp.EndpointID) error
	Run(ctx context.Context) error
	Pull(ctx context.Context, ep fsp.EndpointID) error        // 发送下一批积压；没有时回复 outbox.nothing_held
	File(ctx context.Context, ep fsp.EndpointID, n int) error  // 重发第 n 份长输出文件，1 为最近一份
	Held(ep fsp.EndpointID) int                                // 当前积压条数
}

// Waker：唤醒通道的统一入口，由 notify 包实现。
type Waker interface {
	// Wake 按配置发送提醒；同一入口同类提醒受 notify.min_interval 限制。
	// kind：approval_pending | turn_finished | im_offline。
	// args 只能放会话编号等非敏感信息，不能放消息正文、工具参数、命令或路径。
	Wake(ctx context.Context, ep fsp.EndpointID, kind string, args map[string]any)
}

type TerminalSession struct {
	Agent          string // 配置里的 agent 名
	Driver         fsp.DriverKind
	AgentSessionID string
	Title          string
	Cwd            string
	UpdatedAt      time.Time
	Shared         bool // 二档会话：允许与终端同时使用，不做占用检查
}

type Handoff interface {
	// Discover 列出工作区内各 agent 近 lookback_days 天的会话，排除已登记到 Ferry 的；结果缓存 1 分钟。
	Discover(ctx context.Context, ep fsp.EndpointID) ([]TerminalSession, error)
	// Busy 判断终端里是否仍有进程占用该会话；Windows 返回 fsp.ErrUnsupported。
	Busy(ctx context.Context, ts TerminalSession) (bool, error)
	// BackCommand 返回在终端恢复该会话的命令；未核实的 agent 返回 fsp.ErrUnsupported。
	BackCommand(agent, agentSessionID string) ([]string, error)
}

type LongOutput struct {
	ID        int64
	Endpoint  fsp.EndpointID
	Session   fsp.SessionID
	Path      string
	Chars     int
	CreatedAt time.Time
}

// Store 新增：
//   HoldOutbox(ctx, id int64, at time.Time) error                // 置为 held 并记录 held_at
//   AddLongOutput(ctx, lo LongOutput) (int64, error)
//   LongOutputs(ctx, ep fsp.EndpointID, limit int) ([]LongOutput, error) // 最新在前
//   DeleteLongOutput(ctx, id int64) error
```

`IMAdapter` 签名不变，行为变化：`Send` 支持 `fsp.OutFile`；入站附件下载成功时 `Attachment.Path` 非空。

### 3.2 配置变更 `internal/config/types.go`

```go
type Config struct {
	// P1 字段保持不变，新增：
	Handoff HandoffConfig `toml:"handoff"`
	Notify  NotifyConfig  `toml:"notify"`
}

type WeChatConfig struct {
	// P1 字段保持不变，新增：
	CDNBaseURL string `toml:"cdn_base_url"` // 默认取 openilink-sdk-go 的默认 CDN 基址；测试指向假服务
	MaxMediaMB int    `toml:"max_media_mb"` // 默认 20
}

type AgentConfig struct {
	// P1 字段保持不变，新增：
	Server  *ServerConfig `toml:"server"`  // 设置后使用服务驱动；与 registry、command 三选一
	Handoff *bool         `toml:"handoff"` // 是否参与终端会话发现；缺省时有接力配置即为 true
}

type ServerConfig struct {
	Kind      string `toml:"kind"`       // opencode | qwen
	URL       string `toml:"url"`        // 只允许 127.0.0.1、::1、localhost
	SecretEnv string `toml:"secret_env"` // 从该环境变量读取密码或令牌，可空
}

type HandoffProfile struct {
	Agent        string   `toml:"agent"`         // 预设名或 agent 名
	Resume       []string `toml:"resume"`        // 例：["kimi", "--resume", "{id}"]；为空表示不支持
	ProcessMatch string   `toml:"process_match"` // 正则，匹配进程完整命令行
}

type HandoffConfig struct {
	Profiles     []HandoffProfile `toml:"profiles"`      // 覆盖或补充 3.3 的内置表
	LookbackDays int              `toml:"lookback_days"` // 默认 7
}

type OutboxConfig struct {
	// P1 字段保持不变，新增：
	WindowTTL  Duration `toml:"window_ttl"`  // 默认：00 总览第 7 节实测值减 30 分钟；未实测时 23h
	DigestMax  int      `toml:"digest_max"`  // 摘要最多列几条，默认 5
	FileKeep   int      `toml:"file_keep"`   // 每个入口保留的长输出文件数，默认 20
	LongOutput string   `toml:"long_output"` // file | split，默认 file
}

type NotifyConfig struct {
	Enabled     []string      `toml:"enabled"`      // ntfy bark webhook email；默认空
	Events      []string      `toml:"events"`       // 默认 approval_pending、turn_finished、im_offline
	MinInterval Duration      `toml:"min_interval"` // 默认 10m
	Ntfy        NtfyConfig    `toml:"ntfy"`
	Bark        BarkConfig    `toml:"bark"`
	Webhook     WebhookConfig `toml:"webhook"`
	Email       EmailConfig   `toml:"email"`
}

type NtfyConfig struct {
	URL      string `toml:"url"`       // 默认 https://ntfy.sh
	Topic    string `toml:"topic"`
	TokenEnv string `toml:"token_env"` // 可空；设置时带 Authorization: Bearer
}

type BarkConfig struct {
	URL    string `toml:"url"`     // 默认 https://api.day.app
	KeyEnv string `toml:"key_env"` // 设备 key 所在的环境变量
}

type WebhookConfig struct {
	URL       string `toml:"url"`
	SecretEnv string `toml:"secret_env"` // 设置时请求头 X-Ferry-Signature 为 body 的 HMAC-SHA256 十六进制
}

type EmailConfig struct {
	Host        string   `toml:"host"`
	Port        int      `toml:"port"` // 默认 587，STARTTLS
	User        string   `toml:"user"`
	PasswordEnv string   `toml:"password_env"`
	From        string   `toml:"from"`
	To          []string `toml:"to"`
}
```

新增校验：`server.url` 必须是回环地址；`kind` 只能是 opencode 或 qwen；`notify.enabled` 里列出的通道，其必填项必须齐全；`window_ttl` 不小于 1s；`long_output` 只能是 file 或 split。

配置样例（追加到 `internal/config/testdata/sample.toml`，由 T3 放置）：

```toml
[[agents]]
name = "opencode-live"
[agents.server]
kind = "opencode"
url = "http://127.0.0.1:4096"
secret_env = "OPENCODE_SERVER_PASSWORD"

[[agents]]
name = "qwen-live"
[agents.server]
kind = "qwen"
url = "http://127.0.0.1:4170"

[outbox]
long_output = "file"

[notify]
enabled = ["ntfy"]
[notify.ntfy]
topic = "ferry-your-random-topic"
```

### 3.3 接力内置表 `internal/handoff/profiles.go`（T0 按第 0 节核实结果写）

`{id}` 为会话 ID 占位符。标注「待核实」的项在核实前 `Resume` 留空，`ferry back` 对其返回不支持。

| agent（预设名） | 恢复命令 | 进程匹配 | 一档可用 | 依据 |
| --- | --- | --- | --- | --- |
| kimi | `kimi --resume {id}` | `(^\|/)kimi( \|$)`，排除含 ` acp` 的命令行 | 待核实 | 源码确认 `--session` 与 `--resume` 为同一参数 |
| qwen-code | `qwen --resume {id}` | 命令行含 `qwen` 且不含 `--acp` | 待核实 | 源码确认有 `--resume` |
| opencode | 待核实 | `(^\|/)opencode( \|$)`，排除 `acp` 与 `serve` | 待核实 | 优先用二档 |
| claude-acp | `claude --resume {id}` | `(^\|/)claude( \|$)` | 待核实 | 本机 `claude --help` 核实（2026-09-17）：`--resume <session-id>` |
| codex-acp | `codex resume {id}` | `(^\|/)codex( \|$)` | 待核实 | 本机 `codex resume --help` 核实（2026-09-17）：`codex resume [SESSION_ID]` |
| codebuddy-code、minimax-code、qoder、glm-acp-agent | 待核实 | 待核实 | 待核实 | 待核实 |
| fake（测试专用） | `ferry-fakeagent --resume {id}` | `ferry-fakeagent` | 是 | P0 已定 |

说明：表格里的 `\|` 是 Markdown 转义，写进代码时为普通竖线。

### 3.4 本机控制接口 `internal/control`

- 守护进程监听 `127.0.0.1:0`，把 `{"port":…, "token":"<32 位十六进制>", "pid":…}` 写入 `<home>/control.json`，文件权限 0600。
- 每行一个 JSON 对象。令牌不符时直接断开连接并记审计。

```go
package control // types.go 由 T0 写

type Request struct {
	Token string          `json:"token"`
	Op    string          `json:"op"` // P2：status | park
	Args  json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"` // not_found | unsupported | busy | internal
	Data  json.RawMessage `json:"data,omitempty"`
}

type ParkRequest struct {
	Slot int `json:"slot,omitempty"` // 0 表示 owner 入口的前台会话
}

type ParkResponse struct {
	Agent          string   `json:"agent"`
	AgentSessionID string   `json:"agent_session_id"`
	Cwd            string   `json:"cwd"`
	BackCommand    []string `json:"back_command"` // 不支持时为空
}

type StatusResponse struct {
	IMOnline bool   `json:"im_online"`
	BotID    string `json:"bot_id"`
	Sessions int    `json:"sessions"`
	Running  int    `json:"running"`
}

// 以下由 T1 实现
type Handler interface {
	Park(ctx context.Context, req ParkRequest) (ParkResponse, error)
	Status(ctx context.Context) (StatusResponse, error)
}

func Serve(ctx context.Context, home string, h Handler) error
func Dial(home string) (*Client, error) // control.json 不存在或连接失败时返回 fsp.ErrNotFound
func (c *Client) Park(ctx context.Context, req ParkRequest) (ParkResponse, error)
func (c *Client) Status(ctx context.Context) (StatusResponse, error)
func (c *Client) Close() error
```

`ferry back [--slot n] [--exec]`：

1. `control.Dial` 成功 → 调用 `Park`，打印 `back.command`（命令按 shell 规则加引号）；带 `--exec` 时切到会话目录并执行该命令（Unix 用 exec 替换当前进程，Windows 用子进程并透传标准输入输出）。
2. 守护进程未运行 → 直接读数据库，找到 owner 入口的前台会话（或 `--slot` 指定的会话），用 `Handoff.BackCommand` 生成命令，并提示守护进程未运行。
3. 该 agent 不支持 → 打印 `back.unsupported`，退出码 3。

### 3.5 服务驱动映射 `internal/driver/server`

依据：OpenCode 仓库 `server.mdx`，Qwen Code 仓库 `daemon-rest-api-reference.md`（2026-09-17 核对路由）。第 0 节核实的细节写在「待核实」处。

**OpenCode**

| 动作 | 调用 |
| --- | --- |
| 健康检查 | `GET /global/health`，返回 `{healthy, version}` |
| 新建会话 | `POST /session`，body `{title}`，返回 Session（取 `id`） |
| 恢复 | `GET /session/:id` 存在即可，无需其他调用 |
| 列表 | `GET /session` |
| 发送 | `POST /session/:id/prompt_async`，body `{parts: [{type: "text", text}]}`，返回 204 |
| 事件 | `GET /event`（SSE），首事件 `server.connected`，其后为 bus 事件，按会话 ID 过滤。**候选事件名（仅社区来源，官方文档正文未列，按「未核实」对待）**：文本增量 `message.part.updated`；会话空闲 `session.idle`；错误 `session.error`；权限 `permission.asked` / `permission.updated` / `permission.replied` |
| 取消 | `POST /session/:id/abort` |
| 权限回复 | `POST /session/:id/permissions/:permissionID`，body `{response, remember?}`；**response 的取值：仍待核实**（官方文档只给字段名） |
| 认证 | 设置了 `OPENCODE_SERVER_PASSWORD` 时走 **HTTP Basic**，用户名默认 `opencode`，密码为该环境变量的值（opencode.ai/docs/server 核实 2026-09-17） |

**Qwen Code**

| 动作 | 调用 |
| --- | --- |
| 健康检查 | `GET /health`；能力查询 `GET /capabilities` |
| 新建会话 | `POST /session`，body 含 `cwd`；该目录必须已由 `qwen serve --workspace` 注册，否则返回 `400 workspace_mismatch` |
| 恢复 | `POST /session/:id/load` 或 `POST /session/:id/resume`，按 `/capabilities` 选择 |
| 发送 | `POST /session/:id/prompt` |
| 事件 | `GET /session/:id/events`（SSE）。事件名与字段依 `docs/developers/daemon/09-event-schema.md` 核实（2026-09-17）：内容增量 `session_update`（`sessionUpdate`、`content?`）；权限 `permission_request`（`requestId`、`sessionId`、`toolCall`、`options[]`、`originatorClientId`）与 `permission_resolved`（`requestId`、`outcome`）；轮次结束 `turn_complete`（`sessionId`、`stopReason`、`promptId?`）；轮次出错 `turn_error`（`sessionId`、`message`、`code?`、`promptId?`）；会话终止 `session_died` / `session_closed`；流错误 `stream_error` |
| 取消 | `POST /session/:id/cancel` |
| 权限回复 | `POST /session/:id/permission/:requestId` |
| 状态 | `GET /session/:id/status` |
| 认证 | `Authorization: Bearer <令牌>`（协议文档核实 2026-09-17：普通路由一律用 Bearer） |
| 客户端标识 | `X-Qwen-Client-Id`：**create/load 响应给了就原样带回**，没给就不带。它是附件与归属标识，不是安全主体（协议文档核实 2026-09-17） |

两种服务共同的规则：

- 只连接回环地址；每个会话一条事件流；事件流断开时按 1、2、4 … 30 秒退避重连，重连后查询一次会话状态补齐；连续 5 分钟连不上则发 EvError，会话置为 parked。
- 权限事件转成 `fsp.ApprovalRequest`，选项映射为 allow_once、allow_always、reject_once（按核实的取值对应）。
- `List` 返回的会话 `Shared = true`：二档会话允许与终端同时使用。
- `Close` 只断开事件流，不删除服务端会话。

### 3.6 发件箱 v2 规则（T3 实现）

- **窗口**：`now - OpenedAt ≥ window_ttl` 视为没有窗口。
- **没有窗口时**：所有新消息置为 held，包括紧急消息。紧急消息 held 时调用 `Waker.Wake`：审批请求用 `approval_pending`，本轮最终结果用 `turn_finished`。
- **FlushHeld**（核心在入站放行后调用）：有积压时，先发一条 `outbox.digest`，每条积压一行（会话编号、类型、前 40 字），最多 `digest_max` 行；再按时间顺序补投积压，紧急的优先；补投数量为「额度 − 3」（给摘要、受理回执、本轮结果各留 1 个）；还有剩余时在摘要末尾加 `outbox.digest_more`。
- **Pull**：发送下一批，数量为「额度 − 1」；没有积压时回复 `outbox.nothing_held`。
- **长输出**（`long_output = file`）：文本超过阈值时写入 `<home>/outbox/<入口哈希>/<时间戳>-<会话ID>.md`，发送 `outbox.file_summary`（开头 300 字加总字数）和一条文件消息，共占 2 个额度；每个入口只保留最近 `file_keep` 份；文件消息发送失败时回退为分段文本。
- **File**：重发第 n 份，占 1 个额度；不存在时回复 `outbox.file_missing`。
- 入口哈希为 `EndpointID.String()` 的 SHA-256 前 16 位十六进制。

### 3.7 微信媒体（T4 实现）

`internal/ilinkwire` 追加以下类型（T0 写，字段名依据 openilink-sdk-go v0.6.0 `types.go`）：

```go
type UploadMediaType int

const (
	UploadImage UploadMediaType = 1
	UploadVideo UploadMediaType = 2
	UploadFile  UploadMediaType = 3
	UploadVoice UploadMediaType = 4
)

type GetUploadURLReq struct {
	FileKey       string          `json:"filekey,omitempty"`
	MediaType     UploadMediaType `json:"media_type,omitempty"`
	ToUserID      string          `json:"to_user_id,omitempty"`
	RawSize       int64           `json:"rawsize,omitempty"`
	RawFileMD5    string          `json:"rawfilemd5,omitempty"`
	FileSize      int64           `json:"filesize,omitempty"` // 密文大小
	ThumbRawSize  int64           `json:"thumb_rawsize,omitempty"`
	ThumbRawMD5   string          `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize int64           `json:"thumb_filesize,omitempty"`
	NoNeedThumb   bool            `json:"no_need_thumb,omitempty"`
	AESKey        string          `json:"aeskey,omitempty"`
	BaseInfo      *BaseInfo       `json:"base_info,omitempty"`
}

type GetUploadURLResp struct {
	APIError
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}

// ImageItem 追加字段
//   ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
//   AESKey      string    `json:"aeskey,omitempty"`
//   URL         string    `json:"url,omitempty"`
//   MidSize     int64     `json:"mid_size,omitempty"`
//   ThumbSize   int64     `json:"thumb_size,omitempty"`
//   ThumbHeight int       `json:"thumb_height,omitempty"`
//   ThumbWidth  int       `json:"thumb_width,omitempty"`
//   HDSize      int64     `json:"hd_size,omitempty"`
// VoiceItem 追加字段
//   EncodeType    int `json:"encode_type,omitempty"`
//   BitsPerSample int `json:"bits_per_sample,omitempty"`
//   SampleRate    int `json:"sample_rate,omitempty"`
```

行为：

- **加密**：AES-128-ECB，PKCS#7 填充。密钥字段可能是 base64（标准或 URL 安全、带或不带填充）或其内容再做一次十六进制编码，按 openilink-sdk-go `cdn.go` 的顺序逐一尝试。
- **地址**：下载为 `<cdn_base>/download?encrypted_query_param=<转义后的参数>`，媒体带 `full_url` 时优先用它；上传为 `upload_full_url`，没有时用 `<cdn_base>/upload?encrypted_query_param=<upload_param>&filekey=<filekey>`。上传成功后如何取得消息里的 `encrypt_query_param`，照 `cdn.go` 的做法。
- **入站**：图片、文件、语音下载解密后保存到 `<home>/inbox/<入口哈希>/<message_id>-<安全文件名>`，文件名去掉路径分隔符与控制字符；超过 `max_media_mb` 时不下载；语音保留原始编码文件，`voice_item.text` 非空时并入文本。
- **出站**：`fsp.OutFile` 读取文件（不超过 `max_media_mb`），随机生成 16 字节密钥与 32 位十六进制 filekey；扩展名为 png、jpg、jpeg、gif、webp 时按图片发送，否则按文件发送；`no_need_thumb` 为 true。
- 如复制 openilink-sdk-go 的代码，文件头保留其 MIT 版权声明。

假 iLink 服务（T4 负责扩展）：实现 `getuploadurl`，在同一个 httptest 服务上提供 `/cdn/upload` 与 `/cdn/download`，并新增：

```go
func (s *Server) CDNBaseURL() string
func (s *Server) PushImage(fromUserID string, plain []byte) (contextToken string)
func (s *Server) PushFile(fromUserID, name string, plain []byte) (contextToken string)
func (s *Server) SentFiles() []SentFile // 已解密的出站文件，便于断言

type SentFile struct {
	ToUserID, Name string
	Kind           ilinkwire.ItemType
	Plain          []byte
}
```

### 3.8 核心变更（T1 实现）

- **`/ls`**：先列已登记会话（编号即 slot），再列 `Handoff.Discover` 的结果，编号接着往后排，行尾标「终端」（`session.list_terminal_item`）。终端条目的临时编号在内存里保留 1 分钟。
- **`/use <n>`** 指向终端条目时：非 Shared 的会话先调 `Busy`；占用时回复 `handoff.busy`，无法确认时回复 `handoff.busy_unknown`，都不接管；带 `force` 时跳过检查。然后登记为 origin=terminal 的会话，`Bind`，经对应驱动 `Resume`，回复 `handoff.adopted`。
- **驱动选择**：`SessionSpec.Server` 非空用 `fsp.DriverServer`，否则用 `fsp.DriverACP`。
- **附件**：Path 非空的附件以 `prompt.attachment` 逐行附在 prompt 末尾；Path 为空的附件按原因回复 `media.too_large` 或 `media.download_failed`，文本部分照常处理；文本为空且没有可用附件时结束。
- **命令**：`/pull` 调 `Outbox.Pull`；`/file <n>` 调 `Outbox.File`。
- **微信离线**：IM 返回 `fsp.ErrSessionExpired` 时，对 owner 入口调用 `Waker.Wake(im_offline)`。
- **控制接口**：核心实现 `control.Handler`；`Park` 关闭会话进程、置为 parked、回复 owner `handoff.parked_by_back`，并返回恢复命令。
- **Deps 新增**：`Handoff ports.Handoff`、`Waker ports.Waker`。

### 3.9 存储迁移 `internal/store/migrations/002_p2.sql`（T0 写）

```sql
ALTER TABLE outbox ADD COLUMN held_at INTEGER NOT NULL DEFAULT 0;

CREATE TABLE long_outputs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint   TEXT NOT NULL,
  session_id TEXT NOT NULL,
  path       TEXT NOT NULL,
  chars      INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX long_outputs_endpoint ON long_outputs(endpoint, id);
```

### 3.10 构造函数（新增与变更）

```go
// internal/handoff（T1）
type Options struct {
	Roots    []string
	Agents   []config.AgentConfig
	Profiles []config.HandoffProfile // 配置里的补充项，与 3.3 的内置表合并，配置优先
	Lookback time.Duration
	Registry ports.Registry
	Drivers  map[fsp.DriverKind]ports.Driver
	Store    ports.Store
	Clock    clock.Clock
	Log      *slog.Logger
}
func New(opts Options) ports.Handoff

// internal/driver/server（T2）
func New(log *slog.Logger) ports.Driver // Kind() 返回 fsp.DriverServer

// internal/notify（T3）
type Options struct {
	Config config.NotifyConfig
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}
func New(opts Options) (ports.Waker, error)
func Test(ctx context.Context, cfg config.NotifyConfig, channel string) error // ferry notify test <通道>

// internal/outbox（T3）Options 新增字段
//   Waker ports.Waker
//   Home  string

// internal/core（T1）Deps 新增字段见 3.8
```

`registry.Resolve` 对设置了 `server` 的 agent 返回 `Server` 非空、`Command` 为空的规格，`Secret` 从 `secret_env` 读取。

### 3.11 i18n 新增键（T0 加齐中英两份）

| 键 | 中文 |
| --- | --- |
| `session.list_terminal_item` | {{.Slot}}. {{.Agent}} · {{.Title}} · {{.When}}（终端） |
| `handoff.adopted` | 已接管终端会话：{{.Title}} |
| `handoff.busy` | 这个会话还在终端里使用（{{.Agent}}，{{.Cwd}}）。请先在电脑上退出，或发送 /use {{.Slot}} force 强制接管 |
| `handoff.busy_unknown` | 无法确认终端是否还在使用这个会话。确认已退出后，发送 /use {{.Slot}} force |
| `handoff.parked_by_back` | 会话 {{.Slot}} 已交回终端 |
| `back.command` | 在终端执行：{{.Command}} |
| `back.unsupported` | {{.Agent}} 暂不支持自动接回，请用它自己的方式恢复会话 {{.ID}} |
| `back.no_session` | 没有可以接回的会话 |
| `back.daemon_down` | 守护进程未运行，以下命令根据本地记录生成 |
| `outbox.digest` | 你不在时有 {{.N}} 条消息：（换行）{{.Lines}} |
| `outbox.digest_more` | 发送 /pull 查看剩余 {{.N}} 条 |
| `outbox.nothing_held` | 没有积压的消息 |
| `outbox.file_summary` | 输出较长（共 {{.Chars}} 字），完整内容见文件。开头：（换行）{{.Head}} |
| `outbox.file_missing` | 没有第 {{.N}} 份文件 |
| `prompt.attachment` | [附件] {{.Path}} |
| `media.too_large` | 文件超过 {{.Max}}MB，未下载 |
| `media.download_failed` | 附件下载失败：{{.Name}} |
| `server.unreachable` | 连不上 {{.Agent}} 的本机服务（{{.URL}}），请确认它已启动 |
| `server.workspace_mismatch` | Qwen 守护进程没有注册这个目录，请用 qwen serve --workspace {{.Dir}} 启动 |
| `notify.approval_pending.title` / `.body` | Ferry：有待处理的审批 / 会话 {{.Slot}} 在等你审批，打开微信发任意消息即可查看 |
| `notify.turn_finished.title` / `.body` | Ferry：任务完成 / 会话 {{.Slot}} 已完成，打开微信发任意消息即可查看 |
| `notify.im_offline.title` / `.body` | Ferry：微信已断开 / 请在电脑上运行 ferry login wechat 重新登录 |
| `usage.use_force` | 用法：/use <编号> [force] |
| `cli.notify_ok` | 已通过 {{.Channel}} 发送测试提醒 |

### 3.12 失败面

| 场景 | 行为 |
| --- | --- |
| 服务驱动连不上 | `Start` 返回错误；回复 `server.unreachable` |
| Qwen 返回 `400 workspace_mismatch` | 映射为 `fsp.ErrWorkspaceDenied`；回复 `server.workspace_mismatch` |
| 事件流断开 | 见 3.5 |
| 终端会话发现：某个 agent 超过 10 秒未返回 | 跳过该 agent，记 warn |
| 终端会话恢复失败 | 回复 `session.start_failed`，不登记会话 |
| 占用检测出错 | 按「无法确认」处理 |
| 媒体下载失败或超限 | 见 3.8 |
| 文件上传失败 | 长输出回退为分段文本；`/file` 回复失败原因 |
| 唤醒通道发送失败 | 记日志与审计，最多重试 1 次 |
| 控制接口不可用 | `ferry back` 读数据库给出命令，并打印 `back.daemon_down` |
| 控制令牌不符 | 断开连接，记审计 |

### 3.13 文件归属表

| 路径 | owner | 其他任务 |
| --- | --- | --- |
| `internal/ports/**`、`internal/config/types.go`、`internal/store/migrations/**`、`internal/ilinkwire/**`、`internal/handoff/profiles.go`、`internal/control/types.go`、`locales/*.toml`、`go.mod`、`go.sum`、`internal/deps/**` | T0 | 只读，改动即任务失败 |
| `internal/handoff/**`（profiles.go 除外）、`internal/control/**`（types.go 除外）、`internal/core/**`、`cmd/ferry/back.go` | T1 | 不可改 |
| `internal/driver/server/**`、`internal/driver/acp/**`、`internal/registry/**` | T2 | 不可改 |
| `internal/outbox/**`、`internal/notify/**`、`internal/command/**`、`internal/store/**`（migrations 除外）、`internal/config/**`（types.go 除外）、`cmd/ferry/notify.go` | T3 | 不可改 |
| `internal/im/wechat/**`、`internal/testkit/fakeilink/**` | T4 | 不可改 |
| `cmd/ferry/**`（back.go、notify.go 除外）、`test/e2e/**`、`README.md`、`README.zh-CN.md`、`docs/en/handoff.md`、`docs/zh-CN/handoff.md`、`docs/en/agents.md`、`docs/zh-CN/agents.md`、`docs/en/wechat.md`、`docs/zh-CN/wechat.md` | TΩ | 不可改 |
| 其余 P0、P1 产物 | 无 | 只读 |
| `docs/dev/**`、`LICENSE` | 人工 | 只读 |

## 4. 基线

- T0 起点：`main` 上的 `p1-done`，工作区干净，第 0 节核实结果已提交。
- 并行基线：标签 `p2-base`。

## 5. 任务表

| 任务 | 目标 | 时序 |
| --- | --- | --- |
| T0 | 接口与配置变更、迁移、媒体线上类型、接力内置表、控制协议类型、i18n 键，以及让旧代码继续编译的最小占位 | 先跑；打 `p2-base` 后其余才起 |
| T1 | 接力（发现、占用检测、恢复命令）、控制接口、核心变更、`ferry back` | 并行 |
| T2 | OpenCode 与 Qwen 服务驱动；注册表支持服务型 agent | 并行 |
| T3 | 发件箱 v2、唤醒通道、命令与存储变更、`ferry notify test` | 并行 |
| T4 | 微信媒体收发；假 iLink 服务的媒体能力 | 并行 |
| TΩ | 合流、装配、端到端测试、文档 | 最后 |

### 5.1 T0 细则

- 按 3.1、3.2、3.4（仅 types.go）、3.7（仅类型）、3.9、3.11 写代码与数据；按第 0 节核实结果写 `internal/handoff/profiles.go`。
- 接口变更会让旧实现编译失败：在 outbox 里给新方法加返回 `fsp.ErrUnsupported` 的占位，在 store 里给新方法加占位，在 core 的 Deps 里加新字段；在 handoff、notify、driver/server 包里写出 3.10 的签名。
- `go test ./...` 必须保持全绿（P1 的测试不能因为占位而失败）。
- 本轮不新增依赖。

### 5.2 T1 细则：接力、控制接口、核心

- **Discover**：对每个参与接力的 agent 并行调用驱动的 `List`，单个 agent 超时 10 秒。先不带目录调用；agent 报参数错误时，改为对每个工作区根目录及其一级子目录分别调用。只保留 cwd 位于工作区内、更新时间在回看期内、且未登记到 Ferry 的会话。
- **Busy**：
  - Linux：遍历 `/proc`，读取命令行与 `/proc/<pid>/cwd`；命令行匹配 `ProcessMatch` 且工作目录（解析符号链接后）等于会话目录，即视为占用。
  - macOS：用 `ps -axo pid=,ppid=,command=` 找匹配的进程，再用 `lsof -a -d cwd -p <pid> -Fn` 取工作目录。
  - 两个平台都排除守护进程自身及其后代进程（沿父进程链判断）。
  - Windows：返回 `fsp.ErrUnsupported`。
- **BackCommand**：把 `{id}` 替换成会话 ID；没有 Resume 时返回 `fsp.ErrUnsupported`。
- **控制接口**：按 3.4 实现服务端与客户端。
- **核心**：按 3.8 修改。
- **`cmd/ferry/back.go`**：按 3.4 实现，在 `init()` 里注册到 `rootCmd`。
- 测试至少包含：TestDiscoverFiltersRoots、TestDiscoverExcludesRegistered、TestDiscoverCache、TestDiscoverAgentTimeout、TestDiscoverFallbackPerRoot、TestBusyProcessInCwd（仅 Linux、macOS）、TestBusyIgnoresOwnDescendants、TestBackCommandTemplate、TestBackCommandUnsupported；TestControlTokenRequired、TestControlPark、TestControlStatus、TestControlFileMode；TestLsShowsTerminalSessions、TestUseAdoptsAndResumes、TestUseBusyRefused、TestUseBusyUnknown、TestUseForce、TestSharedSkipsBusy、TestServerDriverSelected、TestAttachmentsInPrompt、TestMediaErrors、TestPullAndFileCommands、TestIMOfflineWakes、TestParkViaControl；TestBackPrintsCommand、TestBackWithoutDaemon、TestBackUnsupportedExitCode。

### 5.3 T2 细则：服务驱动

- 按 3.5 实现 opencode 与 qwen 两个适配器，共用 SSE 读取、重连与权限映射代码（放在本包内部）。
- 测试用 httptest 写假 OpenCode 服务与假 Qwen 守护进程，路由与字段按 3.5（含第 0 节核实结果）。
- registry：对 `server` 型 agent 返回服务规格，`Agents()` 用健康检查接口判断可用性（超时 2 秒）。
- 测试至少包含：TestOpenCodeStartAndPrompt、TestOpenCodeEventsToChunks、TestOpenCodePermissionRoundTrip、TestOpenCodeAbort、TestOpenCodeList、TestOpenCodeAuth、TestQwenStartAndPrompt、TestQwenEvents、TestQwenPermission、TestQwenLoadOrResume、TestQwenWorkspaceMismatch、TestQwenBearer、TestSSEReconnect、TestSSEGiveUpAfter5Min（假时钟）、TestLoopbackOnly；TestResolveServerAgent、TestServerSecretFromEnv、TestServerAvailability。

### 5.4 T3 细则：发件箱 v2、唤醒、命令、存储

- 发件箱按 3.6 实现；存储实现 3.1 新增的方法与 002 迁移的读写。
- 唤醒通道：
  - ntfy：`POST <url>/<topic>`，请求头 `Title`，正文为 body。
  - Bark：`GET <url>/<key>/<标题>/<正文>`，路径段做 URL 转义。
  - Webhook：`POST` JSON `{kind, title, body, ts}`，按配置签名。
  - 邮件：`net/smtp` 走 STARTTLS。
  - 同一入口同类提醒间隔不小于 `min_interval`。
- 命令：`/use <n> force` 解析为 Args `["n", "force"]`；新增用法提示。
- `cmd/ferry/notify.go`：`ferry notify test <通道>`，在 `init()` 里注册。
- 测试至少包含：TestHeldWhenWindowExpired、TestWakeOnHeldUrgent、TestDigestOnFlush、TestFlushRespectsBudget、TestDigestMoreHint、TestPullNextBatch、TestPullNothing、TestLongOutputToFile、TestFileKeepLimit、TestFileResend、TestFileMissing、TestFileFallbackToSplit；TestNtfy、TestBark、TestWebhookSignature、TestEmailSMTP（本包内的最小假 SMTP 服务）、TestMinInterval、TestNoSensitiveContent；TestUseForceArg；TestMigration002、TestHoldOutbox、TestLongOutputsCRUD；TestNotifyTestCommand。

### 5.5 T4 细则：微信媒体

- 按 3.7 实现收发与假服务扩展。
- 测试至少包含：TestInboundImageDownloaded、TestInboundFileDownloaded、TestInboundVoiceTranscript、TestMediaTooLarge、TestUnsafeFileName、TestAESKeyVariants、TestSendFileUploadsAndSends、TestSendImageByExtension、TestUploadFailureReported、TestFullURLPreferred；TestFakeUploadAndCDN、TestFakePushImage、TestFakeSentFilesDecrypt。

### 5.6 TΩ 细则

- 合并顺序：T4 → T3 → T2 → T1。
- 装配：守护进程启动 `control.Serve`；创建 notify 的 Waker 并传给发件箱与核心；`Drivers` 加入服务驱动；创建 handoff 并传给核心；`ferry status` 优先经控制接口查询。
- 端到端：按 2.2 实现 B1–B11、B13；B4 用构建标签限定 Linux 与 macOS。
- 文档（中英两份，保持 check-i18n 通过）：`handoff.md` 写三档接力的用法与各 agent 支持情况表；`agents.md` 写国产 agent 预设、npm 镜像、OpenCode 与 Qwen 服务配置；`wechat.md` 补充附件、`/pull`、`/file`、唤醒通道；README 补一句接力说明。

## 6. 每个任务的验收

| 任务 | 必须通过（命令见各自提示词） |
| --- | --- |
| T0 | G1–G3、全量单测与 P1 端到端、check-i18n、迁移文件、新接口可文档化，共 8 项 |
| T1 | handoff、control、core、back 命令四组测试，重复 3 次稳定，G1–G3，共 8 项 |
| T2 | server 驱动与 registry 测试，重复 3 次稳定，G1–G3，共 5 项 |
| T3 | outbox、notify、command 与 store、notify 命令四组测试，G1–G3，共 7 项 |
| T4 | wechat 与假服务测试，重复 3 次稳定，G1–G3，共 5 项 |
| TΩ | A1–A3、B1–B14、C1–C2，共 19 项 |

## 7. 卡住了怎么办

见 00 总览 3.8。第 3.3、3.5 节里仍标「待核实」的项，执行者按「不支持」处理并在回执里列出，不要自行猜测。

## 8. 派工提示词

### P2-T0

````
# 任务 P2-T0 — P2 契约变更与占位

## 工作区
直接在 main 分支工作（当前应为 p1-done，且 spec-p2 第 0 节核实结果已提交）。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.1、3.2 节（现有接口）
- docs/dev/spec-p2.md 第 3 节全部、第 5.1 节

## 目标
把 spec-p2 第 3 节的接口、配置、迁移、线上类型、接力内置表、控制协议类型与 i18n 键写成代码，并用最小占位让现有代码继续编译、现有测试继续通过。

## 可写文件
spec-p2 第 3.13 节中 owner 为 T0 的路径；因接口变更而必须补占位的文件（outbox、store、core 的 Deps、handoff、notify、driver/server 包里只含签名或返回 fsp.ErrUnsupported 的代码）。

## 禁改
- docs/dev/**、LICENSE
- 不实现任何新功能，不新增依赖

## 验收（8 项）
1. `go build ./...` → 退出码 0
2. `go vet ./...` → 无输出
3. `test -z "$(gofmt -l .)"` → 退出码 0
4. `go test ./...` → 全部 ok
5. `go test ./test/e2e/ -v` → P1 的 11 个测试全部 PASS
6. `go run ./scripts/check-i18n` → `i18n ok`
7. `test -f internal/store/migrations/002_p2.sql` → 退出码 0
8. `go doc ./internal/ports Handoff && go doc ./internal/ports Waker` → 退出码 0

## 完成后
git add -A && git commit -m "p2-t0: freeze p2 contracts"

回执正文附 `git show --stat HEAD`，并列出 3.3、3.5 中仍为「待核实」的项。最后一行严格为：
RECEIPT P2-T0 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P2-T1

````
# 任务 P2-T1 — 终端接力、控制接口与核心变更

## 工作区
git fetch --tags
git worktree add -b p2-t1 ../ferry-p2-t1 p2-base
cd ../ferry-p2-t1

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.6 节（核心现有规则）
- docs/dev/spec-p2.md 第 3.1、3.3、3.4、3.8、3.10、3.11、3.12 节，第 5.2 节

## 目标
实现 internal/handoff（终端会话发现、占用检测、恢复命令）、internal/control（本机控制接口）、核心的接力与附件改动，以及 `ferry back`。

## 可写文件（只有这些）
- internal/handoff/**（profiles.go 除外）
- internal/control/**（types.go 除外）
- internal/core/**
- cmd/ferry/back.go

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/handoff/profiles.go、internal/control/types.go、locales/**、go.mod、go.sum
- 白名单以外的任何文件

驱动、发件箱、唤醒通道在你的工作区里只有占位实现，测试替身写在本包 _test.go 里。占用检测的测试用构建出的 ferry-fakeagent 进程。

## 验收（8 项）
1. `go test ./internal/handoff/... -v` → 全部 PASS，含 5.2 列出的 handoff 测试（Windows 以外）
2. `go test ./internal/control/... -v` → 全部 PASS，含 5.2 列出的 control 测试
3. `go test ./internal/core/... -v` → 全部 PASS，含 5.2 列出的核心测试，且 P1 的核心测试仍通过
4. `go test ./cmd/ferry/ -run TestBack -v` → 3 个测试 PASS
5. `go test ./internal/handoff/... ./internal/core/... -count=3` → ok
6. `go build ./...` → 退出码 0
7. `go vet ./...` → 无输出
8. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p2-t1: handoff control and core"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P2-T1 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P2-T2

````
# 任务 P2-T2 — OpenCode 与 Qwen 服务驱动

## 工作区
git fetch --tags
git worktree add -b p2-t2 ../ferry-p2-t2 p2-base
cd ../ferry-p2-t2

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.1 节（Driver 与 AgentSession 接口）
- docs/dev/spec-p2.md 第 3.1、3.2、3.5、3.10、3.12 节，第 5.3 节

## 目标
实现 internal/driver/server：让 Ferry 作为另一个客户端接入 `opencode serve` 与 `qwen serve`，与终端里的界面共享同一个会话；注册表支持服务型 agent。

## 可写文件（只有这些）
- internal/driver/server/**
- internal/driver/acp/**（仅在需要修复与服务驱动共用的问题时）
- internal/registry/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/fsp/**、go.mod、go.sum
- 白名单以外的任何文件

3.5 中标「待核实」的字段若仍未核实，按表中已确认的部分实现，其余用可配置的常量集中放置，并在回执中列出。

## 验收（5 项）
1. `go test ./internal/driver/server/... ./internal/registry/... -v` → 全部 PASS，含 5.3 列出的全部测试名
2. `go test ./internal/driver/server/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p2-t2: server drivers"

回执正文：每条验收命令的真实输出关键行；仍未核实的字段；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P2-T2 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P2-T3

````
# 任务 P2-T3 — 发件箱 v2 与唤醒通道

## 工作区
git fetch --tags
git worktree add -b p2-t3 ../ferry-p2-t3 p2-base
cd ../ferry-p2-t3

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 5.5 节（发件箱 v1 规则）
- docs/dev/spec-p2.md 第 3.1、3.2、3.6、3.9、3.10、3.11、3.12 节，第 5.4 节

## 目标
实现窗口外积压与摘要、/pull、长输出转文件与 /file、四种唤醒通道，以及相应的命令与存储变更和 `ferry notify test`。

## 可写文件（只有这些）
- internal/outbox/**
- internal/notify/**
- internal/command/**
- internal/store/**（migrations 除外）
- internal/config/**（types.go 除外）
- cmd/ferry/notify.go

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/store/migrations/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

所有外部服务（ntfy、Bark、Webhook、SMTP）一律用本包内的 httptest 或 net.Listen 替身，不访问外网。

## 验收（7 项）
1. `go test ./internal/outbox/... -v` → 全部 PASS，含 5.4 列出的发件箱测试，且 P1 的发件箱测试仍通过
2. `go test ./internal/notify/... -v` → 全部 PASS，含 5.4 列出的唤醒测试
3. `go test ./internal/command/... ./internal/store/... ./internal/config/... -v` → 全部 PASS
4. `go test ./cmd/ferry/ -run TestNotifyTestCommand -v` → PASS
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p2-t3: outbox v2 and notifiers"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P2-T3 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P2-T4

````
# 任务 P2-T4 — 微信媒体收发

## 工作区
git fetch --tags
git worktree add -b p2-t4 ../ferry-p2-t4 p2-base
cd ../ferry-p2-t4

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.2、3.4 节（线上格式与假服务）
- docs/dev/spec-p2.md 第 3.1、3.2、3.7、3.12 节，第 5.5 节

## 目标
让微信适配器能收图片、文件、语音（下载解密到本地）并能发文件与图片；假 iLink 服务同步支持上传地址与 CDN。

## 可写文件（只有这些）
- internal/im/wechat/**
- internal/testkit/fakeilink/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/ilinkwire/**、internal/config/types.go、go.mod、go.sum
- 白名单以外的任何文件

加密与地址细节以 openilink-sdk-go（MIT）的 cdn.go 为准；复制代码时保留其版权声明。

## 验收（5 项）
1. `go test ./internal/im/wechat/... ./internal/testkit/fakeilink/... -v` → 全部 PASS，含 5.5 列出的全部测试名，且 P0、P1 的已有测试仍通过
2. `go test ./internal/im/wechat/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p2-t4: wechat media"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P2-T4 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P2-TΩ

````
# 任务 P2-TΩ — 合流、装配与端到端测试

## 工作区
main 分支，当前应位于标签 p2-base。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p2.md 全文，重点第 2 节、第 3.13 节、第 5.6 节

## 第一步：合流
按顺序合并：p2-t4 → p2-t3 → p2-t2 → p2-t1。
一次合一个：`git merge --no-ff <分支>`，合完立刻跑 `go build ./... && go test ./...`，全绿再合下一个。
冲突按 3.13 归属表判定；表里没写的，停下来报告。合流阶段只修集成缝隙，不补漏掉的功能。

## 第二步：装配、端到端与文档
按 5.6 完成装配，实现 test/e2e 的 B1–B11、B13，更新 5.6 列出的文档。

## 可写文件
- 合流时：解决冲突所需的文件
- 第二步：spec-p2 第 3.13 节中 owner 为 TΩ 的路径

## 禁改
- docs/dev/**、LICENSE、internal/ports/**、go.mod、go.sum

## 全量验收（19 项）
1. `go build ./... && go test ./...` → 全部 ok
2. `go run ./cmd/ferry back --help` → 含 `--exec` 与 `--slot`
3. `go run ./cmd/ferry --help` → 含 back 与 notify
4–16. `go test ./test/e2e/ -run <名称> -v`，名称依次为 TestTerminalDiscovery、TestAdoptAndContinue、TestBackCommand、TestBusyRefused、TestServerOpenCode、TestServerQwen、TestHeldAndDigest、TestPull、TestLongOutputFile、TestWakeNotifier、TestInboundImage、TestReplyBudget、TestRetryDedupe → 每个都 PASS
17. `go run ./scripts/check-i18n` → `i18n ok`
18. `go test ./test/e2e/ -v` → 全部 PASS（含 P1 的 11 个）
19. `go vet ./... && test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p2-tomega: wire handoff delivery media"
不要推送，不要打标签。

回执正文：每次合入后的验收结果；冲突与判定依据；19 项验收的真实输出关键行；还差什么。最后一行严格为：
RECEIPT P2-TΩ status=<done|blocked> merged=<合入数>/4 checks=<通过数>/19
````

## 9. 人工验收清单（真实微信 + 真实 agent）

| # | 操作 | 期望 | 对应 |
| --- | --- | --- | --- |
| 1 | 在 `~/code/demo` 终端里运行 `kimi`，聊两句后退出；微信发 `/ls` | 出现标注「终端」的 Kimi 会话 | A10 |
| 2 | 微信 `/use <n>` 后接着上文提问 | 回答能接上终端里的上下文 | A10 |
| 3 | 回到电脑执行 `ferry back`，再执行打印出的命令 | 打印 `kimi --resume <id>`；终端里能看到微信里的对话。看不到时，把 Kimi 在 3.3 表里标为「一档不可用」 | A10 |
| 4 | 终端里的 `qwen` 不退出，微信 `/use` 它 | 收到占用提示，没有接管；退出终端后再 `/use` 成功 | A11 |
| 5 | `opencode serve --port 4096`，另开终端 `opencode attach http://127.0.0.1:4096`；配置 `opencode-live`；微信 `/new opencode-live ~/code/demo` 后发消息 | 终端界面实时出现微信发来的这一轮 | A12 |
| 6 | `qwen serve --workspace ~/code/demo`，浏览器打开 `http://127.0.0.1:4170`；配置 `qwen-live` 并在微信里使用 | Web Shell 里看到同一个会话 | — |
| 7 | 微信发一张截图并问「图里是什么报错」 | agent 读到图片路径并回答 | — |
| 8 | 让 agent 输出一份很长的 diff；再发 `/file 1` | 手机收到摘要和文件；再次收到同一文件 | — |
| 9 | 临时把 `window_ttl` 设为 `10m`；派一个任务后 11 分钟不发消息，再发任意消息 | 先收到积压摘要，再收到结果 | A13 |
| 10 | 配置 ntfy 并在手机装 ntfy 应用；`ferry notify test ntfy`；再制造一个窗口外的审批 | 手机收到两条提醒，正文不含命令或路径 | — |
| 11 | 让 agent 连续输出很多段 | 同一条消息对应的回复不超过 8 条 | A14 |
| 12 | 发送过程中短暂断网再恢复 | 手机上没有重复消息 | A15 |

全部通过后：把第 3、4 项的结果补进 3.3 表并提交；`git push origin main`，确认 CI 全绿；`git tag p2-done v0.2.0 && git push origin p2-done v0.2.0`。
