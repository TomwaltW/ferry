package ids

import (
	"strings"
	"testing"
)

func TestNewShape(t *testing.T) {
	id := New("sess")
	prefix, body, found := strings.Cut(id, "_")
	if !found {
		t.Fatalf("New returned %q, want a prefix and an underscore", id)
	}
	if prefix != "sess" {
		t.Errorf("prefix = %q, want %q", prefix, "sess")
	}
	if len(body) != idLen {
		t.Errorf("random part is %d characters, want %d", len(body), idLen)
	}
	if strings.ContainsFunc(body, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz234567", r)
	}) {
		t.Errorf("random part %q has characters outside lower-case base32", body)
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := New("id")
		if seen[id] {
			t.Fatalf("New returned %q twice", id)
		}
		seen[id] = true
	}
}
