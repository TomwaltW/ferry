//go:build !windows

package pty

// platformSupported is true off Windows: every pseudo terminal go-pty offers
// there is a plain Unix one.
func platformSupported() bool { return true }

// commandPath leaves the program as given: the pseudo terminal starts it the
// way os/exec does, PATH lookup included.
func commandPath(name string) string { return name }
