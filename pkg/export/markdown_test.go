package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"runtime"
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
	markdown, csvFormat, empty, chosenTemplate := "markdown", "csv", "", "chosen.tmpl"
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
		{"untrusted template default", recipe.ExportConfig{Template: "private.txt"}, ReportOverrides{}, "", false, "", "requires explicit --export-template"},
		{"explicit template choice", recipe.ExportConfig{Template: "private.txt"}, ReportOverrides{Template: &chosenTemplate}, "markdown", true, chosenTemplate, ""},
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

func TestReportTemplateUserDefaults(t *testing.T) {
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	projectDir := filepath.Join(dir, "project")
	for _, path := range []string{userDir, projectDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	userPath := filepath.Join(userDir, "recipes.yaml")
	if err := os.WriteFile(userPath, []byte("recipes:\n  report:\n    export:\n      template: report.tmpl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(userDir, "report.tmpl"):    "USER_AUTHORIZED_TEMPLATE {{.Title}}",
		filepath.Join(projectDir, "report.tmpl"): "PROJECT_FILE_SENTINEL",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(projectDir)
	loader := recipe.NewLoader(recipe.WithUserPath(userPath), recipe.WithProjectDir(projectDir))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	defaults := loader.Get("report")
	if defaults == nil {
		t.Fatalf("user report missing: %v", loader.Warnings())
	}
	opts, err := ResolveReportOptions(defaults.Export, ReportOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	content, err := GenerateReport(nil, nil, opts)
	if err != nil || !strings.HasPrefix(string(content), "USER_AUTHORIZED_TEMPLATE ") || bytes.Contains(content, []byte("PROJECT_FILE_SENTINEL")) {
		t.Fatalf("user default must resolve beside the user config, not in the project: %q %v", content, err)
	}
	defaults.Export.Template = filepath.Join(projectDir, "report.tmpl")
	if _, err := ResolveReportOptions(defaults.Export, ReportOverrides{}); err == nil || !strings.Contains(err.Error(), "requires explicit --export-template") {
		t.Fatalf("replacing the trusted path retained authority: %v", err)
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
func TestReportWritersReplaceLinks(t *testing.T) {
	for _, writer := range []struct {
		name  string
		write func(string) error
	}{
		{"report", func(path string) error { return WriteReportFile(path, []byte("new report")) }},
		{"markdown", func(path string) error {
			return SaveMarkdownToFile([]model.Issue{{ID: "linked-report", Title: "new report", Status: model.StatusOpen}}, path)
		}},
		{"standalone JSON", func(path string) error {
			exporter := NewSQLiteExporter([]*model.Issue{{ID: "linked-report", Title: "new report", Status: model.StatusOpen}}, nil, nil, nil)
			return exporter.ExportToJSON(path)
		}},
	} {
		for _, kind := range []string{"symlink", "hardlink"} {
			t.Run(writer.name+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "outside-report")
				const sentinel = "outside fake report must remain unchanged"
				if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
				output := filepath.Join(dir, "reports")
				if err := os.Mkdir(output, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(output, "report.md")
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(target, path); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("link unavailable: %v", err)
					}
					t.Fatal(err)
				}
				if err := writer.write(path); err != nil {
					t.Fatal(err)
				}
				if content, err := os.ReadFile(target); err != nil || string(content) != sentinel {
					t.Fatalf("report overwrote outside target: %q, %v", content, err)
				}
				if content, err := os.ReadFile(path); err != nil || !bytes.Contains(content, []byte("new report")) {
					t.Fatalf("report not published: %q, %v", content, err)
				}
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("report output is not a regular file: %v, %v", info, err)
				}
			})
		}
	}
}

func TestFileExportsRejectInvalidPaths(t *testing.T) {
	for _, write := range []func(string) error{
		func(path string) error { return WriteReportFile(path, []byte("must not be written")) },
		NewSQLiteExporter(nil, nil, nil, nil).ExportToJSON,
	} {
		dir := t.TempDir()
		missingParent := filepath.Join(dir, "missing")
		for _, path := range []string{"", dir + string(os.PathSeparator), dir, filepath.Join(missingParent, "report.md")} {
			if err := write(path); err == nil {
				t.Errorf("invalid export destination %q accepted", path)
			}
		}
		if _, err := os.Stat(missingParent); !os.IsNotExist(err) {
			t.Fatalf("export unexpectedly created missing parent: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid export destination created files: %v, %v", entries, err)
		}
	}
}

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

func TestExportSurfaces_DueDateDeferUntilExternalRef(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	deferUntil := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	extRef := "GH-210|spec"
	issue := model.Issue{
		ID:          "EXP-1",
		Title:       "Full metadata export",
		Status:      model.StatusOpen,
		Priority:    1,
		IssueType:   model.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
		DueDate:     &due,
		DeferUntil:  &deferUntil,
		ExternalRef: &extRef,
	}

	md, err := GenerateMarkdown([]model.Issue{issue}, "Metadata Report")
	if err != nil {
		t.Fatalf("GenerateMarkdown failed: %v", err)
	}
	for _, want := range []string{
		"| **Due** | 2026-11-20 |",
		"| **Deferred Until** | 2026-10-25 |",
		"| **External Ref** | `GH-210\\|spec` |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown export missing %q:\n%s", want, md)
		}
	}

	sqliteExp := NewSQLiteExporter([]*model.Issue{&issue}, nil, nil, nil)
	exported := sqliteExp.GetExportedIssues()
	if len(exported) != 1 {
		t.Fatalf("GetExportedIssues returned %d items, want 1", len(exported))
	}
	if exported[0].DueDate == nil || !exported[0].DueDate.Equal(due) {
		t.Fatalf("ExportIssue.DueDate = %v, want %v", exported[0].DueDate, due)
	}
	if exported[0].DeferUntil == nil || !exported[0].DeferUntil.Equal(deferUntil) {
		t.Fatalf("ExportIssue.DeferUntil = %v, want %v", exported[0].DeferUntil, deferUntil)
	}
	if exported[0].ExternalRef != extRef {
		t.Fatalf("ExportIssue.ExternalRef = %q, want %q", exported[0].ExternalRef, extRef)
	}

	outPath, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: []model.Issue{issue},
		Title:  "Interactive Metadata",
		Path:   filepath.Join(t.TempDir(), "graph.html"),
	})
	if err != nil {
		t.Fatalf("GenerateInteractiveGraphHTML failed: %v", err)
	}
	htmlBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", outPath, err)
	}
	htmlOut := string(htmlBytes)
	for _, want := range []string{
		`"due_date":"2026-11-20"`,
		`"defer_until":"2026-10-25"`,
		`"external_ref":"GH-210|spec"`,
		`addMeta('Deferred Until', node.defer_until);`,
		`addMeta('External Ref', node.external_ref);`,
	} {
		if !strings.Contains(htmlOut, want) {
			t.Fatalf("interactive graph HTML missing %q", want)
		}
	}
}

func TestGeneratePriorityBriefFromTriageJSON_RendersAlertsAndWhatIf(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "root-1", Title: "Unblock cascade", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "dep-1", Title: "Waiting 1", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "dep-1", DependsOnID: "root-1", Type: model.DepBlocks}}},
		{ID: "dep-2", Title: "Waiting 2", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "dep-2", DependsOnID: "dep-1", Type: model.DepBlocks}}},
		{ID: "cyc-a", Title: "Cycle A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "cyc-a", DependsOnID: "cyc-b", Type: model.DepBlocks}}},
		{ID: "cyc-b", Title: "Cycle B", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "cyc-b", DependsOnID: "cyc-a", Type: model.DepBlocks}}},
	}

	triage := analysis.ComputeTriageWithOptionsAndTime(issues, analysis.TriageOptions{
		UseFastConfig:  true,
		WaitForPhase2:  true,
		ActionableOnly: true,
	}, now)
	triageJSON, err := json.Marshal(triage)
	if err != nil {
		t.Fatalf("marshal triage: %v", err)
	}

	cfg := DefaultPriorityBriefConfig()
	brief, err := GeneratePriorityBriefFromTriageJSON(triageJSON, cfg)
	if err != nil {
		t.Fatalf("GeneratePriorityBriefFromTriageJSON failed: %v", err)
	}
	for _, want := range []string{
		"## ⚠️ Alerts",
		"**cycle**",
		"## 🔮 What-If Impact",
		"**root-1**",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("priority brief missing %q:\n%s", want, brief)
		}
	}

	cfg.IncludeWhatIf = false
	withoutOptional, err := GeneratePriorityBriefFromTriageJSON(triageJSON, cfg)
	if err != nil {
		t.Fatalf("GeneratePriorityBriefFromTriageJSON(disabled) failed: %v", err)
	}
	if strings.Contains(withoutOptional, "## 🔮 What-If Impact") {
		t.Fatalf("expected IncludeWhatIf=false to omit What-If section:\n%s", withoutOptional)
	}
	if !strings.Contains(withoutOptional, "## ⚠️ Alerts") {
		t.Fatalf("expected Alerts section to remain rendered when alerts are present:\n%s", withoutOptional)
	}
}

func TestGenerateMarkdown_DeferredOtherSummaryRowAndGrouping(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "md-open", Title: "Ready API", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, Assignee: "alice", Labels: []string{"security", "backend"}, CreatedAt: now, UpdatedAt: now},
		{ID: "md-prog", Title: "Active UI", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeFeature, Assignee: "bob", Labels: []string{"frontend"}, CreatedAt: now, UpdatedAt: now},
		{ID: "md-def", Title: "Parked Sprint", Status: model.Status("deferred"), Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "md-custom", Title: "QA Signoff", Status: model.Status("qa_review"), Priority: 2, IssueType: model.TypeTask, Assignee: "alice", CreatedAt: now, UpdatedAt: now},
		{ID: "md-closed", Title: "Shipped Init", Status: model.StatusClosed, Priority: 3, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	md, err := GenerateReport(issues, nil, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
	})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	mdStr := string(md)
	for _, want := range []string{
		"| **Total** | 5 |",
		"| Open | 1 |",
		"| In Progress | 1 |",
		"| Blocked | 0 |",
		"| Deferred / Other | 2 |",
		"| Closed | 1 |",
	} {
		if !strings.Contains(mdStr, want) {
			t.Fatalf("markdown summary missing %q:\n%s", want, mdStr)
		}
	}

	// Verify standard statuses omit Deferred / Other row cleanly.
	noDeferredMD, err := GenerateReport([]model.Issue{issues[0], issues[4]}, nil, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
	})
	if err != nil {
		t.Fatalf("GenerateReport(no deferred) failed: %v", err)
	}
	if strings.Contains(string(noDeferredMD), "Deferred / Other") {
		t.Fatalf("expected Deferred / Other row to be omitted when count is 0:\n%s", string(noDeferredMD))
	}

	// Verify ResolveReportOptions + GroupBy + recipe.ViewConfig.GroupBy render grouped sections and preserve TOC anchors.
	groupByStatus := "status"
	resolvedOpts, err := ResolveReportOptions(recipe.ExportConfig{Format: "markdown"}, ReportOverrides{GroupBy: &groupByStatus})
	if err != nil {
		t.Fatalf("ResolveReportOptions(GroupBy=status) failed: %v", err)
	}
	resolvedOpts.IncludeGraph = false
	resolvedOpts.GeneratedAt = now
	groupedStatusRaw, err := GenerateReport(issues, nil, resolvedOpts)
	if err != nil {
		t.Fatalf("GenerateReport(GroupBy=status) failed: %v", err)
	}
	groupedStatus := string(groupedStatusRaw)
	for _, want := range []string{
		"## Group: 🟢 open (1)",
		"## Group: 🔵 in_progress (1)",
		"## Group: ⚪ deferred (1)",
		"## Group: ⚪ qa_review (1)",
		"## Group: ⚫ closed (1)",
		`<a id="md-open-ready-api"></a>`,
		`<a id="md-def-parked-sprint"></a>`,
	} {
		if !strings.Contains(groupedStatus, want) {
			t.Fatalf("grouped status markdown missing %q:\n%s", want, groupedStatus)
		}
	}

	// Fallback to View.GroupBy when explicit GroupBy is unset.
	viewGroupedRaw, err := GenerateReport(issues, nil, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
		View:         recipe.ViewConfig{GroupBy: "assignee"},
	})
	if err != nil {
		t.Fatalf("GenerateReport(View.GroupBy=assignee) failed: %v", err)
	}
	viewGrouped := string(viewGroupedRaw)
	alicePos := strings.Index(viewGrouped, "## Group: @alice (2)")
	bobPos := strings.Index(viewGrouped, "## Group: @bob (1)")
	unassignedPos := strings.Index(viewGrouped, "## Group: Unassigned (2)")
	if alicePos < 0 || bobPos < 0 || unassignedPos < 0 || !(alicePos < bobPos && bobPos < unassignedPos) {
		t.Fatalf("expected deterministic @alice -> @bob -> Unassigned group order:\n%s", viewGrouped)
	}
}

func TestGenerateMarkdown_ViewColumnsTagsGroupingAndEscaping(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "bv-1",
			Title:     "[RC-C17] Pipe | & Bracket [Title]\nNextLine",
			Status:    model.Status("qa|review"),
			Priority:  0,
			IssueType: model.IssueType("sec|bug"),
			Assignee:  "alice|ops\nteam",
			Labels:    []string{"auth|v2", "backend"},
			CreatedAt: now,
			UpdatedAt: now.Add(24 * time.Hour),
		},
		{
			ID:        "bv-2",
			Title:     "Downstream Consumer",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "bv-2", DependsOnID: "bv-1", Type: model.DepBlocks},
			},
		},
	}

	// Custom View.Columns + GroupBy: "tags" + TOC bracket escaping + summary/section cell escaping
	rawCustom, err := GenerateReport(issues, nil, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
		View: recipe.ViewConfig{
			GroupBy: "tags",
			Columns: []string{"id", "title", "assignee", "tags", "created", "updated", "blocks", "blocked_by"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateReport(View.Columns + tags) failed: %v", err)
	}
	mdCustom := string(rawCustom)
	for _, want := range []string{
		"| ID | Title | Assignee | Labels | Created | Updated | Blocks | Blocked By |",
		`| bv-1 | [RC-C17] Pipe \| & Bracket [Title] NextLine | @alice\|ops team | auth\|v2, backend | 2026-11-15 | 2026-11-16 | bv-2 | - |`,
		`| bv-2 | Downstream Consumer | - | - | 2026-11-15 | 2026-11-15 | - | bv-1 |`,
		`- [⚪ bv-1 \[RC-C17\] Pipe | & Bracket \[Title\] NextLine](#`,
		`## Group: auth|v2 (1)`,
		`## Group: Unlabeled (1)`,
		`| **Type** | • sec\|bug |`,
		`| **Status** | ⚪ qa\|review |`,
	} {
		if !strings.Contains(mdCustom, want) {
			t.Fatalf("expected markdown output to contain %q, got:\n%s", want, mdCustom)
		}
	}

	// Empty / unrecognized View.Columns falls back cleanly to default 5 columns
	rawDefault, err := GenerateReport(issues[:1], nil, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
		View: recipe.ViewConfig{
			Columns: []string{"unknown_col"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateReport(default fallback columns) failed: %v", err)
	}
	if !strings.Contains(string(rawDefault), "| ID | Title | Type | Priority | Status |") {
		t.Fatalf("expected fallback 5-column header, got:\n%s", string(rawDefault))
	}

	// Priority brief escapes pipes and newlines across all 4 tables
	briefJSON := `{
		"meta": {"version": "1.0", "generated_at": "2026-11-15T10:00:00Z", "phase2_ready": true, "issue_count": 2},
		"quick_ref": {"open_count": 2, "actionable_count": 1, "blocked_count": 1, "in_progress_count": 0},
		"recommendations": [{
			"id": "rec|1",
			"title": "FixA|B\nLine",
			"type": "bug",
			"status": "open",
			"priority": 0,
			"score": 0.91,
			"reasons": ["Unblocks X|Y\nNow"],
			"unblocks_ids": ["u|1", "u|2"],
			"breakdown": {"pagerank_norm": 0.8, "betweenness_norm": 0.5, "time_to_impact_norm": 0.9}
		}],
		"quick_wins": [{"id": "qw|1", "title": "Win|Title", "score": 0.8, "reason": "Fast|Reason"}],
		"blockers_to_clear": [{"id": "blk|1", "title": "Blk|Title", "unblocks_count": 2, "unblocks_ids": ["d|1"], "actionable": true}]
	}`
	brief, err := GeneratePriorityBriefFromTriageJSON([]byte(briefJSON), DefaultPriorityBriefConfig())
	if err != nil {
		t.Fatalf("GeneratePriorityBriefFromTriageJSON failed: %v", err)
	}
	for _, want := range []string{
		`**rec\|1** FixA\|B Line`,
		`Unblocks X\|Y Now`,
		`**qw\|1** Win\|Title`,
		`Fast\|Reason`,
		`**blk\|1** Blk\|Title`,
		`u\|1, u\|2`,
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("expected priority brief to escape table cell %q, got:\n%s", want, brief)
		}
	}
}

func TestGenerateReport_MetricsAllFormatsCSVColumnsHeadingNewlinesAndIssueMetadata(t *testing.T) {
	eta := 90
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(2 * time.Hour)
	issues := []model.Issue{
		{
			ID:               "rep-1",
			Title:            "Primary\r\nHeading Title That Exceeds Truncation Limit",
			Description:      "Root blocker task",
			Status:           model.StatusOpen,
			Priority:         0,
			IssueType:        model.TypeBug,
			CreatedBy:        "alice@example.com",
			EstimatedMinutes: &eta,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		{
			ID:          "rep-2",
			Title:       "Dependent Closed Item",
			Status:      model.StatusClosed,
			Priority:    2,
			IssueType:   model.TypeTask,
			CreatedBy:   "bob@example.com",
			CloseReason: "Verified in production",
			CreatedAt:   now,
			UpdatedAt:   closedAt,
			ClosedAt:    &closedAt,
			Dependencies: []*model.Dependency{
				{IssueID: "rep-2", DependsOnID: "rep-1", Type: model.DepBlocks},
			},
		},
	}

	stats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	metrics := []string{"pagerank", "betweenness", "eigenvector", "hubs", "authorities", "critical_path", "in_degree", "out_degree"}

	// 1. Markdown report: Metrics columns, heading newline stripping, and CreatedBy/EstimatedMinutes/CloseReason.
	mdBytes, err := GenerateReport(issues, issues, ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      metrics,
		GraphStats:   &stats,
	})
	if err != nil {
		t.Fatalf("GenerateReport(markdown) failed: %v", err)
	}
	md := string(mdBytes)
	for _, want := range []string{
		"| ID | Title | Type | Priority | Status | PageRank | Betweenness | Eigenvector | Hubs | Authorities | Critical Path | In-Degree | Out-Degree |",
		"## 🐛 rep-1 Primary Heading Title That Exceeds Truncation Limit\n",
		"| **Created By** | alice@example.com |",
		"| **Estimated** | 1h 30m (90m) |",
		"| **Close Reason** | Verified in production |",
		"| **PageRank** | ",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown report missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "## 🐛 rep-1 Primary\n") || strings.Contains(md, "## 🐛 rep-1 Primary\r") {
		t.Fatalf("markdown heading failed to strip CR/LF:\n%s", md)
	}

	// 2. HTML report via GenerateReport(Format: "html"): includes metrics columns and metadata rows.
	htmlBytes, err := GenerateReport(issues, issues, ReportOptions{
		Format:       "html",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"pagerank", "critical_path"},
		GraphStats:   &stats,
	})
	if err != nil {
		t.Fatalf("GenerateReport(html) failed: %v", err)
	}
	htmlOut := string(htmlBytes)
	for _, want := range []string{
		"<th>PageRank</th><th>Critical Path</th>",
		"<tr><th>Created By</th><td>alice@example.com</td></tr>",
		"<tr><th>Estimated</th><td>1h 30m (90m)</td></tr>",
		"<tr><th>Close Reason</th><td>Verified in production</td></tr>",
	} {
		if !strings.Contains(htmlOut, want) {
			t.Fatalf("HTML report missing %q", want)
		}
	}

	// 3. CSV report: honors View.Columns, View.TruncateTitle, and Metrics columns.
	csvBytes, err := GenerateReport(issues, issues, ReportOptions{
		Format:       "csv",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"pagerank", "in_degree"},
		GraphStats:   &stats,
		View: recipe.ViewConfig{
			Columns:       []string{"id", "title", "status"},
			TruncateTitle: 15,
		},
	})
	if err != nil {
		t.Fatalf("GenerateReport(csv) failed: %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	wantHeader := []string{"id", "title", "status", "pagerank", "in_degree"}
	if len(rows) != 3 || fmt.Sprint(rows[0]) != fmt.Sprint(wantHeader) {
		t.Fatalf("CSV header/rows = %q, want header %q", rows, wantHeader)
	}
	if utf8.RuneCountInString(rows[1][1]) > 15 || !strings.HasSuffix(rows[1][1], "…") {
		t.Fatalf("CSV title was not truncated to 15 runes: %q", rows[1][1])
	}

	// 4. JSON report: includes per-issue "metrics" object when Metrics + GraphStats provided.
	jsonBytes, err := GenerateReport(issues, issues, ReportOptions{
		Format:       "json",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"pagerank", "out_degree"},
		GraphStats:   &stats,
	})
	if err != nil {
		t.Fatalf("GenerateReport(json) failed: %v", err)
	}
	var jsonParsed struct {
		Issues []struct {
			ID      string             `json:"id"`
			Metrics map[string]float64 `json:"metrics"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(jsonBytes, &jsonParsed); err != nil {
		t.Fatalf("Unmarshal JSON report: %v", err)
	}
	if len(jsonParsed.Issues) != 2 {
		t.Fatalf("expected 2 JSON issues, got %d", len(jsonParsed.Issues))
	}
	if _, ok := jsonParsed.Issues[0].Metrics["pagerank"]; !ok {
		t.Fatalf("expected pagerank in JSON issue metrics, got %+v", jsonParsed.Issues[0].Metrics)
	}
	if _, ok := jsonParsed.Issues[1].Metrics["out_degree"]; !ok {
		t.Fatalf("expected out_degree in JSON issue metrics, got %+v", jsonParsed.Issues[1].Metrics)
	}
}

func TestGenerateReport_SlackUnavailableOnCyclicAndClosedIssues(t *testing.T) {
	now := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)

	// Case 1: Active cycle -> slack is unavailable ("N/A" in MD/HTML/CSV, omitted in JSON).
	cyclicIssues := []model.Issue{
		{
			ID: "cyc-1", Title: "Cycle Node 1", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "cyc-1", DependsOnID: "cyc-2", Type: model.DepBlocks}},
		},
		{
			ID: "cyc-2", Title: "Cycle Node 2", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "cyc-2", DependsOnID: "cyc-1", Type: model.DepBlocks}},
		},
	}
	cyclicStats := analysis.NewAnalyzer(cyclicIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())

	for _, format := range []string{"markdown", "html", "csv"} {
		out, err := GenerateReport(cyclicIssues, cyclicIssues, ReportOptions{
			Format:       format,
			IncludeGraph: false,
			GeneratedAt:  now,
			Metrics:      []string{"pagerank", "slack"},
			GraphStats:   &cyclicStats,
		})
		if err != nil {
			t.Fatalf("GenerateReport(%s, cyclic) failed: %v", format, err)
		}
		text := string(out)
		if !strings.Contains(text, "N/A") {
			t.Fatalf("expected %s cyclic report to render N/A for slack, got:\n%s", format, text)
		}
		if format == "markdown" && strings.Contains(text, "| **Slack** | 0.00 |") {
			t.Fatalf("markdown cyclic report falsely emitted 0.00 slack:\n%s", text)
		}
		if format == "html" && strings.Contains(text, "<tr><th>Slack</th><td>0.00</td></tr>") {
			t.Fatalf("html cyclic report falsely emitted 0.00 slack:\n%s", text)
		}
	}

	jsonOut, err := GenerateReport(cyclicIssues, cyclicIssues, ReportOptions{
		Format:       "json",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"pagerank", "slack"},
		GraphStats:   &cyclicStats,
	})
	if err != nil {
		t.Fatalf("GenerateReport(json, cyclic) failed: %v", err)
	}
	var parsedCyclic struct {
		Issues []struct {
			ID      string             `json:"id"`
			Metrics map[string]float64 `json:"metrics"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(jsonOut, &parsedCyclic); err != nil {
		t.Fatalf("Unmarshal cyclic JSON: %v", err)
	}
	for _, iss := range parsedCyclic.Issues {
		if _, hasSlack := iss.Metrics["slack"]; hasSlack {
			t.Fatalf("expected JSON metrics for %s to omit unavailable slack, got %+v", iss.ID, iss.Metrics)
		}
		if _, hasPR := iss.Metrics["pagerank"]; !hasPR {
			t.Fatalf("expected JSON metrics for %s to retain pagerank, got %+v", iss.ID, iss.Metrics)
		}
	}

	// Case 2: Acyclic graph with one open issue (slack=0.00) and one closed issue (slack="N/A", omitted in JSON).
	mixedIssues := []model.Issue{
		{ID: "open-1", Title: "Active Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "closed-1", Title: "Historical Closed", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	mixedStats := analysis.NewAnalyzer(mixedIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())

	csvOut, err := GenerateReport(mixedIssues, mixedIssues, ReportOptions{
		Format:       "csv",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"slack"},
		GraphStats:   &mixedStats,
		View:         recipe.ViewConfig{Columns: []string{"id", "slack"}},
	})
	if err != nil {
		t.Fatalf("GenerateReport(csv, mixed) failed: %v", err)
	}
	csvRows, err := csv.NewReader(bytes.NewReader(csvOut)).ReadAll()
	if err != nil {
		t.Fatalf("parse mixed CSV: %v", err)
	}
	if len(csvRows) != 3 || csvRows[1][1] != "0.00" || csvRows[2][1] != "N/A" {
		t.Fatalf("unexpected mixed CSV rows (want open-1=0.00, closed-1=N/A): %+v", csvRows)
	}

	mixedJSON, err := GenerateReport(mixedIssues, mixedIssues, ReportOptions{
		Format:       "json",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"pagerank", "slack"},
		GraphStats:   &mixedStats,
	})
	if err != nil {
		t.Fatalf("GenerateReport(json, mixed) failed: %v", err)
	}
	var parsedMixed struct {
		Issues []struct {
			ID      string             `json:"id"`
			Metrics map[string]float64 `json:"metrics"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(mixedJSON, &parsedMixed); err != nil {
		t.Fatalf("Unmarshal mixed JSON: %v", err)
	}
	if _, ok := parsedMixed.Issues[0].Metrics["slack"]; !ok {
		t.Fatalf("expected open-1 to include slack=0, got %+v", parsedMixed.Issues[0].Metrics)
	}
	if _, ok := parsedMixed.Issues[1].Metrics["slack"]; ok {
		t.Fatalf("expected closed-1 to omit slack, got %+v", parsedMixed.Issues[1].Metrics)
	}
}

func TestGenerateReport_MermaidPreservesReadinessAfterStrippingTombstones(t *testing.T) {
	now := time.Date(2026, 11, 15, 16, 30, 0, 0, time.UTC)
	selected := []model.Issue{
		{
			ID: "open-ready", Title: "Unblocked Once Tombstone Removed", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "open-ready", DependsOnID: "tomb-blocker", Type: model.DepBlocks},
			},
		},
	}
	context := []model.Issue{
		selected[0],
		{
			ID: "tomb-blocker", Title: "Deleted Prerequisite", Status: model.StatusTombstone, Priority: 2, IssueType: model.TypeTask,
			CreatedAt: now, UpdatedAt: now,
		},
	}
	readiness := model.NewReadinessIndex(context)
	safeID := sanitizeMermaidID("open-ready")

	// Test both explicit opts.Readiness AND nil opts.Readiness across mermaid, markdown, and html formats.
	for _, explicitReadiness := range []*model.ReadinessIndex{readiness, nil} {
		for _, format := range []string{"mermaid", "markdown", "html"} {
			raw, err := GenerateReport(selected, context, ReportOptions{
				Format:       format,
				IncludeGraph: true,
				GeneratedAt:  now,
				Readiness:    explicitReadiness,
			})
			if err != nil {
				t.Fatalf("GenerateReport(%s, explicitReadiness=%v) failed: %v", format, explicitReadiness != nil, err)
			}
			out := string(raw)
			if !strings.Contains(out, fmt.Sprintf("class %s open", safeID)) {
				t.Fatalf("%s report (explicitReadiness=%v) failed to classify open-ready as open:\n%s", format, explicitReadiness != nil, out)
			}
			if strings.Contains(out, fmt.Sprintf("class %s blocked", safeID)) {
				t.Fatalf("%s report (explicitReadiness=%v) falsely classified open-ready as blocked:\n%s", format, explicitReadiness != nil, out)
			}
			if strings.Contains(out, "tomb-blocker") && format == "mermaid" {
				t.Fatalf("mermaid report leaked stripped tombstone node:\n%s", out)
			}
		}
	}
}

func TestGenerateHTMLReport_GroupByModes(t *testing.T) {
	now := time.Date(2026, 11, 15, 17, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID: "h-1", Title: "Critical Auth Bug", Status: model.StatusInProgress, Priority: 0,
			IssueType: model.TypeBug, Assignee: "alice", Labels: []string{"security", "backend"},
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "h-2", Title: "Export Polish", Status: model.StatusOpen, Priority: 2,
			IssueType: model.TypeFeature, Assignee: "bob", Labels: []string{"export"},
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "h-3", Title: "Cleanup Chore", Status: model.StatusClosed, Priority: 3,
			IssueType: model.TypeChore, CreatedAt: now, UpdatedAt: now,
		},
	}

	cases := []struct {
		groupBy   string
		wantOrder []string
	}{
		{
			groupBy:   "priority",
			wantOrder: []string{"<h2>Group: 🔥 Critical (P0) (1)</h2>", "<h2>Group: 🔹 Medium (P2) (1)</h2>", "<h2>Group: ☕ Low (P3) (1)</h2>"},
		},
		{
			groupBy:   "status",
			wantOrder: []string{"<h2>Group: 🟢 open (1)</h2>", "<h2>Group: 🔵 in_progress (1)</h2>", "<h2>Group: ⚫ closed (1)</h2>"},
		},
		{
			groupBy:   "type",
			wantOrder: []string{"<h2>Group: 🐛 bug (1)</h2>", "<h2>Group: ✨ feature (1)</h2>", "<h2>Group: 🧹 chore (1)</h2>"},
		},
		{
			groupBy:   "label",
			wantOrder: []string{"<h2>Group: backend (1)</h2>", "<h2>Group: export (1)</h2>", "<h2>Group: Unlabeled (1)</h2>"},
		},
		{
			groupBy:   "assignee",
			wantOrder: []string{"<h2>Group: @alice (1)</h2>", "<h2>Group: @bob (1)</h2>", "<h2>Group: Unassigned (1)</h2>"},
		},
	}

	for _, tc := range cases {
		raw, err := GenerateReport(issues, nil, ReportOptions{
			Format:       "html",
			IncludeGraph: false,
			GeneratedAt:  now,
			View:         recipe.ViewConfig{GroupBy: tc.groupBy},
		})
		if err != nil {
			t.Fatalf("GenerateReport(html, View.GroupBy=%s) failed: %v", tc.groupBy, err)
		}
		out := string(raw)
		lastIdx := -1
		for _, heading := range tc.wantOrder {
			idx := strings.Index(out, heading)
			if idx < 0 {
				t.Fatalf("HTML report (groupBy=%s) missing group heading %q:\n%s", tc.groupBy, heading, out)
			}
			if idx <= lastIdx {
				t.Fatalf("HTML report (groupBy=%s) heading %q out of order:\n%s", tc.groupBy, heading, out)
			}
			lastIdx = idx
		}
	}
}

func TestGenerateHTMLReport_CompleteMetadataAndSanitizedPriorityBriefAlerts(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	due := now.Add(72 * time.Hour)
	deferUntil := now.Add(48 * time.Hour)
	closedAt := now.Add(24 * time.Hour)
	extRef := "EXT-999<tag>"
	issues := []model.Issue{
		{
			ID:                 "html-full-1",
			Title:              "HTML Complete Issue",
			Description:        "Detailed body",
			Status:             model.StatusClosed,
			Priority:           1,
			IssueType:          model.TypeFeature,
			CreatedAt:          now,
			UpdatedAt:          now.Add(12 * time.Hour),
			DueDate:            &due,
			DeferUntil:         &deferUntil,
			ClosedAt:           &closedAt,
			ExternalRef:        &extRef,
			AcceptanceCriteria: "Must pass <script>alert('ac')</script>",
			Design:             "Architecture <img src=x onerror=alert(1)>",
			Notes:              "Operational notes & details",
			Dependencies: []*model.Dependency{
				{IssueID: "html-full-1", DependsOnID: "dep-target<1>", Type: model.DepBlocks},
			},
			Comments: []*model.Comment{
				{ID: "1", Author: "alice<b>", Text: "Comment <script>x</script>", CreatedAt: now.Add(2 * time.Hour)},
			},
		},
	}

	rawHTML, err := GenerateReport(issues, issues, ReportOptions{
		Format:       "html",
		IncludeGraph: false,
		GeneratedAt:  now,
	})
	if err != nil {
		t.Fatalf("GenerateReport(html) failed: %v", err)
	}
	htmlStr := string(rawHTML)
	for _, want := range []string{
		"<tr><th>Created</th><td>2026-11-15 18:00</td></tr>",
		"<tr><th>Updated</th><td>2026-11-16 06:00</td></tr>",
		"<tr><th>Due</th><td>2026-11-18</td></tr>",
		"<tr><th>Deferred Until</th><td>2026-11-17</td></tr>",
		"<tr><th>Closed</th><td>2026-11-16 18:00</td></tr>",
		"<tr><th>External Ref</th><td><code>EXT-999&lt;tag&gt;</code></td></tr>",
		"<h3>Acceptance Criteria</h3>",
		"&lt;script&gt;alert(&#39;ac&#39;)&lt;/script&gt;",
		"<h3>Design</h3>",
		"&lt;img src=x onerror=alert(1)&gt;",
		"<h3>Notes</h3>",
		"Operational notes &amp; details",
		"<h3>Dependencies</h3>",
		"<code>dep-target&lt;1&gt;</code>",
		"<h3>Comments</h3>",
		"<strong>alice&lt;b&gt;</strong>",
		"Comment &lt;script&gt;x&lt;/script&gt;",
	} {
		if !strings.Contains(htmlStr, want) {
			t.Fatalf("HTML report missing %q:\n%s", want, htmlStr)
		}
	}
	if strings.Contains(htmlStr, "<script>alert('ac')</script>") || strings.Contains(htmlStr, "<script>x</script>") {
		t.Fatalf("HTML report failed to escape raw <script> tags:\n%s", htmlStr)
	}

	// Verify sanitizeInlineMarkdown on alert.Type and alert.Message in GeneratePriorityBriefFromTriageJSON.
	alertJSON := `{
		"meta": {"version": "1.0", "generated_at": "2026-11-15T18:00:00Z", "phase2_ready": true, "issue_count": 1},
		"quick_ref": {"open_count": 1, "actionable_count": 1, "blocked_count": 0, "in_progress_count": 0},
		"alerts": [{
			"type": "stale|type\ninjected",
			"severity": "warning",
			"message": "Alert line 1\r\n## Injected Heading | Pipe"
		}]
	}`
	brief, err := GeneratePriorityBriefFromTriageJSON([]byte(alertJSON), DefaultPriorityBriefConfig())
	if err != nil {
		t.Fatalf("GeneratePriorityBriefFromTriageJSON failed: %v", err)
	}
	if strings.Contains(brief, "\n## Injected Heading") {
		t.Fatalf("priority brief allowed newline heading injection in alert message:\n%s", brief)
	}
	for _, want := range []string{
		`**stale\|type injected**`,
		`\#\# Injected Heading \| Pipe`,
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("priority brief missing sanitized alert %q:\n%s", want, brief)
		}
	}
}

func TestInteractiveGraph_VendorEmbedDeterminismDeferredAndLightMode(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	exportNow := time.Unix(1700000000, 0).UTC()
	futureDefer := exportNow.Add(6 * time.Hour) // Same day or future sub-day timestamp

	issues := []model.Issue{
		{
			ID:        "ig-ready",
			Title:     "Actionable Open Node",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: exportNow,
			UpdatedAt: exportNow,
		},
		{
			ID:         "ig-deferred",
			Title:      "Future DeferUntil Open Node",
			Status:     model.StatusOpen,
			Priority:   1,
			IssueType:  model.TypeTask,
			DeferUntil: &futureDefer,
			CreatedAt:  exportNow,
			UpdatedAt:  exportNow,
		},
	}

	dir := t.TempDir()
	path1, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Title:  "Deterministic Interactive Graph",
		Path:   filepath.Join(dir, "g1.html"),
	})
	if err != nil {
		t.Fatalf("first GenerateInteractiveGraphHTML: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	path2, err := GenerateInteractiveGraphHTML(InteractiveGraphOptions{
		Issues: issues,
		Title:  "Deterministic Interactive Graph",
		Path:   filepath.Join(dir, "g2.html"),
	})
	if err != nil {
		t.Fatalf("second GenerateInteractiveGraphHTML: %v", err)
	}

	b1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("ReadFile(g1.html): %v", err)
	}
	b2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("ReadFile(g2.html): %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatal("InteractiveGraphHTML output differed across wall-clock time despite SOURCE_DATE_EPOCH")
	}

	htmlOut := string(b1)
	for _, want := range []string{
		`"id":"ig-deferred"`,
		`"is_deferred":true`,
		`"exported_at_ms":1700000000000`,
		`function isNodeDeferred(n)`,
		`function computeLinkColor(l)`,
		`function computeLinkArrowColor(l)`,
		`window.innerWidth - menuW - pad`,
	} {
		if !strings.Contains(htmlOut, want) {
			t.Fatalf("InteractiveGraphHTML missing %q", want)
		}
	}

	// Verify vendor scripts match ViewerAssetsFS.
	fgVendor, err := ViewerAssetsFS.ReadFile("viewer_assets/vendor/force-graph.min.js")
	if err != nil || len(fgVendor) == 0 {
		t.Fatalf("ViewerAssetsFS force-graph.min.js read failed: %v", err)
	}
	if !bytes.Contains(b1, fgVendor[:128]) {
		t.Fatal("InteractiveGraphHTML did not embed force-graph.min.js from ViewerAssetsFS")
	}
}

func TestResolveReportOptionsAndGenerateReport_IncludeActionsPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	boolPtr := func(v bool) *bool { return &v }
	strPtr := func(s string) *string { return &s }

	workDir := t.TempDir()
	issues := []model.Issue{
		{
			ID:        "act-1",
			Title:     "Actionable Task",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Origin: &model.IssueOrigin{
				Tracker:          "br",
				Executable:       "/usr/local/bin/br",
				Database:         filepath.Join(workDir, ".beads", "beads.db"),
				TrackerDirectory: filepath.Join(workDir, ".beads"),
				LocalID:          "act-1",
				WorkingDirectory: workDir,
				SupportsClaim:    true,
			},
		},
	}
	readiness := model.NewReadinessIndex(issues)

	// 1. Recipe defaults.IncludeActions=false suppresses actions when CLI override is nil.
	optsRecipeFalse, err := ResolveReportOptions(
		recipe.ExportConfig{IncludeActions: boolPtr(false)},
		ReportOverrides{},
	)
	if err != nil {
		t.Fatalf("ResolveReportOptions(recipe false): %v", err)
	}
	if optsRecipeFalse.IncludeActionPlan || optsRecipeFalse.IncludeActions == nil || *optsRecipeFalse.IncludeActions {
		t.Fatalf("expected recipe IncludeActions=false to disable actions, got %+v", optsRecipeFalse)
	}
	optsRecipeFalse.GeneratedAt = now
	optsRecipeFalse.Readiness = readiness
	optsRecipeFalse.AuthorityComplete = true
	mdDisabled, err := GenerateReport(issues, issues, optsRecipeFalse)
	if err != nil {
		t.Fatalf("GenerateReport(recipe false, md): %v", err)
	}
	if strings.Contains(string(mdDisabled), "Quick Actions") || strings.Contains(string(mdDisabled), "--status=in_progress") {
		t.Fatalf("expected Markdown report with recipe IncludeActions=false to omit Quick Actions, got:\n%s", string(mdDisabled))
	}

	optsRecipeFalseJSON, err := ResolveReportOptions(
		recipe.ExportConfig{Format: "json", IncludeActions: boolPtr(false)},
		ReportOverrides{},
	)
	if err != nil {
		t.Fatalf("ResolveReportOptions(recipe false, json): %v", err)
	}
	optsRecipeFalseJSON.GeneratedAt = now
	optsRecipeFalseJSON.Readiness = readiness
	optsRecipeFalseJSON.AuthorityComplete = true
	rawDisabledJSON, err := GenerateReport(issues, issues, optsRecipeFalseJSON)
	if err != nil {
		t.Fatalf("GenerateReport(recipe false, json): %v", err)
	}
	var parsedDisabled struct {
		Issues []struct {
			Actions model.IssueActions `json:"actions"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(rawDisabledJSON, &parsedDisabled); err != nil {
		t.Fatalf("Unmarshal disabled JSON: %v", err)
	}
	if len(parsedDisabled.Issues) != 1 || parsedDisabled.Issues[0].Actions.Claim != nil || parsedDisabled.Issues[0].Actions.Show != nil {
		t.Fatalf("expected JSON actions omitted when recipe IncludeActions=false, got %+v", parsedDisabled.Issues)
	}

	// 2. Explicit CLI IncludeActions=true overrides recipe IncludeActions=false (positive control).
	optsOverrideTrue, err := ResolveReportOptions(
		recipe.ExportConfig{IncludeActions: boolPtr(false)},
		ReportOverrides{Format: strPtr("json"), IncludeActions: boolPtr(true)},
	)
	if err != nil {
		t.Fatalf("ResolveReportOptions(override true): %v", err)
	}
	optsOverrideTrue.GeneratedAt = now
	optsOverrideTrue.Readiness = readiness
	optsOverrideTrue.AuthorityComplete = true
	rawEnabledJSON, err := GenerateReport(issues, issues, optsOverrideTrue)
	if err != nil {
		t.Fatalf("GenerateReport(override true, json): %v", err)
	}
	var parsedEnabled struct {
		Issues []struct {
			Actions model.IssueActions `json:"actions"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(rawEnabledJSON, &parsedEnabled); err != nil {
		t.Fatalf("Unmarshal enabled JSON: %v", err)
	}
	if len(parsedEnabled.Issues) != 1 || parsedEnabled.Issues[0].Actions.Claim == nil || parsedEnabled.Issues[0].Actions.Show == nil {
		t.Fatalf("expected JSON actions populated when CLI overrides IncludeActions=true, got %+v", parsedEnabled.Issues)
	}

	optsOverrideTrueMD, err := ResolveReportOptions(
		recipe.ExportConfig{IncludeActions: boolPtr(false)},
		ReportOverrides{IncludeActions: boolPtr(true)},
	)
	if err != nil {
		t.Fatalf("ResolveReportOptions(override true, md): %v", err)
	}
	optsOverrideTrueMD.GeneratedAt = now
	optsOverrideTrueMD.Readiness = readiness
	optsOverrideTrueMD.AuthorityComplete = true
	mdEnabled, err := GenerateReport(issues, issues, optsOverrideTrueMD)
	if err != nil {
		t.Fatalf("GenerateReport(override true, md): %v", err)
	}
	if !strings.Contains(string(mdEnabled), "Quick Actions") || !strings.Contains(string(mdEnabled), "--claim") {
		t.Fatalf("expected Markdown report with override IncludeActions=true to include Quick Actions and --claim, got:\n%s", string(mdEnabled))
	}

	// 3. Explicit CLI IncludeActions=false overrides recipe IncludeActions=true (negative control).
	optsOverrideFalse, err := ResolveReportOptions(
		recipe.ExportConfig{IncludeActions: boolPtr(true)},
		ReportOverrides{IncludeActions: boolPtr(false)},
	)
	if err != nil {
		t.Fatalf("ResolveReportOptions(override false): %v", err)
	}
	if optsOverrideFalse.IncludeActionPlan || optsOverrideFalse.IncludeActions == nil || *optsOverrideFalse.IncludeActions {
		t.Fatalf("expected explicit IncludeActions=false to override recipe true, got %+v", optsOverrideFalse)
	}
}

func TestGenerateMarkdownAndReport_ActiveSubgraphBetweennessAndKCore(t *testing.T) {
	now := time.Date(2026, 9, 4, 14, 0, 0, 0, time.UTC)
	closedAt := now

	// Active 3-clique (open-c1, open-c2, open-c3 -> k-core 2) + active bridge (open-leaf -> open-bridge -> open-c1 -> betweenness > 0),
	// plus a closed 3-clique/bridge (closed-hub bridging closed-a, closed-b, and open-c1) that has positive full-graph betweenness & k-core >= 2!
	issues := []model.Issue{
		{ID: "open-c1", Title: "Active Core 1", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{
			ID: "open-c2", Title: "Active Core 2", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "open-c2", DependsOnID: "open-c1", Type: model.DepBlocks}},
		},
		{
			ID: "open-c3", Title: "Active Core 3", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "open-c3", DependsOnID: "open-c1", Type: model.DepBlocks},
				{IssueID: "open-c3", DependsOnID: "open-c2", Type: model.DepBlocks},
			},
		},
		{
			ID: "open-bridge", Title: "Active Bridge", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "open-bridge", DependsOnID: "open-c3", Type: model.DepBlocks}},
		},
		{
			ID: "open-leaf", Title: "Active Leaf", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "open-leaf", DependsOnID: "open-bridge", Type: model.DepBlocks}},
		},
		{
			ID: "closed-hub", Title: "Closed Historical Hub", Status: model.StatusClosed, ClosedAt: &closedAt, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "closed-hub", DependsOnID: "open-c1", Type: model.DepBlocks}},
		},
		{
			ID: "closed-a", Title: "Closed Peer A", Status: model.StatusClosed, ClosedAt: &closedAt, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "closed-a", DependsOnID: "closed-hub", Type: model.DepBlocks},
				{IssueID: "closed-a", DependsOnID: "open-c1", Type: model.DepBlocks},
			},
		},
		{
			ID: "open-after-closed", Title: "Open After Closed Hub", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "open-after-closed", DependsOnID: "closed-hub", Type: model.DepBlocks}},
		},
	}

	stats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	if stats.GetBetweennessScore("closed-hub") <= 0 || stats.CoreNumber()["closed-hub"] < 2 {
		t.Fatalf("negative control setup invalid: full-graph closed-hub betweenness=%v kcore=%d, want >0 and >=2",
			stats.GetBetweennessScore("closed-hub"), stats.CoreNumber()["closed-hub"])
	}

	opts := ReportOptions{
		Format:       "markdown",
		IncludeGraph: false,
		GeneratedAt:  now,
		Metrics:      []string{"betweenness", "kcore"},
		GraphStats:   &stats,
	}
	md, err := GenerateMarkdown(issues, "Active Metrics Report", opts)
	if err != nil {
		t.Fatalf("GenerateMarkdown failed: %v", err)
	}
	if !strings.Contains(md, "| closed-hub | Closed Historical Hub | 📋 task | P1 | ⚫ closed | 0.0000 | 0 |") {
		t.Fatalf("expected closed-hub summary row to render 0.0000 betweenness and 0 kcore, got:\n%s", md)
	}
	if !strings.Contains(md, "| open-bridge | Active Bridge | 📋 task | P1 | 🟢 open | 3.0000 | 1 |") {
		t.Fatalf("expected open-bridge summary row to render active betweenness 3.0000, got:\n%s", md)
	}
	if !strings.Contains(md, "| open-c3 | Active Core 3 | 📋 task | P1 | 🟢 open | 4.0000 | 2 |") {
		t.Fatalf("expected open-c3 summary row to render active betweenness 4.0000 and kcore 2, got:\n%s", md)
	}

	// Verify exact numeric values via JSON and CSV reports.
	opts.Format = "json"
	rawJSON, err := GenerateReport(issues, issues, opts)
	if err != nil {
		t.Fatalf("GenerateReport(json) failed: %v", err)
	}
	var parsed struct {
		Issues []struct {
			ID      string             `json:"id"`
			Metrics map[string]float64 `json:"metrics"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		t.Fatalf("Unmarshal JSON report: %v", err)
	}
	byID := make(map[string]map[string]float64, len(parsed.Issues))
	for _, row := range parsed.Issues {
		byID[row.ID] = row.Metrics
	}
	if byID["closed-hub"]["betweenness"] != 0 || byID["closed-hub"]["kcore"] != 0 {
		t.Fatalf("closed-hub JSON metrics = %+v, want betweenness=0 and kcore=0", byID["closed-hub"])
	}
	if byID["open-bridge"]["betweenness"] <= 0 {
		t.Fatalf("open-bridge JSON metrics = %+v, want active betweenness > 0", byID["open-bridge"])
	}
	if byID["open-c1"]["kcore"] != 2 || byID["open-c2"]["kcore"] != 2 || byID["open-c3"]["kcore"] != 2 {
		t.Fatalf("active 3-clique kcore = (%v, %v, %v), want (2, 2, 2)",
			byID["open-c1"]["kcore"], byID["open-c2"]["kcore"], byID["open-c3"]["kcore"])
	}
}
