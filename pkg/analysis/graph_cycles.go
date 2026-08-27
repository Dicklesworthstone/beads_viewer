package analysis

import (
	"sort"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/topo"
)

type cycleDetectionResult struct {
	cycles    [][]graph.Node
	total     int
	truncated bool
}

// findCyclesSafe finds a limited number of cycles in the graph without exponential blowup.
// It uses Tarjan's SCC algorithm to identify cyclic components and extracts one cycle per component.
// The result retains the pre-limit representative count so callers can report
// truncation without implying that every simple cycle in an SCC was enumerated.
func findCyclesSafe(g graph.Directed, limit int) cycleDetectionResult {
	if limit <= 0 {
		return cycleDetectionResult{}
	}
	sccs := topo.TarjanSCC(g)
	var cycles [][]graph.Node

	for _, scc := range sccs {
		if len(scc) == 1 {
			// Check for self-loop
			n := scc[0]
			if g.HasEdgeFromTo(n.ID(), n.ID()) {
				cycles = append(cycles, []graph.Node{n, n})
			}
			continue
		}

		// Find a cycle within this non-trivial SCC
		if cycle := findOneCycleInSCC(g, scc); len(cycle) > 0 {
			cycles = append(cycles, cycle)
		}
	}

	// Sort cycles for determinism
	// 1. By length (ascending - shortest cycles are more interesting/fixable)
	// 2. By content (lexicographically for stability)
	sort.Slice(cycles, func(i, j int) bool {
		if len(cycles[i]) != len(cycles[j]) {
			return len(cycles[i]) < len(cycles[j])
		}
		// Lexicographic comparison of node IDs
		for k := 0; k < len(cycles[i]); k++ {
			id1 := cycles[i][k].ID()
			id2 := cycles[j][k].ID()
			if id1 != id2 {
				return id1 < id2
			}
		}
		return false
	})

	result := cycleDetectionResult{total: len(cycles)}
	if len(cycles) > limit {
		result.truncated = true
		cycles = cycles[:limit]
	}
	result.cycles = cycles
	return result
}

// findOneCycleInSCC finds a single cycle within a Strongly Connected Component.
func findOneCycleInSCC(g graph.Directed, scc []graph.Node) []graph.Node {
	// Sort SCC nodes for deterministic DFS starting point
	sort.Slice(scc, func(i, j int) bool {
		return scc[i].ID() < scc[j].ID()
	})

	// Build a set for fast containment check
	inSCC := make(map[int64]bool, len(scc))
	for _, n := range scc {
		inSCC[n.ID()] = true
	}

	// Pre-compute and sort adjacency lists for nodes in SCC
	// This avoids repeated filtering and sorting during traversal
	adj := make(map[int64][]graph.Node, len(scc))
	for _, u := range scc {
		to := g.From(u.ID())
		var neighbors []graph.Node
		for to.Next() {
			n := to.Node()
			if inSCC[n.ID()] {
				neighbors = append(neighbors, n)
			}
		}
		sort.Slice(neighbors, func(i, j int) bool {
			return neighbors[i].ID() < neighbors[j].ID()
		})
		adj[u.ID()] = neighbors
	}

	// Iterative DFS state
	visited := make(map[int64]bool)
	onStack := make(map[int64]bool)
	stackPos := make(map[int64]int) // O(1) lookup for position in stack
	stack := []graph.Node{}

	// Track neighbor index for each node on stack to resume iteration
	neighborIndex := make(map[int64]int)

	if len(scc) > 0 {
		stackPos[scc[0].ID()] = 0 // Track initial node position
		stack = append(stack, scc[0])
	}

	for len(stack) > 0 {
		u := stack[len(stack)-1] // Peek
		uID := u.ID()

		if !visited[uID] {
			visited[uID] = true
			onStack[uID] = true
		}

		// Get neighbors
		neighbors := adj[uID]
		idx := neighborIndex[uID]

		if idx < len(neighbors) {
			v := neighbors[idx]
			neighborIndex[uID]++ // Advance for next iteration

			if onStack[v.ID()] {
				// Cycle found! Reconstruct path from v to u then close with v
				// Use O(1) lookup instead of linear search
				if stackIdx, ok := stackPos[v.ID()]; ok {
					cycle := append([]graph.Node{}, stack[stackIdx:]...)
					cycle = append(cycle, v) // Close the loop
					return cycle
				}
			}

			if !visited[v.ID()] {
				stackPos[v.ID()] = len(stack) // Track position before append
				stack = append(stack, v)
			}
		} else {
			// All neighbors visited, backtrack
			onStack[uID] = false
			delete(stackPos, uID) // Remove position tracking
			stack = stack[:len(stack)-1]
			delete(neighborIndex, uID)
		}
	}

	return nil
}
