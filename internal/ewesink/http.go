package ewesink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chazu/ewe"
)

// defaultMaxPages is the enforced safety cap on a paginating fetch — an
// unbounded live-HTTP loop is strictly worse than a CPU loop. Overridable
// upward per call; never disengaged.
const defaultMaxPages = 1000

// httpClient returns the configured client or the default.
func (d Deps) httpClient() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

// HTTPAllFunc creates the #HttpAll sink: an HTTP fetch with a closed set of Go-
// side paging strategies (none/page/link/cursor). The paging loop, item
// extraction, and stop-detection are all Go; CUE only declares which strategy
// and where its signals live. Returns the concatenated item list across pages.
func HTTPAllFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeStruct},
		ResultType: ewe.TypeList,
		Execute: func(ctx context.Context, args []any) (any, error) {
			req, err := singleArgMap(name, args)
			if err != nil {
				return nil, err
			}
			return d.fetchAll(ctx, name, req)
		},
	}
}

// HTTPFunc creates the #Http sink: a single request returning the decoded body.
// It is #HttpAll with paginate.style="none" and no item extraction.
func HTTPFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeStruct},
		ResultType: ewe.TypeAny,
		Execute: func(ctx context.Context, args []any) (any, error) {
			req, err := singleArgMap(name, args)
			if err != nil {
				return nil, err
			}
			rr, err := d.buildAndDo(ctx, req, nil, nil)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return rr.body, nil
		},
	}
}

// HTTPBatchFunc creates the #HttpBatch sink: fan out over a list of independent
// request structs, returning a per-item result envelope {ok, value?, error?}.
// Unlike pagination (one logical collection, fail-loud), a batch tolerates
// per-item failure because the items are independent (ledger E4).
func HTTPBatchFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeList},
		ResultType: ewe.TypeList,
		Execute: func(ctx context.Context, args []any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s: expected 1 argument (a list of requests), got %d", name, len(args))
			}
			list, ok := args[0].([]any)
			if !ok {
				return nil, fmt.Errorf("%s: argument must be a list, got %T", name, args[0])
			}
			out := make([]any, len(list))
			for i, item := range list {
				req, ok := item.(map[string]any)
				if !ok {
					out[i] = map[string]any{"ok": false, "error": fmt.Sprintf("item %d: not a request struct", i)}
					continue
				}
				rr, err := d.buildAndDo(ctx, req, nil, nil)
				if err != nil {
					out[i] = map[string]any{"ok": false, "error": err.Error()}
					continue
				}
				out[i] = map[string]any{"ok": true, "value": rr.body}
			}
			return out, nil
		},
	}
}

// fetchAll runs the paginating loop for #HttpAll.
func (d Deps) fetchAll(ctx context.Context, name string, req map[string]any) (any, error) {
	pag, _ := req["paginate"].(map[string]any)
	style := "none"
	if pag != nil {
		if s, ok := pag["style"].(string); ok && s != "" {
			style = s
		}
	}
	maxPages := defaultMaxPages
	if pag != nil {
		if mp, ok := toInt(pag["maxPages"]); ok && mp > 0 {
			maxPages = mp
		}
	}
	var itemsPath string
	if pag != nil {
		itemsPath, _ = pag["itemsPath"].(string)
	}

	// Per-request mutable extras: query overrides (page/cursor params) and an
	// explicit next URL (link style). Lowered auth + headers are resolved once
	// and applied to every page.
	var (
		items    []any
		pageNum  = 1
		nextURL  string // link style; "" means use req.url
		cursor   string // cursor style token
		haveNext = true
	)
	if pag != nil {
		if s, ok := toInt(pag["start"]); ok {
			pageNum = s
		}
	}

	for p := 0; ; p++ {
		if p >= maxPages {
			return nil, fmt.Errorf("%s: exceeded maxPages=%d (runaway pagination)", name, maxPages)
		}

		extraQuery := map[string]any{}
		overrideURL := nextURL
		switch style {
		case "page":
			if param, ok := pag["param"].(string); ok && param != "" {
				extraQuery[param] = pageNum
			}
		case "cursor":
			if cursor != "" {
				if cp, ok := pag["cursorParam"].(string); ok && cp != "" {
					extraQuery[cp] = cursor
				}
			}
		}

		rr, err := d.buildAndDo(ctx, req, extraQuery, overrideStr(overrideURL))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		pageItems, err := extractItems(rr.body, itemsPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		items = append(items, pageItems...)

		// Decide stop / advance.
		switch style {
		case "none":
			haveNext = false
		case "page":
			stop := "empty"
			if s, ok := pag["stop"].(string); ok && s != "" {
				stop = s
			}
			switch stop {
			case "partial":
				size := pageSize(req, pag)
				if size <= 0 || len(pageItems) < size {
					haveNext = false
				}
			default: // "empty"
				if len(pageItems) == 0 {
					haveNext = false
				}
			}
			pageNum++
		case "link":
			next := linkNext(rr.header.Get("Link"))
			if next == "" {
				haveNext = false
			} else {
				nextURL = next
			}
		case "cursor":
			if hp, ok := pag["hasMorePath"].(string); ok && hp != "" {
				if hm, ok := getPath(rr.body, hp).(bool); ok && !hm {
					haveNext = false
				}
			}
			tok := getPath(rr.body, strOf(pag["nextPath"]))
			ts, _ := tok.(string)
			if ts == "" || tok == nil {
				haveNext = false
			} else {
				cursor = ts
			}
		default:
			return nil, fmt.Errorf("%s: unknown paginate.style %q", name, style)
		}

		if !haveNext {
			break
		}
	}
	return items, nil
}

// requestResult holds a decoded response.
type requestResult struct {
	status int
	header http.Header
	body   any
}

// buildAndDo lowers auth, resolves secrets, builds the request (merging
// extraQuery and an optional override URL), executes it, and decodes the JSON
// body. Non-2xx is an error (action-level Retries are mu's concern).
func (d Deps) buildAndDo(ctx context.Context, req map[string]any, extraQuery map[string]any, overrideURL *string) (*requestResult, error) {
	rawURL, ok := req["url"].(string)
	if overrideURL != nil {
		rawURL, ok = *overrideURL, true
	}
	if !ok || rawURL == "" {
		return nil, fmt.Errorf("missing required field \"url\"")
	}

	method := "GET"
	if m, ok := req["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	// Assemble headers and query, lowering any auth block into them.
	headers := cloneMap(req["headers"])
	query := cloneMap(req["query"])
	for k, v := range extraQuery {
		query[k] = v
	}
	if auth, ok := req["auth"].(map[string]any); ok && auth != nil {
		if err := lowerAuth(auth, headers, query); err != nil {
			return nil, err
		}
	}

	// Reveal secrets in headers + query just before the syscall.
	rh, err := resolveSecrets(headers, d.requireReveal())
	if err != nil {
		return nil, err
	}
	rq, err := resolveSecrets(query, d.requireReveal())
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	for k, v := range rq.(map[string]any) {
		q.Set(k, strOf(v))
	}
	u.RawQuery = q.Encode()

	// Body: structured request bodies are revealed then marshaled in Go (never
	// json.Marshal'd in CUE — see the authoring gotcha).
	var bodyReader io.Reader
	if b, ok := req["body"]; ok && b != nil {
		rb, err := resolveSecrets(b, d.requireReveal())
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(rb)
		if err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
		bodyReader = strings.NewReader(string(payload))
	}

	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err) // never echoes headers/body
	}
	if bodyReader != nil && hreq.Header.Get("Content-Type") == "" {
		hreq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range rh.(map[string]any) {
		hreq.Header.Set(k, strOf(v))
	}

	resp, err := d.httpClient().Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, u.Redacted(), err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: status %d", method, u.Redacted(), resp.StatusCode)
	}

	var decoded any
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // preserve integer IDs (e.g. for URL paths) as ints, not 1.0 floats
		if err := dec.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode response JSON: %w", err)
		}
	}
	return &requestResult{status: resp.StatusCode, header: resp.Header, body: decoded}, nil
}

// requireReveal returns the configured reveal func, or one that errors closed if
// none is configured (a ref reached a sink with no resolver).
func (d Deps) requireReveal() RevealFunc {
	if d.Reveal != nil {
		return d.Reveal
	}
	return func(name string) (string, error) {
		return "", fmt.Errorf("secret %q: no reveal resolver configured for this action", name)
	}
}

// --- helpers ---

func singleArgMap(name string, args []any) (map[string]any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected 1 argument (a request struct), got %d", name, len(args))
	}
	m, ok := args[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: argument must be a struct, got %T", name, args[0])
	}
	return m, nil
}

// extractItems pulls the per-page item list from a body: at itemsPath if set,
// else the body itself. Must resolve to a list.
func extractItems(body any, itemsPath string) ([]any, error) {
	v := body
	if itemsPath != "" {
		v = getPath(body, itemsPath)
	}
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list at items path %q, got %T", itemsPath, v)
	}
	return list, nil
}

// getPath walks a dotted path through nested maps. Returns nil if any segment is
// absent or not a map.
func getPath(v any, path string) any {
	if path == "" {
		return v
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	return cur
}

// linkNext parses an RFC 5988 Link header and returns the rel="next" URL, or "".
func linkNext(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(seg[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		for _, param := range seg[1:] {
			param = strings.TrimSpace(param)
			if param == `rel="next"` || param == "rel=next" {
				return urlPart[1 : len(urlPart)-1]
			}
		}
	}
	return ""
}

// pageSize reads the page-size value named by paginate.sizeParam from the query.
func pageSize(req, pag map[string]any) int {
	sizeParam, _ := pag["sizeParam"].(string)
	if sizeParam == "" {
		return 0
	}
	query, _ := req["query"].(map[string]any)
	if query == nil {
		return 0
	}
	n, _ := toInt(query[sizeParam])
	return n
}

func cloneMap(v any) map[string]any {
	out := map[string]any{}
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			out[k] = val
		}
	}
	return out
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i, true
		}
	}
	return 0, false
}

func strOf(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func overrideStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
