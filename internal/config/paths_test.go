package config

import (
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/paths"
)

// cache.path in mu.cue > $MU_HOME/cache > ~/.mu/cache.
func TestResolvePathsPrecedence(t *testing.T) {
	home := t.TempDir()
	muHome := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv(paths.EnvHome, "")
	p, err := ResolvePaths(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".mu", "cache"); p.CacheDir != want {
		t.Fatalf("default CacheDir = %q, want %q", p.CacheDir, want)
	}

	t.Setenv(paths.EnvHome, muHome)
	p, err = ResolvePaths(&ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if p != paths.Under(muHome) {
		t.Fatalf("MU_HOME paths = %+v, want %+v", p, paths.Under(muHome))
	}

	explicit := filepath.Join(t.TempDir(), "c")
	p, err = ResolvePaths(&ProjectConfig{Cache: &CacheConfig{Path: explicit}})
	if err != nil {
		t.Fatal(err)
	}
	if p.CacheDir != explicit {
		t.Fatalf("cache.path CacheDir = %q, want %q", p.CacheDir, explicit)
	}
	if p.PluginsDir != filepath.Join(muHome, "plugins") {
		t.Fatalf("PluginsDir = %q, want under MU_HOME", p.PluginsDir)
	}
}
