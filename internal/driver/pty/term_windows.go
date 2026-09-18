package pty

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// sizePoll is how often the console size is compared with the pseudo
// console's. Windows has no SIGWINCH to say when it changed.
const sizePoll = 250 * time.Millisecond

// rawMode hands the user's console over to the wrapped program: no line
// editing, no echo, no Ctrl+C handling of its own, and keys arriving as the
// escape sequences ConPTY passes on (x/term turns on virtual terminal input).
// The returned function puts it back, and is safe to call even when nothing
// was changed.
//
// An input that is not a console — a pipe in a test, a redirect — is left
// alone: there is no state to save.
func rawMode(in io.Reader) (func(), error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}, nil
	}

	state, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return func() {}, fmt.Errorf("pty: put the console into raw mode: %w", err)
	}
	return func() { _ = term.Restore(int(f.Fd()), state) }, nil
}

// vtOutput makes the user's console interpret the escape sequences ConPTY
// writes, which the classic console host only does when asked. The returned
// function restores the previous mode.
func vtOutput(out io.Writer) func() {
	f, ok := out.(*os.File)
	if !ok {
		return func() {}
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	want := mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT
	if want == mode || windows.SetConsoleMode(h, want) != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}

// followSize keeps the pseudo console the same size as the user's, now and
// whenever the window changes. The returned function stops watching.
func followSize(p gopty.Pty, out io.Writer, log *slog.Logger) func() {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}
	}

	lastW, lastH := 0, 0
	resize := func() {
		w, h, err := term.GetSize(int(f.Fd()))
		if err != nil {
			log.Debug("pty: could not read the console size", "err", err)
			return
		}
		if w == lastW && h == lastH {
			return
		}
		if err := p.Resize(w, h); err != nil {
			log.Debug("pty: could not resize the pseudo console", "err", err)
			return
		}
		lastW, lastH = w, h
	}
	resize()

	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(sizePoll)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				resize()
			}
		}
	}()
	return func() { close(done) }
}
