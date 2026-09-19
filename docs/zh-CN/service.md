# 作为系统服务运行

[English](../en/service.md) · **中文**

`ferry service` 把守护进程交给操作系统的服务管理器，让它自己启动、崩溃后自己
拉起来。它和 `ferry daemon` 是同一个守护进程：同一份配置、同一把锁、同一个日志文件。

## 命令

```sh
ferry service install
ferry service status
ferry service uninstall
```

- `install` 注册服务并立即启动。先跑 `ferry init` 并登录好各个入口；没有配置时
  它会提示后退出。已经装过的话，先卸载再装。
- `status` 打印运行中、已停止、未安装或状态未知。
- `uninstall` 先停止服务（没在跑不算错），再注销。

服务记住的是安装时 `ferry` 可执行文件的绝对路径和当时生效的 `FERRY_HOME`。
挪了可执行文件或者改了 `FERRY_HOME`，要先卸载再重装。服务在跑的时候不要再手动
跑 `ferry daemon`：同一时刻只有一个守护进程能拿到锁。

## 以谁的身份运行

三个平台上，服务都以**安装它的那个人**的身份运行，绝不用 root 或 LocalSystem。
你的凭据在你自己的钥匙串里，agent 的登录态和配置在你自己的 home 目录里；
而一个提权运行的守护进程，会把权限交给每一条经审批放行的命令。

## macOS

服务是一个 LaunchAgent。你登录后启动，退出后会被重新拉起。没有人登录之前它不运行。
守护进程的日志照旧在 `<FERRY_HOME>/logs`，和 `ferry daemon` 一样。

## Linux

服务是一个 systemd 用户单元，任何退出都会被重启。用户单元在你登录时启动；
要让它开机后、登录前就启动，执行一次 linger：

```sh
loginctl enable-linger $USER
```

开了 linger 之后，守护进程可能在钥匙串解锁之前就启动。读不到凭据时它会退出，
systemd 会一直重试；你登录、钥匙串解锁之后，下一次重试就会成功。

## Windows

服务是一个以你的账号登录、开机后延迟自动启动的 Windows 服务：

- 在管理员终端里运行 `ferry service install`。
- 它会询问你的 Windows 登录密码。密码只交给 Windows 服务管理器，Ferry 不保存。
- 你的账号需要「作为服务登录」权限。Windows 因缺这个权限拒绝启动服务时，
  Ferry 会提示：在「本地安全策略 → 用户权限分配 → 作为服务登录」里加入这个账号，
  然后重装。Ferry 不会替你改策略。
- 服务名 `ferry` 在一台机器上全局唯一，所以一台机器只跑一个 Ferry 服务。
