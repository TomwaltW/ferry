# Contributing to Ferry

**English** · [中文版](CONTRIBUTING.zh-CN.md)

Ferry is built in phases. Each phase freezes a contract first, then runs several
tasks in parallel against it. The full build plan lives in `docs/dev/`, which is
Chinese-only by design and is not part of the bilingual documentation set.

## Ground rules

- The frozen contract of a phase cannot be changed by a parallel task. If you
  need something that is not in it, stop and report instead of inventing it.
- Every file has exactly one owner. Files outside your whitelist are read-only.
- Do not add a dependency on your own. `go.mod` is changed only by the task that
  opens a phase.

## Code conventions

- A function that can block takes `context.Context` as its first parameter.
- Time is injected through `internal/clock`. Tests advance a fake clock instead
  of sleeping on business timeouts.
- Errors use the sentinels in `internal/fsp`, wrapped with
  `fmt.Errorf("...: %w", err)`. Do not panic outside placeholder code.
- Logging is `log/slog`. Never log tokens, pairing codes or message bodies; log
  the length of a body, not the body.
- Every user-facing string goes through i18n. Keys live in `locales/en.toml` and
  `locales/zh-CN.toml`; no Chinese or English literals in code.
- Test doubles live in `internal/testkit/**` or in a local `_test.go`. Prefer
  duplication over a shared utils package.
- Identifiers, comments and commit messages are English. User-facing
  documentation is bilingual.

## Bilingual duty

This is not optional, and it is enforced by a tool.

- `README.md`, `CONTRIBUTING.md`, `SECURITY.md` and `spec/fsp.md` each have a
  `.zh-CN.md` counterpart. Change one, change both in the same commit.
- `docs/en/` and `docs/zh-CN/` hold exactly the same set of relative paths.
- Two counterparts must agree on the number of `## ` and `### ` headings and on
  the number of fenced code blocks, and their code blocks must be identical once
  comments are stripped. Translate the comments, never the commands.
- The English file links to the Chinese one within its first five lines, and the
  Chinese file links back to the English one.
- `locales/en.toml` and `locales/zh-CN.toml` carry exactly the same key set.
- A Chinese file may pin the English revision it was translated from with
  `<!-- source_commit: abc1234 -->` on its first line.

## Before you commit

```sh
go build ./...
go vet ./...
test -z "$(gofmt -l .)"
go test ./...
go run ./scripts/check-i18n
```

## Commit messages

One line, English, in the form `pN-tK: what changed`, for example
`p1-t2: acp driver and registry`. Contributors do not push tags.
