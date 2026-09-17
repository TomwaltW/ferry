//go:build !windows

package handoff

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Exec runs the resume command in dir. On Unix it replaces the current process,
// so the agent inherits the terminal exactly as if it had been typed there.
func Exec(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("handoff: nothing to execute")
	}
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("handoff: find %s: %w", args[0], err)
	}
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("handoff: enter %s: %w", dir, err)
		}
	}
	return syscall.Exec(bin, args, os.Environ())
}
