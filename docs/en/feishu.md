# Feishu entry

**English** · [中文版](../zh-CN/feishu.md)

Feishu (Lark outside China) is Ferry's second chat entry point. One daemon
serves WeChat and Feishu at the same time; the commands, approvals and session
behaviour are the same on both.

## Creating the app

In the Feishu open platform, create a custom app for your organization and:

1. Enable the bot capability.
2. Under event subscriptions, choose the long connection (WebSocket) mode and
   subscribe to the "receive message" event. No public URL is needed.
3. Grant the permissions to read and send direct messages.
4. Note the App ID and the App Secret, and find your own open_id.

## Logging in

```sh
ferry login feishu
```

The command asks for the App ID, the App Secret and your open_id, checks them
against Feishu, and then stores them: the secret goes into the system keyring,
never into the configuration file; the App ID and your open_id are written to
the `[feishu]` table. Restart the daemon to pick the entry point up.

```toml
[feishu]
enabled = true
app_id = "cli_xxxxxxxx"
owner_open_id = "ou_xxxxxxxx"
approvers = ["owner"]
domain = "feishu"
```

`domain` is `feishu` or `lark`. `ferry logout feishu` removes the stored secret.

## Sessions and isolation

Every entry point keeps its own sessions. `/ls` in Feishu lists only the
sessions started from Feishu, and WeChat never sees them; the same holds the
other way round. The owner of each platform is set separately: `owner_open_id`
owns the Feishu side, the account that scanned the QR code owns WeChat.

Sessions started outside chat — `ferry proxy`, `ferry channel`, `ferry run` —
report to the platform named by `external.bind_platform`, WeChat by default:

```toml
[external]
bind_platform = "feishu"
```

## Approvals

Approvals are plain text, exactly as in WeChat: reply `y <code>` to allow once,
`a <code>` to always allow, `n <code>` to refuse. Interactive cards are planned
for a later release.

## Limits

- Direct chats only; messages in group chats are ignored.
- Feishu has no reply window, so nothing is held back; sending is capped at five
  messages a second, and a send Feishu refuses for going too fast is retried
  after 1, 2 and 4 seconds.
- The SDK reconnects on its own. A connection that stays down for more than five
  minutes marks Feishu offline and raises a wake-up reminder to the owner.
