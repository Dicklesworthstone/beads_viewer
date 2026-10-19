package ui_test

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/charmbracelet/x/ansi"
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
		Cores: []analysis.InsightItem{
			{ID: "core-1", Value: 3},
			{ID: "core-2", Value: 2},
		},
		Articulation: []string{"art-1"},
		Slack: []analysis.InsightItem{
			{ID: "slack-1", Value: 4},
			{ID: "slack-2", Value: 2},
		},
		Cycles: [][]string{
			{"cycle-a", "cycle-b", "cycle-c"},
			{"cycle-x", "cycle-y"},
		},
		ClusterDensity: 0.42,
		Stats: analysis.NewGraphStatsForTest(
			map[string]float64{"bottleneck-1": 0.15},                       // pageRank
			map[string]float64{"bottleneck-1": 0.85, "bottleneck-2": 0.65}, // betweenness
			map[string]float64{"influencer-1": 0.92},                       // eigenvector
			map[string]float64{"hub-1": 2.5, "hub-2": 1.8},                 // hubs
			map[string]float64{"auth-1": 3.2},                              // authorities
			map[string]float64{"keystone-1": 5.0, "keystone-2": 3.0},       // criticalPathScore
			map[string]int{"bottleneck-1": 2},                              // outDegree
			map[string]int{"bottleneck-1": 3},                              // inDegree
			nil,                                                            // cycles
			0,                                                              // density
			nil,                                                            // topologicalOrder
		),
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
		{ID: "core-1", Title: "Core Node 1", Status: model.StatusOpen},
		{ID: "core-2", Title: "Core Node 2", Status: model.StatusOpen},
		{ID: "art-1", Title: "Articulation", Status: model.StatusOpen},
		{ID: "slack-1", Title: "Slack Node 1", Status: model.StatusOpen},
		{ID: "slack-2", Title: "Slack Node 2", Status: model.StatusOpen},
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

	// NextPanel to Cores
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "core-1" {
		t.Errorf("Expected core-1 after NextPanel, got %s", id)
	}

	// NextPanel to Articulation
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "art-1" {
		t.Errorf("Expected art-1 after NextPanel, got %s", id)
	}

	// NextPanel to Slack
	m.NextPanel()
	id = m.SelectedIssueID()
	if id != "slack-1" {
		t.Errorf("Expected slack-1 after NextPanel, got %s", id)
	}

	// PrevPanel should go back to Articulation
	m.PrevPanel()
	id = m.SelectedIssueID()
	if id != "art-1" {
		t.Errorf("Expected art-1 after PrevPanel, got %s", id)
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

	// Navigate to Cycles panel (8 NextPanels from start)
	for i := 0; i < 8; i++ {
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

	// Toggle heatmap view (bv-95) - should not panic
	m.ToggleHeatmap()
	_ = m.View()
	m.ToggleHeatmap()
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
		Stats: analysis.NewGraphStatsForTest(
			map[string]float64{"detailed-issue": 0.1},  // pageRank
			map[string]float64{"detailed-issue": 0.75}, // betweenness
			map[string]float64{"detailed-issue": 0.5},  // eigenvector
			map[string]float64{"detailed-issue": 1.0},  // hubs
			map[string]float64{"detailed-issue": 2.0},  // authorities
			map[string]float64{"detailed-issue": 3.0},  // criticalPathScore
			map[string]int{"detailed-issue": 1},        // outDegree
			map[string]int{"detailed-issue": 2},        // inDegree
			nil, 0, nil,
		),
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

// gh214DetailModel builds an insights model whose first bottleneck has a long
// description, so its rendered width shows where the detail markdown wraps.
func gh214DetailModel(t *testing.T) ui.InsightsModel {
	t.Helper()
	issue := model.Issue{
		ID:          "wide-1",
		Title:       "Detail width",
		Description: strings.Repeat("alpha beta gamma delta epsilon ", 12),
		Status:      model.StatusInProgress,
		IssueType:   model.TypeTask,
		Priority:    1,
		Assignee:    "someone",
	}
	m := ui.NewInsightsModel(analysis.Insights{
		Bottlenecks: []analysis.InsightItem{{ID: issue.ID, Value: 1}},
	}, map[string]*model.Issue{issue.ID: &issue}, createTheme())
	m.SetSize(200, 50)
	return m
}

// detailPanelLeftEdge returns the column of the detail panel's top-left
// corner: the last rounded corner on the view's first panel line.
func detailPanelLeftEdge(t *testing.T, view string) int {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if idx := strings.LastIndex(line, "╭"); idx >= 0 {
			return ansi.StringWidth(line[:idx])
		}
	}
	t.Fatalf("no panel corner in view:\n%s", ansi.Strip(view))
	return 0
}

// TestInsightsDetailPanelUsesItsOwnWidth_GH214: the detail markdown wraps at
// the detail viewport's width, not at the metric column width the panel
// explanations were last rendered with, and the meta fields are compact
// "Field: value" lines instead of an evenly stretched two-column table.
func TestInsightsDetailPanelUsesItsOwnWidth_GH214(t *testing.T) {
	m := gh214DetailModel(t)
	view := ansi.Strip(m.View())
	left := detailPanelLeftEdge(t, view)

	longest := 0
	sawStatus := false
	for _, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > 200 {
			t.Fatalf("view line is %d columns wide, over the 200 given:\n%s", w, line)
		}
		if ansi.StringWidth(line) <= left {
			continue
		}
		detail := strings.Trim(ansi.Cut(line, left, 200), " │╭╮╰╯─")
		if strings.Contains(detail, "Status: IN_PROGRESS") {
			sawStatus = true
			// Each field has its own line, sized to its content.
			if strings.Contains(detail, "ID:") || strings.Contains(detail, "Priority:") {
				t.Fatalf("meta fields share a line: %q", detail)
			}
		}
		if strings.Contains(detail, "alpha") {
			longest = max(longest, ansi.StringWidth(strings.TrimSpace(detail)))
		}
	}
	if !sawStatus {
		t.Fatalf("detail panel has no %q line:\n%s", "Status: IN_PROGRESS", view)
	}
	// The panel is 66 columns wide (a third of 200), so its content wraps at
	// 62. The metric explanations wrap near 36 columns.
	if longest < 50 {
		t.Fatalf("description wraps at %d columns; want the detail content width (~62):\n%s", longest, view)
	}
}

// TestInsightsDetailPanelResize_GH214: `<` widens and `>` narrows the detail
// panel within bounds, like the list/detail split (#89).
func TestInsightsDetailPanelResize_GH214(t *testing.T) {
	m := gh214DetailModel(t)
	initial := detailPanelLeftEdge(t, m.View())

	m.WidenDetailPanel()
	wider := detailPanelLeftEdge(t, m.View())
	if wider >= initial {
		t.Fatalf("WidenDetailPanel: left edge %d, want < %d", wider, initial)
	}

	m.NarrowDetailPanel()
	m.NarrowDetailPanel()
	narrower := detailPanelLeftEdge(t, m.View())
	if narrower <= initial {
		t.Fatalf("NarrowDetailPanel: left edge %d, want > %d", narrower, initial)
	}

	// Bounds: the panel never shrinks below a usable width or crowds out the
	// metric columns.
	for i := 0; i < 40; i++ {
		m.NarrowDetailPanel()
	}
	if edge := detailPanelLeftEdge(t, m.View()); 200-edge < 30 {
		t.Fatalf("detail panel narrowed to %d columns", 200-edge)
	}
	for i := 0; i < 40; i++ {
		m.WidenDetailPanel()
	}
	if edge := detailPanelLeftEdge(t, m.View()); edge < 60 {
		t.Fatalf("metric columns squeezed to %d columns", edge)
	}
}
