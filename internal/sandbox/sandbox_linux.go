//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const sandboxInitArg = "__sandbox_init__"

func detectIsolation() IsolationLevel {
	if userNamespacesAvailable() {
		return IsolationNamespace
	}
	return IsolationCopy
}

func userNamespacesAvailable() bool {
	data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone")
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(data)) != "0"
}

// execIsolated runs the command inside Linux namespaces via re-exec.
func (s *Sandbox) execIsolated(ctx context.Context, command []string, env map[string]string, network bool) (int, error, bool) {
	if s.isolation != IsolationNamespace {
		return 0, nil, false
	}

	cfg := initConfig{
		RootDir: s.rootDir,
		WorkDir: s.workDir,
		Command: command,
		Env:     s.buildEnv(env),
		Network: network,
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return -1, fmt.Errorf("sandbox: marshal init config: %w", err), true
	}

	cloneFlags := syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS |
		syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	if !network {
		cloneFlags |= syscall.CLONE_NEWNET
	}

	cmd := exec.CommandContext(ctx, "/proc/self/exe", sandboxInitArg)
	cmd.Stdin = strings.NewReader(string(cfgJSON))
	cmd.Stdout = s.stdoutWriter()
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(cloneFlags),
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
		Pdeathsig:                  syscall.SIGKILL,
	}

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

// initConfig is passed from parent to the re-exec'd child via stdin.
type initConfig struct {
	RootDir string   `json:"root_dir"`
	WorkDir string   `json:"work_dir"`
	Command []string `json:"command"`
	Env     []string `json:"env"`
	Network bool     `json:"network"`
}

// RunInit is called early in main() when os.Args[1] == "__sandbox_init__".
// It sets up the namespace environment and execs the build command. Never returns.
func RunInit() {
	cfgJSON, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal("read config: %v", err)
	}

	var cfg initConfig
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		fatal("parse config: %v", err)
	}

	// 1. Break mount propagation
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fatal("mount private: %v", err)
	}

	// 2. Mount rootDir as a bind mount (pivot_root requires a mount point)
	if err := syscall.Mount(cfg.RootDir, cfg.RootDir, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		fatal("bind root: %v", err)
	}

	// 3. Set up minimal /dev inside the sandbox rootfs
	setupMinimalDev(cfg.RootDir)

	// 4. Set up /proc inside rootfs
	procDir := filepath.Join(cfg.RootDir, "proc")
	os.MkdirAll(procDir, 0o755)

	// 5. pivot_root
	putold := filepath.Join(cfg.RootDir, ".pivot_root")
	os.MkdirAll(putold, 0o700)
	if err := syscall.PivotRoot(cfg.RootDir, putold); err != nil {
		fatal("pivot_root: %v", err)
	}
	if err := os.Chdir("/"); err != nil {
		fatal("chdir /: %v", err)
	}

	// 6. Unmount old root
	if err := syscall.Unmount("/.pivot_root", syscall.MNT_DETACH); err != nil {
		fatal("unmount old root: %v", err)
	}
	os.RemoveAll("/.pivot_root")

	// 7. Mount /proc (PID namespace gives clean view)
	if err := syscall.Mount("proc", "/proc", "proc",
		syscall.MS_NODEV|syscall.MS_NOEXEC|syscall.MS_NOSUID, ""); err != nil {
		fatal("mount /proc: %v", err)
	}

	// 8. Make most of the filesystem read-only
	makeReadOnly([]string{"/work", "/out", "/tmp", "/dev"})

	// 9. Set hostname
	syscall.Sethostname([]byte("mu-sandbox"))

	// 10. Exec the build command
	argv0 := cfg.Command[0]
	// Resolve relative to /bin inside sandbox
	if !filepath.IsAbs(argv0) {
		if resolved, err := exec.LookPath(argv0); err == nil {
			argv0 = resolved
		}
	}

	if err := syscall.Exec(argv0, cfg.Command, cfg.Env); err != nil {
		fatal("exec %s: %v", argv0, err)
	}
}

func setupMinimalDev(rootfs string) {
	devDir := filepath.Join(rootfs, "dev")
	os.MkdirAll(devDir, 0o755)
	syscall.Mount("tmpfs", devDir, "tmpfs",
		syscall.MS_NOSUID|syscall.MS_NOEXEC, "mode=755")

	for _, dev := range []string{
		"/dev/null", "/dev/zero", "/dev/full",
		"/dev/random", "/dev/urandom",
	} {
		target := filepath.Join(rootfs, dev)
		f, err := os.Create(target)
		if err != nil {
			continue
		}
		f.Close()
		syscall.Mount(dev, target, "", syscall.MS_BIND, "")
	}

	os.Symlink("/proc/self/fd", filepath.Join(devDir, "fd"))
	os.Symlink("/proc/self/fd/0", filepath.Join(devDir, "stdin"))
	os.Symlink("/proc/self/fd/1", filepath.Join(devDir, "stdout"))
	os.Symlink("/proc/self/fd/2", filepath.Join(devDir, "stderr"))
}

func makeReadOnly(writablePaths []string) {
	writable := make(map[string]bool, len(writablePaths))
	for _, p := range writablePaths {
		writable[p] = true
	}

	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountpoint := fields[1]
		if writable[mountpoint] {
			continue
		}
		// Best-effort remount read-only
		syscall.Mount("", mountpoint, "",
			syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_RDONLY, "")
	}
}

// IsSandboxInit returns true if the process was re-exec'd as sandbox init.
func IsSandboxInit() bool {
	return len(os.Args) >= 2 && os.Args[1] == sandboxInitArg
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sandbox-init: "+format+"\n", args...)
	os.Exit(126)
}
