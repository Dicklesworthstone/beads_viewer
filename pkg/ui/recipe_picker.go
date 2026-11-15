package ui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"

	"github.com/charmbracelet/lipgloss"
)

// RecipePickerModel represents the recipe picker overlay
type RecipePickerModel struct {
	recipes       []recipe.Recipe
	selectedIndex int
	width         int
	height        int
	theme         Theme
}

// NewRecipePickerModel creates a new recipe picker
func NewRecipePickerModel(recipes []recipe.Recipe, theme Theme) RecipePickerModel {
	return RecipePickerModel{
		recipes:       recipes,
		selectedIndex: 0,
		theme:         theme,
	}
}

// SetSize updates the picker dimensions
func (m *RecipePickerModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// MoveUp moves selection up
func (m *RecipePickerModel) MoveUp() {
	if m.selectedIndex > 0 {
		m.selectedIndex--
	}
}

// MoveDown moves selection down
func (m *RecipePickerModel) MoveDown() {
	if m.selectedIndex < len(m.recipes)-1 {
		m.selectedIndex++
	}
}

// SelectedRecipe returns the currently selected recipe
func (m *RecipePickerModel) SelectedRecipe() *recipe.Recipe {
	if len(m.recipes) == 0 || m.selectedIndex >= len(m.recipes) {
		return nil
	}
	return &m.recipes[m.selectedIndex]
}

// SelectedIndex returns the current selection index
func (m *RecipePickerModel) SelectedIndex() int {
	return m.selectedIndex
}

// View renders the recipe picker overlay
func (m *RecipePickerModel) View() string {
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

	// Build content
	var lines []string

	// Title
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		MarginBottom(1)
	lines = append(lines, titleStyle.Render("Select Recipe"))
	lines = append(lines, "")

	// Recipe list windowed around selectedIndex so 24-38 row terminals never clip unreachable items
	if len(m.recipes) > 0 {
		selIdx := m.selectedIndex
		if selIdx < 0 {
			selIdx = 0
		} else if selIdx >= len(m.recipes) {
			selIdx = len(m.recipes) - 1
		}

		availLines := m.height - 10
		if availLines < 5 {
			availLines = 5
		}

		compact := len(m.recipes)*3 > availLines
		renderedItems := make([][]string, len(m.recipes))
		for i, r := range m.recipes {
			isSelected := i == selIdx
			nameStyle := t.Renderer.NewStyle()
			if isSelected {
				nameStyle = nameStyle.Foreground(t.Primary).Bold(true)
			} else {
				nameStyle = nameStyle.Foreground(t.Base.GetForeground())
			}

			prefix := "  "
			if isSelected {
				prefix = "▸ "
			}

			name := truncateRunesHelper(prefix+r.Name, boxWidth-4, "…")
			itemLines := []string{nameStyle.Render(name)}
			if r.Description != "" && (!compact || isSelected || availLines >= 8) {
				descStyle := t.Renderer.NewStyle().
					Foreground(t.Secondary).
					Italic(true)
				desc := "    " + truncateRunesHelper(r.Description, boxWidth-8, "…")
				itemLines = append(itemLines, descStyle.Render(desc))
			}
			renderedItems[i] = itemLines
		}

		spacerStep := 1
		if compact {
			spacerStep = 0
		}

		startIdx := 0
		for startIdx < selIdx {
			used := 0
			if startIdx > 0 {
				used++
			}
			if selIdx < len(m.recipes)-1 {
				used++
			}
			for i := startIdx; i <= selIdx; i++ {
				if i > startIdx {
					used += spacerStep
				}
				used += len(renderedItems[i])
			}
			if used <= availLines {
				break
			}
			startIdx++
		}

		endIdx := selIdx
		for endIdx+1 < len(m.recipes) {
			candEnd := endIdx + 1
			used := 0
			if startIdx > 0 {
				used++
			}
			if candEnd < len(m.recipes)-1 {
				used++
			}
			for i := startIdx; i <= candEnd; i++ {
				if i > startIdx {
					used += spacerStep
				}
				used += len(renderedItems[i])
			}
			if used > availLines {
				break
			}
			endIdx = candEnd
		}

		moreStyle := t.Renderer.NewStyle().Foreground(t.Secondary).Italic(true)
		if startIdx > 0 {
			lines = append(lines, moreStyle.Render(fmt.Sprintf("  ↑ %d more", startIdx)))
		}
		for i := startIdx; i <= endIdx; i++ {
			lines = append(lines, renderedItems[i]...)
			if spacerStep > 0 && i < endIdx {
				lines = append(lines, "")
			}
		}
		if rem := len(m.recipes) - 1 - endIdx; rem > 0 {
			lines = append(lines, moreStyle.Render(fmt.Sprintf("  ↓ %d more", rem)))
		}
	}

	// Footer with keybindings
	lines = append(lines, "")
	footerStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)
	lines = append(lines, footerStyle.Render("j/k: navigate • enter: apply • esc: cancel"))

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

// RecipeCount returns the number of recipes
func (m *RecipePickerModel) RecipeCount() int {
	return len(m.recipes)
}

// FormatRecipeInfo returns a formatted string for the active recipe display
func FormatRecipeInfo(r *recipe.Recipe) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("Recipe: %s", r.Name)
}
