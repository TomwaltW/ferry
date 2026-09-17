package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// pair is one English document and its Chinese counterpart, addressed by
// slash-separated paths relative to the checked root.
type pair struct {
	en       string
	zh       string
	required bool
}

// rootPairs are the fixed documents of rule 1. The spec pair is only required
// once one of its halves exists.
var rootPairs = []pair{
	{en: "README.md", zh: "README.zh-CN.md", required: true},
	{en: "CONTRIBUTING.md", zh: "CONTRIBUTING.zh-CN.md", required: true},
	{en: "SECURITY.md", zh: "SECURITY.zh-CN.md", required: true},
	{en: "spec/fsp.md", zh: "spec/fsp.zh-CN.md"},
}

const (
	docsEN = "docs/en"
	docsZH = "docs/zh-CN"

	localeEN = "locales/en.toml"
	localeZH = "locales/zh-CN.toml"

	// crossLinkLines is how far into a file rule 4 looks for the link to the
	// other language.
	crossLinkLines = 5
)

// problem is one rule violation, printed as "i18n: <file>: <message>".
type problem struct {
	file string
	msg  string
}

func (p problem) String() string { return fmt.Sprintf("i18n: %s: %s", p.file, p.msg) }

// report is the outcome of one run: hard problems fail the command, warnings
// are printed but do not.
type report struct {
	problems []problem
	warnings []string
}

func (r *report) failf(file, format string, args ...any) {
	r.problems = append(r.problems, problem{file: file, msg: fmt.Sprintf(format, args...)})
}

func (r *report) warnf(file, format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf("i18n: warn: %s: %s", file, fmt.Sprintf(format, args...)))
}

// check runs every rule of spec-p0 3.7 against root.
func check(root string, strict bool) *report {
	r := &report{}
	for _, p := range collectPairs(root, r) {
		checkPairContent(root, p, strict, r)
	}
	checkLocaleKeys(root, r)
	sort.SliceStable(r.problems, func(i, j int) bool { return r.problems[i].file < r.problems[j].file })
	return r
}

// collectPairs applies rule 1 and rule 2 and returns the pairs whose two halves
// both exist, so that the content rules have something to compare.
func collectPairs(root string, r *report) []pair {
	var out []pair
	for _, p := range rootPairs {
		enOK, zhOK := exists(root, p.en), exists(root, p.zh)
		switch {
		case enOK && zhOK:
			out = append(out, p)
		case p.required:
			if !enOK {
				r.failf(p.en, "required bilingual file is missing")
			}
			if !zhOK {
				r.failf(p.zh, "required bilingual file is missing")
			}
		case enOK:
			r.failf(p.zh, "missing Chinese counterpart of %s", p.en)
		case zhOK:
			r.failf(p.en, "missing English counterpart of %s", p.zh)
		}
	}
	return append(out, collectDocsPairs(root, r)...)
}

// collectDocsPairs applies rule 2: docs/en and docs/zh-CN must hold the same
// set of relative paths.
func collectDocsPairs(root string, r *report) []pair {
	enDir, zhDir := exists(root, docsEN), exists(root, docsZH)
	if !enDir && !zhDir {
		return nil
	}
	if !enDir {
		r.failf(docsEN, "directory is missing while %s exists", docsZH)
		return nil
	}
	if !zhDir {
		r.failf(docsZH, "directory is missing while %s exists", docsEN)
		return nil
	}
	enFiles, err := listFiles(root, docsEN)
	if err != nil {
		r.failf(docsEN, "cannot be listed: %v", err)
		return nil
	}
	zhFiles, err := listFiles(root, docsZH)
	if err != nil {
		r.failf(docsZH, "cannot be listed: %v", err)
		return nil
	}
	var out []pair
	for _, rel := range enFiles {
		if slices.Contains(zhFiles, rel) {
			out = append(out, pair{en: path.Join(docsEN, rel), zh: path.Join(docsZH, rel), required: true})
			continue
		}
		r.failf(path.Join(docsZH, rel), "missing counterpart of %s", path.Join(docsEN, rel))
	}
	for _, rel := range zhFiles {
		if !slices.Contains(enFiles, rel) {
			r.failf(path.Join(docsEN, rel), "missing counterpart of %s", path.Join(docsZH, rel))
		}
	}
	return out
}

// checkPairContent applies rules 3, 4 and 6 to one pair.
func checkPairContent(root string, p pair, strict bool, r *report) {
	enLines, err := readLines(root, p.en)
	if err != nil {
		r.failf(p.en, "cannot be read: %v", err)
		return
	}
	zhLines, err := readLines(root, p.zh)
	if err != nil {
		r.failf(p.zh, "cannot be read: %v", err)
		return
	}

	en, zh := parseMarkdown(enLines), parseMarkdown(zhLines)
	if en.h2 != zh.h2 {
		r.failf(p.zh, `"## " heading count differs from %s: %d vs %d`, p.en, zh.h2, en.h2)
	}
	if en.h3 != zh.h3 {
		r.failf(p.zh, `"### " heading count differs from %s: %d vs %d`, p.en, zh.h3, en.h3)
	}
	if len(en.blocks) != len(zh.blocks) {
		r.failf(p.zh, "fenced code block count differs from %s: %d vs %d", p.en, len(zh.blocks), len(en.blocks))
	} else {
		for i := range en.blocks {
			if !slices.Equal(en.blocks[i], zh.blocks[i]) {
				r.failf(p.zh, "code block %d differs from %s once comments are stripped", i+1, p.en)
			}
		}
	}

	if !linksTo(head(enLines), p.en, p.zh) {
		r.failf(p.en, "no link to %s within the first %d lines", p.zh, crossLinkLines)
	}
	if !linksTo(head(zhLines), p.zh, p.en) {
		r.failf(p.zh, "no link to %s within the first %d lines", p.en, crossLinkLines)
	}

	checkSourceCommit(root, p, zhLines, strict, r)
}

var sourceCommitRe = regexp.MustCompile(`^<!--\s*source_commit:\s*([0-9a-fA-F]{7,40})\s*-->\s*$`)

// checkSourceCommit applies rule 6: a Chinese file may pin the English commit
// it was translated from; later edits to the English file mean it went stale.
func checkSourceCommit(root string, p pair, zhLines []string, strict bool, r *report) {
	if len(zhLines) == 0 {
		return
	}
	m := sourceCommitRe.FindStringSubmatch(strings.TrimSpace(zhLines[0]))
	if m == nil {
		return
	}
	commit := m[1]
	out, err := exec.Command("git", "-C", root, "log", "--format=%H", commit+"..HEAD", "--", p.en).Output()
	if err != nil {
		r.warnf(p.zh, "cannot compare against source_commit %s: %v", commit, err)
		return
	}
	if strings.TrimSpace(string(out)) == "" {
		return
	}
	msg := fmt.Sprintf("%s changed after source_commit %s", p.en, commit)
	if strict {
		r.failf(p.zh, "%s", msg)
		return
	}
	r.warnf(p.zh, "%s", msg)
}

// checkLocaleKeys applies rule 5.
func checkLocaleKeys(root string, r *report) {
	enOK, zhOK := exists(root, localeEN), exists(root, localeZH)
	if !enOK && !zhOK {
		return
	}
	if !enOK {
		r.failf(localeEN, "catalog is missing while %s exists", localeZH)
		return
	}
	if !zhOK {
		r.failf(localeZH, "catalog is missing while %s exists", localeEN)
		return
	}
	enKeys, err := localeKeys(root, localeEN)
	if err != nil {
		r.failf(localeEN, "%v", err)
		return
	}
	zhKeys, err := localeKeys(root, localeZH)
	if err != nil {
		r.failf(localeZH, "%v", err)
		return
	}
	for _, k := range enKeys {
		if !slices.Contains(zhKeys, k) {
			r.failf(localeZH, "key %q is present in %s but missing here", k, localeEN)
		}
	}
	for _, k := range zhKeys {
		if !slices.Contains(enKeys, k) {
			r.failf(localeEN, "key %q is present in %s but missing here", k, localeZH)
		}
	}
}

// localeKeys returns the sorted message IDs of one catalog.
func localeKeys(root, rel string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("cannot be parsed: %w", err)
	}
	keys := map[string]struct{}{}
	flattenKeys("", raw, keys)
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

// messageFields are the reserved field names of a go-i18n message table; a
// table made only of these is a message, not a nesting level. Kept local on
// purpose: internal/i18n owns the runtime copy, this tool owns the check.
var messageFields = map[string]bool{
	"id": true, "description": true, "hash": true,
	"leftdelim": true, "rightdelim": true,
	"zero": true, "one": true, "two": true,
	"few": true, "many": true, "other": true,
}

func flattenKeys(prefix string, node map[string]any, out map[string]struct{}) {
	if prefix != "" && isMessageTable(node) {
		out[prefix] = struct{}{}
		return
	}
	for k, v := range node {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			flattenKeys(key, child, out)
			continue
		}
		out[key] = struct{}{}
	}
}

func isMessageTable(node map[string]any) bool {
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

// mdDoc is the part of a Markdown file that the content rules compare.
type mdDoc struct {
	h2     int
	h3     int
	blocks [][]string
}

var fenceRe = regexp.MustCompile("^(`{3,}|~{3,})")

// parseMarkdown counts headings outside fenced blocks and collects the body of
// every fenced block with comments and trailing space removed.
func parseMarkdown(lines []string) mdDoc {
	var d mdDoc
	var fence string
	var cur []string
	for _, raw := range lines {
		trimmed := strings.TrimLeft(raw, " \t")
		marker := fenceRe.FindString(trimmed)
		switch {
		case marker != "" && fence == "":
			fence = marker
			cur = nil
			continue
		case marker != "" && fence != "":
			// A closing fence uses the same character, is at least as long as
			// the opening one, and carries no info string.
			if marker[0] == fence[0] && len(marker) >= len(fence) && strings.TrimSpace(trimmed) == marker {
				d.blocks = append(d.blocks, cur)
				fence, cur = "", nil
				continue
			}
		}
		if fence != "" {
			cur = append(cur, stripComment(raw))
			continue
		}
		if strings.HasPrefix(raw, "### ") {
			d.h3++
			continue
		}
		if strings.HasPrefix(raw, "## ") {
			d.h2++
		}
	}
	if fence != "" {
		d.blocks = append(d.blocks, cur)
	}
	return d
}

// stripComment drops everything from the first "#" or "//" and the trailing
// whitespace, so that translated comments do not make two code blocks differ.
func stripComment(line string) string {
	cut := len(line)
	if i := strings.Index(line, "//"); i != -1 && i < cut {
		cut = i
	}
	if i := strings.Index(line, "#"); i != -1 && i < cut {
		cut = i
	}
	return strings.TrimRight(line[:cut], " \t")
}

var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

// linksTo reports whether any Markdown link in lines resolves to target, with
// relative targets read from the directory holding from.
func linksTo(lines []string, from, target string) bool {
	dir := path.Dir(from)
	for _, line := range lines {
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			href := m[1]
			if i := strings.IndexAny(href, "#?"); i != -1 {
				href = href[:i]
			}
			if href == "" {
				continue
			}
			resolved := path.Clean(path.Join(dir, href))
			if strings.HasPrefix(href, "/") {
				resolved = path.Clean(strings.TrimPrefix(href, "/"))
			}
			if resolved == target {
				return true
			}
		}
	}
	return false
}

func head(lines []string) []string {
	if len(lines) > crossLinkLines {
		return lines[:crossLinkLines]
	}
	return lines
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func readLines(root, rel string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), nil
}

// listFiles returns every regular file under dir, as slash-separated paths
// relative to dir.
func listFiles(root, dir string) ([]string, error) {
	base := filepath.Join(root, filepath.FromSlash(dir))
	var out []string
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}
