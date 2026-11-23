package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// GraphModel represents the dependency graph view with visual ASCII art visualization
type GraphModel struct {
	issues         []model.Issue
	issueMap       map[string]*model.Issue
	insights       *analysis.Insights
	analyzer       *analysis.Analyzer
	etaCtx         *analysis.ETAContext
	priorityHints  map[string]*analysis.PriorityRecommendation
	nowFunc        func() time.Time
	selectedIdx    int
	scrollOffset   int
	width          int
	height         int
	theme          Theme
	scrollX        int
	scrollY        int
	maxScrollX     int
	maxScrollY     int
	expanded       map[string]bool
	criticalPath   map[string]bool
	criticalNext   map[string]string
	canvasID       string
	canvasWidth    int
	canvasExpanded bool
	canvasLines    []string
	canvasColumns  int

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

// SetSnapshot updates the graph data from a pre-built DataSnapshot (bv-za8z).
// This avoids rebuilding blockers/dependents and metric ranks on the UI thread.
func (g *GraphModel) SetSnapshot(snapshot *DataSnapshot) {
	if snapshot == nil {
		return
	}
	g.canvasLines = nil

	// Capture current selection
	var selectedID string
	if len(g.sortedIDs) > 0 && g.selectedIdx >= 0 && g.selectedIdx < len(g.sortedIDs) {
		selectedID = g.sortedIDs[g.selectedIdx]
	}

	g.issues = snapshot.Issues
	g.issueMap = snapshot.IssueMap
	g.insights = &snapshot.insights

	if g.issueMap == nil {
		g.issueMap = make(map[string]*model.Issue, len(g.issues))
		for i := range g.issues {
			g.issueMap[g.issues[i].ID] = &g.issues[i]
		}
	}

	layout := snapshot.GetGraphLayout()
	if layout != nil && len(layout.SortedIDs) > 0 {
		g.blockers = layout.Blockers
		g.dependents = layout.Dependents
		g.sortedIDs = layout.SortedIDs

		g.rankPageRank = layout.RankPageRank
		g.rankBetweenness = layout.RankBetweenness
		g.rankEigenvector = layout.RankEigenvector
		g.rankHubs = layout.RankHubs
		g.rankAuthorities = layout.RankAuthorities
		g.rankCriticalPath = layout.RankCriticalPath
		g.rankInDegree = layout.RankInDegree
		g.rankOutDegree = layout.RankOutDegree
		g.criticalPath = layout.CriticalPath
		g.criticalNext = layout.CriticalNext
	} else {
		g.rebuildGraph()
	}

	// Restore selection
	if selectedID != "" {
		found := false
		for i, id := range g.sortedIDs {
			if id == selectedID {
				g.selectedIdx = i
				found = true
				break
			}
		}
		if !found && g.selectedIdx >= len(g.sortedIDs) {
			g.selectedIdx = 0
		}
	}
	if g.selectedIdx >= len(g.sortedIDs) {
		g.selectedIdx = 0
	}
	g.restoreNavigation(selectedID)
}

// SetAnalyzer injects the active scoped Analyzer so What-If impact in the
// metrics panel reuses the model's readiness/candidate scope without rebuilding.
func (g *GraphModel) SetAnalyzer(analyzer *analysis.Analyzer, nowFunc func() time.Time) {
	if g.analyzer != analyzer {
		g.analyzer = analyzer
		g.canvasLines = nil
	}
	g.nowFunc = nowFunc
}

// SetETAContext injects a precomputed full-corpus ETAContext so Graph View ETA
// forecasts stay O(1+L) and match the Detail Pane even when g.issues is filtered
// to open/ready issues (excluding closed closure history).
func (g *GraphModel) SetETAContext(ctx *analysis.ETAContext) {
	if g.etaCtx != ctx {
		g.etaCtx = ctx
		g.canvasLines = nil
	}
}

// SetPriorityHints injects priority misalignment hints so the Graph metrics
// panel surfaces suggested priority changes alongside What-If & ETA.
func (g *GraphModel) SetPriorityHints(hints map[string]*analysis.PriorityRecommendation) {
	g.priorityHints = hints
}

// SetIssues updates the graph data preserving the selected issue if possible
func (g *GraphModel) SetIssues(issues []model.Issue, insights *analysis.Insights) {
	// Capture current selection
	var selectedID string
	if len(g.sortedIDs) > 0 && g.selectedIdx >= 0 && g.selectedIdx < len(g.sortedIDs) {
		selectedID = g.sortedIDs[g.selectedIdx]
	}

	g.issues = issues
	g.insights = insights
	g.rebuildGraph()

	// Restore selection
	if selectedID != "" {
		// Try to find the previously selected ID in the new list
		found := false
		for i, id := range g.sortedIDs {
			if id == selectedID {
				g.selectedIdx = i
				found = true
				break
			}
		}
		// If not found (e.g. filter changed or issue deleted), selectedIdx
		// was reset to 0 or clamped in rebuildGraph, which is acceptable behavior.
		if !found {
			// Ensure we don't end up out of bounds if sortedIDs shrank
			if g.selectedIdx >= len(g.sortedIDs) {
				g.selectedIdx = 0
			}
		}
	}
	g.restoreNavigation(selectedID)
}

func (g *GraphModel) restoreNavigation(selectedID string) {
	for id := range g.expanded {
		if g.issueMap[id] == nil {
			delete(g.expanded, id)
		}
	}
	if selected := g.SelectedIssue(); selected == nil || selected.ID != selectedID {
		g.ensureVisible()
	}
}

func (g *GraphModel) rebuildGraph() {
	g.canvasLines = nil
	size := len(g.issues)
	g.issueMap = make(map[string]*model.Issue, size)
	g.blockers = make(map[string][]string, size)
	g.dependents = make(map[string][]string, size)
	g.sortedIDs = make([]string, 0, size)

	for i := range g.issues {
		issue := &g.issues[i]
		g.issueMap[issue.ID] = issue
		g.sortedIDs = append(g.sortedIDs, issue.ID)
	}

	// Build relationships
	for _, issue := range g.issues {
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type.IsBlocking() {
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
			scoreI := g.insights.Stats.GetCriticalPathScore(g.sortedIDs[i])
			scoreJ := g.insights.Stats.GetCriticalPathScore(g.sortedIDs[j])
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
	g.criticalPath, g.criticalNext = visibleCriticalChain(g.sortedIDs, g.blockers, g.dependents)
}

// visibleCriticalChain selects one deterministic longest chain in the visible
// graph. Project-wide scores cannot choose it: filtering can hide an entire long
// branch. A cycle prevents topological completion, so no chain is invented there.
// Snapshot construction does this work off the UI loop; filtered views use the
// same calculation when their visible graph changes.
func visibleCriticalChain(ids []string, blockers, dependents map[string][]string) (map[string]bool, map[string]string) {
	pending := make(map[string]int, len(ids))
	depth := make(map[string]int, len(ids))
	previous := make(map[string]string, len(ids))
	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		depth[id] = 1
	}
	for _, id := range ids {
		for _, blocker := range blockers[id] {
			if _, visible := depth[blocker]; visible {
				pending[id]++
			}
		}
		if pending[id] == 0 {
			queue = append(queue, id)
		}
	}
	current := ""
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		if depth[id] > depth[current] || (depth[id] == depth[current] && id < current) {
			current = id
		}
		for _, dependent := range dependents[id] {
			if _, visible := depth[dependent]; !visible {
				continue
			}
			if d := depth[id] + 1; d > depth[dependent] || (d == depth[dependent] && id < previous[dependent]) {
				depth[dependent], previous[dependent] = d, id
			}
			pending[dependent]--
			if pending[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if len(queue) != len(ids) || current == "" {
		return nil, nil
	}
	criticalPath := make(map[string]bool)
	criticalNext := make(map[string]string)
	for current != "" {
		criticalPath[current] = true
		parent := previous[current]
		if parent != "" {
			criticalNext[parent] = current
		}
		current = parent
	}
	return criticalPath, criticalNext
}

// computeRankings precomputes rankings for all metrics
func (g *GraphModel) computeRankings() {
	g.rankPageRank = nil
	g.rankBetweenness = nil
	g.rankEigenvector = nil
	g.rankHubs = nil
	g.rankAuthorities = nil
	g.rankCriticalPath = nil
	g.rankInDegree = nil
	g.rankOutDegree = nil

	if g.insights == nil || g.insights.Stats == nil {
		return
	}

	stats := g.insights.Stats

	// Reuse precomputed ranks from analysis (computed in Phase 1/2).
	g.rankPageRank = stats.PageRankRank()
	g.rankBetweenness = stats.ActiveBetweennessRank()
	g.rankEigenvector = stats.EigenvectorRank()
	g.rankHubs = stats.HubsRank()
	g.rankAuthorities = stats.AuthoritiesRank()
	g.rankCriticalPath = stats.CriticalPathRank()
	g.rankInDegree = stats.InDegreeRank()
	g.rankOutDegree = stats.OutDegreeRank()
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

func (g *GraphModel) MoveToStart() {
	if len(g.sortedIDs) == 0 {
		return
	}
	g.selectedIdx = 0
	g.ensureVisible()
}

func (g *GraphModel) MoveToEnd() {
	if len(g.sortedIDs) == 0 {
		return
	}
	g.selectedIdx = len(g.sortedIDs) - 1
	g.ensureVisible()
}

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

func (g *GraphModel) ScrollLeft() { g.scrollX = max(0, g.scrollX-8) }
func (g *GraphModel) ScrollRight() {
	g.ensureCanvasBounds()
	g.scrollX = min(g.maxScrollX, g.scrollX+8)
}
func (g *GraphModel) ScrollUp() { g.scrollY = max(0, g.scrollY-3) }
func (g *GraphModel) ScrollDown() {
	g.ensureCanvasBounds()
	g.scrollY = min(g.maxScrollY, g.scrollY+3)
}

func (g *GraphModel) visualGraphDimensions(width, height int) (int, int) {
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 40
	}
	if width < 80 {
		return width, height
	}
	listWidth := 28
	if width < 120 {
		listWidth = 24
	}
	return width - listWidth - 3, height - 2
}

func (g *GraphModel) ensureCanvasBounds() {
	issue := g.SelectedIssue()
	if issue == nil {
		g.maxScrollX, g.maxScrollY = 0, 0
		return
	}
	id := issue.ID
	paneWidth, paneHeight := g.visualGraphDimensions(g.width, g.height)
	viewportWidth, viewportHeight := max(1, paneWidth), max(1, paneHeight)
	canvasWidth := max(60, viewportWidth)
	neighbors := max(len(g.blockers[id]), len(g.dependents[id]))
	rowWidth := min(5, neighbors) * 22
	if neighbors > 5 {
		rowWidth += len(fmt.Sprintf("+%d more", neighbors-5))
	}
	canvasWidth = max(canvasWidth, rowWidth)
	t := g.theme
	if t.Renderer == nil {
		t = DefaultTheme(lipgloss.DefaultRenderer())
	}
	if g.canvasLines == nil || g.canvasID != id || g.canvasWidth != canvasWidth || g.canvasExpanded != g.expanded[id] {
		g.canvasLines = g.renderGraphCanvas(id, issue, canvasWidth, t)
		g.canvasID, g.canvasWidth, g.canvasExpanded = id, canvasWidth, g.expanded[id]
		g.canvasColumns = 0
		for _, line := range g.canvasLines {
			g.canvasColumns = max(g.canvasColumns, ansi.StringWidth(line))
		}
	}
	bodyHeight := max(0, viewportHeight-2)
	g.maxScrollX = max(0, g.canvasColumns-viewportWidth)
	g.maxScrollY = max(0, len(g.canvasLines)-max(1, bodyHeight))
}

func (g *GraphModel) ensureVisible() {
	g.scrollX, g.scrollY = 0, 0
}

// ToggleExpand shows the selected node's transitive dependency paths. Expansion
// belongs to the node, so returning to it preserves the user's choice.
func (g *GraphModel) ToggleExpand() {
	if issue := g.SelectedIssue(); issue != nil {
		if g.expanded == nil {
			g.expanded = make(map[string]bool)
		}
		g.expanded[issue.ID] = !g.expanded[issue.ID]
		g.ensureVisible()
	}
}

func (g *GraphModel) SelectedIssue() *model.Issue {
	if len(g.sortedIDs) == 0 || g.selectedIdx < 0 || g.selectedIdx >= len(g.sortedIDs) {
		return nil
	}
	id := g.sortedIDs[g.selectedIdx]
	return g.issueMap[id]
}

// SelectByID selects an issue by its ID (bv-xf4p)
func (g *GraphModel) SelectByID(id string) bool {
	for i, sortedID := range g.sortedIDs {
		if sortedID == id {
			g.selectedIdx = i
			g.ensureVisible()
			return true
		}
	}
	return false
}

func (g *GraphModel) TotalCount() int {
	return len(g.sortedIDs)
}

// SetSize updates the graph dimensions before rendering so scroll bounds
// respect terminal resize events prior to the next View() call.
func (g *GraphModel) SetSize(width, height int) {
	g.width = width
	g.height = height
}

// View renders the visual graph view
func (g *GraphModel) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	g.width = width
	g.height = height
	t := g.theme

	if len(g.sortedIDs) == 0 || g.selectedIdx < 0 || g.selectedIdx >= len(g.sortedIDs) {
		empty := t.Renderer.NewStyle().
			Width(width).
			Height(height).
			Align(lipgloss.Center, lipgloss.Center).
			Foreground(t.Secondary).
			Render("No issues to display")
		return g.clipGraph(empty, width, height, 0, 0)
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

	return g.clipGraph(lipgloss.JoinHorizontal(lipgloss.Top, listView, separator, graphView), width, height, 0, 0)
}

// clipGraph cuts terminal cells, not bytes/runes, preserving graphemes and ANSI
// style sequences even when a wide character straddles the viewport boundary.
func (g *GraphModel) clipGraph(content string, width, height, x, y int) string {
	w := max(0, width)
	h := max(0, height)
	lines := strings.Split(content, "\n")
	y = min(max(0, y), len(lines))
	lines = lines[y:min(len(lines), y+h)]
	for i, line := range lines {
		if w == 0 {
			lines[i] = ""
			continue
		}
		if x > 0 && ansi.StringWidth(line) > x && ansi.StringWidth(ansi.Cut(line, 0, x)) < x {
			nextX := x + 1
			for nextX < x+w && ansi.StringWidth(ansi.Cut(line, 0, nextX)) < nextX {
				nextX++
			}
			pad := strings.Repeat(" ", min(w, nextX-x))
			if nextX >= x+w {
				lines[i] = pad
			} else {
				lines[i] = pad + ansi.Cut(line, nextX, x+w)
			}
		} else {
			lines[i] = ansi.Cut(line, x, x+w)
		}
		if ansi.StringWidth(lines[i]) > w {
			lines[i] = ansi.Truncate(lines[i], w, "")
		}
	}
	return strings.Join(lines, "\n")
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

func (g *GraphModel) renderGraphCanvas(id string, issue *model.Issue, width int, t Theme) []string {
	var sections []string
	if g.expanded[id] {
		sections = append(sections, g.renderDependencyPaths(id, t), "")
	}

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

	return strings.Split(strings.Join(sections, "\n"), "\n")
}

// Cache only the selected canvas, invalidating it on data, width or expansion
// changes. Repeated pan/scroll keys then render just the visible lines instead of
// rebuilding every transitive edge and every metric row for each frame.
func (g *GraphModel) renderVisualGraph(id string, issue *model.Issue, width, height int, t Theme) string {
	viewportWidth, viewportHeight := max(1, width), max(1, height)
	canvasWidth := max(60, viewportWidth)
	neighbors := max(len(g.blockers[id]), len(g.dependents[id]))
	rowWidth := min(5, neighbors) * 22 // 20 content cells plus two border cells
	if neighbors > 5 {
		rowWidth += len(fmt.Sprintf("+%d more", neighbors-5))
	}
	canvasWidth = max(canvasWidth, rowWidth)
	if g.canvasLines == nil || g.canvasID != id || g.canvasWidth != canvasWidth || g.canvasExpanded != g.expanded[id] {
		g.canvasLines = g.renderGraphCanvas(id, issue, canvasWidth, t)
		g.canvasID, g.canvasWidth, g.canvasExpanded = id, canvasWidth, g.expanded[id]
		g.canvasColumns = 0
		for _, line := range g.canvasLines {
			g.canvasColumns = max(g.canvasColumns, ansi.StringWidth(line))
		}
	}
	bodyHeight := max(0, viewportHeight-2)
	g.maxScrollX = max(0, g.canvasColumns-viewportWidth)
	g.maxScrollY = max(0, len(g.canvasLines)-max(1, bodyHeight))
	g.scrollX = min(g.scrollX, g.maxScrollX)
	g.scrollY = min(g.scrollY, g.maxScrollY)
	state := "collapsed"
	if g.expanded[id] {
		state = "expanded"
	}
	statusID := smartTruncateID(id, max(4, viewportWidth/3))
	status := fmt.Sprintf("%s • %s • column %d/%d • row %d/%d", statusID, state, g.scrollX+1, g.maxScrollX+1, g.scrollY+1, g.maxScrollY+1)
	hint := "H/L: pan • J/K: scroll • space: expand • j/k: select • enter: detail • ◆ critical path"
	foot := t.Renderer.NewStyle().Foreground(t.Secondary).Render(status + "\n" + hint)
	if bodyHeight == 0 {
		return g.clipGraph(foot, viewportWidth, viewportHeight, 0, 0)
	}
	visible := strings.Join(g.canvasLines[g.scrollY:min(len(g.canvasLines), g.scrollY+bodyHeight)], "\n")
	body := g.clipGraph(visible, viewportWidth, bodyHeight, g.scrollX, 0)
	return body + "\n" + g.clipGraph(foot, viewportWidth, 2, 0, 0)
}

// renderDependencyPaths lists actual edges in both directions without treating
// siblings as a chain. Each node is visited once per direction; cycles and shared
// branches remain visible as edges but cannot recurse forever. Out-of-scope
// endpoints are labeled and never traversed into hidden issue data.
func (g *GraphModel) renderDependencyPaths(id string, t Theme) string {
	lines := []string{t.Renderer.NewStyle().Bold(true).Foreground(t.Feature).Render("DEPENDENCY PATHS (prerequisite → dependent)")}
	for _, direction := range []struct {
		title string
		edges map[string][]string
		up    bool
	}{{"Upstream", g.blockers, true}, {"Downstream", g.dependents, false}} {
		lines = append(lines, direction.title)
		queue := []string{id}
		seen := map[string]bool{id: true}
		for i := 0; i < len(queue); i++ {
			from := queue[i]
			neighbors := append([]string(nil), direction.edges[from]...)
			sort.Strings(neighbors)
			for j, to := range neighbors {
				if j > 0 && neighbors[j-1] == to {
					continue
				}
				left, right := from, to
				if direction.up {
					left, right = to, from
				}
				line := "  " + left + " → " + right
				if g.issueMap[to] == nil {
					line += " (not in filter)"
				} else if !seen[to] {
					seen[to] = true
					queue = append(queue, to)
				}
				if g.criticalNext[left] == right {
					line = t.Renderer.NewStyle().Foreground(t.Feature).Bold(true).Render("◆" + line)
				}
				lines = append(lines, line)
			}
		}
	}
	return strings.Join(lines, "\n")
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
	if g.criticalPath[id] {
		line1 = "◆ " + line1
		statusColor = t.Feature
	}

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
	if g.criticalPath[id] {
		content = "◆ " + content
	}
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

	// Get all values and ranks (using thread-safe accessors for Phase 2 data)
	pageRank := stats.GetPageRankScore(id)
	betweenness := stats.GetBetweennessScore(id)
	eigenvector := stats.GetEigenvectorScore(id)
	hubs := stats.GetHubScore(id)
	authorities := stats.GetAuthorityScore(id)
	critPath := stats.GetCriticalPathScore(id)
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
		bar := RenderMiniBar(normalized, 6, t)

		// Rank badge
		rankBadge := RenderRankBadge(rank, total)

		return nameStyle.Render(name) + " " + valueStyle.Render(valStr) + " " + bar + " " + rankBadge
	}

	// Find max values for normalization (using thread-safe accessors)
	maxCP, maxPR, maxBW, maxEV := 0.0, 0.0, 0.0, 0.0
	maxHub, maxAuth, maxIn, maxOut := 0.0, 0.0, 0.0, 0.0
	for _, issueID := range g.sortedIDs {
		if v := stats.GetCriticalPathScore(issueID); v > maxCP {
			maxCP = v
		}
		if v := stats.GetPageRankScore(issueID); v > maxPR {
			maxPR = v
		}
		if v := stats.GetBetweennessScore(issueID); v > maxBW {
			maxBW = v
		}
		if v := stats.GetEigenvectorScore(issueID); v > maxEV {
			maxEV = v
		}
		if v := stats.GetHubScore(issueID); v > maxHub {
			maxHub = v
		}
		if v := stats.GetAuthorityScore(issueID); v > maxAuth {
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

	// Section: What-If, Root Blockers & ETA Forecast (open issues only, when wired with scoped Analyzer)
	if selIssue := g.issueMap[id]; selIssue != nil && !isClosedLikeStatus(selIssue.Status) && g.analyzer != nil {
		whatIf := g.analyzer.ComputeWhatIf(id, stats)
		blockerChain := g.analyzer.GetBlockerChain(id)
		var visibleRoots []analysis.BlockerChainEntry
		if blockerChain != nil && blockerChain.IsBlocked {
			for _, rb := range blockerChain.RootBlockers {
				// Respect current GraphView filter scope: never leak hidden/out-of-filter context IDs.
				if _, ok := g.issueMap[rb.ID]; ok {
					visibleRoots = append(visibleRoots, rb)
				}
			}
		}
		hasRootBlockers := blockerChain != nil && blockerChain.IsBlocked && (len(visibleRoots) > 0 || blockerChain.HasCycle)
		var eta analysis.ETAEstimate
		var etaErr error
		etaEligible := !selIssue.IsTemplate && !selIssue.Pinned && selIssue.Status != model.StatusPinned
		if !etaEligible {
			etaErr = fmt.Errorf("not eta-eligible")
		} else if g.etaCtx != nil {
			eta = g.etaCtx.EstimateForIssue(*selIssue, 1)
			if eta.EstimatedDays <= 0 || eta.ETADate.IsZero() {
				etaErr = fmt.Errorf("no eta")
			}
		} else {
			now := g.analyzer.Now()
			if g.nowFunc != nil {
				now = g.nowFunc()
			}
			eta, etaErr = analysis.EstimateETAForIssue(g.issues, stats, id, 1, now)
		}
		var prioHint *analysis.PriorityRecommendation
		if g.priorityHints != nil {
			if h := g.priorityHints[id]; h != nil && h.SuggestedPriority != selIssue.Priority {
				prioHint = h
			}
		}
		if (whatIf != nil && (whatIf.Delta.DirectUnblocks > 0 || whatIf.Delta.TransitiveUnblocks > 0)) || hasRootBlockers || etaErr == nil || prioHint != nil {
			rows = append(rows, "")
			rows = append(rows, sectionStyle.Render("What-If & Forecast"))
			detailStyle := t.Renderer.NewStyle().Foreground(ColorText)
			mutedStyle := t.Renderer.NewStyle().Foreground(ColorMuted)
			if prioHint != nil {
				arrow := "↓"
				if prioHint.Direction == "increase" {
					arrow = "↑"
				}
				hintLine := fmt.Sprintf("  Priority hint: %s P%d→P%d (%.0f%%)", arrow, prioHint.CurrentPriority, prioHint.SuggestedPriority, prioHint.Confidence*100)
				if len(prioHint.Reasoning) > 0 {
					hintLine += " — " + prioHint.Reasoning[0]
				}
				rows = append(rows, detailStyle.Render(truncateRunesHelper(hintLine, max(20, width-4), "…")))
			}
			if hasRootBlockers {
				if len(visibleRoots) > 0 {
					rootLabels := make([]string, 0, min(3, len(visibleRoots)))
					for i, rb := range visibleRoots {
						if i >= 3 {
							break
						}
						tag := rb.ID
						if rb.Actionable {
							tag += " ✓"
						}
						rootLabels = append(rootLabels, tag)
					}
					suffix := ""
					if len(visibleRoots) > len(rootLabels) {
						suffix = fmt.Sprintf(" +%d", len(visibleRoots)-len(rootLabels))
					}
					rootLine := fmt.Sprintf("  Start here (%d blk): %s%s", blockerChain.ChainLength, strings.Join(rootLabels, ", "), suffix)
					rows = append(rows, detailStyle.Render(truncateRunesHelper(rootLine, max(20, width-4), "…")))
				}
				if blockerChain.HasCycle {
					cycLine := "  ⚠ Blocked by dependency cycle"
					rows = append(rows, mutedStyle.Render(truncateRunesHelper(cycLine, max(20, width-4), "…")))
				}
			}
			if whatIf != nil && (whatIf.Delta.DirectUnblocks > 0 || whatIf.Delta.TransitiveUnblocks > 0) {
				unblockLine := fmt.Sprintf("  Closing unblocks +%d direct", whatIf.Delta.DirectUnblocks)
				if whatIf.Delta.TransitiveUnblocks > whatIf.Delta.DirectUnblocks {
					unblockLine += fmt.Sprintf(" (+%d total)", whatIf.Delta.TransitiveUnblocks)
				}
				if len(whatIf.Delta.UnblockedIssueIDs) > 0 {
					shown := whatIf.Delta.UnblockedIssueIDs
					if len(shown) > 3 {
						shown = shown[:3]
					}
					totalDirect := max(whatIf.Delta.DirectUnblocks, len(whatIf.Delta.UnblockedIssueIDs))
					suffix := ""
					if totalDirect > len(shown) {
						suffix = fmt.Sprintf(" +%d", totalDirect-len(shown))
					}
					unblockLine += " → " + strings.Join(shown, ", ") + suffix
				}
				rows = append(rows, detailStyle.Render(unblockLine))
			}
			if etaErr == nil {
				confPct := int(math.Round(eta.Confidence * 100))
				etaLine := fmt.Sprintf("  ETA: %s (~%.1fd • %d%% conf)", eta.ETADate.Format("2006-01-02"), eta.EstimatedDays, confPct)
				rows = append(rows, mutedStyle.Render(etaLine))
			}
		}
	}

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
	switch {
	case isClosedLikeStatus(status):
		return "✅"
	case status == model.StatusOpen:
		return "🔵"
	case status == model.StatusInProgress:
		return "🟡"
	case status == model.StatusBlocked:
		return "🔴"
	case status == model.StatusDeferred || status == model.StatusDraft:
		return "⏸️"
	case status == model.StatusPinned:
		return "📌"
	case status == model.StatusHooked:
		return "🪝"
	case status == model.StatusReview:
		return "👁️"
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
	case model.StatusDeferred, model.StatusDraft:
		return t.Deferred
	case model.StatusPinned:
		return t.Pinned
	case model.StatusHooked:
		return t.Hooked
	case model.StatusReview:
		return t.Review
	case model.StatusClosed:
		return t.Closed
	case model.StatusTombstone:
		return t.Tombstone
	default:
		return t.Secondary
	}
}

func getPriorityIcon(priority int) string {
	switch priority {
	case 0:
		return "🚨"
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

	clamp := func(s string) string {
		r := []rune(s)
		if len(r) <= maxLen {
			return s
		}
		if maxLen == 1 {
			return string(r[:1])
		}
		return string(r[:maxLen-1]) + "…"
	}

	runes := []rune(id)
	if len(runes) <= maxLen {
		return id
	}

	// Split by common separators to abbreviate parts
	f := func(c rune) bool {
		return c == '_' || c == '-'
	}
	parts := strings.FieldsFunc(id, f)

	sep := "_"
	if strings.Contains(id, "-") && !strings.Contains(id, "_") {
		sep = "-"
	}

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
				// Non-last parts: just first char + separator
				if len(partRunes) > 0 {
					abbrev.WriteRune(partRunes[0])
					abbrev.WriteString(sep)
					runeCount += 1 + len(sep)
				}
			}
		}
		result := abbrev.String()
		return clamp(result)
	}

	// Fallback: simple truncation
	return clamp(string(runes))
}
