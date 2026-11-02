package dag_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/sandbox"
)

// nativeHelper builds internal/sandbox/testdata/native-helper, a static Go
// program that probes the sandbox from inside (no host shell needed).
func nativeHelper(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MU_NATIVE_HELPER"); p != "" {
		return p
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH; cannot build the native helper")
	}
	path := filepath.Join(t.TempDir(), "native-helper")
	cmd := exec.Command(goBin, "build", "-o", path, "../sandbox/testdata/native-helper")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v\n%s", err, out)
	}
	return path
}

// runHelper runs the native helper as a pure (sandboxed) action and reports
// whether it exited zero. The helper exits non-zero when its expectation
// (e.g. "this read is denied") does not hold.
func runHelper(t *testing.T, helper string, network bool, args ...string) bool {
	t.Helper()
	store := newStore(t)
	f, err := os.Open(helper)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.Put(context.Background(), f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	a := &dag.Action{
		ID: "probe", Command: append([]string{"native-helper"}, args...), Network: network,
		Toolchain: map[string]cas.Digest{"bin/native-helper": digest},
		Env:       map[string]string{}, ProjectRoot: project, WorkDir: project,
	}
	result := runGraph(t, &dag.Executor{Store: store, Workers: 1, NoCache: true}, a)
	return len(result.Failed) == 0
}

func requireKernelIsolation(t *testing.T) {
	t.Helper()
	if level := sandbox.DetectedLevel(); level == sandbox.IsolationCopy {
		t.Skip("kernel isolation unavailable on this host (copy isolation)")
	}
}

// network:false is enforced: a pure action cannot reach a local server; with
// network:true the same dial succeeds.
func TestSandboxNetworkEnforcement(t *testing.T) {
	requireKernelIsolation(t)
	helper := nativeHelper(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !runHelper(t, helper, false, "network", u.Host, "deny") {
		t.Fatal("network:false action reached a local server")
	}
	if !runHelper(t, helper, true, "network", u.Host, "allow") {
		t.Fatal("network:true action could not reach a local server")
	}
}

// A pure action can neither write nor read host files outside its sandbox.
func TestSandboxFilesystemConfinement(t *testing.T) {
	requireKernelIsolation(t)
	helper := nativeHelper(t)
	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("host sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !runHelper(t, helper, false, "write-denied", outside) {
		t.Fatal("sandboxed action wrote a host file")
	}
	if !runHelper(t, helper, false, "write-denied", filepath.Join(filepath.Dir(outside), "new-file")) {
		t.Fatal("sandboxed action created a host file")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "host sentinel" {
		t.Fatalf("host file changed: %q, %v", data, err)
	}
	if !runHelper(t, helper, false, "read-denied", outside) {
		t.Fatal("sandboxed action read a host file")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	ssh := filepath.Join(home, ".ssh")
	if _, err := os.Stat(ssh); err != nil {
		t.Skipf("%s does not exist", ssh)
	}
	if !runHelper(t, helper, false, "list-denied", ssh) {
		t.Fatalf("sandboxed action listed %s", ssh)
	}
	entries, _ := os.ReadDir(ssh)
	for _, e := range entries {
		if e.Type().IsRegular() {
			if !runHelper(t, helper, false, "read-denied", filepath.Join(ssh, e.Name())) {
				t.Fatalf("sandboxed action read %s", filepath.Join(ssh, e.Name()))
			}
			break
		}
	}
}
