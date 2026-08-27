package analysis

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

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
