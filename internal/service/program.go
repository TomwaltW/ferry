package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	kservice "github.com/kardianos/service"

	"github.com/TomwaltW/ferry/internal/clock"
)

// stopWait bounds how long Stop waits for the daemon to wind down; the
// Windows service manager gives a service about 20 seconds.
const stopWait = 15 * time.Second

// Program is what the service manager hosts: Run in the background from
// Start until Stop.
type Program struct {
	// Run is the daemon; it returns once ctx is cancelled.
	Run func(ctx context.Context) error
	// Exit is called when Run returns before Stop asked it to, with Run's
	// error. The service manager cannot be told from inside Run, so the
	// usual choice is to exit the process and let the manager restart it.
	Exit func(err error)
	// Clock times the wait in Stop; nil is the real clock.
	Clock clock.Clock

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopping atomic.Bool
}

// Start launches Run and returns at once, as the service manager expects.
func (p *Program) Start(kservice.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.mu.Lock()
	p.cancel, p.done = cancel, done
	p.mu.Unlock()
	go func() {
		err := p.Run(ctx)
		close(done)
		if !p.stopping.Load() && p.Exit != nil {
			p.Exit(err)
		}
	}()
	return nil
}

// Stop cancels Run and waits for it to return.
func (p *Program) Stop(kservice.Service) error {
	p.stopping.Store(true)
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	clk := p.Clock
	if clk == nil {
		clk = clock.Real()
	}
	select {
	case <-done:
	case <-clk.After(stopWait):
	}
	return nil
}
