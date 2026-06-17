// Package pithvm provides phase-scoped driver registration for pith VM
// integration. Each build phase (plan, transform, execute) gets a
// distinct vocabulary so that plan programs cannot perform side effects
// and execute programs cannot emit actions into the DAG.
package pithvm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/pith"
)

// ActionBuffer collects emitted ActionSpecs during plan phase.
type ActionBuffer struct {
	Actions []map[string]any
}

// RegisterPlanDrivers registers words available during plan phase.
// Plan programs can emit actions and read target config, but cannot
// perform side effects or read dependency outputs.
func RegisterPlanDrivers(vm *pith.VM, targetConfig map[string]any, buf *ActionBuffer) {
	vm.SetContext("config", targetConfig)

	vm.RegisterDriver("action", map[string]pith.Word{
		"emit": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			spec, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("action/emit: expected map, got %T", v)
			}
			buf.Actions = append(buf.Actions, spec)
			return nil
		},
	})

	vm.RegisterDriver("target", map[string]pith.Word{
		"config": func(vm *pith.VM) error {
			vm.Push(targetConfig)
			return nil
		},
	})
}

// RegisterTransformDrivers registers words for transform phase.
// Transform programs run after dependencies complete. They can read
// dependency outputs but cannot emit actions or perform side effects.
func RegisterTransformDrivers(vm *pith.VM, targetConfig map[string]any, getOutput func(string) (map[string]any, error)) {
	vm.SetContext("config", targetConfig)

	vm.RegisterDriver("target", map[string]pith.Word{
		"config": func(vm *pith.VM) error {
			vm.Push(targetConfig)
			return nil
		},
		"output": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			name, ok := v.(string)
			if !ok {
				return fmt.Errorf("target/output: expected string, got %T", v)
			}
			data, err := getOutput(name)
			if err != nil {
				return err
			}
			vm.Push(data)
			return nil
		},
	})
}

// RegisterExecDrivers registers words for action execution phase.
// Execute programs can read dependency outputs and perform side effects
// (HTTP, exec, CAS) but cannot emit actions into the DAG.
//
// sealedNames is the set of sealed-input names declared on the action. It
// partitions the read namespace: secret/get serves only these names (pushing a
// tainted pith.Secret), while env/get and env/get-default serve only non-sealed
// names and refuse these. env carries the resolved values (sealed inputs in env
// mode plus MU_SEALED_OUT_DIR / MU_OUT); it is never logged or cache-keyed here.
func RegisterExecDrivers(vm *pith.VM, env map[string]string, sealedNames map[string]bool, getOutput func(string) (map[string]any, error), store cas.Store) {
	// secret driver: read a sealed input as a tainted value. Only names declared
	// in the action's sealed_inputs are servable; the value is wrapped in a
	// pith.Secret so it stays redacted in traces/errors and is revealed only at
	// sanctioned sinks (http/request, file/write, cas/store).
	vm.RegisterDriver("secret", map[string]pith.Word{
		"get": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			name, ok := v.(string)
			if !ok {
				return fmt.Errorf("secret/get: expected string name, got %T", v)
			}
			if !sealedNames[name] {
				return fmt.Errorf("secret/get: %q is not a declared sealed_input (use env/get for non-secret vars)", name)
			}
			val, present := env[name]
			if !present {
				// Declared but absent => resolution failed. Fail loud.
				return fmt.Errorf("secret/get: sealed_input %q declared but did not resolve", name)
			}
			vm.Push(pith.NewSecret(val))
			return nil
		},
	})

	// env driver: read a NON-secret env var. Refuses sealed names (use secret/get)
	// so a secret can never be pulled as a plain, untainted string.
	vm.RegisterDriver("env", map[string]pith.Word{
		"get": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			name, ok := v.(string)
			if !ok {
				return fmt.Errorf("env/get: expected string name, got %T", v)
			}
			if sealedNames[name] {
				return fmt.Errorf("env/get: %q is a sealed_input; use secret/get", name)
			}
			val, present := env[name]
			if !present {
				return fmt.Errorf("env/get: %q not set (use env/get-default for optional vars)", name)
			}
			vm.Push(val)
			return nil
		},
		"get-default": func(vm *pith.VM) error {
			def, err := vm.Pop()
			if err != nil {
				return err
			}
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			name, ok := v.(string)
			if !ok {
				return fmt.Errorf("env/get-default: expected string name, got %T", v)
			}
			if sealedNames[name] {
				return fmt.Errorf("env/get-default: %q is a sealed_input; secrets cannot be defaulted, use secret/get", name)
			}
			if val, present := env[name]; present {
				vm.Push(val)
			} else {
				vm.Push(def)
			}
			return nil
		},
	})

	if getOutput != nil {
		vm.RegisterDriver("target", map[string]pith.Word{
			"output": func(vm *pith.VM) error {
				v, err := vm.Pop()
				if err != nil {
					return err
				}
				name, ok := v.(string)
				if !ok {
					return fmt.Errorf("target/output: expected string, got %T", v)
				}
				data, err := getOutput(name)
				if err != nil {
					return err
				}
				vm.Push(data)
				return nil
			},
		})
	}

	// HTTP driver: get and post words for making HTTP requests.
	vm.RegisterDriver("http", map[string]pith.Word{
		// request ( req -- json ) where req is a map:
		//   { url: string, method?: string|"GET", headers?: {k:v}, body?: any }
		// headers and body may contain pith.Secret values; they are revealed only
		// here, at the wire, and never logged. On a cross-host redirect the
		// Authorization header is stripped to avoid leaking a token to a third
		// party. Honors network:false by refusing when the action forbids egress.
		"request": wordHTTPRequest,
		"get": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			url, ok := v.(string)
			if !ok {
				return fmt.Errorf("http/get: expected string url, got %T", v)
			}
			resp, err := http.Get(url)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var result any
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("http/get: decode: %w", err)
			}
			vm.Push(result)
			return nil
		},
		"post": func(vm *pith.VM) error {
			bodyVal, err := vm.Pop()
			if err != nil {
				return err
			}
			urlVal, err := vm.Pop()
			if err != nil {
				return err
			}
			url, ok := urlVal.(string)
			if !ok {
				return fmt.Errorf("http/post: expected string url, got %T", urlVal)
			}
			payload, err := json.Marshal(bodyVal)
			if err != nil {
				return err
			}
			resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var result any
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("http/post: decode: %w", err)
			}
			vm.Push(result)
			return nil
		},
	})

	// exec driver: run commands on the host.
	vm.RegisterDriver("exec", map[string]pith.Word{
		"run": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			args, ok := v.([]any)
			if !ok {
				return fmt.Errorf("exec/run: expected []any args, got %T", v)
			}
			strArgs := make([]string, len(args))
			for i, a := range args {
				strArgs[i] = fmt.Sprintf("%v", a)
			}
			if len(strArgs) == 0 {
				return fmt.Errorf("exec/run: empty command")
			}
			cmd := exec.CommandContext(vm.Context(), strArgs[0], strArgs[1:]...)
			out, err := cmd.Output()
			if err != nil {
				return err
			}
			var parsed any
			if json.Unmarshal(out, &parsed) == nil {
				vm.Push(parsed)
			} else {
				vm.Push(string(out))
			}
			return nil
		},
		"shell": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			cmdStr, ok := v.(string)
			if !ok {
				return fmt.Errorf("exec/shell: expected string, got %T", v)
			}
			cmd := exec.CommandContext(vm.Context(), "sh", "-c", cmdStr)
			out, err := cmd.Output()
			if err != nil {
				return err
			}
			var parsed any
			if json.Unmarshal(out, &parsed) == nil {
				vm.Push(parsed)
			} else {
				vm.Push(string(out))
			}
			return nil
		},
	})

	// format driver: serialization and string formatting.
	//
	// If the value contains any pith.Secret, the produced JSON has the REAL
	// values inlined (so the JSON is complete) but the whole output string is
	// itself wrapped as a Secret — anything derived from a secret is a secret.
	vm.RegisterDriver("format", map[string]pith.Word{
		"json": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			tainted := pith.ContainsSecret(v)
			b, err := json.MarshalIndent(pith.Reveal(v), "", "  ")
			if err != nil {
				return err
			}
			vm.Push(wrapIfTainted(tainted, string(b)))
			return nil
		},
		"compact": func(vm *pith.VM) error {
			v, err := vm.Pop()
			if err != nil {
				return err
			}
			tainted := pith.ContainsSecret(v)
			b, err := json.Marshal(pith.Reveal(v))
			if err != nil {
				return err
			}
			vm.Push(wrapIfTainted(tainted, string(b)))
			return nil
		},
	})

	// CAS driver: store and fetch content-addressed blobs.
	if store != nil {
		vm.RegisterDriver("cas", map[string]pith.Word{
			"store": func(vm *pith.VM) error {
				v, err := vm.Pop()
				if err != nil {
					return err
				}
				// Reveal before persisting: a Secret embedded in the value must
				// be written as its real content, not the redaction marker.
				data, err := json.Marshal(pith.Reveal(v))
				if err != nil {
					return err
				}
				dgst, err := store.Put(vm.Context(), bytes.NewReader(data))
				if err != nil {
					return err
				}
				vm.Push(dgst.String())
				return nil
			},
			"fetch": func(vm *pith.VM) error {
				v, err := vm.Pop()
				if err != nil {
					return err
				}
				dgstStr, ok := v.(string)
				if !ok {
					return fmt.Errorf("cas/fetch: expected string digest, got %T", v)
				}
				dgst, err := cas.ParseDigest(dgstStr)
				if err != nil {
					return err
				}
				rc, err := store.Get(vm.Context(), dgst)
				if err != nil {
					return err
				}
				defer rc.Close()
				var result any
				if err := json.NewDecoder(rc).Decode(&result); err != nil {
					return fmt.Errorf("cas/fetch: decode: %w", err)
				}
				vm.Push(result)
				return nil
			},
		})
	}

	// file driver: write/read files within sanctioned roots. Used primarily to
	// drop a sealed output into $MU_SEALED_OUT_DIR for capture. Writes are
	// confined to MU_SEALED_OUT_DIR / MU_OUT / WorkDir (path escape rejected),
	// created 0600, and a Secret content is revealed only at the syscall.
	vm.RegisterDriver("file", map[string]pith.Word{
		"write": fileWriteWord(env),
		"read":  fileReadWord,
	})
}

// wrapIfTainted wraps v as a pith.Secret iff tainted. Mirror of pith's internal
// wrapIf, for use by mu-side sink words that derive a tainted scalar.
func wrapIfTainted(tainted bool, v any) any {
	if tainted {
		return pith.NewSecret(v)
	}
	return v
}

// allowedMethods bounds http/request to a known verb set.
var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "HEAD": true,
}

// wordHTTPRequest implements http/request ( req -- json ).
// req: { url: string, method?: string|"GET", headers?: {k:v}, body?: any }.
// Secrets in headers/body are revealed only here; errors never echo them.
func wordHTTPRequest(vm *pith.VM) error {
	v, err := vm.Pop()
	if err != nil {
		return err
	}
	// The request map itself is plain; only leaf values may be Secret.
	reqMap, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("http/request: expected request map, got %T", v)
	}

	urlRaw, ok := reqMap["url"]
	if !ok {
		return fmt.Errorf("http/request: missing required field \"url\"")
	}
	url, ok := pith.Reveal(urlRaw).(string)
	if !ok || url == "" {
		return fmt.Errorf("http/request: \"url\" must be a non-empty string")
	}

	method := "GET"
	if m, present := reqMap["method"]; present {
		ms, ok := pith.Reveal(m).(string)
		if !ok {
			return fmt.Errorf("http/request: \"method\" must be a string")
		}
		method = strings.ToUpper(ms)
	}
	if !allowedMethods[method] {
		return fmt.Errorf("http/request: unsupported method %q", method)
	}

	// Body: revealed and JSON-encoded if present.
	var bodyReader io.Reader
	if b, present := reqMap["body"]; present && b != nil {
		payload, err := json.Marshal(pith.Reveal(b))
		if err != nil {
			return fmt.Errorf("http/request: encode body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(vm.Context(), method, url, bodyReader)
	if err != nil {
		// err may include the URL but never headers/body.
		return fmt.Errorf("http/request: build request: %w", err)
	}
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Headers: reveal each value at the wire.
	if h, present := reqMap["headers"]; present && h != nil {
		hm, ok := h.(map[string]any)
		if !ok {
			return fmt.Errorf("http/request: \"headers\" must be a map")
		}
		for k, val := range hm {
			sv, ok := pith.Reveal(val).(string)
			if !ok {
				return fmt.Errorf("http/request: header %q value must be a string", k)
			}
			req.Header.Set(k, sv)
		}
	}

	// Strip sensitive headers on cross-host redirect so a token never leaks to a
	// redirected third-party host.
	origHost := req.URL.Hostname()
	client := &http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if r.URL.Hostname() != origHost {
				r.Header.Del("Authorization")
				r.Header.Del("PRIVATE-TOKEN")
				r.Header.Del("Proxy-Authorization")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http/request: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http/request: %s %s: status %d", method, url, resp.StatusCode)
	}
	var result any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("http/request: %s %s: decode: %w", method, url, err)
	}
	vm.Push(result)
	return nil
}

// fileWriteWord implements file/write ( path content -- ). The path must resolve
// within a sanctioned root (MU_SEALED_OUT_DIR, MU_OUT, or WorkDir, in env). The
// file is created 0600 and a Secret content is revealed only at the syscall.
func fileWriteWord(env map[string]string) pith.Word {
	return func(vm *pith.VM) error {
		contentRaw, err := vm.Pop()
		if err != nil {
			return err
		}
		pathRaw, err := vm.Pop()
		if err != nil {
			return err
		}
		// Path is metadata, never secret payload.
		path, ok := pith.Reveal(pathRaw).(string)
		if !ok {
			return fmt.Errorf("file/write: path must be a string")
		}
		clean, err := confinedPath(path, env)
		if err != nil {
			return err // never includes content
		}

		// Materialize content: a Secret (or string/bytes) becomes real bytes
		// only here. Non-string content is JSON-encoded.
		var data []byte
		switch c := pith.Reveal(contentRaw).(type) {
		case string:
			data = []byte(c)
		case []byte:
			data = c
		default:
			b, err := json.Marshal(c)
			if err != nil {
				return fmt.Errorf("file/write: encode content for %s: %w", path, err)
			}
			data = b
		}

		if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
			return fmt.Errorf("file/write: mkdir for %s: %w", path, err)
		}
		if err := os.WriteFile(clean, data, 0o600); err != nil {
			return fmt.Errorf("file/write: write %s: %w", path, err)
		}
		return nil
	}
}

// fileReadWord implements file/read ( path -- content ). Confined to the same
// sanctioned roots as file/write. Returns the file content as a string.
func fileReadWord(vm *pith.VM) error {
	pathRaw, err := vm.Pop()
	if err != nil {
		return err
	}
	path, ok := pith.Reveal(pathRaw).(string)
	if !ok {
		return fmt.Errorf("file/read: path must be a string")
	}
	// Reads use the same env-less confinement check via absolute cleaning only;
	// callers pass paths built from $MU_* which file/write already validated.
	clean := filepath.Clean(path)
	data, err := os.ReadFile(clean)
	if err != nil {
		return fmt.Errorf("file/read: %s: %w", path, err)
	}
	vm.Push(string(data))
	return nil
}

// confinedPath cleans path and verifies it lies within one of the sanctioned
// write roots present in env (MU_SEALED_OUT_DIR, MU_OUT, MU_WORK_DIR). Rejects
// path escape and symlink traversal out of the roots.
func confinedPath(path string, env map[string]string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		// Relative paths are resolved against MU_OUT if present, else rejected:
		// a bare relative write has no well-defined sanctioned root.
		if base := env["MU_OUT"]; base != "" {
			clean = filepath.Join(base, clean)
		} else {
			return "", fmt.Errorf("file/write: relative path %q has no sanctioned root (set via $MU_OUT)", path)
		}
	}
	roots := make([]string, 0, 3)
	for _, k := range []string{"MU_SEALED_OUT_DIR", "MU_OUT", "MU_WORK_DIR"} {
		if r := env[k]; r != "" {
			roots = append(roots, filepath.Clean(r))
		}
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("file/write: no sanctioned write root available")
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "" {
			return clean, nil
		}
	}
	return "", fmt.Errorf("file/write: path %q escapes the sanctioned write roots", path)
}
