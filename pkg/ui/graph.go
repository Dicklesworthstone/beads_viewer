package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/lipgloss"
)

// GraphModel represents the dependency graph view with visual ASCII art visualization
type GraphModel struct {
	issues       []model.Issue
	issueMap     map[string]*model.Issue
	insights     *analysis.Insights
	selectedIdx  int
	scrollOffset int
	width        int
	height       int
	theme        Theme

	// Precomputed graph relationships
	blockers   map[string][]string // What each issue depends on (blocks this issue)
	dependents map[string][]string // What depends on each issue (this issue blocks)

	// Flat list for navigation
	sortedIDs []string

	// Precomputed rankings for all metrics (id -> rank, 1-indexed)
	rankPageRank     map[string]int
	rankBetweenness  map[string]int
	rankEigenvector  map[string]int
	rankHubs         map[string]int
	rankAuthorities  map[string]int
	rankCriticalPath map[string]int
	rankInDegree     map[string]int
	rankOutDegree    map[string]int
}

// NewGraphModel creates a new graph view from issues
func NewGraphModel(issues []model.Issue, insights *analysis.Insights, theme Theme) GraphModel {
	g := GraphModel{
		issues:   issues,
		insights: insights,
		theme:    theme,
	}
	g.rebuildGraph()
	return g
}

// SetIssues updates the graph data
func (g *GraphModel) SetIssues(issues []model.Issue, insights *analysis.Insights) {
	g.issues = issues
	g.insights = insights
	g.rebuildGraph()
}

func (g *GraphModel) rebuildGraph() {
	g.issueMap = make(map[string]*model.Issue)
	g.blockers = make(map[string][]string)
	g.dependents = make(map[string][]string)
	g.sortedIDs = nil

	for i := range g.issues {
		issue := &g.issues[i]
		g.issueMap[issue.ID] = issue
		g.sortedIDs = append(g.sortedIDs, issue.ID)
	}

	// Build relationships
	for _, issue := range g.issues {
		for _, dep := range issue.Dependencies {
			if dep.Type == model.DepBlocks || dep.Type == model.DepParentChild {
				g.blockers[issue.ID] = append(g.blockers[issue.ID], dep.DependsOnID)
				g.dependents[dep.DependsOnID] = append(g.dependents[dep.DependsOnID], issue.ID)
			}
		}
	}

	// Compute rankings for all metrics
	g.computeRankings()

	// Sort by critical path score if available, else by ID
	if g.insights != nil && g.insights.Stats != nil {
		sort.Slice(g.sortedIDs, func(i, j int) bool {
			scoreI := g.insights.Stats.CriticalPathScore[g.sortedIDs[i]]
			scoreJ := g.insights.Stats.CriticalPathScore[g.sortedIDs[j]]
			if scoreI != scoreJ {
				return scoreI > scoreJ
			}
			return g.sortedIDs[i] < g.sortedIDs[j]
		})
	} else {
		sort.Strings(g.sortedIDs)
	}

	if g.selectedIdx >= len(g.sortedIDs) {
		g.selectedIdx = 0
	}
}

// computeRankings precomputes rankings for all metrics
func (g *GraphModel) computeRankings() {
	g.rankPageRank = make(map[string]int)
	g.rankBetweenness = make(map[string]int)
	g.rankEigenvector = make(map[string]int)
	g.rankHubs = make(map[string]int)
	g.rankAuthorities = make(map[string]int)
	g.rankCriticalPath = make(map[string]int)
	g.rankInDegree = make(map[string]int)
	g.rankOutDegree = make(map[string]int)

	if g.insights == nil || g.insights.Stats == nil {
		return
	}

	stats := g.insights.Stats

	// Helper to compute ranks from a float64 map (higher = better rank)
	computeFloatRanks := func(m map[string]float64) map[string]int {
		ranks := make(map[string]int)
		type kv struct {
			k string
			v float64
		}
		var sorted []kv
		for k, v := range m {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].v > sorted[j].v // Descending
		})
		for i, item := range sorted {
			ranks[item.k] = i + 1 // 1-indexed
		}
		return ranks
	}

	// Helper for int maps
	computeIntRanks := func(m map[string]int) map[string]int {
		ranks := make(map[string]int)
		type kv struct {
			k string
			v int
		}
		var sorted []kv
		for k, v := range m {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].v > sorted[j].v
		})
		for i, item := range sorted {
			ranks[item.k] = i + 1
		}
		return ranks
	}

	g.rankPageRank = computeFloatRanks(stats.PageRank)
	g.rankBetweenness = computeFloatRanks(stats.Betweenness)
	g.rankEigenvector = computeFloatRanks(stats.Eigenvector)
	g.rankHubs = computeFloatRanks(stats.Hubs)
	g.rankAuthorities = computeFloatRanks(stats.Authorities)
	g.rankCriticalPath = computeFloatRanks(stats.CriticalPathScore)
	g.rankInDegree = computeIntRanks(stats.InDegree)
	g.rankOutDegree = computeIntRanks(stats.OutDegree)
}

// Navigation
func (g *GraphModel) MoveUp() {
	if g.selectedIdx > 0 {
		g.selectedIdx--
		g.ensureVisible()
	}
}

func (g *GraphModel) MoveDown() {
	if g.selectedIdx < len(g.sortedIDs)-1 {
		g.selectedIdx++
		g.ensureVisible()
	}
}

func (g *GraphModel) MoveLeft()  { g.MoveUp() }
func (g *GraphModel) MoveRight() { g.MoveDown() }

func (g *GraphModel) PageUp() {
	g.selectedIdx -= 10
	if g.selectedIdx < 0 {
		g.selectedIdx = 0
	}
	g.ensureVisible()
}

func (g *GraphModel) PageDown() {
	if len(g.sortedIDs) == 0 {
		return
	}
	g.selectedIdx += 10
	if g.selectedIdx >= len(g.sortedIDs) {
		g.selectedIdx = len(g.sortedIDs) - 1
	}
	g.ensureVisible()
}

func (g *GraphModel) ScrollLeft()  {}
func (g *GraphModel) ScrollRight() {}

func (g *GraphModel) ensureVisible() {}

func (g *GraphModel) SelectedIssue() *model.Issue {
	if len(g.sortedIDs) == 0 {
		return nil
	}
	id := g.sortedIDs[g.selectedIdx]
	return g.issueMap[id]
}

func (g *GraphModel) TotalCount() int {
	return len(g.sortedIDs)
}

// View renders the visual graph view
func (g *GraphModel) View(width, height int) string {
	g.width = width
	g.height = height
	t := g.theme

	if len(g.sortedIDs) == 0 {
		return t.Renderer.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(t.Secondary).
			Render("No issues to display")
	}

	selectedID := g.sortedIDs[g.selectedIdx]
	selectedIssue := g.issueMap[selectedID]
	if selectedIssue == nil {
		return "Error: selected issue not found"
	}

	// Layout: Left panel (node list) | Right panel (visual graph + metrics)
	listWidth := 28
	if width < 120 {
		listWidth = 24
	}
	if width < 80 {
		// Narrow: just show visual graph
		return g.renderVisualGraph(selectedID, selectedIssue, width, height, t)
	}

	detailWidth := width - listWidth - 3

	// Left: scrollable list of all nodes
	listView := g.renderNodeList(listWidth, height-2, t)

	// Right: visual graph + metrics
	graphView := g.renderVisualGraph(selectedID, selectedIssue, detailWidth, height-2, t)

	// Combine with separator
	sepHeight := height - 2
	if sepHeight < 1 {
		sepHeight = 1
	}
	separator := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Render(strings.Repeat("│\n", sepHeight))

	return lipgloss.JoinHorizontal(lipgloss.Top, listView, separator, graphView)
}

// renderNodeList renders the left panel with all nodes
func (g *GraphModel) renderNodeList(width, height int, t Theme) string {
	var lines []string

	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Width(width)
	lines = append(lines, headerStyle.Render(fmt.Sprintf("📊 Nodes (%d)", len(g.sortedIDs))))
	lines = append(lines, strings.Repeat("─", width))

	visibleItems := height - 4
	if visibleItems < 1 {
		visibleItems = 1
	}

	startIdx := g.scrollOffset
	if g.selectedIdx < startIdx {
		startIdx = g.selectedIdx
	} else if g.selectedIdx >= startIdx+visibleItems {
		startIdx = g.selectedIdx - visibleItems + 1
	}
	g.scrollOffset = startIdx

	endIdx := startIdx + visibleItems
	if endIdx > len(g.sortedIDs) {
		endIdx = len(g.sortedIDs)
	}

	for i := startIdx; i < endIdx; i++ {
		id := g.sortedIDs[i]
		issue := g.issueMap[id]
		if issue == nil {
			continue
		}

		isSelected := i == g.selectedIdx
		statusIcon := getStatusIcon(issue.Status)
		maxIDLen := width - 4
		displayID := smartTruncateID(id, maxIDLen)
		line := fmt.Sprintf("%s %s", statusIcon, displayID)

		var style lipgloss.Style
		if isSelected {
			style = t.Renderer.NewStyle().
				Bold(true).
				Foreground(t.Primary).
				Background(t.Highlight).
				Width(width)
		} else {
			style = t.Renderer.NewStyle().
				Foreground(getStatusColor(issue.Status, t)).
				Width(width)
		}
		lines = append(lines, style.Render(line))
	}

	if len(g.sortedIDs) > visibleItems {
		scrollInfo := fmt.Sprintf("(%d-%d of %d)", startIdx+1, endIdx, len(g.sortedIDs))
		scrollStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Italic(true).
			Width(width).
			Align(lipgloss.Center)
		lines = append(lines, scrollStyle.Render(scrollInfo))
	}

	return strings.Join(lines, "\n")
}

// renderVisualGraph renders the ASCII art graph visualization with metrics
func (g *GraphModel) renderVisualGraph(id string, issue *model.Issue, width, height int, t Theme) string {
	var sections []string

	blockerIDs := g.blockers[id]
	dependentIDs := g.dependents[id]

	// ═══════════════════════════════════════════════════════════════════════
	// BLOCKERS SECTION (what this issue depends on)
	// ═══════════════════════════════════════════════════════════════════════
	if len(blockerIDs) > 0 {
		sections = append(sections, g.renderBlockersVisual(blockerIDs, width, t))
		// Connecting lines down to ego
		sections = append(sections, g.renderConnectorDown(len(blockerIDs), width, t))
	}

	// ═══════════════════════════════════════════════════════════════════════
	// EGO NODE (selected issue) - prominent center box
	// ═══════════════════════════════════════════════════════════════════════
	sections = append(sections, g.renderEgoNode(id, issue, width, t))

	// ═══════════════════════════════════════════════════════════════════════
	// DEPENDENTS SECTION (what depends on this issue)
	// ═══════════════════════════════════════════════════════════════════════
	if len(dependentIDs) > 0 {
		// Connecting lines down from ego
		sections = append(sections, g.renderConnectorDown(len(dependentIDs), width, t))
		sections = append(sections, g.renderDependentsVisual(dependentIDs, width, t))
	}

	sections = append(sections, "")

	// ═══════════════════════════════════════════════════════════════════════
	// COMPREHENSIVE METRICS PANEL - ALL 8 metrics with values AND ranks
	// ═══════════════════════════════════════════════════════════════════════
	sections = append(sections, g.renderMetricsPanel(id, width, t))

	// Navigation hint
	navStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Italic(true)
	sections = append(sections, "")
	sections = append(sections, navStyle.Render("j/k: navigate • enter: view details • g: back to list"))

	return strings.Join(sections, "\n")
}

// renderBlockersVisual renders blocker nodes as boxes
func (g *GraphModel) renderBlockersVisual(blockerIDs []string, width int, t Theme) string {
	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Feature).
		Width(width).
		Align(lipgloss.Center)

	header := headerStyle.Render("▲ BLOCKED BY (must complete first) ▲")

	// Calculate box width based on available space and number of blockers
	maxBoxes := 5
	if len(blockerIDs) < maxBoxes {
		maxBoxes = len(blockerIDs)
	}
	if maxBoxes < 1 {
		maxBoxes = 1
	}
	boxWidth := (width - 4) / maxBoxes
	if boxWidth > 20 {
		boxWidth = 20
	}
	if boxWidth < 12 {
		boxWidth = 12
	}
	// Ensure boxWidth doesn't exceed available space (narrow terminals)
	if boxWidth > width-2 {
		boxWidth = width - 2
	}
	if boxWidth < 8 {
		boxWidth = 8
	}

	var boxes []string
	for i, bid := range blockerIDs {
		if i >= 5 {
			remaining := len(blockerIDs) - 5
			boxes = append(boxes, t.Renderer.NewStyle().
				Foreground(t.Secondary).
				Italic(true).
				Render(fmt.Sprintf("+%d more", remaining)))
			break
		}
		boxes = append(boxes, g.renderNodeBox(bid, boxWidth, t, false))
	}

	boxRow := lipgloss.JoinHorizontal(lipgloss.Center, boxes...)
	centered := t.Renderer.NewStyle().Width(width).Align(lipgloss.Center).Render(boxRow)

	return header + "\n" + centered
}

// renderDependentsVisual renders dependent nodes as boxes
func (g *GraphModel) renderDependentsVisual(dependentIDs []string, width int, t Theme) string {
	maxBoxes := 5
	if len(dependentIDs) < maxBoxes {
		maxBoxes = len(dependentIDs)
	}
	if maxBoxes < 1 {
		maxBoxes = 1
	}
	boxWidth := (width - 4) / maxBoxes
	if boxWidth > 20 {
		boxWidth = 20
	}
	if boxWidth < 12 {
		boxWidth = 12
	}
	// Ensure boxWidth doesn't exceed available space (narrow terminals)
	if boxWidth > width-2 {
		boxWidth = width - 2
	}
	if boxWidth < 8 {
		boxWidth = 8
	}

	var boxes []string
	for i, did := range dependentIDs {
		if i >= 5 {
			remaining := len(dependentIDs) - 5
			boxes = append(boxes, t.Renderer.NewStyle().
				Foreground(t.Secondary).
				Italic(true).
				Render(fmt.Sprintf("+%d more", remaining)))
			break
		}
		boxes = append(boxes, g.renderNodeBox(did, boxWidth, t, false))
	}

	boxRow := lipgloss.JoinHorizontal(lipgloss.Center, boxes...)
	centered := t.Renderer.NewStyle().Width(width).Align(lipgloss.Center).Render(boxRow)

	headerStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(t.Feature).
		Width(width).
		Align(lipgloss.Center)

	header := headerStyle.Render("▼ BLOCKS (waiting on this) ▼")

	return centered + "\n" + header
}

// renderNodeBox renders a single node as an ASCII box
func (g *GraphModel) renderNodeBox(id string, boxWidth int, t Theme, isEgo bool) string {
	issue := g.issueMap[id]

	var statusIcon, displayID, title string
	var statusColor lipgloss.AdaptiveColor

	if issue != nil {
		statusIcon = getStatusIcon(issue.Status)
		statusColor = getStatusColor(issue.Status, t)
		displayID = smartTruncateID(id, boxWidth-4)
		if issue.Title != "" {
			title = truncateRunesHelper(issue.Title, boxWidth-4, "…")
		}
	} else {
		statusIcon = "❓"
		statusColor = t.Secondary
		displayID = smartTruncateID(id, boxWidth-4)
		title = "(not in filter)"
	}

	// Build box content
	line1 := fmt.Sprintf("%s %s", statusIcon, displayID)

	var boxStyle lipgloss.Style
	if isEgo {
		// Ego node gets double-line border and highlight
		boxStyle = t.Renderer.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(t.Primary).
			Foreground(t.Primary).
			Bold(true).
			Width(boxWidth).
			Align(lipgloss.Center).
			Padding(0, 1)
	} else {
		boxStyle = t.Renderer.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(statusColor).
			Foreground(statusColor).
			Width(boxWidth).
			Align(lipgloss.Center).
			Padding(0, 0)
	}

	content := line1
	if title != "" && boxWidth > 14 {
		content = line1 + "\n" + title
	}

	return boxStyle.Render(content)
}

// renderEgoNode renders the selected/ego node prominently
func (g *GraphModel) renderEgoNode(id string, issue *model.Issue, width int, t Theme) string {
	statusIcon := getStatusIcon(issue.Status)
	prioIcon := getPriorityIcon(issue.Priority)
	typeIcon := getTypeIcon(issue.IssueType)

	egoWidth := width / 2
	if egoWidth > 50 {
		egoWidth = 50
	}
	if egoWidth < 30 {
		egoWidth = 30
	}
	// Don't exceed available width
	if egoWidth > width-4 {
		egoWidth = width - 4
	}
	if egoWidth < 10 {
		egoWidth = 10
	}

	icons := fmt.Sprintf("%s %s %s", statusIcon, prioIcon, typeIcon)
	displayID := smartTruncateID(id, egoWidth-4)
	title := ""
	if issue.Title != "" {
		title = truncateRunesHelper(issue.Title, egoWidth-4, "…")
	}

	content := icons + " " + displayID
	if title != "" {
		content += "\n" + title
	}

	// Add connection counts
	blockerCount := len(g.blockers[id])
	dependentCount := len(g.dependents[id])
	content += fmt.Sprintf("\n⬆%d  ⬇%d", blockerCount, dependentCount)

	egoStyle := t.Renderer.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(t.Primary).
		Foreground(t.Primary).
		Bold(true).
		Width(egoWidth).
		Align(lipgloss.Center).
		Padding(0, 1)

	box := egoStyle.Render(content)

	// Center the ego box
	return t.Renderer.NewStyle().Width(width).Align(lipgloss.Center).Render(box)
}

// renderConnectorDown renders connector lines between sections
func (g *GraphModel) renderConnectorDown(count int, width int, t Theme) string {
	if count == 0 {
		return ""
	}

	connStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Width(width).
		Align(lipgloss.Center)

	if count == 1 {
		return connStyle.Render("│\n│\n▼")
	}

	// Multiple connections - fan pattern using proper rune slicing
	// Pattern chars: ├ ─ ┼ ─ ┼ ─ ┤ (for 3 connections)
	lines := []string{"│"}

	// Build the connector pattern properly
	var pattern strings.Builder
	pattern.WriteRune('├')
	for i := 0; i < count && i < 4; i++ {
		if i > 0 {
			pattern.WriteRune('┼')
		}
		pattern.WriteRune('─')
	}
	pattern.WriteRune('┤')
	lines = append(lines, pattern.String())
	lines = append(lines, "▼")

	return connStyle.Render(strings.Join(lines, "\n"))
}

// renderMetricsPanel renders ALL graph metrics with polished visualization
func (g *GraphModel) renderMetricsPanel(id string, width int, t Theme) string {
	total := len(g.sortedIDs)

	// ══════════════════════════════════════════════════════════════════════════
	// POLISHED METRICS PANEL - Stripe-level visual design
	// ══════════════════════════════════════════════════════════════════════════

	// Panel header with accent background
	panelHeaderStyle := t.Renderer.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorPrimary).
		Padding(0, 2).
		Width(width - 4)

	panelTitle := panelHeaderStyle.Render("📊 GRAPH METRICS")

	if g.insights == nil || g.insights.Stats == nil {
		noDataStyle := t.Renderer.NewStyle().
			Foreground(ColorMuted).
			Italic(true).
			Padding(1, 2).
			Width(width - 4).
			Align(lipgloss.Center)
		return panelTitle + "\n" + noDataStyle.Render("No graph analysis data available")
	}

	stats := g.insights.Stats

	// Get all values and ranks
	pageRank := stats.PageRank[id]
	betweenness := stats.Betweenness[id]
	eigenvector := stats.Eigenvector[id]
	hubs := stats.Hubs[id]
	authorities := stats.Authorities[id]
	critPath := stats.CriticalPathScore[id]
	inDeg := float64(stats.InDegree[id])
	outDeg := float64(stats.OutDegree[id])

	rankPR := g.rankPageRank[id]
	rankBW := g.rankBetweenness[id]
	rankEV := g.rankEigenvector[id]
	rankHub := g.rankHubs[id]
	rankAuth := g.rankAuthorities[id]
	rankCP := g.rankCriticalPath[id]
	rankIn := g.rankInDegree[id]
	rankOut := g.rankOutDegree[id]

	// Default ranks to total if 0
	if rankPR == 0 {
		rankPR = total
	}
	if rankBW == 0 {
		rankBW = total
	}
	if rankEV == 0 {
		rankEV = total
	}
	if rankHub == 0 {
		rankHub = total
	}
	if rankAuth == 0 {
		rankAuth = total
	}
	if rankCP == 0 {
		rankCP = total
	}
	if rankIn == 0 {
		rankIn = total
	}
	if rankOut == 0 {
		rankOut = total
	}

	// Helper to render a metric row with mini-bar visualization
	renderMetricRow := func(name string, value float64, rank int, maxVal float64, isInt bool) string {
		// Name with fixed width
		nameStyle := t.Renderer.NewStyle().Foreground(ColorSecondary).Width(14)

		// Value formatting
		var valStr string
		if isInt {
			valStr = fmt.Sprintf("%d", int(value))
		} else if value >= 1.0 {
			valStr = fmt.Sprintf("%.2f", value)
		} else {
			valStr = fmt.Sprintf("%.4f", value)
		}
		valueStyle := t.Renderer.NewStyle().Foreground(ColorText).Bold(true).Width(8).Align(lipgloss.Right)

		// Mini-bar for relative importance (normalize to 0-1)
		normalized := 0.0
		if maxVal > 0 {
			normalized = value / maxVal
		}
		bar := RenderMiniBar(normalized, 6)

		// Rank badge
		rankBadge := RenderRankBadge(rank, total)

		return nameStyle.Render(name) + " " + valueStyle.Render(valStr) + " " + bar + " " + rankBadge
	}

	// Find max values for normalization
	maxCP, maxPR, maxBW, maxEV := 0.0, 0.0, 0.0, 0.0
	maxHub, maxAuth, maxIn, maxOut := 0.0, 0.0, 0.0, 0.0
	for _, issueID := range g.sortedIDs {
		if v := stats.CriticalPathScore[issueID]; v > maxCP {
			maxCP = v
		}
		if v := stats.PageRank[issueID]; v > maxPR {
			maxPR = v
		}
		if v := stats.Betweenness[issueID]; v > maxBW {
			maxBW = v
		}
		if v := stats.Eigenvector[issueID]; v > maxEV {
			maxEV = v
		}
		if v := stats.Hubs[issueID]; v > maxHub {
			maxHub = v
		}
		if v := stats.Authorities[issueID]; v > maxAuth {
			maxAuth = v
		}
		if v := float64(stats.InDegree[issueID]); v > maxIn {
			maxIn = v
		}
		if v := float64(stats.OutDegree[issueID]); v > maxOut {
			maxOut = v
		}
	}

	var rows []string
	rows = append(rows, panelTitle)
	rows = append(rows, RenderDivider(width-4))

	// Section: Importance Metrics
	sectionStyle := t.Renderer.NewStyle().
		Foreground(ColorPrimary).
		Bold(true).
		Padding(0, 1)
	rows = append(rows, sectionStyle.Render("Importance"))
	rows = append(rows, "  "+renderMetricRow("Critical Path", critPath, rankCP, maxCP, false))
	rows = append(rows, "  "+renderMetricRow("PageRank", pageRank, rankPR, maxPR, false))
	rows = append(rows, "  "+renderMetricRow("Eigenvector", eigenvector, rankEV, maxEV, false))

	rows = append(rows, "")

	// Section: Flow Metrics
	rows = append(rows, sectionStyle.Render("Flow & Connectivity"))
	rows = append(rows, "  "+renderMetricRow("Betweenness", betweenness, rankBW, maxBW, false))
	rows = append(rows, "  "+renderMetricRow("Hub Score", hubs, rankHub, maxHub, false))
	rows = append(rows, "  "+renderMetricRow("Authority", authorities, rankAuth, maxAuth, false))

	rows = append(rows, "")

	// Section: Degree
	rows = append(rows, sectionStyle.Render("Connections"))
	rows = append(rows, "  "+renderMetricRow("In-Degree", inDeg, rankIn, maxIn, true))
	rows = append(rows, "  "+renderMetricRow("Out-Degree", outDeg, rankOut, maxOut, true))

	rows = append(rows, "")

	// Legend
	legendStyle := t.Renderer.NewStyle().
		Foreground(ColorMuted).
		Italic(true).
		Width(width - 4)

	rows = append(rows, legendStyle.Render("█ relative score │ #N rank of "+fmt.Sprintf("%d", total)+" issues"))

	return strings.Join(rows, "\n")
}

// Helper functions

func getStatusIcon(status model.Status) string {
	switch status {
	case model.StatusOpen:
		return "🔵"
	case model.StatusInProgress:
		return "🟡"
	case model.StatusBlocked:
		return "🔴"
	case model.StatusClosed:
		return "✅"
	default:
		return "⚪"
	}
}

func getStatusColor(status model.Status, t Theme) lipgloss.AdaptiveColor {
	switch status {
	case model.StatusOpen:
		return t.Open
	case model.StatusInProgress:
		return t.InProgress
	case model.StatusBlocked:
		return t.Blocked
	case model.StatusClosed:
		return t.Closed
	default:
		return t.Secondary
	}
}

func getPriorityIcon(priority int) string {
	switch priority {
	case 1:
		return "🔥"
	case 2:
		return "⚡"
	case 3:
		return "📌"
	case 4:
		return "📋"
	default:
		return "  "
	}
}

func getTypeIcon(itype model.IssueType) string {
	switch itype {
	case model.TypeBug:
		return "🐛"
	case model.TypeFeature:
		return "✨"
	case model.TypeTask:
		return "📝"
	case model.TypeEpic:
		return "🎯"
	case model.TypeChore:
		return "🔧"
	default:
		return "📄"
	}
}

func smartTruncateID(id string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	runes := []rune(id)
	if len(runes) <= maxLen {
		return id
	}

	parts := strings.Split(id, "_")
	if len(parts) > 2 {
		var abbrev strings.Builder
		runeCount := 0
		for i, part := range parts {
			partRunes := []rune(part)
			if i == len(parts)-1 {
				// Last part: keep as much as possible
				remaining := maxLen - runeCount
				if remaining > 0 {
					if len(partRunes) <= remaining {
						abbrev.WriteString(part)
					} else if remaining > 1 {
						abbrev.WriteString(string(partRunes[:remaining-1]))
						abbrev.WriteRune('…')
					} else {
						abbrev.WriteRune('…')
					}
				}
			} else {
				// Non-last parts: just first char + underscore
				if len(partRunes) > 0 {
					abbrev.WriteRune(partRunes[0])
					abbrev.WriteRune('_')
					runeCount += 2
				}
			}
		}
		result := abbrev.String()
		if len([]rune(result)) <= maxLen {
			return result
		}
	}

	// Fallback: simple truncation
	if maxLen > 1 {
		return string(runes[:maxLen-1]) + "…"
	}
	return string(runes[:maxLen])
}
