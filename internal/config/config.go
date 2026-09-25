package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
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
	minWindowTTL     = time.Second
)

// DefaultWindowTTL is how long a reply window is assumed to last. The measured
// context_token lifetime minus 30 minutes belongs here; until that measurement
// exists, spec-p2 3.2 fixes it at 23 hours.
const DefaultWindowTTL = 23 * time.Hour

// DefaultGather is how long an idle session waits for the rest of a message
// sent in pieces, and maxGather the longest wait Validate accepts: past that
// the pause reads as the bot not answering.
const (
	DefaultGather = 2500 * time.Millisecond
	maxGather     = 10 * time.Second
)

// The two ways of handling a turn whose text is too long for chat.
const (
	LongOutputFile  = "file"
	LongOutputSplit = "split"
)

// Channels is every wake-up channel notify.enabled may name.
var Channels = []string{"ntfy", "bark", "webhook", "email"}

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Home returns FERRY_HOME, or ~/.ferry when it is unset.
func Home() string {
	if h := os.Getenv("FERRY_HOME"); h != "" {
		return h
	}
	return DefaultHome()
}

// DefaultHome returns ~/.ferry, the data directory used when FERRY_HOME is
// unset, whatever FERRY_HOME says right now.
func DefaultHome() string {
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
			MaxMediaMB:     20,
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
			WindowTTL:           Duration{DefaultWindowTTL},
			DigestMax:           5,
			FileKeep:            20,
			LongOutput:          LongOutputFile,
		},
		Input:   InputConfig{Gather: Duration{DefaultGather}},
		Handoff: HandoffConfig{LookbackDays: 7},
		Notify: NotifyConfig{
			Events:      []string{"approval_pending", "turn_finished", "im_offline"},
			MinInterval: Duration{10 * time.Minute},
			Ntfy:        NtfyConfig{URL: "https://ntfy.sh"},
			Bark:        BarkConfig{URL: "https://api.day.app"},
			Email:       EmailConfig{Port: 587},
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
	if hasKey(raw, "notify", "enabled") {
		c.Notify.Enabled = nil
	}
	if hasKey(raw, "notify", "events") {
		c.Notify.Events = nil
	}
	if hasKey(raw, "notify", "email", "to") {
		c.Notify.Email.To = nil
	}
	if hasKey(raw, "handoff", "profiles") {
		c.Handoff.Profiles = nil
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// hasKey reports whether the decoded file spells out the given path, so that a
// list it sets can replace the default rather than appending to it.
func hasKey(raw map[string]any, path ...string) bool {
	node := raw
	for _, table := range path[:len(path)-1] {
		child, ok := node[table].(map[string]any)
		if !ok {
			return false
		}
		node = child
	}
	_, ok := node[path[len(path)-1]]
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
		// registry, command and server are three ways of saying where the agent
		// comes from; exactly one of them belongs in an entry.
		sources := 0
		for _, set := range []bool{a.Registry != "", len(a.Command) != 0, a.Server != nil} {
			if set {
				sources++
			}
		}
		switch sources {
		case 1:
		case 0:
			return fmt.Errorf("agents[%d]: %q sets none of registry, command and server", i, a.Name)
		default:
			return fmt.Errorf("agents[%d]: %q sets more than one of registry, command and server", i, a.Name)
		}
		if a.Server != nil {
			if err := a.Server.validate(); err != nil {
				return fmt.Errorf("agents[%d].server.%w", i, err)
			}
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
	if c.Outbox.WindowTTL.Duration < minWindowTTL {
		return fmt.Errorf("outbox.window_ttl: %s is below %s", c.Outbox.WindowTTL.Duration, minWindowTTL)
	}
	if g := c.Input.Gather.Duration; g < 0 || g > maxGather {
		return fmt.Errorf("input.gather: %s is outside 0s to %s", g, maxGather)
	}
	switch c.Outbox.LongOutput {
	case LongOutputFile, LongOutputSplit:
	default:
		return fmt.Errorf("outbox.long_output: %q is neither %q nor %q", c.Outbox.LongOutput, LongOutputFile, LongOutputSplit)
	}
	if c.WeChat.Enabled {
		if err := validApprovers("wechat.approvers", c.WeChat.Approvers); err != nil {
			return err
		}
	}
	if err := c.Notify.validate(); err != nil {
		return err
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
	return c.validateUsers()
}

// validateUsers checks the per-user workspace fields of spec-p4-b1 3.4. Both
// are optional; a configuration without them validates as it did before P4.
func (c Config) validateUsers() error {
	if c.Workspace.UsersRoot != "" {
		if err := existingDir(c.Workspace.UsersRoot); err != nil {
			return fmt.Errorf("workspace.users_root: %w", err)
		}
	}
	seen := make(map[string]bool, len(c.Workspace.Users))
	for i, u := range c.Workspace.Users {
		if err := c.userPlatform(u.Platform); err != nil {
			return fmt.Errorf("workspace.users[%d].platform: %w", i, err)
		}
		if strings.TrimSpace(u.ID) == "" {
			return fmt.Errorf("workspace.users[%d].id: must be set", i)
		}
		key := u.Platform + ":" + u.ID
		if seen[key] {
			return fmt.Errorf("workspace.users[%d]: %s %q is listed more than once", i, u.Platform, u.ID)
		}
		seen[key] = true
		if len(u.Roots) == 0 {
			return fmt.Errorf("workspace.users[%d].roots: at least one directory is required", i)
		}
		for j, root := range u.Roots {
			if err := existingDir(root); err != nil {
				return fmt.Errorf("workspace.users[%d].roots[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}

// userPlatform checks the platform of a [[workspace.users]] entry: one of the
// two IM platforms, or a channel server this configuration starts.
func (c Config) userPlatform(p string) error {
	switch fsp.Platform(p) {
	case fsp.PlatformWeChat, fsp.PlatformFeishu:
		return nil
	}
	if strings.HasPrefix(p, ChannelPlatformPrefix) {
		for _, s := range c.ChannelServers {
			if string(s.Platform()) == p {
				return nil
			}
		}
		return fmt.Errorf("%q names no configured channel server", p)
	}
	return fmt.Errorf("%q is none of %s, %s and %s<name>", p, fsp.PlatformWeChat, fsp.PlatformFeishu, ChannelPlatformPrefix)
}

// existingDir checks that p, with ~ expanded, names a directory.
func existingDir(p string) error {
	abs, err := expandPath(p)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("%q: %w", p, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", p)
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

// NormalizedUsersRoot is workspace.users_root in the form NormalizedRoots
// gives workspace.roots, or "" when it is unset. A user's directory is
// filepath.Join of it and UserDirName (spec-p4-b1 3.4).
func (c Config) NormalizedUsersRoot() (string, error) {
	if c.Workspace.UsersRoot == "" {
		return "", nil
	}
	p, err := normalizeDir(c.Workspace.UsersRoot)
	if err != nil {
		return "", fmt.Errorf("workspace.users_root: %w", err)
	}
	return p, nil
}

// NormalizedRoots are the roots of one [[workspace.users]] entry, in the form
// Config.NormalizedRoots gives workspace.roots.
func (u WorkspaceUser) NormalizedRoots() ([]string, error) {
	out := make([]string, 0, len(u.Roots))
	for i, root := range u.Roots {
		p, err := normalizeDir(root)
		if err != nil {
			return nil, fmt.Errorf("workspace.users %s %q roots[%d]: %w", u.Platform, u.ID, i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// normalizeDir expands ~, makes the path absolute and resolves symlinks, as
// NormalizedRoots does.
func normalizeDir(p string) (string, error) {
	abs, err := expandPath(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%q: %w", p, err)
	}
	return resolved, nil
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

// validate checks the fields of one service-backed agent.
func (s ServerConfig) validate() error {
	switch s.Kind {
	case "opencode", "qwen":
	default:
		return fmt.Errorf("kind: %q is neither \"opencode\" nor \"qwen\"", s.Kind)
	}
	if s.URL == "" {
		return errors.New("url: must be set")
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("url: %q: %w", s.URL, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url: %q has no host", s.URL)
	}
	// Ferry only ever drives a service on this machine; a remote address would
	// send prompts off the box.
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("url: %q is not a loopback address (127.0.0.1, ::1 or localhost)", s.URL)
		}
	}
	return nil
}

// validate checks that every channel named in notify.enabled has the fields it
// needs to send anything.
func (n NotifyConfig) validate() error {
	for i, name := range n.Enabled {
		var missing string
		switch name {
		case "ntfy":
			if n.Ntfy.Topic == "" {
				missing = "notify.ntfy.topic"
			}
		case "bark":
			if n.Bark.KeyEnv == "" {
				missing = "notify.bark.key_env"
			}
		case "webhook":
			if n.Webhook.URL == "" {
				missing = "notify.webhook.url"
			}
		case "email":
			switch {
			case n.Email.Host == "":
				missing = "notify.email.host"
			case n.Email.From == "":
				missing = "notify.email.from"
			case len(n.Email.To) == 0:
				missing = "notify.email.to"
			}
		default:
			return fmt.Errorf("notify.enabled[%d]: %q is not one of %s", i, name, strings.Join(Channels, ", "))
		}
		if missing != "" {
			return fmt.Errorf("notify.enabled lists %q but %s is empty", name, missing)
		}
	}
	return nil
}
