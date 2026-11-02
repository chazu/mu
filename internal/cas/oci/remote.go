package oci

import (
	"fmt"
)

// NewRemote opens the registry repository ref ("host/namespace/repo") as the
// read side of the build cache (cas.Cache.Remote). Construction does not
// contact the registry. Credentials come from CredentialStore
// (~/.mu/credentials.json, then the Docker chain); anonymous access is fine
// for public registries. An authentication failure is reported as an
// AuthError carrying a login hint.
func NewRemote(ref string) (*OCIStore, error) {
	if ref == "" {
		return nil, fmt.Errorf("oci: remote cache requires a repository reference (e.g. ghcr.io/acme/mu-cache)")
	}
	repo, err := NewRepository(ref)
	if err != nil {
		return nil, err
	}
	return New(repo), nil
}
