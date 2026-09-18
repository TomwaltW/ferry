package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// consoleInput reads the console the way a terminal program does rather than
// the way cmd.exe does: keys arrive as the bytes the terminal sent, escape
// sequences included, with no line editing or echo by the console host in
// between. That is also what a Unix shell's read sees from a pseudo terminal,
// and what the agents ferry run wraps ask for when they take the console over.
//
// Standard input that is not a console — a pipe — is left alone.
func consoleInput() func() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	raw := mode&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT|windows.ENABLE_PROCESSED_INPUT) |
		windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
