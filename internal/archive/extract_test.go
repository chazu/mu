package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name, body, link string
	typ              byte
}

func reg(name, body string) entry   { return entry{name: name, body: body, typ: tar.TypeReg} }
func dir(name string) entry         { return entry{name: name, typ: tar.TypeDir} }
func sym(name, target string) entry { return entry{name: name, link: target, typ: tar.TypeSymlink} }

func tarOf(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o755, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ == tar.TypeXGlobalHeader {
			hdr = &tar.Header{Typeflag: e.typ, PAXRecords: map[string]string{"comment": e.name}}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tarStream builds a tar of n entries, each with size bytes of zeros (zeros
// compress to almost nothing, like a decompression bomb).
func tarStream(t testing.TB, n int, size int64) []byte {
	t.Helper()
	entries := make([]entry, n)
	zeros := string(make([]byte, size))
	for i := range entries {
		entries[i] = reg(fmt.Sprintf("f%06d", i), zeros)
	}
	return tarOf(t, entries...)
}

func gz(t testing.TB, data []byte) []byte {
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

func writeArchive(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
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

// sandbox returns a parent directory containing only dest ("out"), so tests
// can prove nothing was written beside it.
func sandbox(t *testing.T) (parent, dest string) {
	t.Helper()
	parent = t.TempDir()
	dest = filepath.Join(parent, "out")
	return parent, dest
}

func assertOnlyDest(t *testing.T, parent string) {
	t.Helper()
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.Name() != "out" {
			t.Fatalf("extraction wrote outside the destination: %s", e.Name())
		}
	}
}

var (
	toolchainPolicy = Policy{Limits: Toolchain, AllowSymlinks: true}
	pluginPolicy    = Policy{Limits: Plugin}
)

func extractTarBytes(t *testing.T, data []byte, dest string, p Policy) error {
	t.Helper()
	return ExtractTar(context.Background(), bytes.NewReader(data), dest, p)
}

func TestDetectFormat(t *testing.T) {
	for name, want := range map[string]Format{"a.tar.gz": TarGz, "A.TGZ": TarGz, "a.tar.xz": TarXz, "a.txz": TarXz, "a.zip": Zip} {
		if got, ok := DetectFormat(name); !ok || got != want {
			t.Errorf("DetectFormat(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := DetectFormat("jq-macos-arm64"); ok {
		t.Error("raw binary detected as archive")
	}
}

// A symlink pointing out of the tree followed by an entry beneath it is the
// classic zip-slip: it must fail without writing outside the destination.
func TestSymlinkEscapeThenWriteThroughIsRejected(t *testing.T) {
	for _, p := range []Policy{toolchainPolicy, pluginPolicy} {
		parent, dest := sandbox(t)
		data := tarOf(t, sym("link", "../outside"), reg("link/pwned", "gotcha"))
		if err := extractTarBytes(t, data, dest, p); err == nil {
			t.Fatalf("AllowSymlinks=%v: escaping symlink accepted", p.AllowSymlinks)
		}
		assertOnlyDest(t, parent)
	}
}

func TestAbsoluteSymlinkIsRejected(t *testing.T) {
	parent, dest := sandbox(t)
	err := extractTarBytes(t, tarOf(t, sym("bin/leak", "/etc/passwd")), dest, toolchainPolicy)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("absolute symlink = %v, want rejection", err)
	}
	assertOnlyDest(t, parent)
}

func TestInTreeRelativeSymlink(t *testing.T) {
	data := tarOf(t, reg("lib/real", "x"), sym("bin/tool", "../lib/real"), sym("libdir", "lib"), reg("libdir/via", "y"))
	_, dest := sandbox(t)
	if err := extractTarBytes(t, data, dest, toolchainPolicy); err != nil {
		t.Fatalf("in-tree symlink rejected for toolchains: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(dest, "bin", "tool")); err != nil || got != "../lib/real" {
		t.Fatalf("Readlink = %q, %v", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "lib", "via")); err != nil || string(b) != "y" {
		t.Fatalf("write through in-tree dir symlink = %q, %v", b, err)
	}

	_, dest = sandbox(t)
	if err := extractTarBytes(t, data, dest, pluginPolicy); err == nil {
		t.Fatal("symlink accepted with AllowSymlinks=false")
	}
}

// Lexically in-tree targets can still resolve outside through another link;
// the os.Root backstop must refuse the write.
func TestChainedSymlinkEscapeIsContainedByRoot(t *testing.T) {
	parent, dest := sandbox(t)
	data := tarOf(t, dir("d/"), sym("d/up", ".."), sym("d/up2", "up/.."), reg("d/up2/pwned", "x"))
	if err := extractTarBytes(t, data, dest, toolchainPolicy); err == nil {
		t.Fatal("write through chained escaping symlink succeeded")
	}
	assertOnlyDest(t, parent)

	// Even without a write through it, an escaping chain must not survive.
	_, dest = sandbox(t)
	data = tarOf(t, dir("d/"), sym("d/up", ".."), sym("d/up2", "up/.."))
	if err := extractTarBytes(t, data, dest, toolchainPolicy); err == nil {
		t.Fatal("chained symlink resolving outside the tree accepted")
	}
	// A dangling in-tree link is harmless and allowed.
	_, dest = sandbox(t)
	if err := extractTarBytes(t, tarOf(t, sym("bin/tool", "../missing/tool")), dest, toolchainPolicy); err != nil {
		t.Fatalf("dangling in-tree symlink rejected: %v", err)
	}
}

func TestRejectsHardLinksEscapesAndDuplicates(t *testing.T) {
	cases := map[string][]entry{
		"hard link":       {reg("a", "x"), {name: "b", link: "a", typ: tar.TypeLink}},
		"parent escape":   {reg("../escape", "x")},
		"nested escape":   {reg("a/../../escape", "x")},
		"absolute":        {reg("/tmp/abs", "x")},
		"duplicate file":  {reg("a", "x"), reg("a", "y")},
		"file over link":  {reg("real", "x"), sym("a", "real"), reg("a", "y")},
		"device":          {{name: "dev", typ: tar.TypeChar}},
		"root as file":    {reg(".", "x")},
		"symlink escapes": {sym("a/b", "../../x")},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			parent, dest := sandbox(t)
			if err := extractTarBytes(t, tarOf(t, entries...), dest, toolchainPolicy); err == nil {
				t.Fatal("accepted")
			}
			assertOnlyDest(t, parent)
			if b, err := os.ReadFile(filepath.Join(dest, "real")); err == nil && string(b) != "x" {
				t.Fatalf("existing file overwritten: %q", b)
			}
		})
	}
}

func TestStripPrefixAndRootEntries(t *testing.T) {
	data := tarOf(t, dir("./"), dir("go/"), reg("go/bin/go", "g"), reg("other", "o"), entry{name: "pax", typ: tar.TypeXGlobalHeader})
	_, dest := sandbox(t)
	p := toolchainPolicy
	p.StripPrefix = "go"
	if err := extractTarBytes(t, data, dest, p); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"bin/go": "g", "other": "o"} {
		if b, err := os.ReadFile(filepath.Join(dest, rel)); err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", rel, b, err)
		}
	}
}

func TestZipSymlinks(t *testing.T) {
	build := func(target string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("lib/real")
		w.Write([]byte("x"))
		hdr := &zip.FileHeader{Name: "bin/tool"}
		hdr.SetMode(fs.ModeSymlink | 0o777)
		w, _ = zw.CreateHeader(hdr)
		w.Write([]byte(target))
		zw.Close()
		return buf.Bytes()
	}
	_, dest := sandbox(t)
	if err := ExtractFile(context.Background(), writeArchive(t, "a.zip", build("../lib/real")), dest, Zip, toolchainPolicy); err != nil {
		t.Fatalf("in-tree zip symlink: %v", err)
	}
	if got, _ := os.Readlink(filepath.Join(dest, "bin", "tool")); got != "../lib/real" {
		t.Fatalf("Readlink = %q", got)
	}
	parent, dest := sandbox(t)
	if err := ExtractFile(context.Background(), writeArchive(t, "b.zip", build("../../outside")), dest, Zip, toolchainPolicy); err == nil {
		t.Fatal("escaping zip symlink accepted")
	}
	assertOnlyDest(t, parent)
	_, dest = sandbox(t)
	if err := ExtractFile(context.Background(), writeArchive(t, "c.zip", build("../lib/real")), dest, Zip, pluginPolicy); err == nil {
		t.Fatal("zip symlink accepted with AllowSymlinks=false")
	}
}

func TestExtractTarGzEnforcesEntryLimit(t *testing.T) {
	p := Policy{Limits: Limits{MaxEntries: 5, MaxFileBytes: 10, MaxTotalBytes: 100}}
	err := ExtractFile(context.Background(), writeArchive(t, "a.tar.gz", gz(t, tarStream(t, 6, 1))), t.TempDir(), TarGz, p)
	if !errors.Is(err, ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if err := ExtractFile(context.Background(), writeArchive(t, "b.tar.gz", gz(t, tarStream(t, 5, 1))), t.TempDir(), TarGz, p); err != nil {
		t.Fatalf("archive at the entry limit rejected: %v", err)
	}
}

func TestExtractTarGzStopsDecompressionBomb(t *testing.T) {
	const limit = 1 << 20
	t.Setenv(EnvMaxBytes, fmt.Sprint(limit))
	bomb := gz(t, tarStream(t, 8, limit/2)) // 4 MiB expanded from a few KiB
	if len(bomb) > 64<<10 {
		t.Fatalf("fixture not bomb-like: %d compressed bytes", len(bomb))
	}
	dest := t.TempDir()
	err := ExtractFile(context.Background(), writeArchive(t, "bomb.tar.gz", bomb), dest, TarGz, toolchainPolicy)
	if !errors.Is(err, ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if _, total := treeBytes(t, dest); total > limit {
		t.Fatalf("wrote %d bytes past a %d-byte budget", total, limit)
	}
}

func TestExtractTarRejectsOversizeFileByHeader(t *testing.T) {
	t.Setenv(EnvMaxBytes, "10")
	dest := t.TempDir()
	err := extractTarBytes(t, tarStream(t, 1, 11), dest, toolchainPolicy)
	if !errors.Is(err, ErrExceeded) {
		t.Fatalf("extract = %v, want ErrExceeded", err)
	}
	if files, _ := treeBytes(t, dest); files != 0 {
		t.Fatalf("oversize declared file was created before rejection")
	}
}

// Skipped entries still count, so an archive of millions of ignored headers
// cannot spin forever.
func TestExtractTarCountsSkippedEntries(t *testing.T) {
	data := tarOf(t, dir("./"), dir("./"), dir("./"), dir("./"))
	p := Policy{Limits: Limits{MaxEntries: 3, MaxFileBytes: 1, MaxTotalBytes: 1}}
	if err := extractTarBytes(t, data, t.TempDir(), p); !errors.Is(err, ErrExceeded) {
		t.Fatalf("ExtractTar = %v, want ErrExceeded", err)
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
		p := Policy{Limits: Limits{MaxEntries: 3, MaxFileBytes: 10, MaxTotalBytes: 100}}
		dest := t.TempDir()
		err := ExtractFile(context.Background(), writeArchive(t, "e.zip", makeZipBomb(4, 1)), dest, Zip, p)
		if !errors.Is(err, ErrExceeded) {
			t.Fatalf("extract = %v, want ErrExceeded", err)
		}
		if files, _ := treeBytes(t, dest); files != 0 {
			t.Fatalf("zip entry limit should reject before writing; wrote %d files", files)
		}
	})
	t.Run("total bytes", func(t *testing.T) {
		const limit = 1 << 20
		t.Setenv(EnvMaxBytes, fmt.Sprint(limit))
		dest := t.TempDir()
		err := ExtractFile(context.Background(), writeArchive(t, "b.zip", makeZipBomb(4, limit/2)), dest, Zip, toolchainPolicy)
		if !errors.Is(err, ErrExceeded) {
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
		t.Setenv(EnvMaxBytes, "1024")
		dest := t.TempDir()
		if err := ExtractFile(context.Background(), writeArchive(t, "liar.zip", buf.Bytes()), dest, Zip, toolchainPolicy); err == nil {
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
	p := Policy{Limits: Limits{MaxEntries: 2, MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 30}}
	done := make(chan error, 1)
	go func() {
		done <- ExtractFile(context.Background(), writeArchive(t, "a.tar.xz", compressed), t.TempDir(), TarXz, p)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrExceeded) {
			t.Fatalf("extract = %v, want ErrExceeded", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("xz extraction hung after rejecting the archive")
	}
}

func TestExtractRejectsMalformedLimitOverride(t *testing.T) {
	t.Setenv(EnvMaxBytes, "plenty")
	err := extractTarBytes(t, tarStream(t, 1, 1), t.TempDir(), toolchainPolicy)
	if err == nil || !strings.Contains(err.Error(), EnvMaxBytes) {
		t.Fatalf("extract = %v, want malformed override error", err)
	}
}

// FuzzExtractTar feeds arbitrary bytes to the tar extractor with a small
// budget and symlinks allowed. It may fail, but must never panic, write
// outside destDir, or exceed the budget. Seeds run in the normal test suite.
func FuzzExtractTar(f *testing.F) {
	f.Add(tarStream(f, 2, 3))
	f.Add(tarStream(f, 40, 1))
	f.Add(tarStream(f, 2, 64<<10))
	f.Add(tarOf(f, sym("link", "../outside"), reg("link/pwned", "x")))
	f.Add(tarOf(f, dir("d/"), sym("d/up", ".."), sym("d/up2", "up/.."), reg("d/up2/pwned", "x")))
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, data []byte) {
		const limit = 65536
		parent, dest := sandbox(t)
		p := Policy{Limits: Limits{MaxEntries: 32, MaxFileBytes: limit, MaxTotalBytes: limit}, AllowSymlinks: true}
		_ = ExtractTar(context.Background(), bytes.NewReader(data), dest, p)
		assertOnlyDest(t, parent)
		if _, total := treeBytes(t, dest); total > limit {
			t.Fatalf("expanded %d bytes past a %d-byte budget", total, limit)
		}
	})
}

func TestStripPrefix(t *testing.T) {
	tests := []struct{ path, prefix, want string }{
		{"mygo-1.0/bin/go", "mygo-1.0", "bin/go"},
		{"mygo-1.0", "mygo-1.0", ""},
		{"mygo-1.0", "mygo-1.0/", ""},
		{"mygo-1.0x/bin", "mygo-1.0", "mygo-1.0x/bin"},
		{"other/file", "mygo-1.0", "other/file"},
		{"bin/go", "", "bin/go"},
	}
	for _, tt := range tests {
		if got := stripPrefix(tt.path, tt.prefix); got != tt.want {
			t.Errorf("stripPrefix(%q, %q) = %q, want %q", tt.path, tt.prefix, got, tt.want)
		}
	}
}
