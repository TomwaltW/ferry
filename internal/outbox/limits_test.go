package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// platformIM is a fakeIM whose platform and limits the test chooses, so that
// one box can serve two entry points that allow different things.
type platformIM struct {
	platform fsp.Platform
	limits   ports.DeliveryLimits

	mu   sync.Mutex
	sent []fsp.Outbound
	ctxs []string
}

func (p *platformIM) Platform() fsp.Platform { return p.platform }

func (p *platformIM) Limits() ports.DeliveryLimits { return p.limits }

func (p *platformIM) Run(ctx context.Context, _ chan<- fsp.Inbound) error {
	<-ctx.Done()
	return nil
}

func (p *platformIM) Send(_ context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, msg)
	p.ctxs = append(p.ctxs, replyCtx)
	return fsp.DeliveryAccepted, nil
}

func (p *platformIM) Typing(context.Context, fsp.EndpointID, string, bool) error { return nil }

func (p *platformIM) Status() ports.AdapterStatus {
	return ports.AdapterStatus{Online: true, BotID: "bot", OwnerID: "owner"}
}

func (p *platformIM) texts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.sent))
	for _, m := range p.sent {
		out = append(out, m.Text)
	}
	return out
}

func (p *platformIM) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

var _ ports.IMAdapter = (*platformIM)(nil)

// multi is a running box serving several entry points at once.
type multi struct {
	t     *testing.T
	box   *box
	store *memStore
	clk   *clock.Fake
	ctx   context.Context
}

// newMulti starts a send loop over ims and blocks until its self-check ticker
// exists, so that a later Advance cannot be missed.
func newMulti(t *testing.T, ims ...*platformIM) *multi {
	t.Helper()
	byPlatform := make(map[fsp.Platform]ports.IMAdapter, len(ims))
	for _, im := range ims {
		byPlatform[im.platform] = im
	}
	cfg := config.OutboxConfig{
		MergeInterval:       config.Duration{Duration: 30 * time.Second},
		ReplyBudget:         8,
		LongOutputThreshold: 1500,
		SendTimeout:         config.Duration{Duration: 15 * time.Second},
		WindowTTL:           config.Duration{Duration: config.DefaultWindowTTL},
		DigestMax:           5,
		LongOutput:          config.LongOutputSplit,
	}
	m := &multi{
		t:     t,
		store: newMemStore(),
		clk:   clock.NewFake(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)),
	}
	m.box = New(Options{
		Config: cfg, IMs: byPlatform, Store: m.store, I18n: fakeI18n{},
		Clock: m.clk, Log: slog.New(slog.DiscardHandler),
	}).(*box)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := m.box.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	m.clk.BlockUntil(1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return m
}

func (m *multi) enqueue(ep fsp.EndpointID, text string) {
	m.t.Helper()
	msg := fsp.Outbound{Endpoint: ep, Session: sessionA, Kind: fsp.OutText, Text: text, Urgent: true}
	if err := m.box.Enqueue(m.ctx, msg); err != nil {
		m.t.Fatalf("Enqueue(%q): %v", text, err)
	}
}

func (m *multi) waitFor(what string, cond func() bool) {
	m.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			m.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// quiesce waits for two full queue passes, the second of which started after
// this call, so that "nothing was sent" means the loop looked and declined.
func (m *multi) quiesce() {
	m.t.Helper()
	start := m.box.passes.Load()
	m.waitFor("a full queue pass", func() bool {
		m.box.signal()
		return m.box.passes.Load() >= start+2
	})
}

func (m *multi) heldCount(ep fsp.EndpointID) int {
	rows, err := m.store.OutboxByState(m.ctx, ep, fsp.DeliveryHeld)
	if err != nil {
		m.t.Fatalf("list held: %v", err)
	}
	return len(rows)
}

func feishuIM() *platformIM {
	return &platformIM{
		platform: fsp.PlatformFeishu,
		limits:   ports.DeliveryLimits{NeedsWindow: false, RatePerSec: 5},
	}
}

func wechatIM() *platformIM {
	return &platformIM{
		platform: fsp.PlatformWeChat,
		limits:   ports.DeliveryLimits{NeedsWindow: true},
	}
}

var (
	feishuEP = fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: "cli_app", PeerID: "ou_bob"}
	wechatEP = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot", PeerID: "peer"}
)

// TestFeishuNoWindow: an entry point that needs no reply window delivers
// without one ever having been opened.
func TestFeishuNoWindow(t *testing.T) {
	im := feishuIM()
	m := newMulti(t, im)

	m.enqueue(feishuEP, "the turn finished")
	m.waitFor("the message to go out", func() bool { return im.count() == 1 })

	if got := im.texts(); got[0] != "the turn finished" {
		t.Errorf("sent %q, want the queued message", got[0])
	}
	if n := m.heldCount(feishuEP); n != 0 {
		t.Errorf("%d messages held, want none on a platform with no window", n)
	}
	if _, err := m.store.LoadWindow(m.ctx, feishuEP); err == nil {
		t.Error("the outbox invented a reply window for an entry point that has none")
	}

	t.Run("a budget it cannot spend never runs out", func(t *testing.T) {
		// A separate box without a rate cap, so this is about the budget
		// alone: more messages than config.ReplyBudget, which binds WeChat and
		// not Feishu.
		free := feishuIM()
		free.limits.RatePerSec = 0
		m := newMulti(t, free)
		for i := range 12 {
			m.enqueue(feishuEP, fmt.Sprintf("line %d", i))
		}
		m.waitFor("every message to go out", func() bool { return free.count() == 12 })
		if n := m.heldCount(feishuEP); n != 0 {
			t.Errorf("%d messages held, want none", n)
		}
	})
}

// TestPerPlatformLimits: one outbox, two entry points, each judged by its own
// adapter's Limits (spec-p3 3.1).
func TestPerPlatformLimits(t *testing.T) {
	feishu, wechat := feishuIM(), wechatIM()
	m := newMulti(t, feishu, wechat)

	m.enqueue(feishuEP, "for feishu")
	m.enqueue(wechatEP, "for wechat")

	m.waitFor("the Feishu message to go out", func() bool { return feishu.count() == 1 })
	m.quiesce()

	if n := wechat.count(); n != 0 {
		t.Errorf("WeChat sent %d messages with no window open, want 0", n)
	}
	if n := m.heldCount(wechatEP); n != 1 {
		t.Errorf("%d WeChat messages held, want 1", n)
	}
	if n := m.heldCount(feishuEP); n != 0 {
		t.Errorf("%d Feishu messages held, want 0", n)
	}

	t.Run("the WeChat window lets its own backlog through", func(t *testing.T) {
		m.box.OpenWindow(wechatEP, "ctx-1", m.clk.Now())
		if err := m.box.FlushHeld(m.ctx, wechatEP); err != nil {
			t.Fatalf("FlushHeld: %v", err)
		}
		m.waitFor("the WeChat backlog to go out", func() bool { return wechat.count() >= 2 })
		if got := wechat.texts(); got[len(got)-1] != "for wechat" {
			t.Errorf("WeChat sent %q last, want the held message after the digest", got[len(got)-1])
		}
	})

	t.Run("the send rate is the platform's own", func(t *testing.T) {
		// 5 per second is one send every 200ms, and the loop waits for it: the
		// clock has to move before either of these leaves.
		const gap = 200 * time.Millisecond
		before := feishu.count()

		m.enqueue(feishuEP, "first")
		m.clk.BlockUntil(2) // the self-check ticker, plus the wait for the rate
		if n := feishu.count(); n != before {
			t.Fatalf("%d messages went out before the rate allowed any", n-before)
		}
		m.clk.Advance(gap)
		m.waitFor("the first of the pair", func() bool { return feishu.count() == before+1 })

		m.enqueue(feishuEP, "second")
		m.clk.BlockUntil(2)
		m.clk.Advance(gap)
		m.waitFor("the second of the pair", func() bool { return feishu.count() == before+2 })
		if got := feishu.texts(); got[len(got)-1] != "second" {
			t.Errorf("the last message was %q, want %q", got[len(got)-1], "second")
		}
	})
}
