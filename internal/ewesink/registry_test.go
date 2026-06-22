package ewesink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/ewe"
)

// TestEndToEndPopulator exercises the full ewe-on-mu stack against a stub HTTP
// server: arg resolution + secret reveal (auth block AND whole-value ref) +
// #HttpAll fetch + comprehension build + #HttpBatch fan-out + indexed join +
// #WriteFile to MU_OUT. This is the example-5 core (GitLab governance) with no
// external infra.
//
// NOTE: records here self-tag with a regular `schema` field, not hidden
// `_schema`, because CUE's json.Marshal silently drops hidden fields — the
// populate output-format question is resolved on the body-kind/ingest path.
func TestEndToEndPopulator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos":
			_ = json.NewEncoder(w).Encode([]any{
				map[string]any{"id": 1, "name": "a", "default_branch": "main"},
				map[string]any{"id": 2, "name": "b", "default_branch": "dev"},
			})
		case "/repos/1/branches":
			_ = json.NewEncoder(w).Encode([]any{"main", "x"})
		case "/repos/2/branches":
			_ = json.NewEncoder(w).Encode([]any{"dev"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	reg, err := NewRegistry(Deps{
		Client: srv.Client(),
		Reveal: fakeReveal(map[string]string{"TOK": "tok123"}),
		MuOut:  tmp,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	prog := strings.ReplaceAll(`import "op"

_env: op.#Env & { args: [] }
_tok: op.#Secret & { args: ["TOK"] }
_repos: op.#HttpAll & { args: [{
	url:      "SRV/repos"
	auth:     { header: { name: "PRIVATE-TOKEN", ref: "TOK" } }
	paginate: { style: "none" }
}] }
_reqs: [ for r in _repos.result {
	url:     "SRV/repos/\(r.id)/branches"
	headers: { "PRIVATE-TOKEN": _tok.result }
} ]
_branches: op.#HttpBatch & { args: [_reqs] }
_out: [ for i, r in _repos.result {
	name:          r.name
	default_branch: r.default_branch
	branch_count:  len(_branches.result[i].value)
	schema:        "git.repository.gitlab"
} ]
write: op.#WriteFile & { args: ["\(_env.result.MU_OUT)/repos.json", _out] }
`, "SRV", srv.URL)

	if _, err := ewe.NewProcessor(reg).ProcessSource(context.Background(), prog); err != nil {
		t.Fatalf("ProcessSource: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "repos.json"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2: %s", len(records), raw)
	}
	if records[0]["name"] != "a" || records[0]["default_branch"] != "main" {
		t.Errorf("record 0 = %v", records[0])
	}
	if bc, _ := records[0]["branch_count"].(float64); bc != 2 {
		t.Errorf("record 0 branch_count = %v, want 2", records[0]["branch_count"])
	}
	if bc, _ := records[1]["branch_count"].(float64); bc != 1 {
		t.Errorf("record 1 branch_count = %v, want 1", records[1]["branch_count"])
	}
	if records[0]["schema"] != "git.repository.gitlab" {
		t.Errorf("record 0 missing schema tag: %v", records[0])
	}
	// The token never appears in the output file.
	if strings.Contains(string(raw), "tok123") {
		t.Error("output file leaked the revealed token")
	}
}
