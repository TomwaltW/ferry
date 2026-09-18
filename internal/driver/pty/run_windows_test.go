package pty

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// echoChildEnv turns the test binary into the program ferry run wraps: with it
// set, the binary answers every line with "got:<line>" and exits on "quit", or
// exits at once with the code the variable holds when that is a number.
const echoChildEnv = "FERRY_PTY_ECHO_CHILD"

func init() {
	v := os.Getenv(echoChildEnv)
	if v == "" {
		return
	}
	if code, err := strconv.Atoi(v); err == nil {
		os.Exit(code)
	}
	echoChild()
	os.Exit(0)
}

// echoChild reads the console the way a terminal program does — escape
// sequences as bytes, no line editing or echo by the console host — and
// answers each line. That is the mode the agents ferry run wraps put the
// console in.
func echoChild() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		_ = windows.SetConsoleMode(h, mode&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT|windows.ENABLE_PROCESSED_INPUT)|
			windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
	}
	in := bufio.NewReader(os.Stdin)
	var line []byte
	for {
		b, err := in.ReadByte()
		if err != nil {
			return
		}
		if b != '\r' && b != '\n' {
			line = append(line, b)
			continue
		}
		if len(line) == 0 {
			continue
		}
		// %q shows exactly what ConPTY delivered, paste markers included.
		fmt.Printf("got:%s raw=%q\n", line, line)
		if string(line) == "quit" {
			return
		}
		line = line[:0]
	}
}

// TestRunConPTYEcho is spec-p4 3.8 on Windows: ferry run wraps the program in
// ConPTY, registers it, types a prompt from chat into it and reports the
// answer, with the escapes cleaned out of what goes back to chat.
func TestRunConPTYEcho(t *testing.T) {
	if !Supported() {
		t.Fatal("Supported is false, but every Windows the CI runs on has ConPTY")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	t.Setenv(echoChildEnv, "echo")

	d := startDaemon(t)
	keys, typed := io.Pipe()
	out := &buffer{}

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := Run(t.Context(), Options{
			Name:    "echo-child",
			Idle:    runIdle,
			Command: []string{self},
			Home:    d.home,
			In:      keys,
			Out:     out,
			Log:     testLogger(),
		})
		done <- result{code, err}
	}()

	attach := d.waitAttach()
	if attach.Kind != control.KindPTY || attach.Agent != "echo-child" || !attach.AllowInject || attach.PID <= 0 {
		t.Errorf("attach = %+v, want a pty session named echo-child that takes input", attach)
	}

	if err := d.send(control.Message{Type: control.MsgPrompt, SessionID: "sess-pty", Text: "hello", Sender: "owner"}); err != nil {
		t.Fatalf("send the prompt: %v", err)
	}
	chunk := d.waitMsg(control.MsgEvent)
	if chunk.Event == nil || chunk.Event.Kind != fsp.EvChunk {
		t.Fatalf("the first event is %+v, want a chunk", chunk.Event)
	}
	t.Logf("the terminal saw %q", out.String())
	if !strings.Contains(chunk.Event.Text, "got:hello") {
		t.Errorf("the chunk says %q, want it to contain %q", chunk.Event.Text, "got:hello")
	}
	if strings.Contains(chunk.Event.Text, "\x1b") {
		t.Errorf("the chunk still has terminal escapes in it: %q", chunk.Event.Text)
	}
	end := d.waitMsg(control.MsgEvent)
	if end.Event == nil || end.Event.Kind != fsp.EvTurnEnd {
		t.Fatalf("the second event is %+v, want the end of the turn", end.Event)
	}

	// What the user types still reaches the program.
	if _, err := typed.Write([]byte("quit\r")); err != nil {
		t.Fatalf("type into the program: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run returned %v", got.err)
		}
		if got.code != 0 {
			t.Errorf("exit code = %d, want 0", got.code)
		}
	case <-time.After(msgWait):
		t.Fatal("Run did not return after the program exited")
	}
	if !strings.Contains(stripANSI(out.String()), "got:quit") {
		t.Errorf("the terminal never saw what the user typed: %q", out.String())
	}
	if d.waitMsg(control.MsgDetach).SessionID != "sess-pty" {
		t.Error("the daemon was not told the session ended")
	}
	_ = typed.Close()
}

// TestExitCodePropagatedConPTY is the Windows half of TestExitCodePropagated:
// the program's own exit code is the one ferry run reports.
func TestExitCodePropagatedConPTY(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	t.Setenv(echoChildEnv, "7")
	code, err := Run(t.Context(), Options{
		Idle:    runIdle,
		Command: []string{self},
		Home:    t.TempDir(),
		Out:     io.Discard,
		Log:     testLogger(),
	})
	if err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}
