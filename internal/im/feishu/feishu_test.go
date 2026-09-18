package feishu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/security"

	"github.com/zalando/go-keyring"
)

const (
	testAppID  = "cli_app"
	testOwner  = "ou_owner"
	testSecret = "s3cret"
)

var epoch = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

// fakeClient is the local stand-in for the SDK. feishutest cannot be used from
// inside this package — it imports it — so the double lives here.
type fakeClient struct {
	mu        sync.Mutex
	inbox     chan Message
	sent      []sentMessage
	cards     []Card
	onCard    func(CardAction) (string, bool) // set by Start
	startErrs []error
	sendErrs  []error
	pingErr   error
	starts    int
	started   chan struct{}
}

type sentMessage struct{ kind, openID, body string }

func newFakeClient() *fakeClient {
	return &fakeClient{inbox: make(chan Message, 8), started: make(chan struct{}, 8)}
}

func (c *fakeClient) Start(ctx context.Context, onMessage func(Message), onCardAction func(CardAction) (string, bool)) error {
	c.mu.Lock()
	c.starts++
	c.onCard = onCardAction
	var err error
	if len(c.startErrs) != 0 {
		err, c.startErrs = c.startErrs[0], c.startErrs[1:]
	}
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-c.inbox:
			onMessage(m)
		}
	}
}

func (c *fakeClient) SendText(_ context.Context, openID, text string) (string, error) {
	return c.record(sentMessage{kind: "text", openID: openID, body: text})
}

// SendCard books the card as a "card" message with its body, and keeps the
// card itself in cards.
func (c *fakeClient) SendCard(_ context.Context, openID string, card Card) (string, error) {
	id, err := c.record(sentMessage{kind: "card", openID: openID, body: card.Body})
	if err == nil {
		c.mu.Lock()
		c.cards = append(c.cards, card)
		c.mu.Unlock()
	}
	return id, err
}

// click calls the card handler of the running Start, as the SDK does from
// its own goroutine.
func (c *fakeClient) click(act CardAction) (string, bool) {
	c.mu.Lock()
	h := c.onCard
	c.mu.Unlock()
	return h(act)
}

func (c *fakeClient) sentCards() []Card {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Card(nil), c.cards...)
}

func (c *fakeClient) SendFile(_ context.Context, openID, path string) (string, error) {
	return c.record(sentMessage{kind: "file", openID: openID, body: path})
}

func (c *fakeClient) record(s sentMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sendErrs) != 0 {
		var err error
		err, c.sendErrs = c.sendErrs[0], c.sendErrs[1:]
		if err != nil {
			return "", err
		}
	}
	c.sent = append(c.sent, s)
	return "m-" + s.openID, nil
}

func (c *fakeClient) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingErr
}

func (c *fakeClient) messages() []sentMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentMessage(nil), c.sent...)
}

func (c *fakeClient) sendCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent) + len(c.sendErrs)
}

// ---------- harness ----------

type rig struct {
	t      *testing.T
	a      *Adapter
	client *fakeClient
	clk    *clock.Fake
	in     chan fsp.Inbound
	errs   chan error
	cancel context.CancelFunc
}

// newRig builds an adapter on a fake client; each option adjusts the Options
// before the adapter is made.
func newRig(t *testing.T, options ...func(*Options)) *rig {
	t.Helper()
	c := newFakeClient()
	clk := clock.NewFake(epoch)
	opts := Options{
		Config: config.FeishuConfig{Enabled: true, AppID: testAppID, OwnerOpenID: testOwner},
		Secret: testSecret,
		Home:   t.TempDir(),
		Clock:  clk,
		Log:    slog.New(slog.DiscardHandler),
	}
	for _, o := range options {
		o(&opts)
	}
	a, err := NewWithClient(opts, c)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return &rig{t: t, a: a, client: c, clk: clk, in: make(chan fsp.Inbound, 8), errs: make(chan error, 1)}
}

// run starts the adapter and waits until the client's connection is open.
func (r *rig) run() *rig {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.errs <- r.a.Run(ctx, r.in) }()
	select {
	case <-r.client.started:
	case <-time.After(5 * time.Second):
		r.t.Fatal("the adapter never opened the connection")
	}
	r.t.Cleanup(func() {
		cancel()
		select {
		case <-r.errs:
		case <-time.After(5 * time.Second):
			r.t.Error("Run did not return after the context was cancelled")
		}
	})
	return r
}

func (r *rig) waitInbound() fsp.Inbound {
	r.t.Helper()
	select {
	case in := <-r.in:
		return in
	case <-time.After(5 * time.Second):
		r.t.Fatal("no inbound message arrived")
		return fsp.Inbound{}
	}
}

// ---------- inbound ----------

func TestFeishuReceiveP2P(t *testing.T) {
	r := newRig(t).run()
	at := epoch.Add(-time.Minute)
	r.client.inbox <- Message{
		MessageID:  "om_1",
		ChatType:   ChatTypeP2P,
		OpenID:     "ou_bob",
		Text:       "hello",
		CreateTime: at,
	}

	in := r.waitInbound()
	want := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}
	if in.Endpoint != want {
		t.Errorf("endpoint = %+v, want %+v", in.Endpoint, want)
	}
	if in.SenderID != "ou_bob" {
		t.Errorf("sender = %q, want %q", in.SenderID, "ou_bob")
	}
	if in.MessageID != "om_1" || in.ReplyCtx != "om_1" {
		t.Errorf("message = %q and reply context = %q, want both %q", in.MessageID, in.ReplyCtx, "om_1")
	}
	if in.Text != "hello" {
		t.Errorf("text = %q, want %q", in.Text, "hello")
	}
	if !in.ReceivedAt.Equal(at) {
		t.Errorf("received at %v, want %v", in.ReceivedAt, at)
	}
	if st := r.a.Status(); !st.Online {
		t.Errorf("status after a message = %+v, want it online", st)
	}
}

func TestFeishuIgnoreGroup(t *testing.T) {
	r := newRig(t).run()
	r.client.inbox <- Message{MessageID: "om_g", ChatType: ChatTypeGroup, OpenID: "ou_bob", Text: "hi all"}
	// A direct message queued behind it proves the group one was handled and
	// dropped rather than merely slow.
	r.client.inbox <- Message{MessageID: "om_p", ChatType: ChatTypeP2P, OpenID: "ou_bob", Text: "hi"}

	in := r.waitInbound()
	if in.MessageID != "om_p" {
		t.Fatalf("first inbound message was %q, want the group message to be dropped", in.MessageID)
	}
	select {
	case extra := <-r.in:
		t.Errorf("a second message arrived: %+v", extra)
	default:
	}
}

// ---------- outbound ----------

func TestFeishuSendText(t *testing.T) {
	r := newRig(t)
	ep := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}

	st, err := r.a.Send(context.Background(), fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: "done"}, "om_1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if st != fsp.DeliveryAccepted {
		t.Errorf("state = %q, want %q", st, fsp.DeliveryAccepted)
	}
	want := []sentMessage{{kind: "text", openID: "ou_bob", body: "done"}}
	if got := r.client.messages(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("sent %+v, want %+v", got, want)
	}
}

func TestFeishuSendFile(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "turn.md")
	if err := os.WriteFile(path, []byte("long output"), 0o600); err != nil {
		t.Fatalf("write the file: %v", err)
	}
	ep := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}

	st, err := r.a.Send(context.Background(), fsp.Outbound{
		Endpoint: ep, Kind: fsp.OutFile, FilePath: path, Text: filepath.Base(path),
	}, "om_1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if st != fsp.DeliveryAccepted {
		t.Errorf("state = %q, want %q", st, fsp.DeliveryAccepted)
	}
	want := sentMessage{kind: "file", openID: "ou_bob", body: path}
	if got := r.client.messages(); len(got) != 1 || got[0] != want {
		t.Errorf("sent %+v, want %+v", got, want)
	}
}

// TestFeishuRateLimit: a send Feishu refuses for going too fast is retried
// after 1, 2 and 4 seconds, and no more than three times (spec-p3 3.10).
func TestFeishuRateLimit(t *testing.T) {
	t.Run("recovers within three retries", func(t *testing.T) {
		r := newRig(t)
		r.client.sendErrs = []error{ErrRateLimited, ErrRateLimited}
		ep := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}

		type outcome struct {
			state fsp.DeliveryState
			err   error
		}
		done := make(chan outcome, 1)
		go func() {
			st, err := r.a.Send(context.Background(), fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: "hi"}, "")
			done <- outcome{st, err}
		}()

		for _, want := range []time.Duration{time.Second, 2 * time.Second} {
			r.clk.BlockUntil(1)
			r.clk.Advance(want)
		}
		got := <-done
		if got.err != nil {
			t.Fatalf("Send: %v", got.err)
		}
		if got.state != fsp.DeliveryAccepted {
			t.Errorf("state = %q, want %q", got.state, fsp.DeliveryAccepted)
		}
		if elapsed := r.clk.Now().Sub(epoch); elapsed != 3*time.Second {
			t.Errorf("backed off for %v, want 1s then 2s", elapsed)
		}
		if n := len(r.client.messages()); n != 1 {
			t.Errorf("%d messages got through, want 1", n)
		}
	})

	t.Run("gives up after three retries", func(t *testing.T) {
		r := newRig(t)
		r.client.sendErrs = []error{ErrRateLimited, ErrRateLimited, ErrRateLimited, ErrRateLimited, ErrRateLimited}
		ep := fsp.EndpointID{Platform: fsp.PlatformFeishu, BotID: testAppID, PeerID: "ou_bob"}

		errs := make(chan error, 1)
		states := make(chan fsp.DeliveryState, 1)
		go func() {
			st, err := r.a.Send(context.Background(), fsp.Outbound{Endpoint: ep, Kind: fsp.OutText, Text: "hi"}, "")
			states <- st
			errs <- err
		}()
		for _, want := range rateLimitBackoff {
			r.clk.BlockUntil(1)
			r.clk.Advance(want)
		}
		if err := <-errs; !errors.Is(err, ErrRateLimited) {
			t.Fatalf("Send error = %v, want it to wrap ErrRateLimited", err)
		}
		if st := <-states; st != fsp.DeliveryUnknown {
			t.Errorf("state = %q, want %q", st, fsp.DeliveryUnknown)
		}
		// One attempt plus three retries, and the fifth scripted error unused.
		if left := r.client.sendCalls(); left != 1 {
			t.Errorf("%d scripted outcomes left, want 1 (four attempts in all)", left)
		}
	})
}

// ---------- connection ----------

func TestFeishuOfflineAfter5Min(t *testing.T) {
	r := newRig(t)
	down := errors.New("websocket closed")
	r.client.startErrs = []error{down, down}

	err := r.a.Run(context.Background(), r.in)
	if err == nil {
		t.Fatal("Run returned nil for a connection that failed")
	}
	if errors.Is(err, fsp.ErrSessionExpired) {
		t.Fatalf("Run reported the entry point expired straight away: %v", err)
	}
	if st := r.a.Status(); st.Online {
		t.Errorf("status = %+v, want it offline", st)
	}

	// The core retries Run; five minutes later the outage is no longer a blip.
	r.clk.Advance(OfflineAfter)
	err = r.a.Run(context.Background(), r.in)
	if !errors.Is(err, fsp.ErrSessionExpired) {
		t.Fatalf("Run after %v down = %v, want fsp.ErrSessionExpired", OfflineAfter, err)
	}
	if st := r.a.Status(); st.Online {
		t.Errorf("status = %+v, want it offline", st)
	}
}

func TestFeishuOwnerStatus(t *testing.T) {
	r := newRig(t)
	st := r.a.Status()
	if st.Online {
		t.Errorf("a fresh adapter is %+v, want it offline until Run", st)
	}
	if st.BotID != testAppID {
		t.Errorf("bot = %q, want the app ID %q", st.BotID, testAppID)
	}
	if st.OwnerID != testOwner {
		t.Errorf("owner = %q, want owner_open_id %q", st.OwnerID, testOwner)
	}
	if lim := r.a.Limits(); lim.NeedsWindow || lim.RatePerSec != RatePerSec {
		t.Errorf("limits = %+v, want no window and %v per second", lim, float64(RatePerSec))
	}
	if p := r.a.Platform(); p != fsp.PlatformFeishu {
		t.Errorf("platform = %q, want %q", p, fsp.PlatformFeishu)
	}
}

// ---------- login ----------

func TestLoginInteractive(t *testing.T) {
	keyring.MockInit() // never touch the developer's real keychain
	home := t.TempDir()
	writeConfig(t, home)

	client := newFakeClient()
	restore := newClient
	newClient = func(opts Options) (Client, error) {
		if opts.Config.AppID != testAppID || opts.Secret != testSecret {
			return nil, fmt.Errorf("login built a client with %q/%q", opts.Config.AppID, opts.Secret)
		}
		return client, nil
	}
	t.Cleanup(func() { newClient = restore })

	var out bytes.Buffer
	in := strings.NewReader(testAppID + "\n" + testSecret + "\n" + testOwner + "\n")
	if err := LoginInteractive(context.Background(), in, &out, home); err != nil {
		t.Fatalf("LoginInteractive: %v", err)
	}

	cfg, err := config.Load(config.Path(home))
	if err != nil {
		t.Fatalf("reload the configuration: %v", err)
	}
	if !cfg.Feishu.Enabled || cfg.Feishu.AppID != testAppID || cfg.Feishu.OwnerOpenID != testOwner {
		t.Errorf("configuration holds %+v, want feishu enabled for %q and %q", cfg.Feishu, testAppID, testOwner)
	}
	if strings.Contains(readFile(t, config.Path(home)), testSecret) {
		t.Error("the app secret reached the configuration file")
	}

	creds, err := security.LoadCredentials(home, fsp.PlatformFeishu)
	if err != nil {
		t.Fatalf("load the stored credentials: %v", err)
	}
	if creds.BotToken != testSecret {
		t.Errorf("stored secret = %q, want %q", creds.BotToken, testSecret)
	}
	if creds.BotID != testAppID || creds.OwnerID != testOwner {
		t.Errorf("stored credentials = %+v, want the app ID and the owner open ID", creds)
	}
	if !strings.Contains(out.String(), testAppID) {
		t.Errorf("the transcript is %q, want it to name the app", out.String())
	}

	t.Run("a Feishu that refuses the credentials writes nothing", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home)
		client.pingErr = errors.New("app not found")

		err := LoginInteractive(context.Background(), strings.NewReader(
			testAppID+"\n"+testSecret+"\n"+testOwner+"\n"), io.Discard, home)
		if err == nil {
			t.Fatal("LoginInteractive accepted credentials Feishu refused")
		}
		cfg, err := config.Load(config.Path(home))
		if err != nil {
			t.Fatalf("reload the configuration: %v", err)
		}
		if cfg.Feishu.Enabled {
			t.Errorf("configuration holds %+v, want Feishu left disabled", cfg.Feishu)
		}
	})
}

func writeConfig(t *testing.T, home string) {
	t.Helper()
	cfg := config.Default()
	cfg.Workspace.Roots = []string{home}
	if err := config.Save(config.Path(home), cfg); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
