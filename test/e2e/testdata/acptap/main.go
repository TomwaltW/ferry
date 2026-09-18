// Command acptap sits between a driver and an agent and records the JSON-RPC
// method of every request and notification the driver sends, one per line, in
// the file ACPTAP_LOG. Everything else passes through untouched: the agent's
// stdout and stderr are this process's own, and the tap exits with the
// agent's exit code.
//
// The end-to-end test of ACP v2 (B8) uses it to see which methods actually
// crossed the wire, which neither side reports on its own.
//
// Usage: ACPTAP_LOG=<file> acptap <agent> [args...]
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ACPTAP_LOG=<file> acptap <agent> [args...]")
		os.Exit(2)
	}
	logFile, err := os.OpenFile(os.Getenv("ACPTAP_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acptap:", err)
		os.Exit(2)
	}
	defer logFile.Close()

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	agentIn, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acptap:", err)
		os.Exit(2)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "acptap:", err)
		os.Exit(2)
	}

	// The driver closing our stdin is how it asks the agent to stop, so the
	// agent's stdin closes with it.
	go func() {
		defer agentIn.Close()
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			line := sc.Bytes()
			var m struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &m) == nil && m.Method != "" {
				fmt.Fprintln(logFile, m.Method)
			}
			if _, err := agentIn.Write(append(line, '\n')); err != nil {
				io.Copy(io.Discard, os.Stdin)
				return
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
