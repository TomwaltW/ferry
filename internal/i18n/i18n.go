// Package i18n renders every user-facing string from the catalogs in locales.
package i18n

import (
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/locales"
)

// langs lists the catalogs that must be present in every bundle. The first
// entry is also the fallback used for unknown languages.
var langs = []fsp.Lang{fsp.LangZH, fsp.LangEN}

// Bundle holds the loaded catalogs.
type Bundle struct {
	localizers map[fsp.Lang]*goi18n.Localizer
	keys       map[fsp.Lang]map[string]struct{}
}

// Load reads en.toml and zh-CN.toml from fsys.
func Load(fsys fs.FS) (*Bundle, error) {
	inner := goi18n.NewBundle(language.MustParse(string(fsp.LangZH)))
	inner.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	b := &Bundle{
		localizers: make(map[fsp.Lang]*goi18n.Localizer, len(langs)),
		keys:       make(map[fsp.Lang]map[string]struct{}, len(langs)),
	}
	for _, lang := range langs {
		name := string(lang) + ".toml"
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("i18n: read %s: %w", name, err)
		}
		var raw map[string]any
		if err := toml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("i18n: parse %s: %w", name, err)
		}
		keys := make(map[string]struct{})
		flatten("", raw, keys)
		if _, err := inner.ParseMessageFileBytes(data, name); err != nil {
			return nil, fmt.Errorf("i18n: load %s: %w", name, err)
		}
		b.keys[lang] = keys
	}
	for _, lang := range langs {
		b.localizers[lang] = goi18n.NewLocalizer(inner, string(lang))
	}
	return b, nil
}

var (
	defaultOnce   sync.Once
	defaultBundle *Bundle
	defaultErr    error
)

// Default returns the bundle embedded at build time and panics if it fails to load.
func Default() *Bundle {
	defaultOnce.Do(func() {
		defaultBundle, defaultErr = Load(locales.FS)
	})
	if defaultErr != nil {
		panic(fmt.Sprintf("i18n: load embedded locales: %v", defaultErr))
	}
	return defaultBundle
}

// T renders the template stored under key for lang with args. A missing key
// returns the key itself and logs a warning; an unknown language falls back to
// zh-CN.
func (b *Bundle) T(lang fsp.Lang, key string, args map[string]any) string {
	l := b.resolve(lang)
	if _, ok := b.keys[l][key]; !ok {
		slog.Warn("i18n: missing key", "lang", string(l), "key", key)
		return key
	}
	out, err := b.localizers[l].Localize(&goi18n.LocalizeConfig{
		MessageID:    key,
		TemplateData: args,
	})
	if err != nil {
		slog.Warn("i18n: render failed", "lang", string(l), "key", key, "err", err)
		return key
	}
	return out
}

// Keys returns every key present for lang.
func (b *Bundle) Keys(lang fsp.Lang) []string {
	l := b.resolve(lang)
	out := make([]string, 0, len(b.keys[l]))
	for k := range b.keys[l] {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// resolve maps an arbitrary language onto a loaded catalog, falling back to zh-CN.
func (b *Bundle) resolve(lang fsp.Lang) fsp.Lang {
	if _, ok := b.keys[lang]; ok {
		return lang
	}
	return fsp.LangZH
}

// messageFields are the reserved field names of a go-i18n message table. A
// table made only of these is a message, not a nesting level.
var messageFields = map[string]bool{
	"id": true, "description": true, "hash": true,
	"leftdelim": true, "rightdelim": true,
	"zero": true, "one": true, "two": true,
	"few": true, "many": true, "other": true,
}

// flatten collects the message IDs of a parsed catalog, joining nested tables
// with dots the way go-i18n does.
func flatten(prefix string, node map[string]any, out map[string]struct{}) {
	if prefix != "" && isMessage(node) {
		out[prefix] = struct{}{}
		return
	}
	for k, v := range node {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			flatten(key, child, out)
			continue
		}
		out[key] = struct{}{}
	}
}

func isMessage(node map[string]any) bool {
	if len(node) == 0 {
		return false
	}
	for k, v := range node {
		if _, ok := v.(map[string]any); ok {
			return false
		}
		if !messageFields[strings.ToLower(k)] {
			return false
		}
	}
	return true
}
