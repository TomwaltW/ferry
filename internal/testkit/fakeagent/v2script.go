package fakeagent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/ids"
)

// The scripted behaviours of the v2 mode. They follow the same table as the v1
// ones in script.go — same prompts, same output, same timings — and differ only
// in the shapes they are sent over: a turn is announced with a running
// state_update and ends with an idle one that names the stop reason.

// v2StopReason values, as the prompt lifecycle page spells them.
const (
	v2StopEndTurn   = "end_turn"
	v2StopCancelled = "cancelled"
)

// v2AskCallID is the tool call id an "ask" prompt announces and then refers to
// from its permission request.
const v2AskCallID = "call_ask"

// runTurn runs one scripted turn and closes it with an idle state_update.
func (a *v2Agent) runTurn(ctx context.Context, id, text string) {
	a.sendState(id, "running", "")

	said, stop := a.run(ctx, id, text)
	if said != "" {
		// A turn that produced output records it even when it was cancelled, so
		// a later resume replays what the user actually saw.
		_ = a.appendTurn(id, turn{Role: roleAgent, Text: said})
	}
	a.sendState(id, "idle", stop)
}

// run dispatches on the first word of the prompt, per the table in spec-p0 3.5.
// "fail" never reaches here: a prompt that is supposed to fail is refused before
// it is accepted.
func (a *v2Agent) run(ctx context.Context, id, text string) (said, stop string) {
	verb, rest, _ := strings.Cut(text, " ")
	switch verb {
	case "echo":
		return a.v2Echo(ctx, id, rest)
	case "ask":
		return a.v2Ask(ctx, id, rest)
	case "slow":
		return a.v2Slow(ctx, id, rest)
	case "burst":
		return a.v2Burst(ctx, id, rest)
	case "plan":
		return a.v2Plan(ctx, id)
	case "tool":
		return a.v2Tool(ctx, id)
	default:
		return a.v2Say(ctx, id, "unknown: "+text)
	}
}

// update sends one session/update notification.
func (a *v2Agent) update(id string, update map[string]any) {
	_ = a.conn.notify("session/update", map[string]any{"sessionId": id, "update": update})
}

// sendChunk streams one message chunk. Every chunk carries a messageId, which
// v2 made mandatory.
func (a *v2Agent) sendChunk(ctx context.Context, id, kind, text string) {
	if ctx.Err() != nil {
		return
	}
	a.update(id, map[string]any{
		"sessionUpdate": kind,
		"messageId":     ids.New("msg"),
		"content":       map[string]any{"type": "text", "text": text},
	})
}

// sendState reports what the agent is doing. The idle one ends the turn and is
// the only one that carries a stop reason.
func (a *v2Agent) sendState(id, state, stop string) {
	update := map[string]any{"sessionUpdate": "state_update", "state": state}
	if stop != "" {
		update["stopReason"] = stop
	}
	a.update(id, update)
}

func (a *v2Agent) v2Say(ctx context.Context, id, text string) (string, string) {
	a.sendChunk(ctx, id, "agent_message_chunk", text)
	if ctx.Err() != nil {
		return text, v2StopCancelled
	}
	return text, v2StopEndTurn
}

// v2Echo streams the argument back five characters at a time.
func (a *v2Agent) v2Echo(ctx context.Context, id, text string) (string, string) {
	r := []rune(text)
	for i := 0; i < len(r); i += echoChunk {
		if ctx.Err() != nil {
			return string(r[:i]), v2StopCancelled
		}
		end := min(i+echoChunk, len(r))
		a.sendChunk(ctx, id, "agent_message_chunk", string(r[i:end]))
	}
	return text, v2StopEndTurn
}

// v2Ask announces a tool call, then asks the client for permission over it. The
// request only names the call: its details went out with the announcement, which
// is how v2 keeps the two in step.
func (a *v2Agent) v2Ask(ctx context.Context, id, rest string) (string, string) {
	kind, path, _ := strings.Cut(rest, " ")

	a.update(id, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    v2AskCallID,
		"name":          "ask",
		"title":         "ask " + path,
		"kind":          v2ToolKind(kind),
		"status":        "pending",
		"rawInput":      map[string]any{"path": path},
		"locations":     []map[string]any{{"path": path}},
	})
	a.sendState(id, "requires_action", "")

	raw, err := a.conn.call(ctx, "session/request_permission", map[string]any{
		"sessionId":   id,
		"title":       "ask " + path,
		"description": "ask " + path,
		"subject": map[string]any{
			"type":     "tool_call",
			"toolCall": map[string]any{"toolCallId": v2AskCallID},
		},
		"options": []map[string]any{
			{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
			{"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
			{"optionId": "reject-once", "name": "Reject once", "kind": "reject_once"},
		},
	})
	if err != nil {
		return "", v2StopCancelled
	}

	var resp struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Outcome.Outcome != "selected" {
		return "", v2StopCancelled
	}

	answer := "denied"
	switch resp.Outcome.OptionID {
	case "allow-once", "allow-always":
		answer = "allowed"
	}
	a.update(id, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    v2AskCallID,
		"status":        "completed",
	})
	return a.v2Say(ctx, id, answer)
}

// v2ToolKind keeps a tool kind the protocol knows and calls everything else
// other, the same way the v1 script does.
func v2ToolKind(name string) string {
	switch name {
	case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other":
		return name
	default:
		return "other"
	}
}

// v2Slow ticks a dot every 100ms for the requested number of milliseconds.
func (a *v2Agent) v2Slow(ctx context.Context, id, rest string) (string, string) {
	ms, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || ms < 0 {
		return a.v2Say(ctx, id, "slow wants a millisecond count")
	}

	deadline := a.clk.After(time.Duration(ms) * time.Millisecond)
	ticker := a.clk.NewTicker(slowTick)
	defer ticker.Stop()

	var said strings.Builder
	for {
		select {
		case <-ctx.Done():
			return said.String(), v2StopCancelled
		case <-deadline:
			return said.String(), v2StopEndTurn
		case <-ticker.C():
			a.sendChunk(ctx, id, "agent_message_chunk", ".")
			said.WriteString(".")
		}
	}
}

// v2Burst sends n fixed-width lines 50ms apart, each repeating one letter so a
// test can tell the chunks apart by index.
func (a *v2Agent) v2Burst(ctx context.Context, id, rest string) (string, string) {
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n < 0 {
		return a.v2Say(ctx, id, "burst wants a count")
	}

	var said strings.Builder
	for i := range n {
		if i > 0 {
			select {
			case <-ctx.Done():
				return said.String(), v2StopCancelled
			case <-a.clk.After(burstInterval):
			}
		}
		line := strings.Repeat(string(rune('a'+i%26)), burstWidth) + "\n"
		a.sendChunk(ctx, id, "agent_message_chunk", line)
		said.WriteString(line)
	}
	return said.String(), v2StopEndTurn
}

// v2Plan sends one three-step plan. v2 plans carry an id and a type, and every
// update repeats the whole list.
func (a *v2Agent) v2Plan(ctx context.Context, id string) (string, string) {
	a.update(id, map[string]any{
		"sessionUpdate": "plan_update",
		"type":          "items",
		"planId":        "plan_fake",
		"entries": []map[string]any{
			{"content": "read the request", "priority": "high", "status": "completed"},
			{"content": "write the code", "priority": "medium", "status": "in_progress"},
			{"content": "run the tests", "priority": "low", "status": "pending"},
		},
	})
	if ctx.Err() != nil {
		return "", v2StopCancelled
	}
	return "", v2StopEndTurn
}

// v2Tool announces a pending tool call, completes it, then reports done. In v2
// both halves are tool_call_update: the first mention of an id creates the call.
func (a *v2Agent) v2Tool(ctx context.Context, id string) (string, string) {
	const callID = "call_tool"

	a.update(id, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    callID,
		"name":          "run_tests",
		"title":         "run tests",
		"kind":          "execute",
		"status":        "pending",
	})
	a.update(id, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    callID,
		"status":        "completed",
	})
	return a.v2Say(ctx, id, "done")
}
