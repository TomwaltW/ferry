// Package deps pins the third-party modules later phases build on, so that
// go.mod keeps them while the packages that will use them are still stubs.
// Only the phase T0 task may edit this file or go.mod.
package deps

import (
	_ "github.com/coder/acp-go-sdk"
	_ "github.com/mdp/qrterminal/v3"
	_ "github.com/nicksnyder/go-i18n/v2/i18n"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/spf13/cobra"
	_ "github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"
)
