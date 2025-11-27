package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/lipgloss"
)

// MetricPanel represents each panel type in the insights view
type MetricPanel int

const (
	PanelBottlenecks MetricPanel = iota
	PanelKeystones
	PanelInfluencers
	PanelHubs
	PanelAuthorities
	PanelCycles
	PanelCount // Sentinel for wrapping
)

// MetricInfo contains explanation for each metric
type MetricInfo struct {
	Icon        string
	Title       string
	ShortDesc   string
	WhatIs      string
	WhyUseful   string
	HowToUse    string
	FormulaHint string
}

var metricDescriptions = map[MetricPanel]MetricInfo{
	PanelBottlenecks: {
		Icon:        "🚧",
		Title:       "Bottlenecks",
		ShortDesc:   "Betweenness Centrality",
		WhatIs:      "Measures how often a bead lies on shortest paths between other beads.",
		WhyUseful:   "High-scoring beads are critical junctions. Delays here ripple across the project.",
		HowToUse:    "Prioritize these to unblock parallel workstreams. Consider breaking them into smaller pieces.",
		FormulaHint: "BW(v) = Σ (σst(v) / σst) for all s≠v≠t",
	},
	PanelKeystones: {
		Icon:        "🏛️",
		Title:       "Keystones",
		ShortDesc:   "Impact Depth",
		WhatIs:      "Measures how deep in the dependency chain a bead sits (downstream chain length).",
		WhyUseful:   "Keystones are foundational. Everything above them depends on their completion.",
		HowToUse:    "Complete these first. Blocking a keystone blocks the entire chain above it.",
		FormulaHint: "Impact(v) = 1 + max(Impact(u)) for all u that depend on v",
	},
	PanelInfluencers: {
		Icon:        "🌐",
		Title:       "Influencers",
		ShortDesc:   "Eigenvector Centrality",
		WhatIs:      "Scores beads by their connections to other well-connected beads.",
		WhyUseful:   "Influencers are connected to important beads. Changes here have wide-reaching effects.",
		HowToUse:    "Review these carefully before changes. They're central to the project structure.",
		FormulaHint: "EV(v) = (1/λ) × Σ A[v,u] × EV(u)",
	},
	PanelHubs: {
		Icon:        "🛰️",
		Title:       "Hubs",
		ShortDesc:   "HITS Hub Score",
		WhatIs:      "Beads that depend on many important authorities (aggregators).",
		WhyUseful:   "Hubs collect dependencies. They often represent high-level features or epics.",
		HowToUse:    "Track these for project milestones. Their completion signals major progress.",
		FormulaHint: "Hub(v) = Σ Authority(u) for all u where v→u",
	},
	PanelAuthorities: {
		Icon:        "📚",
		Title:       "Authorities",
		ShortDesc:   "HITS Authority Score",
		WhatIs:      "Beads that are depended upon by many important hubs (providers).",
		WhyUseful:   "Authorities are foundational services/components that many features need.",
		HowToUse:    "Stabilize these early. Breaking an authority breaks many dependent hubs.",
		FormulaHint: "Auth(v) = Σ Hub(u) for all u where u→v",
	},
	PanelCycles: {
		Icon:        "🔄",
		Title:       "Cycles",
		ShortDesc:   "Circular Dependencies",
		WhatIs:      "Groups of beads that form dependency loops (A→B→C→A).",
		WhyUseful:   "Cycles indicate structural problems. They can't be resolved in sequence.",
		HowToUse:    "Break cycles by removing or reversing a dependency. Refactor to decouple.",
		FormulaHint: "Detected via Tarjan's SCC algorithm",
	},
}

// InsightsModel is an interactive insights dashboard
type InsightsModel struct {
	insights analysis.Insights
	issueMap map[string]*model.Issue
	theme    Theme

	// Navigation state
	focusedPanel  MetricPanel
	selectedIndex [PanelCount]int // Selection per panel
	scrollOffset  [PanelCount]int // Scroll offset per panel

	// View options
	showExplanations bool
	showCalculation  bool
	showDetailPanel  bool

	// Dimensions
	width  int
	height int
	ready  bool
}

// NewInsightsModel creates a new interactive insights model
func NewInsightsModel(ins analysis.Insights, issueMap map[string]*model.Issue, theme Theme) InsightsModel {
	return InsightsModel{
		insights:         ins,
		issueMap:         issueMap,
		theme:            theme,
		showExplanations: true, // Visible by default
		showCalculation:  true, // Always show calculation details
		showDetailPanel:  true,
	}
}

func (m *InsightsModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.ready = true
}

func (m *InsightsModel) SetInsights(ins analysis.Insights) {
	m.insights = ins
}

// Navigation methods
func (m *InsightsModel) MoveUp() {
	count := m.currentPanelItemCount()
	if count == 0 {
		return
	}
	if m.selectedIndex[m.focusedPanel] > 0 {
		m.selectedIndex[m.focusedPanel]--
	}
}

func (m *InsightsModel) MoveDown() {
	count := m.currentPanelItemCount()
	if count == 0 {
		return
	}
	if m.selectedIndex[m.focusedPanel] < count-1 {
		m.selectedIndex[m.focusedPanel]++
	}
}

func (m *InsightsModel) NextPanel() {
	m.focusedPanel = (m.focusedPanel + 1) % PanelCount
}

func (m *InsightsModel) PrevPanel() {
	if m.focusedPanel == 0 {
		m.focusedPanel = PanelCount - 1
	} else {
		m.focusedPanel--
	}
}

func (m *InsightsModel) ToggleExplanations() {
	m.showExplanations = !m.showExplanations
}

func (m *InsightsModel) ToggleCalculation() {
	m.showCalculation = !m.showCalculation
}

// currentPanelItemCount returns the number of items in the focused panel (including cycles)
func (m *InsightsModel) currentPanelItemCount() int {
	switch m.focusedPanel {
	case PanelBottlenecks:
		return len(m.insights.Bottlenecks)
	case PanelKeystones:
		return len(m.insights.Keystones)
	case PanelInfluencers:
		return len(m.insights.Influencers)
	case PanelHubs:
		return len(m.insights.Hubs)
	case PanelAuthorities:
		return len(m.insights.Authorities)
	case PanelCycles:
		return len(m.insights.Cycles)
	default:
		return 0
	}
}

// getPanelItems returns the InsightItems for a given panel (nil for cycles)
func (m *InsightsModel) getPanelItems(panel MetricPanel) []analysis.InsightItem {
	switch panel {
	case PanelBottlenecks:
		return m.insights.Bottlenecks
	case PanelKeystones:
		return m.insights.Keystones
	case PanelInfluencers:
		return m.insights.Influencers
	case PanelHubs:
		return m.insights.Hubs
	case PanelAuthorities:
		return m.insights.Authorities
	default:
		return nil
	}
}

// SelectedIssueID returns the currently selected issue ID
func (m *InsightsModel) SelectedIssueID() string {
	// For cycles panel, return first item in selected cycle
	if m.focusedPanel == PanelCycles {
		idx := m.selectedIndex[PanelCycles]
		if idx >= 0 && idx < len(m.insights.Cycles) && len(m.insights.Cycles[idx]) > 0 {
			return m.insights.Cycles[idx][0]
		}
		return ""
	}

	// For other panels, return selected item's ID
	items := m.getPanelItems(m.focusedPanel)
	idx := m.selectedIndex[m.focusedPanel]
	if idx >= 0 && idx < len(items) {
		return items[idx].ID
	}
	return ""
}

// View renders the insights dashboard (pointer receiver to persist scroll state)
func (m *InsightsModel) View() string {
	if !m.ready {
		return ""
	}

	t := m.theme

	// Calculate layout dimensions
	mainWidth := m.width
	detailWidth := 0
	if m.showDetailPanel && m.width > 120 {
		detailWidth = min(50, m.width/3)
		mainWidth = m.width - detailWidth - 1
	}

	// 3-column layout for panels
	colWidth := (mainWidth - 6) / 3
	if colWidth < 25 {
		colWidth = 25
	}

	rowHeight := (m.height - 4) / 2
	if rowHeight < 8 {
		rowHeight = 8
	}

	// Render each panel
	panels := make([]string, 6)
	panels[0] = m.renderMetricPanel(PanelBottlenecks, colWidth, rowHeight, t)
	panels[1] = m.renderMetricPanel(PanelKeystones, colWidth, rowHeight, t)
	panels[2] = m.renderMetricPanel(PanelInfluencers, colWidth, rowHeight, t)
	panels[3] = m.renderMetricPanel(PanelHubs, colWidth, rowHeight, t)
	panels[4] = m.renderMetricPanel(PanelAuthorities, colWidth, rowHeight, t)
	panels[5] = m.renderCyclesPanel(colWidth, rowHeight, t)

	// Arrange in 2 rows of 3
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, panels[0], panels[1], panels[2])
	btmRow := lipgloss.JoinHorizontal(lipgloss.Top, panels[3], panels[4], panels[5])

	mainContent := lipgloss.JoinVertical(lipgloss.Left, topRow, btmRow)

	// Add detail panel if enabled
	if detailWidth > 0 {
		detailPanel := m.renderDetailPanel(detailWidth, m.height-2, t)
		return lipgloss.JoinHorizontal(lipgloss.Top, mainContent, detailPanel)
	}

	return mainContent
}

func (m *InsightsModel) renderMetricPanel(panel MetricPanel, width, height int, t Theme) string {
	info := metricDescriptions[panel]
	items := m.getPanelItems(panel)
	isFocused := m.focusedPanel == panel
	selectedIdx := m.selectedIndex[panel]

	// Panel border style
	borderColor := t.Secondary
	if isFocused {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width).
		Height(height).
		Padding(0, 1)

	// Title with count and value range
	titleStyle := t.Renderer.NewStyle().Bold(true)
	if isFocused {
		titleStyle = titleStyle.Foreground(t.Primary)
	} else {
		titleStyle = titleStyle.Foreground(t.Secondary)
	}

	var sb strings.Builder

	// Header line: Icon Title (count)
	headerLine := fmt.Sprintf("%s %s (%d)", info.Icon, info.Title, len(items))
	sb.WriteString(titleStyle.Render(headerLine))
	sb.WriteString("\n")

	// Subtitle: metric name
	subtitleStyle := t.Renderer.NewStyle().Foreground(t.Subtext).Italic(true)
	sb.WriteString(subtitleStyle.Render(info.ShortDesc))
	sb.WriteString("\n")

	// Explanation (if enabled)
	if m.showExplanations {
		explainStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Width(width - 4)
		sb.WriteString(explainStyle.Render(info.WhatIs))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	// Items list
	// Calculate visible rows more conservatively
	// Header(1) + Subtitle(1) + Explain(2-3 lines typically) + Spacer(1) + Scroll(1) = ~7 lines overhead
	visibleRows := height - 7
	if m.showExplanations {
		// Explanations can wrap, so give more buffer
		visibleRows -= 1
	}
	if visibleRows < 3 {
		visibleRows = 3
	}

	// Scrolling
	startIdx := m.scrollOffset[panel]
	if selectedIdx >= startIdx+visibleRows {
		startIdx = selectedIdx - visibleRows + 1
	}
	if selectedIdx < startIdx {
		startIdx = selectedIdx
	}
	m.scrollOffset[panel] = startIdx

	endIdx := startIdx + visibleRows
	if endIdx > len(items) {
		endIdx = len(items)
	}

	for i := startIdx; i < endIdx; i++ {
		item := items[i]
		isSelected := isFocused && i == selectedIdx

		row := m.renderInsightRow(item.ID, item.Value, width-4, isSelected, t)
		sb.WriteString(row)
		sb.WriteString("\n")
	}

	// Scroll indicator
	if len(items) > visibleRows {
		scrollInfo := fmt.Sprintf("↕ %d/%d", selectedIdx+1, len(items))
		scrollStyle := t.Renderer.NewStyle().
			Foreground(t.Subtext).
			Align(lipgloss.Center).
			Width(width - 4)
		sb.WriteString(scrollStyle.Render(scrollInfo))
	}

	return panelStyle.Render(sb.String())
}

func (m *InsightsModel) renderInsightRow(id string, value float64, width int, isSelected bool, t Theme) string {
	issue := m.issueMap[id]

	// Format value
	var valueStr string
	if value >= 1.0 {
		valueStr = fmt.Sprintf("%.1f", value)
	} else if value >= 0.01 {
		valueStr = fmt.Sprintf("%.3f", value)
	} else {
		valueStr = fmt.Sprintf("%.2e", value)
	}

	// Build row content
	var rowBuilder strings.Builder

	// Selection indicator
	if isSelected {
		rowBuilder.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("▸ "))
	} else {
		rowBuilder.WriteString("  ")
	}

	// Value badge
	valueStyle := t.Renderer.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "#E8E8E8", Dark: "#3D3D3D"}).
		Foreground(t.Primary).
		Bold(true).
		Padding(0, 1)
	rowBuilder.WriteString(valueStyle.Render(valueStr))
	rowBuilder.WriteString(" ")

	// Issue content
	if issue != nil {
		// Type icon
		icon, iconColor := t.GetTypeIcon(string(issue.IssueType))
		rowBuilder.WriteString(t.Renderer.NewStyle().Foreground(iconColor).Render(icon))
		rowBuilder.WriteString(" ")

		// Status indicator
		statusColor := t.GetStatusColor(string(issue.Status))
		statusDot := t.Renderer.NewStyle().Foreground(statusColor).Render("●")
		rowBuilder.WriteString(statusDot)
		rowBuilder.WriteString(" ")

		// Title (truncated) - leave room for description preview
		usedWidth := 18 + len(valueStr)
		remainingWidth := width - usedWidth
		titleWidth := remainingWidth * 2 / 3         // Title gets 2/3 of remaining
		descWidth := remainingWidth - titleWidth - 3 // -3 for " - "

		if titleWidth < 10 {
			titleWidth = 10
		}
		if descWidth < 5 {
			descWidth = 0 // Don't show description if not enough space
		}

		title := truncateRunesHelper(issue.Title, titleWidth, "…")

		titleStyle := t.Renderer.NewStyle()
		if isSelected {
			titleStyle = titleStyle.Foreground(t.Primary).Bold(true)
		}
		rowBuilder.WriteString(titleStyle.Render(title))

		// Description preview (if space allows)
		if descWidth > 0 && issue.Description != "" {
			// Clean up description - remove newlines, trim whitespace
			desc := strings.Join(strings.Fields(issue.Description), " ")
			desc = truncateRunesHelper(desc, descWidth, "…")
			descStyle := t.Renderer.NewStyle().Foreground(t.Subtext).Italic(true)
			rowBuilder.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Render(" - "))
			rowBuilder.WriteString(descStyle.Render(desc))
		}
	} else {
		// Fallback: just show ID
		idTrunc := truncateRunesHelper(id, width-12-len(valueStr), "…")
		idStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
		if isSelected {
			idStyle = idStyle.Foreground(t.Primary).Bold(true)
		}
		rowBuilder.WriteString(idStyle.Render(idTrunc))
	}

	return rowBuilder.String()
}

func (m *InsightsModel) renderCyclesPanel(width, height int, t Theme) string {
	info := metricDescriptions[PanelCycles]
	isFocused := m.focusedPanel == PanelCycles
	cycles := m.insights.Cycles

	borderColor := t.Secondary
	if isFocused {
		borderColor = t.Primary
	}

	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(width).
		Height(height).
		Padding(0, 1)

	titleStyle := t.Renderer.NewStyle().Bold(true)
	if isFocused {
		titleStyle = titleStyle.Foreground(t.Primary)
	} else {
		titleStyle = titleStyle.Foreground(t.Secondary)
	}

	var sb strings.Builder

	// Header
	headerLine := fmt.Sprintf("%s %s (%d)", info.Icon, info.Title, len(cycles))
	sb.WriteString(titleStyle.Render(headerLine))
	sb.WriteString("\n")

	subtitleStyle := t.Renderer.NewStyle().Foreground(t.Subtext).Italic(true)
	sb.WriteString(subtitleStyle.Render(info.ShortDesc))
	sb.WriteString("\n")

	if m.showExplanations {
		explainStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Width(width - 4)
		sb.WriteString(explainStyle.Render(info.WhatIs))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	if len(cycles) == 0 {
		healthyStyle := t.Renderer.NewStyle().
			Foreground(t.Open).
			Bold(true)
		sb.WriteString(healthyStyle.Render("✓ No cycles detected"))
		sb.WriteString("\n")
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Subtext).Render("Graph is acyclic (DAG)"))
	} else {
		selectedIdx := m.selectedIndex[PanelCycles]
		visibleRows := height - 6
		if m.showExplanations {
			visibleRows -= 2
		}
		if visibleRows < 3 {
			visibleRows = 3
		}

		// Scrolling support for cycles (same logic as metric panels)
		startIdx := m.scrollOffset[PanelCycles]
		if selectedIdx >= startIdx+visibleRows {
			startIdx = selectedIdx - visibleRows + 1
		}
		if selectedIdx < startIdx {
			startIdx = selectedIdx
		}
		m.scrollOffset[PanelCycles] = startIdx

		endIdx := startIdx + visibleRows
		if endIdx > len(cycles) {
			endIdx = len(cycles)
		}

		for i := startIdx; i < endIdx; i++ {
			cycle := cycles[i]
			isSelected := isFocused && i == selectedIdx
			prefix := "  "
			if isSelected {
				prefix = t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("▸ ")
			}

			// Render cycle as chain
			cycleStr := m.renderCycleChain(cycle, width-6, t)

			warningStyle := t.Renderer.NewStyle().Foreground(t.Blocked)
			if isSelected {
				warningStyle = warningStyle.Bold(true)
			}

			sb.WriteString(prefix)
			sb.WriteString(warningStyle.Render(cycleStr))
			sb.WriteString("\n")
		}

		// Scroll indicator
		if len(cycles) > visibleRows {
			scrollInfo := fmt.Sprintf("↕ %d/%d", selectedIdx+1, len(cycles))
			scrollStyle := t.Renderer.NewStyle().
				Foreground(t.Subtext).
				Align(lipgloss.Center).
				Width(width - 4)
			sb.WriteString(scrollStyle.Render(scrollInfo))
		}
	}

	return panelStyle.Render(sb.String())
}

func (m *InsightsModel) renderCycleChain(cycle []string, maxWidth int, t Theme) string {
	if len(cycle) == 0 {
		return ""
	}

	// Build chain: A → B → C → A
	var parts []string
	for _, id := range cycle {
		// Try to get short title (check both key existence and nil value)
		if issue, ok := m.issueMap[id]; ok && issue != nil {
			shortTitle := truncateRunesHelper(issue.Title, 15, "…")
			parts = append(parts, shortTitle)
		} else {
			parts = append(parts, truncateRunesHelper(id, 12, "…"))
		}
	}
	// Close the cycle
	if len(parts) > 0 {
		parts = append(parts, parts[0])
	}

	chain := strings.Join(parts, " → ")
	if len([]rune(chain)) > maxWidth {
		chain = truncateRunesHelper(chain, maxWidth, "…")
	}
	return chain
}

func (m *InsightsModel) renderDetailPanel(width, height int, t Theme) string {
	panelStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Width(width).
		Height(height).
		Padding(0, 1)

	selectedID := m.SelectedIssueID()
	if selectedID == "" {
		emptyStyle := t.Renderer.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Width(width - 4).
			Align(lipgloss.Center)
		return panelStyle.Render(emptyStyle.Render("\nSelect a bead to view details"))
	}

	issue := m.issueMap[selectedID]
	if issue == nil {
		return panelStyle.Render(t.Renderer.NewStyle().Foreground(t.Subtext).Render("Issue not found: " + selectedID))
	}

	contentWidth := width - 6
	var sb strings.Builder

	// === HEADER: Type, Status, Priority ===
	icon, iconColor := t.GetTypeIcon(string(issue.IssueType))
	statusColor := t.GetStatusColor(string(issue.Status))

	sb.WriteString(t.Renderer.NewStyle().Foreground(iconColor).Render(icon))
	sb.WriteString(" ")
	sb.WriteString(t.Renderer.NewStyle().Bold(true).Render(string(issue.IssueType)))
	sb.WriteString("  ")
	sb.WriteString(t.Renderer.NewStyle().Foreground(statusColor).Bold(true).Render(strings.ToUpper(string(issue.Status))))
	sb.WriteString("  ")
	sb.WriteString(GetPriorityIcon(issue.Priority))
	sb.WriteString(fmt.Sprintf(" P%d", issue.Priority))
	sb.WriteString("\n")

	// === ID (short) ===
	idStyle := t.Renderer.NewStyle().Foreground(t.Subtext)
	sb.WriteString(idStyle.Render(truncateRunesHelper(issue.ID, contentWidth, "…")))
	sb.WriteString("\n\n")

	// === TITLE ===
	titleHeaderStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)
	sb.WriteString(titleHeaderStyle.Render("TITLE"))
	sb.WriteString("\n")
	wrappedTitle := wrapText(issue.Title, contentWidth)
	sb.WriteString(t.Renderer.NewStyle().Bold(true).Render(wrappedTitle))
	sb.WriteString("\n\n")

	// === DESCRIPTION (full) ===
	if issue.Description != "" {
		sb.WriteString(titleHeaderStyle.Render("DESCRIPTION"))
		sb.WriteString("\n")
		wrappedDesc := wrapText(issue.Description, contentWidth)
		sb.WriteString(wrappedDesc)
		sb.WriteString("\n\n")
	}

	// === DESIGN (full) ===
	if issue.Design != "" {
		sb.WriteString(titleHeaderStyle.Render("DESIGN"))
		sb.WriteString("\n")
		wrappedDesign := wrapText(issue.Design, contentWidth)
		sb.WriteString(wrappedDesign)
		sb.WriteString("\n\n")
	}

	// === ACCEPTANCE CRITERIA (full) ===
	if issue.AcceptanceCriteria != "" {
		sb.WriteString(titleHeaderStyle.Render("ACCEPTANCE CRITERIA"))
		sb.WriteString("\n")
		wrappedAC := wrapText(issue.AcceptanceCriteria, contentWidth)
		sb.WriteString(wrappedAC)
		sb.WriteString("\n\n")
	}

	// === NOTES (full) ===
	if issue.Notes != "" {
		sb.WriteString(titleHeaderStyle.Render("NOTES"))
		sb.WriteString("\n")
		wrappedNotes := wrapText(issue.Notes, contentWidth)
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Subtext).Italic(true).Render(wrappedNotes))
		sb.WriteString("\n\n")
	}

	// === ASSIGNEE ===
	if issue.Assignee != "" {
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Render("Assignee: "))
		sb.WriteString("@" + issue.Assignee)
		sb.WriteString("\n\n")
	}

	// === DEPENDENCIES ===
	if len(issue.Dependencies) > 0 {
		sb.WriteString(titleHeaderStyle.Render(fmt.Sprintf("DEPENDENCIES (%d)", len(issue.Dependencies))))
		sb.WriteString("\n")
		for _, dep := range issue.Dependencies {
			depIssue := m.issueMap[dep.DependsOnID]
			depTypeStr := string(dep.Type)
			// Calculate prefix width: "  • " (4) + type + ": " (2)
			prefixWidth := 6 + len([]rune(depTypeStr))
			titleWidth := contentWidth - prefixWidth
			if titleWidth < 10 {
				titleWidth = 10
			}
			if depIssue != nil {
				depTitle := truncateRunesHelper(depIssue.Title, titleWidth, "…")
				sb.WriteString(fmt.Sprintf("  • %s: %s\n", depTypeStr, depTitle))
			} else {
				sb.WriteString(fmt.Sprintf("  • %s: %s\n", depTypeStr, truncateRunesHelper(dep.DependsOnID, titleWidth, "…")))
			}
		}
		sb.WriteString("\n")
	}

	// === METRIC VALUES ===
	if m.insights.Stats != nil {
		dividerStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)
		sb.WriteString(dividerStyle.Render("─── METRICS ───"))
		sb.WriteString("\n")

		stats := m.insights.Stats
		metricStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
		valueStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)

		metrics := []struct {
			name  string
			value float64
		}{
			{"PageRank", stats.PageRank[selectedID]},
			{"Betweenness", stats.Betweenness[selectedID]},
			{"Eigenvector", stats.Eigenvector[selectedID]},
			{"Impact", stats.CriticalPathScore[selectedID]},
			{"Hub", stats.Hubs[selectedID]},
			{"Authority", stats.Authorities[selectedID]},
		}

		for _, metric := range metrics {
			sb.WriteString(metricStyle.Render(fmt.Sprintf("%-11s", metric.name+":")))
			sb.WriteString(valueStyle.Render(formatMetricValue(metric.value)))
			sb.WriteString(" ")
		}
		sb.WriteString("\n")

		// Degree info on one line
		sb.WriteString(metricStyle.Render("In: "))
		sb.WriteString(valueStyle.Render(fmt.Sprintf("%d", stats.InDegree[selectedID])))
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Subtext).Render(" ← "))
		sb.WriteString(metricStyle.Render("Out: "))
		sb.WriteString(valueStyle.Render(fmt.Sprintf("%d", stats.OutDegree[selectedID])))
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Subtext).Render(" →"))
		sb.WriteString("\n\n")
	}

	// === CALCULATION PROOF ===
	if m.showCalculation && m.insights.Stats != nil {
		sb.WriteString(m.renderCalculationProof(selectedID, contentWidth, t))
	}

	return panelStyle.Render(sb.String())
}

// formatMetricValue formats a metric value nicely
func formatMetricValue(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	} else if v >= 1.0 {
		return fmt.Sprintf("%.2f", v)
	} else if v >= 0.01 {
		return fmt.Sprintf("%.3f", v)
	} else if v > 0 {
		return fmt.Sprintf("%.2e", v)
	}
	return "0"
}

// renderCalculationProof shows the actual beads and numbers that contributed to the metric
func (m *InsightsModel) renderCalculationProof(selectedID string, width int, t Theme) string {
	var sb strings.Builder
	stats := m.insights.Stats
	info := metricDescriptions[m.focusedPanel]

	dividerStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)
	sb.WriteString(dividerStyle.Render("─── CALCULATION PROOF ───"))
	sb.WriteString("\n")

	// Formula hint
	formulaStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
	sb.WriteString(formulaStyle.Render(info.FormulaHint))
	sb.WriteString("\n\n")

	labelStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	valueStyle := t.Renderer.NewStyle().Foreground(t.Primary).Bold(true)
	itemStyle := t.Renderer.NewStyle() // Default text color
	subStyle := t.Renderer.NewStyle().Foreground(t.Subtext)

	switch m.focusedPanel {
	case PanelBottlenecks:
		// Betweenness: Show shortest path involvement
		bw := stats.Betweenness[selectedID]
		sb.WriteString(labelStyle.Render("Betweenness Score: "))
		sb.WriteString(valueStyle.Render(formatMetricValue(bw)))
		sb.WriteString("\n\n")

		// Find beads that depend on this one (upstream) and beads this depends on (downstream)
		upstream := m.findDependents(selectedID)
		downstream := m.findDependencies(selectedID)

		if len(upstream) > 0 {
			sb.WriteString(labelStyle.Render(fmt.Sprintf("Beads depending on this (%d):\n", len(upstream))))
			for i, id := range upstream {
				if i >= 5 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("  ... +%d more\n", len(upstream)-5)))
					break
				}
				title := m.getBeadTitle(id, width-4)
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  ↓ %s\n", title)))
			}
		}

		if len(downstream) > 0 {
			sb.WriteString(labelStyle.Render(fmt.Sprintf("This depends on (%d):\n", len(downstream))))
			for i, id := range downstream {
				if i >= 5 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("  ... +%d more\n", len(downstream)-5)))
					break
				}
				title := m.getBeadTitle(id, width-4)
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  ↑ %s\n", title)))
			}
		}

		sb.WriteString("\n")
		sb.WriteString(subStyle.Render(wrapText("This bead lies on many shortest paths between other beads, making it a critical junction in the dependency graph.", width)))

	case PanelKeystones:
		// Impact Depth: Show the dependency chain
		impact := stats.CriticalPathScore[selectedID]
		sb.WriteString(labelStyle.Render("Impact Depth: "))
		sb.WriteString(valueStyle.Render(formatMetricValue(impact)))
		sb.WriteString(labelStyle.Render(" levels deep"))
		sb.WriteString("\n\n")

		// Show the chain of dependents
		chain := m.buildImpactChain(selectedID, int(impact))
		if len(chain) > 0 {
			sb.WriteString(labelStyle.Render("Dependency chain:\n"))
			for i, id := range chain {
				indent := strings.Repeat("  ", i)
				// Account for indent (2*i chars) + "└─ " (3 chars)
				titleWidth := width - (2*i + 3)
				if titleWidth < 10 {
					titleWidth = 10
				}
				title := m.getBeadTitle(id, titleWidth)
				if i == 0 {
					sb.WriteString(valueStyle.Render(fmt.Sprintf("%s└─ %s\n", indent, title)))
				} else {
					sb.WriteString(itemStyle.Render(fmt.Sprintf("%s└─ %s\n", indent, title)))
				}
				if i >= 6 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("%s   ... chain continues\n", indent)))
					break
				}
			}
		}

	case PanelInfluencers:
		// Eigenvector: Show influential neighbors
		ev := stats.Eigenvector[selectedID]
		sb.WriteString(labelStyle.Render("Eigenvector Centrality: "))
		sb.WriteString(valueStyle.Render(formatMetricValue(ev)))
		sb.WriteString("\n\n")

		// Find neighbors and their eigenvector scores
		neighbors := m.findNeighborsWithScores(selectedID, stats.Eigenvector)
		if len(neighbors) > 0 {
			sb.WriteString(labelStyle.Render("Connected to influential beads:\n"))
			for i, n := range neighbors {
				if i >= 5 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("  ... +%d more connections\n", len(neighbors)-5)))
					break
				}
				title := m.getBeadTitle(n.id, width-15)
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  • %s ", title)))
				sb.WriteString(subStyle.Render(fmt.Sprintf("(EV: %s)\n", formatMetricValue(n.score))))
			}
		}

		sb.WriteString("\n")
		sb.WriteString(subStyle.Render(wrapText("Score reflects connections to other well-connected beads.", width)))

	case PanelHubs:
		// Hubs: Show authorities this hub depends on
		hubScore := stats.Hubs[selectedID]
		sb.WriteString(labelStyle.Render("Hub Score: "))
		sb.WriteString(valueStyle.Render(formatMetricValue(hubScore)))
		sb.WriteString("\n\n")

		// Find authorities (dependencies) with their authority scores
		deps := m.findDependenciesWithScores(selectedID, stats.Authorities)
		if len(deps) > 0 {
			sb.WriteString(labelStyle.Render("Depends on these authorities:\n"))
			// Calculate total sum over all items
			sumAuth := 0.0
			for _, d := range deps {
				sumAuth += d.score
			}
			// Display up to 5 items
			for i, d := range deps {
				if i >= 5 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("  ... +%d more\n", len(deps)-5)))
					break
				}
				title := m.getBeadTitle(d.id, width-15)
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  → %s ", title)))
				sb.WriteString(subStyle.Render(fmt.Sprintf("(Auth: %s)\n", formatMetricValue(d.score))))
			}
			sb.WriteString("\n")
			sb.WriteString(subStyle.Render(fmt.Sprintf("Sum of %d authority scores: %s", len(deps), formatMetricValue(sumAuth))))
		}

	case PanelAuthorities:
		// Authorities: Show hubs that depend on this authority
		authScore := stats.Authorities[selectedID]
		sb.WriteString(labelStyle.Render("Authority Score: "))
		sb.WriteString(valueStyle.Render(formatMetricValue(authScore)))
		sb.WriteString("\n\n")

		// Find hubs (dependents) with their hub scores
		dependents := m.findDependentsWithScores(selectedID, stats.Hubs)
		if len(dependents) > 0 {
			sb.WriteString(labelStyle.Render("Hubs that depend on this:\n"))
			// Calculate total sum over all items
			sumHub := 0.0
			for _, d := range dependents {
				sumHub += d.score
			}
			// Display up to 5 items
			for i, d := range dependents {
				if i >= 5 {
					sb.WriteString(subStyle.Render(fmt.Sprintf("  ... +%d more\n", len(dependents)-5)))
					break
				}
				title := m.getBeadTitle(d.id, width-15)
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  ← %s ", title)))
				sb.WriteString(subStyle.Render(fmt.Sprintf("(Hub: %s)\n", formatMetricValue(d.score))))
			}
			sb.WriteString("\n")
			sb.WriteString(subStyle.Render(fmt.Sprintf("Sum of %d hub scores: %s", len(dependents), formatMetricValue(sumHub))))
		}

	case PanelCycles:
		// Cycles: Show the cycle members
		idx := m.selectedIndex[PanelCycles]
		if idx >= 0 && idx < len(m.insights.Cycles) {
			cycle := m.insights.Cycles[idx]
			sb.WriteString(labelStyle.Render(fmt.Sprintf("Cycle with %d beads:\n", len(cycle))))
			for i, id := range cycle {
				title := m.getBeadTitle(id, width-6)
				arrow := "→"
				if i == len(cycle)-1 {
					arrow = "↺" // loops back
				}
				sb.WriteString(itemStyle.Render(fmt.Sprintf("  %s %s\n", arrow, title)))
			}
			sb.WriteString("\n")
			sb.WriteString(subStyle.Render(wrapText("These beads form a circular dependency. Break the cycle by removing or reversing one edge.", width)))
		}
	}

	sb.WriteString("\n")
	sb.WriteString(subStyle.Render(wrapText(info.HowToUse, width)))

	return sb.String()
}

// Helper type for scored items
type scoredItem struct {
	id    string
	score float64
}

// getBeadTitle returns a truncated title for a bead ID
func (m *InsightsModel) getBeadTitle(id string, maxWidth int) string {
	if issue, ok := m.issueMap[id]; ok && issue != nil {
		return truncateRunesHelper(issue.Title, maxWidth, "…")
	}
	return truncateRunesHelper(id, maxWidth, "…")
}

// findDependents returns IDs of beads that depend on the given bead (sorted for consistent order)
func (m *InsightsModel) findDependents(targetID string) []string {
	var dependents []string
	for id, issue := range m.issueMap {
		if issue == nil {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep.DependsOnID == targetID {
				dependents = append(dependents, id)
				break
			}
		}
	}
	// Sort for consistent display order (map iteration is non-deterministic)
	for i := 0; i < len(dependents)-1; i++ {
		for j := i + 1; j < len(dependents); j++ {
			if dependents[j] < dependents[i] {
				dependents[i], dependents[j] = dependents[j], dependents[i]
			}
		}
	}
	return dependents
}

// findDependencies returns IDs of beads that the given bead depends on
func (m *InsightsModel) findDependencies(targetID string) []string {
	issue := m.issueMap[targetID]
	if issue == nil {
		return nil
	}
	var deps []string
	for _, dep := range issue.Dependencies {
		deps = append(deps, dep.DependsOnID)
	}
	return deps
}

// findNeighborsWithScores returns neighbors with their metric scores, sorted by score
func (m *InsightsModel) findNeighborsWithScores(targetID string, scores map[string]float64) []scoredItem {
	var items []scoredItem
	seen := make(map[string]bool)

	// Add dependents
	for _, id := range m.findDependents(targetID) {
		if !seen[id] {
			seen[id] = true
			items = append(items, scoredItem{id: id, score: scores[id]})
		}
	}
	// Add dependencies (avoid duplicates from cycles)
	for _, id := range m.findDependencies(targetID) {
		if !seen[id] {
			seen[id] = true
			items = append(items, scoredItem{id: id, score: scores[id]})
		}
	}

	// Sort by score descending
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return items
}

// findDependenciesWithScores returns dependencies with their metric scores
func (m *InsightsModel) findDependenciesWithScores(targetID string, scores map[string]float64) []scoredItem {
	var items []scoredItem
	for _, id := range m.findDependencies(targetID) {
		items = append(items, scoredItem{id: id, score: scores[id]})
	}
	// Sort by score descending
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return items
}

// findDependentsWithScores returns dependents with their metric scores
func (m *InsightsModel) findDependentsWithScores(targetID string, scores map[string]float64) []scoredItem {
	var items []scoredItem
	for _, id := range m.findDependents(targetID) {
		items = append(items, scoredItem{id: id, score: scores[id]})
	}
	// Sort by score descending
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return items
}

// buildImpactChain builds the dependency chain from a bead to its deepest dependency
func (m *InsightsModel) buildImpactChain(startID string, maxDepth int) []string {
	var chain []string
	if maxDepth <= 0 || m.insights.Stats == nil {
		return chain
	}

	current := startID
	visited := make(map[string]bool)

	for len(chain) < maxDepth && !visited[current] {
		visited[current] = true
		chain = append(chain, current)

		// Find the dependency with highest impact score
		deps := m.findDependencies(current)
		if len(deps) == 0 {
			break
		}

		bestDep := ""
		bestScore := -1.0
		for _, dep := range deps {
			score := m.insights.Stats.CriticalPathScore[dep]
			if score > bestScore {
				bestScore = score
				bestDep = dep
			}
		}
		if bestDep == "" {
			break
		}
		current = bestDep
	}
	return chain
}

// wrapText wraps text to fit within maxWidth
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	var currentLine strings.Builder
	currentLen := 0

	for _, word := range words {
		wordLen := len([]rune(word))
		if currentLen+wordLen+1 > maxWidth && currentLen > 0 {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLen = 0
		}
		if currentLen > 0 {
			currentLine.WriteString(" ")
			currentLen++
		}
		currentLine.WriteString(word)
		currentLen += wordLen
	}
	if currentLen > 0 {
		lines = append(lines, currentLine.String())
	}

	return strings.Join(lines, "\n")
}
