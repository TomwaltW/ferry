package outbox

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// hold puts one message into the backlog by enqueuing it while the window is
// shut, then draining the queue.
func (h *harness) hold(session fsp.SessionID, text string, urgent bool) {
	h.t.Helper()
	h.enqueue(session, text, urgent)
	h.box.pump(h.ctx)
}

// TestHeldWhenWindowExpired is the window half of spec-p2 3.6: once the window
// is older than window_ttl there is no window at all, so everything waits.
func TestHeldWhenWindowExpired(t *testing.T) {
	h := newBox(t, func(c *config.OutboxConfig) { c.WindowTTL = config.Duration{Duration: time.Second} })
	h.open("ctx-1")

	// Inside the window the message goes straight out.
	h.enqueue(sessionA, "in time", true)
	h.box.pump(h.ctx)
	if n := h.im.count(); n != 1 {
		t.Fatalf("sent %d inside the window, want 1: %q", n, h.sentTexts())
	}

	h.clk.Advance(time.Second)
	h.enqueue(sessionA, "too late", true)
	h.box.pump(h.ctx)

	if n := h.im.count(); n != 1 {
		t.Errorf("sent %d after the window expired, want no more than the first: %q", n, h.sentTexts())
	}
	if got := h.store.row(t, 2).State; got != fsp.DeliveryHeld {
		t.Errorf("row 2 state = %q, want held once window_ttl has passed", got)
	}
	if h.box.Held(testEndpoint) != 1 {
		t.Errorf("Held = %d, want 1", h.box.Held(testEndpoint))
	}
}

// TestWakeOnHeldUrgent: an urgent message nobody can see raises a reminder, and
// the reminder names the session and nothing else.
func TestWakeOnHeldUrgent(t *testing.T) {
	h := newBox(t, nil)
	h.store.bind(sessionA, 3)

	// No window at all: everything is held, urgent messages included.
	h.enqueue(sessionA, "chunk of output", false)
	h.box.pump(h.ctx)
	if got := len(h.waker.wakes()); got != 0 {
		t.Errorf("wake-ups after a non-urgent message = %d, want 0", got)
	}

	h.enqueueKind(sessionA, "please approve rm -rf /tmp/x", fsp.OutApproval, true)
	h.box.pump(h.ctx)
	wakes := h.waker.wakes()
	if len(wakes) != 1 {
		t.Fatalf("wake-ups = %d, want 1 for the held approval", len(wakes))
	}
	if wakes[0].Kind != wakeApproval {
		t.Errorf("kind = %q, want %q", wakes[0].Kind, wakeApproval)
	}
	if wakes[0].Endpoint != testEndpoint {
		t.Errorf("endpoint = %s, want %s", wakes[0].Endpoint, testEndpoint)
	}
	if got := wakes[0].Args["Slot"]; got != 3 {
		t.Errorf("Slot = %v, want the session's number 3", got)
	}
	// The reminder travels over a third-party service: no body, no command.
	for k, v := range wakes[0].Args {
		if k != "Slot" {
			t.Errorf("wake argument %q = %v, want only Slot", k, v)
		}
	}

	// The final result of a turn is a different reminder.
	h.enqueue(sessionA, "done", true)
	h.box.pump(h.ctx)
	wakes = h.waker.wakes()
	if len(wakes) != 2 {
		t.Fatalf("wake-ups = %d, want 2", len(wakes))
	}
	if wakes[1].Kind != wakeFinished {
		t.Errorf("kind = %q, want %q", wakes[1].Kind, wakeFinished)
	}
}

// TestDigestOnFlush is the B7 shape: the backlog is announced before it is
// redelivered.
func TestDigestOnFlush(t *testing.T) {
	h := newBox(t, nil)
	h.store.bind(sessionA, 1)
	h.hold(sessionA, "the result of the slow turn", true)

	h.open("ctx-1")
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}

	msgs := h.im.messages()
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want the digest and the held message: %q", len(msgs), h.sentTexts())
	}
	if !strings.HasPrefix(msgs[0].Text, "outbox.digest(") {
		t.Errorf("first message = %q, want the digest", msgs[0].Text)
	}
	if !strings.Contains(msgs[0].Text, "N=1") {
		t.Errorf("digest = %q, want it to count 1 held message", msgs[0].Text)
	}
	// One line per held message: session number, kind, and a preview.
	if !strings.Contains(msgs[0].Text, "1. text the result of the slow turn") {
		t.Errorf("digest = %q, want a line naming session 1 and the kind", msgs[0].Text)
	}
	if strings.Contains(msgs[0].Text, "digest_more") {
		t.Errorf("digest = %q, want no /pull hint when nothing is left over", msgs[0].Text)
	}
	if msgs[1].Text != "the result of the slow turn" {
		t.Errorf("second message = %q, want the held message itself", msgs[1].Text)
	}
	if got := h.store.row(t, 1).State; got != fsp.DeliveryAccepted {
		t.Errorf("row 1 state = %q, want accepted after redelivery", got)
	}
	if h.box.Held(testEndpoint) != 0 {
		t.Errorf("Held = %d, want 0 once the backlog is redelivered", h.box.Held(testEndpoint))
	}
	if got := h.store.window(t, testEndpoint).Used; got != 2 {
		t.Errorf("window used = %d, want 2 (digest plus one message)", got)
	}
}

// TestFlushRespectsBudget: redelivery stops at budget minus three, leaving a
// reply each for the digest, the receipt and the turn the operator just began.
func TestFlushRespectsBudget(t *testing.T) {
	h := newBox(t, func(c *config.OutboxConfig) { c.ReplyBudget = 8 })
	h.store.bind(sessionA, 1)
	for i := range 12 {
		h.hold(sessionA, fmt.Sprintf("held %d", i+1), false)
	}
	if h.box.Held(testEndpoint) != 12 {
		t.Fatalf("held = %d, want 12", h.box.Held(testEndpoint))
	}

	h.open("ctx-1")
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}

	// 8 replies: one for the digest and five redelivered, three kept back.
	msgs := h.im.messages()
	if len(msgs) != 6 {
		t.Fatalf("sent %d messages, want the digest plus 5: %q", len(msgs), h.sentTexts())
	}
	if got := h.store.window(t, testEndpoint).Used; got != 6 {
		t.Errorf("window used = %d, want 6, leaving 2 of the 8 replies", got)
	}
	for i, want := range []string{"held 1", "held 2", "held 3", "held 4", "held 5"} {
		if msgs[i+1].Text != want {
			t.Errorf("message %d = %q, want %q: the backlog goes out oldest first", i+1, msgs[i+1].Text, want)
		}
	}
	if h.box.Held(testEndpoint) != 7 {
		t.Errorf("Held = %d, want the remaining 7", h.box.Held(testEndpoint))
	}
}

// TestDigestMoreHint: whatever the flush could not carry is announced with the
// /pull hint, and the digest itself never runs past digest_max lines.
func TestDigestMoreHint(t *testing.T) {
	h := newBox(t, func(c *config.OutboxConfig) {
		c.ReplyBudget = 8
		c.DigestMax = 3
	})
	h.store.bind(sessionA, 1)
	for i := range 12 {
		h.hold(sessionA, fmt.Sprintf("held %d", i+1), false)
	}

	h.open("ctx-1")
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}

	digest := h.im.messages()[0].Text
	if !strings.Contains(digest, "N=12") {
		t.Errorf("digest = %q, want it to count all 12 held messages", digest)
	}
	if !strings.Contains(digest, "outbox.digest_more(N=7)") {
		t.Errorf("digest = %q, want the /pull hint for the remaining 7", digest)
	}
	// digest_max caps the listing: three lines, not twelve.
	if got := strings.Count(digest, "1. text held"); got != 3 {
		t.Errorf("digest lists %d messages, want digest_max = 3:\n%s", got, digest)
	}
}

func TestPullNextBatch(t *testing.T) {
	h := newBox(t, func(c *config.OutboxConfig) { c.ReplyBudget = 8 })
	h.store.bind(sessionA, 1)
	for i := range 12 {
		h.hold(sessionA, fmt.Sprintf("held %d", i+1), false)
	}

	h.open("ctx-1")
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}
	h.im.reset()

	// A new inbound message means a fresh window before /pull is handled.
	h.clk.Advance(time.Minute)
	h.open("ctx-2")
	if err := h.box.Pull(h.ctx, testEndpoint); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// Budget minus one: seven of the remaining messages.
	msgs := h.im.messages()
	if len(msgs) != 7 {
		t.Fatalf("Pull sent %d messages, want 7: %q", len(msgs), h.sentTexts())
	}
	if msgs[0].Text != "held 6" {
		t.Errorf("first pulled message = %q, want held 6", msgs[0].Text)
	}
	if h.box.Held(testEndpoint) != 0 {
		t.Errorf("Held = %d, want the backlog cleared", h.box.Held(testEndpoint))
	}

	// Once it is empty, /pull says so.
	h.im.reset()
	h.clk.Advance(time.Minute)
	h.open("ctx-3")
	if err := h.box.Pull(h.ctx, testEndpoint); err != nil {
		t.Fatalf("Pull on an empty backlog: %v", err)
	}
	if got := h.sentTexts(); len(got) != 1 || got[0] != "outbox.nothing_held" {
		t.Errorf("Pull sent %q, want outbox.nothing_held", got)
	}
}

func TestPullNothing(t *testing.T) {
	h := newBox(t, nil)
	h.open("ctx-1")
	if err := h.box.Pull(h.ctx, testEndpoint); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got := h.sentTexts(); len(got) != 1 || got[0] != "outbox.nothing_held" {
		t.Errorf("Pull sent %q, want a single outbox.nothing_held", got)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Errorf("window used = %d, want the notice to spend one reply", got)
	}
}

// TestFlushUrgentFirst: an approval that has been waiting jumps ahead of older
// chatter when the backlog is redelivered.
func TestFlushUrgentFirst(t *testing.T) {
	h := newBox(t, func(c *config.OutboxConfig) { c.ReplyBudget = 8 })
	h.store.bind(sessionA, 1)
	h.hold(sessionA, "old chatter", false)
	h.clk.Advance(time.Minute)
	h.hold(sessionA, "newer chatter", false)
	h.clk.Advance(time.Minute)
	h.enqueueKind(sessionA, "approve me", fsp.OutApproval, true)
	h.box.pump(h.ctx)

	h.open("ctx-1")
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}

	msgs := h.im.messages()
	if len(msgs) != 4 {
		t.Fatalf("sent %d, want the digest and three messages: %q", len(msgs), h.sentTexts())
	}
	if msgs[1].Text != "approve me" {
		t.Errorf("first redelivered = %q, want the urgent approval", msgs[1].Text)
	}
	if msgs[2].Text != "old chatter" || msgs[3].Text != "newer chatter" {
		t.Errorf("rest = %q / %q, want the others oldest first", msgs[2].Text, msgs[3].Text)
	}
}

// TestFlushWithoutWindowKeepsBacklog: FlushHeld called while the window is
// still shut changes nothing.
func TestFlushWithoutWindowKeepsBacklog(t *testing.T) {
	h := newBox(t, nil)
	h.hold(sessionA, "waiting", true)

	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Fatalf("FlushHeld: %v", err)
	}
	if n := h.im.count(); n != 0 {
		t.Errorf("sent %d messages without a window, want 0: %q", n, h.sentTexts())
	}
	if h.box.Held(testEndpoint) != 1 {
		t.Errorf("Held = %d, want the message still waiting", h.box.Held(testEndpoint))
	}
}
