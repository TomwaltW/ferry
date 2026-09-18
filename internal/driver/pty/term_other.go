//go:build !unix && !windows

package pty

import (
	"io"
	"log/slog"

	gopty "github.com/aymanbagabas/go-pty"
)

// Off Unix and Windows there is no pseudo terminal go-pty can open, so Run
// fails before it gets this far and the terminal is left exactly as it is.

func rawMode(in io.Reader) (func(), error) { return func() {}, nil }

func vtOutput(out io.Writer) func() { return func() {} }

func followSize(p gopty.Pty, out io.Writer, log *slog.Logger) func() { return func() {} }
