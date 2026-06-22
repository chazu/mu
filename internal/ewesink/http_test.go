package ewesink

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func httpAll(t *testing.T, srv *httptest.Server, reveal RevealFunc, req map[string]any) (any, error) {
	t.Helper()
	fn := HTTPAllFunc("#HttpAll", Deps{Client: srv.Client(), Reveal: reveal})
	return fn.Execute(context.Background(), []any{req})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Test 1: page + stop:"empty" — 3 full pages then an empty page.
func TestHttpAllPageEmpty(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 3 {
			writeJSON(w, []any{fmt.Sprintf("p%d-a", page), fmt.Sprintf("p%d-b", page)})
		} else {
			writeJSON(w, []any{})
		}
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url":      srv.URL,
		"paginate": map[string]any{"style": "page", "param": "page", "start": 1},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	items := got.([]any)
	if len(items) != 6 {
		t.Errorf("got %d items, want 6", len(items))
	}
	if reqs != 4 {
		t.Errorf("made %d requests, want 4", reqs)
	}
}

// Test 2: page + sizeParam + stop:"partial" — stops on the first short page.
func TestHttpAllPagePartial(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 1 {
			writeJSON(w, []any{"a", "b"})
		} else {
			writeJSON(w, []any{"c"}) // short page (< per_page=2)
		}
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url":   srv.URL,
		"query": map[string]any{"per_page": 2},
		"paginate": map[string]any{
			"style": "page", "param": "page", "start": 1,
			"sizeParam": "per_page", "stop": "partial",
		},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 3 {
		t.Errorf("got %d items, want 3", len(got.([]any)))
	}
	if reqs != 2 {
		t.Errorf("made %d requests, want 2", reqs)
	}
}

// Test 3: link — follows rel="next" across pages, stops when dropped.
func TestHttpAllLink(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, srv.URL, page+1))
		}
		writeJSON(w, []any{fmt.Sprintf("p%d", page)})
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url":      srv.URL,
		"paginate": map[string]any{"style": "link"},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 3 {
		t.Errorf("got %d items, want 3", len(got.([]any)))
	}
}

// Test 4: cursor + nextPath — feeds the token, stops when nextPath is null.
func TestHttpAllCursorNextPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("starting_after")
		switch after {
		case "":
			writeJSON(w, map[string]any{"items": []any{"a"}, "meta": map[string]any{"next": "c1"}})
		case "c1":
			writeJSON(w, map[string]any{"items": []any{"b"}, "meta": map[string]any{"next": nil}})
		default:
			writeJSON(w, map[string]any{"items": []any{}, "meta": map[string]any{"next": nil}})
		}
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url": srv.URL,
		"paginate": map[string]any{
			"style": "cursor", "cursorParam": "starting_after",
			"nextPath": "meta.next", "itemsPath": "items",
		},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 2 {
		t.Errorf("got %d items, want 2", len(got.([]any)))
	}
}

// Test 5: cursor + hasMorePath:false — stops on the boolean gate.
func TestHttpAllCursorHasMore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("cur")
		if after == "" {
			writeJSON(w, map[string]any{"data": []any{"a"}, "has_more": true, "cursor": "n1"})
		} else {
			writeJSON(w, map[string]any{"data": []any{"b"}, "has_more": false, "cursor": "n2"})
		}
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url": srv.URL,
		"paginate": map[string]any{
			"style": "cursor", "cursorParam": "cur",
			"nextPath": "cursor", "hasMorePath": "has_more", "itemsPath": "data",
		},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 2 {
		t.Errorf("got %d items, want 2", len(got.([]any)))
	}
}

// Test 6: itemsPath — items nested under "data" are extracted.
func TestHttpAllItemsPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{"x", "y", "z"}})
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url":      srv.URL,
		"paginate": map[string]any{"style": "none", "itemsPath": "data"},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 3 {
		t.Errorf("got %d items, want 3", len(got.([]any)))
	}
}

// Test 7: maxPages — a never-terminating stub hits the cap → error.
func TestHttpAllMaxPagesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{"x"}) // never empty → page/empty never stops
	}))
	defer srv.Close()

	_, err := httpAll(t, srv, nil, map[string]any{
		"url":      srv.URL,
		"paginate": map[string]any{"style": "page", "param": "page", "maxPages": 3},
	})
	if err == nil {
		t.Fatal("expected maxPages error, got nil")
	}
	if !strings.Contains(err.Error(), "maxPages") {
		t.Errorf("error = %q, want maxPages", err.Error())
	}
}

// Test 8: mid-pagination 500 → whole call errors (no partial list).
func TestHttpAllMidFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 1 {
			writeJSON(w, []any{"a", "b"})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	got, err := httpAll(t, srv, nil, map[string]any{
		"url":      srv.URL,
		"paginate": map[string]any{"style": "page", "param": "page", "start": 1},
	})
	if err == nil {
		t.Fatalf("expected error on mid-pagination 500, got items %v", got)
	}
	if got != nil {
		t.Errorf("expected nil result on failure, got %v", got)
	}
}

// Test 9: secret/auth resolution applies on every page request.
func TestHttpAllAuthEveryPage(t *testing.T) {
	var authed, total int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total++
		if r.Header.Get("Authorization") == "Bearer tok123" {
			authed++
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 2 {
			writeJSON(w, []any{"a"})
		} else {
			writeJSON(w, []any{})
		}
	}))
	defer srv.Close()

	reveal := fakeReveal(map[string]string{"TOK": "tok123"})
	got, err := httpAll(t, srv, reveal, map[string]any{
		"url":      srv.URL,
		"auth":     map[string]any{"bearer": map[string]any{"ref": "TOK"}},
		"paginate": map[string]any{"style": "page", "param": "page", "start": 1},
	})
	if err != nil {
		t.Fatalf("httpAll: %v", err)
	}
	if len(got.([]any)) != 2 {
		t.Errorf("got %d items, want 2", len(got.([]any)))
	}
	if authed != total || total < 3 {
		t.Errorf("authed=%d total=%d — auth not applied on every request", authed, total)
	}
}
