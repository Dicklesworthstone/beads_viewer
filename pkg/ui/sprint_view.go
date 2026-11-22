package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// renderSprintDashboard renders the sprint view with progress, burndown, and at-risk items (bv-161)
func (m *Model) renderSprintDashboard() string {
	t := m.theme
	if m.selectedSprint == nil {
		return "No sprint selected"
	}
	sprint := m.selectedSprint

	// Laid out in the main content width (not m.width) so the shortcuts
	// sidebar, when open, fits beside it (GH #209).
	bodyWidth := m.mainContentWidth()
	boxWidth := min(80, max(bodyWidth-4, 1))
	horizPad := 2
	if boxWidth <= 6 {
		horizPad = 0
	} else if boxWidth <= 10 {
		horizPad = 1
	}
	innerWidth := max(boxWidth-2*horizPad, 1)

	var sb strings.Builder

	// Title
	titleStyle := t.Renderer.NewStyle().Bold(true).Foreground(t.Primary)
	sb.WriteString(titleStyle.Render(fmt.Sprintf("📅 Sprint: %s", sprint.Name)))
	sb.WriteString("\n\n")

	// Date range and days remaining
	now := m.analysisReferenceTime()
	labelStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Bold(true)
	valStyle := t.Renderer.NewStyle().Foreground(t.Base.GetForeground())

	// Calculate days remaining
	var daysRemaining int
	var sprintDuration int
	var daysPassed int
	if !sprint.EndDate.IsZero() {
		daysRemaining = int(sprint.EndDate.Sub(now).Hours() / 24)
		if daysRemaining < 0 {
			daysRemaining = 0
		}
		if !sprint.StartDate.IsZero() {
			sprintDuration = int(sprint.EndDate.Sub(sprint.StartDate).Hours() / 24)
			daysPassed = int(now.Sub(sprint.StartDate).Hours() / 24)
			if daysPassed < 0 {
				daysPassed = 0
			}
			if daysPassed > sprintDuration {
				daysPassed = sprintDuration
			}
		}
	}

	// Sprint dates line
	dateInfo := fmt.Sprintf("%s → %s",
		sprint.StartDate.Format("Jan 2"),
		sprint.EndDate.Format("Jan 2"))
	sb.WriteString(labelStyle.Render("Dates:    "))
	sb.WriteString(valStyle.Render(dateInfo))
	sb.WriteString("\n")

	// Days remaining with visual indicator
	daysStyle := valStyle
	if daysRemaining <= 2 && daysRemaining > 0 {
		daysStyle = t.Renderer.NewStyle().Foreground(t.Feature) // Warning
	} else if daysRemaining == 0 {
		daysStyle = t.Renderer.NewStyle().Foreground(t.Blocked) // Critical
	}
	sb.WriteString(labelStyle.Render("Remaining:"))
	sb.WriteString(daysStyle.Render(fmt.Sprintf(" %d days", daysRemaining)))
	sb.WriteString("\n\n")

	// Compute bead stats (including dependency-blocked issues via ReadinessIndex)
	scopedIssues := m.candidateScopedIssues()
	var sprintReadiness *model.ReadinessIndex
	if m.analyzer != nil {
		sprintReadiness = m.analyzer.Readiness()
	}
	var totalBeads, closedBeads, openBeads, blockedBeads, inProgressBeads int
	var sprintIssues []model.Issue
	beadIDSet := make(map[string]bool)
	for _, id := range sprint.BeadIDs {
		beadIDSet[id] = true
	}
	for _, iss := range scopedIssues {
		if beadIDSet[iss.ID] {
			totalBeads++
			sprintIssues = append(sprintIssues, iss)
			if isClosedLikeStatus(iss.Status) {
				closedBeads++
				continue
			}
			isDepBlocked := sprintReadiness != nil && len(sprintReadiness.Blockers(iss.ID)) > 0
			switch {
			case iss.Status == model.StatusBlocked || isDepBlocked:
				blockedBeads++
			case iss.Status == model.StatusInProgress:
				inProgressBeads++
			}
			openBeads++
		}
	}

	// Progress bar
	sb.WriteString(labelStyle.Render("Progress: "))
	progressPct := 0.0
	if totalBeads > 0 {
		progressPct = float64(closedBeads) / float64(totalBeads)
	}
	barWidth := innerWidth - 20
	if barWidth < 10 {
		barWidth = 10
	}
	filled := int(float64(barWidth) * progressPct)
	if filled > barWidth {
		filled = barWidth
	}
	barStyle := t.Renderer.NewStyle().Foreground(t.Open)
	emptyStyle := t.Renderer.NewStyle().Foreground(t.Muted)
	sb.WriteString(barStyle.Render(strings.Repeat("█", filled)))
	sb.WriteString(emptyStyle.Render(strings.Repeat("░", barWidth-filled)))
	sb.WriteString(fmt.Sprintf(" %d/%d (%.0f%%)\n", closedBeads, totalBeads, progressPct*100))

	// Status breakdown
	sb.WriteString(labelStyle.Render("Status:   "))
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Open).Render(fmt.Sprintf("✓%d ", closedBeads)))
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Feature).Render(fmt.Sprintf("⏳%d ", inProgressBeads)))
	sb.WriteString(t.Renderer.NewStyle().Foreground(t.Blocked).Render(fmt.Sprintf("⛔%d ", blockedBeads)))
	sb.WriteString(valStyle.Render(fmt.Sprintf("○%d", openBeads-inProgressBeads-blockedBeads)))
	sb.WriteString("\n\n")

	// Real daily burndown chart (ASCII) powered by analysis.ComputeSprintBurndown
	// so TUI Sprint View and --robot-burndown share the exact same closure curve,
	// burn rates, and completion projection.
	burndown := analysis.ComputeSprintBurndown(sprint, sprintIssues, now)
	sb.WriteString(labelStyle.Render("Burndown:"))
	sb.WriteString("\n")
	if burndown.TotalDays > 0 && totalBeads > 0 {
		chartHeight := 5
		chartWidth := min(max(burndown.TotalDays-1, 1), 24)
		rowForRemaining := func(rem float64) int {
			if totalBeads <= 0 {
				return 0
			}
			frac := rem / float64(totalBeads)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			r := int(frac*float64(chartHeight-1) + 0.5)
			if r < 0 {
				return 0
			}
			if r >= chartHeight {
				return chartHeight - 1
			}
			return r
		}

		// Precompute per-column ideal and actual remaining rows.
		idealRows := make([]int, chartWidth+1)
		actualRows := make([]int, chartWidth+1)
		for col := 0; col <= chartWidth; col++ {
			dayFrac := float64(col) / float64(chartWidth)
			idealVal := float64(totalBeads) * (1.0 - dayFrac)
			idealRows[col] = rowForRemaining(idealVal)
			actualRows[col] = -1
			if len(burndown.DailyPoints) > 0 {
				dayIdx := int(dayFrac*float64(max(burndown.TotalDays-1, 0)) + 0.5)
				if dayIdx < len(burndown.DailyPoints) {
					actualRows[col] = rowForRemaining(float64(burndown.DailyPoints[dayIdx].Remaining))
				}
			}
		}

		for row := chartHeight - 1; row >= 0; row-- {
			var line strings.Builder
			line.WriteString("  ")
			for col := 0; col <= chartWidth; col++ {
				hasActual := actualRows[col] == row
				hasIdeal := idealRows[col] == row
				if hasActual {
					line.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("●"))
				} else if hasIdeal {
					line.WriteString(t.Renderer.NewStyle().Foreground(t.Secondary).Render("·"))
				} else {
					line.WriteString(" ")
				}
			}
			sb.WriteString(line.String())
			sb.WriteString("\n")
		}
		sb.WriteString("  ")
		sb.WriteString(strings.Repeat("─", chartWidth+1))
		sb.WriteString("\n")
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Muted).Italic(true).Render("  · ideal  ● actual"))
		sb.WriteString("\n")

		// Burn rate & completion projection summary
		statusLabel := t.Renderer.NewStyle().Foreground(t.Open).Bold(true).Render("On track")
		if !burndown.OnTrack {
			statusLabel = t.Renderer.NewStyle().Foreground(t.Blocked).Bold(true).Render("Behind")
		}
		projText := "Complete!"
		if burndown.RemainingIssues > 0 {
			if burndown.ProjectedComplete != nil {
				projText = "Projected " + burndown.ProjectedComplete.Format("Jan 2")
			} else {
				projText = "No closures yet"
			}
		}
		sb.WriteString(labelStyle.Render("Velocity: "))
		sb.WriteString(valStyle.Render(fmt.Sprintf("%.2f/day (ideal %.2f/day) • ", burndown.ActualBurnRate, burndown.IdealBurnRate)))
		sb.WriteString(statusLabel)
		sb.WriteString(valStyle.Render(" • " + projText))
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(valStyle.Render("  (insufficient data)"))
		sb.WriteString("\n\n")
	}

	// At-risk items: the same four signals and .bv/drift.yaml thresholds
	// --robot-burndown reports (analysis.DetectAtRisk), so the TUI and robot agree.
	// Pass full m.issues with a candidate-scoped sprint copy so out-of-scope open
	// blockers and parent epics resolve in DetectAtRisk's issueMap (matching --robot-burndown).
	sb.WriteString(labelStyle.Render("At Risk:"))
	sb.WriteString("\n")
	scopedSprint := *sprint
	if len(scopedIssues) != len(m.issues) {
		scopedSet := make(map[string]bool, len(scopedIssues))
		for _, iss := range scopedIssues {
			scopedSet[iss.ID] = true
		}
		scopedBeadIDs := make([]string, 0, len(sprint.BeadIDs))
		for _, id := range sprint.BeadIDs {
			if scopedSet[id] {
				scopedBeadIDs = append(scopedBeadIDs, id)
			}
		}
		scopedSprint.BeadIDs = scopedBeadIDs
	}
	atRisk := analysis.DetectAtRiskWithReadiness(m.issues, &scopedSprint, sprintReadiness, now, m.sprintAtRiskThresholds)
	if len(atRisk) == 0 {
		sb.WriteString(t.Renderer.NewStyle().Foreground(t.Open).Render("  ✓ No at-risk items"))
		sb.WriteString("\n")
	} else {
		for i, item := range atRisk {
			if i >= 5 {
				sb.WriteString(valStyle.Render(fmt.Sprintf("  … +%d more", len(atRisk)-5)))
				sb.WriteString("\n")
				break
			}
			signals := make([]string, 0, len(item.Signals))
			for _, s := range item.Signals {
				signals = append(signals, string(s))
			}
			sb.WriteString(t.Renderer.NewStyle().Foreground(t.Feature).Render(
				fmt.Sprintf("  ⚠ %s - %s (%s)\n", item.ID, truncateStrSprint(item.Title, 30), strings.Join(signals, ", "))))
		}
	}
	sb.WriteString("\n")

	// Sprint beads list with selectable bead cursor (Tab/Shift+Tab or n/p)
	sb.WriteString(labelStyle.Render("Beads in Sprint:"))
	sb.WriteString("\n")
	if len(sprintIssues) == 0 {
		m.sprintBeadCursor = 0
		sb.WriteString(valStyle.Render("  (none)"))
		sb.WriteString("\n")
	} else {
		if m.sprintBeadCursor < 0 {
			m.sprintBeadCursor = 0
		}
		if m.sprintBeadCursor >= len(sprintIssues) {
			m.sprintBeadCursor = len(sprintIssues) - 1
		}
		const maxVisibleBeads = 10
		startIdx := 0
		if m.sprintBeadCursor >= maxVisibleBeads {
			startIdx = m.sprintBeadCursor - maxVisibleBeads + 1
		}
		endIdx := min(startIdx+maxVisibleBeads, len(sprintIssues))
		if startIdx > 0 {
			sb.WriteString(valStyle.Render(fmt.Sprintf("  … +%d above", startIdx)))
			sb.WriteString("\n")
		}
		for i := startIdx; i < endIdx; i++ {
			iss := sprintIssues[i]
			statusIcon := "○"
			statusStyle := valStyle
			if isClosedLikeStatus(iss.Status) {
				statusIcon = "✓"
				statusStyle = t.Renderer.NewStyle().Foreground(t.Open)
			} else {
				isDepBlocked := sprintReadiness != nil && len(sprintReadiness.Blockers(iss.ID)) > 0
				switch {
				case iss.Status == model.StatusBlocked || isDepBlocked:
					statusIcon = "⛔"
					statusStyle = t.Renderer.NewStyle().Foreground(t.Blocked)
				case iss.Status == model.StatusInProgress:
					statusIcon = "⏳"
					statusStyle = t.Renderer.NewStyle().Foreground(t.Feature)
				}
			}
			prefix := "  "
			if i == m.sprintBeadCursor {
				prefix = "▸ "
				statusStyle = statusStyle.Bold(true).Foreground(t.Primary)
			}
			sb.WriteString(statusStyle.Render(fmt.Sprintf("%s%s %s - %s\n", prefix, statusIcon, iss.ID, truncateStrSprint(iss.Title, 40))))
		}
		if endIdx < len(sprintIssues) {
			sb.WriteString(valStyle.Render(fmt.Sprintf("  … +%d more", len(sprintIssues)-endIdx)))
			sb.WriteString("\n")
		}
	}

	// Window inner content for short/narrow terminals while preserving footer + borders.
	vertPad := 1
	if m.height > 0 && m.height <= 26 {
		vertPad = 0
	}
	placeHeight := max(m.height-1, 1)
	maxBoxH := max(m.height-2, 1)
	if maxBoxH > placeHeight {
		maxBoxH = placeHeight
	}
	clampLine := func(line string) string {
		if lipgloss.Width(line) > innerWidth {
			suffix := "…"
			if innerWidth <= 2 {
				suffix = ""
			}
			return ansi.Truncate(line, innerWidth, suffix)
		}
		return line
	}
	footerLine := clampLine(t.Renderer.NewStyle().Foreground(t.Muted).Italic(true).Render(
		"P/esc: close • j/k: sprint • tab/n/p: bead • ctrl+d/u: scroll • Enter: jump"))
	rawBodyLines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	bodyLines := make([]string, len(rawBodyLines))
	for i, line := range rawBodyLines {
		bodyLines[i] = clampLine(line)
	}
	// Box chrome: border (2) + vertical padding (2*vertPad) + footer (1) + optional spacer (1).
	availInner := max(maxBoxH-2-2*vertPad, 1)
	footerGap := 1
	if availInner <= 2 {
		footerGap = 0
	}
	maxBodyLines := max(availInner-1-footerGap, 1)
	if len(bodyLines) > maxBodyLines {
		maxScroll := len(bodyLines) - maxBodyLines
		if m.sprintScroll > maxScroll {
			m.sprintScroll = maxScroll
		}
		if m.sprintScroll < 0 {
			m.sprintScroll = 0
		}
		end := min(m.sprintScroll+maxBodyLines, len(bodyLines))
		bodyLines = bodyLines[m.sprintScroll:end]
	} else {
		m.sprintScroll = 0
	}
	sep := "\n\n"
	if footerGap == 0 {
		sep = "\n"
	}
	finalContent := strings.Join(bodyLines, "\n") + sep + footerLine

	// Wrap in a box
	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(vertPad, horizPad).
		Width(boxWidth).
		MaxHeight(maxBoxH)

	return lipgloss.Place(
		max(bodyWidth, 1),
		placeHeight,
		lipgloss.Center,
		lipgloss.Top,
		boxStyle.Render(finalContent),
	)
}

// truncateStrSprint truncates a string to maxLen runes, adding ellipsis if needed.
// Uses rune-based counting to safely handle UTF-8 multi-byte characters.
func truncateStrSprint(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

// noSprintsStatus is shown when P is pressed but the beads directory has no
// sprints.jsonl (or it is empty).
const noSprintsStatus = "No sprints defined (.beads/sprints.jsonl)"

// activeSprintIndex picks the sprint to show first: the one whose window
// contains now (latest start wins on overlap), otherwise the most recent by
// end date (then start date), otherwise the last one in file order.
func activeSprintIndex(sprints []model.Sprint, now time.Time) int {
	if len(sprints) == 0 {
		return -1
	}
	best := -1
	for i, s := range sprints {
		if s.StartDate.IsZero() || s.EndDate.IsZero() {
			continue
		}
		if now.Before(s.StartDate) || now.After(s.EndDate) {
			continue
		}
		if best == -1 || s.StartDate.After(sprints[best].StartDate) {
			best = i
		}
	}
	if best != -1 {
		return best
	}
	// No active window: most recent sprint.
	best = len(sprints) - 1
	for i, s := range sprints {
		if i == best {
			continue
		}
		cur := sprints[best]
		switch {
		case s.EndDate.After(cur.EndDate):
			best = i
		case s.EndDate.Equal(cur.EndDate) && s.StartDate.After(cur.StartDate):
			best = i
		}
	}
	return best
}

// openSprintView switches to the sprint dashboard for the active sprint
// (bv-161). With no sprints loaded it only reports a status message and
// leaves the current view untouched.
func (m *Model) openSprintView() (*Model, tea.Cmd) {
	idx := activeSprintIndex(m.sprints, m.analysisReferenceTime())
	if idx < 0 {
		m.statusMsg = noSprintsStatus
		m.statusIsError = false
		return m, nil
	}
	m.isGraphView = false
	m.isBoardView = false
	m.isActionableView = false
	m.isHistoryView = false
	if !m.isSplitView {
		// The dashboard replaces the detail pane; closing it lands on the list.
		m.showDetails = false
	}
	m.selectedSprint = &m.sprints[idx]
	m.sprintScroll = 0
	m.sprintBeadCursor = 0
	m.isSprintView = true
	m.focused = focusSprint
	m.sprintViewText = m.renderSprintDashboard()
	return m, nil
}

// sprintScopedIssues returns the currently visible sprint issues in displayed order.
func (m *Model) sprintScopedIssues() []model.Issue {
	if m.selectedSprint == nil {
		return nil
	}
	beadIDSet := make(map[string]bool, len(m.selectedSprint.BeadIDs))
	for _, id := range m.selectedSprint.BeadIDs {
		beadIDSet[id] = true
	}
	var out []model.Issue
	for _, iss := range m.candidateScopedIssues() {
		if beadIDSet[iss.ID] {
			out = append(out, iss)
		}
	}
	return out
}

// handleSprintKeys handles keyboard input when in sprint view (bv-161)
func (m *Model) handleSprintKeys(msg tea.KeyMsg) *Model {
	switch msg.String() {
	case "P", "esc":
		// Exit sprint view
		m.isSprintView = false
		m.focused = focusList
	case "j", "down":
		// Next sprint
		if len(m.sprints) > 1 && m.selectedSprint != nil {
			for i, s := range m.sprints {
				if s.ID == m.selectedSprint.ID && i < len(m.sprints)-1 {
					m.selectedSprint = &m.sprints[i+1]
					m.sprintScroll = 0
					m.sprintBeadCursor = 0
					m.sprintViewText = m.renderSprintDashboard()
					break
				}
			}
		}
	case "k", "up":
		// Previous sprint
		if len(m.sprints) > 1 && m.selectedSprint != nil {
			for i, s := range m.sprints {
				if s.ID == m.selectedSprint.ID && i > 0 {
					m.selectedSprint = &m.sprints[i-1]
					m.sprintScroll = 0
					m.sprintBeadCursor = 0
					m.sprintViewText = m.renderSprintDashboard()
					break
				}
			}
		}
	case "tab", "n":
		// Next bead in sprint
		sprintIssues := m.sprintScopedIssues()
		if len(sprintIssues) > 0 {
			m.sprintBeadCursor = (m.sprintBeadCursor + 1) % len(sprintIssues)
			m.sprintViewText = m.renderSprintDashboard()
		}
	case "shift+tab", "p":
		// Previous bead in sprint
		sprintIssues := m.sprintScopedIssues()
		if len(sprintIssues) > 0 {
			m.sprintBeadCursor = (m.sprintBeadCursor - 1 + len(sprintIssues)) % len(sprintIssues)
			m.sprintViewText = m.renderSprintDashboard()
		}
	case "ctrl+d", "pgdown":
		m.sprintScroll += 5
		m.sprintViewText = m.renderSprintDashboard()
	case "ctrl+u", "pgup":
		m.sprintScroll = max(0, m.sprintScroll-5)
		m.sprintViewText = m.renderSprintDashboard()
	case "enter":
		if m.selectedSprint == nil {
			return m
		}
		// Match displayed sprintIssues order and jump to the highlighted bead cursor
		sprintIssues := m.sprintScopedIssues()
		if len(sprintIssues) == 0 {
			return m
		}
		idx := m.sprintBeadCursor
		if idx < 0 || idx >= len(sprintIssues) {
			idx = 0
		}
		targetID := sprintIssues[idx].ID
		if targetID == "" {
			return m
		}
		m.revealRecipeIssue(targetID)
		if !m.selectListIssueByID(targetID) {
			return m
		}
		m.isSprintView = false
		m.focused = focusList
		if m.isSplitView {
			m.focused = focusDetail
		} else {
			m.showDetails = true
			m.focused = focusDetail
			m.viewport.GotoTop()
		}
		m.updateViewportContent()
	}
	return m
}
