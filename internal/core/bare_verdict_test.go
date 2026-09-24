package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// waiting is one approval of the foreground session whose question has been
// on screen long enough for a bare decision word to answer it.
func (h *harness) waiting(id string) ports.PendingApproval {
	shown := h.clk.Now().Add(-bareShown)
	return ports.PendingApproval{ID: id, Session: h.session().id(), AskedAt: shown, ShownAt: shown}
}

var voiceNote = fsp.Attachment{Kind: "voice", Name: "note.silk", Path: "/tmp/note.silk"}

func TestBareVerdictAnswersTheOnlyApproval(t *testing.T) {
	cases := []struct {
		word string
		want fsp.Decision
	}{
		{"同意", fsp.DecisionAllowOnce},
		{"y", fsp.DecisionAllowOnce},
		{"拒绝", fsp.DecisionReject},
		{"n", fsp.DecisionReject},
	}
	for _, tc := range cases {
		t.Run(tc.word, func(t *testing.T) {
			h := newHarness(t)
			agent := h.newSession()
			h.approvals.setOpen(h.waiting("abcde"))
			// A lapse long enough ago is no reason to refuse.
			h.approvals.setLapsed(h.clk.Now().Add(-2 * bareAfterLapse))

			h.owner(tc.word)
			h.waitFor("the verdict", func() bool { return len(h.approvals.verdicts()) == 1 })
			if v := h.approvals.verdicts()[0]; v.id != "abcde" || v.decision != tc.want || v.by != ownerID {
				t.Errorf("verdict = %+v, want %s on abcde by the owner", v, tc.want)
			}
			h.owner("/help")
			h.waitText("help.text")
			if n := len(agent.prompted()); n != 0 {
				t.Errorf("the agent was prompted %d times, want the word kept from it", n)
			}
			if n := h.countText("approval.need_id"); n != 0 {
				t.Errorf("%d hints sent, want none", n)
			}
		})
	}
}

func TestBareVerdictAsksForTheID(t *testing.T) {
	cases := []struct {
		name  string
		word  string
		setup func(h *harness)
		n     int
	}{
		{"two approvals waiting", "同意", func(h *harness) {
			h.approvals.setOpen(h.waiting("abcde"), h.waiting("fghij"))
		}, 2},
		{"shown a moment ago", "同意", func(h *harness) {
			p := h.waiting("abcde")
			p.ShownAt = h.clk.Now().Add(-time.Second)
			h.approvals.setOpen(p)
		}, 1},
		{"question not delivered", "同意", func(h *harness) {
			p := h.waiting("abcde")
			p.ShownAt = time.Time{}
			h.approvals.setOpen(p)
		}, 1},
		{"another approval just lapsed", "同意", func(h *harness) {
			h.approvals.setOpen(h.waiting("abcde"))
			h.approvals.setLapsed(h.clk.Now().Add(-30 * time.Second))
		}, 1},
		{"another session's approval", "同意", func(h *harness) {
			p := h.waiting("abcde")
			p.Session = "sess-elsewhere"
			h.approvals.setOpen(p)
		}, 1},
		{"always allow needs the ID", "总是", func(h *harness) {
			h.approvals.setOpen(h.waiting("abcde"))
		}, 1},
		{"a refusal is held to the same rules", "拒绝", func(h *harness) {
			h.approvals.setOpen(h.waiting("abcde"), h.waiting("fghij"))
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			agent := h.newSession()
			tc.setup(h)

			h.owner(tc.word)
			hint := h.waitText("approval.need_id|")
			if !strings.Contains(hint.Text, fmt.Sprintf("N=%d", tc.n)) || !strings.Contains(hint.Text, "approval.need_id_item") {
				t.Errorf("hint = %q, want %d approvals listed", hint.Text, tc.n)
			}
			if !hint.Urgent {
				t.Error("the hint answers the approver's own message and must be urgent")
			}
			if v := h.approvals.verdicts(); len(v) != 0 {
				t.Errorf("verdicts = %+v, want none", v)
			}
			if n := len(agent.prompted()); n != 0 {
				t.Errorf("the agent was prompted %d times, want the word kept from it", n)
			}
		})
	}
}

func TestBareVerdictIsTextWithNothingWaiting(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()

	h.owner("y")
	h.waitText("turn.accepted|")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "y" {
		t.Errorf("prompt = %q, want the word as ordinary text", got)
	}
	if v := h.approvals.verdicts(); len(v) != 0 {
		t.Errorf("verdicts = %+v, want none", v)
	}
}

func TestBareVerdictFromNonApproverIsText(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	h.approvals.setOpen(h.waiting("abcde"))

	h.send(memberID, "同意")
	h.waitText("turn.accepted|")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "[from "+memberID+"] 同意" {
		t.Errorf("prompt = %q, want the member's word as ordinary text", got)
	}
	if v := h.approvals.verdicts(); len(v) != 0 {
		t.Errorf("verdicts = %+v, want none", v)
	}
	if n := h.countText("approval.need_id"); n != 0 {
		t.Errorf("%d hints sent to a member, want none", n)
	}
}

// TestSpokenVerdictOnlyRefuses: WeChat's transcription can drop the 不 of
// 不同意, so a voice message may refuse an approval but never allow one.
func TestSpokenVerdictOnlyRefuses(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		for _, text := range []string{"同意", "y abcde"} {
			h := newHarness(t)
			agent := h.newSession()
			h.approvals.setOpen(h.waiting("abcde"))

			h.send(ownerID, text, voiceNote)
			h.waitText("approval.voice_allow|")
			if v := h.approvals.verdicts(); len(v) != 0 {
				t.Errorf("%q: verdicts = %+v, want none", text, v)
			}
			if n := len(agent.prompted()); n != 0 {
				t.Errorf("%q: the agent was prompted %d times, want none", text, n)
			}
		}
	})
	t.Run("reject", func(t *testing.T) {
		h := newHarness(t)
		h.newSession()
		h.approvals.setOpen(h.waiting("abcde"))

		h.send(ownerID, "拒绝", voiceNote)
		h.waitFor("the verdict", func() bool { return len(h.approvals.verdicts()) == 1 })
		if v := h.approvals.verdicts()[0]; v.id != "abcde" || v.decision != fsp.DecisionReject {
			t.Errorf("verdict = %+v, want reject on abcde", v)
		}
	})
	t.Run("nothing waiting", func(t *testing.T) {
		h := newHarness(t)
		agent := h.newSession()

		h.send(ownerID, "同意", voiceNote)
		h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
		if n := h.countText("approval.voice_allow"); n != 0 {
			t.Errorf("%d voice notices with nothing waiting, want none", n)
		}
	})
}
