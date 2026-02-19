package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// preprocessTimeout is the maximum time a preprocessor command may run.
const preprocessTimeout = 30 * time.Second

// Preprocess runs the configured preprocessor command against filePath,
// returning the parsed JSON output. The file path is appended as the last
// argument to the command. Stderr is captured and included in any error.
func Preprocess(pp *Preprocessor, filePath string) (*ProjectConfig, error) {
	if len(pp.Command) == 0 {
		return nil, fmt.Errorf("preprocessor: empty command")
	}

	ctx, cancel := context.WithTimeout(context.Background(), preprocessTimeout)
	defer cancel()

	args := make([]string, len(pp.Command)-1, len(pp.Command))
	copy(args, pp.Command[1:])
	args = append(args, filePath)

	cmd := exec.CommandContext(ctx, pp.Command[0], args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("preprocessor: command timed out after %s", preprocessTimeout)
		}
		// exec.ErrNotFound is wrapped by exec.CommandContext when the binary
		// doesn't exist, so errors.Is works here through the standard chain.
		if execErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("preprocessor: command exited with status %d: %s",
				execErr.ExitCode(), trimStderr(stderr.Bytes()))
		}
		return nil, fmt.Errorf("preprocessor: %w", err)
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil {
		return nil, fmt.Errorf("preprocessor: invalid JSON output: %w", err)
	}
	return &cfg, nil
}

// trimStderr returns at most 512 bytes of stderr, trimmed of trailing whitespace.
func trimStderr(b []byte) string {
	b = bytes.TrimRight(b, " \t\r\n")
	if len(b) > 512 {
		b = b[:512]
	}
	return string(b)
}
