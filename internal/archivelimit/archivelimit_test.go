package archivelimit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func budget(t *testing.T, l Limits) *Budget {
	t.Helper()
	b, err := NewWithLimits(l)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEntryLimitIsInclusive(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 2, MaxFileBytes: 10, MaxTotalBytes: 10})
	for i := 0; i < 2; i++ {
		if err := b.Entry("ok"); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	if err := b.Entry("third"); !errors.Is(err, ErrExceeded) || !strings.Contains(err.Error(), EnvMaxEntries) {
		t.Fatalf("third entry = %v, want ErrExceeded naming %s", err, EnvMaxEntries)
	}
}

func TestCopyAtExactBoundariesSucceeds(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 6})
	var out bytes.Buffer
	if n, err := b.Copy(&out, strings.NewReader("abcd"), "a"); err != nil || n != 4 {
		t.Fatalf("file at file limit: n=%d err=%v", n, err)
	}
	if n, err := b.Copy(&out, strings.NewReader("ef"), "b"); err != nil || n != 2 {
		t.Fatalf("file reaching total limit: n=%d err=%v", n, err)
	}
	if n, err := b.Copy(&out, strings.NewReader(""), "empty"); err != nil || n != 0 {
		t.Fatalf("empty file at exhausted total: n=%d err=%v", n, err)
	}
}

func TestCopyOneByteOverFileLimitFails(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 100})
	var out bytes.Buffer
	_, err := b.Copy(&out, strings.NewReader("abcde"), "big")
	if !errors.Is(err, ErrExceeded) || !strings.Contains(err.Error(), EnvMaxFileBytes) {
		t.Fatalf("Copy = %v, want file-limit ErrExceeded", err)
	}
	if out.Len() > 4 {
		t.Fatalf("wrote %d bytes past a 4-byte limit", out.Len())
	}
}

func TestCopyOneByteOverTotalFails(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 10, MaxFileBytes: 100, MaxTotalBytes: 5})
	var out bytes.Buffer
	if _, err := b.Copy(&out, strings.NewReader("abc"), "a"); err != nil {
		t.Fatal(err)
	}
	_, err := b.Copy(&out, strings.NewReader("def"), "b")
	if !errors.Is(err, ErrExceeded) || !strings.Contains(err.Error(), EnvMaxTotalBytes) {
		t.Fatalf("Copy = %v, want total-limit ErrExceeded", err)
	}
	if out.Len() > 5 {
		t.Fatalf("wrote %d bytes past a 5-byte total", out.Len())
	}
}

// An endless (highly compressible) stream stops at the limit instead of
// filling the disk.
func TestCopyStopsUnboundedStream(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 1, MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 20})
	n, err := b.Copy(io.Discard, zeroReader{}, "bomb")
	if !errors.Is(err, ErrExceeded) || n != 1<<20 {
		t.Fatalf("Copy(endless) n=%d err=%v", n, err)
	}
}

func TestDeclaredRejectsBeforeWriting(t *testing.T) {
	b := budget(t, Limits{MaxEntries: 10, MaxFileBytes: 10, MaxTotalBytes: 15})
	if err := b.Declared("ok", 10); err != nil {
		t.Fatal(err)
	}
	if err := b.Declared("unknown", -1); err != nil {
		t.Fatal(err)
	}
	if err := b.Declared("too big", 11); !errors.Is(err, ErrExceeded) {
		t.Fatalf("Declared(11) = %v", err)
	}
	if _, err := b.Copy(io.Discard, strings.NewReader("0123456789"), "ten"); err != nil {
		t.Fatal(err)
	}
	if err := b.Declared("past total", 6); !errors.Is(err, ErrExceeded) {
		t.Fatalf("Declared past total = %v", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	env := map[string]string{EnvMaxEntries: "7", EnvMaxFileBytes: "8", EnvMaxTotalBytes: "9"}
	l, err := withEnv(Plugin, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if l != (Limits{MaxEntries: 7, MaxFileBytes: 8, MaxTotalBytes: 9}) {
		t.Fatalf("limits = %+v", l)
	}
	for _, bad := range []string{"0", "-1", "lots", "1e9"} {
		_, err := withEnv(Plugin, func(k string) (string, bool) { return bad, k == EnvMaxTotalBytes })
		if err == nil {
			t.Errorf("override %q accepted", bad)
		}
	}
	l, err = withEnv(Toolchain, func(string) (string, bool) { return "", false })
	if err != nil || l != Toolchain {
		t.Fatalf("no env: %+v %v", l, err)
	}
}

func TestNewEnvironment(t *testing.T) {
	t.Setenv(EnvMaxEntries, "3")
	b, err := New(Toolchain)
	if err != nil || b.Limits().MaxEntries != 3 || b.Limits().MaxTotalBytes != Toolchain.MaxTotalBytes {
		t.Fatalf("New = %+v, %v", b, err)
	}
	t.Setenv(EnvMaxEntries, "nope")
	if _, err := New(Toolchain); err == nil {
		t.Fatal("malformed env accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
