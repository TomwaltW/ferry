package server

import "github.com/TomwaltW/ferry/internal/fsp"

// This file is the one place where Ferry writes down what the two services call
// things: event names, permission values, tool kinds and the handful of payload
// values that select behaviour. Nothing else in the package spells an event
// name or a permission value.
//
// Values the human verification of spec-p2 section 0 could not confirm are
// marked UNVERIFIED. Running
//
//	grep -rn UNVERIFIED ./internal/driver/server
//
// lists everything that has to be rechecked against a real service, and
// correcting it is an edit to this file rather than a search through the
// package. The fake services in the tests are built from these same tables, so
// a green test suite says the driver agrees with itself, not that it agrees
// with OpenCode or Qwen.

// ---------- OpenCode ----------

// ocEvent is what the driver does with one OpenCode bus event.
type ocEvent int

const (
	ocIgnore ocEvent = iota
	ocConnected
	ocPart
	ocIdle
	ocFailed
	ocPermissionAsked
	ocPermissionResolved
)

// opencodeEvents maps the `type` of an OpenCode bus event onto what the driver
// does with it. Only the first frame is documented; the rest come from
// community sources.
var opencodeEvents = map[string]ocEvent{
	"server.connected":     ocConnected,          // verified: server.mdx names it as the first frame
	"message.part.updated": ocPart,               // UNVERIFIED (spec-p2 section 0)
	"session.idle":         ocIdle,               // UNVERIFIED (spec-p2 section 0)
	"session.error":        ocFailed,             // UNVERIFIED (spec-p2 section 0)
	"permission.asked":     ocPermissionAsked,    // UNVERIFIED (spec-p2 section 0)
	"permission.updated":   ocPermissionAsked,    // UNVERIFIED (spec-p2 section 0): same payload under a newer name
	"permission.replied":   ocPermissionResolved, // UNVERIFIED (spec-p2 section 0)
}

// opencodePartKinds maps the `type` of a message part onto the event kind it
// becomes. A part type that is not listed is dropped.
var opencodePartKinds = map[string]fsp.EventKind{
	"text":      fsp.EvChunk,   // UNVERIFIED (spec-p2 section 0)
	"reasoning": fsp.EvThought, // UNVERIFIED (spec-p2 section 0)
	"tool":      fsp.EvTool,    // UNVERIFIED (spec-p2 section 0)
}

// opencodeOptions are the three choices spec-p2 3.5 asks for, and at the same
// time the values Ferry puts in the `response` field when it answers. OpenCode
// sends no option list of its own, so Ferry supplies one; the option ID is the
// response value, which keeps the offer and the reply in a single table.
//
// UNVERIFIED (spec-p2 section 0): the official documentation names the
// `response` field but not its values.
var opencodeOptions = []fsp.ApprovalOption{
	{ID: "once", Kind: fsp.OptAllowOnce, Label: "Allow once"},
	{ID: "always", Kind: fsp.OptAllowAlways, Label: "Allow always"},
	{ID: "reject", Kind: fsp.OptRejectOnce, Label: "Reject"},
}

// opencodeReject is the response sent when a verdict carries no option at all,
// which is how a cancelled or timed out approval reaches the driver. Rejecting
// is the safe reading of "no answer".
//
// UNVERIFIED (spec-p2 section 0), same as opencodeOptions.
const opencodeReject = "reject"

// opencodeUser is the HTTP Basic user name; the password is the value of
// OPENCODE_SERVER_PASSWORD. Verified against opencode.ai/docs/server.
const opencodeUser = "opencode"

// opencodeToolKinds maps an OpenCode tool name onto one of the tool kinds fsp
// knows. Anything not listed is reported as "other".
var opencodeToolKinds = map[string]string{
	"bash":      "execute", // UNVERIFIED (spec-p2 section 0)
	"edit":      "edit",    // UNVERIFIED (spec-p2 section 0)
	"write":     "edit",    // UNVERIFIED (spec-p2 section 0)
	"patch":     "edit",    // UNVERIFIED (spec-p2 section 0)
	"read":      "read",    // UNVERIFIED (spec-p2 section 0)
	"list":      "read",    // UNVERIFIED (spec-p2 section 0)
	"grep":      "search",  // UNVERIFIED (spec-p2 section 0)
	"glob":      "search",  // UNVERIFIED (spec-p2 section 0)
	"webfetch":  "fetch",   // UNVERIFIED (spec-p2 section 0)
	"task":      "think",   // UNVERIFIED (spec-p2 section 0)
	"todowrite": "think",   // UNVERIFIED (spec-p2 section 0)
}

// ---------- Qwen Code ----------

// qwEvent is what the driver does with one event from the Qwen daemon.
type qwEvent int

const (
	qwIgnore qwEvent = iota
	qwUpdate
	qwPermissionRequest
	qwPermissionResolved
	qwTurnComplete
	qwTurnError
	qwSessionGone
	qwStreamError
)

// qwenEvents maps a Qwen daemon event name onto what the driver does with it.
// Verified against the daemon's docs/developers/daemon/09-event-schema.md.
var qwenEvents = map[string]qwEvent{
	"session_update":      qwUpdate,
	"permission_request":  qwPermissionRequest,
	"permission_resolved": qwPermissionResolved,
	"turn_complete":       qwTurnComplete,
	"turn_error":          qwTurnError,
	"session_died":        qwSessionGone,
	"session_closed":      qwSessionGone,
	"stream_error":        qwStreamError,
}

// qwenUpdateKinds maps the `sessionUpdate` discriminator of a session_update
// event onto the event kind it becomes. A value that is not listed is dropped.
//
// UNVERIFIED (spec-p2 section 0): the event schema names the field but not the
// values it takes; these are the ACP spellings the daemon is built on.
var qwenUpdateKinds = map[string]fsp.EventKind{
	"agent_message_chunk": fsp.EvChunk,
	"agent_thought_chunk": fsp.EvThought,
	"tool_call":           fsp.EvTool,
	"tool_call_update":    fsp.EvTool,
	"plan":                fsp.EvPlan,
}

// qwenOptionKinds maps the kind on a permission option onto Ferry's. An option
// whose kind is not listed is offered as allow_once, because the daemon lists
// the option it considers safest first and Ferry must still show something.
//
// UNVERIFIED (spec-p2 section 0): the schema names the options array but not
// the kinds its entries carry.
var qwenOptionKinds = map[string]fsp.ApprovalOptionKind{
	"allow_once":    fsp.OptAllowOnce,
	"allow_always":  fsp.OptAllowAlways,
	"reject_once":   fsp.OptRejectOnce,
	"reject_always": fsp.OptRejectAlways,
}

// qwenStopReasons maps the stopReason of a turn_complete event onto the stop
// reasons fsp.Event documents. Anything else is reported as end_turn.
//
// UNVERIFIED (spec-p2 section 0): the schema names the field but not its values.
var qwenStopReasons = map[string]string{
	"end_turn":   "end_turn",
	"cancelled":  "cancelled",
	"canceled":   "cancelled",
	"max_tokens": "max_tokens",
	"refusal":    "refusal",
	"error":      "error",
}

// qwenRunningStates are the values of the `state` field of GET
// /session/:id/status that mean a turn is still in flight. The route is
// verified; its payload is not.
//
// UNVERIFIED (spec-p2 section 0).
var qwenRunningStates = map[string]bool{
	"running":   true,
	"busy":      true,
	"prompting": true,
	"thinking":  true,
}

// qwenClientIDHeader carries the client identity the daemon hands out when a
// session is created or loaded: given one, Ferry repeats it verbatim on every
// later request, and given none it sends nothing. It identifies attachments and
// ownership, not a security principal. Verified against the protocol document.
const qwenClientIDHeader = "X-Qwen-Client-Id"

// qwenWorkspaceMismatch is the error code the daemon returns with 400 when the
// directory was never registered with `qwen serve --workspace`. Verified
// against spec-p2 3.5; it maps onto fsp.ErrWorkspaceDenied.
const qwenWorkspaceMismatch = "workspace_mismatch"
