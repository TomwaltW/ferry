package acp

import (
	"encoding/json"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// previewRunes is the cap spec-p1 3.8 puts on the tool input shown in an
// approval request.
const previewRunes = 2000

// queue carries events from the JSON-RPC receive goroutine to whoever reads
// Events. It grows without bound on purpose: blocking the receive goroutine
// would stall the whole connection, responses included, so a slow reader must
// never be able to wedge the agent.
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

// mapUpdate turns a session update into the event the core expects, reporting
// false for the updates Ferry has nothing to say about — user message chunks
// replayed on load, mode changes, usage and the rest.
func mapUpdate(u acp.SessionUpdate, at time.Time) (fsp.Event, bool) {
	switch {
	case u.AgentMessageChunk != nil:
		text := blockText(u.AgentMessageChunk.Content)
		if text == "" {
			return fsp.Event{}, false
		}
		return fsp.Event{Kind: fsp.EvChunk, Text: text, At: at}, true

	case u.AgentThoughtChunk != nil:
		text := blockText(u.AgentThoughtChunk.Content)
		if text == "" {
			return fsp.Event{}, false
		}
		return fsp.Event{Kind: fsp.EvThought, Text: text, At: at}, true

	case u.ToolCall != nil:
		c := u.ToolCall
		return fsp.Event{
			Kind: fsp.EvTool,
			Tool: &fsp.ToolInfo{
				CallID: string(c.ToolCallId),
				Kind:   string(c.Kind),
				Title:  c.Title,
				Status: string(c.Status),
			},
			At: at,
		}, true

	case u.ToolCallUpdate != nil:
		c := u.ToolCallUpdate
		info := &fsp.ToolInfo{CallID: string(c.ToolCallId)}
		if c.Kind != nil {
			info.Kind = string(*c.Kind)
		}
		if c.Title != nil {
			info.Title = *c.Title
		}
		if c.Status != nil {
			info.Status = string(*c.Status)
		}
		return fsp.Event{Kind: fsp.EvTool, Tool: info, At: at}, true

	case u.Plan != nil:
		steps := make([]fsp.PlanStep, 0, len(u.Plan.Entries))
		for _, e := range u.Plan.Entries {
			steps = append(steps, fsp.PlanStep{Title: e.Content, Status: string(e.Status)})
		}
		return fsp.Event{Kind: fsp.EvPlan, Plan: steps, At: at}, true

	default:
		return fsp.Event{}, false
	}
}

// blockText is the text of a content block, or "" for the kinds that carry no
// text of their own.
func blockText(b acp.ContentBlock) string {
	if b.Text != nil {
		return b.Text.Text
	}
	return ""
}

// approvalRequest maps an agent's permission request onto the request the
// approval module shows a user. ID and Session are left to the core.
func approvalRequest(params acp.RequestPermissionRequest, at time.Time) fsp.ApprovalRequest {
	call := params.ToolCall

	title := ""
	if call.Title != nil {
		title = *call.Title
	}
	kind := ""
	if call.Kind != nil {
		kind = string(*call.Kind)
	}

	paths := make([]string, 0, len(call.Locations))
	for _, l := range call.Locations {
		paths = append(paths, l.Path)
	}

	options := make([]fsp.ApprovalOption, 0, len(params.Options))
	for _, o := range params.Options {
		options = append(options, fsp.ApprovalOption{
			ID:    string(o.OptionId),
			Kind:  fsp.ApprovalOptionKind(o.Kind),
			Label: o.Name,
		})
	}

	return fsp.ApprovalRequest{
		ToolName:     title,
		ToolKind:     kind,
		Description:  title,
		InputPreview: inputPreview(call.RawInput),
		Paths:        paths,
		Options:      options,
		CreatedAt:    at,
	}
}

// inputPreview renders a tool's raw input as JSON, cut to the length the
// protocol allows. Input that cannot be encoded is left out rather than
// guessed at.
func inputPreview(raw any) string {
	if raw == nil {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return truncateRunes(string(b), previewRunes)
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
