package fakeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/ids"
)

// The v2 mode of the fake agent. It speaks version 2 of the Agent Client
// Protocol over newline delimited JSON, written out here rather than taken from
// the driver's own types: a double that shared them could not catch a field
// spelled wrong on either side.
//
// The scripted behaviours are the ones of the v1 table in spec-p0 3.5, so the
// same prompts produce the same output, only over the v2 shapes.

// V2Version is the protocol version this mode answers initialize with.
const V2Version = 2

// The v2 auth method types. A terminal method cannot be completed over the
// protocol, so a client that finds only those has to give up and say so.
const (
	V2AuthAgent    = "agent"
	V2AuthTerminal = "terminal"
)

// v2AuthMethodID is the id of the single method this agent advertises when it is
// asked to want a login.
const v2AuthMethodID = "fake-login"

// v2AuthRequiredCode is the reserved error the agent answers with while it is
// still waiting to be logged in.
const v2AuthRequiredCode = -32000

// V2Options configures the v2 mode.
type V2Options struct {
	// Auth, when set to V2AuthAgent or V2AuthTerminal, makes the agent
	// advertise one authentication method of that type and refuse to open a
	// session until auth/login has succeeded.
	Auth string
}

// ServeV2 runs the fake agent in v2 mode until in is closed or ctx ends. home
// is where sessions are persisted, shared with the v1 mode so a session opened
// under one can be listed under the other.
func ServeV2(ctx context.Context, in io.Reader, out io.Writer, home string, opts V2Options) error {
	if home == "" {
		return errors.New("fakeagent: home must not be empty")
	}
	st, err := newStore(home)
	if err != nil {
		return err
	}

	a := &v2Agent{
		clk:   clock.Real(),
		store: st,
		opts:  opts,
		conn:  &v2Conn{w: out, pending: map[int64]chan v2Reply{}},
		turns: map[string]context.CancelFunc{},
	}
	a.serve(ctx, in)
	return nil
}

// v2Msg is one line of the protocol: request, response or notification.
type v2Msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *v2Err          `json:"error,omitempty"`
}

type v2Err struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *v2Err) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

type v2Reply struct {
	result json.RawMessage
	err    *v2Err
}

// v2Conn is the agent's half of the connection: one writer everybody shares and
// the replies to the requests the agent itself sent.
type v2Conn struct {
	mu      sync.Mutex
	w       io.Writer
	nextID  int64
	pending map[int64]chan v2Reply
}

func (c *v2Conn) write(m v2Msg) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(line, '\n'))
	return err
}

func (c *v2Conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(v2Msg{JSONRPC: "2.0", Method: method, Params: raw})
}

// call sends a request to the client and waits for its answer.
func (c *v2Conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan v2Reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	idRaw, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	if err := c.write(v2Msg{JSONRPC: "2.0", ID: idRaw, Method: method, Params: raw}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	}
}

func (c *v2Conn) deliver(m v2Msg) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- v2Reply{result: m.Result, err: m.Error}:
	default:
	}
}

// v2Agent is the agent side of a v2 connection.
type v2Agent struct {
	clk   clock.Clock
	store *store
	opts  V2Options
	conn  *v2Conn

	mu       sync.Mutex
	loggedIn bool
	// turns holds the cancel function of the turn running in each session, so
	// that session/cancel can end it.
	turns map[string]context.CancelFunc

	// wg counts the turns still running, so serve can let them finish.
	wg sync.WaitGroup
}

func (a *v2Agent) now() string { return a.clk.Now().UTC().Format(time.RFC3339Nano) }

// serve reads the client's messages until the stream ends, then lets the turns
// still in flight finish so their last updates are written.
func (a *v2Agent) serve(ctx context.Context, in io.Reader) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m v2Msg
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null":
			a.request(ctx, m)
		case m.Method != "":
			a.notification(m)
		default:
			a.conn.deliver(m)
		}
	}
	a.wg.Wait()
}

// request answers one call from the client. Everything here is quick except
// session/prompt, which answers as soon as it has taken the input and leaves the
// turn running behind it.
func (a *v2Agent) request(ctx context.Context, m v2Msg) {
	result, rpcErr := a.dispatch(ctx, m.Method, m.Params)
	if rpcErr != nil {
		_ = a.conn.write(v2Msg{JSONRPC: "2.0", ID: m.ID, Error: rpcErr})
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		_ = a.conn.write(v2Msg{JSONRPC: "2.0", ID: m.ID, Error: &v2Err{Code: -32603, Message: err.Error()}})
		return
	}
	_ = a.conn.write(v2Msg{JSONRPC: "2.0", ID: m.ID, Result: raw})
}

func (a *v2Agent) notification(m v2Msg) {
	if m.Method != "session/cancel" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return
	}
	a.mu.Lock()
	cancel := a.turns[p.SessionID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *v2Agent) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *v2Err) {
	switch method {
	case "initialize":
		return a.initialize(params)
	case "auth/login":
		return a.authLogin(params)
	case "auth/logout":
		a.mu.Lock()
		a.loggedIn = false
		a.mu.Unlock()
		return map[string]any{}, nil
	case "session/new":
		return a.newSession(params)
	case "session/resume":
		return a.resumeSession(ctx, params)
	case "session/list":
		return a.listSessions(params)
	case "session/close":
		return map[string]any{}, nil
	case "session/prompt":
		return a.prompt(ctx, params)
	case "session/set_config_option":
		return map[string]any{"configOptions": v2ConfigOptions()}, nil
	default:
		return nil, &v2Err{Code: -32601, Message: "method not found: " + method}
	}
}

func (a *v2Agent) initialize(params json.RawMessage) (any, *v2Err) {
	var p struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable initialize: " + err.Error()}
	}
	recordInitialize(a.store, p.ProtocolVersion)

	// This agent speaks version 2 and nothing else, so a client asking for
	// anything else is answered with the latest version there is.
	resp := map[string]any{
		"protocolVersion": V2Version,
		"capabilities": map[string]any{
			"session": map[string]any{
				"prompt": map[string]any{},
				"mcp":    map[string]any{},
			},
		},
		"info":        map[string]any{"name": "ferry-fakeagent", "version": "p3"},
		"authMethods": a.authMethods(),
	}
	return resp, nil
}

// authMethods is what initialize advertises: nothing at all unless the agent was
// started with a login to want.
func (a *v2Agent) authMethods() []map[string]any {
	if a.opts.Auth == "" {
		return []map[string]any{}
	}
	return []map[string]any{{
		"methodId":    v2AuthMethodID,
		"name":        "Fake login",
		"type":        a.opts.Auth,
		"description": "the login the fake agent pretends to need",
	}}
}

func (a *v2Agent) authLogin(params json.RawMessage) (any, *v2Err) {
	var p struct {
		MethodID string `json:"methodId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable auth/login: " + err.Error()}
	}
	if p.MethodID != v2AuthMethodID {
		return nil, &v2Err{Code: -32602, Message: "no such auth method: " + p.MethodID}
	}
	a.mu.Lock()
	a.loggedIn = true
	a.mu.Unlock()
	return map[string]any{}, nil
}

// needsLogin reports the authentication error while a login the agent asked for
// has not happened.
func (a *v2Agent) needsLogin() *v2Err {
	if a.opts.Auth == "" {
		return nil
	}
	a.mu.Lock()
	in := a.loggedIn
	a.mu.Unlock()
	if in {
		return nil
	}
	return &v2Err{Code: v2AuthRequiredCode, Message: "Authentication required"}
}

func (a *v2Agent) newSession(params json.RawMessage) (any, *v2Err) {
	if e := a.needsLogin(); e != nil {
		return nil, e
	}
	var p struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable session/new: " + err.Error()}
	}

	rec := record{ID: ids.New("sess"), Cwd: p.Cwd, UpdatedAt: a.now(), History: []turn{}}
	if err := a.store.save(rec); err != nil {
		return nil, &v2Err{Code: -32603, Message: err.Error()}
	}
	return map[string]any{"sessionId": rec.ID, "configOptions": v2ConfigOptions()}, nil
}

// v2ConfigOptions is the one option this agent offers, which is also how the
// session mode is set in v2.
func v2ConfigOptions() []map[string]any {
	return []map[string]any{{
		"configId":     "mode",
		"name":         "Session Mode",
		"category":     "mode",
		"type":         "select",
		"currentValue": "ask",
		"options": []map[string]any{
			{"value": "ask", "name": "Ask"},
			{"value": "code", "name": "Code"},
		},
	}}
}

// resumeSession reattaches to a stored session, replaying its history only when
// the client asked for it.
func (a *v2Agent) resumeSession(ctx context.Context, params json.RawMessage) (any, *v2Err) {
	if e := a.needsLogin(); e != nil {
		return nil, e
	}
	var p struct {
		SessionID  string `json:"sessionId"`
		ReplayFrom *struct {
			Type string `json:"type"`
		} `json:"replayFrom"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable session/resume: " + err.Error()}
	}

	rec, err := a.store.load(p.SessionID)
	if err != nil {
		if errors.Is(err, errNoSession) {
			return nil, &v2Err{Code: -32602, Message: "unknown session: " + p.SessionID}
		}
		return nil, &v2Err{Code: -32603, Message: err.Error()}
	}

	if p.ReplayFrom != nil {
		for _, t := range rec.History {
			kind := "agent_message_chunk"
			if t.Role == roleUser {
				kind = "user_message_chunk"
			}
			a.sendChunk(ctx, p.SessionID, kind, t.Text)
		}
	}
	return map[string]any{"configOptions": v2ConfigOptions()}, nil
}

func (a *v2Agent) listSessions(params json.RawMessage) (any, *v2Err) {
	if e := a.needsLogin(); e != nil {
		return nil, e
	}
	var p struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable session/list: " + err.Error()}
	}

	recs, err := a.store.list(p.Cwd)
	if err != nil {
		return nil, &v2Err{Code: -32603, Message: err.Error()}
	}

	sessions := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		info := map[string]any{"sessionId": r.ID, "cwd": r.Cwd}
		if r.Title != "" {
			info["title"] = r.Title
		}
		if r.UpdatedAt != "" {
			info["updatedAt"] = r.UpdatedAt
		}
		sessions = append(sessions, info)
	}
	return map[string]any{"sessions": sessions}, nil
}

// prompt takes the input and hands the work to a goroutine. The one prompt that
// is refused outright is "fail", which is what the v1 table says it does.
func (a *v2Agent) prompt(ctx context.Context, params json.RawMessage) (any, *v2Err) {
	if e := a.needsLogin(); e != nil {
		return nil, e
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &v2Err{Code: -32602, Message: "unusable session/prompt: " + err.Error()}
	}

	var text strings.Builder
	for _, b := range p.Prompt {
		text.WriteString(b.Text)
	}

	if err := a.appendTurn(p.SessionID, turn{Role: roleUser, Text: text.String()}); err != nil {
		if errors.Is(err, errNoSession) {
			return nil, &v2Err{Code: -32602, Message: "unknown session: " + p.SessionID}
		}
		return nil, &v2Err{Code: -32603, Message: err.Error()}
	}

	if verb, _, _ := strings.Cut(text.String(), " "); verb == "fail" {
		return nil, &v2Err{Code: -32603, Message: "fake agent was asked to fail"}
	}

	turnCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.turns[p.SessionID] = cancel
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		a.runTurn(turnCtx, p.SessionID, text.String())
		a.mu.Lock()
		delete(a.turns, p.SessionID)
		a.mu.Unlock()
	}()

	// An accepted prompt is answered with an empty result; how it went arrives
	// later, as the idle state_update.
	return map[string]any{}, nil
}

// appendTurn adds one entry to a session's history, setting the title from the
// first user turn.
func (a *v2Agent) appendTurn(id string, t turn) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec, err := a.store.load(id)
	if err != nil {
		return err
	}
	if rec.Title == "" && t.Role == roleUser {
		rec.Title = truncateRunes(t.Text, titleRunes)
	}
	rec.History = append(rec.History, t)
	rec.UpdatedAt = a.now()
	return a.store.save(rec)
}
