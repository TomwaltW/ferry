# Glossary · 术语表

One file, both languages, so that a translator never has to guess which English
word a Chinese term maps onto. This list is normative: the English column is
what appears in identifiers and log messages, the Chinese column is what appears
in `locales/zh-CN.toml` and in the Chinese documentation.

一份文件装下两种语言，翻译时不用猜某个中文词对应哪个英文词。这张表是规范性的：
英文列是标识符和日志里出现的写法，中文列是 `locales/zh-CN.toml` 和中文文档里
出现的写法。

| English | 中文 | Meaning · 含义 |
| --- | --- | --- |
| session | 会话 | One conversation with one coding agent, with its own working directory, state and history. 与某个编码 agent 的一次对话，自带工作目录、状态和历史。 |
| managed session | 托管会话 | A session Ferry started and owns end to end: it launched the agent process and holds its session ID. Ferry 自己拉起并全程持有的会话 —— agent 进程由它启动，会话 ID 也由它保管。 |
| handoff session | 接力会话 | A session that began in a terminal and can be taken over from chat and handed back, with only one side driving at a time. 在终端里起头、可以被聊天接管再交还的会话，同一时刻只有一侧在驱动。 |
| entry | 入口 | The chat platform Ferry is reachable through, such as WeChat or Feishu. 用户能找到 Ferry 的那个聊天平台，例如微信或飞书。 |
| outbox | 发件箱 | The queue that holds, deduplicates and retries every outbound message until the platform accepts it. 负责暂存、去重和重试每条出站消息，直到平台接收为止的队列。 |
| delivery window | 投递窗口 | The bounded period during which the platform will accept a reply to a given inbound message. 平台愿意接受针对某条入站消息的回复的那段有限时间。 |
| approval | 审批 | The request a tool call raises when it needs a human decision before running. 工具调用在执行前需要人做决定时发出的那个请求。 |
| approver | 审批人 | The paired account entitled to answer an approval. 有资格裁决一个审批的、已配对的账号。 |
| driver | 驱动 | The adapter that speaks one agent's protocol, such as `acp`, `server` or `pty`. 讲某种 agent 协议的适配层，例如 `acp`、`server`、`pty`。 |
| pairing | 配对 | Binding a chat account to the daemon so that it may drive sessions. 把一个聊天账号绑定到守护进程，使它有资格驱动会话。 |

## Notes · 说明

- Do not translate `agent`, `token`, `ACP`, `FSP` or a driver name. Keep them as
  they are in both languages. `agent`、`token`、`ACP`、`FSP` 和驱动名不翻译，
  两种语言里都保持原样。
- A term that is not on this list yet, and that users will see, belongs on it
  before it ships. 用户会看到、但还不在表上的术语，上线之前先补进来。
