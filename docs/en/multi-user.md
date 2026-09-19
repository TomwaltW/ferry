# Multiple users

**English** · [中文版](../zh-CN/multi-user.md)

One Ferry daemon can serve more than one person: the owner, plus anyone the
owner pairs. By default everyone works in the same `workspace.roots`, as in
earlier releases. Turn on per-user workspaces to give each paired user a
directory of their own.

## Who is who

- **Owner.** The account that scanned the WeChat QR code, `owner_open_id` on
  Feishu, or the `owner` a channel server reports. The owner of a bot is the
  owner in **every chat of that bot**, not only in their own direct chat: who
  is speaking is vouched for by the platform, and changing chats does not
  change it. Being the owner of one bot gives nothing on another bot.
- **Paired user.** Anyone else, after the owner confirmed their pairing code
  with `/pair <code>`. Pairing is per chat and sender, exactly as before.
- **User, for isolation.** One sender in one chat. In a direct chat that is one
  person. In a group chat, or a channel server chat several people write to,
  the chat as a whole counts as one user; Ferry does not separate the people
  inside it.

## Turning isolation on

Isolation is off until you configure it, so upgrading changes nothing. Two
settings under `[workspace]` turn it on, and they can be combined:

```toml
[workspace]
roots = ["~/code"]
users_root = "~/ferry-users"

[[workspace.users]]
platform = "wechat"
id = "wxid_colleague"
roots = ["~/code/shared-project"]
```

A user's roots are decided in this order, first match wins:

1. The owner always works in `workspace.roots`. A `[[workspace.users]]` entry
   naming the owner's ID has no effect.
2. A user with a `[[workspace.users]]` entry works in that entry's `roots`.
   `platform` is `wechat`, `feishu` or `channel-<name>` of a configured
   channel server; `id` is that platform's sender ID. Every root must exist.
3. With `users_root` set, everyone else works in
   `<users_root>/<platform>/<name>`, created with mode 0700 the first time it is
   needed. `users_root` itself must exist.
4. Without `users_root`, everyone else shares `workspace.roots`.

## The per-user directory

The directory name is `<safe>-<hash>`: `safe` is the sender ID with every byte
outside `A-Z a-z 0-9 . _ -` (and a leading dot) replaced by `_`, cut to 32
bytes; `hash` is the first 16 hex digits of `sha256("<platform>:<id>")`. For
example `wechat/wxid_abc_im.wechat-c9668c000037e6b6`. The layout never changes
between releases, and no ID can turn it into `.` or `..` or collide with
another ID that only looks similar. When a user is paired and has a directory
of their own, the confirmation tells them where it is.

## What isolation covers

| Where | Behaviour |
| --- | --- |
| `/new <agent> [dir]` | `dir` must be inside the sender's roots, otherwise the reply is "directory not allowed". Without `dir`, the first of the sender's roots is used. |
| `/ls` | Your own sessions, as always, plus only the terminal sessions whose directory is inside your roots. |
| `/use <n>` on a terminal session | Refused when its directory is outside your roots. |
| Approval policy | `outside_workspace` rules are judged against the roots of the session's owner, not the global roots. |

Sessions stay bound to the chat they were started from, so two people in two
direct chats never saw each other's sessions even before this feature. What
isolation adds is the directories and the terminal sessions.

## Approvers per entry point

Who may answer an approval is set per entry point, and `"owner"` in a list
means the owner of that bot:

```toml
[wechat]
approvers = ["owner"]

[feishu]
approvers = ["owner", "ou_teammate"]

[[channel_servers]]
name = "team"
approvers = ["owner"]
```

Leaving `approvers` out means `["owner"]`. Writing an empty list is a
configuration error, because it would leave nobody able to approve. A channel
server uses its own list; it no longer borrows the WeChat one. Approval
requests still go only to the chat the session belongs to.
