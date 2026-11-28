package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type actionableWhatIf struct {
	DirectUnblocks     int
	TransitiveUnblocks int
	EstimatedDaysSaved float64
}

// ActionableModel represents the actionable items view grouped by tracks
type ActionableModel struct {
	plan              analysis.ExecutionPlan
	selectedTrack     int
	selectedItem      int
	scrollOffset      int
	width             int
	height            int
	theme             Theme
	capacityText      string
	etaByID           map[string]string
	riskByID          map[string]analysis.RiskSignals
	whatIfByID        map[string]actionableWhatIf
	whatIfBatch       []string
	whatIfBatchSet    map[string]bool
	whatIfBatchResult *analysis.WhatIfBatchResult
}

// ToggleWhatIfBatch adds or removes id from the multi-issue Batch What-If selection (W32-1).
// Returns true if id is now in the batch, false if removed.
func (m *ActionableModel) ToggleWhatIfBatch(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if m.whatIfBatchSet == nil {
		m.whatIfBatchSet = make(map[string]bool)
	}
	if m.whatIfBatchSet[id] {
		delete(m.whatIfBatchSet, id)
		filtered := m.whatIfBatch[:0]
		for _, existing := range m.whatIfBatch {
			if existing != id {
				filtered = append(filtered, existing)
			}
		}
		m.whatIfBatch = filtered
		if len(m.whatIfBatch) == 0 {
			m.whatIfBatchResult = nil
		}
		m.ensureVisible()
		return false
	}
	m.whatIfBatchSet[id] = true
	m.whatIfBatch = append(m.whatIfBatch, id)
	m.ensureVisible()
	return true
}

// ClearWhatIfBatch clears the active Batch What-If selection and cached result.
func (m *ActionableModel) ClearWhatIfBatch() {
	m.whatIfBatch = nil
	m.whatIfBatchSet = nil
	m.whatIfBatchResult = nil
	m.ensureVisible()
}

// WhatIfBatchIDs returns a defensive copy of the currently selected Batch What-If IDs.
func (m *ActionableModel) WhatIfBatchIDs() []string {
	if len(m.whatIfBatch) == 0 {
		return nil
	}
	out := make([]string, len(m.whatIfBatch))
	copy(out, m.whatIfBatch)
	return out
}

// HasWhatIfBatch reports whether any issues are queued in Batch What-If mode.
func (m *ActionableModel) HasWhatIfBatch() bool {
	return len(m.whatIfBatch) > 0
}

// IsInWhatIfBatch reports whether id is currently in the Batch What-If selection.
func (m *ActionableModel) IsInWhatIfBatch(id string) bool {
	return m.whatIfBatchSet[id]
}

// SetWhatIfBatchResult stores the evaluated multi-issue Batch What-If delta.
func (m *ActionableModel) SetWhatIfBatchResult(res *analysis.WhatIfBatchResult) {
	m.whatIfBatchResult = res
	m.ensureVisible()
}

// WhatIfBatchResult returns the currently cached Batch What-If result (may be nil).
func (m *ActionableModel) WhatIfBatchResult() *analysis.WhatIfBatchResult {
	return m.whatIfBatchResult
}

func (m *ActionableModel) hasBatchBanner() bool {
	return len(m.whatIfBatch) > 0 && m.whatIfBatchResult != nil
}

// SetCapacitySummary attaches a compact inline capacity summary rendered in the header bar.
func (m *ActionableModel) SetCapacitySummary(text string) {
	m.capacityText = text
}

// SetETAByID attaches compact per-item ETA labels (e.g. "~1.5d") for track cards.
func (m *ActionableModel) SetETAByID(eta map[string]string) {
	m.etaByID = eta
}

// SetRiskByID attaches scoped operational risk signals for compact high-risk track indicators (W33-5).
func (m *ActionableModel) SetRiskByID(risk map[string]analysis.RiskSignals) {
	m.riskByID = risk
}

// SetWhatIfByID attaches transitive What-If unblock summaries for track cards.
func (m *ActionableModel) SetWhatIfByID(whatIf map[string]actionableWhatIf) {
	m.whatIfByID = whatIf
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

// PageUp moves selection up by a page
func (m *ActionableModel) PageUp() {
	if len(m.plan.Tracks) == 0 {
		return
	}
	pageSize := m.height / 2
	if pageSize < 1 {
		pageSize = 1
	}
	for i := 0; i < pageSize; i++ {
		m.MoveUp()
	}
}

// PageDown moves selection down by a page
func (m *ActionableModel) PageDown() {
	if len(m.plan.Tracks) == 0 {
		return
	}
	pageSize := m.height / 2
	if pageSize < 1 {
		pageSize = 1
	}
	for i := 0; i < pageSize; i++ {
		m.MoveDown()
	}
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
		items := len(m.plan.Tracks[m.selectedTrack].Items)
		if items > 0 {
			m.selectedItem = items - 1
		} else {
			m.selectedItem = 0
		}
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

// GoToStart jumps to the first actionable item in the first non-empty track.
func (m *ActionableModel) GoToStart() {
	if len(m.plan.Tracks) == 0 {
		return
	}
	m.selectedTrack = 0
	m.selectedItem = 0
	m.ensureVisible()
}

// GoToEnd jumps to the last actionable item in the last track.
func (m *ActionableModel) GoToEnd() {
	if len(m.plan.Tracks) == 0 {
		return
	}
	m.selectedTrack = len(m.plan.Tracks) - 1
	items := len(m.plan.Tracks[m.selectedTrack].Items)
	if items > 0 {
		m.selectedItem = items - 1
	} else {
		m.selectedItem = 0
	}
	m.ensureVisible()
}

// SelectIssueByID selects the specified issue ID if present in any execution track.
func (m *ActionableModel) SelectIssueByID(id string) bool {
	if id == "" || len(m.plan.Tracks) == 0 {
		return false
	}
	for tIdx, track := range m.plan.Tracks {
		for iIdx, item := range track.Items {
			if item.ID == id {
				m.selectedTrack = tIdx
				m.selectedItem = iIdx
				m.ensureVisible()
				return true
			}
		}
	}
	return false
}

// hasSummaryBanner reports whether Render() emits the 2-line RECOMMENDED summary block.
func (m *ActionableModel) hasSummaryBanner() bool {
	return m.plan.Summary.HighestImpact != "" && m.plan.Summary.UnblocksCount > 0
}

// ensureVisible adjusts scroll to keep selection visible using exact Render() line offsets.
func (m *ActionableModel) ensureVisible() {
	if len(m.plan.Tracks) == 0 || m.selectedTrack < 0 || m.selectedTrack >= len(m.plan.Tracks) {
		m.scrollOffset = 0
		return
	}

	// Render() always starts with 2 header lines (header + blank line).
	lineNum := 2
	if m.hasSummaryBanner() {
		lineNum += 2 // summary banner + blank line
	}
	if m.hasBatchBanner() {
		lineNum += 2 // batch what-if banner + blank line
	}

	// Each preceding track emits: track header (1) + divider (1) + items (N) + trailing blank (1) = N + 3 lines.
	for i := 0; i < m.selectedTrack; i++ {
		lineNum += len(m.plan.Tracks[i].Items) + 3
	}

	trackHeaderStart := lineNum
	itemStart := lineNum + 2 + m.selectedItem // +2 for current track header + divider

	// Selected item expands by 1 extra line when it has UnblocksIDs.
	itemHeight := 1
	track := m.plan.Tracks[m.selectedTrack]
	if m.selectedItem >= 0 && m.selectedItem < len(track.Items) {
		if len(track.Items[m.selectedItem].UnblocksIDs) > 0 {
			itemHeight = 2
		}
	}

	visibleLines := m.height - 2
	if visibleLines < 1 {
		visibleLines = 1
	}

	topTarget := itemStart
	if m.selectedItem == 0 {
		if m.selectedTrack == 0 && itemStart+itemHeight <= visibleLines {
			topTarget = 0
		} else if itemStart+itemHeight-trackHeaderStart <= visibleLines {
			topTarget = trackHeaderStart
		}
	}

	if topTarget < m.scrollOffset {
		m.scrollOffset = topTarget
	}

	bottomLine := itemStart + itemHeight
	if bottomLine > m.scrollOffset+visibleLines {
		m.scrollOffset = bottomLine - visibleLines
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
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

	headerWidth := m.width - 4
	if headerWidth < 8 {
		headerWidth = 8
	}
	innerBannerWidth := m.width - 8
	if innerBannerWidth < 4 {
		innerBannerWidth = 4
	}

	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Base.GetForeground()).
		Background(t.Primary).
		Padding(0, 2).
		Width(headerWidth)

	headerText := fmt.Sprintf("⚡ ACTIONABLE ITEMS  │  %d items in %d tracks", totalItems, len(m.plan.Tracks))
	if m.capacityText != "" {
		headerText += "  │  " + m.capacityText
	}
	header := truncateRunesHelper(
		headerText,
		innerBannerWidth,
		"…",
	)
	lines = append(lines, headerStyle.Render(header))
	lines = append(lines, "")

	if len(m.plan.Tracks) == 0 {
		emptyStyle := t.Renderer.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Padding(2, 4).
			Width(headerWidth).
			Align(lipgloss.Center)
		lines = append(lines, emptyStyle.Render("✓ No actionable items. All tasks are either blocked or completed."))
		return strings.Join(lines, "\n")
	}

	// ══════════════════════════════════════════════════════════════════════════
	// IMPACT SUMMARY - Highlighted recommendation
	// ══════════════════════════════════════════════════════════════════════════
	if m.hasSummaryBanner() {
		summaryStyle := t.Renderer.NewStyle().
			Foreground(t.Open).
			Background(t.Highlight).
			Bold(true).
			Padding(0, 2).
			Width(headerWidth)
		summary := truncateRunesHelper(
			fmt.Sprintf("💡 RECOMMENDED: Start with %s → %s (unblocks %d)",
				m.plan.Summary.HighestImpact,
				m.plan.Summary.ImpactReason,
				m.plan.Summary.UnblocksCount),
			innerBannerWidth,
			"…",
		)
		lines = append(lines, summaryStyle.Render(summary))
		lines = append(lines, "")
	}

	if m.hasBatchBanner() {
		res := m.whatIfBatchResult
		batchStyle := t.Renderer.NewStyle().
			Foreground(t.Primary).
			Background(t.Highlight).
			Bold(true).
			Padding(0, 2).
			Width(headerWidth)
		parts := []string{
			fmt.Sprintf("⚡ BATCH WHAT-IF (%d)", len(m.whatIfBatch)),
			fmt.Sprintf("+%d direct", res.Delta.DirectUnblocks),
			fmt.Sprintf("+%d cascade", res.Delta.TransitiveUnblocks),
		}
		if res.SynergyUnblocks > 0 {
			synText := fmt.Sprintf("★ %d synergy", res.SynergyUnblocks)
			if len(res.SynergyIssueIDs) > 0 {
				shown := res.SynergyIssueIDs
				if len(shown) > 3 {
					shown = shown[:3]
				}
				synText += " (" + strings.Join(shown, ",") + ")"
			}
			parts = append(parts, synText)
		}
		if res.Delta.EstimatedDaysSaved >= 0.1 {
			parts = append(parts, fmt.Sprintf("~%.1fd saved", res.Delta.EstimatedDaysSaved))
		}
		parts = append(parts, "W toggle • Esc clear")
		batchText := truncateRunesHelper(strings.Join(parts, " · "), innerBannerWidth, "…")
		lines = append(lines, batchStyle.Render(batchText))
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

		trackNum := strings.TrimPrefix(track.TrackID, "track-")
		badge := trackBadgeStyle.Render(fmt.Sprintf("TRACK %s", trackNum))
		maxReasonWidth := m.width - lipgloss.Width(badge) - 5
		if maxReasonWidth < 4 {
			maxReasonWidth = 4
		}
		reasonText := truncateRunesHelper(track.Reason, maxReasonWidth, "…")

		trackLine := ansi.Truncate(badge+" "+trackReasonStyle.Render(reasonText), max(1, m.width-2), "…")
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

			if m.whatIfBatchSet[item.ID] {
				itemLine.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("[W] "))
			}

			// ID with secondary styling
			idStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
			if isSelected {
				idStyle = idStyle.Bold(true)
			}
			itemLine.WriteString(idStyle.Render(item.ID))
			if item.NotReady {
				itemLine.WriteString(t.Renderer.NewStyle().Foreground(t.Muted).Render(" [not-ready]"))
			}
			itemLine.WriteString(" ")

			// Title with selection highlighting (strictly fit single terminal line so ensureVisible() math stays exact)
			lineBudget := max(1, m.width-2)
			prefixWidth := lipgloss.Width(itemLine.String())
			unblockStr := ""
			if len(item.UnblocksIDs) > 0 {
				unblockStr = fmt.Sprintf(" →%d", len(item.UnblocksIDs))
			}
			unblockWidth := lipgloss.Width(unblockStr)
			etaLabel := m.etaByID[item.ID]
			etaWidth := 0
			if etaLabel != "" {
				etaWidth = lipgloss.Width(etaLabel) + 1
			}
			riskLabel := ""
			riskHigh := false
			riskWidth := 0
			if sig, ok := m.riskByID[item.ID]; ok && sig.CompositeRisk >= 0.50 {
				riskLabel = fmt.Sprintf("⚠%.0f%%", sig.CompositeRisk*100)
				riskHigh = sig.CompositeRisk >= 0.60
				riskWidth = lipgloss.Width(riskLabel) + 1
			}
			maxTitleLen := lineBudget - prefixWidth - unblockWidth - etaWidth - riskWidth
			if etaLabel != "" && maxTitleLen < 8 {
				// Drop optional ETA badge on narrow terminals rather than wrapping onto 2 lines.
				etaLabel = ""
				etaWidth = 0
				maxTitleLen = lineBudget - prefixWidth - unblockWidth - riskWidth
			}
			if riskLabel != "" && maxTitleLen < 8 {
				// Drop optional risk badge next so ID + title stay legible on narrow terminals.
				riskLabel = ""
				riskWidth = 0
				maxTitleLen = lineBudget - prefixWidth - unblockWidth
			}
			if maxTitleLen > 0 {
				title := truncateRunesHelper(item.Title, maxTitleLen, "…")

				titleStyle := t.Renderer.NewStyle()
				if isSelected {
					titleStyle = titleStyle.Foreground(t.Primary).Bold(true)
				} else {
					titleStyle = titleStyle.Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#E8E8E8"})
				}
				itemLine.WriteString(titleStyle.Render(title))
			}

			if riskLabel != "" {
				riskColor := t.InProgress
				if riskHigh {
					riskColor = t.Blocked
				}
				riskBadge := t.Renderer.NewStyle().
					Foreground(riskColor).
					Bold(true).
					Render(" " + riskLabel)
				itemLine.WriteString(riskBadge)
			}

			if etaLabel != "" {
				etaBadge := t.Renderer.NewStyle().
					Foreground(t.Muted).
					Render(" " + etaLabel)
				itemLine.WriteString(etaBadge)
			}

			// Unblocks count badge
			if unblockStr != "" {
				unblockBadge := t.Renderer.NewStyle().
					Foreground(t.Open).
					Bold(true).
					Render(unblockStr)
				itemLine.WriteString(unblockBadge)
			}

			// Style the line with background if selected (strictly single physical line for ensureVisible)
			lineStyle := t.Renderer.NewStyle().Width(lineBudget)
			if isSelected {
				lineStyle = lineStyle.Background(t.Highlight)
			}

			lines = append(lines, lineStyle.Render(ansi.Truncate(itemLine.String(), lineBudget, "…")))

			// Show unblocks detail for selected item
			if isSelected && len(item.UnblocksIDs) > 0 {
				unblocksStyle := t.Renderer.NewStyle().
					Foreground(t.Feature).
					Italic(true).
					PaddingLeft(8)
				unblocksText := "↳ Unblocks: " + strings.Join(item.UnblocksIDs, ", ")
				if wi, ok := m.whatIfByID[item.ID]; ok {
					var extra []string
					if wi.TransitiveUnblocks > len(item.UnblocksIDs) {
						extra = append(extra, fmt.Sprintf("%d total cascade", wi.TransitiveUnblocks))
					}
					if wi.EstimatedDaysSaved >= 0.1 {
						extra = append(extra, fmt.Sprintf("~%.1fd saved", wi.EstimatedDaysSaved))
					}
					if len(extra) > 0 {
						unblocksText += " (" + strings.Join(extra, ", ") + ")"
					}
				}
				unblocksText = ansi.Truncate(unblocksText, max(1, m.width-10), "…")
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
