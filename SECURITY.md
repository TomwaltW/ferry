# Security Policy

**English** · [中文版](SECURITY.zh-CN.md)

Ferry runs on your own machine, holds chat platform credentials, and can let a
remote chat message drive a coding agent over your source tree. Treat a bug in
it as a bug with real blast radius.

## Reporting a vulnerability

Mail `security@example.com` with a description, a minimal reproduction and the
version from `ferry version`.

Placeholder — this address must be replaced with the real contact before the
first public release.

Please do not open a public issue for a vulnerability until a fix is released.
Expect an acknowledgement within a few days; Ferry is maintained by a single
developer, so response times are best effort.

## Supported versions

Ferry has not reached 1.0. Only the latest tagged release and `main` receive
fixes. Pre-1.0 versions may change behaviour between minor releases.

## What we consider a vulnerability

- Leaking a bot token, a platform session token, a pairing code or a message
  body into logs, crash output or a release artifact.
- Letting an unpaired or unauthorised sender start, resume or drive a session.
- Escaping the configured workspace roots, so that an agent reads or writes a
  path the operator never allowed.
- Bypassing the approval flow, so that a tool call runs without the decision it
  was supposed to wait for.
- Accepting a forged or replayed inbound message as genuine.

## What is out of scope

- Anything an operator can only do to their own machine on purpose.
- Weaknesses of the upstream chat platform or of the agent being driven.
- Missing hardening in the test doubles under `internal/testkit/`, which are
  never meant to face a network.
