package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FlowMatrixModel renders an interactive dependency flow dashboard
// showing how labels block each other with visual impact indicators
type FlowMatrixModel struct {
	flow         *analysis.CrossLabelFlow
	issues       []model.Issue
	labelStats   []labelFlowStats
	cursor       int
	scrollOffset int
	width        int
	height       int
	theme        Theme
	focusPanel   int // 0 = labels list, 1 = detail panel
	ready        bool

	// Drill-down state
	showDrilldown   bool
	drilldownIssues []model.Issue // Adjacent blocker/blocked endpoints for each unique pair.
	drilldownCursor int
	drilldownScroll int
	drilldownTitle  string
}

// labelFlowStats holds computed stats for a single label
type labelFlowStats struct {
	Label           string
	OutgoingCount   int      // How many issues in other labels this blocks
	IncomingCount   int      // How many issues from other labels block this
	OutgoingLabels  []string // Labels this blocks
	IncomingLabels  []string // Labels that block this
	BottleneckScore float64  // Normalized blocking power
	IsBottleneck    bool     // In top bottlenecks
}

// NewFlowMatrixModel creates a new flow matrix dashboard
func NewFlowMatrixModel(theme Theme) FlowMatrixModel {
	return FlowMatrixModel{
		theme:      theme,
		focusPanel: 0,
	}
}

// SetData refreshes flow data while preserving a surviving label and endpoint.
func (m *FlowMatrixModel) SetData(flow *analysis.CrossLabelFlow, issues []model.Issue) {
	label := m.SelectedLabel()
	var blockerID, blockedID string
	endpoint := m.drilldownCursor % 2
	if m.SelectedDrilldownIssue() != nil {
		pairStart := m.drilldownCursor - endpoint
		if pairStart+1 < len(m.drilldownIssues) {
			blockerID = m.drilldownIssues[pairStart].ID
			blockedID = m.drilldownIssues[pairStart+1].ID
		}
	}
	m.flow = flow
	m.issues = issues
	m.computeStats()
	m.ready = len(m.labelStats) > 0
	m.cursor, m.scrollOffset = 0, 0
	found := false
	for i, stats := range m.labelStats {
		if stats.Label == label {
			m.cursor, found = i, true
			break
		}
	}
	m.ensureVisible()
	if !found {
		m.showDrilldown = false
		m.drilldownIssues = nil
		m.drilldownCursor, m.drilldownScroll = 0, 0
	} else if m.showDrilldown {
		m.openDrilldown()
		for i := 0; i+1 < len(m.drilldownIssues); i += 2 {
			if m.drilldownIssues[i].ID == blockerID && m.drilldownIssues[i+1].ID == blockedID {
				m.drilldownCursor = i + endpoint
				break
			}
		}
		m.ensureDrilldownVisible()
	}
}

// SetSize sets the available rendering dimensions
func (m *FlowMatrixModel) SetSize(width, height int) {
	m.width = max(0, width)
	m.height = max(0, height)
	m.ensureVisible()
	m.ensureDrilldownVisible()
}

// computeStats builds per-label statistics from the flow matrix
func (m *FlowMatrixModel) computeStats() {
	if m.flow == nil || len(m.flow.Labels) == 0 {
		m.labelStats = nil
		return
	}

	labels := m.flow.Labels
	n := len(labels)
	matrix := m.flow.FlowMatrix

	// Validate matrix dimensions to prevent panics
	if len(matrix) < n {
		m.labelStats = nil
		return
	}
	for i := 0; i < n; i++ {
		if len(matrix[i]) < n {
			m.labelStats = nil
			return
		}
	}

	// Build index for quick lookup
	labelIndex := make(map[string]int, n)
	for i, l := range labels {
		labelIndex[l] = i
	}

	// Compute stats for each label
	stats := make([]labelFlowStats, n)
	maxOutgoing := 0

	for i, label := range labels {
		stats[i].Label = label

		// Outgoing: row i sums
		for j := 0; j < n; j++ {
			if i != j && matrix[i][j] > 0 {
				stats[i].OutgoingCount += matrix[i][j]
				stats[i].OutgoingLabels = append(stats[i].OutgoingLabels, labels[j])
			}
		}

		// Incoming: column i sums
		for j := 0; j < n; j++ {
			if i != j && matrix[j][i] > 0 {
				stats[i].IncomingCount += matrix[j][i]
				stats[i].IncomingLabels = append(stats[i].IncomingLabels, labels[j])
			}
		}

		if stats[i].OutgoingCount > maxOutgoing {
			maxOutgoing = stats[i].OutgoingCount
		}
	}

	// Compute bottleneck scores (normalized)
	for i := range stats {
		if maxOutgoing > 0 {
			stats[i].BottleneckScore = float64(stats[i].OutgoingCount) / float64(maxOutgoing)
		}
	}

	// Mark bottlenecks (top N or those in BottleneckLabels)
	bottleneckSet := make(map[string]bool)
	for _, bl := range m.flow.BottleneckLabels {
		bottleneckSet[bl] = true
	}
	for i := range stats {
		stats[i].IsBottleneck = bottleneckSet[stats[i].Label]
	}

	// Sort by outgoing count (highest blocking power first)
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].OutgoingCount != stats[j].OutgoingCount {
			return stats[i].OutgoingCount > stats[j].OutgoingCount
		}
		return stats[i].Label < stats[j].Label
	})

	m.labelStats = stats
}

// Update handles keyboard input
func (m *FlowMatrixModel) Update(msg tea.KeyMsg) tea.Cmd {
	if m.showDrilldown {
		return m.updateDrilldown(msg)
	}

	key := msg.String()

	switch key {
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.cursor = 0
		m.scrollOffset = 0
	case "G", "end":
		if len(m.labelStats) > 0 {
			m.cursor = len(m.labelStats) - 1
			m.ensureVisible()
		}
	case "tab":
		m.focusPanel = (m.focusPanel + 1) % 2
	case "enter":
		if m.cursor < len(m.labelStats) {
			m.openDrilldown()
		}
	case "ctrl+d":
		m.moveCursor(m.visibleRows() / 2)
	case "ctrl+u":
		m.moveCursor(-m.visibleRows() / 2)
	}

	return nil
}

func (m *FlowMatrixModel) updateDrilldown(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc", "q":
		m.showDrilldown = false
	case "j", "down":
		if m.drilldownCursor < len(m.drilldownIssues)-1 {
			m.drilldownCursor++
			m.ensureDrilldownVisible()
		}
	case "k", "up":
		if m.drilldownCursor > 0 {
			m.drilldownCursor--
			m.ensureDrilldownVisible()
		}
	}
	return nil
}

func (m *FlowMatrixModel) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.labelStats) {
		m.cursor = len(m.labelStats) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureVisible()
}

func (m *FlowMatrixModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+visible {
		m.scrollOffset = m.cursor - visible + 1
	}
}

func (m *FlowMatrixModel) ensureDrilldownVisible() {
	visible := m.drilldownVisibleRows()
	if m.drilldownCursor < m.drilldownScroll {
		m.drilldownScroll = m.drilldownCursor / 2 * 2
	}
	if m.drilldownCursor >= m.drilldownScroll+visible {
		m.drilldownScroll = m.drilldownCursor/2*2 - visible + 2
	}
	m.drilldownScroll = min(m.drilldownScroll, max(0, len(m.drilldownIssues)-visible))
}

func (m *FlowMatrixModel) drilldownVisibleRows() int {
	// Keep both endpoints on screen together, including while scrolling.
	return max(2, (m.height-8)/2*2)
}

func (m *FlowMatrixModel) visibleRows() int {
	rows := m.height - 6 // header, footer, borders
	if rows < 3 {
		rows = 3
	}
	return rows
}

func (m *FlowMatrixModel) openDrilldown() {
	if m.cursor < 0 || m.cursor >= len(m.labelStats) || m.flow == nil {
		return
	}
	selectedLabel := m.labelStats[m.cursor].Label

	// The producer already decided which edges block. Label membership alone
	// includes unrelated work and misses the other endpoint of each relationship.
	issueMap := make(map[string]model.Issue, len(m.issues))
	for _, iss := range m.issues {
		issueMap[iss.ID] = iss
	}
	type pairKey struct{ blocker, blocked string }
	seen := make(map[pairKey]bool)
	var pairs []pairKey
	for _, dep := range m.flow.Dependencies {
		if dep.FromLabel != selectedLabel && dep.ToLabel != selectedLabel {
			continue
		}
		for _, pair := range dep.BlockingPairs {
			key := pairKey{pair.BlockerID, pair.BlockedID}
			_, blockerFound := issueMap[key.blocker]
			_, blockedFound := issueMap[key.blocked]
			if blockerFound && blockedFound && !seen[key] {
				seen[key] = true
				pairs = append(pairs, key)
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].blocker != pairs[j].blocker {
			return pairs[i].blocker < pairs[j].blocker
		}
		return pairs[i].blocked < pairs[j].blocked
	})
	m.drilldownIssues = make([]model.Issue, 0, 2*len(pairs))
	for _, pair := range pairs {
		m.drilldownIssues = append(m.drilldownIssues, issueMap[pair.blocker], issueMap[pair.blocked])
	}
	m.drilldownCursor = 0
	m.drilldownScroll = 0
	m.drilldownTitle = fmt.Sprintf("Dependencies involving: %s", selectedLabel)
	m.showDrilldown = true
}

// SelectedLabel returns the currently selected label (for drill-down from parent)
func (m *FlowMatrixModel) SelectedLabel() string {
	if m.cursor >= 0 && m.cursor < len(m.labelStats) {
		return m.labelStats[m.cursor].Label
	}
	return ""
}

// View renders the flow matrix dashboard
func (m FlowMatrixModel) View() string {
	if !m.ready {
		return m.theme.Base.Render("No cross-label dependencies found")
	}

	if m.showDrilldown {
		return m.renderDrilldown()
	}

	// Calculate panel widths with safety bounds
	leftWidth := m.width * 35 / 100 // 35% for labels list
	minLeftWidth := 25
	minRightWidth := 30
	sepWidth := 3 // border/separator space

	// Ensure we don't exceed total width
	if leftWidth < minLeftWidth {
		leftWidth = minLeftWidth
	}
	rightWidth := m.width - leftWidth - sepWidth
	if rightWidth < minRightWidth {
		rightWidth = minRightWidth
	}

	// If total exceeds width, scale down proportionally
	totalNeeded := leftWidth + rightWidth + sepWidth
	if totalNeeded > m.width && m.width > 0 {
		scale := float64(m.width-sepWidth) / float64(leftWidth+rightWidth)
		leftWidth = int(float64(leftWidth) * scale)
		rightWidth = m.width - leftWidth - sepWidth
		if leftWidth < 10 {
			leftWidth = 10
		}
		if rightWidth < 10 {
			rightWidth = m.width - leftWidth - sepWidth
			if rightWidth < 10 {
				rightWidth = 10
			}
		}
	}

	// Build panels
	leftPanel := m.renderLabelsPanel(leftWidth)
	rightPanel := m.renderDetailPanel(rightWidth)

	// Header
	header := m.renderHeader()

	// Join panels side by side
	leftLines := strings.Split(leftPanel, "\n")
	rightLines := strings.Split(rightPanel, "\n")

	// Normalize heights
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	for len(leftLines) < maxLines {
		leftLines = append(leftLines, strings.Repeat(" ", leftWidth))
	}
	for len(rightLines) < maxLines {
		rightLines = append(rightLines, strings.Repeat(" ", rightWidth))
	}

	var body strings.Builder
	separator := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Border).
		Render("│")

	for i := 0; i < maxLines; i++ {
		body.WriteString(leftLines[i])
		body.WriteString(" ")
		body.WriteString(separator)
		body.WriteString(" ")
		body.WriteString(rightLines[i])
		if i < maxLines-1 {
			body.WriteString("\n")
		}
	}

	// Footer
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body.String(), footer)
}

func (m FlowMatrixModel) renderHeader() string {
	titleStyle := m.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary).
		PaddingRight(2)

	statsStyle := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Subtext)

	title := titleStyle.Render("DEPENDENCY FLOW")
	stats := statsStyle.Render(fmt.Sprintf("│ %d labels │ %d cross-label deps │ %d bottlenecks",
		len(m.flow.Labels),
		m.flow.TotalCrossLabelDeps,
		len(m.flow.BottleneckLabels)))

	headerLine := lipgloss.JoinHorizontal(lipgloss.Left, title, stats)

	borderStyle := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Border)

	return lipgloss.JoinVertical(lipgloss.Left,
		headerLine,
		borderStyle.Render(strings.Repeat("─", m.width)))
}

func (m FlowMatrixModel) renderLabelsPanel(width int) string {
	var b strings.Builder

	// Panel header
	headerStyle := m.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(m.theme.Secondary).
		Width(width)

	focusIndicator := " "
	if m.focusPanel == 0 {
		focusIndicator = "▸"
	}
	b.WriteString(headerStyle.Render(focusIndicator + " LABELS (by blocking power)"))
	b.WriteString("\n")

	// Separator
	sepStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)
	b.WriteString(sepStyle.Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	// Find max for bar scaling
	maxOut := 1
	for _, s := range m.labelStats {
		if s.OutgoingCount > maxOut {
			maxOut = s.OutgoingCount
		}
	}

	// Visible rows
	visible := m.visibleRows()
	start := m.scrollOffset
	end := start + visible
	if end > len(m.labelStats) {
		end = len(m.labelStats)
	}

	// Bar width
	barWidth := width - 20 // space for label name and count
	if barWidth < 5 {
		barWidth = 5
	}

	for i := start; i < end; i++ {
		stat := m.labelStats[i]
		isSelected := i == m.cursor

		// Build the row
		row := m.renderLabelRow(stat, isSelected, barWidth, maxOut, width)
		b.WriteString(row)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	// Pad remaining space
	for i := end - start; i < visible; i++ {
		b.WriteString("\n")
		b.WriteString(strings.Repeat(" ", width))
	}

	return b.String()
}

func (m FlowMatrixModel) renderLabelRow(stat labelFlowStats, selected bool, barWidth, maxOut, totalWidth int) string {
	// Label name (truncated if needed, using rune count for UTF-8 safety)
	labelWidth := 12
	label := stat.Label
	labelRunes := []rune(label)
	if len(labelRunes) > labelWidth {
		label = string(labelRunes[:labelWidth-1]) + "…"
	}

	// Color based on bottleneck status
	var labelColor lipgloss.AdaptiveColor
	if stat.IsBottleneck {
		labelColor = m.theme.Blocked // Red for bottlenecks
	} else if stat.BottleneckScore > 0.5 {
		labelColor = m.theme.Feature // Orange for high impact
	} else if stat.OutgoingCount > 0 {
		labelColor = m.theme.Task // Yellow for some impact
	} else {
		labelColor = m.theme.Subtext // Gray for no impact
	}

	labelStyle := m.theme.Renderer.NewStyle().
		Foreground(labelColor).
		Width(labelWidth)

	// Bar visualization
	barFilled := 0
	if maxOut > 0 {
		barFilled = stat.OutgoingCount * barWidth / maxOut
	}
	if barFilled > barWidth {
		barFilled = barWidth
	}

	// Use different characters for bar intensity
	bar := ""
	if barFilled > 0 {
		bar = strings.Repeat("█", barFilled)
	}
	barEmpty := strings.Repeat("░", barWidth-barFilled)

	barStyle := m.theme.Renderer.NewStyle().Foreground(labelColor)
	emptyStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)

	// Count
	countStr := fmt.Sprintf("%3d", stat.OutgoingCount)

	// Assemble row
	row := fmt.Sprintf("%s %s%s %s",
		labelStyle.Render(fmt.Sprintf("%-*s", labelWidth, label)),
		barStyle.Render(bar),
		emptyStyle.Render(barEmpty),
		countStr)

	// Selection highlight
	if selected {
		selectStyle := m.theme.Renderer.NewStyle().
			Background(m.theme.Highlight).
			Width(totalWidth)
		row = selectStyle.Render(row)
	}

	return row
}

func (m FlowMatrixModel) renderDetailPanel(width int) string {
	var b strings.Builder

	if m.cursor >= len(m.labelStats) {
		return "Select a label"
	}

	stat := m.labelStats[m.cursor]

	// Panel header
	headerStyle := m.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	b.WriteString(headerStyle.Render(fmt.Sprintf("▸ %s", stat.Label)))
	b.WriteString("\n")

	// Separator
	sepStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)
	b.WriteString(sepStyle.Render(strings.Repeat("─", width)))
	b.WriteString("\n\n")

	// Stats summary
	summaryStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext)
	b.WriteString(summaryStyle.Render("IMPACT SUMMARY"))
	b.WriteString("\n")

	// Blocking power indicator
	scoreBar := m.renderScoreBar(stat.BottleneckScore, 20)
	scoreLabel := "Low"
	scoreColor := m.theme.Open
	if stat.BottleneckScore > 0.7 {
		scoreLabel = "HIGH"
		scoreColor = m.theme.Blocked
	} else if stat.BottleneckScore > 0.3 {
		scoreLabel = "Medium"
		scoreColor = m.theme.Feature
	}
	scoreStyle := m.theme.Renderer.NewStyle().Foreground(scoreColor).Bold(true)
	b.WriteString(fmt.Sprintf("  Blocking Power: %s %s\n", scoreBar, scoreStyle.Render(scoreLabel)))

	if stat.IsBottleneck {
		bottleneckStyle := m.theme.Renderer.NewStyle().
			Foreground(m.theme.Blocked).
			Bold(true)
		b.WriteString(bottleneckStyle.Render("  ⚠ BOTTLENECK"))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Two-column layout for blocks/blocked by
	halfWidth := (width - 4) / 2

	// BLOCKS section
	blocksHeader := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Blocked).
		Bold(true).
		Render(fmt.Sprintf("BLOCKS → (%d)", stat.OutgoingCount))

	blockedByHeader := m.theme.Renderer.NewStyle().
		Foreground(m.theme.InProgress).
		Bold(true).
		Render(fmt.Sprintf("← BLOCKED BY (%d)", stat.IncomingCount))

	b.WriteString(fmt.Sprintf("%-*s  %s\n", halfWidth, blocksHeader, blockedByHeader))

	// List entries
	maxEntries := 6
	outLabels := stat.OutgoingLabels
	inLabels := stat.IncomingLabels

	// Get counts for each label
	outCounts := m.getFlowCounts(stat.Label, outLabels, true)
	inCounts := m.getFlowCounts(stat.Label, inLabels, false)

	for i := 0; i < maxEntries; i++ {
		leftStr := ""
		rightStr := ""

		if i < len(outLabels) {
			count := outCounts[outLabels[i]]
			miniBar := m.miniBar(count, 5)
			leftStr = fmt.Sprintf("  %s %s (%d)", miniBar, outLabels[i], count)
		}
		if i < len(inLabels) {
			count := inCounts[inLabels[i]]
			miniBar := m.miniBar(count, 5)
			rightStr = fmt.Sprintf("  %s %s (%d)", miniBar, inLabels[i], count)
		}

		b.WriteString(fmt.Sprintf("%-*s  %s\n", halfWidth, leftStr, rightStr))
	}

	if len(outLabels) > maxEntries || len(inLabels) > maxEntries {
		moreStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext).Italic(true)
		leftMore := ""
		rightMore := ""
		if len(outLabels) > maxEntries {
			leftMore = fmt.Sprintf("  +%d more", len(outLabels)-maxEntries)
		}
		if len(inLabels) > maxEntries {
			rightMore = fmt.Sprintf("  +%d more", len(inLabels)-maxEntries)
		}
		b.WriteString(fmt.Sprintf("%-*s  %s\n", halfWidth, moreStyle.Render(leftMore), moreStyle.Render(rightMore)))
	}

	b.WriteString("\n")

	// Hint
	hintStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext).Italic(true)
	b.WriteString(hintStyle.Render("Press Enter to see issues"))

	return b.String()
}

func (m FlowMatrixModel) getFlowCounts(sourceLabel string, targetLabels []string, outgoing bool) map[string]int {
	counts := make(map[string]int)
	if m.flow == nil {
		return counts
	}

	// Build label index
	labelIndex := make(map[string]int)
	for i, l := range m.flow.Labels {
		labelIndex[l] = i
	}

	sourceIdx, ok := labelIndex[sourceLabel]
	if !ok {
		return counts
	}

	for _, target := range targetLabels {
		targetIdx, ok := labelIndex[target]
		if !ok {
			continue
		}
		if outgoing {
			counts[target] = m.flow.FlowMatrix[sourceIdx][targetIdx]
		} else {
			counts[target] = m.flow.FlowMatrix[targetIdx][sourceIdx]
		}
	}
	return counts
}

func (m FlowMatrixModel) renderScoreBar(score float64, width int) string {
	filled := int(score * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}

	var color lipgloss.AdaptiveColor
	if score > 0.7 {
		color = m.theme.Blocked
	} else if score > 0.3 {
		color = m.theme.Feature
	} else {
		color = m.theme.Open
	}

	barStyle := m.theme.Renderer.NewStyle().Foreground(color)
	emptyStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)

	return barStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", width-filled))
}

func (m FlowMatrixModel) miniBar(count, maxWidth int) string {
	if count <= 0 {
		return strings.Repeat("·", maxWidth)
	}

	// Scale linearly for visualization
	filled := count
	if filled > maxWidth {
		filled = maxWidth
	}

	var color lipgloss.AdaptiveColor
	if count >= 5 {
		color = m.theme.Blocked
	} else if count >= 2 {
		color = m.theme.Feature
	} else {
		color = m.theme.Task
	}

	barStyle := m.theme.Renderer.NewStyle().Foreground(color)
	return barStyle.Render(strings.Repeat("■", filled)) + strings.Repeat("·", maxWidth-filled)
}

func (m FlowMatrixModel) renderFooter() string {
	borderStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)
	helpStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext)

	help := "j/k: navigate  Enter: drill down  Tab: switch panel  Esc: close"

	return lipgloss.JoinVertical(lipgloss.Left,
		borderStyle.Render(strings.Repeat("─", m.width)),
		helpStyle.Render(help))
}

func (m FlowMatrixModel) renderDrilldown() string {
	var b strings.Builder

	// Header
	headerStyle := m.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	header := fmt.Sprintf("%s (%d relationships)", m.drilldownTitle, len(m.drilldownIssues)/2)
	b.WriteString(headerStyle.Render(truncateRunesHelper(header, m.width, "…")))
	b.WriteString("\n")

	borderStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n\n")

	if len(m.drilldownIssues) == 0 {
		b.WriteString(truncateRunesHelper("No blocking relationships found", m.width, "…"))
		return b.String()
	}

	// Visible rows
	visible := m.drilldownVisibleRows()
	start := m.drilldownScroll
	end := start + visible
	if end > len(m.drilldownIssues) {
		end = len(m.drilldownIssues)
	}

	for i := start; i < end; i++ {
		iss := m.drilldownIssues[i]
		selected := i == m.drilldownCursor

		// Status indicator
		statusColor := m.theme.GetStatusColor(string(iss.Status))
		statusStyle := m.theme.Renderer.NewStyle().Foreground(statusColor)
		statusIndicator := "●"

		prefix := ""
		if i%2 == 1 {
			prefix = "  blocks "
		}
		text := truncateRunesHelper(prefix+iss.ID+" "+iss.Title, max(0, m.width-2), "…")
		row := statusStyle.Render(statusIndicator) + " " + text
		if m.width < 2 {
			row = truncateRunesHelper(statusIndicator, m.width, "")
		}

		if selected {
			selectStyle := m.theme.Renderer.NewStyle().
				Background(m.theme.Highlight).
				Width(m.width)
			row = selectStyle.Render(row)
		}

		b.WriteString(row)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	// Footer
	b.WriteString("\n\n")
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")

	helpStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext)
	b.WriteString(helpStyle.Render(truncateRunesHelper("j/k: endpoint  Enter: details  Esc: back", m.width, "…")))

	return b.String()
}

// MoveUp moves the cursor up by one
func (m *FlowMatrixModel) MoveUp() {
	if m.showDrilldown {
		if m.drilldownCursor > 0 {
			m.drilldownCursor--
			m.ensureDrilldownVisible()
		}
	} else {
		m.moveCursor(-1)
	}
}

// MoveDown moves the cursor down by one
func (m *FlowMatrixModel) MoveDown() {
	if m.showDrilldown {
		if m.drilldownCursor < len(m.drilldownIssues)-1 {
			m.drilldownCursor++
			m.ensureDrilldownVisible()
		}
	} else {
		m.moveCursor(1)
	}
}

// TogglePanel switches focus between the labels list and detail panel
func (m *FlowMatrixModel) TogglePanel() {
	m.focusPanel = (m.focusPanel + 1) % 2
}

// OpenDrilldown opens the drill-down view for the selected label
func (m *FlowMatrixModel) OpenDrilldown() {
	m.openDrilldown()
}

// GoToStart moves cursor to the first item
func (m *FlowMatrixModel) GoToStart() {
	if m.showDrilldown {
		m.drilldownCursor = 0
		m.drilldownScroll = 0
	} else {
		m.cursor = 0
		m.scrollOffset = 0
	}
}

// GoToEnd moves cursor to the last item
func (m *FlowMatrixModel) GoToEnd() {
	if m.showDrilldown {
		if len(m.drilldownIssues) > 0 {
			m.drilldownCursor = len(m.drilldownIssues) - 1
			m.ensureDrilldownVisible()
		}
	} else {
		if len(m.labelStats) > 0 {
			m.cursor = len(m.labelStats) - 1
			m.ensureVisible()
		}
	}
}

// SelectedDrilldownIssue returns the currently selected issue in drilldown mode
func (m *FlowMatrixModel) SelectedDrilldownIssue() *model.Issue {
	if !m.showDrilldown || m.drilldownCursor < 0 || m.drilldownCursor >= len(m.drilldownIssues) {
		return nil
	}
	return &m.drilldownIssues[m.drilldownCursor]
}

// FlowMatrixView is the legacy function for backward compatibility
// It now returns a simple text summary pointing to the interactive view
func FlowMatrixView(flow analysis.CrossLabelFlow, width int) string {
	if len(flow.Labels) == 0 {
		return "No cross-label dependencies found"
	}

	var b strings.Builder
	b.WriteString("DEPENDENCY FLOW SUMMARY\n")
	b.WriteString(strings.Repeat("─", 40))
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("Labels: %d\n", len(flow.Labels)))
	b.WriteString(fmt.Sprintf("Cross-label dependencies: %d\n", flow.TotalCrossLabelDeps))
	b.WriteString(fmt.Sprintf("Bottleneck labels: %v\n", flow.BottleneckLabels))

	return b.String()
}
