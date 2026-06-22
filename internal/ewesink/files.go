package ewesink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chazu/ewe"
)

// EnvFunc creates the #Env sink: returns the action's exposed environment as a
// struct (e.g. result.MU_OUT). Takes no arguments.
func EnvFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ResultType: ewe.TypeStruct,
		Execute: func(_ context.Context, args []any) (any, error) {
			if len(args) != 0 {
				return nil, fmt.Errorf("%s: takes no arguments, got %d", name, len(args))
			}
			out := make(map[string]any, len(d.Env))
			for k, v := range d.Env {
				out[k] = v
			}
			return out, nil
		},
	}
}

// WriteFileFunc creates the #WriteFile sink: writes content to a path confined
// to MU_OUT. content may be a string (written verbatim) or structured (secret
// refs revealed, then JSON-marshaled in Go). Returns the written path. Files are
// created 0600.
func WriteFileFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeString, ewe.TypeAny},
		ResultType: ewe.TypeString,
		Execute: func(_ context.Context, args []any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s: expected 2 arguments (path, content), got %d", name, len(args))
			}
			path, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("%s: path must be a string, got %T", name, args[0])
			}
			abs, err := confinePath(d.MuOut, path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}

			content, err := resolveSecrets(args[1], d.requireReveal())
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			var data []byte
			switch c := content.(type) {
			case string:
				data = []byte(c)
			default:
				data, err = json.Marshal(c)
				if err != nil {
					return nil, fmt.Errorf("%s: marshal content: %w", name, err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if err := os.WriteFile(abs, data, 0o600); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return path, nil
		},
	}
}

// ReadFileFunc creates the #ReadFile sink: reads a declared input from WorkDir
// (cross-action dataflow). The name is confined to WorkDir. Returns the decoded
// JSON value, or the raw string if it is not JSON.
func ReadFileFunc(name string, d Deps) ewe.Function {
	return ewe.Function{
		Name:       name,
		ParamTypes: []ewe.CUEType{ewe.TypeString},
		ResultType: ewe.TypeAny,
		Execute: func(_ context.Context, args []any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s: expected 1 argument (name), got %d", name, len(args))
			}
			rel, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("%s: name must be a string, got %T", name, args[0])
			}
			abs, err := confinePath(d.WorkDir, rel)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			raw, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			var decoded any
			if json.Unmarshal(raw, &decoded) == nil {
				return decoded, nil
			}
			return string(raw), nil
		},
	}
}

// confinePath resolves rel against base and rejects any path that escapes base
// (path-traversal guard). base must be non-empty.
func confinePath(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("no base directory configured")
	}
	cleanBase := filepath.Clean(base)
	abs := filepath.Clean(rel)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cleanBase, rel)
	}
	if abs != cleanBase && !strings.HasPrefix(abs, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", rel, base)
	}
	return abs, nil
}
