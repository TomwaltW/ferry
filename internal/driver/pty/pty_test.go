//go:build linux || darwin

package pty

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// echoShell is a shell that answers every line it reads and stops on "quit". It
// stands in for an agent that only offers a terminal.
const echoShell = `while IFS= read -r line; do echo "got:$line"; if [ "$line" = quit ]; then exit 0; fi; done`

// TestRunEchoInjection is the whole of ferry run: the session registers with the
// daemon, a prompt from chat is typed into the program, and what the program says
// comes back as one turn.
func TestRunEchoInjection(t *testing.T) {
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
			Name:    "echo-shell",
			Idle:    runIdle,
			Command: []string{"sh", "-c", echoShell},
			Home:    d.home,
			In:      keys,
			Out:     out,
			Log:     testLogger(),
		})
		done <- result{code, err}
	}()

	// The session registers as a pseudo terminal that takes input from chat but
	// cannot relay approvals.
	attach := d.waitAttach()
	if attach.Kind != control.KindPTY {
		t.Errorf("Kind = %q, want %q", attach.Kind, control.KindPTY)
	}
	if attach.Agent != "echo-shell" {
		t.Errorf("Agent = %q, want the name given on the command line", attach.Agent)
	}
	if !attach.AllowInject {
		t.Error("AllowInject is false, but a wrapped program does take typed input")
	}
	if attach.RemoteApprove {
		t.Error("RemoteApprove is true, but a wrapped program has no approvals to relay")
	}
	if attach.PID <= 0 {
		t.Errorf("PID = %d, want the pid of the wrapped program", attach.PID)
	}
	if attach.Cwd == "" {
		t.Error("Cwd is empty")
	}

	// A prompt from chat is typed in, and the answer comes back as a chunk and
	// the end of a turn.
	if err := d.send(control.Message{Type: control.MsgPrompt, SessionID: "sess-pty", Text: "hello", Sender: "owner"}); err != nil {
		t.Fatalf("send the prompt: %v", err)
	}

	chunk := d.waitMsg(control.MsgEvent)
	if chunk.Event == nil || chunk.Event.Kind != fsp.EvChunk {
		t.Fatalf("the first event is %+v, want a chunk", chunk.Event)
	}
	if !strings.Contains(chunk.Event.Text, "got:hello") {
		t.Errorf("the chunk says %q, want it to contain %q", chunk.Event.Text, "got:hello")
	}
	if strings.Contains(chunk.Event.Text, PasteStart) || strings.Contains(chunk.Event.Text, "\x1b") {
		t.Errorf("the chunk still has terminal escapes in it: %q", chunk.Event.Text)
	}
	if chunk.SessionID != "sess-pty" {
		t.Errorf("SessionID = %q, want the one the daemon handed out", chunk.SessionID)
	}

	end := d.waitMsg(control.MsgEvent)
	if end.Event == nil || end.Event.Kind != fsp.EvTurnEnd {
		t.Fatalf("the second event is %+v, want the end of the turn", end.Event)
	}
	if end.Event.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", end.Event.StopReason)
	}

	// What the user types still reaches the program, and the program's output
	// still reaches the terminal.
	if _, err := typed.Write([]byte("quit\n")); err != nil {
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

	// The terminal sees the raw stream, paste markers and all: forwarding is
	// byte for byte, and only what goes to chat is cleaned up.
	transcript := stripANSI(out.String())
	if !strings.Contains(transcript, "got:hello") {
		t.Errorf("the terminal never saw the answer: %q", out.String())
	}
	if !strings.Contains(transcript, "got:quit") {
		t.Errorf("the terminal never saw what the user typed: %q", out.String())
	}
	if d.waitMsg(control.MsgDetach).SessionID != "sess-pty" {
		t.Error("the daemon was not told the session ended")
	}
	_ = typed.Close()
}

// TestExitCodePropagated pins the last line of spec-p3 3.8: ferry run ends the
// way the program it wrapped ended. There is no daemon here, which is also the
// case of a wrapper that only wraps.
func TestExitCodePropagated(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		want    int
		wantErr bool
	}{
		{name: "a plain exit code", script: "exit 7", want: 7},
		{name: "success", script: "exit 0", want: 0},
		// A program killed by a signal has no code of its own; the shell
		// convention of 128 plus the signal is what is reported.
		{name: "killed by a signal", script: "kill -TERM $$; sleep 5", want: 128 + 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, err := Run(t.Context(), Options{
				Idle:    runIdle,
				Command: []string{"sh", "-c", c.script},
				Home:    t.TempDir(),
				Out:     io.Discard,
				Log:     testLogger(),
			})
			if err != nil {
				t.Fatalf("Run returned %v", err)
			}
			if code != c.want {
				t.Errorf("exit code = %d, want %d", code, c.want)
			}
		})
	}

	// A command that is not there is a failure of ferry run itself, not an exit
	// code to pass on.
	if _, err := Run(t.Context(), Options{
		Command: []string{"ferry-no-such-program"},
		Home:    t.TempDir(),
		Out:     io.Discard,
		Log:     testLogger(),
	}); err == nil {
		t.Error("Run of a missing command returned no error")
	}

	// An empty command line is caught before anything is started.
	if _, err := Run(t.Context(), Options{Home: t.TempDir()}); err == nil {
		t.Error("Run with no command returned no error")
	}
}
