//go:build unix

package pty

import (
	"os"
	"syscall"
)

// exitCode is the status ferry run exits with. A program that ended normally
// hands over its own code; one that was killed by a signal has none, and the
// shell convention of 128 plus the signal number is used instead.
func exitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
