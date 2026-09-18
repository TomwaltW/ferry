package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	kservice "github.com/kardianos/service"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
)

// ExitError carries the exit code of a command that has already printed its
// message.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

var (
	exit1 = &ExitError{Code: 1}
	exit2 = &ExitError{Code: 2}
)

// Env is everything install, uninstall and status need from the outside.
// cmd/ferry fills it from the real system; tests fill it with fakes.
type Env struct {
	GOOS string
	// Home is the absolute FERRY_HOME.
	Home string
	// Executable is the absolute path of the ferry binary.
	Executable string
	// CurrentUser returns the login name the service will run as.
	CurrentUser func() (string, error)
	// Open returns the service manager for a definition; an error wrapping
	// ErrUnsupported means the system cannot host the service.
	Open func(cfg *kservice.Config) (Manager, error)
	// Elevated reports administrator rights (only consulted on Windows).
	Elevated func() bool
	// ReadPassword reads a password without echoing it.
	ReadPassword func() (string, error)
	Bundle       *i18n.Bundle
	Out, Err     io.Writer
}

// lang is the configured language, or zh-CN before there is a config.
func (e *Env) lang() fsp.Lang {
	cfg, err := config.Load(config.Path(e.Home))
	if err != nil || cfg.Lang == "" {
		return fsp.LangZH
	}
	return cfg.Lang
}

func (e *Env) say(w io.Writer, key string, args map[string]any) {
	fmt.Fprintln(w, e.Bundle.T(e.lang(), key, args))
}

// open builds the definition for this system and opens its manager. An
// unsupported system is reported here, with exit code 2.
func (e *Env) open(p Params) (Manager, error) {
	p.Executable, p.Home = e.Executable, e.Home
	cfg := BuildConfig(e.GOOS, p)
	m, err := e.Open(&cfg)
	if errors.Is(err, ErrUnsupported) {
		e.say(e.Err, "cli.service_unsupported", map[string]any{"Reason": err.Error()})
		return nil, exit2
	}
	return m, err
}

func (e *Env) sayStatus(w io.Writer, state string) {
	lang := e.lang()
	fmt.Fprintln(w, e.Bundle.T(lang, "cli.service_status", map[string]any{
		"Name":  Name,
		"State": e.Bundle.T(lang, "service.state."+state, nil),
	}))
}

// Install registers the service and starts it.
func Install(e *Env) error {
	if _, err := config.Load(config.Path(e.Home)); err != nil {
		if errors.Is(err, fsp.ErrNotFound) {
			e.say(e.Err, "cli.need_init", nil)
			return exit1
		}
		return err
	}

	m, err := e.open(Params{})
	if err != nil {
		return err
	}
	if _, err := m.Status(); !errors.Is(err, kservice.ErrNotInstalled) {
		// Status succeeds for an installed service and fails with something
		// other than ErrNotInstalled for one it cannot read (a failed unit),
		// which is installed as well.
		e.say(e.Err, "cli.service_already_installed", map[string]any{"Name": Name})
		return exit1
	}

	var p Params
	if e.GOOS == "windows" {
		if !e.Elevated() {
			e.say(e.Err, "cli.service_needs_admin", nil)
			return exit1
		}
		if p.User, err = e.CurrentUser(); err != nil {
			return err
		}
		e.say(e.Err, "cli.service_password", map[string]any{"User": p.User})
		if p.Password, err = e.ReadPassword(); err != nil {
			return err
		}
		// Only this manager carries the password; it goes to the service
		// manager in Install and dies with the process.
		if m, err = e.open(p); err != nil {
			return err
		}
	}

	// launchd opens its stdout and stderr files under <home>/logs before
	// ferry has a chance to create the directory.
	if err := os.MkdirAll(filepath.Join(e.Home, "logs"), 0o700); err != nil {
		return err
	}
	if err := m.Install(); err != nil {
		if IsAccessDenied(err) {
			e.say(e.Err, "cli.service_needs_admin", nil)
			return exit1
		}
		return fmt.Errorf("install service: %w", err)
	}
	if err := m.Start(); err != nil {
		// Leave nothing half installed, so a retry after fixing the cause
		// does not stop at "already installed".
		_ = m.Uninstall()
		if IsLogonRightMissing(err) {
			e.say(e.Err, "cli.service_logon_right", map[string]any{"User": p.User})
			return exit1
		}
		if IsAccessDenied(err) {
			e.say(e.Err, "cli.service_needs_admin", nil)
			return exit1
		}
		return fmt.Errorf("start service: %w", err)
	}

	e.say(e.Out, "cli.service_installed", map[string]any{"Name": Name})
	if e.GOOS == "linux" {
		user, err := e.CurrentUser()
		if err != nil {
			return err
		}
		e.say(e.Out, "cli.service_linger", map[string]any{"User": user})
	}
	return nil
}

// Uninstall stops the service if it is running and removes it.
func Uninstall(e *Env) error {
	m, err := e.open(Params{})
	if err != nil {
		return err
	}
	st, err := m.Status()
	if errors.Is(err, kservice.ErrNotInstalled) {
		e.sayStatus(e.Out, "not_installed")
		return exit1
	}
	if e.GOOS == "windows" && !e.Elevated() {
		e.say(e.Err, "cli.service_needs_admin", nil)
		return exit1
	}
	// A service that is not running is not an error to stop; one that is
	// running and will not stop is.
	if err := m.Stop(); err != nil && st == kservice.StatusRunning {
		if IsAccessDenied(err) {
			e.say(e.Err, "cli.service_needs_admin", nil)
			return exit1
		}
		return fmt.Errorf("stop service: %w", err)
	}
	if err := m.Uninstall(); err != nil {
		if IsAccessDenied(err) {
			e.say(e.Err, "cli.service_needs_admin", nil)
			return exit1
		}
		return fmt.Errorf("uninstall service: %w", err)
	}
	e.say(e.Out, "cli.service_uninstalled", map[string]any{"Name": Name})
	return nil
}

// Status prints the state of the service. Anything it cannot determine is
// reported as unknown rather than as a failure.
func Status(e *Env) error {
	m, err := e.open(Params{})
	if err != nil {
		return err
	}
	e.sayStatus(e.Out, stateOf(m.Status()))
	return nil
}

func stateOf(st kservice.Status, err error) string {
	switch {
	case errors.Is(err, kservice.ErrNotInstalled):
		return "not_installed"
	case err != nil:
		return "unknown"
	case st == kservice.StatusRunning:
		return "running"
	case st == kservice.StatusStopped:
		return "stopped"
	default:
		return "unknown"
	}
}
