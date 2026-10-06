package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCachedPluginSelectionRequiresExplicitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	name := "choice"
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, hash := range []string{a, b} {
		dir := filepath.Join(home, ".mu", "plugins", name, "bundle-"+hash)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mu-plugin.json"), []byte(`{"entrypoint":"plugin.sh"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "GUIDE.md"), []byte(hash), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := resolveCachedPlugin(name); err == nil || !strings.Contains(err.Error(), "--digest") {
		t.Fatalf("ambiguous selection: %v", err)
	}
	def, digest, err := resolveCachedPlugin(name, "sha256:"+a)
	if err != nil || digest.Hash != a || !strings.HasSuffix(def.WorkDir, "bundle-"+a) {
		t.Fatalf("explicit selection: %+v, %v, %v", def, digest, err)
	}
	versions, err := cachedPluginVersions(name)
	if err != nil || len(versions) != 2 {
		t.Fatalf("listing hid cached versions: %+v, %v", versions, err)
	}
	if code := printGuideForPlugin(name); code != 1 {
		t.Fatalf("ambiguous guide selected a version: %d", code)
	}
	if code := printGuideForPlugin(name, "sha256:"+a); code != 0 {
		t.Fatalf("explicit guide failed: %d", code)
	}
	if _, _, err := resolveCachedPlugin(name, "sha256:"+a[:12]); err == nil {
		t.Fatal("accepted abbreviated explicit identity")
	}
	if _, _, err := resolveCachedPlugin("../outside"); err == nil {
		t.Fatal("accepted traversal name")
	}
}

func TestFullCachePublicationSupersedesLegacyPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hash := strings.Repeat("a", 64)
	dir := filepath.Join(home, ".mu", "plugins", "single")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{hash[:12], hash} {
		if err := os.WriteFile(filepath.Join(dir, "plugin-"+suffix+".sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	def, digest, err := resolveCachedPlugin("single")
	if err != nil || digest.Hash != hash || !strings.Contains(def.Script, hash) {
		t.Fatalf("legacy version displaced complete publication: %+v, %s, %v", def, digest, err)
	}
}
