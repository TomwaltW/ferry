package store

import (
	"context"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// openAudit returns a fresh store and its audit reader side.
func openAudit(t *testing.T) (ports.Store, ports.AuditReader) {
	t.Helper()
	st, _ := openTest(t)
	r, ok := st.(ports.AuditReader)
	if !ok {
		t.Fatalf("%T does not implement ports.AuditReader", st)
	}
	return st, r
}

func writeAudit(t *testing.T, st ports.Store, entries ...ports.AuditEntry) {
	t.Helper()
	for _, e := range entries {
		if err := st.Audit(context.Background(), e); err != nil {
			t.Fatalf("Audit %s: %v", e.Kind, err)
		}
	}
}

func auditDetails(entries []ports.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Detail
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAuditSinceOrder(t *testing.T) {
	st, r := openAudit(t)
	// Written out of time order; the three at base share one millisecond (the
	// sub-millisecond part is dropped on write) and must keep write order.
	writeAudit(t, st,
		ports.AuditEntry{At: base.Add(2 * time.Second), Kind: "inbound", Detail: "late"},
		ports.AuditEntry{At: base, Kind: "inbound", Detail: "same-1"},
		ports.AuditEntry{At: base.Add(300 * time.Microsecond), Kind: "inbound", Detail: "same-2"},
		ports.AuditEntry{At: base, Kind: "inbound", Detail: "same-3"},
		ports.AuditEntry{At: base.Add(time.Second), Kind: "inbound", Detail: "middle"},
	)

	got, err := r.AuditSince(context.Background(), base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("AuditSince: %v", err)
	}
	want := []string{"same-1", "same-2", "same-3", "middle", "late"}
	if d := auditDetails(got); !equalStrings(d, want) {
		t.Errorf("order = %v, want %v", d, want)
	}
}

func TestAuditSinceFilters(t *testing.T) {
	st, r := openAudit(t)
	writeAudit(t, st,
		ports.AuditEntry{At: base.Add(-time.Millisecond), Kind: "inbound", Detail: "before"},
		ports.AuditEntry{At: base, Kind: "inbound", Detail: "at"},
		ports.AuditEntry{At: base.Add(time.Millisecond), Kind: "inbound", Detail: "after"},
	)
	ctx := context.Background()

	tests := []struct {
		since time.Time
		want  []string
	}{
		{base.Add(-time.Hour), []string{"before", "at", "after"}},
		{base, []string{"at", "after"}}, // inclusive
		{base.Add(time.Millisecond), []string{"after"}},
		{base.Add(time.Hour), nil},
	}
	for _, tt := range tests {
		got, err := r.AuditSince(ctx, tt.since)
		if err != nil {
			t.Fatalf("AuditSince(%v): %v", tt.since, err)
		}
		if d := auditDetails(got); !equalStrings(d, tt.want) {
			t.Errorf("AuditSince(%v) = %v, want %v", tt.since, d, tt.want)
		}
	}

	// All six fields survive the round trip.
	full := ports.AuditEntry{
		At:       base.Add(time.Minute),
		Kind:     "approval_asked",
		Endpoint: fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "peer:with:colons"},
		SenderID: "userA",
		Session:  fsp.SessionID("s-1"),
		Detail:   "tool=shell",
	}
	writeAudit(t, st, full)
	got, err := r.AuditSince(ctx, full.At)
	if err != nil {
		t.Fatalf("AuditSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	g := got[0]
	if !g.At.Equal(full.At) || g.Kind != full.Kind || g.Endpoint != full.Endpoint ||
		g.SenderID != full.SenderID || g.Session != full.Session || g.Detail != full.Detail {
		t.Errorf("round trip = %+v, want %+v", g, full)
	}
}

func TestAuditSinceZeroEndpoint(t *testing.T) {
	st, r := openAudit(t)
	writeAudit(t, st,
		ports.AuditEntry{At: base, Kind: "login", Detail: "no endpoint"},
		ports.AuditEntry{At: base.Add(time.Second), Kind: "inbound", Endpoint: epA, Detail: "with endpoint"},
	)

	got, err := r.AuditSince(context.Background(), base)
	if err != nil {
		t.Fatalf("AuditSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Endpoint != (fsp.EndpointID{}) {
		t.Errorf("endpoint of a login entry = %+v, want the zero endpoint", got[0].Endpoint)
	}
	if got[1].Endpoint != epA {
		t.Errorf("endpoint = %+v, want %+v", got[1].Endpoint, epA)
	}
}
