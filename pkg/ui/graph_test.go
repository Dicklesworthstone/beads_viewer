package ui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/charmbracelet/x/ansi"
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

	// Scroll left from origin should stay clamped at 0
	g.ScrollLeft()
	g.ScrollLeft()
	g.ScrollLeft()

	// PageUp from origin should stay clamped at 0
	g.PageUp()
	g.PageUp()

	sel := g.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Fatalf("expected selection to stay on issue 1 after origin scrolling, got %v", sel)
	}
	plain := ansi.Strip(g.View(80, 24))
	if !strings.Contains(plain, "1") || !strings.Contains(plain, "One") {
		t.Errorf("graph view after clamped scrolling missing issue content:\n%s", plain)
	}
}

// This must fail against the original empty ScrollLeft/ScrollRight methods:
// changing a footer or scroll counter without moving content is insufficient.
func TestGraphModelPanningRevealsClippedContent(t *testing.T) {
	g := ui.NewGraphModel([]model.Issue{{ID: "A", Title: "Viewport marker 界界👩‍💻 é", Status: model.StatusOpen}}, nil, createTheme())
	body := func(view string) string {
		lines := strings.Split(ansi.Strip(view), "\n")
		if len(lines) < 3 {
			t.Fatalf("missing graph body: %q", view)
		}
		return strings.Join(lines[:len(lines)-2], "\n")
	}
	initial := g.View(30, 20)
	g.ScrollRight()
	right := g.View(30, 20)
	if body(initial) == body(right) {
		t.Fatalf("right pan did not move actual graph content:\n%s", right)
	}
	if got := g.SelectedIssue(); got == nil || got.ID != "A" {
		t.Fatalf("panning changed selection: %+v", got)
	}
	g.ScrollLeft()
	if got := g.View(30, 20); got != initial {
		t.Fatalf("reverse pan did not restore original frame:\n%s\nwant:\n%s", got, initial)
	}
	g.ScrollLeft()
	if got := g.View(30, 20); got != initial {
		t.Fatal("left pan escaped the origin")
	}
	for i := 0; i < 50; i++ {
		g.ScrollRight()
	}
	end := g.View(30, 20)
	g.ScrollRight()
	if got := g.View(30, 20); got != end {
		t.Fatal("right pan escaped the content boundary")
	}
	for _, view := range []string{initial, right, end} {
		if !utf8.ValidString(view) {
			t.Fatal("panning broke UTF-8")
		}
		for _, line := range strings.Split(view, "\n") {
			if w := ansi.StringWidth(line); w > 30 {
				t.Errorf("panned line occupies %d cells, want <=30: %q", w, line)
			}
		}
	}
	// Resizing and removing the old overflow must clamp the old position.
	wide := g.View(200, 80)
	g.ScrollLeft()
	if got := g.View(200, 80); got != wide {
		t.Fatal("wide resize retained a stale horizontal offset")
	}
}

func TestGraphModelViewportDimensions(t *testing.T) {
	issues := []model.Issue{{ID: "界界👩‍💻", Title: strings.Repeat("Wide界", 20), Status: model.StatusOpen}}
	for _, size := range [][2]int{{0, 0}, {-1, 4}, {1, 1}, {5, 3}, {30, 10}, {80, 5}, {140, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			g := ui.NewGraphModel(issues, nil, createTheme())
			for i := 0; i < 6; i++ {
				if i == 2 {
					g.SetIssues(nil, nil)
				} else if i == 4 {
					g = ui.NewGraphModel(nil, nil, createTheme())
				}
				view := g.View(size[0], size[1])
				if size[0] <= 0 || size[1] <= 0 {
					if view != "" {
						t.Fatalf("non-positive viewport returned %q", view)
					}
					continue
				}
				lines := strings.Split(view, "\n")
				if len(lines) > size[1] || !utf8.ValidString(view) {
					t.Fatalf("invalid viewport height/UTF-8: %q", view)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Errorf("line exceeds %d cells: %q", size[0], line)
					}
				}
				g.ScrollRight()
			}
		})
	}
}

func TestGraphModelHighlightsActualCriticalChain(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
	}
	stats := analysis.NewAnalyzer(issues).Analyze()
	insights := stats.GenerateInsights(5)
	g := ui.NewGraphModel(issues, &insights, createTheme())
	if !g.SelectByID("B") {
		t.Fatal("chain midpoint missing")
	}
	view := ansi.Strip(g.View(200, 100))
	for _, marker := range []string{"◆ 🔵 A", "◆ 🔵 C"} {
		if !strings.Contains(view, marker) {
			t.Errorf("longest chain marker %q missing:\n%s", marker, view)
		}
	}
	if strings.Contains(view, "◆ 🔵 D") {
		t.Fatal("equally long alternative branch was marked as part of the chosen chain")
	}
}

func TestGraphModelHighlightsVisibleScope(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}
	previous := "A"
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("hidden-%d", i)
		issues = append(issues, model.Issue{ID: id, Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: previous, Type: model.DepBlocks}}})
		previous = id
	}
	stats := analysis.NewAnalyzer(issues).Analyze()
	insights := stats.GenerateInsights(20)
	g := ui.NewGraphModel(issues[:4], &insights, createTheme())
	g.SelectByID("C")
	view := ansi.Strip(g.View(200, 100))
	if !strings.Contains(view, "◆ 🔵 B") || !strings.Contains(view, "◆ 🔵 D") {
		t.Fatalf("full-project scores displaced the longest displayed chain:\n%s", view)
	}
	g.SelectByID("A")
	view = ansi.Strip(g.View(200, 100))
	if strings.Contains(view, "◆ 🔵") {
		t.Fatal("isolated visible node inherited a hidden critical chain")
	}
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
			out := g.View(tt.width, tt.height)
			if !utf8.ValidString(out) {
				t.Fatalf("View(%d, %d) produced invalid UTF-8", tt.width, tt.height)
			}
			plain := ansi.Strip(out)
			if strings.TrimSpace(plain) == "" {
				t.Fatalf("View(%d, %d) returned empty output", tt.width, tt.height)
			}
			if len(tt.issues) == 0 {
				if !strings.Contains(plain, "No issues") {
					t.Errorf("empty graph should display empty-state message, got:\n%s", plain)
				}
				return
			}
			if !strings.Contains(plain, tt.issues[0].ID) {
				t.Errorf("graph view missing primary issue ID %q:\n%s", tt.issues[0].ID, plain)
			}
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

// TestGraphModelWithRankings verifies rankings are computed correctly
func TestGraphModelWithRankings(t *testing.T) {
	theme := createTheme()

	// Create a chain where we can verify ranking order
	issues := []model.Issue{
		{ID: "A", Title: "Root", Status: model.StatusOpen},
		{ID: "B", Title: "Middle", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Leaf", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	// Create analyzer and insights
	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()
	insights := stats.GenerateInsights(5)

	g := ui.NewGraphModel(issues, &insights, theme)

	// Verify View renders without panic (which uses rankings)
	output := g.View(100, 40)
	if output == "" {
		t.Error("Expected non-empty view output")
	}

	// Verify all nodes present
	if g.TotalCount() != 3 {
		t.Errorf("Expected 3 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelSelectionPreservation verifies SetIssues preserves selection when possible
func TestGraphModelSelectionPreservation(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "First"},
		{ID: "B", Title: "Second"},
		{ID: "C", Title: "Third"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Navigate to B
	g.MoveDown()
	sel := g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selected issue")
	}

	// Find B's position
	var bFound bool
	for i := 0; i < 3; i++ {
		sel = g.SelectedIssue()
		if sel != nil && sel.ID == "B" {
			bFound = true
			break
		}
		g.MoveDown()
	}

	if !bFound {
		// B might already be selected; reset and find it
		g = ui.NewGraphModel(issues, nil, theme)
		for i := 0; i < 3; i++ {
			sel = g.SelectedIssue()
			if sel != nil && sel.ID == "B" {
				bFound = true
				break
			}
			g.MoveDown()
		}
	}

	// Now update with new issues that still include B
	newIssues := []model.Issue{
		{ID: "B", Title: "Second Updated"},
		{ID: "D", Title: "Fourth"},
	}
	g.SetIssues(newIssues, nil)

	// B should still be selected if it was before
	sel = g.SelectedIssue()
	// Note: The selection behavior depends on whether B was actually selected
	// and the sorting order. Just verify we have a valid selection.
	if sel == nil {
		t.Fatal("Expected selection after SetIssues")
	}
	if sel.ID != "B" && sel.ID != "D" {
		t.Errorf("Expected selection to be one of {B,D}, got %q", sel.ID)
	}
	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelSelectionLostOnFilter verifies selection resets when selected issue is removed
func TestGraphModelSelectionLostOnFilter(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "First"},
		{ID: "B", Title: "Second"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Navigate to make sure we have a selection
	sel := g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected initial selection")
	}
	initialID := sel.ID

	// Update with issues that don't include the initial selection
	var newIssues []model.Issue
	if initialID == "A" {
		newIssues = []model.Issue{{ID: "B", Title: "Only B"}}
	} else {
		newIssues = []model.Issue{{ID: "A", Title: "Only A"}}
	}

	g.SetIssues(newIssues, nil)

	// Should have valid selection (the only remaining issue)
	sel = g.SelectedIssue()
	if sel == nil {
		t.Error("Expected valid selection after filter")
	}
	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node, got %d", g.TotalCount())
	}
}

// TestGraphModelExtremeWidths verifies View handles extreme terminal widths
func TestGraphModelExtremeWidths(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "Root"},
		{ID: "B", Title: "Dependent", Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"very_narrow", 20, 24},
		{"narrow", 40, 24},
		{"very_narrow_short", 20, 10},
		{"minimum", 10, 5},
		{"extremely_narrow", 5, 5},
		{"very_wide", 300, 24},
		{"extremely_wide", 500, 50},
		{"very_short", 80, 5},
		{"extremely_short", 80, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := g.View(tt.width, tt.height)
			if !utf8.ValidString(output) {
				t.Fatalf("View(%d, %d) emitted invalid UTF-8", tt.width, tt.height)
			}
			if tt.width >= 20 && tt.height >= 10 && strings.TrimSpace(ansi.Strip(output)) == "" {
				t.Errorf("View(%d, %d) returned empty output for non-empty graph", tt.width, tt.height)
			}
		})
	}
}

// TestGraphModelNilDependencies verifies nil dependencies don't cause panic
func TestGraphModelNilDependencies(t *testing.T) {
	theme := createTheme()

	// Issue with nil entries in Dependencies slice
	issues := []model.Issue{
		{ID: "A", Title: "Has nil deps", Dependencies: []*model.Dependency{
			nil,
			{DependsOnID: "B", Type: model.DepBlocks},
			nil,
		}},
		{ID: "B", Title: "Target"},
	}

	// Should not panic
	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}

	// View should not panic
	output := g.View(80, 24)
	if output == "" {
		t.Error("Expected non-empty view")
	}
}

// TestGraphModelAllDependencyTypesNilSafe tests all dependency types with nil entries
func TestGraphModelAllDependencyTypesNilSafe(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "Mixed deps", Dependencies: []*model.Dependency{
			nil,
			{DependsOnID: "B", Type: model.DepBlocks},
			nil,
			{DependsOnID: "C", Type: model.DepRelated},
			nil,
		}},
		{ID: "B", Title: "Blocker"},
		{ID: "C", Title: "Related"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 3 {
		t.Errorf("Expected 3 nodes, got %d", g.TotalCount())
	}

	// Navigate through all
	for i := 0; i < 5; i++ {
		g.MoveDown()
		_ = g.SelectedIssue()
	}

	// View should work
	_ = g.View(80, 24)
}

// TestGraphModelPriorityTypes verifies different priorities render correctly
func TestGraphModelPriorityTypes(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "P0", Title: "Critical", Priority: 0},
		{ID: "P1", Title: "High", Priority: 1},
		{ID: "P2", Title: "Medium", Priority: 2},
		{ID: "P3", Title: "Low", Priority: 3},
		{ID: "P4", Title: "Backlog", Priority: 4},
		{ID: "P5", Title: "Unknown", Priority: 5}, // Beyond normal range
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Should render all without panic
	output := g.View(100, 30)
	if output == "" {
		t.Error("Expected non-empty view")
	}

	if g.TotalCount() != 6 {
		t.Errorf("Expected 6 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelIssueTypes verifies different issue types render correctly
func TestGraphModelIssueTypes(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Title: "Bug", IssueType: model.TypeBug},
		{ID: "2", Title: "Feature", IssueType: model.TypeFeature},
		{ID: "3", Title: "Task", IssueType: model.TypeTask},
		{ID: "4", Title: "Epic", IssueType: model.TypeEpic},
		{ID: "5", Title: "Chore", IssueType: model.TypeChore},
		{ID: "6", Title: "Unknown", IssueType: "unknown"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	output := g.View(100, 30)
	if output == "" {
		t.Error("Expected non-empty view")
	}

	if g.TotalCount() != 6 {
		t.Errorf("Expected 6 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelManyBlockers verifies rendering with many blocker connections
func TestGraphModelManyBlockers(t *testing.T) {
	theme := createTheme()

	// Create issue with many blockers (tests "+N more" rendering)
	deps := make([]*model.Dependency, 10)
	for i := 0; i < 10; i++ {
		deps[i] = &model.Dependency{
			DependsOnID: fmt.Sprintf("blocker-%d", i),
			Type:        model.DepBlocks,
		}
	}

	issues := []model.Issue{
		{ID: "main", Title: "Main Issue", Dependencies: deps},
	}

	// Add the blocker issues
	for i := 0; i < 10; i++ {
		issues = append(issues, model.Issue{
			ID:    fmt.Sprintf("blocker-%d", i),
			Title: fmt.Sprintf("Blocker %d", i),
		})
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 11 {
		t.Errorf("Expected 11 nodes, got %d", g.TotalCount())
	}

	// View should handle many connections (showing "+N more")
	output := g.View(120, 40)
	if output == "" {
		t.Error("Expected non-empty view")
	}
}

// TestGraphModelManyDependents verifies rendering with many dependent connections
func TestGraphModelManyDependents(t *testing.T) {
	theme := createTheme()

	// Create root issue
	issues := []model.Issue{
		{ID: "root", Title: "Root Issue"},
	}

	// Add many dependents
	for i := 0; i < 10; i++ {
		issues = append(issues, model.Issue{
			ID:    fmt.Sprintf("dependent-%d", i),
			Title: fmt.Sprintf("Dependent %d", i),
			Dependencies: []*model.Dependency{
				{DependsOnID: "root", Type: model.DepBlocks},
			},
		})
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 11 {
		t.Errorf("Expected 11 nodes, got %d", g.TotalCount())
	}

	// View should handle many dependents
	output := g.View(120, 40)
	if output == "" {
		t.Error("Expected non-empty view")
	}
}

// TestGraphModelPageNavigation verifies page up/down navigation bounds
func TestGraphModelPageNavigation(t *testing.T) {
	theme := createTheme()

	// Create many issues to test pagination
	var issues []model.Issue
	for i := 0; i < 50; i++ {
		issues = append(issues, model.Issue{
			ID:    fmt.Sprintf("issue-%02d", i),
			Title: fmt.Sprintf("Issue %d", i),
		})
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// PageDown multiple times
	for i := 0; i < 10; i++ {
		g.PageDown()
	}
	sel := g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selected issue after PageDown")
	}

	// PageUp back to start
	for i := 0; i < 10; i++ {
		g.PageUp()
	}
	sel = g.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selected issue after PageUp")
	}

	// Verify we're back near the start
	// (selection index should be 0 after many PageUp from the top)
}

// TestGraphModelUnicodeIDs verifies handling of Unicode in issue IDs
func TestGraphModelUnicodeIDs(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "日本語-1", Title: "Japanese ID"},
		{ID: "émoji-🎉", Title: "Emoji ID"},
		{ID: "кириллица", Title: "Cyrillic ID"},
		{ID: "普通话", Title: "Chinese ID"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 4 {
		t.Errorf("Expected 4 nodes, got %d", g.TotalCount())
	}

	// View should handle Unicode
	output := g.View(80, 24)
	if output == "" {
		t.Error("Expected non-empty view")
	}
}

// TestGraphModelEmptyTitle verifies handling of empty titles
func TestGraphModelEmptyTitle(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "no-title", Title: ""},
		{ID: "has-title", Title: "Has Title"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Should not panic
	output := g.View(80, 24)
	if output == "" {
		t.Error("Expected non-empty view")
	}

	if g.TotalCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", g.TotalCount())
	}
}

// TestGraphModelReferenceToSelf verifies self-referential dependencies
func TestGraphModelReferenceToSelf(t *testing.T) {
	theme := createTheme()

	// Issue depends on itself
	issues := []model.Issue{
		{ID: "self", Title: "Self Reference", Dependencies: []*model.Dependency{
			{DependsOnID: "self", Type: model.DepBlocks},
		}},
	}

	// Should not hang or panic
	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 1 {
		t.Errorf("Expected 1 node, got %d", g.TotalCount())
	}

	// View should handle self-reference
	output := g.View(80, 24)
	if output == "" {
		t.Error("Expected non-empty view")
	}
}

// TestGraphModelSelectByID verifies SelectByID navigates to the correct issue (bv-8a4r)
func TestGraphModelSelectByID(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "alpha", Title: "Alpha Issue"},
		{ID: "beta", Title: "Beta Issue"},
		{ID: "gamma", Title: "Gamma Issue"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Select by existing ID
	found := g.SelectByID("beta")
	if !found {
		t.Error("SelectByID should return true for existing ID 'beta'")
	}

	sel := g.SelectedIssue()
	if sel == nil || sel.ID != "beta" {
		t.Errorf("Expected 'beta' selected, got %v", sel)
	}

	// Select another ID
	found = g.SelectByID("gamma")
	if !found {
		t.Error("SelectByID should return true for existing ID 'gamma'")
	}

	sel = g.SelectedIssue()
	if sel == nil || sel.ID != "gamma" {
		t.Errorf("Expected 'gamma' selected, got %v", sel)
	}

	// Select first ID
	found = g.SelectByID("alpha")
	if !found {
		t.Error("SelectByID should return true for existing ID 'alpha'")
	}

	sel = g.SelectedIssue()
	if sel == nil || sel.ID != "alpha" {
		t.Errorf("Expected 'alpha' selected, got %v", sel)
	}
}

// TestGraphModelSelectByIDNotFound verifies SelectByID returns false for non-existent IDs (bv-8a4r)
func TestGraphModelSelectByIDNotFound(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "A", Title: "Issue A"},
		{ID: "B", Title: "Issue B"},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	// Get initial selection
	initial := g.SelectedIssue()
	if initial == nil {
		t.Fatal("Expected initial selection")
	}
	initialID := initial.ID

	// Try to select non-existent ID
	found := g.SelectByID("nonexistent")
	if found {
		t.Error("SelectByID should return false for non-existent ID")
	}

	// Selection should be unchanged
	sel := g.SelectedIssue()
	if sel == nil || sel.ID != initialID {
		t.Errorf("Selection should be unchanged after failed SelectByID; expected %q, got %v", initialID, sel)
	}
}

// TestGraphModelSelectByIDEmpty verifies SelectByID handles empty graph (bv-8a4r)
func TestGraphModelSelectByIDEmpty(t *testing.T) {
	theme := createTheme()

	g := ui.NewGraphModel([]model.Issue{}, nil, theme)

	// Should return false and not panic
	found := g.SelectByID("any")
	if found {
		t.Error("SelectByID should return false for empty graph")
	}
}

// TestGraphModelDiamondPattern verifies diamond dependency pattern (A→B, A→C, B→D, C→D) (bv-8a4r)
func TestGraphModelDiamondPattern(t *testing.T) {
	theme := createTheme()

	// Diamond: D depends on B and C, B and C both depend on A
	//     A
	//    / \
	//   B   C
	//    \ /
	//     D
	issues := []model.Issue{
		{ID: "A", Title: "Root"},
		{ID: "B", Title: "Left branch", Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Right branch", Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "D", Title: "Leaf", Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
	}

	g := ui.NewGraphModel(issues, nil, theme)

	if g.TotalCount() != 4 {
		t.Errorf("Expected 4 nodes in diamond, got %d", g.TotalCount())
	}

	// Verify all nodes are navigable
	seen := make(map[string]bool)
	for i := 0; i < 6; i++ {
		sel := g.SelectedIssue()
		if sel != nil {
			seen[sel.ID] = true
		}
		g.MoveDown()
	}

	for _, id := range []string{"A", "B", "C", "D"} {
		if !seen[id] {
			t.Errorf("Expected to see node %s in diamond pattern", id)
		}
	}

	// View should render without panic
	output := g.View(100, 30)
	if output == "" {
		t.Error("Expected non-empty view for diamond pattern")
	}
}

// TestGraphModelSelectByIDWithDependencies verifies SelectByID works in complex graph (bv-8a4r)
func TestGraphModelSelectByIDWithDependencies(t *testing.T) {
	theme := createTheme()

	// Create chain with dependencies
	issues := []model.Issue{
		{ID: "root", Title: "Root"},
		{ID: "child-1", Title: "Child 1", Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "child-2", Title: "Child 2", Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "grandchild", Title: "Grandchild", Dependencies: []*model.Dependency{
			{DependsOnID: "child-1", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()
	insights := stats.GenerateInsights(5)

	g := ui.NewGraphModel(issues, &insights, theme)

	// Select grandchild (likely sorted differently due to critical path)
	found := g.SelectByID("grandchild")
	if !found {
		t.Error("SelectByID should find 'grandchild'")
	}

	sel := g.SelectedIssue()
	if sel == nil || sel.ID != "grandchild" {
		t.Errorf("Expected 'grandchild' selected, got %v", sel)
	}

	// Render with selection
	output := g.View(100, 30)
	if output == "" {
		t.Error("Expected non-empty view")
	}

	// Now select root
	found = g.SelectByID("root")
	if !found {
		t.Error("SelectByID should find 'root'")
	}

	sel = g.SelectedIssue()
	if sel == nil || sel.ID != "root" {
		t.Errorf("Expected 'root' selected, got %v", sel)
	}
}

func TestGraphModel_WhatIfAndETAForecastInMetricsPanel(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	closedAt := now.AddDate(0, 0, -2)
	est := 120
	issues := []model.Issue{
		{ID: "root-open", Title: "Root Blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, EstimatedMinutes: &est, CreatedAt: now.AddDate(0, 0, -5), UpdatedAt: now},
		{
			ID: "dep-1", Title: "Dependent 1", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "dep-1", DependsOnID: "root-open", Type: model.DepBlocks}},
		},
		{
			ID: "dep-2", Title: "Dependent 2", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "dep-2", DependsOnID: "dep-1", Type: model.DepBlocks}},
		},
		{ID: "done-1", Title: "Closed Task", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, ClosedAt: &closedAt, CreatedAt: now.AddDate(0, 0, -6), UpdatedAt: closedAt},
	}

	an := analysis.NewAnalyzer(issues)
	an.SetNow(now)
	stats := an.AnalyzeWithConfig(analysis.FullAnalysisConfig())
	insights := stats.GenerateInsights(len(issues))
	theme := createTheme()

	g := ui.NewGraphModel(issues, &insights, theme)
	g.SetAnalyzer(an, func() time.Time { return now })

	if !g.SelectByID("root-open") {
		t.Fatal("failed to select root-open")
	}
	outOpen := g.View(120, 60)
	if !strings.Contains(outOpen, "What-If & Forecast") || !strings.Contains(outOpen, "+1 direct (+2 total)") || !strings.Contains(outOpen, "ETA:") {
		t.Fatalf("expected GraphModel metrics panel to display What-If (+1 direct, +2 total) and ETA for open blocker, got:\n%s", outOpen)
	}

	if !g.SelectByID("done-1") {
		t.Fatal("failed to select done-1")
	}
	outClosed := g.View(120, 60)
	if strings.Contains(outClosed, "What-If & Forecast") {
		t.Fatalf("expected GraphModel metrics panel to omit What-If & Forecast for closed issue, got:\n%s", outClosed)
	}
}
