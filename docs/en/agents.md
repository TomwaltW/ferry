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

## ACP version

Each agent entry picks the version of the Agent Client Protocol Ferry speaks to
it with `acp_version`:

```toml
[[agents]]
name = "my-agent"
command = ["my-agent", "--acp"]
acp_version = "auto"
```

| Value | Behaviour |
| --- | --- |
| `v1` | The default. Ferry speaks ACP v1, as every earlier release did. |
| `v2` | Ferry speaks ACP v2: `session/resume` instead of `session/load`, a turn that ends with an idle state update, `session/list` and `session/close`. An agent that answers the handshake with v1 fails to start. |
| `auto` | Ferry offers v2 first; an agent that negotiates down to v1 is restarted and spoken to over v1. |

ACP v2 is still evolving; keep `v1` unless the agent's documentation says it
speaks v2. When a v2 agent needs a login that requires your input, the session
does not start and chat asks you to log in with the agent in a terminal first;
logins that need no interaction are done automatically.
