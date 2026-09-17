// Package locales embeds the translation catalogs so that a single binary
// carries every language it supports.
package locales

import "embed"

//go:embed *.toml
var FS embed.FS
