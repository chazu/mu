package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
)

// loginFixture starts an in-memory registry, points HOME at a temp dir, and
// moves the working directory outside any project.
func loginFixture(t *testing.T) (host, credsFile string) {
	t.Helper()
	server := httptest.NewServer(ociserver.New(ocimem.New(), nil))
	t.Cleanup(server.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	return strings.TrimPrefix(server.URL, "http://"), filepath.Join(home, ".mu", "credentials.json")
}

// withStdin runs fn with os.Stdin reading input from a pipe (never a TTY).
func withStdin(t *testing.T, input string, fn func() int) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	original := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = original; r.Close() }()
	return captureBuildCode(t, fn)
}

func TestLoginOutsideProjectWritesAndLogoutRemovesCredentials(t *testing.T) {
	host, creds := loginFixture(t)

	code := withStdin(t, "s3cret\n", func() int {
		return runLogin([]string{"--username", "alice", "--password-stdin", host})
	})
	if code != exitOK {
		t.Fatalf("login exit = %d", code)
	}
	info, err := os.Stat(creds)
	if err != nil {
		t.Fatalf("credentials not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", perm)
	}
	data, _ := os.ReadFile(creds)
	if !strings.Contains(string(data), host) {
		t.Fatalf("credentials lack %s: %s", host, data)
	}

	if code := captureBuildCode(t, func() int { return runLogout([]string{host}) }); code != exitOK {
		t.Fatalf("logout exit = %d", code)
	}
	data, _ = os.ReadFile(creds)
	if strings.Contains(string(data), host) {
		t.Fatalf("logout left %s in credentials: %s", host, data)
	}
}

func TestLoginWithoutPasswordStdinNeedsTTY(t *testing.T) {
	host, creds := loginFixture(t)
	code := withStdin(t, "", func() int { return runLogin([]string{"--username", "alice", host}) })
	if code != exitUsage {
		t.Fatalf("login exit = %d, want %d", code, exitUsage)
	}
	if _, err := os.Stat(creds); !os.IsNotExist(err) {
		t.Fatalf("credentials written without a password (stat err %v)", err)
	}
}

func TestLoginHostRequiredOutsideProject(t *testing.T) {
	loginFixture(t)
	if code := withStdin(t, "pw", func() int { return runLogin([]string{"--username", "a", "--password-stdin"}) }); code != exitUsage {
		t.Fatalf("login without host exit = %d, want %d", code, exitUsage)
	}
	if code := runLogout(nil); code != exitUsage {
		t.Fatalf("logout without host exit = %d, want %d", code, exitUsage)
	}
}

func TestLoginDefaultsToTheProjectsOnlyRegistryHost(t *testing.T) {
	host, creds := loginFixture(t)
	project := t.TempDir()
	cfg := fmt.Sprintf("package mu\ncache: {remote: %q, push: %q}\n", host+"/org/cache", host+"/org/cache")
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	code := withStdin(t, "pw", func() int { return runLogin([]string{"--username", "alice", "--password-stdin"}) })
	if code != exitOK {
		t.Fatalf("login exit = %d", code)
	}
	if data, _ := os.ReadFile(creds); !strings.Contains(string(data), host) {
		t.Fatalf("credentials lack %s: %s", host, data)
	}
}

func TestCacheLoginIsMoved(t *testing.T) {
	for _, sub := range []string{"login", "logout"} {
		if code := runCache([]string{sub, "localhost:1"}); code != exitUsage {
			t.Fatalf("mu cache %s exit = %d, want %d", sub, code, exitUsage)
		}
	}
}
