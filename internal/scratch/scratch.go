// Package scratch orchestrates toolchain building from the scratch environment,
// including fetching, extraction, verification, and registration for mu builds.
package scratch

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/builtin"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
)

// Builder fetches, extracts, verifies, and registers toolchains.
type Builder struct {
	Store    cas.Store
	Registry *coordinator.ToolchainRegistry
	CacheDir string // directory for downloaded archives and extracted trees
}

// Build processes all toolchains in cfg. For each toolchain it checks the
// CAS for an existing manifest (cache hit -> skip). On cache miss it downloads,
// extracts, verifies, and registers the toolchain.
func (b *Builder) Build(ctx context.Context, cfg *config.ProjectConfig) error {
	for _, tc := range cfg.Toolchains {
		if err := b.buildOne(ctx, tc); err != nil {
			return fmt.Errorf("scratch build %s: %w", tc.Name, err)
		}
	}
	return nil
}

func (b *Builder) buildOne(ctx context.Context, tc config.Toolchain) error {
	name := tc.Name
	version := tc.Config.Version

	// Check CAS for existing manifest (cache hit -> skip).
	existing, err := b.Registry.Lookup(ctx, name, version)
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	if existing != nil {
		return nil
	}

	// Cache miss: fetch the archive.
	archivePath := filepath.Join(b.CacheDir, "downloads", name+"-"+version+archiveExt(tc.Config.URL))
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	if err := builtin.ForgeFetch(ctx, tc.Config.URL, tc.Config.SHA256, archivePath); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	// Extract the archive.
	extractDir := filepath.Join(b.CacheDir, "extract", name+"-"+version)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	if err := extract(archivePath, extractDir, tc.Config.StripPrefix); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Verify toolchain by running binary --version.
	if err := verify(ctx, extractDir, name); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	// Build manifest by walking extracted files and storing each in CAS.
	artifacts, err := b.storeArtifacts(ctx, extractDir)
	if err != nil {
		return fmt.Errorf("store artifacts: %w", err)
	}

	manifest := &coordinator.ToolchainManifest{
		Name:      name,
		Version:   version,
		Artifacts: artifacts,
	}

	if err := b.Registry.Register(ctx, manifest); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// storeArtifacts walks dir and stores each regular file in CAS, returning a
// map of relative path -> CAS digest string.
func (b *Builder) storeArtifacts(ctx context.Context, dir string) (map[string]string, error) {
	artifacts := make(map[string]string)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", rel, err)
		}
		defer f.Close()

		dgst, err := b.Store.Put(ctx, f)
		if err != nil {
			return fmt.Errorf("put %s: %w", rel, err)
		}

		artifacts[rel] = dgst.String()
		return nil
	})
	return artifacts, err
}

// verify runs "<name> --version" and checks for a zero exit code.
// It looks for the binary at bin/<name> first, then <name> at the root.
func verify(ctx context.Context, extractDir, name string) error {
	binPath := filepath.Join(extractDir, "bin", name)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		binPath = filepath.Join(extractDir, name)
	}

	// Try "version" (Go style) first, then "--version" (GNU style).
	for _, arg := range []string{"version", "--version"} {
		cmd := exec.CommandContext(ctx, binPath, arg)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s: neither 'version' nor '--version' succeeded", binPath)
}

// extract dispatches to the appropriate extractor based on the archive file extension.
func extract(archivePath, destDir, stripPrefix string) error {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archivePath, destDir, stripPrefix)
	case strings.HasSuffix(lower, ".tar.xz") || strings.HasSuffix(lower, ".txz"):
		return extractTarXz(archivePath, destDir, stripPrefix)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, destDir, stripPrefix)
	default:
		return fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
}

// extractTarGz extracts a .tar.gz archive with optional strip-prefix.
func extractTarGz(archivePath, destDir, stripPrefix string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	return extractTar(tar.NewReader(gz), destDir, stripPrefix)
}

// extractTarXz extracts a .tar.xz archive by piping through the xz command.
func extractTarXz(archivePath, destDir, stripPrefix string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	cmd := exec.Command("xz", "--decompress", "--stdout")
	cmd.Stdin = f

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("xz pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("xz start: %w", err)
	}

	extractErr := extractTar(tar.NewReader(stdout), destDir, stripPrefix)

	if err := cmd.Wait(); err != nil && extractErr == nil {
		return fmt.Errorf("xz: %w", err)
	}
	return extractErr
}

// extractTar reads entries from a tar reader and writes them to destDir,
// stripping the given prefix from entry names.
func extractTar(tr *tar.Reader, destDir, stripPrefix string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		name := stripPrefixFromPath(hdr.Name, stripPrefix)
		if name == "" || name == "." {
			continue
		}

		target := filepath.Join(destDir, name)

		// Guard against path traversal.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target) // remove any existing symlink
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// extractZip extracts a .zip archive with optional strip-prefix.
func extractZip(archivePath, destDir, stripPrefix string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		name := stripPrefixFromPath(f.Name, stripPrefix)
		if name == "" || name == "." {
			continue
		}

		target := filepath.Join(destDir, name)

		// Guard against path traversal.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("zip entry %s: %w", f.Name, err)
		}
		writeErr := writeFile(target, rc, f.Mode())
		rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

// writeFile creates (or truncates) a file at path and copies content from r.
func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// stripPrefixFromPath removes the leading prefix directory from a path.
// If stripPrefix is empty, the path is returned as-is.
func stripPrefixFromPath(path, stripPrefix string) string {
	if stripPrefix == "" {
		return path
	}
	// Normalise: ensure stripPrefix ends with "/".
	prefix := stripPrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):]
	}
	// The path might be the prefix directory itself.
	if strings.TrimSuffix(path, "/") == strings.TrimSuffix(stripPrefix, "/") {
		return ""
	}
	return path
}

// archiveExt extracts the compound extension from a URL path
// (e.g. ".tar.gz" from "https://example.com/go-1.0.tar.gz").
func archiveExt(rawURL string) string {
	base := filepath.Base(rawURL)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".zip"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			return ext
		}
	}
	return filepath.Ext(base)
}

// sha256File computes the SHA-256 hex digest of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
