package oci

import (
	"encoding/json"
	"testing"
)

func TestPluginConfigRoundTrip(t *testing.T) {
	src := PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Guide:      "GUIDE.md",
		Digest:     "sha256:abc123",
		Source:     "https://github.com/example/mu-plugins",
	}
	b, err := json.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PluginConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Compare field-by-field since slices aren't comparable with ==.
	if got.Name != src.Name || got.Entrypoint != src.Entrypoint ||
		got.Toolchain != src.Toolchain || got.Guide != src.Guide ||
		got.Digest != src.Digest || got.Source != src.Source ||
		len(got.Files) != len(src.Files) || got.Files[0] != src.Files[0] {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", got, src)
	}
}

func TestPluginIndexRoundTrip(t *testing.T) {
	src := PluginIndex{
		SchemaVersion: 1,
		Plugins:       []string{"fmt", "lint", "test"},
	}
	b, err := json.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PluginIndex
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SchemaVersion != 1 || len(got.Plugins) != 3 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestPluginTagShortensDigest(t *testing.T) {
	// 64-char sha256 hex → "sha256-" + first 12 chars.
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	want := "sha256-0123456789ab"
	if got := PluginTag(full); got != want {
		t.Fatalf("PluginTag(%q) = %q, want %q", full, got, want)
	}
	// Short input passes through with prefix.
	if got := PluginTag("abc"); got != "sha256-abc" {
		t.Fatalf("PluginTag(short) = %q, want sha256-abc", got)
	}
}
