package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryOutageHelper(t *testing.T) {
	if raw := os.Getenv("MU_OUTAGE_BUILD_ARGS"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			os.Exit(97)
		}
		os.Exit(runBuild(args))
	}
}

func outageBuild(t *testing.T, root string, args ...string) (int, []byte) {
	t.Helper()
	encoded, _ := json.Marshal(append([]string{"--config", filepath.Join(root, "mu.cue")}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegistryOutageHelper$")
	child.Env = append(os.Environ(), "MU_OUTAGE_BUILD_ARGS="+string(encoded), "HOME="+filepath.Join(root, "home"))
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("registry prevented local completion: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		return 0, output
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), output
	}
	t.Fatalf("start helper: %v", err)
	return -1, nil
}

func outageFixture(t *testing.T, registry string, onlyRemote bool, targets []map[string]any, plugins []map[string]any) string {
	t.Helper()
	root := t.TempDir()
	backends := []map[string]any{{"type": "oci", "registry": registry}}
	if !onlyRemote {
		backends = append([]map[string]any{{"type": "disk", "path": filepath.Join(root, "cache")}}, backends...)
	}
	cfg := map[string]any{"cache": map[string]any{"backends": backends, "write_through": true}, "targets": targets}
	if plugins != nil {
		cfg["plugins"] = plugins
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(root, "mu.cue"), append([]byte("package mu\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestColdLocalBuildSurvivesSilentRegistryOnceForDependentActions(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-r.Context().Done() }))
	defer server.Close()
	var targets []map[string]any
	for i := 0; i < 4; i++ {
		target := map[string]any{"target": fmt.Sprintf("//t%d", i), "toolchain": "shell", "config": map[string]any{"impure": false, "command": []string{"sh", "-c", fmt.Sprintf(`printf done > "$MU_OUT/out%d"`, i)}, "outputs": []string{fmt.Sprintf("out%d", i)}}}
		if i > 0 {
			target["deps"] = []string{fmt.Sprintf("//t%d", i-1)}
		}
		targets = append(targets, target)
	}
	root := outageFixture(t, strings.TrimPrefix(server.URL, "http://")+"/cache", false, targets, nil)
	code, output := outageBuild(t, root, "--emit-manifest", "//t3")
	if code != 0 {
		t.Fatalf("local rebuild failed: %d\n%s", code, output)
	}
	if requests.Load() != 1 {
		t.Fatalf("retried unavailable optional registry %d times", requests.Load())
	}
	for i := 0; i < 4; i++ {
		if data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("out%d", i))); err != nil || string(data) != "done" {
			t.Fatalf("missing local output: %q, %v", data, err)
		}
	}
	// A new command with fully warm local receipts must make zero remote calls.
	requests.Store(0)
	if code, output := outageBuild(t, root, "--emit-manifest", "//t3"); code != 0 {
		t.Fatalf("warm build: %d, %s", code, output)
	}
	if requests.Load() != 0 {
		t.Fatal("warm build contacted registry")
	}
}

func TestOfflineRemoteOnlyCacheAndUnusedPluginDoNotRequireRegistry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	target := map[string]any{"target": "//local", "toolchain": "shell", "config": map[string]any{"impure": false, "command": []string{"true"}}}
	plugins := []map[string]any{{"name": "unused", "digest": "sha256:" + strings.Repeat("a", 64)}}
	root := outageFixture(t, strings.TrimPrefix(server.URL, "http://")+"/cache", true, []map[string]any{target}, plugins)
	if code, output := outageBuild(t, root, "--offline", "--no-cache", "//local"); code != 0 {
		t.Fatalf("offline local action: %d, %s", code, output)
	}
	if requests.Load() != 0 {
		t.Fatal("offline contacted registry")
	}
}

func TestOfflineMissingRequiredPluginFailsWithoutSubstitution(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	target := map[string]any{"target": "//remote", "toolchain": "needed"}
	plugins := []map[string]any{{"name": "needed", "digest": "sha256:" + strings.Repeat("a", 64)}}
	root := outageFixture(t, strings.TrimPrefix(server.URL, "http://")+"/cache", true, []map[string]any{target}, plugins)
	code, output := outageBuild(t, root, "--offline", "//remote")
	if code == 0 || !strings.Contains(string(output), "sha256:"+strings.Repeat("a", 64)) {
		t.Fatalf("missing identity not reported: %d, %s", code, output)
	}
	if requests.Load() != 0 {
		t.Fatal("missing dependency triggered remote access")
	}
	if code, output := outageBuild(t, root, "--offline", "--publish", "//remote"); code != exitUsage {
		t.Fatalf("offline publish was not rejected: %d, %s", code, output)
	}
}

func TestExplicitRemoteDiscoveryDoesNotReportOutageAsEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	root := outageFixture(t, strings.TrimPrefix(server.URL, "http://")+"/cache", false, []map[string]any{}, nil)
	if code := runPluginList([]string{"--config", filepath.Join(root, "mu.cue"), "--remote", "--json"}); code != exitFail {
		t.Fatalf("unavailable registry reported success: %d", code)
	}
}
