# Ferry

[English README](README.md) · **中文** · Apache-2.0 · 状态：P4 批次一 — 微信 + 飞书 + 托管会话

把你的编码 agent 带到聊天窗口。Ferry 是一个自托管的守护进程，让你已经在用的
每个编码 agent 共用同一个聊天机器人 —— 于是你可以在手机上发起任务、离开键盘时
批掉一个权限请求，回到终端再把会话接着干完。

## 能做什么

- **一个 bot，管所有 agent。** Claude Code、Qwen Code、Kimi CLI、OpenCode，以及
  任何讲 ACP 的 agent，都藏在同一个聊天联系人后面。
- **托管会话。** Ferry 负责拉起 agent、记住它的工作目录和会话 ID，把输出分片、
  计划和工具调用流式送回聊天窗口。
- **在聊天里审批。** 工具要改文件或执行命令时会来问你，给的是一个短审批码，
  而不是一大段 JSON。
- **接力。** 终端里起的会话可以从聊天接管，也能交还回去，同一时刻只有一侧在驱动。
- **可靠投递。** 发件箱负责重试、去重，并在平台回复窗口关闭时把消息暂存住，
  而不是直接丢掉。

## 当前状态

Ferry 还在施工中，第一个 release `v0.1.0` 还没发布。P1 已经进来了：扫码登录、配对、
经 ACP 的托管会话、在聊天里审批，以及一个照顾微信回复窗口的发件箱。P2 补上了终端接力
和窗口外的可靠补投。P3 补上了飞书、proxy 驱动和 PTY。P4 批次一补上了按用户隔离的工作区、
机器人级 owner、审计导出、飞书审批卡片和系统服务。所有面向用户的东西从第一天起就同时
提供英文和中文。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh | sh
ferry version
```

脚本会挑出对应平台的发布版二进制，用 `checksums.txt` 校验 sha256，装进
`~/.local/bin`。想装到别处设 `FERRY_INSTALL_DIR`，想钉住某个版本设
`FERRY_VERSION`。安装脚本要有一个已发布的 release 才能用，目前还没有；`v0.1.0` 发布之前请[从源码装](#从源码装)。

国内网络下，**脚本本身也要走镜像**，不能只镜像它下载的文件：

```sh
export FERRY_GITHUB_PROXY=https://ghfast.top/
curl -fsSL "${FERRY_GITHUB_PROXY}https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh" | sh
```

每个 release 也附带压缩包和 `checksums.txt`，可以手动下载并自行校验。

### 从源码装

有 Go 工具链时：

```sh
go install github.com/TomwaltW/ferry/cmd/ferry@latest
```

或者从源码构建：

```sh
git clone https://github.com/TomwaltW/ferry.git
cd ferry
go build ./cmd/ferry
./ferry version
```

## 快速上手

```sh
ferry init
ferry login wechat
ferry daemon
```

`ferry init` 问你语言、允许 agent 工作的目录、默认 agent。
`ferry login wechat` 会画出二维码 —— 用**你平时聊天的那个号**扫，
这个号就是 bot 的 owner。然后给 bot 发 `/new kimi ~/code/demo`，
派个活，审批回 `同意 <编码>`；只有一个审批在等时回「同意」就行。完整流程见
[docs/zh-CN/quickstart.md](docs/zh-CN/quickstart.md)，
聊天这一侧见 [docs/zh-CN/wechat.md](docs/zh-CN/wechat.md)。

用飞书？`ferry login feishu` 会把它加成微信之外的第二个入口，由同一个守护进程服务，
见 [docs/zh-CN/feishu.md](docs/zh-CN/feishu.md)。在编辑器里干活？把 Zed（或任何
ACP 编辑器）的 agent 命令设成 `ferry proxy <agent>`，编辑器里的会话就会镜像到聊天，
见 [docs/zh-CN/proxy.md](docs/zh-CN/proxy.md)。

想让守护进程离开终端也一直在线？`ferry service install` 把它交给 launchd、systemd
或 Windows 服务管理器，以你本人的身份运行，见 [docs/zh-CN/service.md](docs/zh-CN/service.md)。
`ferry audit export --since 7d` 把审计日志导出为 CSV，不含任何消息正文。

## 文档

- [快速上手](docs/zh-CN/quickstart.md) —— 安装、登录、第一个会话
- [微信入口](docs/zh-CN/wechat.md) —— 配对、陌生人门控、回复窗口
- [飞书入口](docs/zh-CN/feishu.md) —— 第二个聊天平台
- [编辑器代理](docs/zh-CN/proxy.md) —— 给 Zed 等 ACP 编辑器用的 `ferry proxy`
- [Claude Code](docs/zh-CN/claude-code.md) —— 可选的 channel 插件与兼容模式
- [包装终端程序](docs/zh-CN/pty.md) —— `ferry run`，实验功能
- [Agent](docs/zh-CN/agents.md) —— 预设、驱动、工作区根目录
- [接力](docs/zh-CN/handoff.md) —— 会话在终端与聊天之间转移
- [多用户](docs/zh-CN/multi-user.md) —— 按用户的工作区与审批人
- [作为系统服务运行](docs/zh-CN/service.md) —— macOS、Linux、Windows 上的 `ferry service`
- [安全](docs/zh-CN/security.md) —— 凭据、审批、路径限制
- [常见问题](docs/zh-CN/faq.md)
- [术语表](docs/glossary.md) —— 中英术语对照
- [FSP 协议类型](spec/fsp.zh-CN.md) —— 已冻结的核心类型

## 参与贡献

先读 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)。一句话版本：标识符、注释和
提交信息用英文；每份面向用户的文档都要有中英两版；`go run ./scripts/check-i18n`
必须保持绿色。

## 许可证

Apache-2.0，见 [LICENSE](LICENSE)。
