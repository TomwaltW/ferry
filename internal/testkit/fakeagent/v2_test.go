package fakeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The v2 mode is tested from the wire up: the harness writes the protocol by
// hand, as generic JSON, so nothing here can agree with the agent about a field
// name by sharing a Go type with it.

// v2wait is how long the harness waits for a message before giving up.
const v2wait = 10 * time.Second

// v2harness is a client speaking v2 to an in-process ServeV2 over two pipes.
type v2harness struct {
	t    *testing.T
	home string

	mu      sync.Mutex
	nextID  int64
	replies map[string]chan map[string]any

	w        io.WriteCloser
	updates  chan map[string]any
	requests chan map[string]any
}

func startV2(t *testing.T, opts V2Options) *v2harness {
	t.Helper()

	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	home := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- ServeV2(ctx, agentIn, agentOut, home, opts) }()

	h := &v2harness{
		t:        t,
		home:     home,
		replies:  map[string]chan map[string]any{},
		w:        clientOut,
		updates:  make(chan map[string]any, 256),
		requests: make(chan map[string]any, 8),
	}
	go h.read(clientIn)

	t.Cleanup(func() {
		// Closing the client's writer is the peer disconnect ServeV2 waits on.
		_ = clientOut.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("ServeV2 returned %v, want nil", err)
			}
		case <-time.After(v2wait):
			t.Error("ServeV2 did not return after the input closed")
		}
		cancel()
		_ = agentOut.Close()
	})
	return h
}

// read sorts what the agent sends into responses, notifications and the
// requests it makes of the client.
func (h *v2harness) read(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		_, hasMethod := m["method"]
		id, hasID := m["id"]
		switch {
		case hasMethod && hasID:
			h.requests <- m
		case hasMethod:
			h.updates <- m
		case hasID:
			h.mu.Lock()
			ch := h.replies[fmt.Sprint(id)]
			h.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

// call sends one request and returns the whole response, error included: a test
// that expects a failure needs to see it.
func (h *v2harness) call(method string, params map[string]any) map[string]any {
	h.t.Helper()

	h.mu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan map[string]any, 1)
	h.replies[fmt.Sprint(id)] = ch
	h.mu.Unlock()

	h.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	select {
	case m := <-ch:
		return m
	case <-time.After(v2wait):
		h.t.Fatalf("timed out waiting for the answer to %s", method)
		return nil
	}
}

// result is the result of a call that must have succeeded.
func (h *v2harness) result(method string, params map[string]any) map[string]any {
	h.t.Helper()
	m := h.call(method, params)
	if e, ok := m["error"]; ok {
		h.t.Fatalf("%s failed: %v", method, e)
	}
	res, ok := m["result"].(map[string]any)
	if !ok {
		h.t.Fatalf("%s answered %v, want a result object", method, m)
	}
	return res
}

func (h *v2harness) send(m map[string]any) {
	h.t.Helper()
	line, err := json.Marshal(m)
	if err != nil {
		h.t.Fatalf("encode %v: %v", m, err)
	}
	if _, err := h.w.Write(append(line, '\n')); err != nil {
		h.t.Fatalf("write %v: %v", m, err)
	}
}

// update waits for the next session/update and returns its update object.
func (h *v2harness) update() map[string]any {
	h.t.Helper()
	select {
	case m := <-h.updates:
		if m["method"] != "session/update" {
			h.t.Fatalf("got the notification %v, want a session/update", m["method"])
		}
		params, _ := m["params"].(map[string]any)
		update, ok := params["update"].(map[string]any)
		if !ok {
			h.t.Fatalf("the session/update %v carries no update object", m)
		}
		return update
	case <-time.After(v2wait):
		h.t.Fatal("timed out waiting for a session/update")
		return nil
	}
}

// chunksUntilIdle collects the message chunks of a turn and returns them with the
// idle state_update that ended it.
func (h *v2harness) chunksUntilIdle() (string, map[string]any) {
	h.t.Helper()
	var said strings.Builder
	for {
		u := h.update()
		switch u["sessionUpdate"] {
		case "state_update":
			if u["state"] == "idle" {
				return said.String(), u
			}
		case "agent_message_chunk":
			if u["messageId"] == nil || u["messageId"] == "" {
				h.t.Errorf("the chunk %v has no messageId, which v2 requires", u)
			}
			content, ok := u["content"].(map[string]any)
			if !ok {
				h.t.Fatalf("the chunk %v carries no content object", u)
			}
			if content["type"] != "text" {
				h.t.Errorf("the content type is %v, want text", content["type"])
			}
			said.WriteString(fmt.Sprint(content["text"]))
		}
	}
}

// TestFakeAgentV2Mode drives the v2 mode through a whole session: negotiation,
// session setup, a turn that is accepted and then ends idle, the listing, a
// resume that replays, and the prompt that is refused outright.
func TestFakeAgentV2Mode(t *testing.T) {
	h := startV2(t, V2Options{})

	// Negotiation: the agent answers version 2, advertises the session
	// capability as a whole and wants no login.
	init := h.result("initialize", map[string]any{
		"protocolVersion": 2,
		"capabilities":    map[string]any{},
		"info":            map[string]any{"name": "test", "version": "1"},
	})
	if got := init["protocolVersion"]; fmt.Sprint(got) != "2" {
		t.Errorf("protocolVersion = %v, want 2", got)
	}
	caps, ok := init["capabilities"].(map[string]any)
	if !ok || caps["session"] == nil {
		t.Errorf("capabilities = %v, want a session object", init["capabilities"])
	}
	if methods, ok := init["authMethods"].([]any); !ok || len(methods) != 0 {
		t.Errorf("authMethods = %v, want an empty list", init["authMethods"])
	}

	// Session setup: a new session comes back with its id and the config
	// options, the mode among them.
	cwd := t.TempDir()
	newSession := h.result("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	id, _ := newSession["sessionId"].(string)
	if id == "" {
		t.Fatalf("session/new answered %v, want a sessionId", newSession)
	}
	options, ok := newSession["configOptions"].([]any)
	if !ok || len(options) == 0 {
		t.Fatalf("session/new answered %v, want config options", newSession)
	}
	first, _ := options[0].(map[string]any)
	if first["configId"] != "mode" {
		t.Errorf("the first config option is %v, want the mode", first)
	}

	// A turn: the prompt is answered with an empty result, the output streams as
	// chunks, and an idle state_update ends it with a stop reason.
	accepted := h.result("session/prompt", map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": "echo hello"}},
	})
	if len(accepted) != 0 {
		t.Errorf("session/prompt answered %v, want an empty result", accepted)
	}
	if running := h.update(); running["sessionUpdate"] != "state_update" || running["state"] != "running" {
		t.Errorf("the first update is %v, want a running state_update", running)
	}
	said, idle := h.chunksUntilIdle()
	if said != "hello" {
		t.Errorf("the agent said %q, want %q", said, "hello")
	}
	if idle["stopReason"] != "end_turn" {
		t.Errorf("stopReason = %v, want end_turn", idle["stopReason"])
	}

	// The listing sees the session, titled after the first prompt.
	listed := h.result("session/list", map[string]any{"cwd": cwd})
	sessions, ok := listed["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("session/list answered %v, want the one session", listed)
	}
	info, _ := sessions[0].(map[string]any)
	if info["sessionId"] != id {
		t.Errorf("the listed session is %v, want %s", info["sessionId"], id)
	}
	if info["title"] != "echo hello" {
		t.Errorf("the listed title is %v, want the first prompt", info["title"])
	}

	// A resume that asks for the history gets it replayed before it returns.
	h.result("session/resume", map[string]any{
		"sessionId":  id,
		"cwd":        cwd,
		"replayFrom": map[string]any{"type": "start"},
	})
	replayed := map[string]int{}
	for range 2 {
		u := h.update()
		replayed[fmt.Sprint(u["sessionUpdate"])]++
	}
	if replayed["user_message_chunk"] != 1 || replayed["agent_message_chunk"] != 1 {
		t.Errorf("the replay sent %v, want one chunk of each role", replayed)
	}

	// session/close answers, and a prompt the table says must fail is refused
	// before it is ever accepted.
	h.result("session/close", map[string]any{"sessionId": id})

	failed := h.call("session/prompt", map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": "fail"}},
	})
	if failed["error"] == nil {
		t.Errorf("the fail prompt answered %v, want an error", failed)
	}

	// The negotiation is recorded where the driver tests can read it.
	if got := initializeVersions(t, h.home); len(got) != 1 || got[0] != "2" {
		t.Errorf("the initialize log holds %v, want one v2 initialize", got)
	}
}

// TestFakeAgentV2Login covers the other half of the v2 mode: an agent that wants
// a login refuses to work until it has had one.
func TestFakeAgentV2Login(t *testing.T) {
	h := startV2(t, V2Options{Auth: V2AuthTerminal})

	init := h.result("initialize", map[string]any{"protocolVersion": 2, "capabilities": map[string]any{}})
	methods, ok := init["authMethods"].([]any)
	if !ok || len(methods) != 1 {
		t.Fatalf("authMethods = %v, want the one method", init["authMethods"])
	}
	method, _ := methods[0].(map[string]any)
	if method["methodId"] != v2AuthMethodID || method["type"] != V2AuthTerminal {
		t.Errorf("the auth method is %v, want %s of type %s", method, v2AuthMethodID, V2AuthTerminal)
	}

	refused := h.call("session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	if refused["error"] == nil {
		t.Fatalf("session/new answered %v, want an authentication error", refused)
	}

	h.result("auth/login", map[string]any{"methodId": v2AuthMethodID})
	opened := h.result("session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	if opened["sessionId"] == nil {
		t.Errorf("session/new after the login answered %v, want a sessionId", opened)
	}
}

// initializeVersions reads the versions recorded in the initialize log.
func initializeVersions(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, InitializeLog))
	if err != nil {
		t.Fatalf("read the initialize log: %v", err)
	}
	return strings.Fields(string(data))
}
