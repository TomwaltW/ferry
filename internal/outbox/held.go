package outbox

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// digestPreview is how much of a held message a digest line shows.
const digestPreview = 40

// flushReserve keeps a reply each for the digest, the acceptance receipt and
// the result of the turn the operator has just started (spec-p2 3.6).
const flushReserve = 3

// FlushHeld redelivers the backlog after the core has reopened the window: a
// digest first, then as many held messages as the budget allows.
func (b *box) FlushHeld(ctx context.Context, ep fsp.EndpointID) error {
	held, err := b.held(ctx, ep)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		return nil
	}
	win, _, open, err := b.window(ctx, ep)
	if err != nil {
		return err
	}
	if !open {
		// Nothing can be said yet; the backlog keeps waiting.
		return nil
	}

	batch := b.budget(ep, win) - flushReserve
	if batch < 0 {
		batch = 0
	}
	if batch > len(held) {
		batch = len(held)
	}
	remaining := len(held) - batch

	if err := b.sendOwn(ctx, ep, &win, fsp.Outbound{
		Kind:   fsp.OutText,
		Text:   b.digest(ctx, ep, held, remaining),
		Urgent: true,
	}); err != nil {
		return err
	}
	return b.redeliver(ctx, ep, &win, held[:batch])
}

// Pull sends the next batch of held messages, keeping one reply for the result
// of the turn in progress.
func (b *box) Pull(ctx context.Context, ep fsp.EndpointID) error {
	held, err := b.held(ctx, ep)
	if err != nil {
		return err
	}
	win, _, open, err := b.window(ctx, ep)
	if err != nil {
		return err
	}
	if !open {
		return nil
	}
	if len(held) == 0 {
		return b.sendOwn(ctx, ep, &win, fsp.Outbound{
			Kind:   fsp.OutText,
			Text:   b.t(ep, "outbox.nothing_held", nil),
			Urgent: true,
		})
	}
	batch := b.budget(ep, win) - 1
	if batch < 1 {
		return nil
	}
	if batch > len(held) {
		batch = len(held)
	}
	return b.redeliver(ctx, ep, &win, held[:batch])
}

// Held reports how many messages are waiting for a window.
func (b *box) Held(ep fsp.EndpointID) int {
	rows, err := b.store.OutboxByState(context.Background(), ep, fsp.DeliveryHeld)
	if err != nil {
		b.log.Error("outbox: count held", "endpoint", ep.String(), "err", err)
		return 0
	}
	return len(rows)
}

// held returns the backlog in redelivery order: urgent messages first, then by
// the time they were queued.
func (b *box) held(ctx context.Context, ep fsp.EndpointID) ([]ports.OutboxRow, error) {
	rows, err := b.store.OutboxByState(ctx, ep, fsp.DeliveryHeld)
	if err != nil {
		return nil, fmt.Errorf("outbox: list held: %w", err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Msg.Urgent != rows[j].Msg.Urgent {
			return rows[i].Msg.Urgent
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	return rows, nil
}

// redeliver sends one held message per reply, oldest first.
func (b *box) redeliver(ctx context.Context, ep fsp.EndpointID, win *ports.Window, rows []ports.OutboxRow) error {
	for _, r := range rows {
		if ctx.Err() != nil {
			return nil
		}
		if b.budget(ep, *win) < 1 {
			return nil
		}
		if err := b.deliver(ctx, ep, win, r, []ports.OutboxRow{r}, 1); err != nil {
			return err
		}
	}
	return nil
}

// digest is the "you missed N messages" summary: one line per held message up
// to digest_max, and a pointer to /pull for whatever is left over.
func (b *box) digest(ctx context.Context, ep fsp.EndpointID, held []ports.OutboxRow, remaining int) string {
	max := b.cfg.DigestMax
	if max < 1 {
		max = len(held)
	}
	slots := b.slots(ctx, ep)
	lines := make([]string, 0, max)
	for _, r := range held {
		if len(lines) == max {
			break
		}
		lines = append(lines, digestLine(slots, r))
	}
	text := b.t(ep, "outbox.digest", map[string]any{
		"N":     len(held),
		"Lines": strings.Join(lines, "\n"),
	})
	if remaining > 0 {
		text += "\n" + b.t(ep, "outbox.digest_more", map[string]any{"N": remaining})
	}
	return text
}

// digestLine is "<session>. <kind> <first 40 characters>".
func digestLine(slots map[fsp.SessionID]int, r ports.OutboxRow) string {
	who := "-"
	if slot, ok := slots[r.Msg.Session]; ok {
		who = fmt.Sprintf("%d", slot)
	}
	preview := r.Msg.Text
	if preview == "" && r.Msg.FilePath != "" {
		preview = shortPath(r.Msg.FilePath)
	}
	preview = strings.Join(strings.Fields(preview), " ")
	if runes := []rune(preview); len(runes) > digestPreview {
		preview = string(runes[:digestPreview])
	}
	return strings.TrimRight(fmt.Sprintf("%s. %s %s", who, r.Msg.Kind, preview), " ")
}

// shortPath keeps a file message's digest line to its base name.
func shortPath(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i != -1 {
		return p[i+1:]
	}
	return p
}

// slots maps each of the endpoint's sessions to the number the operator sees.
func (b *box) slots(ctx context.Context, ep fsp.EndpointID) map[fsp.SessionID]int {
	bindings, err := b.store.Bindings(ctx, ep)
	if err != nil {
		b.log.Debug("outbox: bindings", "endpoint", ep.String(), "err", err)
		return nil
	}
	out := make(map[fsp.SessionID]int, len(bindings))
	for _, bd := range bindings {
		out[bd.Session] = bd.Slot
	}
	return out
}
