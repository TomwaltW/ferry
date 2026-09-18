package conformance

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/mcpmini"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// waitFor bounds the test's own plumbing.
const waitFor = 5 * time.Second

// The one tool both peers offer, so that the same assertions fit either.
const (
	toolName        = "echo"
	toolDescription = "Return the text it is given"
)

// The custom notification the suite sends in both directions.
// notifications/progress is part of MCP proper, which is what makes it usable
// against the official SDK: the SDK rejects a method it does not know before
// any handler of ours could see it.
const notifyProgress = "notifications/progress"

// echoIn is the tool's argument, and the shape both peers infer their schema
// from.
type echoIn struct {
	Text string `json:"text" jsonschema:"the text to echo back"`
}

// progressParams is notifications/progress as it goes over the wire. Only the
// members the suite reads are named.
type progressParams struct {
	ProgressToken any     `json:"progressToken"`
	Message       string  `json:"message,omitempty"`
	Progress      float64 `json:"progress"`
}

// peer is one implementation under test, seen from Ferry's client.
type peer struct {
	name  string
	start func(t *testing.T, in io.Reader, out io.Writer) handle
}

// handle drives the server half of a conformance run.
type handle interface {
	// push sends a notification from the server to the client.
	push(t *testing.T, message string)
	// awaitNotification returns the next notification the client sent.
	awaitNotification(t *testing.T) progressParams
	stop()
}

// peers is the whole point of this package: the same tests, either side.
func peers() []peer {
	return []peer{
		{name: "mcpmini", start: startMini},
		{name: "go-sdk", start: startSDK},
	}
}

// --- the mcpmini peer ------------------------------------------------------

type miniHandle struct {
	srv    *mcpmini.Server
	cancel context.CancelFunc
	got    chan progressParams
	done   chan struct{}
}

func startMini(t *testing.T, in io.Reader, out io.Writer) handle {
	t.Helper()
	h := &miniHandle{got: make(chan progressParams, 8), done: make(chan struct{})}

	schema, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
		"required":   []string{"text"},
	})
	if err != nil {
		t.Fatalf("encode the input schema: %v", err)
	}

	h.srv = mcpmini.NewServer(mcpmini.ServerOptions{
		Info:         mcpmini.Info{Name: "conformance", Version: "1"},
		Instructions: instructions,
		Tools:        []mcpmini.Tool{{Name: toolName, Description: toolDescription, InputSchema: schema}},
		Call: func(_ context.Context, name string, args json.RawMessage) (mcpmini.ToolResult, error) {
			if name != toolName {
				return mcpmini.ToolResult{
					Content: []mcpmini.Content{{Type: "text", Text: "no tool named " + name}},
					IsError: true,
				}, nil
			}
			var a echoIn
			if err := json.Unmarshal(args, &a); err != nil {
				return mcpmini.ToolResult{
					Content: []mcpmini.Content{{Type: "text", Text: err.Error()}},
					IsError: true,
				}, nil
			}
			return mcpmini.ToolResult{Content: []mcpmini.Content{{Type: "text", Text: a.Text}}}, nil
		},
		OnNotify: func(_ context.Context, method string, params json.RawMessage) {
			if method != notifyProgress {
				return
			}
			var p progressParams
			if err := json.Unmarshal(params, &p); err != nil {
				return
			}
			select {
			case h.got <- p:
			default:
			}
		},
		In:  in,
		Out: out,
	})

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		_ = h.srv.Run(ctx)
	}()
	return h
}

func (h *miniHandle) push(t *testing.T, message string) {
	t.Helper()
	err := h.srv.Notify(context.Background(), notifyProgress, progressParams{
		ProgressToken: "conformance",
		Message:       message,
		Progress:      1,
	})
	if err != nil {
		t.Fatalf("push a notification: %v", err)
	}
}

func (h *miniHandle) awaitNotification(t *testing.T) progressParams {
	t.Helper()
	select {
	case p := <-h.got:
		return p
	case <-time.After(waitFor):
		t.Fatal("the server received no notification")
		return progressParams{}
	}
}

func (h *miniHandle) stop() {
	h.cancel()
	_ = h.srv.Close()
	<-h.done
}

// --- the official SDK peer -------------------------------------------------

type sdkHandle struct {
	session *mcp.ServerSession
	got     chan progressParams
}

func startSDK(t *testing.T, in io.Reader, out io.Writer) handle {
	t.Helper()
	h := &sdkHandle{got: make(chan progressParams, 8)}

	srv := mcp.NewServer(
		&mcp.Implementation{Name: "conformance", Version: "1"},
		&mcp.ServerOptions{
			Instructions: instructions,
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationServerRequest) {
				select {
				case h.got <- progressParams{
					ProgressToken: req.Params.ProgressToken,
					Message:       req.Params.Message,
					Progress:      req.Params.Progress,
				}:
				default:
				}
			},
		},
	)
	mcp.AddTool(srv, &mcp.Tool{Name: toolName, Description: toolDescription},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})

	session, err := srv.Connect(context.Background(), &mcp.IOTransport{
		Reader: io.NopCloser(in),
		Writer: nopWriteCloser{out},
	}, nil)
	if err != nil {
		t.Fatalf("connect the official SDK server: %v", err)
	}
	h.session = session
	return h
}

func (h *sdkHandle) push(t *testing.T, message string) {
	t.Helper()
	err := h.session.NotifyProgress(context.Background(), &mcp.ProgressNotificationParams{
		ProgressToken: "conformance",
		Message:       message,
		Progress:      1,
	})
	if err != nil {
		t.Fatalf("push a notification: %v", err)
	}
}

func (h *sdkHandle) awaitNotification(t *testing.T) progressParams {
	t.Helper()
	select {
	case p := <-h.got:
		return p
	case <-time.After(waitFor):
		t.Fatal("the server received no notification")
		return progressParams{}
	}
}

func (h *sdkHandle) stop() { _ = h.session.Close() }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// instructions is what both peers report, so that a test can tell a handshake
// that carried them from one that dropped them.
const instructions = "conformance peer"

// --- the suite -------------------------------------------------------------

// session is one connected pair: Ferry's client against one peer.
type session struct {
	cli  *mcpmini.Client
	peer handle
	got  chan note
}

type note struct {
	method string
	params json.RawMessage
}

func connect(t *testing.T, p peer) *session {
	t.Helper()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	h := p.start(t, serverIn, serverOut)

	s := &session{peer: h, got: make(chan note, 8)}
	s.cli = mcpmini.NewClient(mcpmini.ClientOptions{
		Info: mcpmini.Info{Name: "ferry", Version: "test"},
		In:   clientIn,
		Out:  clientOut,
		OnNotify: func(_ context.Context, method string, params json.RawMessage) {
			select {
			case s.got <- note{method: method, params: append(json.RawMessage(nil), params...)}:
			default:
			}
		},
	})
	if err := s.cli.Start(context.Background()); err != nil {
		h.stop()
		t.Fatalf("the handshake with the %s peer failed: %v", p.name, err)
	}

	t.Cleanup(func() {
		_ = s.cli.Close()
		h.stop()
		_ = serverIn.Close()
		_ = serverOut.Close()
	})
	return s
}

// awaitNotification returns the next notification the client received, skipping
// anything the peer sends on its own.
func (s *session) awaitNotification(t *testing.T, method string) json.RawMessage {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case n := <-s.got:
			if n.method == method {
				return n.params
			}
		case <-deadline:
			t.Fatalf("no %s notification arrived", method)
			return nil
		}
	}
}

func TestConformanceInitialize(t *testing.T) {
	for _, p := range peers() {
		t.Run(p.name, func(t *testing.T) {
			s := connect(t, p)

			// Both peers must settle on a version Ferry knows, and on the same
			// one: a client that read the answer differently would go on to
			// speak the wrong dialect.
			got := s.cli.ProtocolVersion()
			if !slices.Contains(mcpmini.SupportedVersions, got) {
				t.Fatalf("settled on %q, which is not in %v", got, mcpmini.SupportedVersions)
			}
			if got != mcpmini.LatestHandshakeVersion {
				t.Errorf("settled on %q, want %q: the client asked for it and both peers support it",
					got, mcpmini.LatestHandshakeVersion)
			}

			res := s.cli.Server()
			if res == nil {
				t.Fatal("the handshake produced no result")
			}
			if res.ServerInfo.Name != "conformance" || res.ServerInfo.Version != "1" {
				t.Errorf("serverInfo = %+v, want conformance 1", res.ServerInfo)
			}
			if res.Instructions != instructions {
				t.Errorf("instructions = %q, want %q", res.Instructions, instructions)
			}
			if res.Capabilities.Tools == nil {
				t.Error("capabilities.tools is absent although the peer offers a tool")
			}

			// The session is usable straight after the handshake.
			if err := s.cli.Ping(context.Background()); err != nil {
				t.Errorf("Ping right after the handshake: %v", err)
			}
		})
	}
}

func TestConformanceTools(t *testing.T) {
	for _, p := range peers() {
		t.Run(p.name, func(t *testing.T) {
			s := connect(t, p)
			ctx := context.Background()

			tools, err := s.cli.ListTools(ctx)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools) != 1 {
				t.Fatalf("ListTools returned %d tools, want 1: %+v", len(tools), tools)
			}
			if tools[0].Name != toolName {
				t.Errorf("name = %q, want %q", tools[0].Name, toolName)
			}
			if tools[0].Description != toolDescription {
				t.Errorf("description = %q, want %q", tools[0].Description, toolDescription)
			}

			// The schema has to survive as JSON Schema, whichever side built it:
			// it is what tells the model how to call the tool.
			var schema struct {
				Type       string `json:"type"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
				t.Fatalf("the input schema is not JSON: %v", err)
			}
			if schema.Type != "object" {
				t.Errorf("input schema type = %q, want object", schema.Type)
			}
			if schema.Properties["text"].Type != "string" {
				t.Errorf("input schema properties = %+v, want a string text property", schema.Properties)
			}

			res, err := s.cli.CallTool(ctx, toolName, echoIn{Text: "conformance"})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("result = %+v, want a successful call", res)
			}
			if len(res.Content) != 1 {
				t.Fatalf("content = %+v, want one entry", res.Content)
			}
			if res.Content[0].Type != "text" || res.Content[0].Text != "conformance" {
				t.Errorf("content = %+v, want a text entry saying conformance", res.Content[0])
			}

			// A tool that is not there must come back as an answer, not as a
			// dropped connection: the session has to stay usable.
			if _, err := s.cli.CallTool(ctx, "nope", echoIn{Text: "x"}); err == nil {
				// The official SDK reports this as a protocol error and mcpmini
				// as a tool error; either is an answer, and both leave the
				// session alive. Only silence would be wrong.
				t.Log("the peer answered an unknown tool with a tool error rather than a protocol error")
			}
			if err := s.cli.Ping(ctx); err != nil {
				t.Errorf("the session did not survive a call to an unknown tool: %v", err)
			}
		})
	}
}

func TestConformanceNotifications(t *testing.T) {
	for _, p := range peers() {
		t.Run(p.name, func(t *testing.T) {
			s := connect(t, p)
			ctx := context.Background()

			// Server to client.
			s.peer.push(t, "from the server")
			raw := s.awaitNotification(t, notifyProgress)
			var down progressParams
			if err := json.Unmarshal(raw, &down); err != nil {
				t.Fatalf("parse the notification: %v", err)
			}
			if down.Message != "from the server" {
				t.Errorf("message = %q, want %q", down.Message, "from the server")
			}
			if down.ProgressToken != "conformance" {
				t.Errorf("progressToken = %v, want conformance", down.ProgressToken)
			}

			// Client to server.
			if err := s.cli.Notify(ctx, notifyProgress, progressParams{
				ProgressToken: "conformance",
				Message:       "from the client",
				Progress:      2,
			}); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			up := s.peer.awaitNotification(t)
			if up.Message != "from the client" {
				t.Errorf("message = %q, want %q", up.Message, "from the client")
			}
			if up.Progress != 2 {
				t.Errorf("progress = %v, want 2", up.Progress)
			}

			// A cancellation is ignored by both, and leaves the session alive.
			if err := s.cli.Notify(ctx, mcpmini.NotifyCancelled, map[string]any{
				"requestId": 999, "reason": "nobody is waiting",
			}); err != nil {
				t.Fatalf("Notify a cancellation: %v", err)
			}
			if err := s.cli.Ping(ctx); err != nil {
				t.Errorf("the session did not survive a cancellation: %v", err)
			}
		})
	}
}
