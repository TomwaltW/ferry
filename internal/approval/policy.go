package approval

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
)

// The three actions a rule can take.
const (
	actionAllow = "allow"
	actionAsk   = "ask"
	actionDeny  = "deny"
)

// kindOther is where every tool kind the contract does not name ends up.
const kindOther = "other"

// knownKinds is the tool vocabulary from fsp.ApprovalRequest.
var knownKinds = map[string]bool{
	"read": true, "edit": true, "delete": true, "move": true, "search": true,
	"execute": true, "think": true, "fetch": true, "switch_mode": true, kindOther: true,
}

// defaultRules apply when the configuration carries no rules of its own:
// reading is free, destructive work outside the workspace is refused, and
// everything else falls through to the default action.
var defaultRules = []config.PolicyRule{
	{Kind: []string{"read", "search", "think"}, Action: actionAllow},
	{Kind: []string{"delete", "move", "edit"}, OutsideWorkspace: boolPtr(true), Action: actionDeny},
}

func boolPtr(b bool) *bool { return &b }

type rule struct {
	kinds   map[string]bool // nil means any kind
	outside *bool
	pattern *regexp.Regexp
	action  string
}

// compileRules turns the configured rules into matchers. A rule whose
// command_pattern does not compile is dropped rather than silently matching
// everything.
func compileRules(in []config.PolicyRule) ([]rule, error) {
	out := make([]rule, 0, len(in))
	for i, r := range in {
		c := rule{outside: r.OutsideWorkspace, action: strings.ToLower(strings.TrimSpace(r.Action))}
		switch c.action {
		case actionAllow, actionAsk, actionDeny:
		default:
			return nil, fmt.Errorf("approval: policy rule %d: unknown action %q", i, r.Action)
		}
		if len(r.Kind) > 0 {
			c.kinds = make(map[string]bool, len(r.Kind))
			for _, k := range r.Kind {
				c.kinds[normalizeKind(k)] = true
			}
		}
		if r.CommandPattern != "" {
			re, err := regexp.Compile(r.CommandPattern)
			if err != nil {
				return nil, fmt.Errorf("approval: policy rule %d: command_pattern: %w", i, err)
			}
			c.pattern = re
		}
		out = append(out, c)
	}
	return out, nil
}

// normalizeKind maps anything outside the contract's vocabulary to "other".
func normalizeKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if knownKinds[k] {
		return k
	}
	return kindOther
}

// evaluate returns the action for the request: the first matching rule wins,
// and a request that matches nothing falls to the configured default.
func (m *manager) evaluate(req fsp.ApprovalRequest) string {
	kind := normalizeKind(req.ToolKind)
	var (
		outsideKnown bool
		anyOutside   bool
	)
	for _, r := range m.rules {
		if r.kinds != nil && !r.kinds[kind] {
			continue
		}
		if r.outside != nil {
			if !outsideKnown {
				anyOutside = m.anyOutside(req.Paths)
				outsideKnown = true
			}
			if *r.outside {
				// "at least one path outside": a request with no paths at all
				// cannot satisfy it.
				if len(req.Paths) == 0 || !anyOutside {
					continue
				}
			} else if anyOutside {
				continue
			}
		}
		if r.pattern != nil && !r.pattern.MatchString(req.InputPreview) {
			continue
		}
		return r.action
	}
	return m.defaultAction
}

// anyOutside reports whether at least one path lies outside the workspace.
func (m *manager) anyOutside(paths []string) bool {
	for _, p := range paths {
		if !m.inside(p) {
			return true
		}
	}
	return false
}

// inside resolves p to an absolute, symlink-free path and reports whether it
// sits under a workspace root. A path that cannot be resolved counts as
// outside, so a symlink pointing out of the workspace cannot sneak in.
func (m *manager) inside(p string) bool {
	if p == "" {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	for _, root := range m.roots {
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// normalizeRoots resolves the workspace roots the same way inside resolves a
// path, so that the two can be compared directly.
func normalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		out = append(out, filepath.Clean(abs))
	}
	return out
}
