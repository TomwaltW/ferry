package core

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// lockedBuffer collects log output from the daemon's goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const echoPlatform = fsp.Platform("channel-echo")

// ownerAt is the endpoint SetOwner records an owner at: the owner's own.
func ownerAt(platform fsp.Platform, bot, sender string) fsp.EndpointID {
	return fsp.EndpointID{Platform: platform, BotID: bot, PeerID: sender}
}

// channelTestbed is a daemon whose only entry point is channel server "echo"
// with the given owner, over a store seeded with a stale "local" owner, an
// earlier owner u-41, a member and the owner of another server.
func channelTestbed(t *testing.T, owner string) (*harness, *lockedBuffer) {
	t.Helper()
	h := newTestbed(t)
	h.im.platform = echoPlatform
	h.im.status = ports.AdapterStatus{Online: true, BotID: "echo", OwnerID: owner}
	ctx := context.Background()
	mustNil(t, h.store.SetRole(ctx, ownerAt(echoPlatform, "echo", "local"), "local", "owner"))
	mustNil(t, h.store.SetRole(ctx, ownerAt(echoPlatform, "echo", "u-41"), "u-41", "owner"))
	mustNil(t, h.store.SetRole(ctx, ownerAt(echoPlatform, "echo", "u-7"), "u-7", "member"))
	mustNil(t, h.store.SetRole(ctx, ownerAt("channel-other", "other", "local"), "local", "owner"))
	logs := &lockedBuffer{}
	h.deps.Log = slog.New(slog.NewTextHandler(logs, nil))
	return h, logs
}

func (h *harness) roleAt(ep fsp.EndpointID, sender string) string {
	h.t.Helper()
	role, err := h.store.Role(context.Background(), ep, sender)
	mustNil(h.t, err)
	return role
}

func TestAdoptOwner(t *testing.T) {
	t.Run("channel_owner_changed", func(t *testing.T) {
		h, logs := channelTestbed(t, "u-42")
		h.start()
		if got, want := h.acl.ownerTuple(), [3]string{string(echoPlatform), "echo", "u-42"}; got != want {
			t.Errorf("SetOwner called with %v, want %v", got, want)
		}
		for _, who := range []string{"local", "u-41"} {
			if got := h.roleAt(ownerAt(echoPlatform, "echo", who), who); got != "" {
				t.Errorf("stale owner %s still has role %q", who, got)
			}
		}
		if got := h.roleAt(ownerAt(echoPlatform, "echo", "u-7"), "u-7"); got != "member" {
			t.Errorf("member u-7 has role %q, want member", got)
		}
		if got := h.roleAt(ownerAt("channel-other", "other", "local"), "local"); got != "owner" {
			t.Errorf("the other server's owner has role %q, want owner", got)
		}
		if !strings.Contains(logs.String(), "removed stale channel owners") || !strings.Contains(logs.String(), "server=echo removed=2") {
			t.Errorf("no log line for the two removed owners:\n%s", logs)
		}
	})

	t.Run("channel_owner_unset", func(t *testing.T) {
		h, logs := channelTestbed(t, "")
		h.start()
		if got := h.acl.ownerTuples(); len(got) != 0 {
			t.Errorf("SetOwner called with %v, want no call", got)
		}
		for _, who := range []string{"local", "u-41"} {
			if got := h.roleAt(ownerAt(echoPlatform, "echo", who), who); got != "" {
				t.Errorf("stale owner %s still has role %q", who, got)
			}
		}
		if got := h.roleAt(ownerAt("channel-other", "other", "local"), "local"); got != "owner" {
			t.Errorf("the other server's owner has role %q, want owner", got)
		}
		if !strings.Contains(logs.String(), "server=echo removed=2") {
			t.Errorf("no log line for the two removed owners:\n%s", logs)
		}
	})

	t.Run("channel_owner_local", func(t *testing.T) {
		h, logs := channelTestbed(t, "local")
		h.start()
		if got := h.roleAt(ownerAt(echoPlatform, "echo", "local"), "local"); got != "owner" {
			t.Errorf("local has role %q, want owner", got)
		}
		if got := h.roleAt(ownerAt(echoPlatform, "echo", "u-41"), "u-41"); got != "" {
			t.Errorf("stale owner u-41 still has role %q", got)
		}
		if !strings.Contains(logs.String(), "server=echo removed=1") {
			t.Errorf("no log line for the one removed owner:\n%s", logs)
		}
	})

	t.Run("other_platforms_untouched", func(t *testing.T) {
		h := newTestbed(t)
		h.im.platform = fsp.PlatformWeChat
		h.im.status = ports.AdapterStatus{Online: true, BotID: "wx-bot", OwnerID: ""}
		h.im2 = &fakeIM{
			platform: fsp.PlatformFeishu,
			status:   ports.AdapterStatus{Online: true, BotID: "cli_app", OwnerID: "ou-new"},
		}
		h.deps.IM = nil
		h.deps.IMs = []ports.IMAdapter{h.im, h.im2}
		ctx := context.Background()
		mustNil(t, h.store.SetRole(ctx, ownerAt(fsp.PlatformWeChat, "wx-bot", "wx-old"), "wx-old", "owner"))
		mustNil(t, h.store.SetRole(ctx, ownerAt(fsp.PlatformFeishu, "cli_app", "ou-old"), "ou-old", "owner"))
		logs := &lockedBuffer{}
		h.deps.Log = slog.New(slog.NewTextHandler(logs, nil))
		h.start()
		h.waitFor("the second adapter to be running", func() bool { return h.im2.channel() != nil })
		if got := h.roleAt(ownerAt(fsp.PlatformWeChat, "wx-bot", "wx-old"), "wx-old"); got != "owner" {
			t.Errorf("the WeChat owner has role %q, want owner", got)
		}
		if got := h.roleAt(ownerAt(fsp.PlatformFeishu, "cli_app", "ou-old"), "ou-old"); got != "owner" {
			t.Errorf("the earlier Feishu owner has role %q, want owner", got)
		}
		if strings.Contains(logs.String(), "removed stale channel owners") {
			t.Errorf("owners removed outside channel servers:\n%s", logs)
		}
	})
}
