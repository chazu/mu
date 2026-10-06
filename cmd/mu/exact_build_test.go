package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectedPlanRejectsStatefulPlannerChangeBeforeExecution(t *testing.T) {
	root := exactBuildFixture(t, true)
	marker := filepath.Join(root, "executed")
	plan := captureBuildStdout(t, func() int { return runBuild([]string{"--plan", "--json", "//app"}) })
	var identity struct {
		PlanSHA256 string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan, &identity); err != nil {
		t.Fatalf("decode first plan: %v\n%s", err, plan)
	}
	if code := runBuild([]string{"--expect-plan-sha256", identity.PlanSHA256, "--emit-manifest", "//app"}); code != exitFail {
		t.Fatalf("changed expected build exit = %d, want %d", code, exitFail)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("changed planner action executed; stat error = %v", err)
	}
}

func TestExpectedPlanExecutesMatchingGraphAfterOneInProcessPlan(t *testing.T) {
	root := exactBuildFixture(t, false)
	marker := filepath.Join(root, "executed")
	plan := captureBuildStdout(t, func() int { return runBuild([]string{"--plan", "--json", "//app"}) })
	var identity struct {
		PlanSHA256 string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan, &identity); err != nil {
		t.Fatal(err)
	}
	if code := captureBuildCode(t, func() int {
		return runBuild([]string{"--expect-plan-sha256", identity.PlanSHA256, "--emit-manifest", "//app"})
	}); code != exitOK {
		t.Fatalf("matching expected build exit = %d, want %d", code, exitOK)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("matching guarded action did not execute: %v", err)
	}
	count, err := os.ReadFile(filepath.Join(root, "plan-count"))
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "2\n" {
		t.Fatalf("planner call count = %q, want one preview plus one guarded-build plan", count)
	}
}

func TestExpectedPlanRejectsChangedPluginContentWithSameActions(t *testing.T) {
	root := exactBuildFixture(t, false)
	marker := filepath.Join(root, "executed")
	plan := captureBuildStdout(t, func() int { return runBuild([]string{"--plan", "--json", "//app"}) })
	var identity struct {
		PlanSHA256 string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan, &identity); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, "plugin.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, append(script, []byte("\n# changed plugin content\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runBuild([]string{"--expect-plan-sha256", identity.PlanSHA256, "--emit-manifest", "//app"}); code != exitFail {
		t.Fatalf("changed-plugin expected build exit = %d, want %d", code, exitFail)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("action with changed plugin identity executed; stat error = %v", err)
	}
}

func exactBuildFixture(t *testing.T, change bool) string {
	t.Helper()
	root := t.TempDir()
	countPath := filepath.Join(root, "plan-count")
	marker := filepath.Join(root, "executed")
	changedCommand := fmt.Sprintf(`["touch",%q]`, marker)
	if !change {
		changedCommand = fmt.Sprintf(`["touch",%q]`, marker)
	}
	firstCommand := changedCommand
	if change {
		firstCommand = `["true"]`
	}
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"discover"'*)
      echo '{"name":"stateful","version":"1.0.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["discover","plan"]}'
      ;;
    *'"method":"plan"'*)
      count=0
      if [ -f %q ]; then count=$(cat %q); fi
      count=$((count + 1))
      echo "$count" > %q
      if [ "$count" -eq 1 ]; then command='%s'; else command='%s'; fi
      printf '{"actions":[{"id":"run","command":%%s,"outputs":[],"impure":true}],"declared_outputs":{}}\n' "$command"
      ;;
  esac
done
`, countPath, countPath, countPath, firstCommand, changedCommand)
	scriptPath := filepath.Join(root, "plugin.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`package mu
plugins: [{name: "stateful", script: %q}]
targets: [{target: "//app", toolchain: "stateful", sources: [], config: {}}]
`, scriptPath)
	if err := os.WriteFile(filepath.Join(root, "mu.cue"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return root
}

func captureBuildStdout(t *testing.T, run func() int) []byte {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := run()
	_ = writer.Close()
	os.Stdout = original
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitOK {
		t.Fatalf("runBuild exit = %d, want %d; stdout=%s", code, exitOK, payload)
	}
	return payload
}

func captureBuildCode(t *testing.T, run func() int) int {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := run()
	_ = writer.Close()
	_ = reader.Close()
	os.Stdout = original
	return code
}

func TestPlanOrderHelper(t *testing.T) {
	if config := os.Getenv("MU_PLAN_ORDER_CONFIG"); config != "" {
		os.Exit(runBuild([]string{"--config", config, "--plan", "--json", "//consumer"}))
	}
}

func TestImplicitPlanDigestStableAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	script := `#!/bin/sh
while IFS= read -r line; do
 case "$line" in
 *'"method":"discover"'*) echo '{"name":"order","version":"1","protocol_version":1,"capabilities":["discover","plan"]}' ;;
 *'"name":"//p/a"'*) echo '{"actions":[{"id":"run","command":["true"],"outputs":["a"]}],"declared_outputs":{"file":"a"}}' ;;
 *'"name":"//p/b"'*) echo '{"actions":[{"id":"run","command":["true"],"outputs":["b"]}],"declared_outputs":{"file":"b"}}' ;;
 *'"name":"//p/c"'*) echo '{"actions":[{"id":"run","command":["true"],"outputs":["c"]}],"declared_outputs":{"file":"c"}}' ;;
 *'"name":"//p/d"'*) echo '{"actions":[{"id":"run","command":["true"],"outputs":["d"]}],"declared_outputs":{"file":"d"}}' ;;
 *) echo '{"actions":[{"id":"run","command":["true"],"inputs":{"a":"a","b":"b","c":"c","d":"d"}}],"declared_outputs":{}}' ;;
 esac
done
`
	pluginPath := filepath.Join(root, "plugin.sh")
	if err := os.WriteFile(pluginPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`package mu
plugins: [{name:"order",script:%q}]
targets: [
 {target:"//p/a",toolchain:"order"},
 {target:"//p/b",toolchain:"order"},
 {target:"//p/c",toolchain:"order"},
 {target:"//p/d",toolchain:"order"},
 {target:"//consumer",toolchain:"order",deps:["//p/a","//p/b","//p/c","//p/d"]},
]
`, pluginPath)
	configPath := filepath.Join(root, "mu.cue")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < 12; i++ {
		child := exec.Command(os.Args[0], "-test.run=^TestPlanOrderHelper$")
		child.Env = append(os.Environ(), "MU_PLAN_ORDER_CONFIG="+configPath)
		payload, err := child.Output()
		if err != nil {
			t.Fatalf("child plan: %v, %s", err, payload)
		}
		var doc struct {
			Digest string `json:"plan_sha256"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			t.Fatalf("decode plan: %v, %s", err, payload)
		}
		if len(strings.TrimSpace(doc.Digest)) != 64 {
			t.Fatalf("invalid plan digest: %q", doc.Digest)
		}
		if i == 0 {
			first = doc.Digest
		}
		if doc.Digest != first {
			t.Fatalf("implicit-edge plan changed across processes: %s != %s", doc.Digest, first)
		}
	}
}
