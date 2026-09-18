package audit

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

func TestParseSince(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"7d", 7 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"12h", 12 * time.Hour},
		{"30m", 30 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"45s", 45 * time.Second},
	}
	for _, tt := range tests {
		got, err := ParseSince(tt.in)
		if err != nil {
			t.Errorf("ParseSince(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSince(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseSinceRejects(t *testing.T) {
	for _, in := range []string{
		"", "0", "0d", "0s", "-1h", "-7d", "+7d", "7x", "d", "7", "1.5d", "7d12h", " 7d", "99999999999999d",
	} {
		if d, err := ParseSince(in); !errors.Is(err, ErrSince) {
			t.Errorf("ParseSince(%q) = %v, %v; want ErrSince", in, d, err)
		}
	}
}

func TestCSVHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, nil); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if got, want := buf.String(), "at,kind,endpoint,sender_id,session_id,detail\n"; got != want {
		t.Errorf("empty export = %q, want %q", got, want)
	}
}

func TestCSVRowFormat(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	entries := []ports.AuditEntry{
		{
			// Local time with an offset and sub-millisecond noise.
			At:       time.Date(2026, 9, 18, 18, 4, 5, 123456789, cst),
			Kind:     "approval_asked",
			Endpoint: fsp.EndpointID{Platform: fsp.PlatformWeChat, BotID: "bot1", PeerID: "userA"},
			SenderID: "userA",
			Session:  fsp.SessionID("s-1"),
			Detail:   `tool="shell", cwd=/tmp`,
		},
		{
			At:     time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
			Kind:   "login",
			Detail: "line1\nline2",
		},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, entries); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	want := "at,kind,endpoint,sender_id,session_id,detail\n" +
		`2026-09-18T10:04:05.123Z,approval_asked,wechat:bot1:userA,userA,s-1,"tool=""shell"", cwd=/tmp"` + "\n" +
		"2026-09-18T10:00:00.000Z,login,,,,\"line1\nline2\"\n"
	if got := buf.String(); got != want {
		t.Errorf("export =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(buf.String(), "\r") {
		t.Error("export contains \\r; want plain \\n line endings")
	}
}

func TestCSVOnlySixColumns(t *testing.T) {
	entries := []ports.AuditEntry{
		{At: time.Unix(0, 0), Kind: "inbound", Detail: "a,b,c,d,e,f,g"},
		{At: time.Unix(1, 0), Kind: "outbound_failed", Endpoint: fsp.EndpointID{Platform: "feishu", BotID: "b", PeerID: "p:q"}},
		{},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, entries); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(records) != len(entries)+1 {
		t.Fatalf("got %d records, want %d", len(records), len(entries)+1)
	}
	for i, rec := range records {
		if len(rec) != 6 {
			t.Errorf("record %d has %d columns, want 6: %q", i, len(rec), rec)
		}
	}
	if got := records[2][2]; got != "feishu:b:p:q" {
		t.Errorf("endpoint column = %q, want %q", got, "feishu:b:p:q")
	}
}
