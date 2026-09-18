# 编辑器代理

[English](../en/proxy.md) · **中文**

`ferry proxy <agent>` 夹在 ACP 编辑器（Zed、JetBrains 系 IDE、VS Code 扩展、Toad）
和它要跑的 agent 中间。编辑器照常用；同一个会话会镜像到你的聊天里，你可以在手机上
看着它，在允许的情况下还能往里发消息、在聊天里处理审批。

## 配置

把编辑器里「外部 agent」的命令从 agent 本身换成 Ferry。以 Zed 为例：

```json
{
  "agent_servers": {
    "Qwen via Ferry": {
      "command": "ferry",
      "args": ["proxy", "qwen-code"]
    }
  }
}
```

具体的配置格式以编辑器自己的「外部 agent」文档为准。agent 名是预设名，或你配置里
某个 `[[agents]]` 条目的名字；在聊天里发 `/agents` 可以查看。proxy 读取和守护进程同一份配置
（`FERRY_HOME`，默认 `~/.ferry`）。先把 `ferry daemon` 跑起来；守护进程没运行时，
proxy 只做透明转发，在 stderr 打一行提示，不会同步到聊天。

## 聊天里看到什么

每个编辑器会话接入时，聊天里会收到一条通知，写明 agent 和目录，并像其他会话
一样分到一个编号。编辑器发起的一轮会先标一句「编辑器发起了新一轮」，随后是
agent 的输出。会话汇报给 `external.bind_platform` 指定的平台（默认微信）。

## 参数

| 参数 | 作用 |
| --- | --- |
| `--allow-inject` | 编辑器空闲时，聊天里发的消息会送给 agent；编辑器里显示为你发的一条消息，后面跟着回答。不加这个参数时聊天只能看。 |
| `--remote-approve` | 权限请求改发到聊天，用 `y <编号>` / `n <编号>` 回复；编辑器不再弹。 |
| `--title <名称>` | 这个会话在聊天里的名字。 |

```json
{"command": "ferry", "args": ["proxy", "--allow-inject", "--remote-approve", "qwen-code"]}
```

## 审批

默认情况下权限请求只发给编辑器。聊天里只会收到「编辑器里有待处理的审批」的提示，
在哪儿坐着就在哪儿答。加了 `--remote-approve` 后，请求只发到聊天，编辑器完全看不到。

## 限制

- 编辑器的一轮在跑时，聊天发来的消息排队；聊天的一轮在跑时，编辑器发起的也排队。
- 编辑器断开时，进行中的一轮会被取消，agent 被关闭，聊天里的会话随之结束。
- ACP SDK 不认识的扩展方法一律回复 method not found。
