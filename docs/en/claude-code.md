# Claude Code

**English** · [中文版](../zh-CN/claude-code.md)

Two optional ways to connect Ferry with Claude Code's channels: `ferry channel`
makes a running Claude Code session reachable from your chat, and compatibility
mode turns an existing third-party channel server into one more Ferry entry
point.

## Requirements

Claude Code channels need **Anthropic's own authentication** — a claude.ai
account or a Console API key. They are not available when Claude Code runs on
Amazon Bedrock, Google Vertex AI or Microsoft Foundry, and Team and Enterprise
organizations have to enable them first. While channels are a research preview,
a custom channel such as Ferry's is loaded with the development flag shown
below. Everything else in Ferry works without an Anthropic account.

## ferry channel

`ferry channel` is a stdio MCP server that Claude Code starts. It attaches to
the daemon, so a message you send from WeChat or Feishu arrives in the Claude
Code session, Claude answers through its `reply` tool, and the answer comes back
to the same chat.

1. Run `ferry daemon`.
2. Declare the server in the project's `.mcp.json`:

   ```json
   {"mcpServers": {"ferry": {"command": "ferry", "args": ["channel"]}}}
   ```

3. Start Claude Code with the development flag, naming the server:

   ```sh
   claude --dangerously-load-development-channels server:ferry
   ```

   Accept the development-channel warning and the new MCP server.

Chat is told "Claude Code is connected" with the session number. The same files
ship as a plugin in the repository's `plugin/` directory.

Without a running daemon `ferry channel` still completes the handshake, but
relays nothing and tells the model so.

## Approvals

When Claude Code asks to use a tool, the request goes both to the local dialog
and to your chat, under the five-letter ID Claude Code chose; reply `y <ID>` or
`n <ID>`. Whichever answer arrives first wins. If you answer in the terminal,
the request in chat stays open until it times out, and the verdict sent then is
dropped by Claude Code — a limit of the protocol, not something Ferry can fix.
Only tool-use approvals are relayed.

## Compatibility mode

Any server written for Claude Code channels can serve as a Ferry entry point.
Ferry starts it, reads the channel events it pushes, and answers through its
reply tool:

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

The entry point is called `channel-<name>`. `owner` is the sender ID treated as
the owner; the three other keys say which meta keys carry the sender and the
chat, and which tool sends a reply. A server that exits is restarted once a
minute, five times at most, and then reported offline.

`owner` has no default. A channel server with no `owner` has no owner: nobody
on it is the owner, so nobody on it can pair either. A message that names no
sender is booked to `local`, and is the owner's only on a server that sets
`owner = "local"`. Do that only if the server reports no sender IDs and you are
the only one who can write to it (see [security.md](security.md)).

### Upgrading from v0.1.0

In v0.1.0, a channel server with no `owner` got `local` filled in, so every
message that named no sender came from the owner. Now a server with no `owner`
has no owner. If you run a server alone, it reports no sender IDs, and you
relied on the old behaviour, add this line to its `[[channel_servers]]` table:

```toml
owner = "local"
```

## Limits

- Compatibility mode forwards no approvals to the third-party server.
- The owner is recognised in every chat of the server, not only in direct
  chats (see [multi-user.md](multi-user.md)). Everyone else still pairs per
  chat and sender, so a pairing in one chat does not carry over to another.
- Meta keys may contain only letters, digits and underscores; Claude Code
  silently drops any other key.
