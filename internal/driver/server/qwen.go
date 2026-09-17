package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Routes of `qwen serve`, verified against the daemon's REST reference on
// 2026-09-17. Unlike OpenCode the event stream is per session.
const (
	qwHealthPath       = "/health"
	qwCapabilitiesPath = "/capabilities"
	qwSessionsPath     = "/session"
)

func qwSessionPath(id string) string { return qwSessionsPath + "/" + id }
func qwLoadPath(id string) string    { return qwSessionPath(id) + "/load" }
func qwResumePath(id string) string  { return qwSessionPath(id) + "/resume" }
func qwPromptPath(id string) string  { return qwSessionPath(id) + "/prompt" }
func qwCancelPath(id string) string  { return qwSessionPath(id) + "/cancel" }
func qwEventsPath(id string) string  { return qwSessionPath(id) + "/events" }
func qwStatusPath(id string) string  { return qwSessionPath(id) + "/status" }
func qwPermissionPath(id, r string) string {
	return qwSessionPath(id) + "/permission/" + r
}

// qwen speaks to a running `qwen serve`.
type qwen struct {
	c   *client
	log *slog.Logger
}

var _ backend = (*qwen)(nil)

func newQwen(spec ports.ServerSpec, log *slog.Logger) (*qwen, error) {
	c, err := newClient(spec.URL, log)
	if err != nil {
		return nil, err
	}
	if spec.Secret != "" {
		token := spec.Secret
		c.auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}
	return &qwen{c: c, log: log}, nil
}

func (q *qwen) kind() string { return "qwen" }

// canList is false: spec-p2 3.5 gives the daemon no route that enumerates
// sessions, so listing is reported unsupported rather than guessed at.
func (q *qwen) canList() bool { return false }

func (q *qwen) health(ctx context.Context) error {
	_, err := q.c.do(ctx, http.MethodGet, qwHealthPath, nil, nil)
	return err
}

// qwenCapabilities is GET /capabilities. spec-p2 3.5 says the resume route is
// picked from it but does not give the payload, so both shapes that could carry
// it are accepted. UNVERIFIED (spec-p2 section 0).
type qwenCapabilities struct {
	Capabilities []string        `json:"capabilities"`
	Features     map[string]bool `json:"features"`
}

// supports reports whether the daemon listed a named capability.
func (c qwenCapabilities) supports(name string) bool {
	if c.Features[name] {
		return true
	}
	return slices.Contains(c.Capabilities, name)
}

// qwenSession is the answer to creating or loading a session. UNVERIFIED
// (spec-p2 section 0): the routes are verified, the field names are not, so
// both spellings of the session ID are read.
type qwenSession struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
	ClientID  string `json:"clientId"`
}

func (s qwenSession) id() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	return s.ID
}

// create registers a session for spec.Cwd. The daemon only accepts directories
// that were registered with `qwen serve --workspace`; anything else comes back
// as a workspace mismatch, which the core turns into server.workspace_mismatch.
func (q *qwen) create(ctx context.Context, spec ports.SessionSpec) (string, error) {
	var out qwenSession
	body := map[string]string{"cwd": spec.Cwd}

	header, err := q.c.do(ctx, http.MethodPost, qwSessionsPath, body, &out)
	if err != nil {
		if isWorkspaceMismatch(err) {
			return "", fmt.Errorf("server: qwen has not registered %s: %w", spec.Cwd, fsp.ErrWorkspaceDenied)
		}
		return "", fmt.Errorf("server: open a qwen session: %w", err)
	}
	if out.id() == "" {
		return "", fmt.Errorf("server: qwen opened a session without an ID")
	}

	q.adoptClientID(header, out)
	return out.id(), nil
}

// attach loads a session the daemon already has, over whichever of the two
// routes it says it supports.
func (q *qwen) attach(ctx context.Context, spec ports.SessionSpec, agentSessionID string) (string, error) {
	path := qwLoadPath(agentSessionID)
	caps, err := q.capabilities(ctx)
	switch {
	case err != nil:
		q.log.Debug("could not read the qwen capabilities, loading the session", "err", err)
	case !caps.supports("load") && caps.supports("resume"):
		path = qwResumePath(agentSessionID)
	}

	var out qwenSession
	body := map[string]string{"cwd": spec.Cwd}
	header, err := q.c.do(ctx, http.MethodPost, path, body, &out)
	if err != nil {
		switch {
		case isWorkspaceMismatch(err):
			return "", fmt.Errorf("server: qwen has not registered %s: %w", spec.Cwd, fsp.ErrWorkspaceDenied)
		case status(err) == http.StatusNotFound:
			return "", fmt.Errorf("server: qwen has no session %s: %w", agentSessionID, fsp.ErrNotFound)
		}
		return "", fmt.Errorf("server: load qwen session %s: %w", agentSessionID, err)
	}

	q.adoptClientID(header, out)
	if id := out.id(); id != "" {
		return id, nil
	}
	return agentSessionID, nil
}

// list refuses: the daemon has no documented route for it, and guessing one
// would be a new protocol rather than a driver.
func (q *qwen) list(ctx context.Context) ([]ports.AgentSessionInfo, error) {
	return nil, fmt.Errorf("server: qwen has no route that lists sessions: %w", fsp.ErrUnsupported)
}

func (q *qwen) openEvents(ctx context.Context, id string) (io.ReadCloser, error) {
	return q.c.stream(ctx, qwEventsPath(id))
}

func (q *qwen) prompt(ctx context.Context, id, text string) error {
	// UNVERIFIED (spec-p2 section 0): the route is verified, the body is not.
	body := map[string]any{"prompt": text}
	_, err := q.c.do(ctx, http.MethodPost, qwPromptPath(id), body, nil)
	return err
}

func (q *qwen) cancel(ctx context.Context, id string) error {
	_, err := q.c.do(ctx, http.MethodPost, qwCancelPath(id), nil, nil)
	return err
}

// reply answers a permission with the option the daemon itself offered. A
// verdict with no option is what a cancelled or timed out approval looks like,
// and the daemon is told so rather than left waiting.
func (q *qwen) reply(ctx context.Context, id, requestID string, v fsp.Verdict) error {
	// UNVERIFIED (spec-p2 section 0): the route is verified, the body is not.
	body := map[string]any{
		"requestId": requestID,
		"optionId":  v.OptionID,
		"cancelled": v.OptionID == "",
	}
	_, err := q.c.do(ctx, http.MethodPost, qwPermissionPath(id, requestID), body, nil)
	return err
}

func (q *qwen) resync(ctx context.Context, id string) (sessionStatus, error) {
	// UNVERIFIED (spec-p2 section 0): the route is verified, the payload is not.
	var out struct {
		State   string `json:"state"`
		Status  string `json:"status"`
		Running *bool  `json:"running"`
	}
	if _, err := q.c.do(ctx, http.MethodGet, qwStatusPath(id), nil, &out); err != nil {
		if status(err) == http.StatusNotFound {
			return sessionStatus{}, nil
		}
		return sessionStatus{}, err
	}

	running := qwenRunningStates[out.State] || qwenRunningStates[out.Status]
	if out.Running != nil {
		running = *out.Running
	}
	return sessionStatus{Alive: true, Running: running}, nil
}

func (q *qwen) capabilities(ctx context.Context) (qwenCapabilities, error) {
	var out qwenCapabilities
	_, err := q.c.do(ctx, http.MethodGet, qwCapabilitiesPath, nil, &out)
	return out, err
}

// adoptClientID keeps the identity the daemon handed out, from the header when
// it sent one and from the body otherwise. A daemon that sends neither gets no
// such header back.
func (q *qwen) adoptClientID(header http.Header, body qwenSession) {
	id := header.Get(qwenClientIDHeader)
	if id == "" {
		id = body.ClientID
	}
	if id != "" {
		q.c.setHeader(qwenClientIDHeader, id)
	}
}

// isWorkspaceMismatch reports the daemon's refusal to work in a directory that
// was never registered.
func isWorkspaceMismatch(err error) bool {
	var he *httpError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(he.Body, qwenWorkspaceMismatch)
}

// ---------- events ----------

func (q *qwen) decode(id string, ev sseEvent) []action {
	var env struct {
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	if len(ev.Data) > 0 {
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			q.log.Debug("qwen sent a frame that is not JSON", "err", err)
			return nil
		}
	}
	name := ev.Name
	if name == "" {
		name = env.Type
	}
	if name == "" {
		name = env.Event
	}

	switch qwenEvents[name] {
	case qwUpdate:
		return q.decodeUpdate(id, ev.Data)
	case qwPermissionRequest:
		return q.decodePermission(id, ev.Data)
	case qwPermissionResolved:
		return q.decodeResolved(id, ev.Data)
	case qwTurnComplete:
		return q.decodeTurnComplete(id, ev.Data)
	case qwTurnError:
		return q.decodeTurnError(id, ev.Data)
	case qwSessionGone:
		return q.decodeGone(id, ev.Data)
	case qwStreamError:
		// The stream itself failed. Reconnecting is the answer, and the
		// streamer is already doing it.
		q.log.Debug("qwen reported a stream error", "session", id)
		return nil
	default:
		return nil
	}
}

// qwenUpdate is a session_update event. sessionUpdate and content are verified;
// the fields that carry a tool call are UNVERIFIED (spec-p2 section 0).
type qwenUpdate struct {
	SessionID     string          `json:"sessionId"`
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	Entries       []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
}

func (q *qwen) decodeUpdate(id string, data []byte) []action {
	var u qwenUpdate
	if err := json.Unmarshal(data, &u); err != nil {
		return nil
	}
	if !sameSession(id, u.SessionID) {
		return nil
	}

	switch qwenUpdateKinds[u.SessionUpdate] {
	case fsp.EvChunk, fsp.EvThought:
		text := textOf(u.Content)
		if text == "" {
			return nil
		}
		return []action{{event: &fsp.Event{
			Kind: qwenUpdateKinds[u.SessionUpdate],
			Text: text,
			At:   time.Now(),
		}}}

	case fsp.EvTool:
		return []action{{event: &fsp.Event{
			Kind: fsp.EvTool,
			Tool: &fsp.ToolInfo{
				CallID: u.ToolCallID,
				Kind:   u.Kind,
				Title:  u.Title,
				Status: u.Status,
			},
			At: time.Now(),
		}}}

	case fsp.EvPlan:
		steps := make([]fsp.PlanStep, 0, len(u.Entries))
		for _, e := range u.Entries {
			steps = append(steps, fsp.PlanStep{Title: e.Content, Status: e.Status})
		}
		return []action{{event: &fsp.Event{Kind: fsp.EvPlan, Plan: steps, At: time.Now()}}}

	default:
		return nil
	}
}

// qwenPermission is a permission_request event. The field names come from the
// daemon's event schema; the kinds its options carry do not (see
// qwenOptionKinds).
type qwenPermission struct {
	RequestID string `json:"requestId"`
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		Name      string          `json:"name"`
		Title     string          `json:"title"`
		Kind      string          `json:"kind"`
		RawInput  json.RawMessage `json:"rawInput"`
		Locations []struct {
			Path string `json:"path"`
		} `json:"locations"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
	OriginatorClientID string `json:"originatorClientId"`
}

func (q *qwen) decodePermission(id string, data []byte) []action {
	var p qwenPermission
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	if !sameSession(id, p.SessionID) || p.RequestID == "" {
		return nil
	}
	// A request another client raised still reaches the user: a shared session
	// is the point of the second tier, and whoever is at a phone may well be
	// the one to answer it.

	call := p.ToolCall
	name := call.Name
	if name == "" {
		name = call.Title
	}

	paths := make([]string, 0, len(call.Locations))
	for _, l := range call.Locations {
		paths = append(paths, l.Path)
	}

	options := make([]fsp.ApprovalOption, 0, len(p.Options))
	for _, o := range p.Options {
		kind, ok := qwenOptionKinds[o.Kind]
		if !ok {
			kind = fsp.OptAllowOnce
		}
		options = append(options, fsp.ApprovalOption{ID: o.OptionID, Kind: kind, Label: o.Name})
	}

	title := call.Title
	if title == "" {
		title = name
	}
	return []action{{ask: &askAction{
		requestID: p.RequestID,
		req: fsp.ApprovalRequest{
			ToolName:     name,
			ToolKind:     call.Kind,
			Description:  title,
			InputPreview: preview(call.RawInput),
			Paths:        paths,
			Options:      options,
			CreatedAt:    time.Now(),
		},
	}}}
}

func (q *qwen) decodeResolved(id string, data []byte) []action {
	var p struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
		Outcome   string `json:"outcome"`
	}
	if err := json.Unmarshal(data, &p); err != nil || p.RequestID == "" {
		return nil
	}
	if !sameSession(id, p.SessionID) {
		return nil
	}
	return []action{{resolved: p.RequestID}}
}

func (q *qwen) decodeTurnComplete(id string, data []byte) []action {
	var p struct {
		SessionID  string `json:"sessionId"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	if !sameSession(id, p.SessionID) {
		return nil
	}

	reason, ok := qwenStopReasons[p.StopReason]
	if !ok {
		reason = "end_turn"
	}
	return []action{{event: &fsp.Event{Kind: fsp.EvTurnEnd, StopReason: reason, At: time.Now()}}}
}

func (q *qwen) decodeTurnError(id string, data []byte) []action {
	var p struct {
		SessionID string `json:"sessionId"`
		Message   string `json:"message"`
		Code      string `json:"code"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	if !sameSession(id, p.SessionID) {
		return nil
	}

	msg := p.Message
	if msg == "" {
		msg = p.Code
	}
	// Like the ACP driver: a turn that failed still ended.
	return []action{{event: &fsp.Event{
		Kind:       fsp.EvTurnEnd,
		StopReason: "error",
		Err:        msg,
		At:         time.Now(),
	}}}
}

func (q *qwen) decodeGone(id string, data []byte) []action {
	var p struct {
		SessionID string `json:"sessionId"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	if !sameSession(id, p.SessionID) {
		return nil
	}

	msg := "the qwen daemon closed this session"
	if p.Reason != "" {
		msg += ": " + p.Reason
	}
	return []action{{event: &fsp.Event{Kind: fsp.EvError, Err: msg, At: time.Now()}}}
}

// sameSession reports whether an event that named a session named this one. An
// event that names none belongs to the stream it arrived on.
func sameSession(want, got string) bool { return got == "" || got == want }

// textOf reads the text out of a content field, which the daemon may write as a
// bare string, as one content block or as a list of them. UNVERIFIED (spec-p2
// section 0): the schema names the field but not its shape.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var block struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err == nil && block.Text != "" {
		return block.Text
	}

	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, part := range blocks {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}
