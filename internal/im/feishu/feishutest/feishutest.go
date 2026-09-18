// Package feishutest is the fake Feishu client the tests and the end-to-end
// rig drive the adapter with. It keeps no network and no SDK behind it: a test
// pushes inbound messages in and reads the sends back out.
package feishutest

import (
	"context"
	"sync"

	"github.com/TomwaltW/ferry/internal/im/feishu"
)

// Sent is one message the adapter handed to the client.
type Sent struct {
	Kind   string // text | file
	OpenID string
	Text   string // the text, or the path of a file
}

// The two kinds a Sent can have.
const (
	KindText = "text"
	KindFile = "file"
)

// Client is a feishu.Client a test drives by hand. The zero value is not
// usable; call New.
type Client struct {
	mu sync.Mutex
	// inbox carries the messages Deliver pushes to a running Start.
	inbox chan feishu.Message
	sent  []Sent
	// startErrs are handed to successive Start calls; an exhausted list means
	// Start blocks until its context ends, which is a healthy connection.
	startErrs []error
	// sendErrs are handed to successive sends, nil meaning success.
	sendErrs []error
	pingErr  error
	starts   int
	running  chan struct{} // closed and replaced every time Start begins
}

func New() *Client {
	return &Client{inbox: make(chan feishu.Message, 16), running: make(chan struct{})}
}

// FailStart makes the next len(errs) Start calls return those errors instead
// of holding a connection open.
func (c *Client) FailStart(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startErrs = append(c.startErrs, errs...)
}

// FailSend makes the next sends return these errors in turn; a nil entry lets
// that send through.
func (c *Client) FailSend(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErrs = append(c.sendErrs, errs...)
}

// FailPing makes Ping report err.
func (c *Client) FailPing(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pingErr = err
}

// Deliver hands one message to whoever is inside Start.
func (c *Client) Deliver(m feishu.Message) { c.inbox <- m }

// Running is closed once Start has begun, so a test can wait for the
// connection before delivering anything.
func (c *Client) Running() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Starts is how many times the adapter opened the connection.
func (c *Client) Starts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts
}

// Sent returns everything the adapter has sent so far.
func (c *Client) Sent() []Sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Sent(nil), c.sent...)
}

func (c *Client) Start(ctx context.Context, onMessage func(feishu.Message)) error {
	c.mu.Lock()
	c.starts++
	var err error
	if len(c.startErrs) != 0 {
		err, c.startErrs = c.startErrs[0], c.startErrs[1:]
	}
	close(c.running)
	c.running = make(chan struct{})
	c.mu.Unlock()
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

func (c *Client) SendText(ctx context.Context, openID, text string) (string, error) {
	return c.record(Sent{Kind: KindText, OpenID: openID, Text: text})
}

func (c *Client) SendFile(ctx context.Context, openID, path string) (string, error) {
	return c.record(Sent{Kind: KindFile, OpenID: openID, Text: path})
}

// record books one send and hands back the next scripted outcome. A refused
// send is not recorded: nothing left the client.
func (c *Client) record(s Sent) (string, error) {
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
	return "msg-" + s.OpenID, nil
}

func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingErr
}

var _ feishu.Client = (*Client)(nil)
