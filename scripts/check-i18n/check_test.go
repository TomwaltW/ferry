package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// run checks one fixture tree and returns the problems as printable lines.
func run(t *testing.T, fixture string) []string {
	t.Helper()
	r := check(filepath.Join("testdata", fixture), false)
	out := make([]string, 0, len(r.problems))
	for _, p := range r.problems {
		out = append(out, p.String())
	}
	return out
}

// wantOne asserts that the fixture produced exactly one problem and that it
// mentions the expected file and reason.
func wantOne(t *testing.T, fixture, file, substr string) {
	t.Helper()
	got := run(t, fixture)
	if len(got) != 1 {
		t.Fatalf("%s: got %d problems %q, want exactly 1", fixture, len(got), got)
	}
	if !strings.HasPrefix(got[0], "i18n: "+file+": ") {
		t.Errorf("%s: problem %q is not reported against %s", fixture, got[0], file)
	}
	if !strings.Contains(got[0], substr) {
		t.Errorf("%s: problem %q does not mention %q", fixture, got[0], substr)
	}
}

func TestOKFixturePasses(t *testing.T) {
	if got := run(t, "ok"); len(got) != 0 {
		t.Fatalf("ok fixture: want no problems, got %q", got)
	}
}

func TestMissingPairedFile(t *testing.T) {
	wantOne(t, "missing-pair", "SECURITY.zh-CN.md", "required bilingual file is missing")
}

func TestHeadingCountMismatch(t *testing.T) {
	wantOne(t, "heading-mismatch", "README.zh-CN.md", `"## " heading count differs`)
}

func TestLocaleKeyMismatch(t *testing.T) {
	wantOne(t, "locale-keys", "locales/en.toml", `key "hint.extra"`)
}

func TestDocsPathSetMismatch(t *testing.T) {
	wantOne(t, "docs-path-mismatch", "docs/zh-CN/agents.md", "missing counterpart of docs/en/agents.md")
}

func TestMissingCrossLink(t *testing.T) {
	wantOne(t, "missing-crosslink", "docs/zh-CN/quickstart.md", "no link to docs/en/quickstart.md")
}

func TestCodeBlockMismatch(t *testing.T) {
	wantOne(t, "codeblock-mismatch", "README.zh-CN.md", "code block 1 differs")
}

func TestStripCommentKeepsCodeAndDropsComment(t *testing.T) {
	cases := map[string]string{
		"go build ./...   # 构建":       "go build ./...",
		"\tName string // 名字":         "\tName string",
		"PlatformWeChat = \"wechat\"": "PlatformWeChat = \"wechat\"",
		"  ":                          "",
	}
	for in, want := range cases {
		if got := stripComment(in); got != want {
			t.Errorf("stripComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMarkdownIgnoresHeadingsInsideFences(t *testing.T) {
	lines := []string{
		"# Title",
		"## Real",
		"```sh",
		"## not a heading",
		"### not a heading either",
		"```",
		"### Real",
	}
	d := parseMarkdown(lines)
	if d.h2 != 1 || d.h3 != 1 {
		t.Errorf("parseMarkdown: h2=%d h3=%d, want 1 and 1", d.h2, d.h3)
	}
	if len(d.blocks) != 1 {
		t.Fatalf("parseMarkdown: got %d code blocks, want 1", len(d.blocks))
	}
	if len(d.blocks[0]) != 2 || d.blocks[0][0] != "" {
		t.Errorf("parseMarkdown: block = %q, want two comment-only lines", d.blocks[0])
	}
}

func TestLinksToResolvesRelativeTargets(t *testing.T) {
	if !linksTo([]string{"[中文版](../zh-CN/faq.md)"}, "docs/en/faq.md", "docs/zh-CN/faq.md") {
		t.Error("linksTo: relative sibling link not resolved")
	}
	if !linksTo([]string{"[English](README.md)"}, "README.zh-CN.md", "README.md") {
		t.Error("linksTo: root link not resolved")
	}
	if linksTo([]string{"[self](faq.md)"}, "docs/en/faq.md", "docs/zh-CN/faq.md") {
		t.Error("linksTo: a self link must not count as a cross-language link")
	}
}
