package handoff

import "github.com/TomwaltW/ferry/internal/config"

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
		ProcessMatch: `(^|/)kimi( |$)`,
	},
	{
		Agent:        "qwen-code",
		Resume:       []string{"qwen", "--resume", "{id}"},
		ProcessMatch: `(^|/)qwen( |$)`,
	},
	{
		// Resume unverified: prefer the service driver for OpenCode.
		Agent:        "opencode",
		ProcessMatch: `(^|/)opencode( |$)`,
	},
	{
		Agent:        "claude-acp",
		Resume:       []string{"claude", "--resume", "{id}"},
		ProcessMatch: `(^|/)claude( |$)`,
	},
	{
		Agent:        "codex-acp",
		Resume:       []string{"codex", "resume", "{id}"},
		ProcessMatch: `(^|/)codex( |$)`,
	},
	{
		Agent:        "fake",
		Resume:       []string{"ferry-fakeagent", "--resume", "{id}"},
		ProcessMatch: `ferry-fakeagent`,
	},
}
