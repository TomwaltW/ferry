package server

import (
	"context"
	"encoding/json"
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

// ---------- a stand-in for `opencode serve` ----------

// testStream is one open event-stream connection held by a fake service.
type testStream struct {
	ch   chan string
	done chan struct{}
}

type ocReplyRecord struct {
	session  string
	request  string
	response string
	remember bool
}

type ocSessionState struct {
	id      string
	title   string
	dir     string
	updated int64
	busy    bool
}

// fakeOpenCode serves the routes of spec-p2 3.5 and nothing else. Its event
// payloads are written from the tables in eventmap.go, which is exactly why a
// green run says the driver agrees with itself rather than with OpenCode.
type fakeOpenCode struct {
	srv *httptest.Server

	mu         sync.Mutex
	password   string
	order      []string
	sessions   map[string]*ocSessionState
	next       int
	streams    []*testStream
	down       bool
	connects   int
	prompts    []string
	aborts     []string
	replies    []ocReplyRecord
	statusHits int
	refusals   int
}

func newFakeOpenCode(t *testing.T) *fakeOpenCode {
	t.Helper()
	f := &fakeOpenCode{sessions: map[string]*ocSessionState{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", f.guard(f.health))
	mux.HandleFunc("POST /session", f.guard(f.create))
	mux.HandleFunc("GET /session", f.guard(f.list))
	mux.HandleFunc("GET /session/{id}", f.guard(f.get))
	mux.HandleFunc("POST /session/{id}/prompt_async", f.guard(f.prompt))
	mux.HandleFunc("POST /session/{id}/abort", f.guard(f.abort))
	mux.HandleFunc("POST /session/{id}/permissions/{pid}", f.guard(f.permission))
	mux.HandleFunc("GET /event", f.guard(f.events))

	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.dropStreams()
		f.srv.Close()
	})
	return f
}

func (f *fakeOpenCode) spec() ports.SessionSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ports.SessionSpec{
		Agent: "opencode-live",
		Cwd:   "/work/repo",
		Server: &ports.ServerSpec{
			Kind:   "opencode",
			URL:    f.srv.URL,
			Secret: f.password,
		},
	}
}

func (f *fakeOpenCode) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		want := f.password
		f.mu.Unlock()

		if want != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != opencodeUser || pass != want {
				f.mu.Lock()
				f.refusals++
				f.mu.Unlock()
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	}
}

func (f *fakeOpenCode) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"healthy": true, "version": "0.4.2"})
}

func (f *fakeOpenCode) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.next++
	s := &ocSessionState{
		id:      fmt.Sprintf("ses_%d", f.next),
		title:   body.Title,
		dir:     "/work/repo",
		updated: testStart.UnixMilli(),
	}
	f.sessions[s.id] = s
	f.order = append(f.order, s.id)
	f.mu.Unlock()

	writeJSON(w, sessionJSON(s))
}

func (f *fakeOpenCode) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	out := make([]any, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, sessionJSON(f.sessions[id]))
	}
	f.mu.Unlock()
	writeJSON(w, out)
}

func (f *fakeOpenCode) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	s, ok := f.sessions[r.PathValue("id")]
	f.statusHits++
	f.mu.Unlock()

	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	writeJSON(w, sessionJSON(s))
}

func (f *fakeOpenCode) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	for _, p := range body.Parts {
		f.prompts = append(f.prompts, p.Text)
	}
	if s, ok := f.sessions[r.PathValue("id")]; ok {
		s.busy = true
	}
	f.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeOpenCode) abort(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.aborts = append(f.aborts, r.PathValue("id"))
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (f *fakeOpenCode) permission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Response string `json:"response"`
		Remember bool   `json:"remember"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.replies = append(f.replies, ocReplyRecord{
		session:  r.PathValue("id"),
		request:  r.PathValue("pid"),
		response: body.Response,
		remember: body.Remember,
	})
	f.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true})
}

func (f *fakeOpenCode) events(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.down {
		f.mu.Unlock()
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	s := &testStream{ch: make(chan string, 16), done: make(chan struct{})}
	f.streams = append(f.streams, s)
	f.connects++
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	fmt.Fprintf(w, "data: %s\n\n", `{"type":"server.connected","properties":{}}`)
	flusher.Flush()

	for {
		select {
		case msg := <-s.ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-s.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// emit broadcasts one bus event to every open stream.
func (f *fakeOpenCode) emit(typ string, props any) {
	encoded, err := json.Marshal(map[string]any{"type": typ, "properties": props})
	if err != nil {
		panic(err)
	}

	f.mu.Lock()
	streams := slices.Clone(f.streams)
	f.mu.Unlock()

	for _, s := range streams {
		select {
		case s.ch <- string(encoded):
		case <-s.done:
		}
	}
}

// dropStreams cuts every open connection, the way a service restart would.
func (f *fakeOpenCode) dropStreams() {
	f.mu.Lock()
	streams := f.streams
	f.streams = nil
	f.mu.Unlock()

	for _, s := range streams {
		close(s.done)
	}
}

// setDown makes the service refuse new streams and cut the open ones.
func (f *fakeOpenCode) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
	if down {
		f.dropStreams()
	}
}

func (f *fakeOpenCode) waitConnects(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		got := f.connects
		f.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d event streams connected, want %d", got, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fakeOpenCode) snapshot(read func(*fakeOpenCode)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	read(f)
}

func sessionJSON(s *ocSessionState) map[string]any {
	return map[string]any{
		"id":        s.id,
		"title":     s.title,
		"directory": s.dir,
		"busy":      s.busy,
		"time":      map[string]any{"created": s.updated, "updated": s.updated},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// startOpenCode opens a session against the fake service.
func startOpenCode(t *testing.T, f *fakeOpenCode, ap ports.Approver, clk clock.Clock) ports.AgentSession {
	t.Helper()
	d := newDriver(testLogger(), clk)
	sess, err := d.Start(context.Background(), f.spec(), ap)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	f.waitConnects(t, 1)
	return sess
}

// ---------- tests ----------

func TestOpenCodeStartAndPrompt(t *testing.T) {
	f := newFakeOpenCode(t)
	sess := startOpenCode(t, f, nil, clock.NewFake(testStart))

	if got := sess.AgentSessionID(); got != "ses_1" {
		t.Errorf("AgentSessionID = %q, want ses_1", got)
	}
	if caps := sess.Caps(); !caps.List || !caps.Load || !caps.Cancel {
		t.Errorf("Caps = %+v, want list, load and cancel", caps)
	}

	if err := sess.Prompt(context.Background(), "build it"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	f.snapshot(func(f *fakeOpenCode) {
		if !slices.Equal(f.prompts, []string{"build it"}) {
			t.Errorf("the service was sent %q, want [build it]", f.prompts)
		}
		if title := f.sessions["ses_1"].title; title != "ferry: repo" {
			t.Errorf("the session was titled %q, want it named after the directory", title)
		}
	})

	// A second prompt during the same turn is refused rather than queued.
	if err := sess.Prompt(context.Background(), "and again"); err != fsp.ErrBusy {
		t.Errorf("a second prompt gave %v, want fsp.ErrBusy", err)
	}

	f.emit("session.idle", map[string]any{"sessionID": "ses_1"})
	e := waitEvent(t, sess.Events(), "the turn end", ofKind(fsp.EvTurnEnd))
	if e.StopReason != "end_turn" {
		t.Errorf("the turn ended with %q, want end_turn", e.StopReason)
	}

	if err := sess.Prompt(context.Background(), "next"); err != nil {
		t.Errorf("the session stayed busy after the turn ended: %v", err)
	}

	// SetConfig has nothing to set on either service.
	if err := sess.SetConfig(context.Background(), "mode", "plan"); err == nil {
		t.Error("SetConfig accepted an option, want it refused")
	}
}

func TestOpenCodeEventsToChunks(t *testing.T) {
	f := newFakeOpenCode(t)
	sess := startOpenCode(t, f, nil, clock.NewFake(testStart))
	events := sess.Events()

	part := func(id, sessionID, kind, text string) map[string]any {
		return map[string]any{"part": map[string]any{
			"id": id, "sessionID": sessionID, "type": kind, "text": text,
		}}
	}

	// A part that repeats what it already said only yields what is new, so the
	// user never sees the same sentence twice.
	f.emit("message.part.updated", part("prt_1", "ses_1", "text", "Hello"))
	if e := waitEvent(t, events, "the first chunk", ofKind(fsp.EvChunk)); e.Text != "Hello" {
		t.Errorf("the first chunk was %q, want Hello", e.Text)
	}
	f.emit("message.part.updated", part("prt_1", "ses_1", "text", "Hello, world"))
	if e := waitEvent(t, events, "the second chunk", ofKind(fsp.EvChunk)); e.Text != ", world" {
		t.Errorf("the second chunk was %q, want the new text only", e.Text)
	}

	// Another session's parts share the connection and must be dropped.
	f.emit("message.part.updated", part("prt_9", "ses_other", "text", "not yours"))
	f.emit("message.part.updated", part("prt_2", "ses_1", "reasoning", "thinking"))
	e := waitEvent(t, events, "the thought", func(e fsp.Event) bool { return e.Kind != fsp.EvChunk || e.Text != "" })
	if e.Kind != fsp.EvThought || e.Text != "thinking" {
		t.Fatalf("got %+v, want the thought of this session only", e)
	}

	f.emit("message.part.updated", map[string]any{"part": map[string]any{
		"id": "prt_3", "sessionID": "ses_1", "type": "tool",
		"callID": "call_1", "tool": "bash", "state": map[string]any{"status": "running"},
	}})
	e = waitEvent(t, events, "the tool call", ofKind(fsp.EvTool))
	if e.Tool == nil || e.Tool.CallID != "call_1" || e.Tool.Kind != "execute" || e.Tool.Status != "running" {
		t.Errorf("the tool call came through as %+v, want call_1 execute running", e.Tool)
	}

	// A failed turn is still a turn that ended.
	f.emit("session.error", map[string]any{
		"sessionID": "ses_1",
		"error":     map[string]any{"name": "ProviderError", "data": map[string]any{"message": "quota"}},
	})
	e = waitEvent(t, events, "the failed turn", ofKind(fsp.EvTurnEnd))
	if e.StopReason != "error" || e.Err != "quota" {
		t.Errorf("the failed turn came through as %+v, want error/quota", e)
	}
}

func TestOpenCodePermissionRoundTrip(t *testing.T) {
	ap := newApprover(fsp.Verdict{Decision: fsp.DecisionAllowAlways, OptionID: "always"})
	ap.release = make(chan struct{})

	f := newFakeOpenCode(t)
	sess := startOpenCode(t, f, ap, clock.NewFake(testStart))

	permission := map[string]any{
		"id": "perm_1", "sessionID": "ses_1", "type": "bash",
		"title": "run the tests", "metadata": map[string]any{"command": "go test ./..."},
	}
	f.emit("permission.asked", permission)

	req := ap.waitRequest(t)
	if req.ToolName != "bash" || req.ToolKind != "execute" {
		t.Errorf("the request named %q/%q, want bash/execute", req.ToolName, req.ToolKind)
	}
	if req.Description != "run the tests" {
		t.Errorf("the request read %q, want the permission title", req.Description)
	}
	if len(req.Options) != len(opencodeOptions) {
		t.Fatalf("the user was offered %d options, want %d", len(req.Options), len(opencodeOptions))
	}
	for i, want := range opencodeOptions {
		if req.Options[i] != want {
			t.Errorf("option %d = %+v, want %+v", i, req.Options[i], want)
		}
	}

	// The same permission announced again while it is still on screen must not
	// ask the user twice.
	f.emit("permission.updated", permission)
	// Frames are handled in order, so a chunk arriving after the repeat means
	// the repeat has been dealt with.
	f.emit("message.part.updated", map[string]any{"part": map[string]any{
		"id": "prt_1", "sessionID": "ses_1", "type": "text", "text": "still here",
	}})
	waitEvent(t, sess.Events(), "a chunk after the repeated permission", ofKind(fsp.EvChunk))
	select {
	case again := <-ap.reqs:
		t.Fatalf("the user was asked twice about %s", again.ToolName)
	default:
	}

	close(ap.release)

	deadline := time.Now().Add(3 * time.Second)
	for {
		var replies []ocReplyRecord
		f.snapshot(func(f *fakeOpenCode) { replies = slices.Clone(f.replies) })
		if len(replies) == 1 {
			got := replies[0]
			want := ocReplyRecord{session: "ses_1", request: "perm_1", response: "always", remember: true}
			if got != want {
				t.Errorf("the service was answered %+v, want %+v", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service was answered %d times, want once", len(replies))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestOpenCodeAbort(t *testing.T) {
	f := newFakeOpenCode(t)
	sess := startOpenCode(t, f, nil, clock.NewFake(testStart))

	if err := sess.Prompt(context.Background(), "long job"); err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	if err := sess.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	f.snapshot(func(f *fakeOpenCode) {
		if !slices.Equal(f.aborts, []string{"ses_1"}) {
			t.Errorf("the service was told to abort %q, want [ses_1]", f.aborts)
		}
	})

	// Cancelling does not end the turn by itself: the service says so on the
	// event stream.
	f.emit("session.idle", map[string]any{"sessionID": "ses_1"})
	if e := waitEvent(t, sess.Events(), "the turn end", ofKind(fsp.EvTurnEnd)); e.StopReason != "end_turn" {
		t.Errorf("the turn ended with %q, want end_turn", e.StopReason)
	}
}

func TestOpenCodeList(t *testing.T) {
	f := newFakeOpenCode(t)
	d := newDriver(testLogger(), clock.NewFake(testStart))
	ctx := context.Background()

	// Two sessions the user opened in a terminal.
	for range 2 {
		if _, err := d.Start(ctx, f.spec(), nil); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	}

	infos, err := d.List(ctx, f.spec())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d sessions, want 2", len(infos))
	}
	if infos[0].AgentSessionID != "ses_1" || infos[1].AgentSessionID != "ses_2" {
		t.Errorf("List returned %q and %q, want ses_1 and ses_2", infos[0].AgentSessionID, infos[1].AgentSessionID)
	}
	if infos[0].Cwd != "/work/repo" || infos[0].Title != "ferry: repo" {
		t.Errorf("the first session came through as %+v, want its directory and title", infos[0])
	}
	if !infos[0].UpdatedAt.Equal(testStart) {
		t.Errorf("the first session was last touched at %v, want %v", infos[0].UpdatedAt, testStart)
	}
}

func TestOpenCodeAuth(t *testing.T) {
	f := newFakeOpenCode(t)
	f.mu.Lock()
	f.password = "hunter2"
	f.mu.Unlock()

	d := newDriver(testLogger(), clock.NewFake(testStart))
	ctx := context.Background()

	// The password is what OPENCODE_SERVER_PASSWORD held; the registry has
	// already read it into the spec.
	sess, err := d.Start(ctx, f.spec(), nil)
	if err != nil {
		t.Fatalf("Start with the password failed: %v", err)
	}
	defer sess.Close()

	blind := f.spec()
	blind.Server.Secret = ""
	if _, err := d.Start(ctx, blind, nil); err == nil {
		t.Fatal("Start without the password succeeded, want it refused")
	}

	wrong := f.spec()
	wrong.Server.Secret = "letmein"
	if _, err := d.Start(ctx, wrong, nil); err == nil {
		t.Fatal("Start with the wrong password succeeded, want it refused")
	}

	f.snapshot(func(f *fakeOpenCode) {
		if f.refusals < 2 {
			t.Errorf("the service refused %d requests, want at least 2", f.refusals)
		}
	})
}
