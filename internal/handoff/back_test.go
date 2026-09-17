package handoff

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/store"
)

// backStore answers the two questions ferry back asks of the database when no
// daemon is running.
type backStore struct {
	ports.Store
	sessions []fsp.Session
	bindings map[string][]ports.Binding
}

func (b *backStore) ActiveSessions(ctx context.Context) ([]fsp.Session, error) {
	return b.sessions, nil
}

func (b *backStore) Bindings(ctx context.Context, ep fsp.EndpointID) ([]ports.Binding, error) {
	return b.bindings[ep.String()], nil
}

func backFixture() *backStore {
	ep := testEndpoint
	return &backStore{
		sessions: []fsp.Session{
			{ID: "s1", Agent: "fake", AgentSessionID: "sess-1", Cwd: "/ws/one", Owner: ep, State: fsp.StateIdle, LastActive: epoch.Add(-time.Hour)},
			{ID: "s2", Agent: "fake", AgentSessionID: "sess-2", Cwd: "/ws/two", Owner: ep, State: fsp.StateIdle, LastActive: epoch},
		},
		bindings: map[string][]ports.Binding{ep.String(): {
			{Endpoint: ep, Session: "s1", Slot: 1},
			{Endpoint: ep, Session: "s2", Slot: 2, Foreground: true},
		}},
	}
}

func backModule(t *testing.T) ports.Handoff {
	t.Helper()
	return newHandoff(t, Options{Agents: []config.AgentConfig{{Name: "fake", Registry: "fake"}}})
}

func TestBackWithoutDaemon(t *testing.T) {
	st := backFixture()
	h := backModule(t)

	t.Run("the foreground session by default", func(t *testing.T) {
		rec, cmd, err := Local(context.Background(), st, h, 0)
		if err != nil {
			t.Fatalf("Local failed: %v", err)
		}
		if rec.ID != "s2" {
			t.Errorf("session = %q, want the foreground s2", rec.ID)
		}
		want := []string{"ferry-fakeagent", "--resume", "sess-2"}
		if !reflect.DeepEqual(cmd, want) {
			t.Errorf("command = %v, want %v", cmd, want)
		}
		if rec.Cwd != "/ws/two" {
			t.Errorf("cwd = %q, want the session's own directory", rec.Cwd)
		}
	})

	t.Run("a named slot", func(t *testing.T) {
		rec, cmd, err := Local(context.Background(), st, h, 1)
		if err != nil {
			t.Fatalf("Local failed: %v", err)
		}
		if rec.ID != "s1" {
			t.Errorf("session = %q, want s1", rec.ID)
		}
		if want := []string{"ferry-fakeagent", "--resume", "sess-1"}; !reflect.DeepEqual(cmd, want) {
			t.Errorf("command = %v, want %v", cmd, want)
		}
	})

	t.Run("a slot nobody has", func(t *testing.T) {
		if _, _, err := Local(context.Background(), st, h, 9); !errors.Is(err, fsp.ErrNotFound) {
			t.Errorf("Local = %v, want fsp.ErrNotFound", err)
		}
	})

	t.Run("no sessions at all", func(t *testing.T) {
		if _, _, err := Local(context.Background(), &backStore{}, h, 0); !errors.Is(err, fsp.ErrNotFound) {
			t.Errorf("Local = %v, want fsp.ErrNotFound", err)
		}
	})
}

func TestBackWithoutDaemonFallsBackToTheLastUsed(t *testing.T) {
	st := backFixture()
	// Nothing is in the foreground, so the session used last wins.
	ep := testEndpoint.String()
	st.bindings[ep] = []ports.Binding{
		{Session: "s1", Slot: 1},
		{Session: "s2", Slot: 2},
	}
	rec, _, err := Local(context.Background(), st, backModule(t), 0)
	if err != nil {
		t.Fatalf("Local failed: %v", err)
	}
	if rec.ID != "s2" {
		t.Errorf("session = %q, want s2, which was used last", rec.ID)
	}
}

func TestBackUnsupportedAgentHasNoCommand(t *testing.T) {
	st := backFixture()
	st.sessions[1].Agent = "opencode"
	h := newHandoff(t, Options{Agents: []config.AgentConfig{{Name: "opencode", Registry: "opencode"}}})

	rec, cmd, err := Local(context.Background(), st, h, 0)
	if err != nil {
		t.Fatalf("Local failed: %v", err)
	}
	// The caller prints back.unsupported and exits 3 on exactly this.
	if len(cmd) != 0 {
		t.Errorf("command = %v, want none for an agent whose resume is unverified", cmd)
	}
	if rec.Agent != "opencode" || rec.AgentSessionID != "sess-2" {
		t.Errorf("session = %+v, want the agent and id back.unsupported names", rec)
	}
}

func TestShellJoin(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"nothing to quote": {
			args: []string{"ferry-fakeagent", "--resume", "sess-1"},
			want: "ferry-fakeagent --resume sess-1",
		},
		"a path with spaces": {
			args: []string{"kimi", "--resume", "my session"},
			want: `kimi --resume 'my session'`,
		},
		"an embedded quote": {
			args: []string{"codex", "resume", "it's"},
			want: `codex resume 'it'\''s'`,
		},
		"an empty argument": {
			args: []string{"agent", ""},
			want: "agent ''",
		},
		"shell metacharacters": {
			args: []string{"agent", "a;rm -rf /", "$HOME", "a|b"},
			want: `agent 'a;rm -rf /' '$HOME' 'a|b'`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ShellJoin(tc.args); got != tc.want {
				t.Errorf("ShellJoin(%q) = %s, want %s", tc.args, got, tc.want)
			}
		})
	}
}

// TestBackWithoutDaemonAgainstTheRealStore is the same walk over the real
// database: ferry back reads it directly when no daemon is running, so the
// fakes above must not be the only thing the query is checked against.
func TestBackWithoutDaemonAgainstTheRealStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ferry.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	ep := testEndpoint
	for i, id := range []fsp.SessionID{"s1", "s2"} {
		mustNil(t, st.PutSession(ctx, fsp.Session{
			ID:             id,
			Driver:         fsp.DriverACP,
			Agent:          "fake",
			Cwd:            "/ws/" + string(id),
			AgentSessionID: "sess-" + string(id),
			Origin:         fsp.OriginTerminal,
			State:          fsp.StateIdle,
			Owner:          ep,
			CreatedAt:      epoch,
			LastActive:     epoch.Add(time.Duration(i) * time.Hour),
		}))
		if _, err := st.Bind(ctx, ep, id); err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
	}
	mustNil(t, st.SetForeground(ctx, ep, "s1"))

	rec, cmd, err := Local(ctx, st, backModule(t), 0)
	if err != nil {
		t.Fatalf("Local failed: %v", err)
	}
	if rec.ID != "s1" {
		t.Errorf("session = %q, want the foreground s1", rec.ID)
	}
	if want := []string{"ferry-fakeagent", "--resume", "sess-s1"}; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %v, want %v", cmd, want)
	}

	// Slot 2 is the one the database handed out second.
	rec, _, err = Local(ctx, st, backModule(t), 2)
	if err != nil {
		t.Fatalf("Local of slot 2 failed: %v", err)
	}
	if rec.ID != "s2" {
		t.Errorf("slot 2 = %q, want s2", rec.ID)
	}
}
