// Package paths names every directory and file mu keeps under ~/.mu. It is
// the one place those locations are spelled out; callers resolve a Paths
// value once (config.ResolvePaths applies the project's cache.path) and pass
// it around instead of joining "~/.mu/..." themselves.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths are mu's per-user state locations.
type Paths struct {
	Root            string // ~/.mu
	CacheDir        string // local CAS (blobs/, ac/, ingest/); cache.path overrides it
	PluginsDir      string // extracted plugin versions, <name>/bundle-<hex>
	ToolchainsDir   string // binaries extracted from toolchain artifacts
	CredentialsFile string // registry credentials written by `mu login`
}

// Under returns the layout rooted at root (normally ~/.mu).
func Under(root string) Paths {
	return Paths{
		Root:            root,
		CacheDir:        filepath.Join(root, "cache"),
		PluginsDir:      filepath.Join(root, "plugins"),
		ToolchainsDir:   filepath.Join(root, "toolchains"),
		CredentialsFile: filepath.Join(root, "credentials.json"),
	}
}

// Default returns the layout under the user's home directory.
func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolving home directory: %w", err)
	}
	return Under(filepath.Join(home, ".mu")), nil
}
