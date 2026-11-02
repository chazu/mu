//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		"-D", "HOME_DIR=" + filepath.Join(realRoot, "home"),
		"-D", "ROOT_DIR=" + realRoot,
	}
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "sandbox-exec", args...)
	cmd.Dir = s.cwd()
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

;; Path lookup may inspect metadata, but data reads stay within declared
;; files and platform/toolchain runtime trees. Never grant a whole temp tree.
(allow file-read-metadata)
;; Read-only access (file-read* covers data, metadata, xattr, search)
(allow file-read*
  (literal "/")
  (subpath (param "TOOLCHAIN_DIR"))
  (subpath (param "SOURCE_DIR"))
  (subpath (param "ROOT_DIR"))
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/usr")
  (subpath "/System")
  (subpath "/Applications/Xcode.app/Contents/Developer")
  (subpath "/dev")
  (subpath "/etc"))

;; Writable: output + tmp + work dir
(allow file-write*
  (subpath (param "OUTPUT_DIR"))
  (subpath (param "TMPDIR"))
  (subpath (param "HOME_DIR"))
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
	if grants := s.hostPathRules(); grants != "" {
		profile += "\n;; Granted host paths (host_paths)\n" + grants
	}
	if network {
		profile += networkRules
	} else {
		profile += "\n;; Network denied\n(deny network-outbound)\n(deny network-inbound)\n(deny network-bind)\n"
	}
	return profile
}

// networkRules is appended for network: true. Besides sockets, macOS clients
// resolve names through mDNSResponder and verify TLS certificates through
// trustd/Security, which read the system keychains and trust settings.
const networkRules = `
;; Network allowed
(allow network-outbound)
(allow network-bind)
(allow mach-lookup
  (global-name "com.apple.dnssd.service")
  (global-name "com.apple.trustd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.ocspd"))
(allow file-read*
  (subpath "/private/etc")
  (subpath "/private/var/db/mds")
  (subpath "/Library/Keychains")
  (subpath "/System/Library/Keychains")
  (subpath "/System/Library/Security"))
`

// hostPathRules allows read and exec of each granted host path. The kernel
// matches real paths, so a path behind a symlink (/var -> /private/var,
// /opt/homebrew/bin/go -> ../Cellar/...) is granted under both spellings.
func (s *Sandbox) hostPathRules() string {
	var b strings.Builder
	for _, p := range s.HostPaths {
		paths := []string{p}
		if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
			paths = append(paths, real)
		}
		for _, path := range paths {
			fmt.Fprintf(&b, "(allow file-read* process-exec (subpath %s))\n", sbplString(path))
		}
	}
	return b.String()
}

// sbplString quotes s as an SBPL string literal.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// IsSandboxInit always returns false on macOS (no re-exec pattern needed).
func IsSandboxInit() bool { return false }

// RunInit is a no-op on macOS.
func RunInit() {}
