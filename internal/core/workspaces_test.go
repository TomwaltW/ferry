package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fakeWorkspaces gives each sender the roots the test says, and everyone else
// the shared ones.
type fakeWorkspaces struct {
	mu     sync.Mutex
	shared []string
	users  map[string][]string // sender ID -> roots
	err    error
}

func (f *fakeWorkspaces) Roots(ctx context.Context, ep fsp.EndpointID, senderID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if roots, ok := f.users[senderID]; ok {
		return roots, nil
	}
	return f.shared, nil
}

var _ ports.Workspaces = (*fakeWorkspaces)(nil)

// withWorkspaces puts a Workspaces behind the daemon before it starts: the
// owner keeps the shared root, and memberID works in a directory of their own,
// which it returns.
func (h *harness) withWorkspaces() (*fakeWorkspaces, string) {
	h.t.Helper()
	userDir := realDir(h.t, h.t.TempDir())
	ws := &fakeWorkspaces{shared: []string{h.root}, users: map[string][]string{memberID: {userDir}}}
	h.deps.Workspaces = ws
	return ws, userDir
}

func TestNewDefaultsToUserRoot(t *testing.T) {
	tb := newTestbed(t)
	_, userDir := tb.withWorkspaces()
	h := tb.start()

	h.send(memberID, "/new "+testAgent)
	msg := h.waitText("session.created|")
	if !strings.Contains(msg.Text, "Cwd="+userDir+" ") {
		t.Errorf("reply = %q, want the session in the member's own root %s", msg.Text, userDir)
	}
	h.waitFor("the agent to start", func() bool { return h.driver.startCount() == 1 })

	// The owner's bare /new still lands in the shared root.
	h.owner("/new " + testAgent)
	h.waitFor("a second session", func() bool { return h.countText("session.created|") == 2 })
	if n := h.countText("Cwd=" + h.root + " "); n != 1 {
		t.Errorf("sessions in the shared root = %d, want the owner's one", n)
	}
}

func TestNewRefusedOutsideUserRoots(t *testing.T) {
	tb := newTestbed(t)
	ws, _ := tb.withWorkspaces()
	h := tb.start()

	h.send(memberID, "/new "+testAgent+" "+h.root)
	msg := h.waitText("workspace.denied|")
	if want := "workspace.denied|zh-CN Dir=" + h.root; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.startCount() != 0 {
		t.Error("a directory outside the member's roots started an agent")
	}

	// When the member's roots cannot be had at all, /new is refused too.
	ws.mu.Lock()
	ws.err = errors.New("users_root is not writable")
	ws.mu.Unlock()
	h.send(memberID, "/new "+testAgent)
	h.waitFor("a second refusal", func() bool { return h.countText("workspace.denied|") == 2 })
	if h.driver.startCount() != 0 {
		t.Error("a failing Workspaces started an agent")
	}
}

func TestLsHidesTerminalSessionsOutsideRoots(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff(tb.terminalSession())
	tb.withWorkspaces()
	h := tb.start()

	// The terminal session sits in the shared root, which is not the member's.
	h.send(memberID, "/ls")
	h.waitText("hint.no_session|")
	if n := h.countText("session.list_terminal_item|"); n != 0 {
		t.Errorf("the member was shown %d terminal entries, want none", n)
	}

	// The owner still sees it.
	h.owner("/ls")
	h.waitText("session.list_terminal_item|")
}

func TestUseTerminalOutsideRootsRefused(t *testing.T) {
	tb := newTestbed(t)
	ho := tb.withHandoff(tb.terminalSession())
	tb.withWorkspaces()
	h := tb.start()

	// The owner's /ls numbers the terminal entry for the whole endpoint; the
	// member using that number still has to be allowed its directory.
	slot := adoptable(t, h)
	h.send(memberID, fmt.Sprintf("/use %d", slot))
	msg := h.waitText("workspace.denied|")
	if want := "workspace.denied|zh-CN Dir=" + h.root; msg.Text != want {
		t.Errorf("reply = %q, want %q", msg.Text, want)
	}
	if h.driver.resumeCount() != 0 || ho.busyCount() != 0 {
		t.Errorf("resumes = %d, busy checks = %d, want neither for a refused /use", h.driver.resumeCount(), ho.busyCount())
	}

	// The owner may still take it over.
	h.owner(fmt.Sprintf("/use %d", slot))
	h.waitText("handoff.adopted|")
}

func TestPairConfirmedTellsWorkspace(t *testing.T) {
	tb := newTestbed(t)
	ws, _ := tb.withWorkspaces()
	h := tb.start()
	carolDir := realDir(t, t.TempDir())
	ws.mu.Lock()
	ws.users["carol"] = []string{carolDir}
	ws.mu.Unlock()

	peer := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "carol"}
	h.acl.mu.Lock()
	h.acl.confirmEp, h.acl.confirmWho = peer, "carol"
	h.acl.mu.Unlock()

	h.owner("/pair 123456")
	msg := h.waitText("pair.confirmed_workspace|")
	if want := "pair.confirmed_workspace|zh-CN Dir=" + carolDir; msg.Text != want {
		t.Errorf("confirmation = %q, want %q", msg.Text, want)
	}
	if msg.Endpoint != peer {
		t.Errorf("confirmation went to %v, want %v", msg.Endpoint, peer)
	}

	// Someone who shares the roots is told plainly.
	h.acl.mu.Lock()
	h.acl.confirmEp, h.acl.confirmWho = fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "dave"}, "dave"
	h.acl.mu.Unlock()
	h.owner("/pair 654321")
	h.waitText("pair.confirmed|zh-CN")
	if n := h.countText("pair.confirmed_workspace|"); n != 1 {
		t.Errorf("workspace confirmations = %d, want only carol's", n)
	}
}

// Without Workspaces every paired user shares the workspace roots and sees the
// terminal sessions in them, as in P1–P3.
func TestNilWorkspacesKeepsSharedRoots(t *testing.T) {
	tb := newTestbed(t)
	tb.withHandoff(tb.terminalSession())
	h := tb.start()

	h.send(memberID, "/new "+testAgent)
	msg := h.waitText("session.created|")
	if !strings.Contains(msg.Text, "Cwd="+h.root+" ") {
		t.Errorf("reply = %q, want the session in the shared root %s", msg.Text, h.root)
	}

	h.send(memberID, "/ls")
	h.waitText("session.list_terminal_item|")

	peer := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "carol"}
	h.acl.mu.Lock()
	h.acl.confirmEp, h.acl.confirmWho = peer, "carol"
	h.acl.mu.Unlock()
	h.owner("/pair 123456")
	h.waitText("pair.confirmed|zh-CN")
	if n := h.countText("pair.confirmed_workspace|"); n != 0 {
		t.Error("a pairing without Workspaces announced a workspace")
	}
}

// The bot owner is the owner in every chat of the bot (spec-p4-b1 3.3), so
// their text reaches the agent as written even where the owner role is recorded
// at their own endpoint only; anyone else in that chat is still named.
func TestOwnerNoFromPrefixInAnyChat(t *testing.T) {
	tb := newTestbed(t)
	ctx := context.Background()
	tb.ep = fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "room-1"}
	own := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: ownerID}
	mustNil(t, tb.store.SetRole(ctx, own, ownerID, "owner"))
	mustNil(t, tb.store.SetRole(ctx, tb.ep, memberID, "member"))
	h := tb.start()

	agent := h.newSession()
	h.owner("build it")
	h.waitFor("the prompt", func() bool { return len(agent.prompted()) == 1 })
	if got := agent.prompted()[0]; got != "build it" {
		t.Errorf("owner's prompt in another chat = %q, want the text unchanged", got)
	}

	agent.emit(fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn"})
	h.waitFor("the turn to end", func() bool { return h.sessionState() == fsp.StateIdle })
	h.send(memberID, "and test it")
	h.waitFor("the member's prompt", func() bool { return len(agent.prompted()) == 2 })
	if got, want := agent.prompted()[1], "[from "+memberID+"] and test it"; got != want {
		t.Errorf("member's prompt = %q, want %q", got, want)
	}
}
