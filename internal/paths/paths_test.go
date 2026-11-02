package paths

import (
	"path/filepath"
	"testing"
)

func TestDefaultUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, "")
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := Under(filepath.Join(home, ".mu")); p != want {
		t.Fatalf("Default() = %+v, want %+v", p, want)
	}
}

func TestDefaultHonoursMuHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv(EnvHome, root)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := Under(root); p != want {
		t.Fatalf("Default() = %+v, want %+v", p, want)
	}
}

func TestDefaultMuHomeTildeAndRelative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, "~/state")
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "state"); p.Root != want {
		t.Fatalf("Root = %q, want %q", p.Root, want)
	}

	t.Setenv(EnvHome, "rel/mu")
	p, err = Default()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.Root) || filepath.Base(p.Root) != "mu" {
		t.Fatalf("Root = %q, want absolute path ending in rel/mu", p.Root)
	}
}
