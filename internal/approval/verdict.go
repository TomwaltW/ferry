package approval

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// idAlphabet leaves out "l", which is too easy to read as "1" in a chat
// window. IDs are five characters long.
const (
	idAlphabet = "abcdefghijkmnopqrstuvwxyz"
	idLen      = 5
)

// verdictWords are the words a decision can be written with. The ASCII ones
// must be followed by a space before the ID; the Chinese ones may run into it
// ("同意abcde"), which is how a Chinese keyboard types it.
var verdictWords = map[string]fsp.Decision{
	"y":      fsp.DecisionAllowOnce,
	"yes":    fsp.DecisionAllowOnce,
	"同意":     fsp.DecisionAllowOnce,
	"允许":     fsp.DecisionAllowOnce,
	"a":      fsp.DecisionAllowAlways,
	"always": fsp.DecisionAllowAlways,
	"总是":     fsp.DecisionAllowAlways,
	"总是允许":   fsp.DecisionAllowAlways,
	"始终允许":   fsp.DecisionAllowAlways,
	"n":      fsp.DecisionReject,
	"no":     fsp.DecisionReject,
	"拒绝":     fsp.DecisionReject,
	"不同意":    fsp.DecisionReject,
	"不允许":    fsp.DecisionReject,
}

// closingMarks are the sentence endings a verdict may carry, voice input in
// particular: WeChat transcribes a spoken "同意" as "同意。". A question mark is
// not among them, because a question is not a decision.
const closingMarks = "。．.！!～~"

// ParseVerdict identifies a decision sent as a whole message: a decision word
// and an ID ("y abcde", "同意 abcde", "同意abcde"), or the word alone ("同意"),
// which reports an empty ID and leaves the core to decide whether it may stand
// for the one approval waiting.
func (m *manager) ParseVerdict(text string) (string, fsp.Decision, bool) {
	text = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(text), closingMarks))
	fields := strings.Fields(text)
	switch len(fields) {
	case 1:
		if d, ok := verdictWords[strings.ToLower(fields[0])]; ok {
			return "", d, true
		}
		return gluedVerdict(fields[0])
	case 2:
		d, ok := verdictWords[strings.ToLower(fields[0])]
		id := strings.ToLower(fields[1])
		if !ok || !idPattern.MatchString(id) {
			return "", "", false
		}
		return id, d, true
	}
	return "", "", false
}

// gluedVerdict reads a Chinese decision word with the ID right after it.
func gluedVerdict(word string) (string, fsp.Decision, bool) {
	for w, d := range verdictWords {
		rest, ok := strings.CutPrefix(word, w)
		if !ok || w[0] < utf8.RuneSelf {
			continue
		}
		if id := strings.ToLower(rest); idPattern.MatchString(id) {
			return id, d, true
		}
	}
	return "", "", false
}

// Pending lists the approvals asked at ep that nobody has answered yet, oldest
// first, each with when its question got through: the outbox row it was queued
// under has been accepted by the platform. A question still queued, held back,
// refused or of unknown fate has a zero ShownAt.
func (m *manager) Pending(ctx context.Context, ep fsp.EndpointID) []ports.PendingApproval {
	type entry struct {
		open ports.PendingApproval
		key  string
	}
	m.mu.Lock()
	var entries []entry
	for id, p := range m.pending {
		if p.owner == ep {
			entries = append(entries, entry{ports.PendingApproval{ID: id, Session: p.req.Session, AskedAt: p.asked}, p.key})
		}
	}
	m.mu.Unlock()

	open := make([]ports.PendingApproval, 0, len(entries))
	for _, e := range entries {
		row, err := m.store.OutboxByKey(ctx, ep, e.key)
		switch {
		case err == nil && row.State == fsp.DeliveryAccepted:
			e.open.ShownAt = row.UpdatedAt
		case err != nil && !errors.Is(err, fsp.ErrNotFound):
			m.log.Error("approval: look up the question", "id", e.open.ID, "error", err)
		}
		open = append(open, e.open)
	}
	slices.SortFunc(open, func(a, b ports.PendingApproval) int {
		if c := a.AskedAt.Compare(b.AskedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return open
}

// Lapsed reports when an approval asked at ep last ended without a human
// answer, timed out or cancelled; zero when none has.
func (m *manager) Lapsed(ep fsp.EndpointID) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lapsed[ep]
}

// Resolve applies a human decision. It reports false for an ID that is not
// waiting for an answer, which the caller drops without a word. An agent that
// does not offer the chosen option gets a nudge instead of a verdict, and the
// approval stays open.
func (m *manager) Resolve(ctx context.Context, id string, d fsp.Decision, by string) bool {
	m.mu.Lock()
	p, ok := m.pending[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	optionID, available := optionFor(p.req.Options, d)
	if !available {
		m.mu.Unlock()
		m.reply(ctx, p, "approval.option_unavailable", map[string]any{"ID": id})
		return true
	}
	delete(m.pending, id)
	m.mu.Unlock()

	v := fsp.Verdict{ApprovalID: id, Decision: d, OptionID: optionID, By: by}
	p.result <- v
	m.persist(ctx, v, p.req)
	m.reply(ctx, p, "approval.decided", map[string]any{"ID": id, "Decision": m.decisionText(d)})
	return true
}

// decisionText is the decision as the follow-up message words it, never the raw
// value the drivers see.
func (m *manager) decisionText(d fsp.Decision) string {
	switch d {
	case fsp.DecisionAllowOnce:
		return m.t("approval.decision.allow_once", nil)
	case fsp.DecisionAllowAlways:
		return m.t("approval.decision.allow_always", nil)
	default:
		return m.t("approval.decision.reject", nil)
	}
}

// CancelSession ends every approval still open for the session, as a refusal
// with no option chosen.
func (m *manager) CancelSession(id fsp.SessionID) {
	m.mu.Lock()
	var cancelled []*pendingApproval
	now := m.clk.Now()
	for key, p := range m.pending {
		if p.req.Session == id {
			cancelled = append(cancelled, p)
			delete(m.pending, key)
			m.lapsed[p.owner] = now
		}
	}
	m.mu.Unlock()

	ctx := context.Background()
	for _, p := range cancelled {
		v := fsp.Verdict{ApprovalID: p.req.ID, Decision: fsp.DecisionReject, By: byCancel}
		p.result <- v
		m.persist(ctx, v, p.req)
	}
}

// optionFor maps a decision onto one of the agent's options. Refusing always
// works, with an empty option ID standing for a cancellation; allowing needs
// the matching option to exist.
func optionFor(options []fsp.ApprovalOption, d fsp.Decision) (string, bool) {
	switch d {
	case fsp.DecisionAllowOnce:
		return firstOptionID(options, fsp.OptAllowOnce)
	case fsp.DecisionAllowAlways:
		return firstOptionID(options, fsp.OptAllowAlways)
	default:
		id, _, _ := firstOption(options, fsp.OptRejectOnce, fsp.OptRejectAlways)
		return id, true
	}
}

func firstOptionID(options []fsp.ApprovalOption, kind fsp.ApprovalOptionKind) (string, bool) {
	id, _, ok := firstOption(options, kind)
	return id, ok
}

// idPattern is the shape of an approval ID: exactly the alphabet and length
// freshIDLocked produces. A verdict must name an ID of this shape (compared in
// lower case), and a request that already carries one keeps it: it comes from
// a peer that has shown it to a human — Claude Code's permission relay issues
// the ID before Ferry ever sees the request — so reusing it keeps one ID on
// both sides of the relay.
var idPattern = regexp.MustCompile(`^[a-km-z]{5}$`)

// idForLocked keeps the ID the request arrived with when it is well formed and
// free, and assigns a new one otherwise. The caller holds m.mu.
func (m *manager) idForLocked(preset string) string {
	if idPattern.MatchString(preset) {
		if _, taken := m.pending[preset]; !taken {
			return preset
		}
	}
	return m.freshIDLocked()
}

// freshIDLocked returns an ID no pending approval is using. The caller holds
// m.mu.
func (m *manager) freshIDLocked() string {
	for {
		id := newID()
		if _, taken := m.pending[id]; !taken {
			return id
		}
	}
}

func newID() string {
	limit := big.NewInt(int64(len(idAlphabet)))
	var b strings.Builder
	b.Grow(idLen)
	for range idLen {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic("approval: crypto/rand unavailable: " + err.Error())
		}
		b.WriteByte(idAlphabet[n.Int64()])
	}
	return b.String()
}
