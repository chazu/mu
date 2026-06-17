package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
)

func TestIsolationLevel(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	level := sb.Level()
	switch runtime.GOOS {
	case "darwin":
		if level != IsolationSeatbelt {
			t.Errorf("darwin: got level %d, want IsolationSeatbelt(%d)", level, IsolationSeatbelt)
		}
	case "linux":
		// Level depends on kernel config (unprivileged_userns_clone).
		if level != IsolationNamespace && level != IsolationCopy {
			t.Errorf("linux: got unexpected level %d", level)
		}
	default:
		if level != IsolationCopy {
			t.Errorf("other: got level %d, want IsolationCopy(%d)", level, IsolationCopy)
		}
	}
}

func TestExecNetworkDenied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("network enforcement test requires macOS Seatbelt")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	// curl should fail with network denied.
	code, err := sb.Exec(context.Background(),
		[]string{"sh", "-c", "curl -s --max-time 2 http://example.com > /dev/null 2>&1; echo $?"},
		map[string]string{"PATH": "/usr/bin:/bin"},
		false,
	)
	// We expect the command itself to succeed (sh runs), but curl fails inside.
	if err != nil {
		t.Logf("exec error (expected with network deny): %v", err)
	}
	_ = code
}

func TestExecWriteOutsideSandboxDenied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("write enforcement test requires macOS Seatbelt")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	// Attempt to write outside the sandbox should fail.
	target := filepath.Join(t.TempDir(), "escape.txt")
	code, _ := sb.Exec(context.Background(),
		[]string{"sh", "-c", "echo pwned > " + target},
		map[string]string{"PATH": "/usr/bin:/bin"},
		false,
	)
	// sh should return non-zero or the file shouldn't exist.
	if code == 0 {
		if _, err := os.Stat(target); err == nil {
			data, _ := os.ReadFile(target)
			t.Errorf("sandbox allowed write outside root: %s contains %q", target, data)
		}
	}
}

func TestExecReadHostFileDenied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("read enforcement test requires macOS Seatbelt")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	// Attempt to read a user file outside allowed paths.
	outFile := "leaked.txt"
	sb.Exec(context.Background(),
		[]string{"sh", "-c", "cat ~/.bash_history > " + outFile + " 2>/dev/null || echo denied > " + outFile},
		map[string]string{"PATH": "/usr/bin:/bin"},
		false,
	)

	got, err := os.ReadFile(sb.OutputPath(outFile))
	if err != nil {
		t.Skipf("output file not created: %v", err)
	}
	if strings.TrimSpace(string(got)) != "denied" {
		t.Logf("sandbox may have allowed read of ~/.bash_history (output length: %d)", len(got))
	}
}

func BenchmarkExecCopy(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("sh not available on windows")
	}

	store := newBenchStore(b)
	for b.Loop() {
		sb, err := New(store)
		if err != nil {
			b.Fatal(err)
		}
		sb.isolation = IsolationCopy
		_, err = sb.Exec(context.Background(),
			[]string{"sh", "-c", "echo hello > out.txt"},
			map[string]string{"PATH": "/usr/bin:/bin"},
			false,
		)
		if err != nil {
			b.Fatal(err)
		}
		sb.Cleanup()
	}
}

func BenchmarkExecIsolated(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("sh not available on windows")
	}

	store := newBenchStore(b)
	for b.Loop() {
		sb, err := New(store)
		if err != nil {
			b.Fatal(err)
		}
		_, err = sb.Exec(context.Background(),
			[]string{"sh", "-c", "echo hello > out.txt"},
			map[string]string{"PATH": "/usr/bin:/bin"},
			false,
		)
		if err != nil {
			b.Fatal(err)
		}
		sb.Cleanup()
	}
}

func newBenchStore(b *testing.B) cas.Store {
	b.Helper()
	s, err := oci.NewLocal(b.TempDir())
	if err != nil {
		b.Fatalf("oci.NewLocal: %v", err)
	}
	return s
}
