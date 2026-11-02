package oci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/registryhttp"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// Registry references. Everywhere mu names an OCI registry location — the
// cache remote, the cache push target, the publish base — it is one string,
// "host/namespace/repo" (or "host/namespace" for the publish base), with no
// scheme. The host is everything before the first "/". Registries on
// localhost or 127.0.0.1 are spoken to over plain HTTP; all others use TLS.

// NewRepository opens the repository ref ("host/namespace/repo") with mu's
// registry HTTP client and credentials. It is the one constructor for every
// repository mu talks to: cache remote reads, `mu cache push`, and
// `mu build --publish`. Construction does not contact the registry.
func NewRepository(ref string) (*remote.Repository, error) {
	if ref == "" {
		return nil, errors.New("oci: empty repository reference (want \"host/namespace/repo\")")
	}
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("oci: parse repository reference %q: %w", ref, err)
	}
	repo.PlainHTTP = IsLocalRegistry(ref)
	repo.Client = newAuthClient(true)
	return repo, nil
}

// NewRegistry opens a registry host (no repository) with mu's registry HTTP
// client and no credentials, for `mu cache login` to verify a credential.
func NewRegistry(host string) (*remote.Registry, error) {
	reg, err := remote.NewRegistry(host)
	if err != nil {
		return nil, fmt.Errorf("oci: parse registry %q: %w", host, err)
	}
	reg.PlainHTTP = IsLocalRegistry(host)
	reg.Client = newAuthClient(false)
	return reg, nil
}

// newAuthClient returns a fresh auth client (never the shared
// auth.DefaultClient) over registryhttp's bounded transport, resolving
// credentials through CredentialStore when withCredentials is set. A broken
// credential store degrades to anonymous access, which is enough for public
// registries; a private one then answers 401, reported by AuthError.
func newAuthClient(withCredentials bool) *auth.Client {
	httpClient := registryhttp.NewClient(registryhttp.Options{})
	if os.Getenv("MU_HTTP_TRACE") != "" {
		httpClient.Transport = &tracingTransport{inner: httpClient.Transport}
	}
	client := &auth.Client{Cache: auth.NewCache(), Client: httpClient}
	if withCredentials {
		if store, err := CredentialStore(); err == nil {
			client.Credential = credentials.Credential(store)
		}
	}
	return client
}

// RegistryHost returns the host part of a registry reference.
func RegistryHost(ref string) string {
	host, _, _ := strings.Cut(ref, "/")
	return host
}

// IsLocalRegistry reports whether ref names a registry on localhost or
// 127.0.0.1, which mu speaks to over plain HTTP.
func IsLocalRegistry(ref string) bool {
	host := RegistryHost(ref)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}

// CredentialsPath returns mu's own credentials file, ~/.mu/credentials.json:
// Docker-format JSON that `mu cache login` writes, kept under ~/.mu so it
// never conflicts with ~/.docker and never needs a credential helper binary.
func CredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mu", "credentials.json"), nil
}

// CredentialStore returns the credential store every registry access uses.
// Lookups try ~/.mu/credentials.json first, then the Docker chain
// (~/.docker/config.json and any configured helpers), so existing
// `docker login` / `oras login` sessions keep working. Writes (login,
// logout) go to ~/.mu/credentials.json only. A broken Docker chain (for
// example `credsStore: desktop` without docker-credential-desktop on PATH)
// is skipped rather than failing the lookup.
func CredentialStore() (credentials.Store, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	primary, err := credentials.NewStore(path, credentials.StoreOptions{AllowPlaintextPut: true})
	if err != nil {
		return nil, err
	}
	docker, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return primary, nil
	}
	return credentials.NewStoreWithFallbacks(primary, &safeReadStore{inner: docker}), nil
}

// safeReadStore turns Get errors into empty credentials so a broken fallback
// store cannot fail the whole lookup. NewStoreWithFallbacks never writes to a
// fallback; Put and Delete are forwarded for completeness.
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

// LoginHint is the command that fixes an authentication failure against host.
func LoginHint(host string) string {
	if host == "" {
		host = "<host>"
	}
	return "run: mu cache login " + host
}

// AuthError reports that host refused mu's credentials (HTTP 401 or 403, or
// a challenge mu has no credential for). It matches cas.ErrUnauthorized so
// the build cache reports it instead of treating it as an outage.
type AuthError struct {
	Host string
	Err  error
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("registry %s refused authentication: %v (%s)", e.Host, e.Err, LoginHint(e.Host))
}

func (e *AuthError) Unwrap() []error { return []error{cas.ErrUnauthorized, e.Err} }

// asAuthError wraps err in an AuthError when it is an authentication failure
// against host; any other error is returned unchanged.
func asAuthError(host string, err error) error {
	if err == nil || errors.Is(err, cas.ErrUnauthorized) {
		return err
	}
	var resp *errcode.ErrorResponse
	if errors.As(err, &resp) && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		return &AuthError{Host: host, Err: err}
	}
	if errors.Is(err, auth.ErrBasicCredentialNotFound) {
		return &AuthError{Host: host, Err: err}
	}
	return err
}

// tracingTransport dumps each request/response to stderr when MU_HTTP_TRACE
// is set, with credentials redacted. Intended for debugging only.
type tracingTransport struct {
	inner http.RoundTripper
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dump, err := httputil.DumpRequestOut(redactedRequest(req), false); err == nil {
		fmt.Fprintf(os.Stderr, "--> %s\n", dump)
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if dump, derr := httputil.DumpResponse(redactedResponse(resp), false); derr == nil {
		fmt.Fprintf(os.Stderr, "<-- %s\n", dump)
	}
	return resp, err
}

func redactedHeaders(header http.Header) http.Header {
	copy := header.Clone()
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"} {
		if copy.Get(name) != "" {
			copy.Set(name, "[redacted]")
		}
	}
	return copy
}

func redactedRequest(req *http.Request) *http.Request {
	copy := req.Clone(req.Context())
	copy.Header = redactedHeaders(req.Header)
	return copy
}

func redactedResponse(resp *http.Response) *http.Response {
	copy := *resp
	copy.Header = redactedHeaders(resp.Header)
	return &copy
}
