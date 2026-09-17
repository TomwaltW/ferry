package outbox

import (
	"context"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// The P2 outbox interface grew three methods. These placeholders keep P1
// building and its tests green; the P2 T3 task replaces them with the real
// behaviour from spec-p2 3.6.

func (b *box) Pull(ctx context.Context, ep fsp.EndpointID) error { return fsp.ErrUnsupported }

func (b *box) File(ctx context.Context, ep fsp.EndpointID, n int) error { return fsp.ErrUnsupported }

func (b *box) Held(ep fsp.EndpointID) int { return 0 }

var _ ports.Outbox = (*box)(nil)
