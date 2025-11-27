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
}

func (i IssueItem) Title() string {
	return i.Issue.Title
}

func (i IssueItem) Description() string {
	return fmt.Sprintf("%s %s • %s", i.Issue.ID, i.Issue.Status, i.Issue.Assignee)
}

func (i IssueItem) FilterValue() string {
	// Enhanced filter value including labels and assignee
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

	return sb.String()
}
