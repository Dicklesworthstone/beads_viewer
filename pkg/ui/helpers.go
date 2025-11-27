package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// FormatTimeRel returns a relative time string (e.g., "2h ago", "3d ago")
func FormatTimeRel(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	}
}

// truncateRunesHelper truncates a string to maxRunes runes, adding suffix if needed.
// This is UTF-8 safe.
func truncateRunesHelper(s string, maxRunes int, suffix string) string {
	if maxRunes <= 0 {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)
	if runeCount <= maxRunes {
		return s
	}

	suffixLen := utf8.RuneCountInString(suffix)
	if maxRunes <= suffixLen {
		return suffix
	}

	runes := []rune(s)
	return string(runes[:maxRunes-suffixLen]) + suffix
}

// DependencyNode represents a visual node in the dependency tree
type DependencyNode struct {
	ID       string
	Title    string
	Status   string
	Type     string // "root", "blocks", "related", etc.
	Children []*DependencyNode
}

// BuildDependencyTree constructs a tree from dependencies for visualization.
// maxDepth limits recursion to prevent infinite loops and performance issues.
// Set maxDepth to 0 for unlimited depth (use with caution).
func BuildDependencyTree(rootID string, issueMap map[string]*model.Issue, maxDepth int) *DependencyNode {
	visited := make(map[string]bool)
	return buildTreeRecursive(rootID, issueMap, "root", visited, 0, maxDepth)
}

func buildTreeRecursive(id string, issueMap map[string]*model.Issue, depType string, visited map[string]bool, depth, maxDepth int) *DependencyNode {
	// Check depth limit (0 = unlimited)
	if maxDepth > 0 && depth > maxDepth {
		return nil
	}

	// Cycle detection
	if visited[id] {
		return &DependencyNode{
			ID:     id,
			Title:  "(cycle)",
			Status: "?",
			Type:   depType,
		}
	}

	issue, exists := issueMap[id]
	if !exists {
		return &DependencyNode{
			ID:     id,
			Title:  "(not found)",
			Status: "?",
			Type:   depType,
		}
	}

	visited[id] = true
	defer func() { visited[id] = false }() // Allow revisiting in different branches

	node := &DependencyNode{
		ID:     issue.ID,
		Title:  issue.Title,
		Status: string(issue.Status),
		Type:   depType,
	}

	// Recursively add children (dependencies)
	for _, dep := range issue.Dependencies {
		childNode := buildTreeRecursive(dep.DependsOnID, issueMap, string(dep.Type), visited, depth+1, maxDepth)
		if childNode != nil {
			node.Children = append(node.Children, childNode)
		}
	}

	return node
}

// RenderDependencyTree renders a dependency tree as a formatted string
func RenderDependencyTree(node *DependencyNode) string {
	if node == nil {
		return "No dependency data."
	}

	var sb strings.Builder
	sb.WriteString("Dependency Graph:\n")
	renderTreeNode(&sb, node, "", true, true) // isRoot=true for root node
	return sb.String()
}

func renderTreeNode(sb *strings.Builder, node *DependencyNode, prefix string, isLast bool, isRoot bool) {
	if node == nil {
		return
	}

	// Determine the connector
	var connector string
	if isRoot {
		connector = "" // Root has no connector
	} else if isLast {
		connector = "└── "
	} else {
		connector = "├── "
	}

	// Get icons
	statusIcon := GetStatusIcon(node.Status)
	typeIcon := getDepTypeIcon(node.Type)

	// Truncate title if too long (UTF-8 safe)
	title := truncateRunesHelper(node.Title, 40, "...")

	// Render this node
	sb.WriteString(fmt.Sprintf("%s%s%s %s %s %s (%s) [%s]\n",
		prefix,
		connector,
		statusIcon,
		typeIcon,
		node.ID,
		title,
		node.Status,
		node.Type,
	))

	// Calculate prefix for children
	var childPrefix string
	if isRoot {
		childPrefix = "" // Children of root start with no prefix
	} else if isLast {
		childPrefix = prefix + "    "
	} else {
		childPrefix = prefix + "│   "
	}

	// Render children
	for i, child := range node.Children {
		isChildLast := i == len(node.Children)-1
		renderTreeNode(sb, child, childPrefix, isChildLast, false) // isRoot=false for children
	}
}

func getDepTypeIcon(depType string) string {
	switch depType {
	case "root":
		return "📍"
	case "blocks":
		return "⛔"
	case "related":
		return "🔗"
	case "parent-child":
		return "📦"
	case "discovered-from":
		return "🔍"
	default:
		return "•"
	}
}

// GetStatusIcon returns a colored icon for a status
func GetStatusIcon(s string) string {
	switch s {
	case "open":
		return "🟢"
	case "in_progress":
		return "🔵"
	case "blocked":
		return "🔴"
	case "closed":
		return "⚫"
	default:
		return "⚪"
	}
}

// GetPriorityIcon returns the emoji for a priority level
func GetPriorityIcon(priority int) string {
	switch priority {
	case 0:
		return "🔥" // Critical
	case 1:
		return "⚡" // High
	case 2:
		return "🔹" // Medium
	case 3:
		return "☕" // Low
	case 4:
		return "💤" // Backlog
	default:
		return "  "
	}
}
