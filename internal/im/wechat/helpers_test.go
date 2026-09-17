package wechat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ilinkwire"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/TomwaltW/ferry/internal/testkit/fakeilink"
)

var epoch = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

const (
	owner = "user-owner"
	peer  = "user-peer"
	// recvTimeout only guards against a test hanging forever; nothing waits for
	// it when the code works.
	recvTimeout = 5 * time.Second
)

// memKV is the in-memory stand-in for ports.KV, which has no implementation in
// this worktree.
type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemKV() *memKV { return &memKV{m: map[string]string{}} }

func (k *memKV) GetKV(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	if !ok {
		return "", fsp.ErrNotFound
	}
	return v, nil
}

func (k *memKV) SetKV(_ context.Context, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = value
	return nil
}

// peek returns a key without the not-found error, for assertions.
func (k *memKV) peek(key string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key]
}

var _ ports.KV = (*memKV)(nil)

// recordingClock reports every business wait the code asks for, so a test can
// assert the backoff schedule instead of guessing at it.
type recordingClock struct {
	*clock.Fake
	waits chan time.Duration
}

func newRecordingClock(start time.Time) *recordingClock {
	return &recordingClock{Fake: clock.NewFake(start), waits: make(chan time.Duration, 64)}
}

func (c *recordingClock) After(d time.Duration) <-chan time.Time {
	ch := c.Fake.After(d)
	c.waits <- d
	return ch
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newAdapter builds an adapter already logged in against a fake gateway.
func newAdapter(t *testing.T, s *fakeilink.Server, kv ports.KV, clk clock.Clock) *Adapter {
	t.Helper()
	return newAdapterAt(t, s.URL, ports.Credentials{
		BotToken: s.BotToken(),
		BotID:    s.BotID(),
		OwnerID:  owner,
		BaseURL:  s.URL,
	}, kv, clk)
}

// newAdapterAt builds an adapter pointed at any base URL, for the tests that
// script a gateway of their own.
func newAdapterAt(t *testing.T, base string, creds ports.Credentials, kv ports.KV, clk clock.Clock) *Adapter {
	t.Helper()
	a, err := New(Options{
		Config: config.WeChatConfig{Enabled: true, BaseURL: base, Render: renderPlainMode},
		Creds:  creds,
		KV:     kv,
		Home:   t.TempDir(),
		Clock:  clk,
		Log:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// loggedIn starts a fake gateway that already holds a bot token.
func loggedIn(t *testing.T, opts fakeilink.Options) *fakeilink.Server {
	t.Helper()
	s := fakeilink.New(opts)
	t.Cleanup(s.Close)
	s.ConfirmScan(owner)
	return s
}

// stubServer is a gateway scripted by the test itself, for the answers
// fakeilink cannot produce (bot-authored messages, redirects, 5xx sequences).
type stubServer struct {
	*httptest.Server
	done chan struct{}
}

func stub(t *testing.T, h func(g *stubServer, w http.ResponseWriter, r *http.Request)) *stubServer {
	t.Helper()
	g := &stubServer{done: make(chan struct{})}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(g, w, r)
	}))
	t.Cleanup(g.Server.Close)
	// Registered last, so it runs first: every parked handler is released
	// before Close waits for them.
	t.Cleanup(func() { close(g.done) })
	return g
}

// park holds a request until the client goes away or the test ends, so a
// scripted gateway never answers in a hot loop.
func (g *stubServer) park(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-g.done:
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// running is one Adapter.Run in flight.
type running struct {
	out    chan fsp.Inbound
	errc   chan error
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

func start(t *testing.T, a *Adapter) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{out: make(chan fsp.Inbound, 8), errc: make(chan error, 1), cancel: cancel}
	go func() { r.errc <- a.Run(ctx, r.out) }()
	t.Cleanup(func() { r.stop() })
	return r
}

// stop ends the run and returns what Run returned.
func (r *running) stop() error {
	r.once.Do(func() {
		r.cancel()
		select {
		case r.err = <-r.errc:
		case <-time.After(recvTimeout):
			r.err = context.DeadlineExceeded
		}
	})
	return r.err
}

// wait returns what Run returned without cancelling it first.
func (r *running) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.errc:
		r.once.Do(func() { r.err = err })
		return err
	case <-time.After(recvTimeout):
		t.Fatal("Run did not return")
		return nil
	}
}

func (r *running) recv(t *testing.T) fsp.Inbound {
	t.Helper()
	select {
	case in := <-r.out:
		return in
	case <-time.After(recvTimeout):
		t.Fatal("no inbound message arrived")
		return fsp.Inbound{}
	}
}

// textMessage is a user message carrying one text item.
func textMessage(id int64, from, text string) ilinkwire.Message {
	return ilinkwire.Message{
		MessageID:    id,
		FromUserID:   from,
		MessageType:  ilinkwire.MsgTypeUser,
		CreateTimeMs: epoch.UnixMilli(),
		ContextToken: "ctx-" + from,
		ItemList: []ilinkwire.MessageItem{{
			Type:     ilinkwire.ItemText,
			TextItem: &ilinkwire.TextItem{Text: text},
		}},
	}
}

// rewriteHost sends requests for host to target instead, which is how a test
// reaches an https redirect host with a local server.
type rewriteHost struct {
	host   string
	target *url.URL
}

func (t rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == t.host {
		clone := r.Clone(r.Context())
		clone.URL.Scheme = t.target.Scheme
		clone.URL.Host = t.target.Host
		clone.Host = ""
		r = clone
	}
	return http.DefaultTransport.RoundTrip(r)
}
