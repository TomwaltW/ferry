# Ferry

**English** · [中文版 README](README.zh-CN.md) · Apache-2.0 · status: P1 WeChat + managed sessions

Carry your coding agents to chat. Ferry is a self-hosted daemon that puts one
chat bot in front of every coding agent you already run — so you can start a
task from your phone, answer a permission prompt while away from the keyboard,
and pick the session back up in your terminal.

## What it does

- **One bot, every agent.** Claude Code, Qwen Code, Kimi CLI, OpenCode and any
  other ACP-speaking agent sit behind a single chat contact.
- **Managed sessions.** Ferry starts the agent, keeps its working directory and
  session ID, and streams chunks, plans and tool calls back to chat.
- **Approvals in chat.** A tool that wants to edit or run something asks you in
  the chat window, with a short approval code instead of a wall of JSON.
- **Handoff.** A session started in the terminal can be taken over from chat and
  handed back, with only one side driving at a time.
- **Reliable delivery.** An outbox retries, deduplicates, and holds messages
  until the platform reply window reopens, instead of dropping them.

## Status

Ferry is under active construction. P1 is in: scan to log in, pairing, managed
sessions over ACP, approvals in chat, and an outbox that respects WeChat's reply
window. Terminal handoff and reliable out-of-window delivery are P2; Feishu, the
proxy driver and PTY are P3. Everything user-facing ships in English and Chinese
from day one.

## Install

```sh
go install github.com/TomwaltW/ferry/cmd/ferry@latest
```

Or build from a clone:

```sh
git clone https://github.com/TomwaltW/ferry.git
cd ferry
go build ./cmd/ferry
./ferry version
```

## Quick start

```sh
ferry init
ferry login wechat
ferry daemon
```

`ferry init` asks for your language, the directories agents may work in, and a
default agent. `ferry login wechat` prints a QR code — scan it with the account
that should *be* the bot, from a second account you will chat from. Then message
that bot `/new kimi ~/code/demo`, send it a task, and answer any approval with
`y <code>`. The full walkthrough is in
[docs/en/quickstart.md](docs/en/quickstart.md), and the chat side is documented
in [docs/en/wechat.md](docs/en/wechat.md).

## Documentation

- [Quickstart](docs/en/quickstart.md) — install, log in, first session
- [WeChat entry](docs/en/wechat.md) — pairing, gating, reply windows
- [Agents](docs/en/agents.md) — presets, drivers, workspace roots
- [Handoff](docs/en/handoff.md) — moving a session between terminal and chat
- [Security](docs/en/security.md) — secrets, approvals, path limits
- [FAQ](docs/en/faq.md)
- [Glossary](docs/glossary.md) — the bilingual term list
- [FSP protocol types](spec/fsp.md) — the frozen core types

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) first. The short version: identifiers,
comments and commit messages in English; every user-facing document in both
English and Chinese; `go run ./scripts/check-i18n` must stay green.

## License

Apache-2.0. See [LICENSE](LICENSE).
