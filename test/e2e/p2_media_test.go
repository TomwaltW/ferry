package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B10 (spec-p2 B10, TestWakeNotifier) is not in this file. The wake-up path it
// pins — an urgent approval held because the reply window closed, reaching the
// operator over ntfy (internal/outbox/send.go:169) — only runs when the outbox
// and the daemon are built with a Waker. The rig builds neither with one
// (harness_test.go:236 and :268 pass no Waker, and nothing in test/e2e calls
// notify.New), so notify.ntfy.url is dead configuration here: wakeFor returns
// early on a nil waker and the fake ntfy server would never be called.
// Adding that hook means editing harness_test.go, which this round assigns to
// T2 for the driver-table line alone (spec-p2-backfill G6, 3.1), so per 3.1 and
// 7 this is reported rather than worked around. A parallel copy of the rig in
// this file would dodge the file-ownership rule while breaking the harness-hook
// one, and would fork what TΩ has to merge.

// B11 (spec-p2 B11): the gateway pushes an encrypted image. The decrypted bytes
// land in the inbox unchanged, and the path — carried by prompt.attachment — is
// what the agent is given.
func TestInboundImage(t *testing.T) {
	r := newRig(t)
	// Opening the session starts the agent, and the image then travels through
	// the fake CDN; both are slower than the usual settle time on a loaded
	// machine.
	r.patience = subprocWait
	r.newSession()

	// PNG magic plus a body: bytes that are recognisably an image and not the
	// text of a message.
	plain := []byte("\x89PNG\r\n\x1a\nthe bytes of a picture")
	mark := r.markOwner()
	r.gw.PushImage(ownerID, plain)

	// The adapter saves under <home>/inbox/<entry hash>/<message id>-image.jpg,
	// one directory per conversation (internal/im/wechat/media.go:172). That is
	// one level deeper than spec-p2 B11's shorthand "<home>/inbox/", so the
	// glob spans the hash directory instead of asserting the shorthand.
	var saved string
	r.waitFor("the decrypted image under <home>/inbox", func() bool {
		hits, err := filepath.Glob(filepath.Join(r.home, "inbox", "*", "*"))
		if err != nil || len(hits) != 1 {
			return false
		}
		saved = hits[0]
		return true
	})

	// The image arrived without text, so the prompt is the attachment line and
	// nothing else. The fake agent has no scripted behaviour for a prompt
	// starting with "[附件]", so it echoes the whole prompt back as
	// "unknown: <prompt>" — the agent's own account of what it was given.
	want := r.tr("prompt.attachment", map[string]any{"Path": saved})
	echo := r.waitOwnerTextFrom(mark, want)
	if !strings.Contains(echo, saved) {
		t.Errorf("the agent echoed:\n%s\nwant it to carry the saved path %q", echo, saved)
	}

	// Only now are the bytes read. The glob above returns as soon as the file
	// exists, which os.WriteFile does before it writes; the echo cannot arrive
	// until saveMedia has returned (media.go:88), so by here the write is done.
	got, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("read the saved image: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("the inbox holds %q, want the original %q", got, plain)
	}

	// A second witness, from the agent's own store rather than from its reply:
	// the user turn it recorded for this prompt.
	if prompt := r.agentUserTurn(t, saved); prompt != want {
		t.Errorf("the agent recorded the prompt %q, want exactly the attachment line %q", prompt, want)
	}
}

// agentUserTurn returns the user turn the fake agent recorded that mentions
// want. The fake agent keeps its sessions as JSON under FAKEAGENT_HOME, which
// the rig puts at <home>/agent (harness_test.go:157).
func (r *rig) agentUserTurn(t *testing.T, want string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(r.home, "agent", "sessions", "*.json"))
	if err != nil {
		t.Fatalf("list the agent's sessions: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var rec struct {
			History []struct {
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"history"`
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("decode %s: %v", f, err)
		}
		for _, turn := range rec.History {
			if turn.Role == "user" && strings.Contains(turn.Text, want) {
				return turn.Text
			}
		}
	}
	t.Fatalf("no user turn mentioning %q in the agent's sessions under %s", want, filepath.Join(r.home, "agent", "sessions"))
	return ""
}
