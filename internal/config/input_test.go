package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInputGather pins input.gather: 2.5s unless the file says otherwise, "0s"
// switches it off, it survives Save and Load, and Validate keeps it within
// 0s..10s.
func TestInputGather(t *testing.T) {
	if got := Default().Input.Gather.Duration; got != 2500*time.Millisecond {
		t.Errorf("default input.gather = %s, want 2.5s", got)
	}

	c, err := decode([]byte("[input]\ngather = \"0s\"\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.Input.Gather.Duration != 0 {
		t.Errorf(`input.gather = "0s" decoded as %s, want 0`, c.Input.Gather.Duration)
	}
	c, err = decode([]byte("lang = \"en\"\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.Input.Gather.Duration != DefaultGather {
		t.Errorf("input.gather left out decoded as %s, want the default", c.Input.Gather.Duration)
	}

	c = withRoot(t)
	c.Input.Gather = Duration{4 * time.Second}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.Input.Gather.Duration != 4*time.Second {
		t.Errorf("round trip input.gather = %s, want 4s", back.Input.Gather.Duration)
	}

	for _, ok := range []time.Duration{0, time.Second, maxGather} {
		c := withRoot(t)
		c.Input.Gather = Duration{ok}
		if err := c.Validate(); err != nil {
			t.Errorf("Validate with input.gather = %s: %v", ok, err)
		}
	}
	for _, bad := range []time.Duration{-time.Second, maxGather + time.Millisecond} {
		c := withRoot(t)
		c.Input.Gather = Duration{bad}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "input.gather") {
			t.Errorf("Validate with input.gather = %s = %v, want an input.gather error", bad, err)
		}
	}
}
