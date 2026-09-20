package e2e

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/testkit/fakeserver"
)

// spec-p2 2.2 B5 and B6: the server driver end to end. The agent is a service
// that is already running, so the whole path is fake gateway -> WeChat adapter
// -> core -> server driver -> fake service, and back up the same way. Both
// tests drive a prompt down and an event stream up, then put a permission
// through the approval the owner sees in WeChat.

// withServerAgent adds an agent backed by a local service. Each fixture has to
// exist before the rig starts, because the configuration is written and
// validated before the daemon comes up.
func withServerAgent(name, kind, url string) rigOption {
	return func(r *rig) {
		r.cfg.Agents = append(r.cfg.Agents, config.AgentConfig{
			Name:   name,
			Server: &config.ServerConfig{Kind: kind, URL: url},
		})
	}
}

func TestServerOpenCode(t *testing.T) {
	svc := fakeserver.NewOpenCode("")
	t.Cleanup(svc.Close)

	const agent = "opencode-live"
	r := newRig(t, withServerAgent(agent, "opencode", svc.URL))
	demo := filepath.Join(r.ws, "demo")
	// OpenCode's create route is not told a directory; the service reports the
	// one it was started in, and the driver names the session after it.
	svc.SetDir(demo)

	r.send("/new " + agent + " " + demo)
	r.waitOwnerText("会话 1")

	if !svc.WaitSession(1) {
		t.Fatal("the service was never asked to open a session")
	}
	sid := svc.SessionIDs()[0]
	if !svc.WaitStreams(1) {
		t.Fatal("the driver never opened the event stream")
	}
	if got, want := svc.Title(sid), "ferry: demo"; got != want {
		t.Errorf("the session was titled %q, want %q", got, want)
	}

	// The prompt travels over POST /session/{id}/prompt_async.
	mark := r.markOwner()
	r.send("hello")
	if !svc.WaitPrompt(1) {
		t.Fatal("the service never received a prompt")
	}
	if got := svc.Prompts(); !slices.Contains(got, "hello") {
		t.Fatalf("the service was sent %q, want it to include hello", got)
	}

	// Text pushed down the event stream comes back out in WeChat.
	svc.EmitText(sid, "prt_1", "opencode is here")
	r.waitOwnerTextFrom(mark, "opencode is here")

	// A permission event becomes an approval the owner answers with y <ID>.
	approvalAt := r.markOwner()
	svc.EmitPermission(sid, "perm_1", "bash", "run the tests", "go test ./...")
	msg, id := r.waitApproval(approvalAt)
	if !strings.Contains(msg, "run the tests") {
		t.Errorf("the approval does not name the tool's title:\n%s", msg)
	}
	r.send("y " + id)

	if !svc.WaitReplies(1) {
		t.Fatalf("the service was never answered; the owner saw:\n%s", strings.Join(r.sentTo(ownerID), "\n---\n"))
	}
	got := svc.Replies()[0]
	// "once" is the allow-once option ID the driver offers for OpenCode, and
	// the option ID is the value it puts in the response field.
	want := fakeserver.OpenCodeReply{Session: sid, Request: "perm_1", Response: "once"}
	if got != want {
		t.Errorf("the service was answered %+v, want %+v", got, want)
	}

	svc.EmitIdle(sid)
}

func TestServerQwen(t *testing.T) {
	svc := fakeserver.NewQwen("")
	t.Cleanup(svc.Close)

	const agent = "qwen-live"
	r := newRig(t, withServerAgent(agent, "qwen", svc.URL))
	demo := filepath.Join(r.ws, "demo")
	// The daemon only opens sessions on a directory it was started with, the
	// way `qwen serve --workspace` registers one.
	svc.Register(demo)

	r.send("/new " + agent + " " + demo)
	r.waitOwnerText("会话 1")

	if !svc.WaitSession(1) {
		t.Fatalf("the daemon was never asked to open a session on a registered workspace, it saw %q", svc.Cwds())
	}
	sid := svc.SessionIDs()[0]
	if got := svc.Cwds(); !slices.Contains(got, demo) {
		t.Errorf("the daemon was asked for %q, want it to include %q", got, demo)
	}
	if !svc.WaitStreams(sid, 1) {
		t.Fatal("the driver never opened GET /session/{id}/events")
	}

	// POST /session/{id}/prompt.
	mark := r.markOwner()
	r.send("hello")
	if !svc.WaitPrompt(1) {
		t.Fatal("the daemon never received a prompt")
	}
	if got := svc.Prompts(); !slices.Contains(got, "hello") {
		t.Fatalf("the daemon was sent %q, want it to include hello", got)
	}

	// GET /session/{id}/events carries the reply back.
	svc.EmitText(sid, "qwen is here")
	r.waitOwnerTextFrom(mark, "qwen is here")

	// POST /session/{id}/permission/{requestId} closes the approval.
	approvalAt := r.markOwner()
	svc.EmitPermission(sid, "req-1", "run_shell_command", "run the tests", "go test ./...", filepath.Join(demo, "a.txt"))
	msg, id := r.waitApproval(approvalAt)
	if !strings.Contains(msg, "run the tests") {
		t.Errorf("the approval does not name the tool's title:\n%s", msg)
	}
	r.send("y " + id)

	if !svc.WaitReplies(1) {
		t.Fatalf("the daemon was never answered; the owner saw:\n%s", strings.Join(r.sentTo(ownerID), "\n---\n"))
	}
	got := svc.Replies()[0]
	if got.Session != sid || got.Request != "req-1" || got.Option != "proceed_once" {
		t.Errorf("the daemon was answered %+v, want %s/req-1/proceed_once", got, sid)
	}

	svc.EmitTurnComplete(sid)
}
