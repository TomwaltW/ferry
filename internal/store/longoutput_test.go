package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// TestMigration002 checks that the P2 migration reached the database: the
// held_at column on outbox, the long_outputs table and its index.
func TestMigration002(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ferry.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.(*DB).db

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
	}

	rows, err := db.Query(`PRAGMA table_info(outbox)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	held := false
	for rows.Next() {
		// PRAGMA table_info: cid, name, type, notnull, dflt_value, pk.
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "held_at" {
			held = true
			if notNull != 1 || !dflt.Valid || dflt.String != "0" {
				t.Errorf("held_at column = notnull %d default %v, want NOT NULL DEFAULT 0", notNull, dflt)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	if !held {
		t.Error("outbox has no held_at column")
	}

	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'long_outputs'`).Scan(&name)
	if err != nil {
		t.Fatalf("long_outputs table: %v", err)
	}
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'long_outputs_endpoint'`).Scan(&name)
	if err != nil {
		t.Fatalf("long_outputs_endpoint index: %v", err)
	}
}

func TestHoldOutbox(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	id, err := st.AddOutbox(ctx, fsp.Outbound{Endpoint: epA, Session: "sess_1", Kind: fsp.OutText, Text: "later"}, base)
	if err != nil {
		t.Fatalf("AddOutbox: %v", err)
	}

	heldAt := base.Add(2 * time.Minute)
	if err := st.HoldOutbox(ctx, id, heldAt); err != nil {
		t.Fatalf("HoldOutbox: %v", err)
	}

	rows, err := st.OutboxByState(ctx, epA, fsp.DeliveryHeld)
	if err != nil {
		t.Fatalf("OutboxByState held: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].Msg.Text != "later" {
		t.Fatalf("held rows = %+v, want row %d", rows, id)
	}
	// The row left the queued state, so the send loop no longer sees it.
	queued, err := st.OutboxByState(ctx, epA, fsp.DeliveryQueued)
	if err != nil {
		t.Fatalf("OutboxByState queued: %v", err)
	}
	if len(queued) != 0 {
		t.Errorf("queued rows = %d, want 0 after holding", len(queued))
	}

	var got int64
	if err := st.(*DB).db.QueryRow(`SELECT held_at FROM outbox WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatalf("read held_at: %v", err)
	}
	if got != heldAt.UnixMilli() {
		t.Errorf("held_at = %d, want %d", got, heldAt.UnixMilli())
	}

	if err := st.HoldOutbox(ctx, 9999, base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("HoldOutbox(missing) = %v, want fsp.ErrNotFound", err)
	}
}

func TestLongOutputsCRUD(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	var ids []int64
	for i := range 3 {
		lo := ports.LongOutput{
			Endpoint:  epA,
			Session:   "sess_1",
			Path:      filepath.Join("/home", "out", string(rune('a'+i))+".md"),
			Chars:     100 * (i + 1),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		id, err := st.AddLongOutput(ctx, lo)
		if err != nil {
			t.Fatalf("AddLongOutput %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	// Another endpoint's file must never show up in epA's list.
	if _, err := st.AddLongOutput(ctx, ports.LongOutput{Endpoint: epB, Session: "sess_9", Path: "/home/out/z.md", Chars: 1, CreatedAt: base}); err != nil {
		t.Fatalf("AddLongOutput for epB: %v", err)
	}

	all, err := st.LongOutputs(ctx, epA, 0)
	if err != nil {
		t.Fatalf("LongOutputs: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("LongOutputs = %d rows, want 3", len(all))
	}
	// Newest first.
	if all[0].ID != ids[2] || all[2].ID != ids[0] {
		t.Errorf("LongOutputs order = %d %d %d, want newest first", all[0].ID, all[1].ID, all[2].ID)
	}
	if all[0].Chars != 300 || all[0].Session != "sess_1" || all[0].Endpoint != epA {
		t.Errorf("newest = %+v, want the third file round-tripped", all[0])
	}
	if !all[0].CreatedAt.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("created at %s, want %s", all[0].CreatedAt, base.Add(2*time.Minute))
	}

	two, err := st.LongOutputs(ctx, epA, 2)
	if err != nil {
		t.Fatalf("LongOutputs(2): %v", err)
	}
	if len(two) != 2 || two[0].ID != ids[2] || two[1].ID != ids[1] {
		t.Errorf("LongOutputs(2) = %+v, want the two newest", two)
	}

	if err := st.DeleteLongOutput(ctx, ids[2]); err != nil {
		t.Fatalf("DeleteLongOutput: %v", err)
	}
	all, err = st.LongOutputs(ctx, epA, 0)
	if err != nil {
		t.Fatalf("LongOutputs after delete: %v", err)
	}
	if len(all) != 2 || all[0].ID != ids[1] {
		t.Errorf("after delete = %+v, want two rows headed by %d", all, ids[1])
	}
	if err := st.DeleteLongOutput(ctx, ids[2]); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("DeleteLongOutput(missing) = %v, want fsp.ErrNotFound", err)
	}

	none, err := st.LongOutputs(ctx, fsp.EndpointID{Platform: fsp.PlatformFake, BotID: "b", PeerID: "p"}, 0)
	if err != nil {
		t.Fatalf("LongOutputs for an unknown endpoint: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown endpoint = %+v, want none", none)
	}
}
