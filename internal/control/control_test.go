package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// fakeHandler stands in for the core: it records what it was asked and answers
// from a table the test fills in.
type fakeHandler struct {
	mu       sync.Mutex
	parks    []ParkRequest
	park     ParkResponse
	parkErr  error
	status   StatusResponse
	statusEr error

	attaches  []AttachRequest
	attach    AttachResponse
	attachErr error
	streams   chan ServerStream
}

func (f *fakeHandler) Park(ctx context.Context, req ParkRequest) (ParkResponse, error) {
	f.mu.Lock()
	f.parks = append(f.parks, req)
	f.mu.Unlock()
	return f.park, f.parkErr
}

func (f *fakeHandler) Status(ctx context.Context) (StatusResponse, error) {
	return f.status, f.statusEr
}

func (f *fakeHandler) parkCalls() []ParkRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ParkRequest(nil), f.parks...)
}

// serve starts a daemon in a temporary home and returns that home once
// control.json is on disk.
func serve(t *testing.T, h Handler) string {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, home, h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v, want nil after the context ended", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the context was cancelled")
		}
	})

	path := filepath.Join(home, SocketName)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return home
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func readEndpoint(t *testing.T, home string) Endpoint {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, SocketName))
	if err != nil {
		t.Fatalf("read %s: %v", SocketName, err)
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		t.Fatalf("parse %s: %v", SocketName, err)
	}
	return ep
}

func TestControlStatus(t *testing.T) {
	h := &fakeHandler{status: StatusResponse{IMOnline: true, BotID: "bot-1", Sessions: 3, Running: 1}}
	home := serve(t, h)

	c, err := Dial(home)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer c.Close()

	got, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !reflect.DeepEqual(got, h.status) {
		t.Errorf("Status = %+v, want %+v", got, h.status)
	}

	// The same connection answers more than one request.
	if _, err := c.Status(context.Background()); err != nil {
		t.Errorf("the second Status failed: %v", err)
	}
}

func TestControlPark(t *testing.T) {
	h := &fakeHandler{park: ParkResponse{
		Agent:          "fake",
		AgentSessionID: "sess-1",
		Cwd:            "/tmp/demo",
		BackCommand:    []string{"ferry-fakeagent", "--resume", "sess-1"},
	}}
	home := serve(t, h)

	c, err := Dial(home)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer c.Close()

	got, err := c.Park(context.Background(), ParkRequest{Slot: 2})
	if err != nil {
		t.Fatalf("Park failed: %v", err)
	}
	if !reflect.DeepEqual(got, h.park) {
		t.Errorf("Park = %+v, want %+v", got, h.park)
	}
	if want := []ParkRequest{{Slot: 2}}; !reflect.DeepEqual(h.parkCalls(), want) {
		t.Errorf("the daemon saw %+v, want %+v", h.parkCalls(), want)
	}
}

func TestControlParkErrorsKeepTheirMeaning(t *testing.T) {
	cases := map[string]struct {
		err  error
		want error
	}{
		"no such session": {err: fsp.ErrNotFound, want: fsp.ErrNotFound},
		"cannot resume":   {err: fsp.ErrUnsupported, want: fsp.ErrUnsupported},
		"still busy":      {err: fsp.ErrBusy, want: fsp.ErrBusy},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := serve(t, &fakeHandler{parkErr: tc.err})
			c, err := Dial(home)
			if err != nil {
				t.Fatalf("Dial failed: %v", err)
			}
			defer c.Close()
			if _, err := c.Park(context.Background(), ParkRequest{}); !errors.Is(err, tc.want) {
				t.Errorf("Park = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestControlTokenRequired(t *testing.T) {
	h := &fakeHandler{status: StatusResponse{BotID: "bot-1"}}
	home := serve(t, h)
	ep := readEndpoint(t, home)

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{Token: "0000000000000000", Op: OpStatus}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// A wrong token gets no answer at all: the connection simply ends.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if scanner := bufio.NewScanner(conn); scanner.Scan() {
		t.Errorf("the daemon answered %q, want the connection closed", scanner.Text())
	}
	if n := len(h.parkCalls()); n != 0 {
		t.Errorf("the handler was reached %d times, want none", n)
	}
}

func TestControlFileMode(t *testing.T) {
	home := serve(t, &fakeHandler{})
	path := filepath.Join(home, SocketName)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %o, want 600", SocketName, mode)
		}
	}

	ep := readEndpoint(t, home)
	if ep.Port <= 0 {
		t.Errorf("port = %d, want a real one", ep.Port)
	}
	if len(ep.Token) != 32 {
		t.Errorf("token = %q, want 32 hex characters", ep.Token)
	}
	for _, r := range ep.Token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("token = %q, want lowercase hex", ep.Token)
		}
	}
	if ep.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", ep.PID, os.Getpid())
	}

	// And the listener is on the loopback interface only.
	if host, _, _ := net.SplitHostPort(net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port))); host != "127.0.0.1" {
		t.Errorf("the endpoint is reachable at %s, want 127.0.0.1", host)
	}
}

func TestControlFileGoesAwayWithTheDaemon(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, home, &fakeHandler{}) }()

	path := filepath.Join(home, SocketName)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survived the daemon, so a stale file would look like a live one", SocketName)
	}
}

func TestDialWithoutDaemon(t *testing.T) {
	t.Run("no file", func(t *testing.T) {
		if _, err := Dial(t.TempDir()); !errors.Is(err, fsp.ErrNotFound) {
			t.Errorf("Dial = %v, want fsp.ErrNotFound", err)
		}
	})

	t.Run("stale file", func(t *testing.T) {
		home := t.TempDir()
		// A port nobody listens on: the daemon died without cleaning up.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		data, _ := json.Marshal(Endpoint{Port: port, Token: "abc", PID: 1})
		if err := os.WriteFile(filepath.Join(home, SocketName), data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Dial(home); !errors.Is(err, fsp.ErrNotFound) {
			t.Errorf("Dial = %v, want fsp.ErrNotFound", err)
		}
	})
}

func TestUnknownOperation(t *testing.T) {
	home := serve(t, &fakeHandler{})
	ep := readEndpoint(t, home)

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ep.Port)))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{Token: ep.Token, Op: "restart"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	scanner := bufio.NewScanner(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("no answer: %v", scanner.Err())
	}
	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.OK || resp.Error != ErrUnsupported {
		t.Errorf("response = %+v, want an unsupported error", resp)
	}
}
