# Ferry

**English** · [中文版 README](README.zh-CN.md) · Apache-2.0 · status: P4 batch 1 — WeChat + Feishu + managed sessions

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

Ferry is under active construction, and `v0.1.0`, the first tagged release, is
not out yet.
P1 is in: scan to log in, pairing, managed sessions over ACP, approvals in chat,
and an outbox that respects WeChat's reply window. P2 added terminal handoff and
reliable out-of-window delivery. P3 added Feishu, the proxy driver and PTY. P4
batch 1 added per-user workspaces, a bot-wide owner, audit export, Feishu
approval cards and a system service. Everything user-facing ships in English and
Chinese from day one.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh | sh
ferry version
```

The script picks the release build for your platform, checks it against
`checksums.txt`, and puts `ferry` in `~/.local/bin`. Set `FERRY_INSTALL_DIR` to
install somewhere else, or `FERRY_VERSION` to pin a release. The installer
needs a published release to fetch, and none is out yet; until `v0.1.0` ships,
build [from source](#from-source).

Behind the Great Firewall the script itself has to come through a mirror as
well, not just the downloads it makes:

```sh
export FERRY_GITHUB_PROXY=https://ghfast.top/
curl -fsSL "${FERRY_GITHUB_PROXY}https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh" | sh
```

Every release also carries the archives and a `checksums.txt` you can download
and verify by hand.

### From source

With a Go toolchain:

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
default agent. `ferry login wechat` prints a QR code — scan it with the WeChat
account you will chat from, which becomes the bot's owner. Then message the bot
`/new kimi ~/code/demo`, send it a task, and answer any approval with
`y <code>`. The full walkthrough is in
[docs/en/quickstart.md](docs/en/quickstart.md), and the chat side is documented
in [docs/en/wechat.md](docs/en/wechat.md).

Prefer Feishu? `ferry login feishu` adds it as a second entry point next to
WeChat, served by the same daemon — see [docs/en/feishu.md](docs/en/feishu.md).
Working in an editor? Set Zed's (or any ACP editor's) agent command to
`ferry proxy <agent>` and the editor session is mirrored to your chat — see
[docs/en/proxy.md](docs/en/proxy.md).

To keep the daemon running without a terminal, `ferry service install` hands it
to launchd, systemd or the Windows service manager, running as you — see
[docs/en/service.md](docs/en/service.md). `ferry audit export --since 7d`
writes the audit log as CSV, without any message bodies.

## Documentation

- [Quickstart](docs/en/quickstart.md) — install, log in, first session
- [WeChat entry](docs/en/wechat.md) — pairing, gating, reply windows
- [Feishu entry](docs/en/feishu.md) — the second chat platform
- [Editor proxy](docs/en/proxy.md) — `ferry proxy` for Zed and other ACP editors
- [Claude Code](docs/en/claude-code.md) — the optional channel plugin and compatibility mode
- [Wrapping a terminal program](docs/en/pty.md) — `ferry run`, experimental
- [Agents](docs/en/agents.md) — presets, drivers, workspace roots
- [Handoff](docs/en/handoff.md) — moving a session between terminal and chat
- [Multiple users](docs/en/multi-user.md) — per-user workspaces and approvers
- [Running as a service](docs/en/service.md) — `ferry service` on macOS, Linux and Windows
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
