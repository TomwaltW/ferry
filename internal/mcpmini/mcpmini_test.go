package mcpmini

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitFor is test plumbing, not a business timeout: every wait here is for a
// goroutine that has already been handed its work.
const waitFor = 5 * time.Second

// echoTool is the one tool the test servers offer.
var echoTool = Tool{
	Name:        "echo",
	Description: "Return the text it is given",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
}

func echoCall(_ context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if name != echoTool.Name {
		return ToolResult{Content: []Content{{Type: "text", Text: "no tool named " + name}}, IsError: true}, nil
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: []Content{{Type: "text", Text: in.Text}}}, nil
}

// notes collects the notifications one side received.
type notes struct {
	mu   sync.Mutex
	got  []note
	ch   chan note
	once sync.Once
}

type note struct {
	Method string
	Params json.RawMessage
}

func newNotes() *notes { return &notes{ch: make(chan note, 16)} }

func (n *notes) record(_ context.Context, method string, params json.RawMessage) {
	n.mu.Lock()
	n.got = append(n.got, note{Method: method, Params: append(json.RawMessage(nil), params...)})
	n.mu.Unlock()
	select {
	case n.ch <- note{Method: method, Params: append(json.RawMessage(nil), params...)}:
	default:
	}
}

func (n *notes) next(t *testing.T) note {
	t.Helper()
	select {
	case got := <-n.ch:
		return got
	case <-time.After(waitFor):
		t.Fatal("no notification arrived")
		return note{}
	}
}

// quiet fails the test if another notification turns up shortly.
func (n *notes) quiet(t *testing.T) {
	t.Helper()
	select {
	case got := <-n.ch:
		t.Fatalf("unexpected notification %q", got.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

func (n *notes) methods() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.got))
	for _, g := range n.got {
		out = append(out, g.Method)
	}
	return out
}

// connect wires a server and a client to each other over two pipes and runs
// the handshake, which is how both roles meet in production too.
func connect(t *testing.T, sopts ServerOptions, copts ClientOptions) (*Server, *Client) {
	t.Helper()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	sopts.In, sopts.Out = serverIn, serverOut
	copts.In, copts.Out = clientIn, clientOut
	if sopts.Info.Name == "" {
		sopts.Info = Info{Name: "test-server", Version: "1"}
	}
	if copts.Info.Name == "" {
		copts.Info = Info{Name: "test-client", Version: "1"}
	}

	s := NewServer(sopts)
	c := NewClient(copts)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = s.Run(ctx)
	}()

	if err := c.Start(context.Background()); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		cancel()
		_ = serverIn.Close()
		_ = serverOut.Close()
		select {
		case <-served:
		case <-time.After(waitFor):
			t.Error("Server.Run did not return")
		}
	})
	return s, c
}

func TestInitializeNegotiation(t *testing.T) {
	tests := []struct {
		name string
		ask  string
		want string
		why  string
	}{
		{
			name: "a version both sides know is kept",
			ask:  ProtocolVersion20250618,
			want: ProtocolVersion20250618,
			why:  "the spec says a server that supports the requested version must answer with it",
		},
		{
			name: "the newest handshake version is kept",
			ask:  ProtocolVersion20251125,
			want: ProtocolVersion20251125,
		},
		{
			name: "a version past the handshake falls back",
			ask:  ProtocolVersion20260728,
			want: ProtocolVersion20251125,
			why:  "initialize was superseded in 2026-07-28, so it never settles on it",
		},
		{
			name: "an unknown version falls back to the newest",
			ask:  "1999-01-01",
			want: ProtocolVersion20251125,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := connect(t,
				ServerOptions{Tools: []Tool{echoTool}, Call: echoCall},
				ClientOptions{ProtocolVersion: tt.ask},
			)
			if got := c.ProtocolVersion(); got != tt.want {
				t.Errorf("client settled on %q, want %q (%s)", got, tt.want, tt.why)
			}
			if got := s.ProtocolVersion(); got != tt.want {
				t.Errorf("server settled on %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("the default is the newest handshake version", func(t *testing.T) {
		_, c := connect(t, ServerOptions{}, ClientOptions{})
		if got := c.ProtocolVersion(); got != LatestHandshakeVersion {
			t.Errorf("ProtocolVersion = %q, want %q", got, LatestHandshakeVersion)
		}
	})

	t.Run("the handshake carries info, capabilities and instructions", func(t *testing.T) {
		_, c := connect(t, ServerOptions{
			Info:         Info{Name: "ferry", Version: "0.3"},
			Instructions: "read me",
			Experimental: map[string]any{"claude/channel": map[string]any{}},
			Tools:        []Tool{echoTool},
			Call:         echoCall,
		}, ClientOptions{})

		res := c.Server()
		if res == nil {
			t.Fatal("Server() is nil after Start")
		}
		if res.ServerInfo.Name != "ferry" || res.ServerInfo.Version != "0.3" {
			t.Errorf("serverInfo = %+v, want ferry 0.3", res.ServerInfo)
		}
		if res.Instructions != "read me" {
			t.Errorf("instructions = %q, want %q", res.Instructions, "read me")
		}
		if _, ok := res.Capabilities.Experimental["claude/channel"]; !ok {
			t.Errorf("experimental = %v, want a claude/channel entry", res.Capabilities.Experimental)
		}
		if res.Capabilities.Tools == nil {
			t.Error("capabilities.tools is absent although the server offers a tool")
		}
	})

	t.Run("notifications/initialized ends the handshake", func(t *testing.T) {
		s, _ := connect(t, ServerOptions{}, ClientOptions{})
		deadline := time.Now().Add(waitFor)
		for !s.Initialized() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !s.Initialized() {
			t.Error("the server never saw notifications/initialized")
		}
	})
}

func TestToolsListAndCall(t *testing.T) {
	_, c := connect(t, ServerOptions{Tools: []Tool{echoTool}, Call: echoCall}, ClientOptions{})
	ctx := context.Background()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %+v, want one tool named echo", tools)
	}
	if tools[0].Description != echoTool.Description {
		t.Errorf("description = %q, want %q", tools[0].Description, echoTool.Description)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("the input schema did not survive the trip: %v", err)
	}
	if schema.Type != "object" || schema.Properties["text"].Type != "string" {
		t.Errorf("input schema = %+v, want an object with a string text property", schema)
	}

	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError is set on a call that should have worked: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "hello" {
		t.Errorf("content = %+v, want one text content saying hello", res.Content)
	}

	// A tool error belongs in the result, not in the transport.
	res, err = c.CallTool(ctx, "nope", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool on an unknown tool returned a transport error: %v", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want IsError", res)
	}
}

func TestNotificationsBothWays(t *testing.T) {
	fromClient := newNotes()
	fromServer := newNotes()

	s, c := connect(t,
		ServerOptions{OnNotify: fromClient.record, Tools: []Tool{echoTool}, Call: echoCall},
		ClientOptions{OnNotify: fromServer.record},
	)
	ctx := context.Background()

	if err := s.Notify(ctx, "notifications/claude/channel", map[string]any{
		"content": "hi there",
		"meta":    map[string]string{"chat_id": "c1"},
	}); err != nil {
		t.Fatalf("Server.Notify: %v", err)
	}
	got := fromServer.next(t)
	if got.Method != "notifications/claude/channel" {
		t.Errorf("method = %q, want notifications/claude/channel", got.Method)
	}
	var inbound struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	if err := json.Unmarshal(got.Params, &inbound); err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if inbound.Content != "hi there" || inbound.Meta["chat_id"] != "c1" {
		t.Errorf("params = %+v, want the content and meta that were sent", inbound)
	}

	if err := c.Notify(ctx, "notifications/claude/channel/permission", map[string]any{
		"request_id": "abcde",
		"behavior":   "allow",
	}); err != nil {
		t.Fatalf("Client.Notify: %v", err)
	}
	// The handshake's own notification arrives first.
	for {
		got = fromClient.next(t)
		if got.Method != NotifyInitialized {
			break
		}
	}
	if got.Method != "notifications/claude/channel/permission" {
		t.Fatalf("method = %q, want notifications/claude/channel/permission", got.Method)
	}
	var verdict struct {
		RequestID string `json:"request_id"`
		Behavior  string `json:"behavior"`
	}
	if err := json.Unmarshal(got.Params, &verdict); err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if verdict.RequestID != "abcde" || verdict.Behavior != "allow" {
		t.Errorf("params = %+v, want abcde/allow", verdict)
	}
}

func TestPing(t *testing.T) {
	_, c := connect(t, ServerOptions{}, ClientOptions{})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	// Ping stays available for as long as the session does.
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("second Ping: %v", err)
	}
}

func TestCancelledIgnored(t *testing.T) {
	fromClient := newNotes()
	_, c := connect(t,
		ServerOptions{OnNotify: fromClient.record, Tools: []Tool{echoTool}, Call: echoCall},
		ClientOptions{},
	)
	ctx := context.Background()

	// Drain the handshake notification so that only what follows is in play.
	if got := fromClient.next(t); got.Method != NotifyInitialized {
		t.Fatalf("first notification = %q, want %q", got.Method, NotifyInitialized)
	}

	if err := c.Notify(ctx, NotifyCancelled, map[string]any{"requestId": 1, "reason": "gone"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	fromClient.quiet(t)
	for _, m := range fromClient.methods() {
		if m == NotifyCancelled {
			t.Fatalf("notifications/cancelled reached the handler; methods = %v", fromClient.methods())
		}
	}

	// The session is unharmed: a call still works right after.
	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "still here"})
	if err != nil {
		t.Fatalf("CallTool after a cancellation: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "still here" {
		t.Errorf("content = %+v, want the echoed text", res.Content)
	}
}

func TestLineFraming(t *testing.T) {
	// The peer is driven by hand here, so that the bytes on the wire are the
	// thing under test rather than whatever the client would have written.
	serverIn, toServer := io.Pipe()
	fromServer, serverOut := io.Pipe()

	multiline := "first\nsecond\r\nthird\ttabbed \"quoted\""
	s := NewServer(ServerOptions{
		Info:  Info{Name: "framing", Version: "1"},
		Tools: []Tool{echoTool},
		Call: func(context.Context, string, json.RawMessage) (ToolResult, error) {
			return ToolResult{Content: []Content{{Type: "text", Text: multiline}}}, nil
		},
		In:  serverIn,
		Out: serverOut,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	// Blank and whitespace-only lines carry no frame and must not disturb the
	// stream; every request is one line, and so is every answer.
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"hand","version":"1"}}}`,
		``,
		`   `,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		"",
	}, "\n")

	go func() {
		_, _ = io.WriteString(toServer, input)
		_ = toServer.Close()
	}()

	sc := bufio.NewScanner(fromServer)
	sc.Buffer(make([]byte, 0, 4096), maxFrame)
	var lines []string
	for len(lines) < 3 && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 3 {
		t.Fatalf("read %d lines, want 3: %q", len(lines), lines)
	}

	for i, line := range lines {
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("line %d carries a newline of its own: %q", i, line)
		}
		var f frame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("line %d is not one JSON object: %v (%q)", i, err, line)
		}
		if f.JSONRPC != JSONRPCVersion {
			t.Errorf("line %d jsonrpc = %q, want %q", i, f.JSONRPC, JSONRPCVersion)
		}
	}

	// The three answers arrive in the order the requests did, and the tool
	// result's own newlines travelled escaped rather than splitting the frame.
	var ids []string
	for _, line := range lines {
		var f frame
		_ = json.Unmarshal([]byte(line), &f)
		ids = append(ids, string(f.ID))
	}
	if want := []string{"1", "2", "3"}; !equalStrings(ids, want) {
		t.Errorf("answer IDs = %v, want %v", ids, want)
	}

	var callResult ToolResult
	var second frame
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if err := json.Unmarshal(second.Result, &callResult); err != nil {
		t.Fatalf("parse the tools/call result: %v", err)
	}
	if len(callResult.Content) != 1 || callResult.Content[0].Text != multiline {
		t.Errorf("content = %+v, want the multi-line text back unchanged", callResult.Content)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
