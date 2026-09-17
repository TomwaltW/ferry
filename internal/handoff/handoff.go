package handoff

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Timings from spec-p2 5.2: a single agent gets ten seconds to answer a list
// request, and a finished discovery is reused for a minute.
const (
	agentTimeout    = 10 * time.Second
	cacheTTL        = time.Minute
	defaultLookback = 7 * 24 * time.Hour
)

type Options struct {
	Roots    []string
	Agents   []config.AgentConfig
	Profiles []config.HandoffProfile // configuration entries, merged over Profiles with configuration winning
	Lookback time.Duration
	Registry ports.Registry
	Drivers  map[fsp.DriverKind]ports.Driver
	Store    ports.Store
	Clock    clock.Clock
	Log      *slog.Logger
}

// handoff finds the sessions an agent started in a terminal and tells the
// caller how to take one over or give it back.
type handoff struct {
	roots    []string
	agents   []config.AgentConfig
	profiles []config.HandoffProfile // the built-in table with the configuration applied on top
	lookback time.Duration
	registry ports.Registry
	drivers  map[fsp.DriverKind]ports.Driver
	store    ports.Store
	clk      clock.Clock
	log      *slog.Logger

	// selfPID is the process whose own descendants never count as a terminal
	// holding a session: they are the agents Ferry itself started.
	selfPID   int
	listProcs func(ctx context.Context) ([]procInfo, error)
	procCwd   func(ctx context.Context, pid int) (string, error)

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at    time.Time
	items []ports.TerminalSession
}

var _ ports.Handoff = (*handoff)(nil)

func New(opts Options) ports.Handoff {
	h := &handoff{
		agents:    opts.Agents,
		profiles:  mergeProfiles(Profiles, opts.Profiles),
		lookback:  opts.Lookback,
		registry:  opts.Registry,
		drivers:   opts.Drivers,
		store:     opts.Store,
		clk:       opts.Clock,
		log:       opts.Log,
		selfPID:   os.Getpid(),
		listProcs: platformProcesses,
		procCwd:   platformProcessCwd,
		cache:     make(map[string]cacheEntry),
	}
	if h.lookback <= 0 {
		h.lookback = defaultLookback
	}
	if h.clk == nil {
		h.clk = clock.Real()
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	for _, root := range opts.Roots {
		if norm, err := normalize(root); err == nil {
			h.roots = append(h.roots, norm)
		}
	}
	return h
}

// mergeProfiles lays the configuration over the built-in table: an entry with
// the same agent name replaces the built-in one, anything else is appended.
func mergeProfiles(builtin, extra []config.HandoffProfile) []config.HandoffProfile {
	out := append([]config.HandoffProfile(nil), builtin...)
	for _, p := range extra {
		replaced := false
		for i := range out {
			if out[i].Agent == p.Agent {
				out[i], replaced = p, true
				break
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	return out
}

// profile finds the handoff entry of an agent, first by its configured name and
// then by the preset it was built from.
func (h *handoff) profile(agent string) (config.HandoffProfile, bool) {
	if p, ok := findProfile(h.profiles, agent); ok {
		return p, true
	}
	for _, a := range h.agents {
		if a.Name == agent && a.Registry != "" {
			return findProfile(h.profiles, a.Registry)
		}
	}
	return config.HandoffProfile{}, false
}

func findProfile(profiles []config.HandoffProfile, agent string) (config.HandoffProfile, bool) {
	for _, p := range profiles {
		if p.Agent == agent {
			return p, true
		}
	}
	return config.HandoffProfile{}, false
}

// BackCommand fills the session ID into the agent's resume command. An agent
// whose command has not been verified reports fsp.ErrUnsupported rather than
// handing the operator a guess.
func (h *handoff) BackCommand(agent, agentSessionID string) ([]string, error) {
	p, ok := h.profile(agent)
	if !ok || len(p.Resume) == 0 {
		return nil, fmt.Errorf("handoff: %s cannot be resumed in a terminal: %w", agent, fsp.ErrUnsupported)
	}
	out := make([]string, 0, len(p.Resume))
	for _, arg := range p.Resume {
		out = append(out, strings.ReplaceAll(arg, "{id}", agentSessionID))
	}
	return out, nil
}

// normalize makes a path absolute and resolves symlinks, so two spellings of
// the same directory compare equal.
func normalize(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("handoff: empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
