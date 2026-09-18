package proxy

import (
	"encoding/json"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// previewRunes is the cap spec-p1 3.8 puts on the tool input shown in an
// approval request.
const previewRunes = 2000

// statusAwaitingEditor is the tool status spec-p3 3.6 gives a permission
// request the editor is answering: the daemon turns it into
// proxy.editor_approval rather than asking anybody in chat.
const statusAwaitingEditor = "awaiting_editor"

// editorTurnMarker is what the daemon localises when the editor starts a turn.
// The proxy has no language of its own — it never reads the owner's locale — so
// it mirrors the key and lets the daemon render it.
const editorTurnMarker = "proxy.editor_turn"

// mapUpdate turns one session update into the event the daemon mirrors,
// reporting false for the updates Ferry has nothing to say about. It repeats
// the mapping of internal/driver/acp on purpose: that package belongs to
// another task this round, and the conventions prefer a copy to a shared
// utility package.
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

// approvalRequest maps the agent's permission request onto the request chat
// shows. ID and Session are left to the daemon: the approval module assigns the
// short ID, and control.Message carries the session.
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

// toolEvent describes a permission request as a tool event, which is how the
// default approval mode tells the daemon that the editor is deciding.
func toolEvent(params acp.RequestPermissionRequest, status string, at time.Time) fsp.Event {
	call := params.ToolCall
	info := &fsp.ToolInfo{CallID: string(call.ToolCallId), Status: status}
	if call.Kind != nil {
		info.Kind = string(*call.Kind)
	}
	if call.Title != nil {
		info.Title = *call.Title
	}
	return fsp.Event{Kind: fsp.EvTool, Tool: info, At: at}
}

// inputPreview renders a tool's raw input as JSON, cut to the length the
// protocol allows. Input that cannot be encoded is left out rather than guessed
// at.
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
