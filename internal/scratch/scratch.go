// Package scratch orchestrates toolchain building from the scratch environment,
// including fetching, extraction, verification, and registration for mu builds.
package scratch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/archive"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/registryhttp"
)

// Builder fetches, extracts, verifies, and registers toolchains.
type Builder struct {
	NoCache  bool // rebuild toolchain results; verified archives remain inputs
	Store    cas.Store
	Registry *coordinator.ToolchainRegistry
	CacheDir string    // directory for downloaded archives and extracted trees
	Warnings io.Writer // non-fatal notices (skipped out-of-tree symlinks); nil means os.Stderr
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
	var existing *coordinator.ToolchainManifest
	var err error
	if !b.NoCache {
		existing, err = b.Registry.Lookup(ctx, name, version)
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		existing = nil
	} // receipt caches are optional; verified archives can rebuild them
	if existing != nil {
		return nil
	}

	// Cache miss: fetch the archive.
	archivePath := filepath.Join(b.CacheDir, "downloads", name+"-"+version+archiveExt(tc.Config.URL))
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	// An extracted tree alone cannot prove the configured archive identity.
	// Reuse only a local archive whose bytes match the declared SHA-256.
	cached := func() bool {
		f, err := os.Open(archivePath)
		if err != nil {
			return false
		}
		defer f.Close()
		actual, err := cas.ComputeDigest(cas.ContextReader(ctx, f))
		return err == nil && actual.Hash == tc.Config.SHA256
	}()
	if !cached {
		if err := registryhttp.Fetch(ctx, tc.Config.URL, tc.Config.SHA256, archivePath); err != nil {
			return fmt.Errorf("required toolchain archive unavailable locally: %w", err)
		}
	}

	// Extract into a fresh private staging directory. Its files are stored in
	// CAS below and the tree is then discarded, so a previous or concurrent
	// run can never contribute stale files to this manifest. A crash leaves
	// only a ".extract-*" directory, which the staging sweeper reclaims.
	extractRoot := filepath.Join(b.CacheDir, "extract")
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	extractDir, err := os.MkdirTemp(extractRoot, ".extract-"+name+"-"+version+"-*")
	if err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extract(ctx, archivePath, extractDir, tc.Config.StripPrefix, name); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Verify toolchain by running binary --version.
	if err := verify(ctx, extractDir, name); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	// Build manifest by walking extracted files and storing each in CAS.
	tree, err := b.storeArtifacts(ctx, extractDir)
	if err != nil {
		return fmt.Errorf("store artifacts: %w", err)
	}
	if len(tree.skipped) > 0 {
		w := b.Warnings
		if w == nil {
			w = os.Stderr
		}
		fmt.Fprintf(w, "warning: toolchain %s %s: %d symlink(s) leaving the toolchain tree not stored: %s\n",
			name, version, len(tree.skipped), strings.Join(tree.skipped, ", "))
	}

	manifest := &coordinator.ToolchainManifest{
		Name:      name,
		Version:   version,
		Artifacts: tree.artifacts,
		Modes:     tree.modes,
		Symlinks:  tree.links,
	}

	if err := b.Registry.Register(ctx, manifest); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// storedTree is a toolchain tree as recorded in its manifest: slash-separated
// relative paths of regular files (CAS digest strings and permission bits) and
// of in-tree symlinks (their relative targets). skipped lists symlinks that
// leave the tree, which are not recorded.
type storedTree struct {
	artifacts map[string]string
	modes     map[string]uint32
	links     map[string]string
	skipped   []string
}

// storeArtifacts walks dir and stores each regular file in CAS. Symlinks are
// never read through (an archive could point one at ~/.ssh and get its
// content into CAS): a relative link that stays inside the tree is recorded
// as a link, so the sandbox can recreate it; any other link is skipped.
func (b *Builder) storeArtifacts(ctx context.Context, dir string) (tree storedTree, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return tree, err
	}
	defer root.Close()
	tree.artifacts = make(map[string]string)
	tree.modes = make(map[string]uint32)
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			target, err := root.Readlink(rel)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", rel, err)
			}
			if filepath.IsAbs(target) || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), target)) {
				tree.skipped = append(tree.skipped, rel)
				return nil
			}
			if tree.links == nil {
				tree.links = make(map[string]string)
			}
			tree.links[rel] = target
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: unsupported file type %s", rel, d.Type())
		}
		f, err := root.Open(rel)
		if err != nil {
			return fmt.Errorf("open %s: %w", rel, err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("open %s: not a regular file", rel)
		}
		dgst, err := b.Store.Put(ctx, f)
		if err != nil {
			return fmt.Errorf("put %s: %w", rel, err)
		}
		tree.artifacts[rel] = dgst.String()
		tree.modes[rel] = uint32(info.Mode().Perm())
		return nil
	})
	sort.Strings(tree.skipped)
	return tree, err
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

// extract expands a toolchain archive (by file extension) into destDir. A
// non-archive download is installed as bin/<name>. Toolchains may contain
// symlinks, but only relative ones that stay inside the tree.
func extract(ctx context.Context, archivePath, destDir, stripPrefix, name string) error {
	format, ok := archive.DetectFormat(archivePath)
	if !ok {
		return extractRawBinary(archivePath, destDir, name)
	}
	return archive.ExtractFile(ctx, archivePath, destDir, format, archive.Policy{
		Limits:        archive.Toolchain,
		AllowSymlinks: true,
		StripPrefix:   stripPrefix,
	})
}

// extractRawBinary copies a single binary file to <destDir>/bin/<name> and
// makes it executable. name is the toolchain name, so verify finds the binary
// where it looks for it regardless of how the download or staging dir is named.
func extractRawBinary(srcPath, destDir, name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("toolchain name %q is not a valid binary file name", name)
	}
	binDir := filepath.Join(destDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	destPath := filepath.Join(binDir, name)

	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
