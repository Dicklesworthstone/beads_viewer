package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// ReportOverrides contains only explicitly supplied CLI settings. A pointer to
// false or an empty template deliberately overrides the recipe value.
type ReportOverrides struct {
	Format       *string
	IncludeGraph *bool
	Template     *string
}

type ReportOptions struct {
	Format            string
	IncludeGraph      bool
	Template          string
	Title             string
	GeneratedAt       time.Time
	Readiness         *model.ReadinessIndex
	AuthorityComplete bool
	GraphIssues       []model.Issue
	SourceAuthority   any
	AuthorityHash     string
	DataHash          string
	SourcePath        string
	SourceKind        string
	AsOf              string
	AsOfCommit        string
}

func ResolveReportOptions(defaults recipe.ExportConfig, explicit ReportOverrides) (ReportOptions, error) {
	opts := ReportOptions{Format: "markdown", Title: "Beads Export"}
	if defaults.Format != "" {
		opts.Format = defaults.Format
	}
	if explicit.Format != nil {
		opts.Format = *explicit.Format
	}
	opts.IncludeGraph = opts.Format != "csv"
	if defaults.IncludeGraph != nil {
		opts.IncludeGraph = *defaults.IncludeGraph
	}
	if explicit.IncludeGraph != nil {
		opts.IncludeGraph = *explicit.IncludeGraph
	}
	opts.Template = defaults.Template
	if explicit.Template != nil {
		opts.Template = *explicit.Template
	}
	if err := opts.validate(); err != nil {
		return ReportOptions{}, err
	}
	return opts, nil
}

func (opts ReportOptions) validate() error {
	switch opts.Format {
	case "markdown", "json", "csv", "mermaid":
	default:
		return fmt.Errorf("export format %q must be markdown, json, csv or mermaid", opts.Format)
	}
	if opts.Format == "csv" && opts.IncludeGraph {
		return fmt.Errorf("CSV cannot include a graph; set --export-include-graph=false")
	}
	if opts.Format == "mermaid" && !opts.IncludeGraph {
		return fmt.Errorf("Mermaid export requires include_graph=true")
	}
	if opts.Template != "" && opts.Format != "markdown" {
		return fmt.Errorf("custom templates require markdown export")
	}
	return nil
}

// GenerateReport renders selected bodies in their supplied recipe order. The
// context is consulted only for dependency graph closure and readiness; its
// other issue bodies never become selected output rows.
func GenerateReport(selected, context []model.Issue, opts ReportOptions) ([]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.Title == "" {
		opts.Title = "Beads Export"
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now().UTC()
	}
	if opts.IncludeGraph {
		opts.GraphIssues = reportGraphContext(selected, context)
	}
	switch opts.Format {
	case "markdown":
		if opts.Template != "" {
			return renderReportTemplate(selected, opts)
		}
		content, err := GenerateMarkdown(selected, opts.Title, opts)
		return []byte(content), err
	case "json":
		type row struct {
			model.Issue
			Actions model.IssueActions `json:"actions"`
		}
		rows := make([]row, 0, len(selected))
		for _, issue := range selected {
			claimable := opts.AuthorityComplete && opts.Readiness != nil && opts.Readiness.Claimable(issue.ID, opts.GeneratedAt)
			rows = append(rows, row{issue, issue.Actions(claimable)})
		}
		var graph *GraphExportResult
		if opts.IncludeGraph {
			var err error
			graph, err = ExportGraph(opts.GraphIssues, nil, GraphExportConfig{Format: GraphFormatJSON, DataHash: analysis.ComputeDataHash(opts.GraphIssues)})
			if err != nil {
				return nil, err
			}
		}
		return json.MarshalIndent(struct {
			Title           string             `json:"title"`
			GeneratedAt     time.Time          `json:"generated_at"`
			SourceAuthority any                `json:"source_authority"`
			AuthorityHash   string             `json:"authority_hash,omitempty"`
			DataHash        string             `json:"data_hash,omitempty"`
			SourcePath      string             `json:"source_path,omitempty"`
			SourceKind      string             `json:"source_kind,omitempty"`
			AsOf            string             `json:"as_of,omitempty"`
			AsOfCommit      string             `json:"as_of_commit,omitempty"`
			Issues          []row              `json:"issues"`
			Graph           *GraphExportResult `json:"graph,omitempty"`
		}{opts.Title, opts.GeneratedAt, opts.SourceAuthority, opts.AuthorityHash, opts.DataHash, opts.SourcePath, opts.SourceKind, opts.AsOf, opts.AsOfCommit, rows, graph}, "", "  ")
	case "csv":
		var content bytes.Buffer
		writer := csv.NewWriter(&content)
		if err := writer.Write([]string{"id", "title", "status", "priority", "issue_type", "description", "labels"}); err != nil {
			return nil, err
		}
		for _, issue := range selected {
			if err := writer.Write([]string{issue.ID, issue.Title, string(issue.Status), fmt.Sprint(issue.Priority), string(issue.IssueType), issue.Description, strings.Join(issue.Labels, ";")}); err != nil {
				return nil, err
			}
		}
		writer.Flush()
		return content.Bytes(), writer.Error()
	case "mermaid":
		return []byte(reportMermaid(opts.GraphIssues)), nil
	}
	return nil, fmt.Errorf("unhandled export format %q", opts.Format)
}

func reportGraphContext(selected, context []model.Issue) []model.Issue {
	byID := make(map[string]model.Issue, len(context)+len(selected))
	for _, issue := range context {
		byID[issue.ID] = issue
	}
	queue := make([]string, 0, len(selected))
	for _, issue := range selected {
		byID[issue.ID] = issue
		queue = append(queue, issue.ID)
	}
	seen := make(map[string]bool)
	result := make([]model.Issue, 0, len(selected))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		issue, ok := byID[id]
		if !ok || issue.Status == model.StatusTombstone {
			continue
		}
		result = append(result, issue)
		for _, dep := range issue.Dependencies {
			if dep != nil {
				queue = append(queue, dep.DependsOnID)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func reportMermaid(issues []model.Issue) string {
	ids := make(map[string]bool, len(issues))
	for _, issue := range issues {
		ids[issue.ID] = true
	}
	return GenerateMermaidGraph(issues, ids, MermaidConfig{ShowNoDependenciesNode: true})
}

// Template data has no methods or function-valued fields. No filesystem,
// environment, process or custom function is exposed to template execution.
func renderReportTemplate(issues []model.Issue, opts ReportOptions) ([]byte, error) {
	file, err := os.Open(opts.Template)
	if err != nil {
		return nil, fmt.Errorf("read export template: %w", err)
	}
	defer file.Close()
	const maxTemplate = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, maxTemplate+1))
	if err != nil {
		return nil, fmt.Errorf("read export template: %w", err)
	}
	if len(raw) > maxTemplate {
		return nil, fmt.Errorf("export template exceeds %d bytes", maxTemplate)
	}
	tmpl, err := template.New("report").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse export template: %w", err)
	}
	type issueData struct {
		ID, Title, Status, IssueType, Description string
		Priority                                  int
		Labels                                    []string
	}
	data := struct {
		Title, GeneratedAt, Graph string
		Issues                    []issueData
	}{Title: escapeReportText(opts.Title), GeneratedAt: opts.GeneratedAt.Format(time.RFC3339), Issues: make([]issueData, 0, len(issues))}
	if opts.IncludeGraph {
		data.Graph = reportMermaid(opts.GraphIssues)
	}
	for _, issue := range issues {
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, escapeReportText(label))
		}
		data.Issues = append(data.Issues, issueData{escapeReportText(issue.ID), escapeReportText(issue.Title), escapeReportText(string(issue.Status)), escapeReportText(string(issue.IssueType)), escapeReportText(issue.Description), issue.Priority, labels})
	}
	writer := &reportTemplateWriter{remaining: 16 << 20}
	if err := tmpl.Execute(writer, data); err != nil {
		return nil, fmt.Errorf("render export template: %w", err)
	}
	return writer.Bytes(), nil
}

type reportTemplateWriter struct {
	bytes.Buffer
	remaining int
}

func (w *reportTemplateWriter) Write(raw []byte) (int, error) {
	if len(raw) > w.remaining {
		return 0, fmt.Errorf("rendered export template exceeds 16 MiB")
	}
	w.remaining -= len(raw)
	return w.Buffer.Write(raw)
}

func escapeReportText(value string) string {
	// Escape Markdown before HTML so numeric entities introduced for quotes
	// are not broken by escaping their '#'. HTML-sensitive punctuation is
	// handled by EscapeString; the remaining punctuation stays literal even
	// in list, heading, fence, link and GFM strikethrough positions.
	const markdownPunctuation = "\\`*_[]|#!$%()+,-./:;=?@^{}~"
	var markdown strings.Builder
	markdown.Grow(len(value))
	for _, r := range value {
		if strings.ContainsRune(markdownPunctuation, r) {
			markdown.WriteByte('\\')
		}
		markdown.WriteRune(r)
	}
	escaped := html.EscapeString(markdown.String())
	var literal strings.Builder
	literal.Grow(len(escaped))
	lineStart := true
	for _, r := range escaped {
		if lineStart && (r == ' ' || r == '\t') {
			// Character references preserve the text while preventing field
			// indentation from starting a Markdown code block.
			if r == ' ' {
				literal.WriteString("&#32;")
			} else {
				literal.WriteString("&#9;")
			}
			continue
		}
		literal.WriteRune(r)
		lineStart = r == '\n' || r == '\r'
	}
	return literal.String()
}

// Package-level compiled regex for slug creation (avoids recompilation per call)
var slugNonAlphanumericRegex = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeMermaidID ensures an ID is valid for Mermaid diagrams.
// Mermaid node IDs must be alphanumeric with hyphens/underscores.
func sanitizeMermaidID(id string) string {
	var sb strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			sb.WriteRune(r)
		}
	}
	result := sb.String()
	if result == "" {
		return "node"
	}
	return result
}

// sanitizeMermaidText prepares text for use in Mermaid node labels.
// Removes/escapes characters that break Mermaid syntax.
func sanitizeMermaidText(text string) string {
	// Remove or replace problematic characters
	replacer := strings.NewReplacer(
		"\"", "'",
		"[", "(",
		"]", ")",
		"{", "(",
		"}", ")",
		"<", "&lt;",
		">", "&gt;",
		"|", "/",
		"`", "'",
		"\n", " ",
		"\r", "",
	)
	result := replacer.Replace(text)

	// Remove any remaining control characters
	result = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, result)

	result = strings.TrimSpace(result)

	// Truncate if too long (UTF-8 safe using runes)
	runes := []rune(result)
	if len(runes) > 40 {
		result = string(runes[:37]) + "..."
	}

	return result
}

// GenerateMarkdown creates a comprehensive markdown report of all issues
func GenerateMarkdown(issues []model.Issue, title string, options ...ReportOptions) (string, error) {
	opts := ReportOptions{IncludeGraph: true, GeneratedAt: time.Now(), GraphIssues: issues}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now()
	}
	if opts.IncludeGraph && opts.GraphIssues == nil {
		opts.GraphIssues = issues
	}
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", opts.GeneratedAt.Format(time.RFC1123)))

	// Summary Statistics
	sb.WriteString("## Summary\n\n")

	open, inProgress, blocked, closed := 0, 0, 0, 0
	for _, i := range issues {
		if isClosedLikeStatus(i.Status) {
			closed++
			continue
		}
		switch i.Status {
		case model.StatusInProgress:
			inProgress++
		case model.StatusBlocked:
			blocked++
		default:
			open++
		}
	}

	sb.WriteString("| Metric | Count |\n|--------|-------|\n")
	sb.WriteString(fmt.Sprintf("| **Total** | %d |\n", len(issues)))
	sb.WriteString(fmt.Sprintf("| Open | %d |\n", open))
	sb.WriteString(fmt.Sprintf("| In Progress | %d |\n", inProgress))
	sb.WriteString(fmt.Sprintf("| Blocked | %d |\n", blocked))
	sb.WriteString(fmt.Sprintf("| Closed | %d |\n\n", closed))

	// Quick Actions Section
	sb.WriteString(generateQuickActions(issues, opts))

	// Precompute stable, unique slugs for TOC anchors and headings.
	slugCounts := make(map[string]int, len(issues))
	issueSlugs := make([]string, len(issues))
	for idx, i := range issues {
		base := createSlug(issueHeadingText(i))
		issueSlugs[idx] = uniqueSlug(base, slugCounts)
	}

	// Table of Contents
	sb.WriteString("## Table of Contents\n\n")
	for idx, i := range issues {
		slug := issueSlugs[idx]
		statusIcon := getStatusEmoji(string(i.Status))
		sb.WriteString(fmt.Sprintf("- [%s %s %s](#%s)\n", statusIcon, i.ID, i.Title, slug))
	}
	sb.WriteString("\n---\n\n")

	// Dependency Graph (Mermaid)
	if opts.IncludeGraph {
		sb.WriteString("## Dependency Graph\n\n")
		sb.WriteString("```mermaid\n")

		graph := reportMermaid(opts.GraphIssues)
		sb.WriteString(graph)

		sb.WriteString("```\n\n")
		sb.WriteString("---\n\n")
	}

	// Individual Issues
	for idx, i := range issues {
		typeIcon := getTypeEmoji(string(i.IssueType))
		slug := issueSlugs[idx]
		sb.WriteString(fmt.Sprintf("<a id=\"%s\"></a>\n\n", slug))
		sb.WriteString(fmt.Sprintf("## %s\n\n", issueHeadingText(i)))

		// Metadata Table
		sb.WriteString("| Property | Value |\n|----------|-------|\n")
		sb.WriteString(fmt.Sprintf("| **Type** | %s %s |\n", typeIcon, i.IssueType))
		sb.WriteString(fmt.Sprintf("| **Priority** | %s |\n", getPriorityLabel(i.Priority)))
		sb.WriteString(fmt.Sprintf("| **Status** | %s %s |\n", getStatusEmoji(string(i.Status)), i.Status))
		if i.Assignee != "" {
			// Sanitize assignee: replace newlines with spaces, escape pipes
			cleanAssignee := strings.ReplaceAll(i.Assignee, "\n", " ")
			cleanAssignee = strings.ReplaceAll(cleanAssignee, "\r", "")
			escapedAssignee := strings.ReplaceAll(cleanAssignee, "|", "\\|")
			sb.WriteString(fmt.Sprintf("| **Assignee** | @%s |\n", escapedAssignee))
		}
		sb.WriteString(fmt.Sprintf("| **Created** | %s |\n", i.CreatedAt.Format("2006-01-02 15:04")))
		sb.WriteString(fmt.Sprintf("| **Updated** | %s |\n", i.UpdatedAt.Format("2006-01-02 15:04")))
		if i.ClosedAt != nil {
			sb.WriteString(fmt.Sprintf("| **Closed** | %s |\n", i.ClosedAt.Format("2006-01-02 15:04")))
		}
		if len(i.Labels) > 0 {
			// Escape pipe characters and sanitize newlines in labels
			escapedLabels := make([]string, len(i.Labels))
			for idx, label := range i.Labels {
				cleanLabel := strings.ReplaceAll(label, "\n", " ")
				cleanLabel = strings.ReplaceAll(cleanLabel, "\r", "")
				escapedLabels[idx] = strings.ReplaceAll(cleanLabel, "|", "\\|")
			}
			sb.WriteString(fmt.Sprintf("| **Labels** | %s |\n", strings.Join(escapedLabels, ", ")))
		}
		sb.WriteString("\n")

		if i.Description != "" {
			sb.WriteString("### Description\n\n")
			sb.WriteString(i.Description + "\n\n")
		}

		if i.AcceptanceCriteria != "" {
			sb.WriteString("### Acceptance Criteria\n\n")
			sb.WriteString(i.AcceptanceCriteria + "\n\n")
		}

		if i.Design != "" {
			sb.WriteString("### Design\n\n")
			sb.WriteString(i.Design + "\n\n")
		}

		if i.Notes != "" {
			sb.WriteString("### Notes\n\n")
			sb.WriteString(i.Notes + "\n\n")
		}

		if len(i.Dependencies) > 0 {
			sb.WriteString("### Dependencies\n\n")
			for _, dep := range i.Dependencies {
				if dep == nil {
					continue
				}
				icon := "🔗"
				if dep.Type.IsBlocking() {
					icon = "⛔"
				}
				sb.WriteString(fmt.Sprintf("- %s **%s**: `%s`\n", icon, dep.Type, dep.DependsOnID))
			}
			sb.WriteString("\n")
		}

		if len(i.Comments) > 0 {
			sb.WriteString("### Comments\n\n")
			for _, c := range i.Comments {
				if c == nil {
					continue
				}
				escapedText := strings.ReplaceAll(c.Text, "\n", "\n> ")
				sb.WriteString(fmt.Sprintf("> **%s** (%s)\n>\n> %s\n\n",
					c.Author, c.CreatedAt.Format("2006-01-02"), escapedText))
			}
		}

		// Per-issue command snippets
		sb.WriteString(generateIssueCommands(i, opts))

		sb.WriteString("---\n\n")
	}

	return sb.String(), nil
}

func issueHeadingText(i model.Issue) string {
	typeIcon := getTypeEmoji(string(i.IssueType))
	return fmt.Sprintf("%s %s %s", typeIcon, i.ID, i.Title)
}

func uniqueSlug(base string, counts map[string]int) string {
	if base == "" {
		base = "section"
	}
	if count, ok := counts[base]; ok {
		count++
		counts[base] = count
		return fmt.Sprintf("%s-%d", base, count)
	}
	counts[base] = 0
	return base
}

// createSlug creates a URL-friendly slug from heading text.
func createSlug(text string) string {
	// Convert to lowercase and replace non-alphanumeric with hyphens
	slug := strings.ToLower(text)
	slug = slugNonAlphanumericRegex.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	return slug
}

func getStatusEmoji(status string) string {
	switch status {
	case "open":
		return "🟢"
	case "in_progress":
		return "🔵"
	case "blocked":
		return "🔴"
	case "closed", "tombstone":
		return "⚫"
	default:
		return "⚪"
	}
}

func isClosedLikeStatus(status model.Status) bool {
	return status == model.StatusClosed || status == model.StatusTombstone
}

func getTypeEmoji(issueType string) string {
	switch issueType {
	case "bug":
		return "🐛"
	case "feature":
		return "✨"
	case "task":
		return "📋"
	case "epic":
		return "🚀" // Use rocket instead of mountain - VS-16 variation selector causes width issues
	case "chore":
		return "🧹"
	default:
		return "•"
	}
}

func getPriorityLabel(priority int) string {
	switch priority {
	case 0:
		return "🔥 Critical (P0)"
	case 1:
		return "⚡ High (P1)"
	case 2:
		return "🔹 Medium (P2)"
	case 3:
		return "☕ Low (P3)"
	case 4:
		return "💤 Backlog (P4)"
	default:
		return fmt.Sprintf("P%d", priority)
	}
}

// SaveMarkdownToFile writes the generated markdown to a file
func SaveMarkdownToFile(issues []model.Issue, filename string) error {
	// Make a copy to avoid mutating the caller's slice
	issuesCopy := make([]model.Issue, len(issues))
	copy(issuesCopy, issues)

	// Sort issues for the report: Open first, then priority, then date
	sort.Slice(issuesCopy, func(i, j int) bool {
		iClosed := isClosedLikeStatus(issuesCopy[i].Status)
		jClosed := isClosedLikeStatus(issuesCopy[j].Status)
		if iClosed != jClosed {
			return !iClosed
		}
		if issuesCopy[i].Priority != issuesCopy[j].Priority {
			return issuesCopy[i].Priority < issuesCopy[j].Priority
		}
		return issuesCopy[i].CreatedAt.After(issuesCopy[j].CreatedAt)
	})

	content, err := GenerateMarkdown(issuesCopy, "Beads Export")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, []byte(content), 0644)
}

// generateQuickActions offers inspection commands bound to each live tracker.
// A report does not establish that every open issue is ready to be closed.
func generateQuickActions(issues []model.Issue, options ReportOptions) string {
	var sb strings.Builder
	for _, issue := range issues {
		if isClosedLikeStatus(issue.Status) {
			continue
		}
		actions := issue.Actions(false)
		if actions.Show != nil {
			if sb.Len() == 0 {
				sb.WriteString("## Quick Actions\n\nInspect the current tracker before acting on this snapshot:\n\n```bash\n")
			}
			sb.WriteString(actions.Show.Shell + "\n")
		}
	}
	if sb.Len() > 0 {
		sb.WriteString("```\n\n")
	}
	return sb.String()
}

// generateIssueCommands creates command snippets for a single issue
func generateIssueCommands(issue model.Issue, options ReportOptions) string {
	var sb strings.Builder

	// Skip command snippets for closed issues
	if isClosedLikeStatus(issue.Status) {
		return ""
	}

	claimable := options.AuthorityComplete && options.Readiness != nil && options.Readiness.Claimable(issue.ID, options.GeneratedAt)
	actions := issue.Actions(claimable)
	if actions.Show == nil {
		return ""
	}

	sb.WriteString("<details>\n<summary>📋 Commands</summary>\n\n")
	sb.WriteString("```bash\n")

	if actions.Claim != nil {
		sb.WriteString("# Atomically claim; the live tracker may reject a stale recommendation\n")
		sb.WriteString(actions.Claim.Shell + "\n\n")
	}

	sb.WriteString("# View full details\n")
	sb.WriteString(actions.Show.Shell + "\n")

	sb.WriteString("```\n\n")
	sb.WriteString("</details>\n\n")

	return sb.String()
}

// shellEscape escapes a string for safe use in shell commands.
// Uses single quotes and escapes any single quotes within the string.
func shellEscape(s string) string {
	// If the string contains no special characters, return as-is
	if isShellSafe(s) {
		return s
	}
	// Otherwise, wrap in single quotes and escape any single quotes
	escaped := strings.ReplaceAll(s, "'", "'\"'\"'")
	return "'" + escaped + "'"
}

// isShellSafe returns true if the string is safe to use unquoted in shell
func isShellSafe(s string) bool {
	for _, r := range s {
		if !isShellSafeChar(r) {
			return false
		}
	}
	return len(s) > 0
}

// isShellSafeChar returns true if the character is safe in unquoted shell strings
func isShellSafeChar(r rune) bool {
	// Allow alphanumeric, hyphen, underscore, period, and some punctuation
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == ':'
}

// ============================================================================
// Priority Brief Export (bv-96)
// ============================================================================

// PriorityBriefConfig configures the priority brief generation
type PriorityBriefConfig struct {
	MaxRecommendations int    // Max recommendations to include (default: 5)
	MaxQuickWins       int    // Max quick wins to include (default: 3)
	MaxBlockers        int    // Max blockers to include (default: 3)
	IncludeWhatIf      bool   // Include what-if deltas
	IncludeLegend      bool   // Include metric legend
	DataHash           string // Optional data hash for verification
}

// DefaultPriorityBriefConfig returns sensible defaults for the priority brief
func DefaultPriorityBriefConfig() PriorityBriefConfig {
	return PriorityBriefConfig{
		MaxRecommendations: 5,
		MaxQuickWins:       3,
		MaxBlockers:        3,
		IncludeWhatIf:      true,
		IncludeLegend:      true,
	}
}

// GeneratePriorityBriefFromTriage creates a priority brief from a TriageResult (bv-96)
// This is the production version that takes proper triage data
func GeneratePriorityBriefFromTriageJSON(triageJSON []byte, config PriorityBriefConfig) (string, error) {
	// Parse the JSON
	var triage struct {
		Meta struct {
			Version     string    `json:"version"`
			GeneratedAt time.Time `json:"generated_at"`
			Phase2Ready bool      `json:"phase2_ready"`
			IssueCount  int       `json:"issue_count"`
		} `json:"meta"`
		QuickRef struct {
			OpenCount       int `json:"open_count"`
			ActionableCount int `json:"actionable_count"`
			BlockedCount    int `json:"blocked_count"`
			InProgressCount int `json:"in_progress_count"`
			TopPicks        []struct {
				ID       string   `json:"id"`
				Title    string   `json:"title"`
				Score    float64  `json:"score"`
				Reasons  []string `json:"reasons"`
				Unblocks int      `json:"unblocks"`
			} `json:"top_picks"`
		} `json:"quick_ref"`
		Recommendations []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Status    string   `json:"status"`
			Priority  int      `json:"priority"`
			Score     float64  `json:"score"`
			Action    string   `json:"action"`
			Reasons   []string `json:"reasons"`
			Breakdown struct {
				PageRankNorm     float64 `json:"pagerank_norm"`
				BetweennessNorm  float64 `json:"betweenness_norm"`
				TimeToImpactNorm float64 `json:"time_to_impact_norm"`
			} `json:"breakdown"`
		} `json:"recommendations"`
		QuickWins []struct {
			ID     string  `json:"id"`
			Title  string  `json:"title"`
			Score  float64 `json:"score"`
			Reason string  `json:"reason"`
		} `json:"quick_wins"`
		BlockersToClear []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			UnblocksCount int    `json:"unblocks_count"`
			Actionable    bool   `json:"actionable"`
		} `json:"blockers_to_clear"`
	}

	if err := json.Unmarshal(triageJSON, &triage); err != nil {
		return "", fmt.Errorf("failed to parse triage JSON: %w", err)
	}

	var sb strings.Builder

	// Header
	sb.WriteString("# 📊 Priority Brief\n\n")
	sb.WriteString(fmt.Sprintf("*Generated: %s*  \n", triage.Meta.GeneratedAt.Format("2006-01-02 15:04")))
	sb.WriteString(fmt.Sprintf("*Version: %s | Issues: %d*\n\n", triage.Meta.Version, triage.Meta.IssueCount))

	// Data hash
	if config.DataHash != "" {
		sb.WriteString(fmt.Sprintf("**Hash:** `%s`\n\n", config.DataHash))
	}

	// Summary stats
	sb.WriteString("## 📈 Summary\n\n")
	sb.WriteString("| Open | In Progress | Blocked | Actionable |\n")
	sb.WriteString("|:----:|:-----------:|:-------:|:----------:|\n")
	sb.WriteString(fmt.Sprintf("| %d | %d | %d | %d |\n\n",
		triage.QuickRef.OpenCount,
		triage.QuickRef.InProgressCount,
		triage.QuickRef.BlockedCount,
		triage.QuickRef.ActionableCount))

	sb.WriteString("---\n\n")

	// Top Recommendations
	sb.WriteString("## 🎯 Top Recommendations\n\n")
	if len(triage.Recommendations) == 0 {
		sb.WriteString("*No recommendations available.*\n\n")
	} else {
		sb.WriteString("| # | Issue | Type | P | Score | PR | BW | TI | Top Reason |\n")
		sb.WriteString("|:-:|-------|:----:|:-:|:-----:|:--:|:--:|:--:|------------|\n")

		limit := config.MaxRecommendations
		if limit > len(triage.Recommendations) {
			limit = len(triage.Recommendations)
		}

		for i := 0; i < limit; i++ {
			rec := triage.Recommendations[i]
			typeIcon := getTypeIcon(rec.Type)
			reason := "-"
			if len(rec.Reasons) > 0 {
				reason = truncateString(rec.Reasons[0], 30)
			}
			sb.WriteString(fmt.Sprintf("| %d | **%s** %s | %s | P%d | %.2f | %s | %s | %s | %s |\n",
				i+1,
				rec.ID,
				truncateString(rec.Title, 25),
				typeIcon,
				rec.Priority,
				rec.Score,
				barChart(rec.Breakdown.PageRankNorm),
				barChart(rec.Breakdown.BetweennessNorm),
				barChart(rec.Breakdown.TimeToImpactNorm),
				reason,
			))
		}
		sb.WriteString("\n")
	}

	// Quick Wins
	sb.WriteString("## ⚡ Quick Wins\n\n")
	if len(triage.QuickWins) == 0 {
		sb.WriteString("*No quick wins identified.*\n\n")
	} else {
		sb.WriteString("| Issue | Reason |\n")
		sb.WriteString("|-------|--------|\n")

		limit := config.MaxQuickWins
		if limit > len(triage.QuickWins) {
			limit = len(triage.QuickWins)
		}

		for i := 0; i < limit; i++ {
			qw := triage.QuickWins[i]
			sb.WriteString(fmt.Sprintf("| **%s** %s | %s |\n",
				qw.ID,
				truncateString(qw.Title, 30),
				truncateString(qw.Reason, 40),
			))
		}
		sb.WriteString("\n")
	}

	// Blockers
	sb.WriteString("## 🚧 Blockers to Clear\n\n")
	if len(triage.BlockersToClear) == 0 {
		sb.WriteString("*No critical blockers.*\n\n")
	} else {
		sb.WriteString("| Issue | Unblocks | Ready? |\n")
		sb.WriteString("|-------|:--------:|:------:|\n")

		limit := config.MaxBlockers
		if limit > len(triage.BlockersToClear) {
			limit = len(triage.BlockersToClear)
		}

		for i := 0; i < limit; i++ {
			b := triage.BlockersToClear[i]
			ready := "❌"
			if b.Actionable {
				ready = "✅"
			}
			sb.WriteString(fmt.Sprintf("| **%s** %s | %d | %s |\n",
				b.ID,
				truncateString(b.Title, 30),
				b.UnblocksCount,
				ready,
			))
		}
		sb.WriteString("\n")
	}

	// Legend
	if config.IncludeLegend {
		sb.WriteString("---\n\n")
		sb.WriteString("## 📖 Legend\n\n")
		sb.WriteString("| Symbol | Meaning |\n")
		sb.WriteString("|:------:|:--------|\n")
		sb.WriteString("| **PR** | PageRank - dependency importance |\n")
		sb.WriteString("| **BW** | Betweenness - critical path frequency |\n")
		sb.WriteString("| **TI** | Time-to-Impact - urgency factor |\n")
		sb.WriteString("| █░░░ | Low (0-25%) |\n")
		sb.WriteString("| ██░░ | Medium (25-50%) |\n")
		sb.WriteString("| ███░ | High (50-75%) |\n")
		sb.WriteString("| ████ | Very High (75-100%) |\n")
	}

	return sb.String(), nil
}

// barChart creates a mini ASCII bar chart for a 0-1 value
func barChart(value float64) string {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	filled := int(value * 4)
	switch filled {
	case 0:
		return "░░░░"
	case 1:
		return "█░░░"
	case 2:
		return "██░░"
	case 3:
		return "███░"
	default:
		return "████"
	}
}

// truncateString truncates a string to maxLen runes with ellipsis.
// Uses rune-based counting to safely handle UTF-8 multi-byte characters.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

// getTypeIcon returns a compact icon for issue type (for tables)
func getTypeIcon(issueType string) string {
	switch issueType {
	case "bug":
		return "🐛"
	case "feature":
		return "✨"
	case "task":
		return "📋"
	case "epic":
		return "🚀"
	case "chore":
		return "🧹"
	default:
		return "•"
	}
}
