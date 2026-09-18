package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/driver/claudechannel"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/im/feishu/feishutest"
	"github.com/TomwaltW/ferry/internal/mcpmini"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeagent"
)

// The Feishu app and its owner in the fake client.
const (
	feishuApp   = "cli_e2e_app"
	feishuOwner = "ou_e2e_owner"
)

// openWindow has the WeChat owner say something first. WeChat only lets the
// bot answer inside the window an inbound message opens, so a session attached
// from the computer is announced once the owner has written, as in real use.
func (r *rig) openWindow() {
	r.t.Helper()
	mark := r.markOwner()
	r.send("/ls")
	r.waitOwnerTextFrom(mark, r.tr("hint.no_session", nil))
}

// subprocWait bounds everything a test waits for from a ferry subprocess: it
// builds nothing, but it does start an agent of its own.
const subprocWait = 15 * time.Second

// --- Feishu ---------------------------------------------------------------

// withFeishu enables the Feishu entry point on the fake client.
func withFeishu() rigOption {
	return func(r *rig) {
		r.fs = feishutest.New()
		r.cfg.Feishu = config.FeishuConfig{Enabled: true, AppID: feishuApp, OwnerOpenID: feishuOwner}
	}
}

var feishuSeq struct {
	sync.Mutex
	n int
}

// feishuSend is the Feishu owner writing to the bot in a direct chat.
func (r *rig) feishuSend(text string) {
	feishuSeq.Lock()
	feishuSeq.n++
	id := fmt.Sprintf("om_e2e_%d", feishuSeq.n)
	feishuSeq.Unlock()
	r.fs.Deliver(feishu.Message{
		MessageID: id, ChatType: feishu.ChatTypeP2P, OpenID: feishuOwner, Text: text, CreateTime: time.Now(),
	})
}

// feishuTexts is every text the bot sent to the Feishu owner, in order.
func (r *rig) feishuTexts() []string {
	var out []string
	for _, s := range r.fs.Sent() {
		if s.Kind == feishutest.KindText && s.OpenID == feishuOwner {
			out = append(out, s.Text)
		}
	}
	return out
}

func (r *rig) waitFeishuFrom(from int, want string) string {
	r.t.Helper()
	var hit string
	r.waitFor(fmt.Sprintf("a Feishu message after #%d containing %q", from, want), func() bool {
		msgs := r.feishuTexts()
		for i := from; i < len(msgs); i++ {
			if strings.Contains(msgs[i], want) {
				hit = msgs[i]
				return true
			}
		}
		return false
	})
	return hit
}

// B1: Feishu serves a session end to end while WeChat keeps working, and the
// two entry points never see each other's sessions.
func TestFeishuFake(t *testing.T) {
	r := newRig(t, withFeishu())
	demo := filepath.Join(r.ws, "demo")
	other := filepath.Join(r.ws, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	r.feishuSend("/new " + agentName + " " + demo)
	r.waitFeishuFrom(0, "会话 1")
	mark := len(r.feishuTexts())
	r.feishuSend("echo hi")
	r.waitFeishuFrom(mark, r.tr("turn.accepted", map[string]any{"Agent": agentName, "Slot": 1}))
	r.waitFeishuFrom(mark, "hi")

	// WeChat, at the same time, runs a session of its own.
	r.send("/new " + agentName + " " + other)
	r.waitOwnerText("会话 1")
	wx := r.markOwner()
	r.send("echo from wechat")
	r.waitOwnerTextFrom(wx, "from wechat")

	// Each /ls lists its own entry point's session and nothing else.
	mark = len(r.feishuTexts())
	r.feishuSend("/ls")
	fsList := r.waitFeishuFrom(mark, r.tr("session.list_header", nil))
	if !strings.Contains(fsList, demo) || strings.Contains(fsList, other) {
		t.Errorf("Feishu /ls should list %s and not %s:\n%s", demo, other, fsList)
	}
	wx = r.markOwner()
	r.send("/ls")
	wxList := r.waitOwnerTextFrom(wx, r.tr("session.list_header", nil))
	if !strings.Contains(wxList, other) || strings.Contains(wxList, demo) {
		t.Errorf("WeChat /ls should list %s and not %s:\n%s", other, demo, wxList)
	}

	// Neither conversation leaked into the other chat.
	for _, m := range r.sentTo(ownerID) {
		if strings.Contains(m, demo) {
			t.Errorf("WeChat was told about the Feishu session:\n%s", m)
		}
	}
	for _, m := range r.feishuTexts() {
		if strings.Contains(m, "from wechat") || strings.Contains(m, other) {
			t.Errorf("Feishu was told about the WeChat session:\n%s", m)
		}
	}
}

// B2: an approval raised in a Feishu session is asked and decided in Feishu.
func TestFeishuApproval(t *testing.T) {
	r := newRig(t, withFeishu())
	r.feishuSend("/new " + agentName + " " + filepath.Join(r.ws, "demo"))
	r.waitFeishuFrom(0, "会话 1")

	mark := len(r.feishuTexts())
	r.feishuSend("ask execute " + filepath.Join(r.ws, "demo", "run.sh"))
	req := r.waitFeishuFrom(mark, r.approvalMark())
	id := r.approvalIDFrom(req)

	mark = len(r.feishuTexts())
	r.feishuSend("y " + id)
	r.waitFeishuFrom(mark, "allowed")

	for _, m := range r.sentTo(ownerID) {
		if strings.Contains(m, id) {
			t.Errorf("WeChat saw the Feishu approval:\n%s", m)
		}
	}
}

// --- ferry proxy ----------------------------------------------------------

// editor is the ACP client an editor would be: it records what the proxy shows
// it and answers permission requests by allowing once.
type editor struct {
	conn    *acpsdk.ClientSideConnection
	updates chan acpsdk.SessionNotification
	perms   chan acpsdk.RequestPermissionRequest
}

func (e *editor) SessionUpdate(ctx context.Context, p acpsdk.SessionNotification) error {
	e.updates <- p
	return nil
}

func (e *editor) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	e.perms <- p
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow-once")}, nil
}

func (e *editor) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, acpsdk.NewMethodNotFound("fs/read_text_file")
}

func (e *editor) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, acpsdk.NewMethodNotFound("fs/write_text_file")
}

func (e *editor) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/create")
}

func (e *editor) KillTerminal(ctx context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/kill")
}

func (e *editor) TerminalOutput(ctx context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, acpsdk.NewMethodNotFound("terminal/output")
}

func (e *editor) ReleaseTerminal(ctx context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/release")
}

func (e *editor) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, acpsdk.NewMethodNotFound("terminal/wait_for_exit")
}

// agentText drains what the agent has said to the editor so far.
func (e *editor) agentText() string {
	var b strings.Builder
	for {
		select {
		case u := <-e.updates:
			if c := u.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
				b.WriteString(c.Content.Text.Text)
			}
		default:
			return b.String()
		}
	}
}

// waitUserChunk returns the next user message the proxy showed the editor,
// which is how input typed in chat appears there.
func (e *editor) waitUserChunk(t *testing.T) string {
	t.Helper()
	deadline := time.After(subprocWait)
	for {
		select {
		case u := <-e.updates:
			if c := u.Update.UserMessageChunk; c != nil && c.Content.Text != nil {
				return c.Content.Text.Text
			}
		case <-deadline:
			t.Fatal("timed out waiting for a user message chunk in the editor")
			return ""
		}
	}
}

// waitAgentText waits until the agent's streamed text contains want.
func (e *editor) waitAgentText(t *testing.T, want string) {
	t.Helper()
	var seen strings.Builder
	deadline := time.After(subprocWait)
	for {
		select {
		case u := <-e.updates:
			if c := u.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
				seen.WriteString(c.Content.Text.Text)
				if strings.Contains(seen.String(), want) {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the editor to be shown %q; it saw %q", want, seen.String())
		}
	}
}

// startProxy runs `ferry proxy fake` as an editor would and connects to it.
func (r *rig) startProxy(flags ...string) *editor {
	t := r.t
	t.Helper()
	r.openWindow()
	args := append([]string{"proxy"}, flags...)
	cmd := exec.Command(ferryBin, append(args, agentName)...)
	cmd.Env = append(os.Environ(), "FERRY_HOME="+r.home)
	cmd.Stderr = testWriter{t}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ferry proxy: %v", err)
	}
	t.Cleanup(func() {
		// The editor going away is what ends a proxy.
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(subprocWait):
			cmd.Process.Kill()
			t.Error("ferry proxy did not exit after the editor disconnected")
		}
	})

	e := &editor{
		updates: make(chan acpsdk.SessionNotification, 512),
		perms:   make(chan acpsdk.RequestPermissionRequest, 16),
	}
	e.conn = acpsdk.NewClientSideConnection(e, in, out)

	ctx, stop := context.WithTimeout(context.Background(), subprocWait)
	defer stop()
	if _, err := e.conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientInfo:         &acpsdk.Implementation{Name: "e2e-editor", Version: "test"},
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatalf("initialize through ferry proxy: %v", err)
	}
	return e
}

// newSession opens an editor session on <ws>/demo and waits for chat to be
// told about it.
func (e *editor) newSession(r *rig) acpsdk.SessionId {
	r.t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), subprocWait)
	defer stop()
	cwd := filepath.Join(r.ws, "demo")
	resp, err := e.conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		r.t.Fatalf("session/new through ferry proxy: %v", err)
	}
	r.waitOwnerText(r.tr("proxy.attached", map[string]any{"Agent": agentName, "Cwd": cwd, "Slot": 1}))
	return resp.SessionId
}

// prompt starts one editor turn and returns where its outcome will arrive.
func (e *editor) prompt(id acpsdk.SessionId, text string) <-chan error {
	done := make(chan error, 1)
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), subprocWait)
		defer stop()
		resp, err := e.conn.Prompt(ctx, acpsdk.PromptRequest{SessionId: id, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock(text)}})
		if err == nil && resp.StopReason != acpsdk.StopReasonEndTurn {
			err = fmt.Errorf("stop reason %q", resp.StopReason)
		}
		done <- err
	}()
	return done
}

func awaitTurn(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("editor turn: %v", err)
		}
	case <-time.After(subprocWait):
		t.Fatal("the editor turn did not finish")
	}
}

// B3: an editor drives the agent through ferry proxy, and WeChat watches.
func TestProxyMirror(t *testing.T) {
	r := newRig(t)
	e := r.startProxy()
	id := e.newSession(r)

	mark := r.markOwner()
	done := e.prompt(id, "echo hi")
	e.waitAgentText(t, "hi")
	awaitTurn(t, done)

	r.waitOwnerTextFrom(mark, "hi")
	r.waitOwnerTextFrom(mark, r.tr("proxy.editor_turn", nil))
	// Both waits above still pass when the marker and the agent's first chunk
	// arrive glued together, which is the shape B3 regressed on. The "hi" here
	// is the agent's first chunk from the prompt above: change that prompt and
	// this refutation stops watching the newline while still passing.
	r.refuteFrom(ownerID, mark, r.tr("proxy.editor_turn", nil)+"hi", "the turn marker ran into the agent text")
}

// B4: with --allow-inject, WeChat input reaches an idle session and the editor
// sees it as a user message followed by the agent's answer.
func TestProxyInject(t *testing.T) {
	r := newRig(t)
	e := r.startProxy("--allow-inject")
	e.newSession(r)

	r.send("echo x")
	if got := e.waitUserChunk(t); got != "echo x" {
		t.Errorf("the editor was shown %q as the injected input, want %q", got, "echo x")
	}
	e.waitAgentText(t, "x")
}

// B5: by default a permission request goes to the editor only and WeChat is
// told it is waiting there; with --remote-approve it goes to WeChat only.
func TestProxyApprovalModes(t *testing.T) {
	t.Run("editor", func(t *testing.T) {
		r := newRig(t)
		e := r.startProxy()
		id := e.newSession(r)

		script := filepath.Join(r.ws, "demo", "run.sh")
		mark := r.markOwner()
		done := e.prompt(id, "ask execute "+script)
		select {
		case <-e.perms:
		case <-time.After(subprocWait):
			t.Fatal("the editor was never asked for permission")
		}
		awaitTurn(t, done)
		if got := e.agentText(); !strings.Contains(got, "allowed") {
			t.Errorf("the agent told the editor %q, want it to contain %q", got, "allowed")
		}

		// An empty Tool renders a prefix that matches whatever the agent asked
		// about, so the notice is checked against the tool name the agent sent.
		notice := r.tr("proxy.editor_approval", map[string]any{"Tool": "ask " + script})
		r.waitOwnerTextFrom(mark, notice)
		r.refuteFrom(ownerID, mark, r.approvalMark(), "the editor holds the approval")
	})

	t.Run("remote", func(t *testing.T) {
		r := newRig(t)
		e := r.startProxy("--remote-approve")
		id := e.newSession(r)

		mark := r.markOwner()
		done := e.prompt(id, "ask execute "+filepath.Join(r.ws, "demo", "run.sh"))
		_, apID := r.waitApproval(mark)
		r.send("y " + apID)
		awaitTurn(t, done)

		if got := e.agentText(); !strings.Contains(got, "allowed") {
			t.Errorf("the agent told the editor %q, want it to contain %q", got, "allowed")
		}
		select {
		case p := <-e.perms:
			t.Errorf("the editor was asked for permission despite --remote-approve: %+v", p)
		default:
		}
	})
}

// --- Claude Code channel --------------------------------------------------

// notice is one notification the shim sent to its host.
type notice struct {
	method string
	params json.RawMessage
}

// B6: the test plays Claude Code and runs ferry channel. WeChat messages arrive
// as channel events, the reply tool answers in WeChat, and a permission
// request is decided in WeChat under the ID Claude Code chose.
func TestChannelShim(t *testing.T) {
	r := newRig(t)
	r.openWindow()

	notes := make(chan notice, 64)
	cli := mcpmini.NewClient(mcpmini.ClientOptions{
		Info:    mcpmini.Info{Name: "claude-code-e2e", Version: "test"},
		Command: []string{ferryBin, "channel"},
		Env:     map[string]string{"FERRY_HOME": r.home},
		OnNotify: func(_ context.Context, method string, params json.RawMessage) {
			notes <- notice{method, append(json.RawMessage(nil), params...)}
		},
		Capabilities: mcpmini.Capabilities{Experimental: map[string]any{claudechannel.CapChannel: map[string]any{}}},
	})
	ctx, stop := context.WithTimeout(context.Background(), 4*subprocWait)
	defer stop()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("start ferry channel: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	caps := cli.Server().Capabilities.Experimental
	if _, ok := caps[claudechannel.CapChannelPermission]; !ok {
		t.Errorf("with a daemon running the shim should declare %s; it declared %v", claudechannel.CapChannelPermission, caps)
	}
	r.waitOwnerText(r.tr("channel.attached", map[string]any{"Slot": 1}))

	next := func(method string) json.RawMessage {
		t.Helper()
		for {
			select {
			case n := <-notes:
				if n.method == method {
					return n.params
				}
			case <-time.After(subprocWait):
				t.Fatalf("timed out waiting for %s", method)
				return nil
			}
		}
	}

	// Inbound: a WeChat message becomes a channel event.
	r.send("hello claude")
	var ev struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	if err := json.Unmarshal(next(claudechannel.NotifyChannel), &ev); err != nil {
		t.Fatalf("decode the channel event: %v", err)
	}
	if ev.Content != "hello claude" {
		t.Errorf("channel content = %q, want %q", ev.Content, "hello claude")
	}
	metaKey := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	for k := range ev.Meta {
		if !metaKey.MatchString(k) {
			t.Errorf("meta key %q has characters Claude Code drops", k)
		}
	}
	chatID := ev.Meta[claudechannel.MetaChatID]
	if chatID == "" {
		t.Fatalf("the channel event carried no chat_id: %v", ev.Meta)
	}

	// Outbound: the reply tool answers in WeChat.
	mark := r.markOwner()
	res, err := cli.CallTool(ctx, claudechannel.ToolReply, map[string]any{"chat_id": chatID, "text": "pong from claude"})
	if err != nil || res.IsError {
		t.Fatalf("reply tool: err=%v result=%+v", err, res)
	}
	r.waitOwnerTextFrom(mark, "pong from claude")

	// Permission relay under Claude Code's own request ID.
	mark = r.markOwner()
	if err := cli.Notify(ctx, claudechannel.NotifyPermissionRequest, map[string]any{
		"request_id":    "abcde",
		"tool_name":     "Bash",
		"description":   "run the build",
		"input_preview": "make build",
	}); err != nil {
		t.Fatalf("send the permission request: %v", err)
	}
	if _, id := r.waitApproval(mark); id != "abcde" {
		t.Fatalf("WeChat approval ID = %q, want the request's own %q", id, "abcde")
	}
	r.send("y abcde")
	var verdict struct {
		RequestID string `json:"request_id"`
		Behavior  string `json:"behavior"`
	}
	if err := json.Unmarshal(next(claudechannel.NotifyPermission), &verdict); err != nil {
		t.Fatalf("decode the verdict: %v", err)
	}
	if verdict.RequestID != "abcde" || verdict.Behavior != claudechannel.BehaviorAllow {
		t.Errorf("verdict = %+v, want request_id abcde and behavior allow", verdict)
	}
}

// B7: a third-party channel server configured as an entry point. What it pushes
// reaches a session; the agent's answers go back through its reply tool.
func TestChannelHost(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A direct chat: the chat is the owner's own, as in WeChat and Feishu. The
	// core records a channel server's owner on the endpoint whose peer is the
	// owner ID, while the endpoint of an inbound message is its chat_id, so
	// only a chat_id equal to the owner ID is recognised as the owner's.
	const user = "u-echo"
	r := newRig(t, func(r *rig) {
		r.cfg.ChannelServers = []config.ChannelServerConfig{{
			Name:    "echo",
			Command: []string{echoChannelBin},
			Env: map[string]string{
				"ECHO_CHANNEL_IN":   in,
				"ECHO_CHANNEL_OUT":  out,
				"ECHO_CHANNEL_USER": user,
				"ECHO_CHANNEL_CHAT": user,
			},
			Owner: user,
		}}
	})

	push := func(line string) {
		t.Helper()
		f, err := os.OpenFile(in, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := fmt.Fprintln(f, line); err != nil {
			t.Fatal(err)
		}
	}
	replies := func() []string {
		data, _ := os.ReadFile(out)
		var texts []string
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if _, text, ok := strings.Cut(l, "\t"); ok {
				texts = append(texts, strings.ReplaceAll(text, `\n`, "\n"))
			}
		}
		return texts
	}
	waitReply := func(from int, want string) {
		t.Helper()
		r.waitFor(fmt.Sprintf("a reply through the channel server containing %q", want), func() bool {
			rs := replies()
			for i := from; i < len(rs); i++ {
				if strings.Contains(rs[i], want) {
					return true
				}
			}
			return false
		})
	}

	push("/new " + agentName + " " + filepath.Join(r.ws, "demo"))
	waitReply(0, "会话 1")
	mark := len(replies())
	push("echo via channel")
	waitReply(mark, "via channel")

	for _, m := range r.sentTo(ownerID) {
		if strings.Contains(m, "via channel") {
			t.Errorf("WeChat saw the channel server's conversation:\n%s", m)
		}
	}
}

// --- ACP v2 ---------------------------------------------------------------

// B8: an agent configured for v2 is created, prompted, listed, resumed and
// closed, and every one of those crossed the wire as a v2 method.
func TestACPv2(t *testing.T) {
	tap := filepath.Join(t.TempDir(), "methods.log")
	agentHome := ""
	r := newRig(t, func(r *rig) {
		a := &r.cfg.Agents[0]
		agentHome = a.Env["FAKEAGENT_HOME"]
		a.Command = []string{tapBin, agentBin, "--acp-version", "2"}
		a.Env["ACPTAP_LOG"] = tap
		a.ACPVersion = config.ACPVersionV2
	})
	// The core resolves symlinks in a session's directory (macOS keeps temp
	// directories under /var, a link to /private/var), and the agent lists by
	// the directory it was given.
	demo, err := filepath.EvalSymlinks(filepath.Join(r.ws, "demo"))
	if err != nil {
		t.Fatal(err)
	}

	// New, then one turn that ends with the idle state_update.
	r.newSession()
	mark := r.markOwner()
	r.send("echo over v2")
	r.waitOwnerTextFrom(mark, "over v2")
	r.waitFor("the turn to end", func() bool {
		return r.daemon.Status().Running == 0
	})
	mark = r.markOwner()
	r.send("/ls")
	if list := r.waitOwnerTextFrom(mark, r.tr("session.list_header", nil)); !strings.Contains(list, r.tr("session.state.idle", nil)) {
		t.Errorf("after the idle state_update the session should be idle:\n%s", list)
	}

	// List, through the very driver the daemon runs.
	agentCfg := r.cfg.Agents[0]
	env := []string{}
	for k, v := range agentCfg.Env {
		env = append(env, k+"="+v)
	}
	ctx, stop := context.WithTimeout(context.Background(), subprocWait)
	defer stop()
	var infos []ports.AgentSessionInfo
	infos, err = r.driver.List(ctx, ports.SessionSpec{
		Agent: agentName, Command: agentCfg.Command, Env: append(os.Environ(), env...), Cwd: demo,
	})
	if err != nil {
		t.Fatalf("list over v2: %v", err)
	}
	if len(infos) == 0 {
		t.Error("list over v2 found no session")
	}

	// Resume: a restart parks the session and /use brings it back.
	r.restart()
	r.send("/use 1")
	r.waitOwnerText(r.tr("session.switched", map[string]any{"Slot": 1}))
	mark = r.markOwner()
	r.send("echo resumed")
	r.waitOwnerTextFrom(mark, "resumed")

	// Close.
	mark = r.markOwner()
	r.send("/close 1")
	r.waitOwnerTextFrom(mark, r.tr("session.closed", map[string]any{"Slot": 1}))

	var methods []string
	r.waitFor("session/close on the wire", func() bool {
		data, _ := os.ReadFile(tap)
		methods = strings.Fields(string(data))
		return contains(methods, "session/close")
	})
	for _, want := range []string{"initialize", "session/new", "session/prompt", "session/list", "session/resume", "session/close"} {
		if !contains(methods, want) {
			t.Errorf("%s never crossed the wire; methods seen: %v", want, methods)
		}
	}
	// v1 has session/load where v2 has session/resume.
	if contains(methods, "session/load") {
		t.Errorf("a v1 method crossed the wire: %v", methods)
	}

	// Every handshake the fake agent answered was a v2 one.
	data, err := os.ReadFile(filepath.Join(agentHome, fakeagent.InitializeLog))
	if err != nil {
		t.Fatalf("read the agent's initialize log: %v", err)
	}
	for _, v := range strings.Fields(string(data)) {
		if v != "2" {
			t.Errorf("the agent answered an initialize at protocol version %s; log: %q", v, data)
		}
	}
}

var _ io.Closer = (*mcpmini.Client)(nil)
