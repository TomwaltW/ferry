package v2

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
// test drives that scripted agent in its v2 mode; no real agent is ever started.
var fakeAgentBin string

// eventWait is how long a test waits for an event before giving up. It is far
// longer than anything the fake agent takes, so a timeout means a real hang.
const eventWait = 15 * time.Second

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))

	dir, err := os.MkdirTemp("", "ferry-acp2-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp/v2 test: create a temporary directory:", err)
		os.Exit(1)
	}

	fakeAgentBin = filepath.Join(dir, "ferry-fakeagent"+exeSuffix())
	build := exec.Command("go", "build", "-o", fakeAgentBin, "github.com/TomwaltW/ferry/cmd/ferry-fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "acp/v2 test: build ferry-fakeagent: %v\n%s", err, out)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// exeSuffix is what Windows needs before a built binary can be executed.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
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

// v2Spec points at the fake agent in v2 mode, persisting its sessions under home
// so a later resume or list can find them. args are extra flags for the agent.
func v2Spec(t *testing.T, home string, args ...string) ports.SessionSpec {
	t.Helper()
	return ports.SessionSpec{
		Agent:   "fake",
		Command: append([]string{fakeAgentBin, "--acp-version", "2"}, args...),
		Cwd:     t.TempDir(),
		Env:     []string{"FAKEAGENT_HOME=" + home, "PATH=" + os.Getenv("PATH")},
	}
}

// v1Spec points at the same binary in its default v1 mode, which is what an
// agent that does not speak v2 looks like from here.
func v1Spec(t *testing.T, home string) ports.SessionSpec {
	t.Helper()
	return ports.SessionSpec{
		Agent:   "fake",
		Command: []string{fakeAgentBin},
		Cwd:     t.TempDir(),
		Env:     []string{"FAKEAGENT_HOME=" + home, "PATH=" + os.Getenv("PATH")},
	}
}

// approver is a ports.Approver that hands each request to the test and records
// what it was asked.
type approver struct {
	ask  func(context.Context, fsp.ApprovalRequest) (fsp.Verdict, error)
	seen chan fsp.ApprovalRequest
}

func newApprover(ask func(context.Context, fsp.ApprovalRequest) (fsp.Verdict, error)) *approver {
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

func TestV2Negotiation(t *testing.T) {
	d := testDriver(t)

	// An agent that speaks v2 initializes, reports the session capability as a
	// whole and opens a session.
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start against a v2 agent failed: %v", err)
	}
	defer s.Close()

	if s.AgentSessionID() == "" {
		t.Error("AgentSessionID is empty")
	}
	caps := s.Caps()
	if !caps.Resume || !caps.List || !caps.Cancel {
		t.Errorf("Caps = %+v, want resume, list and cancel from a v2 agent", caps)
	}
	if !caps.Config {
		t.Error("Caps.Config is false, but session/new offered a config option")
	}

	// An agent that answers with version 1 is not a v2 agent, and the driver
	// says exactly that rather than trying to carry on.
	_, err = d.Start(t.Context(), v1Spec(t, t.TempDir()), nil)
	if !errors.Is(err, ErrNegotiatedV1) {
		t.Fatalf("Start against a v1 agent returned %v, want ErrNegotiatedV1", err)
	}
}

func TestV2PromptAcceptedThenIdle(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	// The agent answers session/prompt as soon as it has taken the input, so
	// Prompt returns while the turn is still running — which is why a second
	// prompt is refused as busy and the turn only ends on the idle update.
	if err := s.Prompt(t.Context(), "slow 300"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	if err := s.Prompt(t.Context(), "echo nope"); !errors.Is(err, fsp.ErrBusy) {
		t.Fatalf("the second Prompt returned %v, want fsp.ErrBusy", err)
	}

	events := untilTurnEnd(t, s.Events())
	if len(chunksOf(events)) == 0 {
		t.Error("the slow turn streamed nothing")
	}
	end := events[len(events)-1]
	if end.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn from the idle state_update", end.StopReason)
	}
	if end.At.IsZero() {
		t.Error("the turn end has no timestamp")
	}
	if end.Session != "" {
		t.Errorf("Session = %q, want it left for the core to fill in", end.Session)
	}

	// The session is free again once the agent reported itself idle.
	if err := s.Prompt(t.Context(), "echo hello"); err != nil {
		t.Fatalf("Prompt after the turn ended failed: %v", err)
	}
	events = untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "hello" {
		t.Errorf("the agent said %q, want %q", got, "hello")
	}
}

func TestV2Cancel(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "slow 60000"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	// Wait for the turn to actually be under way before cancelling it.
	if e := nextEvent(t, s.Events()); e.Kind != fsp.EvChunk {
		t.Fatalf("first event is %s, want a chunk from the slow turn", e.Kind)
	}
	if err := s.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	events := untilTurnEnd(t, s.Events())
	end := events[len(events)-1]
	if end.StopReason != "cancelled" {
		t.Errorf("StopReason = %q, want cancelled", end.StopReason)
	}
}

func TestV2ListResumeClose(t *testing.T) {
	d := testDriver(t)
	home := t.TempDir()
	spec := v2Spec(t, home)

	s, err := d.Start(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	id := s.AgentSessionID()

	if err := s.Prompt(t.Context(), "echo remembered"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	untilTurnEnd(t, s.Events())

	// Close sends session/close and then stops the process; the event channel
	// closes with it.
	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	select {
	case _, ok := <-s.Events():
		if ok {
			t.Error("an event arrived after Close")
		}
	case <-time.After(eventWait):
		t.Error("the event channel stayed open after Close")
	}

	// session/list finds the session that was just closed.
	listed, err := d.List(t.Context(), spec)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	found := false
	for _, info := range listed {
		if info.AgentSessionID == id {
			found = true
			if info.Title == "" {
				t.Error("the listed session has no title")
			}
			if info.UpdatedAt.IsZero() {
				t.Error("the listed session has no timestamp")
			}
		}
	}
	if !found {
		t.Fatalf("List returned %+v, want the session %s among them", listed, id)
	}

	// session/resume reattaches to it, and the resumed session takes prompts.
	back, err := d.Resume(t.Context(), spec, id, nil)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	defer back.Close()

	if back.AgentSessionID() != id {
		t.Errorf("the resumed session is %s, want %s", back.AgentSessionID(), id)
	}
	if err := back.Prompt(t.Context(), "echo again"); err != nil {
		t.Fatalf("Prompt after Resume failed: %v", err)
	}
	events := untilTurnEnd(t, back.Events())
	if got := strings.Join(chunksOf(events), ""); got != "again" {
		t.Errorf("the resumed session said %q, want %q", got, "again")
	}
}

func TestV2LoginRequired(t *testing.T) {
	d := testDriver(t)

	// A login only a person can complete is not Ferry's to do: startup fails and
	// says so, so that the core can tell the user to log in at a terminal.
	_, err := d.Start(t.Context(), v2Spec(t, t.TempDir(), "--auth-method", "terminal"), nil)
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Start against an interactive login returned %v, want ErrLoginRequired", err)
	}

	// A login the agent can do over the protocol is done without bothering
	// anybody, and the session opens as usual.
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir(), "--auth-method", "agent"), nil)
	if err != nil {
		t.Fatalf("Start against an agent login failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "echo signed in"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	if got := strings.Join(chunksOf(events), ""); got != "signed in" {
		t.Errorf("the agent said %q, want %q", got, "signed in")
	}
}

// TestV2PermissionRoundTrip covers the one request an agent sends a client. In
// v2 it only names the tool call, so what the user is shown has to come from the
// announcement that went before it.
func TestV2PermissionRoundTrip(t *testing.T) {
	ap := newApprover(func(_ context.Context, _ fsp.ApprovalRequest) (fsp.Verdict, error) {
		return fsp.Verdict{Decision: fsp.DecisionAllowOnce, OptionID: "allow-once", By: "tester"}, nil
	})

	d := testDriver(t)
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), ap)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "ask execute /tmp/run.sh"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())

	select {
	case req := <-ap.seen:
		if req.ToolName != "ask /tmp/run.sh" {
			t.Errorf("ToolName = %q, want the title of the tool call", req.ToolName)
		}
		if req.ToolKind != "execute" {
			t.Errorf("ToolKind = %q, want execute, remembered from the announcement", req.ToolKind)
		}
		if len(req.Paths) != 1 || req.Paths[0] != "/tmp/run.sh" {
			t.Errorf("Paths = %v, want the path of the announced call", req.Paths)
		}
		if !strings.Contains(req.InputPreview, "/tmp/run.sh") {
			t.Errorf("InputPreview = %q, want the raw input of the announced call", req.InputPreview)
		}
		if len(req.Options) != 3 {
			t.Errorf("got %d options, want the three the agent offered", len(req.Options))
		}
	default:
		t.Fatal("the approver was never asked")
	}

	if got := strings.Join(chunksOf(events), ""); got != "allowed" {
		t.Errorf("the agent said %q, want allowed", got)
	}
}

// TestV2PlanAndToolEvents pins the two update variants v2 reshaped: plans grew an
// id, and a tool call is created by the first update that names it.
func TestV2PlanAndToolEvents(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Prompt(t.Context(), "plan"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events := untilTurnEnd(t, s.Events())
	var plan []fsp.PlanStep
	for _, e := range events {
		if e.Kind == fsp.EvPlan {
			plan = e.Plan
		}
	}
	if len(plan) != 3 {
		t.Fatalf("got %d plan steps, want 3: %+v", len(plan), plan)
	}
	if plan[0].Title != "read the request" || plan[0].Status != "completed" {
		t.Errorf("the first plan step is %+v, want the completed first entry", plan[0])
	}

	if err := s.Prompt(t.Context(), "tool"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	events = untilTurnEnd(t, s.Events())
	var statuses []string
	for _, e := range events {
		if e.Kind == fsp.EvTool {
			if e.Tool.CallID != "call_tool" {
				t.Errorf("CallID = %q, want call_tool", e.Tool.CallID)
			}
			statuses = append(statuses, e.Tool.Status)
		}
	}
	if len(statuses) != 2 || statuses[0] != "pending" || statuses[1] != "completed" {
		t.Errorf("tool statuses = %v, want pending then completed", statuses)
	}
}

// TestV2SetConfigMode checks the option path that replaced v1's session/set_mode.
func TestV2SetConfigMode(t *testing.T) {
	d := testDriver(t)
	s, err := d.Start(t.Context(), v2Spec(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.SetConfig(t.Context(), "mode", "code"); err != nil {
		t.Errorf("SetConfig(mode) failed: %v", err)
	}
	err = s.SetConfig(t.Context(), "nonesuch", "x")
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("SetConfig for an unknown option returned %v, want fsp.ErrUnsupported", err)
	}
}
