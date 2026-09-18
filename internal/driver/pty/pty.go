package pty

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

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
}

// Supported reports whether this platform has a pseudo terminal Ferry can use.
// Windows has ConPTY, but wiring it up is P4.
func Supported() bool { return runtime.GOOS != "windows" }

// Run starts the command in a pseudo terminal, forwards the user's terminal
// both ways and registers the session with the daemon. It returns the exit
// code of the child, which `ferry run` exits with.
func Run(ctx context.Context, opts Options) (int, error) {
	return 0, fmt.Errorf("pty: Run: %w", fsp.ErrUnsupported)
}
