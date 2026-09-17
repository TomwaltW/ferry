package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// pump walks every endpoint that has queued messages.
func (b *box) pump(ctx context.Context) {
	defer b.passes.Add(1)
	eps, err := b.store.OutboxEndpoints(ctx, fsp.DeliveryQueued)
	if err != nil {
		b.log.Error("outbox: list endpoints", "err", err)
		return
	}
	for _, ep := range eps {
		if ctx.Err() != nil {
			return
		}
		if err := b.pumpEndpoint(ctx, ep); err != nil {
			b.log.Error("outbox: pump", "endpoint", ep.String(), "err", err)
		}
	}
}

// pumpEndpoint applies the budget rules of spec 5.5 to one endpoint:
// urgent messages go out while a reply is left, non-urgent ones merge per
// session and always leave one reply for an urgent message.
func (b *box) pumpEndpoint(ctx context.Context, ep fsp.EndpointID) error {
	rows, err := b.store.OutboxByState(ctx, ep, fsp.DeliveryQueued)
	if err != nil {
		return fmt.Errorf("list queued: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	win, err := b.store.LoadWindow(ctx, ep)
	switch {
	case errors.Is(err, fsp.ErrNotFound):
		return b.rejectAll(ctx, ep, rows, "no reply window")
	case err != nil:
		return fmt.Errorf("load window: %w", err)
	case win.ReplyCtx == "":
		return b.rejectAll(ctx, ep, rows, "reply window without a reply context")
	}

	var urgent, normal []ports.OutboxRow
	for _, r := range rows {
		if r.Msg.Urgent {
			urgent = append(urgent, r)
			continue
		}
		normal = append(normal, r)
	}

	for _, u := range urgent {
		if ctx.Err() != nil {
			return nil
		}
		budget := b.cfg.ReplyBudget - win.Used
		if budget < 1 {
			// P2 redelivers held messages; P1 only stops sending.
			if err := b.store.UpdateOutbox(ctx, u.ID, fsp.DeliveryHeld, u.Attempts, b.clk.Now()); err != nil {
				return fmt.Errorf("hold %d: %w", u.ID, err)
			}
			b.log.Warn("outbox: reply budget exhausted, message held",
				"endpoint", ep.String(), "row", u.ID)
			continue
		}
		// Anything non-urgent still waiting for this session rides along at the
		// front of the urgent message instead of spending a reply of its own.
		var merged []ports.OutboxRow
		rest := normal[:0]
		for _, n := range normal {
			if n.Msg.Session == u.Msg.Session {
				merged = append(merged, n)
				continue
			}
			rest = append(rest, n)
		}
		normal = rest
		consumed := make([]ports.OutboxRow, 0, len(merged)+1)
		consumed = append(consumed, merged...)
		consumed = append(consumed, u)
		if err := b.deliver(ctx, ep, &win, u, consumed, budget); err != nil {
			return err
		}
	}

	now := b.clk.Now()
	for _, group := range bySession(normal) {
		if ctx.Err() != nil {
			return nil
		}
		// The oldest message in the group sets the merge deadline.
		if now.Sub(group[0].CreatedAt) < b.cfg.MergeInterval.Duration {
			continue
		}
		budget := b.cfg.ReplyBudget - win.Used
		// One reply always stays reserved for an urgent message.
		if budget < 2 {
			continue
		}
		if err := b.deliver(ctx, ep, &win, group[0], group, budget-1); err != nil {
			return err
		}
	}
	return nil
}

// bySession groups rows by session, keeping both the groups and the rows
// inside them in queue order.
func bySession(rows []ports.OutboxRow) [][]ports.OutboxRow {
	var order []fsp.SessionID
	groups := make(map[fsp.SessionID][]ports.OutboxRow, len(rows))
	for _, r := range rows {
		if _, ok := groups[r.Msg.Session]; !ok {
			order = append(order, r.Msg.Session)
		}
		groups[r.Msg.Session] = append(groups[r.Msg.Session], r)
	}
	out := make([][]ports.OutboxRow, 0, len(order))
	for _, s := range order {
		out = append(out, groups[s])
	}
	return out
}

func (b *box) rejectAll(ctx context.Context, ep fsp.EndpointID, rows []ports.OutboxRow, reason string) error {
	now := b.clk.Now()
	for _, r := range rows {
		if err := b.store.UpdateOutbox(ctx, r.ID, fsp.DeliveryRejected, r.Attempts, now); err != nil {
			return fmt.Errorf("reject %d: %w", r.ID, err)
		}
		b.audit(ctx, ep, r.Msg.Session, reason)
	}
	return nil
}

// deliver sends the text of consumed as maxParts messages at most, then writes
// the outcome back to every row it consumed.
func (b *box) deliver(ctx context.Context, ep fsp.EndpointID, win *ports.Window, primary ports.OutboxRow, consumed []ports.OutboxRow, maxParts int) error {
	parts := b.segments(ep, joinTexts(consumed), maxParts)

	state := fsp.DeliveryAccepted
	attempts := 0
	for i, part := range parts {
		msg := primary.Msg
		msg.Text = part
		msg.DedupeKey = dedupeKey(primary, i)

		st, tries := b.send(ctx, ep, win, msg)
		attempts += tries
		if st == fsp.DeliveryAccepted || st == fsp.DeliveryUnknown {
			// An unknown send may well have arrived, so it spends a reply too.
			win.Used++
			if err := b.store.SaveWindow(ctx, *win); err != nil {
				return fmt.Errorf("save window: %w", err)
			}
		}
		if st != fsp.DeliveryAccepted {
			state = st
			break
		}
	}

	now := b.clk.Now()
	for _, r := range consumed {
		if err := b.store.UpdateOutbox(ctx, r.ID, state, r.Attempts+attempts, now); err != nil {
			return fmt.Errorf("update %d: %w", r.ID, err)
		}
	}
	return nil
}

// send performs one message, retrying an unknown outcome with the same dedupe
// key so that the platform discards a duplicate.
func (b *box) send(ctx context.Context, ep fsp.EndpointID, win *ports.Window, msg fsp.Outbound) (fsp.DeliveryState, int) {
	attempts := 0
	for try := 0; ; try++ {
		state, err := b.sendOnce(ctx, msg, win.ReplyCtx)
		attempts++
		switch state {
		case fsp.DeliveryAccepted:
			return state, attempts
		case fsp.DeliveryUnknown:
			if try < len(retryDelays) {
				select {
				case <-b.clk.After(retryDelays[try]):
					continue
				case <-ctx.Done():
					return state, attempts
				}
			}
			b.audit(ctx, ep, msg.Session, fmt.Sprintf("delivery unknown after %d attempts", attempts))
			return state, attempts
		default:
			b.audit(ctx, ep, msg.Session, "send rejected: "+errText(err))
			return fsp.DeliveryRejected, attempts
		}
	}
}

// sendOnce calls the adapter under the configured send timeout and maps a bare
// error onto a delivery state.
func (b *box) sendOnce(ctx context.Context, msg fsp.Outbound, replyCtx string) (fsp.DeliveryState, error) {
	sendCtx := ctx
	if d := b.cfg.SendTimeout.Duration; d > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	state, err := b.im.Send(sendCtx, msg, replyCtx)
	if err == nil {
		return state, nil
	}
	if state != "" {
		return state, err
	}
	if errors.Is(err, fsp.ErrNoWindow) {
		return fsp.DeliveryRejected, err
	}
	// A timeout or a transport error leaves the outcome open; retry the same
	// dedupe key rather than dropping the message.
	return fsp.DeliveryUnknown, err
}

func errText(err error) string {
	if err == nil {
		return "unspecified"
	}
	return err.Error()
}

// dedupeKey keeps the key stable across retries. Segment numbers start at 1;
// a caller-supplied key keeps its own spelling for the first segment.
func dedupeKey(primary ports.OutboxRow, segment int) string {
	if primary.Msg.DedupeKey == "" {
		return fmt.Sprintf("ob-%d-%d", primary.ID, segment+1)
	}
	if segment == 0 {
		return primary.Msg.DedupeKey
	}
	return fmt.Sprintf("%s-%d", primary.Msg.DedupeKey, segment+1)
}

func joinTexts(rows []ports.OutboxRow) string {
	texts := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Msg.Text == "" {
			continue
		}
		texts = append(texts, r.Msg.Text)
	}
	return strings.Join(texts, "\n\n")
}
