package scratch

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chazu/mu/internal/archivelimit"
)

// tarStream builds an uncompressed tar of n entries, each with size bytes of
// zeros (zeros compress to almost nothing, like a decompression bomb).
func tarStream(t testing.TB, n int, size int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	zeros := make([]byte, size)
	for i := 0; i < n; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%06d", i), Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(zeros); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func treeBytes(t *testing.T, dir string) (files int, total int64) {
	t.Helper()
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			files++
			total += info.Size()
		}
		return nil
	})
	return files, total
}

func writeArchive(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractTarGzEnforcesEntryLimit(t *testing.T) {
	t.Setenv(archivelimit.EnvMaxEntries, "5")
	dest := t.TempDir()
	err := extractTarGz(writeArchive(t, "a.tar.gz", gz(t, tarStream(t, 6, 1))), dest, "")
	if !errors.Is(err, archivelimit.ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if err := extractTarGz(writeArchive(t, "b.tar.gz", gz(t, tarStream(t, 5, 1))), t.TempDir(), ""); err != nil {
		t.Fatalf("archive at the entry limit rejected: %v", err)
	}
}

func TestExtractTarGzStopsDecompressionBomb(t *testing.T) {
	const limit = 1 << 20
	t.Setenv(archivelimit.EnvMaxTotalBytes, fmt.Sprint(limit))
	bomb := gz(t, tarStream(t, 8, limit/2)) // 4 MiB expanded from a few KiB
	if len(bomb) > 64<<10 {
		t.Fatalf("fixture not bomb-like: %d compressed bytes", len(bomb))
	}
	dest := t.TempDir()
	err := extractTarGz(writeArchive(t, "bomb.tar.gz", bomb), dest, "")
	if !errors.Is(err, archivelimit.ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if _, total := treeBytes(t, dest); total > limit {
		t.Fatalf("wrote %d bytes past a %d-byte budget", total, limit)
	}
}

func TestExtractTarGzRejectsOversizeFileByHeader(t *testing.T) {
	t.Setenv(archivelimit.EnvMaxFileBytes, "10")
	dest := t.TempDir()
	err := extractTarGz(writeArchive(t, "big.tar.gz", gz(t, tarStream(t, 1, 11))), dest, "")
	if !errors.Is(err, archivelimit.ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if files, _ := treeBytes(t, dest); files != 0 {
		t.Fatalf("oversize declared file was created before rejection")
	}
}

// Skipped entries (traversal, unsupported types) still count, so an archive
// of millions of ignored headers cannot spin forever.
func TestExtractTarCountsSkippedEntries(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 4; i++ {
		tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("../escape%d", i), Typeflag: tar.TypeReg, Mode: 0o644})
	}
	tw.Close()
	b, _ := archivelimit.NewWithLimits(archivelimit.Limits{MaxEntries: 3, MaxFileBytes: 1, MaxTotalBytes: 1})
	if err := extractTar(tar.NewReader(&buf), t.TempDir(), "", b); !errors.Is(err, archivelimit.ErrExceeded) {
		t.Fatalf("extractTar = %v, want ErrExceeded", err)
	}
}

func TestExtractZipEnforcesLimits(t *testing.T) {
	makeZipBomb := func(n int, size int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		zeros := make([]byte, size)
		for i := 0; i < n; i++ {
			w, err := zw.Create(fmt.Sprintf("f%d", i))
			if err != nil {
				t.Fatal(err)
			}
			w.Write(zeros)
		}
		zw.Close()
		return buf.Bytes()
	}

	t.Run("entries", func(t *testing.T) {
		t.Setenv(archivelimit.EnvMaxEntries, "3")
		dest := t.TempDir()
		err := extractZip(writeArchive(t, "e.zip", makeZipBomb(4, 1)), dest, "")
		if !errors.Is(err, archivelimit.ErrExceeded) {
			t.Fatalf("extract = %v, want ErrExceeded", err)
		}
		if files, _ := treeBytes(t, dest); files != 0 {
			t.Fatalf("zip entry limit should reject before writing; wrote %d files", files)
		}
	})
	t.Run("total bytes", func(t *testing.T) {
		const limit = 1 << 20
		t.Setenv(archivelimit.EnvMaxTotalBytes, fmt.Sprint(limit))
		dest := t.TempDir()
		err := extractZip(writeArchive(t, "b.zip", makeZipBomb(4, limit/2)), dest, "")
		if !errors.Is(err, archivelimit.ErrExceeded) {
			t.Fatalf("extract = %v, want ErrExceeded", err)
		}
		if _, total := treeBytes(t, dest); total > limit {
			t.Fatalf("wrote %d bytes past a %d-byte budget", total, limit)
		}
	})
	t.Run("understated size", func(t *testing.T) {
		// A header that understates its inflated size must not bypass limits.
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		var raw bytes.Buffer
		fl, err := flate.NewWriter(&raw, flate.BestCompression)
		if err != nil {
			t.Fatal(err)
		}
		fl.Write(bytes.Repeat([]byte{0}, 1<<20))
		fl.Close()
		w, err := zw.CreateRaw(&zip.FileHeader{Name: "liar", Method: zip.Deflate, CompressedSize64: uint64(raw.Len()), UncompressedSize64: 10})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(raw.Bytes())
		zw.Close()
		t.Setenv(archivelimit.EnvMaxFileBytes, "1024")
		dest := t.TempDir()
		if err := extractZip(writeArchive(t, "liar.zip", buf.Bytes()), dest, ""); err == nil {
			t.Fatal("understated zip entry extracted without error")
		}
		if _, total := treeBytes(t, dest); total > 1024 {
			t.Fatalf("wrote %d bytes past a 1024-byte file limit", total)
		}
	})
}

// Rejecting mid-stream must stop xz; previously an early return left xz
// blocked on a full pipe and Wait never returned.
func TestExtractTarXzStopsDecompressorOnLimit(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not installed")
	}
	stream := tarStream(t, 64, 64<<10) // 4 MiB, far more than a pipe buffer
	cmd := exec.Command("xz", "--compress", "--stdout")
	cmd.Stdin = bytes.NewReader(stream)
	compressed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(archivelimit.EnvMaxEntries, "2")
	done := make(chan error, 1)
	go func() { done <- extractTarXz(writeArchive(t, "a.tar.xz", compressed), t.TempDir(), "") }()
	select {
	case err := <-done:
		if !errors.Is(err, archivelimit.ErrExceeded) {
			t.Fatalf("extract = %v, want ErrExceeded", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("extractTarXz hung after rejecting the archive")
	}
}

func TestExtractRejectsMalformedLimitOverride(t *testing.T) {
	t.Setenv(archivelimit.EnvMaxTotalBytes, "plenty")
	err := extractTarGz(writeArchive(t, "a.tar.gz", gz(t, tarStream(t, 1, 1))), t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), archivelimit.EnvMaxTotalBytes) {
		t.Fatalf("extract = %v, want malformed override error", err)
	}
}

// FuzzExtractTar feeds arbitrary bytes to the toolchain tar extractor with a
// small budget. It may fail, but must never panic, write outside destDir, or
// exceed the budget. Seeds run as part of the normal test suite.
func FuzzExtractTar(f *testing.F) {
	f.Add(tarStream(f, 2, 3))
	f.Add(tarStream(f, 40, 1))
	f.Add(tarStream(f, 2, 64<<10))
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, data []byte) {
		const limit = 65536
		b, err := archivelimit.NewWithLimits(archivelimit.Limits{MaxEntries: 32, MaxFileBytes: limit, MaxTotalBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		parent := t.TempDir()
		dest := filepath.Join(parent, "out")
		os.MkdirAll(dest, 0o755)
		_ = extractTar(tar.NewReader(bytes.NewReader(data)), dest, "", b)
		if entries, _ := os.ReadDir(parent); len(entries) != 1 {
			t.Fatalf("extraction wrote outside destination: %v", entries)
		}
		if _, total := treeBytes(t, dest); total > limit {
			t.Fatalf("expanded %d bytes past a %d-byte budget", total, limit)
		}
	})
}
