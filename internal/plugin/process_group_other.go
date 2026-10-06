//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package plugin

import "os/exec"

func isolateProcess(cmd *exec.Cmd) {}
func killProcess(cmd *exec.Cmd)    { _ = cmd.Process.Kill() }
