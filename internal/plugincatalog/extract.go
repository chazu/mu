package plugincatalog

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractArchive extracts a catalog source archive into destDir. Only regular
// files and directories are accepted; symlinks and hard links are rejected so
// an untrusted archive cannot write outside the destination or create an
// executable path with surprising resolution behavior.
func ExtractArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read gzip archive: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create extraction directory: %w", err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]struct{}{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		name, err := safeArchivePath(hdr.Name)
		if err != nil {
			return err
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("archive contains duplicate entry %q", name)
		}
		seen[name] = struct{}{}
		dest := filepath.Join(destDir, filepath.FromSlash(name))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("create directory %q: %w", name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return fmt.Errorf("create parent for %q: %w", name, err)
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", name, err)
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("extract archive file %q: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close archive file %q: %w", name, closeErr)
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", name, hdr.Typeflag)
		}
	}
	return nil
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("archive entry has invalid path")
	}
	name = filepath.ToSlash(name)
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive entry %q is absolute", name)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q escapes extraction directory", name)
	}
	return clean, nil
}
