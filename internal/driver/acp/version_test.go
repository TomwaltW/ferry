package acp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeagent"
)

// v2Spec points at the fake agent in its v2 mode, which is the only mode that
// answers initialize with version 2.
func v2Spec(t *testing.T, home string) ports.SessionSpec {
	t.Helper()
	spec := agentSpec(t, home)
	spec.Command = append(spec.Command, "--acp-version", "2")
	return spec
}

// initializes reads the protocol versions the fake agent has been asked for,
// oldest first. Each line is one initialize it answered.
func initializes(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, fakeagent.InitializeLog))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the initialize log: %v", err)
	}
	return strings.Fields(string(data))
}

// TestAutoFallsBackToV1 pins the auto path of spec-p3 3.7: v2 first, and an
// agent that negotiates back to version 1 is closed and started again over v1.
func TestAutoFallsBackToV1(t *testing.T) {
	home := t.TempDir()
	d := NewVersioned(testLogger(), FixedVersion(config.ACPVersionAuto))

	s, err := d.Start(t.Context(), agentSpec(t, home), nil)
	if err != nil {
		t.Fatalf("Start in auto mode failed: %v", err)
	}
	defer s.Close()

	// Two initializes: the v2 probe the agent turned down, then the v1 start.
	if got := initializes(t, home); len(got) != 2 || got[0] != "2" || got[1] != "1" {
		t.Fatalf("the agent was initialized with %v, want a v2 probe followed by v1", got)
	}

	// What came back is a working v1 session.
	if err := s.Prompt(t.Context(), "echo hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "hello" {
		t.Errorf("the agent said %q, want %q", got, "hello")
	}

	// An agent that does speak v2 is left on v2, with no second start.
	v2Home := t.TempDir()
	up, err := d.Start(t.Context(), v2Spec(t, v2Home), nil)
	if err != nil {
		t.Fatalf("Start in auto mode against a v2 agent failed: %v", err)
	}
	defer up.Close()

	if got := initializes(t, v2Home); len(got) != 1 || got[0] != "2" {
		t.Errorf("the v2 agent was initialized with %v, want one v2 initialize", got)
	}
}

// TestV1Unchanged is the guard on the v1 path: P1's driver still behaves exactly
// as it did, and asking for v1 never probes v2 first.
func TestV1Unchanged(t *testing.T) {
	// New is still the v1 driver, with the caps and the streaming of spec-p1.
	home := t.TempDir()
	plain := testDriver(t)
	s, err := plain.Start(t.Context(), agentSpec(t, home), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if plain.Kind() != fsp.DriverACP {
		t.Errorf("Kind = %q, want acp", plain.Kind())
	}
	caps := s.Caps()
	if !caps.Load || !caps.List || !caps.Cancel {
		t.Errorf("Caps = %+v, want load, list and cancel from the fake agent", caps)
	}
	if err := s.Prompt(t.Context(), "echo hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "hello" {
		t.Errorf("the agent said %q, want %q", got, "hello")
	}
	if end := events[len(events)-1]; end.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", end.StopReason)
	}
	if got := initializes(t, home); len(got) != 1 || got[0] != "1" {
		t.Errorf("the agent was initialized with %v, want one v1 initialize", got)
	}

	// The versioned driver on v1 — the default of spec-p3 3.2 — goes down the
	// same path: one initialize, at version 1, and the same events.
	for _, version := range []string{config.ACPVersionV1, ""} {
		t.Run("version="+version, func(t *testing.T) {
			home := t.TempDir()
			d := NewVersioned(testLogger(), FixedVersion(version))

			s, err := d.Start(t.Context(), agentSpec(t, home), nil)
			if err != nil {
				t.Fatalf("Start failed: %v", err)
			}
			defer s.Close()

			if err := s.Prompt(t.Context(), "echo hello"); err != nil {
				t.Fatalf("Prompt failed: %v", err)
			}
			events := untilTurnEnd(t, s.Events())
			if got := strings.Join(chunksOf(events), ""); got != "hello" {
				t.Errorf("the agent said %q, want %q", got, "hello")
			}
			if got := initializes(t, home); len(got) != 1 || got[0] != "1" {
				t.Errorf("the agent was initialized with %v, want one v1 initialize", got)
			}
		})
	}

	// Resume and List still reach the v1 methods: the fake agent only
	// implements session/load and session/list over v1.
	shared := t.TempDir()
	d := NewVersioned(testLogger(), FixedVersion(config.ACPVersionV1))
	spec := agentSpec(t, shared)

	first, err := d.Start(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	id := first.AgentSessionID()
	if err := first.Prompt(t.Context(), "echo remembered"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	untilTurnEnd(t, first.Events())
	if err := first.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	listed, err := d.List(t.Context(), spec)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	found := false
	for _, info := range listed {
		if info.AgentSessionID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("List returned %+v, want the session %s among them", listed, id)
	}

	back, err := d.Resume(t.Context(), spec, id, nil)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	defer back.Close()
	if back.AgentSessionID() != id {
		t.Errorf("the resumed session is %s, want %s", back.AgentSessionID(), id)
	}

	// A version nobody configured is v1 too, and an unknown one is not an
	// excuse to guess at v2.
	if _, err := d.Resume(t.Context(), spec, "sess-nonesuch", nil); err == nil {
		t.Error("Resume of an unknown session succeeded, want an error from the agent")
	} else if errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("Resume of an unknown session returned %v, want the agent's own error", err)
	}
}
