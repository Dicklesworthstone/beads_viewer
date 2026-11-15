package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestExportGraph_JSON(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "First Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Second Issue", Status: model.StatusInProgress, Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format:   GraphFormatJSON,
		DataHash: "test-hash",
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if result.Format != "json" {
		t.Errorf("Expected format 'json', got %s", result.Format)
	}

	if result.Nodes != 2 {
		t.Errorf("Expected 2 nodes, got %d", result.Nodes)
	}

	if result.Edges != 1 {
		t.Errorf("Expected 1 edge, got %d", result.Edges)
	}

	if result.Adjacency == nil {
		t.Fatal("Expected adjacency to be non-nil for JSON format")
	}

	if len(result.Adjacency.Nodes) != 2 {
		t.Errorf("Expected 2 adjacency nodes, got %d", len(result.Adjacency.Nodes))
	}

	if len(result.Adjacency.Edges) != 1 {
		t.Errorf("Expected 1 adjacency edge, got %d", len(result.Adjacency.Edges))
	}
}

// TestExportGraph_JSON_PreservesEdgeTypes locks in the fix for #167: the JSON
// adjacency export must preserve the source dependency type rather than
// collapsing every non-blocking edge into "related". parent-child and
// discovered-from edges previously appeared as "related", which broke
// structural/parentage audits built on --robot-graph.
func TestExportGraph_JSON_PreservesEdgeTypes(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Title: "Root", Status: model.StatusOpen, Priority: 1},
		{ID: "blk", Title: "Blocks edge", Status: model.StatusOpen, Priority: 2,
			Dependencies: []*model.Dependency{{IssueID: "blk", DependsOnID: "root", Type: model.DepBlocks}}},
		{ID: "rel", Title: "Related edge", Status: model.StatusOpen, Priority: 2,
			Dependencies: []*model.Dependency{{IssueID: "rel", DependsOnID: "root", Type: model.DepRelated}}},
		{ID: "pc", Title: "Parent-child edge", Status: model.StatusOpen, Priority: 2,
			Dependencies: []*model.Dependency{{IssueID: "pc", DependsOnID: "root", Type: model.DepParentChild}}},
		{ID: "df", Title: "Discovered-from edge", Status: model.StatusOpen, Priority: 2,
			Dependencies: []*model.Dependency{{IssueID: "df", DependsOnID: "root", Type: model.DepDiscoveredFrom}}},
		// Empty/unset Type is the legacy default for a blocking edge and must
		// normalize to "blocks".
		{ID: "def", Title: "Default (empty) edge", Status: model.StatusOpen, Priority: 2,
			Dependencies: []*model.Dependency{{IssueID: "def", DependsOnID: "root", Type: ""}}},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	result, err := ExportGraph(issues, &stats, GraphExportConfig{Format: GraphFormatJSON})
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}
	if result.Adjacency == nil {
		t.Fatal("Expected adjacency to be non-nil for JSON format")
	}

	gotByFrom := make(map[string]string, len(result.Adjacency.Edges))
	for _, e := range result.Adjacency.Edges {
		gotByFrom[e.From] = e.Type
	}

	want := map[string]string{
		"blk": "blocks",
		"rel": "related",
		"pc":  "parent-child",
		"df":  "discovered-from",
		"def": "blocks",
	}
	if len(result.Adjacency.Edges) != len(want) {
		t.Fatalf("expected %d edges, got %d (%+v)", len(want), len(result.Adjacency.Edges), result.Adjacency.Edges)
	}
	for from, wantType := range want {
		if got := gotByFrom[from]; got != wantType {
			t.Errorf("edge from %q: got type %q, want %q", from, got, wantType)
		}
	}
}

func TestExportGraph_DOT(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "First Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Second Issue", Status: model.StatusClosed, Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatDOT,
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if result.Format != "dot" {
		t.Errorf("Expected format 'dot', got %s", result.Format)
	}

	if result.Graph == "" {
		t.Error("Expected non-empty graph string for DOT format")
	}

	// Check DOT structure
	if !strings.Contains(result.Graph, "digraph G") {
		t.Error("DOT output should contain 'digraph G'")
	}

	if !strings.Contains(result.Graph, "bv-1") {
		t.Error("DOT output should contain node bv-1")
	}

	if !strings.Contains(result.Graph, "bv-2") {
		t.Error("DOT output should contain node bv-2")
	}

	if !strings.Contains(result.Graph, "->") {
		t.Error("DOT output should contain edge arrow")
	}

	// Check status colors
	if !strings.Contains(result.Graph, "#C8E6C9") { // open color
		t.Error("DOT output should contain open status color")
	}

	if !strings.Contains(result.Graph, "#CFD8DC") { // closed color
		t.Error("DOT output should contain closed status color")
	}
}

func TestExportGraph_DOT_EscapesBackslashesInIDs(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv\\1", Title: "First Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "bv\\2", Title: "Second Issue", Status: model.StatusClosed, Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "bv\\2", DependsOnID: "bv\\1", Type: model.DepBlocks},
			},
		},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatDOT,
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if !strings.Contains(result.Graph, "\"bv\\\\1\"") {
		t.Error("DOT output should escape backslashes in node ID bv\\1")
	}

	if !strings.Contains(result.Graph, "\"bv\\\\2\"") {
		t.Error("DOT output should escape backslashes in node ID bv\\2")
	}

	if !strings.Contains(result.Graph, "\"bv\\\\2\" -> \"bv\\\\1\"") {
		t.Error("DOT output should escape backslashes in edge IDs")
	}
}

func TestExportGraph_DOT_TruncationUTF8(t *testing.T) {
	title := strings.Repeat("å", 20) // 40 bytes, 20 runes
	issues := []model.Issue{
		{ID: "bv-utf8", Title: title, Status: model.StatusOpen, Priority: 1},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{Format: GraphFormatDOT}
	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if !utf8.ValidString(result.Graph) {
		t.Fatal("DOT output should be valid UTF-8")
	}
}

func TestExportGraph_DOT_EscapesNewlinesInLabels(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Hello\nWorld", Status: model.StatusOpen, Priority: 1},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{Format: GraphFormatDOT}
	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if strings.Contains(result.Graph, "Hello\nWorld") {
		t.Error("DOT output should not contain raw newlines inside labels")
	}
	if !strings.Contains(result.Graph, "Hello World") {
		t.Error("DOT output should replace newlines with spaces in labels")
	}
}

func TestExportGraph_Mermaid(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "First Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Second Issue", Status: model.StatusBlocked, Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{ID: "tombstone-1", Title: "Removed Issue", Status: model.StatusTombstone, Priority: 3},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatMermaid,
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if result.Format != "mermaid" {
		t.Errorf("Expected format 'mermaid', got %s", result.Format)
	}

	if result.Graph == "" {
		t.Error("Expected non-empty graph string for Mermaid format")
	}

	// Check Mermaid structure
	if !strings.Contains(result.Graph, "graph TD") {
		t.Error("Mermaid output should contain 'graph TD'")
	}

	if !strings.Contains(result.Graph, "classDef open") {
		t.Error("Mermaid output should contain open class definition")
	}

	if !strings.Contains(result.Graph, "classDef blocked") {
		t.Error("Mermaid output should contain blocked class definition")
	}

	// Check for bold edge (blocks)
	if !strings.Contains(result.Graph, "==>") {
		t.Error("Mermaid output should contain bold edge for blocks dependency")
	}

	// Tombstone nodes should be styled as closed-like.
	if !strings.Contains(result.Graph, "class tombstone-1 closed") {
		t.Error("Mermaid output should style tombstone nodes as closed")
	}
}

func TestExportGraph_DOT_TombstoneUsesClosedColor(t *testing.T) {
	issues := []model.Issue{
		{ID: "tombstone-1", Title: "Removed Issue", Status: model.StatusTombstone, Priority: 3},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{Format: GraphFormatDOT}
	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if !strings.Contains(result.Graph, "fillcolor=\"#CFD8DC\"") {
		t.Error("DOT output should style tombstone nodes with the closed color")
	}
}

func TestExportGraph_LabelFilter(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "API Issue", Status: model.StatusOpen, Labels: []string{"api"}},
		{ID: "bv-2", Title: "UI Issue", Status: model.StatusOpen, Labels: []string{"ui"}},
		{ID: "bv-3", Title: "Another API Issue", Status: model.StatusOpen, Labels: []string{"api", "backend"}},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatJSON,
		Label:  "api",
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if result.Nodes != 2 {
		t.Errorf("Expected 2 nodes (api labeled only), got %d", result.Nodes)
	}

	if result.FiltersApplied["label"] != "api" {
		t.Errorf("Expected label filter 'api', got %s", result.FiltersApplied["label"])
	}
}

func TestExportGraph_SubgraphRoot(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Root Issue", Status: model.StatusOpen},
		{ID: "bv-2", Title: "Child Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
		{ID: "bv-3", Title: "Grandchild Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-3", DependsOnID: "bv-2", Type: model.DepBlocks},
			},
		},
		{ID: "bv-4", Title: "Unrelated Issue", Status: model.StatusOpen},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatJSON,
		Root:   "bv-2",
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	// bv-2 depends on bv-1 and unblocks bv-3, so the focused subgraph
	// includes both dependency directions.
	if result.Nodes != 3 {
		t.Errorf("Expected 3 nodes in subgraph from bv-2, got %d", result.Nodes)
	}

	if result.FiltersApplied["root"] != "bv-2" {
		t.Errorf("Expected root filter 'bv-2', got %s", result.FiltersApplied["root"])
	}
}

func TestExportGraph_SubgraphRootIncludesDependents(t *testing.T) {
	issues := []model.Issue{
		{ID: "root-a", Title: "Root Issue", Status: model.StatusOpen},
		{ID: "child-b", Title: "Child Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "child-b", DependsOnID: "root-a", Type: model.DepBlocks},
			},
		},
		{ID: "leaf-c", Title: "Leaf Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "leaf-c", DependsOnID: "child-b", Type: model.DepBlocks},
			},
		},
		{ID: "independent-d", Title: "Unrelated Issue", Status: model.StatusOpen},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	result, err := ExportGraph(issues, &stats, GraphExportConfig{
		Format: GraphFormatJSON,
		Root:   "root-a",
	})
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}
	if result.Nodes != 3 {
		t.Fatalf("expected root subgraph to include root and dependents, got %d nodes", result.Nodes)
	}

	got := make(map[string]bool)
	for _, node := range result.Adjacency.Nodes {
		got[node.ID] = true
	}
	for _, want := range []string{"root-a", "child-b", "leaf-c"} {
		if !got[want] {
			t.Fatalf("focused graph missing %s; nodes=%v", want, got)
		}
	}
	if got["independent-d"] {
		t.Fatalf("focused graph should not include independent issue; nodes=%v", got)
	}
}

func TestExportGraph_SubgraphDepthLimitsBothDirections(t *testing.T) {
	issues := []model.Issue{
		{ID: "root-a", Title: "Root Issue", Status: model.StatusOpen},
		{ID: "child-b", Title: "Child Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "child-b", DependsOnID: "root-a", Type: model.DepBlocks},
			},
		},
		{ID: "leaf-c", Title: "Leaf Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "leaf-c", DependsOnID: "child-b", Type: model.DepBlocks},
			},
		},
		{ID: "sibling-d", Title: "Sibling Issue", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "sibling-d", DependsOnID: "root-a", Type: model.DepBlocks},
			},
		},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	result, err := ExportGraph(issues, &stats, GraphExportConfig{
		Format: GraphFormatJSON,
		Root:   "root-a",
		Depth:  1,
	})
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}
	if result.Nodes != 3 {
		t.Fatalf("expected depth-1 root subgraph to include root and direct dependents, got %d nodes", result.Nodes)
	}

	got := make(map[string]bool)
	for _, node := range result.Adjacency.Nodes {
		got[node.ID] = true
	}
	for _, want := range []string{"root-a", "child-b", "sibling-d"} {
		if !got[want] {
			t.Fatalf("depth-limited focused graph missing %s; nodes=%v", want, got)
		}
	}
	if got["leaf-c"] {
		t.Fatalf("depth-limited focused graph should not include depth-2 leaf; nodes=%v", got)
	}
}

func TestExportGraph_EmptyResult(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-1", Title: "Issue", Status: model.StatusOpen, Labels: []string{"api"}},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatJSON,
		Label:  "nonexistent",
	}

	result, err := ExportGraph(issues, &stats, config)
	if err != nil {
		t.Fatalf("ExportGraph failed: %v", err)
	}

	if result.Nodes != 0 {
		t.Errorf("Expected 0 nodes for nonexistent label, got %d", result.Nodes)
	}
}

func TestGraphExportResult_JSON(t *testing.T) {
	result := &GraphExportResult{
		Format: "json",
		Nodes:  5,
		Edges:  3,
		Explanation: GraphExplanation{
			What:      "Test graph",
			WhenToUse: "Testing",
		},
	}

	data, err := result.JSON()
	if err != nil {
		t.Fatalf("JSON() failed: %v", err)
	}

	// Parse back
	var parsed GraphExportResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if parsed.Nodes != 5 {
		t.Errorf("Expected 5 nodes, got %d", parsed.Nodes)
	}
}

func TestExportGraph_DeterministicOutput(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "bv-3", Title: "Third", Status: model.StatusOpen, CreatedAt: now},
		{ID: "bv-1", Title: "First", Status: model.StatusOpen, CreatedAt: now},
		{ID: "bv-2", Title: "Second", Status: model.StatusOpen, CreatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
				{IssueID: "bv-2", DependsOnID: "bv-3", Type: model.DepRelated},
			},
		},
	}

	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	config := GraphExportConfig{
		Format: GraphFormatDOT,
	}

	// Run twice and compare
	result1, _ := ExportGraph(issues, &stats, config)
	result2, _ := ExportGraph(issues, &stats, config)

	if result1.Graph != result2.Graph {
		t.Error("DOT output should be deterministic across calls")
	}
}

func TestExportGraph_MermaidAndDOT_DependencyBlockedAndCustomStatuses(t *testing.T) {
	issues := []model.Issue{
		{ID: "ready-root", Title: "Ready Open Root", Status: model.StatusOpen, Priority: 1},
		{ID: "closed-prereq", Title: "Closed Prereq", Status: model.StatusClosed, Priority: 1},
		{
			ID:       "blocked-open",
			Title:    "Open With Active Blocker",
			Status:   model.StatusOpen,
			Priority: 1,
			Dependencies: []*model.Dependency{
				{IssueID: "blocked-open", DependsOnID: "ready-root", Type: model.DepBlocks},
			},
		},
		{
			ID:       "unblocked-open",
			Title:    "Open With Closed Blocker",
			Status:   model.StatusOpen,
			Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "unblocked-open", DependsOnID: "closed-prereq", Type: model.DepBlocks},
			},
		},
		{ID: "st-deferred", Title: "Deferred", Status: model.StatusDeferred, Priority: 3},
		{ID: "st-pinned", Title: "Pinned", Status: model.StatusPinned, Priority: 2},
		{ID: "st-hooked", Title: "Hooked", Status: model.StatusHooked, Priority: 2},
		{ID: "st-review", Title: "Review", Status: model.StatusReview, Priority: 2},
		{ID: "st-custom", Title: "Custom Triaged", Status: model.Status("triaged"), Priority: 2},
	}

	stats := analysis.NewAnalyzer(issues).Analyze()

	mermaidRes, err := ExportGraph(issues, &stats, GraphExportConfig{Format: GraphFormatMermaid})
	if err != nil {
		t.Fatalf("ExportGraph(mermaid) failed: %v", err)
	}
	for _, want := range []string{
		"class ready-root open",
		"class blocked-open blocked",
		"class unblocked-open open",
		"class st-deferred deferred",
		"class st-pinned pinned",
		"class st-hooked hooked",
		"class st-review review",
		"class st-custom custom",
	} {
		if !strings.Contains(mermaidRes.Graph, want) {
			t.Fatalf("Mermaid graph missing %q:\n%s", want, mermaidRes.Graph)
		}
	}

	dotRes, err := ExportGraph(issues, &stats, GraphExportConfig{Format: GraphFormatDOT})
	if err != nil {
		t.Fatalf("ExportGraph(dot) failed: %v", err)
	}
	for _, want := range []string{
		`"ready-root" [label="ready-root\nReady Open Root\nP1 open", fillcolor="#C8E6C9"`,
		`"blocked-open" [label="blocked-open\nOpen With Active Blocker\nP1 open", fillcolor="#FFCDD2"`,
		`"unblocked-open" [label="unblocked-open\nOpen With Closed Blocker\nP2 open", fillcolor="#C8E6C9"`,
		`"st-deferred" [label="st-deferred\nDeferred\nP3 deferred", fillcolor="#FFE0B2"`,
		`"st-pinned" [label="st-pinned\nPinned\nP2 pinned", fillcolor="#E1BEE7"`,
		`"st-hooked" [label="st-hooked\nHooked\nP2 hooked", fillcolor="#F8BBD0"`,
		`"st-review" [label="st-review\nReview\nP2 review", fillcolor="#FFF9C4"`,
		`"st-custom" [label="st-custom\nCustom Triaged\nP2 triaged", fillcolor="#E2E8F0"`,
	} {
		if !strings.Contains(dotRes.Graph, want) {
			t.Fatalf("DOT graph missing %q:\n%s", want, dotRes.Graph)
		}
	}
}
