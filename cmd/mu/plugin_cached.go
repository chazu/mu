package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/plugin"
)

var errCachedPluginMissing = errors.New("cached plugin not found")

type cachedPluginVersion struct {
	path   string
	digest cas.Digest
	bundle bool
	legacy bool
}

func cachedPluginVersions(name string) ([]cachedPluginVersion, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid plugin cache name %q", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".mu", "plugins", name)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", errCachedPluginMissing, name)
	}
	if err != nil {
		return nil, err
	}
	var versions []cachedPluginVersion
	for _, entry := range entries {
		bundle := strings.HasPrefix(entry.Name(), "bundle-") && entry.IsDir()
		single := strings.HasPrefix(entry.Name(), "plugin-") && entry.Type().IsRegular()
		if !bundle && !single {
			continue
		}
		prefix := "plugin-"
		if bundle {
			prefix = "bundle-"
		}
		hash := strings.TrimPrefix(entry.Name(), prefix)
		if single {
			hash = strings.TrimSuffix(hash, filepath.Ext(hash))
		}
		if len(hash) == 0 || len(hash) > 64 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		versions = append(versions, cachedPluginVersion{path: filepath.Join(dir, entry.Name()), digest: cas.NewSHA256(hash), bundle: bundle, legacy: len(hash) != 64})
	}
	// Ignore short legacy aliases when a full-digest publication supersedes
	// them. Old partial extraction paths must not displace complete versions.
	var out []cachedPluginVersion
	for _, v := range versions {
		superseded := false
		for _, other := range versions {
			if v.legacy && !other.legacy && v.bundle == other.bundle && strings.HasPrefix(other.digest.Hash, v.digest.Hash) {
				superseded = true
				break
			}
		}
		if !superseded {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].digest.String() == out[j].digest.String() {
			return out[i].path < out[j].path
		}
		return out[i].digest.String() < out[j].digest.String()
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errCachedPluginMissing, name)
	}
	return out, nil
}

// A single cached identity remains convenient. Multiple versions require an
// explicit full digest; directory order is never a version-selection policy.
func resolveCachedPlugin(name string, selection ...string) (plugin.PluginDef, cas.Digest, error) {
	versions, err := cachedPluginVersions(name)
	if err != nil {
		return plugin.PluginDef{}, cas.Digest{}, err
	}
	requested := ""
	if len(selection) > 0 {
		requested = selection[0]
	}
	if requested != "" {
		digest, err := cas.ParseDigest(requested)
		if err != nil || digest.Algorithm != "sha256" || len(digest.Hash) != 64 {
			return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("--digest requires a full sha256 digest")
		}
		if _, err := hex.DecodeString(digest.Hash); err != nil {
			return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("--digest requires hexadecimal SHA-256")
		}
		var matches []cachedPluginVersion
		for _, v := range versions {
			if !v.legacy && v.digest == digest {
				matches = append(matches, v)
			}
		}
		versions = matches
	}
	if len(versions) == 0 {
		return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("plugin %q has no cached version %s", name, requested)
	}
	// Same digest in multiple layouts is still one identity; prefer a complete
	// bundle over a standalone entry, never over another digest.
	if len(versions) > 1 {
		same := true
		for _, v := range versions {
			if v.digest != versions[0].digest {
				same = false
			}
		}
		if !same {
			var ids []string
			for _, v := range versions {
				ids = append(ids, v.digest.String())
			}
			return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("plugin %q has multiple cached versions (%s); select one with --digest", name, strings.Join(ids, ", "))
		}
		sort.SliceStable(versions, func(i, j int) bool { return versions[i].bundle && !versions[j].bundle })
	}
	v := versions[0]
	def := plugin.PluginDef{Name: name, Script: v.path, Toolchain: inferPluginToolchain(v.path)}
	if v.bundle {
		entry, toolchain, err := resolveBundleEntry(v.path)
		if err != nil {
			return plugin.PluginDef{}, cas.Digest{}, err
		}
		def.Script, def.Toolchain, def.WorkDir = entry, toolchain, v.path
	}
	return def, v.digest, nil
}
