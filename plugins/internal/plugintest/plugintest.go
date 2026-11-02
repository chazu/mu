// Package plugintest holds test helpers shared by the in-tree Go plugins.
//
// Its main job is the bb -> Go parity check used while flipping a plugin's
// entrypoint from plugin.bb to its Go port: the bb plugin's responses to a
// fixed request script are captured once into testdata, and the Go port
// must answer the same script with semantically equal responses.
package plugintest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/chazu/mu/sdk/muplugin"
)

// Normalize strips the wire-shape noise that differs between JSON encoders
// without changing meaning to the plugin protocol's consumers: null, "",
// false, [] and {} values are dropped from objects (recursively). Key order
// is irrelevant because values are compared as decoded maps.
//
// Callers must not rely on Normalize for fields where emptiness is
// meaningful (for example an action's "env": {} means "clean environment"
// while an absent env lets an impure action inherit mu's environment);
// assert those explicitly.
func Normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			n := Normalize(val)
			if isEmpty(n) {
				continue
			}
			out[k] = n
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Normalize(val)
		}
		return out
	default:
		return v
	}
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// ReadNDJSON decodes every non-blank line of path as a JSON object.
func ReadNDJSON(t testing.TB, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("%s: decode line %d: %v", path, len(out)+1, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

// Parity sends each request in requestsPath (NDJSON) through p, in order,
// and compares each response with the matching line of goldenPath (the bb
// plugin's captured responses) after Normalize. Requests run sequentially
// against the same process state, so stateful scripts (store then resolve)
// work. adjust, if non-nil, may rewrite a decoded golden response before
// comparison to account for a documented, intentional deviation.
func Parity(t *testing.T, p *muplugin.Plugin, requestsPath, goldenPath string, adjust func(i int, golden map[string]any)) {
	t.Helper()
	requests := ReadNDJSON(t, requestsPath)
	golden := ReadNDJSON(t, goldenPath)
	if len(requests) != len(golden) {
		t.Fatalf("%d requests but %d golden responses", len(requests), len(golden))
	}
	for i, raw := range requests {
		reqBytes, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var req muplugin.Request
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		got, err := muplugin.Exchange(context.Background(), p, req)
		if err != nil {
			t.Fatalf("request %d (%s): %v", i+1, reqBytes, err)
		}
		want := golden[i]
		if adjust != nil {
			adjust(i, want)
		}
		if g, w := Normalize(got), Normalize(want); !reflect.DeepEqual(g, w) {
			gj, _ := json.MarshalIndent(g, "", "  ")
			wj, _ := json.MarshalIndent(w, "", "  ")
			t.Errorf("request %d %s\n got (Go): %s\nwant (bb): %s", i+1, reqBytes, gj, wj)
		}
	}
}
