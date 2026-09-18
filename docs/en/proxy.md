# Editor proxy

**English** · [中文版](../zh-CN/proxy.md)

`ferry proxy <agent>` sits between an ACP editor — Zed, JetBrains IDEs, VS Code
extensions, Toad — and the agent it runs. The editor keeps working exactly as
before; the same session is mirrored to your chat, so you can watch it from
your phone and, if you allow it, type into it or answer its approvals there.

## Setting it up

Point the editor's external agent command at Ferry instead of the agent. In Zed,
for example:

```json
{
  "agent_servers": {
    "Qwen via Ferry": {
      "command": "ferry",
      "args": ["proxy", "qwen-code"]
    }
  }
}
```

The exact settings format belongs to the editor — check its documentation on
external agents. The agent name is a preset or an `[[agents]]` entry of your
configuration; `/agents` in chat lists them. The proxy reads
the same configuration as the daemon (`FERRY_HOME`, `~/.ferry` by default).
Start `ferry daemon` first; without it the proxy forwards everything untouched,
prints a notice on stderr, and nothing reaches chat.

## What chat sees

Every editor session is announced in chat with its agent and directory, and
gets a session number like any other. A turn the editor starts is marked as
such, and the agent's output follows. The session reports to the platform
named by `external.bind_platform` (WeChat unless you change it).

## Flags

| Flag | Effect |
| --- | --- |
| `--allow-inject` | Messages from chat are sent to the agent while the editor is idle. The editor shows them as your own message, followed by the answer. Without this flag chat can only watch. |
| `--remote-approve` | Permission requests go to chat instead of the editor, and are answered with `y <code>` / `n <code>`. |
| `--title <name>` | The name the session goes by in chat. |

```json
{"command": "ferry", "args": ["proxy", "--allow-inject", "--remote-approve", "qwen-code"]}
```

## Approvals

By default a permission request goes to the editor only. Chat is told that
something is waiting in the editor, and nothing more; the answer is given where
you are sitting. With `--remote-approve` the request goes to chat only and the
editor never sees it.

## Limits

- A prompt from chat waits while an editor turn is running; a prompt from the
  editor waits while a chat turn is running.
- When the editor disconnects, the running turn is cancelled, the agent is shut
  down and the chat session is closed.
- Extension methods the ACP SDK does not know are answered with "method not
  found".
