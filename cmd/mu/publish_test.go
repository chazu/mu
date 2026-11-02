package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
)

func TestExpandPublishTargets(t *testing.T) {
	available := []string{"//pkg/a", "//pkg/b", "//other"}
	got, err := expandPublishTargets([]string{"//pkg/...", "//pkg/a", "//other"}, available)
	if err != nil {
		t.Fatal(err)
	}
	want := []publishCandidate{{"//pkg/a", true}, {"//pkg/b", false}, {"//other", true}}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %+v, want %+v", got, want)
		}
	}
	if _, err := expandPublishTargets([]string{"//nothing/..."}, available); err == nil {
		t.Fatal("unmatched wildcard was accepted")
	}
}

// publishFixture writes a project with two output-producing targets under
// //pkg/, one output-less target, and config.publish pointing at an
// in-memory httptest registry. HOME is redirected so nothing touches ~/.mu.
func publishFixture(t *testing.T) (configPath string, registry *ocimem.Registry) {
	t.Helper()
	registry = ocimem.New()
	server := httptest.NewServer(ociserver.New(registry, nil))
	t.Cleanup(server.Close)

	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	shell := func(name, command string, outputs []string) map[string]any {
		return map[string]any{"target": name, "toolchain": "shell", "config": map[string]any{
			"impure": false, "command": []string{"sh", "-c", command}, "outputs": outputs,
		}}
	}
	cfg := map[string]any{
		"publish": map[string]any{"repository": strings.TrimPrefix(server.URL, "http://") + "/org"},
		"targets": []map[string]any{
			shell("//pkg/a", `printf a > "$MU_OUT/a"`, []string{"a"}),
			shell("//pkg/b", `printf b > "$MU_OUT/b"`, []string{"b"}),
			shell("//pkg/none", "true", nil),
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(root, "mu.cue")
	if err := os.WriteFile(configPath, append([]byte("package mu\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath, registry
}

func publishedLatest(registry *ocimem.Registry, repo string) bool {
	_, err := registry.ResolveTag(context.Background(), repo, "latest")
	return err == nil
}

func TestBuildPublishExpandsWildcardTargets(t *testing.T) {
	configPath, registry := publishFixture(t)
	code := captureBuildCode(t, func() int {
		return runBuild([]string{"--config", configPath, "--publish", "//pkg/..."})
	})
	if code != exitOK {
		t.Fatalf("publish //pkg/... exit = %d, want %d", code, exitOK)
	}
	for _, repo := range []string{"org/pkg/a", "org/pkg/b"} {
		if !publishedLatest(registry, repo) {
			t.Errorf("%s:latest was not published", repo)
		}
	}
	if publishedLatest(registry, "org/pkg/none") {
		t.Error("output-less wildcard target was published")
	}
}

func TestBuildPublishNamedTargetWithoutOutputsFails(t *testing.T) {
	configPath, registry := publishFixture(t)
	code := captureBuildCode(t, func() int {
		return runBuild([]string{"--config", configPath, "--publish", "//pkg/none"})
	})
	if code != exitFail {
		t.Fatalf("publish //pkg/none exit = %d, want %d", code, exitFail)
	}

	// A named output-less target fails before anything else is pushed.
	code = captureBuildCode(t, func() int {
		return runBuild([]string{"--config", configPath, "--publish", "//pkg/a", "//pkg/none"})
	})
	if code != exitFail {
		t.Fatalf("publish //pkg/a //pkg/none exit = %d, want %d", code, exitFail)
	}
	if publishedLatest(registry, "org/pkg/a") {
		t.Error("//pkg/a was published although a named target had no outputs")
	}
}
