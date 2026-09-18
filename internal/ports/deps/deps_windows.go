//go:build windows

package deps

import (
	// The Windows process table of terminal hand-off (spec-p4-b1 3.8). The
	// module was already required through go-pty and x/term; this makes it a
	// direct requirement at the same version, and only builds on Windows
	// because every file of the package does.
	_ "golang.org/x/sys/windows"
)
