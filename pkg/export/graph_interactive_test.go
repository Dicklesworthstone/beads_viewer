package export

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
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
		"const XHTML_NS = 'http://www.w3.org/1999/xhtml';",
		"const ALLOWED_MARKDOWN_TAGS = new Set([",
		"const DROP_SUBTREE_TAGS = new Set([",
		"function escapeHTML(value)",
		"function sanitizeHTMLFragment(root)",
		"function renderSafeMarkdownFragment(value)",
		"document.getElementById(prefix + 'description-content').replaceChildren(renderSafeMarkdownFragment(node.description))",
		"document.getElementById(prefix + 'design-content').replaceChildren(renderSafeMarkdownFragment(node.design))",
		"document.getElementById(prefix + 'acceptance-content').replaceChildren(renderSafeMarkdownFragment(node.acceptance_criteria))",
		"document.getElementById(prefix + 'notes-content').replaceChildren(renderSafeMarkdownFragment(node.notes))",
		"el.namespaceURI !== XHTML_NS || DROP_SUBTREE_TAGS.has(tag)",
		"document.createTreeWalker(root, NodeFilter.SHOW_COMMENT)",
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
		"function renderSafeMarkdown(value)",
		"description-content').innerHTML =",
		"design-content').innerHTML =",
		"acceptance-content').innerHTML =",
		"notes-content').innerHTML =",
		"return template.innerHTML",
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

func TestGenerateInteractiveGraphHTML_CyclicSlackSafetyDagErrorMarkdownAndMetrics(t *testing.T) {
	tmpDir := t.TempDir()

	cyclicIssues := []model.Issue{
		{
			ID:          "cyc-a",
			Title:       "Cycle A",
			Description: "> Quote\n\nInline `a < b && c != \"x\"`",
			Status:      model.StatusOpen,
			Priority:    0,
			IssueType:   model.TypeBug,
			Dependencies: []*model.Dependency{
				{IssueID: "cyc-a", DependsOnID: "cyc-b", Type: model.DepBlocks},
			},
		},
		{
			ID:        "cyc-b",
			Title:     "Cycle B",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{IssueID: "cyc-b", DependsOnID: "cyc-a", Type: model.DepBlocks},
			},
		},
		{
			ID:        "cyc-leaf",
			Title:     "Downstream of cycle",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeFeature,
			Dependencies: []*model.Dependency{
				{IssueID: "cyc-leaf", DependsOnID: "cyc-a", Type: model.DepBlocks},
			},
		},
	}
	cyclicStats := analysis.NewAnalyzer(cyclicIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	if len(cyclicStats.Cycles()) == 0 {
		t.Fatalf("expected cyclic fixture to report at least 1 cycle")
	}

	cyclicPath, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues:      cyclicIssues,
		Stats:       &cyclicStats,
		Title:       "Cyclic Graph Safety",
		Path:        tmpDir + "/cyclic.html",
		ProjectName: "cyclic",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML(cyclic) failed: %v", err)
	}
	rawCyclic, err := os.ReadFile(cyclicPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", cyclicPath, err)
	}
	html := string(rawCyclic)

	for _, want := range []string{
		".onDagError(() => handleDagCycleError())",
		"Cannot apply DAG layout: dependency cycle detected",
		"localStorage.removeItem('bv-graph-layout')",
		"addMetric('Slack', node.slack_available === false ? (DATA.has_cycles ? 'N/A (cycle)' : 'N/A') : fmt(node.slack, 1))",
		"slackEl.textContent = node.slack_available === false ? (DATA.has_cycles ? 'N/A (cycle)' : 'N/A') : fmtSide(node.slack, 1);",
		"slackEl.className = 'metric-value' + (node.slack_available !== false && isActiveNodeStatus(node.status) && node.slack === 0 ? ' highlight' : '');",
		"template.innerHTML = marked.parse(raw);",
		"sanitizeHTMLFragment(template.content);",
		"frag.appendChild(template.content);",
		`.split(/\r?\n/)[0]`,
		"document.body.classList.contains('light-mode') ? '#1a1a2e' : '#e8e8f0'",
		"node.core_number",
		"node.eigenvector",
		"node.hub",
		"node.authority",
		"node.last_author",
		"node.commit_count",
		"DATA.git_range",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("cyclic interactive HTML missing expected guard/wiring %q", want)
		}
	}
	for _, bad := range []string{
		"marked.parse(escapeHTML(",
		`.split('\\n')[0]`,
	} {
		if strings.Contains(html, bad) {
			t.Fatalf("cyclic interactive HTML contains regressed pattern %q", bad)
		}
	}

	const prefix = "\nconst DATA = "
	start := strings.Index(html, prefix)
	end := strings.Index(html[start+len(prefix):], ";\nconst STATUS_COLORS")
	if start < 0 || end < 0 {
		t.Fatalf("could not extract embedded DATA JSON")
	}
	var cyclicPayload struct {
		SlackAvailable bool        `json:"slack_available"`
		HasCycles      bool        `json:"has_cycles"`
		Nodes          []graphNode `json:"nodes"`
		Links          []graphLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(html[start+len(prefix):start+len(prefix)+end]), &cyclicPayload); err != nil {
		t.Fatalf("Unmarshal cyclic DATA JSON: %v", err)
	}
	if cyclicPayload.SlackAvailable {
		t.Fatalf("expected slack_available=false on cyclic graph")
	}
	if !cyclicPayload.HasCycles {
		t.Fatalf("expected has_cycles=true on cyclic graph")
	}
	for _, n := range cyclicPayload.Nodes {
		if n.SlackAvailable {
			t.Fatalf("expected node %s slack_available=false on cyclic graph", n.ID)
		}
	}
	for _, l := range cyclicPayload.Links {
		if l.Critical {
			t.Fatalf("expected zero critical links on cyclic graph, got %+v", l)
		}
	}

	// Acyclic graph positive control: critical path nodes & link ARE marked critical with slack_available=true.
	acyclicIssues := []model.Issue{
		{ID: "dag-root", Title: "Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
		{
			ID:        "dag-leaf",
			Title:     "Leaf",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{IssueID: "dag-leaf", DependsOnID: "dag-root", Type: model.DepBlocks},
			},
		},
	}
	acyclicStats := analysis.NewAnalyzer(acyclicIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	acyclicPath, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: acyclicIssues,
		Stats:  &acyclicStats,
		Path:   tmpDir + "/acyclic.html",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML(acyclic) failed: %v", err)
	}
	rawAcyclic, err := os.ReadFile(acyclicPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", acyclicPath, err)
	}
	aHTML := string(rawAcyclic)
	aStart := strings.Index(aHTML, prefix)
	aEnd := strings.Index(aHTML[aStart+len(prefix):], ";\nconst STATUS_COLORS")
	var acyclicPayload struct {
		SlackAvailable bool        `json:"slack_available"`
		Nodes          []graphNode `json:"nodes"`
		Links          []graphLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(aHTML[aStart+len(prefix):aStart+len(prefix)+aEnd]), &acyclicPayload); err != nil {
		t.Fatalf("Unmarshal acyclic DATA JSON: %v", err)
	}
	if !acyclicPayload.SlackAvailable {
		t.Fatalf("expected slack_available=true on acyclic graph")
	}
	for _, n := range acyclicPayload.Nodes {
		if !n.SlackAvailable {
			t.Fatalf("expected node %s slack_available=true on acyclic graph", n.ID)
		}
	}
	if len(acyclicPayload.Links) != 1 || !acyclicPayload.Links[0].Critical {
		t.Fatalf("expected critical link on 2-node DAG chain, got %+v", acyclicPayload.Links)
	}

	// Closed node on an acyclic graph must have slack_available=false (never false critical-path slack=0).
	acyclicWithClosed := append(append([]model.Issue{}, acyclicIssues...), model.Issue{
		ID:        "dag-closed",
		Title:     "Closed historical task",
		Status:    model.StatusClosed,
		Priority:  2,
		IssueType: model.TypeTask,
	})
	closedStats := analysis.NewAnalyzer(acyclicWithClosed).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	closedPath, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: acyclicWithClosed,
		Stats:  &closedStats,
		Path:   tmpDir + "/acyclic_closed.html",
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML(acyclicWithClosed) failed: %v", err)
	}
	rawClosed, err := os.ReadFile(closedPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", closedPath, err)
	}
	cHTML := string(rawClosed)
	cStart := strings.Index(cHTML, prefix)
	cEnd := strings.Index(cHTML[cStart+len(prefix):], ";\nconst STATUS_COLORS")
	var closedPayload struct {
		SlackAvailable bool        `json:"slack_available"`
		HasCycles      bool        `json:"has_cycles"`
		Nodes          []graphNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(cHTML[cStart+len(prefix):cStart+len(prefix)+cEnd]), &closedPayload); err != nil {
		t.Fatalf("Unmarshal acyclicWithClosed DATA JSON: %v", err)
	}
	if !closedPayload.SlackAvailable || closedPayload.HasCycles {
		t.Fatalf("expected slack_available=true and has_cycles=false, got %+v", closedPayload)
	}
	for _, n := range closedPayload.Nodes {
		if n.ID == "dag-closed" && n.SlackAvailable {
			t.Fatalf("expected closed node dag-closed to have slack_available=false")
		}
		if n.ID != "dag-closed" && !n.SlackAvailable {
			t.Fatalf("expected active node %s to have slack_available=true", n.ID)
		}
	}
}

func TestGenerateInteractiveGraphFilenameWithOpts_ExplicitAndGitWorkDirControls(t *testing.T) {
	exportAt := time.Date(2026, 9, 4, 12, 30, 0, 0, time.UTC)

	// 1. Explicit exportedAt + explicit gitHash ignores workDir git state completely.
	explicitName := GenerateInteractiveGraphFilenameWithOpts("my repo/sub", exportAt, t.TempDir(), "  cafe987  ")
	wantExplicit := "my_repo_sub_graph_export__as_of__2026_09_04__12_30__git_head_hash__cafe987.html"
	if explicitName != wantExplicit {
		t.Fatalf("explicit filename = %q, want %q", explicitName, wantExplicit)
	}

	// 1b. 40-char full SHA-1 GitHash (as passed by cmd/bv) truncates to 8 hex chars.
	fullHashName := GenerateInteractiveGraphFilenameWithOpts("demo", exportAt, t.TempDir(), "0123456789abcdef0123456789abcdef01234567")
	wantFullHash := "demo_graph_export__as_of__2026_09_04__12_30__git_head_hash__01234567.html"
	if fullHashName != wantFullHash {
		t.Fatalf("40-char gitHash filename = %q, want %q", fullHashName, wantFullHash)
	}

	// 1c. Ref/branch-like gitHash with slashes, backslashes, and spaces sanitizes cleanly.
	branchName := GenerateInteractiveGraphFilenameWithOpts("demo\\sub", exportAt, t.TempDir(), "feature/branch name\\rev")
	wantBranch := "demo_sub_graph_export__as_of__2026_09_04__12_30__git_head_hash__feature_branch_name_rev.html"
	if branchName != wantBranch {
		t.Fatalf("sanitized gitHash filename = %q, want %q", branchName, wantBranch)
	}

	// 2. Empty gitHash in a non-git temp directory must fall back to "nogit" (never leaking CWD repo hash!).
	noGitDir := t.TempDir()
	noGitName := GenerateInteractiveGraphFilenameWithOpts("demo", exportAt, noGitDir, "")
	wantNoGit := "demo_graph_export__as_of__2026_09_04__12_30__git_head_hash__nogit.html"
	if noGitName != wantNoGit {
		t.Fatalf("non-git workDir filename = %q, want %q", noGitName, wantNoGit)
	}

	// 3. Empty gitHash in an initialized temp git repo resolves that repo's short HEAD hash.
	if _, err := exec.LookPath("git"); err == nil {
		gitDir := t.TempDir()
		runGit := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = gitDir
			cmd.Env = append(loader.SanitizedGitEnv(),
				"GIT_AUTHOR_NAME=Test",
				"GIT_AUTHOR_EMAIL=test@example.com",
				"GIT_COMMITTER_NAME=Test",
				"GIT_COMMITTER_EMAIL=test@example.com",
				"GIT_AUTHOR_DATE=2026-09-04T12:00:00Z",
				"GIT_COMMITTER_DATE=2026-09-04T12:00:00Z",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
			}
			return strings.TrimSpace(string(out))
		}
		runGit("init", "-q")
		if err := os.WriteFile(filepath.Join(gitDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		runGit("add", "README.md")
		runGit("commit", "-q", "-m", "init")
		expectedShort := runGit("rev-parse", "--short", "HEAD")

		resolvedName := GenerateInteractiveGraphFilenameWithOpts("demo", exportAt, gitDir, "")
		wantResolved := "demo_graph_export__as_of__2026_09_04__12_30__git_head_hash__" + expectedShort + ".html"
		if resolvedName != wantResolved {
			t.Fatalf("git workDir filename = %q, want %q", resolvedName, wantResolved)
		}
	}
}

func TestGenerateInteractiveGraphHTML_ExternalReadinessWithheldFlagsAndClosedArticulation(t *testing.T) {
	exportAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	closedAt := exportAt

	// Full workspace contains external open blocker and external closed blocker omitted from the exported slice.
	fullWorkspace := []model.Issue{
		{ID: "ext-open-blocker", Title: "External Open Blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
		{ID: "ext-closed-blocker", Title: "External Closed Blocker", Status: model.StatusClosed, ClosedAt: &closedAt, Priority: 1, IssueType: model.TypeTask},
		{
			ID: "slice-blocked-ext", Title: "Blocked By Omitted Open Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "slice-blocked-ext", DependsOnID: "ext-open-blocker", Type: model.DepBlocks}},
		},
		{
			ID: "slice-ready-ext", Title: "Ready Because Omitted Blocker Closed", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "slice-ready-ext", DependsOnID: "ext-closed-blocker", Type: model.DepBlocks}},
		},
		{ID: "slice-ephemeral", Title: "Ephemeral Wisp", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Ephemeral: true},
		{ID: "slice-pinned", Title: "Pinned Notice", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Pinned: true},
		{ID: "slice-template", Title: "Issue Template", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, IsTemplate: true},
		// Active cut-vertex chain: open-leaf -> open-cut -> open-root
		{ID: "open-root", Title: "Open Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
		{
			ID: "open-cut", Title: "Active Cut Vertex", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "open-cut", DependsOnID: "open-root", Type: model.DepBlocks}},
		},
		{
			ID: "open-leaf", Title: "Active Leaf", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "open-leaf", DependsOnID: "open-cut", Type: model.DepBlocks}},
		},
		// Historical closed cut vertex bridging open-root to closed-wing
		{
			ID: "closed-cut", Title: "Closed Historical Cut Vertex", Status: model.StatusClosed, ClosedAt: &closedAt, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "closed-cut", DependsOnID: "open-root", Type: model.DepBlocks}},
		},
		{
			ID: "closed-wing", Title: "Closed Historical Wing", Status: model.StatusClosed, ClosedAt: &closedAt, Priority: 3, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "closed-wing", DependsOnID: "closed-cut", Type: model.DepBlocks}},
		},
	}

	fullReadiness := model.NewReadinessIndex(fullWorkspace)
	slice := fullWorkspace[2:] // Omit ext-open-blocker and ext-closed-blocker from the rendered slice.
	sliceStats := analysis.NewAnalyzer(slice).AnalyzeWithConfig(analysis.FullAnalysisConfig())

	outPath, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues:     slice,
		Stats:      &sliceStats,
		Readiness:  fullReadiness,
		ExportedAt: exportAt,
		Path:       filepath.Join(t.TempDir(), "slice.html"),
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	html := string(raw)

	const prefix = "\nconst DATA = "
	start := strings.Index(html, prefix)
	end := strings.Index(html[start+len(prefix):], ";\nconst STATUS_COLORS")
	if start < 0 || end < 0 {
		t.Fatalf("failed to locate embedded DATA JSON")
	}
	var payload struct {
		Nodes []graphNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(html[start+len(prefix):start+len(prefix)+end]), &payload); err != nil {
		t.Fatalf("Unmarshal DATA JSON: %v", err)
	}
	byID := make(map[string]graphNode, len(payload.Nodes))
	for _, n := range payload.Nodes {
		byID[n.ID] = n
	}

	// Authoritative Readiness positive & negative controls on omitted external blockers:
	if byID["slice-blocked-ext"].IsActionable || len(byID["slice-blocked-ext"].BlockedBy) != 1 || byID["slice-blocked-ext"].BlockedBy[0] != "ext-open-blocker" {
		t.Fatalf("slice-blocked-ext = %+v, want IsActionable=false BlockedBy=[ext-open-blocker]", byID["slice-blocked-ext"])
	}
	if !byID["slice-ready-ext"].IsActionable || len(byID["slice-ready-ext"].BlockedBy) != 0 {
		t.Fatalf("slice-ready-ext = %+v, want IsActionable=true BlockedBy=[] via authoritative Readiness", byID["slice-ready-ext"])
	}

	// Ephemeral, pinned, and template flags are withheld from readiness while open-root is actionable:
	for _, withheldID := range []string{"slice-ephemeral", "slice-pinned", "slice-template"} {
		if byID[withheldID].IsActionable {
			t.Fatalf("%s IsActionable = true, want false (withheld)", withheldID)
		}
	}
	if !byID["slice-ephemeral"].Ephemeral || !byID["slice-pinned"].Pinned || !byID["slice-template"].IsTemplate {
		t.Fatalf("expected ephemeral/pinned/is_template flags serialized in graphNode JSON")
	}
	if !byID["open-root"].IsActionable {
		t.Fatalf("open-root positive control IsActionable = false, want true")
	}

	// Active cut vertex vs closed historical cut vertex + active PageRankRank:
	if !byID["open-cut"].IsArticulation || byID["open-cut"].Betweenness <= 0 || byID["open-cut"].PageRankRank <= 0 {
		t.Fatalf("open-cut = %+v, want IsArticulation=true, Betweenness > 0, and active PageRankRank > 0", byID["open-cut"])
	}
	if byID["closed-cut"].IsArticulation || byID["closed-cut"].Betweenness != 0 || byID["closed-cut"].CoreNumber != 0 || byID["closed-cut"].BetweennessRank != 0 || byID["closed-cut"].PageRankRank != 0 {
		t.Fatalf("closed-cut = %+v, want IsArticulation=false Betweenness=0 CoreNumber=0 BetweennessRank=0 PageRankRank=0", byID["closed-cut"])
	}
	// Verify active PageRankRank starts at #1 among active issues without gaps from closed nodes.
	hasRankOne := false
	for id, node := range byID {
		if node.Status == "closed" || node.Status == "tombstone" {
			if node.PageRankRank != 0 {
				t.Fatalf("resolved node %s has PageRankRank=%d, want 0", id, node.PageRankRank)
			}
			continue
		}
		if node.PageRankRank == 1 {
			hasRankOne = true
		}
	}
	if !hasRankOne {
		t.Fatalf("expected top active issue to receive PageRankRank == 1 even when closed hubs exist")
	}
}

func TestGenerateInteractiveGraphHTML_SourceRepoSerializedAndFilterable(t *testing.T) {
	issues := []model.Issue{
		{ID: "api-1", Title: "API auth", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api"},
		{ID: "web-1", Title: "Web UI", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeFeature, SourceRepo: "web"},
	}
	stats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	outPath := filepath.Join(t.TempDir(), "multi_repo.html")
	written, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Stats:  &stats,
		Path:   outPath,
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}
	raw, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	html := string(raw)
	if !strings.Contains(html, `id="filter-repo"`) {
		t.Fatalf("standalone interactive graph HTML missing #filter-repo control")
	}
	const prefix = "const DATA = "
	start := strings.Index(html, prefix)
	if start < 0 {
		t.Fatalf("missing DATA payload in standalone HTML")
	}
	end := strings.Index(html[start+len(prefix):], ";\nconst STATUS_COLORS")
	if end < 0 {
		t.Fatalf("missing STATUS_COLORS terminator in standalone HTML")
	}
	var payload struct {
		Nodes []graphNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(html[start+len(prefix):start+len(prefix)+end]), &payload); err != nil {
		t.Fatalf("Unmarshal DATA JSON: %v", err)
	}
	repos := make(map[string]string, len(payload.Nodes))
	for _, n := range payload.Nodes {
		repos[n.ID] = n.SourceRepo
	}
	if repos["api-1"] != "api" || repos["web-1"] != "web" {
		t.Fatalf("graphNode.SourceRepo mismatch: %+v", repos)
	}
}

func TestGenerateInteractiveGraphHTML_CyclesWhatIfTriageTabsAndMetadata(t *testing.T) {
	est := 90
	closedAt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:               "C-1",
			Title:            "Cycle Alpha",
			Status:           model.StatusOpen,
			Priority:         0,
			IssueType:        model.TypeBug,
			EstimatedMinutes: &est,
			CreatedBy:        "alice",
			Dependencies:     []*model.Dependency{{IssueID: "C-1", DependsOnID: "C-2", Type: model.DepBlocks}},
		},
		{
			ID:           "C-2",
			Title:        "Cycle Beta",
			Status:       model.StatusOpen,
			Priority:     1,
			IssueType:    model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "C-2", DependsOnID: "C-1", Type: model.DepBlocks}},
		},
		{
			ID:          "DONE-1",
			Title:       "Shipped Foundation",
			Status:      model.StatusClosed,
			Priority:    2,
			IssueType:   model.TypeChore,
			ClosedAt:    &closedAt,
			CloseReason: "Completed and verified in CI",
		},
	}
	stats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	triageData := analysis.ComputeTriage(issues)
	triageData.Alerts = append(triageData.Alerts, analysis.Alert{
		Type:     "cycle_detected",
		Severity: "critical",
		Message:  "Active 2-node cycle",
		IssueID:  "C-1",
	})
	outPath := filepath.Join(t.TempDir(), "interactive_parity.html")
	written, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Stats:  &stats,
		Triage: &triageData,
		Path:   outPath,
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}
	raw, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	html := string(raw)

	// Guard against unescaped % in fmt.Sprintf templates.
	if strings.Contains(html, "%!(") {
		t.Fatalf("rendered standalone HTML contains Go fmt.Sprintf formatting error marker %%!(")
	}
	// Strict CSP forbids inline event attributes.
	if strings.Contains(html, "onclick=") {
		t.Fatalf("rendered standalone HTML contains CSP-violating inline onclick= attribute")
	}
	for _, marker := range []string{
		`id="btn-cycles"`,
		`id="cycle-banner"`,
		`id="ctx-whatif"`,
		`id="btn-detail-whatif"`,
		`id="whatif-banner"`,
		`id="triage-tabs"`,
		`function simulateWhatIfClose(`,
		`function handleDagCycleError(`,
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("standalone interactive HTML missing required marker %q", marker)
		}
	}

	const prefix = "const DATA = "
	start := strings.Index(html, prefix)
	if start < 0 {
		t.Fatalf("missing DATA payload")
	}
	end := strings.Index(html[start+len(prefix):], ";\nconst STATUS_COLORS")
	if end < 0 {
		t.Fatalf("missing STATUS_COLORS terminator")
	}
	var payload struct {
		Nodes       []graphNode `json:"nodes"`
		Cycles      [][]string  `json:"cycles"`
		CycleBreaks []struct {
			From         string `json:"from"`
			To           string `json:"to"`
			CyclesBroken int    `json:"cycles_broken"`
		} `json:"cycle_breaks"`
	}
	if err := json.Unmarshal([]byte(html[start+len(prefix):start+len(prefix)+end]), &payload); err != nil {
		t.Fatalf("Unmarshal DATA JSON: %v", err)
	}
	if len(payload.Cycles) == 0 {
		t.Fatalf("expected DATA.cycles to include C-1 <-> C-2 cycle, got empty")
	}
	if len(payload.Cycles[0]) != 2 {
		t.Fatalf("expected open 2-node cycle [C-1, C-2] without duplicated closing node, got %v", payload.Cycles[0])
	}
	if len(payload.CycleBreaks) == 0 {
		t.Fatalf("expected DATA.cycle_breaks to be non-empty for cyclic graph")
	}
	for _, cb := range payload.CycleBreaks {
		if cb.From == cb.To {
			t.Fatalf("DATA.cycle_breaks emitted phantom self-loop edge %s -> %s for 2-node cycle: %+v", cb.From, cb.To, payload.CycleBreaks)
		}
	}
	byID := make(map[string]graphNode, len(payload.Nodes))
	for _, n := range payload.Nodes {
		byID[n.ID] = n
	}
	if byID["C-1"].EstimatedMinutes == nil || *byID["C-1"].EstimatedMinutes != 90 || byID["C-1"].CreatedBy != "alice" {
		t.Fatalf("C-1 metadata mismatch: est=%v created_by=%q", byID["C-1"].EstimatedMinutes, byID["C-1"].CreatedBy)
	}
	if byID["DONE-1"].CloseReason != "Completed and verified in CI" {
		t.Fatalf("DONE-1 close_reason mismatch: %q", byID["DONE-1"].CloseReason)
	}
}
