# Agent

[English](../en/agents.md) · **中文**

Ferry 能驱动哪些编码 agent、一个 agent 怎么配。目前只是提纲 —— 内容随 P1 补充，
更多驱动随 P2、P3 补充。

## 预设

内置有哪些预设、每个预设固定了什么：命令行、驱动类型、默认工作目录。
默认预设是 `qwen-code`。P1 补充。

## 驱动

`acp` 对应讲 Agent Client Protocol 的 agent，`server` 对应自带服务模式的 agent，
之后还有 `proxy`、`claude-channel` 和 `pty`。每种驱动能提供和不能提供哪些能力。
P1、P2 补充。

## 工作区根目录

agent 被允许读写哪些目录，以及伸到目录之外的工具调用会怎样。P1 补充。

## 会话与状态

启动中、空闲、运行中、等待审批、驻留和关闭 —— 每个状态下，此刻到达的一条消息
会被怎么处理。P1 补充。

## 接入新 agent

怎么让 Ferry 用上一个还没有预设的 agent。P2 补充。

## ACP 版本

每个 agent 条目用 `acp_version` 选择 Ferry 跟它说哪一版 Agent Client Protocol：

```toml
[[agents]]
name = "my-agent"
command = ["my-agent", "--acp"]
acp_version = "auto"
```

| 取值 | 行为 |
| --- | --- |
| `v1` | 默认值。Ferry 说 ACP v1，和以往版本一样。 |
| `v2` | Ferry 说 ACP v2：用 `session/resume` 代替 `session/load`，一轮以空闲的状态更新结束，支持 `session/list` 与 `session/close`。握手时 agent 回 v1 则启动失败。 |
| `auto` | 先按 v2 握手；agent 协商回 v1 时，重启它并改用 v1。 |

ACP v2 仍在演进；除非 agent 的文档写明支持 v2，否则保持 `v1`。v2 agent 需要
交互式登录时，会话不会启动，聊天里会提示你先在电脑终端里用它完成登录；
不需要交互的登录方式由 Ferry 自动完成。
