package control

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/TomwaltW/ferry/internal/clock"
)

// tokenBytes is half the 32 hex characters spec-p2 3.4 asks for.
const tokenBytes = 16

// maxLine bounds one request, so a stray client cannot make the daemon buffer
// without end.
const maxLine = 1 << 20

// ServeOptions is what Serve needs beyond the home directory and the handler:
// the clock the keepalive of an attached stream runs on, and where to log.
// Both are optional.
type ServeOptions struct {
	Home    string
	Handler Handler
	Clock   clock.Clock
	Log     *slog.Logger
}

// Serve listens on a loopback port, publishes it in <home>/control.json with
// mode 0600 and answers control requests until ctx ends. The file is removed on
// the way out, so a stale one always means a daemon that died.
func Serve(ctx context.Context, home string, h Handler) error {
	return ServeWith(ctx, ServeOptions{Home: home, Handler: h})
}

// ServeWith is Serve with the clock and the logger spelled out, which is how
// the tests drive the keepalive of an attached stream without waiting.
func ServeWith(ctx context.Context, opts ServeOptions) error {
	home, h := opts.Home, opts.Handler
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real()
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("control: listen: %w", err)
	}
	defer ln.Close()

	token, err := newToken()
	if err != nil {
		return err
	}
	path := filepath.Join(home, SocketName)
	ep := Endpoint{Port: ln.Addr().(*net.TCPAddr).Port, Token: token, PID: os.Getpid()}
	if err := writeEndpoint(path, ep); err != nil {
		return err
	}
	defer os.Remove(path)

	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("control: accept: %w", err)
		}
		go serveConn(ctx, conn, token, h, clk, log)
	}
}

func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("control: generate a token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// writeEndpoint publishes control.json atomically, so a reader never sees half
// a file, and never with wider permissions than 0600.
func writeEndpoint(path string, ep Endpoint) error {
	data, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("control: encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("control: create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("control: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("control: publish %s: %w", path, err)
	}
	// Rename keeps the mode of the temporary file, but an existing file with
	// a wider mode would survive a crash, so it is set again explicitly.
	return os.Chmod(path, 0o600)
}

// serveConn reads one JSON object per line until the peer goes away. A request
// whose token does not match ends the connection: there is nothing to say to a
// caller that could not read control.json.
func serveConn(ctx context.Context, conn net.Conn, token string, h Handler, clk clock.Clock, log *slog.Logger) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	dec := bufio.NewScanner(conn)
	dec.Buffer(make([]byte, 0, 4096), maxLine)
	enc := json.NewEncoder(conn)
	for dec.Scan() {
		var req Request
		if err := json.Unmarshal(dec.Bytes(), &req); err != nil {
			_ = enc.Encode(Response{Error: ErrInternal})
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 {
			// Audited rather than answered: the daemon must not become an
			// oracle for guessing the token.
			log.Warn("control: token rejected", "remote", conn.RemoteAddr().String(), "op", req.Op)
			return
		}
		if req.Op == OpAttach {
			// The connection stops being request/response here and belongs to
			// the external session until one of the two sides goes away.
			serveAttach(ctx, conn, enc, dec, req, h, clk, log)
			return
		}
		if err := enc.Encode(dispatch(ctx, req, h)); err != nil {
			return
		}
	}
}

func dispatch(ctx context.Context, req Request, h Handler) Response {
	switch req.Op {
	case OpStatus:
		st, err := h.Status(ctx)
		if err != nil {
			return Response{Error: errorCode(err)}
		}
		return ok(st)
	case OpPark:
		var args ParkRequest
		if len(req.Args) != 0 {
			if err := json.Unmarshal(req.Args, &args); err != nil {
				return Response{Error: ErrInternal}
			}
		}
		resp, err := h.Park(ctx, args)
		if err != nil {
			return Response{Error: errorCode(err)}
		}
		return ok(resp)
	default:
		return Response{Error: ErrUnsupported}
	}
}

func ok(data any) Response {
	raw, err := json.Marshal(data)
	if err != nil {
		return Response{Error: ErrInternal}
	}
	return Response{OK: true, Data: raw}
}
