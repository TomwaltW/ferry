package outbox

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// longText is a turn well past the threshold used by these tests.
func longText(runes int) string {
	return strings.Repeat("在", runes)
}

// fileBox is a harness whose long turns become files.
func fileBox(t *testing.T, tune func(*config.OutboxConfig)) *harness {
	t.Helper()
	return newBox(t, func(c *config.OutboxConfig) {
		c.LongOutput = config.LongOutputFile
		c.LongOutputThreshold = 100
		if tune != nil {
			tune(c)
		}
	})
}

// TestLongOutputToFile is the B9 shape: a long turn becomes a summary plus one
// file message, and the file holds the whole text.
func TestLongOutputToFile(t *testing.T) {
	h := fileBox(t, nil)
	h.open("ctx-1")

	text := longText(600)
	h.enqueue(sessionA, text, true)
	h.box.pump(h.ctx)

	msgs := h.im.messages()
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want the summary and the file: %q", len(msgs), h.sentTexts())
	}
	if msgs[0].Kind != fsp.OutText || !strings.HasPrefix(msgs[0].Text, "outbox.file_summary(") {
		t.Errorf("first message = %+v, want the summary", msgs[0])
	}
	if !strings.Contains(msgs[0].Text, "Chars=600") {
		t.Errorf("summary = %q, want the total character count", msgs[0].Text)
	}
	// The summary quotes the opening 300 characters, not the whole turn.
	if !strings.Contains(msgs[0].Text, "Head="+longText(300)+")") {
		t.Errorf("summary = %q, want the first 300 characters as the head", msgs[0].Text)
	}
	if msgs[1].Kind != fsp.OutFile {
		t.Fatalf("second message = %+v, want a file message", msgs[1])
	}
	if msgs[1].FilePath == "" {
		t.Fatal("file message carries no path")
	}

	// The file lives under <home>/outbox/<endpoint hash>/ and holds everything.
	wantDir := filepath.Join(h.home, "outbox", endpointHash(testEndpoint))
	if got := filepath.Dir(msgs[1].FilePath); got != wantDir {
		t.Errorf("file directory = %q, want %q", got, wantDir)
	}
	if !strings.Contains(filepath.Base(msgs[1].FilePath), string(sessionA)) {
		t.Errorf("file name = %q, want the session in it", filepath.Base(msgs[1].FilePath))
	}
	data, err := os.ReadFile(msgs[1].FilePath)
	if err != nil {
		t.Fatalf("read %s: %v", msgs[1].FilePath, err)
	}
	if string(data) != text {
		t.Errorf("file holds %d characters, want the whole turn of %d",
			utf8.RuneCountInString(string(data)), utf8.RuneCountInString(text))
	}
	info, err := os.Stat(msgs[1].FilePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows has no POSIX permission bits: os.Chmod there only toggles the
	// read-only attribute, so Perm() reports 0666 however the file was created.
	// Ferry therefore does NOT restrict this file on Windows beyond whatever the
	// containing directory's ACL already gives it. That is a real gap, not a
	// test artefact — hardening it needs Windows ACLs, which is a contract
	// decision for P4, not something to assert away here.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	// One row recorded, and the whole thing cost two replies.
	longs := h.store.longOutputs()
	if len(longs) != 1 {
		t.Fatalf("long output rows = %d, want 1", len(longs))
	}
	if longs[0].Chars != 600 || longs[0].Session != sessionA || longs[0].Endpoint != testEndpoint {
		t.Errorf("row = %+v, want 600 characters for session A", longs[0])
	}
	if got := h.store.window(t, testEndpoint).Used; got != 2 {
		t.Errorf("window used = %d, want 2 (summary plus file)", got)
	}
	if got := h.store.row(t, 1).State; got != fsp.DeliveryAccepted {
		t.Errorf("row 1 state = %q, want accepted", got)
	}
}

// TestLongOutputSplitMode: with long_output = split the turn stays in chat.
func TestLongOutputSplitMode(t *testing.T) {
	h := fileBox(t, func(c *config.OutboxConfig) { c.LongOutput = config.LongOutputSplit })
	h.open("ctx-1")
	h.enqueue(sessionA, longText(300), true)
	h.box.pump(h.ctx)

	for _, m := range h.im.messages() {
		if m.Kind == fsp.OutFile {
			t.Fatalf("split mode sent a file message: %+v", m)
		}
	}
	if len(h.store.longOutputs()) != 0 {
		t.Errorf("split mode recorded %d long outputs, want 0", len(h.store.longOutputs()))
	}
}

// TestApprovalNeverBecomesFile: a verdict is answered in chat, however long the
// request is.
func TestApprovalNeverBecomesFile(t *testing.T) {
	h := fileBox(t, nil)
	h.open("ctx-1")
	h.enqueueKind(sessionA, longText(600), fsp.OutApproval, true)
	h.box.pump(h.ctx)

	for _, m := range h.im.messages() {
		if m.Kind == fsp.OutFile {
			t.Fatalf("an approval request became a file: %+v", m)
		}
	}
}

func TestFileKeepLimit(t *testing.T) {
	h := fileBox(t, func(c *config.OutboxConfig) { c.FileKeep = 2 })

	var paths []string
	for i := range 4 {
		h.clk.Advance(time.Second)
		h.open("ctx-1")
		h.enqueue(sessionA, longText(200+i), true)
		h.box.pump(h.ctx)
		msgs := h.im.messages()
		paths = append(paths, msgs[len(msgs)-1].FilePath)
	}

	longs := h.store.longOutputs()
	if len(longs) != 2 {
		t.Fatalf("long output rows = %d, want file_keep = 2", len(longs))
	}
	for _, lo := range longs {
		if lo.Path != paths[2] && lo.Path != paths[3] {
			t.Errorf("kept %q, want one of the two newest files", lo.Path)
		}
	}
	// The dropped files are gone from disk as well.
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists, want it removed once beyond file_keep", p)
		}
	}
	for _, p := range paths[2:] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed, want the newest files kept: %v", p, err)
		}
	}
}

func TestFileResend(t *testing.T) {
	h := fileBox(t, nil)
	h.open("ctx-1")
	h.enqueue(sessionA, longText(200), true)
	h.box.pump(h.ctx)
	h.clk.Advance(time.Second)
	h.enqueue(sessionA, longText(300), true)
	h.box.pump(h.ctx)

	sent := h.im.messages()
	first, second := sent[1].FilePath, sent[3].FilePath
	h.im.reset()

	h.clk.Advance(time.Minute)
	h.open("ctx-2")
	if err := h.box.File(h.ctx, testEndpoint, 1); err != nil {
		t.Fatalf("File(1): %v", err)
	}
	msgs := h.im.messages()
	if len(msgs) != 1 || msgs[0].Kind != fsp.OutFile {
		t.Fatalf("File(1) sent %+v, want one file message", msgs)
	}
	// 1 is the most recent file.
	if msgs[0].FilePath != second {
		t.Errorf("File(1) sent %q, want the newest file %q", msgs[0].FilePath, second)
	}
	if got := h.store.window(t, testEndpoint).Used; got != 1 {
		t.Errorf("window used = %d, want 1: a resend costs one reply", got)
	}

	h.im.reset()
	if err := h.box.File(h.ctx, testEndpoint, 2); err != nil {
		t.Fatalf("File(2): %v", err)
	}
	if got := h.im.messages()[0].FilePath; got != first {
		t.Errorf("File(2) sent %q, want the older file %q", got, first)
	}
}

func TestFileMissing(t *testing.T) {
	h := fileBox(t, nil)
	h.open("ctx-1")

	// Nothing has ever been written.
	if err := h.box.File(h.ctx, testEndpoint, 1); err != nil {
		t.Fatalf("File(1): %v", err)
	}
	if got := h.sentTexts(); len(got) != 1 || got[0] != "outbox.file_missing(N=1)" {
		t.Errorf("File(1) sent %q, want outbox.file_missing", got)
	}

	// A row whose file has been removed behind Ferry's back reads the same way.
	h.im.reset()
	h.enqueue(sessionA, longText(200), true)
	h.box.pump(h.ctx)
	path := h.im.messages()[1].FilePath
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	h.im.reset()
	h.clk.Advance(time.Minute)
	h.open("ctx-2")
	if err := h.box.File(h.ctx, testEndpoint, 1); err != nil {
		t.Fatalf("File(1) after removal: %v", err)
	}
	if got := h.sentTexts(); len(got) != 1 || got[0] != "outbox.file_missing(N=1)" {
		t.Errorf("File(1) after removal sent %q, want outbox.file_missing", got)
	}
}

// TestFileFallbackToSplit: when the platform refuses the file message, the turn
// still reaches the operator as text (spec-p2 3.12).
func TestFileFallbackToSplit(t *testing.T) {
	h := fileBox(t, func(c *config.OutboxConfig) { c.LongOutputThreshold = 100 })
	h.open("ctx-1")
	// The summary goes through; the file message is refused.
	h.im.script(
		result{state: fsp.DeliveryAccepted},
		result{state: fsp.DeliveryRejected, err: errors.New("file upload failed")},
	)

	text := longText(250)
	h.enqueue(sessionA, text, true)
	h.box.pump(h.ctx)

	msgs := h.im.messages()
	if len(msgs) < 3 {
		t.Fatalf("sent %d messages, want the summary, the refused file and the text: %q", len(msgs), h.sentTexts())
	}
	if msgs[1].Kind != fsp.OutFile {
		t.Errorf("second message = %+v, want the file attempt", msgs[1])
	}
	var rebuilt strings.Builder
	for _, m := range msgs[2:] {
		if m.Kind != fsp.OutText {
			t.Errorf("fallback message = %+v, want plain text", m)
		}
		rebuilt.WriteString(m.Text)
	}
	if rebuilt.String() != text {
		t.Errorf("fallback text is %d characters, want the whole turn of %d",
			utf8.RuneCountInString(rebuilt.String()), utf8.RuneCountInString(text))
	}
	if got := h.store.row(t, 1).State; got != fsp.DeliveryAccepted {
		t.Errorf("row 1 state = %q, want accepted: the text got through", got)
	}
	// The record stays so that /file can try the same file again later.
	if len(h.store.longOutputs()) != 1 {
		t.Errorf("long output rows = %d, want the record kept for /file", len(h.store.longOutputs()))
	}
}

// TestFileNeedsTwoReplies: with only one reply left the turn is split rather
// than turned into a summary with no file behind it.
func TestFileNeedsTwoReplies(t *testing.T) {
	h := fileBox(t, func(c *config.OutboxConfig) { c.ReplyBudget = 2 })
	h.open("ctx-1")
	h.enqueue(sessionA, "first", true)
	h.box.pump(h.ctx)
	h.im.reset()

	h.enqueue(sessionA, longText(200), true)
	h.box.pump(h.ctx)

	for _, m := range h.im.messages() {
		if m.Kind == fsp.OutFile {
			t.Errorf("sent a file with one reply left: %+v", m)
		}
	}
}

// TestEndpointHashIsOpaque: the directory name gives nothing away about who the
// chat peer is.
func TestEndpointHashIsOpaque(t *testing.T) {
	h := endpointHash(testEndpoint)
	if len(h) != 16 {
		t.Errorf("hash = %q, want 16 hex characters", h)
	}
	if strings.Contains(h, testEndpoint.PeerID) || strings.Contains(h, testEndpoint.BotID) {
		t.Errorf("hash %q leaks the endpoint", h)
	}
	if endpointHash(fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "other"}) == h {
		t.Error("two endpoints share a directory")
	}
}
