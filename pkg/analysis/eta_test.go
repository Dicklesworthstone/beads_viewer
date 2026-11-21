package analysis

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestEstimateETAForIssue_Basic(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:        "test-1",
			Title:     "Test issue",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"backend"},
		},
	}

	eta, err := EstimateETAForIssue(issues, nil, "test-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	if eta.IssueID != "test-1" {
		t.Errorf("Expected issue ID 'test-1', got %q", eta.IssueID)
	}

	if eta.EstimatedMinutes <= 0 {
		t.Errorf("Expected positive estimated minutes, got %d", eta.EstimatedMinutes)
	}

	if eta.Confidence <= 0 || eta.Confidence > 1 {
		t.Errorf("Expected confidence between 0 and 1, got %f", eta.Confidence)
	}

	if eta.ETADate.Before(now) {
		t.Errorf("ETA date should be in the future, got %v", eta.ETADate)
	}

	if eta.ETADateHigh.Before(eta.ETADate) {
		t.Errorf("High estimate should be >= ETA date")
	}

	if len(eta.Factors) == 0 {
		t.Error("Expected at least one factor")
	}
}

func TestEstimateETAForIssue_NotFound(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{}

	_, err := EstimateETAForIssue(issues, nil, "nonexistent", 1, now)
	if err == nil {
		t.Error("Expected error for nonexistent issue")
	}
}

func TestEstimateETAForIssue_WithExplicitEstimate(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	explicitMinutes := 120

	issues := []model.Issue{
		{
			ID:               "test-1",
			Title:            "Test issue with estimate",
			Status:           model.StatusOpen,
			IssueType:        model.TypeTask,
			EstimatedMinutes: &explicitMinutes,
		},
	}

	eta, err := EstimateETAForIssue(issues, nil, "test-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	// Explicit estimate should be higher confidence
	if eta.Confidence < 0.3 {
		t.Errorf("Expected higher confidence with explicit estimate, got %f", eta.Confidence)
	}

	// Should mention explicit estimate in factors
	hasExplicitFactor := false
	for _, f := range eta.Factors {
		if strings.HasPrefix(f, "estimate:") {
			hasExplicitFactor = true
			break
		}
	}
	if !hasExplicitFactor {
		t.Error("Expected factor mentioning estimate")
	}
}

func TestEstimateETAForIssue_TypeWeights(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	taskIssue := model.Issue{
		ID:        "task-1",
		Title:     "Task",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
	}

	epicIssue := model.Issue{
		ID:        "epic-1",
		Title:     "Epic",
		Status:    model.StatusOpen,
		IssueType: model.TypeEpic,
	}

	taskETA, _ := EstimateETAForIssue([]model.Issue{taskIssue}, nil, "task-1", 1, now)
	epicETA, _ := EstimateETAForIssue([]model.Issue{epicIssue}, nil, "epic-1", 1, now)

	// Epic should take longer than task (type weight is higher)
	if epicETA.EstimatedDays <= taskETA.EstimatedDays {
		t.Errorf("Epic should have longer ETA than task: epic=%f, task=%f",
			epicETA.EstimatedDays, taskETA.EstimatedDays)
	}
}

func TestEstimateETAForIssue_MultipleAgents(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:        "test-1",
			Title:     "Test issue",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
		},
	}

	eta1, _ := EstimateETAForIssue(issues, nil, "test-1", 1, now)
	eta2, _ := EstimateETAForIssue(issues, nil, "test-1", 2, now)

	// 2 agents should complete faster (roughly half the time)
	if eta2.EstimatedDays >= eta1.EstimatedDays {
		t.Errorf("2 agents should be faster than 1: 1 agent=%f days, 2 agents=%f days",
			eta1.EstimatedDays, eta2.EstimatedDays)
	}
}

func TestEstimateETAForIssue_VelocityFromClosures(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(-7 * 24 * time.Hour) // 7 days ago

	issues := []model.Issue{
		{
			ID:        "open-1",
			Title:     "Open issue",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"backend"},
		},
		{
			ID:        "closed-1",
			Title:     "Closed issue",
			Status:    model.StatusClosed,
			IssueType: model.TypeTask,
			Labels:    []string{"backend"},
			ClosedAt:  &closedAt,
		},
	}

	eta, err := EstimateETAForIssue(issues, nil, "open-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	// Should have velocity factor from closure history
	hasVelocityFactor := false
	for _, f := range eta.Factors {
		if strings.HasPrefix(f, "velocity:") {
			hasVelocityFactor = true
			break
		}
	}
	if !hasVelocityFactor {
		t.Error("Expected velocity factor from closure history")
	}
}

func TestEstimateETAForIssue_DepthAffectsComplexity(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:          "depth-1",
			Title:       "Depth issue",
			Status:      model.StatusOpen,
			IssueType:   model.TypeTask,
			Description: "",
		},
	}

	baseETA, err := EstimateETAForIssue(issues, nil, "depth-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	stats := &GraphStats{
		criticalPathScore: map[string]float64{"depth-1": 10},
	}
	depthETA, err := EstimateETAForIssue(issues, stats, "depth-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	if depthETA.EstimatedMinutes <= baseETA.EstimatedMinutes {
		t.Errorf("Expected depth to increase estimated minutes: base=%d, depth=%d", baseETA.EstimatedMinutes, depthETA.EstimatedMinutes)
	}
	if depthETA.EstimatedDays <= baseETA.EstimatedDays {
		t.Errorf("Expected depth to increase estimated days: base=%f, depth=%f", baseETA.EstimatedDays, depthETA.EstimatedDays)
	}
}

func TestComputeMedianEstimatedMinutes(t *testing.T) {
	// No estimates - should return default
	emptyIssues := []model.Issue{{ID: "1"}}
	median := computeMedianEstimatedMinutes(emptyIssues)
	if median != DefaultEstimatedMinutes {
		t.Errorf("Expected default %d for empty estimates, got %d", DefaultEstimatedMinutes, median)
	}

	// Odd number of estimates
	est30, est60, est90 := 30, 60, 90
	oddIssues := []model.Issue{
		{ID: "1", EstimatedMinutes: &est30},
		{ID: "2", EstimatedMinutes: &est60},
		{ID: "3", EstimatedMinutes: &est90},
	}
	median = computeMedianEstimatedMinutes(oddIssues)
	if median != 60 {
		t.Errorf("Expected median 60 for odd count, got %d", median)
	}

	// Even number of estimates
	est120 := 120
	evenIssues := []model.Issue{
		{ID: "1", EstimatedMinutes: &est30},
		{ID: "2", EstimatedMinutes: &est60},
		{ID: "3", EstimatedMinutes: &est90},
		{ID: "4", EstimatedMinutes: &est120},
	}
	median = computeMedianEstimatedMinutes(evenIssues)
	// Median of [30, 60, 90, 120] = (60 + 90) / 2 = 75
	if median != 75 {
		t.Errorf("Expected median 75 for even count, got %d", median)
	}
}

func TestClampFloat(t *testing.T) {
	if clampFloat(0.5, 0.0, 1.0) != 0.5 {
		t.Error("Value in range should not change")
	}
	if clampFloat(-0.5, 0.0, 1.0) != 0.0 {
		t.Error("Value below range should be clamped to lo")
	}
	if clampFloat(1.5, 0.0, 1.0) != 1.0 {
		t.Error("Value above range should be clamped to hi")
	}
}

func TestDurationDays(t *testing.T) {
	if durationDays(0) != 0 {
		t.Error("0 days should return 0 duration")
	}
	if durationDays(-1) != 0 {
		t.Error("negative days should return 0 duration")
	}

	oneDay := durationDays(1)
	expected := 24 * time.Hour
	if oneDay != expected {
		t.Errorf("Expected %v for 1 day, got %v", expected, oneDay)
	}
}

// TestHasLabel tests the hasLabel helper function
func TestHasLabelETA(t *testing.T) {
	if !hasLabel([]string{"a", "b", "c"}, "b") {
		t.Error("hasLabel should find 'b' in slice")
	}
	if !hasLabel([]string{"API"}, "api") {
		t.Error("hasLabel should be case-insensitive")
	}
	if hasLabel([]string{"a", "b", "c"}, "d") {
		t.Error("hasLabel should not find 'd' in slice")
	}
	if hasLabel([]string{}, "a") {
		t.Error("hasLabel should return false for empty slice")
	}
	if hasLabel(nil, "a") {
		t.Error("hasLabel should return false for nil slice")
	}
}

func TestEstimateETAForIssue_AllIssueTypes(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	types := []struct {
		issueType model.IssueType
		name      string
	}{
		{model.TypeBug, "bug"},
		{model.TypeTask, "task"},
		{model.TypeChore, "chore"},
		{model.TypeFeature, "feature"},
		{model.TypeEpic, "epic"},
	}

	for _, tc := range types {
		t.Run(tc.name, func(t *testing.T) {
			issues := []model.Issue{
				{
					ID:        "test-1",
					Title:     "Test " + tc.name,
					Status:    model.StatusOpen,
					IssueType: tc.issueType,
				},
			}
			eta, err := EstimateETAForIssue(issues, nil, "test-1", 1, now)
			if err != nil {
				t.Fatalf("EstimateETAForIssue failed for %s: %v", tc.name, err)
			}
			if eta.EstimatedMinutes <= 0 {
				t.Errorf("Expected positive minutes for %s, got %d", tc.name, eta.EstimatedMinutes)
			}
		})
	}
}

func TestEstimateETAForIssue_ZeroAgents(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:        "test-1",
			Title:     "Test issue",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
		},
	}

	// Zero agents should normalize to 1
	eta, err := EstimateETAForIssue(issues, nil, "test-1", 0, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}
	if eta.Agents != 1 {
		t.Errorf("Expected agents to be normalized to 1, got %d", eta.Agents)
	}
}

func TestEstimateETAForIssue_DescriptionLengthImpact(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	shortDesc := "Short description"
	longDesc := strings.Repeat("This is a very long description. ", 100) // ~3000 chars

	shortIssue := model.Issue{
		ID:          "short-1",
		Title:       "Short desc issue",
		Status:      model.StatusOpen,
		IssueType:   model.TypeTask,
		Description: shortDesc,
	}

	longIssue := model.Issue{
		ID:          "long-1",
		Title:       "Long desc issue",
		Status:      model.StatusOpen,
		IssueType:   model.TypeTask,
		Description: longDesc,
	}

	shortETA, _ := EstimateETAForIssue([]model.Issue{shortIssue}, nil, "short-1", 1, now)
	longETA, _ := EstimateETAForIssue([]model.Issue{longIssue}, nil, "long-1", 1, now)

	// Longer description should increase complexity
	if longETA.EstimatedMinutes <= shortETA.EstimatedMinutes {
		t.Errorf("Long description should increase estimate: short=%d, long=%d",
			shortETA.EstimatedMinutes, longETA.EstimatedMinutes)
	}
}

func TestEstimateETAForIssue_NoLabelsConfidencePenalty(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	withLabels := model.Issue{
		ID:        "with-labels",
		Title:     "Issue with labels",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Labels:    []string{"backend", "api"},
	}

	noLabels := model.Issue{
		ID:        "no-labels",
		Title:     "Issue without labels",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Labels:    []string{},
	}

	withLabelsETA, _ := EstimateETAForIssue([]model.Issue{withLabels}, nil, "with-labels", 1, now)
	noLabelsETA, _ := EstimateETAForIssue([]model.Issue{noLabels}, nil, "no-labels", 1, now)

	// No labels should have lower confidence (penalty applied)
	if noLabelsETA.Confidence >= withLabelsETA.Confidence {
		t.Errorf("No labels should have lower confidence: with=%f, without=%f",
			withLabelsETA.Confidence, noLabelsETA.Confidence)
	}
}

func TestVelocityMinutesPerDayForLabel_EdgeCases(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	since := now.Add(-30 * 24 * time.Hour)

	// No closed issues
	openOnly := []model.Issue{
		{ID: "1", Status: model.StatusOpen},
	}
	v, n := velocityMinutesPerDayForLabel(openOnly, "", since, 60)
	if v != 0 || n != 0 {
		t.Errorf("No closed issues should return 0 velocity: v=%f, n=%d", v, n)
	}

	// Closed issues before window
	oldClosed := now.Add(-60 * 24 * time.Hour) // 60 days ago
	oldClosures := []model.Issue{
		{ID: "1", Status: model.StatusClosed, ClosedAt: &oldClosed},
	}
	v, n = velocityMinutesPerDayForLabel(oldClosures, "", since, 60)
	if v != 0 || n != 0 {
		t.Errorf("Old closures should return 0 velocity: v=%f, n=%d", v, n)
	}

	// Closed issues within window
	recentClosed := now.Add(-7 * 24 * time.Hour) // 7 days ago
	est120 := 120
	recentClosures := []model.Issue{
		{ID: "1", Status: model.StatusClosed, ClosedAt: &recentClosed, EstimatedMinutes: &est120, Labels: []string{"api"}},
	}
	v, n = velocityMinutesPerDayForLabel(recentClosures, "api", since, 60)
	if n != 1 {
		t.Errorf("Expected 1 sample, got %d", n)
	}
	if v <= 0 {
		t.Errorf("Expected positive velocity, got %f", v)
	}
}

func TestEstimateETAConfidence_AllCases(t *testing.T) {
	est120 := 120

	tests := []struct {
		name            string
		issue           model.Issue
		velocitySamples int
		minExpected     float64
		maxExpected     float64
	}{
		{
			name:            "no estimate, no samples, no labels",
			issue:           model.Issue{Labels: nil},
			velocitySamples: 0,
			minExpected:     0.10, // 0.25 - 0.05 - 0.05 = 0.15, clamped to 0.10
			maxExpected:     0.20,
		},
		{
			name:            "with estimate, no samples, with labels",
			issue:           model.Issue{EstimatedMinutes: &est120, Labels: []string{"api"}},
			velocitySamples: 0,
			minExpected:     0.40, // 0.25 + 0.25 - 0.05 = 0.45
			maxExpected:     0.50,
		},
		{
			name:            "with estimate, few samples (1-4), with labels",
			issue:           model.Issue{EstimatedMinutes: &est120, Labels: []string{"api"}},
			velocitySamples: 3,
			minExpected:     0.55, // 0.25 + 0.25 + 0.10 = 0.60
			maxExpected:     0.65,
		},
		{
			name:            "with estimate, medium samples (5-14), with labels",
			issue:           model.Issue{EstimatedMinutes: &est120, Labels: []string{"api"}},
			velocitySamples: 10,
			minExpected:     0.65, // 0.25 + 0.25 + 0.20 = 0.70
			maxExpected:     0.75,
		},
		{
			name:            "with estimate, many samples (15+), with labels",
			issue:           model.Issue{EstimatedMinutes: &est120, Labels: []string{"api"}},
			velocitySamples: 20,
			minExpected:     0.75, // 0.25 + 0.25 + 0.30 = 0.80
			maxExpected:     0.85,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conf := estimateETAConfidence(tc.issue, tc.velocitySamples)
			if conf < tc.minExpected || conf > tc.maxExpected {
				t.Errorf("Expected confidence in [%f, %f], got %f", tc.minExpected, tc.maxExpected, conf)
			}
		})
	}
}

func TestEstimateETAForIssue_GlobalVelocityFallback(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(-7 * 24 * time.Hour)

	// Issue with labels that have no velocity data, but global velocity exists
	issues := []model.Issue{
		{
			ID:        "target",
			Title:     "Target issue",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Labels:    []string{"rare-label"}, // No closures for this label
		},
		{
			ID:       "closed-global",
			Title:    "Closed issue",
			Status:   model.StatusClosed,
			ClosedAt: &closedAt,
			Labels:   []string{"other-label"},
		},
	}

	eta, err := EstimateETAForIssue(issues, nil, "target", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}

	// Should fallback to global velocity
	hasGlobalVelocity := false
	for _, f := range eta.Factors {
		if strings.Contains(f, "global") {
			hasGlobalVelocity = true
			break
		}
	}
	if !hasGlobalVelocity {
		t.Error("Expected global velocity fallback in factors")
	}
}

func TestEstimateETAForIssue_EffectiveWorkVelocityScaleParity(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	longDesc := strings.Repeat("Detailed epic acceptance criteria and subsystem wiring. ", 60)

	// Close 30 identical complex epics over 30 days => steady pace of exactly 1 epic/day.
	issues := make([]model.Issue, 0, 31)
	for d := 1; d <= 30; d++ {
		closedAt := now.Add(-time.Duration(d) * 24 * time.Hour)
		issues = append(issues, model.Issue{
			ID:          "closed-epic-" + string(rune('a'+(d-1)%26)) + string(rune('0'+d/10)),
			Title:       "Historical Epic",
			Description: longDesc,
			Status:      model.StatusClosed,
			IssueType:   model.TypeEpic,
			Labels:      []string{"core"},
			ClosedAt:    &closedAt,
		})
	}
	issues = append(issues, model.Issue{
		ID:          "open-epic",
		Title:       "Next Identical Epic",
		Description: longDesc,
		Status:      model.StatusOpen,
		IssueType:   model.TypeEpic,
		Labels:      []string{"core"},
	})

	eta, err := EstimateETAForIssue(issues, nil, "open-epic", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}
	// With 30 identical epics closed in 30 days and k=5 shrinkage toward an identical global
	// velocity, the estimated duration for 1 open identical epic must be ~1.0 day (not 5.4x-10.8x).
	diff := eta.EstimatedDays - 1.0
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.05 {
		t.Fatalf("expected ~1.0 day ETA for steady 1-epic/day pace, got %.4f days (factors=%v)", eta.EstimatedDays, eta.Factors)
	}
}

func TestEstimateVelocity_BuhlmannStraubCredibilityShrinkage(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-5 * 24 * time.Hour)
	est300 := 300
	est1800 := 1800

	// 19 baseline closures of 300m each + 1 outlier label closure of 1800m over 30d:
	// global total = 19*300 + 1800 = 7500m => v_global = 250 m/day.
	// outlier label n=1 => v_label = 1800/30 = 60 m/day.
	// Buhlmann-Straub k=5: v_shrunk = (1*60 + 5*250) / 6 = 1310 / 6 = 218.3333 m/day.
	issues := make([]model.Issue, 0, 21)
	for i := 0; i < 19; i++ {
		issues = append(issues, model.Issue{
			ID:               "base-" + string(rune('a'+i)),
			Status:           model.StatusClosed,
			IssueType:        model.TypeTask,
			EstimatedMinutes: &est300,
			Labels:           []string{"steady"},
			ClosedAt:         &closedAt,
		})
	}
	issues = append(issues,
		model.Issue{
			ID:               "outlier-closed",
			Status:           model.StatusClosed,
			IssueType:        model.TypeTask,
			EstimatedMinutes: &est1800,
			Labels:           []string{"rare"},
			ClosedAt:         &closedAt,
		},
		model.Issue{
			ID:               "target-open",
			Status:           model.StatusOpen,
			IssueType:        model.TypeTask,
			EstimatedMinutes: &est300,
			Labels:           []string{"rare"},
		},
	)

	vel, _, _ := estimateVelocityMinutesPerDayWithStats(issues, nil, issues[len(issues)-1], now, 300)
	want := (1.0*60.0 + 5.0*250.0) / 6.0
	diff := vel - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.01 {
		t.Fatalf("Buhlmann-Straub shrunk velocity = %.4f, want %.4f (unshrunk 60.0, global 250.0)", vel, want)
	}
}

func TestComputeGrahamBounds_ParallelSchedulingInvariants(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	m120 := 120
	// Chain A -> B (240m critical path) + 2 independent tasks C, D (120m each) => W = 480m, L_crit = 240m.
	openIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &m120},
		{ID: "B", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &m120, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &m120},
		{ID: "D", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &m120},
	}

	b3 := ComputeGrahamBounds(openIssues, openIssues, nil, 3, now)
	if b3.TotalMinutes != 480 || b3.CriticalPathMinutes != 240 {
		t.Fatalf("unexpected W/L_crit: %+v", b3)
	}
	// m=3: T_min = max(240, 480/3=160) = 240; T_max = 240 + (480-240)/3 = 320.
	if b3.GrahamMinMinutes != 240 || b3.GrahamMaxMinutes != 320 {
		t.Fatalf("unexpected m=3 Graham minute bounds: min=%.2f max=%.2f (want 240, 320)", b3.GrahamMinMinutes, b3.GrahamMaxMinutes)
	}
	if !(b3.GrahamMinDays <= b3.GrahamMaxDays && b3.GrahamMinDays > 0) {
		t.Fatalf("invalid Graham day bounds: %+v", b3)
	}

	// m=1: T_min == T_max == W == 480.
	b1 := ComputeGrahamBounds(openIssues, openIssues, nil, 1, now)
	if b1.GrahamMinMinutes != 480 || b1.GrahamMaxMinutes != 480 {
		t.Fatalf("m=1 Graham bounds must collapse to W=480: %+v", b1)
	}
}

func TestEstimateETAAndGrahamBounds_DefaultCalendarVelocityParity(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	est140 := 140
	openSingle := []model.Issue{
		{ID: "TASK-1", Title: "Calendar velocity parity", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &est140},
	}

	// With zero historical closures, both EstimateETAForIssue and ComputeGrahamBounds (m=1)
	// must use the exact same calendar-day fallback velocity (medianMinutes / 7.0 = 20 min/day -> 7.0 days).
	eta, err := EstimateETAForIssue(openSingle, nil, "TASK-1", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue failed: %v", err)
	}
	bounds := ComputeGrahamBounds(openSingle, openSingle, nil, 1, now)

	diff := eta.EstimatedDays - bounds.GrahamMinDays
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-6 {
		t.Fatalf("default fallback velocity mismatch: EstimateETAForIssue=%.6fd vs ComputeGrahamBounds=%.6fd", eta.EstimatedDays, bounds.GrahamMinDays)
	}
	if eta.EstimatedDays < 6.99 || eta.EstimatedDays > 7.01 {
		t.Fatalf("expected 1 issue at medianMinutes with 1 issue/7-calendar-day fallback to take 7.0 days, got %.6f", eta.EstimatedDays)
	}
}

func TestComputeGrahamBounds_IgnoresParentChildDependencies(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	m120 := 120
	openIssues := []model.Issue{
		{ID: "PARENT", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &m120},
		{
			ID:               "CHILD",
			Status:           model.StatusOpen,
			IssueType:        model.TypeTask,
			EstimatedMinutes: &m120,
			Dependencies: []*model.Dependency{
				{IssueID: "CHILD", DependsOnID: "PARENT", Type: model.DepParentChild},
			},
		},
	}

	bounds := ComputeGrahamBounds(openIssues, openIssues, nil, 2, now)
	if bounds.TotalMinutes != 240 {
		t.Fatalf("TotalMinutes = %d, want 240", bounds.TotalMinutes)
	}
	if bounds.CriticalPathMinutes != 120 {
		t.Fatalf("non-blocking DepParentChild inflated CriticalPathMinutes to %d, want 120", bounds.CriticalPathMinutes)
	}
}

func TestETAIgnoresFutureClosures(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	pastClosed := now.AddDate(0, 0, -5)
	futureClosed := now.Add(48 * time.Hour)
	futureUpdated := now.Add(3 * time.Hour)

	est100 := 100
	est300 := 300
	est3000 := 3000

	issues := []model.Issue{
		{
			ID:               "TARGET",
			Title:            "Open API work",
			Status:           model.StatusOpen,
			IssueType:        model.TypeTask,
			Labels:           []string{"api"},
			EstimatedMinutes: &est100,
		},
		{
			ID:               "PAST-1",
			Title:            "Completed 5 days ago",
			Status:           model.StatusClosed,
			IssueType:        model.TypeTask,
			Labels:           []string{"api"},
			EstimatedMinutes: &est300, // 300 min / 30d = 10 min/day -> TARGET takes 10.0 days
			ClosedAt:         &pastClosed,
			UpdatedAt:        pastClosed,
		},
		{
			ID:               "FUTURE-CLOSED",
			Title:            "Future ClosedAt (e.g. historical --as-of or clock skew)",
			Status:           model.StatusClosed,
			IssueType:        model.TypeTask,
			Labels:           []string{"api"},
			EstimatedMinutes: &est3000,
			ClosedAt:         &futureClosed,
			UpdatedAt:        futureClosed,
		},
		{
			ID:               "FUTURE-UPDATED-FALLBACK",
			Title:            "Closed without ClosedAt, future UpdatedAt",
			Status:           model.StatusClosed,
			IssueType:        model.TypeTask,
			Labels:           []string{"api"},
			EstimatedMinutes: &est3000,
			UpdatedAt:        futureUpdated,
		},
	}

	etaAtNow, err := EstimateETAForIssue(issues, nil, "TARGET", 1, now)
	if err != nil {
		t.Fatalf("EstimateETAForIssue(now) failed: %v", err)
	}
	if etaAtNow.EstimatedDays < 9.99 || etaAtNow.EstimatedDays > 10.01 {
		t.Fatalf("future closures leaked into EstimateETAForIssue: got %.4f days, want 10.0 days", etaAtNow.EstimatedDays)
	}
	if etaAtNow.VelocityMinutesPerDay < 9.99 || etaAtNow.VelocityMinutesPerDay > 10.01 {
		t.Fatalf("VelocityMinutesPerDay = %.4f, want 10.0", etaAtNow.VelocityMinutesPerDay)
	}

	openOnly := issues[:1]
	boundsAtNow := ComputeGrahamBounds(openOnly, issues, nil, 1, now)
	if boundsAtNow.VelocityMinutesPerDay < 9.99 || boundsAtNow.VelocityMinutesPerDay > 10.01 {
		t.Fatalf("future closures leaked into ComputeGrahamBounds: VelocityMinutesPerDay=%.4f, want 10.0", boundsAtNow.VelocityMinutesPerDay)
	}
	if boundsAtNow.GrahamMinDays < 9.99 || boundsAtNow.GrahamMinDays > 10.01 {
		t.Fatalf("GrahamMinDays = %.4f, want 10.0", boundsAtNow.GrahamMinDays)
	}

	// Planted negative control: advancing `now` past those closure timestamps makes them legitimate
	// historical closures within (laterNow-30d, laterNow], increasing velocity to 6300/30 = 210 min/day.
	laterNow := now.AddDate(0, 0, 5)
	etaLater, err := EstimateETAForIssue(issues, nil, "TARGET", 1, laterNow)
	if err != nil {
		t.Fatalf("EstimateETAForIssue(laterNow) failed: %v", err)
	}
	wantLaterDays := 100.0 / 210.0
	if etaLater.EstimatedDays < wantLaterDays-0.01 || etaLater.EstimatedDays > wantLaterDays+0.01 {
		t.Fatalf("laterNow EstimateETAForIssue = %.4f days, want %.4f once closures are in the past", etaLater.EstimatedDays, wantLaterDays)
	}
	if etaLater.VelocityMinutesPerDay < 209.99 || etaLater.VelocityMinutesPerDay > 210.01 {
		t.Fatalf("laterNow VelocityMinutesPerDay = %.4f, want 210.0", etaLater.VelocityMinutesPerDay)
	}
}

func TestVelocityAndDurationSafety_ZeroFutureClosedAtAndNilCompareSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	zeroTime := time.Time{}
	created := now.Add(-48 * time.Hour)
	updated := now.Add(-24 * time.Hour)
	futureClosed := now.Add(72 * time.Hour)
	est60 := 60

	issues := []model.Issue{
		{
			ID:               "ZERO-CLOSED",
			Status:           model.StatusClosed,
			CreatedAt:        created,
			UpdatedAt:        updated,
			ClosedAt:         &zeroTime, // non-nil zero time must fall back to UpdatedAt, never year 0001!
			EstimatedMinutes: &est60,
		},
		{
			ID:               "FUTURE-CLOSED",
			Status:           model.StatusClosed,
			CreatedAt:        created,
			UpdatedAt:        updated,
			ClosedAt:         &futureClosed, // future closure must be rejected
			EstimatedMinutes: &est60,
		},
		{
			ID:               "TOMB-1",
			Status:           model.StatusTombstone,
			CreatedAt:        created,
			UpdatedAt:        updated,
			EstimatedMinutes: &est60,
		},
	}

	vel := ComputeProjectVelocity(issues, now, 4)
	if vel.ClosedLast7Days != 2 || vel.ClosedLast30Days != 2 {
		t.Fatalf("expected 2 valid closures (ZERO-CLOSED fallback + TOMB-1, excluding FUTURE-CLOSED), got 7d=%d 30d=%d", vel.ClosedLast7Days, vel.ClosedLast30Days)
	}
	if vel.AvgDaysToClose < 0.99 || vel.AvgDaysToClose > 1.01 {
		t.Fatalf("non-nil zero *ClosedAt corrupted AvgDaysToClose: got %.4f want 1.0", vel.AvgDaysToClose)
	}

	// ETA velocity must also accept ZERO-CLOSED via UpdatedAt fallback and reject FUTURE-CLOSED.
	since := now.Add(-30 * 24 * time.Hour)
	vPerDay, samples := velocityMinutesPerDayForLabelWithStats(issues, nil, "", since, now, 60)
	if samples != 2 || vPerDay <= 0 {
		t.Fatalf("velocityMinutesPerDayForLabelWithStats = (%.4f, %d), want positive with 2 samples", vPerDay, samples)
	}

	// durationDays overflow & NaN/Inf guards
	if d := durationDays(math.NaN()); d != 0 {
		t.Fatalf("durationDays(NaN) = %v, want 0", d)
	}
	if d := durationDays(1e9); d != time.Duration(math.MaxInt64) {
		t.Fatalf("durationDays(1e9) = %v, want MaxInt64 (no int64 wrap)", d)
	}
	if d := durationDays(math.Inf(1)); d != time.Duration(math.MaxInt64) {
		t.Fatalf("durationDays(+Inf) = %v, want MaxInt64", d)
	}

	// CompareSnapshots nil guards
	snap := NewSnapshotAt(issues, now, "rev")
	if diff := CompareSnapshots(nil, nil); diff == nil || len(diff.NewIssues) != 0 {
		t.Fatalf("CompareSnapshots(nil, nil) unexpected: %+v", diff)
	}
	if diff := CompareSnapshots(nil, snap); diff == nil || len(diff.NewIssues) != len(issues) {
		t.Fatalf("CompareSnapshots(nil, snap) NewIssues=%d, want %d", len(diff.NewIssues), len(issues))
	}
	if diff := CompareSnapshots(snap, nil); diff == nil || len(diff.RemovedIssues) != len(issues) {
		t.Fatalf("CompareSnapshots(snap, nil) RemovedIssues=%d, want %d", len(diff.RemovedIssues), len(issues))
	}
}

func TestForceFullAnalysis_HonorsExactActiveBetweennessAndProfileMetrics(t *testing.T) {
	// Construct 505 active nodes + 5 closed nodes with density >= 0.01 (ConfigForSize would skip betweenness)
	// and prove FullAnalysisConfig() (--force-full-analysis) computes non-zero exact active betweenness.
	const activeN = 505
	issues := make([]model.Issue, 0, activeN+5)
	for i := 0; i < activeN; i++ {
		id := fmt.Sprintf("A-%03d", i)
		iss := model.Issue{ID: id, Status: model.StatusOpen, IssueType: model.TypeTask}
		// Connect chain plus hub edges so density > 0.01 and node A-001 has positive betweenness
		if i > 0 {
			iss.Dependencies = append(iss.Dependencies, &model.Dependency{
				IssueID: id, DependsOnID: fmt.Sprintf("A-%03d", i-1), Type: model.DepBlocks,
			})
		}
		// Add ~6 extra edges per node (~3000 edges / (505*504) ~= 0.0118 >= 0.01)
		for step := 2; step <= 7 && i-step >= 0; step++ {
			iss.Dependencies = append(iss.Dependencies, &model.Dependency{
				IssueID: id, DependsOnID: fmt.Sprintf("A-%03d", i-step), Type: model.DepBlocks,
			})
		}
		issues = append(issues, iss)
	}
	for c := 0; c < 5; c++ {
		issues = append(issues, model.Issue{ID: fmt.Sprintf("C-%d", c), Status: model.StatusClosed, IssueType: model.TypeTask})
	}

	an := NewAnalyzer(issues)
	fullCfg := FullAnalysisConfig()
	fullCfg.ComputeCycles = false
	fullCfg.ComputeHITS = false
	stats := an.AnalyzeWithConfig(fullCfg)
	activeBW, _, _ := an.activeSubgraphMetrics(&stats)
	nonZero := 0
	for _, v := range activeBW {
		if v > 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Fatalf("FullAnalysisConfig() on %d dense active nodes + 5 closed returned all-zero active betweenness (silently overridden by ConfigForSize)", activeN)
	}
	if len(stats.ActiveBetweennessRank()) != activeN {
		t.Fatalf("ActiveBetweennessRank len = %d, want %d", len(stats.ActiveBetweennessRank()), activeN)
	}

	// Verify AnalyzeWithProfile reports BetweennessIsApproximate=true on sparse 505-node graph,
	// populates InDegreeRank/OutDegreeRank, and times Articulation independently.
	sparse := make([]model.Issue, activeN)
	for i := 0; i < activeN; i++ {
		id := fmt.Sprintf("S-%03d", i)
		sparse[i] = model.Issue{ID: id, Status: model.StatusOpen, IssueType: model.TypeTask}
		if i > 0 {
			sparse[i].Dependencies = []*model.Dependency{{IssueID: id, DependsOnID: fmt.Sprintf("S-%03d", i-1), Type: model.DepBlocks}}
		}
	}
	anSparse := NewAnalyzer(sparse)
	cfgSparse := ConfigForSize(len(sparse), len(sparse)-1)
	pStats, profile := anSparse.AnalyzeWithProfile(cfgSparse)
	if !profile.Config.BetweennessIsApproximate {
		t.Fatalf("AnalyzeWithProfile reported BetweennessIsApproximate=false for mode=%s", profile.Config.BetweennessMode)
	}
	if len(pStats.InDegreeRank()) != activeN || len(pStats.OutDegreeRank()) != activeN {
		t.Fatalf("computePhase1WithProfile omitted degree ranks: in=%d out=%d", len(pStats.InDegreeRank()), len(pStats.OutDegreeRank()))
	}
	if pStats.Status().Articulation.State != "computed" || len(pStats.ArticulationPoints()) != activeN-2 {
		t.Fatalf("AnalyzeWithProfile articulation state=%q count=%d, want computed/%d",
			pStats.Status().Articulation.State, len(pStats.ArticulationPoints()), activeN-2)
	}
	// Verify Articulation runs independently even when KCore is disabled.
	cfgArtOnly := cfgSparse
	cfgArtOnly.ComputeKCore = false
	cfgArtOnly.ComputeArticulation = true
	pArtStats, _ := NewAnalyzer(sparse).AnalyzeWithProfile(cfgArtOnly)
	if pArtStats.Status().Articulation.State != "computed" || len(pArtStats.ArticulationPoints()) != activeN-2 {
		t.Fatalf("Articulation with ComputeKCore=false: state=%q count=%d, want computed/%d",
			pArtStats.Status().Articulation.State, len(pArtStats.ArticulationPoints()), activeN-2)
	}
}

func TestWave22_ComputeGrahamBounds_ClosedParentStopsInheritedBlocking(t *testing.T) {
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	closedAt := now.Add(-48 * time.Hour)
	est120 := 120
	issues := []model.Issue{
		{ID: "X", Title: "Open blocker", Status: model.StatusOpen, IssueType: model.TypeTask, EstimatedMinutes: &est120},
		{
			ID: "P", Title: "Closed epic", Status: model.StatusClosed, IssueType: model.TypeEpic,
			ClosedAt:     &closedAt,
			Dependencies: []*model.Dependency{{IssueID: "P", DependsOnID: "X", Type: model.DepBlocks}},
		},
		{
			ID: "C", Title: "Open child of closed epic", Status: model.StatusOpen, IssueType: model.TypeTask,
			EstimatedMinutes: &est120,
			Dependencies:     []*model.Dependency{{IssueID: "C", DependsOnID: "P", Type: model.DepParentChild}},
		},
	}
	openOnly := []model.Issue{issues[0], issues[2]}
	gb := ComputeGrahamBounds(openOnly, issues, nil, 2, now)
	// X and C are both ready in parallel (closed parent P stops inheritance per model.ReadinessIndex).
	// Total work = 2 * estimate(120m task) = 264m; critical path = single task (132m), NOT 264m!
	if gb.CriticalPathMinutes >= gb.TotalMinutes {
		t.Fatalf("closed parent P leaked inherited blocker into Graham critical path: crit=%d total=%d",
			gb.CriticalPathMinutes, gb.TotalMinutes)
	}
}
