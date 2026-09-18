package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWindowsProcessTable reads this very process out of the table: it has to
// be there with its parent, its command line and its working directory.
func TestWindowsProcessTable(t *testing.T) {
	procs, err := platformProcesses(t.Context())
	if err != nil {
		t.Fatalf("platformProcesses: %v", err)
	}
	self := os.Getpid()
	var me *procInfo
	for i := range procs {
		if procs[i].pid == self {
			me = &procs[i]
			break
		}
	}
	if me == nil {
		t.Fatalf("pid %d is not in a table of %d processes", self, len(procs))
	}
	if me.ppid != os.Getppid() {
		t.Errorf("ppid = %d, want %d", me.ppid, os.Getppid())
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if base := strings.TrimSuffix(filepath.Base(exe), ".exe"); !strings.Contains(strings.ToLower(me.cmdline), strings.ToLower(base)) {
		t.Errorf("command line = %q, want it to name %s", me.cmdline, base)
	}

	dir, err := platformProcessCwd(t.Context(), self)
	if err != nil {
		t.Fatalf("platformProcessCwd: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	got, err := normalize(dir)
	if err != nil {
		t.Fatalf("normalize(%q): %v", dir, err)
	}
	want, err := normalize(wd)
	if err != nil {
		t.Fatalf("normalize(%q): %v", wd, err)
	}
	if !sameDir("windows", got, want) {
		t.Errorf("working directory = %q (read as %q), want %q", got, dir, want)
	}

	// A child in another directory reports that directory, not this one.
	other := t.TempDir()
	child := startStandIn(t, other)
	want, err = normalize(other)
	if err != nil {
		t.Fatalf("normalize(%q): %v", other, err)
	}
	// The child needs a moment to set up its parameters after it starts.
	var last error
	for range 100 {
		dir, last = platformProcessCwd(t.Context(), child.Process.Pid)
		if last == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("platformProcessCwd(child): %v", last)
	}
	if got, _ := normalize(dir); !sameDir("windows", got, want) {
		t.Errorf("child working directory = %q, want %q", dir, want)
	}
}
