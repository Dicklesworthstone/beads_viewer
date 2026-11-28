package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/charmbracelet/lipgloss"
)

// RepoPickerModel represents the repository filter picker overlay (workspace mode).
type RepoPickerModel struct {
	repos         []string
	selectedIndex int
	selected      map[string]bool // repo -> selected
	crossRepoFlow *analysis.CrossRepoFlow
	width         int
	height        int
	theme         Theme
}

// SetCrossRepoFlow attaches cross-repository dependency counts for display in the picker (W32-6).
func (m *RepoPickerModel) SetCrossRepoFlow(flow *analysis.CrossRepoFlow) {
	m.crossRepoFlow = flow
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

	// Calculate box dimensions (P24A-8: never exceed m.width-2 even on ultra-narrow panes)
	boxWidth := 50
	if m.width < 60 {
		boxWidth = m.width - 10
	}
	if boxWidth < 30 {
		boxWidth = 30
	}
	if maxBox := max(1, m.width-2); boxWidth > maxBox {
		boxWidth = maxBox
	}
	padH := 2
	if boxWidth < 16 {
		padH = 0
	} else if boxWidth < 26 {
		padH = 1
	}
	innerW := max(1, boxWidth-2*padH)

	var lines []string

	ultraShort := m.height < 8
	compactChrome := m.height <= 13
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true)
	if !ultraShort {
		titleStyle = titleStyle.MarginBottom(1)
	}
	lines = append(lines, titleStyle.Render(truncateRunesHelper("Repo Filter", innerW, "…")))
	if !compactChrome {
		lines = append(lines, "")
	}

	footerStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)
	footerText := "j/k: navigate • space: toggle • a: all • enter: apply • esc: cancel"
	footerRendered := footerStyle.Width(innerW).Render(footerText)
	footerLines := strings.Count(footerRendered, "\n") + 1
	// Vertical chrome: border(2) + padding(2 or 0) + title/blanks(3, 2, or 1) + blank before footer(1 or 0) + footerLines.
	baseChrome := 8
	if ultraShort {
		baseChrome = 3
	} else if compactChrome {
		baseChrome = 5
	}
	minItems := 2
	if m.height <= 9 {
		minItems = 1
	}
	if m.height < baseChrome+footerLines+minItems {
		footerText = truncateRunesHelper("j/k • space • a:all • enter • esc", innerW, "…")
		footerRendered = footerStyle.Width(innerW).Render(footerText)
		footerLines = strings.Count(footerRendered, "\n") + 1
	}

	if len(m.repos) == 0 {
		emptyStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
		lines = append(lines, emptyStyle.Render(truncateRunesHelper("No repos available.", innerW, "…")))
	} else {
		selIdx := m.selectedIndex
		if selIdx < 0 {
			selIdx = 0
		} else if selIdx >= len(m.repos) {
			selIdx = len(m.repos) - 1
		}

		chromeLines := baseChrome + footerLines
		showFlowSummary := m.crossRepoFlow != nil && m.crossRepoFlow.TotalCrossRepoDeps > 0 && m.height >= chromeLines+4
		if showFlowSummary {
			chromeLines += 2
		}
		maxVisible := m.height - chromeLines
		if maxVisible < minItems {
			maxVisible = minItems
		}

		startIdx := 0
		endIdx := len(m.repos)
		compactScrollHint := ""
		if len(m.repos) > maxVisible {
			if maxVisible == 1 {
				startIdx = selIdx
				endIdx = selIdx + 1
			} else if maxVisible < 3 {
				startIdx = selIdx
				endIdx = selIdx + 1
				rem := len(m.repos) - endIdx
				compactScrollHint = fmt.Sprintf("  ↑ %d / ↓ %d more", startIdx, rem)
			} else if selIdx < maxVisible-1 {
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

		blocksByRepo := make(map[string]int)
		blockedByRepo := make(map[string]int)
		if m.crossRepoFlow != nil {
			for _, dep := range m.crossRepoFlow.Dependencies {
				if fromKey := normalizeRepoKey(dep.FromRepo); fromKey != "" {
					blocksByRepo[fromKey] += dep.IssueCount
				}
				if toKey := normalizeRepoKey(dep.ToRepo); toKey != "" {
					blockedByRepo[toKey] += dep.IssueCount
				}
			}
		}

		moreStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
		if compactScrollHint == "" && startIdx > 0 {
			lines = append(lines, moreStyle.Render(truncateRunesHelper(fmt.Sprintf("  ↑ %d more", startIdx), innerW, "…")))
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

			flowSuffix := ""
			repoKey := normalizeRepoKey(repo)
			outCnt := blocksByRepo[repoKey]
			inCnt := blockedByRepo[repoKey]
			if outCnt > 0 || inCnt > 0 {
				var parts []string
				if outCnt > 0 {
					parts = append(parts, fmt.Sprintf("→%d", outCnt))
				}
				if inCnt > 0 {
					parts = append(parts, fmt.Sprintf("←%d", inCnt))
				}
				flowSuffix = " (" + strings.Join(parts, " ") + ")"
			}

			line := truncateRunesHelper(prefix+check+" "+repo+flowSuffix, innerW, "…")
			lines = append(lines, nameStyle.Render(line))
		}
		if compactScrollHint != "" {
			lines = append(lines, moreStyle.Render(truncateRunesHelper(compactScrollHint, innerW, "…")))
		} else if rem := len(m.repos) - endIdx; rem > 0 {
			lines = append(lines, moreStyle.Render(truncateRunesHelper(fmt.Sprintf("  ↓ %d more", rem), innerW, "…")))
		}
		if showFlowSummary {
			flowSummary := fmt.Sprintf("Cross-repo: %d blocking edge(s)", m.crossRepoFlow.TotalCrossRepoDeps)
			if len(m.crossRepoFlow.BottleneckRepos) > 0 {
				flowSummary += " • top blocker: " + m.crossRepoFlow.BottleneckRepos[0]
			}
			lines = append(lines, "")
			lines = append(lines, moreStyle.Render(truncateRunesHelper(flowSummary, innerW, "…")))
		}
	}

	if !ultraShort {
		lines = append(lines, "")
	}
	lines = append(lines, footerStyle.Render(footerText))

	content := strings.Join(lines, "\n")

	padV := 1
	if compactChrome {
		padV = 0
	}
	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(padV, padH).
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
