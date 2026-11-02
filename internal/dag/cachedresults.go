package dag

import (
	"context"
	"fmt"
	"maps"

	"github.com/chazu/mu/internal/cas"
)

// CachedResult is a planned action whose result is already in a store.
type CachedResult struct {
	ActionID string
	Key      cas.ActionKey
	Result   *cas.ActionResult
}

// CachedResults finds the stored results of g's pure actions without running
// anything. lookup reads one result (nil, nil on a miss), normally from the
// local store.
//
// An action's key depends on the digests its upstream inputs actually had, so
// keys are only known in dependency order: a producer's stored result supplies
// the digests that fill its consumers' placeholder inputs, exactly as the
// executor does after the producer runs. An impure producer has no stored
// result; as in the executor, its declared outputs are hashed in place.
//
// missing lists the pure actions with no (readable) stored result, including
// every action whose key cannot be derived because an upstream result is
// missing.
// Neither list contains impure actions, which are never cached.
func CachedResults(ctx context.Context, g *Graph, lookup func(context.Context, cas.ActionKey) (*cas.ActionResult, error)) (found []CachedResult, missing []string, err error) {
	order, err := TopoSortFlat(g)
	if err != nil {
		return nil, nil, err
	}
	resolved := make(map[string]completedAction, len(order))
	unresolved := make(map[string]bool)
	for _, a := range order {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		blocked := false
		for _, dep := range g.DependsOn(a.ID) {
			if unresolved[dep] {
				blocked = true
				break
			}
		}
		if !blocked {
			keyed, rerr := withResolvedInputs(a, resolved)
			if rerr != nil {
				blocked = true
			} else if a.Impure {
				resolved[a.ID] = completedAction{action: a}
				continue
			} else {
				key := ComputeActionKey(keyed)
				// An unreadable result is a miss, as it is for the executor.
				result, lerr := lookup(ctx, key)
				if lerr != nil && ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				if lerr == nil && result != nil {
					resolved[a.ID] = completedAction{action: a, outputs: result.Outputs}
					found = append(found, CachedResult{ActionID: a.ID, Key: key, Result: result})
					continue
				}
				blocked = true
			}
		}
		unresolved[a.ID] = true
		if !a.Impure {
			missing = append(missing, a.ID)
		}
	}
	return found, missing, nil
}

// withResolvedInputs returns a copy of a with its placeholder inputs filled
// from resolved producers, leaving the planned action untouched.
func withResolvedInputs(a *Action, resolved map[string]completedAction) (*Action, error) {
	if len(a.InputSources) == 0 {
		return a, nil
	}
	cp := *a
	cp.Inputs = maps.Clone(a.Inputs)
	if cp.Inputs == nil {
		cp.Inputs = make(map[string]cas.Digest, len(a.InputSources))
	}
	for _, name := range sortedKeys(a.InputSources) {
		src := a.InputSources[name]
		producer, ok := resolved[src.ProducerID]
		if !ok {
			return nil, fmt.Errorf("input %q: producer %q unresolved", name, src.ProducerID)
		}
		var d cas.Digest
		var err error
		if src.OutputPath == "" {
			d, err = producer.allOutputsDigest()
		} else {
			d, err = producer.outputDigest(src.OutputPath)
		}
		if err != nil {
			return nil, err
		}
		cp.Inputs[name] = d
	}
	return &cp, nil
}
