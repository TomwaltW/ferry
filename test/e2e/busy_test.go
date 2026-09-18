package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Starting the stand-in terminal agent takes two hops. The daemon runs inside
// this test process and Busy rightly ignores the daemon's own descendants —
// they are agents Ferry started — so an agent this process started directly
// would never count as a terminal. Instead the test binary is started again as
// a launcher: it starts the agent, prints its pid and exits, leaving the agent
// an orphan that belongs to nobody here, the way a terminal's would.
const (
	launchBinEnv  = "FERRY_E2E_LAUNCH_BIN"
	launchDirEnv  = "FERRY_E2E_LAUNCH_DIR"
	launchHomeEnv = "FERRY_E2E_LAUNCH_HOME"
)

func init() {
	bin := os.Getenv(launchBinEnv)
	if bin == "" {
		return
	}
	cmd := exec.Command(bin)
	cmd.Dir = os.Getenv(launchDirEnv)
	cmd.Env = append(os.Environ(), "FAKEAGENT_HOME="+os.Getenv(launchHomeEnv))
	// The agent inherits this process's standard input: the read end of a pipe
	// whose write end the test holds, so the agent waits on it for as long as
	// the test wants it alive.
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "launch:", err)
		os.Exit(1)
	}
	fmt.Println(cmd.Process.Pid)
	os.Exit(0)
}

// startTerminalAgent leaves a ferry-fakeagent process running in dir that is
// not a descendant of this process, and stops it when the test ends.
func startTerminalAgent(t *testing.T, dir string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	keep, hold, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	launcher := exec.Command(self)
	launcher.Env = append(os.Environ(),
		launchBinEnv+"="+agentBin,
		launchDirEnv+"="+dir,
		launchHomeEnv+"="+t.TempDir(),
	)
	launcher.Stdin = keep
	launcher.Stderr = testWriter{t}
	out, err := launcher.Output()
	keep.Close()
	if err != nil {
		hold.Close()
		t.Fatalf("launch the terminal agent: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		hold.Close()
		t.Fatalf("the launcher printed %q, want a pid", out)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		hold.Close()
	})
}

// writeTerminalSession puts a session into the fake agent's own store, as if
// it had been started from a terminal: the agent lists it, Ferry does not own it.
func (r *rig) writeTerminalSession(id, cwd, title string) {
	r.t.Helper()
	dir := filepath.Join(r.home, "agent", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatalf("agent sessions: %v", err)
	}
	rec := map[string]any{
		"id":         id,
		"cwd":        cwd,
		"title":      title,
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"history":    []any{},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		r.t.Fatalf("encode the session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o644); err != nil {
		r.t.Fatalf("write the session: %v", err)
	}
}

func withHandoff() rigOption { return func(r *rig) { r.handoff = true } }

// B6 (spec-p2 B4): a terminal still running the agent in the session's
// directory keeps /use from taking the session over; /use <n> force does.
func TestBusyRefused(t *testing.T) {
	r := newRig(t, withHandoff())
	// Discovery starts the agent to list its sessions, which takes longer
	// than the usual settle time on a loaded machine.
	r.patience = subprocWait

	demo := filepath.Join(r.ws, "demo")
	const title = "busy-demo"
	r.writeTerminalSession("term-busy", demo, title)
	startTerminalAgent(t, demo)

	mark := r.markOwner()
	r.send("/ls")
	item := r.tr("session.list_terminal_item", map[string]any{"Slot": 1, "Agent": agentName, "Title": title, "When": ""})
	// The time is the one part of the line the test does not control.
	r.waitOwnerTextFrom(mark, strings.SplitN(item, " · ", 3)[0]+" · "+title)

	busy := r.tr("handoff.busy", map[string]any{"Slot": 1, "Agent": agentName, "Cwd": demo})
	adopted := r.tr("handoff.adopted", map[string]any{"Title": title})

	mark = r.markOwner()
	r.send("/use 1")
	r.waitOwnerTextFrom(mark, busy)
	r.refuteFrom(ownerID, mark, adopted, "the terminal still holds the session")

	mark = r.markOwner()
	r.send("/use 1 force")
	r.waitOwnerTextFrom(mark, adopted)
}
