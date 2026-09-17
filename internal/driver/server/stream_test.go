package server

import (
	"context"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
)

func TestSSEReconnect(t *testing.T) {
	f := newFakeOpenCode(t)
	fake := clock.NewFake(testStart)
	sess := startOpenCode(t, f, nil, fake)
	events := sess.Events()

	if err := sess.Prompt(context.Background(), "a long job"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	// The service restarts mid-turn. Reconnecting waits one second, and the
	// session is checked afterwards because the stream missed whatever
	// happened while it was down.
	f.dropStreams()
	fake.BlockUntil(1)
	fake.Advance(time.Second)
	f.waitConnects(t, 2)

	var hits int
	f.snapshot(func(f *fakeOpenCode) { hits = f.statusHits })
	if hits < 1 {
		t.Error("the session was not checked after reconnecting")
	}

	// The service still reports the turn as running, so the session stays busy.
	if err := sess.Prompt(context.Background(), "again"); err != fsp.ErrBusy {
		t.Errorf("a prompt during the recovered turn gave %v, want fsp.ErrBusy", err)
	}

	f.emit("message.part.updated", map[string]any{"part": map[string]any{
		"id": "prt_1", "sessionID": "ses_1", "type": "text", "text": "back",
	}})
	if e := waitEvent(t, events, "a chunk on the new stream", ofKind(fsp.EvChunk)); e.Text != "back" {
		t.Errorf("the chunk after reconnecting was %q, want back", e.Text)
	}

	// Now the turn finishes while the stream is down. The session would stay
	// busy for good if reconnecting did not catch up on it.
	f.snapshot(func(f *fakeOpenCode) { f.sessions["ses_1"].busy = false })
	f.dropStreams()
	fake.BlockUntil(1)
	fake.Advance(time.Second)
	f.waitConnects(t, 3)

	if e := waitEvent(t, events, "the missed turn end", ofKind(fsp.EvTurnEnd)); e.StopReason != "end_turn" {
		t.Errorf("the missed turn ended with %q, want end_turn", e.StopReason)
	}
	if err := sess.Prompt(context.Background(), "next"); err != nil {
		t.Errorf("the session stayed busy after catching up: %v", err)
	}
}

func TestSSEGiveUpAfter5Min(t *testing.T) {
	f := newFakeOpenCode(t)
	fake := clock.NewFake(testStart)
	sess := startOpenCode(t, f, nil, fake)

	failed := make(chan fsp.Event, 1)
	go func() {
		for e := range sess.Events() {
			if e.Kind == fsp.EvError {
				failed <- e
				return
			}
		}
		close(failed)
	}()

	// The service goes away for good: every reconnect is refused.
	f.setDown(true)

	// Two minutes of failed attempts are not enough to give up.
	for range 12 {
		fake.Advance(10 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case e := <-failed:
		t.Fatalf("the driver gave up after two minutes: %+v", e)
	default:
	}

	// Past five minutes it stops trying and says so.
	var got fsp.Event
	deadline := time.Now().Add(5 * time.Second)
	for got.Kind == "" {
		select {
		case e, ok := <-failed:
			if !ok {
				t.Fatal("the event channel closed without an error event")
			}
			got = e
		default:
			if time.Now().After(deadline) {
				t.Fatal("the driver never gave up on the service")
			}
			fake.Advance(30 * time.Second)
			time.Sleep(2 * time.Millisecond)
		}
	}

	if got.Err == "" {
		t.Error("the error event says nothing about why")
	}
	// A parked session refuses prompts: there is nothing listening any more.
	if err := sess.Prompt(context.Background(), "anyone there"); err == nil {
		t.Error("a parked session accepted a prompt")
	}
}
