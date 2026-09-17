# 开发派工 Spec — Ferry P3 扩展（附 P4 发行清单）

> 生成于 2026-09-17。本轮唯一权威文档，另须遵守 `docs/dev/00-overview.md` 第 3 节。
> 前置条件：`p2-done` 已在 `main`；第 0 节的人工准备已完成。冻结后不再修改。

## 0. 已核实的事实与开工前准备

**已核实（2026-09-17）**

| 项 | 结论 |
| --- | --- |
| 飞书 Go SDK | 模块 `github.com/larksuite/oapi-sdk-go/v3`，最新标签 v3.12.0；仓库含 `ws` 包（WebSocket 长连接客户端），README 说明其 Channel 模块基于 WebSocket，提供事件监听、消息规范化与媒体上传 |
| golang.org/x/term | 最新标签 v0.46.0 |
| MCP Go SDK | v1.8.0 源码中有 2025-06-18、2025-11-25 等协议版本常量；mcpmini 支持的版本以该源码为准 |
| Claude Code Channels | 字段见 3.5，依据官方 channels reference；自定义 channel 在研究预览期间需要 `--dangerously-load-development-channels`；Channels 需要 Anthropic 官方身份验证（claude.ai 账号或 Console API key），Bedrock、Vertex AI、Foundry 上不可用 |
| ACP v2 | 流程见 3.7，依据 agentclientprotocol.com 的 v2 概览；v2 仍在演进 |

**开工前准备（人工，约 30 分钟）**

1. 飞书开放平台建一个企业自建应用：开启机器人能力；事件订阅选择长连接方式，订阅接收消息事件；开通读取与发送单聊消息的权限；记下 App ID 与 App Secret；在飞书里找到自己的 open_id。
2. 重新阅读 ACP v2 概览与各方法页面；与 3.7 不一致的地方，先改本文件再发 T0。
3. 查阅 Claude Code 官方插件文档，确认插件清单格式与 `--dangerously-load-development-channels` 的参数写法，写回 3.5 的「待核实」处。
4. 准备人工验收工具：Zed（External Agents 配置），或终端里的 Toad。

## 1. 问题定义

**要做什么**：

1. **飞书入口**：同一个守护进程同时服务微信与飞书，会话按入口隔离，命令与审批体验一致。
2. **`ferry proxy <agent>`**：给 Zed、JetBrains、VS Code 扩展、Toad 等 ACP 客户端做代理，会话同时镜像到 IM；可选允许 IM 输入与 IM 审批。
3. **Claude Code 可选插件**：`ferry channel` 薄壳（Channels 协议），以及把第三方 channel server 当作 IM 入口的兼容模式。
4. **ACP v2 适配**：按 agent 配置选择 v1、v2 或自动协商。
5. **`ferry run <命令>`**：用伪终端包住没有 ACP 的 CLI（实验功能，本轮仅 Linux、macOS）。

**为什么**：飞书是第二入口，降低对 iLink 的单点依赖；proxy 让任意 ACP agent 在编辑器里和 IM 同时在线；Claude Code 插件与兼容模式覆盖有 Anthropic 账号的用户和现有 channel 生态；v2 与 PTY 扩大可接入的 agent 范围。

**明确不做**：

- 钉钉、企业微信、Telegram 入口
- 飞书交互卡片审批（本轮用文本审批，卡片放 P4）
- 向第三方 channel server 转发审批请求
- Windows 上的 `ferry run`（go-pty 支持 ConPTY，放 P4）

## 2. 验收标准（DoD）

### 2.1 能跑

- **A1** `go build ./... && go test ./...` → 全部 ok
- **A2** `go run ./cmd/ferry --help` → 在 P2 基础上多出 proxy、channel、run；`go run ./cmd/ferry login --help` 列出 wechat 与 feishu
- **A3** `go run ./cmd/ferry proxy --help`、`go run ./cmd/ferry run --help`、`go run ./cmd/ferry channel --help` → 都显示用法，退出码 0

### 2.2 行为正确

端到端测试由 TΩ 写在 `test/e2e/`（B7 的测试用 channel server 由 T3 提供）。

- **B1** `go test ./test/e2e/ -run TestFeishuFake -v` → 用 `feishutest` 的假客户端：飞书私聊 `/new fake <ws>/demo` 并发 `echo hi`，飞书侧收到回执与结果；微信侧同时可用；两个入口的 `/ls` 互相看不到对方的会话
- **B2** `-run TestFeishuApproval` → 飞书侧 `ask execute <ws>/demo/run.sh` 产生文本审批；`y <ID>` 后 agent 回复 `allowed`
- **B3** `-run TestProxyMirror` → 测试扮演编辑器（acp-go-sdk 客户端）连接 `ferry proxy fake`；编辑器发 `echo hi` 并收到流式输出；微信 owner 收到 `proxy.attached` 与镜像输出
- **B4** `-run TestProxyInject` → 带 `--allow-inject`：会话空闲时微信发 `echo x`，agent 收到；编辑器收到一条内容为 `echo x` 的 `user_message_chunk` 和随后的输出
- **B5** `-run TestProxyApprovalModes` → 默认模式下 `ask execute …` 的权限请求只转给编辑器，微信只收到 `proxy.editor_approval`；带 `--remote-approve` 时权限请求只发到微信，由微信裁决
- **B6** `-run TestChannelShim` → 测试扮演 Claude Code（mcpmini 客户端）启动 `ferry channel`：微信 owner 的消息以 `notifications/claude/channel` 到达，meta 键只含字母、数字、下划线；测试调用 `reply` 工具后假网关记录到回复；测试发送 `notifications/claude/channel/permission_request`（request_id 为 `abcde`），微信收到 ID 为 `abcde` 的审批，回复 `y abcde` 后测试收到 `notifications/claude/channel/permission`，behavior 为 `allow`
- **B7** `-run TestChannelHost` → 配置 `test/e2e/testdata/echo-channel` 为入口：它发出的 `notifications/claude/channel` 进入会话；agent 的回复经它的 `reply` 工具送回
- **B8** `-run TestACPv2` → 假 agent 以 v2 模式运行：新建、发送、以空闲 state_update 结束本轮、列表、恢复、关闭全部走 v2 方法
- **B9** `-run TestPTYRun`（仅 Linux、macOS）→ `ferry run -- sh -c 'while read l; do echo got:$l; done'` 登记为会话；微信发 `hello` 后收到含 `got:hello` 的回复
- **B10** `go test ./spec/conformance/... -v` → mcpmini 与 go-sdk v1.8.0 在 initialize、tools/list、tools/call、通知上的行为一致
- **B11** `go run ./scripts/check-i18n` → `i18n ok`

### 2.3 没弄坏

- **C1** `go test ./test/e2e/ -v` → P1、P2 的全部端到端测试与本轮新增测试都 PASS
- **C2** 00 总览 3.5 的 G1–G3 → 全部通过

## 3. 冻结契约

### 3.1 多入口：接口与核心变更

```go
// internal/ports：IMAdapter 新增 Limits
type DeliveryLimits struct {
	NeedsWindow bool          // true：只能在入站消息开启的窗口内回复（微信）
	WindowTTL   time.Duration // 为 0 时使用 outbox.window_ttl
	ReplyBudget int           // 为 0 且 NeedsWindow 为 true 时使用 outbox.reply_budget；NeedsWindow 为 false 时 0 表示不限
	RatePerSec  float64       // 发送速率上限，0 表示不限
}

type IMAdapter interface {
	// P1 的方法保持不变，新增：
	Limits() DeliveryLimits
}
```

- 平台取值：`wechat`、`feishu`、`channel-<名称>`（兼容模式）。
- `core.Deps` 的 `IM ports.IMAdapter` 改为 `IMs []ports.IMAdapter`；`outbox.Options` 的 `IM` 改为 `IMs map[fsp.Platform]ports.IMAdapter`，按 `msg.Endpoint.Platform` 选择适配器，并按其 `Limits()` 决定是否需要窗口、额度与速率。
- 启动时核心对每个适配器调用 `ACL.SetOwner`（owner 取自 `Status().OwnerID`）。
- 外部会话（proxy、channel、pty）绑定到 `external.bind_platform` 指定平台的 owner 入口，默认 `wechat`。
- T0 给微信适配器补一个 `Limits()`，返回 `DeliveryLimits{NeedsWindow: true}`。

### 3.2 配置变更 `internal/config/types.go`

```go
type Config struct {
	// P1、P2 字段保持不变，新增：
	Feishu         FeishuConfig          `toml:"feishu"`
	ChannelServers []ChannelServerConfig `toml:"channel_servers"`
	External       ExternalConfig        `toml:"external"`
}

type FeishuConfig struct {
	Enabled     bool     `toml:"enabled"`
	AppID       string   `toml:"app_id"`
	OwnerOpenID string   `toml:"owner_open_id"`
	Approvers   []string `toml:"approvers"` // 默认 ["owner"]
	Domain      string   `toml:"domain"`    // feishu | lark，默认 feishu
	// App Secret 由 ferry login feishu 存入钥匙串，不写进配置文件
}

type ChannelServerConfig struct {
	Name       string            `toml:"name"`         // 平台名为 channel-<name>
	Command    []string          `toml:"command"`
	Env        map[string]string `toml:"env"`
	Owner      string            `toml:"owner"`        // 视为 owner 的发送者 ID；缺省 local
	UserIDMeta string            `toml:"user_id_meta"` // 从哪个 meta 键取发送者 ID，默认 user_id
	ChatIDMeta string            `toml:"chat_id_meta"` // 从哪个 meta 键取回复目标，默认 chat_id
	ReplyTool  string            `toml:"reply_tool"`   // 默认 reply
}

type ExternalConfig struct {
	BindPlatform string `toml:"bind_platform"` // 默认 wechat
}

type AgentConfig struct {
	// P1、P2 字段保持不变，新增：
	ACPVersion string `toml:"acp_version"` // v1 | v2 | auto，默认 v1
}
```

校验：启用飞书时 `app_id` 与 `owner_open_id` 必填；channel server 名匹配 `^[a-z0-9-]{1,24}$` 且唯一；`bind_platform` 必须是已启用的平台。

### 3.3 本机控制接口扩展 `internal/control`

`types.go` 与 `client.go` 由 T0 完整实现（它们是 T1–T4 共用的协议管道）；`server.go` 由 T1 实现。

```go
// 新增 op：attach。成功后该连接进入流式模式，双方随时互发 Message。
type AttachRequest struct {
	Kind           string `json:"kind"` // proxy | channel | pty
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Cwd            string `json:"cwd"`
	Title          string `json:"title,omitempty"`
	PID            int    `json:"pid"`
	AllowInject    bool   `json:"allow_inject"`
	RemoteApprove  bool   `json:"remote_approve"`
}

type AttachResponse struct {
	SessionID string `json:"session_id"`
	Slot      int    `json:"slot"`
	Endpoint  string `json:"endpoint"`
}

type Message struct {
	Type      string               `json:"type"`
	SessionID string               `json:"session_id,omitempty"`
	ReqID     string               `json:"req_id,omitempty"` // 审批请求与裁决的对应关系
	Event     *fsp.Event           `json:"event,omitempty"`
	Approval  *fsp.ApprovalRequest `json:"approval,omitempty"`
	Verdict   *fsp.Verdict         `json:"verdict,omitempty"`
	Text      string               `json:"text,omitempty"`
	Sender    string               `json:"sender,omitempty"`
	Key       string               `json:"key,omitempty"`
	Value     string               `json:"value,omitempty"`
}

// 客户端（T0 实现）
func (c *Client) Attach(ctx context.Context, req AttachRequest) (*Stream, AttachResponse, error)

type Stream struct{ /* T0 定义 */ }

func (s *Stream) Send(m Message) error
func (s *Stream) Recv() (Message, error) // 连接断开时返回 io.EOF
func (s *Stream) Close() error

// 服务端（T1 实现）：Handler 新增方法
type ServerStream interface {
	Send(m Message) error
	Recv() (Message, error)
}
// Handler.Attach(ctx context.Context, req AttachRequest, s ServerStream) (AttachResponse, error)
```

消息类型：

| 方向 | type | 含义 |
| --- | --- | --- |
| 外部 → 守护进程 | `event` | 会话事件：输出、工具、计划、轮次结束、错误 |
| 外部 → 守护进程 | `approval_request` | 请求由 IM 审批，带 ReqID |
| 外部 → 守护进程 | `approval_cancel` | 撤回某个审批请求 |
| 外部 → 守护进程 | `detach` | 会话结束 |
| 守护进程 → 外部 | `prompt` | IM 发来的输入（Text、Sender） |
| 守护进程 → 外部 | `verdict` | 审批结果，带 ReqID |
| 守护进程 → 外部 | `cancel` | 取消当前轮次 |
| 守护进程 → 外部 | `config` | 设置配置项（Key、Value） |
| 守护进程 → 外部 | `close` | 关闭会话 |
| 双向 | `ping` / `pong` | 每 15 秒一次；30 秒收不到任何消息视为断开 |

`fsp.Event` 等类型用 `encoding/json` 的默认字段名序列化。

**核心中的外部会话**（T1）：把流包装成 `ports.AgentSession`：`Prompt` 发 `prompt`，`Cancel` 发 `cancel`，`SetConfig` 发 `config`，`Close` 发 `close`，`Events` 来自 `event` 消息。收到 `approval_request` 时调用 `Approvals.Ask`，结果以 `verdict` 发回。`AllowInject` 为 false 时，IM 输入回复 `inject.disabled`；为 true 时只在会话空闲时发送 `prompt`，忙时进入 P1 的待发队列。连接断开或收到 `detach` 时，会话置为 closed，回复 `session.external_closed`。外部会话的 origin 为 terminal，驱动种类分别为 proxy、claude-channel、pty。

### 3.4 飞书适配器 `internal/im/feishu`（T1）

```go
// Client 封装飞书 SDK，便于测试替换。
type Client interface {
	Start(ctx context.Context, onMessage func(Message)) error // 长连接，阻塞到 ctx 结束
	SendText(ctx context.Context, openID, text string) (messageID string, err error)
	SendFile(ctx context.Context, openID, path string) (messageID string, err error)
	Ping(ctx context.Context) error // 用于登录校验
}

type Message struct {
	MessageID, ChatType, OpenID, Text string // ChatType：p2p | group
	CreateTime time.Time
}

type Options struct {
	Config config.FeishuConfig
	Secret string // App Secret
	Home   string
	Clock  clock.Clock
	Log    *slog.Logger
}

func New(opts Options) (*Adapter, error)                           // 使用真实 SDK（oapi-sdk-go v3.12.0 的 ws 包）
func NewWithClient(opts Options, c Client) (*Adapter, error)       // 测试用
func LoginInteractive(ctx context.Context, in io.Reader, out io.Writer, home string) error
// 包 internal/im/feishu/feishutest 提供实现 Client 的假客户端，供 e2e 使用。
```

行为：

- 本轮只处理单聊（p2p），群消息忽略。
- Inbound：Endpoint 为 `feishu`、AppID、发送者 open_id；MessageID 为消息 ID；Text 取文本消息内容；ReplyCtx 为消息 ID。
- `Limits()` 返回 `DeliveryLimits{NeedsWindow: false, RatePerSec: 5}`。
- Send：文本按 open_id 发送单聊消息；OutFile 先上传再发送文件消息；`Typing` 返回 nil。
- 状态：长连接断开超过 5 分钟视为离线；`Status().OwnerID` 为 `owner_open_id`。
- `LoginInteractive` 依次询问 App ID、App Secret、owner open_id，调用 `Ping` 校验后，把 Secret 存入钥匙串（平台 feishu），并把其余两项写入配置。

### 3.5 Claude Code 可选插件（T3）

**`ferry channel`（薄壳）** 是一个 stdio MCP server，由 Claude Code 启动，经控制接口以 `attach(kind=channel)` 接入守护进程。协议字段必须与官方 channels reference 一致，不得改名：

| 项 | 规定 |
| --- | --- |
| 能力声明 | `capabilities.experimental` 含 `claude/channel` 与 `claude/channel/permission`（只有做了发送者认证的 channel 才能声明后者；Ferry 的 ACL 满足此条件）；`capabilities.tools` 为空对象 |
| instructions | 固定英文说明：消息以 `<channel source="ferry" ...>` 形式到达；用 `reply` 工具回复并传回标签里的 `chat_id`；回复保持简短，对方在手机上阅读 |
| 入站事件 | 通知 `notifications/claude/channel`，参数 `content`（字符串）与 `meta`（字符串到字符串的映射）；meta 键只能含字母、数字、下划线，含连字符的键会被静默丢弃 |
| 约定的 meta 键 | `chat_id`、`message_id`、`user`、`user_id`、`platform`、`ts` |
| 工具 | `reply`，参数 `chat_id`（字符串）、`text`（字符串）、`files`（可选，绝对路径数组）；`chat_id` 不是已知入口时返回工具错误 |
| 审批请求（宿主 → channel） | 通知 `notifications/claude/channel/permission_request`，参数 `request_id`（5 个小写字母，a–z 去掉 l）、`tool_name`、`description`、`input_preview` |
| 审批结果（channel → 宿主） | 通知 `notifications/claude/channel/permission`，参数 `request_id` 与 `behavior`（`allow` 或 `deny`） |
| 中继规则 | 本地对话框与远程并行，先到先得；ID 不匹配的裁决被静默丢弃；只覆盖工具调用审批 |

薄壳行为：

- IM 输入（`prompt` 消息）转成 `notifications/claude/channel`。
- `reply` 调用转成 `event` 消息：先发 `agent.chunk`，再发 `agent.turn_end`，由发件箱作为紧急消息发出。
- `permission_request` 转成 `approval_request`，其中 `Approval.ID` 直接使用 `request_id`，ToolName、Description、InputPreview 一一对应。收到 `verdict` 后：允许类为 `allow`，拒绝或取消为 `deny`。
- 本地对话框先处理时，微信里的审批会一直等到超时，届时发出的裁决会被 Claude Code 丢弃；这是协议限制，在文档里说明。
- 守护进程未运行时，薄壳正常完成 MCP 握手，但不声明 `claude/channel/permission`，并通过 instructions 告知模型当前不可用。

**审批模块变更**：请求自带的 `ID` 符合 `^[a-km-z]{5}$` 且不与未决审批冲突时沿用它，否则照旧分配新 ID。

**插件清单** `plugin/`：按 Claude Code 官方插件文档的格式声明一个名为 ferry 的 MCP server，命令为 `ferry channel`（清单格式待核实，见第 0 节）。开发阶段的使用方式：在 `.mcp.json` 加入 `{"mcpServers": {"ferry": {"command": "ferry", "args": ["channel"]}}}`，然后用 `claude --dangerously-load-development-channels server:ferry` 启动（参数写法待核实）。

**兼容模式** `internal/im/channelhost`：按 `channel_servers` 配置启动第三方 channel server（mcpmini 客户端），把它当作一个 IM 入口：

- 收到 `notifications/claude/channel` → Inbound：Endpoint 为 `channel-<name>`、名称、`meta[chat_id_meta]`；SenderID 取 `meta[user_id_meta]`，没有时为 `local`；Text 为 content；MessageID 取 `meta.message_id`，没有时生成。
- Send → `tools/call`，工具名为 `reply_tool`，参数 `chat_id` 与 `text`。
- `Limits()` 返回 `DeliveryLimits{NeedsWindow: false}`；`Status().OwnerID` 为配置的 `owner`。
- 本轮不向第三方 server 转发审批请求。
- server 进程退出后每 60 秒重启一次，最多 5 次；之后置为离线。

**mcpmini** `internal/mcpmini`：JSON-RPC 2.0，stdio 逐行分帧；支持客户端与服务端两种角色；支持 initialize 与版本协商（支持的版本取自 go-sdk v1.8.0 源码中的常量，至少包含 2025-06-18 与 2025-11-25）、`notifications/initialized`、`tools/list`、`tools/call`、`ping`、双向自定义通知；收到 `notifications/cancelled` 时忽略。

**一致性测试** `spec/conformance`：同一组测试分别用 mcpmini 与 go-sdk v1.8.0 做对端，覆盖 initialize、tools/list、tools/call 与自定义通知。

### 3.6 `ferry proxy`（T2）

用法：`ferry proxy [--allow-inject] [--remote-approve] [--title <名称>] <agent>`

- 由编辑器以 stdio 启动。读取 `FERRY_HOME` 下的配置，用注册表解析 agent 并启动它。
- 用 acp-go-sdk 同时扮演两侧：对编辑器是 agent，对真实 agent 是客户端。逐一转发 SDK 支持的全部方法与通知，会话 ID 原样透传；SDK 不认识的扩展方法返回 method not found。
- 每个 `session/new` 或 `session/load` 成功后，以 `attach(kind=proxy)` 登记一个外部会话（一个进程可登记多个会话，共用一条控制连接，消息用 SessionID 区分）。
- **镜像**：agent 发给编辑器的 `session/update` 同时转成 `event` 发给守护进程；编辑器发起新一轮时额外发一个 EvChunk，内容为 `proxy.editor_turn` 对应的文本标记（由守护进程本地化）。
- **IM 输入**（`--allow-inject`）：收到 `prompt` 时，若编辑器没有进行中的轮次，proxy 先向编辑器发一条 `user_message_chunk`（内容为该输入），再以自己的身份向 agent 发 `session/prompt`，并把输出同时转给编辑器与守护进程；这期间编辑器发起的 prompt 排队，等本轮结束再发。
- **审批**：默认模式下权限请求只转给编辑器，同时发一个 EvTool（Status 为 `awaiting_editor`），守护进程据此回复 `proxy.editor_approval`；`--remote-approve` 模式下不转给编辑器，改发 `approval_request`，按 `verdict` 回复 agent。
- 守护进程未运行时透明转发，并在 stderr 打印 `cli.proxy_no_daemon`。
- 编辑器断开时取消进行中的轮次，关闭 agent，发送 `detach`。

### 3.7 ACP v2（T4）

依据 v2 概览：

1. `initialize` 建立连接；agent 返回非空 `authMethods` 时，必须实现 `auth/login` 与 `auth/logout`，客户端在需要时调用 `auth/login`。
2. 会话建立：`session/new` 或 `session/resume`。
3. 轮次：客户端发 `session/prompt`，agent 在输入被受理后即返回响应；随后以 `session/update` 通知送出已受理的消息、状态更新与进度；需要时发权限请求；客户端可发 `session/cancel` 通知中断；agent 就绪时发送空闲的 `state_update`，前台工作结束时附带停止原因。
4. 其他基础方法：`session/list`、`session/close`。

实现要求：

- `internal/driver/acp/v2`：不依赖 SDK，自行实现上述 JSON-RPC 方法，字段名以 v2 规范页面为准。
- 空闲 `state_update` 映射为 EvTurnEnd（带停止原因）；`Resume` 用 `session/resume`；`List` 用 `session/list`；`Close` 先发 `session/close`。
- `auth/login` 只自动执行无需用户交互的方式；需要交互时启动失败，回复 `acp.login_required`。
- `acp_version = "auto"`：先以 v2 发 `initialize`，agent 协商回 v1 时关闭进程，改用 v1 路径重新启动。
- 假 agent 新增 `--acp-version 2` 模式，按同一规范实现，行为与 P0 的 v1 指令表一致。

### 3.8 `ferry run`（T4）

用法：`ferry run [--name <名称>] [--idle <毫秒>] [--prompt-regex <正则>] -- <命令> [参数...]`

- 用 go-pty 在伪终端里启动命令；用 x/term 把用户终端切到原始模式，双向转发；窗口大小变化时同步。
- 以 `attach(kind=pty)` 登记外部会话；守护进程未运行时只做包装，不接入。
- **注入**：收到 `prompt` 后，等到连续 `idle` 毫秒（默认 1500）没有输出、且设置了 `prompt-regex` 时最后一行匹配，再以括号粘贴（`ESC[200~` 文本 `ESC[201~`）写入并回车；用户在 2 秒内有按键时推迟注入。
- **输出**：注入后收集输出直到再次空闲；去掉 ANSI 控制序列、回车覆盖与回显的输入，作为 EvChunk 加 EvTurnEnd 发出。
- 不支持审批中继：会话登记时守护进程回复 `pty.no_approval`。
- 子进程退出时 `ferry run` 以相同退出码退出，并发送 `detach`。
- Windows 返回不支持。

### 3.9 i18n 新增键（T0 加齐中英两份）

| 键 | 中文 |
| --- | --- |
| `proxy.attached` | 编辑器会话已接入：{{.Agent}} · {{.Cwd}}（会话 {{.Slot}}） |
| `proxy.editor_turn` | （编辑器发起了新一轮） |
| `proxy.editor_approval` | 编辑器里有待处理的审批：{{.Tool}} |
| `channel.attached` | Claude Code 已接入（会话 {{.Slot}}） |
| `pty.attached` | 终端程序已接入：{{.Name}}（会话 {{.Slot}}） |
| `pty.no_approval` | 这是包装运行的终端程序，审批请在电脑上处理 |
| `inject.disabled` | 这个会话没有开启 IM 输入，只能查看输出 |
| `session.external_closed` | 会话 {{.Slot}} 已结束（{{.Kind}} 已退出） |
| `acp.login_required` | {{.Agent}} 需要先登录，请在电脑终端里用它完成登录 |
| `feishu.offline` | 飞书连接已断开，正在重试 |
| `cli.proxy_no_daemon` | 守护进程未运行，本次只做透明转发，不会同步到 IM |
| `cli.feishu_app_id` / `cli.feishu_app_secret` / `cli.feishu_owner` | 请输入 App ID / 请输入 App Secret / 请输入你的 open_id |
| `cli.feishu_ok` | 飞书登录成功，应用：{{.App}} |
| `cli.run_unsupported` | 当前系统暂不支持 ferry run |

### 3.10 失败面

| 场景 | 行为 |
| --- | --- |
| 飞书长连接断开 | SDK 自动重连；超过 5 分钟未恢复时置为离线，并对 owner 调用 `Waker.Wake(im_offline)` |
| 飞书发送被限流 | 按 1、2、4 秒退避，最多重试 3 次 |
| proxy 连不上守护进程 | 透明转发，打印 `cli.proxy_no_daemon` |
| 编辑器断开 | 见 3.6 |
| 控制连接断开 | 外部会话置为 closed，回复 `session.external_closed` |
| `reply` 工具的 chat_id 未知 | 返回工具错误，不发送 |
| 第三方 channel server 退出 | 见 3.5 |
| 调用第三方 `reply` 工具失败 | 消息置为 rejected，记审计 |
| v2 agent 需要交互式登录 | 启动失败，回复 `acp.login_required` |
| v2 协商结果为 v1 | `auto` 模式回退 v1；`v2` 模式报错 |
| PTY 子进程退出 | `ferry run` 以相同退出码退出，会话 closed |
| 注入时用户正在输入 | 推迟到用户停止输入 2 秒后 |

### 3.11 文件归属表

| 路径 | owner | 其他任务 |
| --- | --- | --- |
| `internal/ports/**`、`internal/config/types.go`、`internal/control/types.go`、`internal/control/client.go`、`internal/store/migrations/**`、`locales/*.toml`、`go.mod`、`go.sum`、`internal/deps/**`、`internal/im/wechat/limits.go` | T0 | 只读，改动即任务失败 |
| `internal/im/feishu/**`、`internal/core/**`、`internal/outbox/**`、`internal/control/**`（types.go、client.go 除外） | T1 | 不可改 |
| `internal/driver/proxy/**`、`cmd/ferry/proxy.go` | T2 | 不可改 |
| `internal/mcpmini/**`、`internal/driver/claudechannel/**`、`internal/im/channelhost/**`、`internal/approval/**`、`plugin/**`、`spec/conformance/**`、`cmd/ferry/channel.go`、`test/e2e/testdata/echo-channel/**` | T3 | 不可改 |
| `internal/driver/acp/**`、`internal/driver/pty/**`、`internal/testkit/fakeagent/**`、`cmd/ferry-fakeagent/**`、`cmd/ferry/run.go` | T4 | 不可改 |
| `cmd/ferry/**`（上述文件除外）、`test/e2e/**`（echo-channel 除外）、`README.md`、`README.zh-CN.md`、`docs/en/**`、`docs/zh-CN/**` | TΩ | 不可改 |
| 其余已有产物 | 无 | 只读 |
| `docs/dev/**`、`LICENSE` | 人工 | 只读 |

## 4. 基线

- T0 起点：`main` 上的 `p2-done`，工作区干净，第 0 节准备完成。
- 并行基线：标签 `p3-base`。

## 5. 任务表

| 任务 | 目标 | 时序 |
| --- | --- | --- |
| T0 | 接口与配置变更、控制协议类型与客户端、依赖、i18n 键、各新包签名 | 先跑；打 `p3-base` 后其余才起 |
| T1 | 飞书适配器；核心与发件箱的多入口和外部会话；控制接口服务端 | 并行 |
| T2 | `ferry proxy` | 并行 |
| T3 | mcpmini、`ferry channel`、兼容模式、审批 ID 沿用、插件清单、一致性测试 | 并行 |
| T4 | ACP v2、`ferry run`、假 agent 的 v2 模式 | 并行 |
| TΩ | 合流、装配、端到端测试、文档 | 最后 |

### 5.1 T0 细则

- 按 3.1、3.2、3.3（types.go 与 client.go 完整实现）、3.9 写代码。
- 依赖：加入 `github.com/larksuite/oapi-sdk-go/v3 v3.12.0`、`github.com/modelcontextprotocol/go-sdk v1.8.0`、`github.com/aymanbagabas/go-pty v0.2.3`、`golang.org/x/term v0.46.0`，在 `internal/deps` 里空白导入后 `go mod tidy`。
- 在 feishu、proxy、claudechannel、channelhost、mcpmini、pty、acp/v2 各包写出签名；给微信适配器写 `limits.go`；把核心与发件箱的 `IM` 字段改为 3.1 的形式，并做让现有测试继续通过的最小修改。
- 控制客户端写单元测试（用本包内的假服务端）。

### 5.2 T1 细则

- 飞书适配器按 3.4；真实 SDK 部分只做薄封装，逻辑全部经 `Client` 接口测试；提供 `feishutest` 假客户端。
- 核心：多入口、每个平台的 owner、外部会话（3.3）、`pty.no_approval` 与各类接入提示。
- 发件箱：按平台的 `Limits()` 处理窗口、额度与速率。
- 控制接口服务端：`attach` 与流式消息、ping 超时。
- 测试至少包含：TestFeishuReceiveP2P、TestFeishuIgnoreGroup、TestFeishuSendText、TestFeishuSendFile、TestFeishuRateLimit、TestFeishuOfflineAfter5Min、TestFeishuOwnerStatus、TestLoginInteractive；TestMultiIMIsolation、TestOwnerPerPlatform、TestAttachProxySession、TestExternalEventsToOutbox、TestExternalApprovalRequest、TestInjectDisabled、TestInjectWaitsForIdle、TestExternalDetachCloses、TestPTYNoApprovalNotice；TestAttachStream、TestPingTimeout；TestPerPlatformLimits、TestFeishuNoWindow。

### 5.3 T2 细则

- 按 3.6 实现。测试里用 acp-go-sdk 客户端扮演编辑器，用假 agent 做真实 agent，用本包内的假控制服务端扮演守护进程。
- 测试至少包含：TestProxyPassthroughEcho、TestProxyMirrorToDaemon、TestProxyNoDaemonPassthrough、TestProxyInjectWhenIdle、TestProxyQueuesEditorPromptDuringInject、TestProxyEditorApprovalDefault、TestProxyRemoteApprove、TestProxyEditorDisconnect、TestProxyMultipleSessions、TestProxyUnknownMethod。

### 5.4 T3 细则

- 按 3.5 实现 mcpmini、薄壳、兼容模式、审批 ID 沿用、插件清单与一致性测试；`test/e2e/testdata/echo-channel` 是一个用 mcpmini 写的最小 channel server：收到 stdin 行时发 `notifications/claude/channel`，实现 `reply` 工具并把回复写到 stdout 可观察的位置。
- 测试至少包含：TestInitializeNegotiation、TestToolsListAndCall、TestNotificationsBothWays、TestPing、TestCancelledIgnored、TestLineFraming；TestCapabilitiesDeclared、TestInstructionsPresent、TestInboundMetaKeys、TestReplyTool、TestReplyUnknownChat、TestPermissionRelayAllow、TestPermissionRelayDeny、TestPermissionIDReused、TestShimWithoutDaemon；TestHostSpawnAndReceive、TestHostReplyViaTool、TestHostRestartOnExit、TestHostOwnerMapping；TestPresetIDAccepted、TestPresetIDCollision；TestConformanceInitialize、TestConformanceTools、TestConformanceNotifications。

### 5.5 T4 细则

- 按 3.7、3.8 实现；v1 路径保持不变。
- 测试至少包含：TestV2Negotiation、TestV2PromptAcceptedThenIdle、TestV2Cancel、TestV2ListResumeClose、TestV2LoginRequired、TestAutoFallsBackToV1、TestV1Unchanged；TestRunEchoInjection、TestIdleDetection、TestBracketedPaste、TestStripANSI、TestUserTypingDelaysInjection、TestExitCodePropagated（以上仅 Linux、macOS）、TestRunUnsupportedOnWindows（Windows 构建标签）；TestFakeAgentV2Mode。

### 5.6 TΩ 细则

- 合并顺序：T4 → T3 → T2 → T1。
- 装配：按配置创建微信、飞书与各 channel server 适配器；控制接口服务端接上核心的 `Attach`；`ferry login feishu` 调用 `feishu.LoginInteractive`。
- 端到端：按 2.2 实现 B1–B9；B9 用构建标签限定 Linux 与 macOS。
- 文档（中英两份）：新增 `docs/en/feishu.md`、`docs/en/proxy.md`、`docs/en/claude-code.md`、`docs/en/pty.md` 及其中文版；`agents.md` 补 ACP 版本设置；`handoff.md` 补三档里的「代理直连」与 Claude Code 可选插件说明（写明需要 Anthropic 官方身份验证）；README 补飞书与编辑器用法各一句。保持 check-i18n 通过。

## 6. 每个任务的验收

| 任务 | 必须通过（命令见各自提示词） |
| --- | --- |
| T0 | G1–G3、全量单测、全部端到端、check-i18n、依赖锁定、控制客户端测试，共 8 项 |
| T1 | feishu、core、control 与 outbox 三组测试，重复 3 次稳定，G1–G3，共 7 项 |
| T2 | proxy 测试，重复 3 次稳定，G1–G3，共 5 项 |
| T3 | mcpmini、channel 与兼容模式、approval、一致性四组测试，G1–G3，共 7 项 |
| T4 | acp（含 v2）、pty、fakeagent 三组测试，重复 3 次稳定，G1–G3，共 7 项 |
| TΩ | A1–A3、B1–B11、C1–C2，共 16 项 |

## 7. 卡住了怎么办

见 00 总览 3.8。第 0 节和 3.5 里仍为「待核实」的项，执行者按已确认的部分实现，其余集中为可配置常量，并在回执中列出。

## 8. 派工提示词

### P3-T0

````
# 任务 P3-T0 — P3 契约变更、控制协议与依赖

## 工作区
直接在 main 分支工作（当前应为 p2-done）。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p2.md 第 3.4 节（现有控制接口）
- docs/dev/spec-p3.md 第 3 节全部、第 5.1 节

## 目标
把 spec-p3 第 3 节的接口、配置、控制协议（types.go 与 client.go 完整实现）、i18n 键与新依赖落到代码里，写出各新包的签名，并保证现有代码与测试不受影响。

## 可写文件
spec-p3 第 3.11 节中 owner 为 T0 的路径；因接口变更必须做的最小修改（core 与 outbox 的 IM 字段、各新包里只含签名的文件）。

## 禁改
- docs/dev/**、LICENSE
- 不实现 T1–T4 的功能

## 验收（8 项）
1. `go build ./...` → 退出码 0
2. `go vet ./...` → 无输出
3. `test -z "$(gofmt -l .)"` → 退出码 0
4. `go test ./...` → 全部 ok
5. `go test ./test/e2e/ -v` → P1、P2 的端到端测试全部 PASS
6. `go run ./scripts/check-i18n` → `i18n ok`
7. `grep -q 'github.com/larksuite/oapi-sdk-go/v3 v3.12.0' go.mod && grep -q 'github.com/modelcontextprotocol/go-sdk v1.8.0' go.mod` → 退出码 0
8. `go test ./internal/control/... -run TestClient -v` → PASS

## 完成后
git add -A && git commit -m "p3-t0: freeze p3 contracts"

回执正文附 `git show --stat HEAD`。最后一行严格为：
RECEIPT P3-T0 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P3-T1

````
# 任务 P3-T1 — 飞书入口、多入口核心与外部会话

## 工作区
git fetch --tags
git worktree add -b p3-t1 ../ferry-p3-t1 p3-base
cd ../ferry-p3-t1

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.6 节；docs/dev/spec-p2.md 第 3.6、3.8 节
- docs/dev/spec-p3.md 第 3.1–3.4、3.9、3.10 节，第 5.2 节

## 目标
实现飞书适配器（经 Client 接口封装官方 SDK 的长连接），让核心与发件箱同时服务多个入口，并支持 proxy、channel、pty 三类外部会话经控制接口接入。

## 可写文件（只有这些）
- internal/im/feishu/**
- internal/core/**
- internal/outbox/**
- internal/control/**（types.go、client.go 除外）

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/control/types.go、internal/control/client.go、locales/**、go.mod、go.sum
- 白名单以外的任何文件

测试不访问真实飞书；真实 SDK 的连通性留给人工验收。

## 验收（7 项）
1. `go test ./internal/im/feishu/... -v` → 全部 PASS，含 5.2 列出的飞书测试
2. `go test ./internal/core/... -v` → 全部 PASS，含 5.2 列出的核心测试，且已有核心测试仍通过
3. `go test ./internal/control/... ./internal/outbox/... -v` → 全部 PASS
4. `go test ./internal/core/... ./internal/im/feishu/... -count=3` → ok
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p3-t1: feishu and multi-im core"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P3-T1 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P3-T2

````
# 任务 P3-T2 — ferry proxy

## 工作区
git fetch --tags
git worktree add -b p3-t2 ../ferry-p3-t2 p3-base
cd ../ferry-p3-t2

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p0.md 第 3.5 节（假 agent）
- docs/dev/spec-p3.md 第 3.3、3.6、3.10 节，第 5.3 节

## 目标
实现 `ferry proxy <agent>`：对编辑器扮演 ACP agent、对真实 agent 扮演客户端，双向转发，同时把会话镜像到守护进程，并按参数支持 IM 输入与 IM 审批。

## 可写文件（只有这些）
- internal/driver/proxy/**
- cmd/ferry/proxy.go

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/control/**、go.mod、go.sum
- 白名单以外的任何文件

控制接口只使用 T0 提供的客户端；守护进程一侧用本包测试里的假服务端。

## 验收（5 项）
1. `go test ./internal/driver/proxy/... -v` → 全部 PASS，含 5.3 列出的全部测试名
2. `go test ./internal/driver/proxy/... -count=3` → ok
3. `go build ./...` → 退出码 0
4. `go vet ./...` → 无输出
5. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p3-t2: acp proxy"

回执正文：每条验收命令的真实输出关键行；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P3-T2 status=<done|blocked> commit=<短hash> checks=<通过数>/5 files=<改动文件数>
````

### P3-T3

````
# 任务 P3-T3 — Claude Code 可选插件与 channel 兼容模式

## 工作区
git fetch --tags
git worktree add -b p3-t3 ../ferry-p3-t3 p3-base
cd ../ferry-p3-t3

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 3.7 节（审批 ID 规则）
- docs/dev/spec-p3.md 第 3.3、3.5、3.10 节，第 5.4 节

## 目标
实现 mcpmini、`ferry channel` 薄壳（字段与 Claude Code channels reference 一致）、把第三方 channel server 当作入口的兼容模式、审批 ID 沿用规则、插件清单，以及与官方 MCP Go SDK 的一致性测试。

## 可写文件（只有这些）
- internal/mcpmini/**
- internal/driver/claudechannel/**
- internal/im/channelhost/**
- internal/approval/**
- plugin/**
- spec/conformance/**
- cmd/ferry/channel.go
- test/e2e/testdata/echo-channel/**

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/control/**、go.mod、go.sum
- 白名单以外的任何文件

3.5 里的协议字段名一个字都不能改。插件清单格式仍为「待核实」时，按官方文档当前格式写，并在回执中注明依据。

## 验收（7 项）
1. `go test ./internal/mcpmini/... -v` → 全部 PASS，含 5.4 列出的 mcpmini 测试
2. `go test ./internal/driver/claudechannel/... ./internal/im/channelhost/... -v` → 全部 PASS，含 5.4 列出的对应测试
3. `go test ./internal/approval/... -v` → 全部 PASS，含 TestPresetIDAccepted 与 TestPresetIDCollision，且已有审批测试仍通过
4. `go test ./spec/conformance/... -v` → 全部 PASS
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p3-t3: claude channel shim and channel host"

回执正文：每条验收命令的真实输出关键行；插件清单的格式依据；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P3-T3 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P3-T4

````
# 任务 P3-T4 — ACP v2 与 ferry run

## 工作区
git fetch --tags
git worktree add -b p3-t4 ../ferry-p3-t4 p3-base
cd ../ferry-p3-t4

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 第 5.3 节（ACP v1 驱动规则）
- docs/dev/spec-p3.md 第 3.3、3.7、3.8、3.10 节，第 5.5 节
- ACP v2 概览与各方法页面：https://agentclientprotocol.com/protocol/v2/overview

## 目标
为 ACP 驱动加上 v2 路径与自动协商，给假 agent 加 v2 模式；实现 `ferry run`，用伪终端包装没有 ACP 的 CLI 并接入守护进程。

## 可写文件（只有这些）
- internal/driver/acp/**
- internal/driver/pty/**
- internal/testkit/fakeagent/**
- cmd/ferry-fakeagent/**
- cmd/ferry/run.go

## 禁改
- docs/dev/**
- internal/ports/**、internal/config/types.go、internal/control/**、go.mod、go.sum
- 白名单以外的任何文件

v2 规范与 3.7 不一致时，停下来报告，不要自行选择。

## 验收（7 项）
1. `go test ./internal/driver/acp/... -v` → 全部 PASS，含 5.5 列出的 v2 测试，且 v1 测试仍通过
2. `go test ./internal/driver/pty/... -v` → 全部 PASS，含 5.5 列出的 PTY 测试
3. `go test ./internal/testkit/fakeagent/... -v` → 全部 PASS，含 TestFakeAgentV2Mode，且 P0 的测试仍通过
4. `go test ./internal/driver/acp/... ./internal/driver/pty/... -count=3` → ok
5. `go build ./...` → 退出码 0
6. `go vet ./...` → 无输出
7. `test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p3-t4: acp v2 and pty runner"

回执正文：每条验收命令的真实输出关键行；参照的 v2 页面与日期；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P3-T4 status=<done|blocked> commit=<短hash> checks=<通过数>/7 files=<改动文件数>
````

### P3-TΩ

````
# 任务 P3-TΩ — 合流、装配与端到端测试

## 工作区
main 分支，当前应位于标签 p3-base。开始前确认 `git status --short` 为空。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p3.md 全文，重点第 2 节、第 3.11 节、第 5.6 节

## 第一步：合流
按顺序合并：p3-t4 → p3-t3 → p3-t2 → p3-t1。
一次合一个：`git merge --no-ff <分支>`，合完立刻跑 `go build ./... && go test ./...`，全绿再合下一个。
冲突按 3.11 归属表判定；表里没写的，停下来报告。合流阶段只修集成缝隙，不补漏掉的功能。

## 第二步：装配、端到端与文档
按 5.6 完成装配，实现 test/e2e 的 B1–B9，更新 5.6 列出的文档。

## 可写文件
- 合流时：解决冲突所需的文件
- 第二步：spec-p3 第 3.11 节中 owner 为 TΩ 的路径

## 禁改
- docs/dev/**、LICENSE、internal/ports/**、go.mod、go.sum

## 全量验收（16 项）
1. `go build ./... && go test ./...` → 全部 ok
2. `go run ./cmd/ferry --help` → 含 proxy、channel、run；`go run ./cmd/ferry login --help` → 含 wechat 与 feishu
3. `go run ./cmd/ferry proxy --help && go run ./cmd/ferry run --help && go run ./cmd/ferry channel --help` → 退出码 0
4–12. `go test ./test/e2e/ -run <名称> -v`，名称依次为 TestFeishuFake、TestFeishuApproval、TestProxyMirror、TestProxyInject、TestProxyApprovalModes、TestChannelShim、TestChannelHost、TestACPv2、TestPTYRun → 每个都 PASS
13. `go test ./spec/conformance/... -v` → PASS
14. `go run ./scripts/check-i18n` → `i18n ok`
15. `go test ./test/e2e/ -v` → 全部 PASS（含 P1、P2 的测试）
16. `go vet ./... && test -z "$(gofmt -l .)"` → 退出码 0

## 完成后
git add -A && git commit -m "p3-tomega: wire feishu proxy channel pty"
不要推送，不要打标签。

回执正文：每次合入后的验收结果；冲突与判定依据；16 项验收的真实输出关键行；还差什么。最后一行严格为：
RECEIPT P3-TΩ status=<done|blocked> merged=<合入数>/4 checks=<通过数>/16
````

## 9. 人工验收清单

| # | 操作 | 期望 |
| --- | --- | --- |
| 1 | `ferry login feishu`，填入第 0 节准备的信息；配置里启用飞书；重启守护进程 | `ferry status` 显示微信与飞书都在线 |
| 2 | 飞书私聊机器人：`/new kimi ~/code/demo`，提问并触发一次审批 | 回执、结果、审批与微信里的体验一致 |
| 3 | 微信与飞书分别发 `/ls` | 各自只看到自己的会话 |
| 4 | 在 Zed 的 External Agents 设置里把命令设为 `ferry proxy qwen-code` 并对话 | 微信收到接入提示与镜像输出 |
| 5 | 改为 `ferry proxy --allow-inject qwen-code`，会话空闲时在微信发一句话 | Zed 里出现这句话和 agent 的回复 |
| 6 | 改为 `--remote-approve`，让 agent 执行命令 | 审批只出现在微信，裁决后 Zed 里继续 |
| 7 | 在终端里用 Toad 配置 `ferry proxy kimi`（配置方式以 Toad 文档为准）并对话 | 效果同第 4 项 |
| 8 | 仅限有 Anthropic 官方账号的用户：在 `.mcp.json` 加入 ferry，用开发标志启动 Claude Code；微信发消息，再触发一次工具审批并在微信里回复 `y <id>` | Claude 的回复出现在微信；审批经微信生效 |
| 9 | 按官方 fakechat 插件的源码配置为 `channel_servers`，在它的网页里发消息 | agent 的回复出现在 fakechat 网页里 |
| 10 | `ferry run -- <某个没有 ACP 的 CLI>`，在微信发一句话 | 收到该 CLI 的输出；终端里能正常继续手动操作 |
| 11 | 若已有支持 ACP v2 的 agent：把它的 `acp_version` 设为 `auto` 走一遍对话、恢复与关闭 | 全部正常；没有可用 agent 时跳过并记录 |

全部通过后：`git push origin main`，确认 CI 全绿；`git tag p3-done v0.3.0 && git push origin p3-done v0.3.0`。

## 附录：P4 发行清单

P4 按需分批进行。每一批开工前，按本目录的格式另写一份 `spec-p4-<批次>.md`（问题定义、验收命令、冻结契约、文件归属、派工提示词），再按 00 总览的七步执行。

| 任务 | 内容 | 验收 |
| --- | --- | --- |
| 多用户隔离 | 每个配对用户有独立的会话空间与工作区根目录；审批人按入口配置 | 两个微信号互相看不到对方的会话，也不能在对方的目录里开会话 |
| 系统服务 | `ferry service install`、`uninstall`、`status`，基于 kardianos/service v1.3.0 | macOS、Linux、Windows 安装后开机自启，`ferry status` 显示在线 |
| Windows 补齐 | `ferry run` 支持 ConPTY；接力占用检测支持 Windows | Windows 上 P2 的 B4 与 P3 的 B9 对应测试通过 |
| 飞书卡片审批 | 用交互卡片按钮承载审批，按钮回调携带审批 ID | 点击按钮即完成裁决，文本回复仍然可用 |
| 审计导出 | `ferry audit export --since 7d --format csv` | 字段完整，不含消息正文 |
| 发行渠道 | Homebrew tap、Scoop 清单、安装脚本的国内镜像说明、发布签名（方式待定） | `brew install`、`scoop install` 与安装脚本在三个平台一次成功 |
| 文档站 | 中英双语文档站（工具待定），GitHub Pages 部署，check-i18n 覆盖文档站源文件 | 两种语言的导航与页面一一对应 |
| 公开发布 | README 30 秒演示、中英文发布文章、Issue 与 PR 模板、行为准则、仓库改为公开 | GitHub Release `v1.0.0` 发布 |
| 名称与商标 | 确认 Ferry 在 GitHub、Homebrew 与商标上可用，否则改名 | 结论写入 00 总览第 6 节 |

**v1.0.0 门槛**：产品方案 A1–A15 全部通过；连续 2 周没有 P0 级缺陷；三个平台的安装方式各一次成功；P1 验证关卡的 20 名试用者中至少 10 人仍在使用。
