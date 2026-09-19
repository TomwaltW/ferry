# Wrapping a terminal program

**English** · [中文版](../zh-CN/pty.md)

`ferry run` puts a command-line program that speaks no agent protocol at all
into a pseudo terminal, so chat can watch it and type into it. It is an
experimental feature, available on Linux, macOS and Windows.

## Usage

```sh
ferry run [--name <name>] [--idle <ms>] [--prompt-regex <re>] -- <command> [args...]
```

For example:

```sh
ferry run --name aider -- aider --no-auto-commits
```

The terminal stays yours: the program runs in it as usual, in raw mode, and
follows window resizes. Start `ferry daemon` first; without it `ferry run` is a
plain wrapper and nothing reaches chat. When the program exits, `ferry run`
exits with the same code and the chat session closes.

## How input from chat is typed

A message from chat is not typed at once. `ferry run` waits until the program
has printed nothing for `--idle` milliseconds (1500 by default) and, when
`--prompt-regex` is set, until the last line on screen matches it. It then
pastes the text as a bracketed paste and presses Enter. If you pressed a key in
the last two seconds, it waits until you stop.

The output that follows is collected until the program is quiet again, cleaned
of colour codes, carriage-return redraws and the echo of what was typed, and
sent to chat as one reply.

## Flags

| Flag | Effect |
| --- | --- |
| `--name <name>` | What the session is called in chat; the command name by default. |
| `--idle <ms>` | How long the program must be quiet before chat input is typed. |
| `--prompt-regex <re>` | Only type when the last line matches, such as `^> $`. |

## Limits

- No approvals are relayed: the program has no way to ask for one. Chat is told
  so when the session starts; answer prompts on the computer.
- Output detection is a heuristic. A program that keeps redrawing the screen
  never looks idle; set `--prompt-regex` for such programs.
- On Windows it needs ConPTY, which arrived in Windows 10 version 1809; on
  older Windows `ferry run` says it is unsupported and exits. Input from chat
  is typed the same way as on Linux and macOS.
