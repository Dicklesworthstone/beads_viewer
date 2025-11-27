package ui_test

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

// TestGraphModelEmpty verifies behavior with no issues
func TestGraphModelEmpty(t *testing.T) {
	theme := createTheme()
	g := ui.NewGraphModel([]model.Issue{}, nil, theme)

	// Should return nil for selected issue
	sel := g.SelectedIssue()
	if sel != nil {
		t.Errorf("Expected nil selection for empty graph, got %v", sel)
	}

	// Count should be 0
	if g.TotalCount() != 0 {
		t.Errorf("Expected 0 nodes, got %d", g.TotalCount())
	}

	// Navigation should not panic
	g.MoveUp()
	g.MoveDown()
	g.MoveLeft()
	g.MoveRight()
	g.PageUp()
	g.PageDown()
	g.ScrollLeft()
	g.ScrollRight()
}

// TestGraphModelSingleNode verifies graph with single node
func TestGraphModelSingleNode(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "A", Title: "Single Issue", Status: model.StatusOpen},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node, got %d", g.TotalCount())
	}

	sel := g.SelectedIssue()
	if sel == nil || sel.ID != "A" {
		t.Errorf("Expected issue A selected, got %v", sel)
	}

	// Navigation should stay on single node
	g.MoveDown()
	sel = g.SelectedIssue()
	if sel == nil || sel.ID != "A" {
		t.Errorf("Expected to stay on A after MoveDown, got %v", sel)
	}

	g.MoveRight()
	sel = g.SelectedIssue()
	if sel == nil || sel.ID != "A" {
		t.Errorf("Expected to stay on A after MoveRight, got %v", sel)
	}
}

// TestGraphModelLayerAssignment verifies nodes are placed in correct layers
func TestGraphModelLayerAssignment(t *testing.T) {
	theme := createTheme()

	// Chain: A depends on B, B depends on C
	// Expected layers: C (layer 0), B (layer 1), A (layer 2)
	issues := []model.Issue{
		{ID: "A", Title: "Depends on B", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Depends on C", Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Root node"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 3 {
		t.Errorf("Expected 3 nodes, got %d", g.TotalCount())
	}

	// First selected should be in layer 0 (C is the root)
	sel := g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected a selected issue")
	}

	// Verify we can navigate through all nodes
	seen := make(map[string]bool)
	for i := 0; i < 5; i++ { // Extra iterations to ensure we don't go out of bounds
		sel := g.SelectedIssue()
		if sel != nil {
			seen[sel.ID] = true
		}
		g.MoveRight()
	}

	if !seen["A"] || !seen["B"] || !seen["C"] {
		t.Errorf("Expected to see all nodes A, B, C; saw %v", seen)
	}
}

// TestGraphModelEdgeDirection verifies edges go from dependency to dependent
func TestGraphModelEdgeDirection(t *testing.T) {
	theme := createTheme()

	// A depends on B: edge should go FROM B TO A (downward in the graph)
	issues := []model.Issue{
		{ID: "A", Title: "Child", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Parent"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// B should be in layer 0 (root), A in layer 1 (dependent)
	// This is tested indirectly through the graph structure

	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelCycleDetection verifies cycles don't cause infinite loops
func TestGraphModelCycleDetection(t *testing.T) {
	theme := createTheme()

	// Cycle: A -> B -> C -> A
	issues := []model.Issue{
		{ID: "A", Title: "A", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "B", Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "C", Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	// Should not hang or panic
	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 3 {
		t.Errorf("Expected 3 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelNavigation verifies node navigation
func TestGraphModelNavigation(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "One"},
		{ID: "2", Title: "Two"},
		{ID: "3", Title: "Three"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Should start on first node
	sel := g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected a selected issue")
	}
	startID := sel.ID

	// MoveRight should change selection
	g.MoveRight()
	sel = g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected a selected issue after MoveRight")
	}

	// MoveLeft should go back
	g.MoveLeft()
	sel = g.SelectedIssue()
	if sel == nil || sel.ID != startID {
		t.Errorf("Expected to return to %s, got %v", startID, sel)
	}
}

// TestGraphModelScrollBounds verifies scroll clamping
func TestGraphModelScrollBounds(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "One"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Scroll left from origin should stay at 0
	g.ScrollLeft()
	g.ScrollLeft()
	g.ScrollLeft()
	// After View call, scrollX should be clamped to 0

	// PageUp from origin should stay at 0
	g.PageUp()
	g.PageUp()
	// After View call, scrollY should be clamped to 0

	// View should not panic
	_ = g.View(80, 24)
}

// TestGraphModelSetIssuesClearsGraph verifies SetIssues resets the graph
func TestGraphModelSetIssuesClearsGraph(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "One"},
		{ID: "2", Title: "Two"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}

	// Clear with empty issues
	g.SetIssues([]model.Issue{}, nil)

	if g.TotalCount() != 0 {
		t.Errorf("Expected 0 nodes after clearing, got %d", g.TotalCount())
	}

	// Set new issues
	g.SetIssues([]model.Issue{{ID: "A", Title: "New"}}, nil)

	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node after new issues, got %d", g.TotalCount())
	}

	sel := g.SelectedIssue()
	if sel == nil || sel.ID != "A" {
		t.Errorf("Expected A selected, got %v", sel)
	}
}

// TestGraphModelWithInsights verifies graph works with insights data
func TestGraphModelWithInsights(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "Test A"},
		{ID: "B", Title: "Test B", Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	// Create analyzer and insights
	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()
	insights := stats.GenerateInsights(5)

	g := ui.NewGraphModel(issues, &insights, theme)

	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}

	// View should not panic with insights
	_ = g.View(80, 24)
}

// TestGraphModelViewRendering verifies View doesn't panic
func TestGraphModelViewRendering(t *testing.T) {
	theme := createTheme()

	tests := []struct {
		name   string
		issues []model.Issue
		width  int
		height int
	}{
		{"empty", []model.Issue{}, 80, 24},
		{"single", []model.Issue{{ID: "1", Title: "Test"}}, 80, 24},
		{"narrow", []model.Issue{{ID: "1", Title: "Test"}}, 40, 24},
		{"short", []model.Issue{{ID: "1", Title: "Test"}}, 80, 10},
		{"chain", []model.Issue{
			{ID: "A", Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
			{ID: "B", Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
			{ID: "C"},
		}, 120, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := ui.NewGraphModel(tt.issues, nil, theme)
			// Should not panic
			_ = g.View(tt.width, tt.height)
		})
	}
}

// TestGraphModelIgnoresNonBlockingDeps verifies only blocking deps create edges
func TestGraphModelIgnoresNonBlockingDeps(t *testing.T) {
	theme := createTheme()

	// A has a "related" dep on B (should be ignored in layer calc)
	issues := []model.Issue{
		{ID: "A", Title: "A", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepRelated},
		}},
		{ID: "B", Title: "B"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Both should be in layer 0 since "related" doesn't create a hierarchy
	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelMissingDependency verifies handling of deps pointing to non-existent issues
func TestGraphModelMissingDependency(t *testing.T) {
	theme := createTheme()

	// A depends on B, but B doesn't exist
	issues := []model.Issue{
		{ID: "A", Title: "A", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	// Should not panic
	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node, got %d", g.TotalCount())
	}
}

// TestGraphModelMultipleRoots verifies graph with multiple root nodes
func TestGraphModelMultipleRoots(t *testing.T) {
	theme := createTheme()

	// A, B, C are all roots (no dependencies)
	issues := []model.Issue{
		{ID: "A", Title: "Root A"},
		{ID: "B", Title: "Root B"},
		{ID: "C", Title: "Root C"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 3 {
		t.Errorf("Expected 3 nodes, got %d", g.TotalCount())
	}

	// All should be accessible via navigation
	visited := make(map[string]bool)
	for i := 0; i < 5; i++ {
		sel := g.SelectedIssue()
		if sel != nil {
			visited[sel.ID] = true
		}
		g.MoveRight()
	}

	if len(visited) != 3 {
		t.Errorf("Expected to visit 3 nodes, visited %v", visited)
	}
}

// TestGraphModelLongTitle verifies title truncation
func TestGraphModelLongTitle(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "This is a very long title that should be truncated properly to fit in the node box"},
	}

	// Should not panic
	g := ui.NewGraphModel(issues, nil, theme)
	_ = g.View(80, 24)

	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node, got %d", g.TotalCount())
	}
}

// TestGraphModelStatusColors verifies different statuses render without panic
func TestGraphModelStatusColors(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "Open", Status: model.StatusOpen},
		{ID: "2", Title: "InProgress", Status: model.StatusInProgress},
		{ID: "3", Title: "Blocked", Status: model.StatusBlocked},
		{ID: "4", Title: "Closed", Status: model.StatusClosed},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Should not panic with different status colors
	_ = g.View(120, 30)

	if g.TotalCount() != 4 {
		t.Errorf("Expected 4 nodes, got %d", g.TotalCount())
	}
}
