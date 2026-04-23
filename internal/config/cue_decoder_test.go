package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCueDecoder_ValidProjectConfig loads testdata/mu.cue and compares
// against the jsonDecoder's output from the paired testdata/mu.json.
// The fixture is intentionally shaped to avoid numeric drift in
// Target.Config (only strings and bools) so deep-equals is meaningful.
func TestCueDecoder_ValidProjectConfig(t *testing.T) {
	dir := "testdata"

	cueCfg, err := cueDecoder{}.Decode(dir)
	if err != nil {
		t.Fatalf("cueDecoder.Decode: %v", err)
	}
	jsonCfg, err := jsonDecoder{}.Decode(dir)
	if err != nil {
		t.Fatalf("jsonDecoder.Decode: %v", err)
	}

	// jsonDecoder preserves declaration order. Sort its targets the same
	// way cueDecoder does so deep-equals is valid.
	sortTargetsByName(jsonCfg.Targets)
	// jsonDecoder populates PluginDirs via the subdir walk; cueDecoder
	// does not (out of scope for this story). Clear it so the comparison
	// focuses on what cueDecoder is responsible for.
	jsonCfg.PluginDirs = nil

	// jsonDecoder runs expandSourceGlobs, which rewrites an empty
	// Sources []string{} to nil as a side effect of its accumulator
	// pattern. The cueDecoder does not expand globs (out of scope for
	// this story), so align empty-slice shape before deep-equals.
	normalizeEmptySlices := func(ts []Target) {
		for i := range ts {
			if len(ts[i].Sources) == 0 {
				ts[i].Sources = nil
			}
		}
	}
	normalizeEmptySlices(cueCfg.Targets)
	normalizeEmptySlices(jsonCfg.Targets)

	if !reflect.DeepEqual(cueCfg, jsonCfg) {
		t.Fatalf("cueDecoder output differs from jsonDecoder:\ncue:  %#v\njson: %#v", cueCfg, jsonCfg)
	}
}

// TestCueDecoder_TargetsSorted asserts that CUE's unordered struct
// fields are normalized to sorted order on the Target slice. The fixture
// declares targets in mixed order (//cmd/mu, //lint, //lint/go-vet);
// the decoder must yield them in lex order regardless.
func TestCueDecoder_TargetsSorted(t *testing.T) {
	cfg, err := cueDecoder{}.Decode("testdata")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []string{"//cmd/mu", "//lint", "//lint/go-vet"}
	if len(cfg.Targets) != len(want) {
		t.Fatalf("expected %d targets, got %d", len(want), len(cfg.Targets))
	}
	for i, n := range want {
		if cfg.Targets[i].Name != n {
			t.Errorf("targets[%d].Name = %q, want %q", i, cfg.Targets[i].Name, n)
		}
	}
}

// TestCueDecoder_ValidPluginConfig exercises DecodePlugin on a fixture
// with a top-level "plugin" field.
func TestCueDecoder_ValidPluginConfig(t *testing.T) {
	cfg, err := cueDecoder{}.DecodePlugin("testdata/plugin")
	if err != nil {
		t.Fatalf("DecodePlugin: %v", err)
	}
	if cfg.Plugin == nil {
		t.Fatal("Plugin is nil")
	}
	if cfg.Plugin.Entrypoint != "plugin.bb" {
		t.Errorf("Entrypoint = %q, want plugin.bb", cfg.Plugin.Entrypoint)
	}
	if cfg.Plugin.Toolchain != "bb" {
		t.Errorf("Toolchain = %q, want bb", cfg.Plugin.Toolchain)
	}
	if got, want := len(cfg.Targets), 1; got != want {
		t.Fatalf("targets = %d, want %d", got, want)
	}
	if cfg.Targets[0].Name != "build" {
		t.Errorf("target[0].Name = %q, want build", cfg.Targets[0].Name)
	}
}

// TestCueDecoder_DecodePluginRejectsNoPluginKey ensures the plugin
// decoder refuses a file that lacks the "plugin" field. We reuse the
// root-config fixture here since it is valid #ProjectConfig but not
// valid #PluginConfig.
func TestCueDecoder_DecodePluginRejectsNoPluginKey(t *testing.T) {
	_, err := cueDecoder{}.DecodePlugin("testdata")
	if err == nil {
		t.Fatal("expected error decoding non-plugin dir as plugin, got nil")
	}
}

// TestCueDecoder_RejectsInvalid exercises a few schema-violation cases.
// Each fixture is a directory under testdata/ containing a single
// mu.cue that violates #ProjectConfig.
func TestCueDecoder_RejectsInvalid(t *testing.T) {
	cases := []struct {
		dir     string
		wantSub string
	}{
		{"testdata/invalid_missing_toolchain", "toolchain"},
		{"testdata/invalid_bad_target_name", "target"},
	}
	for _, tc := range cases {
		t.Run(filepath.Base(tc.dir), func(t *testing.T) {
			_, err := cueDecoder{}.Decode(tc.dir)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestCueDecoder_NumberRoundTrip asserts that integer and float literals
// in Target.Config survive decode, and documents the int64/float64
// drift vs the JSON decoder.
func TestCueDecoder_NumberRoundTrip(t *testing.T) {
	cfg, err := cueDecoder{}.Decode("testdata/numbers")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(cfg.Targets))
	}
	cm := cfg.Targets[0].Config

	// CUE decodes integer literals to int64 (not float64 as JSON does).
	// This is the documented drift; ToInt64 / ToFloat64 helpers in
	// numconv.go smooth it out at read sites.
	iv, ok := cm["int_val"]
	if !ok {
		t.Fatal("int_val missing")
	}
	if _, isInt64 := iv.(int64); !isInt64 {
		t.Errorf("int_val type = %T, want int64 (CUE decodes ints as int64)", iv)
	}
	if n, err := ToInt64(iv); err != nil || n != 42 {
		t.Errorf("ToInt64(int_val) = (%d, %v), want (42, nil)", n, err)
	}

	fv, ok := cm["float_val"]
	if !ok {
		t.Fatal("float_val missing")
	}
	if _, isFloat := fv.(float64); !isFloat {
		t.Errorf("float_val type = %T, want float64", fv)
	}
	if f, err := ToFloat64(fv); err != nil || f != 3.5 {
		t.Errorf("ToFloat64(float_val) = (%v, %v), want (3.5, nil)", f, err)
	}

	if cm["str_val"] != "hi" {
		t.Errorf("str_val = %v, want hi", cm["str_val"])
	}
	if cm["bool_val"] != true {
		t.Errorf("bool_val = %v, want true", cm["bool_val"])
	}
}

// TestCueDecoder_TriStateBoolPreserved asserts that *bool fields
// distinguish "absent" (nil) from "present with value false". This is
// the semantics the per-layer Read/Write gating relies on.
func TestCueDecoder_TriStateBoolPreserved(t *testing.T) {
	cfg, err := cueDecoder{}.Decode("testdata/tristate")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Cache == nil || len(cfg.Cache.Backends) != 3 {
		t.Fatalf("expected 3 backends, got %#v", cfg.Cache)
	}
	b0, b1, b2 := cfg.Cache.Backends[0], cfg.Cache.Backends[1], cfg.Cache.Backends[2]

	if b0.Read != nil {
		t.Errorf("b0.Read = %v, want nil", *b0.Read)
	}
	if b0.Write != nil {
		t.Errorf("b0.Write = %v, want nil", *b0.Write)
	}

	if b1.Read != nil {
		t.Errorf("b1.Read = %v, want nil", *b1.Read)
	}
	if b1.Write == nil || *b1.Write != false {
		t.Errorf("b1.Write = %v, want *false", b1.Write)
	}

	if b2.Read == nil || *b2.Read != true {
		t.Errorf("b2.Read = %v, want *true", b2.Read)
	}
	if b2.Write == nil || *b2.Write != false {
		t.Errorf("b2.Write = %v, want *false", b2.Write)
	}
}

// TestIsCuePluginDir exercises the probe helper that parallels
// IsPluginDir for the JSON decoder.
func TestIsCuePluginDir(t *testing.T) {
	pluginData, err := os.ReadFile("testdata/plugin/mu.cue")
	if err != nil {
		t.Fatalf("read plugin fixture: %v", err)
	}
	if !IsCuePluginDir(pluginData) {
		t.Error("IsCuePluginDir(plugin fixture) = false, want true")
	}

	rootData, err := os.ReadFile("testdata/mu.cue")
	if err != nil {
		t.Fatalf("read root fixture: %v", err)
	}
	if IsCuePluginDir(rootData) {
		t.Error("IsCuePluginDir(root fixture) = true, want false")
	}

	if IsCuePluginDir([]byte("this is not cue @@@@ !!!")) {
		t.Error("IsCuePluginDir(garbage) = true, want false")
	}
}

// TestCueDecoder_MissingFile ensures a missing mu.cue produces a clear
// error rather than a panic or silent success.
func TestCueDecoder_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	_, err := cueDecoder{}.Decode(tmp)
	if err == nil {
		t.Fatal("expected error for missing mu.cue, got nil")
	}
	if !strings.Contains(err.Error(), "mu.cue") {
		t.Errorf("error %q does not mention mu.cue", err.Error())
	}
}
