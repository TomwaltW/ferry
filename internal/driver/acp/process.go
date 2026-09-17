package acp

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

// stderrLine caps how much of one line of an agent's stderr is logged. Agents
// are chatty and some of them print whole stack traces.
const stderrLine = 500

// process is the agent subprocess and the pipes Ferry talks to it over.
//
// The three streams are plain os.Pipe pairs rather than the pipes exec hands
// out, because exec closes those from Wait: the JSON-RPC connection reads
// stdout for as long as the agent lives, which would race with reaping it.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// done closes once the process has been reaped; err is its exit status and
	// is only safe to read after done is closed.
	done chan struct{}
	err  error

	closeStdin sync.Once
}

// spawn starts the agent named by command in dir with exactly env, and wires up
// its three standard streams. The caller owns the process from here on.
func spawn(command []string, dir string, env []string, log *slog.Logger) (*process, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("acp: the session spec has no command")
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = env

	// parentEnds are the halves the child does not need; they are closed once
	// it has them, and on any failure along the way.
	var parentEnds []io.Closer
	closeAll := func(cs []io.Closer) {
		for _, c := range cs {
			c.Close()
		}
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("acp: open a pipe for the agent's stdin: %w", err)
	}
	cmd.Stdin = stdinR
	parentEnds = append(parentEnds, stdinR)

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll([]io.Closer{stdinR, stdinW})
		return nil, fmt.Errorf("acp: open a pipe for the agent's stdout: %w", err)
	}
	cmd.Stdout = stdoutW
	parentEnds = append(parentEnds, stdoutW)

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll([]io.Closer{stdinR, stdinW, stdoutR, stdoutW})
		return nil, fmt.Errorf("acp: open a pipe for the agent's stderr: %w", err)
	}
	cmd.Stderr = stderrW
	parentEnds = append(parentEnds, stderrW)

	if err := cmd.Start(); err != nil {
		closeAll([]io.Closer{stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW})
		return nil, fmt.Errorf("acp: start %s: %w", command[0], err)
	}
	closeAll(parentEnds)

	p := &process{cmd: cmd, stdin: stdinW, stdout: stdoutR, done: make(chan struct{})}
	go logStderr(stderrR, log, command[0])
	go p.reap()
	return p, nil
}

// reap waits for the process to exit, then closes done. spawn starts it, so
// done always closes eventually, even for a session that failed to start.
func (p *process) reap() {
	p.err = p.cmd.Wait()
	close(p.done)
}

// endInput closes the agent's stdin, which is how a stdio agent is asked to
// shut down. It is safe to call more than once.
func (p *process) endInput() {
	p.closeStdin.Do(func() { p.stdin.Close() })
}

// kill ends the process without waiting for it to agree.
func (p *process) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// logStderr copies the agent's stderr into the log at debug level, one record
// per line and truncated so a stack trace cannot swamp the log.
func logStderr(r io.ReadCloser, log *slog.Logger, name string) {
	defer r.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > stderrLine {
			line = line[:stderrLine]
		}
		log.Debug("agent stderr", "agent", name, "line", line)
	}
}
