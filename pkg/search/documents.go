package search

import (
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// IssueDocument returns the default text representation used for semantic indexing.
// We boost important fields by repeating them: ID (x3), title (x2), labels (x1), description (x1).
func IssueDocument(issue model.Issue) string {
	var parts []string

	id := strings.TrimSpace(issue.ID)
	if id != "" {
		parts = append(parts, id, id, id)
	}

	title := strings.TrimSpace(issue.Title)
	if title != "" {
		parts = append(parts, title, title)
	}

	labels := strings.TrimSpace(strings.Join(issue.Labels, " "))
	if labels != "" {
		parts = append(parts, labels)
	}

	if issue.ExternalRef != nil {
		if extRef := strings.TrimSpace(*issue.ExternalRef); extRef != "" {
			parts = append(parts, extRef)
		}
	}

	desc := strings.TrimSpace(issue.Description)
	if desc != "" {
		parts = append(parts, desc)
	}

	return strings.Join(parts, "\n")
}

// DocumentsFromIssues builds an ID->document map suitable for indexing.
func DocumentsFromIssues(issues []model.Issue) map[string]string {
	docs := make(map[string]string, len(issues))
	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		docs[issue.ID] = IssueDocument(issue)
	}
	return docs
}
