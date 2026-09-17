package handoff

import (
	"context"
	"sort"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// shellSafe are the characters a shell leaves alone, so a command made only of
// them needs no quoting.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// Local answers ferry back when no daemon is running: it finds the session in
// the database and builds the command that resumes it, without contacting any
// agent. The session is left alone, because nothing is holding it.
//
// It returns fsp.ErrNotFound when there is no such session, and the session
// with a nil command when the agent cannot be resumed.
func Local(ctx context.Context, st ports.Store, h ports.Handoff, slot int) (fsp.Session, []string, error) {
	rec, err := LocalSession(ctx, st, slot)
	if err != nil {
		return fsp.Session{}, nil, err
	}
	cmd, err := h.BackCommand(rec.Agent, rec.AgentSessionID)
	if err != nil {
		// An agent whose resume command has not been verified is reported as
		// such by the caller, not treated as a failure.
		return rec, nil, nil
	}
	return rec, cmd, nil
}

// LocalSession picks the session ferry back is about: the one at slot n of its
// endpoint, or else the foreground session, or else whichever was used last.
func LocalSession(ctx context.Context, st ports.Store, slot int) (fsp.Session, error) {
	sessions, err := st.ActiveSessions(ctx)
	if err != nil {
		return fsp.Session{}, err
	}
	if len(sessions) == 0 {
		return fsp.Session{}, fsp.ErrNotFound
	}

	byID := make(map[fsp.SessionID]fsp.Session, len(sessions))
	var endpoints []fsp.EndpointID
	seen := make(map[string]bool)
	for _, rec := range sessions {
		byID[rec.ID] = rec
		if !seen[rec.Owner.String()] {
			seen[rec.Owner.String()] = true
			endpoints = append(endpoints, rec.Owner)
		}
	}

	for _, ep := range endpoints {
		bindings, err := st.Bindings(ctx, ep)
		if err != nil {
			return fsp.Session{}, err
		}
		for _, b := range bindings {
			rec, ok := byID[b.Session]
			if !ok {
				continue
			}
			if slot > 0 {
				if b.Slot == slot {
					return rec, nil
				}
				continue
			}
			if b.Foreground {
				return rec, nil
			}
		}
	}
	if slot > 0 {
		return fsp.Session{}, fsp.ErrNotFound
	}

	// Nothing is in the foreground: the session used last is the best guess.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})
	return sessions[0], nil
}

// ShellJoin renders a command the way it has to be typed. Anything outside the
// safe set goes into single quotes, with embedded quotes spliced in.
func ShellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	safe := true
	for _, r := range arg {
		if !strings.ContainsRune(shellSafe, r) {
			safe = false
			break
		}
	}
	if safe {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
