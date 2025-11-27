package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/lipgloss"
)

// BoardModel represents the Kanban board view with adaptive columns
type BoardModel struct {
	columns      [4][]model.Issue
	activeColIdx []int  // Indices of non-empty columns (for navigation)
	focusedCol   int    // Index into activeColIdx
	selectedRow  [4]int // Store selection for each column
	ready        bool
	width        int
	height       int
	theme        Theme
}

// Column indices for the Kanban board
const (
	ColOpen       = 0
	ColInProgress = 1
	ColBlocked    = 2
	ColClosed     = 3
)

// sortIssuesByPriorityAndDate sorts issues by priority (ascending) then by creation date (descending)
func sortIssuesByPriorityAndDate(issues []model.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Priority != issues[j].Priority {
			return issues[i].Priority < issues[j].Priority
		}
		return issues[i].CreatedAt.After(issues[j].CreatedAt)
	})
}

// updateActiveColumns rebuilds the list of non-empty column indices
func (b *BoardModel) updateActiveColumns() {
	b.activeColIdx = nil
	for i := 0; i < 4; i++ {
		if len(b.columns[i]) > 0 {
			b.activeColIdx = append(b.activeColIdx, i)
		}
	}
	// If all columns are empty, include all columns anyway
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

// NewBoardModel creates a new Kanban board from the given issues
func NewBoardModel(issues []model.Issue, theme Theme) BoardModel {
	var cols [4][]model.Issue

	// Distribute issues into columns by status
	for _, issue := range issues {
		switch issue.Status {
		case model.StatusOpen:
			cols[ColOpen] = append(cols[ColOpen], issue)
		case model.StatusInProgress:
			cols[ColInProgress] = append(cols[ColInProgress], issue)
		case model.StatusBlocked:
			cols[ColBlocked] = append(cols[ColBlocked], issue)
		case model.StatusClosed:
			cols[ColClosed] = append(cols[ColClosed], issue)
		}
	}

	// Sort each column
	for i := 0; i < 4; i++ {
		sortIssuesByPriorityAndDate(cols[i])
	}

	b := BoardModel{
		columns:    cols,
		focusedCol: 0,
		theme:      theme,
	}
	b.updateActiveColumns()
	return b
}

// SetIssues updates the board data, typically after filtering
func (b *BoardModel) SetIssues(issues []model.Issue) {
	var cols [4][]model.Issue

	for _, issue := range issues {
		switch issue.Status {
		case model.StatusOpen:
			cols[ColOpen] = append(cols[ColOpen], issue)
		case model.StatusInProgress:
			cols[ColInProgress] = append(cols[ColInProgress], issue)
		case model.StatusBlocked:
			cols[ColBlocked] = append(cols[ColBlocked], issue)
		case model.StatusClosed:
			cols[ColClosed] = append(cols[ColClosed], issue)
		}
	}

	// Sort each column
	for i := 0; i < 4; i++ {
		sortIssuesByPriorityAndDate(cols[i])
	}

	b.columns = cols

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
	col := b.actualFocusedCol()
	if b.selectedRow[col] > 0 {
		b.selectedRow[col]--
	}
}

func (b *BoardModel) MoveRight() {
	if b.focusedCol < len(b.activeColIdx)-1 {
		b.focusedCol++
	}
}

func (b *BoardModel) MoveLeft() {
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

	// Calculate column widths - distribute space proportionally
	// Minimum column width for readability
	minColWidth := 28
	maxColWidth := 60

	// Calculate available width (subtract gaps between columns)
	gaps := numCols - 1
	availableWidth := width - (gaps * 2) // 2 chars gap between columns

	// Base width per column
	baseWidth := availableWidth / numCols
	if baseWidth < minColWidth {
		baseWidth = minColWidth
	}
	if baseWidth > maxColWidth {
		baseWidth = maxColWidth
	}

	colHeight := height - 4 // Account for header
	if colHeight < 8 {
		colHeight = 8
	}

	columnTitles := []string{"OPEN", "IN PROGRESS", "BLOCKED", "CLOSED"}
	columnColors := []lipgloss.AdaptiveColor{t.Open, t.InProgress, t.Blocked, t.Closed}
	columnEmoji := []string{"📋", "🔄", "🚫", "✅"}

	var renderedCols []string

	for i, colIdx := range b.activeColIdx {
		isFocused := b.focusedCol == i
		issues := b.columns[colIdx]
		issueCount := len(issues)

		// Header with emoji, title, and count
		headerText := fmt.Sprintf("%s %s (%d)", columnEmoji[colIdx], columnTitles[colIdx], issueCount)
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

		// Calculate visible rows
		// Cards have 3 content lines + 1 margin, plus borders:
		// - Non-selected: bottom border only (+1) = ~5 lines
		// - Selected: full rounded border (+2) = ~6 lines
		// Use 5 as average to avoid overflow
		cardHeight := 5
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

			card := b.renderCard(issue, baseWidth-4, isSelected, colIdx)
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
	return lipgloss.JoinHorizontal(lipgloss.Top, renderedCols...)
}

// renderCard creates a visually rich card for an issue with Stripe-level polish
func (b BoardModel) renderCard(issue model.Issue, width int, selected bool, colIdx int) string {
	t := b.theme

	// ══════════════════════════════════════════════════════════════════════════
	// CARD STYLING
	// ══════════════════════════════════════════════════════════════════════════
	cardStyle := t.Renderer.NewStyle().
		Width(width).
		Padding(0, 1).
		MarginBottom(1)

	if selected {
		// Selected: elevated with accent border
		cardStyle = cardStyle.
			Background(t.Highlight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary)
	} else {
		// Unselected: subtle card with bottom border
		cardStyle = cardStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Border)
	}

	// ══════════════════════════════════════════════════════════════════════════
	// LINE 1: Type icon + Priority + ID
	// ══════════════════════════════════════════════════════════════════════════
	icon, iconColor := t.GetTypeIcon(string(issue.IssueType))
	prioIcon := GetPriorityIcon(issue.Priority)

	// Truncate ID for narrow cards
	maxIDLen := width - 8
	if maxIDLen < 6 {
		maxIDLen = 6
	}
	displayID := truncateRunesHelper(issue.ID, maxIDLen, "…")

	line1 := fmt.Sprintf("%s %s %s",
		t.Renderer.NewStyle().Foreground(iconColor).Render(icon),
		prioIcon,
		t.Renderer.NewStyle().Bold(true).Foreground(t.Secondary).Render(displayID),
	)

	// ══════════════════════════════════════════════════════════════════════════
	// LINE 2: Title with selection highlighting
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
	// LINE 3: Metadata chips (assignee, deps, labels, age)
	// ══════════════════════════════════════════════════════════════════════════
	var meta []string

	// Assignee chip
	if issue.Assignee != "" {
		assignee := truncateRunesHelper(issue.Assignee, 8, "…")
		meta = append(meta, t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Render("@"+assignee))
	}

	// Dependencies chip with count
	depCount := len(issue.Dependencies)
	if depCount > 0 {
		depStyle := t.Renderer.NewStyle().Foreground(t.Feature)
		meta = append(meta, depStyle.Render(fmt.Sprintf("→%d", depCount)))
	}

	// Labels chip (first label + count)
	if len(issue.Labels) > 0 {
		labelPreview := truncateRunesHelper(issue.Labels[0], 6, "")
		labelText := labelPreview
		if len(issue.Labels) > 1 {
			labelText += fmt.Sprintf("+%d", len(issue.Labels)-1)
		}
		labelStyle := t.Renderer.NewStyle().
			Foreground(t.InProgress).
			Padding(0, 0)
		meta = append(meta, labelStyle.Render(labelText))
	}

	line3 := ""
	if len(meta) > 0 {
		line3 = strings.Join(meta, " ")
	} else {
		// Show age if no other metadata
		age := FormatTimeRel(issue.UpdatedAt)
		line3 = t.Renderer.NewStyle().
			Foreground(ColorMuted).
			Italic(true).
			Render(age)
	}

	return cardStyle.Render(lipgloss.JoinVertical(lipgloss.Left, line1, line2, line3))
}
