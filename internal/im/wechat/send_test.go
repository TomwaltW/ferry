package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

func TestSendUsesContextTokenAndClientID(t *testing.T) {
	s := loggedIn(t, fakeilink.Options{})
	a := newAdapter(t, s, newMemKV(), clock.NewFake(epoch))
	token := s.PushText(peer, "question")

	ctx := context.Background()
	msg := fsp.Outbound{
		Endpoint:  a.endpoint(peer),
		Kind:      fsp.OutText,
		Text:      "**done**, see `main.go`",
		DedupeKey: "dk-1",
	}
	state, err := a.Send(ctx, msg, token)
	if err != nil || state != fsp.DeliveryAccepted {
		t.Fatalf("Send = (%s, %v), want (accepted, nil)", state, err)
	}

	sent := s.Sent()
	if len(sent) != 1 {
		t.Fatalf("gateway recorded %d deliveries, want 1", len(sent))
	}
	got := sent[0]
	if got.ClientID != "dk-1" {
		t.Fatalf("client_id = %q, want the dedupe key", got.ClientID)
	}
	if got.ContextToken != token {
		t.Fatalf("context_token = %q, want %q", got.ContextToken, token)
	}
	if got.ToUserID != peer {
		t.Fatalf("to_user_id = %q, want %q", got.ToUserID, peer)
	}
	if got.Text != "done, see main.go" {
		t.Fatalf("text = %q, want it rendered plain", got.Text)
	}
	if len(got.Items) != 1 || got.Items[0].Type != ilinkwire.ItemText {
		t.Fatalf("items = %+v, want a single text item", got.Items)
	}

	// Retransmitting the same key is the gateway's business, not ours: it is
	// recognised as a duplicate rather than delivered twice.
	if state, err := a.Send(ctx, msg, token); err != nil || state != fsp.DeliveryAccepted {
		t.Fatalf("resend = (%s, %v), want (accepted, nil)", state, err)
	}
	if again := s.Sent(); len(again) != 2 || !again[1].Duplicate {
		t.Fatalf("resend was not recognised as a duplicate: %+v", again)
	}

	t.Run("refusals", func(t *testing.T) {
		cases := []struct {
			name    string
			msg     fsp.Outbound
			replyTo string
			want    error
		}{
			{"no dedupe key", fsp.Outbound{Endpoint: a.endpoint(peer), Kind: fsp.OutText, Text: "x"}, token, errNoDedupeKey},
			{"no window", fsp.Outbound{Endpoint: a.endpoint(peer), Kind: fsp.OutText, Text: "x", DedupeKey: "dk-2"}, "", fsp.ErrNoWindow},
			{"missing file", fsp.Outbound{Endpoint: a.endpoint(peer), Kind: fsp.OutFile, FilePath: filepath.Join(t.TempDir(), "gone.txt"), DedupeKey: "dk-3"}, token, fs.ErrNotExist},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				state, err := a.Send(ctx, tc.msg, tc.replyTo)
				if state != fsp.DeliveryRejected || !errors.Is(err, tc.want) {
					t.Fatalf("Send = (%s, %v), want (rejected, %v)", state, err, tc.want)
				}
			})
		}
		if len(s.Sent()) != 2 {
			t.Fatalf("a refused message reached the gateway: %+v", s.Sent())
		}
	})
}

func TestSendTimeoutIsUnknown(t *testing.T) {
	fake := clock.NewFake(epoch)
	s := loggedIn(t, fakeilink.Options{Clock: fake})
	a := newAdapter(t, s, newMemKV(), fake)
	token := s.PushText(peer, "question")

	// The gateway takes the request and never answers: the reply may or may not
	// have landed, which is exactly what unknown means.
	s.FailNext(ilinkwire.PathSendMessage, fakeilink.FailTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	state, err := a.Send(ctx, fsp.Outbound{
		Endpoint:  a.endpoint(peer),
		Kind:      fsp.OutText,
		Text:      "slow answer",
		DedupeKey: "dk-slow",
	}, token)
	if state != fsp.DeliveryUnknown {
		t.Fatalf("Send = %s, want unknown", state)
	}
	if err == nil {
		t.Fatal("Send returned no error for a request that timed out")
	}
	if sent := s.Sent(); len(sent) != 0 {
		t.Fatalf("gateway recorded %+v, want nothing accepted", sent)
	}
	if !a.Status().Online {
		t.Fatal("a timeout took the adapter offline, want it kept online")
	}
}

func TestTypingTicketCached(t *testing.T) {
	var (
		mu      sync.Mutex
		configs int
		typings []ilinkwire.SendTypingReq
	)

	srv := stub(t, func(_ *stubServer, w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(ilinkwire.HeaderAuthType); got != ilinkwire.AuthTypeValue {
			t.Errorf("%s = %q, want %q", ilinkwire.HeaderAuthType, got, ilinkwire.AuthTypeValue)
		}
		if got := r.Header.Get(ilinkwire.HeaderAuthorization); got != "Bearer btok" {
			t.Errorf("%s = %q, want the bot token", ilinkwire.HeaderAuthorization, got)
		}
		if got := r.Header.Get(ilinkwire.HeaderUIN); got == "" {
			t.Errorf("%s is missing", ilinkwire.HeaderUIN)
		}

		switch r.URL.Path {
		case "/" + ilinkwire.PathGetConfig:
			var req ilinkwire.GetConfigReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode getconfig: %v", err)
			}
			if req.BaseInfo == nil || req.BaseInfo.ChannelVersion != ilinkwire.DefaultChannelVersion {
				t.Errorf("base_info = %+v, want channel_version %q", req.BaseInfo, ilinkwire.DefaultChannelVersion)
			}
			mu.Lock()
			configs++
			mu.Unlock()
			writeJSON(t, w, ilinkwire.GetConfigResp{TypingTicket: "ticket-1"})
		case "/" + ilinkwire.PathSendTyping:
			var req ilinkwire.SendTypingReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode sendtyping: %v", err)
			}
			mu.Lock()
			typings = append(typings, req)
			mu.Unlock()
			writeJSON(t, w, struct{}{})
		default:
			http.NotFound(w, r)
		}
	})

	fake := clock.NewFake(epoch)
	kv := newMemKV()
	a := newAdapterAt(t, srv.URL, ports.Credentials{BotToken: "btok", BotID: "bot-1"}, kv, fake)
	ctx := context.Background()
	ep := a.endpoint(peer)

	if err := a.Typing(ctx, ep, "ctx-1", true); err != nil {
		t.Fatalf("Typing on: %v", err)
	}
	if err := a.Typing(ctx, ep, "ctx-1", false); err != nil {
		t.Fatalf("Typing off: %v", err)
	}

	mu.Lock()
	gotConfigs, gotTypings := configs, append([]ilinkwire.SendTypingReq(nil), typings...)
	mu.Unlock()
	if gotConfigs != 1 {
		t.Fatalf("getconfig called %d times, want the ticket cached after the first", gotConfigs)
	}
	if len(gotTypings) != 2 {
		t.Fatalf("sendtyping called %d times, want 2", len(gotTypings))
	}
	if gotTypings[0].Status != typingOn || gotTypings[1].Status != typingOff {
		t.Fatalf("typing statuses = %d, %d, want %d then %d",
			gotTypings[0].Status, gotTypings[1].Status, typingOn, typingOff)
	}
	if gotTypings[0].TypingTicket != "ticket-1" || gotTypings[0].ILinkUserID != peer {
		t.Fatalf("sendtyping = %+v, want the ticket for %s", gotTypings[0], peer)
	}
	if cached := kv.peek(a.typingKey(peer)); !strings.Contains(cached, "ticket-1") {
		t.Fatalf("kv holds %q, want the cached ticket", cached)
	}

	// Past the day the ticket is good for, a fresh one is fetched.
	fake.Advance(typingTicketTTL + time.Hour)
	if err := a.Typing(ctx, ep, "ctx-1", true); err != nil {
		t.Fatalf("Typing after the ticket expired: %v", err)
	}
	mu.Lock()
	gotConfigs = configs
	mu.Unlock()
	if gotConfigs != 2 {
		t.Fatalf("getconfig called %d times, want a second call once the ticket aged out", gotConfigs)
	}
}
