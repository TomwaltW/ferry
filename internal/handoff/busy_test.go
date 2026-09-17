package handoff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// busyMarker goes into the command line of the stand-in terminal process, so
// the pattern matches it and nothing else on the machine.
const busyMarker = "ferry-handoff-busy-marker"

// startMarked runs a process that sits in dir with the marker in its command
// line, standing in for an agent someone left running in a terminal.
//
// The script is two commands on purpose: a shell handed a single one execs it
// and the marker disappears along with the shell's own argv.
func startMarked(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", ": "+busyMarker+"; sleep 30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the stand-in terminal process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func busyModule(t *testing.T, dir string) *handoff {
	t.Helper()
	return newHandoff(t, Options{
		Roots:    []string{dir},
		Agents:   demoAgents(),
		Profiles: []config.HandoffProfile{{Agent: "demo", ProcessMatch: busyMarker}},
	})
}

// waitBusy polls the process table: ps and /proc both need a moment to catch up
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
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("the process table is only read on Linux and macOS, not on %s", runtime.GOOS)
	}
	dir := realDir(t, t.TempDir())
	startMarked(t, dir)

	h := busyModule(t, dir)
	// The stand-in is a child of the test binary, which plays the daemon, so
	// the daemon is moved to an unrelated process for this test.
	sibling := exec.Command("sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatalf("start the stand-in daemon process: %v", err)
	}
	t.Cleanup(func() {
		_ = sibling.Process.Kill()
		_ = sibling.Wait()
	})
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
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("the process table is only read on Linux and macOS, not on %s", runtime.GOOS)
	}
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
