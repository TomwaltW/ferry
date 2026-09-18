//go:build linux || darwin

package pty

import (
	"regexp"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/clock"
)

// epoch is where the fake clock starts. Any fixed instant will do; the rules are
// all about differences.
var epoch = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

// testIdle is short enough to read in a test and has nothing to do with the
// production default, which the gate uses when it is given nothing.
const testIdle = 200 * time.Millisecond

func TestIdleDetection(t *testing.T) {
	clk := clock.NewFake(epoch)
	g := newGate(clk, testIdle, nil)

	// A program that just wrote something is not ready to be typed at, and says
	// how long is left rather than making the caller guess.
	g.sawOutput([]byte("working"))
	ok, after := g.ready(true)
	if ok {
		t.Fatal("the gate opened while the program was still writing")
	}
	if after != testIdle {
		t.Errorf("the gate wants %v more, want the whole idle period %v", after, testIdle)
	}

	// Part of the way there is still not there.
	clk.Advance(testIdle / 2)
	if ok, after := g.ready(true); ok || after != testIdle/2 {
		t.Errorf("halfway through the idle period the gate reported ok=%v after=%v", ok, after)
	}

	// Once the program has been quiet for the whole period, it is ready.
	clk.Advance(testIdle / 2)
	if ok, after := g.ready(true); !ok || after != 0 {
		t.Errorf("after the idle period the gate reported ok=%v after=%v, want it open", ok, after)
	}

	// More output starts the countdown again.
	g.sawOutput([]byte("more"))
	if ok, _ := g.ready(true); ok {
		t.Error("the gate stayed open after the program wrote again")
	}

	// The default is used when no idle period was given, which is what the
	// command line does with a missing --idle.
	plain := newGate(clock.NewFake(epoch), 0, nil)
	if plain.idle != DefaultIdle {
		t.Errorf("the default idle period is %v, want %v", plain.idle, DefaultIdle)
	}

	// A prompt pattern is the one condition waiting alone does not satisfy: the
	// program has to write the prompt first.
	waiting := newGate(clk, testIdle, regexp.MustCompile(`\$ $`))
	waiting.sawOutput([]byte("busy...\n"))
	clk.Advance(testIdle)
	if ok, after := waiting.ready(true); ok || after != testIdle {
		t.Errorf("with the prompt missing the gate reported ok=%v after=%v", ok, after)
	}
	waiting.sawOutput([]byte("\n$ "))
	clk.Advance(testIdle)
	if ok, _ := waiting.ready(true); !ok {
		t.Errorf("the gate stayed shut on the line %q, which the pattern matches", waiting.line)
	}
}

func TestUserTypingDelaysInjection(t *testing.T) {
	clk := clock.NewFake(epoch)
	g := newGate(clk, testIdle, nil)

	// The program is quiet, so on its own account the prompt could go in.
	clk.Advance(testIdle)
	if ok, _ := g.ready(true); !ok {
		t.Fatal("the gate is shut even though the program is quiet")
	}

	// The user touches the keyboard: the prompt now waits for them, and for
	// longer than the idle period, because TypingGrace is longer.
	g.sawKeys()
	ok, after := g.ready(true)
	if ok {
		t.Fatal("the gate opened while the user was typing")
	}
	if after != TypingGrace {
		t.Errorf("the gate wants %v more, want the typing grace %v", after, TypingGrace)
	}

	// Still typing keeps pushing it back.
	clk.Advance(TypingGrace - 10*time.Millisecond)
	g.sawKeys()
	if ok, after := g.ready(true); ok || after != TypingGrace {
		t.Errorf("after another keystroke the gate reported ok=%v after=%v", ok, after)
	}

	// Two seconds after the last keystroke it goes in.
	clk.Advance(TypingGrace)
	if ok, after := g.ready(true); !ok || after != 0 {
		t.Errorf("two seconds after the last keystroke the gate reported ok=%v after=%v", ok, after)
	}

	// While collecting the answer to a prompt the user is not consulted: that
	// phase only waits for the program to fall quiet.
	g.sawKeys()
	if ok, _ := g.ready(false); !ok {
		t.Error("collecting the answer waited for the keyboard, which it should not")
	}
}

func TestBracketedPaste(t *testing.T) {
	got := string(pasteBytes("echo hello"))
	want := "\x1b[200~" + "echo hello" + "\x1b[201~" + "\r"
	if got != want {
		t.Errorf("pasteBytes = %q, want %q", got, want)
	}

	// The markers are the ones the package publishes, and the carriage return at
	// the end is what submits the line.
	if want != PasteStart+"echo hello"+PasteEnd+"\r" {
		t.Error("the paste markers of the package do not match the bytes written")
	}
	if got[len(got)-1] != '\r' {
		t.Errorf("the paste ends with %q, want a carriage return", got[len(got)-1])
	}

	// Text with a newline of its own still goes in as one paste: that is the
	// point of the markers.
	multi := string(pasteBytes("first\nsecond"))
	if multi != PasteStart+"first\nsecond"+PasteEnd+"\r" {
		t.Errorf("pasteBytes with a newline = %q", multi)
	}
}
