package ui_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
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

	// Cycle-closing edges are annotated while preserving the target's metadata.
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

type dependencyTreeEdge struct{ from, to, kind string }
type dependencyTreeMetadata struct{ title, status string }

// The former path-expansion semantics are deliberately independent of the
// compact traversal. Collect the union of every visible path, retaining source
// metadata even where the former renderer substituted a cycle placeholder.
func dependencyPathFacts(root string, issues map[string]*model.Issue, maxDepth int) (map[string]dependencyTreeMetadata, map[dependencyTreeEdge]bool, int) {
	nodes := make(map[string]dependencyTreeMetadata)
	edges := make(map[dependencyTreeEdge]bool)
	active, counted := make(map[string]bool), make(map[string]bool)
	rows := 1
	var visit func(string, int)
	visit = func(id string, depth int) {
		issue := issues[id]
		if issue == nil {
			nodes[id] = dependencyTreeMetadata{"(not found)", "?"}
			return
		}
		nodes[id] = dependencyTreeMetadata{issue.Title, string(issue.Status)}
		if active[id] || (maxDepth > 0 && depth >= maxDepth) {
			return
		}
		active[id] = true
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if !counted[id] {
				rows++
			}
			edges[dependencyTreeEdge{id, dep.DependsOnID, string(dep.Type)}] = true
			visit(dep.DependsOnID, depth+1)
		}
		counted[id] = true
		active[id] = false
	}
	visit(root, 0)
	return nodes, edges, rows
}

func assertCompactDependencyFacts(t *testing.T, root string, issues map[string]*model.Issue, maxDepth int) *ui.DependencyNode {
	t.Helper()
	wantNodes, wantEdges, wantRows := dependencyPathFacts(root, issues, maxDepth)
	tree := ui.BuildDependencyTree(root, issues, maxDepth)
	nodes := make(map[string]dependencyTreeMetadata)
	edges := make(map[dependencyTreeEdge]bool)
	expansions := make(map[string]int)
	rows := 0
	cycleLabels, hasCycle := 0, false
	returnsTo := func(from, to string) bool {
		seen := make(map[string]bool)
		pending := []string{to}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if id == from {
				return true
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			for edge := range wantEdges {
				if edge.from == id {
					pending = append(pending, edge.to)
				}
			}
		}
		return false
	}
	var visit func(*ui.DependencyNode, int)
	visit = func(node *ui.DependencyNode, depth int) {
		if node == nil {
			t.Fatal("dependency traversal emitted a nil row")
		}
		rows++
		metadata := dependencyTreeMetadata{node.Title, node.Status}
		if metadata != wantNodes[node.ID] {
			t.Errorf("row %s lost issue metadata: got %+v want %+v", node.ID, metadata, wantNodes[node.ID])
		}
		nodes[node.ID] = metadata
		if maxDepth > 0 && depth > maxDepth {
			t.Errorf("row %s exceeds depth %d: %d", node.ID, maxDepth, depth)
		}
		if len(node.Children) > 0 {
			expansions[node.ID]++
			if expansions[node.ID] > 1 {
				t.Errorf("issue %s expanded more than once", node.ID)
			}
		}
		for _, child := range node.Children {
			edges[dependencyTreeEdge{node.ID, child.ID, child.Type}] = true
			cyclic := returnsTo(node.ID, child.ID)
			hasCycle = hasCycle || cyclic
			row := *child
			row.Children = nil
			if strings.Contains(ui.RenderDependencyTree(&row), "(cycle)") {
				cycleLabels++
				if !cyclic {
					t.Errorf("edge %s -> %s labelled cycle without a return path in the displayed union", node.ID, child.ID)
				}
			}
			visit(child, depth+1)
		}
	}
	visit(tree, 0)
	if !reflect.DeepEqual(nodes, wantNodes) || !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("compact graph differs from original path union: nodes=%v want=%v edges=%v want=%v", nodes, wantNodes, edges, wantEdges)
	}
	// One root plus each outgoing source dependency once: path multiplicity
	// cannot enlarge the display, and parallel typed/source edges are not lost.
	if rows != wantRows {
		t.Errorf("compact rows=%d want exactly root + reachable source edges=%d", rows, wantRows)
	}
	if hasCycle && cycleLabels == 0 {
		t.Error("cyclic displayed graph has no cycle-closing edge annotation")
	}
	return tree
}

func TestCompactDependencyTreePreservesPathsAndShallowExpansion(t *testing.T) {
	makeIssues := func(adjacency map[string][]string) map[string]*model.Issue {
		issues := make(map[string]*model.Issue)
		for id, targets := range adjacency {
			issue := &model.Issue{ID: id, Title: "Title " + id, Status: model.StatusOpen}
			for _, target := range targets {
				issue.Dependencies = append(issue.Dependencies, &model.Dependency{DependsOnID: target, Type: model.DepBlocks})
			}
			issues[id] = issue
		}
		return issues
	}
	tests := []struct {
		name      string
		adjacency map[string][]string
		depth     int
	}{
		{"diamond", map[string][]string{"root": {"a", "b"}, "a": {"shared"}, "b": {"shared"}, "shared": {"leaf"}, "leaf": {}}, 0},
		{"deep-first-shortcut", map[string][]string{"root": {"a", "shared"}, "a": {"b"}, "b": {"shared"}, "shared": {"leaf"}, "leaf": {"end"}, "end": {}}, 3},
		// d->e is a DFS backedge, but the sole shortest-depth expansion of e
		// belongs to d. A cycle flag must not suppress e->d beneath that row.
		{"cycle-on-shallow-owner", map[string][]string{"root": {"a", "d", "y"}, "a": {"x"}, "x": {"y"}, "y": {"e"}, "e": {"d"}, "d": {"e"}}, 3},
		{"sibling-cycle", map[string][]string{"root": {"a", "b"}, "a": {"b"}, "b": {"a"}}, 0},
		{"self-cycle", map[string][]string{"root": {"root"}}, 0},
		{"cycle-beyond-depth", map[string][]string{"root": {"a"}, "a": {"b"}, "b": {"a"}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := makeIssues(tc.adjacency)
			tree := assertCompactDependencyFacts(t, "root", issues, tc.depth)
			rendered := ui.RenderDependencyTree(tree)
			if tc.name == "diamond" || tc.name == "deep-first-shortcut" {
				if !strings.Contains(rendered, "(reference: shown elsewhere)") || strings.Contains(rendered, "(cycle)") {
					t.Errorf("shared DAG targets must be references, not cycles:\n%s", rendered)
				}
			}
			if tc.name == "sibling-cycle" || tc.name == "self-cycle" || tc.name == "cycle-on-shallow-owner" {
				if !strings.Contains(rendered, "(cycle)") {
					t.Errorf("displayed cycle has no closing-edge annotation:\n%s", rendered)
				}
			}
			if tc.name == "cycle-beyond-depth" && strings.Contains(rendered, "(cycle)") {
				t.Errorf("cycle outside the displayed edge union must not be asserted:\n%s", rendered)
			}
			if tc.name == "deep-first-shortcut" {
				shared := tree.Children[1]
				if shared.ID != "shared" || len(shared.Children) != 1 || shared.Children[0].ID != "leaf" || len(shared.Children[0].Children) != 1 || shared.Children[0].Children[0].ID != "end" {
					t.Fatalf("shortest path did not retain its full allowed descendants:\n%s", rendered)
				}
			}
			if tc.name == "cycle-on-shallow-owner" {
				d := tree.Children[1]
				if d.ID != "d" || len(d.Children) != 1 || len(d.Children[0].Children) != 1 || d.Children[0].Children[0].ID != "d" {
					t.Fatalf("cycle-labelled canonical e lost its outgoing d edge:\n%s", rendered)
				}
			}
			if again := ui.RenderDependencyTree(ui.BuildDependencyTree("root", issues, tc.depth)); again != rendered {
				t.Fatal("same graph changed canonical ownership or cycle/reference annotations")
			}
		})
	}
}

func TestCompactDependencyTreeMissingAndTypedEdges(t *testing.T) {
	issues := map[string]*model.Issue{
		"root": {ID: "root", Title: "日本語 root", Status: model.StatusInProgress, Dependencies: []*model.Dependency{
			nil, {DependsOnID: "shared", Type: model.DepBlocks}, {DependsOnID: "shared", Type: model.DepRelated},
			{DependsOnID: "missing", Type: model.DepParentChild}, {DependsOnID: "nil", Type: model.DepDiscoveredFrom},
			{DependsOnID: "shared", Type: model.DependencyType("custom")}, {DependsOnID: "shared", Type: model.DepBlocks},
		}},
		"shared": {ID: "shared", Title: "Shared ✓", Status: model.StatusClosed},
		"nil":    nil,
	}
	tree := assertCompactDependencyFacts(t, "root", issues, 0)
	if len(tree.Children) != 6 {
		t.Fatalf("nil dependency must be omitted, but duplicate/typed dependencies kept: %d", len(tree.Children))
	}
	for _, root := range []string{"missing", "nil"} {
		t.Run(root, func(t *testing.T) { assertCompactDependencyFacts(t, root, issues, 0) })
	}
}

func TestCompactDependencyTreeGeneratedGraphMeaning(t *testing.T) {
	for seed := int64(0); seed < 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		issues := make(map[string]*model.Issue)
		for i := 0; i < 8; i++ {
			id := fmt.Sprint(i)
			issue := &model.Issue{ID: id, Title: "Generated " + id, Status: []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusBlocked, model.StatusClosed}[i%4]}
			for j := 0; j < 8; j++ {
				if rng.Intn(4) == 0 {
					issue.Dependencies = append(issue.Dependencies, &model.Dependency{DependsOnID: fmt.Sprint(j), Type: []model.DependencyType{model.DepBlocks, model.DepRelated, model.DepParentChild}[j%3]})
				}
			}
			issues[id] = issue
		}
		for _, depth := range []int{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("seed=%d/depth=%d", seed, depth), func(t *testing.T) {
				assertCompactDependencyFacts(t, "0", issues, depth)
			})
		}
	}
	t.Run("actual-dense-fixture", func(t *testing.T) {
		issues, err := testutil.PerformanceIssues("cyclic-dense", 1000, 20260904)
		if err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]*model.Issue, len(issues))
		for i := range issues {
			byID[issues[i].ID] = &issues[i]
		}
		tree := assertCompactDependencyFacts(t, issues[0].ID, byID, 3)
		nodes, edges, rows := dependencyPathFacts(issues[0].ID, byID, 3)
		t.Logf("actual dense detail: root=%s unique issues=%d typed edges=%d rows=%d rendered rows=%d", issues[0].ID, len(nodes), len(edges), rows, strings.Count(ui.RenderDependencyTree(tree), "\n")-1)
	})
}

func TestDependencyTreeRowWritingExactBytes(t *testing.T) {
	tree := &ui.DependencyNode{ID: "root", Title: "日本語 ✓", Status: "open", Type: "root", Children: []*ui.DependencyNode{
		{ID: "a", Title: "Alpha", Status: "in_progress", Type: "blocks", Children: []*ui.DependencyNode{{ID: "c", Title: "C", Status: "closed", Type: "parent-child"}}},
		{ID: "b", Title: "Beta", Status: "blocked", Type: "related"},
		{ID: "d", Title: "Delta", Status: "?", Type: "discovered-from"},
		{ID: "e", Title: "Other", Status: "custom", Type: "custom"},
	}}
	// Freeze exact established row bytes independently of the row writer.
	want := "Dependency Graph:\n🟢 📍 root 日本語 ✓ (open) [root]\n" +
		"├── 🔵 ⛔ a Alpha (in_progress) [blocks]\n" +
		"│   └── ⚫ 📦 c C (closed) [parent-child]\n" +
		"├── 🔴 🔗 b Beta (blocked) [related]\n" +
		"├── ⚪ 🔍 d Delta (?) [discovered-from]\n" +
		"└── ⚪ • e Other (custom) [custom]\n"
	if got := ui.RenderDependencyTree(tree); got != want {
		t.Fatalf("row writer changed exact bytes:\ngot %q\nwant %q", got, want)
	}
	tree.Children[0].Title = "Changed\nline"
	if got := ui.RenderDependencyTree(tree); got == want || !strings.Contains(got, "Changed\nline") {
		t.Fatal("row writer ignored changed title/newline bytes")
	}
}

func TestDependencyTreeRowWritingAllocationBound(t *testing.T) {
	tree := &ui.DependencyNode{ID: "root", Title: "Root", Status: "open", Type: "root"}
	for i := 0; i < 1000; i++ {
		tree.Children = append(tree.Children, &ui.DependencyNode{ID: "child", Title: "Child", Status: "closed", Type: "blocks"})
	}
	var rendered string
	allocations := testing.AllocsPerRun(1, func() { rendered = ui.RenderDependencyTree(tree) })
	if strings.Count(rendered, "\n") != 1002 {
		t.Fatal("allocation probe did not render all fixed rows")
	}
	// The previous fmt.Sprintf boxed eight string arguments per row. This
	// bound rejects that measured cost without depending on elapsed time.
	if limit := float64(4*len(tree.Children) + 64); allocations > limit {
		t.Fatalf("fixed1001-row render allocated %.0f times, limit %.0f", allocations, limit)
	}
	t.Logf("fixed1001-row render allocations=%.0f", allocations)
}
