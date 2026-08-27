// Package ui provides the history view for displaying bead-to-commit correlations.
package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// historyFocus tracks which pane has focus in the history view
type historyFocus int

const (
	historyFocusList     historyFocus = iota // Left pane (beads or commits)
	historyFocusTimeline                     // Timeline pane for 4-pane layout
	historyFocusMiddle                       // Middle pane for 3/4-pane layout (bv-xrfh)
	historyFocusDetail                       // Right pane (details)
)

// historyLayout tracks the responsive layout mode (bv-xrfh)
type historyLayout int

const (
	layoutNarrow   historyLayout = iota // < 100 cols: two-pane optimized
	layoutStandard                      // 100-150 cols: three-pane standard
	layoutWide                          // > 150 cols: three-pane with timeline
)

// Layout breakpoints (bv-xrfh)
const (
	layoutBreakpointStandard = 100 // Width to switch to 3-pane
	layoutBreakpointWide     = 150 // Width to switch to wide mode
)

// historyViewMode tracks bead-centric vs git-centric view (bv-tl3n)
type historyViewMode int

const (
	historyModeBead historyViewMode = iota // Default: beads on left, commits for selected bead
	historyModeGit                         // Git mode: commits on left, related beads for selected commit
)

// CommitListEntry represents a commit in git-centric mode (bv-tl3n)
type CommitListEntry struct {
	SHA       string
	ShortSHA  string
	Message   string
	Author    string
	Timestamp string
	FileCount int
	BeadIDs   []string // Beads related to this commit

	// sortTimestamp retains the complete commit instant. Timestamp is formatted
	// only to minute precision for display, so sorting on it can reverse commits
	// from the same minute when the SHA tie-breaker happens to disagree with time.
	sortTimestamp time.Time
}

// historySearchMode tracks what type of search is active (bv-nkrj)
type historySearchMode int

const (
	searchModeOff    historySearchMode = iota // No search active
	searchModeAll                             // Search across all fields
	searchModeCommit                          // Search commit messages only
	searchModeSHA                             // Search by SHA prefix
	searchModeBead                            // Search bead ID/title
	searchModeAuthor                          // Search by author
)

// TimelineEntryType categorizes timeline entries (bv-1x6o)
type timelineEntryType int

const (
	timelineEntryEvent   timelineEntryType = iota // Lifecycle event (created, claimed, closed)
	timelineEntryCommit                           // Code commit
	timelineEntrySession                          // Cass coding session (bv-pr1l)
)

// TimelineEntry represents a single entry in the timeline visualization (bv-1x6o)
type TimelineEntry struct {
	Timestamp  time.Time
	EntryType  timelineEntryType
	Label      string  // Event type name or commit SHA
	Detail     string  // Full message or event detail
	Confidence float64 // For commits: correlation confidence (0-1)
	EventType  string  // For events: "created", "claimed", "closed", etc.

	// Session fields (bv-pr1l)
	SessionAgent        string  // For sessions: "claude", "cursor", etc.
	SessionMessageCount int     // For sessions: number of messages in session
	SessionPath         string  // For sessions: path to session file
	SessionScore        float64 // For sessions: correlation score
}

// FileTreeNode represents a node in the file tree (bv-190l)
type FileTreeNode struct {
	Name        string          // File or directory name
	Path        string          // Full path
	IsDir       bool            // True if directory
	Children    []*FileTreeNode // Child nodes (for directories)
	ChangeCount int             // Number of commits touching this path
	Expanded    bool            // True if directory is expanded
	Level       int             // Nesting depth for indentation
}

// HistoryModel represents the TUI view for bead history and code correlations
type HistoryModel struct {
	// Data
	report    *correlation.HistoryReport
	histories []correlation.BeadHistory // Filtered and sorted list
	beadIDs   []string                  // Sorted bead IDs for navigation

	// Navigation state
	selectedBead   int // Index into beadIDs
	selectedCommit int // Index into selected bead's commits
	scrollOffset   int // For scrolling the bead list
	focused        historyFocus

	// Git-centric mode state (bv-tl3n)
	viewMode            historyViewMode
	commitList          []CommitListEntry // All commits sorted by recency
	selectedGitCommit   int               // Index into commitList
	selectedRelatedBead int               // Index into selected commit's BeadIDs
	gitScrollOffset     int               // For scrolling the commit list

	// Three-pane middle panel scroll state (bv-xrfh)
	middleScrollOffset int // Scroll offset for middle pane content

	// Timeline panel state (bv-1x6o)
	timelineScrollOffset int // Scroll offset for timeline panel

	// Filters
	authorFilter  string  // Filter by author (empty = all)
	minConfidence float64 // Minimum confidence threshold (0-1)

	// Search state (bv-nkrj)
	searchInput     textinput.Model   // Text input for search query
	searchMode      historySearchMode // Current search mode
	searchActive    bool              // Whether search input is focused
	lastSearchQuery string            // Cache for detecting query changes
	filteredCommits []CommitListEntry // Filtered commit list for git mode

	// Display state
	width  int
	height int
	theme  Theme

	// Expanded state tracking
	expandedBeads map[string]bool // Track which beads have commits expanded

	// File tree state (bv-190l)
	showFileTree    bool            // Whether file tree panel is visible
	fileTree        []*FileTreeNode // Root-level nodes of the file tree
	flatFileList    []*FileTreeNode // Flattened visible nodes for navigation
	selectedFileIdx int             // Index in flatFileList
	fileTreeScroll  int             // Scroll offset for file tree
	fileFilter      string          // Current file filter (empty = no filter)
	fileTreeFocus   bool            // True when file tree has focus

	// Cass session integration state (bv-pr1l)
	sessionCache map[string][]cass.ScoredResult // Cached sessions per bead ID

	// View mode transition state (bv-kvlx)
	modeChangedAt time.Time // Timestamp of last mode toggle for transition animation
}

// NewHistoryModel creates a new history view from a correlation report
func NewHistoryModel(report *correlation.HistoryReport, theme Theme) HistoryModel {
	// Initialize search input (bv-nkrj)
	ti := textinput.New()
	ti.Placeholder = "Search commits, beads, authors..."
	ti.CharLimit = 100
	ti.Width = 40

	h := HistoryModel{
		report:        report,
		theme:         theme,
		focused:       historyFocusList,
		minConfidence: 0.0, // Show all by default
		expandedBeads: make(map[string]bool),
		searchInput:   ti,
		searchMode:    searchModeOff,
		sessionCache:  make(map[string][]cass.ScoredResult), // bv-pr1l
	}
	h.rebuildFilteredList()
	return h
}

// SetReport updates the history data
func (h *HistoryModel) SetReport(report *correlation.HistoryReport) {
	selectedBeadID := h.SelectedBeadID()
	selectedBeadCommitSHA := ""
	if selected := h.SelectedCommit(); selected != nil {
		selectedBeadCommitSHA = selected.SHA
	}
	selectedGitSHA := ""
	selectedRelatedBead := ""
	if selected := h.SelectedGitCommit(); selected != nil {
		selectedGitSHA = selected.SHA
		selectedRelatedBead = h.SelectedRelatedBeadID()
	}
	expandedPaths := make(map[string]bool)
	var rememberExpansion func([]*FileTreeNode)
	rememberExpansion = func(nodes []*FileTreeNode) {
		for _, node := range nodes {
			if node.Expanded {
				expandedPaths[node.Path] = true
			}
			rememberExpansion(node.Children)
		}
	}
	rememberExpansion(h.fileTree)
	hadFileTree := h.fileTree != nil
	selectedFilePath := ""
	if h.selectedFileIdx >= 0 && h.selectedFileIdx < len(h.flatFileList) {
		selectedFilePath = h.flatFileList[h.selectedFileIdx].Path
	}

	h.report = report
	// Keep the user's query, search focus, filters, and view mode while applying
	// the refreshed report. Rebuilding only the base bead list would silently
	// drop an active query and leave git-mode results tied to the old report.
	h.applySearchFilter()
	if h.viewMode == historyModeBead {
		h.restoreBeadSelection(selectedBeadID, selectedBeadCommitSHA)
	} else {
		h.restoreGitSelection(selectedGitSHA, selectedRelatedBead)
	}
	// Timeline rows are derived from the whole report and do not have a stable
	// row identity across refreshes. Reset rather than retaining an invalid old
	// offset that can display impossible percentages or require many keypresses.
	h.timelineScrollOffset = 0
	if h.showFileTree || hadFileTree {
		h.buildFileTree()
		var restoreExpansion func([]*FileTreeNode)
		restoreExpansion = func(nodes []*FileTreeNode) {
			for _, node := range nodes {
				node.Expanded = expandedPaths[node.Path]
				restoreExpansion(node.Children)
			}
		}
		restoreExpansion(h.fileTree)
		h.rebuildFlatFileList()
		h.selectedFileIdx = 0
		for i, node := range h.flatFileList {
			if node.Path == selectedFilePath {
				h.selectedFileIdx = i
				break
			}
		}
		if len(h.flatFileList) == 0 {
			h.fileTreeScroll = 0
		} else if h.fileTreeScroll >= len(h.flatFileList) {
			h.fileTreeScroll = len(h.flatFileList) - 1
		}
	}
}

func (h *HistoryModel) restoreBeadSelection(beadID, commitSHA string) {
	h.selectedBead = 0
	h.selectedCommit = 0
	for i, candidateID := range h.beadIDs {
		if candidateID != beadID {
			continue
		}
		h.selectedBead = i
		for j := range h.histories[i].Commits {
			if h.histories[i].Commits[j].SHA == commitSHA {
				h.selectedCommit = j
				break
			}
		}
		break
	}
	h.ensureBeadVisible()
	if h.selectedBead >= 0 && h.selectedBead < len(h.histories) {
		h.ensureMiddleScrollVisible(h.selectedCommit, len(h.histories[h.selectedBead].Commits))
	} else {
		h.middleScrollOffset = 0
	}
}

func (h *HistoryModel) restoreGitSelection(commitSHA, relatedBeadID string) {
	commits := h.GetFilteredCommitList()
	h.selectedGitCommit = 0
	h.selectedRelatedBead = 0
	for i := range commits {
		if commits[i].SHA != commitSHA {
			continue
		}
		h.selectedGitCommit = i
		for j, beadID := range commits[i].BeadIDs {
			if beadID == relatedBeadID {
				h.selectedRelatedBead = j
				break
			}
		}
		break
	}
	h.ensureGitCommitVisible()
	if h.selectedGitCommit >= 0 && h.selectedGitCommit < len(commits) {
		h.ensureMiddleScrollVisible(h.selectedRelatedBead, len(commits[h.selectedGitCommit].BeadIDs))
	} else {
		h.middleScrollOffset = 0
	}
}

// SetSessionsForBead stores correlated sessions for a bead in the cache (bv-pr1l)
// This is called when sessions are loaded asynchronously from the main model.
func (h *HistoryModel) SetSessionsForBead(beadID string, sessions []cass.ScoredResult) {
	if h.sessionCache == nil {
		h.sessionCache = make(map[string][]cass.ScoredResult)
	}
	h.sessionCache[beadID] = sessions
}

// HasSessionsForBead returns true if sessions are cached for the given bead (bv-pr1l)
func (h *HistoryModel) HasSessionsForBead(beadID string) bool {
	if h.sessionCache == nil {
		return false
	}
	_, ok := h.sessionCache[beadID]
	return ok
}

// GetSessionsForBead returns cached sessions for a bead (bv-pr1l)
func (h *HistoryModel) GetSessionsForBead(beadID string) []cass.ScoredResult {
	if h.sessionCache == nil {
		return nil
	}
	return h.sessionCache[beadID]
}

// ClearSessionCache clears all cached sessions (bv-pr1l)
func (h *HistoryModel) ClearSessionCache() {
	h.sessionCache = make(map[string][]cass.ScoredResult)
}

// rebuildFilteredList rebuilds the filtered and sorted list of histories
func (h *HistoryModel) rebuildFilteredList() {
	// Capture current selection
	var selectedID string
	if h.selectedBead >= 0 && h.selectedBead < len(h.beadIDs) {
		selectedID = h.beadIDs[h.selectedBead]
	}

	h.histories = nil
	h.beadIDs = nil

	if h.report == nil {
		return
	}

	// Filter and collect histories
	for beadID, history := range h.report.Histories {
		// Skip beads with no commits
		if len(history.Commits) == 0 {
			continue
		}

		// Apply author filter
		if h.authorFilter != "" {
			var filtered []correlation.CorrelatedCommit
			for _, c := range history.Commits {
				if strings.Contains(strings.ToLower(c.Author), strings.ToLower(h.authorFilter)) ||
					strings.Contains(strings.ToLower(c.AuthorEmail), strings.ToLower(h.authorFilter)) {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) == 0 {
				continue
			}
			history.Commits = filtered
		}

		// Apply confidence filter - keep only commits meeting threshold
		if h.minConfidence > 0 {
			var filtered []correlation.CorrelatedCommit
			for _, c := range history.Commits {
				if c.Confidence >= h.minConfidence {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) == 0 {
				continue
			}
			history.Commits = filtered
		}

		// Apply file filter (bv-190l) - keep only commits touching the filtered path
		if h.fileFilter != "" {
			var filtered []correlation.CorrelatedCommit
			for _, c := range history.Commits {
				for _, file := range c.Files {
					// Match if file path equals filter or starts with filter (directory match)
					if file.Path == h.fileFilter || strings.HasPrefix(file.Path, h.fileFilter+"/") {
						filtered = append(filtered, c)
						break
					}
				}
			}
			if len(filtered) == 0 {
				continue
			}
			history.Commits = filtered
		}

		h.histories = append(h.histories, history)
		h.beadIDs = append(h.beadIDs, beadID)
	}

	// Sort by most commits first
	sort.Slice(h.histories, func(i, j int) bool {
		if len(h.histories[i].Commits) != len(h.histories[j].Commits) {
			return len(h.histories[i].Commits) > len(h.histories[j].Commits)
		}
		return h.histories[i].BeadID < h.histories[j].BeadID
	})

	// Rebuild beadIDs to match sorted order
	h.beadIDs = make([]string, len(h.histories))
	for i, hist := range h.histories {
		h.beadIDs[i] = hist.BeadID
	}

	// Restore selection if possible
	found := false
	if selectedID != "" {
		for i, id := range h.beadIDs {
			if id == selectedID {
				h.selectedBead = i
				found = true
				break
			}
		}
	}

	if found {
		// Clamp selected commit as commit list might have shrunk
		numCommits := len(h.histories[h.selectedBead].Commits)
		if h.selectedCommit >= numCommits {
			if numCommits > 0 {
				h.selectedCommit = numCommits - 1
			} else {
				h.selectedCommit = 0
			}
		}
	} else {
		// Reset selection if out of bounds or lost
		h.selectedBead = 0
		h.selectedCommit = 0
	}
}

// SetSize updates the view dimensions
func (h *HistoryModel) SetSize(width, height int) {
	h.width = width
	h.height = height
	timelineWidth, timelineHeight := h.timelinePanelSize()
	h.clampTimelineScrollOffset(timelineWidth, timelineHeight)

	// Adjust focus if pane is lost due to resize
	panes := h.paneCount()
	if panes < 4 && h.focused == historyFocusTimeline {
		h.focused = historyFocusList
	}
	if panes < 3 && h.focused == historyFocusMiddle {
		h.focused = historyFocusList
	}
}

type renderedTimelineLayout struct {
	title          string
	entries        []string
	summary        string
	entryRowBudget int
	showScroll     bool
}

func renderedTimelineRows(entries []string) int {
	rows := 0
	for _, entry := range entries {
		rows += lipgloss.Height(entry)
	}
	return rows
}

func timelineWindowEnd(entries []string, start, rowBudget int) int {
	if start < 0 {
		start = 0
	}
	if start >= len(entries) || rowBudget <= 0 {
		return start
	}

	rows := 0
	end := start
	for end < len(entries) {
		entryRows := lipgloss.Height(entries[end])
		if rows+entryRows > rowBudget {
			break
		}
		rows += entryRows
		end++
	}
	return end
}

func timelineFinalWindowStart(entries []string, rowBudget int) int {
	if len(entries) == 0 || rowBudget <= 0 {
		return 0
	}

	rows := 0
	start := len(entries)
	for start > 0 {
		entryRows := lipgloss.Height(entries[start-1])
		if rows+entryRows > rowBudget {
			break
		}
		rows += entryRows
		start--
	}
	return start
}

func (h *HistoryModel) timelinePanelSize() (width, height int) {
	width = int(float64(h.width) * 0.22)
	if width < 1 {
		width = 1
	}
	height = historyPanelHeight(h.height, h.renderHeader())
	return width, height
}

func (h *HistoryModel) layoutTimeline(hist *correlation.BeadHistory, width, height int) renderedTimelineLayout {
	entries := h.buildTimeline(*hist)
	layout := renderedTimelineLayout{
		title:   h.renderTimelineTitle(width, hist.BeadID),
		entries: make([]string, len(entries)),
		summary: h.renderTimelineSummary(hist, width),
	}
	for i := range entries {
		layout.entries[i] = h.renderTimelineEntry(entries[i], width)
	}

	contentRows := height - 2 // Rounded border.
	if contentRows < 0 {
		contentRows = 0
	}
	maxEntryRows := 0
	for _, entry := range layout.entries {
		maxEntryRows = max(maxEntryRows, lipgloss.Height(entry))
	}
	fixedRows := lipgloss.Height(layout.title)
	if len(layout.entries) == 0 {
		fixedRows += 2 // Blank line plus the empty-state message.
	}
	if layout.summary != "" {
		summaryRows := lipgloss.Height(layout.summary)
		// Timeline events are the panel's primary content. Keep the optional
		// summary only when the tallest relevant entry can still render whole.
		if fixedRows+summaryRows+maxEntryRows <= contentRows {
			fixedRows += summaryRows
		} else {
			layout.summary = ""
		}
	}

	availableRows := contentRows - fixedRows
	if availableRows < 0 {
		availableRows = 0
	}
	layout.entryRowBudget = availableRows
	if renderedTimelineRows(layout.entries) > availableRows && availableRows > maxEntryRows {
		// Reserve one row for the scroll indicator only when every individual
		// entry can still fit in the remaining viewport.
		layout.showScroll = true
		layout.entryRowBudget--
	}
	return layout
}

func (h *HistoryModel) timelineMaxScroll(width, height int) int {
	hist := h.SelectedHistory()
	if hist == nil {
		return 0
	}
	layout := h.layoutTimeline(hist, width, height)
	return timelineFinalWindowStart(layout.entries, layout.entryRowBudget)
}

func (h *HistoryModel) clampTimelineScrollOffset(width, height int) {
	if h.timelineScrollOffset < 0 {
		h.timelineScrollOffset = 0
	}
	if maxScroll := h.timelineMaxScroll(width, height); h.timelineScrollOffset > maxScroll {
		h.timelineScrollOffset = maxScroll
	}
}

// SetAuthorFilter sets the author filter and rebuilds the list
func (h *HistoryModel) SetAuthorFilter(author string) {
	h.authorFilter = author
	h.applySearchFilter()
}

// SetMinConfidence sets the minimum confidence threshold and rebuilds the list
func (h *HistoryModel) SetMinConfidence(conf float64) {
	h.minConfidence = conf
	h.applySearchFilter()
}

// File tree methods (bv-190l)

// buildFileTree constructs a tree from all files in the history report
func (h *HistoryModel) buildFileTree() {
	if h.report == nil {
		h.fileTree = nil
		h.flatFileList = nil
		return
	}

	// Count correlated commit appearances per file and ancestor directory path.
	// A commit that touches multiple files in the same directory should increment
	// that directory once, not once per file.
	fileChanges := make(map[string]int)
	for _, hist := range h.report.Histories {
		for _, commit := range hist.Commits {
			pathsInCommit := make(map[string]struct{})
			for _, file := range commit.Files {
				for _, path := range fileTreePathPrefixes(file.Path) {
					pathsInCommit[path] = struct{}{}
				}
			}
			for path := range pathsInCommit {
				fileChanges[path]++
			}
		}
	}

	// Build tree structure
	root := make(map[string]*FileTreeNode)

	for path, count := range fileChanges {
		parts := strings.Split(path, "/")

		// Create/update nodes for each part of the path
		for i := range parts {
			isLast := i == len(parts)-1
			fullPath := strings.Join(parts[:i+1], "/")
			name := parts[i]

			if _, exists := root[fullPath]; !exists {
				root[fullPath] = &FileTreeNode{
					Name:        name,
					Path:        fullPath,
					IsDir:       !isLast,
					ChangeCount: 0,
					Expanded:    false,
					Level:       i,
				}
			} else if !isLast {
				root[fullPath].IsDir = true
			}

			if isLast {
				root[fullPath].ChangeCount = count
			}
		}
	}

	// Link children to parents
	for path, node := range root {
		if node.Level == 0 {
			continue
		}
		parentPath := strings.Join(strings.Split(path, "/")[:node.Level], "/")
		if parent, exists := root[parentPath]; exists {
			parent.Children = append(parent.Children, node)
		}
	}

	// Extract root level nodes
	h.fileTree = nil
	for _, node := range root {
		if node.Level == 0 {
			h.sortTreeNode(node)
			h.fileTree = append(h.fileTree, node)
		}
	}

	// Sort root level
	sort.Slice(h.fileTree, func(i, j int) bool {
		if h.fileTree[i].IsDir != h.fileTree[j].IsDir {
			return h.fileTree[i].IsDir
		}
		return h.fileTree[i].Name < h.fileTree[j].Name
	})

	h.rebuildFlatFileList()
}

func fileTreePathPrefixes(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}

	parts := strings.Split(path, "/")
	nonEmptyParts := parts[:0]
	for _, part := range parts {
		if part != "" {
			nonEmptyParts = append(nonEmptyParts, part)
		}
	}
	if len(nonEmptyParts) == 0 {
		return nil
	}

	prefixes := make([]string, 0, len(nonEmptyParts))
	for i := range nonEmptyParts {
		prefixes = append(prefixes, strings.Join(nonEmptyParts[:i+1], "/"))
	}
	return prefixes
}

// sortTreeNode recursively sorts a tree node's children
func (h *HistoryModel) sortTreeNode(node *FileTreeNode) {
	if node.Children == nil {
		return
	}
	for _, child := range node.Children {
		h.sortTreeNode(child)
	}
	sort.Slice(node.Children, func(i, j int) bool {
		if node.Children[i].IsDir != node.Children[j].IsDir {
			return node.Children[i].IsDir
		}
		return node.Children[i].Name < node.Children[j].Name
	})
}

// rebuildFlatFileList creates a flat list of visible nodes for navigation
func (h *HistoryModel) rebuildFlatFileList() {
	h.flatFileList = nil
	for _, node := range h.fileTree {
		h.addToFlatList(node)
	}
}

// addToFlatList recursively adds nodes to the flat list
func (h *HistoryModel) addToFlatList(node *FileTreeNode) {
	h.flatFileList = append(h.flatFileList, node)
	if node.IsDir && node.Expanded {
		for _, child := range node.Children {
			h.addToFlatList(child)
		}
	}
}

// ToggleFileTree toggles the file tree panel visibility
func (h *HistoryModel) ToggleFileTree() {
	h.showFileTree = !h.showFileTree
	if h.showFileTree && h.fileTree == nil {
		h.buildFileTree()
	}
}

// IsFileTreeVisible returns whether the file tree panel is visible
func (h *HistoryModel) IsFileTreeVisible() bool {
	return h.showFileTree
}

// FileTreeHasFocus returns whether the file tree has focus
func (h *HistoryModel) FileTreeHasFocus() bool {
	return h.fileTreeFocus
}

// SetFileTreeFocus sets the file tree focus state
func (h *HistoryModel) SetFileTreeFocus(focus bool) {
	h.fileTreeFocus = focus
}

// MoveUpFileTree moves selection up in the file tree
func (h *HistoryModel) MoveUpFileTree() {
	if h.selectedFileIdx > 0 {
		h.selectedFileIdx--
	}
}

// MoveDownFileTree moves selection down in the file tree
func (h *HistoryModel) MoveDownFileTree() {
	if h.selectedFileIdx < 0 {
		if len(h.flatFileList) > 0 {
			h.selectedFileIdx = 0
		}
		return
	}
	if h.selectedFileIdx < len(h.flatFileList)-1 {
		h.selectedFileIdx++
	}
}

// ToggleExpandFile expands or collapses the selected directory
func (h *HistoryModel) ToggleExpandFile() {
	if h.selectedFileIdx < 0 || h.selectedFileIdx >= len(h.flatFileList) {
		return
	}
	node := h.flatFileList[h.selectedFileIdx]
	if node.IsDir {
		node.Expanded = !node.Expanded
		h.rebuildFlatFileList()
	}
}

// SelectFile sets the file filter to the selected file
func (h *HistoryModel) SelectFile() {
	if h.selectedFileIdx < 0 || h.selectedFileIdx >= len(h.flatFileList) {
		return
	}
	node := h.flatFileList[h.selectedFileIdx]
	if h.fileFilter == node.Path {
		h.fileFilter = ""
	} else {
		h.fileFilter = node.Path
	}
	h.applySearchFilter()
}

// ClearFileFilter clears the file filter
func (h *HistoryModel) ClearFileFilter() {
	h.fileFilter = ""
	h.applySearchFilter()
}

// GetFileFilter returns the current file filter
func (h *HistoryModel) GetFileFilter() string {
	return h.fileFilter
}

// SelectedFileName returns the name of the selected file/directory
func (h *HistoryModel) SelectedFileName() string {
	if h.selectedFileIdx < 0 || h.selectedFileIdx >= len(h.flatFileList) {
		return ""
	}
	return h.flatFileList[h.selectedFileIdx].Name
}

// SelectedFileNode returns the currently selected file tree node
func (h *HistoryModel) SelectedFileNode() *FileTreeNode {
	if h.selectedFileIdx < 0 || h.selectedFileIdx >= len(h.flatFileList) {
		return nil
	}
	return h.flatFileList[h.selectedFileIdx]
}

// CollapseFileNode collapses the selected node if it's an expanded directory
func (h *HistoryModel) CollapseFileNode() {
	if h.selectedFileIdx < 0 || h.selectedFileIdx >= len(h.flatFileList) {
		return
	}
	node := h.flatFileList[h.selectedFileIdx]
	if node.IsDir && node.Expanded {
		node.Expanded = false
		h.rebuildFlatFileList()
	}
}

// Navigation methods

// MoveUp moves selection up in the current focus pane
func (h *HistoryModel) MoveUp() {
	if h.focused == historyFocusList {
		if h.selectedBead > 0 {
			h.selectedBead--
			h.selectedCommit = 0
			h.middleScrollOffset = 0 // Reset middle scroll when changing bead (bv-xrfh)
			h.timelineScrollOffset = 0
			h.ensureBeadVisible()
		}
	} else if h.focused == historyFocusTimeline {
		timelineWidth, timelineHeight := h.timelinePanelSize()
		h.clampTimelineScrollOffset(timelineWidth, timelineHeight)
		if h.timelineScrollOffset > 0 {
			h.timelineScrollOffset--
		}
	} else {
		// In middle or detail pane, move to previous commit
		if h.selectedCommit > 0 {
			h.selectedCommit--
			// Update middle pane scroll if in three-pane layout (bv-xrfh)
			if h.focused == historyFocusMiddle && h.selectedBead >= 0 && h.selectedBead < len(h.histories) {
				h.ensureMiddleScrollVisible(h.selectedCommit, len(h.histories[h.selectedBead].Commits))
			}
		}
	}
}

// MoveDown moves selection down in the current focus pane
func (h *HistoryModel) MoveDown() {
	if h.focused == historyFocusList {
		if h.selectedBead < len(h.histories)-1 {
			h.selectedBead++
			h.selectedCommit = 0
			h.middleScrollOffset = 0 // Reset middle scroll when changing bead (bv-xrfh)
			h.timelineScrollOffset = 0
			h.ensureBeadVisible()
		}
	} else if h.focused == historyFocusTimeline {
		timelineWidth, timelineHeight := h.timelinePanelSize()
		h.clampTimelineScrollOffset(timelineWidth, timelineHeight)
		if h.timelineScrollOffset < h.timelineMaxScroll(timelineWidth, timelineHeight) {
			h.timelineScrollOffset++
		}
	} else {
		// In middle or detail pane, move to next commit
		if h.selectedBead >= 0 && h.selectedBead < len(h.histories) {
			commits := h.histories[h.selectedBead].Commits
			if h.selectedCommit < len(commits)-1 {
				h.selectedCommit++
				// Update middle pane scroll if in three-pane layout (bv-xrfh)
				if h.focused == historyFocusMiddle {
					h.ensureMiddleScrollVisible(h.selectedCommit, len(commits))
				}
			}
		}
	}
}

// ToggleFocus cycles through panes based on current layout (bv-xrfh)
func (h *HistoryModel) ToggleFocus() {
	panes := h.paneCount()
	if panes == 4 {
		// Four-pane: List -> Timeline -> Middle -> Detail -> List
		switch h.focused {
		case historyFocusList:
			h.focused = historyFocusTimeline
		case historyFocusTimeline:
			h.focused = historyFocusMiddle
		case historyFocusMiddle:
			h.focused = historyFocusDetail
		default:
			h.focused = historyFocusList
		}
	} else if panes == 3 {
		// Three-pane: List -> Middle -> Detail -> List
		switch h.focused {
		case historyFocusList:
			h.focused = historyFocusMiddle
		case historyFocusMiddle:
			h.focused = historyFocusDetail
		default:
			h.focused = historyFocusList
		}
	} else {
		// Two-pane: List <-> Detail
		if h.focused == historyFocusList {
			h.focused = historyFocusDetail
		} else {
			h.focused = historyFocusList
		}
	}
}

// IsDetailFocused returns true if the detail pane has focus (bv-190l)
func (h *HistoryModel) IsDetailFocused() bool {
	return h.focused == historyFocusDetail
}

// NextCommit moves to the next commit within the selected bead (J key)
func (h *HistoryModel) NextCommit() {
	if h.selectedBead < 0 || h.selectedBead >= len(h.histories) {
		return
	}
	commits := h.histories[h.selectedBead].Commits
	if h.selectedCommit < len(commits)-1 {
		h.selectedCommit++
	}
}

// PrevCommit moves to the previous commit within the selected bead (K key)
func (h *HistoryModel) PrevCommit() {
	if h.selectedCommit > 0 {
		h.selectedCommit--
	}
}

// CycleConfidence cycles through common confidence thresholds (0, 0.5, 0.75, 0.9)
func (h *HistoryModel) CycleConfidence() {
	thresholds := []float64{0, 0.5, 0.75, 0.9}
	// Find current threshold index
	currentIdx := 0
	for i, t := range thresholds {
		if h.minConfidence >= t-0.01 && h.minConfidence <= t+0.01 {
			currentIdx = i
			break
		}
	}
	// Move to next threshold (wrap around)
	nextIdx := (currentIdx + 1) % len(thresholds)
	h.SetMinConfidence(thresholds[nextIdx])
}

// GetMinConfidence returns the current minimum confidence threshold
func (h *HistoryModel) GetMinConfidence() float64 {
	return h.minConfidence
}

// ToggleExpand expands/collapses the commits for the selected bead
func (h *HistoryModel) ToggleExpand() {
	if h.selectedBead >= 0 && h.selectedBead < len(h.beadIDs) {
		beadID := h.beadIDs[h.selectedBead]
		h.expandedBeads[beadID] = !h.expandedBeads[beadID]
	}
}

// Search and Filter methods (bv-nkrj)

// StartSearch activates the search input
func (h *HistoryModel) StartSearch() tea.Cmd {
	h.searchActive = true
	h.searchMode = searchModeAll
	return h.searchInput.Focus()
}

// StartSearchWithMode activates search with a specific mode
func (h *HistoryModel) StartSearchWithMode(mode historySearchMode) tea.Cmd {
	h.searchActive = true
	h.searchMode = mode
	cmd := h.searchInput.Focus()

	// Set appropriate placeholder based on mode
	switch mode {
	case searchModeCommit:
		h.searchInput.Placeholder = "Search commit messages..."
	case searchModeSHA:
		h.searchInput.Placeholder = "Enter SHA prefix..."
	case searchModeBead:
		h.searchInput.Placeholder = "Search bead ID or title..."
	case searchModeAuthor:
		h.searchInput.Placeholder = "Search by author..."
	default:
		h.searchInput.Placeholder = "Search commits, beads, authors..."
	}
	return cmd
}

// CancelSearch cancels the search and clears the query
func (h *HistoryModel) CancelSearch() {
	h.searchActive = false
	h.searchInput.Blur()
	h.searchInput.SetValue("")
	h.searchMode = searchModeOff
	h.lastSearchQuery = ""
	h.applySearchFilter()
}

// FinishSearch exits active search input while preserving the current query and filter.
func (h *HistoryModel) FinishSearch() {
	h.searchActive = false
	h.searchInput.Blur()
}

// ClearSearch clears the search query but keeps search mode active
func (h *HistoryModel) ClearSearch() {
	h.searchInput.SetValue("")
	h.lastSearchQuery = ""
	h.applySearchFilter()
}

// IsSearchActive returns whether search input is active
func (h *HistoryModel) IsSearchActive() bool {
	return h.searchActive
}

// SearchQuery returns the current search query
func (h *HistoryModel) SearchQuery() string {
	return h.searchInput.Value()
}

// UpdateSearchInput updates the search input model (call from Update) and
// returns any follow-up command, such as an asynchronous clipboard paste.
func (h *HistoryModel) UpdateSearchInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	h.searchInput, cmd = h.searchInput.Update(msg)

	// Check if query changed and apply filter
	currentQuery := h.searchInput.Value()
	if currentQuery != h.lastSearchQuery {
		h.lastSearchQuery = currentQuery
		h.applySearchFilter()
	}
	return cmd
}

// applySearchFilter filters the data based on current search query
func (h *HistoryModel) applySearchFilter() {
	selectedBeadID := ""
	selectedBeadCommitSHA := ""
	selectedGitSHA := ""
	selectedRelatedBead := ""
	if h.viewMode == historyModeGit {
		if selected := h.SelectedGitCommit(); selected != nil {
			selectedGitSHA = selected.SHA
			selectedRelatedBead = h.SelectedRelatedBeadID()
		}
	} else {
		selectedBeadID = h.SelectedBeadID()
		if selected := h.SelectedCommit(); selected != nil {
			selectedBeadCommitSHA = selected.SHA
		}
	}

	// Always rebuild base filtered list first (applies author/confidence filters)
	// This ensures we always filter from the complete set, not an already-filtered list
	// (bv-nkrj fix: backspacing to relax filter now works correctly)
	h.rebuildFilteredList()
	if h.viewMode == historyModeGit {
		h.buildCommitList()
	}

	query := strings.TrimSpace(h.searchInput.Value())
	if query == "" {
		h.filteredCommits = nil // Use full commitList in git mode
	} else {
		// Apply search filter on top of base filters.
		if h.viewMode == historyModeGit {
			h.filterCommitList(query)
		} else {
			h.filterBeadList(query)
		}
	}

	if h.viewMode == historyModeGit {
		h.restoreGitSelection(selectedGitSHA, selectedRelatedBead)
	} else {
		h.restoreBeadSelection(selectedBeadID, selectedBeadCommitSHA)
	}
	// Timeline rows and their count are derived from the filtered commit slice.
	// Retaining an offset from the previous slice can strand navigation beyond
	// the new maximum even though rendering locally displays the first row.
	h.timelineScrollOffset = 0
}

// filterCommitList filters commits in git mode based on search query
func (h *HistoryModel) filterCommitList(query string) {
	if len(h.commitList) == 0 {
		// A non-empty query with no source commits is still an active filter.
		// Keep a non-nil empty slice so GetFilteredCommitList does not interpret
		// this as the sentinel for "no filter" if commits are installed later.
		h.filteredCommits = []CommitListEntry{}
		return
	}

	query = strings.ToLower(query)
	// nil means "no active query" throughout HistoryModel. Start with a
	// non-nil empty result so a valid query that matches nothing cannot fall
	// through to the complete commit list.
	filtered := make([]CommitListEntry, 0)

	for _, commit := range h.commitList {
		if h.commitMatchesQuery(commit, query) {
			filtered = append(filtered, commit)
		}
	}

	h.filteredCommits = filtered
	// Reset selection if out of bounds
	if h.selectedGitCommit >= len(filtered) {
		h.selectedGitCommit = 0
		h.selectedRelatedBead = 0
	}
	h.gitScrollOffset = 0
}

// commitMatchesQuery checks if a commit matches the search query
func (h *HistoryModel) commitMatchesQuery(commit CommitListEntry, query string) bool {
	switch h.searchMode {
	case searchModeSHA:
		return strings.HasPrefix(strings.ToLower(commit.SHA), query) ||
			strings.HasPrefix(strings.ToLower(commit.ShortSHA), query)
	case searchModeCommit:
		return strings.Contains(strings.ToLower(commit.Message), query)
	case searchModeAuthor:
		return strings.Contains(strings.ToLower(commit.Author), query)
	case searchModeBead:
		for _, beadID := range commit.BeadIDs {
			if strings.Contains(strings.ToLower(beadID), query) {
				return true
			}
			// Also check bead title if available
			if h.report != nil {
				if hist, ok := h.report.Histories[beadID]; ok {
					if strings.Contains(strings.ToLower(hist.Title), query) {
						return true
					}
				}
			}
		}
		return false
	default: // searchModeAll - search across all fields
		if strings.HasPrefix(strings.ToLower(commit.SHA), query) ||
			strings.HasPrefix(strings.ToLower(commit.ShortSHA), query) {
			return true
		}
		if strings.Contains(strings.ToLower(commit.Message), query) {
			return true
		}
		if strings.Contains(strings.ToLower(commit.Author), query) {
			return true
		}
		for _, beadID := range commit.BeadIDs {
			if strings.Contains(strings.ToLower(beadID), query) {
				return true
			}
			if h.report != nil {
				if hist, ok := h.report.Histories[beadID]; ok &&
					strings.Contains(strings.ToLower(hist.Title), query) {
					return true
				}
			}
		}
		return false
	}
}

// filterBeadList filters beads in bead mode based on search query
func (h *HistoryModel) filterBeadList(query string) {
	if h.report == nil {
		return
	}

	query = strings.ToLower(query)
	var filteredHistories []correlation.BeadHistory
	var filteredIDs []string

	// h.histories already carries the author/confidence/file-filtered commit
	// slices. Searching the raw report here would resurrect commits excluded by
	// those base filters and make the displayed rows disagree with the badges.
	for _, hist := range h.histories {
		beadID := hist.BeadID
		if h.beadMatchesQuery(beadID, hist, query) {
			filteredHistories = append(filteredHistories, hist)
			filteredIDs = append(filteredIDs, beadID)
		}
	}

	h.histories = filteredHistories
	h.beadIDs = filteredIDs

	// Reset selection if out of bounds
	if h.selectedBead >= len(h.beadIDs) {
		h.selectedBead = 0
		h.selectedCommit = 0
	}
	h.scrollOffset = 0
}

// beadMatchesQuery checks if a bead matches the search query
func (h *HistoryModel) beadMatchesQuery(beadID string, hist correlation.BeadHistory, query string) bool {
	switch h.searchMode {
	case searchModeBead:
		return strings.Contains(strings.ToLower(beadID), query) ||
			strings.Contains(strings.ToLower(hist.Title), query)
	case searchModeCommit:
		for _, commit := range hist.Commits {
			if strings.Contains(strings.ToLower(commit.Message), query) {
				return true
			}
		}
		return false
	case searchModeSHA:
		for _, commit := range hist.Commits {
			if strings.HasPrefix(strings.ToLower(commit.SHA), query) ||
				strings.HasPrefix(strings.ToLower(commit.ShortSHA), query) {
				return true
			}
		}
		return false
	case searchModeAuthor:
		for _, commit := range hist.Commits {
			if strings.Contains(strings.ToLower(commit.Author), query) {
				return true
			}
		}
		return false
	default: // searchModeAll
		// Check bead ID and title
		if strings.Contains(strings.ToLower(beadID), query) ||
			strings.Contains(strings.ToLower(hist.Title), query) {
			return true
		}
		// Check commits
		for _, commit := range hist.Commits {
			if strings.Contains(strings.ToLower(commit.Message), query) ||
				strings.Contains(strings.ToLower(commit.Author), query) ||
				strings.HasPrefix(strings.ToLower(commit.ShortSHA), query) {
				return true
			}
		}
		return false
	}
}

// GetFilteredCommitList returns the filtered commit list for git mode
func (h *HistoryModel) GetFilteredCommitList() []CommitListEntry {
	if h.filteredCommits != nil {
		return h.filteredCommits
	}
	return h.commitList
}

// GetSearchModeName returns a human-readable name for the current search mode
func (h *HistoryModel) GetSearchModeName() string {
	switch h.searchMode {
	case searchModeCommit:
		return "msg"
	case searchModeSHA:
		return "sha"
	case searchModeBead:
		return "bead"
	case searchModeAuthor:
		return "author"
	default:
		return "all"
	}
}

// Git-Centric View Mode methods (bv-tl3n)

// ToggleViewMode switches between Bead mode and Git mode
func (h *HistoryModel) ToggleViewMode() {
	// Track mode change time for transition animation (bv-kvlx)
	h.modeChangedAt = time.Now()

	if h.viewMode == historyModeBead {
		h.viewMode = historyModeGit
		h.buildCommitList()
		h.selectedGitCommit = 0
		h.selectedRelatedBead = 0
		h.gitScrollOffset = 0
	} else {
		h.viewMode = historyModeBead
		h.selectedBead = 0
		h.selectedCommit = 0
		h.scrollOffset = 0
		h.timelineScrollOffset = 0
	}
	// Re-apply preserved search query after mode changes, even after the input
	// has been submitted and blurred.
	if strings.TrimSpace(h.searchInput.Value()) != "" {
		h.applySearchFilter()
	}
}

// IsGitMode returns true if in git-centric view mode
func (h *HistoryModel) IsGitMode() bool {
	return h.viewMode == historyModeGit
}

// buildCommitList constructs the sorted commit list for git mode
func (h *HistoryModel) buildCommitList() {
	if h.report == nil {
		h.commitList = nil
		return
	}

	entryIndexBySHA := make(map[string]int)
	beadIDsBySHA := make(map[string]map[string]struct{})
	var entries []CommitListEntry

	// Collect commits from the base filtered histories. Search is applied to the
	// resulting commit list below, but author, confidence, and file filters have
	// already narrowed h.histories and must affect git-centric mode too.
	for _, hist := range h.histories {
		beadID := hist.BeadID
		for _, commit := range hist.Commits {
			if entryIndex, ok := entryIndexBySHA[commit.SHA]; ok {
				if _, ok := beadIDsBySHA[commit.SHA][beadID]; !ok {
					beadIDsBySHA[commit.SHA][beadID] = struct{}{}
					entries[entryIndex].BeadIDs = append(entries[entryIndex].BeadIDs, beadID)
				}
				continue
			}

			entryIndexBySHA[commit.SHA] = len(entries)
			beadIDsBySHA[commit.SHA] = map[string]struct{}{beadID: {}}

			entries = append(entries, CommitListEntry{
				SHA:           commit.SHA,
				ShortSHA:      commit.ShortSHA,
				Message:       commit.Message,
				Author:        commit.Author,
				Timestamp:     commit.Timestamp.Format("2006-01-02 15:04"),
				FileCount:     len(commit.Files),
				BeadIDs:       []string{beadID},
				sortTimestamp: commit.Timestamp,
			})
		}
	}

	for i := range entries {
		sort.Strings(entries[i].BeadIDs)
	}

	// Sort by the complete timestamp descending (most recent first). The display
	// string intentionally omits seconds and therefore cannot be the sort key.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].sortTimestamp.Equal(entries[j].sortTimestamp) {
			// Deterministic fallback for commits at the same instant.
			return entries[i].SHA > entries[j].SHA
		}
		return entries[i].sortTimestamp.After(entries[j].sortTimestamp)
	})

	h.commitList = entries
}

// MoveUpGit moves selection up in git mode
func (h *HistoryModel) MoveUpGit() {
	if h.focused == historyFocusList {
		if h.selectedGitCommit > 0 {
			h.selectedGitCommit--
			h.selectedRelatedBead = 0
			h.middleScrollOffset = 0 // Reset middle scroll when changing commit (bv-xrfh)
			h.ensureGitCommitVisible()
		}
	} else {
		// In middle or detail pane, move to previous related bead
		if h.selectedRelatedBead > 0 {
			h.selectedRelatedBead--
			// Update middle pane scroll if in three-pane layout (bv-xrfh)
			if h.focused == historyFocusMiddle {
				commit := h.SelectedGitCommit()
				if commit != nil {
					h.ensureMiddleScrollVisible(h.selectedRelatedBead, len(commit.BeadIDs))
				}
			}
		}
	}
}

// MoveDownGit moves selection down in git mode
func (h *HistoryModel) MoveDownGit() {
	commits := h.GetFilteredCommitList() // Use filtered list (bv-nkrj)
	if h.focused == historyFocusList {
		if h.selectedGitCommit < len(commits)-1 {
			h.selectedGitCommit++
			h.selectedRelatedBead = 0
			h.middleScrollOffset = 0 // Reset middle scroll when changing commit (bv-xrfh)
			h.ensureGitCommitVisible()
		}
	} else {
		// In middle or detail pane, move to next related bead
		if h.selectedGitCommit >= 0 && h.selectedGitCommit < len(commits) {
			beadCount := len(commits[h.selectedGitCommit].BeadIDs)
			if h.selectedRelatedBead < beadCount-1 {
				h.selectedRelatedBead++
				// Update middle pane scroll if in three-pane layout (bv-xrfh)
				if h.focused == historyFocusMiddle {
					h.ensureMiddleScrollVisible(h.selectedRelatedBead, beadCount)
				}
			}
		}
	}
}

// NextRelatedBead moves to the next related bead in git mode (J key)
func (h *HistoryModel) NextRelatedBead() {
	commits := h.GetFilteredCommitList() // Use filtered list (bv-nkrj)
	if h.selectedGitCommit < 0 || h.selectedGitCommit >= len(commits) {
		return
	}
	beadCount := len(commits[h.selectedGitCommit].BeadIDs)
	if h.selectedRelatedBead < beadCount-1 {
		h.selectedRelatedBead++
	}
}

// PrevRelatedBead moves to the previous related bead in git mode (K key)
func (h *HistoryModel) PrevRelatedBead() {
	if h.selectedRelatedBead > 0 {
		h.selectedRelatedBead--
	}
}

// SelectedGitCommit returns the selected commit in git mode
func (h *HistoryModel) SelectedGitCommit() *CommitListEntry {
	commits := h.GetFilteredCommitList() // Use filtered list (bv-nkrj)
	if h.selectedGitCommit >= 0 && h.selectedGitCommit < len(commits) {
		return &commits[h.selectedGitCommit]
	}
	return nil
}

// SelectedRelatedBeadID returns the currently selected related bead ID in git mode
func (h *HistoryModel) SelectedRelatedBeadID() string {
	commit := h.SelectedGitCommit()
	if commit != nil && h.selectedRelatedBead >= 0 && h.selectedRelatedBead < len(commit.BeadIDs) {
		return commit.BeadIDs[h.selectedRelatedBead]
	}
	return ""
}

// ensureGitCommitVisible adjusts scroll offset to keep selected commit visible
func (h *HistoryModel) ensureGitCommitVisible() {
	visibleItems := h.listHeight()
	if visibleItems < 1 {
		visibleItems = 1
	}

	if h.selectedGitCommit < h.gitScrollOffset {
		h.gitScrollOffset = h.selectedGitCommit
	} else if h.selectedGitCommit >= h.gitScrollOffset+visibleItems {
		h.gitScrollOffset = h.selectedGitCommit - visibleItems + 1
	}
}

// ensureBeadVisible adjusts scroll offset to keep selected bead visible
func (h *HistoryModel) ensureBeadVisible() {
	visibleItems := h.listHeight()
	if visibleItems < 1 {
		visibleItems = 1
	}

	if h.selectedBead < h.scrollOffset {
		h.scrollOffset = h.selectedBead
	} else if h.selectedBead >= h.scrollOffset+visibleItems {
		h.scrollOffset = h.selectedBead - visibleItems + 1
	}
}

// ensureMiddleScrollVisible adjusts middle pane scroll offset (bv-xrfh)
func (h *HistoryModel) ensureMiddleScrollVisible(selectedIdx, itemCount int) {
	// Use similar height calculation as middle pane (accounting for header/border)
	visibleItems := h.height - 7 // Header, separator, border padding
	if visibleItems < 1 {
		visibleItems = 1
	}

	if selectedIdx < h.middleScrollOffset {
		h.middleScrollOffset = selectedIdx
	} else if selectedIdx >= h.middleScrollOffset+visibleItems {
		h.middleScrollOffset = selectedIdx - visibleItems + 1
	}

	// Clamp to valid range
	maxScroll := itemCount - visibleItems
	if maxScroll < 0 {
		maxScroll = 0
	}
	if h.middleScrollOffset > maxScroll {
		h.middleScrollOffset = maxScroll
	}
}

// listHeight returns the number of visible items in the list
func (h *HistoryModel) listHeight() int {
	// Reserve 3 lines for header/filter bar
	return h.height - 3
}

// SelectedBeadID returns the currently selected bead ID
func (h *HistoryModel) SelectedBeadID() string {
	if h.selectedBead >= 0 && h.selectedBead < len(h.beadIDs) {
		return h.beadIDs[h.selectedBead]
	}
	return ""
}

// SelectedHistory returns the currently selected bead history
func (h *HistoryModel) SelectedHistory() *correlation.BeadHistory {
	if h.selectedBead >= 0 && h.selectedBead < len(h.histories) {
		return &h.histories[h.selectedBead]
	}
	return nil
}

// SelectedCommit returns the currently selected commit
func (h *HistoryModel) SelectedCommit() *correlation.CorrelatedCommit {
	hist := h.SelectedHistory()
	if hist != nil && h.selectedCommit >= 0 && h.selectedCommit < len(hist.Commits) {
		return &hist.Commits[h.selectedCommit]
	}
	return nil
}

// GetHistoryForBead returns the history for a specific bead ID
func (h *HistoryModel) GetHistoryForBead(beadID string) *correlation.BeadHistory {
	if h.report == nil {
		return nil
	}
	hist, ok := h.report.Histories[beadID]
	if !ok {
		return nil
	}
	return &hist
}

// HasReport returns true if history data is loaded
func (h *HistoryModel) HasReport() bool {
	return h.report != nil
}

// determineLayout returns the appropriate layout based on terminal width (bv-xrfh)
func (h *HistoryModel) determineLayout() historyLayout {
	if h.width < layoutBreakpointStandard {
		return layoutNarrow
	} else if h.width < layoutBreakpointWide {
		return layoutStandard
	}
	return layoutWide
}

// paneCount returns the number of visible panes for the current layout (bv-xrfh)
func (h *HistoryModel) paneCount() int {
	layout := h.determineLayout()
	if layout == layoutWide && h.viewMode != historyModeGit {
		return 4
	}
	switch layout {
	case layoutNarrow:
		return 2
	case layoutStandard, layoutWide:
		return 3
	default:
		return 2
	}
}

func historyPanelHeight(totalHeight int, header string) int {
	// Every panel has a two-row rounded border and needs at least one content
	// row. For ordinary terminal sizes this is the exact remaining row budget;
	// the floor only keeps very small debug/test sizes renderable.
	return max(totalHeight-lipgloss.Height(header), 3)
}

// View renders the history view
func (h *HistoryModel) View() string {
	if h.report == nil {
		return h.renderEmpty("No history data loaded")
	}

	// In git mode, check commit list; in bead mode, check histories
	if h.viewMode == historyModeGit {
		if len(h.commitList) == 0 {
			return h.renderEmpty("No commits with bead correlations found")
		}
	} else {
		if len(h.histories) == 0 {
			return h.renderEmpty("No beads with commit correlations found")
		}
	}

	// Dispatch to layout-specific renderer (bv-xrfh)
	layout := h.determineLayout()
	switch layout {
	case layoutStandard, layoutWide:
		return h.renderThreePaneView()
	default:
		return h.renderTwoPaneView()
	}
}

// renderTwoPaneView renders the narrow two-pane layout (bv-xrfh)
func (h *HistoryModel) renderTwoPaneView() string {
	// Calculate panel widths (45% list, 55% detail for narrow)
	listWidth := int(float64(h.width) * 0.45)
	detailWidth := h.width - listWidth

	// Render header
	header := h.renderHeader()
	panelHeight := historyPanelHeight(h.height, header)

	// Render panels based on view mode (bv-tl3n)
	var listPanel, detailPanel string
	if h.viewMode == historyModeGit {
		listPanel = h.renderGitCommitListPanel(listWidth, panelHeight)
		detailPanel = h.renderGitDetailPanel(detailWidth, panelHeight)
	} else {
		listPanel = h.renderListPanel(listWidth, panelHeight)
		detailPanel = h.renderDetailPanel(detailWidth, panelHeight)
	}

	// Combine panels
	panels := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, detailPanel)

	return lipgloss.JoinVertical(lipgloss.Left, header, panels)
}

// renderThreePaneView renders the three-pane layout for wider terminals (bv-xrfh)
// In wide mode (>150 cols), adds a fourth timeline pane (bv-1x6o)
func (h *HistoryModel) renderThreePaneView() string {
	layout := h.determineLayout()

	// Render header
	header := h.renderHeader()
	panelHeight := historyPanelHeight(h.height, header)

	// Wide layout: 4 panes with timeline (bv-1x6o)
	if layout == layoutWide && h.viewMode != historyModeGit {
		// Wide bead mode: 20% beads | 22% timeline | 25% commits | 33% details
		listWidth := int(float64(h.width) * 0.20)
		timelineWidth := int(float64(h.width) * 0.22)
		middleWidth := int(float64(h.width) * 0.25)
		detailWidth := h.width - listWidth - timelineWidth - middleWidth

		listPanel := h.renderListPanel(listWidth, panelHeight)
		timelinePanel := h.renderTimelinePanel(timelineWidth, panelHeight)
		middlePanel := h.renderCommitMiddlePanel(middleWidth, panelHeight)
		detailPanel := h.renderDetailPanel(detailWidth, panelHeight)

		panels := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, timelinePanel, middlePanel, detailPanel)
		return lipgloss.JoinVertical(lipgloss.Left, header, panels)
	}

	// Standard 3-pane layout (also used for git mode in wide)
	var listWidth, middleWidth, detailWidth int
	if layout == layoutWide {
		// Wide git mode: 25% | 30% | 45%
		listWidth = int(float64(h.width) * 0.25)
		middleWidth = int(float64(h.width) * 0.30)
		detailWidth = h.width - listWidth - middleWidth
	} else {
		// Standard: 30% | 35% | 35%
		listWidth = int(float64(h.width) * 0.30)
		middleWidth = int(float64(h.width) * 0.35)
		detailWidth = h.width - listWidth - middleWidth
	}

	// Render panels based on view mode
	var listPanel, middlePanel, detailPanel string

	if h.viewMode == historyModeGit {
		// Git mode: commits on left, related beads in middle, detail on right
		listPanel = h.renderGitCommitListPanel(listWidth, panelHeight)
		middlePanel = h.renderGitBeadListPanel(middleWidth, panelHeight)
		detailPanel = h.renderGitDetailPanel(detailWidth, panelHeight)
	} else {
		// Bead mode: beads on left, commits in middle, detail on right
		listPanel = h.renderListPanel(listWidth, panelHeight)
		middlePanel = h.renderCommitMiddlePanel(middleWidth, panelHeight)
		detailPanel = h.renderDetailPanel(detailWidth, panelHeight)
	}

	// Combine panels
	panels := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, middlePanel, detailPanel)

	return lipgloss.JoinVertical(lipgloss.Left, header, panels)
}

// buildTimeline creates timeline entries from a bead's history (bv-1x6o)
func (h *HistoryModel) buildTimeline(hist correlation.BeadHistory) []TimelineEntry {
	var entries []TimelineEntry

	// Add lifecycle events from milestones (more reliable than Events slice)
	if hist.Milestones.Created != nil {
		entries = append(entries, TimelineEntry{
			Timestamp: hist.Milestones.Created.Timestamp,
			EntryType: timelineEntryEvent,
			Label:     "○ Created",
			Detail:    hist.Title,
			EventType: "created",
		})
	}
	if hist.Milestones.Claimed != nil {
		entries = append(entries, TimelineEntry{
			Timestamp: hist.Milestones.Claimed.Timestamp,
			EntryType: timelineEntryEvent,
			Label:     "● Claimed",
			Detail:    fmt.Sprintf("by %s", hist.Milestones.Claimed.Author),
			EventType: "claimed",
		})
	}
	if hist.Milestones.Reopened != nil {
		entries = append(entries, TimelineEntry{
			Timestamp: hist.Milestones.Reopened.Timestamp,
			EntryType: timelineEntryEvent,
			Label:     "↻ Reopened",
			Detail:    "",
			EventType: "reopened",
		})
	}
	if hist.Milestones.Closed != nil {
		entries = append(entries, TimelineEntry{
			Timestamp: hist.Milestones.Closed.Timestamp,
			EntryType: timelineEntryEvent,
			Label:     "✓ Closed",
			Detail:    "",
			EventType: "closed",
		})
	}

	// Add commits
	for _, commit := range hist.Commits {
		entries = append(entries, TimelineEntry{
			Timestamp:  commit.Timestamp,
			EntryType:  timelineEntryCommit,
			Label:      commit.ShortSHA,
			Detail:     commit.Message,
			Confidence: commit.Confidence,
		})
	}

	// Add sessions from cache if available (bv-pr1l)
	if sessions, ok := h.sessionCache[hist.BeadID]; ok {
		for _, session := range sessions {
			entries = append(entries, TimelineEntry{
				Timestamp:           session.Timestamp,
				EntryType:           timelineEntrySession,
				Label:               fmt.Sprintf("📎 %s session", capitalizeFirst(session.Agent)),
				Detail:              session.Title,
				SessionAgent:        session.Agent,
				SessionMessageCount: 0, // Message count not available from SearchResult
				SessionPath:         session.SourcePath,
				SessionScore:        session.FinalScore,
			})
		}
	}

	// Sort chronologically, with commits before sessions on timestamp ties
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Timestamp.Equal(entries[j].Timestamp) {
			// On ties: events first, then commits, then sessions
			return entries[i].EntryType < entries[j].EntryType
		}
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	return entries
}

// capitalizeFirst capitalizes the first letter of a string (bv-pr1l)
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

// formatTimelineTimestamp formats a timestamp for the timeline (bv-1x6o)
func (h *HistoryModel) formatTimelineTimestamp(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < 24*time.Hour:
		return t.Format("3:04 PM")
	case diff < 7*24*time.Hour:
		return t.Format("Mon 3PM")
	case diff < 365*24*time.Hour:
		return t.Format("Jan 2")
	default:
		return t.Format("Jan '06")
	}
}

func (h *HistoryModel) renderTimelineTitle(width int, beadID string) string {
	label := "TIMELINE"
	if beadID != "" {
		label += ": " + beadID
	}
	contentWidth := max(width-4, 1)
	label = truncateRunesHelper(label, contentWidth, "…")
	return h.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(h.theme.Primary).
		Width(contentWidth).
		Align(lipgloss.Center).
		Render(label)
}

func (h *HistoryModel) renderTimelineEntry(entry TimelineEntry, width int) string {
	t := h.theme
	r := t.Renderer
	lineColor := t.Border
	contentWidth := max(width-4, 1)

	timestamp := h.formatTimelineTimestamp(entry.Timestamp)
	timestampStyle := r.NewStyle().
		Foreground(t.Subtext).
		Width(8).
		Align(lipgloss.Right)

	var b strings.Builder
	b.WriteString(timestampStyle.Render(timestamp))
	b.WriteString(r.NewStyle().Foreground(lineColor).Render(" ┃ "))

	switch entry.EntryType {
	case timelineEntryEvent:
		var eventColor lipgloss.TerminalColor
		switch entry.EventType {
		case "created":
			eventColor = t.Secondary
		case "claimed":
			eventColor = t.InProgress
		case "closed":
			eventColor = t.Closed
		case "reopened":
			eventColor = t.Open
		default:
			eventColor = t.Secondary
		}
		b.WriteString(r.NewStyle().Foreground(eventColor).Bold(true).Render(entry.Label))
		if entry.Detail != "" {
			b.WriteString(" ")
			detail := truncateRunesHelper(entry.Detail, width-22, "...")
			b.WriteString(r.NewStyle().Foreground(t.Subtext).Render(detail))
		}

	case timelineEntryCommit:
		var confColor lipgloss.TerminalColor
		if entry.Confidence >= 0.8 {
			confColor = t.Closed
		} else if entry.Confidence >= 0.5 {
			confColor = t.InProgress
		} else {
			confColor = t.Subtext
		}
		b.WriteString("├─ ")
		b.WriteString(r.NewStyle().Foreground(confColor).Bold(true).Render(entry.Label))
		b.WriteString(" ")
		b.WriteString(r.NewStyle().Foreground(confColor).Render(fmt.Sprintf("%d%%", int(entry.Confidence*100))))

		msg := strings.Split(entry.Detail, "\n")[0]
		msg = truncateRunesHelper(msg, width-28, "...")
		if msg != "" {
			b.WriteString("\n")
			b.WriteString(timestampStyle.Render(""))
			b.WriteString(r.NewStyle().Foreground(lineColor).Render(" ┃   "))
			b.WriteString(r.NewStyle().Foreground(t.Subtext).Italic(true).Render(msg))
		}

	case timelineEntrySession:
		var sessionColor lipgloss.TerminalColor
		if entry.SessionScore >= 80 {
			sessionColor = t.Primary
		} else if entry.SessionScore >= 50 {
			sessionColor = t.InProgress
		} else {
			sessionColor = t.Subtext
		}
		b.WriteString(r.NewStyle().Foreground(sessionColor).Bold(true).Render(entry.Label))
		if entry.Detail != "" {
			b.WriteString("\n")
			b.WriteString(timestampStyle.Render(""))
			b.WriteString(r.NewStyle().Foreground(lineColor).Render(" ┃   "))
			title := truncateRunesHelper(entry.Detail, width-16, "...")
			b.WriteString(r.NewStyle().Foreground(t.Subtext).Italic(true).Render(title))
		}
	}

	// The panel's Width rule wraps arbitrary overlong content. Bound each
	// already-rendered row first so its measured height remains its terminal
	// height when the enclosing panel is rendered.
	return r.NewStyle().MaxWidth(contentWidth).Render(b.String())
}

func (h *HistoryModel) renderTimelineSummary(hist *correlation.BeadHistory, width int) string {
	if hist == nil || hist.CycleTime == nil {
		return ""
	}

	t := h.theme
	r := t.Renderer
	contentWidth := max(width-4, 1)
	separator := r.NewStyle().Foreground(t.Border).Render(strings.Repeat("─", max(width-6, 0)))

	var parts []string
	if hist.CycleTime.CreateToClose != nil {
		parts = append(parts, fmt.Sprintf("Cycle: %s", formatDuration(*hist.CycleTime.CreateToClose)))
	}
	if len(hist.Commits) > 0 {
		avgConf := 0.0
		for _, commit := range hist.Commits {
			avgConf += commit.Confidence
		}
		avgConf /= float64(len(hist.Commits))
		parts = append(parts, fmt.Sprintf("%d commits (avg %d%%)", len(hist.Commits), int(avgConf*100)))
	}
	summary := truncateRunesHelper(strings.Join(parts, " │ "), contentWidth, "…")
	summary = r.NewStyle().Foreground(t.Subtext).Render(summary)
	return separator + "\n" + summary
}

// renderTimelinePanel renders the timeline visualization panel (bv-1x6o)
func (h *HistoryModel) renderTimelinePanel(width, height int) string {
	t := h.theme
	r := t.Renderer

	// Panel border style
	borderColor := t.Border
	if h.focused == historyFocusTimeline {
		borderColor = t.Primary
	}
	panelStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	// Get selected bead
	if len(h.beadIDs) == 0 || h.selectedBead < 0 || h.selectedBead >= len(h.beadIDs) {
		content := h.renderTimelineTitle(width, "") + "\n\n" +
			r.NewStyle().Foreground(t.Secondary).Render("Select a bead to view timeline")
		return panelStyle.Render(content)
	}

	beadID := h.beadIDs[h.selectedBead]
	hist := h.SelectedHistory()
	if hist == nil {
		content := h.renderTimelineTitle(width, "") + "\n\n" +
			r.NewStyle().Foreground(t.Secondary).Render("No history data")
		return panelStyle.Render(content)
	}

	layout := h.layoutTimeline(hist, width, height)
	layout.title = h.renderTimelineTitle(width, beadID)
	blocks := []string{layout.title}
	if len(layout.entries) == 0 {
		blocks = append(blocks, "", r.NewStyle().
			Foreground(t.Secondary).
			MaxWidth(max(width-4, 1)).
			Render("No events recorded"))
	} else {
		maxScroll := timelineFinalWindowStart(layout.entries, layout.entryRowBudget)
		if h.timelineScrollOffset < 0 {
			h.timelineScrollOffset = 0
		}
		if h.timelineScrollOffset > maxScroll {
			h.timelineScrollOffset = maxScroll
		}
		startIdx := h.timelineScrollOffset
		endIdx := timelineWindowEnd(layout.entries, startIdx, layout.entryRowBudget)
		blocks = append(blocks, layout.entries[startIdx:endIdx]...)

		if layout.showScroll {
			scrollInfo := fmt.Sprintf("↕ %d-%d of %d", startIdx+1, endIdx, len(layout.entries))
			scrollStyle := r.NewStyle().Foreground(t.Subtext).Italic(true)
			scrollLine := r.NewStyle().Width(8).Render("") +
				r.NewStyle().Foreground(t.Border).Render(" ┃ ") +
				scrollStyle.Render(scrollInfo)
			blocks = append(blocks, r.NewStyle().MaxWidth(max(width-4, 1)).Render(scrollLine))
		}
	}

	if layout.summary != "" {
		blocks = append(blocks, layout.summary)
	}

	return panelStyle.Render(strings.Join(blocks, "\n"))
}

// formatDuration formats a duration in a human-readable way (bv-1x6o)
func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1d"
	}
	return fmt.Sprintf("%dd", days)
}

// renderCompactTimeline generates a single-line timeline visualization (bv-1x6o)
// Example: ○──●──├──├──├──✓  5d cycle, 3 commits
func (h *HistoryModel) renderCompactTimeline(hist correlation.BeadHistory, maxWidth int) string {
	t := h.theme
	r := t.Renderer

	var markers []string
	var startTime, endTime time.Time

	// Add event markers
	if hist.Milestones.Created != nil {
		markers = append(markers, "○")
		startTime = hist.Milestones.Created.Timestamp
	}
	if hist.Milestones.Claimed != nil {
		markers = append(markers, "●")
		if startTime.IsZero() {
			startTime = hist.Milestones.Claimed.Timestamp
		}
	}

	// Add commit markers (limited to avoid overflow)
	commitCount := len(hist.Commits)
	maxCommitMarkers := 5
	if commitCount > maxCommitMarkers {
		// Show first few + ellipsis indicator
		for i := 0; i < maxCommitMarkers-1; i++ {
			markers = append(markers, "├")
		}
		markers = append(markers, "…")
	} else {
		for i := 0; i < commitCount; i++ {
			markers = append(markers, "├")
		}
	}

	// Add close marker
	if hist.Milestones.Closed != nil {
		markers = append(markers, "✓")
		endTime = hist.Milestones.Closed.Timestamp
	}

	if len(markers) == 0 {
		return r.NewStyle().Foreground(t.Subtext).Render("(no timeline data)")
	}

	// Build the timeline string
	timeline := strings.Join(markers, "──")

	// Add summary info
	var summary []string
	if hist.CycleTime != nil && hist.CycleTime.CreateToClose != nil {
		summary = append(summary, formatDuration(*hist.CycleTime.CreateToClose)+" cycle")
	}
	if commitCount > 0 {
		if commitCount == 1 {
			summary = append(summary, "1 commit")
		} else {
			summary = append(summary, fmt.Sprintf("%d commits", commitCount))
		}
	}

	result := timeline
	if len(summary) > 0 {
		result += "  " + strings.Join(summary, ", ")
	}

	// Add date range if we have both
	if !startTime.IsZero() && !endTime.IsZero() {
		dateRange := fmt.Sprintf("%s ─ %s",
			startTime.Format("Jan 2"),
			endTime.Format("Jan 2"))
		// Only add date range if we have room
		if len(result)+len(dateRange)+4 < maxWidth {
			result += "\n" + r.NewStyle().Foreground(t.Subtext).Render(dateRange)
		}
	}

	// Truncate if needed (UTF-8 safe using runewidth)
	result = truncateRunesHelper(result, maxWidth, "...")

	return result
}

// renderEmpty renders an empty state message
func (h *HistoryModel) renderEmpty(msg string) string {
	t := h.theme
	style := t.Renderer.NewStyle().
		Width(h.width).
		Height(h.height).
		Align(lipgloss.Center, lipgloss.Center).
		Foreground(t.Secondary)

	return style.Render(msg + "\n\nPress H to close")
}

// renderHeader renders the filter bar, statistics, and title (bv-y5sx)
func (h *HistoryModel) renderHeader() string {
	t := h.theme

	titleStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Padding(0, 1)

	// Show view mode indicator with icons (bv-tl3n, bv-kvlx)
	// Icons: ◉ for git-centric (commits), ◈ for bead-centric (beads)
	var modeIcon, modeLabel string
	if h.viewMode == historyModeGit {
		modeIcon = "◉"
		modeLabel = "Git"
	} else {
		modeIcon = "◈"
		modeLabel = "Beads"
	}

	// Check for transition flash effect (bv-kvlx)
	// Show highlight for 150ms after mode toggle.
	// Note: Since Bubble Tea only re-renders on Update, the flash shows during the
	// render immediately after toggle, and clears on subsequent renders. If no user
	// input occurs, the flash persists until next action - this is acceptable TUI behavior.
	isTransitioning := !h.modeChangedAt.IsZero() && time.Since(h.modeChangedAt) <= 150*time.Millisecond

	modeStyle := t.Renderer.NewStyle().
		Bold(true).
		Padding(0, 1)

	if isTransitioning {
		// Flash effect: bright background with contrasting text
		modeStyle = modeStyle.
			Background(t.Primary).
			Foreground(lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#1a1a2e"})
	} else {
		// Normal mode: just colored text
		modeStyle = modeStyle.Foreground(t.InProgress)
	}

	modeIndicator := fmt.Sprintf("%s %s", modeIcon, modeLabel)
	title := titleStyle.Render("HISTORY") + modeStyle.Render(modeIndicator)

	// Search input or close hint (bv-nkrj)
	var rightContent string
	if h.searchActive {
		// Show search input
		searchStyle := t.Renderer.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Padding(0, 1)

		modeLabel := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Render(fmt.Sprintf("[%s] ", h.GetSearchModeName()))

		inputView := h.searchInput.View()
		searchBox := searchStyle.Render(modeLabel + inputView)

		escHint := t.Renderer.NewStyle().
			Foreground(t.Muted).
			Padding(0, 1).
			Render("[Esc] cancel")

		rightContent = searchBox + escHint
	} else {
		// Show close hint and search hint
		rightContent = t.Renderer.NewStyle().
			Foreground(t.Muted).
			Padding(0, 1).
			Render("[/] search  [H] close")
	}

	// Combine title line with spacing
	titleLineSpacerWidth := h.width - lipgloss.Width(title) - lipgloss.Width(rightContent)
	if titleLineSpacerWidth < 1 {
		titleLineSpacerWidth = 1
	}
	titleLineSpacer := strings.Repeat(" ", titleLineSpacerWidth)
	titleLine := lipgloss.JoinHorizontal(lipgloss.Top, title, titleLineSpacer, rightContent)

	// Build stats line (bv-y5sx)
	statsLine := h.renderStatsLine()

	// Build filter status line (bv-y5sx)
	filterLine := h.renderFilterLine()

	// Add separator line
	separatorWidth := h.width
	if separatorWidth < 1 {
		separatorWidth = 1
	}
	separator := t.Renderer.NewStyle().
		Foreground(t.Muted).
		Width(h.width).
		Render(strings.Repeat("─", separatorWidth))

	return lipgloss.JoinVertical(lipgloss.Left, titleLine, statsLine, filterLine, separator)
}

// renderStatsLine renders the statistics badges line (bv-y5sx)
func (h *HistoryModel) renderStatsLine() string {
	if h.report == nil {
		return ""
	}

	t := h.theme
	stats := h.report.Stats

	// Badge style - subtle background with contrasting text
	badgeStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Padding(0, 1)

	// Value style - highlighted
	valueStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	// Build stats badges
	var badges []string

	// Beads with commits
	beadsBadge := badgeStyle.Render(valueStyle.Render(fmt.Sprintf("%d", stats.BeadsWithCommits)) + " beads")
	badges = append(badges, beadsBadge)

	// Total commits
	commitsBadge := badgeStyle.Render(valueStyle.Render(fmt.Sprintf("%d", stats.TotalCommits)) + " commits")
	badges = append(badges, commitsBadge)

	// Unique authors
	authorsBadge := badgeStyle.Render(valueStyle.Render(fmt.Sprintf("%d", stats.UniqueAuthors)) + " authors")
	badges = append(badges, authorsBadge)

	// Average cycle time (if available)
	if stats.AvgCycleTimeDays != nil {
		cycleStr := formatCycleTime(*stats.AvgCycleTimeDays)
		cycleBadge := badgeStyle.Render("⌀ " + valueStyle.Render(cycleStr) + " cycle")
		badges = append(badges, cycleBadge)
	}

	// Commits per bead
	if stats.AvgCommitsPerBead > 0 {
		cpdBadge := badgeStyle.Render(valueStyle.Render(fmt.Sprintf("%.1f", stats.AvgCommitsPerBead)) + " commits/bead")
		badges = append(badges, cpdBadge)
	}

	// Join with bullet separator
	separator := t.Renderer.NewStyle().Foreground(t.Muted).Render(" • ")
	return strings.Join(badges, separator)
}

// renderFilterLine renders the current filter status (bv-y5sx)
func (h *HistoryModel) renderFilterLine() string {
	t := h.theme

	filterStyle := t.Renderer.NewStyle().
		Foreground(t.Muted).
		Italic(true).
		Padding(0, 1)

	activeFilterStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Padding(0, 1)

	var parts []string

	// Build active filters list
	var activeFilters []string
	if h.authorFilter != "" {
		activeFilters = append(activeFilters, fmt.Sprintf("@%s", h.authorFilter))
	}
	if h.minConfidence > 0 {
		activeFilters = append(activeFilters, fmt.Sprintf("≥%.0f%% conf", h.minConfidence*100))
	}
	if query := strings.TrimSpace(h.searchInput.Value()); query != "" {
		activeFilters = append(activeFilters, fmt.Sprintf("\"%s\"", query))
	}

	// Show filter status
	if len(activeFilters) > 0 {
		parts = append(parts, activeFilterStyle.Render("Filter: "+strings.Join(activeFilters, ", ")))
	}

	// Show count based on mode
	if h.viewMode == historyModeGit {
		commits := h.GetFilteredCommitList()
		totalCommits := len(h.commitList)
		if len(commits) != totalCommits {
			parts = append(parts, filterStyle.Render(fmt.Sprintf("Showing %d/%d commits", len(commits), totalCommits)))
		} else {
			parts = append(parts, filterStyle.Render(fmt.Sprintf("Showing all %d commits", totalCommits)))
		}
	} else {
		totalBeads := 0
		if h.report != nil {
			totalBeads = len(h.report.Histories)
		}
		if len(h.histories) != totalBeads {
			parts = append(parts, filterStyle.Render(fmt.Sprintf("Showing %d/%d beads", len(h.histories), totalBeads)))
		} else {
			parts = append(parts, filterStyle.Render(fmt.Sprintf("Showing all %d beads with commits", len(h.histories))))
		}
	}

	return strings.Join(parts, "  │  ")
}

// formatCycleTime formats cycle time in days to a human-readable string
func formatCycleTime(days float64) string {
	if days < 1 {
		hours := days * 24
		if hours < 1 {
			return fmt.Sprintf("%.0fm", hours*60)
		}
		return fmt.Sprintf("%.1fh", hours)
	}
	if days < 7 {
		return fmt.Sprintf("%.1fd", days)
	}
	weeks := days / 7
	return fmt.Sprintf("%.1fw", weeks)
}

// renderListPanel renders the left panel with bead list
func (h *HistoryModel) renderListPanel(width, height int) string {
	t := h.theme

	// Panel border style based on focus
	borderColor := t.Muted
	if h.focused == historyFocusList {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2). // Account for border
		Height(height - 2)

	// Column header
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Width(width - 4)
	header := headerStyle.Render("BEADS WITH HISTORY")

	// Build list content
	var lines []string
	lines = append(lines, header)
	sepWidth := width - 4
	if sepWidth < 1 {
		sepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", sepWidth))

	visibleItems := height - 5 // Account for header, separator, border
	if visibleItems < 1 {
		visibleItems = 1
	}

	for i := h.scrollOffset; i < len(h.histories) && i < h.scrollOffset+visibleItems; i++ {
		hist := h.histories[i]
		line := h.renderBeadLine(i, hist, width-4)
		lines = append(lines, line)
	}

	// Pad with empty lines if needed
	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderBeadLine renders a single bead in the list
func (h *HistoryModel) renderBeadLine(idx int, hist correlation.BeadHistory, width int) string {
	t := h.theme

	selected := idx == h.selectedBead

	// Indicator
	indicator := "  "
	if selected {
		indicator = "▸ "
	}

	// Status icon
	statusIcon := "○"
	switch hist.Status {
	case "closed":
		statusIcon = "✓"
	case "in_progress":
		statusIcon = "●"
	}

	// Commit count
	commitCount := fmt.Sprintf("%d commits", len(hist.Commits))

	// Event count badge (bv-7k8p) - shows lifecycle events if any
	eventBadge := ""
	if len(hist.Events) > 0 {
		eventBadge = renderCompactEventBadge(len(hist.Events), t)
	}

	// Calculate space for event badge
	eventBadgeWidth := lipgloss.Width(eventBadge)
	if eventBadgeWidth > 0 {
		eventBadgeWidth += 1 // Space before badge
	}

	// Truncate title
	maxTitleLen := width - len(indicator) - len(statusIcon) - len(commitCount) - eventBadgeWidth - 6
	if maxTitleLen < 10 {
		maxTitleLen = 10
	}
	title := truncateRunesHelper(hist.Title, maxTitleLen, "…")

	// Build line
	idStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Width(12)
	titleStyle := t.Renderer.NewStyle().Width(maxTitleLen)
	countStyle := t.Renderer.NewStyle().Foreground(t.Muted).Align(lipgloss.Right)

	if selected && h.focused == historyFocusList {
		idStyle = idStyle.Bold(true).Foreground(t.Primary)
		titleStyle = titleStyle.Bold(true)
	}

	// Include event badge if present
	countPart := countStyle.Render(commitCount)
	if eventBadge != "" {
		countPart = countPart + " " + eventBadge
	}

	line := fmt.Sprintf("%s%s %s %s %s",
		indicator,
		statusIcon,
		idStyle.Render(hist.BeadID),
		titleStyle.Render(title),
		countPart,
	)

	return line
}

// renderFileTreePanel renders the file tree panel (bv-190l)
func (h *HistoryModel) renderFileTreePanel(width, height int) string {
	t := h.theme

	// Panel border style based on focus
	borderColor := t.Muted
	if h.fileTreeFocus {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	// Header with filter indicator
	headerText := "FILES"
	if h.fileFilter != "" {
		headerText = fmt.Sprintf("FILES [%s]", truncate(h.fileFilter, 15))
	}
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Width(width - 4)
	header := headerStyle.Render(headerText)

	var lines []string
	lines = append(lines, header)
	sepWidth := width - 4
	if sepWidth < 1 {
		sepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", sepWidth))

	// Build flat file list if needed
	if len(h.flatFileList) == 0 && len(h.fileTree) > 0 {
		h.rebuildFlatFileList()
	}

	visibleItems := height - 5
	if visibleItems < 1 {
		visibleItems = 1
	}

	// Adjust scroll to keep selection visible
	if h.selectedFileIdx < h.fileTreeScroll {
		h.fileTreeScroll = h.selectedFileIdx
	}
	if h.selectedFileIdx >= h.fileTreeScroll+visibleItems {
		h.fileTreeScroll = h.selectedFileIdx - visibleItems + 1
	}

	for i := h.fileTreeScroll; i < len(h.flatFileList) && i < h.fileTreeScroll+visibleItems; i++ {
		node := h.flatFileList[i]
		line := h.renderFileTreeLine(i, node, width-4)
		lines = append(lines, line)
	}

	// Pad with empty lines
	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderFileTreeLine renders a single file tree node (bv-190l)
func (h *HistoryModel) renderFileTreeLine(idx int, node *FileTreeNode, width int) string {
	t := h.theme

	selected := idx == h.selectedFileIdx
	isFiltered := node.Path == h.fileFilter

	// Indentation
	indent := strings.Repeat("  ", node.Level)

	// Indicator
	indicator := "  "
	if selected && h.fileTreeFocus {
		indicator = "▸ "
	}

	// Expand/collapse icon for directories
	icon := "  "
	if node.IsDir {
		if node.Expanded {
			icon = "▼ "
		} else {
			icon = "▶ "
		}
	} else {
		icon = "  "
	}

	// Change count
	countStr := fmt.Sprintf("(%d)", node.ChangeCount)

	// Calculate max name length
	maxNameLen := width - len(indent) - len(indicator) - len(icon) - len(countStr) - 2
	if maxNameLen < 5 {
		maxNameLen = 5
	}

	name := truncateRunesHelper(node.Name, maxNameLen, "…")

	// Styling
	nameStyle := t.Renderer.NewStyle()
	countStyle := t.Renderer.NewStyle().Foreground(t.Muted)

	if node.IsDir {
		nameStyle = nameStyle.Foreground(t.Secondary)
	}
	if isFiltered {
		nameStyle = nameStyle.Bold(true).Foreground(t.Closed) // Green for active filter
	}
	if selected && h.fileTreeFocus {
		nameStyle = nameStyle.Bold(true)
		if !isFiltered {
			nameStyle = nameStyle.Foreground(t.Primary)
		}
	}

	line := fmt.Sprintf("%s%s%s%s %s",
		indent,
		indicator,
		icon,
		nameStyle.Render(name),
		countStyle.Render(countStr),
	)

	return line
}

// renderDetailPanel renders the right panel with commit details (bv-9fk1 enhanced)
func (h *HistoryModel) renderDetailPanel(width, height int) string {
	t := h.theme

	// Panel border style based on focus
	borderColor := t.Muted
	if h.focused == historyFocusDetail {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	hist := h.SelectedHistory()
	if hist == nil {
		return panelStyle.Render("No bead selected")
	}

	// Header
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary)
	header := headerStyle.Render("COMMIT DETAILS")

	// Bead info with status indicator
	statusIcon := "○"
	switch hist.Status {
	case "closed":
		statusIcon = "✓"
	case "in_progress":
		statusIcon = "●"
	}
	beadInfo := fmt.Sprintf("%s %s: %s", statusIcon, hist.BeadID, hist.Title)
	if width > 10 {
		beadInfo = truncateRunesHelper(beadInfo, width-6, "…")
	} else {
		beadInfo = truncateRunesHelper(beadInfo, 5, "…")
	}
	beadInfoStyle := t.Renderer.NewStyle().Foreground(t.Secondary)

	var lines []string
	lines = append(lines, header)
	lines = append(lines, beadInfoStyle.Render(beadInfo))
	detailSepWidth := width - 4
	if detailSepWidth < 1 {
		detailSepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", detailSepWidth))

	// === LIFECYCLE EVENTS SECTION (bv-7k8p) ===
	// Show compact timeline of lifecycle events if available
	if len(hist.Events) > 0 {
		// Limit events section to ~4 lines to leave room for commits
		maxEventLines := 5
		eventLines := h.renderEventsSection(hist.Events, width-4, maxEventLines)
		lines = append(lines, eventLines...)
		lines = append(lines, "") // Spacer before commits
	}

	// Calculate aggregate stats for footer (bv-9fk1)
	var totalFiles, totalAdd, totalDel int
	var totalConf float64
	uniqueFiles := make(map[string]bool)
	for _, commit := range hist.Commits {
		totalConf += commit.Confidence
		for _, f := range commit.Files {
			uniqueFiles[f.Path] = true
			totalAdd += f.Insertions
			totalDel += f.Deletions
		}
	}
	totalFiles = len(uniqueFiles)
	avgConf := 0.0
	if len(hist.Commits) > 0 {
		avgConf = totalConf / float64(len(hist.Commits))
	}

	// Reserve space for footer (3 lines: separator + stats + hint)
	footerHeight := 3
	contentHeight := height - 2 - footerHeight - 3 // -3 for header lines

	// Render commits
	for i, commit := range hist.Commits {
		isSelected := i == h.selectedCommit && h.focused == historyFocusDetail
		commitLines := h.renderCommitDetail(commit, width-4, isSelected)
		lines = append(lines, commitLines...)
		if i < len(hist.Commits)-1 {
			lines = append(lines, "") // Spacer between commits
		}
	}

	// Pad content to push footer to bottom
	for len(lines) < contentHeight+3 { // +3 for header lines
		lines = append(lines, "")
	}

	// Truncate content if too long (before adding footer)
	if len(lines) > contentHeight+3 {
		lines = lines[:contentHeight+3]
	}

	// === STATS FOOTER (bv-9fk1) ===
	lines = append(lines, strings.Repeat("─", detailSepWidth))

	// Stats line
	statsStyle := t.Renderer.NewStyle().Foreground(t.Muted)
	confStyle := t.Renderer.NewStyle()
	switch {
	case avgConf >= 0.8:
		confStyle = confStyle.Foreground(t.Open)
	case avgConf >= 0.5:
		confStyle = confStyle.Foreground(t.Secondary)
	default:
		confStyle = confStyle.Foreground(t.Muted)
	}

	var statsItems []string
	statsItems = append(statsItems, fmt.Sprintf("%d commits", len(hist.Commits)))
	statsItems = append(statsItems, fmt.Sprintf("%d files", totalFiles))
	if totalAdd > 0 || totalDel > 0 {
		addStr := t.Renderer.NewStyle().Foreground(t.Open).Render(fmt.Sprintf("+%d", totalAdd))
		delStr := t.Renderer.NewStyle().Foreground(t.Closed).Render(fmt.Sprintf("-%d", totalDel))
		statsItems = append(statsItems, addStr+"/"+delStr)
	}
	statsItems = append(statsItems, confStyle.Render(fmt.Sprintf("%.0f%% avg", avgConf*100)))

	statsLine := statsStyle.Render(strings.Join(statsItems, " • "))
	lines = append(lines, statsLine)

	// Navigation hint (bv-xf4p: added o and g keys)
	hintStyle := t.Renderer.NewStyle().Foreground(ColorFooterHint).Italic(true)
	lines = append(lines, hintStyle.Render("J/K:nav  y:copy  o:open  g:graph"))

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderCommitDetail renders details for a single commit (bv-9fk1 enhanced)
func (h *HistoryModel) renderCommitDetail(commit correlation.CorrelatedCommit, width int, selected bool) []string {
	t := h.theme
	var lines []string

	// Selection indicator
	indicator := "  "
	if selected {
		indicator = "▸ "
	}

	// === COMMIT HEADER ===
	// Type icon + SHA + relative time
	typeIcon := commitTypeIndicator(commit.Message)
	if typeIcon != "" {
		typeIcon += " "
	}

	shaStyle := t.Renderer.NewStyle().Foreground(t.Primary)
	if selected {
		shaStyle = shaStyle.Bold(true)
	}

	relTime := relativeTime(commit.Timestamp)
	relTimeStyle := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true)

	// Header line: [indicator] [icon] SHA (relative time)
	headerLine := fmt.Sprintf("%s%s%s %s",
		indicator,
		typeIcon,
		shaStyle.Render(commit.ShortSHA),
		relTimeStyle.Render("("+relTime+")"),
	)
	lines = append(lines, headerLine)

	// === AUTHOR LINE ===
	// [Initials] Author Name • absolute date
	initials := authorInitials(commit.Author)
	initialsStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground()).
		Background(t.Muted).
		Padding(0, 1).
		Bold(true)
	authorStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	dateStr := commit.Timestamp.Format("2006-01-02 15:04")

	authorLine := fmt.Sprintf("    %s %s • %s",
		initialsStyle.Render(initials),
		authorStyle.Render(commit.Author),
		dateStr,
	)
	// Use lipgloss.Width for accurate visual width (handles ANSI escape codes)
	if width > 10 && lipgloss.Width(authorLine) > width-2 {
		// Truncate author name if needed
		maxAuthor := width - 30
		if maxAuthor < 10 {
			maxAuthor = 10
		}
		authorName := truncateRunesHelper(commit.Author, maxAuthor, "…")
		authorLine = fmt.Sprintf("    %s %s • %s",
			initialsStyle.Render(initials),
			authorStyle.Render(authorName),
			dateStr,
		)
	}
	lines = append(lines, authorLine)

	// === MESSAGE ===
	// Parse conventional commit for better display
	cc := parseConventionalCommit(commit.Message)

	if cc.IsConventional {
		// Show type badge + subject
		typeBadgeStyle := t.Renderer.NewStyle().
			Foreground(t.Primary).
			Bold(true)
		var scopeStr string
		if cc.Scope != "" {
			scopeStr = "(" + cc.Scope + ")"
		}
		breakingStr := ""
		if cc.Breaking {
			breakingStr = t.Renderer.NewStyle().Foreground(t.Closed).Bold(true).Render("!")
		}
		typeLine := fmt.Sprintf("    %s%s%s: %s",
			typeBadgeStyle.Render(cc.Type),
			scopeStr,
			breakingStr,
			truncate(cc.Subject, width-len(cc.Type)-len(scopeStr)-10),
		)
		lines = append(lines, typeLine)
	} else {
		// Non-conventional: just show the message
		msgLine := fmt.Sprintf("    %s", truncate(cc.Subject, width-6))
		lines = append(lines, msgLine)
	}

	// === CONFIDENCE & METHOD ===
	confStyle := t.Renderer.NewStyle()
	switch {
	case commit.Confidence >= 0.8:
		confStyle = confStyle.Foreground(t.Open) // Green
	case commit.Confidence >= 0.5:
		confStyle = confStyle.Foreground(t.Secondary) // Yellow/neutral
	default:
		confStyle = confStyle.Foreground(t.Muted) // Gray
	}

	methodStr := methodLabel(commit.Method)
	confLine := fmt.Sprintf("    %s %s",
		confStyle.Render(fmt.Sprintf("%.0f%% confidence", commit.Confidence*100)),
		t.Renderer.NewStyle().Foreground(t.Muted).Render(methodStr),
	)
	lines = append(lines, confLine)

	// === FILE CHANGES ===
	if len(commit.Files) > 0 {
		// File summary header
		var totalAdd, totalDel int
		for _, f := range commit.Files {
			totalAdd += f.Insertions
			totalDel += f.Deletions
		}

		fileSummary := fmt.Sprintf("    %d file(s)",
			len(commit.Files),
		)
		if totalAdd > 0 || totalDel > 0 {
			addStyle := t.Renderer.NewStyle().Foreground(t.Open)
			delStyle := t.Renderer.NewStyle().Foreground(t.Closed)
			fileSummary += fmt.Sprintf(" %s %s",
				addStyle.Render(fmt.Sprintf("+%d", totalAdd)),
				delStyle.Render(fmt.Sprintf("-%d", totalDel)),
			)
		}
		lines = append(lines, fileSummary)

		// Group files by directory and show (max 5 files)
		groups := groupFilesByDirectory(commit.Files)
		fileCount := 0
		maxFiles := 5

		for _, group := range groups {
			if fileCount >= maxFiles {
				moreCount := len(commit.Files) - fileCount
				moreStyle := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true)
				lines = append(lines, moreStyle.Render(fmt.Sprintf("      +%d more files...", moreCount)))
				break
			}

			for _, f := range group.Files {
				if fileCount >= maxFiles {
					break
				}

				// Get just filename from path
				filename := f.Path
				lastSlash := strings.LastIndex(f.Path, "/")
				if lastSlash >= 0 && lastSlash < len(f.Path)-1 {
					filename = f.Path[lastSlash+1:]
				}

				// Action icon and color
				actionIcon := fileActionIcon(f.Action)
				actionColor := fileActionColor(f.Action, t)
				actionStyle := t.Renderer.NewStyle().Foreground(actionColor)

				// +/- stats if available
				statsStr := ""
				if f.Insertions > 0 || f.Deletions > 0 {
					addStr := t.Renderer.NewStyle().Foreground(t.Open).Render(fmt.Sprintf("+%d", f.Insertions))
					delStr := t.Renderer.NewStyle().Foreground(t.Closed).Render(fmt.Sprintf("-%d", f.Deletions))
					statsStr = fmt.Sprintf(" %s/%s", addStr, delStr)
				}

				fileLine := fmt.Sprintf("      %s %s%s",
					actionStyle.Render(actionIcon),
					truncate(filename, width-15),
					statsStr,
				)
				lines = append(lines, fileLine)
				fileCount++
			}
		}
	}

	return lines
}

// Helper functions

func methodLabel(method correlation.CorrelationMethod) string {
	switch method {
	case correlation.MethodCoCommitted:
		return "(co-committed)"
	case correlation.MethodExplicitID:
		return "(explicit ID)"
	case correlation.MethodTemporalAuthor:
		return "(temporal)"
	default:
		return ""
	}
}

// Commit detail enhancement helpers (bv-9fk1)

// authorInitials extracts initials from an author name (e.g., "John Doe" -> "JD")
func authorInitials(name string) string {
	if name == "" {
		return "??"
	}
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "??"
	}
	if len(parts) == 1 {
		// Single name - take first two runes (Unicode-safe)
		runes := []rune(parts[0])
		if len(runes) >= 2 {
			return strings.ToUpper(string(runes[:2]))
		}
		return strings.ToUpper(string(runes))
	}
	// Multi-part name - first rune of first and last parts
	first := string([]rune(parts[0])[0])
	last := string([]rune(parts[len(parts)-1])[0])
	return strings.ToUpper(first + last)
}

// relativeTime formats a time as a relative string (e.g., "2d ago", "3h ago")
func relativeTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	if diff < 0 {
		return "in future"
	}

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		return fmt.Sprintf("%dm ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		return fmt.Sprintf("%dh ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	case diff < 30*24*time.Hour:
		weeks := int(diff.Hours() / 24 / 7)
		return fmt.Sprintf("%dw ago", weeks)
	case diff < 365*24*time.Hour:
		months := int(diff.Hours() / 24 / 30)
		return fmt.Sprintf("%dmo ago", months)
	default:
		years := int(diff.Hours() / 24 / 365)
		return fmt.Sprintf("%dy ago", years)
	}
}

// conventionalCommit holds parsed conventional commit info
type conventionalCommit struct {
	Type           string // feat, fix, docs, etc.
	Scope          string // optional scope in parentheses
	Breaking       bool   // has ! after type/scope
	Subject        string // the description after the colon
	Body           string // everything after first line
	IsConventional bool   // true if successfully parsed
}

// parseConventionalCommit parses a conventional commit message
func parseConventionalCommit(msg string) conventionalCommit {
	result := conventionalCommit{IsConventional: false}

	lines := strings.SplitN(msg, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		result.Body = strings.TrimSpace(lines[1])
	}

	// Match pattern: type(scope)!: description or type!: description or type: description
	// Common types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert
	patterns := []string{
		"feat", "fix", "docs", "style", "refactor", "perf", "test",
		"build", "ci", "chore", "revert", "wip",
	}

	for _, prefix := range patterns {
		if strings.HasPrefix(strings.ToLower(firstLine), prefix) {
			rest := firstLine[len(prefix):]

			// Check for scope
			if strings.HasPrefix(rest, "(") {
				endParen := strings.Index(rest, ")")
				if endParen > 0 {
					result.Scope = rest[1:endParen]
					rest = rest[endParen+1:]
				}
			}

			// Check for breaking change indicator
			if strings.HasPrefix(rest, "!") {
				result.Breaking = true
				rest = rest[1:]
			}

			// Check for colon
			if strings.HasPrefix(rest, ":") {
				result.Type = prefix
				result.Subject = strings.TrimSpace(rest[1:])
				result.IsConventional = true
				return result
			}
		}
	}

	// Not conventional - use whole first line as subject
	result.Subject = firstLine
	return result
}

// commitTypeIndicator returns an icon/badge for commit type
func commitTypeIndicator(msg string) string {
	lowerMsg := strings.ToLower(msg)

	// Check for merge commit
	if strings.HasPrefix(lowerMsg, "merge ") {
		return "⊕" // merge symbol
	}

	// Check for revert
	if strings.HasPrefix(lowerMsg, "revert ") {
		return "↩" // revert symbol
	}

	// Check conventional commit type
	cc := parseConventionalCommit(msg)
	if cc.IsConventional {
		switch cc.Type {
		case "feat":
			return "✨" // sparkles for feature
		case "fix":
			return "🐛" // bug for fix
		case "docs":
			return "📝" // docs
		case "refactor":
			return "♻" // refactor
		case "perf":
			return "⚡" // performance
		case "test":
			return "🧪" // test
		case "chore":
			return "🔧" // chore
		case "ci":
			return "🔄" // CI
		case "build":
			return "📦" // build
		case "style":
			return "💄" // style
		}
	}

	return "" // no special indicator
}

// filesByDirectory groups files by their parent directory
type fileGroup struct {
	Dir   string
	Files []correlation.FileChange
}

func groupFilesByDirectory(files []correlation.FileChange) []fileGroup {
	dirMap := make(map[string][]correlation.FileChange)
	var dirOrder []string

	for _, f := range files {
		dir := "."
		lastSlash := strings.LastIndex(f.Path, "/")
		if lastSlash >= 0 {
			dir = f.Path[:lastSlash]
		}

		if _, exists := dirMap[dir]; !exists {
			dirOrder = append(dirOrder, dir)
		}
		dirMap[dir] = append(dirMap[dir], f)
	}

	var groups []fileGroup
	for _, dir := range dirOrder {
		groups = append(groups, fileGroup{
			Dir:   dir,
			Files: dirMap[dir],
		})
	}
	return groups
}

// fileActionColor returns the appropriate theme color for a file action
func fileActionColor(action string, t Theme) lipgloss.TerminalColor {
	switch action {
	case "A":
		return t.Open // green for added
	case "D":
		return t.Closed // red for deleted
	case "M":
		return t.InProgress // yellow/orange for modified
	case "R":
		return t.Secondary // neutral for renamed
	default:
		return t.Muted
	}
}

// fileActionIcon returns an icon for file action
func fileActionIcon(action string) string {
	switch action {
	case "A":
		return "+"
	case "D":
		return "-"
	case "M":
		return "~"
	case "R":
		return "→"
	default:
		return "?"
	}
}

// === Lifecycle Event Helpers (bv-7k8p) ===

// eventTypeIcon returns an icon for a lifecycle event type
func eventTypeIcon(et correlation.EventType) string {
	switch et {
	case correlation.EventCreated:
		return "🆕"
	case correlation.EventClaimed:
		return "👤"
	case correlation.EventClosed:
		return "✓"
	case correlation.EventReopened:
		return "↺"
	case correlation.EventModified:
		return "✎"
	default:
		return "•"
	}
}

// eventTypeColor returns the appropriate theme color for an event type
func eventTypeColor(et correlation.EventType, t Theme) lipgloss.TerminalColor {
	switch et {
	case correlation.EventCreated:
		return t.Primary // new items get primary highlight
	case correlation.EventClaimed:
		return t.InProgress // claimed = in progress
	case correlation.EventClosed:
		return t.Open // closed = success/green
	case correlation.EventReopened:
		return t.Secondary // reopened = warning
	case correlation.EventModified:
		return t.Muted // modifications are low-key
	default:
		return t.Muted
	}
}

// eventTypeLabel returns a human-readable label for an event type
func eventTypeLabel(et correlation.EventType) string {
	switch et {
	case correlation.EventCreated:
		return "Created"
	case correlation.EventClaimed:
		return "Claimed"
	case correlation.EventClosed:
		return "Closed"
	case correlation.EventReopened:
		return "Reopened"
	case correlation.EventModified:
		return "Modified"
	default:
		return string(et)
	}
}

// renderEventsSection renders a compact timeline of lifecycle events (bv-7k8p)
// Returns at most maxLines lines total (header + events + optional "more" line)
func (h *HistoryModel) renderEventsSection(events []correlation.BeadEvent, width int, maxLines int) []string {
	if len(events) == 0 {
		return nil
	}

	t := h.theme
	var lines []string

	// Section header (takes 1 line)
	headerStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Bold(true)
	lines = append(lines, headerStyle.Render(fmt.Sprintf("LIFECYCLE (%d)", len(events))))

	// Timeline style
	timeStyle := t.Renderer.NewStyle().Foreground(t.Muted).Width(8)
	authorStyle := t.Renderer.NewStyle().Foreground(t.Secondary)

	// Calculate how many events we can show:
	// - 1 line for header
	// - If more events exist than we can show, reserve 1 line for "+N more"
	// - Remaining lines are for events
	availableForEvents := maxLines - 1 // subtract header
	needsMoreLine := len(events) > availableForEvents
	if needsMoreLine {
		availableForEvents-- // reserve line for "+N more"
	}

	// Show most recent events first (reverse chronological for timeline)
	displayed := 0
	for i := len(events) - 1; i >= 0 && displayed < availableForEvents; i-- {
		event := events[i]

		// Event icon with color
		icon := eventTypeIcon(event.EventType)
		iconColor := eventTypeColor(event.EventType, t)
		iconStyle := t.Renderer.NewStyle().Foreground(iconColor)
		coloredIcon := iconStyle.Render(icon)

		// Relative time
		timeStr := relativeTime(event.Timestamp)
		if len(timeStr) > 7 {
			timeStr = timeStr[:7]
		}

		// Author initials
		initials := authorInitials(event.Author)

		// Build event line: "│ ✓ 2d ago JD"
		// Use unicode box drawing for timeline
		connector := "│"
		if i == 0 {
			connector = "└" // Last event (first chronologically)
		}

		eventLine := fmt.Sprintf("%s %s %s %s",
			connector,
			coloredIcon,
			timeStyle.Render(timeStr),
			authorStyle.Render(initials),
		)

		// Truncate if needed
		if lipgloss.Width(eventLine) > width-2 {
			// Simplified version without author
			eventLine = fmt.Sprintf("%s %s %s",
				connector,
				coloredIcon,
				timeStyle.Render(timeStr),
			)
		}

		lines = append(lines, eventLine)
		displayed++
	}

	// Show "+N more" if we couldn't display all events
	if needsMoreLine {
		remaining := len(events) - displayed
		moreStyle := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true)
		lines = append(lines, moreStyle.Render(fmt.Sprintf("  +%d more", remaining)))
	}

	return lines
}

// renderCompactEventBadge renders a compact event count badge for list items (bv-7k8p)
func renderCompactEventBadge(eventCount int, t Theme) string {
	if eventCount == 0 {
		return ""
	}

	badgeStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary)

	return badgeStyle.Render(fmt.Sprintf("⚡%d", eventCount))
}

// Git Mode rendering functions (bv-tl3n)

// renderGitCommitListPanel renders the left panel with commit list in git mode
func (h *HistoryModel) renderGitCommitListPanel(width, height int) string {
	t := h.theme

	// Panel border style based on focus
	borderColor := t.Muted
	if h.focused == historyFocusList {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	// Column header
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Width(width - 4)
	header := headerStyle.Render("COMMITS")

	// Build list content
	var lines []string
	lines = append(lines, header)
	sepWidth := width - 4
	if sepWidth < 1 {
		sepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", sepWidth))

	visibleItems := height - 5
	if visibleItems < 1 {
		visibleItems = 1
	}

	// Use filtered list if search is active (bv-nkrj)
	commits := h.GetFilteredCommitList()
	for i := h.gitScrollOffset; i < len(commits) && i < h.gitScrollOffset+visibleItems; i++ {
		commit := commits[i]
		line := h.renderGitCommitLine(i, commit, width-4)
		lines = append(lines, line)
	}

	// Pad with empty lines if needed
	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderGitCommitLine renders a single commit in git mode list
func (h *HistoryModel) renderGitCommitLine(idx int, commit CommitListEntry, width int) string {
	t := h.theme

	selected := idx == h.selectedGitCommit

	// Indicator
	indicator := "  "
	if selected {
		indicator = "▸ "
	}

	// Bead count badge
	beadCount := fmt.Sprintf("[%d]", len(commit.BeadIDs))

	// Truncate message
	maxMsgLen := width - len(indicator) - len(commit.ShortSHA) - len(beadCount) - 6
	if maxMsgLen < 10 {
		maxMsgLen = 10
	}
	msg := truncateRunesHelper(commit.Message, maxMsgLen, "…")

	// Build line
	shaStyle := t.Renderer.NewStyle().Foreground(t.Primary)
	msgStyle := t.Renderer.NewStyle()
	countStyle := t.Renderer.NewStyle().Foreground(t.Secondary)

	if selected && h.focused == historyFocusList {
		shaStyle = shaStyle.Bold(true)
		msgStyle = msgStyle.Bold(true)
	}

	line := fmt.Sprintf("%s%s %s %s",
		indicator,
		shaStyle.Render(commit.ShortSHA),
		msgStyle.Render(msg),
		countStyle.Render(beadCount),
	)

	return line
}

// renderGitDetailPanel renders the right panel with related beads and commit details in git mode
func (h *HistoryModel) renderGitDetailPanel(width, height int) string {
	t := h.theme

	// Panel border style based on focus
	borderColor := t.Muted
	if h.focused == historyFocusDetail {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	commit := h.SelectedGitCommit()
	if commit == nil {
		return panelStyle.Render("No commit selected")
	}

	var lines []string

	// Header: Related Beads
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary)
	lines = append(lines, headerStyle.Render("RELATED BEADS"))

	detailSepWidth := width - 4
	if detailSepWidth < 1 {
		detailSepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", detailSepWidth))

	// List related beads
	for i, beadID := range commit.BeadIDs {
		isSelected := i == h.selectedRelatedBead && h.focused == historyFocusDetail

		indicator := "  "
		if isSelected {
			indicator = "▸ "
		}

		// Get bead info from report
		beadStyle := t.Renderer.NewStyle()
		statusIcon := "○"
		title := beadID

		if h.report != nil {
			if hist, ok := h.report.Histories[beadID]; ok {
				title = hist.Title
				switch hist.Status {
				case "closed":
					statusIcon = "✓"
				case "in_progress":
					statusIcon = "●"
				}
			}
		}

		if isSelected {
			beadStyle = beadStyle.Bold(true).Foreground(t.Primary)
		}

		// Truncate title
		maxLen := width - 8
		if maxLen < 10 {
			maxLen = 10
		}
		title = truncateRunesHelper(title, maxLen, "…")

		beadLine := fmt.Sprintf("%s%s %s %s", indicator, statusIcon, beadID, beadStyle.Render(title))
		lines = append(lines, beadLine)
	}

	// Add separator before commit details
	lines = append(lines, "")
	lines = append(lines, strings.Repeat("─", detailSepWidth))
	lines = append(lines, headerStyle.Render("COMMIT DETAILS"))
	lines = append(lines, strings.Repeat("─", detailSepWidth))

	// Commit details
	shaLine := fmt.Sprintf("SHA: %s", commit.SHA)
	if width > 10 && len(shaLine) > width-6 {
		shaLine = shaLine[:width-7] + "…"
	}
	lines = append(lines, t.Renderer.NewStyle().Foreground(t.Primary).Render(shaLine))

	authorLine := truncateRunesHelper(fmt.Sprintf("Author: %s", commit.Author), width-6, "…")
	lines = append(lines, t.Renderer.NewStyle().Foreground(t.Secondary).Render(authorLine))

	dateLine := fmt.Sprintf("Date: %s", commit.Timestamp)
	lines = append(lines, t.Renderer.NewStyle().Foreground(t.Muted).Render(dateLine))

	filesLine := fmt.Sprintf("Files: %d changed", commit.FileCount)
	lines = append(lines, t.Renderer.NewStyle().Foreground(t.Muted).Render(filesLine))

	// Message
	lines = append(lines, "")
	msgStyle := t.Renderer.NewStyle().Foreground(t.Base.GetForeground())
	msgLines := strings.Split(commit.Message, "\n")
	for _, ml := range msgLines {
		ml = truncateRunesHelper(ml, width-6, "…")
		lines = append(lines, msgStyle.Render(ml))
	}

	// Reserve space for footer hint (bv-xf4p)
	footerHeight := 2
	contentHeight := height - 2 - footerHeight
	if contentHeight < 1 {
		contentHeight = 1 // Minimum content height to avoid negative slicing
	}

	// Pad with empty lines
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}

	// Truncate if too many lines
	if len(lines) > contentHeight {
		lines = lines[:contentHeight]
	}

	// Add footer hint (bv-xf4p)
	lines = append(lines, strings.Repeat("─", detailSepWidth))
	hintStyle := t.Renderer.NewStyle().Foreground(ColorFooterHint).Italic(true)
	lines = append(lines, hintStyle.Render("J/K:bead  y:copy  o:open  g:graph"))

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderCommitMiddlePanel renders commits for selected bead in middle pane (bv-xrfh)
func (h *HistoryModel) renderCommitMiddlePanel(width, height int) string {
	t := h.theme

	borderColor := t.Muted
	if h.focused == historyFocusMiddle {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	hist := h.SelectedHistory()
	if hist == nil {
		return panelStyle.Render("Select a bead to view commits")
	}

	var lines []string
	headerStyle := t.Renderer.NewStyle().Bold(true).Foreground(t.Primary).Width(width - 4)
	lines = append(lines, headerStyle.Render("COMMITS"))

	sepWidth := width - 4
	if sepWidth < 1 {
		sepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", sepWidth))

	visibleItems := height - 5
	if visibleItems < 1 {
		visibleItems = 1
	}

	// Use scroll offset for middle pane (bv-xrfh fix)
	totalCommits := len(hist.Commits)
	startIdx := h.middleScrollOffset
	if startIdx >= totalCommits {
		startIdx = 0
	}
	endIdx := startIdx + visibleItems
	if endIdx > totalCommits {
		endIdx = totalCommits
	}

	for i := startIdx; i < endIdx; i++ {
		commit := hist.Commits[i]
		isSelected := i == h.selectedCommit && h.focused == historyFocusMiddle

		indicator := "  "
		if isSelected {
			indicator = "▸ "
		}

		shaStyle := t.Renderer.NewStyle().Foreground(t.Primary)
		if isSelected {
			shaStyle = shaStyle.Bold(true)
		}

		maxMsgLen := width - len(commit.ShortSHA) - 8
		if maxMsgLen < 10 {
			maxMsgLen = 10
		}
		msg := truncateRunesHelper(commit.Message, maxMsgLen, "…")

		line := fmt.Sprintf("%s%s %s", indicator, shaStyle.Render(commit.ShortSHA), msg)
		lines = append(lines, line)
	}

	// Add scroll indicator if needed (bv-xrfh)
	if totalCommits > visibleItems {
		scrollInfo := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true)
		scrollPct := 0
		maxScroll := totalCommits - visibleItems
		if maxScroll > 0 {
			scrollPct = h.middleScrollOffset * 100 / maxScroll
		}
		lines = append(lines, scrollInfo.Render(fmt.Sprintf("↕ %d/%d (%d%%)", endIdx, totalCommits, scrollPct)))
	}

	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// renderGitBeadListPanel renders related beads for selected commit in middle pane (bv-xrfh)
func (h *HistoryModel) renderGitBeadListPanel(width, height int) string {
	t := h.theme

	borderColor := t.Muted
	if h.focused == historyFocusMiddle {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	commit := h.SelectedGitCommit()
	if commit == nil {
		return panelStyle.Render("Select a commit to view beads")
	}

	var lines []string
	headerStyle := t.Renderer.NewStyle().Bold(true).Foreground(t.Primary).Width(width - 4)
	lines = append(lines, headerStyle.Render("RELATED BEADS"))

	sepWidth := width - 4
	if sepWidth < 1 {
		sepWidth = 1
	}
	lines = append(lines, strings.Repeat("─", sepWidth))

	visibleItems := height - 5
	if visibleItems < 1 {
		visibleItems = 1
	}

	// Use scroll offset for middle pane (bv-xrfh fix)
	totalBeads := len(commit.BeadIDs)
	startIdx := h.middleScrollOffset
	if startIdx >= totalBeads {
		startIdx = 0
	}
	endIdx := startIdx + visibleItems
	if endIdx > totalBeads {
		endIdx = totalBeads
	}

	for i := startIdx; i < endIdx; i++ {
		beadID := commit.BeadIDs[i]
		isSelected := i == h.selectedRelatedBead && h.focused == historyFocusMiddle

		indicator := "  "
		if isSelected {
			indicator = "▸ "
		}

		beadStyle := t.Renderer.NewStyle()
		statusIcon := "○"
		title := beadID

		if h.report != nil {
			if hist, ok := h.report.Histories[beadID]; ok {
				title = hist.Title
				switch hist.Status {
				case "closed":
					statusIcon = "✓"
				case "in_progress":
					statusIcon = "●"
				}
			}
		}

		if isSelected {
			beadStyle = beadStyle.Bold(true).Foreground(t.Primary)
		}

		maxLen := width - 12
		if maxLen < 10 {
			maxLen = 10
		}
		title = truncateRunesHelper(title, maxLen, "…")

		beadLine := fmt.Sprintf("%s%s %s", indicator, statusIcon, beadStyle.Render(title))
		lines = append(lines, beadLine)
	}

	// Add scroll indicator if needed (bv-xrfh)
	if totalBeads > visibleItems {
		scrollInfo := t.Renderer.NewStyle().Foreground(t.Muted).Italic(true)
		scrollPct := 0
		maxScroll := totalBeads - visibleItems
		if maxScroll > 0 {
			scrollPct = h.middleScrollOffset * 100 / maxScroll
		}
		lines = append(lines, scrollInfo.Render(fmt.Sprintf("↕ %d/%d (%d%%)", endIdx, totalBeads, scrollPct)))
	}

	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}
