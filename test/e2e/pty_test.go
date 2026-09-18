//go:build linux || darwin

package e2e

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// B9: ferry run wraps a program without ACP in a pseudo terminal, registers it
// as a session, and types what WeChat sends into it.
func TestPTYRun(t *testing.T) {
	r := newRig(t)
	r.openWindow()
	// Injection waits for the default 1.5 s of quiet before typing and again
	// before reporting, so this test needs more than the usual settle time.
	r.patience = subprocWait

	cmd := exec.Command(ferryBin, "run", "--", "sh", "-c", "while read l; do echo got:$l; done")
	cmd.Env = append(os.Environ(), "FERRY_HOME="+r.home)
	cmd.Dir = r.ws
	// The user's keyboard: kept open and silent for the whole test.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = testWriter{t}
	cmd.Stderr = testWriter{t}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ferry run: %v", err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(subprocWait):
			t.Error("ferry run did not exit")
		}
	})

	r.waitOwnerText(r.tr("pty.attached", map[string]any{"Name": "sh", "Slot": 1}))
	r.waitOwnerText(r.tr("pty.no_approval", nil))

	mark := r.markOwner()
	r.send("hello")
	r.waitOwnerTextFrom(mark, "got:hello")
}
