package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	kservice "github.com/kardianos/service"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/i18n"
)

const (
	testExe  = "/opt/ferry/bin/ferry"
	testUser = `DESKTOP-1\alice`
)

// fakeManager is an in-memory service manager.
type fakeManager struct {
	installed, running bool
	// statusErr, when set, is what Status returns for an installed service.
	statusErr                                   error
	installErr, startErr, stopErr, uninstallErr error
	calls                                       []string
}

func (f *fakeManager) Install() error {
	f.calls = append(f.calls, "install")
	if f.installErr != nil {
		return f.installErr
	}
	f.installed = true
	return nil
}

func (f *fakeManager) Uninstall() error {
	f.calls = append(f.calls, "uninstall")
	if f.uninstallErr != nil {
		return f.uninstallErr
	}
	f.installed, f.running = false, false
	return nil
}

func (f *fakeManager) Start() error {
	f.calls = append(f.calls, "start")
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

func (f *fakeManager) Stop() error {
	f.calls = append(f.calls, "stop")
	if f.stopErr != nil {
		return f.stopErr
	}
	f.running = false
	return nil
}

func (f *fakeManager) Status() (kservice.Status, error) {
	f.calls = append(f.calls, "status")
	switch {
	case !f.installed:
		return kservice.StatusUnknown, kservice.ErrNotInstalled
	case f.statusErr != nil:
		return kservice.StatusUnknown, f.statusErr
	case f.running:
		return kservice.StatusRunning, nil
	default:
		return kservice.StatusStopped, nil
	}
}

func (f *fakeManager) Run() error { return nil }

type rig struct {
	env      *Env
	fake     *fakeManager
	opened   []kservice.Config
	out, err bytes.Buffer
}

// newRig returns an Env for goos whose home holds a config, backed by a fake
// manager, running elevated, reading "pw" as the password.
func newRig(t *testing.T, goos string) *rig {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Workspace.Roots = []string{home}
	if err := config.Save(config.Path(home), cfg); err != nil {
		t.Fatal(err)
	}
	r := &rig{fake: &fakeManager{}}
	r.env = &Env{
		GOOS:         goos,
		Home:         home,
		Executable:   testExe,
		CurrentUser:  func() (string, error) { return testUser, nil },
		Elevated:     func() bool { return true },
		ReadPassword: func() (string, error) { return "pw", nil },
		Bundle:       i18n.Default(),
		Out:          &r.out,
		Err:          &r.err,
	}
	r.env.Open = func(cfg *kservice.Config) (Manager, error) {
		r.opened = append(r.opened, *cfg)
		return r.fake, nil
	}
	return r
}

func (r *rig) text(key string, args map[string]any) string {
	return r.env.Bundle.T(r.env.lang(), key, args)
}

func (r *rig) statusText(state string) string {
	return r.text("cli.service_status", map[string]any{"Name": Name, "State": r.text("service.state."+state, nil)})
}

func wantExit(t *testing.T, err error, code int) {
	t.Helper()
	if code == 0 {
		if err != nil {
			t.Fatalf("err = %v, want success", err)
		}
		return
	}
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != code {
		t.Fatalf("err = %v, want exit %d", err, code)
	}
}

func wantCalls(t *testing.T, f *fakeManager, want ...string) {
	t.Helper()
	if !slices.Equal(f.calls, want) {
		t.Fatalf("manager calls = %v, want %v", f.calls, want)
	}
}

// summary renders a definition for the test log, which the receipt quotes.
func summary(c kservice.Config) string {
	keys := make([]string, 0, len(c.Option))
	for k := range c.Option {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "Name=%s DisplayName=%s Description=%q UserName=%q\n", c.Name, c.DisplayName, c.Description, c.UserName)
	fmt.Fprintf(&b, "  exec: %s %s\n  env: %v\n", c.Executable, strings.Join(c.Arguments, " "), c.EnvVars)
	for _, k := range keys {
		v := c.Option[k]
		switch {
		case k == "Password":
			v = "<redacted>"
		case k == "SystemdScript":
			v = "\n" + fmt.Sprint(v)
		}
		fmt.Fprintf(&b, "  option %s = %v\n", k, v)
	}
	return b.String()
}

func baseChecks(t *testing.T, c kservice.Config, home string) {
	t.Helper()
	if c.Name != "ferry" || c.DisplayName != "Ferry" || c.Description != "Ferry carries your coding agents to chat" {
		t.Errorf("identity = %q %q %q", c.Name, c.DisplayName, c.Description)
	}
	if c.Executable != testExe || !slices.Equal(c.Arguments, []string{"service", "run"}) {
		t.Errorf("command = %q %v, want %q [service run]", c.Executable, c.Arguments, testExe)
	}
	if len(c.EnvVars) != 1 || c.EnvVars["FERRY_HOME"] != home {
		t.Errorf("EnvVars = %v, want only FERRY_HOME=%s", c.EnvVars, home)
	}
}

func TestConfigDarwin(t *testing.T) {
	home := "/Users/alice/.ferry"
	c := BuildConfig("darwin", Params{Executable: testExe, Home: home, User: testUser, Password: "pw"})
	t.Logf("darwin:\n%s", summary(c))
	baseChecks(t, c, home)
	if c.UserName != "" {
		t.Errorf("UserName = %q; a LaunchAgent already runs as its owner", c.UserName)
	}
	for k, v := range map[string]any{"UserService": true, "RunAtLoad": true, "KeepAlive": true, "LogDirectory": "/Users/alice/.ferry/logs"} {
		if c.Option[k] != v {
			t.Errorf("Option[%s] = %v, want %v", k, c.Option[k], v)
		}
	}
	if _, ok := c.Option["Password"]; ok {
		t.Error("darwin definition carries a password")
	}
}

func TestConfigLinux(t *testing.T) {
	home := "/home/alice/.ferry"
	c := BuildConfig("linux", Params{Executable: testExe, Home: home, User: "alice", Password: "pw"})
	t.Logf("linux:\n%s", summary(c))
	baseChecks(t, c, home)
	if c.UserName != "" {
		t.Errorf("UserName = %q; User= in a user unit fails to start", c.UserName)
	}
	if c.Option["UserService"] != true || c.Option["Restart"] != "always" {
		t.Errorf("Option = %v, want UserService=true Restart=always", c.Option)
	}
	unit, _ := c.Option["SystemdScript"].(string)
	if !strings.Contains(unit, "WantedBy=default.target") || strings.Contains(unit, "multi-user.target") {
		t.Errorf("user unit must be wanted by default.target:\n%s", unit)
	}
	for _, key := range []string{"{{Path | cmdEscape}}", "{{range Arguments}}", "{{range EnvVars}}", "{{if Restart}}Restart={{Restart}}"} {
		if !strings.Contains(unit, key) {
			t.Errorf("user unit lacks %s", key)
		}
	}
	if strings.Contains(unit, "User=") {
		t.Error("user unit sets User=")
	}
	if _, ok := c.Option["Password"]; ok {
		t.Error("linux definition carries a password")
	}
}

func TestConfigWindows(t *testing.T) {
	home := `C:\Users\alice\.ferry`
	c := BuildConfig("windows", Params{Executable: testExe, Home: home, User: testUser, Password: "pw"})
	t.Logf("windows:\n%s", summary(c))
	baseChecks(t, c, home)
	if c.UserName != testUser {
		t.Errorf("UserName = %q, want the installing account %q (never LocalSystem)", c.UserName, testUser)
	}
	for k, v := range map[string]any{"Password": "pw", "StartType": "automatic", "DelayedAutoStart": true, "OnFailure": "restart"} {
		if c.Option[k] != v {
			t.Errorf("Option[%s] = %v, want %v", k, c.Option[k], v)
		}
	}
	if _, ok := c.Option["UserService"]; ok {
		t.Error("UserService has no effect on Windows")
	}
}

func TestInstallRegistersAndStarts(t *testing.T) {
	r := newRig(t, "darwin")
	wantExit(t, Install(r.env), 0)
	wantCalls(t, r.fake, "status", "install", "start")
	if !r.fake.installed || !r.fake.running {
		t.Fatalf("installed=%v running=%v", r.fake.installed, r.fake.running)
	}
	if want := r.text("cli.service_installed", map[string]any{"Name": "ferry"}); strings.TrimSpace(r.out.String()) != want {
		t.Fatalf("stdout = %q, want %q", r.out.String(), want)
	}
	if info, err := os.Stat(filepath.Join(r.env.Home, "logs")); err != nil || !info.IsDir() {
		t.Fatalf("logs directory not created: %v", err)
	}
	if got := r.opened[len(r.opened)-1]; got.EnvVars["FERRY_HOME"] != r.env.Home || got.Executable != testExe {
		t.Fatalf("installed definition = %+v", got)
	}
}

func TestInstallAlreadyInstalled(t *testing.T) {
	for name, fake := range map[string]*fakeManager{
		"running": {installed: true, running: true},
		"stopped": {installed: true},
		"failed":  {installed: true, statusErr: errors.New("service in failed state")},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "linux")
			r.fake = fake
			wantExit(t, Install(r.env), 1)
			wantCalls(t, r.fake, "status")
			if want := r.text("cli.service_already_installed", map[string]any{"Name": "ferry"}); !strings.Contains(r.err.String(), want) {
				t.Fatalf("stderr = %q, want %q", r.err.String(), want)
			}
		})
	}
}

func TestInstallNeedsInit(t *testing.T) {
	r := newRig(t, "darwin")
	if err := os.Remove(config.Path(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	wantExit(t, Install(r.env), 1)
	if len(r.opened) != 0 || len(r.fake.calls) != 0 {
		t.Fatalf("touched the service manager without a config: opened %d, calls %v", len(r.opened), r.fake.calls)
	}
	if want := r.text("cli.need_init", nil); strings.TrimSpace(r.err.String()) != want {
		t.Fatalf("stderr = %q, want %q", r.err.String(), want)
	}
}

func TestInstallUnsupported(t *testing.T) {
	r := newRig(t, "linux")
	r.env.Open = func(*kservice.Config) (Manager, error) {
		return nil, fmt.Errorf("%w: no systemd user instance", ErrUnsupported)
	}
	wantExit(t, Install(r.env), 2)
	wantExit(t, Status(r.env), 2)
	if !strings.Contains(r.err.String(), "no systemd user instance") {
		t.Fatalf("stderr = %q, want the reason", r.err.String())
	}
}

func TestUninstallStopsAndRemoves(t *testing.T) {
	r := newRig(t, "darwin")
	r.fake.installed, r.fake.running = true, true
	wantExit(t, Uninstall(r.env), 0)
	wantCalls(t, r.fake, "status", "stop", "uninstall")
	if r.fake.installed || r.fake.running {
		t.Fatalf("installed=%v running=%v", r.fake.installed, r.fake.running)
	}
	if want := r.text("cli.service_uninstalled", map[string]any{"Name": "ferry"}); strings.TrimSpace(r.out.String()) != want {
		t.Fatalf("stdout = %q, want %q", r.out.String(), want)
	}

	// Stopping a service that is not running is not an error.
	r = newRig(t, "linux")
	r.fake.installed, r.fake.stopErr = true, errors.New("not running")
	wantExit(t, Uninstall(r.env), 0)
	wantCalls(t, r.fake, "status", "stop", "uninstall")

	// A running service that refuses to stop is.
	r = newRig(t, "linux")
	r.fake.installed, r.fake.running, r.fake.stopErr = true, true, errors.New("stuck")
	if err := Uninstall(r.env); err == nil || !r.fake.installed {
		t.Fatalf("err = %v installed = %v, want the stop error and the service kept", err, r.fake.installed)
	}
}

func TestUninstallNotInstalled(t *testing.T) {
	r := newRig(t, "darwin")
	wantExit(t, Uninstall(r.env), 1)
	wantCalls(t, r.fake, "status")
	if want := r.statusText("not_installed"); strings.TrimSpace(r.out.String()) != want {
		t.Fatalf("stdout = %q, want %q", r.out.String(), want)
	}
}

func TestStatusStates(t *testing.T) {
	for _, tc := range []struct {
		state string
		fake  fakeManager
	}{
		{"running", fakeManager{installed: true, running: true}},
		{"stopped", fakeManager{installed: true}},
		{"not_installed", fakeManager{}},
		{"unknown", fakeManager{installed: true, statusErr: errors.New("service in failed state")}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			r := newRig(t, "linux")
			*r.fake = tc.fake
			wantExit(t, Status(r.env), 0)
			if want := r.statusText(tc.state); strings.TrimSpace(r.out.String()) != want {
				t.Fatalf("stdout = %q, want %q", r.out.String(), want)
			}
		})
	}
	// A status value kardianos does not name is unknown too.
	if got := stateOf(kservice.Status(9), nil); got != "unknown" {
		t.Fatalf("stateOf(9) = %s", got)
	}
}

func TestWindowsPasswordNotStored(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	r := newRig(t, "windows")
	prompts := 0
	r.env.ReadPassword = func() (string, error) { prompts++; return secret, nil }
	wantExit(t, Install(r.env), 0)
	if prompts != 1 {
		t.Fatalf("password read %d times, want 1", prompts)
	}
	if want := r.text("cli.service_password", map[string]any{"User": testUser}); !strings.Contains(r.err.String(), want) {
		t.Fatalf("stderr = %q, want the password prompt", r.err.String())
	}

	// The password reaches the service manager with the account...
	last := r.opened[len(r.opened)-1]
	if last.UserName != testUser || last.Option["Password"] != secret {
		t.Fatalf("installed as %q with password %v", last.UserName, last.Option["Password"] == secret)
	}
	// ...and goes nowhere else: not into the arguments or environment, not
	// onto the screen, not into any file under FERRY_HOME.
	if strings.Contains(strings.Join(last.Arguments, " "), secret) || strings.Contains(fmt.Sprint(last.EnvVars), secret) {
		t.Fatal("password in the service command line or environment")
	}
	if strings.Contains(r.out.String()+r.err.String(), secret) {
		t.Fatal("password printed")
	}
	err := filepath.WalkDir(r.env.Home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("password stored in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The manager used before the prompt had no password to carry.
	if _, ok := r.opened[0].Option["Password"]; ok {
		t.Fatal("password set before it was read")
	}
}

func TestWindowsNeedsAdmin(t *testing.T) {
	r := newRig(t, "windows")
	r.env.Elevated = func() bool { return false }
	r.env.ReadPassword = func() (string, error) { t.Fatal("asked for a password without admin rights"); return "", nil }
	wantExit(t, Install(r.env), 1)
	wantCalls(t, r.fake, "status")
	want := r.text("cli.service_needs_admin", nil)
	if !strings.Contains(r.err.String(), want) {
		t.Fatalf("stderr = %q, want %q", r.err.String(), want)
	}

	// An elevation check that passes but a service manager that still says
	// ERROR_ACCESS_DENIED gets the same hint.
	r = newRig(t, "windows")
	r.fake.installErr = fmt.Errorf("open manager: %w", syscall.Errno(5))
	wantExit(t, Install(r.env), 1)
	if !strings.Contains(r.err.String(), want) {
		t.Fatalf("stderr = %q, want %q", r.err.String(), want)
	}

	r = newRig(t, "windows")
	r.fake.installed = true
	r.env.Elevated = func() bool { return false }
	wantExit(t, Uninstall(r.env), 1)
	if !r.fake.installed || !strings.Contains(r.err.String(), want) {
		t.Fatalf("installed=%v stderr=%q", r.fake.installed, r.err.String())
	}
}

func TestLogonRightHint(t *testing.T) {
	r := newRig(t, "windows")
	r.fake.startErr = fmt.Errorf("start: %w", syscall.Errno(1069))
	wantExit(t, Install(r.env), 1)
	if want := r.text("cli.service_logon_right", map[string]any{"User": testUser}); !strings.Contains(r.err.String(), want) {
		t.Fatalf("stderr = %q, want %q", r.err.String(), want)
	}
	// The registration is rolled back so a retry after granting the right
	// does not stop at "already installed".
	wantCalls(t, r.fake, "status", "install", "start", "uninstall")
	if r.fake.installed {
		t.Fatal("service left installed after a failed start")
	}
	if strings.Contains(r.out.String(), r.text("cli.service_installed", map[string]any{"Name": "ferry"})) {
		t.Fatal("reported installed")
	}
	if IsLogonRightMissing(errors.New("1069")) || IsLogonRightMissing(syscall.Errno(5)) || !IsLogonRightMissing(syscall.Errno(1069)) {
		t.Fatal("IsLogonRightMissing misclassifies")
	}
}

func TestLingerHintOnLinux(t *testing.T) {
	r := newRig(t, "linux")
	r.env.CurrentUser = func() (string, error) { return "alice", nil }
	wantExit(t, Install(r.env), 0)
	want := r.text("cli.service_linger", map[string]any{"User": "alice"})
	if !strings.Contains(r.out.String(), want) || !strings.Contains(want, "loginctl enable-linger alice") {
		t.Fatalf("stdout = %q, want %q", r.out.String(), want)
	}
	if len(r.opened) != 1 || r.opened[0].UserName != "" {
		t.Fatalf("linux opened %d definitions, user %q", len(r.opened), r.opened[0].UserName)
	}

	for _, goos := range []string{"darwin", "windows"} {
		r := newRig(t, goos)
		wantExit(t, Install(r.env), 0)
		if strings.Contains(r.out.String(), "enable-linger") {
			t.Fatalf("%s printed the linger hint", goos)
		}
	}
}

func TestProgramStopCancelsRun(t *testing.T) {
	exited := make(chan error, 1)
	p := &Program{
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Exit: func(err error) { exited <- err },
	}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("Exit called on a requested stop: %v", err)
	default:
	}
}

func TestProgramExitsWhenRunFails(t *testing.T) {
	boom := errors.New("already running")
	exited := make(chan error, 1)
	p := &Program{
		Run:  func(context.Context) error { return boom },
		Exit: func(err error) { exited <- err },
	}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if !errors.Is(err, boom) {
			t.Fatalf("Exit(%v), want %v", err, boom)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exit not called after Run failed")
	}
}

func TestProgramStopGivesUp(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	release := make(chan struct{})
	defer close(release)
	p := &Program{
		Run:   func(context.Context) error { <-release; return nil },
		Clock: clk,
	}
	_ = p.Start(nil)
	stopped := make(chan struct{})
	go func() { _ = p.Stop(nil); close(stopped) }()
	for {
		select {
		case <-stopped:
			return
		case <-time.After(10 * time.Millisecond):
			clk.Advance(stopWait)
		}
	}
}
