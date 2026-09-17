package approval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// fakeOutbox records what the approval module wanted to send.
type fakeOutbox struct {
	mu   sync.Mutex
	sent []fsp.Outbound
	ch   chan fsp.Outbound
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{ch: make(chan fsp.Outbound, 32)}
}

func (f *fakeOutbox) OpenWindow(fsp.EndpointID, string, time.Time) {}

func (f *fakeOutbox) Enqueue(_ context.Context, msg fsp.Outbound) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	select {
	case f.ch <- msg:
	default:
	}
	return nil
}

func (f *fakeOutbox) FlushHeld(context.Context, fsp.EndpointID) error { return nil }

func (f *fakeOutbox) Run(context.Context) error { return nil }

// next waits for the next message. The wait is test plumbing, not a business
// timeout, so it uses real time.
func (f *fakeOutbox) next(t *testing.T) fsp.Outbound {
	t.Helper()
	select {
	case msg := <-f.ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("no message was enqueued")
		return fsp.Outbound{}
	}
}

// quiet fails the test if anything else was sent within a short window.
func (f *fakeOutbox) quiet(t *testing.T) {
	t.Helper()
	select {
	case msg := <-f.ch:
		t.Fatalf("unexpected message %q", msg.Text)
	case <-time.After(50 * time.Millisecond):
	}
}

func (f *fakeOutbox) all() []fsp.Outbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsp.Outbound(nil), f.sent...)
}

var _ ports.Outbox = (*fakeOutbox)(nil)

// fakeI18n renders "key{Arg=value}" so that tests can assert on the key and on
// the arguments without depending on the wording of a locale file.
type fakeI18n struct{}

func (fakeI18n) T(_ fsp.Lang, key string, args map[string]any) string {
	if len(args) == 0 {
		return key
	}
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%v", n, args[n]))
	}
	return key + "{" + strings.Join(parts, ",") + "}"
}

var _ ports.I18n = fakeI18n{}

// --- P2 placeholders -------------------------------------------------------
// The Outbox interface grew Pull, File and Held in P2. The P2 T3 task gives
// them real behaviour and real tests; these keep P1's suite compiling.

func (f *fakeOutbox) Pull(ctx context.Context, ep fsp.EndpointID) error { return fsp.ErrUnsupported }

func (f *fakeOutbox) File(ctx context.Context, ep fsp.EndpointID, n int) error {
	return fsp.ErrUnsupported
}

func (f *fakeOutbox) Held(ep fsp.EndpointID) int { return 0 }
