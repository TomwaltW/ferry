package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/i18n"
	"github.com/TomwaltW/ferry/internal/ports"
)

var testEndpoint = fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "peer"}

var start = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

// capture records what a fake service received.
type capture struct {
	mu      sync.Mutex
	paths   []string // decoded
	escaped []string // as it travelled on the wire
	bodies  []string
	headers []http.Header
	status  int
}

func (c *capture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.paths = append(c.paths, r.URL.Path)
		c.escaped = append(c.escaped, r.URL.EscapedPath())
		c.bodies = append(c.bodies, string(body))
		c.headers = append(c.headers, r.Header.Clone())
		status := c.status
		c.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

func (c *capture) at(t *testing.T, i int) (path, body string, h http.Header) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i > len(c.paths)-1 {
		t.Fatalf("request %d does not exist (%d received)", i, len(c.paths))
	}
	return c.paths[i], c.bodies[i], c.headers[i]
}

// escapedAt returns the request path exactly as it was sent.
func (c *capture) escapedAt(t *testing.T, i int) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i > len(c.escaped)-1 {
		t.Fatalf("request %d does not exist (%d received)", i, len(c.escaped))
	}
	return c.escaped[i]
}

// newWaker builds a Waker over the real catalogue and a fake clock.
func newWaker(t *testing.T, cfg config.NotifyConfig, clk clock.Clock) ports.Waker {
	t.Helper()
	w, err := New(Options{
		Config: cfg,
		I18n:   i18n.Default(),
		Lang:   func(fsp.EndpointID) fsp.Lang { return fsp.LangZH },
		Clock:  clk,
		Log:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

func TestNtfy(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	t.Setenv("FERRY_TEST_NTFY_TOKEN", "tok-123")

	cfg := config.NotifyConfig{
		Enabled: []string{"ntfy"},
		Events:  []string{KindApprovalPending},
		Ntfy:    config.NtfyConfig{URL: srv.URL, Topic: "ferry-topic", TokenEnv: "FERRY_TEST_NTFY_TOKEN"},
	}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{"Slot": 2})

	if c.count() != 1 {
		t.Fatalf("ntfy received %d requests, want 1", c.count())
	}
	path, body, h := c.at(t, 0)
	if path != "/ferry-topic" {
		t.Errorf("path = %q, want /ferry-topic", path)
	}
	if h.Get("Authorization") != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want the bearer token", h.Get("Authorization"))
	}
	wantTitle := i18n.Default().T(fsp.LangZH, "notify.approval_pending.title", nil)
	if h.Get("Title") != wantTitle {
		t.Errorf("Title header = %q, want %q", h.Get("Title"), wantTitle)
	}
	if !strings.Contains(body, "2") {
		t.Errorf("body = %q, want it to name session 2", body)
	}
}

func TestNtfyWithoutToken(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: []string{"ntfy"},
		Ntfy:    config.NtfyConfig{URL: srv.URL, Topic: "t"},
	}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindIMOffline, nil)

	if c.count() != 1 {
		t.Fatalf("ntfy received %d requests, want 1", c.count())
	}
	if _, _, h := c.at(t, 0); h.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want none when no token is configured", h.Get("Authorization"))
	}
}

func TestBark(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	t.Setenv("FERRY_TEST_BARK_KEY", "device/key")

	cfg := config.NotifyConfig{
		Enabled: []string{"bark"},
		Bark:    config.BarkConfig{URL: srv.URL, KeyEnv: "FERRY_TEST_BARK_KEY"},
	}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindTurnFinished, map[string]any{"Slot": 1})

	if c.count() != 1 {
		t.Fatalf("bark received %d requests, want 1", c.count())
	}
	path := c.escapedAt(t, 0)
	// The key contains a slash: escaped, it stays one path segment, so the
	// title and body cannot be pushed out of place.
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) != 3 {
		t.Fatalf("escaped path = %q, want three segments key/title/body", path)
	}
	decoded := make([]string, len(segments))
	for i, seg := range segments {
		v, err := url.PathUnescape(seg)
		if err != nil {
			t.Fatalf("unescape %q: %v", seg, err)
		}
		decoded[i] = v
	}
	if decoded[0] != "device/key" {
		t.Errorf("key segment = %q, want the device key", decoded[0])
	}
	wantTitle := i18n.Default().T(fsp.LangZH, "notify.turn_finished.title", nil)
	if decoded[1] != wantTitle {
		t.Errorf("title segment = %q, want %q", decoded[1], wantTitle)
	}
	if !strings.Contains(decoded[2], "1") {
		t.Errorf("body segment = %q, want it to name session 1", decoded[2])
	}
}

func TestWebhookSignature(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	t.Setenv("FERRY_TEST_WEBHOOK_SECRET", "s3cret")

	cfg := config.NotifyConfig{
		Enabled: []string{"webhook"},
		Webhook: config.WebhookConfig{URL: srv.URL + "/hook", SecretEnv: "FERRY_TEST_WEBHOOK_SECRET"},
	}
	clk := clock.NewFake(start)
	w := newWaker(t, cfg, clk)
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{"Slot": 3})

	if c.count() != 1 {
		t.Fatalf("webhook received %d requests, want 1", c.count())
	}
	path, body, h := c.at(t, 0)
	if path != "/hook" {
		t.Errorf("path = %q, want /hook", path)
	}
	var payload webhookPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload %q: %v", body, err)
	}
	if payload.Kind != KindApprovalPending {
		t.Errorf("kind = %q, want %q", payload.Kind, KindApprovalPending)
	}
	if payload.Title == "" || payload.Body == "" {
		t.Errorf("payload = %+v, want a rendered title and body", payload)
	}
	if payload.TS != start.Format(time.RFC3339) {
		t.Errorf("ts = %q, want the clock's %q", payload.TS, start.Format(time.RFC3339))
	}
	want := Sign("s3cret", []byte(body))
	if got := h.Get(SignatureHeader); got != want {
		t.Errorf("%s = %q, want %q", SignatureHeader, got, want)
	}
}

func TestWebhookWithoutSecretIsUnsigned(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{Enabled: []string{"webhook"}, Webhook: config.WebhookConfig{URL: srv.URL}}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindIMOffline, nil)

	if _, _, h := c.at(t, 0); h.Get(SignatureHeader) != "" {
		t.Errorf("%s = %q, want no signature without a secret", SignatureHeader, h.Get(SignatureHeader))
	}
}

func TestEmailSMTP(t *testing.T) {
	s := startFakeSMTP(t)
	t.Setenv("FERRY_TEST_SMTP_PASSWORD", "hunter2")

	cfg := config.EmailConfig{
		Host:        "127.0.0.1",
		Port:        s.port(t),
		User:        "ferry@example.com",
		PasswordEnv: "FERRY_TEST_SMTP_PASSWORD",
		From:        "ferry@example.com",
		To:          []string{"me@example.com", "backup@example.com"},
	}
	ch, err := newEmail(cfg)
	if err != nil {
		t.Fatalf("newEmail: %v", err)
	}
	// Trust the fake's own certificate; everywhere else the system roots apply.
	ch.(*emailChannel).tlsConfig = &tls.Config{ServerName: "127.0.0.1", RootCAs: s.roots, MinVersion: tls.VersionTLS12}

	body := i18n.Default().T(fsp.LangZH, "notify.approval_pending.body", map[string]any{"Slot": 4})
	title := i18n.Default().T(fsp.LangZH, "notify.approval_pending.title", nil)
	if err := ch.send(context.Background(), note{Kind: KindApprovalPending, Title: title, Body: body, At: start}); err != nil {
		t.Fatalf("send: %v", err)
	}

	from, rcpts, data, _, started := s.snapshot()
	if !started {
		t.Error("the session never went through STARTTLS")
	}
	if from != "ferry@example.com" {
		t.Errorf("MAIL FROM = %q, want ferry@example.com", from)
	}
	if len(rcpts) != 2 || rcpts[0] != "me@example.com" || rcpts[1] != "backup@example.com" {
		t.Errorf("RCPT TO = %v, want both recipients", rcpts)
	}
	if user, password := s.decodedAuth(t); user != "ferry@example.com" || password != "hunter2" {
		t.Errorf("AUTH PLAIN = %q / %q, want the configured credentials", user, password)
	}
	if !strings.Contains(data, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("message has no UTF-8 content type:\n%s", data)
	}
	// A Chinese subject is not a legal header unencoded.
	if !strings.Contains(data, "Subject: =?utf-8?q?") {
		t.Errorf("subject is not RFC 2047 encoded:\n%s", data)
	}
	if !strings.Contains(data, "4") {
		t.Errorf("message body does not name session 4:\n%s", data)
	}
}

func TestEmailNeedsFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  config.EmailConfig
	}{
		{"no host", config.EmailConfig{From: "a@b", To: []string{"c@d"}}},
		{"no from", config.EmailConfig{Host: "h", To: []string{"c@d"}}},
		{"no recipients", config.EmailConfig{Host: "h", From: "a@b"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newEmail(tt.cfg); err == nil {
				t.Fatal("newEmail = nil error, want a complaint about the missing field")
			}
		})
	}
}

func TestMinInterval(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	clk := clock.NewFake(start)
	cfg := config.NotifyConfig{
		Enabled:     []string{"ntfy"},
		MinInterval: config.Duration{Duration: 10 * time.Minute},
		Ntfy:        config.NtfyConfig{URL: srv.URL, Topic: "t"},
	}
	w := newWaker(t, cfg, clk)
	ctx := context.Background()

	w.Wake(ctx, testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
	w.Wake(ctx, testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
	if c.count() != 1 {
		t.Fatalf("sent %d reminders inside the interval, want 1", c.count())
	}

	// A different kind is a different reminder and is not held back.
	w.Wake(ctx, testEndpoint, KindTurnFinished, map[string]any{"Slot": 1})
	if c.count() != 2 {
		t.Fatalf("sent %d reminders, want the other kind through as well", c.count())
	}

	// So is the same kind for a different endpoint.
	other := fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "bot", PeerID: "other"}
	w.Wake(ctx, other, KindApprovalPending, map[string]any{"Slot": 1})
	if c.count() != 3 {
		t.Fatalf("sent %d reminders, want another endpoint through as well", c.count())
	}

	clk.Advance(9 * time.Minute)
	w.Wake(ctx, testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
	if c.count() != 3 {
		t.Fatalf("sent %d reminders after 9m, want the 10m interval to hold", c.count())
	}

	clk.Advance(time.Minute)
	w.Wake(ctx, testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
	if c.count() != 4 {
		t.Fatalf("sent %d reminders after 10m, want the interval to have passed", c.count())
	}
}

// TestNoSensitiveContent is the B10 judge: a reminder names the session and
// nothing else. Tool names, commands, paths and message bodies never leave the
// machine through a wake-up channel.
func TestNoSensitiveContent(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: []string{"ntfy", "webhook"},
		Ntfy:    config.NtfyConfig{URL: srv.URL, Topic: "t"},
		Webhook: config.WebhookConfig{URL: srv.URL + "/hook"},
	}
	w := newWaker(t, cfg, clock.NewFake(start))

	secrets := []string{
		"run_shell_command",
		"rm -rf /tmp/demo",
		"/Users/me/code/demo/secret.go",
		"the agent finished refactoring the parser",
	}
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{
		"Slot":         7,
		"ToolName":     secrets[0],
		"Command":      secrets[1],
		"Path":         secrets[2],
		"Text":         secrets[3],
		"InputPreview": secrets[1],
	})

	if c.count() != 2 {
		t.Fatalf("received %d requests, want one per channel", c.count())
	}
	for i := range 2 {
		path, body, h := c.at(t, i)
		whole := path + "\n" + body + "\n" + h.Get("Title")
		for _, s := range secrets {
			if strings.Contains(whole, s) {
				t.Errorf("request %d leaks %q:\n%s", i, s, whole)
			}
		}
		if !strings.Contains(whole, "7") {
			t.Errorf("request %d does not name session 7:\n%s", i, whole)
		}
	}
}

func TestEventsFilter(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: []string{"ntfy"},
		Events:  []string{KindApprovalPending},
		Ntfy:    config.NtfyConfig{URL: srv.URL, Topic: "t"},
	}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindTurnFinished, map[string]any{"Slot": 1})
	if c.count() != 0 {
		t.Errorf("sent %d reminders for a kind outside notify.events, want 0", c.count())
	}
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
	if c.count() != 1 {
		t.Errorf("sent %d reminders for an enabled kind, want 1", c.count())
	}
}

func TestNoChannelsIsSilent(t *testing.T) {
	w := newWaker(t, config.NotifyConfig{}, clock.NewFake(start))
	// Nothing to assert beyond it not panicking or blocking: notify is off.
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})
}

// TestSendRetriesOnce pins spec-p2 3.12: a failed reminder is retried at most
// once, and the failure is swallowed rather than returned.
func TestSendRetriesOnce(t *testing.T) {
	c := &capture{status: http.StatusInternalServerError}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{Enabled: []string{"ntfy"}, Ntfy: config.NtfyConfig{URL: srv.URL, Topic: "t"}}
	w := newWaker(t, cfg, clock.NewFake(start))
	w.Wake(context.Background(), testEndpoint, KindApprovalPending, map[string]any{"Slot": 1})

	if c.count() != 2 {
		t.Errorf("attempts = %d, want the first plus one retry", c.count())
	}
}

func TestNewRejectsUnknownChannel(t *testing.T) {
	_, err := New(Options{Config: config.NotifyConfig{Enabled: []string{"sms"}}})
	if err == nil {
		t.Fatal("New with an unknown channel = nil error, want a complaint")
	}
}

// TestNotifyTestCommand covers what `ferry notify test <channel>` does: build
// one channel from the configuration and push a single reminder through it.
func TestNotifyTestCommand(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{
		// The channel need not be enabled: the point is to try it out first.
		Ntfy: config.NtfyConfig{URL: srv.URL, Topic: "check"},
	}
	if err := Test(context.Background(), cfg, "ntfy"); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if c.count() != 1 {
		t.Fatalf("received %d requests, want 1", c.count())
	}
	path, body, h := c.at(t, 0)
	if path != "/check" {
		t.Errorf("path = %q, want /check", path)
	}
	if h.Get("Title") != testTitle || body != testBody {
		t.Errorf("title/body = %q / %q, want the test reminder", h.Get("Title"), body)
	}

	if err := Test(context.Background(), cfg, "sms"); err == nil {
		t.Error("Test with an unknown channel = nil error, want a complaint")
	}
	// A channel with nothing configured cannot be tested either.
	if err := Test(context.Background(), config.NotifyConfig{}, "webhook"); err == nil {
		t.Error("Test without a webhook URL = nil error, want a complaint")
	}
}

func TestTestReportsServiceFailure(t *testing.T) {
	c := &capture{status: http.StatusForbidden}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	cfg := config.NotifyConfig{Ntfy: config.NtfyConfig{URL: srv.URL, Topic: "t"}}
	err := Test(context.Background(), cfg, "ntfy")
	if err == nil {
		t.Fatal("Test against a refusing service = nil error, want the status reported")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want it to carry the status", err)
	}
}
