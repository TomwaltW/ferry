package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
)

// maxFrameBytes caps one line of an event stream. A tool result pasted into a
// message part is the largest thing that arrives on one line.
const maxFrameBytes = 4 << 20

// Reconnect schedule from spec-p2 3.5: 1, 2, 4 ... seconds, capped at 30, and
// five minutes without a connection is where the driver stops trying.
const (
	firstBackoff = time.Second
	maxBackoff   = 30 * time.Second
	giveUpAfter  = 5 * time.Minute
)

// sseEvent is one frame of a text/event-stream. Name is the `event:` field,
// which OpenCode leaves out because it puts the name inside the payload.
type sseEvent struct {
	Name string
	Data []byte
}

// readSSE parses frames off r until it ends, calling fn for each. Comment lines
// (the keep-alives both services send) and fields Ferry has no use for are
// skipped.
func readSSE(r io.Reader, fn func(sseEvent)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)

	var (
		name string
		data []byte
		seen bool
	)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if seen {
				fn(sseEvent{Name: name, Data: data})
			}
			name, data, seen = "", nil, false

		case strings.HasPrefix(line, ":"):
			// A comment, which is how both services keep the socket warm.

		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name, seen = value, true
			case "data":
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data, seen = append(data, value...), true
			}
		}
	}
	if seen {
		fn(sseEvent{Name: name, Data: data})
	}
	return sc.Err()
}

// streamer keeps one session's event stream up. It reconnects on the schedule
// of spec-p2 3.5, asks the session to catch up after every reconnect, and gives
// up once the service has been unreachable for five minutes.
type streamer struct {
	clk clock.Clock
	log *slog.Logger

	// open makes one connection attempt.
	open func(ctx context.Context) (io.ReadCloser, error)
	// onEvent receives every frame, in order, from one goroutine.
	onEvent func(sseEvent)
	// onReconnect runs after a connection has been re-established, never after
	// the first one: whatever happened while the stream was down is missing.
	onReconnect func(ctx context.Context)
	// onGiveUp runs once, when the service has stayed unreachable too long.
	onGiveUp func(error)
}

// run drives the stream until ctx ends or it gives up. first is the connection
// the caller already opened, which is what lets Start fail loudly when the
// service is not there at all.
func (s *streamer) run(ctx context.Context, first io.ReadCloser) {
	var (
		body      = first
		reconnect bool
		downSince time.Time
		attempt   int
		lastErr   error
	)

	for {
		if body != nil {
			if reconnect {
				s.onReconnect(ctx)
			}
			downSince, attempt, lastErr = time.Time{}, 0, nil

			if err := readSSE(body, s.onEvent); err != nil && ctx.Err() == nil {
				s.log.Debug("the event stream ended", "err", err)
				lastErr = err
			}
			body.Close()
			body, reconnect = nil, true
			if ctx.Err() != nil {
				return
			}
			downSince = s.clk.Now()
		}

		if !downSince.IsZero() && s.clk.Now().Sub(downSince) >= giveUpAfter {
			s.onGiveUp(giveUpError(lastErr))
			return
		}

		select {
		case <-s.clk.After(backoffAt(attempt)):
		case <-ctx.Done():
			return
		}
		attempt++

		next, err := s.open(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Debug("could not reopen the event stream", "attempt", attempt, "err", err)
			lastErr = err
			continue
		}
		body = next
	}
}

// backoffAt is the wait before attempt n, counting from zero: one second,
// doubling, capped at thirty.
func backoffAt(attempt int) time.Duration {
	d := firstBackoff
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

func giveUpError(last error) error {
	if last == nil {
		return errors.New("the service has been unreachable for five minutes")
	}
	return errors.New("the service has been unreachable for five minutes: " + last.Error())
}
