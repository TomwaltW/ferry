package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// withRoot returns Default with workspace.roots pointing at a directory that
// exists, which is the one default Validate cannot satisfy on its own.
func withRoot(t *testing.T) Config {
	t.Helper()
	c := Default()
	c.Workspace.Roots = []string{t.TempDir()}
	return c
}

func TestDefaultValid(t *testing.T) {
	d := Default()
	// Every default is valid except workspace.roots, which ferry init must ask
	// for; check that this is the only complaint.
	err := d.Validate()
	if err == nil {
		t.Fatal("Default().Validate() = nil, want a workspace.roots error")
	}
	if !strings.Contains(err.Error(), "workspace.roots") {
		t.Fatalf("Default().Validate() = %v, want it to name workspace.roots", err)
	}
	if err := withRoot(t).Validate(); err != nil {
		t.Fatalf("Validate with a root: %v", err)
	}

	if d.Lang != fsp.LangZH {
		t.Errorf("lang = %q, want %q", d.Lang, fsp.LangZH)
	}
	if d.DefaultAgent != "qwen-code" {
		t.Errorf("default_agent = %q, want qwen-code", d.DefaultAgent)
	}
	if len(d.Agents) != 4 {
		t.Errorf("agents = %d, want 4 registry presets", len(d.Agents))
	}
	if d.Policy.Default != "ask" {
		t.Errorf("policy.default = %q, want ask", d.Policy.Default)
	}
	if d.Policy.ApprovalTimeout.Duration != 10*time.Minute {
		t.Errorf("policy.approval_timeout = %s, want 10m", d.Policy.ApprovalTimeout.Duration)
	}
	if d.Outbox.MergeInterval.Duration != 30*time.Second {
		t.Errorf("outbox.merge_interval = %s, want 30s", d.Outbox.MergeInterval.Duration)
	}
	if d.Outbox.ReplyBudget != 8 || d.Outbox.LongOutputThreshold != 1500 {
		t.Errorf("outbox budget/threshold = %d/%d, want 8/1500", d.Outbox.ReplyBudget, d.Outbox.LongOutputThreshold)
	}
	if d.Outbox.SendTimeout.Duration != 15*time.Second {
		t.Errorf("outbox.send_timeout = %s, want 15s", d.Outbox.SendTimeout.Duration)
	}
	if !d.WeChat.Enabled || d.WeChat.Render != "plain" {
		t.Errorf("wechat = %+v, want enabled with plain render", d.WeChat)
	}
	if !d.Registry.SnapshotOnly {
		t.Error("registry.snapshot_only = false, want true")
	}
	if d.Limits.MaxManagedSessions != 3 {
		t.Errorf("limits.max_managed_sessions = %d, want 3", d.Limits.MaxManagedSessions)
	}
}

func TestLoadSample(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sample.toml"))
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	// The sample points at ~/code, which need not exist on this machine;
	// substitute a real directory so the sample's own content is what is tested.
	dir := t.TempDir()
	root := filepath.Join(dir, "code")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data = bytes.ReplaceAll(data, []byte(`"~/code"`), []byte(strconv.Quote(root)))
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DefaultAgent != "kimi" {
		t.Errorf("default_agent = %q, want kimi", c.DefaultAgent)
	}
	if len(c.Agents) != 3 {
		t.Fatalf("agents = %d, want the 3 from the file (defaults must be replaced, not merged)", len(c.Agents))
	}
	if got := c.Agents[1].Env["OPENAI_BASE_URL"]; got != "https://example.invalid/v1" {
		t.Errorf("agents[1].env = %q, want the sample's base URL", got)
	}
	if got := c.Agents[2].Command; len(got) != 2 || got[0] != "/usr/local/bin/my-agent" {
		t.Errorf("agents[2].command = %q, want the sample's command", got)
	}
	if len(c.Policy.Rules) != 1 || c.Policy.Rules[0].Action != "allow" {
		t.Errorf("policy.rules = %+v, want one allow rule", c.Policy.Rules)
	}
	if c.Policy.ApprovalTimeout.Duration != 10*time.Minute {
		t.Errorf("policy.approval_timeout = %s, want 10m", c.Policy.ApprovalTimeout.Duration)
	}
	if c.Outbox.MergeInterval.Duration != 30*time.Second {
		t.Errorf("outbox.merge_interval = %s, want 30s", c.Outbox.MergeInterval.Duration)
	}
	if c.Registry.NPMRegistry != "https://registry.npmmirror.com" {
		t.Errorf("registry.npm_registry = %q, want the mirror", c.Registry.NPMRegistry)
	}
	// Keys the file leaves out keep their defaults.
	if c.Outbox.LongOutputThreshold != 1500 || c.Limits.MaxManagedSessions != 3 {
		t.Errorf("defaults lost: threshold=%d sessions=%d", c.Outbox.LongOutputThreshold, c.Limits.MaxManagedSessions)
	}
	if c.WeChat.BaseURL == "" || c.WeChat.Render != "plain" {
		t.Errorf("wechat defaults lost: %+v", c.WeChat)
	}
	if c.Policy.Default != "ask" {
		t.Errorf("policy.default = %q, want the default ask", c.Policy.Default)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("Load(missing) = %v, want fsp.ErrNotFound", err)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(*Config)
		field string
	}{
		{"bad lang", func(c *Config) { c.Lang = "fr" }, "lang"},
		{"bad agent name", func(c *Config) { c.Agents[0].Name = "Qwen_Code" }, "agents[0].name"},
		{"duplicate agent", func(c *Config) {
			c.Agents = append(c.Agents, AgentConfig{Name: "kimi", Registry: "kimi"})
		}, "is used more than once"},
		{"agent with neither", func(c *Config) {
			c.Agents = []AgentConfig{{Name: "solo"}}
			c.DefaultAgent = "solo"
		}, "neither registry nor command"},
		{"agent with both", func(c *Config) {
			c.Agents = []AgentConfig{{Name: "solo", Registry: "kimi", Command: []string{"/bin/true"}}}
			c.DefaultAgent = "solo"
		}, "both registry and command"},
		{"unknown default agent", func(c *Config) { c.DefaultAgent = "nope" }, "default_agent"},
		{"bad policy default", func(c *Config) { c.Policy.Default = "maybe" }, "policy.default"},
		{"bad rule action", func(c *Config) {
			c.Policy.Rules = []PolicyRule{{Action: "maybe"}}
		}, "policy.rules[0].action"},
		{"bad rule pattern", func(c *Config) {
			c.Policy.Rules = []PolicyRule{{Action: "allow", CommandPattern: "("}}
		}, "policy.rules[0].command_pattern"},
		{"small reply budget", func(c *Config) { c.Outbox.ReplyBudget = 1 }, "outbox.reply_budget"},
		{"small merge interval", func(c *Config) { c.Outbox.MergeInterval = Duration{50 * time.Millisecond} }, "outbox.merge_interval"},
		{"no roots", func(c *Config) { c.Workspace.Roots = nil }, "workspace.roots"},
		{"missing root", func(c *Config) { c.Workspace.Roots = []string{filepath.Join(t.TempDir(), "absent")} }, "workspace.roots[0]"},
		{"root is a file", func(c *Config) {
			f := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			c.Workspace.Roots = []string{f}
		}, "is not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := withRoot(t)
			tt.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tt.field)
			}
		})
	}
}

func TestTildeExpansion(t *testing.T) {
	if runtime.GOOS == "windows" {
		// ~ resolves through USERPROFILE rather than HOME there, and creating a
		// symlink needs privileges the CI runner does not have.
		t.Skip("the test is shaped around HOME and symlinks, which Windows does not provide")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	real := filepath.Join(home, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// EvalSymlinks also resolves the temp directory itself (/var -> /private/var
	// on macOS), so compare against the resolved form.
	wantReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	c := Default()
	c.Workspace.Roots = []string{"~/real", "~/link", real}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	roots, err := c.NormalizedRoots()
	if err != nil {
		t.Fatalf("NormalizedRoots: %v", err)
	}
	if len(roots) != 3 {
		t.Fatalf("NormalizedRoots = %q, want 3 entries", roots)
	}
	for i, got := range roots {
		if got != wantReal {
			t.Errorf("roots[%d] = %q, want %q", i, got, wantReal)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("roots[%d] = %q, want an absolute path", i, got)
		}
	}

	c.Workspace.Roots = []string{"~/absent"}
	if _, err := c.NormalizedRoots(); err == nil {
		t.Error("NormalizedRoots(~/absent) = nil error, want one")
	}

	t.Run("Home", func(t *testing.T) {
		t.Setenv("FERRY_HOME", "")
		if got, want := Home(), filepath.Join(home, ".ferry"); got != want {
			t.Errorf("Home() = %q, want %q", got, want)
		}
		t.Setenv("FERRY_HOME", "/opt/ferry")
		if got := Home(); got != "/opt/ferry" {
			t.Errorf("Home() = %q, want /opt/ferry", got)
		}
		if got, want := Path("/opt/ferry"), "/opt/ferry/config.toml"; got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

func TestDurationTOML(t *testing.T) {
	type holder struct {
		D Duration `toml:"d"`
	}
	for _, tt := range []struct {
		text string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"10m", 10 * time.Minute},
		{"100ms", 100 * time.Millisecond},
		{"1h30m", 90 * time.Minute},
	} {
		var h holder
		if err := toml.Unmarshal([]byte(`d = "`+tt.text+`"`), &h); err != nil {
			t.Fatalf("Unmarshal(%q): %v", tt.text, err)
		}
		if h.D.Duration != tt.want {
			t.Errorf("Unmarshal(%q) = %s, want %s", tt.text, h.D.Duration, tt.want)
		}
		out, err := toml.Marshal(holder{D: Duration{tt.want}})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tt.want, err)
		}
		var back holder
		if err := toml.Unmarshal(out, &back); err != nil {
			t.Fatalf("round trip %s: %v\n%s", tt.want, err, out)
		}
		if back.D.Duration != tt.want {
			t.Errorf("round trip %s = %s", tt.want, back.D.Duration)
		}
	}

	var h holder
	err := toml.Unmarshal([]byte(`d = "soon"`), &h)
	if err == nil {
		t.Fatal(`Unmarshal("soon") = nil error, want one`)
	}
	if !strings.Contains(err.Error(), "soon") {
		t.Errorf("error = %v, want it to quote the bad value", err)
	}
}

func TestSavePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	path := Path(dir)

	c := Default()
	root := t.TempDir()
	c.Workspace.Roots = []string{root}
	c.DefaultAgent = "kimi"

	if err := Save(path, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" { // Windows has no Unix permission bits
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %04o, want 0600", got)
		}
	}

	// Saving over a world-readable file must tighten it again.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := Save(path, c); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	if info, err = os.Stat(path); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" { // Windows has no Unix permission bits
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode after rewrite = %04o, want 0600", got)
		}
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if back.DefaultAgent != "kimi" || len(back.Agents) != len(c.Agents) {
		t.Errorf("round trip = %q/%d agents, want kimi/%d", back.DefaultAgent, len(back.Agents), len(c.Agents))
	}
	if back.Outbox.MergeInterval.Duration != c.Outbox.MergeInterval.Duration {
		t.Errorf("round trip merge_interval = %s, want %s", back.Outbox.MergeInterval.Duration, c.Outbox.MergeInterval.Duration)
	}
	if len(back.Workspace.Roots) != 1 || back.Workspace.Roots[0] != root {
		t.Errorf("round trip roots = %q, want [%q]", back.Workspace.Roots, root)
	}
}
