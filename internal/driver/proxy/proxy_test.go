package proxy

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
)

// TestProxyPassthroughEcho is the editor's whole view of a proxied run:
// initialize, a session and a turn all behave as if the agent had been started
// directly.
func TestProxyPassthroughEcho(t *testing.T) {
	home := newHome(t)
	startDaemon(t, home)
	r := startRun(t, home, Options{})

	init := r.initialize(t)
	if !init.AgentCapabilities.LoadSession {
		t.Error("the agent's loadSession capability did not reach the editor")
	}
	if init.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Errorf("protocol version = %v, want %v", init.ProtocolVersion, acp.ProtocolVersionNumber)
	}

	id := r.newSession(t)
	if id == "" {
		t.Fatal("session/new returned no session ID")
	}

	resp := r.prompt(t, id, "echo hello world")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if got := r.editor.said(); got != "hello world" {
		t.Errorf("the editor was shown %q, want %q", got, "hello world")
	}
}

// TestProxyMirrorToDaemon covers the registration and the mirror: what the
// daemon learns about the session, and what it hears during a turn the editor
// started.
func TestProxyMirrorToDaemon(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{Title: "editor session", AllowInject: true, RemoteApprove: true})

	r.initialize(t)
	id := r.newSession(t)

	a := d.attached(t)
	switch {
	case a.req.Kind != control.KindProxy:
		t.Errorf("kind = %q, want %q", a.req.Kind, control.KindProxy)
	case a.req.Agent != "fake":
		t.Errorf("agent = %q, want %q", a.req.Agent, "fake")
	case a.req.AgentSessionID != string(id):
		t.Errorf("agent session = %q, want %q", a.req.AgentSessionID, id)
	case a.req.Title != "editor session":
		t.Errorf("title = %q, want %q", a.req.Title, "editor session")
	case a.req.PID != os.Getpid():
		t.Errorf("pid = %d, want %d", a.req.PID, os.Getpid())
	case a.req.Cwd == "":
		t.Error("the attach request named no working directory")
	case !a.req.AllowInject || !a.req.RemoteApprove:
		t.Errorf("allow_inject = %v, remote_approve = %v, want both true", a.req.AllowInject, a.req.RemoteApprove)
	}

	r.prompt(t, id, "echo mirrored")

	events := a.events(t)
	if len(events) < 3 {
		t.Fatalf("the daemon saw %d events, want the turn marker, the text and the turn end", len(events))
	}
	if first := events[0]; first.Kind != fsp.EvChunk || first.Text != editorTurnMarker {
		t.Errorf("first event = %v %q, want a chunk holding %q", first.Kind, first.Text, editorTurnMarker)
	}
	if got := chunkText(events); got != "mirrored" {
		t.Errorf("the daemon was mirrored %q, want %q", got, "mirrored")
	}
	last := events[len(events)-1]
	if last.Kind != fsp.EvTurnEnd || last.StopReason != string(acp.StopReasonEndTurn) {
		t.Errorf("last event = %v %q, want a turn end with %q", last.Kind, last.StopReason, acp.StopReasonEndTurn)
	}
	for _, e := range events {
		if e.Session != "" {
			t.Errorf("the proxy filled in a session ID (%q); that is the daemon's to assign", e.Session)
			break
		}
	}
}

// TestProxyNoDaemonPassthrough is the failure the spec cares about most: a
// daemon that is not running must cost the editor nothing.
func TestProxyNoDaemonPassthrough(t *testing.T) {
	home := newHome(t)
	r := startRun(t, home, Options{AllowInject: true})

	r.initialize(t)
	id := r.newSession(t)

	resp := r.prompt(t, id, "echo still here")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if got := r.editor.said(); got != "still here" {
		t.Errorf("the editor was shown %q, want %q", got, "still here")
	}

	want := i18n.Default().T(fsp.LangZH, "cli.proxy_no_daemon", nil)
	if got := r.stderr.String(); !strings.Contains(got, want) {
		t.Errorf("stderr = %q, want it to contain %q", got, want)
	}
}

// TestProxyInjectWhenIdle sends a prompt from chat while the editor is doing
// nothing: the editor is shown the input, and both ends see the answer.
func TestProxyInjectWhenIdle(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{AllowInject: true})

	r.initialize(t)
	r.newSession(t)
	a := d.attached(t)

	if err := a.send(control.Message{
		Type:      control.MsgPrompt,
		SessionID: a.resp.SessionID,
		Text:      "echo from chat",
		Sender:    "owner",
	}); err != nil {
		t.Fatalf("send a prompt from chat: %v", err)
	}

	if got := r.editor.waitUserChunk(t); got != "echo from chat" {
		t.Errorf("the editor was shown %q as the injected input, want %q", got, "echo from chat")
	}

	events := a.events(t)
	if got := chunkText(events); got != "from chat" {
		t.Errorf("the daemon was mirrored %q, want %q", got, "from chat")
	}
	for _, e := range events {
		if e.Kind == fsp.EvChunk && e.Text == editorTurnMarker {
			t.Error("an injected turn was marked as an editor turn")
		}
	}
	if last := events[len(events)-1]; last.StopReason != string(acp.StopReasonEndTurn) {
		t.Errorf("stop reason = %q, want %q", last.StopReason, acp.StopReasonEndTurn)
	}

	// The same text reached the editor, which is the point of injection: both
	// ends watch one conversation.
	if got := r.editor.waitSaid(t, "from chat"); !strings.Contains(got, "from chat") {
		t.Errorf("the editor was shown %q, want it to contain %q", got, "from chat")
	}
}

// TestProxyQueuesEditorPromptDuringInject holds an editor turn back until the
// injected one has finished. Without the queue both prompts would run on one
// session, and the SDK would cancel the first.
func TestProxyQueuesEditorPromptDuringInject(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{AllowInject: true})

	r.initialize(t)
	id := r.newSession(t)
	a := d.attached(t)

	if err := a.send(control.Message{
		Type:      control.MsgPrompt,
		SessionID: a.resp.SessionID,
		Text:      "slow 400",
		Sender:    "owner",
	}); err != nil {
		t.Fatalf("send a prompt from chat: %v", err)
	}

	// Wait for the injected turn to be under way before the editor speaks.
	r.editor.waitUserChunk(t)
	r.editor.waitAgentChunk(t)

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	editorDone := make(chan result, 1)
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), wait)
		defer stop()
		resp, err := r.editor.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: id,
			Prompt:    []acp.ContentBlock{acp.TextBlock("echo queued")},
		})
		editorDone <- result{resp, err}
	}()

	// The injected turn ends first, and on its own terms.
	injected := a.events(t)
	if last := injected[len(injected)-1]; last.StopReason != string(acp.StopReasonEndTurn) {
		t.Fatalf("the injected turn ended with %q, want %q: the editor's prompt did not wait",
			last.StopReason, acp.StopReasonEndTurn)
	}
	for _, e := range injected {
		if e.Kind == fsp.EvChunk && e.Text == "queued" {
			t.Fatal("the editor's prompt ran inside the injected turn")
		}
	}

	// Only then does the editor's turn start, marker first.
	queued := a.events(t)
	if first := queued[0]; first.Kind != fsp.EvChunk || first.Text != editorTurnMarker {
		t.Errorf("the queued turn started with %v %q, want %q", first.Kind, first.Text, editorTurnMarker)
	}
	if got := chunkText(queued); got != "queued" {
		t.Errorf("the queued turn mirrored %q, want %q", got, "queued")
	}

	select {
	case got := <-editorDone:
		if got.err != nil {
			t.Fatalf("the queued prompt failed: %v", got.err)
		}
		if got.resp.StopReason != acp.StopReasonEndTurn {
			t.Errorf("the queued prompt ended with %q, want %q", got.resp.StopReason, acp.StopReasonEndTurn)
		}
	case <-time.After(wait):
		t.Fatal("the queued prompt never returned")
	}
}

// TestProxyEditorApprovalDefault leaves permissions with the editor and tells
// the daemon that is where the answer is coming from.
func TestProxyEditorApprovalDefault(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{})

	r.initialize(t)
	id := r.newSession(t)
	a := d.attached(t)

	resp := r.prompt(t, id, "ask edit /tmp/whatever")
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}

	select {
	case req := <-r.editor.perms:
		if len(req.Options) != 3 {
			t.Errorf("the editor was offered %d options, want 3", len(req.Options))
		}
	default:
		t.Fatal("the editor was never asked for permission")
	}
	if got := r.editor.said(); got != "allowed" {
		t.Errorf("the agent said %q, want %q", got, "allowed")
	}

	events := a.events(t)
	var waiting *fsp.ToolInfo
	for _, e := range events {
		if e.Kind == fsp.EvTool && e.Tool != nil && e.Tool.Status == statusAwaitingEditor {
			waiting = e.Tool
		}
	}
	if waiting == nil {
		t.Fatalf("the daemon was never told a tool is waiting in the editor; it saw %v", events)
	}
	if waiting.Kind != "edit" {
		t.Errorf("tool kind = %q, want %q", waiting.Kind, "edit")
	}
	if waiting.Title == "" {
		t.Error("the tool event carried no title")
	}
}

// TestProxyRemoteApprove takes the permission away from the editor: chat
// decides, and the verdict comes back on the request's own ID.
func TestProxyRemoteApprove(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{RemoteApprove: true})

	r.initialize(t)
	id := r.newSession(t)
	a := d.attached(t)

	done := make(chan acp.PromptResponse, 1)
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), wait)
		defer stop()
		resp, _ := r.editor.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: id,
			Prompt:    []acp.ContentBlock{acp.TextBlock("ask edit /tmp/remote")},
		})
		done <- resp
	}()

	m := a.waitType(t, control.MsgApprovalRequest)
	if m.ReqID == "" {
		t.Fatal("the approval request carried no request ID")
	}
	if m.SessionID != a.resp.SessionID {
		t.Errorf("session = %q, want %q", m.SessionID, a.resp.SessionID)
	}
	if m.Approval == nil {
		t.Fatal("the approval request carried no request")
	}
	if m.Approval.ToolKind != "edit" {
		t.Errorf("tool kind = %q, want %q", m.Approval.ToolKind, "edit")
	}
	if len(m.Approval.Paths) != 1 || m.Approval.Paths[0] != "/tmp/remote" {
		t.Errorf("paths = %v, want [/tmp/remote]", m.Approval.Paths)
	}
	var allow string
	for _, o := range m.Approval.Options {
		if o.Kind == fsp.OptAllowOnce {
			allow = o.ID
		}
	}
	if allow == "" {
		t.Fatalf("no allow-once option was offered: %v", m.Approval.Options)
	}

	if err := a.send(control.Message{
		Type:      control.MsgVerdict,
		SessionID: a.resp.SessionID,
		ReqID:     m.ReqID,
		Verdict:   &fsp.Verdict{Decision: fsp.DecisionAllowOnce, OptionID: allow, By: "owner"},
	}); err != nil {
		t.Fatalf("send the verdict: %v", err)
	}

	select {
	case resp := <-done:
		if resp.StopReason != acp.StopReasonEndTurn {
			t.Errorf("stop reason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
		}
	case <-time.After(wait):
		t.Fatal("the turn never ended")
	}

	if got := r.editor.said(); got != "allowed" {
		t.Errorf("the agent said %q, want %q", got, "allowed")
	}
	select {
	case req := <-r.editor.perms:
		t.Errorf("the editor was asked for permission anyway: %v", req.ToolCall.ToolCallId)
	default:
	}
}

// TestProxyEditorDisconnect ends the run the way an editor does: it closes the
// pipe. The turn is cancelled, the daemon is told, and the agent is stopped.
func TestProxyEditorDisconnect(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{})

	r.initialize(t)
	id := r.newSession(t)
	a := d.attached(t)

	go func() {
		ctx, stop := context.WithTimeout(context.Background(), wait)
		defer stop()
		_, _ = r.editor.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: id,
			Prompt:    []acp.ContentBlock{acp.TextBlock("slow 30000")},
		})
	}()
	r.editor.waitAgentChunk(t)

	r.disconnect()

	if m := a.waitType(t, control.MsgDetach); m.SessionID != a.resp.SessionID {
		t.Errorf("detach named session %q, want %q", m.SessionID, a.resp.SessionID)
	}
	if err := r.wait(t); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

// TestProxyMultipleSessions runs two conversations through one proxy and keeps
// them apart at the daemon.
func TestProxyMultipleSessions(t *testing.T) {
	home := newHome(t)
	d := startDaemon(t, home)
	r := startRun(t, home, Options{})

	r.initialize(t)
	first := r.newSession(t)
	a1 := d.attached(t)
	second := r.newSession(t)
	a2 := d.attached(t)

	if first == second {
		t.Fatalf("the agent handed out %q twice", first)
	}
	if a1.req.AgentSessionID != string(first) || a2.req.AgentSessionID != string(second) {
		t.Fatalf("registered %q and %q, want %q and %q",
			a1.req.AgentSessionID, a2.req.AgentSessionID, first, second)
	}
	if a1.resp.SessionID == a2.resp.SessionID {
		t.Fatal("the daemon gave both sessions the same ID")
	}

	r.prompt(t, second, "echo second")
	events := a2.events(t)
	if got := chunkText(events); got != "second" {
		t.Errorf("the second session mirrored %q, want %q", got, "second")
	}
	for _, m := range drain(a2) {
		if m.SessionID != a2.resp.SessionID {
			t.Errorf("a message named session %q on the second session's connection, want %q",
				m.SessionID, a2.resp.SessionID)
		}
	}

	// Nothing of that turn went to the other session's connection.
	select {
	case m := <-a1.msgs:
		t.Errorf("the first session was sent a %s it has nothing to do with", m.Type)
	case <-time.After(200 * time.Millisecond):
	}

	r.prompt(t, first, "echo first")
	if got := chunkText(a1.events(t)); got != "first" {
		t.Errorf("the first session mirrored %q, want %q", got, "first")
	}
}

// drain returns whatever has already arrived without waiting for more.
func drain(a *attachConn) []control.Message {
	var out []control.Message
	for {
		select {
		case m, ok := <-a.msgs:
			if !ok {
				return out
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestProxyUnknownMethod checks the one thing the proxy does not forward: a
// method the SDK does not know is answered with method not found rather than
// passed to the agent.
func TestProxyUnknownMethod(t *testing.T) {
	home := newHome(t)
	startDaemon(t, home)

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Agent: "fake",
			Home:  home,
			In:    inR,
			Out:   outW,
			Err:   &syncBuffer{},
		})
		inR.Close()
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-done:
		case <-time.After(wait):
			t.Error("the proxy did not return")
		}
		outR.Close()
	})

	const line = `{"jsonrpc":"2.0","id":7,"method":"ferry/nonsense","params":{}}` + "\n"
	if _, err := inW.Write([]byte(line)); err != nil {
		t.Fatalf("write the request: %v", err)
	}

	type reply struct {
		ID    int `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	answers := make(chan reply, 1)
	go func() {
		dec := json.NewDecoder(outR)
		for {
			var got reply
			if err := dec.Decode(&got); err != nil {
				return
			}
			if got.ID == 7 {
				answers <- got
				return
			}
		}
	}()

	select {
	case got := <-answers:
		if got.Error == nil {
			t.Fatal("an unknown method was answered without an error")
		}
		if got.Error.Code != -32601 {
			t.Errorf("error code = %d, want -32601 (method not found): %s", got.Error.Code, got.Error.Message)
		}
	case <-time.After(wait):
		t.Fatal("the unknown method was never answered")
	}
}
