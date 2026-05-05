package coordinator

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chau/mu/internal/builtin"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/plugin"
)

// ResolvedPlugin holds a plugin definition with its CAS digest and resolved
// filesystem path (for CAS-stored plugins).
type ResolvedPlugin struct {
	Def    plugin.PluginDef
	Digest cas.Digest // content hash of the plugin (zero for command plugins)
}

// PluginResolver stores plugin files in CAS and resolves them to filesystem
// paths for execution.
type PluginResolver struct {
	Store       cas.Store
	ProjectRoot string
	CacheDir    string // base dir for extracted plugins (e.g. ~/.mu/plugins)
}

// Resolve processes all plugin definitions, storing files in CAS and
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

// resolveDigest extracts a plugin directly from CAS by its digest.
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

	// No source hint available for digest-only plugins.
	cachedPath, err := r.extractFromCAS(ctx, p.Name, dgst, "")
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:      p.Name,
			Script:    cachedPath,
			Toolchain: inferPluginToolchain(cachedPath),
		},
		Digest: dgst,
	}, nil
}

// resolveLocal handles a local plugin path — either a single file or a
// directory containing a plugin manifest (mu.cue with a "plugin" key).
func (r *PluginResolver) resolveLocal(ctx context.Context, p config.PluginDef) (*ResolvedPlugin, error) {
	scriptPath := p.Script
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(r.ProjectRoot, scriptPath)
	}

	info, err := os.Stat(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p.Script, err)
	}

	if info.IsDir() {
		return r.resolveLocalDir(ctx, p, scriptPath)
	}
	return r.resolveLocalFile(ctx, p, scriptPath)
}

// resolveLocalFile reads a single local plugin file, stores it in CAS,
// and returns a resolved plugin pointing to the cached copy.
func (r *PluginResolver) resolveLocalFile(ctx context.Context, p config.PluginDef, scriptPath string) (*ResolvedPlugin, error) {
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

	// Extract to a stable path for execution, preserving the original extension.
	cachedPath, err := r.extractFromCAS(ctx, p.Name, dgst, p.Script)
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:      p.Name,
			Script:    cachedPath,
			Toolchain: inferPluginToolchain(cachedPath),
		},
		Digest: dgst,
	}, nil
}

// resolveLocalDir bundles a plugin directory into a deterministic tar,
// stores it in CAS, extracts the full directory, and returns a resolved
// plugin with the entrypoint and WorkDir set.
func (r *PluginResolver) resolveLocalDir(ctx context.Context, p config.PluginDef, dirPath string) (*ResolvedPlugin, error) {
	// 1. Load plugin manifest.
	manifest, err := config.LoadPluginManifest(dirPath)
	if err != nil {
		return nil, err
	}

	// 2. Verify entrypoint exists.
	entryRel := manifest.Plugin.Entrypoint
	entryAbs := filepath.Join(dirPath, entryRel)
	if _, err := os.Stat(entryAbs); err != nil {
		return nil, fmt.Errorf("entrypoint %q not found in plugin directory: %w", entryRel, err)
	}

	// 3. Bundle the directory as a deterministic tar and store in CAS.
	//    Always include the guide file if declared, even if files is explicit.
	files := manifest.Plugin.Files
	if manifest.Plugin.Guide != "" {
		guideAbs := filepath.Join(dirPath, manifest.Plugin.Guide)
		if _, err := os.Stat(guideAbs); err == nil {
			found := false
			for _, f := range files {
				if f == manifest.Plugin.Guide {
					found = true
					break
				}
			}
			if !found && len(files) > 0 {
				files = append(files, manifest.Plugin.Guide)
			}
		}
	}
	// Expand any declared schema directories into individual file entries
	// so they ride along in the deterministic bundle. Only meaningful when
	// an explicit Files list is set; otherwise everything under the plugin
	// dir is included already.
	if len(files) > 0 && len(manifest.Plugin.Schemas) > 0 {
		seen := make(map[string]bool, len(files))
		for _, f := range files {
			seen[f] = true
		}
		for _, decl := range manifest.Plugin.Schemas {
			schemaFiles, err := walkSchemaDir(dirPath, decl.Path)
			if err != nil {
				return nil, fmt.Errorf("plugin schema %s@%s: %w", decl.Module, decl.Version, err)
			}
			for _, sf := range schemaFiles {
				if !seen[sf] {
					files = append(files, sf)
					seen[sf] = true
				}
			}
		}
	}
	dgst, err := r.bundleDir(ctx, dirPath, files)
	if err != nil {
		return nil, fmt.Errorf("bundle plugin directory: %w", err)
	}

	// 4. Extract the tar to a stable directory.
	extractDir, err := r.extractDirFromCAS(ctx, p.Name, dgst)
	if err != nil {
		return nil, err
	}

	// 5. Resolve entrypoint and toolchain.
	entryPath := filepath.Join(extractDir, entryRel)
	toolchain := manifest.Plugin.Toolchain
	if toolchain == "" {
		toolchain = inferPluginToolchain(entryPath)
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:      p.Name,
			Script:    entryPath,
			Toolchain: toolchain,
			WorkDir:   extractDir,
		},
		Digest: dgst,
	}, nil
}

// bundleDir creates a deterministic tar archive of the directory and stores
// it in CAS. If files is non-empty, only those files (relative to dirPath)
// are included. Otherwise all files are included (hidden directories like
// .git are always skipped). Entries are sorted lexicographically and
// timestamps/UIDs are zeroed for reproducible hashes.
func (r *PluginResolver) bundleDir(ctx context.Context, dirPath string, files []string) (cas.Digest, error) {
	type fileEntry struct {
		relPath string
		absPath string
		info    fs.FileInfo
	}
	var entries []fileEntry

	if len(files) > 0 {
		// Explicit file list from the plugin manifest.
		for _, f := range files {
			absPath := filepath.Join(dirPath, f)
			info, err := os.Stat(absPath)
			if err != nil {
				return cas.Digest{}, fmt.Errorf("plugin file %q: %w", f, err)
			}
			entries = append(entries, fileEntry{relPath: f, absPath: absPath, info: info})
		}
	} else {
		// No explicit list — include all non-hidden files.
		err := filepath.Walk(dirPath, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() && info.Name() != "." && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dirPath, path)
			if err != nil {
				return err
			}
			entries = append(entries, fileEntry{relPath: rel, absPath: path, info: info})
			return nil
		})
		if err != nil {
			return cas.Digest{}, fmt.Errorf("walking plugin directory: %w", err)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})

	// Create deterministic tar in memory.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.relPath,
			Size:     e.info.Size(),
			Mode:     int64(e.info.Mode() & fs.ModePerm),
			Typeflag: tar.TypeReg,
			// Zero timestamps and UIDs for determinism.
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return cas.Digest{}, fmt.Errorf("write tar header for %s: %w", e.relPath, err)
		}

		f, err := os.Open(e.absPath)
		if err != nil {
			return cas.Digest{}, fmt.Errorf("open %s: %w", e.relPath, err)
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return cas.Digest{}, fmt.Errorf("write tar body for %s: %w", e.relPath, err)
		}
		f.Close()
	}

	if err := tw.Close(); err != nil {
		return cas.Digest{}, fmt.Errorf("close tar: %w", err)
	}

	// Store the tar blob in CAS.
	dgst, err := r.Store.Put(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return cas.Digest{}, fmt.Errorf("store tar in CAS: %w", err)
	}

	return dgst, nil
}

// extractDirFromCAS extracts a plugin tar bundle from CAS to a stable
// directory: ~/.mu/plugins/<name>/bundle-<hash>/.
func (r *PluginResolver) extractDirFromCAS(ctx context.Context, name string, dgst cas.Digest) (string, error) {
	dir := filepath.Join(r.CacheDir, name)
	short := dgst.Hash
	if len(short) > 12 {
		short = short[:12]
	}
	extractDir := filepath.Join(dir, "bundle-"+short)

	// Skip if already extracted.
	if _, err := os.Stat(extractDir); err == nil {
		return extractDir, nil
	}

	// Clean old bundle versions.
	entries, _ := filepath.Glob(filepath.Join(dir, "bundle-*"))
	for _, old := range entries {
		if old != extractDir {
			os.RemoveAll(old)
		}
	}

	// Get tar from CAS.
	rc, err := r.Store.Get(ctx, dgst)
	if err != nil {
		return "", fmt.Errorf("get plugin bundle from CAS: %w", err)
	}
	defer rc.Close()

	// Extract tar entries.
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar entry: %w", err)
		}

		// Validate: no absolute paths, no path traversal.
		if filepath.IsAbs(hdr.Name) || strings.Contains(hdr.Name, "..") {
			return "", fmt.Errorf("tar entry %q: path traversal not allowed", hdr.Name)
		}

		destPath := filepath.Join(extractDir, hdr.Name)

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return "", err
		}

		mode := fs.FileMode(hdr.Mode)
		if mode == 0 {
			mode = 0o644
		}

		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return "", fmt.Errorf("create %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return "", fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
		f.Close()
	}

	return extractDir, nil
}

// resolveRemote fetches a remote plugin by URL, verifies sha256, stores in CAS.
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

	cachedPath, err := r.extractFromCAS(ctx, p.Name, expectedDigest, p.URL)
	if err != nil {
		return nil, err
	}

	return &ResolvedPlugin{
		Def: plugin.PluginDef{
			Name:      p.Name,
			Script:    cachedPath,
			Toolchain: inferPluginToolchain(cachedPath),
		},
		Digest: expectedDigest,
	}, nil
}

// extractFromCAS writes a single-file plugin from CAS to a stable filesystem path.
// srcHint is the original file path or URL, used to preserve the file extension.
// If empty, the file is extracted with no extension (bare executable).
// Files are always extracted with executable permissions (0o755).
func (r *PluginResolver) extractFromCAS(ctx context.Context, name string, dgst cas.Digest, srcHint string) (string, error) {
	dir := filepath.Join(r.CacheDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// Preserve the original file extension from the source hint.
	ext := filepath.Ext(srcHint)

	// Use the digest hash as filename to auto-invalidate on content change.
	short := dgst.Hash
	if len(short) > 12 {
		short = short[:12]
	}
	destPath := filepath.Join(dir, "plugin-"+short+ext)

	// Skip if already extracted.
	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil
	}

	// Clean old versions.
	entries, _ := filepath.Glob(filepath.Join(dir, "plugin-*"))
	for _, old := range entries {
		if old != destPath {
			os.Remove(old)
		}
	}

	rc, err := r.Store.Get(ctx, dgst)
	if err != nil {
		return "", fmt.Errorf("get plugin from CAS: %w", err)
	}
	defer rc.Close()

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
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

// inferPluginToolchain infers the runtime toolchain from a plugin's file
// extension. Returns "bb" for .bb files, empty string for everything else
// (direct execution). This is used when the plugin doesn't explicitly
// declare a toolchain.
func inferPluginToolchain(path string) string {
	if strings.HasSuffix(path, ".bb") {
		return "bb"
	}
	return ""
}

// walkSchemaDir returns the .cue files under pluginDir/schemaPath as
// paths relative to pluginDir, sorted lexicographically. Errors if
// schemaPath is not a directory or contains no .cue files.
func walkSchemaDir(pluginDir, schemaPath string) ([]string, error) {
	abs := filepath.Join(pluginDir, schemaPath)
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat schema dir %q: %w", schemaPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("schema path %q is not a directory", schemaPath)
	}
	var rels []string
	err = filepath.Walk(abs, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".cue") {
			return nil
		}
		rel, err := filepath.Rel(pluginDir, path)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("schema dir %q contains no .cue files", schemaPath)
	}
	sort.Strings(rels)
	return rels, nil
}
