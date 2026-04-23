package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Config-file basenames recognized by FindProjectRoot, Load, and the
// subdirectory walker. mu.cue is preferred over mu.json; the two MUST NOT
// coexist in the same directory — if they do, callers get a clear error
// pointing at `mu migrate`.
const (
	configFileCUE  = "mu.cue"
	configFileJSON = "mu.json"
)

// probeConfigFile reports which of {mu.cue, mu.json} is present in dir.
//
// Returns (configFileCUE, nil) if only mu.cue is a regular file,
// (configFileJSON, nil) if only mu.json is a regular file, and
// ("", nil) if neither is present.
//
// Returns a non-nil error if BOTH are present in the same directory; the
// error message points the user at `mu migrate` so they can consolidate.
//
// Symlinked config files are treated as absent (not present). This keeps
// the symlink-safety invariant that the walker already honors for
// subdirectories — a symlinked mu.json or mu.cue inside the project tree
// can escape the root, so we pretend it isn't there.
func probeConfigFile(dir string) (string, error) {
	hasRegular := func(path string) bool {
		info, err := os.Lstat(path)
		if err != nil {
			return false
		}
		return info.Mode().IsRegular()
	}
	hasCue := hasRegular(filepath.Join(dir, configFileCUE))
	hasJSON := hasRegular(filepath.Join(dir, configFileJSON))
	if hasCue && hasJSON {
		return "", fmt.Errorf("both mu.cue and mu.json present in %s; run `mu migrate` to consolidate on one format", dir)
	}
	if hasCue {
		return configFileCUE, nil
	}
	if hasJSON {
		return configFileJSON, nil
	}
	return "", nil
}

// FindProjectRoot walks up from startDir looking for a directory that
// contains a project config file (mu.cue preferred, mu.json legacy). It
// returns the absolute path to that directory.
//
// If both mu.cue and mu.json are present in the same directory, an error
// is returned immediately pointing at `mu migrate`.
func FindProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving start directory: %w", err)
	}

	for {
		name, err := probeConfigFile(dir)
		if err != nil {
			return "", err
		}
		if name != "" {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding a config file.
			return "", fmt.Errorf("mu.cue or mu.json not found in %s or any parent directory", startDir)
		}
		dir = parent
	}
}

// Load reads the project configuration rooted at projectRoot. It probes
// projectRoot for mu.cue (preferred) or mu.json (legacy) and dispatches
// to the appropriate decoder. It then walks subdirectories and merges
// any per-package config files it finds — mu.cue or mu.json — per the
// existing prefix-and-rebase rules. If both coexist in the same
// directory, Load errors with a `mu migrate` hint.
func Load(projectRoot string) (*ProjectConfig, error) {
	name, err := probeConfigFile(projectRoot)
	if err != nil {
		return nil, err
	}
	switch name {
	case configFileCUE:
		return loadCUEProject(projectRoot)
	case configFileJSON:
		return loadJSONProject(projectRoot)
	default:
		return nil, fmt.Errorf("no mu.cue or mu.json in %s", projectRoot)
	}
}

// loadCUEProject loads the root mu.cue via cueDecoder, then walks
// subdirectories merging per-package configs (mu.cue or mu.json).
func loadCUEProject(projectRoot string) (*ProjectConfig, error) {
	cfg, err := cueDecoder{}.Decode(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := mergeSubdirConfigs(cfg, projectRoot, true); err != nil {
		return nil, err
	}
	if err := expandSourceGlobs(cfg, projectRoot); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadJSONProject is the legacy JSON implementation of project-config
// loading. It is wired up as jsonDecoder.Decode via the Decoder interface.
func loadJSONProject(projectRoot string) (*ProjectConfig, error) {
	rootFile := filepath.Join(projectRoot, configFileJSON)
	cfg, err := loadFile(rootFile)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", rootFile, err)
	}
	if err := mergeSubdirConfigs(cfg, projectRoot, false); err != nil {
		return nil, err
	}
	if err := expandSourceGlobs(cfg, projectRoot); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeSubdirConfigs walks projectRoot looking for per-package config
// files and merges each one into cfg.
//
// Within a given subdirectory the walker probes for mu.cue and mu.json:
//   - exactly one present → decode via the matching decoder;
//   - both present → error with `mu migrate` hint;
//   - neither present → skip the directory.
//
// When the root config declares a preprocessor, the walker uses the
// preprocessor's extension (mu.<ext>) exclusively and ignores mu.cue /
// mu.json in subdirectories. This preserves the existing preprocessor
// contract.
//
// WalkDir (unlike Walk) does not follow symlinks, preventing infinite
// loops from cyclic symlinks and config injection from outside the
// project root. In addition, we explicitly skip:
//   - any entry whose own mode is a symlink (belt-and-braces);
//   - hidden directories (name starts with "."); and
//   - testdata directories (Go convention for test fixtures).
//
// Symlinked config files inside otherwise-real subdirectories are
// filtered out by probeConfigFile via Lstat + Mode().IsRegular().
//
// When loadCue is false the walker only loads mu.json partials in
// subdirectories (legacy JSON-root behavior), but it still probes each
// subdir and reports the "both mu.cue and mu.json present" error so
// migration conflicts are surfaced uniformly. When loadCue is true
// (CUE-root projects) both mu.cue and mu.json subdir configs are loaded
// — this is the "mixed tree" path required by the mcm roadmap.
func mergeSubdirConfigs(cfg *ProjectConfig, projectRoot string, loadCue bool) error {
	usePP := cfg.Preprocessor != nil && cfg.Preprocessor.Extension != "" && len(cfg.Preprocessor.Command) > 0
	var ppFileName string
	if usePP {
		ppFileName = "mu." + cfg.Preprocessor.Extension
	}

	return filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Skip symlinks entirely — both directories and files.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		// Root was already loaded by the caller; don't re-probe or
		// re-load it. This also avoids re-flagging a legitimate
		// "both mu.cue and mu.json present" situation that the caller
		// (Load) already surfaced — and lets callers that bypass Load
		// (e.g. jsonDecoder{}.Decode on a fixture directory that
		// happens to contain both) still function.
		if path == projectRoot {
			return nil
		}

		// Skip hidden directories and testdata (Go test convention).
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "testdata" {
			return filepath.SkipDir
		}

		// Determine what file (if any) to load from this directory.
		var subFile, kind string
		if usePP {
			pp := filepath.Join(path, ppFileName)
			if info, err := os.Lstat(pp); err == nil && info.Mode().IsRegular() {
				subFile = pp
				kind = "pp"
			}
		} else {
			probed, err := probeConfigFile(path)
			if err != nil {
				return err // both mu.cue and mu.json present
			}
			switch probed {
			case configFileJSON:
				subFile = filepath.Join(path, probed)
				kind = probed
			case configFileCUE:
				if loadCue {
					subFile = filepath.Join(path, probed)
					kind = probed
				}
				// else: JSON-root project, skip cue-only subdirs.
			}
		}
		if subFile == "" {
			return nil
		}

		var partial *ProjectConfig
		var err error
		switch kind {
		case "pp":
			partial, err = Preprocess(cfg.Preprocessor, subFile)
		case configFileJSON:
			partial, err = loadFile(subFile)
		case configFileCUE:
			partial, err = cueDecoder{}.Decode(path)
		}
		if err != nil {
			return fmt.Errorf("loading %s: %w", subFile, err)
		}

		// Compute the package path relative to project root. A config at
		// projectRoot/foo/bar/ produces the prefix "//foo/bar".
		relDir, err := filepath.Rel(projectRoot, path)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", subFile, err)
		}

		// Track plugin directories for post-build CAS bundling.
		switch kind {
		case configFileJSON:
			raw, _ := os.ReadFile(subFile)
			if IsPluginDir(raw) {
				cfg.PluginDirs = append(cfg.PluginDirs, relDir)
			}
		case configFileCUE:
			raw, _ := os.ReadFile(subFile)
			if IsCuePluginDir(raw) {
				cfg.PluginDirs = append(cfg.PluginDirs, relDir)
			}
		}

		prefix := "//" + filepath.ToSlash(relDir)
		// Normalise the root case: "//."->"//".
		prefix = strings.TrimSuffix(prefix, "/.")

		subDir := path

		// Prefix target names and rebase source paths so they are
		// absolute within the project.
		for i := range partial.Targets {
			t := &partial.Targets[i]
			if !strings.HasPrefix(t.Name, "//") {
				if prefix == "//" {
					t.Name = "//" + t.Name
				} else {
					t.Name = prefix + "/" + t.Name
				}
			}
			// Rebase source file paths relative to the subdirectory.
			for j := range t.Sources {
				if !filepath.IsAbs(t.Sources[j]) {
					abs := filepath.Join(subDir, t.Sources[j])
					if rel, err := filepath.Rel(projectRoot, abs); err == nil {
						t.Sources[j] = rel
					}
				}
			}
		}

		// Rebase plugin paths relative to the subdirectory.
		for i := range partial.Plugins {
			p := &partial.Plugins[i]
			if p.Script != "" && !filepath.IsAbs(p.Script) {
				abs := filepath.Join(subDir, p.Script)
				rel, err := filepath.Rel(projectRoot, abs)
				if err == nil {
					p.Script = rel
				}
			}
			// Only rebase command args that look like relative paths
			// (contain a path separator). Bare command names like "bb"
			// should not be rebased.
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
			// doublestar operates on io/fs paths which always use "/".
			pattern := filepath.ToSlash(src)
			if !doublestar.ValidatePattern(pattern) {
				return fmt.Errorf("target %q: invalid glob %q", t.Name, src)
			}
			matches, err := doublestar.Glob(fsys, pattern)
			if err != nil {
				return fmt.Errorf("target %q: invalid glob %q: %w", t.Name, src, err)
			}
			// Filter out hidden segments (e.g. .git/foo.go) and sort.
			filtered := matches[:0]
			for _, m := range matches {
				if hasHiddenSegment(m) {
					continue
				}
				filtered = append(filtered, m)
			}
			if len(filtered) == 0 {
				// No matches — keep the literal pattern so the resolve
				// step can report a clear error about the missing file.
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

// loadFile reads and unmarshals a single JSON config file.
func loadFile(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	return &cfg, nil
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
