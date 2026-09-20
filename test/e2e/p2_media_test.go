package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
)

// withNtfy points the wake-up notifier at a server of our own and turns on the
// one event B10 is about.
func withNtfy(base string) rigOption {
	return func(r *rig) {
		r.cfg.Notify.Enabled = []string{"ntfy"}
		r.cfg.Notify.Events = []string{"approval_pending"}
		r.cfg.Notify.Ntfy = config.NtfyConfig{URL: base, Topic: "ferry-e2e"}
	}
}

// B10 (spec-p2 B10): an approval the outbox has to hold because the reply
// window closed raises one out-of-band reminder. The reminder travels over a
// third-party service, so it may name the session and nothing else — no tool,
// no command, no path (internal/outbox/send.go:154).
func TestWakeNotifier(t *testing.T) {
	var mu sync.Mutex
	var reminders []string
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		// Everything the service gets to see: the topic it is posted to, the
		// title header, and the body.
		reminders = append(reminders, req.URL.Path+"\n"+req.Header.Get("Title")+"\n"+string(body))
		mu.Unlock()
	}))
	t.Cleanup(ntfy.Close)

	r := newRig(t, withNtfy(ntfy.URL), withOutbox(func(o *config.OutboxConfig) {
		o.WindowTTL = config.Duration{Duration: time.Second}
		o.MergeInterval = config.Duration{Duration: productionMerge}
	}))
	r.patience = subprocWait

	// The session is opened on a window that cannot go stale while the agent
	// starts; only the approval below is meant to arrive out of window.
	r.pushAged("/new "+agentName+" "+filepath.Join(r.ws, "demo"), -30*time.Second)
	r.waitOwnerText("会话 1")

	// This prompt opens a window that was already expired when it was born, so
	// the approval it provokes cannot be delivered: the outbox holds it, and
	// holding an urgent message is what wakes the notifier.
	script := filepath.Join(r.ws, "demo", "rotate-prod-keys.sh")
	r.pushAged("ask execute "+script, 30*time.Second)

	r.waitFor("the notifier to raise a reminder", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reminders) > 0
	})
	mu.Lock()
	got := append([]string(nil), reminders...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("the notifier raised %d reminders, want exactly one: %q", len(got), got)
	}
	note := got[0]
	if !strings.Contains(note, "有待处理的审批") {
		t.Errorf("the reminder does not say that an approval is waiting:\n%s", note)
	}
	if !strings.Contains(note, "会话 1") {
		t.Errorf("the reminder does not name the session:\n%s", note)
	}
	// The negative half, and the point of the test: none of this may reach a
	// service the operator does not control.
	for _, leak := range []string{script, "rotate-prod-keys.sh", r.ws, "execute"} {
		if strings.Contains(note, leak) {
			t.Errorf("the reminder hands %q to the notification service:\n%s", leak, note)
		}
	}
}

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
