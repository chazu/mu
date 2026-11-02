// Package archive is mu's only archive extractor. Scratch toolchains, catalog
// source archives, and plugin CAS bundles all expand through it, so they share
// one set of rules:
//
//   - Every write goes through an os.Root opened on the destination, so no
//     entry, symlink, or crafted path can create or modify anything outside it.
//   - An entry that is absolute or escapes the destination is an error, never
//     silently skipped. Hard links are always rejected; symlinks only when the
//     Policy allows them, and then only relative targets that stay in the tree.
//   - Entries and expanded bytes count against a Budget, so a small
//     compressed input cannot expand into unbounded disk use or inode churn.
//     Limits are enforced on the bytes actually written, not only on sizes the
//     archive declares, and skipped entries count too.
//
// Callers own cleanup: on error the destination may hold a partial tree.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
)

// Format is a supported archive container.
type Format int

const (
	TarGz Format = iota + 1 // .tar.gz, .tgz
	TarXz                   // .tar.xz, .txz (decompressed with the xz command)
	Zip                     // .zip
)

// DetectFormat reports the archive format implied by name's extension.
func DetectFormat(name string) (Format, bool) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return TarGz, true
	case strings.HasSuffix(lower, ".tar.xz") || strings.HasSuffix(lower, ".txz"):
		return TarXz, true
	case strings.HasSuffix(lower, ".zip"):
		return Zip, true
	}
	return 0, false
}

// Policy configures one extraction.
type Policy struct {
	// Limits is the budget profile (Toolchain or Plugin); MU_ARCHIVE_MAX_BYTES
	// overrides its byte limits.
	Limits Limits
	// AllowSymlinks permits symlink entries whose target is relative and,
	// resolved against the entry's directory, stays inside the tree. When
	// false every symlink is an error.
	AllowSymlinks bool
	// StripPrefix removes this leading directory from entry names. Entries
	// outside it are extracted unchanged; the prefix directory itself is
	// skipped.
	StripPrefix string
}

// maxLinkTarget bounds a zip symlink's target, which is stored as content.
const maxLinkTarget = 4096

// ExtractFile expands the archive at archivePath into destDir (created if
// missing).
func ExtractFile(ctx context.Context, archivePath, destDir string, format Format, p Policy) error {
	switch format {
	case TarGz:
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
		return ExtractTar(ctx, gz, destDir, p)
	case TarXz:
		return extractTarXz(ctx, archivePath, destDir, p)
	case Zip:
		return extractZip(ctx, archivePath, destDir, p)
	}
	return fmt.Errorf("unsupported archive format %d", format)
}

// ExtractTar expands an uncompressed tar stream into destDir (created if
// missing).
func ExtractTar(ctx context.Context, r io.Reader, destDir string, p Policy) error {
	x, err := newExtractor(destDir, p)
	if err != nil {
		return err
	}
	defer x.root.Close()
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return x.finish()
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		if err := x.budget.Entry(hdr.Name); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			continue // archive-wide metadata (e.g. git archive's commit id)
		case tar.TypeDir:
			err = x.dir(hdr.Name)
		case tar.TypeReg, tar.TypeRegA:
			err = x.file(hdr.Name, hdr.Size, fs.FileMode(hdr.Mode), tr)
		case tar.TypeSymlink:
			err = x.symlink(hdr.Name, hdr.Linkname)
		case tar.TypeLink:
			err = fmt.Errorf("archive entry %q is a hard link; hard links are not supported", hdr.Name)
		default:
			err = fmt.Errorf("archive entry %q has unsupported type %q", hdr.Name, hdr.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}

func extractTarXz(ctx context.Context, archivePath, destDir string, p Policy) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "xz", "--decompress", "--stdout")
	cmd.Stdin = f
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("xz pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("xz start: %w", err)
	}
	extractErr := ExtractTar(ctx, stdout, destDir, p)
	if extractErr != nil {
		// Stop decompression we no longer read; otherwise xz blocks on a full
		// pipe and Wait never returns.
		_ = cmd.Process.Kill()
	}
	if err := cmd.Wait(); err != nil && extractErr == nil {
		return fmt.Errorf("xz: %w", err)
	}
	return extractErr
}

func extractZip(ctx context.Context, archivePath, destDir string, p Policy) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()
	x, err := newExtractor(destDir, p)
	if err != nil {
		return err
	}
	defer x.root.Close()
	// The central directory is read up front, so the entry count can be
	// rejected before anything is written.
	for _, f := range zr.File {
		if err := x.budget.Entry(f.Name); err != nil {
			return err
		}
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			err = x.dir(f.Name)
		case mode&fs.ModeSymlink != 0:
			err = x.zipSymlink(f)
		case mode.IsRegular():
			// Declared sizes can lie; Copy enforces the bytes actually inflated.
			size := int64(min(f.UncompressedSize64, math.MaxInt64))
			var rc io.ReadCloser
			if rc, err = f.Open(); err != nil {
				return fmt.Errorf("zip entry %s: %w", f.Name, err)
			}
			err = x.file(f.Name, size, mode, rc)
			rc.Close()
		default:
			err = fmt.Errorf("archive entry %q has unsupported mode %s", f.Name, mode)
		}
		if err != nil {
			return err
		}
	}
	return x.finish()
}

type extractor struct {
	root   *os.Root
	budget *Budget
	policy Policy
	seen   map[string]bool // cleaned path -> is directory
	links  []string        // symlinks created, checked once the tree is final
}

func newExtractor(destDir string, p Policy) (*extractor, error) {
	budget, err := NewBudget(p.Limits)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create extraction directory: %w", err)
	}
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return nil, err
	}
	return &extractor{root: root, budget: budget, policy: p, seen: map[string]bool{}}, nil
}

// target validates an entry name and maps it to its slash-separated path
// inside the root. skip reports an entry that names the root (or the strip
// prefix directory) itself.
func (x *extractor) target(name string, isDir bool) (rel string, skip bool, err error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false, fmt.Errorf("archive entry has an invalid path %q", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", false, fmt.Errorf("archive entry %q is absolute", name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	clean = stripPrefix(clean, x.policy.StripPrefix)
	if clean == "" || clean == "." {
		if !isDir {
			return "", false, fmt.Errorf("archive entry %q names the extraction root", name)
		}
		return "", true, nil
	}
	if wasDir, ok := x.seen[clean]; ok && !(wasDir && isDir) {
		return "", false, fmt.Errorf("archive contains duplicate entry %q", clean)
	}
	x.seen[clean] = isDir
	return clean, false, nil
}

func (x *extractor) dir(name string) error {
	rel, skip, err := x.target(name, true)
	if err != nil || skip {
		return err
	}
	if err := x.root.MkdirAll(rel, 0o755); err != nil {
		return fmt.Errorf("create directory %q: %w", rel, err)
	}
	return nil
}

func (x *extractor) file(name string, size int64, mode fs.FileMode, r io.Reader) error {
	rel, _, err := x.target(name, false)
	if err != nil {
		return err
	}
	if err := x.budget.Declared(name, size); err != nil {
		return err
	}
	if err := x.parent(rel); err != nil {
		return err
	}
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	// O_EXCL also refuses to write through anything already at rel,
	// including a symlink created by an earlier entry.
	f, err := x.root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create %q: %w", rel, err)
	}
	_, copyErr := x.budget.Copy(f, r, name)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("extract %q: %w", rel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %q: %w", rel, closeErr)
	}
	return nil
}

func (x *extractor) symlink(name, linkTarget string) error {
	if !x.policy.AllowSymlinks {
		return fmt.Errorf("archive entry %q is a symlink; symlinks are not allowed here", name)
	}
	rel, _, err := x.target(name, false)
	if err != nil {
		return err
	}
	if linkTarget == "" || strings.ContainsRune(linkTarget, 0) {
		return fmt.Errorf("symlink %q has an invalid target", name)
	}
	if path.IsAbs(linkTarget) {
		return fmt.Errorf("symlink %q has absolute target %q", name, linkTarget)
	}
	resolved := path.Join(path.Dir(rel), linkTarget)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %q -> %q escapes the extraction directory", name, linkTarget)
	}
	if err := x.parent(rel); err != nil {
		return err
	}
	if err := x.root.Symlink(linkTarget, rel); err != nil {
		return fmt.Errorf("create symlink %q: %w", rel, err)
	}
	x.links = append(x.links, rel)
	return nil
}

// finish checks every symlink against the final tree. The per-entry check is
// lexical, so a target like "up/.." is accepted even when "up" is itself a
// link to "..". Resolving through the root catches such chains: a link that
// resolves outside the tree is an error. A dangling link is harmless, since
// the tree no longer changes.
func (x *extractor) finish() error {
	for _, rel := range x.links {
		_, err := x.root.Stat(rel)
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("symlink %q does not resolve inside the extraction directory: %w", rel, err)
		}
	}
	return x.root.Close()
}

func (x *extractor) zipSymlink(f *zip.File) error {
	if !x.policy.AllowSymlinks {
		return fmt.Errorf("archive entry %q is a symlink; symlinks are not allowed here", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxLinkTarget+1))
	if err != nil {
		return fmt.Errorf("zip entry %s: %w", f.Name, err)
	}
	if len(data) > maxLinkTarget {
		return fmt.Errorf("symlink %q target is longer than %d bytes", f.Name, maxLinkTarget)
	}
	return x.symlink(f.Name, string(data))
}

func (x *extractor) parent(rel string) error {
	if dir := path.Dir(rel); dir != "." {
		if err := x.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent of %q: %w", rel, err)
		}
	}
	return nil
}

// stripPrefix removes the leading prefix directory from a cleaned path. A path
// outside the prefix is returned unchanged; the prefix itself becomes "".
func stripPrefix(p, prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return p
	}
	prefix = path.Clean(prefix)
	if p == prefix {
		return ""
	}
	if rest, ok := strings.CutPrefix(p, prefix+"/"); ok {
		return rest
	}
	return p
}
