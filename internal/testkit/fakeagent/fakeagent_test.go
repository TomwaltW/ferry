package fakeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestMain quiets the SDK, which logs every connection close through the
// default slog handler. Its own SetLogger races with its receive goroutine, so
// the default handler is the only safe place to do this.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// permitFunc answers a session/request_permission. A nil one rejects.
type permitFunc func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)

// testClient is the client side of the pipe: it records every session update
// and delegates permission requests to the test.
type testClient struct {
	permit permitFunc

	mu      sync.Mutex
	updates []acp.SessionNotification
	seen    chan acp.SessionNotification
}

var _ acp.Client = (*testClient)(nil)

func newTestClient(permit permitFunc) *testClient {
	return &testClient{permit: permit, seen: make(chan acp.SessionNotification, 256)}
}

func (c *testClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, params)
	c.mu.Unlock()
	select {
	case c.seen <- params:
	default:
	}
	return nil
}

func (c *testClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if c.permit == nil {
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected("reject-once"),
		}, nil
	}
	return c.permit(ctx, params)
}

func (c *testClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (c *testClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (c *testClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (c *testClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}

func (c *testClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (c *testClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}

func (c *testClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}

// agentText joins the agent_message_chunk updates recorded so far.
func (c *testClient) agentText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, u := range c.updates {
		if chunk := u.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			b.WriteString(chunk.Content.Text.Text)
		}
	}
	return b.String()
}

// agentChunks returns the agent_message_chunk updates one by one.
func (c *testClient) agentChunks() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, u := range c.updates {
		if chunk := u.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			out = append(out, chunk.Content.Text.Text)
		}
	}
	return out
}

// snapshot copies the recorded updates so a test can walk them safely.
func (c *testClient) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.updates...)
}

func (c *testClient) reset() {
	c.mu.Lock()
	c.updates = nil
	c.mu.Unlock()
	for {
		select {
		case <-c.seen:
		default:
			return
		}
	}
}

// waitFor blocks until an update satisfying match arrives, failing the test on
// timeout. It watches the notification stream rather than sleeping.
func (c *testClient) waitFor(t *testing.T, match func(acp.SessionNotification) bool) acp.SessionNotification {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-c.seen:
			if match(u) {
				return u
			}
		case <-deadline:
			t.Fatal("timed out waiting for a session update")
		}
	}
}

// harness wires a client to an in-process Serve over two pipes.
type harness struct {
	client *testClient
	conn   *acp.ClientSideConnection
	home   string
}

func start(t *testing.T, permit permitFunc) *harness {
	t.Helper()

	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	home := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, agentIn, agentOut, home) }()

	c := newTestClient(permit)
	h := &harness{client: c, conn: acp.NewClientSideConnection(c, clientOut, clientIn), home: home}

	t.Cleanup(func() {
		// Closing the client's writer is the peer disconnect Serve waits on.
		_ = clientOut.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve returned %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the input closed")
		}
		cancel()
		_ = agentOut.Close()
	})

	if _, err := h.conn.Initialize(t.Context(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	}); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return h
}

// newSession opens a session in cwd and returns its id.
func (h *harness) newSession(t *testing.T, cwd string) acp.SessionId {
	t.Helper()
	resp, err := h.conn.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	return resp.SessionId
}

func (h *harness) prompt(t *testing.T, id acp.SessionId, text string) acp.PromptResponse {
	t.Helper()
	resp, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
		SessionId: id,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})
	if err != nil {
		t.Fatalf("session/prompt %q failed: %v", text, err)
	}
	return resp
}

func TestInitializeAdvertisesLoadSession(t *testing.T) {
	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	go func() { _ = Serve(t.Context(), agentIn, agentOut, t.TempDir()) }()
	t.Cleanup(func() { _ = clientOut.Close(); _ = agentOut.Close() })

	conn := acp.NewClientSideConnection(newTestClient(nil), clientOut, clientIn)
	resp, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if resp.ProtocolVersion != 1 {
		t.Errorf("protocolVersion = %d, want 1", resp.ProtocolVersion)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Error("loadSession = false, want true")
	}
	if resp.AgentCapabilities.SessionCapabilities.List == nil {
		t.Error("sessionCapabilities.list is unset, want session/list advertised")
	}
}

func TestEcho(t *testing.T) {
	h := start(t, nil)
	id := h.newSession(t, t.TempDir())

	resp := h.prompt(t, id, "echo hello world")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}

	chunks := h.client.agentChunks()
	want := []string{"hello", " worl", "d"}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks %q, want %d", len(chunks), chunks, len(want))
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestUnknownPromptEchoesBack(t *testing.T) {
	h := start(t, nil)
	id := h.newSession(t, t.TempDir())

	h.prompt(t, id, "hello there")
	if got, want := h.client.agentText(), "unknown: hello there"; got != want {
		t.Errorf("agent said %q, want %q", got, want)
	}
}

func TestPermissionAllow(t *testing.T) {
	var got acp.RequestPermissionRequest
	h := start(t, func(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
		got = req
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected("allow-once"),
		}, nil
	})
	id := h.newSession(t, t.TempDir())

	resp := h.prompt(t, id, "ask execute /repo/run.sh")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if text := h.client.agentText(); text != "allowed" {
		t.Errorf("agent said %q, want %q", text, "allowed")
	}
	if got.ToolCall.Kind == nil || *got.ToolCall.Kind != acp.ToolKindExecute {
		t.Errorf("toolCall.kind = %v, want %q", got.ToolCall.Kind, acp.ToolKindExecute)
	}
	if len(got.ToolCall.Locations) != 1 || got.ToolCall.Locations[0].Path != "/repo/run.sh" {
		t.Errorf("toolCall.locations = %+v, want one entry for /repo/run.sh", got.ToolCall.Locations)
	}
	wantOptions := []struct {
		id   acp.PermissionOptionId
		kind acp.PermissionOptionKind
	}{
		{"allow-once", acp.PermissionOptionKindAllowOnce},
		{"allow-always", acp.PermissionOptionKindAllowAlways},
		{"reject-once", acp.PermissionOptionKindRejectOnce},
	}
	if len(got.Options) != len(wantOptions) {
		t.Fatalf("got %d options %+v, want %d", len(got.Options), got.Options, len(wantOptions))
	}
	for i, w := range wantOptions {
		if got.Options[i].OptionId != w.id || got.Options[i].Kind != w.kind {
			t.Errorf("option %d = %q/%q, want %q/%q", i, got.Options[i].OptionId, got.Options[i].Kind, w.id, w.kind)
		}
	}
}

func TestPermissionReject(t *testing.T) {
	h := start(t, func(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected("reject-once"),
		}, nil
	})
	id := h.newSession(t, t.TempDir())

	resp := h.prompt(t, id, "ask edit /repo/main.go")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if text := h.client.agentText(); text != "denied" {
		t.Errorf("agent said %q, want %q", text, "denied")
	}
}

func TestCancel(t *testing.T) {
	t.Run("slow turn", func(t *testing.T) {
		h := start(t, nil)
		id := h.newSession(t, t.TempDir())

		done := make(chan acp.PromptResponse, 1)
		go func() {
			resp, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
				SessionId: id,
				Prompt:    []acp.ContentBlock{acp.TextBlock("slow 60000")},
			})
			if err != nil {
				t.Errorf("session/prompt failed: %v", err)
			}
			done <- resp
		}()

		// Wait for the turn to be under way before cancelling it.
		h.client.waitFor(t, func(u acp.SessionNotification) bool {
			chunk := u.Update.AgentMessageChunk
			return chunk != nil && chunk.Content.Text != nil && chunk.Content.Text.Text == "."
		})

		start := time.Now()
		if err := h.conn.Cancel(t.Context(), acp.CancelNotification{SessionId: id}); err != nil {
			t.Fatalf("session/cancel failed: %v", err)
		}
		select {
		case resp := <-done:
			if took := time.Since(start); took > 200*time.Millisecond {
				t.Errorf("the turn ended %v after the cancel, want at most 200ms", took)
			}
			if resp.StopReason != acp.StopReasonCancelled {
				t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancelled turn never returned")
		}
	})

	t.Run("pending approval answered as cancelled", func(t *testing.T) {
		asked := make(chan struct{})
		release := make(chan struct{})
		h := start(t, func(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			close(asked)
			<-release
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
		})
		id := h.newSession(t, t.TempDir())

		done := make(chan acp.PromptResponse, 1)
		go func() {
			resp, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
				SessionId: id,
				Prompt:    []acp.ContentBlock{acp.TextBlock("ask execute /repo/run.sh")},
			})
			if err != nil {
				t.Errorf("session/prompt failed: %v", err)
			}
			done <- resp
		}()

		<-asked
		if err := h.conn.Cancel(t.Context(), acp.CancelNotification{SessionId: id}); err != nil {
			t.Fatalf("session/cancel failed: %v", err)
		}
		close(release)

		select {
		case resp := <-done:
			if resp.StopReason != acp.StopReasonCancelled {
				t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancelled turn never returned")
		}
	})

	t.Run("pending approval never answered", func(t *testing.T) {
		asked := make(chan struct{})
		h := start(t, func(ctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			close(asked)
			// A client that goes quiet: only the cancel unblocks this.
			<-ctx.Done()
			return acp.RequestPermissionResponse{}, ctx.Err()
		})
		id := h.newSession(t, t.TempDir())

		done := make(chan acp.PromptResponse, 1)
		go func() {
			resp, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
				SessionId: id,
				Prompt:    []acp.ContentBlock{acp.TextBlock("ask read /repo/main.go")},
			})
			if err != nil {
				t.Errorf("session/prompt failed: %v", err)
			}
			done <- resp
		}()

		<-asked
		if err := h.conn.Cancel(t.Context(), acp.CancelNotification{SessionId: id}); err != nil {
			t.Fatalf("session/cancel failed: %v", err)
		}

		select {
		case resp := <-done:
			if resp.StopReason != acp.StopReasonCancelled {
				t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancelled turn never returned")
		}
	})
}

func TestListAndLoad(t *testing.T) {
	h := start(t, nil)

	cwdA, cwdB := t.TempDir(), t.TempDir()
	idA := h.newSession(t, cwdA)
	idB := h.newSession(t, cwdB)

	h.prompt(t, idA, "echo the quick brown fox")
	h.prompt(t, idB, "echo second")

	all, err := h.conn.ListSessions(t.Context(), acp.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("session/list failed: %v", err)
	}
	if len(all.Sessions) != 2 {
		t.Fatalf("session/list returned %d sessions, want 2", len(all.Sessions))
	}

	filtered, err := h.conn.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: acp.Ptr(cwdA)})
	if err != nil {
		t.Fatalf("session/list with a cwd filter failed: %v", err)
	}
	if len(filtered.Sessions) != 1 || filtered.Sessions[0].SessionId != idA {
		t.Fatalf("the cwd filter returned %+v, want only %s", filtered.Sessions, idA)
	}
	info := filtered.Sessions[0]
	if info.Title == nil || *info.Title != "echo the quick brown" {
		t.Errorf("title = %v, want the first 20 characters %q", info.Title, "echo the quick brown")
	}
	if info.UpdatedAt == nil {
		t.Error("updatedAt is unset, want a timestamp")
	} else if _, err := time.Parse(time.RFC3339Nano, *info.UpdatedAt); err != nil {
		t.Errorf("updatedAt %q is not RFC 3339: %v", *info.UpdatedAt, err)
	}
	if info.Cwd != cwdA {
		t.Errorf("cwd = %q, want %q", info.Cwd, cwdA)
	}

	// The session file is what a restarted process would read back.
	raw, err := os.ReadFile(filepath.Join(h.home, "sessions", string(idA)+".json"))
	if err != nil {
		t.Fatalf("reading the session file failed: %v", err)
	}
	var stored record
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("the session file is not valid JSON: %v", err)
	}
	if stored.ID != string(idA) || stored.Cwd != cwdA || len(stored.History) != 2 {
		t.Errorf("stored session = %+v, want id %s, cwd %s and 2 history entries", stored, idA, cwdA)
	}

	// Loading replays the history as user then agent chunks.
	h.client.reset()
	if _, err := h.conn.LoadSession(t.Context(), acp.LoadSessionRequest{
		SessionId:  idA,
		Cwd:        cwdA,
		McpServers: []acp.McpServer{},
	}); err != nil {
		t.Fatalf("session/load failed: %v", err)
	}

	var replay []string
	for _, u := range h.client.snapshot() {
		switch {
		case u.Update.UserMessageChunk != nil:
			replay = append(replay, "user:"+u.Update.UserMessageChunk.Content.Text.Text)
		case u.Update.AgentMessageChunk != nil:
			replay = append(replay, "agent:"+u.Update.AgentMessageChunk.Content.Text.Text)
		}
	}
	want := []string{"user:echo the quick brown fox", "agent:the quick brown fox"}
	if strings.Join(replay, "|") != strings.Join(want, "|") {
		t.Errorf("replay = %v, want %v", replay, want)
	}

	// An id nobody created is invalid params, per the failure table.
	_, err = h.conn.LoadSession(t.Context(), acp.LoadSessionRequest{
		SessionId:  acp.SessionId("sess_nope"),
		Cwd:        cwdA,
		McpServers: []acp.McpServer{},
	})
	assertRequestError(t, err, -32602)
}

func TestBurst(t *testing.T) {
	h := start(t, nil)
	id := h.newSession(t, t.TempDir())

	resp := h.prompt(t, id, "burst 3")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}

	chunks := h.client.agentChunks()
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) != burstWidth+1 || !strings.HasSuffix(chunk, "\n") {
			t.Errorf("chunk %d is %d bytes %q, want %d characters and a newline", i, len(chunk), chunk, burstWidth)
		}
		if want := strings.Repeat(string(rune('a'+i)), burstWidth) + "\n"; chunk != want {
			t.Errorf("chunk %d = %q, want %q", i, chunk, want)
		}
	}
}

func TestPlanAndTool(t *testing.T) {
	h := start(t, nil)
	id := h.newSession(t, t.TempDir())

	h.prompt(t, id, "plan")
	var plans []*acp.SessionUpdatePlan
	for _, u := range h.client.snapshot() {
		if u.Update.Plan != nil {
			plans = append(plans, u.Update.Plan)
		}
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plan updates, want 1", len(plans))
	}
	if len(plans[0].Entries) != 3 {
		t.Errorf("the plan has %d entries, want 3", len(plans[0].Entries))
	}

	h.client.reset()
	resp := h.prompt(t, id, "tool")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}

	var starts, updates int
	for _, u := range h.client.snapshot() {
		if tc := u.Update.ToolCall; tc != nil {
			starts++
			if tc.Title != "run tests" {
				t.Errorf("tool call title = %q, want %q", tc.Title, "run tests")
			}
			if tc.Kind != acp.ToolKindExecute {
				t.Errorf("tool call kind = %q, want %q", tc.Kind, acp.ToolKindExecute)
			}
			if tc.Status != acp.ToolCallStatusPending {
				t.Errorf("tool call status = %q, want %q", tc.Status, acp.ToolCallStatusPending)
			}
		}
		if tu := u.Update.ToolCallUpdate; tu != nil {
			updates++
			if tu.Status == nil || *tu.Status != acp.ToolCallStatusCompleted {
				t.Errorf("tool call update status = %v, want %q", tu.Status, acp.ToolCallStatusCompleted)
			}
		}
	}
	if starts != 1 || updates != 1 {
		t.Errorf("got %d tool_call and %d tool_call_update, want 1 of each", starts, updates)
	}
	if text := h.client.agentText(); text != "done" {
		t.Errorf("agent said %q, want %q", text, "done")
	}
}

func TestFail(t *testing.T) {
	h := start(t, nil)
	id := h.newSession(t, t.TempDir())

	_, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
		SessionId: id,
		Prompt:    []acp.ContentBlock{acp.TextBlock("fail")},
	})
	assertRequestError(t, err, -32603)
}

func TestPromptOnUnknownSession(t *testing.T) {
	h := start(t, nil)

	_, err := h.conn.Prompt(t.Context(), acp.PromptRequest{
		SessionId: acp.SessionId("sess_nope"),
		Prompt:    []acp.ContentBlock{acp.TextBlock("echo hi")},
	})
	assertRequestError(t, err, -32602)
}

// TestServeFlushesOnInputClose covers the contract that a closed input still
// gets the responses for the requests already accepted, which is what the
// binary smoke test relies on.
func TestServeFlushesOnInputClose(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n")
	var out bytes.Buffer

	if err := Serve(t.Context(), in, &out, t.TempDir()); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}

	first, _, _ := strings.Cut(out.String(), "\n")
	if !strings.Contains(first, `"protocolVersion":1`) {
		t.Fatalf("first line of output was %q, want it to carry \"protocolVersion\":1", first)
	}
}

func TestServeRejectsEmptyHome(t *testing.T) {
	if err := Serve(t.Context(), strings.NewReader(""), io.Discard, ""); err == nil {
		t.Fatal("Serve with an empty home returned nil, want an error")
	}
}

// TestResumeFlag builds the binary and checks the acknowledgement the P2
// terminal-reattach test looks for.
func TestResumeFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("building the binary is too slow for -short")
	}
	bin := filepath.Join(t.TempDir(), "ferry-fakeagent"+exeSuffix)

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/TomwaltW/ferry/cmd/ferry-fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the binary failed: %v\n%s", err, out)
	}

	out, err := exec.CommandContext(t.Context(), bin, "--resume", "abc").Output()
	if err != nil {
		t.Fatalf("running --resume failed: %v", err)
	}
	if got, want := string(out), "resumed abc\n"; got != want {
		t.Errorf("--resume printed %q, want %q", got, want)
	}
}

func assertRequestError(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want JSON-RPC code %d", code)
	}
	var reqErr *acp.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("got %T (%v), want an *acp.RequestError", err, err)
	}
	if reqErr.Code != code {
		t.Errorf("error code = %d, want %d", reqErr.Code, code)
	}
}

// exeSuffix is what Windows needs on a built binary before it can be executed.
var exeSuffix = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()
