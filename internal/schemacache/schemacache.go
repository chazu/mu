// Package schemacache implements a content-addressed cache for CUE
// schema modules referenced by mu plugins.
//
// Schemas are keyed by (module, version). Identical content for the
// same key is idempotent; mismatched content for an existing key is
// rejected — versions are immutable once cached. The cache is
// append-only: older versions are retained so that historical imports
// can be reclassified against the schema they were originally
// understood under.
//
// On-disk layout:
//
//	<root>/<module>/<version>/<file...>.cue
//
// where <module> is the CUE module path (e.g. "mu/aws") used verbatim
// as a directory tree.
package schemacache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned when a (module, version) is not cached.
var ErrNotFound = errors.New("schemacache: not found")

// ErrVersionMismatch is returned when an Insert tries to write content
// for an existing (module, version) that differs from what is already
// stored. Versions are immutable.
var ErrVersionMismatch = errors.New("schemacache: version content mismatch")

// Cache stores CUE schema modules on the local filesystem.
type Cache struct {
	root string
}

// New returns a Cache rooted at the given directory. The directory is
// created if it does not exist.
func New(root string) (*Cache, error) {
	if root == "" {
		return nil, errors.New("schemacache: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("schemacache: create root: %w", err)
	}
	return &Cache{root: root}, nil
}

// Root returns the on-disk root of the cache.
func (c *Cache) Root() string { return c.root }

// versionDir is the on-disk directory for a (module, version) pair.
func (c *Cache) versionDir(module, version string) string {
	return filepath.Join(c.root, filepath.FromSlash(module), version)
}

// Has reports whether the cache contains the given module and version.
func (c *Cache) Has(module, version string) bool {
	if err := validateKey(module, version); err != nil {
		return false
	}
	info, err := os.Stat(c.versionDir(module, version))
	return err == nil && info.IsDir()
}

// Files returns the relative paths (slash-separated) of all .cue files
// stored for the given module and version, sorted lexicographically.
// Returns ErrNotFound if the version is not cached.
func (c *Cache) Files(module, version string) ([]string, error) {
	if err := validateKey(module, version); err != nil {
		return nil, err
	}
	dir := c.versionDir(module, version)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".cue") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	return rels, nil
}

// Read returns the contents of the named .cue file within the cached
// (module, version). The relative path is slash-separated. Returns
// ErrNotFound if either the version or the file is missing.
func (c *Cache) Read(module, version, relPath string) ([]byte, error) {
	if err := validateKey(module, version); err != nil {
		return nil, err
	}
	if err := validateRelPath(relPath); err != nil {
		return nil, err
	}
	if !c.Has(module, version) {
		return nil, ErrNotFound
	}
	full := filepath.Join(c.versionDir(module, version), filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// File represents a single CUE source file to be inserted under a
// module/version. RelPath is slash-separated and must not escape the
// version directory.
type File struct {
	RelPath string
	Content []byte
}

// Insert stores the given files under (module, version). If the
// version is already cached, Insert verifies that the incoming content
// matches byte-for-byte; on mismatch it returns ErrVersionMismatch.
// On match, Insert is a no-op.
//
// Insert is atomic per call: it materializes content under a temp dir
// and renames it into place, so concurrent inserts of the same key
// never observe a partially-written version.
func (c *Cache) Insert(module, version string, files []File) error {
	if err := validateKey(module, version); err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("schemacache: at least one file required")
	}
	for _, f := range files {
		if err := validateRelPath(f.RelPath); err != nil {
			return fmt.Errorf("schemacache: file %q: %w", f.RelPath, err)
		}
		if !strings.HasSuffix(f.RelPath, ".cue") {
			return fmt.Errorf("schemacache: file %q: only .cue files are accepted", f.RelPath)
		}
	}

	target := c.versionDir(module, version)
	if c.Has(module, version) {
		return c.verifyMatches(target, files)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("schemacache: create module dir: %w", err)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(target), ".tmp-"+version+"-*")
	if err != nil {
		return fmt.Errorf("schemacache: tmp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	for _, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.RelPath))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("schemacache: mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, f.Content, 0o644); err != nil {
			return fmt.Errorf("schemacache: write %s: %w", dst, err)
		}
	}

	if err := os.Rename(tmp, target); err != nil {
		// Lost a race: another writer materialized the same version
		// between our Has check and our rename. Verify content matches.
		if _, statErr := os.Stat(target); statErr == nil {
			return c.verifyMatches(target, files)
		}
		return fmt.Errorf("schemacache: rename: %w", err)
	}
	return nil
}

// verifyMatches walks the target dir and compares it to the supplied
// files. The set of paths and their bytes must match exactly.
func (c *Cache) verifyMatches(dir string, files []File) error {
	want := make(map[string][]byte, len(files))
	for _, f := range files {
		want[f.RelPath] = f.Content
	}

	seen := make(map[string]bool, len(files))
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		seen[relSlash] = true
		expected, ok := want[relSlash]
		if !ok {
			return fmt.Errorf("%w: existing file %q not in incoming set", ErrVersionMismatch, relSlash)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytesEqual(got, expected) {
			return fmt.Errorf("%w: file %q content differs", ErrVersionMismatch, relSlash)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for rel := range want {
		if !seen[rel] {
			return fmt.Errorf("%w: incoming file %q missing from cached version", ErrVersionMismatch, rel)
		}
	}
	return nil
}

// Hash returns a stable hash of the cached version's contents. Useful
// for cross-checking that two cache instances agree. Returns
// ErrNotFound if the version is not cached.
func (c *Cache) Hash(module, version string) (string, error) {
	if !c.Has(module, version) {
		return "", ErrNotFound
	}
	files, err := c.Files(module, version)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		fmt.Fprintf(h, "%s\x00", rel)
		f, err := os.Open(filepath.Join(c.versionDir(module, version), filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateKey(module, version string) error {
	if module == "" {
		return errors.New("schemacache: module is required")
	}
	if version == "" {
		return errors.New("schemacache: version is required")
	}
	if strings.Contains(module, "..") || strings.HasPrefix(module, "/") {
		return fmt.Errorf("schemacache: invalid module %q", module)
	}
	if strings.ContainsAny(version, "/\\") || version == "." || version == ".." {
		return fmt.Errorf("schemacache: invalid version %q", version)
	}
	return nil
}

func validateRelPath(rel string) error {
	if rel == "" {
		return errors.New("relative path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("relative path %q must not be absolute", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean != rel {
		return fmt.Errorf("relative path %q is not in canonical form", rel)
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return fmt.Errorf("relative path %q escapes module dir", rel)
	}
	return nil
}
