package store

import (
	"context"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// row is one (endpoint, sender, role) triple of the acl table.
type row struct {
	ep     fsp.EndpointID
	sender string
	role   string
}

// ownRow is an owner row as SetOwner writes it: at the owner's own endpoint.
func ownRow(platform fsp.Platform, bot, sender string) row {
	return row{fsp.EndpointID{Platform: platform, BotID: bot, PeerID: sender}, sender, "owner"}
}

func seedRoles(t *testing.T, st ports.Store, rows ...row) {
	t.Helper()
	for _, r := range rows {
		if err := st.SetRole(context.Background(), r.ep, r.sender, r.role); err != nil {
			t.Fatalf("SetRole %v: %v", r, err)
		}
	}
}

// wantRoles checks each row's role now: the role it was seeded with when kept,
// "" when removed.
func wantRoles(t *testing.T, st ports.Store, kept []row, gone []row) {
	t.Helper()
	for _, r := range kept {
		if got, err := st.Role(context.Background(), r.ep, r.sender); err != nil || got != r.role {
			t.Errorf("role of %s at %s = %q (err %v), want %q kept", r.sender, r.ep, got, err, r.role)
		}
	}
	for _, r := range gone {
		if got, err := st.Role(context.Background(), r.ep, r.sender); err != nil || got != "" {
			t.Errorf("role of %s at %s = %q (err %v), want it removed", r.sender, r.ep, got, err)
		}
	}
}

func TestDeleteOwnersExcept(t *testing.T) {
	const ch = fsp.Platform("channel-echo")
	ctx := context.Background()

	t.Run("keeps_the_named_owner", func(t *testing.T) {
		st, _ := openTest(t)
		local, old, cur := ownRow(ch, "echo", "local"), ownRow(ch, "echo", "u-41"), ownRow(ch, "echo", "u-42")
		seedRoles(t, st, local, old, cur)
		n, err := st.DeleteOwnersExcept(ctx, ch, "echo", "u-42")
		if err != nil || n != 2 {
			t.Fatalf("DeleteOwnersExcept = %d, %v; want 2, nil", n, err)
		}
		wantRoles(t, st, []row{cur}, []row{local, old})
	})

	t.Run("empty_keep_removes_every_owner", func(t *testing.T) {
		st, _ := openTest(t)
		local, old := ownRow(ch, "echo", "local"), ownRow(ch, "echo", "u-41")
		seedRoles(t, st, local, old)
		n, err := st.DeleteOwnersExcept(ctx, ch, "echo", "")
		if err != nil || n != 2 {
			t.Fatalf("DeleteOwnersExcept = %d, %v; want 2, nil", n, err)
		}
		wantRoles(t, st, nil, []row{local, old})
		if n, err := st.DeleteOwnersExcept(ctx, ch, "echo", ""); err != nil || n != 0 {
			t.Errorf("second DeleteOwnersExcept = %d, %v; want 0, nil", n, err)
		}
	})

	t.Run("keeps_local_when_local_is_the_owner", func(t *testing.T) {
		st, _ := openTest(t)
		local, old := ownRow(ch, "echo", "local"), ownRow(ch, "echo", "u-41")
		seedRoles(t, st, local, old)
		n, err := st.DeleteOwnersExcept(ctx, ch, "echo", "local")
		if err != nil || n != 1 {
			t.Fatalf("DeleteOwnersExcept = %d, %v; want 1, nil", n, err)
		}
		wantRoles(t, st, []row{local}, []row{old})
	})

	t.Run("leaves_other_roles_bots_and_platforms", func(t *testing.T) {
		st, _ := openTest(t)
		room := fsp.EndpointID{Platform: ch, BotID: "echo", PeerID: "room-1"}
		stale := ownRow(ch, "echo", "local")
		keep := []row{
			{room, "u-7", "member"},
			{room, "u-8", "approver"},
			{fsp.EndpointID{Platform: ch, BotID: "echo", PeerID: "u-9"}, "u-9", "member"},
			ownRow("channel-other", "other", "local"),
			ownRow("channel-echo2", "echo2", "local"),
			ownRow(fsp.PlatformWeChat, "echo", "local"),
			ownRow(fsp.PlatformFeishu, "echo", "local"),
		}
		seedRoles(t, st, append([]row{stale}, keep...)...)
		n, err := st.DeleteOwnersExcept(ctx, ch, "echo", "")
		if err != nil || n != 1 {
			t.Fatalf("DeleteOwnersExcept = %d, %v; want 1, nil", n, err)
		}
		wantRoles(t, st, keep, []row{stale})
	})

	t.Run("wildcards_in_bot_ids_match_only_themselves", func(t *testing.T) {
		st, _ := openTest(t)
		under, lookalike := ownRow(fsp.PlatformFeishu, "cli_a", "ou-1"), ownRow(fsp.PlatformFeishu, "clixa", "ou-1")
		pct, pctLookalike := ownRow("channel-p", "p%", "ou-2"), ownRow("channel-p", "pzz", "ou-2")
		seedRoles(t, st, under, lookalike, pct, pctLookalike)
		if n, err := st.DeleteOwnersExcept(ctx, fsp.PlatformFeishu, "cli_a", ""); err != nil || n != 1 {
			t.Fatalf("DeleteOwnersExcept cli_a = %d, %v; want 1, nil", n, err)
		}
		if n, err := st.DeleteOwnersExcept(ctx, "channel-p", "p%", ""); err != nil || n != 1 {
			t.Fatalf("DeleteOwnersExcept p%% = %d, %v; want 1, nil", n, err)
		}
		wantRoles(t, st, []row{lookalike, pctLookalike}, []row{under, pct})
	})
}
