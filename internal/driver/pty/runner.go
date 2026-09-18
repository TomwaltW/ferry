package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"

	gopty "github.com/aymanbagabas/go-pty"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// readChunk is how much of the program's output is read at a time. It is small
// on purpose: a chunk is the unit the idle clock and the line tracker see.
const readChunk = 4096

// promptQueue is how many prompts from chat may be waiting to be typed. The
// daemon does the real queueing; this is only slack for a burst.
const promptQueue = 16

// runner is one wrapped program: the pseudo terminal it runs in, the user's
// terminal on the other side, and the daemon connection that carries chat in and
// output back.
type runner struct {
	opts Options
	log  *slog.Logger
	clk  clock.Clock
	gate *gate

	pty gopty.Pty
	cmd *gopty.Cmd

	// stream and session are the daemon side, and are only set when a daemon
	// was there to attach to.
	stream   *control.Stream
	session  string
	detached sync.Once

	prompts chan string

	mu sync.Mutex
	// capturing collects the program's output for the chat while an injected
	// prompt is being answered; echo is the line that was typed, so it can be
	// left out of what is sent back.
	capturing bool
	captured  []byte
	echo      string
}

// run does the work of Run once the platform and the options have been checked.
func run(ctx context.Context, opts Options) (int, error) {
	r := &runner{
		opts:    opts,
		log:     opts.Log,
		clk:     opts.Clock,
		prompts: make(chan string, promptQueue),
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.clk == nil {
		r.clk = clock.Real()
	}

	var re *regexp.Regexp
	if opts.PromptRegex != "" {
		compiled, err := regexp.Compile(opts.PromptRegex)
		if err != nil {
			return 0, fmt.Errorf("pty: --prompt-regex %q: %w", opts.PromptRegex, err)
		}
		re = compiled
	}
	r.gate = newGate(r.clk, opts.Idle, re)

	p, err := gopty.New()
	if err != nil {
		return 0, fmt.Errorf("pty: open a pseudo terminal: %w", err)
	}
	defer p.Close()
	r.pty = p

	cwd, err := os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("pty: find the working directory: %w", err)
	}

	r.cmd = p.Command(opts.Command[0], opts.Command[1:]...)
	r.cmd.Env = os.Environ()
	r.cmd.Dir = cwd

	// The user's terminal has to stop interpreting keys for the wrapped program
	// to see them, and has to be handed back afterwards whatever happens.
	restore, err := rawMode(opts.In)
	if err != nil {
		return 0, err
	}
	defer restore()
	stopResize := followSize(p, opts.Out, r.log)
	defer stopResize()

	if err := r.cmd.Start(); err != nil {
		return 0, fmt.Errorf("pty: start %s: %w", opts.Command[0], err)
	}

	// Everything the runner does of its own accord ends when the child does.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	r.attach(runCtx, cwd)
	defer r.detach()

	// copyIn is deliberately not waited for. A read of the user's terminal
	// cannot be interrupted, so that goroutine ends on its next keystroke, once
	// the pseudo terminal it writes to is gone.
	go r.copyIn(runCtx)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.copyOut() }()
	go func() { defer wg.Done(); r.injectLoop(runCtx) }()
	if r.stream != nil {
		wg.Add(1)
		go func() { defer wg.Done(); r.recvLoop(runCtx) }()
	}

	waitErr := r.cmd.Wait()
	code := exitCode(r.cmd.ProcessState)

	// The child is gone: nothing is left to forward or to inject. Closing the
	// pseudo terminal ends the output loop, and hanging up on the daemon ends
	// the one waiting on it.
	cancel()
	p.Close()
	r.detach()
	wg.Wait()

	// An exit status that never arrived is the one case the caller hears about:
	// the program ran, so its own code is the answer to everything else.
	if r.cmd.ProcessState == nil {
		return 1, fmt.Errorf("pty: wait for %s: %w", opts.Command[0], waitErr)
	}
	return code, nil
}

// attach registers the session with the daemon. A daemon that is not running is
// not a failure: ferry run then only wraps the program, as spec-p3 3.8 says.
func (r *runner) attach(ctx context.Context, cwd string) {
	client, err := control.Dial(r.opts.Home)
	if err != nil {
		if !errors.Is(err, fsp.ErrNotFound) {
			r.log.Debug("pty: could not reach the daemon", "err", err)
		}
		return
	}

	name := r.opts.Name
	if name == "" {
		name = r.opts.Command[0]
	}

	stream, resp, err := client.Attach(ctx, control.AttachRequest{
		Kind:  control.KindPTY,
		Agent: name,
		Cwd:   cwd,
		Title: name,
		PID:   r.cmd.Process.Pid,
		// A wrapped program takes typed input, so chat may send it some.
		// Approvals are another matter: there is nothing to relay them through,
		// which is what the daemon answers pty.no_approval about.
		AllowInject:   true,
		RemoteApprove: false,
	})
	if err != nil {
		r.log.Debug("pty: the daemon refused the session", "err", err)
		client.Close()
		return
	}
	r.stream, r.session = stream, resp.SessionID
}

// detach tells the daemon the session is over and hangs up. It runs once, so the
// orderly path and the deferred safety net cannot both send a goodbye.
func (r *runner) detach() {
	if r.stream == nil {
		return
	}
	r.detached.Do(func() {
		if err := r.stream.Send(control.Message{Type: control.MsgDetach, SessionID: r.session}); err != nil {
			r.log.Debug("pty: could not say goodbye to the daemon", "err", err)
		}
		r.stream.Close()
	})
}

// copyIn forwards the user's keystrokes to the program, noting that the user is
// at the keyboard so an injection waits for them to stop.
func (r *runner) copyIn(ctx context.Context) {
	if r.opts.In == nil {
		return
	}
	buf := make([]byte, readChunk)
	for {
		n, err := r.opts.In.Read(buf)
		if n > 0 {
			r.gate.sawKeys()
			if _, werr := r.pty.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// copyOut forwards the program's output to the user's terminal, and keeps a copy
// while an injected prompt is being answered.
func (r *runner) copyOut() {
	buf := make([]byte, readChunk)
	for {
		n, err := r.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			r.gate.sawOutput(chunk)
			r.collect(chunk)
			if r.opts.Out != nil {
				if _, werr := r.opts.Out.Write(chunk); werr != nil {
					r.log.Debug("pty: could not write to the terminal", "err", werr)
				}
			}
		}
		if err != nil {
			// The pseudo terminal reads as an error once the child is gone,
			// which is the end of the output rather than a fault.
			return
		}
	}
}

func (r *runner) collect(chunk []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.capturing {
		r.captured = append(r.captured, chunk...)
	}
}

// recvLoop acts on what the daemon sends. An input the session cannot take is
// dropped with a note in the log: the reply to chat is the daemon's to make.
func (r *runner) recvLoop(ctx context.Context) {
	for {
		m, err := r.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.log.Debug("pty: the daemon connection ended", "err", err)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}

		switch m.Type {
		case control.MsgPrompt:
			if m.Text == "" {
				continue
			}
			select {
			case r.prompts <- m.Text:
			default:
				r.log.Debug("pty: too many prompts waiting, dropping one")
			}

		case control.MsgCancel:
			// The one thing a wrapped program understands as "stop": the
			// interrupt character the user would have typed.
			if _, err := r.pty.Write([]byte{0x03}); err != nil {
				r.log.Debug("pty: could not interrupt the program", "err", err)
			}

		case control.MsgClose:
			// Closing is the user's own doing on this side; all Ferry can do is
			// ask the program to end its input.
			if _, err := r.pty.Write([]byte{0x04}); err != nil {
				r.log.Debug("pty: could not end the program's input", "err", err)
			}

		case control.MsgConfig, control.MsgVerdict:
			r.log.Debug("pty: a wrapped program has no use for this message", "type", m.Type)
		}
	}
}

// injectLoop types the prompts from chat into the program, one at a time and
// only when the gate lets it.
func (r *runner) injectLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-r.prompts:
			r.inject(ctx, text)
		}
	}
}

// inject waits for a quiet moment, types the prompt as a paste, then collects
// what the program said back and sends it on as one turn.
func (r *runner) inject(ctx context.Context, text string) {
	if err := r.gate.wait(ctx, true); err != nil {
		return
	}

	r.startCapture(text)
	r.gate.sawInjection()
	if _, err := r.pty.Write(pasteBytes(text)); err != nil {
		r.log.Debug("pty: could not type the prompt", "err", err)
		r.endCapture()
		return
	}

	// The answer is whatever arrives before the program falls quiet again.
	if err := r.gate.wait(ctx, false); err != nil {
		// A program that exited mid-answer still said what it said.
		r.report(r.endCapture())
		return
	}
	r.report(r.endCapture())
}

func (r *runner) startCapture(echo string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capturing = true
	r.captured = nil
	r.echo = echo
}

// endCapture stops collecting and returns what the program said, without the
// escape sequences, the rewritten lines or the echo of the prompt.
func (r *runner) endCapture() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capturing = false
	raw, echo := string(r.captured), r.echo
	r.captured, r.echo = nil, ""
	return cleanOutput(raw, echo)
}

// report sends one turn's worth of output to the daemon: the text, then the end
// of the turn.
func (r *runner) report(text string) {
	if r.stream == nil {
		return
	}
	at := r.clk.Now()
	if text != "" {
		r.sendEvent(fsp.Event{Kind: fsp.EvChunk, Text: text, At: at})
	}
	r.sendEvent(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn", At: at})
}

func (r *runner) sendEvent(e fsp.Event) {
	if err := r.stream.Send(control.Message{Type: control.MsgEvent, SessionID: r.session, Event: &e}); err != nil {
		r.log.Debug("pty: could not send an event to the daemon", "err", err)
	}
}
