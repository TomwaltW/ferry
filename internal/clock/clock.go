// Package clock is the single source of time for Ferry. Production code takes
// a Clock so that tests can drive timeouts with Fake instead of sleeping.
package clock

import "time"

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) Ticker
}

// Real returns a Clock backed by the time package.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (realClock) NewTicker(d time.Duration) Ticker { return realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }

func (r realTicker) Stop() { r.t.Stop() }

var _ Clock = realClock{}
