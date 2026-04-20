package cas

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoreFactory constructs a concrete backend for a named type. Factories
// are registered by backend-type string at init time so higher layers
// (cmd/mu) can build a Store from config without import cycles.
//
// The expansion func converts a user-visible path (possibly with ~ or
// environment variables) to an absolute filesystem path. It is nil for
// non-path backends (e.g. OCI).
type StoreFactory func(spec BackendSpec) (Store, error)

// BackendSpec is the subset of a config.CacheBackend that cas factories
// need, decoupled from the config package to avoid an import cycle.
type BackendSpec struct {
	Type     string
	Path     string // expanded (absolute) when Type == "disk"
	Registry string // raw, when Type == "oci"
}

var backendFactories = map[string]StoreFactory{}

// RegisterBackend installs a factory for a given backend type. Typically
// called from an init() in the package that provides the factory (e.g.
// internal/cas/oci). Re-registering overrides the previous entry.
func RegisterBackend(typ string, f StoreFactory) {
	backendFactories[typ] = f
}

// BuildStore composes a Store from a cache config spec. The caller is
// responsible for path expansion on disk backends (the factory is
// already given absolute paths). If backends is nil/empty AND
// defaultDiskPath is non-empty, a single local disk store at that path
// is returned — matching mu's pre-tiered-cache behaviour.
//
// A single backend returns that backend's Store directly (no Tiered
// wrapper) so the hot path is identical to today. Multiple backends
// compose into a Tiered with ReadRepair/WriteThrough as configured.
func BuildStore(spec BuildSpec) (Store, error) {
	if len(spec.Backends) == 0 {
		if spec.DefaultDiskPath == "" {
			return nil, fmt.Errorf("cas: no cache backends configured and no default disk path")
		}
		f := backendFactories["disk"]
		if f == nil {
			return nil, fmt.Errorf("cas: disk backend factory not registered")
		}
		return f(BackendSpec{Type: "disk", Path: spec.DefaultDiskPath})
	}

	layers := make([]Store, 0, len(spec.Backends))
	names := make([]string, 0, len(spec.Backends))
	policies := make([]LayerPolicy, 0, len(spec.Backends))

	for i, b := range spec.Backends {
		f, ok := backendFactories[b.Type]
		if !ok {
			return nil, fmt.Errorf("cas: unknown backend type %q at index %d", b.Type, i)
		}
		store, err := f(b.BackendSpec)
		if err != nil {
			return nil, fmt.Errorf("cas: backend %d (%s): %w", i, b.Type, err)
		}
		layers = append(layers, store)
		names = append(names, backendName(b))
		policies = append(policies, LayerPolicy{Read: b.Read, Write: b.Write})
	}

	if len(layers) == 1 {
		return layers[0], nil
	}
	return &Tiered{
		Layers:       layers,
		LayerNames:   names,
		Policies:     policies,
		ReadRepair:   spec.ReadRepair,
		WriteThrough: spec.WriteThrough,
		Observer:     spec.Observer,
	}, nil
}

// BuildSpec is the input to BuildStore. The embedded Backends carries
// layer order (0 = nearest); Observer is forwarded to any Tiered store.
type BuildSpec struct {
	Backends        []ResolvedBackend
	ReadRepair      bool
	WriteThrough    bool
	DefaultDiskPath string
	Observer        func(TieredEvent)
}

// ResolvedBackend is BackendSpec plus pre-applied Read/Write policy.
type ResolvedBackend struct {
	BackendSpec
	Read  bool
	Write bool
}

func backendName(b ResolvedBackend) string {
	if b.Type == "disk" {
		return "disk:" + filepath.Base(b.Path)
	}
	if b.Type == "oci" {
		return "oci:" + b.Registry
	}
	return b.Type
}

// ExpandPath expands a leading ~ and any ${VAR} references to an
// absolute filesystem path. Missing environment variables expand to
// empty string (os.ExpandEnv's default). An error is returned if the
// result is not a well-formed path.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding ~: %w", err)
		}
		if p == "~" {
			p = home
		} else if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
	}
	p = os.ExpandEnv(p)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, nil
}
