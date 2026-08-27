package analysis

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestGenerateTopReasons_Empty(t *testing.T) {
	score := ImpactScore{
		Breakdown: ScoreBreakdown{},
	}

	reasons := GenerateTopReasons(score)

	// Should return empty or only reasons with non-zero weight
	for _, r := range reasons {
		if r.Weight < 0.01 {
			t.Errorf("should not include negligible reason %s with weight %f", r.Factor, r.Weight)
		}
	}
}

func TestEstimateDaysSavedDoesNotOverflowIntAccumulator(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	issueMap := map[string]model.Issue{
		"A": {ID: "A", EstimatedMinutes: &maxInt},
		"B": {ID: "B", EstimatedMinutes: &maxInt},
	}

	got := estimateDaysSaved([]string{"A", "B"}, issueMap)
	if got <= 0 || math.IsInf(got, 0) || math.IsNaN(got) {
		t.Fatalf("estimateDaysSaved overflowed: %v", got)
	}

	analyzer := NewAnalyzer([]model.Issue{
		{ID: "A", EstimatedMinutes: &maxInt},
		{ID: "B", EstimatedMinutes: &maxInt},
	})
	if got := analyzer.computeMedianEstimatedMinutes(); got != maxInt {
		t.Fatalf("median estimate=%d, want %d", got, maxInt)
	}
}

func TestGenerateTopReasons_ThreeMax(t *testing.T) {
	score := ImpactScore{
		Breakdown: ScoreBreakdown{
			PageRank:          0.3,
			PageRankNorm:      0.8,
			Betweenness:       0.25,
			BetweennessNorm:   0.7,
			BlockerRatio:      0.2,
			BlockerRatioNorm:  0.5,
			Staleness:         0.15,
			StalenessNorm:     0.4,
			PriorityBoost:     0.1,
			PriorityBoostNorm: 0.3,
		},
	}

	reasons := GenerateTopReasons(score)

	if len(reasons) > 3 {
		t.Errorf("expected at most 3 reasons, got %d", len(reasons))
	}

	// Should be sorted by weight descending
	for i := 1; i < len(reasons); i++ {
		if reasons[i].Weight > reasons[i-1].Weight {
			t.Error("reasons should be sorted by weight descending")
		}
	}
}

func TestGenerateTopReasons_Emojis(t *testing.T) {
	score := ImpactScore{
		Breakdown: ScoreBreakdown{
			PageRank:     0.5,
			PageRankNorm: 0.8,
		},
	}

	reasons := GenerateTopReasons(score)

	if len(reasons) == 0 {
		t.Fatal("expected at least one reason")
	}

	if reasons[0].Emoji == "" {
		t.Error("expected emoji to be set")
	}
}

func TestGenerateTopReasons_VeryHighExplanation(t *testing.T) {
	score := ImpactScore{
		Breakdown: ScoreBreakdown{
			PageRank:     0.5,
			PageRankNorm: 0.9, // Very high (>0.7)
		},
	}

	reasons := GenerateTopReasons(score)

	if len(reasons) == 0 {
		t.Fatal("expected at least one reason")
	}

	if reasons[0].Explanation[:9] != "Very high" {
		t.Errorf("expected explanation to start with 'Very high', got %s", reasons[0].Explanation)
	}
}

func TestGenerateTopReasons_HighExplanation(t *testing.T) {
	score := ImpactScore{
		Breakdown: ScoreBreakdown{
			PageRank:     0.5,
			PageRankNorm: 0.5, // High (>0.4)
		},
	}

	reasons := GenerateTopReasons(score)

	if len(reasons) == 0 {
		t.Fatal("expected at least one reason")
	}

	if reasons[0].Explanation[:4] != "High" {
		t.Errorf("expected explanation to start with 'High', got %s", reasons[0].Explanation)
	}
}

func TestPriorityExplanation_Fields(t *testing.T) {
	exp := PriorityExplanation{
		TopReasons: []PriorityReason{
			{Factor: "pagerank", Weight: 0.5, Explanation: "test", Emoji: "🎯"},
		},
		WhatIf: &WhatIfDelta{
			DirectUnblocks:     3,
			TransitiveUnblocks: 5,
		},
		Status: ExplanationStatus{
			ComputedAt:    "2025-01-01T00:00:00Z",
			Deterministic: true,
			Phase2Ready:   true,
		},
	}

	if len(exp.TopReasons) != 1 {
		t.Error("expected one top reason")
	}

	if exp.WhatIf.DirectUnblocks != 3 {
		t.Error("expected direct unblocks to be 3")
	}

	if !exp.Status.Deterministic {
		t.Error("expected deterministic to be true")
	}
}

func TestDefaultFieldDescriptions(t *testing.T) {
	desc := DefaultFieldDescriptions()

	if desc == nil {
		t.Fatal("expected non-nil descriptions")
	}

	// Check for key descriptions
	requiredKeys := []string{
		"top_reasons",
		"what_if.unblocks",
		"what_if.cascade",
		"what_if.depth",
		"what_if.days_saved",
		"status.phase2",
		"status.capped",
	}

	for _, key := range requiredKeys {
		if _, ok := desc[key]; !ok {
			t.Errorf("missing field description for %s", key)
		}
	}
}

func TestExtractReasoningStrings(t *testing.T) {
	reasons := []PriorityReason{
		{Factor: "pagerank", Emoji: "🎯", Explanation: "Central in graph"},
		{Factor: "blockers", Emoji: "🚧", Explanation: "High blocker count"},
	}

	strings := extractReasoningStrings(reasons)

	if len(strings) != 2 {
		t.Fatalf("expected 2 strings, got %d", len(strings))
	}

	if strings[0] != "🎯 Central in graph" {
		t.Errorf("unexpected string: %s", strings[0])
	}
}

func TestEnhancedPriorityRecommendation(t *testing.T) {
	epr := EnhancedPriorityRecommendation{
		PriorityRecommendation: PriorityRecommendation{
			IssueID:           "TEST-1",
			Title:             "Test Issue",
			CurrentPriority:   3,
			SuggestedPriority: 1,
			ImpactScore:       0.8,
			Confidence:        0.9,
			Direction:         "up",
		},
		Explanation: PriorityExplanation{
			TopReasons: []PriorityReason{},
			Status: ExplanationStatus{
				Deterministic: true,
			},
		},
	}

	if epr.IssueID != "TEST-1" {
		t.Error("expected embedded PriorityRecommendation fields to be accessible")
	}

	if !epr.Explanation.Status.Deterministic {
		t.Error("expected explanation status to be accessible")
	}
}

func TestGenerateEnhancedRecommendations_Empty(t *testing.T) {
	analyzer := NewCachedAnalyzer([]model.Issue{}, nil)
	recs := analyzer.GenerateEnhancedRecommendations()

	if len(recs) > 0 {
		t.Error("expected empty recommendations for empty analyzer")
	}
}

func TestGenerateEnhancedRecommendations_WithIssues(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "BLOCKER-1",
			Title:     "Critical Blocker",
			Status:    model.StatusOpen,
			Priority:  3, // Low priority
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			UpdatedAt: now.Add(-1 * 24 * time.Hour),
		},
		{
			ID:        "DEPENDENT-1",
			Title:     "Dependent Issue",
			Status:    model.StatusBlocked,
			Priority:  1,
			CreatedAt: now.Add(-10 * 24 * time.Hour),
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "DEPENDENT-1", DependsOnID: "BLOCKER-1", Type: model.DepBlocks},
			},
		},
	}

	analyzer := NewCachedAnalyzer(issues, nil)
	recs := analyzer.GenerateEnhancedRecommendations()

	// Should have some recommendations (BLOCKER-1 has impact due to dependency)
	if len(recs) == 0 {
		t.Log("No recommendations generated - this may be expected based on thresholds")
		return
	}

	// Check that explanations are populated
	for _, rec := range recs {
		if rec.Explanation.Status.ComputedAt == "" {
			t.Error("expected computed_at to be set")
		}
	}
}

func TestGenerateEnhancedRecommendations_UsesAnalyzerClock(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)
	issues := []model.Issue{
		{ID: "blocker", Title: "Blocker", Status: model.StatusOpen, Priority: 4, UpdatedAt: pinned.Add(-30 * 24 * time.Hour)},
		{ID: "dependent", Title: "Dependent", Status: model.StatusOpen, Priority: 0, Dependencies: []*model.Dependency{{DependsOnID: "blocker", Type: model.DepBlocks}}},
	}
	analyzer := NewAnalyzer(issues)
	analyzer.SetNow(pinned)

	recommendations := analyzer.GenerateEnhancedRecommendations()
	if len(recommendations) == 0 {
		t.Fatal("expected at least one enhanced recommendation")
	}
	for _, recommendation := range recommendations {
		if got, want := recommendation.Explanation.Status.ComputedAt, pinned.Format(time.RFC3339); got != want {
			t.Fatalf("computed_at = %q, want %q", got, want)
		}
	}
}

func TestTopWhatIfDeltas_SkipsTombstone(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Removed blocker", Status: model.StatusTombstone},
		{
			ID:     "B",
			Title:  "Blocked work",
			Status: model.StatusBlocked,
			Dependencies: []*model.Dependency{
				{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
			},
		},
	}

	analyzer := NewAnalyzer(issues)
	results := analyzer.TopWhatIfDeltas(10)
	if len(results) != 0 {
		t.Fatalf("expected tombstone to be excluded, got %d results", len(results))
	}
}

func TestWhatIfDeltaExcludesDeferredAndParentGatedCascade(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen},
		{ID: "PARENT-BLOCKER", Status: model.StatusOpen},
		{ID: "PARENT", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "PARENT-BLOCKER", Type: model.DepBlocks},
		}},
		{ID: "DEFERRED", Status: model.StatusBlocked, DeferUntil: &future, Dependencies: []*model.Dependency{
			{DependsOnID: "ROOT", Type: model.DepBlocks},
		}},
		{ID: "PARENT-GATED", Status: model.StatusBlocked, Dependencies: []*model.Dependency{
			{DependsOnID: "ROOT", Type: model.DepBlocks},
			{DependsOnID: "PARENT", Type: model.DepParentChild},
		}},
	}

	analyzer := NewAnalyzer(issues)
	analyzer.SetNow(now)
	delta := analyzer.computeWhatIfDeltaFromStats("ROOT", &GraphStats{})
	if delta.DirectUnblocks != 0 || delta.TransitiveUnblocks != 0 || delta.BlockedReduction != 0 {
		t.Fatalf("what-if claimed unavailable work became actionable: %+v", delta)
	}
}

func TestWhatIfDeltaCountsParentPropagationCascade(t *testing.T) {
	issues := []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen},
		{ID: "PARENT", Status: model.StatusBlocked, Dependencies: []*model.Dependency{
			{DependsOnID: "ROOT", Type: model.DepBlocks},
		}},
		{ID: "CHILD", Status: model.StatusBlocked, Dependencies: []*model.Dependency{
			{DependsOnID: "PARENT", Type: model.DepParentChild},
		}},
	}

	analyzer := NewAnalyzer(issues)
	delta := analyzer.computeWhatIfDeltaFromStats("ROOT", &GraphStats{})
	if delta.DirectUnblocks != 1 || delta.TransitiveUnblocks != 2 {
		t.Fatalf("what-if parent cascade = %+v, want direct=1 transitive=2", delta)
	}
}

func TestTopWhatIfDeltasFromStats_UsesProvidedStatsAndPreservesOutput(t *testing.T) {
	const dependentCount = 12
	issues := make([]model.Issue, 0, dependentCount+2)
	issues = append(issues, model.Issue{ID: "ROOT", Title: "Root blocker", Status: model.StatusOpen})
	for i := 0; i < dependentCount; i++ {
		id := fmt.Sprintf("DEPENDENT-%03d", i)
		issues = append(issues, model.Issue{
			ID:     id,
			Title:  id,
			Status: model.StatusBlocked,
			Dependencies: []*model.Dependency{
				{IssueID: id, DependsOnID: "ROOT", Type: model.DepBlocks},
			},
		})
	}
	issues = append(issues, model.Issue{
		ID:     "CASCADE",
		Title:  "Cascade leaf",
		Status: model.StatusBlocked,
		Dependencies: []*model.Dependency{
			{IssueID: "CASCADE", DependsOnID: "DEPENDENT-000", Type: model.DepBlocks},
		},
	})

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()
	want := analyzer.TopWhatIfDeltas(0)
	got := analyzer.TopWhatIfDeltasFromStats(&stats, 0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provided-stats output differs from ordinary output:\n got: %#v\nwant: %#v", got, want)
	}
	if len(got) != 2 || got[0].IssueID != "ROOT" || got[1].IssueID != "DEPENDENT-000" {
		t.Fatalf("unexpected order: %#v", got)
	}

	root := got[0].Delta
	if root.DirectUnblocks != dependentCount || root.TransitiveUnblocks != dependentCount+1 {
		t.Fatalf("unexpected root unblock counts: %+v", root)
	}
	if root.BlockedReduction != dependentCount || root.ParallelizationGain == nil || *root.ParallelizationGain != dependentCount-1 {
		t.Fatalf("unexpected root reduction/parallelization: %+v", root)
	}
	if len(root.UnblockedIssueIDs) != MaxUnblockedIDsShown || root.UnblockedIssueIDs[0] != "DEPENDENT-000" || root.UnblockedIssueIDs[9] != "DEPENDENT-009" {
		t.Fatalf("unexpected capped unblocked IDs: %v", root.UnblockedIssueIDs)
	}

	provided := &GraphStats{criticalPathScore: map[string]float64{"ROOT": MaxCriticalPathDepth}}
	fromProvided := analyzer.TopWhatIfDeltasFromStats(provided, 1)
	if len(fromProvided) != 1 || fromProvided[0].IssueID != "ROOT" {
		t.Fatalf("n=1 cap returned unexpected result: %#v", fromProvided)
	}
	if fromProvided[0].Delta.DepthReduction != 1 {
		t.Fatalf("provided critical-path score was not consumed: depth reduction=%v, want 1", fromProvided[0].Delta.DepthReduction)
	}

	fromNil := analyzer.TopWhatIfDeltasFromStats(nil, 0)
	if !reflect.DeepEqual(fromNil, want) {
		t.Fatalf("nil stats fallback differs from ordinary output:\n got: %#v\nwant: %#v", fromNil, want)
	}
}

func TestGenerateEnhancedRecommendations_CappedAt10(t *testing.T) {
	now := time.Now()
	var issues []model.Issue

	// Create 20 issues
	for i := 0; i < 20; i++ {
		issues = append(issues, model.Issue{
			ID:        "TEST-" + string(rune('A'+i)),
			Title:     "Test Issue",
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: now.Add(-time.Duration(i) * 24 * time.Hour),
			UpdatedAt: now,
		})
	}

	analyzer := NewCachedAnalyzer(issues, nil)
	recs := analyzer.GenerateEnhancedRecommendations()

	if len(recs) > 10 {
		t.Errorf("expected at most 10 recommendations, got %d", len(recs))
	}
}

func TestGenerateEnhancedRecommendations_SortedByImpactScore(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "HIGH-IMPACT",
			Title:     "High Impact",
			Status:    model.StatusOpen,
			Priority:  1,
			CreatedAt: now.Add(-60 * 24 * time.Hour), // Older = more stale
			UpdatedAt: now,
		},
		{
			ID:        "LOW-IMPACT",
			Title:     "Low Impact",
			Status:    model.StatusOpen,
			Priority:  4,
			CreatedAt: now.Add(-1 * 24 * time.Hour),
			UpdatedAt: now,
		},
	}

	analyzer := NewCachedAnalyzer(issues, nil)
	recs := analyzer.GenerateEnhancedRecommendations()

	if len(recs) < 2 {
		t.Skip("Not enough recommendations to test sorting")
	}

	// Should be sorted by impact score descending
	for i := 1; i < len(recs); i++ {
		if recs[i].ImpactScore > recs[i-1].ImpactScore {
			t.Error("recommendations should be sorted by impact score descending")
		}
	}
}

func TestExplanationStatus_Capped(t *testing.T) {
	status := ExplanationStatus{
		Capped:       true,
		CappedFields: "unblocked_issue_ids",
	}

	if !status.Capped {
		t.Error("expected capped to be true")
	}

	if status.CappedFields != "unblocked_issue_ids" {
		t.Error("expected capped fields to be set")
	}
}

// BenchmarkTopWhatIfDeltas_StatsReuse measures the complete top-what-if batch
// after the analyzer's graph statistics have already been cached. The wide
// graph keeps cascade traversal small for all but one issue, making redundant
// per-issue cache reads visible in the benchmark result.
func BenchmarkTopWhatIfDeltas_StatsReuse(b *testing.B) {
	b.Setenv("BV_ROBOT", "1")
	b.Setenv("BV_CACHE_DIR", b.TempDir())

	const issueCount = 540
	issues := make([]model.Issue, issueCount)
	issues[0] = model.Issue{ID: "ROOT", Title: "Root blocker", Status: model.StatusOpen}
	for i := 1; i < issueCount; i++ {
		id := fmt.Sprintf("DEPENDENT-%03d", i)
		issues[i] = model.Issue{
			ID:     id,
			Title:  id,
			Status: model.StatusBlocked,
			Dependencies: []*model.Dependency{
				{IssueID: id, DependsOnID: "ROOT", Type: model.DepBlocks},
			},
		}
	}

	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()
	if stats.NodeCount != issueCount {
		b.Fatalf("benchmark setup produced %d nodes, want %d", stats.NodeCount, issueCount)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got := analyzer.TopWhatIfDeltas(10)
		if len(got) != 1 || got[0].IssueID != "ROOT" || got[0].Delta.TransitiveUnblocks != issueCount-1 {
			b.Fatalf("unexpected top what-if result: %+v", got)
		}
	}
}
