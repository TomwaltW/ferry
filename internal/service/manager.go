package service

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"

	kservice "github.com/kardianos/service"
)

// Manager is the part of a kardianos service that Ferry drives. The value
// Open returns is a kardianos service; tests use a fake.
type Manager interface {
	Install() error
	Uninstall() error
	Start() error
	Stop() error
	// Status returns kservice.ErrNotInstalled when the service is absent.
	Status() (kservice.Status, error)
	// Run blocks while the service runs, answering the Windows service
	// manager or waiting for SIGTERM on Unix.
	Run() error
}

// ErrUnsupported means this system cannot host Ferry as a service; the
// wrapped text is the reason shown to the user.
var ErrUnsupported = errors.New("service manager not supported")

// Windows error codes the command turns into hints.
const (
	errnoAccessDenied       syscall.Errno = 5    // ERROR_ACCESS_DENIED
	errnoServiceLogonFailed syscall.Errno = 1069 // ERROR_SERVICE_LOGON_FAILED
)

// IsAccessDenied reports whether err is Windows refusing a service manager
// call to a process that is not elevated.
func IsAccessDenied(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == errnoAccessDenied
}

// IsLogonRightMissing reports whether err is Windows refusing to start the
// service as its account, which in practice means the account lacks the
// Log on as a service right.
func IsLogonRightMissing(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == errnoServiceLogonFailed
}

// Open returns the service manager of this system for cfg. prog is what Run
// hosts; commands that only install or query pass nil.
func Open(cfg *kservice.Config, prog kservice.Interface) (Manager, error) {
	if err := supported(runtime.GOOS); err != nil {
		return nil, err
	}
	if prog == nil {
		prog = idle{}
	}
	s, err := kservice.New(prog, cfg)
	if errors.Is(err, kservice.ErrNoServiceSystemDetected) {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// supported rules out the systems where Ferry could not run as its user:
// anything but macOS, Linux and Windows, and Linux without a systemd user
// instance (a system unit would run as root).
func supported(goos string) error {
	switch goos {
	case "darwin", "windows":
		return nil
	case "linux":
		if p := kservice.Platform(); p != "linux-systemd" {
			if p == "" {
				p = "none"
			}
			return fmt.Errorf("%w: only systemd user services are supported, the service manager here is %s", ErrUnsupported, p)
		}
		if out, err := exec.Command("systemctl", "--user", "show-environment").CombinedOutput(); err != nil {
			return fmt.Errorf("%w: no systemd user instance (systemctl --user: %v %s)", ErrUnsupported, err, trimOutput(out))
		}
		return nil
	default:
		return fmt.Errorf("%w: ferry service supports macOS, Linux and Windows, not %s", ErrUnsupported, goos)
	}
}

func trimOutput(out []byte) string {
	const max = 200
	if len(out) > max {
		out = out[:max]
	}
	return string(out)
}

// idle is the program of a manager that is never Run.
type idle struct{}

func (idle) Start(kservice.Service) error { return nil }
func (idle) Stop(kservice.Service) error  { return nil }
