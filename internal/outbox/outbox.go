package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// selfCheck is how often the send loop looks at the queue on its own. Enqueue
// and OpenWindow wake it immediately; the tick only has to notice a merge
// interval that has run out while nothing else happened.
const selfCheck = time.Second

// retryDelays are the waits between attempts for a send whose outcome is
// unknown (spec 3.10): three retries reusing the same dedupe key.
var retryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// The wake-up kinds an undeliverable urgent message raises (spec-p2 3.1).
const (
	wakeApproval = "approval_pending"
	wakeFinished = "turn_finished"
)

// Reasons a message cannot go out now. They reach the audit trail, never the
// user, and never carry a message body.
const (
	reasonNoWindow    = "no reply window"
	reasonNoReplyCtx  = "reply window without a reply context"
	reasonWindowStale = "reply window older than window_ttl"
	reasonBudget      = "reply budget exhausted"
)

type Options struct {
	Config config.OutboxConfig
	IM     ports.IMAdapter
	Store  ports.Store
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
	// Waker raises an out-of-band reminder when an urgent message cannot be
	// delivered. Optional: without one, a held message simply waits.
	Waker ports.Waker
	// Home is the data directory; long outputs are written under <Home>/outbox.
	// Without it a long turn is split into messages instead.
	Home string
}

// box is the outbox: one queue in the store, one send loop, per-endpoint reply
// windows, and the P2 backlog of everything that arrived while the window was
// shut.
type box struct {
	cfg   config.OutboxConfig
	im    ports.IMAdapter
	store ports.Store
	i18n  ports.I18n
	lang  func(fsp.EndpointID) fsp.Lang
	clk   clock.Clock
	log   *slog.Logger
	waker ports.Waker
	home  string
	ttl   time.Duration

	wake chan struct{}
	// passes counts completed queue passes. Tests wait on it instead of
	// sleeping; production code only ever increments it.
	passes atomic.Int64
	// seq numbers the messages the outbox composes itself, so that each one
	// carries a dedupe key of its own.
	seq atomic.Int64
}

func New(opts Options) ports.Outbox {
	b := &box{
		cfg:   opts.Config,
		im:    opts.IM,
		store: opts.Store,
		i18n:  opts.I18n,
		lang:  opts.Lang,
		clk:   opts.Clock,
		log:   opts.Log,
		waker: opts.Waker,
		home:  opts.Home,
		ttl:   opts.Config.WindowTTL.Duration,
		wake:  make(chan struct{}, 1),
	}
	if b.clk == nil {
		b.clk = clock.Real()
	}
	if b.log == nil {
		b.log = slog.New(slog.DiscardHandler)
	}
	if b.lang == nil {
		b.lang = func(fsp.EndpointID) fsp.Lang { return fsp.LangZH }
	}
	if b.ttl < 1 {
		// A caller that never set window_ttl still gets the documented
		// lifetime rather than a window that is stale the moment it opens.
		b.ttl = config.DefaultWindowTTL
	}
	return b
}

// OpenWindow resets the endpoint's reply budget. The core calls it after every
// inbound message it lets through.
func (b *box) OpenWindow(ep fsp.EndpointID, replyCtx string, inboundAt time.Time) {
	w := ports.Window{Endpoint: ep, ReplyCtx: replyCtx, OpenedAt: inboundAt}
	if err := b.store.SaveWindow(context.Background(), w); err != nil {
		b.log.Error("outbox: save window", "endpoint", ep.String(), "err", err)
		return
	}
	b.signal()
}

func (b *box) Enqueue(ctx context.Context, msg fsp.Outbound) error {
	if _, err := b.store.AddOutbox(ctx, msg, b.clk.Now()); err != nil {
		return fmt.Errorf("outbox: enqueue: %w", err)
	}
	b.signal()
	return nil
}

// Run merges and sends until ctx ends.
func (b *box) Run(ctx context.Context) error {
	ticker := b.clk.NewTicker(selfCheck)
	defer ticker.Stop()
	for {
		b.pump(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-b.wake:
		case <-ticker.C():
		}
	}
}

func (b *box) signal() {
	select {
	case b.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// window returns the endpoint's reply window and whether anything can be sent
// through it. A window with no reply context, or one older than window_ttl, is
// no window at all (spec-p2 3.6).
func (b *box) window(ctx context.Context, ep fsp.EndpointID) (ports.Window, string, bool, error) {
	win, err := b.store.LoadWindow(ctx, ep)
	switch {
	case errors.Is(err, fsp.ErrNotFound):
		return ports.Window{}, reasonNoWindow, false, nil
	case err != nil:
		return ports.Window{}, "", false, fmt.Errorf("outbox: load window: %w", err)
	case win.ReplyCtx == "":
		return win, reasonNoReplyCtx, false, nil
	}
	if age := b.clk.Now().Sub(win.OpenedAt); age.Nanoseconds() >= b.ttl.Nanoseconds() {
		return win, reasonWindowStale, false, nil
	}
	return win, "", true, nil
}

// budget is how many replies the window has left.
func (b *box) budget(win ports.Window) int { return b.cfg.ReplyBudget - win.Used }

func (b *box) audit(ctx context.Context, ep fsp.EndpointID, session fsp.SessionID, detail string) {
	e := ports.AuditEntry{
		At:       b.clk.Now(),
		Kind:     "outbound_failed",
		Endpoint: ep,
		Session:  session,
		Detail:   detail,
	}
	if err := b.store.Audit(ctx, e); err != nil {
		b.log.Error("outbox: audit", "endpoint", ep.String(), "err", err)
	}
}

// t renders a catalogue key for the endpoint's language.
func (b *box) t(ep fsp.EndpointID, key string, args map[string]any) string {
	if b.i18n == nil {
		return key
	}
	return b.i18n.T(b.lang(ep), key, args)
}

var _ ports.Outbox = (*box)(nil)
