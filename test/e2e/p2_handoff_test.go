package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// The terminal handoff of spec-p2 2.2 B1–B3. A session that was started in a
// terminal is not a process here: handoff.Discover starts the agent once and
// asks it what it has, so a preset session is a file under the fake agent's own
// home, which the rig points at <home>/agent.

// terminalTurn is one entry of the history a preset session carries.
// session/load replays those turns, which is what B2 pins down: they are a
// transcript of what already happened, so none of it may reach WeChat.
type terminalTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// writeTerminalSessionWith is writeTerminalSession with a conversation already
// in the session. The two sit side by side rather than as one helper because
// busy_test.go, like every file the round did not create, is read only.
func (r *rig) writeTerminalSessionWith(id, cwd, title string, history []terminalTurn) {
	r.t.Helper()
	dir := filepath.Join(r.home, "agent", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatalf("agent sessions: %v", err)
	}
	if history == nil {
		history = []terminalTurn{}
	}
	rec := map[string]any{
		"id":         id,
		"cwd":        cwd,
		"title":      title,
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"history":    history,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		r.t.Fatalf("encode the session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o644); err != nil {
		r.t.Fatalf("write the session: %v", err)
	}
}

// terminalItem renders session.list_terminal_item for slot 1 and splits it on
// the separator, so a test can match the part it controls (the agent and the
// title) and the terminal marker separately from the time, which it does not.
func (r *rig) terminalItem(title string) (prefix, marker string) {
	r.t.Helper()
	item := r.tr("session.list_terminal_item", map[string]any{
		"Slot": 1, "Agent": agentName, "Title": title, "When": "",
	})
	parts := strings.SplitN(item, " · ", 3)
	if len(parts) != 3 {
		r.t.Fatalf("session.list_terminal_item no longer has three fields, update the e2e split:\n%s", item)
	}
	// With an empty When the last field is the marker alone.
	if parts[2] == "" {
		r.t.Fatalf("session.list_terminal_item carries no terminal marker:\n%s", item)
	}
	return parts[0] + " · " + parts[1], parts[2]
}

// adoptTerminal lists the terminal sessions and takes the one at number 1 over.
// Nothing is holding it, so /use needs no force.
func (r *rig) adoptTerminal(title string) {
	r.t.Helper()
	prefix, _ := r.terminalItem(title)

	mark := r.markOwner()
	r.send("/ls")
	r.waitOwnerTextFrom(mark, prefix)

	mark = r.markOwner()
	r.send("/use 1")
	r.waitOwnerTextFrom(mark, r.tr("handoff.adopted", map[string]any{"Title": title}))
}

// findSession looks for the Ferry session that carries an agent session ID.
func (r *rig) findSession(agentSessionID string) (fsp.Session, bool) {
	r.t.Helper()
	rows, err := r.store.ListSessions(context.Background(), r.ep)
	if err != nil {
		r.t.Fatalf("list sessions: %v", err)
	}
	for _, rec := range rows {
		if rec.AgentSessionID == agentSessionID {
			return rec, true
		}
	}
	return fsp.Session{}, false
}

func (r *rig) sessionFor(agentSessionID string) fsp.Session {
	r.t.Helper()
	rec, ok := r.findSession(agentSessionID)
	if !ok {
		r.t.Fatalf("no Ferry session holds agent session %q: the adoption started a new one instead of loading it", agentSessionID)
	}
	return rec
}

// runFerryBack runs `ferry back` against the rig's home, with the directory the
// fake agent was built into on PATH: the built-in handoff profile resumes the
// agent as plain "ferry-fakeagent", so --exec has to be able to find it the way
// a terminal would. It is separate from runFerry, which belongs to p4_test.go.
func (r *rig) runFerryBack(args ...string) (string, string, int) {
	r.t.Helper()
	cmd := exec.Command(ferryBin, append([]string{"back"}, args...)...)
	cmd.Env = append(os.Environ(),
		"FERRY_HOME="+r.home,
		"PATH="+filepath.Dir(agentBin)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		r.t.Fatalf("run ferry back %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

// B1 (spec-p2 2.2 B1): a session the agent has in the workspace shows up in
// /ls as a terminal entry, under the title the agent gave it.
func TestTerminalDiscovery(t *testing.T) {
	r := newRig(t, withHandoff())
	// Discovery starts the agent to list its sessions, which takes longer than
	// the usual settle time on a loaded machine.
	r.patience = subprocWait

	const title = "discovery-demo"
	r.writeTerminalSessionWith("term-discovery", filepath.Join(r.ws, "demo"), title, nil)

	prefix, marker := r.terminalItem(title)
	mark := r.markOwner()
	r.send("/ls")
	// The time is the one part of the line the test does not control.
	line := r.waitOwnerTextFrom(mark, prefix)
	if !strings.Contains(line, marker) {
		t.Errorf("the entry is not marked as a terminal session (want %q):\n%s", marker, line)
	}
}

// B2 (spec-p2 2.2 B2): /use takes the terminal session over through
// session/load — the agent session ID survives — the conversation continues,
// and the history the load replays never reaches WeChat.
func TestAdoptAndContinue(t *testing.T) {
	r := newRig(t, withHandoff())
	r.patience = subprocWait

	const (
		id     = "term-adopt"
		title  = "adopt-demo"
		replay = "b2-replay-must-not-leak"
		pong   = "b2pong"
	)
	r.writeTerminalSessionWith(id, filepath.Join(r.ws, "demo"), title, []terminalTurn{
		{Role: "user", Text: replay + "-asked"},
		{Role: "agent", Text: replay + "-answered"},
	})

	r.adoptTerminal(title)

	// Loaded, not started afresh: session/new would have minted another ID.
	rec := r.sessionFor(id)
	if rec.Origin != fsp.OriginTerminal {
		t.Errorf("the adopted session has origin %q, want %q", rec.Origin, fsp.OriginTerminal)
	}

	// The replay happens inside the adoption, before the receipt, so by now a
	// leak would already be on screen: check the whole conversation.
	r.refuteFrom(ownerID, 0, replay, "the history session/load replayed reached WeChat")

	mark := r.markOwner()
	r.send("echo " + pong)
	r.waitOwnerTextFrom(mark, pong)
}

// B3 (spec-p2 2.2 B3): ferry back prints the command that resumes the session
// in a terminal and parks the Ferry side; --exec runs that command instead of
// leaving it to be typed.
//
// It is the built command, not an in-process call: handoff.Exec replaces the
// running process on Unix (syscall.Exec), so --exec can only be exercised in a
// child. The printed line is what backTarget produced either way.
func TestBackCommand(t *testing.T) {
	r := newRig(t, withHandoff())
	r.patience = subprocWait

	const (
		id    = "term-back"
		title = "back-demo"
	)
	r.writeTerminalSessionWith(id, filepath.Join(r.ws, "demo"), title, nil)
	r.adoptTerminal(title)

	want := "ferry-fakeagent --resume " + id
	stdout, stderr, code := r.runFerryBack("--slot", "1")
	if code != 0 {
		t.Fatalf("ferry back exited with %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, want) {
		t.Errorf("ferry back did not print %q:\n%s", want, stdout)
	}

	r.waitFor("the session to be parked", func() bool {
		rec, ok := r.findSession(id)
		return ok && rec.State == fsp.StateParked
	})

	// --exec resumes it where the operator would have typed the command.
	stdout, stderr, code = r.runFerryBack("--slot", "1", "--exec")
	if code != 0 {
		t.Fatalf("ferry back --exec exited with %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := "resumed " + id; !strings.Contains(stdout, got) {
		t.Errorf("ferry back --exec did not print %q:\n%s\nstderr:\n%s", got, stdout, stderr)
	}
}
