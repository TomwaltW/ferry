package claudechannel

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/mcpmini"
)

// waitFor bounds the test's own plumbing, not any business timeout.
const waitFor = 5 * time.Second

var (
	epoch     = time.Date(2026, time.September, 18, 9, 0, 0, 0, time.UTC)
	sessionID = "sess_channel1"
)

// fakeStream stands in for the daemon's end of the control connection.
type fakeStream struct {
	toShim   chan control.Message
	fromShim chan control.Message
	closed   chan struct{}
	once     sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		toShim:   make(chan control.Message, 16),
		fromShim: make(chan control.Message, 16),
		closed:   make(chan struct{}),
	}
}

func (f *fakeStream) Send(m control.Message) error {
	select {
	case f.fromShim <- m:
		return nil
	case <-f.closed:
		return io.EOF
	}
}

func (f *fakeStream) Recv() (control.Message, error) {
	select {
	case m := <-f.toShim:
		return m, nil
	case <-f.closed:
		return control.Message{}, io.EOF
	}
}

func (f *fakeStream) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// next waits for the next message the shim sent the daemon.
func (f *fakeStream) next(t *testing.T) control.Message {
	t.Helper()
	select {
	case m := <-f.fromShim:
		return m
	case <-time.After(waitFor):
		t.Fatal("the shim sent the daemon nothing")
		return control.Message{}
	}
}

// notes collects what the shim pushed at Claude Code.
type notes struct {
	ch chan note
}

type note struct {
	Method string
	Params json.RawMessage
}

func newNotes() *notes { return &notes{ch: make(chan note, 16)} }

func (n *notes) record(_ context.Context, method string, params json.RawMessage) {
	n.ch <- note{Method: method, Params: append(json.RawMessage(nil), params...)}
}

func (n *notes) next(t *testing.T) note {
	t.Helper()
	select {
	case got := <-n.ch:
		return got
	case <-time.After(waitFor):
		t.Fatal("the shim pushed no notification")
		return note{}
	}
}

func (n *notes) quiet(t *testing.T) {
	t.Helper()
	select {
	case got := <-n.ch:
		t.Fatalf("unexpected notification %q", got.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

// harness runs a shim with an mcpmini client in Claude Code's place.
type harness struct {
	shim  *shim
	cli   *mcpmini.Client
	st    *fakeStream
	notes *notes
	clk   *clock.Fake
}

// newHarness builds the shim either attached to a daemon or without one.
func newHarness(t *testing.T, withDaemon bool) *harness {
	t.Helper()

	shimIn, hostOut := io.Pipe()
	hostIn, shimOut := io.Pipe()

	var st *fakeStream
	var session string
	if withDaemon {
		st = newFakeStream()
		session = sessionID
	}

	clk := clock.NewFake(epoch)
	opts := shimOptions{
		Session:  session,
		Platform: "wechat",
		In:       shimIn,
		Out:      shimOut,
		Clock:    clk,
	}
	if st != nil {
		opts.Stream = st
	}
	s := newShim(opts)

	// A fixed message ID keeps meta comparable across runs.
	counter := 0
	s.newMessageID = func() string {
		counter++
		return "cmsg_" + strconv.Itoa(counter)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_ = s.run(ctx)
	}()

	n := newNotes()
	cli := mcpmini.NewClient(mcpmini.ClientOptions{
		Info:     mcpmini.Info{Name: "claude-code", Version: "test"},
		In:       hostIn,
		Out:      hostOut,
		OnNotify: n.record,
	})
	if err := cli.Start(context.Background()); err != nil {
		cancel()
		t.Fatalf("the handshake failed: %v", err)
	}

	t.Cleanup(func() {
		_ = cli.Close()
		cancel()
		_ = shimIn.Close()
		_ = shimOut.Close()
		if st != nil {
			_ = st.Close()
		}
		select {
		case <-ran:
		case <-time.After(waitFor):
			t.Error("the shim did not stop")
		}
	})

	return &harness{shim: s, cli: cli, st: st, notes: n, clk: clk}
}

// prompt hands the shim one IM message, as the daemon would.
func (h *harness) prompt(t *testing.T, text, sender string) {
	t.Helper()
	h.st.toShim <- control.Message{
		Type:      control.MsgPrompt,
		SessionID: sessionID,
		Text:      text,
		Sender:    sender,
	}
}

func (h *harness) reply(t *testing.T, args map[string]any) mcpmini.ToolResult {
	t.Helper()
	res, err := h.cli.CallTool(context.Background(), ToolReply, args)
	if err != nil {
		t.Fatalf("calling the reply tool failed at the transport level: %v", err)
	}
	return res
}

func resultText(res mcpmini.ToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func TestCapabilitiesDeclared(t *testing.T) {
	h := newHarness(t, true)

	res := h.cli.Server()
	if res == nil {
		t.Fatal("the handshake produced no result")
	}
	exp := res.Capabilities.Experimental
	if _, ok := exp[CapChannel]; !ok {
		t.Errorf("experimental = %v, want a %q entry", exp, CapChannel)
	}
	if _, ok := exp[CapChannelPermission]; !ok {
		t.Errorf("experimental = %v, want a %q entry: Ferry's ACL authenticates senders, so the relay may be declared", exp, CapChannelPermission)
	}
	// The protocol asks for empty objects, not for any payload of our own.
	for _, key := range []string{CapChannel, CapChannelPermission} {
		obj, ok := exp[key].(map[string]any)
		if !ok {
			t.Errorf("%s = %#v, want an object", key, exp[key])
			continue
		}
		if len(obj) != 0 {
			t.Errorf("%s = %v, want an empty object", key, obj)
		}
	}
	if res.Capabilities.Tools == nil {
		t.Error("capabilities.tools is absent, so Claude Code would not discover the reply tool")
	}
	if res.ServerInfo.Name != serverName {
		t.Errorf("serverInfo.name = %q, want %q: it is the source attribute of the inbound tag", res.ServerInfo.Name, serverName)
	}
}

func TestInstructionsPresent(t *testing.T) {
	h := newHarness(t, true)
	got := h.cli.Server().Instructions
	if got == "" {
		t.Fatal("the server sent no instructions")
	}
	for _, want := range []string{
		`<channel source="` + serverName + `"`,
		ToolReply,
		MetaChatID,
		"phone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the instructions do not mention %q:\n%s", want, got)
		}
	}
}

func TestInboundMetaKeys(t *testing.T) {
	h := newHarness(t, true)
	h.prompt(t, "how is the build?", "userA")

	got := h.notes.next(t)
	if got.Method != NotifyChannel {
		t.Fatalf("method = %q, want %q", got.Method, NotifyChannel)
	}
	var p inboundParams
	if err := json.Unmarshal(got.Params, &p); err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if p.Content != "how is the build?" {
		t.Errorf("content = %q, want the text the daemon sent", p.Content)
	}

	want := map[string]string{
		MetaChatID:    sessionID,
		MetaMessageID: "cmsg_1",
		MetaUser:      "userA",
		MetaUserID:    "userA",
		MetaPlatform:  "wechat",
		MetaTimestamp: strconv.FormatInt(epoch.Unix(), 10),
	}
	for k, v := range want {
		if p.Meta[k] != v {
			t.Errorf("meta[%q] = %q, want %q", k, p.Meta[k], v)
		}
	}
	if len(p.Meta) != len(want) {
		t.Errorf("meta = %v, want exactly the agreed keys %v", p.Meta, want)
	}

	// A key the host would drop silently is worse than a missing one: it looks
	// delivered from this side and never arrives.
	identifier := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	for k := range p.Meta {
		if !identifier.MatchString(k) {
			t.Errorf("meta key %q has characters the host drops silently", k)
		}
	}

	// chat_id is what the model must pass back, so it has to name a chat the
	// reply tool accepts.
	if res := h.reply(t, map[string]any{"chat_id": p.Meta[MetaChatID], "text": "green"}); res.IsError {
		t.Errorf("replying to the chat_id from meta failed: %s", resultText(res))
	}
}

func TestReplyTool(t *testing.T) {
	h := newHarness(t, true)

	tools, err := h.cli.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolReply {
		t.Fatalf("tools = %+v, want exactly one named %q", tools, ToolReply)
	}
	var schema struct {
		Type       string `json:"type"`
		Required   []string
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("parse the input schema: %v", err)
	}
	for _, field := range []string{"chat_id", "text", "files"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("the schema has no %q property", field)
		}
	}
	if schema.Properties["chat_id"].Type != "string" || schema.Properties["text"].Type != "string" {
		t.Errorf("chat_id and text must be strings, got %+v", schema.Properties)
	}
	if schema.Properties["files"].Type != "array" {
		t.Errorf("files must be an array, got %q", schema.Properties["files"].Type)
	}

	res := h.reply(t, map[string]any{"chat_id": sessionID, "text": "the build is green"})
	if res.IsError {
		t.Fatalf("the reply failed: %s", resultText(res))
	}
	if resultText(res) != replyResultText {
		t.Errorf("result = %q, want %q", resultText(res), replyResultText)
	}

	// A reply reaches the outbox as a chunk followed by the end of a turn, so
	// that it goes out as one urgent message.
	first := h.st.next(t)
	if first.Type != control.MsgEvent || first.Event == nil || first.Event.Kind != fsp.EvChunk {
		t.Fatalf("first message = %+v, want an %s event", first, fsp.EvChunk)
	}
	if first.Event.Text != "the build is green" {
		t.Errorf("chunk text = %q, want the reply text", first.Event.Text)
	}
	if first.SessionID != sessionID {
		t.Errorf("session = %q, want %q", first.SessionID, sessionID)
	}
	second := h.st.next(t)
	if second.Type != control.MsgEvent || second.Event == nil || second.Event.Kind != fsp.EvTurnEnd {
		t.Fatalf("second message = %+v, want an %s event", second, fsp.EvTurnEnd)
	}

	// Files travel with the reply rather than being dropped in silence.
	res = h.reply(t, map[string]any{
		"chat_id": sessionID,
		"text":    "here it is",
		"files":   []string{"/tmp/report.txt"},
	})
	if res.IsError {
		t.Fatalf("the reply with a file failed: %s", resultText(res))
	}
	chunk := h.st.next(t)
	if chunk.Event == nil || !strings.Contains(chunk.Event.Text, "/tmp/report.txt") {
		t.Errorf("chunk = %+v, want it to carry the file path", chunk.Event)
	}
	h.st.next(t) // the turn end
}

func TestReplyUnknownChat(t *testing.T) {
	h := newHarness(t, true)

	res := h.reply(t, map[string]any{"chat_id": "sess_somebody_else", "text": "hello?"})
	if !res.IsError {
		t.Fatalf("result = %+v, want a tool error: the chat is not one of ours", res)
	}
	if !strings.Contains(resultText(res), "sess_somebody_else") {
		t.Errorf("the error reads %q, want it to name the chat that was refused", resultText(res))
	}

	// Nothing may have gone out: a refused reply that still reached the outbox
	// would be a message sent to the wrong person.
	select {
	case m := <-h.st.fromShim:
		t.Fatalf("the shim sent %+v to the daemon for an unknown chat", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPermissionRelayAllow(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()

	if err := h.cli.Notify(ctx, NotifyPermissionRequest, permissionRequestParams{
		RequestID:    "qrstu",
		ToolName:     "Bash",
		Description:  "Run shell command",
		InputPreview: `{"command":"go test ./..."}`,
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	got := h.st.next(t)
	if got.Type != control.MsgApprovalRequest {
		t.Fatalf("message type = %q, want %q", got.Type, control.MsgApprovalRequest)
	}
	if got.Approval == nil {
		t.Fatal("the message carries no approval request")
	}
	if got.Approval.ToolName != "Bash" {
		t.Errorf("tool = %q, want Bash", got.Approval.ToolName)
	}
	if got.Approval.Description != "Run shell command" {
		t.Errorf("description = %q, want the one Claude Code sent", got.Approval.Description)
	}
	if got.Approval.InputPreview != `{"command":"go test ./..."}` {
		t.Errorf("input preview = %q, want the one Claude Code sent", got.Approval.InputPreview)
	}

	// The daemon answers, and the answer becomes the protocol's verdict.
	h.st.toShim <- control.Message{
		Type:      control.MsgVerdict,
		SessionID: sessionID,
		ReqID:     "qrstu",
		Verdict:   &fsp.Verdict{ApprovalID: "qrstu", Decision: fsp.DecisionAllowOnce, OptionID: "opt-1", By: "userA"},
	}

	n := h.notes.next(t)
	if n.Method != NotifyPermission {
		t.Fatalf("method = %q, want %q", n.Method, NotifyPermission)
	}
	var p permissionParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if p.Behavior != BehaviorAllow {
		t.Errorf("behavior = %q, want %q", p.Behavior, BehaviorAllow)
	}
	if p.RequestID != "qrstu" {
		t.Errorf("request_id = %q, want qrstu", p.RequestID)
	}
}

func TestPermissionRelayDeny(t *testing.T) {
	tests := []struct {
		name    string
		verdict *fsp.Verdict
	}{
		{"a refusal", &fsp.Verdict{ApprovalID: "qrstu", Decision: fsp.DecisionReject, OptionID: "opt-no", By: "userA"}},
		{"a timeout", &fsp.Verdict{ApprovalID: "qrstu", Decision: fsp.DecisionReject, By: "timeout"}},
		{"a cancellation", &fsp.Verdict{ApprovalID: "qrstu", Decision: fsp.DecisionReject, By: "cancel"}},
		{"an allow that picked no option", &fsp.Verdict{ApprovalID: "qrstu", Decision: fsp.DecisionAllowOnce}},
		{"no verdict at all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, true)
			ctx := context.Background()
			if err := h.cli.Notify(ctx, NotifyPermissionRequest, permissionRequestParams{
				RequestID: "qrstu", ToolName: "Write", Description: "write a file",
			}); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			h.st.next(t)

			h.st.toShim <- control.Message{
				Type: control.MsgVerdict, SessionID: sessionID, ReqID: "qrstu", Verdict: tt.verdict,
			}

			n := h.notes.next(t)
			var p permissionParams
			if err := json.Unmarshal(n.Params, &p); err != nil {
				t.Fatalf("parse params: %v", err)
			}
			if p.Behavior != BehaviorDeny {
				t.Errorf("behavior = %q, want %q", p.Behavior, BehaviorDeny)
			}
			if p.RequestID != "qrstu" {
				t.Errorf("request_id = %q, want qrstu", p.RequestID)
			}
		})
	}
}

func TestPermissionIDReused(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()

	const id = "mnpqr"
	if err := h.cli.Notify(ctx, NotifyPermissionRequest, permissionRequestParams{
		RequestID: id, ToolName: "Bash", Description: "Run shell command", InputPreview: "ls",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	got := h.st.next(t)
	// The local dialog never shows the ID, so the one Claude Code issued is the
	// only name both sides share. It has to survive into the approval.
	if got.ReqID != id {
		t.Errorf("ReqID = %q, want %q", got.ReqID, id)
	}
	if got.Approval == nil || got.Approval.ID != id {
		t.Fatalf("approval = %+v, want its ID to be %q", got.Approval, id)
	}
	if got.Approval.Session != fsp.SessionID(sessionID) {
		t.Errorf("session = %q, want %q", got.Approval.Session, sessionID)
	}
	if got.Approval.CreatedAt.IsZero() {
		t.Error("the approval has no creation time")
	}

	// And the verdict must come back under that same ID: Claude Code drops a
	// verdict whose ID it did not issue.
	h.st.toShim <- control.Message{
		Type: control.MsgVerdict, SessionID: sessionID, ReqID: id,
		Verdict: &fsp.Verdict{ApprovalID: id, Decision: fsp.DecisionAllowOnce, OptionID: "opt-1"},
	}
	n := h.notes.next(t)
	var p permissionParams
	if err := json.Unmarshal(n.Params, &p); err != nil {
		t.Fatalf("parse params: %v", err)
	}
	if p.RequestID != id {
		t.Errorf("request_id = %q, want the ID Claude Code issued (%q)", p.RequestID, id)
	}
}

func TestShimWithoutDaemon(t *testing.T) {
	h := newHarness(t, false)

	// The handshake still completes: Claude Code gets a working MCP server.
	res := h.cli.Server()
	if res == nil {
		t.Fatal("the handshake produced no result")
	}
	if _, ok := res.Capabilities.Experimental[CapChannel]; !ok {
		t.Errorf("experimental = %v, want a %q entry even without a daemon", res.Capabilities.Experimental, CapChannel)
	}
	if _, ok := res.Capabilities.Experimental[CapChannelPermission]; ok {
		t.Errorf("experimental = %v, want no %q entry: there is nobody to put the question to", res.Capabilities.Experimental, CapChannelPermission)
	}
	if res.Instructions != instructionsWithoutDaemon {
		t.Errorf("instructions = %q, want the ones that say the channel is idle", res.Instructions)
	}
	if !strings.Contains(res.Instructions, "not running") {
		t.Errorf("the instructions do not tell the model the daemon is down:\n%s", res.Instructions)
	}

	// The tool is still listed, and says so rather than pretending to send.
	tools, err := h.cli.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolReply {
		t.Fatalf("tools = %+v, want the reply tool", tools)
	}
	got := h.reply(t, map[string]any{"chat_id": "anything", "text": "hello?"})
	if !got.IsError {
		t.Errorf("result = %+v, want a tool error", got)
	}

	// Ping keeps working, which is how Claude Code checks the server is alive.
	if err := h.cli.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}

	// A permission request cannot be relayed and must not crash the shim.
	if err := h.cli.Notify(context.Background(), NotifyPermissionRequest, permissionRequestParams{
		RequestID: "qrstu", ToolName: "Bash",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	h.notes.quiet(t)
	if err := h.cli.Ping(context.Background()); err != nil {
		t.Errorf("Ping after an unrelayable permission request: %v", err)
	}
}
