//go:build windows

package pty

import (
	"errors"
	"io"
	"testing"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// TestRunUnsupportedOnWindows is the Windows half of spec-p3 3.8: the pseudo
// terminal is P4 work, so ferry run says so plainly instead of half working.
func TestRunUnsupportedOnWindows(t *testing.T) {
	if Supported() {
		t.Fatal("Supported reports true on Windows, want false until ConPTY is wired up")
	}

	code, err := Run(t.Context(), Options{
		Command: []string{"cmd.exe", "/c", "echo hi"},
		Home:    t.TempDir(),
		Out:     io.Discard,
	})
	if !errors.Is(err, fsp.ErrUnsupported) {
		t.Errorf("Run returned %v, want fsp.ErrUnsupported", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 next to the error", code)
	}
}
