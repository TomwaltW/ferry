package ports

import (
	"context"
	"time"
)

// AuditReader reads the audit log back, for ferry audit export
// (spec-p4-b1 3.5). The SQLite store implements it beside Store, which keeps
// its method set so that no existing Store has to change.
type AuditReader interface {
	// AuditSince returns every entry written at or after since, oldest first;
	// entries of the same millisecond keep the order they were written in.
	// An entry written without an endpoint comes back with the zero one.
	AuditSince(ctx context.Context, since time.Time) ([]AuditEntry, error)
}
