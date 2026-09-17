package security

import (
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// SaveCredentials stores credentials in the system keyring, falling back to
// <home>/credentials/<platform>.json.
func SaveCredentials(home string, platform fsp.Platform, c ports.Credentials) error {
	panic("not implemented")
}

// LoadCredentials returns fsp.ErrNotFound when nothing is stored.
func LoadCredentials(home string, platform fsp.Platform) (ports.Credentials, error) {
	panic("not implemented")
}

func DeleteCredentials(home string, platform fsp.Platform) error { panic("not implemented") }

// MaskSecrets keeps the first 4 characters of anything that looks like a
// secret and replaces the rest with ****.
func MaskSecrets(s string) string { panic("not implemented") }

// SanitizeForIM strips control and bidi characters and shortens s to max runes.
func SanitizeForIM(s string, max int) string { panic("not implemented") }
