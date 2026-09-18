package handoff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// busyMarker goes into the command line of the stand-in terminal process, so
// the pattern matches it and nothing else on the machine.
const busyMarker = "ferry-handoff-busy-marker"

// standInEnv turns the test binary into a stand-in process: started with it
// set, the binary sits for standInLife and exits before any test runs. The
// stand-ins are the test binary rather than sh and sleep so the same tests run
// on Windows.
const standInEnv = "FERRY_HANDOFF_STAND_IN"

const standInLife = 30 * time.Second

func init() {
	if os.Getenv(standInEnv) != "" {
		time.Sleep(standInLife)
		os.Exit(0)
	}
}

// startStandIn runs the test binary as a stand-in process in dir, with args on
// its command line, and stops it when the test ends.
func startStandIn(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	cmd := exec.Command(self, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), standInEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a stand-in process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// startMarked runs a process that sits in dir with the marker in its command
// line, standing in for an agent someone left running in a terminal.
func startMarked(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	return startStandIn(t, dir, busyMarker)
}

func busyModule(t *testing.T, dir string) *handoff {
	t.Helper()
	return newHandoff(t, Options{
		Roots:    []string{dir},
		Agents:   demoAgents(),
		Profiles: []config.HandoffProfile{{Agent: "demo", ProcessMatch: busyMarker}},
	})
}

// waitBusy polls the process table: ps, /proc and a Toolhelp snapshot all need a moment to catch up
// with a process that has only just started.
func waitBusy(t *testing.T, h *handoff, ts ports.TerminalSession) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy, err := h.Busy(context.Background(), ts)
		if err != nil {
			t.Fatalf("Busy failed: %v", err)
		}
		if busy || time.Now().After(deadline) {
			return busy
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBusyProcessInCwd(t *testing.T) {
	dir := realDir(t, t.TempDir())
	startMarked(t, dir)

	h := busyModule(t, dir)
	// The stand-in is a child of the test binary, which plays the daemon, so
	// the daemon is moved to an unrelated process for this test.
	sibling := startStandIn(t, t.TempDir())
	h.selfPID = sibling.Process.Pid

	if !waitBusy(t, h, ports.TerminalSession{Agent: "demo", Cwd: dir}) {
		t.Errorf("Busy = false, want true while a matching process sits in %s", dir)
	}

	// The same process in a different directory holds nothing.
	other := realDir(t, t.TempDir())
	busy, err := h.Busy(context.Background(), ports.TerminalSession{Agent: "demo", Cwd: other})
	if err != nil {
		t.Fatalf("Busy failed: %v", err)
	}
	if busy {
		t.Errorf("Busy = true for %s, want false: no process works there", other)
	}
}

func TestBusyIgnoresOwnDescendants(t *testing.T) {
	dir := realDir(t, t.TempDir())
	startMarked(t, dir)

	h := busyModule(t, dir)
	if h.selfPID != os.Getpid() {
		t.Fatalf("selfPID = %d, want this process", h.selfPID)
	}
	// Give the table the same moment the positive test gets, so a false here
	// means the descendant was skipped rather than not yet visible.
	time.Sleep(200 * time.Millisecond)

	busy, err := h.Busy(context.Background(), ports.TerminalSession{Agent: "demo", Cwd: dir})
	if err != nil {
		t.Fatalf("Busy failed: %v", err)
	}
	if busy {
		t.Errorf("Busy = true, want false: an agent Ferry itself started is not a terminal")
	}
}

func TestBusySharedSessionIsNeverChecked(t *testing.T) {
	h := busyModule(t, t.TempDir())
	h.listProcs = func(ctx context.Context) ([]procInfo, error) {
		t.Error("the process table was read for a shared session")
		return nil, nil
	}
	busy, err := h.Busy(context.Background(), ports.TerminalSession{Agent: "demo", Cwd: t.TempDir(), Shared: true})
	if err != nil || busy {
		t.Errorf("Busy = %v, %v, want false, nil for a shared session", busy, err)
	}
}

func TestBusyWithoutPatternIsUnsupported(t *testing.T) {
	h := newHandoff(t, Options{Agents: demoAgents()})
	busy, err := h.Busy(context.Background(), ports.TerminalSession{Agent: "demo", Cwd: t.TempDir()})
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("Busy = %v, %v, want fsp.ErrUnsupported so the user is asked to force", busy, err)
	}
}

func TestBusyUnreadableTableIsAnError(t *testing.T) {
	dir := realDir(t, t.TempDir())
	h := busyModule(t, dir)
	h.listProcs = func(ctx context.Context) ([]procInfo, error) {
		return nil, fsp.ErrUnsupported
	}
	busy, err := h.Busy(context.Background(), ports.TerminalSession{Agent: "demo", Cwd: dir})
	if busy || !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("Busy = %v, %v, want false and the platform's own error", busy, err)
	}
}

func TestDescendantOf(t *testing.T) {
	parents := map[int]int{10: 1, 20: 10, 30: 20, 40: 1}
	cases := []struct {
		pid, ancestor int
		want          bool
	}{
		{20, 10, true},
		{30, 10, true},
		{40, 10, false},
		{10, 10, false}, // a process is not its own descendant
	}
	for _, tc := range cases {
		if got := descendantOf(parents, tc.pid, tc.ancestor); got != tc.want {
			t.Errorf("descendantOf(%d, %d) = %v, want %v", tc.pid, tc.ancestor, got, tc.want)
		}
	}

	// A cycle in the table must not spin forever.
	if descendantOf(map[int]int{1: 2, 2: 1}, 1, 99) {
		t.Errorf("descendantOf found an ancestor in a cycle")
	}
}

// TestProcessMatchWindowsCommandLine checks the built-in patterns against the
// ways each platform writes the same program. The patterns are plain regular
// expressions, so both platforms' are checked everywhere.
func TestProcessMatchWindowsCommandLine(t *testing.T) {
	names := map[string]string{
		"kimi":       "kimi",
		"qwen-code":  "qwen",
		"opencode":   "opencode",
		"claude-acp": "claude",
		"codex-acp":  "codex",
	}
	for agent, prog := range names {
		p, ok := findProfile(Profiles, agent)
		if !ok {
			t.Fatalf("no built-in profile for %s", agent)
		}
		if want := processMatch(runtime.GOOS, prog); p.ProcessMatch != want {
			t.Errorf("%s: ProcessMatch = %q, want the %s pattern %q", agent, p.ProcessMatch, runtime.GOOS, want)
		}

		// The Unix pattern is the one P2 shipped, character for character.
		if got, want := processMatch("linux", prog), `(^|/)`+prog+`( |$)`; got != want {
			t.Errorf("%s: the Unix pattern is %q, want it unchanged as %q", agent, got, want)
		}

		win := regexp.MustCompile(processMatch("windows", prog))
		for _, line := range []string{
			`"C:\Program Files\` + prog + `\` + prog + `.exe" acp`,
			`"C:\Program Files\` + prog + `\` + prog + `.exe"`,
			`C:\Users\me\.local\bin\` + prog + `.exe acp`,
			`C:\Users\me\.local\bin\` + strings.ToUpper(prog) + `.EXE`,
			`C:/tools/` + prog + `.exe --acp`,
			prog + ` acp`,
			prog + `.exe`,
		} {
			if !win.MatchString(line) {
				t.Errorf("%s: the Windows pattern does not match %q", agent, line)
			}
		}
		for _, line := range []string{
			`C:\tools\` + prog + `\helper.exe`,
			`C:\tools\` + prog + `-helper.exe`,
			`C:\tools\my` + prog + `.exe`,
			`notepad.exe C:\notes\` + prog + `.txt`,
		} {
			if win.MatchString(line) {
				t.Errorf("%s: the Windows pattern matches %q, which is some other program", agent, line)
			}
		}

		unix := regexp.MustCompile(processMatch("linux", prog))
		for line, want := range map[string]bool{
			"/usr/local/bin/" + prog + " acp":   true,
			prog:                                true,
			"node /usr/local/bin/" + prog:       true,
			"/opt/" + prog + "/helper":          false,
			"/usr/bin/my" + prog:                false,
			"/usr/bin/" + prog + "-helper":      false,
			`C:\Program Files\` + prog + ".exe": false,
		} {
			if got := unix.MatchString(line); got != want {
				t.Errorf("%s: the Unix pattern on %q = %v, want %v", agent, line, got, want)
			}
		}
	}
}

func TestCwdCompareIgnoresCaseOnWindows(t *testing.T) {
	if !sameDir("windows", `C:\Users\Me\Work`, `c:\users\me\WORK`) {
		t.Error("two spellings of one Windows directory compare as different places")
	}
	if sameDir("windows", `C:\Users\Me\Work`, `C:\Users\Me\Work2`) {
		t.Error("two Windows directories compare as the same place")
	}
	if sameDir("linux", "/home/me/Work", "/home/me/work") {
		t.Error("Linux directories that differ in case compare as the same place")
	}
	if sameDir("darwin", "/Users/me/Work", "/Users/me/work") {
		t.Error("macOS directories that differ in case compare as the same place: the pre-P4 comparison was exact")
	}
	if !sameDir("linux", "/home/me/work", "/home/me/work") {
		t.Error("a Linux directory does not compare as itself")
	}
}
