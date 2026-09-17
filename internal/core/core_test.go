package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

const (
	ownerID   = "owner-1"
	memberID  = "bob"
	otherID   = "stranger"
	testAgent = "demo"
)

var epoch = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

// harness owns a daemon and every test double behind it.
type harness struct {
	t *testing.T

	deps      Deps
	d         *Daemon
	im        *fakeIM
	store     *memStore
	outbox    *fakeOutbox
	acl       *fakeACL
	approvals *fakeApprovals
	registry  *fakeRegistry
	driver    *fakeDriver
	clk       *clock.Fake

	ep   fsp.EndpointID
	root string
	seq  atomic.Int64

	cancel context.CancelFunc
	done   chan error
}

// newTestbed assembles the daemon's dependencies without starting it, so a test
// can seed the store or change the configuration first.
func newTestbed(t *testing.T) *harness {
	t.Helper()
	root := realDir(t, t.TempDir())
	ep := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "peer"}

	store := newMemStore()
	ctx := context.Background()
	mustNil(t, store.SetRole(ctx, ep, ownerID, "owner"))
	mustNil(t, store.SetRole(ctx, ep, memberID, "member"))

	h := &harness{
		t:         t,
		im:        &fakeIM{status: ports.AdapterStatus{Online: true, BotID: "bot", OwnerID: ownerID}},
		store:     store,
		outbox:    &fakeOutbox{},
		acl:       &fakeACL{allowed: map[string]bool{ownerID: true, memberID: true}, approvers: map[string]bool{ownerID: true}, code: "424242"},
		approvals: &fakeApprovals{},
		registry: &fakeRegistry{
			specs:  map[string]ports.SessionSpec{testAgent: {Agent: testAgent, Command: []string{"demo", "--acp"}}},
			agents: []ports.AgentInfo{{Name: testAgent, Display: "Demo", Source: "command", Available: true}},
		},
		driver: &fakeDriver{},
		clk:    clock.NewFake(epoch),
		ep:     ep,
		root:   root,
	}

	cfg := config.Config{
		Lang:         fsp.LangZH,
		DefaultAgent: testAgent,
		Workspace:    config.WorkspaceConfig{Roots: []string{root}},
		Outbox:       config.OutboxConfig{MergeInterval: config.Duration{Duration: 30 * time.Second}, ReplyBudget: 8},
		Limits:       config.LimitsConfig{MaxManagedSessions: 3},
	}
	h.deps = Deps{
		Config:    cfg,
		Home:      t.TempDir(),
		IM:        h.im,
		Drivers:   map[fsp.DriverKind]ports.Driver{fsp.DriverACP: h.driver},
		Registry:  h.registry,
		Store:     h.store,
		Outbox:    h.outbox,
		ACL:       h.acl,
		Approvals: h.approvals,
		Commands:  fakeCommands{},
		I18n:      fakeI18n{},
		Clock:     h.clk,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

// start runs the daemon and waits until it is reading inbound messages.
func (h *harness) start() *harness {
	h.t.Helper()
	h.d = New(h.deps)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.d.Run(ctx) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			h.t.Error("Run did not return after the context was cancelled")
		}
	})
	h.waitFor("the adapter to be running", func() bool { return h.im.channel() != nil })
	return h
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newTestbed(t).start()
}

func (h *harness) send(sender, text string, atts ...fsp.Attachment) string {
	h.t.Helper()
	id := fmt.Sprintf("msg-%d", h.seq.Add(1))
	h.sendID(sender, id, text, atts...)
	return id
}

func (h *harness) sendID(sender, id, text string, atts ...fsp.Attachment) {
	h.t.Helper()
	select {
	case h.im.channel() <- fsp.Inbound{
		Endpoint:    h.ep,
		SenderID:    sender,
		MessageID:   id,
		Text:        text,
		Attachments: atts,
		ReplyCtx:    "ctx-" + id,
		ReceivedAt:  h.clk.Now(),
	}:
	case <-time.After(3 * time.Second):
		h.t.Fatalf("inbound channel blocked while sending %q", text)
	}
}

func (h *harness) owner(text string) string { return h.send(ownerID, text) }

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s; outbox = %q", what, h.outbox.texts())
}

func (h *harness) waitText(sub string) fsp.Outbound {
	h.t.Helper()
	var found fsp.Outbound
	h.waitFor("a message containing "+sub, func() bool {
		for _, m := range h.outbox.all() {
			if strings.Contains(m.Text, sub) {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func (h *harness) countText(sub string) int {
	n := 0
	for _, text := range h.outbox.texts() {
		if strings.Contains(text, sub) {
			n++
		}
	}
	return n
}

// newSession creates a session in the workspace root and returns its agent.
func (h *harness) newSession() *fakeAgent {
	h.t.Helper()
	before := h.driver.startCount()
	h.owner("/new " + testAgent + " " + h.root)
	h.waitText("session.created|")
	h.waitFor("the agent to start", func() bool { return h.driver.startCount() == before+1 })
	return h.driver.agent(before)
}

func (h *harness) session() *session {
	return h.d.foreground(context.Background(), h.ep)
}

func (h *harness) sessionState() fsp.SessionState {
	if s := h.session(); s != nil {
		return s.State()
	}
	return ""
}

func pendingLen(s *session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func realDir(t *testing.T, dir string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return path
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func hasKind(entries []ports.AuditEntry, kind string) bool {
	for _, e := range entries {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// ---------- inbound order ----------

func TestDedupe(t *testing.T) {
	h := newHarness(t)
	h.sendID(ownerID, "dup-1", "hello")
	h.waitText("hint.no_session|")
	h.sendID(ownerID, "dup-1", "hello")  // same message ID, must be dropped
	h.sendID(ownerID, "dup-2", "hello2") // a later message proves the duplicate was handled
	h.waitFor("the second message to be answered", func() bool { return h.countText("hint.no_session|") == 2 })

	if got := h.countText("hint.no_session|"); got != 2 {
		t.Errorf("hint replies = %d, want 2 (the duplicate must not be answered)", got)
	}
}

func TestStrangerGetsPairingCode(t *testing.T) {
	h := newHarness(t)
	h.send(otherID, "/new "+testAgent+" "+h.root)
	msg := h.waitText("pair.code|")

	if want := "pair.code|zh-CN Code=424242"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.startCount() != 0 {
		t.Errorf("a stranger started %d sessions, want 0", h.driver.startCount())
	}
	if !hasKind(h.store.auditEntries(), "rejected") {
		t.Errorf("audit kinds = %v, want one entry of kind rejected", h.store.auditKinds())
	}

	// Once the pairing quota is full the stranger is told to wait instead.
	h.acl.mu.Lock()
	h.acl.offerErr = fsp.ErrQuotaExceeded
	h.acl.mu.Unlock()
	h.send(otherID, "hello again")
	h.waitText("pair.busy|")
}

func TestVerdictOnlyFromApprover(t *testing.T) {
	h := newHarness(t)
	h.send(memberID, "y abcde") // allowed, but not an approver
	h.send(ownerID, "n abcde")
	h.waitFor("the approver's verdict", func() bool { return len(h.approvals.verdicts()) == 1 })

	got := h.approvals.verdicts()
	if len(got) != 1 {
		t.Fatalf("verdicts = %+v, want exactly one", got)
	}
	if got[0].by != ownerID || got[0].id != "abcde" || got[0].decision != fsp.DecisionReject {
		t.Errorf("verdict = %+v, want {abcde reject %s}", got[0], ownerID)
	}
	if h.driver.startCount() != 0 {
		t.Error("a verdict must not reach the agent")
	}
}

func TestEscapedSlash(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.owner("//ls")
	h.waitFor("the escaped text to reach the agent", func() bool { return len(agent.prompted()) == 1 })

	if got := agent.prompted()[0]; got != "/ls" {
		t.Errorf("prompt = %q, want %q", got, "/ls")
	}
	if n := h.countText("session.list_header|"); n != 0 {
		t.Errorf("escaped message ran the ls command %d times", n)
	}
}

func TestNoSessionHint(t *testing.T) {
	h := newHarness(t)
	h.owner("what is up")
	msg := h.waitText("hint.no_session|")

	if want := "hint.no_session|zh-CN"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
}

func TestNewAndEcho(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	created := h.waitText("session.created|")
	want := fmt.Sprintf("session.created|zh-CN Agent=%s Cwd=%s Slot=1", testAgent, h.root)
	if created.Text != want {
		t.Errorf("reply = %q, want %q", created.Text, want)
	}
	if agent.spec.Cwd != h.root {
		t.Errorf("spec.Cwd = %q, want %q", agent.spec.Cwd, h.root)
	}

	h.owner("hi there")
	h.waitText("turn.accepted|")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })

	agent.emit(fsp.Event{Kind: fsp.EvChunk, Text: "hello "})
	agent.emit(fsp.Event{Kind: fsp.EvThought, Text: "secret reasoning"})
	agent.emit(fsp.Event{Kind: fsp.EvChunk, Text: "world"})
	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})

	out := h.waitText("hello world")
	if !out.Urgent {
		t.Error("the final result of a turn must be urgent")
	}
	if h.countText("secret reasoning") != 0 {
		t.Error("thoughts must never be sent")
	}
	h.waitFor("the session to go idle", func() bool { return h.sessionState() == fsp.StateIdle })
	if calls := h.im.typingCalls(); len(calls) < 2 || !calls[0] || calls[len(calls)-1] {
		t.Errorf("typing calls = %v, want it switched on and then off", calls)
	}
}

func TestOwnerTextNotPrefixed(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.owner("build it")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })

	if got := agent.prompted()[0]; got != "build it" {
		t.Errorf("prompt = %q, want the owner's text unchanged", got)
	}
}

func TestMemberTextPrefixed(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.send(memberID, "build it")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })

	want := "[from " + memberID + "] build it"
	if got := agent.prompted()[0]; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

func TestBusyMerge(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.owner("one")
	h.waitFor("the first prompt", func() bool { return len(agent.prompted()) == 1 })
	h.owner("two")
	h.owner("three")
	h.waitFor("both messages to be queued", func() bool { return pendingLen(h.session()) == 2 })

	if n := h.countText("turn.accepted|"); n != 1 {
		t.Errorf("turn.accepted replies = %d, want 1 (a busy session stays quiet)", n)
	}

	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})
	h.waitFor("the queued text to be prompted", func() bool { return len(agent.prompted()) == 2 })

	if got, want := agent.prompted()[1], "two\n\nthree"; got != want {
		t.Errorf("merged prompt = %q, want %q", got, want)
	}
}

func TestStopCancels(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	id := h.session().id()

	h.owner("long task")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	h.owner("/stop")
	h.waitFor("the turn to be cancelled", func() bool { return agent.cancelCount() == 1 })

	found := false
	for _, got := range h.approvals.cancels() {
		if got == id {
			found = true
		}
	}
	if !found {
		t.Errorf("CancelSession calls = %v, want one for %s", h.approvals.cancels(), id)
	}

	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "cancelled"})
	h.waitText("turn.stopped|")
}

func TestToolLineOnce(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.owner("read the file")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })

	tool := &fsp.ToolInfo{CallID: "call-1", Kind: "read", Title: "read main.go"}
	agent.emit(fsp.Event{Kind: fsp.EvTool, Tool: tool})
	agent.emit(fsp.Event{Kind: fsp.EvTool, Tool: &fsp.ToolInfo{CallID: "call-1", Kind: "read", Title: "read main.go", Status: "done"}})
	agent.emit(fsp.Event{Kind: fsp.EvChunk, Text: "done"})
	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})

	msg := h.waitText("▸ read main.go")
	if got := strings.Count(msg.Text, "▸ read main.go"); got != 1 {
		t.Errorf("tool line appears %d times in %q, want 1", got, msg.Text)
	}
}

// ---------- session lifecycle ----------

func TestParkOnExit(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	id := h.session().id()

	agent.exit() // the agent process goes away
	msg := h.waitText("session.parked|")

	if want := "session.parked|zh-CN Slot=1"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	h.waitFor("the record to be parked", func() bool { return h.store.states()[id] == fsp.StateParked })
	if !hasKind(h.store.auditEntries(), "session_parked") {
		t.Errorf("audit kinds = %v, want session_parked", h.store.auditKinds())
	}
	cancelled := false
	for _, got := range h.approvals.cancels() {
		if got == id {
			cancelled = true
		}
	}
	if !cancelled {
		t.Error("parking must cancel the pending approvals of the session")
	}
}

func TestUseResumes(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	id := h.session().id()
	agent.exit()
	h.waitText("session.parked|")

	h.owner("/use 1")
	msg := h.waitText("session.switched|")

	if want := "session.switched|zh-CN Slot=1"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.resumeCount() != 1 {
		t.Errorf("Resume calls = %d, want 1", h.driver.resumeCount())
	}
	h.waitFor("the session to be live again", func() bool { return h.store.states()[id] == fsp.StateIdle })
}

func TestUseFallsBackWhenUnsupported(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	agent.exit()
	h.waitText("session.parked|")

	h.driver.mu.Lock()
	h.driver.resumeErr = fsp.ErrUnsupported
	h.driver.mu.Unlock()

	h.owner("/use 1")
	msg := h.waitText("session.resume_unsupported|")

	want := fmt.Sprintf("session.resume_unsupported|zh-CN Agent=%s Slot=2", testAgent)
	if msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.startCount() != 2 {
		t.Errorf("Start calls = %d, want 2 (a fresh session replaces the parked one)", h.driver.startCount())
	}
	fresh := h.driver.agent(1)
	if fresh.spec.Cwd != h.root {
		t.Errorf("fresh session cwd = %q, want %q", fresh.spec.Cwd, h.root)
	}
}

func TestWorkspaceDenied(t *testing.T) {
	h := newHarness(t)
	outside := realDir(t, t.TempDir())
	h.owner("/new " + testAgent + " " + outside)
	msg := h.waitText("workspace.denied|")

	if want := "workspace.denied|zh-CN Dir=" + outside; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.startCount() != 0 {
		t.Error("a denied directory must not start an agent")
	}
}

func TestSymlinkEscapeDenied(t *testing.T) {
	h := newHarness(t)
	outside := realDir(t, t.TempDir())
	link := filepath.Join(h.root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	h.owner("/new " + testAgent + " " + link)
	h.waitText("workspace.denied|")

	if h.driver.startCount() != 0 {
		t.Error("a symlink out of the workspace must not start an agent")
	}

	// The same directory reached without the symlink is still allowed.
	inside := filepath.Join(h.root, "project")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	h.owner("/new " + testAgent + " " + inside)
	h.waitText("session.created|")
}

func TestSessionLimit(t *testing.T) {
	h := newTestbed(t)
	h.deps.Config.Limits.MaxManagedSessions = 1
	h.start()

	h.newSession()
	h.owner("/new " + testAgent + " " + h.root)
	msg := h.waitText("session.limit|")

	if want := "session.limit|zh-CN Max=1"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.startCount() != 1 {
		t.Errorf("Start calls = %d, want 1", h.driver.startCount())
	}
}

// ---------- commands ----------

func TestLangSwitch(t *testing.T) {
	h := newHarness(t)
	h.owner("/lang en")
	msg := h.waitText("lang.switched|")

	if want := "lang.switched|en"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if got, err := h.store.GetKV(context.Background(), langKey(h.ep)); err != nil || got != string(fsp.LangEN) {
		t.Errorf("stored language = %q (err %v), want %q", got, err, fsp.LangEN)
	}

	// Everything the endpoint sees afterwards is English.
	h.owner("still no session")
	h.waitText("hint.no_session|en")
}

func TestPairCommand(t *testing.T) {
	h := newHarness(t)
	peer := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "carol"}
	h.acl.mu.Lock()
	h.acl.confirmEp, h.acl.confirmWho = peer, "carol"
	h.acl.mu.Unlock()

	h.send(memberID, "/pair 123456") // not an approver: ignored
	h.owner("/pair 123456")
	owner := h.waitText("pair.confirmed_owner|")

	if want := "pair.confirmed_owner|zh-CN Sender=carol"; owner.Text != want {
		t.Errorf("reply to the approver = %q, want %q", owner.Text, want)
	}
	confirmed := h.waitText("pair.confirmed|zh-CN")
	if confirmed.Endpoint != peer {
		t.Errorf("confirmation went to %v, want %v", confirmed.Endpoint, peer)
	}
	if n := h.countText("pair.confirmed_owner|"); n != 1 {
		t.Errorf("pair confirmations = %d, want 1 (a member's /pair is ignored)", n)
	}

	// An unknown code is reported back.
	h.acl.mu.Lock()
	h.acl.confirmErr = fsp.ErrNotFound
	h.acl.mu.Unlock()
	h.owner("/pair 999999")
	h.waitText("pair.invalid|")
}

// ---------- start-up and platform failures ----------

func TestRestartParksAll(t *testing.T) {
	h := newTestbed(t)
	ctx := context.Background()
	live := fsp.Session{ID: "sess-live", Driver: fsp.DriverACP, Agent: testAgent, Cwd: h.root, Origin: fsp.OriginManaged, State: fsp.StateRunning, Owner: h.ep, CreatedAt: epoch}
	idle := fsp.Session{ID: "sess-idle", Driver: fsp.DriverACP, Agent: testAgent, Cwd: h.root, Origin: fsp.OriginManaged, State: fsp.StateIdle, Owner: h.ep, CreatedAt: epoch}
	gone := fsp.Session{ID: "sess-gone", Driver: fsp.DriverACP, Agent: testAgent, Cwd: h.root, Origin: fsp.OriginManaged, State: fsp.StateClosed, Owner: h.ep, CreatedAt: epoch}
	for _, rec := range []fsp.Session{live, idle, gone} {
		mustNil(t, h.store.PutSession(ctx, rec))
		if rec.State != fsp.StateClosed {
			if _, err := h.store.Bind(ctx, h.ep, rec.ID); err != nil {
				t.Fatalf("Bind: %v", err)
			}
		}
	}
	mustNil(t, h.store.SetForeground(ctx, h.ep, live.ID))
	h.start()

	h.waitFor("the stale sessions to be parked", func() bool {
		states := h.store.states()
		return states["sess-live"] == fsp.StateParked && states["sess-idle"] == fsp.StateParked
	})
	if got := h.store.states()["sess-gone"]; got != fsp.StateClosed {
		t.Errorf("closed session became %q, want it left closed", got)
	}
	if got, want := h.acl.ownerTuple(), [3]string{string(fsp.PlatformFake), "bot", ownerID}; got != want {
		t.Errorf("SetOwner called with %v, want %v", got, want)
	}

	// A parked session tells the user how to bring it back instead of prompting.
	h.owner("are you there")
	h.waitText("session.parked|")
}

func TestIMExpiredRetries(t *testing.T) {
	h := newTestbed(t)
	h.im.errs = []error{fsp.ErrSessionExpired}
	h.start()

	h.waitFor("the expired session to be recorded", func() bool {
		return hasKind(h.store.auditEntries(), "expired")
	})
	if got := h.d.Status(); got.IMOnline {
		t.Error("Status reports the adapter online while it is waiting to retry")
	}

	h.clk.BlockUntil(1) // the daemon is waiting out the retry interval
	h.clk.Advance(imRetryInterval)
	h.waitFor("the adapter to be restarted", func() bool { return h.im.runCount() == 2 })

	// The daemon survived: it still answers messages.
	h.owner("anyone home")
	h.waitText("hint.no_session|")
	if got := h.d.Status(); !got.IMOnline {
		t.Error("Status still reports offline after a successful retry")
	}
}

// ---------- further command and buffering behaviour ----------

func TestMergeIntervalFlush(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.owner("write something long")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })

	agent.emit(fsp.Event{Kind: fsp.EvChunk, Text: "partial output"})
	h.clk.BlockUntil(1) // the session's merge ticker
	h.clk.Advance(h.d.mergeInterval())

	msg := h.waitText("partial output")
	if msg.Urgent {
		t.Error("a merge-window flush must not be urgent")
	}
	if msg.Session != h.session().id() {
		t.Errorf("message session = %q, want %q", msg.Session, h.session().id())
	}
}

func TestCloseUnbinds(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	id := h.session().id()

	h.owner("/close")
	msg := h.waitText("session.closed|")

	if want := "session.closed|zh-CN Slot=1"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	h.waitFor("the record to be closed", func() bool { return h.store.states()[id] == fsp.StateClosed })
	if !agent.isClosed() {
		t.Error("the agent session was left open")
	}
	bindings, err := h.store.Bindings(context.Background(), h.ep)
	if err != nil || len(bindings) != 0 {
		t.Errorf("bindings = %+v (err %v), want none", bindings, err)
	}
	if n := h.countText("session.parked|"); n != 0 {
		t.Error("closing a session must not also park it")
	}
}

func TestLsAndAgents(t *testing.T) {
	h := newHarness(t)
	h.newSession()

	h.owner("/ls")
	list := h.waitText("session.list_header|")
	wantItem := fmt.Sprintf("session.list_item|zh-CN Agent=%s Cwd=%s Mark=▶ Slot=1 State=session.state.idle|zh-CN", testAgent, h.root)
	if !strings.Contains(list.Text, wantItem) {
		t.Errorf("session list = %q, want a line %q", list.Text, wantItem)
	}

	h.owner("/agents")
	agents := h.waitText("agents.list_header|")
	wantAgent := fmt.Sprintf("agents.list_item|zh-CN Display=Demo Name=%s Status=agents.available|zh-CN", testAgent)
	if !strings.Contains(agents.Text, wantAgent) {
		t.Errorf("agent list = %q, want a line %q", agents.Text, wantAgent)
	}

	h.owner("/pull")
	h.waitFor("the outbox to be asked for the next batch", func() bool { return h.outbox.pullCount() == 1 })
	h.owner("/help")
	h.waitText("help.text|")
}
