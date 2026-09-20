package fakeserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
)

// openCodeUser is the HTTP Basic user name OpenCode's server expects; the
// password is whatever the agent's secret_env holds. It mirrors opencodeUser in
// internal/driver/server/eventmap.go.
const openCodeUser = "opencode"

// OpenCodeReply is one answer the driver sent back to a permission request.
type OpenCodeReply struct {
	Session  string
	Request  string
	Response string
	Remember bool
}

type ocSession struct {
	id      string
	title   string
	dir     string
	updated int64
	busy    bool
}

// OpenCode serves the routes of spec-p2 3.5 for `opencode serve` and nothing
// else. Create it before the daemon, point an agent's server.url at URL, and
// drive the session with Emit.
type OpenCode struct {
	// URL is the base address to put in an agent's server.url. It is a
	// 127.0.0.1 address, which is the only kind the configuration accepts.
	URL string

	srv *httptest.Server

	mu       sync.Mutex
	password string
	dir      string
	order    []string
	sessions map[string]*ocSession
	next     int
	streams  []*stream
	connects int
	prompts  []string
	aborts   []string
	replies  []OpenCodeReply
}

// NewOpenCode starts the fixture. Password is the HTTP Basic password the
// service demands; empty means it demands none, which is how the e2e rig runs
// it. Close it when the test ends.
func NewOpenCode(password string) *OpenCode {
	f := &OpenCode{
		password: password,
		dir:      "/work/repo",
		sessions: map[string]*ocSession{},
	}

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
	f.URL = f.srv.URL
	return f
}

// SetDir sets the working directory the service reports for its sessions.
// OpenCode's create route is not told a directory, so the service decides it;
// a test that cares sets the directory it started the service in.
func (f *OpenCode) SetDir(dir string) {
	f.mu.Lock()
	f.dir = dir
	f.mu.Unlock()
}

// Close cuts every open stream and shuts the service down.
func (f *OpenCode) Close() {
	f.dropStreams()
	f.srv.Close()
}

func (f *OpenCode) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		want := f.password
		f.mu.Unlock()

		if want != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != openCodeUser || pass != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	}
}

func (f *OpenCode) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"healthy": true, "version": "0.4.2"})
}

func (f *OpenCode) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.next++
	s := &ocSession{id: fmt.Sprintf("ses_%d", f.next), title: body.Title, dir: f.dir}
	f.sessions[s.id] = s
	f.order = append(f.order, s.id)
	f.mu.Unlock()

	writeJSON(w, ocSessionJSON(s))
}

func (f *OpenCode) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	out := make([]any, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, ocSessionJSON(f.sessions[id]))
	}
	f.mu.Unlock()
	writeJSON(w, out)
}

func (f *OpenCode) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	s, ok := f.sessions[r.PathValue("id")]
	f.mu.Unlock()

	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	writeJSON(w, ocSessionJSON(s))
}

func (f *OpenCode) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

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

func (f *OpenCode) abort(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.aborts = append(f.aborts, r.PathValue("id"))
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (f *OpenCode) permission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Response string `json:"response"`
		Remember bool   `json:"remember"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.replies = append(f.replies, OpenCodeReply{
		Session:  r.PathValue("id"),
		Request:  r.PathValue("pid"),
		Response: body.Response,
		Remember: body.Remember,
	})
	f.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true})
}

func (f *OpenCode) events(w http.ResponseWriter, r *http.Request) {
	s := newStream()

	f.mu.Lock()
	f.streams = append(f.streams, s)
	f.connects++
	f.mu.Unlock()

	s.pump(w, r, "data: "+`{"type":"server.connected","properties":{}}`+"\n\n")
}

// Emit broadcasts one bus event to every open stream, in the envelope the
// OpenCode event bus uses.
func (f *OpenCode) Emit(typ string, props any) {
	encoded, err := json.Marshal(map[string]any{"type": typ, "properties": props})
	if err != nil {
		panic(err)
	}
	frame := "data: " + string(encoded) + "\n\n"

	f.mu.Lock()
	streams := slices.Clone(f.streams)
	f.mu.Unlock()

	for _, s := range streams {
		s.send(frame)
	}
}

// EmitText pushes one text part, which the driver turns into a chunk. Calling
// it twice with the same partID and a longer text yields only what is new, so a
// test that wants two separate chunks uses two IDs.
func (f *OpenCode) EmitText(sessionID, partID, text string) {
	f.Emit("message.part.updated", map[string]any{"part": map[string]any{
		"id": partID, "sessionID": sessionID, "type": "text", "text": text,
	}})
}

// EmitPermission pushes a permission request. command is what the tool would
// run and is what reaches the approval preview.
func (f *OpenCode) EmitPermission(sessionID, permissionID, tool, title, command string) {
	f.Emit("permission.asked", map[string]any{
		"id": permissionID, "sessionID": sessionID, "type": tool,
		"title":    title,
		"metadata": map[string]any{"command": command},
	})
}

// EmitIdle ends the turn.
func (f *OpenCode) EmitIdle(sessionID string) {
	f.Emit("session.idle", map[string]any{"sessionID": sessionID})
}

func (f *OpenCode) dropStreams() {
	f.mu.Lock()
	streams := f.streams
	f.streams = nil
	f.mu.Unlock()

	for _, s := range streams {
		close(s.done)
	}
}

// SessionIDs lists the sessions the service has created, in order.
func (f *OpenCode) SessionIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

// Title is the title the driver asked for when it created a session.
func (f *OpenCode) Title(sessionID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		return s.title
	}
	return ""
}

// Prompts is every prompt text the service was sent, in order.
func (f *OpenCode) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
}

// Replies is every permission answer the driver sent, in order.
func (f *OpenCode) Replies() []OpenCodeReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

// Aborts is every session the driver cancelled, in order.
func (f *OpenCode) Aborts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.aborts)
}

// WaitSession waits until the service has created n sessions and reports
// whether it did.
func (f *OpenCode) WaitSession(n int) bool {
	return waitFor(Patience, func() bool { return len(f.SessionIDs()) >= n })
}

// WaitStreams waits until n event streams have been opened, which is how a test
// knows the driver is listening before it emits anything.
func (f *OpenCode) WaitStreams(n int) bool {
	return waitFor(Patience, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.connects >= n
	})
}

// WaitPrompt waits until the service has been sent n prompts.
func (f *OpenCode) WaitPrompt(n int) bool {
	return waitFor(Patience, func() bool { return len(f.Prompts()) >= n })
}

// WaitReplies waits until the driver has answered n permission requests.
func (f *OpenCode) WaitReplies(n int) bool {
	return waitFor(Patience, func() bool { return len(f.Replies()) >= n })
}

func ocSessionJSON(s *ocSession) map[string]any {
	return map[string]any{
		"id":        s.id,
		"title":     s.title,
		"directory": s.dir,
		"busy":      s.busy,
		"time":      map[string]any{"created": s.updated, "updated": s.updated},
	}
}
