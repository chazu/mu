// Package pithvm provides phase-scoped driver registration for pith VM
// integration. Each build phase (plan, transform, execute) gets a
// distinct vocabulary so that plan programs cannot perform side effects
// and execute programs cannot emit actions into the DAG.
package pithvm

import (
	"fmt"

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
func RegisterExecDrivers(vm *pith.VM, env map[string]string, getOutput func(string) (map[string]any, error)) {
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
	// HTTP, exec, CAS, secret drivers will be added as needed.
	// For v1, exec-phase pith programs have access to target/output
	// and all builtin pith words (stack, data, combinators).
}
