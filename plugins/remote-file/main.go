// remote-file plugin: ensure a file on a remote host matches a local
// source file's bytes, mode, and owner over SSH. Implements discover and
// plan; every build re-asserts the file.
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
	"context"
	"fmt"
	"strings"

	"github.com/chazu/mu/plugins/internal/ssh"
	"github.com/chazu/mu/sdk/muplugin"
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

func main() {
	newPlugin().Main()
}

// newPlugin builds the plugin value; tests drive it in-process.
func newPlugin() *muplugin.Plugin {
	schema := ssh.ConfigSchema()
	schema["path"] = map[string]any{"type": "string", "required": true, "description": "Absolute path of the file on the remote host"}
	schema["mode"] = map[string]any{"type": "string", "description": "File mode, e.g. \"0644\" (default 0644)"}
	schema["owner"] = map[string]any{"type": "string", "description": "Owning user on the remote host"}
	schema["group"] = map[string]any{"type": "string", "description": "Owning group on the remote host"}
	schema["make_parents"] = map[string]any{"type": "boolean", "default": true, "description": "Create the parent directory if missing"}
	return &muplugin.Plugin{
		Name:         "remote-file",
		Version:      "0.2.0",
		Description:  "Manage files on remote hosts over SSH with ownership and permissions",
		Produces:     []string{"remote.file"},
		ConfigSchema: schema,
		Plan:         plan,
	}
}

func validateTarget(t muplugin.TargetInfo) (ssh.Conn, error) {
	if len(t.Sources) != 1 {
		return ssh.Conn{}, fmt.Errorf("remote-file requires exactly one source")
	}
	conn, err := ssh.ConnFromConfig("remote-file", t.Config)
	if err != nil {
		return ssh.Conn{}, err
	}
	if p, _ := t.Config["path"].(string); p == "" {
		return ssh.Conn{}, fmt.Errorf("remote-file requires config.path")
	}
	return conn, nil
}

func boolDefault(cfg map[string]any, key string, def bool) bool {
	b, ok := cfg[key].(bool)
	if !ok {
		return def
	}
	return b
}

// buildExportLines renders the RF_* exports prepended to the remote
// script. RF_SUDO_PASS is added later by the builder-side wrapper from
// $SSH_PASS, so it never appears in the plan.
func buildExportLines(cfg map[string]any) string {
	path, _ := cfg["path"].(string)
	var b strings.Builder
	b.WriteString("export RF_PATH=" + ssh.Quote(path) + "\n")
	mp := "1"
	if !boolDefault(cfg, "make_parents", true) {
		mp = "0"
	}
	b.WriteString("export RF_MAKE_PARENTS=" + ssh.Quote(mp) + "\n")
	sudo := "0"
	if boolDefault(cfg, "sudo", false) {
		sudo = "1"
	}
	b.WriteString("export RF_SUDO=" + ssh.Quote(sudo) + "\n")
	for _, k := range []string{"mode", "owner", "group"} {
		if v, ok := cfg[k].(string); ok && v != "" {
			b.WriteString("export RF_" + strings.ToUpper(k) + "=" + ssh.Quote(v) + "\n")
		}
	}
	return b.String()
}

// buildWrapper constructs the builder-side bash wrapper: it picks
// sshpass-vs-BatchMode, builds the quoted remote script, and pipes the
// source file's bytes to ssh's stdin.
func buildWrapper(cfg map[string]any, srcPath string, conn ssh.Conn, sudo bool) string {
	remoteScript := buildExportLines(cfg) + writeScriptBody

	var b strings.Builder
	b.WriteString(ssh.WrapperPreamble)
	b.WriteString("REMOTE_SCRIPT=" + ssh.Quote(remoteScript) + "\n")
	if sudo {
		b.WriteString(ssh.SudoPassExport("RF"))
	}
	b.WriteString(ssh.RemoteCmdLine)
	b.WriteString("cat " + ssh.Quote(srcPath) + " | \"${SSH_CMD[@]}\" " + ssh.Options)
	b.WriteString("-p " + ssh.Quote(conn.Port) + " ")
	b.WriteString(ssh.Quote(conn.Dest()) + " ")
	b.WriteString("\"$REMOTE_CMD\"\n")
	return b.String()
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	if req.Target.Config == nil {
		req.Target.Config = map[string]any{}
	}
	conn, err := validateTarget(req.Target)
	if err != nil {
		return muplugin.PlanResponse{}, err
	}
	cfg := req.Target.Config
	src := req.Target.Sources[0]
	path, _ := cfg["path"].(string)

	// The action claims exactly the target's sealed inputs and their
	// delivery modes (strict sealed routing). remote-file produces no
	// sealed outputs; a target declaring some fails routing validation.
	action := muplugin.ActionSpec{
		ID:               "push",
		Command:          []string{"bash", "-c", buildWrapper(cfg, src, conn, boolDefault(cfg, "sudo", false))},
		Inputs:           map[string]string{"src": src},
		Outputs:          []string{},
		Env:              ssh.ActionEnv(),
		EnvPassthrough:   ssh.PassthroughNames(),
		SealedInputs:     req.Target.SealedInputs,
		SealedInputModes: req.Target.SealedInputModes,
		Network:          true,
		Impure:           true,
		DependsOn:        []string{},
	}

	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{action},
		Outputs: map[string]string{"remote.file": path},
	}, nil
}
