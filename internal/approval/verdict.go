package approval

import (
	"context"
	"crypto/rand"
	"math/big"
	"regexp"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// idAlphabet leaves out "l", which is too easy to read as "1" in a chat
// window. IDs are five characters long.
const (
	idAlphabet = "abcdefghijkmnopqrstuvwxyz"
	idLen      = 5
)

// verdictPattern recognises a whole message that is nothing but a decision,
// for example "y abcde" or "NO zxcvb".
var verdictPattern = regexp.MustCompile(`(?i)^\s*(y|yes|a|always|n|no)\s+([a-km-z]{5})\s*$`)

// ParseVerdict identifies a decision sent as a whole message.
func (m *manager) ParseVerdict(text string) (string, fsp.Decision, bool) {
	match := verdictPattern.FindStringSubmatch(text)
	if match == nil {
		return "", "", false
	}
	id := strings.ToLower(match[2])
	switch strings.ToLower(match[1]) {
	case "y", "yes":
		return id, fsp.DecisionAllowOnce, true
	case "a", "always":
		return id, fsp.DecisionAllowAlways, true
	default:
		return id, fsp.DecisionReject, true
	}
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
	m.reply(ctx, p, "approval.decided", map[string]any{"ID": id, "Decision": string(d)})
	return true
}

// CancelSession ends every approval still open for the session, as a refusal
// with no option chosen.
func (m *manager) CancelSession(id fsp.SessionID) {
	m.mu.Lock()
	var cancelled []*pendingApproval
	for key, p := range m.pending {
		if p.req.Session == id {
			cancelled = append(cancelled, p)
			delete(m.pending, key)
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
