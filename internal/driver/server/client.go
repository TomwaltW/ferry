package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// callTimeout caps one ordinary request. The event stream is not an ordinary
// request and is opened without it.
const callTimeout = 30 * time.Second

// maxBodyBytes caps how much of a response Ferry reads. Session listings are
// the largest thing either service returns.
const maxBodyBytes = 4 << 20

// client is the HTTP side of a service driver: a loopback base URL, the
// authentication the service asked for, and any header the service told Ferry
// to repeat back.
type client struct {
	base *url.URL
	hc   *http.Client
	log  *slog.Logger

	// auth stamps the credentials onto a request. It is nil when the agent is
	// configured without a secret.
	auth func(*http.Request)

	mu sync.Mutex
	// extra are headers the service handed out at session creation and expects
	// to see again, such as X-Qwen-Client-Id.
	extra map[string]string
}

// newClient checks the URL and builds the HTTP client for it. The timeout lives
// on each request's context rather than on the client, so that the event stream
// can stay open indefinitely.
func newClient(rawURL string, log *slog.Logger) (*client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("server: read the service address %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("server: the service address %q must be http or https", rawURL)
	}
	if err := checkLoopback(u); err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/")

	return &client{
		base:  u,
		hc:    &http.Client{},
		log:   log,
		extra: map[string]string{},
	}, nil
}

// checkLoopback refuses any address that is not this machine. A driver that
// speaks to "a local service" must not be talkable into reaching across the
// network, because the session it opens has the user's workspace in it.
func checkLoopback(u *url.URL) error {
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("server: %q is not a loopback address; only 127.0.0.1, ::1 and localhost are allowed", u.Host)
}

// setHeader records a header to repeat on every later request. An empty value
// clears it, so a service that stops sending one stops receiving it.
func (c *client) setHeader(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value == "" {
		delete(c.extra, name)
		return
	}
	c.extra[name] = value
}

func (c *client) header(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.extra[name]
}

// httpError is a response the service refused. Backends look at Status and Body
// to tell one refusal from another, such as Qwen's workspace mismatch.
type httpError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *httpError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200]
	}
	if body == "" {
		return fmt.Sprintf("server: %s %s: %s", e.Method, e.Path, http.StatusText(e.Status))
	}
	return fmt.Sprintf("server: %s %s: %d %s", e.Method, e.Path, e.Status, body)
}

// do sends one request and decodes the response into out, which may be nil.
// The response headers come back so that a backend can pick up an identity the
// service handed out.
func (c *client) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("server: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.Header, fmt.Errorf("server: read the answer to %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Header, &httpError{Method: method, Path: path, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("server: read the answer to %s %s: %w", method, path, err)
		}
	}
	return resp.Header, nil
}

// stream opens a server-sent event stream. It has no timeout of its own: the
// caller's context is what ends it.
func (c *client) stream(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("server: open %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		return nil, &httpError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Body: string(data)}
	}
	return resp.Body, nil
}

func (c *client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("server: encode the body of %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("server: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.mu.Lock()
	for name, value := range c.extra {
		req.Header.Set(name, value)
	}
	c.mu.Unlock()

	if c.auth != nil {
		c.auth(req)
	}
	return req, nil
}
