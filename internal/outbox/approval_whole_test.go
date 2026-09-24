package outbox

import (
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// An approval question is never cut short to make room for the progress text
// that would ride in front of it: a bare "同意" trusts that a delivered
// question was seen whole.
func TestApprovalNeverTruncatedByRiders(t *testing.T) {
	h := newRunning(t, func(c *config.OutboxConfig) { c.ReplyBudget = 2 })
	h.open("ctx-1")
	h.enqueue(sessionA, "receipt", true)
	h.waitSent(1) // one reply left

	progress := strings.Repeat("进度", 800) // 1600 runes, more than one message
	h.enqueue(sessionA, progress, false)
	question := "审批 abcde · shell\nrm -rf build\n回复「同意」允许一次"
	h.enqueueKind(sessionA, question, fsp.OutApproval, true)

	h.waitSent(2)
	if got := h.im.messages()[1].Text; got != question {
		t.Errorf("sent %q, want the question alone and whole", got)
	}
	h.quiesce()
	if got := h.store.row(t, 3).State; got != fsp.DeliveryAccepted {
		t.Errorf("question row = %q, want accepted", got)
	}
	if got := h.store.row(t, 2).State; got != fsp.DeliveryQueued {
		t.Errorf("progress row = %q, want it still queued for later", got)
	}
}

// A question that does not fit the replies left even on its own waits for the
// next window instead of going out truncated.
func TestApprovalHeldWhenItCannotFitWhole(t *testing.T) {
	h := newRunning(t, func(c *config.OutboxConfig) {
		c.ReplyBudget = 2
		c.LongOutputThreshold = 20
	})
	h.open("ctx-1")
	h.enqueue(sessionA, "receipt", true)
	h.waitSent(1)

	h.enqueueKind(sessionA, strings.Repeat("长", 50), fsp.OutApproval, true)
	h.waitFor("the question to be held", func() bool { return h.store.row(t, 2).State == fsp.DeliveryHeld })
	h.quiesce()
	if n := h.im.count(); n != 1 {
		t.Errorf("sent %d, want only the receipt: %q", n, h.sentTexts())
	}
}

// With room enough, the progress text still rides in front of the question.
func TestApprovalStillCarriesRidersThatFit(t *testing.T) {
	h := newRunning(t, nil)
	h.open("ctx-1")
	h.enqueue(sessionA, "working on it", false)
	h.enqueueKind(sessionA, "审批 abcde · shell", fsp.OutApproval, true)
	h.waitSent(1)
	if got, want := h.im.messages()[0].Text, "working on it\n\n审批 abcde · shell"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
}
