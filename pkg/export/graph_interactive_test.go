package export

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestGenerateInteractiveGraphFilename(t *testing.T) {
	name := GenerateInteractiveGraphFilename("my-project")

	if !strings.HasPrefix(name, "my-project_graph_export__as_of__") {
		t.Errorf("Expected prefix 'my-project_graph_export__as_of__', got: %s", name)
	}
	if !strings.HasSuffix(name, ".html") {
		t.Errorf("Expected .html suffix, got: %s", name)
	}
	if !strings.Contains(name, "__git_head_hash__") {
		t.Errorf("Expected git hash section, got: %s", name)
	}
}

func TestGenerateInteractiveGraphFilename_SpecialChars(t *testing.T) {
	name := GenerateInteractiveGraphFilename("my project/path")

	if strings.Contains(name, " ") {
		t.Errorf("Spaces should be replaced with underscores, got: %s", name)
	}
	if strings.Contains(name, "/") && !strings.HasSuffix(name, ".html") {
		t.Errorf("Slashes should be replaced with underscores, got: %s", name)
	}
	if !strings.HasPrefix(name, "my_project_path_graph_export") {
		t.Errorf("Expected sanitized name prefix, got: %s", name)
	}
}

func TestGenerateInteractiveGraphHTML_EmptyIssues(t *testing.T) {
	opts := InteractiveGraphOptions{
		Issues: []model.Issue{},
	}

	_, err := GenerateInteractiveGraphHTML(opts)
	if err == nil {
		t.Error("Expected error for empty issues")
	}
	if !strings.Contains(err.Error(), "no issues") {
		t.Errorf("Expected 'no issues' error, got: %v", err)
	}
}

func TestGenerateInteractiveGraphHTML_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	issues := []model.Issue{
		{ID: "bv-1", Title: "First Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-2", Title: "Second Issue", Status: model.StatusInProgress, Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	opts := InteractiveGraphOptions{
		Issues:      issues,
		Title:       "Test Graph",
		DataHash:    "test-hash",
		Path:        tmpDir + "/test_graph.html",
		ProjectName: "test-project",
	}

	path, err := GenerateInteractiveGraphHTML(opts)
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}

	if path == "" {
		t.Error("Expected non-empty path")
	}
}

func TestGenerateInteractiveGraphHTML_ContextMenuCopyActions(t *testing.T) {
	tmpDir := t.TempDir()
	path, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: []model.Issue{
			{
				ID:          "bv-1",
				Title:       "First Issue",
				Description: "Raw issue description",
				Status:      model.StatusOpen,
				Priority:    1,
			},
		},
		Path: tmpDir + "/copy-actions.html",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated graph: %v", err)
	}
	html := string(data)

	required := []string{
		`id="ctx-copy">📋 Copy ID`,
		`id="ctx-copy-description">📝 Copy description`,
		"const value = contextNode ? contextNode.id : '';",
		"const value = contextNode ? contextNode.description : '';",
		"Permission policies can reject the modern API",
		"document.execCommand('copy')",
		"showToast('No ' + fieldName.toLowerCase() + ' to copy')",
		"showToast('Copy failed')",
	}
	for _, want := range required {
		if !strings.Contains(html, want) {
			t.Fatalf("generated graph missing copy-action guard %q", want)
		}
	}
}

func TestGenerateInteractiveGraphHTML_EscapesDynamicHTML(t *testing.T) {
	tmpDir := t.TempDir()
	issues := []model.Issue{
		{
			ID:                 `bv-1" data-x="bad`,
			Title:              `<img src=x onerror=alert(1)>`,
			Description:        `[bad link](javascript:alert(1)) <img src=x onerror=alert(1)>`,
			Design:             `<script>alert(1)</script>`,
			AcceptanceCriteria: `accept <svg onload=alert(1)>`,
			Notes:              `note <iframe srcdoc="<script>alert(1)</script>"></iframe>`,
			Status:             model.StatusOpen,
			Priority:           1,
			Labels:             []string{`<label onclick=alert(1)>`},
			Assignee:           `<b>mallory</b>`,
		},
	}

	path, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues:      issues,
		Title:       "Escaping Graph",
		Path:        tmpDir + "/escaping.html",
		ProjectName: "escaping",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated graph: %v", err)
	}
	html := string(data)

	required := []string{
		"function escapeHTML(value)",
		"function sanitizeHTMLFragment(root)",
		"function renderSafeMarkdown(value)",
		"renderSafeMarkdown(node.description)",
		"renderSafeMarkdown(node.design)",
		"renderSafeMarkdown(node.acceptance_criteria)",
		"renderSafeMarkdown(node.notes)",
		"escapeHTML(n.title)",
		"highlightMatches(f.substring(start, end), query)",
		"safeClassToken(n.status)",
	}
	for _, want := range required {
		if !strings.Contains(html, want) {
			t.Fatalf("generated graph missing safety guard %q", want)
		}
	}

	forbidden := []string{
		"marked.parse(node.description)",
		"marked.parse(node.design)",
		"marked.parse(node.acceptance_criteria)",
		"marked.parse(node.notes)",
		"new RegExp(q, 'gi')",
		"<img src=x onerror=alert(1)>",
	}
	for _, bad := range forbidden {
		if strings.Contains(html, bad) {
			t.Fatalf("generated graph contains unsafe pattern %q", bad)
		}
	}
}

func TestGenerateInteractiveGraphHTML_UnifiedFiltersPathFinderRecentAndActiveBlockers(t *testing.T) {
	tmpDir := t.TempDir()
	issues := []model.Issue{
		{
			ID:        "bv-open",
			Title:     "Actionable despite non-blocking and resolved deps",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-open", DependsOnID: "bv-epic", Type: model.DepParentChild},
				{IssueID: "bv-open", DependsOnID: "bv-rel", Type: model.DepRelated},
				{IssueID: "bv-open", DependsOnID: "bv-disc", Type: model.DepDiscoveredFrom},
				{IssueID: "bv-open", DependsOnID: "bv-closed", Type: model.DepBlocks},
				{IssueID: "bv-open", DependsOnID: "bv-tomb", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-blocked",
			Title:     "Actively blocked by bv-open",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeFeature,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-blocked", DependsOnID: "bv-open", Type: model.DepBlocks},
			},
		},
		{
			ID:        "bv-missing-dep",
			Title:     "Blocked by missing prerequisite",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-missing-dep", DependsOnID: "ghost-prereq", Type: model.DepBlocks},
			},
		},
		{ID: "bv-epic", Title: "Parent Epic", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeEpic},
		{ID: "bv-rel", Title: "Related Issue", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "bv-disc", Title: "Discovered Origin", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeBug},
		{ID: "bv-closed", Title: "Closed Prereq", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask},
		{ID: "bv-tomb", Title: "Tombstoned Prereq", Status: model.StatusTombstone, Priority: 3, IssueType: model.TypeTask},
	}

	stats := analysis.NewAnalyzer(issues).Analyze()
	stats.WaitForPhase2()

	path, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues:      issues,
		Stats:       &stats,
		Title:       "Standalone Graph Wiring",
		Path:        tmpDir + "/standalone.html",
		ProjectName: "standalone",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	html := string(raw)

	if count := strings.Count(html, "function applyFilters()"); count != 1 {
		t.Fatalf("expected exactly 1 unified applyFilters() definition, got %d", count)
	}

	for _, want := range []string{
		"addToRecent(node);",
		"if (pathFinderMode)",
		"const path = bfsPath(startId, d.id);",
		"priorityFilter = 'all';",
		"labelFilter = 'all';",
		"document.getElementById('filter-priority').value = 'all';",
		"document.getElementById('filter-label').value = 'all';",
		"document.getElementById('recent-panel').classList.remove('visible');",
		"document.getElementById('btn-recent').classList.remove('active');",
		"function computeNodeColor(n)",
		"isActiveNodeStatus(node.status) && node.slack === 0",
		"Graph.nodeColor(computeNodeColor).linkColor(Graph.linkColor()).linkWidth(Graph.linkWidth());",
		"isBlockingLinkType(l.type) && (!targetNode || isActiveNodeStatus(targetNode.status))",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("standalone interactive HTML missing expected wiring %q", want)
		}
	}

	const prefix = "\nconst DATA = "
	start := strings.Index(html, prefix)
	if start < 0 {
		t.Fatalf("could not locate embedded DATA payload")
	}
	jsonPart := html[start+len(prefix):]
	end := strings.Index(jsonPart, ";\nconst STATUS_COLORS")
	if end < 0 {
		t.Fatalf("could not locate end of embedded DATA payload")
	}

	var payload struct {
		Nodes []graphNode `json:"nodes"`
		Links []graphLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(jsonPart[:end]), &payload); err != nil {
		t.Fatalf("Unmarshal embedded DATA JSON: %v", err)
	}

	nodeMap := make(map[string]graphNode, len(payload.Nodes))
	for _, n := range payload.Nodes {
		nodeMap[n.ID] = n
	}

	if got := nodeMap["bv-open"].BlockedBy; len(got) != 0 {
		t.Fatalf("bv-open BlockedBy = %v, want empty (non-blocking, closed, and tombstone deps excluded)", got)
	}
	if got := nodeMap["bv-open"].Blocks; len(got) != 1 || got[0] != "bv-blocked" {
		t.Fatalf("bv-open Blocks = %v, want [bv-blocked]", got)
	}
	if got := nodeMap["bv-blocked"].BlockedBy; len(got) != 1 || got[0] != "bv-open" {
		t.Fatalf("bv-blocked BlockedBy = %v, want [bv-open]", got)
	}
	if got := nodeMap["bv-missing-dep"].BlockedBy; len(got) != 1 || got[0] != "ghost-prereq" {
		t.Fatalf("bv-missing-dep BlockedBy = %v, want [ghost-prereq] (missing blocker target counts as unsatisfied)", got)
	}
	for _, id := range []string{"bv-closed", "bv-tomb", "bv-epic", "bv-rel", "bv-disc"} {
		if got := nodeMap[id].Blocks; len(got) != 0 {
			t.Fatalf("%s Blocks = %v, want empty", id, got)
		}
	}
	for _, l := range payload.Links {
		if l.Type != string(model.DepBlocks) && l.Critical {
			t.Fatalf("non-blocking link %+v should never be marked critical", l)
		}
		if (l.Target == "bv-closed" || l.Target == "bv-tomb") && l.Critical {
			t.Fatalf("resolved link %+v should never be marked critical", l)
		}
	}
}
