package oci

import (
	"fmt"

	"github.com/chau/mu/internal/cas"
)

func init() {
	cas.RegisterBackend("disk", buildDiskStore)
	cas.RegisterBackend("oci", buildOCIStore)
}

func buildDiskStore(spec cas.BackendSpec) (cas.Store, error) {
	if spec.Path == "" {
		return nil, fmt.Errorf("disk backend requires a path")
	}
	return NewLocal(spec.Path)
}

// buildOCIStore is a placeholder for remote OCI registry support. The
// wiring (oras-go remote.Repository + Docker credential chain) is not
// in this PR — the tiered-cache composition lands first so the code
// structure, tests, and disk-to-disk composition are usable today.
// When remote support ships, only this function changes.
func buildOCIStore(spec cas.BackendSpec) (cas.Store, error) {
	return nil, fmt.Errorf("oci backend %q: remote OCI cache backends are not yet implemented (disk-to-disk tiering works; see docs/plans/2026-04-19-feat-tiered-cache-composition-plan.md)", spec.Registry)
}
