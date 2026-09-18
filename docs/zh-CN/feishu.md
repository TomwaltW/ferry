# 飞书入口

[English](../en/feishu.md) · **中文**

飞书（海外版为 Lark）是 Ferry 的第二个聊天入口。同一个守护进程同时服务微信和
飞书，两边的命令、审批和会话行为完全一致。

## 创建应用

在飞书开放平台为你的企业建一个自建应用，然后：

1. 开启机器人能力。
2. 事件订阅选择**长连接（WebSocket）**方式，订阅「接收消息」事件。不需要公网地址。
3. 开通读取与发送单聊消息的权限。
4. 记下 App ID 与 App Secret，并找到你自己的 open_id。

## 登录

```sh
ferry login feishu
```

命令依次询问 App ID、App Secret 和你的 open_id，向飞书校验通过后保存：
Secret 存进系统钥匙串，**不会写进配置文件**；App ID 和 open_id 写进 `[feishu]` 表。
重启守护进程后生效。

```toml
[feishu]
enabled = true
app_id = "cli_xxxxxxxx"
owner_open_id = "ou_xxxxxxxx"
approvers = ["owner"]
domain = "feishu"
```

`domain` 取 `feishu` 或 `lark`。`ferry logout feishu` 删除保存的 Secret。

## 会话与隔离

每个入口各管各的会话。飞书里的 `/ls` 只列出从飞书开的会话，微信看不到它们；
反过来也一样。每个平台的 owner 分开设置：飞书这边是 `owner_open_id`，
微信那边是扫码的那个号。

在聊天以外开的会话 —— `ferry proxy`、`ferry channel`、`ferry run` —— 汇报给
`external.bind_platform` 指定的平台，默认是微信：

```toml
[external]
bind_platform = "feishu"
```

## 审批

审批是纯文本，和微信完全一样：回复 `y <编号>` 允许一次，`a <编号>` 总是允许，
`n <编号>` 拒绝。交互卡片放在之后的版本。

## 限制

- 只处理单聊，群消息一律忽略。
- 飞书没有回复窗口，所以不会压消息；发送速率上限为每秒 5 条，被飞书限流的消息
  按 1、2、4 秒退避重试。
- SDK 会自动重连。连接断开超过 5 分钟时，飞书被标为离线，并向 owner 发出唤醒提醒。
