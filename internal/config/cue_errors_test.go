package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files under testdata/errors/")

// TestFormatCueError_Golden feeds each fixture under testdata/errors/
// through cueDecoder.Decode and compares the resulting error string to
// the companion .golden file. Run `go test ./internal/config/ -update`
// to regenerate goldens after an intentional formatting change.
func TestFormatCueError_Golden(t *testing.T) {
	cases := []string{
		"type_mismatch",
		"missing_required",
		"regex_mismatch",
		"closed_struct",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", "errors", name)
			_, err := cueDecoder{}.Decode(dir)
			if err == nil {
				t.Fatalf("expected error from fixture %s, got nil", name)
			}
			got := err.Error()
			goldenPath := filepath.Join(dir, "error.golden")
			if *updateGolden {
				if werr := os.WriteFile(goldenPath, []byte(got), 0o644); werr != nil {
					t.Fatalf("write golden: %v", werr)
				}
				return
			}
			wantBytes, rerr := os.ReadFile(goldenPath)
			if rerr != nil {
				t.Fatalf("read golden %s: %v (run with -update to create it)", goldenPath, rerr)
			}
			want := string(wantBytes)
			if got != want {
				t.Fatalf("formatted error mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
			}
		})
	}
}

// TestFormatCueError_ContainsTargetName spot-checks that target-name
// substitution fires on a real fixture (the decoder path is where
// formatCueError is wired in).
func TestFormatCueError_ContainsTargetName(t *testing.T) {
	_, err := cueDecoder{}.Decode("testdata/errors/type_mismatch")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !contains(msg, "//foo/bar") {
		t.Errorf("expected error to mention target name //foo/bar, got:\n%s", msg)
	}
	if !contains(msg, "mu.cue:") {
		t.Errorf("expected error to include file:line:col, got:\n%s", msg)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
