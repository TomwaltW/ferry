package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// TestDatabaseIsOwnerOnly pins the file modes of a fresh database. SQLite would
// otherwise create it 0644 under a default umask, which is looser than every
// other file Ferry writes, and this one holds queued message bodies.
func TestDatabaseIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "ferry.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Write something so the write-ahead files exist while the store is open.
	msg := fsp.Outbound{Endpoint: epA, Kind: fsp.OutText, Text: "hello"}
	if _, err := st.AddOutbox(context.Background(), msg, base); err != nil {
		t.Fatalf("add outbox: %v", err)
	}

	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s is %04o, want 0600", filepath.Base(p), got)
		}
	}
}

// TestReopenKeepsOwnerOnly makes sure a database left behind by an older Ferry
// is tightened rather than left at whatever mode it already had.
func TestReopenKeepsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "ferry.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen: %v", err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("reopened database is %04o, want 0600", got)
	}
}
