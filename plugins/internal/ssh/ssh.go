// Package ssh holds the SSH plumbing shared by the remote-exec and
// remote-file plugins: connection config, the action environment, shell
// quoting, and the fragments of the builder-side bash wrapper that choose
// between agent and password authentication.
//
// Nothing here opens a connection. The plugins only plan; the wrapper
// scripts built from these fragments run ssh when mu executes the action.
package ssh

import (
	"fmt"
	"strconv"
	"strings"
)

// ActionPath is the fixed PATH of every SSH action: the usual locations of
// ssh, sshpass and bash. It is a constant so it can be part of the action
// key without varying per machine.
const ActionPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

// Passthrough names the host variables ssh needs at execution time: HOME
// (for ~/.ssh config and known_hosts) and SSH_AUTH_SOCK (the agent). They
// are sent as env_passthrough so their values never enter the plan or the
// action key.
var Passthrough = []string{"HOME", "SSH_AUTH_SOCK"}

// ActionEnv returns a fresh copy of the declared environment of an SSH
// action.
func ActionEnv() map[string]string {
	return map[string]string{"PATH": ActionPath}
}

// PassthroughNames returns a fresh copy of Passthrough.
func PassthroughNames() []string {
	return append([]string(nil), Passthrough...)
}

// Options are the ssh options every invocation passes, as shell text with
// a trailing space.
const Options = "-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "

// Conn is the destination read from a target's config.
type Conn struct {
	Host string
	User string // default "root"
	Port string // default "22"
}

// Dest is the user@host argument to ssh.
func (c Conn) Dest() string { return c.User + "@" + c.Host }

// ConnFromConfig reads host, user and port from a target config. plugin is
// the plugin name used in the error for a missing host.
func ConnFromConfig(plugin string, cfg map[string]any) (Conn, error) {
	host, _ := cfg["host"].(string)
	if host == "" {
		return Conn{}, fmt.Errorf("%s requires config.host", plugin)
	}
	user := "root"
	if u, ok := cfg["user"].(string); ok && u != "" {
		user = u
	}
	return Conn{Host: host, User: user, Port: portString(cfg["port"])}, nil
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

// ConfigSchema returns the config_schema entries for the connection
// fields (host, user, port, sudo), for a plugin to extend with its own.
func ConfigSchema() map[string]any {
	return map[string]any{
		"host": map[string]any{"type": "string", "required": true, "description": "Remote hostname or IP"},
		"user": map[string]any{"type": "string", "default": "root", "description": "SSH login user"},
		"port": map[string]any{"type": "integer", "default": 22, "description": "SSH port"},
		"sudo": map[string]any{"type": "boolean", "default": false, "description": "Run privileged steps via sudo -S using SSH_PASS"},
	}
}

// Quote single-quotes s for safe inclusion in a bash command.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// QuoteArgv quotes each element of argv and joins them with spaces.
func QuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = Quote(a)
	}
	return strings.Join(parts, " ")
}

// WrapperPreamble is the start of every builder-side wrapper. It sets
// SSH_CMD to `sshpass -e ssh` when the SSH_PASS sealed input is present
// (exporting it as SSHPASS) and to `ssh -o BatchMode=yes` (agent or key
// auth, never a prompt) otherwise.
const WrapperPreamble = "set -eu\n" +
	"if [ -n \"${SSH_PASS:-}\" ]; then export SSHPASS=\"$SSH_PASS\"; fi\n" +
	"if [ -n \"${SSH_PASS:-}\" ]; then\n" +
	"  SSH_CMD=(sshpass -e ssh)\n" +
	"else\n" +
	"  SSH_CMD=(ssh -o BatchMode=yes)\n" +
	"fi\n"

// SudoPassExport returns wrapper lines that, when SSH_PASS is set, prepend
// `export <prefix>_SUDO_PASS=<SSH_PASS>` to $REMOTE_SCRIPT so the remote
// script can feed it to `sudo -S`. The value is %q-quoted by the builder's
// bash at execution time; it never appears in the plan.
func SudoPassExport(prefix string) string {
	return "if [ -n \"${SSH_PASS:-}\" ]; then\n" +
		"  " + prefix + "_SUDO_EXPORT=$(printf 'export " + prefix + "_SUDO_PASS=%q\\n' \"$SSH_PASS\")\n" +
		"  REMOTE_SCRIPT=\"${" + prefix + "_SUDO_EXPORT}\n${REMOTE_SCRIPT}\"\n" +
		"fi\n"
}

// RemoteCmdLine sets REMOTE_CMD to `bash -c <REMOTE_SCRIPT>` as one ssh
// argument, so the remote shell does not word-split the script.
const RemoteCmdLine = "REMOTE_CMD=\"bash -c $(printf %q \"$REMOTE_SCRIPT\")\"\n"
