package dag

import (
	"fmt"
	"strings"
)

// TopoSort returns actions grouped into levels of parallelizable work.
// Level 0 has no dependencies, level 1 depends only on level 0, etc.
// Returns an error if the graph contains a cycle (listing the cycle) or
// if any action depends on an unknown action ID.
func TopoSort(g *Graph) ([][]*Action, error) {
	if g.Len() == 0 {
		return nil, nil
	}

	// Validate that all dependency references point to known actions.
	for _, id := range g.order {
		for _, dep := range g.deps[id] {
			if _, ok := g.actions[dep]; !ok {
				return nil, fmt.Errorf("dag: action %q depends on unknown action %q", id, dep)
			}
		}
	}

	// Compute in-degree for each node.
	inDegree := make(map[string]int, len(g.order))
	for _, id := range g.order {
		// Ensure every node has an entry, then add its dependency count.
		// Each dependency edge dep -> id means id has in-degree +1
		// (id depends on dep, so dep must come before id).
		inDegree[id] += len(g.deps[id])
	}

	// Seed the first queue with all zero in-degree nodes, in insertion order
	// for determinism.
	var queue []string
	for _, id := range g.order {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	var levels [][]*Action
	visited := 0

	for len(queue) > 0 {
		// All nodes in the current queue form one parallelizable level.
		level := make([]*Action, len(queue))
		for i, id := range queue {
			level[i] = g.actions[id]
		}
		levels = append(levels, level)
		visited += len(queue)

		var nextQueue []string
		for _, id := range queue {
			// Decrement in-degree of all dependents (nodes that depend on id).
			for _, dep := range g.rdeps[id] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					nextQueue = append(nextQueue, dep)
				}
			}
		}
		queue = nextQueue
	}

	if visited != len(g.order) {
		cycle := findCycle(g, inDegree)
		return nil, fmt.Errorf("dag: cycle detected: %s", formatCycle(cycle))
	}

	return levels, nil
}

// TopoSortFlat returns all actions in a valid topological order (flat list).
func TopoSortFlat(g *Graph) ([]*Action, error) {
	levels, err := TopoSort(g)
	if err != nil {
		return nil, err
	}
	var flat []*Action
	for _, level := range levels {
		flat = append(flat, level...)
	}
	return flat, nil
}

// findCycle walks the remaining nodes with non-zero in-degree to find a cycle.
func findCycle(g *Graph, inDegree map[string]int) []string {
	// Collect the set of nodes still in the graph (non-zero in-degree).
	remaining := make(map[string]bool)
	for id, deg := range inDegree {
		if deg > 0 {
			remaining[id] = true
		}
	}

	// Pick an arbitrary remaining node and walk its dependency chain
	// until we revisit a node, then extract the cycle.
	visited := make(map[string]int) // id -> position in path
	var path []string

	// Start from the first remaining node in insertion order for determinism.
	var start string
	for _, id := range g.order {
		if remaining[id] {
			start = id
			break
		}
	}

	cur := start
	for {
		if pos, ok := visited[cur]; ok {
			// We found the cycle: from pos back to cur.
			cycle := append(path[pos:], cur)
			return cycle
		}
		visited[cur] = len(path)
		path = append(path, cur)

		// Follow a dependency edge that stays within the remaining set.
		moved := false
		for _, dep := range g.deps[cur] {
			if remaining[dep] {
				cur = dep
				moved = true
				break
			}
		}
		if !moved {
			// Should not happen if there is truly a cycle in remaining,
			// but guard against it.
			return path
		}
	}
}

// formatCycle formats a cycle path like "A -> B -> C -> A" using arrow notation.
func formatCycle(cycle []string) string {
	parts := make([]string, len(cycle))
	copy(parts, cycle)
	return strings.Join(parts, " \u2192 ")
}
