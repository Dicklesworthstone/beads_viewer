// tree.go - Hierarchical tree view for epic/task/subtask relationships (bv-gllx)
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/debug"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
// beads directory is desired. When empty, persistence is disabled so tree
// navigation never writes to an unconfigured working directory.
func (t *TreeModel) SetBeadsDir(dir string) {
	if t.beadsDir != dir {
		t.beadsDir = dir
		t.hierarchyExpanded = nil
	}
}

// SetReadOnly marks the tree model as read-only (e.g., during --as-of or
// time-travel sessions) so expand/collapse changes are never persisted to disk.
func (t *TreeModel) SetReadOnly(readOnly bool) {
	t.readOnly = readOnly
}

// IsReadOnly reports whether persistence is disabled for this tree model.
func (t *TreeModel) IsReadOnly() bool {
	return t.readOnly
}

// persistenceDisabled reports whether reading/writing tree-state.json should be skipped.
func (t *TreeModel) persistenceDisabled() bool {
	return savedConfigDisabled() || t.readOnly || t.beadsDir == ""
}

// collectExpandOverrides walks the current tree roots and records explicit
// non-default expand/collapse states (ignoring secondary SharedRef leaf copies).
func (t *TreeModel) collectExpandOverrides() map[string]bool {
	overrides := make(map[string]bool)
	var walk func(node *IssueTreeNode)
	walk = func(node *IssueTreeNode) {
		if node == nil || node.Issue == nil || node.SharedRef {
			return
		}
		defaultExpanded := node.Depth < 2
		if node.Expanded != defaultExpanded {
			overrides[node.Issue.ID] = node.Expanded
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range t.roots {
		walk(root)
	}
	return overrides
}

// captureActiveExpandState stores the active mode's explicit expand/collapse
// overrides into its dedicated per-mode map so Hierarchy and Blocking modes
// never clobber one another.
func (t *TreeModel) captureActiveExpandState() {
	if t.mode == TreeModeBlocking {
		t.blockingExpanded = t.collectExpandOverrides()
	} else {
		t.hierarchyExpanded = t.collectExpandOverrides()
	}
}

// resetNodesToDefaultExpansion resets all non-SharedRef nodes to the default
// depth-based expansion (depth < 2) before applying per-mode overrides.
func (t *TreeModel) resetNodesToDefaultExpansion() {
	var walk func(node *IssueTreeNode)
	walk = func(node *IssueTreeNode) {
		if node == nil || node.SharedRef {
			return
		}
		node.Expanded = node.Depth < 2
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range t.roots {
		walk(root)
	}
}

// saveState persists the current Hierarchy expand/collapse state to disk (bv-19vz).
// Only stores explicit user changes; nodes not in the map use default behavior.
// Strict no-op in TreeModeBlocking, when BV_NO_SAVED_CONFIG is set, when read-only,
// or when beadsDir is unset.
func (t *TreeModel) saveState() {
	if t.mode != TreeModeHierarchy {
		return
	}
	t.hierarchyExpanded = t.collectExpandOverrides()
	if t.persistenceDisabled() {
		return
	}

	state := &TreeState{
		Version:  TreeStateVersion,
		Expanded: make(map[string]bool, len(t.hierarchyExpanded)),
	}
	for id, exp := range t.hierarchyExpanded {
		state.Expanded[id] = exp
	}

	// Write to file
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		debug.Log("tree: failed to marshal tree state: %v", err)
		return
	}

	path := TreeStatePath(t.beadsDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		debug.Log("tree: failed to create state directory %s: %v", dir, err)
		return
	}

	if err := writeTreeStateFile(path, data); err != nil {
		debug.Log("tree: failed to write tree state to %s: %v", path, err)
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
	if err := tmpFile.Chmod(0o600); err != nil {
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

// loadState restores expand/collapse state for the active mode (bv-afcm).
// TreeModeBlocking uses its separate in-memory blockingExpanded map and never
// reads or mutates .beads/tree-state.json. TreeModeHierarchy uses in-memory
// hierarchyExpanded when present, or loads .beads/tree-state.json on first build.
func (t *TreeModel) loadState() {
	t.resetNodesToDefaultExpansion()

	if t.mode == TreeModeBlocking {
		if len(t.blockingExpanded) > 0 {
			t.applyState(&TreeState{Version: TreeStateVersion, Expanded: t.blockingExpanded})
		}
		return
	}

	if t.hierarchyExpanded != nil {
		t.applyState(&TreeState{Version: TreeStateVersion, Expanded: t.hierarchyExpanded})
		return
	}

	if savedConfigDisabled() || t.beadsDir == "" {
		return
	}

	path := TreeStatePath(t.beadsDir)
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist = first run, use defaults
		return
	}

	var state TreeState
	if err := json.Unmarshal(data, &state); err != nil {
		debug.Log("tree: invalid tree state file, using defaults: %v", err)
		return
	}
	if state.Version != TreeStateVersion {
		debug.Log("tree: unsupported tree state version %d, using defaults", state.Version)
		return
	}

	t.hierarchyExpanded = make(map[string]bool, len(state.Expanded))
	for id, exp := range state.Expanded {
		t.hierarchyExpanded[id] = exp
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
		if node, ok := t.issueMap[id]; ok && !node.SharedRef {
			node.Expanded = expanded
		}
		// If ID not found, it's stale - ignore
	}
}

// TreeViewMode determines what relationships are displayed
type TreeViewMode int

const (
	TreeModeHierarchy TreeViewMode = iota // parent-child deps (default)
	TreeModeBlocking                      // blocking deps
)

const (
	maxBlockingTreeDepth = 64
	maxBlockingTreeNodes = 10000
)

// IssueTreeNode represents a node in the hierarchical issue tree
type IssueTreeNode struct {
	Issue     *model.Issue     // Reference to the actual issue
	Children  []*IssueTreeNode // Child nodes
	Expanded  bool             // Is this node expanded?
	Depth     int              // Nesting level (0 = root)
	Parent    *IssueTreeNode   // Back-reference for navigation
	SharedRef bool             // True when attached as a non-reexpanded shared DAG target
}

// TreeModel manages the hierarchical tree view state
type TreeModel struct {
	roots          []*IssueTreeNode          // Root nodes (issues with no parent)
	flatList       []*IssueTreeNode          // Flattened visible nodes for navigation
	cursor         int                       // Current selection index in flatList
	theme          Theme                     // Visual styling
	mode           TreeViewMode              // Hierarchy vs blocking
	issueMap       map[string]*IssueTreeNode // Quick lookup by issue ID
	width          int                       // Available width
	height         int                       // Available height
	viewportOffset int                       // Index of first visible node (bv-r4ng)

	// Per-mode explicit expand/collapse state
	hierarchyExpanded map[string]bool
	blockingExpanded  map[string]bool

	// Build state
	built               bool          // Has tree been built?
	lastHash            string        // Hash of issues for cache invalidation
	issues              []model.Issue // Cached source issues for mode switching
	hasParentChildEdges bool          // True when ≥1 parent-child edge exists in issues
	hasBlockingEdges    bool          // True when ≥1 blocking edge exists in issues

	// Persistence state (bv-19vz)
	beadsDir string // Directory containing .beads (for tree-state.json)
	readOnly bool   // When true, saveState is a strict no-op (e.g., --as-of / time-travel)
}

// NewTreeModel creates an empty tree model
func NewTreeModel(theme Theme) TreeModel {
	return TreeModel{
		theme:    theme,
		mode:     TreeModeHierarchy,
		issueMap: make(map[string]*IssueTreeNode),
	}
}

// Mode returns the active tree relationship mode (Hierarchy vs Blocking).
func (t *TreeModel) Mode() TreeViewMode {
	return t.mode
}

// ModeName returns the human-readable name for the active tree mode.
func (t *TreeModel) ModeName() string {
	if t.mode == TreeModeBlocking {
		return "Blocking Chain"
	}
	return "Hierarchy"
}

// SetMode sets the active tree mode and rebuilds while preserving the selected issue ID
// and each mode's independent expand/collapse state.
func (t *TreeModel) SetMode(mode TreeViewMode) {
	if t.mode == mode {
		return
	}
	prevSelectedID := t.GetSelectedID()
	if t.built {
		t.captureActiveExpandState()
	}
	t.mode = mode
	t.lastHash = ""
	if t.built {
		issues := t.issues
		t.Build(issues)
		if prevSelectedID != "" {
			t.SelectByID(prevSelectedID)
		}
	}
}

// CycleMode toggles between TreeModeHierarchy and TreeModeBlocking, preserving selection.
func (t *TreeModel) CycleMode() {
	if t.mode == TreeModeHierarchy {
		t.SetMode(TreeModeBlocking)
	} else {
		t.SetMode(TreeModeHierarchy)
	}
}

func scanTreeEdgePresence(issues []model.Issue) (hasParentChild, hasBlocking bool) {
	for i := range issues {
		for _, dep := range issues[i].Dependencies {
			if dep == nil {
				continue
			}
			if dep.Type == model.DepParentChild {
				hasParentChild = true
			}
			if dep.Type.IsBlocking() {
				hasBlocking = true
			}
			if hasParentChild && hasBlocking {
				return true, true
			}
		}
	}
	return hasParentChild, hasBlocking
}

// buildBlockingTreeNodes constructs blocker -> dependent chains over blocking edges
// in strictly O(V + E) nodes by expanding each issue's downstream DAG once and
// attaching leaf SharedRef nodes on secondary incoming blocking edges.
func buildBlockingTreeNodes(issues []model.Issue) ([]*IssueTreeNode, map[string]*IssueTreeNode, bool) {
	t := TreeModel{
		mode:     TreeModeBlocking,
		issueMap: make(map[string]*IssueTreeNode),
	}
	if len(issues) == 0 {
		return nil, t.issueMap, false
	}

	issueByID := make(map[string]*model.Issue, len(issues))
	for i := range issues {
		issueByID[issues[i].ID] = &issues[i]
	}

	childrenOf := make(map[string][]*model.Issue)
	seenChildEdge := make(map[string]map[string]bool)
	hasInSetBlocker := make(map[string]bool)
	participates := make(map[string]bool)
	hasBlocking := false

	for i := range issues {
		issue := &issues[i]
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			hasBlocking = true
			participates[issue.ID] = true
			blockerID := dep.DependsOnID
			if _, ok := issueByID[blockerID]; ok {
				participates[blockerID] = true
				if seenChildEdge[blockerID] == nil {
					seenChildEdge[blockerID] = make(map[string]bool)
				}
				if !seenChildEdge[blockerID][issue.ID] {
					seenChildEdge[blockerID][issue.ID] = true
					childrenOf[blockerID] = append(childrenOf[blockerID], issue)
				}
				hasInSetBlocker[issue.ID] = true
			}
		}
	}

	if !hasBlocking {
		return nil, t.issueMap, false
	}

	sortBlockingIssues := func(list []*model.Issue) {
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if a == nil || b == nil {
				return a != nil
			}
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			return a.ID < b.ID
		})
	}

	var rootIssues []*model.Issue
	for i := range issues {
		issue := &issues[i]
		if participates[issue.ID] && !hasInSetBlocker[issue.ID] {
			rootIssues = append(rootIssues, issue)
		}
	}
	sortBlockingIssues(rootIssues)
	for id := range childrenOf {
		sortBlockingIssues(childrenOf[id])
	}

	// O(V + E) minimum-depth BFS pre-pass so shallow roots/branches always own the
	// primary expanded subtree and shared descendants are never hidden behind a
	// deeper collapsed path.
	claimed := make(map[string]bool, len(participates))
	primaryParent := make(map[string]string, len(participates))
	bfsDepth := make(map[string]int, len(participates))
	queue := make([]string, 0, len(participates))

	drainBFS := func() {
		for head := 0; head < len(queue); head++ {
			u := queue[head]
			if bfsDepth[u] >= maxBlockingTreeDepth {
				continue
			}
			for _, child := range childrenOf[u] {
				if child == nil || claimed[child.ID] {
					continue
				}
				claimed[child.ID] = true
				primaryParent[child.ID] = u
				bfsDepth[child.ID] = bfsDepth[u] + 1
				queue = append(queue, child.ID)
			}
		}
		queue = queue[:0]
	}

	for _, root := range rootIssues {
		if root == nil || claimed[root.ID] {
			continue
		}
		claimed[root.ID] = true
		primaryParent[root.ID] = ""
		bfsDepth[root.ID] = 0
		queue = append(queue, root.ID)
	}
	drainBFS()

	// Cycle fallback: if a connected component is a pure blocking cycle with no
	// unblocked root, deterministically walk blocking predecessors to the cycle,
	// pick the lowest-ID member as display root, and run BFS from it.
	var remaining []string
	for id := range participates {
		if !claimed[id] {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	var cycleRoots []*model.Issue
	for _, startID := range remaining {
		if claimed[startID] {
			continue
		}
		curr := startID
		order := []string{}
		pos := make(map[string]int)
		for {
			if idx, seen := pos[curr]; seen {
				minID := order[idx]
				for _, cid := range order[idx+1:] {
					if cid < minID {
						minID = cid
					}
				}
				curr = minID
				break
			}
			pos[curr] = len(order)
			order = append(order, curr)
			nextBlocker := ""
			found := false
			for _, dep := range issueByID[curr].Dependencies {
				if dep == nil || !dep.Type.IsBlocking() || issueByID[dep.DependsOnID] == nil {
					continue
				}
				if !found || dep.DependsOnID < nextBlocker {
					nextBlocker = dep.DependsOnID
					found = true
				}
			}
			if !found {
				break
			}
			curr = nextBlocker
		}
		seedID := curr
		if claimed[seedID] {
			seedID = startID
		}
		if issueByID[seedID] != nil && !claimed[seedID] {
			claimed[seedID] = true
			primaryParent[seedID] = ""
			bfsDepth[seedID] = 0
			cycleRoots = append(cycleRoots, issueByID[seedID])
			queue = append(queue, seedID)
			drainBFS()
		}
	}
	sortBlockingIssues(cycleRoots)

	visited := make(map[string]bool)
	primaryBuilt := make(map[string]bool, len(issues))
	nodeCount := 0

	var buildDAGNode func(issue *model.Issue, depth int, parent *IssueTreeNode) *IssueTreeNode
	buildDAGNode = func(issue *model.Issue, depth int, parent *IssueTreeNode) *IssueTreeNode {
		if issue == nil || nodeCount >= maxBlockingTreeNodes {
			return nil
		}
		parentID := ""
		if parent != nil && parent.Issue != nil {
			parentID = parent.Issue.ID
		}
		isPrimary := !primaryBuilt[issue.ID] && primaryParent[issue.ID] == parentID
		if visited[issue.ID] || !isPrimary {
			nodeCount++
			return &IssueTreeNode{
				Issue:     issue,
				Depth:     depth,
				Parent:    parent,
				Expanded:  false,
				SharedRef: true,
			}
		}

		primaryBuilt[issue.ID] = true
		nodeCount++
		node := &IssueTreeNode{
			Issue:    issue,
			Depth:    depth,
			Parent:   parent,
			Expanded: depth < 2,
		}
		t.issueMap[issue.ID] = node
		if depth >= maxBlockingTreeDepth {
			return node
		}

		visited[issue.ID] = true
		defer func() { visited[issue.ID] = false }()

		for _, child := range childrenOf[issue.ID] {
			if nodeCount >= maxBlockingTreeNodes {
				break
			}
			if childNode := buildDAGNode(child, depth+1, node); childNode != nil {
				node.Children = append(node.Children, childNode)
			}
		}
		t.sortNodes(node.Children)
		return node
	}

	for _, issue := range rootIssues {
		if nodeCount >= maxBlockingTreeNodes {
			break
		}
		if node := buildDAGNode(issue, 0, nil); node != nil {
			t.roots = append(t.roots, node)
		}
	}
	for _, issue := range cycleRoots {
		if nodeCount >= maxBlockingTreeNodes {
			break
		}
		if node := buildDAGNode(issue, 0, nil); node != nil {
			t.roots = append(t.roots, node)
		}
	}

	t.sortNodes(t.roots)
	return t.roots, t.issueMap, true
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
	t.ensureCursorVisible()
}

// Build constructs the tree from issues using the active mode (Hierarchy or Blocking).
// Implementation for bv-j3ck.
func (t *TreeModel) Build(issues []model.Issue) {
	// Reset state
	t.roots = nil
	t.flatList = nil
	t.issueMap = make(map[string]*IssueTreeNode)
	t.cursor = 0
	t.viewportOffset = 0
	t.lastHash = ""
	t.issues = issues
	t.hasParentChildEdges, t.hasBlockingEdges = scanTreeEdgePresence(issues)

	if len(issues) == 0 {
		t.built = true
		return
	}

	if t.mode == TreeModeBlocking {
		roots, nodeMap, _ := buildBlockingTreeNodes(issues)
		t.roots = roots
		t.issueMap = nodeMap
	} else {
		roots, nodeMap := buildIssueTreeNodes(issues)
		t.roots = roots
		t.issueMap = nodeMap
	}

	// Load persisted state (bv-afcm) before building the flat list.
	t.loadState()
	t.rebuildFlatList()
	t.built = true
}

// BuildFromIssues rebuilds the tree from an explicit issue slice (e.g. repo-scoped
// issues in workspace mode) while preserving the currently selected issue when present.
func (t *TreeModel) BuildFromIssues(issues []model.Issue) {
	prevSelectedID := ""
	if issue := t.SelectedIssue(); issue != nil {
		prevSelectedID = issue.ID
	}
	t.Build(issues)
	if prevSelectedID != "" {
		t.SelectByID(prevSelectedID)
	}
}

// BuildFromSnapshot wires the tree view to precomputed tree data from a DataSnapshot.
// This avoids building the parent/child structure on the UI thread when the snapshot
// already contains it (bv-t435).
func (t *TreeModel) BuildFromSnapshot(snapshot *DataSnapshot) {
	if snapshot == nil {
		return
	}

	snapshotHash := ""
	if snapshot.DataHash != "" {
		snapshotHash = fmt.Sprintf("%s:mode=%d", snapshot.DataHash, t.mode)
	}

	// Skip work if we're already built for this snapshot and mode.
	if t.built && snapshotHash != "" && t.lastHash == snapshotHash {
		return
	}

	// Preserve current selection (best-effort) by issue ID.
	prevSelectedID := ""
	if issue := t.SelectedIssue(); issue != nil {
		prevSelectedID = issue.ID
	}

	t.issues = snapshot.Issues
	t.hasParentChildEdges, t.hasBlockingEdges = scanTreeEdgePresence(snapshot.Issues)

	// Snapshot precomputes Hierarchy roots only; Blocking mode builds from snapshot.Issues.
	if t.mode == TreeModeBlocking {
		t.Build(snapshot.Issues)
		t.lastHash = snapshotHash
		if prevSelectedID != "" {
			t.SelectByID(prevSelectedID)
		}
		return
	}

	// Deep-copy precomputed hierarchy roots and rebuild t.issueMap over the copied
	// nodes so ToggleExpand/ExpandAll/CollapseAll never mutate shared DataSnapshot.TreeRoots.
	t.roots, t.issueMap = deepCopyTree(snapshot.TreeRoots, snapshot.TreeNodeMap, snapshot.IssueMap)
	if len(t.roots) > 0 && len(t.issueMap) == 0 {
		t.issueMap = make(map[string]*IssueTreeNode)
		var indexCopied func([]*IssueTreeNode)
		indexCopied = func(nodes []*IssueTreeNode) {
			for _, n := range nodes {
				if n == nil {
					continue
				}
				if n.Issue != nil && !n.SharedRef {
					if _, exists := t.issueMap[n.Issue.ID]; !exists {
						t.issueMap[n.Issue.ID] = n
					}
				}
				indexCopied(n.Children)
			}
		}
		indexCopied(t.roots)
	}

	// If the snapshot didn't include tree data, fall back to building it now.
	if len(t.roots) == 0 || len(t.issueMap) == 0 {
		t.Build(snapshot.Issues)
		t.lastHash = snapshotHash
		if prevSelectedID != "" {
			t.SelectByID(prevSelectedID)
		}
		return
	}

	// Apply persisted expand/collapse state and rebuild visible list.
	t.loadState()
	t.rebuildFlatList()
	t.built = true
	t.lastHash = snapshotHash

	// Restore selection if possible.
	if prevSelectedID != "" {
		t.SelectByID(prevSelectedID)
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

		// 3. CreatedAt (oldest first for stable ordering), then ID tiebreaker
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
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

// treeViewportLayout returns (headerLines, visibleNodeCount, showFooter) so that
// View() never exceeds t.height lines (accounting for both the mode header line
// and the [start-end of total] scroll indicator footer).
func (t *TreeModel) treeViewportLayout() (headerLines, visibleCount int, showFooter bool) {
	totalHeight := t.height
	if totalHeight <= 0 {
		totalHeight = 20
	}
	if totalHeight == 1 {
		return 0, 1, false
	}
	headerLines = 1
	availAfterHeader := totalHeight - headerLines
	if len(t.flatList) <= availAfterHeader {
		return headerLines, availAfterHeader, false
	}
	if availAfterHeader >= 2 {
		return headerLines, availAfterHeader - 1, true
	}
	return headerLines, 1, false
}

func (t *TreeModel) renderHeader() string {
	r := t.theme.Renderer
	badgeStyle := r.NewStyle().Foreground(t.theme.Primary).Bold(true)
	hintStyle := r.NewStyle().Foreground(t.theme.Muted)

	var header string
	if t.mode == TreeModeBlocking {
		header = badgeStyle.Render("Tree [Blocking Chain]") + hintStyle.Render(" — b/m: Hierarchy mode")
	} else {
		hint := " — b/m: Blocking Tree mode"
		if !t.hasParentChildEdges && len(t.issues) > 0 {
			hint = " — no parent-child deps (Press b/m for Blocking Tree mode)"
		}
		header = badgeStyle.Render("Tree [Hierarchy]") + hintStyle.Render(hint)
	}
	if t.width > 0 {
		header = ansi.Truncate(header, t.width, "…")
	}
	return header
}

// View renders the tree view.
// Implementation for bv-1371, updated for windowed rendering (bv-db02).
// Only renders visible nodes based on viewportOffset and height for O(viewport)
// performance instead of O(n) where n is total nodes.
func (t *TreeModel) View() string {
	if !t.built || len(t.flatList) == 0 {
		return t.renderEmptyState()
	}

	headerLines, _, showFooter := t.treeViewportLayout()
	start, end := t.visibleRange()

	lines := make([]string, 0, headerLines+(end-start)+1)
	if headerLines > 0 {
		lines = append(lines, t.renderHeader())
	}

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
		if t.width > 0 && lipgloss.Width(line) > t.width {
			tail := "…"
			if t.width <= 1 {
				tail = ""
			}
			line = ansi.Truncate(line, t.width, tail)
		}

		lines = append(lines, line)
	}

	// Add position indicator if scrolling is needed (bv-2nax)
	if showFooter {
		lines = append(lines, t.renderPositionIndicator(start, end))
	}

	return strings.Join(lines, "\n")
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
	rendered := t.theme.Renderer.NewStyle().
		Foreground(t.theme.Muted).
		Render(indicator)
	if t.width > 0 && lipgloss.Width(rendered) > t.width {
		tail := "…"
		if t.width <= 1 {
			tail = ""
		}
		rendered = ansi.Truncate(rendered, t.width, tail)
	}
	return rendered
}

// renderEmptyState renders the view when there are no issues (or no blocking edges in Blocking mode).
func (t *TreeModel) renderEmptyState() string {
	r := t.theme.Renderer

	titleStyle := r.NewStyle().
		Foreground(t.theme.Primary).
		Bold(true)

	mutedStyle := r.NewStyle().
		Foreground(t.theme.Muted)

	var lines []string
	if t.mode == TreeModeBlocking && len(t.issues) > 0 {
		lines = []string{
			titleStyle.Render("Tree View [Blocking Chain]"),
			"",
			mutedStyle.Render("No blocking dependencies found in the current issue set."),
			mutedStyle.Render("Press b or m to switch to Hierarchy mode."),
			"",
			mutedStyle.Render("Press E to return to list view."),
		}
	} else {
		lines = []string{
			titleStyle.Render("Tree View [Hierarchy]"),
			"",
			mutedStyle.Render("No issues to display."),
			"",
			mutedStyle.Render("To create hierarchy, add parent-child dependencies:"),
			mutedStyle.Render("  br dep add <child> parent-child:<parent>"),
			mutedStyle.Render("Press b/m for Blocking Tree mode. Press E to return to list view."),
		}
	}
	if t.width > 0 {
		tail := "…"
		if t.width <= 1 {
			tail = ""
		}
		for i := range lines {
			if lipgloss.Width(lines[i]) > t.width {
				lines[i] = ansi.Truncate(lines[i], t.width, tail)
			}
		}
	}
	if t.height > 0 && len(lines) > t.height {
		lines = lines[:t.height]
	}
	return strings.Join(lines, "\n")
}

// renderNode renders a single tree node with tree characters and styling.
func (t *TreeModel) renderNode(node *IssueTreeNode, isSelected bool) string {
	if node == nil || node.Issue == nil {
		return ""
	}

	issue := node.Issue
	r := t.theme.Renderer
	var leftBuilder strings.Builder

	// Build the tree prefix (indentation + branch characters for node.Depth)
	prefix := t.buildTreePrefix(node)
	leftBuilder.WriteString(prefix)

	// Expand/collapse indicator
	indicator := t.getExpandIndicator(node)
	indicatorStyle := r.NewStyle().Foreground(t.theme.Secondary)
	leftBuilder.WriteString(indicatorStyle.Render(indicator))
	leftBuilder.WriteString(" ")

	// Type icon
	icon, iconColor := t.theme.GetTypeIcon(string(issue.IssueType))
	iconStyle := r.NewStyle().Foreground(iconColor)
	leftBuilder.WriteString(iconStyle.Render(icon))
	leftBuilder.WriteString(" ")

	// Priority badge (P0, P1, P2, etc.)
	prioText := fmt.Sprintf("P%d", issue.Priority)
	prioStyle := r.NewStyle().Bold(true)
	if issue.Priority <= 1 {
		prioStyle = prioStyle.Foreground(t.theme.Primary)
	} else {
		prioStyle = prioStyle.Foreground(t.theme.Muted)
	}
	leftBuilder.WriteString(prioStyle.Render(prioText))
	leftBuilder.WriteString(" ")

	// Issue ID (single-line sanitized)
	cleanID := strings.ReplaceAll(strings.ReplaceAll(issue.ID, "\r", ""), "\n", "")
	idStyle := r.NewStyle().Foreground(t.theme.Highlight)
	leftBuilder.WriteString(idStyle.Render(cleanID))
	leftBuilder.WriteString(" ")

	leftPart := leftBuilder.String()

	// Status indicator (colored dot at end)
	statusColor := t.theme.GetStatusColor(string(issue.Status))
	statusDot := " " + GetStatusIcon(string(issue.Status))
	statusStyle := r.NewStyle().Foreground(statusColor)
	rightPart := statusStyle.Render(statusDot)

	// Compute cell-width budget from actual rendered node chrome (using node.Depth
	// prefix width + selected row border/padding chrome, with zero minimum floor).
	title := issue.Title
	if t.width > 0 {
		selectedChrome := 0
		if isSelected {
			selectedChrome = lipgloss.Width(t.theme.Selected.Render(""))
		}
		availRowWidth := t.width - selectedChrome
		if availRowWidth <= 0 {
			return ""
		}
		leftWidth := lipgloss.Width(leftPart)
		rightWidth := lipgloss.Width(rightPart)
		maxTitleLen := availRowWidth - leftWidth - rightWidth
		if maxTitleLen > 0 {
			title = t.truncateTitle(title, maxTitleLen)
		} else {
			title = ""
		}
		line := leftPart + title + rightPart
		if lipgloss.Width(line) > availRowWidth {
			tail := "…"
			if availRowWidth <= 1 {
				tail = ""
			}
			line = ansi.Truncate(line, availRowWidth, tail)
		}
		return line
	}

	title = t.truncateTitle(title, 80)
	return leftPart + title + rightPart
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
		if node.SharedRef {
			return "↪" // Shared DAG node already expanded elsewhere
		}
		return "•" // Leaf node
	}
	if node.Expanded {
		return "▾" // Expanded
	}
	return "▸" // Collapsed
}

// truncateTitle truncates a title to the given max cell width with ellipsis.
func (t *TreeModel) truncateTitle(title string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	clean := strings.ReplaceAll(strings.ReplaceAll(title, "\r", " "), "\n", " ")
	if maxLen <= 3 {
		return strings.Repeat(".", maxLen)
	}
	if lipgloss.Width(clean) <= maxLen {
		return clean
	}
	return ansi.Truncate(clean, maxLen, "…")
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

func (t *TreeModel) persistState() {
	t.captureActiveExpandState()
	t.saveState()
}

// jumpToPrimary reveals the primary (non-SharedRef) tree node for id by expanding
// any collapsed ancestors, rebuilding flatList, and focusing it.
func (t *TreeModel) jumpToPrimary(id string) bool {
	if id == "" || t.issueMap == nil {
		return false
	}
	target, ok := t.issueMap[id]
	if !ok || target == nil {
		return false
	}
	for p := target.Parent; p != nil; p = p.Parent {
		p.Expanded = true
	}
	t.rebuildFlatList()
	for i, n := range t.flatList {
		if n == target || (n != nil && !n.SharedRef && n.Issue != nil && n.Issue.ID == id) {
			t.cursor = i
			t.ensureCursorVisible()
			t.persistState()
			return true
		}
	}
	return false
}

// ToggleExpand expands or collapses the currently selected node, or jumps to the
// primary subtree occurrence when invoked on a SharedRef ("↪") node.
func (t *TreeModel) ToggleExpand() {
	node := t.SelectedNode()
	if node == nil {
		return
	}
	if node.SharedRef && node.Issue != nil {
		t.jumpToPrimary(node.Issue.ID)
		return
	}
	if len(node.Children) > 0 {
		node.Expanded = !node.Expanded
		t.persistState() // Persist expand/collapse state (bv-19vz)
		t.rebuildFlatList()
		t.ensureCursorVisible()
	}
}

// ExpandAll expands all nodes in the tree.
func (t *TreeModel) ExpandAll() {
	for _, root := range t.roots {
		t.setExpandedRecursive(root, true)
	}
	t.captureActiveExpandState()
	t.rebuildFlatList()
	t.saveState() // Persist expand/collapse state (bv-19vz)
	t.ensureCursorVisible()
}

// CollapseAll collapses all nodes in the tree.
func (t *TreeModel) CollapseAll() {
	for _, root := range t.roots {
		t.setExpandedRecursive(root, false)
	}
	t.captureActiveExpandState()
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
// - If node is a SharedRef ("↪"): jump to its primary subtree occurrence
// - If node has children and is collapsed: expand it
// - If node has children and is expanded: move to first child
// - If node is a leaf: do nothing
func (t *TreeModel) ExpandOrMoveToChild() {
	node := t.SelectedNode()
	if node == nil {
		return
	}
	if node.SharedRef && node.Issue != nil {
		t.jumpToPrimary(node.Issue.ID)
		return
	}
	if len(node.Children) == 0 {
		return // Leaf node
	}

	if !node.Expanded {
		// Expand the node
		node.Expanded = true
		t.captureActiveExpandState()
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
		t.captureActiveExpandState()
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
// The range [start, end) covers nodes visible in the viewport after reserving
// header and optional scroll-indicator footer rows so View() never exceeds t.height.
// This is an O(1) calculation based on viewportOffset and height.
func (t *TreeModel) visibleRange() (start, end int) {
	if len(t.flatList) == 0 {
		return 0, 0
	}

	_, visibleCount, _ := t.treeViewportLayout()

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

// SelectByID moves cursor to the node with the given issue ID and scrolls it into view.
// If the target is hidden under a collapsed ancestor, it expands ancestors to reveal it.
// Returns true if found, false otherwise.
func (t *TreeModel) SelectByID(id string) bool {
	for i, node := range t.flatList {
		if node != nil && !node.SharedRef && node.Issue != nil && node.Issue.ID == id {
			t.cursor = i
			t.ensureCursorVisible()
			return true
		}
	}
	if t.jumpToPrimary(id) {
		return true
	}
	for i, node := range t.flatList {
		if node != nil && node.Issue != nil && node.Issue.ID == id {
			t.cursor = i
			t.ensureCursorVisible()
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

	_, visibleCount, _ := t.treeViewportLayout()

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
