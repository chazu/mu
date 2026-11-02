// Package paths names every directory and file mu keeps in its state
// directory (MU_HOME, default ~/.mu). It is the one place those locations are
// spelled out; callers resolve a Paths value once (config.ResolvePaths
// applies the project's cache.path) and pass it around instead of joining
// "~/.mu/..." themselves.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome names the environment variable that relocates mu's whole state
// directory (cache unless cache.path is set, plugins, toolchains,
// credentials, staging, locks). Unset or empty means ~/.mu.
const EnvHome = "MU_HOME"

// Paths are mu's per-user state locations.
type Paths struct {
	Root            string // $MU_HOME, default ~/.mu
	CacheDir        string // local CAS (blobs/, ac/, ingest/); cache.path overrides it
	PluginsDir      string // extracted plugin versions, <name>/bundle-<hex>
	ToolchainsDir   string // binaries extracted from toolchain artifacts
	CredentialsFile string // registry credentials written by `mu login`
}

// Under returns the layout rooted at root (normally $MU_HOME or ~/.mu).
func Under(root string) Paths {
	return Paths{
		Root:            root,
		CacheDir:        filepath.Join(root, "cache"),
		PluginsDir:      filepath.Join(root, "plugins"),
		ToolchainsDir:   filepath.Join(root, "toolchains"),
		CredentialsFile: filepath.Join(root, "credentials.json"),
	}
}

// Default returns the layout under $MU_HOME when it is set, else under
// ~/.mu. A leading ~ in MU_HOME expands to the home directory and a
// relative MU_HOME is made absolute against the working directory.
func Default() (Paths, error) {
	root, err := homeRoot()
	if err != nil {
		return Paths{}, err
	}
	return Under(root), nil
}

func homeRoot() (string, error) {
	if v := os.Getenv(EnvHome); v != "" {
		if v == "~" || strings.HasPrefix(v, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("expanding %s: %w", EnvHome, err)
			}
			v = filepath.Join(home, strings.TrimPrefix(v, "~"))
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("%s: %w", EnvHome, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".mu"), nil
}
