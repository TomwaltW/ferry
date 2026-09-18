package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// fakeAgentBin is the ferry-fakeagent built once for the whole package. Every
// test drives that scripted agent through the proxy; no real agent is started.
var fakeAgentBin string

// wait is how long a test waits for something it expects. It is far longer than
// anything the fake agent takes, so a timeout means a real hang.
const wait = 15 * time.Second

// daemonToken is the token the fake daemon publishes in control.json. It has
// the 32 hex characters the real one has.
const daemonToken = "0123456789abcdef0123456789abcdef"

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func TestMain(m *testing.M) {
	// The SDK logs connection teardown through the default handler, and its own
	// SetLogger races with the receive goroutine.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	dir, err := os.MkdirTemp("", "ferry-proxy-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "proxy test: create a temporary directory:", err)
		os.Exit(1)
	}

	fakeAgentBin = filepath.Join(dir, "ferry-fakeagent"+exeSuffix())
	build := exec.Command("go", "build", "-o", fakeAgentBin, "github.com/TomwaltW/ferry/cmd/ferry-fakeagent")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "proxy test: build ferry-fakeagent: %v\n%s", err, out)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newHome writes a configuration whose only agent is the fake one, and returns
// the FERRY_HOME it lives in.
func newHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	cfg := config.Default()
	cfg.DefaultAgent = "fake"
	cfg.Workspace.Roots = []string{home}
	cfg.Agents = []config.AgentConfig{{
		Name:    "fake",
		Command: []string{fakeAgentBin},
		Env:     map[string]string{"FAKEAGENT_HOME": filepath.Join(home, "agent")},
	}}
	if err := config.Save(config.Path(home), cfg); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
	return home
}

// ---------- the fake daemon ----------

// daemon is the control server the proxy attaches to. It speaks the protocol of
// spec-p3 3.3 and nothing else: one attach per connection, then messages until
// the proxy goes away.
type daemon struct {
	ln    net.Listener
	conns chan *attachConn

	mu   sync.Mutex
	slot int
}

// startDaemon publishes a control.json in home and answers attach on it.
func startDaemon(t *testing.T, home string) *daemon {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d := &daemon{ln: ln, conns: make(chan *attachConn, 8)}

	ep := control.Endpoint{Port: ln.Addr().(*net.TCPAddr).Port, Token: daemonToken, PID: os.Getpid()}
	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("encode the endpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, control.SocketName), append(data, '\n'), 0o600); err != nil {
		t.Fatalf("publish control.json: %v", err)
	}

	go d.accept()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *daemon) accept() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

// serve answers one connection: the attach handshake, then the stream.
func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	enc := json.NewEncoder(conn)

	// A connection that says nothing is the proxy checking whether anybody is
	// home, which is how it decides what to print on stderr.
	if !sc.Scan() {
		return
	}
	var req control.Request
	if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
		return
	}
	if req.Token != daemonToken {
		return
	}
	if req.Op != control.OpAttach {
		_ = enc.Encode(control.Response{Error: control.ErrUnsupported})
		return
	}

	var ar control.AttachRequest
	if err := json.Unmarshal(req.Args, &ar); err != nil {
		_ = enc.Encode(control.Response{Error: control.ErrInternal})
		return
	}

	d.mu.Lock()
	d.slot++
	slot := d.slot
	d.mu.Unlock()

	resp := control.AttachResponse{
		SessionID: fmt.Sprintf("sess_fake%d", slot),
		Slot:      slot,
		Endpoint:  "fake:bot:owner",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if err := enc.Encode(control.Response{OK: true, Data: data}); err != nil {
		return
	}

	a := &attachConn{req: ar, resp: resp, conn: conn, enc: enc, msgs: make(chan control.Message, 256)}
	d.conns <- a

	for sc.Scan() {
		var m control.Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return
		}
		switch m.Type {
		case control.MsgPing:
			_ = a.send(control.Message{Type: control.MsgPong})
		case control.MsgPong:
		default:
			a.msgs <- m
		}
	}
	close(a.msgs)
}

// attached returns the next session that registered, failing the test if none
// does.
func (d *daemon) attached(t *testing.T) *attachConn {
	t.Helper()
	select {
	case a := <-d.conns:
		return a
	case <-time.After(wait):
		t.Fatal("timed out waiting for a session to attach")
		return nil
	}
}

// attachConn is one attached session on the daemon's side.
type attachConn struct {
	req  control.AttachRequest
	resp control.AttachResponse
	conn net.Conn
	msgs chan control.Message

	mu  sync.Mutex
	enc *json.Encoder
}

func (a *attachConn) send(m control.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enc.Encode(m)
}

// next returns the next message the proxy sent, failing the test on silence.
func (a *attachConn) next(t *testing.T) control.Message {
	t.Helper()
	select {
	case m, ok := <-a.msgs:
		if !ok {
			t.Fatal("the proxy closed the control connection")
		}
		return m
	case <-time.After(wait):
		t.Fatal("timed out waiting for a message from the proxy")
		return control.Message{}
	}
}

// nextOf returns the next message of one of the given types, failing the test
// if something else arrives first.
func (a *attachConn) nextOf(t *testing.T, types ...string) control.Message {
	t.Helper()
	m := a.next(t)
	for _, want := range types {
		if m.Type == want {
			return m
		}
	}
	t.Fatalf("expected one of %v, got %s", types, m.Type)
	return control.Message{}
}

// events collects mirrored events until the turn ends, the turn end included.
func (a *attachConn) events(t *testing.T) []fsp.Event {
	t.Helper()
	var out []fsp.Event
	for {
		m := a.nextOf(t, control.MsgEvent)
		if m.Event == nil {
			t.Fatal("an event message carried no event")
		}
		out = append(out, *m.Event)
		if m.Event.Kind == fsp.EvTurnEnd {
			return out
		}
	}
}

// ---------- the fake editor ----------

// editor is the acp.Client the proxy serves. It records what it was told and
// answers permission requests the way the test asked it to.
type editor struct {
	conn    *acp.ClientSideConnection
	updates chan acp.SessionNotification
	perms   chan acp.RequestPermissionRequest

	// answer decides permission requests; the default allows once.
	answer func(acp.RequestPermissionRequest) acp.RequestPermissionResponse
}

func newEditor() *editor {
	return &editor{
		updates: make(chan acp.SessionNotification, 512),
		perms:   make(chan acp.RequestPermissionRequest, 16),
		answer: func(acp.RequestPermissionRequest) acp.RequestPermissionResponse {
			return acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeSelected("allow-once"),
			}
		},
	}
}

func (e *editor) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	e.updates <- params
	return nil
}

func (e *editor) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	e.perms <- params
	return e.answer(params), nil
}

func (e *editor) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (e *editor) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (e *editor) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (e *editor) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}

func (e *editor) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (e *editor) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}

func (e *editor) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}

// said returns the agent text the editor has been shown since the last call.
// Every notification of a finished turn has arrived by the time its response
// has, so draining without blocking is enough.
func (e *editor) said() string {
	var b strings.Builder
	for {
		select {
		case u := <-e.updates:
			if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
				b.WriteString(u.Update.AgentMessageChunk.Content.Text.Text)
			}
		default:
			return b.String()
		}
	}
}

// waitUserChunk returns the next user message chunk the proxy showed the
// editor, which is how an injected prompt appears there.
func (e *editor) waitUserChunk(t *testing.T) string {
	t.Helper()
	for {
		select {
		case u := <-e.updates:
			if u.Update.UserMessageChunk != nil && u.Update.UserMessageChunk.Content.Text != nil {
				return u.Update.UserMessageChunk.Content.Text.Text
			}
		case <-time.After(wait):
			t.Fatal("timed out waiting for a user message chunk")
			return ""
		}
	}
}

// waitAgentChunk waits until the editor has been shown some agent text, which
// is how a test knows a turn is under way.
func (e *editor) waitAgentChunk(t *testing.T) string {
	t.Helper()
	for {
		select {
		case u := <-e.updates:
			if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
				return u.Update.AgentMessageChunk.Content.Text.Text
			}
		case <-time.After(wait):
			t.Fatal("timed out waiting for agent output")
			return ""
		}
	}
}

// ---------- the run under test ----------

// run is one proxy process: the pipes it was started on, the editor at the
// other end, and the error it finally returned.
type run struct {
	editor   *editor
	stderr   *syncBuffer
	done     chan error
	toProxy  *os.File
	closeOne sync.Once

	waitOnce sync.Once
	err      error
}

// startRun launches Run on a pair of pipes and connects a fake editor to it.
func startRun(t *testing.T, home string, opts Options) *run {
	t.Helper()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	opts.Agent = "fake"
	opts.Home = home
	opts.In = inR
	opts.Out = outW
	stderr := &syncBuffer{}
	opts.Err = stderr
	opts.Log = slog.New(slog.DiscardHandler)

	r := &run{editor: newEditor(), stderr: stderr, done: make(chan error, 1), toProxy: inW}
	go func() {
		err := Run(context.Background(), opts)
		inR.Close()
		outW.Close()
		r.done <- err
	}()

	r.editor.conn = acp.NewClientSideConnection(r.editor, inW, outR)

	t.Cleanup(func() {
		r.disconnect()
		r.wait(t)
		outR.Close()
	})
	return r
}

// wait returns what Run returned, once. Both a test and the cleanup after it
// may ask.
func (r *run) wait(t *testing.T) error {
	t.Helper()
	r.waitOnce.Do(func() {
		select {
		case r.err = <-r.done:
		case <-time.After(wait):
			t.Error("the proxy did not return after the editor disconnected")
		}
	})
	return r.err
}

// disconnect closes the editor's end of the proxy's input, which is what an
// editor that goes away looks like.
func (r *run) disconnect() {
	r.closeOne.Do(func() { r.toProxy.Close() })
}

// initialize and newSession are the two calls every test starts with.
func (r *run) initialize(t *testing.T) acp.InitializeResponse {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), wait)
	defer stop()

	resp, err := r.editor.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "fake-editor", Version: "test"},
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return resp
}

func (r *run) newSession(t *testing.T) acp.SessionId {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), wait)
	defer stop()

	resp, err := r.editor.conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return resp.SessionId
}

// prompt runs one editor turn and returns how it ended.
func (r *run) prompt(t *testing.T, id acp.SessionId, text string) acp.PromptResponse {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), wait)
	defer stop()

	resp, err := r.editor.conn.Prompt(ctx, acp.PromptRequest{
		SessionId: id,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})
	if err != nil {
		t.Fatalf("session/prompt %q: %v", text, err)
	}
	return resp
}

// syncBuffer is an io.Writer a test can read while the proxy writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*syncBuffer)(nil)

// waitType returns the next message of the given type, skipping anything that
// arrives before it.
func (a *attachConn) waitType(t *testing.T, typ string) control.Message {
	t.Helper()
	for {
		m := a.next(t)
		if m.Type == typ {
			return m
		}
	}
}

// chunkText joins the text of the chunk events in a mirrored turn, leaving out
// the marker the proxy sends when the editor starts one.
func chunkText(events []fsp.Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == fsp.EvChunk && e.Text != editorTurnMarker {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}
