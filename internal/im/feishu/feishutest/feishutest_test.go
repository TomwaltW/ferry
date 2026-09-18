package feishutest

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/ports"
)

type keyI18n struct{}

func (keyI18n) T(_ fsp.Lang, key string, _ map[string]any) string { return key }

// TestRecordCardAndClick walks the path the end-to-end tests use: an approval
// message goes out as a recorded card, and clicking one of its buttons comes
// back through Run as the typed verdict.
func TestRecordCardAndClick(t *testing.T) {
	c := New()
	a, err := feishu.NewWithClient(feishu.Options{
		Config: config.FeishuConfig{Enabled: true, AppID: "cli_app", OwnerOpenID: "ou_owner"},
		Log:    slog.New(slog.DiscardHandler),
		I18n:   keyI18n{},
	}, c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ep := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: "cli_app", PeerID: "ou_owner"}
	key := ports.ApprovalDedupeKey(ports.ApprovalRef{ID: "abcde", AllowAlways: true}, time.Now())
	if _, err := a.Send(ctx, fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: "run ls?", DedupeKey: key}, ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cards := c.Cards()
	if len(cards) != 1 || cards[0].Text != "run ls?" || cards[0].Card == nil || len(cards[0].Card.Buttons) != 3 {
		t.Fatalf("cards = %+v, want one card with three buttons", cards)
	}

	in := make(chan fsp.Inbound, 1)
	running := c.Running()
	go func() { _ = a.Run(ctx, in) }()
	<-running

	toast, ok := c.Click(ctx, "ou_owner", cards[0].Card.Buttons[1].Value)
	if !ok || toast != "approval.card_received" {
		t.Errorf("click answered %q/%v, want the approval.card_received toast", toast, ok)
	}
	got := <-in
	if got.Text != "a abcde" || got.SenderID != "ou_owner" || got.MessageID == "" {
		t.Errorf("inbound = %+v, want the verdict \"a abcde\" from ou_owner", got)
	}

	if _, ok := c.ClickAction(ctx, feishu.CardAction{EventID: "x", OpenID: "ou_owner", Value: map[string]any{"k": "v"}}); ok {
		t.Error("a foreign value was answered")
	}

	// A refused card falls back to text, which is recorded as KindText.
	c.FailSend(errors.New("refused"))
	if _, err := a.Send(ctx, fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: "again?", DedupeKey: key + "-1"}, ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := c.Sent()
	if last := sent[len(sent)-1]; last.Kind != KindText || last.Text != "again?" {
		t.Errorf("last send = %+v, want the text fallback", last)
	}
}
