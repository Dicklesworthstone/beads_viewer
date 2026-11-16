package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// LabelDashboardModel renders a lightweight table of label health
type LabelDashboardModel struct {
	labels       []analysis.LabelHealth
	cursor       int
	scrollOffset int // Index of the first visible row
	width        int
	height       int
	theme        Theme
}

func NewLabelDashboardModel(theme Theme) LabelDashboardModel {
	return LabelDashboardModel{theme: theme}
}

func (m *LabelDashboardModel) clampCursorAndScroll() {
	if len(m.labels) == 0 {
		m.cursor = 0
		m.scrollOffset = 0
		return
	}
	if m.cursor >= len(m.labels) {
		m.cursor = len(m.labels) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}

	if m.height <= 0 {
		if m.scrollOffset > m.cursor {
			m.scrollOffset = m.cursor
		}
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
		return
	}

	visibleRows := m.height - 1
	if visibleRows < 1 {
		visibleRows = 1
	}
	maxScroll := len(m.labels) - visibleRows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	} else if m.cursor >= m.scrollOffset+visibleRows {
		m.scrollOffset = m.cursor - visibleRows + 1
	}
}

func (m *LabelDashboardModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.clampCursorAndScroll()
}

func (m *LabelDashboardModel) SetData(labels []analysis.LabelHealth) {
	m.labels = labels
	// Sort by health level (critical first), then blocked desc, then health asc, then name
	sort.SliceStable(m.labels, func(i, j int) bool {
		li, lj := m.labels[i], m.labels[j]
		levelRank := func(l string) int {
			switch l {
			case analysis.HealthLevelCritical:
				return 0
			case analysis.HealthLevelWarning:
				return 1
			default:
				return 2
			}
		}
		ri, rj := levelRank(li.HealthLevel), levelRank(lj.HealthLevel)
		if ri != rj {
			return ri < rj
		}
		if li.Blocked != lj.Blocked {
			return li.Blocked > lj.Blocked
		}
		if li.Health != lj.Health {
			return li.Health < lj.Health
		}
		return li.Label < lj.Label
	})
	m.clampCursorAndScroll()
}

// MoveUp moves selection up by one label row.
func (m *LabelDashboardModel) MoveUp() {
	if m.cursor > 0 {
		m.cursor--
		if m.cursor < m.scrollOffset {
			m.scrollOffset = m.cursor
		}
	}
}

// MoveDown moves selection down by one label row.
func (m *LabelDashboardModel) MoveDown() {
	visibleRows := max(1, m.height-1)
	if m.cursor < len(m.labels)-1 {
		m.cursor++
		if m.cursor >= m.scrollOffset+visibleRows {
			m.scrollOffset = m.cursor - visibleRows + 1
		}
	}
}

// PageDown advances selection by half a visible page.
func (m *LabelDashboardModel) PageDown() {
	if len(m.labels) == 0 {
		return
	}
	visibleRows := max(1, m.height-1)
	step := max(1, visibleRows/2)
	m.cursor = min(len(m.labels)-1, m.cursor+step)
	if m.cursor >= m.scrollOffset+visibleRows {
		m.scrollOffset = m.cursor - visibleRows + 1
	}
}

// PageUp moves selection back by half a visible page.
func (m *LabelDashboardModel) PageUp() {
	if len(m.labels) == 0 {
		return
	}
	visibleRows := max(1, m.height-1)
	step := max(1, visibleRows/2)
	m.cursor = max(0, m.cursor-step)
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
}

// Update handles navigation keys; returns selected label on enter
func (m *LabelDashboardModel) Update(msg tea.KeyMsg) (string, tea.Cmd) {
	visibleRows := m.height - 1
	if visibleRows < 1 {
		visibleRows = 1
	}

	switch msg.String() {
	case "j", "down":
		m.MoveDown()
	case "k", "up":
		m.MoveUp()
	case "g", "home":
		m.cursor = 0
		m.scrollOffset = 0
	case "G", "end":
		if len(m.labels) > 0 {
			m.cursor = len(m.labels) - 1
			// Scroll to bottom
			if len(m.labels) > visibleRows {
				m.scrollOffset = len(m.labels) - visibleRows
			} else {
				m.scrollOffset = 0
			}
		}
	case "ctrl+d", "pgdown":
		m.PageDown()
	case "ctrl+u", "pgup":
		m.PageUp()
	case "enter":
		if m.cursor >= 0 && m.cursor < len(m.labels) {
			return m.labels[m.cursor].Label, nil
		}
	}
	return "", nil
}

func (m LabelDashboardModel) View() string {
	if len(m.labels) == 0 {
		return "No labels found"
	}

	headers := []string{"Label", "Health", "Blocked", "Velocity 7d/30d", "Stale"}
	widths := m.computeColumnWidths(headers)

	var b strings.Builder
	// Header
	headerLine := m.renderRow(headers, widths, true, false)
	b.WriteString(headerLine)
	b.WriteString("\n")

	visibleRows := m.height - 1
	if visibleRows < 1 {
		visibleRows = 1
	}

	start := m.scrollOffset
	if maxScroll := len(m.labels) - visibleRows; maxScroll >= 0 && start > maxScroll {
		start = maxScroll
	} else if maxScroll < 0 {
		start = 0
	}
	if start < 0 {
		start = 0
	}
	end := start + visibleRows
	if end > len(m.labels) {
		end = len(m.labels)
	}

	for i := start; i < end; i++ {
		lh := m.labels[i]
		row := m.getRowCells(lh)
		selected := i == m.cursor
		b.WriteString(m.renderRow(row, widths, false, selected))
		if i != end-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// getRowCells returns the fully rendered (colored) cells for a label row
func (m LabelDashboardModel) getRowCells(lh analysis.LabelHealth) []string {
	return []string{
		m.renderLabelCell(lh),
		m.renderHealthCell(lh),
		m.renderBlockedCell(lh),
		fmt.Sprintf("%d/%d", lh.Velocity.ClosedLast7Days, lh.Velocity.ClosedLast30Days),
		fmt.Sprintf("%d", lh.Freshness.StaleCount),
	}
}

func (m LabelDashboardModel) computeColumnWidths(headers []string) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, lh := range m.labels {
		cells := m.getRowCells(lh)
		for i, c := range cells {
			w := lipgloss.Width(c)
			if w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Ensure total fits width; if not, truncate label column first
	total := len(headers) - 1 // spaces between columns
	for _, w := range widths {
		total += w
	}
	if m.width > 0 && total > m.width {
		excess := total - m.width
		if excess >= widths[0]-4 {
			widths[0] = 4
		} else {
			widths[0] -= excess
		}
	}
	return widths
}

func (m LabelDashboardModel) renderRow(cells []string, widths []int, header bool, selected bool) string {
	var parts []string
	for i, cell := range cells {
		singleLine := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(cell)
		w := widths[i]
		if w > 0 && ansi.StringWidth(singleLine) > w {
			singleLine = ansi.Truncate(singleLine, w, "…")
		}
		style := lipgloss.NewStyle().Width(w).MaxWidth(w)
		parts = append(parts, style.Render(singleLine))
	}
	row := strings.Join(parts, " ")
	if header {
		return m.theme.Header.Render(row)
	}
	if selected {
		return m.theme.Selected.Render(row)
	}
	return m.theme.Base.Render(row)
}

func (m LabelDashboardModel) renderLabelCell(lh analysis.LabelHealth) string {
	indicator := ""
	if lh.HealthLevel == analysis.HealthLevelCritical {
		indicator = " !"
	} else if lh.Blocked > 0 {
		indicator = " ⛔"
	}
	return lh.Label + indicator
}

func (m LabelDashboardModel) renderHealthCell(lh analysis.LabelHealth) string {
	barWidth := 10
	filled := int(float64(barWidth) * float64(lh.Health) / 100.0)
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	filledStr := strings.Repeat("█", filled)
	blankStr := strings.Repeat("░", barWidth-filled)
	bar := filledStr + blankStr

	style := m.theme.Base
	switch lh.HealthLevel {
	case analysis.HealthLevelHealthy:
		style = style.Foreground(m.theme.Open)
	case analysis.HealthLevelWarning:
		style = style.Foreground(m.theme.Feature) // orange-ish
	default:
		style = style.Foreground(m.theme.Blocked)
	}

	return fmt.Sprintf("%3d %s", lh.Health, style.Render(bar))
}

func (m LabelDashboardModel) renderBlockedCell(lh analysis.LabelHealth) string {
	if lh.Blocked == 0 {
		return "0"
	}
	return m.theme.Base.Foreground(m.theme.Blocked).Bold(true).Render(fmt.Sprintf("%d", lh.Blocked))
}
