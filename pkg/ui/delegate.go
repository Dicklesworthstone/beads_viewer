package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// IssueDelegate renders issue items in the list
type IssueDelegate struct {
	Theme             Theme
	ShowPriorityHints bool
	PriorityHints     map[string]*analysis.PriorityRecommendation
}

func (d IssueDelegate) Height() int {
	return 1
}

func (d IssueDelegate) Spacing() int {
	return 0
}

func (d IssueDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	return nil
}

func (d IssueDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(IssueItem)
	if !ok {
		return
	}

	t := d.Theme
	width := m.Width()
	if width <= 0 {
		width = 80
	}
	// Reduce width by 1 to prevent terminal wrapping on the exact edge
	width = width - 1

	isSelected := index == m.Index()

	// ══════════════════════════════════════════════════════════════════════════
	// POLISHED ROW LAYOUT - Stripe-level visual hierarchy
	// Layout: [sel] [type] [prio-badge] [status-badge] [ID] [title...] [meta]
	// ══════════════════════════════════════════════════════════════════════════

	// Get all the data
	icon, iconColor := t.GetTypeIcon(string(i.Issue.IssueType))
	idStr := i.Issue.ID
	title := i.Issue.Title
	ageStr := FormatTimeRel(i.Issue.CreatedAt)
	commentCount := len(i.Issue.Comments)

	// Calculate widths for right-side columns (fixed)
	rightWidth := 0
	var rightParts []string

	// Show Age and Comments only if we have reasonable width
	if width > 60 {
		// Age - with subtle styling
		ageStyle := t.Renderer.NewStyle().Foreground(ColorMuted)
		rightParts = append(rightParts, ageStyle.Render(fmt.Sprintf("%8s", ageStr)))
		rightWidth += 9

		// Comments with icon
		if commentCount > 0 {
			commentStyle := t.Renderer.NewStyle().Foreground(ColorInfo)
			rightParts = append(rightParts, commentStyle.Render(fmt.Sprintf("💬%d", commentCount)))
			rightWidth += 4 + len(fmt.Sprintf("%d", commentCount))
		} else {
			rightParts = append(rightParts, "   ")
			rightWidth += 3
		}
	}

	// Assignee (if present and we have room)
	if width > 100 && i.Issue.Assignee != "" {
		assignee := truncateRunesHelper(i.Issue.Assignee, 12, "…")
		assigneeStyle := t.Renderer.NewStyle().Foreground(ColorSecondary)
		rightParts = append(rightParts, assigneeStyle.Render(fmt.Sprintf("@%-12s", assignee)))
		rightWidth += 14
	}

	// Labels (if present and we have room) - render as mini tags
	if width > 140 && len(i.Issue.Labels) > 0 {
		labelStr := truncateRunesHelper(strings.Join(i.Issue.Labels, ","), 20, "…")
		labelStyle := t.Renderer.NewStyle().
			Foreground(ColorPrimary).
			Background(ColorBgSubtle).
			Padding(0, 1)
		rightParts = append(rightParts, labelStyle.Render(labelStr))
		rightWidth += lipgloss.Width(labelStyle.Render(labelStr)) + 1
	}

	// Left side fixed columns with polished badges
	// [selector 2] [icon 2] [prio-badge 3] [hint 1-2] [status-badge 6] [id dynamic] [space]
	leftFixedWidth := 2 + 3 // selector + icon

	// Priority badge (polished)
	prioBadge := RenderPriorityBadge(i.Issue.Priority)
	prioBadgeWidth := lipgloss.Width(prioBadge)
	leftFixedWidth += prioBadgeWidth + 1

	// Priority hint indicator
	if d.ShowPriorityHints {
		leftFixedWidth += 2
	}

	// Status badge (polished)
	statusBadge := RenderStatusBadge(string(i.Issue.Status))
	statusBadgeWidth := lipgloss.Width(statusBadge)
	leftFixedWidth += statusBadgeWidth + 1

	// ID width - use actual rune length, but cap reasonably
	idRunes := []rune(idStr)
	idWidth := len(idRunes)
	if idWidth > 35 {
		idWidth = 35
		idStr = truncateRunesHelper(idStr, 35, "…")
	}
	leftFixedWidth += idWidth + 1

	// Diff badge width adjustment
	if badge := i.DiffStatus.Badge(); badge != "" {
		leftFixedWidth += lipgloss.Width(badge) + 1
	}

	// Title gets everything in between
	titleWidth := width - leftFixedWidth - rightWidth - 2
	if titleWidth < 5 {
		titleWidth = 5
	}

	// Truncate title if needed
	titleRunes := []rune(title)
	if len(titleRunes) > titleWidth {
		title = string(titleRunes[:titleWidth-1]) + "…"
	} else {
		// Pad title to fill space
		title = title + strings.Repeat(" ", titleWidth-len(titleRunes))
	}

	// ══════════════════════════════════════════════════════════════════════════
	// BUILD THE ROW
	// ══════════════════════════════════════════════════════════════════════════
	var leftSide strings.Builder

	// Selection indicator with accent color
	if isSelected {
		leftSide.WriteString(t.Renderer.NewStyle().Foreground(t.Primary).Bold(true).Render("▸ "))
	} else {
		leftSide.WriteString("  ")
	}

	// Type icon with color
	leftSide.WriteString(t.Renderer.NewStyle().Foreground(iconColor).Render(icon))
	leftSide.WriteString(" ")

	// Priority badge (polished)
	leftSide.WriteString(prioBadge)
	leftSide.WriteString(" ")

	// Priority hint indicator (↑/↓)
	if d.ShowPriorityHints && d.PriorityHints != nil {
		if hint, ok := d.PriorityHints[i.Issue.ID]; ok {
			if hint.Direction == "increase" {
				leftSide.WriteString(t.Renderer.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true).Render("↑"))
			} else if hint.Direction == "decrease" {
				leftSide.WriteString(t.Renderer.NewStyle().Foreground(lipgloss.Color("#4ECDC4")).Bold(true).Render("↓"))
			}
		} else {
			leftSide.WriteString(" ")
		}
		leftSide.WriteString(" ")
	}

	// Status badge (polished)
	leftSide.WriteString(statusBadge)
	leftSide.WriteString(" ")

	// ID with secondary styling
	idStyle := t.Renderer.NewStyle().Foreground(t.Secondary)
	if isSelected {
		idStyle = idStyle.Bold(true)
	}
	leftSide.WriteString(idStyle.Render(idStr))
	leftSide.WriteString(" ")

	// Diff badge (time-travel mode)
	if badge := i.DiffStatus.Badge(); badge != "" {
		leftSide.WriteString(badge)
		leftSide.WriteString(" ")
	}

	// Title with emphasis when selected
	titleStyle := t.Renderer.NewStyle()
	if isSelected {
		titleStyle = titleStyle.Foreground(t.Primary).Bold(true)
	} else {
		titleStyle = titleStyle.Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#E8E8E8"})
	}
	leftSide.WriteString(titleStyle.Render(title))

	// Right side
	rightSide := strings.Join(rightParts, " ")

	// Combine: left + padding + right
	leftLen := lipgloss.Width(leftSide.String())
	rightLen := lipgloss.Width(rightSide)
	padding := width - leftLen - rightLen
	if padding < 0 {
		padding = 0
	}

	// Construct the row string
	row := leftSide.String() + strings.Repeat(" ", padding) + rightSide

	// Apply row background for selection and clamp width
	rowStyle := t.Renderer.NewStyle().Width(width).MaxWidth(width)
	if isSelected {
		row = rowStyle.Background(t.Highlight).Render(row)
	} else {
		row = rowStyle.Render(row)
	}

	fmt.Fprint(w, row)
}
