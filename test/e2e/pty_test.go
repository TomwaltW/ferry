package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// lineechoBin is the Windows stand-in for the shell loop of B9. It is built
// here on first use rather than in TestMain: only TestPTYRun needs it, and only
// on Windows.
var (
	lineechoOnce sync.Once
	lineechoBin  string
	lineechoErr  error
)

func buildLineecho(t *testing.T) string {
	t.Helper()
	lineechoOnce.Do(func() {
		// Next to the other binaries, so TestMain cleans it up with them.
		lineechoBin = filepath.Join(filepath.Dir(ferryBin), "lineecho.exe")
		build := exec.Command("go", "build", "-o", lineechoBin, "./test/e2e/testdata/lineecho")
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			lineechoErr = fmt.Errorf("%v\n%s", err, out)
		}
	})
	if lineechoErr != nil {
		t.Fatalf("build lineecho: %v", lineechoErr)
	}
	return lineechoBin
}

// ptyProgram is the program B9 wraps and the name it registers under: the
// shell loop of spec-p3 on Unix, and on Windows the Go fixture that does the
// same, since Windows has no sh.
func ptyProgram(t *testing.T) (name string, args []string) {
	if runtime.GOOS == "windows" {
		return "lineecho", []string{"--name", "lineecho", "--", buildLineecho(t)}
	}
	return "sh", []string{"--", "sh", "-c", "while read l; do echo got:$l; done"}
}

// B9: ferry run wraps a program without ACP in a pseudo terminal, registers it
// as a session, and types what WeChat sends into it.
func TestPTYRun(t *testing.T) {
	r := newRig(t)
	r.openWindow()
	// Injection waits for the default 1.5 s of quiet before typing and again
	// before reporting, so this test needs more than the usual settle time.
	r.patience = subprocWait

	name, args := ptyProgram(t)
	cmd := exec.Command(ferryBin, append([]string{"run"}, args...)...)
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

	r.waitOwnerText(r.tr("pty.attached", map[string]any{"Name": name, "Slot": 1}))
	r.waitOwnerText(r.tr("pty.no_approval", nil))

	mark := r.markOwner()
	r.send("hello")
	r.waitOwnerTextFrom(mark, "got:hello")
}
