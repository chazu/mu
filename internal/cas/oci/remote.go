package oci

import (
	"fmt"
	"strings"

	"github.com/chazu/mu/internal/registryhttp"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// NewRemote opens the registry repository ref ("host/namespace/repo") as the
// read side of the build cache (cas.Cache.Remote). Construction does not
// contact the registry. Credentials are resolved through the Docker
// credential chain (~/.docker/config.json + any configured credential
// helpers); anonymous access is fine for public registries.
//
// Registries bound to localhost or 127.0.0.1 automatically use plain HTTP
// so that the common "registry:2 on localhost:5000" dev setup works out
// of the box. All other registries require TLS.
func NewRemote(ref string) (*OCIStore, error) {
	if ref == "" {
		return nil, fmt.Errorf("oci: remote cache requires a repository reference (e.g. ghcr.io/acme/mu-cache)")
	}

	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("oci: parse reference %q: %w", ref, err)
	}

	if isLocalRegistry(ref) {
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
	client := &auth.Client{Cache: auth.NewCache(), Client: registryhttp.NewClient(registryhttp.Options{})}
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
