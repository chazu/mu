//go:build darwin || linux

// Package rootexec starts commands in an already-open directory without
// changing the parent's cwd or resolving a replaceable pathname in the child.
package rootexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const initArg = "__mu_rooted_exec_v1__"

// The trampoline also runs in test binaries that import this package. It
// consumes only an inherited descriptor and argv; no global environment switch
// can redirect a normal invocation into the helper.
func init() {
	if len(os.Args) < 3 || os.Args[1] != initArg {
		return
	}
	if err := syscall.Fchdir(3); err != nil {
		fail("enter work directory: %v", err)
	}
	syscall.CloseOnExec(3)
	command := os.Args[2:]
	env := os.Environ()
	if _, ok := os.LookupEnv("PATH"); !ok {
		os.Setenv("PATH", "/usr/bin:/bin")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		fail("find command: %v", err)
	}
	if err := syscall.Exec(path, command, env); err != nil {
		fail("execute command: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mu rooted exec: "+format+"\n", args...)
	os.Exit(126)
}

// Command keeps dir alive until Run completes; the caller owns and closes it.
// The child enters fd 3 with fchdir before looking up the command using its
// actual execution environment. Renaming/swapping the original path is harmless.
func Command(ctx context.Context, command []string, env []string, dir *os.File) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := append([]string{initArg}, command...)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.ExtraFiles = []*os.File{dir}
	cmd.Env = env
	return cmd, nil
}
