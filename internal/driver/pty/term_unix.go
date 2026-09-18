//go:build unix

package pty

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/term"
)

// rawMode hands the user's terminal over to the wrapped program: no line
// editing, no signal keys, no echo of its own. The returned function puts it
// back, and is safe to call even when nothing was changed.
//
// An input that is not a terminal — a pipe in a test, a redirect — is left
// alone: there is no state to save.
func rawMode(in io.Reader) (func(), error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}, nil
	}

	state, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return func() {}, fmt.Errorf("pty: put the terminal into raw mode: %w", err)
	}
	return func() { _ = term.Restore(int(f.Fd()), state) }, nil
}

// followSize keeps the pseudo terminal the same size as the user's, now and
// every time the window changes. The returned function stops watching.
func followSize(p gopty.Pty, out io.Writer, log *slog.Logger) func() {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}
	}

	resize := func() {
		w, h, err := term.GetSize(int(f.Fd()))
		if err != nil {
			log.Debug("pty: could not read the terminal size", "err", err)
			return
		}
		if err := p.Resize(w, h); err != nil {
			log.Debug("pty: could not resize the pseudo terminal", "err", err)
		}
	}
	resize()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-winch:
				resize()
			}
		}
	}()

	return func() {
		signal.Stop(winch)
		close(done)
	}
}

// vtOutput has nothing to do on Unix: a terminal there reads escape sequences
// already.
func vtOutput(out io.Writer) func() { return func() {} }
