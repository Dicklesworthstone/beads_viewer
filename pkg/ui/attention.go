package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	tea "github.com/charmbracelet/bubbletea"
)

// attentionRowLimit caps the table at the top ten labels; keys 1-9 jump to a
// rank directly and the cursor reaches the tenth.
const attentionRowLimit = 10

// AttentionModel is the navigable label-attention view opened with `]`
// (bv-117). It mirrors LabelDashboardModel: a cursor over the ranked labels,
// j/k/g/G navigation, and Enter reporting the selected label so the parent
// can open the label drilldown.
type AttentionModel struct {
	labels       []analysis.LabelAttentionScore
	cursor       int
	scrollOffset int
	width        int
	height       int
	theme        Theme
}

// NewAttentionModel creates an empty attention view.
func NewAttentionModel(theme Theme) AttentionModel {
	return AttentionModel{theme: theme}
}

// SetData replaces the ranked labels (already sorted by attention, highest
// first, by analysis.ComputeLabelAttentionScores) and clamps the cursor.
func (m *AttentionModel) SetData(result analysis.LabelAttentionResult) {
	labels := result.Labels
	if len(labels) > attentionRowLimit {
		labels = labels[:attentionRowLimit]
	}
	m.labels = append([]analysis.LabelAttentionScore(nil), labels...)
	if m.cursor >= len(m.labels) {
		m.cursor = len(m.labels) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.scrollOffset = 0
	m.ensureCursorVisible()
}

// SetSize records the available area; height includes the header row.
func (m *AttentionModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.ensureCursorVisible()
}

// Len returns the number of ranked labels shown.
func (m AttentionModel) Len() int {
	return len(m.labels)
}

// Cursor returns the selected row index.
func (m AttentionModel) Cursor() int {
	return m.cursor
}

// SelectedLabel returns the label under the cursor, or "" when empty.
func (m AttentionModel) SelectedLabel() string {
	return m.LabelAt(m.cursor)
}

// LabelAt returns the label at a rank index (0-based), or "" if out of range.
func (m AttentionModel) LabelAt(idx int) string {
	if idx < 0 || idx >= len(m.labels) {
		return ""
	}
	return m.labels[idx].Label
}

func (m *AttentionModel) visibleRows() int {
	rows := m.height - 1 // header
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (m *AttentionModel) ensureCursorVisible() {
	rows := m.visibleRows()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+rows {
		m.scrollOffset = m.cursor - rows + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// MoveUp moves the cursor to the previous label.
func (m *AttentionModel) MoveUp() {
	if m.cursor > 0 {
		m.cursor--
		m.ensureCursorVisible()
	}
}

// MoveDown moves the cursor to the next label.
func (m *AttentionModel) MoveDown() {
	if m.cursor < len(m.labels)-1 {
		m.cursor++
		m.ensureCursorVisible()
	}
}

// MoveToTop jumps to rank 1.
func (m *AttentionModel) MoveToTop() {
	m.cursor = 0
	m.ensureCursorVisible()
}

// MoveToBottom jumps to the last ranked label.
func (m *AttentionModel) MoveToBottom() {
	if len(m.labels) > 0 {
		m.cursor = len(m.labels) - 1
		m.ensureCursorVisible()
	}
}

// Update handles navigation keys. It returns the selected label on Enter and
// whether the key was consumed; exit keys (]/Esc/q) and the 1-9 quick filters
// are the parent's responsibility.
func (m *AttentionModel) Update(msg tea.KeyMsg) (string, bool) {
	switch msg.String() {
	case "j", "down":
		m.MoveDown()
	case "k", "up":
		m.MoveUp()
	case "g", "home":
		m.MoveToTop()
	case "G", "end":
		m.MoveToBottom()
	case "enter":
		return m.SelectedLabel(), true
	default:
		return "", false
	}
	return "", true
}

// attentionColumnWidths returns the fixed table geometry for a given width:
// rank, label, attention, reason. The reason column absorbs the remainder
// and never drops below 20 cells.
func attentionColumnWidths(width int) []int {
	headers := 4
	sepWidth := len(" | ") * (headers - 1)
	widths := []int{4, 18, 10, width - 4 - 18 - 10 - sepWidth}
	if widths[3] < 20 {
		widths[3] = 20
	}
	return widths
}

// attentionReason renders the real formula components behind a score:
// Attention = PageRankSum × StalenessFactor × (1 + BlockImpact) / VelocityFactor
// where VelocityFactor is the number of issues closed in the last 30 days + 1.
func attentionReason(s analysis.LabelAttentionScore) string {
	closed30 := int(s.VelocityFactor) - 1
	if closed30 < 0 {
		closed30 = 0
	}
	return fmt.Sprintf("pr=%.2f stale=%.2f block=%.0f closed30=%d", s.PageRankSum, s.StalenessFactor, s.BlockImpact, closed30)
}

// View renders the header plus the visible ranked rows; the cursor row is
// drawn with the theme's Selected style.
func (m AttentionModel) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}
	colWidths := attentionColumnWidths(width)
	selectedWidth := width - m.theme.Selected.GetHorizontalFrameSize()
	if selectedWidth < 40 {
		selectedWidth = 40
	}
	selectedColWidths := attentionColumnWidths(selectedWidth)

	renderRowWithWidths := func(widths []int, cells []string) string {
		parts := make([]string, 0, len(cells))
		for i, c := range cells {
			c = truncate(c, widths[i])
			parts = append(parts, padRight(c, widths[i]))
		}
		return strings.Join(parts, " | ")
	}

	var b strings.Builder
	b.WriteString(renderRowWithWidths(colWidths, []string{"Rank", "Label", "Attention", "Reason"}))
	b.WriteString("\n")

	rows := m.height - 1
	if rows < 1 {
		rows = 1
	}
	start := m.scrollOffset
	if start > len(m.labels) {
		start = len(m.labels)
	}
	end := start + rows
	if end > len(m.labels) {
		end = len(m.labels)
	}
	for i := start; i < end; i++ {
		s := m.labels[i]
		rowWidths := colWidths
		if i == m.cursor {
			rowWidths = selectedColWidths
		}
		line := renderRowWithWidths(rowWidths, []string{
			fmt.Sprintf("%d", i+1),
			s.Label,
			fmt.Sprintf("%.2f", s.AttentionScore),
			attentionReason(s),
		})
		if i == m.cursor {
			line = m.theme.Selected.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
