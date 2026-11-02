package plugintest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeSSHScript stands in for ssh. It never opens a connection: it parses
// the ssh options, records the destination, the argv and stdin of the call
// under $FAKE_SSH_DIR/call.N/, and then runs the remote command locally
// with bash (real ssh joins the remaining arguments with spaces and hands
// them to the remote shell, which this mimics).
const fakeSSHScript = `#!/bin/bash
set -eu
dir="$FAKE_SSH_DIR"
n=$(ls "$dir" | grep -c '^call\.' || true)
call="$dir/call.$((n+1))"
mkdir -p "$call"
printf '%s\n' "$@" > "$call/argv"
printf '%s' "${SSHPASS:-}" > "$call/sshpass"
port=22
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -p) port=$2; shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
printf '%s' "$1" > "$call/dest"
printf '%s' "$port" > "$call/port"
shift
remote="$*"
tee "$call/stdin" | bash -c "$remote"
`

// fakeSSHPassScript stands in for sshpass: it records that it was used and
// runs the rest of its argv ("-e ssh ...") with -e dropped.
const fakeSSHPassScript = `#!/bin/bash
set -eu
[ "$1" = "-e" ] && shift
: > "$FAKE_SSH_DIR/sshpass-used"
exec "$@"
`

// sha256sumShim lets the remote-side scripts' sha256sum run on hosts that
// only ship shasum (macOS).
const sha256sumShim = `#!/bin/bash
if command -v shasum >/dev/null 2>&1; then exec shasum -a 256 "$@"; fi
for d in /usr/bin /bin; do [ -x "$d/sha256sum" ] && exec "$d/sha256sum" "$@"; done
echo "sha256sum shim: no implementation" >&2; exit 1
`

// FakeSSH is a fake ssh/sshpass environment for executing a plugin's
// planned wrapper script without contacting any host.
type FakeSSH struct {
	Bin string // directory holding the fake ssh, sshpass and sha256sum
	Dir string // where each ssh call is recorded (call.1, call.2, ...)
}

// NewFakeSSH writes the fake tools into a fresh temp directory.
func NewFakeSSH(t testing.TB) *FakeSSH {
	t.Helper()
	f := &FakeSSH{Bin: t.TempDir(), Dir: t.TempDir()}
	for name, body := range map[string]string{
		"ssh":       fakeSSHScript,
		"sshpass":   fakeSSHPassScript,
		"sha256sum": sha256sumShim,
	} {
		if err := os.WriteFile(filepath.Join(f.Bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// SSHCall is one recorded invocation of the fake ssh.
type SSHCall struct {
	Argv    []string
	Dest    string
	Port    string
	Stdin   string
	SSHPass string // $SSHPASS as seen by ssh (set when run via sshpass -e)
}

// Run executes argv (normally an action's ["bash", "-c", wrapper]) in dir
// with only the fake tools and the system directories on PATH, plus env.
// It returns combined output and the error from the command.
func (f *FakeSSH) Run(t testing.TB, dir string, argv []string, env map[string]string) (string, error) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + f.Bin + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + t.TempDir(),
		"FAKE_SSH_DIR=" + f.Dir,
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Calls returns the recorded ssh invocations in order.
func (f *FakeSSH) Calls(t testing.TB) []SSHCall {
	t.Helper()
	var calls []SSHCall
	for i := 1; ; i++ {
		d := filepath.Join(f.Dir, fmt.Sprintf("call.%d", i))
		if _, err := os.Stat(d); err != nil {
			return calls
		}
		read := func(name string) string {
			data, _ := os.ReadFile(filepath.Join(d, name))
			return string(data)
		}
		calls = append(calls, SSHCall{
			Argv:    strings.Split(strings.TrimSuffix(read("argv"), "\n"), "\n"),
			Dest:    read("dest"),
			Port:    read("port"),
			Stdin:   read("stdin"),
			SSHPass: read("sshpass"),
		})
	}
}

// UsedSSHPass reports whether the wrapper went through sshpass.
func (f *FakeSSH) UsedSSHPass() bool {
	_, err := os.Stat(filepath.Join(f.Dir, "sshpass-used"))
	return err == nil
}
