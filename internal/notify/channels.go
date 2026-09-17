package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/config"
)

// httpTimeout bounds one attempt at a channel that speaks HTTP.
const httpTimeout = 10 * time.Second

// sharedClient is used by every HTTP channel; the timeout is per attempt.
var sharedClient = &http.Client{Timeout: httpTimeout}

// ---------- ntfy ----------

type ntfyChannel struct {
	url   string // <base>/<topic>
	token string
	http  *http.Client
}

func newNtfy(cfg config.NtfyConfig) (channel, error) {
	if cfg.Topic == "" {
		return nil, errors.New("notify: ntfy needs notify.ntfy.topic")
	}
	base := cfg.URL
	if base == "" {
		base = "https://ntfy.sh"
	}
	return &ntfyChannel{
		url:   strings.TrimRight(base, "/") + "/" + cfg.Topic,
		token: os.Getenv(cfg.TokenEnv),
		http:  sharedClient,
	}, nil
}

func (n *ntfyChannel) name() string { return "ntfy" }

func (n *ntfyChannel) send(ctx context.Context, msg note) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(msg.Body))
	if err != nil {
		return fmt.Errorf("notify: ntfy request: %w", err)
	}
	req.Header.Set("Title", headerSafe(msg.Title))
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	return doHTTP(n.http, req, "ntfy")
}

// ---------- Bark ----------

type barkChannel struct {
	base string
	key  string
	http *http.Client
}

func newBark(cfg config.BarkConfig) (channel, error) {
	if cfg.KeyEnv == "" {
		return nil, errors.New("notify: bark needs notify.bark.key_env")
	}
	key := os.Getenv(cfg.KeyEnv)
	if key == "" {
		return nil, fmt.Errorf("notify: bark device key: %s is not set", cfg.KeyEnv)
	}
	base := cfg.URL
	if base == "" {
		base = "https://api.day.app"
	}
	return &barkChannel{base: strings.TrimRight(base, "/"), key: key, http: sharedClient}, nil
}

func (b *barkChannel) name() string { return "bark" }

// send builds <url>/<key>/<title>/<body>; every segment is escaped, so a title
// with a slash or a space cannot change the path.
func (b *barkChannel) send(ctx context.Context, msg note) error {
	target := b.base + "/" + url.PathEscape(b.key) + "/" + url.PathEscape(msg.Title) + "/" + url.PathEscape(msg.Body)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("notify: bark request: %w", err)
	}
	return doHTTP(b.http, req, "bark")
}

// ---------- webhook ----------

type webhookChannel struct {
	url    string
	secret string
	http   *http.Client
}

// webhookPayload is the JSON a webhook receives.
type webhookPayload struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	TS    string `json:"ts"`
}

// SignatureHeader carries the HMAC-SHA256 of the request body in hex.
const SignatureHeader = "X-Ferry-Signature"

func newWebhook(cfg config.WebhookConfig) (channel, error) {
	if cfg.URL == "" {
		return nil, errors.New("notify: webhook needs notify.webhook.url")
	}
	return &webhookChannel{url: cfg.URL, secret: os.Getenv(cfg.SecretEnv), http: sharedClient}, nil
}

func (w *webhookChannel) name() string { return "webhook" }

func (w *webhookChannel) send(ctx context.Context, msg note) error {
	body, err := json.Marshal(webhookPayload{
		Kind:  msg.Kind,
		Title: msg.Title,
		Body:  msg.Body,
		TS:    msg.At.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("notify: webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.secret != "" {
		req.Header.Set(SignatureHeader, Sign(w.secret, body))
	}
	return doHTTP(w.http, req, "webhook")
}

// Sign returns the hex HMAC-SHA256 that a webhook receiver recomputes over the
// raw request body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ---------- shared HTTP ----------

func doHTTP(c *http.Client, req *http.Request, who string) error {
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("notify: %s: %w", who, err)
	}
	defer resp.Body.Close()
	// Read a little of the body so the connection can be reused, and so a
	// failure can say what the service complained about.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notify: %s: %s: %s", who, resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

// headerSafe strips the line breaks that would otherwise split a header.
func headerSafe(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
