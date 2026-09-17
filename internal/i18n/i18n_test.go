package i18n

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/TomwaltW/ferry/internal/fsp"
)

// testFS is a catalog pair that exercises plain values, a template and a key
// that exists in one language only.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"en.toml": &fstest.MapFile{Data: []byte(`
"app.name" = "Ferry"
"greet.hello" = "Hello {{.Name}}, you have {{.Count}} messages"
"only.en" = "english only"
`)},
		"zh-CN.toml": &fstest.MapFile{Data: []byte(`
"app.name" = "渡口"
"greet.hello" = "你好 {{.Name}}，你有 {{.Count}} 条消息"
`)},
	}
}

func TestLookup(t *testing.T) {
	b, err := Load(testFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.T(fsp.LangEN, "app.name", nil); got != "Ferry" {
		t.Errorf("T(en, app.name) = %q, want %q", got, "Ferry")
	}
	if got := b.T(fsp.LangZH, "app.name", nil); got != "渡口" {
		t.Errorf("T(zh-CN, app.name) = %q, want %q", got, "渡口")
	}
}

func TestTemplateArgs(t *testing.T) {
	b, err := Load(testFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	args := map[string]any{"Name": "Ada", "Count": 3}
	if got, want := b.T(fsp.LangEN, "greet.hello", args), "Hello Ada, you have 3 messages"; got != want {
		t.Errorf("T(en, greet.hello) = %q, want %q", got, want)
	}
	if got, want := b.T(fsp.LangZH, "greet.hello", args), "你好 Ada，你有 3 条消息"; got != want {
		t.Errorf("T(zh-CN, greet.hello) = %q, want %q", got, want)
	}
}

func TestMissingKeyReturnsKey(t *testing.T) {
	b, err := Load(testFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.T(fsp.LangEN, "no.such.key", nil); got != "no.such.key" {
		t.Errorf("T(en, no.such.key) = %q, want the key itself", got)
	}
	// Present in en, absent from zh-CN: still the key, never the other language.
	if got := b.T(fsp.LangZH, "only.en", nil); got != "only.en" {
		t.Errorf("T(zh-CN, only.en) = %q, want the key itself", got)
	}
}

func TestUnknownLangFallsBackToZH(t *testing.T) {
	b, err := Load(testFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.T(fsp.Lang("fr"), "app.name", nil); got != "渡口" {
		t.Errorf("T(fr, app.name) = %q, want the zh-CN value", got)
	}
	if got := b.Keys(fsp.Lang("fr")); !slices.Equal(got, b.Keys(fsp.LangZH)) {
		t.Errorf("Keys(fr) = %v, want the zh-CN keys %v", got, b.Keys(fsp.LangZH))
	}
}

func TestKeysSorted(t *testing.T) {
	b, err := Load(testFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"app.name", "greet.hello", "only.en"}
	if got := b.Keys(fsp.LangEN); !slices.Equal(got, want) {
		t.Errorf("Keys(en) = %v, want %v", got, want)
	}
}

func TestLoadMissingCatalog(t *testing.T) {
	only := fstest.MapFS{"en.toml": &fstest.MapFile{Data: []byte(`"app.name" = "Ferry"`)}}
	if _, err := Load(only); err == nil {
		t.Fatal("Load with no zh-CN.toml: want error, got nil")
	}
}

func TestDefaultUsesEmbeddedCatalogs(t *testing.T) {
	b := Default()
	if got := b.T(fsp.LangEN, "app.name", nil); got != "Ferry" {
		t.Errorf("Default().T(en, app.name) = %q, want %q", got, "Ferry")
	}
	for _, lang := range []fsp.Lang{fsp.LangEN, fsp.LangZH} {
		if got := b.T(lang, "app.tagline", nil); got == "app.tagline" || got == "" {
			t.Errorf("Default().T(%s, app.tagline) = %q, want a translated string", lang, got)
		}
	}
	if !slices.Equal(b.Keys(fsp.LangEN), b.Keys(fsp.LangZH)) {
		t.Errorf("embedded catalogs disagree: en=%v zh-CN=%v", b.Keys(fsp.LangEN), b.Keys(fsp.LangZH))
	}
}
