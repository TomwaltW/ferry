package registry

import (
	"os"
	"sort"
	"strings"
)

// ferryPrefix names the variables that configure Ferry itself. They never reach
// an agent, no matter where they were set.
const ferryPrefix = "FERRY_"

// passThrough are the variables an agent inherits from the daemon: enough to
// find its own tools, its home directory, a temporary directory, the locale and
// the proxy. Everything else is dropped so a daemon's environment cannot leak
// into a subprocess by accident.
var passThrough = []string{
	"PATH",
	"HOME",
	"USERPROFILE",
	"APPDATA",
	"LOCALAPPDATA",
	"TMPDIR",
	"TEMP",
	"TMP",
	"LANG",
	"LC_ALL",
	"SHELL",
	"TERM",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
}

// allowedNames is passThrough plus the lowercase spelling of each name, which
// is what the proxy variables are usually written as.
var allowedNames = func() map[string]bool {
	m := make(map[string]bool, 2*len(passThrough))
	for _, name := range passThrough {
		m[name] = true
		m[strings.ToLower(name)] = true
	}
	return m
}()

// buildEnv assembles the environment for an agent: the whitelisted part of the
// daemon's own environment, then the registry entry's variables, then the ones
// from the configuration, each layer overriding the one before it. The result
// is sorted so a spec is comparable.
func buildEnv(layers ...map[string]string) []string {
	merged := make(map[string]string, len(allowedNames))
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if ok && allowedNames[name] {
			merged[name] = value
		}
	}
	for _, layer := range layers {
		for name, value := range layer {
			merged[name] = value
		}
	}

	out := make([]string, 0, len(merged))
	for name, value := range merged {
		if strings.HasPrefix(name, ferryPrefix) {
			continue
		}
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}
