package store

import (
	"context"
	"fmt"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// HoldOutbox marks a row held and records when, so that FlushHeld can redeliver
// it in the order it arrived once a reply window reopens.
func (d *DB) HoldOutbox(ctx context.Context, id int64, at time.Time) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE outbox SET state = ?, held_at = ?, updated_at = ? WHERE id = ?`,
		string(fsp.DeliveryHeld), millis(at), millis(at), id)
	if err != nil {
		return fmt.Errorf("store: hold outbox %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: hold outbox %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: hold outbox %d: %w", id, fsp.ErrNotFound)
	}
	return nil
}

func (d *DB) AddLongOutput(ctx context.Context, lo ports.LongOutput) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO long_outputs (endpoint, session_id, path, chars, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		lo.Endpoint.String(), string(lo.Session), lo.Path, lo.Chars, millis(lo.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("store: add long output: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: add long output: %w", err)
	}
	return id, nil
}

// LongOutputs returns the endpoint's files, newest first. A limit of zero or
// less returns all of them.
func (d *DB) LongOutputs(ctx context.Context, ep fsp.EndpointID, limit int) ([]ports.LongOutput, error) {
	if limit < 1 {
		limit = -1 // SQLite reads a negative LIMIT as "no limit"
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, endpoint, session_id, path, chars, created_at
		FROM long_outputs WHERE endpoint = ? ORDER BY id DESC LIMIT ?`, ep.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: long outputs for %s: %w", ep, err)
	}
	defer rows.Close()
	var out []ports.LongOutput
	for rows.Next() {
		var (
			lo        ports.LongOutput
			endpoint  string
			session   string
			createdAt int64
		)
		if err := rows.Scan(&lo.ID, &endpoint, &session, &lo.Path, &lo.Chars, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan long output: %w", err)
		}
		parsed, err := fsp.ParseEndpoint(endpoint)
		if err != nil {
			return nil, fmt.Errorf("store: scan long output: %w", err)
		}
		lo.Endpoint = parsed
		lo.Session = fsp.SessionID(session)
		lo.CreatedAt = fromMillis(createdAt)
		out = append(out, lo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan long outputs: %w", err)
	}
	return out, nil
}

// DeleteLongOutput removes the row. Removing the file itself is the outbox's
// job, since only it knows which files it wrote.
func (d *DB) DeleteLongOutput(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM long_outputs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete long output %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete long output %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete long output %d: %w", id, fsp.ErrNotFound)
	}
	return nil
}
