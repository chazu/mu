package pithvm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/pith"
)

// run registers exec drivers with the given env/sealedNames and runs prog.
func run(t *testing.T, env map[string]string, sealed map[string]bool, prog []any) (*pith.VM, error) {
	t.Helper()
	vm := pith.New(context.Background())
	RegisterExecDrivers(vm, env, sealed, nil, nil)
	return vm, vm.Run(prog)
}

// --- secret/get ---

func TestSecretGetReturnsTainted(t *testing.T) {
	env := map[string]string{"TOKEN": "glpat-xyz"}
	sealed := map[string]bool{"TOKEN": true}
	vm, err := run(t, env, sealed, []any{"'TOKEN", "secret/get"})
	if err != nil {
		t.Fatal(err)
	}
	top, _ := vm.Result()
	s, ok := top.(pith.Secret)
	if !ok {
		t.Fatalf("secret/get should push a pith.Secret, got %T", top)
	}
	if s.Inner() != "glpat-xyz" {
		t.Errorf("inner = %v", s.Inner())
	}
}

func TestSecretGetRejectsNonSealed(t *testing.T) {
	env := map[string]string{"FOO": "bar"}
	_, err := run(t, env, map[string]bool{}, []any{"'FOO", "secret/get"})
	if err == nil || !strings.Contains(err.Error(), "not a declared sealed_input") {
		t.Errorf("expected non-sealed rejection, got %v", err)
	}
}

func TestSecretGetUnresolvedFailsLoud(t *testing.T) {
	// Declared sealed but absent from env => resolution failed.
	_, err := run(t, map[string]string{}, map[string]bool{"TOKEN": true}, []any{"'TOKEN", "secret/get"})
	if err == nil || !strings.Contains(err.Error(), "did not resolve") {
		t.Errorf("expected loud resolution failure, got %v", err)
	}
}

// --- env/get and env/get-default ---

func TestEnvGetRefusesSealed(t *testing.T) {
	env := map[string]string{"TOKEN": "glpat-xyz"}
	sealed := map[string]bool{"TOKEN": true}
	_, err := run(t, env, sealed, []any{"'TOKEN", "env/get"})
	if err == nil || !strings.Contains(err.Error(), "use secret/get") {
		t.Errorf("env/get must refuse a sealed name, got %v", err)
	}
}

func TestEnvGetErrorsOnMiss(t *testing.T) {
	_, err := run(t, map[string]string{}, map[string]bool{}, []any{"'NOPE", "env/get"})
	if err == nil || !strings.Contains(err.Error(), "not set") {
		t.Errorf("env/get should error on miss, got %v", err)
	}
}

func TestEnvGetDefault(t *testing.T) {
	vm, err := run(t, map[string]string{}, map[string]bool{}, []any{"'REGION", "'us-east-1", "env/get-default"})
	if err != nil {
		t.Fatal(err)
	}
	if top, _ := vm.Result(); top != "us-east-1" {
		t.Errorf("default = %v, want us-east-1", top)
	}
}

func TestEnvGetDefaultRefusesSealed(t *testing.T) {
	sealed := map[string]bool{"TOKEN": true}
	_, err := run(t, map[string]string{}, sealed, []any{"'TOKEN", "'fallback", "env/get-default"})
	if err == nil || !strings.Contains(err.Error(), "cannot be defaulted") {
		t.Errorf("env/get-default must refuse a sealed name, got %v", err)
	}
}

// --- http/request: reveals real header at the wire ---

func TestHTTPRequestRevealsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	env := map[string]string{"TOKEN": "glpat-secret"}
	sealed := map[string]bool{"TOKEN": true}
	// build {url, headers:{PRIVATE-TOKEN: secret}} then request
	prog := []any{
		map[string]any{"url": srv.URL},
		"'headers",
		map[string]any{}, "'PRIVATE-TOKEN", []any{"'TOKEN", "secret/get"}, "apply", "set",
		"set",
		"http/request",
	}
	vm, err := run(t, env, sealed, prog)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "glpat-secret" {
		t.Errorf("server saw PRIVATE-TOKEN=%q, want the revealed secret", gotAuth)
	}
	res, _ := vm.Result()
	m, ok := res.(map[string]any)
	if !ok || m["ok"] != true {
		t.Errorf("response not decoded: %v", res)
	}
}

func TestHTTPRequestNon2xxErrorsWithoutLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	env := map[string]string{"TOKEN": "glpat-secret"}
	sealed := map[string]bool{"TOKEN": true}
	prog := []any{
		map[string]any{"url": srv.URL},
		"'headers",
		map[string]any{}, "'PRIVATE-TOKEN", []any{"'TOKEN", "secret/get"}, "apply", "set",
		"set",
		"http/request",
	}
	_, err := run(t, env, sealed, prog)
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
	if strings.Contains(err.Error(), "glpat-secret") {
		t.Errorf("error leaked the token: %v", err)
	}
}

// --- file/write: confinement + 0600 + reveal ---

func TestFileWriteToSealedOutDir(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"MU_SEALED_OUT_DIR": dir, "TOKEN": "glpat-secret"}
	sealed := map[string]bool{"TOKEN": true}
	target := filepath.Join(dir, "RESULT")
	// Build the path in-program: $MU_SEALED_OUT_DIR + "/RESULT". (A bare absolute
	// string in program position would be dispatched as a word, not pushed.)
	prog := []any{
		"'MU_SEALED_OUT_DIR", "env/get", "'/RESULT", "concat",
		[]any{"'TOKEN", "secret/get"}, "apply",
		"file/write",
	}
	if _, err := run(t, env, sealed, prog); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "glpat-secret" {
		t.Errorf("file content = %q, want the revealed secret", string(data))
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestFileWriteRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"MU_SEALED_OUT_DIR": dir}
	// $MU_SEALED_OUT_DIR + "/../../etc/evil" escapes the root.
	prog := []any{
		"'MU_SEALED_OUT_DIR", "env/get", "'/../../etc/evil", "concat",
		"'pwned", "file/write",
	}
	_, err := run(t, env, map[string]bool{}, prog)
	if err == nil || !strings.Contains(err.Error(), "escapes the sanctioned") {
		t.Errorf("expected path-escape rejection, got %v", err)
	}
}

func TestFileWriteNoRootRejected(t *testing.T) {
	// Absolute path, but no sanctioned roots in env.
	prog := []any{"'/tmp/anywhere", "'x", "file/write"}
	_, err := run(t, map[string]string{}, map[string]bool{}, prog)
	if err == nil || !strings.Contains(err.Error(), "no sanctioned write root") {
		t.Errorf("expected no-root rejection, got %v", err)
	}
}

// --- format/json taints output when input contains a secret ---

func TestFormatJSONTaintsAndReveals(t *testing.T) {
	env := map[string]string{"TOKEN": "glpat-secret"}
	sealed := map[string]bool{"TOKEN": true}
	// {tok: secret} format/json
	prog := []any{
		map[string]any{}, "'tok", []any{"'TOKEN", "secret/get"}, "apply", "set",
		"format/json",
	}
	vm, err := run(t, env, sealed, prog)
	if err != nil {
		t.Fatal(err)
	}
	top, _ := vm.Result()
	s, ok := top.(pith.Secret)
	if !ok {
		t.Fatalf("format/json of a secret-containing struct should be tainted, got %T", top)
	}
	// The inner string must contain the REAL token (valid JSON), not the marker.
	inner, _ := s.Inner().(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		t.Fatalf("inner is not valid JSON: %v", err)
	}
	if parsed["tok"] != "glpat-secret" {
		t.Errorf("JSON should inline the real token, got %v", parsed["tok"])
	}
}

func TestFormatJSONCleanStaysPlain(t *testing.T) {
	prog := []any{map[string]any{"a": "b"}, "format/json"}
	vm, err := run(t, map[string]string{}, map[string]bool{}, prog)
	if err != nil {
		t.Fatal(err)
	}
	if top, _ := vm.Result(); !isString(top) {
		t.Errorf("clean format/json should be a plain string, got %T", top)
	}
}

func isString(v any) bool { _, ok := v.(string); return ok }

// --- trace redaction end to end (pith renders Secret as marker) ---

func TestTraceRedactsThroughExecDrivers(t *testing.T) {
	var buf bytes.Buffer
	vm := pith.NewWithTrace(context.Background(), &buf)
	env := map[string]string{"TOKEN": "glpat-secret"}
	RegisterExecDrivers(vm, env, map[string]bool{"TOKEN": true}, nil, nil)
	if err := vm.Run([]any{"'TOKEN", "secret/get"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "glpat-secret") {
		t.Errorf("trace leaked secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), pith.RedactedMarker) {
		t.Errorf("trace should show redaction marker, got: %s", buf.String())
	}
}
