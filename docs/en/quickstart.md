# Quickstart

**English** · [中文版](../zh-CN/quickstart.md)

From a fresh install to a coding agent answering you in chat. Everything below
works against your own machine: Ferry is a daemon you run, not a service you
sign up for.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh | sh
ferry version
```

The script picks the release build for your platform, checks it against
`checksums.txt`, and puts `ferry` in `~/.local/bin` — no Go toolchain needed.
`FERRY_INSTALL_DIR` sends it somewhere else, `FERRY_VERSION` pins a release.

Behind the Great Firewall the script itself has to come through a mirror as
well, not just the downloads it makes:

```bash
export FERRY_GITHUB_PROXY=https://ghfast.top/
curl -fsSL "${FERRY_GITHUB_PROXY}https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh" | sh
```

With a Go toolchain you can build it yourself instead:

```bash
go install github.com/TomwaltW/ferry/cmd/ferry@latest
```

Ferry keeps its configuration, database, credentials and logs under
`FERRY_HOME`, which defaults to `~/.ferry`. Point that variable somewhere else
if you want to keep several setups apart — with one exception: credentials go
into the system keyring when there is one, and the keyring entry is shared by
every `FERRY_HOME` on the machine. A second setup therefore starts out already
logged in, and `ferry logout` in either one signs both out.

## Configure

`ferry init` asks three things: the bot language, the directories agents are
allowed to work in, and which agent to use by default. Answer them once and it
writes `config.toml`. Pass the answers as flags to skip the questions;
`--lang` takes `en` or `zh-CN`.

```bash
ferry init
ferry init --yes --root ~/code --agent kimi
ferry doctor
```

`ferry doctor` prints one line per check: configuration, workspace roots,
credentials, database, every configured agent, and whether `npx` and `uvx` are
around for the presets that need them. It always exits 0 — it is a report, not
a gate.

## Log in

```bash
ferry login wechat
```

A QR code appears in the terminal. Scan it with the WeChat account that should
*be* the bot — most people use a second account for this, and talk to it from
their main one. The account that scans becomes the owner: the only one allowed
to use the bot until it pairs someone else, and the one asked to approve tool
calls.

Credentials go into the system keyring when there is one, and into
`$FERRY_HOME/credentials/` with mode 0600 when there is not. `ferry logout
wechat` forgets them.

## Run the daemon

```bash
ferry daemon
ferry status
```

The daemon holds a single-instance lock, writes its log to
`$FERRY_HOME/logs/ferry.log` (rotated at 10 MB, three kept), and refreshes
`status.json` every five seconds so `ferry status` can tell you whether it is
alive and whether WeChat is online.

## Your first session

Message the bot from your own account:

```text
/new kimi ~/code/demo
/ls
/help
```

`/new` starts an agent in that directory — it must be inside one of your
workspace roots — and replies with the session number. After that, anything you
send that is not a command becomes a prompt for that session. You get a receipt
immediately, progress as it streams, and the result when the turn ends.

If the agent wants to run or edit something, you get an approval request with a
five-letter code:

```text
y abcde
n abcde
a abcde
```

`y` allows once, `n` refuses, `a` allows that kind of call from now on.
Unanswered requests are refused when the timeout expires.

## When something looks wrong

```bash
ferry doctor
ferry status
tail -f ~/.ferry/logs/ferry.log
```

If `ferry status` says WeChat is offline, the platform session has expired: run
`ferry login wechat` again. The daemon stays up while that is true and retries
on its own, so you do not need to restart it.
