package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// BoardModel represents the Kanban board view with adaptive columns
type BoardModel struct {
	columns      [4][]model.Issue
	activeColIdx []int  // Indices of non-empty columns (for navigation)
	focusedCol   int    // Index into activeColIdx
	selectedRow  [4]int // Store selection for each column
	theme        Theme

	// Swimlane grouping mode (bv-wjs0)
	swimLaneMode SwimLaneMode
	allIssues    []model.Issue // Store all issues for re-grouping on mode change
	boardState   *BoardState   // Optional precomputed columns for all swimlane modes (bv-guxz)

	// Reverse dependency index: maps issue ID -> slice of issue IDs it blocks (bv-1daf)
	blocksIndex map[string][]string

	// Issue lookup map: ID -> *Issue for getting blocker titles (bv-kklp)
	issueMap map[string]*model.Issue

	// Detail panel (bv-r6kh)
	showDetail   bool
	detailVP     viewport.Model
	mdRenderer   *glamour.TermRenderer
	lastDetailID string // Track which issue detail is currently rendered

	// Search state (bv-yg39)
	searchMode    bool
	searchQuery   string
	searchMatches []searchMatch // Cards matching current query
	searchCursor  int           // Current match index

	// Vim key combo tracking (bv-yg39)
	waitingForG bool // True if we're waiting for second 'g' in 'gg' combo

	// Empty column visibility override (bv-tf6j)
	// nil = auto (status shows all, priority/type hide empty)
	// true = always show all columns
	// false = always hide empty columns
	showEmptyColumns *bool

	// Inline card expansion (bv-i3ii)
	// expandedCardID tracks which card is currently expanded inline
	// Empty string means no card is expanded
	expandedCardID string
}

// searchMatch holds info about a matching card (bv-yg39)
type searchMatch struct {
	col int // Column index (0-3)
	row int // Row index within column
}

// Column indices for the Kanban board
const (
	ColOpen       = 0
	ColInProgress = 1
	ColBlocked    = 2
	ColClosed     = 3
)

// SwimLaneMode determines how cards are grouped into columns (bv-wjs0)
type SwimLaneMode int

const (
	SwimByStatus   SwimLaneMode = iota // Default: Open | In Progress | Blocked | Closed
	SwimByPriority                     // P0 Critical | P1 High | P2 Medium | P3+ Other
	SwimByType                         // Bug | Feature | Task | Epic
)

// SwimLaneModeCount is the total number of swimlane modes for cycling
const SwimLaneModeCount = 3

// ColumnStats holds computed statistics for a board column (bv-nl8a)
type ColumnStats struct {
	Total        int           // Total issues in column
	P0Count      int           // Critical priority count
	P1Count      int           // High priority count
	BlockedCount int           // Issues with blocking dependencies
	OldestAge    time.Duration // Age of oldest item
}

// computeColumnStats calculates statistics for issues in a column (bv-nl8a)
func computeColumnStats(issues []model.Issue, issueMap map[string]*model.Issue) ColumnStats {
	stats := ColumnStats{Total: len(issues)}

	var oldest time.Time
	for _, issue := range issues {
		if issue.Priority == 0 {
			stats.P0Count++
		} else if issue.Priority == 1 {
			stats.P1Count++
		}

		// Count blocked items (has unresolved blocking deps)
		hasOpenBlocker := false
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if issueMap == nil {
				hasOpenBlocker = true
				break
			}
			if blocker, ok := issueMap[dep.DependsOnID]; ok && blocker != nil && !isClosedLikeStatus(blocker.Status) {
				hasOpenBlocker = true
				break
			}
		}
		if hasOpenBlocker {
			stats.BlockedCount++
		}

		// Track oldest by created date
		if !issue.CreatedAt.IsZero() {
			if oldest.IsZero() || issue.CreatedAt.Before(oldest) {
				oldest = issue.CreatedAt
			}
		}
	}

	if !oldest.IsZero() {
		stats.OldestAge = time.Since(oldest)
	}

	return stats
}

// formatOldestAge formats age duration for display (bv-nl8a)
func formatOldestAge(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days == 0 {
		return "<1d"
	}
	if days < 7 {
		return fmt.Sprintf("%dd", days)
	}
	if days < 30 {
		weeks := days / 7
		return fmt.Sprintf("%dw", weeks)
	}
	months := days / 30
	return fmt.Sprintf("%dmo", months)
}

// sortIssuesByPriorityAndDate sorts issues by priority (ascending) then by creation date (descending)
func sortIssuesByPriorityAndDate(issues []model.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Priority != issues[j].Priority {
			return issues[i].Priority < issues[j].Priority
		}
		return issues[i].CreatedAt.After(issues[j].CreatedAt)
	})
}

// updateActiveColumns rebuilds the list of non-empty column indices (bv-tf6j)
// Behavior depends on swimlane mode unless explicitly overridden:
// - Status mode: shows all 4 columns (even empty) for workflow visibility
// - Priority/Type modes: hides empty columns to save space
func (b *BoardModel) updateActiveColumns() {
	// Determine whether to show empty columns
	showEmpty := b.shouldShowEmptyColumns()

	b.activeColIdx = nil
	for i := 0; i < 4; i++ {
		if len(b.columns[i]) > 0 || showEmpty {
			b.activeColIdx = append(b.activeColIdx, i)
		}
	}
	// If all columns are empty (and we're hiding empty), include all columns anyway
	if len(b.activeColIdx) == 0 {
		b.activeColIdx = []int{ColOpen, ColInProgress, ColBlocked, ColClosed}
	}
	// Ensure focused column is within valid range
	if b.focusedCol >= len(b.activeColIdx) {
		b.focusedCol = len(b.activeColIdx) - 1
	}
	if b.focusedCol < 0 {
		b.focusedCol = 0
	}
}

// shouldShowEmptyColumns returns whether empty columns should be visible (bv-tf6j)
func (b *BoardModel) shouldShowEmptyColumns() bool {
	// Explicit override takes precedence
	if b.showEmptyColumns != nil {
		return *b.showEmptyColumns
	}
	// Auto behavior: Status mode shows all, others hide empty
	return b.swimLaneMode == SwimByStatus
}

// ToggleEmptyColumns cycles through empty column visibility modes (bv-tf6j)
// nil (auto) -> true (show all) -> false (hide empty) -> nil (auto)
func (b *BoardModel) ToggleEmptyColumns() {
	if b.showEmptyColumns == nil {
		showAll := true
		b.showEmptyColumns = &showAll
	} else if *b.showEmptyColumns {
		hideEmpty := false
		b.showEmptyColumns = &hideEmpty
	} else {
		b.showEmptyColumns = nil // Back to auto
	}
	b.updateActiveColumns()
}

// GetEmptyColumnVisibilityMode returns the current visibility mode name (bv-tf6j)
func (b *BoardModel) GetEmptyColumnVisibilityMode() string {
	if b.showEmptyColumns == nil {
		return "Auto"
	}
	if *b.showEmptyColumns {
		return "Show All"
	}
	return "Hide Empty"
}

// HiddenColumnCount returns the number of empty columns currently hidden (bv-tf6j)
func (b *BoardModel) HiddenColumnCount() int {
	hidden := 0
	for i := 0; i < 4; i++ {
		if len(b.columns[i]) == 0 {
			// Check if this column is in activeColIdx
			found := false
			for _, idx := range b.activeColIdx {
				if idx == i {
					found = true
					break
				}
			}
			if !found {
				hidden++
			}
		}
	}
	return hidden
}

// buildBlocksIndex creates a reverse dependency map: for each issue that is depended on,
// it stores the list of issue IDs that depend on it (bv-1daf)
func buildBlocksIndex(issues []model.Issue) map[string][]string {
	index := make(map[string][]string)
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type.IsBlocking() {
				// dep.DependsOnID blocks issue.ID
				index[dep.DependsOnID] = append(index[dep.DependsOnID], issue.ID)
			}
		}
	}
	return index
}

// groupIssuesByMode distributes issues into 4 columns based on swimlane mode (bv-wjs0)
func groupIssuesByMode(issues []model.Issue, mode SwimLaneMode) [4][]model.Issue {
	var cols [4][]model.Issue

	for _, issue := range issues {
		var colIdx int
		switch mode {
		case SwimByStatus:
			// Default: Open | In Progress | Blocked | Closed
			switch {
			case isClosedLikeStatus(issue.Status):
				colIdx = 3
			case issue.Status == model.StatusOpen:
				colIdx = 0
			case issue.Status == model.StatusInProgress:
				colIdx = 1
			case issue.Status == model.StatusBlocked:
				colIdx = 2
			default:
				colIdx = 0
			}
		case SwimByPriority:
			// P0 Critical | P1 High | P2 Medium | P3+ Other
			switch {
			case issue.Priority == 0:
				colIdx = 0 // Critical
			case issue.Priority == 1:
				colIdx = 1 // High
			case issue.Priority == 2:
				colIdx = 2 // Medium
			default:
				colIdx = 3 // P3+ Other
			}
		case SwimByType:
			// Bug | Feature | Task | Epic
			switch issue.IssueType {
			case model.TypeBug:
				colIdx = 0
			case model.TypeFeature:
				colIdx = 1
			case model.TypeTask:
				colIdx = 2
			case model.TypeEpic:
				colIdx = 3
			default:
				colIdx = 2 // Default to Task
			}
		}
		cols[colIdx] = append(cols[colIdx], issue)
	}

	// Sort each column
	for i := 0; i < 4; i++ {
		sortIssuesByPriorityAndDate(cols[i])
	}

	return cols
}

// GetSwimLaneModeName returns the display name for the current swimlane mode (bv-wjs0)
func (b *BoardModel) GetSwimLaneModeName() string {
	switch b.swimLaneMode {
	case SwimByStatus:
		return "Status"
	case SwimByPriority:
		return "Priority"
	case SwimByType:
		return "Type"
	default:
		return "Status"
	}
}

// GetSwimLaneMode returns the current swimlane mode (bv-wjs0)
func (b *BoardModel) GetSwimLaneMode() SwimLaneMode {
	return b.swimLaneMode
}

// CycleSwimLaneMode cycles to the next swimlane mode and regroups issues (bv-wjs0)
func (b *BoardModel) CycleSwimLaneMode() {
	b.swimLaneMode = SwimLaneMode((int(b.swimLaneMode) + 1) % SwimLaneModeCount)
	b.regroupIssues()
}

// regroupIssues rebuilds columns based on current swimlane mode (bv-wjs0)
func (b *BoardModel) regroupIssues() {
	if b.boardState != nil {
		b.columns = b.boardState.ColumnsForMode(b.swimLaneMode)
	} else {
		b.columns = groupIssuesByMode(b.allIssues, b.swimLaneMode)
	}

	// Reset selection to avoid out-of-bounds
	for i := 0; i < 4; i++ {
		if b.selectedRow[i] >= len(b.columns[i]) {
			if len(b.columns[i]) > 0 {
				b.selectedRow[i] = len(b.columns[i]) - 1
			} else {
				b.selectedRow[i] = 0
			}
		}
	}

	b.updateActiveColumns()
	b.CancelSearch()    // Clear stale search matches
	b.lastDetailID = "" // Force detail panel refresh
}

// getColumnHeaders returns the column header titles based on swimlane mode (bv-wjs0)
func (b *BoardModel) getColumnHeaders() ([]string, []string) {
	switch b.swimLaneMode {
	case SwimByPriority:
		return []string{"P0 CRITICAL", "P1 HIGH", "P2 MEDIUM", "P3+ OTHER"},
			[]string{"🔥", "⚡", "🔹", "💤"}
	case SwimByType:
		return []string{"BUG", "FEATURE", "TASK", "EPIC"},
			[]string{"🐛", "✨", "📋", "🎯"}
	default: // SwimByStatus
		return []string{"OPEN", "IN PROGRESS", "BLOCKED", "CLOSED"},
			[]string{"📋", "🔄", "🚫", "✅"}
	}
}

// NewBoardModel creates a new Kanban board from the given issues
func NewBoardModel(issues []model.Issue, theme Theme) BoardModel {
	// Group issues by default mode (status) - bv-wjs0
	cols := groupIssuesByMode(issues, SwimByStatus)

	// Initialize markdown renderer for detail panel (bv-r6kh)
	var mdRenderer *glamour.TermRenderer
	mdRenderer, _ = glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(60),
	)

	// Build issue lookup map for getting blocker titles (bv-kklp)
	issueMap := make(map[string]*model.Issue, len(issues))
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	b := BoardModel{
		columns:      cols,
		focusedCol:   0,
		theme:        theme,
		swimLaneMode: SwimByStatus, // Default mode (bv-wjs0)
		allIssues:    issues,       // Store for regrouping (bv-wjs0)
		blocksIndex:  buildBlocksIndex(issues),
		issueMap:     issueMap,
		detailVP:     viewport.New(40, 20),
		mdRenderer:   mdRenderer,
	}
	b.updateActiveColumns()
	return b
}

// SetIssues updates the board data, typically after filtering
func (b *BoardModel) SetIssues(issues []model.Issue) {
	// Store all issues for regrouping on mode change (bv-wjs0)
	b.allIssues = issues
	b.boardState = nil

	// Group by current swimlane mode (bv-wjs0)
	b.columns = groupIssuesByMode(issues, b.swimLaneMode)

	b.blocksIndex = buildBlocksIndex(issues) // Rebuild reverse dependency index (bv-1daf)

	// Rebuild issue lookup map for blocker titles (bv-kklp)
	b.issueMap = make(map[string]*model.Issue, len(issues))
	for i := range issues {
		b.issueMap[issues[i].ID] = &issues[i]
	}

	// Clear search state - stale matches could reference invalid positions (bv-yg39)
	b.CancelSearch()

	// Reset detail panel cache to force refresh if same issue is selected
	b.lastDetailID = ""

	// Sanitize selection to prevent out-of-bounds
	for i := 0; i < 4; i++ {
		if b.selectedRow[i] >= len(b.columns[i]) {
			if len(b.columns[i]) > 0 {
				b.selectedRow[i] = len(b.columns[i]) - 1
			} else {
				b.selectedRow[i] = 0
			}
		}
	}

	b.updateActiveColumns()
}

// SetSnapshot updates the board data directly from a DataSnapshot (bv-guxz).
// This avoids UI-thread grouping/sorting work when the full dataset is shown.
func (b *BoardModel) SetSnapshot(s *DataSnapshot) {
	if s == nil {
		b.SetIssues(nil)
		return
	}

	b.allIssues = s.Issues
	b.boardState = s.BoardState

	if b.boardState != nil {
		b.columns = b.boardState.ColumnsForMode(b.swimLaneMode)
	} else {
		b.columns = groupIssuesByMode(s.Issues, b.swimLaneMode)
	}

	// Prefer snapshot-precomputed reverse-dependency index when available.
	if s.GraphLayout != nil && s.GraphLayout.Dependents != nil {
		b.blocksIndex = s.GraphLayout.Dependents
	} else {
		b.blocksIndex = buildBlocksIndex(s.Issues)
	}

	// Use snapshot issue map for blocker titles.
	b.issueMap = s.IssueMap

	// Clear search state - stale matches could reference invalid positions (bv-yg39)
	b.CancelSearch()

	// Reset detail panel cache to force refresh if same issue is selected
	b.lastDetailID = ""

	// Sanitize selection to prevent out-of-bounds
	for i := 0; i < 4; i++ {
		if b.selectedRow[i] >= len(b.columns[i]) {
			if len(b.columns[i]) > 0 {
				b.selectedRow[i] = len(b.columns[i]) - 1
			} else {
				b.selectedRow[i] = 0
			}
		}
	}

	b.updateActiveColumns()
}

// actualFocusedCol returns the actual column index (0-3) being focused
func (b *BoardModel) actualFocusedCol() int {
	if len(b.activeColIdx) == 0 {
		return 0
	}
	return b.activeColIdx[b.focusedCol]
}

// Navigation methods
func (b *BoardModel) MoveDown() {
	b.CollapseExpanded() // Auto-collapse on navigation (bv-i3ii)
	col := b.actualFocusedCol()
	count := len(b.columns[col])
	if count == 0 {
		return
	}
	if b.selectedRow[col] < count-1 {
		b.selectedRow[col]++
	}
}

func (b *BoardModel) MoveUp() {
	b.CollapseExpanded() // Auto-collapse on navigation (bv-i3ii)
	col := b.actualFocusedCol()
	if b.selectedRow[col] > 0 {
		b.selectedRow[col]--
	}
}

func (b *BoardModel) MoveRight() {
	b.CollapseExpanded() // Auto-collapse on navigation (bv-i3ii)
	if b.focusedCol < len(b.activeColIdx)-1 {
		b.focusedCol++
	}
}

func (b *BoardModel) MoveLeft() {
	b.CollapseExpanded() // Auto-collapse on navigation (bv-i3ii)
	if b.focusedCol > 0 {
		b.focusedCol--
	}
}

func (b *BoardModel) MoveToTop() {
	col := b.actualFocusedCol()
	b.selectedRow[col] = 0
}

func (b *BoardModel) MoveToBottom() {
	col := b.actualFocusedCol()
	count := len(b.columns[col])
	if count > 0 {
		b.selectedRow[col] = count - 1
	}
}

func (b *BoardModel) PageDown(visibleRows int) {
	col := b.actualFocusedCol()
	count := len(b.columns[col])
	if count == 0 {
		return
	}
	newRow := b.selectedRow[col] + visibleRows/2
	if newRow >= count {
		newRow = count - 1
	}
	b.selectedRow[col] = newRow
}

func (b *BoardModel) PageUp(visibleRows int) {
	col := b.actualFocusedCol()
	newRow := b.selectedRow[col] - visibleRows/2
	if newRow < 0 {
		newRow = 0
	}
	b.selectedRow[col] = newRow
}

// ═══════════════════════════════════════════════════════════════════════════
// Enhanced Navigation (bv-yg39)
// ═══════════════════════════════════════════════════════════════════════════

// JumpToColumn jumps directly to a specific column (1-4 maps to 0-3)
func (b *BoardModel) JumpToColumn(colIdx int) {
	if colIdx < 0 || colIdx > 3 {
		return
	}
	for i, activeCol := range b.activeColIdx {
		if activeCol == colIdx {
			b.focusedCol = i
			return
		}
	}
	// Column is empty - find nearest active column
	bestIdx := 0
	bestDist := 100
	for i, activeCol := range b.activeColIdx {
		dist := activeCol - colIdx
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}
	b.focusedCol = bestIdx
}

// JumpToFirstColumn jumps to the first non-empty column (H key)
func (b *BoardModel) JumpToFirstColumn() {
	if len(b.activeColIdx) > 0 {
		b.focusedCol = 0
	}
}

// JumpToLastColumn jumps to the last non-empty column (L key)
func (b *BoardModel) JumpToLastColumn() {
	if len(b.activeColIdx) > 0 {
		b.focusedCol = len(b.activeColIdx) - 1
	}
}

// ClearWaitingForG clears the gg combo state
func (b *BoardModel) ClearWaitingForG() { b.waitingForG = false }

// SetWaitingForG sets the gg combo state
func (b *BoardModel) SetWaitingForG() { b.waitingForG = true }

// IsWaitingForG returns whether we're waiting for second g
func (b *BoardModel) IsWaitingForG() bool { return b.waitingForG }

// ═══════════════════════════════════════════════════════════════════════════
// Search functionality (bv-yg39)
// ═══════════════════════════════════════════════════════════════════════════

// IsSearchMode returns whether search mode is active
func (b *BoardModel) IsSearchMode() bool { return b.searchMode }

// StartSearch enters search mode
func (b *BoardModel) StartSearch() {
	b.searchMode = true
	b.searchQuery = ""
	b.searchMatches = nil
	b.searchCursor = 0
}

// CancelSearch exits search mode and clears results
func (b *BoardModel) CancelSearch() {
	b.searchMode = false
	b.searchQuery = ""
	b.searchMatches = nil
	b.searchCursor = 0
}

// FinishSearch exits search mode but keeps results for n/N navigation
func (b *BoardModel) FinishSearch() {
	b.searchMode = false
}

// SearchQuery returns the current search query
func (b *BoardModel) SearchQuery() string { return b.searchQuery }

// SearchMatchCount returns the number of matches
func (b *BoardModel) SearchMatchCount() int { return len(b.searchMatches) }

// SearchCursorPos returns current match position (1-indexed for display)
func (b *BoardModel) SearchCursorPos() int {
	if len(b.searchMatches) == 0 {
		return 0
	}
	return b.searchCursor + 1
}

// AppendSearchChar adds a character to the search query
func (b *BoardModel) AppendSearchChar(ch rune) {
	b.searchQuery += string(ch)
	b.updateSearchMatches()
}

// BackspaceSearch removes the last character from search query
func (b *BoardModel) BackspaceSearch() {
	if len(b.searchQuery) > 0 {
		runes := []rune(b.searchQuery)
		b.searchQuery = string(runes[:len(runes)-1])
		b.updateSearchMatches()
	}
}

// updateSearchMatches finds all cards matching the search query
func (b *BoardModel) updateSearchMatches() {
	b.searchMatches = nil
	b.searchCursor = 0
	if b.searchQuery == "" {
		return
	}
	query := strings.ToLower(b.searchQuery)
	for colIdx, issues := range b.columns {
		for rowIdx, issue := range issues {
			idLower := strings.ToLower(issue.ID)
			titleLower := strings.ToLower(issue.Title)
			if strings.Contains(idLower, query) || strings.Contains(titleLower, query) {
				b.searchMatches = append(b.searchMatches, searchMatch{col: colIdx, row: rowIdx})
			}
		}
	}
	if len(b.searchMatches) > 0 {
		b.jumpToMatch(0)
	}
}

// jumpToMatch navigates to a specific match
func (b *BoardModel) jumpToMatch(idx int) {
	if idx < 0 || idx >= len(b.searchMatches) {
		return
	}
	b.searchCursor = idx
	match := b.searchMatches[idx]
	for i, activeCol := range b.activeColIdx {
		if activeCol == match.col {
			b.focusedCol = i
			break
		}
	}
	b.selectedRow[match.col] = match.row
}

// NextMatch jumps to the next search match (n key)
func (b *BoardModel) NextMatch() {
	if len(b.searchMatches) == 0 {
		return
	}
	b.jumpToMatch((b.searchCursor + 1) % len(b.searchMatches))
}

// PrevMatch jumps to the previous search match (N key)
func (b *BoardModel) PrevMatch() {
	if len(b.searchMatches) == 0 {
		return
	}
	prevIdx := b.searchCursor - 1
	if prevIdx < 0 {
		prevIdx = len(b.searchMatches) - 1
	}
	b.jumpToMatch(prevIdx)
}

// IsMatchHighlighted returns true if position is current search match
func (b *BoardModel) IsMatchHighlighted(colIdx, rowIdx int) bool {
	if !b.searchMode || len(b.searchMatches) == 0 {
		return false
	}
	match := b.searchMatches[b.searchCursor]
	return match.col == colIdx && match.row == rowIdx
}

// IsSearchMatch returns true if position matches the search query
func (b *BoardModel) IsSearchMatch(colIdx, rowIdx int) bool {
	if !b.searchMode || b.searchQuery == "" {
		return false
	}
	for _, m := range b.searchMatches {
		if m.col == colIdx && m.row == rowIdx {
			return true
		}
	}
	return false
}

// Detail panel methods (bv-r6kh)

// ToggleDetail toggles the detail panel visibility
func (b *BoardModel) ToggleDetail() {
	b.showDetail = !b.showDetail
}

// ShowDetail shows the detail panel
func (b *BoardModel) ShowDetail() {
	b.showDetail = true
}

// HideDetail hides the detail panel
func (b *BoardModel) HideDetail() {
	b.showDetail = false
}

// IsDetailShown returns whether detail panel is visible
func (b *BoardModel) IsDetailShown() bool {
	return b.showDetail
}

// DetailScrollDown scrolls the detail panel down
func (b *BoardModel) DetailScrollDown(lines int) {
	b.detailVP.LineDown(lines)
}

// DetailScrollUp scrolls the detail panel up
func (b *BoardModel) DetailScrollUp(lines int) {
	b.detailVP.LineUp(lines)
}

// SelectedIssue returns the currently selected issue, or nil if none
func (b *BoardModel) SelectedIssue() *model.Issue {
	col := b.actualFocusedCol()
	cols := b.columns[col]
	row := b.selectedRow[col]
	if len(cols) > 0 && row < len(cols) {
		return &cols[row]
	}
	return nil
}

// SelectIssueByID attempts to focus and select the given issue ID on the board.
// Returns true if the issue was found in the current board columns.
func (b *BoardModel) SelectIssueByID(id string) bool {
	if id == "" {
		return false
	}

	// Search all columns; if found, set both focused column and selected row.
	for col := 0; col < 4; col++ {
		for row := range b.columns[col] {
			if b.columns[col][row].ID != id {
				continue
			}

			// Focus the matching column (focusedCol is an index into activeColIdx).
			for i, colIdx := range b.activeColIdx {
				if colIdx == col {
					b.focusedCol = i
					break
				}
			}
			b.selectedRow[col] = row
			return true
		}
	}

	return false
}

// ColumnCount returns the number of issues in a column
func (b *BoardModel) ColumnCount(col int) int {
	if col >= 0 && col < 4 {
		return len(b.columns[col])
	}
	return 0
}

// TotalCount returns the total number of issues across all columns
func (b *BoardModel) TotalCount() int {
	total := 0
	for i := 0; i < 4; i++ {
		total += len(b.columns[i])
	}
	return total
}

// ═══════════════════════════════════════════════════════════════════════════
// Inline card expansion (bv-i3ii)
// ═══════════════════════════════════════════════════════════════════════════

// ToggleExpand toggles inline expansion for the selected card
// If a different card is expanded, it collapses that and expands the new one
func (b *BoardModel) ToggleExpand() {
	selected := b.SelectedIssue()
	if selected == nil {
		return
	}
	if b.expandedCardID == selected.ID {
		// Collapse if already expanded
		b.expandedCardID = ""
	} else {
		// Expand selected card (auto-collapses previous)
		b.expandedCardID = selected.ID
	}
}

// CollapseExpanded collapses any currently expanded card
func (b *BoardModel) CollapseExpanded() {
	b.expandedCardID = ""
}

// IsCardExpanded returns true if the specified card is currently expanded
func (b *BoardModel) IsCardExpanded(id string) bool {
	return b.expandedCardID != "" && b.expandedCardID == id
}

// GetExpandedID returns the ID of the currently expanded card (empty if none)
func (b *BoardModel) GetExpandedID() string {
	return b.expandedCardID
}

// HasExpandedCard returns true if any card is currently expanded
func (b *BoardModel) HasExpandedCard() bool {
	return b.expandedCardID != ""
}

// View renders the Kanban board with adaptive columns
func (b BoardModel) View(width, height int) string {
	t := b.theme

	// Calculate how many columns we're showing
	numCols := len(b.activeColIdx)
	if numCols == 0 {
		return t.Renderer.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(t.Secondary).
			Render("No issues to display")
	}

	// Calculate board width vs detail panel width (bv-r6kh)
	// Detail panel takes ~35% of width when shown, min 40 chars
	boardWidth := width
	detailWidth := 0
	if b.showDetail && width > 120 {
		detailWidth = width * 35 / 100
		if detailWidth < 40 {
			detailWidth = 40
		}
		if detailWidth > 80 {
			detailWidth = 80
		}
		boardWidth = width - detailWidth - 1 // 1 char gap
	}

	// Calculate column widths - distribute space evenly
	// Minimum column width for readability, NO maximum cap (bv-ic17)
	minColWidth := 28

	// Calculate available width (subtract gaps between columns)
	gaps := numCols - 1
	availableWidth := boardWidth - (gaps * 2) // 2 chars gap between columns

	// Distribute width evenly across columns, respecting minimum
	baseWidth := availableWidth / numCols
	if baseWidth < minColWidth {
		baseWidth = minColWidth
	}
	// NO maxColWidth cap - use all available horizontal space

	colHeight := height - 6 // Account for column header + title bar (bv-tf6j)
	if colHeight < 8 {
		colHeight = 8
	}

	// Get dynamic column headers based on swimlane mode (bv-wjs0)
	columnTitles, columnEmoji := b.getColumnHeaders()

	// Column colors - use appropriate colors based on mode
	var columnColors []lipgloss.AdaptiveColor
	switch b.swimLaneMode {
	case SwimByPriority:
		// P0 red, P1 orange, P2 blue, P3+ gray
		columnColors = []lipgloss.AdaptiveColor{
			{Light: "#c62828", Dark: "#ef5350"}, // Critical - red
			{Light: "#f57c00", Dark: "#ffb74d"}, // High - orange
			{Light: "#1565c0", Dark: "#64b5f6"}, // Medium - blue
			{Light: "#616161", Dark: "#9e9e9e"}, // Other - gray
		}
	case SwimByType:
		// Bug red, Feature green, Task blue, Epic purple
		columnColors = []lipgloss.AdaptiveColor{
			{Light: "#c62828", Dark: "#ef5350"}, // Bug - red
			{Light: "#2e7d32", Dark: "#81c784"}, // Feature - green
			{Light: "#1565c0", Dark: "#64b5f6"}, // Task - blue
			{Light: "#7b1fa2", Dark: "#ce93d8"}, // Epic - purple
		}
	default: // SwimByStatus
		columnColors = []lipgloss.AdaptiveColor{t.Open, t.InProgress, t.Blocked, t.Closed}
	}

	var renderedCols []string

	for i, colIdx := range b.activeColIdx {
		isFocused := b.focusedCol == i
		issues := b.columns[colIdx]
		issueCount := len(issues)

		// Compute column statistics (bv-nl8a)
		stats := computeColumnStats(issues, b.issueMap)

		// Build header text with adaptive stats based on terminal width (bv-nl8a)
		// - Narrow (<100): Just count
		// - Medium (100-140): Count + P0/P1 counts
		// - Wide (>140): Full stats including oldest age
		var headerText string
		baseHeader := fmt.Sprintf("%s %s (%d)", columnEmoji[colIdx], columnTitles[colIdx], issueCount)

		if width < 100 {
			// Narrow: just the base header
			headerText = baseHeader
		} else if width < 140 {
			// Medium: add P0/P1 indicators if any exist
			var indicators []string
			if stats.P0Count > 0 {
				indicators = append(indicators, fmt.Sprintf("%d🔴", stats.P0Count))
			}
			if stats.P1Count > 0 {
				indicators = append(indicators, fmt.Sprintf("%d🟡", stats.P1Count))
			}
			if len(indicators) > 0 {
				headerText = baseHeader + " " + strings.Join(indicators, " ")
			} else {
				headerText = baseHeader
			}
		} else {
			// Wide: full stats including oldest age
			var indicators []string
			if stats.P0Count > 0 {
				indicators = append(indicators, fmt.Sprintf("%d🔴", stats.P0Count))
			}
			if stats.P1Count > 0 {
				indicators = append(indicators, fmt.Sprintf("%d🟡", stats.P1Count))
			}
			// Show blocked count in In Progress column (colIdx == ColInProgress when in status mode)
			if b.swimLaneMode == SwimByStatus && colIdx == ColInProgress && stats.BlockedCount > 0 {
				indicators = append(indicators, fmt.Sprintf("⚠️%d", stats.BlockedCount))
			}
			// Show oldest age with color indicator
			if stats.OldestAge > 0 && issueCount > 0 {
				ageStr := formatOldestAge(stats.OldestAge)
				indicators = append(indicators, fmt.Sprintf("⏱%s", ageStr))
			}
			if len(indicators) > 0 {
				headerText = baseHeader + " " + strings.Join(indicators, " ")
			} else {
				headerText = baseHeader
			}
		}

		headerStyle := t.Renderer.NewStyle().
			Width(baseWidth).
			Align(lipgloss.Center).
			Bold(true).
			Padding(0, 1)

		if isFocused {
			headerStyle = headerStyle.
				Background(columnColors[colIdx]).
				Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1a1a1a"})
		} else {
			headerStyle = headerStyle.
				Background(lipgloss.AdaptiveColor{Light: "#E0E0E0", Dark: "#2a2a2a"}).
				Foreground(columnColors[colIdx])
		}

		header := headerStyle.Render(headerText)

		// Calculate visible rows (bv-1daf: 3 content lines)
		// Card height breakdown:
		// - 3 content lines (line1: meta, line2: title, line3: deps/labels)
		// - 2 border lines (RoundedBorder adds top + bottom)
		// - 1 margin line (MarginBottom(1))
		// Total: 6 lines per card
		cardHeight := 6
		visibleCards := (colHeight - 1) / cardHeight
		if visibleCards < 1 {
			visibleCards = 1
		}

		sel := b.selectedRow[colIdx]
		if sel >= issueCount && issueCount > 0 {
			sel = issueCount - 1
		}

		// Simple scrolling: keep selected card visible
		start := 0
		if sel >= visibleCards {
			start = sel - visibleCards + 1
		}

		end := start + visibleCards
		if end > issueCount {
			end = issueCount
		}

		// Render cards
		var cards []string
		for rowIdx := start; rowIdx < end; rowIdx++ {
			issue := issues[rowIdx]
			isSelected := isFocused && rowIdx == sel

			// Check if this card is expanded (bv-i3ii)
			var card string
			if b.IsCardExpanded(issue.ID) {
				card = b.renderExpandedCard(issue, baseWidth-4, colIdx, rowIdx)
			} else {
				card = b.renderCard(issue, baseWidth-4, isSelected, colIdx, rowIdx)
			}
			cards = append(cards, card)
		}

		// Empty column placeholder
		if issueCount == 0 {
			emptyStyle := t.Renderer.NewStyle().
				Width(baseWidth-4).
				Height(colHeight-2).
				Align(lipgloss.Center, lipgloss.Center).
				Foreground(t.Secondary).
				Italic(true)
			cards = append(cards, emptyStyle.Render("(empty)"))
		}

		// Scroll indicator
		if issueCount > visibleCards {
			scrollInfo := fmt.Sprintf("↕ %d/%d", sel+1, issueCount)
			scrollStyle := t.Renderer.NewStyle().
				Width(baseWidth - 4).
				Align(lipgloss.Center).
				Foreground(t.Secondary).
				Italic(true)
			cards = append(cards, scrollStyle.Render(scrollInfo))
		}

		// Column content
		content := lipgloss.JoinVertical(lipgloss.Left, cards...)

		// Column container
		colStyle := t.Renderer.NewStyle().
			Width(baseWidth).
			Height(colHeight).
			Padding(0, 1).
			Border(lipgloss.RoundedBorder())

		if isFocused {
			colStyle = colStyle.BorderForeground(columnColors[colIdx])
		} else {
			colStyle = colStyle.BorderForeground(t.Secondary)
		}

		column := lipgloss.JoinVertical(lipgloss.Center, header, colStyle.Render(content))
		renderedCols = append(renderedCols, column)
	}

	// Join columns with gaps
	columnsView := lipgloss.JoinHorizontal(lipgloss.Top, renderedCols...)

	// Build title bar with swimlane mode and hidden column indicator (bv-tf6j)
	titleBar := b.renderTitleBar(boardWidth, t)

	// Combine title bar and columns
	boardView := lipgloss.JoinVertical(lipgloss.Left, titleBar, columnsView)

	// Add detail panel if shown (bv-r6kh)
	if detailWidth > 0 {
		detailPanel := b.renderDetailPanel(detailWidth, height-2)
		return lipgloss.JoinHorizontal(lipgloss.Top, boardView, detailPanel)
	}

	return boardView
}

// renderTitleBar creates the board title bar with swimlane mode and hidden column count (bv-tf6j)
func (b BoardModel) renderTitleBar(width int, t Theme) string {
	// Build title: "BOARD [by: Status]" or "BOARD [by: Priority] [+2 hidden]"
	modeName := b.GetSwimLaneModeName()
	title := fmt.Sprintf("BOARD [by: %s]", modeName)

	// Add hidden column indicator if columns are hidden
	hiddenCount := b.HiddenColumnCount()
	if hiddenCount > 0 {
		title = fmt.Sprintf("%s [+%d hidden]", title, hiddenCount)
	}

	// Style the title bar
	titleStyle := t.Renderer.NewStyle().
		Width(width).
		Align(lipgloss.Center).
		Foreground(t.Primary).
		Bold(true).
		Padding(0, 0, 1, 0) // Bottom padding

	return titleStyle.Render(title)
}

// getAgeColor returns a color based on issue age (bv-1daf)
// green (<7d), yellow (7-30d), red (>30d stale)
func getAgeColor(t time.Time) lipgloss.TerminalColor {
	if t.IsZero() {
		return ColorMuted
	}
	days := int(time.Since(t).Hours() / 24)
	switch {
	case days < 7:
		return lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // green
	case days < 30:
		return lipgloss.AdaptiveColor{Light: "#f57c00", Dark: "#ffb74d"} // yellow/orange
	default:
		return lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#e57373"} // red
	}
}

// formatPriority returns priority as P0/P1/P2/P3/P4 (bv-1daf)
func formatPriority(p int) string {
	if p < 0 {
		p = 0
	}
	if p > 4 {
		p = 4
	}
	return fmt.Sprintf("P%d", p)
}

// renderCard creates a visually rich card for an issue (bv-1daf: 4-line format)
func (b BoardModel) renderCard(issue model.Issue, width int, selected bool, colIdx, rowIdx int) string {
	t := b.theme

	// ══════════════════════════════════════════════════════════════════════════
	// DETERMINE BLOCKING STATUS for color coding (bv-kklp)
	// ══════════════════════════════════════════════════════════════════════════
	hasBlockingDeps := false
	for _, dep := range issue.Dependencies {
		if dep != nil && dep.Type.IsBlocking() {
			hasBlockingDeps = true
			break
		}
	}
	blocksOthers := len(b.blocksIndex[issue.ID]) > 0

	// ══════════════════════════════════════════════════════════════════════════
	// SEARCH MATCH HIGHLIGHTING (bv-yg39)
	// ══════════════════════════════════════════════════════════════════════════
	isCurrentMatch := b.IsMatchHighlighted(colIdx, rowIdx) // Current search cursor position
	isAnyMatch := b.IsSearchMatch(colIdx, rowIdx)          // Any match in search results

	// ══════════════════════════════════════════════════════════════════════════
	// CARD STYLING - Fixed 4-line height (bv-1daf) with blocking colors (bv-kklp)
	// ══════════════════════════════════════════════════════════════════════════
	cardStyle := t.Renderer.NewStyle().
		Width(width).
		Padding(0, 1).
		MarginBottom(1)

	// Border color based on blocking status (bv-kklp):
	// - Red: Blocked (has blocking dependencies)
	// - Yellow/Orange: High-impact (blocks others)
	// - Green: Ready to work (open, no blockers)
	// - Default: Normal border
	// Search matches override border color (bv-yg39)
	var borderColor lipgloss.TerminalColor
	if selected {
		borderColor = t.Primary // Selected always uses primary
	} else if isCurrentMatch {
		borderColor = lipgloss.AdaptiveColor{Light: "#7b1fa2", Dark: "#ce93d8"} // Purple - current search match
	} else if isAnyMatch {
		borderColor = lipgloss.AdaptiveColor{Light: "#1565c0", Dark: "#64b5f6"} // Blue - search match
	} else if hasBlockingDeps {
		borderColor = lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#ef5350"} // Red - blocked
	} else if blocksOthers {
		borderColor = lipgloss.AdaptiveColor{Light: "#f57c00", Dark: "#ffb74d"} // Yellow/orange - high impact
	} else if issue.Status == model.StatusOpen {
		borderColor = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // Green - ready
	} else {
		borderColor = t.Border // Default border
	}

	if selected {
		cardStyle = cardStyle.
			Background(t.Highlight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)
	} else if isCurrentMatch {
		// Highlight current match with subtle background (bv-yg39)
		cardStyle = cardStyle.
			Background(lipgloss.AdaptiveColor{Light: "#e1bee7", Dark: "#4a148c"}).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)
	} else {
		cardStyle = cardStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)
	}

	// ══════════════════════════════════════════════════════════════════════════
	// LINE 1: Type icon + Priority (P0/P1/P2) + ID + Age with color (bv-1daf)
	// ══════════════════════════════════════════════════════════════════════════
	icon, iconColor := t.GetTypeIcon(string(issue.IssueType))

	// Priority as P0/P1/P2 text (clearer than emoji flame levels)
	prioText := formatPriority(issue.Priority)
	prioStyle := t.Renderer.NewStyle().Bold(true)
	if issue.Priority <= 1 {
		prioStyle = prioStyle.Foreground(lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#ef5350"})
	} else {
		prioStyle = prioStyle.Foreground(t.Secondary)
	}

	// Truncate ID for narrow cards - reserve space for age indicator
	maxIDLen := width - 14 // Icon(2) + space + P#(2) + space + age(6) + spacing
	if maxIDLen < 6 {
		maxIDLen = 6
	}
	displayID := truncateRunesHelper(issue.ID, maxIDLen, "…")

	// Age indicator with color coding: green(<7d), yellow(7-30d), red(>30d)
	ageText := FormatTimeRel(issue.UpdatedAt)
	if len(ageText) > 6 {
		ageText = truncateRunesHelper(ageText, 6, "")
	}
	ageColor := getAgeColor(issue.UpdatedAt)
	ageStyled := t.Renderer.NewStyle().Foreground(ageColor).Render(ageText)

	line1 := fmt.Sprintf("%s %s %s %s",
		t.Renderer.NewStyle().Foreground(iconColor).Render(icon),
		prioStyle.Render(prioText),
		t.Renderer.NewStyle().Bold(true).Foreground(t.Secondary).Render(displayID),
		ageStyled,
	)

	// ══════════════════════════════════════════════════════════════════════════
	// LINE 2: Title with full available width (bv-1daf)
	// ══════════════════════════════════════════════════════════════════════════
	titleWidth := width - 2
	if titleWidth < 10 {
		titleWidth = 10
	}
	truncatedTitle := truncateRunesHelper(issue.Title, titleWidth, "…")

	titleStyle := t.Renderer.NewStyle()
	if selected {
		titleStyle = titleStyle.Foreground(t.Primary).Bold(true)
	} else {
		titleStyle = titleStyle.Foreground(t.Base.GetForeground())
	}
	line2 := titleStyle.Render(truncatedTitle)

	// ══════════════════════════════════════════════════════════════════════════
	// LINE 3: Blocked-by + Blocks count + Labels (bv-1daf)
	// No @assignee - not useful for agent workflows per spec
	// ══════════════════════════════════════════════════════════════════════════
	var meta []string

	// Blocked-by indicator: 🚫←bv-456 (title...) - show first blocking dep with title (bv-kklp)
	for _, dep := range issue.Dependencies {
		if dep != nil && dep.Type.IsBlocking() {
			blockerID := truncateRunesHelper(dep.DependsOnID, 10, "…")
			blockedStyle := t.Renderer.NewStyle().Foreground(t.Blocked)
			// Try to get blocker title for better context
			blockerBadge := "🚫←" + blockerID
			if blocker, ok := b.issueMap[dep.DependsOnID]; ok && blocker != nil {
				titleSnippet := truncateRunesHelper(blocker.Title, 12, "…")
				blockerBadge = fmt.Sprintf("🚫←%s (%s)", blockerID, titleSnippet)
			}
			meta = append(meta, blockedStyle.Render(blockerBadge))
			break // Only show first blocker
		}
	}

	// Blocks count: ⚡→N (this card blocks N others) - from reverse index
	if blockedIDs, ok := b.blocksIndex[issue.ID]; ok && len(blockedIDs) > 0 {
		blocksStyle := t.Renderer.NewStyle().Foreground(t.Feature)
		meta = append(meta, blocksStyle.Render(fmt.Sprintf("⚡→%d", len(blockedIDs))))
	}

	// Labels: show 2-3 label names (no "+N" count per spec)
	if len(issue.Labels) > 0 {
		maxLabels := 3
		if len(issue.Labels) < maxLabels {
			maxLabels = len(issue.Labels)
		}
		var labelParts []string
		for i := 0; i < maxLabels; i++ {
			labelParts = append(labelParts, truncateRunesHelper(issue.Labels[i], 8, ""))
		}
		labelText := strings.Join(labelParts, ",")
		labelStyle := t.Renderer.NewStyle().Foreground(t.InProgress)
		meta = append(meta, labelStyle.Render(labelText))
	}

	line3 := ""
	if len(meta) > 0 {
		line3 = strings.Join(meta, " ")
	}

	// Render card with 3 content lines (line4 removed to eliminate extra vertical gap)
	return cardStyle.Render(lipgloss.JoinVertical(lipgloss.Left, line1, line2, line3))
}

// renderExpandedCard creates an expanded inline view of a card (bv-i3ii)
// Shows full description, dependencies with titles, and all labels
// Note: colIdx, rowIdx kept for API consistency with renderCard but unused since
// expanded card is always the selected card (no separate search highlighting needed)
func (b BoardModel) renderExpandedCard(issue model.Issue, width int, _, _ int) string {
	t := b.theme

	// ══════════════════════════════════════════════════════════════════════════
	// DETERMINE BLOCKING STATUS for color coding (same as renderCard)
	// ══════════════════════════════════════════════════════════════════════════
	hasBlockingDeps := false
	for _, dep := range issue.Dependencies {
		if dep != nil && dep.Type.IsBlocking() {
			hasBlockingDeps = true
			break
		}
	}
	blocksOthers := len(b.blocksIndex[issue.ID]) > 0

	// ══════════════════════════════════════════════════════════════════════════
	// CARD STYLING - Expanded card is always selected (since we expand selected)
	// ══════════════════════════════════════════════════════════════════════════
	cardStyle := t.Renderer.NewStyle().
		Width(width).
		Padding(0, 1).
		MarginBottom(1)

	// Border color based on blocking status
	var borderColor lipgloss.TerminalColor
	if hasBlockingDeps {
		borderColor = lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#ef5350"} // Red - blocked
	} else if blocksOthers {
		borderColor = lipgloss.AdaptiveColor{Light: "#f57c00", Dark: "#ffb74d"} // Yellow - high impact
	} else if issue.Status == model.StatusOpen {
		borderColor = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // Green - ready
	} else {
		borderColor = t.Primary // Selected uses primary
	}

	cardStyle = cardStyle.
		Background(t.Highlight).
		Border(lipgloss.DoubleBorder()). // Double border to distinguish expanded state
		BorderForeground(borderColor)

	// ══════════════════════════════════════════════════════════════════════════
	// HEADER: Type icon + Priority + ID + Expand indicator
	// ══════════════════════════════════════════════════════════════════════════
	icon, iconColor := t.GetTypeIcon(string(issue.IssueType))
	prioText := formatPriority(issue.Priority)
	prioStyle := t.Renderer.NewStyle().Bold(true)
	if issue.Priority <= 1 {
		prioStyle = prioStyle.Foreground(lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#ef5350"})
	} else {
		prioStyle = prioStyle.Foreground(t.Secondary)
	}

	header := fmt.Sprintf("%s %s %s ▼",
		t.Renderer.NewStyle().Foreground(iconColor).Render(icon),
		prioStyle.Render(prioText),
		t.Renderer.NewStyle().Bold(true).Foreground(t.Primary).Render(issue.ID),
	)

	// ══════════════════════════════════════════════════════════════════════════
	// TITLE: Full title (not truncated)
	// ══════════════════════════════════════════════════════════════════════════
	titleStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)
	title := titleStyle.Render(issue.Title)

	// ══════════════════════════════════════════════════════════════════════════
	// SEPARATOR
	// ══════════════════════════════════════════════════════════════════════════
	sepStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	separator := sepStyle.Render(strings.Repeat("─", width-4))

	// ══════════════════════════════════════════════════════════════════════════
	// DESCRIPTION: First ~8 lines, rendered with glamour if possible
	// ══════════════════════════════════════════════════════════════════════════
	var descLines []string
	if issue.Description != "" {
		// Limit description to ~8 lines
		lines := strings.Split(issue.Description, "\n")
		maxLines := 8
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		desc := strings.Join(lines, "\n")

		// Render with markdown if possible
		rendered := desc
		if b.mdRenderer != nil {
			if md, err := b.mdRenderer.Render(desc); err == nil {
				rendered = strings.TrimSpace(md)
			}
		}
		descLines = append(descLines, rendered)
	}

	// ══════════════════════════════════════════════════════════════════════════
	// DEPENDENCIES: Show blocking deps with titles
	// ══════════════════════════════════════════════════════════════════════════
	var depLines []string
	var blockingDeps []*model.Dependency
	for _, dep := range issue.Dependencies {
		if dep != nil && dep.Type.IsBlocking() {
			blockingDeps = append(blockingDeps, dep)
		}
	}
	if len(blockingDeps) > 0 {
		depLines = append(depLines, t.Renderer.NewStyle().Bold(true).Foreground(t.Blocked).Render("Blocked by:"))
		for _, dep := range blockingDeps {
			blockerText := fmt.Sprintf("  • %s", dep.DependsOnID)
			if blocker, ok := b.issueMap[dep.DependsOnID]; ok && blocker != nil {
				blockerText = fmt.Sprintf("  • %s: %s (%s)", dep.DependsOnID, blocker.Title, blocker.Status)
			}
			depLines = append(depLines, t.Renderer.NewStyle().Foreground(t.Blocked).Render(blockerText))
		}
	}

	// Show what this blocks
	if blockedIDs, ok := b.blocksIndex[issue.ID]; ok && len(blockedIDs) > 0 {
		depLines = append(depLines, t.Renderer.NewStyle().Bold(true).Foreground(t.Feature).Render("Blocks:"))
		for _, blockedID := range blockedIDs {
			blockedText := fmt.Sprintf("  • %s", blockedID)
			if blocked, ok := b.issueMap[blockedID]; ok && blocked != nil {
				blockedText = fmt.Sprintf("  • %s: %s", blockedID, blocked.Title)
			}
			depLines = append(depLines, t.Renderer.NewStyle().Foreground(t.Feature).Render(blockedText))
		}
	}

	// ══════════════════════════════════════════════════════════════════════════
	// LABELS: Full label list
	// ══════════════════════════════════════════════════════════════════════════
	var labelLine string
	if len(issue.Labels) > 0 {
		labelStyle := t.Renderer.NewStyle().Foreground(t.InProgress)
		labelLine = labelStyle.Render("🏷 " + strings.Join(issue.Labels, ", "))
	}

	// ══════════════════════════════════════════════════════════════════════════
	// TIMESTAMPS
	// ══════════════════════════════════════════════════════════════════════════
	timeStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
	timestamps := timeStyle.Render(fmt.Sprintf("Created: %s | Updated: %s",
		FormatTimeRel(issue.CreatedAt), FormatTimeRel(issue.UpdatedAt)))

	// ══════════════════════════════════════════════════════════════════════════
	// ASSEMBLE CARD
	// ══════════════════════════════════════════════════════════════════════════
	var parts []string
	parts = append(parts, header, title, separator)
	parts = append(parts, descLines...)
	if len(depLines) > 0 {
		parts = append(parts, "") // blank line
		parts = append(parts, depLines...)
	}
	if labelLine != "" {
		parts = append(parts, "") // blank line
		parts = append(parts, labelLine)
	}
	parts = append(parts, separator, timestamps)

	return cardStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// renderDetailPanel renders the detail panel for the selected issue (bv-r6kh)
func (b *BoardModel) renderDetailPanel(width, height int) string {
	t := b.theme

	// Get the selected issue
	issue := b.SelectedIssue()

	// Update viewport dimensions
	vpWidth := width - 4 // Account for border
	vpHeight := height - 6
	if vpWidth < 20 {
		vpWidth = 20
	}
	if vpHeight < 5 {
		vpHeight = 5
	}
	b.detailVP.Width = vpWidth
	b.detailVP.Height = vpHeight

	// Build content based on selection state
	if issue == nil {
		// No issue selected - show help text (use special marker to detect "no selection" state)
		if b.lastDetailID != "_none_" {
			b.lastDetailID = "_none_"
			helpText := "## No Selection\n\nNavigate to a card with **h/l** and **j/k** to see details here.\n\nPress **Tab** to hide this panel."
			rendered := helpText
			if b.mdRenderer != nil {
				if md, err := b.mdRenderer.Render(helpText); err == nil {
					rendered = md
				}
			}
			b.detailVP.SetContent(rendered)
			b.detailVP.GotoTop()
		}
	} else {
		// Issue selected - only update content if the issue changed
		if b.lastDetailID != issue.ID {
			b.lastDetailID = issue.ID

			var content strings.Builder

			// Header with ID and type
			icon, _ := t.GetTypeIcon(string(issue.IssueType))
			content.WriteString(fmt.Sprintf("## %s %s\n\n", icon, issue.ID))

			// Title
			content.WriteString(fmt.Sprintf("**%s**\n\n", issue.Title))

			// Status and Priority
			statusIcon := GetStatusIcon(string(issue.Status))
			prioIcon := GetPriorityIcon(issue.Priority)
			content.WriteString(fmt.Sprintf("%s %s  %s P%d\n\n",
				statusIcon, issue.Status, prioIcon, issue.Priority))

			// Metadata section
			if issue.Assignee != "" {
				content.WriteString(fmt.Sprintf("**Assignee:** @%s\n\n", issue.Assignee))
			}

			if len(issue.Labels) > 0 {
				content.WriteString(fmt.Sprintf("**Labels:** %s\n\n", strings.Join(issue.Labels, ", ")))
			}

			// Dependencies - show with titles and status (bv-kklp)
			// First count blocking deps to avoid empty "Blocked by:" header
			var blockingDeps []*model.Dependency
			for _, dep := range issue.Dependencies {
				if dep != nil && dep.Type.IsBlocking() {
					blockingDeps = append(blockingDeps, dep)
				}
			}
			if len(blockingDeps) > 0 {
				content.WriteString("**Blocked by:**\n")
				for _, dep := range blockingDeps {
					// Look up blocker info for richer display
					if blocker, ok := b.issueMap[dep.DependsOnID]; ok && blocker != nil {
						content.WriteString(fmt.Sprintf("- %s: %s (%s)\n",
							dep.DependsOnID, blocker.Title, blocker.Status))
					} else {
						content.WriteString(fmt.Sprintf("- %s\n", dep.DependsOnID))
					}
				}
				content.WriteString("\n")
			}

			// Show what this issue blocks (bv-kklp)
			if blockedIDs, ok := b.blocksIndex[issue.ID]; ok && len(blockedIDs) > 0 {
				content.WriteString("**Blocks:**\n")
				for _, blockedID := range blockedIDs {
					if blocked, ok := b.issueMap[blockedID]; ok && blocked != nil {
						content.WriteString(fmt.Sprintf("- %s: %s\n", blockedID, blocked.Title))
					} else {
						content.WriteString(fmt.Sprintf("- %s\n", blockedID))
					}
				}
				content.WriteString(fmt.Sprintf("\n💡 Completing this would unblock %d issue(s)\n\n", len(blockedIDs)))
			}

			// Description
			if issue.Description != "" {
				content.WriteString("---\n\n")
				content.WriteString(issue.Description)
				content.WriteString("\n")
			}

			// Resolution (for closed issues with close_reason)
			if issue.Status.IsClosed() && issue.CloseReason != nil && *issue.CloseReason != "" {
				content.WriteString("\n---\n\n")
				content.WriteString("**Resolution:**\n\n")
				content.WriteString(*issue.CloseReason)
				content.WriteString("\n")
			}

			// Timestamps
			content.WriteString("\n---\n\n")
			content.WriteString(fmt.Sprintf("*Created: %s*\n", FormatTimeRel(issue.CreatedAt)))
			content.WriteString(fmt.Sprintf("*Updated: %s*\n", FormatTimeRel(issue.UpdatedAt)))

			// Render with markdown
			rendered := content.String()
			if b.mdRenderer != nil {
				if md, err := b.mdRenderer.Render(rendered); err == nil {
					rendered = md
				}
			}
			b.detailVP.SetContent(rendered)
			b.detailVP.GotoTop()
		}
	}

	// Build scroll indicator
	var sb strings.Builder
	sb.WriteString(b.detailVP.View())

	scrollPercent := b.detailVP.ScrollPercent()
	if scrollPercent < 1.0 || b.detailVP.YOffset > 0 {
		scrollHint := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Italic(true).
			Render(fmt.Sprintf("─ %d%% ─ ctrl+j/k", int(scrollPercent*100)))
		sb.WriteString("\n")
		sb.WriteString(scrollHint)
	}

	// Panel border style
	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Width(width).
		Height(height).
		Padding(0, 1)

	// Title bar
	titleBar := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Width(width - 4).
		Align(lipgloss.Center).
		Render("DETAILS")

	return panelStyle.Render(lipgloss.JoinVertical(lipgloss.Left, titleBar, sb.String()))
}
