package handoff

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// ancestorHops bounds the walk up the parent chain, so a process table that
// contradicts itself cannot spin forever.
const ancestorHops = 64

// procInfo is one row of the process table: enough to match the command line
// and to tell Ferry's own children apart from a terminal's.
type procInfo struct {
	pid     int
	ppid    int
	cmdline string
}

// Busy reports whether a terminal still holds the session: a process whose
// command line matches the agent's pattern and whose working directory is the
// session's. Agents Ferry itself started are skipped. Linux, macOS and Windows
// can be asked; any other platform answers fsp.ErrUnsupported.
func (h *handoff) Busy(ctx context.Context, ts ports.TerminalSession) (bool, error) {
	// A service-backed session is meant to be used from both sides at once.
	if ts.Shared {
		return false, nil
	}
	p, ok := h.profile(ts.Agent)
	if !ok || p.ProcessMatch == "" {
		return false, fmt.Errorf("handoff: no process pattern for %s: %w", ts.Agent, fsp.ErrUnsupported)
	}
	re, err := regexp.Compile(p.ProcessMatch)
	if err != nil {
		return false, fmt.Errorf("handoff: process pattern of %s: %w", ts.Agent, err)
	}
	cwd, err := normalize(ts.Cwd)
	if err != nil {
		return false, fmt.Errorf("handoff: session directory %s: %w", ts.Cwd, err)
	}

	procs, err := h.listProcs(ctx)
	if err != nil {
		return false, fmt.Errorf("handoff: read the process table: %w", err)
	}
	parents := make(map[int]int, len(procs))
	for _, proc := range procs {
		parents[proc.pid] = proc.ppid
	}

	for _, proc := range procs {
		if proc.pid == h.selfPID || descendantOf(parents, proc.pid, h.selfPID) {
			continue
		}
		if !re.MatchString(proc.cmdline) {
			continue
		}
		dir, err := h.procCwd(ctx, proc.pid)
		if err != nil {
			// A process that ended between the two calls, or one owned by
			// somebody else, simply does not answer.
			h.log.Debug("handoff: process directory", "pid", proc.pid, "error", err)
			continue
		}
		norm, err := normalize(dir)
		if err != nil {
			continue
		}
		if sameDir(runtime.GOOS, norm, cwd) {
			return true, nil
		}
	}
	return false, nil
}

// sameDir compares two normalised directories the way the file system on goos
// does: Windows does not care about case, so C:\Work and c:\work are one place.
func sameDir(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// descendantOf walks the parent chain of pid looking for ancestor.
func descendantOf(parents map[int]int, pid, ancestor int) bool {
	for range ancestorHops {
		parent, ok := parents[pid]
		if !ok || parent <= 0 || parent == pid {
			return false
		}
		if parent == ancestor {
			return true
		}
		pid = parent
	}
	return false
}
