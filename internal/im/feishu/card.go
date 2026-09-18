package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The verdicts a card button carries, the same letters the text form of a
// verdict starts with (spec-p4-b1 3.6, F15).
const (
	VerdictAllowOnce   = "y"
	VerdictAllowAlways = "a"
	VerdictReject      = "n"
)

// Card is one approval card: a title, the message text as plain text, and the
// buttons under it. The client turns it into Feishu's card JSON (cardJSON).
type Card struct {
	Title   string
	Body    string
	Buttons []CardButton
}

// CardButton is one button of a card and the value Feishu hands back when it
// is clicked.
type CardButton struct {
	Text  string
	Value CardValue
}

// CardValue is the value bound to an approval button, sent back verbatim by
// the card callback.
type CardValue struct {
	Approval string `json:"ferry_approval"`
	Verdict  string `json:"verdict"`
}

// Map is the value as the callback delivers it: a JSON object decoded into a
// map.
func (v CardValue) Map() map[string]any {
	return map[string]any{"ferry_approval": v.Approval, "verdict": v.Verdict}
}

// CardAction is one click on a card button, as the long-lived connection
// reports it.
type CardAction struct {
	EventID string         // header.event_id of the callback
	Token   string         // event.token, the card's update token
	OpenID  string         // event.operator.open_id, who clicked
	Value   map[string]any // event.action.value
}

// approvalIDRe is the shape of an approval ID (approval/verdict.go).
var approvalIDRe = regexp.MustCompile(`^[a-km-z]{5}$`)

// parseCardValue reads the value of an approval button. Anything else, from a
// card this package did not send or a value it did not write, reports false.
func parseCardValue(v map[string]any) (CardValue, bool) {
	id, ok := v["ferry_approval"].(string)
	if !ok || !approvalIDRe.MatchString(id) {
		return CardValue{}, false
	}
	verdict, ok := v["verdict"].(string)
	if !ok {
		return CardValue{}, false
	}
	switch verdict {
	case VerdictAllowOnce, VerdictAllowAlways, VerdictReject:
	default:
		return CardValue{}, false
	}
	return CardValue{Approval: id, Verdict: verdict}, true
}

// approvalCard builds the card for an approval message, or reports false when
// the message is not one: no i18n to write the buttons with, a file, or a
// dedupe key that names no approval.
func (a *Adapter) approvalCard(msg fsp.Outbound) (Card, bool) {
	if a.i18n == nil || msg.Kind == fsp.OutFile {
		return Card{}, false
	}
	ref, ok := ports.ParseApprovalDedupeKey(msg.DedupeKey)
	if !ok {
		return Card{}, false
	}
	lang := a.langOf(msg.Endpoint)
	button := func(key, verdict string) CardButton {
		return CardButton{
			Text:  a.i18n.T(lang, key, nil),
			Value: CardValue{Approval: ref.ID, Verdict: verdict},
		}
	}
	buttons := []CardButton{button("approval.button_allow_once", VerdictAllowOnce)}
	if ref.AllowAlways {
		buttons = append(buttons, button("approval.button_allow_always", VerdictAllowAlways))
	}
	buttons = append(buttons, button("approval.button_reject", VerdictReject))
	return Card{
		Title:   a.i18n.T(lang, "approval.card_title", map[string]any{"ID": ref.ID}),
		Body:    msg.Text,
		Buttons: buttons,
	}, true
}

func (a *Adapter) langOf(ep fsp.EndpointID) fsp.Lang {
	if a.lang == nil {
		return fsp.LangZH
	}
	return a.lang(ep)
}

// cardAction turns a click into the text verdict "<y|a|n> <id>" and hands it
// to the core, which authorises and resolves it exactly as if it had been
// typed. It answers with the toast to show, or false for a click this package
// does not own, which gets no toast.
func (a *Adapter) cardAction(ctx context.Context, out chan<- fsp.Inbound, act CardAction) (string, bool) {
	v, ok := parseCardValue(act.Value)
	if !ok {
		a.log.Debug("feishu: card action with a foreign value ignored")
		return "", false
	}
	if act.OpenID == "" {
		a.log.Warn("feishu: card action without an operator ignored", "event", act.EventID)
		return "", false
	}
	a.markLive()

	now := a.clk.Now()
	id := act.EventID
	if id == "" {
		id = act.Token
	}
	if id == "" {
		// Neither is expected to be missing; the time and a counter still keep
		// two clicks apart.
		id = act.OpenID + "-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(a.clicks.Add(1), 10)
	}
	in := fsp.Inbound{
		Endpoint:   a.endpoint(act.OpenID),
		SenderID:   act.OpenID,
		MessageID:  "card-" + id,
		Text:       v.Verdict + " " + v.Approval,
		ReceivedAt: now,
	}
	// The core reads out through a buffered queue (64 in internal/core), so
	// this only waits when that backlog is full; then the toast can miss the
	// three seconds Feishu allows, but the click itself still reaches the core.
	select {
	case out <- in:
	case <-ctx.Done():
		return "", false
	}
	toast := ""
	if a.i18n != nil {
		toast = a.i18n.T(a.langOf(in.Endpoint), "approval.card_received", nil)
	}
	return toast, true
}

// cardJSON renders a card as Feishu card JSON 2.0 (open.feishu.cn,
// feishu-cards/card-json-v2-structure). The body is a plain_text element, never
// lark_md: it carries commands an agent wrote and must not be rendered as
// markdown or turned into links.
func cardJSON(c Card) (string, error) {
	type text struct {
		Tag     string `json:"tag"`
		Content string `json:"content"`
	}
	type behavior struct {
		Type  string    `json:"type"`
		Value CardValue `json:"value"`
	}
	type button struct {
		Tag       string     `json:"tag"`
		Text      text       `json:"text"`
		Type      string     `json:"type"`
		Behaviors []behavior `json:"behaviors"`
	}
	type column struct {
		Tag      string   `json:"tag"`
		Width    string   `json:"width"`
		Elements []button `json:"elements"`
	}
	columns := make([]column, 0, len(c.Buttons))
	for _, b := range c.Buttons {
		kind := "default"
		switch b.Value.Verdict {
		case VerdictAllowOnce:
			kind = "primary"
		case VerdictReject:
			kind = "danger"
		}
		columns = append(columns, column{Tag: "column", Width: "auto", Elements: []button{{
			Tag:       "button",
			Text:      text{Tag: "plain_text", Content: b.Text},
			Type:      kind,
			Behaviors: []behavior{{Type: "callback", Value: b.Value}},
		}}})
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": map[string]any{
			"title":    text{Tag: "plain_text", Content: c.Title},
			"template": "orange",
		},
		"body": map[string]any{
			"elements": []any{
				map[string]any{"tag": "div", "text": text{Tag: "plain_text", Content: c.Body}},
				map[string]any{"tag": "column_set", "columns": columns},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("feishu: encode a card: %w", err)
	}
	return string(data), nil
}
