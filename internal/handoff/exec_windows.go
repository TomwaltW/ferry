package handoff

import (
	"fmt"
	"os"
	"os/exec"
)

// Exec runs the resume command in dir. Windows has no way to replace the
// running process, so the agent becomes a child with the same standard
// streams and ferry waits for it.
func Exec(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("handoff: nothing to execute")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
