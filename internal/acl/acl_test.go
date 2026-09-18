package acl

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/store"
)

var (
	ep   = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userA"}
	base = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
)

// newACL builds an ACL over a real store in a temporary database.
func newACL(t *testing.T, cfg config.WeChatConfig) (ports.ACL, ports.Store, *clock.Fake) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ferry.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewFake(base)
	return New(config.Config{WeChat: cfg}, st, clk), st, clk
}

// newFullACL builds an ACL over a whole configuration, for the approver lists
// that differ per platform.
func newFullACL(t *testing.T, cfg config.Config) ports.ACL {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ferry.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(cfg, st, clock.NewFake(base))
}

// Each platform decides approvals by its own list: a name trusted on Feishu is
// not one on WeChat, and a platform no list names — a third-party channel
// server appears as channel:<name> — keeps the WeChat list rather than falling
// to an empty one nobody could approve through.
func TestApproversArePerPlatform(t *testing.T) {
	a := newFullACL(t, config.Config{
		WeChat: config.WeChatConfig{Approvers: []string{"wxUser"}},
		Feishu: config.FeishuConfig{Approvers: []string{"fsUser"}},
	})

	feishuEP := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: "bot1", PeerID: "roomA"}
	if !a.IsApprover(feishuEP, "fsUser") {
		t.Error("IsApprover(feishu, fsUser) = false, want true from the [feishu] list")
	}
	if a.IsApprover(feishuEP, "wxUser") {
		t.Error("IsApprover(feishu, wxUser) = true, want false: the [wechat] list does not carry over")
	}

	wechatEP := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "roomA"}
	if !a.IsApprover(wechatEP, "wxUser") {
		t.Error("IsApprover(wechat, wxUser) = false, want true from the [wechat] list")
	}
	if a.IsApprover(wechatEP, "fsUser") {
		t.Error("IsApprover(wechat, fsUser) = true, want false: the [feishu] list does not carry over")
	}

	channelEP := fsp.EndpointID{Platform: fsp.Platform("channel:demo"), BotID: "demo", PeerID: "roomA"}
	if !a.IsApprover(channelEP, "wxUser") {
		t.Error("IsApprover(channel:demo, wxUser) = false, want the [wechat] list to still govern")
	}

	// config.Load does not apply FeishuConfig.WithDefaults, so an omitted
	// [feishu] approvers has to resolve to the ["owner"] config documents —
	// not to an empty list, and not to the [wechat] names either.
	b := newFullACL(t, config.Config{WeChat: config.WeChatConfig{Approvers: []string{"wxUser"}}})
	if err := b.SetOwner(context.Background(), fsp.PlatformFeishu, "bot1", "ownerA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	ownerEP := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: "bot1", PeerID: "ownerA"}
	if !b.IsApprover(ownerEP, "ownerA") {
		t.Error(`IsApprover(feishu owner) = false with no [feishu] approvers, want true: it defaults to ["owner"]`)
	}
	if b.IsApprover(feishuEP, "wxUser") {
		t.Error("IsApprover(feishu, wxUser) = true with no [feishu] approvers, want false: the [wechat] list does not carry over")
	}
}

func TestOwnerIsApprover(t *testing.T) {
	ctx := context.Background()

	a, st, _ := newACL(t, config.WeChatConfig{Approvers: []string{"owner"}})
	if err := a.SetOwner(ctx, fsp.PlatformWeChat, "bot1", "userA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	role, err := st.Role(ctx, ep, "userA")
	if err != nil {
		t.Fatalf("Role: %v", err)
	}
	if role != RoleOwner {
		t.Errorf("role after SetOwner = %q, want %q", role, RoleOwner)
	}
	if !a.Allowed(ep, "userA") {
		t.Error("Allowed(owner) = false, want true")
	}
	if !a.IsApprover(ep, "userA") {
		t.Error("IsApprover(owner) = false, want true when the configuration lists \"owner\"")
	}
	if a.Allowed(ep, "stranger") || a.IsApprover(ep, "stranger") {
		t.Error("a sender with no role is allowed or an approver, want neither")
	}

	// Without "owner" in the list the owner role alone does not approve.
	b, _, _ := newACL(t, config.WeChatConfig{})
	if err := b.SetOwner(ctx, fsp.PlatformWeChat, "bot1", "userA"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if !b.Allowed(ep, "userA") {
		t.Error("Allowed(owner) = false, want true")
	}
	if b.IsApprover(ep, "userA") {
		t.Error("IsApprover(owner) = true, want false when the configuration omits \"owner\"")
	}
}

func TestApproverList(t *testing.T) {
	ctx := context.Background()
	a, st, _ := newACL(t, config.WeChatConfig{Approvers: []string{"userX"}})

	// Named in the configuration: an approver even with no role recorded.
	if !a.IsApprover(ep, "userX") {
		t.Error("IsApprover(configured) = false, want true")
	}
	// ... but the ACL still governs whether they may talk to the bot at all.
	if a.Allowed(ep, "userX") {
		t.Error("Allowed(configured approver without a role) = true, want false")
	}

	// The approver role approves regardless of the configuration.
	if err := st.SetRole(ctx, ep, "userY", RoleApprover); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if !a.IsApprover(ep, "userY") {
		t.Error("IsApprover(approver role) = false, want true")
	}
	if !a.Allowed(ep, "userY") {
		t.Error("Allowed(approver role) = false, want true")
	}

	// A plain member may talk but not approve.
	if err := st.SetRole(ctx, ep, "userZ", RoleMember); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if !a.Allowed(ep, "userZ") {
		t.Error("Allowed(member) = false, want true")
	}
	if a.IsApprover(ep, "userZ") {
		t.Error("IsApprover(member) = true, want false")
	}
}

func TestPairingLimit(t *testing.T) {
	ctx := context.Background()
	a, _, clk := newACL(t, config.WeChatConfig{})

	seen := make(map[string]bool)
	for i := range maxPending {
		code, err := a.OfferPairing(ctx, ep, "userA")
		if err != nil {
			t.Fatalf("OfferPairing %d: %v", i+1, err)
		}
		if len(code) != 6 {
			t.Errorf("code %q has %d characters, want 6", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Errorf("code %q is not all digits", code)
				break
			}
		}
		if seen[code] {
			t.Errorf("OfferPairing handed out %q twice", code)
		}
		seen[code] = true
	}

	if _, err := a.OfferPairing(ctx, ep, "userA"); !errors.Is(err, fsp.ErrQuotaExceeded) {
		t.Errorf("OfferPairing past the limit error = %v, want fsp.ErrQuotaExceeded", err)
	}

	// A second endpoint has its own budget.
	other := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userB"}
	if _, err := a.OfferPairing(ctx, other, "userB"); err != nil {
		t.Errorf("OfferPairing at a second endpoint: %v", err)
	}

	// Once the codes expire the endpoint may ask again.
	clk.Advance(pairingTTL + time.Minute)
	if _, err := a.OfferPairing(ctx, ep, "userA"); err != nil {
		t.Errorf("OfferPairing after the codes expired: %v", err)
	}
}

func TestConfirmPairing(t *testing.T) {
	ctx := context.Background()
	a, st, clk := newACL(t, config.WeChatConfig{})

	code, err := a.OfferPairing(ctx, ep, "userA")
	if err != nil {
		t.Fatalf("OfferPairing: %v", err)
	}
	if a.Allowed(ep, "userA") {
		t.Fatal("Allowed before confirmation = true, want false")
	}

	gotEP, gotSender, err := a.ConfirmPairing(ctx, code)
	if err != nil {
		t.Fatalf("ConfirmPairing: %v", err)
	}
	if gotEP != ep || gotSender != "userA" {
		t.Errorf("ConfirmPairing = (%s, %s), want (%s, userA)", gotEP, gotSender, ep)
	}
	role, err := st.Role(ctx, ep, "userA")
	if err != nil {
		t.Fatalf("Role: %v", err)
	}
	if role != RoleMember {
		t.Errorf("role after confirmation = %q, want %q", role, RoleMember)
	}
	if !a.Allowed(ep, "userA") {
		t.Error("Allowed after confirmation = false, want true")
	}
	if a.IsApprover(ep, "userA") {
		t.Error("IsApprover(member) = true, want false")
	}

	// A code is single use.
	if _, _, err := a.ConfirmPairing(ctx, code); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("second ConfirmPairing error = %v, want fsp.ErrNotFound", err)
	}
	if _, _, err := a.ConfirmPairing(ctx, "000000"); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("ConfirmPairing(unknown) error = %v, want fsp.ErrNotFound", err)
	}

	// An expired code is refused.
	code, err = a.OfferPairing(ctx, ep, "userB")
	if err != nil {
		t.Fatalf("OfferPairing: %v", err)
	}
	clk.Advance(pairingTTL + time.Minute)
	if _, _, err := a.ConfirmPairing(ctx, code); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("ConfirmPairing(expired) error = %v, want fsp.ErrNotFound", err)
	}
}
