package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"

	"github.com/charmbracelet/lipgloss"
)

// ActionableModel represents the actionable items view grouped by tracks
type ActionableModel struct {
	plan          analysis.ExecutionPlan
	selectedTrack int
	selectedItem  int
	scrollOffset  int
	width         int
	height        int
	theme         Theme
}

// NewActionableModel creates a new actionable view from execution plan
func NewActionableModel(plan analysis.ExecutionPlan, theme Theme) ActionableModel {
	return ActionableModel{
		plan:          plan,
		selectedTrack: 0,
		selectedItem:  0,
		scrollOffset:  0,
		theme:         theme,
	}
}

// SetSize updates the view dimensions
func (m *ActionableModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// MoveUp moves selection up
func (m *ActionableModel) MoveUp() {
	if len(m.plan.Tracks) == 0 {
		return
	}

	if m.selectedItem > 0 {
		m.selectedItem--
	} else if m.selectedTrack > 0 {
		m.selectedTrack--
		m.selectedItem = len(m.plan.Tracks[m.selectedTrack].Items) - 1
	}
	m.ensureVisible()
}

// MoveDown moves selection down
func (m *ActionableModel) MoveDown() {
	if len(m.plan.Tracks) == 0 {
		return
	}

	track := m.plan.Tracks[m.selectedTrack]
	if m.selectedItem < len(track.Items)-1 {
		m.selectedItem++
	} else if m.selectedTrack < len(m.plan.Tracks)-1 {
		m.selectedTrack++
		m.selectedItem = 0
	}
	m.ensureVisible()
}

// SelectedIssueID returns the ID of the currently selected issue
func (m *ActionableModel) SelectedIssueID() string {
	if len(m.plan.Tracks) == 0 {
		return ""
	}
	if m.selectedTrack >= len(m.plan.Tracks) {
		return ""
	}
	track := m.plan.Tracks[m.selectedTrack]
	if m.selectedItem >= len(track.Items) {
		return ""
	}
	return track.Items[m.selectedItem].ID
}

// ensureVisible adjusts scroll to keep selection visible
func (m *ActionableModel) ensureVisible() {
	// Calculate the line number of the current selection
	lineNum := 0
	for i := 0; i < m.selectedTrack; i++ {
		lineNum += 1 + len(m.plan.Tracks[i].Items) + 1 // header + items + blank
	}
	lineNum += 1 + m.selectedItem // header + item position

	// Calculate item height (expanded if selected and has unblocks)
	itemHeight := 1
	track := m.plan.Tracks[m.selectedTrack]
	if len(track.Items) > m.selectedItem {
		if len(track.Items[m.selectedItem].UnblocksIDs) > 0 {
			itemHeight = 2
		}
	}

	visibleLines := m.height - 2 // account for header (2 lines used in Render for header+blank)
	if visibleLines < 5 {
		visibleLines = 5
	}

	// Ensure top is visible
	if lineNum < m.scrollOffset {
		m.scrollOffset = lineNum
	}

	// Ensure bottom is visible
	// We need the last line of the item (lineNum + itemHeight - 1) to be visible
	// The last visible line index is m.scrollOffset + visibleLines - 1
	// So: lineNum + itemHeight - 1 <= m.scrollOffset + visibleLines - 1
	//     lineNum + itemHeight <= m.scrollOffset + visibleLines
	bottomLine := lineNum + itemHeight
	if bottomLine > m.scrollOffset+visibleLines {
		m.scrollOffset = bottomLine - visibleLines
	}
}

// Render renders the actionable view with polished card-based layout
func (m *ActionableModel) Render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	t := m.theme
	var lines []string

	// ══════════════════════════════════════════════════════════════════════════
	// HEADER - Polished title with summary stats
	// ══════════════════════════════════════════════════════════════════════════
	totalItems := 0
	for _, track := range m.plan.Tracks {
		totalItems += len(track.Items)
	}

	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Base.GetForeground()).
		Background(t.Primary).
		Padding(0, 2).
		Width(m.width - 4)

	header := fmt.Sprintf("⚡ ACTIONABLE ITEMS  │  %d items in %d tracks", totalItems, len(m.plan.Tracks))
	lines = append(lines, headerStyle.Render(header))
	lines = append(lines, "")

	if len(m.plan.Tracks) == 0 {
		emptyStyle := t.Renderer.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Padding(2, 4).
			Width(m.width - 4).
			Align(lipgloss.Center)
		lines = append(lines, emptyStyle.Render("✓ No actionable items. All tasks are either blocked or completed."))
		return strings.Join(lines, "\n")
	}

	// ══════════════════════════════════════════════════════════════════════════
	// IMPACT SUMMARY - Highlighted recommendation
	// ══════════════════════════════════════════════════════════════════════════
	if m.plan.Summary.HighestImpact != "" && m.plan.Summary.UnblocksCount > 0 {
		summaryStyle := t.Renderer.NewStyle().
			Foreground(t.Open).
			Background(t.Highlight).
			Bold(true).
			Padding(0, 2).
			Width(m.width - 4)
		summary := fmt.Sprintf("💡 RECOMMENDED: Start with %s → %s (unblocks %d)",
			m.plan.Summary.HighestImpact,
			m.plan.Summary.ImpactReason,
			m.plan.Summary.UnblocksCount)
		lines = append(lines, summaryStyle.Render(summary))
		lines = append(lines, "")
	}

	// ══════════════════════════════════════════════════════════════════════════
	// RENDER TRACKS - Card-based items with visual hierarchy
	// ══════════════════════════════════════════════════════════════════════════
	for trackIdx, track := range m.plan.Tracks {
		// Track header with pill-style badge
		trackBadgeStyle := t.Renderer.NewStyle().
			Foreground(t.Base.GetForeground()).
			Background(t.Secondary).
			Bold(true).
			Padding(0, 1)

		trackReasonStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Italic(true)

		trackNum := track.TrackID
		if len(trackNum) > 6 {
			trackNum = trackNum[6:] // Strip "track-" prefix
		}

		trackLine := trackBadgeStyle.Render(fmt.Sprintf("TRACK %s", trackNum)) +
			" " + trackReasonStyle.Render(track.Reason)
		lines = append(lines, trackLine)

		// Subtle divider
		divWidth := m.width - 4
		if divWidth < 0 {
			divWidth = 0
		}
		lines = append(lines, t.Renderer.NewStyle().Foreground(t.Highlight).Render(strings.Repeat("·", divWidth)))

		// Track items as mini-cards
		for itemIdx, item := range track.Items {
			isSelected := trackIdx == m.selectedTrack && itemIdx == m.selectedItem

			// Build the item card
			var itemLine strings.Builder

			// Selection indicator
			if isSelected {
				itemLine.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("▸ "))
			} else {
				itemLine.WriteString("  ")
			}

			// Tree connector with better styling
			connectorStyle := t.Renderer.NewStyle().Foreground(t.Subtext)
			if itemIdx < len(track.Items)-1 {
				itemLine.WriteString(connectorStyle.Render("├─ "))
			} else {
				itemLine.WriteString(connectorStyle.Render("└─ "))
			}

			// Priority badge (polished)
			itemLine.WriteString(GetPriorityIcon(item.Priority))
			itemLine.WriteString(" ")

			// ID with secondary styling
			idStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
			if isSelected {
				idStyle = idStyle.Bold(true)
			}
			itemLine.WriteString(idStyle.Render(item.ID))
			itemLine.WriteString(" ")

			// Title with selection highlighting
			maxTitleLen := m.width - lipgloss.Width(itemLine.String()) - 20
			if maxTitleLen < 10 {
				maxTitleLen = 10
			}
			title := truncateRunesHelper(item.Title, maxTitleLen, "…")

			titleStyle := t.Renderer.NewStyle()
			if isSelected {
				titleStyle = titleStyle.Foreground(t.Primary).Bold(true)
			} else {
				titleStyle = titleStyle.Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#E8E8E8"})
			}
			itemLine.WriteString(titleStyle.Render(title))

			// Unblocks count badge
			if len(item.UnblocksIDs) > 0 {
				unblockBadge := t.Renderer.NewStyle().
					Foreground(t.Open).
					Bold(true).
					Render(fmt.Sprintf(" →%d", len(item.UnblocksIDs)))
				itemLine.WriteString(unblockBadge)
			}

			// Style the line with background if selected
			lineStyle := t.Renderer.NewStyle().Width(m.width - 2)
			if isSelected {
				lineStyle = lineStyle.Background(t.Highlight)
			}

			lines = append(lines, lineStyle.Render(itemLine.String()))

			// Show unblocks detail for selected item
			if isSelected && len(item.UnblocksIDs) > 0 {
				unblocksStyle := t.Renderer.NewStyle().
					Foreground(t.Feature).
					Italic(true).
					PaddingLeft(8)
				unblocksText := "↳ Unblocks: " + strings.Join(item.UnblocksIDs, ", ")
				unblocksText = truncateRunesHelper(unblocksText, m.width-12, "...")
				lines = append(lines, unblocksStyle.Render(unblocksText))
			}
		}

		lines = append(lines, "") // Blank line between tracks
	}

	// ══════════════════════════════════════════════════════════════════════════
	// APPLY SCROLL OFFSET
	// ══════════════════════════════════════════════════════════════════════════
	visibleLines := m.height - 2
	if visibleLines < 1 {
		visibleLines = 1
	}

	startLine := m.scrollOffset
	if startLine > len(lines)-visibleLines {
		startLine = len(lines) - visibleLines
	}
	if startLine < 0 {
		startLine = 0
	}

	endLine := startLine + visibleLines
	if endLine > len(lines) {
		endLine = len(lines)
	}

	return strings.Join(lines[startLine:endLine], "\n")
}
