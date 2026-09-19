# Running as a service

**English** · [中文版](../zh-CN/service.md)

`ferry service` hands the daemon to your operating system's service manager,
so it starts on its own and comes back after a crash. It is the same daemon as
`ferry daemon`: the same configuration, lock and log file.

## Commands

```sh
ferry service install
ferry service status
ferry service uninstall
```

- `install` registers the service and starts it at once. Run `ferry init` and
  log in to your entry points first; without a configuration it stops and says
  so. If the service is already installed, uninstall it before installing again.
- `status` prints running, stopped, not installed, or unknown.
- `uninstall` stops the service, if it is running, and removes it.

The service remembers the absolute path of the `ferry` binary and the
`FERRY_HOME` in effect when you installed it. After moving the binary or
changing `FERRY_HOME`, uninstall and install again. Do not run `ferry daemon`
by hand while the service is up: only one daemon can hold the lock.

## Whose account it runs as

On every platform the service runs as **the person who installed it**, never as
root or LocalSystem. Your credentials are in your own keychain, your agents'
logins and settings are in your own home directory, and a daemon with raised
privileges would hand those privileges to every command an approval lets
through.

## macOS

The service is a LaunchAgent. It starts when you log in and is restarted if it
exits. It does not run before anyone has logged in. The daemon's log stays in
`<FERRY_HOME>/logs`, as with `ferry daemon`.

## Linux

The service is a systemd user unit, restarted whenever it exits. A user unit
starts when you log in; to start it at boot, before you log in, enable
lingering once:

```sh
loginctl enable-linger $USER
```

With lingering on, the daemon may start before your keyring is unlocked. If it
cannot read its credentials it exits, and systemd keeps retrying; once you log
in and the keyring unlocks, the next attempt succeeds.

## Windows

The service is a Windows service that logs on as your account and starts
automatically, delayed, at boot:

- Run `ferry service install` from an administrator terminal.
- It asks for your Windows password. The password goes to the Windows service
  manager only; Ferry does not keep it.
- Your account needs the "Log on as a service" right. If Windows refuses to
  start the service for lack of it, Ferry says so: add the account under Local
  Security Policy → User Rights Assignment → Log on as a service, and install
  again. Ferry does not change the policy for you.
- The service name `ferry` is machine-wide, so one machine runs one Ferry
  service.
