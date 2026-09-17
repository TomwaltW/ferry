package wechat

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

func TestPollDeliversText(t *testing.T) {
	s := loggedIn(t, fakeilink.Options{})
	a := newAdapter(t, s, newMemKV(), clock.NewFake(epoch))

	token := s.PushText(peer, "hello ferry")
	run := start(t, a)

	in := run.recv(t)
	want := fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: s.BotID(), PeerID: peer}
	if in.Endpoint != want {
		t.Fatalf("endpoint = %+v, want %+v", in.Endpoint, want)
	}
	if in.SenderID != peer || in.Text != "hello ferry" {
		t.Fatalf("inbound = %+v, want a text message from %s", in, peer)
	}
	if in.ReplyCtx != token {
		t.Fatalf("ReplyCtx = %q, want the context token %q", in.ReplyCtx, token)
	}
	if in.MessageID != "1" {
		t.Fatalf("MessageID = %q, want the gateway message_id", in.MessageID)
	}
	if in.ReceivedAt.IsZero() {
		t.Fatal("ReceivedAt is zero, want create_time_ms")
	}
	if a.Status().Online != true {
		t.Fatal("adapter is offline after a successful poll")
	}

	// A voice note joins the body through its transcription and stays an
	// attachment; pictures and files are attachments only.
	s.PushMessage(ilinkwire.Message{
		FromUserID: peer,
		ItemList: []ilinkwire.MessageItem{
			{Type: ilinkwire.ItemText, TextItem: &ilinkwire.TextItem{Text: "look"}},
			{Type: ilinkwire.ItemVoice, VoiceItem: &ilinkwire.VoiceItem{Text: "spoken words", PlayTime: 3}},
			{Type: ilinkwire.ItemImage, ImageItem: &ilinkwire.ImageItem{Media: &ilinkwire.CDNMedia{FullURL: "https://cdn.example/i"}}},
			{Type: ilinkwire.ItemFile, FileItem: &ilinkwire.FileItem{FileName: "notes.pdf", Len: "4096"}},
		},
	})

	in = run.recv(t)
	if in.Text != "look\nspoken words" {
		t.Fatalf("Text = %q, want the transcription joined to the text", in.Text)
	}
	if len(in.Attachments) != 3 {
		t.Fatalf("attachments = %+v, want voice, image and file", in.Attachments)
	}
	kinds := []string{in.Attachments[0].Kind, in.Attachments[1].Kind, in.Attachments[2].Kind}
	if kinds[0] != "voice" || kinds[1] != "image" || kinds[2] != "file" {
		t.Fatalf("attachment kinds = %v", kinds)
	}
	if got := in.Attachments[2]; got.Name != "notes.pdf" || got.Size != 4096 {
		t.Fatalf("file attachment = %+v, want notes.pdf of 4096 bytes", got)
	}
	if got := in.Attachments[2].Path; got != "" {
		t.Fatalf("Path = %q, want nothing downloaded in P1", got)
	}
}

func TestPollSkipsBotAndGroup(t *testing.T) {
	// fakeilink stamps every pushed message as a user message, so the batch is
	// scripted here instead.
	bot := textMessage(1, "bot-self", "my own reply")
	bot.MessageType = ilinkwire.MsgTypeBot
	group := textMessage(2, peer, "group chatter")
	group.GroupID = "group-1"
	direct := textMessage(3, peer, "direct question")

	var polls atomic.Int32
	srv := stub(t, func(g *stubServer, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+ilinkwire.PathGetUpdates {
			http.NotFound(w, r)
			return
		}
		if polls.Add(1) > 1 {
			g.park(w, r)
			return
		}
		writeJSON(t, w, ilinkwire.GetUpdatesResp{
			Msgs:                 []ilinkwire.Message{bot, group, direct},
			GetUpdatesBuf:        "cursor-1",
			LongPollingTimeoutMs: 2000,
		})
	})

	kv := newMemKV()
	a := newAdapterAt(t, srv.URL, ports.Credentials{BotToken: "btok", BotID: "bot-self"}, kv, clock.NewFake(epoch))
	run := start(t, a)

	in := run.recv(t)
	if in.Text != "direct question" {
		t.Fatalf("delivered %q, want only the direct message", in.Text)
	}
	select {
	case extra := <-run.out:
		t.Fatalf("delivered a message that should have been skipped: %+v", extra)
	default:
	}
}

func TestCursorPersisted(t *testing.T) {
	s := loggedIn(t, fakeilink.Options{})
	kv := newMemKV()

	a := newAdapter(t, s, kv, clock.NewFake(epoch))
	key := a.cursorKey()

	s.PushText(peer, "first")
	first := start(t, a)
	if in := first.recv(t); in.Text != "first" {
		t.Fatalf("first run delivered %q", in.Text)
	}

	// The cursor is stored only after the batch has been handed over.
	deadline := time.Now().Add(recvTimeout)
	for kv.peek(key) == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cursor := kv.peek(key)
	if cursor == "" {
		t.Fatalf("no cursor under %q after the first batch", key)
	}
	if err := first.stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run returned %v, want context.Canceled", err)
	}

	// A second adapter with the same KV resumes where the first one stopped.
	b := newAdapter(t, s, kv, clock.NewFake(epoch))
	s.PushText(peer, "second")
	resumed := start(t, b)
	if in := resumed.recv(t); in.Text != "second" {
		t.Fatalf("resumed run delivered %q, want only the new message", in.Text)
	}
	if kv.peek(key) == cursor {
		t.Fatalf("cursor %q was never advanced", cursor)
	}
}

func TestSessionExpired(t *testing.T) {
	s := loggedIn(t, fakeilink.Options{})
	a := newAdapter(t, s, newMemKV(), clock.NewFake(epoch))
	token := s.PushText(peer, "hi")
	s.ExpireSession()

	run := start(t, a)
	if err := run.wait(t); !errors.Is(err, fsp.ErrSessionExpired) {
		t.Fatalf("Run returned %v, want fsp.ErrSessionExpired", err)
	}
	st := a.Status()
	if st.Online {
		t.Fatal("adapter still reports online after the session expired")
	}
	if st.Detail == "" {
		t.Fatal("Status().Detail is empty, want the reason for being offline")
	}
	if st.BotID != s.BotID() || st.OwnerID != owner {
		t.Fatalf("status = %+v, want the logged in identity", st)
	}

	state, err := a.Send(context.Background(), fsp.Outbound{
		Endpoint:  a.endpoint(peer),
		Kind:      fsp.OutText,
		Text:      "answer",
		DedupeKey: "dk-expired",
	}, token)
	if state != fsp.DeliveryRejected || !errors.Is(err, fsp.ErrSessionExpired) {
		t.Fatalf("Send = (%s, %v), want (rejected, fsp.ErrSessionExpired)", state, err)
	}
}

func TestBackoff(t *testing.T) {
	var polls atomic.Int32
	srv := stub(t, func(g *stubServer, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+ilinkwire.PathGetUpdates {
			http.NotFound(w, r)
			return
		}
		switch polls.Add(1) {
		case 1, 2, 3, 5:
			http.Error(w, "boom", http.StatusInternalServerError)
		case 4:
			writeJSON(t, w, ilinkwire.GetUpdatesResp{
				Msgs:                 []ilinkwire.Message{textMessage(7, peer, "after the storm")},
				GetUpdatesBuf:        "cursor-1",
				LongPollingTimeoutMs: 2000,
			})
		default:
			g.park(w, r)
		}
	})

	rc := newRecordingClock(epoch)
	a := newAdapterAt(t, srv.URL, ports.Credentials{BotToken: "btok", BotID: "bot-1"}, newMemKV(), rc)
	run := start(t, a)

	for _, want := range []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second} {
		got := waitFor(t, rc)
		if got != want {
			t.Fatalf("backoff = %v, want %v", got, want)
		}
		rc.Advance(got)
	}

	if in := run.recv(t); in.Text != "after the storm" {
		t.Fatalf("delivered %q after the failures", in.Text)
	}
	if got := waitFor(t, rc); got != 1*time.Second {
		t.Fatalf("backoff after a success = %v, want it reset to 1s", got)
	}
}

func waitFor(t *testing.T, rc *recordingClock) time.Duration {
	t.Helper()
	select {
	case d := <-rc.waits:
		return d
	case <-time.After(recvTimeout):
		t.Fatal("the adapter never waited")
		return 0
	}
}
