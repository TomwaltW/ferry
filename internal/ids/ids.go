// Package ids generates the opaque identifiers used across Ferry.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

const idLen = 26

var alphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// New returns prefix, an underscore, and 26 random lower-case base32
// characters, for example "sess_k3m...". The randomness comes from crypto/rand.
func New(prefix string) string {
	// 26 base32 characters carry 130 bits, which needs 17 random bytes.
	var buf [17]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error())
	}
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteByte('_')
	b.WriteString(alphabet.EncodeToString(buf[:])[:idLen])
	return b.String()
}
