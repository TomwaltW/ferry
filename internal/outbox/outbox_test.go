package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// ---------- test doubles ----------

// memStore is the slice of ports.Store the outbox uses, kept in memory. The
// methods the outbox never calls return fsp.ErrUnsupported so that a mistake
// shows up instead of passing quietly.
type memStore struct {
	mu      sync.Mutex
	rows    []ports.OutboxRow // index+1 is the row ID
	windows map[string]ports.Window
	audits  []ports.AuditEntry
}

func newMemStore() *memStore {
	return &memStore{windows: make(map[string]ports.Window)}
}

func (s *memStore) AddOutbox(_ context.Context, msg fsp.Outbound, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := int64(len(s.rows) + 1)
	s.rows = append(s.rows, ports.OutboxRow{ID: id, Msg: msg, State: fsp.DeliveryQueued, CreatedAt: at})
	return id, nil
}

func (s *memStore) UpdateOutbox(_ context.Context, id int64, state fsp.DeliveryState, attempts int, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || int(id) > len(s.rows) {
		return fsp.ErrNotFound
	}
	s.rows[id-1].State = state
	s.rows[id-1].Attempts = attempts
	return nil
}

func (s *memStore) OutboxByState(_ context.Context, ep fsp.EndpointID, state fsp.DeliveryState) ([]ports.OutboxRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.OutboxRow
	for _, r := range s.rows {
		if r.Msg.Endpoint == ep && r.State == state {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *memStore) OutboxEndpoints(_ context.Context, state fsp.DeliveryState) ([]fsp.EndpointID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fsp.EndpointID
	seen := make(map[fsp.EndpointID]bool)
	for _, r := range s.rows {
		if r.State != state || seen[r.Msg.Endpoint] {
			continue
		}
		seen[r.Msg.Endpoint] = true
		out = append(out, r.Msg.Endpoint)
	}
	return out, nil
}

func (s *memStore) SaveWindow(_ context.Context, w ports.Window) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windows[w.Endpoint.String()] = w
	return nil
}

func (s *memStore) LoadWindow(_ context.Context, ep fsp.EndpointID) (ports.Window, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.windows[ep.String()]
	if !ok {
		return ports.Window{}, fsp.ErrNotFound
	}
	return w, nil
}

func (s *memStore) Audit(_ context.Context, e ports.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, e)
	return nil
}

func (s *memStore) row(t *testing.T, id int64) ports.OutboxRow {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || int(id) > len(s.rows) {
		t.Fatalf("row %d does not exist (%d rows)", id, len(s.rows))
	}
	return s.rows[id-1]
}

func (s *memStore) window(t *testing.T, ep fsp.EndpointID) ports.Window {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.windows[ep.String()]
	if !ok {
		t.Fatalf("no window for %s", ep)
	}
	return w
}

func (s *memStore) auditKinds() []ports.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ports.AuditEntry(nil), s.audits...)
}

// Everything below is part of ports.Store but not of the outbox's world.
func (s *memStore) GetKV(context.Context, string) (string, error) { return "", fsp.ErrNotFound }
func (s *memStore) SetKV(context.Context, string, string) error   { return fsp.ErrUnsupported }
func (s *memStore) PutSession(context.Context, fsp.Session) error { return fsp.ErrUnsupported }
func (s *memStore) Close() error                                  { return nil }
func (s *memStore) GetSession(context.Context, fsp.SessionID) (fsp.Session, error) {
	return fsp.Session{}, fsp.ErrNotFound
}

func (s *memStore) ListSessions(context.Context, fsp.EndpointID) ([]fsp.Session, error) {
	return nil, fsp.ErrUnsupported
}

func (s *memStore) ActiveSessions(context.Context) ([]fsp.Session, error) {
	return nil, fsp.ErrUnsupported
}

func (s *memStore) Bind(context.Context, fsp.EndpointID, fsp.SessionID) (int, error) {
	return 0, fsp.ErrUnsupported
}

func (s *memStore) Unbind(context.Context, fsp.EndpointID, fsp.SessionID) error {
	return fsp.ErrUnsupported
}

func (s *memStore) SetForeground(context.Context, fsp.EndpointID, fsp.SessionID) error {
	return fsp.ErrUnsupported
}

func (s *memStore) Bindings(context.Context, fsp.EndpointID) ([]ports.Binding, error) {
	return nil, fsp.ErrUnsupported
}

func (s *memStore) SeenMessage(context.Context, fsp.EndpointID, string, time.Time) (bool, error) {
	return false, fsp.ErrUnsupported
}

func (s *memStore) PutApproval(context.Context, fsp.ApprovalRequest) error {
	return fsp.ErrUnsupported
}

func (s *memStore) DecideApproval(context.Context, fsp.Verdict, time.Time) error {
	return fsp.ErrUnsupported
}

func (s *memStore) SetRole(context.Context, fsp.EndpointID, string, string) error {
	return fsp.ErrUnsupported
}

func (s *memStore) Role(context.Context, fsp.EndpointID, string) (string, error) {
	return "", fsp.ErrUnsupported
}

func (s *memStore) AddPairing(context.Context, ports.Pairing) error { return fsp.ErrUnsupported }

func (s *memStore) TakePairing(context.Context, string, time.Time) (ports.Pairing, error) {
	return ports.Pairing{}, fsp.ErrNotFound
}

func (s *memStore) CountPairings(context.Context, fsp.EndpointID, time.Time) (int, error) {
	return 0, fsp.ErrUnsupported
}

var _ ports.Store = (*memStore)(nil)

// fakeIM records what was sent and hands out scripted outcomes.
type fakeIM struct {
	mu      sync.Mutex
	sent    []fsp.Outbound
	ctxs    []string
	results []result
}

type result struct {
	state fsp.DeliveryState
	err   error
}

func (f *fakeIM) Send(_ context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	f.ctxs = append(f.ctxs, replyCtx)
	if len(f.results) == 0 {
		return fsp.DeliveryAccepted, nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.state, r.err
}

func (f *fakeIM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeIM) messages() []fsp.Outbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsp.Outbound(nil), f.sent...)
}

func (f *fakeIM) replyCtx(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxs[i]
}

func (f *fakeIM) script(rs ...result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = rs
}

func (f *fakeIM) Platform() fsp.Platform { return fsp.PlatformFake }

func (f *fakeIM) Run(ctx context.Context, _ chan<- fsp.Inbound) error {
	<-ctx.Done()
	return nil
}

func (f *fakeIM) Typing(context.Context, fsp.EndpointID, string, bool) error { return nil }

func (f *fakeIM) Status() ports.AdapterStatus {
	return ports.AdapterStatus{Online: true, BotID: "bot", OwnerID: "owner"}
}

var _ ports.IMAdapter = (*fakeIM)(nil)

// fakeI18n renders a key and its arguments verbatim so that tests can assert
// on the outcome without depending on the catalogue's wording.
type fakeI18n struct{}

func (fakeI18n) T(_ fsp.Lang, key string, args map[string]any) string {
	if len(args) == 0 {
		return key
	}
	return fmt.Sprintf("%s(N=%v)", key, args["N"])
}

// ---------- harness ----------

var testEndpoint = fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "peer"}

const (
	sessionA fsp.SessionID = "sess_a"
	sessionB fsp.SessionID = "sess_b"
)

type harness struct {
	t     *testing.T
	box   *box
	store *memStore
	im    *fakeIM
	clk   *clock.Fake
	ctx   context.Context
	start time.Time
}

func newBox(t *testing.T, tune func(*config.OutboxConfig)) *harness {
	t.Helper()
	cfg := config.OutboxConfig{
		MergeInterval:       config.Duration{Duration: 30 * time.Second},
		ReplyBudget:         8,
		LongOutputThreshold: 1500,
		SendTimeout:         config.Duration{Duration: 15 * time.Second},
	}
	if tune != nil {
		tune(&cfg)
	}
	start := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	h := &harness{
		t:     t,
		store: newMemStore(),
		im:    &fakeIM{},
		clk:   clock.NewFake(start),
		ctx:   context.Background(),
		start: start,
	}
	h.box = New(Options{
		Config: cfg,
		IM:     h.im,
		Store:  h.store,
		I18n:   fakeI18n{},
		Clock:  h.clk,
		Log:    slog.New(slog.DiscardHandler),
	}).(*box)
	return h
}

// run starts the send loop and waits until its self-check ticker exists, so
// that a later Advance cannot be missed.
func newRunning(t *testing.T, tune func(*config.OutboxConfig)) *harness {
	t.Helper()
	h := newBox(t, tune)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := h.box.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	h.clk.BlockUntil(1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return h
}

func (h *harness) open(replyCtx string) {
	h.t.Helper()
	h.box.OpenWindow(testEndpoint, replyCtx, h.clk.Now())
}

func (h *harness) enqueue(session fsp.SessionID, text string, urgent bool) {
	h.t.Helper()
	msg := fsp.Outbound{
		Endpoint: testEndpoint,
		Session:  session,
		Kind:     fsp.OutText,
		Text:     text,
		Urgent:   urgent,
	}
	if err := h.box.Enqueue(h.ctx, msg); err != nil {
		h.t.Fatalf("Enqueue(%q): %v", text, err)
	}
}

// waitFor polls until cond holds. It never waits on a business timeout: the
// fake clock drives those, and this only bridges goroutine scheduling.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s (sent %d)", what, h.im.count())
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) waitSent(n int) {
	h.t.Helper()
	h.waitFor(fmt.Sprintf("%d sent messages", n), func() bool { return h.im.count() >= n })
}

// quiesce waits for two complete queue passes, the second of which is
// guaranteed to have started after this call. Only then does "nothing was
// sent" mean the loop looked and declined.
func (h *harness) quiesce() {
	h.t.Helper()
	start := h.box.passes.Load()
	h.waitFor("a full queue pass", func() bool {
		h.box.signal()
		return h.box.passes.Load() >= start+2
	})
}

func (h *harness) sentTexts() []string {
	out := []string{}
	for _, m := range h.im.messages() {
		out = append(out, m.Text)
	}
	return out
}

// ---------- tests ----------

func TestUrgentImmediate(t *testing.T) {
	h := newRunning(t, nil)
	h.open("ctx-1")
	h.enqueue(sessionA, "done", true)

	h.waitSent(1)
	msgs := h.im.messages()
	if msgs[0].Text != "done" {
		t.Errorf("text = %q, want %q", msgs[0].Text, "done")
	}
	if msgs[0].DedupeKey != "ob-1-1" {
		t.Errorf("dedupe key = %q, want ob-1-1", msgs[0].DedupeKey)
	}
	if got := h.im.replyCtx(0); got != "ctx-1" {
		t.Errorf("reply context = %q, want ctx-1", got)
	}

	h.waitFor("the row to be accepted", func() bool {
		return h.store.row(t, 1).State == fsp.DeliveryAccepted
	})
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Errorf("window used = %d, want 1", got)
	}
}

func TestMergeInterval(t *testing.T) {
	h := newRunning(t, nil)
	h.open("ctx-1")
	h.enqueue(sessionA, "first", false)
	h.enqueue(sessionA, "second", false)

	// Not yet: the merge interval has not run out.
	h.clk.Advance(10 * time.Second)
	h.quiesce()
	if n := h.im.count(); n != 0 {
		t.Fatalf("sent %d messages before the merge interval, want 0: %q", n, h.sentTexts())
	}

	h.clk.Advance(20 * time.Second)
	h.waitSent(1)
	h.quiesce()
	if n := h.im.count(); n != 1 {
		t.Fatalf("sent %d messages, want 1 merged one: %q", n, h.sentTexts())
	}
	if got, want := h.im.messages()[0].Text, "first\n\nsecond"; got != want {
		t.Errorf("merged text = %q, want %q", got, want)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Errorf("window used = %d, want 1 for the merged message", got)
	}

	// A different session merges separately.
	h.enqueue(sessionA, "third", false)
	h.enqueue(sessionB, "other", false)
	h.clk.Advance(30 * time.Second)
	h.waitSent(3)
	texts := h.sentTexts()
	if texts[1] != "third" || texts[2] != "other" {
		t.Errorf("per-session merge = %q, want [... third other]", texts)
	}
}

func TestReplyBudgetReserve(t *testing.T) {
	h := newRunning(t, func(c *config.OutboxConfig) { c.ReplyBudget = 2 })
	h.open("ctx-1")

	// Budget 2: a non-urgent message may take the first reply.
	h.enqueue(sessionA, "chunk one", false)
	h.clk.Advance(30 * time.Second)
	h.waitSent(1)

	// Budget 1 now, and that last reply belongs to urgent messages only.
	h.enqueue(sessionA, "chunk two", false)
	h.clk.Advance(30 * time.Second)
	h.quiesce()
	if n := h.im.count(); n != 1 {
		t.Fatalf("sent %d, want the non-urgent message to stay queued: %q", n, h.sentTexts())
	}
	if got := h.store.row(t, 2).State; got != fsp.DeliveryQueued {
		t.Errorf("row 2 state = %q, want queued", got)
	}

	// The urgent message takes the reserved reply and carries the waiting
	// non-urgent text at its front.
	h.enqueue(sessionA, "turn finished", true)
	h.waitSent(2)
	if got, want := h.im.messages()[1].Text, "chunk two\n\nturn finished"; got != want {
		t.Errorf("urgent text = %q, want %q", got, want)
	}
	if got := h.store.row(t, 2).State; got != fsp.DeliveryAccepted {
		t.Errorf("merged row 2 state = %q, want accepted", got)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 2 {
		t.Errorf("window used = %d, want 2", got)
	}
}

func TestHeldWhenExhausted(t *testing.T) {
	h := newRunning(t, func(c *config.OutboxConfig) { c.ReplyBudget = 2 })
	h.open("ctx-1")

	h.enqueue(sessionA, "one", true)
	h.waitSent(1)
	h.enqueue(sessionA, "two", true)
	h.waitSent(2)

	// The window is spent; the next urgent message is held, not retried.
	h.enqueue(sessionA, "three", true)
	h.waitFor("row 3 to be held", func() bool { return h.store.row(t, 3).State == fsp.DeliveryHeld })
	h.quiesce()
	if n := h.im.count(); n != 2 {
		t.Fatalf("sent %d, want 2: %q", n, h.sentTexts())
	}

	// A non-urgent message stays queued instead: it waits for the next urgent
	// message to merge with.
	h.enqueue(sessionA, "chunk", false)
	h.clk.Advance(30 * time.Second)
	h.quiesce()
	if got := h.store.row(t, 4).State; got != fsp.DeliveryQueued {
		t.Errorf("row 4 state = %q, want queued", got)
	}

	// A fresh window lets the queue drain again.
	h.open("ctx-2")
	h.clk.Advance(30 * time.Second)
	h.waitSent(3)
	if got, want := h.im.messages()[2].Text, "chunk"; got != want {
		t.Errorf("text after reopening = %q, want %q", got, want)
	}
}

func TestSplitLongText(t *testing.T) {
	t.Run("units", func(t *testing.T) {
		tests := []struct {
			name  string
			text  string
			limit int
			want  []string
		}{
			{"short", "hello", 10, []string{"hello"}},
			{"paragraphs", "aaaa\n\nbbbb\n\ncccc", 10, []string{"aaaa\n\nbbbb", "cccc"}},
			{"lines", "aaaa\nbbbb\ncccc", 5, []string{"aaaa", "bbbb", "cccc"}},
			{"mid line", "aaaaaaaaaa", 4, []string{"aaaa", "aaaa", "aa"}},
			{"no limit", "aaaaaaaaaa", 0, []string{"aaaaaaaaaa"}},
			{"runes not bytes", "中文中文中文", 3, []string{"中文中", "文中文"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := splitText(tt.text, tt.limit)
				if len(got) != len(tt.want) {
					t.Fatalf("splitText = %q, want %q", got, tt.want)
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Fatalf("splitText = %q, want %q", got, tt.want)
					}
					if tt.limit > 0 && utf8.RuneCountInString(got[i]) > tt.limit {
						t.Fatalf("part %d of %q is longer than %d runes", i, got, tt.limit)
					}
				}
			})
		}
	})

	t.Run("sent in parts", func(t *testing.T) {
		h := newRunning(t, func(c *config.OutboxConfig) { c.LongOutputThreshold = 20 })
		h.open("ctx-1")
		h.enqueue(sessionA, "paragraph one here\n\nparagraph two here\n\nparagraph three", true)
		h.waitSent(3)
		h.quiesce()
		msgs := h.im.messages()
		if len(msgs) != 3 {
			t.Fatalf("sent %d parts, want 3: %q", len(msgs), h.sentTexts())
		}
		for i, m := range msgs {
			if utf8.RuneCountInString(m.Text) > 20 {
				t.Errorf("part %d is %d runes, want at most 20", i, utf8.RuneCountInString(m.Text))
			}
			if want := fmt.Sprintf("ob-1-%d", i+1); m.DedupeKey != want {
				t.Errorf("part %d key = %q, want %q", i, m.DedupeKey, want)
			}
		}
		if got := h.store.window(t, testEndpoint).Used; got != 3 {
			t.Errorf("window used = %d, want 3, one per part", got)
		}
	})

	t.Run("truncated when parts exceed the budget", func(t *testing.T) {
		const limit = 40
		h := newRunning(t, func(c *config.OutboxConfig) {
			c.LongOutputThreshold = limit
			c.ReplyBudget = 2
		})
		// Four paragraphs of 35 runes: four parts, but only two replies left.
		paras := []string{
			"alpha " + strings.Repeat("A", 29),
			"bravo " + strings.Repeat("B", 29),
			"charlie " + strings.Repeat("C", 27),
			"delta " + strings.Repeat("D", 29),
		}
		h.open("ctx-1")
		h.enqueue(sessionA, strings.Join(paras, "\n\n"), true)
		h.waitSent(2)
		h.quiesce()
		msgs := h.im.messages()
		if len(msgs) != 2 {
			t.Fatalf("sent %d parts, want 2 (the budget): %q", len(msgs), h.sentTexts())
		}
		if msgs[0].Text != paras[0] {
			t.Errorf("first part = %q, want %q", msgs[0].Text, paras[0])
		}
		last := msgs[1].Text
		if !strings.Contains(last, "outbox.truncated(N=") {
			t.Errorf("last part = %q, want it to carry outbox.truncated", last)
		}
		if n := utf8.RuneCountInString(last); n > limit {
			t.Errorf("last part is %d runes, want at most %d", n, limit)
		}
		if !strings.HasPrefix(last, "bravo ") {
			t.Errorf("last part = %q, want it to start with the text it truncates", last)
		}
	})
}

func TestRetryUnknownSameKey(t *testing.T) {
	h := newRunning(t, nil)
	h.im.script(
		result{state: fsp.DeliveryUnknown, err: errors.New("timeout")},
		result{state: fsp.DeliveryUnknown, err: errors.New("timeout")},
	)
	h.open("ctx-1")
	h.enqueue(sessionA, "result", true)

	// Two retries, 2 seconds then 4 seconds apart (spec 3.10).
	h.waitSent(1)
	h.clk.BlockUntil(2)
	h.clk.Advance(2 * time.Second)
	h.waitSent(2)
	h.clk.BlockUntil(2)
	h.clk.Advance(4 * time.Second)
	h.waitSent(3)
	h.quiesce()

	msgs := h.im.messages()
	if len(msgs) != 3 {
		t.Fatalf("sent %d times, want 3: %q", len(msgs), h.sentTexts())
	}
	for i, m := range msgs {
		if m.DedupeKey != "ob-1-1" {
			t.Errorf("attempt %d key = %q, want the same ob-1-1 every time", i, m.DedupeKey)
		}
		if m.Text != "result" {
			t.Errorf("attempt %d text = %q, want %q", i, m.Text, "result")
		}
	}
	row := h.store.row(t, 1)
	if row.State != fsp.DeliveryAccepted {
		t.Errorf("row state = %q, want accepted", row.State)
	}
	if row.Attempts != 3 {
		t.Errorf("row attempts = %d, want 3", row.Attempts)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Errorf("window used = %d, want 1: the retries are one reply", got)
	}
}

func TestNoWindowRejected(t *testing.T) {
	h := newRunning(t, nil)
	// No OpenWindow: the endpoint never replied to anything.
	h.enqueue(sessionA, "unsolicited", true)

	h.waitFor("row 1 to be rejected", func() bool {
		return h.store.row(t, 1).State == fsp.DeliveryRejected
	})
	h.quiesce()
	if n := h.im.count(); n != 0 {
		t.Errorf("sent %d messages without a window, want 0: %q", n, h.sentTexts())
	}
	audits := h.store.auditKinds()
	if len(audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(audits))
	}
	if audits[0].Kind != "outbound_failed" {
		t.Errorf("audit kind = %q, want outbound_failed", audits[0].Kind)
	}
	if audits[0].Endpoint != testEndpoint || audits[0].Session != sessionA {
		t.Errorf("audit = %+v, want the endpoint and session filled in", audits[0])
	}
	if strings.Contains(audits[0].Detail, "unsolicited") {
		t.Errorf("audit detail %q leaks the message body", audits[0].Detail)
	}
}

func TestRowsPersisted(t *testing.T) {
	// No send loop: this is about what Enqueue and one pass write to the store.
	h := newBox(t, nil)

	h.enqueue(sessionA, "first", false)
	h.enqueue(sessionA, "second", true)

	for id, want := range map[int64]string{1: "first", 2: "second"} {
		row := h.store.row(t, id)
		if row.State != fsp.DeliveryQueued {
			t.Errorf("row %d state = %q, want queued", id, row.State)
		}
		if row.Msg.Text != want {
			t.Errorf("row %d text = %q, want %q", id, row.Msg.Text, want)
		}
		if !row.CreatedAt.Equal(h.start) {
			t.Errorf("row %d created at %s, want the clock's %s", id, row.CreatedAt, h.start)
		}
		if row.Attempts != 0 {
			t.Errorf("row %d attempts = %d, want 0", id, row.Attempts)
		}
	}

	h.open("ctx-1")
	h.box.pump(h.ctx)

	// The urgent message goes out and takes the queued non-urgent one with it,
	// so both rows end up accepted after a single reply.
	for _, id := range []int64{1, 2} {
		row := h.store.row(t, id)
		if row.State != fsp.DeliveryAccepted {
			t.Errorf("row %d state = %q, want accepted", id, row.State)
		}
		if row.Attempts != 1 {
			t.Errorf("row %d attempts = %d, want 1", id, row.Attempts)
		}
	}
	if n := h.im.count(); n != 1 {
		t.Fatalf("sent %d messages, want 1: %q", n, h.sentTexts())
	}
	w := h.store.window(t, testEndpoint)
	if w.Used != 1 || w.ReplyCtx != "ctx-1" || !w.OpenedAt.Equal(h.start) {
		t.Errorf("window = %+v, want used 1 at %s with ctx-1", w, h.start)
	}

	// Nothing is left queued.
	rows, err := h.store.OutboxByState(h.ctx, testEndpoint, fsp.DeliveryQueued)
	if err != nil {
		t.Fatalf("OutboxByState: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("queued rows = %d, want 0", len(rows))
	}
}

func TestFlushHeldIsNoOp(t *testing.T) {
	h := newBox(t, nil)
	if err := h.box.FlushHeld(h.ctx, testEndpoint); err != nil {
		t.Errorf("FlushHeld: %v", err)
	}
}

func TestOpenWindowResetsBudget(t *testing.T) {
	h := newBox(t, nil)
	h.open("ctx-1")
	h.enqueue(sessionA, "one", true)
	h.box.pump(h.ctx)
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Fatalf("window used = %d, want 1", got)
	}

	h.clk.Advance(time.Minute)
	h.open("ctx-2")
	w := h.store.window(t, testEndpoint)
	if w.Used != 0 {
		t.Errorf("window used = %d after reopening, want 0", w.Used)
	}
	if w.ReplyCtx != "ctx-2" {
		t.Errorf("reply context = %q, want ctx-2", w.ReplyCtx)
	}
	if !w.OpenedAt.Equal(h.clk.Now()) {
		t.Errorf("opened at %s, want %s", w.OpenedAt, h.clk.Now())
	}
}

func TestRejectedNotRetried(t *testing.T) {
	h := newBox(t, nil)
	h.im.script(result{state: fsp.DeliveryRejected, err: errors.New("code -3")})
	h.open("ctx-1")
	h.enqueue(sessionA, "one", true)
	h.box.pump(h.ctx)

	if n := h.im.count(); n != 1 {
		t.Errorf("sent %d times, want 1: a rejected message is not retried", n)
	}
	if got := h.store.row(t, 1).State; got != fsp.DeliveryRejected {
		t.Errorf("row state = %q, want rejected", got)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 0 {
		t.Errorf("window used = %d, want 0: a rejected message spends no reply", got)
	}
	audits := h.store.auditKinds()
	if len(audits) != 1 || audits[0].Kind != "outbound_failed" {
		t.Errorf("audits = %+v, want one outbound_failed", audits)
	}
}
