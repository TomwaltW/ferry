package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// dialTimeout is how long the CLI waits for a daemon that published a port but
// is not answering on it.
const dialTimeout = 2 * time.Second

// Client is one connection to the daemon's control socket.
type Client struct {
	mu    sync.Mutex
	conn  net.Conn
	enc   *json.Encoder
	dec   *bufio.Scanner
	token string
}

// Dial reads <home>/control.json and connects to the daemon. A missing file or
// a port nobody listens on both mean "no daemon", which is fsp.ErrNotFound.
func Dial(home string) (*Client, error) {
	path := filepath.Join(home, SocketName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("control: %s: %w", path, fsp.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("control: read %s: %w", path, err)
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("control: parse %s: %w", path, fsp.ErrNotFound)
	}
	if ep.Port <= 0 || ep.Token == "" {
		return nil, fmt.Errorf("control: %s is incomplete: %w", path, fsp.ErrNotFound)
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)), dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("control: connect to the daemon: %w", fsp.ErrNotFound)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	return &Client{conn: conn, enc: json.NewEncoder(conn), dec: scanner, token: ep.Token}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Park(ctx context.Context, req ParkRequest) (ParkResponse, error) {
	args, err := json.Marshal(req)
	if err != nil {
		return ParkResponse{}, fmt.Errorf("control: encode park: %w", err)
	}
	var out ParkResponse
	if err := c.call(ctx, OpPark, args, &out); err != nil {
		return ParkResponse{}, err
	}
	return out, nil
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	if err := c.call(ctx, OpStatus, nil, &out); err != nil {
		return StatusResponse{}, err
	}
	return out, nil
}

// call writes one request and reads the one response that answers it. The
// deadline of ctx, when it has one, also bounds the read.
func (c *Client) call(ctx context.Context, op string, args json.RawMessage, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer c.conn.SetDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()

	if err := c.enc.Encode(Request{Token: c.token, Op: op, Args: args}); err != nil {
		return fmt.Errorf("control: send %s: %w", op, err)
	}
	if !c.dec.Scan() {
		if err := c.dec.Err(); err != nil {
			return fmt.Errorf("control: read the answer to %s: %w", op, err)
		}
		// The daemon closes the connection on a token it does not know.
		return fmt.Errorf("control: the daemon closed the connection: %w", fsp.ErrNotFound)
	}
	var resp Response
	if err := json.Unmarshal(c.dec.Bytes(), &resp); err != nil {
		return fmt.Errorf("control: parse the answer to %s: %w", op, err)
	}
	if !resp.OK {
		return decodeError(resp.Error)
	}
	if out == nil || len(resp.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("control: parse the data of %s: %w", op, err)
	}
	return nil
}
