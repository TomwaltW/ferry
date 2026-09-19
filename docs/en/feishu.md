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
4. Turn on card callback interaction ("卡片回传交互") for the app, and, still in
   the long connection mode, subscribe to the card callback
   `card.action.trigger`. Approval buttons need it; without it approvals still
   work by text.
5. Publish the app version so the settings take effect.
6. Note the App ID and the App Secret, and find your own open_id.

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

In Feishu an approval arrives as an interactive card. Its title is the
approval code, its body is the same text WeChat would show, and it has the
buttons **Allow once**, **Always allow** (only when the agent offers it) and
**Reject**. A click answers with the toast "Received; working on it"; the card
itself does not change, and the decision is confirmed by a normal message, as
with a text answer. The card goes over the long connection too: no public
callback URL is needed.

Text answers keep working next to the buttons: reply `y <code>` to allow once,
`a <code>` to always allow, `n <code>` to refuse. Whichever answer arrives
first decides; a later click or reply on the same approval is ignored. A click
counts only when the person clicking is an approver (`approvers` in
`[feishu]`), exactly as a text answer does.

If Feishu refuses the card — for example because card interaction is not
enabled for the app — the same approval is sent again as plain text, so it can
still be answered by text.

## Limits

- Direct chats only; messages in group chats are ignored.
- Feishu has no reply window, so nothing is held back; sending is capped at five
  messages a second, and a send Feishu refuses for going too fast is retried
  after 1, 2 and 4 seconds.
- The SDK reconnects on its own. A connection that stays down for more than five
  minutes marks Feishu offline and raises a wake-up reminder to the owner.
