package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fileHead is how much of a long output the summary message quotes.
const fileHead = 300

// fileReplies is what one long output costs: the summary and the file itself.
const fileReplies = 2

// endpointHash names the per-endpoint directory under <home>/outbox. A chat
// peer ID has no business appearing in a path.
func endpointHash(ep fsp.EndpointID) string {
	sum := sha256.Sum256([]byte(ep.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// fileWanted reports whether this delivery should become a file rather than a
// run of messages. Approvals never do: a verdict is answered in chat.
func (b *box) fileWanted(primary ports.OutboxRow, text string, maxParts int) bool {
	switch {
	case b.cfg.LongOutput != config.LongOutputFile:
		return false
	case b.home == "":
		return false
	case primary.Msg.Kind != fsp.OutText:
		return false
	case maxParts < fileReplies:
		return false
	case b.cfg.LongOutputThreshold < 1:
		return false
	}
	return utf8.RuneCountInString(text) > b.cfg.LongOutputThreshold
}

// sendAsFile writes the turn to <home>/outbox/<endpoint>/<time>-<session>.md and
// sends the summary followed by the file. It reports the delivery state, how
// many attempts it took, how many replies it spent, and whether the caller is
// done: a file message that does not get through falls back to split text
// (spec-p2 3.6).
func (b *box) sendAsFile(ctx context.Context, ep fsp.EndpointID, win *ports.Window, primary ports.OutboxRow, text string) (fsp.DeliveryState, int, int, bool) {
	path, err := b.writeLongOutput(ctx, ep, primary.Msg.Session, text)
	if err != nil {
		b.log.Error("outbox: write long output", "endpoint", ep.String(), "err", err)
		return fsp.DeliveryAccepted, 0, 0, false
	}

	attempts, spent := 0, 0
	summary := primary.Msg
	summary.Kind = fsp.OutText
	summary.Text = b.t(ep, "outbox.file_summary", map[string]any{
		"Chars": utf8.RuneCountInString(text),
		"Head":  head(text, fileHead),
	})
	summary.DedupeKey = dedupeKey(primary, 0)
	st, tries := b.send(ctx, ep, win, summary)
	attempts += tries
	if st == fsp.DeliveryAccepted || st == fsp.DeliveryUnknown {
		if err := b.spend(ctx, ep, win); err != nil {
			b.log.Error("outbox: save window", "endpoint", ep.String(), "err", err)
		}
		spent++
	}
	if st != fsp.DeliveryAccepted {
		return st, attempts, spent, true
	}

	file := primary.Msg
	file.Kind = fsp.OutFile
	file.FilePath = path
	file.Text = filepath.Base(path)
	file.DedupeKey = dedupeKey(primary, 1)
	st, tries = b.send(ctx, ep, win, file)
	attempts += tries
	if st == fsp.DeliveryAccepted || st == fsp.DeliveryUnknown {
		if err := b.spend(ctx, ep, win); err != nil {
			b.log.Error("outbox: save window", "endpoint", ep.String(), "err", err)
		}
		spent++
	}
	if st != fsp.DeliveryAccepted {
		// The row stays: /file can try the same file again later.
		b.log.Warn("outbox: file message failed, falling back to text",
			"endpoint", ep.String(), "state", st)
		return fsp.DeliveryAccepted, attempts, spent, false
	}
	return fsp.DeliveryAccepted, attempts, spent, true
}

// writeLongOutput stores the text and records it, then drops whatever is now
// beyond file_keep.
func (b *box) writeLongOutput(ctx context.Context, ep fsp.EndpointID, session fsp.SessionID, text string) (string, error) {
	dir := filepath.Join(b.home, "outbox", endpointHash(ep))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	now := b.clk.Now()
	name := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405.000"), safeName(string(session)))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := b.store.AddLongOutput(ctx, ports.LongOutput{
		Endpoint:  ep,
		Session:   session,
		Path:      path,
		Chars:     utf8.RuneCountInString(text),
		CreatedAt: now,
	}); err != nil {
		os.Remove(path)
		return "", err
	}
	b.pruneLongOutputs(ctx, ep)
	return path, nil
}

// pruneLongOutputs keeps only the newest file_keep files of an endpoint.
func (b *box) pruneLongOutputs(ctx context.Context, ep fsp.EndpointID) {
	keep := b.cfg.FileKeep
	if keep < 1 {
		return
	}
	rows, err := b.store.LongOutputs(ctx, ep, 0)
	if err != nil {
		b.log.Error("outbox: list long outputs", "endpoint", ep.String(), "err", err)
		return
	}
	if len(rows) < keep+1 {
		return
	}
	for _, lo := range rows[keep:] {
		if err := os.Remove(lo.Path); err != nil && !os.IsNotExist(err) {
			b.log.Warn("outbox: remove long output", "row", lo.ID, "err", err)
		}
		if err := b.store.DeleteLongOutput(ctx, lo.ID); err != nil {
			b.log.Error("outbox: delete long output", "row", lo.ID, "err", err)
		}
	}
}

// File resends the nth long output, 1 being the most recent.
func (b *box) File(ctx context.Context, ep fsp.EndpointID, n int) error {
	win, _, open, err := b.window(ctx, ep)
	if err != nil {
		return err
	}
	if !open {
		return nil
	}
	if n < 1 {
		n = 1
	}
	rows, err := b.store.LongOutputs(ctx, ep, n)
	if err != nil {
		return fmt.Errorf("outbox: list long outputs: %w", err)
	}
	if len(rows) < n {
		return b.missingFile(ctx, ep, &win, n)
	}
	lo := rows[n-1]
	if _, err := os.Stat(lo.Path); err != nil {
		b.log.Warn("outbox: long output is gone", "row", lo.ID, "err", err)
		return b.missingFile(ctx, ep, &win, n)
	}
	return b.sendOwn(ctx, ep, &win, fsp.Outbound{
		Session:   lo.Session,
		Kind:      fsp.OutFile,
		FilePath:  lo.Path,
		Text:      filepath.Base(lo.Path),
		Urgent:    true,
		DedupeKey: fmt.Sprintf("ob-file-%d-%d-%d", lo.ID, win.OpenedAt.UnixMilli(), b.seq.Add(1)),
	})
}

func (b *box) missingFile(ctx context.Context, ep fsp.EndpointID, win *ports.Window, n int) error {
	return b.sendOwn(ctx, ep, win, fsp.Outbound{
		Kind:   fsp.OutText,
		Text:   b.t(ep, "outbox.file_missing", map[string]any{"N": n}),
		Urgent: true,
	})
}

// head returns the first n runes of text.
func head(text string, n int) string {
	runes := []rune(text)
	if len(runes) < n+1 {
		return text
	}
	return string(runes[:n])
}

// safeName keeps a session ID usable as part of a file name.
func safeName(s string) string {
	if s == "" {
		return "session"
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
	if runes := []rune(clean); len(runes) > 64 {
		clean = string(runes[:64])
	}
	return clean
}
