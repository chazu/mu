package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalCueRoot returns a minimal valid #ProjectConfig mu.cue body that
// satisfies schema.cue and yields the given target name.
func minimalCueRoot(targetName string) string {
	return `toolchains: [
  {toolchain: "go", from: "scratch"},
]
targets: [
  {target: "` + targetName + `", toolchain: "go", sources: ["main.go"]},
]
`
}

func TestFindProjectRoot_Direct(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mu.cue"), minimalCueRoot("//app"))

	root, err := FindProjectRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root != dir {
		t.Fatalf("expected %s, got %s", dir, root)
	}
}

func TestFindProjectRoot_WalkUp(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))

	child := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := FindProjectRoot(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != root {
		t.Fatalf("expected %s, got %s", root, found)
	}
}

func TestFindProjectRoot_NotFound(t *testing.T) {
	dir := t.TempDir() // no mu.cue here
	_, err := FindProjectRoot(dir)
	if err == nil {
		t.Fatal("expected error when mu.cue is absent")
	}
}

// targetCue returns a CUE struct literal for one target with the given
// fields. Sources are rendered as a CUE list.
func targetCue(name, toolchain string, sources []string) string {
	return fmt.Sprintf(`{target: %q, toolchain: %q, sources: %s}`, name, toolchain, cueStringList(sources))
}

func cueStringList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%q", x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func TestLoad_Basic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
plugins: [{name: "go", command: ["mu-plugin-go"]}]
targets: [{target: "//app", toolchain: "go", sources: ["*.go"]}]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "//app" {
		t.Fatalf("expected target name //app, got %s", cfg.Targets[0].Name)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(cfg.Plugins))
	}
}

func TestLoad_SealedOutputModes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), `
plugins: [{name: "fake", command: ["fake-plugin"]}]
targets: [{
  target: "//app"
  toolchain: "fake"
  sealed_outputs: {TOKEN: "fake:apps/token"}
  sealed_output_modes: {TOKEN: "create_if_absent"}
}]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(cfg.Targets))
	}
	if got := cfg.Targets[0].SealedOutputModes["TOKEN"]; got != "create_if_absent" {
		t.Fatalf("sealed_output_modes TOKEN = %q, want create_if_absent", got)
	}
}

func TestLoad_MergeSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	sub := filepath.Join(root, "pkg", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "mu.cue"), `
targets: [{target: "server", toolchain: "go", sources: ["*.go"]}]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(cfg.Targets))
	}

	var found bool
	for _, tgt := range cfg.Targets {
		if tgt.Name == "//pkg/web/server" {
			found = true
		}
	}
	if !found {
		names := make([]string, len(cfg.Targets))
		for i, tgt := range cfg.Targets {
			names[i] = tgt.Name
		}
		t.Fatalf("expected target //pkg/web/server, got targets: %v", names)
	}
}

func TestLoad_MultipleSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	for _, pkg := range []string{"alpha", "beta", "gamma"} {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "mu.cue"), `
targets: [{target: "lib", toolchain: "go", sources: ["*.go"]}]
`)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Root target + 3 subdir targets.
	if len(cfg.Targets) != 4 {
		t.Fatalf("expected 4 targets, got %d", len(cfg.Targets))
	}
}

func TestLoad_AlreadyPrefixedTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "mu.cue"), `
targets: [
  {target: "//pkg/name", toolchain: "go", sources: ["*.go"]},
  {target: "//pkg:colon", toolchain: "go", sources: ["*.go"]},
  {target: "//pkg", toolchain: "go", sources: ["*.go"]},
]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := map[string]bool{}
	for _, tgt := range cfg.Targets {
		found[tgt.Name] = true
	}
	if !found["//pkg/name"] || !found["//pkg:colon"] || !found["//pkg"] {
		t.Fatalf("already-prefixed targets should not be modified, targets=%+v", cfg.Targets)
	}
}

func TestLoad_SubconfigTargetOutsideDirRejected(t *testing.T) {
	for _, name := range []string{"//explicit/name", "//pkgx/name", "//other:x"} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
		writeFile(t, filepath.Join(root, "pkg", "mu.cue"),
			`targets: [{target: "`+name+`", toolchain: "go", sources: ["*.go"]}]`)
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), "outside this directory") ||
			!strings.Contains(err.Error(), filepath.Join(root, "pkg", "mu.cue")) {
			t.Fatalf("%s: Load error = %v, want outside-directory error naming pkg/mu.cue", name, err)
		}
	}
}

func TestLoad_SubconfigCommandRewrite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
	writeFile(t, filepath.Join(root, "sub", "mu.cue"), `
plugins: [
  {name: "dot", command: ["./bin/plugin", "./arg", "a/b"]},
  {name: "up", command: ["../tool", "../x"]},
  {name: "shell", command: ["sh", "-c", "exec curl https://example.com/x | jq .a/b"]},
  {name: "bare", command: ["bin/plugin"]},
]
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := map[string][]string{}
	for _, p := range cfg.Plugins {
		got[p.Name] = p.Command
	}
	want := map[string][]string{
		"dot":   {filepath.Join("sub", "bin", "plugin"), "./arg", "a/b"},
		"up":    {"." + string(filepath.Separator) + "tool", "../x"},
		"shell": {"sh", "-c", "exec curl https://example.com/x | jq .a/b"},
		"bare":  {"bin/plugin"},
	}
	for name, w := range want {
		if strings.Join(got[name], "\x00") != strings.Join(w, "\x00") {
			t.Errorf("plugin %s command = %q, want %q", name, got[name], w)
		}
	}
}

func TestLoad_SubconfigRootOnlyFieldsRejected(t *testing.T) {
	for _, field := range []string{
		`secrets: {writable_refs: []}`,
		`cache: {}`,
		`publish: {repository: "ghcr.io/o/r"}`,
	} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
		sub := filepath.Join(root, "sub", "mu.cue")
		writeFile(t, sub, field+"\n")
		name := field[:strings.Index(field, ":")]
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), sub) ||
			!strings.Contains(err.Error(), `"`+name+`" may only be set in the project root`) {
			t.Fatalf("%s: Load error = %v, want root-only error naming %s", name, err, sub)
		}
	}
}

func TestLoad_DuplicateNamesAcrossConfigsRejected(t *testing.T) {
	cases := []struct {
		kind, root, sub, other string
	}{
		{"toolchain", minimalCueRoot("//root"), `toolchains: [{toolchain: "go", from: "scratch"}]`, ""},
		{"plugin", minimalCueRoot("//root") + `plugins: [{name: "p", command: ["p"]}]` + "\n",
			`plugins: [{name: "p", command: ["q"]}]`, ""},
		{"plugin", minimalCueRoot("//root"),
			`plugins: [{name: "p", command: ["q"]}]`, `plugins: [{name: "p", command: ["r"]}]`},
	}
	for _, tc := range cases {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "mu.cue"), tc.root)
		first := filepath.Join(root, "mu.cue")
		second := filepath.Join(root, "sub", "mu.cue")
		writeFile(t, second, tc.sub+"\n")
		if tc.other != "" {
			first = second
			second = filepath.Join(root, "zz", "mu.cue")
			writeFile(t, second, tc.other+"\n")
		}
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), tc.kind+" ") ||
			!strings.Contains(err.Error(), first+" and "+second) {
			t.Fatalf("%s: Load error = %v, want duplicate error naming %s and %s", tc.kind, err, first, second)
		}
	}
}

func TestLoad_SkipsNestedModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
	// A separate project: its own cue.mod, absolute target names and a
	// redeclared toolchain would all be errors as a sub-config.
	writeFile(t, filepath.Join(root, "examples", "demo", "cue.mod", "module.cue"),
		`module: "example.com/demo"
language: version: "v0.11.0"
`)
	writeFile(t, filepath.Join(root, "examples", "demo", "mu.cue"), `package mu
`+minimalCueRoot("//demo"))

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "//root" {
		t.Fatalf("targets = %+v, want only //root", cfg.Targets)
	}
	demo, err := Load(filepath.Join(root, "examples", "demo"))
	if err != nil {
		t.Fatalf("Load nested project: %v", err)
	}
	if len(demo.Targets) != 1 || demo.Targets[0].Name != "//demo" {
		t.Fatalf("nested targets = %+v, want only //demo", demo.Targets)
	}
}

func TestLoad_SkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(real, "mu.cue"), `
targets: [{target: "lib", toolchain: "go", sources: ["*.go"]}]
`)

	symDir := filepath.Join(root, "linked")
	if err := os.Symlink(real, symDir); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Root target + lib in real (symlinked dir is skipped).
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets (symlinked dir should be skipped), got %d", len(cfg.Targets))
	}
}

func TestLoad_SkipsSymlinkedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "mu.cue"), `
targets: [{target: "injected", toolchain: "go", sources: ["*.go"]}]
`)

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "mu.cue"), filepath.Join(sub, "mu.cue")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target (symlinked file should be skipped), got %d", len(cfg.Targets))
	}
}

func TestLoad_SkipsHiddenAndTestdata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	bad := `targets: [{target: "!not-a-label!"}]`
	writeFile(t, filepath.Join(root, ".hidden", "mu.cue"), bad)
	writeFile(t, filepath.Join(root, "testdata", "mu.cue"), bad)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v — walker likely descended into hidden/testdata", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target (root only), got %d", len(cfg.Targets))
	}
}

func TestLoad_RejectsRemovedPreprocessor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mu.cue"), minimalCueRoot("//app")+`
preprocessor: {extension: "json", command: ["cat"]}
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "preprocessor was removed") {
		t.Fatalf("Load error = %v, want \"preprocessor was removed\"", err)
	}
}

func TestLoad_RejectsRemovedAdvice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mu.cue"), minimalCueRoot("//app")+`
advice: [{plugin: "void", phases: ["after-build"]}]
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "advice was removed") {
		t.Fatalf("Load error = %v, want \"advice was removed\"", err)
	}
}

func TestLoad_RejectsRemovedKindImplements(t *testing.T) {
	for _, field := range []string{`kind: "kit"`, `implements: "//iface"`} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
		writeFile(t, filepath.Join(root, "sub", "mu.cue"),
			`targets: [{target: "x", toolchain: "shell", sources: [], `+field+`}]`)
		_, err := Load(root)
		if err == nil || !strings.Contains(err.Error(), "kind/implements were removed") {
			t.Fatalf("%s: Load error = %v, want \"kind/implements were removed\"", field, err)
		}
	}
}

func TestLoad_RejectsRemovedSealedRouting(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))
	writeFile(t, filepath.Join(root, "sub", "mu.cue"),
		`targets: [{target: "x", toolchain: "shell", sources: [], sealed_routing: "strict"}]`)
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "sealed_routing was removed; strict routing is always on") {
		t.Fatalf("Load error = %v, want \"sealed_routing was removed\"", err)
	}
}
