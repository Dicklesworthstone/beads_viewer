package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// FormatTimeRel returns a relative time string (e.g., "2h ago", "3d ago")
func FormatTimeRel(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	d := time.Since(t)
	if d < 0 {
		// Future timestamps treated as now
		return "now"
	}
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

// truncateRunesHelper truncates a string to max visual width (cells), adding suffix if needed.
// Uses go-runewidth to handle wide characters correctly.
func truncateRunesHelper(s string, maxWidth int, suffix string) string {
	if maxWidth <= 0 {
		return ""
	}

	width := runewidth.StringWidth(s)
	if width <= maxWidth {
		return s
	}

	suffixWidth := runewidth.StringWidth(suffix)
	if suffixWidth > maxWidth {
		// Even suffix is too wide, truncate suffix
		return runewidth.Truncate(suffix, maxWidth, "")
	}

	targetWidth := maxWidth - suffixWidth
	return runewidth.Truncate(s, targetWidth, "") + suffix
}

// padRight pads string s with spaces on the right to reach visual width.
// Uses go-runewidth to handle wide characters (emojis, CJK) correctly,
// consistent with truncateRunesHelper which also uses visual width.
func padRight(s string, width int) string {
	visualWidth := runewidth.StringWidth(s)
	if visualWidth >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visualWidth)
}

// truncate truncates string s to maxRunes
func truncate(s string, maxRunes int) string {
	return truncateRunesHelper(s, maxRunes, "…")
}

// DependencyNode represents a visual node in the dependency tree
type DependencyNode struct {
	ID        string
	Title     string
	Status    string
	Type      string // "root", "blocks", "related", etc.
	Children  []*DependencyNode
	Reference bool // The target is also shown at its canonical occurrence.
	Cycle     bool // This edge closes a directed DFS cycle in the displayed graph.
}

// BuildDependencyTree constructs a tree from dependencies for visualization.
// Each reachable issue is expanded once, at its shortest distance from the root.
// All dependency edges within maxDepth remain visible; repeated targets become
// reference rows. A zero maxDepth includes the entire reachable graph.
func BuildDependencyTree(rootID string, issueMap map[string]*model.Issue, maxDepth int) *DependencyNode {
	// Discover minimum depths first. A deep occurrence visited before a short
	// path must not consume the expansion and hide descendants at the limit.
	depths := map[string]int{rootID: 0}
	order := []string{rootID}
	for i := 0; i < len(order); i++ {
		id := order[i]
		issue := issueMap[id]
		if issue == nil || (maxDepth > 0 && depths[id] >= maxDepth) {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if _, exists := depths[dep.DependsOnID]; !exists {
				depths[dep.DependsOnID] = depths[id] + 1
				order = append(order, dep.DependsOnID)
			}
		}
	}

	// Reference rows alone do not identify cycles between sibling branches.
	// Mark DFS backedges over the displayed edge union, in stable source order.
	// These are cycle-closing edges, not an enumeration of every cycle/SCC edge.
	state := make(map[string]uint8, len(order))
	cycleEdges := make(map[string]map[string]bool)
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		issue := issueMap[id]
		if issue != nil && (maxDepth <= 0 || depths[id] < maxDepth) {
			for _, dep := range issue.Dependencies {
				if dep == nil {
					continue
				}
				switch state[dep.DependsOnID] {
				case 0:
					visit(dep.DependsOnID)
				case 1:
					if cycleEdges[id] == nil {
						cycleEdges[id] = make(map[string]bool)
					}
					cycleEdges[id][dep.DependsOnID] = true
				}
			}
		}
		state[id] = 2
	}
	visit(rootID)

	makeNode := func(id, depType string) *DependencyNode {
		node := &DependencyNode{ID: id, Title: "(not found)", Status: "?", Type: depType}
		if issue := issueMap[id]; issue != nil {
			node.ID, node.Title, node.Status = issue.ID, issue.Title, string(issue.Status)
		}
		return node
	}
	root := makeNode(rootID, "root")
	expanded := map[string]*DependencyNode{rootID: root}
	for _, id := range order {
		issue := issueMap[id]
		if issue == nil || (maxDepth > 0 && depths[id] >= maxDepth) {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			child := makeNode(dep.DependsOnID, string(dep.Type))
			child.Cycle = cycleEdges[id][dep.DependsOnID]
			if _, exists := expanded[dep.DependsOnID]; exists {
				child.Reference = true
			} else {
				expanded[dep.DependsOnID] = child
			}
			expanded[id].Children = append(expanded[id].Children, child)
		}
	}
	return root
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
	sb.WriteString(prefix)
	sb.WriteString(connector)
	sb.WriteString(statusIcon)
	sb.WriteByte(' ')
	sb.WriteString(typeIcon)
	sb.WriteByte(' ')
	sb.WriteString(node.ID)
	sb.WriteByte(' ')
	sb.WriteString(title)
	sb.WriteString(" (")
	sb.WriteString(node.Status)
	sb.WriteString(") [")
	sb.WriteString(node.Type)
	sb.WriteByte(']')
	if node.Cycle {
		sb.WriteString(" (cycle)")
	}
	if node.Reference {
		sb.WriteString(" (reference: shown elsewhere)")
	}
	sb.WriteByte('\n')

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

// GetPriorityLabel returns a compact text label for priority (P0, P1, etc.)
func GetPriorityLabel(priority int) string {
	if priority >= 0 && priority <= 4 {
		return fmt.Sprintf("P%d", priority)
	}
	return "P?"
}

// GetAgeDays returns the number of days since the given time
func GetAgeDays(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	return int(time.Since(t).Hours() / 24)
}

// GetAgeColor returns a color based on staleness:
// green (<7 days), yellow (7-30 days), red (>30 days)
func GetAgeColor(t time.Time) lipgloss.AdaptiveColor {
	days := GetAgeDays(t)
	switch {
	case days < 7:
		return ColorSuccess // Green - fresh
	case days < 30:
		return ColorWarning // Yellow/Orange - aging
	default:
		return ColorDanger // Red - stale
	}
}

// FormatAgeBadge returns a compact age string with timer emoji (e.g., "3d ⏱")
func FormatAgeBadge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	days := GetAgeDays(t)
	switch {
	case days == 0:
		return "<1d"
	case days < 7:
		return fmt.Sprintf("%dd", days)
	case days < 30:
		return fmt.Sprintf("%dw", days/7)
	default:
		return fmt.Sprintf("%dmo", days/30)
	}
}
