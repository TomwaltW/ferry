package core

import (
	"context"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// awaitingHarness has the foreground session blocked on an approval whose
// question has been delivered.
func awaitingHarness(t *testing.T, shown bool) *harness {
	t.Helper()
	h := newTestbed(t)
	release := make(chan struct{})
	h.approvals.release = release
	h.start()
	t.Cleanup(func() { close(release) })

	agent := h.newSession()
	h.owner("run it")
	h.waitFor("the turn", func() bool { return len(agent.prompted()) == 1 })
	go agent.approver.Ask(context.Background(), fsp.ApprovalRequest{ID: "abcde", ToolName: "shell"})
	h.waitFor("the session to wait on the approval", func() bool { return h.sessionState() == fsp.StateAwaiting })

	p := ports.PendingApproval{ID: "abcde", Session: h.session().id(), AskedAt: h.clk.Now().Add(-time.Minute)}
	if shown {
		p.ShownAt = p.AskedAt
	}
	h.approvals.setOpen(p)
	return h
}

func TestStillWaitingNotice(t *testing.T) {
	h := awaitingHarness(t, true)

	h.owner("好的")
	notice := h.waitText("approval.still_waiting|")
	if want := "approval.still_waiting|zh-CN ID=abcde"; notice.Text != want {
		t.Errorf("notice = %q, want %q", notice.Text, want)
	}
	h.owner("好的，继续")
	h.settle()
	if n := h.countText("approval.still_waiting"); n != 1 {
		t.Errorf("%d notices for one approval, want 1", n)
	}
	if n := pendingLen(h.session()); n != 2 {
		t.Errorf("%d texts held back, want both", n)
	}
}

func TestStillWaitingNoticeNeedsAnApproverAndADeliveredQuestion(t *testing.T) {
	t.Run("member", func(t *testing.T) {
		h := awaitingHarness(t, true)
		h.send(memberID, "好的")
		h.settle()
		if n := h.countText("approval.still_waiting"); n != 0 {
			t.Errorf("%d notices to a member, want none", n)
		}
	})
	t.Run("question not delivered", func(t *testing.T) {
		h := awaitingHarness(t, false)
		h.owner("好的")
		h.settle()
		if n := h.countText("approval.still_waiting"); n != 0 {
			t.Errorf("%d notices about an undelivered question, want none", n)
		}
	})
}
