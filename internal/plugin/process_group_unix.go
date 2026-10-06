//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package plugin

import (
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killProcess(cmd *exec.Cmd) {
	// Plugins often start a shell or compiler; kill the ordinary descendants
	// that share the dedicated process group as well as the direct child.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
