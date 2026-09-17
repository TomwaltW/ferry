package security

import (
	"regexp"
	"strings"
)

// maskTail is what replaces everything after the first four characters of a
// secret.
const maskTail = "****"

// tokenPatterns match secrets that are recognisable on their own.
var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{6,}`),         // OpenAI style keys
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{16,}`),  // fine-grained GitHub tokens
	regexp.MustCompile(`ghp_[A-Za-z0-9]{16,}`),          // classic GitHub tokens
	regexp.MustCompile(`xox[A-Za-z]-[A-Za-z0-9\-]{8,}`), // Slack tokens
	regexp.MustCompile(`AKIA[A-Z0-9]{12,}`),             // AWS access key IDs
}

// pemBlock captures the body of a PEM private key so that the BEGIN and END
// lines survive while the key material does not.
var pemBlock = regexp.MustCompile(`(?s)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----)(.*?)(-----END [A-Z0-9 ]*PRIVATE KEY-----)`)

// keyValue matches "password: hunter2", "api_key=abc" and their relatives. The
// value is the last group so that it sits at the end of the match.
// Longer names come first so that "access_token" does not match as "token".
var keyValue = regexp.MustCompile(`(?i)\b(?:client[_-]?secret|access[_-]?token|auth[_-]?token|api[_-]?key|access[_-]?key|private[_-]?key|apikey|password|passwd|secret|token|pass)\b["']?\s*[:=]\s*["']?([^\s"',;)]+)`)

// MaskSecrets keeps the first 4 characters of anything that looks like a
// secret and replaces the rest with ****.
func MaskSecrets(s string) string {
	s = pemBlock.ReplaceAllStringFunc(s, func(m string) string {
		parts := pemBlock.FindStringSubmatch(m)
		return parts[1] + "\n" + mask(strings.TrimSpace(parts[2])) + "\n" + parts[3]
	})
	for _, re := range tokenPatterns {
		s = re.ReplaceAllStringFunc(s, mask)
	}
	s = keyValue.ReplaceAllStringFunc(s, func(m string) string {
		value := keyValue.FindStringSubmatch(m)[1]
		// The value always ends the match, so everything before it is the key,
		// the separator and an optional opening quote.
		return m[:len(m)-len(value)] + mask(value)
	})
	return s
}

// mask keeps the first four runes of tok. Anything shorter is replaced whole,
// because four characters of a four character secret is the secret.
func mask(tok string) string {
	r := []rune(tok)
	if len(r) <= 4 {
		return maskTail
	}
	return string(r[:4]) + maskTail
}
