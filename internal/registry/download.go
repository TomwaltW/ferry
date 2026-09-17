package registry

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxArchive caps how much is read from an archive entry, so a malformed or
// hostile archive cannot fill the disk.
const maxArchive = 1 << 30 // 1 GiB

// download makes sure the agent's binary is unpacked under
// <home>/agents/<id>/<version>/ and returns the command to run it. An archive
// that is already unpacked is reused; a failed download leaves nothing behind.
func (r *resolver) download(ctx context.Context, l launch) ([]string, error) {
	dir := filepath.Join(r.home, "agents", l.entry.ID, l.entry.Version)
	target, err := binaryPath(dir, l.binary.Cmd)
	if err != nil {
		return nil, err
	}
	if executable(target) {
		return append([]string{target}, l.binary.Args...), nil
	}

	src := archiveURL(r.cfg.GitHubProxy, l.binary.Archive)
	r.log.Info("downloading agent", "agent", l.entry.ID, "version", l.entry.Version, "url", src)

	archive, err := r.fetchArchive(ctx, src, l.binary.SHA256)
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive)

	if err := r.unpack(archive, l.binary.Archive, dir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err != nil {
		return nil, fmt.Errorf("registry: the archive of %s has no %s: %w", l.entry.ID, l.binary.Cmd, err)
	}
	if !executable(target) {
		if err := os.Chmod(target, 0o755); err != nil {
			return nil, fmt.Errorf("registry: make %s executable: %w", target, err)
		}
	}
	return append([]string{target}, l.binary.Args...), nil
}

// archiveURL applies the configured GitHub proxy as a prefix, joining with a
// single slash so a proxy written with or without a trailing one both work.
func archiveURL(proxy, archive string) string {
	if proxy == "" {
		return archive
	}
	return strings.TrimSuffix(proxy, "/") + "/" + archive
}

// fetchArchive downloads to a temporary file and checks its sha256 before
// anything is unpacked. The file is the caller's to remove.
func (r *resolver) fetchArchive(ctx context.Context, src, want string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", fmt.Errorf("registry: request %s: %w", src, err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry: download %s: %w", src, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry: download %s: %s", src, resp.Status)
	}

	f, err := os.CreateTemp("", "ferry-agent-*.archive")
	if err != nil {
		return "", fmt.Errorf("registry: create a temporary file: %w", err)
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, maxArchive)); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("registry: download %s: %w", src, err)
	}

	got := hex.EncodeToString(sum.Sum(nil))
	if !strings.EqualFold(got, want) {
		os.Remove(f.Name())
		return "", fmt.Errorf("registry: checksum mismatch for %s: got %s, want %s", src, got, want)
	}
	return f.Name(), nil
}

// unpack extracts archive into dir. It builds the tree next to dir and moves it
// into place at the end, so an interrupted extraction never looks finished.
// name is the archive's own name, which is what decides the format.
func (r *resolver) unpack(archive, name, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("registry: create %s: %w", filepath.Dir(dir), err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".unpack-*")
	if err != nil {
		return fmt.Errorf("registry: create a staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	switch {
	case hasSuffix(name, ".tar.gz", ".tgz"):
		err = extractTarGz(archive, staging)
	case hasSuffix(name, ".zip"):
		err = extractZip(archive, staging)
	default:
		err = fmt.Errorf("registry: %s is not a .tar.gz or .zip archive", name)
	}
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("registry: replace %s: %w", dir, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return fmt.Errorf("registry: install into %s: %w", dir, err)
	}
	return nil
}

// hasSuffix reports whether the archive's path, ignoring any query string, ends
// in one of the given extensions.
func hasSuffix(name string, exts ...string) bool {
	if u, err := url.Parse(name); err == nil && u.Path != "" {
		name = u.Path
	}
	name = strings.ToLower(name)
	for _, ext := range exts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// binaryPath turns a registry cmd such as "./opencode" into an absolute path
// under dir, refusing anything that would point outside it.
func binaryPath(dir, cmd string) (string, error) {
	rel := path.Clean(strings.TrimPrefix(cmd, "./"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", fmt.Errorf("registry: %q is not a path inside the archive", cmd)
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// entryPath is binaryPath for an archive member, which may name a directory and
// must likewise stay inside dir.
func entryPath(dir, name string) (string, error) {
	rel := path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("registry: archive entry %q has no name", name)
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

func extractTarGz(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("registry: open the archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("registry: read the archive as gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("registry: read the archive: %w", err)
		}

		dest, err := entryPath(dir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("registry: create %s: %w", dest, err)
			}
		case tar.TypeReg:
			if err := writeFile(dest, tr, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		default:
			// Symlinks, devices and the rest have no place in an agent
			// archive; skipping them keeps extraction predictable.
			continue
		}
	}
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("registry: read the archive as zip: %w", err)
	}
	defer zr.Close()

	for _, member := range zr.File {
		dest, err := entryPath(dir, member.Name)
		if err != nil {
			return err
		}
		if member.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("registry: create %s: %w", dest, err)
			}
			continue
		}
		if !member.Mode().IsRegular() {
			continue
		}

		rc, err := member.Open()
		if err != nil {
			return fmt.Errorf("registry: read %s from the archive: %w", member.Name, err)
		}
		err = writeFile(dest, rc, member.Mode().Perm())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeFile creates one extracted file, making its parent directories and
// keeping the archive's permission bits so an executable stays executable.
func writeFile(dest string, src io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("registry: create %s: %w", filepath.Dir(dest), err)
	}
	if perm == 0 {
		perm = 0o644
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("registry: create %s: %w", dest, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(src, maxArchive)); err != nil {
		return fmt.Errorf("registry: write %s: %w", dest, err)
	}
	return f.Close()
}
