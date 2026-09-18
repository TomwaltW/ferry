# 接力

[English](../en/handoff.md) · **中文**

把同一个会话在终端和聊天窗口之间搬来搬去，同时保证不会两边一起驱动。
目前只是提纲 —— 内容随 P2 补充。

## 为什么要接力

聊天和终端各起一个会话会丢掉什么，共用一个会话又能换来什么。P2 补充。

## 三档接力

聊天和电脑能多紧密地共用一个会话，取决于 agent 和你怎么跑它：

| 档位 | 方式 | 能否两边同时在线 |
| --- | --- | --- |
| 一档：接管 | 聊天里 `/ls` 列出 agent 最近的终端会话，`/use` 经 ACP 恢复，`ferry back` 交还给终端。 | 否，同一时刻只有一边驱动 |
| 二档：共用服务 | 有服务模式的 agent（`opencode serve`、`qwen serve`）：终端和聊天连着同一个正在跑的会话。 | 是 |
| 三档：代理直连 | 在 Zed 等 ACP 编辑器里通过 `ferry proxy <agent>` 跑 agent：编辑器驱动，聊天旁观，加 `--allow-inject` 后也能往里发消息。见 [proxy.md](proxy.md)。 | 是 |

## Claude Code 插件

Claude Code 可以通过可选的 `ferry channel` 插件从聊天里够到：它把消息和工具审批
中继进一个正在跑的 Claude Code 会话。**需要 Anthropic 官方身份验证**（claude.ai 账号
或 Console API key），在 Bedrock、Vertex AI、Foundry 上不可用。配置方法与限制见
[claude-code.md](claude-code.md)。

## 从聊天接管

认领一个在终端里起的会话、聊天在驱动时终端显示什么、一轮进行到一半产生的输出
怎么处理。P2 补充。

## 交还回去

释放会话，以及终端怎么把记录接着往下读。P2 补充。

## 互斥与锁

为什么同一时刻只允许一个驱动方持有会话、被持有的会话会回复什么、
残留的锁怎么清掉。P2 补充。

## 限制

哪些 agent 支持接力，不支持时会降级成什么样。P2、P3 补充。
