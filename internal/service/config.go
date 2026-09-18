// Package service installs Ferry as a service of the operating system's
// service manager (spec-p4-b1 3.7). It wraps kardianos/service behind the
// Manager interface so the command logic can be tested with a fake, and it
// builds the per-platform service definition as a pure function of goos.
//
// On every platform the service runs as the person who installed it, never
// as root or LocalSystem: the credentials live in that person's keychain and
// an elevated daemon would hand elevation to every command an approval lets
// through.
package service

import (
	"path/filepath"

	kservice "github.com/kardianos/service"
)

// The service definition every platform shares.
const (
	Name        = "ferry"
	DisplayName = "Ferry"
	Description = "Ferry carries your coding agents to chat"
)

// HomeEnv is the variable the service reads its data directory from.
const HomeEnv = "FERRY_HOME"

// Params are the inputs of BuildConfig that differ from one machine to the
// next. The caller resolves them (os.Executable, config.Home, os/user) so the
// function itself stays pure.
type Params struct {
	// Executable is the absolute path of the ferry binary.
	Executable string
	// Home is the absolute FERRY_HOME the service will use.
	Home string
	// User is the account the service runs as. Only Windows needs it: a
	// LaunchAgent and a systemd user unit already run as their owner.
	User string
	// Password is the Windows password of User. It is handed to the service
	// manager and kept nowhere else.
	Password string
}

// systemdUserUnit replaces the kardianos default, which is written for system
// units: it targets multi-user.target, which a user instance never reaches,
// so the unit would never start at login, and it reads /etc/sysconfig. The
// keys and pipes are the ones the default template uses.
const systemdUserUnit = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}

[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if Restart}}Restart={{Restart}}
{{end}}RestartSec=10
{{range EnvVars}}{{.}}
{{end}}
[Install]
WantedBy=default.target
`

// BuildConfig returns the service definition for goos.
//
//   - darwin: a LaunchAgent that starts when the user logs in and is kept alive.
//   - linux: a systemd user unit restarted on any exit.
//   - windows: a service logging on as p.User, started automatically after a
//     delay and restarted when it fails.
func BuildConfig(goos string, p Params) kservice.Config {
	c := kservice.Config{
		Name:        Name,
		DisplayName: DisplayName,
		Description: Description,
		Executable:  p.Executable,
		Arguments:   []string{"service", "run"},
		EnvVars:     map[string]string{HomeEnv: p.Home},
		Option:      kservice.KeyValue{},
	}
	switch goos {
	case "darwin":
		c.Option["UserService"] = true
		c.Option["RunAtLoad"] = true
		c.Option["KeepAlive"] = true
		// launchd's own stdout and stderr files; the daemon's log is
		// <home>/logs/ferry.log as with ferry daemon.
		c.Option["LogDirectory"] = filepath.Join(p.Home, "logs")
	case "linux":
		c.Option["UserService"] = true
		c.Option["Restart"] = "always"
		c.Option["SystemdScript"] = systemdUserUnit
	case "windows":
		c.UserName = p.User
		if p.Password != "" {
			c.Option["Password"] = p.Password
		}
		c.Option["StartType"] = "automatic"
		c.Option["DelayedAutoStart"] = true
		c.Option["OnFailure"] = "restart"
		c.Option["OnFailureDelayDuration"] = "10s"
	}
	return c
}
