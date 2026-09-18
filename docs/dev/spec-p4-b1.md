# 开发派工 Spec — Ferry P4 第一批（Windows 补齐、多用户隔离、审计导出、飞书卡片审批、系统服务）

> 生成于 2026-09-18，由 P4-T0 按 `docs/dev/spec-p3.md` 末尾「附录：P4 发行清单」写成。本批次唯一权威文档，另须遵守 `docs/dev/00-overview.md` 第 3 节。
> 前置条件：T0 从 `main` 的 `0492443`（`Merge branch 'p3-tomega' into main`）开工；此后 `main` 前进到 `6b699ee`（合入 `windows-ci-green`，见 F4）。本文件与第 3 节标「T0 已落地」的代码都在分支 `p4-t0` 上。冻结后不再修改。
> 本批次范围由总管定：附录 9 项里收前 5 项。发行渠道、文档站、公开发布、名称与商标要用户本人的账号与决策，不在本批次。

## 0. 已核实的事实与开工前准备

**已核实（2026-09-18，本机 darwin/arm64、go1.27.1，另有 GitHub Actions 记录为证）**

| # | 项 | 结论 |
| --- | --- | --- |
| F1 | 基线（T0 开工时） | `main` = `origin/main` = `0492443`；`go build ./...` 退出码 0；`go test ./... -count=1` 为 `ok` 31 条、`FAIL` 0 条；`go test ./test/e2e/... -count=1 -v` 为 `--- PASS` 20 条（P1 的 11 条 + P3 的 9 条） |
| F2 | 基线的 CI | run `35327973192`（`0492443`）：7 个 job 里 lint、i18n、cross、race、test (ubuntu-latest)、test (macos-latest) 绿，**只有 test (windows-latest) 红**。往前 4 次推到 `main` 的 run 也都是、且只是这个 job 红：`35191427689`（p1-tomega 修 Windows e2e）、`35194160875` 与 `35194714543`（两条 `test: make ... run on windows`）、`35196014351`（p2-t0）。再往前是 p1-tomega 的第一次 push（`35190898538`），当时 race 与 Windows 两个 job 都红。所以从 p1-tomega 到 `0492443`，`main` 上的 CI 一次也没全绿过；第一次全绿是 `6b699ee`（F4） |
| F3 | Windows 的 6 条失败 | 原文与根因见 1.1 的表。根因来自 CI 日志原文和代码，**与派单里「像是哪一类」那一栏有两处不同**：`TestFeishuFake` 不是大小写/分隔符问题，`TestProxyInjectWhenIdle` 不是进程或管道时序问题 |
| F4 | Windows 修复已合入 `main` | 另一会话在分支 `windows-ci-green` 上只改测试修掉了全部 6 条（基于 `0492443`，7 个已有的测试文件；没有新增测试函数，也没有新包）。提交 `81ca746` 后来 amend 成 `9cb195a`（只多改 `registry_test.go` 一处），这是最终版。它已合入 `main`：`6b699ee`（`Merge branch 'windows-ci-green' into main`），已 push。CI run `35334782587`（`6b699ee`）**7 个 job 全绿，含 test (windows-latest)，是 `main` 上第一次全绿**。`p4-t0` 与它改的文件不相交，`git merge-tree` 试合并无冲突 |
| F5 | 标签现状 | 仓库里只有 `p0-base`、`p0-done`、`p1-base`、`p2-base`、`p3-base`。没有 `p1-done`、`p2-done`、`p3-done`，也没有 `v0.1.0`、`v0.2.0`、`v0.3.0` |
| F6 | P2 的 TΩ 从未执行 | `main` 的历史里没有 `p2-tomega`：p2-t1..t4 合入后直接是 p3-t0。spec-p2 2.2 的 B1–B13 端到端测试一条都不在 `test/e2e`；`git log --all -S TestBusyRefused` 为空。附录写的验收「Windows 上 P2 的 B4 …… 通过」照字面跑 `go test ./test/e2e/ -run TestBusyRefused`，结果会是 `ok … [no tests to run]`，**是恒真判据**。B4 的行为只在单元层有覆盖：`internal/core/handoff_test.go` 的 `TestUseBusyRefused`（假 Handoff），以及 `internal/handoff/busy_test.go` 的两条真实进程测试（在 Linux、macOS 以外 Skip） |
| F7 | P3 的 B9 | `test/e2e/pty_test.go` 首行是 `//go:build linux \|\| darwin`；`internal/driver/pty/pty.go:50` 的 `Supported()` 是 `runtime.GOOS != "windows"`。Windows 上只跑 `run_windows_test.go`，它断言的是「不支持」 |
| F8 | Windows 上的接力占用检测 | `internal/handoff/proc_other.go`（`!linux && !darwin`）返回 `fsp.ErrUnsupported`，核心据此回复 `handoff.busy_unknown`。内置 profile 的 `ProcessMatch`（kimi、qwen-code、opencode、claude-acp、codex-acp）按 `/` 分隔写，认不出 `C:\…\kimi.exe` 这样的 Windows 命令行。只有 `fake` 的 `ferry-fakeagent` 能认出来 |
| F9 | 平台名 | 第三方 channel server 的平台是 `channel-<name>`：`config.ChannelPlatformPrefix = "channel-"`，与 spec-p3 3.1 一致。`internal/acl/acl.go` 的注释、`internal/acl/acl_test.go:72` 和派单里写的 `channel:<name>` 是笔误；冒号还会和 `EndpointID.String()` 的分隔符冲突 |
| F10 | owner 作用域现状 | `acl.SetOwner` 把 owner 角色记在 `{platform, botID, PeerID: ownerID}`，入站消息的 endpoint 是 `{platform, botID, PeerID: chat}`，所以只有 chat 恰好等于 owner ID 时才认得出 owner。`test/e2e/p3_test.go` 的 `TestChannelHost` 靠 `ECHO_CHANNEL_CHAT = user` 绕过了这一点（注释里写明了）。**没有任何测试钉住「只在自己私聊里是 owner」** |
| F11 | 审批人现状 | `acl.approvers(p)`：feishu 走 `Feishu.WithDefaults().Approvers`，其余（wechat、fake、`channel-*`）一律走 `[wechat].approvers`。`config.Load` 不调 `WithDefaults`，飞书省略名单时读到的是 nil，所以 acl 必须经 `WithDefaults` 取，否则飞书上没人能批（`TestFeishuApproval` 踩过）。`internal/acl/acl_test.go` 的 `TestApproversArePerPlatform` 钉的正是「channel 平台沿用 wechat 名单」这条 P3 语义 |
| F12 | 默认审批策略 | `internal/approval/policy.go` 的 `defaultRules`：read、search、think 在任何位置都自动允许；edit、delete、move 在工作区外拒绝；其余走默认 ask。「工作区」是全局 `workspace.roots` |
| F13 | 多用户现状 | 会话按 endpoint 绑定，私聊 endpoint 就是一个人，所以两个微信号的托管会话本来就互相看不见。但 `/ls` 里的终端会话条目（`handoff.Discover`，只按全局 roots 过滤）对所有已配对用户可见，也能 `/use` 接管；`/new` 只按全局 roots 校验。`test/e2e/e2e_test.go` 的 `TestStrangerPairing`（P1 B2）钉住了「配对后的陌生人可以在 `<ws>/demo` 开会话」 |
| F14 | 审批消息到达适配器时的形态 | `approval.askOwner` 发出的是 `fsp.Outbound{Kind: OutApproval, Text, Urgent: true}`，`DedupeKey` 为空。`outbox.deliver` 会把同会话排在前面的普通消息拼到它前面，并把 `Kind` 一律改成 `OutText`（`internal/outbox/send.go`，`deliver` 里 `msg.Kind = fsp.OutText`）。调用方给的 `DedupeKey` 只原样留在第一段，后续段追加 `-<n>`（`dedupeKey()`）。所以**适配器从 Outbound 认不出审批，也拿不到审批 ID** |
| F15 | 裁决的文字形式 | `approval/verdict.go`：`(?i)^\s*(y\|yes\|a\|always\|n\|no)\s+([a-km-z]{5})\s*$`。`Resolve` 成功后回复 `approval.decided`；选项不存在时回复 `approval.option_unavailable` |
| F16 | 凭据存放 | `security.SaveCredentials` 先写系统钥匙串（go-keyring：macOS 钥匙串、Linux Secret Service、Windows 凭据管理器），写不进去才落 `<home>/credentials/<platform>.json` |
| F17 | kardianos/service v1.3.0（读模块缓存里的源码） | Option `UserService` 只在 darwin（LaunchAgent）和 Linux（systemd 等）上生效；Windows 用 `Config.UserName` 加 Option `Password`（`CreateService` 的 `ServiceStartName`、`Password`），另有 `DelayedAutoStart`、`StartType`；darwin 有 `RunAtLoad`、`KeepAlive`；systemd 有 `Restart`（默认 `always`）。状态取值 `StatusUnknown`、`StatusRunning`、`StatusStopped`；未安装返回 `ErrNotInstalled`；`Interactive()` 判断是不是被服务管理器拉起的 |
| F18 | 飞书 SDK v3.12.0（读模块缓存里的源码） | `event/dispatcher/callback_dispatch.go` 有 `OnP2CardActionTrigger`；`ws/const.go` 有 `MessageTypeCard = "card"`。长连接能收卡片回传交互，**不需要公网回调地址** |
| F19 | 依赖钉住 | 没有包 import 时，`go get github.com/kardianos/service@v1.3.0 && go mod tidy` 会把它删掉（实测：`go.mod`、`go.sum` 回到原样）。P0、P3 用 `internal/deps/deps.go` 的空白导入钉住依赖，但本批次 T0 的可写范围不含 `internal/deps/**`，处理见 3.1 |
| F20 | schema 版本被测试钉住 | `internal/store/longoutput_test.go:30` 断言 schema version = 2，任何 `003_*.sql` 迁移都会让它红。见 3.9 |
| F21 | go-toml v2.4.3 | 文件里写 `x = []` 解码为非 nil 的空切片，省略则为 nil（实测）。校验能区分「没写」和「写了空表」 |

**开工前准备（人工）**

1. **v0.3.0 已不再等本批次。** `windows-ci-green` 已合入 `main`（`6b699ee`），`main` 的 CI 第一次全绿（F4）。何时按 spec-p3 第 9 节打 `p3-done` 与 `v0.3.0`（那一节的人工验收清单也要过），以及要不要补打 `v0.1.0`、`v0.2.0`（F5），都由你决定。T1-a 因此只剩确认（见 3.8）。
2. T0 回执为 done 后：把 `p4-t0` 合入 `main`（已试合并，无冲突），在 `main` 上复跑 T0 的 7 项验收，全绿后执行 `git tag p4-b1-base && git push origin p4-b1-base`。**第 7 项在 `main` 上要改成对 `6b699ee` 比较**（`git diff --name-only 6b699ee HEAD`），或者直接在 `p4-t0` 分支上对 `0492443` 比较；否则 `windows-ci-green` 的 7 个测试文件也会被算进来，看起来像越界。
3. 按全局约定建 worktree（`<repo>/.worktrees/task-t1` … `task-t5`，分支 `p4-t1` … `p4-t5`，基线钉 `p4-b1-base` 对应的**具体 sha**），在其中一个里实跑第 8 节「开场自检」的 6 条，把实测值填进各派工提示词的占位，派单落盘为 `review/paste-t1.md` … `paste-t5.md`，然后 `claude-fleet t1..t5`。
4. T4 的人工准备：在飞书开放平台给应用开启「卡片回传交互」，并在长连接模式下订阅卡片回调（`card.action.trigger`）。
5. T5 的人工准备：Windows 验收机上确认安装账号有「作为服务登录」权限，或准备在本地安全策略里加上；Linux 验收机要能运行 `loginctl enable-linger`。

## 1. 问题定义

**要做什么**（五轨，内容照抄附录）：

1. **Windows 补齐**：`ferry run` 支持 ConPTY；接力占用检测支持 Windows。本批次还要让 CI 的 Windows job 转绿，拆成 T1-a、T1-b 两组，见 1.1。
2. **多用户隔离**：每个配对用户有独立的会话空间与工作区根目录；审批人按入口配置。
3. **审计导出**：`ferry audit export --since 7d --format csv`。
4. **飞书卡片审批**：用交互卡片按钮承载审批，按钮回调携带审批 ID。
5. **系统服务**：`ferry service install`、`uninstall`、`status`，基于 kardianos/service v1.3.0。

**为什么在第一批**：

| 轨 | 理由 |
| --- | --- |
| T1 | T1-a 卡着 `v0.3.0`（见 1.1 的因果链）；T1-b 是附录原定范围 |
| T2 | 动 ACL、核心与审批策略，越早冻越好 |
| T3 | 自成一体，好切 |
| T4 | 可以用 `feishutest` 假客户端验收 |
| T5 | 要加依赖，而依赖只能由 T0 改 |

### 1.1 T1 是两件同名的事

**因果链**：spec-p3 第 9 节收尾写着「`git push origin main`，确认 CI 全绿；`git tag p3-done v0.3.0`」。从 p1-tomega 到 `0492443`，`main` 上的 CI 一次也没全绿过，其中最近 5 次都只红在 `test (windows-latest)` 一个 job 上（F2）。CI 不全绿就打不了 `v0.3.0`。**所以 T1-a 是解锁 `v0.3.0` 的那一组。** 修复已合入 `main`（`6b699ee`），`main` 的 CI 已全绿（F4），`v0.3.0` 不再等本批次。T1-a 在本批次里只剩两件事：保证合流后 Windows job 仍然绿（C3），并按 3.8 把私密落盘的判据写死。

**T1-a：让 Windows CI 转绿。** 下表 6 条逐条点名，验收是 `test (windows-latest)` 绿。`6b699ee` 已经做到了，本批次要的是合流后不回归：

| 包 | 测试 | CI 原文（run 35327973192） | 真实根因 | 修法（`windows-ci-green` 已做，已在 `main`） |
| --- | --- | --- | --- | --- |
| `internal/im/wechat` | `TestInboundImageDownloaded` | `mode = -rw-rw-rw- (err <nil>), want 0600` | Windows 没有 POSIX 权限位，`os.Chmod` 只切只读属性，`Perm()` 一律报 0666。**产品侧也确实没有收紧**（见 3.8） | 断言只在非 Windows 上做，缺口写进注释 |
| `internal/outbox` | `TestLongOutputToFile` | `file mode = 666, want 600` | 同上 | 同上 |
| `internal/registry` | `TestResolveBinaryFromPath` | `Command[0] = "…\002\agents\kimi\1.50.0\kimi.exe", want … "…\001\kimi.bat"`；`home has 1 entries` | 夹具写的是 `kimi.bat`，但 `snapshot.json` 里 `windows-x86_64` 的 cmd 是 `./kimi.exe`，`binaryName` 交给 `exec.LookPath` 的是带扩展名的 `kimi.exe`，只能精确匹配 | 夹具改建 `kimi.exe` |
| `internal/driver/proxy` | `TestProxyInjectWhenIdle` | `the editor was shown "", want it to contain "from chat"` | `said()` 是非阻塞地把通道排空，断言跟它要等的更新赛跑（前两条断言都过了，只红在最后一条） | 新增 `waitSaid`，累积到文本出现为止 |
| `internal/im/channelhost` | `TestHostRunsEchoChannel` | `the fixture pushed no message`（11.6 秒） | `go build -o` 建 `echo-channel` 时没加 `.exe`，Windows 不执行没有扩展名的文件；e2e 的 harness 早就加了，这个测试漏了 | 加 `.exe` |
| `test/e2e` | `TestFeishuFake` | 期望 `C:\Users\RUNNER~1\…\ws\demo`，`/ls` 里是 `C:\Users\runneradmin\…\ws\demo` | 核心按 `filepath.EvalSymlinks` 存工作区路径，测试拿没解析的路径去比。**不是大小写或分隔符问题**：一边是 8.3 短名 `RUNNER~1`，一边是长名 `runneradmin`。macOS 上是**碰巧通过**的：`/var/…` 恰好是 `/private/var/…` 的子串 | rig 先对 home 做一次 `EvalSymlinks` |

**T1-b：附录原定范围。** `ferry run` 支持 ConPTY；接力占用检测支持 Windows。附录的验收是「Windows 上 P2 的 B4 与 P3 的 B9 对应测试通过」，但**这两条判据现在都是恒真的**：B9 被构建标签排除在 Windows 之外（F7），B4 根本不存在（F6）。所以 T1-b 必须先让它们能在 Windows 上真的跑起来：放开 B9 的构建标签，补写 B4，并在 CI 里断言它们在 Windows 上是 PASS 而不是 SKIP（3.8）。

**只写 T1-b 的验收，T1 做完了 CI 还是红；只写 T1-a 的，附录的活等于没做。两组都要过。**

### 1.2 明确不做

- 附录后 4 项：发行渠道、文档站、公开发布、名称与商标
- Windows 私密文件的 ACL 加固。本批次只写死「按平台分叉」的判据，理由见 3.8
- 群聊或多人共用一个 chat 时的按人隔离。会话空间仍按 endpoint，私聊就是按人（3.4）
- 补写 P2 其余的端到端测试（B1–B3、B5–B13）。只补 B4，因为 T1-b 的验收要用它
- 飞书群聊（沿用 P3：只处理单聊）
- 把审批请求转发给不在那个 chat 里的审批人。审批请求仍只发往会话所属的 endpoint
- 安装服务时改 Windows 本地安全策略（「作为服务登录」权限由人手工授予）
- 打任何 `v0.x` 标签（开工前准备 1，人工决定）
- 控制协议的新 op（3.10）与存储迁移（3.9）

## 2. 验收标准（DoD）

### 2.1 能跑

- **A1** `go build ./... && go test ./...` → 全部 ok
- **A2** `go run ./cmd/ferry --help` → 在 P3 的基础上多出 `audit` 与 `service`
- **A3** `go run ./cmd/ferry audit export --help` 与 `go run ./cmd/ferry service --help` → 都显示用法，退出码 0

### 2.2 行为正确

端到端测试由 TΩ 写在 `test/e2e/p4_test.go`，B6、B7 由 T1 写。

- **B1** `go test ./test/e2e/ -run TestMultiUserIsolation -v` → rig 配 `workspace.users_root`；陌生人配对后发 `/new fake`（不带目录），会话落在 `<users_root>/wechat/<config.UserDirName(wechat, 陌生人 ID)>`；发 `/new fake <ws>/demo` 收到 `workspace.denied`；owner 的 `/ls` 看不到陌生人的会话，陌生人的 `/ls` 看不到 owner 的会话，也没有终端会话条目
- **B2** `-run TestOwnerAnyChat` → channel server 的 `chat_id` 与 owner ID 不同（`ECHO_CHANNEL_CHAT = "room-1"`，`ECHO_CHANNEL_USER` = owner）：推一条 `/new fake <ws>/demo`，直接收到「会话 1」，不收到配对码
- **B3** `go test ./internal/acl/ -run 'TestApproversForEntry|TestChannelApproversOwnList' -v` → PASS（T2）
- **B4** `-run TestAuditExport` → 微信发 `echo audit-7f3c` 并收到回复后，用构建出的 `ferry` 二进制（`FERRY_HOME` = rig 的 home）跑 `ferry audit export --since 1h`：退出码 0；首行是 `at,kind,endpoint,sender_id,session_id,detail`；有一行 `kind` 为 `inbound`、`detail` 为 `len=15`；整份输出不含 `audit-7f3c`。再跑 `--since 7x` 与 `--format json`，退出码都是 2
- **B5** `-run TestFeishuCardApproval` → 飞书侧发 `ask execute <ws>/demo/run.sh`：`feishutest` 记录到一张卡片，按钮值带着该审批 ID（有 `y` 和 `n`；`a` 当且仅当请求提供 allow_always 时才有）；模拟点击 `y` 后，agent 回复 `allowed`。再触发一次审批，用文本 `n <ID>` 裁决仍然生效
- **B6** `-run TestBusyRefused`（Linux、macOS、Windows）→ spec-p2 B4 原文：启动一个工作目录为 `<ws>/demo` 的 `ferry-fakeagent` 进程模拟终端占用；`/use` 对应的终端会话时收到占用提示，且没有接管；发 `/use <n> force` 后接管成功
- **B7** `-run TestPTYRun`（Linux、macOS、Windows）→ spec-p3 B9；Windows 上换用等价的 Go 夹具（3.8）
- **B8** `go test ./internal/service/... -v` → PASS（T5；真实安装留给人工验收）
- **B9** `go run ./scripts/check-i18n` → `i18n ok`

### 2.3 没弄坏

- **C1** `go test ./test/e2e/ -v` → P1、P3 的 20 条与本轮新增的 5 条（B1、B2、B4、B5、B6）全部 PASS；B7 就是原有的 `TestPTYRun`
- **C2** 00 总览 3.5 的 G1–G3 → 全部通过
- **C3** 总管 push 后：CI 的 7 个 job 全绿；`test (windows-latest)` 里 T1 新加的那一步显示 `TestBusyRefused`、`TestPTYRun`、`TestBusyProcessInCwd`、`TestBusyIgnoresOwnDescendants` 都是 `--- PASS`

## 3. 冻结契约

### 3.1 依赖（T0 已落地）

- `go.mod` 新增直接依赖 `github.com/kardianos/service v1.3.0`（00 总览 3.3 预留，「引入阶段 P4」）。
- `golang.org/x/sys` 从 indirect 升为直接依赖，**版本不变**，仍是 `v0.48.0`。它早已在模块图里（经 go-pty、x/term 引入），`go.sum` 不变；这一步是为了让 T1-b 的 Windows 进程表能直接用 `golang.org/x/sys/windows`，而不必动 `go.mod`。00 总览 3.3 的依赖白名单里还没有这一行，`docs/dev` 由人工维护，请总管补上：`golang.org/x/sys | v0.48.0 | Windows 进程表 | P4`。
- **钉住方式**：新包 `internal/ports/deps`。`deps.go` 空白导入 `github.com/kardianos/service`；`deps_windows.go` 带 `//go:build windows`，空白导入 `golang.org/x/sys/windows`。没有任何包 import 它。原因是 F19：`internal/deps/**` 不在本批次 T0 的可写范围里，而 `go mod tidy` 会删掉没人 import 的依赖。**下一个能改 `internal/deps` 的 T0 把这两行挪回去，再删掉 `internal/ports/deps`。**
- 其他任务不改 `go.mod`、`go.sum`，也不许为此跑 `go mod tidy`。需要新依赖 → 停，报告（00 总览 3.3）。

### 3.2 配置变更（T0 已落地）

`internal/config/types.go`：

```go
type WorkspaceConfig struct {
	Roots []string `toml:"roots"`
	// 为空：P1–P3 行为，所有已配对用户共用 roots。
	// 非空：不是 owner、又没有 [[workspace.users]] 条目的已配对用户，工作区是
	// <users_root>/<UserDirName>。
	UsersRoot string          `toml:"users_root"`
	Users     []WorkspaceUser `toml:"users"` // 点名给出的用户工作区；优先于 users_root；owner 永远用 roots
}

type WorkspaceUser struct {
	Platform string   `toml:"platform"` // wechat | feishu | channel-<name>
	ID       string   `toml:"id"`       // 该平台上的发送者 ID
	Roots    []string `toml:"roots"`
}

type ChannelServerConfig struct {
	// P3 字段保持不变，新增：
	Approvers []string `toml:"approvers"` // "owner" 或该 server 报来的发送者 ID；缺省 ["owner"]（WithDefaults 填）
}

const ApproverOwner = "owner" // 审批人名单里代表 bot owner 的那一项

func UserDirName(platform fsp.Platform, id string) string // 布局见 3.4
func (c Config) ApproversFor(p fsp.Platform) []string   // 语义见 3.3
```

`internal/config/config.go`：

```go
func (c Config) NormalizedUsersRoot() (string, error)      // users_root 规范化（同 NormalizedRoots）；未设置返回 ""
func (u WorkspaceUser) NormalizedRoots() ([]string, error) // 条目的 roots 规范化（同 NormalizedRoots）
```

**校验**（没有新字段的配置照旧通过，唯一的例外是第一行）：

| 字段 | 规则 | 在哪里校验 |
| --- | --- | --- |
| `[wechat].approvers`（wechat 启用时）、`[feishu].approvers`（feishu 启用时）、`channel_servers[].approvers` | 省略合法；**显式写空表报错**（`… an empty list leaves nobody to approve; leave it out for ["owner"]`）；任何一项为空串报错 | wechat 在 `Validate`；feishu、channel 在 `ValidateEntries`（沿用 P3 的分工） |
| `workspace.users_root` | 设了就必须是已存在的目录（`~` 展开） | `Validate` |
| `workspace.users[]` | `platform` ∈ {`wechat`、`feishu`、本文件配置的 `channel-<name>`}；`id` 非空；`roots` 至少一个，且都是已存在的目录；`(platform, id)` 不重复 | `Validate` |

显式空表以前可以通过校验：wechat 上意味着「名单里没人」，飞书上意味着 `["owner"]`，两边语义相反，而且都不报错。现在统一改为报错。仓库里没有任何配置或测试写过空表（已 grep 确认）。

### 3.3 owner 作用域与审批人（派单挂着的三个未决项）

**Q1 owner 的作用域 → 这个 bot 的 owner，在该 bot 的任何 endpoint 上都认。**

- 判据：`isOwner(ep, sender) := Role({ep.Platform, ep.BotID, sender}, sender) == "owner"`，也就是直接读 `SetOwner` 写下的那一行。不改存储、不改 `ports.ACL` 的签名。
- `Allowed(ep, sender)`：ep 上有任何角色，**或** `isOwner`。
- `IsApprover(ep, sender)`：`sender` 在 `ApproversFor(ep.Platform)` 里（`"owner"` 这一项除外）；或 ep 上的角色是 `approver`；或名单含 `"owner"` 且 `isOwner(ep, sender)`。
- 核心第 9 步「非 owner 的消息加 `[from <sender>]` 前缀」同样按 `isOwner` 判定。
- **理由**：owner 身份由平台认证（微信扫码人、飞书的 `owner_open_id`、channel server 报来的 `owner`），换个 chat 并不改变说话的是谁。窄解读没有任何测试钉住，`TestChannelHost` 反而要绕开它（F10）；在窄解读下，第三方 channel server 的 owner 在自己的 bot 上会收到配对码。放宽只影响 owner 本人，**不给任何其他人增加权限**：其他人照旧按 `(chat, sender)` 配对。
- **随之而来的安全注意**（TΩ 写进 security 文档）：channel server 不带 `user_id` 时，发送者按 P3 规则视为 `local`，即 owner。在本契约下，这条规则在该 server 的任何 chat 里都生效。多人共用的 channel server 必须携带发送者 ID，否则等于把 owner 权限交给所有能往里发消息的人。

**Q2 审批人按入口配置 → `Config.ApproversFor(platform)` 是唯一的取法，fallback 写死如下，永不返回空。**

| platform | 名单 |
| --- | --- |
| `wechat` | `[wechat].approvers`（`Default()` 给 `["owner"]`，文件里写了就替换） |
| `feishu` | `[feishu].approvers`；省略 → `["owner"]` |
| `channel-<name>`（已配置） | 该 server 的 `approvers`；省略 → `["owner"]` |
| 其他（`fake`、未配置的 `channel-*`、任何没有表的平台） | `["owner"]` |

- 显式空表在配置校验时报错（3.2），所以运行时不存在「查到空名单 → 没人能批 → 静默 fail closed」。
- `config.Load` 不调 `WithDefaults` 这个坑（F11）由 `ApproversFor` 统一处理。**acl 只许经 `ApproversFor` 取名单，不许再直接读 `cfg.WeChat.Approvers` 或 `cfg.Feishu.Approvers`。**
- **理由**：省略即 `["owner"]` 与 P3 飞书的 `WithDefaults` 一致；显式空表报错，而不是悄悄变成没人能批。「失败且不报错」是最坏的失败面。

**Q3 第三方 channel server 的 owner → `channel_servers[].owner`（缺省 `local`）就是平台 `channel-<name>` 的 owner；审批人用它自己的 `approvers`（缺省 `["owner"]`），不再借用 `[wechat]` 的名单；按 Q1，owner 在该 server 的任何 `chat_id` 上都认。**

- **理由**：按平台查表，必须给开放字符串平台一个非空的答案。继续借用 `[wechat]` 名单，等于拿微信 ID 去比 channel server 报来的发送者 ID，是跨平台串号。
- T2 要按本节改写 `internal/acl/acl_test.go` 的 `TestApproversArePerPlatform`（它钉的是 P3 语义），顺手把 `channel:demo` 更正为 `channel-demo`。**这是契约变更带来的改写，不算改验收。**

### 3.4 多用户隔离（T2）

**用户** = 一个 endpoint 上的一个发送者；bot owner 按 3.3 全 bot 通认。私聊的 endpoint 就是一个人。群聊或多人共用的 chat 里，本批次把 endpoint 当作「一个用户」，不做按人隔离（1.2）。

**端口**（T0 已落地，`internal/ports/workspaces.go`）：

```go
type Workspaces interface {
	// 按顺序，先命中者为准：bot owner → workspace.roots；有 [[workspace.users]] 条目 → 条目的 roots；
	// 设了 users_root → <users_root>/<config.UserDirName(platform, sender)>（不存在就以 0700 创建）；
	// 否则 → workspace.roots（与 P1–P3 相同，共用）。
	// 返回绝对路径、已解析符号链接（核心比较目录用的就是这个形式），永不为空。
	// 会话的 roots = Roots(ctx, s.Owner, s.Owner.PeerID)。
	Roots(ctx context.Context, ep fsp.EndpointID, senderID string) ([]string, error)
}
```

它是独立的接口，不并进 `ports.ACL`：`ports.ACL` 加方法会让现有实现与测试替身编译不过。

**目录布局**（T0 已落地，`config.UserDirName`；布局落在磁盘上，永不改）：`<platform>/<safe>-<hash>`。`safe` 是把 ID 中 `[A-Za-z0-9._-]` 以外的字节、以及开头的 `.`，都替换成 `_`，再截到 32 字节；`hash` 是 `sha256("<platform>:<id>")` 的前 16 个十六进制字符。例：`wechat/wxid_abc_im.wechat-c9668c000037e6b6`。`a/b` 与 `a_b` 会得到不同的目录；任何 ID 都不会让它变成 `.` 或 `..`。

**核心里用到 Roots 的地方**（T2）：

| 位置 | 行为 |
| --- | --- |
| `/new <agent> [dir]` | `dir` 必须在发送者的 Roots 内，否则回复 `workspace.denied`；不带 `dir` 时用发送者的第一个 root |
| `/ls` 的终端会话条目 | 只列 cwd 在发送者 Roots 内的条目 |
| `/use <n>` 接管终端会话 | 条目的 cwd 必须在发送者 Roots 内，否则回复 `workspace.denied` |
| 审批策略的 `outside_workspace` | 按**会话**的 Roots 判定（`internal/approval`），不再用全局 roots |
| 配对确认 | 被配对的用户拿到自己的目录（有 `[[workspace.users]]` 条目，或开了 `users_root`）时，给他发 `pair.confirmed_workspace`（带 `Dir`），否则照旧发 `pair.confirmed` |

**装配**：`core.Deps` 新增 `Workspaces ports.Workspaces`；`approval.Options` 新增 `Workspaces ports.Workspaces`。两处为 nil 时都退回全局 roots，即 P3 行为，这样现有的单测与测试替身不用改。实现放在 T2 自己的包里（`internal/acl` 或新建 `internal/workspace`，T2 定），由 TΩ 在 `cmd/ferry/assemble.go` 装配。

**不变的**：会话仍按 endpoint 绑定，不改绑定表，不加迁移（3.9）；审批请求仍发往会话的 owner endpoint；谁能批按 3.3。

**与 P1 的兼容**：`users_root` 缺省为空，此时所有已配对用户照旧共用 roots，`TestStrangerPairing` 不受影响。隔离要靠配置打开：默认打开等于改写 P1 B2 的验收。

### 3.5 审计导出（T3）

**命令**：`ferry audit export [--since <时长>] [--format csv]`

| 项 | 规定 |
| --- | --- |
| `--since` | 缺省 `7d`。`<n>d` 表示 n 天；其余照 `time.ParseDuration`（`12h`、`30m`、`1h30m`）；必须大于 0。非法 → stderr 打 `cli.audit_since_invalid`，退出码 2 |
| `--format` | 缺省 `csv`，本批次只支持 `csv`。其他值 → stderr 打 `cli.audit_format_unsupported`，退出码 2 |
| 输出 | 写到 stdout。先输出一行表头 `at,kind,endpoint,sender_id,session_id,detail`，再每条审计一行；没有记录时只有表头，退出码 0 |
| 列 | `at`：UTC，RFC 3339，毫秒精度（Go 格式 `2006-01-02T15:04:05.000Z07:00`，UTC 下以 `Z` 结尾）。`kind`：原样。`endpoint`：`EndpointID.String()`，零值为空串。`sender_id`、`session_id`、`detail`：原样。**只有这六列**，不做二次加工 |
| 顺序 | 按 `at` 升序，同一毫秒内按写入顺序 |
| 编码 | UTF-8，`encoding/csv` 的默认写法（逗号、`\n`、按需加引号） |
| 读路径 | CLI 直接打开数据库（同 `ferry ls`、`ferry pair`），守护进程在不在都能导（WAL 允许并发读）。`store.Open` 返回 `ports.Store`，CLI 类型断言到 `ports.AuditReader`；断言不成立是内部错误，退出码 2。打不开数据库，退出码 2 |
| 不含正文 | 审计表的 `detail` 从 P1 起就约定「never the message body」（`ports.AuditEntry`），导出只输出这六列 |

**端口**（T0 已落地，`internal/ports/audit.go`）：

```go
type AuditReader interface {
	// at 不早于 since 的全部记录，先旧后新；同一毫秒内保持写入顺序。
	// 写入时没有 endpoint 的记录，读回来是零值 endpoint。
	AuditSince(ctx context.Context, since time.Time) ([]AuditEntry, error)
}
```

由 `*store.DB` 实现（T3 新建 `internal/store/audit.go`，写 `var _ ports.AuditReader = (*DB)(nil)`）。它是独立接口：`ports.Store` 加方法会让现有的 Store 实现编译不过，而 `internal/store/store.go` 不归任何任务。

### 3.6 飞书卡片审批（T4；生产方在 T2）

**审批 ID 的载体**（T0 已落地，`internal/ports/approvalkey.go`）：F14 说明，出站消息里唯一能原样到达适配器的是第一段的 `DedupeKey`，所以审批 ID 就放在它上面。

```go
type ApprovalRef struct {
	ID          string // fsp.ApprovalRequest.ID 的五个字母
	AllowAlways bool   // 请求带不带 allow_always 选项
}

// "approval-<id>-<a|o>-<unix 毫秒>"：a 表示提供 allow_always，o 表示不提供；at 取请求的 CreatedAt。
// 时间戳让重用同一 ID 的两次审批互不相同（微信上 DedupeKey 就是网关据以去重的 client_id）。
func ApprovalDedupeKey(ref ApprovalRef, at time.Time) string

// 认得上面的格式，以及发件箱给后续段追加的 "-<n>"；其他键一律返回 false。
func ParseApprovalDedupeKey(key string) (ApprovalRef, bool)
```

- **生产方（T2，`internal/approval`）**：`askOwner` 发出的 `OutApproval`，`DedupeKey = ports.ApprovalDedupeKey(ApprovalRef{ID: req.ID, AllowAlways: 请求含 allow_always 选项}, req.CreatedAt)`。其他消息（超时、已处理等）不设。微信上这个键会成为 iLink 的 `client_id`（`internal/im/wechat/send.go`）：约 30 个字符，分段后缀另加 2–3 个，字符集与现有的 `ob-` 键相同（小写字母、数字、连字符）。假网关不校验长度与字符，真实 iLink 是否接受留给第 9 节第 1 条确认。
- **消费方（T4，`internal/im/feishu`）**：`Send` 时 `DedupeKey` 能被 `ParseApprovalDedupeKey` 解析，就发交互卡片；解析不了，照旧发文本。

**卡片**：

| 项 | 规定 |
| --- | --- |
| 标题 | `approval.card_title`（`ID`） |
| 正文 | `msg.Text` 原样。它已经过 `SanitizeForIM` 与 `MaskSecrets`，可能拼着前面的输出。**用纯文本元素，不用 lark_md**：预览里是 agent 给的命令，不能被当成 markdown 渲染或借机注入链接 |
| 按钮 | 依次为「允许一次」(`y`)、「总是允许」(`a`，仅当 `AllowAlways`)、「拒绝」(`n`)。文字取 `approval.button_allow_once`、`approval.button_allow_always`、`approval.button_reject` |
| 按钮值 | JSON 对象 `{"ferry_approval": "<id>", "verdict": "y" \| "a" \| "n"}` |
| 语言 | `feishu.Options` 新增 `I18n ports.I18n` 与 `Lang func(fsp.EndpointID) fsp.Lang`；`I18n` 为 nil 时退回发文本，现有单测不用改。装配由 TΩ 做（同 notify 的 `langOf`） |
| 卡片格式 | 以飞书开放平台当时的文档为准，T4 在回执里写明依据 |
| 分段 | 发件箱分出的后续段（键带 `-<n>`）也能解析，同样带按钮。点哪张都是同一个审批 |

**回调**：长连接上的卡片回传交互（`OnP2CardActionTrigger`，F18）。按钮值解析成功时，适配器合成一条 `fsp.Inbound`，写进 `Run` 的 `out` 通道：

```
Endpoint   = {feishu, AppID, 点击者 open_id}
SenderID   = 点击者 open_id
MessageID  = "card-" + 回调的 event_id（没有时用 token）
Text       = "<y|a|n> <id>"
ReceivedAt = 现在
```

并在 3 秒内以 toast `approval.card_received` 应答。卡片本身不更新；裁决结果由审批模块照常以 `approval.decided` 文本发出（F15）。于是鉴权、裁决、回执都走核心现有的第 4 步（`ParseVerdict` → `IsApprover` → `Resolve`），**核心一行不改**。按钮值解析不了 → 忽略，不应答 toast。

- 卡片只发往单聊（发送按 open_id，同 P3），回调按点击者构造单聊 endpoint；其余交给核心鉴权。点击者不是审批人 → 核心静默丢弃，与文本一致。
- `Client` 接口是 `internal/im/feishu` 的内部接口，由 T4 自己扩展（发卡片、卡片回调）。`feishutest` 同步提供两样东西：记录卡片（含按钮值）、模拟一次点击。TΩ 的 B5 要用。
- **e2e 影响**：审批改成卡片后，P3 的 `TestFeishuApproval` 用的 `feishuTexts()` 只收 `KindText`，会看不到审批。TΩ 让这个辅助函数把卡片正文也算进去，**断言本身不改**。

### 3.7 系统服务（T5）

**命令**：`ferry service install`、`ferry service uninstall`、`ferry service status`，外加隐藏子命令 `ferry service run`，它是服务管理器拉起的入口，`--help` 里不列。

**服务定义**：Name `ferry`、DisplayName `Ferry`、Description `Ferry carries your coding agents to chat`；可执行文件是当前 `ferry` 的绝对路径，参数为 `service run`，环境变量 `FERRY_HOME` 取安装时解析出的绝对路径。

**三个平台都以安装者本人的身份运行，绝不用 root 或 LocalSystem。** 理由：凭据在个人钥匙串里（F16）；agent 的登录态和配置在个人 home 里；用提权身份跑 agent，会让审批放行的命令拿到提权。

| 平台 | 形态 |
| --- | --- |
| macOS | LaunchAgent（`UserService=true`）、`RunAtLoad=true`、`KeepAlive=true`：用户登录后启动 |
| Linux | systemd 用户单元（`UserService=true`）、`Restart=always`；装完打印 `cli.service_linger`，因为开机后、登录前就启动需要 `loginctl enable-linger` |
| Windows | Windows 服务，`UserName` 取当前账号（`os/user.Current().Username`），`Password` 交互输入（`golang.org/x/term` 不回显，提示 `cli.service_password`；Ferry 不保存），自动启动且 `DelayedAutoStart=true`。不在管理员终端 → `cli.service_needs_admin`；启动报错 1069（缺「作为服务登录」权限）→ `cli.service_logon_right`。服务名 `ferry` 在 Windows 上全机唯一，一台机器装一份 |

**行为**：

- `install`：注册后立即启动，打印 `cli.service_installed`。没有配置 → `cli.need_init`，退出码 1；已经安装 → `cli.service_already_installed`，退出码 1。
- `uninstall`：先停止（没在跑不算错），再注销，打印 `cli.service_uninstalled`。未安装 → 打印 `cli.service_status`（状态 `service.state.not_installed`），退出码 1。
- `status`：打印 `cli.service_status`，其中 `State` 取 `service.state.running`、`stopped`、`not_installed` 或 `unknown`，退出码 0。
- `run`：与 `ferry daemon` 同一套启动逻辑，同一把锁、同一个日志文件；由 kardianos 的 `Run` 承载（Windows 上应答服务管理器，Unix 上处理信号）。`ferry daemon` 保持原样，供前台使用。
- kardianos 不支持的系统 → `cli.service_unsupported`（带原因），退出码 2。
- **测试**：`internal/service` 把 kardianos 包一层接口，CLI 逻辑用假实现测；按平台生成的 `service.Config` 做成纯函数（入参含 goos），在任何平台上都能测。

### 3.8 Windows 补齐（T1）

**T1-a**

- `p4-b1-base` 已含 `windows-ci-green`：`9cb195a` 经 `6b699ee` 合入了 `main`（F4）。所以 T1-a **不写代码**，只做确认：T1 验收第 6 条，加上 push 后的 C3。那 7 个测试文件不在 T1 的可写范围内。

**私密落盘判据（契约）**：

- **Unix**：私密文件 0600、私密目录 0700，由测试断言。
- **Windows**：没有 POSIX 权限位，`os.Chmod` 只切只读属性，`Perm()` 恒为 0666。测试按平台分叉，只在非 Windows 上断言。仓库里已有 6 处这样写（`config_test.go` ×2、`security_test.go` ×3、`control_test.go` ×1），`windows-ci-green` 又加了 2 处。
- **产品侧，本批次不给 Windows 上的私密文件加 ACL。** Windows 上的私密性来自 `FERRY_HOME` 所在目录继承的 ACL：默认的 `%USERPROFILE%\.ferry` 只有本人、SYSTEM 与 Administrators 能访问。`FERRY_HOME` 设到共享位置时，其他本机用户可以读这些文件。这是已知缺口，由 TΩ 写进 SECURITY 文档，留给后续批次。
- **不加「私密文件落盘」端口，也不加权限常量。** 理由：写入点有 6 个（`outbox/file.go`、`im/wechat/media.go`、`security/credentials.go`、`config/config.go`、`control/server.go`、`cmd/ferry/daemon.go`），分属 6 个包，本批次统一改造会让各轨的文件归属相交；常量也解决不了 Windows 上的问题。`internal/fsp` 不在本批次可写范围内。

**T1-b**

- **ConPTY**：Windows 10 1809 及以上，`pty.Supported()` 为 true，`ferry run` 用 go-pty 的 ConPTY；x/term 的原始模式与窗口大小同步照旧。注入与输出清洗照 spec-p3 3.8，**唯一允许的平台差异**是：Windows 上若子进程收不到括号粘贴（ConPTY 对 `ESC[200~` 的处理以实测为准），注入可以改为直接写文本加回车，须在回执里写明实测依据。空闲判定、2 秒内有按键则推迟、退出码透传都不变。ConPTY 不可用 → `cli.run_unsupported`。`run_windows_test.go` 里断言「不支持」的 `TestRunUnsupportedOnWindows` 随之删除或改写：这是契约变更，不算改验收。
- **占用检测**：`internal/handoff` 新增 Windows 进程表，列出 pid、ppid、命令行和工作目录，可用 `golang.org/x/sys/windows`（3.1）。`proc_other.go` 的构建标签相应收窄。判据同 Linux、macOS：命令行匹配 `ProcessMatch`，且工作目录（规范化后；**Windows 上比较不区分大小写**）等于会话目录；跳过守护进程自己的后代；读不到某个进程的目录就跳过它。内置 profile 的 `ProcessMatch` 改成同时认 Windows 命令行（反斜杠路径、`.exe` 后缀、引号），Unix 上的匹配结果不许变。`internal/handoff/busy_test.go` 的两条真实进程测试在 Windows 上不再 Skip。`ports.Handoff.Busy` 的注释已由 T0 改成与此一致。
- **B6（TestBusyRefused）**：T1 按 spec-p2 B4 原文补写，三个平台都跑，新文件 `test/e2e/busy_test.go`。**坑**：e2e 的守护进程和测试在同一个进程里，测试直接 exec 出来的进程是它的后代，会被 Busy 跳过。要用「中间进程启动它之后立即退出」的两跳方式，或给 handoff 注入 selfPID（只对测试可见）。
- **B7（TestPTYRun）**：去掉 `//go:build linux || darwin`。Unix 上的命令保持 spec-p3 B9 原样；Windows 上改用 T1 新增的 Go 夹具 `test/e2e/testdata/lineecho`，逐行读入、回显 `got:<行>`。
- **防恒真**：T1 在 `.github/workflows/ci.yml` 的 test 矩阵里加一步，带 `if: matrix.os == 'windows-latest'` 和 `shell: bash`（windows 执行机默认的 shell 是 pwsh）：

  ```bash
  go test ./test/e2e/ ./internal/handoff/ -count=1 -v \
    -run 'TestBusyRefused|TestPTYRun|TestBusyProcessInCwd|TestBusyIgnoresOwnDescendants' | tee win-b4-b9.txt
  for t in TestBusyRefused TestPTYRun TestBusyProcessInCwd TestBusyIgnoresOwnDescendants; do
    grep -q -- "--- PASS: $t " win-b4-b9.txt || { echo "$t did not PASS on windows"; exit 1; }
  done
  ```

  出现 `--- SKIP`，或少任何一个名字，这一步就红。

### 3.9 存储：本批次不加迁移

`internal/store/migrations/**` 本批次不动。理由：`longoutput_test.go:30` 钉死了 schema version = 2（F20），而那个文件不归任何任务；五轨也都不需要新表或新列。多用户按配置加目录布局实现，会话仍按 endpoint 绑定；审计导出读现有的 `audit` 表。导出按 `at` 过滤做一次全表扫描：按 7 天、每天几百条审计估算，在万行以内，可以接受。以后要加索引或列的那一批，由该批的 T0 连同那条测试一起改。

### 3.10 控制协议：本批次不改

`internal/control/types.go`、`client.go`、`client_test.go` 均未改动。五轨都不需要新 op：

- 审计导出直接读库，守护进程不在也要能导。
- 服务状态来自服务管理器，加上现有的 status 文件（`ferry status`）。
- 卡片回调经飞书长连接进入适配器。
- 多用户与 Windows 补齐都发生在守护进程内部。

### 3.11 i18n 新增键（T0 已加齐中英两份）

| 键 | 中文 | 使用方 |
| --- | --- | --- |
| `pair.confirmed_workspace` | 配对成功，现在可以使用了。你的工作目录：{{.Dir}} | T2 |
| `cli.audit_since_invalid` | --since 需要形如 7d、12h、30m 的正时长，收到的是 {{.Value}} | T3 |
| `cli.audit_format_unsupported` | 不支持导出为 {{.Format}}，目前只支持 csv | T3 |
| `approval.card_title` | 审批 {{.ID}} | T4 |
| `approval.button_allow_once` / `approval.button_allow_always` / `approval.button_reject` | 允许一次 / 总是允许 / 拒绝 | T4 |
| `approval.card_received` | 已收到，正在处理 | T4 |
| `cli.service_installed` | 已安装并启动系统服务 {{.Name}} | T5 |
| `cli.service_uninstalled` | 已停止并卸载系统服务 {{.Name}} | T5 |
| `cli.service_already_installed` | 系统服务 {{.Name}} 已经安装；要重装请先运行 ferry service uninstall | T5 |
| `cli.service_status` | 系统服务 {{.Name}}：{{.State}} | T5 |
| `service.state.running` / `stopped` / `not_installed` / `unknown` | 运行中 / 已停止 / 未安装 / 状态未知 | T5 |
| `cli.service_needs_admin` | 需要管理员权限，请在管理员终端里重试 | T5 |
| `cli.service_password` | 请输入 {{.User}} 的 Windows 登录密码（服务以这个账号运行，密码只交给 Windows 服务管理器，Ferry 不保存） | T5 |
| `cli.service_logon_right` | Windows 拒绝以 {{.User}} 身份启动服务：这个账号缺少「作为服务登录」权限。请在「本地安全策略 → 用户权限分配 → 作为服务登录」里加入它后重试 | T5 |
| `cli.service_linger` | 已作为用户服务安装。要在开机后、登录前就启动，请运行：loginctl enable-linger {{.User}} | T5 |
| `cli.service_unsupported` | 当前系统不支持安装为系统服务：{{.Reason}} | T5 |

T1 不新增键：沿用 `cli.run_unsupported`、`handoff.busy`、`handoff.busy_unknown`。

### 3.12 失败面

| 场景 | 行为 |
| --- | --- |
| 创建用户目录失败（`users_root` 下） | `Roots` 返回错误；`/new` 回复 `workspace.denied`（`Dir` 为该用户目录），记 error 日志 |
| 审批判定时 `Roots` 出错 | 按「在工作区外」处理（最严） |
| `workspace.users` 条目里写的是 owner 的 ID | 不生效，owner 永远用 `workspace.roots` |
| 显式空的审批人名单 | 配置校验报错，守护进程不启动（3.2） |
| 未配置的 `channel-*` 平台，或 `fake` | 审批人名单为 `["owner"]`（3.3） |
| `--since` 非法 / `--format` 不是 csv | 退出码 2，stderr 打对应的键 |
| 审计库打不开 / store 不实现 `AuditReader` | 退出码 2 |
| 没有审计记录 | 只输出表头，退出码 0 |
| 卡片发送失败（飞书拒收、应用没开卡片能力） | 同一条消息退回发纯文本，文本审批照样可用；记 warn |
| 卡片按钮值解析不了 | 忽略，不应答 toast |
| 点击者不是审批人 | 核心静默丢弃（同文本） |
| 同一审批点了两次，或点击与文本同时到 | 先到先得，后到的 `Resolve` 返回 false，被丢弃 |
| 服务安装：Windows 无管理员权限 / 1069 | `cli.service_needs_admin` / `cli.service_logon_right`，退出码 1 |
| 服务安装：系统不受支持（如 Linux 没有 systemd 用户实例） | `cli.service_unsupported`，退出码 2 |
| 服务运行时凭据读不到（如 Linux 开机未登录，钥匙串未解锁） | 守护进程按现有逻辑报错退出，服务管理器按 `Restart`、`KeepAlive` 重启；文档写明 linger 与钥匙串的限制 |
| Windows 上读不到某个进程（权限不足） | 跳过该进程 |
| Windows 上整个进程表都读不到 | Busy 返回错误，核心按「无法确认」回复 `handoff.busy_unknown`（spec-p2 既有规则） |
| ConPTY 不可用（Windows 10 1809 以前） | `cli.run_unsupported` |

### 3.13 文件归属表

| 路径 | owner | 其他任务 |
| --- | --- | --- |
| `internal/ports/**`（含 `deps/`）、`internal/config/types.go`、`internal/config/config.go`、`internal/control/types.go`、`internal/control/client.go`、`internal/control/client_test.go`、`internal/store/migrations/**`、`locales/*.toml`、`go.mod`、`go.sum` | T0 | 只读，改动即任务失败 |
| `.github/workflows/ci.yml`；`internal/handoff/**`；`internal/driver/pty/**`；`cmd/ferry/run.go`；`test/e2e/pty_test.go`、`test/e2e/busy_test.go`（新）、`test/e2e/testdata/lineecho/**`（新）、`test/e2e/harness_test.go`（B6 若要在 rig 里注入 selfPID） | T1 | 不可改 |
| `internal/acl/**`、`internal/core/**`、`internal/approval/**`、`internal/workspace/**`（若新建） | T2 | 不可改 |
| `internal/store/audit.go`、`internal/store/audit_test.go`（均新建）、`internal/audit/**`（新）、`cmd/ferry/audit.go`（新） | T3 | 不可改 |
| `internal/im/feishu/**`（含 `feishutest`） | T4 | 不可改 |
| `internal/service/**`（新）、`cmd/ferry/service.go`（新）、`cmd/ferry/daemon.go` | T5 | 不可改 |
| `cmd/ferry/**`（上述文件除外）、`test/e2e/**`（上述 T1 的文件除外；`harness_test.go` 在合入 `p4-t1` 之后可改）、`README.md`、`README.zh-CN.md`、`SECURITY.md`、`SECURITY.zh-CN.md`、`docs/en/**`、`docs/zh-CN/**` | TΩ | 不可改 |
| 其余已有产物（`internal/store`（T3 的两个新文件除外）、`internal/outbox/**`、`internal/registry/**`、`internal/im/wechat/**`、`internal/im/channelhost/**`、pty 以外的驱动、`internal/fsp/**`、`internal/security/**`、`internal/deps/**` 等；`windows-ci-green` 改过的 7 个测试文件也在此列） | 无 | 只读 |
| `docs/dev/**`、`LICENSE` | 人工 | 只读 |

`internal/acl/**` 在本批次对 T0 开放过，但 T0 没有改它（Q1–Q3 的实现归 T2）；T0 提交之后它归 T2。

## 4. 基线

- **T0 起点**：`main` 上的 `0492443`，工作区干净。T0 在 `.worktrees/task-t0`、分支 `p4-t0` 上完成。
- **T0 之后 `main` 前进到 `6b699ee`**（合入 `windows-ci-green`，F4）。`p4-t0` 与它改的文件不相交，`git merge-tree` 试合并无冲突。
- **并行基线**：标签 `p4-b1-base`，即总管把 `p4-t0` 合入 `main`（`6b699ee` 之上）、复跑 T0 的 7 项验收之后的那个提交（复跑第 7 项的注意事项见开工前准备 2）。**所以基线已经含 T1-a 的修复。** worktree 钉该标签对应的**具体 sha**，不钉分支名。
- **开场自检期望值**（T0 提交时实测；总管派单前在新 worktree 里复测刷新）：`go build ./...` 退出码 0；`go test ./... -count=1` 为 `ok` 31 条、`FAIL` 0 条（新增的 `internal/ports/deps` 没有测试文件，不计入 ok）；e2e `--- PASS` 20 条；`check-i18n` 为 `i18n ok`。`windows-ci-green` 只改 7 个已有的测试文件，不加包，也不加测试函数（已核对），所以合入后这几个数不变。以实跑为准。

## 5. 任务表

| 任务 | 目标 | 时序 |
| --- | --- | --- |
| T0 | 依赖、配置类型与校验、端口（Workspaces、AuditReader、审批键）、i18n 键、本文件 | 已完成；打 `p4-b1-base` 后其余才起 |
| T1 | T1-a 确认 Windows 修复已在基线；T1-b ConPTY、Windows 占用检测、补写 B6、放开 B7、CI 防恒真检查 | 并行 |
| T2 | owner 全 bot 通认、`ApproversFor` 接入 ACL、`Workspaces` 实现、核心与审批策略按用户 Roots、审批消息带键 | 并行 |
| T3 | `AuditReader` 的实现、`ferry audit export` | 并行 |
| T4 | 卡片发送、按钮回调合成文本裁决、`feishutest` | 并行 |
| T5 | `internal/service`、`ferry service` | 并行 |
| TΩ | 合流、装配、e2e B1、B2、B4、B5、文档 | 最后 |

### 5.1 T1 细则

- T1-a 按 3.8。
- T1-b 按 3.8，测试至少包含：
  - `internal/handoff`：`TestBusyProcessInCwd`、`TestBusyIgnoresOwnDescendants`（Windows 上不再 Skip）；`TestProcessMatchWindowsCommandLine`（纯正则，各平台都跑：内置 profile 能认 `"C:\…\kimi.exe" acp` 一类命令行，Unix 命令行的结果不变）；`TestCwdCompareIgnoresCaseOnWindows`；`TestWindowsProcessTable`（只在 Windows 构建：进程表里有测试进程自己，带命令行与工作目录）
  - `internal/driver/pty`：现有测试照旧通过；Windows 上 `TestRunConPTYEcho` 替代 `TestRunUnsupportedOnWindows`
  - `test/e2e`：`TestBusyRefused`（新）、`TestPTYRun`（三平台）
  - `.github/workflows/ci.yml`：3.8 的检查步骤

### 5.2 T2 细则

- 按 3.3、3.4 实现，并在 `internal/approval` 给审批消息设键（3.6 的生产方）。
- 测试至少包含：
  - `internal/acl`：`TestOwnerRecognisedOnEveryEndpoint`、`TestOwnerNotRecognisedOnAnotherBot`、`TestApproversForEntry`、`TestChannelApproversOwnList`、`TestOwnerKeywordMeansBotOwner`；按 3.3 改写 `TestApproversArePerPlatform`
  - `Workspaces` 的实现所在包：`TestRootsOwner`、`TestRootsExplicitUser`、`TestRootsUsersRootCreatesDir`（0700，路径 = `UserDirName`）、`TestRootsSharedWithoutUsersRoot`
  - `internal/core`：`TestNewDefaultsToUserRoot`、`TestNewRefusedOutsideUserRoots`、`TestLsHidesTerminalSessionsOutsideRoots`、`TestUseTerminalOutsideRootsRefused`、`TestPairConfirmedTellsWorkspace`、`TestNilWorkspacesKeepsSharedRoots`、`TestOwnerNoFromPrefixInAnyChat`
  - `internal/approval`：`TestOutsideWorkspaceUsesSessionRoots`、`TestNilWorkspacesKeepsGlobalRoots`、`TestApprovalMessageCarriesKey`、`TestApprovalKeyMarksAllowAlways`

### 5.3 T3 细则

- 按 3.5 实现。解析 `--since` 与写 CSV 放在新包 `internal/audit`，CLI 在 `cmd/ferry/audit.go`。
- 测试至少包含：
  - `internal/store`：`TestAuditSinceOrder`、`TestAuditSinceFilters`、`TestAuditSinceZeroEndpoint`；已有的 store 测试仍通过（包括 schema version = 2）
  - `internal/audit`：`TestParseSince`、`TestParseSinceRejects`（`0`、负数、`7x`、空串）、`TestCSVHeader`、`TestCSVRowFormat`（时间格式、endpoint 串、引号）、`TestCSVOnlySixColumns`

### 5.4 T4 细则

- 按 3.6 实现。真实 SDK 的部分只做薄封装，逻辑全部经 `Client` 接口测试；`feishutest` 提供记录卡片与模拟点击。
- 测试至少包含：`TestApprovalKeySendsCard`、`TestCardButtonsFollowAllowAlways`、`TestCardBodyIsPlainMessageText`、`TestCardFallsBackToTextOnError`、`TestNoKeySendsText`、`TestNoI18nSendsText`、`TestCardActionBecomesVerdict`、`TestCardActionForeignValueIgnored`、`TestCardActionMessageIDUnique`、`TestCardActionAnswersToast`、`TestSegmentKeyAlsoGetsButtons`；P3 的飞书测试仍通过

### 5.5 T5 细则

- 按 3.7 实现。
- 测试至少包含：`TestConfigDarwin`、`TestConfigLinux`、`TestConfigWindows`（纯函数，各平台都跑）、`TestInstallRegistersAndStarts`、`TestInstallAlreadyInstalled`、`TestInstallNeedsInit`、`TestUninstallStopsAndRemoves`、`TestUninstallNotInstalled`、`TestStatusStates`、`TestWindowsPasswordNotStored`、`TestLogonRightHint`、`TestLingerHintOnLinux`

### 5.6 TΩ 细则

- 合并顺序：`p4-t1` → `p4-t3` → `p4-t5` → `p4-t4` → `p4-t2`。一次合一个（`git merge --no-ff`），合完立刻跑 `go build ./... && go test ./...`，全绿再合下一个。
- 装配（`cmd/ferry/assemble.go`）：T2 的 `Workspaces` 接到 `core.Deps.Workspaces` 与 `approval.Options.Workspaces`；`feishu.Options` 的 `I18n` 接 `a.Bundle`，`Lang` 接 `langOf(st, cfg.Lang)`。
- 端到端：在 `test/e2e/p4_test.go` 里按 2.2 实现 B1、B2、B4、B5；按 3.6 让 `feishuTexts()` 把卡片正文也算进去；B6、B7 由 T1 提供。
- 文档（中英两份）：新增 `docs/en/multi-user.md`、`docs/en/service.md` 及其中文版；`feishu.md` 补卡片审批，以及在开放平台开启卡片回传交互的步骤；`security.md` 与 `SECURITY*.md` 补三件事：owner 全 bot 通认与 channel server 必须携带发送者 ID（3.3）、Windows 私密文件的已知缺口（3.8）、服务以本人身份运行（3.7）；`handoff.md` 补 Windows 占用检测；`pty.md` 补 Windows；README 各补一句 `ferry audit export` 与 `ferry service`。保持 check-i18n 通过。

## 6. 每个任务的验收

| 任务 | 必须通过（命令见各自提示词） |
| --- | --- |
| T0 | G1–G3、全量单测 31 ok、e2e 20 PASS、check-i18n、`git diff --name-only` 白名单，共 7 项（已完成） |
| T1 | handoff 与 pty 两组测试、重复 3 次稳定、B6 与 B7 在本机 PASS、Windows 交叉 vet、Windows 测试文件清单、基线含 Windows 修复、全量单测、G1–G3、白名单，共 11 项；外加总管 push 后的 C3 |
| T2 | acl、core、approval 三组测试、重复 3 次稳定、e2e 20 PASS、全量单测、G1–G3、白名单，共 10 项 |
| T3 | store、audit 两组测试、CLI 的 help、正常导出、两种非法参数、全量单测、G1–G3、白名单，共 11 项 |
| T4 | feishu 测试、重复 3 次稳定、P3 的两条飞书 e2e、全量单测、G1–G3、白名单，共 8 项 |
| T5 | service 测试、重复 3 次稳定、CLI 的 help、status、Linux 与 Windows 交叉 vet、全量单测、e2e 20 PASS、G1–G3、白名单，共 11 项 |
| TΩ | A1–A3、B1–B9、C1–C2，共 14 项；外加 push 后的 C3 |

## 7. 卡住了怎么办

见 00 总览 3.8。本批次另外约定：

1. 任何一轨需要改 T0 的文件（`internal/ports/**`、`internal/config/types.go`、`config.go`、`locales/*.toml`、`go.mod`、`go.sum`）→ 停，报告。
2. 发现某条现有 e2e 的断言与本契约冲突 → 停，报告，不要改 e2e。3.3 点名的 `TestApproversArePerPlatform`、3.6 点名的 `feishuTexts()`、3.8 点名的 `TestRunUnsupportedOnWindows` 是已裁定的例外。
3. T1：ConPTY 或 Windows 进程表的行为与 3.8 冲突（例如读不到其他进程的工作目录，导致 B6 在 CI 上不稳定）→ 停，报告实测。
4. T4：长连接收不到 `card.action.trigger` → 停，报告。不要自建 HTTP 回调服务。
5. T5：kardianos 的行为与 3.7 不一致（例如 Windows 上无法以用户账号注册）→ 停，报告。

## 8. 派工提示词

P4-T0 已按派单执行，产出即本文件与第 3 节标「T0 已落地」的代码，这里不再重复。以下提示词里的 `<p4-b1-base 的 sha>`、自检条数，由总管在建好 worktree、实跑之后填入（开工前准备 3）。

### P4-T1

````
# 任务 P4-T1 — Windows 补齐（T1-a 让 Windows CI 转绿 + T1-b ConPTY 与占用检测）

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-t1，分支 p4-t1，基线 = p4-b1-base（<p4-b1-base 的 sha>）。
不要 git worktree add，不要 cd 去主仓库，不要 push，不要打标签。

## 开场自检（全对上才开工）
1. 用 Read 工具读 .claude/hooks/track-guard.py → 必须被拦，理由第一行是「[守卫在线] …」
2. git log --oneline -1 → <p4-b1-base 的 sha 与标题>
3. git status --short → 无输出
4. go build ./... ; echo exit=$? → exit=0
5. go test ./... -count=1 → ok <31> 条、FAIL 0 条
6. go test ./test/e2e/... -count=1 -v | grep -c '^--- PASS' → <20>
第 1 条读到了内容 = 守卫没挂上，停下报告；第 2、5、6 条对不上 = 基线不对，停下报告，别当回归去修。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p2.md 2.2 的 B4（TestBusyRefused 的原文）
- docs/dev/spec-p3.md 3.8（ferry run）与 2.2 的 B9
- docs/dev/spec-p4-b1.md 第 0 节（F2–F8）、1.1、3.1、3.8、3.12、3.13、5.1

## 目标
T1-a：基线已含 windows-ci-green（经 6b699ee 合入 main），只确认、不写代码，并遵守 3.8 的私密落盘判据（不写 ACL 代码）。
T1-b：ferry run 在 Windows 上走 ConPTY；internal/handoff 在 Windows 上读进程表做占用检测；补写 TestBusyRefused（三平台）；放开 TestPTYRun 的构建标签（Windows 用 lineecho 夹具）；在 CI 里加 3.8 的防恒真检查。

## 可写文件（只有这些）
- .github/workflows/ci.yml
- internal/handoff/**
- internal/driver/pty/**
- cmd/ferry/run.go
- test/e2e/pty_test.go、test/e2e/busy_test.go（新）、test/e2e/testdata/lineecho/**（新）
- test/e2e/harness_test.go（只在 B6 需要往 rig 里注入 selfPID 时改）

## 禁改
- docs/dev/**、LICENSE
- internal/ports/**、internal/config/**、internal/security/**、internal/fsp/**、locales/**、go.mod、go.sum（不许跑 go mod tidy）
- 白名单以外的任何文件

## 验收（11 项）
1. go test ./internal/handoff/... ./internal/driver/pty/... -count=1 -v → 全部 PASS，含 5.1 列出的测试名（Windows 专属的除外）
2. go test ./internal/handoff/... ./internal/driver/pty/... -count=3 → ok
3. go test ./test/e2e/ -run 'TestBusyRefused|TestPTYRun' -count=1 -v | grep -E '^--- (PASS|FAIL|SKIP)' → 两行，都是 PASS
4. GOOS=windows go vet ./internal/handoff/... ./internal/driver/pty/... ./cmd/ferry/ ./test/e2e/... ; echo exit=$? → 无输出，exit=0
5. GOOS=windows go list -f '{{join .TestGoFiles " "}}' ./test/e2e/ → 含 busy_test.go 与 pty_test.go
6. git merge-base --is-ancestor 6b699ee HEAD ; echo exit=$? → exit=0（基线含 main 上合入 windows-ci-green 的那个提交）
7. go test ./... -count=1 ; echo exit=$? → 全部 ok、FAIL 0 条，exit=0
8. go build ./... ; echo exit=$? → exit=0
9. go vet ./... ; echo exit=$? → 无输出，exit=0
10. test -z "$(gofmt -l .)" ; echo exit=$? → exit=0
11. git diff --name-only <p4-b1-base 的 sha> → 每一行都在「可写文件」里
总管 push p4-t1 之后，CI 的 7 个 job 全绿，且 windows 上新加的那一步显示 4 个 PASS（spec-p4-b1 2.3 C3）。这条由总管核对，不计入上面的 11 项。

## 完成后
git add -A && git commit -m "p4-t1: windows ci green, conpty and busy detection"

回执正文：每条验收命令的原文、输出尾部与 exit code；ConPTY 括号粘贴的实测结论（3.8）；改动文件列表；若 blocked，写清卡点、试过什么、需要什么决定。最后一行严格为：
RECEIPT P4-T1 status=<done|blocked> commit=<短hash> checks=<通过数>/11 files=<改动文件数>
````

### P4-T2

````
# 任务 P4-T2 — 多用户隔离（owner 全 bot 通认、按入口的审批人、按用户的工作区）

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-t2，分支 p4-t2，基线 = p4-b1-base（<p4-b1-base 的 sha>）。
不要 git worktree add，不要 cd 去主仓库，不要 push，不要打标签。

## 开场自检（全对上才开工）
与 P4-T1 相同的 6 条（守卫被拦、git log、git status、go build、ok <31> / FAIL 0、e2e <20>）。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p1.md 3.6（入站 11 步）与审批规则
- docs/dev/spec-p4-b1.md 第 0 节（F9–F14）、3.2、3.3、3.4、3.6 的「生产方」、3.12、3.13、5.2

## 目标
按 3.3 实现 owner 全 bot 通认，并让 ACL 只经 config.ApproversFor 取审批人名单；按 3.4 实现 ports.Workspaces，把核心（/new、/ls 的终端条目、/use 接管、配对提示）和审批策略的 outside_workspace 改成按用户或会话的 Roots；按 3.6 让审批消息带上 ports.ApprovalDedupeKey。Workspaces 为 nil 时一切保持 P3 行为。

## 可写文件（只有这些）
- internal/acl/**
- internal/core/**
- internal/approval/**
- internal/workspace/**（如果你决定把 Workspaces 的实现放在新包里）

## 禁改
- docs/dev/**、LICENSE
- internal/ports/**、internal/config/**、internal/store/**、locales/**、go.mod、go.sum
- test/e2e/**（e2e 由 TΩ 写；现有的 20 条一条都不许红）
- 白名单以外的任何文件

## 验收（10 项）
1. go test ./internal/acl/... -count=1 -v → 全部 PASS，含 5.2 列出的 acl 与 Workspaces 测试
2. go test ./internal/core/... -count=1 -v → 全部 PASS，含 5.2 列出的核心测试，且已有核心测试仍通过
3. go test ./internal/approval/... -count=1 -v → 全部 PASS，含 5.2 列出的审批测试
4. go test ./internal/acl/... ./internal/core/... ./internal/approval/... -count=3 → ok
5. go test ./test/e2e/... -count=1 -v | grep -c '^--- PASS' → 20（尤其是 TestStrangerPairing、TestFeishuApproval、TestChannelHost）
6. go test ./... -count=1 ; echo exit=$? → 全部 ok，exit=0
7. go build ./... ; echo exit=$? → exit=0
8. go vet ./... ; echo exit=$? → 无输出，exit=0
9. test -z "$(gofmt -l .)" ; echo exit=$? → exit=0
10. git diff --name-only <p4-b1-base 的 sha> → 每一行都在「可写文件」里

## 完成后
git add -A && git commit -m "p4-t2: per-user workspaces and bot-wide owner"

回执正文：每条验收命令的原文、输出尾部与 exit code；Workspaces 的实现放在哪个包、构造函数签名（TΩ 装配要用）；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P4-T2 status=<done|blocked> commit=<短hash> checks=<通过数>/10 files=<改动文件数>
````

### P4-T3

````
# 任务 P4-T3 — 审计导出（ferry audit export）

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-t3，分支 p4-t3，基线 = p4-b1-base（<p4-b1-base 的 sha>）。
不要 git worktree add，不要 cd 去主仓库，不要 push，不要打标签。

## 开场自检（全对上才开工）
与 P4-T1 相同的 6 条。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p4-b1.md 3.5、3.9、3.11、3.12、3.13、5.3
- internal/ports/store.go 的 AuditEntry，internal/store/acl.go 的 Audit（写入格式）

## 目标
在 *store.DB 上实现 ports.AuditReader；新包 internal/audit 负责解析 --since 和写 CSV；cmd/ferry/audit.go 注册 `ferry audit export`。格式、顺序、退出码严格照 3.5。

## 可写文件（只有这些）
- internal/store/audit.go（新）、internal/store/audit_test.go（新）
- internal/audit/**（新）
- cmd/ferry/audit.go（新）

## 禁改
- docs/dev/**、LICENSE
- internal/ports/**、internal/store 的其他文件（含 migrations/**：本批次不加迁移，见 3.9）、locales/**、go.mod、go.sum
- 白名单以外的任何文件

## 验收（11 项）
1. go test ./internal/store/... -count=1 -v → 全部 PASS，含 TestAuditSinceOrder、TestAuditSinceFilters、TestAuditSinceZeroEndpoint，且已有 store 测试仍通过
2. go test ./internal/audit/... -count=1 -v → 全部 PASS，含 5.3 列出的测试
3. go run ./cmd/ferry audit export --help ; echo exit=$? → 显示用法，exit=0
4. FERRY_HOME=$(mktemp -d) go run ./cmd/ferry audit export --since 7d ; echo exit=$? → 只有一行 at,kind,endpoint,sender_id,session_id,detail，exit=0
5. FERRY_HOME=$(mktemp -d) go run ./cmd/ferry audit export --since 7x ; echo exit=$? → stderr 为 cli.audit_since_invalid 的文本，exit=2
6. FERRY_HOME=$(mktemp -d) go run ./cmd/ferry audit export --format json ; echo exit=$? → stderr 为 cli.audit_format_unsupported 的文本，exit=2
7. go test ./... -count=1 ; echo exit=$? → 全部 ok，exit=0
8. go build ./... ; echo exit=$? → exit=0
9. go vet ./... ; echo exit=$? → 无输出，exit=0
10. test -z "$(gofmt -l .)" ; echo exit=$? → exit=0
11. git diff --name-only <p4-b1-base 的 sha> → 每一行都在「可写文件」里

## 完成后
git add -A && git commit -m "p4-t3: audit export"

回执正文：每条验收命令的原文、输出尾部与 exit code；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P4-T3 status=<done|blocked> commit=<短hash> checks=<通过数>/11 files=<改动文件数>
````

### P4-T4

````
# 任务 P4-T4 — 飞书卡片审批

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-t4，分支 p4-t4，基线 = p4-b1-base（<p4-b1-base 的 sha>）。
不要 git worktree add，不要 cd 去主仓库，不要 push，不要打标签。

## 开场自检（全对上才开工）
与 P4-T1 相同的 6 条。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p3.md 3.4（飞书适配器）
- docs/dev/spec-p4-b1.md 第 0 节（F14、F15、F18）、3.6、3.11、3.12、3.13、5.4
- 飞书开放平台的卡片 JSON 与「卡片回传交互」文档（以当时版本为准）

## 目标
飞书适配器的 Send 遇到能被 ports.ParseApprovalDedupeKey 解析的 DedupeKey 时，发交互卡片（标题、纯文本正文、按钮与按钮值严格照 3.6）；长连接上的卡片回调合成 "<y|a|n> <id>" 的 fsp.Inbound 交给核心；feishu.Options 新增 I18n 与 Lang；feishutest 提供记录卡片与模拟点击。核心、审批模块都不改。

## 可写文件（只有这些）
- internal/im/feishu/**（含 feishutest）

## 禁改
- docs/dev/**、LICENSE
- internal/ports/**、internal/config/**、internal/core/**、internal/approval/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

测试不访问真实飞书；真实的卡片与回调留给人工验收。

## 验收（8 项）
1. go test ./internal/im/feishu/... -count=1 -v → 全部 PASS，含 5.4 列出的测试，且 P3 的飞书测试仍通过
2. go test ./internal/im/feishu/... -count=3 → ok
3. go test ./test/e2e/ -run 'TestFeishuFake|TestFeishuApproval' -count=1 -v | grep -E '^--- (PASS|FAIL)' → 两行，都是 PASS
4. go test ./... -count=1 ; echo exit=$? → 全部 ok，exit=0
5. go build ./... ; echo exit=$? → exit=0
6. go vet ./... ; echo exit=$? → 无输出，exit=0
7. test -z "$(gofmt -l .)" ; echo exit=$? → exit=0
8. git diff --name-only <p4-b1-base 的 sha> → 每一行都在「可写文件」里

## 完成后
git add -A && git commit -m "p4-t4: feishu approval cards"

回执正文：每条验收命令的原文、输出尾部与 exit code；卡片 JSON 的格式依据（文档链接与日期）；feishutest 里模拟点击的用法（TΩ 的 B5 要用）；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P4-T4 status=<done|blocked> commit=<短hash> checks=<通过数>/8 files=<改动文件数>
````

### P4-T5

````
# 任务 P4-T5 — 系统服务（ferry service）

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-t5，分支 p4-t5，基线 = p4-b1-base（<p4-b1-base 的 sha>）。
不要 git worktree add，不要 cd 去主仓库，不要 push，不要打标签。

## 开场自检（全对上才开工）
与 P4-T1 相同的 6 条。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p4-b1.md 第 0 节（F16、F17）、3.1、3.7、3.11、3.12、3.13、5.5
- cmd/ferry/daemon.go（ferry service run 要复用它的启动逻辑）

## 目标
新包 internal/service 把 kardianos/service v1.3.0 包一层接口；cmd/ferry/service.go 注册 install、uninstall、status 与隐藏的 run；三个平台都以安装者本人身份运行，形态、提示与退出码严格照 3.7。需要时可以在 cmd/ferry/daemon.go 里把启动逻辑抽成函数，但 ferry daemon 的行为不许变。

## 可写文件（只有这些）
- internal/service/**（新）
- cmd/ferry/service.go（新）
- cmd/ferry/daemon.go

## 禁改
- docs/dev/**、LICENSE
- internal/ports/**（含 deps/）、internal/config/**、locales/**、go.mod、go.sum
- 白名单以外的任何文件

测试不在本机真的安装服务；真实安装留给人工验收。

## 验收（11 项）
1. go test ./internal/service/... -count=1 -v → 全部 PASS，含 5.5 列出的测试
2. go test ./internal/service/... -count=3 → ok
3. go run ./cmd/ferry service --help ; echo exit=$? → 列出 install、uninstall、status（不列 run），exit=0
4. FERRY_HOME=$(mktemp -d) go run ./cmd/ferry service status ; echo exit=$? → 打印 cli.service_status 的文本（状态为「未安装」或「状态未知」），exit=0
5. GOOS=windows go vet ./internal/service/... ./cmd/ferry/ && GOOS=linux go vet ./internal/service/... ./cmd/ferry/ ; echo exit=$? → 无输出，exit=0
6. go test ./... -count=1 ; echo exit=$? → 全部 ok，exit=0
7. go test ./test/e2e/... -count=1 -v | grep -c '^--- PASS' → 20（动了 daemon.go 也不许掉）
8. go build ./... ; echo exit=$? → exit=0
9. go vet ./... ; echo exit=$? → 无输出，exit=0
10. test -z "$(gofmt -l .)" ; echo exit=$? → exit=0
11. git diff --name-only <p4-b1-base 的 sha> → 每一行都在「可写文件」里

## 完成后
git add -A && git commit -m "p4-t5: system service"

回执正文：每条验收命令的原文、输出尾部与 exit code；三个平台各自生成的服务配置摘要；改动文件列表；若 blocked，写清卡点与需要的决定。最后一行严格为：
RECEIPT P4-T5 status=<done|blocked> commit=<短hash> checks=<通过数>/11 files=<改动文件数>
````

### P4-TΩ

````
# 任务 P4-TΩ — 合流、装配、端到端与文档

## 工作区
worktree 已由总管建好：/Users/shensikai/Documents/Projects/Ferry/.worktrees/task-tomega，分支 p4-tomega，基线 = p4-b1-base（<p4-b1-base 的 sha>）。开始前确认 git status --short 为空。
不要 push，不要打标签。

## 先读
- docs/dev/00-overview.md 第 3 节
- docs/dev/spec-p4-b1.md 全文，重点第 2 节、3.13、5.6
- 五条回执（尤其 T2 的 Workspaces 构造函数签名、T4 的 feishutest 用法）

## 第一步：合流
按顺序合并：p4-t1 → p4-t3 → p4-t5 → p4-t4 → p4-t2。
一次合一个：git merge --no-ff <分支>，合完立刻跑 go build ./... && go test ./...，全绿再合下一个。
冲突按 3.13 的归属表判定；表里没写的，停下来报告。合流阶段只修集成缝隙，不补漏掉的功能。

## 第二步：装配、端到端与文档
按 5.6 完成装配；在 test/e2e/p4_test.go 里实现 B1、B2、B4、B5，并按 3.6 调整 feishuTexts()（断言不改）；按 5.6 更新文档。

## 可写文件
- 合流时：解决冲突所需的文件
- 第二步：spec-p4-b1 3.13 中 owner 为 TΩ 的路径

## 禁改
- docs/dev/**、LICENSE、internal/ports/**、internal/config/**、locales/**、go.mod、go.sum

## 全量验收（14 项）
1. go build ./... && go test ./... → 全部 ok
2. go run ./cmd/ferry --help → 含 audit 与 service
3. go run ./cmd/ferry audit export --help && go run ./cmd/ferry service --help → 退出码 0
4–9. go test ./test/e2e/ -run <名称> -count=1 -v，名称依次为 TestMultiUserIsolation、TestOwnerAnyChat、TestAuditExport、TestFeishuCardApproval、TestBusyRefused、TestPTYRun → 每个都 PASS
10. go test ./internal/acl/ -run 'TestApproversForEntry|TestChannelApproversOwnList' -v → PASS
11. go test ./internal/service/... -v → PASS
12. go run ./scripts/check-i18n → i18n ok
13. go test ./test/e2e/ -count=1 -v | grep -c '^--- PASS' → 25（P1、P3 的 20 条 + 本轮 5 条）
14. go vet ./... && test -z "$(gofmt -l .)" → 退出码 0
push 之后的 C3（CI 7 个 job 全绿、Windows 那一步显示 4 个 PASS）由总管核对。

## 完成后
git add -A && git commit -m "p4-tomega: wire multi-user, audit, cards and service"

回执正文：每次合入后的验收结果；冲突与判定依据；14 项验收的真实输出关键行；还差什么。最后一行严格为：
RECEIPT P4-TΩ status=<done|blocked> merged=<合入数>/5 checks=<通过数>/14
````

## 9. 人工验收清单

| # | 操作 | 期望 |
| --- | --- | --- |
| 1 | 配置 `workspace.users_root`；用第二个微信号配对；两边各自 `/new` 并 `/ls`；再在你自己的会话里触发一次审批 | 第二个号的会话落在它自己的目录；它 `/new` 进你的目录被拒；两边互相看不到会话；它的 `/ls` 里没有终端会话条目；审批消息照常收到，说明 iLink 接受 3.6 的审批键作为 `client_id` |
| 2 | 用官方 fakechat 当 `channel_servers`（`chat_id` 与 `owner` 不同），发消息并触发一次审批 | 不收到配对码；审批按该 server 的 `approvers` 裁决 |
| 3 | 飞书开放平台开启卡片回传交互后，飞书私聊触发一次审批，点「允许一次」 | 收到卡片；点击后 agent 继续；toast 显示「已收到，正在处理」；随后收到「审批 … 已处理」 |
| 4 | 再触发一次飞书审批，回复文本 `n <ID>` | 文本裁决仍然生效 |
| 5 | `ferry audit export --since 7d > audit.csv`，用表格软件打开，再搜你发过的一句话 | 六列齐全；搜不到消息正文 |
| 6 | macOS：`ferry service install` → 注销再登录 → `ferry status`、`ferry service status` → `ferry service uninstall` | 登录后守护进程在线；状态为运行中；卸载干净 |
| 7 | Linux：`ferry service install` → `loginctl enable-linger $USER` → 重启、不登录，从另一台机器发微信 | 守护进程在线。若钥匙串未解锁导致起不来，记录现象（3.12） |
| 8 | Windows：管理员终端 `ferry service install`（输入密码）→ 重启 → `ferry status` | 守护进程在线；若提示缺「作为服务登录」权限，按提示授予后重试成功 |
| 9 | Windows：`ferry run -- <某个没有 ACP 的 CLI>`，在微信发一句话 | 收到该 CLI 的输出；终端里能正常继续手动操作 |
| 10 | Windows：终端里在 `<ws>/demo` 开着 qwen 或 kimi 不退出，微信 `/use` 它 | 收到占用提示、没有接管；退出后再 `/use` 成功 |
| 11 | TΩ 的提交 push 之后看 CI | 7 个 job 全绿；Windows 那一步 4 个 PASS |

全部通过后：`git push origin main`，确认 CI 全绿；`git tag p4-b1-done && git push origin p4-b1-done`。本批次不打版本号标签；P4 全部结束时打 `v1.0.0`（00 总览第 2 节）。
