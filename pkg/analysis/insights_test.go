package analysis

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ============================================================================
// GenerateInsights tests
// ============================================================================

func TestGenerateInsights_EmptyStats(t *testing.T) {
	stats := NewGraphStatsForTest(
		make(map[string]float64), // pageRank
		make(map[string]float64), // betweenness
		make(map[string]float64), // eigenvector
		make(map[string]float64), // hubs
		make(map[string]float64), // authorities
		make(map[string]float64), // criticalPathScore
		nil,                      // outDegree
		nil,                      // inDegree
		nil,                      // cycles
		0,                        // density
		nil,                      // topologicalOrder
	)

	insights := stats.GenerateInsights(5)

	if len(insights.Bottlenecks) != 0 {
		t.Errorf("Expected no bottlenecks, got %d", len(insights.Bottlenecks))
	}
	if len(insights.Keystones) != 0 {
		t.Errorf("Expected no keystones, got %d", len(insights.Keystones))
	}
	if len(insights.Influencers) != 0 {
		t.Errorf("Expected no influencers, got %d", len(insights.Influencers))
	}
	if len(insights.Hubs) != 0 {
		t.Errorf("Expected no hubs, got %d", len(insights.Hubs))
	}
	if len(insights.Authorities) != 0 {
		t.Errorf("Expected no authorities, got %d", len(insights.Authorities))
	}
}

func TestGenerateInsights_WithData(t *testing.T) {
	stats := NewGraphStatsForTest(
		map[string]float64{"A": 0.3, "B": 0.5, "C": 0.2}, // pageRank
		map[string]float64{"A": 0.8, "B": 0.3, "C": 0.5}, // betweenness
		map[string]float64{"A": 0.4, "B": 0.6, "C": 0.2}, // eigenvector
		map[string]float64{"A": 1.0, "B": 2.0, "C": 0.5}, // hubs
		map[string]float64{"A": 0.7, "B": 0.9, "C": 0.3}, // authorities
		map[string]float64{"A": 3.0, "B": 1.0, "C": 5.0}, // criticalPathScore
		map[string]int{"A": 0, "B": 1, "C": 0},           // outDegree
		nil,                                              // inDegree
		[][]string{{"X", "Y", "Z"}},                      // cycles
		0.42,                                             // density
		nil,                                              // topologicalOrder
	)

	insights := stats.GenerateInsights(2) // Limit to top 2

	// Check Bottlenecks (sorted by Betweenness)
	if len(insights.Bottlenecks) != 2 {
		t.Fatalf("Expected 2 bottlenecks, got %d", len(insights.Bottlenecks))
	}
	if insights.Bottlenecks[0].ID != "A" || insights.Bottlenecks[0].Value != 0.8 {
		t.Errorf("Expected top bottleneck to be A with 0.8, got %s with %f",
			insights.Bottlenecks[0].ID, insights.Bottlenecks[0].Value)
	}

	// Check Keystones (sorted by CriticalPathScore)
	if len(insights.Keystones) != 2 {
		t.Fatalf("Expected 2 keystones, got %d", len(insights.Keystones))
	}
	if insights.Keystones[0].ID != "C" || insights.Keystones[0].Value != 5.0 {
		t.Errorf("Expected top keystone to be C with 5.0, got %s with %f",
			insights.Keystones[0].ID, insights.Keystones[0].Value)
	}

	// Check Influencers (sorted by Eigenvector)
	if len(insights.Influencers) != 2 {
		t.Fatalf("Expected 2 influencers, got %d", len(insights.Influencers))
	}
	if insights.Influencers[0].ID != "B" || insights.Influencers[0].Value != 0.6 {
		t.Errorf("Expected top influencer to be B with 0.6, got %s with %f",
			insights.Influencers[0].ID, insights.Influencers[0].Value)
	}

	// Check Hubs (sorted by Hubs score)
	if len(insights.Hubs) != 2 {
		t.Fatalf("Expected 2 hubs, got %d", len(insights.Hubs))
	}
	if insights.Hubs[0].ID != "B" || insights.Hubs[0].Value != 2.0 {
		t.Errorf("Expected top hub to be B with 2.0, got %s with %f",
			insights.Hubs[0].ID, insights.Hubs[0].Value)
	}

	// Check Authorities (sorted by Authority score)
	if len(insights.Authorities) != 2 {
		t.Fatalf("Expected 2 authorities, got %d", len(insights.Authorities))
	}
	if insights.Authorities[0].ID != "B" || insights.Authorities[0].Value != 0.9 {
		t.Errorf("Expected top authority to be B with 0.9, got %s with %f",
			insights.Authorities[0].ID, insights.Authorities[0].Value)
	}

	// Check Cycles are passed through
	if len(insights.Cycles) != 1 {
		t.Fatalf("Expected 1 cycle, got %d", len(insights.Cycles))
	}
	if len(insights.Cycles[0]) != 3 {
		t.Errorf("Expected cycle of length 3, got %d", len(insights.Cycles[0]))
	}

	// Check density is passed through
	if insights.ClusterDensity != 0.42 {
		t.Errorf("Expected density 0.42, got %f", insights.ClusterDensity)
	}

	// Check Orphans derived from OutDegree
	if len(insights.Orphans) != 2 {
		t.Fatalf("Expected 2 orphans, got %d", len(insights.Orphans))
	}
	if insights.Orphans[0] != "A" || insights.Orphans[1] != "C" {
		t.Errorf("Expected orphans [A C], got %v", insights.Orphans)
	}

	// Check Stats reference
	if insights.Stats == nil {
		t.Error("Expected Stats to be set")
	}
}

func TestGenerateInsights_ZeroLimit(t *testing.T) {
	stats := NewGraphStatsForTest(
		map[string]float64{"A": 0.3, "B": 0.5}, // pageRank
		map[string]float64{"A": 0.8, "B": 0.3}, // betweenness
		map[string]float64{"A": 0.4, "B": 0.6}, // eigenvector
		map[string]float64{"A": 1.0, "B": 2.0}, // hubs
		map[string]float64{"A": 0.7, "B": 0.9}, // authorities
		map[string]float64{"A": 3.0, "B": 1.0}, // criticalPathScore
		map[string]int{"A": 0, "B": 2}, nil, nil, 0, nil,
	)

	// Zero limit should return all items
	insights := stats.GenerateInsights(0)

	if len(insights.Bottlenecks) != 2 {
		t.Errorf("Expected 2 bottlenecks with limit 0, got %d", len(insights.Bottlenecks))
	}
	if len(insights.Orphans) != 1 || insights.Orphans[0] != "A" {
		t.Errorf("Expected orphans [A] with limit 0, got %v", insights.Orphans)
	}
}

func TestGenerateInsights_NegativeLimit(t *testing.T) {
	stats := NewGraphStatsForTest(
		map[string]float64{"A": 0.3, "B": 0.5}, // pageRank
		map[string]float64{"A": 0.8, "B": 0.3}, // betweenness
		map[string]float64{"A": 0.4, "B": 0.6}, // eigenvector
		map[string]float64{"A": 1.0, "B": 2.0}, // hubs
		map[string]float64{"A": 0.7, "B": 0.9}, // authorities
		map[string]float64{"A": 3.0, "B": 1.0}, // criticalPathScore
		nil, nil, nil, 0, nil,
	)

	// Negative limit should return all items
	insights := stats.GenerateInsights(-5)

	if len(insights.Bottlenecks) != 2 {
		t.Errorf("Expected 2 bottlenecks with negative limit, got %d", len(insights.Bottlenecks))
	}
}

func TestGenerateInsights_LimitExceedsItems(t *testing.T) {
	stats := NewGraphStatsForTest(
		map[string]float64{"A": 0.3}, // pageRank
		map[string]float64{"A": 0.8}, // betweenness
		map[string]float64{"A": 0.4}, // eigenvector
		map[string]float64{"A": 1.0}, // hubs
		map[string]float64{"A": 0.7}, // authorities
		map[string]float64{"A": 3.0}, // criticalPathScore
		nil, nil, nil, 0, nil,
	)

	// Limit of 100 with only 1 item
	insights := stats.GenerateInsights(100)

	if len(insights.Bottlenecks) != 1 {
		t.Errorf("Expected 1 bottleneck when limit exceeds items, got %d", len(insights.Bottlenecks))
	}
}

// ============================================================================
// getTopItems tests
// ============================================================================

func TestGetTopItems_Empty(t *testing.T) {
	result := getTopItems(map[string]float64{}, 5)
	if len(result) != 0 {
		t.Errorf("Expected empty result, got %d items", len(result))
	}
}

func TestGetTopItems_SingleItem(t *testing.T) {
	m := map[string]float64{"only": 1.0}
	result := getTopItems(m, 5)

	if len(result) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(result))
	}
	if result[0].ID != "only" || result[0].Value != 1.0 {
		t.Errorf("Expected only:1.0, got %s:%f", result[0].ID, result[0].Value)
	}
}

func TestGetTopItems_SortOrder(t *testing.T) {
	m := map[string]float64{
		"low":    0.1,
		"medium": 0.5,
		"high":   0.9,
	}
	result := getTopItems(m, 3)

	if len(result) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(result))
	}

	// Should be sorted descending by value
	if result[0].ID != "high" {
		t.Errorf("Expected first item to be 'high', got %s", result[0].ID)
	}
	if result[1].ID != "medium" {
		t.Errorf("Expected second item to be 'medium', got %s", result[1].ID)
	}
	if result[2].ID != "low" {
		t.Errorf("Expected third item to be 'low', got %s", result[2].ID)
	}
}

func TestGetTopItems_LimitApplied(t *testing.T) {
	m := map[string]float64{
		"a": 0.1,
		"b": 0.2,
		"c": 0.3,
		"d": 0.4,
		"e": 0.5,
	}
	result := getTopItems(m, 2)

	if len(result) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(result))
	}

	// Should get the top 2 (highest values)
	if result[0].ID != "e" || result[1].ID != "d" {
		t.Errorf("Expected top 2 to be e and d, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestGetTopItems_EqualValues(t *testing.T) {
	m := map[string]float64{
		"a": 1.0,
		"b": 1.0,
		"c": 1.0,
	}
	result := getTopItems(m, 3)

	if len(result) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(result))
	}

	// All values should be 1.0
	for _, item := range result {
		if item.Value != 1.0 {
			t.Errorf("Expected value 1.0, got %f for %s", item.Value, item.ID)
		}
	}
}

func TestGetTopItems_ZeroValues(t *testing.T) {
	m := map[string]float64{
		"zero":     0.0,
		"negative": -1.0,
		"positive": 1.0,
	}
	result := getTopItems(m, 3)

	if len(result) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(result))
	}

	// positive should be first, zero second, negative last
	if result[0].ID != "positive" {
		t.Errorf("Expected first to be 'positive', got %s", result[0].ID)
	}
	if result[1].ID != "zero" {
		t.Errorf("Expected second to be 'zero', got %s", result[1].ID)
	}
	if result[2].ID != "negative" {
		t.Errorf("Expected third to be 'negative', got %s", result[2].ID)
	}
}

// ============================================================================
// Integration test with real analyzer output
// ============================================================================

func TestGenerateInsights_FromRealAnalysis(t *testing.T) {
	// Create a realistic issue set
	issues := []model.Issue{
		{ID: "EPIC-1", Status: model.StatusOpen},
		{ID: "TASK-1", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "EPIC-1", Type: model.DepBlocks},
		}},
		{ID: "TASK-2", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "EPIC-1", Type: model.DepBlocks},
		}},
		{ID: "TASK-3", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "TASK-1", Type: model.DepBlocks},
			{DependsOnID: "TASK-2", Type: model.DepBlocks},
		}},
		{ID: "TASK-4", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "TASK-3", Type: model.DepBlocks},
		}},
	}

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()
	insights := stats.GenerateInsights(3)

	// EPIC-1 should have high betweenness (it's a central hub)
	if len(insights.Bottlenecks) == 0 {
		t.Fatal("Expected at least one bottleneck")
	}

	// Should have keystones (critical path nodes)
	if len(insights.Keystones) == 0 {
		t.Fatal("Expected at least one keystone")
	}

	// Stats reference should be set
	if insights.Stats == nil {
		t.Error("Stats should be populated in insights")
	}

	// Density should be calculated
	if insights.ClusterDensity == 0 {
		t.Error("Density should be non-zero for a connected graph")
	}
}

func TestGenerateInsights_WithCycles(t *testing.T) {
	// Create issues with a cycle
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()
	insights := stats.GenerateInsights(10)

	// Should detect the cycle
	if len(insights.Cycles) == 0 {
		t.Error("Expected cycle to be detected")
	}

	// Cycle should contain all 3 nodes
	if len(insights.Cycles) > 0 {
		cycleNodes := make(map[string]bool)
		for _, node := range insights.Cycles[0] {
			cycleNodes[node] = true
		}
		if !cycleNodes["A"] || !cycleNodes["B"] || !cycleNodes["C"] {
			t.Error("Cycle should contain A, B, and C")
		}
	}
}

// ============================================================================
// InsightItem tests
// ============================================================================

func TestInsightItem_Fields(t *testing.T) {
	item := InsightItem{
		ID:    "TEST-1",
		Value: 0.75,
	}

	if item.ID != "TEST-1" {
		t.Errorf("Expected ID TEST-1, got %s", item.ID)
	}
	if item.Value != 0.75 {
		t.Errorf("Expected Value 0.75, got %f", item.Value)
	}
}

// ============================================================================
// Insights struct tests
// ============================================================================

func TestInsights_EmptyFields(t *testing.T) {
	insights := Insights{}

	// All fields should be nil/empty by default
	if len(insights.Bottlenecks) != 0 {
		t.Error("Expected nil or empty Bottlenecks")
	}
	if len(insights.Keystones) != 0 {
		t.Error("Expected nil or empty Keystones")
	}
	if len(insights.Cycles) != 0 {
		t.Error("Expected nil or empty Cycles")
	}
	if insights.ClusterDensity != 0 {
		t.Error("Expected zero ClusterDensity")
	}
	if insights.Stats != nil {
		t.Error("Expected nil Stats")
	}
}

func TestGenerateScopedInsights_ExcludesClosedTopologyByDefault(t *testing.T) {
	issues := []model.Issue{
		{ID: "CLOSED-HUB", Status: model.StatusClosed},
		{ID: "CLOSED-A", Status: model.StatusClosed, Dependencies: []*model.Dependency{
			{DependsOnID: "CLOSED-HUB", Type: model.DepBlocks},
		}},
		{ID: "CLOSED-B", Status: model.StatusClosed, Dependencies: []*model.Dependency{
			{DependsOnID: "CLOSED-A", Type: model.DepBlocks},
		}},
		{ID: "OPEN-ROOT", Status: model.StatusOpen},
		{ID: "OPEN-MID", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "OPEN-ROOT", Type: model.DepBlocks},
		}},
		{ID: "OPEN-LEAF", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "OPEN-MID", Type: model.DepBlocks},
		}},
		{ID: "CLOSED-ORPHAN", Status: model.StatusClosed},
		{ID: "OPEN-ORPHAN", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	stats := an.Analyze()

	activeOnly := an.GenerateScopedInsights(&stats, 10, false)
	for _, item := range activeOnly.Bottlenecks {
		if item.ID == "CLOSED-HUB" || item.ID == "CLOSED-A" || item.ID == "CLOSED-B" {
			t.Fatalf("scoped insights (includeInactive=false) leaked closed bottleneck %s", item.ID)
		}
	}
	for _, id := range activeOnly.Articulation {
		if id == "CLOSED-HUB" || id == "CLOSED-A" || id == "CLOSED-B" {
			t.Fatalf("scoped insights (includeInactive=false) leaked closed articulation point %s", id)
		}
	}
	for _, id := range activeOnly.Orphans {
		if id == "CLOSED-ORPHAN" {
			t.Fatalf("scoped insights (includeInactive=false) leaked closed orphan %s", id)
		}
	}

	withInactive := an.GenerateScopedInsights(&stats, 10, true)
	foundClosedOrphan := false
	for _, id := range withInactive.Orphans {
		if id == "CLOSED-ORPHAN" {
			foundClosedOrphan = true
			break
		}
	}
	if !foundClosedOrphan {
		t.Fatalf("expected CLOSED-ORPHAN when includeInactive=true, got %v", withInactive.Orphans)
	}
}

func TestConfigForSize_SkipPhase2DisablesAllPhase2Algorithms(t *testing.T) {
	t.Setenv("BV_SKIP_PHASE2", "1")
	cfg := ConfigForSize(25, 40)
	if cfg.ComputePageRank || cfg.ComputeBetweenness || cfg.ComputeEigenvector ||
		cfg.ComputeHITS || cfg.ComputeCriticalPath || cfg.ComputeCycles ||
		cfg.ComputeKCore || cfg.ComputeArticulation || cfg.ComputeSlack {
		t.Fatalf("BV_SKIP_PHASE2=1 left Phase 2 algorithms enabled: %+v", cfg)
	}
}
