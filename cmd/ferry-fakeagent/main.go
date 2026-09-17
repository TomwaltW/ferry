// Command ferry-fakeagent runs the scripted ACP agent used by the test suite.
// It speaks ACP over stdin and stdout, so a driver test can start it the same
// way it would start a real agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/TomwaltW/ferry/internal/testkit/fakeagent"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ferry-fakeagent:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ferry-fakeagent", flag.ContinueOnError)
	resume := fs.String("resume", "", "print that this session id was resumed, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// --resume stands in for an agent that reattaches to a terminal; the P2
	// tests only need the acknowledgement line.
	if *resume != "" {
		_, err := fmt.Fprintf(out, "resumed %s\n", *resume)
		return err
	}

	// The ACP SDK logs a line per connection through the default handler; a
	// test double should only speak up when something is actually wrong.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	home := os.Getenv("FAKEAGENT_HOME")
	if home == "" {
		dir, err := os.MkdirTemp("", "ferry-fakeagent-")
		if err != nil {
			return fmt.Errorf("create a home directory: %w", err)
		}
		defer os.RemoveAll(dir)
		home = dir
	}
	return fakeagent.Serve(ctx, os.Stdin, out, home)
}
