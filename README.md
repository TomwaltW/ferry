# Ferry

**English** · [中文版 README](README.zh-CN.md) · Apache-2.0 · status: P0 skeleton

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

Ferry is under active construction. P0 freezes the protocol types, the test
doubles and the bilingual scaffolding; there is no working chat entry point yet.
The roadmap is P1 WeChat plus managed sessions, P2 terminal handoff plus
reliable delivery, P3 Feishu, proxy and PTY. Everything user-facing ships in
English and Chinese from day one.

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

Not available yet — the chat entry point lands in P1. Until then the only
command is `ferry version`. The walkthrough is being written in
[docs/en/quickstart.md](docs/en/quickstart.md).

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
