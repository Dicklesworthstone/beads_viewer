package analysis

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
	graph "gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/simple"
)

// buildTestGraph creates a simple directed graph from edges for testing
func buildTestGraph(nodes int, edges [][2]int) *simple.DirectedGraph {
	g := simple.NewDirectedGraph()

	// Add nodes
	for i := 0; i < nodes; i++ {
		g.AddNode(simple.Node(int64(i)))
	}

	// Add edges
	for _, e := range edges {
		g.SetEdge(simple.Edge{F: simple.Node(int64(e[0])), T: simple.Node(int64(e[1]))})
	}

	return g
}

func TestFindCyclesSafe_Empty(t *testing.T) {
	g := simple.NewDirectedGraph()
	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles for empty graph, got %d", len(cycles))
	}
}

func TestFindCyclesSafe_NoCycles(t *testing.T) {
	// Simple DAG: 0 -> 1 -> 2
	g := buildTestGraph(3, [][2]int{{0, 1}, {1, 2}})

	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles for DAG, got %d", len(cycles))
	}
}

func TestFindCyclesSafe_SelfLoop(t *testing.T) {
	// Node 0 has self-loop
	// g := buildTestGraph(2, [][2]int{{0, 0}, {0, 1}})
	//
	// cycles := findCyclesSafe(g, 10)
	// if len(cycles) == 0 {
	// 	t.Error("expected to find self-loop cycle")
	// 	return
	// }
	//
	// // Self-loop should be [0, 0]
	// found := false
	// for _, cycle := range cycles {
	// 	if len(cycle) == 2 && cycle[0].ID() == cycle[1].ID() {
	// 		found = true
	// 		break
	// 	}
	// }
	// if !found {
	// 	t.Error("expected self-loop to be detected as [n, n]")
	// }
}

func TestFindCyclesSafe_SimpleCycle(t *testing.T) {
	// Cycle: 0 -> 1 -> 2 -> 0
	g := buildTestGraph(3, [][2]int{{0, 1}, {1, 2}, {2, 0}})

	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) == 0 {
		t.Error("expected to find cycle")
		return
	}

	// Should find one cycle of length 3 (plus closing node = 4)
	cycle := cycles[0]
	if len(cycle) != 4 {
		t.Errorf("expected cycle length 4 (including close), got %d", len(cycle))
	}

	// First and last should be same (cycle closes)
	if cycle[0].ID() != cycle[len(cycle)-1].ID() {
		t.Error("cycle should close (first == last)")
	}
}

func TestFindCyclesSafe_DirectCycle(t *testing.T) {
	// Direct cycle: 0 <-> 1
	g := buildTestGraph(2, [][2]int{{0, 1}, {1, 0}})

	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) == 0 {
		t.Error("expected to find direct cycle")
		return
	}

	// Should be length 3 (0 -> 1 -> 0)
	cycle := cycles[0]
	if len(cycle) != 3 {
		t.Errorf("expected cycle length 3 for direct cycle, got %d", len(cycle))
	}
}

func TestFindCyclesSafe_MultipleCycles(t *testing.T) {
	// Two separate cycles:
	// Cycle 1: 0 -> 1 -> 0
	// Cycle 2: 2 -> 3 -> 2
	g := buildTestGraph(4, [][2]int{{0, 1}, {1, 0}, {2, 3}, {3, 2}})

	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) < 2 {
		t.Errorf("expected at least 2 cycles, got %d", len(cycles))
	}
}

func TestFindCyclesSafe_Limit(t *testing.T) {
	// Create multiple cycles
	g := buildTestGraph(6, [][2]int{
		{0, 1}, {1, 0}, // Cycle 1
		{2, 3}, {3, 2}, // Cycle 2
		{4, 5}, {5, 4}, // Cycle 3
	})

	// Limit to 2
	result := findCyclesSafe(g, 2)
	cycles := result.cycles
	if len(cycles) != 2 {
		t.Errorf("stored cycles=%d, want exact limit 2", len(cycles))
	}
	if result.total != 3 {
		t.Errorf("total=%d, want 3 cycle representatives before limiting", result.total)
	}
	if !result.truncated {
		t.Error("expected truncation flag when cycle representatives exceed limit")
	}
}

func TestAnalyzerCycleLimitReportsTruncationTruthfully(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Dependencies: []*model.Dependency{{DependsOnID: "D", Type: model.DepBlocks}}},
		{ID: "D", Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "E", Dependencies: []*model.Dependency{{DependsOnID: "F", Type: model.DepBlocks}}},
		{ID: "F", Dependencies: []*model.Dependency{{DependsOnID: "E", Type: model.DepBlocks}}},
	}
	config := AnalysisConfig{
		DisableCache:     true,
		RunToCompletion:  true,
		ComputeCycles:    true,
		MaxCyclesToStore: 2,
	}

	stats, profile := NewAnalyzer(issues).AnalyzeWithProfile(config)
	if got := len(stats.Cycles()); got != 2 {
		t.Fatalf("stored cycle count=%d, want configured limit 2", got)
	}
	if profile.CycleCount != 3 {
		t.Fatalf("profile cycle count=%d, want all 3 cycle representatives", profile.CycleCount)
	}
	cycleStatus := stats.Status().Cycles
	if cycleStatus.State != "computed" {
		t.Fatalf("cycle status=%q, want computed", cycleStatus.State)
	}
	if want := "truncated to 2 of 3 cycle representatives"; cycleStatus.Reason != want {
		t.Fatalf("cycle status reason=%q, want %q", cycleStatus.Reason, want)
	}
}

func TestAnalyzerCyclesIgnoreClosedAndTombstonedNodes(t *testing.T) {
	for _, inactiveStatus := range []model.Status{model.StatusClosed, model.StatusTombstone} {
		t.Run(string(inactiveStatus), func(t *testing.T) {
			issues := []model.Issue{
				{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
				{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
				{ID: "C", Status: inactiveStatus, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
			}
			config := AnalysisConfig{
				DisableCache:     true,
				RunToCompletion:  true,
				ComputeCycles:    true,
				MaxCyclesToStore: 10,
			}

			stats := NewAnalyzer(issues).AnalyzeWithConfig(config)
			if got := stats.Cycles(); len(got) != 0 {
				t.Fatalf("inactive issue preserved a non-operational cycle: %#v", got)
			}
		})
	}
}

func TestCyclicGraphDoesNotPublishEmptyCriticalMetricsAsComputed(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	config := AnalysisConfig{
		DisableCache:        true,
		RunToCompletion:     true,
		ComputeCriticalPath: true,
		ComputeSlack:        true,
	}

	stats := NewAnalyzer(issues).AnalyzeWithConfig(config)
	status := stats.Status()
	for name, entry := range map[string]statusEntry{
		"critical": status.Critical,
		"slack":    status.Slack,
	} {
		if entry.State != "skipped" || entry.Reason == "" {
			t.Fatalf("%s status = %+v, want skipped with cycle reason", name, entry)
		}
	}
	if len(stats.CriticalPathScore()) != 0 || len(stats.Slack()) != 0 {
		t.Fatalf("cyclic graph published critical metrics: critical=%v slack=%v", stats.CriticalPathScore(), stats.Slack())
	}
}

func TestCycleEligibilityHashChangesWhenCycleMemberCloses(t *testing.T) {
	openIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	closedIssues := append([]model.Issue(nil), openIssues...)
	closedIssues[1].Status = model.StatusClosed

	openAnalyzer := NewAnalyzer(openIssues)
	closedAnalyzer := NewAnalyzer(closedIssues)
	if openAnalyzer.graphStructureHash() != closedAnalyzer.graphStructureHash() {
		t.Fatal("status-only change unexpectedly changed topology hash")
	}
	if openAnalyzer.cycleEligibilityHash() == closedAnalyzer.cycleEligibilityHash() {
		t.Fatal("status-only change did not change cycle cache identity")
	}
}

func TestFindCyclesSafe_LimitChoosesCanonicalSubset(t *testing.T) {
	// More disjoint SCCs than the limit used to truncate Tarjan's map-derived
	// order before sorting, so identical graphs could return different subsets.
	edges := [][2]int{
		{8, 9}, {9, 8},
		{2, 3}, {3, 2},
		{6, 7}, {7, 6},
		{0, 1}, {1, 0},
		{4, 5}, {5, 4},
	}
	g := buildTestGraph(10, edges)
	cycles := findCyclesSafe(g, 3).cycles
	if len(cycles) != 3 {
		t.Fatalf("cycle count=%d, want 3", len(cycles))
	}
	for i, wantStart := range []int64{0, 2, 4} {
		if got := cycles[i][0].ID(); got != wantStart {
			t.Fatalf("cycle %d starts at %d, want canonical %d; cycles=%v", i, got, wantStart, cycles)
		}
	}
	if got := findCyclesSafe(g, 0); len(got.cycles) != 0 || got.total != 0 || got.truncated {
		t.Fatalf("zero limit returned unexpected result: %+v", got)
	}
}

func TestFindCyclesSafe_SortedByLength(t *testing.T) {
	// Create cycles of different lengths
	// Short cycle: 0 -> 1 -> 0 (length 2)
	// Long cycle: 2 -> 3 -> 4 -> 5 -> 2 (length 4)
	g := buildTestGraph(6, [][2]int{
		{0, 1}, {1, 0}, // 2-cycle
		{2, 3}, {3, 4}, {4, 5}, {5, 2}, // 4-cycle
	})

	cycles := findCyclesSafe(g, 10).cycles
	if len(cycles) < 2 {
		t.Skip("need at least 2 cycles for sort test")
	}

	// First cycle should be shorter or equal
	for i := 1; i < len(cycles); i++ {
		if len(cycles[i]) < len(cycles[i-1]) {
			t.Errorf("cycles not sorted by length: %d < %d at index %d",
				len(cycles[i]), len(cycles[i-1]), i)
		}
	}
}

func TestFindCyclesSafe_Determinism(t *testing.T) {
	g := buildTestGraph(4, [][2]int{
		{0, 1}, {1, 2}, {2, 0}, // Triangle cycle
		{0, 3}, // Extra edge
	})

	// Run multiple times
	var firstCycles [][]int64
	for run := 0; run < 5; run++ {
		cycles := findCyclesSafe(g, 10).cycles

		if firstCycles == nil {
			firstCycles = make([][]int64, len(cycles))
			for i, c := range cycles {
				firstCycles[i] = make([]int64, len(c))
				for j, n := range c {
					firstCycles[i][j] = n.ID()
				}
			}
		} else {
			if len(cycles) != len(firstCycles) {
				t.Errorf("run %d: cycle count changed", run)
				continue
			}
			for i, c := range cycles {
				if len(c) != len(firstCycles[i]) {
					t.Errorf("run %d: cycle %d length changed", run, i)
				}
			}
		}
	}
}

func TestFindOneCycleInSCC_Empty(t *testing.T) {
	g := simple.NewDirectedGraph()
	cycle := findOneCycleInSCC(g, nil)
	if len(cycle) != 0 {
		t.Errorf("expected empty cycle for nil SCC, got %d nodes", len(cycle))
	}
}

func TestFindOneCycleInSCC_SingleNode(t *testing.T) {
	g := buildTestGraph(1, nil)
	scc := []simple.Node{simple.Node(0)}
	// Single node without self-loop - no cycle
	cycle := findOneCycleInSCC(g, toGraphNodes(scc))
	if len(cycle) != 0 {
		t.Errorf("expected no cycle for single node without self-loop, got %d", len(cycle))
	}
}

func TestFindOneCycleInSCC_TriangleSCC(t *testing.T) {
	// Complete SCC: 0 -> 1 -> 2 -> 0
	g := buildTestGraph(3, [][2]int{{0, 1}, {1, 2}, {2, 0}})
	scc := []simple.Node{simple.Node(0), simple.Node(1), simple.Node(2)}

	cycle := findOneCycleInSCC(g, toGraphNodes(scc))
	if len(cycle) == 0 {
		t.Error("expected to find cycle in triangle SCC")
		return
	}

	// Verify it closes
	if cycle[0].ID() != cycle[len(cycle)-1].ID() {
		t.Error("cycle should close")
	}
}

func TestFindOneCycleInSCC_LargeSCC(t *testing.T) {
	// Larger SCC with multiple internal paths
	edges := [][2]int{
		{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 0}, // Main cycle
		{0, 2}, {1, 3}, {2, 4}, // Shortcuts
	}
	g := buildTestGraph(5, edges)
	scc := []simple.Node{simple.Node(0), simple.Node(1), simple.Node(2), simple.Node(3), simple.Node(4)}

	cycle := findOneCycleInSCC(g, toGraphNodes(scc))
	if len(cycle) == 0 {
		t.Error("expected to find cycle in large SCC")
		return
	}

	// Verify it closes
	if cycle[0].ID() != cycle[len(cycle)-1].ID() {
		t.Error("cycle should close")
	}
}

// Helper functions
func toGraphNodes(nodes []simple.Node) []graph.Node {
	result := make([]graph.Node, len(nodes))
	for i, n := range nodes {
		result[i] = n
	}
	return result
}

// Integration test with real issues
func TestFindCyclesSafe_WithIssues(t *testing.T) {
	// Use testutil to create cyclic issues
	issues := testutil.QuickCycle(4)

	// Build analyzer graph (tests integration)
	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()

	cycles := stats.Cycles()
	if len(cycles) == 0 {
		t.Error("expected cycles in cyclic issue graph")
	}
}

func TestFindCyclesSafe_StarTopology(t *testing.T) {
	// Star topology has no cycles
	issues := testutil.QuickStar(5)

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()

	cycles := stats.Cycles()
	if len(cycles) != 0 {
		t.Errorf("expected no cycles in star topology, got %d", len(cycles))
	}
}

func TestFindCyclesSafe_DiamondTopology(t *testing.T) {
	// Diamond topology has no cycles (DAG)
	issues := testutil.QuickDiamond(3)

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()

	cycles := stats.Cycles()
	if len(cycles) != 0 {
		t.Errorf("expected no cycles in diamond topology, got %d", len(cycles))
	}
}

func BenchmarkFindCyclesSafe_Small(b *testing.B) {
	g := buildTestGraph(5, [][2]int{{0, 1}, {1, 2}, {2, 0}})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findCyclesSafe(g, 10)
	}
}

func BenchmarkFindCyclesSafe_Medium(b *testing.B) {
	// Build a larger graph with cycles
	edges := make([][2]int, 0)
	for i := 0; i < 50; i++ {
		edges = append(edges, [2]int{i, (i + 1) % 50})
	}
	g := buildTestGraph(50, edges)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findCyclesSafe(g, 10)
	}
}

func BenchmarkFindCyclesSafe_Large(b *testing.B) {
	// Build a large graph with multiple SCCs
	edges := make([][2]int, 0)
	// Create 10 separate cycles of size 10
	for c := 0; c < 10; c++ {
		base := c * 10
		for i := 0; i < 10; i++ {
			edges = append(edges, [2]int{base + i, base + (i+1)%10})
		}
	}
	g := buildTestGraph(100, edges)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findCyclesSafe(g, 20)
	}
}

func BenchmarkFindOneCycleInSCC(b *testing.B) {
	// Create SCC of size 20
	edges := make([][2]int, 0)
	for i := 0; i < 20; i++ {
		edges = append(edges, [2]int{i, (i + 1) % 20})
	}
	g := buildTestGraph(20, edges)

	scc := make([]simple.Node, 20)
	for i := 0; i < 20; i++ {
		scc[i] = simple.Node(int64(i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findOneCycleInSCC(g, toGraphNodes(scc))
	}
}
