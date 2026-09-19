# Security

**English** · [中文版](../zh-CN/security.md)

What Ferry protects, what it deliberately does not, and the knobs an operator
has. Outline only — content arrives with P1 and P2. To report a vulnerability,
see [SECURITY.md](../../SECURITY.md).

## Threat model

Ferry sits between a public chat platform and a shell on your machine. Who is
trusted, who is not, and what an attacker gains by reaching each layer. Lands
in P1.

## Secrets

Where the bot token and platform session token are stored, why the system
keyring is preferred, and what is deliberately never written to disk or logs.
Lands in P1.

## Approvals

Why a tool call stops and asks, what the approval code is, who may answer, and
what an unanswered request does when it times out. Lands in P1.

## Path limits

Workspace roots, how a path is resolved before it is checked, and what an
out-of-workspace call is answered with. Lands in P1.

## Logging

What is logged, what is redacted to a length, and what to hand over when
reporting a bug. Lands in P2.

## The owner and channel servers

The owner of a bot is recognised in every chat of that bot, not only in their
own direct chat (see [multi-user.md](multi-user.md)). That gives nothing to
anyone else: everybody else still pairs per chat and sender.

It does matter for third-party channel servers. A message that arrives without
a sender ID (the `user_id` meta key, or the key named by `user_id_meta`) counts
as sent by `local`, and `local` is the default `owner` of a channel server. So
a channel server that does not report sender IDs makes **everyone who can write
into any of its chats the owner**: no pairing, owner approvals, owner
workspace. A channel server that more than one person can write to must report
the sender ID of every message, and its `owner` must be set to the real owner's
ID.

## Private files on Windows

On Linux and macOS, Ferry writes its private files (credentials when the
keyring is unavailable, the configuration, downloaded media, long outputs, the
control endpoint) with mode 0600 and private directories with 0700.

Windows has no such permission bits, and Ferry does not yet set access control
lists of its own. This is a **known gap**, not a solved problem: on Windows the
privacy of those files comes only from the permissions `FERRY_HOME` inherits
from its parent directory. The default `%USERPROFILE%\.ferry` is readable by you,
SYSTEM and Administrators only. If you point `FERRY_HOME` at a shared location,
other local users can read those files. Keep `FERRY_HOME` under your own
profile on Windows.

## Running as a service

`ferry service install` runs the daemon as the account that installed it — a
LaunchAgent on macOS, a systemd user unit on Linux, a Windows service logging
on as you — and never as root or LocalSystem (see [service.md](service.md)).
The daemon, and every agent and command it starts, therefore has exactly your
own permissions: an approved command can do what you can do, and no more. On
Windows your password is handed to the service manager and not stored by
Ferry.
