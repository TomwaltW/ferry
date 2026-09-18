package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// dialTimeout is how long the CLI waits for a daemon that published a port but
// is not answering on it.
const dialTimeout = 2 * time.Second

// Client is one connection to the daemon's control socket.
type Client struct {
	mu    sync.Mutex
	conn  net.Conn
	enc   *json.Encoder
	dec   *bufio.Scanner
	token string
	clk   clock.Clock

	// closed is set by Close, so that a read interrupted by our own Close
	// reads as a clean end of stream rather than as a socket error.
	closed atomic.Bool
}

// Dial reads <home>/control.json and connects to the daemon. A missing file or
// a port nobody listens on both mean "no daemon", which is fsp.ErrNotFound.
func Dial(home string) (*Client, error) {
	path := filepath.Join(home, SocketName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("control: %s: %w", path, fsp.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("control: read %s: %w", path, err)
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("control: parse %s: %w", path, fsp.ErrNotFound)
	}
	if ep.Port <= 0 || ep.Token == "" {
		return nil, fmt.Errorf("control: %s is incomplete: %w", path, fsp.ErrNotFound)
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)), dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("control: connect to the daemon: %w", fsp.ErrNotFound)
	}
	return newClient(conn, ep.Token, clock.Real()), nil
}

// newClient wraps an established connection. Dial uses it; tests use it to
// speak to a listener of their own, with a clock they drive themselves.
func newClient(conn net.Conn, token string, clk clock.Clock) *Client {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	return &Client{conn: conn, enc: json.NewEncoder(conn), dec: scanner, token: token, clk: clk}
}

func (c *Client) Close() error {
	c.closed.Store(true)
	return c.conn.Close()
}

func (c *Client) Park(ctx context.Context, req ParkRequest) (ParkResponse, error) {
	args, err := json.Marshal(req)
	if err != nil {
		return ParkResponse{}, fmt.Errorf("control: encode park: %w", err)
	}
	var out ParkResponse
	if err := c.call(ctx, OpPark, args, &out); err != nil {
		return ParkResponse{}, err
	}
	return out, nil
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	if err := c.call(ctx, OpStatus, nil, &out); err != nil {
		return StatusResponse{}, err
	}
	return out, nil
}

// call writes one request and reads the one response that answers it. The
// deadline of ctx, when it has one, also bounds the read.
func (c *Client) call(ctx context.Context, op string, args json.RawMessage, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer c.conn.SetDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()

	if err := c.enc.Encode(Request{Token: c.token, Op: op, Args: args}); err != nil {
		return fmt.Errorf("control: send %s: %w", op, err)
	}
	if !c.dec.Scan() {
		if err := c.dec.Err(); err != nil {
			return fmt.Errorf("control: read the answer to %s: %w", op, err)
		}
		// The daemon closes the connection on a token it does not know.
		return fmt.Errorf("control: the daemon closed the connection: %w", fsp.ErrNotFound)
	}
	var resp Response
	if err := json.Unmarshal(c.dec.Bytes(), &resp); err != nil {
		return fmt.Errorf("control: parse the answer to %s: %w", op, err)
	}
	if !resp.OK {
		return decodeError(resp.Error)
	}
	if out == nil || len(resp.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("control: parse the data of %s: %w", op, err)
	}
	return nil
}

// Attach registers an external session and hands back the stream that carries
// it. It is the last call the Client makes on this connection: Park and Status
// share the same socket, and after attach the socket belongs to the stream.
func (c *Client) Attach(ctx context.Context, req AttachRequest) (*Stream, AttachResponse, error) {
	args, err := json.Marshal(req)
	if err != nil {
		return nil, AttachResponse{}, fmt.Errorf("control: encode attach: %w", err)
	}
	var out AttachResponse
	if err := c.call(ctx, OpAttach, args, &out); err != nil {
		return nil, AttachResponse{}, err
	}
	s := &Stream{c: c, done: make(chan struct{})}
	s.mark()
	go s.keepalive()
	return s, out, nil
}

// Stream is the client half of an attached connection. Send is safe to call
// from any goroutine; Recv expects a single reader, which is how the external
// processes are built: one loop reads, everything else writes.
type Stream struct {
	c    *Client
	done chan struct{}
	once sync.Once
	// last is the clock time of the newest message, in nanoseconds, so that
	// the keepalive can read it without taking the send lock.
	last atomic.Int64
}

func (s *Stream) Send(m Message) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if err := s.c.enc.Encode(m); err != nil {
		return fmt.Errorf("control: send %s: %w", m.Type, err)
	}
	return nil
}

// Recv returns the next message meant for the caller. Ping and pong never
// surface: a ping is answered here, and a pong only refreshes the keepalive.
// A connection that went away — the peer's, or ours through Close — reads as
// io.EOF.
func (s *Stream) Recv() (Message, error) {
	for {
		if !s.c.dec.Scan() {
			if err := s.c.dec.Err(); err != nil && !s.c.closed.Load() {
				return Message{}, fmt.Errorf("control: read the stream: %w", err)
			}
			return Message{}, io.EOF
		}
		var m Message
		if err := json.Unmarshal(s.c.dec.Bytes(), &m); err != nil {
			return Message{}, fmt.Errorf("control: parse a stream message: %w", err)
		}
		s.mark()
		switch m.Type {
		case MsgPing:
			if err := s.Send(Message{Type: MsgPong}); err != nil {
				return Message{}, err
			}
		case MsgPong:
		default:
			return m, nil
		}
	}
}

// Close ends the stream and the connection under it. A Recv blocked on the
// socket returns io.EOF.
func (s *Stream) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.c.Close()
	})
	return err
}

func (s *Stream) mark() { s.last.Store(s.c.clk.Now().UnixNano()) }

// keepalive sends a ping every PingInterval and closes the stream once
// PingTimeout passed without a single message, which is the only way to notice
// a peer that stopped answering without closing its end.
func (s *Stream) keepalive() {
	for {
		select {
		case <-s.done:
			return
		case <-s.c.clk.After(PingInterval):
			if s.c.clk.Now().Sub(time.Unix(0, s.last.Load())) >= PingTimeout {
				_ = s.Close()
				return
			}
			if err := s.Send(Message{Type: MsgPing}); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}
