package claudechannel

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// The channels protocol as the official reference spells it. These names cross
// the wire to Claude Code, so none of them may be renamed.
const (
	// CapChannel and CapChannelPermission go into capabilities.experimental.
	// Only a channel that authenticates its senders may declare the second
	// one; Ferry's ACL does.
	CapChannel           = "claude/channel"
	CapChannelPermission = "claude/channel/permission"

	// NotifyChannel carries one inbound message, with params content (a
	// string) and meta (a map of string to string).
	NotifyChannel = "notifications/claude/channel"
	// NotifyPermissionRequest comes from the host, with params request_id,
	// tool_name, description and input_preview.
	NotifyPermissionRequest = "notifications/claude/channel/permission_request"
	// NotifyPermission goes back to the host, with params request_id and
	// behavior.
	NotifyPermission = "notifications/claude/channel/permission"

	// ToolReply is the one tool the shim offers: chat_id, text and an optional
	// files array of absolute paths.
	ToolReply = "reply"
)

// The values NotifyPermission's behavior takes.
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
)

// The meta keys the protocol agrees on. A key with a hyphen in it is dropped
// silently by the host, so every one of these is letters and underscores.
const (
	MetaChatID    = "chat_id"
	MetaMessageID = "message_id"
	MetaUser      = "user"
	MetaUserID    = "user_id"
	MetaPlatform  = "platform"
	MetaTimestamp = "ts"
)

// Options is what `ferry channel` is started with: Claude Code runs it as a
// stdio MCP server.
type Options struct {
	Home string
	In   io.Reader
	Out  io.Writer
	Log  *slog.Logger
}

// Run serves the MCP session on In/Out and relays it to the daemon over an
// attach(kind=channel) connection. Without a daemon it still completes the
// handshake, but declares no permission capability and says so in its
// instructions.
func Run(ctx context.Context, opts Options) error {
	return fmt.Errorf("claudechannel: Run: %w", fsp.ErrUnsupported)
}
