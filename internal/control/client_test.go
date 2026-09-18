package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// fakeDaemon is the other end of a control connection: it speaks the raw line
// protocol, so that the tests below exercise the client alone rather than the
// client against the server the T1 task writes.
type fakeDaemon struct {
	t    *testing.T
	conn net.Conn
	enc  *json.Encoder
	dec  *bufio.Scanner
}

func (d *fakeDaemon) readRequest() Request {
	d.t.Helper()
	if !d.dec.Scan() {
		d.t.Fatalf("daemon: no request: %v", d.dec.Err())
	}
	var req Request
	if err := json.Unmarshal(d.dec.Bytes(), &req); err != nil {
		d.t.Fatalf("daemon: parse request %q: %v", d.dec.Bytes(), err)
	}
	return req
}

func (d *fakeDaemon) readMessage() Message {
	d.t.Helper()
	if !d.dec.Scan() {
		d.t.Fatalf("daemon: no message: %v", d.dec.Err())
	}
	var m Message
	if err := json.Unmarshal(d.dec.Bytes(), &m); err != nil {
		d.t.Fatalf("daemon: parse message %q: %v", d.dec.Bytes(), err)
	}
	return m
}

func (d *fakeDaemon) write(v any) {
	d.t.Helper()
	if err := d.enc.Encode(v); err != nil {
		d.t.Fatalf("daemon: write: %v", err)
	}
}

// dialFake puts a Client and a fakeDaemon on the two ends of one loopback
// connection. The clock is the client's, so keepalive only moves when the test
// says so.
func dialFake(t *testing.T, clk clock.Clock) (*Client, *fakeDaemon) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	srv, ok := <-accepted
	if !ok {
		t.Fatal("the fake daemon accepted nothing")
	}

	c := newClient(conn, "token", clk)
	t.Cleanup(func() {
		c.Close()
		srv.Close()
	})
	scanner := bufio.NewScanner(srv)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	return c, &fakeDaemon{t: t, conn: srv, enc: json.NewEncoder(srv), dec: scanner}
}

func testClock() *clock.Fake { return clock.NewFake(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)) }

// attach runs the handshake with a daemon that answers resp, and returns the
// request it saw along with the stream.
func attach(t *testing.T, c *Client, d *fakeDaemon, req AttachRequest, resp AttachResponse) (*Stream, Request) {
	t.Helper()
	seen := make(chan Request, 1)
	go func() {
		got := d.readRequest()
		d.write(ok(resp))
		seen <- got
	}()
	s, got, err := c.Attach(context.Background(), req)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got != resp {
		t.Errorf("Attach returned %+v, want %+v", got, resp)
	}
	return s, <-seen
}

func TestClientAttachHandshake(t *testing.T) {
	c, d := dialFake(t, testClock())
	want := AttachRequest{Kind: KindProxy, Agent: "fake", Cwd: "/tmp/demo", Title: "demo", PID: 4711, AllowInject: true}
	_, req := attach(t, c, d, want, AttachResponse{SessionID: "s-1", Slot: 2, Endpoint: "wechat:bot:owner"})

	if req.Op != OpAttach {
		t.Errorf("op = %q, want %q", req.Op, OpAttach)
	}
	if req.Token != "token" {
		t.Errorf("token = %q, want the one from control.json", req.Token)
	}
	var got AttachRequest
	if err := json.Unmarshal(req.Args, &got); err != nil {
		t.Fatalf("parse args %q: %v", req.Args, err)
	}
	if got != want {
		t.Errorf("args = %+v, want %+v", got, want)
	}
}

func TestClientAttachReportsDaemonError(t *testing.T) {
	c, d := dialFake(t, testClock())
	go func() {
		d.readRequest()
		d.write(Response{Error: ErrNotFound})
	}()
	s, _, err := c.Attach(context.Background(), AttachRequest{Kind: KindPTY})
	if !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("Attach error = %v, want %v", err, fsp.ErrNotFound)
	}
	if s != nil {
		t.Errorf("Attach returned a stream %v alongside an error", s)
	}
}

func TestClientStreamSendAndRecv(t *testing.T) {
	c, d := dialFake(t, testClock())
	s, _ := attach(t, c, d, AttachRequest{Kind: KindChannel}, AttachResponse{SessionID: "s-1"})

	ev := fsp.Event{Kind: fsp.EvChunk, Text: "hi"}
	if err := s.Send(Message{Type: MsgEvent, SessionID: "s-1", Event: &ev}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := d.readMessage()
	if got.Type != MsgEvent || got.SessionID != "s-1" || got.Event == nil || got.Event.Text != "hi" {
		t.Errorf("the daemon read %+v, want an event carrying %q", got, "hi")
	}

	d.write(Message{Type: MsgPrompt, SessionID: "s-1", Text: "echo hi", Sender: "owner"})
	in, err := s.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	want := Message{Type: MsgPrompt, SessionID: "s-1", Text: "echo hi", Sender: "owner"}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("Recv = %+v, want %+v", in, want)
	}
}

func TestClientStreamRecvAnswersPingAndSkipsPong(t *testing.T) {
	c, d := dialFake(t, testClock())
	s, _ := attach(t, c, d, AttachRequest{Kind: KindProxy}, AttachResponse{SessionID: "s-1"})

	d.write(Message{Type: MsgPing})
	d.write(Message{Type: MsgPong})
	d.write(Message{Type: MsgCancel, SessionID: "s-1"})

	got, err := s.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got.Type != MsgCancel {
		t.Errorf("Recv = %+v, want the cancel: ping and pong must not surface", got)
	}
	if pong := d.readMessage(); pong.Type != MsgPong {
		t.Errorf("the daemon read %+v after its ping, want a pong", pong)
	}
}

func TestClientStreamRecvEndsOnDisconnect(t *testing.T) {
	c, d := dialFake(t, testClock())
	s, _ := attach(t, c, d, AttachRequest{Kind: KindPTY}, AttachResponse{SessionID: "s-1"})

	d.conn.Close()
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after the daemon went away = %v, want io.EOF", err)
	}
}

func TestClientStreamRecvEndsOnClose(t *testing.T) {
	c, d := dialFake(t, testClock())
	s, _ := attach(t, c, d, AttachRequest{Kind: KindPTY}, AttachResponse{SessionID: "s-1"})

	errs := make(chan error, 1)
	go func() {
		_, err := s.Recv()
		errs <- err
	}()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Recv after Close = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after Close")
	}
}

func TestClientStreamKeepalivePings(t *testing.T) {
	clk := testClock()
	c, d := dialFake(t, clk)
	s, _ := attach(t, c, d, AttachRequest{Kind: KindProxy}, AttachResponse{SessionID: "s-1"})
	defer s.Close()

	clk.BlockUntil(1)
	clk.Advance(PingInterval)
	if got := d.readMessage(); got.Type != MsgPing {
		t.Errorf("after %v the daemon read %+v, want a ping", PingInterval, got)
	}
}

func TestClientStreamKeepaliveDropsASilentDaemon(t *testing.T) {
	clk := testClock()
	c, d := dialFake(t, clk)
	s, _ := attach(t, c, d, AttachRequest{Kind: KindProxy}, AttachResponse{SessionID: "s-1"})

	// The first tick still pings; the second one falls PingTimeout after the
	// last message, which is what a daemon that stopped answering looks like.
	clk.BlockUntil(1)
	clk.Advance(PingInterval)
	if got := d.readMessage(); got.Type != MsgPing {
		t.Fatalf("the daemon read %+v, want a ping", got)
	}
	clk.BlockUntil(1)
	clk.Advance(PingInterval)

	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv on a stream the keepalive dropped = %v, want io.EOF", err)
	}
}

var _ ServerStream = (*Stream)(nil)
