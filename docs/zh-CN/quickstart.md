# 快速上手

[English](../en/quickstart.md) · **中文**

从装好到 agent 在聊天窗口回你话。下面每一步都跑在你自己的机器上 ——
Ferry 是你自己跑的守护进程，不是要注册的服务。

## 安装

```bash
curl -fsSL https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh | sh
ferry version
```

脚本会挑出对应平台的发布版二进制，用 `checksums.txt` 校验 sha256，装进
`~/.local/bin` —— **不需要 Go 工具链**。想装到别处设 `FERRY_INSTALL_DIR`，
想钉住某个版本设 `FERRY_VERSION`。

国内网络下，**脚本本身也要走镜像**，不能只镜像它下载的文件：

```bash
export FERRY_GITHUB_PROXY=https://ghfast.top/
curl -fsSL "${FERRY_GITHUB_PROXY}https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh" | sh
```

有 Go 工具链的话，也可以自己构建：

```bash
go install github.com/TomwaltW/ferry/cmd/ferry@latest
```

配置、数据库、凭据和日志都放在 `FERRY_HOME` 下，默认是 `~/.ferry`。
想同时维护几套环境，把这个环境变量指到别处即可。每套环境的登录态各自独立，
存进系统钥匙串的凭据也按 `FERRY_HOME` 分开，在其中一套里 `ferry logout`
不会登出别的。

从 v0.1.0 之前的版本升级：默认的 `~/.ferry` 自动沿用原来的登录态；
用自定义 `FERRY_HOME`、登录存在系统钥匙串里的环境，升级后是未登录状态，
需要重新 `ferry login` 一次；登录存在 `$FERRY_HOME/credentials/` 下的不受影响。

## 配置

`ferry init` 问三件事：机器人语言、允许 agent 工作的目录、默认用哪个 agent。
回答一次就会写出 `config.toml`。答案也可以直接用参数给，跳过提问；
`--lang` 接 `en` 或 `zh-CN`。

```bash
ferry init
ferry init --yes --root ~/code --agent kimi
ferry doctor
```

`ferry doctor` 每项检查打一行：配置、工作区根目录、凭据、数据库、每个已配置的
agent，以及某些预设需要的 `npx` 与 `uvx` 在不在。它**永远退出 0** ——
那是一份体检报告，不是一道闸门。

## 登录

```bash
ferry login wechat
```

终端会画出二维码。用**你平时聊天的那个微信号**扫它，一般就是主号。
扫码的号就是 owner：在它配对别人之前，只有它能用这个 bot，审批也找它。
bot 不是另一个微信号：登录时它拿到自己的 ID，`ferry login wechat`
成功后会把两个 ID 都打出来。

不要用小号扫、再拿主号跟它聊。那样小号成了 owner，
主号在 bot 眼里是陌生人，发什么都只会收到配对码。

有系统钥匙串时凭据存进钥匙串，没有就落到 `$FERRY_HOME/credentials/`，
权限 0600。`ferry logout wechat` 删掉它们。

## 启动守护进程

```bash
ferry daemon
ferry status
```

守护进程持有单实例锁，日志写在 `$FERRY_HOME/logs/ferry.log`
（满 10 MB 轮转，保留 3 份），每 5 秒刷新一次 `status.json`，
所以 `ferry status` 能告诉你它活着没有、微信在不在线。

## 第一个会话

用你自己的号给 bot 发消息：

```text
/new kimi ~/code/demo
/ls
/help
```

`/new` 会在那个目录里起一个 agent —— 目录必须在你的工作区根目录之内 ——
然后回一个会话编号。之后你发的任何非命令内容，都会作为 prompt 交给那个会话。
Ferry 先等一小会儿看你还有没有后续消息，然后回受理回执，
再发流式进度，本轮结束时发结果。

agent 想执行或修改东西时，你会收到一条带 5 位字母编码的审批请求：

```text
y abcde
n abcde
a abcde
```

`y` 允许一次，`n` 拒绝，`a` 以后这类调用都允许。
「同意」「拒绝」「总是」可以代替 `y`、`n`、`a`；只有一个审批在等时，
单独回 `y` 或「同意」就行。没人回的请求会在超时后按拒绝处理。

## 感觉不对劲的时候

```bash
ferry doctor
ferry status
tail -f ~/.ferry/logs/ferry.log
```

`ferry status` 说微信离线，就是平台会话过期了：重新跑 `ferry login wechat`。
这期间守护进程不会退出，自己会重试，你不用重启它。
