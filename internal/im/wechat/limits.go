package wechat

import "github.com/TomwaltW/ferry/internal/ports"

// Limits states what iLink allows: a reply only fits inside the window an
// inbound message opened, and the window's length and budget come from the
// outbox configuration rather than from the platform.
func (a *Adapter) Limits() ports.DeliveryLimits {
	return ports.DeliveryLimits{NeedsWindow: true}
}
