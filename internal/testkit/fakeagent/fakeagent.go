// Package fakeagent is a scripted ACP agent used by every driver test. Its
// replies are decided by the prefix of the incoming prompt, listed in the
// table of spec-p0 3.5, so a test can ask for streaming, a permission prompt,
// a slow turn, a plan, a tool call or a JSON-RPC failure by name.
package fakeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/clock"
)

// Serve runs the fake agent over the given reader and writer until in is
// closed or ctx ends. When in closes, responses for requests already accepted
// are flushed before returning. home is where sessions are persisted.
func Serve(ctx context.Context, in io.Reader, out io.Writer, home string) error {
	if home == "" {
		return errors.New("fakeagent: home must not be empty")
	}
	st, err := newStore(home)
	if err != nil {
		return err
	}

	d := newDrain()
	ag := newAgent(clock.Real(), st)
	// The SDK starts its receive goroutine inside the constructor, so its
	// SetLogger races with its own shutdown path and is left alone here. A
	// caller that wants the connection quiet sets the default slog handler.
	conn := acp.NewAgentSideConnection(ag, d.wrapWriter(out), d.wrapReader(ctx, in))
	ag.attach(conn)

	select {
	case <-conn.Done():
	case <-ctx.Done():
	}
	d.wait(ctx)
	return nil
}

// drain counts the JSON-RPC requests the peer has sent against the responses
// written back, so Serve can wait for every accepted request to be answered
// before it returns. Counting happens on the byte stream rather than around
// the handlers because the SDK writes a response after its handler returns.
type drain struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending int
	give    bool // stop waiting even though responses are still outstanding
}

func newDrain() *drain {
	d := &drain{}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// envelope is the part of a JSON-RPC message needed to tell a request (id and
// method) from a response (id, no method).
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func parseEnvelope(line []byte) (env envelope, hasID bool) {
	if json.Unmarshal(line, &env) != nil {
		return envelope{}, false
	}
	return env, len(env.ID) > 0 && string(env.ID) != "null"
}

func (d *drain) accepted() {
	d.mu.Lock()
	d.pending++
	d.mu.Unlock()
}

func (d *drain) answered() {
	d.mu.Lock()
	if d.pending > 0 {
		d.pending--
	}
	d.mu.Unlock()
	d.cond.Broadcast()
}

// wait blocks until every accepted request has been answered, or until ctx
// ends.
func (d *drain) wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.mu.Lock()
		defer d.mu.Unlock()
		for d.pending > 0 && !d.give {
			d.cond.Wait()
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		d.mu.Lock()
		d.give = true
		d.mu.Unlock()
		d.cond.Broadcast()
		<-done
	}
}

func (d *drain) wrapReader(ctx context.Context, r io.Reader) io.Reader {
	return &countingReader{d: d, ctx: ctx, r: r}
}

func (d *drain) wrapWriter(w io.Writer) io.Writer {
	return &countingWriter{d: d, w: w}
}

// countingReader tallies inbound requests as the bytes are handed to the SDK,
// which keeps the tally ahead of the dispatch that follows the Read. It also
// reports EOF once ctx ends so a cancelled Serve stops feeding the connection.
type countingReader struct {
	d    *drain
	ctx  context.Context
	r    io.Reader
	part []byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.ctx.Err() != nil {
		return 0, io.EOF
	}
	n, err := c.r.Read(p)
	if n > 0 {
		eachLine(&c.part, p[:n], func(line []byte) {
			if env, ok := parseEnvelope(line); ok && env.Method != "" {
				c.d.accepted()
			}
		})
	}
	return n, err
}

// countingWriter tallies outbound responses once their bytes have been
// written. Requests the agent itself sends carry a method and are skipped.
type countingWriter struct {
	d    *drain
	w    io.Writer
	part []byte
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		eachLine(&c.part, p[:n], func(line []byte) {
			if env, ok := parseEnvelope(line); ok && env.Method == "" {
				c.d.answered()
			}
		})
	}
	return n, err
}

// eachLine calls fn for every newline-terminated line in chunk, carrying an
// unterminated tail over to the next call through part.
func eachLine(part *[]byte, chunk []byte, fn func(line []byte)) {
	buf := append(*part, chunk...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		if line := buf[:i]; len(line) > 0 {
			fn(line)
		}
		buf = buf[i+1:]
	}
	*part = append([]byte(nil), buf...)
}
