package coordinator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/builtin"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/plugin"
)

// ResolvedPlugin holds a plugin definition with its CAS digest and resolved
// filesystem path (for script-based plugins).
type ResolvedPlugin struct {
	Def    plugin.PluginDef
	Digest cas.Digest // content hash of the plugin script (zero for command plugins)
}

// PluginResolver stores plugin scripts in CAS and resolves them to filesystem
// paths for execution.
type PluginResolver struct {
	Store       cas.Store
	ProjectRoot string
	CacheDir    string // base dir for extracted plugin scripts (e.g. ~/.mu/plugins)
}

// Resolve processes all plugin definitions, storing scripts in CAS and
// returning resolved plugin defs with filesystem paths.
func (r *PluginResolver) Resolve(ctx context.Context, plugins []config.PluginDef) ([]ResolvedPlugin, error) {
	resolved := make([]ResolvedPlugin, 0, len(plugins))

	for _, p := range plugins {
		rp, err := r.resolveOne(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", p.Name, err)
		}
		resolved = append(resolved, *rp)
	}

	return resolved, nil
}

func (r *PluginResolver) resolveOne(ctx context.Context, p config.PluginDef) (*ResolvedPlugin, error) {
	switch {
	case p.URL != "":
		return r.resolveRemote(ctx, p)
	case p.Script != "":
		return r.resolveLocal(ctx, p)
	case p.Digest != "":
		return r.resolveDigest(ctx, p)
	case len(p.Command) > 0:
		return &ResolvedPlugin{
			Def: plugin.PluginDef{
				Name:    p.Name,
				Command: p.Command,
			},
		}, nil
	default:
		return nil, fmt.Errorf("no command, script, url, or digest specified")
	}
}

// resolveDigest extracts a plugin script directly from CAS by its digest.
func (r *PluginResolver) resolveDigest(ctx context.Context, p config.PluginDef) (*ResolvedPlugin, error) {
	dgst, err := cas.ParseDigest(p.Digest)
	if err != nil {
		return nil, fmt.Errorf("parse digest %q: %w", p.Digest, err)
	}

	has, err := r.Store.Has(ctx, dgst)
	if err != nil {
		return nil, fmt.Errorf("check CAS for digest %s: %w", dgst, err)
	}
	if !has {
		return nil, fmt.Errorf("plugin digest %s not found in CAS", dgst)
	}

	cachedPath, err := r.extractFromCAS(ctx, p.Name, dgst)
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:   p.Name,
			Script: cachedPath,
		},
		Digest: dgst,
	}, nil
}

// resolveLocal reads a local script file, stores it in CAS, and returns a
// resolved plugin pointing to the cached copy.
func (r *PluginResolver) resolveLocal(ctx context.Context, p config.PluginDef) (*ResolvedPlugin, error) {
	scriptPath := p.Script
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(r.ProjectRoot, scriptPath)
	}

	f, err := os.Open(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("open script %s: %w", p.Script, err)
	}
	defer f.Close()

	// Store in CAS.
	dgst, err := r.Store.Put(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("store script: %w", err)
	}

	// Extract to a stable path for execution.
	cachedPath, err := r.extractFromCAS(ctx, p.Name, dgst)
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:   p.Name,
			Script: cachedPath,
		},
		Digest: dgst,
	}, nil
}

// resolveRemote fetches a remote script by URL, verifies sha256, stores in CAS.
func (r *PluginResolver) resolveRemote(ctx context.Context, p config.PluginDef) (*ResolvedPlugin, error) {
	// Check if we already have it in CAS by the declared sha256.
	expectedDigest := cas.NewSHA256(p.SHA256)
	has, err := r.Store.Has(ctx, expectedDigest)
	if err != nil {
		return nil, fmt.Errorf("check CAS: %w", err)
	}

	if !has {
		// Fetch to a temp file, verify, store in CAS.
		tmp, err := os.CreateTemp("", "mu-plugin-fetch-*")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)

		if err := builtin.ForgeFetch(ctx, p.URL, p.SHA256, tmpPath); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", p.URL, err)
		}

		f, err := os.Open(tmpPath)
		if err != nil {
			return nil, err
		}
		dgst, err := r.Store.Put(ctx, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("store fetched plugin: %w", err)
		}
		if dgst != expectedDigest {
			return nil, fmt.Errorf("digest mismatch after store: got %s, want %s", dgst, expectedDigest)
		}
	}

	cachedPath, err := r.extractFromCAS(ctx, p.Name, expectedDigest)
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:   p.Name,
			Script: cachedPath,
		},
		Digest: expectedDigest,
	}, nil
}

// extractFromCAS writes a plugin script from CAS to a stable filesystem path.
func (r *PluginResolver) extractFromCAS(ctx context.Context, name string, dgst cas.Digest) (string, error) {
	dir := filepath.Join(r.CacheDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// Use the digest hash as filename to auto-invalidate on content change.
	short := dgst.Hash
	if len(short) > 12 {
		short = short[:12]
	}
	destPath := filepath.Join(dir, "plugin-"+short+".bb")

	// Skip if already extracted.
	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil
	}

	// Clean old versions.
	entries, _ := filepath.Glob(filepath.Join(dir, "plugin-*.bb"))
	for _, old := range entries {
		if !strings.HasSuffix(old, short+".bb") {
			os.Remove(old)
		}
	}

	rc, err := r.Store.Get(ctx, dgst)
	if err != nil {
		return "", fmt.Errorf("get plugin from CAS: %w", err)
	}
	defer rc.Close()

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	return destPath, nil
}
