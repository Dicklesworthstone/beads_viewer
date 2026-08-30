package ui

import (
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LabelPickerModel provides a fuzzy search popup for quick filtering by an
// arbitrary set of string values (labels, assignees, issue types, ...).
type LabelPickerModel struct {
	allLabels     []string
	labelCounts   map[string]int // count of issues per label
	filtered      []string
	input         textinput.Model
	selectedIndex int
	width         int
	height        int
	theme         Theme
	title         string // overlay title; defaults to "Filter by Label" when empty
}

// NewLabelPickerModel creates a new label picker with fuzzy search
// labels should be pre-sorted by count descending (from LabelExtractionResult.TopLabels)
func NewLabelPickerModel(labels []string, counts map[string]int, theme Theme) LabelPickerModel {
	// Sort labels by count descending
	sorted := sortLabelsByCountDesc(labels, counts)

	ti := textinput.New()
	ti.Placeholder = "type to filter..."
	ti.CharLimit = 50
	ti.Width = 30

	return LabelPickerModel{
		allLabels:     sorted,
		labelCounts:   counts,
		filtered:      sorted,
		input:         ti,
		selectedIndex: 0,
		theme:         theme,
	}
}

// NewPickerModel creates a fuzzy-search picker over an arbitrary set of
// values with a custom overlay title (e.g. "Filter by Assignee").
func NewPickerModel(values []string, counts map[string]int, theme Theme, title string) LabelPickerModel {
	m := NewLabelPickerModel(values, counts, theme)
	m.title = title
	return m
}

// Focus activates the picker input and returns its initial cursor command.
func (m *LabelPickerModel) Focus() tea.Cmd {
	return m.input.Focus()
}

// Blur deactivates the picker input.
func (m *LabelPickerModel) Blur() {
	m.input.Blur()
}

// sortLabelsByCountDesc sorts labels by count descending, then alphabetically for ties
func sortLabelsByCountDesc(labels []string, counts map[string]int) []string {
	sorted := make([]string, len(labels))
	copy(sorted, labels)
	sort.Slice(sorted, func(i, j int) bool {
		ci := counts[sorted[i]]
		cj := counts[sorted[j]]
		if ci != cj {
			return ci > cj // descending by count
		}
		return sorted[i] < sorted[j] // alphabetically for ties
	})
	return sorted
}

// SetSize updates the picker dimensions
func (m *LabelPickerModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// SetLabels updates the available labels with their counts
func (m *LabelPickerModel) SetLabels(labels []string, counts map[string]int) {
	m.labelCounts = counts
	m.allLabels = sortLabelsByCountDesc(labels, counts)
	m.filterLabels()
}

// MoveUp moves selection up
func (m *LabelPickerModel) MoveUp() {
	if m.selectedIndex > 0 {
		m.selectedIndex--
	}
}

// MoveDown moves selection down
func (m *LabelPickerModel) MoveDown() {
	if m.selectedIndex < len(m.filtered)-1 {
		m.selectedIndex++
	}
}

// SelectedLabel returns the currently selected label
func (m *LabelPickerModel) SelectedLabel() string {
	if len(m.filtered) == 0 || m.selectedIndex >= len(m.filtered) {
		return ""
	}
	return m.filtered[m.selectedIndex]
}

// UpdateInput processes a key message for the text input and returns any
// follow-up command (for example, an asynchronous clipboard paste).
func (m *LabelPickerModel) UpdateInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.filterLabels()
	return cmd
}

// Reset clears the input and resets selection
func (m *LabelPickerModel) Reset() {
	m.input.SetValue("")
	m.filterLabels()
}

// filterLabels filters the labels based on current input using fuzzy matching
func (m *LabelPickerModel) filterLabels() {
	query := strings.ToLower(strings.TrimSpace(m.input.Value()))
	if query == "" {
		m.filtered = m.allLabels
		m.selectedIndex = 0
		return
	}

	type scored struct {
		label string
		score int
	}

	var matches []scored
	for _, label := range m.allLabels {
		if score := fuzzyScore(label, query); score > 0 {
			matches = append(matches, scored{label, score})
		}
	}

	// Sort by score (higher is better), then alphabetically
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].label < matches[j].label
	})

	m.filtered = make([]string, len(matches))
	for i, match := range matches {
		m.filtered[i] = match.label
	}

	// Keep selection in bounds
	if m.selectedIndex >= len(m.filtered) {
		m.selectedIndex = len(m.filtered) - 1
	}
	if m.selectedIndex < 0 {
		m.selectedIndex = 0
	}
}

// fuzzyScore returns a score for how well query matches label (0 = no match)
// Uses fzf-style scoring: consecutive matches, word boundary bonuses
func fuzzyScore(label, query string) int {
	label = strings.ToLower(label)
	query = strings.ToLower(query)

	// Exact match gets highest score
	if label == query {
		return 1000
	}

	// Prefix match gets high score
	if strings.HasPrefix(label, query) {
		return 500 + len(query)
	}

	// Contains match
	if strings.Contains(label, query) {
		return 200 + len(query)
	}

	// Fuzzy subsequence match
	li, qi := 0, 0
	score := 0
	consecutive := 0
	lastMatchIdx := -1

	for li < len(label) && qi < len(query) {
		if label[li] == query[qi] {
			qi++
			matchScore := 10

			// Bonus for consecutive matches
			if lastMatchIdx == li-1 {
				consecutive++
				matchScore += consecutive * 5
			} else {
				consecutive = 0
			}

			// Bonus for word boundary match
			if li == 0 || !unicode.IsLetter(rune(label[li-1])) {
				matchScore += 15
			}

			score += matchScore
			lastMatchIdx = li
		}
		li++
	}

	// Only count as match if all query chars were found
	if qi == len(query) {
		return score
	}
	return 0
}

// View renders the label picker overlay
func (m *LabelPickerModel) View() string {
	if m.width == 0 {
		m.width = 60
	}
	if m.height == 0 {
		m.height = 20
	}

	t := m.theme

	// Calculate box dimensions
	boxWidth := 40
	if m.width < 50 {
		boxWidth = m.width - 10
	}
	if boxWidth < 25 {
		boxWidth = 25
	}

	maxVisible := 10
	if m.height < 15 {
		maxVisible = m.height - 7
	}
	if maxVisible < 3 {
		maxVisible = 3
	}

	var lines []string

	// Title
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		MarginBottom(1)
	title := m.title
	if title == "" {
		title = "Filter by Label"
	}
	lines = append(lines, titleStyle.Render(title))
	lines = append(lines, "")

	// Search input
	inputStyle := t.Renderer.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(t.Secondary).
		Padding(0, 1).
		Width(boxWidth - 6)
	lines = append(lines, inputStyle.Render(m.input.View()))
	lines = append(lines, "")

	// Label list with scroll
	if len(m.filtered) == 0 {
		dimStyle := t.Renderer.NewStyle().
			Foreground(t.Secondary).
			Italic(true)
		lines = append(lines, dimStyle.Render("  No matching labels"))
	} else {
		// Calculate visible window
		start := 0
		if m.selectedIndex >= maxVisible {
			start = m.selectedIndex - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(m.filtered) {
			end = len(m.filtered)
		}

		for i := start; i < end; i++ {
			label := m.filtered[i]
			isSelected := i == m.selectedIndex

			itemStyle := t.Renderer.NewStyle()
			countStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
			if isSelected {
				itemStyle = itemStyle.Foreground(t.Primary).Bold(true)
				countStyle = countStyle.Foreground(t.Primary)
			} else {
				itemStyle = itemStyle.Foreground(t.Base.GetForeground())
			}

			prefix := "  "
			if isSelected {
				prefix = "> "
			}

			// Format label with count in parentheses
			count := m.labelCounts[label]
			countStr := " (" + itoa(count) + ")"
			// Reserve space for count when truncating label
			maxLabelLen := boxWidth - 8 - len(countStr)
			if maxLabelLen < 10 {
				maxLabelLen = 10
			}
			displayLabel := truncateRunesHelper(label, maxLabelLen, "...")
			lines = append(lines, itemStyle.Render(prefix+displayLabel)+countStyle.Render(countStr))
		}

		// Show count if scrolling
		if len(m.filtered) > maxVisible {
			countStyle := t.Renderer.NewStyle().
				Foreground(t.Secondary).
				Italic(true)
			lines = append(lines, "")
			lines = append(lines, countStyle.Render(
				"  "+strings.Repeat(" ", boxWidth/2-10)+
					"("+itoa(m.selectedIndex+1)+"/"+itoa(len(m.filtered))+")",
			))
		}
	}

	// Footer with keybindings
	lines = append(lines, "")
	footerStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)
	lines = append(lines, footerStyle.Render("j/k: navigate | enter: apply | esc: cancel"))

	content := strings.Join(lines, "\n")

	// Box style
	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Width(boxWidth)

	box := boxStyle.Render(content)

	// Center in viewport
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		box,
	)
}

// InputValue returns the current input value
func (m *LabelPickerModel) InputValue() string {
	return m.input.Value()
}

// FilteredCount returns the number of filtered labels
func (m *LabelPickerModel) FilteredCount() int {
	return len(m.filtered)
}

// itoa is a simple int to string helper
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
