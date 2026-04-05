// Package dag provides a directed acyclic graph representation for mu build actions.
package dag

import (
	"fmt"

	"github.com/chau/mu/internal/cas"
)

// Action describes a single build step in the DAG.
type Action struct {
	ID        string
	Command   []string
	Inputs    map[string]cas.Digest // name -> content hash (resolved by coordinator)
	Outputs   []string              // declared output file paths
	DependsOn []string              // action IDs this depends on
	Env       map[string]string
	Network   bool   // allow network access (honor system in v1)
	WorkDir   string // execution working directory
	Impure    bool   // skip CAS cache when true

	// SealedInputs maps env var names to secret references (e.g. "pass:deploy/token").
	// These are resolved to actual values by the coordinator before execution and
	// injected into the action's environment. Sealed inputs are deliberately excluded
	// from cache key computation, build manifests, and verbose logging.
	SealedInputs map[string]string

	// Toolchain is the set of artifacts (relative path → CAS digest) that
	// constitute the hermetic build environment for this action. When set,
	// the executor unpacks these into a sandbox before running the command.
	Toolchain map[string]cas.Digest

	// Sources lists relative file paths (from WorkDir) to copy into the
	// sandbox's working directory. Used only when Toolchain is set.
	Sources []string
}

// Graph is an adjacency-list representation of a build DAG.
type Graph struct {
	actions map[string]*Action  // id -> action
	deps    map[string][]string // id -> ids it depends on
	rdeps   map[string][]string // id -> ids that depend on it (reverse)
	order   []string            // insertion order for deterministic iteration
}

// NewGraph returns an empty Graph ready for use.
func NewGraph() *Graph {
	return &Graph{
		actions: make(map[string]*Action),
		deps:    make(map[string][]string),
		rdeps:   make(map[string][]string),
	}
}

// AddAction adds an action to the graph and indexes its dependency edges.
// It returns an error if an action with the same ID already exists.
func (g *Graph) AddAction(a *Action) error {
	if _, exists := g.actions[a.ID]; exists {
		return fmt.Errorf("dag: duplicate action ID %q", a.ID)
	}
	g.actions[a.ID] = a
	g.order = append(g.order, a.ID)
	g.deps[a.ID] = a.DependsOn
	for _, dep := range a.DependsOn {
		g.rdeps[dep] = append(g.rdeps[dep], a.ID)
	}
	return nil
}

// Action returns the action with the given ID, or nil if not found.
func (g *Graph) Action(id string) *Action {
	return g.actions[id]
}

// Actions returns all actions in insertion order.
func (g *Graph) Actions() []*Action {
	out := make([]*Action, len(g.order))
	for i, id := range g.order {
		out[i] = g.actions[id]
	}
	return out
}

// DependsOn returns the IDs of actions that the given action directly depends on.
func (g *Graph) DependsOn(id string) []string {
	return g.deps[id]
}

// Dependents returns the IDs of actions that directly depend on the given action.
func (g *Graph) Dependents(id string) []string {
	return g.rdeps[id]
}

// Len returns the number of actions in the graph.
func (g *Graph) Len() int {
	return len(g.actions)
}

// TransitiveDependents returns all action IDs transitively downstream of the
// given action using breadth-first search. This is used for failure cancellation.
func (g *Graph) TransitiveDependents(id string) []string {
	visited := make(map[string]bool)
	queue := []string{id}
	visited[id] = true

	var result []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range g.rdeps[cur] {
			if !visited[dep] {
				visited[dep] = true
				result = append(result, dep)
				queue = append(queue, dep)
			}
		}
	}
	return result
}
