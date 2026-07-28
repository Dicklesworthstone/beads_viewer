package export

import (
	"os"
	"strings"
	"testing"

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

func TestGenerateInteractiveGraphHTML_ContextMenuCopiesIDOrDescription(t *testing.T) {
	tmpDir := t.TempDir()
	issues := []model.Issue{
		{
			ID:          "bv-copy",
			Title:       "Copy fields independently",
			Description: "Only this description should be copied.",
			Status:      model.StatusOpen,
			Priority:    2,
		},
	}

	path, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Path:   tmpDir + "/copy_context_menu.html",
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
		"navigator.clipboard.writeText(contextNode.id)",
		"navigator.clipboard.writeText(contextNode.description)",
		"showToast('No description to copy')",
	}
	for _, want := range required {
		if !strings.Contains(html, want) {
			t.Fatalf("generated graph missing context-menu behavior %q", want)
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
