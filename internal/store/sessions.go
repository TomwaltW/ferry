package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/TomwaltW/ferry/internal/fsp"
)

const sessionColumns = `id, driver, agent, cwd, agent_session_id, origin, state, owner, created_at, last_active`

func (d *DB) PutSession(ctx context.Context, s fsp.Session) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO sessions (`+sessionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			driver = excluded.driver,
			agent = excluded.agent,
			cwd = excluded.cwd,
			agent_session_id = excluded.agent_session_id,
			origin = excluded.origin,
			state = excluded.state,
			owner = excluded.owner,
			created_at = excluded.created_at,
			last_active = excluded.last_active`,
		string(s.ID), string(s.Driver), s.Agent, s.Cwd, s.AgentSessionID,
		string(s.Origin), string(s.State), s.Owner.String(), millis(s.CreatedAt), millis(s.LastActive))
	if err != nil {
		return fmt.Errorf("store: put session %s: %w", s.ID, err)
	}
	return nil
}

func (d *DB) GetSession(ctx context.Context, id fsp.SessionID) (fsp.Session, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, string(id))
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return fsp.Session{}, fmt.Errorf("store: session %s: %w", id, fsp.ErrNotFound)
	}
	if err != nil {
		return fsp.Session{}, fmt.Errorf("store: session %s: %w", id, err)
	}
	return s, nil
}

func (d *DB) ListSessions(ctx context.Context, owner fsp.EndpointID) ([]fsp.Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE owner = ? ORDER BY created_at ASC, id ASC`, owner.String())
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for %s: %w", owner, err)
	}
	return collectSessions(rows)
}

func (d *DB) ActiveSessions(ctx context.Context) ([]fsp.Session, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE state != ? ORDER BY created_at ASC, id ASC`, string(fsp.StateClosed))
	if err != nil {
		return nil, fmt.Errorf("store: active sessions: %w", err)
	}
	return collectSessions(rows)
}

type scanner interface{ Scan(dest ...any) error }

func scanSession(sc scanner) (fsp.Session, error) {
	var (
		s                     fsp.Session
		id, driver, origin    string
		state, owner          string
		createdAt, lastActive int64
	)
	if err := sc.Scan(&id, &driver, &s.Agent, &s.Cwd, &s.AgentSessionID,
		&origin, &state, &owner, &createdAt, &lastActive); err != nil {
		return fsp.Session{}, err
	}
	ep, err := fsp.ParseEndpoint(owner)
	if err != nil {
		return fsp.Session{}, err
	}
	s.ID = fsp.SessionID(id)
	s.Driver = fsp.DriverKind(driver)
	s.Origin = fsp.SessionOrigin(origin)
	s.State = fsp.SessionState(state)
	s.Owner = ep
	s.CreatedAt = fromMillis(createdAt)
	s.LastActive = fromMillis(lastActive)
	return s, nil
}

func collectSessions(rows *sql.Rows) ([]fsp.Session, error) {
	defer rows.Close()
	var out []fsp.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan sessions: %w", err)
	}
	return out, nil
}
