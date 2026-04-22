package main

import (
	"context"
	"os"
	"path/filepath"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// muCredentialsPath returns the path to mu's own credentials file. We use a
// plain Docker-format JSON so ORAS's FileStore can read/write it directly,
// but the location is under ~/.mu so it never conflicts with ~/.docker and
// never requires a credential helper binary.
func muCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mu", "credentials.json"), nil
}

// openCredentialStore returns a credential store that:
//   - writes to ~/.mu/credentials.json (plaintext JSON, no Docker Desktop
//     or other credential helper required);
//   - reads from that file first, then falls back to the standard Docker
//     chain (~/.docker/config.json + any helpers) so users who are already
//     logged in via `docker login` / `oras login` still get picked up.
//
// If the Docker chain is broken at call time (e.g. `credsStore: desktop`
// is configured but `docker-credential-desktop` is not on PATH), the
// fallback is silently skipped so mu can still use its own file.
func openCredentialStore() (credentials.Store, error) {
	path, err := muCredentialsPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	primary, err := credentials.NewStore(path, credentials.StoreOptions{
		AllowPlaintextPut: true,
	})
	if err != nil {
		return nil, err
	}

	docker, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return primary, nil
	}
	return credentials.NewStoreWithFallbacks(primary, &safeReadStore{inner: docker}), nil
}

// safeReadStore wraps a credentials.Store so Get errors become empty
// credentials. This lets us include the Docker chain as a fallback without
// a broken credsStore (e.g. Docker Desktop helper missing) failing the
// whole lookup. Put/Delete are never called on a fallback store by
// NewStoreWithFallbacks, but we forward them for completeness.
type safeReadStore struct {
	inner credentials.Store
}

func (s *safeReadStore) Get(ctx context.Context, serverAddress string) (auth.Credential, error) {
	cred, err := s.inner.Get(ctx, serverAddress)
	if err != nil {
		return auth.EmptyCredential, nil
	}
	return cred, nil
}

func (s *safeReadStore) Put(ctx context.Context, serverAddress string, cred auth.Credential) error {
	return s.inner.Put(ctx, serverAddress, cred)
}

func (s *safeReadStore) Delete(ctx context.Context, serverAddress string) error {
	return s.inner.Delete(ctx, serverAddress)
}
