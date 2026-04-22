// Package sandbox provides hermetic execution environments for build actions.
//
// A Sandbox creates a temporary directory, unpacks a toolchain OCI image into
// it, copies source files in, and executes a command with a restricted
// environment. After execution, declared outputs are extracted.
//
// Current isolation: copy sandbox (restricted PATH/env, temp rootfs).
//
// Planned isolation improvements:
//   - Linux: user namespaces + pivot_root + overlayfs (no root required)
//   - macOS: sandbox-exec profiles for filesystem restriction
//   - Network: network namespaces on Linux, sandbox-exec on macOS
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/cas"
)

// Sandbox manages a temporary rootfs for executing a build action.
type Sandbox struct {
	rootDir string    // the temp directory serving as our rootfs
	workDir string    // working directory inside rootfs for the action
	store   cas.Store // CAS for unpacking toolchain blobs
}

// New creates a Sandbox with a fresh temporary directory.
// The caller must call Cleanup when done.
func New(store cas.Store) (*Sandbox, error) {
	root, err := os.MkdirTemp("", "mu-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create temp dir: %w", err)
	}

	// Create standard subdirectories.
	for _, d := range []string{"bin", "work", "out", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			os.RemoveAll(root)
			return nil, fmt.Errorf("sandbox: create subdir %s: %w", d, err)
		}
	}

	return &Sandbox{
		rootDir: root,
		workDir: filepath.Join(root, "work"),
		store:   store,
	}, nil
}

// RootDir returns the sandbox's root directory path.
func (s *Sandbox) RootDir() string {
	return s.rootDir
}

// WorkDir returns the sandbox's working directory (where sources are placed).
func (s *Sandbox) WorkDir() string {
	return s.workDir
}

// UnpackToolchain extracts all artifacts from a toolchain manifest into the
// sandbox's rootfs. Each artifact is fetched from CAS by digest and written
// to its relative path under the root directory.
func (s *Sandbox) UnpackToolchain(ctx context.Context, artifacts map[string]cas.Digest) error {
	for relPath, dgst := range artifacts {
		if err := s.unpackBlob(ctx, relPath, dgst); err != nil {
			return fmt.Errorf("sandbox: unpack %s: %w", relPath, err)
		}
	}
	return nil
}

// unpackBlob fetches a blob from CAS and writes it to relPath under rootDir.
func (s *Sandbox) unpackBlob(ctx context.Context, relPath string, dgst cas.Digest) error {
	// Guard against path traversal.
	dest := filepath.Join(s.rootDir, relPath)
	if !strings.HasPrefix(dest, s.rootDir+string(filepath.Separator)) {
		return fmt.Errorf("path traversal: %s", relPath)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	rc, err := s.store.Get(ctx, dgst)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", dgst, err)
	}
	defer rc.Close()

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// CopySource copies a source file into the sandbox's work directory,
// preserving relative path structure.
func (s *Sandbox) CopySource(src, relPath string) error {
	dest := filepath.Join(s.workDir, relPath)
	if !strings.HasPrefix(dest, s.workDir+string(filepath.Separator)) && dest != s.workDir {
		return fmt.Errorf("sandbox: source path traversal: %s", relPath)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	// Try hardlink first (fast, no copy), fall back to copy.
	if err := os.Link(src, dest); err == nil {
		return nil
	}

	return copyFile(src, dest)
}

// CopySources copies multiple source files into the work directory.
// srcRoot is the project root; relPaths are relative to it.
func (s *Sandbox) CopySources(srcRoot string, relPaths []string) error {
	for _, rel := range relPaths {
		src := filepath.Join(srcRoot, rel)
		if err := s.CopySource(src, rel); err != nil {
			return fmt.Errorf("sandbox: copy source %s: %w", rel, err)
		}
	}
	return nil
}

// Exec runs a command inside the sandbox with a hermetic environment.
// The command runs in the work directory. PATH includes the sandbox's bin
// directory (where toolchain binaries are unpacked) plus any additional
// paths from the env map.
func (s *Sandbox) Exec(ctx context.Context, command []string, env map[string]string, network bool) (int, error) {
	if len(command) == 0 {
		return -1, fmt.Errorf("sandbox: empty command")
	}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = s.workDir
	cmd.Env = s.buildEnv(env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return exitCode, err
	}
	return exitCode, nil
}

// buildEnv constructs the environment for the sandboxed process.
// It starts with the explicit env vars, then sets sandbox-specific paths.
func (s *Sandbox) buildEnv(env map[string]string) []string {
	merged := make(map[string]string, len(env)+3)

	// Copy explicit env vars.
	for k, v := range env {
		merged[k] = v
	}

	// Ensure PATH includes the sandbox bin directory.
	binDir := filepath.Join(s.rootDir, "bin")
	if existing, ok := merged["PATH"]; ok {
		merged["PATH"] = binDir + string(os.PathListSeparator) + existing
	} else {
		merged["PATH"] = binDir
	}

	// Set TMPDIR to sandbox's tmp directory for hermetic temp file creation.
	if _, ok := merged["TMPDIR"]; !ok {
		merged["TMPDIR"] = filepath.Join(s.rootDir, "tmp")
	}

	result := make([]string, 0, len(merged))
	for k, v := range merged {
		result = append(result, k+"="+v)
	}
	return result
}

// OutputPath returns the absolute path for a declared output file, relative
// to the sandbox's work directory.
func (s *Sandbox) OutputPath(relPath string) string {
	return filepath.Join(s.workDir, relPath)
}

// Cleanup removes the sandbox's temporary directory and all its contents.
func (s *Sandbox) Cleanup() error {
	return os.RemoveAll(s.rootDir)
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
