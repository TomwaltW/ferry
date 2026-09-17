package registry

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// emptyPath points PATH at a directory of its own, so whatever the developer
// happens to have installed cannot decide which distribution is picked.
func emptyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

// fakeExe writes an executable file named name into dir and returns its path.
// Nothing runs it; the resolver only has to find it.
func fakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	// Windows only treats a file as executable via PATHEXT, and looks it up
	// through the extension rather than the permission bits.
	body, mode := "#!/bin/sh\nexit 0\n", os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		name += ".bat"
		body, mode = "@echo off\r\nexit /b 0\r\n", os.FileMode(0o644)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// envValue returns the value of name in a SessionSpec environment, and whether
// it was there at all.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

func TestSnapshotParses(t *testing.T) {
	byID, err := parseSnapshot(snapshotJSON)
	if err != nil {
		t.Fatalf("parseSnapshot failed: %v", err)
	}
	if got, want := len(byID), len(SnapshotIDs); got != want {
		t.Errorf("snapshot has %d agents, want %d", got, want)
	}

	for _, id := range SnapshotIDs {
		e, ok := byID[id]
		if !ok {
			t.Errorf("snapshot is missing %q", id)
			continue
		}
		if e.Name == "" || e.Version == "" {
			t.Errorf("%s: name %q version %q, want both set", id, e.Name, e.Version)
		}
		d := e.Distribution
		if d.NPX == nil && d.UVX == nil && len(d.Binary) == 0 {
			t.Errorf("%s has no distribution", id)
		}
		for key, b := range d.Binary {
			if b.Archive == "" || b.Cmd == "" || len(b.SHA256) != 64 {
				t.Errorf("%s/%s: archive %q cmd %q sha256 %q", id, key, b.Archive, b.Cmd, b.SHA256)
			}
		}
		if d.NPX != nil && d.NPX.Package == "" {
			t.Errorf("%s: npx entry has no package", id)
		}
	}

	// The snapshot must stay loadable through the constructor too, which is
	// the only path production code takes.
	if _, err := New(config.RegistryConfig{SnapshotOnly: true}, nil, t.TempDir(), testLogger()); err != nil {
		t.Fatalf("New over the committed snapshot failed: %v", err)
	}
}

func TestResolveCommand(t *testing.T) {
	dir := emptyPath(t)
	exe := fakeExe(t, dir, "my-agent")

	agents := []config.AgentConfig{{
		Name:    "mine",
		Command: []string{exe, "--acp"},
		Env:     map[string]string{"MY_TOKEN": "t0ken"},
	}}
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	spec, err := r.Resolve(t.Context(), "mine")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if want := []string{exe, "--acp"}; !slices.Equal(spec.Command, want) {
		t.Errorf("Command = %v, want %v", spec.Command, want)
	}
	if spec.Agent != "mine" {
		t.Errorf("Agent = %q, want %q", spec.Agent, "mine")
	}
	if spec.Cwd != "" {
		t.Errorf("Cwd = %q, want it left to the caller", spec.Cwd)
	}
	if got, ok := envValue(spec.Env, "MY_TOKEN"); !ok || got != "t0ken" {
		t.Errorf("MY_TOKEN = %q (present %v), want %q", got, ok, "t0ken")
	}

	// A bare name that is not on PATH cannot be resolved by name either.
	if _, err := r.Resolve(t.Context(), "nobody"); !errors.Is(err, fsp.ErrNotFound) {
		t.Errorf("Resolve of an unconfigured agent = %v, want fsp.ErrNotFound", err)
	}
}

func TestResolveNpx(t *testing.T) {
	dir := emptyPath(t)
	fakeExe(t, dir, "npx")

	agents := []config.AgentConfig{{Name: "qwen-code", Registry: "qwen-code"}}
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	spec, err := r.Resolve(t.Context(), "qwen-code")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(spec.Command) < 3 || spec.Command[0] != "npx" || spec.Command[1] != "-y" {
		t.Fatalf("Command = %v, want it to start with npx -y", spec.Command)
	}
	if !strings.Contains(spec.Command[2], "@") {
		t.Errorf("package %q, want the registry's versioned package", spec.Command[2])
	}
	if _, ok := envValue(spec.Env, "npm_config_registry"); ok {
		t.Error("npm_config_registry is set even though no mirror was configured")
	}
}

func TestNpmMirrorEnv(t *testing.T) {
	dir := emptyPath(t)
	fakeExe(t, dir, "npx")

	const mirror = "https://registry.npmmirror.com"
	cfg := config.RegistryConfig{SnapshotOnly: true, NPMRegistry: mirror}
	agents := []config.AgentConfig{{Name: "qwen-code", Registry: "qwen-code"}}
	r, err := New(cfg, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	spec, err := r.Resolve(t.Context(), "qwen-code")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if got, ok := envValue(spec.Env, "npm_config_registry"); !ok || got != mirror {
		t.Errorf("npm_config_registry = %q (present %v), want %q", got, ok, mirror)
	}
}

func TestResolveBinaryFromPath(t *testing.T) {
	dir := emptyPath(t)
	// kimi ships as a binary whose cmd is "./kimi", so an already installed
	// kimi on PATH must win over downloading the archive.
	exe := fakeExe(t, dir, "kimi")

	agents := []config.AgentConfig{{Name: "kimi", Registry: "kimi"}}
	home := t.TempDir()
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, home, testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	spec, err := r.Resolve(t.Context(), "kimi")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if spec.Command[0] != exe {
		t.Errorf("Command[0] = %q, want the kimi already on PATH at %q", spec.Command[0], exe)
	}
	if len(spec.Command) < 2 || spec.Command[1] != "acp" {
		t.Errorf("Command = %v, want the registry's args appended", spec.Command)
	}
	if entries, err := os.ReadDir(home); err == nil && len(entries) != 0 {
		t.Errorf("home has %d entries, want nothing downloaded", len(entries))
	}
}

// archive builds a tar.gz or zip holding one executable file, and returns the
// bytes with their sha256.
func archive(t *testing.T, format, name string) ([]byte, string) {
	t.Helper()
	const body = "#!/bin/sh\nexit 0\n"

	var buf bytes.Buffer
	switch format {
	case "tar.gz":
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write the tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write the tar body: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("close the tar: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("close the gzip: %v", err)
		}
	case "zip":
		zw := zip.NewWriter(&buf)
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create the zip entry: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write the zip body: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close the zip: %v", err)
		}
	default:
		t.Fatalf("unknown archive format %q", format)
	}

	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// downloadable builds a one-agent snapshot whose only distribution is a binary
// archive served by the returned test server.
func downloadable(t *testing.T, format, ext, sha string, body []byte) map[string]entry {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	e := entry{
		ID:      "demo",
		Name:    "Demo Agent",
		Version: "1.2.3",
		Distribution: distribution{Binary: map[string]binaryDist{
			platformKey(): {
				Archive: srv.URL + "/demo-" + format + ext,
				Cmd:     "./demo",
				Args:    []string{"acp"},
				SHA256:  sha,
			},
		}},
	}
	return map[string]entry{"demo": e}
}

func TestResolveBinaryDownload(t *testing.T) {
	for _, tc := range []struct{ format, ext string }{
		{"tar.gz", ".tar.gz"},
		{"zip", ".zip"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			emptyPath(t)
			body, sha := archive(t, tc.format, "demo")
			snapshot := downloadable(t, tc.format, tc.ext, sha, body)

			home := t.TempDir()
			agents := []config.AgentConfig{{Name: "demo", Registry: "demo"}}
			r, err := newWithSnapshot(config.RegistryConfig{SnapshotOnly: true}, agents, home, testLogger(), snapshot)
			if err != nil {
				t.Fatalf("newWithSnapshot failed: %v", err)
			}

			spec, err := r.Resolve(t.Context(), "demo")
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}

			want := filepath.Join(home, "agents", "demo", "1.2.3", "demo")
			if spec.Command[0] != want {
				t.Errorf("Command[0] = %q, want %q", spec.Command[0], want)
			}
			if len(spec.Command) != 2 || spec.Command[1] != "acp" {
				t.Errorf("Command = %v, want the archive's args appended", spec.Command)
			}
			if !executable(want) {
				t.Errorf("%s is not executable after extraction", want)
			}

			// A second Resolve reuses what is already unpacked.
			again, err := r.Resolve(t.Context(), "demo")
			if err != nil {
				t.Fatalf("second Resolve failed: %v", err)
			}
			if !slices.Equal(again.Command, spec.Command) {
				t.Errorf("second Resolve gave %v, want %v", again.Command, spec.Command)
			}
		})
	}
}

func TestChecksumMismatch(t *testing.T) {
	emptyPath(t)
	body, _ := archive(t, "tar.gz", "demo")
	const wrong = "0000000000000000000000000000000000000000000000000000000000000000"
	snapshot := downloadable(t, "tar.gz", ".tar.gz", wrong, body)

	home := t.TempDir()
	agents := []config.AgentConfig{{Name: "demo", Registry: "demo"}}
	r, err := newWithSnapshot(config.RegistryConfig{SnapshotOnly: true}, agents, home, testLogger(), snapshot)
	if err != nil {
		t.Fatalf("newWithSnapshot failed: %v", err)
	}

	if _, err := r.Resolve(t.Context(), "demo"); err == nil {
		t.Fatal("Resolve succeeded, want a checksum failure")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("Resolve error = %v, want it to name the checksum", err)
	}

	if _, err := os.Stat(filepath.Join(home, "agents", "demo", "1.2.3")); !os.IsNotExist(err) {
		t.Errorf("stat of the install directory = %v, want nothing unpacked", err)
	}
}

func TestGitHubProxyPrefix(t *testing.T) {
	const upstream = "https://github.com/o/r/releases/download/v1/a.tar.gz"
	for _, tc := range []struct{ proxy, want string }{
		{"", upstream},
		{"https://ghproxy.example", "https://ghproxy.example/" + upstream},
		{"https://ghproxy.example/", "https://ghproxy.example/" + upstream},
	} {
		if got := archiveURL(tc.proxy, upstream); got != tc.want {
			t.Errorf("archiveURL(%q) = %q, want %q", tc.proxy, got, tc.want)
		}
	}

	// And end to end: the download has to go through the proxy host, carrying
	// the upstream URL along as its path.
	emptyPath(t)
	body, sha := archive(t, "tar.gz", "demo")
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asked = append(asked, req.URL.Path)
		w.Write(body)
	}))
	defer srv.Close()

	snapshot := map[string]entry{"demo": {
		ID:      "demo",
		Version: "1.2.3",
		Distribution: distribution{Binary: map[string]binaryDist{
			platformKey(): {Archive: upstream, Cmd: "./demo", SHA256: sha},
		}},
	}}
	cfg := config.RegistryConfig{SnapshotOnly: true, GitHubProxy: srv.URL}
	agents := []config.AgentConfig{{Name: "demo", Registry: "demo"}}
	r, err := newWithSnapshot(cfg, agents, t.TempDir(), testLogger(), snapshot)
	if err != nil {
		t.Fatalf("newWithSnapshot failed: %v", err)
	}
	if _, err := r.Resolve(t.Context(), "demo"); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(asked) != 1 {
		t.Fatalf("the proxy saw %d requests, want 1", len(asked))
	}
	if !strings.Contains(asked[0], "github.com/o/r") {
		t.Errorf("the proxy was asked for %q, want the upstream URL as its path", asked[0])
	}
}

func TestEnvWhitelist(t *testing.T) {
	dir := emptyPath(t)
	exe := fakeExe(t, dir, "my-agent")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("https_proxy", "http://127.0.0.1:7897")
	t.Setenv("FERRY_HOME", "/tmp/ferry-home")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak-me")

	agents := []config.AgentConfig{{
		Name:    "mine",
		Command: []string{exe},
		Env:     map[string]string{"OPENAI_BASE_URL": "https://example.invalid/v1", "FERRY_TOKEN": "nope"},
	}}
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	spec, err := r.Resolve(t.Context(), "mine")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	for _, name := range []string{"PATH", "LANG", "https_proxy", "OPENAI_BASE_URL"} {
		if _, ok := envValue(spec.Env, name); !ok {
			t.Errorf("%s is missing from the environment", name)
		}
	}
	for _, name := range []string{"FERRY_HOME", "FERRY_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if v, ok := envValue(spec.Env, name); ok {
			t.Errorf("%s = %q was passed through, want it dropped", name, v)
		}
	}
	if !slices.IsSorted(spec.Env) {
		t.Error("the environment is not sorted, so two identical specs need not compare equal")
	}
}

func TestAgentsAvailability(t *testing.T) {
	dir := emptyPath(t)
	exe := fakeExe(t, dir, "my-agent")

	agents := []config.AgentConfig{
		{Name: "mine", Command: []string{exe}},
		{Name: "gone", Command: []string{filepath.Join(dir, "not-installed")}},
		{Name: "qwen-code", Registry: "qwen-code"},
		{Name: "opencode", Registry: "opencode"},
		{Name: "bogus", Registry: "no-such-agent"},
	}
	r, err := New(config.RegistryConfig{SnapshotOnly: true}, agents, t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	infos := r.Agents(t.Context())
	if len(infos) != len(agents) {
		t.Fatalf("Agents returned %d entries, want %d", len(infos), len(agents))
	}
	byName := make(map[string]ports.AgentInfo, len(infos))
	for _, info := range infos {
		byName[info.Name] = info
	}

	if got := byName["mine"]; !got.Available || got.Source != "command" || got.Reason != "" {
		t.Errorf("mine = %+v, want an available command agent with no reason", got)
	}
	if got := byName["gone"]; got.Available || !strings.Contains(got.Reason, "not found") {
		t.Errorf("gone = %+v, want it unavailable with a reason", got)
	}
	// qwen-code is npx only, and PATH has no npx.
	if got := byName["qwen-code"]; got.Available || !strings.Contains(got.Reason, "npx") {
		t.Errorf("qwen-code = %+v, want it unavailable because npx is missing", got)
	}
	// opencode ships a binary, so it is available but has to be fetched first.
	if got := byName["opencode"]; !got.Available || got.Reason != reasonDownload {
		t.Errorf("opencode = %+v, want it available with %q", got, reasonDownload)
	}
	if got := byName["opencode"]; got.Source != "registry" || got.Display != "OpenCode" {
		t.Errorf("opencode = %+v, want the registry's display name", got)
	}
	if got := byName["bogus"]; got.Available || !strings.Contains(got.Reason, "no registry agent") {
		t.Errorf("bogus = %+v, want it unavailable because the registry has no such agent", got)
	}

	// With npx installed, the npx-only agent becomes available.
	fakeExe(t, dir, "npx")
	if got := r.Agents(t.Context()); !got[2].Available || got[2].Reason != "" {
		t.Errorf("qwen-code = %+v once npx is installed, want it available", got[2])
	}
}
