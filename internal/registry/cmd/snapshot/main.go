// Command snapshot regenerates internal/registry/snapshot.json from the
// official ACP registry. It is run by hand when the presets need refreshing;
// nothing in Ferry fetches the registry at run time.
//
// Usage:
//
//	go run ./internal/registry/cmd/snapshot -out internal/registry/snapshot.json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/TomwaltW/ferry/internal/registry"
)

const defaultURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	url := fs.String("url", defaultURL, "registry URL to read")
	out := fs.String("out", "internal/registry/snapshot.json", "file to write")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to wait for the registry")
	if err := fs.Parse(args); err != nil {
		return err
	}

	body, err := fetch(*url, *timeout)
	if err != nil {
		return err
	}
	filtered, err := filter(body)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, filtered, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s with %d agents\n", *out, len(registry.SnapshotIDs))
	return nil
}

func fetch(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return body, nil
}

// upstream mirrors just enough of the registry format to filter it. Agents stay
// raw so every field the registry publishes survives into the snapshot.
type upstream struct {
	Version string            `json:"version"`
	Agents  []json.RawMessage `json:"agents"`
}

// filter keeps the agents named by registry.SnapshotIDs, in that order, and
// fails if the registry has stopped publishing any of them.
func filter(body []byte) ([]byte, error) {
	var up upstream
	if err := json.Unmarshal(body, &up); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}

	byID := make(map[string]json.RawMessage, len(up.Agents))
	for _, raw := range up.Agents {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, fmt.Errorf("parse an agent entry: %w", err)
		}
		byID[head.ID] = raw
	}

	kept := make([]json.RawMessage, 0, len(registry.SnapshotIDs))
	var missing []string
	for _, id := range registry.SnapshotIDs {
		raw, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		kept = append(kept, raw)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("registry no longer lists %v", missing)
	}
	if up.Version == "" {
		return nil, errors.New("registry has no version")
	}

	encoded, err := json.MarshalIndent(upstream{Version: up.Version, Agents: kept}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return append(encoded, '\n'), nil
}
