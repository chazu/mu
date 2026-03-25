package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FindProjectRoot walks up from startDir looking for a directory that
// contains mu.json. It returns the absolute path to that directory.
func FindProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving start directory: %w", err)
	}

	for {
		candidate := filepath.Join(dir, "mu.json")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding mu.json.
			return "", fmt.Errorf("mu.json not found in %s or any parent directory", startDir)
		}
		dir = parent
	}
}

// Load reads mu.json from projectRoot and then discovers and merges any
// mu.json files found recursively in subdirectories. When a preprocessor is
// declared in the root mu.json, Load looks for mu.<ext> files (using the
// preprocessor's extension) and pipes them through the external command.
// Otherwise it falls back to mu.json.
func Load(projectRoot string) (*ProjectConfig, error) {
	rootFile := filepath.Join(projectRoot, "mu.json")
	cfg, err := loadFile(rootFile)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", rootFile, err)
	}

	// Determine which filename to look for in subdirectories and how to load it.
	subFileName := "mu.json"
	usePP := cfg.Preprocessor != nil && cfg.Preprocessor.Extension != "" && len(cfg.Preprocessor.Command) > 0
	if usePP {
		subFileName = "mu." + cfg.Preprocessor.Extension
	}

	// Walk the tree looking for mu.json files in subdirectories and merge each one.
	// WalkDir (unlike Walk) does not follow symlinks, preventing infinite
	// loops from cyclic symlinks and config injection from outside the
	// project root.
	err = filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Skip symlinks entirely — both directories and files.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		// Skip hidden directories (e.g. .git, .claude) to avoid picking up
		// configs from worktrees, caches, and other tool-managed copies.
		if d.IsDir() && d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != subFileName {
			return nil
		}

		// Skip the root mu.json — it was already loaded above.
		if path == rootFile {
			return nil
		}

		var partial *ProjectConfig
		if usePP {
			partial, err = Preprocess(cfg.Preprocessor, path)
		} else {
			partial, err = loadFile(path)
		}
		if err != nil {
			return fmt.Errorf("loading %s: %w", path, err)
		}

		// Compute the package path relative to project root. A mu.json file
		// sitting at projectRoot/foo/bar/ produces the prefix "//foo/bar".
		relDir, err := filepath.Rel(projectRoot, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", path, err)
		}

		prefix := "//" + filepath.ToSlash(relDir)
		// Normalise the root case: "//."->"//".
		prefix = strings.TrimSuffix(prefix, "/.")

		// Prefix target names and rebase source paths so they are
		// absolute within the project.
		subDir := filepath.Dir(path)
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
	if err != nil {
		return nil, err
	}

	return cfg, nil
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
