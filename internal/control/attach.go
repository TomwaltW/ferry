package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
)

// serveAttach turns one control connection into the stream of an external
// session. It is the last thing that happens on that connection: the
// request/response half is over, and from here both sides exchange Message
// lines until one of them goes away.
func serveAttach(ctx context.Context, conn net.Conn, enc *json.Encoder, dec *bufio.Scanner, req Request, h Handler, clk clock.Clock, log *slog.Logger) {
	var args AttachRequest
	if err := json.Unmarshal(req.Args, &args); err != nil {
		_ = enc.Encode(Response{Error: ErrInternal})
		return
	}
	switch args.Kind {
	case KindProxy, KindChannel, KindPTY:
	default:
		_ = enc.Encode(Response{Error: ErrUnsupported})
		return
	}

	s := newServerStream(conn, enc, dec, clk, log)
	resp, err := h.Attach(ctx, args, s)
	if err != nil {
		_ = enc.Encode(Response{Error: errorCode(err)})
		return
	}
	if err := s.open(ok(resp)); err != nil {
		log.Debug("control: answering attach failed", "err", err)
		s.close()
		return
	}
	go s.keepalive()

	select {
	case <-s.done:
	case <-ctx.Done():
		s.close()
	}
}

// serverStream is the daemon's end of an attached connection. Send is safe
// from any goroutine; Recv expects the single reader the core gives it.
type serverStream struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *bufio.Scanner
	clk  clock.Clock
	log  *slog.Logger

	mu sync.Mutex // guards enc
	// ready is closed once the attach response is on the wire. Until then
	// nothing else may be written: the external process is still reading the
	// answer to its handshake.
	ready chan struct{}
	done  chan struct{}
	once  sync.Once
	// last is the clock time of the newest message, in nanoseconds, so the
	// keepalive can read it without taking the write lock.
	last atomic.Int64
}

func newServerStream(conn net.Conn, enc *json.Encoder, dec *bufio.Scanner, clk clock.Clock, log *slog.Logger) *serverStream {
	s := &serverStream{
		conn:  conn,
		enc:   enc,
		dec:   dec,
		clk:   clk,
		log:   log,
		ready: make(chan struct{}),
		done:  make(chan struct{}),
	}
	s.mark()
	return s
}

// open writes the attach response and lets the stream start.
func (s *serverStream) open(resp Response) error {
	s.mu.Lock()
	err := s.enc.Encode(resp)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("control: answer attach: %w", err)
	}
	close(s.ready)
	return nil
}

// await blocks until the handshake is over, or reports that the stream ended
// before it ever opened.
func (s *serverStream) await() error {
	select {
	case <-s.ready:
		return nil
	case <-s.done:
		return io.EOF
	}
}

func (s *serverStream) Send(m Message) error {
	if err := s.await(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(m); err != nil {
		return fmt.Errorf("control: send %s: %w", m.Type, err)
	}
	return nil
}

// Recv returns the next message the external process meant for the daemon.
// Ping and pong never surface: a ping is answered here, and a pong only
// refreshes the keepalive. A connection that went away reads as io.EOF.
func (s *serverStream) Recv() (Message, error) {
	if err := s.await(); err != nil {
		return Message{}, err
	}
	for {
		if !s.dec.Scan() {
			if err := s.dec.Err(); err != nil && !s.ended() {
				return Message{}, fmt.Errorf("control: read the stream: %w", err)
			}
			s.close()
			return Message{}, io.EOF
		}
		var m Message
		if err := json.Unmarshal(s.dec.Bytes(), &m); err != nil {
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

// close ends the stream and the connection under it. A Recv blocked on the
// socket returns io.EOF.
func (s *serverStream) close() {
	s.once.Do(func() {
		close(s.done)
		if err := s.conn.Close(); err != nil {
			s.log.Debug("control: closing an attached connection", "err", err)
		}
	})
}

func (s *serverStream) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *serverStream) mark() { s.last.Store(s.clk.Now().UnixNano()) }

// keepalive sends a ping every PingInterval and ends the stream once
// PingTimeout has passed with nothing arriving, which is the only way to
// notice an external process that stopped answering without closing its end.
func (s *serverStream) keepalive() {
	for {
		select {
		case <-s.done:
			return
		case <-s.clk.After(PingInterval):
			if s.clk.Now().Sub(time.Unix(0, s.last.Load())) >= PingTimeout {
				s.log.Debug("control: an attached process went quiet", "timeout", PingTimeout)
				s.close()
				return
			}
			if err := s.Send(Message{Type: MsgPing}); err != nil {
				s.close()
				return
			}
		}
	}
}

var _ ServerStream = (*serverStream)(nil)
