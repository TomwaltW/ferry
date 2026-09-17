package control

import "encoding/json"

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
	Op    string          `json:"op"` // P2: status | park
	Args  json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"` // not_found | unsupported | busy | internal
	Data  json.RawMessage `json:"data,omitempty"`
}
