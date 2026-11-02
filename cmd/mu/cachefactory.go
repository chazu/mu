package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
)

// buildCacheStore constructs the build cache from mu.cue's cache block: the
// local disk store at cache.path (default ~/.mu/cache), plus cache.remote as
// a read-only fallback unless offline is set. The second parameter (the old
// verbose flag) is unused; remote restores are logged at debug level.
func buildCacheStore(cacheCfg *config.CacheConfig, _ bool, offline ...bool) (cas.Store, error) {
	localOnly := len(offline) > 0 && offline[0]

	path := ""
	if cacheCfg != nil && cacheCfg.Path != "" {
		p, err := cas.ExpandPath(cacheCfg.Path)
		if err != nil {
			return nil, fmt.Errorf("cache.path: %w", err)
		}
		path = p
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home directory: %w", err)
		}
		path = filepath.Join(home, ".mu", "cache")
	}
	local, err := cas.NewDiskStore(path)
	if err != nil {
		return nil, err
	}

	if localOnly || cacheCfg == nil || cacheCfg.Remote == "" {
		return local, nil
	}
	remote, err := oci.NewRemote(cacheCfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("cache.remote: %w", err)
	}
	return &cas.Cache{Local: local, Remote: remote}, nil
}
