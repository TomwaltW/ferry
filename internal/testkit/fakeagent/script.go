package fakeagent

import (
	"context"
	"strconv"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Timings the scripted behaviours are defined by. They are part of what the
// driver tests observe, so they are deliberately real durations.
const (
	slowTick      = 100 * time.Millisecond
	burstInterval = 50 * time.Millisecond
	burstWidth    = 60
	echoChunk     = 5
)

// promptText joins the text blocks of a prompt; the fake agent scripts on text
// alone and ignores the other content kinds.
func promptText(blocks []acp.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Text != nil {
			b.WriteString(blk.Text.Text)
		}
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// run dispatches on the first word of the prompt, per the table in spec-p0
// 3.5. It returns the text the agent streamed, the stop reason, and a
// JSON-RPC error when the turn itself is supposed to fail.
func (a *agent) run(ctx context.Context, id acp.SessionId, text string) (string, acp.StopReason, error) {
	verb, rest, _ := strings.Cut(text, " ")
	switch verb {
	case "echo":
		return a.runEcho(ctx, id, rest)
	case "ask":
		return a.runAsk(ctx, id, rest)
	case "slow":
		return a.runSlow(ctx, id, rest)
	case "burst":
		return a.runBurst(ctx, id, rest)
	case "plan":
		return a.runPlan(ctx, id)
	case "tool":
		return a.runTool(ctx, id)
	case "fail":
		return "", "", acp.NewInternalError(map[string]any{"error": "fake agent was asked to fail"})
	default:
		return a.say(ctx, id, "unknown: "+text)
	}
}

// send streams one session update, reporting a cancelled turn as such rather
// than as an error when the context ended underneath it.
func (a *agent) send(ctx context.Context, id acp.SessionId, update acp.SessionUpdate) error {
	err := a.peer().SessionUpdate(ctx, acp.SessionNotification{SessionId: id, Update: update})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// say streams one agent message chunk and ends the turn.
func (a *agent) say(ctx context.Context, id acp.SessionId, text string) (string, acp.StopReason, error) {
	if err := a.send(ctx, id, acp.UpdateAgentMessageText(text)); err != nil {
		return "", "", err
	}
	return text, acp.StopReasonEndTurn, nil
}

// runEcho streams the argument back five characters at a time.
func (a *agent) runEcho(ctx context.Context, id acp.SessionId, text string) (string, acp.StopReason, error) {
	r := []rune(text)
	for i := 0; i < len(r); i += echoChunk {
		end := min(i+echoChunk, len(r))
		if err := a.send(ctx, id, acp.UpdateAgentMessageText(string(r[i:end]))); err != nil {
			return "", "", err
		}
	}
	return text, acp.StopReasonEndTurn, nil
}

// runAsk asks the client for permission over the given tool kind and path,
// then reports the answer. A cancelled outcome — whether the client says so or
// the turn is cancelled while the request is still pending — ends as cancelled.
func (a *agent) runAsk(ctx context.Context, id acp.SessionId, rest string) (string, acp.StopReason, error) {
	kind, path, _ := strings.Cut(rest, " ")

	resp, err := a.peer().RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: id,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call_ask"),
			Title:      acp.Ptr("ask " + path),
			Kind:       acp.Ptr(toolKind(kind)),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			Locations:  []acp.ToolCallLocation{{Path: path}},
		},
		Options: []acp.PermissionOption{
			{OptionId: "allow-once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "allow-always", Name: "Allow always", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "reject-once", Name: "Reject once", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", acp.StopReasonCancelled, nil
		}
		return "", "", err
	}
	if resp.Outcome.Selected == nil {
		return "", acp.StopReasonCancelled, nil
	}

	answer := "denied"
	switch resp.Outcome.Selected.OptionId {
	case "allow-once", "allow-always":
		answer = "allowed"
	}
	return a.say(ctx, id, answer)
}

// toolKind maps the tool kind named in an ask prompt onto the protocol enum,
// falling back to "other" for anything unrecognised.
func toolKind(name string) acp.ToolKind {
	switch acp.ToolKind(name) {
	case acp.ToolKindRead, acp.ToolKindEdit, acp.ToolKindDelete, acp.ToolKindMove,
		acp.ToolKindSearch, acp.ToolKindExecute, acp.ToolKindThink, acp.ToolKindFetch,
		acp.ToolKindSwitchMode, acp.ToolKindOther:
		return acp.ToolKind(name)
	default:
		return acp.ToolKindOther
	}
}

// runSlow ticks a dot every 100ms for the requested number of milliseconds. A
// cancel lands as soon as the context ends, well inside the 200ms the spec
// allows.
func (a *agent) runSlow(ctx context.Context, id acp.SessionId, rest string) (string, acp.StopReason, error) {
	ms, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || ms < 0 {
		return "", "", acp.NewInvalidParams(map[string]any{"error": "slow wants a millisecond count", "got": rest})
	}

	deadline := a.clk.After(time.Duration(ms) * time.Millisecond)
	ticker := a.clk.NewTicker(slowTick)
	defer ticker.Stop()

	var said strings.Builder
	for {
		select {
		case <-ctx.Done():
			return said.String(), acp.StopReasonCancelled, nil
		case <-deadline:
			return said.String(), acp.StopReasonEndTurn, nil
		case <-ticker.C():
			if err := a.send(ctx, id, acp.UpdateAgentMessageText(".")); err != nil {
				if ctx.Err() != nil {
					return said.String(), acp.StopReasonCancelled, nil
				}
				return "", "", err
			}
			said.WriteString(".")
		}
	}
}

// runBurst sends n fixed-width lines 50ms apart. Each line repeats a single
// letter so a test can tell the chunks apart by index.
func (a *agent) runBurst(ctx context.Context, id acp.SessionId, rest string) (string, acp.StopReason, error) {
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n < 0 {
		return "", "", acp.NewInvalidParams(map[string]any{"error": "burst wants a count", "got": rest})
	}

	var said strings.Builder
	for i := range n {
		if i > 0 {
			select {
			case <-ctx.Done():
				return said.String(), acp.StopReasonCancelled, nil
			case <-a.clk.After(burstInterval):
			}
		}
		line := strings.Repeat(string(rune('a'+i%26)), burstWidth) + "\n"
		if err := a.send(ctx, id, acp.UpdateAgentMessageText(line)); err != nil {
			if ctx.Err() != nil {
				return said.String(), acp.StopReasonCancelled, nil
			}
			return "", "", err
		}
		said.WriteString(line)
	}
	return said.String(), acp.StopReasonEndTurn, nil
}

// runPlan sends one three-step plan and ends the turn.
func (a *agent) runPlan(ctx context.Context, id acp.SessionId) (string, acp.StopReason, error) {
	plan := acp.UpdatePlan(
		acp.PlanEntry{Content: "read the request", Priority: acp.PlanEntryPriorityHigh, Status: acp.PlanEntryStatusCompleted},
		acp.PlanEntry{Content: "write the code", Priority: acp.PlanEntryPriorityMedium, Status: acp.PlanEntryStatusInProgress},
		acp.PlanEntry{Content: "run the tests", Priority: acp.PlanEntryPriorityLow, Status: acp.PlanEntryStatusPending},
	)
	if err := a.send(ctx, id, plan); err != nil {
		return "", "", err
	}
	return "", acp.StopReasonEndTurn, nil
}

// runTool announces a pending tool call, completes it, then reports done.
func (a *agent) runTool(ctx context.Context, id acp.SessionId) (string, acp.StopReason, error) {
	const callID = acp.ToolCallId("call_tool")

	start := acp.StartToolCall(callID, "run tests",
		acp.WithStartKind(acp.ToolKindExecute),
		acp.WithStartStatus(acp.ToolCallStatusPending),
	)
	if err := a.send(ctx, id, start); err != nil {
		return "", "", err
	}
	done := acp.UpdateToolCall(callID, acp.WithUpdateStatus(acp.ToolCallStatusCompleted))
	if err := a.send(ctx, id, done); err != nil {
		return "", "", err
	}
	return a.say(ctx, id, "done")
}
