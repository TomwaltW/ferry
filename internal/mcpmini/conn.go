package mcpmini

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// maxFrame bounds one line of the protocol. A tool result carrying a file
// listing is the biggest frame Ferry sends, and 16 MiB is what the official Go
// SDK allows, so the two agree on what is too long.
const maxFrame = 16 << 20

// The JSON-RPC 2.0 error codes both roles use.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcError is the error member of a response.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return "mcpmini: rpc error " + strconv.Itoa(e.Code) + ": " + e.Message
}

// frame is one line of the protocol. Requests, notifications and responses all
// travel in this shape; which one it is follows from the members that are set.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// isRequest reports whether the peer expects an answer to this frame.
func (f frame) isRequest() bool { return f.Method != "" && len(f.ID) > 0 }

// isNotification reports whether this frame is a one-way message.
func (f frame) isNotification() bool { return f.Method != "" && len(f.ID) == 0 }

// handlerFunc answers one inbound request. A nil result with a nil error means
// an empty result object, which is what ping answers with.
type handlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

// conn carries JSON-RPC 2.0 over a pair of streams, one JSON object per line.
// Both roles share it: the only difference between them is which methods their
// handler answers.
type conn struct {
	out io.Writer
	in  *bufio.Scanner
	log *slog.Logger

	wmu sync.Mutex

	pmu     sync.Mutex
	nextID  int64
	pending map[string]chan frame

	closeOnce sync.Once
	done      chan struct{}
	err       atomic.Pointer[error]
}

func newConn(in io.Reader, out io.Writer, log *slog.Logger) *conn {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 4096), maxFrame)
	return &conn{
		out:     out,
		in:      sc,
		log:     log,
		pending: make(map[string]chan frame),
		done:    make(chan struct{}),
	}
}

// close ends the connection. Every call still waiting is released with cause,
// and later writes fail rather than block.
func (c *conn) close(cause error) {
	c.closeOnce.Do(func() {
		if cause != nil {
			c.err.Store(&cause)
		}
		close(c.done)
		c.pmu.Lock()
		waiting := c.pending
		c.pending = make(map[string]chan frame)
		c.pmu.Unlock()
		for _, ch := range waiting {
			close(ch)
		}
	})
}

// cause is why the connection ended, or nil for a clean end of stream.
func (c *conn) cause() error {
	if p := c.err.Load(); p != nil {
		return *p
	}
	return nil
}

// write puts one frame on its own line. encoding/json escapes newlines inside
// strings, so a frame never spans two lines however the payload looks.
func (c *conn) write(f frame) error {
	f.JSONRPC = JSONRPCVersion
	line, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("mcpmini: encode %s: %w", f.Method, err)
	}
	select {
	case <-c.done:
		return fmt.Errorf("mcpmini: write %s: %w", f.Method, io.ErrClosedPipe)
	default:
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.out.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("mcpmini: write %s: %w", f.Method, err)
	}
	return nil
}

// notify sends a one-way message.
func (c *conn) notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(frame{Method: method, Params: raw})
}

// call sends a request and waits for the answer that carries its ID.
func (c *conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}

	c.pmu.Lock()
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	reply := make(chan frame, 1)
	c.pending[id] = reply
	c.pmu.Unlock()

	defer func() {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
	}()

	if err := c.write(frame{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return nil, err
	}

	select {
	case f, ok := <-reply:
		if !ok {
			return nil, fmt.Errorf("mcpmini: %s: connection closed: %w", method, endCause(c.cause()))
		}
		if f.Error != nil {
			return nil, fmt.Errorf("mcpmini: %s: %w", method, f.Error)
		}
		return f.Result, nil
	case <-ctx.Done():
		// The peer may still answer; nothing waits for it any more.
		_ = c.notify(NotifyCancelled, cancelledParams{RequestID: json.RawMessage(id), Reason: "caller gave up"})
		return nil, fmt.Errorf("mcpmini: %s: %w", method, ctx.Err())
	}
}

// cancelledParams is what notifications/cancelled carries. Both roles ignore
// the notification on the way in; this is only so the peer sees a well-formed
// one on the way out.
type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason,omitempty"`
}

// endCause turns a clean end of stream into an error callers can test for.
func endCause(err error) error {
	if err == nil {
		return io.EOF
	}
	return err
}

// read runs the receive loop until the stream ends or ctx is done. Requests go
// to handle and are answered; notifications go to onNotify, with
// notifications/cancelled dropped as the protocol asks.
func (c *conn) read(ctx context.Context, handle handlerFunc, onNotify NotifyFunc) {
	defer c.close(nil)
	for c.in.Scan() {
		line := c.in.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			c.log.Error("mcpmini: unparsable frame", "error", err)
			_ = c.write(frame{ID: json.RawMessage("null"), Error: &rpcError{Code: codeParseError, Message: "invalid JSON"}})
			continue
		}
		switch {
		case f.isRequest():
			c.serve(ctx, f, handle)
		case f.isNotification():
			if f.Method == NotifyCancelled {
				// The protocol says a cancellation may arrive for work that
				// already finished, so it is never an error to drop one.
				continue
			}
			if onNotify != nil {
				onNotify(ctx, f.Method, f.Params)
			}
		default:
			c.deliver(f)
		}
		select {
		case <-ctx.Done():
			c.close(ctx.Err())
			return
		default:
		}
	}
	if err := c.in.Err(); err != nil {
		c.close(fmt.Errorf("mcpmini: read: %w", err))
	}
}

// serve answers one request. Handlers run on the read loop: every method here
// is a lookup or a short relay, and answering in order is what keeps the
// stdio pipe's frames in the order the peer sent them.
func (c *conn) serve(ctx context.Context, f frame, handle handlerFunc) {
	if handle == nil {
		_ = c.write(frame{ID: f.ID, Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + f.Method}})
		return
	}
	result, err := handle(ctx, f.Method, f.Params)
	if err != nil {
		_ = c.write(frame{ID: f.ID, Error: rpcErrorFor(err)})
		return
	}
	if result == nil {
		result = struct{}{}
	}
	raw, mErr := json.Marshal(result)
	if mErr != nil {
		_ = c.write(frame{ID: f.ID, Error: &rpcError{Code: codeInternalError, Message: mErr.Error()}})
		return
	}
	_ = c.write(frame{ID: f.ID, Result: raw})
}

// deliver hands a response to whoever is waiting for its ID.
func (c *conn) deliver(f frame) {
	id := string(f.ID)
	c.pmu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pmu.Unlock()
	if !ok {
		c.log.Warn("mcpmini: answer to a request nobody is waiting for", "id", id)
		return
	}
	ch <- f
}

// rpcErrorFor maps a handler's error onto a JSON-RPC error. The sentinel
// errors Ferry uses carry the closest protocol code.
func rpcErrorFor(err error) *rpcError {
	var re *rpcError
	if errors.As(err, &re) {
		return re
	}
	switch {
	case errors.Is(err, fsp.ErrNotFound), errors.Is(err, fsp.ErrUnsupported):
		return &rpcError{Code: codeMethodNotFound, Message: err.Error()}
	default:
		return &rpcError{Code: codeInternalError, Message: err.Error()}
	}
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("mcpmini: encode params: %w", err)
	}
	return raw, nil
}

// trimSpace drops the whitespace a peer may pad a line with. A line that is
// nothing but padding carries no frame.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
