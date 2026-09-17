# Agents

**English** · [中文版](../zh-CN/agents.md)

Which coding agents Ferry can drive and how one is configured. Outline only —
content arrives with P1, extra drivers with P2 and P3.

## Presets

The shipped presets and what each one fixes: the command line, the driver kind
and the default working directory. The default preset is `qwen-code`. Lands in
P1.

## Drivers

`acp` for agents speaking the Agent Client Protocol, `server` for agents with
their own server mode, and later `proxy`, `claude-channel` and `pty`. Which
capability each driver can and cannot offer. Lands in P1 and P2.

## Workspace roots

The directories an agent is allowed to read and write, and what happens to a
tool call that reaches outside them. Lands in P1.

## Sessions and state

Starting, idling, running, awaiting an approval, parking and closing — and what
each state means for a message that arrives right then. Lands in P1.

## Adding an agent

How to point Ferry at an agent that has no preset yet. Lands in P2.
