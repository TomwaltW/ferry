package security

import (
	"fmt"
	"strings"
	"unicode"
)

// maxBlankLines is how many consecutive blank lines survive.
const maxBlankLines = 2

// SanitizeForIM strips control and bidi characters and shortens s to max
// runes. A max of zero or less leaves the length alone.
func SanitizeForIM(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = stripUnsafe(s)
	s = collapseBlankLines(s)
	return truncateMiddle(s, max)
}

// stripUnsafe drops control characters other than newline and tab, and the
// bidi overrides that let text lie about its own order.
func stripUnsafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return -1
		case r >= 0x202A && r <= 0x202E: // LRE RLE PDF LRO RLO
			return -1
		case r >= 0x2066 && r <= 0x2069: // LRI RLI FSI PDI
			return -1
		}
		return r
	}, s)
}

// collapseBlankLines squeezes runs of three or more blank lines down to two.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blanks++
			if blanks > maxBlankLines {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// truncateMiddle keeps the opening 70% and the closing 20% of the budget and
// says how much went missing in between.
func truncateMiddle(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 70 / 100
	tail := max * 20 / 100
	dropped := len(r) - head - tail
	return string(r[:head]) + fmt.Sprintf("…[+%d]…", dropped) + string(r[len(r)-tail:])
}
