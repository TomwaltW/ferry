package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// unlimited stands for "as many replies as it takes" on a platform that caps
// nothing. It is a number rather than a flag so that the budget arithmetic of
// spec-p1 5.5 — reserve one, spend one — reads the same everywhere.
const unlimited = 1 << 20

// limits is what one platform allows, with the outbox configuration filling in
// whatever the adapter left at zero (spec-p3 3.1).
type limits struct {
	needsWindow bool
	ttl         time.Duration
	// budget is how many replies one window carries; unlimited when the
	// platform caps nothing.
	budget     int
	ratePerSec float64
}

// limitsFor reads the endpoint's platform. An endpoint nobody serves gets the
// WeChat-shaped defaults: the send itself then fails with fsp.ErrNotFound, and
// erring towards holding the message is the safer of the two mistakes.
func (b *box) limitsFor(ep fsp.EndpointID) limits {
	lim := limits{needsWindow: true, ttl: b.ttl, budget: b.cfg.ReplyBudget}
	im, ok := b.ims[ep.Platform]
	if !ok {
		return lim
	}
	dl := im.Limits()
	lim.needsWindow = dl.NeedsWindow
	lim.ratePerSec = dl.RatePerSec
	if dl.WindowTTL > 0 {
		lim.ttl = dl.WindowTTL
	}
	switch {
	case dl.ReplyBudget > 0:
		lim.budget = dl.ReplyBudget
	case dl.NeedsWindow:
		lim.budget = b.cfg.ReplyBudget
	default:
		lim.budget = unlimited
	}
	if lim.budget < 1 {
		lim.budget = unlimited
	}
	return lim
}

// remaining is how many replies the window has left.
func (l limits) remaining(win ports.Window) int {
	if l.budget >= unlimited {
		return unlimited
	}
	return l.budget - win.Used
}

// minInterval is the shortest gap between two sends on this platform.
func (l limits) minInterval() time.Duration {
	if l.ratePerSec <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / l.ratePerSec)
}

// rateGate spaces out the sends of one platform. The wait happens in the send
// loop, which is where every other delay of the outbox already lives.
type rateGate struct {
	mu   sync.Mutex
	next map[fsp.Platform]time.Time
}

func newRateGate() *rateGate { return &rateGate{next: make(map[fsp.Platform]time.Time)} }

// await blocks until the platform may be written to again, then books this
// send against it.
func (b *box) await(ctx context.Context, platform fsp.Platform, lim limits) error {
	interval := lim.minInterval()
	if interval <= 0 {
		return nil
	}
	for {
		b.rate.mu.Lock()
		now := b.clk.Now()
		next := b.rate.next[platform]
		if !now.Before(next) {
			b.rate.next[platform] = now.Add(interval)
			b.rate.mu.Unlock()
			return nil
		}
		wait := next.Sub(now)
		b.rate.mu.Unlock()

		select {
		case <-b.clk.After(wait):
		case <-ctx.Done():
			return fmt.Errorf("outbox: wait for the %s send rate: %w", platform, ctx.Err())
		}
	}
}
