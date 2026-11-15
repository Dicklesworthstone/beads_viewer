package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
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

// SetData updates the view with computed velocity data
func (m *VelocityComparisonModel) SetData(issues []model.Issue) {
	now := time.Now().UTC()
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
	if m.cursor < 0 || m.cursor >= len(m.data) {
		m.cursor = 0
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

	// Column widths
	labelWidth := 20
	weekWidth := 5
	avgWidth := 6
	trendWidth := 10
	sparkWidth := 6

	// Adjust label width based on available space
	usedWidth := weekWidth*4 + avgWidth + trendWidth + sparkWidth + 10 // padding
	if m.width-usedWidth > labelWidth {
		labelWidth = min(m.width-usedWidth, 30)
	}

	header := fmt.Sprintf("%-*s %*s %*s %*s %*s %*s %-*s %s",
		labelWidth, "Label",
		weekWidth, "W-4",
		weekWidth, "W-3",
		weekWidth, "W-2",
		weekWidth, "W-1",
		avgWidth, "Avg",
		trendWidth, "Trend",
		"Spark",
	)
	sb.WriteString(headerStyle.Render(header))
	sb.WriteString("\n")

	// Separator
	sepStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	separator := strings.Repeat("─", max(min(len(header)+2, m.width-2), 0))
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

			// Row style
			rowStyle := t.Renderer.NewStyle()
			if isSelected {
				rowStyle = rowStyle.
					Foreground(t.Primary).
					Bold(true).
					Background(ThemeBg("#333"))
			}

			// Truncate label if needed (UTF-8 safe)
			displayLabel := truncateRunesHelper(row.Label, labelWidth, "…")

			// Format trend with color
			trendStyle := t.Renderer.NewStyle()
			switch row.Trend {
			case "accelerating":
				trendStyle = trendStyle.Foreground(ThemeFg("#00ff00"))
			case "decelerating":
				trendStyle = trendStyle.Foreground(ThemeFg("#ff6666"))
			case "stable":
				trendStyle = trendStyle.Foreground(t.Secondary)
			case "erratic":
				trendStyle = trendStyle.Foreground(ThemeFg("#ffaa00"))
			default:
				trendStyle = trendStyle.Foreground(t.Secondary)
			}

			trendText := fmt.Sprintf("%s %-8s", row.TrendSymbol, row.Trend)
			if len(trendText) > trendWidth {
				trendText = trendText[:trendWidth]
			}

			// Format sparkline with color gradient
			sparkStyle := t.Renderer.NewStyle().Foreground(ThemeFg("#88aaff"))

			// Build row string
			rowText := fmt.Sprintf("%-*s %*d %*d %*d %*d %*.1f ",
				labelWidth, displayLabel,
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

			sb.WriteString(rowStyle.Render(prefix + rowText))
			sb.WriteString(trendStyle.Render(trendText))
			sb.WriteString(" ")
			sb.WriteString(sparkStyle.Render(row.SparklineBar))
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
