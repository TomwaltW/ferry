// Package i18n renders every user-facing string from the catalogs in locales.
// P0-T3 implements the behaviour; this file only fixes the contract.
package i18n

import (
	"io/fs"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// Bundle holds the loaded catalogs.
type Bundle struct {
	// fields are defined by P0-T3
}

// Load reads en.toml and zh-CN.toml from fsys.
func Load(fsys fs.FS) (*Bundle, error) { panic("not implemented") }

// Default returns the bundle embedded at build time and panics if it fails to load.
func Default() *Bundle { panic("not implemented") }

// T renders the template stored under key for lang with args. A missing key
// returns the key itself and logs a warning; an unknown language falls back to
// zh-CN.
func (b *Bundle) T(lang fsp.Lang, key string, args map[string]any) string {
	panic("not implemented")
}

// Keys returns every key present for lang.
func (b *Bundle) Keys(lang fsp.Lang) []string { panic("not implemented") }
