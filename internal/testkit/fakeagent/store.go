package fakeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// errNoSession reports a session id that has no file under <home>/sessions.
var errNoSession = errors.New("fakeagent: unknown session")

// turn is one entry of a session's history. Role is either "user" or "agent";
// the two roles replay as user_message_chunk and agent_message_chunk.
type turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

const (
	roleUser  = "user"
	roleAgent = "agent"
)

// record is the on-disk shape of <home>/sessions/<id>.json.
type record struct {
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
	History   []turn `json:"history"`
}

// store persists sessions as one JSON file each under dir.
type store struct{ dir string }

// newStore creates <home>/sessions and returns a store rooted there.
func newStore(home string) (*store, error) {
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("fakeagent: create %s: %w", dir, err)
	}
	return &store{dir: dir}, nil
}

// validID rejects ids that would escape the sessions directory. Session ids
// arrive from the peer on load, so they are never trusted as path elements.
func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

func (s *store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *store) save(r record) error {
	if !validID(r.ID) {
		return fmt.Errorf("fakeagent: save %q: %w", r.ID, errNoSession)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("fakeagent: encode session %s: %w", r.ID, err)
	}
	if err := os.WriteFile(s.path(r.ID), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("fakeagent: write session %s: %w", r.ID, err)
	}
	return nil
}

func (s *store) load(id string) (record, error) {
	if !validID(id) {
		return record{}, fmt.Errorf("fakeagent: load %q: %w", id, errNoSession)
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return record{}, fmt.Errorf("fakeagent: load %q: %w", id, errNoSession)
		}
		return record{}, fmt.Errorf("fakeagent: read session %s: %w", id, err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, fmt.Errorf("fakeagent: decode session %s: %w", id, err)
	}
	return r, nil
}

// list returns every stored session, newest first, with ties broken by id so
// the order is stable across runs. cwd, when non-empty, keeps only the
// sessions opened in that working directory.
func (s *store) list(cwd string) ([]record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("fakeagent: read %s: %w", s.dir, err)
	}
	out := make([]record, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := s.load(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, err
		}
		if cwd != "" && r.Cwd != cwd {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
