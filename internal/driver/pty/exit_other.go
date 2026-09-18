//go:build !unix

package pty

import "os"

// exitCode is the status ferry run exits with. Off Unix there are no signals to
// account for, so the program's own code is all there is.
func exitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
