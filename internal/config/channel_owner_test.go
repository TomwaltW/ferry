package config

import (
	"slices"
	"testing"
)

// A channel server's owner is never filled in: with none configured the server
// has no owner, and an explicit one, local included, is kept as written. The
// other defaults still apply.
func TestChannelServerOwnerNotDefaulted(t *testing.T) {
	cases := []struct {
		name  string
		owner string
	}{
		{"unset", ""},
		{"explicit local", "local"},
		{"explicit ID", "u-42"},
	}
	for _, c := range cases {
		got := ChannelServerConfig{Name: "demo", Owner: c.owner}.WithDefaults()
		if got.Owner != c.owner {
			t.Errorf("%s: Owner = %q, want %q", c.name, got.Owner, c.owner)
		}
		if got.UserIDMeta != DefaultChannelUserIDMeta {
			t.Errorf("%s: UserIDMeta = %q, want %q", c.name, got.UserIDMeta, DefaultChannelUserIDMeta)
		}
		if got.ChatIDMeta != DefaultChannelChatIDMeta {
			t.Errorf("%s: ChatIDMeta = %q, want %q", c.name, got.ChatIDMeta, DefaultChannelChatIDMeta)
		}
		if got.ReplyTool != DefaultChannelReplyTool {
			t.Errorf("%s: ReplyTool = %q, want %q", c.name, got.ReplyTool, DefaultChannelReplyTool)
		}
		if want := []string{ApproverOwner}; !slices.Equal(got.Approvers, want) {
			t.Errorf("%s: Approvers = %q, want %q", c.name, got.Approvers, want)
		}
	}
}
