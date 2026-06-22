package ewesink

import "fmt"

// lowerAuth converts an HTTP request `auth:` block (Layer 3 sugar) into Layer-1/
// Layer-2 secret refs, writing them into the request's headers and query maps.
// Exactly one scheme per block; auth is sugar, not a separate resolution path —
// resolveSecrets reveals the refs it produces. The maps must be non-nil.
//
//	bearer: {ref}                       -> headers.Authorization = "Bearer <reveal>"
//	header: {name, ref, template?}      -> headers[name]         = template<reveal> (default "{}")
//	query:  {param, ref}                -> query[param]          = <reveal> (sink URL-encodes)
//	basic:  {user?|userRef?, passRef}   -> headers.Authorization = "Basic <base64(user:pass)>"
func lowerAuth(auth, headers, query map[string]any) error {
	schemes := 0
	for _, k := range []string{"bearer", "header", "query", "basic"} {
		if _, ok := auth[k]; ok {
			schemes++
		}
	}
	if schemes == 0 {
		return fmt.Errorf("auth: no scheme set (want one of bearer/header/query/basic)")
	}
	if schemes > 1 {
		return fmt.Errorf("auth: exactly one scheme allowed, got %d", schemes)
	}

	switch {
	case auth["bearer"] != nil:
		b, err := asMap(auth["bearer"], "bearer")
		if err != nil {
			return err
		}
		ref, err := asString(b["ref"], "auth.bearer.ref")
		if err != nil {
			return err
		}
		headers["Authorization"] = templateRef("Bearer {}", ref)

	case auth["header"] != nil:
		h, err := asMap(auth["header"], "header")
		if err != nil {
			return err
		}
		name, err := asString(h["name"], "auth.header.name")
		if err != nil {
			return err
		}
		ref, err := asString(h["ref"], "auth.header.ref")
		if err != nil {
			return err
		}
		tmpl := "{}"
		if t, ok := h["template"]; ok {
			if tmpl, err = asString(t, "auth.header.template"); err != nil {
				return err
			}
		}
		headers[name] = templateRef(tmpl, ref)

	case auth["query"] != nil:
		q, err := asMap(auth["query"], "query")
		if err != nil {
			return err
		}
		param, err := asString(q["param"], "auth.query.param")
		if err != nil {
			return err
		}
		ref, err := asString(q["ref"], "auth.query.ref")
		if err != nil {
			return err
		}
		query[param] = map[string]any{"$secret": ref}

	case auth["basic"] != nil:
		b, err := asMap(auth["basic"], "basic")
		if err != nil {
			return err
		}
		passRef, err := asString(b["passRef"], "auth.basic.passRef")
		if err != nil {
			return err
		}
		// user may be a literal (baked into the template) or a secret ref.
		var tmpl string
		var refs []any
		switch {
		case b["userRef"] != nil:
			userRef, err := asString(b["userRef"], "auth.basic.userRef")
			if err != nil {
				return err
			}
			tmpl, refs = "{}:{}", []any{userRef, passRef}
		case b["user"] != nil:
			user, err := asString(b["user"], "auth.basic.user")
			if err != nil {
				return err
			}
			tmpl, refs = user+":{}", []any{passRef}
		default:
			return fmt.Errorf("auth.basic: need user or userRef")
		}
		// encode the user:pass pair as base64, then prefix "Basic " (kept outside
		// the encode, per the scheme).
		headers["Authorization"] = map[string]any{
			"$secretTemplate": tmpl,
			"refs":            refs,
			"encode":          "base64",
			"prefix":          "Basic ",
		}
	}
	return nil
}

// templateRef builds a single-ref Layer-2 template struct.
func templateRef(tmpl, ref string) map[string]any {
	return map[string]any{"$secretTemplate": tmpl, "refs": []any{ref}}
}

func asMap(v any, what string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("auth.%s: must be a struct, got %T", what, v)
	}
	return m, nil
}

func asString(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: must be a string, got %T", what, v)
	}
	return s, nil
}
