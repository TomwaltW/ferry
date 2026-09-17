package store

import "os"

// ensureDir creates dir and its parents with owner-only permissions.
func ensureDir(dir string) error { return os.MkdirAll(dir, 0o700) }
