package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"

	// Pull in backend registrations (disk, oci-stub).
	_ "github.com/chazu/mu/internal/cas/oci"
)

// buildCacheStore constructs the CAS store from mu.cue cache config. When
// no cache block is configured, falls back to a single local disk store
// at ~/.mu/cache. Multiple backends compose into a cas.Tiered.
func buildCacheStore(cacheCfg *config.CacheConfig, verbose bool) (cas.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	defaultDisk := filepath.Join(home, ".mu", "cache")

	spec := cas.BuildSpec{
		DefaultDiskPath: defaultDisk,
		Observer:        cacheObserver(verbose),
	}

	if cacheCfg != nil {
		spec.ReadRepair = cacheCfg.ReadRepair
		spec.WriteThrough = cacheCfg.WriteThrough
		if len(cacheCfg.Backends) == 0 && (cacheCfg.ReadRepair || cacheCfg.WriteThrough) {
			return nil, fmt.Errorf("cache config sets read_repair or write_through but backends is empty")
		}
		for i, b := range cacheCfg.Backends {
			rb := cas.ResolvedBackend{
				BackendSpec: cas.BackendSpec{
					Type:     b.Type,
					Registry: b.Registry,
				},
				Read:  readFlag(b.Read),
				Write: writeFlag(b.Write),
			}
			if b.Type == "disk" {
				p, err := cas.ExpandPath(b.Path)
				if err != nil {
					return nil, fmt.Errorf("backends[%d].path: %w", i, err)
				}
				rb.BackendSpec.Path = p
			}
			spec.Backends = append(spec.Backends, rb)
		}
	}

	return cas.BuildStore(spec)
}

func readFlag(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

func writeFlag(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// cacheObserver returns a Tiered event handler. When verbose is true, hits
// and misses are logged at debug level with layer attribution; errors are
// always logged at warn level.
func cacheObserver(verbose bool) func(cas.TieredEvent) {
	return func(e cas.TieredEvent) {
		switch e.Outcome {
		case "error":
			slog.Warn("cache layer error",
				"op", e.Op, "layer", e.Layer, "name", e.LayerName, "digest", e.Digest.String(), "err", e.Err)
		case "repair":
			slog.Info("cache read-repair",
				"op", e.Op, "layer", e.Layer, "name", e.LayerName, "digest", e.Digest.String())
		default:
			if verbose {
				slog.Debug("cache layer event",
					"op", e.Op, "outcome", e.Outcome, "layer", e.Layer, "name", e.LayerName, "digest", e.Digest.String())
			}
		}
	}
}
