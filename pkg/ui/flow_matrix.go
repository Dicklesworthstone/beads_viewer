package ui

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FlowMatrixModel renders an interactive dependency flow dashboard
// showing how labels block each other with visual impact indicators
type FlowMatrixModel struct {
	flow         *analysis.CrossLabelFlow
	cascade      analysis.BlockageCascadeAnalysis
	issues       []model.Issue
	referenceNow time.Time
	readiness    *model.ReadinessIndex
	candidateIDs map[string]bool
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

// SetScope configures the reference clock, readiness authority, and candidate selection
// used when computing blockage cascades.
func (m *FlowMatrixModel) SetScope(referenceNow time.Time, readiness *model.ReadinessIndex, candidateIDs map[string]bool) {
	m.referenceNow = referenceNow
	m.readiness = readiness
	m.candidateIDs = maps.Clone(candidateIDs)
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
	} else {
		wasDrilldown := m.showDrilldown
		m.populateDrilldownForSelectedLabel()
		m.showDrilldown = wasDrilldown
		if blockerID != "" || blockedID != "" {
			for i := 0; i+1 < len(m.drilldownIssues); i += 2 {
				if m.drilldownIssues[i].ID == blockerID && m.drilldownIssues[i+1].ID == blockedID {
					m.drilldownCursor = i + endpoint
					break
				}
			}
			m.ensureDrilldownVisible()
		}
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
		m.cascade = analysis.BlockageCascadeAnalysis{}
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
	m.cascade = analysis.ComputeBlockageCascadeAtWithScope(m.issues, *m.flow, analysis.DefaultLabelHealthConfig(), m.referenceNow, m.readiness, m.candidateIDs)
}

// Update handles keyboard input
func (m *FlowMatrixModel) Update(msg tea.KeyMsg) tea.Cmd {
	if m.showDrilldown {
		return m.updateDrilldown(msg)
	}

	key := msg.String()

	switch key {
	case "j", "down":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(1)
		} else {
			m.moveCursor(1)
		}
	case "k", "up":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(-1)
		} else {
			m.moveCursor(-1)
		}
	case "g", "home":
		if m.focusPanel == 1 {
			m.drilldownCursor = 0
			m.drilldownScroll = 0
		} else {
			m.cursor = 0
			m.scrollOffset = 0
			m.populateDrilldownForSelectedLabel()
		}
	case "G", "end":
		if m.focusPanel == 1 {
			if len(m.drilldownIssues) > 0 {
				m.drilldownCursor = len(m.drilldownIssues) - 1
				m.ensureDrilldownVisible()
			}
		} else if len(m.labelStats) > 0 {
			m.cursor = len(m.labelStats) - 1
			m.ensureVisible()
			m.populateDrilldownForSelectedLabel()
		}
	case "tab":
		m.TogglePanel()
	case "enter":
		if m.cursor < len(m.labelStats) {
			m.openDrilldown()
		}
	case "ctrl+d":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(max(1, m.activeDrilldownPageRows()/2))
		} else {
			m.moveCursor(max(1, m.visibleRows()/2))
		}
	case "ctrl+u":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(-max(1, m.activeDrilldownPageRows()/2))
		} else {
			m.moveCursor(-max(1, m.visibleRows()/2))
		}
	case "pgdown":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(max(2, m.activeDrilldownPageRows()))
		} else {
			m.moveCursor(max(1, m.visibleRows()))
		}
	case "pgup":
		if m.focusPanel == 1 {
			m.moveDrilldownCursor(-max(2, m.activeDrilldownPageRows()))
		} else {
			m.moveCursor(-max(1, m.visibleRows()))
		}
	}

	return nil
}

func (m *FlowMatrixModel) updateDrilldown(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc", "q":
		m.showDrilldown = false
		m.ensureDrilldownVisible()
	case "j", "down":
		m.moveDrilldownCursor(1)
	case "k", "up":
		m.moveDrilldownCursor(-1)
	case "g", "home":
		m.drilldownCursor = 0
		m.drilldownScroll = 0
	case "G", "end":
		if len(m.drilldownIssues) > 0 {
			m.drilldownCursor = len(m.drilldownIssues) - 1
			m.ensureDrilldownVisible()
		}
	case "ctrl+d":
		m.moveDrilldownCursor(max(1, m.activeDrilldownPageRows()/2))
	case "ctrl+u":
		m.moveDrilldownCursor(-max(1, m.activeDrilldownPageRows()/2))
	case "pgdown":
		m.moveDrilldownCursor(max(2, m.activeDrilldownPageRows()))
	case "pgup":
		m.moveDrilldownCursor(-max(2, m.activeDrilldownPageRows()))
	}
	return nil
}

func (m *FlowMatrixModel) moveCursor(delta int) {
	prev := m.cursor
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
	if m.cursor != prev || len(m.drilldownIssues) == 0 {
		m.populateDrilldownForSelectedLabel()
	}
}

func (m *FlowMatrixModel) moveDrilldownCursor(delta int) {
	if len(m.drilldownIssues) == 0 {
		m.populateDrilldownForSelectedLabel()
	}
	if len(m.drilldownIssues) == 0 {
		return
	}
	m.drilldownCursor += delta
	if m.drilldownCursor < 0 {
		m.drilldownCursor = 0
	}
	if m.drilldownCursor >= len(m.drilldownIssues) {
		m.drilldownCursor = len(m.drilldownIssues) - 1
	}
	m.ensureDrilldownVisible()
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
	start, _ := m.computeDrilldownWindow(m.activeDrilldownRawAvail())
	m.drilldownScroll = start
}

func (m FlowMatrixModel) activeDrilldownPageRows() int {
	_, avail := m.computeDrilldownWindow(m.activeDrilldownRawAvail())
	return max(2, avail)
}

func (m FlowMatrixModel) activeDrilldownRawAvail() int {
	if m.showDrilldown {
		return m.drilldownVisibleRows()
	}
	panelHeight := max(5, m.height-4)
	maxHeight := max(3, panelHeight-2)
	usedLines := m.detailSummaryLineCount(maxHeight)
	return maxHeight - usedLines - 1
}

func (m FlowMatrixModel) detailMaxEntries(maxHeight int) int {
	if len(m.drilldownIssues) == 0 {
		return 4
	}
	return min(4, max(1, (maxHeight-9)/2))
}

func (m FlowMatrixModel) detailSummaryLineCount(maxHeight int) int {
	if m.cursor < 0 || m.cursor >= len(m.labelStats) {
		return 0
	}
	stat := m.labelStats[m.cursor]
	lines := 5 // header, separator, IMPACT SUMMARY, Blocking Power, BLOCKS/BLOCKED BY header
	if stat.IsBottleneck {
		lines++
	}
	maxEntries := m.detailMaxEntries(maxHeight)
	pairRows := min(maxEntries, max(len(stat.OutgoingLabels), len(stat.IncomingLabels)))
	lines += pairRows
	if len(stat.OutgoingLabels) > maxEntries || len(stat.IncomingLabels) > maxEntries {
		lines++
	}
	if cascade := m.cascade.GetCascadeForLabel(stat.Label); cascade != nil && (cascade.BlockedCount > 0 || cascade.TotalImpact > 0) {
		cascadeLines := 2
		if len(cascade.Recommendations) > 0 {
			cascadeLines++
		}
		if len(m.drilldownIssues) == 0 || maxHeight-lines-cascadeLines >= 3 {
			lines += cascadeLines
		}
	}
	return lines
}

func (m FlowMatrixModel) computeDrilldownWindow(rawAvail int) (start, availIssues int) {
	if len(m.drilldownIssues) == 0 {
		return 0, max(2, (rawAvail/2)*2)
	}
	if rawAvail >= 2 {
		availIssues = (rawAvail / 2) * 2
		pairStart := (m.drilldownCursor / 2) * 2
		start = (m.drilldownScroll / 2) * 2
		if m.drilldownCursor < start {
			start = pairStart
		}
		if m.drilldownCursor >= start+availIssues {
			start = pairStart - availIssues + 2
		}
		maxScroll := max(0, ((len(m.drilldownIssues)-availIssues+1)/2)*2)
		if start > maxScroll {
			start = maxScroll
		}
		if start < 0 {
			start = 0
		}
		return start, availIssues
	}
	availIssues = 1
	start = max(0, min(m.drilldownCursor, len(m.drilldownIssues)-1))
	return start, availIssues
}

func (m *FlowMatrixModel) drilldownVisibleRows() int {
	// Keep both endpoints on screen together, including while scrolling.
	return max(2, (m.height-8)/2*2)
}

func (m *FlowMatrixModel) visibleRows() int {
	rows := m.height - 8 // header, footer, panel borders
	if rows < 3 {
		rows = 3
	}
	return rows
}

func (m *FlowMatrixModel) populateDrilldownForSelectedLabel() {
	if m.cursor < 0 || m.cursor >= len(m.labelStats) || m.flow == nil {
		m.drilldownIssues = nil
		m.drilldownCursor = 0
		m.drilldownScroll = 0
		m.drilldownTitle = ""
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
}

func (m *FlowMatrixModel) openDrilldown() {
	if m.cursor < 0 || m.cursor >= len(m.labelStats) || m.flow == nil {
		return
	}
	m.populateDrilldownForSelectedLabel()
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

	// Calculate panel box widths (including 2 border columns per panel)
	totalWidth := m.width
	if totalWidth < 20 {
		totalWidth = 20
	}
	leftBoxWidth := totalWidth * 36 / 100
	if leftBoxWidth < 22 {
		leftBoxWidth = 22
	}
	rightBoxWidth := totalWidth - leftBoxWidth
	if rightBoxWidth < 26 {
		rightBoxWidth = 26
	}
	if leftBoxWidth+rightBoxWidth > totalWidth {
		leftBoxWidth = max(8, totalWidth*38/100)
		rightBoxWidth = max(8, totalWidth-leftBoxWidth)
	}

	leftInnerWidth := max(4, leftBoxWidth-2)
	rightInnerWidth := max(4, rightBoxWidth-2)

	panelHeight := max(5, m.height-4)
	panelInnerHeight := max(3, panelHeight-2)

	leftContent := m.renderLabelsPanel(leftInnerWidth)
	rightContent := m.renderDetailPanel(rightInnerWidth, panelInnerHeight)

	leftBorder := lipgloss.RoundedBorder()
	leftBorderColor := m.theme.Border
	if m.focusPanel == 0 {
		leftBorder = lipgloss.ThickBorder()
		leftBorderColor = m.theme.Primary
	}
	rightBorder := lipgloss.RoundedBorder()
	rightBorderColor := m.theme.Border
	if m.focusPanel == 1 {
		rightBorder = lipgloss.ThickBorder()
		rightBorderColor = m.theme.Primary
	}

	leftBox := m.theme.Renderer.NewStyle().
		Border(leftBorder).
		BorderForeground(leftBorderColor).
		Width(leftInnerWidth).
		Height(panelInnerHeight).
		MaxHeight(panelHeight).
		Render(clampLinesToHeight(leftContent, panelInnerHeight, leftInnerWidth))

	rightBox := m.theme.Renderer.NewStyle().
		Border(rightBorder).
		BorderForeground(rightBorderColor).
		Width(rightInnerWidth).
		Height(panelInnerHeight).
		MaxHeight(panelHeight).
		Render(clampLinesToHeight(rightContent, panelInnerHeight, rightInnerWidth))

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
	header := m.renderHeader()
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func clampLinesToHeight(s string, maxRows, maxWidth int) string {
	if maxRows <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	if maxWidth > 0 {
		for i, ln := range lines {
			if lipgloss.Width(ln) > maxWidth {
				lines[i] = lipgloss.NewStyle().MaxWidth(maxWidth).Render(ln)
			}
		}
	}
	return strings.Join(lines, "\n")
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
	if m.width > 0 && lipgloss.Width(headerLine) > m.width {
		headerLine = lipgloss.NewStyle().MaxWidth(m.width).Render(headerLine)
	}

	borderStyle := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Border)

	return lipgloss.JoinVertical(lipgloss.Left,
		headerLine,
		borderStyle.Render(strings.Repeat("─", max(0, m.width))))
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

func (m FlowMatrixModel) renderDetailPanel(width, maxHeight int) string {
	var b strings.Builder

	if m.cursor >= len(m.labelStats) {
		return "Select a label"
	}

	stat := m.labelStats[m.cursor]

	// Panel header
	headerColor := m.theme.Secondary
	focusIndicator := "  "
	if m.focusPanel == 1 {
		headerColor = m.theme.Primary
		focusIndicator = "▸ "
	}
	headerStyle := m.theme.Renderer.NewStyle().
		Bold(true).
		Foreground(headerColor)

	b.WriteString(headerStyle.Render(fmt.Sprintf("%s%s", focusIndicator, stat.Label)))
	b.WriteString("\n")

	// Separator
	sepStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Border)
	b.WriteString(sepStyle.Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	// Stats summary
	summaryStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext)
	b.WriteString(summaryStyle.Render("IMPACT SUMMARY"))
	b.WriteString("\n")

	// Blocking power indicator
	scoreBar := m.renderScoreBar(stat.BottleneckScore, min(20, max(5, width-24)))
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

	// Two-column layout for blocks/blocked by
	halfWidth := max(10, (width-4)/2)

	blocksHeader := m.theme.Renderer.NewStyle().
		Foreground(m.theme.Blocked).
		Bold(true).
		Render(fmt.Sprintf("BLOCKS → (%d)", stat.OutgoingCount))

	blockedByHeader := m.theme.Renderer.NewStyle().
		Foreground(m.theme.InProgress).
		Bold(true).
		Render(fmt.Sprintf("← BLOCKED BY (%d)", stat.IncomingCount))

	padFlowCol := func(s string, colW int) string {
		visW := lipgloss.Width(s)
		if visW < colW {
			return s + strings.Repeat(" ", colW-visW)
		}
		return s
	}

	b.WriteString(padFlowCol(blocksHeader, halfWidth) + "  " + blockedByHeader + "\n")

	maxEntries := m.detailMaxEntries(maxHeight)
	outLabels := stat.OutgoingLabels
	inLabels := stat.IncomingLabels

	outCounts := m.getFlowCounts(stat.Label, outLabels, true)
	inCounts := m.getFlowCounts(stat.Label, inLabels, false)

	for i := 0; i < maxEntries; i++ {
		if i >= len(outLabels) && i >= len(inLabels) {
			break
		}
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

		b.WriteString(padFlowCol(leftStr, halfWidth) + "  " + rightStr + "\n")
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
		b.WriteString(padFlowCol(moreStyle.Render(leftMore), halfWidth) + "  " + moreStyle.Render(rightMore) + "\n")
	}

	if cascade := m.cascade.GetCascadeForLabel(stat.Label); cascade != nil && (cascade.BlockedCount > 0 || cascade.TotalImpact > 0) {
		cascadeLines := 2
		if len(cascade.Recommendations) > 0 {
			cascadeLines++
		}
		usedBeforeCascade := strings.Count(b.String(), "\n")
		if len(m.drilldownIssues) == 0 || maxHeight-usedBeforeCascade-cascadeLines >= 3 {
			b.WriteString(summaryStyle.Render("TRANSITIVE CASCADE"))
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("  Blocked in %s: %d  │  Downstream waiting: %d (%d levels)\n",
				stat.Label, cascade.BlockedCount, cascade.TotalImpact, len(cascade.CascadeLevels)))
			if len(cascade.Recommendations) > 0 {
				top := cascade.Recommendations[0]
				b.WriteString(fmt.Sprintf("  Top unblock: %s (unblocks %d)\n", top.IssueID, top.UnblocksCount))
			}
		}
	}

	// Render scrollable drilldown issue list inside right pane when issues exist
	if len(m.drilldownIssues) > 0 {
		usedLines := strings.Count(b.String(), "\n")
		start, availIssues := m.computeDrilldownWindow(maxHeight - usedLines - 1)
		end := min(len(m.drilldownIssues), start+availIssues)

		b.WriteString(summaryStyle.Render(fmt.Sprintf("RELATED ISSUES (%d/%d)", m.drilldownCursor+1, len(m.drilldownIssues))))
		b.WriteString("\n")
		for i := start; i < end; i++ {
			iss := m.drilldownIssues[i]
			prefix := "  "
			if i%2 == 1 {
				prefix = "    ↳ blocks "
			}
			lineText := truncateRunesHelper(fmt.Sprintf("%s%s %s", prefix, iss.ID, iss.Title), max(1, width-2), "…")
			if m.focusPanel == 1 && i == m.drilldownCursor {
				selStyle := m.theme.Renderer.NewStyle().
					Background(m.theme.Highlight).
					Foreground(m.theme.Primary).
					Bold(true).
					Width(width)
				b.WriteString(selStyle.Render("▸ " + truncateRunesHelper(strings.TrimPrefix(lineText, "  "), max(1, width-2), "…")))
			} else {
				b.WriteString(lineText)
			}
			b.WriteString("\n")
		}
	} else {
		hintStyle := m.theme.Renderer.NewStyle().Foreground(m.theme.Subtext).Italic(true)
		b.WriteString(hintStyle.Render("Press Enter to see issues"))
	}

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
	if m.width > 0 {
		help = truncateRunesHelper(help, m.width, "…")
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		borderStyle.Render(strings.Repeat("─", max(0, m.width))),
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
	start, visible := m.computeDrilldownWindow(m.drilldownVisibleRows())
	end := min(len(m.drilldownIssues), start+visible)

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
	if m.showDrilldown || m.focusPanel == 1 {
		m.moveDrilldownCursor(-1)
	} else {
		m.moveCursor(-1)
	}
}

// MoveDown moves the cursor down by one
func (m *FlowMatrixModel) MoveDown() {
	if m.showDrilldown || m.focusPanel == 1 {
		m.moveDrilldownCursor(1)
	} else {
		m.moveCursor(1)
	}
}

// TogglePanel switches focus between the labels list and detail panel
func (m *FlowMatrixModel) TogglePanel() {
	m.focusPanel = (m.focusPanel + 1) % 2
	if m.focusPanel == 1 && len(m.drilldownIssues) == 0 {
		m.populateDrilldownForSelectedLabel()
	}
}

// OpenDrilldown opens the drill-down view for the selected label
func (m *FlowMatrixModel) OpenDrilldown() {
	m.openDrilldown()
}

// GoToStart moves cursor to the first item
func (m *FlowMatrixModel) GoToStart() {
	if m.showDrilldown || m.focusPanel == 1 {
		m.drilldownCursor = 0
		m.drilldownScroll = 0
	} else {
		m.cursor = 0
		m.scrollOffset = 0
		m.populateDrilldownForSelectedLabel()
	}
}

// GoToEnd moves cursor to the last item
func (m *FlowMatrixModel) GoToEnd() {
	if m.showDrilldown || m.focusPanel == 1 {
		if len(m.drilldownIssues) > 0 {
			m.drilldownCursor = len(m.drilldownIssues) - 1
			m.ensureDrilldownVisible()
		}
	} else {
		if len(m.labelStats) > 0 {
			m.cursor = len(m.labelStats) - 1
			m.ensureVisible()
			m.populateDrilldownForSelectedLabel()
		}
	}
}

// SelectedDrilldownIssue returns the currently selected issue in drilldown or right-panel focus mode
func (m *FlowMatrixModel) SelectedDrilldownIssue() *model.Issue {
	if (!m.showDrilldown && m.focusPanel != 1) || m.drilldownCursor < 0 || m.drilldownCursor >= len(m.drilldownIssues) {
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
