# WeChat entry

**English** · [中文版](../zh-CN/wechat.md)

How Ferry talks to WeChat through the iLink bot gateway, and what the chat side
of a session actually looks like.

## Logging in

`ferry login wechat` fetches a QR code and prints it in the terminal. The code
refreshes itself up to three times while you take your phone out. Scan it with
the account you will chat from. The bot is not a WeChat account of its own, so
there is no second account to scan with.

What a confirmed login returns is a bot token, a bot ID and the ID of whoever
scanned. That last one becomes the owner, and the login prints both IDs when it
succeeds. If the platform later invalidates the session, the daemon marks
WeChat offline, keeps running, and retries once a minute; `ferry login wechat`
puts it back.

## Commands

Only a whole message counts as a command, so a sentence that merely mentions
`/stop` is still an ordinary prompt. A message starting with `//` is stripped of
one slash and treated as text.

| Command | Alias | What it does |
| --- | --- | --- |
| `/ls` | 会话 | List this chat's sessions; `▶` marks the current one |
| `/new [agent] [dir]` | 新建 | Start a session; the agent defaults to `default_agent` and the directory to your first workspace root |
| `/use <n>` | 切换, 切换到 | Switch to session n, resuming it if it was parked |
| `/agents` | | List configured agents and whether they can run |
| `/stop` | 停, 停止 | Cancel the current turn |
| `/close [n]` | 关闭 | Close a session |
| `/mode <key> <value>` | | Set a session option, such as the agent's mode |
| `/lang <zh or en>` | 语言 | Switch the bot's language for this chat |
| `/pair <code>` | 配对 | Confirm a pairing code (approvers only) |
| `/pull` | 收件 | Send the next batch of messages held while the window was shut |
| `/file [n]` | 文件 | Resend long output n as a file |
| `/help` | 帮助 | The command list |

`/pull` answers `Nothing is held back` once the backlog is empty. `/file` without a
number resends the most recent one.

Chinese keyboards are taken as they type. Desktop WeChat with a pinyin input
method turns the `/` key into `、`, which counts as `/` in front of a command
word, so `、ls` is `/ls`; full-width characters such as `／ｌｓ` or `２` read as
their ASCII selves. The number of `/use`, `/close`, `/file` and `/pair` may
follow the word without a space, as in `切换2` or `关闭2`, and a one-word command
may end in a full stop, the way voice input writes it (`停。`).

切换, 关闭, 文件, 语言 and 配对 are everyday words, so they make a command only
when the rest of the message is exactly what the command takes: `切换 2`,
`关闭`, `文件 3`, `语言 英文`, `配对 123456`. A message such as
`文件 main.go 里有个 bug` or `关闭 2 个测试` goes to the agent as text.

## Messages in pieces

People often split one request over several chat messages. While the session is
idle, Ferry waits a moment before it starts the turn: every new message restarts
a 2.5-second wait, and the turn starts at most ten seconds after the first
message. Everything that arrived goes to the agent as one prompt, with a single
receipt, and the typing indicator shows while Ferry waits. A picture or file
sent without words waits four times as long, since the question usually follows
in a message of its own.

Commands and approval answers are still handled at once, and `/stop` during the
wait drops what was gathered. A busy session queues messages as before.
`input.gather` in the configuration sets the wait, and `"0s"` turns it off.

## Voice messages

WeChat transcribes voice messages itself. Ferry hands the transcription to the
agent, marked as one so the agent allows for homophones in names and
identifiers; the recording stays on disk but is not passed on, since no agent
can play it. The receipt repeats what WeChat heard: if it misheard, send `/stop`
(or 停) before the agent acts on it. A voice message WeChat could not transcribe
gets a request to say it again or type it. A voice message can refuse an
approval but not allow one, as described below.

## Approvals

When an agent wants to do something the policy does not decide on its own, you
get a message carrying a five-letter code, the tool name, a description and a
truncated preview of the input. Answer with the code:

```text
y abcde
n abcde
a abcde
```

The Chinese words work the same way, with or without a space before the code:
`同意 abcde` (or 允许) allows once, `拒绝 abcde` (or 不同意, 不允许) refuses, and
`总是 abcde` (or 总是允许, 始终允许) always allows. A comma or colon between the
word and the code, and a trailing full stop, are fine.

While it is the only approval waiting, `y`, `n`, `同意` or `拒绝` on its own
answers it. Ferry takes a word without a code only when it cannot be mistaken:
the approval belongs to the session in the foreground, its question was
delivered at least three seconds ago, and no other approval in this chat timed
out or was cancelled in the last minute. Otherwise you are asked to add the
code, and the word goes nowhere else. With no approval waiting, the same word is
ordinary text for the agent. "Always allow" always needs the code. So do the
approvals of a Claude Code channel session: Claude Code asks in its terminal as
well, and a request answered there stays open in chat until it times out, so a
bare word next to one is ordinary text for the session.

Anything else you send while the session waits on an approval is held back
until the approval is settled. The first such message gets a note saying so,
so that an answer like 好的 is not mistaken for a decision.

A voice message can refuse an approval but never allow one: WeChat's
transcription can drop the 不 of 不同意. Type your answer to allow.

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
