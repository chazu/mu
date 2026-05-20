//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func detectIsolation() IsolationLevel {
	if sandboxExecAvailable() {
		return IsolationSeatbelt
	}
	return IsolationCopy
}

func sandboxExecAvailable() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// execIsolated runs the command under macOS sandbox-exec with a deny-default SBPL profile.
func (s *Sandbox) execIsolated(ctx context.Context, command []string, env map[string]string, network bool) (int, error, bool) {
	if s.isolation != IsolationSeatbelt {
		return 0, nil, false
	}

	profile := s.generateSBPL(network)

	profileFile, err := os.CreateTemp("", "mu-sbpl-*.sb")
	if err != nil {
		return -1, fmt.Errorf("sandbox: create SBPL profile: %w", err), true
	}
	defer os.Remove(profileFile.Name())

	if _, err := profileFile.WriteString(profile); err != nil {
		profileFile.Close()
		return -1, fmt.Errorf("sandbox: write SBPL profile: %w", err), true
	}
	profileFile.Close()

	// Resolve symlinks — macOS sandbox kernel uses real paths.
	realRoot, _ := filepath.EvalSymlinks(s.rootDir)
	if realRoot == "" {
		realRoot = s.rootDir
	}
	realWork, _ := filepath.EvalSymlinks(s.workDir)
	if realWork == "" {
		realWork = s.workDir
	}

	binDir := filepath.Join(realRoot, "bin")
	tmpDir := filepath.Join(realRoot, "tmp")
	outDir := filepath.Join(realRoot, "out")

	args := []string{
		"-f", profileFile.Name(),
		"-D", "TOOLCHAIN_DIR=" + binDir,
		"-D", "SOURCE_DIR=" + realWork,
		"-D", "OUTPUT_DIR=" + outDir,
		"-D", "TMPDIR=" + tmpDir,
		"-D", "ROOT_DIR=" + realRoot,
	}
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "sandbox-exec", args...)
	cmd.Dir = s.workDir
	cmd.Env = s.buildEnv(env)
	cmd.Stdout = s.stdoutWriter()
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return exitCode, err, true
	}
	return exitCode, nil, true
}

func (s *Sandbox) generateSBPL(network bool) string {
	profile := `(version 1)
(deny default)
(debug deny)

;; Process lifecycle
(allow process-fork)
(allow process-exec
  (subpath (param "TOOLCHAIN_DIR"))
  (subpath (param "ROOT_DIR"))
  (subpath "/bin")
  (subpath "/usr/bin")
  (subpath "/usr/lib"))

;; Read-only access (file-read* covers data, metadata, xattr, search)
(allow file-read*
  (literal "/")
  (subpath (param "TOOLCHAIN_DIR"))
  (subpath (param "SOURCE_DIR"))
  (subpath (param "ROOT_DIR"))
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/usr")
  (subpath "/opt")
  (subpath "/private")
  (subpath "/System")
  (subpath "/Library")
  (subpath "/Applications/Xcode.app/Contents/Developer")
  (subpath "/dev")
  (subpath "/var")
  (subpath "/tmp")
  (subpath "/etc"))

;; Writable: output + tmp + work dir
(allow file-write*
  (subpath (param "OUTPUT_DIR"))
  (subpath (param "TMPDIR"))
  (subpath (param "SOURCE_DIR"))
  (literal "/dev/null"))

;; Minimal IPC (required by dyld and system libs)
(allow sysctl-read)
(allow ipc-posix-shm)
(allow ipc-posix-sem)
(allow mach-lookup
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.notification_center"))
(allow signal (target self))
`
	if network {
		profile += "\n;; Network allowed\n(allow network-outbound)\n(allow network-bind)\n"
	} else {
		profile += "\n;; Network denied\n(deny network-outbound)\n(deny network-inbound)\n(deny network-bind)\n"
	}
	return profile
}

// IsSandboxInit always returns false on macOS (no re-exec pattern needed).
func IsSandboxInit() bool { return false }

// RunInit is a no-op on macOS.
func RunInit() {}
