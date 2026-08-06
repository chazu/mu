// remote-exec plugin: run a command on a remote host via SSH.
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// Optionally guarded by a check command. Implements discover and plan
// (no observe).
//
// Config:
//
//	host                (string, required)
//	user                (string, default "root")
//	port                (int, default 22)
//	command             ([]string, required — argv executed remotely)
//	check               ([]string, optional — if exits 0, command is skipped)
//	env                 (map[string]string, optional — exported before command)
//	work_dir            (string, optional — cd before command)
//	sudo                (bool, default false)
//	impure              (bool, default true)
//	sealed_output_files (map[string]string, optional — sealed_output NAME ->
//	                     absolute remote path)
//
// Sealed inputs:  SSH_PASS (required when sudo:true).
// Sealed outputs: see sealed_output_files above.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/chazu/mu/sdk/muplugin"
)

func main() {
	(&muplugin.Plugin{
		Name:        "remote-exec",
		Version:     "0.2.0",
		Description: "Execute commands on remote hosts over SSH",
		ConfigSchema: map[string]any{
			"host":                map[string]any{"type": "string", "description": "Remote hostname or IP"},
			"user":                map[string]any{"type": "string", "default": "root", "description": "SSH login user"},
			"port":                map[string]any{"type": "integer", "default": 22, "description": "SSH port"},
			"command":             map[string]any{"type": "array", "description": "argv executed remotely"},
			"check":               map[string]any{"type": "array", "description": "Guard command; if exits 0, command is skipped"},
			"env":                 map[string]any{"type": "object", "description": "Environment variables exported before command"},
			"work_dir":            map[string]any{"type": "string", "description": "Directory to cd into before running command"},
			"sudo":                map[string]any{"type": "boolean", "default": false, "description": "Run command via sudo -S using SSH_PASS"},
			"impure":              map[string]any{"type": "boolean", "default": true, "description": "Skip CAS cache for this action"},
			"sealed_output_files": map[string]any{"type": "object", "description": "Map of sealed output names to absolute remote file paths"},
		},
		Plan: plan,
	}).Main()
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	target := req.Target
	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	host, _ := cfg["host"].(string)
	if host == "" {
		return muplugin.PlanResponse{}, fmt.Errorf("remote-exec requires config.host")
	}
	cmdArgv := stringSlice(cfg["command"])
	if len(cmdArgv) == 0 {
		return muplugin.PlanResponse{}, fmt.Errorf("remote-exec requires config.command")
	}

	sealedOut := target.SealedOutputs
	sealedFiles := stringMap(cfg["sealed_output_files"])
	if len(sealedOut) > 0 {
		outKeys := keySet(sealedOut)
		fileKeys := keySet(sealedFiles)
		if !sameKeys(outKeys, fileKeys) {
			return muplugin.PlanResponse{}, fmt.Errorf(
				"remote-exec: sealed_outputs keys %s must match config.sealed_output_files keys %s",
				formatKeySet(outKeys), formatKeySet(fileKeys))
		}
	}

	user := "root"
	if u, ok := cfg["user"].(string); ok && u != "" {
		user = u
	}
	port := portString(cfg["port"])
	sudo := boolVal(cfg, "sudo")
	impure := true
	if v, ok := cfg["impure"].(bool); ok {
		impure = v
	}

	sealedInputs := target.SealedInputs
	sealedModes := target.SealedInputModes
	sealedOutModes := target.SealedOutputModes

	wrapper := buildWrapper(cfg, host, user, port, sudo, sortedKeys(sealedInputs), sealedFiles)

	env := map[string]string{
		"PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
	}
	if v := os.Getenv("SSH_AUTH_SOCK"); v != "" {
		env["SSH_AUTH_SOCK"] = v
	}
	if v := os.Getenv("HOME"); v != "" {
		env["HOME"] = v
	}

	action := muplugin.ActionSpec{
		ID:           "exec",
		Command:      []string{"bash", "-c", wrapper},
		Inputs:       map[string]string{},
		Outputs:      []string{},
		Env:          env,
		SealedInputs: sealedInputs,
		Network:      true,
		Impure:       impure,
		DependsOn:    []string{},
	}
	if len(sealedModes) > 0 {
		action.SealedInputModes = sealedModes
	}
	if len(sealedOut) > 0 {
		action.SealedOutputs = sealedOut
	}
	if len(sealedOutModes) > 0 {
		action.SealedOutputModes = sealedOutModes
	}

	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{action},
		Outputs: map[string]string{},
	}, nil
}

// buildRemoteScript renders the bash snippet that runs on the remote host.
// forwardKeys: sealed_input env var names (minus SSH_PASS) forwarded through
// sudo via --preserve-env.
func buildRemoteScript(cfg map[string]any, forwardKeys []string) string {
	cmdArgv := stringSlice(cfg["command"])
	check := stringSlice(cfg["check"])
	env := stringMap(cfg["env"])
	workDir, _ := cfg["work_dir"].(string)
	sudo := boolVal(cfg, "sudo")

	var b strings.Builder
	b.WriteString("set -eu\n")

	// env exports: stable sort to match the file plugin's ordering policy
	// (bb iterates map order which is undefined; sorted is deterministic).
	envKeys := make([]string, 0, len(env))
	for k := range env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}

	if workDir != "" {
		fmt.Fprintf(&b, "cd %s\n", shellQuote(workDir))
	}

	if len(check) > 0 {
		fmt.Fprintf(&b, "if %s >/dev/null 2>&1; then echo '[check passed, skipping]'; exit 0; fi\n",
			argvToStr(check))
	}

	preserve := ""
	if len(forwardKeys) > 0 {
		preserve = "--preserve-env=" + strings.Join(forwardKeys, ",") + " "
	}
	runPrefix := ""
	if sudo {
		runPrefix = "printf '%s\\n' \"${RE_SUDO_PASS:-}\" | sudo -S -p '' " + preserve
	}
	fmt.Fprintf(&b, "%s%s\n", runPrefix, argvToStr(cmdArgv))

	return b.String()
}

// buildSealedOutputFetchers emits bash lines that ssh-cat each sealed_output
// file from the remote into $MU_SEALED_OUT_DIR/NAME (0600).
func buildSealedOutputFetchers(sealedFiles map[string]string) string {
	if len(sealedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n")
	b.WriteString("  echo 'remote-exec: sealed_output_files declared but MU_SEALED_OUT_DIR is unset' >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	names := make([]string, 0, len(sealedFiles))
	for k := range sealedFiles {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		remotePath := sealedFiles[name]
		fmt.Fprintf(&b,
			"\"${SSH_CMD[@]}\" "+
				"-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "+
				"-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" "+
				"cat %s > \"$MU_SEALED_OUT_DIR/%s\"\n",
			shellQuote(remotePath), name)
		fmt.Fprintf(&b, "chmod 600 \"$MU_SEALED_OUT_DIR/%s\"\n", name)
	}
	return b.String()
}

// buildWrapper renders the bash wrapper executed on the builder.
func buildWrapper(cfg map[string]any, host, user, port string, sudo bool, sealedKeys []string, sealedFiles map[string]string) string {
	forwardKeys := make([]string, 0, len(sealedKeys))
	for _, k := range sealedKeys {
		if k != "SSH_PASS" {
			forwardKeys = append(forwardKeys, k)
		}
	}
	remoteScript := buildRemoteScript(cfg, forwardKeys)
	quotedScript := shellQuote(remoteScript)

	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then export SSHPASS=\"$SSH_PASS\"; fi\n")
	b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then\n")
	b.WriteString("  SSH_CMD=(sshpass -e ssh)\n")
	b.WriteString("else\n")
	b.WriteString("  SSH_CMD=(ssh -o BatchMode=yes)\n")
	b.WriteString("fi\n")
	fmt.Fprintf(&b, "REMOTE_SCRIPT=%s\n", quotedScript)

	for _, k := range forwardKeys {
		fmt.Fprintf(&b, "if [ -n \"${%s:-}\" ]; then\n", k)
		fmt.Fprintf(&b, "  _RE_EXPORT=$(printf 'export %s=%%q\\n' \"$%s\")\n", k, k)
		b.WriteString("  REMOTE_SCRIPT=\"${_RE_EXPORT}\n${REMOTE_SCRIPT}\"\n")
		b.WriteString("fi\n")
	}

	if sudo {
		b.WriteString("if [ -n \"${SSH_PASS:-}\" ]; then\n")
		b.WriteString("  RE_SUDO_EXPORT=$(printf 'export RE_SUDO_PASS=%q\\n' \"$SSH_PASS\")\n")
		b.WriteString("  REMOTE_SCRIPT=\"${RE_SUDO_EXPORT}\n${REMOTE_SCRIPT}\"\n")
		b.WriteString("fi\n")
	}

	b.WriteString("REMOTE_CMD=\"bash -c $(printf %q \"$REMOTE_SCRIPT\")\"\n")
	fmt.Fprintf(&b, "REMOTE_PORT=%s\n", shellQuote(port))
	fmt.Fprintf(&b, "REMOTE_DEST=%s\n", shellQuote(user+"@"+host))
	b.WriteString("\"${SSH_CMD[@]}\" ")
	b.WriteString("-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ")
	b.WriteString("-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" ")
	b.WriteString("\"$REMOTE_CMD\"\n")
	b.WriteString(buildSealedOutputFetchers(sealedFiles))
	return b.String()
}

// shellQuote single-quotes a string for safe inclusion in a bash command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func argvToStr(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func stringSlice(v any) []string {
	s, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(s))
	for _, x := range s {
		if str, ok := x.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func portString(v any) string {
	switch x := v.(type) {
	case string:
		if x != "" {
			return x
		}
	case float64:
		return strconv.Itoa(int(x))
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return "22"
}

func boolVal(cfg map[string]any, key string) bool {
	b, _ := cfg[key].(bool)
	return b
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func keySet(m map[string]string) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func sameKeys(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func formatKeySet(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("#{")
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%q", k)
	}
	b.WriteByte('}')
	return b.String()
}
