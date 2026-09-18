package approval

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fakeWorkspaces hands out roots per endpoint and sender, and records who was
// asked about.
type fakeWorkspaces struct {
	mu    sync.Mutex
	roots map[string][]string // "<endpoint>|<sender>" -> roots
	err   error
	asked []string
}

func (f *fakeWorkspaces) Roots(ctx context.Context, ep fsp.EndpointID, senderID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ep.String() + "|" + senderID
	f.asked = append(f.asked, key)
	if f.err != nil {
		return nil, f.err
	}
	return f.roots[key], nil
}

var _ ports.Workspaces = (*fakeWorkspaces)(nil)

// withWorkspaces rebuilds the harness's manager with a Workspaces behind it.
func (h *harness) withWorkspaces(t *testing.T, w ports.Workspaces, roots ...string) {
	t.Helper()
	h.m = New(Options{
		Roots:       roots,
		Workspaces:  w,
		DefaultLang: fsp.LangZH,
		Store:       h.store,
		Outbox:      h.outbox,
		I18n:        fakeI18n{},
		Clock:       h.clk,
	}).(*manager)
}

// outside_workspace is judged against the roots of the session's owner, not
// the shared roots: an edit in the shared root is outside a user who works in
// a directory of their own, and one in that directory is inside.
func TestOutsideWorkspaceUsesSessionRoots(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	shared := realDir(t, t.TempDir())
	userDir := realDir(t, t.TempDir())
	ws := &fakeWorkspaces{roots: map[string][]string{owner.String() + "|" + owner.PeerID: {userDir}}}
	h.withWorkspaces(t, ws, shared)
	ctx := context.Background()

	if got := h.m.evaluate(ctx, request("edit", shared)); got != actionDeny {
		t.Errorf("evaluate(edit in the shared root) = %q, want %q: it is outside the session's roots", got, actionDeny)
	}
	if got := h.m.evaluate(ctx, request("edit", userDir)); got != actionAsk {
		t.Errorf("evaluate(edit in the user's root) = %q, want %q", got, actionAsk)
	}
	ws.mu.Lock()
	asked := append([]string(nil), ws.asked...)
	ws.mu.Unlock()
	if len(asked) == 0 || asked[0] != owner.String()+"|"+owner.PeerID {
		t.Errorf("Workspaces asked about %q, want the session owner %s|%s", asked, owner, owner.PeerID)
	}

	// Reading needs no roots at all.
	before := len(asked)
	if got := h.m.evaluate(ctx, request("read", shared)); got != actionAllow {
		t.Errorf("evaluate(read) = %q, want %q", got, actionAllow)
	}
	ws.mu.Lock()
	after := len(ws.asked)
	ws.mu.Unlock()
	if after != before {
		t.Error("a rule without outside_workspace asked Workspaces for roots")
	}

	// Roots failing counts as outside, the strictest reading (spec-p4-b1 3.12),
	// and so does a session nobody can find.
	ws.mu.Lock()
	ws.err = errors.New("users_root is gone")
	ws.mu.Unlock()
	if got := h.m.evaluate(ctx, request("edit", userDir)); got != actionDeny {
		t.Errorf("evaluate(edit) with Roots failing = %q, want %q", got, actionDeny)
	}
	ws.mu.Lock()
	ws.err = nil
	ws.mu.Unlock()
	lost := request("edit", userDir)
	lost.Session = "sess_missing"
	if got := h.m.evaluate(ctx, lost); got != actionDeny {
		t.Errorf("evaluate(edit) for an unknown session = %q, want %q", got, actionDeny)
	}
}

// Without a Workspaces every session is judged against the shared roots, as in
// P1–P3.
func TestNilWorkspacesKeepsGlobalRoots(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	if got := h.m.evaluate(ctx, request("edit", h.root)); got != actionAsk {
		t.Errorf("evaluate(edit in the shared root) = %q, want %q", got, actionAsk)
	}
	outside := realDir(t, t.TempDir())
	if got := h.m.evaluate(ctx, request("edit", outside)); got != actionDeny {
		t.Errorf("evaluate(edit outside) = %q, want %q", got, actionDeny)
	}
}

// askAndTake asks req, returns the message it sent, and answers it so Ask
// returns.
func (h *harness) askAndTake(t *testing.T, req fsp.ApprovalRequest) (fsp.Outbound, string) {
	t.Helper()
	ch := h.ask(context.Background(), req)
	// Skip whatever an earlier approval left behind, such as its verdict.
	msg := h.outbox.next(t)
	for msg.Kind != fsp.OutApproval {
		msg = h.outbox.next(t)
	}
	id := h.pendingID(t)
	if !h.m.Resolve(context.Background(), id, fsp.DecisionReject, "userA") {
		t.Fatal("Resolve = false, want true")
	}
	waitVerdict(t, ch)
	return msg, id
}

func TestApprovalMessageCarriesKey(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	msg, id := h.askAndTake(t, request("execute"))

	if msg.Kind != fsp.OutApproval {
		t.Fatalf("first message kind = %v, want OutApproval", msg.Kind)
	}
	want := ports.ApprovalDedupeKey(ports.ApprovalRef{ID: id, AllowAlways: true}, base)
	if msg.DedupeKey != want {
		t.Errorf("DedupeKey = %q, want %q", msg.DedupeKey, want)
	}
	ref, ok := ports.ParseApprovalDedupeKey(msg.DedupeKey)
	if !ok || ref.ID != id {
		t.Errorf("ParseApprovalDedupeKey(%q) = (%+v, %v), want the approval %s", msg.DedupeKey, ref, ok, id)
	}

	// Only the question carries it: the follow-ups are plain messages.
	for _, m := range h.outbox.all() {
		if m.Kind == fsp.OutApproval {
			continue
		}
		if m.DedupeKey != "" {
			t.Errorf("follow-up %q carries DedupeKey %q, want none", m.Text, m.DedupeKey)
		}
	}

	// A second approval reusing the ID at another time gets another key.
	later := request("execute")
	later.ID = id
	later.CreatedAt = base.Add(2e9)
	msg2, _ := h.askAndTake(t, later)
	if msg2.DedupeKey == msg.DedupeKey {
		t.Errorf("two approvals share the key %q, want distinct keys", msg.DedupeKey)
	}
}

func TestApprovalKeyMarksAllowAlways(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})

	with, _ := h.askAndTake(t, request("execute"))
	if ref, ok := ports.ParseApprovalDedupeKey(with.DedupeKey); !ok || !ref.AllowAlways {
		t.Errorf("key %q with an allow_always option = (%+v, %v), want AllowAlways", with.DedupeKey, ref, ok)
	}

	req := request("execute")
	req.Options = []fsp.ApprovalOption{
		{ID: "o-yes", Kind: fsp.OptAllowOnce, Label: "allow"},
		{ID: "o-no", Kind: fsp.OptRejectOnce, Label: "reject"},
	}
	without, _ := h.askAndTake(t, req)
	if ref, ok := ports.ParseApprovalDedupeKey(without.DedupeKey); !ok || ref.AllowAlways {
		t.Errorf("key %q without an allow_always option = (%+v, %v), want no AllowAlways", without.DedupeKey, ref, ok)
	}
}

func realDir(t *testing.T, dir string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return path
}
