package pty

import "strings"

// What a wrapped program writes is meant for a terminal, not for a chat window.
// Turning one into the other means dropping the control sequences that paint the
// screen, honouring the carriage returns that overwrite what was already there,
// and leaving out the echo of the input Ferry itself typed.

// stripANSI removes the escape sequences a terminal would have acted on rather
// than shown: the CSI sequences (colours, cursor moves, the bracketed paste
// markers), the OSC ones that set the window title, and the short two byte
// escapes. Everything else is kept as it is.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			// A lone BEL is a bell, not text.
			if c != 0x07 {
				b.WriteByte(c)
			}
			i++
			continue
		}
		i += skipEscape(s[i:])
	}
	return b.String()
}

// skipEscape reports how many bytes the escape sequence at the start of s takes.
// An escape that never ends swallows the rest of the string, which is what a
// terminal would do with it too.
func skipEscape(s string) int {
	if len(s) < 2 {
		return len(s)
	}

	switch s[1] {
	case '[':
		// CSI: parameter and intermediate bytes, then one final byte in 0x40 to
		// 0x7e. The bracketed paste markers are two of these.
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)

	case ']', 'P', 'X', '^', '_':
		// A string sequence, ended by BEL or by the two byte ST.
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)

	default:
		// Everything else is escape plus one byte, such as the keypad modes.
		return 2
	}
}

// applyCR plays back the carriage returns in one line, so that a progress bar
// that rewrote itself in place reads as whatever it ended up saying rather than
// as every state it went through.
func applyCR(line string) string {
	out := make([]rune, 0, len(line))
	col := 0
	for _, r := range line {
		switch r {
		case '\r':
			col = 0
		case '\b':
			if col > 0 {
				col--
			}
		default:
			if col < len(out) {
				out[col] = r
			} else {
				out = append(out, r)
			}
			col++
		}
	}
	return string(out)
}

// cleanOutput turns what the program wrote after an injection into the text the
// chat gets: no escape sequences, no rewritten lines, and without the echo of
// the line Ferry typed.
func cleanOutput(raw, echo string) string {
	clean := stripANSI(raw)
	clean = strings.ReplaceAll(clean, "\r\n", "\n")

	lines := strings.Split(clean, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		line = strings.TrimRight(applyCR(line), " \t")
		// The terminal echoes the injected line back before the program has
		// said anything. Only the first one goes: a program that repeats the
		// input on purpose is reporting, not echoing.
		if !dropped && echo != "" && strings.TrimSpace(line) == strings.TrimSpace(echo) {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}

	return strings.Trim(strings.Join(kept, "\n"), "\n")
}

// lineCap bounds the line being tracked, for the program that writes a very long
// line without ever ending it.
const lineCap = 4096

// lastLine follows the cursor across a chunk of output and returns the line it
// ends on, which is what a prompt pattern is matched against. prev is where the
// previous chunk left off, already cleaned.
func lastLine(prev string, chunk []byte) string {
	text := stripANSI(prev + string(chunk))
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	text = applyCR(text)
	if len(text) > lineCap {
		text = text[len(text)-lineCap:]
	}
	return text
}
