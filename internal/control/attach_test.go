package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// Attach records the external sessions the fake core was asked to register and
// hands the stream to whoever is waiting for it.
func (f *fakeHandler) Attach(_ context.Context, req AttachRequest, s ServerStream) (AttachResponse, error) {
	f.mu.Lock()
	f.attaches = append(f.attaches, req)
	resp, err := f.attach, f.attachErr
	f.mu.Unlock()
	if err != nil {
		return AttachResponse{}, err
	}
	select {
	case f.streams <- s:
	default:
	}
	return resp, nil
}

func (f *fakeHandler) attachCalls() []AttachRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AttachRequest(nil), f.attaches...)
}

// stream waits for the stream of the session the daemon has just registered.
func (f *fakeHandler) stream(t *testing.T) ServerStream {
	t.Helper()
	select {
	case s := <-f.streams:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("Attach was never called")
		return nil
	}
}

// serveClock is serve with a clock of the test's own, so that the keepalive of
// an attached stream moves only when the test says so.
func serveClock(t *testing.T, h Handler, clk clock.Clock) string {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeWith(ctx, ServeOptions{
			Home: home, Handler: h, Clock: clk, Log: slog.New(slog.DiscardHandler),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ServeWith returned %v, want nil after the context ended", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ServeWith did not return after the context was cancelled")
		}
	})

	path := filepath.Join(home, SocketName)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return home
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAttachStream walks one external session end to end: the handshake, a
// frame each way, and the end of stream when the external process goes away.
func TestAttachStream(t *testing.T) {
	want := AttachResponse{SessionID: "sess_x", Slot: 2, Endpoint: "wechat:bot:peer"}
	h := &fakeHandler{attach: want, streams: make(chan ServerStream, 1)}
	home := serveClock(t, h, clock.NewFake(attachEpoch))

	c, err := Dial(home)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	req := AttachRequest{
		Kind: KindProxy, Agent: "demo", Cwd: "/tmp/work", PID: 4242,
		AllowInject: true, RemoteApprove: true,
	}
	stream, resp, err := c.Attach(context.Background(), req)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if resp != want {
		t.Errorf("Attach returned %+v, want %+v", resp, want)
	}
	if got := h.attachCalls(); len(got) != 1 || got[0] != req {
		t.Fatalf("the daemon saw %+v, want one %+v", got, req)
	}
	server := h.stream(t)

	// External process to daemon.
	ev := &fsp.Event{Kind: fsp.EvChunk, Text: "working"}
	if err := stream.Send(Message{Type: MsgEvent, SessionID: want.SessionID, Event: ev}); err != nil {
		t.Fatalf("send an event: %v", err)
	}
	got, err := server.Recv()
	if err != nil {
		t.Fatalf("the daemon failed to read the event: %v", err)
	}
	if got.Type != MsgEvent || got.SessionID != want.SessionID || got.Event == nil || got.Event.Text != "working" {
		t.Errorf("the daemon read %+v, want the chunk event", got)
	}

	// Daemon to external process.
	if err := server.Send(Message{Type: MsgPrompt, SessionID: want.SessionID, Text: "go on", Sender: "owner-1"}); err != nil {
		t.Fatalf("send a prompt: %v", err)
	}
	back, err := stream.Recv()
	if err != nil {
		t.Fatalf("the external process failed to read the prompt: %v", err)
	}
	if back.Type != MsgPrompt || back.Text != "go on" || back.Sender != "owner-1" {
		t.Errorf("the external process read %+v, want the prompt", back)
	}

	// The external process goes away; the daemon's side ends cleanly.
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := server.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("Recv after the external process left = %v, want io.EOF", err)
	}
}

func TestAttachRejectsAnUnknownKind(t *testing.T) {
	h := &fakeHandler{streams: make(chan ServerStream, 1)}
	home := serveClock(t, h, clock.NewFake(attachEpoch))
	c, err := Dial(home)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	_, _, err = c.Attach(context.Background(), AttachRequest{Kind: "telegram"})
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Fatalf("Attach with an unknown kind = %v, want fsp.ErrUnsupported", err)
	}
	if got := h.attachCalls(); len(got) != 0 {
		t.Errorf("the daemon was asked to register %+v, want nothing", got)
	}
}

// TestPingTimeout: an external process that stops answering is dropped after
// PingTimeout. The peer here is a raw socket rather than a Client, because a
// Client would answer the ping and never time out.
func TestPingTimeout(t *testing.T) {
	clk := clock.NewFake(attachEpoch)
	h := &fakeHandler{attach: AttachResponse{SessionID: "sess_q", Slot: 1}, streams: make(chan ServerStream, 1)}
	home := serveClock(t, h, clk)

	conn, dec := rawAttach(t, home, AttachRequest{Kind: KindPTY, Agent: "top", Cwd: "/tmp"})
	server := h.stream(t)

	// The first interval only earns a ping, which the silent peer ignores.
	clk.BlockUntil(1)
	clk.Advance(PingInterval)
	if got := readMessage(t, dec); got.Type != MsgPing {
		t.Fatalf("the daemon sent %+v, want a ping", got)
	}

	// The second takes the stream past PingTimeout with nothing heard back.
	clk.BlockUntil(1)
	clk.Advance(PingInterval)

	if _, err := server.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("Recv on a stream that timed out = %v, want io.EOF", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if dec.Scan() {
		t.Errorf("the connection is still open, it carried %q", dec.Bytes())
	}
}

var attachEpoch = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

// rawAttach performs the handshake over a bare socket and returns the
// connection along with a scanner positioned after the response.
func rawAttach(t *testing.T, home string, req AttachRequest) (net.Conn, *bufio.Scanner) {
	t.Helper()
	ep := readEndpoint(t, home)
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)))
	if err != nil {
		t.Fatalf("dial the daemon: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	args, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode the attach request: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(Request{Token: ep.Token, Op: OpAttach, Args: args}); err != nil {
		t.Fatalf("send the attach request: %v", err)
	}
	dec := bufio.NewScanner(conn)
	dec.Buffer(make([]byte, 0, 4096), maxLine)
	if !dec.Scan() {
		t.Fatalf("no answer to attach: %v", dec.Err())
	}
	var resp Response
	if err := json.Unmarshal(dec.Bytes(), &resp); err != nil {
		t.Fatalf("parse the answer to attach: %v", err)
	}
	if !resp.OK {
		t.Fatalf("attach was refused with %q", resp.Error)
	}
	return conn, dec
}

func readMessage(t *testing.T, dec *bufio.Scanner) Message {
	t.Helper()
	if !dec.Scan() {
		t.Fatalf("no message: %v", dec.Err())
	}
	var m Message
	if err := json.Unmarshal(dec.Bytes(), &m); err != nil {
		t.Fatalf("parse %q: %v", dec.Bytes(), err)
	}
	return m
}
