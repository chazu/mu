package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestJSONDecoder_DecodeMatchesLoad verifies that jsonDecoder.Decode,
// reached through the Decoder interface, produces the same ProjectConfig as
// the public Load entry point. This is the drop-in-equivalence guarantee
// that future Decoder implementations (e.g. CUE) can be checked against.
func TestJSONDecoder_DecodeMatchesLoad(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{
		Targets: []Target{
			{Name: "hello", Toolchain: "cowsay", Sources: []string{"hello.txt"}},
		},
		Toolchains: []Toolchain{
			{Name: "cowsay", From: "alpine"},
		},
	})

	viaLoad, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var dec Decoder = jsonDecoder{}
	viaDecoder, err := dec.Decode(root)
	if err != nil {
		t.Fatalf("jsonDecoder.Decode: %v", err)
	}

	if !reflect.DeepEqual(viaLoad, viaDecoder) {
		t.Fatalf("jsonDecoder.Decode diverged from Load:\nLoad    = %#v\nDecoder = %#v", viaLoad, viaDecoder)
	}
}

// TestJSONDecoder_DecodePluginMatchesLoad verifies the same equivalence for
// plugin manifests.
func TestJSONDecoder_DecodePluginMatchesLoad(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "mu.json"), PluginConfig{
		Plugin: &PluginManifest{Entrypoint: "plugin.bb"},
	})

	viaLoad, err := LoadPluginManifest(dir)
	if err != nil {
		t.Fatalf("LoadPluginManifest: %v", err)
	}

	var dec Decoder = jsonDecoder{}
	viaDecoder, err := dec.DecodePlugin(dir)
	if err != nil {
		t.Fatalf("jsonDecoder.DecodePlugin: %v", err)
	}

	if !reflect.DeepEqual(viaLoad, viaDecoder) {
		t.Fatalf("jsonDecoder.DecodePlugin diverged from LoadPluginManifest:\nLoad    = %#v\nDecoder = %#v", viaLoad, viaDecoder)
	}
}

// TestDefaultDecoderIsJSON asserts that Load's dispatch target is currently
// the JSON decoder. When a new default is wired in, this test should be
// updated deliberately.
func TestDefaultDecoderIsJSON(t *testing.T) {
	if _, ok := defaultDecoder().(jsonDecoder); !ok {
		t.Fatalf("defaultDecoder() = %T, want jsonDecoder", defaultDecoder())
	}
}
