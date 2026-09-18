package claudechannel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/TomwaltW/ferry/internal/control"
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

// agentName is what an external session started by this shim reports as its
// agent. The session is Claude Code's, not one Ferry launched.
const agentName = "claude-code"

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
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	log := opts.Log
	if log == nil {
		// Stdout carries the protocol, so the log never goes there.
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	st, session, platform := attach(ctx, opts.Home, log)
	s := newShim(shimOptions{
		Stream:   st,
		Session:  session,
		Platform: platform,
		In:       in,
		Out:      out,
		Log:      log,
	})
	return s.run(ctx)
}

// attach registers an external session with the daemon. A daemon that is not
// running is not an error: the shim carries on without one, which is what the
// no-daemon instructions describe.
func attach(ctx context.Context, home string, log *slog.Logger) (stream, string, string) {
	cli, err := control.Dial(home)
	if err != nil {
		if !errors.Is(err, fsp.ErrNotFound) {
			log.Error("claudechannel: reach the daemon", "error", err)
		} else {
			log.Info("claudechannel: no daemon is running; serving an idle channel")
		}
		return nil, "", ""
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = home
	}
	st, resp, err := cli.Attach(ctx, control.AttachRequest{
		Kind:  control.KindChannel,
		Agent: agentName,
		Cwd:   cwd,
		PID:   os.Getpid(),
		// Claude Code is driven from IM, and its permission prompts are what
		// the relay exists for, so both are on.
		AllowInject:   true,
		RemoteApprove: true,
	})
	if err != nil {
		log.Error("claudechannel: attach to the daemon", "error", err)
		_ = cli.Close()
		return nil, "", ""
	}
	log.Info("claudechannel: attached", "session", resp.SessionID, "slot", resp.Slot)
	return st, resp.SessionID, platformOf(resp.Endpoint)
}
