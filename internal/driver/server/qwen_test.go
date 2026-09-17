package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// ---------- a stand-in for `qwen serve` ----------

type qwReplyRecord struct {
	session  string
	request  string
	option   string
	clientID string
}

type qwCall struct {
	method   string
	path     string
	clientID string
}

// fakeQwen serves the routes of spec-p2 3.5. Its event frames are written from
// the tables in eventmap.go, so it tests the driver against itself; what a real
// daemon sends is what spec-p2 section 0 could not check.
type fakeQwen struct {
	srv *httptest.Server

	mu sync.Mutex
	// token is the bearer token the daemon demands, empty for none.
	token string
	// handOut is the client ID the daemon gives out on create and load; empty
	// means it gives none, and then Ferry must send none either.
	handOut string
	// inBody puts the client ID in the payload rather than in the header.
	inBody bool
	// caps is what GET /capabilities lists; capsFail makes it refuse.
	caps     []string
	capsFail bool
	// workspaces are the directories registered with --workspace.
	workspaces map[string]bool

	next     int
	sessions map[string]bool
	running  map[string]bool
	streams  map[string][]*testStream
	connects map[string]int

	calls    []qwCall
	prompts  []string
	cancels  []string
	replies  []qwReplyRecord
	refusals int
}

func newFakeQwen(t *testing.T) *fakeQwen {
	t.Helper()
	f := &fakeQwen{
		caps:       []string{"load"},
		workspaces: map[string]bool{"/work/repo": true},
		sessions:   map[string]bool{},
		running:    map[string]bool{},
		streams:    map[string][]*testStream{},
		connects:   map[string]int{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", f.guard(f.health))
	mux.HandleFunc("GET /capabilities", f.guard(f.capabilities))
	mux.HandleFunc("POST /session", f.guard(f.create))
	mux.HandleFunc("POST /session/{id}/load", f.guard(f.load))
	mux.HandleFunc("POST /session/{id}/resume", f.guard(f.load))
	mux.HandleFunc("POST /session/{id}/prompt", f.guard(f.prompt))
	mux.HandleFunc("POST /session/{id}/cancel", f.guard(f.cancel))
	mux.HandleFunc("POST /session/{id}/permission/{rid}", f.guard(f.permission))
	mux.HandleFunc("GET /session/{id}/events", f.guard(f.events))
	mux.HandleFunc("GET /session/{id}/status", f.guard(f.status))

	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.dropStreams()
		f.srv.Close()
	})
	return f
}

func (f *fakeQwen) spec() ports.SessionSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ports.SessionSpec{
		Agent:  "qwen-live",
		Cwd:    "/work/repo",
		Server: &ports.ServerSpec{Kind: "qwen", URL: f.srv.URL, Secret: f.token},
	}
}

func (f *fakeQwen) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		want := f.token
		f.calls = append(f.calls, qwCall{
			method:   r.Method,
			path:     r.URL.Path,
			clientID: r.Header.Get(qwenClientIDHeader),
		})
		f.mu.Unlock()

		if want != "" && r.Header.Get("Authorization") != "Bearer "+want {
			f.mu.Lock()
			f.refusals++
			f.mu.Unlock()
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (f *fakeQwen) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok"})
}

func (f *fakeQwen) capabilities(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	fail, caps := f.capsFail, slices.Clone(f.caps)
	f.mu.Unlock()

	if fail {
		http.Error(w, "not implemented", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"capabilities": caps})
}

func (f *fakeQwen) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cwd string `json:"cwd"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	registered := f.workspaces[body.Cwd]
	f.mu.Unlock()

	if !registered {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":%q,"message":"the workspace is not registered"}`, qwenWorkspaceMismatch)
		return
	}

	f.mu.Lock()
	f.next++
	id := fmt.Sprintf("sess-%d", f.next)
	f.sessions[id] = true
	f.mu.Unlock()

	f.answerSession(w, id)
}

func (f *fakeQwen) load(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	f.mu.Lock()
	known := f.sessions[id]
	f.mu.Unlock()

	if !known {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	f.answerSession(w, id)
}

// answerSession hands back a session, with the client ID wherever the fake was
// told to put it.
func (f *fakeQwen) answerSession(w http.ResponseWriter, id string) {
	f.mu.Lock()
	handOut, inBody := f.handOut, f.inBody
	f.mu.Unlock()

	out := map[string]any{"sessionId": id}
	switch {
	case handOut == "":
	case inBody:
		out["clientId"] = handOut
	default:
		w.Header().Set(qwenClientIDHeader, handOut)
	}
	writeJSON(w, out)
}

func (f *fakeQwen) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.prompts = append(f.prompts, body.Prompt)
	f.running[r.PathValue("id")] = true
	f.mu.Unlock()

	writeJSON(w, map[string]any{"accepted": true})
}

func (f *fakeQwen) cancel(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.cancels = append(f.cancels, r.PathValue("id"))
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (f *fakeQwen) permission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OptionID string `json:"optionId"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.replies = append(f.replies, qwReplyRecord{
		session:  r.PathValue("id"),
		request:  r.PathValue("rid"),
		option:   body.OptionID,
		clientID: r.Header.Get(qwenClientIDHeader),
	})
	f.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true})
}

func (f *fakeQwen) status(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	f.mu.Lock()
	known, running := f.sessions[id], f.running[id]
	f.mu.Unlock()

	if !known {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	state := "idle"
	if running {
		state = "running"
	}
	writeJSON(w, map[string]any{"state": state})
}

func (f *fakeQwen) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	f.mu.Lock()
	if !f.sessions[id] {
		f.mu.Unlock()
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	s := &testStream{ch: make(chan string, 16), done: make(chan struct{})}
	f.streams[id] = append(f.streams[id], s)
	f.connects[id]++
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	// The daemon sends no opening frame, so the headers have to be pushed out
	// on their own or the client waits for them for ever.
	flusher.Flush()

	for {
		select {
		case msg := <-s.ch:
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-s.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// emit sends one named event down a session's stream, the way the daemon's
// event schema spells it.
func (f *fakeQwen) emit(sessionID, name string, payload map[string]any) {
	if payload["sessionId"] == nil {
		payload["sessionId"] = sessionID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	frame := fmt.Sprintf("event: %s\ndata: %s\n\n", name, encoded)

	f.mu.Lock()
	streams := slices.Clone(f.streams[sessionID])
	f.mu.Unlock()

	for _, s := range streams {
		select {
		case s.ch <- frame:
		case <-s.done:
		}
	}
}

func (f *fakeQwen) dropStreams() {
	f.mu.Lock()
	all := f.streams
	f.streams = map[string][]*testStream{}
	f.mu.Unlock()

	for _, streams := range all {
		for _, s := range streams {
			close(s.done)
		}
	}
}

func (f *fakeQwen) waitConnects(t *testing.T, sessionID string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		got := f.connects[sessionID]
		f.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d event streams connected for %s, want %d", got, sessionID, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fakeQwen) snapshot(read func(*fakeQwen)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	read(f)
}

// paths is every route the fake was asked for, in order.
func (f *fakeQwen) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.path)
	}
	return out
}

func startQwen(t *testing.T, f *fakeQwen, ap ports.Approver, clk clock.Clock) ports.AgentSession {
	t.Helper()
	d := newDriver(testLogger(), clk)
	sess, err := d.Start(context.Background(), f.spec(), ap)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	f.waitConnects(t, sess.AgentSessionID(), 1)
	return sess
}

// ---------- tests ----------

func TestQwenStartAndPrompt(t *testing.T) {
	f := newFakeQwen(t)
	sess := startQwen(t, f, nil, clock.NewFake(testStart))

	if got := sess.AgentSessionID(); got != "sess-1" {
		t.Errorf("AgentSessionID = %q, want sess-1", got)
	}
	if sess.Caps().List {
		t.Error("Caps says qwen can list sessions; spec-p2 3.5 gives it no such route")
	}

	if err := sess.Prompt(context.Background(), "explain this file"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	f.snapshot(func(f *fakeQwen) {
		if !slices.Equal(f.prompts, []string{"explain this file"}) {
			t.Errorf("the daemon was sent %q, want [explain this file]", f.prompts)
		}
	})

	if err := sess.Prompt(context.Background(), "and again"); err != fsp.ErrBusy {
		t.Errorf("a second prompt gave %v, want fsp.ErrBusy", err)
	}

	f.emit("sess-1", "turn_complete", map[string]any{"stopReason": "end_turn", "promptId": "p1"})
	if e := waitEvent(t, sess.Events(), "the turn end", ofKind(fsp.EvTurnEnd)); e.StopReason != "end_turn" {
		t.Errorf("the turn ended with %q, want end_turn", e.StopReason)
	}
	if err := sess.Prompt(context.Background(), "next"); err != nil {
		t.Errorf("the session stayed busy after the turn ended: %v", err)
	}

	if err := sess.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	f.snapshot(func(f *fakeQwen) {
		if !slices.Equal(f.cancels, []string{"sess-1"}) {
			t.Errorf("the daemon was told to cancel %q, want [sess-1]", f.cancels)
		}
	})

	// Listing is refused rather than guessed at.
	d := newDriver(testLogger(), clock.NewFake(testStart))
	if _, err := d.List(context.Background(), f.spec()); !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("List gave %v, want fsp.ErrUnsupported", err)
	}
}

func TestQwenEvents(t *testing.T) {
	f := newFakeQwen(t)
	sess := startQwen(t, f, nil, clock.NewFake(testStart))
	events := sess.Events()

	// Content arrives as a bare string in one place and as a block in another;
	// both are read.
	f.emit("sess-1", "session_update", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": "hello",
	})
	if e := waitEvent(t, events, "the first chunk", ofKind(fsp.EvChunk)); e.Text != "hello" {
		t.Errorf("the first chunk was %q, want hello", e.Text)
	}

	f.emit("sess-1", "session_update", map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": " world"},
	})
	if e := waitEvent(t, events, "the second chunk", ofKind(fsp.EvChunk)); e.Text != " world" {
		t.Errorf("the second chunk was %q, want ' world'", e.Text)
	}

	// Another session's events never reach this one, even though they cannot
	// normally share a stream.
	f.emit("sess-1", "session_update", map[string]any{
		"sessionId": "sess-9", "sessionUpdate": "agent_message_chunk", "content": "not yours",
	})
	f.emit("sess-1", "session_update", map[string]any{
		"sessionUpdate": "agent_thought_chunk", "content": "weighing it up",
	})
	e := waitEvent(t, events, "the thought", func(e fsp.Event) bool { return e.Kind != fsp.EvChunk })
	if e.Kind != fsp.EvThought || e.Text != "weighing it up" {
		t.Fatalf("got %+v, want this session's thought only", e)
	}

	f.emit("sess-1", "session_update", map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "tc-1",
		"title": "read main.go", "kind": "read", "status": "pending",
	})
	e = waitEvent(t, events, "the tool call", ofKind(fsp.EvTool))
	if e.Tool == nil || e.Tool.CallID != "tc-1" || e.Tool.Kind != "read" {
		t.Errorf("the tool call came through as %+v, want tc-1/read", e.Tool)
	}

	f.emit("sess-1", "session_update", map[string]any{
		"sessionUpdate": "plan",
		"entries": []map[string]any{
			{"content": "read the file", "status": "completed"},
			{"content": "write the test", "status": "pending"},
		},
	})
	e = waitEvent(t, events, "the plan", ofKind(fsp.EvPlan))
	if len(e.Plan) != 2 || e.Plan[1].Title != "write the test" || e.Plan[1].Status != "pending" {
		t.Errorf("the plan came through as %+v, want two steps", e.Plan)
	}

	// A failed turn is still a turn that ended.
	f.emit("sess-1", "turn_error", map[string]any{"message": "the model refused", "code": "refusal"})
	e = waitEvent(t, events, "the failed turn", ofKind(fsp.EvTurnEnd))
	if e.StopReason != "error" || e.Err != "the model refused" {
		t.Errorf("the failed turn came through as %+v, want error", e)
	}

	// Losing the session itself is the one thing that is an error event.
	f.emit("sess-1", "session_closed", map[string]any{"reason": "the daemon is stopping"})
	e = waitEvent(t, events, "the closed session", ofKind(fsp.EvError))
	if e.Err == "" {
		t.Error("the error event says nothing about why")
	}
	if err := sess.Prompt(context.Background(), "hello"); err == nil {
		t.Error("a closed session accepted a prompt")
	}
}

func TestQwenPermission(t *testing.T) {
	request := func(id string) map[string]any {
		return map[string]any{
			"requestId": id,
			"toolCall": map[string]any{
				"name": "run_shell_command", "title": "run the tests", "kind": "execute",
				"rawInput":  map[string]any{"command": "go test ./..."},
				"locations": []map[string]any{{"path": "/work/repo"}},
			},
			"options": []map[string]any{
				{"optionId": "proceed_once", "name": "Yes", "kind": "allow_once"},
				{"optionId": "proceed_always", "name": "Yes, always", "kind": "allow_always"},
				{"optionId": "cancel", "name": "No", "kind": "reject_once"},
			},
			"originatorClientId": "cli-terminal",
		}
	}

	t.Run("answered in ferry", func(t *testing.T) {
		f := newFakeQwen(t)
		ap := newApprover(fsp.Verdict{Decision: fsp.DecisionAllowOnce, OptionID: "proceed_once"})
		sess := startQwen(t, f, ap, clock.NewFake(testStart))

		f.emit(sess.AgentSessionID(), "permission_request", request("req-1"))

		req := ap.waitRequest(t)
		if req.ToolName != "run_shell_command" || req.ToolKind != "execute" {
			t.Errorf("the request named %q/%q, want run_shell_command/execute", req.ToolName, req.ToolKind)
		}
		if !slices.Equal(req.Paths, []string{"/work/repo"}) {
			t.Errorf("the request touched %q, want /work/repo", req.Paths)
		}
		if req.InputPreview == "" {
			t.Error("the request shows no input at all")
		}
		want := []fsp.ApprovalOption{
			{ID: "proceed_once", Kind: fsp.OptAllowOnce, Label: "Yes"},
			{ID: "proceed_always", Kind: fsp.OptAllowAlways, Label: "Yes, always"},
			{ID: "cancel", Kind: fsp.OptRejectOnce, Label: "No"},
		}
		if !slices.Equal(req.Options, want) {
			t.Errorf("the user was offered %+v, want %+v", req.Options, want)
		}

		waitReplies(t, f, 1)
		f.snapshot(func(f *fakeQwen) {
			got := f.replies[0]
			if got.session != "sess-1" || got.request != "req-1" || got.option != "proceed_once" {
				t.Errorf("the daemon was answered %+v, want req-1/proceed_once", got)
			}
		})
	})

	t.Run("answered in the terminal", func(t *testing.T) {
		f := newFakeQwen(t)
		ap := newApprover(fsp.Verdict{Decision: fsp.DecisionAllowOnce, OptionID: "proceed_once"})
		ap.release = make(chan struct{})
		sess := startQwen(t, f, ap, clock.NewFake(testStart))

		// A shared session is open in a terminal too, and whoever is sitting
		// there answers first.
		f.emit(sess.AgentSessionID(), "permission_request", request("req-2"))
		ap.waitRequest(t)
		f.emit(sess.AgentSessionID(), "permission_resolved", map[string]any{
			"requestId": "req-2", "outcome": "allowed",
		})

		// Frames are handled in order, so a chunk sent after the resolution
		// arriving means the resolution has been dealt with.
		f.emit(sess.AgentSessionID(), "session_update", map[string]any{
			"sessionUpdate": "agent_message_chunk", "content": "carrying on",
		})
		waitEvent(t, sess.Events(), "a chunk after the resolution", ofKind(fsp.EvChunk))

		// Releasing the approver now must not send a second answer.
		close(ap.release)
		time.Sleep(50 * time.Millisecond)
		f.snapshot(func(f *fakeQwen) {
			if len(f.replies) != 0 {
				t.Errorf("the daemon was answered %+v, want nothing after the terminal answered", f.replies)
			}
		})
	})
}

func waitReplies(t *testing.T, f *fakeQwen, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got int
		f.snapshot(func(f *fakeQwen) { got = len(f.replies) })
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon was answered %d times, want %d", got, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestQwenLoadOrResume(t *testing.T) {
	cases := []struct {
		name string
		caps []string
		fail bool
		want string
	}{
		{name: "load", caps: []string{"load"}, want: "/session/sess-1/load"},
		{name: "resume only", caps: []string{"resume"}, want: "/session/sess-1/resume"},
		{name: "both", caps: []string{"load", "resume"}, want: "/session/sess-1/load"},
		{name: "no capabilities route", fail: true, want: "/session/sess-1/load"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeQwen(t)
			f.snapshot(func(f *fakeQwen) {
				f.caps, f.capsFail = tc.caps, tc.fail
				f.sessions["sess-1"] = true
			})

			d := newDriver(testLogger(), clock.NewFake(testStart))
			sess, err := d.Resume(context.Background(), f.spec(), "sess-1", nil)
			if err != nil {
				t.Fatalf("Resume failed: %v", err)
			}
			defer sess.Close()

			if !slices.Contains(f.paths(), tc.want) {
				t.Errorf("the daemon was asked for %q, want %s among them", f.paths(), tc.want)
			}
			if got := sess.AgentSessionID(); got != "sess-1" {
				t.Errorf("AgentSessionID = %q, want sess-1", got)
			}
		})
	}

	// A session the daemon does not have is reported as missing, not opened.
	f := newFakeQwen(t)
	d := newDriver(testLogger(), clock.NewFake(testStart))
	if _, err := d.Resume(context.Background(), f.spec(), "sess-gone", nil); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("resuming a session the daemon lost gave %v, want fsp.ErrNotFound", err)
	}
}

func TestQwenWorkspaceMismatch(t *testing.T) {
	f := newFakeQwen(t)
	d := newDriver(testLogger(), clock.NewFake(testStart))

	spec := f.spec()
	spec.Cwd = "/somewhere/else"

	_, err := d.Start(context.Background(), spec, nil)
	if !errors.Is(err, fsp.ErrWorkspaceDenied) {
		t.Fatalf("starting in an unregistered directory gave %v, want fsp.ErrWorkspaceDenied", err)
	}

	// The registered directory still works, so the refusal is about the
	// directory and not about the daemon.
	if _, err := d.Start(context.Background(), f.spec(), nil); err != nil {
		t.Fatalf("Start in the registered directory failed: %v", err)
	}
}

func TestQwenBearer(t *testing.T) {
	f := newFakeQwen(t)
	f.snapshot(func(f *fakeQwen) {
		f.token = "tok-abc"
		f.handOut = "cli-7"
	})

	d := newDriver(testLogger(), clock.NewFake(testStart))
	sess, err := d.Start(context.Background(), f.spec(), nil)
	if err != nil {
		t.Fatalf("Start with the token failed: %v", err)
	}
	defer sess.Close()

	blind := f.spec()
	blind.Server.Secret = ""
	if _, err := d.Start(context.Background(), blind, nil); err == nil {
		t.Fatal("Start without the token succeeded, want it refused")
	}
	f.snapshot(func(f *fakeQwen) {
		if f.refusals == 0 {
			t.Error("the daemon never refused a request, so the token was not checked")
		}
	})

	// The identity the daemon handed out comes back on every later request,
	// and on none of the ones before it.
	if err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	f.snapshot(func(f *fakeQwen) {
		for _, c := range f.calls {
			switch c.path {
			case "/session":
				if c.clientID != "" {
					t.Errorf("creating the session already carried %q", c.clientID)
				}
			case "/session/sess-1/prompt", "/session/sess-1/events":
				if c.clientID != "cli-7" {
					t.Errorf("%s carried %q, want cli-7", c.path, c.clientID)
				}
			}
		}
	})
}

func TestQwenWithoutClientID(t *testing.T) {
	f := newFakeQwen(t)
	sess := startQwen(t, f, nil, clock.NewFake(testStart))

	if err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	f.snapshot(func(f *fakeQwen) {
		for _, c := range f.calls {
			if c.clientID != "" {
				t.Errorf("%s carried %q although the daemon handed out no client ID", c.path, c.clientID)
			}
		}
	})
}

func TestQwenClientIDFromBody(t *testing.T) {
	f := newFakeQwen(t)
	f.snapshot(func(f *fakeQwen) {
		f.handOut, f.inBody = "cli-body", true
	})
	sess := startQwen(t, f, nil, clock.NewFake(testStart))

	if err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	f.snapshot(func(f *fakeQwen) {
		for _, c := range f.calls {
			if c.path == "/session/sess-1/prompt" && c.clientID != "cli-body" {
				t.Errorf("the prompt carried %q, want cli-body", c.clientID)
			}
		}
	})
}
