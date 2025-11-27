package analysis_test

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestComputeImpactScoresEmpty(t *testing.T) {
	an := analysis.NewAnalyzer([]model.Issue{})
	scores := an.ComputeImpactScores()

	if len(scores) != 0 {
		t.Errorf("Expected 0 scores for empty list, got %d", len(scores))
	}
}

func TestComputeImpactScoresSkipsClosed(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Open Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Closed Issue", Status: model.StatusClosed, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScores()

	if len(scores) != 1 {
		t.Errorf("Expected 1 score (closed excluded), got %d", len(scores))
	}
	if scores[0].IssueID != "A" {
		t.Errorf("Expected issue A, got %s", scores[0].IssueID)
	}
}

func TestComputeImpactScoresPriorityBoost(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "P0", Title: "Priority 0", Status: model.StatusOpen, Priority: 0, UpdatedAt: now},
		{ID: "P1", Title: "Priority 1", Status: model.StatusOpen, Priority: 1, UpdatedAt: now},
		{ID: "P2", Title: "Priority 2", Status: model.StatusOpen, Priority: 2, UpdatedAt: now},
		{ID: "P3", Title: "Priority 3", Status: model.StatusOpen, Priority: 3, UpdatedAt: now},
		{ID: "P4", Title: "Priority 4", Status: model.StatusOpen, Priority: 4, UpdatedAt: now},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScoresAt(now)

	// Build map for easy lookup
	scoreMap := make(map[string]analysis.ImpactScore)
	for _, s := range scores {
		scoreMap[s.IssueID] = s
	}

	// Verify priority boost values
	if scoreMap["P0"].Breakdown.PriorityBoostNorm != 1.0 {
		t.Errorf("P0 should have boost 1.0, got %f", scoreMap["P0"].Breakdown.PriorityBoostNorm)
	}
	if scoreMap["P1"].Breakdown.PriorityBoostNorm != 0.75 {
		t.Errorf("P1 should have boost 0.75, got %f", scoreMap["P1"].Breakdown.PriorityBoostNorm)
	}
	if scoreMap["P2"].Breakdown.PriorityBoostNorm != 0.5 {
		t.Errorf("P2 should have boost 0.5, got %f", scoreMap["P2"].Breakdown.PriorityBoostNorm)
	}
	if scoreMap["P3"].Breakdown.PriorityBoostNorm != 0.25 {
		t.Errorf("P3 should have boost 0.25, got %f", scoreMap["P3"].Breakdown.PriorityBoostNorm)
	}
	if scoreMap["P4"].Breakdown.PriorityBoostNorm != 0.0 {
		t.Errorf("P4 should have boost 0.0, got %f", scoreMap["P4"].Breakdown.PriorityBoostNorm)
	}
}

func TestComputeImpactScoresStaleness(t *testing.T) {
	now := time.Now()

	issues := []model.Issue{
		{ID: "fresh", Title: "Fresh", Status: model.StatusOpen, Priority: 1, UpdatedAt: now},
		{ID: "week", Title: "Week Old", Status: model.StatusOpen, Priority: 1, UpdatedAt: now.AddDate(0, 0, -7)},
		{ID: "month", Title: "Month Old", Status: model.StatusOpen, Priority: 1, UpdatedAt: now.AddDate(0, 0, -30)},
		{ID: "ancient", Title: "Ancient", Status: model.StatusOpen, Priority: 1, UpdatedAt: now.AddDate(0, 0, -60)},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScoresAt(now)

	scoreMap := make(map[string]analysis.ImpactScore)
	for _, s := range scores {
		scoreMap[s.IssueID] = s
	}

	// Fresh item: ~0 staleness
	if scoreMap["fresh"].Breakdown.StalenessNorm > 0.1 {
		t.Errorf("Fresh item should have low staleness, got %f", scoreMap["fresh"].Breakdown.StalenessNorm)
	}

	// Week old: ~0.23 staleness (7/30)
	if scoreMap["week"].Breakdown.StalenessNorm < 0.2 || scoreMap["week"].Breakdown.StalenessNorm > 0.3 {
		t.Errorf("Week old should have ~0.23 staleness, got %f", scoreMap["week"].Breakdown.StalenessNorm)
	}

	// Month old: 1.0 staleness (30/30)
	if scoreMap["month"].Breakdown.StalenessNorm < 0.9 {
		t.Errorf("Month old should have ~1.0 staleness, got %f", scoreMap["month"].Breakdown.StalenessNorm)
	}

	// Ancient: capped at 1.0
	if scoreMap["ancient"].Breakdown.StalenessNorm != 1.0 {
		t.Errorf("Ancient should be capped at 1.0 staleness, got %f", scoreMap["ancient"].Breakdown.StalenessNorm)
	}
}

func TestComputeImpactScoresBlockerRatio(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Title: "Root", Status: model.StatusOpen, Priority: 1},
		{ID: "dep1", Title: "Dep 1", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "dep2", Title: "Dep 2", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "dep3", Title: "Dep 3", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScores()

	scoreMap := make(map[string]analysis.ImpactScore)
	for _, s := range scores {
		scoreMap[s.IssueID] = s
	}

	// root has 3 things depending on it, highest InDegree
	// All others have InDegree 0
	if scoreMap["root"].Breakdown.BlockerRatioNorm != 1.0 {
		t.Errorf("Root should have max blocker ratio (1.0), got %f", scoreMap["root"].Breakdown.BlockerRatioNorm)
	}
	if scoreMap["dep1"].Breakdown.BlockerRatioNorm != 0.0 {
		t.Errorf("dep1 should have 0 blocker ratio, got %f", scoreMap["dep1"].Breakdown.BlockerRatioNorm)
	}
}

func TestComputeImpactScoresPageRank(t *testing.T) {
	// Chain: A <- B <- C (C depends on B depends on A)
	// A should have highest PageRank (fundamental dependency)
	issues := []model.Issue{
		{ID: "A", Title: "Root", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Middle", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Leaf", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScores()

	scoreMap := make(map[string]analysis.ImpactScore)
	for _, s := range scores {
		scoreMap[s.IssueID] = s
	}

	// A is the fundamental dependency (highest PageRank)
	if scoreMap["A"].Breakdown.PageRankNorm != 1.0 {
		t.Errorf("A should have highest PageRank (1.0), got %f", scoreMap["A"].Breakdown.PageRankNorm)
	}
}

func TestComputeImpactScoresSortedDescending(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "low", Title: "Low Score", Status: model.StatusOpen, Priority: 4, UpdatedAt: now},
		{ID: "high", Title: "High Score", Status: model.StatusOpen, Priority: 0, UpdatedAt: now.AddDate(0, 0, -30)},
		{ID: "mid", Title: "Mid Score", Status: model.StatusOpen, Priority: 2, UpdatedAt: now.AddDate(0, 0, -15)},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScoresAt(now)

	if len(scores) < 2 {
		t.Fatal("Expected at least 2 scores")
	}

	// Verify sorted in descending order
	for i := 0; i < len(scores)-1; i++ {
		if scores[i].Score < scores[i+1].Score {
			t.Errorf("Scores not sorted descending: %f < %f at index %d",
				scores[i].Score, scores[i+1].Score, i)
		}
	}
}

func TestComputeImpactScoreSingle(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 2},
	}

	an := analysis.NewAnalyzer(issues)

	scoreA := an.ComputeImpactScore("A")
	if scoreA == nil {
		t.Fatal("Expected score for A")
	}
	if scoreA.IssueID != "A" {
		t.Errorf("Expected issue A, got %s", scoreA.IssueID)
	}

	scoreNone := an.ComputeImpactScore("nonexistent")
	if scoreNone != nil {
		t.Error("Expected nil for nonexistent issue")
	}
}

func TestTopImpactScores(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "A", Status: model.StatusOpen, Priority: 0},
		{ID: "B", Title: "B", Status: model.StatusOpen, Priority: 1},
		{ID: "C", Title: "C", Status: model.StatusOpen, Priority: 2},
		{ID: "D", Title: "D", Status: model.StatusOpen, Priority: 3},
		{ID: "E", Title: "E", Status: model.StatusOpen, Priority: 4},
	}

	an := analysis.NewAnalyzer(issues)

	top3 := an.TopImpactScores(3)
	if len(top3) != 3 {
		t.Errorf("Expected 3 scores, got %d", len(top3))
	}

	// Request more than available
	top10 := an.TopImpactScores(10)
	if len(top10) != 5 {
		t.Errorf("Expected 5 scores (all available), got %d", len(top10))
	}
}

func TestScoreBreakdownWeights(t *testing.T) {
	// Verify weights sum to 1.0
	totalWeight := analysis.WeightPageRank +
		analysis.WeightBetweenness +
		analysis.WeightBlockerRatio +
		analysis.WeightStaleness +
		analysis.WeightPriorityBoost

	if totalWeight != 1.0 {
		t.Errorf("Weights should sum to 1.0, got %f", totalWeight)
	}
}

func TestComputeImpactScoreDetails(t *testing.T) {
	issues := []model.Issue{
		{ID: "test", Title: "Test Issue", Status: model.StatusInProgress, Priority: 2},
	}

	an := analysis.NewAnalyzer(issues)
	scores := an.ComputeImpactScores()

	if len(scores) != 1 {
		t.Fatal("Expected 1 score")
	}

	score := scores[0]
	if score.Title != "Test Issue" {
		t.Errorf("Expected title 'Test Issue', got %s", score.Title)
	}
	if score.Priority != 2 {
		t.Errorf("Expected priority 2, got %d", score.Priority)
	}
	if score.Status != "in_progress" {
		t.Errorf("Expected status 'in_progress', got %s", score.Status)
	}
}

func TestGenerateRecommendationsEmpty(t *testing.T) {
	an := analysis.NewAnalyzer([]model.Issue{})
	recs := an.GenerateRecommendations()

	if len(recs) != 0 {
		t.Errorf("Expected 0 recommendations for empty issues, got %d", len(recs))
	}
}

func TestGenerateRecommendationsHighImpactLowPriority(t *testing.T) {
	// Create a scenario where an issue blocks many others but has low priority
	// This should generate a recommendation to increase priority
	now := time.Now()
	issues := []model.Issue{
		{ID: "blocker", Title: "Blocker Task", Status: model.StatusOpen, Priority: 3, UpdatedAt: now.AddDate(0, 0, -20)},
		{ID: "dep1", Title: "Dep 1", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "dep2", Title: "Dep 2", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "dep3", Title: "Dep 3", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	recs := an.GenerateRecommendations()

	// Should have at least one recommendation for "blocker"
	var blockerRec *analysis.PriorityRecommendation
	for i := range recs {
		if recs[i].IssueID == "blocker" {
			blockerRec = &recs[i]
			break
		}
	}

	if blockerRec == nil {
		t.Fatal("Expected recommendation for blocker issue")
	}

	if blockerRec.Direction != "increase" {
		t.Errorf("Expected direction 'increase', got %s", blockerRec.Direction)
	}

	if blockerRec.SuggestedPriority >= blockerRec.CurrentPriority {
		t.Errorf("Expected suggested priority < current priority (increase = lower number)")
	}

	if len(blockerRec.Reasoning) == 0 {
		t.Error("Expected reasoning to be populated")
	}
}

func TestGenerateRecommendationsConfidence(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "root", Title: "Root", Status: model.StatusOpen, Priority: 4, UpdatedAt: now.AddDate(0, 0, -30)},
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	recs := an.GenerateRecommendations()

	// Recommendations should be sorted by confidence descending
	for i := 0; i < len(recs)-1; i++ {
		if recs[i].Confidence < recs[i+1].Confidence {
			t.Errorf("Recommendations not sorted by confidence: %f < %f",
				recs[i].Confidence, recs[i+1].Confidence)
		}
	}

	// All recommendations should have confidence >= MinConfidence
	thresholds := analysis.DefaultThresholds()
	for _, rec := range recs {
		if rec.Confidence < thresholds.MinConfidence {
			t.Errorf("Recommendation has confidence %f < minimum %f",
				rec.Confidence, thresholds.MinConfidence)
		}
	}
}

func TestGenerateRecommendationsWithCustomThresholds(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "A", Status: model.StatusOpen, Priority: 3},
		{ID: "B", Title: "B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)

	// Very high confidence threshold - should filter out most
	strictThresholds := analysis.RecommendationThresholds{
		HighPageRank:     0.9,
		HighBetweenness:  0.9,
		StalenessDays:    1,
		MinConfidence:    0.9,
		SignificantDelta: 0.5,
	}

	strictRecs := an.GenerateRecommendationsWithThresholds(strictThresholds)

	// Very low confidence threshold - should include more
	looseThresholds := analysis.RecommendationThresholds{
		HighPageRank:     0.1,
		HighBetweenness:  0.1,
		StalenessDays:    1,
		MinConfidence:    0.1,
		SignificantDelta: 0.05,
	}

	looseRecs := an.GenerateRecommendationsWithThresholds(looseThresholds)

	// Loose thresholds should give at least as many recommendations
	if len(looseRecs) < len(strictRecs) {
		t.Errorf("Loose thresholds gave fewer recs (%d) than strict (%d)",
			len(looseRecs), len(strictRecs))
	}
}

func TestDefaultThresholds(t *testing.T) {
	thresholds := analysis.DefaultThresholds()

	if thresholds.HighPageRank <= 0 || thresholds.HighPageRank > 1 {
		t.Errorf("HighPageRank should be 0-1, got %f", thresholds.HighPageRank)
	}
	if thresholds.HighBetweenness <= 0 || thresholds.HighBetweenness > 1 {
		t.Errorf("HighBetweenness should be 0-1, got %f", thresholds.HighBetweenness)
	}
	if thresholds.MinConfidence <= 0 || thresholds.MinConfidence > 1 {
		t.Errorf("MinConfidence should be 0-1, got %f", thresholds.MinConfidence)
	}
	if thresholds.StalenessDays <= 0 {
		t.Errorf("StalenessDays should be positive, got %d", thresholds.StalenessDays)
	}
}

func TestRecommendationDirection(t *testing.T) {
	// Test that direction is correctly set
	now := time.Now()

	// Low priority blocking many = suggest increase (lower number)
	issues := []model.Issue{
		{ID: "low", Title: "Low Priority Blocker", Status: model.StatusOpen, Priority: 4, UpdatedAt: now.AddDate(0, 0, -20)},
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "low", Type: model.DepBlocks},
		}},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "low", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	recs := an.GenerateRecommendations()

	for _, rec := range recs {
		if rec.IssueID == "low" {
			if rec.Direction != "increase" {
				t.Errorf("Expected direction 'increase' for low priority blocker, got %s", rec.Direction)
			}
			break
		}
	}
}
