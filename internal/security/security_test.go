package security

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"

	"github.com/zalando/go-keyring"
)

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"openai key", "use sk-abcdefghijklmnop now", "use sk-a**** now"},
		{"github classic", "ghp_abcdefghijklmnopqrstuvwxyz01", "ghp_****"},
		{"github fine grained", "github_pat_11ABCDEFG0abcdefghijkl", "gith****"},
		{"slack", "xoxb-1234567890-abcdefghij", "xoxb****"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "AKIA****"},
		{"password pair", "password: hunter2secret", "password: hunt****"},
		{"api key pair", `api_key="abcdefgh"`, `api_key="abcd****"`},
		{"token pair", "token=deadbeefcafe", "token=dead****"},
		{"access token pair", "access_token = zyxwvutsrq", "access_token = zyxw****"},
		{"secret in json", `{"secret":"topsecretvalue"}`, `{"secret":"tops****"}`},
		{"short value", "password: abc", "password: ****"},
		{"nothing to hide", "just a plain sentence about sk and tokens", "just a plain sentence about sk and tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskSecrets(tt.in); got != tt.want {
				t.Errorf("MaskSecrets(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("pem block", func(t *testing.T) {
		in := "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEAxyz\nabcdefg\n-----END RSA PRIVATE KEY-----\nafter"
		got := MaskSecrets(in)
		for _, secret := range []string{"MIIEpAIBAAKCAQEAxyz", "abcdefg"} {
			if strings.Contains(got, secret) {
				t.Errorf("MaskSecrets kept key material %q:\n%s", secret, got)
			}
		}
		if !strings.Contains(got, "-----BEGIN RSA PRIVATE KEY-----") || !strings.Contains(got, "-----END RSA PRIVATE KEY-----") {
			t.Errorf("MaskSecrets dropped the PEM markers:\n%s", got)
		}
		if !strings.HasPrefix(got, "before\n") || !strings.HasSuffix(got, "\nafter") {
			t.Errorf("MaskSecrets disturbed the text around the block:\n%s", got)
		}
	})

	t.Run("mask is idempotent", func(t *testing.T) {
		once := MaskSecrets("api_key=sk-abcdefghijklmnop")
		if twice := MaskSecrets(once); twice != once {
			t.Errorf("MaskSecrets twice = %q, want %q", twice, once)
		}
	})
}

func TestSanitizeForIM(t *testing.T) {
	t.Run("control characters", func(t *testing.T) {
		got := SanitizeForIM("a\x00b\x07c\x1bd", 0)
		if got != "abcd" {
			t.Errorf("SanitizeForIM = %q, want %q", got, "abcd")
		}
	})

	t.Run("newlines and tabs survive", func(t *testing.T) {
		got := SanitizeForIM("a\tb\nc", 0)
		if got != "a\tb\nc" {
			t.Errorf("SanitizeForIM = %q, want %q", got, "a\tb\nc")
		}
	})

	t.Run("carriage returns", func(t *testing.T) {
		got := SanitizeForIM("a\r\nb", 0)
		if got != "a\nb" {
			t.Errorf("SanitizeForIM = %q, want %q", got, "a\nb")
		}
	})

	t.Run("bidi overrides", func(t *testing.T) {
		got := SanitizeForIM("a\u202Eb\u202Ac\u2066d\u2069e", 0)
		if got != "abcde" {
			t.Errorf("SanitizeForIM = %q, want %q", got, "abcde")
		}
	})

	t.Run("blank line runs", func(t *testing.T) {
		got := SanitizeForIM("a\n\n\n\n\n\nb", 0)
		if got != "a\n\n\nb" {
			t.Errorf("SanitizeForIM = %q, want %q (at most two blank lines)", got, "a\n\n\nb")
		}
		// Two blank lines are already within the limit.
		if got := SanitizeForIM("a\n\n\nb", 0); got != "a\n\n\nb" {
			t.Errorf("SanitizeForIM = %q, want it unchanged", got)
		}
	})

	t.Run("short text is untouched", func(t *testing.T) {
		in := strings.Repeat("a", 50)
		if got := SanitizeForIM(in, 50); got != in {
			t.Errorf("SanitizeForIM shortened text that already fits: %q", got)
		}
	})

	t.Run("long text keeps head and tail", func(t *testing.T) {
		in := strings.Repeat("a", 70) + strings.Repeat("b", 30)
		got := SanitizeForIM(in, 50)
		want := strings.Repeat("a", 35) + "…[+55]…" + strings.Repeat("b", 10)
		if got != want {
			t.Errorf("SanitizeForIM = %q, want %q", got, want)
		}
	})

	t.Run("length is counted in runes", func(t *testing.T) {
		in := strings.Repeat("中", 100)
		got := SanitizeForIM(in, 50)
		want := strings.Repeat("中", 35) + "…[+55]…" + strings.Repeat("中", 10)
		if got != want {
			t.Errorf("SanitizeForIM = %q, want %q", got, want)
		}
	})
}

func creds() ports.Credentials {
	return ports.Credentials{
		BotToken: "sk-secretvalue",
		BotID:    "bot1",
		OwnerID:  "userA",
		BaseURL:  "https://example.invalid",
	}
}

func TestCredentialsKeyring(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	home := t.TempDir()
	want := creds()
	if err := SaveCredentials(home, fsp.PlatformWeChat, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	// A working keyring keeps nothing on disk.
	path := filepath.Join(home, "credentials", "wechat.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials landed on disk at %s although the keyring works", path)
	}
	blob, err := keyring.Get(keyringService, string(fsp.PlatformWeChat))
	if err != nil {
		t.Fatalf("keyring.Get: %v", err)
	}
	if !strings.Contains(blob, "sk-secretvalue") {
		t.Errorf("keyring holds %q, want the credentials as JSON", blob)
	}

	got, err := LoadCredentials(home, fsp.PlatformWeChat)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got != want {
		t.Errorf("LoadCredentials = %+v, want %+v", got, want)
	}

	if err := DeleteCredentials(home, fsp.PlatformWeChat); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := LoadCredentials(home, fsp.PlatformWeChat); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("LoadCredentials after delete error = %v, want fsp.ErrNotFound", err)
	}
	// Deleting again is not an error.
	if err := DeleteCredentials(home, fsp.PlatformWeChat); err != nil {
		t.Errorf("second DeleteCredentials: %v", err)
	}
}

func TestCredentialsFileFallback(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring on this box"))
	t.Cleanup(keyring.MockInit)

	home := t.TempDir()
	want := creds()
	if err := SaveCredentials(home, fsp.PlatformWeChat, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	dir := filepath.Join(home, "credentials")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if runtime.GOOS != "windows" { // Windows has no Unix permission bits
		if got := di.Mode().Perm(); got != 0o700 {
			t.Errorf("directory mode = %o, want 700", got)
		}
	}
	path := filepath.Join(dir, "wechat.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("file mode = %o, want 600", got)
		}
	}

	got, err := LoadCredentials(home, fsp.PlatformWeChat)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got != want {
		t.Errorf("LoadCredentials = %+v, want %+v", got, want)
	}

	// Saving again over an existing file keeps the restrictive mode.
	if err := SaveCredentials(home, fsp.PlatformWeChat, want); err != nil {
		t.Fatalf("SaveCredentials again: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("file mode after a second save = %o, want 600", got)
		}
	}

	if err := DeleteCredentials(home, fsp.PlatformWeChat); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s survived DeleteCredentials", path)
	}
	if _, err := LoadCredentials(home, fsp.PlatformWeChat); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("LoadCredentials after delete error = %v, want fsp.ErrNotFound", err)
	}
}
