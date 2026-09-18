//go:build !unix

package pty

import (
	"io"
	"log/slog"

	gopty "github.com/aymanbagabas/go-pty"
)

// Off Unix there is no ferry run yet — Run reports the platform as unsupported
// before it gets this far — so the terminal is left exactly as it is.

func rawMode(in io.Reader) (func(), error) { return func() {}, nil }

func followSize(p gopty.Pty, out io.Writer, log *slog.Logger) func() { return func() {} }
