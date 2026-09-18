package v2

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// previewRunes is the cap spec-p1 3.8 puts on the tool input shown in an
// approval request.
const previewRunes = 2000

// queue carries events from the read loop to whoever reads Events. It grows
// without bound on purpose: blocking the read loop would stall the whole
// connection, responses included, so a slow reader must never wedge the agent.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []fsp.Event
	closed bool
	out    chan fsp.Event
}

func newQueue() *queue {
	q := &queue{out: make(chan fsp.Event)}
	q.cond = sync.NewCond(&q.mu)
	go q.pump()
	return q
}

// push queues one event. Events pushed after close are dropped.
func (q *queue) push(e fsp.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, e)
	q.cond.Signal()
}

// close stops accepting events. Whatever is already queued is still delivered,
// and out closes once it has been. Calling close twice is fine.
func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.cond.Signal()
}

func (q *queue) pump() {
	defer close(q.out)
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 {
			q.mu.Unlock()
			return
		}
		e := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()

		q.out <- e
	}
}

// mapUpdate turns a v2 session update into the event the core expects. It
// reports false for the variants Ferry has nothing to say about: the whole
// message upserts that repeat what the chunks already said, the user's own
// echo, the display-only terminals and the config option lists.
//
// A state_update is not mapped here. It changes what the session thinks is
// going on, so the session handles it and decides whether a turn just ended.
func mapUpdate(u SessionUpdate, at time.Time) (fsp.Event, bool) {
	switch u.SessionUpdate {
	case UpdateAgentMessageChunk:
		text := contentText(u.Content)
		if text == "" {
			return fsp.Event{}, false
		}
		return fsp.Event{Kind: fsp.EvChunk, Text: text, At: at}, true

	case UpdateAgentThoughtChunk:
		text := contentText(u.Content)
		if text == "" {
			return fsp.Event{}, false
		}
		return fsp.Event{Kind: fsp.EvThought, Text: text, At: at}, true

	case UpdateToolCall:
		return fsp.Event{
			Kind: fsp.EvTool,
			Tool: &fsp.ToolInfo{
				CallID: u.ToolCallID,
				Kind:   u.Kind,
				Title:  toolTitle(u),
				Status: u.Status,
			},
			At: at,
		}, true

	case UpdatePlan:
		steps := make([]fsp.PlanStep, 0, len(u.Entries))
		for _, e := range u.Entries {
			steps = append(steps, fsp.PlanStep{Title: e.Content, Status: e.Status})
		}
		return fsp.Event{Kind: fsp.EvPlan, Plan: steps, At: at}, true

	default:
		return fsp.Event{}, false
	}
}

// toolTitle is what to call a tool call. v2 merged the announcement into the
// update, where the human title is optional and the method name is not, so the
// name stands in when there is no title.
func toolTitle(u SessionUpdate) string {
	if u.Title != "" {
		return u.Title
	}
	return u.Name
}

// contentText reads the text of an update's content, which is one block in the
// chunk variants and a list of them in the whole message upserts.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var one ContentBlock
	if err := json.Unmarshal(raw, &one); err == nil {
		return one.Text
	}
	var many []ContentBlock
	if err := json.Unmarshal(raw, &many); err != nil {
		return ""
	}
	var out string
	for _, b := range many {
		out += b.Text
	}
	return out
}

// approvalRequest maps an agent's permission request onto the request the
// approval module shows a user. ID and Session are left to the core.
//
// known is what the session remembers about the tool call the request is about:
// a v2 request usually only names the call, and its kind, raw input and paths
// came earlier, with the update that announced it.
func approvalRequest(params RequestPermissionParams, known *ToolCallRef, at time.Time) fsp.ApprovalRequest {
	var call ToolCallRef
	if known != nil {
		call = *known
	}
	if params.Subject != nil && params.Subject.ToolCall != nil {
		call = mergeToolCall(call, *params.Subject.ToolCall)
	}

	// v2 moved the human wording onto the request itself; the tool call keeps
	// its own title for the cases where the request did not bother.
	title := params.Title
	if title == "" {
		title = call.Title
	}
	description := params.Description
	if description == "" {
		description = title
	}

	paths := make([]string, 0, len(call.Locations))
	for _, l := range call.Locations {
		paths = append(paths, l.Path)
	}

	options := make([]fsp.ApprovalOption, 0, len(params.Options))
	for _, o := range params.Options {
		options = append(options, fsp.ApprovalOption{
			ID:    o.OptionID,
			Kind:  fsp.ApprovalOptionKind(o.Kind),
			Label: o.Name,
		})
	}

	return fsp.ApprovalRequest{
		ToolName:     title,
		ToolKind:     call.Kind,
		Description:  description,
		InputPreview: inputPreview(call.RawInput),
		Paths:        paths,
		Options:      options,
		CreatedAt:    at,
	}
}

// mergeToolCall lays what a request said about a tool call over what was already
// known about it, field by field.
func mergeToolCall(known, inline ToolCallRef) ToolCallRef {
	if inline.ToolCallID != "" {
		known.ToolCallID = inline.ToolCallID
	}
	if inline.Title != "" {
		known.Title = inline.Title
	}
	if inline.Kind != "" {
		known.Kind = inline.Kind
	}
	if len(inline.RawInput) > 0 {
		known.RawInput = inline.RawInput
	}
	if len(inline.Locations) > 0 {
		known.Locations = inline.Locations
	}
	return known
}

// inputPreview renders a tool's raw input as JSON, cut to the length the
// protocol allows.
func inputPreview(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return truncateRunes(string(raw), previewRunes)
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
