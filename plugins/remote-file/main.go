// remote-file plugin: ensure a file on a remote host matches a local
// source file's bytes, mode, and owner over SSH.
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// Implements plan (converge) and observe (drift detection). Output bytes
// match the bb version action-for-action so existing scenarios continue
// to pass.
//
// Config:
//
//	host         (string, required)
//	user         (string, default "root")
//	port         (int, default 22)
//	path         (string, required — absolute remote path)
//	mode         (string, optional — e.g. "0644")
//	owner        (string, optional)
//	group        (string, optional)
//	make_parents (bool, default true)
//	sudo         (bool, default false)
//
// Sources: exactly one file. Sealed inputs: SSH_PASS (optional; required
// if sudo:true).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/chau/mu/sdk/muplugin"
)

const writeScriptBody = `set -eu
run() {
  if [ "${RF_SUDO:-0}" = '1' ]; then
    printf '%s\n' "${RF_SUDO_PASS:-}" | sudo -S -p '' "$@"
  else
    "$@"
  fi
}
if [ "${RF_MAKE_PARENTS:-1}" = '1' ]; then
  DIR=$(dirname "$RF_PATH")
  run install -d "$DIR"
fi
TMP=$(mktemp)
cat > "$TMP"
run install -m "${RF_MODE:-0644}" "$TMP" "$RF_PATH"
rm -f "$TMP"
if [ -n "${RF_OWNER:-}${RF_GROUP:-}" ]; then
  OWN="${RF_OWNER:-}"
  if [ -n "${RF_GROUP:-}" ]; then
    OWN="${OWN}:${RF_GROUP}"
  fi
  run chown "$OWN" "$RF_PATH"
fi
sha256sum "$RF_PATH" | awk '{print $1}'
`

const observeScript = `set -eu
if [ ! -e "$RF_PATH" ]; then
  printf '{"exists":false}\n'
  exit 0
fi
SHA=$(sha256sum "$RF_PATH" | awk '{print $1}')
MODE=$(stat -c '%a' "$RF_PATH")
OWNER=$(stat -c '%U' "$RF_PATH")
GROUP=$(stat -c '%G' "$RF_PATH")
printf '{"exists":true,"sha256":"%s","mode":"0%s","owner":"%s","group":"%s"}\n' \
  "$SHA" "$MODE" "$OWNER" "$GROUP"
`

func main() {
	(&muplugin.Plugin{
		Name:        "remote-file",
		Version:     "0.1.0",
		Description: "Manage files on remote hosts over SSH with ownership and permissions",
		Produces:    []string{"remote.file"},
		Plan:        plan,
		Observe:     observe,
	}).Main()
}

// shellQuote single-quotes a string for safe inclusion in a bash command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func validateTarget(t muplugin.TargetInfo) error {
	if len(t.Sources) != 1 {
		return fmt.Errorf("remote-file requires exactly one source")
	}
	if h, _ := t.Config["host"].(string); h == "" {
		return fmt.Errorf("remote-file requires config.host")
	}
	if p, _ := t.Config["path"].(string); p == "" {
		return fmt.Errorf("remote-file requires config.path")
	}
	return nil
}

func portString(cfg map[string]any) string {
	p, ok := cfg["port"]
	if !ok {
		return "22"
	}
	switch v := p.(type) {
	case string:
		if v != "" {
			return v
		}
	case float64:
		return strconv.Itoa(int(v))
	case int:
		return strconv.Itoa(v)
	}
	return "22"
}

func boolDefault(cfg map[string]any, key string, def bool) bool {
	v, ok := cfg[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// buildExportLines mirrors plugin.bb's build-export-lines. RF_SUDO_PASS is
// injected later at builder-wrapper time from $SSH_PASS when sudo:true, so
// it is NOT included here.
func buildExportLines(cfg map[string]any) string {
	path, _ := cfg["path"].(string)
	var b strings.Builder
	b.WriteString("export RF_PATH=" + shellQuote(path) + "\n")
	mp := "1"
	if !boolDefault(cfg, "make_parents", true) {
		mp = "0"
	}
	b.WriteString("export RF_MAKE_PARENTS=" + shellQuote(mp) + "\n")
	sudo := "0"
	if boolDefault(cfg, "sudo", false) {
		sudo = "1"
	}
	b.WriteString("export RF_SUDO=" + shellQuote(sudo) + "\n")
	if mode, ok := cfg["mode"].(string); ok && mode != "" {
		b.WriteString("export RF_MODE=" + shellQuote(mode) + "\n")
	}
	if owner, ok := cfg["owner"].(string); ok && owner != "" {
		b.WriteString("export RF_OWNER=" + shellQuote(owner) + "\n")
	}
	if group, ok := cfg["group"].(string); ok && group != "" {
		b.WriteString("export RF_GROUP=" + shellQuote(group) + "\n")
	}
	return b.String()
}

// buildWrapper constructs the builder-side bash wrapper that picks
// sshpass-vs-BatchMode, builds a quoted remote script, and pipes the
// source file's bytes to ssh's stdin. Byte-for-byte equivalent to the
// bb plugin's build-wrapper.
func buildWrapper(cfg map[string]any, srcPath, host, user, port string, sudo bool) string {
	exports := buildExportLines(cfg)
	remoteScript := exports + writeScriptBody
	quotedScript := shellQuote(remoteScript)

	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then export SSHPASS=\"$SSH_PASS\"; fi\n")
	b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then\n")
	b.WriteString("  SSH_CMD=(sshpass -e ssh)\n")
	b.WriteString("else\n")
	b.WriteString("  SSH_CMD=(ssh -o BatchMode=yes)\n")
	b.WriteString("fi\n")
	b.WriteString("REMOTE_SCRIPT=" + quotedScript + "\n")
	if sudo {
		b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then\n")
		b.WriteString("  RF_SUDO_EXPORT=$(printf 'export RF_SUDO_PASS=%q\\n' \"$SSH_PASS\")\n")
		b.WriteString("  REMOTE_SCRIPT=\"${RF_SUDO_EXPORT}\n${REMOTE_SCRIPT}\"\n")
		b.WriteString("fi\n")
	}
	b.WriteString("REMOTE_CMD=\"bash -c $(printf %q \"$REMOTE_SCRIPT\")\"\n")
	b.WriteString("cat " + shellQuote(srcPath) + " | \"${SSH_CMD[@]}\" ")
	b.WriteString("-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ")
	b.WriteString("-p " + shellQuote(port) + " ")
	b.WriteString(shellQuote(user+"@"+host) + " ")
	b.WriteString("\"$REMOTE_CMD\"\n")
	return b.String()
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	if err := validateTarget(req.Target); err != nil {
		return muplugin.PlanResponse{}, err
	}
	cfg := req.Target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	src := req.Target.Sources[0]
	host, _ := cfg["host"].(string)
	user := "root"
	if u, ok := cfg["user"].(string); ok && u != "" {
		user = u
	}
	port := portString(cfg)
	sudo := boolDefault(cfg, "sudo", false)
	path, _ := cfg["path"].(string)

	wrapper := buildWrapper(cfg, src, host, user, port, sudo)

	env := map[string]string{
		"PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
	}
	if v := os.Getenv("SSH_AUTH_SOCK"); v != "" {
		env["SSH_AUTH_SOCK"] = v
	}
	if v := os.Getenv("HOME"); v != "" {
		env["HOME"] = v
	}

	sealed := req.Target.SealedInputs

	action := muplugin.ActionSpec{
		ID:           "push",
		Command:      []string{"bash", "-c", wrapper},
		Inputs:       map[string]string{"src": src},
		Outputs:      []string{},
		Env:          env,
		SealedInputs: sealed,
		Network:      true,
		Impure:       true,
		DependsOn:    []string{},
	}

	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{action},
		Outputs: map[string]string{"remote.file": path},
	}, nil
}

// sshArgv builds the ssh argv (without a trailing remote command) from
// config. When SSH_PASS is present, wraps with sshpass for password auth.
// When absent, uses BatchMode=yes (agent-only). Mirrors plugin.bb's
// ssh-argv exactly.
func sshArgv(cfg map[string]any, hasPass bool) []string {
	host, _ := cfg["host"].(string)
	user := "root"
	if u, ok := cfg["user"].(string); ok && u != "" {
		user = u
	}
	port := portString(cfg)
	opts := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"-p", port,
	}
	if !hasPass {
		opts = append(opts, "-o", "BatchMode=yes")
	}
	dest := user + "@" + host
	var base []string
	if hasPass {
		base = []string{"sshpass", "-e", "ssh"}
	} else {
		base = []string{"ssh"}
	}
	out := append(base, opts...)
	out = append(out, dest)
	return out
}

func observe(ctx context.Context, req muplugin.ObserveRequest) (muplugin.ObserveResponse, error) {
	if err := validateTarget(req.Target); err != nil {
		return muplugin.ObserveResponse{Error: "observe failed: " + err.Error()}, nil
	}
	cfg := req.Target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	path, _ := cfg["path"].(string)
	host, _ := cfg["host"].(string)
	pass := req.Secrets["SSH_PASS"]
	hasPass := pass != ""

	argv := sshArgv(cfg, hasPass)
	argv = append(argv, "env", "RF_PATH="+path, "bash", "-s")

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(observeScript)
	if hasPass {
		cmd.Env = append(os.Environ(), "SSHPASS="+pass)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		return muplugin.ObserveResponse{
			Error: fmt.Sprintf("observe failed (exit %d): %s",
				exitCode, strings.TrimSpace(stderr.String())),
		}, nil
	}

	var state map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &state); err != nil {
		return muplugin.ObserveResponse{
			Error: "observe failed: " + err.Error(),
		}, nil
	}

	record := map[string]any{
		"_schema": "remote.file",
		"host":    host,
		"path":    path,
	}
	for k, v := range state {
		record[k] = v
	}

	return muplugin.ObserveResponse{
		Current: map[string]any{
			"records": []any{record},
		},
	}, nil
}
