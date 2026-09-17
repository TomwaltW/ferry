package command

import (
	"slices"
	"testing"

	"github.com/TomwaltW/ferry/internal/ports"
)

// TestUseForceArg pins the P2 spelling of /use: the optional "force" is simply
// the second argument, so the core can tell "take it over anyway" from a plain
// /use without the parser having to know what force means (spec-p2 5.4).
func TestUseForceArg(t *testing.T) {
	tests := []struct {
		text string
		want ports.Command
	}{
		{"/use 2 force", ports.Command{Name: "use", Args: []string{"2", "force"}}},
		{"/use 2  force", ports.Command{Name: "use", Args: []string{"2", "force"}}},
		{"  /use 10 force  ", ports.Command{Name: "use", Args: []string{"10", "force"}}},
		{"/USE 2 force", ports.Command{Name: "use", Args: []string{"2", "force"}}},
		// Still just arguments: a wrong one reaches the core, which answers
		// with usage.use_force.
		{"/use 2 FORCE", ports.Command{Name: "use", Args: []string{"2", "FORCE"}}},
		{"/use force", ports.Command{Name: "use", Args: []string{"force"}}},
		{"/use 2 force extra", ports.Command{Name: "use", Args: []string{"2", "force", "extra"}}},
	}
	p := New()
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := p.Parse(tt.text)
			if !ok {
				t.Fatalf("Parse(%q) = not a command, want %+v", tt.text, tt.want)
			}
			if got.Name != tt.want.Name || !slices.Equal(got.Args, tt.want.Args) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

// TestPullAndFileArgs covers the two commands the P2 outbox answers.
func TestPullAndFileArgs(t *testing.T) {
	tests := []struct {
		text string
		want ports.Command
	}{
		{"/pull", ports.Command{Name: "pull"}},
		{"收件", ports.Command{Name: "pull"}},
		// /pull takes no argument, but an extra word still parses; the core
		// ignores it rather than treating the message as a prompt.
		{"/pull 2", ports.Command{Name: "pull", Args: []string{"2"}}},
		{"/file 1", ports.Command{Name: "file", Args: []string{"1"}}},
		{"/file 12", ports.Command{Name: "file", Args: []string{"12"}}},
		{"/file", ports.Command{Name: "file"}},
		{"/FILE 3", ports.Command{Name: "file", Args: []string{"3"}}},
	}
	p := New()
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := p.Parse(tt.text)
			if !ok {
				t.Fatalf("Parse(%q) = not a command, want %+v", tt.text, tt.want)
			}
			if got.Name != tt.want.Name || !slices.Equal(got.Args, tt.want.Args) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}
