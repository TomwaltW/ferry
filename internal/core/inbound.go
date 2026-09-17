package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// handleInbound runs the eleven steps of spec-p1 3.6 for one inbound message.
func (d *Daemon) handleInbound(ctx context.Context, in fsp.Inbound) {
	ep := in.Endpoint

	// 1. Duplicates never reach anything else.
	seen, err := d.deps.Store.SeenMessage(ctx, ep, in.MessageID, in.ReceivedAt)
	if err != nil {
		d.log.Error("dedupe", "endpoint", ep.String(), "error", err)
	}
	if seen {
		return
	}

	// 2. Strangers only ever get a pairing code, whatever they sent.
	if !d.deps.ACL.Allowed(ep, in.SenderID) {
		// The window is opened here as well, otherwise the pairing code of a
		// first-time sender has no reply context to travel on.
		d.setWindow(ep, in.ReplyCtx)
		d.deps.Outbox.OpenWindow(ep, in.ReplyCtx, in.ReceivedAt)
		code, err := d.deps.ACL.OfferPairing(ctx, ep, in.SenderID)
		switch {
		case errors.Is(err, fsp.ErrQuotaExceeded):
			d.reply(ctx, ep, "pair.busy", nil)
		case err != nil:
			d.log.Error("offer pairing", "endpoint", ep.String(), "error", err)
		default:
			d.reply(ctx, ep, "pair.code", map[string]any{"Code": code})
		}
		d.audit(ctx, ports.AuditEntry{Kind: "rejected", Endpoint: ep, SenderID: in.SenderID, Detail: "not allowed"})
		return
	}

	// 3. Every allowed message refreshes the reply window.
	d.setWindow(ep, in.ReplyCtx)
	d.deps.Outbox.OpenWindow(ep, in.ReplyCtx, in.ReceivedAt)
	if err := d.deps.Outbox.FlushHeld(ctx, ep); err != nil {
		d.log.Error("flush held", "endpoint", ep.String(), "error", err)
	}

	// 4. A verdict is a whole message and only counts from an approver.
	if id, decision, ok := d.deps.Approvals.ParseVerdict(in.Text); ok {
		if d.deps.ACL.IsApprover(ep, in.SenderID) {
			d.deps.Approvals.Resolve(ctx, id, decision, in.SenderID)
		}
		return
	}

	// 5. "//" escapes a message that would otherwise look like a command.
	text := in.Text
	if unescaped, ok := d.deps.Commands.Unescape(text); ok {
		text = unescaped
	} else if cmd, ok := d.deps.Commands.Parse(text); ok {
		// 6.
		d.runCommand(ctx, in, cmd)
		return
	}

	// 7. Attachments on their own are not supported yet.
	if strings.TrimSpace(text) == "" {
		if len(in.Attachments) != 0 {
			d.reply(ctx, ep, "media.unsupported", nil)
		}
		return
	}

	// 8.
	s := d.foreground(ctx, ep)
	if s == nil {
		d.reply(ctx, ep, "hint.no_session", nil)
		return
	}
	if s.State() == fsp.StateParked {
		d.reply(ctx, ep, "session.parked", map[string]any{"Slot": s.slot})
		return
	}

	// 9. Anything not sent by the owner carries the sender along.
	prompt := text
	if role, err := d.deps.Store.Role(ctx, ep, in.SenderID); err != nil {
		d.log.Error("role", "endpoint", ep.String(), "error", err)
	} else if role != "owner" {
		prompt = "[from " + in.SenderID + "] " + text
	}

	// 10. A busy session collects the text instead of answering.
	if s.submit(ctx, prompt) {
		d.reply(ctx, ep, "turn.accepted", map[string]any{"Agent": s.agent(), "Slot": s.slot})
		d.typing(ctx, ep, true)
	}

	// 11. The body never reaches the audit log, only its length.
	d.audit(ctx, ports.AuditEntry{
		Kind:     "inbound",
		Endpoint: ep,
		SenderID: in.SenderID,
		Session:  s.id(),
		Detail:   fmt.Sprintf("len=%d", len([]rune(text))),
	})
}
