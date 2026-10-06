package coordinator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/registryhttp"
)

type noRemoteStore struct{ cas.Store }

func (noRemoteStore) Has(context.Context, cas.Digest) (bool, error) {
	return false, fmt.Errorf("unexpected remote probe")
}
func (noRemoteStore) Get(context.Context, cas.Digest) (io.ReadCloser, error) {
	return nil, fmt.Errorf("unexpected remote fetch")
}

func TestAlreadyExtractedPluginsDoNotRequireCASOrRegistry(t *testing.T) {
	for _, bundle := range []bool{false, true} {
		cache := t.TempDir()
		dir := filepath.Join(cache, "cached")
		data := []byte("#!/bin/sh\nprintf cached\n")
		digest, err := cas.ComputeDigest(strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if bundle {
			dir = filepath.Join(dir, "bundle-"+digest.Hash)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plugin"), data, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plugin-"+digest.Hash+".sh"), data, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		resolver := &PluginResolver{Store: noRemoteStore{}, CacheDir: cache}
		plugins, err := resolver.Resolve(registryhttp.WithOffline(context.Background()), []config.PluginDef{{Name: "cached", Digest: digest.String()}})
		if err != nil || len(plugins) != 1 || plugins[0].Digest != digest || plugins[0].Def.Toolchain != "" {
			t.Fatalf("cached native dependency: %+v, %v", plugins, err)
		}
	}
}

func TestExtractedToolchainsAreVersionedAndDigestVerified(t *testing.T) {
	store := newTestStore(t)
	registry := NewToolchainRegistry(store)
	digest, err := store.Put(context.Background(), strings.NewReader("first binary"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(context.Background(), &ToolchainManifest{Name: "tool", Version: "1", Artifacts: map[string]string{"bin/tool": digest.String()}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	first, err := registry.ExtractBinary(context.Background(), "tool", "bin/tool", dir)
	if err != nil {
		t.Fatal(err)
	}
	// A complete exact-digest snapshot remains usable without the CAS blob.
	registry.store = noRemoteStore{}
	cached, err := registry.ExtractBinary(context.Background(), "tool", "bin/tool", dir)
	if err != nil || cached != first {
		t.Fatalf("cached binary required registry: %q, %v", cached, err)
	}
	if err := os.WriteFile(first, []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ExtractBinary(context.Background(), "tool", "bin/tool", dir); err == nil {
		t.Fatal("accepted wrong cached binary identity")
	}
}
