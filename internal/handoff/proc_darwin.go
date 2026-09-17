package handoff

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// platformProcesses shells out to ps: macOS has no /proc, and the alternative
// is cgo against libproc, which the build does not allow.
func platformProcesses(ctx context.Context) ([]procInfo, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("handoff: ps: %w", err)
	}
	var procs []procInfo
	for _, line := range strings.Split(string(out), "\n") {
		proc, ok := parsePSLine(line)
		if !ok {
			continue
		}
		procs = append(procs, proc)
	}
	return procs, nil
}

// parsePSLine reads "<pid> <ppid> <command…>". The command keeps its spaces, so
// only the first two fields are split off.
func parsePSLine(line string) (procInfo, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return procInfo{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return procInfo{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procInfo{}, false
	}
	rest := strings.TrimLeft(line, " ")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, fields[0]), " ")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, fields[1]), " ")
	return procInfo{pid: pid, ppid: ppid, cmdline: rest}, true
}

// platformProcessCwd asks lsof for the one descriptor that is the working
// directory. -Fn makes it print "n<path>" lines, which is stable output.
func platformProcessCwd(ctx context.Context, pid int) (string, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("handoff: lsof %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if dir, ok := strings.CutPrefix(line, "n"); ok && dir != "" {
			return dir, nil
		}
	}
	return "", fmt.Errorf("handoff: lsof %d reported no working directory", pid)
}
