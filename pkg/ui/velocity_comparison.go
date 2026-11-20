package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// VelocityComparisonModel shows side-by-side velocity comparison for all labels
type VelocityComparisonModel struct {
	data         []velocityRow
	cursor       int
	width        int
	height       int
	scrollOffset int
	theme        Theme
}

// velocityRow holds computed velocity data for display
type velocityRow struct {
	Label        string
	Weeks        [4]int // W-4, W-3, W-2, W-1 (oldest to newest)
	Avg          float64
	Trend        string // "accelerating", "decelerating", "stable", "erratic", "insufficient_data"
	TrendSymbol  string // Visual indicator
	SparklineBar string // ASCII sparkline
	MaxWeekValue int    // For normalization
}

// NewVelocityComparisonModel creates a new velocity comparison view
func NewVelocityComparisonModel(theme Theme) VelocityComparisonModel {
	return VelocityComparisonModel{
		theme: theme,
	}
}

// SetData updates the view with computed velocity data using wall-clock UTC time.
func (m *VelocityComparisonModel) SetData(issues []model.Issue) {
	m.SetDataAt(issues, time.Now().UTC())
}

// SetDataAt updates the view with computed velocity data anchored to reference time now.
func (m *VelocityComparisonModel) SetDataAt(issues []model.Issue, now time.Time) {
	selectedLabel := ""
	if m.cursor >= 0 && m.cursor < len(m.data) {
		selectedLabel = m.data[m.cursor].Label
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	velocities := analysis.ComputeAllHistoricalVelocity(issues, 4, now)

	// Convert to rows and sort by average velocity (descending)
	m.data = make([]velocityRow, 0, len(velocities))
	for label, hv := range velocities {
		row := m.buildRow(label, hv)
		m.data = append(m.data, row)
	}

	// Sort by average velocity descending, then by label name
	sort.Slice(m.data, func(i, j int) bool {
		if m.data[i].Avg != m.data[j].Avg {
			return m.data[i].Avg > m.data[j].Avg
		}
		return m.data[i].Label < m.data[j].Label
	})

	// Keep both navigation coordinates valid when a refresh shrinks or clears
	// the label set. A stale scroll offset can otherwise put the entire new
	// dataset above the render window even after the cursor is reset.
	if len(m.data) == 0 {
		m.cursor = 0
		m.scrollOffset = 0
		return
	}
	if selectedLabel != "" {
		for i := range m.data {
			if m.data[i].Label == selectedLabel {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor < 0 || m.cursor >= len(m.data) {
		m.cursor = 0
	}
	maxScroll := max(0, len(m.data)-m.visibleRowCount())
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	m.ensureVisible()
}

// buildRow creates a velocityRow from HistoricalVelocity data
func (m *VelocityComparisonModel) buildRow(label string, hv analysis.HistoricalVelocity) velocityRow {
	row := velocityRow{
		Label: label,
		Trend: hv.GetVelocityTrend(),
		Avg:   hv.MovingAvg4Week,
	}

	// Extract last 4 weeks (WeeklyVelocity is ordered newest to oldest)
	for i := 0; i < 4 && i < len(hv.WeeklyVelocity); i++ {
		// Store in reverse order: index 0 = W-4 (oldest), index 3 = W-1 (newest)
		row.Weeks[3-i] = hv.WeeklyVelocity[i].Closed
		if hv.WeeklyVelocity[i].Closed > row.MaxWeekValue {
			row.MaxWeekValue = hv.WeeklyVelocity[i].Closed
		}
	}

	// Set trend symbol
	switch row.Trend {
	case "accelerating":
		row.TrendSymbol = "▲"
	case "decelerating":
		row.TrendSymbol = "▼"
	case "stable":
		row.TrendSymbol = "─"
	case "erratic":
		row.TrendSymbol = "~"
	default:
		row.TrendSymbol = "?"
	}

	// Build sparkline bar
	row.SparklineBar = buildSparkline(row.Weeks[:], row.MaxWeekValue)

	return row
}

// buildSparkline creates an ASCII sparkline from velocity values
func buildSparkline(values []int, maxVal int) string {
	if maxVal == 0 {
		return "    " // 4 spaces for empty sparkline
	}

	// Unicode block characters for 8-level height
	blocks := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	var sb strings.Builder
	for _, v := range values {
		// Normalize to 0-8 range
		level := (v * 8) / maxVal
		if level > 8 {
			level = 8
		}
		sb.WriteRune(blocks[level])
	}
	return sb.String()
}

// SetSize updates the view dimensions
func (m *VelocityComparisonModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.ensureVisible()
}

// MoveUp moves cursor up
func (m *VelocityComparisonModel) MoveUp() {
	if m.cursor > 0 {
		m.cursor--
		m.ensureVisible()
	}
}

// MoveDown moves cursor down
func (m *VelocityComparisonModel) MoveDown() {
	if m.cursor < len(m.data)-1 {
		m.cursor++
		m.ensureVisible()
	}
}

// MoveToTop jumps cursor to the first label row
func (m *VelocityComparisonModel) MoveToTop() {
	if len(m.data) > 0 {
		m.cursor = 0
		m.scrollOffset = 0
	}
}

// MoveToBottom jumps cursor to the last label row
func (m *VelocityComparisonModel) MoveToBottom() {
	if len(m.data) > 0 {
		m.cursor = len(m.data) - 1
		m.ensureVisible()
	}
}

// PageDown moves cursor down by half a page
func (m *VelocityComparisonModel) PageDown() {
	if len(m.data) > 0 {
		step := max(1, m.visibleRowCount()/2)
		m.cursor = min(len(m.data)-1, m.cursor+step)
		m.ensureVisible()
	}
}

// PageUp moves cursor up by half a page
func (m *VelocityComparisonModel) PageUp() {
	if len(m.data) > 0 {
		step := max(1, m.visibleRowCount()/2)
		m.cursor = max(0, m.cursor-step)
		m.ensureVisible()
	}
}

// ensureVisible adjusts scroll offset to keep cursor visible
func (m *VelocityComparisonModel) ensureVisible() {
	visibleRows := m.visibleRowCount()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	} else if m.cursor >= m.scrollOffset+visibleRows {
		m.scrollOffset = m.cursor - visibleRows + 1
	}
}

// visibleRowCount returns how many data rows can be displayed without clipping
func (m *VelocityComparisonModel) visibleRowCount() int {
	// Account for title + blank (2), header + separator (2), optional scroll indicator (1), and blank + footer (2) = 7 lines
	available := m.height - 7
	if available < 1 {
		return 1
	}
	return available
}

// SelectedLabel returns the currently selected label
func (m *VelocityComparisonModel) SelectedLabel() string {
	if len(m.data) == 0 || m.cursor >= len(m.data) {
		return ""
	}
	return m.data[m.cursor].Label
}

// View renders the velocity comparison table
func (m *VelocityComparisonModel) View() string {
	if m.width == 0 {
		m.width = 80
	}
	if m.height == 0 {
		m.height = 20
	}

	t := m.theme

	var sb strings.Builder

	// Title
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)
	sb.WriteString(titleStyle.Render("Velocity Comparison"))
	sb.WriteString("\n\n")

	// Table header
	headerStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Bold(true)

	// Column widths (scale label column down on narrow terminals so rows stay within m.width)
	weekWidth := 5
	avgWidth := 6
	trendWidth := 11
	sparkWidth := 6
	usedWidth := weekWidth*4 + avgWidth + trendWidth + sparkWidth + 10 // padding + cursor prefix
	labelWidth := 20
	if avail := m.width - usedWidth; avail > labelWidth {
		labelWidth = min(avail, 30)
	} else if avail < labelWidth {
		labelWidth = max(6, avail)
	}

	header := fmt.Sprintf("  %-*s %*s %*s %*s %*s %*s %-*s %s",
		labelWidth, "Label",
		weekWidth, "W-4",
		weekWidth, "W-3",
		weekWidth, "W-2",
		weekWidth, "W-1",
		avgWidth, "Avg",
		trendWidth, "Trend",
		"Spark",
	)
	if lipgloss.Width(header) > m.width {
		header = ansi.Truncate(header, m.width, "…")
	}
	sb.WriteString(headerStyle.Render(header))
	sb.WriteString("\n")

	// Separator
	sepStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	separator := strings.Repeat("─", max(min(lipgloss.Width(header)+2, m.width-2), 0))
	sb.WriteString(sepStyle.Render(separator))
	sb.WriteString("\n")

	// Data rows
	if len(m.data) == 0 {
		dimStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Italic(true)
		sb.WriteString(dimStyle.Render("  No velocity data available"))
		sb.WriteString("\n")
	} else {
		visibleRows := m.visibleRowCount()
		endIdx := m.scrollOffset + visibleRows
		if endIdx > len(m.data) {
			endIdx = len(m.data)
		}

		for i := m.scrollOffset; i < endIdx; i++ {
			row := m.data[i]
			isSelected := i == m.cursor

			// Row style using adaptive Theme colors (safe on both light and dark terminals)
			rowStyle := t.Renderer.NewStyle()
			if isSelected {
				rowStyle = rowStyle.
					Foreground(t.Primary).
					Bold(true).
					Background(t.Highlight)
			}

			// Truncate label safely by display width
			displayLabel := truncateRunesHelper(row.Label, labelWidth, "…")
			if w := lipgloss.Width(displayLabel); w < labelWidth {
				displayLabel += strings.Repeat(" ", labelWidth-w)
			}

			// Format trend with adaptive theme colors
			trendStyle := t.Renderer.NewStyle()
			switch row.Trend {
			case "accelerating":
				trendStyle = trendStyle.Foreground(t.Open)
			case "decelerating":
				trendStyle = trendStyle.Foreground(t.Blocked)
			case "stable":
				trendStyle = trendStyle.Foreground(t.Secondary)
			case "erratic":
				trendStyle = trendStyle.Foreground(t.InProgress)
			default:
				trendStyle = trendStyle.Foreground(t.Secondary)
			}

			// UTF-8 and display-width safe truncation (never slices multi-byte symbols like "─")
			trendText := fmt.Sprintf("%s %s", row.TrendSymbol, row.Trend)
			if lipgloss.Width(trendText) > trendWidth {
				trendText = ansi.Truncate(trendText, trendWidth, "…")
			}
			if w := lipgloss.Width(trendText); w < trendWidth {
				trendText += strings.Repeat(" ", trendWidth-w)
			}

			// Format sparkline with adaptive theme accent
			sparkStyle := t.Renderer.NewStyle().Foreground(t.Primary)

			// Build row string
			rowText := fmt.Sprintf("%s %*d %*d %*d %*d %*.1f ",
				displayLabel,
				weekWidth, row.Weeks[0],
				weekWidth, row.Weeks[1],
				weekWidth, row.Weeks[2],
				weekWidth, row.Weeks[3],
				avgWidth, row.Avg,
			)

			prefix := "  "
			if isSelected {
				prefix = "> "
			}

			line := rowStyle.Render(prefix+rowText) + trendStyle.Render(trendText) + " " + sparkStyle.Render(row.SparklineBar)
			if lipgloss.Width(line) > m.width {
				line = ansi.Truncate(line, m.width, "…")
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}

		// Show scroll indicator if needed
		if len(m.data) > visibleRows {
			scrollInfo := fmt.Sprintf("  [%d-%d of %d]", m.scrollOffset+1, endIdx, len(m.data))
			dimStyle := t.Renderer.NewStyle().
				Foreground(t.Secondary).
				Italic(true)
			sb.WriteString(dimStyle.Render(scrollInfo))
			sb.WriteString("\n")
		}
	}

	// Footer hints
	footerStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)
	sb.WriteString("\n")
	sb.WriteString(footerStyle.Render("j/k: navigate | enter: filter by label | esc: back"))

	return sb.String()
}

// DataCount returns the number of labels
func (m *VelocityComparisonModel) DataCount() int {
	return len(m.data)
}
