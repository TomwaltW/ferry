package wechat

import (
	"fmt"
	"regexp"
	"strings"
)

// WeChat renders no Markdown at all, so render=plain flattens it: headings
// become 【...】, emphasis and code markers go away, fenced code is indented by
// two spaces, links read "text (url)" and table rows are joined with " · ".
var (
	headingRe   = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	fenceRe     = regexp.MustCompile("^\\s*(?:```|~~~)")
	linkRe      = regexp.MustCompile(`!?\[([^\]]*)\]\(\s*([^)\s]*)[^)]*\)`)
	codeSpanRe  = regexp.MustCompile("`+([^`]*)`+")
	boldStarRe  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	boldLineRe  = regexp.MustCompile(`__(.+?)__`)
	italicStar  = regexp.MustCompile(`\*(.+?)\*`)
	italicLine  = regexp.MustCompile(`(^|[^\p{L}\p{N}_])_([^_\n]+)_([^\p{L}\p{N}_]|$)`)
	tableSepRe  = regexp.MustCompile(`^:?-+:?$`)
	codeIndent  = "  "
	placeholder = "\x00%d\x00"
)

// renderPlain turns Markdown into the flat text WeChat can show.
func renderPlain(s string) string {
	if s == "" {
		return ""
	}

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue // the fence itself is not part of the message
		}
		if inFence {
			out = append(out, codeIndent+line)
			continue
		}
		if cells, ok := tableCells(line); ok {
			if cells == nil {
				continue // the |---|---| separator carries no content
			}
			out = append(out, strings.Join(cells, " · "))
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, "【"+inline(m[2])+"】")
			continue
		}
		out = append(out, inline(line))
	}
	return strings.Join(out, "\n")
}

// tableCells splits a table row. The second result is false for an ordinary
// line; a nil slice marks the separator row between header and body.
func tableCells(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || len(trimmed) < 2 {
		return nil, false
	}
	parts := strings.Split(strings.Trim(trimmed, "|"), "|")

	cells := make([]string, 0, len(parts))
	separator := true
	for _, p := range parts {
		cell := strings.TrimSpace(p)
		if !tableSepRe.MatchString(cell) {
			separator = false
		}
		cells = append(cells, inline(cell))
	}
	if separator {
		return nil, true
	}
	return cells, true
}

// inline strips the markers that only make sense in a Markdown renderer. Links
// are pulled out first so that emphasis rules never touch a URL.
func inline(s string) string {
	var links []string
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := linkRe.FindStringSubmatch(m)
		text, target := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		switch {
		case target == "":
			links = append(links, text)
		case text == "":
			links = append(links, target)
		default:
			links = append(links, text+" ("+target+")")
		}
		return fmt.Sprintf(placeholder, len(links)-1)
	})

	s = codeSpanRe.ReplaceAllString(s, "$1")
	s = boldStarRe.ReplaceAllString(s, "$1")
	s = boldLineRe.ReplaceAllString(s, "$1")
	s = italicStar.ReplaceAllString(s, "$1")
	s = italicLine.ReplaceAllString(s, "$1$2$3")

	for i, link := range links {
		s = strings.ReplaceAll(s, fmt.Sprintf(placeholder, i), link)
	}
	return s
}
