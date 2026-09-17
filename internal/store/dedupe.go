package store

import (
	"context"
	"fmt"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// dedupeTTL is how long a message ID is remembered.
const dedupeTTL = 24 * time.Hour

// SeenMessage reports whether the message has already been handled, recording
// it when it has not. Records older than 24 hours are dropped on the way.
func (d *DB) SeenMessage(ctx context.Context, ep fsp.EndpointID, messageID string, at time.Time) (bool, error) {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM dedupe WHERE seen_at < ?`, millis(at.Add(-dedupeTTL))); err != nil {
		return false, fmt.Errorf("store: prune dedupe: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO dedupe (endpoint, message_id, seen_at) VALUES (?, ?, ?)`,
		ep.String(), messageID, millis(at))
	if err != nil {
		return false, fmt.Errorf("store: record message %q: %w", messageID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record message %q: %w", messageID, err)
	}
	return n == 0, nil
}
