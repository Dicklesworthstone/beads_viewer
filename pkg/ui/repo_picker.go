package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RepoPickerModel represents the repository filter picker overlay (workspace mode).
type RepoPickerModel struct {
	repos         []string
	selectedIndex int
	selected      map[string]bool // repo -> selected
	width         int
	height        int
	theme         Theme
}

// NewRepoPickerModel creates a new repo picker. By default, all repos are selected.
func NewRepoPickerModel(repos []string, theme Theme) RepoPickerModel {
	m := RepoPickerModel{
		repos:         append([]string(nil), repos...),
		selectedIndex: 0,
		selected:      make(map[string]bool, len(repos)),
		theme:         theme,
	}
	for _, r := range m.repos {
		m.selected[r] = true
	}
	return m
}

// SetSize updates the picker dimensions.
func (m *RepoPickerModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// SetActiveRepos initializes selection from the currently active repo filter (nil = all).
func (m *RepoPickerModel) SetActiveRepos(active map[string]bool) {
	if len(m.repos) == 0 {
		m.selected = map[string]bool{}
		return
	}

	m.selected = make(map[string]bool, len(m.repos))
	if active == nil {
		for _, r := range m.repos {
			m.selected[r] = true
		}
		return
	}

	for _, r := range m.repos {
		if active[r] {
			m.selected[r] = true
		}
	}
}

// MoveUp moves selection up.
func (m *RepoPickerModel) MoveUp() {
	if m.selectedIndex > 0 {
		m.selectedIndex--
	}
}

// MoveDown moves selection down.
func (m *RepoPickerModel) MoveDown() {
	if m.selectedIndex < len(m.repos)-1 {
		m.selectedIndex++
	}
}

// ToggleSelected toggles the selected state of the current repo.
func (m *RepoPickerModel) ToggleSelected() {
	if len(m.repos) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.repos) {
		return
	}
	r := m.repos[m.selectedIndex]
	m.selected[r] = !m.selected[r]
}

// SelectAll selects all repos.
func (m *RepoPickerModel) SelectAll() {
	for _, r := range m.repos {
		m.selected[r] = true
	}
}

// SelectedRepos returns the selected repos as a map (repo -> true).
func (m RepoPickerModel) SelectedRepos() map[string]bool {
	out := make(map[string]bool)
	for _, r := range m.repos {
		if m.selected[r] {
			out[r] = true
		}
	}
	return out
}

// View renders the repo picker overlay.
func (m *RepoPickerModel) View() string {
	if m.width == 0 {
		m.width = 60
	}
	if m.height == 0 {
		m.height = 20
	}

	t := m.theme

	// Calculate box dimensions
	boxWidth := 50
	if m.width < 60 {
		boxWidth = m.width - 10
	}
	if boxWidth < 30 {
		boxWidth = 30
	}

	var lines []string

	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		MarginBottom(1)
	lines = append(lines, titleStyle.Render("Repo Filter"))
	lines = append(lines, "")

	if len(m.repos) == 0 {
		emptyStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
		lines = append(lines, emptyStyle.Render("No repos available."))
	} else {
		selIdx := m.selectedIndex
		if selIdx < 0 {
			selIdx = 0
		} else if selIdx >= len(m.repos) {
			selIdx = len(m.repos) - 1
		}

		maxVisible := m.height - 10
		if maxVisible < 5 {
			maxVisible = 5
		}

		startIdx := 0
		endIdx := len(m.repos)
		if len(m.repos) > maxVisible {
			if selIdx < maxVisible-1 {
				startIdx = 0
				endIdx = maxVisible - 1
			} else if selIdx >= len(m.repos)-(maxVisible-1) {
				endIdx = len(m.repos)
				startIdx = len(m.repos) - (maxVisible - 1)
				if startIdx < 0 {
					startIdx = 0
				}
			} else {
				slots := maxVisible - 2
				if slots < 1 {
					slots = 1
				}
				startIdx = selIdx - slots/2
				endIdx = startIdx + slots
			}
		}

		moreStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
		if startIdx > 0 {
			lines = append(lines, moreStyle.Render(fmt.Sprintf("  ↑ %d more", startIdx)))
		}
		for i := startIdx; i < endIdx; i++ {
			repo := m.repos[i]
			isCursor := i == selIdx
			isSelected := m.selected[repo]

			nameStyle := t.Renderer.NewStyle().Foreground(t.Base.GetForeground())
			if isCursor {
				nameStyle = nameStyle.Foreground(t.Primary).Bold(true)
			}

			prefix := "  "
			if isCursor {
				prefix = "▸ "
			}
			check := "[ ]"
			if isSelected {
				check = "[x]"
			}

			line := truncateRunesHelper(prefix+check+" "+repo, boxWidth-4, "…")
			lines = append(lines, nameStyle.Render(line))
		}
		if rem := len(m.repos) - endIdx; rem > 0 {
			lines = append(lines, moreStyle.Render(fmt.Sprintf("  ↓ %d more", rem)))
		}
	}

	lines = append(lines, "")
	footerStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)
	lines = append(lines, footerStyle.Render("j/k: navigate • space: toggle • a: all • enter: apply • esc: cancel"))

	content := strings.Join(lines, "\n")

	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Width(boxWidth)
	box := boxStyle.Render(content)

	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		box,
	)
}
