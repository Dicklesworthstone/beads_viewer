package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ShortcutsSidebar provides a toggleable panel showing context-aware keyboard shortcuts
// Unlike the help overlay, this can remain visible while working (bv-3qi5)
type ShortcutsSidebar struct {
	width        int
	height       int
	scrollOffset int
	theme        Theme
	context      string       // Current context for filtering shortcuts
	keyRegistry  *KeyRegistry // Registry for auto-generated bindings (bv-xl6g)
	focusHint    focus        // Current focus for registry lookup (bv-xl6g)
}

// shortcutItem represents a single keyboard shortcut
type shortcutItem struct {
	key  string
	desc string
}

// shortcutSection groups shortcuts by category
type shortcutSection struct {
	title    string
	items    []shortcutItem
	contexts []string // Which contexts this section applies to (empty = all)
}

// NewShortcutsSidebar creates a new shortcuts sidebar
func NewShortcutsSidebar(theme Theme) ShortcutsSidebar {
	return ShortcutsSidebar{
		theme:   theme,
		width:   34, // Fixed width for sidebar (increased for readability)
		context: "list",
	}
}

// SetSize updates the sidebar dimensions
func (s *ShortcutsSidebar) SetSize(width, height int) {
	s.width = width
	s.height = height
}

// SetContext updates the current context for filtering shortcuts
func (s *ShortcutsSidebar) SetContext(ctx string) {
	s.context = ctx
}

// SetFocus updates the current focus for registry-based bindings (bv-xl6g)
func (s *ShortcutsSidebar) SetFocus(f focus) {
	s.focusHint = f
	s.context = ContextFromFocus(f)
}

// SetKeyRegistry sets the key registry for auto-generated bindings (bv-xl6g)
func (s *ShortcutsSidebar) SetKeyRegistry(r *KeyRegistry) {
	s.keyRegistry = r
}

// ScrollUp scrolls the sidebar content up
func (s *ShortcutsSidebar) ScrollUp() {
	if s.scrollOffset > 0 {
		s.scrollOffset--
	}
}

// ScrollDown scrolls the sidebar content down
func (s *ShortcutsSidebar) ScrollDown() {
	s.scrollOffset++
}

// ScrollPageUp scrolls up by a page
func (s *ShortcutsSidebar) ScrollPageUp() {
	s.scrollOffset -= 10
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
}

// ScrollPageDown scrolls down by a page
func (s *ShortcutsSidebar) ScrollPageDown() {
	s.scrollOffset += 10
}

// ResetScroll resets scroll position to top
func (s *ShortcutsSidebar) ResetScroll() {
	s.scrollOffset = 0
}

// Width returns the fixed width of the sidebar
func (s *ShortcutsSidebar) Width() int {
	return s.width
}

// sectionsFromRegistry builds shortcut sections from the key registry (bv-xl6g).
// Returns nil if registry is nil or has no bindings for current focus.
func (s *ShortcutsSidebar) sectionsFromRegistry() []shortcutSection {
	if s.keyRegistry == nil {
		return nil
	}

	bindings := s.keyRegistry.AllBindingsForFocus(s.focusHint)
	if len(bindings) == 0 {
		return nil
	}

	// Group by Category
	categoryItems := make(map[string][]shortcutItem)
	categoryOrder := []string{} // Preserve order of first appearance

	for _, b := range bindings {
		cat := b.Category
		if cat == "" {
			cat = "Other"
		}
		if _, exists := categoryItems[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categoryItems[cat] = append(categoryItems[cat], shortcutItem{
			key:  b.Key,
			desc: b.Desc,
		})
	}

	// Build sections in category order
	sections := make([]shortcutSection, 0, len(categoryOrder))
	for _, cat := range categoryOrder {
		sections = append(sections, shortcutSection{
			title:    cat,
			items:    categoryItems[cat],
			contexts: []string{}, // Registry bindings are already focus-filtered
		})
	}

	return sections
}

// allSections returns all shortcut sections with their contexts.
// Tries registry-based sections first, falls back to hardcoded (bv-xl6g).
func (s *ShortcutsSidebar) allSections() []shortcutSection {
	// Try registry-based sections first
	if sections := s.sectionsFromRegistry(); len(sections) > 0 {
		return sections
	}

	// Fallback to hardcoded sections
	return s.hardcodedSections()
}

// hardcodedSections returns the manually maintained shortcut sections.
// Used as fallback when registry has no bindings (bv-xl6g).
func (s *ShortcutsSidebar) hardcodedSections() []shortcutSection {
	return []shortcutSection{
		{
			title: "Navigation",
			contexts: []string{
				"list", "detail", "split", "board", "graph", "insights",
				"history", "actionable", "tree", "flow", "attention", "label",
			},
			items: []shortcutItem{
				{"j/k", "Move ↓/↑"},
				{"G/Home", "End/Start"},
				{"^d/^u", "Page ↓/↑"},
				{"Enter", "Details"},
				{"Esc", "Back"},
			},
		},
		{
			title:    "Help & Global",
			contexts: []string{}, // All contexts
			items: []shortcutItem{
				{"?", "Help overlay"},
				{"~", "Context help"},
				{"`", "Full tutorial"},
				{";", "This sidebar"},
				{"^r/F5", "Refresh"},
				{"q", "Back / Quit"},
			},
		},
		{
			title:    "Views",
			contexts: []string{"list", "detail", "split"},
			items: []shortcutItem{
				{"a", "Actionable"},
				{"b", "Board"},
				{"g", "Graph"},
				{"E", "Tree"},
				{"h", "History"},
				{"i", "Insights"},
				{"P", "Sprint"},
				{"[", "Labels"},
				{"]", "Attention"},
				{"f", "Flow matrix"},
				{"p", "Priority hints"},
			},
		},
		{
			title:    "Graph",
			contexts: []string{"graph"},
			items: []shortcutItem{
				{"hjkl", "Navigate"},
				{"Space", "Expand/collapse"},
				{"H/L", "Scroll ←/→"},
				{"J/K", "Scroll ↓/↑"},
				{"PgUp/Dn", "Jump 10 nodes"},
				{"y", "Copy ID"},
				{"Enter", "Jump to issue"},
			},
		},
		{
			title:    "Tree",
			contexts: []string{"tree"},
			items: []shortcutItem{
				{"j/k", "Move ↓/↑"},
				{"Enter", "Expand/collapse"},
				{"h/l", "Parent / Child"},
				{"o/O", "Expand/Collapse all"},
				{"gg/G", "Top / Bottom"},
				{"Tab", "Toggle detail"},
				{"y", "Copy ID"},
				{"V", "Cass sessions"},
				{"E/Esc", "Exit tree"},
			},
		},
		{
			title:    "Insights",
			contexts: []string{"insights"},
			items: []shortcutItem{
				{"h/l/Tab", "Switch panel"},
				{"j/k", "Select item"},
				{"^j/^k", "Scroll detail"},
				{"</>", "Resize detail"},
				{"e", "Explanations"},
				{"x", "Calc proof"},
				{"m/H", "Heatmap"},
				{"Enter", "Jump to issue"},
			},
		},
		{
			title:    "Actionable",
			contexts: []string{"actionable"},
			items: []shortcutItem{
				{"j/k", "Move ↓/↑"},
				{"Home/G", "Start / End"},
				{"PgUp/Dn", "Page ↑/↓"},
				{"Enter", "Open details"},
				{"y", "Copy ID"},
				{"a/Esc", "Exit actionable"},
			},
		},
		{
			title:    "History",
			contexts: []string{"history"},
			items: []shortcutItem{
				{"v", "Git/Bead mode"},
				{"/", "Search"},
				{"j/k", "Navigate ↓/↑"},
				{"J/K", "Detail ↓/↑"},
				{"Tab", "Focus toggle"},
				{"t/f", "Timeline / Files"},
				{"c", "Confidence"},
				{"y", "Copy SHA"},
				{"o", "Open in browser"},
				{"V", "Cass sessions"},
			},
		},
		{
			title:    "Board",
			contexts: []string{"board"},
			items: []shortcutItem{
				{"h/l", "Columns ←/→"},
				{"H/L/1-4", "Jump column"},
				{"j/k", "Items ↓/↑"},
				{"gg/G", "Top / Bottom"},
				{"/", "Search cards"},
				{"s", "Swimlanes"},
				{"o/c/r", "Status filter"},
				{"d/e", "Expand / Empty"},
				{"Tab", "Toggle detail"},
				{"y/V", "Copy ID / Cass"},
				{"Enter", "Full view"},
			},
		},
		{
			title:    "Flow Matrix",
			contexts: []string{"flow"},
			items: []shortcutItem{
				{"j/k", "Move ↓/↑"},
				{"g/G", "Top / Bottom"},
				{"Tab", "Switch panel"},
				{"Enter", "Drilldown / Issue"},
				{"y", "Copy ID"},
				{"f/Esc", "Back / Exit"},
			},
		},
		{
			title:    "Sprint",
			contexts: []string{"sprint"},
			items: []shortcutItem{
				{"j/k", "Next / Prev sprint"},
				{"P/Esc", "Exit sprint view"},
			},
		},
		{
			title:    "Attention",
			contexts: []string{"attention"},
			items: []shortcutItem{
				{"j/k", "Move ↓/↑"},
				{"g/G", "Top / Bottom"},
				{"Enter", "Label drilldown"},
				{"1-9", "Filter by rank"},
				{"]/Esc", "Exit attention"},
			},
		},
		{
			title:    "Labels",
			contexts: []string{"label"},
			items: []shortcutItem{
				{"j/k", "Select label"},
				{"Enter", "Filter by label"},
				{"h", "Health detail"},
				{"d", "Label drilldown"},
				{"v", "Velocity view"},
				{"[/Esc", "Exit labels"},
			},
		},
		{
			title:    "Velocity",
			contexts: []string{"velocity"},
			items: []shortcutItem{
				{"j/k", "Select label"},
				{"Enter", "Filter by label"},
				{"v/Esc", "Back to labels"},
			},
		},
		{
			title:    "Filters",
			contexts: []string{"list", "split"},
			items: []shortcutItem{
				{"o", "Open only"},
				{"c", "Closed only"},
				{"r", "Ready (no blocks)"},
				{"l", "Label picker"},
				{"/", "Search"},
				{"s/S", "Sort / Triage"},
				{"w", "Repo picker"},
			},
		},
		{
			title:    "Actions",
			contexts: []string{"list", "detail", "split"},
			items: []shortcutItem{
				{"t/T", "Time-travel"},
				{"x", "Export .md"},
				{"y", "Copy ID"},
				{"C", "Copy issue"},
				{"O", "Open in $EDITOR"},
				{"'", "Recipe picker"},
				{"!", "Alerts panel"},
				{"U", "Self-update"},
				{"V", "Cass sessions"},
			},
		},
	}
}

// View renders the sidebar
func (s *ShortcutsSidebar) View() string {
	t := s.theme

	// Styles
	titleStyle := t.Renderer.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		Align(lipgloss.Center).
		Width(s.width - 4)

	sectionStyle := t.Renderer.NewStyle().
		Foreground(t.Secondary).
		Bold(true).
		MarginTop(1)

	keyStyle := t.Renderer.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#BD93F9"}).
		Bold(true).
		Width(8)

	descStyle := t.Renderer.NewStyle().
		Foreground(t.Base.GetForeground())

	dimStyle := t.Renderer.NewStyle().
		Foreground(ColorFooterHint).
		Italic(true)

	// Build content
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Shortcuts"))
	sb.WriteString("\n")

	// Filter sections by context
	sections := s.allSections()
	for _, section := range sections {
		// Check if this section applies to current context
		if len(section.contexts) > 0 {
			found := false
			for _, ctx := range section.contexts {
				if ctx == s.context {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		sb.WriteString(sectionStyle.Render(section.title))
		sb.WriteString("\n")

		for _, item := range section.items {
			line := keyStyle.Render(item.key) + descStyle.Render(item.desc)
			sb.WriteString(line + "\n")
		}
	}

	// Build content lines for scrolling. Wrap to the box's inner width first
	// (width minus 1 cell of padding on each side) so a long description that
	// wraps is counted as the lines it really occupies; otherwise the box grows
	// past its height and the final MaxHeight clips its bottom border.
	innerWidth := max(s.width-2, 1)
	fullContent := t.Renderer.NewStyle().Width(innerWidth).Render(strings.TrimSuffix(sb.String(), "\n"))
	lines := strings.Split(fullContent, "\n")
	totalLines := len(lines)

	// Calculate visible area: s.height is the sidebar's total rendered height,
	// which includes the top and bottom border rows and the scroll-hint row.
	availableHeight := s.height - 3
	if availableHeight < 5 {
		availableHeight = 5
	}

	// Clamp scroll
	maxScroll := totalLines - availableHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if s.scrollOffset > maxScroll {
		s.scrollOffset = maxScroll
	}

	// Extract visible lines
	startLine := s.scrollOffset
	endLine := startLine + availableHeight
	if endLine > totalLines {
		endLine = totalLines
	}
	visibleLines := lines[startLine:endLine]
	visibleContent := strings.Join(visibleLines, "\n")

	// Add scroll hint if needed
	var footer string
	if totalLines > availableHeight {
		scrollPercent := 0
		if maxScroll > 0 {
			scrollPercent = s.scrollOffset * 100 / maxScroll
		}
		footer = dimStyle.Render(fmt.Sprintf("j/k scroll %d%%", scrollPercent))
	} else {
		footer = dimStyle.Render("; hide")
	}

	// Combine content and footer
	content := visibleContent + "\n" + footer

	// Create the sidebar box. Height is the content height inside the border,
	// so the rendered box (border included) is exactly s.height rows tall.
	boxStyle := t.Renderer.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Secondary).
		Padding(0, 1).
		Width(s.width)
	if s.height > 0 {
		boxStyle = boxStyle.Height(max(s.height-2, 1)).MaxHeight(s.height)
	}

	return boxStyle.Render(content)
}

// ContextFromFocus returns the context string for the current focus.
func ContextFromFocus(f focus) string {
	switch f {
	case focusList:
		return "list"
	case focusDetail:
		return "detail"
	case focusBoard:
		return "board"
	case focusGraph:
		return "graph"
	case focusInsights:
		return "insights"
	case focusHistory:
		return "history"
	case focusActionable:
		return "actionable"
	case focusLabelDashboard:
		return "label"
	case focusTree:
		return "tree"
	case focusFlowMatrix:
		return "flow"
	case focusSprint:
		return "sprint"
	case focusAttention:
		return "attention"
	case focusVelocityComparison:
		return "velocity"
	default:
		return "list"
	}
}
