package v2

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// maxLine caps one JSON-RPC message. The v2 transport is newline delimited
// JSON, so a message that big is a peer gone wrong rather than a long answer.
const maxLine = 1 << 20

// errConnClosed reports a peer that went away with requests still in flight.
var errConnClosed = errors.New("acp/v2: the connection closed")

// codeAuthRequired is the reserved error an agent answers with while it still
// needs a login. The protocol leaves the exact code to the agent, so it is
// matched loosely: any error mentioning authentication counts too.
const codeAuthRequired = -32000

// message is one line of the protocol. The same struct serves requests,
// responses and notifications, which is what lets the read loop decide what it
// has by looking at which fields are set.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// reply is one answer handed back to whoever is waiting in call.
type reply struct {
	result json.RawMessage
	err    *rpcError
}

// conn is a JSON-RPC 2.0 peer speaking newline delimited JSON. It is written
// for this package rather than taken from a library: v2 has no SDK yet, and the
// protocol is small enough that one file covers it.
type conn struct {
	log *slog.Logger

	wmu sync.Mutex
	w   io.Writer

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan reply
	closed  bool

	// handle answers the requests the agent sends; in v2 that is
	// session/request_permission and nothing else. It runs on its own
	// goroutine, because an approval blocks until a person answers and the read
	// loop must keep running meanwhile.
	handle func(ctx context.Context, method string, params json.RawMessage) (any, *rpcError)
	// notify receives the agent's notifications in the order they arrived.
	notify func(method string, params json.RawMessage)
}

func newConn(w io.Writer, log *slog.Logger) *conn {
	return &conn{log: log, w: w, pending: map[int64]chan reply{}}
}

// call sends one request and waits for its answer, for as long as ctx allows.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("acp/v2: %s: %w", method, errConnClosed)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	idRaw, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("acp/v2: encode the id of %s: %w", method, err)
	}
	if err := c.write(message{JSONRPC: "2.0", ID: idRaw, Method: method, Params: raw}); err != nil {
		return fmt.Errorf("acp/v2: send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("acp/v2: %s: %w", method, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("acp/v2: %s: %w", method, r.err)
		}
		if out == nil || len(r.result) == 0 {
			return nil
		}
		if err := json.Unmarshal(r.result, out); err != nil {
			return fmt.Errorf("acp/v2: parse the answer to %s: %w", method, err)
		}
		return nil
	}
}

// send writes a notification, which by definition has no answer to wait for.
func (c *conn) send(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	if err := c.write(message{JSONRPC: "2.0", Method: method, Params: raw}); err != nil {
		return fmt.Errorf("acp/v2: send %s: %w", method, err)
	}
	return nil
}

func (c *conn) write(m message) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(line, '\n'))
	return err
}

// serve reads until the stream ends, dispatching as it goes. Every request
// still waiting when the stream ends is failed, so no caller is left hanging on
// an agent that died.
func (c *conn) serve(r io.Reader) {
	defer c.shutdown()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			c.log.Debug("acp/v2: unparsable message from the agent", "err", err)
			continue
		}
		c.dispatch(m)
	}
	if err := scanner.Err(); err != nil {
		c.log.Debug("acp/v2: the agent's stream ended badly", "err", err)
	}
}

func (c *conn) dispatch(m message) {
	switch {
	case m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null":
		go c.answer(m)
	case m.Method != "":
		if c.notify != nil {
			c.notify(m.Method, m.Params)
		}
	default:
		c.deliver(m)
	}
}

// answer runs one incoming request and writes whatever it returned.
func (c *conn) answer(m message) {
	if c.handle == nil {
		c.replyError(m.ID, &rpcError{Code: -32601, Message: "method not found: " + m.Method})
		return
	}
	result, rpcErr := c.handle(context.Background(), m.Method, m.Params)
	if rpcErr != nil {
		c.replyError(m.ID, rpcErr)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		c.replyError(m.ID, &rpcError{Code: -32603, Message: "could not encode the answer: " + err.Error()})
		return
	}
	if err := c.write(message{JSONRPC: "2.0", ID: m.ID, Result: raw}); err != nil {
		c.log.Debug("acp/v2: could not answer the agent", "method", m.Method, "err", err)
	}
}

func (c *conn) replyError(id json.RawMessage, e *rpcError) {
	if err := c.write(message{JSONRPC: "2.0", ID: id, Error: e}); err != nil {
		c.log.Debug("acp/v2: could not send an error to the agent", "err", err)
	}
}

// deliver hands a response to the call that is waiting for it.
func (c *conn) deliver(m message) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		c.log.Debug("acp/v2: a response arrived with an unusable id", "id", string(m.ID))
		return
	}

	c.mu.Lock()
	ch, ok := c.pending[id]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- reply{result: m.Result, err: m.Error}:
	default:
	}
}

// shutdown fails every request still in flight.
func (c *conn) shutdown() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	waiting := c.pending
	c.pending = map[int64]chan reply{}
	c.mu.Unlock()

	for _, ch := range waiting {
		select {
		case ch <- reply{err: &rpcError{Code: -32000, Message: errConnClosed.Error()}}:
		default:
		}
	}
}

// marshalParams encodes the params of a call, sending nothing at all for a nil
// one so that a parameterless method stays parameterless on the wire.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("acp/v2: encode parameters: %w", err)
	}
	return raw, nil
}

// isAuthRequired reports the error an agent answers with while it is still
// waiting to be logged in.
func isAuthRequired(err error) bool {
	var e *rpcError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == codeAuthRequired
}
