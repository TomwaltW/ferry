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
