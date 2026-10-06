package sandbox

import (
	"context"
	"github.com/chazu/mu/internal/cas"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeSandboxAcceptance(t *testing.T) {
	helper := os.Getenv("MU_NATIVE_HELPER")
	if helper == "" {
		if os.Getenv("MU_REQUIRE_NATIVE_SANDBOX") == "1" {
			t.Fatal("MU_NATIVE_HELPER must name the static acceptance executable")
		}
		t.Skip("build ./internal/sandbox/testdata/native-helper and set MU_NATIVE_HELPER")
	}
	store := newTestStore(t)
	sb, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Cleanup()
	expected := IsolationSeatbelt
	if runtime.GOOS == "linux" {
		expected = IsolationNamespace
	}
	if sb.Level() != expected {
		t.Fatalf("native isolation unavailable: got %v, want %v", sb.Level(), expected)
	}
	f, err := os.Open(helper)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.Put(context.Background(), f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.UnpackToolchain(context.Background(), map[string]cas.Digest{"bin/native-helper": digest}); err != nil {
		t.Fatal(err)
	}
	run := func(network bool, args ...string) {
		t.Helper()
		code, err := sb.Exec(context.Background(), append([]string{"native-helper"}, args...), map[string]string{}, network)
		if err != nil || code != 0 {
			t.Fatalf("native %v: code=%d, err=%v", args, code, err)
		}
	}
	run(false, "work")
	if data, err := os.ReadFile(sb.OutputPath("native-output")); err != nil || string(data) != "ok" {
		t.Fatalf("native cwd/write: %q, %v", data, err)
	}
	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("host sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(false, "read-denied", outside)
	run(false, "write-denied", outside)
	if data, err := os.ReadFile(outside); err != nil || string(data) != "host sentinel" {
		t.Fatalf("host file changed: %q, %v", data, err)
	}
	if runtime.GOOS == "linux" {
		run(false, "write-denied", "/root-write-must-fail")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	run(false, "network", listener.Addr().String(), "deny")
	run(true, "network", listener.Addr().String(), "allow")
}
