package analysis

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestDefaultAdvancedInsightsConfig(t *testing.T) {
	cfg := DefaultAdvancedInsightsConfig()

	if cfg.TopKSetLimit != 5 {
		t.Errorf("TopKSetLimit: expected 5, got %d", cfg.TopKSetLimit)
	}
	if cfg.CoverageSetLimit != 5 {
		t.Errorf("CoverageSetLimit: expected 5, got %d", cfg.CoverageSetLimit)
	}
	if cfg.KPathsLimit != 5 {
		t.Errorf("KPathsLimit: expected 5, got %d", cfg.KPathsLimit)
	}
	if cfg.PathLengthCap != 50 {
		t.Errorf("PathLengthCap: expected 50, got %d", cfg.PathLengthCap)
	}
	if cfg.CycleBreakLimit != 5 {
		t.Errorf("CycleBreakLimit: expected 5, got %d", cfg.CycleBreakLimit)
	}
	if cfg.ParallelCutLimit != 5 {
		t.Errorf("ParallelCutLimit: expected 5, got %d", cfg.ParallelCutLimit)
	}
}

func TestGenerateAdvancedInsightsReportsEffectiveNormalizedConfig(t *testing.T) {
	insights := NewAnalyzer(nil).GenerateAdvancedInsights(AdvancedInsightsConfig{})
	want := DefaultAdvancedInsightsConfig()
	if insights.Config != want {
		t.Fatalf("reported config = %+v, want effective defaults %+v", insights.Config, want)
	}
}

func TestDefaultUsageHints(t *testing.T) {
	hints := DefaultUsageHints()

	expected := []string{"topk_set", "coverage_set", "k_paths", "parallel_cut", "parallel_gain", "cycle_break"}
	for _, key := range expected {
		if hints[key] == "" {
			t.Errorf("Missing usage hint for %s", key)
		}
	}
}

func TestGenerateAdvancedInsightsEmpty(t *testing.T) {
	an := NewAnalyzer([]model.Issue{})
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights == nil {
		t.Fatal("expected non-nil insights")
	}
	if insights.Config.TopKSetLimit != 5 {
		t.Error("config not preserved")
	}
	if len(insights.UsageHints) == 0 {
		t.Error("expected usage hints")
	}

	// All features should have status
	if insights.TopKSet == nil || insights.TopKSet.Status.State == "" {
		t.Error("TopKSet missing or no status")
	}
	if insights.CoverageSet == nil || insights.CoverageSet.Status.State == "" {
		t.Error("CoverageSet missing or no status")
	}
	if insights.KPaths == nil || insights.KPaths.Status.State == "" {
		t.Error("KPaths missing or no status")
	}
	if insights.ParallelCut == nil || insights.ParallelCut.Status.State == "" {
		t.Error("ParallelCut missing or no status")
	}
	if insights.ParallelGain == nil || insights.ParallelGain.Status.State == "" {
		t.Error("ParallelGain missing or no status")
	}
	if insights.CycleBreak == nil || insights.CycleBreak.Status.State == "" {
		t.Error("CycleBreak missing or no status")
	}
}

func TestGenerateAdvancedInsightsNoCycles(t *testing.T) {
	// Linear chain with no cycles
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Cycle break should report no cycles
	if insights.CycleBreak == nil {
		t.Fatal("expected CycleBreak result")
	}
	if insights.CycleBreak.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.CycleBreak.Status.State)
	}
	if insights.CycleBreak.CycleCount != 0 {
		t.Errorf("expected 0 cycles, got %d", insights.CycleBreak.CycleCount)
	}
	if len(insights.CycleBreak.Suggestions) != 0 {
		t.Error("expected no suggestions for acyclic graph")
	}
}

func TestGenerateAdvancedInsightsWithCycles(t *testing.T) {
	// Create a cycle: A -> B -> C -> A
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Cycle break should detect the cycle
	if insights.CycleBreak == nil {
		t.Fatal("expected CycleBreak result")
	}
	if insights.CycleBreak.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.CycleBreak.Status.State)
	}
	if insights.CycleBreak.CycleCount == 0 {
		t.Error("expected cycles to be detected")
	}
	if len(insights.CycleBreak.Suggestions) == 0 {
		t.Error("expected cycle break suggestions")
	}
	if insights.CycleBreak.Advisory == "" {
		t.Error("expected advisory text")
	}
	validEdges := map[string]bool{
		"A->B": true,
		"B->C": true,
		"C->A": true,
	}
	for _, suggestion := range insights.CycleBreak.Suggestions {
		edge := suggestion.EdgeFrom + "->" + suggestion.EdgeTo
		if !validEdges[edge] {
			t.Fatalf("cycle break suggested nonexistent edge %q from closed cycle encoding: %#v", edge, insights.CycleBreak.Suggestions)
		}
	}
	if insights.CycleBreak.Status.Limited != len(validEdges) {
		t.Fatalf("cycle edge count = %d, want %d real edges", insights.CycleBreak.Status.Limited, len(validEdges))
	}
}

func TestCycleBreakCollateralExcludesInactiveDependents(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "CLOSED", Status: model.StatusClosed, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "TOMBSTONE", Status: model.StatusTombstone, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	got := NewAnalyzer(issues).GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).CycleBreak
	for _, suggestion := range got.Suggestions {
		if suggestion.EdgeFrom == "A" && suggestion.EdgeTo == "B" {
			if suggestion.Collateral != 1 {
				t.Fatalf("collateral = %d, want only active dependent A", suggestion.Collateral)
			}
			return
		}
	}
	t.Fatalf("missing A->B cycle-break suggestion: %#v", got.Suggestions)
}

func TestCycleBreakAcceptsCompactSelfLoop(t *testing.T) {
	issues := []model.Issue{{
		ID:           "A",
		Status:       model.StatusOpen,
		Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}},
	}}
	provided := NewGraphStatsForTest(
		nil, nil, nil, nil, nil, nil,
		nil, nil, [][]string{{"A"}}, 0, nil,
	)

	got := NewAnalyzer(issues).GenerateAdvancedInsightsFromStats(provided, DefaultAdvancedInsightsConfig()).CycleBreak
	if got.CycleCount != 1 || len(got.Suggestions) != 1 {
		t.Fatalf("compact self-loop result = %+v, want one cycle and one suggestion", got)
	}
	suggestion := got.Suggestions[0]
	if suggestion.EdgeFrom != "A" || suggestion.EdgeTo != "A" || suggestion.Impact != 1 {
		t.Fatalf("compact self-loop suggestion = %+v, want A->A with impact 1", suggestion)
	}
}

func TestGenerateAdvancedInsightsFromStatsUsesProvidedCyclesAndPreservesCaps(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
		{ID: "D", Status: model.StatusOpen},
	}
	an := NewAnalyzer(issues)
	provided := NewGraphStatsForTest(
		nil, nil, nil, nil, nil, nil,
		nil, nil,
		[][]string{{"A", "B", "C"}, {"A", "B", "D"}},
		0, nil,
	)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.CycleBreakLimit = 1

	insights := an.GenerateAdvancedInsightsFromStats(provided, cfg)
	cycleBreak := insights.CycleBreak
	if cycleBreak.CycleCount != 2 {
		t.Fatalf("CycleCount: expected supplied count 2, got %d", cycleBreak.CycleCount)
	}
	if len(cycleBreak.Suggestions) != 1 {
		t.Fatalf("suggestions: expected cap of 1, got %d", len(cycleBreak.Suggestions))
	}
	got := cycleBreak.Suggestions[0]
	if got.EdgeFrom != "A" || got.EdgeTo != "B" || got.Impact != 2 {
		t.Fatalf("top suggestion: expected supplied shared edge A->B with impact 2, got %+v", got)
	}
	if !cycleBreak.Status.Capped || cycleBreak.Status.Limited != 5 {
		t.Fatalf("status: expected capped=true limited=5, got %+v", cycleBreak.Status)
	}
	if got.InCycles == nil || len(got.InCycles) != 2 || got.InCycles[0] != 0 || got.InCycles[1] != 1 {
		t.Fatalf("cycle ordering: expected [0 1], got %v", got.InCycles)
	}
}

func TestCycleBreakPreservesTruncatedCycleInputEvidence(t *testing.T) {
	an := NewAnalyzer([]model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
		{ID: "D", Status: model.StatusOpen},
	})
	provided := NewGraphStatsForTest(
		nil, nil, nil, nil, nil, nil,
		nil, nil, [][]string{{"A", "B"}, {"C", "D"}}, 0, nil,
	)
	provided.status.Cycles.Reason = "truncated to 2 of 3 cycle representatives"

	got := an.GenerateAdvancedInsightsFromStats(provided, DefaultAdvancedInsightsConfig()).CycleBreak
	if !got.Status.Capped {
		t.Fatalf("truncated cycle input did not mark feature capped: %+v", got.Status)
	}
	if got.Status.Reason != provided.status.Cycles.Reason {
		t.Fatalf("cycle truncation reason = %q, want %q", got.Status.Reason, provided.status.Cycles.Reason)
	}
	if !strings.Contains(got.Advisory, "only stored cycles") {
		t.Fatalf("advisory omitted stored-cycle limitation: %q", got.Advisory)
	}
}

func TestGenerateAdvancedInsightsFromStatsDoesNotTreatUnavailableCyclesAsDAG(t *testing.T) {
	an := NewAnalyzer([]model.Issue{{ID: "A", Status: model.StatusOpen}})
	cfg := DefaultAdvancedInsightsConfig()

	tests := []struct {
		name          string
		metricStatus  statusEntry
		featureStatus string
	}{
		{
			name:          "skipped",
			metricStatus:  statusEntry{State: "skipped", Reason: "disabled for this analysis"},
			featureStatus: "skipped",
		},
		{
			name:          "timeout",
			metricStatus:  statusEntry{State: "timeout", Reason: "deadline"},
			featureStatus: "error",
		},
		{
			name:          "canceled before publication",
			metricStatus:  statusEntry{State: "pending"},
			featureStatus: "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provided := NewGraphStatsForTest(
				nil, nil, nil, nil, nil, nil,
				nil, nil, nil, 0, nil,
			)
			provided.status.Cycles = tt.metricStatus

			got := an.GenerateAdvancedInsightsFromStats(provided, cfg).CycleBreak
			if got.Status.State != tt.featureStatus {
				t.Fatalf("feature state: got %q, want %q", got.Status.State, tt.featureStatus)
			}
			if got.Status.Reason == "" {
				t.Fatal("expected unavailable cycle analysis to include a reason")
			}
			if strings.Contains(got.Advisory, "proper DAG") {
				t.Fatalf("unavailable cycle analysis made an acyclic claim: %q", got.Advisory)
			}
			if got.CycleCount != 0 || len(got.Suggestions) != 0 {
				t.Fatalf("unavailable cycle analysis returned findings: %+v", got)
			}
		})
	}
}

func TestGenerateAdvancedInsightsCycleBreakNegativeLimitUsesDefault(t *testing.T) {
	an := NewAnalyzer([]model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	})
	provided := NewGraphStatsForTest(
		nil, nil, nil, nil, nil, nil,
		nil, nil, [][]string{{"A", "B", "C"}}, 0, nil,
	)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.CycleBreakLimit = -1

	insights := an.GenerateAdvancedInsightsFromStats(provided, cfg)
	got := insights.CycleBreak
	if insights.Config.CycleBreakLimit != DefaultAdvancedInsightsConfig().CycleBreakLimit {
		t.Fatalf("reported cycle-break limit = %d, want effective default", insights.Config.CycleBreakLimit)
	}
	if got.Status.State != "available" {
		t.Fatalf("feature state: got %q, want available", got.Status.State)
	}
	if len(got.Suggestions) != 3 {
		t.Fatalf("suggestions: got %d, want all 3 cycle edges under default limit", len(got.Suggestions))
	}
	if got.Status.Capped {
		t.Fatalf("negative limit should normalize to the default, got capped status %+v", got.Status)
	}
}

func TestGenerateAdvancedInsightsFromStatsPreservesFullOutputAndNilFallback(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Alpha", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "Beta", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Title: "Gamma", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Title: "Delta", Status: model.StatusClosed},
	}
	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	stats := an.Analyze()

	want := an.GenerateAdvancedInsights(cfg)
	fromStats := an.GenerateAdvancedInsightsFromStats(&stats, cfg)
	fromNil := an.GenerateAdvancedInsightsFromStats(nil, cfg)

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal ordinary insights: %v", err)
	}
	for name, got := range map[string]*AdvancedInsights{
		"provided stats": fromStats,
		"nil fallback":   fromNil,
	} {
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal %s insights: %v", name, err)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s changed full output or deterministic ordering\nwant: %s\n got: %s", name, wantJSON, gotJSON)
		}
	}
}

func TestCycleBreakSuggestionsCapping(t *testing.T) {
	// Create multiple cycles by having a hub with many back-edges
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		// Create back-edges to form cycles
		{ID: "Hub2", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
	}
	// Add edge from Hub to Hub2 to complete cycles
	issues[0].Dependencies = []*model.Dependency{{DependsOnID: "Hub2", Type: model.DepBlocks}}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.CycleBreakLimit = 2 // Low cap for testing
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.CycleBreak == nil {
		t.Fatal("expected CycleBreak result")
	}
	if len(insights.CycleBreak.Suggestions) > 2 {
		t.Errorf("expected at most 2 suggestions (capped), got %d", len(insights.CycleBreak.Suggestions))
	}
}

func TestCycleBreakDeterministic(t *testing.T) {
	// Run multiple times and verify deterministic output
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}

	cfg := DefaultAdvancedInsightsConfig()
	var firstResult *CycleBreakResult

	for i := 0; i < 5; i++ {
		an := NewAnalyzer(issues)
		insights := an.GenerateAdvancedInsights(cfg)

		if firstResult == nil {
			firstResult = insights.CycleBreak
			continue
		}

		// Compare with first result
		if len(insights.CycleBreak.Suggestions) != len(firstResult.Suggestions) {
			t.Fatalf("iteration %d: suggestion count changed", i)
		}
		for j, s := range insights.CycleBreak.Suggestions {
			if s.EdgeFrom != firstResult.Suggestions[j].EdgeFrom || s.EdgeTo != firstResult.Suggestions[j].EdgeTo {
				t.Errorf("iteration %d: suggestion %d order changed", i, j)
			}
		}
	}
}

func TestPendingFeatureStatus(t *testing.T) {
	issues := []model.Issue{{ID: "A", Status: model.StatusOpen}}
	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Features that are still pending (awaiting implementation)
	pendingFeatures := []struct {
		name   string
		status FeatureStatus
	}{
		{"ParallelGain", insights.ParallelGain.Status},
	}

	for _, f := range pendingFeatures {
		if f.status.State != "pending" {
			t.Errorf("%s: expected pending state, got %s", f.name, f.status.State)
		}
		if f.status.Reason == "" {
			t.Errorf("%s: expected reason for pending state", f.name)
		}
	}

	// CycleBreak should be available
	if insights.CycleBreak.Status.State != "available" {
		t.Errorf("CycleBreak: expected available state, got %s", insights.CycleBreak.Status.State)
	}

	// TopKSet should be available (bv-145)
	if insights.TopKSet.Status.State != "available" {
		t.Errorf("TopKSet: expected available state, got %s", insights.TopKSet.Status.State)
	}

	// CoverageSet should be available (bv-152)
	if insights.CoverageSet.Status.State != "available" {
		t.Errorf("CoverageSet: expected available state, got %s", insights.CoverageSet.Status.State)
	}

	// KPaths should be available (bv-153)
	if insights.KPaths.Status.State != "available" {
		t.Errorf("KPaths: expected available state, got %s", insights.KPaths.Status.State)
	}

	// ParallelCut should be available (bv-154)
	if insights.ParallelCut.Status.State != "available" {
		t.Errorf("ParallelCut: expected available state, got %s", insights.ParallelCut.Status.State)
	}
}

func TestTopKSetEmpty(t *testing.T) {
	an := NewAnalyzer([]model.Issue{})
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.TopKSet == nil {
		t.Fatal("expected TopKSet result")
	}
	if insights.TopKSet.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.TopKSet.Status.State)
	}
	if len(insights.TopKSet.Items) != 0 {
		t.Error("expected no items for empty graph")
	}
	if insights.TopKSet.TotalGain != 0 {
		t.Errorf("expected 0 total gain, got %d", insights.TopKSet.TotalGain)
	}
}

func TestTopKSetLinearChain(t *testing.T) {
	// A -> B -> C -> D: completing A unblocks B, completing B unblocks C, etc.
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.TopKSetLimit = 2
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.TopKSet == nil {
		t.Fatal("expected TopKSet result")
	}
	if insights.TopKSet.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.TopKSet.Status.State)
	}
	// First pick should be A (unblocks B)
	if len(insights.TopKSet.Items) < 1 {
		t.Fatal("expected at least 1 item")
	}
	if insights.TopKSet.Items[0].ID != "A" {
		t.Errorf("first pick should be A, got %s", insights.TopKSet.Items[0].ID)
	}
	if insights.TopKSet.Items[0].MarginalGain != 1 {
		t.Errorf("A should unblock 1 (B), got %d", insights.TopKSet.Items[0].MarginalGain)
	}
	// Second pick should be B (unblocks C)
	if len(insights.TopKSet.Items) < 2 {
		t.Fatal("expected 2 items")
	}
	if insights.TopKSet.Items[1].ID != "B" {
		t.Errorf("second pick should be B, got %s", insights.TopKSet.Items[1].ID)
	}
}

func TestTopKSetNeverSelectsBlockedHighGainBeforePrerequisite(t *testing.T) {
	// B has the largest raw fanout, but it cannot be worked until A is done.
	// The returned sequence must therefore be A, then B.
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "X", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "Z", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.TopKSetLimit = 2
	got := an.GenerateAdvancedInsights(cfg).TopKSet

	if len(got.Items) != 2 {
		t.Fatalf("expected 2 feasible picks, got %#v", got.Items)
	}
	if got.Items[0].ID != "A" || got.Items[1].ID != "B" {
		t.Fatalf("pick order = [%s %s], want [A B]", got.Items[0].ID, got.Items[1].ID)
	}
	if got.Items[0].MarginalGain != 1 || got.Items[1].MarginalGain != 3 {
		t.Fatalf("marginal gains = [%d %d], want [1 3]", got.Items[0].MarginalGain, got.Items[1].MarginalGain)
	}
}

func TestTopKSetExcludesFutureDeferredCandidatesAndUnlocks(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "DEFERRED", Status: model.StatusOpen, DeferUntil: &future},
		{ID: "READY", Status: model.StatusOpen},
		{ID: "X", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "DEFERRED", Type: model.DepBlocks}}},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "DEFERRED", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	an.SetNow(now)
	got := an.GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).TopKSet

	if len(got.Items) != 1 || got.Items[0].ID != "READY" {
		t.Fatalf("future-deferred work leaked into feasible sequence: %#v", got.Items)
	}
	if got.Items[0].MarginalGain != 0 {
		t.Fatalf("READY marginal gain = %d, want 0", got.Items[0].MarginalGain)
	}
}

func TestTopKSetDeterministic(t *testing.T) {
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	cfg := DefaultAdvancedInsightsConfig()
	var firstResult *TopKSetResult

	for i := 0; i < 5; i++ {
		an := NewAnalyzer(issues)
		insights := an.GenerateAdvancedInsights(cfg)

		if firstResult == nil {
			firstResult = insights.TopKSet
			continue
		}

		// Compare with first result
		if len(insights.TopKSet.Items) != len(firstResult.Items) {
			t.Fatalf("iteration %d: item count changed", i)
		}
		for j, item := range insights.TopKSet.Items {
			if item.ID != firstResult.Items[j].ID {
				t.Errorf("iteration %d: item %d ID changed from %s to %s", i, j, firstResult.Items[j].ID, item.ID)
			}
		}
	}
}

func TestTopKSetCapping(t *testing.T) {
	// Create more items than the cap
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "E", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "F", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.TopKSetLimit = 3
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.TopKSet.Items) > 3 {
		t.Errorf("expected at most 3 items (capped), got %d", len(insights.TopKSet.Items))
	}
	if !insights.TopKSet.Status.Capped {
		t.Error("expected Capped=true when results exceed limit")
	}
}

func TestTopKSetDoesNotClaimCapForCycleStrandedRemainder(t *testing.T) {
	issues := []model.Issue{
		{ID: "READY", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	cfg := DefaultAdvancedInsightsConfig()
	cfg.TopKSetLimit = 1

	got := NewAnalyzer(issues).GenerateAdvancedInsights(cfg).TopKSet
	if len(got.Items) != 1 || got.Items[0].ID != "READY" {
		t.Fatalf("feasible sequence = %#v, want only READY", got.Items)
	}
	if got.Status.Capped {
		t.Fatalf("cycle-stranded issues caused a false capped claim: %+v", got.Status)
	}
}

// Coverage Set Tests (bv-152)

func TestCoverageSetEmpty(t *testing.T) {
	an := NewAnalyzer([]model.Issue{})
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.CoverageSet == nil {
		t.Fatal("expected CoverageSet result")
	}
	if insights.CoverageSet.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.CoverageSet.Status.State)
	}
	if len(insights.CoverageSet.Items) != 0 {
		t.Error("expected no items for empty graph")
	}
	if insights.CoverageSet.TotalEdges != 0 {
		t.Errorf("expected 0 total edges, got %d", insights.CoverageSet.TotalEdges)
	}
	// Empty coverage is vacuously 100%
	if insights.CoverageSet.CoverageRatio != 1.0 {
		t.Errorf("expected coverage ratio 1.0 for empty graph, got %f", insights.CoverageSet.CoverageRatio)
	}
}

func TestCoverageSetNoEdges(t *testing.T) {
	// Disconnected nodes with no dependencies
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.CoverageSet.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.CoverageSet.Status.State)
	}
	if len(insights.CoverageSet.Items) != 0 {
		t.Error("expected no items for graph with no edges")
	}
	if insights.CoverageSet.TotalEdges != 0 {
		t.Errorf("expected 0 total edges, got %d", insights.CoverageSet.TotalEdges)
	}
}

func TestCoverageSetLinearChain(t *testing.T) {
	// A -> B -> C -> D: edges A-B, B-C, C-D
	// Greedy vertex cover should pick B or C first (highest degree)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.CoverageSet == nil {
		t.Fatal("expected CoverageSet result")
	}
	if insights.CoverageSet.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.CoverageSet.Status.State)
	}
	if insights.CoverageSet.TotalEdges != 3 {
		t.Errorf("expected 3 edges, got %d", insights.CoverageSet.TotalEdges)
	}
	// With 3 edges in a chain, greedy should pick 2 nodes (B and C)
	// and cover all edges
	if insights.CoverageSet.EdgesCovered != 3 {
		t.Errorf("expected all 3 edges covered, got %d", insights.CoverageSet.EdgesCovered)
	}
	if insights.CoverageSet.CoverageRatio != 1.0 {
		t.Errorf("expected 100%% coverage, got %f", insights.CoverageSet.CoverageRatio)
	}
	if len(insights.CoverageSet.Items) != 2 {
		t.Fatalf("expected two selected nodes, got %#v", insights.CoverageSet.Items)
	}
	if second := insights.CoverageSet.Items[1]; second.EdgesAdded != 1 || second.TotalDegree != 2 {
		t.Fatalf("second selection should add 1 uncovered edge but retain original degree 2, got %+v", second)
	}
}

func TestCoverageSetCountsDuplicateDependencyRecordsOnce(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
			{DependsOnID: "A", Type: model.DepBlocks},
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	got := NewAnalyzer(issues).GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).CoverageSet
	if got.TotalEdges != 1 || got.EdgesCovered != 1 {
		t.Fatalf("duplicate dependency records changed semantic edge counts: %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].EdgesAdded != 1 || got.Items[0].TotalDegree != 1 {
		t.Fatalf("duplicate dependency records changed coverage selection: %+v", got.Items)
	}
}

func TestCoverageSetHubPattern(t *testing.T) {
	// Hub -> A, B, C, D: hub has degree 4
	// Selecting hub alone covers all 4 edges
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.CoverageSet == nil {
		t.Fatal("expected CoverageSet result")
	}
	// Hub should be picked first and cover all edges
	if len(insights.CoverageSet.Items) < 1 {
		t.Fatal("expected at least 1 item")
	}
	if insights.CoverageSet.Items[0].ID != "Hub" {
		t.Errorf("first pick should be Hub (highest degree), got %s", insights.CoverageSet.Items[0].ID)
	}
	if insights.CoverageSet.Items[0].EdgesAdded != 4 {
		t.Errorf("Hub should cover 4 edges, got %d", insights.CoverageSet.Items[0].EdgesAdded)
	}
	// All edges should be covered by just the hub
	if insights.CoverageSet.EdgesCovered != 4 {
		t.Errorf("expected 4 edges covered, got %d", insights.CoverageSet.EdgesCovered)
	}
}

func TestCoverageSetDeterministic(t *testing.T) {
	// Run multiple times and verify deterministic output
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	cfg := DefaultAdvancedInsightsConfig()
	var firstResult *CoverageSetResult

	for i := 0; i < 5; i++ {
		an := NewAnalyzer(issues)
		insights := an.GenerateAdvancedInsights(cfg)

		if firstResult == nil {
			firstResult = insights.CoverageSet
			continue
		}

		// Compare with first result
		if len(insights.CoverageSet.Items) != len(firstResult.Items) {
			t.Fatalf("iteration %d: item count changed", i)
		}
		for j, item := range insights.CoverageSet.Items {
			if item.ID != firstResult.Items[j].ID {
				t.Errorf("iteration %d: item %d ID changed from %s to %s", i, j, firstResult.Items[j].ID, item.ID)
			}
		}
	}
}

func TestCoverageSetCapping(t *testing.T) {
	// Create graph with many edges that would need more than 2 nodes to cover
	// Triangle: A-B, B-C, A-C (all nodes have degree 2)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.CoverageSetLimit = 1 // Cap at 1
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.CoverageSet.Items) > 1 {
		t.Errorf("expected at most 1 item (capped), got %d", len(insights.CoverageSet.Items))
	}
	// Should be capped since 1 node can't cover all 3 edges in a triangle
	if !insights.CoverageSet.Status.Capped {
		t.Error("expected Capped=true when not all edges covered")
	}
}

func TestCoverageSetClosedIssuesIgnored(t *testing.T) {
	// Closed issues should not be considered
	issues := []model.Issue{
		{ID: "A", Status: model.StatusClosed},    // Closed - should be ignored
		{ID: "T", Status: model.StatusTombstone}, // Tombstone - should be ignored
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Edge B->A should be ignored since A is closed
	// C has no deps, so no edges exist
	if insights.CoverageSet.TotalEdges != 0 {
		t.Errorf("expected 0 edges (closed issue ignored), got %d", insights.CoverageSet.TotalEdges)
	}
}

func TestCoverageSetSelectionSequence(t *testing.T) {
	// Verify selection sequence is assigned correctly
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	for i, item := range insights.CoverageSet.Items {
		expectedSeq := i + 1
		if item.SelectionSeq != expectedSeq {
			t.Errorf("item %d: expected SelectionSeq=%d, got %d", i, expectedSeq, item.SelectionSeq)
		}
	}
}

// K-Paths Tests (bv-153)

func TestKPathsEmpty(t *testing.T) {
	an := NewAnalyzer([]model.Issue{})
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.KPaths == nil {
		t.Fatal("expected KPaths result")
	}
	if insights.KPaths.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.KPaths.Status.State)
	}
	if len(insights.KPaths.Paths) != 0 {
		t.Error("expected no paths for empty graph")
	}
}

func TestKPathsNoEdges(t *testing.T) {
	// Disconnected nodes with no dependencies
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.KPaths.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.KPaths.Status.State)
	}
	// No edges means no non-trivial paths
	if len(insights.KPaths.Paths) != 0 {
		t.Errorf("expected no paths for graph with no edges, got %d", len(insights.KPaths.Paths))
	}
}

func TestKPathsDoesNotPublishPartialResultForCyclicGraph(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "X", Status: model.StatusOpen},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "X", Type: model.DepBlocks}}},
	}

	got := NewAnalyzer(issues).GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).KPaths
	if got.Status.State != "skipped" {
		t.Fatalf("feature state: got %q, want skipped", got.Status.State)
	}
	if got.Status.Reason == "" {
		t.Fatal("expected cycle reason")
	}
	if len(got.Paths) != 0 {
		t.Fatalf("cyclic graph published partial acyclic-fragment paths: %#v", got.Paths)
	}
}

func TestKPathsLinearChain(t *testing.T) {
	// A -> B -> C -> D: longest path is A-B-C-D (length 4)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.KPaths == nil {
		t.Fatal("expected KPaths result")
	}
	if insights.KPaths.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.KPaths.Status.State)
	}
	if len(insights.KPaths.Paths) < 1 {
		t.Fatal("expected at least 1 path")
	}

	// First path should be the longest: A -> B -> C -> D
	path := insights.KPaths.Paths[0]
	if path.Rank != 1 {
		t.Errorf("expected rank 1, got %d", path.Rank)
	}
	if path.Length != 4 {
		t.Errorf("expected length 4, got %d", path.Length)
	}
	// Check path order: should start from source (A) and end at sink (D)
	if len(path.IssueIDs) != 4 {
		t.Fatalf("expected 4 issue IDs, got %d", len(path.IssueIDs))
	}
	if path.IssueIDs[0] != "A" {
		t.Errorf("expected path to start with A, got %s", path.IssueIDs[0])
	}
	if path.IssueIDs[3] != "D" {
		t.Errorf("expected path to end with D, got %s", path.IssueIDs[3])
	}
}

func TestKPathsStatusCountsRepresentativeSources(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}
	cfg := DefaultAdvancedInsightsConfig()
	cfg.KPathsLimit = 1

	got := NewAnalyzer(issues).GenerateAdvancedInsights(cfg).KPaths
	if got.Status.Limited != 1 {
		t.Fatalf("eligible representative path count = %d, want 1", got.Status.Limited)
	}
	if got.Status.Capped {
		t.Fatalf("single-source chain reported duplicate-source prefixes as capped: %+v", got.Status)
	}
}

func TestKPathsMultiplePaths(t *testing.T) {
	// Create two separate chains:
	// Chain 1: A -> B -> C (length 3)
	// Chain 2: X -> Y (length 2)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "X", Status: model.StatusOpen},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "X", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.KPathsLimit = 5
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.KPaths.Paths) < 2 {
		t.Fatalf("expected at least 2 paths, got %d", len(insights.KPaths.Paths))
	}

	// First path should be longer (length 3)
	if insights.KPaths.Paths[0].Length != 3 {
		t.Errorf("first path should have length 3, got %d", insights.KPaths.Paths[0].Length)
	}
	// Second path should be shorter (length 2)
	if insights.KPaths.Paths[1].Length != 2 {
		t.Errorf("second path should have length 2, got %d", insights.KPaths.Paths[1].Length)
	}
}

func TestKPathsDeterministic(t *testing.T) {
	// Run multiple times and verify deterministic output
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "X", Status: model.StatusOpen},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "X", Type: model.DepBlocks}}},
	}

	cfg := DefaultAdvancedInsightsConfig()
	var firstResult *KPathsResult

	for i := 0; i < 5; i++ {
		an := NewAnalyzer(issues)
		insights := an.GenerateAdvancedInsights(cfg)

		if firstResult == nil {
			firstResult = insights.KPaths
			continue
		}

		// Compare with first result
		if len(insights.KPaths.Paths) != len(firstResult.Paths) {
			t.Fatalf("iteration %d: path count changed from %d to %d", i, len(firstResult.Paths), len(insights.KPaths.Paths))
		}
		for j, path := range insights.KPaths.Paths {
			if path.Length != firstResult.Paths[j].Length {
				t.Errorf("iteration %d: path %d length changed", i, j)
			}
			if len(path.IssueIDs) != len(firstResult.Paths[j].IssueIDs) {
				t.Errorf("iteration %d: path %d issue count changed", i, j)
			}
			for k, id := range path.IssueIDs {
				if id != firstResult.Paths[j].IssueIDs[k] {
					t.Errorf("iteration %d: path %d issue %d changed from %s to %s",
						i, j, k, firstResult.Paths[j].IssueIDs[k], id)
				}
			}
		}
	}
}

func TestKPathsCapping(t *testing.T) {
	// Create multiple chains to test capping
	issues := []model.Issue{
		{ID: "A1", Status: model.StatusOpen},
		{ID: "A2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A1", Type: model.DepBlocks}}},
		{ID: "B1", Status: model.StatusOpen},
		{ID: "B2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B1", Type: model.DepBlocks}}},
		{ID: "C1", Status: model.StatusOpen},
		{ID: "C2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C1", Type: model.DepBlocks}}},
		{ID: "D1", Status: model.StatusOpen},
		{ID: "D2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "D1", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.KPathsLimit = 2 // Cap at 2
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.KPaths.Paths) > 2 {
		t.Errorf("expected at most 2 paths (capped), got %d", len(insights.KPaths.Paths))
	}
	if !insights.KPaths.Status.Capped {
		t.Error("expected Capped=true when results exceed limit")
	}
}

func TestKPathsClosedIssuesIgnored(t *testing.T) {
	// Closed issues should not be considered
	issues := []model.Issue{
		{ID: "A", Status: model.StatusClosed}, // Closed - should be ignored
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Since A is closed, B has no valid blocker, so the only path would be B->C
	// But B->C is only length 2, and the path would be [B, C]
	if len(insights.KPaths.Paths) > 0 {
		path := insights.KPaths.Paths[0]
		for _, id := range path.IssueIDs {
			if id == "A" {
				t.Error("closed issue A should not appear in paths")
			}
		}
	}
}

func TestKPathsPathLengthCap(t *testing.T) {
	// Create a long chain and verify path length capping
	issues := make([]model.Issue, 10)
	issues[0] = model.Issue{ID: "N0", Status: model.StatusOpen}
	for i := 1; i < 10; i++ {
		issues[i] = model.Issue{
			ID:     "N" + string(rune('0'+i)),
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "N" + string(rune('0'+i-1)), Type: model.DepBlocks},
			},
		}
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.PathLengthCap = 5 // Cap path length at 5
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.KPaths.Paths) < 1 {
		t.Fatal("expected at least 1 path")
	}

	path := insights.KPaths.Paths[0]
	if path.Length > 5 {
		t.Errorf("expected path length <= 5 (capped), got %d", path.Length)
	}
	if !path.Truncated {
		t.Error("expected Truncated=true for capped path")
	}
	if !insights.KPaths.Status.Capped {
		t.Error("expected feature status capped=true when a returned path is truncated")
	}
}

func TestKPathsRankOrdering(t *testing.T) {
	// Verify ranks are assigned sequentially
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "X", Status: model.StatusOpen},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "X", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	for i, path := range insights.KPaths.Paths {
		expectedRank := i + 1
		if path.Rank != expectedRank {
			t.Errorf("path %d: expected Rank=%d, got %d", i, expectedRank, path.Rank)
		}
	}
}

func TestKPathsDiamondGraph(t *testing.T) {
	// Diamond: A -> B, A -> C, B -> D, C -> D
	// Two paths of equal length: A-B-D and A-C-D
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.KPaths.Paths) < 1 {
		t.Fatal("expected at least 1 path")
	}

	// The longest path should have length 3 (A -> B/C -> D)
	path := insights.KPaths.Paths[0]
	if path.Length != 3 {
		t.Errorf("expected path length 3, got %d", path.Length)
	}
	// Path should start with A and end with D
	if path.IssueIDs[0] != "A" {
		t.Errorf("expected path to start with A, got %s", path.IssueIDs[0])
	}
	if path.IssueIDs[path.Length-1] != "D" {
		t.Errorf("expected path to end with D, got %s", path.IssueIDs[path.Length-1])
	}
}

// Parallel Cut Tests (bv-154)

func TestParallelCutEmpty(t *testing.T) {
	an := NewAnalyzer([]model.Issue{})
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.ParallelCut == nil {
		t.Fatal("expected ParallelCut result")
	}
	if insights.ParallelCut.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.ParallelCut.Status.State)
	}
	if len(insights.ParallelCut.Suggestions) != 0 {
		t.Error("expected no suggestions for empty graph")
	}
}

func TestParallelCutNoEdges(t *testing.T) {
	// Disconnected nodes with no dependencies
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.ParallelCut.Status.State != "available" {
		t.Errorf("expected available state, got %s", insights.ParallelCut.Status.State)
	}
	// No edges means no parallel gain opportunities
	if len(insights.ParallelCut.Suggestions) != 0 {
		t.Errorf("expected no suggestions for graph with no edges, got %d", len(insights.ParallelCut.Suggestions))
	}
}

func TestParallelCutLinearChainNoGain(t *testing.T) {
	// Chain: A -> B -> C -> D
	// Completing any node only unblocks 1 dependent (gain = 0)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// No node has gain > 0 in a simple chain (each completion only unblocks 1)
	if len(insights.ParallelCut.Suggestions) != 0 {
		t.Errorf("expected no suggestions for linear chain, got %d", len(insights.ParallelCut.Suggestions))
	}
}

func TestParallelCutForkHasGain(t *testing.T) {
	// Fork: Hub -> A, Hub -> B, Hub -> C
	// Completing Hub unblocks 3 nodes, gain = 3 - 1 = 2
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if insights.ParallelCut == nil {
		t.Fatal("expected ParallelCut result")
	}
	if len(insights.ParallelCut.Suggestions) < 1 {
		t.Fatal("expected at least 1 suggestion")
	}

	// Hub should be the suggestion with gain = 2 (3 unblocked - 1 = 2)
	suggestion := insights.ParallelCut.Suggestions[0]
	if suggestion.ID != "Hub" {
		t.Errorf("expected Hub as suggestion, got %s", suggestion.ID)
	}
	if suggestion.ParallelGain != 2 {
		t.Errorf("expected parallel gain 2, got %d", suggestion.ParallelGain)
	}
	if len(suggestion.EnabledTracks) != 3 {
		t.Errorf("expected 3 enabled tracks, got %d", len(suggestion.EnabledTracks))
	}
}

func TestParallelCutMultipleForks(t *testing.T) {
	// Two forks: Hub1 -> {A, B}, Hub2 -> {X, Y, Z}
	// Hub1 has gain = 2 - 1 = 1
	// Hub2 has gain = 3 - 1 = 2
	issues := []model.Issue{
		{ID: "Hub1", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub1", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub1", Type: model.DepBlocks}}},
		{ID: "Hub2", Status: model.StatusOpen},
		{ID: "X", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub2", Type: model.DepBlocks}}},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub2", Type: model.DepBlocks}}},
		{ID: "Z", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub2", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.ParallelCut.Suggestions) < 2 {
		t.Fatalf("expected at least 2 suggestions, got %d", len(insights.ParallelCut.Suggestions))
	}

	// Hub2 should be first (higher gain)
	if insights.ParallelCut.Suggestions[0].ID != "Hub2" {
		t.Errorf("expected Hub2 first (higher gain), got %s", insights.ParallelCut.Suggestions[0].ID)
	}
	if insights.ParallelCut.Suggestions[0].ParallelGain != 2 {
		t.Errorf("expected Hub2 gain 2, got %d", insights.ParallelCut.Suggestions[0].ParallelGain)
	}

	// Hub1 should be second
	if insights.ParallelCut.Suggestions[1].ID != "Hub1" {
		t.Errorf("expected Hub1 second, got %s", insights.ParallelCut.Suggestions[1].ID)
	}
	if insights.ParallelCut.Suggestions[1].ParallelGain != 1 {
		t.Errorf("expected Hub1 gain 1, got %d", insights.ParallelCut.Suggestions[1].ParallelGain)
	}
}

func TestParallelCutDeterministic(t *testing.T) {
	// Run multiple times and verify deterministic output
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	cfg := DefaultAdvancedInsightsConfig()
	var firstResult *ParallelCutResult

	for i := 0; i < 5; i++ {
		an := NewAnalyzer(issues)
		insights := an.GenerateAdvancedInsights(cfg)

		if firstResult == nil {
			firstResult = insights.ParallelCut
			continue
		}

		// Compare with first result
		if len(insights.ParallelCut.Suggestions) != len(firstResult.Suggestions) {
			t.Fatalf("iteration %d: suggestion count changed from %d to %d",
				i, len(firstResult.Suggestions), len(insights.ParallelCut.Suggestions))
		}
		for j, s := range insights.ParallelCut.Suggestions {
			if s.ID != firstResult.Suggestions[j].ID {
				t.Errorf("iteration %d: suggestion %d ID changed from %s to %s",
					i, j, firstResult.Suggestions[j].ID, s.ID)
			}
			if s.ParallelGain != firstResult.Suggestions[j].ParallelGain {
				t.Errorf("iteration %d: suggestion %d gain changed", i, j)
			}
		}
	}
}

func TestParallelCutCapping(t *testing.T) {
	// Create many forks to test capping
	issues := []model.Issue{
		{ID: "H1", Status: model.StatusOpen},
		{ID: "A1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H1", Type: model.DepBlocks}}},
		{ID: "A2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H1", Type: model.DepBlocks}}},
		{ID: "H2", Status: model.StatusOpen},
		{ID: "B1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H2", Type: model.DepBlocks}}},
		{ID: "B2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H2", Type: model.DepBlocks}}},
		{ID: "H3", Status: model.StatusOpen},
		{ID: "C1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H3", Type: model.DepBlocks}}},
		{ID: "C2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H3", Type: model.DepBlocks}}},
		{ID: "H4", Status: model.StatusOpen},
		{ID: "D1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H4", Type: model.DepBlocks}}},
		{ID: "D2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "H4", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	cfg.ParallelCutLimit = 2 // Cap at 2
	insights := an.GenerateAdvancedInsights(cfg)

	if len(insights.ParallelCut.Suggestions) > 2 {
		t.Errorf("expected at most 2 suggestions (capped), got %d", len(insights.ParallelCut.Suggestions))
	}
}

func TestParallelCutClosedIssuesIgnored(t *testing.T) {
	// Closed issues should not be considered
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusClosed}, // Closed - should be ignored
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Hub is closed, so no suggestions involving it
	for _, s := range insights.ParallelCut.Suggestions {
		if s.ID == "Hub" {
			t.Error("closed issue Hub should not appear in suggestions")
		}
	}
}

func TestParallelCutExcludesFutureDeferredCandidateAndUnlocks(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "DEFERRED", Status: model.StatusOpen, DeferUntil: &future},
		{ID: "READY", Status: model.StatusOpen},
		{ID: "X", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "DEFERRED", Type: model.DepBlocks}}},
		{ID: "Y", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "DEFERRED", Type: model.DepBlocks}}},
		{ID: "Z", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "DEFERRED", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	an.SetNow(now)
	got := an.GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).ParallelCut

	if len(got.Suggestions) != 0 {
		t.Fatalf("future-deferred candidate leaked into parallel cut: %#v", got.Suggestions)
	}
	if got.MaxParallel != 1 {
		t.Fatalf("max_parallel = %d, want current actionable width 1", got.MaxParallel)
	}
}

func TestParallelCutAccountsForParentBlockedPropagation(t *testing.T) {
	// CHILD has no direct blocker, but its parent P is blocked by ROOT. Completing
	// ROOT makes both P and CHILD actionable, increasing ready width from 1 to 2.
	issues := []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen},
		{ID: "P", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "ROOT", Type: model.DepBlocks}}},
		{ID: "CHILD", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "P", Type: model.DepParentChild}}},
	}

	an := NewAnalyzer(issues)
	got := an.GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).ParallelCut

	if len(got.Suggestions) != 1 {
		t.Fatalf("suggestions = %#v, want ROOT", got.Suggestions)
	}
	suggestion := got.Suggestions[0]
	if suggestion.ID != "ROOT" || suggestion.ParallelGain != 1 {
		t.Fatalf("suggestion = %#v, want ROOT with gain 1", suggestion)
	}
	if strings.Join(suggestion.EnabledTracks, ",") != "CHILD,P" {
		t.Fatalf("enabled tracks = %v, want [CHILD P]", suggestion.EnabledTracks)
	}
	if got.MaxParallel != 2 {
		t.Fatalf("max_parallel = %d, want 2", got.MaxParallel)
	}
}

func TestParallelCutMaxParallel(t *testing.T) {
	// Fork: Hub -> A, B, C
	// Initial actionable: 1 (Hub)
	// After completing Hub: 3 actionable (A, B, C)
	// The projected ready width should therefore be 3.
	issues := []model.Issue{
		{ID: "Hub", Status: model.StatusOpen},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "Hub", Type: model.DepBlocks}}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// Completing the returned cut {Hub} leaves A, B, and C ready.
	if insights.ParallelCut.MaxParallel != 3 {
		t.Errorf("expected MaxParallel 3, got %d", insights.ParallelCut.MaxParallel)
	}
}

func TestParallelCutMaxParallelIncludesJointUnlocks(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "A1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "A2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "B1", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "JOINT", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	got := NewAnalyzer(issues).GenerateAdvancedInsights(DefaultAdvancedInsightsConfig()).ParallelCut
	if len(got.Suggestions) != 2 {
		t.Fatalf("suggestions = %#v, want A and B", got.Suggestions)
	}
	if got.MaxParallel != 5 {
		t.Fatalf("max_parallel = %d, want five issues ready after completing A and B", got.MaxParallel)
	}
}

func TestParallelCutDiamondNoGain(t *testing.T) {
	// Diamond: A -> B, A -> C, B -> D, C -> D
	// Completing A unblocks B and C (2 items, gain = 1)
	// Completing B or C alone doesn't unblock D (needs both)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)
	cfg := DefaultAdvancedInsightsConfig()
	insights := an.GenerateAdvancedInsights(cfg)

	// A should have gain = 1 (unblocks B and C, 2 - 1 = 1)
	if len(insights.ParallelCut.Suggestions) < 1 {
		t.Fatal("expected at least 1 suggestion")
	}
	if insights.ParallelCut.Suggestions[0].ID != "A" {
		t.Errorf("expected A as suggestion, got %s", insights.ParallelCut.Suggestions[0].ID)
	}
	if insights.ParallelCut.Suggestions[0].ParallelGain != 1 {
		t.Errorf("expected gain 1, got %d", insights.ParallelCut.Suggestions[0].ParallelGain)
	}
}
