package config

import (
	"strings"
	"testing"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
			{Name: "//lib", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_EmptyConfig(t *testing.T) {
	cfg := &ProjectConfig{}
	if err := Validate(cfg); err != nil {
		t.Fatalf("empty config should be valid, got: %v", err)
	}
}

func TestValidate_MissingTargetName(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing target name")
	}
	if !strings.Contains(err.Error(), "missing required field \"target\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingToolchain(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain")
	}
	if !strings.Contains(err.Error(), "missing required field \"toolchain\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_TargetNameMustStartWithDoubleSlash(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "app", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for target name without // prefix")
	}
	if !strings.Contains(err.Error(), "must start with \"//\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateTargetNames(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
			{Name: "//app", Toolchain: "rust", Sources: []string{"*.rs"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate target names")
	}
	if !strings.Contains(err.Error(), "duplicate target name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "", Toolchain: ""},           // two errors: missing name and toolchain
			{Name: "bad-name", Toolchain: "go"}, // one error: no // prefix
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected multiple errors")
	}
	msg := err.Error()
	// Should report at least 3 issues.
	count := strings.Count(msg, "\n")
	if count < 3 {
		t.Fatalf("expected at least 3 error lines, got %d in:\n%s", count, msg)
	}
}

// --- Toolchain validation ---

func TestValidate_MissingToolchainName(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "", From: "scratch"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain name")
	}
	if !strings.Contains(err.Error(), "missing required field \"toolchain\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingToolchainFrom(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "go", From: ""},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain from")
	}
	if !strings.Contains(err.Error(), "missing required field \"from\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateToolchainNames(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "go", From: "scratch"},
			{Name: "go", From: "scratch"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate toolchain names")
	}
	if !strings.Contains(err.Error(), "duplicate toolchain name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- Plugin validation ---

func TestValidate_MissingPluginName(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "", Command: []string{"./plugin"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing plugin name")
	}
	if !strings.Contains(err.Error(), "missing required field \"name\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingPluginSource(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
	if !strings.Contains(err.Error(), "must set one of") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_PluginMultipleSources(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Command: []string{"./a"}, Script: "plugin.bb"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for multiple sources")
	}
	if !strings.Contains(err.Error(), "only one of") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_PluginURLRequiresSHA256(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", URL: "https://example.com/plugin.bb"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for URL without SHA256")
	}
	if !strings.Contains(err.Error(), "requires \"sha256\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_PluginURLWithSHA256(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", URL: "https://example.com/plugin.bb", SHA256: strings.Repeat("ab", 32)},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_PluginScriptOnly(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Script: "plugins/go/plugin.bb"},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_DuplicatePluginNames(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Command: []string{"./a"}},
			{Name: "my-plugin", Command: []string{"./b"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate plugin names")
	}
	if !strings.Contains(err.Error(), "duplicate plugin name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- Cache validation ---

func TestValidate_CacheRemoteMustBeRepositoryReference(t *testing.T) {
	for _, remote := range []string{"oci://ghcr.io/acme/cache", "https://ghcr.io/acme/cache", "ghcr.io"} {
		err := Validate(&ProjectConfig{Cache: &CacheConfig{Remote: remote}})
		if err == nil || !strings.Contains(err.Error(), "cache.remote") {
			t.Errorf("remote %q: err = %v, want cache.remote error", remote, err)
		}
	}
}

func TestValidate_ValidCacheConfig(t *testing.T) {
	cfg := &ProjectConfig{
		Cache: &CacheConfig{
			Path:   "/tmp/cache",
			Remote: "ghcr.io/example/mu-cache",
			Push:   "ghcr.io/example/mu-cache",
		},
		Publish: &PublishConfig{Repository: "ghcr.io/example"},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid cache config, got: %v", err)
	}
}

func TestValidate_PushAndPublishMustBeRegistryReferences(t *testing.T) {
	for field, cfg := range map[string]*ProjectConfig{
		"cache.push":         {Cache: &CacheConfig{Push: "ghcr.io"}},
		"publish.repository": {Publish: &PublishConfig{Repository: "https://ghcr.io/acme"}},
	} {
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: err = %v, want %s error", field, err, field)
		}
	}
}

func TestValidate_FullValidConfig(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
		},
		Toolchains: []Toolchain{
			{Name: "go", From: "scratch"},
		},
		Plugins: []PluginDef{
			{Name: "go-plugin", Command: []string{"./plugins/go"}},
		},
		Cache: &CacheConfig{Path: "/tmp/cache"},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected full valid config to pass, got: %v", err)
	}
}

func TestValidate_PluginDigestAndSHA256MustBeFullIdentities(t *testing.T) {
	full := strings.Repeat("ab", 32)
	cases := map[string]PluginDef{
		"short digest":       {Name: "p", Digest: "sha256:abc123"},
		"digest without alg": {Name: "p", Digest: full},
		"uppercase digest":   {Name: "p", Digest: "sha256:" + strings.ToUpper(full)},
		"short sha256":       {Name: "p", URL: "https://example.com/p", SHA256: "abc123"},
		"prefixed sha256":    {Name: "p", URL: "https://example.com/p", SHA256: "sha256:" + full},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(&ProjectConfig{Plugins: []PluginDef{p}}); err == nil {
				t.Fatalf("Validate accepted %+v", p)
			}
		})
	}
	if err := Validate(&ProjectConfig{Plugins: []PluginDef{{Name: "p", Digest: "sha256:" + full}}}); err != nil {
		t.Fatalf("full digest rejected: %v", err)
	}
}
