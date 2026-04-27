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
// mu.cue files it finds (or the preprocessor extension when configured).
func Load(projectRoot string) (*ProjectConfig, error) {
	if !hasMuCue(projectRoot) {
		return nil, fmt.Errorf("no mu.cue in %s", projectRoot)
	}
	cfg, err := cueDecoder{}.Decode(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := mergeSubdirConfigs(cfg, projectRoot); err != nil {
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
// When the root config declares a preprocessor, the walker uses the
// preprocessor's extension (mu.<ext>) exclusively and ignores mu.cue in
// subdirectories. This preserves the existing preprocessor contract.
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
func mergeSubdirConfigs(cfg *ProjectConfig, projectRoot string) error {
	usePP := cfg.Preprocessor != nil && cfg.Preprocessor.Extension != "" && len(cfg.Preprocessor.Command) > 0
	var ppFileName string
	if usePP {
		ppFileName = "mu." + cfg.Preprocessor.Extension
	}

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
		if strings.HasPrefix(name, ".") || name == "testdata" {
			return filepath.SkipDir
		}

		var subFile, kind string
		if usePP {
			pp := filepath.Join(path, ppFileName)
			if info, err := os.Lstat(pp); err == nil && info.Mode().IsRegular() {
				subFile = pp
				kind = "pp"
			}
		} else if hasMuCue(path) {
			subFile = filepath.Join(path, configFileCUE)
			kind = configFileCUE
		}
		if subFile == "" {
			return nil
		}

		var partial *ProjectConfig
		var err error
		switch kind {
		case "pp":
			partial, err = Preprocess(cfg.Preprocessor, subFile)
		case configFileCUE:
			partial, err = cueDecoder{}.Decode(path)
		}
		if err != nil {
			return fmt.Errorf("loading %s: %w", subFile, err)
		}

		relDir, err := filepath.Rel(projectRoot, path)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", subFile, err)
		}

		if kind == configFileCUE {
			raw, _ := os.ReadFile(subFile)
			if IsCuePluginDir(raw) {
				cfg.PluginDirs = append(cfg.PluginDirs, relDir)
			}
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
			for j := range p.Command {
				arg := p.Command[j]
				if !filepath.IsAbs(arg) && strings.ContainsAny(arg, "/\\") {
					abs := filepath.Join(subDir, arg)
					if rel, err := filepath.Rel(projectRoot, abs); err == nil {
						p.Command[j] = rel
					}
				}
			}
		}

		merge(cfg, partial)
		return nil
	})
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
// Toolchains and plugins with names that already exist in dst are skipped
// (the parent/root definition wins).
func merge(dst, src *ProjectConfig) {
	dst.Targets = append(dst.Targets, src.Targets...)

	existingTC := make(map[string]bool, len(dst.Toolchains))
	for _, tc := range dst.Toolchains {
		existingTC[tc.Name] = true
	}
	for _, tc := range src.Toolchains {
		if !existingTC[tc.Name] {
			dst.Toolchains = append(dst.Toolchains, tc)
			existingTC[tc.Name] = true
		}
	}

	existingPl := make(map[string]bool, len(dst.Plugins))
	for _, p := range dst.Plugins {
		existingPl[p.Name] = true
	}
	for _, p := range src.Plugins {
		if !existingPl[p.Name] {
			dst.Plugins = append(dst.Plugins, p)
			existingPl[p.Name] = true
		}
	}
}
