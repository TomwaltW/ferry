package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fakeI18n writes "<lang>:<key>" plus the ID argument, so a test can tell
// which key, and which language, produced a string.
type fakeI18n struct{}

func (fakeI18n) T(lang fsp.Lang, key string, args map[string]any) string {
	s := string(lang) + ":" + key
	if id, ok := args["ID"]; ok {
		s += fmt.Sprintf("(%v)", id)
	}
	return s
}

// bobLang is English for ou_bob and Chinese for everyone else, so the tests
// see that the language follows the endpoint.
func bobLang(ep fsp.EndpointID) fsp.Lang {
	if ep.PeerID == "ou_bob" {
		return fsp.LangEN
	}
	return fsp.LangZH
}

func withCards(o *Options) {
	o.I18n = fakeI18n{}
	o.Lang = bobLang
}

var bob = fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}

func approvalKey(id string, always bool) string {
	return ports.ApprovalDedupeKey(ports.ApprovalRef{ID: id, AllowAlways: always}, epoch)
}

func sendOK(t *testing.T, r *rig, msg fsp.Outbound) {
	t.Helper()
	st, err := r.a.Send(context.Background(), msg, "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if st != fsp.DeliveryAccepted {
		t.Fatalf("state = %q, want %q", st, fsp.DeliveryAccepted)
	}
}

// onlyCard asserts that exactly one message went out, as a card to ou_bob,
// and returns it.
func onlyCard(t *testing.T, r *rig) Card {
	t.Helper()
	got := r.client.messages()
	if len(got) != 1 || got[0].kind != "card" || got[0].openID != "ou_bob" {
		t.Fatalf("sent %+v, want one card to ou_bob", got)
	}
	return r.client.sentCards()[0]
}

// decodeCard parses the JSON a card is sent as.
func decodeCard(t *testing.T, c Card) map[string]any {
	t.Helper()
	raw, err := cardJSON(c)
	if err != nil {
		t.Fatalf("cardJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("the card is not JSON: %v\n%s", err, raw)
	}
	return m
}

// path walks a decoded JSON value by object keys and array indexes.
func path(t *testing.T, v any, steps ...any) any {
	t.Helper()
	for _, s := range steps {
		switch k := s.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v: not an object at %q", v, k)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("%v: no element %d", v, k)
			}
			v = a[k]
		}
	}
	return v
}

// ---------- sending ----------

func TestApprovalKeySendsCard(t *testing.T) {
	r := newRig(t, withCards)
	sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "run ls?", DedupeKey: approvalKey("abcde", true)})

	card := onlyCard(t, r)
	if want := "en:approval.card_title(abcde)"; card.Title != want {
		t.Errorf("title = %q, want %q", card.Title, want)
	}
	if card.Body != "run ls?" {
		t.Errorf("body = %q, want the message text", card.Body)
	}

	m := decodeCard(t, card)
	if path(t, m, "schema") != "2.0" {
		t.Errorf("schema = %v, want card JSON 2.0", path(t, m, "schema"))
	}
	if path(t, m, "header", "title", "content") != card.Title {
		t.Errorf("header = %v, want the title", path(t, m, "header"))
	}
}

func TestCardButtonsFollowAllowAlways(t *testing.T) {
	for _, tc := range []struct {
		always   bool
		verdicts []string
		keys     []string
	}{
		{true, []string{"y", "a", "n"}, []string{"approval.button_allow_once", "approval.button_allow_always", "approval.button_reject"}},
		{false, []string{"y", "n"}, []string{"approval.button_allow_once", "approval.button_reject"}},
	} {
		t.Run(fmt.Sprintf("allow_always=%v", tc.always), func(t *testing.T) {
			r := newRig(t, withCards)
			sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "edit?", DedupeKey: approvalKey("pqrst", tc.always)})
			card := onlyCard(t, r)

			if len(card.Buttons) != len(tc.verdicts) {
				t.Fatalf("buttons = %+v, want %v", card.Buttons, tc.verdicts)
			}
			for i, b := range card.Buttons {
				if want := "en:" + tc.keys[i]; b.Text != want {
					t.Errorf("button %d text = %q, want %q", i, b.Text, want)
				}
				if want := (CardValue{Approval: "pqrst", Verdict: tc.verdicts[i]}); b.Value != want {
					t.Errorf("button %d value = %+v, want %+v", i, b.Value, want)
				}
			}

			// The JSON Feishu gets carries each value as a callback behavior,
			// exactly {"ferry_approval": "<id>", "verdict": "<v>"}.
			columns := path(t, decodeCard(t, card), "body", "elements", 1, "columns").([]any)
			if len(columns) != len(tc.verdicts) {
				t.Fatalf("%d button columns, want %d", len(columns), len(tc.verdicts))
			}
			for i, col := range columns {
				btn := path(t, col, "elements", 0)
				if path(t, btn, "tag") != "button" || path(t, btn, "behaviors", 0, "type") != "callback" {
					t.Errorf("column %d = %v, want a button with a callback behavior", i, col)
				}
				want := map[string]any{"ferry_approval": "pqrst", "verdict": tc.verdicts[i]}
				if got := path(t, btn, "behaviors", 0, "value"); !reflect.DeepEqual(got, want) {
					t.Errorf("button %d value = %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestCardBodyIsPlainMessageText(t *testing.T) {
	r := newRig(t, withCards)
	text := "earlier output\n\nrun `rm -rf ./build` **now**? [docs](https://evil.example) <at id=all></at>"
	sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: text, DedupeKey: approvalKey("abcde", false)})
	card := onlyCard(t, r)
	if card.Body != text {
		t.Fatalf("body = %q, want the message text unchanged", card.Body)
	}

	raw, err := cardJSON(card)
	if err != nil {
		t.Fatalf("cardJSON: %v", err)
	}
	for _, tag := range []string{`"lark_md"`, `"markdown"`} {
		if strings.Contains(raw, tag) {
			t.Errorf("the card uses %s: %s", tag, raw)
		}
	}
	body := path(t, decodeCard(t, card), "body", "elements", 0)
	if path(t, body, "tag") != "div" || path(t, body, "text", "tag") != "plain_text" {
		t.Errorf("body element = %v, want a div of plain_text", body)
	}
	if path(t, body, "text", "content") != text {
		t.Errorf("body content = %q, want %q", path(t, body, "text", "content"), text)
	}
}

func TestCardFallsBackToTextOnError(t *testing.T) {
	r := newRig(t, withCards)
	r.client.sendErrs = []error{errors.New("card capability not enabled (code 230099)")}
	sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "run ls?\ny abcde", DedupeKey: approvalKey("abcde", true)})

	want := []sentMessage{{kind: "text", openID: "ou_bob", body: "run ls?\ny abcde"}}
	if got := r.client.messages(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %+v, want the same message as text %+v", got, want)
	}

	t.Run("a card refused for going too fast is retried as a card", func(t *testing.T) {
		r := newRig(t, withCards)
		r.client.sendErrs = []error{ErrRateLimited}
		done := make(chan error, 1)
		go func() {
			_, err := r.a.Send(context.Background(), fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "x", DedupeKey: approvalKey("abcde", true)}, "")
			done <- err
		}()
		r.clk.BlockUntil(1)
		r.clk.Advance(time.Second)
		if err := <-done; err != nil {
			t.Fatalf("Send: %v", err)
		}
		onlyCard(t, r)
	})
}

func TestNoKeySendsText(t *testing.T) {
	r := newRig(t, withCards)
	for _, key := range []string{"", "ob-42", "approval-abcde", "approval-ABCDE-a-1"} {
		sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "hi", DedupeKey: key})
	}
	for _, m := range r.client.messages() {
		if m.kind != "text" {
			t.Errorf("sent %+v, want text for every key that names no approval", m)
		}
	}
	if n := len(r.client.messages()); n != 4 {
		t.Errorf("%d messages sent, want 4", n)
	}
}

func TestNoI18nSendsText(t *testing.T) {
	r := newRig(t) // no I18n, as every P3 caller builds it
	sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "run ls?", DedupeKey: approvalKey("abcde", true)})
	want := []sentMessage{{kind: "text", openID: "ou_bob", body: "run ls?"}}
	if got := r.client.messages(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %+v, want %+v", got, want)
	}
}

func TestSegmentKeyAlsoGetsButtons(t *testing.T) {
	r := newRig(t, withCards)
	sendOK(t, r, fsp.Outbound{Endpoint: bob, Kind: fsp.OutText, Text: "second half", DedupeKey: approvalKey("abcde", true) + "-2"})
	card := onlyCard(t, r)
	if len(card.Buttons) != 3 {
		t.Fatalf("buttons = %+v, want all three", card.Buttons)
	}
	for _, b := range card.Buttons {
		if b.Value.Approval != "abcde" {
			t.Errorf("button %+v, want it bound to approval abcde", b)
		}
	}
	if card.Title != "en:approval.card_title(abcde)" || card.Body != "second half" {
		t.Errorf("card = %+v, want the same approval with this segment's text", card)
	}
}

// ---------- callbacks ----------

func TestCardActionBecomesVerdict(t *testing.T) {
	r := newRig(t, withCards).run()
	r.clk.Advance(time.Minute)
	for _, v := range []string{"y", "a", "n"} {
		// The value goes through JSON, as it does on the wire.
		var value map[string]any
		if err := json.Unmarshal([]byte(`{"ferry_approval":"abcde","verdict":"`+v+`"}`), &value); err != nil {
			t.Fatal(err)
		}
		if _, ok := r.client.click(CardAction{EventID: "ev-" + v, Token: "tok", OpenID: "ou_bob", Value: value}); !ok {
			t.Fatalf("click %s was not handled", v)
		}
		in := r.waitInbound()
		if in.Endpoint != bob || in.SenderID != "ou_bob" {
			t.Errorf("endpoint %+v, sender %q, want ou_bob's direct chat", in.Endpoint, in.SenderID)
		}
		if want := v + " abcde"; in.Text != want {
			t.Errorf("text = %q, want %q", in.Text, want)
		}
		if want := "card-ev-" + v; in.MessageID != want {
			t.Errorf("message = %q, want %q", in.MessageID, want)
		}
		if !in.ReceivedAt.Equal(epoch.Add(time.Minute)) {
			t.Errorf("received at %v, want now", in.ReceivedAt)
		}
	}
	if st := r.a.Status(); !st.Online {
		t.Errorf("status = %+v, want online after a click", st)
	}
}

func TestCardActionForeignValueIgnored(t *testing.T) {
	r := newRig(t, withCards).run()
	for _, value := range []map[string]any{
		nil,
		{},
		{"action": "approve"},
		{"ferry_approval": "abcde"},
		{"verdict": "y"},
		{"ferry_approval": "abcde", "verdict": "yes"},
		{"ferry_approval": "abcde", "verdict": "x"},
		{"ferry_approval": "ABCDE", "verdict": "y"},
		{"ferry_approval": "abcdl", "verdict": "y"}, // no l in an approval ID
		{"ferry_approval": "abcdef", "verdict": "y"},
		{"ferry_approval": 12345, "verdict": "y"},
		{"ferry_approval": "abcde", "verdict": true},
	} {
		toast, ok := r.client.click(CardAction{EventID: "ev", OpenID: "ou_bob", Value: value})
		if ok || toast != "" {
			t.Errorf("value %v answered %q/%v, want it ignored without a toast", value, toast, ok)
		}
	}
	// A click without a clicker is not a verdict either.
	if _, ok := r.client.click(CardAction{EventID: "ev", Value: CardValue{Approval: "abcde", Verdict: "y"}.Map()}); ok {
		t.Error("a click without an open id was handled")
	}
	select {
	case in := <-r.in:
		t.Errorf("an ignored click reached the core: %+v", in)
	default:
	}
}

func TestCardActionMessageIDUnique(t *testing.T) {
	r := newRig(t, withCards).run()
	value := CardValue{Approval: "abcde", Verdict: "y"}.Map()
	seen := map[string]bool{}
	for _, act := range []CardAction{
		{EventID: "ev-1", Token: "tok-1", OpenID: "ou_bob", Value: value},
		{EventID: "ev-2", Token: "tok-1", OpenID: "ou_bob", Value: value}, // the same card clicked again
		{Token: "tok-3", OpenID: "ou_bob", Value: value},
		{OpenID: "ou_bob", Value: value},
		{OpenID: "ou_bob", Value: value}, // same instant, nothing to tell them apart
	} {
		if _, ok := r.client.click(act); !ok {
			t.Fatalf("click %+v was not handled", act)
		}
		id := r.waitInbound().MessageID
		if !strings.HasPrefix(id, "card-") || seen[id] {
			t.Errorf("message id %q: want a fresh card- id", id)
		}
		seen[id] = true
	}
	if !seen["card-ev-1"] || !seen["card-tok-3"] {
		t.Errorf("ids %v, want card-<event_id>, falling back to card-<token>", seen)
	}
}

func TestCardActionAnswersToast(t *testing.T) {
	r := newRig(t, withCards).run()
	toast, ok := r.client.click(CardAction{EventID: "ev", OpenID: "ou_bob", Value: CardValue{Approval: "abcde", Verdict: "n"}.Map()})
	if !ok || toast != "en:approval.card_received" {
		t.Errorf("toast = %q/%v, want the approval.card_received text in ou_bob's language", toast, ok)
	}
	r.waitInbound()

	toast, _ = r.client.click(CardAction{EventID: "ev2", OpenID: "ou_carol", Value: CardValue{Approval: "abcde", Verdict: "n"}.Map()})
	if toast != "zh-CN:approval.card_received" {
		t.Errorf("toast for ou_carol = %q, want it in Chinese", toast)
	}
	r.waitInbound()
}

// TestConvertCardAction pins the thin SDK layer: the fields of a
// card.action.trigger callback the adapter reads.
func TestConvertCardAction(t *testing.T) {
	var ev larkcallback.CardActionTriggerEvent
	payload := `{"schema":"2.0","header":{"event_id":"ev_9","event_type":"card.action.trigger"},
		"event":{"operator":{"open_id":"ou_bob"},"token":"c-tok",
		"action":{"tag":"button","value":{"ferry_approval":"abcde","verdict":"a"}}}}`
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatal(err)
	}
	act, ok := convertCardAction(&ev)
	want := CardAction{EventID: "ev_9", Token: "c-tok", OpenID: "ou_bob", Value: map[string]any{"ferry_approval": "abcde", "verdict": "a"}}
	if !ok || !reflect.DeepEqual(act, want) {
		t.Errorf("convertCardAction = %+v/%v, want %+v", act, ok, want)
	}

	for _, bad := range []*larkcallback.CardActionTriggerEvent{
		nil,
		{},
		{EventV2Base: &larkevent.EventV2Base{}, Event: &larkcallback.CardActionTriggerRequest{}},
	} {
		if _, ok := convertCardAction(bad); ok {
			t.Errorf("convertCardAction(%+v) reported a click", bad)
		}
	}
}
