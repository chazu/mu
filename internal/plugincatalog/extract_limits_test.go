package plugincatalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/archive"
)

func catalogArchive(t testing.TB, n int, size int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	zeros := make([]byte, size)
	for i := 0; i < n; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("pkg/f%d", i), Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(zeros)
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func expandedBytes(dir string) (total int64) {
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func TestExtractArchiveEnforcesLimits(t *testing.T) {
	cases := []struct {
		name, env, value string
		n                int
		size             int64
	}{
		{"file bytes", archive.EnvMaxBytes, "100", 1, 101},
		{"total bytes", archive.EnvMaxBytes, "1048576", 4, 512 << 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			path := filepath.Join(t.TempDir(), "a.tar.gz")
			os.WriteFile(path, catalogArchive(t, tc.n, tc.size), 0o644)
			dest := filepath.Join(t.TempDir(), "out")
			err := ExtractArchive(path, dest)
			if !errors.Is(err, archive.ErrExceeded) {
				t.Fatalf("ExtractArchive = %v, want ErrExceeded", err)
			}
			if total := expandedBytes(dest); total > 1<<20 {
				t.Fatalf("wrote %d bytes past the budget", total)
			}
		})
	}
}

// FuzzExtractArchive feeds arbitrary bytes as a catalog archive. Extraction
// may fail, but must never panic, write outside the destination, or exceed
// the configured budget. Seeds run as part of the normal test suite.
func FuzzExtractArchive(f *testing.F) {
	f.Add(catalogArchive(f, 2, 3))
	f.Add(catalogArchive(f, 40, 1))
	f.Add(catalogArchive(f, 2, 64<<10))
	f.Add([]byte{0x1f, 0x8b, 0x08})
	f.Add([]byte("not an archive"))
	f.Fuzz(func(t *testing.T, data []byte) {
		t.Setenv(archive.EnvMaxBytes, "65536")
		parent := t.TempDir()
		path := filepath.Join(parent, "in.tar.gz")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(parent, "out")
		_ = ExtractArchive(path, dest)
		entries, _ := os.ReadDir(parent)
		for _, e := range entries {
			if e.Name() != "in.tar.gz" && e.Name() != "out" {
				t.Fatalf("extraction wrote outside destination: %s", e.Name())
			}
		}
		if total := expandedBytes(dest); total > 65536 {
			t.Fatalf("expanded %d bytes past a 65536-byte budget", total)
		}
	})
}

func TestExtractArchiveRejectsLinks(t *testing.T) {
	for _, hdr := range []*tar.Header{
		{Name: "pkg/link", Linkname: "target", Typeflag: tar.TypeSymlink},
		{Name: "pkg/hard", Linkname: "pkg/f", Typeflag: tar.TypeLink},
	} {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		tw.WriteHeader(hdr)
		tw.Close()
		zw.Close()
		path := filepath.Join(t.TempDir(), "a.tar.gz")
		os.WriteFile(path, buf.Bytes(), 0o644)
		if err := ExtractArchive(path, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Fatalf("%s accepted", hdr.Name)
		}
	}
}
