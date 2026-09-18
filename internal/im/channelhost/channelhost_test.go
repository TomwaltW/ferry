package channelhost

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/driver/claudechannel"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/mcpmini"
	"github.com/TomwaltW/ferry/internal/ports"
)

// waitFor bounds the test's own plumbing, not any business timeout: the
// restart interval itself is driven by a fake clock.
const waitFor = 10 * time.Second

var epoch = time.Date(2026, time.September, 18, 9, 0, 0, 0, time.UTC)

// The helper process reads its instructions out of the environment.
const (
	envHelper  = "FERRY_TEST_CHANNEL_HELPER"
	envMode    = "FERRY_TEST_CHANNEL_MODE"
	envDir     = "FERRY_TEST_CHANNEL_DIR"
	envTool    = "FERRY_TEST_CHANNEL_TOOL"
	envContent = "FERRY_TEST_CHANNEL_CONTENT"
	envMeta    = "FERRY_TEST_CHANNEL_META"
)

// The helper's two modes: push one message and stay up, or quit as soon as the
// handshake is over.
const (
	modeEcho = "echo"
	modeExit = "exit"
)

// TestHelperChannelServer is not a test: re-executing the test binary with
// FERRY_TEST_CHANNEL_HELPER set is how these tests get a real third-party
// channel server, process boundary and all.
func TestHelperChannelServer(t *testing.T) {
	if os.Getenv(envHelper) != "1" {
		t.Skip("not the helper process")
	}
	dir := os.Getenv(envDir)
	appendLine(filepath.Join(dir, "starts.log"), "started")

	tool := os.Getenv(envTool)
	if tool == "" {
		tool = config.DefaultChannelReplyTool
	}

	var srv *mcpmini.Server
	srv = mcpmini.NewServer(mcpmini.ServerOptions{
		Info:         mcpmini.Info{Name: "helper", Version: "1"},
		Instructions: "a test channel",
		Experimental: map[string]any{claudechannel.CapChannel: map[string]any{}},
		Tools:        []mcpmini.Tool{{Name: tool, Description: "reply"}},
		Call: func(_ context.Context, name string, args json.RawMessage) (mcpmini.ToolResult, error) {
			if name != tool {
				return mcpmini.ToolResult{IsError: true}, nil
			}
			appendLine(filepath.Join(dir, "replies.log"), string(args))
			return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: "sent"}}}, nil
		},
		OnNotify: func(ctx context.Context, method string, _ json.RawMessage) {
			if method != mcpmini.NotifyInitialized {
				return
			}
			if os.Getenv(envMode) == modeExit {
				// A server that dies right after the handshake is what the
				// restart path has to survive.
				os.Exit(1)
			}
			meta := map[string]string{}
			if raw := os.Getenv(envMeta); raw != "" {
				_ = json.Unmarshal([]byte(raw), &meta)
			}
			_ = srv.Notify(ctx, claudechannel.NotifyChannel, map[string]any{
				"content": os.Getenv(envContent),
				"meta":    meta,
			})
		},
		In:  os.Stdin,
		Out: os.Stdout,
	})
	_ = srv.Run(context.Background())
	// Exit before the test framework prints to stdout, which the protocol owns.
	os.Exit(0)
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// helper describes one run of the fake channel server.
type helper struct {
	mode    string
	tool    string
	content string
	meta    map[string]string
}

// harness starts an adapter over a helper process.
type harness struct {
	a   *Adapter
	dir string
	in  chan fsp.Inbound
	clk *clock.Fake
}

func newHarness(t *testing.T, cfg config.ChannelServerConfig, h helper) *harness {
	t.Helper()

	dir := t.TempDir()
	env := map[string]string{
		envHelper:  "1",
		envDir:     dir,
		envMode:    h.mode,
		envTool:    h.tool,
		envContent: h.content,
	}
	if h.meta != nil {
		raw, err := json.Marshal(h.meta)
		if err != nil {
			t.Fatalf("encode meta: %v", err)
		}
		env[envMeta] = string(raw)
	}
	cfg.Command = []string{os.Args[0], "-test.run=TestHelperChannelServer"}
	cfg.Env = env

	clk := clock.NewFake(epoch)
	a, err := New(Options{Config: cfg, Home: dir, Clock: clk})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	in := make(chan fsp.Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_ = a.Run(ctx, in)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ran:
		case <-time.After(waitFor):
			t.Error("Run did not return after the context ended")
		}
	})
	return &harness{a: a, dir: dir, in: in, clk: clk}
}

func (h *harness) next(t *testing.T) fsp.Inbound {
	t.Helper()
	select {
	case msg := <-h.in:
		return msg
	case <-time.After(waitFor):
		t.Fatal("no inbound message arrived from the channel server")
		return fsp.Inbound{}
	}
}

// waitOnline blocks until the adapter reports the server as up.
func (h *harness) waitOnline(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if h.a.Status().Online {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the adapter never came online: %+v", h.a.Status())
}

func TestHostSpawnAndReceive(t *testing.T) {
	h := newHarness(t,
		config.ChannelServerConfig{Name: "demo"},
		helper{
			mode:    modeEcho,
			content: "deploy finished",
			meta: map[string]string{
				claudechannel.MetaChatID:    "chat-7",
				claudechannel.MetaUserID:    "u-42",
				claudechannel.MetaMessageID: "m-1",
			},
		},
	)

	h.waitOnline(t)
	msg := h.next(t)
	if msg.Text != "deploy finished" {
		t.Errorf("text = %q, want the content the server pushed", msg.Text)
	}
	want := fsp.EndpointID{Platform: fsp.Platform("channel-demo"), BotID: "demo", PeerID: "chat-7"}
	if msg.Endpoint != want {
		t.Errorf("endpoint = %+v, want %+v", msg.Endpoint, want)
	}
	if msg.SenderID != "u-42" {
		t.Errorf("sender = %q, want the user_id from meta", msg.SenderID)
	}
	if msg.MessageID != "m-1" {
		t.Errorf("message ID = %q, want the message_id from meta", msg.MessageID)
	}
	if !msg.ReceivedAt.Equal(epoch) {
		t.Errorf("received at %v, want the injected clock's time %v", msg.ReceivedAt, epoch)
	}

	if got := h.a.Platform(); got != fsp.Platform("channel-demo") {
		t.Errorf("platform = %q, want channel-demo", got)
	}
	if h.a.Limits().NeedsWindow {
		t.Error("Limits says a window is needed; a channel server takes a reply whenever one is ready")
	}
}

func TestHostReplyViaTool(t *testing.T) {
	h := newHarness(t,
		// A server that named its tool something else is still reachable.
		config.ChannelServerConfig{Name: "demo", ReplyTool: "send_message"},
		helper{
			mode:    modeEcho,
			tool:    "send_message",
			content: "ping",
			meta:    map[string]string{claudechannel.MetaChatID: "chat-7"},
		},
	)
	msg := h.next(t)

	state, err := h.a.Send(context.Background(), fsp.Outbound{
		Endpoint: msg.Endpoint,
		Kind:     fsp.OutText,
		Text:     "pong",
	}, msg.ReplyCtx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if state != fsp.DeliveryAccepted {
		t.Errorf("state = %q, want %q", state, fsp.DeliveryAccepted)
	}

	lines := readLines(t, filepath.Join(h.dir, "replies.log"))
	if len(lines) != 1 {
		t.Fatalf("the server logged %d calls, want 1: %v", len(lines), lines)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &args); err != nil {
		t.Fatalf("parse the tool arguments: %v", err)
	}
	if args[claudechannel.MetaChatID] != "chat-7" {
		t.Errorf("chat_id = %q, want the chat the message came from", args[claudechannel.MetaChatID])
	}
	if args["text"] != "pong" {
		t.Errorf("text = %q, want %q", args["text"], "pong")
	}

	// A tool the server does not have leaves the message rejected rather than
	// silently accepted.
	bad, err := New(Options{
		Config: config.ChannelServerConfig{Name: "demo", Command: []string{"true"}, ReplyTool: "nope"},
		Clock:  h.clk,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if state, err := bad.Send(context.Background(), fsp.Outbound{Text: "x"}, ""); state != fsp.DeliveryRejected || err == nil {
		t.Errorf("Send on an offline adapter = (%q, %v), want rejected and an error", state, err)
	}
}

func TestHostRestartOnExit(t *testing.T) {
	h := newHarness(t,
		config.ChannelServerConfig{Name: "flaky"},
		helper{mode: modeExit},
	)
	starts := filepath.Join(h.dir, "starts.log")

	// The first run plus RestartLimit restarts, and no more: after that the
	// adapter stays offline rather than spinning.
	want := RestartLimit + 1
	// Each restart waits out RestartInterval on the injected clock, so pushing
	// the clock forward is what drives the next attempt. The clock is polled
	// rather than waited on: once the adapter gives up it stops waiting
	// altogether, and a test that blocked for a waiter would never wake.
	deadline := time.Now().Add(waitFor)
	for len(readLines(t, starts)) < want && time.Now().Before(deadline) {
		h.clk.Advance(RestartInterval)
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(readLines(t, starts)); got != want {
		t.Fatalf("the server was started %d times, want %d", got, want)
	}

	// Having given up, the adapter reports itself offline and says why. The
	// last start is still in flight when its line reaches the log, so the
	// settled state is what to wait for, not merely the first moment the
	// adapter is not online.
	var st ports.AdapterStatus
	settled := time.Now().Add(waitFor)
	for time.Now().Before(settled) {
		if st = h.a.Status(); !st.Online && strings.Contains(st.Detail, "giving up") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.Online {
		t.Error("the adapter still reports itself online after giving up")
	}
	if !strings.Contains(st.Detail, "giving up") {
		t.Errorf("detail = %q, want it to say the adapter gave up", st.Detail)
	}

	// No further attempt follows, even after another interval passes.
	time.Sleep(100 * time.Millisecond)
	if got := len(readLines(t, starts)); got != want {
		t.Errorf("the server was started %d times after giving up, want %d", got, want)
	}
}

func TestHostOwnerMapping(t *testing.T) {
	t.Run("the configured owner is the adapter's owner", func(t *testing.T) {
		h := newHarness(t,
			config.ChannelServerConfig{Name: "demo", Owner: "u-42", UserIDMeta: "sender"},
			helper{
				mode:    modeEcho,
				content: "hi",
				meta:    map[string]string{claudechannel.MetaChatID: "chat-7", "sender": "u-42"},
			},
		)
		msg := h.next(t)
		st := h.a.Status()
		if st.OwnerID != "u-42" {
			t.Errorf("OwnerID = %q, want the configured owner", st.OwnerID)
		}
		if st.BotID != "demo" {
			t.Errorf("BotID = %q, want the server's name", st.BotID)
		}
		if msg.SenderID != st.OwnerID {
			t.Errorf("sender = %q, want it to match the owner %q so the owner's messages are recognised", msg.SenderID, st.OwnerID)
		}
	})

	t.Run("a server that names no sender speaks for the owner", func(t *testing.T) {
		h := newHarness(t,
			// No owner and no user_id_meta configured: both default to local.
			config.ChannelServerConfig{Name: "demo"},
			helper{
				mode:    modeEcho,
				content: "hi",
				meta:    map[string]string{claudechannel.MetaChatID: "chat-7"},
			},
		)
		msg := h.next(t)
		if msg.SenderID != config.DefaultChannelOwner {
			t.Errorf("sender = %q, want %q", msg.SenderID, config.DefaultChannelOwner)
		}
		if got := h.a.Status().OwnerID; got != config.DefaultChannelOwner {
			t.Errorf("OwnerID = %q, want %q", got, config.DefaultChannelOwner)
		}
		if msg.MessageID == "" {
			t.Error("a message the server did not number was given no ID either")
		}
	})

	t.Run("a channel server needs a name and a command", func(t *testing.T) {
		if _, err := New(Options{Config: config.ChannelServerConfig{Command: []string{"true"}}}); err == nil {
			t.Error("New accepted a server with no name")
		}
		if _, err := New(Options{Config: config.ChannelServerConfig{Name: "demo"}}); err == nil {
			t.Error("New accepted a server with no command")
		}
	})
}

// TestHostRunsEchoChannel drives the fixture the end-to-end tests are given,
// built from source. Without this the fixture would ship unexercised: nothing
// under testdata is reached by go build ./... or go vet ./....
func TestHostRunsEchoChannel(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build the fixture with")
	}

	dir := t.TempDir()
	name := "echo-channel"
	if runtime.GOOS == "windows" {
		name += ".exe" // Windows will not exec a file without the extension
	}
	bin := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", bin, "github.com/TomwaltW/ferry/test/e2e/testdata/echo-channel")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the fixture failed: %v\n%s", err, out)
	}

	inbox := filepath.Join(dir, "inbox")
	outbox := filepath.Join(dir, "outbox")
	clk := clock.NewFake(epoch)
	a, err := New(Options{
		Config: config.ChannelServerConfig{
			Name:    "echo",
			Command: []string{bin},
			Env: map[string]string{
				"ECHO_CHANNEL_CONTENT": "deploy finished",
				"ECHO_CHANNEL_IN":      inbox,
				"ECHO_CHANNEL_OUT":     outbox,
				"ECHO_CHANNEL_CHAT":    "chat-1",
			},
		},
		Home:  dir,
		Clock: clk,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	in := make(chan fsp.Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, in) }()

	next := func() fsp.Inbound {
		t.Helper()
		select {
		case msg := <-in:
			return msg
		case <-time.After(waitFor):
			t.Fatal("the fixture pushed no message")
			return fsp.Inbound{}
		}
	}

	// The seed message arrives on its own, which is all B7 needs to start a
	// session.
	first := next()
	if first.Text != "deploy finished" {
		t.Errorf("text = %q, want the seeded content", first.Text)
	}
	if first.Endpoint.PeerID != "chat-1" || first.SenderID != "u-1" {
		t.Errorf("endpoint/sender = %+v / %q, want chat-1 and u-1", first.Endpoint, first.SenderID)
	}

	// A line appended later becomes another message, so a test can keep talking.
	appendLine(inbox, "and now what?")
	if got := next().Text; got != "and now what?" {
		t.Errorf("text = %q, want the appended line", got)
	}

	// The agent's reply goes back through the fixture's own reply tool.
	state, err := a.Send(context.Background(), fsp.Outbound{
		Endpoint: first.Endpoint, Kind: fsp.OutText, Text: "looks good",
	}, first.ReplyCtx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if state != fsp.DeliveryAccepted {
		t.Fatalf("state = %q, want %q", state, fsp.DeliveryAccepted)
	}

	deadline := time.Now().Add(waitFor)
	var lines []string
	for time.Now().Before(deadline) {
		if lines = readLines(t, outbox); len(lines) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(lines) != 1 {
		t.Fatalf("the fixture recorded %d replies, want 1: %v", len(lines), lines)
	}
	if lines[0] != "chat-1\tlooks good" {
		t.Errorf("reply line = %q, want %q", lines[0], "chat-1\tlooks good")
	}
}
