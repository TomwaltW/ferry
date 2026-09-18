// Command echo-channel is a minimal third-party channel server, written with
// mcpmini, for the end-to-end tests of Ferry's compatibility mode.
//
// Its host starts it and owns its stdin and stdout, which carry the MCP
// protocol and nothing else. Everything the test drives goes through files
// instead:
//
//   - ECHO_CHANNEL_CONTENT, when set, is pushed as one channel event as soon
//     as the handshake is over. It is the whole setup a simple test needs.
//   - ECHO_CHANNEL_IN, when set, is a file the server watches. Every line
//     appended to it becomes one more channel event, so a test can keep
//     talking after the session is up.
//   - ECHO_CHANNEL_OUT, when set, is where the reply tool appends what it was
//     asked to send, one "chat_id<TAB>text" line per call. Unset sends them to
//     stderr, which the host logs.
//
// The chat it speaks for is ECHO_CHANNEL_CHAT (default "chat-1") and the
// sender it reports is ECHO_CHANNEL_USER (default "u-1"). The reply tool is
// named by ECHO_CHANNEL_TOOL (default "reply"), so a test can also check that
// a server which named its tool something else is still reachable.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TomwaltW/ferry/internal/mcpmini"
)

// The environment is the whole configuration surface: the tests start this
// process, so flags would only be in the way.
const (
	envContent = "ECHO_CHANNEL_CONTENT"
	envIn      = "ECHO_CHANNEL_IN"
	envOut     = "ECHO_CHANNEL_OUT"
	envChat    = "ECHO_CHANNEL_CHAT"
	envUser    = "ECHO_CHANNEL_USER"
	envTool    = "ECHO_CHANNEL_TOOL"
)

const (
	defaultChat = "chat-1"
	defaultUser = "u-1"
	defaultTool = "reply"
)

// pollInterval is how often the watched file is checked for new lines. It is
// short because the tests wait on it; nothing in production reads this file.
const pollInterval = 20 * time.Millisecond

// The channel protocol's names, spelled out here rather than imported, so that
// this server reads like the third-party one it stands for.
const (
	capChannel    = "claude/channel"
	notifyChannel = "notifications/claude/channel"
)

const instructions = "Messages arrive as <channel source=\"echo\" chat_id=\"...\"> events. " +
	"Reply with the reply tool, passing back the chat_id from the tag."

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "echo-channel:", err)
		os.Exit(1)
	}
}

func run() error {
	chat := envOr(envChat, defaultChat)
	user := envOr(envUser, defaultUser)
	tool := envOr(envTool, defaultTool)

	sink, closeSink, err := openSink()
	if err != nil {
		return err
	}
	defer closeSink()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var counter atomic.Int64
	var srv *mcpmini.Server

	// push sends one inbound message, numbering it as a real platform would.
	push := func(content string) {
		n := counter.Add(1)
		_ = srv.Notify(ctx, notifyChannel, map[string]any{
			"content": content,
			"meta": map[string]string{
				"chat_id":    chat,
				"message_id": "m-" + strconv.FormatInt(n, 10),
				"user":       user,
				"user_id":    user,
				"platform":   "echo",
				"ts":         strconv.FormatInt(time.Now().Unix(), 10),
			},
		})
	}

	srv = mcpmini.NewServer(mcpmini.ServerOptions{
		Info:         mcpmini.Info{Name: "echo", Version: "1"},
		Instructions: instructions,
		Experimental: map[string]any{capChannel: map[string]any{}},
		Tools: []mcpmini.Tool{{
			Name:        tool,
			Description: "Send a message back over this channel",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "chat_id": {"type": "string"},
    "text": {"type": "string"}
  },
  "required": ["chat_id", "text"]
}`),
		}},
		Call: func(_ context.Context, name string, args json.RawMessage) (mcpmini.ToolResult, error) {
			if name != tool {
				return toolError("there is no tool named " + name), nil
			}
			var a struct {
				ChatID string `json:"chat_id"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return toolError("the arguments are not valid: " + err.Error()), nil
			}
			if a.ChatID != chat {
				return toolError("unknown chat_id " + strconv.Quote(a.ChatID)), nil
			}
			// One line per reply, so a test can read them as they arrive. The
			// text is written on a single line: a reply with newlines in it
			// would otherwise look like several replies.
			line := a.ChatID + "\t" + strings.ReplaceAll(a.Text, "\n", "\\n") + "\n"
			if _, err := sink.WriteString(line); err != nil {
				return toolError("the reply could not be written: " + err.Error()), nil
			}
			return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: "sent"}}}, nil
		},
		OnNotify: func(_ context.Context, method string, _ json.RawMessage) {
			if method != mcpmini.NotifyInitialized {
				return
			}
			// The host is ready; anything sent before this would be dropped.
			if seed := os.Getenv(envContent); seed != "" {
				push(seed)
			}
			if path := os.Getenv(envIn); path != "" {
				go watch(ctx, path, push)
			}
		},
		In:  os.Stdin,
		Out: os.Stdout,
	})

	return srv.Run(ctx)
}

// watch turns every line appended to path into one inbound message. It polls
// rather than depending on a file watcher: this is a test fixture, and a
// dependency here would reach the whole module.
func watch(ctx context.Context, path string, push func(string)) {
	var offset int64
	var pending string
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, 0); err != nil {
			_ = f.Close()
			continue
		}
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				offset += int64(n)
				pending += string(buf[:n])
			}
			if err != nil {
				break
			}
		}
		_ = f.Close()

		// Only whole lines are messages; a partial write waits for its newline.
		for {
			line, rest, found := strings.Cut(pending, "\n")
			if !found {
				break
			}
			pending = rest
			if line != "" {
				push(line)
			}
		}
	}
}

// openSink is where replies go: stdout is the protocol, so they cannot go
// there.
func openSink() (*os.File, func(), error) {
	path := os.Getenv(envOut)
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

func toolError(text string) mcpmini.ToolResult {
	return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: text}}, IsError: true}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
