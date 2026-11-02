package main

import (
	"fmt"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
)

// buildCacheStore constructs the build cache: the local disk store at
// cacheDir (paths.CacheDir, which already honours cache.path), plus
// cache.remote as a read-only fallback unless offline is set. Remote
// restores are logged at debug level.
func buildCacheStore(cacheDir string, cacheCfg *config.CacheConfig, offline bool) (cas.Store, error) {
	local, err := cas.NewDiskStore(cacheDir)
	if err != nil {
		return nil, err
	}
	if offline || cacheCfg == nil || cacheCfg.Remote == "" {
		return local, nil
	}
	remote, err := oci.NewRemote(cacheCfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("cache.remote: %w", err)
	}
	return &cas.Cache{Local: local, Remote: remote}, nil
}
