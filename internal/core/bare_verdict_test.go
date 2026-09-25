package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
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
			if always := strings.Contains(hint.Text, "Why=approval.need_id_always"); always != (tc.word == "总是") {
				t.Errorf("hint = %q: the always-allow note is there = %v, want it only for 总是", hint.Text, always)
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

// A question the flush for this very message delivers was not on screen when
// the message was written, however long the flush took.
func TestBareVerdictTimesTheMessageBeforeTheFlush(t *testing.T) {
	h := newHarness(t)
	agent := h.newSession()
	p := h.waiting("abcde")
	p.ShownAt = h.clk.Now().Add(time.Second) // delivered by the flush below
	h.approvals.setOpen(p)
	h.outbox.mu.Lock()
	h.outbox.onFlush = func() { h.clk.Advance(5 * time.Second) }
	h.outbox.mu.Unlock()

	h.owner("同意")
	h.waitText("approval.need_id|")
	if v := h.approvals.verdicts(); len(v) != 0 {
		t.Errorf("verdicts = %+v, want none", v)
	}
	if n := len(agent.prompted()); n != 0 {
		t.Errorf("the agent was prompted %d times, want none", n)
	}
}

// Claude Code asks in its terminal too and never says when it was answered
// there, so a bare word next to its approvals stays text for the session.
func TestBareVerdictLeavesClaudeChannelApprovals(t *testing.T) {
	h := newExternalHarness(t)
	resp, stream := h.attach(control.AttachRequest{
		Kind: control.KindChannel, Agent: "claude", Cwd: h.root, AllowInject: true, RemoteApprove: true,
	})
	id := fsp.SessionID(resp.SessionID)
	if s := h.d.lookup(id); s == nil || s.snapshot().Driver != fsp.DriverClaudeChannel {
		t.Fatalf("the attached session is not a Claude Code channel session")
	}
	shown := h.clk.Now().Add(-bareShown)
	h.approvals.setOpen(ports.PendingApproval{ID: "abcde", Session: id, AskedAt: shown, ShownAt: shown})

	h.sendTo(h.externalEP(), h.im, ownerID, "y")
	m := stream.readOf(control.MsgPrompt)
	if m.Text != "y" {
		t.Errorf("prompt = %q, want the word as text", m.Text)
	}
	if v := h.approvals.verdicts(); len(v) != 0 {
		t.Errorf("verdicts = %+v, want none", v)
	}
	if n := h.countText("approval.need_id"); n != 0 {
		t.Errorf("%d hints, want none", n)
	}
}

// The platform's send time counts too: a word written before the question
// was delivered does not answer it, however late the daemon gets to it.
func TestBareVerdictTimesTheMessageFromItsSendTime(t *testing.T) {
	h := newHarness(t)
	h.newSession()
	p := h.waiting("abcde")
	h.approvals.setOpen(p)

	select {
	case h.im.channel() <- fsp.Inbound{
		Endpoint: h.ep, SenderID: ownerID, MessageID: "late", Text: "y",
		ReplyCtx: "ctx", ReceivedAt: p.ShownAt.Add(-time.Second),
	}:
	case <-time.After(3 * time.Second):
		t.Fatal("inbound channel blocked")
	}
	h.waitText("approval.need_id|")
	if v := h.approvals.verdicts(); len(v) != 0 {
		t.Errorf("verdicts = %+v, want none", v)
	}
}
