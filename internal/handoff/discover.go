package handoff

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Discover asks every agent that takes part in handoff which sessions it has,
// keeps the ones that live inside the workspace and are recent enough, and
// drops the ones Ferry already tracks. The answer is cached for a minute:
// listing means starting each agent once.
func (h *handoff) Discover(ctx context.Context, ep fsp.EndpointID) ([]ports.TerminalSession, error) {
	if items, ok := h.cached(ep); ok {
		return items, nil
	}
	known, err := h.registered(ctx, ep)
	if err != nil {
		return nil, err
	}

	names := h.participants()
	lists := make([][]ports.TerminalSession, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i] = h.listAgent(ctx, name)
		}()
	}
	wg.Wait()

	cutoff := h.clk.Now().Add(-h.lookback)
	seen := make(map[string]bool)
	out := make([]ports.TerminalSession, 0, 8)
	for _, list := range lists {
		for _, ts := range list {
			key := sessionKey(ts.Agent, ts.AgentSessionID)
			if seen[key] || known[key] {
				continue
			}
			cwd, ok := h.inWorkspace(ts.Cwd)
			if !ok {
				continue
			}
			if !ts.UpdatedAt.IsZero() && ts.UpdatedAt.Before(cutoff) {
				continue
			}
			ts.Cwd = cwd
			seen[key] = true
			out = append(out, ts)
		}
	}
	// Newest first, with the agent and session ID breaking ties so the numbers
	// a user sees do not shuffle between two calls.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].AgentSessionID < out[j].AgentSessionID
	})

	h.remember(ep, out)
	return out, nil
}

func sessionKey(agent, id string) string { return agent + "\x00" + id }

func (h *handoff) cached(ep fsp.EndpointID) ([]ports.TerminalSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.cache[ep.String()]
	if !ok || h.clk.Now().Sub(entry.at) >= cacheTTL {
		return nil, false
	}
	return append([]ports.TerminalSession(nil), entry.items...), true
}

func (h *handoff) remember(ep fsp.EndpointID, items []ports.TerminalSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cache[ep.String()] = cacheEntry{at: h.clk.Now(), items: append([]ports.TerminalSession(nil), items...)}
}

// registered is the set of agent sessions Ferry already owns for the endpoint.
// A closed session is not one of them: its agent session is free again.
func (h *handoff) registered(ctx context.Context, ep fsp.EndpointID) (map[string]bool, error) {
	if h.store == nil {
		return map[string]bool{}, nil
	}
	rows, err := h.store.ListSessions(ctx, ep)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, rec := range rows {
		if rec.State == fsp.StateClosed || rec.AgentSessionID == "" {
			continue
		}
		out[sessionKey(rec.Agent, rec.AgentSessionID)] = true
	}
	return out, nil
}

// participants are the configured agents that take part in discovery: the ones
// that say so, plus the ones that have a handoff profile and say nothing.
func (h *handoff) participants() []string {
	var out []string
	for _, a := range h.agents {
		switch {
		case a.Handoff != nil && !*a.Handoff:
			continue
		case a.Handoff != nil:
			out = append(out, a.Name)
		default:
			if _, ok := h.profile(a.Name); ok {
				out = append(out, a.Name)
			}
		}
	}
	return out
}

// listAgent asks one agent for its sessions. It first calls without a
// directory; an agent that rejects that asks for one, so the call is repeated
// for every workspace root and its immediate subdirectories.
func (h *handoff) listAgent(ctx context.Context, name string) []ports.TerminalSession {
	if h.registry == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()

	spec, err := h.registry.Resolve(ctx, name)
	if err != nil {
		h.log.Debug("handoff: resolve agent", "agent", name, "error", err)
		return nil
	}
	dr, ok := h.driverFor(spec)
	if !ok {
		h.log.Debug("handoff: no driver for agent", "agent", name)
		return nil
	}

	infos, err := dr.List(ctx, spec)
	if err != nil && invalidParams(err) {
		infos, err = h.listPerDir(ctx, dr, spec)
	}
	if err != nil {
		h.log.Debug("handoff: list sessions", "agent", name, "error", err)
		return nil
	}

	shared := spec.Server != nil
	out := make([]ports.TerminalSession, 0, len(infos))
	for _, info := range infos {
		out = append(out, ports.TerminalSession{
			Agent:          name,
			Driver:         dr.Kind(),
			AgentSessionID: info.AgentSessionID,
			Title:          info.Title,
			Cwd:            info.Cwd,
			UpdatedAt:      info.UpdatedAt,
			Shared:         shared,
		})
	}
	return out
}

// listPerDir is the fallback for agents that insist on a working directory.
func (h *handoff) listPerDir(ctx context.Context, dr ports.Driver, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	var (
		out  []ports.AgentSessionInfo
		seen = make(map[string]bool)
		last error
	)
	for _, dir := range h.searchDirs() {
		scoped := spec
		scoped.Cwd = dir
		infos, err := dr.List(ctx, scoped)
		if err != nil {
			last = err
			continue
		}
		for _, info := range infos {
			if seen[info.AgentSessionID] {
				continue
			}
			seen[info.AgentSessionID] = true
			out = append(out, info)
		}
	}
	if len(out) == 0 && last != nil {
		return nil, last
	}
	return out, nil
}

// searchDirs is every workspace root plus its immediate subdirectories.
func (h *handoff) searchDirs() []string {
	var out []string
	for _, root := range h.roots {
		out = append(out, root)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, filepath.Join(root, e.Name()))
			}
		}
	}
	return out
}

// inWorkspace normalises a directory an agent reported and requires it to be a
// workspace root or below one.
func (h *handoff) inWorkspace(dir string) (string, bool) {
	norm, err := normalize(dir)
	if err != nil {
		return "", false
	}
	for _, root := range h.roots {
		if norm == root || strings.HasPrefix(norm, root+string(filepath.Separator)) {
			return norm, true
		}
	}
	return "", false
}

// invalidParams reports the JSON-RPC "Invalid params" an agent answers with
// when it wanted a working directory it did not get.
func invalidParams(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "-32602") ||
		strings.Contains(text, "invalid params") ||
		strings.Contains(text, "invalid_params")
}

// driverFor picks the driver a spec belongs to: a service-backed agent goes to
// the server driver, everything else speaks ACP.
func (h *handoff) driverFor(spec ports.SessionSpec) (ports.Driver, bool) {
	kind := fsp.DriverACP
	if spec.Server != nil {
		kind = fsp.DriverServer
	}
	dr, ok := h.drivers[kind]
	return dr, ok && dr != nil
}
