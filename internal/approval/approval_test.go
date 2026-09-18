package approval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/store"
)

var (
	owner   = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userA"}
	base    = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	session = fsp.SessionID("sess_1")
)

type harness struct {
	m      *manager
	store  ports.Store
	outbox *fakeOutbox
	clk    *clock.Fake
	root   string
}

// newHarness wires the approval manager to a real store, a fake outbox and a
// fake clock, with one workspace root and one session owned by owner.
func newHarness(t *testing.T, policy config.PolicyConfig, roots ...string) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ferry.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	root := t.TempDir()
	if len(roots) == 0 {
		roots = []string{root}
	}
	if err := st.PutSession(context.Background(), fsp.Session{
		ID: session, Driver: fsp.DriverACP, Agent: "qwen-code", Cwd: root,
		Origin: fsp.OriginManaged, State: fsp.StateRunning, Owner: owner,
		CreatedAt: base, LastActive: base,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	ob := newFakeOutbox()
	clk := clock.NewFake(base)
	ap := New(Options{
		Policy:      policy,
		Roots:       roots,
		DefaultLang: fsp.LangZH,
		Store:       st,
		Outbox:      ob,
		I18n:        fakeI18n{},
		Clock:       clk,
	})
	return &harness{m: ap.(*manager), store: st, outbox: ob, clk: clk, root: root}
}

func request(kind string, paths ...string) fsp.ApprovalRequest {
	return fsp.ApprovalRequest{
		Session:      session,
		ToolName:     "fs_" + kind,
		ToolKind:     kind,
		Description:  "do something",
		InputPreview: "rm -rf /tmp/x",
		Paths:        paths,
		Options: []fsp.ApprovalOption{
			{ID: "o-yes", Kind: fsp.OptAllowOnce, Label: "allow"},
			{ID: "o-always", Kind: fsp.OptAllowAlways, Label: "always"},
			{ID: "o-no", Kind: fsp.OptRejectOnce, Label: "reject"},
		},
		CreatedAt: base,
	}
}

// pendingID returns the single pending approval's ID.
func (h *harness) pendingID(t *testing.T) string {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if len(h.m.pending) != 1 {
		t.Fatalf("%d approvals pending, want 1", len(h.m.pending))
	}
	for id := range h.m.pending {
		return id
	}
	return ""
}

func (h *harness) pendingCount() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return len(h.m.pending)
}

// ask runs Ask in the background and hands back the channel it will answer on.
func (h *harness) ask(ctx context.Context, req fsp.ApprovalRequest) chan fsp.Verdict {
	out := make(chan fsp.Verdict, 1)
	go func() {
		v, err := h.m.Ask(ctx, req)
		if err != nil {
			panic("Ask returned an error: " + err.Error())
		}
		out <- v
	}()
	return out
}

func waitVerdict(t *testing.T, ch chan fsp.Verdict) fsp.Verdict {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
		return fsp.Verdict{}
	}
}

func TestParseVerdict(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	tests := []struct {
		in       string
		wantID   string
		wantDec  fsp.Decision
		wantOK   bool
		whyNotOK string
	}{
		{in: "y abcde", wantID: "abcde", wantDec: fsp.DecisionAllowOnce, wantOK: true},
		{in: "yes abcde", wantID: "abcde", wantDec: fsp.DecisionAllowOnce, wantOK: true},
		{in: "Y ABCDE", wantID: "abcde", wantDec: fsp.DecisionAllowOnce, wantOK: true},
		{in: "  a  bcdef  ", wantID: "bcdef", wantDec: fsp.DecisionAllowAlways, wantOK: true},
		{in: "always zyxwv", wantID: "zyxwv", wantDec: fsp.DecisionAllowAlways, wantOK: true},
		{in: "n abcde", wantID: "abcde", wantDec: fsp.DecisionReject, wantOK: true},
		{in: "NO abcde", wantID: "abcde", wantDec: fsp.DecisionReject, wantOK: true},
		{in: "y\tabcde", wantID: "abcde", wantDec: fsp.DecisionAllowOnce, wantOK: true},

		{in: "y abcdl", whyNotOK: "the alphabet has no l"},
		{in: "y abcd", whyNotOK: "four characters is too short"},
		{in: "y abcdef", whyNotOK: "six characters is too long"},
		{in: "y abcde please", whyNotOK: "the whole message must be the verdict"},
		{in: "please y abcde", whyNotOK: "the whole message must be the verdict"},
		{in: "yabcde", whyNotOK: "the verdict and the ID are separate words"},
		{in: "maybe abcde", whyNotOK: "unknown verdict word"},
		{in: "y abc1e", whyNotOK: "IDs are letters only"},
		{in: "", whyNotOK: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			id, d, ok := h.m.ParseVerdict(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ParseVerdict(%q) ok = %v, want %v (%s)", tt.in, ok, tt.wantOK, tt.whyNotOK)
			}
			if !tt.wantOK {
				return
			}
			if id != tt.wantID || d != tt.wantDec {
				t.Errorf("ParseVerdict(%q) = (%q, %s), want (%q, %s)", tt.in, id, d, tt.wantID, tt.wantDec)
			}
		})
	}
}

func TestIDAlphabet(t *testing.T) {
	seen := make(map[rune]bool)
	for range 2000 {
		id := newID()
		if len(id) != idLen {
			t.Fatalf("newID() = %q, want %d characters", id, idLen)
		}
		for _, r := range id {
			if !strings.ContainsRune(idAlphabet, r) {
				t.Fatalf("newID() = %q, which is outside the alphabet %q", id, idAlphabet)
			}
			seen[r] = true
		}
	}
	if seen['l'] {
		t.Error("newID() produced an l, which the alphabet excludes")
	}
	if len(seen) != len(idAlphabet) {
		t.Errorf("%d distinct characters over 10000 draws, want all %d", len(seen), len(idAlphabet))
	}
	// The verdict pattern must accept what newID produces.
	h := newHarness(t, config.PolicyConfig{})
	for range 200 {
		id := newID()
		got, _, ok := h.m.ParseVerdict("y " + id)
		if !ok || got != id {
			t.Fatalf("ParseVerdict(%q) = (%q, %v), want the ID back", "y "+id, got, ok)
		}
	}
}

func TestPolicyDefaults(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	inside := filepath.Join(h.root, "a.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", inside, err)
	}
	outside := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", outside, err)
	}

	tests := []struct {
		name string
		req  fsp.ApprovalRequest
		want string
	}{
		{"read is free", request("read", outside), actionAllow},
		{"search is free", request("search"), actionAllow},
		{"think is free", request("think"), actionAllow},
		{"delete outside the workspace", request("delete", outside), actionDeny},
		{"edit outside the workspace", request("edit", inside, outside), actionDeny},
		{"move inside the workspace", request("move", inside), actionAsk},
		{"delete with no path at all", request("delete"), actionAsk},
		{"execute falls to the default", request("execute"), actionAsk},
		{"an unknown kind falls to the default", request("teleport"), actionAsk},
		{"an empty kind falls to the default", request(""), actionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.m.evaluate(tt.req); got != tt.want {
				t.Errorf("evaluate = %q, want %q", got, tt.want)
			}
		})
	}

	// The action maps onto the agent's options without asking anyone.
	ctx := context.Background()
	v, err := h.m.Ask(ctx, request("read", inside))
	if err != nil {
		t.Fatalf("Ask(read): %v", err)
	}
	if v.Decision != fsp.DecisionAllowOnce || v.OptionID != "o-yes" || v.By != byPolicy {
		t.Errorf("Ask(read) = %+v, want allow_once on o-yes decided by the policy", v)
	}
	if len(v.ApprovalID) != idLen {
		t.Errorf("Ask(read) ApprovalID = %q, want %d characters", v.ApprovalID, idLen)
	}

	v, err = h.m.Ask(ctx, request("delete", outside))
	if err != nil {
		t.Fatalf("Ask(delete): %v", err)
	}
	if v.Decision != fsp.DecisionReject || v.OptionID != "o-no" || v.By != byPolicy {
		t.Errorf("Ask(delete outside) = %+v, want a refusal on o-no decided by the policy", v)
	}
	h.outbox.quiet(t)
	if h.pendingCount() != 0 {
		t.Errorf("%d approvals left pending, want 0", h.pendingCount())
	}

	// With no option to refuse with, the verdict cancels instead.
	req := request("delete", outside)
	req.Options = []fsp.ApprovalOption{{ID: "o-yes", Kind: fsp.OptAllowOnce}}
	v, err = h.m.Ask(ctx, req)
	if err != nil {
		t.Fatalf("Ask(delete without a reject option): %v", err)
	}
	if v.Decision != fsp.DecisionReject || v.OptionID != "" {
		t.Errorf("Ask = %+v, want a refusal with an empty option ID", v)
	}

	// An allow with only allow_always available uses it.
	req = request("read", inside)
	req.Options = []fsp.ApprovalOption{{ID: "o-always", Kind: fsp.OptAllowAlways}}
	v, err = h.m.Ask(ctx, req)
	if err != nil {
		t.Fatalf("Ask(read with only allow_always): %v", err)
	}
	if v.Decision != fsp.DecisionAllowAlways || v.OptionID != "o-always" {
		t.Errorf("Ask = %+v, want allow_always on o-always", v)
	}
}

func TestPolicyCustomRules(t *testing.T) {
	yes, no := true, false
	h := newHarness(t, config.PolicyConfig{
		Default: actionDeny,
		Rules: []config.PolicyRule{
			{Kind: []string{"execute"}, CommandPattern: `^git `, Action: actionAllow},
			{Kind: []string{"execute"}, Action: actionDeny},
			{OutsideWorkspace: &no, Action: actionAllow},
			{OutsideWorkspace: &yes, Action: actionAsk},
		},
	})
	inside := filepath.Join(h.root, "a.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", inside, err)
	}
	outside := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", outside, err)
	}

	gitStatus := request("execute")
	gitStatus.InputPreview = "git status"
	rm := request("execute")
	rm.InputPreview = "rm -rf /"

	tests := []struct {
		name string
		req  fsp.ApprovalRequest
		want string
	}{
		{"the first matching rule wins", gitStatus, actionAllow},
		{"a later rule catches the rest", rm, actionDeny},
		{"a rule with no kind matches any kind", request("delete", inside), actionAllow},
		{"paths outside the workspace", request("delete", inside, outside), actionAsk},
		{"no paths counts as inside", request("fetch"), actionAllow},
		{"the built-in rules are replaced", request("read", outside), actionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.m.evaluate(tt.req); got != tt.want {
				t.Errorf("evaluate = %q, want %q", got, tt.want)
			}
		})
	}

	// Nothing matches at all: the configured default applies.
	bare := newHarness(t, config.PolicyConfig{
		Default: actionDeny,
		Rules:   []config.PolicyRule{{Kind: []string{"read"}, Action: actionAllow}},
	})
	if got := bare.m.evaluate(request("execute")); got != actionDeny {
		t.Errorf("evaluate with nothing matching = %q, want %q", got, actionDeny)
	}

	// A rule that does not compile must not widen the policy: the built-in
	// rules take over.
	broken := newHarness(t, config.PolicyConfig{
		Rules: []config.PolicyRule{{CommandPattern: `([`, Action: actionAllow}},
	})
	if got := broken.m.evaluate(request("execute")); got != actionAsk {
		t.Errorf("evaluate with a broken rule = %q, want %q", got, actionAsk)
	}
	if got := broken.m.evaluate(request("read")); got != actionAllow {
		t.Errorf("evaluate with a broken rule = %q, want the built-in rules back", got)
	}
}

func TestSymlinkOutside(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})

	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
	link := filepath.Join(h.root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if h.m.inside(link) {
		t.Error("a symlink out of the workspace counts as inside, want outside")
	}
	if got := h.m.evaluate(request("delete", link)); got != actionDeny {
		t.Errorf("evaluate(delete through the symlink) = %q, want %q", got, actionDeny)
	}

	// A directory symlink is resolved the same way.
	linkDir := filepath.Join(h.root, "dir")
	if err := os.Symlink(elsewhere, linkDir); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	if h.m.inside(filepath.Join(linkDir, "secret.txt")) {
		t.Error("a path through a directory symlink counts as inside, want outside")
	}

	// A real file inside the workspace is inside, symlinks or not.
	real := filepath.Join(h.root, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", real, err)
	}
	if !h.m.inside(real) {
		t.Error("a real file in the workspace counts as outside, want inside")
	}

	// A path that cannot be resolved at all counts as outside.
	if h.m.inside(filepath.Join(h.root, "does-not-exist.txt")) {
		t.Error("an unresolvable path counts as inside, want outside")
	}
}

func TestAskAndResolve(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	result := h.ask(ctx, request("execute"))
	msg := h.outbox.next(t)
	id := h.pendingID(t)

	if msg.Endpoint != owner || msg.Session != session || msg.Kind != fsp.OutApproval || !msg.Urgent {
		t.Errorf("approval message = %+v, want an urgent approval to the session owner", msg)
	}
	if !strings.HasPrefix(msg.Text, "approval.request{") {
		t.Errorf("approval message text = %q, want the approval.request key", msg.Text)
	}
	for _, want := range []string{"ID=" + id, "Tool=fs_execute", "approval.always_hint"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("approval message %q is missing %q", msg.Text, want)
		}
	}

	if !h.m.Resolve(ctx, id, fsp.DecisionAllowOnce, "userA") {
		t.Fatal("Resolve = false, want true")
	}
	v := waitVerdict(t, result)
	if v.ApprovalID != id || v.Decision != fsp.DecisionAllowOnce || v.OptionID != "o-yes" || v.By != "userA" {
		t.Errorf("verdict = %+v, want allow_once on o-yes by userA", v)
	}
	if h.pendingCount() != 0 {
		t.Errorf("%d approvals still pending, want 0", h.pendingCount())
	}

	decided := h.outbox.next(t)
	if !strings.Contains(decided.Text, "approval.decided{") || !strings.Contains(decided.Text, "ID="+id) {
		t.Errorf("follow-up = %q, want approval.decided for %s", decided.Text, id)
	}

	// The same ID cannot be answered twice.
	if h.m.Resolve(ctx, id, fsp.DecisionReject, "userA") {
		t.Error("Resolve on a settled approval = true, want false")
	}

	// The request and the verdict both reached the store.
	if _, err := h.store.GetSession(ctx, session); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if err := h.store.DecideApproval(ctx, fsp.Verdict{ApprovalID: id, Decision: fsp.DecisionReject}, base); err != nil {
		t.Errorf("the approval row is missing from the store: %v", err)
	}
}

func TestTimeoutRejects(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{ApprovalTimeout: config.Duration{Duration: 2 * time.Minute}})
	ctx := context.Background()

	result := h.ask(ctx, request("execute"))
	h.outbox.next(t)
	id := h.pendingID(t)

	// Short of the deadline nothing happens.
	h.clk.BlockUntil(1)
	h.clk.Advance(time.Minute)
	select {
	case v := <-result:
		t.Fatalf("Ask returned %+v before the timeout", v)
	case <-time.After(50 * time.Millisecond):
	}

	h.clk.Advance(2 * time.Minute)
	v := waitVerdict(t, result)
	if v.ApprovalID != id || v.Decision != fsp.DecisionReject || v.OptionID != "o-no" || v.By != byTimeout {
		t.Errorf("verdict = %+v, want a refusal on o-no by the timeout", v)
	}
	if h.pendingCount() != 0 {
		t.Errorf("%d approvals still pending, want 0", h.pendingCount())
	}

	msg := h.outbox.next(t)
	if !strings.Contains(msg.Text, "approval.timeout{") || !strings.Contains(msg.Text, "ID="+id) {
		t.Errorf("follow-up = %q, want approval.timeout for %s", msg.Text, id)
	}

	// A verdict that arrives after the timeout is ignored.
	if h.m.Resolve(ctx, id, fsp.DecisionAllowOnce, "userA") {
		t.Error("Resolve after the timeout = true, want false")
	}
}

func TestUnknownIDIgnored(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	if h.m.Resolve(ctx, "zzzzz", fsp.DecisionAllowOnce, "userA") {
		t.Error("Resolve of an unknown ID = true, want false")
	}
	if h.m.Resolve(ctx, "", fsp.DecisionReject, "userA") {
		t.Error("Resolve of an empty ID = true, want false")
	}
	h.outbox.quiet(t)

	// An ID that belonged to a settled approval is just as unknown.
	result := h.ask(ctx, request("execute"))
	h.outbox.next(t)
	id := h.pendingID(t)
	if !h.m.Resolve(ctx, id, fsp.DecisionReject, "userA") {
		t.Fatal("Resolve = false, want true")
	}
	waitVerdict(t, result)
	h.outbox.next(t) // approval.decided
	if h.m.Resolve(ctx, id, fsp.DecisionReject, "userA") {
		t.Error("Resolve of a settled ID = true, want false")
	}
}

func TestAlwaysUnavailable(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	req := request("execute")
	req.Options = []fsp.ApprovalOption{
		{ID: "o-yes", Kind: fsp.OptAllowOnce},
		{ID: "o-no", Kind: fsp.OptRejectOnce},
	}
	result := h.ask(ctx, req)
	msg := h.outbox.next(t)
	id := h.pendingID(t)

	if strings.Contains(msg.Text, "approval.always_hint") {
		t.Errorf("approval message %q offers \"always\" although the agent has no such option", msg.Text)
	}

	if !h.m.Resolve(ctx, id, fsp.DecisionAllowAlways, "userA") {
		t.Error("Resolve of an unavailable option = false, want true (the ID is known)")
	}
	nudge := h.outbox.next(t)
	if !strings.Contains(nudge.Text, "approval.option_unavailable") {
		t.Errorf("follow-up = %q, want approval.option_unavailable", nudge.Text)
	}
	select {
	case v := <-result:
		t.Fatalf("Ask returned %+v, want it still waiting", v)
	case <-time.After(50 * time.Millisecond):
	}
	if h.pendingCount() != 1 {
		t.Fatalf("%d approvals pending, want the approval to stay open", h.pendingCount())
	}

	// A decision the agent does support settles it.
	if !h.m.Resolve(ctx, id, fsp.DecisionAllowOnce, "userA") {
		t.Fatal("Resolve = false, want true")
	}
	v := waitVerdict(t, result)
	if v.Decision != fsp.DecisionAllowOnce || v.OptionID != "o-yes" {
		t.Errorf("verdict = %+v, want allow_once on o-yes", v)
	}
}

func TestCancelSession(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx := context.Background()

	first := h.ask(ctx, request("execute"))
	h.outbox.next(t)
	second := h.ask(ctx, request("fetch"))
	h.outbox.next(t)

	// Wait for both to register before cancelling.
	deadline := time.Now().Add(5 * time.Second)
	for h.pendingCount() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d approvals pending, want 2", h.pendingCount())
		}
		time.Sleep(time.Millisecond)
	}

	h.m.CancelSession("sess_other")
	if h.pendingCount() != 2 {
		t.Fatalf("cancelling another session settled %d approvals", 2-h.pendingCount())
	}

	h.m.CancelSession(session)
	for _, ch := range []chan fsp.Verdict{first, second} {
		v := waitVerdict(t, ch)
		if v.Decision != fsp.DecisionReject || v.OptionID != "" || v.By != byCancel {
			t.Errorf("verdict = %+v, want a refusal with no option, by cancel", v)
		}
	}
	if h.pendingCount() != 0 {
		t.Errorf("%d approvals still pending, want 0", h.pendingCount())
	}
}

func TestContextCancelled(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})
	ctx, cancel := context.WithCancel(context.Background())

	result := h.ask(ctx, request("execute"))
	h.outbox.next(t)
	h.pendingID(t)

	cancel()
	v := waitVerdict(t, result)
	if v.Decision != fsp.DecisionReject || v.OptionID != "" || v.By != byCancel {
		t.Errorf("verdict = %+v, want a refusal with no option, by cancel", v)
	}
	if h.pendingCount() != 0 {
		t.Errorf("%d approvals still pending, want 0", h.pendingCount())
	}
}

func TestOwnerlessSessionRejects(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{})

	req := request("execute")
	req.Session = "sess_unknown"
	v, err := h.m.Ask(context.Background(), req)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if v.Decision != fsp.DecisionReject || v.OptionID != "o-no" {
		t.Errorf("Ask for a session with no owner = %+v, want a refusal", v)
	}
	h.outbox.quiet(t)
}

// --- P3: an ID the caller brought along ------------------------------------

func TestPresetIDAccepted(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{Default: "ask"})
	ctx := context.Background()

	req := request("execute")
	req.ID = "qrstu" // the shape Claude Code's permission relay issues
	ch := h.ask(ctx, req)

	// The question reaching the outbox is what says the approval is registered.
	msg := h.outbox.next(t)
	if got := h.pendingID(t); got != "qrstu" {
		t.Fatalf("the approval is pending as %q, want the ID the request came with", got)
	}
	if !strings.Contains(msg.Text, "ID=qrstu") {
		t.Errorf("the question reads %q, want it to quote ID=qrstu", msg.Text)
	}

	if !h.m.Resolve(ctx, "qrstu", fsp.DecisionAllowOnce, "userA") {
		t.Fatal("Resolve did not find the approval under the preset ID")
	}
	v := waitVerdict(t, ch)
	if v.ApprovalID != "qrstu" || v.Decision != fsp.DecisionAllowOnce || v.OptionID != "o-yes" {
		t.Errorf("verdict = %+v, want qrstu allowed once through o-yes", v)
	}

	// A preset only survives when it is one of ours; anything else is replaced.
	for _, bad := range []string{"", "abc", "abcdef", "abcdl", "AB CD", "ab3de"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			h := newHarness(t, config.PolicyConfig{Default: "ask"})
			req := request("execute")
			req.ID = bad
			h.ask(context.Background(), req)
			h.outbox.next(t)
			if got := h.pendingID(t); got == bad {
				t.Errorf("the module kept %q, which is not a well-formed ID", got)
			} else if !presetIDPattern.MatchString(got) {
				t.Errorf("the replacement ID %q is not well formed either", got)
			}
		})
	}
}

func TestPresetIDCollision(t *testing.T) {
	h := newHarness(t, config.PolicyConfig{Default: "ask"})
	ctx := context.Background()

	first := request("execute")
	first.ID = "qrstu"
	firstCh := h.ask(ctx, first)
	h.outbox.next(t)
	if got := h.pendingID(t); got != "qrstu" {
		t.Fatalf("the first approval is pending as %q, want qrstu", got)
	}

	// A second request carrying the same ID must not take the first one's
	// place: whoever answers "y qrstu" would otherwise settle the wrong call.
	second := request("delete")
	second.ID = "qrstu"
	secondCh := h.ask(ctx, second)
	h.outbox.next(t)

	if got := h.pendingCount(); got != 2 {
		t.Fatalf("%d approvals pending, want 2", got)
	}
	var other string
	h.m.mu.Lock()
	for id, p := range h.m.pending {
		if id != "qrstu" {
			other = id
			if p.req.ToolKind != "delete" {
				t.Errorf("the freshly assigned ID went to the %q request, want the second one", p.req.ToolKind)
			}
		}
	}
	h.m.mu.Unlock()
	if other == "" {
		t.Fatal("the second request reused qrstu")
	}
	if !presetIDPattern.MatchString(other) {
		t.Errorf("the assigned ID %q is not well formed", other)
	}

	// Each ID settles its own approval.
	if !h.m.Resolve(ctx, other, fsp.DecisionReject, "userA") {
		t.Fatal("Resolve did not find the second approval")
	}
	if v := waitVerdict(t, secondCh); v.ApprovalID != other || v.Decision != fsp.DecisionReject {
		t.Errorf("second verdict = %+v, want %s rejected", v, other)
	}
	if !h.m.Resolve(ctx, "qrstu", fsp.DecisionAllowOnce, "userA") {
		t.Fatal("Resolve did not find the first approval")
	}
	if v := waitVerdict(t, firstCh); v.ApprovalID != "qrstu" || v.Decision != fsp.DecisionAllowOnce {
		t.Errorf("first verdict = %+v, want qrstu allowed", v)
	}
}
