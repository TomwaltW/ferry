package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/TomwaltW/ferry/internal/ilinkwire"
)

// client is the thin iLink transport shared by the adapter and the login flow.
// It owns nothing but the bytes: callers decide what a failure means.
type client struct {
	http           *http.Client
	base           string // no trailing slash
	token          string // bot token, empty before login
	channelVersion string
}

func newClient(base, token, channelVersion string) *client {
	return &client{
		http:           &http.Client{},
		base:           strings.TrimRight(base, "/"),
		token:          token,
		channelVersion: channelVersion,
	}
}

func (c *client) baseInfo() *ilinkwire.BaseInfo {
	return &ilinkwire.BaseInfo{ChannelVersion: c.channelVersion}
}

// get issues a GET against base (the client's own base when empty) and decodes
// the JSON body into out.
func (c *client) get(ctx context.Context, base, path string, q url.Values, out any) error {
	raw, err := c.do(ctx, http.MethodGet, base, path, q, nil)
	if err != nil {
		return err
	}
	return decodeBody(raw, out)
}

// post sends body as JSON and decodes the answer into out, which may be nil. An
// answer carrying ret or errcode becomes an *apiError.
func (c *client) post(ctx context.Context, path string, body, out any) error {
	raw, err := c.do(ctx, http.MethodPost, "", path, nil, body)
	if err != nil {
		return err
	}
	var apiErr ilinkwire.APIError
	if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Failed() {
		return &apiError{APIError: apiErr, path: path}
	}
	return decodeBody(raw, out)
}

func (c *client) do(ctx context.Context, method, base, path string, q url.Values, body any) ([]byte, error) {
	if base == "" {
		base = c.base
	}
	target := strings.TrimRight(base, "/") + "/" + strings.TrimPrefix(path, "/")
	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("wechat: encode %s: %w", path, err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, fmt.Errorf("wechat: build %s: %w", path, err)
	}
	req.Header.Set(ilinkwire.HeaderAuthType, ilinkwire.AuthTypeValue)
	req.Header.Set(ilinkwire.HeaderUIN, randomUIN())
	if c.token != "" {
		req.Header.Set(ilinkwire.HeaderAuthorization, "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transportError{path: path, err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &transportError{path: path, err: err}
	}
	if resp.StatusCode/100 != 2 {
		return raw, &statusError{path: path, status: resp.StatusCode}
	}
	return raw, nil
}

func decodeBody(raw []byte, out any) error {
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("wechat: decode response: %w", err)
	}
	return nil
}

// randomUIN mirrors openilink-sdk-go's randomWechatUIN: a fresh ten digit
// number per request. The gateway only checks that the header is there.
func randomUIN() string {
	return strconv.FormatInt(1_000_000_000+rand.Int64N(9_000_000_000), 10)
}

// apiError is a well formed answer that carries a gateway error code.
type apiError struct {
	ilinkwire.APIError
	path string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("wechat: %s: ret=%d errcode=%d: %s", e.path, e.Ret, e.ErrCode, e.ErrMsg)
}

// statusError is a non 2xx HTTP answer.
type statusError struct {
	path   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("wechat: %s: http %d", e.path, e.status)
}

// transportError is a request that never produced an answer.
type transportError struct {
	path string
	err  error
}

func (e *transportError) Error() string { return fmt.Sprintf("wechat: %s: %v", e.path, e.err) }

func (e *transportError) Unwrap() error { return e.err }

// sessionExpired reports whether err is the gateway saying the bot session is
// gone and a new QR code has to be scanned.
func sessionExpired(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.SessionExpired()
}

// retryable reports whether err deserves an exponential backoff: the request
// never landed, or the gateway answered 5xx.
func retryable(err error) bool {
	var te *transportError
	if errors.As(err, &te) {
		return true
	}
	var se *statusError
	return errors.As(err, &se) && se.status/100 == 5
}

// timedOut reports whether err is a request deadline firing, which the long
// poll treats as an empty answer.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var te *transportError
	if !errors.As(err, &te) {
		return false
	}
	type timeouter interface{ Timeout() bool }
	var to timeouter
	return errors.As(te.err, &to) && to.Timeout()
}
