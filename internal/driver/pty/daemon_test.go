package pty

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
)

// msgWait is how long a test waits for a message before giving up.
const msgWait = 20 * time.Second

// runIdle is the quiet period the wrapped program is given in the tests. It is
// short so a test does not sit through the production 1.5 seconds, and long
// enough that a shell echoing a line does not look like two turns.
//
// Windows gets the production value instead. A ConPTY round trip — inject, the
// child reads, the child writes, copyOut reads — regularly takes longer than
// 150ms on the CI runners, and a turn that ends before the output arrives makes
// the test see turn_end where it wants the chunk.
var runIdle = func() time.Duration {
	if runtime.GOOS == "windows" {
		return DefaultIdle
	}
	return 150 * time.Millisecond
}()

// fakeDaemon is the control socket half of the daemon: enough of the protocol of
// spec-p3 3.3 for ferry run to attach and then exchange messages. It lives here
// rather than in testkit because nothing else needs it.
type fakeDaemon struct {
	t    *testing.T
	home string
	ln   net.Listener

	attached chan control.AttachRequest
	msgs     chan control.Message

	mu   sync.Mutex
	conn net.Conn
	enc  *json.Encoder
}

func startDaemon(t *testing.T) *fakeDaemon {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	home := t.TempDir()

	d := &fakeDaemon{
		t:        t,
		home:     home,
		ln:       ln,
		attached: make(chan control.AttachRequest, 4),
		msgs:     make(chan control.Message, 64),
	}

	endpoint := control.Endpoint{
		Port:  ln.Addr().(*net.TCPAddr).Port,
		Token: strings.Repeat("a", 32),
		PID:   os.Getpid(),
	}
	data, err := json.Marshal(endpoint)
	if err != nil {
		t.Fatalf("encode the endpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, control.SocketName), data, 0o600); err != nil {
		t.Fatalf("write %s: %v", control.SocketName, err)
	}

	go d.serve()
	t.Cleanup(func() {
		ln.Close()
		d.mu.Lock()
		if d.conn != nil {
			d.conn.Close()
		}
		d.mu.Unlock()
	})
	return d
}

// serve answers one attach and then reads the messages of that session.
func (d *fakeDaemon) serve() {
	conn, err := d.ln.Accept()
	if err != nil {
		return
	}
	d.mu.Lock()
	d.conn, d.enc = conn, json.NewEncoder(conn)
	d.mu.Unlock()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)

	// The first line is the attach request; its answer is the last
	// request/response pair on this connection.
	if !scanner.Scan() {
		return
	}
	var req control.Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || req.Op != control.OpAttach {
		return
	}
	var attach control.AttachRequest
	if err := json.Unmarshal(req.Args, &attach); err != nil {
		return
	}

	payload, err := json.Marshal(control.AttachResponse{SessionID: "sess-pty", Slot: 1, Endpoint: "fake"})
	if err != nil {
		return
	}
	if err := d.write(control.Response{OK: true, Data: payload}); err != nil {
		return
	}
	d.attached <- attach

	for scanner.Scan() {
		var m control.Message
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case control.MsgPing:
			_ = d.send(control.Message{Type: control.MsgPong})
		case control.MsgPong:
		default:
			d.msgs <- m
		}
	}
}

func (d *fakeDaemon) write(v any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.enc == nil {
		return io.ErrClosedPipe
	}
	return d.enc.Encode(v)
}

func (d *fakeDaemon) send(m control.Message) error { return d.write(m) }

// waitAttach returns the attach request, failing the test if none arrives.
func (d *fakeDaemon) waitAttach() control.AttachRequest {
	d.t.Helper()
	select {
	case a := <-d.attached:
		return a
	case <-time.After(msgWait):
		d.t.Fatal("ferry run never attached")
		return control.AttachRequest{}
	}
}

// waitMsg returns the next message of the given type, skipping the others.
func (d *fakeDaemon) waitMsg(kind string) control.Message {
	d.t.Helper()
	deadline := time.After(msgWait)
	for {
		select {
		case m := <-d.msgs:
			if m.Type == kind {
				return m
			}
		case <-deadline:
			d.t.Fatalf("timed out waiting for a %s message", kind)
			return control.Message{}
		}
	}
}

// buffer is an io.Writer a test can read from while the runner writes to it.
type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *buffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *buffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
