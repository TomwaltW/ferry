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
