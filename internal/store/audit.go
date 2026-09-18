package store

import (
	"context"
	"fmt"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

var _ ports.AuditReader = (*DB)(nil)

// AuditSince returns every audit entry at or after since, oldest first. The
// autoincrement id breaks ties within a millisecond, so entries written in the
// same millisecond come back in the order they were written.
func (d *DB) AuditSince(ctx context.Context, since time.Time) ([]ports.AuditEntry, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT at, kind, endpoint, sender_id, session_id, detail
		FROM audit WHERE at >= ? ORDER BY at ASC, id ASC`, millis(since))
	if err != nil {
		return nil, fmt.Errorf("store: audit since: %w", err)
	}
	defer rows.Close()

	var out []ports.AuditEntry
	for rows.Next() {
		var (
			at        int64
			endpoint  string
			sessionID string
			e         ports.AuditEntry
		)
		if err := rows.Scan(&at, &e.Kind, &endpoint, &e.SenderID, &sessionID, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: audit since: %w", err)
		}
		// Audit writes the zero endpoint as an empty string (endpointString).
		if endpoint != "" {
			e.Endpoint, err = fsp.ParseEndpoint(endpoint)
			if err != nil {
				return nil, fmt.Errorf("store: audit since: %w", err)
			}
		}
		e.At = fromMillis(at)
		e.Session = fsp.SessionID(sessionID)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: audit since: %w", err)
	}
	return out, nil
}
