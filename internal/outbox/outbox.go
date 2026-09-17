package outbox

import (
	"context"
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

type Options struct {
	Config config.OutboxConfig
	IM     ports.IMAdapter
	Store  ports.Store
	I18n   ports.I18n
	Lang   func(fsp.EndpointID) fsp.Lang
	Clock  clock.Clock
	Log    *slog.Logger
}

// box is the P1 outbox: one queue in the store, one send loop, per-endpoint
// reply windows.
type box struct {
	cfg   config.OutboxConfig
	im    ports.IMAdapter
	store ports.Store
	i18n  ports.I18n
	lang  func(fsp.EndpointID) fsp.Lang
	clk   clock.Clock
	log   *slog.Logger

	wake chan struct{}
	// passes counts completed queue passes. Tests wait on it instead of
	// sleeping; production code only ever increments it.
	passes atomic.Int64
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

// FlushHeld is a no-op in P1: held messages wait for the redelivery path that
// P2 adds.
func (b *box) FlushHeld(context.Context, fsp.EndpointID) error { return nil }

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

var _ ports.Outbox = (*box)(nil)
