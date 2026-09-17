package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Bind gives the session the lowest positive slot not yet used at the
// endpoint. Binding a session that is already bound returns its current slot.
func (d *DB) Bind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) (int, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: bind %s: %w", id, err)
	}
	defer tx.Rollback()

	var slot int
	err = tx.QueryRowContext(ctx,
		`SELECT slot FROM bindings WHERE endpoint = ? AND session_id = ?`, ep.String(), string(id)).Scan(&slot)
	switch {
	case err == nil:
		return slot, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("store: bind %s: %w", id, err)
	}

	slot, err = freeSlot(ctx, tx, ep)
	if err != nil {
		return 0, fmt.Errorf("store: bind %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO bindings (endpoint, session_id, slot, foreground) VALUES (?, ?, ?, 0)`,
		ep.String(), string(id), slot); err != nil {
		return 0, fmt.Errorf("store: bind %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: bind %s: %w", id, err)
	}
	return slot, nil
}

// freeSlot returns the lowest positive integer not taken at the endpoint.
func freeSlot(ctx context.Context, tx *sql.Tx, ep fsp.EndpointID) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT slot FROM bindings WHERE endpoint = ? ORDER BY slot ASC`, ep.String())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			return 0, err
		}
		if slot != want {
			// The first gap in the ascending run is the free slot.
			break
		}
		want++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return want, nil
}

func (d *DB) Unbind(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error {
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM bindings WHERE endpoint = ? AND session_id = ?`, ep.String(), string(id))
	if err != nil {
		return fmt.Errorf("store: unbind %s: %w", id, err)
	}
	return nil
}

func (d *DB) SetForeground(ctx context.Context, ep fsp.EndpointID, id fsp.SessionID) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set foreground %s: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE bindings SET foreground = 0 WHERE endpoint = ?`, ep.String()); err != nil {
		return fmt.Errorf("store: set foreground %s: %w", id, err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE bindings SET foreground = 1 WHERE endpoint = ? AND session_id = ?`, ep.String(), string(id))
	if err != nil {
		return fmt.Errorf("store: set foreground %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set foreground %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set foreground %s: %w", id, fsp.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set foreground %s: %w", id, err)
	}
	return nil
}

func (d *DB) Bindings(ctx context.Context, ep fsp.EndpointID) ([]ports.Binding, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT session_id, slot, foreground FROM bindings WHERE endpoint = ? ORDER BY slot ASC`, ep.String())
	if err != nil {
		return nil, fmt.Errorf("store: bindings for %s: %w", ep, err)
	}
	defer rows.Close()
	var out []ports.Binding
	for rows.Next() {
		var (
			sessionID  string
			slot       int
			foreground int
		)
		if err := rows.Scan(&sessionID, &slot, &foreground); err != nil {
			return nil, fmt.Errorf("store: scan binding: %w", err)
		}
		out = append(out, ports.Binding{
			Endpoint:   ep,
			Session:    fsp.SessionID(sessionID),
			Slot:       slot,
			Foreground: foreground != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan bindings: %w", err)
	}
	return out, nil
}
