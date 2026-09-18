//go:build linux || darwin

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// msgWait is how long a test waits for a message before giving up.
const msgWait = 20 * time.Second

// runIdle is the quiet period the wrapped program is given in the tests. It is
// short so a test does not sit through the production 1.5 seconds, and long
// enough that a shell echoing a line does not look like two turns.
const runIdle = 150 * time.Millisecond

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

// echoShell is a shell that answers every line it reads and stops on "quit". It
// stands in for an agent that only offers a terminal.
const echoShell = `while IFS= read -r line; do echo "got:$line"; if [ "$line" = quit ]; then exit 0; fi; done`

// TestRunEchoInjection is the whole of ferry run: the session registers with the
// daemon, a prompt from chat is typed into the program, and what the program says
// comes back as one turn.
func TestRunEchoInjection(t *testing.T) {
	d := startDaemon(t)

	keys, typed := io.Pipe()
	out := &buffer{}

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := Run(t.Context(), Options{
			Name:    "echo-shell",
			Idle:    runIdle,
			Command: []string{"sh", "-c", echoShell},
			Home:    d.home,
			In:      keys,
			Out:     out,
			Log:     testLogger(),
		})
		done <- result{code, err}
	}()

	// The session registers as a pseudo terminal that takes input from chat but
	// cannot relay approvals.
	attach := d.waitAttach()
	if attach.Kind != control.KindPTY {
		t.Errorf("Kind = %q, want %q", attach.Kind, control.KindPTY)
	}
	if attach.Agent != "echo-shell" {
		t.Errorf("Agent = %q, want the name given on the command line", attach.Agent)
	}
	if !attach.AllowInject {
		t.Error("AllowInject is false, but a wrapped program does take typed input")
	}
	if attach.RemoteApprove {
		t.Error("RemoteApprove is true, but a wrapped program has no approvals to relay")
	}
	if attach.PID <= 0 {
		t.Errorf("PID = %d, want the pid of the wrapped program", attach.PID)
	}
	if attach.Cwd == "" {
		t.Error("Cwd is empty")
	}

	// A prompt from chat is typed in, and the answer comes back as a chunk and
	// the end of a turn.
	if err := d.send(control.Message{Type: control.MsgPrompt, SessionID: "sess-pty", Text: "hello", Sender: "owner"}); err != nil {
		t.Fatalf("send the prompt: %v", err)
	}

	chunk := d.waitMsg(control.MsgEvent)
	if chunk.Event == nil || chunk.Event.Kind != fsp.EvChunk {
		t.Fatalf("the first event is %+v, want a chunk", chunk.Event)
	}
	if !strings.Contains(chunk.Event.Text, "got:hello") {
		t.Errorf("the chunk says %q, want it to contain %q", chunk.Event.Text, "got:hello")
	}
	if strings.Contains(chunk.Event.Text, PasteStart) || strings.Contains(chunk.Event.Text, "\x1b") {
		t.Errorf("the chunk still has terminal escapes in it: %q", chunk.Event.Text)
	}
	if chunk.SessionID != "sess-pty" {
		t.Errorf("SessionID = %q, want the one the daemon handed out", chunk.SessionID)
	}

	end := d.waitMsg(control.MsgEvent)
	if end.Event == nil || end.Event.Kind != fsp.EvTurnEnd {
		t.Fatalf("the second event is %+v, want the end of the turn", end.Event)
	}
	if end.Event.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", end.Event.StopReason)
	}

	// What the user types still reaches the program, and the program's output
	// still reaches the terminal.
	if _, err := typed.Write([]byte("quit\n")); err != nil {
		t.Fatalf("type into the program: %v", err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run returned %v", got.err)
		}
		if got.code != 0 {
			t.Errorf("exit code = %d, want 0", got.code)
		}
	case <-time.After(msgWait):
		t.Fatal("Run did not return after the program exited")
	}

	// The terminal sees the raw stream, paste markers and all: forwarding is
	// byte for byte, and only what goes to chat is cleaned up.
	transcript := stripANSI(out.String())
	if !strings.Contains(transcript, "got:hello") {
		t.Errorf("the terminal never saw the answer: %q", out.String())
	}
	if !strings.Contains(transcript, "got:quit") {
		t.Errorf("the terminal never saw what the user typed: %q", out.String())
	}
	if d.waitMsg(control.MsgDetach).SessionID != "sess-pty" {
		t.Error("the daemon was not told the session ended")
	}
	_ = typed.Close()
}

// TestExitCodePropagated pins the last line of spec-p3 3.8: ferry run ends the
// way the program it wrapped ended. There is no daemon here, which is also the
// case of a wrapper that only wraps.
func TestExitCodePropagated(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		want    int
		wantErr bool
	}{
		{name: "a plain exit code", script: "exit 7", want: 7},
		{name: "success", script: "exit 0", want: 0},
		// A program killed by a signal has no code of its own; the shell
		// convention of 128 plus the signal is what is reported.
		{name: "killed by a signal", script: "kill -TERM $$; sleep 5", want: 128 + 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, err := Run(t.Context(), Options{
				Idle:    runIdle,
				Command: []string{"sh", "-c", c.script},
				Home:    t.TempDir(),
				Out:     io.Discard,
				Log:     testLogger(),
			})
			if err != nil {
				t.Fatalf("Run returned %v", err)
			}
			if code != c.want {
				t.Errorf("exit code = %d, want %d", code, c.want)
			}
		})
	}

	// A command that is not there is a failure of ferry run itself, not an exit
	// code to pass on.
	if _, err := Run(t.Context(), Options{
		Command: []string{"ferry-no-such-program"},
		Home:    t.TempDir(),
		Out:     io.Discard,
		Log:     testLogger(),
	}); err == nil {
		t.Error("Run of a missing command returned no error")
	}

	// An empty command line is caught before anything is started.
	if _, err := Run(t.Context(), Options{Home: t.TempDir()}); err == nil {
		t.Error("Run with no command returned no error")
	}
}
