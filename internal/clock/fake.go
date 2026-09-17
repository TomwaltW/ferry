package clock

import (
	"sync"
	"time"
)

// Fake is a Clock whose time only moves when Advance or Set is called. Moving
// it forward fires every timer and ticker that becomes due, in chronological
// order. Fake is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	deadline time.Time
	period   time.Duration // zero for a one-shot timer
	ch       chan time.Time
}

// NewFake returns a Fake positioned at start.
func NewFake(start time.Time) *Fake {
	f := &Fake{now: start}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that receives once the clock has been moved d beyond
// the current time. A non-positive d fires immediately.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, &waiter{deadline: f.now.Add(d), ch: ch})
	f.cond.Broadcast()
	return ch
}

// NewTicker returns a Ticker that fires every d of fake time. Like
// time.NewTicker it panics when d is not positive.
func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{deadline: f.now.Add(d), period: d, ch: make(chan time.Time, 1)}
	f.waiters = append(f.waiters, w)
	f.cond.Broadcast()
	return &fakeTicker{clock: f, w: w}
}

// Advance moves the clock forward by d. It is Set(Now().Add(d)).
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	f.Set(target)
}

// Set moves the clock to t. Every timer due at or before t fires, each
// observing the instant it was scheduled for. Moving the clock backwards only
// changes Now; nothing fires.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		w := f.dueLocked(t)
		if w == nil {
			break
		}
		f.now = w.deadline
		select {
		case w.ch <- w.deadline:
		default: // a tick nobody consumed, dropped like time.Ticker does
		}
		if w.period > 0 {
			w.deadline = w.deadline.Add(w.period)
			continue
		}
		f.removeLocked(w)
	}
	f.now = t
}

// BlockUntil blocks until at least n timers or tickers are waiting on the
// clock. Tests use it to synchronise with code that is about to wait.
func (f *Fake) BlockUntil(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.waiters) < n {
		f.cond.Wait()
	}
}

// dueLocked returns the earliest waiter due at or before limit, or nil.
func (f *Fake) dueLocked(limit time.Time) *waiter {
	var due *waiter
	for _, w := range f.waiters {
		if w.deadline.After(limit) {
			continue
		}
		if due == nil || w.deadline.Before(due.deadline) {
			due = w
		}
	}
	return due
}

func (f *Fake) removeLocked(target *waiter) {
	for i, w := range f.waiters {
		if w == target {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			return
		}
	}
}

type fakeTicker struct {
	clock *Fake
	w     *waiter
}

func (t *fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t *fakeTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.clock.removeLocked(t.w)
}

var _ Clock = (*Fake)(nil)
