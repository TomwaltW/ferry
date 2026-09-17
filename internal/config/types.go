package config

import (
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// Duration is a time.Duration written in TOML as "30s" or "10m".
// The text codec is implemented by the P1 T4 task.
type Duration struct{ time.Duration }

type Config struct {
	Lang         fsp.Lang        `toml:"lang"`
	DefaultAgent string          `toml:"default_agent"`
	WeChat       WeChatConfig    `toml:"wechat"`
	Workspace    WorkspaceConfig `toml:"workspace"`
	Agents       []AgentConfig   `toml:"agents"`
	Policy       PolicyConfig    `toml:"policy"`
	Outbox       OutboxConfig    `toml:"outbox"`
	Registry     RegistryConfig  `toml:"registry"`
	Limits       LimitsConfig    `toml:"limits"`
	Handoff      HandoffConfig   `toml:"handoff"`
	Notify       NotifyConfig    `toml:"notify"`
}

type WeChatConfig struct {
	Enabled        bool     `toml:"enabled"`
	BaseURL        string   `toml:"base_url"`
	ChannelVersion string   `toml:"channel_version"`
	Render         string   `toml:"render"`       // plain | raw
	Approvers      []string `toml:"approvers"`    // "owner" or concrete WeChat user IDs
	CDNBaseURL     string   `toml:"cdn_base_url"` // defaults to the openilink CDN base; tests point it at a fake
	MaxMediaMB     int      `toml:"max_media_mb"` // default 20
}

type WorkspaceConfig struct {
	Roots []string `toml:"roots"`
}

type AgentConfig struct {
	Name     string            `toml:"name"`
	Registry string            `toml:"registry"` // one of registry, command or server
	Command  []string          `toml:"command"`
	Env      map[string]string `toml:"env"`
	Server   *ServerConfig     `toml:"server"`  // set to drive an already running local service
	Handoff  *bool             `toml:"handoff"` // join terminal session discovery; defaults to true when a handoff profile exists
}

type ServerConfig struct {
	Kind      string `toml:"kind"`       // opencode | qwen
	URL       string `toml:"url"`        // 127.0.0.1, ::1 or localhost only
	SecretEnv string `toml:"secret_env"` // environment variable holding the password or token, may be empty
}

// HandoffProfile tells Ferry how to resume an agent's session in a terminal
// and how to recognise that agent's running process.
type HandoffProfile struct {
	Agent        string   `toml:"agent"`         // preset name or agent name
	Resume       []string `toml:"resume"`        // e.g. ["kimi", "--resume", "{id}"]; empty means unsupported
	ProcessMatch string   `toml:"process_match"` // regexp matched against the full command line
}

type HandoffConfig struct {
	Profiles     []HandoffProfile `toml:"profiles"`      // overrides and additions to the built-in table
	LookbackDays int              `toml:"lookback_days"` // default 7
}

type PolicyRule struct {
	Kind             []string `toml:"kind"`              // empty means any kind
	OutsideWorkspace *bool    `toml:"outside_workspace"` // unset means paths are not considered
	CommandPattern   string   `toml:"command_pattern"`   // regexp matched against InputPreview; empty means not considered
	Action           string   `toml:"action"`            // allow | ask | deny
}

type PolicyConfig struct {
	Default         string       `toml:"default"`
	Rules           []PolicyRule `toml:"rules"`
	ApprovalTimeout Duration     `toml:"approval_timeout"`
}

type OutboxConfig struct {
	MergeInterval       Duration `toml:"merge_interval"`
	ReplyBudget         int      `toml:"reply_budget"`
	LongOutputThreshold int      `toml:"long_output_threshold"` // counted in runes
	SendTimeout         Duration `toml:"send_timeout"`
	WindowTTL           Duration `toml:"window_ttl"`  // measured lifetime minus 30 minutes; 23h while unmeasured
	DigestMax           int      `toml:"digest_max"`  // how many lines a digest lists, default 5
	FileKeep            int      `toml:"file_keep"`   // long-output files kept per endpoint, default 20
	LongOutput          string   `toml:"long_output"` // file | split, default file
}

type NotifyConfig struct {
	Enabled     []string      `toml:"enabled"`      // ntfy bark webhook email; empty by default
	Events      []string      `toml:"events"`       // default approval_pending, turn_finished, im_offline
	MinInterval Duration      `toml:"min_interval"` // default 10m
	Ntfy        NtfyConfig    `toml:"ntfy"`
	Bark        BarkConfig    `toml:"bark"`
	Webhook     WebhookConfig `toml:"webhook"`
	Email       EmailConfig   `toml:"email"`
}

type NtfyConfig struct {
	URL      string `toml:"url"` // default https://ntfy.sh
	Topic    string `toml:"topic"`
	TokenEnv string `toml:"token_env"` // when set, sent as Authorization: Bearer
}

type BarkConfig struct {
	URL    string `toml:"url"`     // default https://api.day.app
	KeyEnv string `toml:"key_env"` // environment variable holding the device key
}

type WebhookConfig struct {
	URL       string `toml:"url"`
	SecretEnv string `toml:"secret_env"` // when set, X-Ferry-Signature carries the HMAC-SHA256 of the body in hex
}

type EmailConfig struct {
	Host        string   `toml:"host"`
	Port        int      `toml:"port"` // default 587, STARTTLS
	User        string   `toml:"user"`
	PasswordEnv string   `toml:"password_env"`
	From        string   `toml:"from"`
	To          []string `toml:"to"`
}

type RegistryConfig struct {
	SnapshotOnly bool   `toml:"snapshot_only"`
	NPMRegistry  string `toml:"npm_registry"`
	GitHubProxy  string `toml:"github_proxy"`
}

type LimitsConfig struct {
	MaxManagedSessions int `toml:"max_managed_sessions"`
}
