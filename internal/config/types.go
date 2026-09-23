package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
	// P3 entry points: a second IM platform, third-party channel servers and
	// where the external sessions of proxy, channel and run report to.
	Feishu         FeishuConfig          `toml:"feishu"`
	ChannelServers []ChannelServerConfig `toml:"channel_servers"`
	External       ExternalConfig        `toml:"external"`
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

// FeishuConfig is the second IM entry point. The app secret never reaches this
// file: ferry login feishu puts it in the keyring under an account of its own
// for each FERRY_HOME ("feishu@" plus a hash of the home), or in
// <FERRY_HOME>/credentials/feishu.json where there is no keyring.
type FeishuConfig struct {
	Enabled     bool     `toml:"enabled"`
	AppID       string   `toml:"app_id"`
	OwnerOpenID string   `toml:"owner_open_id"`
	Approvers   []string `toml:"approvers"` // "owner" or concrete open IDs; defaults to ["owner"]
	Domain      string   `toml:"domain"`    // feishu | lark, defaults to feishu
}

// The two domains the Feishu SDK can talk to.
const (
	FeishuDomainFeishu = "feishu"
	FeishuDomainLark   = "lark"
)

// ChannelServerConfig launches a third-party Claude Code channel server and
// treats it as an IM entry point on platform "channel-<name>".
type ChannelServerConfig struct {
	Name       string            `toml:"name"` // the platform is channel-<name>
	Command    []string          `toml:"command"`
	Env        map[string]string `toml:"env"`
	Owner      string            `toml:"owner"`        // sender ID treated as the owner; no default, unset means no owner
	UserIDMeta string            `toml:"user_id_meta"` // meta key carrying the sender ID; defaults to user_id
	ChatIDMeta string            `toml:"chat_id_meta"` // meta key carrying the reply target; defaults to chat_id
	ReplyTool  string            `toml:"reply_tool"`   // defaults to reply
	// Approvers governs this entry point alone (spec-p4-b1 3.3): "owner" or
	// sender IDs as this server reports them; defaults to ["owner"]. Until P4
	// a channel server borrowed the [wechat] list.
	Approvers []string `toml:"approvers"`
}

// ExternalConfig says which entry point the sessions of ferry proxy, ferry
// channel and ferry run report to.
type ExternalConfig struct {
	BindPlatform string `toml:"bind_platform"` // defaults to wechat
}

type WorkspaceConfig struct {
	Roots []string `toml:"roots"`
	// UsersRoot turns on per-user workspaces (spec-p4-b1 3.4): a paired user
	// who is not the bot owner and has no [[workspace.users]] entry works in
	// <users_root>/<UserDirName> instead of in roots. Empty keeps the P1–P3
	// behaviour, where every paired user shares roots.
	UsersRoot string `toml:"users_root"`
	// Users gives named users roots of their own. An entry wins over
	// users_root; the bot owner always works in roots.
	Users []WorkspaceUser `toml:"users"`
}

// WorkspaceUser is one [[workspace.users]] entry.
type WorkspaceUser struct {
	Platform string   `toml:"platform"` // wechat | feishu | channel-<name>
	ID       string   `toml:"id"`       // the sender ID on that platform
	Roots    []string `toml:"roots"`
}

type AgentConfig struct {
	Name     string            `toml:"name"`
	Registry string            `toml:"registry"` // one of registry, command or server
	Command  []string          `toml:"command"`
	Env      map[string]string `toml:"env"`
	Server   *ServerConfig     `toml:"server"`  // set to drive an already running local service
	Handoff  *bool             `toml:"handoff"` // join terminal session discovery; defaults to true when a handoff profile exists
	// ACPVersion selects the protocol the ACP driver speaks: v1, v2, or auto
	// to try v2 and fall back to v1 when the agent negotiates it. Empty is v1.
	ACPVersion string `toml:"acp_version"`
}

// The values AgentConfig.ACPVersion accepts.
const (
	ACPVersionV1   = "v1"
	ACPVersionV2   = "v2"
	ACPVersionAuto = "auto"
)

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

// Defaults for the fields spec-p3 3.2 leaves optional. Default() and decode()
// live in config.go, which spec-p3 3.11 assigns to no task this round, so the
// P3 readers of these tables apply the defaults themselves through
// WithDefaults.
const (
	DefaultFeishuDomain = FeishuDomainFeishu
	DefaultBindPlatform = string(fsp.PlatformWeChat)
	// DefaultChannelOwner is the synthetic sender ID given to a message that
	// names no sender, and the value to write as owner = "local" to make such
	// messages the owner's. It is no longer any server's default owner.
	DefaultChannelOwner      = "local"
	DefaultChannelUserIDMeta = "user_id"
	DefaultChannelChatIDMeta = "chat_id"
	DefaultChannelReplyTool  = "reply"
	DefaultACPVersion        = ACPVersionV1
)

// ChannelPlatformPrefix precedes a channel server's name in the platform it
// appears as.
const ChannelPlatformPrefix = "channel-"

// ApproverOwner is the approver-list entry that stands for the bot owner, who
// is recognised on every endpoint of its bot (spec-p4-b1 3.3).
const ApproverOwner = "owner"

// userDirHashLen is how many hex digits of the SHA-256 end a user directory
// name: 64 bits, enough that no two users are given the same directory.
const userDirHashLen = 16

// userDirSafeMax bounds the readable half of a user directory name.
const userDirSafeMax = 32

// UserDirName is where a paired user works under workspace.users_root
// (spec-p4-b1 3.4): "<platform>/<safe>-<hash>", with safe the ID with every
// byte outside [A-Za-z0-9._-], and a leading ".", replaced by "_" and cut to
// 32 bytes, and hash the first 16 hex digits of the SHA-256 of
// "<platform>:<id>". The hash keeps apart two IDs that sanitize alike, and
// since it ends the name, no ID can make it "." or "..". The layout is on
// disk, so it never changes.
func UserDirName(platform fsp.Platform, id string) string {
	safe := []byte(id)
	for i, c := range safe {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		case c == '.' && i > 0:
		default:
			safe[i] = '_'
		}
	}
	if len(safe) > userDirSafeMax {
		safe = safe[:userDirSafeMax]
	}
	sum := sha256.Sum256([]byte(string(platform) + ":" + id))
	return filepath.Join(string(platform), string(safe)+"-"+hex.EncodeToString(sum[:])[:userDirHashLen])
}

// channelServerNameRe is the shape spec-p3 3.2 gives a channel server name. It
// also has to survive being pasted into a platform string.
var channelServerNameRe = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)

// WithDefaults fills in the optional fields, so that a caller never has to
// repeat the fallbacks.
func (f FeishuConfig) WithDefaults() FeishuConfig {
	if len(f.Approvers) == 0 {
		f.Approvers = []string{"owner"}
	}
	if f.Domain == "" {
		f.Domain = DefaultFeishuDomain
	}
	return f
}

// WithDefaults fills in the meta keys, the reply tool and the approvers. The
// owner is kept as configured and never defaulted: a server with no owner set
// has no owner.
func (s ChannelServerConfig) WithDefaults() ChannelServerConfig {
	if s.UserIDMeta == "" {
		s.UserIDMeta = DefaultChannelUserIDMeta
	}
	if s.ChatIDMeta == "" {
		s.ChatIDMeta = DefaultChannelChatIDMeta
	}
	if s.ReplyTool == "" {
		s.ReplyTool = DefaultChannelReplyTool
	}
	if len(s.Approvers) == 0 {
		s.Approvers = []string{ApproverOwner}
	}
	return s
}

// Platform is the entry point this channel server appears as.
func (s ChannelServerConfig) Platform() fsp.Platform {
	return fsp.Platform(ChannelPlatformPrefix + s.Name)
}

// WithDefaults fills in the platform external sessions bind to.
func (e ExternalConfig) WithDefaults() ExternalConfig {
	if e.BindPlatform == "" {
		e.BindPlatform = DefaultBindPlatform
	}
	return e
}

// ACPVersionOrDefault is the protocol the agent speaks, v1 unless it says
// otherwise.
func (a AgentConfig) ACPVersionOrDefault() string {
	if a.ACPVersion == "" {
		return DefaultACPVersion
	}
	return a.ACPVersion
}

// EnabledPlatforms lists the entry points the daemon serves, in start order.
func (c Config) EnabledPlatforms() []fsp.Platform {
	out := make([]fsp.Platform, 0, 2+len(c.ChannelServers))
	if c.WeChat.Enabled {
		out = append(out, fsp.PlatformWeChat)
	}
	if c.Feishu.Enabled {
		out = append(out, fsp.PlatformFeishu)
	}
	for _, s := range c.ChannelServers {
		out = append(out, s.Platform())
	}
	return out
}

// ApproversFor is the approver list that governs one entry point
// (spec-p4-b1 3.3): the [wechat] list for wechat, the [feishu] list for
// feishu, and the list of the named server for channel-<name>. A table that
// leaves the list out gives ["owner"], and so does a platform no table
// describes — the fake platform of the tests, a channel server nobody
// configured — so that no entry point ends up where nobody may approve
// anything and nothing says why. An empty list written in the file is refused
// by validation rather than read as either.
func (c Config) ApproversFor(p fsp.Platform) []string {
	var list []string
	switch {
	case p == fsp.PlatformWeChat:
		list = c.WeChat.Approvers
	case p == fsp.PlatformFeishu:
		list = c.Feishu.Approvers
	case strings.HasPrefix(string(p), ChannelPlatformPrefix):
		for _, s := range c.ChannelServers {
			if s.Platform() == p {
				list = s.Approvers
				break
			}
		}
	}
	if len(list) == 0 {
		return []string{ApproverOwner}
	}
	return slices.Clone(list)
}

// validApprovers checks one approver list as written in the file: left out is
// fine, spelled out it must name somebody, and nobody by an empty ID, which
// would match a sender that has none.
func validApprovers(field string, list []string) error {
	if list != nil && len(list) == 0 {
		return fmt.Errorf("%s: an empty list leaves nobody to approve; leave it out for [%q]", field, ApproverOwner)
	}
	for i, who := range list {
		if strings.TrimSpace(who) == "" {
			return fmt.Errorf("%s[%d]: empty", field, i)
		}
	}
	return nil
}

// ValidateEntries checks the P3 entry point tables the way spec-p3 3.2
// prescribes. Config.Validate lives in config.go, which no P3 task owns, so
// the caller that reads the configuration runs this alongside it.
func (c Config) ValidateEntries() error {
	if c.Feishu.Enabled {
		if c.Feishu.AppID == "" {
			return fmt.Errorf("feishu.app_id: required while feishu.enabled is true")
		}
		if c.Feishu.OwnerOpenID == "" {
			return fmt.Errorf("feishu.owner_open_id: required while feishu.enabled is true")
		}
		switch c.Feishu.Domain {
		case "", FeishuDomainFeishu, FeishuDomainLark:
		default:
			return fmt.Errorf("feishu.domain: %q is neither %q nor %q", c.Feishu.Domain, FeishuDomainFeishu, FeishuDomainLark)
		}
		if err := validApprovers("feishu.approvers", c.Feishu.Approvers); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(c.ChannelServers))
	for i, s := range c.ChannelServers {
		if !channelServerNameRe.MatchString(s.Name) {
			return fmt.Errorf("channel_servers[%d].name: %q does not match %s", i, s.Name, channelServerNameRe)
		}
		if seen[s.Name] {
			return fmt.Errorf("channel_servers[%d].name: %q is used more than once", i, s.Name)
		}
		seen[s.Name] = true
		if len(s.Command) == 0 {
			return fmt.Errorf("channel_servers[%d].command: %q has none", i, s.Name)
		}
		if err := validApprovers(fmt.Sprintf("channel_servers[%d].approvers", i), s.Approvers); err != nil {
			return err
		}
	}
	for i, a := range c.Agents {
		switch a.ACPVersion {
		case "", ACPVersionV1, ACPVersionV2, ACPVersionAuto:
		default:
			return fmt.Errorf("agents[%d].acp_version: %q is none of %s, %s and %s", i, a.ACPVersion, ACPVersionV1, ACPVersionV2, ACPVersionAuto)
		}
	}
	// An external session reports to a platform that has to be there when it
	// attaches; an unserved one would silently swallow every mirrored turn.
	bind := fsp.Platform(c.External.WithDefaults().BindPlatform)
	if !slices.Contains(c.EnabledPlatforms(), bind) {
		return fmt.Errorf("external.bind_platform: %q is not an enabled platform", bind)
	}
	return nil
}
