package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

const testGather = 2500 * time.Millisecond

// newGatherHarness starts a daemon whose idle sessions wait testGather for the
// rest of a message sent in pieces.
func newGatherHarness(t *testing.T) *harness {
	t.Helper()
	h := newTestbed(t)
	h.deps.Config.Input.Gather = config.Duration{Duration: testGather}
	return h.start()
}

// inbound counts the messages the daemon has finished handling: the last step
// of each is its audit entry, written after any gather timer is armed.
func (h *harness) inbound() int {
	n := 0
	for _, k := range h.store.auditKinds() {
		if k == "inbound" {
			n++
		}
	}
	return n
}

// piece sends one piece of a message and waits until it has been handled, so
// the clock can be moved on right after.
func (h *harness) piece(text string, atts ...fsp.Attachment) {
	h.t.Helper()
	before := h.inbound()
	h.send(ownerID, text, atts...)
	h.waitFor("the piece "+text+" to be handled", func() bool { return h.inbound() == before+1 })
}

// settle gives the daemon loop a round trip, so a timer that fired has been
// looked at before a test checks that nothing happened.
func (h *harness) settle() {
	h.t.Helper()
	before := h.countText("help.text")
	h.owner("/help")
	h.waitFor("the help text", func() bool { return h.countText("help.text") == before+1 })
}

func TestGatherJoinsPieces(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("帮我改一下")
	h.piece("outbox 的重试")
	h.piece("改成指数退避")
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Fatalf("the agent was prompted %d times before the wait ran out, want 0", n)
	}
	if n := h.countText("turn.accepted"); n != 0 {
		t.Errorf("%d receipts before the turn started, want none", n)
	}

	h.clk.Advance(testGather)
	h.waitFor("the gathered prompt", func() bool { return len(agent.prompted()) == 1 })
	if got, want := agent.prompted()[0], "帮我改一下\noutbox 的重试\n改成指数退避"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	h.waitText("turn.accepted|")
	h.settle()
	if n := h.countText("turn.accepted|"); n != 1 {
		t.Errorf("%d receipts, want exactly one for the whole message", n)
	}
	if n := len(agent.prompted()); n != 1 {
		t.Errorf("the agent was prompted %d times, want once", n)
	}
}

func TestGatherRestartsTheWait(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("one")
	h.clk.Advance(2 * time.Second)
	h.piece("two")
	h.clk.Advance(2 * time.Second) // the first piece's wait is over, but "two" restarted it
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Fatalf("the agent was prompted %d times while pieces were still coming, want 0", n)
	}
	h.clk.Advance(testGather - 2*time.Second)
	h.waitFor("the gathered prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "one\ntwo" {
		t.Errorf("prompt = %q, want both pieces", got)
	}
}

func TestGatherIsCapped(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	// A piece every two seconds would restart the wait forever; the turn
	// starts gatherSpan waits after the first piece all the same.
	var sent []string
	for i := range 5 {
		text := strings.Repeat("x", i+1)
		sent = append(sent, text)
		h.piece(text)
		h.clk.Advance(2 * time.Second)
	}
	h.waitFor("the capped prompt", func() bool { return len(agent.prompted()) == 1 })
	if got, want := agent.prompted()[0], strings.Join(sent, "\n"); got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	if got, want := h.clk.Now().Sub(epoch), gatherSpan*testGather; got != want {
		t.Errorf("the test moved the clock %s, want %s: rework the loop", got, want)
	}
}

func TestGatherLeavesBusySessionsAlone(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("first")
	h.clk.Advance(testGather)
	h.waitFor("the first turn", func() bool { return len(agent.prompted()) == 1 })

	// While the turn runs, text queues as before instead of gathering.
	h.piece("second")
	if n := pendingLen(h.session()); n != 1 {
		t.Errorf("%d texts queued behind the running turn, want 1", n)
	}
	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})
	h.waitFor("the queued text", func() bool { return len(agent.prompted()) == 2 })
	if got := agent.prompted()[1]; got != "second" {
		t.Errorf("queued prompt = %q, want %q", got, "second")
	}
}

func TestStopDropsGatheredPieces(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("rm -rf 那个目录")
	h.owner("停")
	h.waitText("turn.stopped|")
	h.clk.Advance(testGather)
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Errorf("the agent was prompted %d times after 停, want 0", n)
	}
	if n := agent.cancelCount(); n != 0 {
		t.Errorf("the agent was cancelled %d times, want 0: nothing had reached it", n)
	}
	if n := len(h.approvals.cancels()); n != 0 {
		t.Errorf("%d approval cancellations, want none", n)
	}

	// The session carries on as usual afterwards.
	h.piece("hello")
	h.clk.Advance(testGather)
	h.waitFor("the next prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "hello" {
		t.Errorf("prompt = %q, want only the piece after 停", got)
	}
}

func TestStopWithNothingGatheredCancels(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("run")
	h.clk.Advance(testGather)
	h.waitFor("the turn", func() bool { return len(agent.prompted()) == 1 })
	h.owner("停")
	h.waitFor("the cancel", func() bool { return agent.cancelCount() == 1 })
	if n := h.countText("turn.stopped"); n != 0 {
		t.Errorf("%d turn.stopped replies before the turn ended, want none: the turn end sends it", n)
	}
}

func TestCloseDropsGatheredPieces(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("half a thought")
	h.owner("/close")
	h.waitText("session.closed|")
	h.clk.Advance(testGather)
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Errorf("a closed session's agent was prompted %d times, want 0", n)
	}
	if n := h.countText("turn."); n != 0 {
		t.Errorf("%d turn messages after /close, want none: %q", n, h.outbox.texts())
	}
}

func TestParkDropsGatheredPieces(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("half a thought")
	agent.exit()
	h.waitText("session.parked|")
	h.clk.Advance(testGather)
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Errorf("a parked session's agent was prompted %d times, want 0", n)
	}
}

func TestGatherShowsTyping(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("one")
	h.waitFor("typing on", func() bool { return len(h.im.typingCalls()) == 1 })
	h.piece("two")
	if calls := h.im.typingCalls(); len(calls) != 1 || !calls[0] {
		t.Errorf("typing calls = %v, want one switch on for the whole gather", calls)
	}
	h.owner("停")
	h.waitText("turn.stopped|")
	if calls := h.im.typingCalls(); len(calls) != 2 || calls[1] {
		t.Errorf("typing calls = %v, want it switched off by 停", calls)
	}

	h.piece("three")
	h.clk.Advance(testGather)
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	h.waitText("turn.accepted|")
	if calls := h.im.typingCalls(); len(calls) != 3 || !calls[2] {
		t.Errorf("typing calls = %v, want one more switch on and none repeated at the turn start", calls)
	}
}

func TestGatherWaitsLongerAfterAPicture(t *testing.T) {
	h := newGatherHarness(t)
	agent := h.newSession()

	h.piece("", fsp.Attachment{Kind: "image", Name: "err.png", Path: "/tmp/err.png"})
	h.clk.Advance(testGather)
	h.settle()
	if n := len(agent.prompted()); n != 0 {
		t.Fatalf("the agent was prompted %d times with a bare picture, want it to wait for the question", n)
	}
	h.clk.Advance(3 * time.Second)
	h.piece("这个报错怎么办")
	h.clk.Advance(testGather)
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	if got, want := agent.prompted()[0], "prompt.attachment|zh-CN Path=/tmp/err.png\n这个报错怎么办"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// A session can turn busy while pieces wait, when an external editor or
// terminal raises an approval. The next piece then queues behind them.
func TestGatherKeepsOrderWhenTheSessionTurnsBusy(t *testing.T) {
	h := newTestbed(t)
	h.deps.Config.Input.Gather = config.Duration{Duration: testGather}
	release := make(chan struct{})
	h.approvals.release = release
	h.start()
	agent := h.newSession()

	h.piece("one")
	go agent.approver.Ask(context.Background(), fsp.ApprovalRequest{ID: "abcde", ToolName: "shell"})
	h.waitFor("the session to wait on the approval", func() bool { return h.sessionState() == fsp.StateAwaiting })
	h.piece("two")
	close(release)
	h.waitFor("the session to run again", func() bool { return h.sessionState() == fsp.StateRunning })
	h.clk.Advance(testGather)
	h.settle()

	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})
	h.waitFor("the queued text", func() bool { return len(agent.prompted()) == 1 })
	if got, want := agent.prompted()[0], "one\n\ntwo"; got != want {
		t.Errorf("prompt = %q, want %q: the gathered piece first", got, want)
	}
}
