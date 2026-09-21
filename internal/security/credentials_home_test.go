package security

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"

	"github.com/zalando/go-keyring"
)

// homeDefault points the user's home directory at a temp dir, so the default
// FERRY_HOME never depends on the machine running the tests, and returns it.
// Windows reads USERPROFILE, everything else HOME.
func homeDefault(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	return filepath.Join(tmp, ".ferry")
}

// homeSeedLegacy stores creds() under the bare platform account, the way
// v0.1.0 did, and returns the stored blob.
func homeSeedLegacy(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(creds())
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := keyring.Set(keyringService, string(fsp.PlatformWeChat), string(raw)); err != nil {
		t.Fatalf("keyring.Set legacy: %v", err)
	}
	return string(raw)
}

func TestCredentialsPerHome(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	a, b := t.TempDir(), t.TempDir()
	if err := SaveCredentials(a, fsp.PlatformWeChat, creds()); err != nil {
		t.Fatalf("SaveCredentials a: %v", err)
	}
	if _, err := LoadCredentials(b, fsp.PlatformWeChat); !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("LoadCredentials b error = %v, want fsp.ErrNotFound", err)
	}

	other := creds()
	other.BotToken = "sk-othervalue"
	other.BotID = "bot2"
	if err := SaveCredentials(b, fsp.PlatformWeChat, other); err != nil {
		t.Fatalf("SaveCredentials b: %v", err)
	}
	if err := DeleteCredentials(a, fsp.PlatformWeChat); err != nil {
		t.Fatalf("DeleteCredentials a: %v", err)
	}
	got, err := LoadCredentials(b, fsp.PlatformWeChat)
	if err != nil {
		t.Fatalf("LoadCredentials b after deleting a: %v", err)
	}
	if got != other {
		t.Errorf("LoadCredentials b = %+v, want %+v", got, other)
	}
}

func TestCredentialsMigrateDefaultHome(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	home := homeDefault(t)
	legacy := homeSeedLegacy(t)

	got, err := LoadCredentials(home, fsp.PlatformWeChat)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got != creds() {
		t.Errorf("LoadCredentials = %+v, want %+v", got, creds())
	}
	blob, err := keyring.Get(keyringService, keyringAccount(home, fsp.PlatformWeChat))
	if err != nil {
		t.Fatalf("new keyring account after migration: %v", err)
	}
	if blob != legacy {
		t.Errorf("new keyring account holds %q, want %q", blob, legacy)
	}
	blob, err = keyring.Get(keyringService, string(fsp.PlatformWeChat))
	if err != nil {
		t.Fatalf("legacy keyring account after migration: %v", err)
	}
	if blob != legacy {
		t.Errorf("legacy keyring account holds %q, want %q", blob, legacy)
	}
}

func TestCredentialsNoMigrateOtherHome(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	homeDefault(t)
	legacy := homeSeedLegacy(t)
	other := t.TempDir()

	if _, err := LoadCredentials(other, fsp.PlatformWeChat); !errors.Is(err, fsp.ErrNotFound) {
		t.Fatalf("LoadCredentials in another home error = %v, want fsp.ErrNotFound", err)
	}
	blob, err := keyring.Get(keyringService, string(fsp.PlatformWeChat))
	if err != nil {
		t.Fatalf("legacy keyring account after load: %v", err)
	}
	if blob != legacy {
		t.Errorf("legacy keyring account holds %q, want %q", blob, legacy)
	}

	// Logging out elsewhere must not sign out a default home that has not
	// migrated yet.
	if err := DeleteCredentials(other, fsp.PlatformWeChat); err != nil {
		t.Fatalf("DeleteCredentials in another home: %v", err)
	}
	blob, err = keyring.Get(keyringService, string(fsp.PlatformWeChat))
	if err != nil {
		t.Fatalf("legacy keyring account after delete: %v", err)
	}
	if blob != legacy {
		t.Errorf("legacy keyring account holds %q after delete, want %q", blob, legacy)
	}
}

func TestLogoutDefaultHomeClearsLegacy(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	home := homeDefault(t)
	homeSeedLegacy(t)

	if _, err := LoadCredentials(home, fsp.PlatformWeChat); err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if err := DeleteCredentials(home, fsp.PlatformWeChat); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := LoadCredentials(home, fsp.PlatformWeChat); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("LoadCredentials after logout error = %v, want fsp.ErrNotFound", err)
	}
	if _, err := keyring.Get(keyringService, string(fsp.PlatformWeChat)); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("legacy keyring account after logout error = %v, want keyring.ErrNotFound", err)
	}
}

func TestCredentialAccountHidesHome(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	acct := keyringAccount("/x/y", fsp.PlatformWeChat)
	if !regexp.MustCompile(`^wechat@[0-9a-f]{16}$`).MatchString(acct) {
		t.Errorf("keyringAccount = %q, want wechat@ and 16 hex digits", acct)
	}
	if strings.Contains(acct, "/x/y") {
		t.Errorf("keyringAccount = %q spells out the home", acct)
	}
	for _, spelling := range []string{"/x/y/", "/x/./y"} {
		if got := keyringAccount(spelling, fsp.PlatformWeChat); got != acct {
			t.Errorf("keyringAccount(%q) = %q, want %q", spelling, got, acct)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keyringAccount("y", fsp.PlatformWeChat), keyringAccount(filepath.Join(wd, "y"), fsp.PlatformWeChat); got != want {
		t.Errorf("keyringAccount(%q) = %q, want %q like its absolute path", "y", got, want)
	}
}
