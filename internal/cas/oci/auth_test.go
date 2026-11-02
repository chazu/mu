package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/chazu/mu/internal/cas"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	testUser  = "builder"
	testPass  = "s3cret"
	testToken = "good-token"
)

// bearerRegistry serves one in-memory registry twice: open (for seeding) and
// behind token auth, the way registries like ghcr.io and akashic do. The
// token endpoint hands out testToken for testUser:testPass basic auth.
type bearerRegistry struct {
	openHost, authHost string
	authRequests       atomic.Int32
}

func newBearerRegistry(t *testing.T) *bearerRegistry {
	t.Helper()
	backend := ociserver.New(ocimem.New(), nil)
	open := httptest.NewServer(backend)
	t.Cleanup(open.Close)

	br := &bearerRegistry{openHost: strings.TrimPrefix(open.URL, "http://")}
	var authURL string
	authed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		br.authRequests.Add(1)
		if r.URL.Path == "/token" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != testUser || pass != testPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": testToken})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+authURL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(authed.Close)
	authURL = authed.URL
	br.authHost = strings.TrimPrefix(authed.URL, "http://")
	return br
}

// isolateCredentials points HOME and DOCKER_CONFIG at empty temp directories
// so no real credential store is read or written.
func isolateCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_CONFIG", filepath.Join(home, ".docker"))
	return home
}

func writeMuCredentials(t *testing.T, home, host, user, pass string) {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	data, _ := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err := os.MkdirAll(filepath.Join(home, ".mu"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".mu", "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedResult stores one action result (and its output blob) in the registry
// through the unauthenticated endpoint.
func seedResult(t *testing.T, host string) (cas.ActionKey, cas.Digest) {
	t.Helper()
	ctx := context.Background()
	local, err := cas.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := local.Put(ctx, strings.NewReader("cached output"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := remote.NewRepository(host + "/acme/cache")
	if err != nil {
		t.Fatal(err)
	}
	repo.PlainHTTP = true
	key := cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("d", 64))}
	result := &cas.ActionResult{Version: cas.ActionResultVersion, Outputs: map[string]cas.Digest{"out": out}, OutputModes: map[string]uint32{"out": 0o644}}
	if err := New(repo).PushActionResult(ctx, local, key, result); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return key, out
}

func TestRemoteCacheReadsUseMuCredentials(t *testing.T) {
	home := isolateCredentials(t)
	reg := newBearerRegistry(t)
	key, out := seedResult(t, reg.openHost)
	writeMuCredentials(t, home, reg.authHost, testUser, testPass)

	remoteStore, err := NewRemote(reg.authHost + "/acme/cache")
	if err != nil {
		t.Fatal(err)
	}
	local, err := cas.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &cas.Cache{Local: local, Remote: remoteStore, Warn: func(err error) { t.Errorf("remote latched off: %v", err) }}
	result, err := c.GetActionResult(context.Background(), key)
	if err != nil || result == nil || result.Outputs["out"] != out {
		t.Fatalf("GetActionResult = %+v, %v", result, err)
	}
	rc, err := c.Get(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if data, err := io.ReadAll(rc); err != nil || string(data) != "cached output" {
		t.Fatalf("blob = %q, %v", data, err)
	}
}

func TestRemoteCacheAuthFailureIsAnErrorWithLoginHint(t *testing.T) {
	for name, creds := range map[string][2]string{
		"anonymous":      {},
		"wrong password": {testUser, "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			home := isolateCredentials(t)
			reg := newBearerRegistry(t)
			key, out := seedResult(t, reg.openHost)
			if creds[0] != "" {
				writeMuCredentials(t, home, reg.authHost, creds[0], creds[1])
			}
			remoteStore, err := NewRemote(reg.authHost + "/acme/cache")
			if err != nil {
				t.Fatal(err)
			}
			local, err := cas.NewDiskStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := &cas.Cache{Local: local, Remote: remoteStore, Warn: func(err error) { t.Errorf("auth failure latched as an outage: %v", err) }}

			for attempt := 0; attempt < 2; attempt++ {
				before := reg.authRequests.Load()
				_, err = c.GetActionResult(context.Background(), key)
				if !errors.Is(err, cas.ErrUnauthorized) {
					t.Fatalf("attempt %d: err = %v, want cas.ErrUnauthorized", attempt, err)
				}
				t.Logf("error: %v", err)
				if want := "run: mu login " + reg.authHost; !strings.Contains(err.Error(), want) {
					t.Fatalf("attempt %d: error %q lacks hint %q", attempt, err, want)
				}
				if reg.authRequests.Load() == before {
					t.Fatalf("attempt %d: remote was not consulted (latched off)", attempt)
				}
			}
			if _, err := c.Get(context.Background(), out); !errors.Is(err, cas.ErrUnauthorized) {
				t.Fatalf("Get err = %v, want cas.ErrUnauthorized", err)
			}
		})
	}
}
