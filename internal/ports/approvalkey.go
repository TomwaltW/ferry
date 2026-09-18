package ports

import (
	"fmt"
	"regexp"
	"time"
)

// ApprovalRef is what the dedupe key of an approval message says about the
// approval it asks, so that an entry point with buttons can put them on it
// (spec-p4-b1 3.6). The outbox hands an adapter the text of that message
// joined with whatever was queued before it and with Kind rewritten to
// OutText; the key is the one part that reaches the adapter unchanged.
type ApprovalRef struct {
	ID          string // the five letters of fsp.ApprovalRequest.ID
	AllowAlways bool   // the request offers an allow_always option
}

// approvalKeyRe matches the keys ApprovalDedupeKey makes, and the "-<n>" the
// outbox appends to every segment after the first.
var approvalKeyRe = regexp.MustCompile(`^approval-([a-km-z]{5})-([ao])-([0-9]+)(?:-[0-9]+)?$`)

// ApprovalDedupeKey is the DedupeKey the approval module gives the message
// that asks ref: "approval-<id>-<a|o>-<unix ms>", a when allow_always is on
// offer and o when it is not, at being the CreatedAt of the request. The time
// keeps apart two approvals that reuse an ID, which matters on WeChat, where
// the key is the client_id the gateway deduplicates on.
func ApprovalDedupeKey(ref ApprovalRef, at time.Time) string {
	flag := "o"
	if ref.AllowAlways {
		flag = "a"
	}
	return fmt.Sprintf("approval-%s-%s-%d", ref.ID, flag, max(at.UnixMilli(), 0))
}

// ParseApprovalDedupeKey reads a key ApprovalDedupeKey made, with or without a
// segment suffix. Every other key reports false.
func ParseApprovalDedupeKey(key string) (ApprovalRef, bool) {
	m := approvalKeyRe.FindStringSubmatch(key)
	if m == nil {
		return ApprovalRef{}, false
	}
	return ApprovalRef{ID: m[1], AllowAlways: m[2] == "a"}, true
}
