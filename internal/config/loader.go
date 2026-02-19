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
// BUILD files found recursively under projectRoot. When a preprocessor is
// declared in mu.json, Load looks for BUILD.<ext> files (using the
// preprocessor's extension) and pipes them through the external command.
// Otherwise it falls back to BUILD.json.
func Load(projectRoot string) (*ProjectConfig, error) {
	rootFile := filepath.Join(projectRoot, "mu.json")
	cfg, err := loadFile(rootFile)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", rootFile, err)
	}

	// Determine which BUILD filename to look for and how to load it.
	buildFileName := "BUILD.json"
	usePP := cfg.Preprocessor != nil && cfg.Preprocessor.Extension != "" && len(cfg.Preprocessor.Command) > 0
	if usePP {
		buildFileName = "BUILD." + cfg.Preprocessor.Extension
	}

	// Walk the tree looking for BUILD files and merge each one.
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
		if d.IsDir() {
			return nil
		}
		if d.Name() != buildFileName {
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

		// Compute the package path relative to project root. A BUILD file
		// sitting at projectRoot/foo/bar/ produces the prefix "//foo/bar".
		relDir, err := filepath.Rel(projectRoot, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", path, err)
		}

		prefix := "//" + filepath.ToSlash(relDir)
		// Normalise the root case: "//."->"//".
		prefix = strings.TrimSuffix(prefix, "/.")

		// Prefix target names so they are absolute within the project.
		for i := range partial.Targets {
			t := &partial.Targets[i]
			if !strings.HasPrefix(t.Name, "//") {
				if prefix == "//" {
					t.Name = "//" + t.Name
				} else {
					t.Name = prefix + "/" + t.Name
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

// merge appends targets, services, and triggers from src into dst.
func merge(dst, src *ProjectConfig) {
	dst.Targets = append(dst.Targets, src.Targets...)
	dst.Services = append(dst.Services, src.Services...)
	dst.Triggers = append(dst.Triggers, src.Triggers...)
}
