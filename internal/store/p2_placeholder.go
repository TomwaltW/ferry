package store

import (
	"context"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Held messages and long-output files arrive with P2. The schema is already in
// migrations/002_p2.sql; the P2 T3 task fills these in.

func (d *DB) HoldOutbox(ctx context.Context, id int64, at time.Time) error {
	return fsp.ErrUnsupported
}

func (d *DB) AddLongOutput(ctx context.Context, lo ports.LongOutput) (int64, error) {
	return 0, fsp.ErrUnsupported
}

func (d *DB) LongOutputs(ctx context.Context, ep fsp.EndpointID, limit int) ([]ports.LongOutput, error) {
	return nil, fsp.ErrUnsupported
}

func (d *DB) DeleteLongOutput(ctx context.Context, id int64) error { return fsp.ErrUnsupported }

var _ ports.Store = (*DB)(nil)
