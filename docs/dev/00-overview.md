# Ferry 构建派工单 · 00 总览

> 配套《Ferry（暂定名）产品方案 v0.2》，生成于 2026-09-17。
> 本目录 5 个文件下载后原样放进仓库的 `docs/dev/`，执行者只读仓库里的这些文件。

| 文件 | 内容 |
| --- | --- |
| `00-overview.md` | 本文件：使用方法、阶段总表、全局约定、开工准备 |
| `spec-p0.md` | P0 骨架与测试替身：契约 + 6 条派工提示词 |
| `spec-p1.md` | P1 微信 + 托管会话：契约 + 7 条派工提示词 + 人工验收 |
| `spec-p2.md` | P2 终端接力与可靠投递：契约 + 6 条派工提示词 + 人工验收 |
| `spec-p3.md` | P3 扩展（飞书、proxy、Claude Code 插件、ACP v2、PTY）+ P4 发行清单 |

## 1. 怎么用

每个阶段是一轮并行开发，固定七步：

1. **确认存档**：该阶段的 `spec-pN.md` 和本文件已在 `main` 的 `docs/dev/` 下（第 4 节一次放好全部文件）。
2. **发 T0**：把该阶段 T0 提示词整段粘进一个新的编码 agent 会话。T0 直接在 `main` 上工作。
3. **打基线标签**：T0 回执为 done，你在 `main` 上复跑它的验收也全绿后，执行 `git tag pN-base && git push origin pN-base`。
4. **并行发 T1–Tn**：每条提示词开一个新会话，各自在独立 worktree 里工作，互不干扰。
5. **收回执**：每条回执的最后一行是机器可判的一行。只看这一行就能决定合谁、救谁，不用读代码。
6. **发 TΩ**：全部并行任务 done 后，发集成提示词。TΩ 一次合一个分支，每合一个跑一次全量验收。
7. **收尾**：TΩ done 后，你复核并打 `pN-done` 标签，再做该阶段的「人工验收清单」（真实微信、真实 agent）。

执行者可以是任何编码 agent：Claude Code、Codex、Qwen Code、Kimi CLI、OpenCode 都行。提示词里没有任何 agent 专属语法。

**为什么这样切**：「开发 → 检查 → 测试 → 上传」是流水线，不是并行轴。这里每个任务都自带「实现 + 测试 + 自验收」，任务之间靠文件归属不相交来避免互相覆盖；检查和测试写在每个任务的验收里，发布脚本单独成一个任务。

**回执只有两种状态**：done 或 blocked。blocked 时回执会写清卡在哪、需要你做什么决定。任何情况下都不要让执行者自己改契约。

## 2. 阶段总表

| 阶段 | 交付 | 并行任务 | 预计 | 结束标签 |
| --- | --- | --- | --- | --- |
| P0 | 仓库骨架、协议类型、假 iLink 服务、假 ACP agent、双语与 CI | 4 | 3–5 天 | `p0-done` |
| P1 | 扫码登录、配对、托管会话、审批、发件箱 v1；可真实使用 | 5 | 8–10 天 | `p1-done`、`v0.1.0` |
| P2 | 终端接力、OpenCode / Qwen 服务驱动、窗口外补投、唤醒、媒体 | 4 | 8–10 天 | `p2-done`、`v0.2.0` |
| P3 | 飞书入口、`ferry proxy`、Claude Code 可选插件、ACP v2、PTY | 4 | 10–14 天 | `p3-done`、`v0.3.0` |
| P4 | 多用户、系统服务、发行渠道、文档站、公开发布 | 按需 | 持续 | `v1.0.0` |

**P1 验证关卡**：`p1-done` 后找 20 名同时使用多家 agent 的开发者试用一周。如果「一个 bot 管所有会话」和「可靠投递」不在他们最在意的前两项里，暂停 P2，重新评估方向。

## 3. 全局约定（所有任务必须遵守）

### 3.1 基本信息

| 项 | 值 |
| --- | --- |
| 项目名 / 命令名 | Ferry / `ferry`（暂定名） |
| Go 模块路径 | `github.com/TomwaltW/ferry` |
| Go 版本 | `go.mod` 写 `go 1.27`；工具链 go1.27.1 |
| 许可证 | Apache-2.0 |
| README | 英文默认，中文同级 |
| 机器人默认语言 | `zh-CN` |
| 默认 agent 预设 | `qwen-code` |
| 数据目录 | 环境变量 `FERRY_HOME`；未设置时为 `~/.ferry` |

### 3.2 环境

- 国内执行者先运行 `go env -w GOPROXY=https://goproxy.cn,direct`。
- 所有代码在 `CGO_ENABLED=0` 下可编译。`-race` 只在 CI 的 Linux 任务里跑，本机验收不要求。
- 自动化验收只用测试替身（假 iLink 服务、假 ACP agent、各阶段自带的假服务）。执行者不需要微信号、agent 账号或外网（拉取 Go 依赖除外）。

### 3.3 依赖白名单

`go.mod` 只由各阶段 T0 修改。其他任务需要新依赖时，停下来报告。

| 模块 | 版本 | 用途 | 引入阶段 |
| --- | --- | --- | --- |
| github.com/coder/acp-go-sdk | v0.13.5 | ACP 客户端与假 agent | P0 |
| github.com/spf13/cobra | v1.10.2 | 子命令 | P0 |
| github.com/pelletier/go-toml/v2 | v2.4.3 | 配置与 locale 文件 | P0 |
| github.com/nicksnyder/go-i18n/v2 | v2.6.1 | 多语言 | P0 |
| github.com/mdp/qrterminal/v3 | v3.2.1 | 终端二维码 | P0 |
| modernc.org/sqlite | P0-T0 锁定当时最新稳定版 | 纯 Go SQLite | P0 |
| github.com/zalando/go-keyring | v0.2.8 | 系统钥匙串 | P0 |
| github.com/modelcontextprotocol/go-sdk | v1.8.0 | MCP 一致性测试对照 | P3 |
| github.com/aymanbagabas/go-pty | v0.2.3 | 伪终端 | P3 |
| golang.org/x/term | P3-T0 锁定当时最新版 | 终端原始模式 | P3 |
| 飞书官方 Go SDK | P3 开工前人工核实后锁定 | 飞书长连接 | P3 |
| github.com/kardianos/service | v1.3.0 | 系统服务 | P4 |

- 版本号为 2026-09-17 按各仓库 GitHub 标签核实的结果。
- iLink 客户端自研（`internal/ilinkwire` + `internal/im/wechat`）。openilink-sdk-go v0.6.0（MIT）只作字段与行为参考；如复制其代码，须保留原版权声明。
- 标准库优先：HTTP 用 `net/http`，日志用 `log/slog`，测试用 `testing` + `httptest`，不引入断言库。

### 3.4 代码规范

- 可能阻塞的函数，第一个参数是 `context.Context`。
- 时间一律经 `internal/clock` 注入。测试用假时钟推进，不用 `time.Sleep` 等业务超时。
- 错误使用 `internal/fsp` 的哨兵错误，外层用 `fmt.Errorf("...: %w", err)` 包装。除占位实现外不 panic。
- 日志用 `log/slog`。不记录 token、`context_token`、配对码和消息正文；正文只记长度。
- 面向用户的文字一律经 i18n。键在 `locales/en.toml` 与 `locales/zh-CN.toml`，代码里不写死中英文提示。locale 键属于契约，只由各阶段 T0 增加。
- 测试替身只放在 `internal/testkit/**`，或本包 `_test.go` 里的局部实现。不新增公共 utils 包，宁可重复。
- 格式用 `gofmt`，静态检查用 `go vet ./...`。
- `cmd/ferry` 的根命令变量名为 `rootCmd`。每个子命令一个文件，在该文件的 `init()` 里注册。
- 代码注释、提交信息、标识符用英文；面向用户的文档中英双语。

### 3.5 通用验收 G1–G3（每个任务都要跑）

```bash
go build ./...            # G1：退出码 0
go vet ./...              # G2：无输出，退出码 0
test -z "$(gofmt -l .)"   # G3：退出码 0
```

### 3.6 Git 约定

- 并行任务一律在带分支的 worktree 里工作：`git worktree add -b pN-tK ../ferry-pN-tK pN-base`。
- 提交信息：`pN-tK: <一句话>`，例如 `p1-t2: acp driver and registry`。
- 所有任务禁止修改 `docs/dev/**` 和 `LICENSE`。
- 执行者不推送、不打标签；推送和打标签由你来做。

### 3.7 回执格式

T0 与并行任务的最后一行：

```
RECEIPT PN-TK status=<done|blocked> commit=<短hash> checks=<通过数>/<总数> files=<改动文件数>
```

TΩ 的最后一行：

```
RECEIPT PN-TΩ status=<done|blocked> merged=<合入数>/<总数> checks=<通过数>/<总数>
```

回执正文必须包含：每条验收命令真实输出的关键几行；改动文件列表；若 blocked，写清卡点、试过什么、需要什么决定。只写「测试通过」而没有输出的，视为未完成。

### 3.8 卡住时

1. 需要的接口在契约里找不到 → 停，报告，不要自己发明。
2. 需要改的文件不在白名单 → 停，报告，不要「就改一行」。
3. 环境问题 → 先自行排查；排查不动 → 报告。
4. 验收过不了但自认为逻辑正确 → 以验收为准继续改；确信验收本身写错 → 停，报告，不要改验收。

## 4. 开工前的人工准备（一次，约 15 分钟）

1. 在 GitHub 新建仓库 `TomwaltW/ferry`：先设为私有，创建时勾选 Apache-2.0 许可证。
2. 下载本目录全部 5 个文件，然后执行：

```bash
git clone git@github.com:TomwaltW/ferry.git && cd ferry
mkdir -p docs/dev
cp ~/Downloads/00-overview.md ~/Downloads/spec-p0.md ~/Downloads/spec-p1.md \
   ~/Downloads/spec-p2.md ~/Downloads/spec-p3.md docs/dev/
git add docs/dev && git commit -m "docs: add build specs"
git push origin main
```

3. 安装 Go 1.27.1，用 `go version` 确认。
4. 从 P1 起做人工验收时需要：一个已开通 ClawBot 插件的微信号（建议用小号当 bot，主号给它发消息）；本机至少装好一个 ACP agent，国内推荐 Kimi CLI 或 Qwen Code。

## 5. 人工验收总表

自动化验收由各任务完成。下表是每个阶段结束后由你亲手做的检查，编号对应产品方案第 11 章。

| 产品方案编号 | 内容 | 在哪一阶段做 |
| --- | --- | --- |
| A1–A3 | 构建、双语文件、locale 键 | P0（CI 自动） |
| A4–A9 | 扫码、陌生人门控、托管会话、审批、取消、忙时合批 | P1 人工清单 |
| A10–A12 | 一档接力、接力互斥、二档同时在线 | P2 人工清单 |
| A13–A15 | 窗口外补投、回复配额、重试去重 | P2（假网关自动）+ 人工抽查 |

## 6. 默认决策

要改请在对应阶段的 T0 之前改本文件并提交。

| 决策 | 默认 | 影响范围 |
| --- | --- | --- |
| 项目名 | Ferry | 模块路径、命令名、文档 |
| iLink 客户端 | 自研 | P0-T0、P1-T1 |
| 第二个 IM 入口 | 飞书 | P3-T1 |
| ACP v2 | P3 自行实现，不等 SDK | P3-T4 |
| README 默认语言 | 英文 | P0-T3 |
| 默认 agent 预设 | `qwen-code` | P1-T0 |
| 版本号 | P1、P2、P3 结束分别打 `v0.1.0`、`v0.2.0`、`v0.3.0`；P4 结束打 `v1.0.0` | 发行 |

## 7. 实测记录（P1 人工验收时填写，P2-T0 会读取）

| 项 | 实测值 | 测试方法 |
| --- | --- | --- |
| 单条入站消息的回复上限 | 待测（社区报告约 10 条） | 让 agent 连续回复 15 条，数手机实际收到几条 |
| `context_token` 可用时长 | 待测（社区报告约 24 小时） | 分别隔 1、6、23、25 小时后让 agent 主动回复 |
| 群聊能否收到消息 | 待测 | 把 bot 拉进群，@它发消息，看守护进程日志 |
| 语音是否带转写文字 | 待测 | 发一条语音，看入站消息的 `voice_item.text` |
