package approval

import (
	"context"
	"fmt"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
)

// TestDecidedWordsTheDecision pins the follow-up after a verdict to the real
// catalogs: it names the decision in words, never as the raw value
// ("allow_once") the drivers see.
func TestDecidedWordsTheDecision(t *testing.T) {
	cases := []struct {
		d      fsp.Decision
		lang   fsp.Lang
		option string
		want   string
	}{
		{fsp.DecisionAllowOnce, fsp.LangZH, "o-yes", "审批 %s 已处理：允许一次"},
		{fsp.DecisionAllowAlways, fsp.LangZH, "o-always", "审批 %s 已处理：总是允许"},
		{fsp.DecisionReject, fsp.LangZH, "o-no", "审批 %s 已处理：拒绝"},
		{fsp.DecisionAllowOnce, fsp.LangEN, "o-yes", "Approval %s resolved: allowed once"},
		{fsp.DecisionAllowAlways, fsp.LangEN, "o-always", "Approval %s resolved: always allowed"},
		{fsp.DecisionReject, fsp.LangEN, "o-no", "Approval %s resolved: rejected"},
	}
	for _, tc := range cases {
		t.Run(string(tc.lang)+"/"+string(tc.d), func(t *testing.T) {
			h := newHarness(t, config.PolicyConfig{})
			h.m.i18n = i18n.Default()
			h.m.lang = tc.lang
			ctx := context.Background()

			result := h.ask(ctx, request("execute"))
			h.outbox.next(t) // the question
			id := h.pendingID(t)
			if !h.m.Resolve(ctx, id, tc.d, "userA") {
				t.Fatal("Resolve = false, want true")
			}
			if v := waitVerdict(t, result); v.OptionID != tc.option {
				t.Errorf("option = %q, want %q", v.OptionID, tc.option)
			}

			if got, want := h.outbox.next(t).Text, fmt.Sprintf(tc.want, id); got != want {
				t.Errorf("follow-up = %q, want %q", got, want)
			}
		})
	}
}
