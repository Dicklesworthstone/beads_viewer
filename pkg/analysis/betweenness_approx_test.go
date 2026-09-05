package analysis

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
	"gonum.org/v1/gonum/graph/simple"
)

func TestApproxBetweenness_OrderedParallelResults(t *testing.T) {
	issues, err := testutil.PerformanceIssues("unicode", 1000, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	analyzer := NewAnalyzer(issues)
	for _, seed := range []int64{1, 19} {
		want := approxBetweennessDeterministic(analyzer.g, 128, seed)
		if len(want.Scores) == 0 {
			t.Fatal("fixture must exercise nonzero contributions")
		}
		for _, workers := range []int{1, 2, 3, 7} {
			t.Run(fmt.Sprintf("seed%d/workers%d", seed, workers), func(t *testing.T) {
				for repeat := 0; repeat < 3; repeat++ {
					got := approxBetweenness(analyzer.g, 128, seed, workers)
					assertBetweennessResultEqual(t, got, want)
				}
			})
		}
		// Exercise the public entry point with actual scheduler changes, not
		// only the internal worker-count seam. Do not run this test in parallel.
		originalProcs := runtime.GOMAXPROCS(0)
		for _, procs := range []int{1, 2, 4} {
			t.Run(fmt.Sprintf("seed%d/GOMAXPROCS%d", seed, procs), func(t *testing.T) {
				runtime.GOMAXPROCS(procs)
				defer runtime.GOMAXPROCS(originalProcs)
				for repeat := 0; repeat < 3; repeat++ {
					got := ApproxBetweenness(analyzer.g, 128, seed)
					assertBetweennessResultEqual(t, got, want)
				}
			})
		}
	}
}

func TestApproxBetweenness_OrderedBoundaries(t *testing.T) {
	// A diamond plus an isolated vertex gives two fractional, nonzero scores
	// and three absent zero scores. Exact mode must retain that sparse map.
	g := simple.NewDirectedGraph()
	for _, id := range []int64{10, 20, 30, 40, 99} {
		g.AddNode(simple.Node(id))
	}
	for _, edge := range [][2]int64{{10, 20}, {10, 30}, {20, 40}, {30, 40}} {
		g.SetEdge(g.NewEdge(g.Node(edge[0]), g.Node(edge[1])))
	}
	for _, sampleSize := range []int{-5, 0, 1, 4, 5, 10} {
		want := approxBetweennessDeterministic(g, sampleSize, 1)
		for _, workers := range []int{-1, 0, 1, 2, 99} {
			got := approxBetweenness(g, sampleSize, 1, workers)
			assertBetweennessResultEqual(t, got, want)
			if got.TotalNodes != 5 || got.SampleSize != max(1, min(sampleSize, 5)) {
				t.Fatalf("sample%d workers%d: wrong counts: %+v", sampleSize, workers, got)
			}
			if sampleSize >= 5 {
				if got.Mode != BetweennessExact || !reflect.DeepEqual(got.Scores, map[int64]float64{20: 0.5, 30: 0.5}) {
					t.Fatalf("exact diamond scores changed: %+v", got)
				}
			} else if got.Mode != BetweennessApproximate {
				t.Fatalf("sample%d unexpectedly changed mode: %+v", sampleSize, got)
			}
			for id, score := range got.Scores {
				if score == 0 || (id != 20 && id != 30) {
					t.Fatalf("sparse diamond result contains unexpected node%d score%g", id, score)
				}
			}
		}
	}
	empty := simple.NewDirectedGraph()
	for _, workers := range []int{0, 1, 8} {
		got := approxBetweenness(empty, 10, 1, workers)
		if got.Mode != BetweennessApproximate || got.TotalNodes != 0 || got.SampleSize != 0 || got.TimedOut || got.Scores == nil || len(got.Scores) != 0 {
			t.Fatalf("empty graph metadata changed: %+v", got)
		}
	}
}

func assertBetweennessResultEqual(t *testing.T, got, want BetweennessResult) {
	t.Helper()
	if got.Mode != want.Mode || got.SampleSize != want.SampleSize || got.TotalNodes != want.TotalNodes || got.TimedOut != want.TimedOut {
		t.Fatalf("betweenness metadata differs: got%+v want%+v", got, want)
	}
	if !reflect.DeepEqual(got.Scores, want.Scores) {
		for id, score := range want.Scores {
			if value, ok := got.Scores[id]; !ok || value != score {
				t.Fatalf("node%d score%.17g present%t, want exactly%.17g", id, value, ok, score)
			}
		}
		t.Fatalf("score map contains extra entries: got%d want%d", len(got.Scores), len(want.Scores))
	}
	if got.Elapsed <= 0 {
		t.Fatal("completed computation must report elapsed time")
	}
}

func TestApproxBetweenness_SmallGraph(t *testing.T) {
	// For small graphs, ApproxBetweenness should fall back to exact
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{IssueID: "C", DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	analyzer := NewAnalyzer(issues)
	result := ApproxBetweenness(analyzer.g, 10, 1) // Sample size > node count

	if result.Mode != BetweennessExact {
		t.Errorf("Expected exact mode for small graph, got %s", result.Mode)
	}

	if len(result.Scores) == 0 {
		t.Error("Expected betweenness scores to be computed")
	}
}

func TestApproxBetweenness_LargeGraph_Approximate(t *testing.T) {
	// For larger graphs with small sample size, should use approximation
	issues := make([]model.Issue, 50)
	for i := 0; i < 50; i++ {
		issues[i] = model.Issue{
			ID:     string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Status: model.StatusOpen,
		}
		// Create a chain
		if i > 0 {
			issues[i].Dependencies = []*model.Dependency{
				{IssueID: issues[i].ID, DependsOnID: issues[i-1].ID, Type: model.DepBlocks},
			}
		}
	}

	analyzer := NewAnalyzer(issues)
	result := ApproxBetweenness(analyzer.g, 10, 1) // Sample size < node count

	if result.Mode != BetweennessApproximate {
		t.Errorf("Expected approximate mode for large graph with small sample, got %s", result.Mode)
	}

	if result.SampleSize != 10 {
		t.Errorf("Expected sample size 10, got %d", result.SampleSize)
	}
}

func TestApproxBetweenness_EmptyGraph(t *testing.T) {
	issues := []model.Issue{}
	analyzer := NewAnalyzer(issues)
	result := ApproxBetweenness(analyzer.g, 10, 1)

	if result.TotalNodes != 0 {
		t.Errorf("Expected 0 nodes, got %d", result.TotalNodes)
	}
	if result.SampleSize != 0 {
		t.Errorf("Expected 0 sampled pivots, got %d", result.SampleSize)
	}
}

func TestApproxBetweenness_ZeroSampleSize(t *testing.T) {
	// sampleSize=0 should not cause division by zero; should be clamped to 1
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}
	analyzer := NewAnalyzer(issues)

	// This should not panic
	result := ApproxBetweenness(analyzer.g, 0, 42)

	if result.SampleSize < 1 {
		t.Errorf("Expected sample size to be clamped to at least 1, got %d", result.SampleSize)
	}
}

func TestApproxBetweenness_NegativeSampleSize(t *testing.T) {
	// Negative sampleSize should not cause panic; should be clamped to 1
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
	}
	analyzer := NewAnalyzer(issues)

	// This should not panic
	result := ApproxBetweenness(analyzer.g, -5, 42)

	if result.SampleSize < 1 {
		t.Errorf("Expected sample size to be clamped to at least 1, got %d", result.SampleSize)
	}
}

func TestRecommendSampleSize(t *testing.T) {
	tests := []struct {
		nodeCount   int
		edgeCount   int
		minExpected int
		maxExpected int
	}{
		{50, 100, 50, 50},       // Small: use full
		{-1, 0, 0, 0},           // Invalid negative node count: no pivots
		{100, 200, 50, 100},     // Medium: 20% sample
		{500, 1000, 100, 100},   // Large: fixed sample
		{2000, 5000, 200, 200},  // XL: larger fixed sample
		{5000, 10000, 200, 200}, // XL+: still 200
	}

	for _, tt := range tests {
		size := RecommendSampleSize(tt.nodeCount, tt.edgeCount)
		if size < tt.minExpected || size > tt.maxExpected {
			t.Errorf("RecommendSampleSize(%d, %d) = %d, expected between %d and %d",
				tt.nodeCount, tt.edgeCount, size, tt.minExpected, tt.maxExpected)
		}
	}
}

func TestBetweennessMode_ConfigIntegration(t *testing.T) {
	// Test that ConfigForSize properly sets betweenness mode
	tests := []struct {
		nodeCount  int
		edgeCount  int
		expectMode BetweennessMode
	}{
		{50, 100, BetweennessExact},          // Small
		{200, 400, BetweennessExact},         // Medium
		{800, 1600, BetweennessApproximate},  // Large (sparse)
		{3000, 6000, BetweennessApproximate}, // XL
	}

	for _, tt := range tests {
		config := ConfigForSize(tt.nodeCount, tt.edgeCount)
		if config.BetweennessMode != tt.expectMode {
			t.Errorf("ConfigForSize(%d, %d) betweenness mode = %s, expected %s",
				tt.nodeCount, tt.edgeCount, config.BetweennessMode, tt.expectMode)
		}
	}
}

// BenchmarkApproxBetweenness_vs_Exact benchmarks approximate vs exact betweenness
func BenchmarkApproxBetweenness_500nodes_Exact(b *testing.B) {
	issues := generateChainGraph(500)
	analyzer := NewAnalyzer(issues)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ApproxBetweenness(analyzer.g, 500, 42) // Full sample = exact
	}
}

func BenchmarkApproxBetweenness_500nodes_Sample100(b *testing.B) {
	issues := generateChainGraph(500)
	analyzer := NewAnalyzer(issues)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ApproxBetweenness(analyzer.g, 100, 42)
	}
}

func BenchmarkApproxBetweenness_500nodes_Sample50(b *testing.B) {
	issues := generateChainGraph(500)
	analyzer := NewAnalyzer(issues)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ApproxBetweenness(analyzer.g, 50, 42)
	}
}

func BenchmarkApproxBetweenness_OrderedWorkers(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		issues, err := testutil.PerformanceIssues("unicode", size, 20260904)
		if err != nil {
			b.Fatal(err)
		}
		analyzer := NewAnalyzer(issues)
		for _, samples := range []int{32, 256} {
			for _, workers := range []int{1, 4} {
				b.Run(fmt.Sprintf("nodes%d/pivots%d/workers%d", size, samples, workers), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						approxBetweenness(analyzer.g, samples, 1, workers)
					}
				})
			}
		}
	}
}

// generateChainGraph creates a linear dependency chain
func generateChainGraph(n int) []model.Issue {
	issues := make([]model.Issue, n)
	for i := 0; i < n; i++ {
		issues[i] = model.Issue{
			ID:     generateID(i),
			Status: model.StatusOpen,
		}
		if i > 0 {
			issues[i].Dependencies = []*model.Dependency{
				{IssueID: issues[i].ID, DependsOnID: issues[i-1].ID, Type: model.DepBlocks},
			}
		}
	}
	return issues
}

// generateID creates a unique ID for testing
func generateID(i int) string {
	return string(rune('A'+i%26)) + string(rune('0'+i/26))
}
