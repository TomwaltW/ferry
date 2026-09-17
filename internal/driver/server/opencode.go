package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Routes of `opencode serve`, verified against the OpenCode server
// documentation on 2026-09-17. The event stream is global: one connection
// carries every session, which is why decode filters by session ID.
const (
	ocHealthPath   = "/global/health"
	ocSessionsPath = "/session"
	ocEventsPath   = "/event"
)

func ocSessionPath(id string) string { return ocSessionsPath + "/" + id }
func ocPromptPath(id string) string  { return ocSessionPath(id) + "/prompt_async" }
func ocAbortPath(id string) string   { return ocSessionPath(id) + "/abort" }
func ocPermissionPath(id, r string) string {
	return ocSessionPath(id) + "/permissions/" + r
}

// opencode speaks to a running `opencode serve`.
type opencode struct {
	c   *client
	log *slog.Logger

	mu sync.Mutex
	// parts is the text already reported for each message part. OpenCode's
	// part update may carry the whole part or only what is new — spec-p2
	// section 0 could not settle which — so the driver reports the difference
	// and is correct either way.
	parts map[string]string
}

var _ backend = (*opencode)(nil)

func newOpenCode(spec ports.ServerSpec, log *slog.Logger) (*opencode, error) {
	c, err := newClient(spec.URL, log)
	if err != nil {
		return nil, err
	}
	if spec.Secret != "" {
		secret := spec.Secret
		c.auth = func(r *http.Request) { r.SetBasicAuth(opencodeUser, secret) }
	}
	return &opencode{c: c, log: log, parts: map[string]string{}}, nil
}

func (o *opencode) kind() string  { return "opencode" }
func (o *opencode) canList() bool { return true }

func (o *opencode) health(ctx context.Context) error {
	var out struct {
		Healthy bool   `json:"healthy"`
		Version string `json:"version"`
	}
	if _, err := o.c.do(ctx, http.MethodGet, ocHealthPath, nil, &out); err != nil {
		return err
	}
	if !out.Healthy {
		return fmt.Errorf("server: opencode reports itself unhealthy")
	}
	return nil
}

// opencodeSession is what the service returns for a session. Only `id` is
// documented; the rest is UNVERIFIED (spec-p2 section 0) and only shapes how a
// session is listed, never whether it can be driven.
type opencodeSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
	// Busy says whether a turn is in flight; absent means the service did not
	// say, and resync then treats the session as idle.
	Busy *bool `json:"busy"`
}

func (o *opencode) create(ctx context.Context, spec ports.SessionSpec) (string, error) {
	var out opencodeSession
	body := map[string]string{"title": sessionTitle(spec.Cwd)}
	if _, err := o.c.do(ctx, http.MethodPost, ocSessionsPath, body, &out); err != nil {
		return "", fmt.Errorf("server: open an opencode session: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("server: opencode opened a session without an ID")
	}
	return out.ID, nil
}

// attach only checks that the session is still there: OpenCode keeps it in the
// service, so there is nothing to load.
func (o *opencode) attach(ctx context.Context, spec ports.SessionSpec, agentSessionID string) (string, error) {
	var out opencodeSession
	if _, err := o.c.do(ctx, http.MethodGet, ocSessionPath(agentSessionID), nil, &out); err != nil {
		if status(err) == http.StatusNotFound {
			return "", fmt.Errorf("server: opencode has no session %s: %w", agentSessionID, fsp.ErrNotFound)
		}
		return "", err
	}
	return agentSessionID, nil
}

func (o *opencode) list(ctx context.Context) ([]ports.AgentSessionInfo, error) {
	var out []opencodeSession
	if _, err := o.c.do(ctx, http.MethodGet, ocSessionsPath, nil, &out); err != nil {
		return nil, err
	}

	infos := make([]ports.AgentSessionInfo, 0, len(out))
	for _, s := range out {
		infos = append(infos, ports.AgentSessionInfo{
			AgentSessionID: s.ID,
			Title:          s.Title,
			Cwd:            s.Directory,
			UpdatedAt:      millis(s.Time.Updated),
		})
	}
	return infos, nil
}

func (o *opencode) openEvents(ctx context.Context, id string) (io.ReadCloser, error) {
	return o.c.stream(ctx, ocEventsPath)
}

func (o *opencode) prompt(ctx context.Context, id, text string) error {
	body := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": text}},
	}
	_, err := o.c.do(ctx, http.MethodPost, ocPromptPath(id), body, nil)
	return err
}

func (o *opencode) cancel(ctx context.Context, id string) error {
	_, err := o.c.do(ctx, http.MethodPost, ocAbortPath(id), nil, nil)
	return err
}

// reply answers a permission. The value sent is the option ID Ferry offered,
// which is also the `response` value from opencodeOptions; a verdict with no
// option at all is a rejection.
func (o *opencode) reply(ctx context.Context, id, requestID string, v fsp.Verdict) error {
	response := v.OptionID
	if response == "" {
		response = opencodeReject
	}
	body := map[string]any{
		"response": response,
		"remember": v.Decision == fsp.DecisionAllowAlways,
	}
	_, err := o.c.do(ctx, http.MethodPost, ocPermissionPath(id, requestID), body, nil)
	return err
}

func (o *opencode) resync(ctx context.Context, id string) (sessionStatus, error) {
	var out opencodeSession
	if _, err := o.c.do(ctx, http.MethodGet, ocSessionPath(id), nil, &out); err != nil {
		if status(err) == http.StatusNotFound {
			return sessionStatus{}, nil
		}
		return sessionStatus{}, err
	}
	return sessionStatus{Alive: true, Running: out.Busy != nil && *out.Busy}, nil
}

// ocEnvelope is the frame OpenCode puts on the wire: a name and a payload.
type ocEnvelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

func (o *opencode) decode(id string, ev sseEvent) []action {
	var env ocEnvelope
	if len(ev.Data) > 0 {
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			o.log.Debug("opencode sent a frame that is not JSON", "err", err)
			return nil
		}
	}
	name := env.Type
	if name == "" {
		name = ev.Name
	}

	switch opencodeEvents[name] {
	case ocPart:
		return o.decodePart(id, env.Properties)
	case ocIdle:
		if !o.forSession(id, env.Properties) {
			return nil
		}
		return []action{{event: &fsp.Event{Kind: fsp.EvTurnEnd, StopReason: "end_turn", At: time.Now()}}}
	case ocFailed:
		return o.decodeError(id, env.Properties)
	case ocPermissionAsked:
		return o.decodePermission(id, env.Properties)
	case ocPermissionResolved:
		return o.decodeResolved(id, env.Properties)
	default:
		// server.connected and everything Ferry has nothing to say about.
		return nil
	}
}

// ocPart is a message part update. Every field here is UNVERIFIED (spec-p2
// section 0): the event itself is only known from community sources.
type ocPartPayload struct {
	Part struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
		CallID    string `json:"callID"`
		Tool      string `json:"tool"`
		State     struct {
			Status string `json:"status"`
		} `json:"state"`
	} `json:"part"`
}

func (o *opencode) decodePart(id string, raw json.RawMessage) []action {
	var p ocPartPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	part := p.Part
	if part.SessionID != "" && part.SessionID != id {
		return nil
	}

	switch opencodePartKinds[part.Type] {
	case fsp.EvChunk, fsp.EvThought:
		text := o.delta(part.ID, part.Text)
		if text == "" {
			return nil
		}
		return []action{{event: &fsp.Event{
			Kind: opencodePartKinds[part.Type],
			Text: text,
			At:   time.Now(),
		}}}

	case fsp.EvTool:
		callID := part.CallID
		if callID == "" {
			callID = part.ID
		}
		return []action{{event: &fsp.Event{
			Kind: fsp.EvTool,
			Tool: &fsp.ToolInfo{
				CallID: callID,
				Kind:   toolKind(part.Tool),
				Title:  part.Tool,
				Status: part.State.Status,
			},
			At: time.Now(),
		}}}

	default:
		return nil
	}
}

// delta is what is new in a part. A payload that repeats everything said so far
// yields only the tail; one that carries a fragment yields the fragment.
func (o *opencode) delta(partID, text string) string {
	if partID == "" || text == "" {
		return text
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	prev := o.parts[partID]
	o.parts[partID] = text
	if prev != "" && strings.HasPrefix(text, prev) {
		return text[len(prev):]
	}
	return text
}

func (o *opencode) decodeError(id string, raw json.RawMessage) []action {
	// UNVERIFIED (spec-p2 section 0), like the event name itself.
	var p struct {
		SessionID string `json:"sessionID"`
		Error     struct {
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	if p.SessionID != "" && p.SessionID != id {
		return nil
	}

	msg := p.Error.Data.Message
	if msg == "" {
		msg = p.Error.Name
	}
	if msg == "" {
		msg = "opencode reported an error"
	}
	// A turn that failed still ended, so the core hears a turn end with the
	// error stop reason. EvError is reserved for losing the session itself.
	return []action{{event: &fsp.Event{
		Kind:       fsp.EvTurnEnd,
		StopReason: "error",
		Err:        msg,
		At:         time.Now(),
	}}}
}

// ocPermission is the permission payload. UNVERIFIED (spec-p2 section 0).
type ocPermission struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	Type      string          `json:"type"`
	Title     string          `json:"title"`
	Pattern   string          `json:"pattern"`
	Metadata  json.RawMessage `json:"metadata"`
}

func (o *opencode) decodePermission(id string, raw json.RawMessage) []action {
	p, ok := unwrapPermission(raw)
	if !ok || p.ID == "" {
		return nil
	}
	if p.SessionID != "" && p.SessionID != id {
		return nil
	}

	title := p.Title
	if title == "" {
		title = p.Type
	}
	return []action{{ask: &askAction{
		requestID: p.ID,
		req: fsp.ApprovalRequest{
			ToolName:     p.Type,
			ToolKind:     toolKind(p.Type),
			Description:  title,
			InputPreview: preview(p.Metadata),
			Options:      append([]fsp.ApprovalOption(nil), opencodeOptions...),
			CreatedAt:    time.Now(),
		},
	}}}
}

func (o *opencode) decodeResolved(id string, raw json.RawMessage) []action {
	p, ok := unwrapPermission(raw)
	if !ok || p.ID == "" {
		return nil
	}
	if p.SessionID != "" && p.SessionID != id {
		return nil
	}
	return []action{{resolved: p.ID}}
}

// unwrapPermission reads the permission out of a payload that either is the
// permission or wraps it under "permission". UNVERIFIED (spec-p2 section 0):
// both shapes appear in community reports, so both are accepted.
func unwrapPermission(raw json.RawMessage) (ocPermission, bool) {
	var wrapper struct {
		Permission *ocPermission `json:"permission"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Permission != nil {
		return *wrapper.Permission, true
	}
	var p ocPermission
	if err := json.Unmarshal(raw, &p); err != nil {
		return ocPermission{}, false
	}
	return p, true
}

// forSession reports whether a payload carrying only a session ID is this
// session's.
func (o *opencode) forSession(id string, raw json.RawMessage) bool {
	var p struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return p.SessionID == "" || p.SessionID == id
}

// ---------- shared helpers ----------

// toolKind maps a tool name onto one of the kinds fsp documents.
func toolKind(name string) string {
	if k, ok := opencodeToolKinds[strings.ToLower(name)]; ok {
		return k
	}
	return "other"
}

// status is the HTTP status behind an error, or zero when the request never
// got an answer.
func status(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// sessionTitle names a session after the directory it works in, which is what
// the user sees in the service's own listing.
func sessionTitle(cwd string) string {
	if cwd == "" {
		return "ferry"
	}
	return "ferry: " + filepath.Base(cwd)
}

func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// preview renders a payload as the input preview of an approval request, cut to
// the length the protocol allows.
func preview(raw json.RawMessage) string {
	const previewRunes = 2000
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	r := []rune(s)
	if len(r) <= previewRunes {
		return s
	}
	return string(r[:previewRunes])
}
