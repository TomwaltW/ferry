package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fakeAgentBin is the ferry-fakeagent built once for the whole package. Every
// test drives that scripted agent; no real agent is ever started.
var fakeAgentBin string

// eventWait is how long a test waits for an event before giving up. It is far
// longer than anything the fake agent takes, so a timeout means a real hang.
const eventWait = 15 * time.Second

func TestMain(m *testing.M) {
	// The SDK logs connection teardown through the default handler, and its
	// own SetLogger races with the receive goroutine.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	dir, err := os.MkdirTemp("", "ferry-acp-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp test: create a temporary directory:", err)
		os.Exit(1)
	}

	fakeAgentBin = filepath.Join(dir, "ferry-fakeagent"+acpExeSuffix)
	build := exec.Command("go", "build", "-o", fakeAgentBin, "github.com/TomwaltW/ferry/cmd/ferry-fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "acp test: build ferry-fakeagent: %v\n%s", err, out)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testDriver is a driver with the startup and shutdown timings shortened, so a
// test never waits out a production grace period.
func testDriver(t *testing.T) *driver {
	t.Helper()
	d := New(testLogger()).(*driver)
	d.closeGrace = 500 * time.Millisecond
	return d
}

// agentSpec points at the fake agent, persisting its sessions under home so a
// later resume or list can find them.
func agentSpec(t *testing.T, home string) ports.SessionSpec {
	t.Helper()
	return ports.SessionSpec{
		Agent:   "fake",
		Command: []string{fakeAgentBin},
		Cwd:     t.TempDir(),
		Env:     []string{"FAKEAGENT_HOME=" + home, "PATH=" + os.Getenv("PATH")},
	}
}

// askFunc answers one permission request.
type askFunc func(context.Context, fsp.ApprovalRequest) (fsp.Verdict, error)

// approver is a ports.Approver that hands each request to the test and records
// what it was asked.
type approver struct {
	ask  askFunc
	seen chan fsp.ApprovalRequest
}

func newApprover(ask askFunc) *approver {
	return &approver{ask: ask, seen: make(chan fsp.ApprovalRequest, 8)}
}

func (a *approver) Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	select {
	case a.seen <- req:
	default:
	}
	return a.ask(ctx, req)
}

// nextEvent returns the next event, failing the test if the channel goes quiet
// or closes early.
func nextEvent(t *testing.T, ch <-chan fsp.Event) fsp.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the event channel closed before the event arrived")
		}
		return e
	case <-time.After(eventWait):
		t.Fatal("timed out waiting for an event")
		return fsp.Event{}
	}
}

// untilTurnEnd collects events until the turn ends, returning them all.
func untilTurnEnd(t *testing.T, ch <-chan fsp.Event) []fsp.Event {
	t.Helper()
	var got []fsp.Event
	for {
		e := nextEvent(t, ch)
		got = append(got, e)
		if e.Kind == fsp.EvTurnEnd {
			return got
		}
	}
}

// chunksOf returns the text of the agent message chunks among events.
func chunksOf(events []fsp.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == fsp.EvChunk {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestStartAndEcho(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if s.AgentSessionID() == "" {
		t.Error("AgentSessionID is empty")
	}
	caps := s.Caps()
	if !caps.Load || !caps.List || !caps.Cancel {
		t.Errorf("Caps = %+v, want load, list and cancel from the fake agent", caps)
	}

	if err := s.Prompt(t.Context(), "echo hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	events := untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "hello" {
		t.Errorf("the agent said %q, want %q", got, "hello")
	}
	end := events[len(events)-1]
	if end.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", end.StopReason)
	}
	if end.At.IsZero() {
		t.Error("the turn end has no timestamp")
	}
	if end.Session != "" {
		t.Errorf("Session = %q, want it left for the core to fill in", end.Session)
	}
}

func TestStreamingChunks(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "burst 3"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	events := untilTurnEnd(t, s.Events())
	chunks := chunksOf(events)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3 separate ones: %q", len(chunks), chunks)
	}
	for i, c := range chunks {
		if !strings.HasSuffix(c, "\n") || len(strings.TrimSuffix(c, "\n")) != 60 {
			t.Errorf("chunk %d is %d characters, want a 60 character line", i, len(c))
		}
	}
}

func TestPermissionRoundTrip(t *testing.T) {
	ap := newApprover(func(_ context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
		return fsp.Verdict{Decision: fsp.DecisionAllowOnce, OptionID: "allow-once", By: "tester"}, nil
	})

	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), ap)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "ask edit /tmp/ferry-demo.go"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	events := untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "allowed" {
		t.Errorf("the agent said %q, want %q", got, "allowed")
	}

	select {
	case req := <-ap.seen:
		if req.ToolKind != "edit" {
			t.Errorf("ToolKind = %q, want edit", req.ToolKind)
		}
		if req.ToolName == "" || req.Description != req.ToolName {
			t.Errorf("ToolName %q Description %q, want both to carry the tool call title", req.ToolName, req.Description)
		}
		if len(req.Paths) != 1 || req.Paths[0] != "/tmp/ferry-demo.go" {
			t.Errorf("Paths = %v, want the tool call's location", req.Paths)
		}
		if len(req.Options) != 3 {
			t.Fatalf("got %d options, want the agent's 3", len(req.Options))
		}
		if req.Options[0].ID != "allow-once" || req.Options[0].Kind != fsp.OptAllowOnce {
			t.Errorf("first option = %+v, want allow-once mapped to allow_once", req.Options[0])
		}
		if req.Options[2].Kind != fsp.OptRejectOnce {
			t.Errorf("third option kind = %q, want reject_once", req.Options[2].Kind)
		}
		if req.ID != "" || req.Session != "" {
			t.Errorf("ID %q Session %q, want both left to the core", req.ID, req.Session)
		}
	default:
		t.Fatal("the approver was never asked")
	}
}

func TestPermissionCancelledOnStop(t *testing.T) {
	asked := make(chan struct{})
	released := make(chan error, 1)
	ap := newApprover(func(ctx context.Context, _ fsp.ApprovalRequest) (fsp.Verdict, error) {
		close(asked)
		<-ctx.Done()
		released <- ctx.Err()
		// A verdict with no option is how the driver is told to answer
		// cancelled, which is what a stopped turn amounts to.
		return fsp.Verdict{}, nil
	})

	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), ap)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "ask execute /tmp/ferry-demo"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	select {
	case <-asked:
	case <-time.After(eventWait):
		t.Fatal("the approver was never asked")
	}

	if err := s.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	select {
	case err := <-released:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the approver's context ended with %v, want context.Canceled", err)
		}
	case <-time.After(eventWait):
		t.Fatal("Cancel did not release the approver")
	}

	events := untilTurnEnd(t, s.Events())
	if end := events[len(events)-1]; end.StopReason != "cancelled" {
		t.Errorf("StopReason = %q, want cancelled", end.StopReason)
	}
}

func TestBusy(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "slow 3000"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	if err := s.Prompt(t.Context(), "echo nope"); !errors.Is(err, fsp.ErrBusy) {
		t.Errorf("a second Prompt returned %v, want fsp.ErrBusy", err)
	}

	// Cancelling only means anything once the turn is under way: the first
	// tick of the slow turn says session/prompt has reached the agent.
	if e := nextEvent(t, s.Events()); e.Kind != fsp.EvChunk {
		t.Fatalf("first event was %q, want the slow turn's first tick", e.Kind)
	}
	if err := s.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	if end := events[len(events)-1]; end.StopReason != "cancelled" {
		t.Errorf("StopReason = %q, want cancelled", end.StopReason)
	}

	// Once the turn has ended the session takes prompts again.
	if err := s.Prompt(t.Context(), "echo back"); err != nil {
		t.Errorf("Prompt after the turn ended failed: %v", err)
	}
	untilTurnEnd(t, s.Events())
}

func TestProcessExitEmitsError(t *testing.T) {
	d := testDriver(t)
	sess, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sess.Close()

	// Kill the agent behind the driver's back: that is what an agent crashing
	// looks like from here.
	sess.(*session).proc.kill()

	e := nextEvent(t, sess.Events())
	if e.Kind != fsp.EvError {
		t.Fatalf("first event after the exit was %q, want %q", e.Kind, fsp.EvError)
	}
	if e.Err == "" {
		t.Error("the error event says nothing about why the agent went away")
	}

	select {
	case _, ok := <-sess.Events():
		if ok {
			t.Error("another event arrived after the error, want the channel closed")
		}
	case <-time.After(eventWait):
		t.Fatal("the event channel stayed open after the agent exited")
	}
}

func TestResumeViaLoad(t *testing.T) {
	home := t.TempDir()
	d := testDriver(t)
	spec := agentSpec(t, home)

	first, err := d.Start(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := first.Prompt(t.Context(), "echo remembered"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	untilTurnEnd(t, first.Events())
	id := first.AgentSessionID()
	if err := first.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	second, err := d.Resume(t.Context(), spec, id, nil)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	defer second.Close()

	if second.AgentSessionID() != id {
		t.Errorf("AgentSessionID = %q, want the resumed %q", second.AgentSessionID(), id)
	}
	// The replayed history is delivered before session/load returns, so by now
	// anything that leaked would already be queued.
	select {
	case e := <-second.Events():
		t.Fatalf("the replayed history leaked out as %+v", e)
	default:
	}

	if err := second.Prompt(t.Context(), "echo afterwards"); err != nil {
		t.Fatalf("Prompt after resume failed: %v", err)
	}
	events := untilTurnEnd(t, second.Events())
	if got := strings.Join(chunksOf(events), ""); got != "afterwards" {
		t.Errorf("the resumed session said %q, want only the new turn's %q", got, "afterwards")
	}
}

func TestListSessions(t *testing.T) {
	home := t.TempDir()
	d := testDriver(t)
	spec := agentSpec(t, home)

	want := make(map[string]bool, 2)
	for _, text := range []string{"echo first session", "echo second session"} {
		s, err := d.Start(t.Context(), spec, nil)
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		if err := s.Prompt(t.Context(), text); err != nil {
			t.Fatalf("Prompt failed: %v", err)
		}
		untilTurnEnd(t, s.Events())
		want[s.AgentSessionID()] = true
		if err := s.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}

	infos, err := d.List(t.Context(), spec)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(infos) != len(want) {
		t.Fatalf("List returned %d sessions, want %d", len(infos), len(want))
	}
	for _, info := range infos {
		if !want[info.AgentSessionID] {
			t.Errorf("List returned an unknown session %q", info.AgentSessionID)
		}
		if info.Cwd != spec.Cwd {
			t.Errorf("Cwd = %q, want %q", info.Cwd, spec.Cwd)
		}
		if !strings.HasPrefix(info.Title, "echo ") {
			t.Errorf("Title = %q, want the first prompt", info.Title)
		}
		if info.UpdatedAt.IsZero() {
			t.Errorf("session %q has no UpdatedAt", info.AgentSessionID)
		}
	}

	// A different working directory has no sessions of its own.
	elsewhere := spec
	elsewhere.Cwd = t.TempDir()
	if infos, err := d.List(t.Context(), elsewhere); err != nil {
		t.Fatalf("List of another directory failed: %v", err)
	} else if len(infos) != 0 {
		t.Errorf("List of another directory returned %d sessions, want 0", len(infos))
	}
}

func TestInitializeTimeout(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to stand in for an agent that never answers")
	}

	// An agent that never answers initialize has to be killed, not merely
	// abandoned. The stand-in touches a marker a second in: if the driver left
	// it running, the marker appears.
	marker := filepath.Join(t.TempDir(), "still-alive")
	spec := ports.SessionSpec{
		Agent:   "mute",
		Command: []string{sh, "-c", "sleep 1; : > " + marker + "; sleep 30"},
		Cwd:     t.TempDir(),
		Env:     []string{"PATH=" + os.Getenv("PATH")},
	}

	d := testDriver(t)
	d.startTimeout = 150 * time.Millisecond
	d.closeGrace = 50 * time.Millisecond

	started := time.Now()
	if _, err := d.Start(t.Context(), spec, nil); err == nil {
		t.Fatal("Start succeeded against an agent that never answered")
	} else if !strings.Contains(err.Error(), "initialize") {
		t.Errorf("Start error = %v, want it to name initialize", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Start took %v, want it to give up after the start timeout", elapsed)
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the agent was still running after the timeout, want it terminated")
	}
}

func TestPlanAndToolEvents(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "plan"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	if len(events) != 2 || events[0].Kind != fsp.EvPlan {
		t.Fatalf("events = %+v, want a plan and a turn end", events)
	}
	plan := events[0].Plan
	if len(plan) != 3 {
		t.Fatalf("the plan has %d steps, want 3", len(plan))
	}
	if plan[0].Title == "" || plan[0].Status != "completed" || plan[2].Status != "pending" {
		t.Errorf("plan = %+v, want each step's title and status carried over", plan)
	}

	if err := s.Prompt(t.Context(), "tool"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events = untilTurnEnd(t, s.Events())

	var tools []*fsp.ToolInfo
	for _, e := range events {
		if e.Kind == fsp.EvTool {
			tools = append(tools, e.Tool)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tool events, want the call and its update", len(tools))
	}
	if tools[0].Title != "run tests" || tools[0].Kind != "execute" || tools[0].Status != "pending" {
		t.Errorf("first tool event = %+v, want the pending execute call", tools[0])
	}
	if tools[1].CallID != tools[0].CallID || tools[1].Status != "completed" {
		t.Errorf("second tool event = %+v, want the same call completed", tools[1])
	}
}

func TestPromptErrorEndsTurn(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "fail"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())

	end := events[len(events)-1]
	if end.StopReason != "error" {
		t.Errorf("StopReason = %q, want error", end.StopReason)
	}
	if end.Err == "" {
		t.Error("the turn end says nothing about what went wrong")
	}
	// The session is usable again: a failed turn is not a dead session.
	if err := s.Prompt(t.Context(), "echo after"); err != nil {
		t.Errorf("Prompt after a failed turn returned %v, want it accepted", err)
	}
	untilTurnEnd(t, s.Events())
}

func TestSetConfigUnsupported(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), agentSpec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	// The fake agent offers neither modes nor config options.
	if s.Caps().Config {
		t.Error("Caps.Config is set even though the agent offered no options")
	}
	if err := s.SetConfig(t.Context(), "mode", "plan"); !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("SetConfig(mode) = %v, want fsp.ErrUnsupported", err)
	}
	if err := s.SetConfig(t.Context(), "thinking", "true"); !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("SetConfig(thinking) = %v, want fsp.ErrUnsupported", err)
	}
}

// acpExeSuffix is what Windows needs before a built binary can be executed.
var acpExeSuffix = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()
