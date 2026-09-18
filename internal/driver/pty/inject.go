package pty

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
)

// pollFloor keeps the gate from spinning when it is only just not ready yet.
const pollFloor = 10 * time.Millisecond

// gate decides when a prompt from chat may be typed into the wrapped program.
// Three things have to be true, per spec-p3 3.8: the program has been quiet for
// idle, the user has not touched the keyboard for TypingGrace, and — when a
// pattern was given — the line the program is sitting on matches it.
type gate struct {
	clk   clock.Clock
	idle  time.Duration
	grace time.Duration
	re    *regexp.Regexp

	mu         sync.Mutex
	lastOutput time.Time
	lastKey    time.Time
	line       string
}

func newGate(clk clock.Clock, idle time.Duration, re *regexp.Regexp) *gate {
	if idle <= 0 {
		idle = DefaultIdle
	}
	return &gate{clk: clk, idle: idle, grace: TypingGrace, re: re, lastOutput: clk.Now()}
}

// sawOutput records what the program just wrote, which both restarts the idle
// countdown and moves the line the prompt pattern is matched against.
func (g *gate) sawOutput(chunk []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastOutput = g.clk.Now()
	g.line = lastLine(g.line, chunk)
}

// sawInjection marks the moment a prompt was typed in. Without it, collecting
// the answer would see the quiet that came *before* the injection and stop
// before the program had a chance to say anything.
func (g *gate) sawInjection() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastOutput = g.clk.Now()
}

// sawKeys records that the user is typing, which holds an injection back.
func (g *gate) sawKeys() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastKey = g.clk.Now()
}

// ready reports whether a prompt may go in now, and if not, how long the
// soonest condition still has to run. full is false while waiting for the output
// of an injection to settle: by then the user and the prompt pattern have
// already had their say.
func (g *gate) ready(full bool) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.clk.Now()
	wait := time.Duration(0)

	if quiet := now.Sub(g.lastOutput); quiet < g.idle {
		wait = g.idle - quiet
	}
	if !full {
		return wait == 0, atLeast(wait)
	}

	if !g.lastKey.IsZero() {
		if since := now.Sub(g.lastKey); since < g.grace {
			wait = max(wait, g.grace-since)
		}
	}
	if wait > 0 {
		return false, atLeast(wait)
	}

	// The pattern is the one condition no amount of waiting fixes on its own:
	// the program has to write the prompt again first, so the gate looks in
	// again after one more idle period.
	if g.re != nil && !g.re.MatchString(g.line) {
		return false, g.idle
	}
	return true, 0
}

// wait blocks until a prompt may go in, or until ctx ends.
func (g *gate) wait(ctx context.Context, full bool) error {
	for {
		ok, after := g.ready(full)
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-g.clk.After(after):
		}
	}
}

func atLeast(d time.Duration) time.Duration {
	if d < pollFloor {
		return pollFloor
	}
	return d
}

// pasteBytes wraps a prompt the way a paste arrives from a terminal: inside the
// bracketed paste markers, so a program that reads them knows this was not typed,
// and followed by the carriage return that submits it.
func pasteBytes(text string) []byte {
	out := make([]byte, 0, len(PasteStart)+len(text)+len(PasteEnd)+1)
	out = append(out, PasteStart...)
	out = append(out, text...)
	out = append(out, PasteEnd...)
	return append(out, '\r')
}
