package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// Options is the command line of `ferry proxy [--allow-inject]
// [--remote-approve] [--title <name>] <agent>` plus the streams the editor
// started it on.
type Options struct {
	Agent string
	Title string
	// AllowInject lets input from chat reach the agent while the editor is
	// idle; without it such input is answered with inject.disabled.
	AllowInject bool
	// RemoteApprove sends permission requests to chat instead of the editor.
	RemoteApprove bool
	Home          string
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	Log           *slog.Logger
}

// Run serves the editor on In/Out, drives the real agent underneath and
// mirrors every session to the daemon. It returns once the editor disconnects.
// A daemon that is not running is not an error: the proxy then forwards
// transparently and says so on Err.
func Run(ctx context.Context, opts Options) error {
	return fmt.Errorf("proxy: Run: %w", fsp.ErrUnsupported)
}
