package outbox

import (
	"strings"
	"unicode/utf8"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// segments cuts text into at most maxParts messages. Anything that does not
// fit becomes a truncated last message carrying outbox.truncated.
func (b *box) segments(ep fsp.EndpointID, text string, maxParts int) []string {
	limit := b.cfg.LongOutputThreshold
	parts := splitText(text, limit)
	if maxParts <= 0 || len(parts) <= maxParts {
		return parts
	}
	rest := []rune(strings.Join(parts[maxParts-1:], "\n"))
	// Render the notice with the largest count it could carry, so the room
	// reserved for it can only ever be too generous, never too small.
	keep := limit - utf8.RuneCountInString(b.truncatedNotice(ep, len(rest))) - 1
	if keep < 0 {
		keep = 0
	}
	if keep > len(rest) {
		keep = len(rest)
	}
	out := make([]string, 0, maxParts)
	out = append(out, parts[:maxParts-1]...)
	return append(out, string(rest[:keep])+"\n"+b.truncatedNotice(ep, len(rest)-keep))
}

func (b *box) truncatedNotice(ep fsp.EndpointID, dropped int) string {
	const key = "outbox.truncated"
	if b.i18n == nil {
		return key
	}
	return b.i18n.T(b.lang(ep), key, map[string]any{"N": dropped})
}

// atom is the smallest unit splitText will not break: a paragraph, or a line
// of an over-long paragraph, or a slice of an over-long line. sep is the
// separator that preceded it in the original text.
type atom struct{ sep, text string }

// splitText cuts text on paragraph boundaries, then on line boundaries, then
// mid-line, so that no piece is longer than limit runes. A limit of zero or
// less means no splitting.
func splitText(text string, limit int) []string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	var atoms []atom
	for i, para := range strings.Split(text, "\n\n") {
		sep := "\n\n"
		if i == 0 {
			sep = ""
		}
		if utf8.RuneCountInString(para) <= limit {
			atoms = append(atoms, atom{sep: sep, text: para})
			continue
		}
		for j, line := range strings.Split(para, "\n") {
			lineSep := sep
			if j > 0 {
				lineSep = "\n"
			}
			if utf8.RuneCountInString(line) <= limit {
				atoms = append(atoms, atom{sep: lineSep, text: line})
				continue
			}
			for k, chunk := range hardSplit(line, limit) {
				chunkSep := lineSep
				if k > 0 {
					chunkSep = ""
				}
				atoms = append(atoms, atom{sep: chunkSep, text: chunk})
			}
		}
	}

	var out []string
	cur := ""
	first := true
	for _, a := range atoms {
		if first {
			cur, first = a.text, false
			continue
		}
		joined := cur + a.sep + a.text
		if utf8.RuneCountInString(joined) <= limit {
			cur = joined
			continue
		}
		out = append(out, cur)
		cur = a.text
	}
	if !first {
		out = append(out, cur)
	}
	return out
}

// hardSplit cuts a single over-long line into limit-sized runs of runes.
func hardSplit(line string, limit int) []string {
	r := []rune(line)
	out := make([]string, 0, len(r)/limit+1)
	for len(r) > limit {
		out = append(out, string(r[:limit]))
		r = r[limit:]
	}
	return append(out, string(r))
}
