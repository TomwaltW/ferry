# 开发派工 Spec — P2 端到端补写（B1、B2、B3、B5–B11、B13）

> 生成于 2026-09-20。本轮不加功能、不改行为，只补 P2 从未写过的端到端测试。

## 0. 已核实的事实（2026-09-20，本机 darwin/arm64，实跑）

| # | 项 | 结论 |
| --- | --- | --- |
| G1 | 基线 | `main` = `origin/main` = `98f39c0`；`go build ./...` 退出码 0；`go test ./... -count=1` 为 `ok` 34 / `FAIL` 0；`go test ./test/e2e/ -count=1 -v` 为 `--- PASS` **25** / `FAIL` 0；`check-i18n` 绿；`go vet` + `gofmt` 退出码 0 |
| G2 | 缺口来源 | spec-p4-b1 F6：P2 的 TΩ 从未执行，`main` 历史里没有 `p2-tomega`，p2-t1..t4 合入后直接是 p3-t0。spec-p2 2.2 的 B1–B13 端到端测试**只有两条在仓库里** |
| G3 | 已有的两条 | **B4** = `test/e2e/busy_test.go` 的 `TestBusyRefused`（p4-T1 补的，因为 T1-b 的验收要用）；**B12** = P1 的 `TestReplyBudget`（`test/e2e/e2e_test.go`，是 spec-p1 的 B8）。**B14** 是 `check-i18n` 脚本，不是测试 |
| G4 | 要补的 11 条 | B1 `TestTerminalDiscovery`、B2 `TestAdoptAndContinue`、B3 `TestBackCommand`、B5 `TestServerOpenCode`、B6 `TestServerQwen`、B7 `TestHeldAndDigest`、B8 `TestPull`、B9 `TestLongOutputFile`、B10 `TestWakeNotifier`、B11 `TestInboundImage`、B13 `TestRetryDedupe` |
| G5 | rig 的扩展方式（已成惯例，本轮照办） | `rigOption` 是 `func(*rig)`，**定义在各自的测试文件里**，不动 `harness_test.go`：`withHandoff()` 在 `busy_test.go:111`、`withFeishu()` 在 `p3_test.go:51`、`withUsersRoot()` 在 `p4_test.go:26`。`r.cfg` 在选项里可写，所以任何配置项都能就地设 |
| G6 | 唯一需要动 `harness_test.go` 的地方 | `harness_test.go:260` 的驱动表**写死只有 ACP**：`drivers := map[fsp.DriverKind]ports.Driver{fsp.DriverACP: r.driver}`。B5/B6 要的 `fsp.DriverServer` 不在里面（`cmd/ferry/assemble.go:155` 生产侧是有的）。**这一行归 T2 独占**，别的轨不许碰 |
| G7 | 假网关够用 | `internal/testkit/fakeilink`：`PushImage`（密文入站图片，`cdn.go:70`）、`SentFiles`（出站文件，`cdn.go:104`）、`FailNext(path, mode)`（注入超时/失败，`fakeilink.go:262`）、`SentRecord.Duplicate`（同一 `client_id` 二次到达，`fakeilink.go:41`）、`SetReplyLimit`、`ExpireSession`。B9、B11、B13 不需要新夹具 |
| G8 | 服务驱动的假服务**不可复用** | `newFakeOpenCode` 在 `internal/driver/server/opencode_test.go:63`、`newFakeQwen` 在 `qwen_test.go:68`，都是 `_test.go` 内部的 `httptest`，**包外 import 不到**。B5/B6 要新建一份能被 e2e 用的，落在 `internal/testkit/fakeserver/**`（归 T2） |
| G9 | 终端会话怎么来 | `handoff.Discover`（`discover.go:19`）是**起一次 agent 问它有哪些会话**，再按工作区与 `lookback` 过滤。所以 B1/B2 的「预置一个会话」= 在 rig 启动前把会话写进 `FAKEAGENT_HOME`（rig 给 agent 的是 `<home>/agent`，见 `harness_test.go:157`），不是起进程 |
| G10 | B3 要调的函数 | `cmd/ferry/back.go:76` 的 `backTarget(ctx, home, slot)`，以及 `internal/handoff/handoff.go:143` 的 `BackCommand` |
| G11 | 配置旋钮都在 | `OutboxConfig`：`WindowTTL`、`DigestMax`、`LongOutputThreshold`、`FileKeep`、`LongOutput`、`SendTimeout`（B7/B8/B9）。`NtfyConfig.URL` 可指向 httptest（B10）。`/pull`、`/file` 在 `internal/core/commands.go:42-44` |

## 1. 问题定义

P2 交付的是终端接力、OpenCode / Qwen 服务驱动、窗口外补投、唤醒、媒体。这些功能已经上线两轮，
**端到端一条没有**。单元层有覆盖，但「假网关 → 适配器 → 核心 → 驱动 → 回到假网关」这条真实路径没人钉。
F6 还指出一个更糟的形态：按 spec-p2 的字面跑 `go test ./test/e2e/ -run TestBusyRefused`，
在测试不存在时是 `ok … [no tests to run]`，**恒真**。本轮把这 11 条补齐，并且用「按名字数 PASS」的判据取代退出码。

### 1.1 明确不做

- 不改任何 `internal/` 的行为代码。**测试写不过去，是报告，不是改实现。**
- 不补 P2 的文档、不补 P3/P4 的遗漏、不动 `docs/dev/**` 以外的既有文档
- 不加依赖，不改 `go.mod` / `go.sum`
- 不打标签；`p2-backfill-base` 要不要打由本人决定（P4 的 `v0.x` 决定同理，见 spec-p4-b1 §0 准备 1）

### 1.2 spec-p2 与现状冲突怎么办 —— **停下报告，不许自行适配**

这 11 条测的是**两轮之前**定下的行为，P3/P4 改过其中一部分：按入口的审批人（spec-p4-b1 3.3）、
owner 在该 bot 的任何 chat 里都算 owner（3.3 Q1）、按用户的工作区（3.4）。
最可能对不上的是 **B5/B6 的审批那一步**与 **B10 的提醒正文**。

- spec-p2 的断言与当前实现冲突 → **停下，写进回执，不要改实现，也不要把断言改软**
- TΩ 把各轨报上来的冲突汇总成一份清单交给本人 —— 这是 spec 决定，不是写测试的人能定的

## 2. 验收标准（DoD）

### 2.1 能跑

- **A1** `go build ./... && go test ./... -count=1` → `FAIL` 0，`ok` ≥ 34
- **A2** `go vet ./... && test -z "$(gofmt -l .)"` → 退出码 0
- **A3** `go run ./scripts/check-i18n` → `i18n ok`

### 2.2 行为正确

11 条按 spec-p2 2.2 的原文实现，测试函数名**逐字**照抄（TΩ 的清点脚本按名字数）：

| B | 测试名 | 归属 |
| --- | --- | --- |
| B1 | `TestTerminalDiscovery` | T1 |
| B2 | `TestAdoptAndContinue` | T1 |
| B3 | `TestBackCommand` | T1 |
| B5 | `TestServerOpenCode` | T2 |
| B6 | `TestServerQwen` | T2 |
| B7 | `TestHeldAndDigest` | T3 |
| B8 | `TestPull` | T3 |
| B9 | `TestLongOutputFile` | T3 |
| B13 | `TestRetryDedupe` | T3 |
| B10 | `TestWakeNotifier` | T4 |
| B11 | `TestInboundImage` | T4 |

B4（`TestBusyRefused`）、B12（`TestReplyBudget`）已经在仓库里，**不要重写**。

### 2.3 没弄坏

- **C1** `go test ./test/e2e/ -count=1 -v | grep -c '^--- PASS'` → **36**（基线 25 + 本轮 11）、`FAIL` 0
- **C2** 既有 25 条一条都不许变红、不许改断言
- **C3** push 之后 CI 的 7 个 job 全绿（由总管核对）

## 3. 冻结契约

### 3.1 每轨只写自己的文件

新的 `rigOption` **定义在本轨自己的测试文件里**（G5 的三个先例）。`harness_test.go` 只有 T2 能改，
而且只能加驱动表那一处（G6）。需要别的 harness 钩子 = 停下报告。

### 3.2 文件归属表

| 路径 | owner | 其他轨 |
| --- | --- | --- |
| `test/e2e/p2_handoff_test.go`（新） | T1 | 不可改 |
| `test/e2e/p2_server_test.go`（新）、`internal/testkit/fakeserver/**`（新）、`test/e2e/harness_test.go` | T2 | 不可改 |
| `test/e2e/p2_outbox_test.go`（新） | T3 | 不可改 |
| `test/e2e/p2_media_test.go`（新） | T4 | 不可改 |
| 其余全部（`internal/**`、`cmd/**`、既有的 `test/e2e/*.go`、`locales/**`、`go.mod`、`go.sum`、文档） | 无 | 只读 |
| `docs/dev/**`、`LICENSE`、`review/**`、`.claude/**` | 人工 | 只读 |

`internal/testkit/fakeilink`、`fakeagent` 是**只读**的：不够用就停下报告，不要就地改假网关来迁就测试。

### 3.3 命名与形态

- 一个 B 一个顶层 `Test...` 函数，名字照 2.2 的表；子测试随意
- 测试必须在**三平台**都能跑；只能在 Linux/macOS 跑的，用 `runtime.GOOS` 跳过并在回执里写明哪条、为什么
- 不准用 `t.Skip` 把没写完的测试糊过去 —— TΩ 数的是 `--- PASS`，`SKIP` 不算

## 4. 基线

- 基线是**落地本 spec 的那个提交**（只改 `docs/dev/`，代码与 `98f39c0` 逐字节相同，所以 G1 的实测值照样有效）。
  四轨 worktree 钉它的**具体 sha**，分支 `p2bf-t1` … `p2bf-t4`；sha 写在各轨派单的开场自检里
- 开场自检期望值（G1 实测）：`ok` 34 / `FAIL` 0；e2e `--- PASS` 25 / `FAIL` 0；`i18n ok`；`vet`+`gofmt` 退出码 0

## 5. 任务表

| 任务 | 目标 | 条数 | 时序 |
| --- | --- | --- | --- |
| T1 | 终端接力：发现、接管续跑、`ferry back` | 3 | 并行 |
| T2 | 服务驱动：假 OpenCode / 假 Qwen（含新夹具包与驱动表那一行） | 2 | 并行 |
| T3 | 发件箱：窗口外补投与摘要、`/pull`、长输出转文件、重试去重 | 4 | 并行 |
| T4 | 唤醒与媒体：ntfy 提醒、入站图片 | 2 | 并行 |
| TΩ | 合流、清点 36 条、汇总 spec-p2 冲突清单 | — | 最后 |

### 5.1 T1 细则
按 spec-p2 B1、B2、B3。会话靠写 `FAKEAGENT_HOME`（G9），不是起进程；`withHandoff()` 已有，直接用。
B2 要钉住「恢复过程重放的历史**没有**发到微信」；B3 调 `backTarget`（G10），`--exec` 那一支也要覆盖。

### 5.2 T2 细则
先在 `internal/testkit/fakeserver/` 建可被 e2e import 的假 OpenCode 与假 Qwen（照 G8 两个 `_test.go` 里的形状搬，
但要导出）。再在 `harness_test.go:260` 的驱动表里加 `fsp.DriverServer`（照 `cmd/ferry/assemble.go:155` 的写法）。
B5/B6 的审批那一步最可能撞上 P4 的按入口审批人 —— 撞上就按 1.2 停下报告。

### 5.3 T3 细则
按 B7、B8、B9、B13。旋钮见 G11：`window_ttl`、`digest_max`、`long_output_threshold`、`file_keep`。
B13 用 `FailNext` 注入一次超时，再看 `SentRecord.Duplicate`（G7）——判据是「假网关只有 1 条非重复记录」。

### 5.4 T4 细则
按 B10、B11。B10 把 `notify.ntfy.url` 指向 `httptest`（G11），**重点在正文的负向断言**：
只有「有待处理的审批」和会话编号，不含工具名、命令、路径。B11 用 `PushImage`（G7），
查 `<home>/inbox/` 下解密后的文件与原图逐字节相同，且 agent 收到的 prompt 里有该路径。

## 6. 每个任务的验收

每轨都是这 6 条（各自的测试名代入）：

1. `go build ./...` 退出码 0
2. 本轨每条测试**按名字**出现在 `--- PASS:` 里（不准拿退出码当判据）
3. `go test ./test/e2e/ -count=1 -v` → `--- PASS` = 25 + 本轨条数，`FAIL` 0
4. `go test ./... -count=1` → `FAIL` 0
5. `go vet ./... && test -z "$(gofmt -l .)"` 退出码 0
6. `git diff --name-only 98f39c0..HEAD` 每一行都在 3.2 的白名单里

TΩ 另加：合流后 `--- PASS` = **36**、`FAIL` 0，以及 spec-p2 冲突清单。

## 7. 卡住了怎么办

- 夹具不够用（假网关、fakeagent 缺能力）→ 停，报告，**不要改 `internal/testkit`**
- spec-p2 的断言与现状冲突 → 停，报告（1.2）
- 需要动 `harness_test.go` 而你不是 T2 → 停，报告
- 任何一条要靠 `t.Skip` 才能绿 → 停，报告
