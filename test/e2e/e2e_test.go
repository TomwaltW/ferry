package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B1: the scanning user becomes owner and approver, and the adapter is online.
func TestLoginAndOwner(t *testing.T) {
	r := newRig(t)

	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })
	if got := r.adapter.Status().OwnerID; got != ownerID {
		t.Errorf("adapter owner = %q, want %q", got, ownerID)
	}

	// The core adopts the owner at startup; give it the moment it needs.
	r.waitFor("the owner role to be recorded", func() bool { return r.aclmod.Allowed(r.ep, ownerID) })
	if !r.aclmod.IsApprover(r.ep, ownerID) {
		t.Errorf("the scanning user is not an approver")
	}

	// And behaviourally: the owner is never asked to pair.
	r.send("hello")
	r.waitOwnerText(r.tr("hint.no_session", nil))
}

// B2: a stranger only ever gets a pairing code, and works after the owner pairs them.
func TestStrangerPairing(t *testing.T) {
	r := newRig(t)
	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })

	r.sendFrom(stranger, "rm -rf ~")
	msg := r.waitText(stranger, "配对码")
	code := regexpDigits(t, msg)

	// Nothing reached an agent: the stranger was told to pair, nothing else.
	if len(r.sentTo(stranger)) != 1 {
		t.Errorf("stranger got %d messages, want exactly the pairing code", len(r.sentTo(stranger)))
	}
	if kinds := r.auditKinds(); !contains(kinds, "rejected") {
		t.Errorf("audit kinds = %v, want a rejected entry", kinds)
	}

	r.send("/pair " + code)
	r.waitOwnerText(r.tr("pair.confirmed_owner", map[string]any{"Sender": stranger}))
	r.waitText(stranger, r.tr("pair.confirmed", nil))

	// Now the stranger reaches the agent.
	r.sendFrom(stranger, "/new "+agentName+" "+filepath.Join(r.ws, "demo"))
	r.waitText(stranger, "会话 1")
	r.sendFrom(stranger, "echo paired")
	r.waitText(stranger, "paired")
}

// B3: open a managed session, prompt it, and see it listed as idle.
func TestManagedSession(t *testing.T) {
	r := newRig(t)
	r.newSession()

	r.send("echo hello")
	r.waitOwnerText(r.tr("turn.accepted", map[string]any{"Agent": agentName, "Slot": 1}))
	r.waitOwnerText("hello")

	r.send("/ls")
	list := r.waitOwnerText(r.tr("session.list_header", nil))
	if !strings.Contains(list, r.tr("session.state.idle", nil)) {
		t.Errorf("/ls does not show the session as idle:\n%s", list)
	}
}

// B4: an execute tool asks for approval; a wrong ID does nothing; no answer times out.
func TestApprovalFlow(t *testing.T) {
	r := newRig(t)
	r.newSession()

	mark := r.markOwner()
	r.send("ask execute " + filepath.Join(r.ws, "demo", "run.sh"))
	ask := r.waitOwnerTextFrom(mark, "审批")
	id := r.approvalIDFrom(ask)

	// A verdict naming an unknown approval is dropped silently.
	noVerdict := r.markOwner()
	r.send("y zzzzz")
	r.refuteFrom(ownerID, noVerdict, "allowed", "a verdict with an unknown ID must not resolve the pending approval")

	r.send("Y " + id)
	r.waitOwnerTextFrom(noVerdict, "allowed")

	// A second approval that nobody answers is rejected once the timeout passes.
	second := r.markOwner()
	r.send("ask execute " + filepath.Join(r.ws, "demo", "again.sh"))
	ask2 := r.waitOwnerTextFrom(second, "审批")
	id2 := r.approvalIDFrom(ask2)
	if id2 == id {
		t.Fatalf("second approval reused ID %q", id)
	}
	r.waitOwnerTextFrom(second, r.tr("approval.timeout", map[string]any{"ID": id2}))
	r.waitOwnerTextFrom(second, "denied")
}

// B5: the policy decides read and delete without ever asking a human.
func TestPolicy(t *testing.T) {
	r := newRig(t)
	r.newSession()

	readMark := r.markOwner()
	r.send("ask read " + filepath.Join(r.ws, "demo", "a.txt"))
	r.waitOwnerTextFrom(readMark, "allowed")
	r.refuteFrom(ownerID, readMark, "审批", "a read inside the workspace is allowed by policy, not by asking")

	delMark := r.markOwner()
	r.send("ask delete /etc/passwd")
	r.waitOwnerTextFrom(delMark, "denied")
	r.refuteFrom(ownerID, delMark, "审批", "a delete outside the workspace is denied by policy, not by asking")
}

// B6: /stop cancels the running turn.
func TestStop(t *testing.T) {
	r := newRig(t)
	r.newSession()

	r.send("slow 5000")
	r.waitOwnerText(r.tr("turn.accepted", map[string]any{"Agent": agentName, "Slot": 1}))
	r.send("/stop")
	r.waitOwnerText(r.tr("turn.stopped", nil))
}

// B7: messages sent while the agent is busy arrive as one merged prompt.
func TestBusyMerge(t *testing.T) {
	r := newRig(t)
	r.newSession()

	r.send("slow 1000")
	r.waitOwnerText(r.tr("turn.accepted", map[string]any{"Agent": agentName, "Slot": 1}))
	r.send("echo a")
	r.send("echo b")
	r.send("echo c")

	// The three are joined with blank lines into a single "echo a\n\necho b\n\necho c",
	// so the agent echoes everything after the first "echo ".
	merged := r.waitOwnerText("echo c")
	if !strings.Contains(merged, "echo b") {
		t.Errorf("the queued messages did not arrive as one prompt:\n%s", merged)
	}
}

// B8: a chatty turn stays inside the reply budget and nothing is dropped.
func TestReplyBudget(t *testing.T) {
	r := newRig(t)
	r.newSession()

	tok := r.sendTok("burst 30")
	r.waitFor("the burst turn to finish", func() bool {
		return countWindow(r, tok) > 0 && r.quiet(tok, 700*time.Millisecond)
	})

	n := countWindow(r, tok)
	if n > r.cfg.Outbox.ReplyBudget {
		t.Errorf("sent %d replies in one window, budget is %d", n, r.cfg.Outbox.ReplyBudget)
	}
	for _, s := range r.gw.Sent() {
		if s.Dropped {
			t.Errorf("gateway dropped a message: %+v", s)
		}
	}
}

// B9: an expired platform session takes the adapter offline and a fresh scan restores it.
func TestSessionExpired(t *testing.T) {
	r := newRig(t)
	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })

	r.gw.ExpireSession()
	r.waitFor("the daemon to notice WeChat is offline", func() bool { return !r.daemon.Status().IMOnline })

	r.gw.ConfirmScan(ownerID)
	r.restart()
	r.waitFor("the adapter to come back online", func() bool { return r.adapter.Status().Online })

	r.send("/ls")
	r.waitOwnerText(r.tr("hint.no_session", nil))
}

// B10: sessions survive a restart as parked, and /use brings one back.
func TestRestartResume(t *testing.T) {
	r := newRig(t)
	r.newSession()
	r.send("echo one")
	r.waitOwnerText("one")

	r.restart()

	r.send("/ls")
	list := r.waitOwnerText(r.tr("session.list_header", nil))
	if !strings.Contains(list, r.tr("session.state.parked", nil)) {
		t.Errorf("after a restart the session should be parked:\n%s", list)
	}

	r.send("/use 1")
	r.waitOwnerText("会话 1")
	r.send("echo again")
	r.waitOwnerText("again")
}

// B11: a directory outside the workspace roots is refused.
func TestWorkspaceDenied(t *testing.T) {
	r := newRig(t)
	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })

	r.send("/new " + agentName + " /tmp")
	r.waitOwnerText(r.tr("workspace.denied", map[string]any{"Dir": "/tmp"}))
}
