// Package deps pins the third-party modules a P4 batch-1 task builds on while
// no package imports them yet, the way internal/deps does for earlier phases.
// spec-p4-b1 3.1 keeps internal/deps outside the T0 whitelist of this batch, so
// the pins live here instead; the next T0 that owns internal/deps moves them
// there and deletes this package. Nothing imports it.
//
// Only the phase T0 task may edit this package or go.mod.
package deps

import (
	// ferry service install, uninstall, status and run (spec-p4-b1 3.7).
	_ "github.com/kardianos/service"
)
