package handoff

import (
	"regexp"
	"runtime"

	"github.com/TomwaltW/ferry/internal/config"
)

// Profiles is the built-in handoff table from spec-p2 3.3. It is filled in
// from the verification pass in section 0: an agent whose resume command has
// not been verified has an empty Resume, and ferry back reports it as
// unsupported rather than guessing. Configuration entries override these.
//
// {id} is replaced with the agent session ID.
var Profiles = []config.HandoffProfile{
	{
		Agent:        "kimi",
		Resume:       []string{"kimi", "--resume", "{id}"},
		ProcessMatch: processMatch(runtime.GOOS, "kimi"),
	},
	{
		Agent:        "qwen-code",
		Resume:       []string{"qwen", "--resume", "{id}"},
		ProcessMatch: processMatch(runtime.GOOS, "qwen"),
	},
	{
		// Resume unverified: prefer the service driver for OpenCode.
		Agent:        "opencode",
		ProcessMatch: processMatch(runtime.GOOS, "opencode"),
	},
	{
		Agent:        "claude-acp",
		Resume:       []string{"claude", "--resume", "{id}"},
		ProcessMatch: processMatch(runtime.GOOS, "claude"),
	},
	{
		Agent:        "codex-acp",
		Resume:       []string{"codex", "resume", "{id}"},
		ProcessMatch: processMatch(runtime.GOOS, "codex"),
	},
	{
		Agent:        "fake",
		Resume:       []string{"ferry-fakeagent", "--resume", "{id}"},
		ProcessMatch: `ferry-fakeagent`,
	},
}

// processMatch is the pattern that recognises a program named name on the
// command line of a process on goos.
//
// On Unix it is the program's base name, as it was typed or as a shebang put
// it. Windows spells the same program differently: a backslash path, a .exe
// suffix, the path in quotes when it has a space in it, and no regard for case
// — so it gets a pattern of its own rather than a wider one that would change
// what Unix matches.
func processMatch(goos, name string) string {
	quoted := regexp.QuoteMeta(name)
	if goos == "windows" {
		return `(?i)(^|[\\/"])` + quoted + `(\.exe)?("|\s|$)`
	}
	return `(^|/)` + quoted + `( |$)`
}
