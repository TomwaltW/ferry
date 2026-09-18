package control

import (
	"encoding/json"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// SocketName is the file the daemon writes its loopback address and token to,
// with mode 0600.
const SocketName = "control.json"

// Endpoint is the contents of control.json.
type Endpoint struct {
	Port  int    `json:"port"`
	Token string `json:"token"` // 32 hex characters
	PID   int    `json:"pid"`
}

// Request is one line of the control protocol. A token that does not match
// makes the daemon drop the connection and write an audit entry.
type Request struct {
	Token string          `json:"token"`
	Op    string          `json:"op"` // P2: status | park; P3 adds attach
	Args  json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"` // not_found | unsupported | busy | internal
	Data  json.RawMessage `json:"data,omitempty"`
}

// OpAttach registers an external session — one run by ferry proxy, ferry
// channel or ferry run rather than by the daemon. Its answer is the last
// request/response pair on that connection: afterwards both sides exchange
// Message lines until one of them goes away.
const OpAttach = "attach"

// The kinds of external session AttachRequest.Kind names.
const (
	KindProxy   = "proxy"
	KindChannel = "channel"
	KindPTY     = "pty"
)

type AttachRequest struct {
	Kind           string `json:"kind"` // proxy | channel | pty
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Cwd            string `json:"cwd"`
	Title          string `json:"title,omitempty"`
	PID            int    `json:"pid"`
	AllowInject    bool   `json:"allow_inject"`
	RemoteApprove  bool   `json:"remote_approve"`
}

type AttachResponse struct {
	SessionID string `json:"session_id"`
	Slot      int    `json:"slot"`
	Endpoint  string `json:"endpoint"`
}

// Message is one frame of the streaming half of the protocol. A single
// connection may carry several sessions — one ferry proxy process registers one
// per session/new — so everything but ping and pong names its SessionID.
type Message struct {
	Type      string               `json:"type"`
	SessionID string               `json:"session_id,omitempty"`
	ReqID     string               `json:"req_id,omitempty"` // ties an approval request to its verdict
	Event     *fsp.Event           `json:"event,omitempty"`
	Approval  *fsp.ApprovalRequest `json:"approval,omitempty"`
	Verdict   *fsp.Verdict         `json:"verdict,omitempty"`
	Text      string               `json:"text,omitempty"`
	Sender    string               `json:"sender,omitempty"`
	Key       string               `json:"key,omitempty"`
	Value     string               `json:"value,omitempty"`
}

// Message types sent by the external process.
const (
	MsgEvent           = "event"            // session output, tools, plans, turn ends, errors
	MsgApprovalRequest = "approval_request" // asks IM to decide, carries ReqID
	MsgApprovalCancel  = "approval_cancel"  // withdraws one request
	MsgDetach          = "detach"           // the session ended
)

// Message types sent by the daemon.
const (
	MsgPrompt  = "prompt"  // input from IM, in Text and Sender
	MsgVerdict = "verdict" // the answer to an approval_request, carries ReqID
	MsgCancel  = "cancel"  // cancel the running turn
	MsgConfig  = "config"  // set a session option, in Key and Value
	MsgClose   = "close"   // close the session
)

// Message types either side sends.
const (
	MsgPing = "ping"
	MsgPong = "pong"
)

// Keepalive: each side sends a ping every PingInterval, and treats a stream
// that said nothing for PingTimeout as gone. Both are driven by an injected
// clock so that tests do not wait for them.
const (
	PingInterval = 15 * time.Second
	PingTimeout  = 30 * time.Second
)

// ServerStream is the daemon's end of one attached connection. Handler.Attach
// keeps it for the life of the external session; Recv returns io.EOF once the
// external process goes away.
type ServerStream interface {
	Send(m Message) error
	Recv() (Message, error)
}
