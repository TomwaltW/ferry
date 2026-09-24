package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
)

// longApprovals gives an approval time to be answered by a bare word: the
// rig's two-second timeout is shorter than the wait a bare word needs.
func longApprovals(r *rig) {
	r.cfg.Policy.ApprovalTimeout = config.Duration{Duration: time.Minute}
}

// A bare 同意 answers the only approval once its question has been on screen
// for a moment; sent sooner, it is answered with a request for the code, and
// the approval stays open.
func TestBareVerdict(t *testing.T) {
	r := newRig(t, longApprovals)
	r.newSession()

	mark := r.markOwner()
	r.send("ask execute " + filepath.Join(r.ws, "demo", "run.sh"))
	_, id := r.waitApproval(mark)

	early := r.markOwner()
	r.send("同意")
	r.waitOwnerTextFrom(early, r.tr("approval.need_id_item", map[string]any{"ID": id, "Slot": 1}))
	r.refuteFrom(ownerID, early, "allowed", "a bare word right after the question must not decide it")

	// The core wants the question delivered three seconds before the word.
	time.Sleep(3 * time.Second)
	later := r.markOwner()
	r.send("同意。")
	r.waitOwnerTextFrom(later, "allowed")
	r.waitOwnerTextFrom(later, r.tr("approval.decided", map[string]any{
		"ID": id, "Decision": r.tr("approval.decision.allow_once", nil),
	}))
}

// With no approval waiting, a decision word is ordinary text for the agent.
func TestBareWordIsTextWithNothingWaiting(t *testing.T) {
	r := newRig(t)
	r.newSession()

	mark := r.markOwner()
	r.send("同意")
	r.waitOwnerTextFrom(mark, "unknown: 同意")
}

// Pieces of one message sent in quick succession reach the agent as one prompt
// with a single receipt, through the real daemon, outbox and gateway.
func TestGatheredPieces(t *testing.T) {
	r := newRig(t, func(r *rig) { r.cfg.Input.Gather = config.Duration{Duration: 400 * time.Millisecond} })
	r.newSession()

	mark := r.markOwner()
	r.send("echo 帮我改一下")
	r.send("outbox 的重试")
	r.waitOwnerTextFrom(mark, "帮我改一下\noutbox 的重试")
	receipt := r.tr("turn.accepted", map[string]any{"Agent": agentName, "Slot": 1})
	n := 0
	for _, msg := range r.sentTo(ownerID)[mark:] {
		n += strings.Count(msg, receipt)
	}
	if n != 1 {
		t.Errorf("%d receipts for one message sent in two pieces, want 1", n)
	}
}
