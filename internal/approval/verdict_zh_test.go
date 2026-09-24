package approval

import (
	"context"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// TestParseVerdictChinese covers what a phone with a Chinese keyboard or voice
// input sends: Chinese decision words, an ID glued to them, full-width spaces,
// the full stop voice input appends, and a decision word with no ID at all.
func TestParseVerdictChinese(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	tests := []struct {
		in     string
		wantID string
		want   fsp.Decision
	}{
		{"同意 abcde", "abcde", fsp.DecisionAllowOnce},
		{"允许 abcde", "abcde", fsp.DecisionAllowOnce},
		{"总是 abcde", "abcde", fsp.DecisionAllowAlways},
		{"总是允许 abcde", "abcde", fsp.DecisionAllowAlways},
		{"始终允许 abcde", "abcde", fsp.DecisionAllowAlways},
		{"拒绝 abcde", "abcde", fsp.DecisionReject},
		{"不同意 abcde", "abcde", fsp.DecisionReject},
		{"不允许 abcde", "abcde", fsp.DecisionReject},

		// No space between a Chinese word and the ID.
		{"同意abcde", "abcde", fsp.DecisionAllowOnce},
		{"总是允许abcde", "abcde", fsp.DecisionAllowAlways},
		{"拒绝ABCDE", "abcde", fsp.DecisionReject},
		{"不同意abcde", "abcde", fsp.DecisionReject},

		// A full-width space, and the endings voice input and phones add.
		{"同意　abcde", "abcde", fsp.DecisionAllowOnce},
		{"同意 abcde。", "abcde", fsp.DecisionAllowOnce},
		{"y abcde.", "abcde", fsp.DecisionAllowOnce},
		{"拒绝！", "", fsp.DecisionReject},

		// A decision word alone: the core decides what it stands for.
		{"同意", "", fsp.DecisionAllowOnce},
		{"同意。", "", fsp.DecisionAllowOnce},
		{" 总是 ", "", fsp.DecisionAllowAlways},
		{"拒绝", "", fsp.DecisionReject},
		{"y", "", fsp.DecisionAllowOnce},
		{"Yes", "", fsp.DecisionAllowOnce},
		{"N", "", fsp.DecisionReject},
		{"no.", "", fsp.DecisionReject},

		// A comma or colon between the word and the ID, as a pinyin keyboard
		// puts it.
		{"同意，abcde", "abcde", fsp.DecisionAllowOnce},
		{"y, abcde", "abcde", fsp.DecisionAllowOnce},
		{"拒绝：abcde", "abcde", fsp.DecisionReject},
		{"同意，", "", fsp.DecisionAllowOnce},

		// "a" still always allows with its ID.
		{"a abcde", "abcde", fsp.DecisionAllowAlways},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			id, d, ok := h.m.ParseVerdict(tt.in)
			if !ok {
				t.Fatalf("ParseVerdict(%q) = not a verdict, want (%q, %s)", tt.in, tt.wantID, tt.want)
			}
			if id != tt.wantID || d != tt.want {
				t.Errorf("ParseVerdict(%q) = (%q, %s), want (%q, %s)", tt.in, id, d, tt.wantID, tt.want)
			}
		})
	}

	notVerdicts := []struct{ in, why string }{
		{"同意？", "a question is not a decision"},
		{"同意吗", "only the decision word itself"},
		{"我同意", "only the decision word itself"},
		{"同意 abcde 谢谢", "the whole message must be the verdict"},
		{"同意 abcd", "four characters is too short"},
		{"同意abcdl", "the alphabet has no l"},
		{"同意 好的", "the second word must be an ID"},
		{"yabcde", "an ASCII word needs a space before the ID"},
		{"好", "not a decision word"},
		{"ok", "not a decision word"},
		{"。", "punctuation only"},
		{"a", "a pinyin keyboard sends a lone a for 啊"},
		{"A", "a pinyin keyboard sends a lone a for 啊"},
		{"always", "always allowing needs the ID"},
	}
	for _, tt := range notVerdicts {
		if id, d, ok := h.m.ParseVerdict(tt.in); ok {
			t.Errorf("ParseVerdict(%q) = (%q, %s), want not a verdict (%s)", tt.in, id, d, tt.why)
		}
	}
}

// TestPendingListsTheEndpointsApprovals pins what the core reads to decide
// whether a bare "同意" may stand for an approval: the open approvals of one
// endpoint, oldest first, with when each was asked and when its question got
// through.
func TestPendingListsTheEndpointsApprovals(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	if got := h.m.Pending(ctx, owner); len(got) != 0 {
		t.Fatalf("Pending before any request = %+v, want none", got)
	}

	first := h.ask(ctx, request("execute"))
	firstMsg := h.outbox.next(t)
	firstID := h.pendingID(t)
	h.clk.Advance(3 * time.Second)
	second := h.ask(ctx, request("execute"))
	secondMsg := h.outbox.next(t)

	got := h.m.Pending(ctx, owner)
	if len(got) != 2 {
		t.Fatalf("Pending = %+v, want two approvals", got)
	}
	if got[0].ID != firstID || got[0].Session != session || !got[0].AskedAt.Equal(base) {
		t.Errorf("Pending[0] = %+v, want %s of %s asked at %v", got[0], firstID, session, base)
	}
	if !got[1].AskedAt.Equal(base.Add(3 * time.Second)) {
		t.Errorf("Pending[1].AskedAt = %v, want three seconds later", got[1].AskedAt)
	}
	// The fake outbox writes no rows: neither question has got through.
	if !got[0].ShownAt.IsZero() || !got[1].ShownAt.IsZero() {
		t.Errorf("Pending = %+v, want no ShownAt before delivery", got)
	}

	// The outbox accepts the first question, and holds the second one back.
	shown := base.Add(5 * time.Second)
	row, err := h.store.AddOutbox(ctx, firstMsg, base)
	mustOK(t, err)
	mustOK(t, h.store.UpdateOutbox(ctx, row, fsp.DeliveryAccepted, 1, shown))
	row, err = h.store.AddOutbox(ctx, secondMsg, base)
	mustOK(t, err)
	mustOK(t, h.store.HoldOutbox(ctx, row, shown))
	got = h.m.Pending(ctx, owner)
	if !got[0].ShownAt.Equal(shown) {
		t.Errorf("Pending[0].ShownAt = %v, want %v, when its row was accepted", got[0].ShownAt, shown)
	}
	if !got[1].ShownAt.IsZero() {
		t.Errorf("Pending[1].ShownAt = %v, want zero while its question is held", got[1].ShownAt)
	}

	other := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userB"}
	if got := h.m.Pending(ctx, other); len(got) != 0 {
		t.Errorf("Pending of another endpoint = %+v, want none", got)
	}

	h.m.Resolve(ctx, got[0].ID, fsp.DecisionReject, "userA")
	waitVerdict(t, first)
	h.m.Resolve(ctx, got[1].ID, fsp.DecisionReject, "userA")
	waitVerdict(t, second)
	if left := h.m.Pending(ctx, owner); len(left) != 0 {
		t.Errorf("Pending after both were answered = %+v, want none", left)
	}
	// Answered approvals are not lapses.
	if at := h.m.Lapsed(owner); !at.IsZero() {
		t.Errorf("Lapsed after human answers = %v, want zero", at)
	}
}

// TestLapsedNotesUnansweredEnds: a timeout and a cancellation both count, and
// only for the endpoint the approval was asked at.
func TestLapsedNotesUnansweredEnds(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{ApprovalTimeout: config.Duration{Duration: time.Minute}})
	ctx := context.Background()

	result := h.ask(ctx, request("execute"))
	h.outbox.next(t)
	h.clk.BlockUntil(1)
	h.clk.Advance(time.Minute)
	waitVerdict(t, result)
	h.outbox.next(t) // approval.timeout
	if at := h.m.Lapsed(owner); !at.Equal(base.Add(time.Minute)) {
		t.Errorf("Lapsed after a timeout = %v, want %v", at, base.Add(time.Minute))
	}

	h.clk.Advance(time.Minute)
	result = h.ask(ctx, request("execute"))
	h.outbox.next(t)
	h.pendingID(t)
	h.m.CancelSession(session)
	waitVerdict(t, result)
	if at := h.m.Lapsed(owner); !at.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("Lapsed after a cancellation = %v, want %v", at, base.Add(2*time.Minute))
	}

	other := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userB"}
	if at := h.m.Lapsed(other); !at.IsZero() {
		t.Errorf("Lapsed of another endpoint = %v, want zero", at)
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
