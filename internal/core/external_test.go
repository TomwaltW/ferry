package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The second entry point of the multi-platform tests. Its platform is not one
// of the real ones on purpose: what matters is that it is a different one.
const (
	platform2 = fsp.Platform("fake2")
	owner2    = "owner-2"
)

// ---------- the external process's end of a control stream ----------

// fakeStream is what an attached process sees: what it writes reaches the
// daemon's Recv, and what the daemon sends lands in its own queue.
type fakeStream struct {
	t *testing.T

	toDaemon   chan control.Message
	fromDaemon chan control.Message

	once   sync.Once
	closed chan struct{}
}

func newFakeStream(t *testing.T) *fakeStream {
	return &fakeStream{
		t:          t,
		toDaemon:   make(chan control.Message, 16),
		fromDaemon: make(chan control.Message, 16),
		closed:     make(chan struct{}),
	}
}

// Send is the daemon writing to the external process.
func (f *fakeStream) Send(m control.Message) error {
	select {
	case <-f.closed:
		return io.EOF
	default:
	}
	select {
	case f.fromDaemon <- m:
		return nil
	case <-f.closed:
		return io.EOF
	}
}

// Recv is the daemon reading from the external process.
func (f *fakeStream) Recv() (control.Message, error) {
	select {
	case m := <-f.toDaemon:
		return m, nil
	case <-f.closed:
		return control.Message{}, io.EOF
	}
}

// write is the external process sending one frame to the daemon.
func (f *fakeStream) write(m control.Message) { f.toDaemon <- m }

// read waits for the next frame the daemon sent.
func (f *fakeStream) read() control.Message {
	f.t.Helper()
	select {
	case m := <-f.fromDaemon:
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("the daemon sent nothing")
		return control.Message{}
	}
}

// readOf waits for the next frame of a given type, skipping the housekeeping
// ones a test does not care about.
func (f *fakeStream) readOf(want string) control.Message {
	f.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-f.fromDaemon:
			if m.Type == want {
				return m
			}
		case <-deadline:
			f.t.Fatalf("the daemon never sent a %s frame", want)
			return control.Message{}
		}
	}
}

// quiet reports that the daemon sent nothing more.
func (f *fakeStream) quiet() bool {
	select {
	case <-f.fromDaemon:
		return false
	default:
		return true
	}
}

// disconnect is the external process going away without saying goodbye.
func (f *fakeStream) disconnect() { f.once.Do(func() { close(f.closed) }) }

var _ control.ServerStream = (*fakeStream)(nil)

// ---------- harness helpers ----------

// attach registers an external session the way the control socket would.
func (h *harness) attach(req control.AttachRequest) (control.AttachResponse, *fakeStream) {
	h.t.Helper()
	stream := newFakeStream(h.t)
	resp, err := h.d.ControlHandler().Attach(context.Background(), req, stream)
	if err != nil {
		h.t.Fatalf("Attach(%+v): %v", req, err)
	}
	h.t.Cleanup(stream.disconnect)
	return resp, stream
}

// externalEP is the endpoint external sessions report to: the owner of the
// primary entry point.
func (h *harness) externalEP() fsp.EndpointID {
	st := h.im.Status()
	return fsp.EndpointID{Platform: h.im.Platform(), BotID: st.BotID, PeerID: st.OwnerID}
}

// sendTo puts an inbound message on a specific endpoint, which the external
// tests need because their endpoint is the owner's chat, not the harness's.
func (h *harness) sendTo(ep fsp.EndpointID, im *fakeIM, sender, text string) {
	h.t.Helper()
	id := fmt.Sprintf("msg-%d", h.seq.Add(1))
	select {
	case im.channel() <- fsp.Inbound{
		Endpoint:   ep,
		SenderID:   sender,
		MessageID:  id,
		Text:       text,
		ReplyCtx:   "ctx-" + id,
		ReceivedAt: h.clk.Now(),
	}:
	case <-time.After(3 * time.Second):
		h.t.Fatalf("inbound channel blocked while sending %q", text)
	}
}

// newExternalHarness starts a daemon whose owner chat is ready to receive an
// attached session.
func newExternalHarness(t *testing.T) *harness {
	t.Helper()
	h := newTestbed(t)
	ep := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: ownerID}
	mustNil(t, h.store.SetRole(context.Background(), ep, ownerID, "owner"))
	return h.start()
}

// newMultiHarness starts a daemon serving two entry points at once.
func newMultiHarness(t *testing.T) *harness {
	t.Helper()
	h := newTestbed(t)
	h.im2 = &fakeIM{
		platform: platform2,
		status:   ports.AdapterStatus{Online: true, BotID: "bot2", OwnerID: owner2},
	}
	h.deps.IM = nil
	h.deps.IMs = []ports.IMAdapter{h.im, h.im2}
	h.acl.allowed[owner2] = true
	h.acl.approvers[owner2] = true
	h.start()
	h.waitFor("the second adapter to be running", func() bool { return h.im2.channel() != nil })
	return h
}

// ---------- several entry points ----------

func TestMultiIMIsolation(t *testing.T) {
	h := newMultiHarness(t)
	ep1 := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "peer1"}
	ep2 := fsp.EndpointID{Platform: platform2, BotID: "bot2", PeerID: "peer2"}
	ctx := context.Background()
	mustNil(t, h.store.SetRole(ctx, ep1, ownerID, "owner"))
	mustNil(t, h.store.SetRole(ctx, ep2, owner2, "owner"))

	h.sendTo(ep1, h.im, ownerID, "/new "+testAgent+" "+h.root)
	h.waitFor("the first session to start", func() bool { return h.driver.startCount() == 1 })
	agent1 := h.driver.agent(0)

	h.sendTo(ep2, h.im2, owner2, "/new "+testAgent+" "+h.root)
	h.waitFor("the second session to start", func() bool { return h.driver.startCount() == 2 })
	agent2 := h.driver.agent(1)

	// Each chat writes to its own session and to no other.
	h.sendTo(ep1, h.im, ownerID, "for the first")
	h.waitFor("the first agent to be prompted", func() bool { return len(agent1.prompted()) == 1 })
	h.sendTo(ep2, h.im2, owner2, "for the second")
	h.waitFor("the second agent to be prompted", func() bool { return len(agent2.prompted()) == 1 })

	if got := agent1.prompted(); got[0] != "for the first" {
		t.Errorf("the first agent read %q, want %q", got[0], "for the first")
	}
	if got := agent2.prompted(); got[0] != "for the second" {
		t.Errorf("the second agent read %q, want %q", got[0], "for the second")
	}
	if n := len(agent1.prompted()); n != 1 {
		t.Errorf("the first agent was prompted %d times, want 1", n)
	}

	// The sessions of one entry point are invisible from the other.
	for _, tc := range []struct {
		ep    fsp.EndpointID
		im    *fakeIM
		who   string
		agent *fakeAgent
	}{{ep1, h.im, ownerID, agent1}, {ep2, h.im2, owner2, agent2}} {
		bindings, err := h.store.Bindings(ctx, tc.ep)
		if err != nil {
			t.Fatalf("bindings of %s: %v", tc.ep, err)
		}
		if len(bindings) != 1 {
			t.Errorf("%s has %d sessions, want exactly its own", tc.ep, len(bindings))
		}
	}
}

func TestOwnerPerPlatform(t *testing.T) {
	h := newMultiHarness(t)
	h.waitFor("both owners to be adopted", func() bool { return len(h.acl.ownerTuples()) == 2 })

	got := h.acl.ownerTuples()
	want := map[string][3]string{
		string(fsp.PlatformFake): {string(fsp.PlatformFake), "bot", ownerID},
		string(platform2):        {string(platform2), "bot2", owner2},
	}
	seen := map[string][3]string{}
	for _, o := range got {
		seen[o[0]] = o
	}
	for platform, w := range want {
		if seen[platform] != w {
			t.Errorf("owner of %s = %v, want %v", platform, seen[platform], w)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("owners were set for %v, want one per platform", got)
	}
}

// ---------- attaching ----------

func TestAttachProxySession(t *testing.T) {
	h := newExternalHarness(t)
	req := control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root,
		AgentSessionID: "acp-7", PID: 4242, AllowInject: true, RemoteApprove: true,
	}
	resp, _ := h.attach(req)

	ep := h.externalEP()
	if resp.Endpoint != ep.String() {
		t.Errorf("endpoint = %q, want the owner chat %q", resp.Endpoint, ep)
	}
	if resp.Slot != 1 {
		t.Errorf("slot = %d, want 1", resp.Slot)
	}
	if resp.SessionID == "" {
		t.Fatal("Attach returned no session ID")
	}

	rec, err := h.store.GetSession(context.Background(), fsp.SessionID(resp.SessionID))
	if err != nil {
		t.Fatalf("the session was not recorded: %v", err)
	}
	if rec.Origin != fsp.OriginTerminal {
		t.Errorf("origin = %q, want %q", rec.Origin, fsp.OriginTerminal)
	}
	if rec.Driver != fsp.DriverProxy {
		t.Errorf("driver = %q, want %q", rec.Driver, fsp.DriverProxy)
	}
	if rec.Agent != testAgent || rec.Cwd != h.root || rec.AgentSessionID != "acp-7" {
		t.Errorf("record = %+v, want the attach request's agent, cwd and agent session", rec)
	}
	if rec.Owner != ep {
		t.Errorf("owner = %v, want %v", rec.Owner, ep)
	}

	notice := h.waitText("proxy.attached|")
	if !strings.Contains(notice.Text, "Agent="+testAgent) || !strings.Contains(notice.Text, "Slot=1") {
		t.Errorf("the notice was %q, want it to name the agent and the slot", notice.Text)
	}

	t.Run("a second session gets its own slot", func(t *testing.T) {
		second, _ := h.attach(control.AttachRequest{Kind: control.KindChannel, Agent: "claude", Cwd: h.root})
		if second.Slot != 2 {
			t.Errorf("slot = %d, want 2", second.Slot)
		}
		h.waitText("channel.attached|")
	})
}

func TestAttachWithoutAnOwnerIsRefused(t *testing.T) {
	h := newTestbed(t)
	h.im.status = ports.AdapterStatus{Online: true, BotID: "bot"} // nobody signed in
	h.start()

	_, err := h.d.ControlHandler().Attach(context.Background(),
		control.AttachRequest{Kind: control.KindProxy, Agent: testAgent, Cwd: h.root}, newFakeStream(t))
	if !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("Attach without an owner = %v, want fsp.ErrNotFound", err)
	}
}

// ---------- mirroring ----------

func TestExternalEventsToOutbox(t *testing.T) {
	h := newExternalHarness(t)
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root, AllowInject: true,
	})
	id := fsp.SessionID(resp.SessionID)

	stream.write(control.Message{Type: control.MsgEvent, SessionID: resp.SessionID, Event: &fsp.Event{
		Kind: fsp.EvChunk, Text: "the editor asked for a refactor",
	}})
	stream.write(control.Message{Type: control.MsgEvent, SessionID: resp.SessionID, Event: &fsp.Event{
		Kind: fsp.EvTurnEnd, StopReason: "end_turn",
	}})

	msg := h.waitText("the editor asked for a refactor")
	if msg.Endpoint != h.externalEP() {
		t.Errorf("the turn went to %v, want the owner chat %v", msg.Endpoint, h.externalEP())
	}
	if msg.Session != id {
		t.Errorf("the message carries session %q, want %q", msg.Session, id)
	}
	h.waitFor("the session to go idle", func() bool {
		s := h.d.lookup(id)
		return s != nil && s.State() == fsp.StateIdle
	})
}

func TestExternalApprovalRequest(t *testing.T) {
	h := newExternalHarness(t)
	h.approvals.verdict = fsp.Verdict{
		ApprovalID: "abcde", Decision: fsp.DecisionAllowOnce, OptionID: "opt-1", By: ownerID,
	}
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root,
		AllowInject: true, RemoteApprove: true,
	})

	stream.write(control.Message{
		Type:      control.MsgApprovalRequest,
		SessionID: resp.SessionID,
		ReqID:     "req-1",
		Approval: &fsp.ApprovalRequest{
			ToolName: "write_file", ToolKind: "edit", Description: "write main.go",
		},
	})

	verdict := stream.readOf(control.MsgVerdict)
	if verdict.ReqID != "req-1" {
		t.Errorf("the verdict carries request %q, want %q untouched", verdict.ReqID, "req-1")
	}
	if verdict.Verdict == nil || verdict.Verdict.Decision != fsp.DecisionAllowOnce {
		t.Errorf("the verdict is %+v, want the decision chat gave", verdict.Verdict)
	}
	if verdict.SessionID != resp.SessionID {
		t.Errorf("the verdict names session %q, want %q", verdict.SessionID, resp.SessionID)
	}

	asked := h.approvals.askCount()
	if asked != 1 {
		t.Fatalf("chat was asked %d times, want once", asked)
	}
}

func TestPTYNoApprovalNotice(t *testing.T) {
	h := newExternalHarness(t)
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindPTY, Agent: "top", Title: "top", Cwd: h.root,
	})

	h.waitText("pty.attached|")
	h.waitFor("the notice about approvals", func() bool { return h.countText("pty.no_approval|") == 1 })

	// A wrapped program that does ask gets the same answer, and chat is never
	// asked to decide.
	stream.write(control.Message{
		Type:      control.MsgApprovalRequest,
		SessionID: resp.SessionID,
		ReqID:     "req-1",
		Approval:  &fsp.ApprovalRequest{ToolName: "rm", ToolKind: "delete"},
	})
	h.waitFor("the second notice", func() bool { return h.countText("pty.no_approval|") == 2 })

	if n := h.approvals.askCount(); n != 0 {
		t.Errorf("chat was asked %d times, want never for a wrapped terminal program", n)
	}
	if !stream.quiet() {
		t.Error("the daemon answered a wrapped program's approval request")
	}
}

// ---------- injecting chat input ----------

func TestInjectDisabled(t *testing.T) {
	h := newExternalHarness(t)
	_, stream := h.attach(control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root, AllowInject: false,
	})
	h.waitText("proxy.attached|")

	h.sendTo(h.externalEP(), h.im, ownerID, "please stop")
	h.waitText("inject.disabled|")

	if !stream.quiet() {
		t.Error("a session that takes no input was prompted anyway")
	}
}

func TestInjectWaitsForIdle(t *testing.T) {
	h := newExternalHarness(t)
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root, AllowInject: true,
	})
	ep := h.externalEP()
	id := fsp.SessionID(resp.SessionID)

	h.sendTo(ep, h.im, ownerID, "first")
	if got := stream.readOf(control.MsgPrompt); got.Text != "first" {
		t.Fatalf("the process read %q, want %q", got.Text, "first")
	}
	h.waitFor("the session to be running", func() bool {
		s := h.d.lookup(id)
		return s != nil && s.State() == fsp.StateRunning
	})

	// Busy: the second message waits rather than interrupting the turn.
	h.sendTo(ep, h.im, ownerID, "second")
	h.waitFor("the second message to be queued", func() bool {
		s := h.d.lookup(id)
		return s != nil && pendingLen(s) == 1
	})
	if !stream.quiet() {
		t.Fatal("the second message was sent while the turn was still running")
	}

	// The turn ends and the queue goes over as one prompt.
	stream.write(control.Message{Type: control.MsgEvent, SessionID: resp.SessionID, Event: &fsp.Event{
		Kind: fsp.EvTurnEnd, StopReason: "end_turn",
	}})
	if got := stream.readOf(control.MsgPrompt); got.Text != "second" {
		t.Errorf("the process read %q once idle, want %q", got.Text, "second")
	}
}

// ---------- the end of an external session ----------

func TestExternalDetachCloses(t *testing.T) {
	h := newExternalHarness(t)
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindProxy, Agent: testAgent, Cwd: h.root, AllowInject: true,
	})
	id := fsp.SessionID(resp.SessionID)
	h.waitText("proxy.attached|")

	stream.write(control.Message{Type: control.MsgDetach, SessionID: resp.SessionID})

	notice := h.waitText("session.external_closed|")
	if !strings.Contains(notice.Text, "Kind=proxy") || !strings.Contains(notice.Text, "Slot=1") {
		t.Errorf("the notice was %q, want it to name the kind and the slot", notice.Text)
	}
	h.waitFor("the session to be closed", func() bool {
		return h.store.states()[id] == fsp.StateClosed
	})
	if s := h.d.lookup(id); s != nil {
		t.Error("a closed external session is still live")
	}
	bindings, err := h.store.Bindings(context.Background(), h.externalEP())
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("the chat still has %d sessions bound, want none", len(bindings))
	}

	t.Run("a dropped connection ends the session too", func(t *testing.T) {
		resp, stream := h.attach(control.AttachRequest{
			Kind: control.KindPTY, Agent: "top", Cwd: h.root,
		})
		h.waitText("pty.attached|")
		before := h.countText("session.external_closed|")

		stream.disconnect()
		h.waitFor("the second close notice", func() bool {
			return h.countText("session.external_closed|") == before+1
		})
		h.waitFor("the session to be closed", func() bool {
			return h.store.states()[fsp.SessionID(resp.SessionID)] == fsp.StateClosed
		})
	})
}
