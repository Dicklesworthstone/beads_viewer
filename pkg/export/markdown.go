package export

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

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
func GenerateMarkdown(issues []model.Issue, title string) (string, error) {
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", time.Now().Format(time.RFC1123)))

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
	sb.WriteString(generateQuickActions(issues))

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
	sb.WriteString("## Dependency Graph\n\n")
	sb.WriteString("```mermaid\n")

	issueIDs := make(map[string]bool)
	for _, i := range issues {
		issueIDs[i.ID] = true
	}

	graph := GenerateMermaidGraph(issues, issueIDs, MermaidConfig{ShowNoDependenciesNode: true})
	sb.WriteString(graph)

	sb.WriteString("```\n\n")
	sb.WriteString("---\n\n")

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
				if dep.Type == model.DepBlocks {
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
		sb.WriteString(generateIssueCommands(i))

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

// generateQuickActions creates a Quick Actions section with bulk commands
func generateQuickActions(issues []model.Issue) string {
	var sb strings.Builder

	// Collect non-closed issues for bulk operations
	var openIDs, inProgressIDs, blockedIDs []string
	var highPriorityIDs []string // P0 and P1

	for _, i := range issues {
		escapedID := shellEscape(i.ID)
		switch i.Status {
		case model.StatusOpen:
			openIDs = append(openIDs, escapedID)
		case model.StatusInProgress:
			inProgressIDs = append(inProgressIDs, escapedID)
		case model.StatusBlocked:
			blockedIDs = append(blockedIDs, escapedID)
		}
		if !isClosedLikeStatus(i.Status) && i.Priority <= 1 {
			highPriorityIDs = append(highPriorityIDs, escapedID)
		}
	}

	// Only generate section if there are actionable items
	if len(openIDs)+len(inProgressIDs)+len(blockedIDs) == 0 {
		return ""
	}

	sb.WriteString("## Quick Actions\n\n")
	sb.WriteString("Ready-to-run commands for bulk operations:\n\n")
	sb.WriteString("```bash\n")

	// Close in-progress items (most common action)
	if len(inProgressIDs) > 0 {
		sb.WriteString("# Close all in-progress items\n")
		sb.WriteString(fmt.Sprintf("%s close %s\n\n", analysis.BeadsCLI(), strings.Join(inProgressIDs, " ")))
	}

	// Close open items
	if len(openIDs) > 0 && len(openIDs) <= 10 {
		sb.WriteString("# Close all open items\n")
		sb.WriteString(fmt.Sprintf("%s close %s\n\n", analysis.BeadsCLI(), strings.Join(openIDs, " ")))
	} else if len(openIDs) > 10 {
		sb.WriteString(fmt.Sprintf("# Close open items (%d total, showing first 10)\n", len(openIDs)))
		sb.WriteString(fmt.Sprintf("%s close %s\n\n", analysis.BeadsCLI(), strings.Join(openIDs[:10], " ")))
	}

	// Bulk priority update for high-priority items
	if len(highPriorityIDs) > 0 {
		sb.WriteString("# View high-priority items (P0/P1)\n")
		sb.WriteString(fmt.Sprintf("%s show %s\n\n", analysis.BeadsCLI(), strings.Join(highPriorityIDs, " ")))
	}

	// Unblock blocked items
	if len(blockedIDs) > 0 {
		sb.WriteString("# Update blocked items to in_progress when unblocked\n")
		sb.WriteString(fmt.Sprintf("%s update %s -s in_progress\n", analysis.BeadsCLI(), strings.Join(blockedIDs, " ")))
	}

	sb.WriteString("```\n\n")

	return sb.String()
}

// generateIssueCommands creates command snippets for a single issue
func generateIssueCommands(issue model.Issue) string {
	var sb strings.Builder

	// Skip command snippets for closed issues
	if isClosedLikeStatus(issue.Status) {
		return ""
	}

	escapedID := shellEscape(issue.ID)

	sb.WriteString("<details>\n<summary>📋 Commands</summary>\n\n")
	sb.WriteString("```bash\n")

	// Status transitions based on current state
	switch issue.Status {
	case model.StatusOpen:
		sb.WriteString("# Start working on this issue\n")
		sb.WriteString(fmt.Sprintf("%s update %s -s in_progress\n\n", analysis.BeadsCLI(), escapedID))
	case model.StatusInProgress:
		sb.WriteString("# Mark as complete\n")
		sb.WriteString(fmt.Sprintf("%s close %s\n\n", analysis.BeadsCLI(), escapedID))
	case model.StatusBlocked:
		sb.WriteString("# Unblock and start working\n")
		sb.WriteString(fmt.Sprintf("%s update %s -s in_progress\n\n", analysis.BeadsCLI(), escapedID))
	}

	// Common actions
	sb.WriteString("# Add a comment\n")
	sb.WriteString(fmt.Sprintf("%s comment %s 'Your comment here'\n\n", analysis.BeadsCLI(), escapedID))

	sb.WriteString("# Change priority (0=Critical, 1=High, 2=Medium, 3=Low)\n")
	sb.WriteString(fmt.Sprintf("%s update %s -p 1\n\n", analysis.BeadsCLI(), escapedID))

	sb.WriteString("# View full details\n")
	sb.WriteString(fmt.Sprintf("%s show %s\n", analysis.BeadsCLI(), escapedID))

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

// GeneratePriorityBrief creates a compact Markdown priority brief from triage data (bv-96)
// This is designed for human readability and can be rendered to PNG
func GeneratePriorityBrief(triage interface{}, config PriorityBriefConfig) string {
	var sb strings.Builder

	// Use reflection or JSON marshal/unmarshal to access fields
	// For simplicity, we'll use direct field access assuming the types match

	// Header
	sb.WriteString("# 📊 Priority Brief\n\n")
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", time.Now().Format("2006-01-02 15:04")))

	// Add data hash if provided
	if config.DataHash != "" {
		sb.WriteString(fmt.Sprintf("**Data Hash:** `%s`\n\n", config.DataHash))
	}

	sb.WriteString("---\n\n")

	// This is a simplified implementation - in production, you'd use proper type casting
	// For now, return a placeholder that demonstrates the structure
	sb.WriteString("## 🎯 Top Recommendations\n\n")
	sb.WriteString("| # | Issue | Type | Priority | Score | Top Reason |\n")
	sb.WriteString("|---|-------|------|----------|-------|------------|\n")
	sb.WriteString("| 1 | *Run `bv --robot-triage` for data* | - | - | - | - |\n\n")

	sb.WriteString("## ⚡ Quick Wins\n\n")
	sb.WriteString("| Issue | Reason | Impact |\n")
	sb.WriteString("|-------|--------|--------|\n")
	sb.WriteString("| *Run `bv --robot-triage` for data* | - | - |\n\n")

	sb.WriteString("## 🚧 Blockers to Clear\n\n")
	sb.WriteString("| Issue | Unblocks | Actionable |\n")
	sb.WriteString("|-------|----------|------------|\n")
	sb.WriteString("| *Run `bv --robot-triage` for data* | - | - |\n\n")

	// Legend
	if config.IncludeLegend {
		sb.WriteString("---\n\n")
		sb.WriteString("## 📖 Legend\n\n")
		sb.WriteString("| Metric | Description |\n")
		sb.WriteString("|--------|-------------|\n")
		sb.WriteString("| **PR** | PageRank - importance based on incoming dependencies |\n")
		sb.WriteString("| **BW** | Betweenness - how often this issue is on critical paths |\n")
		sb.WriteString("| **TI** | Time-to-Impact - urgency based on depth and estimates |\n")
		sb.WriteString("| **Score** | Composite priority score (0.0-1.0, higher = more important) |\n")
		sb.WriteString("| **Unblocks** | Number of issues that can proceed once this is done |\n\n")
	}

	return sb.String()
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
