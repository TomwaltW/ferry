# Claude Code

[English](../en/claude-code.md) · **中文**

Ferry 与 Claude Code 的 channels 有两种可选的接法：`ferry channel` 让一个正在跑的
Claude Code 会话能从聊天里够到；兼容模式把现成的第三方 channel server 当作 Ferry
的又一个入口。

## 前提

Claude Code channels 需要 **Anthropic 官方身份验证** —— claude.ai 账号或
Console API key。Claude Code 跑在 Amazon Bedrock、Google Vertex AI、
Microsoft Foundry 上时不可用；Team 与 Enterprise 组织需要管理员先开启。
channels 处于研究预览期间，Ferry 这类自定义 channel 要用下面的开发参数加载。
Ferry 的其他功能都不需要 Anthropic 账号。

## ferry channel

`ferry channel` 是一个由 Claude Code 启动的 stdio MCP server。它接入守护进程，
于是你在微信或飞书里发的消息会进入 Claude Code 会话，Claude 用它的 `reply` 工具
回答，回答回到同一个聊天。

1. 运行 `ferry daemon`。
2. 在项目的 `.mcp.json` 里声明这个 server：

   ```json
   {"mcpServers": {"ferry": {"command": "ferry", "args": ["channel"]}}}
   ```

3. 用开发参数启动 Claude Code，点名这个 server：

   ```sh
   claude --dangerously-load-development-channels server:ferry
   ```

   同意开发 channel 的警告和新的 MCP server。

聊天里会收到「Claude Code 已接入」和会话编号。同样的文件以插件形式放在仓库的
`plugin/` 目录。

守护进程没运行时 `ferry channel` 照样完成握手，但什么都不转发，并告诉模型当前不可用。

## 审批

Claude Code 要用工具时，请求会同时发到本机对话框和你的聊天，编号就是 Claude Code
自己给的 5 个字母；回复 `y <编号>` 或 `n <编号>`。先到的答复算数。如果你在终端里
答了，聊天里那条会一直等到超时，那时发出的裁决会被 Claude Code 丢弃 —— 这是协议
本身的限制，Ferry 改不了。只中继工具调用的审批。

## 兼容模式

任何为 Claude Code channels 写的 server 都可以当作 Ferry 的入口。Ferry 启动它，
读取它推来的 channel 事件，再通过它的回复工具作答：

```toml
[[channel_servers]]
name = "tg"
command = ["node", "/opt/telegram-channel/index.js"]
env = { BOT_TOKEN = "..." }
owner = "123456789"
user_id_meta = "user_id"
chat_id_meta = "chat_id"
reply_tool = "reply"
```

入口名为 `channel-<name>`。`owner` 是被视为 owner 的发送者 ID；
另外三项分别指定从哪个 meta 键取发送者、从哪个取对话，以及用哪个工具回复。
server 退出后每分钟重启一次，最多 5 次，之后标为离线。

`owner` 没有默认值。没写 `owner` 的 channel server 没有 owner：上面谁都不是 owner，
也就谁都配对不了。不带发送者 ID 的消息记在 `local` 名下，只有在写了
`owner = "local"` 的 server 上它才是 owner。只有 server 不报发送者 ID、且只有你
一个人能往里写时才这样做（见 [security.md](security.md)）。

### 从 v0.1.0 升级

v0.1.0 在没写 `owner` 时会把它填成 `local`，于是所有不带发送者 ID 的消息都算
owner 发的。现在没写 `owner` 的 server 就没有 owner。如果你是一个人在用、server
不报发送者 ID、又依赖旧行为，在它的 `[[channel_servers]]` 里加一行：

```toml
owner = "local"
```

## 限制

- 兼容模式不向第三方 server 转发审批请求。
- 只有在单聊里（server 给的 `chat_id` 就是 owner 自己的 ID）才认得出 owner；
  在其他对话里，owner 会被当作新的发送者。
- meta 键只能含字母、数字和下划线，其他键会被 Claude Code 静默丢弃。
