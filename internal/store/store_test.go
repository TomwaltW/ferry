package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

var (
	epA = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userA"}
	epB = fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userB"}

	base = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
)

// openTest returns a store over a fresh database file and its path.
func openTest(t *testing.T) (ports.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ferry.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func session(id string, owner fsp.EndpointID, createdAt time.Time) fsp.Session {
	return fsp.Session{
		ID:         fsp.SessionID(id),
		Driver:     fsp.DriverACP,
		Agent:      "qwen-code",
		Cwd:        "/work/demo",
		Origin:     fsp.OriginManaged,
		State:      fsp.StateIdle,
		Owner:      owner,
		CreatedAt:  createdAt,
		LastActive: createdAt,
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ferry.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	ctx := context.Background()
	if err := st.SetKV(ctx, "k", "v"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Opening the same file again must re-run the migrations without error and
	// without dropping what is already there.
	for i := range 2 {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i+2, err)
		}
		got, err := st.GetKV(ctx, "k")
		if err != nil {
			t.Fatalf("GetKV after Open %d: %v", i+2, err)
		}
		if got != "v" {
			t.Errorf("after Open %d GetKV = %q, want %q", i+2, got, "v")
		}
		if err := st.Close(); err != nil {
			t.Fatalf("Close %d: %v", i+2, err)
		}
	}
}

func TestSessionsCRUD(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	// Insert out of chronological order to prove ListSessions sorts.
	second := session("sess_2", epA, base.Add(time.Minute))
	first := session("sess_1", epA, base)
	other := session("sess_3", epB, base.Add(2*time.Minute))
	for _, s := range []fsp.Session{second, first, other} {
		if err := st.PutSession(ctx, s); err != nil {
			t.Fatalf("PutSession %s: %v", s.ID, err)
		}
	}

	got, err := st.GetSession(ctx, "sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Agent != "qwen-code" || got.Owner != epA || !got.CreatedAt.Equal(base) {
		t.Errorf("GetSession = %+v, want agent qwen-code owned by %s at %s", got, epA, base)
	}
	if got.Driver != fsp.DriverACP || got.State != fsp.StateIdle || got.Origin != fsp.OriginManaged {
		t.Errorf("GetSession = %+v, want driver/state/origin round-tripped", got)
	}

	if _, err := st.GetSession(ctx, "sess_missing"); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("GetSession(missing) error = %v, want fsp.ErrNotFound", err)
	}

	// Put by the same ID overwrites.
	updated := first
	updated.State = fsp.StateRunning
	updated.AgentSessionID = "agent-7"
	if err := st.PutSession(ctx, updated); err != nil {
		t.Fatalf("PutSession overwrite: %v", err)
	}
	got, err = st.GetSession(ctx, "sess_1")
	if err != nil {
		t.Fatalf("GetSession after overwrite: %v", err)
	}
	if got.State != fsp.StateRunning || got.AgentSessionID != "agent-7" {
		t.Errorf("after overwrite = %+v, want state running and agent session agent-7", got)
	}

	list, err := st.ListSessions(ctx, epA)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 || list[0].ID != "sess_1" || list[1].ID != "sess_2" {
		t.Fatalf("ListSessions = %v, want sess_1 then sess_2", idsOf(list))
	}

	// ActiveSessions covers every endpoint and skips closed sessions.
	closed := other
	closed.State = fsp.StateClosed
	if err := st.PutSession(ctx, closed); err != nil {
		t.Fatalf("PutSession closed: %v", err)
	}
	active, err := st.ActiveSessions(ctx)
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	if len(active) != 2 || active[0].ID != "sess_1" || active[1].ID != "sess_2" {
		t.Errorf("ActiveSessions = %v, want sess_1 then sess_2", idsOf(active))
	}
}

func idsOf(list []fsp.Session) []fsp.SessionID {
	out := make([]fsp.SessionID, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

func TestBindingsSlots(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	for _, id := range []string{"sess_1", "sess_2", "sess_3"} {
		if err := st.PutSession(ctx, session(id, epA, base)); err != nil {
			t.Fatalf("PutSession %s: %v", id, err)
		}
	}

	for i, id := range []fsp.SessionID{"sess_1", "sess_2", "sess_3"} {
		slot, err := st.Bind(ctx, epA, id)
		if err != nil {
			t.Fatalf("Bind %s: %v", id, err)
		}
		if want := i + 1; slot != want {
			t.Errorf("Bind %s slot = %d, want %d", id, slot, want)
		}
	}

	// Re-binding is idempotent rather than allocating a second slot.
	slot, err := st.Bind(ctx, epA, "sess_2")
	if err != nil {
		t.Fatalf("Bind again: %v", err)
	}
	if slot != 2 {
		t.Errorf("re-Bind slot = %d, want 2", slot)
	}

	// Freeing slot 2 makes it the lowest free slot again.
	if err := st.Unbind(ctx, epA, "sess_2"); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if err := st.PutSession(ctx, session("sess_4", epA, base)); err != nil {
		t.Fatalf("PutSession sess_4: %v", err)
	}
	slot, err = st.Bind(ctx, epA, "sess_4")
	if err != nil {
		t.Fatalf("Bind sess_4: %v", err)
	}
	if slot != 2 {
		t.Errorf("Bind into the gap gave slot %d, want 2", slot)
	}

	if err := st.SetForeground(ctx, epA, "sess_3"); err != nil {
		t.Fatalf("SetForeground: %v", err)
	}
	if err := st.SetForeground(ctx, epA, "sess_1"); err != nil {
		t.Fatalf("SetForeground again: %v", err)
	}

	list, err := st.Bindings(ctx, epA)
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("Bindings = %+v, want 3 rows", list)
	}
	for i, b := range list {
		if want := i + 1; b.Slot != want {
			t.Errorf("Bindings[%d].Slot = %d, want %d", i, b.Slot, want)
		}
		if b.Endpoint != epA {
			t.Errorf("Bindings[%d].Endpoint = %s, want %s", i, b.Endpoint, epA)
		}
		wantFG := b.Session == "sess_1"
		if b.Foreground != wantFG {
			t.Errorf("Bindings[%d] (%s).Foreground = %v, want %v", i, b.Session, b.Foreground, wantFG)
		}
	}

	if err := st.SetForeground(ctx, epA, "sess_missing"); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("SetForeground(unbound) error = %v, want fsp.ErrNotFound", err)
	}

	// Slots are counted per endpoint.
	if err := st.PutSession(ctx, session("sess_5", epB, base)); err != nil {
		t.Fatalf("PutSession sess_5: %v", err)
	}
	slot, err = st.Bind(ctx, epB, "sess_5")
	if err != nil {
		t.Fatalf("Bind at other endpoint: %v", err)
	}
	if slot != 1 {
		t.Errorf("first slot at a second endpoint = %d, want 1", slot)
	}
}

func TestSeenMessage(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	seen, err := st.SeenMessage(ctx, epA, "m1", base)
	if err != nil {
		t.Fatalf("SeenMessage: %v", err)
	}
	if seen {
		t.Error("first SeenMessage = true, want false")
	}

	seen, err = st.SeenMessage(ctx, epA, "m1", base.Add(time.Second))
	if err != nil {
		t.Fatalf("SeenMessage again: %v", err)
	}
	if !seen {
		t.Error("repeated SeenMessage = false, want true")
	}

	// The same ID at another endpoint is a different message.
	seen, err = st.SeenMessage(ctx, epB, "m1", base)
	if err != nil {
		t.Fatalf("SeenMessage at other endpoint: %v", err)
	}
	if seen {
		t.Error("SeenMessage at a second endpoint = true, want false")
	}

	// Past the 24 hour retention the record is pruned, so the ID looks new.
	seen, err = st.SeenMessage(ctx, epA, "m1", base.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("SeenMessage after the retention window: %v", err)
	}
	if seen {
		t.Error("SeenMessage after 25h = true, want false (record should be pruned)")
	}
}

func TestOutboxRows(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	msg := fsp.Outbound{
		Endpoint:  epA,
		Session:   "sess_1",
		Kind:      fsp.OutText,
		Text:      "hello",
		DedupeKey: "k1",
	}
	id1, err := st.AddOutbox(ctx, msg, base)
	if err != nil {
		t.Fatalf("AddOutbox: %v", err)
	}
	urgent := msg
	urgent.Text = "approve me"
	urgent.Kind = fsp.OutApproval
	urgent.DedupeKey = "k2"
	urgent.Urgent = true
	id2, err := st.AddOutbox(ctx, urgent, base.Add(time.Second))
	if err != nil {
		t.Fatalf("AddOutbox urgent: %v", err)
	}
	other := msg
	other.Endpoint = epB
	other.DedupeKey = "k3"
	if _, err := st.AddOutbox(ctx, other, base); err != nil {
		t.Fatalf("AddOutbox other endpoint: %v", err)
	}

	rows, err := st.OutboxByState(ctx, epA, fsp.DeliveryQueued)
	if err != nil {
		t.Fatalf("OutboxByState: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != id1 || rows[1].ID != id2 {
		t.Fatalf("OutboxByState = %+v, want rows %d then %d", rows, id1, id2)
	}
	if rows[0].Msg.Text != "hello" || rows[0].Msg.DedupeKey != "k1" || rows[0].Msg.Urgent {
		t.Errorf("row 1 = %+v, want the queued text message", rows[0].Msg)
	}
	if rows[0].Msg.Endpoint != epA || rows[0].Msg.Session != "sess_1" || rows[0].Msg.Kind != fsp.OutText {
		t.Errorf("row 1 = %+v, want endpoint, session and kind round-tripped", rows[0].Msg)
	}
	if !rows[1].Msg.Urgent || rows[1].Msg.Kind != fsp.OutApproval {
		t.Errorf("row 2 = %+v, want the urgent approval message", rows[1].Msg)
	}
	if rows[0].Attempts != 0 || rows[0].State != fsp.DeliveryQueued || !rows[0].CreatedAt.Equal(base) {
		t.Errorf("row 1 = %+v, want 0 attempts, queued, created at %s", rows[0], base)
	}

	if err := st.UpdateOutbox(ctx, id1, fsp.DeliveryAccepted, 2, base.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateOutbox: %v", err)
	}
	rows, err = st.OutboxByState(ctx, epA, fsp.DeliveryAccepted)
	if err != nil {
		t.Fatalf("OutboxByState accepted: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id1 || rows[0].Attempts != 2 {
		t.Fatalf("accepted rows = %+v, want row %d with 2 attempts", rows, id1)
	}

	if err := st.UpdateOutbox(ctx, 9999, fsp.DeliveryAccepted, 1, base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("UpdateOutbox(missing) error = %v, want fsp.ErrNotFound", err)
	}

	eps, err := st.OutboxEndpoints(ctx, fsp.DeliveryQueued)
	if err != nil {
		t.Fatalf("OutboxEndpoints: %v", err)
	}
	if len(eps) != 2 || eps[0] != epA || eps[1] != epB {
		t.Errorf("OutboxEndpoints = %v, want %s and %s", eps, epA, epB)
	}
}

func TestWindow(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	if _, err := st.LoadWindow(ctx, epA); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("LoadWindow(missing) error = %v, want fsp.ErrNotFound", err)
	}

	w := ports.Window{Endpoint: epA, ReplyCtx: "ctx-token", OpenedAt: base, Used: 1}
	if err := st.SaveWindow(ctx, w); err != nil {
		t.Fatalf("SaveWindow: %v", err)
	}
	got, err := st.LoadWindow(ctx, epA)
	if err != nil {
		t.Fatalf("LoadWindow: %v", err)
	}
	if got.Endpoint != epA || got.ReplyCtx != "ctx-token" || got.Used != 1 || !got.OpenedAt.Equal(base) {
		t.Errorf("LoadWindow = %+v, want %+v", got, w)
	}

	// A second save for the same endpoint replaces the row.
	w.ReplyCtx = "ctx-token-2"
	w.Used = 3
	w.OpenedAt = base.Add(time.Hour)
	if err := st.SaveWindow(ctx, w); err != nil {
		t.Fatalf("SaveWindow again: %v", err)
	}
	got, err = st.LoadWindow(ctx, epA)
	if err != nil {
		t.Fatalf("LoadWindow again: %v", err)
	}
	if got.ReplyCtx != "ctx-token-2" || got.Used != 3 || !got.OpenedAt.Equal(base.Add(time.Hour)) {
		t.Errorf("LoadWindow after overwrite = %+v, want %+v", got, w)
	}
}

func TestPairingExpiry(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	live := ports.Pairing{Code: "123456", Endpoint: epA, SenderID: "userA", ExpiresAt: base.Add(time.Hour)}
	dead := ports.Pairing{Code: "654321", Endpoint: epA, SenderID: "userC", ExpiresAt: base.Add(-time.Minute)}
	for _, p := range []ports.Pairing{live, dead} {
		if err := st.AddPairing(ctx, p); err != nil {
			t.Fatalf("AddPairing %s: %v", p.Code, err)
		}
	}

	n, err := st.CountPairings(ctx, epA, base)
	if err != nil {
		t.Fatalf("CountPairings: %v", err)
	}
	if n != 1 {
		t.Errorf("CountPairings = %d, want 1 (the expired code should not count)", n)
	}

	if _, err := st.TakePairing(ctx, "654321", base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("TakePairing(expired) error = %v, want fsp.ErrNotFound", err)
	}
	if _, err := st.TakePairing(ctx, "000000", base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("TakePairing(unknown) error = %v, want fsp.ErrNotFound", err)
	}

	got, err := st.TakePairing(ctx, "123456", base)
	if err != nil {
		t.Fatalf("TakePairing: %v", err)
	}
	if got.Endpoint != epA || got.SenderID != "userA" || !got.ExpiresAt.Equal(base.Add(time.Hour)) {
		t.Errorf("TakePairing = %+v, want %+v", got, live)
	}

	// Taking is a one-shot.
	if _, err := st.TakePairing(ctx, "123456", base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("second TakePairing error = %v, want fsp.ErrNotFound", err)
	}
	n, err = st.CountPairings(ctx, epA, base)
	if err != nil {
		t.Fatalf("CountPairings after take: %v", err)
	}
	if n != 0 {
		t.Errorf("CountPairings after take = %d, want 0", n)
	}
}

func TestKV(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	if _, err := st.GetKV(ctx, "missing"); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("GetKV(missing) error = %v, want fsp.ErrNotFound", err)
	}
	if err := st.SetKV(ctx, "wechat:bot1:cursor", "42"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	got, err := st.GetKV(ctx, "wechat:bot1:cursor")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if got != "42" {
		t.Errorf("GetKV = %q, want %q", got, "42")
	}
	if err := st.SetKV(ctx, "wechat:bot1:cursor", "43"); err != nil {
		t.Fatalf("SetKV overwrite: %v", err)
	}
	got, err = st.GetKV(ctx, "wechat:bot1:cursor")
	if err != nil {
		t.Fatalf("GetKV after overwrite: %v", err)
	}
	if got != "43" {
		t.Errorf("GetKV after overwrite = %q, want %q", got, "43")
	}
}

func TestRolesAndAudit(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	role, err := st.Role(ctx, epA, "userA")
	if err != nil {
		t.Fatalf("Role: %v", err)
	}
	if role != "" {
		t.Errorf("Role of an unknown sender = %q, want \"\"", role)
	}
	if err := st.SetRole(ctx, epA, "userA", "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := st.SetRole(ctx, epA, "userA", "member"); err != nil {
		t.Fatalf("SetRole overwrite: %v", err)
	}
	role, err = st.Role(ctx, epA, "userA")
	if err != nil {
		t.Fatalf("Role after overwrite: %v", err)
	}
	if role != "member" {
		t.Errorf("Role = %q, want %q", role, "member")
	}

	if err := st.Audit(ctx, ports.AuditEntry{At: base, Kind: "inbound", Endpoint: epA, SenderID: "userA", Detail: "len=12"}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// A system-wide entry carries no endpoint.
	if err := st.Audit(ctx, ports.AuditEntry{At: base, Kind: "login"}); err != nil {
		t.Fatalf("Audit without endpoint: %v", err)
	}
}

func TestApprovalRows(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	req := fsp.ApprovalRequest{
		ID:        "abcde",
		Session:   "sess_1",
		ToolName:  "write_file",
		ToolKind:  "edit",
		Paths:     []string{"/work/demo/a.txt"},
		CreatedAt: base,
	}
	if err := st.PutApproval(ctx, req); err != nil {
		t.Fatalf("PutApproval: %v", err)
	}
	// Storing the same ID again replaces the row rather than failing.
	if err := st.PutApproval(ctx, req); err != nil {
		t.Fatalf("PutApproval again: %v", err)
	}
	v := fsp.Verdict{ApprovalID: "abcde", Decision: fsp.DecisionAllowOnce, OptionID: "opt-1", By: "userA"}
	if err := st.DecideApproval(ctx, v, base.Add(time.Minute)); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	v.ApprovalID = "zzzzz"
	if err := st.DecideApproval(ctx, v, base); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("DecideApproval(missing) error = %v, want fsp.ErrNotFound", err)
	}
}
