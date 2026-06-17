package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/schemacache"
)

// runPluginPush publishes the named plugin to the configured cache.push
// registry under <repo>/mu/plugins/<name>:sha256-<short>, and updates the mu
// plugin index at <repo>/mu/plugin-index:v1.
func runPluginPush(args []string) int {
	fs := flag.NewFlagSet("plugin push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("plugin push", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin push <name>")
		return exitUsage
	}
	name := fs.Arg(0)

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true}); !ok {
		return code
	}

	targetName := "//plugins/" + name
	var entrypoint string
	foundTarget := false
	for _, t := range c.Config.Targets {
		if t.Name == targetName {
			foundTarget = true
			if len(t.Sources) > 0 {
				entrypoint = filepath.Base(t.Sources[0])
			}
			break
		}
	}
	if !foundTarget {
		return c.fail(exitFail, "target %q not found in config", targetName)
	}

	result, err := buildTargets(c.ProjectRoot, c.Config, []string{targetName})
	if err != nil {
		return c.fail(exitFail, "build %s: %v", targetName, err)
	}
	if result.Failed > 0 {
		return c.fail(exitFail, "build failed for %s", targetName)
	}

	dgst, err := extractPluginDigest(result, targetName)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	files, isBundle, err := collectPluginFiles(name, entrypoint)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	// Sort file paths for deterministic layer order.
	paths := make([]string, 0, len(files))
	for k := range files {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	// Toolchain inferred from entrypoint extension.
	toolchain := ""
	if filepath.Ext(entrypoint) == ".bb" {
		toolchain = "bb"
	}

	cfg := oci.PluginConfig{
		Name:       name,
		Entrypoint: entrypoint,
		Toolchain:  toolchain,
		Files:      paths,
		Digest:     dgst.String(),
		Source:     detectGitRemote(c.ProjectRoot),
	}

	// Carry vendored schema declarations from the plugin source so
	// downstream consumers (pudl) can locate them in the artifact
	// without re-parsing mu.cue.
	pluginSrc := filepath.Join(c.ProjectRoot, "plugins", name)
	if pcfg, err := config.LoadPluginManifest(pluginSrc); err == nil && pcfg.Plugin != nil {
		for _, s := range pcfg.Plugin.Schemas {
			cfg.Schemas = append(cfg.Schemas, oci.PluginSchemaDecl{
				Module: s.Module, Version: s.Version, Path: s.Path,
			})
		}
	}

	// Refuse to push if vendored schema bytes conflict with a
	// previously-cached (module, version) under different content.
	if err := checkPushSchemaCollisions(pluginSrc, cfg.Schemas); err != nil {
		return c.fail(exitFail, "%v", err)
	}

	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}
	pluginRepoRef := ref + "/" + oci.PluginRepoPrefix + "/" + name
	indexRepoRef := ref + "/" + oci.PluginIndexRef

	pluginRepo, err := newPushRepository(pluginRepoRef)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	indexRepo, err := newPushRepository(indexRepoRef)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	ctx := context.Background()
	if err := pushPluginToRegistry(ctx, pluginRepo, indexRepo, cfg, files); err != nil {
		return c.fail(exitFail, "push plugin: %v", err)
	}

	kind := "single-file"
	if isBundle {
		kind = "bundle"
	}
	fmt.Printf("Pushed %s plugin %q → %s:%s\n", kind, name, pluginRepoRef, oci.PluginTag(dgst.Hash))
	return exitOK
}

// pushPluginToRegistry runs the registry-side write: push the artifact, then
// update the index. Exposed (lowercase, package-internal) for tests.
func pushPluginToRegistry(ctx context.Context, pluginRepo, indexRepo oci.Registry, cfg oci.PluginConfig, files map[string][]byte) error {
	if _, err := oci.PushPlugin(ctx, pluginRepo, cfg.Name, cfg, files); err != nil {
		return err
	}
	return oci.UpdatePluginIndex(ctx, indexRepo, cfg.Name)
}

// collectPluginFiles reads the on-disk extraction of plugin <name> under
// ~/.mu/plugins/<name>/ and returns a map of relative-path → bytes plus
// a flag indicating whether the source was a multi-file bundle.
//
// Layout assumptions:
//   - Bundle: ~/.mu/plugins/<name>/bundle-<hash>/<files...>
//   - Single file: ~/.mu/plugins/<name>/plugin-<hash>.<ext>
//
// For single-file plugins, entrypoint is used as the destination key when
// provided. If entrypoint is empty, the key falls back to <name><ext> derived
// from the cache filename.
func collectPluginFiles(name, entrypoint string) (map[string][]byte, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, err
	}
	dir := filepath.Join(home, ".mu", "plugins", name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("read plugins dir %s: %w", dir, err)
	}

	out := map[string][]byte{}
	isBundle := false

	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() && strings.HasPrefix(e.Name(), "bundle-") {
			isBundle = true
			err := filepath.Walk(full, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(full, p)
				if relErr != nil {
					return relErr
				}
				b, rerr := os.ReadFile(p)
				if rerr != nil {
					return rerr
				}
				out[filepath.ToSlash(rel)] = b
				return nil
			})
			if err != nil {
				return nil, false, err
			}
			continue
		}
		if !e.IsDir() && strings.HasPrefix(e.Name(), "plugin-") {
			b, rerr := os.ReadFile(full)
			if rerr != nil {
				return nil, false, rerr
			}
			key := entrypoint
			if key == "" {
				// Fallback when entrypoint isn't known: <name><ext> from cache filename.
				key = name + filepath.Ext(e.Name())
			}
			out[key] = b
		}
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("no plugin files found under %s — has the plugin been built?", dir)
	}
	return out, isBundle, nil
}

// detectGitRemote returns the origin URL from .git/config, or "" on any
// failure. Best-effort metadata — does not affect push correctness.
func detectGitRemote(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, ".git", "config"))
	if err != nil {
		return ""
	}
	content := string(b)
	idx := strings.Index(content, `[remote "origin"]`)
	if idx < 0 {
		return ""
	}
	rest := content[idx:]
	urlIdx := strings.Index(rest, "url =")
	if urlIdx < 0 {
		return ""
	}
	line := rest[urlIdx+len("url ="):]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	return strings.TrimSpace(line)
}

// checkPushSchemaCollisions reads vendored schema files from the plugin
// source directory and verifies they don't conflict with mu's local
// schema cache. Returns an error if any (module, version) already exists
// with different content bytes.
func checkPushSchemaCollisions(pluginSrc string, schemas []oci.PluginSchemaDecl) error {
	if len(schemas) == 0 {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	cache, err := schemacache.New(filepath.Join(home, ".mu", "schemas"))
	if err != nil {
		return nil
	}
	for _, s := range schemas {
		if !cache.Has(s.Module, s.Version) {
			continue
		}
		schemaDir := filepath.Join(pluginSrc, s.Path)
		var incoming []schemacache.File
		filepath.WalkDir(schemaDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".cue") {
				return nil
			}
			rel, relErr := filepath.Rel(schemaDir, path)
			if relErr != nil {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			incoming = append(incoming, schemacache.File{
				RelPath: filepath.ToSlash(rel),
				Content: content,
			})
			return nil
		})
		if len(incoming) == 0 {
			continue
		}
		if err := cache.Insert(s.Module, s.Version, incoming); err != nil {
			if errors.Is(err, schemacache.ErrVersionMismatch) {
				return fmt.Errorf("schema collision for (%s, %s): cached content differs from plugin bundle — bump the version before pushing", s.Module, s.Version)
			}
			return err
		}
	}
	return nil
}
