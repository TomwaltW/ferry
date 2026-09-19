# 安全

[English](../en/security.md) · **中文**

Ferry 保护什么、故意不保护什么，以及运维方手里有哪些开关。目前只是提纲 ——
内容随 P1、P2 补充。报告漏洞看 [SECURITY.zh-CN.md](../../SECURITY.zh-CN.md)。

## 威胁模型

Ferry 夹在公开的聊天平台和你机器上的一个 shell 之间。谁可信、谁不可信、
攻击者摸到每一层各能拿到什么。P1 补充。

## 凭据

bot token 和平台会话 token 存在哪、为什么优先用系统钥匙串、
哪些东西是故意不落盘也不进日志的。P1 补充。

## 审批

为什么工具调用会停下来问、审批码是什么、谁有资格裁决、没人裁决超时后会怎样。
P1 补充。

## 路径限制

工作区根目录、路径在被校验之前怎么解析、伸到工作区之外的调用会收到什么回复。
P1 补充。

## 日志

记了什么、什么被缩减成一个长度、报 bug 时该交出哪些东西。P2 补充。

## owner 与 channel server

一个 bot 的 owner 在这个 bot 的每一个聊天里都被认作 owner，不只是在自己的私聊里
（见 [multi-user.md](multi-user.md)）。这不给其他任何人增加权限：其他人仍然按
「聊天 + 发送者」配对。

但对第三方 channel server 有影响。不带发送者 ID（meta 里的 `user_id`，或
`user_id_meta` 指定的键）的消息被当作 `local` 发的，而 `local` 正是 channel server
默认的 `owner`。所以一个不报发送者 ID 的 channel server，会让**任何能往它任一聊天里
发消息的人都成为 owner**：不用配对、能批 owner 的审批、用 owner 的工作区。
多人能写的 channel server 必须给每条消息带上发送者 ID，并把 `owner` 设成真正
owner 的 ID。

## Windows 上的私密文件

在 Linux 和 macOS 上，Ferry 写私密文件（钥匙串不可用时的凭据、配置文件、下载的
媒体、长输出、控制端点）用 0600 权限，私密目录用 0700。

Windows 没有这种权限位，而 Ferry 目前也不自己设置访问控制列表。这是**已知缺口**，
不是已解决的问题：Windows 上这些文件的私密性只来自 `FERRY_HOME` 从父目录继承的权限。
默认的 `%USERPROFILE%\.ferry` 只有你本人、SYSTEM 和 Administrators 能读。把
`FERRY_HOME` 设到共享位置的话，本机的其他用户就能读到这些文件。Windows 上请把
`FERRY_HOME` 放在你自己的用户目录下。

## 作为服务运行

`ferry service install` 以安装者本人的身份运行守护进程 —— macOS 上是 LaunchAgent，
Linux 上是 systemd 用户单元，Windows 上是以你的账号登录的 Windows 服务 —— 绝不用
root 或 LocalSystem（见 [service.md](service.md)）。所以守护进程和它拉起的每个 agent、
每条命令，拥有的恰好是你自己的权限：一条被批准的命令能做的就是你能做的，不会更多。
Windows 上你的密码只交给服务管理器，Ferry 不保存。
