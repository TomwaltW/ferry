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

func (d *DB) SetRole(ctx context.Context, ep fsp.EndpointID, senderID, role string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO acl (endpoint, sender_id, role) VALUES (?, ?, ?)
		ON CONFLICT(endpoint, sender_id) DO UPDATE SET role = excluded.role`,
		ep.String(), senderID, role)
	if err != nil {
		return fmt.Errorf("store: set role for %s: %w", senderID, err)
	}
	return nil
}

// Role returns "" when the sender has no role at the endpoint.
func (d *DB) Role(ctx context.Context, ep fsp.EndpointID, senderID string) (string, error) {
	var role string
	err := d.db.QueryRowContext(ctx,
		`SELECT role FROM acl WHERE endpoint = ? AND sender_id = ?`, ep.String(), senderID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: role of %s: %w", senderID, err)
	}
	return role, nil
}

func (d *DB) AddPairing(ctx context.Context, p ports.Pairing) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO pairing (code, endpoint, sender_id, expires_at) VALUES (?, ?, ?, ?)`,
		p.Code, p.Endpoint.String(), p.SenderID, millis(p.ExpiresAt))
	if err != nil {
		return fmt.Errorf("store: add pairing: %w", err)
	}
	return nil
}

// TakePairing removes the pairing and returns it. An unknown or expired code
// returns fsp.ErrNotFound; an expired row is removed on the way out.
func (d *DB) TakePairing(ctx context.Context, code string, now time.Time) (ports.Pairing, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", err)
	}
	defer tx.Rollback()

	var (
		endpoint  string
		senderID  string
		expiresAt int64
	)
	err = tx.QueryRowContext(ctx,
		`SELECT endpoint, sender_id, expires_at FROM pairing WHERE code = ?`, code).
		Scan(&endpoint, &senderID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", fsp.ErrNotFound)
	}
	if err != nil {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pairing WHERE code = ?`, code); err != nil {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", err)
	}
	if expiresAt <= millis(now) {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", fsp.ErrNotFound)
	}
	ep, err := fsp.ParseEndpoint(endpoint)
	if err != nil {
		return ports.Pairing{}, fmt.Errorf("store: take pairing: %w", err)
	}
	return ports.Pairing{
		Code:      code,
		Endpoint:  ep,
		SenderID:  senderID,
		ExpiresAt: fromMillis(expiresAt),
	}, nil
}

// CountPairings counts the codes at the endpoint that have not expired yet.
func (d *DB) CountPairings(ctx context.Context, ep fsp.EndpointID, now time.Time) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pairing WHERE endpoint = ? AND expires_at > ?`, ep.String(), millis(now)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count pairings for %s: %w", ep, err)
	}
	return n, nil
}

// DeleteOwnersExcept removes the owner rows SetOwner wrote on the bot, the ones
// at the owner's own endpoint "<platform>:<bot>:<sender>", for every sender
// but keep; an empty keep removes them all. The endpoint is compared whole,
// so a "%" or "_" in a bot ID stands only for itself.
func (d *DB) DeleteOwnersExcept(ctx context.Context, platform fsp.Platform, botID, keep string) (int, error) {
	prefix := string(platform) + ":" + botID + ":"
	res, err := d.db.ExecContext(ctx, `
		DELETE FROM acl
		WHERE role = 'owner' AND endpoint = ? || sender_id AND (? = '' OR sender_id <> ?)`,
		prefix, keep, keep)
	if err != nil {
		return 0, fmt.Errorf("store: delete owners of %s:%s: %w", platform, botID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete owners of %s:%s: %w", platform, botID, err)
	}
	return int(n), nil
}

func (d *DB) Audit(ctx context.Context, e ports.AuditEntry) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO audit (at, kind, endpoint, sender_id, session_id, detail)
		VALUES (?, ?, ?, ?, ?, ?)`,
		millis(e.At), e.Kind, endpointString(e.Endpoint), e.SenderID, string(e.Session), e.Detail)
	if err != nil {
		return fmt.Errorf("store: audit %s: %w", e.Kind, err)
	}
	return nil
}

// endpointString keeps the zero endpoint out of the audit log as an empty
// string rather than "::".
func endpointString(ep fsp.EndpointID) string {
	if ep == (fsp.EndpointID{}) {
		return ""
	}
	return ep.String()
}
