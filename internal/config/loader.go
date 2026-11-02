package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// configFileCUE is the basename of the project / per-package config file.
const configFileCUE = "mu.cue"

// hasMuCue reports whether dir contains a regular mu.cue file. Symlinked
// config files are treated as absent so a symlink inside the project tree
// cannot escape the root.
func hasMuCue(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, configFileCUE))
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// FindProjectRoot walks up from startDir looking for a directory that
// contains a mu.cue. It returns the absolute path to that directory.
func FindProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving start directory: %w", err)
	}
	for {
		if hasMuCue(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("mu.cue not found in %s or any parent directory", startDir)
		}
		dir = parent
	}
}

// Load reads the project configuration rooted at projectRoot. It loads
// mu.cue at the root, then walks subdirectories merging any per-package
// mu.cue files it finds.
func Load(projectRoot string) (*ProjectConfig, error) { return LoadOffline(projectRoot, false) }

// LoadOffline uses cached/local CUE modules without contacting a registry.
func LoadOffline(projectRoot string, offline bool) (*ProjectConfig, error) {
	if !hasMuCue(projectRoot) {
		return nil, fmt.Errorf("no mu.cue in %s", projectRoot)
	}
	cfg, err := cueDecoder{offline: offline}.Decode(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := mergeSubdirConfigs(cfg, projectRoot, offline); err != nil {
		return nil, err
	}
	if err := expandSourceGlobs(cfg, projectRoot); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeSubdirConfigs walks projectRoot looking for per-package mu.cue
// files and merges each one into cfg.
//
// WalkDir (unlike Walk) does not follow symlinks, preventing infinite
// loops from cyclic symlinks and config injection from outside the
// project root. In addition, we explicitly skip:
//   - any entry whose own mode is a symlink (belt-and-braces);
//   - hidden directories (name starts with "."); and
//   - testdata directories (Go convention for test fixtures).
//
// Symlinked mu.cue files inside otherwise-real subdirectories are
// filtered out via Lstat + Mode().IsRegular().
//
// A subdirectory with its own cue.mod/ is a separate CUE module and so a
// separate mu project; it is skipped along with everything under it.
//
// Sub-configs are restricted: they may not set project-wide fields
// (cache, publish, secrets), may not redeclare a plugin or toolchain name
// already declared elsewhere, and may only declare targets under their
// own directory.
func mergeSubdirConfigs(cfg *ProjectConfig, projectRoot string, offline ...bool) error {
	rootFile := filepath.Join(projectRoot, configFileCUE)
	origins := newNameOrigins(cfg, rootFile)
	return filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path == projectRoot {
			return nil
		}

		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "testdata" || isNestedModule(path) {
			return filepath.SkipDir
		}

		if !hasMuCue(path) {
			return nil
		}
		subFile := filepath.Join(path, configFileCUE)
		partial, err := cueDecoder{offline: len(offline) > 0 && offline[0]}.Decode(path)
		if err != nil {
			return fmt.Errorf("loading %s: %w", subFile, err)
		}
		if err := rejectRootOnlyFields(partial, subFile); err != nil {
			return err
		}

		relDir, err := filepath.Rel(projectRoot, path)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", subFile, err)
		}

		raw, _ := os.ReadFile(subFile)
		if IsCuePluginDir(raw) {
			cfg.PluginDirs = append(cfg.PluginDirs, relDir)
		}

		prefix := "//" + filepath.ToSlash(relDir)
		prefix = strings.TrimSuffix(prefix, "/.")

		subDir := path

		for i := range partial.Targets {
			t := &partial.Targets[i]
			if !strings.HasPrefix(t.Name, "//") {
				if prefix == "//" {
					t.Name = "//" + t.Name
				} else {
					t.Name = prefix + "/" + t.Name
				}
			} else if !targetUnder(t.Name, prefix) {
				return fmt.Errorf("%s: target %q is outside this directory; name it %s/... or use a relative name", subFile, t.Name, prefix)
			}
			for j := range t.Sources {
				if !filepath.IsAbs(t.Sources[j]) {
					abs := filepath.Join(subDir, t.Sources[j])
					if rel, err := filepath.Rel(projectRoot, abs); err == nil {
						t.Sources[j] = rel
					}
				}
			}
		}

		for i := range partial.Plugins {
			p := &partial.Plugins[i]
			if p.Script != "" && !filepath.IsAbs(p.Script) {
				abs := filepath.Join(subDir, p.Script)
				rel, err := filepath.Rel(projectRoot, abs)
				if err == nil {
					p.Script = rel
				}
			}
			if len(p.Command) > 0 && isDotRelative(p.Command[0]) {
				abs := filepath.Join(subDir, p.Command[0])
				if rel, err := filepath.Rel(projectRoot, abs); err == nil {
					if !strings.ContainsRune(rel, filepath.Separator) {
						rel = "." + string(filepath.Separator) + rel
					}
					p.Command[0] = rel
				}
			}
		}

		if err := origins.claim(partial, subFile); err != nil {
			return err
		}
		merge(cfg, partial)
		return nil
	})
}

// isNestedModule reports whether dir holds its own cue.mod directory,
// marking it as the root of a separate project.
func isNestedModule(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, "cue.mod"))
	return err == nil && info.IsDir()
}

// isDotRelative reports whether a plugin command's executable is written
// as an explicit relative path ("./x" or "../x"). Only such a command[0]
// is rebased onto the project root; arguments are never rewritten.
func isDotRelative(arg string) bool {
	arg = filepath.ToSlash(arg)
	return strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../")
}

// targetUnder reports whether an absolute target name lies in the package
// prefix ("//sub") or below it ("//sub/x", "//sub:x").
func targetUnder(name, prefix string) bool {
	if name == prefix {
		return true
	}
	return strings.HasPrefix(name, prefix+"/") || strings.HasPrefix(name, prefix+":")
}

// rejectRootOnlyFields fails when a sub-directory mu.cue sets a field that
// only the project root may set. These fields are project-wide, so merging
// them from a sub-config would be ambiguous; silently dropping them hides
// the mistake.
func rejectRootOnlyFields(partial *ProjectConfig, file string) error {
	var field string
	switch {
	case partial.Cache != nil:
		field = "cache"
	case partial.Publish != nil:
		field = "publish"
	case partial.Secrets != nil:
		field = "secrets"
	default:
		return nil
	}
	return fmt.Errorf("%s: %q may only be set in the project root mu.cue", file, field)
}

// nameOrigins records which config file declared each plugin and toolchain
// name, so a redeclaration can name both files.
type nameOrigins struct {
	plugins    map[string]string
	toolchains map[string]string
}

func newNameOrigins(root *ProjectConfig, rootFile string) *nameOrigins {
	o := &nameOrigins{plugins: map[string]string{}, toolchains: map[string]string{}}
	for _, p := range root.Plugins {
		o.plugins[p.Name] = rootFile
	}
	for _, tc := range root.Toolchains {
		o.toolchains[tc.Name] = rootFile
	}
	return o
}

// claim registers every plugin and toolchain name in partial as declared
// by file, failing on any name another config (or file itself) declared.
func (o *nameOrigins) claim(partial *ProjectConfig, file string) error {
	for _, p := range partial.Plugins {
		if prev, ok := o.plugins[p.Name]; ok {
			return fmt.Errorf("plugin %q is declared in both %s and %s", p.Name, prev, file)
		}
		o.plugins[p.Name] = file
	}
	for _, tc := range partial.Toolchains {
		if prev, ok := o.toolchains[tc.Name]; ok {
			return fmt.Errorf("toolchain %q is declared in both %s and %s", tc.Name, prev, file)
		}
		o.toolchains[tc.Name] = file
	}
	return nil
}

// isGlob reports whether s contains glob meta-characters.
func isGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// expandSourceGlobs expands glob patterns in target source lists. Patterns
// are matched relative to the project root and support doublestar (**)
// semantics (e.g. "src/**/*.go" matches *.go files at any depth under
// src/). Non-glob entries pass through unchanged. A glob that matches no
// files is kept as-is (the downstream resolve step will report the
// missing file). Matches whose path contains any hidden segment (a
// component beginning with ".") are excluded, mirroring the hidden-dir
// skipping done during config discovery.
func expandSourceGlobs(cfg *ProjectConfig, projectRoot string) error {
	fsys := os.DirFS(projectRoot)
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		var expanded []string
		for _, src := range t.Sources {
			if !isGlob(src) {
				expanded = append(expanded, src)
				continue
			}
			pattern := filepath.ToSlash(src)
			if !doublestar.ValidatePattern(pattern) {
				return fmt.Errorf("target %q: invalid glob %q", t.Name, src)
			}
			matches, err := doublestar.Glob(fsys, pattern)
			if err != nil {
				return fmt.Errorf("target %q: invalid glob %q: %w", t.Name, src, err)
			}
			filtered := matches[:0]
			for _, m := range matches {
				if hasHiddenSegment(m) {
					continue
				}
				filtered = append(filtered, m)
			}
			if len(filtered) == 0 {
				expanded = append(expanded, src)
				continue
			}
			sort.Strings(filtered)
			for _, m := range filtered {
				expanded = append(expanded, filepath.FromSlash(m))
			}
		}
		t.Sources = expanded
	}
	return nil
}

// hasHiddenSegment reports whether any path component of p (split on "/")
// starts with a dot, excluding "." and ".." themselves.
func hasHiddenSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || seg == "" {
			continue
		}
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// merge appends targets, toolchains, and plugins from src into dst.
// Callers reject duplicate plugin and toolchain names (nameOrigins.claim)
// before merging.
func merge(dst, src *ProjectConfig) {
	dst.Targets = append(dst.Targets, src.Targets...)
	dst.Toolchains = append(dst.Toolchains, src.Toolchains...)
	dst.Plugins = append(dst.Plugins, src.Plugins...)
}
