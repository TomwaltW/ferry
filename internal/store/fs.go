package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ensureDir creates dir and its parents with owner-only permissions.
func ensureDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// restrict makes the database owner-only. SQLite creates it with the process
// umask -- 0644 on a default macOS or Linux account -- while every other file
// Ferry writes is 0600, and the database is the one holding queued message
// bodies. The -wal and -shm files take the mode of the database, so tightening
// all three here also covers the ones a later open recreates. A missing file is
// not an error: SQLite removes -wal and -shm on a clean close.
func restrict(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("store: chmod %s: %w", p, err)
		}
	}
	return nil
}
