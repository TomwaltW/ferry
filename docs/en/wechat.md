# WeChat entry

**English** · [中文版](../zh-CN/wechat.md)

How Ferry talks to WeChat through the iLink gateway. Outline only — content
arrives with P1, and the reply-window measurements with P2.

## Logging in

The QR code flow, the bot token, and what happens when the platform session
expires mid-session. Lands in P1.

## Pairing and stranger gating

Who is allowed to drive a session, how the pairing code is issued and consumed,
and what an unpaired sender is answered with. Lands in P1.

## Reply windows

WeChat only lets a bot answer inside a window opened by an inbound message. What
a reply context is, how long it lasts, and how many replies fit in one window.
Measured during P1 manual acceptance, used by P2.

## Delivery and retries

How the outbox deduplicates a retried message, what it holds until a window
reopens, and what it drops. Lands in P2.

## Attachments

Images, files and voice messages, including what is downloaded locally and what
is only referenced. Lands in P2.
