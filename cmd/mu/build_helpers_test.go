package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// captureBuildStdout runs a command function with os.Stdout redirected,
// fails the test unless it exits 0, and returns what it wrote to stdout.
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

// captureBuildCode runs a command function with os.Stdout discarded and
// returns its exit code.
func captureBuildCode(t *testing.T, run func() int) int {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, reader); close(done) }()
	code := run()
	_ = writer.Close()
	<-done
	_ = reader.Close()
	os.Stdout = original
	return code
}

func TestPlanOrderHelper(t *testing.T) {
	if config := os.Getenv("MU_PLAN_ORDER_CONFIG"); config != "" {
		os.Exit(runBuild([]string{"--config", config, "--plan", "--json", "//consumer"}))
	}
}

// TestPlanJSONStableAcrossProcesses guards against implicit dependency edges
// (derived from map iteration) leaking nondeterministic order into the plan.
func TestPlanJSONStableAcrossProcesses(t *testing.T) {
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
	var first []byte
	for i := 0; i < 12; i++ {
		child := exec.Command(os.Args[0], "-test.run=^TestPlanOrderHelper$")
		child.Env = append(os.Environ(), "MU_PLAN_ORDER_CONFIG="+configPath)
		payload, err := child.Output()
		if err != nil {
			t.Fatalf("child plan: %v, %s", err, payload)
		}
		if !bytes.Contains(payload, []byte(`"actions"`)) {
			t.Fatalf("child did not emit a JSON plan: %s", payload)
		}
		if i == 0 {
			first = payload
			continue
		}
		if !bytes.Equal(payload, first) {
			t.Fatalf("plan changed across processes:\n%s\n---\n%s", payload, first)
		}
	}
}
