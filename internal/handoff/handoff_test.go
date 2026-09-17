package handoff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

var epoch = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

var testEndpoint = fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "peer"}

// ---------- test doubles ----------

type fakeRegistry struct{ specs map[string]ports.SessionSpec }

func (f fakeRegistry) Resolve(ctx context.Context, agent string) (ports.SessionSpec, error) {
	spec, ok := f.specs[agent]
	if !ok {
		return ports.SessionSpec{}, fsp.ErrNotFound
	}
	return spec, nil
}

func (f fakeRegistry) Agents(ctx context.Context) []ports.AgentInfo { return nil }

// fakeDriver answers List out of a table keyed by the working directory it was
// asked about, with "" standing for a call that named no directory.
type fakeDriver struct {
	mu       sync.Mutex
	kind     fsp.DriverKind
	byCwd    map[string][]ports.AgentSessionInfo
	errNoCwd error // what a call without a directory fails with
	block    bool  // wait for the context instead of answering
	calls    []string
}

func (f *fakeDriver) Kind() fsp.DriverKind {
	if f.kind == "" {
		return fsp.DriverACP
	}
	return f.kind
}

func (f *fakeDriver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	return nil, fsp.ErrUnsupported
}

func (f *fakeDriver) Resume(ctx context.Context, spec ports.SessionSpec, id string, ap ports.Approver) (ports.AgentSession, error) {
	return nil, fsp.ErrUnsupported
}

func (f *fakeDriver) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	f.mu.Lock()
	f.calls = append(f.calls, spec.Cwd)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if spec.Cwd == "" && f.errNoCwd != nil {
		return nil, f.errNoCwd
	}
	return f.byCwd[spec.Cwd], nil
}

func (f *fakeDriver) listCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// fakeStore only ever answers ListSessions; anything else would be a bug in the
// code under test, and the embedded nil interface says so loudly.
type fakeStore struct {
	ports.Store
	sessions []fsp.Session
}

func (f *fakeStore) ListSessions(ctx context.Context, owner fsp.EndpointID) ([]fsp.Session, error) {
	return f.sessions, nil
}

// ---------- helpers ----------

// newHandoff builds the module and hands back the concrete type, so a test can
// reach the process table hooks.
func newHandoff(t *testing.T, opts Options) *handoff {
	t.Helper()
	if opts.Clock == nil {
		opts.Clock = clock.NewFake(epoch)
	}
	h, ok := New(opts).(*handoff)
	if !ok {
		t.Fatalf("New returned %T, want *handoff", New(opts))
	}
	return h
}

func realDir(t *testing.T, dir string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return path
}

func ids(sessions []ports.TerminalSession) []string {
	out := make([]string, 0, len(sessions))
	for _, ts := range sessions {
		out = append(out, ts.AgentSessionID)
	}
	return out
}

func agentSpecs(cmd ...string) map[string]ports.SessionSpec {
	return map[string]ports.SessionSpec{"demo": {Agent: "demo", Command: cmd}}
}

func demoAgents() []config.AgentConfig { return []config.AgentConfig{{Name: "demo"}} }

func demoProfile(match string) []config.HandoffProfile {
	return []config.HandoffProfile{{Agent: "demo", Resume: []string{"demo", "--resume", "{id}"}, ProcessMatch: match}}
}

// ---------- discovery ----------

func TestDiscoverFiltersRoots(t *testing.T) {
	root := realDir(t, t.TempDir())
	outside := realDir(t, t.TempDir())
	inside := filepath.Join(root, "demo")
	mustNil(t, os.MkdirAll(inside, 0o755))

	dr := &fakeDriver{byCwd: map[string][]ports.AgentSessionInfo{"": {
		{AgentSessionID: "in", Title: "inside", Cwd: inside, UpdatedAt: epoch.Add(-time.Hour)},
		{AgentSessionID: "out", Title: "outside", Cwd: outside, UpdatedAt: epoch.Add(-time.Hour)},
		{AgentSessionID: "old", Title: "stale", Cwd: inside, UpdatedAt: epoch.Add(-30 * 24 * time.Hour)},
	}}}
	h := newHandoff(t, Options{
		Roots:    []string{root},
		Agents:   demoAgents(),
		Profiles: demoProfile("demo"),
		Registry: fakeRegistry{specs: agentSpecs("demo", "--acp")},
		Drivers:  map[fsp.DriverKind]ports.Driver{fsp.DriverACP: dr},
	})

	got, err := h.Discover(context.Background(), testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if want := []string{"in"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Discover returned %v, want %v", ids(got), want)
	}
	if got[0].Cwd != inside {
		t.Errorf("Cwd = %q, want %q", got[0].Cwd, inside)
	}
	if got[0].Agent != "demo" || got[0].Driver != fsp.DriverACP {
		t.Errorf("session = %+v, want the agent and driver it came from", got[0])
	}
	if got[0].Shared {
		t.Errorf("an ACP session must not be marked shared")
	}
}

func TestDiscoverExcludesRegistered(t *testing.T) {
	root := realDir(t, t.TempDir())
	dr := &fakeDriver{byCwd: map[string][]ports.AgentSessionInfo{"": {
		{AgentSessionID: "taken", Cwd: root, UpdatedAt: epoch},
		{AgentSessionID: "free", Cwd: root, UpdatedAt: epoch},
		{AgentSessionID: "closed", Cwd: root, UpdatedAt: epoch},
	}}}
	st := &fakeStore{sessions: []fsp.Session{
		{ID: "s1", Agent: "demo", AgentSessionID: "taken", State: fsp.StateIdle},
		{ID: "s2", Agent: "demo", AgentSessionID: "closed", State: fsp.StateClosed},
	}}
	h := newHandoff(t, Options{
		Roots:    []string{root},
		Agents:   demoAgents(),
		Profiles: demoProfile("demo"),
		Registry: fakeRegistry{specs: agentSpecs("demo")},
		Drivers:  map[fsp.DriverKind]ports.Driver{fsp.DriverACP: dr},
		Store:    st,
	})

	got, err := h.Discover(context.Background(), testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	// A closed session is free again, so it is offered like any other.
	want := []string{"closed", "free"}
	gotIDs := ids(got)
	slices.Sort(gotIDs)
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("Discover returned %v, want %v", gotIDs, want)
	}
}

func TestDiscoverCache(t *testing.T) {
	root := realDir(t, t.TempDir())
	clk := clock.NewFake(epoch)
	dr := &fakeDriver{byCwd: map[string][]ports.AgentSessionInfo{"": {
		{AgentSessionID: "one", Cwd: root, UpdatedAt: epoch},
	}}}
	h := newHandoff(t, Options{
		Roots:    []string{root},
		Agents:   demoAgents(),
		Profiles: demoProfile("demo"),
		Registry: fakeRegistry{specs: agentSpecs("demo")},
		Drivers:  map[fsp.DriverKind]ports.Driver{fsp.DriverACP: dr},
		Clock:    clk,
	})

	ctx := context.Background()
	mustDiscover(t, h, ctx)
	clk.Advance(30 * time.Second)
	mustDiscover(t, h, ctx)
	if n := len(dr.listCalls()); n != 1 {
		t.Errorf("the agent was listed %d times inside the cache window, want 1", n)
	}

	clk.Advance(31 * time.Second)
	mustDiscover(t, h, ctx)
	if n := len(dr.listCalls()); n != 2 {
		t.Errorf("the agent was listed %d times after the cache expired, want 2", n)
	}
}

func TestDiscoverAgentTimeout(t *testing.T) {
	if agentTimeout != 10*time.Second {
		t.Errorf("agentTimeout = %v, want the 10s of spec-p2 5.2", agentTimeout)
	}
	root := realDir(t, t.TempDir())
	slow := &fakeDriver{block: true}
	quick := &fakeDriver{kind: fsp.DriverServer, byCwd: map[string][]ports.AgentSessionInfo{"": {
		{AgentSessionID: "quick", Cwd: root, UpdatedAt: epoch},
	}}}
	h := newHandoff(t, Options{
		Roots: []string{root},
		Agents: []config.AgentConfig{
			{Name: "slow", Handoff: boolPtr(true)},
			{Name: "quick", Handoff: boolPtr(true)},
		},
		Registry: fakeRegistry{specs: map[string]ports.SessionSpec{
			"slow":  {Agent: "slow", Command: []string{"slow"}},
			"quick": {Agent: "quick", Server: &ports.ServerSpec{Kind: "opencode", URL: "http://127.0.0.1:1"}},
		}},
		Drivers: map[fsp.DriverKind]ports.Driver{fsp.DriverACP: slow, fsp.DriverServer: quick},
	})

	// The caller's deadline bounds the per-agent one, so the hanging agent is
	// given up on without the whole listing being lost.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	got, err := h.Discover(ctx, testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if want := []string{"quick"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Discover returned %v, want %v", ids(got), want)
	}
	if !got[0].Shared {
		t.Errorf("a service-backed session must be shared, so no busy check runs")
	}
}

func TestDiscoverFallbackPerRoot(t *testing.T) {
	root := realDir(t, t.TempDir())
	sub := filepath.Join(root, "demo")
	mustNil(t, os.MkdirAll(sub, 0o755))
	mustNil(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600))

	dr := &fakeDriver{
		errNoCwd: errors.New(`acp: list the sessions of demo: {"code":-32602,"message":"Invalid params"}`),
		byCwd: map[string][]ports.AgentSessionInfo{
			sub: {{AgentSessionID: "sub", Cwd: sub, UpdatedAt: epoch}},
		},
	}
	h := newHandoff(t, Options{
		Roots:    []string{root},
		Agents:   demoAgents(),
		Profiles: demoProfile("demo"),
		Registry: fakeRegistry{specs: agentSpecs("demo")},
		Drivers:  map[fsp.DriverKind]ports.Driver{fsp.DriverACP: dr},
	})

	got, err := h.Discover(context.Background(), testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if want := []string{"sub"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Discover returned %v, want %v", ids(got), want)
	}
	// One call without a directory, then the root and its one subdirectory.
	want := []string{"", root, sub}
	if calls := dr.listCalls(); !reflect.DeepEqual(calls, want) {
		t.Errorf("List was called with %v, want %v", calls, want)
	}
}

func TestDiscoverSkipsAgentsThatOptOut(t *testing.T) {
	root := realDir(t, t.TempDir())
	dr := &fakeDriver{byCwd: map[string][]ports.AgentSessionInfo{"": {
		{AgentSessionID: "one", Cwd: root, UpdatedAt: epoch},
	}}}
	h := newHandoff(t, Options{
		Roots:  []string{root},
		Agents: []config.AgentConfig{{Name: "demo", Handoff: boolPtr(false)}, {Name: "nameless"}},
		// "nameless" has no profile and says nothing, so it stays out too.
		Profiles: demoProfile("demo"),
		Registry: fakeRegistry{specs: agentSpecs("demo")},
		Drivers:  map[fsp.DriverKind]ports.Driver{fsp.DriverACP: dr},
	})

	got, err := h.Discover(context.Background(), testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Discover returned %v, want nothing", ids(got))
	}
	if n := len(dr.listCalls()); n != 0 {
		t.Errorf("the driver was called %d times, want none", n)
	}
}

// ---------- back command ----------

func TestBackCommandTemplate(t *testing.T) {
	h := newHandoff(t, Options{
		Agents:   []config.AgentConfig{{Name: "chat", Registry: "qwen-code"}},
		Profiles: []config.HandoffProfile{{Agent: "local", Resume: []string{"local", "--session", "{id}", "--cwd={id}"}}},
	})

	cases := map[string]struct {
		agent string
		want  []string
	}{
		"built-in table":   {agent: "fake", want: []string{"ferry-fakeagent", "--resume", "sess-1"}},
		"through a preset": {agent: "chat", want: []string{"qwen", "--resume", "sess-1"}},
		"from the config":  {agent: "local", want: []string{"local", "--session", "sess-1", "--cwd=sess-1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := h.BackCommand(tc.agent, "sess-1")
			if err != nil {
				t.Fatalf("BackCommand(%q) failed: %v", tc.agent, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("BackCommand(%q) = %v, want %v", tc.agent, got, tc.want)
			}
		})
	}
}

func TestBackCommandUnsupported(t *testing.T) {
	h := newHandoff(t, Options{Agents: []config.AgentConfig{{Name: "oc", Registry: "opencode"}}})

	// opencode is in the built-in table with an empty Resume: unverified, so
	// ferry back says so instead of guessing a command.
	for _, agent := range []string{"opencode", "oc", "nobody"} {
		got, err := h.BackCommand(agent, "sess-1")
		if !errors.Is(err, fsp.ErrUnsupported) {
			t.Errorf("BackCommand(%q) = %v, %v, want fsp.ErrUnsupported", agent, got, err)
		}
	}
}

func TestConfigProfileOverridesBuiltin(t *testing.T) {
	h := newHandoff(t, Options{
		Profiles: []config.HandoffProfile{{Agent: "kimi", Resume: []string{"kimi", "--session", "{id}"}}},
	})
	got, err := h.BackCommand("kimi", "abc")
	if err != nil {
		t.Fatalf("BackCommand failed: %v", err)
	}
	if want := []string{"kimi", "--session", "abc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("BackCommand = %v, want the configured %v", got, want)
	}
}

func mustDiscover(t *testing.T, h *handoff, ctx context.Context) []ports.TerminalSession {
	t.Helper()
	got, err := h.Discover(ctx, testEndpoint)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	return got
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }
