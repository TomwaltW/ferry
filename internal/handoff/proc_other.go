//go:build !linux && !darwin && !windows

package handoff

import (
	"context"
	"fmt"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// platformProcesses has no answer outside Linux, macOS and Windows: the
// operator is told Ferry cannot tell and has to pass force.
func platformProcesses(ctx context.Context) ([]procInfo, error) {
	return nil, fmt.Errorf("handoff: no process table on this platform: %w", fsp.ErrUnsupported)
}

func platformProcessCwd(ctx context.Context, pid int) (string, error) {
	return "", fmt.Errorf("handoff: no process table on this platform: %w", fsp.ErrUnsupported)
}
