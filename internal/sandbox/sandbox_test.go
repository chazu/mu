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

func newTestStore(t *testing.T) cas.Store {
	t.Helper()
	s, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("oci.NewLocal: %v", err)
	}
	return s
}

func TestNewAndCleanup(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Verify directory structure.
	for _, sub := range []string{"bin", "work", "out", "tmp"} {
		path := filepath.Join(sb.RootDir(), sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing subdir %s: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}

	root := sb.RootDir()
	if err := sb.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("rootDir still exists after Cleanup")
	}
}

func TestCopySource(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	// Create a source file to copy in.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	content := "package main\n"
	if err := os.WriteFile(srcFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sb.CopySource(srcFile, "main.go"); err != nil {
		t.Fatalf("CopySource: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(sb.WorkDir(), "main.go"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestCopySourceNested(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	srcDir := t.TempDir()
	nested := filepath.Join(srcDir, "cmd", "server")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(nested, "main.go")
	if err := os.WriteFile(srcFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sb.CopySource(srcFile, "cmd/server/main.go"); err != nil {
		t.Fatalf("CopySource: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sb.WorkDir(), "cmd", "server", "main.go")); err != nil {
		t.Errorf("nested file missing: %v", err)
	}
}

func TestCopySourcePathTraversal(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	err = sb.CopySource("/etc/passwd", "../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestCopySources(t *testing.T) {
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	srcRoot := t.TempDir()
	for _, f := range []string{"go.mod", "main.go"} {
		if err := os.WriteFile(filepath.Join(srcRoot, f), []byte("content-"+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := sb.CopySources(srcRoot, []string{"go.mod", "main.go"}); err != nil {
		t.Fatalf("CopySources: %v", err)
	}

	for _, f := range []string{"go.mod", "main.go"} {
		got, err := os.ReadFile(filepath.Join(sb.WorkDir(), f))
		if err != nil {
			t.Errorf("missing %s: %v", f, err)
			continue
		}
		if string(got) != "content-"+f {
			t.Errorf("%s content = %q", f, got)
		}
	}
}

func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()
	// This unit exercises host-shell execution; native enforcement has its own
	// static-helper acceptance test and is never inferred from this copy level.
	sb.isolation = IsolationCopy

	outFile := "hello.txt"
	code, err := sb.Exec(context.Background(),
		[]string{"sh", "-c", "echo hello > " + outFile},
		map[string]string{"PATH": "/usr/bin:/bin"},
		false,
	)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(sb.OutputPath(outFile))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(got)) != "hello" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(string(got)), "hello")
	}
}

func TestExecPathIncludesBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()
	// This unit exercises host-shell execution; native enforcement has its own
	// static-helper acceptance test and is never inferred from this copy level.
	sb.isolation = IsolationCopy

	// Write a fake tool to the bin directory.
	toolPath := filepath.Join(sb.RootDir(), "bin", "mytool")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\necho mytool-output\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	outFile := "result.txt"
	code, err := sb.Exec(context.Background(),
		[]string{"sh", "-c", "mytool > " + outFile},
		map[string]string{"PATH": "/usr/bin:/bin"},
		false,
	)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(sb.OutputPath(outFile))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.TrimSpace(string(got)) != "mytool-output" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(string(got)), "mytool-output")
	}
}

func TestUnpackToolchain(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Store a fake binary in CAS.
	content := "#!/bin/sh\necho fake-tool\n"
	dgst, err := store.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()

	artifacts := map[string]cas.Digest{
		"bin/go": dgst,
	}

	if err := sb.UnpackToolchain(ctx, artifacts, nil, nil); err != nil {
		t.Fatalf("UnpackToolchain: %v", err)
	}

	// Verify the file was written with executable permissions.
	path := filepath.Join(sb.RootDir(), "bin", "go")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat unpacked file: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("unpacked file is not executable: mode %v", info.Mode())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unpacked file: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// Recorded modes and in-tree symlinks are recreated; a link leaving the
// tree is refused.
func TestUnpackToolchainModesAndSymlinks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	tool, err := store.Put(ctx, strings.NewReader("#!/bin/sh\necho x\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Put(ctx, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	sb, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Cleanup()
	err = sb.UnpackToolchain(ctx,
		map[string]cas.Digest{"libexec/x": tool, "share/x.txt": data},
		map[string]uint32{"libexec/x": 0o755, "share/x.txt": 0o644},
		map[string]string{"bin/x": "../libexec/x", "lib": "share"})
	if err != nil {
		t.Fatalf("UnpackToolchain: %v", err)
	}
	if info, err := os.Stat(filepath.Join(sb.RootDir(), "share", "x.txt")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("share/x.txt mode = %v, %v; want 0644", info.Mode(), err)
	}
	if target, err := os.Readlink(filepath.Join(sb.RootDir(), "bin", "x")); err != nil || target != "../libexec/x" {
		t.Fatalf("bin/x -> %q, %v", target, err)
	}
	if got, err := os.ReadFile(filepath.Join(sb.RootDir(), "lib", "x.txt")); err != nil || string(got) != "data" {
		t.Fatalf("lib/x.txt through directory link = %q, %v", got, err)
	}
	for _, target := range []string{"../../etc/passwd", "/etc/passwd"} {
		if err := sb.UnpackToolchain(ctx, nil, nil, map[string]string{"bin/escape": target}); err == nil {
			t.Fatalf("link to %q accepted", target)
		}
	}
}

func TestExecHermeticEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh not available on windows")
	}

	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sb.Cleanup()
	sb.isolation = IsolationCopy

	// Run env and check that only sandbox-controlled vars are present.
	outFile := "env.txt"
	code, err := sb.Exec(context.Background(),
		[]string{"sh", "-c", "env > " + outFile},
		map[string]string{"FOO": "bar", "PATH": "/usr/bin:/bin"},
		false,
	)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d", code)
	}

	got, err := os.ReadFile(sb.OutputPath(outFile))
	if err != nil {
		t.Fatalf("read env output: %v", err)
	}

	lines := strings.Split(string(got), "\n")
	found := make(map[string]bool)
	values := make(map[string]string)
	for _, line := range lines {
		if k, v, ok := strings.Cut(line, "="); ok {
			found[k] = true
			values[k] = v
		}
	}

	// FOO should be set.
	if !found["FOO"] {
		t.Error("FOO not in environment")
	}
	// PATH should include sandbox bin dir.
	if !found["PATH"] {
		t.Error("PATH not in environment")
	}
	// TMPDIR should be set to sandbox tmp.
	if !found["TMPDIR"] {
		t.Error("TMPDIR not in environment")
	}
	// HOME is the sandbox's own home/, never the host's.
	if want := sb.VisiblePath("home"); values["HOME"] != want {
		t.Errorf("HOME = %q, want sandbox home %q", values["HOME"], want)
	}
}
