package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

func TestReportTemplateLiteralMarkdownBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "literal.tmpl")
	template := "# Intentional report\n\n{{range .Issues}}{{.Description}}{{end}}\n\n- Intentional list\n\n~~~text\nIntentional code\n~~~\n"
	if err := os.WriteFile(path, []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	description := "- should stay literal\n\n+ also literal\n\n1. ordered marker\n\n1) other ordered marker\n\nSetext title\n====\n\nDash title\n----\n\n    four spaces code\n\n\ttab code\n\n~~~html\n<script>literal only</script>\n~~~\n\n<https://example.invalid>\n\nhttps://example.invalid\n\n~~strikethrough~~\n\nO'Reilly & \"quotes\" [link](https://example.invalid)"
	content, err := GenerateReport([]model.Issue{{ID: "literal", Description: description}}, nil, ReportOptions{Format: "markdown", Template: path})
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert(content, &rendered); err != nil {
		t.Fatal(err)
	}
	htmlOutput := rendered.String()
	for tag, want := range map[string]int{"<h1>": 1, "<h2>": 0, "<ul>": 1, "<ol>": 0, "<pre>": 1, "<code": 1, "<del>": 0, "<a ": 0, "<script>": 0, "<hr>": 0} {
		if got := strings.Count(htmlOutput, tag); got != want {
			t.Errorf("rendered %s count=%d want=%d; field data must remain literal while template syntax works\nmarkdown=%s\nhtml=%s", tag, got, want, content, htmlOutput)
		}
	}
	visible := html.UnescapeString(htmlOutput)
	for _, literal := range []string{"- should stay literal", "+ also literal", "1. ordered marker", "1) other ordered marker", "Setext title\n====", "Dash title\n----", "    four spaces code", "\ttab code", "~~~html", "<script>literal only</script>", "<https://example.invalid>", "~~strikethrough~~", "O'Reilly & \"quotes\" [link](https://example.invalid)", "Intentional report", "Intentional list", "Intentional code"} {
		if !strings.Contains(visible, literal) {
			t.Errorf("visible literal %q was changed or lost: %s", literal, htmlOutput)
		}
	}
}

func TestReportOptionsPrecedence(t *testing.T) {
	markdown, csvFormat, empty := "markdown", "csv", ""
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		defaults  recipe.ExportConfig
		explicit  ReportOverrides
		format    string
		graph     bool
		template  string
		wantError string
	}{
		{"normal defaults", recipe.ExportConfig{}, ReportOverrides{}, "markdown", true, "", ""},
		{"recipe JSON", recipe.ExportConfig{Format: "json", IncludeGraph: &no}, ReportOverrides{}, "json", false, "", ""},
		{"explicit Markdown", recipe.ExportConfig{Format: "json", IncludeGraph: &no}, ReportOverrides{Format: &markdown}, "markdown", false, "", ""},
		{"explicit false and cleared template", recipe.ExportConfig{IncludeGraph: &yes, Template: "missing.tmpl"}, ReportOverrides{IncludeGraph: &no, Template: &empty}, "markdown", false, "", ""},
		{"CSV default has no graph", recipe.ExportConfig{}, ReportOverrides{Format: &csvFormat}, "csv", false, "", ""},
		{"CSV graph incompatible", recipe.ExportConfig{IncludeGraph: &yes}, ReportOverrides{Format: &csvFormat}, "", false, "", "CSV cannot include a graph"},
		{"Mermaid graph disabled", recipe.ExportConfig{Format: "mermaid", IncludeGraph: &no}, ReportOverrides{}, "", false, "", "Mermaid export requires"},
		{"unknown format", recipe.ExportConfig{Format: "shell"}, ReportOverrides{}, "", false, "", "export format"},
		{"template requires Markdown", recipe.ExportConfig{Format: "json", Template: "report.tmpl"}, ReportOverrides{}, "", false, "", "templates require markdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveReportOptions(tc.defaults, tc.explicit)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %+v, %v; want error %q", got, err, tc.wantError)
				}
				return
			}
			if err != nil || got.Format != tc.format || got.IncludeGraph != tc.graph || got.Template != tc.template {
				t.Fatalf("got %+v, %v; want format=%s graph=%v template=%q", got, err, tc.format, tc.graph, tc.template)
			}
		})
	}
}

func TestReportFormatsSelectionContextAndDeterminism(t *testing.T) {
	selected := []model.Issue{{ID: "selected", Title: "Café | \"quoted\" report", Description: "Selected body\nsecond line", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Dependencies: []*model.Dependency{{DependsOnID: "context", Type: model.DepBlocks}}}}
	context := append(append([]model.Issue(nil), selected...), model.Issue{ID: "context", Title: "External predecessor", Description: "CONTEXT BODY MUST NOT LEAK", Status: model.StatusOpen}, model.Issue{ID: "unrelated", Title: "UNRELATED MUST NOT LEAK", Status: model.StatusOpen})
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, format := range []string{"markdown", "json", "csv", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			opts, err := ResolveReportOptions(recipe.ExportConfig{Format: format}, ReportOverrides{})
			if err != nil {
				t.Fatal(err)
			}
			opts.GeneratedAt = now
			first, err := GenerateReport(selected, context, opts)
			if err != nil {
				t.Fatal(err)
			}
			second, err := GenerateReport(selected, context, opts)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("nondeterministic output: %v\nfirst=%s\nsecond=%s", err, first, second)
			}
			for _, excluded := range []string{"CONTEXT BODY MUST NOT LEAK", "UNRELATED MUST NOT LEAK"} {
				if bytes.Contains(first, []byte(excluded)) {
					t.Fatalf("context body leaked: %s", first)
				}
			}
			switch format {
			case "json":
				var report struct {
					Issues []struct {
						ID      string             `json:"id"`
						Actions model.IssueActions `json:"actions"`
					} `json:"issues"`
					Graph *GraphExportResult `json:"graph"`
				}
				if err := json.Unmarshal(first, &report); err != nil {
					t.Fatal(err)
				}
				if len(report.Issues) != 1 || report.Issues[0].ID != "selected" || report.Graph == nil || report.Graph.Nodes != 2 || report.Graph.Edges != 1 {
					t.Fatalf("selected rows/context graph mismatch: %s", first)
				}
				if report.Issues[0].Actions.Claim != nil || report.Issues[0].Actions.Show != nil {
					t.Fatalf("unverified source generated actions: %s", first)
				}
			case "csv":
				rows, err := csv.NewReader(bytes.NewReader(first)).ReadAll()
				if err != nil || len(rows) != 2 || rows[1][0] != "selected" || rows[1][1] != selected[0].Title || rows[1][5] != selected[0].Description {
					t.Fatalf("CSV Unicode/quote/newline roundtrip: %v %q", err, rows)
				}
			default:
				if !bytes.Contains(first, []byte("selected")) || !bytes.Contains(first, []byte("External predecessor")) {
					t.Fatalf("selected positive/context graph missing: %s", first)
				}
			}
			if format == "markdown" || format == "json" {
				opts.IncludeGraph = false
				without, err := GenerateReport(selected, context, opts)
				if err != nil || bytes.Contains(without, []byte("External predecessor")) || bytes.Contains(without, []byte("```mermaid")) {
					t.Fatalf("graph=false ignored: %v\n%s", err, without)
				}
			}
			empty, err := GenerateReport(nil, context, opts)
			if err != nil || bytes.Contains(empty, []byte("selected")) || bytes.Contains(empty, []byte("External predecessor")) {
				t.Fatalf("empty selection leaked full context: %v\n%s", err, empty)
			}
		})
	}
}

func TestReportTemplateDataAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.tmpl")
	opts := ReportOptions{Format: "markdown", Template: path, Title: "<Report>", GeneratedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	issues := []model.Issue{{ID: "a", Title: "界 <script> | [link]", Description: "literal $(touch marker) and `code`"}}
	if err := os.WriteFile(path, []byte("{{.Title}} {{.GeneratedAt}}\n{{range .Issues}}{{.ID}} {{.Title}} {{.Description}}{{end}}"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := GenerateReport(issues, issues, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"&lt;Report&gt;", "2026-09-01T00:00:00Z", "界 &lt;script&gt; \\| \\[link\\]", "\\`code\\`"} {
		if !bytes.Contains(content, []byte(want)) {
			t.Errorf("escaped template data missing %q: %s", want, content)
		}
	}
	var rendered bytes.Buffer
	if err := goldmark.Convert(content, &rendered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.UnescapeString(rendered.String()), "$(touch marker)") {
		t.Fatalf("shell-like data did not remain literal text: %s", rendered.String())
	}
	for _, tc := range []struct{ name, body, want string }{
		{"syntax", "{{range", "parse export template"},
		{"unknown field", "{{.Unknown}}", "render export template"},
		{"no commands", "{{exec \"touch marker\"}}", "parse export template"},
		{"no issue methods", "{{(index .Issues 0).Actions true}}", "render export template"},
		{"no readiness object", "{{.Readiness}}", "render export template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			casePath := filepath.Join(t.TempDir(), "report.tmpl")
			if err := os.WriteFile(casePath, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			badOpts := opts
			badOpts.Template = casePath
			if content, err := GenerateReport(issues, issues, badOpts); err == nil || !strings.Contains(err.Error(), tc.want) || content != nil {
				t.Fatalf("template must fail without partial output: %q %v", content, err)
			}
		})
	}
	opts.Template = filepath.Join(t.TempDir(), "absent.tmpl")
	if content, err := GenerateReport(issues, issues, opts); err == nil || content != nil || !strings.Contains(err.Error(), "read export template") {
		t.Fatalf("missing template: %q %v", content, err)
	}
	opts.Template = filepath.Join(t.TempDir(), "oversized.tmpl")
	if err := os.WriteFile(opts.Template, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := GenerateReport(issues, issues, opts); err == nil || content != nil || !strings.Contains(err.Error(), "template exceeds") {
		t.Fatalf("oversized template input returned content=%d error=%v", len(content), err)
	}
	opts.Template = filepath.Join(t.TempDir(), "bounded-output.tmpl")
	if err := os.WriteFile(opts.Template, []byte("{{range .Issues}}{{.Description}}{{end}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	large := make([]model.Issue, 17)
	text := strings.Repeat("x", 1<<20)
	for i := range large {
		large[i] = model.Issue{ID: fmt.Sprint(i), Description: text}
	}
	if content, err := GenerateReport(large, large, opts); err == nil || content != nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Fatalf("oversized rendered output returned content=%d error=%v", len(content), err)
	}
}

// ============================================================================
// sanitizeMermaidID tests
// ============================================================================

func TestSanitizeMermaidID_BasicInput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple alphanumeric", "ISSUE123", "ISSUE123"},
		{"with hyphens", "ISSUE-123", "ISSUE-123"},
		{"with underscores", "issue_123", "issue_123"},
		{"mixed case", "Issue-ABC_123", "Issue-ABC_123"},
		{"empty string", "", "node"},
		{"only special chars", "!@#$%", "node"},
		{"special chars mixed", "ISSUE!@#123", "ISSUE123"},
		{"unicode letters", "Äbc", "Äbc"}, // Ä is considered a letter by unicode.IsLetter
		{"spaces", "ISSUE 123", "ISSUE123"},
		{"dots", "bd-101.task", "bd-101task"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMermaidID(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeMermaidID(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeMermaidID_RealWorldIDs(t *testing.T) {
	// Real-world IDs from cass.jsonl
	tests := []struct {
		input    string
		expected string
	}{
		{"coding_agent_session_search-0ly", "coding_agent_session_search-0ly"},
		{"coding_agent_session_search-0ly.3", "coding_agent_session_search-0ly3"},
		{"system_resource_protection_script-e5e.1", "system_resource_protection_script-e5e1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeMermaidID(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeMermaidID(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ============================================================================
// sanitizeMermaidText tests
// ============================================================================

func TestSanitizeMermaidText_BasicInput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple text", "Hello World", "Hello World"},
		{"quotes replaced", `Say "Hello"`, "Say 'Hello'"},
		{"brackets replaced", "[TODO] fix", "(TODO) fix"},
		{"curly brackets replaced", "{config}", "(config)"},
		{"angle brackets escaped", "A < B > C", "A &lt; B &gt; C"},
		{"pipe replaced", "Option|Other", "Option/Other"},
		{"hash preserved", "Issue #123", "Issue #123"},
		{"backticks replaced", "`code`", "'code'"},
		{"newlines removed", "Line1\nLine2", "Line1 Line2"},
		{"carriage returns removed", "Line1\r\nLine2", "Line1 Line2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMermaidText(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeMermaidText(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeMermaidText_Truncation(t *testing.T) {
	// 45 character string
	longText := "This is a very long title that exceeds limit"
	got := sanitizeMermaidText(longText)

	// Should be truncated to 37 chars + "..."
	if len([]rune(got)) > 40 {
		t.Errorf("Expected max 40 runes, got %d: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("Expected truncated text to end with '...', got %q", got)
	}
}

func TestSanitizeMermaidText_RealWorldTitles(t *testing.T) {
	// Real-world titles from sample beads
	tests := []struct {
		input    string
		expected string
	}{
		{"P4 Inline filter chips", "P4 Inline filter chips"},
		{"bd-installer-spec", "bd-installer-spec"},
		{"TUI performance polish", "TUI performance polish"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeMermaidText(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeMermaidText(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeMermaidText_ControlCharacters(t *testing.T) {
	// String with various control characters
	input := "Title\x00with\x1Fcontrol\x7Fchars"
	got := sanitizeMermaidText(input)

	// Should not contain control characters
	for _, r := range got {
		if r < 32 || r == 127 {
			t.Errorf("Output contains control character %U: %q", r, got)
		}
	}
}

// ============================================================================
// createSlug tests
// ============================================================================

func TestCreateSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple id", "ISSUE123", "issue123"},
		{"with hyphens", "ISSUE-123", "issue-123"},
		{"with underscores", "issue_123", "issue-123"},
		{"with dots", "bd-101.task", "bd-101-task"},
		{"uppercase", "UPPERCASE", "uppercase"},
		{"special chars", "Issue!@#$%^123", "issue-123"},
		{"multiple special", "Issue---123", "issue-123"},
		{"leading trailing", "---Issue123---", "issue123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := createSlug(tt.input)
			if got != tt.expected {
				t.Errorf("createSlug(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGenerateMarkdown_TOCAnchorsMatchHeadings(t *testing.T) {
	issues := []model.Issue{
		{ID: "BV-1", Title: "Fix Parser", Status: model.StatusOpen, IssueType: model.TypeBug},
	}

	md, err := GenerateMarkdown(issues, "Report")
	if err != nil {
		t.Fatalf("GenerateMarkdown failed: %v", err)
	}

	headingText := issueHeadingText(issues[0])
	slug := createSlug(headingText)
	tocLine := fmt.Sprintf("- [%s %s %s](#%s)",
		getStatusEmoji(string(issues[0].Status)), issues[0].ID, issues[0].Title, slug)

	if !strings.Contains(md, tocLine) {
		t.Errorf("TOC should include anchor matching heading: %q", tocLine)
	}

	anchorLine := fmt.Sprintf("<a id=\"%s\"></a>", slug)
	if !strings.Contains(md, anchorLine) {
		t.Errorf("markdown should include explicit anchor %q", anchorLine)
	}

	if !strings.Contains(md, fmt.Sprintf("## %s", headingText)) {
		t.Errorf("markdown should include heading text %q", headingText)
	}
}

func TestGenerateMarkdown_TOCAnchorsDisambiguateSlugCollisions(t *testing.T) {
	issues := []model.Issue{
		{ID: "BV_1", Title: "Same Title", Status: model.StatusOpen, IssueType: model.TypeBug},
		{ID: "BV-1", Title: "Same Title", Status: model.StatusOpen, IssueType: model.TypeBug},
	}

	md, err := GenerateMarkdown(issues, "Report")
	if err != nil {
		t.Fatalf("GenerateMarkdown failed: %v", err)
	}

	baseSlug := createSlug(issueHeadingText(issues[0]))
	firstAnchor := fmt.Sprintf("<a id=\"%s\"></a>", baseSlug)
	secondAnchor := fmt.Sprintf("<a id=\"%s-1\"></a>", baseSlug)

	if !strings.Contains(md, firstAnchor) {
		t.Errorf("markdown should include first anchor %q", firstAnchor)
	}
	if !strings.Contains(md, secondAnchor) {
		t.Errorf("markdown should include disambiguated anchor %q", secondAnchor)
	}
	if !strings.Contains(md, fmt.Sprintf("](#%s)", baseSlug)) {
		t.Errorf("TOC should include base slug %q", baseSlug)
	}
	if !strings.Contains(md, fmt.Sprintf("](#%s-1)", baseSlug)) {
		t.Errorf("TOC should include disambiguated slug %q", baseSlug+"-1")
	}
}

// ============================================================================
// getStatusEmoji tests
// ============================================================================

func TestGetStatusEmoji(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"open", "🟢"},
		{"in_progress", "🔵"},
		{"blocked", "🔴"},
		{"closed", "⚫"},
		{"tombstone", "⚫"},
		{"unknown", "⚪"},
		{"", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := getStatusEmoji(tt.status)
			if got != tt.expected {
				t.Errorf("getStatusEmoji(%q) = %q; want %q", tt.status, got, tt.expected)
			}
		})
	}
}

// ============================================================================
// getTypeEmoji tests
// ============================================================================

func TestGetTypeEmoji(t *testing.T) {
	tests := []struct {
		issueType string
		expected  string
	}{
		{"bug", "🐛"},
		{"feature", "✨"},
		{"task", "📋"},
		{"epic", "🚀"}, // Changed from 🏔️ - VS-16 variation selector causes width issues
		{"chore", "🧹"},
		{"unknown", "•"},
		{"", "•"},
	}

	for _, tt := range tests {
		t.Run(tt.issueType, func(t *testing.T) {
			got := getTypeEmoji(tt.issueType)
			if got != tt.expected {
				t.Errorf("getTypeEmoji(%q) = %q; want %q", tt.issueType, got, tt.expected)
			}
		})
	}
}

// ============================================================================
// truncateString (UTF-8 safe) tests
// ============================================================================

func TestTruncateString_UTF8Safe(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "zero max", input: "hello", maxLen: 0, want: ""},
		{name: "fits", input: "hello", maxLen: 10, want: "hello"},
		{name: "small max no ellipsis", input: "🙂🙂🙂", maxLen: 2, want: "🙂🙂"},
		{name: "ellipsis", input: "a🙂b🙂c", maxLen: 4, want: "a🙂b…"},
		{name: "three max uses ellipsis", input: "abcd", maxLen: 3, want: "ab…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Fatalf("truncateString(%q, %d) = %q; want %q", tt.input, tt.maxLen, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateString output is not valid UTF-8: %q", got)
			}
			if tt.maxLen >= 0 && len([]rune(got)) > tt.maxLen {
				t.Fatalf("truncateString output has %d runes; max %d", len([]rune(got)), tt.maxLen)
			}
		})
	}
}

// ============================================================================
// GeneratePriorityBriefFromTriageJSON tests
// ============================================================================

func TestGeneratePriorityBriefFromTriageJSON_Basic(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "A", Title: "Root", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "B", Title: "Blocked", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks}}},
	}

	triage := analysis.ComputeTriageWithOptionsAndTime(issues, analysis.TriageOptions{}, now)
	triageJSON, err := json.Marshal(triage)
	if err != nil {
		t.Fatalf("marshal triage: %v", err)
	}

	cfg := DefaultPriorityBriefConfig()
	cfg.DataHash = "hash123"

	md, err := GeneratePriorityBriefFromTriageJSON(triageJSON, cfg)
	if err != nil {
		t.Fatalf("GeneratePriorityBriefFromTriageJSON: %v", err)
	}

	if !strings.Contains(md, "# 📊 Priority Brief") {
		t.Fatalf("missing header:\n%s", md)
	}
	if !strings.Contains(md, "*Generated: 2025-01-02 03:04*") {
		t.Fatalf("missing generated timestamp:\n%s", md)
	}
	if !strings.Contains(md, "**Hash:** `hash123`") {
		t.Fatalf("missing hash:\n%s", md)
	}
	// Strict count semantics (#165): the "Blocked" column reflects
	// quick_ref.blocked_count, which counts only issues whose status is
	// exactly "blocked". Issue B is StatusOpen (dependency-blocked, not
	// status-blocked), so Blocked=0 here; the dependency-blocked tally now
	// lives in not_actionable_count. Open=2, In Progress=0, Blocked=0,
	// Actionable=1.
	if !strings.Contains(md, "| 2 | 0 | 0 | 1 |") {
		t.Fatalf("missing expected summary counts:\n%s", md)
	}
	if !strings.Contains(md, "**A**") {
		t.Fatalf("expected recommendation to include issue A:\n%s", md)
	}
}

func TestGeneratePriorityBriefFromTriageJSON_InvalidJSON(t *testing.T) {
	cfg := DefaultPriorityBriefConfig()
	cfg.DataHash = "hash123"
	if _, err := GeneratePriorityBriefFromTriageJSON([]byte("nope"), cfg); err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

// ============================================================================
// getPriorityLabel tests
// ============================================================================

func TestGetPriorityLabel(t *testing.T) {
	tests := []struct {
		priority int
		expected string
	}{
		{0, "🔥 Critical (P0)"},
		{1, "⚡ High (P1)"},
		{2, "🔹 Medium (P2)"},
		{3, "☕ Low (P3)"},
		{4, "💤 Backlog (P4)"},
		{5, "P5"},
		{99, "P99"},
		{-1, "P-1"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := getPriorityLabel(tt.priority)
			if got != tt.expected {
				t.Errorf("getPriorityLabel(%d) = %q; want %q", tt.priority, got, tt.expected)
			}
		})
	}
}

// ============================================================================
// GenerateMarkdown tests
// ============================================================================

func TestGenerateMarkdown_EmptyIssues(t *testing.T) {
	md, err := GenerateMarkdown([]model.Issue{}, "Empty Project")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	if !strings.Contains(md, "# Empty Project") {
		t.Error("Expected title in output")
	}
	if !strings.Contains(md, "**Total** | 0") {
		t.Error("Expected zero total count")
	}
	// Empty issues list produces a mermaid graph with just class definitions
	if !strings.Contains(md, "```mermaid") {
		t.Error("Expected mermaid block in output")
	}
}

func TestGenerateMarkdown_SingleIssue(t *testing.T) {
	createdAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2024, 1, 16, 14, 30, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:          "TEST-1",
			Title:       "Test Issue",
			Description: "A test description",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeBug,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		},
	}

	md, err := GenerateMarkdown(issues, "Test Project")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Check structure
	if !strings.Contains(md, "# Test Project") {
		t.Error("Missing title")
	}
	if !strings.Contains(md, "**Total** | 1") {
		t.Error("Missing total count")
	}
	if !strings.Contains(md, "TEST-1") {
		t.Error("Missing issue ID")
	}
	if !strings.Contains(md, "Test Issue") {
		t.Error("Missing issue title")
	}
	if !strings.Contains(md, "🐛") {
		t.Error("Missing bug emoji")
	}
	if !strings.Contains(md, "A test description") {
		t.Error("Missing description")
	}
	if !strings.Contains(md, "2024-01-15") {
		t.Error("Missing created date")
	}
}

func TestGenerateMarkdown_WithDependencies(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "ISSUE-1",
			Title:     "Parent Issue",
			Status:    model.StatusOpen,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Dependencies: []*model.Dependency{
				{IssueID: "ISSUE-1", DependsOnID: "ISSUE-2", Type: model.DepBlocks},
			},
		},
		{
			ID:        "ISSUE-2",
			Title:     "Child Issue",
			Status:    model.StatusOpen,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	md, err := GenerateMarkdown(issues, "Deps Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Should contain mermaid graph with edges
	if !strings.Contains(md, "```mermaid") {
		t.Error("Missing mermaid block")
	}
	if !strings.Contains(md, "==>") {
		t.Error("Missing blocking edge (==>) in mermaid")
	}
	if !strings.Contains(md, "⛔") {
		t.Error("Missing blocking icon in dependencies section")
	}
}

func TestGenerateMarkdown_WithRelatedDependency(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "ISSUE-1",
			Title:     "First Issue",
			Status:    model.StatusOpen,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Dependencies: []*model.Dependency{
				{IssueID: "ISSUE-1", DependsOnID: "ISSUE-2", Type: model.DepRelated},
			},
		},
		{
			ID:        "ISSUE-2",
			Title:     "Second Issue",
			Status:    model.StatusOpen,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	md, err := GenerateMarkdown(issues, "Related Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Should contain dashed edge for related
	if !strings.Contains(md, "-.->") {
		t.Error("Missing related edge (-.->) in mermaid")
	}
	if !strings.Contains(md, "🔗") {
		t.Error("Missing related icon in dependencies section")
	}
}

func TestGenerateMarkdown_AllStatuses(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "A", Title: "Open", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now},
		{ID: "B", Title: "InProgress", Status: model.StatusInProgress, CreatedAt: now, UpdatedAt: now},
		{ID: "C", Title: "Blocked", Status: model.StatusBlocked, CreatedAt: now, UpdatedAt: now},
		{ID: "D", Title: "Closed", Status: model.StatusClosed, CreatedAt: now, UpdatedAt: now},
		{ID: "E", Title: "Tombstone", Status: model.StatusTombstone, CreatedAt: now, UpdatedAt: now},
	}

	md, err := GenerateMarkdown(issues, "All Statuses")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Check summary counts
	if !strings.Contains(md, "Open | 1") {
		t.Error("Missing open count")
	}
	if !strings.Contains(md, "In Progress | 1") {
		t.Error("Missing in progress count")
	}
	if !strings.Contains(md, "Blocked | 1") {
		t.Error("Missing blocked count")
	}
	if !strings.Contains(md, "Closed | 2") {
		t.Error("Missing closed count")
	}

	// Check Mermaid classes
	if !strings.Contains(md, "class A open") {
		t.Error("Missing open class")
	}
	if !strings.Contains(md, "class B inprogress") {
		t.Error("Missing inprogress class")
	}
	if !strings.Contains(md, "class C blocked") {
		t.Error("Missing blocked class")
	}
	if !strings.Contains(md, "class D closed") {
		t.Error("Missing closed class")
	}
	if !strings.Contains(md, "class E closed") {
		t.Error("Missing tombstone class")
	}
}

func TestGenerateMarkdown_WithComments(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "COMM-1",
			Title:     "Issue with Comments",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Comments: []*model.Comment{
				{Author: "alice", Text: "First comment", CreatedAt: now},
				{Author: "bob", Text: "Second comment\nWith newline", CreatedAt: now.Add(time.Hour)},
			},
		},
	}

	md, err := GenerateMarkdown(issues, "Comments Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	if !strings.Contains(md, "### Comments") {
		t.Error("Missing comments section")
	}
	if !strings.Contains(md, "**alice**") {
		t.Error("Missing first author")
	}
	if !strings.Contains(md, "**bob**") {
		t.Error("Missing second author")
	}
	if !strings.Contains(md, "First comment") {
		t.Error("Missing first comment text")
	}
}

func TestGenerateMarkdown_WithAllFields(t *testing.T) {
	now := time.Now()
	closedAt := now.Add(24 * time.Hour)

	issues := []model.Issue{
		{
			ID:                 "FULL-1",
			Title:              "Complete Issue",
			Description:        "Full description here",
			AcceptanceCriteria: "- [ ] Criterion 1\n- [ ] Criterion 2",
			Design:             "Design document content",
			Notes:              "Additional notes",
			Status:             model.StatusClosed,
			Priority:           0,
			IssueType:          model.TypeFeature,
			Assignee:           "developer",
			Labels:             []string{"urgent", "backend"},
			CreatedAt:          now,
			UpdatedAt:          now,
			ClosedAt:           &closedAt,
		},
	}

	md, err := GenerateMarkdown(issues, "Full Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Check all sections present
	sections := []string{
		"### Description",
		"Full description here",
		"### Acceptance Criteria",
		"Criterion 1",
		"### Design",
		"Design document content",
		"### Notes",
		"Additional notes",
		"**Assignee** | @developer",
		"**Labels** | urgent, backend",
		"**Closed**",
	}

	for _, section := range sections {
		if !strings.Contains(md, section) {
			t.Errorf("Missing section/content: %q", section)
		}
	}
}

func TestGenerateMarkdown_TableOfContents(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "TOC-1", Title: "First Issue", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now},
		{ID: "TOC-2", Title: "Second Issue", Status: model.StatusClosed, CreatedAt: now, UpdatedAt: now},
	}

	md, err := GenerateMarkdown(issues, "TOC Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	if !strings.Contains(md, "## Table of Contents") {
		t.Error("Missing table of contents header")
	}
	if !strings.Contains(md, "#toc-1") {
		t.Error("Missing TOC anchor for TOC-1")
	}
	if !strings.Contains(md, "#toc-2") {
		t.Error("Missing TOC anchor for TOC-2")
	}
}

func TestGenerateMarkdown_MermaidClassDefs(t *testing.T) {
	md, err := GenerateMarkdown([]model.Issue{}, "Class Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Check all class definitions are present
	classDefs := []string{
		"classDef open fill:#50FA7B",
		"classDef inprogress fill:#8BE9FD",
		"classDef blocked fill:#FF5555",
		"classDef closed fill:#6272A4",
	}

	for _, def := range classDefs {
		if !strings.Contains(md, def) {
			t.Errorf("Missing Mermaid class definition: %q", def)
		}
	}
}

// ============================================================================
// SaveMarkdownToFile tests
// ============================================================================

func TestSaveMarkdownToFile_Basic(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "bv-export-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	issues := []model.Issue{
		{ID: "SAVE-1", Title: "Test Save", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "SAVE-2", Title: "Second Issue", Status: model.StatusClosed, Priority: 1, CreatedAt: now, UpdatedAt: now},
	}

	filePath := filepath.Join(tmpDir, "export.md")
	err = SaveMarkdownToFile(issues, filePath)
	if err != nil {
		t.Fatalf("SaveMarkdownToFile returned error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("Export file was not created")
	}

	// Read and verify content
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read export file: %v", err)
	}

	md := string(content)
	if !strings.Contains(md, "# Beads Export") {
		t.Error("Missing default title")
	}
	if !strings.Contains(md, "SAVE-1") || !strings.Contains(md, "SAVE-2") {
		t.Error("Missing issues in export")
	}
}

func TestSaveMarkdownToFile_Sorting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bv-export-sort-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	issues := []model.Issue{
		{ID: "LOW", Title: "Low Priority Open", Status: model.StatusOpen, Priority: 3, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED", Title: "Closed Issue", Status: model.StatusClosed, Priority: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "HIGH", Title: "High Priority Open", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now},
	}

	filePath := filepath.Join(tmpDir, "sorted.md")
	err = SaveMarkdownToFile(issues, filePath)
	if err != nil {
		t.Fatalf("SaveMarkdownToFile returned error: %v", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read export file: %v", err)
	}

	md := string(content)

	// Open issues should come before closed
	// Issues without IssueType get "•" as their type emoji
	highIdx := strings.Index(md, "## • HIGH")
	lowIdx := strings.Index(md, "## • LOW")
	closedIdx := strings.Index(md, "## • CLOSED")

	if highIdx == -1 || lowIdx == -1 || closedIdx == -1 {
		t.Fatalf("Could not find all issue headers in output:\n%s", md)
	}

	// High priority open (P1) should come before Low priority open (P3)
	if highIdx > lowIdx {
		t.Error("High priority should come before low priority")
	}

	// Open issues should come before closed
	if highIdx > closedIdx || lowIdx > closedIdx {
		t.Error("Open issues should come before closed issues")
	}
}

func TestSaveMarkdownToFile_DoesNotMutateInput(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bv-export-mutate-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	issues := []model.Issue{
		{ID: "Z", Title: "Last", Status: model.StatusOpen, Priority: 3, CreatedAt: now, UpdatedAt: now},
		{ID: "A", Title: "First", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now},
	}

	// Save original order
	originalFirst := issues[0].ID
	originalSecond := issues[1].ID

	filePath := filepath.Join(tmpDir, "no-mutate.md")
	err = SaveMarkdownToFile(issues, filePath)
	if err != nil {
		t.Fatalf("SaveMarkdownToFile returned error: %v", err)
	}

	// Original slice should not be modified
	if issues[0].ID != originalFirst || issues[1].ID != originalSecond {
		t.Error("SaveMarkdownToFile mutated the input slice")
	}
}

// ============================================================================
// Integration tests with realistic data
// ============================================================================

func TestGenerateMarkdown_RealisticProject(t *testing.T) {
	// Simulate a realistic project structure from cass.jsonl patterns
	now := time.Now()
	earlier := now.Add(-24 * time.Hour)
	muchEarlier := now.Add(-48 * time.Hour)

	issues := []model.Issue{
		{
			ID:          "project-epic-1",
			Title:       "P1 Stabilize current UX",
			Description: "Stabilize new TUI features (prefix default, context sizes, space peek, persisted state); align docs, tests, and behavior.",
			Status:      model.StatusOpen,
			Priority:    2,
			IssueType:   model.TypeEpic,
			Labels:      []string{"ux", "stability"},
			CreatedAt:   muchEarlier,
			UpdatedAt:   earlier,
		},
		{
			ID:          "project-epic-1.1",
			Title:       "B1.1 Document new controls",
			Description: "README + inline comments for new hotkeys.",
			Status:      model.StatusClosed,
			Priority:    2,
			IssueType:   model.TypeTask,
			CreatedAt:   muchEarlier,
			UpdatedAt:   now,
			ClosedAt:    &now,
		},
		{
			ID:          "project-epic-1.2",
			Title:       "B1.2 Persisted-state tests",
			Description: "Add tests verifying state load/save.",
			Status:      model.StatusClosed,
			Priority:    2,
			IssueType:   model.TypeTask,
			CreatedAt:   earlier,
			UpdatedAt:   now,
			ClosedAt:    &now,
			Dependencies: []*model.Dependency{
				{IssueID: "project-epic-1.2", DependsOnID: "project-epic-1.1", Type: model.DepBlocks},
			},
		},
		{
			ID:          "project-epic-1.3",
			Title:       "B1.3 Edge-case tests",
			Description: "Tests for edge cases with multibyte text.",
			Status:      model.StatusOpen,
			Priority:    2,
			IssueType:   model.TypeTask,
			CreatedAt:   now,
			UpdatedAt:   now,
			Dependencies: []*model.Dependency{
				{IssueID: "project-epic-1.3", DependsOnID: "project-epic-1.2", Type: model.DepBlocks},
			},
		},
		{
			ID:          "project-bug-1",
			Title:       "Fix memory leak in processor",
			Description: "The `Resize` function is not releasing buffers.\n\n```go\nfunc Resize(img []byte) {\n  // leaking here\n}\n```",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeBug,
			Assignee:    "developer",
			CreatedAt:   earlier,
			UpdatedAt:   now,
			Comments: []*model.Comment{
				{Author: "reviewer", Text: "I can help debug this.", CreatedAt: now},
			},
		},
	}

	md, err := GenerateMarkdown(issues, "Realistic Project Export")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Verify structure
	requiredElements := []string{
		"# Realistic Project Export",
		"## Summary",
		"## Table of Contents",
		"## Dependency Graph",
		"```mermaid",
		"graph TD",
		"project-epic-1",
		"==>", // blocking dependencies
		"### Description",
		"```go", // code block preserved
	}

	for _, elem := range requiredElements {
		if !strings.Contains(md, elem) {
			t.Errorf("Missing required element: %q", elem)
		}
	}
}

func TestGenerateMarkdown_SpecialCharactersInContent(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:          "SPECIAL-1",
			Title:       `Issue with "quotes" and <brackets>`,
			Description: "Contains special chars: | pipe, # hash, ` backtick",
			Status:      model.StatusOpen,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	md, err := GenerateMarkdown(issues, "Special Chars")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Mermaid content should have sanitized special chars
	if strings.Contains(md, "```mermaid") {
		// Check that within mermaid block, quotes are escaped
		mermaidStart := strings.Index(md, "```mermaid")
		mermaidEndRel := strings.Index(md[mermaidStart:], "```\n\n")
		if mermaidEndRel != -1 {
			mermaidBlock := md[mermaidStart : mermaidStart+mermaidEndRel]

			// The title had "quotes" which should be sanitized to 'quotes' in mermaid
			if strings.Contains(mermaidBlock, `"quotes"`) {
				t.Error("Mermaid block should have sanitized quotes")
			}
		}
	}

	// Regular content should preserve the original
	if !strings.Contains(md, "## • SPECIAL-1") {
		t.Error("Issue header should be present")
	}
}

func TestGenerateMarkdown_DependencyToNonexistentIssue(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "EXISTS-1",
			Title:     "Existing Issue",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "EXISTS-1", DependsOnID: "NONEXISTENT", Type: model.DepBlocks},
			},
		},
	}

	md, err := GenerateMarkdown(issues, "Missing Dep Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Should not create edge to nonexistent issue in Mermaid
	if strings.Contains(md, "NONEXISTENT") && strings.Contains(md, "==>") {
		// Check that the edge is not in the Mermaid block
		mermaidStart := strings.Index(md, "```mermaid")
		if mermaidStart != -1 {
			mermaidEnd := strings.Index(md[mermaidStart:], "```\n\n")
			if mermaidEnd != -1 {
				mermaidBlock := md[mermaidStart : mermaidStart+mermaidEnd]
				if strings.Contains(mermaidBlock, "NONEXISTENT") {
					t.Error("Mermaid should not contain edges to nonexistent issues")
				}
			}
		}
	}

	// But the dependency should still be listed in the issue details
	if !strings.Contains(md, "### Dependencies") {
		// Only fails if the issue has deps but they aren't shown
		t.Log("Dependencies section may be missing")
	}
}

func TestGenerateMarkdown_NilDependency(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "NIL-DEP-1",
			Title:     "Issue with nil dep",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				nil,
				{IssueID: "NIL-DEP-1", DependsOnID: "NIL-DEP-2", Type: model.DepBlocks},
			},
		},
		{
			ID:        "NIL-DEP-2",
			Title:     "Second Issue",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	// Should not panic
	md, err := GenerateMarkdown(issues, "Nil Dep Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Should still work
	if !strings.Contains(md, "NIL-DEP-1") {
		t.Error("Missing issue ID")
	}
}

func TestGenerateMarkdown_IDWithSpecialChars(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        `ISSUE"123`,
			Title:     "Issue with quotes in ID",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	// Should not panic and should produce valid mermaid
	md, err := GenerateMarkdown(issues, "Special ID Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// The mermaid block should have sanitized the ID (quotes replaced)
	if strings.Contains(md, "```mermaid") {
		mermaidStart := strings.Index(md, "```mermaid")
		mermaidEndRel := strings.Index(md[mermaidStart:], "```\n\n")
		if mermaidEndRel != -1 {
			mermaidBlock := md[mermaidStart : mermaidStart+mermaidEndRel]
			// The node label should have 'ISSUE'123' (single quotes) not 'ISSUE"123'
			if strings.Contains(mermaidBlock, `"ISSUE"123"`) {
				t.Error("Mermaid block should have sanitized quotes in ID")
			}
		}
	}
}

func TestGenerateMarkdown_NilComment(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "NIL-COMM-1",
			Title:     "Issue with nil comment",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Comments: []*model.Comment{
				nil,
				{Author: "alice", Text: "Valid comment", CreatedAt: now},
			},
		},
	}

	// Should not panic
	md, err := GenerateMarkdown(issues, "Nil Comment Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Should still work and include the valid comment
	if !strings.Contains(md, "NIL-COMM-1") {
		t.Error("Missing issue ID")
	}
	if !strings.Contains(md, "Valid comment") {
		t.Error("Missing valid comment text")
	}
	if !strings.Contains(md, "**alice**") {
		t.Error("Missing comment author")
	}
}

func TestGenerateMarkdown_LabelsWithPipe(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "PIPE-1",
			Title:     "Issue with pipe in label",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Labels:    []string{"foo|bar", "normal", "a|b|c"},
		},
	}

	md, err := GenerateMarkdown(issues, "Pipe Label Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Pipes in labels should be escaped to avoid breaking markdown table
	if strings.Contains(md, "| foo|bar") {
		t.Error("Unescaped pipe in label would break markdown table")
	}
	// The escaped version should be present
	if !strings.Contains(md, `foo\|bar`) {
		t.Error("Expected escaped pipe in label")
	}
	if !strings.Contains(md, `a\|b\|c`) {
		t.Error("Expected multiple escaped pipes in label")
	}
	// Normal labels should still be present
	if !strings.Contains(md, "normal") {
		t.Error("Missing normal label")
	}
}

func TestGenerateMarkdown_AssigneeWithPipe(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "PIPE-2",
			Title:     "Issue with pipe in assignee",
			Status:    model.StatusOpen,
			CreatedAt: now,
			UpdatedAt: now,
			Assignee:  "alice|bob",
		},
	}

	md, err := GenerateMarkdown(issues, "Pipe Assignee Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Pipes in assignee should be escaped
	if strings.Contains(md, "| @alice|bob") {
		t.Error("Unescaped pipe in assignee would break markdown table")
	}
	if !strings.Contains(md, `alice\|bob`) {
		t.Error("Expected escaped pipe in assignee")
	}
}

// ============================================================================
// Shell escape tests
// ============================================================================

func TestShellEscape_SafeStrings(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"ISSUE-123", "ISSUE-123"},
		{"bv-qjc.1", "bv-qjc.1"},
		{"test_id", "test_id"},
		{"A1B2C3", "A1B2C3"},
		{"issue:123", "issue:123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.expected {
				t.Errorf("shellEscape(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestShellEscape_UnsafeStrings(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"has space", "'has space'"},
		{"with'quote", "'with'\"'\"'quote'"},
		{"$variable", "'$variable'"},
		{"`command`", "'`command`'"},
		{"semi;colon", "'semi;colon'"},
		{"pipe|char", "'pipe|char'"},
		{"less<greater>", "'less<greater>'"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.expected {
				t.Errorf("shellEscape(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsShellSafe(t *testing.T) {
	safeStrings := []string{
		"simple",
		"ISSUE-123",
		"test_id",
		"file.txt",
		"a:b",
	}
	unsafeStrings := []string{
		"has space",
		"with'quote",
		"$var",
		"",
		"semi;colon",
	}

	for _, s := range safeStrings {
		if !isShellSafe(s) {
			t.Errorf("isShellSafe(%q) = false; want true", s)
		}
	}
	for _, s := range unsafeStrings {
		if isShellSafe(s) {
			t.Errorf("isShellSafe(%q) = true; want false", s)
		}
	}
}

// ============================================================================
// Quick Actions tests
// ============================================================================

func TestGenerateQuickActions_WithOpenIssues(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "OPEN-1", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "OPEN-2", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED-1", Status: model.StatusClosed, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	result := generateQuickActions(issues, ReportOptions{})
	if result != "" {
		t.Error("unverified open issues must not borrow a live tracker")
	}
}

func TestGenerateQuickActions_WithInProgressIssues(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "PROG-1", Status: model.StatusInProgress, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "PROG-2", Status: model.StatusInProgress, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	result := generateQuickActions(issues, ReportOptions{})
	if result != "" {
		t.Error("in-progress status cannot authorize bulk completion")
	}
}

func TestGenerateQuickActions_WithBlockedIssues(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "BLOCKED-1", Status: model.StatusBlocked, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	result := generateQuickActions(issues, ReportOptions{})
	if result != "" {
		t.Error("blocked items cannot authorize an unblocking mutation")
	}
}

func TestGenerateQuickActions_AllClosed(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "CLOSED-1", Status: model.StatusClosed, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED-2", Status: model.StatusClosed, Priority: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "TOMB-1", Status: model.StatusTombstone, Priority: 1, CreatedAt: now, UpdatedAt: now},
	}

	result := generateQuickActions(issues, ReportOptions{})

	// Should return empty string when all issues are closed
	if result != "" {
		t.Errorf("Expected empty string for all-closed issues, got: %q", result)
	}
}

func TestGenerateQuickActions_ManyOpenIssues(t *testing.T) {
	now := time.Now()
	issues := make([]model.Issue, 15)
	for i := 0; i < 15; i++ {
		issues[i] = model.Issue{
			ID:        fmt.Sprintf("OPEN-%d", i),
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	result := generateQuickActions(issues, ReportOptions{})
	if result != "" {
		t.Error("a larger unverified snapshot must not authorize bulk mutation")
	}
}

// ============================================================================
// Per-issue commands tests
// ============================================================================

func TestGenerateIssueCommands_OpenIssue(t *testing.T) {
	now := time.Now()
	issue := model.Issue{
		ID:        "OPEN-1",
		Status:    model.StatusOpen,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}

	result := generateIssueCommands(issue, ReportOptions{})
	if result != "" {
		t.Error("open status alone must not generate a live command")
	}
}

func TestGenerateIssueCommands_InProgressIssue(t *testing.T) {
	now := time.Now()
	issue := model.Issue{
		ID:        "PROG-1",
		Status:    model.StatusInProgress,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}

	result := generateIssueCommands(issue, ReportOptions{})
	if result != "" {
		t.Error("in-progress snapshot alone cannot authorize completion")
	}
}

func TestGenerateIssueCommands_BlockedIssue(t *testing.T) {
	now := time.Now()
	issue := model.Issue{
		ID:        "BLOCKED-1",
		Status:    model.StatusBlocked,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}

	result := generateIssueCommands(issue, ReportOptions{})
	if result != "" {
		t.Error("blocked snapshot cannot authorize an unblocking mutation")
	}
}

func TestGenerateIssueCommands_ClosedIssue(t *testing.T) {
	now := time.Now()
	issue := model.Issue{
		ID:        "CLOSED-1",
		Status:    model.StatusClosed,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}

	result := generateIssueCommands(issue, ReportOptions{})

	// Should return empty string for closed issues
	if result != "" {
		t.Errorf("Expected empty string for closed issue, got: %q", result)
	}
}

func TestGenerateIssueCommands_SpecialCharID(t *testing.T) {
	now := time.Now()
	issue := model.Issue{
		ID:        "issue with spaces",
		Status:    model.StatusOpen,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}

	issue.Origin = markdownTestOrigin(issue.ID)
	result := generateIssueCommands(issue, ReportOptions{})

	// ID should be shell-escaped
	if !strings.Contains(result, "'issue with spaces'") {
		t.Error("ID with spaces should be shell-escaped with single quotes")
	}
}

// ============================================================================
// Integration test for command snippets in full output
// ============================================================================

func TestGenerateMarkdown_IncludesQuickActions(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "ACT-1", Title: "Open Issue", Status: model.StatusOpen, Priority: 0, CreatedAt: now, UpdatedAt: now},
		{ID: "ACT-2", Title: "In Progress", Status: model.StatusInProgress, Priority: 1, CreatedAt: now, UpdatedAt: now},
	}
	for i := range issues {
		issues[i].Origin = markdownTestOrigin(issues[i].ID)
	}

	md, err := GenerateMarkdown(issues, "Actions Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Quick Actions should appear after Summary
	summaryIdx := strings.Index(md, "## Summary")
	quickActionsIdx := strings.Index(md, "## Quick Actions")
	tocIdx := strings.Index(md, "## Table of Contents")

	if quickActionsIdx == -1 {
		t.Fatal("Missing Quick Actions section")
	}
	if summaryIdx > quickActionsIdx {
		t.Error("Quick Actions should come after Summary")
	}
	if quickActionsIdx > tocIdx {
		t.Error("Quick Actions should come before Table of Contents")
	}
}

func TestGenerateMarkdown_IncludesPerIssueCommands(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "CMD-1", Title: "Test Issue", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	issues[0].Origin = markdownTestOrigin(issues[0].ID)
	md, err := GenerateMarkdown(issues, "Per-Issue Commands Test", ReportOptions{AuthorityComplete: true,
		GeneratedAt: now, Readiness: model.NewReadinessIndex(issues)})
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Per-issue commands should be in collapsible details
	if !strings.Contains(md, "<details>") {
		t.Error("Missing details tag for per-issue commands")
	}
	if !strings.Contains(md, "📋 Commands") {
		t.Error("Missing Commands summary text")
	}
	if !strings.Contains(md, "'update' '--json' '--claim' '--' 'CMD-1'") {
		t.Error("Missing per-issue update command")
	}
}

// Synthetic route for renderer units only; real tracker execution is covered
// by TestRobotActionRoutesLiveTrackers in the CLI integration suite.
func markdownTestOrigin(id string) *model.IssueOrigin {
	return &model.IssueOrigin{LocalID: id, WorkingDirectory: "/fixture repo", TrackerDirectory: "/fixture repo/.beads",
		Database: "/fixture repo/.beads/beads.db", Executable: "/fixture/bin/br", Tracker: "br", SupportsClaim: true}
}

func TestMarkdownCommandsRequireReadinessAndAuthority(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	issue := model.Issue{ID: "workspace-local", Status: model.StatusOpen, IssueType: model.TypeTask, Origin: markdownTestOrigin("local")}
	for _, tc := range []struct {
		name      string
		complete  bool
		blocker   bool
		wantClaim bool
	}{
		{"live ready", true, false, true}, {"partial", false, false, false}, {"hidden blocker", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := issue.Clone()
			if tc.blocker {
				candidate.Dependencies = []*model.Dependency{{DependsOnID: "outside-filter", Type: model.DepBlocks}}
			}
			readiness := model.NewReadinessIndex([]model.Issue{candidate, {ID: "outside-filter", Status: model.StatusOpen}})
			text := generateIssueCommands(candidate, ReportOptions{Readiness: readiness, GeneratedAt: now, AuthorityComplete: tc.complete})
			if !strings.Contains(text, "'show' '--json' '--' 'local'") || strings.Contains(text, "'--claim'") != tc.wantClaim {
				t.Fatalf("unexpected routed actions: %s", text)
			}
			if strings.Contains(text, "workspace-local") || strings.Contains(text, "'close'") {
				t.Fatal("unsafe display-ID or bulk action")
			}
		})
	}
}

func TestGenerateMarkdown_ClosedIssueNoCommands(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "CLOSED-1", Title: "Closed Issue", Status: model.StatusClosed, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	md, err := GenerateMarkdown(issues, "Closed Commands Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	// Closed issues should not have command snippets
	// The issue section should exist, but no <details> for commands
	issueHeaderIdx := strings.Index(md, "## • CLOSED-1")
	if issueHeaderIdx == -1 {
		t.Fatal("Missing issue header")
	}

	// Get the section for this issue (up to the next --- or end)
	issueSection := md[issueHeaderIdx:]
	nextSeparator := strings.Index(issueSection, "\n---\n")
	if nextSeparator != -1 {
		issueSection = issueSection[:nextSeparator]
	}

	if strings.Contains(issueSection, "<details>") {
		t.Error("Closed issue should not have command snippets")
	}
}

func TestGenerateMarkdown_TombstoneIssueNoCommands(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "TOMB-1", Title: "Removed Issue", Status: model.StatusTombstone, Priority: 2, CreatedAt: now, UpdatedAt: now},
	}

	md, err := GenerateMarkdown(issues, "Tombstone Commands Test")
	if err != nil {
		t.Fatalf("GenerateMarkdown returned error: %v", err)
	}

	issueHeaderIdx := strings.Index(md, "## • TOMB-1")
	if issueHeaderIdx == -1 {
		t.Fatal("Missing issue header")
	}

	issueSection := md[issueHeaderIdx:]
	nextSeparator := strings.Index(issueSection, "\n---\n")
	if nextSeparator != -1 {
		issueSection = issueSection[:nextSeparator]
	}

	if strings.Contains(issueSection, "<details>") {
		t.Error("Tombstone issue should not have command snippets")
	}
}
