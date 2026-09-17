package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// ---------- test doubles ----------

// fakeHandoff stands in for the handoff module: the terminal sessions it finds,
// whether they are busy and what resumes them all come from the test.
type fakeHandoff struct {
	mu          sync.Mutex
	sessions    []ports.TerminalSession
	discoverErr error
	busy        bool
	busyErr     error
	backCmd     []string
	backErr     error
	discovers   int
	busyChecks  int
}

func (f *fakeHandoff) Discover(ctx context.Context, ep fsp.EndpointID) ([]ports.TerminalSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discovers++
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	return append([]ports.TerminalSession(nil), f.sessions...), nil
}

func (f *fakeHandoff) Busy(ctx context.Context, ts ports.TerminalSession) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busyChecks++
	return f.busy, f.busyErr
}

func (f *fakeHandoff) BackCommand(agent, agentSessionID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.backErr != nil {
		return nil, f.backErr
	}
	out := make([]string, 0, len(f.backCmd))
	for _, arg := range f.backCmd {
		out = append(out, strings.ReplaceAll(arg, "{id}", agentSessionID))
	}
	return out, nil
}

func (f *fakeHandoff) busyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busyChecks
}

// wakeCall is one nudge sent outside chat.
type wakeCall struct {
	ep   fsp.EndpointID
	kind string
	args map[string]any
}

type fakeWaker struct {
	mu    sync.Mutex
	calls []wakeCall
}

func (f *fakeWaker) Wake(ctx context.Context, ep fsp.EndpointID, kind string, args map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wakeCall{ep: ep, kind: kind, args: args})
}

func (f *fakeWaker) wakes() []wakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wakeCall(nil), f.calls...)
}

var (
	_ ports.Handoff = (*fakeHandoff)(nil)
	_ ports.Waker   = (*fakeWaker)(nil)
)

// ---------- helpers ----------

// withHandoff puts a handoff module behind the daemon before it starts.
func (h *harness) withHandoff(sessions ...ports.TerminalSession) *fakeHandoff {
	ho := &fakeHandoff{sessions: sessions, backCmd: []string{"ferry-fakeagent", "--resume", "{id}"}}
	h.deps.Handoff = ho
	return ho
}

func (h *harness) withWaker() *fakeWaker {
	w := &fakeWaker{}
	h.deps.Waker = w
	return w
}

// terminalSession is the one an agent left behind in the workspace root.
func (h *harness) terminalSession() ports.TerminalSession {
	return ports.TerminalSession{
		Agent:          testAgent,
		Driver:         fsp.DriverACP,
		AgentSessionID: "term-1",
		Title:          "refactor the parser",
		Cwd:            h.root,
		UpdatedAt:      epoch.Add(-time.Hour),
	}
}

// ---------- /ls ----------

func TestLsShowsTerminalSessions(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff(tb.terminalSession())
	h := tb.start()
	h.newSession()

	h.owner("/ls")
	list := h.waitText("session.list_terminal_item|")

	// The managed session keeps slot 1, the terminal entry is numbered after it.
	wantManaged := fmt.Sprintf("session.list_item|zh-CN Agent=%s Cwd=%s Mark=▶ Slot=1 State=session.state.idle|zh-CN", testAgent, h.root)
	if !strings.Contains(list.Text, wantManaged) {
		t.Errorf("session list = %q, want a line %q", list.Text, wantManaged)
	}
	wantTerminal := fmt.Sprintf("session.list_terminal_item|zh-CN Agent=%s Slot=2 Title=refactor the parser When=", testAgent)
	if !strings.Contains(list.Text, wantTerminal) {
		t.Errorf("session list = %q, want a line starting %q", list.Text, wantTerminal)
	}
	if ho.discovers != 1 {
		t.Errorf("Discover was called %d times, want once per /ls", ho.discovers)
	}
}

func TestLsWithOnlyTerminalSessions(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff(tb.terminalSession())
	h := tb.start()

	h.owner("/ls")
	list := h.waitText("session.list_header|")
	if !strings.Contains(list.Text, "Slot=1") {
		t.Errorf("session list = %q, want the terminal entry numbered 1", list.Text)
	}
	if n := h.countText("hint.no_session|"); n != 0 {
		t.Errorf("the endpoint was told it has no sessions, but a terminal one was on offer")
	}
}

func TestLsSurvivesADiscoveryFailure(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff()
	ho.discoverErr = errors.New("the agent never answered")
	h := tb.start()
	h.newSession()

	h.owner("/ls")
	list := h.waitText("session.list_header|")
	if strings.Contains(list.Text, "session.list_terminal_item|") {
		t.Errorf("session list = %q, want no terminal entries", list.Text)
	}
}

// ---------- /use on a terminal entry ----------

// adoptable lists a terminal session and returns the number /ls gave it.
func adoptable(t *testing.T, h *harness) int {
	t.Helper()
	h.owner("/ls")
	h.waitText("session.list_terminal_item|")
	return 1
}

func TestUseAdoptsAndResumes(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff(tb.terminalSession())
	h := tb.start()

	slot := adoptable(t, h)
	h.owner(fmt.Sprintf("/use %d", slot))
	msg := h.waitText("handoff.adopted|")
	if !strings.Contains(msg.Text, "Title=refactor the parser") {
		t.Errorf("reply = %q, want the title of the adopted session", msg.Text)
	}

	h.waitFor("the driver to resume the session", func() bool { return h.driver.resumeCount() == 1 })
	agent := h.driver.agent(0)
	if agent.spec.Cwd != h.root {
		t.Errorf("the agent was resumed in %q, want %q", agent.spec.Cwd, h.root)
	}
	if agent.AgentSessionID() != "term-1" {
		t.Errorf("agent session = %q, want the terminal's term-1", agent.AgentSessionID())
	}

	// It is now an ordinary session of the endpoint: bound, in the foreground
	// and marked as having come from a terminal.
	s := h.session()
	if s == nil {
		t.Fatalf("the adopted session is not in the foreground")
	}
	rec := s.snapshot()
	if rec.Origin != fsp.OriginTerminal {
		t.Errorf("origin = %q, want %q", rec.Origin, fsp.OriginTerminal)
	}
	if rec.State != fsp.StateIdle {
		t.Errorf("state = %q, want idle", rec.State)
	}
	if s.slot != 1 {
		t.Errorf("slot = %d, want 1", s.slot)
	}

	// And it takes prompts.
	h.owner("continue")
	h.waitFor("the prompt to reach the agent", func() bool { return len(agent.prompted()) == 1 })
}

func TestUseBusyRefused(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff(tb.terminalSession())
	ho.busy = true
	h := tb.start()

	slot := adoptable(t, h)
	h.owner(fmt.Sprintf("/use %d", slot))
	msg := h.waitText("handoff.busy|")

	want := fmt.Sprintf("handoff.busy|zh-CN Agent=%s Cwd=%s Slot=%d", testAgent, h.root, slot)
	if msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if n := h.driver.resumeCount(); n != 0 {
		t.Errorf("the driver resumed %d sessions, want none while the terminal holds it", n)
	}
	if h.session() != nil {
		t.Errorf("a session was registered even though the takeover was refused")
	}
}

func TestUseBusyUnknown(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff(tb.terminalSession())
	ho.busyErr = fsp.ErrUnsupported
	h := tb.start()

	slot := adoptable(t, h)
	h.owner(fmt.Sprintf("/use %d", slot))
	msg := h.waitText("handoff.busy_unknown|")

	if want := fmt.Sprintf("handoff.busy_unknown|zh-CN Slot=%d", slot); msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if n := h.driver.resumeCount(); n != 0 {
		t.Errorf("the driver resumed %d sessions, want none while the terminal may hold it", n)
	}
}

func TestUseForce(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff(tb.terminalSession())
	ho.busy = true
	h := tb.start()

	slot := adoptable(t, h)
	h.owner(fmt.Sprintf("/use %d force", slot))
	h.waitText("handoff.adopted|")

	if n := ho.busyCount(); n != 0 {
		t.Errorf("the busy check ran %d times, want none with force", n)
	}
	h.waitFor("the driver to resume the session", func() bool { return h.driver.resumeCount() == 1 })
}

func TestSharedSkipsBusy(t *testing.T) {
	tb := newTestbed(t)
	ts := tb.terminalSession()
	ts.Shared = true
	ts.Driver = fsp.DriverServer
	ho := tb.withHandoff(ts)
	ho.busy = true // would refuse, if anyone asked
	h := tb.start()

	slot := adoptable(t, h)
	h.owner(fmt.Sprintf("/use %d", slot))
	h.waitText("handoff.adopted|")

	if n := ho.busyCount(); n != 0 {
		t.Errorf("the busy check ran %d times, want none for a shared session", n)
	}
}

func TestUseForgetsStaleTerminalNumbers(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff(tb.terminalSession())
	h := tb.start()

	slot := adoptable(t, h)
	// The numbers only stand for a minute.
	h.clk.Advance(terminalTTL + time.Second)
	h.owner(fmt.Sprintf("/use %d", slot))
	msg := h.waitText("session.not_found|")

	if want := fmt.Sprintf("session.not_found|zh-CN Slot=%d", slot); msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if n := h.driver.resumeCount(); n != 0 {
		t.Errorf("the driver resumed %d sessions, want none for a number that expired", n)
	}
}

// ---------- driver selection ----------

func TestServerDriverSelected(t *testing.T) {
	tb := newTestbed(t)
	server := &fakeDriver{kind: fsp.DriverServer}
	tb.deps.Drivers[fsp.DriverServer] = server
	tb.registry.specs["live"] = ports.SessionSpec{
		Agent:  "live",
		Server: &ports.ServerSpec{Kind: "opencode", URL: "http://127.0.0.1:4096"},
	}
	h := tb.start()

	h.owner("/new live " + h.root)
	h.waitText("session.created|")

	h.waitFor("the server driver to start the session", func() bool { return server.startCount() == 1 })
	if n := h.driver.startCount(); n != 0 {
		t.Errorf("the ACP driver started %d sessions, want none for a service-backed agent", n)
	}
	if rec := h.session().snapshot(); rec.Driver != fsp.DriverServer {
		t.Errorf("session driver = %q, want %q", rec.Driver, fsp.DriverServer)
	}
}

func TestMissingDriverIsReported(t *testing.T) {
	tb := newTestbed(t)
	tb.registry.specs["live"] = ports.SessionSpec{
		Agent:  "live",
		Server: &ports.ServerSpec{Kind: "qwen", URL: "http://127.0.0.1:4170"},
	}
	h := tb.start()

	h.owner("/new live " + h.root)
	msg := h.waitText("session.start_failed|")
	if !strings.Contains(msg.Text, "no server driver configured") {
		t.Errorf("reply = %q, want it to name the driver that is missing", msg.Text)
	}
}

// ---------- attachments ----------

func TestAttachmentsInPrompt(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.send(ownerID, "look at this",
		fsp.Attachment{Kind: "image", Name: "cat.png", Path: "/home/me/inbox/cat.png"},
		fsp.Attachment{Kind: "file", Name: "notes.md", Path: "/home/me/inbox/notes.md"},
	)
	h.waitFor("the prompt to reach the agent", func() bool { return len(agent.prompted()) == 1 })

	want := "look at this\n" +
		"prompt.attachment|zh-CN Path=/home/me/inbox/cat.png\n" +
		"prompt.attachment|zh-CN Path=/home/me/inbox/notes.md"
	if got := agent.prompted()[0]; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

func TestAttachmentWithoutTextStillReachesTheAgent(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.send(ownerID, "", fsp.Attachment{Kind: "image", Name: "cat.png", Path: "/home/me/inbox/cat.png"})
	h.waitFor("the prompt to reach the agent", func() bool { return len(agent.prompted()) == 1 })

	if want := "prompt.attachment|zh-CN Path=/home/me/inbox/cat.png"; agent.prompted()[0] != want {
		t.Errorf("prompt = %q, want %q", agent.prompted()[0], want)
	}
}

func TestMediaErrors(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	// One attachment was too big to fetch, one simply failed; the text still
	// goes through on its own.
	h.send(ownerID, "have a look",
		fsp.Attachment{Kind: "file", Name: "dump.bin", Size: 64 << 20},
		fsp.Attachment{Kind: "image", Name: "cat.png", Size: 1024},
	)

	tooLarge := h.waitText("media.too_large|")
	if want := "media.too_large|zh-CN Max=20"; tooLarge.Text != want {
		t.Errorf("reply = %q, want %q", tooLarge.Text, want)
	}
	failed := h.waitText("media.download_failed|")
	if want := "media.download_failed|zh-CN Name=cat.png"; failed.Text != want {
		t.Errorf("reply = %q, want %q", failed.Text, want)
	}

	h.waitFor("the text to reach the agent", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "have a look" {
		t.Errorf("prompt = %q, want the text without the attachments that failed", got)
	}
}

func TestAttachmentOnlyFailureNeverReachesTheAgent(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.send(ownerID, "", fsp.Attachment{Kind: "image", Name: "cat.png", Size: 1024})
	h.waitText("media.download_failed|")

	// Nothing else to say: an empty prompt is not worth a turn.
	h.owner("ping")
	h.waitFor("the next message to reach the agent", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "ping" {
		t.Errorf("prompt = %q, want only the later message", got)
	}
}

func TestMediaLimitFollowsTheConfiguration(t *testing.T) {
	tb := newTestbed(t)
	tb.deps.Config.WeChat.MaxMediaMB = 5
	h := tb.start()
	h.newSession()

	h.send(ownerID, "", fsp.Attachment{Kind: "file", Name: "dump.bin", Size: 6 << 20})
	msg := h.waitText("media.too_large|")
	if want := "media.too_large|zh-CN Max=5"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
}

// ---------- /pull and /file ----------

func TestPullAndFileCommands(t *testing.T) {
	h := newHarness(t)

	h.owner("/pull")
	h.waitFor("the outbox to be asked for the next batch", func() bool { return h.outbox.pullCount() == 1 })

	h.owner("/file 2")
	h.waitFor("the outbox to be asked for a file", func() bool { return len(h.outbox.fileCalls()) == 1 })
	if want := []int{2}; !reflect.DeepEqual(h.outbox.fileCalls(), want) {
		t.Errorf("File was called with %v, want %v", h.outbox.fileCalls(), want)
	}

	// Without a number it is the most recent file.
	h.owner("/file")
	h.waitFor("the outbox to be asked again", func() bool { return len(h.outbox.fileCalls()) == 2 })
	if want := []int{2, 1}; !reflect.DeepEqual(h.outbox.fileCalls(), want) {
		t.Errorf("File was called with %v, want %v", h.outbox.fileCalls(), want)
	}

	h.owner("/file zero")
	h.waitText("usage.use|")
	if n := len(h.outbox.fileCalls()); n != 2 {
		t.Errorf("File was called %d times, want the nonsense argument turned away", n)
	}
}

// ---------- waking the operator ----------

func TestIMOfflineWakes(t *testing.T) {
	tb := newTestbed(t)
	waker := tb.withWaker()
	tb.im.errs = []error{fsp.ErrSessionExpired}
	h := tb.start()

	h.waitFor("the wake channel to be told", func() bool { return len(waker.wakes()) == 1 })
	call := waker.wakes()[0]
	if call.kind != "im_offline" {
		t.Errorf("wake kind = %q, want im_offline", call.kind)
	}
	want := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: ownerID}
	if call.ep != want {
		t.Errorf("wake endpoint = %+v, want the owner's %+v", call.ep, want)
	}
	if len(call.args) != 0 {
		t.Errorf("wake args = %v, want nothing that could carry a message body", call.args)
	}
}

func TestOfflineWithoutAWakerIsQuiet(t *testing.T) {
	tb := newTestbed(t)
	tb.im.errs = []error{fsp.ErrSessionExpired}
	h := tb.start()

	// The daemon stays up and keeps retrying, waker or not.
	h.waitFor("the adapter to be retried", func() bool { return h.im.runCount() == 1 })
	if !hasKind(h.store.auditEntries(), "expired") {
		t.Errorf("audit kinds = %v, want an expired entry", h.store.auditKinds())
	}
}

// ---------- the control socket ----------

func TestParkViaControl(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff()
	h := tb.start()
	agent := h.newSession()

	got, err := h.d.ControlHandler().Park(context.Background(), control.ParkRequest{})
	if err != nil {
		t.Fatalf("Park failed: %v", err)
	}

	want := control.ParkResponse{
		Agent:          testAgent,
		AgentSessionID: "agent-1",
		Cwd:            h.root,
		BackCommand:    []string{"ferry-fakeagent", "--resume", "agent-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Park = %+v, want %+v", got, want)
	}

	// The agent is gone, the session is parked, and the owner is told where it
	// went rather than that it fell over.
	h.waitFor("the agent to be closed", agent.isClosed)
	h.waitFor("the session to be parked", func() bool { return h.sessionStateOf(0) == fsp.StateParked })
	msg := h.waitText("handoff.parked_by_back|")
	if want := "handoff.parked_by_back|zh-CN Slot=1"; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if n := h.countText("session.parked|"); n != 0 {
		t.Errorf("the owner was also told the session simply parked, which reads like a failure")
	}
	if ho.discovers != 0 {
		t.Errorf("Park went looking for terminal sessions, which it has no reason to do")
	}
}

func TestParkViaControlBySlot(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff()
	h := tb.start()
	h.newSession()

	if _, err := h.d.ControlHandler().Park(context.Background(), control.ParkRequest{Slot: 7}); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("Park of an unknown slot = %v, want fsp.ErrNotFound", err)
	}
	if _, err := h.d.ControlHandler().Park(context.Background(), control.ParkRequest{Slot: 1}); err != nil {
		t.Errorf("Park of slot 1 failed: %v", err)
	}
}

func TestParkViaControlUnsupportedAgent(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff()
	ho.backErr = fsp.ErrUnsupported
	h := tb.start()
	h.newSession()

	got, err := h.d.ControlHandler().Park(context.Background(), control.ParkRequest{})
	if err != nil {
		t.Fatalf("Park failed: %v", err)
	}
	if len(got.BackCommand) != 0 {
		t.Errorf("BackCommand = %v, want it empty for an agent that cannot be resumed", got.BackCommand)
	}
	// The session is still handed back: the operator just has to do the last
	// step themselves.
	h.waitFor("the session to be parked", func() bool { return h.sessionStateOf(0) == fsp.StateParked })
}

func TestParkViaControlWithoutASession(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff()
	h := tb.start()

	if _, err := h.d.ControlHandler().Park(context.Background(), control.ParkRequest{}); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("Park = %v, want fsp.ErrNotFound", err)
	}
}

func TestStatusViaControl(t *testing.T) {
	h := newHarness(t)
	h.newSession()

	got, err := h.d.ControlHandler().Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	want := control.StatusResponse{IMOnline: true, BotID: "bot", Sessions: 1, Running: 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

// sessionStateOf reports the state of the nth session the daemon knows, or the
// empty state when it has none. Parked sessions leave the foreground, so a test
// that watches one park cannot go through h.session().
func (h *harness) sessionStateOf(i int) fsp.SessionState {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	n := 0
	for _, s := range h.d.sessions {
		if n == i {
			return s.State()
		}
		n++
	}
	return ""
}
