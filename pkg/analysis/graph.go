package analysis

import (
	"math"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

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
	OutDegree         map[string]int     // Number of dependencies this issue has (edges out)
	InDegree          map[string]int     // Number of issues that depend on this issue (edges in)
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
	// We only model *blocking* relationships in the analysis graph. Non-blocking
	// links such as "related" should not influence centrality metrics or cycle
	// detection because they do not gate execution order. Treat parent-child as a
	// blocking relationship to preserve hierarchical execution ordering.
	for _, issue := range issues {
		u, ok := idToNode[issue.ID]
		if !ok {
			continue
		}

		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}

			depType := dep.Type
			if depType == "" {
				depType = model.DepBlocks // Default legacy/unspecified deps to blocking
			}

			if depType != model.DepBlocks {
				continue // Ignore non-blocking links (related, parent-child, discovered-from, etc.)
			}

			v, exists := idToNode[dep.DependsOnID]
			if exists {
				// Issue (u) depends on v → edge u -> v
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

	// Handle empty graph - return empty stats without calling gonum functions
	// that panic on zero-length matrices
	if len(a.issueMap) == 0 {
		return stats
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
	// Only run if graph has edges (HITS can hang on graphs with no edges or
	// certain sparse topologies). We run with a timeout to avoid blocking.
	if a.g.Edges().Len() > 0 {
		type hitsResult struct {
			data map[int64]network.HubAuthority
		}
		done := make(chan hitsResult, 1)
		go func() {
			hubAuth := network.HITS(a.g, 1e-3) // Relaxed tolerance for faster convergence
			done <- hitsResult{data: hubAuth}
		}()

		select {
		case result := <-done:
			for id, ha := range result.data {
				stats.Hubs[a.nodeToID[id]] = ha.Hub
				stats.Authorities[a.nodeToID[id]] = ha.Authority
			}
		case <-time.After(500 * time.Millisecond):
			// HITS timed out - skip these metrics rather than hanging
		}
	}

	// 4. Cycles (with timeout to avoid hanging on complex graphs)
	type cyclesResult struct {
		cycles [][]graph.Node
	}
	cyclesDone := make(chan cyclesResult, 1)
	go func() {
		cyclesDone <- cyclesResult{cycles: topo.DirectedCyclesIn(a.g)}
	}()

	select {
	case result := <-cyclesDone:
		for _, cycle := range result.cycles {
			var cycleIDs []string
			for _, n := range cycle {
				cycleIDs = append(cycleIDs, a.nodeToID[n.ID()])
			}
			stats.Cycles = append(stats.Cycles, cycleIDs)
		}
	case <-time.After(500 * time.Millisecond):
		// Cycle detection timed out - skip
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

// GetActionableIssues returns issues that can be worked on immediately.
// An issue is actionable if:
// 1. It is not closed
// 2. All its blocking dependencies (type "blocks") are closed
// Missing blockers don't block (graceful degradation).
func (a *Analyzer) GetActionableIssues() []model.Issue {
	var actionable []model.Issue

	for _, issue := range a.issueMap {
		// Skip closed issues - they're done
		if issue.Status == model.StatusClosed {
			continue
		}

		// Check all blocking dependencies
		isBlocked := false
		for _, dep := range issue.Dependencies {
			// Only "blocks" type creates hard dependencies
			if dep.Type != model.DepBlocks {
				continue
			}

			// Look up the blocker
			blocker, exists := a.issueMap[dep.DependsOnID]
			if !exists {
				// Missing blocker doesn't block (graceful degradation)
				continue
			}

			// If blocker is not closed, this issue is blocked
			if blocker.Status != model.StatusClosed {
				isBlocked = true
				break
			}
		}

		if !isBlocked {
			actionable = append(actionable, issue)
		}
	}

	return actionable
}

// GetIssue returns a single issue by ID, or nil if not found
func (a *Analyzer) GetIssue(id string) *model.Issue {
	if issue, ok := a.issueMap[id]; ok {
		return &issue
	}
	return nil
}

// GetBlockers returns the IDs of issues that block the given issue
// (only considers "blocks" type dependencies)
func (a *Analyzer) GetBlockers(issueID string) []string {
	issue, ok := a.issueMap[issueID]
	if !ok {
		return nil
	}

	var blockers []string
	for _, dep := range issue.Dependencies {
		if dep.Type == model.DepBlocks {
			if _, exists := a.issueMap[dep.DependsOnID]; exists {
				blockers = append(blockers, dep.DependsOnID)
			}
		}
	}
	return blockers
}

// GetOpenBlockers returns the IDs of non-closed issues that block the given issue
func (a *Analyzer) GetOpenBlockers(issueID string) []string {
	issue, ok := a.issueMap[issueID]
	if !ok {
		return nil
	}

	var openBlockers []string
	for _, dep := range issue.Dependencies {
		if dep.Type == model.DepBlocks {
			if blocker, exists := a.issueMap[dep.DependsOnID]; exists {
				if blocker.Status != model.StatusClosed {
					openBlockers = append(openBlockers, dep.DependsOnID)
				}
			}
		}
	}
	return openBlockers
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
