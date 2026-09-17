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
}

type WeChatConfig struct {
	Enabled        bool     `toml:"enabled"`
	BaseURL        string   `toml:"base_url"`
	ChannelVersion string   `toml:"channel_version"`
	Render         string   `toml:"render"`    // plain | raw
	Approvers      []string `toml:"approvers"` // "owner" or concrete WeChat user IDs
}

type WorkspaceConfig struct {
	Roots []string `toml:"roots"`
}

type AgentConfig struct {
	Name     string            `toml:"name"`
	Registry string            `toml:"registry"` // either this or Command
	Command  []string          `toml:"command"`
	Env      map[string]string `toml:"env"`
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
}

type RegistryConfig struct {
	SnapshotOnly bool   `toml:"snapshot_only"`
	NPMRegistry  string `toml:"npm_registry"`
	GitHubProxy  string `toml:"github_proxy"`
}

type LimitsConfig struct {
	MaxManagedSessions int `toml:"max_managed_sessions"`
}
