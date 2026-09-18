package ports

import (
	"context"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// Workspaces decides where each user may work (spec-p4-b1 3.4). A user is a
// sender at an endpoint, and the bot owner is recognised on every endpoint of
// its bot (spec-p4-b1 3.3). It is an interface of its own, next to ACL rather
// than inside it, so that every existing ACL keeps its method set.
type Workspaces interface {
	// Roots returns the workspace roots of the sender, first match wins:
	// workspace.roots for the bot owner; the roots of the sender's
	// [[workspace.users]] entry; <users_root>/<config.UserDirName>, created
	// with mode 0700 when missing, while workspace.users_root is set; and
	// workspace.roots, shared as in P1–P3, when it is not. Every path is
	// absolute with symlinks resolved, the form the core compares a directory
	// in, and the result is never empty.
	//
	// The roots of a session are those of the peer of its owner endpoint:
	// Roots(ctx, s.Owner, s.Owner.PeerID).
	Roots(ctx context.Context, ep fsp.EndpointID, senderID string) ([]string, error)
}
