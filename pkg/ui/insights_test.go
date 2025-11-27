package ui_test

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

// createTestInsights creates a test Insights struct with sample data
func createTestInsights() analysis.Insights {
	return analysis.Insights{
		Bottlenecks: []analysis.InsightItem{
			{ID: "bottleneck-1", Value: 0.85},
			{ID: "bottleneck-2", Value: 0.65},
			{ID: "bottleneck-3", Value: 0.45},
		},
		Keystones: []analysis.InsightItem{
			{ID: "keystone-1", Value: 5.0},
			{ID: "keystone-2", Value: 3.0},
		},
		Influencers: []analysis.InsightItem{
			{ID: "influencer-1", Value: 0.92},
		},
		Hubs: []analysis.InsightItem{
			{ID: "hub-1", Value: 2.5},
			{ID: "hub-2", Value: 1.8},
		},
		Authorities: []analysis.InsightItem{
			{ID: "auth-1", Value: 3.2},
		},
		Cycles: [][]string{
			{"cycle-a", "cycle-b", "cycle-c"},
			{"cycle-x", "cycle-y"},
		},
		ClusterDensity: 0.42,
		Stats: &analysis.GraphStats{
			Betweenness:       map[string]float64{"bottleneck-1": 0.85, "bottleneck-2": 0.65},
			CriticalPathScore: map[string]float64{"keystone-1": 5.0, "keystone-2": 3.0},
			Eigenvector:       map[string]float64{"influencer-1": 0.92},
			Hubs:              map[string]float64{"hub-1": 2.5, "hub-2": 1.8},
			Authorities:       map[string]float64{"auth-1": 3.2},
			PageRank:          map[string]float64{"bottleneck-1": 0.15},
			InDegree:          map[string]int{"bottleneck-1": 3},
			OutDegree:         map[string]int{"bottleneck-1": 2},
		},
	}
}

// createTestIssueMap creates a map of test issues
func createTestIssueMap() map[string]*model.Issue {
	issues := []model.Issue{
		{ID: "bottleneck-1", Title: "Critical Junction", Status: model.StatusInProgress, IssueType: model.TypeBug},
		{ID: "bottleneck-2", Title: "Secondary Junction", Status: model.StatusOpen, IssueType: model.TypeFeature},
		{ID: "bottleneck-3", Title: "Minor Junction", Status: model.StatusOpen},
		{ID: "keystone-1", Title: "Foundation Component", Status: model.StatusOpen, IssueType: model.TypeTask},
		{ID: "keystone-2", Title: "Base Layer", Status: model.StatusClosed},
		{ID: "influencer-1", Title: "Central Hub", Status: model.StatusInProgress},
		{ID: "hub-1", Title: "Feature Epic", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "auth-1", Type: model.DepBlocks},
		}},
		{ID: "hub-2", Title: "Another Epic", Status: model.StatusOpen},
		{ID: "auth-1", Title: "Core Service", Status: model.StatusClosed},
		{ID: "cycle-a", Title: "Cycle Part A", Status: model.StatusBlocked},
		{ID: "cycle-b", Title: "Cycle Part B", Status: model.StatusBlocked},
		{ID: "cycle-c", Title: "Cycle Part C", Status: model.StatusBlocked},
		{ID: "cycle-x", Title: "Cycle X", Status: model.StatusBlocked},
		{ID: "cycle-y", Title: "Cycle Y", Status: model.StatusBlocked},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}
	return issueMap
}

// TestInsightsModelEmpty verifies behavior with empty insights
func TestInsightsModelEmpty(t *testing.T) {
	theme := createTheme()
	emptyInsights := analysis.Insights{}
	emptyMap := make(map[string]*model.Issue)

	m := ui.NewInsightsModel(emptyInsights, emptyMap, theme)
	m.SetSize(120, 40)

	// Navigation should not panic on empty panels
	m.MoveUp()
	m.MoveDown()
	m.NextPanel()
	m.PrevPanel()

	// SelectedIssueID should return empty string
	if id := m.SelectedIssueID(); id != "" {
		t.Errorf("Expected empty ID for empty insights, got %s", id)
	}

	// View should not panic
	_ = m.View()
}

// TestInsightsModelPanelNavigation verifies panel navigation
func TestInsightsModelPanelNavigation(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Start on Bottlenecks panel (index 0)
	id := m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected bottleneck-1 on start, got %s", id)
	}

	// NextPanel should move to Keystones
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "keystone-1" {
		t.Errorf("Expected keystone-1 after NextPanel, got %s", id)
	}

	// NextPanel to Influencers
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "influencer-1" {
		t.Errorf("Expected influencer-1 after NextPanel, got %s", id)
	}

	// NextPanel to Hubs
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "hub-1" {
		t.Errorf("Expected hub-1 after NextPanel, got %s", id)
	}

	// NextPanel to Authorities
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "auth-1" {
		t.Errorf("Expected auth-1 after NextPanel, got %s", id)
	}

	// NextPanel to Cycles - should return first item of first cycle
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "cycle-a" {
		t.Errorf("Expected cycle-a after NextPanel to Cycles, got %s", id)
	}

	// NextPanel should wrap back to Bottlenecks
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected bottleneck-1 after wrap, got %s", id)
	}

	// PrevPanel should go to Cycles
	m.PrevPanel()
	id = m.SelectedIssueID()
	if id != "cycle-a" {
		t.Errorf("Expected cycle-a after PrevPanel, got %s", id)
	}
}

// TestInsightsModelItemNavigation verifies up/down navigation within panels
func TestInsightsModelItemNavigation(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Start on Bottlenecks panel, first item
	id := m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected bottleneck-1, got %s", id)
	}

	// MoveDown to second item
	m.MoveDown()
	id = m.SelectedIssueID()
	if id != "bottleneck-2" {
		t.Errorf("Expected bottleneck-2 after MoveDown, got %s", id)
	}

	// MoveDown to third item
	m.MoveDown()
	id = m.SelectedIssueID()
	if id != "bottleneck-3" {
		t.Errorf("Expected bottleneck-3 after MoveDown, got %s", id)
	}

	// MoveDown at bottom should stay at bottom
	m.MoveDown()
	id = m.SelectedIssueID()
	if id != "bottleneck-3" {
		t.Errorf("Expected to stay at bottleneck-3, got %s", id)
	}

	// MoveUp should go back
	m.MoveUp()
	id = m.SelectedIssueID()
	if id != "bottleneck-2" {
		t.Errorf("Expected bottleneck-2 after MoveUp, got %s", id)
	}

	// MoveUp to first
	m.MoveUp()
	id = m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected bottleneck-1 after MoveUp, got %s", id)
	}

	// MoveUp at top should stay at top
	m.MoveUp()
	id = m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected to stay at bottleneck-1, got %s", id)
	}
}

// TestInsightsModelCyclesPanelNavigation verifies navigation in cycles panel
func TestInsightsModelCyclesPanelNavigation(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Navigate to Cycles panel (5 NextPanels from start)
	for i := 0; i < 5; i++ {
		m.NextPanel()
	}

	// Should be on first cycle, returning first item
	id := m.SelectedIssueID()
	if id != "cycle-a" {
		t.Errorf("Expected cycle-a, got %s", id)
	}

	// MoveDown to second cycle
	m.MoveDown()
	id = m.SelectedIssueID()
	if id != "cycle-x" {
		t.Errorf("Expected cycle-x (first of second cycle), got %s", id)
	}

	// MoveUp back to first cycle
	m.MoveUp()
	id = m.SelectedIssueID()
	if id != "cycle-a" {
		t.Errorf("Expected cycle-a after MoveUp, got %s", id)
	}
}

// TestInsightsModelToggleFunctions verifies toggle methods
func TestInsightsModelToggleFunctions(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Toggle explanations - should not panic
	m.ToggleExplanations()
	_ = m.View()
	m.ToggleExplanations()
	_ = m.View()

	// Toggle calculation - should not panic
	m.ToggleCalculation()
	_ = m.View()
	m.ToggleCalculation()
	_ = m.View()
}

// TestInsightsModelSetInsights verifies SetInsights updates data
func TestInsightsModelSetInsights(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Verify initial data
	id := m.SelectedIssueID()
	if id != "bottleneck-1" {
		t.Errorf("Expected bottleneck-1, got %s", id)
	}

	// Update with new insights
	newInsights := analysis.Insights{
		Bottlenecks: []analysis.InsightItem{
			{ID: "new-bottleneck", Value: 0.99},
		},
	}
	m.SetInsights(newInsights)

	// Should now show new data
	id = m.SelectedIssueID()
	if id != "new-bottleneck" {
		t.Errorf("Expected new-bottleneck after SetInsights, got %s", id)
	}
}

// TestInsightsModelViewRendering verifies View doesn't panic
func TestInsightsModelViewRendering(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"standard", 120, 40},
		{"narrow", 80, 40},
		{"wide", 200, 50},
		{"short", 120, 20},
		{"minimal", 60, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ui.NewInsightsModel(ins, issueMap, theme)
			m.SetSize(tt.width, tt.height)
			// Should not panic
			_ = m.View()
		})
	}
}

// TestInsightsModelAllPanelsRender verifies all panel types render without panic
func TestInsightsModelAllPanelsRender(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Render each panel type
	for i := 0; i < 6; i++ {
		_ = m.View()
		m.NextPanel()
	}
}

// TestInsightsModelEmptyCycles verifies cycles panel with no cycles
func TestInsightsModelEmptyCycles(t *testing.T) {
	theme := createTheme()
	ins := analysis.Insights{
		Bottlenecks: []analysis.InsightItem{{ID: "test", Value: 1.0}},
		Cycles:      [][]string{}, // No cycles
	}
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Navigate to cycles panel
	for i := 0; i < 5; i++ {
		m.NextPanel()
	}

	// SelectedIssueID should return empty for empty cycles
	id := m.SelectedIssueID()
	if id != "" {
		t.Errorf("Expected empty ID for empty cycles, got %s", id)
	}

	// View should not panic
	_ = m.View()
}

// TestInsightsModelMissingIssue verifies handling when issue not in map
func TestInsightsModelMissingIssue(t *testing.T) {
	theme := createTheme()
	ins := analysis.Insights{
		Bottlenecks: []analysis.InsightItem{
			{ID: "missing-issue", Value: 0.5},
		},
	}
	// Issue map doesn't contain "missing-issue"
	issueMap := make(map[string]*model.Issue)

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Should still return the ID even if issue not in map
	id := m.SelectedIssueID()
	if id != "missing-issue" {
		t.Errorf("Expected missing-issue, got %s", id)
	}

	// View should not panic even with missing issue
	_ = m.View()
}

// TestInsightsModelSetSizeBeforeView verifies SetSize must be called before View
func TestInsightsModelSetSizeBeforeView(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	// Don't call SetSize

	// View should return empty string when not ready
	view := m.View()
	if view != "" {
		t.Errorf("Expected empty view before SetSize, got %d chars", len(view))
	}
}

// TestInsightsModelDetailPanel verifies detail panel rendering
func TestInsightsModelDetailPanel(t *testing.T) {
	theme := createTheme()

	// Create issue with full details
	issue := model.Issue{
		ID:                 "detailed-issue",
		Title:              "Detailed Issue Title",
		Description:        "This is a detailed description of the issue.",
		Design:             "Design notes go here.",
		AcceptanceCriteria: "AC: Must work correctly.",
		Notes:              "Additional notes.",
		Status:             model.StatusInProgress,
		IssueType:          model.TypeFeature,
		Priority:           2,
		Assignee:           "testuser",
		Dependencies: []*model.Dependency{
			{DependsOnID: "dep-1", Type: model.DepBlocks},
		},
	}
	issueMap := map[string]*model.Issue{
		"detailed-issue": &issue,
		"dep-1":          {ID: "dep-1", Title: "Dependency One"},
	}

	ins := analysis.Insights{
		Bottlenecks: []analysis.InsightItem{
			{ID: "detailed-issue", Value: 0.75},
		},
		Stats: &analysis.GraphStats{
			Betweenness:       map[string]float64{"detailed-issue": 0.75},
			CriticalPathScore: map[string]float64{"detailed-issue": 3.0},
			Eigenvector:       map[string]float64{"detailed-issue": 0.5},
			Hubs:              map[string]float64{"detailed-issue": 1.0},
			Authorities:       map[string]float64{"detailed-issue": 2.0},
			PageRank:          map[string]float64{"detailed-issue": 0.1},
			InDegree:          map[string]int{"detailed-issue": 2},
			OutDegree:         map[string]int{"detailed-issue": 1},
		},
	}

	m := ui.NewInsightsModel(ins, issueMap, theme)
	// Wide enough to show detail panel
	m.SetSize(150, 40)

	// Should not panic with full details
	_ = m.View()
}

// TestInsightsModelCalculationProofAllPanels verifies calculation proof for each panel type
func TestInsightsModelCalculationProofAllPanels(t *testing.T) {
	theme := createTheme()
	ins := createTestInsights()
	issueMap := createTestIssueMap()

	m := ui.NewInsightsModel(ins, issueMap, theme)
	// Wide enough to show detail panel with calculation proof
	m.SetSize(180, 50)

	// Test each panel's calculation proof
	for i := 0; i < 6; i++ {
		_ = m.View()
		m.NextPanel()
	}
}

// TestInsightsModelLongCycleChain verifies rendering of long cycle chains
func TestInsightsModelLongCycleChain(t *testing.T) {
	theme := createTheme()

	// Create a long cycle
	longCycle := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	ins := analysis.Insights{
		Cycles: [][]string{longCycle},
	}

	// Create issue map with all cycle members
	issueMap := make(map[string]*model.Issue)
	for _, id := range longCycle {
		issueMap[id] = &model.Issue{ID: id, Title: "Cycle member " + id}
	}

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 40)

	// Navigate to cycles panel
	for i := 0; i < 5; i++ {
		m.NextPanel()
	}

	// View should not panic with long cycle
	_ = m.View()
}

// TestInsightsModelScrolling verifies scrolling behavior with many items
func TestInsightsModelScrolling(t *testing.T) {
	theme := createTheme()

	// Create many bottleneck items
	var bottlenecks []analysis.InsightItem
	issueMap := make(map[string]*model.Issue)
	for i := 0; i < 50; i++ {
		id := string(rune('A' + i%26))
		if i >= 26 {
			id = id + string(rune('A'+i%26))
		}
		bottlenecks = append(bottlenecks, analysis.InsightItem{ID: id, Value: float64(50 - i)})
		issueMap[id] = &model.Issue{ID: id, Title: "Issue " + id}
	}

	ins := analysis.Insights{
		Bottlenecks: bottlenecks,
	}

	m := ui.NewInsightsModel(ins, issueMap, theme)
	m.SetSize(120, 30) // Short height to trigger scrolling

	// Navigate down through all items
	for i := 0; i < 55; i++ {
		m.MoveDown()
		_ = m.View() // Scrolling happens during view
	}

	// Navigate back up
	for i := 0; i < 55; i++ {
		m.MoveUp()
		_ = m.View()
	}
}
