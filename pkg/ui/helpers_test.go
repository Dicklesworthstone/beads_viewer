package ui_test

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

// TestTruncateRunesHelper tests UTF-8 safe truncation
func TestTruncateRunesHelper(t *testing.T) {
	// Access the helper via the package - it's exported through visuals.go or similar
	// Since truncateRunesHelper is not exported, we test it indirectly through View methods
	// that use it. However, let's test what we can access.

	// For now, test through the public interface that uses truncation
	theme := createTheme()

	// Create an issue with a very long title containing Unicode
	issue := model.Issue{
		ID:     "unicode-test",
		Title:  "日本語タイトル with mixed content 混合コンテンツ",
		Status: model.StatusOpen,
	}

	b := ui.NewBoardModel([]model.Issue{issue}, theme)
	// View should not panic with Unicode content
	_ = b.View(80, 24)
}

// TestBuildDependencyTree tests the dependency tree building
func TestBuildDependencyTree(t *testing.T) {
	// Create a simple dependency chain: A -> B -> C
	issues := []model.Issue{
		{ID: "A", Title: "Root Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "Middle Issue", Status: model.StatusInProgress,
			Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Title: "Leaf Issue", Status: model.StatusClosed},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	tree := ui.BuildDependencyTree("A", issueMap, 10)

	if tree == nil {
		t.Fatal("Expected non-nil tree")
	}
	if tree.ID != "A" {
		t.Errorf("Expected root ID 'A', got %s", tree.ID)
	}
	if tree.Title != "Root Issue" {
		t.Errorf("Expected title 'Root Issue', got %s", tree.Title)
	}
	if tree.Status != "open" {
		t.Errorf("Expected status 'open', got %s", tree.Status)
	}
	if tree.Type != "root" {
		t.Errorf("Expected type 'root', got %s", tree.Type)
	}
	if len(tree.Children) != 1 {
		t.Fatalf("Expected 1 child, got %d", len(tree.Children))
	}

	// Check child B
	childB := tree.Children[0]
	if childB.ID != "B" {
		t.Errorf("Expected child ID 'B', got %s", childB.ID)
	}
	if childB.Type != "blocks" {
		t.Errorf("Expected child type 'blocks', got %s", childB.Type)
	}
	if len(childB.Children) != 1 {
		t.Fatalf("Expected 1 grandchild, got %d", len(childB.Children))
	}

	// Check grandchild C
	childC := childB.Children[0]
	if childC.ID != "C" {
		t.Errorf("Expected grandchild ID 'C', got %s", childC.ID)
	}
	if len(childC.Children) != 0 {
		t.Errorf("Expected no children for leaf, got %d", len(childC.Children))
	}
}

// TestBuildDependencyTreeCycleDetection tests cycle detection in tree building
func TestBuildDependencyTreeCycleDetection(t *testing.T) {
	// Create a cycle: A -> B -> C -> A
	issues := []model.Issue{
		{ID: "A", Title: "A", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "B", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Title: "C", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	// Should not infinite loop
	tree := ui.BuildDependencyTree("A", issueMap, 10)

	if tree == nil {
		t.Fatal("Expected non-nil tree even with cycle")
	}

	// Tree should contain a cycle marker - the cycle detection creates a node with "(cycle)" as title
	rendered := ui.RenderDependencyTree(tree)
	if !strings.Contains(rendered, "(cycle)") {
		t.Errorf("Expected cycle marker '(cycle)' in rendered tree, got:\n%s", rendered)
	}
}

// TestBuildDependencyTreeDepthLimit tests max depth limiting
func TestBuildDependencyTreeDepthLimit(t *testing.T) {
	// Create a deep chain: A -> B -> C -> D -> E
	issues := []model.Issue{
		{ID: "A", Title: "A", Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "B", Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Title: "C", Dependencies: []*model.Dependency{{DependsOnID: "D", Type: model.DepBlocks}}},
		{ID: "D", Title: "D", Dependencies: []*model.Dependency{{DependsOnID: "E", Type: model.DepBlocks}}},
		{ID: "E", Title: "E"},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	// Build with depth limit of 2
	tree := ui.BuildDependencyTree("A", issueMap, 2)

	if tree == nil {
		t.Fatal("Expected non-nil tree")
	}

	// Count depth
	depth := 0
	node := tree
	for node != nil && len(node.Children) > 0 {
		depth++
		node = node.Children[0]
	}

	if depth > 2 {
		t.Errorf("Expected depth <= 2, got %d", depth)
	}
}

// TestBuildDependencyTreeMissingDependency tests handling of missing dependencies
func TestBuildDependencyTreeMissingDependency(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "A",
			Dependencies: []*model.Dependency{{DependsOnID: "missing", Type: model.DepBlocks}}},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	tree := ui.BuildDependencyTree("A", issueMap, 10)

	if tree == nil {
		t.Fatal("Expected non-nil tree")
	}
	if len(tree.Children) != 1 {
		t.Fatalf("Expected 1 child for missing dep, got %d", len(tree.Children))
	}

	// Missing dependency should have "(not found)" as title
	if tree.Children[0].Title != "(not found)" {
		t.Errorf("Expected '(not found)' for missing dep, got %s", tree.Children[0].Title)
	}
}

// TestBuildDependencyTreeMissingRoot tests handling of missing root
func TestBuildDependencyTreeMissingRoot(t *testing.T) {
	issueMap := make(map[string]*model.Issue)

	tree := ui.BuildDependencyTree("nonexistent", issueMap, 10)

	if tree == nil {
		t.Fatal("Expected non-nil tree for missing root")
	}
	if tree.Title != "(not found)" {
		t.Errorf("Expected '(not found)' for missing root, got %s", tree.Title)
	}
}

// TestRenderDependencyTree tests tree rendering
func TestRenderDependencyTree(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Title: "Root Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "child1", Type: model.DepBlocks},
				{DependsOnID: "child2", Type: model.DepRelated},
			}},
		{ID: "child1", Title: "Child One", Status: model.StatusInProgress},
		{ID: "child2", Title: "Child Two", Status: model.StatusClosed},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	tree := ui.BuildDependencyTree("root", issueMap, 10)
	rendered := ui.RenderDependencyTree(tree)

	// Should contain the header
	if !strings.Contains(rendered, "Dependency Graph") {
		t.Error("Expected 'Dependency Graph' header in output")
	}

	// Should contain root ID
	if !strings.Contains(rendered, "root") {
		t.Error("Expected 'root' in output")
	}

	// Should contain children
	if !strings.Contains(rendered, "child1") {
		t.Error("Expected 'child1' in output")
	}
	if !strings.Contains(rendered, "child2") {
		t.Error("Expected 'child2' in output")
	}

	// Should contain status info
	if !strings.Contains(rendered, "open") {
		t.Error("Expected 'open' status in output")
	}
}

// TestRenderDependencyTreeNil tests rendering nil tree
func TestRenderDependencyTreeNil(t *testing.T) {
	rendered := ui.RenderDependencyTree(nil)

	if rendered != "No dependency data." {
		t.Errorf("Expected 'No dependency data.', got %s", rendered)
	}
}

// TestGetStatusIcon tests status icon mapping
func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"open", "🟢"},
		{"in_progress", "🔵"},
		{"blocked", "🔴"},
		{"closed", "⚫"},
		{"unknown", "⚪"},
		{"", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			icon := ui.GetStatusIcon(tt.status)
			if icon != tt.expected {
				t.Errorf("GetStatusIcon(%s) = %s; want %s", tt.status, icon, tt.expected)
			}
		})
	}
}

// TestBuildDependencyTreeMultipleDependencyTypes tests different dependency types
func TestBuildDependencyTreeMultipleDependencyTypes(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Title: "Root", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "blocks-dep", Type: model.DepBlocks},
				{DependsOnID: "related-dep", Type: model.DepRelated},
				{DependsOnID: "parent-dep", Type: model.DepParentChild},
				{DependsOnID: "discovered-dep", Type: model.DepDiscoveredFrom},
			}},
		{ID: "blocks-dep", Title: "Blocks", Status: model.StatusOpen},
		{ID: "related-dep", Title: "Related", Status: model.StatusOpen},
		{ID: "parent-dep", Title: "Parent", Status: model.StatusOpen},
		{ID: "discovered-dep", Title: "Discovered", Status: model.StatusOpen},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	tree := ui.BuildDependencyTree("root", issueMap, 10)

	if tree == nil {
		t.Fatal("Expected non-nil tree")
	}
	if len(tree.Children) != 4 {
		t.Errorf("Expected 4 children, got %d", len(tree.Children))
	}

	// Verify dependency types are preserved
	typeMap := make(map[string]string)
	for _, child := range tree.Children {
		typeMap[child.ID] = child.Type
	}

	if typeMap["blocks-dep"] != "blocks" {
		t.Errorf("Expected 'blocks' type, got %s", typeMap["blocks-dep"])
	}
	if typeMap["related-dep"] != "related" {
		t.Errorf("Expected 'related' type, got %s", typeMap["related-dep"])
	}
	if typeMap["parent-dep"] != "parent-child" {
		t.Errorf("Expected 'parent-child' type, got %s", typeMap["parent-dep"])
	}
	if typeMap["discovered-dep"] != "discovered-from" {
		t.Errorf("Expected 'discovered-from' type, got %s", typeMap["discovered-dep"])
	}
}

// TestBuildDependencyTreeLongTitle tests truncation of long titles
func TestBuildDependencyTreeLongTitle(t *testing.T) {
	// Title is 106 characters, truncation limit is 40, so it must be truncated
	longTitle := "This is a very long title that should be truncated to fit within the display area for better readability"
	issues := []model.Issue{
		{ID: "long", Title: longTitle, Status: model.StatusOpen},
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	tree := ui.BuildDependencyTree("long", issueMap, 10)
	rendered := ui.RenderDependencyTree(tree)

	// Title is 106 chars, truncation limit is 40, so it MUST contain "..."
	if !strings.Contains(rendered, "...") {
		t.Errorf("Expected truncation indicator '...' in rendered tree for %d-char title, got:\n%s", len(longTitle), rendered)
	}

	// Should NOT contain the full title since it's truncated
	if strings.Contains(rendered, longTitle) {
		t.Errorf("Expected title to be truncated, but found full title in output")
	}
}

// TestBuildDependencyTreeUnlimitedDepth tests unlimited depth (0)
func TestBuildDependencyTreeUnlimitedDepth(t *testing.T) {
	// Create a deep chain
	var issues []model.Issue
	for i := 0; i < 20; i++ {
		issue := model.Issue{
			ID:     string(rune('A' + i)),
			Title:  "Issue " + string(rune('A'+i)),
			Status: model.StatusOpen,
		}
		if i < 19 {
			issue.Dependencies = []*model.Dependency{
				{DependsOnID: string(rune('A' + i + 1)), Type: model.DepBlocks},
			}
		}
		issues = append(issues, issue)
	}

	issueMap := make(map[string]*model.Issue)
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	// Build with unlimited depth (0)
	tree := ui.BuildDependencyTree("A", issueMap, 0)

	if tree == nil {
		t.Fatal("Expected non-nil tree")
	}

	// Count actual depth
	depth := 0
	node := tree
	for node != nil && len(node.Children) > 0 {
		depth++
		node = node.Children[0]
	}

	// Should traverse all 20 levels
	if depth != 19 {
		t.Errorf("Expected depth 19 with unlimited, got %d", depth)
	}
}
