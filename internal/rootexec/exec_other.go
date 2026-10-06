//go:build !darwin && !linux

package rootexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func Command(context.Context, []string, []string, *os.File) (*exec.Cmd, error) {
	return nil, fmt.Errorf("rooted working-directory execution is supported on Linux and macOS")
}
