// tree.go - Hierarchical tree view for epic/task/subtask relationships (bv-gllx)
package ui

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// TreeState represents the persistent state of the tree view (bv-zv7p).
// This is saved to .beads/tree-state.json to preserve expand/collapse state
// across sessions.
//
// File format (JSON):
//
//	{
//	  "version": 1,
//	  "expanded": {
//	    "bv-123": true,   // explicitly expanded
//	    "bv-456": false   // explicitly collapsed
//	  }
//	}
//
// Design notes:
//   - Only stores explicit user changes; nodes not in the map use default behavior
//   - Default: expanded for depth < 2, collapsed otherwise
//   - Version field enables future schema migrations
//   - Corrupted/missing file = use defaults (graceful degradation)
type TreeState struct {
	Version  int             `json:"version"`  // Schema version (currently 1)
	Expanded map[string]bool `json:"expanded"` // Issue ID -> explicitly set state
}

// TreeStateVersion is the current schema version for tree persistence
const TreeStateVersion = 1

// DefaultTreeState returns a new TreeState with sensible defaults
func DefaultTreeState() *TreeState {
	return &TreeState{
		Version:  TreeStateVersion,
		Expanded: make(map[string]bool),
	}
}

// treeStateFileName is the filename for persisted tree state
const treeStateFileName = "tree-state.json"

// TreeStatePath returns the path to the tree state file.
// By default this is .beads/tree-state.json in the current directory.
// The beadsDir parameter allows overriding the .beads directory location
// (e.g., from BEADS_DIR environment variable).
func TreeStatePath(beadsDir string) string {
	if beadsDir == "" {
		beadsDir = ".beads"
	}
	return filepath.Join(beadsDir, treeStateFileName)
}

// SetBeadsDir sets the beads directory for persistence (bv-19vz).
// This should be called before any expand/collapse operations if a custom
// beads directory is desired. If not called, defaults to ".beads" in cwd.
func (t *TreeModel) SetBeadsDir(dir string) {
	t.beadsDir = dir
}

// saveState persists the current expand/collapse state to disk (bv-19vz).
// Only stores explicit user changes; nodes not in the map use default behavior.
// Errors are logged but do not interrupt the user experience.
func (t *TreeModel) saveState() {
	state := &TreeState{
		Version:  TreeStateVersion,
		Expanded: make(map[string]bool),
	}

	// Walk all nodes and record explicit expand state
	var walk func(node *IssueTreeNode)
	walk = func(node *IssueTreeNode) {
		if node == nil || node.Issue == nil {
			return
		}

		// Default: expanded for depth < 2, collapsed otherwise
		defaultExpanded := node.Depth < 2
		if node.Expanded != defaultExpanded {
			state.Expanded[node.Issue.ID] = node.Expanded
		}

		for _, child := range node.Children {
			walk(child)
		}
	}

	for _, root := range t.roots {
		walk(root)
	}

	// Write to file
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("warning: failed to marshal tree state: %v", err)
		return
	}

	path := TreeStatePath(t.beadsDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("warning: failed to create state directory %s: %v", dir, err)
		return
	}

	if err := writeTreeStateFile(path, data); err != nil {
		log.Printf("warning: failed to write tree state to %s: %v", path, err)
		return
	}
}

func writeTreeStateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary tree state: %w", err)
	}

	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temporary tree state: %w", err)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temporary tree state: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary tree state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace tree state: %w", err)
	}

	cleanup = false
	return nil
}

// loadState restores expand/collapse state from disk (bv-afcm).
// If the file doesn't exist or is corrupted, defaults are used silently.
func (t *TreeModel) loadState() {
	path := TreeStatePath(t.beadsDir)
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist = first run, use defaults
		return
	}

	var state TreeState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("warning: invalid tree state file, using defaults: %v", err)
		return
	}
	if state.Version != TreeStateVersion {
		log.Printf("warning: unsupported tree state version %d, using defaults", state.Version)
		return
	}

	// Apply loaded state to nodes
	t.applyState(&state)
}

// applyState sets expand state on nodes based on loaded state (bv-afcm).
// Unknown IDs in state are silently ignored (stale IDs handled by bv-0jaz).
func (t *TreeModel) applyState(state *TreeState) {
	if state == nil || len(state.Expanded) == 0 {
		return
	}

	for id, expanded := range state.Expanded {
		if node, ok := t.issueMap[id]; ok {
			node.Expanded = expanded
		}
		// If ID not found, it's stale - ignore
	}
}

// TreeViewMode determines what relationships are displayed
type TreeViewMode int

const (
	TreeModeHierarchy TreeViewMode = iota // parent-child deps (default)
	TreeModeBlocking                      // blocking deps (future)
)

// IssueTreeNode represents a node in the hierarchical issue tree
type IssueTreeNode struct {
	Issue    *model.Issue     // Reference to the actual issue
	Children []*IssueTreeNode // Child nodes
	Expanded bool             // Is this node expanded?
	Depth    int              // Nesting level (0 = root)
	Parent   *IssueTreeNode   // Back-reference for navigation
}

// TreeModel manages the hierarchical tree view state
type TreeModel struct {
	roots          []*IssueTreeNode          // Root nodes (issues with no parent)
	flatList       []*IssueTreeNode          // Flattened visible nodes for navigation
	cursor         int                       // Current selection index in flatList
	viewport       viewport.Model            // For scrolling
	theme          Theme                     // Visual styling
	mode           TreeViewMode              // Hierarchy vs blocking
	issueMap       map[string]*IssueTreeNode // Quick lookup by issue ID
	width          int                       // Available width
	height         int                       // Available height
	viewportOffset int                       // Index of first visible node (bv-r4ng)

	// Build state
	built    bool   // Has tree been built?
	lastHash string // Hash of issues for cache invalidation

	// Persistence state (bv-19vz)
	beadsDir string // Directory containing .beads (for tree-state.json)
}

// NewTreeModel creates an empty tree model
func NewTreeModel(theme Theme) TreeModel {
	return TreeModel{
		theme:    theme,
		mode:     TreeModeHierarchy,
		issueMap: make(map[string]*IssueTreeNode),
	}
}

// buildIssueTreeNodes constructs the hierarchical parent/child tree and returns:
// - roots: root nodes (sorted)
// - nodeMap: issue ID -> tree node
//
// This does NOT load persisted expand/collapse state or build the visible flat list.
// Those remain view concerns handled by TreeModel (so user state can change without
// requiring a snapshot rebuild).
func buildIssueTreeNodes(issues []model.Issue) ([]*IssueTreeNode, map[string]*IssueTreeNode) {
	t := TreeModel{
		issueMap: make(map[string]*IssueTreeNode),
	}
	if len(issues) == 0 {
		return nil, t.issueMap
	}

	// Step 1: Build parent→children index and track which issues have parents
	childrenOf := make(map[string][]*model.Issue)
	hasParent := make(map[string]bool)
	issueByID := make(map[string]*model.Issue)

	for i := range issues {
		issue := &issues[i]
		issueByID[issue.ID] = issue

		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type == model.DepParentChild {
				parentID := dep.DependsOnID
				childrenOf[parentID] = append(childrenOf[parentID], issue)
				hasParent[issue.ID] = true
			}
		}
	}

	// Step 2: Identify root nodes (issues with no parent OR whose parent doesn't exist)
	var rootIssues []*model.Issue
	for i := range issues {
		issue := &issues[i]
		if !hasParent[issue.ID] {
			rootIssues = append(rootIssues, issue)
			continue
		}

		// Issue declares a parent - verify at least one referenced parent exists
		hasValidParent := false
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type == model.DepParentChild {
				if _, exists := issueByID[dep.DependsOnID]; exists {
					hasValidParent = true
					break
				}
			}
		}
		if !hasValidParent {
			rootIssues = append(rootIssues, issue)
		}
	}

	// Step 3: Build tree recursively with cycle detection
	visited := make(map[string]bool)
	for _, issue := range rootIssues {
		node := t.buildNode(issue, 0, childrenOf, nil, visited)
		if node != nil {
			t.roots = append(t.roots, node)
		}
	}

	// A component made entirely of parent-child cycles has no natural root.
	// Follow a deterministic parent chain from each unrepresented issue to a
	// cycle member and use that member as a display root. Starting at an
	// arbitrary descendant would unnecessarily split its hierarchy into roots.
	// The existing path guard keeps the resulting tree finite; source
	// dependencies are not changed.
	var remaining []string
	if len(t.issueMap) < len(issueByID) {
		for id := range issueByID {
			if t.issueMap[id] == nil {
				remaining = append(remaining, id)
			}
		}
	}
	sort.Strings(remaining)
	for _, id := range remaining {
		if t.issueMap[id] != nil {
			continue
		}
		onPath := make(map[string]bool)
		for !onPath[id] {
			onPath[id] = true
			parent := ""
			foundParent := false
			for _, dep := range issueByID[id].Dependencies {
				if dep == nil || dep.Type != model.DepParentChild || issueByID[dep.DependsOnID] == nil {
					continue
				}
				if !foundParent || dep.DependsOnID < parent {
					parent = dep.DependsOnID
					foundParent = true
				}
			}
			if !foundParent {
				break
			}
			id = parent
		}
		if node := t.buildNode(issueByID[id], 0, childrenOf, nil, visited); node != nil {
			t.roots = append(t.roots, node)
		}
	}

	// Step 4: Sort roots by priority, type, then created date
	t.sortNodes(t.roots)

	return t.roots, t.issueMap
}

// SetSize updates the available dimensions for the tree view
func (t *TreeModel) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.viewport.Width = width
	t.viewport.Height = height
}

// Build constructs the tree from issues using parent-child dependencies.
// Implementation for bv-j3ck.
func (t *TreeModel) Build(issues []model.Issue) {
	// Reset state
	t.roots = nil
	t.flatList = nil
	t.issueMap = make(map[string]*IssueTreeNode)
	t.cursor = 0

	if len(issues) == 0 {
		t.built = true
		return
	}

	// Build tree structure (no state) and then apply persisted expand/collapse state.
	roots, nodeMap := buildIssueTreeNodes(issues)
	t.roots = roots
	t.issueMap = nodeMap

	// Step 5: Handle empty tree (no parent-child relationships found)
	// If all issues are roots (no hierarchy), that's fine - show them all
	// The View() will handle displaying a helpful message if needed

	// Step 6: Load persisted state (bv-afcm)
	// This modifies node.Expanded values before we build the flat list
	t.loadState()

	// Step 7: Build the flat list for navigation
	// This must come after loadState so expand states are applied
	t.rebuildFlatList()

	t.built = true
}

// BuildFromSnapshot wires the tree view to precomputed tree data from a DataSnapshot.
// This avoids building the parent/child structure on the UI thread when the snapshot
// already contains it (bv-t435).
func (t *TreeModel) BuildFromSnapshot(snapshot *DataSnapshot) {
	if snapshot == nil {
		return
	}

	// Skip work if we're already built for this snapshot.
	if t.built && snapshot.DataHash != "" && t.lastHash == snapshot.DataHash {
		return
	}

	// Preserve current selection (best-effort) by issue ID.
	prevSelectedID := ""
	if issue := t.SelectedIssue(); issue != nil {
		prevSelectedID = issue.ID
	}

	// Reset view state, but keep dimensions/theme/beadsDir.
	t.roots = snapshot.TreeRoots
	t.issueMap = snapshot.TreeNodeMap

	// If the snapshot didn't include tree data, fall back to building it now.
	if len(t.roots) == 0 || t.issueMap == nil {
		t.Build(snapshot.Issues)
		t.lastHash = snapshot.DataHash
		return
	}

	// Apply persisted expand/collapse state and rebuild visible list.
	t.loadState()
	t.rebuildFlatList()
	t.built = true
	t.lastHash = snapshot.DataHash

	// Restore selection if possible.
	if prevSelectedID != "" {
		for i, node := range t.flatList {
			if node != nil && node.Issue != nil && node.Issue.ID == prevSelectedID {
				t.cursor = i
				t.ensureCursorVisible()
				break
			}
		}
	}
}

// buildNode recursively builds a tree node and its children.
// Uses visited map for cycle detection.
func (t *TreeModel) buildNode(issue *model.Issue, depth int,
	childrenOf map[string][]*model.Issue,
	parent *IssueTreeNode,
	visited map[string]bool) *IssueTreeNode {

	if issue == nil {
		return nil
	}

	// Cycle detection - if we've already visited this node in current path
	if visited[issue.ID] {
		// Return a node marked as part of a cycle (no children to break the loop)
		return &IssueTreeNode{
			Issue:    issue,
			Depth:    depth,
			Parent:   parent,
			Expanded: false,
			// Children intentionally left empty to break cycle
		}
	}

	// Mark as visited for cycle detection
	visited[issue.ID] = true
	defer func() { visited[issue.ID] = false }()

	node := &IssueTreeNode{
		Issue:    issue,
		Depth:    depth,
		Parent:   parent,
		Expanded: depth < 2, // Auto-expand first 2 levels
	}

	// Store in lookup map
	t.issueMap[issue.ID] = node

	// Build children recursively
	children := childrenOf[issue.ID]
	for _, child := range children {
		childNode := t.buildNode(child, depth+1, childrenOf, node, visited)
		if childNode != nil {
			node.Children = append(node.Children, childNode)
		}
	}

	// Sort children
	t.sortNodes(node.Children)

	return node
}

// sortNodes sorts a slice of tree nodes by priority, issue type, then created date.
func (t *TreeModel) sortNodes(nodes []*IssueTreeNode) {
	if len(nodes) <= 1 {
		return
	}

	sort.Slice(nodes, func(i, j int) bool {
		// Defensive: check for nil nodes first
		if nodes[i] == nil || nodes[j] == nil {
			return nodes[i] != nil // Non-nil nodes first
		}
		a, b := nodes[i].Issue, nodes[j].Issue
		if a == nil || b == nil {
			return a != nil // Non-nil issues first
		}

		// 1. Priority (ascending - P0 first)
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}

		// 2. IssueType order: epic → feature → task → bug → chore
		aTypeOrder := issueTypeOrder(a.IssueType)
		bTypeOrder := issueTypeOrder(b.IssueType)
		if aTypeOrder != bTypeOrder {
			return aTypeOrder < bTypeOrder
		}

		// 3. CreatedAt (oldest first for stable ordering)
		return a.CreatedAt.Before(b.CreatedAt)
	})
}

// issueTypeOrder returns a numeric order for issue types.
// Lower numbers sort first: epic → feature → task → bug → chore
func issueTypeOrder(t model.IssueType) int {
	switch t {
	case model.TypeEpic:
		return 0
	case model.TypeFeature:
		return 1
	case model.TypeTask:
		return 2
	case model.TypeBug:
		return 3
	case model.TypeChore:
		return 4
	default:
		return 5
	}
}

// View renders the tree view.
// Implementation for bv-1371, updated for windowed rendering (bv-db02).
// Only renders visible nodes based on viewportOffset and height for O(viewport)
// performance instead of O(n) where n is total nodes.
func (t *TreeModel) View() string {
	if !t.built || len(t.flatList) == 0 {
		return t.renderEmptyState()
	}

	var sb strings.Builder

	// Get visible range - O(1) calculation based on viewportOffset and height
	start, end := t.visibleRange()

	// Render only visible nodes (bv-db02: windowed rendering)
	for i := start; i < end; i++ {
		node := t.flatList[i]
		if node == nil || node.Issue == nil {
			continue
		}

		isSelected := i == t.cursor
		line := t.renderNode(node, isSelected)

		if isSelected {
			// Highlight selected row using theme's Selected style
			line = t.theme.Selected.Render(line)
		}

		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Add position indicator if scrolling is needed (bv-2nax)
	// Only shows when there are more nodes than fit in the viewport
	if len(t.flatList) > t.height && t.height > 0 {
		indicator := t.renderPositionIndicator(start, end)
		sb.WriteString(indicator)
	}

	return sb.String()
}

// renderPositionIndicator renders the scroll position indicator (bv-2nax).
// Shows the current visible range in the format "[start-end of total]".
// Uses 1-indexed numbers for user-friendly display.
func (t *TreeModel) renderPositionIndicator(start, end int) string {
	total := len(t.flatList)
	// Convert to 1-indexed for display
	displayStart := start + 1
	displayEnd := end

	indicator := fmt.Sprintf(" [%d-%d of %d]", displayStart, displayEnd, total)
	return t.theme.Renderer.NewStyle().
		Foreground(t.theme.Muted).
		Render(indicator)
}

// renderEmptyState renders the view when there are no issues.
func (t *TreeModel) renderEmptyState() string {
	r := t.theme.Renderer

	titleStyle := r.NewStyle().
		Foreground(t.theme.Primary).
		Bold(true)

	mutedStyle := r.NewStyle().
		Foreground(t.theme.Muted)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Tree View"))
	sb.WriteString("\n\n")
	sb.WriteString(mutedStyle.Render("No issues to display."))
	sb.WriteString("\n\n")
	sb.WriteString(mutedStyle.Render("To create hierarchy, add parent-child dependencies:"))
	sb.WriteString("\n")
	sb.WriteString(mutedStyle.Render("  br dep add <child> parent-child:<parent>"))
	sb.WriteString("\n\n")
	sb.WriteString(mutedStyle.Render("Press E to return to list view."))

	return sb.String()
}

// renderNode renders a single tree node with tree characters and styling.
func (t *TreeModel) renderNode(node *IssueTreeNode, isSelected bool) string {
	if node == nil || node.Issue == nil {
		return ""
	}

	issue := node.Issue
	r := t.theme.Renderer
	var sb strings.Builder

	// Build the tree prefix (indentation + branch characters)
	prefix := t.buildTreePrefix(node)
	sb.WriteString(prefix)

	// Expand/collapse indicator
	indicator := t.getExpandIndicator(node)
	indicatorStyle := r.NewStyle().Foreground(t.theme.Secondary)
	sb.WriteString(indicatorStyle.Render(indicator))
	sb.WriteString(" ")

	// Type icon
	icon, iconColor := t.theme.GetTypeIcon(string(issue.IssueType))
	iconStyle := r.NewStyle().Foreground(iconColor)
	sb.WriteString(iconStyle.Render(icon))
	sb.WriteString(" ")

	// Priority badge (P0, P1, P2, etc.)
	prioText := fmt.Sprintf("P%d", issue.Priority)
	prioStyle := r.NewStyle().Bold(true)
	if issue.Priority <= 1 {
		prioStyle = prioStyle.Foreground(t.theme.Primary)
	} else {
		prioStyle = prioStyle.Foreground(t.theme.Muted)
	}
	sb.WriteString(prioStyle.Render(prioText))
	sb.WriteString(" ")

	// Issue ID
	idStyle := r.NewStyle().Foreground(t.theme.Highlight)
	sb.WriteString(idStyle.Render(issue.ID))
	sb.WriteString(" ")

	// Title (truncated if needed)
	title := issue.Title
	// Use lipgloss.Width for proper display width (handles ANSI codes + Unicode)
	maxTitleLen := t.width - lipgloss.Width(prefix) - 25 // Account for prefix, indicator, icon, priority, ID
	if maxTitleLen < 20 {
		maxTitleLen = 20
	}
	title = t.truncateTitle(title, maxTitleLen)

	// Title uses base style foreground
	sb.WriteString(title)

	// Status indicator (colored dot at end)
	statusColor := t.theme.GetStatusColor(string(issue.Status))
	statusDot := " " + GetStatusIcon(string(issue.Status))
	statusStyle := r.NewStyle().Foreground(statusColor)
	sb.WriteString(statusStyle.Render(statusDot))

	return sb.String()
}

// buildTreePrefix builds the indentation and branch characters for a node.
func (t *TreeModel) buildTreePrefix(node *IssueTreeNode) string {
	if node.Depth == 0 {
		return "" // Root nodes have no prefix
	}

	r := t.theme.Renderer
	treeStyle := r.NewStyle().Foreground(t.theme.Muted)

	var prefixParts []string

	// Walk up the tree to build prefix
	ancestors := t.getAncestors(node)

	// For each ancestor level, determine if we need a vertical line
	for i := 0; i < len(ancestors)-1; i++ {
		ancestor := ancestors[i]
		if t.hasSiblingsBelow(ancestor) {
			prefixParts = append(prefixParts, "│   ")
		} else {
			prefixParts = append(prefixParts, "    ")
		}
	}

	// Add the branch character for this node
	if t.isLastChild(node) {
		prefixParts = append(prefixParts, "└── ")
	} else {
		prefixParts = append(prefixParts, "├── ")
	}

	prefix := strings.Join(prefixParts, "")
	return treeStyle.Render(prefix)
}

// getAncestors returns the ancestors of a node from root to parent, with the node itself at the end.
// The last element is the node - used by buildTreePrefix which iterates to len-1.
func (t *TreeModel) getAncestors(node *IssueTreeNode) []*IssueTreeNode {
	var ancestors []*IssueTreeNode
	current := node.Parent
	for current != nil {
		ancestors = append([]*IssueTreeNode{current}, ancestors...)
		current = current.Parent
	}
	ancestors = append(ancestors, node) // Include the node at the end
	return ancestors
}

// hasSiblingsBelow checks if a node has siblings below it in the tree.
func (t *TreeModel) hasSiblingsBelow(node *IssueTreeNode) bool {
	if node.Parent == nil {
		// For root nodes, check if there are more roots after this one
		for i, root := range t.roots {
			if root == node {
				return i < len(t.roots)-1
			}
		}
		return false
	}

	// For non-root nodes, check siblings
	for i, sibling := range node.Parent.Children {
		if sibling == node {
			return i < len(node.Parent.Children)-1
		}
	}
	return false
}

// isLastChild checks if a node is the last child of its parent.
func (t *TreeModel) isLastChild(node *IssueTreeNode) bool {
	if node.Parent == nil {
		// For root nodes, check if it's the last root
		return len(t.roots) > 0 && t.roots[len(t.roots)-1] == node
	}

	parent := node.Parent
	return len(parent.Children) > 0 && parent.Children[len(parent.Children)-1] == node
}

// getExpandIndicator returns the expand/collapse indicator for a node.
func (t *TreeModel) getExpandIndicator(node *IssueTreeNode) string {
	if len(node.Children) == 0 {
		return "•" // Leaf node
	}
	if node.Expanded {
		return "▾" // Expanded
	}
	return "▸" // Collapsed
}

// truncateTitle truncates a title to the given max length with ellipsis.
func (t *TreeModel) truncateTitle(title string, maxLen int) string {
	if maxLen <= 3 {
		return "..."
	}

	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}

	return string(runes[:maxLen-1]) + "…"
}

// GetPriorityColor returns the color for a priority level.
func (t *TreeModel) GetPriorityColor(priority int) lipgloss.AdaptiveColor {
	switch priority {
	case 0:
		return t.theme.Primary // Critical - red/bright
	case 1:
		return t.theme.Highlight // High - highlighted
	case 2:
		return t.theme.Secondary // Medium - yellow
	default:
		return t.theme.Muted // Low/backlog - gray
	}
}

// SelectedIssue returns the currently selected issue, or nil if none.
func (t *TreeModel) SelectedIssue() *model.Issue {
	if t.cursor >= 0 && t.cursor < len(t.flatList) {
		if node := t.flatList[t.cursor]; node != nil {
			return node.Issue
		}
	}
	return nil
}

// SelectedNode returns the currently selected tree node, or nil if none.
func (t *TreeModel) SelectedNode() *IssueTreeNode {
	if t.cursor >= 0 && t.cursor < len(t.flatList) {
		return t.flatList[t.cursor]
	}
	return nil
}

// MoveDown moves the cursor down in the flat list.
func (t *TreeModel) MoveDown() {
	if t.cursor < len(t.flatList)-1 {
		t.cursor++
		t.ensureCursorVisible()
	}
}

// MoveUp moves the cursor up in the flat list.
func (t *TreeModel) MoveUp() {
	if t.cursor > 0 {
		t.cursor--
		t.ensureCursorVisible()
	}
}

// ToggleExpand expands or collapses the currently selected node.
func (t *TreeModel) ToggleExpand() {
	node := t.SelectedNode()
	if node != nil && len(node.Children) > 0 {
		node.Expanded = !node.Expanded
		t.rebuildFlatList()
		t.saveState() // Persist expand/collapse state (bv-19vz)
		t.ensureCursorVisible()
	}
}

// ExpandAll expands all nodes in the tree.
func (t *TreeModel) ExpandAll() {
	for _, root := range t.roots {
		t.setExpandedRecursive(root, true)
	}
	t.rebuildFlatList()
	t.saveState() // Persist expand/collapse state (bv-19vz)
	t.ensureCursorVisible()
}

// CollapseAll collapses all nodes in the tree.
func (t *TreeModel) CollapseAll() {
	for _, root := range t.roots {
		t.setExpandedRecursive(root, false)
	}
	t.rebuildFlatList()
	t.saveState() // Persist expand/collapse state (bv-19vz)
	t.ensureCursorVisible()
}

// JumpToTop moves cursor to the first node.
func (t *TreeModel) JumpToTop() {
	t.cursor = 0
	t.ensureCursorVisible()
}

// JumpToBottom moves cursor to the last node.
func (t *TreeModel) JumpToBottom() {
	if len(t.flatList) > 0 {
		t.cursor = len(t.flatList) - 1
		t.ensureCursorVisible()
	}
}

// JumpToParent moves cursor to the parent of the currently selected node.
// If already at a root node, does nothing.
func (t *TreeModel) JumpToParent() {
	node := t.SelectedNode()
	if node == nil || node.Parent == nil {
		return // No node selected or already at root
	}

	// Find parent in flatList
	for i, n := range t.flatList {
		if n == node.Parent {
			t.cursor = i
			t.ensureCursorVisible()
			return
		}
	}
}

// ExpandOrMoveToChild handles the → / l key:
// - If node has children and is collapsed: expand it
// - If node has children and is expanded: move to first child
// - If node is a leaf: do nothing
func (t *TreeModel) ExpandOrMoveToChild() {
	node := t.SelectedNode()
	if node == nil || len(node.Children) == 0 {
		return // No node selected or leaf node
	}

	if !node.Expanded {
		// Expand the node
		node.Expanded = true
		t.rebuildFlatList()
		t.saveState() // Persist expand/collapse state (bv-19vz)
		t.ensureCursorVisible()
	} else {
		// Move to first child
		// Find first child in flatList (should be right after current node)
		for i, n := range t.flatList {
			if n == node.Children[0] {
				t.cursor = i
				t.ensureCursorVisible()
				return
			}
		}
	}
}

// CollapseOrJumpToParent handles the ← / h key:
// - If node has children and is expanded: collapse it
// - If node is collapsed or is a leaf: jump to parent
func (t *TreeModel) CollapseOrJumpToParent() {
	node := t.SelectedNode()
	if node == nil {
		return
	}

	if len(node.Children) > 0 && node.Expanded {
		// Collapse the node
		node.Expanded = false
		t.rebuildFlatList()
		t.saveState() // Persist expand/collapse state (bv-19vz)
		t.ensureCursorVisible()
	} else {
		// Jump to parent (already calls ensureCursorVisible)
		t.JumpToParent()
	}
}

// PageDown moves cursor down by half a viewport.
func (t *TreeModel) PageDown() {
	pageSize := t.height / 2
	if pageSize < 1 {
		pageSize = 5
	}
	t.cursor += pageSize
	if t.cursor >= len(t.flatList) {
		t.cursor = len(t.flatList) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.ensureCursorVisible()
}

// PageUp moves cursor up by half a viewport.
func (t *TreeModel) PageUp() {
	pageSize := t.height / 2
	if pageSize < 1 {
		pageSize = 5
	}
	t.cursor -= pageSize
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.ensureCursorVisible()
}

// visibleRange returns the start and end indices of nodes to render (bv-r4ng).
// The range [start, end) covers nodes visible in the viewport.
// This is an O(1) calculation based on viewportOffset and height.
func (t *TreeModel) visibleRange() (start, end int) {
	if len(t.flatList) == 0 {
		return 0, 0
	}

	// Each node renders as 1 line
	visibleCount := t.height
	if visibleCount <= 0 {
		visibleCount = 20 // Default
	}

	// Start with the viewport offset, clamped to non-negative
	start = t.viewportOffset
	if start < 0 {
		start = 0
	}

	// Calculate end based on clamped start
	end = start + visibleCount

	// If end exceeds list, clamp it and adjust start to maximize visible items
	if end > len(t.flatList) {
		end = len(t.flatList)
		start = end - visibleCount
		if start < 0 {
			start = 0
		}
	}

	return start, end
}

// SelectByID moves cursor to the node with the given issue ID.
// Returns true if found, false otherwise.
// Useful for preserving cursor position after rebuild.
func (t *TreeModel) SelectByID(id string) bool {
	for i, node := range t.flatList {
		if node != nil && node.Issue != nil && node.Issue.ID == id {
			t.cursor = i
			return true
		}
	}
	return false
}

// GetSelectedID returns the ID of the currently selected issue, or empty string.
func (t *TreeModel) GetSelectedID() string {
	if issue := t.SelectedIssue(); issue != nil {
		return issue.ID
	}
	return ""
}

// setExpandedRecursive sets the expanded state for a node and all descendants.
func (t *TreeModel) setExpandedRecursive(node *IssueTreeNode, expanded bool) {
	if node == nil {
		return
	}
	node.Expanded = expanded
	for _, child := range node.Children {
		t.setExpandedRecursive(child, expanded)
	}
}

// rebuildFlatList rebuilds the flattened list of visible nodes.
func (t *TreeModel) rebuildFlatList() {
	t.flatList = t.flatList[:0]
	for _, root := range t.roots {
		t.appendVisible(root)
	}
	// Ensure cursor stays in bounds
	if t.cursor >= len(t.flatList) {
		t.cursor = len(t.flatList) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

// appendVisible adds a node and its visible descendants to flatList.
func (t *TreeModel) appendVisible(node *IssueTreeNode) {
	if node == nil {
		return
	}
	t.flatList = append(t.flatList, node)
	if node.Expanded {
		for _, child := range node.Children {
			t.appendVisible(child)
		}
	}
}

// IsBuilt returns whether the tree has been built.
func (t *TreeModel) IsBuilt() bool {
	return t.built
}

// NodeCount returns the total number of visible nodes.
func (t *TreeModel) NodeCount() int {
	return len(t.flatList)
}

// RootCount returns the number of root nodes.
func (t *TreeModel) RootCount() int {
	return len(t.roots)
}

// ensureCursorVisible adjusts viewportOffset so the cursor is visible (bv-lnc4).
// This method should be called after any cursor movement to maintain
// cursor-follows-viewport behavior. It implements cursor-at-edge scrolling:
// the viewport scrolls just enough to keep the cursor visible.
func (t *TreeModel) ensureCursorVisible() {
	if len(t.flatList) == 0 {
		return
	}

	visibleCount := t.height
	if visibleCount <= 0 {
		visibleCount = 20 // Default
	}

	// Cursor above viewport - scroll up to show cursor at top
	if t.cursor < t.viewportOffset {
		t.viewportOffset = t.cursor
	}

	// Cursor below viewport - scroll down to show cursor at bottom
	if t.cursor >= t.viewportOffset+visibleCount {
		t.viewportOffset = t.cursor - visibleCount + 1
	}

	// Clamp offset to valid range
	maxOffset := len(t.flatList) - visibleCount
	if maxOffset < 0 {
		maxOffset = 0
	}
	if t.viewportOffset > maxOffset {
		t.viewportOffset = maxOffset
	}
	if t.viewportOffset < 0 {
		t.viewportOffset = 0
	}
}

// GetViewportOffset returns the current viewport offset (for testing/debugging).
func (t *TreeModel) GetViewportOffset() int {
	return t.viewportOffset
}
