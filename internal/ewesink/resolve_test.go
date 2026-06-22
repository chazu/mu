package ewesink

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"testing"
)

// fakeReveal resolves names from a fixed map; unknown names error.
func fakeReveal(vals map[string]string) RevealFunc {
	return func(name string) (string, error) {
		v, ok := vals[name]
		if !ok {
			return "", fmt.Errorf("no such secret %q", name)
		}
		return v, nil
	}
}

// Test 5: Layer 1 whole-value reference revealed at the sink.
func TestResolveLayer1WholeValue(t *testing.T) {
	reveal := fakeReveal(map[string]string{"X": "tok123"})
	in := map[string]any{
		"headers": map[string]any{
			"PRIVATE-TOKEN": map[string]any{"$secret": "X"},
		},
	}
	got, err := resolveSecrets(in, reveal)
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	want := map[string]any{"headers": map[string]any{"PRIVATE-TOKEN": "tok123"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Test 6: Layer 2 single-ref template.
func TestResolveLayer2Single(t *testing.T) {
	reveal := fakeReveal(map[string]string{"X": "tok123"})
	in := map[string]any{"$secretTemplate": "Bearer {}", "refs": []any{"X"}}
	got, err := resolveSecrets(in, reveal)
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	if got != "Bearer tok123" {
		t.Errorf("got %q, want %q", got, "Bearer tok123")
	}
}

// Test 7: Layer 2 multi-ref + encode base64.
func TestResolveLayer2MultiBase64(t *testing.T) {
	reveal := fakeReveal(map[string]string{"U": "alice", "P": "s3cr3t"})
	in := map[string]any{"$secretTemplate": "{}:{}", "refs": []any{"U", "P"}, "encode": "base64"}
	got, err := resolveSecrets(in, reveal)
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Test 8: Layer 3 — each auth scheme lowers + resolves correctly.
func TestResolveLayer3AuthSchemes(t *testing.T) {
	reveal := fakeReveal(map[string]string{"TOK": "tok123", "U": "alice", "P": "pw"})

	cases := []struct {
		name       string
		auth       map[string]any
		wantHeader map[string]string // header name -> value
		wantQuery  map[string]string // query param -> value
	}{
		{
			name:       "bearer",
			auth:       map[string]any{"bearer": map[string]any{"ref": "TOK"}},
			wantHeader: map[string]string{"Authorization": "Bearer tok123"},
		},
		{
			name:       "header_default_template",
			auth:       map[string]any{"header": map[string]any{"name": "PRIVATE-TOKEN", "ref": "TOK"}},
			wantHeader: map[string]string{"PRIVATE-TOKEN": "tok123"},
		},
		{
			name:       "header_custom_template",
			auth:       map[string]any{"header": map[string]any{"name": "X-Auth", "ref": "TOK", "template": "token {}"}},
			wantHeader: map[string]string{"X-Auth": "token tok123"},
		},
		{
			name:      "query",
			auth:      map[string]any{"query": map[string]any{"param": "private_token", "ref": "TOK"}},
			wantQuery: map[string]string{"private_token": "tok123"},
		},
		{
			name: "basic_userref",
			auth: map[string]any{"basic": map[string]any{"userRef": "U", "passRef": "P"}},
			wantHeader: map[string]string{
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pw")),
			},
		},
		{
			name: "basic_literal_user",
			auth: map[string]any{"basic": map[string]any{"user": "svc", "passRef": "P"}},
			wantHeader: map[string]string{
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("svc:pw")),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]any{}
			query := map[string]any{}
			if err := lowerAuth(tc.auth, headers, query); err != nil {
				t.Fatalf("lowerAuth: %v", err)
			}
			rh, err := resolveSecrets(headers, reveal)
			if err != nil {
				t.Fatalf("resolve headers: %v", err)
			}
			rq, err := resolveSecrets(query, reveal)
			if err != nil {
				t.Fatalf("resolve query: %v", err)
			}
			for k, want := range tc.wantHeader {
				if got := rh.(map[string]any)[k]; got != want {
					t.Errorf("header %q = %v, want %q", k, got, want)
				}
			}
			for k, want := range tc.wantQuery {
				if got := rq.(map[string]any)[k]; got != want {
					t.Errorf("query %q = %v, want %q", k, got, want)
				}
			}
		})
	}
}

// Test 8b: auth with zero or multiple schemes is rejected.
func TestLowerAuthSchemeCount(t *testing.T) {
	if err := lowerAuth(map[string]any{}, map[string]any{}, map[string]any{}); err == nil {
		t.Error("expected error for no scheme")
	}
	two := map[string]any{
		"bearer": map[string]any{"ref": "A"},
		"query":  map[string]any{"param": "p", "ref": "A"},
	}
	if err := lowerAuth(two, map[string]any{}, map[string]any{}); err == nil {
		t.Error("expected error for multiple schemes")
	}
}

// Test 9: a nested ref inside a body struct resolves; a ref already frozen into
// JSON text (the json.Marshal-in-CUE gotcha) does NOT resolve — it stays the
// literal string, documenting the authoring trap as a by-design assertion.
func TestResolveNestedBodyAndFrozenGotcha(t *testing.T) {
	reveal := fakeReveal(map[string]string{"X": "tok123"})

	body := map[string]any{"auth": map[string]any{"token": map[string]any{"$secret": "X"}}}
	got, err := resolveSecrets(body, reveal)
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	if got.(map[string]any)["auth"].(map[string]any)["token"] != "tok123" {
		t.Errorf("nested ref not revealed: %v", got)
	}

	// Frozen: the ref was json.Marshal'd in CUE, so it arrives as literal text.
	frozen := map[string]any{"body": `{"token":{"$secret":"X"}}`}
	gotFrozen, err := resolveSecrets(frozen, reveal)
	if err != nil {
		t.Fatalf("resolveSecrets frozen: %v", err)
	}
	// It stays the literal string — the ref is shipped, not the token (the bug).
	if gotFrozen.(map[string]any)["body"] != `{"token":{"$secret":"X"}}` {
		t.Errorf("frozen body unexpectedly altered: %v", gotFrozen)
	}
}

// Test 10: a ref that never reaches a sink stays an inert tag (fail-closed) —
// only resolveSecrets reveals, and #Secret itself returns the tag, never a value.
func TestSecretRefInertWithoutSink(t *testing.T) {
	fn := SecretFunc("#Secret")
	res, err := fn.Execute(context.Background(), []any{"GITLAB_TOKEN"})
	if err != nil {
		t.Fatalf("#Secret: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["$secret"] != "GITLAB_TOKEN" {
		t.Fatalf("#Secret result = %v, want {$secret: GITLAB_TOKEN}", res)
	}
	if len(m) != 1 {
		t.Errorf("#Secret tag has extra fields: %v", m)
	}
	// Without a resolveSecrets pass the value is never present anywhere.
}
