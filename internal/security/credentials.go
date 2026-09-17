package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"

	"github.com/zalando/go-keyring"
)

// keyringService is the service name Ferry stores its secrets under. The user
// name is the platform, the secret is the JSON encoded credentials.
const keyringService = "ferry"

// credentialsPath is where credentials go when the system keyring is not
// available, for example on a headless Linux box with no secret service.
func credentialsPath(home string, platform fsp.Platform) string {
	return filepath.Join(home, "credentials", string(platform)+".json")
}

// SaveCredentials stores credentials in the system keyring, falling back to
// <home>/credentials/<platform>.json.
func SaveCredentials(home string, platform fsp.Platform, c ports.Credentials) error {
	blob, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("security: encode %s credentials: %w", platform, err)
	}
	if err := keyring.Set(keyringService, string(platform), string(blob)); err == nil {
		// Keep one source of truth: a copy left over from an earlier fallback
		// would otherwise go stale without anyone noticing.
		if err := os.Remove(credentialsPath(home, platform)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("security: drop the %s credentials file: %w", platform, err)
		}
		return nil
	}

	path := credentialsPath(home, platform)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("security: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return fmt.Errorf("security: write %s: %w", path, err)
	}
	// WriteFile leaves an existing file's mode alone, so pin it either way.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("security: set the mode of %s: %w", path, err)
	}
	return nil
}

// LoadCredentials returns fsp.ErrNotFound when nothing is stored.
func LoadCredentials(home string, platform fsp.Platform) (ports.Credentials, error) {
	if blob, err := keyring.Get(keyringService, string(platform)); err == nil {
		var c ports.Credentials
		if err := json.Unmarshal([]byte(blob), &c); err != nil {
			return ports.Credentials{}, fmt.Errorf("security: decode %s credentials from the keyring: %w", platform, err)
		}
		return c, nil
	}

	path := credentialsPath(home, platform)
	blob, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ports.Credentials{}, fmt.Errorf("security: %s credentials: %w", platform, fsp.ErrNotFound)
	}
	if err != nil {
		return ports.Credentials{}, fmt.Errorf("security: read %s: %w", path, err)
	}
	var c ports.Credentials
	if err := json.Unmarshal(blob, &c); err != nil {
		return ports.Credentials{}, fmt.Errorf("security: decode %s: %w", path, err)
	}
	return c, nil
}

// DeleteCredentials removes both copies. Credentials that were never stored
// are not an error.
func DeleteCredentials(home string, platform fsp.Platform) error {
	// A keyring that is missing or unavailable leaves nothing to delete.
	_ = keyring.Delete(keyringService, string(platform))
	path := credentialsPath(home, platform)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("security: remove %s: %w", path, err)
	}
	return nil
}
