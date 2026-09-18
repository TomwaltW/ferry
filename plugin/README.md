# Ferry as a Claude Code channel

`ferry channel` is a stdio MCP server that speaks Claude Code's [channels
protocol](https://code.claude.com/docs/en/channels-reference). With it, a
message you send from WeChat or Feishu arrives in a Claude Code session running
on your machine, Claude's answer goes back to the same chat, and a tool-use
prompt can be approved from there too.

Channels require Anthropic authentication (a claude.ai account or a Console API
key). They are not available on Amazon Bedrock, Google Cloud's Agent Platform or
Microsoft Foundry, and Team and Enterprise organizations must enable them first.

## Layout

| File | What it is |
| --- | --- |
| `.claude-plugin/plugin.json` | The plugin manifest |
| `.mcp.json` | The MCP server declaration the manifest points at |

## Using it while developing

A channel that is not on Anthropic's approved list — this one — is loaded with
the development flag. Two steps:

1. Put the server in the project's `.mcp.json` (the file next to this README is
   exactly what it should contain):

   ```json
   {"mcpServers": {"ferry": {"command": "ferry", "args": ["channel"]}}}
   ```

2. Start Claude Code with the development flag, naming the server:

   ```bash
   claude --dangerously-load-development-channels server:ferry
   ```

Claude Code shows a full-screen warning listing the development channels, and
then asks for consent to use the new MCP server from `.mcp.json`. Accept both.
A dim notice under the startup banner confirms the channel registered.

Run `ferry daemon` first. Without it `ferry channel` still completes the MCP
handshake, but it declares no permission relay and its instructions tell the
model that the channel is idle.

## Known limitation: approvals race

A tool-use prompt goes to the local terminal dialog and to your chat at the same
time, and the first answer wins. If you answer in the terminal, the prompt
waiting in your chat stays there until it times out, and the verdict it then
sends is dropped by Claude Code, which only accepts an answer to a request that
is still open. This is how the protocol works, not a Ferry bug.

---

# 把 Ferry 当作 Claude Code 的 channel

`ferry channel` 是一个走 stdio 的 MCP server，实现 Claude Code 的
[channels 协议](https://code.claude.com/docs/en/channels-reference)。装上以后，
你在微信或飞书里发的消息会进入本机正在跑的 Claude Code 会话，Claude 的回复回到
同一个对话，工具调用的审批也可以在那里处理。

Channels 需要 Anthropic 官方身份验证（claude.ai 账号或 Console API key），
在 Amazon Bedrock、Google Cloud Agent Platform、Microsoft Foundry 上不可用；
Team 与 Enterprise 组织需要管理员先开启。

## 目录

| 文件 | 说明 |
| --- | --- |
| `.claude-plugin/plugin.json` | 插件清单 |
| `.mcp.json` | 清单指向的 MCP server 声明 |

## 开发阶段怎么用

不在 Anthropic 批准名单上的 channel（本插件就是）要用开发参数加载，两步：

1. 在项目的 `.mcp.json` 里加上这个 server（本目录下的 `.mcp.json` 就是它的内容）：

   ```json
   {"mcpServers": {"ferry": {"command": "ferry", "args": ["channel"]}}}
   ```

2. 用开发参数启动 Claude Code，点名这个 server：

   ```bash
   claude --dangerously-load-development-channels server:ferry
   ```

Claude Code 会先弹一个整屏警告列出要加载的开发 channel，再问是否允许使用
`.mcp.json` 里这个新的 MCP server，两个都同意。启动横幅下面会有一行浅色提示
说明 channel 已注册。

先把 `ferry daemon` 跑起来。守护进程没运行时 `ferry channel` 照样能完成 MCP
握手，但不会声明审批中继，instructions 里会直接告诉模型这个 channel 当前是空转的。

## 已知限制：审批会抢

一次工具审批会同时发到本机终端的对话框和你的聊天里，先答的算数。如果你在终端里
答了，聊天里那条会一直等到超时；超时后发出的裁决会被 Claude Code 丢掉，因为它只
认还没关闭的请求。这是协议本身的限制，不是 Ferry 的缺陷。
