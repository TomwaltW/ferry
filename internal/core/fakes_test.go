package core

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// ---------- i18n ----------

// fakeI18n renders "<key>|<lang>" plus the sorted arguments, so assertions can
// look at the key, the language and the parameters without depending on the
// wording of the catalogue.
type fakeI18n struct{}

func (fakeI18n) T(lang fsp.Lang, key string, args map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s", key, lang)
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(&b, " %s=%v", k, args[k])
	}
	return b.String()
}

// ---------- IM adapter ----------

type fakeIM struct {
	mu     sync.Mutex
	out    chan<- fsp.Inbound
	runs   int
	errs   []error // returned by successive Run calls; nil blocks until ctx ends
	status ports.AdapterStatus
	typing []bool
	sent   []fsp.Outbound
}

func (f *fakeIM) Platform() fsp.Platform { return fsp.PlatformFake }

func (f *fakeIM) Run(ctx context.Context, out chan<- fsp.Inbound) error {
	f.mu.Lock()
	f.out = out
	f.runs++
	var err error
	if len(f.errs) != 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (f *fakeIM) Send(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return fsp.DeliveryAccepted, nil
}

func (f *fakeIM) Typing(ctx context.Context, ep fsp.EndpointID, replyCtx string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, on)
	return nil
}

func (f *fakeIM) Status() ports.AdapterStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeIM) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *fakeIM) channel() chan<- fsp.Inbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out
}

func (f *fakeIM) typingCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.typing...)
}

// ---------- outbox ----------

type fakeOutbox struct {
	mu       sync.Mutex
	msgs     []fsp.Outbound
	windows  []ports.Window
	flushed  []fsp.EndpointID
	pulls    []fsp.EndpointID
	files    []int
	blockRun chan struct{}
}

func (f *fakeOutbox) OpenWindow(ep fsp.EndpointID, replyCtx string, inboundAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, ports.Window{Endpoint: ep, ReplyCtx: replyCtx, OpenedAt: inboundAt})
}

func (f *fakeOutbox) Enqueue(ctx context.Context, msg fsp.Outbound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	return nil
}

func (f *fakeOutbox) FlushHeld(ctx context.Context, ep fsp.EndpointID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushed = append(f.flushed, ep)
	return nil
}

func (f *fakeOutbox) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (f *fakeOutbox) all() []fsp.Outbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsp.Outbound(nil), f.msgs...)
}

func (f *fakeOutbox) texts() []string {
	out := []string{}
	for _, m := range f.all() {
		out = append(out, m.Text)
	}
	return out
}

func (f *fakeOutbox) windowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.windows)
}

// ---------- ACL ----------

type fakeACL struct {
	mu         sync.Mutex
	allowed    map[string]bool // sender ID -> allowed
	approvers  map[string]bool
	code       string
	offerErr   error
	offers     int
	confirmEp  fsp.EndpointID
	confirmWho string
	confirmErr error
	owner      [3]string
}

func (f *fakeACL) Allowed(ep fsp.EndpointID, senderID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowed[senderID]
}

func (f *fakeACL) IsApprover(ep fsp.EndpointID, senderID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.approvers[senderID]
}

func (f *fakeACL) SetOwner(ctx context.Context, platform fsp.Platform, botID, ownerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owner = [3]string{string(platform), botID, ownerID}
	return nil
}

func (f *fakeACL) OfferPairing(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offers++
	if f.offerErr != nil {
		return "", f.offerErr
	}
	return f.code, nil
}

func (f *fakeACL) ConfirmPairing(ctx context.Context, code string) (fsp.EndpointID, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confirmErr != nil {
		return fsp.EndpointID{}, "", f.confirmErr
	}
	return f.confirmEp, f.confirmWho, nil
}

func (f *fakeACL) ownerTuple() [3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owner
}

func (f *fakeACL) offerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offers
}

// ---------- approvals ----------

var verdictRe = regexp.MustCompile(`(?i)^\s*(y|yes|a|always|n|no)\s+([a-km-z]{5})\s*$`)

type resolved struct {
	id       string
	decision fsp.Decision
	by       string
}

type fakeApprovals struct {
	mu        sync.Mutex
	resolves  []resolved
	cancelled []fsp.SessionID
	asked     []fsp.ApprovalRequest
	release   chan struct{} // when set, Ask blocks until it is closed
	verdict   fsp.Verdict
}

func (f *fakeApprovals) Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	f.mu.Lock()
	f.asked = append(f.asked, req)
	release := f.release
	verdict := f.verdict
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return fsp.Verdict{}, ctx.Err()
		}
	}
	return verdict, nil
}

func (f *fakeApprovals) ParseVerdict(text string) (string, fsp.Decision, bool) {
	m := verdictRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	switch strings.ToLower(m[1]) {
	case "a", "always":
		return strings.ToLower(m[2]), fsp.DecisionAllowAlways, true
	case "n", "no":
		return strings.ToLower(m[2]), fsp.DecisionReject, true
	default:
		return strings.ToLower(m[2]), fsp.DecisionAllowOnce, true
	}
}

func (f *fakeApprovals) Resolve(ctx context.Context, id string, d fsp.Decision, by string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves = append(f.resolves, resolved{id: id, decision: d, by: by})
	return true
}

func (f *fakeApprovals) CancelSession(id fsp.SessionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
}

func (f *fakeApprovals) verdicts() []resolved {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolved(nil), f.resolves...)
}

func (f *fakeApprovals) cancels() []fsp.SessionID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsp.SessionID(nil), f.cancelled...)
}

func (f *fakeApprovals) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// ---------- commands ----------

// fakeCommands implements the command table of spec-p1 3.5: a command is the
// whole message, the name is case-insensitive, and "//" escapes.
type fakeCommands struct{}

var chineseCommands = map[string]string{
	"会话": "ls",
	"停":  "stop",
	"关闭": "close",
	"帮助": "help",
	"收件": "pull",
	"新建": "new",
}

func (fakeCommands) Unescape(text string) (string, bool) {
	if strings.HasPrefix(text, "//") {
		return text[1:], true
	}
	return text, false
}

func (fakeCommands) Parse(text string) (ports.Command, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return ports.Command{}, false
	}
	name := fields[0]
	if alias, ok := chineseCommands[name]; ok {
		return ports.Command{Name: alias, Args: fields[1:]}, true
	}
	if !strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") {
		return ports.Command{}, false
	}
	name = strings.ToLower(strings.TrimPrefix(name, "/"))
	switch name {
	case "ls", "new", "use", "agents", "stop", "close", "mode", "pull", "file", "lang", "help", "pair":
		return ports.Command{Name: name, Args: fields[1:]}, true
	}
	return ports.Command{}, false
}

// ---------- registry ----------

type fakeRegistry struct {
	mu     sync.Mutex
	specs  map[string]ports.SessionSpec
	agents []ports.AgentInfo
}

func (f *fakeRegistry) Resolve(ctx context.Context, agent string) (ports.SessionSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec, ok := f.specs[agent]
	if !ok {
		return ports.SessionSpec{}, fsp.ErrNotFound
	}
	return spec, nil
}

func (f *fakeRegistry) Agents(ctx context.Context) []ports.AgentInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.AgentInfo(nil), f.agents...)
}

// ---------- driver and agent session ----------

type fakeAgent struct {
	spec     ports.SessionSpec
	approver ports.Approver
	agentID  string
	events   chan fsp.Event

	mu         sync.Mutex
	prompts    []string
	cancels    int
	configs    [][2]string
	configErr  error
	closed     bool
	promptErrs []error
}

func newFakeAgent(spec ports.SessionSpec, ap ports.Approver, id string) *fakeAgent {
	return &fakeAgent{spec: spec, approver: ap, agentID: id, events: make(chan fsp.Event, 32)}
}

func (a *fakeAgent) AgentSessionID() string { return a.agentID }

func (a *fakeAgent) Caps() ports.Caps { return ports.Caps{Resume: true, Cancel: true, Config: true} }

func (a *fakeAgent) Prompt(ctx context.Context, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.promptErrs) != 0 {
		var err error
		err, a.promptErrs = a.promptErrs[0], a.promptErrs[1:]
		if err != nil {
			return err
		}
	}
	a.prompts = append(a.prompts, text)
	return nil
}

func (a *fakeAgent) Cancel(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels++
	return nil
}

func (a *fakeAgent) SetConfig(ctx context.Context, key, value string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.configErr != nil {
		return a.configErr
	}
	a.configs = append(a.configs, [2]string{key, value})
	return nil
}

func (a *fakeAgent) Events() <-chan fsp.Event { return a.events }

func (a *fakeAgent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed = true
		close(a.events)
	}
	return nil
}

// exit simulates the agent process going away on its own.
func (a *fakeAgent) exit() { a.Close() }

func (a *fakeAgent) emit(ev fsp.Event) { a.events <- ev }

func (a *fakeAgent) prompted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

func (a *fakeAgent) cancelCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancels
}

func (a *fakeAgent) configCalls() [][2]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][2]string(nil), a.configs...)
}

func (a *fakeAgent) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

type fakeDriver struct {
	mu        sync.Mutex
	kind      fsp.DriverKind // empty means ACP
	started   []*fakeAgent
	resumed   []*fakeAgent
	startErr  error
	resumeErr error
	next      int
}

func (f *fakeDriver) Kind() fsp.DriverKind {
	if f.kind == "" {
		return fsp.DriverACP
	}
	return f.kind
}

func (f *fakeDriver) Start(ctx context.Context, spec ports.SessionSpec, ap ports.Approver) (ports.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.next++
	a := newFakeAgent(spec, ap, fmt.Sprintf("agent-%d", f.next))
	f.started = append(f.started, a)
	return a, nil
}

func (f *fakeDriver) Resume(ctx context.Context, spec ports.SessionSpec, agentSessionID string, ap ports.Approver) (ports.AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	a := newFakeAgent(spec, ap, agentSessionID)
	f.resumed = append(f.resumed, a)
	f.started = append(f.started, a)
	return a, nil
}

func (f *fakeDriver) List(ctx context.Context, spec ports.SessionSpec) ([]ports.AgentSessionInfo, error) {
	return nil, fsp.ErrUnsupported
}

func (f *fakeDriver) agent(i int) *fakeAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.started) {
		return nil
	}
	return f.started[i]
}

func (f *fakeDriver) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func (f *fakeDriver) resumeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resumed)
}

// ---------- store ----------

// memStore is an in-memory ports.Store good enough to route messages.
type memStore struct {
	mu        sync.Mutex
	kv        map[string]string
	sessions  map[fsp.SessionID]fsp.Session
	order     []fsp.SessionID
	bindings  map[string][]ports.Binding
	seen      map[string]bool
	roles     map[string]string
	pairings  map[string]ports.Pairing
	audits    []ports.AuditEntry
	outbox    []ports.OutboxRow
	windows   map[string]ports.Window
	approvals []fsp.ApprovalRequest
	verdicts  []fsp.Verdict
	nextRow   int64
}

func newMemStore() *memStore {
	return &memStore{
		kv:       map[string]string{},
		sessions: map[fsp.SessionID]fsp.Session{},
		bindings: map[string][]ports.Binding{},
		seen:     map[string]bool{},
		roles:    map[string]string{},
		pairings: map[string]ports.Pairing{},
		windows:  map[string]ports.Window{},
	}
}

func (m *memStore) GetKV(ctx context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[key]
	if !ok {
		return "", fsp.ErrNotFound
	}
	return v, nil
}

func (m *memStore) SetKV(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[key] = value
	return nil
}

func (m *memStore) PutSession(ctx context.Context, s fsp.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.ID]; !ok {
		m.order = append(m.order, s.ID)
	}
	m.sessions[s.ID] = s
	return nil
}

func (m *memStore) GetSession(ctx context.Context, id fsp.SessionID) (fsp.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fsp.Session{}, fsp.ErrNotFound
	}
	return s, nil
}

func (m *memStore) ListSessions(ctx context.Context, owner fsp.EndpointID) ([]fsp.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []fsp.Session{}
	for _, id := range m.order {
		if s := m.sessions[id]; s.Owner == owner {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) ActiveSessions(ctx context.Context) ([]fsp.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []fsp.Session{}
	for _, id := range m.order {
		if s := m.sessions[id]; s.State != fsp.StateClosed {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) Bind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := ep.String()
	used := map[int]bool{}
	for _, b := range m.bindings[key] {
		used[b.Slot] = true
	}
	slot := 1
	for used[slot] {
		slot++
	}
	m.bindings[key] = append(m.bindings[key], ports.Binding{Endpoint: ep, Session: id, Slot: slot})
	return slot, nil
}

func (m *memStore) Unbind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := ep.String()
	kept := m.bindings[key][:0]
	for _, b := range m.bindings[key] {
		if b.Session != id {
			kept = append(kept, b)
		}
	}
	m.bindings[key] = kept
	return nil
}

func (m *memStore) SetForeground(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := ep.String()
	for i := range m.bindings[key] {
		m.bindings[key][i].Foreground = m.bindings[key][i].Session == id
	}
	return nil
}

func (m *memStore) Bindings(ctx context.Context, ep fsp.EndpointID) ([]ports.Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]ports.Binding(nil), m.bindings[ep.String()]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out, nil
}

func (m *memStore) SeenMessage(ctx context.Context, ep fsp.EndpointID, messageID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := ep.String() + "/" + messageID
	if m.seen[key] {
		return true, nil
	}
	m.seen[key] = true
	return false, nil
}

func (m *memStore) AddOutbox(ctx context.Context, msg fsp.Outbound, at time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRow++
	m.outbox = append(m.outbox, ports.OutboxRow{ID: m.nextRow, Msg: msg, State: fsp.DeliveryQueued, CreatedAt: at})
	return m.nextRow, nil
}

func (m *memStore) UpdateOutbox(ctx context.Context, id int64, state fsp.DeliveryState, attempts int, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.outbox {
		if m.outbox[i].ID == id {
			m.outbox[i].State, m.outbox[i].Attempts = state, attempts
		}
	}
	return nil
}

func (m *memStore) OutboxByState(ctx context.Context, ep fsp.EndpointID, state fsp.DeliveryState) ([]ports.OutboxRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ports.OutboxRow{}
	for _, r := range m.outbox {
		if r.Msg.Endpoint == ep && r.State == state {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) OutboxEndpoints(ctx context.Context, state fsp.DeliveryState) ([]fsp.EndpointID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	out := []fsp.EndpointID{}
	for _, r := range m.outbox {
		if r.State == state && !seen[r.Msg.Endpoint.String()] {
			seen[r.Msg.Endpoint.String()] = true
			out = append(out, r.Msg.Endpoint)
		}
	}
	return out, nil
}

func (m *memStore) SaveWindow(ctx context.Context, w ports.Window) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows[w.Endpoint.String()] = w
	return nil
}

func (m *memStore) LoadWindow(ctx context.Context, ep fsp.EndpointID) (ports.Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.windows[ep.String()]
	if !ok {
		return ports.Window{}, fsp.ErrNotFound
	}
	return w, nil
}

func (m *memStore) PutApproval(ctx context.Context, req fsp.ApprovalRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvals = append(m.approvals, req)
	return nil
}

func (m *memStore) DecideApproval(ctx context.Context, v fsp.Verdict, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdicts = append(m.verdicts, v)
	return nil
}

func (m *memStore) SetRole(ctx context.Context, ep fsp.EndpointID, senderID, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roles[ep.String()+"/"+senderID] = role
	return nil
}

func (m *memStore) Role(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.roles[ep.String()+"/"+senderID], nil
}

func (m *memStore) AddPairing(ctx context.Context, p ports.Pairing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pairings[p.Code] = p
	return nil
}

func (m *memStore) TakePairing(ctx context.Context, code string, now time.Time) (ports.Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pairings[code]
	if !ok || p.ExpiresAt.Before(now) {
		return ports.Pairing{}, fsp.ErrNotFound
	}
	delete(m.pairings, code)
	return p, nil
}

func (m *memStore) CountPairings(ctx context.Context, ep fsp.EndpointID, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pairings {
		if p.Endpoint == ep && p.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}

func (m *memStore) Audit(ctx context.Context, e ports.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, e)
	return nil
}

func (m *memStore) Close() error { return nil }

func (m *memStore) auditKinds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, e := range m.audits {
		out = append(out, e.Kind)
	}
	return out
}

func (m *memStore) auditEntries() []ports.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ports.AuditEntry(nil), m.audits...)
}

func (m *memStore) states() map[fsp.SessionID]fsp.SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[fsp.SessionID]fsp.SessionState{}
	for id, s := range m.sessions {
		out[id] = s.State
	}
	return out
}

var (
	_ ports.IMAdapter    = (*fakeIM)(nil)
	_ ports.Outbox       = (*fakeOutbox)(nil)
	_ ports.ACL          = (*fakeACL)(nil)
	_ ports.Approvals    = (*fakeApprovals)(nil)
	_ ports.Commands     = fakeCommands{}
	_ ports.I18n         = fakeI18n{}
	_ ports.Registry     = (*fakeRegistry)(nil)
	_ ports.Driver       = (*fakeDriver)(nil)
	_ ports.AgentSession = (*fakeAgent)(nil)
	_ ports.Store        = (*memStore)(nil)
)

// --- P2 ---------------------------------------------------------------------
// Pull, File and Held belong to the P2 T3 task; the core only has to call them,
// so the double records the calls and nothing more.

func (f *fakeOutbox) Pull(ctx context.Context, ep fsp.EndpointID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, ep)
	return nil
}

func (f *fakeOutbox) File(ctx context.Context, ep fsp.EndpointID, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append(f.files, n)
	return nil
}

func (f *fakeOutbox) Held(ep fsp.EndpointID) int { return 0 }

func (f *fakeOutbox) pullCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pulls)
}

func (f *fakeOutbox) fileCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.files...)
}

func (m *memStore) HoldOutbox(ctx context.Context, id int64, at time.Time) error {
	return fsp.ErrUnsupported
}

func (m *memStore) AddLongOutput(ctx context.Context, lo ports.LongOutput) (int64, error) {
	return 0, fsp.ErrUnsupported
}

func (m *memStore) LongOutputs(ctx context.Context, ep fsp.EndpointID, limit int) ([]ports.LongOutput, error) {
	return nil, fsp.ErrUnsupported
}

func (m *memStore) DeleteLongOutput(ctx context.Context, id int64) error { return fsp.ErrUnsupported }
