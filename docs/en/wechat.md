# WeChat entry

**English** · [中文版](../zh-CN/wechat.md)

How Ferry talks to WeChat through the iLink bot gateway, and what the chat side
of a session actually looks like.

## Logging in

`ferry login wechat` fetches a QR code and prints it in the terminal. The code
refreshes itself up to three times while you take your phone out. Scan it with
the account that should be the bot, not with the account you will chat from.

What a confirmed login returns is a bot token, a bot ID and the ID of whoever
scanned. That last one becomes the owner. If the platform later invalidates the
session, the daemon marks WeChat offline, keeps running, and retries once a
minute; `ferry login wechat` puts it back.

## Commands

Only a whole message counts as a command, so a sentence that merely mentions
`/stop` is still an ordinary prompt. A message starting with `//` is stripped of
one slash and treated as text.

| Command | Alias | What it does |
| --- | --- | --- |
| `/ls` | 会话 | List this chat's sessions; `▶` marks the current one |
| `/new <agent> [dir]` | 新建 | Start a session; the directory defaults to your first workspace root |
| `/use <n>` | | Switch to session n, resuming it if it was parked |
| `/agents` | | List configured agents and whether they can run |
| `/stop` | 停 | Cancel the current turn |
| `/close [n]` | 关闭 | Close a session |
| `/mode <key> <value>` | | Set a session option, such as the agent's mode |
| `/lang <zh or en>` | | Switch the bot's language for this chat |
| `/pair <code>` | | Confirm a pairing code (approvers only) |
| `/help` | 帮助 | The command list |

`/pull` and `/file` are accepted and answer that they arrive in a later version.

## Approvals

When an agent wants to do something the policy does not decide on its own, you
get a message carrying a five-letter code, the tool name, a description and a
truncated preview of the input. Answer with the code:

```text
y abcde
n abcde
a abcde
```

`a` is only offered when the agent itself supports "always allow". An approval
nobody answers is refused when the timeout runs out, and you are told so. Only
approvers can decide: a verdict from anyone else is ignored, as is a verdict
naming an approval that has already been resolved.

The preview is passed through a masker that keeps the first four characters of
anything that looks like a key or a token. Treat it as best-effort, not as a
guarantee: it is still a preview of real input.

## Pairing

Someone who has never used the bot gets no access and no agent. They receive a
six-digit code and nothing else, whatever they sent. An approver turns that into
access:

```text
/pair 123456
```

Codes last an hour, and one chat can have at most three waiting at a time.
Both sides are told when the pairing goes through.

## The reply window

WeChat only lets the bot answer inside a window opened by an incoming message,
and only a limited number of times. Ferry therefore batches: ordinary progress
updates are merged on an interval and sent together, while approval requests and
the final result of a turn are sent immediately.

The budget is spent per incoming message. Ferry always keeps one reply in
reserve so an approval request can get through, splits long output across the
replies it has left, and marks the last piece as truncated rather than losing
the tail silently. If a turn is very chatty you will see fewer, bigger messages
rather than a stream of small ones.
