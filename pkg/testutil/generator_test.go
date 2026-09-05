package testutil

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestPerformanceMeasurementControls(t *testing.T) {
	values := make([]time.Duration, 1000)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Microsecond
	}
	summary, err := SummarizeLatency(values)
	if err != nil || summary.Samples != 1000 || summary.P50MS != .5 || summary.P95MS != .95 || summary.P99MS != .99 || summary.MaxMS != 1 {
		t.Fatalf("nearest-rank distribution = %+v, error=%v", summary, err)
	}
	if err := CheckInteractionLatency(values); err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeLatency(nil); err == nil {
		t.Fatal("empty samples accepted")
	}
	if _, err := SummarizeLatency([]time.Duration{-1}); err == nil {
		t.Fatal("negative latency accepted")
	}
	entries := make(map[string]map[string]any)
	for _, name := range []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"} {
		entries[name] = map[string]any{"state": "computed", "ms": 42}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := PerformanceMetricStates(raw)
	if err != nil || bytes.Contains(complete, []byte(`"ms"`)) {
		t.Fatalf("metric timing removal failed: %s, %v", complete, err)
	}
	entries["PageRank"] = map[string]any{"state": "skipped", "reason": "injected negative"}
	raw, err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	degraded, err := PerformanceMetricStates(raw)
	if err != nil || bytes.Equal(complete, degraded) || !bytes.Contains(degraded, []byte("injected negative")) {
		t.Fatalf("skipped metric disappeared from parity: %s, %v", degraded, err)
	}
	delete(entries, "Cycles")
	raw, err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PerformanceMetricStates(raw); err == nil {
		t.Fatal("missing metric passed parity prerequisites")
	}
}

func TestPerformanceIssuesFrozenWorkloads(t *testing.T) {
	for _, name := range PerformanceWorkloadNames() {
		t.Run(name, func(t *testing.T) {
			issues, err := PerformanceIssues(name, 1000, 20260904)
			if err != nil {
				t.Fatal(err)
			}
			AssertIssueCount(t, issues, 1000)
			AssertNoDuplicateIDs(t, issues)
			AssertAllValid(t, issues)
			repeated, err := PerformanceIssues(name, 1000, 20260904)
			if err != nil {
				t.Fatal(err)
			}
			first, err := json.Marshal(issues)
			if err != nil {
				t.Fatal(err)
			}
			second, err := json.Marshal(repeated)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("same seed and fixed clock changed serialized workload")
			}
			known := make(map[string]bool, len(issues))
			for _, issue := range issues {
				known[issue.ID] = true
			}
			for _, issue := range issues {
				for _, dependency := range issue.Dependencies {
					if dependency.IssueID != issue.ID || dependency.DependsOnID == issue.ID || !known[dependency.DependsOnID] {
						t.Fatalf("invalid workload dependency: %+v", dependency)
					}
				}
			}
			switch name {
			case "deep-chain":
				for i := 1; i < len(issues); i++ {
					if len(issues[i].Dependencies) != 1 || issues[i].Dependencies[0].DependsOnID != issues[i-1].ID {
						t.Fatalf("chain broken at %s", issues[i].ID)
					}
				}
			case "wide-dag":
				AssertNoCycles(t, issues)
				if len(issues[32].Dependencies) != 2 || len(issues[0].Dependencies) != 0 {
					t.Fatal("wide DAG lost its root/leaf structure")
				}
			case "cyclic-dense":
				AssertHasCycle(t, issues)
				if len(issues[0].Dependencies) != 12 {
					t.Fatal("dense component lost its edge fanout")
				}
			case "mostly-closed":
				closed := 0
				for _, issue := range issues {
					if issue.Status == model.StatusClosed {
						closed++
					}
				}
				if closed != 950 {
					t.Fatalf("closed count=%d, want 950", closed)
				}
			case "unicode":
				if !strings.Contains(issues[0].Title, "日本語") || len(issues[0].Description) < 4000 {
					t.Fatal("long Unicode workload lost its text stressor")
				}
			}
		})
	}
	if _, err := PerformanceIssues("unknown", 1000, 1); err == nil {
		t.Fatal("unknown workload accepted")
	}
	if _, err := PerformanceIssues("realistic", 1, 1); err == nil {
		t.Fatal("single-item workload accepted despite navigation requirement")
	}
}

func TestChain(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		size      int
		wantNodes int
		wantEdges int
		wantDepth int
	}{
		{"chain_1", 1, 1, 0, 0},
		{"chain_2", 2, 2, 1, 1},
		{"chain_5", 5, 5, 4, 4},
		{"chain_10", 10, 10, 9, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Chain(tt.size)

			if len(gf.Nodes) != tt.wantNodes {
				t.Errorf("Chain(%d) nodes = %d, want %d", tt.size, len(gf.Nodes), tt.wantNodes)
			}
			if len(gf.Edges) != tt.wantEdges {
				t.Errorf("Chain(%d) edges = %d, want %d", tt.size, len(gf.Edges), tt.wantEdges)
			}
			if gf.Properties.HasCycles {
				t.Error("Chain should not have cycles")
			}
			if !gf.Properties.IsConnected {
				t.Error("Chain should be connected")
			}
			if gf.Properties.ExpectedDepth != tt.wantDepth {
				t.Errorf("Chain(%d) depth = %d, want %d", tt.size, gf.Properties.ExpectedDepth, tt.wantDepth)
			}

			// Verify edge connectivity: edge i should be [i+1, i] (node i+1 depends on node i)
			for i, e := range gf.Edges {
				if e[0] != i+1 || e[1] != i {
					t.Errorf("Edge %d: got [%d,%d], want [%d,%d]", i, e[0], e[1], i+1, i)
				}
			}
		})
	}
}

func TestStar(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		spokes    int
		wantNodes int
		wantEdges int
	}{
		{"star_1", 1, 2, 1},
		{"star_5", 5, 6, 5},
		{"star_10", 10, 11, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Star(tt.spokes)

			if len(gf.Nodes) != tt.wantNodes {
				t.Errorf("Star(%d) nodes = %d, want %d", tt.spokes, len(gf.Nodes), tt.wantNodes)
			}
			if len(gf.Edges) != tt.wantEdges {
				t.Errorf("Star(%d) edges = %d, want %d", tt.spokes, len(gf.Edges), tt.wantEdges)
			}

			// Hub should be node 0
			if gf.Nodes[0] != "hub" {
				t.Errorf("Star hub should be 'hub', got %s", gf.Nodes[0])
			}

			// All edges should point TO hub (index 0)
			for i, e := range gf.Edges {
				if e[1] != 0 {
					t.Errorf("Edge %d target should be hub (0), got %d", i, e[1])
				}
			}
		})
	}
}

func TestReverseStar(t *testing.T) {
	gen := NewDefault()
	gf := gen.ReverseStar(5)

	// All edges should point FROM hub (index 0)
	for i, e := range gf.Edges {
		if e[0] != 0 {
			t.Errorf("Edge %d source should be hub (0), got %d", i, e[0])
		}
	}
}

func TestDiamond(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		width     int
		wantNodes int
		wantEdges int
	}{
		{"diamond_1", 1, 3, 2},  // top + 1 mid + bottom, 2 edges
		{"diamond_2", 2, 4, 4},  // top + 2 mid + bottom, 4 edges
		{"diamond_5", 5, 7, 10}, // top + 5 mid + bottom, 10 edges
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Diamond(tt.width)

			if len(gf.Nodes) != tt.wantNodes {
				t.Errorf("Diamond(%d) nodes = %d, want %d", tt.width, len(gf.Nodes), tt.wantNodes)
			}
			if len(gf.Edges) != tt.wantEdges {
				t.Errorf("Diamond(%d) edges = %d, want %d", tt.width, len(gf.Edges), tt.wantEdges)
			}
			if gf.Properties.ExpectedDepth != 2 {
				t.Errorf("Diamond depth should be 2, got %d", gf.Properties.ExpectedDepth)
			}
		})
	}
}

func TestCycle(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		size      int
		wantEdges int
	}{
		{"cycle_2", 2, 2},
		{"cycle_3", 3, 3},
		{"cycle_5", 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Cycle(tt.size)

			if len(gf.Edges) != tt.wantEdges {
				t.Errorf("Cycle(%d) edges = %d, want %d", tt.size, len(gf.Edges), tt.wantEdges)
			}
			if !gf.Properties.HasCycles {
				t.Error("Cycle should have cycles")
			}

			// Verify cycle connectivity
			lastEdge := gf.Edges[len(gf.Edges)-1]
			if lastEdge[1] != 0 {
				t.Errorf("Last edge should point back to n0, points to %d", lastEdge[1])
			}
		})
	}
}

func TestSelfLoop(t *testing.T) {
	gen := NewDefault()
	gf := gen.SelfLoop()

	if len(gf.Nodes) != 1 {
		t.Errorf("SelfLoop should have 1 node, got %d", len(gf.Nodes))
	}
	if len(gf.Edges) != 1 {
		t.Errorf("SelfLoop should have 1 edge, got %d", len(gf.Edges))
	}
	if gf.Edges[0][0] != gf.Edges[0][1] {
		t.Error("SelfLoop edge should point to itself")
	}
	if !gf.Properties.HasCycles {
		t.Error("SelfLoop should have cycles")
	}
}

func TestTree(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		depth     int
		breadth   int
		wantNodes int
	}{
		{"tree_1_2", 1, 2, 3},  // root + 2 children
		{"tree_2_2", 2, 2, 7},  // 1 + 2 + 4
		{"tree_3_2", 3, 2, 15}, // 1 + 2 + 4 + 8
		{"tree_2_3", 2, 3, 13}, // 1 + 3 + 9
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Tree(tt.depth, tt.breadth)

			if len(gf.Nodes) != tt.wantNodes {
				t.Errorf("Tree(%d,%d) nodes = %d, want %d", tt.depth, tt.breadth, len(gf.Nodes), tt.wantNodes)
			}
			if gf.Properties.HasCycles {
				t.Error("Tree should not have cycles")
			}
			if gf.Properties.ExpectedDepth != tt.depth {
				t.Errorf("Tree depth = %d, want %d", gf.Properties.ExpectedDepth, tt.depth)
			}
		})
	}
}

func TestDisconnected(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name          string
		components    int
		componentSize int
		wantNodes     int
	}{
		{"disconnected_2_3", 2, 3, 6},
		{"disconnected_3_2", 3, 2, 6},
		{"disconnected_5_1", 5, 1, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Disconnected(tt.components, tt.componentSize)

			if len(gf.Nodes) != tt.wantNodes {
				t.Errorf("Disconnected nodes = %d, want %d", len(gf.Nodes), tt.wantNodes)
			}
			if gf.Properties.IsConnected {
				t.Error("Disconnected should not be connected")
			}
		})
	}
}

func TestComplete(t *testing.T) {
	gen := NewDefault()

	tests := []struct {
		name      string
		size      int
		wantEdges int
	}{
		{"complete_2", 2, 1},
		{"complete_3", 3, 3},
		{"complete_4", 4, 6},
		{"complete_5", 5, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := gen.Complete(tt.size)

			if len(gf.Edges) != tt.wantEdges {
				t.Errorf("Complete(%d) edges = %d, want %d", tt.size, len(gf.Edges), tt.wantEdges)
			}
			if gf.Properties.HasCycles {
				t.Error("Complete DAG should not have cycles")
			}
		})
	}
}

func TestRandomDAG(t *testing.T) {
	gen := NewDefault()

	// Test determinism - same seed should produce same result
	gf1 := gen.RandomDAG(10, 0.5)

	gen2 := New(DefaultConfig()) // Same seed
	gf2 := gen2.RandomDAG(10, 0.5)

	if len(gf1.Edges) != len(gf2.Edges) {
		t.Errorf("RandomDAG not deterministic: %d vs %d edges", len(gf1.Edges), len(gf2.Edges))
	}

	// Verify it's a DAG (no edge from higher to lower index)
	for _, e := range gf1.Edges {
		if e[0] >= e[1] {
			t.Errorf("RandomDAG has invalid edge [%d,%d] (should be from lower to higher)", e[0], e[1])
		}
	}
}

func TestBipartite(t *testing.T) {
	gen := NewDefault()
	gf := gen.Bipartite(3, 2)

	expectedNodes := 5
	expectedEdges := 6 // 3 * 2

	if len(gf.Nodes) != expectedNodes {
		t.Errorf("Bipartite nodes = %d, want %d", len(gf.Nodes), expectedNodes)
	}
	if len(gf.Edges) != expectedEdges {
		t.Errorf("Bipartite edges = %d, want %d", len(gf.Edges), expectedEdges)
	}
}

func TestLadder(t *testing.T) {
	gen := NewDefault()
	gf := gen.Ladder(3)

	expectedNodes := 6 // 3 * 2
	// Chain edges: 2 + 2 = 4, Rung edges: 3, Total: 7
	expectedEdges := 7

	if len(gf.Nodes) != expectedNodes {
		t.Errorf("Ladder nodes = %d, want %d", len(gf.Nodes), expectedNodes)
	}
	if len(gf.Edges) != expectedEdges {
		t.Errorf("Ladder edges = %d, want %d", len(gf.Edges), expectedEdges)
	}
}

func TestToIssues(t *testing.T) {
	gen := NewDefault()
	gf := gen.Chain(3) // n0 <- n1 <- n2 (n1 depends on n0, n2 depends on n1)
	issues := gen.ToIssues(gf)

	if len(issues) != 3 {
		t.Errorf("ToIssues should produce 3 issues, got %d", len(issues))
	}

	// First issue (n0) should have no dependencies (it's the root)
	if len(issues[0].Dependencies) != 0 {
		t.Errorf("First issue (n0) should have 0 deps, got %d", len(issues[0].Dependencies))
	}

	// Second issue (n1) should depend on first (n0)
	if len(issues[1].Dependencies) != 1 {
		t.Errorf("Second issue (n1) should have 1 dep, got %d", len(issues[1].Dependencies))
	} else {
		if issues[1].Dependencies[0].DependsOnID != issues[0].ID {
			t.Errorf("Second issue should depend on first, depends on %s", issues[1].Dependencies[0].DependsOnID)
		}
	}

	// Third issue (n2) should depend on second (n1)
	if len(issues[2].Dependencies) != 1 {
		t.Errorf("Third issue (n2) should have 1 dep, got %d", len(issues[2].Dependencies))
	} else {
		if issues[2].Dependencies[0].DependsOnID != issues[1].ID {
			t.Errorf("Third issue should depend on second, depends on %s", issues[2].Dependencies[0].DependsOnID)
		}
	}

	// Verify all issues have valid IDs
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue %d has empty ID", i)
		}
		if !strings.HasPrefix(issue.ID, "TEST-") {
			t.Errorf("Issue %d ID should start with TEST-, got %s", i, issue.ID)
		}
	}
}

func TestToIssuesWithConfig(t *testing.T) {
	cfg := GeneratorConfig{
		Seed:           123,
		IDPrefix:       "CUSTOM",
		IncludeLabels:  true,
		IncludeMinutes: true,
		StatusMix:      []model.Status{model.StatusOpen, model.StatusInProgress},
		TypeMix:        []model.IssueType{model.TypeBug, model.TypeFeature},
	}
	gen := New(cfg)
	gf := gen.Star(5)
	issues := gen.ToIssues(gf)

	// Check prefix
	for _, issue := range issues {
		if !strings.HasPrefix(issue.ID, "CUSTOM-") {
			t.Errorf("Issue ID should start with CUSTOM-, got %s", issue.ID)
		}
	}

	// Check that at least some issues have labels
	hasLabels := false
	for _, issue := range issues {
		if len(issue.Labels) > 0 {
			hasLabels = true
			break
		}
	}
	if !hasLabels {
		t.Error("Expected at least some issues to have labels")
	}

	// Check that at least some issues have estimated minutes
	hasMinutes := false
	for _, issue := range issues {
		if issue.EstimatedMinutes != nil {
			hasMinutes = true
			break
		}
	}
	if !hasMinutes {
		t.Error("Expected at least some issues to have estimated minutes")
	}
}

func TestToJSONL(t *testing.T) {
	issues := QuickChain(3)
	jsonl := ToJSONL(issues)

	lines := strings.Split(strings.TrimSpace(jsonl), "\n")
	if len(lines) != 3 {
		t.Errorf("JSONL should have 3 lines, got %d", len(lines))
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var issue model.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Errorf("Line %d is invalid JSON: %v", i, err)
		}
	}
}

func TestQuickFunctions(t *testing.T) {
	tests := []struct {
		name   string
		fn     func() []model.Issue
		minLen int
	}{
		{"QuickChain", func() []model.Issue { return QuickChain(5) }, 5},
		{"QuickStar", func() []model.Issue { return QuickStar(5) }, 6},
		{"QuickDiamond", func() []model.Issue { return QuickDiamond(3) }, 5},
		{"QuickCycle", func() []model.Issue { return QuickCycle(4) }, 4},
		{"QuickTree", func() []model.Issue { return QuickTree(2, 2) }, 7},
		{"QuickDisconnected", func() []model.Issue { return QuickDisconnected(2, 3) }, 6},
		{"QuickRandom", func() []model.Issue { return QuickRandom(10, 0.3) }, 10},
		{"Empty", func() []model.Issue { return Empty() }, 0},
		{"Single", func() []model.Issue { return Single() }, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := tt.fn()
			if len(issues) < tt.minLen {
				t.Errorf("%s returned %d issues, want at least %d", tt.name, len(issues), tt.minLen)
			}

			// Verify all issues are valid
			for i, issue := range issues {
				if err := issue.Validate(); err != nil {
					t.Errorf("%s issue %d invalid: %v", tt.name, i, err)
				}
			}
		})
	}
}

func TestDeterminism(t *testing.T) {
	// Generate twice with same config
	cfg := DefaultConfig()

	gen1 := New(cfg)
	issues1 := gen1.ToIssues(gen1.RandomDAG(20, 0.4))

	gen2 := New(cfg)
	issues2 := gen2.ToIssues(gen2.RandomDAG(20, 0.4))

	// Should be identical
	if len(issues1) != len(issues2) {
		t.Fatalf("Different lengths: %d vs %d", len(issues1), len(issues2))
	}

	for i := range issues1 {
		if issues1[i].ID != issues2[i].ID {
			t.Errorf("Issue %d ID differs: %s vs %s", i, issues1[i].ID, issues2[i].ID)
		}
		if len(issues1[i].Dependencies) != len(issues2[i].Dependencies) {
			t.Errorf("Issue %d dep count differs: %d vs %d", i, len(issues1[i].Dependencies), len(issues2[i].Dependencies))
		}
	}
}

func TestGraphFixtureJSON(t *testing.T) {
	gen := NewDefault()
	gf := gen.Chain(5)

	// Should be JSON serializable
	data, err := json.Marshal(gf)
	if err != nil {
		t.Fatalf("Failed to marshal GraphFixture: %v", err)
	}

	// Should round-trip
	var gf2 GraphFixture
	if err := json.Unmarshal(data, &gf2); err != nil {
		t.Fatalf("Failed to unmarshal GraphFixture: %v", err)
	}

	if len(gf2.Nodes) != len(gf.Nodes) {
		t.Errorf("Nodes count differs after round-trip: %d vs %d", len(gf2.Nodes), len(gf.Nodes))
	}
}

// Benchmarks

func BenchmarkChain100(b *testing.B) {
	gen := NewDefault()
	for i := 0; i < b.N; i++ {
		_ = gen.ToIssues(gen.Chain(100))
	}
}

func BenchmarkStar100(b *testing.B) {
	gen := NewDefault()
	for i := 0; i < b.N; i++ {
		_ = gen.ToIssues(gen.Star(100))
	}
}

func BenchmarkComplete50(b *testing.B) {
	gen := NewDefault()
	for i := 0; i < b.N; i++ {
		_ = gen.ToIssues(gen.Complete(50))
	}
}

func BenchmarkRandomDAG500(b *testing.B) {
	gen := NewDefault()
	for i := 0; i < b.N; i++ {
		_ = gen.ToIssues(gen.RandomDAG(500, 0.1))
	}
}

func BenchmarkToJSONL1000(b *testing.B) {
	gen := NewDefault()
	issues := gen.ToIssues(gen.Chain(1000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ToJSONL(issues)
	}
}
