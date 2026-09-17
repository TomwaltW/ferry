package handoff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// platformProcesses reads /proc. Entries that vanish while the directory is
// being walked are skipped rather than reported: the table is a snapshot of
// something that keeps moving.
func platformProcesses(ctx context.Context) ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		out = append(out, procInfo{
			pid:     pid,
			ppid:    parentPID(e.Name()),
			cmdline: strings.TrimSpace(string(bytes.ReplaceAll(bytes.TrimRight(raw, "\x00"), []byte{0}, []byte{' '}))),
		})
	}
	return out, nil
}

// parentPID reads PPid out of /proc/<pid>/status, which needs no parsing
// around the parenthesised command name that /proc/<pid>/stat carries.
func parentPID(name string) int {
	raw, err := os.ReadFile(filepath.Join("/proc", name, "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "PPid:")
		if !ok {
			continue
		}
		ppid, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return 0
		}
		return ppid
	}
	return 0
}

// platformProcessCwd resolves /proc/<pid>/cwd, which is a symlink to the
// working directory.
func platformProcessCwd(ctx context.Context, pid int) (string, error) {
	dir, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil {
		return "", fmt.Errorf("handoff: working directory of %d: %w", pid, err)
	}
	return dir, nil
}
