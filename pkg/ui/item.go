package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// DiffStatus represents the diff state of an issue in time-travel mode
type DiffStatus int

const (
	DiffStatusNone     DiffStatus = iota // No diff or not in time-travel mode
	DiffStatusNew                        // Issue was added since comparison point
	DiffStatusClosed                     // Issue was closed since comparison point
	DiffStatusModified                   // Issue was modified since comparison point
	DiffStatusReopened                   // Issue was reopened (closed -> open) since comparison point
)

// DiffBadge returns the badge string for a diff status
func (s DiffStatus) Badge() string {
	switch s {
	case DiffStatusNew:
		return "🆕"
	case DiffStatusClosed:
		return "✅"
	case DiffStatusModified:
		return "~"
	case DiffStatusReopened:
		return "[REOPENED]"
	default:
		return ""
	}
}

// IssueItem wraps model.Issue to implement list.Item
type IssueItem struct {
	Issue      model.Issue
	GraphScore float64
	Impact     float64
	DiffStatus DiffStatus // Diff state for time-travel mode
	RepoPrefix string     // Repository prefix for workspace mode (e.g., "api", "web")

	// Semantic/hybrid search scores (set when search is active)
	SearchScore      float64
	SearchTextScore  float64
	SearchComponents map[string]float64
	SearchScoreSet   bool

	// Triage insights (bv-151)
	TriageScore   float64  // Unified triage score (0-1)
	TriageReason  string   // Primary reason for recommendation
	TriageReasons []string // All triage reasons
	IsQuickWin    bool     // True if identified as a quick win
	IsBlocker     bool     // True if this item blocks significant downstream work
	UnblocksCount int      // Number of items this unblocks
}

func (i IssueItem) Title() string {
	return i.Issue.Title
}

func (i IssueItem) Description() string {
	return fmt.Sprintf("%s %s • %s", i.Issue.ID, i.Issue.Status, i.Issue.Assignee)
}

func (i IssueItem) FilterValue() string {
	// Enhanced filter value including labels, assignee, and repo prefix
	var sb strings.Builder
	sb.WriteString(i.Issue.Title)
	sb.WriteString(" ")
	sb.WriteString(i.Issue.ID)
	sb.WriteString(" ")
	sb.WriteString(string(i.Issue.Status))
	sb.WriteString(" ")
	sb.WriteString(string(i.Issue.IssueType))

	if i.Issue.Assignee != "" {
		sb.WriteString(" ")
		sb.WriteString(i.Issue.Assignee)
	}

	if len(i.Issue.Labels) > 0 {
		sb.WriteString(" ")
		sb.WriteString(strings.Join(i.Issue.Labels, " "))
	}

	// Include repo prefix for filtering
	if i.RepoPrefix != "" {
		sb.WriteString(" ")
		sb.WriteString(i.RepoPrefix)
	}

	return sb.String()
}

// ExtractRepoPrefix extracts the repository prefix from a namespaced issue ID.
// For example, "api-AUTH-123" returns "api", "web-UI-1" returns "web".
// If no prefix is detected (no separator), returns empty string.
func ExtractRepoPrefix(id string) string {
	// Try common separators: -, :, _
	for _, sep := range []string{"-", ":", "_"} {
		if idx := strings.Index(id, sep); idx > 0 {
			// Check if what's before the separator looks like a short prefix (<=10 chars)
			prefix := id[:idx]
			if len(prefix) <= 10 && isAlphanumeric(prefix) {
				return prefix
			}
		}
	}
	return ""
}

// isAlphanumeric checks if a string contains only alphanumeric characters
func isAlphanumeric(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 0
}
