// Package ewesink hosts mu's registration of ewe effect functions — the Go
// "sinks" an ewe populator program calls (HTTP fetch, file write) plus the
// secret model that reveals sealed inputs only inside those sinks.
//
// The safety property (ewe-secrets-spec): secrets are references in CUE; the
// real value never enters the CUE layer, only an inert tag does. resolveSecrets
// reveals refs in Go immediately before a syscall and its output is never
// returned to the CUE layer.
package ewesink

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/chazu/ewe"
)

// RevealFunc resolves a sealed-input name to its plaintext value. It closes over
// the action's resolved SealedInputs (env:/pass: refs) on the mu side. Reveal is
// confined to sinks — populator CUE has no resolve verb.
type RevealFunc func(name string) (string, error)

// SecretFunc creates the #Secret function: a whole-value secret reference
// (Layer 1). It returns the inert tag {"$secret": name}, never the value, so the
// reference is exfil-safe in the CUE layer (a struct cannot be interpolated into
// a string). The value is revealed only in a sink via resolveSecrets.
func SecretFunc(name string) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeString},
		ResultType: ewe.TypeStruct,
		Execute: func(_ context.Context, args []any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s: expected 1 argument, got %d", name, len(args))
			}
			n, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("%s: name must be a string, got %T", name, args[0])
			}
			if n == "" {
				return nil, fmt.Errorf("%s: name must be non-empty", name)
			}
			return map[string]any{"$secret": n}, nil
		},
	}
}

// resolveSecrets deep-walks v, replacing {"$secret":name} (Layer 1) and
// {"$secretTemplate":tmpl,refs,encode} (Layer 2) with revealed strings. It is
// called ONLY inside sink functions, immediately before the network/file
// syscall; its output is never returned to the CUE layer.
func resolveSecrets(v any, reveal RevealFunc) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if name, ok := t["$secret"].(string); ok && len(t) == 1 {
			return reveal(name)
		}
		if tmpl, ok := t["$secretTemplate"].(string); ok {
			return expandTemplate(tmpl, t, reveal)
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			rv, err := resolveSecrets(val, reveal)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			rv, err := resolveSecrets(e, reveal)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// expandTemplate reveals each ref in order, substitutes positional `{}`
// placeholders left-to-right (splitting so a revealed value containing "{}"
// cannot itself be substituted into), applies the optional encode to the full
// assembled string, then an optional internal prefix (used by auth.basic, which
// must keep its "Basic " label outside the base64). Returns the final string.
func expandTemplate(tmpl string, m map[string]any, reveal RevealFunc) (string, error) {
	refsRaw, ok := m["refs"].([]any)
	if !ok {
		return "", fmt.Errorf("$secretTemplate: missing refs list")
	}
	parts := strings.Split(tmpl, "{}")
	if len(parts)-1 != len(refsRaw) {
		return "", fmt.Errorf("$secretTemplate: %d placeholders, %d refs", len(parts)-1, len(refsRaw))
	}
	var b strings.Builder
	for i, r := range refsRaw {
		b.WriteString(parts[i])
		name, ok := r.(string)
		if !ok {
			return "", fmt.Errorf("$secretTemplate: ref %d must be a string, got %T", i, r)
		}
		val, err := reveal(name)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
	}
	b.WriteString(parts[len(parts)-1])
	out := b.String()

	if enc, ok := m["encode"].(string); ok {
		switch enc {
		case "", "none":
		case "base64":
			out = base64.StdEncoding.EncodeToString([]byte(out))
		default:
			return "", fmt.Errorf("$secretTemplate: unknown encode %q", enc)
		}
	}
	if p, ok := m["prefix"].(string); ok {
		out = p + out
	}
	return out, nil
}
