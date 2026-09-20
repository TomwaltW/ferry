package fakeserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
)

// qwenClientIDHeader carries the client identity the daemon hands out, so a
// permission answered by Ferry can be told from one answered in the terminal.
// It mirrors qwenClientIDHeader in internal/driver/server/eventmap.go.
const qwenClientIDHeader = "X-Qwen-Client-Id"

// qwenWorkspaceMismatch is the error code the daemon returns for a directory
// that was never registered with `qwen serve --workspace`.
const qwenWorkspaceMismatch = "workspace_mismatch"

// QwenReply is one answer the driver sent back to a permission request.
type QwenReply struct {
	Session  string
	Request  string
	Option   string
	ClientID string
}

// Qwen serves the routes of spec-p2 3.5 for the `qwen serve` daemon. Unlike
// OpenCode it keeps one event stream per session, and it refuses to open a
// session on a directory that was never registered.
type Qwen struct {
	// URL is the base address to put in an agent's server.url.
	URL string

	srv *httptest.Server

	mu         sync.Mutex
	token      string
	workspaces map[string]bool
	next       int
	order      []string
	sessions   map[string]bool
	running    map[string]bool
	streams    map[string][]*stream
	connects   map[string]int
	cwds       []string
	prompts    []string
	cancels    []string
	replies    []QwenReply
}

// NewQwen starts the fixture. token is the bearer token the daemon demands;
// empty means it demands none. Register the directories it will accept before
// a session is opened on one, the way `qwen serve --workspace` does.
func NewQwen(token string) *Qwen {
	f := &Qwen{
		token:      token,
		workspaces: map[string]bool{},
		sessions:   map[string]bool{},
		running:    map[string]bool{},
		streams:    map[string][]*stream{},
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
	f.URL = f.srv.URL
	return f
}

// Register accepts a working directory, as `qwen serve --workspace` does.
// Opening a session anywhere else comes back as a workspace mismatch.
func (f *Qwen) Register(dirs ...string) {
	f.mu.Lock()
	for _, d := range dirs {
		f.workspaces[d] = true
	}
	f.mu.Unlock()
}

// Close cuts every open stream and shuts the daemon down.
func (f *Qwen) Close() {
	f.dropStreams()
	f.srv.Close()
}

func (f *Qwen) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		want := f.token
		f.mu.Unlock()

		if want != "" && r.Header.Get("Authorization") != "Bearer "+want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (f *Qwen) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok"})
}

func (f *Qwen) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"capabilities": []string{"load"}})
}

func (f *Qwen) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cwd string `json:"cwd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.cwds = append(f.cwds, body.Cwd)
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
	f.order = append(f.order, id)
	f.mu.Unlock()

	writeJSON(w, map[string]any{"sessionId": id})
}

func (f *Qwen) load(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	f.mu.Lock()
	known := f.sessions[id]
	f.mu.Unlock()

	if !known {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"sessionId": id})
}

func (f *Qwen) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.prompts = append(f.prompts, body.Prompt)
	f.running[r.PathValue("id")] = true
	f.mu.Unlock()

	writeJSON(w, map[string]any{"accepted": true})
}

func (f *Qwen) cancel(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.cancels = append(f.cancels, r.PathValue("id"))
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (f *Qwen) permission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OptionID string `json:"optionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.replies = append(f.replies, QwenReply{
		Session:  r.PathValue("id"),
		Request:  r.PathValue("rid"),
		Option:   body.OptionID,
		ClientID: r.Header.Get(qwenClientIDHeader),
	})
	f.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true})
}

func (f *Qwen) status(w http.ResponseWriter, r *http.Request) {
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

func (f *Qwen) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	f.mu.Lock()
	known := f.sessions[id]
	if known {
		s := newStream()
		f.streams[id] = append(f.streams[id], s)
		f.connects[id]++
		f.mu.Unlock()
		// The daemon sends no opening frame of its own.
		s.pump(w, r, "")
		return
	}
	f.mu.Unlock()
	http.Error(w, "no such session", http.StatusNotFound)
}

// Emit sends one named event down a session's stream, in the frame shape the
// daemon's event schema spells out.
func (f *Qwen) Emit(sessionID, name string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
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
		s.send(frame)
	}
}

// EmitText pushes one agent message chunk.
func (f *Qwen) EmitText(sessionID, text string) {
	f.Emit(sessionID, "session_update", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": text,
	})
}

// EmitPermission pushes a permission request offering the daemon's three
// standard options. path is the file the tool would touch and command what it
// would run; both reach the approval the user sees.
func (f *Qwen) EmitPermission(sessionID, requestID, tool, title, command, path string) {
	f.Emit(sessionID, "permission_request", map[string]any{
		"requestId": requestID,
		"toolCall": map[string]any{
			"name": tool, "title": title, "kind": "execute",
			"rawInput":  map[string]any{"command": command},
			"locations": []map[string]any{{"path": path}},
		},
		"options": []map[string]any{
			{"optionId": "proceed_once", "name": "Yes", "kind": "allow_once"},
			{"optionId": "proceed_always", "name": "Yes, always", "kind": "allow_always"},
			{"optionId": "cancel", "name": "No", "kind": "reject_once"},
		},
		"originatorClientId": "cli-terminal",
	})
}

// EmitTurnComplete ends the turn.
func (f *Qwen) EmitTurnComplete(sessionID string) {
	f.Emit(sessionID, "turn_complete", map[string]any{"stopReason": "end_turn"})
}

func (f *Qwen) dropStreams() {
	f.mu.Lock()
	all := f.streams
	f.streams = map[string][]*stream{}
	f.mu.Unlock()

	for _, streams := range all {
		for _, s := range streams {
			close(s.done)
		}
	}
}

// SessionIDs lists the sessions the daemon has created, in order.
func (f *Qwen) SessionIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.order)
}

// Cwds is every working directory a session was asked for, in order, including
// the ones refused as a workspace mismatch.
func (f *Qwen) Cwds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cwds)
}

// Prompts is every prompt text the daemon was sent, in order.
func (f *Qwen) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
}

// Replies is every permission answer the driver sent, in order.
func (f *Qwen) Replies() []QwenReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

// Cancels is every session the driver cancelled, in order.
func (f *Qwen) Cancels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cancels)
}

// WaitSession waits until the daemon has created n sessions.
func (f *Qwen) WaitSession(n int) bool {
	return waitFor(Patience, func() bool { return len(f.SessionIDs()) >= n })
}

// WaitStreams waits until n event streams have been opened for one session,
// which is how a test knows the driver is listening before it emits anything.
func (f *Qwen) WaitStreams(sessionID string, n int) bool {
	return waitFor(Patience, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.connects[sessionID] >= n
	})
}

// WaitPrompt waits until the daemon has been sent n prompts.
func (f *Qwen) WaitPrompt(n int) bool {
	return waitFor(Patience, func() bool { return len(f.Prompts()) >= n })
}

// WaitReplies waits until the driver has answered n permission requests.
func (f *Qwen) WaitReplies(n int) bool {
	return waitFor(Patience, func() bool { return len(f.Replies()) >= n })
}
