# Handoff

**English** · [中文版](../zh-CN/handoff.md)

Moving one session between your terminal and the chat window without two sides
driving it at once. Outline only — content arrives with P2.

## Why handoff

What is lost when chat and terminal each start their own session, and what a
single shared session buys you. Lands in P2.

## Three tiers

How closely chat and your computer share a session depends on the agent and on
how you run it:

| Tier | How | Both sides at once |
| --- | --- | --- |
| 1. Takeover | `/ls` in chat lists the agent's recent terminal sessions, `/use` resumes one over ACP, `ferry back` hands it back to the terminal. | No — one side drives at a time. |
| 2. Shared server | Agents with a server mode (`opencode serve`, `qwen serve`): the terminal and chat talk to the same running session. | Yes |
| 3. Proxy direct | Run the agent through `ferry proxy <agent>` from an ACP editor such as Zed: the editor drives, chat watches, and with `--allow-inject` can type into it. See [proxy.md](proxy.md). | Yes |

## Claude Code plugin

Claude Code can be reached from chat through the optional `ferry channel`
plugin, which relays messages and tool-use approvals into a running Claude Code
session. It needs **Anthropic's own authentication** (a claude.ai account or a
Console API key) and does not work on Bedrock, Vertex AI or Foundry. Setup and
limits are in [claude-code.md](claude-code.md).

## Taking over from chat

Claiming a session that was started in the terminal, what the terminal shows
while chat is driving, and what happens to output produced mid-turn. Lands
in P2.

## Handing back

Releasing the session, and how the terminal picks the transcript back up.
Lands in P2.

## Exclusivity and locks

Why only one driver may hold a session, what a held session answers, and how a
stale lock is cleared. Lands in P2.

## Busy terminals

Before `/use` takes over a terminal session, Ferry checks whether the agent is
still running in a terminal in that session's directory. If it is, the reply
says the session is in use and nothing is taken over; quit the agent on the
computer, or send `/use <n> force` to take it anyway. Agents Ferry started
itself are never counted.

This works on Linux, macOS and Windows. On Windows Ferry reads the process
table itself: it recognises the agent's command line with backslash paths, a
`.exe` suffix and quotes, and compares directories without regard to case. A
process it is not allowed to read — another user's, a protected one, or a
32-bit process next to a 64-bit Ferry — is skipped. If the process table
cannot be read at all, the reply says Ferry cannot tell whether a terminal
holds the session; send `/use <n> force` once you are sure it is closed.

## Limits

Which agents support handoff, and what degrades when one does not. Lands in P2
and P3.
