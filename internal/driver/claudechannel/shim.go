package claudechannel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ids"
	"github.com/TomwaltW/ferry/internal/mcpmini"
)

// serverName is what Claude Code prints as the source attribute of an inbound
// tag: <channel source="ferry" ...>.
const serverName = "ferry"

// defaultPlatform labels a message whose entry point the daemon did not name.
const defaultPlatform = serverName

// instructionsWithDaemon is the fixed English text Claude Code delivers to the
// model when the server connects. It says where messages come from, how to
// answer one, and how long an answer should be.
const instructionsWithDaemon = "Messages from Ferry arrive as <channel source=\"" + serverName + "\" ...> events: the tag's attributes carry chat_id, message_id, user, user_id, platform and ts, and the body is what the person wrote. " +
	"To answer, call the reply tool and pass back the chat_id from the tag along with your text. " +
	"Keep replies short: the person is reading them on a phone."

// instructionsWithoutDaemon replaces it when no daemon is running. The model
// is told up front that nothing will arrive and nothing can be sent, which is
// the only way it can know: the handshake itself still succeeds.
const instructionsWithoutDaemon = "The Ferry daemon is not running, so this channel is idle: no messages will arrive, the reply tool cannot deliver anything, and permission requests are not relayed. " +
	"Start the daemon with `ferry daemon` and restart this session to use it."

// replyResultText is what the reply tool answers with once the message is on
// its way to the outbox.
const replyResultText = "sent"

// replySchema is the reply tool's input schema. chat_id and text are the
// protocol's own fields; files is Ferry's optional extension for absolute
// paths the model wants to send along.
var replySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "chat_id": {"type": "string", "description": "The conversation to reply in, taken from the inbound tag"},
    "text": {"type": "string", "description": "The message to send"},
    "files": {"type": "array", "items": {"type": "string"}, "description": "Absolute paths to send along with the message"}
  },
  "required": ["chat_id", "text"]
}`)

// stream is the daemon side of the shim. control.Stream satisfies it; tests
// put their own in its place.
type stream interface {
	Send(m control.Message) error
	Recv() (control.Message, error)
	Close() error
}

// replyArgs is what the model passes to the reply tool.
type replyArgs struct {
	ChatID string   `json:"chat_id"`
	Text   string   `json:"text"`
	Files  []string `json:"files,omitempty"`
}

// inboundParams is notifications/claude/channel: a message on its way to the
// model.
type inboundParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// permissionRequestParams is notifications/claude/channel/permission_request,
// sent by Claude Code when it wants a tool call approved remotely.
type permissionRequestParams struct {
	RequestID    string `json:"request_id"`
	ToolName     string `json:"tool_name"`
	Description  string `json:"description"`
	InputPreview string `json:"input_preview"`
}

// permissionParams is notifications/claude/channel/permission, the verdict
// going back.
type permissionParams struct {
	RequestID string `json:"request_id"`
	Behavior  string `json:"behavior"`
}

// shimOptions assembles one shim. A nil Stream is the no-daemon case.
type shimOptions struct {
	Stream   stream
	Session  string // the chat_id the model sees, empty without a daemon
	Platform string // the entry point messages come from
	In       io.Reader
	Out      io.Writer
	Clock    clock.Clock
	Log      *slog.Logger
}

// shim is `ferry channel`: an MCP server on one side, the daemon's control
// stream on the other.
type shim struct {
	st       stream
	session  string
	platform string
	clk      clock.Clock
	log      *slog.Logger

	srv *mcpmini.Server

	// newMessageID is overridden in tests so that meta is comparable.
	newMessageID func() string

	mu   sync.Mutex
	sent []control.Message
}

func newShim(opts shimOptions) *shim {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	platform := opts.Platform
	if platform == "" {
		platform = defaultPlatform
	}

	s := &shim{
		st:           opts.Stream,
		session:      opts.Session,
		platform:     platform,
		clk:          clk,
		log:          log,
		newMessageID: func() string { return ids.New("cmsg") },
	}

	s.srv = mcpmini.NewServer(mcpmini.ServerOptions{
		Info:         mcpmini.Info{Name: serverName, Version: ServerVersion},
		Instructions: s.instructions(),
		Experimental: s.experimental(),
		Tools: []mcpmini.Tool{{
			Name:        ToolReply,
			Description: "Send a message back over this channel",
			InputSchema: replySchema,
		}},
		Call:     s.call,
		OnNotify: s.onNotify,
		In:       opts.In,
		Out:      opts.Out,
		Log:      log,
	})
	return s
}

// hasDaemon reports whether there is a daemon to relay to.
func (s *shim) hasDaemon() bool { return s.st != nil }

// experimental is the capability block. The permission capability may only be
// declared by a channel that authenticates its senders — Ferry's ACL does —
// and only when there is a daemon to put the question to.
func (s *shim) experimental() map[string]any {
	caps := map[string]any{CapChannel: map[string]any{}}
	if s.hasDaemon() {
		caps[CapChannelPermission] = map[string]any{}
	}
	return caps
}

func (s *shim) instructions() string {
	if !s.hasDaemon() {
		return instructionsWithoutDaemon
	}
	return instructionsWithDaemon
}

// run serves the MCP session and pumps the daemon's messages into it until
// either side goes away.
func (s *shim) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var pumped chan error
	if s.hasDaemon() {
		pumped = make(chan error, 1)
		go func() { pumped <- s.pump(ctx) }()
	}

	err := s.srv.Run(ctx)

	if s.hasDaemon() {
		// The model's session ended; tell the daemon before going away.
		_ = s.st.Send(control.Message{Type: control.MsgDetach, SessionID: s.session})
		_ = s.st.Close()
		cancel()
		select {
		case perr := <-pumped:
			if err == nil && perr != nil && !errors.Is(perr, io.EOF) {
				err = perr
			}
		case <-time.After(time.Second):
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// pump turns what the daemon sends into channel notifications.
func (s *shim) pump(ctx context.Context) error {
	for {
		msg, err := s.st.Recv()
		if err != nil {
			return err
		}
		switch msg.Type {
		case control.MsgPrompt:
			if err := s.deliver(ctx, msg); err != nil {
				return err
			}
		case control.MsgVerdict:
			if err := s.relayVerdict(ctx, msg); err != nil {
				return err
			}
		case control.MsgClose, control.MsgCancel:
			// Closing the model's session is not ours to do: Claude Code owns
			// its own lifecycle, so the shim only stops relaying.
			return nil
		default:
			s.log.Debug("claudechannel: ignoring a message the shim has no use for", "type", msg.Type)
		}
	}
}

// deliver pushes one IM message to the model.
func (s *shim) deliver(ctx context.Context, msg control.Message) error {
	sender := msg.Sender
	if sender == "" {
		sender = defaultPlatform
	}
	return s.srv.Notify(ctx, NotifyChannel, inboundParams{
		Content: msg.Text,
		Meta: map[string]string{
			MetaChatID:    s.session,
			MetaMessageID: s.newMessageID(),
			MetaUser:      sender,
			MetaUserID:    sender,
			MetaPlatform:  s.platform,
			MetaTimestamp: strconv.FormatInt(s.clk.Now().Unix(), 10),
		},
	})
}

// relayVerdict turns the daemon's answer into the notification Claude Code
// waits for. The request_id is the one Claude Code issued: the approval module
// keeps it, so the two sides name the same request throughout.
func (s *shim) relayVerdict(ctx context.Context, msg control.Message) error {
	id := msg.ReqID
	if id == "" && msg.Verdict != nil {
		id = msg.Verdict.ApprovalID
	}
	if id == "" {
		s.log.Warn("claudechannel: a verdict arrived without a request ID")
		return nil
	}
	return s.srv.Notify(ctx, NotifyPermission, permissionParams{
		RequestID: id,
		Behavior:  behaviorFor(msg.Verdict),
	})
}

// behaviorFor maps a Ferry verdict onto the two words the protocol has.
// Anything that is not an outright allow — a refusal, a timeout, a
// cancellation with no option chosen — is a deny.
func behaviorFor(v *fsp.Verdict) string {
	if v == nil {
		return BehaviorDeny
	}
	switch v.Decision {
	case fsp.DecisionAllowOnce, fsp.DecisionAllowAlways:
		if v.OptionID == "" {
			// An allow that picked no option is a cancellation in disguise.
			return BehaviorDeny
		}
		return BehaviorAllow
	default:
		return BehaviorDeny
	}
}

// onNotify handles what Claude Code pushes at the shim. The permission request
// is the only notification the protocol defines in this direction.
func (s *shim) onNotify(_ context.Context, method string, params json.RawMessage) {
	if method != NotifyPermissionRequest {
		return
	}
	if !s.hasDaemon() {
		// The capability was never declared, so this should not arrive; if it
		// does, there is nobody to ask.
		s.log.Warn("claudechannel: a permission request arrived without a daemon")
		return
	}
	var p permissionRequestParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.log.Error("claudechannel: unparsable permission request", "error", err)
		return
	}
	if p.RequestID == "" {
		s.log.Warn("claudechannel: a permission request arrived without a request_id")
		return
	}
	req := fsp.ApprovalRequest{
		ID:           p.RequestID,
		Session:      fsp.SessionID(s.session),
		ToolName:     p.ToolName,
		Description:  p.Description,
		InputPreview: p.InputPreview,
		// Claude Code knows only allow and deny. The approval module grants a
		// "y" by picking an allow option, so both have to be on offer; their
		// IDs are the behaviours relayVerdict sends back.
		Options: []fsp.ApprovalOption{
			{ID: BehaviorAllow, Kind: fsp.OptAllowOnce, Label: "Allow"},
			{ID: BehaviorDeny, Kind: fsp.OptRejectOnce, Label: "Deny"},
		},
		CreatedAt: s.clk.Now(),
	}
	if err := s.send(control.Message{
		Type:      control.MsgApprovalRequest,
		SessionID: s.session,
		ReqID:     p.RequestID,
		Approval:  &req,
	}); err != nil {
		s.log.Error("claudechannel: relay a permission request", "id", p.RequestID, "error", err)
	}
}

// call runs one tool call. reply is the only tool the shim offers.
func (s *shim) call(_ context.Context, name string, args json.RawMessage) (mcpmini.ToolResult, error) {
	if name != ToolReply {
		return toolError("there is no tool named " + name), nil
	}
	var a replyArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return toolError("the arguments are not valid: " + err.Error()), nil
	}
	if !s.hasDaemon() {
		return toolError("the Ferry daemon is not running, so this reply cannot be delivered"), nil
	}
	if a.ChatID != s.session || s.session == "" {
		// An unknown chat is a tool error, not a send to nowhere: the model
		// must see that the message did not go out.
		return toolError("unknown chat_id " + strconv.Quote(a.ChatID) + "; use the chat_id from the inbound tag"), nil
	}

	now := s.clk.Now()
	// The control protocol has no event for an attachment in P3, so a path the
	// model wants to send travels as a line of the reply's own text.
	text := a.Text
	for _, f := range a.Files {
		text += "\n" + f
	}
	if err := s.send(control.Message{
		Type:      control.MsgEvent,
		SessionID: s.session,
		Event:     &fsp.Event{Session: fsp.SessionID(s.session), Kind: fsp.EvChunk, Text: text, At: now},
	}); err != nil {
		return toolError("the reply could not be handed to the daemon: " + err.Error()), nil
	}
	if err := s.send(control.Message{
		Type:      control.MsgEvent,
		SessionID: s.session,
		Event:     &fsp.Event{Session: fsp.SessionID(s.session), Kind: fsp.EvTurnEnd, StopReason: "end_turn", At: now},
	}); err != nil {
		return toolError("the reply could not be completed: " + err.Error()), nil
	}
	return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: replyResultText}}}, nil
}

// send writes to the daemon and keeps a copy, which is what the tests read
// instead of reaching into the stream.
func (s *shim) send(m control.Message) error {
	if err := s.st.Send(m); err != nil {
		return err
	}
	s.mu.Lock()
	s.sent = append(s.sent, m)
	s.mu.Unlock()
	return nil
}

func toolError(text string) mcpmini.ToolResult {
	return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: text}}, IsError: true}
}

// platformOf reads the platform out of an endpoint string such as
// "wechat:bot1:userA". An endpoint the daemon left empty falls back to the
// server's own name.
func platformOf(endpoint string) string {
	platform, _, found := strings.Cut(endpoint, ":")
	if !found || platform == "" {
		return defaultPlatform
	}
	return platform
}

// ServerVersion is what the handshake reports for this server. The CLI's own
// version is injected at release time and is not reachable from here; the
// protocol only needs something stable to name the implementation.
const ServerVersion = "0.3"
