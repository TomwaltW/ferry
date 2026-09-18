//go:build linux || darwin

package pty

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "hello world", "hello world"},
		{"colours go", "\x1b[31mred\x1b[0m", "red"},
		{"cursor moves go", "a\x1b[2Ab\x1b[1;5Hc", "abc"},
		{"the paste markers go", PasteStart + "echo hi" + PasteEnd, "echo hi"},
		{"a window title goes", "\x1b]0;my title\x07done", "done"},
		{"a title ended by ST goes", "\x1b]0;my title\x1b\\done", "done"},
		{"the keypad escapes go", "\x1b=\x1b>x", "x"},
		{"a bell is not text", "ding\x07", "ding"},
		{"an unfinished escape takes the rest", "text\x1b[3", "text"},
		{"newlines and tabs stay", "a\n\tb", "a\n\tb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripANSI(c.in); got != c.want {
				t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// A carriage return rewrites the line rather than adding to it, which is how
	// a progress bar ends up as its final state.
	crCases := []struct{ in, want string }{
		{"abc\rxy", "xyc"},
		{"loading...\rdone      ", "done      "},
		{"ab\bc", "ac"},
		{"no returns here", "no returns here"},
	}
	for _, c := range crCases {
		if got := applyCR(c.in); got != c.want {
			t.Errorf("applyCR(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// cleanOutput puts the two together and drops the echo of what was typed.
	raw := PasteStart + "echo hi" + PasteEnd + "\r\n\x1b[32mgot:hi\x1b[0m\r\n$ "
	// Trailing spaces go with the rest of the terminal's padding, so the prompt
	// the program left on screen comes through bare.
	if got, want := cleanOutput(raw, "echo hi"), "got:hi\n$"; got != want {
		t.Errorf("cleanOutput = %q, want %q", got, want)
	}
	// Only the echo goes: a program that repeats the input on purpose is
	// reporting, not echoing.
	if got, want := cleanOutput("echo hi\r\necho hi\r\n", "echo hi"), "echo hi"; got != want {
		t.Errorf("cleanOutput dropped too much: %q, want %q", got, want)
	}

	// The line the prompt pattern is matched against follows the cursor.
	if got, want := lastLine("", []byte("first\nsecond")), "second"; got != want {
		t.Errorf("lastLine = %q, want %q", got, want)
	}
	if got, want := lastLine("$ ", []byte("\x1b[32m")), "$ "; got != want {
		t.Errorf("lastLine kept an escape: %q, want %q", got, want)
	}
	if got, want := lastLine("working", []byte("\rdone")), "doneing"; got != want {
		t.Errorf("lastLine = %q, want %q", got, want)
	}
}
