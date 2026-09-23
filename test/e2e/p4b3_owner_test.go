package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// unnamedChannel is one run of the echo channel server: the file a test
// appends messages to and the file its replies land in. A restarted server
// reads its input from the start, so every run gets files of its own.
type unnamedChannel struct{ in, out string }

func newUnnamedChannel(t *testing.T) unnamedChannel {
	t.Helper()
	dir := t.TempDir()
	c := unnamedChannel{in: filepath.Join(dir, "in.txt"), out: filepath.Join(dir, "out.txt")}
	if err := os.WriteFile(c.in, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return c
}

// server is channel server "echo" with the given owner. Its sender key is one
// the echo server never sends, so every message it relays names no sender.
func (c unnamedChannel) server(owner string) config.ChannelServerConfig {
	return config.ChannelServerConfig{
		Name:    "echo",
		Command: []string{echoChannelBin},
		Env: map[string]string{
			"ECHO_CHANNEL_IN":   c.in,
			"ECHO_CHANNEL_OUT":  c.out,
			"ECHO_CHANNEL_CHAT": "room-1",
		},
		UserIDMeta: "nosender",
		Owner:      owner,
	}
}

func (c unnamedChannel) push(t *testing.T, line string) {
	t.Helper()
	f, err := os.OpenFile(c.in, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		t.Fatal(err)
	}
}

func (c unnamedChannel) replies() []string {
	data, _ := os.ReadFile(c.out)
	var texts []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if _, text, ok := strings.Cut(l, "\t"); ok {
			texts = append(texts, strings.ReplaceAll(text, `\n`, "\n"))
		}
	}
	return texts
}

// An earlier build made "local" the owner of every channel server that set no
// owner, and recorded it in ferry.db on every start. After an upgrade, a
// server whose configuration no longer names local must not treat a message
// without a sender as the owner's; one that still sets owner = "local" must.
func TestUpgradeStaleLocalOwner(t *testing.T) {
	ctx := context.Background()
	const platform = fsp.Platform(config.ChannelPlatformPrefix + "echo")
	own := func(sender string) fsp.EndpointID {
		return fsp.EndpointID{Platform: platform, BotID: "echo", PeerID: sender}
	}
	for _, tc := range []struct {
		name    string
		owner   string // the owner after the restart
		isOwner bool   // whether a message without a sender is the owner's then
	}{
		{"unset", "", false},
		{"changed", "u-42", false},
		{"local", config.DefaultChannelOwner, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := newUnnamedChannel(t)
			r := newRig(t, func(r *rig) {
				r.cfg.ChannelServers = []config.ChannelServerConfig{before.server(config.DefaultChannelOwner)}
			})
			r.waitFor("local recorded as the owner", func() bool {
				role, err := r.store.Role(ctx, own(config.DefaultChannelOwner), config.DefaultChannelOwner)
				return err == nil && role == "owner"
			})
			r.stop()

			after := newUnnamedChannel(t)
			r.cfg.ChannelServers = []config.ChannelServerConfig{after.server(tc.owner)}
			r.start()
			after.push(t, "/new "+agentName+" "+filepath.Join(r.ws, "demo"))

			want, unwanted := "配对码", "已新建会话"
			if tc.isOwner {
				want, unwanted = unwanted, want
			}
			r.waitFor("a reply through the channel server", func() bool {
				return slices.ContainsFunc(after.replies(), func(s string) bool {
					return strings.Contains(s, want) || strings.Contains(s, unwanted)
				})
			})
			replies := after.replies()
			if !slices.ContainsFunc(replies, func(s string) bool { return strings.Contains(s, want) }) {
				t.Errorf("owner %q: no reply contains %q", tc.owner, want)
			}
			for _, s := range replies {
				if strings.Contains(s, unwanted) {
					t.Errorf("owner %q: did not expect %q in a reply:\n%s", tc.owner, unwanted, s)
				}
			}

			role, err := r.store.Role(ctx, own(config.DefaultChannelOwner), config.DefaultChannelOwner)
			if err != nil {
				t.Fatalf("Role: %v", err)
			}
			if got := role == "owner"; got != tc.isOwner {
				t.Errorf("owner %q: local holds role %q after the restart", tc.owner, role)
			}
			if tc.owner != "" {
				if role, err := r.store.Role(ctx, own(tc.owner), tc.owner); err != nil || role != "owner" {
					t.Errorf("the configured owner %q has role %q (err %v), want owner", tc.owner, role, err)
				}
			}
		})
	}
}
