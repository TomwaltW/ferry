package pty

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// platformSupported reports whether this Windows has ConPTY, which arrived in
// Windows 10 1809. Older systems lack the kernel32 export altogether.
func platformSupported() bool {
	return windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find() == nil
}

// commandPath finds the program to start. ConPTY starts processes with
// CreateProcess, which does not search PATH for a bare name the way a shell
// or os/exec does, so the lookup happens here.
func commandPath(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return name
}
