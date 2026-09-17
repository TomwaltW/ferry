package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testStart is where the fake clock sits in every test: a fixed instant, so
// that an event's timestamp is comparable.
var testStart = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

// waitEvent reads the session's events until one matches, and fails the test if
// the channel runs out or nothing matches in time.
func waitEvent(t *testing.T, ch <-chan fsp.Event, what string, match func(fsp.Event) bool) fsp.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("the event channel closed before %s arrived", what)
			}
			if match(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func ofKind(k fsp.EventKind) func(fsp.Event) bool {
	return func(e fsp.Event) bool { return e.Kind == k }
}

// fakeApprover stands in for the core's approval module. Every request it is
// given lands on reqs, and it answers with verdict once release is closed, or
// at once when release is nil.
type fakeApprover struct {
	reqs    chan fsp.ApprovalRequest
	release chan struct{}

	mu      sync.Mutex
	verdict fsp.Verdict
	err     error
}

func newApprover(v fsp.Verdict) *fakeApprover {
	return &fakeApprover{reqs: make(chan fsp.ApprovalRequest, 4), verdict: v}
}

func (a *fakeApprover) Ask(ctx context.Context, req fsp.ApprovalRequest) (fsp.Verdict, error) {
	select {
	case a.reqs <- req:
	case <-ctx.Done():
		return fsp.Verdict{}, ctx.Err()
	}

	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return fsp.Verdict{}, ctx.Err()
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.verdict, a.err
}

// waitRequest takes the next approval request the driver raised.
func (a *fakeApprover) waitRequest(t *testing.T) fsp.ApprovalRequest {
	t.Helper()
	select {
	case req := <-a.reqs:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an approval request")
		return fsp.ApprovalRequest{}
	}
}

func TestLoopbackOnly(t *testing.T) {
	loopback := []string{
		"http://127.0.0.1:4096",
		"http://localhost:4096",
		"http://[::1]:4096",
		"http://127.0.0.2:4170",
		"https://localhost:4096",
	}
	elsewhere := []string{
		"http://192.168.1.10:4096",
		"http://10.0.0.1:4170",
		"http://example.com",
		"https://opencode.example.com:4096",
		"http://0.0.0.0:4096",
	}

	for _, kind := range []string{"opencode", "qwen"} {
		for _, raw := range loopback {
			spec := ports.SessionSpec{Agent: "a", Server: &ports.ServerSpec{Kind: kind, URL: raw}}
			if _, err := newBackend(spec, testLogger()); err != nil {
				t.Errorf("%s %s was refused: %v", kind, raw, err)
			}
		}
		for _, raw := range elsewhere {
			spec := ports.SessionSpec{Agent: "a", Server: &ports.ServerSpec{Kind: kind, URL: raw}}
			_, err := newBackend(spec, testLogger())
			if err == nil {
				t.Errorf("%s %s was accepted, want it refused", kind, raw)
				continue
			}
			if !strings.Contains(err.Error(), "loopback") {
				t.Errorf("%s %s was refused with %v, want the loopback rule", kind, raw, err)
			}
		}
	}

	// The refusal has to happen before anything is sent, so Start fails on the
	// address alone.
	d := newDriver(testLogger(), clock.NewFake(testStart))
	spec := ports.SessionSpec{Agent: "a", Server: &ports.ServerSpec{Kind: "opencode", URL: "http://192.168.1.10:4096"}}
	if _, err := d.Start(context.Background(), spec, nil); err == nil {
		t.Fatal("Start accepted a non-loopback address")
	}
}

func TestUnknownServiceKind(t *testing.T) {
	d := newDriver(testLogger(), clock.NewFake(testStart))

	spec := ports.SessionSpec{Agent: "a", Server: &ports.ServerSpec{Kind: "kimi", URL: "http://127.0.0.1:4096"}}
	_, err := d.Start(context.Background(), spec, nil)
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("an unknown service kind gave %v, want fsp.ErrUnsupported", err)
	}

	_, err = d.Start(context.Background(), ports.SessionSpec{Agent: "a"}, nil)
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("an agent without a server block gave %v, want fsp.ErrUnsupported", err)
	}
}

func TestDriverKind(t *testing.T) {
	if got := New(testLogger()).Kind(); got != fsp.DriverServer {
		t.Errorf("Kind() = %q, want %q", got, fsp.DriverServer)
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := backoffAt(i); got != w {
			t.Errorf("backoffAt(%d) = %v, want %v", i, got, w)
		}
	}
}

func TestReadSSEFrames(t *testing.T) {
	body := strings.NewReader(
		": keep alive\n" +
			"event: turn_complete\n" +
			"data: {\"a\":1}\n" +
			"\n" +
			"data: line one\n" +
			"data: line two\n" +
			"\n" +
			"event: last\n" +
			"data: {}\n",
	)

	var got []sseEvent
	if err := readSSE(body, func(e sseEvent) { got = append(got, e) }); err != nil {
		t.Fatalf("readSSE failed: %v", err)
	}

	want := []sseEvent{
		{Name: "turn_complete", Data: []byte(`{"a":1}`)},
		{Name: "", Data: []byte("line one\nline two")},
		{Name: "last", Data: []byte("{}")},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d frames, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || string(got[i].Data) != string(want[i].Data) {
			t.Errorf("frame %d = %q/%q, want %q/%q", i, got[i].Name, got[i].Data, want[i].Name, want[i].Data)
		}
	}
}
