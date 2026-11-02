package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
)

// pushFixture is two projects sharing one local cache, and an in-memory
// registry that project "a" pushes to. HOME is redirected so nothing touches
// ~/.mu or ~/.docker.
type pushFixture struct {
	registry *ocimem.Registry
	cacheDir string
	configs  map[string]string // project name -> mu.cue path
}

const pushRepo = "acme/cache"

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	registry := ocimem.New()
	server := httptest.NewServer(ociserver.New(registry, nil))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")

	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("DOCKER_CONFIG", filepath.Join(root, "home", ".docker"))
	f := &pushFixture{registry: registry, cacheDir: filepath.Join(root, "cache"), configs: map[string]string{}}
	shell := func(name, out string) map[string]any {
		return map[string]any{"target": name, "toolchain": "shell", "config": map[string]any{
			"impure": false, "command": []string{"sh", "-c", `printf ` + name + ` > "$MU_OUT/` + out + `"`}, "outputs": []string{out},
		}}
	}
	for project, targets := range map[string][]map[string]any{
		"a": {shell("//a1", "a1"), shell("//a2", "a2")},
		"b": {shell("//b1", "b1")},
	} {
		dir := filepath.Join(root, project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := map[string]any{
			"cache":   map[string]any{"path": f.cacheDir, "push": host + "/" + pushRepo},
			"targets": targets,
		}
		data, _ := json.Marshal(cfg)
		path := filepath.Join(dir, "mu.cue")
		if err := os.WriteFile(path, append([]byte("package mu\n"), data...), 0o644); err != nil {
			t.Fatal(err)
		}
		f.configs[project] = path
	}
	return f
}

// build builds every target of project and returns the action keys it added
// to the shared cache.
func (f *pushFixture) build(t *testing.T, project string) []cas.ActionKey {
	t.Helper()
	before := f.localKeys(t)
	if code := captureBuildCode(t, func() int { return runBuild([]string{"--config", f.configs[project], "//..."}) }); code != exitOK {
		t.Fatalf("build %s: exit %d", project, code)
	}
	var added []cas.ActionKey
	for _, k := range f.localKeys(t) {
		if !containsKey(before, k) {
			added = append(added, k)
		}
	}
	if len(added) == 0 {
		t.Fatalf("build %s recorded no results", project)
	}
	return added
}

func (f *pushFixture) local(t *testing.T) *cas.DiskStore {
	t.Helper()
	s, err := cas.NewDiskStore(f.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *pushFixture) localKeys(t *testing.T) []cas.ActionKey {
	t.Helper()
	var keys []cas.ActionKey
	_ = f.local(t).WalkActionResults(func(k cas.ActionKey, _ *cas.ActionResult, _ error) error {
		keys = append(keys, k)
		return nil
	})
	return keys
}

func (f *pushFixture) remoteTags(t *testing.T) []string {
	t.Helper()
	var tags []string
	for tag, err := range f.registry.Tags(context.Background(), pushRepo, "") {
		if err != nil {
			break // repository does not exist yet
		}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func (f *pushFixture) push(t *testing.T, project string, args ...string) (int, string) {
	t.Helper()
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runCachePush(append([]string{"--config", f.configs[project]}, args...))
	})
	return code, stdout + stderr
}

func containsKey(keys []cas.ActionKey, k cas.ActionKey) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func tagsFor(keys []cas.ActionKey) []string {
	var tags []string
	for _, k := range keys {
		tags = append(tags, oci.ActionTag(k))
	}
	sort.Strings(tags)
	return tags
}

func TestCachePushOnlyPushesThisProjectsResults(t *testing.T) {
	f := newPushFixture(t)
	aKeys := f.build(t, "a")
	bKeys := f.build(t, "b")

	code, out := f.push(t, "a")
	if code != exitOK {
		t.Fatalf("push: exit %d\n%s", code, out)
	}
	if got, want := f.remoteTags(t), tagsFor(aKeys); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remote tags = %v, want project a's %v", got, want)
	}
	for _, tag := range tagsFor(bKeys) {
		if strings.Contains(strings.Join(f.remoteTags(t), ","), tag) {
			t.Fatalf("another project's result %s was pushed", tag)
		}
	}
	if !strings.Contains(out, "Pushed 2, skipped 0 (already present), failed 0") {
		t.Fatalf("unexpected summary:\n%s", out)
	}

	// Pushing again uploads nothing new.
	code, out = f.push(t, "a")
	if code != exitOK || !strings.Contains(out, "Pushed 0, skipped 2 (already present), failed 0") {
		t.Fatalf("second push: exit %d\n%s", code, out)
	}

	// Selecting one target pushes only its result.
	code, out = f.push(t, "b", "//b1")
	if code != exitOK || !strings.Contains(out, "Pushed 1, skipped 0") {
		t.Fatalf("push //b1: exit %d\n%s", code, out)
	}
}

func TestCachePushContinuesPastAFailedResult(t *testing.T) {
	f := newPushFixture(t)
	aKeys := f.build(t, "a")

	// Break the first result by deleting its output blob from the local cache.
	local := f.local(t)
	broken, err := local.GetActionResult(context.Background(), aKeys[0])
	if err != nil || broken == nil {
		t.Fatalf("result: %v, %v", broken, err)
	}
	for _, d := range broken.Outputs {
		if err := local.Delete(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}

	code, out := f.push(t, "a")
	if code == exitOK {
		t.Fatalf("push succeeded despite a failed result:\n%s", out)
	}
	if !strings.Contains(out, "Pushed 1, skipped 0 (already present), failed 1 of 2") {
		t.Fatalf("unexpected summary:\n%s", out)
	}
	if got, want := f.remoteTags(t), tagsFor(aKeys[1:]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remote tags = %v, want the healthy result %v", got, want)
	}
}

func TestCachePushReportsTargetsWithoutLocalResults(t *testing.T) {
	f := newPushFixture(t)
	code, out := f.push(t, "a", "--dry-run")
	if code != exitOK {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Would push 0 action result(s)") || !strings.Contains(out, "2 planned action(s) have no local result") {
		t.Fatalf("unexpected dry-run output:\n%s", out)
	}
}
