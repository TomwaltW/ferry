# 多用户

[English](../en/multi-user.md) · **中文**

一个 Ferry 守护进程可以服务不止一个人：owner，加上 owner 配对过的人。默认所有人
共用同一个 `workspace.roots`，和之前的版本一样。打开按用户隔离后，每个配对用户
都有自己的工作目录。

## 谁是谁

- **owner。** 扫微信二维码的那个号、飞书的 `owner_open_id`，或 channel server
  报上来的 `owner`。一个 bot 的 owner 在**这个 bot 的每一个聊天里**都是 owner，
  不只是在自己的私聊里：说话的是谁由平台担保，换个聊天并不会改变。
  在一个 bot 上是 owner，在另一个 bot 上什么也不是。
- **已配对用户。** 其他任何人，在 owner 用 `/pair <配对码>` 确认之后。
  配对仍然按「聊天 + 发送者」进行，和以前一样。
- **隔离意义上的「用户」。** 一个聊天里的一个发送者。私聊里就是一个人。
  群聊、或者多人共用的 channel server 聊天里，整个聊天算作一个用户，
  Ferry 不会把里面的人再分开。

## 打开隔离

不配置就不隔离，所以升级不改变任何行为。`[workspace]` 下的两项设置可以打开它，
两者可以同时用：

```toml
[workspace]
roots = ["~/code"]
users_root = "~/ferry-users"

[[workspace.users]]
platform = "wechat"
id = "wxid_colleague"
roots = ["~/code/shared-project"]
```

一个用户的工作区按下面的顺序决定，先命中者为准：

1. owner 永远用 `workspace.roots`。写着 owner ID 的 `[[workspace.users]]` 条目不生效。
2. 有 `[[workspace.users]]` 条目的用户用该条目的 `roots`。`platform` 取 `wechat`、
   `feishu`，或已配置的 channel server 对应的 `channel-<name>`；`id` 是该平台上的
   发送者 ID。每个 root 都必须已存在。
3. 设了 `users_root` 时，其余所有人用 `<users_root>/<platform>/<name>`，第一次
   用到时以 0700 权限创建。`users_root` 本身必须已存在。
4. 没设 `users_root` 时，其余所有人共用 `workspace.roots`。

## 每个用户的目录

目录名是 `<safe>-<hash>`：`safe` 是把发送者 ID 里 `A-Z a-z 0-9 . _ -` 以外的字节
（以及开头的点）换成 `_`，再截到 32 字节；`hash` 是 `sha256("<platform>:<id>")`
的前 16 个十六进制字符。例如 `wechat/wxid_abc_im.wechat-c9668c000037e6b6`。
这个布局在各版本之间不会变，任何 ID 都不会让它变成 `.` 或 `..`，也不会和一个
只是长得像的 ID 撞在一起。用户配对成功、并且有自己的目录时，确认消息会告诉他
目录在哪。

## 隔离管到哪些地方

| 位置 | 行为 |
| --- | --- |
| `/new <agent> [目录]` | 目录必须在发送者的工作区内，否则回复「目录不在允许范围内」。不带目录时用发送者的第一个 root。 |
| `/ls` | 照旧列出你自己的会话，终端会话只列目录在你工作区内的。 |
| 对终端会话 `/use <n>` | 目录在你工作区外时拒绝。 |
| 审批策略 | `outside_workspace` 规则按会话所属用户的工作区判定，不再用全局 roots。 |

会话仍然绑定在开它的那个聊天上，所以两个人各自私聊，在这个功能之前也看不到
对方的会话。隔离新增的是目录和终端会话这两块。

## 按入口配置审批人

谁能裁决审批按入口配置，名单里的 `"owner"` 指这个 bot 的 owner：

```toml
[wechat]
approvers = ["owner"]

[feishu]
approvers = ["owner", "ou_teammate"]

[[channel_servers]]
name = "team"
approvers = ["owner"]
```

不写 `approvers` 就是 `["owner"]`。写一个空表是配置错误，因为那样就没人能批了。
channel server 用它自己的名单，不再借用微信的。审批请求仍然只发往会话所属的那个聊天。
