package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

// FileName is the base name of the configuration file inside the data
// directory.
const FileName = "config.toml"

// Lower bounds enforced by Validate; anything smaller turns the outbox into a
// flood.
const (
	minMergeInterval = 100 * time.Millisecond
	minReplyBudget   = 2
)

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Home returns FERRY_HOME, or ~/.ferry when it is unset.
func Home() string {
	if h := os.Getenv("FERRY_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ferry"
	}
	return filepath.Join(home, ".ferry")
}

// Path returns <home>/config.toml.
func Path(home string) string { return filepath.Join(home, FileName) }

func Default() Config {
	return Config{
		Lang:         fsp.LangZH,
		DefaultAgent: "qwen-code",
		WeChat: WeChatConfig{
			Enabled:        true,
			BaseURL:        ilinkwire.DefaultBaseURL,
			ChannelVersion: ilinkwire.DefaultChannelVersion,
			Render:         "plain",
			Approvers:      []string{"owner"},
		},
		Workspace: WorkspaceConfig{},
		Agents: []AgentConfig{
			{Name: "qwen-code", Registry: "qwen-code"},
			{Name: "kimi", Registry: "kimi"},
			{Name: "codebuddy-code", Registry: "codebuddy-code"},
			{Name: "opencode", Registry: "opencode"},
		},
		Policy: PolicyConfig{
			Default:         "ask",
			ApprovalTimeout: Duration{10 * time.Minute},
		},
		Outbox: OutboxConfig{
			MergeInterval:       Duration{30 * time.Second},
			ReplyBudget:         8,
			LongOutputThreshold: 1500,
			SendTimeout:         Duration{15 * time.Second},
		},
		Registry: RegistryConfig{SnapshotOnly: true},
		Limits:   LimitsConfig{MaxManagedSessions: 3},
	}
}

// Load returns fsp.ErrNotFound when the file is missing; validation errors
// name the offending field.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Config{}, fmt.Errorf("config: %s: %w", path, fsp.ErrNotFound)
	case err != nil:
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	c, err := decode(data)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// decode layers the file on top of Default. A list in the file replaces the
// default list instead of merging into it element by element, which is what a
// reader of the file expects: three [[agents]] entries mean three agents.
func decode(data []byte) (Config, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}
	c := Default()
	if _, ok := raw["agents"]; ok {
		c.Agents = nil
	}
	if hasKey(raw, "workspace", "roots") {
		c.Workspace.Roots = nil
	}
	if hasKey(raw, "wechat", "approvers") {
		c.WeChat.Approvers = nil
	}
	if hasKey(raw, "policy", "rules") {
		c.Policy.Rules = nil
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func hasKey(raw map[string]any, table, key string) bool {
	t, ok := raw[table].(map[string]any)
	if !ok {
		return false
	}
	_, ok = t[key]
	return ok
}

// Save writes the file with mode 0600.
func Save(path string, c Config) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("config: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	// WriteFile only applies the mode when it creates the file; an existing
	// config must not keep looser permissions.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: chmod %s: %w", path, err)
	}
	return nil
}

func (c Config) Validate() error {
	switch c.Lang {
	case fsp.LangZH, fsp.LangEN:
	default:
		return fmt.Errorf("lang: %q is neither %q nor %q", c.Lang, fsp.LangZH, fsp.LangEN)
	}
	seen := make(map[string]bool, len(c.Agents))
	for i, a := range c.Agents {
		if !agentNameRe.MatchString(a.Name) {
			return fmt.Errorf("agents[%d].name: %q does not match %s", i, a.Name, agentNameRe)
		}
		if seen[a.Name] {
			return fmt.Errorf("agents[%d].name: %q is used more than once", i, a.Name)
		}
		seen[a.Name] = true
		hasCommand := len(a.Command) != 0
		switch {
		case a.Registry == "" && !hasCommand:
			return fmt.Errorf("agents[%d]: %q sets neither registry nor command", i, a.Name)
		case a.Registry != "" && hasCommand:
			return fmt.Errorf("agents[%d]: %q sets both registry and command", i, a.Name)
		}
	}
	if c.DefaultAgent == "" {
		return errors.New("default_agent: must be set")
	}
	if !seen[c.DefaultAgent] {
		return fmt.Errorf("default_agent: %q is not in agents", c.DefaultAgent)
	}
	if err := validAction(c.Policy.Default); err != nil {
		return fmt.Errorf("policy.default: %w", err)
	}
	for i, r := range c.Policy.Rules {
		if err := validAction(r.Action); err != nil {
			return fmt.Errorf("policy.rules[%d].action: %w", i, err)
		}
		if r.CommandPattern != "" {
			if _, err := regexp.Compile(r.CommandPattern); err != nil {
				return fmt.Errorf("policy.rules[%d].command_pattern: %w", i, err)
			}
		}
	}
	if c.Outbox.ReplyBudget < minReplyBudget {
		return fmt.Errorf("outbox.reply_budget: %d is below %d", c.Outbox.ReplyBudget, minReplyBudget)
	}
	if c.Outbox.MergeInterval.Duration < minMergeInterval {
		return fmt.Errorf("outbox.merge_interval: %s is below %s", c.Outbox.MergeInterval.Duration, minMergeInterval)
	}
	if len(c.Workspace.Roots) == 0 {
		return errors.New("workspace.roots: at least one directory is required")
	}
	for i, root := range c.Workspace.Roots {
		p, err := expandPath(root)
		if err != nil {
			return fmt.Errorf("workspace.roots[%d]: %w", i, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("workspace.roots[%d]: %q: %w", i, root, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace.roots[%d]: %q is not a directory", i, root)
		}
	}
	return nil
}

func validAction(a string) error {
	switch a {
	case "allow", "ask", "deny":
		return nil
	default:
		return fmt.Errorf("%q is not allow, ask or deny", a)
	}
}

// NormalizedRoots expands ~, makes paths absolute and resolves symlinks.
func (c Config) NormalizedRoots() ([]string, error) {
	roots := make([]string, 0, len(c.Workspace.Roots))
	for i, root := range c.Workspace.Roots {
		p, err := expandPath(root)
		if err != nil {
			return nil, fmt.Errorf("workspace.roots[%d]: %w", i, err)
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fmt.Errorf("workspace.roots[%d]: %q: %w", i, root, err)
		}
		roots = append(roots, resolved)
	}
	return roots, nil
}

// expandPath turns "~" and "~/x" into paths under the user's home directory
// and makes every other path absolute. It does not touch the disk.
func expandPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("absolute %q: %w", p, err)
	}
	return abs, nil
}
