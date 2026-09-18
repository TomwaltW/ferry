package pty

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// DefaultIdle is how long the wrapped program has to stay quiet before an
// injected prompt is considered safe to type.
const DefaultIdle = 1500 * time.Millisecond

// TypingGrace pushes an injection back while the user is at the keyboard.
const TypingGrace = 2 * time.Second

// The bracketed paste markers an injected prompt is wrapped in, so that the
// program reads it as pasted text rather than as keystrokes.
const (
	PasteStart = "\x1b[200~"
	PasteEnd   = "\x1b[201~"
)

// Options is the command line of `ferry run [--name <name>] [--idle <ms>]
// [--prompt-regex <re>] -- <command> [args...]`.
type Options struct {
	Name string
	// Idle is the quiet period before an injection; 0 means DefaultIdle.
	Idle time.Duration
	// PromptRegex, when set, also requires the last line to match before an
	// injection goes in.
	PromptRegex string
	Command     []string
	Home        string
	In          io.Reader
	Out         io.Writer
	Log         *slog.Logger
	// Clock is the source of time for the idle and typing rules; nil is the real
	// one. The tests drive it themselves.
	Clock clock.Clock
}

// Supported reports whether this platform has a pseudo terminal Ferry can use:
// a Unix one, or ConPTY on Windows 10 1809 and later.
func Supported() bool { return platformSupported() }

// Run starts the command in a pseudo terminal, forwards the user's terminal
// both ways and registers the session with the daemon. It returns the exit
// code of the child, which `ferry run` exits with.
func Run(ctx context.Context, opts Options) (int, error) {
	if !Supported() {
		return 0, fmt.Errorf("pty: %s: %w", runtime.GOOS, fsp.ErrUnsupported)
	}
	if len(opts.Command) == 0 {
		return 0, fmt.Errorf("pty: there is no command to run")
	}
	return run(ctx, opts)
}
