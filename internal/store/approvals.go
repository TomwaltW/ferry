package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

func (d *DB) PutApproval(ctx context.Context, req fsp.ApprovalRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("store: put approval %s: %w", req.ID, err)
	}
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO approvals (id, session_id, request, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			request = excluded.request,
			created_at = excluded.created_at`,
		req.ID, string(req.Session), string(body), millis(req.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: put approval %s: %w", req.ID, err)
	}
	return nil
}

func (d *DB) DecideApproval(ctx context.Context, v fsp.Verdict, at time.Time) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE approvals SET decision = ?, decided_by = ?, decided_at = ? WHERE id = ?`,
		string(v.Decision), v.By, millis(at), v.ApprovalID)
	if err != nil {
		return fmt.Errorf("store: decide approval %s: %w", v.ApprovalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: decide approval %s: %w", v.ApprovalID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: decide approval %s: %w", v.ApprovalID, fsp.ErrNotFound)
	}
	return nil
}
