package analysis

import (
	"math"

	"beads_viewer/pkg/model"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/network"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"
)

// GraphStats holds the results of graph analysis
type GraphStats struct {
	PageRank          map[string]float64
	Betweenness       map[string]float64
	Eigenvector       map[string]float64
	Hubs              map[string]float64
	Authorities       map[string]float64
	OutDegree         map[string]int     // Number of issues blocked by this issue
	InDegree          map[string]int     // Number of dependencies this issue has
	CriticalPathScore map[string]float64 // Heuristic for critical path
	Cycles            [][]string
	Density           float64
	TopologicalOrder  []string
}

// Analyzer encapsulates the graph logic
type Analyzer struct {
	g        *simple.DirectedGraph
	idToNode map[string]int64
	nodeToID map[int64]string
	issueMap map[string]model.Issue
}

func NewAnalyzer(issues []model.Issue) *Analyzer {
	g := simple.NewDirectedGraph()
	// Pre-allocate maps for efficiency
	idToNode := make(map[string]int64, len(issues))
	nodeToID := make(map[int64]string, len(issues))
	issueMap := make(map[string]model.Issue, len(issues))

	// 1. Add Nodes
	for _, issue := range issues {
		issueMap[issue.ID] = issue
		n := g.NewNode()
		g.AddNode(n)
		idToNode[issue.ID] = n.ID()
		nodeToID[n.ID()] = issue.ID
	}

	// 2. Add Edges (Dependency Direction)
	// If A depends on B, B blocks A.
	// Edge: B -> A (Blocker -> Blocked)
	// This way, PageRank flows from Blockers to Blocked.
	// High PageRank = "Highly Blocked" (Fragile).
	// REVERSE Logic for "Criticality":
	// If we want "Critical" tasks to have high scores, we should flow FROM Blocked TO Blocker?
	// Or just use OutDegree (Blocks count).
	// Let's stick to natural flow: B -> A means B "causes" A.
	// Wait, usually dependency graph is A -> B (A depends on B).
	// Let's use: Edge A -> B means A DEPENDS ON B.
	// Then High In-Degree = Many things depend on me (I am a blocker).
	// High Out-Degree = I depend on many things (I am blocked).

	for _, issue := range issues {
		u, ok := idToNode[issue.ID]
		if !ok {
			continue
		} // Should not happen

		for _, dep := range issue.Dependencies {
			v, exists := idToNode[dep.DependsOnID]
			if exists {
				// Issue (u) DependsOn (v)
				// Edge: u -> v
				// Meaning: "Control flows from u to v" (u needs v)
				g.SetEdge(g.NewEdge(g.Node(u), g.Node(v)))
			}
		}
	}

	return &Analyzer{
		g:        g,
		idToNode: idToNode,
		nodeToID: nodeToID,
		issueMap: issueMap,
	}
}

func (a *Analyzer) Analyze() GraphStats {
	stats := GraphStats{
		PageRank:          make(map[string]float64),
		Betweenness:       make(map[string]float64),
		Eigenvector:       make(map[string]float64),
		Hubs:              make(map[string]float64),
		Authorities:       make(map[string]float64),
		OutDegree:         make(map[string]int),
		InDegree:          make(map[string]int),
		CriticalPathScore: make(map[string]float64),
	}

	nodes := a.g.Nodes()

	// 1. Basic Degree Centrality
	// In our graph A->B (A depends on B):
	// In-Degree: Nodes pointing TO me. (Who depends on me?) -> Wait, edges are A->B.
	// A->B. A has out-degree 1 (to B). B has in-degree 1 (from A).
	// So:
	// In-Degree of B = Count of nodes that depend on B (Importance/Blocker score).
	// Out-Degree of A = Count of nodes A depends on (Fragility/Blocked score).
	for nodes.Next() {
		n := nodes.Node()
		id := a.nodeToID[n.ID()]

		to := a.g.To(n.ID())
		stats.InDegree[id] = to.Len() // Issues depending on me

		from := a.g.From(n.ID())
		stats.OutDegree[id] = from.Len() // Issues I depend on
	}
	nodes.Reset()

	// 2. PageRank
	// PageRank on A->B (Dependency) means "authority" flows to B.
	// High PageRank = Fundamental Dependencies (Deep Blockers).
	pr := network.PageRank(a.g, 0.85, 1e-6)
	for id, score := range pr {
		stats.PageRank[a.nodeToID[id]] = score
	}

	// 3. Betweenness Centrality
	// Nodes that bridge clusters.
	bw := network.Betweenness(a.g)
	for id, score := range bw {
		stats.Betweenness[a.nodeToID[id]] = score
	}

	// 3b. Eigenvector Centrality (influence via influential neighbors)
	for id, score := range computeEigenvector(a.g) {
		stats.Eigenvector[a.nodeToID[id]] = score
	}

	// 3c. HITS (hubs/authorities) captures dependency vs depended-on roles
	hubAuth := network.HITS(a.g, 1e-8)
	for id, ha := range hubAuth {
		stats.Hubs[a.nodeToID[id]] = ha.Hub
		stats.Authorities[a.nodeToID[id]] = ha.Authority
	}

	// 4. Cycles
	cycles := topo.DirectedCyclesIn(a.g)
	for _, cycle := range cycles {
		var cycleIDs []string
		for _, n := range cycle {
			cycleIDs = append(cycleIDs, a.nodeToID[n.ID()])
		}
		stats.Cycles = append(stats.Cycles, cycleIDs)
	}

	// 5. Topological Sort (Linear Order)
	sorted, err := topo.Sort(a.g)
	if err == nil {
		// Sort returns roughly "execution order".
		// ... (comments) ...
		for i := len(sorted) - 1; i >= 0; i-- {
			stats.TopologicalOrder = append(stats.TopologicalOrder, a.nodeToID[sorted[i].ID()])
		}

		// 6. Critical Path Heuristic (DAG only)
		stats.CriticalPathScore = a.computeHeights(sorted)
	}

	// 7. Density
	n := float64(len(stats.PageRank))
	e := float64(a.g.Edges().Len())
	if n > 1 {
		stats.Density = e / (n * (n - 1))
	}

	return stats
}

func (a *Analyzer) computeHeights(sorted []graph.Node) map[string]float64 {
	heights := make(map[int64]float64)
	// sorted is already a valid topological sort of a.g

	impactScores := make(map[string]float64)

	// Iterate forward: u depends on v (u -> v)
	// u comes before v in topological sort.
	// We want to calculate "Impact Depth": How many layers above depend on me?
	// This equates to "Depth from Root" where Root is the top-level dependent task.
	// Roots (InDegree 0) have Impact 1.
	// If u -> v, v's impact = 1 + Impact(u).

	for _, n := range sorted {
		nid := n.ID()
		maxParentHeight := 0.0

		// To(n) gives nodes p such that p -> n.
		// p depends on n. p is a parent/dependent.
		// Since p comes before n in sort, p is already processed.
		to := a.g.To(nid)
		for to.Next() {
			p := to.Node()
			if h, ok := heights[p.ID()]; ok {
				if h > maxParentHeight {
					maxParentHeight = h
				}
			}
		}
		heights[nid] = 1.0 + maxParentHeight
		impactScores[a.nodeToID[nid]] = heights[nid]
	}

	return impactScores
}

// computeEigenvector runs a simple power-iteration to estimate eigenvector centrality.
// Uses incoming edges so nodes that are depended on by many important nodes score higher.
// It is deliberately lightweight to keep startup fast.
func computeEigenvector(g graph.Directed) map[int64]float64 {
	nodes := g.Nodes()
	var nodeList []graph.Node
	for nodes.Next() {
		nodeList = append(nodeList, nodes.Node())
	}
	n := len(nodeList)
	if n == 0 {
		return nil
	}

	vec := make([]float64, n)
	for i := range vec {
		vec[i] = 1.0 / float64(n)
	}
	work := make([]float64, n)

	index := make(map[int64]int, n)
	for i, node := range nodeList {
		index[node.ID()] = i
	}

	const iterations = 50
	for iter := 0; iter < iterations; iter++ {
		for i := range work {
			work[i] = 0
		}
		for _, node := range nodeList {
			i := index[node.ID()]
			incoming := g.To(node.ID())
			for incoming.Next() {
				j := index[incoming.Node().ID()]
				work[i] += vec[j]
			}
		}
		// normalize
		sum := 0.0
		for _, v := range work {
			sum += v * v
		}
		if sum == 0 {
			break
		}
		norm := 1 / math.Sqrt(sum)
		for i := range work {
			vec[i] = work[i] * norm
		}
	}

	res := make(map[int64]float64, n)
	for i, node := range nodeList {
		res[node.ID()] = vec[i]
	}
	return res
}
