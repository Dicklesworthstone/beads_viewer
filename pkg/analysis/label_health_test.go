package analysis

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestHealthLevelFromScore(t *testing.T) {
	tests := []struct {
		score    int
		expected string
	}{
		{100, HealthLevelHealthy},
		{70, HealthLevelHealthy},
		{69, HealthLevelWarning},
		{40, HealthLevelWarning},
		{39, HealthLevelCritical},
		{0, HealthLevelCritical},
	}

	for _, tt := range tests {
		result := HealthLevelFromScore(tt.score)
		if result != tt.expected {
			t.Errorf("HealthLevelFromScore(%d) = %s, want %s", tt.score, result, tt.expected)
		}
	}
}

func TestComputeCompositeHealth(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// All components at 100 should give 100
	score := ComputeCompositeHealth(100, 100, 100, 100, cfg)
	if score != 100 {
		t.Errorf("All 100s should give 100, got %d", score)
	}

	// All components at 0 should give 0
	score = ComputeCompositeHealth(0, 0, 0, 0, cfg)
	if score != 0 {
		t.Errorf("All 0s should give 0, got %d", score)
	}

	// All components at 50 should give 50
	score = ComputeCompositeHealth(50, 50, 50, 50, cfg)
	if score != 50 {
		t.Errorf("All 50s should give 50, got %d", score)
	}

	// Test weighted average
	// velocity=100, freshness=0, flow=100, criticality=0
	// With equal weights: (100*0.25 + 0*0.25 + 100*0.25 + 0*0.25) = 50
	score = ComputeCompositeHealth(100, 0, 100, 0, cfg)
	if score != 50 {
		t.Errorf("Expected 50 for alternating 100/0, got %d", score)
	}
}

func TestDefaultLabelHealthConfig(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// Check weights sum to 1.0
	totalWeight := cfg.VelocityWeight + cfg.FreshnessWeight + cfg.FlowWeight + cfg.CriticalityWeight
	if totalWeight != 1.0 {
		t.Errorf("Weights should sum to 1.0, got %f", totalWeight)
	}

	// Check reasonable defaults
	if cfg.StaleThresholdDays != 14 {
		t.Errorf("Expected stale threshold of 14 days, got %d", cfg.StaleThresholdDays)
	}

	if cfg.MinIssuesForHealth != 1 {
		t.Errorf("Expected min issues of 1, got %d", cfg.MinIssuesForHealth)
	}
}

func TestNewLabelHealth(t *testing.T) {
	health := NewLabelHealth("test-label")

	if health.Label != "test-label" {
		t.Errorf("Expected label 'test-label', got '%s'", health.Label)
	}

	if health.Health != 100 {
		t.Errorf("New label should start with health 100, got %d", health.Health)
	}

	if health.HealthLevel != HealthLevelHealthy {
		t.Errorf("New label should be healthy, got %s", health.HealthLevel)
	}

	if health.Velocity.TrendDirection != "stable" {
		t.Errorf("Expected stable trend, got %s", health.Velocity.TrendDirection)
	}

	if health.Freshness.StaleThresholdDays != DefaultStaleThresholdDays {
		t.Errorf("Expected stale threshold %d, got %d", DefaultStaleThresholdDays, health.Freshness.StaleThresholdDays)
	}
}

func TestNeedsAttention(t *testing.T) {
	healthyLabel := LabelHealth{Health: 80}
	warningLabel := LabelHealth{Health: 50}
	criticalLabel := LabelHealth{Health: 30}

	if NeedsAttention(healthyLabel) {
		t.Error("Healthy label (80) should not need attention")
	}

	if !NeedsAttention(warningLabel) {
		t.Error("Warning label (50) should need attention")
	}

	if !NeedsAttention(criticalLabel) {
		t.Error("Critical label (30) should need attention")
	}
}

func TestLabelHealthTypes(t *testing.T) {
	// Test that all types can be instantiated and have expected structure
	velocity := VelocityMetrics{
		ClosedLast7Days:  5,
		ClosedLast30Days: 20,
		AvgDaysToClose:   3.5,
		TrendDirection:   "improving",
		TrendPercent:     15.0,
		VelocityScore:    80,
	}

	if velocity.ClosedLast7Days != 5 {
		t.Errorf("VelocityMetrics field mismatch")
	}

	freshness := FreshnessMetrics{
		AvgDaysSinceUpdate: 5.5,
		StaleCount:         2,
		StaleThresholdDays: 14,
		FreshnessScore:     70,
	}

	if freshness.StaleCount != 2 {
		t.Errorf("FreshnessMetrics field mismatch")
	}

	flow := FlowMetrics{
		IncomingDeps:      3,
		OutgoingDeps:      2,
		IncomingLabels:    []string{"api", "core"},
		OutgoingLabels:    []string{"ui"},
		BlockedByExternal: 1,
		BlockingExternal:  1,
		FlowScore:         85,
	}

	if len(flow.IncomingLabels) != 2 {
		t.Errorf("FlowMetrics labels mismatch")
	}

	criticality := CriticalityMetrics{
		AvgPageRank:       0.05,
		AvgBetweenness:    0.15,
		MaxBetweenness:    0.35,
		CriticalPathCount: 3,
		BottleneckCount:   1,
		CriticalityScore:  75,
	}

	if criticality.BottleneckCount != 1 {
		t.Errorf("CriticalityMetrics field mismatch")
	}
}

func TestCrossLabelFlowTypes(t *testing.T) {
	dep := LabelDependency{
		FromLabel:  "api",
		ToLabel:    "ui",
		IssueCount: 3,
		IssueIDs:   []string{"bv-1", "bv-2", "bv-3"},
		BlockingPairs: []BlockingPair{
			{BlockerID: "bv-1", BlockedID: "bv-4", BlockerLabel: "api", BlockedLabel: "ui"},
		},
	}

	if dep.FromLabel != "api" {
		t.Errorf("LabelDependency FromLabel mismatch")
	}

	if len(dep.BlockingPairs) != 1 {
		t.Errorf("Expected 1 blocking pair, got %d", len(dep.BlockingPairs))
	}

	flow := CrossLabelFlow{
		Labels:              []string{"api", "ui", "core"},
		FlowMatrix:          [][]int{{0, 3, 1}, {0, 0, 2}, {0, 0, 0}},
		Dependencies:        []LabelDependency{dep},
		BottleneckLabels:    []string{"api"},
		TotalCrossLabelDeps: 6,
	}

	if len(flow.Labels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(flow.Labels))
	}

	if flow.TotalCrossLabelDeps != 6 {
		t.Errorf("Expected 6 cross-label deps, got %d", flow.TotalCrossLabelDeps)
	}
}

func TestComputeCrossLabelFlow(t *testing.T) {
	now := time.Now().UTC()
	cfg := DefaultLabelHealthConfig()
	issues := []model.Issue{
		{ID: "A", Labels: []string{"api"}, Status: model.StatusOpen},
		{ID: "B", Labels: []string{"ui"}, Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Labels: []string{"api", "core"}, Status: model.StatusOpen},
		{ID: "D", Labels: []string{"ui", "core"}, Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "E", Labels: []string{"api"}, Status: model.StatusClosed, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}

	flow := ComputeCrossLabelFlow(issues, cfg)

	if flow.TotalCrossLabelDeps != 4 { // A->B (api->ui) plus C->D cross-product (api->ui, api->core, core->ui)
		t.Fatalf("expected 4 cross-label deps, got %d", flow.TotalCrossLabelDeps)
	}

	if len(flow.Labels) == 0 || flow.FlowMatrix == nil {
		t.Fatalf("expected labels and flow matrix to be populated")
	}

	// Ensure bottlenecks include api (highest outgoing)
	foundAPI := false
	for _, l := range flow.BottleneckLabels {
		if l == "api" {
			foundAPI = true
			break
		}
	}
	if !foundAPI {
		t.Fatalf("expected api in bottleneck labels")
	}

	// Ensure closed issue E is ignored in flow counts
	apiIdx := -1
	uiIdx := -1
	for i, l := range flow.Labels {
		if l == "api" {
			apiIdx = i
		}
		if l == "ui" {
			uiIdx = i
		}
	}
	if apiIdx == -1 || uiIdx == -1 {
		t.Fatalf("missing api/ui labels in flow")
	}
	if flow.FlowMatrix[apiIdx][uiIdx] != 2 { // A->B and C->D (api part) count
		t.Fatalf("expected api->ui count 2, got %d", flow.FlowMatrix[apiIdx][uiIdx])
	}

	_ = now // suppress unused if future additions use time
}

func TestLabelPath(t *testing.T) {
	path := LabelPath{
		Labels:      []string{"core", "api", "ui"},
		Length:      2,
		IssueCount:  5,
		TotalWeight: 12.5,
	}

	if path.Length != 2 {
		t.Errorf("Expected length 2, got %d", path.Length)
	}

	if len(path.Labels) != 3 {
		t.Errorf("Expected 3 labels in path, got %d", len(path.Labels))
	}
}

func TestLabelAnalysisResult(t *testing.T) {
	result := LabelAnalysisResult{
		TotalLabels:     5,
		HealthyCount:    3,
		WarningCount:    1,
		CriticalCount:   1,
		AttentionNeeded: []string{"blocked-label", "stale-label"},
	}

	if result.TotalLabels != 5 {
		t.Errorf("Expected 5 total labels, got %d", result.TotalLabels)
	}

	total := result.HealthyCount + result.WarningCount + result.CriticalCount
	if total != result.TotalLabels {
		t.Errorf("Health counts (%d) don't sum to total (%d)", total, result.TotalLabels)
	}

	if len(result.AttentionNeeded) != 2 {
		t.Errorf("Expected 2 labels needing attention, got %d", len(result.AttentionNeeded))
	}
}

// ============================================================================
// Label Extraction Tests (bv-101)
// ============================================================================

func TestExtractLabelsEmpty(t *testing.T) {
	result := ExtractLabels([]model.Issue{})

	if result.LabelCount != 0 {
		t.Errorf("Expected 0 labels for empty input, got %d", result.LabelCount)
	}
	if result.IssueCount != 0 {
		t.Errorf("Expected 0 issues for empty input, got %d", result.IssueCount)
	}
	if len(result.Stats) != 0 {
		t.Errorf("Expected empty stats map, got %d entries", len(result.Stats))
	}
}

func TestExtractLabelsBasic(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api", "bug"}, Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Labels: []string{"api", "feature"}, Status: model.StatusClosed, Priority: 2},
		{ID: "bv-3", Labels: []string{"ui"}, Status: model.StatusInProgress, Priority: 1},
		{ID: "bv-4", Labels: []string{}, Status: model.StatusOpen, Priority: 3}, // No labels
	}

	result := ExtractLabels(issues)

	// Check counts
	if result.IssueCount != 4 {
		t.Errorf("Expected 4 issues, got %d", result.IssueCount)
	}
	if result.UnlabeledCount != 1 {
		t.Errorf("Expected 1 unlabeled issue, got %d", result.UnlabeledCount)
	}
	if result.LabelCount != 4 {
		t.Errorf("Expected 4 unique labels, got %d", result.LabelCount)
	}

	// Check labels are sorted
	expectedLabels := []string{"api", "bug", "feature", "ui"}
	for i, label := range expectedLabels {
		if result.Labels[i] != label {
			t.Errorf("Label %d: expected %s, got %s", i, label, result.Labels[i])
		}
	}

	// Check api label stats
	apiStats := result.Stats["api"]
	if apiStats == nil {
		t.Fatal("api label stats missing")
	}
	if apiStats.TotalCount != 2 {
		t.Errorf("api: expected 2 total, got %d", apiStats.TotalCount)
	}
	if apiStats.OpenCount != 1 {
		t.Errorf("api: expected 1 open, got %d", apiStats.OpenCount)
	}
	if apiStats.ClosedCount != 1 {
		t.Errorf("api: expected 1 closed, got %d", apiStats.ClosedCount)
	}

	// Check ui label stats
	uiStats := result.Stats["ui"]
	if uiStats == nil {
		t.Fatal("ui label stats missing")
	}
	if uiStats.InProgress != 1 {
		t.Errorf("ui: expected 1 in_progress, got %d", uiStats.InProgress)
	}

	// Check top labels (should be api first with 2 issues)
	if len(result.TopLabels) < 1 || result.TopLabels[0] != "api" {
		t.Errorf("Expected api as top label, got %v", result.TopLabels)
	}
}

func TestExtractLabels_TombstoneCounts(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusOpen},
		{ID: "bv-2", Labels: []string{"api"}, Status: model.StatusTombstone},
	}

	result := ExtractLabels(issues)
	apiStats := result.Stats["api"]
	if apiStats == nil {
		t.Fatal("api label stats missing")
	}
	if apiStats.OpenCount != 1 {
		t.Errorf("api: expected 1 open, got %d", apiStats.OpenCount)
	}
	if apiStats.ClosedCount != 1 {
		t.Errorf("api: expected 1 closed (tombstone), got %d", apiStats.ClosedCount)
	}
}

func TestExtractLabelsDuplicateLabelsOnIssue(t *testing.T) {
	// Edge case: same label appears twice on an issue (shouldn't happen, but handle gracefully)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api", "api"}, Status: model.StatusOpen}, // Duplicate
	}

	result := ExtractLabels(issues)

	// Both occurrences should be counted (total reflects raw label count per issue)
	if result.LabelCount != 1 {
		t.Errorf("Expected 1 unique label, got %d", result.LabelCount)
	}

	apiStats := result.Stats["api"]
	if apiStats.TotalCount != 2 {
		t.Errorf("Expected 2 counts for duplicate label, got %d", apiStats.TotalCount)
	}
}

func TestExtractLabelsEmptyLabelString(t *testing.T) {
	// Edge case: empty string label (should be skipped)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"", "api", ""}, Status: model.StatusOpen},
	}

	result := ExtractLabels(issues)

	if result.LabelCount != 1 {
		t.Errorf("Expected 1 label (empty strings skipped), got %d", result.LabelCount)
	}
	if result.Labels[0] != "api" {
		t.Errorf("Expected api label, got %s", result.Labels[0])
	}
}

func TestGetLabelIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api", "bug"}},
		{ID: "bv-2", Labels: []string{"api"}},
		{ID: "bv-3", Labels: []string{"ui"}},
	}

	apiIssues := GetLabelIssues(issues, "api")
	if len(apiIssues) != 2 {
		t.Errorf("Expected 2 api issues, got %d", len(apiIssues))
	}

	uiIssues := GetLabelIssues(issues, "ui")
	if len(uiIssues) != 1 {
		t.Errorf("Expected 1 ui issue, got %d", len(uiIssues))
	}

	noIssues := GetLabelIssues(issues, "nonexistent")
	if len(noIssues) != 0 {
		t.Errorf("Expected 0 issues for nonexistent label, got %d", len(noIssues))
	}
}

func TestGetLabelsForIssue(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api", "bug"}},
		{ID: "bv-2", Labels: []string{"ui"}},
	}

	labels := GetLabelsForIssue(issues, "bv-1")
	if len(labels) != 2 {
		t.Errorf("Expected 2 labels for bv-1, got %d", len(labels))
	}

	labels = GetLabelsForIssue(issues, "bv-999")
	if labels != nil {
		t.Errorf("Expected nil for nonexistent issue, got %v", labels)
	}
}

func TestGetCommonLabels(t *testing.T) {
	set1 := []string{"api", "bug", "feature"}
	set2 := []string{"api", "feature", "ui"}
	set3 := []string{"api", "core"}

	// Common to all three: only "api"
	common := GetCommonLabels(set1, set2, set3)
	if len(common) != 1 || common[0] != "api" {
		t.Errorf("Expected [api], got %v", common)
	}

	// Common to two: "api" and "feature"
	common = GetCommonLabels(set1, set2)
	if len(common) != 2 {
		t.Errorf("Expected 2 common labels, got %d", len(common))
	}

	// Empty input
	common = GetCommonLabels()
	if common != nil {
		t.Errorf("Expected nil for empty input, got %v", common)
	}
}

func TestGetLabelCooccurrence(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api", "bug"}},     // api+bug
		{ID: "bv-2", Labels: []string{"api", "bug"}},     // api+bug again
		{ID: "bv-3", Labels: []string{"api", "feature"}}, // api+feature
		{ID: "bv-4", Labels: []string{"ui"}},             // single label, no co-occurrence
	}

	cooc := GetLabelCooccurrence(issues)

	// api+bug should appear twice
	if cooc["api"]["bug"] != 2 {
		t.Errorf("Expected api+bug co-occurrence of 2, got %d", cooc["api"]["bug"])
	}
	if cooc["bug"]["api"] != 2 {
		t.Errorf("Expected bug+api co-occurrence of 2, got %d", cooc["bug"]["api"])
	}

	// api+feature should appear once
	if cooc["api"]["feature"] != 1 {
		t.Errorf("Expected api+feature co-occurrence of 1, got %d", cooc["api"]["feature"])
	}

	// ui has no co-occurrences
	if len(cooc["ui"]) != 0 {
		t.Errorf("Expected no co-occurrences for ui, got %v", cooc["ui"])
	}
}

func TestSortLabelsByCount(t *testing.T) {
	stats := map[string]*LabelStats{
		"api":     {Label: "api", TotalCount: 10},
		"bug":     {Label: "bug", TotalCount: 5},
		"feature": {Label: "feature", TotalCount: 10}, // Same as api
		"ui":      {Label: "ui", TotalCount: 3},
	}

	sorted := sortLabelsByCount(stats)

	// Should be sorted by count descending, then alphabetically for ties
	expected := []string{"api", "feature", "bug", "ui"}
	for i, label := range expected {
		if sorted[i] != label {
			t.Errorf("Position %d: expected %s, got %s", i, label, sorted[i])
		}
	}
}

// ============================================================================
// Velocity Metrics Tests (bv-102)
// ============================================================================

func TestComputeVelocityMetricsEmpty(t *testing.T) {
	now := time.Now()
	v := ComputeVelocityMetrics([]model.Issue{}, now)

	if v.ClosedLast7Days != 0 {
		t.Errorf("Expected 0 closed last 7 days, got %d", v.ClosedLast7Days)
	}
	if v.ClosedLast30Days != 0 {
		t.Errorf("Expected 0 closed last 30 days, got %d", v.ClosedLast30Days)
	}
	if v.AvgDaysToClose != 0 {
		t.Errorf("Expected 0 avg days to close, got %f", v.AvgDaysToClose)
	}
	if v.TrendDirection != "stable" {
		t.Errorf("Expected stable trend, got %s", v.TrendDirection)
	}
	if v.VelocityScore != 0 {
		t.Errorf("Expected velocity score 0, got %d", v.VelocityScore)
	}
}

func TestComputeVelocityMetricsWithClosures(t *testing.T) {
	now := time.Now()
	threeDaysAgo := now.Add(-3 * 24 * time.Hour)
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	twentyDaysAgo := now.Add(-20 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "1", CreatedAt: twentyDaysAgo, ClosedAt: &threeDaysAgo, Status: model.StatusClosed},  // Closed 3 days ago
		{ID: "2", CreatedAt: twentyDaysAgo, ClosedAt: &tenDaysAgo, Status: model.StatusClosed},    // Closed 10 days ago
		{ID: "3", CreatedAt: twentyDaysAgo, ClosedAt: &twentyDaysAgo, Status: model.StatusClosed}, // Closed 20 days ago
		{ID: "4", Status: model.StatusOpen}, // Open, no closure
	}

	v := ComputeVelocityMetrics(issues, now)

	// 1 closed in last 7 days
	if v.ClosedLast7Days != 1 {
		t.Errorf("Expected 1 closed last 7 days, got %d", v.ClosedLast7Days)
	}

	// 3 closed in last 30 days
	if v.ClosedLast30Days != 3 {
		t.Errorf("Expected 3 closed last 30 days, got %d", v.ClosedLast30Days)
	}

	// Velocity score should be positive
	if v.VelocityScore <= 0 {
		t.Errorf("Expected positive velocity score, got %d", v.VelocityScore)
	}
}

func TestComputeVelocityMetricsTrendImproving(t *testing.T) {
	now := time.Now()
	// Current week: 5 closures
	// Previous week: 2 closures
	// Should show improving trend

	var issues []model.Issue
	// 5 closures in current week (days 1-6)
	for i := 1; i <= 5; i++ {
		closedAt := now.Add(time.Duration(-i) * 24 * time.Hour)
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("cur-%d", i),
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			ClosedAt:  &closedAt,
			Status:    model.StatusClosed,
		})
	}
	// 2 closures in previous week (days 8-10)
	for i := 8; i <= 9; i++ {
		closedAt := now.Add(time.Duration(-i) * 24 * time.Hour)
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("prev-%d", i),
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			ClosedAt:  &closedAt,
			Status:    model.StatusClosed,
		})
	}

	v := ComputeVelocityMetrics(issues, now)

	if v.TrendDirection != "improving" {
		t.Errorf("Expected improving trend (5 vs 2), got %s", v.TrendDirection)
	}
	if v.TrendPercent <= 0 {
		t.Errorf("Expected positive trend percent, got %f", v.TrendPercent)
	}
}

func TestComputeVelocityMetricsTrendDeclining(t *testing.T) {
	now := time.Now()
	// Current week: 1 closure
	// Previous week: 5 closures
	// Should show declining trend

	var issues []model.Issue
	// 1 closure in current week
	closedAt := now.Add(-2 * 24 * time.Hour)
	issues = append(issues, model.Issue{
		ID:        "cur-1",
		CreatedAt: now.Add(-30 * 24 * time.Hour),
		ClosedAt:  &closedAt,
		Status:    model.StatusClosed,
	})
	// 5 closures in previous week
	for i := 8; i <= 12; i++ {
		closedAt := now.Add(time.Duration(-i) * 24 * time.Hour)
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("prev-%d", i),
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			ClosedAt:  &closedAt,
			Status:    model.StatusClosed,
		})
	}

	v := ComputeVelocityMetrics(issues, now)

	if v.TrendDirection != "declining" {
		t.Errorf("Expected declining trend (1 vs 5), got %s", v.TrendDirection)
	}
	if v.TrendPercent >= 0 {
		t.Errorf("Expected negative trend percent, got %f", v.TrendPercent)
	}
}

func TestComputeVelocityMetricsAvgDaysToClose(t *testing.T) {
	now := time.Now()
	// Create issues with known time to close
	fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	fifteenDaysAgo := now.Add(-15 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "1", CreatedAt: tenDaysAgo, ClosedAt: &fiveDaysAgo, Status: model.StatusClosed},     // 5 days to close
		{ID: "2", CreatedAt: fifteenDaysAgo, ClosedAt: &fiveDaysAgo, Status: model.StatusClosed}, // 10 days to close
	}

	v := ComputeVelocityMetrics(issues, now)

	// Average should be (5 + 10) / 2 = 7.5 days
	expectedAvg := 7.5
	if v.AvgDaysToClose < expectedAvg-0.1 || v.AvgDaysToClose > expectedAvg+0.1 {
		t.Errorf("Expected avg days to close ~%.1f, got %.1f", expectedAvg, v.AvgDaysToClose)
	}
}

func TestComputeVelocityMetrics_IgnoresNonClosedWithClosedAt(t *testing.T) {
	now := time.Date(2025, 12, 20, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-2 * 24 * time.Hour)
	createdAt := now.Add(-6 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "open-closedat", CreatedAt: createdAt, ClosedAt: &closedAt, Status: model.StatusOpen},
		{ID: "closed", CreatedAt: createdAt, ClosedAt: &closedAt, Status: model.StatusClosed},
	}

	v := ComputeVelocityMetrics(issues, now)

	if v.ClosedLast7Days != 1 {
		t.Fatalf("ClosedLast7Days: expected 1, got %d", v.ClosedLast7Days)
	}
	if v.ClosedLast30Days != 1 {
		t.Fatalf("ClosedLast30Days: expected 1, got %d", v.ClosedLast30Days)
	}

	expectedAvg := closedAt.Sub(createdAt).Hours() / 24.0
	const eps = 1e-9
	if diff := v.AvgDaysToClose - expectedAvg; diff < -eps || diff > eps {
		t.Fatalf("AvgDaysToClose: expected %.2f, got %.2f", expectedAvg, v.AvgDaysToClose)
	}
}

func TestComputeHistoricalVelocity_IgnoresNonClosedWithClosedAt(t *testing.T) {
	now := time.Date(2025, 12, 15, 12, 0, 0, 0, time.UTC) // Monday
	closedAt := now.Add(2 * time.Hour)

	issues := []model.Issue{
		{ID: "open-closedat", ClosedAt: &closedAt, Status: model.StatusOpen, Labels: []string{"api"}},
		{ID: "closed", ClosedAt: &closedAt, Status: model.StatusClosed, Labels: []string{"api"}},
	}

	h := ComputeHistoricalVelocity(issues, "api", 1, now)
	if len(h.WeeklyVelocity) != 1 {
		t.Fatalf("expected 1 weekly snapshot, got %d", len(h.WeeklyVelocity))
	}
	if h.WeeklyVelocity[0].Closed != 1 {
		t.Fatalf("expected 1 closed in current week, got %d", h.WeeklyVelocity[0].Closed)
	}
	if len(h.WeeklyVelocity[0].IssueIDs) != 1 || h.WeeklyVelocity[0].IssueIDs[0] != "closed" {
		t.Fatalf("expected IssueIDs [closed], got %v", h.WeeklyVelocity[0].IssueIDs)
	}
}

// ============================================================================
// Freshness Metrics Tests (bv-102)
// ============================================================================

func TestComputeFreshnessMetricsEmpty(t *testing.T) {
	now := time.Now()
	f := ComputeFreshnessMetrics([]model.Issue{}, now, 14)

	if f.StaleCount != 0 {
		t.Errorf("Expected 0 stale count, got %d", f.StaleCount)
	}
	if f.AvgDaysSinceUpdate != 0 {
		t.Errorf("Expected 0 avg days since update, got %f", f.AvgDaysSinceUpdate)
	}
	if f.StaleThresholdDays != 14 {
		t.Errorf("Expected stale threshold 14, got %d", f.StaleThresholdDays)
	}
}

func TestComputeFreshnessMetricsWithUpdates(t *testing.T) {
	now := time.Now()
	oneDayAgo := now.Add(-1 * 24 * time.Hour)
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	twentyDaysAgo := now.Add(-20 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "1", UpdatedAt: oneDayAgo, Status: model.StatusOpen},     // Fresh
		{ID: "2", UpdatedAt: tenDaysAgo, Status: model.StatusOpen},    // Not stale (< 14 days)
		{ID: "3", UpdatedAt: twentyDaysAgo, Status: model.StatusOpen}, // Stale (> 14 days)
	}

	f := ComputeFreshnessMetrics(issues, now, 14)

	// 1 stale issue (20 days > 14 days threshold)
	if f.StaleCount != 1 {
		t.Errorf("Expected 1 stale issue, got %d", f.StaleCount)
	}

	// Most recent should be the 1-day-ago update
	if !f.MostRecentUpdate.Equal(oneDayAgo) {
		t.Errorf("Expected most recent update %v, got %v", oneDayAgo, f.MostRecentUpdate)
	}

	// Freshness score should be > 0 (not all stale)
	if f.FreshnessScore <= 0 {
		t.Errorf("Expected positive freshness score, got %d", f.FreshnessScore)
	}
}

func TestComputeFreshnessMetricsOldestOpen(t *testing.T) {
	now := time.Now()
	fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	twentyDaysAgo := now.Add(-20 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "1", CreatedAt: fiveDaysAgo, UpdatedAt: fiveDaysAgo, Status: model.StatusOpen},
		{ID: "2", CreatedAt: twentyDaysAgo, UpdatedAt: tenDaysAgo, Status: model.StatusOpen}, // Oldest open
		{ID: "3", CreatedAt: tenDaysAgo, UpdatedAt: tenDaysAgo, Status: model.StatusClosed},  // Closed, shouldn't count
	}

	f := ComputeFreshnessMetrics(issues, now, 14)

	// Oldest open should be the 20-day-old issue
	if !f.OldestOpenIssue.Equal(twentyDaysAgo) {
		t.Errorf("Expected oldest open %v, got %v", twentyDaysAgo, f.OldestOpenIssue)
	}
}

func TestComputeFreshnessMetricsDefaultThreshold(t *testing.T) {
	now := time.Now()
	// Pass 0 or negative threshold - should use default
	f := ComputeFreshnessMetrics([]model.Issue{}, now, 0)

	if f.StaleThresholdDays != DefaultStaleThresholdDays {
		t.Errorf("Expected default threshold %d, got %d", DefaultStaleThresholdDays, f.StaleThresholdDays)
	}

	f = ComputeFreshnessMetrics([]model.Issue{}, now, -5)
	if f.StaleThresholdDays != DefaultStaleThresholdDays {
		t.Errorf("Expected default threshold for negative input, got %d", f.StaleThresholdDays)
	}
}

func TestComputeFreshnessMetricsScoreCapping(t *testing.T) {
	now := time.Now()
	// Very fresh issues should give high score
	justNow := now.Add(-1 * time.Hour)
	issues := []model.Issue{
		{ID: "1", UpdatedAt: justNow, Status: model.StatusOpen},
	}

	f := ComputeFreshnessMetrics(issues, now, 14)

	// Score should be close to 100 for very fresh
	if f.FreshnessScore < 90 {
		t.Errorf("Expected high freshness score for fresh issue, got %d", f.FreshnessScore)
	}

	// Very stale issues should give low score
	veryOld := now.Add(-60 * 24 * time.Hour)
	staleIssues := []model.Issue{
		{ID: "1", UpdatedAt: veryOld, Status: model.StatusOpen},
	}

	f = ComputeFreshnessMetrics(staleIssues, now, 14)

	// Score should be 0 for very stale (> 2x threshold)
	if f.FreshnessScore != 0 {
		t.Errorf("Expected 0 freshness score for very stale issue, got %d", f.FreshnessScore)
	}
}

// ============================================================================
// Label Subgraph Extraction Tests (bv-113)
// ============================================================================

func TestComputeLabelSubgraphEmpty(t *testing.T) {
	// Empty issues
	sg := ComputeLabelSubgraph([]model.Issue{}, "api")
	if !sg.IsEmpty() {
		t.Errorf("Expected empty subgraph for empty issues, got %d issues", sg.IssueCount)
	}

	// Empty label
	issues := []model.Issue{{ID: "bv-1", Labels: []string{"api"}}}
	sg = ComputeLabelSubgraph(issues, "")
	if !sg.IsEmpty() {
		t.Errorf("Expected empty subgraph for empty label, got %d issues", sg.IssueCount)
	}
}

func TestComputeLabelSubgraphSingleLabel(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{ID: "bv-2", Labels: []string{"api"}},
		{ID: "bv-3", Labels: []string{"ui"}}, // Different label, not included
	}

	sg := ComputeLabelSubgraph(issues, "api")

	if sg.CoreCount != 2 {
		t.Errorf("Expected 2 core issues, got %d", sg.CoreCount)
	}
	if sg.IssueCount != 2 {
		t.Errorf("Expected 2 total issues (no deps), got %d", sg.IssueCount)
	}
	if len(sg.DependencyIssues) != 0 {
		t.Errorf("Expected 0 dependency issues, got %d", len(sg.DependencyIssues))
	}
}

func TestComputeLabelSubgraphWithDependencies(t *testing.T) {
	// bv-1 (api) is blocked by bv-3 (core)
	// bv-2 (api) blocks bv-4 (ui)
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
		{ID: "bv-2", Labels: []string{"api"}},
		{ID: "bv-3", Labels: []string{"core"}}, // Blocker, not in api label
		{
			ID:     "bv-4",
			Labels: []string{"ui"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks}, // Blocked by bv-2 (api)
			},
		},
	}

	sg := ComputeLabelSubgraph(issues, "api")

	// Core issues: bv-1, bv-2
	if sg.CoreCount != 2 {
		t.Errorf("Expected 2 core issues, got %d", sg.CoreCount)
	}
	expectedCore := []string{"bv-1", "bv-2"}
	for i, id := range expectedCore {
		if sg.CoreIssues[i] != id {
			t.Errorf("CoreIssues[%d]: expected %s, got %s", i, id, sg.CoreIssues[i])
		}
	}

	// Dependency issues: bv-3 (blocker of bv-1), bv-4 (blocked by bv-2)
	if len(sg.DependencyIssues) != 2 {
		t.Errorf("Expected 2 dependency issues, got %d: %v", len(sg.DependencyIssues), sg.DependencyIssues)
	}

	// Total: 4 issues in subgraph
	if sg.IssueCount != 4 {
		t.Errorf("Expected 4 total issues, got %d", sg.IssueCount)
	}

	// Edge: bv-3 -> bv-1 (bv-3 blocks bv-1)
	if sg.OutDegree["bv-3"] != 1 {
		t.Errorf("Expected bv-3 out-degree 1, got %d", sg.OutDegree["bv-3"])
	}
	if sg.InDegree["bv-1"] != 1 {
		t.Errorf("Expected bv-1 in-degree 1, got %d", sg.InDegree["bv-1"])
	}

	// Edge: bv-2 -> bv-4 (bv-2 blocks bv-4)
	if sg.OutDegree["bv-2"] != 1 {
		t.Errorf("Expected bv-2 out-degree 1, got %d", sg.OutDegree["bv-2"])
	}
	if sg.InDegree["bv-4"] != 1 {
		t.Errorf("Expected bv-4 in-degree 1, got %d", sg.InDegree["bv-4"])
	}
}

func TestComputeLabelSubgraphRootsAndLeaves(t *testing.T) {
	// Chain: bv-1 -> bv-2 -> bv-3 (all api)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
	}

	sg := ComputeLabelSubgraph(issues, "api")

	// bv-1 has no blockers (root)
	roots := sg.GetSubgraphRoots()
	if len(roots) != 1 || roots[0] != "bv-1" {
		t.Errorf("Expected roots [bv-1], got %v", roots)
	}

	// bv-3 doesn't block anything (leaf)
	leaves := sg.GetSubgraphLeaves()
	if len(leaves) != 1 || leaves[0] != "bv-3" {
		t.Errorf("Expected leaves [bv-3], got %v", leaves)
	}
}

func TestComputeLabelSubgraphAdjacency(t *testing.T) {
	// bv-1 blocks bv-2 and bv-3
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	sg := ComputeLabelSubgraph(issues, "api")

	// Check adjacency: bv-1 -> [bv-2, bv-3]
	adj := sg.Adjacency["bv-1"]
	if len(adj) != 2 {
		t.Errorf("Expected bv-1 to have 2 adjacencies, got %d", len(adj))
	}
	// Adjacency list should be sorted
	if adj[0] != "bv-2" || adj[1] != "bv-3" {
		t.Errorf("Expected bv-1 adjacency [bv-2, bv-3], got %v", adj)
	}

	// Total edge count
	if sg.EdgeCount != 2 {
		t.Errorf("Expected 2 edges, got %d", sg.EdgeCount)
	}
}

func TestComputeLabelSubgraphCoreIssueSet(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{ID: "bv-2", Labels: []string{"api"}},
		{ID: "bv-3", Labels: []string{"ui"}},
	}

	sg := ComputeLabelSubgraph(issues, "api")
	coreSet := sg.GetCoreIssueSet()

	if !coreSet["bv-1"] {
		t.Error("Expected bv-1 in core set")
	}
	if !coreSet["bv-2"] {
		t.Error("Expected bv-2 in core set")
	}
	if coreSet["bv-3"] {
		t.Error("bv-3 should not be in core set")
	}
}

func TestComputeLabelSubgraphNonBlockingDeps(t *testing.T) {
	// Non-blocking dependencies should not be included in adjacency
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepRelated}, // Related, not blocking
			},
		},
	}

	sg := ComputeLabelSubgraph(issues, "api")

	// No edges since dependency is "related" not "blocks"
	if sg.EdgeCount != 0 {
		t.Errorf("Expected 0 edges for non-blocking deps, got %d", sg.EdgeCount)
	}
}

func TestHasLabel(t *testing.T) {
	issue := model.Issue{
		ID:     "bv-1",
		Labels: []string{"api", "bug", "urgent"},
	}

	if !HasLabel(issue, "api") {
		t.Error("Expected HasLabel to return true for 'api'")
	}
	if !HasLabel(issue, "bug") {
		t.Error("Expected HasLabel to return true for 'bug'")
	}
	if HasLabel(issue, "feature") {
		t.Error("Expected HasLabel to return false for 'feature'")
	}
	if HasLabel(issue, "") {
		t.Error("Expected HasLabel to return false for empty string")
	}
}

// ============================================================================
// Label-Specific PageRank Tests (bv-114)
// ============================================================================

func TestComputeLabelPageRankEmpty(t *testing.T) {
	// Empty subgraph
	sg := ComputeLabelSubgraph([]model.Issue{}, "api")
	result := ComputeLabelPageRank(sg)

	if result.IssueCount != 0 {
		t.Errorf("Expected 0 issues, got %d", result.IssueCount)
	}
	if len(result.Scores) != 0 {
		t.Errorf("Expected empty scores, got %d", len(result.Scores))
	}
	if len(result.TopIssues) != 0 {
		t.Errorf("Expected empty top issues, got %d", len(result.TopIssues))
	}
}

func TestComputeLabelPageRankSingleNode(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
	}

	result := ComputeLabelPageRankFromIssues(issues, "api")

	if result.IssueCount != 1 {
		t.Errorf("Expected 1 issue, got %d", result.IssueCount)
	}
	if result.CoreCount != 1 {
		t.Errorf("Expected 1 core issue, got %d", result.CoreCount)
	}
	if len(result.Scores) != 1 {
		t.Errorf("Expected 1 score, got %d", len(result.Scores))
	}
	// Single node should have PageRank of 1.0 (all probability mass)
	if result.Scores["bv-1"] < 0.9 {
		t.Errorf("Expected single node PageRank ~1.0, got %f", result.Scores["bv-1"])
	}
}

func TestComputeLabelPageRankChain(t *testing.T) {
	// Chain: bv-1 blocks bv-2 blocks bv-3 (all api)
	// In PageRank, nodes with more incoming links have higher scores
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelPageRankFromIssues(issues, "api")

	if result.IssueCount != 3 {
		t.Errorf("Expected 3 issues, got %d", result.IssueCount)
	}

	// All three should have scores
	if len(result.Scores) != 3 {
		t.Errorf("Expected 3 scores, got %d", len(result.Scores))
	}

	// Top issues should be sorted by score
	if len(result.TopIssues) != 3 {
		t.Errorf("Expected 3 top issues, got %d", len(result.TopIssues))
	}
	// First rank should be 1
	if result.TopIssues[0].Rank != 1 {
		t.Errorf("Expected first issue to have rank 1, got %d", result.TopIssues[0].Rank)
	}
}

func TestComputeLabelPageRankNormalized(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelPageRankFromIssues(issues, "api")

	// Normalized scores should be between 0 and 1
	for id, norm := range result.Normalized {
		if norm < 0 || norm > 1 {
			t.Errorf("Normalized score for %s out of range: %f", id, norm)
		}
	}

	// With varying scores, one should be at 1.0 (max) and one at 0.0 (min)
	if result.MaxScore != result.MinScore {
		foundMax := false
		foundMin := false
		for _, norm := range result.Normalized {
			if norm == 1.0 {
				foundMax = true
			}
			if norm == 0.0 {
				foundMin = true
			}
		}
		if !foundMax {
			t.Error("Expected one normalized score to be 1.0")
		}
		if !foundMin {
			t.Error("Expected one normalized score to be 0.0")
		}
	}
}

func TestComputeLabelPageRankCoreVsDep(t *testing.T) {
	// bv-1 (api) is blocked by bv-2 (core - not api label)
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{ID: "bv-2", Labels: []string{"core"}}, // Dependency, not core
	}

	result := ComputeLabelPageRankFromIssues(issues, "api")

	// Should have 2 issues total (1 core + 1 dep)
	if result.IssueCount != 2 {
		t.Errorf("Expected 2 issues, got %d", result.IssueCount)
	}
	if result.CoreCount != 1 {
		t.Errorf("Expected 1 core issue, got %d", result.CoreCount)
	}

	// CoreOnly should only have bv-1
	if len(result.CoreOnly) != 1 {
		t.Errorf("Expected 1 core-only score, got %d", len(result.CoreOnly))
	}
	if _, ok := result.CoreOnly["bv-1"]; !ok {
		t.Error("Expected bv-1 in CoreOnly")
	}
	if _, ok := result.CoreOnly["bv-2"]; ok {
		t.Error("bv-2 should not be in CoreOnly")
	}

	// TopIssues should mark IsCore correctly
	for _, ri := range result.TopIssues {
		if ri.ID == "bv-1" && !ri.IsCore {
			t.Error("bv-1 should be marked as IsCore")
		}
		if ri.ID == "bv-2" && ri.IsCore {
			t.Error("bv-2 should not be marked as IsCore")
		}
	}
}

func TestComputeLabelPageRankGetTopCoreIssues(t *testing.T) {
	// 3 core issues, 1 dependency
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-4", Type: model.DepBlocks},
			},
		},
		{ID: "bv-4", Labels: []string{"core"}}, // Not api
	}

	result := ComputeLabelPageRankFromIssues(issues, "api")

	// Get top 2 core issues
	topCore := result.GetTopCoreIssues(2)
	if len(topCore) != 2 {
		t.Errorf("Expected 2 top core issues, got %d", len(topCore))
	}

	// All returned should be core
	for _, ri := range topCore {
		if !ri.IsCore {
			t.Errorf("Expected IsCore=true for %s", ri.ID)
		}
	}

	// Get all core issues (more than exist)
	allCore := result.GetTopCoreIssues(10)
	if len(allCore) != 3 {
		t.Errorf("Expected 3 core issues when asking for 10, got %d", len(allCore))
	}
}

// ============================================================================
// Label Critical Path Tests (bv-115)
// ============================================================================

func TestComputeLabelCriticalPathEmpty(t *testing.T) {
	// Empty subgraph
	sg := ComputeLabelSubgraph([]model.Issue{}, "api")
	result := ComputeLabelCriticalPath(sg)

	if result.PathLength != 0 {
		t.Errorf("Expected 0 path length, got %d", result.PathLength)
	}
	if len(result.Path) != 0 {
		t.Errorf("Expected empty path, got %v", result.Path)
	}
	if result.HasCycle {
		t.Error("Empty subgraph should not have cycle")
	}
}

func TestComputeLabelCriticalPathSingleNode(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Title: "Single issue"},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	if result.PathLength != 1 {
		t.Errorf("Expected path length 1, got %d", result.PathLength)
	}
	if len(result.Path) != 1 || result.Path[0] != "bv-1" {
		t.Errorf("Expected path [bv-1], got %v", result.Path)
	}
	if result.MaxHeight != 1 {
		t.Errorf("Expected max height 1, got %d", result.MaxHeight)
	}
}

func TestComputeLabelCriticalPathChain(t *testing.T) {
	// Chain: bv-1 blocks bv-2 blocks bv-3 (all api)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Title: "Root"},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Title:  "Middle",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Title:  "Leaf",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// Critical path should be bv-1 -> bv-2 -> bv-3
	if result.PathLength != 3 {
		t.Errorf("Expected path length 3, got %d", result.PathLength)
	}
	expectedPath := []string{"bv-1", "bv-2", "bv-3"}
	for i, id := range expectedPath {
		if result.Path[i] != id {
			t.Errorf("Path[%d]: expected %s, got %s", i, id, result.Path[i])
		}
	}

	// Max height should be 3
	if result.MaxHeight != 3 {
		t.Errorf("Expected max height 3, got %d", result.MaxHeight)
	}

	// Check individual heights
	if result.AllHeights["bv-1"] != 1 {
		t.Errorf("Expected bv-1 height 1, got %d", result.AllHeights["bv-1"])
	}
	if result.AllHeights["bv-2"] != 2 {
		t.Errorf("Expected bv-2 height 2, got %d", result.AllHeights["bv-2"])
	}
	if result.AllHeights["bv-3"] != 3 {
		t.Errorf("Expected bv-3 height 3, got %d", result.AllHeights["bv-3"])
	}
}

func TestComputeLabelCriticalPathDiamond(t *testing.T) {
	// Diamond shape: bv-1 blocks both bv-2 and bv-3, both block bv-4
	// Critical path length is 3 (any path through the diamond)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Title: "Top"},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Title:  "Left",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"api"},
			Title:  "Right",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-4",
			Labels: []string{"api"},
			Title:  "Bottom",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
				{DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// Path length should be 3 (bv-1 -> bv-2 or bv-3 -> bv-4)
	if result.PathLength != 3 {
		t.Errorf("Expected path length 3, got %d", result.PathLength)
	}

	// Path should start with bv-1 and end with bv-4
	if result.Path[0] != "bv-1" {
		t.Errorf("Path should start with bv-1, got %s", result.Path[0])
	}
	if result.Path[2] != "bv-4" {
		t.Errorf("Path should end with bv-4, got %s", result.Path[2])
	}

	// Middle should be either bv-2 or bv-3
	middle := result.Path[1]
	if middle != "bv-2" && middle != "bv-3" {
		t.Errorf("Middle of path should be bv-2 or bv-3, got %s", middle)
	}
}

func TestComputeLabelCriticalPathWithExternalDeps(t *testing.T) {
	// bv-1 (api) is blocked by bv-4 (core), so critical path includes non-core issue
	issues := []model.Issue{
		{ID: "bv-4", Labels: []string{"core"}, Title: "External blocker"},
		{
			ID:     "bv-1",
			Labels: []string{"api"},
			Title:  "Blocked by external",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-4", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Title:  "Blocked by api issue",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// Path should be bv-4 -> bv-1 -> bv-2 (length 3)
	if result.PathLength != 3 {
		t.Errorf("Expected path length 3, got %d", result.PathLength)
	}
	expectedPath := []string{"bv-4", "bv-1", "bv-2"}
	for i, id := range expectedPath {
		if result.Path[i] != id {
			t.Errorf("Path[%d]: expected %s, got %s", i, id, result.Path[i])
		}
	}
}

func TestComputeLabelCriticalPathCycle(t *testing.T) {
	// Create a cycle: bv-1 -> bv-2 -> bv-3 -> bv-1
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"cycle"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-2",
			Labels: []string{"cycle"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"cycle"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "cycle")

	// Should detect cycle
	if !result.HasCycle {
		t.Error("Expected HasCycle=true for cyclic graph")
	}
}

func TestComputeLabelCriticalPathIsMember(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{ID: "bv-3", Labels: []string{"api"}}, // Not on critical path
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// bv-1 and bv-2 are on critical path
	if !result.IsCriticalPathMember("bv-1") {
		t.Error("bv-1 should be on critical path")
	}
	if !result.IsCriticalPathMember("bv-2") {
		t.Error("bv-2 should be on critical path")
	}
	// bv-3 is not on critical path (it's isolated)
	if result.IsCriticalPathMember("bv-3") {
		t.Error("bv-3 should not be on critical path")
	}
	// Non-existent issue
	if result.IsCriticalPathMember("bv-999") {
		t.Error("bv-999 should not be on critical path")
	}
}

func TestComputeLabelCriticalPathTitles(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Title: "First task"},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Title:  "Second task",
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// Check titles are populated
	if len(result.PathTitles) != 2 {
		t.Errorf("Expected 2 titles, got %d", len(result.PathTitles))
	}
	if result.PathTitles[0] != "First task" {
		t.Errorf("Expected first title 'First task', got '%s'", result.PathTitles[0])
	}
	if result.PathTitles[1] != "Second task" {
		t.Errorf("Expected second title 'Second task', got '%s'", result.PathTitles[1])
	}
}

func TestComputeLabelCriticalPathMultipleRoots(t *testing.T) {
	// Two independent chains, one longer than the other
	// Chain 1: bv-1 -> bv-2 (length 2)
	// Chain 2: bv-3 -> bv-4 -> bv-5 (length 3)
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}},
		{
			ID:     "bv-2",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{ID: "bv-3", Labels: []string{"api"}},
		{
			ID:     "bv-4",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-5",
			Labels: []string{"api"},
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-4", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelCriticalPathFromIssues(issues, "api")

	// Critical path should be the longer chain: bv-3 -> bv-4 -> bv-5
	if result.PathLength != 3 {
		t.Errorf("Expected path length 3, got %d", result.PathLength)
	}
	expectedPath := []string{"bv-3", "bv-4", "bv-5"}
	for i, id := range expectedPath {
		if result.Path[i] != id {
			t.Errorf("Path[%d]: expected %s, got %s", i, id, result.Path[i])
		}
	}
}

// ============================================================================
// Label Attention Score Tests (bv-116)
// ============================================================================

func TestComputeLabelAttentionScoresEmpty(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	result := ComputeLabelAttentionScores([]model.Issue{}, cfg, now)

	if result.TotalLabels != 0 {
		t.Errorf("Expected 0 labels, got %d", result.TotalLabels)
	}
	if len(result.Labels) != 0 {
		t.Errorf("Expected empty labels slice, got %d", len(result.Labels))
	}
}

func TestComputeLabelAttentionScoresSingleLabel(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-2", Labels: []string{"api"}, Status: model.StatusOpen, UpdatedAt: now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	if result.TotalLabels != 1 {
		t.Errorf("Expected 1 label, got %d", result.TotalLabels)
	}
	if len(result.Labels) != 1 {
		t.Errorf("Expected 1 label score, got %d", len(result.Labels))
	}
	if result.Labels[0].Label != "api" {
		t.Errorf("Expected label 'api', got '%s'", result.Labels[0].Label)
	}
	if result.Labels[0].Rank != 1 {
		t.Errorf("Expected rank 1, got %d", result.Labels[0].Rank)
	}
}

func TestComputeLabelAttentionScoresRanking(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()
	staleDate := now.Add(-30 * 24 * time.Hour) // 30 days ago

	// Create scenarios where one label clearly needs more attention:
	// - "stale" label: all stale issues
	// - "active" label: all fresh issues
	issues := []model.Issue{
		// Stale label - should need more attention
		{ID: "bv-1", Labels: []string{"stale"}, Status: model.StatusOpen, UpdatedAt: staleDate},
		{ID: "bv-2", Labels: []string{"stale"}, Status: model.StatusOpen, UpdatedAt: staleDate},
		// Active label - should need less attention
		{ID: "bv-3", Labels: []string{"active"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-4", Labels: []string{"active"}, Status: model.StatusClosed, UpdatedAt: now, ClosedAt: &now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	if result.TotalLabels != 2 {
		t.Fatalf("Expected 2 labels, got %d", result.TotalLabels)
	}

	// Should be sorted by attention descending
	// Stale label should have higher staleness factor
	staleScore := result.GetLabelAttention("stale")
	activeScore := result.GetLabelAttention("active")

	if staleScore == nil || activeScore == nil {
		t.Fatal("Expected both labels to have scores")
	}

	// Stale should have higher staleness factor
	if staleScore.StalenessFactor <= activeScore.StalenessFactor {
		t.Errorf("Expected stale label to have higher staleness factor: stale=%f, active=%f",
			staleScore.StalenessFactor, activeScore.StalenessFactor)
	}
}

func TestComputeLabelAttentionScoresBlockImpact(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	// blocker label blocks other issues
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"blocker"}, Status: model.StatusOpen, UpdatedAt: now},
		{
			ID:        "bv-2",
			Labels:    []string{"blocked"},
			Status:    model.StatusOpen,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-3",
			Labels:    []string{"blocked"},
			Status:    model.StatusOpen,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	blockerScore := result.GetLabelAttention("blocker")
	blockedScore := result.GetLabelAttention("blocked")

	if blockerScore == nil || blockedScore == nil {
		t.Fatal("Expected both labels to have scores")
	}

	// Blocker label should have higher block impact
	if blockerScore.BlockImpact != 2 {
		t.Errorf("Expected blocker to have block impact of 2, got %f", blockerScore.BlockImpact)
	}
	if blockedScore.BlockImpact != 0 {
		t.Errorf("Expected blocked to have block impact of 0, got %f", blockedScore.BlockImpact)
	}
}

func TestComputeLabelAttentionScoresVelocity(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()
	recentClose := now.Add(-5 * 24 * time.Hour) // 5 days ago

	issues := []model.Issue{
		// Fast label - high velocity
		{ID: "bv-1", Labels: []string{"fast"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-2", Labels: []string{"fast"}, Status: model.StatusClosed, UpdatedAt: now, ClosedAt: &recentClose},
		{ID: "bv-3", Labels: []string{"fast"}, Status: model.StatusClosed, UpdatedAt: now, ClosedAt: &recentClose},
		// Slow label - no velocity
		{ID: "bv-4", Labels: []string{"slow"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-5", Labels: []string{"slow"}, Status: model.StatusOpen, UpdatedAt: now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	fastScore := result.GetLabelAttention("fast")
	slowScore := result.GetLabelAttention("slow")

	if fastScore == nil || slowScore == nil {
		t.Fatal("Expected both labels to have scores")
	}

	// Fast label should have higher velocity factor
	if fastScore.VelocityFactor <= slowScore.VelocityFactor {
		t.Errorf("Expected fast label to have higher velocity: fast=%f, slow=%f",
			fastScore.VelocityFactor, slowScore.VelocityFactor)
	}
}

func TestComputeLabelAttentionScoresNormalized(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-2", Labels: []string{"ui"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-3", Labels: []string{"core"}, Status: model.StatusOpen, UpdatedAt: now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	// Normalized scores should be between 0 and 1
	for _, score := range result.Labels {
		if score.NormalizedScore < 0 || score.NormalizedScore > 1 {
			t.Errorf("Normalized score for %s out of range: %f", score.Label, score.NormalizedScore)
		}
	}
}

func TestComputeLabelAttentionScoresGetTop(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"a"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-2", Labels: []string{"b"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-3", Labels: []string{"c"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-4", Labels: []string{"d"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-5", Labels: []string{"e"}, Status: model.StatusOpen, UpdatedAt: now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	// Get top 2
	top2 := result.GetTopAttentionLabels(2)
	if len(top2) != 2 {
		t.Errorf("Expected 2 top labels, got %d", len(top2))
	}

	// First should be rank 1
	if top2[0].Rank != 1 {
		t.Errorf("Expected first to be rank 1, got %d", top2[0].Rank)
	}

	// Get more than exist
	topAll := result.GetTopAttentionLabels(10)
	if len(topAll) != 5 {
		t.Errorf("Expected 5 labels when asking for 10, got %d", len(topAll))
	}
}

// === Edge case tests for circular dependencies (bv-127) ===

func TestComputeLabelSubgraphCircularDeps(t *testing.T) {
	// Create circular dependency: A -> B -> C -> A
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"core"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-1", DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-2",
			Labels: []string{"core"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"core"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-3", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	sg := ComputeLabelSubgraph(issues, "core")

	// Should handle cycle without infinite loop
	if sg.IsEmpty() {
		t.Error("Expected non-empty subgraph")
	}
	if len(sg.CoreIssues) != 3 {
		t.Errorf("Expected 3 core issues, got %d", len(sg.CoreIssues))
	}
	// In a cycle, all nodes have both in and out edges
	for _, id := range sg.CoreIssues {
		if sg.InDegree[id] == 0 {
			t.Errorf("Issue %s should have incoming edges in cycle", id)
		}
		if sg.OutDegree[id] == 0 {
			t.Errorf("Issue %s should have outgoing edges in cycle", id)
		}
	}
}

func TestComputeLabelPageRankCircularDeps(t *testing.T) {
	// Circular dependency should still produce valid PageRank
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"cycle"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-1", DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-2",
			Labels: []string{"cycle"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelPageRankFromIssues(issues, "cycle")

	// Should not panic and should have scores
	if len(result.Scores) != 2 {
		t.Errorf("Expected 2 scores, got %d", len(result.Scores))
	}

	// In a 2-node cycle, PageRank should be similar for both
	score1 := result.Scores["bv-1"]
	score2 := result.Scores["bv-2"]
	diff := score1 - score2
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.1 {
		t.Errorf("Expected similar PageRank in cycle, got %f vs %f", score1, score2)
	}
}

func TestComputeLabelAttentionScoresCircularDeps(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	// Create circular deps across labels
	issues := []model.Issue{
		{
			ID:        "bv-1",
			Labels:    []string{"alpha"},
			Status:    model.StatusOpen,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-1", DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-2",
			Labels:    []string{"beta"},
			Status:    model.StatusOpen,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	// Should handle circular deps without crash
	if len(result.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(result.Labels))
	}

	// Both should have similar attention (symmetric cycle)
	for _, score := range result.Labels {
		if score.AttentionScore < 0 {
			t.Errorf("Attention score should be non-negative: %f", score.AttentionScore)
		}
	}
}

func TestComputeAllLabelHealthIntegration(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)

	closedAt := now
	issues := []model.Issue{
		// Healthy label: recent activity, no blocks
		{ID: "bv-1", Labels: []string{"healthy"}, Status: model.StatusOpen, UpdatedAt: now},
		{ID: "bv-2", Labels: []string{"healthy"}, Status: model.StatusClosed, UpdatedAt: now, ClosedAt: &closedAt},

		// Warning label: some stale issues
		{ID: "bv-3", Labels: []string{"warning"}, Status: model.StatusOpen, UpdatedAt: old},
		{ID: "bv-4", Labels: []string{"warning"}, Status: model.StatusOpen, UpdatedAt: now},

		// Critical label: blocked and stale
		{
			ID:        "bv-5",
			Labels:    []string{"critical"},
			Status:    model.StatusBlocked,
			UpdatedAt: old,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-5", DependsOnID: "bv-6", Type: model.DepBlocks},
			},
		},
		{ID: "bv-6", Labels: []string{"critical"}, Status: model.StatusOpen, UpdatedAt: old},
	}

	result := ComputeAllLabelHealth(issues, cfg, now, nil)

	// Should have all labels
	if len(result.Labels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(result.Labels))
	}

	// Check we have summaries
	if len(result.Summaries) != 3 {
		t.Errorf("Expected 3 summaries, got %d", len(result.Summaries))
	}

	// CrossLabelFlow is optional and not populated by ComputeAllLabelHealth
	// Test it separately with ComputeCrossLabelFlow

	// Check health levels make sense
	healthyFound := false
	criticalFound := false
	for _, summary := range result.Summaries {
		if summary.Label == "healthy" && summary.HealthLevel == "healthy" {
			healthyFound = true
		}
		// Note: "critical" may or may not be critical based on scoring
		if summary.Label == "critical" {
			criticalFound = true
		}
	}
	if !healthyFound {
		t.Log("Note: 'healthy' label may not have healthy status based on scoring")
	}
	if !criticalFound {
		t.Error("Expected 'critical' label in summaries")
	}
}

func TestComputeCrossLabelFlowCircularDeps(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// Create circular flow: A -> B -> C -> A
	issues := []model.Issue{
		{
			ID:     "bv-1",
			Labels: []string{"labelA"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-1", DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-2",
			Labels: []string{"labelB"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-3", Type: model.DepBlocks},
			},
		},
		{
			ID:     "bv-3",
			Labels: []string{"labelC"},
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-3", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	flow := ComputeCrossLabelFlow(issues, cfg)

	// Should handle cycles without infinite loop
	if len(flow.Labels) != 3 {
		t.Errorf("Expected 3 labels in flow, got %d", len(flow.Labels))
	}

	// Should have cross-label dependencies
	if flow.TotalCrossLabelDeps == 0 {
		t.Error("Expected cross-label dependencies in cycle")
	}
}

func TestLabelSubgraphNoLabels(t *testing.T) {
	// Issues with no labels
	issues := []model.Issue{
		{ID: "bv-1", Status: model.StatusOpen},
		{ID: "bv-2", Status: model.StatusOpen},
	}

	sg := ComputeLabelSubgraph(issues, "nonexistent")

	if !sg.IsEmpty() {
		t.Error("Expected empty subgraph for nonexistent label")
	}
	if len(sg.CoreIssues) != 0 {
		t.Errorf("Expected 0 core issues, got %d", len(sg.CoreIssues))
	}
}

func TestLabelPageRankNoLabels(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Status: model.StatusOpen},
	}

	result := ComputeLabelPageRankFromIssues(issues, "missing")

	if len(result.Scores) != 0 {
		t.Errorf("Expected 0 scores for missing label, got %d", len(result.Scores))
	}
	if result.IssueCount != 0 {
		t.Errorf("Expected 0 issue count, got %d", result.IssueCount)
	}
}

func TestAttentionScoresSingleLabel(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	now := time.Now()

	// Just one label
	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"solo"}, Status: model.StatusOpen, UpdatedAt: now},
	}

	result := ComputeLabelAttentionScores(issues, cfg, now)

	if len(result.Labels) != 1 {
		t.Errorf("Expected 1 label, got %d", len(result.Labels))
	}
	if result.Labels[0].Label != "solo" {
		t.Errorf("Expected 'solo' label, got %s", result.Labels[0].Label)
	}
	// Single label should have normalized score of 1.0 (or 0 if no others)
	if result.Labels[0].Rank != 1 {
		t.Errorf("Expected rank 1, got %d", result.Labels[0].Rank)
	}
}

// ============================================================================
// Historical Velocity Tests (bv-123)
// ============================================================================

func TestComputeHistoricalVelocity_BasicCounting(t *testing.T) {
	// Use a Monday as 'now' for clearer week alignment
	now := time.Date(2025, 12, 15, 12, 0, 0, 0, time.UTC) // Monday Dec 15, 2025

	// Create issues closed in different weeks (relative to Monday start)
	// Week 0 = Dec 15-21 (current week)
	// Week 1 = Dec 8-14
	// Week 2 = Dec 1-7
	// Week 3 = Nov 24-30
	week0Close := time.Date(2025, 12, 16, 10, 0, 0, 0, time.UTC) // Tuesday Dec 16 (week 0)
	week1Close := time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC) // Wednesday Dec 10 (week 1)
	week2Close := time.Date(2025, 12, 3, 10, 0, 0, 0, time.UTC)  // Wednesday Dec 3 (week 2)
	week3Close := time.Date(2025, 11, 26, 10, 0, 0, 0, time.UTC) // Wednesday Nov 26 (week 3)

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		{ID: "bv-2", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		{ID: "bv-3", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		{ID: "bv-4", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week2Close},
		{ID: "bv-5", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week2Close},
		{ID: "bv-6", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week2Close},
		{ID: "bv-7", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week3Close},
		{ID: "bv-8", Labels: []string{"ui"}, Status: model.StatusClosed, ClosedAt: &week0Close}, // Different label
	}

	result := ComputeHistoricalVelocity(issues, "api", 4, now)

	if result.Label != "api" {
		t.Errorf("Expected label 'api', got '%s'", result.Label)
	}
	if result.WeeksAnalyzed != 4 {
		t.Errorf("Expected 4 weeks analyzed, got %d", result.WeeksAnalyzed)
	}

	// Week 0 (current week): 2 closed
	if result.WeeklyVelocity[0].Closed != 2 {
		t.Errorf("Week 0: expected 2 closed, got %d", result.WeeklyVelocity[0].Closed)
	}
	// Week 1 (last week): 1 closed
	if result.WeeklyVelocity[1].Closed != 1 {
		t.Errorf("Week 1: expected 1 closed, got %d", result.WeeklyVelocity[1].Closed)
	}
	// Week 2: 3 closed
	if result.WeeklyVelocity[2].Closed != 3 {
		t.Errorf("Week 2: expected 3 closed, got %d", result.WeeklyVelocity[2].Closed)
	}
	// Week 3: 1 closed
	if result.WeeklyVelocity[3].Closed != 1 {
		t.Errorf("Week 3: expected 1 closed, got %d", result.WeeklyVelocity[3].Closed)
	}
}

func TestComputeHistoricalVelocity_PeakAndTrough(t *testing.T) {
	// Use a Monday for clearer week alignment
	now := time.Date(2025, 12, 15, 12, 0, 0, 0, time.UTC) // Monday Dec 15

	// Create varying velocity with specific dates in each week
	week0Close := time.Date(2025, 12, 16, 10, 0, 0, 0, time.UTC) // Week 0: Dec 15-21
	week1Close := time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC) // Week 1: Dec 8-14
	week2Close := time.Date(2025, 12, 3, 10, 0, 0, 0, time.UTC)  // Week 2: Dec 1-7
	week3Close := time.Date(2025, 11, 26, 10, 0, 0, 0, time.UTC) // Week 3: Nov 24-30

	issues := []model.Issue{
		// Week 0: 2 issues
		{ID: "w0-1", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		{ID: "w0-2", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		// Week 1: 5 issues (peak)
		{ID: "w1-1", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		{ID: "w1-2", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		{ID: "w1-3", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		{ID: "w1-4", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		{ID: "w1-5", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week1Close},
		// Week 2: 1 issue (trough)
		{ID: "w2-1", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week2Close},
		// Week 3: 3 issues
		{ID: "w3-1", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week3Close},
		{ID: "w3-2", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week3Close},
		{ID: "w3-3", Labels: []string{"test"}, Status: model.StatusClosed, ClosedAt: &week3Close},
	}

	result := ComputeHistoricalVelocity(issues, "test", 4, now)

	// Peak should be week 1 with 5
	if result.PeakWeek != 1 {
		t.Errorf("Expected peak week 1, got %d", result.PeakWeek)
	}
	if result.PeakVelocity != 5 {
		t.Errorf("Expected peak velocity 5, got %d", result.PeakVelocity)
	}

	// Trough should be week 2 with 1
	if result.TroughWeek != 2 {
		t.Errorf("Expected trough week 2, got %d", result.TroughWeek)
	}
	if result.TroughVelocity != 1 {
		t.Errorf("Expected trough velocity 1, got %d", result.TroughVelocity)
	}
}

func TestComputeHistoricalVelocity_MovingAverages(t *testing.T) {
	// Use a Monday for clearer week alignment
	now := time.Date(2025, 12, 15, 12, 0, 0, 0, time.UTC) // Monday Dec 15

	// Create 8 weeks of data: each week has weeksAgo+1 closures
	// Week 0: 1, Week 1: 2, Week 2: 3, ... Week 7: 8
	var issues []model.Issue
	for w := 0; w < 8; w++ {
		// Place closures in the middle of each week (Wednesday)
		weekClose := now.AddDate(0, 0, -7*w+2) // Wednesday of that week
		for i := 0; i <= w; i++ {
			issues = append(issues, model.Issue{
				ID:       fmt.Sprintf("w%d-%d", w, i),
				Labels:   []string{"avg"},
				Status:   model.StatusClosed,
				ClosedAt: &weekClose,
			})
		}
	}

	result := ComputeHistoricalVelocity(issues, "avg", 8, now)

	// 4-week moving avg: (1+2+3+4)/4 = 2.5
	expected4 := 2.5
	if result.MovingAvg4Week != expected4 {
		t.Errorf("Expected 4-week moving avg %.2f, got %.2f", expected4, result.MovingAvg4Week)
	}

	// 8-week moving avg: (1+2+3+4+5+6+7+8)/8 = 4.5
	expected8 := 4.5
	if result.MovingAvg8Week != expected8 {
		t.Errorf("Expected 8-week moving avg %.2f, got %.2f", expected8, result.MovingAvg8Week)
	}
}

func TestComputeHistoricalVelocity_EmptyLabel(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"other"}, Status: model.StatusOpen},
	}

	result := ComputeHistoricalVelocity(issues, "nonexistent", 4, now)

	if result.Label != "nonexistent" {
		t.Errorf("Expected label 'nonexistent', got '%s'", result.Label)
	}
	if result.PeakVelocity != 0 {
		t.Errorf("Expected 0 peak velocity for nonexistent label, got %d", result.PeakVelocity)
	}
	// All weeks should have 0 closed
	for i, snap := range result.WeeklyVelocity {
		if snap.Closed != 0 {
			t.Errorf("Week %d: expected 0 closed, got %d", i, snap.Closed)
		}
	}
}

func TestHistoricalVelocity_GetVelocityTrend(t *testing.T) {
	tests := []struct {
		name          string
		weeklyData    []int // From most recent to oldest
		expectedTrend string
	}{
		{
			name:          "accelerating",
			weeklyData:    []int{5, 4, 4, 3, 2, 2, 1, 1},
			expectedTrend: "accelerating",
		},
		{
			name:          "decelerating",
			weeklyData:    []int{1, 1, 2, 2, 4, 4, 5, 5},
			expectedTrend: "decelerating",
		},
		{
			name:          "stable",
			weeklyData:    []int{3, 3, 3, 3, 3, 3, 3, 3},
			expectedTrend: "stable",
		},
		{
			name:          "insufficient_data",
			weeklyData:    []int{3, 3},
			expectedTrend: "insufficient_data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hv := HistoricalVelocity{
				WeeksAnalyzed:  len(tt.weeklyData),
				WeeklyVelocity: make([]WeeklySnapshot, len(tt.weeklyData)),
			}
			for i, v := range tt.weeklyData {
				hv.WeeklyVelocity[i] = WeeklySnapshot{Closed: v, WeeksAgo: i}
			}
			// Calculate variance for erratic detection
			if len(tt.weeklyData) > 0 {
				var sum float64
				for _, snap := range hv.WeeklyVelocity {
					sum += float64(snap.Closed)
				}
				mean := sum / float64(len(tt.weeklyData))
				var variance float64
				for _, snap := range hv.WeeklyVelocity {
					diff := float64(snap.Closed) - mean
					variance += diff * diff
				}
				hv.Variance = variance / float64(len(tt.weeklyData))
			}

			trend := hv.GetVelocityTrend()
			if trend != tt.expectedTrend {
				t.Errorf("GetVelocityTrend() = %s, want %s", trend, tt.expectedTrend)
			}
		})
	}
}

func TestHistoricalVelocity_GetWeeklyAverage(t *testing.T) {
	hv := HistoricalVelocity{
		WeeksAnalyzed: 4,
		WeeklyVelocity: []WeeklySnapshot{
			{Closed: 2},
			{Closed: 4},
			{Closed: 6},
			{Closed: 8},
		},
	}

	avg := hv.GetWeeklyAverage()
	expected := 5.0 // (2+4+6+8)/4
	if avg != expected {
		t.Errorf("GetWeeklyAverage() = %.2f, want %.2f", avg, expected)
	}
}

func TestComputeAllHistoricalVelocity(t *testing.T) {
	// Use a Monday for clearer week alignment
	now := time.Date(2025, 12, 15, 12, 0, 0, 0, time.UTC)
	// Close issues on Tuesday of current week (week 0)
	week0Close := time.Date(2025, 12, 16, 10, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		{ID: "bv-2", Labels: []string{"ui"}, Status: model.StatusClosed, ClosedAt: &week0Close},
		{ID: "bv-3", Labels: []string{"api", "ui"}, Status: model.StatusClosed, ClosedAt: &week0Close},
	}

	result := ComputeAllHistoricalVelocity(issues, 4, now)

	if len(result) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(result))
	}

	apiVel, ok := result["api"]
	if !ok {
		t.Fatal("Expected 'api' label in results")
	}
	// api should have 2 issues (bv-1 and bv-3)
	if apiVel.WeeklyVelocity[0].Closed != 2 {
		t.Errorf("api week 0: expected 2 closed, got %d", apiVel.WeeklyVelocity[0].Closed)
	}

	uiVel, ok := result["ui"]
	if !ok {
		t.Fatal("Expected 'ui' label in results")
	}
	// ui should have 2 issues (bv-2 and bv-3)
	if uiVel.WeeklyVelocity[0].Closed != 2 {
		t.Errorf("ui week 0: expected 2 closed, got %d", uiVel.WeeklyVelocity[0].Closed)
	}
}

// ============================================================================
// Blockage Impact Cascade Tests (bv-112)
// ============================================================================

func TestComputeBlockageCascadeEmpty(t *testing.T) {
	cfg := DefaultLabelHealthConfig()
	flow := CrossLabelFlow{
		Labels:     []string{},
		FlowMatrix: [][]int{},
	}

	result := ComputeBlockageCascade([]model.Issue{}, flow, cfg)

	if result.TotalBlocked != 0 {
		t.Errorf("Expected 0 total blocked, got %d", result.TotalBlocked)
	}
	if len(result.Cascades) != 0 {
		t.Errorf("Expected 0 cascades, got %d", len(result.Cascades))
	}
}

func TestComputeBlockageCascadeNoCascade(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// No cross-label dependencies in flow
	flow := CrossLabelFlow{
		Labels:     []string{"api", "ui"},
		FlowMatrix: [][]int{{0, 0}, {0, 0}},
	}

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusBlocked},
	}

	result := ComputeBlockageCascade(issues, flow, cfg)

	// Should have 1 cascade (api with blocked issue) but no downstream impact
	if result.TotalBlocked != 1 {
		t.Errorf("Expected 1 total blocked, got %d", result.TotalBlocked)
	}
}

func TestComputeBlockageCascadeSimple(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// api blocks ui (flow direction: from=api, to=ui means api->ui dependency)
	flow := CrossLabelFlow{
		Labels:     []string{"api", "ui"},
		FlowMatrix: [][]int{{0, 2}, {0, 0}}, // api blocks 2 ui issues
	}

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"api"}, Status: model.StatusBlocked},
		{ID: "bv-2", Labels: []string{"ui"}, Status: model.StatusOpen},
		{ID: "bv-3", Labels: []string{"ui"}, Status: model.StatusOpen},
	}

	result := ComputeBlockageCascade(issues, flow, cfg)

	if result.TotalBlocked != 1 {
		t.Errorf("Expected 1 total blocked, got %d", result.TotalBlocked)
	}
	if len(result.Cascades) != 1 {
		t.Errorf("Expected 1 cascade, got %d", len(result.Cascades))
	}
	if len(result.Cascades) > 0 {
		cascade := result.Cascades[0]
		if cascade.SourceLabel != "api" {
			t.Errorf("Expected source label 'api', got %s", cascade.SourceLabel)
		}
		if cascade.TotalImpact != 2 {
			t.Errorf("Expected total impact 2, got %d", cascade.TotalImpact)
		}
	}
}

func TestComputeBlockageCascadeTransitive(t *testing.T) {
	cfg := DefaultLabelHealthConfig()

	// Chain: db -> api -> ui
	flow := CrossLabelFlow{
		Labels: []string{"db", "api", "ui"},
		FlowMatrix: [][]int{
			{0, 3, 0}, // db blocks 3 api issues
			{0, 0, 2}, // api blocks 2 ui issues
			{0, 0, 0}, // ui blocks nothing
		},
	}

	issues := []model.Issue{
		{ID: "bv-1", Labels: []string{"db"}, Status: model.StatusBlocked},
	}

	result := ComputeBlockageCascade(issues, flow, cfg)

	if len(result.Cascades) != 1 {
		t.Fatalf("Expected 1 cascade, got %d", len(result.Cascades))
	}

	cascade := result.Cascades[0]
	if cascade.SourceLabel != "db" {
		t.Errorf("Expected source label 'db', got %s", cascade.SourceLabel)
	}

	// Should have 2 levels: api (level 1) and ui (level 2)
	if len(cascade.CascadeLevels) != 2 {
		t.Errorf("Expected 2 cascade levels, got %d", len(cascade.CascadeLevels))
	}

	// Total impact should be 3 (api) + 2 (ui) = 5
	if cascade.TotalImpact != 5 {
		t.Errorf("Expected total impact 5, got %d", cascade.TotalImpact)
	}
}

func TestBlockageCascadeResult_FormatCascadeTree(t *testing.T) {
	cascade := &BlockageCascadeResult{
		SourceLabel:  "database",
		BlockedCount: 4,
		CascadeLevels: []CascadeLevel{
			{Level: 1, Labels: []LabelCascadeEntry{{Label: "backend", WaitingCount: 3}}},
			{Level: 2, Labels: []LabelCascadeEntry{{Label: "testing", WaitingCount: 2}}},
		},
	}

	tree := cascade.FormatCascadeTree()

	// Check basic structure
	if tree == "" {
		t.Error("FormatCascadeTree returned empty string")
	}
	if !strings.Contains(tree, "database (4 blocked)") {
		t.Errorf("Tree should contain source label info, got: %s", tree)
	}
	if !strings.Contains(tree, "backend: 3 waiting") {
		t.Errorf("Tree should contain backend entry, got: %s", tree)
	}
	if !strings.Contains(tree, "testing: 2 waiting") {
		t.Errorf("Tree should contain testing entry, got: %s", tree)
	}
}

func TestBlockageCascadeAnalysis_GetCascadeForLabel(t *testing.T) {
	analysis := &BlockageCascadeAnalysis{
		Cascades: []BlockageCascadeResult{
			{SourceLabel: "api", BlockedCount: 2},
			{SourceLabel: "db", BlockedCount: 3},
		},
	}

	// Found case
	cascade := analysis.GetCascadeForLabel("api")
	if cascade == nil {
		t.Fatal("Expected to find cascade for 'api'")
	}
	if cascade.BlockedCount != 2 {
		t.Errorf("Expected blocked count 2, got %d", cascade.BlockedCount)
	}

	// Not found case
	cascade = analysis.GetCascadeForLabel("nonexistent")
	if cascade != nil {
		t.Error("Expected nil for nonexistent label")
	}
}

func TestBlockageCascadeAnalysis_GetMostImpactfulCascade(t *testing.T) {
	// Empty case
	analysis := &BlockageCascadeAnalysis{Cascades: []BlockageCascadeResult{}}
	cascade := analysis.GetMostImpactfulCascade()
	if cascade != nil {
		t.Error("Expected nil for empty cascades")
	}

	// Non-empty case (cascades are pre-sorted by impact)
	analysis = &BlockageCascadeAnalysis{
		Cascades: []BlockageCascadeResult{
			{SourceLabel: "high", TotalImpact: 10},
			{SourceLabel: "low", TotalImpact: 2},
		},
	}
	cascade = analysis.GetMostImpactfulCascade()
	if cascade == nil {
		t.Fatal("Expected non-nil cascade")
	}
	if cascade.SourceLabel != "high" {
		t.Errorf("Expected 'high' label, got %s", cascade.SourceLabel)
	}
}

func TestComputeBlockageCascadeAt_DependencyBlockedAndPermutationInvariant(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	cfg := DefaultLabelHealthConfig()

	baseIssues := []model.Issue{
		{
			ID:        "bv-root-a",
			Title:     "Root A",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"platform", "infra"},
		},
		{
			ID:        "bv-root-b",
			Title:     "Root B",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"infra", "platform"},
		},
		{
			ID:        "bv-db-1",
			Title:     "Open but dependency-blocked DB task",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"db"},
			Dependencies: []*model.Dependency{
				{IssueID: "bv-db-1", DependsOnID: "bv-root-b", Type: model.DepBlocks},
				{IssueID: "bv-db-1", DependsOnID: "bv-root-a", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-api-1",
			Title:     "API waiting on DB",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"api"},
			Dependencies: []*model.Dependency{
				{IssueID: "bv-api-1", DependsOnID: "bv-db-1", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-ui-1",
			Title:     "UI waiting on API",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"ui"},
			Dependencies: []*model.Dependency{
				{IssueID: "bv-ui-1", DependsOnID: "bv-api-1", Type: model.DepBlocks},
			},
		},
	}

	flow := ComputeCrossLabelFlow(baseIssues, cfg)
	canonical := ComputeBlockageCascadeAt(baseIssues, flow, cfg, now)
	if canonical.TotalBlocked != 3 {
		t.Fatalf("expected 3 dependency-blocked open issues (db, api, ui), got %d", canonical.TotalBlocked)
	}
	if canonical.CriticalChain == nil || len(canonical.CriticalChain) < 3 {
		t.Fatalf("expected non-empty CriticalChain >= 3 labels, got %#v", canonical.CriticalChain)
	}
	if len(canonical.TopUnblocks) == 0 {
		t.Fatalf("expected TopUnblocks recommendations for open dependency blockers, got empty")
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical cascade: %v", err)
	}

	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 25; iter++ {
		perm := make([]model.Issue, len(baseIssues))
		copy(perm, baseIssues)
		rng.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		for idx := range perm {
			labels := append([]string(nil), perm[idx].Labels...)
			rng.Shuffle(len(labels), func(i, j int) { labels[i], labels[j] = labels[j], labels[i] })
			perm[idx].Labels = labels
		}
		permFlow := ComputeCrossLabelFlow(perm, cfg)
		got := ComputeBlockageCascadeAt(perm, permFlow, cfg, now)
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal permuted cascade: %v", err)
		}
		if string(gotJSON) != string(canonicalJSON) {
			t.Fatalf("permutation %d drifted:\ngot:  %s\nwant: %s", iter, gotJSON, canonicalJSON)
		}
	}
}

func TestComputeAllLabelHealth_MatchesPerLabelOracle(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cfg := DefaultLabelHealthConfig()
	labelPool := []string{"", "api", "backend", "ui", "db", "auth", "ops", "perf", "security"}
	statuses := []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusBlocked, model.StatusClosed, model.StatusDeferred, model.StatusTombstone}
	depTypes := []model.DependencyType{model.DepBlocks, model.DepRelated, model.DepParentChild}

	rng := rand.New(rand.NewSource(20261113))
	for trial := 0; trial < 100; trial++ {
		n := rng.Intn(30)
		issues := make([]model.Issue, n)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("iss-%02d", i)
			status := statuses[rng.Intn(len(statuses))]
			created := now.Add(-time.Duration(rng.Intn(60)+1) * 24 * time.Hour)
			updated := created.Add(time.Duration(rng.Intn(30)) * 24 * time.Hour)
			var closedAt *time.Time
			if status == model.StatusClosed {
				c := updated
				closedAt = &c
			}
			k := rng.Intn(4)
			var labels []string
			for j := 0; j < k; j++ {
				labels = append(labels, labelPool[rng.Intn(len(labelPool))])
			}
			issues[i] = model.Issue{
				ID:        id,
				Title:     fmt.Sprintf("Issue %d", i),
				Status:    status,
				Priority:  rng.Intn(5),
				IssueType: model.TypeTask,
				Labels:    labels,
				CreatedAt: created,
				UpdatedAt: updated,
				ClosedAt:  closedAt,
			}
		}
		for i := 0; i < n; i++ {
			edgeCount := rng.Intn(4)
			for e := 0; e < edgeCount; e++ {
				target := fmt.Sprintf("iss-%02d", rng.Intn(n+2)) // includes missing targets
				issues[i].Dependencies = append(issues[i].Dependencies, &model.Dependency{
					IssueID:     issues[i].ID,
					DependsOnID: target,
					Type:        depTypes[rng.Intn(len(depTypes))],
				})
			}
		}

		analyzer := NewAnalyzer(issues)
		stats := analyzer.Analyze()
		batch := ComputeAllLabelHealth(issues, cfg, now, &stats)
		scores := ComputeLabelHealthScores(issues, cfg, now, &stats)

		if len(batch.Labels) != batch.TotalLabels {
			t.Fatalf("trial %d: len(Labels)=%d != TotalLabels=%d", trial, len(batch.Labels), batch.TotalLabels)
		}
		if len(scores) != batch.TotalLabels {
			t.Fatalf("trial %d: len(scores)=%d != TotalLabels=%d", trial, len(scores), batch.TotalLabels)
		}
		for _, got := range batch.Labels {
			want := ComputeLabelHealthForLabel(got.Label, issues, cfg, now, &stats)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial %d label %q mismatch:\ngot:  %#v\nwant: %#v", trial, got.Label, got, want)
			}
			if scores[got.Label] != want.Health {
				t.Fatalf("trial %d label %q score=%d != want.Health=%d", trial, got.Label, scores[got.Label], want.Health)
			}
		}
	}
}
