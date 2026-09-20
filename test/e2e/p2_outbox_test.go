package e2e

// spec-p2 2.2 B7, B8, B9 and B13: what the outbox does when the reply window is
// shut, when the operator asks for the backlog, when a turn is too long for
// chat, and when a send leaves its outcome open.

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

// withOutbox adjusts the outbox knobs before the daemon first starts.
func withOutbox(fn func(*config.OutboxConfig)) rigOption {
	return func(r *rig) { fn(&r.cfg.Outbox) }
}

// productionMerge is the merge interval a real deployment runs with. The rig
// drops it to 200ms so that streamed output reaches the chat in pieces, which
// is the opposite of what B7 and B9 need: both want one whole turn in one
// message, which is what arrives when the mid-turn flush ticker never fires.
const productionMerge = 30 * time.Second

// withFakeCDN points the adapter's media endpoint at the fake gateway's own
// CDN. Without it an outbound file is uploaded to the production endpoint that
// wechat.cdn_base_url names by default (internal/im/wechat/cdn.go), which is
// what the toml comment on the field means by "tests point it at a fake".
func withFakeCDN() rigOption {
	return func(r *rig) { r.cfg.WeChat.CDNBaseURL = r.gw.CDNBaseURL() }
}

// pushAged sends as the owner with an arrival time of its own. The core opens
// the reply window at that moment (internal/core/inbound.go), so a message
// backdated past window_ttl opens a window that is stale the instant it is
// born, and a future-dated one opens a window that cannot go stale while the
// test waits for something slow.
func (r *rig) pushAged(text string, age time.Duration) string {
	return r.gw.PushMessage(ilinkwire.Message{
		FromUserID:   ownerID,
		CreateTimeMs: time.Now().Add(-age).UnixMilli(),
		ItemList: []ilinkwire.MessageItem{{
			Type:     ilinkwire.ItemText,
			TextItem: &ilinkwire.TextItem{Text: text},
		}},
	})
}

// heldCount is how many messages are waiting for a window to reopen.
func (r *rig) heldCount() int {
	r.t.Helper()
	rows, err := r.store.OutboxByState(context.Background(), r.ep, fsp.DeliveryHeld)
	if err != nil {
		r.t.Fatalf("list the held messages: %v", err)
	}
	return len(rows)
}

// indexOf is where the first message to the owner at or after from containing
// want sits, or -1.
func (r *rig) indexOf(from int, want string) int {
	msgs := r.sentTo(ownerID)
	for i := from; i < len(msgs); i++ {
		if strings.Contains(msgs[i], want) {
			return i
		}
	}
	return -1
}

// waitExactFrom waits for a message at or after from that is exactly want. A
// digest quotes the messages it lists, so containment alone would not tell a
// redelivered message from the line describing it.
func (r *rig) waitExactFrom(from int, want string) {
	r.t.Helper()
	r.waitFor(fmt.Sprintf("the message %q after #%d", want, from), func() bool {
		msgs := r.sentTo(ownerID)
		for i := from; i < len(msgs); i++ {
			if msgs[i] == want {
				return true
			}
		}
		return false
	})
}

// catalogMark is the part of a catalogue entry that carries no argument: what
// two renderings with different arguments still agree on. Matching that keeps a
// test honest about the wording without pinning the numbers inside it.
func (r *rig) catalogMark(key string, a, b map[string]any) string {
	r.t.Helper()
	mark := commonPrefix(r.tr(key, a), r.tr(key, b))
	if mark == "" {
		r.t.Fatalf("%s renders nothing that both arguments share", key)
	}
	return mark
}

func commonPrefix(a, b string) string {
	ar, br := []rune(a), []rune(b)
	i := 0
	for i < min(len(ar), len(br)) && ar[i] == br[i] {
		i++
	}
	return string(ar[:i])
}

// digestHead is the first line of the digest for n held messages: the count,
// and nothing the test does not control.
func (r *rig) digestHead(n int) string {
	head, _, _ := strings.Cut(r.tr("outbox.digest", map[string]any{"N": n, "Lines": ""}), "\n")
	return head
}

// dots reports whether a message is the run of full stops the fake agent
// streams for slow, and nothing else.
func dots(msg string) bool {
	s := strings.TrimSpace(msg)
	return s != "" && strings.Trim(s, ".") == ""
}

// B7 (spec-p2 2.2): a turn that outlives the reply window is held, and the next
// message the operator sends brings the digest first and the result after it.
func TestHeldAndDigest(t *testing.T) {
	r := newRig(t, withOutbox(func(o *config.OutboxConfig) {
		o.WindowTTL = config.Duration{Duration: time.Second}
		o.MergeInterval = config.Duration{Duration: productionMerge}
	}))
	r.patience = subprocWait

	// Starting the agent can take longer than the one second window on a busy
	// machine, and a held receipt would put a second line in the digest. The
	// session is opened on a window that cannot go stale while the agent comes up.
	r.pushAged("/new "+agentName+" "+filepath.Join(r.ws, "demo"), -30*time.Second)
	r.waitOwnerText("会话 1")

	// The turn takes twice as long as the window lasts, so its result is held.
	r.send("slow 2000")
	r.waitFor("the result of the turn to be held", func() bool { return r.heldCount() == 1 })

	from := r.markOwner()
	r.send("hello")
	r.waitOwnerTextFrom(from, r.digestHead(1))
	digest := r.indexOf(from, r.digestHead(1))
	more := r.catalogMark("outbox.digest_more",
		map[string]any{"N": 1}, map[string]any{"N": 2})
	if msgs := r.sentTo(ownerID); strings.Contains(msgs[digest], more) {
		t.Errorf("the digest points at /pull although nothing is left over:\n%s", msgs[digest])
	}

	// Then the result itself, which is the run of dots the agent streamed.
	result := -1
	r.waitFor("the held result to follow the digest", func() bool {
		msgs := r.sentTo(ownerID)
		for i := digest + 1; i < len(msgs); i++ {
			if dots(msgs[i]) {
				result = i
				return true
			}
		}
		return false
	})
	msgs := r.sentTo(ownerID)
	for i := from; i < digest; i++ {
		if dots(msgs[i]) {
			t.Fatalf("the result reached the chat at #%d, ahead of the digest at #%d", i, digest)
		}
	}
	if result <= digest {
		t.Fatalf("the result is at #%d and the digest at #%d, want the digest first", result, digest)
	}
	if n := r.heldCount(); n != 0 {
		t.Errorf("%d messages are still held, want the backlog cleared", n)
	}
}

// B8 (spec-p2 2.2): twelve messages pile up while the window is shut; the next
// message brings a digest and the first batch, /pull brings the next, and once
// the backlog is empty /pull says there is nothing left.
func TestPull(t *testing.T) {
	// The reply budget is 8 and a flush keeps 3 replies back — one for the
	// digest, one for the receipt, one for the turn the operator has just
	// started (internal/outbox/held.go) — so a batch is 5.
	const (
		backlog = 12
		batch   = 5
	)
	r := newRig(t, withOutbox(func(o *config.OutboxConfig) {
		o.WindowTTL = config.Duration{Duration: time.Second}
	}))
	r.patience = subprocWait
	notFound := func(slot int) string {
		return r.tr("session.not_found", map[string]any{"Slot": slot})
	}

	// Each of the twelve arrives with a timestamp older than window_ttl, so the
	// window it opens is stale already and its answer is held. No agent is
	// involved: /use on a number nobody holds is answered in one message.
	for slot := 1; slot <= backlog; slot++ {
		r.pushAged(fmt.Sprintf("/use %d", slot), 10*time.Second)
	}
	r.waitFor(fmt.Sprintf("%d messages to pile up", backlog), func() bool {
		return r.heldCount() == backlog
	})

	// Writing anything reopens the window: the digest comes first, then five.
	from := r.markOwner()
	r.send("hello")
	r.waitOwnerTextFrom(from, r.digestHead(backlog))
	r.waitOwnerTextFrom(from, r.tr("outbox.digest_more", map[string]any{"N": backlog - batch}))
	for slot := 1; slot <= batch; slot++ {
		r.waitExactFrom(from, notFound(slot))
	}
	r.refuteFrom(ownerID, from, notFound(batch+1), "the sixth answer is past the first batch")
	r.waitFor("seven messages to be left in the backlog", func() bool {
		return r.heldCount() == backlog-batch
	})

	// /pull: the flush that every inbound triggers carries the next five, and
	// Pull itself spends what is left of the budget on one more.
	from = r.markOwner()
	r.send("/pull")
	r.waitOwnerTextFrom(from, r.digestHead(backlog-batch))
	for slot := batch + 1; slot <= backlog-1; slot++ {
		r.waitExactFrom(from, notFound(slot))
	}
	r.refuteFrom(ownerID, from, notFound(backlog), "the twelfth answer is still held")
	r.waitFor("one message to be left in the backlog", func() bool { return r.heldCount() == 1 })

	// The last one goes out here, and /pull reports an empty backlog.
	from = r.markOwner()
	r.send("/pull")
	r.waitExactFrom(from, notFound(backlog))
	r.waitExactFrom(from, r.tr("outbox.nothing_held", nil))
	r.waitFor("the backlog to be empty", func() bool { return r.heldCount() == 0 })
}

// B9 (spec-p2 2.2): a turn too long for chat goes out as a summary and a file,
// and /file 1 hands the same file over again.
func TestLongOutputFile(t *testing.T) {
	r := newRig(t, withFakeCDN(), withOutbox(func(o *config.OutboxConfig) {
		// Production merging: the whole turn reaches the outbox as one message,
		// which is what makes it long enough to become a file at all.
		o.MergeInterval = config.Duration{Duration: productionMerge}
	}))
	r.patience = subprocWait
	r.newSession()

	from := r.markOwner()
	r.send("burst 100") // 100 lines of 60 characters: about 6000 runes
	r.waitOwnerTextFrom(from, r.catalogMark("outbox.file_summary",
		map[string]any{"Chars": 1, "Head": ""},
		map[string]any{"Chars": 2, "Head": ""}))
	r.waitFor("the long output to arrive as a file", func() bool {
		return len(r.gw.SentFiles()) == 1
	})

	sent := r.gw.SentFiles()[0]
	if sent.Kind != ilinkwire.ItemFile {
		t.Errorf("the long output arrived as %v, want a file", sent.Kind)
	}
	if n := utf8.RuneCountInString(string(sent.Plain)); n <= r.cfg.Outbox.LongOutputThreshold {
		t.Errorf("the file holds %d runes, want more than the %d that make a turn long",
			n, r.cfg.Outbox.LongOutputThreshold)
	}

	// /file 1 is the same file once more.
	from = r.markOwner()
	r.send("/file 1")
	r.waitFor("the file to be handed over a second time", func() bool {
		return len(r.gw.SentFiles()) == 2
	})
	again := r.gw.SentFiles()[1]
	if again.Name != sent.Name {
		t.Errorf("/file 1 sent %q, want the same file %q", again.Name, sent.Name)
	}
	if !bytes.Equal(again.Plain, sent.Plain) {
		t.Errorf("/file 1 sent %d bytes, want the same %d", len(again.Plain), len(sent.Plain))
	}
}

// B13 (spec-p2 2.2): a send whose outcome the gateway leaves open is retried
// under the same client_id, and the gateway ends up with one record of it.
func TestRetryDedupe(t *testing.T) {
	r := newRig(t, withOutbox(func(o *config.OutboxConfig) {
		// Short enough that the hung attempt gives up quickly, long enough that
		// the retry, which the gateway answers at once, never trips it.
		o.SendTimeout = config.Duration{Duration: 500 * time.Millisecond}
	}))
	// The retry waits two seconds (internal/outbox/send.go retryDelays).
	r.patience = subprocWait
	r.waitFor("the adapter to report online", func() bool { return r.adapter.Status().Online })

	// One message, and the gateway hangs on the first attempt at sending it.
	// Typing goes to a path of its own, so nothing else consumes the fault.
	r.gw.FailNext(ilinkwire.PathSendMessage, fakeilink.FailTimeout)
	want := r.tr("session.not_found", map[string]any{"Slot": 7})
	from := r.markOwner()
	r.send("/use 7")
	r.waitOwnerTextFrom(from, want)

	// The gateway saw the message once: the attempt it hung on never reached
	// the handler that records one.
	var records []fakeilink.SentRecord
	for _, s := range r.gw.Sent() {
		if s.Text == want {
			records = append(records, s)
		}
	}
	if len(records) != 1 || records[0].Duplicate {
		t.Fatalf("the gateway holds %d records of the message (%+v), want one that is not a duplicate",
			len(records), records)
	}

	// It took two attempts, and the second reused the client_id of the first:
	// the dedupe key the outbox derives from the queue row (outbox/send.go).
	rows, err := r.store.OutboxByState(context.Background(), r.ep, fsp.DeliveryAccepted)
	if err != nil {
		t.Fatalf("list the delivered messages: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Msg.Text != want {
			continue
		}
		found = true
		if row.Attempts != 2 {
			t.Errorf("the message took %d attempts, want 2: one that timed out and one that got through",
				row.Attempts)
		}
		if id := fmt.Sprintf("ob-%d-1", row.ID); records[0].ClientID != id {
			t.Errorf("the gateway saw client_id %q, want %q, the key the retry reuses",
				records[0].ClientID, id)
		}
	}
	if !found {
		t.Fatalf("no delivered queue row carries the message, want the one /use 7 answered")
	}
}
