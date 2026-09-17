package fsp

import (
	"fmt"
	"strings"
)

// String returns "<platform>:<bot>:<peer>".
func (e EndpointID) String() string {
	return string(e.Platform) + ":" + e.BotID + ":" + e.PeerID
}

// ParseEndpoint is the inverse of EndpointID.String. It splits on the first two
// colons only, so PeerID may itself contain colons.
func ParseEndpoint(s string) (EndpointID, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return EndpointID{}, fmt.Errorf("fsp: parse endpoint %q: want <platform>:<bot>:<peer>", s)
	}
	return EndpointID{
		Platform: Platform(parts[0]),
		BotID:    parts[1],
		PeerID:   parts[2],
	}, nil
}
