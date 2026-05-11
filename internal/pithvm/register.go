// Package pithvm provides phase-scoped driver registration for pith VM
// integration. Each build phase (plan, transform, execute) gets a
// distinct vocabulary so that plan programs cannot perform side effects
// and execute programs cannot emit actions into the DAG.
package pithvm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/chau/mu/internal/cas"
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
func RegisterExecDrivers(vm *pith.VM, env map[string]string, getOutput func(string) (map[string]any, error), store cas.Store) {
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

	// CAS driver: store and fetch content-addressed blobs.
	if store != nil {
		vm.RegisterDriver("cas", map[string]pith.Word{
			"store": func(vm *pith.VM) error {
				v, err := vm.Pop()
				if err != nil {
					return err
				}
				data, err := json.Marshal(v)
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
}
