package core

import (
	"context"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/security"
)

// gatherSpan caps a gather: every new piece restarts the wait, but no turn
// waits longer than this many times input.gather after its first piece.
const gatherSpan = 4

// heardBudget is how much of a transcription the receipt echoes.
const heardBudget = 200

// fragment is one inbound message on its way into a prompt.
type fragment struct {
	ep     fsp.EndpointID
	prompt string
	heard  string // WeChat's transcription, when the message was voice
	// wordless is a picture or file sent with no text. WeChat has no captions,
	// so the question usually follows as a message of its own, and it takes
	// longer to type than the next piece of a sentence.
	wordless bool
}

// gatherTick is a gather timer that ran out: the turn it waited for may start,
// unless a newer piece, /stop, /close or a park has made gen stale.
type gatherTick struct {
	s   *session
	gen uint64
}

// gather holds frag back while the session is idle and input.gather is on,
// and reports whether it did. Chat users often split one request over several
// messages; starting a turn on the first piece would have the agent guess at
// "帮我改一下" and spend replies on the guess.
//
// The timer is armed here, on the Run loop, before the message is done with,
// so a test with a fake clock can move time on as soon as it sees the message
// handled. Its goroutine only reports back to the loop; fireGather starts the
// turn.
func (d *Daemon) gather(ctx context.Context, s *session, frag fragment) bool {
	wait := d.deps.Config.Input.Gather.Duration
	if wait <= 0 {
		return false
	}
	now := d.deps.Clock.Now()
	s.mu.Lock()
	if s.as == nil || s.busyLocked() {
		s.mu.Unlock()
		return false
	}
	first := len(s.gather) == 0
	if first {
		s.gatherFirst = now
	}
	s.gather = append(s.gather, frag)
	s.gatherGen++
	tick := gatherTick{s: s, gen: s.gatherGen}
	if frag.wordless {
		wait *= gatherSpan
	}
	delay := min(wait, s.gatherFirst.Add(gatherSpan*d.deps.Config.Input.Gather.Duration).Sub(now))
	s.mu.Unlock()

	if first {
		// The typing indicator is not a reply, so the wait costs nothing and
		// does not look like a message gone missing.
		d.typing(ctx, frag.ep, true)
	}

	fired := d.deps.Clock.After(delay)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		select {
		case <-fired:
		case <-ctx.Done():
			return
		}
		select {
		case d.gatherFired <- tick:
		case <-ctx.Done():
		}
	}()
	return true
}

// fireGather starts the turn a gather was waiting for, unless its timer has
// gone stale.
func (d *Daemon) fireGather(ctx context.Context, t gatherTick) {
	s := t.s
	s.mu.Lock()
	if t.gen != s.gatherGen || len(s.gather) == 0 {
		s.mu.Unlock()
		return
	}
	frags := s.gather
	s.gather = nil
	s.mu.Unlock()
	if !d.startTurn(ctx, s, frags, true) {
		d.typing(ctx, frags[len(frags)-1].ep, false)
	}
}

// startTurn prompts the session with frags as one message, or queues them when
// it is busy, and answers a turn that started with the receipt. The receipt of
// a spoken message says what WeChat heard, so a misheard name can be stopped
// before the agent acts on it; it costs no reply the plain receipt did not.
// typing says the indicator is on already, as it is after a gather. It
// reports whether the turn started.
func (d *Daemon) startTurn(ctx context.Context, s *session, frags []fragment, typing bool) bool {
	prompts := make([]string, 0, len(frags))
	var heard []string
	for _, f := range frags {
		prompts = append(prompts, f.prompt)
		if f.heard != "" {
			heard = append(heard, f.heard)
		}
	}
	if !s.submit(ctx, strings.Join(prompts, "\n")) {
		return false
	}
	ep := frags[len(frags)-1].ep
	args := map[string]any{"Agent": s.agent(), "Slot": s.slot}
	if len(heard) == 0 {
		d.reply(ctx, ep, "turn.accepted", args)
	} else {
		args["Heard"] = security.SanitizeForIM(strings.Join(heard, " "), heardBudget)
		// Only a managed session is sure to treat /stop as "cancel this turn";
		// an external one may read it as the end of the chat link.
		args["StopHint"] = ""
		if s.external == nil {
			args["StopHint"] = d.t(ctx, ep, "turn.voice_stop_hint", nil)
		}
		d.reply(ctx, ep, "turn.accepted_voice", args)
	}
	if !typing {
		d.typing(ctx, ep, true)
	}
	return true
}
