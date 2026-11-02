// remote-exec plugin: run a command on a remote host via SSH, optionally
// guarded by a check command. Implements discover and plan.
//
// Unlike the retired Babashka version, a non-array command (for example
// "uptime") is rejected rather than split into single-character argv.
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
	"sort"
	"strings"

	"github.com/chazu/mu/plugins/internal/ssh"
	"github.com/chazu/mu/sdk/muplugin"
)

func main() {
	newPlugin().Main()
}

// newPlugin builds the plugin value; tests drive it in-process.
func newPlugin() *muplugin.Plugin {
	props := ssh.ConfigProperties()
	props["command"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "argv executed remotely"}
	props["check"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Guard command; if exits 0, command is skipped"}
	props["env"] = map[string]any{"type": "object", "description": "Environment variables exported before command"}
	props["work_dir"] = map[string]any{"type": "string", "description": "Directory to cd into before running command"}
	props["impure"] = map[string]any{"type": "boolean", "default": true, "description": "Skip CAS cache for this action"}
	props["sealed_output_files"] = map[string]any{"type": "object", "description": "Map of sealed output names to absolute remote file paths"}
	return &muplugin.Plugin{
		Name:        "remote-exec",
		Version:     "0.3.0",
		Description: "Execute commands on remote hosts over SSH",
		ConfigSchema: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"host", "command"},
			"additionalProperties": false,
		},
		Plan: plan,
	}
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	target := req.Target
	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	conn, err := ssh.ConnFromConfig("remote-exec", cfg)
	if err != nil {
		return muplugin.PlanResponse{}, err
	}
	if len(stringSlice(cfg["command"])) == 0 {
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

	sudo := boolVal(cfg, "sudo")
	impure := true
	if v, ok := cfg["impure"].(bool); ok {
		impure = v
	}

	wrapper := buildWrapper(cfg, conn, sudo, sortedKeys(target.SealedInputs), sealedFiles)

	// The single action claims exactly what the target declares (strict
	// sealed routing): every input with its mode, every output with its mode.
	action := muplugin.ActionSpec{
		ID:                "exec",
		Command:           []string{"bash", "-c", wrapper},
		Inputs:            map[string]string{},
		Outputs:           []string{},
		Env:               ssh.ActionEnv(),
		EnvPassthrough:    ssh.PassthroughNames(),
		SealedInputs:      target.SealedInputs,
		SealedInputModes:  target.SealedInputModes,
		SealedOutputs:     sealedOut,
		SealedOutputModes: target.SealedOutputModes,
		Network:           true,
		Impure:            impure,
		DependsOn:         []string{},
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

	// env exports in sorted order, so the script (and the action key) is
	// deterministic.
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "export %s=%s\n", k, ssh.Quote(env[k]))
	}

	if workDir != "" {
		fmt.Fprintf(&b, "cd %s\n", ssh.Quote(workDir))
	}

	if len(check) > 0 {
		fmt.Fprintf(&b, "if %s >/dev/null 2>&1; then echo '[check passed, skipping]'; exit 0; fi\n",
			ssh.QuoteArgv(check))
	}

	preserve := ""
	if len(forwardKeys) > 0 {
		preserve = "--preserve-env=" + strings.Join(forwardKeys, ",") + " "
	}
	runPrefix := ""
	if sudo {
		runPrefix = "printf '%s\\n' \"${RE_SUDO_PASS:-}\" | sudo -S -p '' " + preserve
	}
	fmt.Fprintf(&b, "%s%s\n", runPrefix, ssh.QuoteArgv(cmdArgv))

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
	for _, name := range sortedKeys(sealedFiles) {
		fmt.Fprintf(&b,
			"\"${SSH_CMD[@]}\" "+ssh.Options+
				"-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" "+
				"cat %s > \"$MU_SEALED_OUT_DIR/%s\"\n",
			ssh.Quote(sealedFiles[name]), name)
		fmt.Fprintf(&b, "chmod 600 \"$MU_SEALED_OUT_DIR/%s\"\n", name)
	}
	return b.String()
}

// buildWrapper renders the bash wrapper executed on the builder. It:
//  1. picks `sshpass -e ssh` or `ssh -o BatchMode=yes` from $SSH_PASS;
//  2. forwards every sealed input except SSH_PASS (reserved for ssh auth)
//     to the remote by prepending `export K=<printf %q>` lines;
//  3. with sudo, also prepends RE_SUDO_PASS derived from SSH_PASS;
//  4. passes the remote script as one ssh argument (no word splitting);
//  5. ssh-cats each sealed_output_files entry into $MU_SEALED_OUT_DIR.
func buildWrapper(cfg map[string]any, conn ssh.Conn, sudo bool, sealedKeys []string, sealedFiles map[string]string) string {
	forwardKeys := make([]string, 0, len(sealedKeys))
	for _, k := range sealedKeys {
		if k != "SSH_PASS" {
			forwardKeys = append(forwardKeys, k)
		}
	}
	remoteScript := buildRemoteScript(cfg, forwardKeys)

	var b strings.Builder
	b.WriteString(ssh.WrapperPreamble)
	fmt.Fprintf(&b, "REMOTE_SCRIPT=%s\n", ssh.Quote(remoteScript))

	for _, k := range forwardKeys {
		fmt.Fprintf(&b, "if [ -n \"${%s:-}\" ]; then\n", k)
		fmt.Fprintf(&b, "  _RE_EXPORT=$(printf 'export %s=%%q\\n' \"$%s\")\n", k, k)
		b.WriteString("  REMOTE_SCRIPT=\"${_RE_EXPORT}\n${REMOTE_SCRIPT}\"\n")
		b.WriteString("fi\n")
	}

	if sudo {
		b.WriteString(ssh.SudoPassExport("RE"))
	}

	b.WriteString(ssh.RemoteCmdLine)
	fmt.Fprintf(&b, "REMOTE_PORT=%s\n", ssh.Quote(conn.Port))
	fmt.Fprintf(&b, "REMOTE_DEST=%s\n", ssh.Quote(conn.Dest()))
	b.WriteString("\"${SSH_CMD[@]}\" " + ssh.Options)
	b.WriteString("-p \"$REMOTE_PORT\" \"$REMOTE_DEST\" ")
	b.WriteString("\"$REMOTE_CMD\"\n")
	b.WriteString(buildSealedOutputFetchers(sealedFiles))
	return b.String()
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
