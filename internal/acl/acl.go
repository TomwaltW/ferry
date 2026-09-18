package acl

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Roles recorded in the store.
const (
	RoleOwner    = "owner"
	RoleApprover = "approver"
	RoleMember   = "member"
)

const (
	// pairingTTL is how long a pairing code stays usable.
	pairingTTL = time.Hour
	// maxPending is how many unconfirmed codes one endpoint may hold.
	maxPending = 3
	// codeAttempts bounds the retries when a random code is already taken.
	codeAttempts = 10
)

type acl struct {
	cfg config.Config
	st  ports.Store
	clk clock.Clock
}

var _ ports.ACL = (*acl)(nil)

func New(cfg config.Config, st ports.Store, clk clock.Clock) ports.ACL {
	return &acl{cfg: cfg, st: st, clk: clk}
}

// approvers is the list that governs one endpoint: [feishu] approvers for a
// Feishu endpoint, the [wechat] list for everything else — the fake platform,
// and the channel:<name> a third-party server appears as.
//
// Feishu goes through WithDefaults rather than reading the field, because
// config.Load does not apply it: an omitted [feishu] approvers arrives here
// nil, and taking that literally would leave nobody able to approve anything on
// Feishu — fails closed, and says nothing about why. WithDefaults answers with
// the ["owner"] that config documents, so the default is stated in one place
// instead of two that can drift.
func (a *acl) approvers(p fsp.Platform) []string {
	if p == fsp.PlatformFeishu {
		return a.cfg.Feishu.WithDefaults().Approvers
	}
	return a.cfg.WeChat.Approvers
}

// Allowed reports whether the sender has any role at the endpoint. A store
// error denies access rather than opening it.
func (a *acl) Allowed(ep fsp.EndpointID, senderID string) bool {
	role, err := a.st.Role(context.Background(), ep, senderID)
	if err != nil {
		return false
	}
	return role != ""
}

// IsApprover reports whether the sender may decide approvals: the owner when
// the configuration lists "owner", anyone holding the approver role, and
// anyone named in the approver list of the endpoint's own platform.
func (a *acl) IsApprover(ep fsp.EndpointID, senderID string) bool {
	approvers := a.approvers(ep.Platform)
	for _, who := range approvers {
		if who != RoleOwner && who == senderID {
			return true
		}
	}
	role, err := a.st.Role(context.Background(), ep, senderID)
	if err != nil {
		return false
	}
	switch role {
	case RoleApprover:
		return true
	case RoleOwner:
		for _, who := range approvers {
			if who == RoleOwner {
				return true
			}
		}
	}
	return false
}

// SetOwner records the scanning user as the owner of their own endpoint.
func (a *acl) SetOwner(ctx context.Context, platform fsp.Platform, botID, ownerID string) error {
	ep := fsp.EndpointID{Platform: platform, BotID: botID, PeerID: ownerID}
	if err := a.st.SetRole(ctx, ep, ownerID, RoleOwner); err != nil {
		return fmt.Errorf("acl: set owner %s: %w", ownerID, err)
	}
	return nil
}

// OfferPairing hands out a six digit code the owner can confirm. It returns
// fsp.ErrQuotaExceeded once the endpoint holds maxPending unconfirmed codes.
func (a *acl) OfferPairing(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) {
	now := a.clk.Now()
	pending, err := a.st.CountPairings(ctx, ep, now)
	if err != nil {
		return "", fmt.Errorf("acl: offer pairing: %w", err)
	}
	if pending >= maxPending {
		return "", fmt.Errorf("acl: %d pending codes at %s: %w", pending, ep, fsp.ErrQuotaExceeded)
	}

	var lastErr error
	for range codeAttempts {
		code, err := newCode()
		if err != nil {
			return "", fmt.Errorf("acl: offer pairing: %w", err)
		}
		p := ports.Pairing{Code: code, Endpoint: ep, SenderID: senderID, ExpiresAt: now.Add(pairingTTL)}
		if err := a.st.AddPairing(ctx, p); err != nil {
			// Almost always a collision with a code that is still live.
			lastErr = err
			continue
		}
		return code, nil
	}
	return "", fmt.Errorf("acl: offer pairing: %w", lastErr)
}

// ConfirmPairing consumes the code and makes its sender a member.
func (a *acl) ConfirmPairing(ctx context.Context, code string) (fsp.EndpointID, string, error) {
	p, err := a.st.TakePairing(ctx, strings.TrimSpace(code), a.clk.Now())
	if err != nil {
		return fsp.EndpointID{}, "", fmt.Errorf("acl: confirm pairing: %w", err)
	}
	if err := a.st.SetRole(ctx, p.Endpoint, p.SenderID, RoleMember); err != nil {
		return fsp.EndpointID{}, "", fmt.Errorf("acl: confirm pairing: %w", err)
	}
	return p.Endpoint, p.SenderID, nil
}

// newCode returns six random digits, leading zeros included.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
