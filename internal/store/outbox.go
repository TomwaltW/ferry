package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

func (d *DB) AddOutbox(ctx context.Context, msg fsp.Outbound, at time.Time) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO outbox (endpoint, session_id, kind, body, file_path, dedupe_key, urgent, state, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		msg.Endpoint.String(), string(msg.Session), string(msg.Kind), msg.Text, msg.FilePath,
		msg.DedupeKey, boolToInt(msg.Urgent), string(fsp.DeliveryQueued), millis(at), millis(at))
	if err != nil {
		return 0, fmt.Errorf("store: add outbox: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: add outbox: %w", err)
	}
	return id, nil
}

func (d *DB) UpdateOutbox(ctx context.Context, id int64, state fsp.DeliveryState, attempts int, at time.Time) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE outbox SET state = ?, attempts = ?, updated_at = ? WHERE id = ?`,
		string(state), attempts, millis(at), id)
	if err != nil {
		return fmt.Errorf("store: update outbox %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update outbox %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update outbox %d: %w", id, fsp.ErrNotFound)
	}
	return nil
}

func (d *DB) OutboxByState(ctx context.Context, ep fsp.EndpointID, state fsp.DeliveryState) ([]ports.OutboxRow, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, endpoint, session_id, kind, body, file_path, dedupe_key, urgent, state, attempts, created_at
		FROM outbox WHERE endpoint = ? AND state = ? ORDER BY id ASC`, ep.String(), string(state))
	if err != nil {
		return nil, fmt.Errorf("store: outbox for %s: %w", ep, err)
	}
	defer rows.Close()
	var out []ports.OutboxRow
	for rows.Next() {
		var (
			row       ports.OutboxRow
			endpoint  string
			session   string
			kind      string
			state     string
			urgent    int
			createdAt int64
		)
		if err := rows.Scan(&row.ID, &endpoint, &session, &kind, &row.Msg.Text, &row.Msg.FilePath,
			&row.Msg.DedupeKey, &urgent, &state, &row.Attempts, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan outbox row: %w", err)
		}
		parsed, err := fsp.ParseEndpoint(endpoint)
		if err != nil {
			return nil, fmt.Errorf("store: scan outbox row: %w", err)
		}
		row.Msg.Endpoint = parsed
		row.Msg.Session = fsp.SessionID(session)
		row.Msg.Kind = fsp.OutKind(kind)
		row.Msg.Urgent = urgent != 0
		row.State = fsp.DeliveryState(state)
		row.CreatedAt = fromMillis(createdAt)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan outbox rows: %w", err)
	}
	return out, nil
}

func (d *DB) OutboxEndpoints(ctx context.Context, state fsp.DeliveryState) ([]fsp.EndpointID, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT endpoint FROM outbox WHERE state = ? ORDER BY endpoint ASC`, string(state))
	if err != nil {
		return nil, fmt.Errorf("store: outbox endpoints: %w", err)
	}
	defer rows.Close()
	var out []fsp.EndpointID
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return nil, fmt.Errorf("store: scan outbox endpoint: %w", err)
		}
		ep, err := fsp.ParseEndpoint(endpoint)
		if err != nil {
			return nil, fmt.Errorf("store: scan outbox endpoint: %w", err)
		}
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan outbox endpoints: %w", err)
	}
	return out, nil
}

func (d *DB) SaveWindow(ctx context.Context, w ports.Window) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO windows (endpoint, reply_ctx, opened_at, used) VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET
			reply_ctx = excluded.reply_ctx,
			opened_at = excluded.opened_at,
			used = excluded.used`,
		w.Endpoint.String(), w.ReplyCtx, millis(w.OpenedAt), w.Used)
	if err != nil {
		return fmt.Errorf("store: save window for %s: %w", w.Endpoint, err)
	}
	return nil
}

func (d *DB) LoadWindow(ctx context.Context, ep fsp.EndpointID) (ports.Window, error) {
	var (
		w        ports.Window
		openedAt int64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT reply_ctx, opened_at, used FROM windows WHERE endpoint = ?`, ep.String()).
		Scan(&w.ReplyCtx, &openedAt, &w.Used)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Window{}, fmt.Errorf("store: window for %s: %w", ep, fsp.ErrNotFound)
	}
	if err != nil {
		return ports.Window{}, fmt.Errorf("store: window for %s: %w", ep, err)
	}
	w.Endpoint = ep
	w.OpenedAt = fromMillis(openedAt)
	return w, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
