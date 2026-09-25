package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// TestOutboxByKey pins the lookup the approval module uses to tell whether its
// question got through, and when: the newest row of the endpoint queued under
// the key, with the time its state last changed.
func TestOutboxByKey(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	if _, err := st.OutboxByKey(ctx, epA, "approval-abcde-o-1"); !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("OutboxByKey on an empty outbox: err = %v, want ErrNotFound", err)
	}

	msg := fsp.Outbound{Endpoint: epA, Kind: fsp.OutApproval, Text: "first", DedupeKey: "approval-abcde-o-1", Urgent: true}
	first, err := st.AddOutbox(ctx, msg, base)
	if err != nil {
		t.Fatalf("AddOutbox: %v", err)
	}
	row, err := st.OutboxByKey(ctx, epA, msg.DedupeKey)
	if err != nil {
		t.Fatalf("OutboxByKey: %v", err)
	}
	if row.ID != first || row.State != fsp.DeliveryQueued || !row.UpdatedAt.Equal(base) || row.Msg.Text != "first" {
		t.Errorf("OutboxByKey = %+v, want row %d, queued, updated at %v", row, first, base)
	}

	delivered := base.Add(4 * time.Second)
	if err := st.UpdateOutbox(ctx, first, fsp.DeliveryAccepted, 1, delivered); err != nil {
		t.Fatalf("UpdateOutbox: %v", err)
	}
	row, err = st.OutboxByKey(ctx, epA, msg.DedupeKey)
	if err != nil {
		t.Fatalf("OutboxByKey after delivery: %v", err)
	}
	if row.State != fsp.DeliveryAccepted || !row.UpdatedAt.Equal(delivered) || !row.CreatedAt.Equal(base) {
		t.Errorf("OutboxByKey = %+v, want accepted at %v, created at %v", row, delivered, base)
	}

	// The newest row under the key wins, and other endpoints do not count.
	again := msg
	again.Text = "second"
	second, err := st.AddOutbox(ctx, again, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("AddOutbox again: %v", err)
	}
	other := msg
	other.Endpoint = epB
	if _, err := st.AddOutbox(ctx, other, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("AddOutbox other endpoint: %v", err)
	}
	row, err = st.OutboxByKey(ctx, epA, msg.DedupeKey)
	if err != nil {
		t.Fatalf("OutboxByKey with two rows: %v", err)
	}
	if row.ID != second || row.Msg.Text != "second" || row.Msg.Endpoint != epA {
		t.Errorf("OutboxByKey = %+v, want the newest row %d of %s", row, second, epA)
	}
}
