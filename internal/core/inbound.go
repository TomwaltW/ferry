package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// defaultMaxMediaMB is the attachment size limit when the configuration leaves
// it unset, per spec-p2 3.2.
const defaultMaxMediaMB = 20

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

	// 4. A verdict is a whole message and only counts from an approver. A
	// decision word without an ID is ordinary text unless an approval is
	// waiting here.
	if id, decision, ok := d.deps.Approvals.ParseVerdict(in.Text); ok {
		if id == "" {
			if d.bareVerdict(ctx, in, decision) {
				return
			}
		} else {
			if d.deps.ACL.IsApprover(ep, in.SenderID) {
				d.resolveVerdict(ctx, in, id, decision)
			}
			return
		}
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

	// 7. Attachments that made it to disk join the prompt; the ones that did
	// not are reported, and the text still goes through.
	attachments := d.attachmentLines(ctx, ep, in.Attachments)
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
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
	// A mirrored session the operator did not open to chat is read-only.
	if !s.allowsInject() {
		d.reply(ctx, ep, "inject.disabled", nil)
		return
	}

	// 9. Anything not sent by the bot owner carries the sender along.
	prompt := text
	if owner, err := d.isOwner(ctx, ep, in.SenderID); err != nil {
		d.log.Error("role", "endpoint", ep.String(), "error", err)
	} else if !owner {
		prompt = "[from " + in.SenderID + "] " + text
	}
	if len(attachments) != 0 {
		prompt = strings.TrimRight(prompt, "\n")
		if prompt != "" {
			prompt += "\n"
		}
		prompt += strings.Join(attachments, "\n")
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

// attachmentLines turns the attachments of an inbound message into the lines
// appended to the prompt. An attachment without a path never made it to disk:
// the endpoint is told why, and the agent never hears about it.
func (d *Daemon) attachmentLines(ctx context.Context, ep fsp.EndpointID, atts []fsp.Attachment) []string {
	var lines []string
	maxMB := d.maxMediaMB()
	for _, a := range atts {
		if a.Path != "" {
			lines = append(lines, d.t(ctx, ep, "prompt.attachment", map[string]any{"Path": a.Path}))
			continue
		}
		if a.Size > int64(maxMB)<<20 {
			d.reply(ctx, ep, "media.too_large", map[string]any{"Max": maxMB})
			continue
		}
		d.reply(ctx, ep, "media.download_failed", map[string]any{"Name": a.Name})
	}
	return lines
}

// maxMediaMB is the size past which an attachment is not downloaded at all.
func (d *Daemon) maxMediaMB() int {
	if n := d.deps.Config.WeChat.MaxMediaMB; n > 0 {
		return n
	}
	return defaultMaxMediaMB
}

// Before a decision word sent without an ID ("同意") may answer the approval
// waiting, the approval's question must have been delivered for bareShown, so
// it has been on screen; and no approval at the endpoint may have lapsed within
// bareAfterLapse, so an answer typed for one that just timed out does not land
// on the next.
const (
	bareShown      = 3 * time.Second
	bareAfterLapse = time.Minute
)

// bareVerdict handles a decision word sent without an ID ("同意", "y"). It
// reports false, leaving the word to be ordinary text, when the sender cannot
// decide or nothing is waiting at the endpoint. Otherwise the word never
// reaches an agent: it answers the one approval waiting when that cannot be
// mistaken, and the approver is asked to add the ID when it can.
func (d *Daemon) bareVerdict(ctx context.Context, in fsp.Inbound, decision fsp.Decision) bool {
	ep := in.Endpoint
	if !d.deps.ACL.IsApprover(ep, in.SenderID) {
		return false
	}
	open := d.deps.Approvals.Pending(ctx, ep)
	if len(open) == 0 {
		return false
	}
	if hasVoice(in.Attachments) && decision != fsp.DecisionReject {
		d.reply(ctx, ep, "approval.voice_allow", nil)
		return true
	}
	if d.bareAnswers(ctx, ep, open, decision) {
		d.deps.Approvals.Resolve(ctx, open[0].ID, decision, in.SenderID)
		return true
	}
	lines := make([]string, 0, len(open))
	for _, p := range open {
		lines = append(lines, d.t(ctx, ep, "approval.need_id_item", map[string]any{
			"ID": p.ID, "Slot": d.slotOf(ctx, ep, p.Session),
		}))
	}
	d.reply(ctx, ep, "approval.need_id", map[string]any{
		"N": len(open), "ID": open[len(open)-1].ID, "List": strings.Join(lines, "\n"),
	})
	return true
}

// bareAnswers reports whether a decision word without an ID can only mean the
// approval waiting: the only one at the endpoint, of the foreground session,
// shown long enough, with no other approval lapsed just before. "Always allow"
// is never given without the ID: a lone "a" is too easily an answer to
// something else, and it outlasts the approval.
func (d *Daemon) bareAnswers(ctx context.Context, ep fsp.EndpointID, open []ports.PendingApproval, decision fsp.Decision) bool {
	if decision == fsp.DecisionAllowAlways || len(open) != 1 {
		return false
	}
	now := d.deps.Clock.Now()
	if open[0].ShownAt.IsZero() || now.Sub(open[0].ShownAt) < bareShown {
		return false
	}
	if lapsed := d.deps.Approvals.Lapsed(ep); !lapsed.IsZero() && now.Sub(lapsed) < bareAfterLapse {
		return false
	}
	fg := d.foreground(ctx, ep)
	return fg != nil && fg.id() == open[0].Session
}

// resolveVerdict applies a verdict that names its approval. A spoken one may
// only refuse: transcription can drop the 不 of 不同意.
func (d *Daemon) resolveVerdict(ctx context.Context, in fsp.Inbound, id string, decision fsp.Decision) {
	if hasVoice(in.Attachments) && decision != fsp.DecisionReject {
		d.reply(ctx, in.Endpoint, "approval.voice_allow", nil)
		return
	}
	d.deps.Approvals.Resolve(ctx, id, decision, in.SenderID)
}

// hasVoice reports whether a message carries a voice recording, which on
// WeChat means its text is the platform's transcription of it.
func hasVoice(atts []fsp.Attachment) bool {
	for _, a := range atts {
		if a.Kind == "voice" {
			return true
		}
	}
	return false
}
