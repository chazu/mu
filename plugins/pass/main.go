// pass plugin: bidirectional secret provider backed by the `pass`
// password-store CLI (https://passwordstore.org).
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// Behavior matches the bb version exactly for both resolve_secret and
// store_secret, including the "raw:" ref prefix handling and the three
// store modes (create, overwrite, create_if_absent).
//
// Capabilities: discover, resolve_secret, store_secret.
// SecretPlugin wires both resolve + store and derives the capabilities list.
//
// Secret ref grammar (resolve_secret):
//
//	"<path>"      -> first line of `pass show <path>`, trimmed.
//	"raw:<path>"  -> full output of `pass show <path>` with trailing
//	                 newlines trimmed. For multi-line secrets such as
//	                 SSH private keys, certificates, or JSON blobs.
//
// On store_secret, a leading "raw:" prefix is stripped because there is
// no first-line/full-content distinction at insert time — `pass insert`
// always stores raw bytes.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/chazu/mu/sdk/muplugin"
)

func main() {
	muplugin.SecretPlugin("pass", "0.3.0", &passBackend{}).Main()
}

// passBackend implements muplugin.SecretBackend by shelling out to the
// `pass` CLI. It holds no state; one zero-value instance is reused for
// the lifetime of the process.
type passBackend struct{}

// Resolve runs `pass show <path>` and returns either the first line
// (default) or the full trimmed output (when ref starts with "raw:").
// Matches the bb plugin's str/trim and trailing-newline behavior.
func (passBackend) Resolve(ctx context.Context, ref string) (string, error) {
	raw := strings.HasPrefix(ref, "raw:")
	path := ref
	if raw {
		path = ref[len("raw:"):]
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pass", "show", path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		return "", fmt.Errorf("pass show failed (exit %d): %s",
			exitCode, strings.TrimSpace(stderr.String()))
	}

	out := stdout.String()
	if raw {
		// Trim trailing newlines only; preserve interior whitespace.
		return strings.TrimRight(out, "\n"), nil
	}
	// First line, trimmed of surrounding whitespace.
	first := out
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		first = out[:i]
	}
	return strings.TrimSpace(first), nil
}

// Store writes value to the pass entry at ref, honoring mode:
//
//	"create"           - fail if the entry already exists.
//	"overwrite"        - always (re)write the entry. Also the default
//	                     when mode is "" (matches bb's nil/empty case).
//	"create_if_absent" - no-op if the entry exists; otherwise insert.
//
// A leading "raw:" prefix on ref is stripped (read-time hint only).
func (passBackend) Store(ctx context.Context, ref, value, mode string) error {
	if value == "" {
		// Match bb: nil/missing value is rejected. The wire protocol
		// can't distinguish nil from empty string in Go, so treat
		// empty as "not provided" — empty secrets are not useful.
		return errors.New("store_secret requires secret_value")
	}
	path := ref
	if strings.HasPrefix(ref, "raw:") {
		path = ref[len("raw:"):]
	}

	switch mode {
	case "create", "create_if_absent":
		if passExists(ctx, path) {
			if mode == "create_if_absent" {
				return nil
			}
			return fmt.Errorf("pass entry already exists: %s", path)
		}
		return passInsert(ctx, path, value)
	case "overwrite", "":
		return passInsert(ctx, path, value)
	default:
		return fmt.Errorf("unknown secret_mode: %s", mode)
	}
}

// passExists reports whether `pass show <path>` succeeds (exit 0).
// Mirrors the bb plugin's pass-exists? helper.
func passExists(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "pass", "show", path)
	// Discard stdout/stderr — we only care about the exit code.
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	return cmd.Run() == nil
}

// passInsert runs `pass insert -m -f <path>` with value piped to stdin.
// Captures stdout/stderr (rather than inheriting) so that pass's "Enter
// contents..." prompts cannot corrupt the NDJSON channel on stdout.
func passInsert(ctx context.Context, path, value string) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pass", "insert", "-m", "-f", path)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		return fmt.Errorf("pass insert failed (exit %d): %s",
			exitCode, strings.TrimSpace(stderr.String()))
	}
	return nil
}
