package config

import (
	"fmt"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/paths"
)

// ResolvePaths returns mu's per-user locations with the project's cache.path
// applied. cfg may be nil (no project), which yields the defaults.
func ResolvePaths(cfg *ProjectConfig) (paths.Paths, error) {
	p, err := paths.Default()
	if err != nil {
		return paths.Paths{}, err
	}
	if cfg != nil && cfg.Cache != nil && cfg.Cache.Path != "" {
		dir, err := cas.ExpandPath(cfg.Cache.Path)
		if err != nil {
			return paths.Paths{}, fmt.Errorf("cache.path: %w", err)
		}
		p.CacheDir = dir
	}
	return p, nil
}
