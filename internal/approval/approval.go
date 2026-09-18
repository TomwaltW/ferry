package approval

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/security"
)

type Options struct {
	Policy      config.PolicyConfig
	Roots       []string // normalised workspace roots
	DefaultLang fsp.Lang
	Store       ports.Store
	Outbox      ports.Outbox
	I18n        ports.I18n
	Clock       clock.Clock
	Log         *slog.Logger
}

// defaultTimeout is how long an unanswered approval waits before it counts as
// a refusal.
const defaultTimeout = 10 * time.Minute

// How the deciders identify themselves in the audit log.
const (
	byPolicy  = "policy"
	byTimeout = "timeout"
	byCancel  = "cancel"
)

// Sanitiser budgets for the two free-text fields of an approval message.
const (
	descriptionBudget = 300
	previewBudget     = 800
)

type manager struct {
	rules         []rule
	defaultAction string
	roots         []string
	lang          fsp.Lang
	store         ports.Store
	outbox        ports.Outbox
	i18n          ports.I18n
	clk           clock.Clock
	log           *slog.Logger
	timeout       time.Duration

	mu      sync.Mutex
	pending map[string]*pendingApproval
}

// pendingApproval is an approval waiting for a human. result carries the
// verdict from whoever settles it back to the blocked Ask.
type pendingApproval struct {
	req    fsp.ApprovalRequest
	owner  fsp.EndpointID
	result chan fsp.Verdict
}

var _ ports.Approvals = (*manager)(nil)

func New(opts Options) ports.Approvals {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}

	configured := opts.Policy.Rules
	if len(configured) == 0 {
		configured = defaultRules
	}
	rules, err := compileRules(configured)
	if err != nil {
		// A broken rule must not silently widen the policy: fall back to the
		// built-in set, which is the safe one.
		log.Error("approval: falling back to the built-in policy", "error", err)
		rules, _ = compileRules(defaultRules)
	}

	action := strings.ToLower(strings.TrimSpace(opts.Policy.Default))
	switch action {
	case actionAllow, actionAsk, actionDeny:
	default:
		action = actionAsk
	}

	timeout := opts.Policy.ApprovalTimeout.Duration
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	return &manager{
		rules:         rules,
		defaultAction: action,
		roots:         normalizeRoots(opts.Roots),
		lang:          opts.DefaultLang,
		store:         opts.Store,
		outbox:        opts.Outbox,
		i18n:          opts.I18n,
		clk:           clk,
		log:           log,
		timeout:       timeout,
		pending:       make(map[string]*pendingApproval),
	}
}

// Ask applies the policy and, when the policy says so, puts the question to
// the session owner and blocks until it is answered, times out or the context
// ends.
func (m *manager) Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	if req.CreatedAt.IsZero() {
		req.CreatedAt = m.clk.Now()
	}

	switch m.evaluate(req) {
	case actionAllow:
		if id, kind, ok := firstOption(req.Options, fsp.OptAllowOnce, fsp.OptAllowAlways); ok {
			decision := fsp.DecisionAllowOnce
			if kind == fsp.OptAllowAlways {
				decision = fsp.DecisionAllowAlways
			}
			return m.auto(ctx, req, decision, id), nil
		}
		// Nothing to allow with: ask after all.
	case actionDeny:
		id, _, _ := firstOption(req.Options, fsp.OptRejectOnce, fsp.OptRejectAlways)
		return m.auto(ctx, req, fsp.DecisionReject, id), nil
	}
	return m.askOwner(ctx, req)
}

// auto records a request the policy decided on its own and returns the verdict.
func (m *manager) auto(ctx context.Context, req fsp.ApprovalRequest, d fsp.Decision, optionID string) fsp.Verdict {
	m.mu.Lock()
	req.ID = m.idForLocked(req.ID)
	m.mu.Unlock()

	m.record(ctx, req)
	v := fsp.Verdict{ApprovalID: req.ID, Decision: d, OptionID: optionID, By: byPolicy}
	m.persist(ctx, v, req)
	return v
}

// askOwner registers the approval, sends the question and waits.
func (m *manager) askOwner(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	owner, err := m.sessionOwner(ctx, req.Session)
	if err != nil {
		// With nobody to ask, refusing is the only safe answer.
		m.log.Error("approval: no owner to ask", "session", req.Session, "error", err)
		id, _, _ := firstOption(req.Options, fsp.OptRejectOnce, fsp.OptRejectAlways)
		return m.auto(ctx, req, fsp.DecisionReject, id), nil
	}

	m.mu.Lock()
	req.ID = m.idForLocked(req.ID)
	p := &pendingApproval{req: req, owner: owner, result: make(chan fsp.Verdict, 1)}
	m.pending[req.ID] = p
	m.mu.Unlock()

	m.record(ctx, req)
	m.enqueue(ctx, owner, req.Session, fsp.OutApproval, m.requestText(req))

	timeout := m.clk.After(m.timeout)
	select {
	case v := <-p.result:
		return v, nil
	case <-timeout:
		if _, won := m.take(req.ID); !won {
			// Someone answered as the timer fired; their verdict is on its way.
			return <-p.result, nil
		}
		clean := context.WithoutCancel(ctx)
		v := rejectVerdict(req, byTimeout)
		m.persist(clean, v, req)
		m.reply(clean, p, "approval.timeout", map[string]any{"ID": req.ID})
		return v, nil
	case <-ctx.Done():
		if _, won := m.take(req.ID); !won {
			return <-p.result, nil
		}
		clean := context.WithoutCancel(ctx)
		v := fsp.Verdict{ApprovalID: req.ID, Decision: fsp.DecisionReject, By: byCancel}
		m.persist(clean, v, req)
		return v, nil
	}
}

// requestText renders the approval question. The description and the preview
// are shortened and the preview is also masked, because it is the field that
// carries whatever the agent was about to run.
func (m *manager) requestText(req fsp.ApprovalRequest) string {
	hint := ""
	if _, _, ok := firstOption(req.Options, fsp.OptAllowAlways); ok {
		hint = m.t("approval.always_hint", map[string]any{"ID": req.ID})
	}
	return m.t("approval.request", map[string]any{
		"ID":          req.ID,
		"Tool":        req.ToolName,
		"Description": security.SanitizeForIM(req.Description, descriptionBudget),
		"Preview":     security.MaskSecrets(security.SanitizeForIM(req.InputPreview, previewBudget)),
		"AlwaysHint":  hint,
	})
}

func (m *manager) sessionOwner(ctx context.Context, id fsp.SessionID) (fsp.EndpointID, error) {
	s, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fsp.EndpointID{}, err
	}
	return s.Owner, nil
}

// take removes the pending approval and reports whether this caller is the one
// that got it. Only the winner may settle it.
func (m *manager) take(id string) (*pendingApproval, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return nil, false
	}
	delete(m.pending, id)
	return p, true
}

// record stores the request and notes that it was asked.
func (m *manager) record(ctx context.Context, req fsp.ApprovalRequest) {
	if err := m.store.PutApproval(ctx, req); err != nil {
		m.log.Error("approval: store request", "id", req.ID, "error", err)
	}
	m.audit(ctx, ports.AuditEntry{
		At:      req.CreatedAt,
		Kind:    "approval_asked",
		Session: req.Session,
		Detail:  "tool=" + req.ToolName + " kind=" + normalizeKind(req.ToolKind),
	})
}

// persist stores the verdict and notes who decided it.
func (m *manager) persist(ctx context.Context, v fsp.Verdict, req fsp.ApprovalRequest) {
	now := m.clk.Now()
	if err := m.store.DecideApproval(ctx, v, now); err != nil {
		m.log.Error("approval: store verdict", "id", v.ApprovalID, "error", err)
	}
	m.audit(ctx, ports.AuditEntry{
		At:       now,
		Kind:     "approval_decided",
		SenderID: v.By,
		Session:  req.Session,
		Detail:   "id=" + v.ApprovalID + " decision=" + string(v.Decision),
	})
}

func (m *manager) audit(ctx context.Context, e ports.AuditEntry) {
	if err := m.store.Audit(ctx, e); err != nil {
		m.log.Error("approval: audit", "kind", e.Kind, "error", err)
	}
}

// reply sends a follow-up about an approval to the owner that was asked.
func (m *manager) reply(ctx context.Context, p *pendingApproval, key string, args map[string]any) {
	m.enqueue(ctx, p.owner, p.req.Session, fsp.OutText, m.t(key, args))
}

func (m *manager) enqueue(ctx context.Context, ep fsp.EndpointID, session fsp.SessionID, kind fsp.OutKind, text string) {
	if m.outbox == nil {
		return
	}
	msg := fsp.Outbound{Endpoint: ep, Session: session, Kind: kind, Text: text, Urgent: true}
	if err := m.outbox.Enqueue(ctx, msg); err != nil {
		m.log.Error("approval: enqueue", "endpoint", ep, "error", err)
	}
}

func (m *manager) t(key string, args map[string]any) string {
	if m.i18n == nil {
		return key
	}
	return m.i18n.T(m.lang, key, args)
}

// rejectVerdict picks the option that says no, falling back to an empty option
// ID, which the drivers read as a cancellation.
func rejectVerdict(req fsp.ApprovalRequest, by string) fsp.Verdict {
	id, _, _ := firstOption(req.Options, fsp.OptRejectOnce, fsp.OptRejectAlways)
	return fsp.Verdict{ApprovalID: req.ID, Decision: fsp.DecisionReject, OptionID: id, By: by}
}

// firstOption returns the ID of the first option matching any of kinds, in the
// order the kinds are given.
func firstOption(options []fsp.ApprovalOption, kinds ...fsp.ApprovalOptionKind) (string, fsp.ApprovalOptionKind, bool) {
	for _, want := range kinds {
		for _, o := range options {
			if o.Kind == want {
				return o.ID, o.Kind, true
			}
		}
	}
	return "", "", false
}
