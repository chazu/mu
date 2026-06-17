package oci

import (
	"fmt"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
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

// buildOCIStore constructs a Store backed by a remote OCI registry.
// Credentials are resolved through the Docker credential chain
// (~/.docker/config.json + any configured credential helpers). Anonymous
// access is fine for public registries.
//
// Registries bound to localhost or 127.0.0.1 automatically use plain HTTP
// so that the common "registry:2 on localhost:5000" dev setup works out
// of the box. All other registries require TLS.
func buildOCIStore(spec cas.BackendSpec) (cas.Store, error) {
	if spec.Registry == "" {
		return nil, fmt.Errorf("oci backend requires a registry reference (e.g. ghcr.io/acme/mu-cache)")
	}

	repo, err := remote.NewRepository(spec.Registry)
	if err != nil {
		return nil, fmt.Errorf("oci: parse reference %q: %w", spec.Registry, err)
	}

	if isLocalRegistry(spec.Registry) {
		repo.PlainHTTP = true
	}

	credStore, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		// Missing / unreadable Docker config is not fatal — anonymous reads
		// still work for public registries.
		credStore = nil
	}
	// Construct a per-repository Client so we don't mutate auth.DefaultClient
	// (which is a shared package-level singleton).
	client := &auth.Client{Cache: auth.NewCache()}
	if credStore != nil {
		client.Credential = credentials.Credential(credStore)
	}
	repo.Client = client

	return New(repo), nil
}

func isLocalRegistry(ref string) bool {
	host := ref
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		host = ref[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}
