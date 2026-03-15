package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TutorialPage represents a single page of tutorial content.
type TutorialPage struct {
	ID       string   // Unique identifier (e.g., "intro", "navigation")
	Title    string   // Page title displayed in header
	Content  string   // Markdown content
	Section  string   // Parent section for TOC grouping
	Contexts []string // Which view contexts this page applies to (empty = all)
}

// tutorialFocus tracks which element has focus (bv-wdsd)
type tutorialFocus int

const (
	focusTutorialContent tutorialFocus = iota
	focusTutorialTOC
)

// TutorialModel manages the tutorial overlay state.
type TutorialModel struct {
	pages        []TutorialPage
	currentPage  int
	scrollOffset int
	tocVisible   bool
	progress     map[string]bool // Tracks which pages have been viewed
	width        int
	height       int
	theme        Theme
	contextMode  bool   // If true, filter pages by current context
	context      string // Current view context (e.g., "list", "board", "graph")

	// Markdown rendering with Glamour (bv-lb0h)
	markdownRenderer *MarkdownRenderer

	// Keyboard navigation state (bv-wdsd)
	focus       tutorialFocus // Current focus: content or TOC
	shouldClose bool          // Signal to parent to close tutorial
	tocCursor   int           // Cursor position in TOC when focused
}

// NewTutorialModel creates a new tutorial model with default pages.
func NewTutorialModel(theme Theme) TutorialModel {
	// Calculate initial content width for markdown renderer
	contentWidth := 80 - 6 // default width minus padding
	if contentWidth < 40 {
		contentWidth = 40
	}

	return TutorialModel{
		pages:            defaultTutorialPages(),
		currentPage:      0,
		scrollOffset:     0,
		tocVisible:       false,
		progress:         make(map[string]bool),
		width:            80,
		height:           24,
		theme:            theme,
		contextMode:      false,
		context:          "",
		markdownRenderer: NewMarkdownRendererWithTheme(contentWidth, theme),
		focus:            focusTutorialContent,
		shouldClose:      false,
		tocCursor:        0,
	}
}

// Init initializes the tutorial model.
func (m TutorialModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input for the tutorial with focus management (bv-wdsd).
func (m TutorialModel) Update(msg tea.Msg) (TutorialModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Global keys (work in any focus mode)
		switch msg.String() {
		case "esc", "q":
			// Mark current page as viewed before closing
			pages := m.visiblePages()
			if m.currentPage >= 0 && m.currentPage < len(pages) {
				m.progress[pages[m.currentPage].ID] = true
			}
			m.shouldClose = true
			return m, nil

		case "t":
			// Toggle TOC and switch focus
			m.tocVisible = !m.tocVisible
			if m.tocVisible {
				m.focus = focusTutorialTOC
				m.tocCursor = m.currentPage // Sync TOC cursor with current page
			} else {
				m.focus = focusTutorialContent
			}
			return m, nil

		case "tab":
			// Switch focus between content and TOC (if visible)
			if m.tocVisible {
				if m.focus == focusTutorialContent {
					m.focus = focusTutorialTOC
					m.tocCursor = m.currentPage
				} else {
					m.focus = focusTutorialContent
				}
			} else {
				// If TOC not visible, tab advances page
				m.NextPage()
			}
			return m, nil
		}

		// Route to focus-specific handlers
		if m.focus == focusTutorialTOC && m.tocVisible {
			return m.handleTOCKeys(msg), nil
		}
		return m.handleContentKeys(msg), nil
	}
	return m, nil
}

// handleContentKeys handles keys when content area has focus (bv-wdsd).
func (m TutorialModel) handleContentKeys(msg tea.KeyMsg) TutorialModel {
	switch msg.String() {
	// Page navigation
	case "right", "l", "n", " ": // Space added for next page
		m.NextPage()
	case "left", "h", "p", "shift+tab":
		m.PrevPage()

	// Content scrolling
	case "j", "down":
		m.scrollOffset++
	case "k", "up":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}

	// Half-page scrolling (use same overhead as renderContent)
	case "ctrl+d":
		visibleHeight := m.height - 11
		if visibleHeight < 5 {
			visibleHeight = 5
		}
		m.scrollOffset += visibleHeight / 2
	case "ctrl+u":
		visibleHeight := m.height - 11
		if visibleHeight < 5 {
			visibleHeight = 5
		}
		m.scrollOffset -= visibleHeight / 2
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}

	// Jump to top/bottom
	case "g", "home":
		m.scrollOffset = 0
	case "G", "end":
		m.scrollOffset = 9999 // Will be clamped in View()

	// Jump to specific page (1-9)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		pageNum := int(msg.String()[0] - '0')
		pages := m.visiblePages()
		if pageNum > 0 && pageNum <= len(pages) {
			m.JumpToPage(pageNum - 1)
		}
	}
	return m
}

// handleTOCKeys handles keys when TOC has focus (bv-wdsd).
func (m TutorialModel) handleTOCKeys(msg tea.KeyMsg) TutorialModel {
	pages := m.visiblePages()

	switch msg.String() {
	case "j", "down":
		if m.tocCursor < len(pages)-1 {
			m.tocCursor++
		}
	case "k", "up":
		if m.tocCursor > 0 {
			m.tocCursor--
		}
	case "g", "home":
		m.tocCursor = 0
	case "G", "end":
		m.tocCursor = len(pages) - 1
	case "enter", " ":
		// Jump to selected page in TOC
		m.JumpToPage(m.tocCursor)
		m.focus = focusTutorialContent
	case "h", "left":
		// Switch back to content
		m.focus = focusTutorialContent
	}
	return m
}

// View renders the tutorial overlay.
func (m TutorialModel) View() string {
	pages := m.visiblePages()
	if len(pages) == 0 {
		return m.renderEmptyState()
	}

	// Clamp current page
	if m.currentPage >= len(pages) {
		m.currentPage = len(pages) - 1
	}
	if m.currentPage < 0 {
		m.currentPage = 0
	}

	currentPage := pages[m.currentPage]

	// Mark as viewed
	m.progress[currentPage.ID] = true

	r := m.theme.Renderer

	// Calculate dimensions
	contentWidth := m.width - 6 // padding and borders
	if m.tocVisible {
		contentWidth -= 24 // TOC sidebar width
	}
	if contentWidth < 40 {
		contentWidth = 40
	}

	// Build the view
	var b strings.Builder

	// Header
	header := m.renderHeader(currentPage, len(pages))
	b.WriteString(header)
	b.WriteString("\n")

	// Separator line
	sepStyle := r.NewStyle().Foreground(m.theme.Border)
	b.WriteString(sepStyle.Render(strings.Repeat("─", contentWidth+4)))
	b.WriteString("\n")

	// Page title and section
	pageTitleStyle := r.NewStyle().Bold(true).Foreground(m.theme.Primary)
	sectionStyle := r.NewStyle().Foreground(m.theme.Subtext).Italic(true)
	pageTitle := pageTitleStyle.Render(currentPage.Title)
	if currentPage.Section != "" {
		pageTitle += sectionStyle.Render(" — " + currentPage.Section)
	}
	b.WriteString(pageTitle)
	b.WriteString("\n")

	// Content area (with optional TOC)
	if m.tocVisible {
		toc := m.renderTOC(pages)
		content := m.renderContent(currentPage, contentWidth)
		// Join TOC and content horizontally
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, toc, "  ", content))
	} else {
		content := m.renderContent(currentPage, contentWidth)
		b.WriteString(content)
	}

	b.WriteString("\n")

	// Footer with navigation hints
	footer := m.renderFooter(len(pages))
	b.WriteString(footer)

	// Wrap in modal style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(1, 2).
		Width(m.width).
		MaxHeight(m.height)

	return modalStyle.Render(b.String())
}

// renderHeader renders the tutorial header with title and progress bar.
func (m TutorialModel) renderHeader(page TutorialPage, totalPages int) string {
	r := m.theme.Renderer

	titleStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	// Progress indicator: [2/15] ███░░░
	pageNum := m.currentPage + 1
	progressText := r.NewStyle().
		Foreground(m.theme.Subtext).
		Render(fmt.Sprintf("[%d/%d]", pageNum, totalPages))

	// Visual progress bar
	barWidth := 10
	filledWidth := 0
	if totalPages > 0 {
		filledWidth = (pageNum * barWidth) / totalPages
		// Ensure at least 1 filled bar when on any page
		if filledWidth < 1 && pageNum > 0 {
			filledWidth = 1
		}
	}
	if filledWidth > barWidth {
		filledWidth = barWidth
	}
	progressBar := r.NewStyle().
		Foreground(m.theme.Open). // Using Open (green) for progress
		Render(strings.Repeat("█", filledWidth)) +
		r.NewStyle().
			Foreground(m.theme.Muted).
			Render(strings.Repeat("░", barWidth-filledWidth))

	// Title
	title := titleStyle.Render("📚 beads_viewer Tutorial")

	// Calculate spacing to align progress to the right
	headerContent := title + "  " + progressText + " " + progressBar

	return headerContent
}

// renderContent renders the page content with native lipgloss components or Glamour markdown.
func (m TutorialModel) renderContent(page TutorialPage, width int) string {
	r := m.theme.Renderer

	// Check if we have a structured page for this ID (preferred)
	if structuredPage := getStructuredPage(page.ID); structuredPage != nil {
		// Use native lipgloss component rendering
		return m.renderStructuredContent(*structuredPage, width)
	}

	// Fallback to markdown rendering for unconverted pages
	var renderedContent string
	if m.markdownRenderer != nil {
		rendered, err := m.markdownRenderer.Render(page.Content)
		if err == nil {
			renderedContent = strings.TrimSpace(rendered)
		} else {
			// Fallback to raw content on error
			renderedContent = page.Content
		}
	} else {
		renderedContent = page.Content
	}

	// Split rendered content into lines for scrolling
	lines := strings.Split(renderedContent, "\n")

	// Compress runs of 3+ blank lines into 2 blank lines max
	// This helps with glamour sometimes adding excessive whitespace
	var compressedLines []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount <= 2 {
				compressedLines = append(compressedLines, line)
			}
		} else {
			blankCount = 0
			compressedLines = append(compressedLines, line)
		}
	}
	lines = compressedLines

	// Calculate visible lines based on height
	// Overhead: border (2) + padding (2) + header (1) + separator (1) + title (1) +
	//           title margin (1) + footer (1) = 9 lines
	// Plus 1-2 for scroll indicators that may be added
	visibleHeight := m.height - 11
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	// Clamp scroll offset
	maxScroll := len(lines) - visibleHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}

	// Get visible lines
	endLine := m.scrollOffset + visibleHeight
	if endLine > len(lines) {
		endLine = len(lines)
	}
	visibleLines := lines[m.scrollOffset:endLine]

	// Join visible lines (already styled by Glamour)
	content := strings.Join(visibleLines, "\n")

	// Add scroll indicators (these are accounted for in the height calculation)
	if m.scrollOffset > 0 {
		scrollUpHint := r.NewStyle().Foreground(m.theme.Muted).Render("↑ more above")
		content = scrollUpHint + "\n" + content
	}
	if endLine < len(lines) {
		scrollDownHint := r.NewStyle().Foreground(m.theme.Muted).Render("↓ more below")
		content = content + "\n" + scrollDownHint
	}

	return content
}

// renderStructuredContent renders a structured tutorial page with native lipgloss components.
func (m TutorialModel) renderStructuredContent(page StructuredTutorialPage, width int) string {
	// Render all elements using native lipgloss components
	renderedContent := RenderStructuredPage(page, m.theme, width)

	// Split into lines for scrolling
	lines := strings.Split(renderedContent, "\n")

	// Calculate visible lines based on height
	visibleHeight := m.height - 11
	if visibleHeight < 5 {
		visibleHeight = 5
	}

	// Clamp scroll offset
	maxScroll := len(lines) - visibleHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}

	// Get visible lines
	endLine := m.scrollOffset + visibleHeight
	if endLine > len(lines) {
		endLine = len(lines)
	}
	visibleLines := lines[m.scrollOffset:endLine]

	// Join visible lines
	content := strings.Join(visibleLines, "\n")

	// Add scroll indicators
	if m.scrollOffset > 0 {
		scrollUpHint := m.theme.Renderer.NewStyle().Foreground(m.theme.Muted).Render("↑ more above")
		content = scrollUpHint + "\n" + content
	}
	if endLine < len(lines) {
		scrollDownHint := m.theme.Renderer.NewStyle().Foreground(m.theme.Muted).Render("↓ more below")
		content = content + "\n" + scrollDownHint
	}

	return content
}

// renderTOC renders the table of contents sidebar with focus indication (bv-wdsd).
func (m TutorialModel) renderTOC(pages []TutorialPage) string {
	r := m.theme.Renderer

	// Use different border style when TOC has focus
	borderColor := m.theme.Border
	if m.focus == focusTutorialTOC {
		borderColor = m.theme.Primary
	}

	tocStyle := r.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(22)

	headerStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	sectionStyle := r.NewStyle().
		Foreground(m.theme.Secondary).
		Bold(true)

	itemStyle := r.NewStyle().
		Foreground(m.theme.Subtext)

	selectedStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	// TOC cursor style (when TOC has focus and cursor is on this item)
	cursorStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.InProgress).
		Background(m.theme.Highlight)

	viewedStyle := r.NewStyle().
		Foreground(m.theme.Open)

	var b strings.Builder
	b.WriteString(headerStyle.Render("Contents"))
	if m.focus == focusTutorialTOC {
		b.WriteString(r.NewStyle().Foreground(m.theme.Primary).Render(" ●"))
	}
	b.WriteString("\n")

	currentSection := ""
	for i, page := range pages {
		// Show section header if changed
		if page.Section != currentSection && page.Section != "" {
			currentSection = page.Section
			b.WriteString("\n")
			b.WriteString(sectionStyle.Render("▸ " + currentSection))
			b.WriteString("\n")
		}

		// Determine style based on cursor position and current page
		prefix := "   "
		style := itemStyle

		// TOC has focus and cursor is on this item
		if m.focus == focusTutorialTOC && i == m.tocCursor {
			prefix = " → "
			style = cursorStyle
		} else if i == m.currentPage {
			// Current page indicator (but not cursor)
			prefix = " ▶ "
			style = selectedStyle
		}

		// Truncate long titles
		title := page.Title
		if len(title) > 14 {
			title = title[:12] + "…"
		}

		// Viewed indicator
		viewed := ""
		if m.progress[page.ID] {
			viewed = viewedStyle.Render(" ✓")
		}

		b.WriteString(style.Render(prefix+title) + viewed)
		b.WriteString("\n")
	}

	return tocStyle.Render(b.String())
}

// renderFooter renders context-sensitive navigation hints (bv-wdsd).
func (m TutorialModel) renderFooter(totalPages int) string {
	r := m.theme.Renderer

	keyStyle := r.NewStyle().
		Bold(true).
		Foreground(m.theme.Primary)

	descStyle := r.NewStyle().
		Foreground(ColorFooterHint)

	sepStyle := r.NewStyle().
		Foreground(ColorFooterSep)

	var hints []string

	if m.focus == focusTutorialTOC && m.tocVisible {
		// TOC-focused hints
		hints = []string{
			keyStyle.Render("j/k") + descStyle.Render(" select"),
			keyStyle.Render("Enter") + descStyle.Render(" go to page"),
			keyStyle.Render("Tab") + descStyle.Render(" back to content"),
			keyStyle.Render("t") + descStyle.Render(" hide TOC"),
			keyStyle.Render("q") + descStyle.Render(" close"),
		}
	} else {
		// Content-focused hints
		hints = []string{
			keyStyle.Render("←/→/Space") + descStyle.Render(" pages"),
			keyStyle.Render("j/k") + descStyle.Render(" scroll"),
			keyStyle.Render("Ctrl+d/u") + descStyle.Render(" half-page"),
			keyStyle.Render("t") + descStyle.Render(" TOC"),
			keyStyle.Render("q") + descStyle.Render(" close"),
		}
	}

	sep := sepStyle.Render(" │ ")
	return strings.Join(hints, sep)
}

// renderEmptyState renders a message when no pages are available.
func (m TutorialModel) renderEmptyState() string {
	r := m.theme.Renderer

	style := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(2, 4).
		Width(m.width)

	return style.Render("No tutorial pages available for this context.")
}

// NextPage advances to the next page.
func (m *TutorialModel) NextPage() {
	pages := m.visiblePages()
	if m.currentPage < len(pages)-1 {
		m.currentPage++
		m.scrollOffset = 0
	}
}

// PrevPage goes to the previous page.
func (m *TutorialModel) PrevPage() {
	if m.currentPage > 0 {
		m.currentPage--
		m.scrollOffset = 0
	}
}

// JumpToPage jumps to a specific page index.
func (m *TutorialModel) JumpToPage(index int) {
	pages := m.visiblePages()
	if index >= 0 && index < len(pages) {
		m.currentPage = index
		m.scrollOffset = 0
	}
}

// JumpToSection jumps to the first page in a section.
func (m *TutorialModel) JumpToSection(sectionID string) {
	pages := m.visiblePages()
	for i, page := range pages {
		if page.ID == sectionID || page.Section == sectionID {
			m.currentPage = i
			m.scrollOffset = 0
			return
		}
	}
}

// SetContext sets the current view context for filtering.
func (m *TutorialModel) SetContext(ctx string) {
	m.context = ctx
	// Reset to first page when context changes
	m.currentPage = 0
	m.scrollOffset = 0
}

// SetContextMode enables or disables context-based filtering.
func (m *TutorialModel) SetContextMode(enabled bool) {
	m.contextMode = enabled
	if enabled {
		m.currentPage = 0
		m.scrollOffset = 0
	}
}

// SetSize sets the tutorial dimensions and updates the markdown renderer.
func (m *TutorialModel) SetSize(width, height int) {
	m.width = width
	m.height = height

	// Update markdown renderer width to match content area
	contentWidth := width - 6 // padding and borders
	if m.tocVisible {
		contentWidth -= 24 // TOC sidebar width
	}
	if contentWidth < 40 {
		contentWidth = 40
	}

	if m.markdownRenderer != nil {
		m.markdownRenderer.SetWidthWithTheme(contentWidth, m.theme)
	}
}

// MarkViewed marks a page as viewed.
func (m *TutorialModel) MarkViewed(pageID string) {
	m.progress[pageID] = true
}

// Progress returns the progress map for persistence.
func (m TutorialModel) Progress() map[string]bool {
	return m.progress
}

// SetProgress restores progress from persistence.
func (m *TutorialModel) SetProgress(progress map[string]bool) {
	if progress != nil {
		m.progress = progress
	}
}

// CurrentPageID returns the ID of the current page.
func (m TutorialModel) CurrentPageID() string {
	pages := m.visiblePages()
	if m.currentPage >= 0 && m.currentPage < len(pages) {
		return pages[m.currentPage].ID
	}
	return ""
}

// IsComplete returns true if all pages have been viewed.
func (m TutorialModel) IsComplete() bool {
	pages := m.visiblePages()
	for _, page := range pages {
		if !m.progress[page.ID] {
			return false
		}
	}
	return len(pages) > 0
}

// ShouldClose returns true if user requested to close the tutorial (bv-wdsd).
func (m TutorialModel) ShouldClose() bool {
	return m.shouldClose
}

// ResetClose resets the close flag (call after handling close) (bv-wdsd).
func (m *TutorialModel) ResetClose() {
	m.shouldClose = false
}

// visiblePages returns pages filtered by context if contextMode is enabled.
func (m TutorialModel) visiblePages() []TutorialPage {
	if !m.contextMode || m.context == "" {
		return m.pages
	}

	var filtered []TutorialPage
	for _, page := range m.pages {
		// Include if no context restriction or matches current context
		if len(page.Contexts) == 0 {
			filtered = append(filtered, page)
			continue
		}
		for _, ctx := range page.Contexts {
			if ctx == m.context {
				filtered = append(filtered, page)
				break
			}
		}
	}
	return filtered
}

// CenterTutorial returns the tutorial view centered in the terminal.
func (m TutorialModel) CenterTutorial(termWidth, termHeight int) string {
	tutorial := m.View()

	// Get actual rendered dimensions
	tutorialWidth := lipgloss.Width(tutorial)
	tutorialHeight := lipgloss.Height(tutorial)

	// Calculate padding
	padTop := (termHeight - tutorialHeight) / 2
	padLeft := (termWidth - tutorialWidth) / 2

	if padTop < 0 {
		padTop = 0
	}
	if padLeft < 0 {
		padLeft = 0
	}

	r := m.theme.Renderer

	centered := r.NewStyle().
		MarginTop(padTop).
		MarginLeft(padLeft).
		Render(tutorial)

	return centered
}

// defaultTutorialPages returns the built-in tutorial content.
// Content organized by section - see bv-kdv2, bv-sbib, bv-36wz, etc.
func defaultTutorialPages() []TutorialPage {
	return []TutorialPage{
		// =============================================================
		// INTRODUCTION & PHILOSOPHY (bv-kdv2)
		// =============================================================
		{
			ID:      "intro-welcome",
			Title:   "Welcome",
			Section: "Introduction",
			Content: introWelcomeContent,
		},
		{
			ID:      "intro-philosophy",
			Title:   "The Beads Philosophy",
			Section: "Introduction",
			Content: introPhilosophyContent,
		},
		{
			ID:      "intro-audience",
			Title:   "Who Is This For?",
			Section: "Introduction",
			Content: introAudienceContent,
		},
		{
			ID:      "intro-quickstart",
			Title:   "Quick Start",
			Section: "Introduction",
			Content: introQuickstartContent,
		},

		// =============================================================
		// CORE CONCEPTS (bv-sbib)
		// =============================================================
		{
			ID:      "concepts-beads",
			Title:   "What Are Beads?",
			Section: "Core Concepts",
			Content: conceptsBeadsContent,
		},
		{
			ID:      "concepts-dependencies",
			Title:   "Dependencies & Blocking",
			Section: "Core Concepts",
			Content: conceptsDependenciesContent,
		},
		{
			ID:      "concepts-labels",
			Title:   "Labels & Organization",
			Section: "Core Concepts",
			Content: conceptsLabelsContent,
		},
		{
			ID:      "concepts-priorities",
			Title:   "Priorities & Status",
			Section: "Core Concepts",
			Content: conceptsPrioritiesContent,
		},
		{
			ID:      "concepts-graph",
			Title:   "The Dependency Graph",
			Section: "Core Concepts",
			Content: conceptsGraphContent,
		},

		// =============================================================
		// VIEWS & NAVIGATION (bv-36wz, bv-wra5, bv-h6jw)
		// =============================================================
		{
			ID:      "views-nav-fundamentals",
			Title:   "Navigation Fundamentals",
			Section: "Views",
			Content: viewsNavFundamentalsContent,
		},
		{
			ID:       "views-list",
			Title:    "List View",
			Section:  "Views",
			Contexts: []string{"list"},
			Content:  viewsListContent,
		},
		{
			ID:       "views-detail",
			Title:    "Detail View",
			Section:  "Views",
			Contexts: []string{"detail"},
			Content:  viewsDetailContent,
		},
		{
			ID:       "views-split",
			Title:    "Split View",
			Section:  "Views",
			Contexts: []string{"split"},
			Content:  viewsSplitContent,
		},
		{
			ID:       "views-board",
			Title:    "Board View",
			Section:  "Views",
			Contexts: []string{"board"},
			Content:  viewsBoardContent,
		},
		{
			ID:       "views-graph",
			Title:    "Graph View",
			Section:  "Views",
			Contexts: []string{"graph"},
			Content:  viewsGraphContent,
		},
		{
			ID:       "views-insights",
			Title:    "Insights Panel",
			Section:  "Views",
			Contexts: []string{"insights"},
			Content:  viewsInsightsContent,
		},
		{
			ID:       "views-history",
			Title:    "History View",
			Section:  "Views",
			Contexts: []string{"history"},
			Content:  viewsHistoryContent,
		},

		// =============================================================
		// ADVANCED FEATURES (bv-19gf)
		// =============================================================
		{
			ID:      "advanced-semantic-search",
			Title:   "Semantic + Hybrid Search",
			Section: "Advanced",
			Content: advancedSemanticSearchContent,
		},
		{
			ID:      "advanced-time-travel",
			Title:   "Time Travel",
			Section: "Advanced",
			Content: advancedTimeTravelContent,
		},
		{
			ID:      "advanced-label-analytics",
			Title:   "Label Analytics",
			Section: "Advanced",
			Content: advancedLabelAnalyticsContent,
		},
		{
			ID:      "advanced-export",
			Title:   "Export & Deployment",
			Section: "Advanced",
			Content: advancedExportContent,
		},
		{
			ID:      "advanced-workspace",
			Title:   "Workspace Mode",
			Section: "Advanced",
			Content: advancedWorkspaceContent,
		},
		{
			ID:      "advanced-recipes",
			Title:   "Recipes",
			Section: "Advanced",
			Content: advancedRecipesContent,
		},
		{
			ID:      "advanced-ai",
			Title:   "AI Agent Integration",
			Section: "Advanced",
			Content: advancedAIAgentContent,
		},

		// =============================================================
		// REAL-WORLD WORKFLOWS (bv-a2rv)
		// =============================================================
		{
			ID:      "workflow-new-feature",
			Title:   "Starting a New Feature",
			Section: "Workflows",
			Content: workflowNewFeatureContent,
		},
		{
			ID:      "workflow-bug-triage",
			Title:   "Triaging a Bug Report",
			Section: "Workflows",
			Content: workflowBugTriageContent,
		},
		{
			ID:      "workflow-sprint-planning",
			Title:   "Sprint Planning Session",
			Section: "Workflows",
			Content: workflowSprintPlanningContent,
		},
		{
			ID:      "workflow-onboarding",
			Title:   "Onboarding New Members",
			Section: "Workflows",
			Content: workflowOnboardingContent,
		},
		{
			ID:      "workflow-stakeholder-review",
			Title:   "Stakeholder Reviews",
			Section: "Workflows",
			Content: workflowStakeholderReviewContent,
		},

		// =============================================================
		// REFERENCE
		// =============================================================
		{
			ID:      "ref-keyboard",
			Title:   "Keyboard Reference",
			Section: "Reference",
			Content: `## Quick Keyboard Reference

### Global
| Key | Action |
|-----|--------|
| **?** | Help overlay |
| **q** | Quit |
| **Esc** | Close/go back |
| **b/g/i/h** | Switch views |

### Navigation
| Key | Action |
|-----|--------|
| **j/k** | Move down/up |
| **h/l** | Move left/right |
| **g/G** | Top/bottom |
| **Enter** | Select |

### Filtering
| Key | Action |
|-----|--------|
| **/** | Fuzzy search |
| **Ctrl+S** | Semantic search |
| **H** | Hybrid ranking |
| **Alt+H** | Hybrid preset |
| **o/c/r/a** | Status filter |

> Press **?** in any view for context help.`,
		},
	}
}

// =============================================================================
// INTRODUCTION & PHILOSOPHY CONTENT (bv-kdv2)
// =============================================================================

// introWelcomeContent is Page 1 of the Introduction section.
const introWelcomeContent = `## Welcome to beads_viewer

` + "```" + `
    ╭──────────────────────────────────────╮
    │      beads_viewer (bv)               │
    │  Issue tracking that lives in code   │
    ╰──────────────────────────────────────╯
` + "```" + `

**The problem:** You're deep in flow, coding away, when you need to check an issue.
You switch to a browser, navigate to your issue tracker, lose context,
and break your concentration.

**The solution:** ` + "`bv`" + ` brings issue tracking *into your terminal*, where you already work.
No browser tabs. No context switching. No cloud dependencies.

### The 30-Second Value Proposition

1. **Issues live in your repo** — version controlled, diffable, greppable
2. **Works offline** — no internet required, no accounts to manage
3. **AI-native** — designed for both humans and coding agents
4. **Zero dependencies** — just a single binary and your git repo

> Press **→** or **Space** to continue.`

// introPhilosophyContent is Page 2 of the Introduction section.
const introPhilosophyContent = `## The Beads Philosophy

Why "beads"? Think of git commits as **beads on a string** — each one a
discrete, meaningful step in your project's history.

Issues are beads too. They're snapshots of work: what needs doing, what's
in progress, what's complete. They belong *with your code*, not in some
external system.

### Core Principles

**1. Issues as First-Class Citizens**
Your ` + "`.beads/`" + ` directory is just as important as your ` + "`src/`" + `.
Issues get the same git treatment as code: branching, merging, history.

**2. No External Dependencies**
No servers to run. No accounts to create. No API keys to manage.
If you have git and a terminal, you have everything you need.

**3. Diffable and Greppable**
Issues are stored as plain JSONL. You can ` + "`git diff`" + ` your backlog.
You can ` + "`grep`" + ` for patterns across all issues.

**4. Human and Agent Readable**
The same data works for both humans (via ` + "`bv`" + `) and AI agents (via ` + "`--robot-*`" + ` flags).

> Press **→** to continue.`

// introAudienceContent is Page 3 of the Introduction section.
const introAudienceContent = `## Who Is This For?

### Solo Developers

Managing personal projects? Keep your TODO lists organized without
the overhead of heavyweight tools. Everything stays in your repo,
backs up with your code, and travels wherever you push.

### Small Teams

Want lightweight issue tracking without the subscription fees?
Share your ` + "`.beads/`" + ` directory through git. Everyone sees the same
state. No sync issues. No "who has the latest?"

### AI Coding Agents

This is where bv shines. AI agents like Claude, Cursor, and Codex
need structured task management. The ` + "`--robot-*`" + ` flags output
machine-readable formats perfect for agent consumption:

` + "```bash\nbv --robot-triage    # What should I work on?\nbv --robot-plan      # How can work be parallelized?\n```" + `

### Anyone Tired of Context-Switching

If you've ever lost your train of thought switching between your
editor and a web-based issue tracker, bv is for you. Stay in the
terminal. Stay in flow.

> Press **→** to continue.`

// introQuickstartContent is Page 4 of the Introduction section.
const introQuickstartContent = `## Quick Start

You're already running ` + "`bv`" + ` — you're ahead of the game!

### Basic Navigation

| Key | Action |
|-----|--------|
| **j / k** | Move down / up |
| **Enter** | Open issue details |
| **Esc** | Close overlay / go back |
| **q** | Quit bv |

### Switching Views

| Key | View |
|-----|------|
| **Esc** | Return to List |
| **b** | Board (Kanban) |
| **g** | Graph (dependencies) |
| **i** | Insights panel |
| **h** | History |

### Getting Help

| Key | What You Get |
|-----|--------------|
| **?** | Quick help overlay |
| **Space** (in help) | This tutorial |
| **` + "`" + `** (backtick) | Jump to tutorial |
| **~** (tilde) | Context-sensitive help |

### Next Steps

Try pressing **t** to see the Table of Contents for this tutorial.
Or press **q** to exit and start exploring!

> **Tip:** Press **?** anytime you need a quick reference.`

// =============================================================================
// CORE CONCEPTS CONTENT (bv-sbib)
// =============================================================================

// conceptsBeadsContent is Page 1 of the Core Concepts section.
const conceptsBeadsContent = `## What Are Beads?

A **bead** is an issue, task, or unit of work in your project. Think of your
project's work as beads on a string — discrete items that together form
the complete picture.

### Anatomy of a Bead

` + "```" + `
┌─────────────────────────────────────────────────────────┐
│ bv-abc123                               ← Unique ID     │
├─────────────────────────────────────────────────────────┤
│ Title: Fix authentication timeout                       │
│ Type: bug                  Status: open                 │
│ Priority: P1               Created: 2025-01-15          │
├─────────────────────────────────────────────────────────┤
│ Labels: auth, security, urgent                          │
├─────────────────────────────────────────────────────────┤
│ Description:                                            │
│ Users report being logged out after 5 minutes...        │
├─────────────────────────────────────────────────────────┤
│ Dependencies:                                           │
│   Blocks: bv-xyz789 (Production deploy)                 │
│   Blocked-by: (none)                                    │
└─────────────────────────────────────────────────────────┘
` + "```" + `

### Issue Types

| Type | When to Use |
|------|-------------|
| **bug** | Something broken that needs fixing |
| **feature** | New functionality to add |
| **task** | General work item |
| **epic** | Large initiative containing sub-tasks |
| **chore** | Maintenance, cleanup, tech debt |
| **docs** | Documentation work |

### How Beads Are Stored

In the primary ` + "`br`" + ` stack, your issues live in ` + "`.beads/beads.jsonl`" + `.
In modern ` + "`bd`" + ` workspaces, ` + "`bv`" + ` uses a compatibility export centered on ` + "`.beads/issues.jsonl`" + `.

The important point: ` + "`bv`" + ` always works from a local JSON Lines view:

` + "```" + `json
{"id":"bv-abc123","title":"Fix auth","type":"bug","priority":1,...}
{"id":"bv-def456","title":"Add dark mode","type":"feature",...}
` + "```" + `

This means your issues are:
- **Version controlled** — branch, merge, history
- **Diffable** — see exactly what changed
- **Greppable** — search with standard tools

> Press **→** to continue.`

// conceptsDependenciesContent is Page 2 of the Core Concepts section.
const conceptsDependenciesContent = `## Dependencies & Blocking

Not all work can happen in parallel. Some issues must wait for others.
This is where **dependencies** come in.

### The Relationship

` + "```" + `
    ┌───────────┐         ┌───────────┐
    │  bv-abc1  │ ──────► │  bv-def2  │
    │  (Auth)   │ blocks  │ (Deploy)  │
    └───────────┘         └───────────┘

    bv-abc1 BLOCKS bv-def2
    bv-def2 is BLOCKED BY bv-abc1
` + "```" + `

In plain terms: **You can't deploy until auth is fixed.**

### Visual Indicators

Throughout bv, blocking relationships are shown:

| Indicator | Meaning |
|-----------|---------|
| 🔴 | Blocked — waiting on something else |
| 🟢 | Ready — no blockers, can start now |
| **→** in detail | Shows what this issue blocks |
| **←** in detail | Shows what blocks this issue |

### The "Ready" Filter

Press **r** in List view to filter to **ready** issues only:

` + "```" + `
  Ready = Open + Zero Blockers
` + "```" + `

This is your **actionable work queue**. These issues have no dependencies
blocking them — you can start any of them right now.

> **Tip:** Start your day with ` + "`br ready`" + ` on the primary ` + "`br`" + ` stack, or ` + "`bd ready`" + ` when ` + "`bv`" + ` detects a ` + "`bd`" + ` workspace.

### Adding Dependencies

From the command line:

` + "```bash\nbr dep add bv-def2 bv-abc1   # def2 depends on abc1\n```" + `

This creates the blocking relationship shown above.

> Press **→** to continue.`

// conceptsLabelsContent is Page 3 of the Core Concepts section.
const conceptsLabelsContent = `## Labels & Organization

Labels provide **flexible categorization** that cuts across types
and priorities. Use them for anything that doesn't fit elsewhere.

### Common Label Patterns

| Category | Example Labels |
|----------|----------------|
| **Area** | frontend, backend, api, database |
| **Owner** | team-alpha, @alice, contractor |
| **Scope** | mvp, v2, tech-debt, nice-to-have |
| **State** | needs-review, blocked-external, waiting-response |

### Multi-Label Support

Issues can have multiple labels:

` + "```" + `
bv-abc123  [bug] [P1]  auth, security, needs-review
` + "```" + `

This issue is a high-priority auth bug that needs security review.

### Working with Labels

| Key | Action |
|-----|--------|
| **L** | Open label picker (apply labels) |
| **Shift+L** | Filter by label |
| **[** | Switch to Labels dashboard view |

### Label Analytics

The **Labels view** (press **[**) shows:
- Issue count per label
- Health indicators (stale issues, blockers)
- Distribution across priorities

` + "```" + `
┌─────────────────────────────────────────┐
│ Label         │ Open │ In Progress │ %  │
├───────────────┼──────┼─────────────┼────┤
│ frontend      │   12 │           3 │ 28%│
│ backend       │    8 │           5 │ 24%│
│ needs-review  │    6 │           0 │ 11%│
└─────────────────────────────────────────┘
` + "```" + `

> **Tip:** Keep your label set small. Too many labels = no one uses them.

> Press **→** to continue.`

// conceptsPrioritiesContent is Page 4 of the Core Concepts section.
const conceptsPrioritiesContent = `## Priorities & Status

Every issue has a **priority** and a **status**. Together, they answer:
"How important is this?" and "Where is it in the workflow?"

### Priority Levels

| Level | Meaning | Response Time |
|-------|---------|---------------|
| **P0** | Critical/emergency | Drop everything |
| **P1** | High priority | This sprint/week |
| **P2** | Medium priority | This cycle/month |
| **P3** | Low priority | When you have time |
| **P4** | Backlog | Someday/maybe |

> **Guideline:** If everything is P0, nothing is P0.

### Status Flow

` + "```" + `
   ┌──────┐     ┌─────────────┐     ┌────────┐
   │ open │ ──► │ in_progress │ ──► │ closed │
   └──────┘     └─────────────┘     └────────┘
                      │
                      ▼
                ┌─────────┐
                │ blocked │ (auto-detected from deps)
                └─────────┘
` + "```" + `

| Status | When to Use |
|--------|-------------|
| **open** | New or not yet started |
| **in_progress** | Actively being worked |
| **blocked** | Waiting on dependencies |
| **closed** | Complete or won't fix |

### Priority in the UI

The **Insights panel** (press **i**) calculates a priority score:

` + "```" + `
Priority Score = Base Priority + Blocking Factor + Freshness
` + "```" + `

- **Blocking Factor**: How many issues are waiting on this?
- **Freshness**: How long since last update?

This surfaces issues that are both important AND blocking other work.

### Changing Priority/Status

| Key | Action |
|-----|--------|
| **p** | Change priority |
| **s** | Change status |

Or from the command line:

` + "```bash\nbr update bv-abc123 --priority=P1\nbr update bv-abc123 --status=in_progress\n```" + `

> Press **→** to continue.`

// conceptsGraphContent is Page 5 of the Core Concepts section.
const conceptsGraphContent = `## The Dependency Graph

Your issues form a **directed acyclic graph (DAG)**. That sounds complex,
but the concept is simple: work flows in one direction, with no cycles.

### Mental Model

` + "```" + `
                    ┌─────────┐
                    │ bv-001  │  (Epic: User Auth)
                    │  EPIC   │
                    └────┬────┘
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
     ┌─────────┐   ┌─────────┐   ┌─────────┐
     │ bv-002  │   │ bv-003  │   │ bv-004  │
     │ Login   │   │ Signup  │   │ Reset   │
     └────┬────┘   └────┬────┘   └─────────┘
          │              │
          ▼              ▼
     ┌─────────┐   ┌─────────┐
     │ bv-005  │   │ bv-006  │
     │ Tests   │   │ Tests   │
     └─────────┘   └─────────┘

     Arrows flow DOWN toward what's blocked.
` + "```" + `

### Key Insights from the Graph

1. **Root nodes** (no arrows in) — Can be started immediately
2. **Leaf nodes** (no arrows out) — Nothing depends on them
3. **High fan-out** — Completing this unblocks many items
4. **Critical path** — The longest chain determines minimum time

### Graph View (Press g)

The **Graph view** visualizes these relationships:

| Visual | Meaning |
|--------|---------|
| Node size | Priority (bigger = higher) |
| Green node | Closed |
| Blue node | In progress |
| Red node | Blocked |
| Arrow A→B | A blocks B |

### Navigation in Graph View

| Key | Action |
|-----|--------|
| **j/k** | Move between nodes vertically |
| **h/l** | Move between siblings |
| **f** | Focus on selected subgraph |
| **Enter** | View selected issue |
| **Esc** | Return to list |

### Why This Matters

The graph reveals:
- **Bottlenecks**: One issue blocking many
- **Parallel tracks**: Independent work streams
- **Priority inversions**: Low-priority blocking high-priority

> **Tip:** Use ` + "`br blocked`" + ` to quickly see all blocked issues.

> Press **→** to continue to Views & Navigation.`

// =============================================================================
// VIEWS & NAVIGATION CONTENT (bv-36wz)
// =============================================================================

// viewsNavFundamentalsContent is Page 1 of the Views section.
const viewsNavFundamentalsContent = `## Navigation Fundamentals

bv uses **vim-style navigation** throughout. If you know vim, you're already
at home. If not, you'll pick it up in minutes.

### Core Movement

| Key | Action |
|-----|--------|
| **j** | Move down |
| **k** | Move up |
| **h** | Move left (in multi-column views) |
| **l** | Move right (in multi-column views) |

### Jump Commands

| Key | Action |
|-----|--------|
| **g** | Jump to top |
| **G** | Jump to bottom |
| **Ctrl+d** | Half-page down |
| **Ctrl+u** | Half-page up |

### Universal Keys

These work in every view:

| Key | Action |
|-----|--------|
| **?** | Help overlay |
| **Esc** | Close overlay / go back |
| **Enter** | Select / open |
| **q** | Quit bv |

### The Shortcuts Sidebar

Press **;** (semicolon) to toggle a floating sidebar showing all available
shortcuts for your current view. It updates as you navigate.

> Press **→** to continue.`

// viewsListContent is the List View page content.
const viewsListContent = `## List View

The **List view** is your issue inbox — where you'll spend most of your time.

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ bv-abc1  [P1] [bug] Fix login timeout              │ ← selected
│ bv-def2  [P2] [feature] Add dark mode              │
│ bv-ghi3  [P2] [task] Update dependencies           │
│ bv-jkl4  [P3] [chore] Clean up test fixtures       │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Filtering

Quickly narrow down what you see:

| Key | Filter |
|-----|--------|
| **o** | Open issues only |
| **c** | Closed issues only |
| **r** | Ready issues (no blockers) |
| **a** | All issues (reset filter) |

### Searching

| Key | Search Type |
|-----|-------------|
| **/** | Fuzzy search (fast, typo-tolerant) |
| **Ctrl+S** | Semantic search (meaning-based) |
| **H** | Hybrid ranking (semantic + graph) |
| **Alt+H** | Cycle hybrid preset |
| **n/N** | Next/previous search result |

### Sorting

Press **s** to cycle through sort modes: priority → created → updated.
Press **S** (shift+s) to reverse the current sort order.

### When to Use List View

- Daily triage: filter to ` + "`r`" + ` (ready) and work top-down
- Quick status check: filter to ` + "`o`" + ` (open) to see backlog size
- Finding specific issues: use **/** or **Ctrl+S** to search

> Press **→** to continue.`

// viewsDetailContent is the Detail View page content.
const viewsDetailContent = `## Detail View

Press **Enter** on any issue to see its full details.

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ bv-abc1: Fix login timeout                         │
├─────────────────────────────────────────────────────┤
│ Status: open          Priority: P1                 │
│ Type: bug             Created: 2025-01-15          │
├─────────────────────────────────────────────────────┤
│                                                     │
│ ## Description                                      │
│                                                     │
│ Users report being logged out after 5 minutes      │
│ of inactivity. Should be 30 minutes per spec.      │
│                                                     │
│ ## Dependencies                                     │
│ Blocks: bv-xyz9 (Deploy to production)             │
│                                                     │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Detail View Actions

| Key | Action |
|-----|--------|
| **O** | Open/edit in external editor |
| **C** | Copy issue ID to clipboard |
| **j/k** | Scroll content up/down |
| **Esc** | Return to list |

### Markdown Rendering

Issue descriptions are rendered with full markdown support:
- Headers, bold, italic, code blocks
- Lists and tables
- Links (displayed but not clickable in terminal)

> Press **→** to continue.`

// viewsSplitContent is the Split View page content.
const viewsSplitContent = `## Split View

Press **Tab** from Detail view to enter Split view — list and detail side by side.

` + "```" + `
┌────────────────────┬────────────────────────────────┐
│ bv-abc1 [P1] bug   │ bv-abc1: Fix login timeout     │
│ bv-def2 [P2] feat  │ ────────────────────────────── │
│ bv-ghi3 [P2] task  │ Status: open    Priority: P1   │
│ bv-jkl4 [P3] chore │                                │
│                    │ ## Description                 │
│                    │ Users report being logged...   │
└────────────────────┴────────────────────────────────┘
` + "```" + `

### Split View Navigation

| Key | Action |
|-----|--------|
| **Tab** | Switch focus between panes |
| **j/k** | Navigate in focused pane |
| **Esc** | Return to full list |

### When to Use Split View

- **Code review**: Quickly scan multiple related issues
- **Triage session**: Read details without losing list context
- **Dependency analysis**: Navigate while viewing relationships

> **Tip:** The detail pane auto-updates as you navigate the list.

> Press **→** to continue.`

// viewsBoardContent is the Board View page content.
const viewsBoardContent = `## Board View

Press **b** to switch to the Kanban-style board.

` + "```" + `
┌─────────────┬─────────────┬─────────────┬─────────────┐
│    OPEN     │ IN PROGRESS │   BLOCKED   │   CLOSED    │
├─────────────┼─────────────┼─────────────┼─────────────┤
│ bv-abc1     │ bv-mno7     │ bv-stu0     │ bv-vwx1     │
│ bv-def2     │             │             │ bv-yza2     │
│ bv-ghi3     │             │             │ bv-bcd3     │
│ bv-jkl4     │             │             │             │
└─────────────┴─────────────┴─────────────┴─────────────┘
` + "```" + `

### Board Navigation

| Key | Action |
|-----|--------|
| **h/l** | Move between columns |
| **j/k** | Move within a column |
| **Enter** | View issue details |
| **m** | Move issue to different status |

### Visual Indicators

- Card height indicates description length
- Priority shown with color intensity
- Blocked issues appear in the BLOCKED column automatically

### When to Use Board View

- **Sprint planning**: Visualize work distribution
- **Standups**: Quick status overview
- **Bottleneck detection**: Spot column imbalances

> Press **→** to continue.`

// viewsGraphContent is the Graph View page content.
const viewsGraphContent = `## Graph View

Press **g** to visualize issue dependencies as a graph.

` + "```" + `
                    ┌─────────┐
                    │ bv-abc1 │
                    └────┬────┘
                         │
              ┌──────────┼──────────┐
              ▼          ▼          ▼
         ┌─────────┐ ┌─────────┐ ┌─────────┐
         │ bv-def2 │ │ bv-ghi3 │ │ bv-jkl4 │
         └────┬────┘ └─────────┘ └────┬────┘
              │                       │
              ▼                       ▼
         ┌─────────┐            ┌─────────┐
         │ bv-mno5 │            │ bv-pqr6 │
         └─────────┘            └─────────┘
` + "```" + `

### Reading the Graph

- **Arrows point TO dependencies** (A → B means A *blocks* B)
- **Node size** reflects priority
- **Color** indicates status (green=closed, blue=in_progress, etc.)
- **Highlighted node** is your current selection

### Graph Navigation

| Key | Action |
|-----|--------|
| **j/k** | Navigate between connected nodes |
| **h/l** | Navigate siblings |
| **Enter** | Select node and view details |
| **f** | Focus: show only this node's subgraph |
| **Esc** | Exit focus / return to list |

### When to Use Graph View

- **Critical path analysis**: Find what's blocking important work
- **Dependency planning**: Understand execution order
- **Impact assessment**: See what closing an issue unblocks

> Press **→** to continue.`

// viewsInsightsContent is the Insights Panel page content.
const viewsInsightsContent = `## Insights Panel

Press **i** to open the Insights panel — AI-powered prioritization assistance.

### Priority Score Algorithm

Each issue gets a computed **priority score** based on:

1. **Explicit priority** (P0-P4)
2. **Blocking factor** — how many issues it unblocks
3. **Freshness** — recently updated issues score higher
4. **Type weight** — bugs often prioritized over features

### Attention Scores

The panel highlights issues that may need attention:

- **Stale issues**: Open for too long without updates
- **Blocked chains**: Issues creating bottlenecks
- **Priority inversions**: Low-priority items blocking high-priority

### Visual Heatmap

Press **m** to toggle heatmap mode, which colors the list by:
- Red = high attention needed
- Yellow = moderate
- Green = on track

### When to Use Insights

- **Weekly review**: Find neglected issues
- **Sprint planning**: Data-driven prioritization
- **Bottleneck hunting**: Identify blocking patterns

> Press **→** to continue.`

// viewsHistoryContent is the History View page content.
const viewsHistoryContent = `## History View

Press **h** to see the git-integrated timeline of your project.

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ 2025-01-15 14:32  abc1234  feat: Add login flow    │
│   └─ bv-abc1 opened, bv-def2 closed                │
│                                                     │
│ 2025-01-15 10:15  def5678  fix: Timeout issue      │
│   └─ bv-ghi3 status → in_progress                  │
│                                                     │
│ 2025-01-14 16:45  ghi9012  chore: Bump deps        │
│   └─ (no bead changes)                             │
└─────────────────────────────────────────────────────┘
` + "```" + `

### History Features

- **Git commits** with associated bead changes
- **Bead-only changes** from ` + "`br`" + ` commands
- **Time-travel preview**: See project state at any point

### History Navigation

| Key | Action |
|-----|--------|
| **j/k** | Navigate timeline |
| **Enter** | Preview project state at that commit |
| **d** | Show diff for selected commit |
| **Esc** | Return to current state |

### Time Travel

When you press **Enter** on a historical commit, bv shows you:
- What issues existed at that moment
- Their status at that time
- The dependency graph as it was

This is read-only — you're viewing the past, not changing it.

> **Use case:** "What was our backlog like before the big refactor?"`

// =============================================================================
// ADVANCED FEATURES CONTENT (bv-19gf)
// =============================================================================

// advancedSemanticSearchContent is the Semantic Search tutorial page.
const advancedSemanticSearchContent = `## Semantic + Hybrid Search

Semantic search finds issues by **meaning**, not just exact words. Hybrid
ranking keeps those results relevant while surfacing items with higher
impact in the dependency graph.

### Search Modes

| Mode | Key | What it does |
|------|-----|-------------|
| Fuzzy | **/** | Literal text match (fast) |
| Semantic | **Ctrl+S** | Meaning-based retrieval |
| Hybrid | **H** | Semantic + graph-aware ranking |
| Preset | **Alt+H** | Cycle hybrid presets |

### How It Works

1. **Semantic retrieval** builds a local vector index from weighted issue text.
   IDs and titles carry extra weight so exact lookups still win.
2. **Hybrid ranking** re-scores those candidates using PageRank, status,
   impact, priority, and recency—so the most important matches rise.
3. **Short queries** (e.g. "benchmarks") get a literal-match boost and a
   wider candidate pool for precise, fast lookups.

### Example

Searching for "permissions":

- **Fuzzy** finds issues containing the word permissions
- **Semantic** finds access control, roles, authorization, ACLs
- **Hybrid** floats the items that block other work

### When to Use It

- **Exploratory**: "What do we have about performance?"
- **Conceptual**: "auth bugs" / "rate limiting" / "retry logic"
- **Prioritize**: Use **H** when you want the most important matches

### Tuning (Optional)

` + "```bash\nBV_SEARCH_MODE=hybrid\nBV_SEARCH_PRESET=impact-first\nBV_SEARCH_WEIGHTS='{\"text\":0.4,\"pagerank\":0.2,\"status\":0.15,\"impact\":0.1,\"priority\":0.1,\"recency\":0.05}'\n```" + `

> Press **→** to continue.`

// advancedTimeTravelContent is the Time Travel tutorial page.
const advancedTimeTravelContent = `## Time Travel (t/T)

See how your project looked at any point in history — compare past to present.

### Accessing Time Travel

| Key | Action |
|-----|--------|
| **t** | Full time travel with git ref input |
| **T** | Quick time travel to HEAD~5 |
| **h** | History view (visual timeline) |

### Git Reference Syntax

Time travel understands git references:

| Reference | Meaning |
|-----------|---------|
| ` + "`HEAD~5`" + ` | 5 commits ago |
| ` + "`main`" + ` | Tip of main branch |
| ` + "`v1.2.0`" + ` | Tagged release |
| ` + "`@{2.weeks.ago}`" + ` | Two weeks back |

### Example: Sprint Retrospective

` + "```" + `
1. Press T and enter "HEAD~50" (start of sprint)
2. See: 45 open issues, 12 blocked
3. Press Esc to return to present
4. Now: 22 open, 3 blocked
5. Diff shows: 23 closed, 9 unblocked!
` + "```" + `

### The Diff View

When in time-travel mode, press **d** to see changes:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ Changes: HEAD~50 → HEAD                             │
├─────────────────────────────────────────────────────┤
│ + Added: 8 issues                                   │
│ - Closed: 23 issues                                 │
│ ~ Modified: 12 issues                               │
│                                                     │
│ Cycles: 1 resolved, 0 introduced                    │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Use Cases

- **Sprint review**: "What did we accomplish?"
- **Debugging**: "When did this issue get blocked?"
- **Onboarding**: "What was the project like 6 months ago?"
- **Post-mortem**: "Show me the state when the bug was introduced"

### Robot Mode Equivalent

` + "```bash\nbv --robot-diff --diff-since HEAD~50\n```" + `

Returns structured JSON with added/closed/modified counts.

> Press **→** to continue.`

// advancedLabelAnalyticsContent is the Label Analytics tutorial page.
const advancedLabelAnalyticsContent = `## Label Analytics

Labels are more than tags — they're a lens for understanding your project.

### The Labels Dashboard ([)

Press **[** to open the Labels dashboard:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ LABEL           │ OPEN │ PROG │ BLOCK │ HEALTH     │
├─────────────────┼──────┼──────┼───────┼────────────┤
│ frontend        │   12 │    3 │     2 │ ▓▓▓▓▓░░░ ⚠ │
│ backend         │    8 │    5 │     0 │ ▓▓▓▓▓▓▓░ ✓ │
│ security        │    4 │    1 │     3 │ ▓▓░░░░░░ ⛔ │
│ tech-debt       │   15 │    0 │     0 │ ▓▓▓░░░░░ ⚠ │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Health Indicators

| Symbol | Meaning |
|--------|---------|
| ✓ | Healthy — good progress, few blockers |
| ⚠ | Warning — stale items or slow velocity |
| ⛔ | Critical — high blocked ratio, needs attention |

### Health Score Factors

1. **Velocity**: How fast are issues closing?
2. **Staleness**: Are old issues piling up?
3. **Blocked ratio**: What % is stuck?
4. **Work distribution**: Is work spread evenly?

### Label Flow Analysis

Press **f** in Labels view to see cross-label flow:

` + "```" + `
frontend → backend: 5 dependencies
backend → database: 3 dependencies
security → * : 2 dependencies (blocks many areas)
` + "```" + `

This reveals **architectural coupling** — which areas block others.

### Robot Mode Commands

` + "```bash\nbv --robot-label-health              # Health metrics\nbv --robot-label-flow                # Cross-label deps\nbv --robot-label-attention --limit=5 # Top labels needing work\n```" + `

### Strategic Use

- **Balanced labels**: Similar health across areas = smooth progress
- **Bottleneck labels**: High block count = address first
- **Orphan labels**: Zero activity = maybe archive them

> Press **→** to continue.`

// advancedExportContent is the Export & Deployment tutorial page.
const advancedExportContent = `## Export & Deployment

Share your project status with people who don't use the terminal.

### Quick Markdown Export (x)

Press **x** in any view to export current state to markdown:

` + "```markdown\n# Project Status - 2025-01-15\n\n## Open Issues (24)\n| ID | Priority | Title |\n|----|----------|-------|\n| bv-abc1 | P1 | Fix login timeout |\n...\n\n## Blocked Issues (5)\n...\n```" + `

Great for:
- Pasting into Slack/Discord
- Email updates
- Meeting notes

### Static Site Generation (--pages)

Generate a complete web dashboard:

` + "```bash\nbv --pages                              # Interactive wizard\nbv --export-pages ./dashboard           # Export to directory\nbv --preview-pages ./dashboard          # Preview locally\n```" + `

The output is **self-contained HTML** that works offline:
- Triage recommendations
- Dependency graph visualization
- Full-text search
- No server required

### Deployment Options

**GitHub Pages** (via wizard):

` + "```bash\nbv --pages\n# Select: GitHub Pages\n# Follow prompts to configure\n```" + `

**Cloudflare Pages** (manual):

` + "```bash\nbv --export-pages ./dashboard --pages-title \"Sprint Status\"\n# Upload ./dashboard to Cloudflare Pages\n```" + `

**Local sharing**:

` + "```bash\nbv --export-pages ./share\n# Zip and send, or serve with any HTTP server\nnpx serve ./share\n```" + `

### CI/CD Integration

` + "```yaml\n# .github/workflows/dashboard.yml\njobs:\n  deploy:\n    steps:\n      - run: bv --export-pages ./pages --pages-title \"${{ github.ref_name }}\"\n      - uses: peaceiris/actions-gh-pages@v3\n        with:\n          publish_dir: ./pages\n```" + `

> Press **→** to continue.`

// advancedWorkspaceContent is the Workspace Mode tutorial page.
const advancedWorkspaceContent = `## Workspace Mode

Manage multiple repositories as a single unified project.

### When to Use Workspaces

- **Monorepo alternatives**: Multiple related repos
- **Microservices**: Track issues across services
- **Frontend + Backend**: Separate repos, unified view

### Setting Up a Workspace

Create a ` + "`.beads/workspace.json`" + `:

` + "```json\n{\n  \"name\": \"My Product\",\n  \"repos\": [\n    { \"path\": \"../frontend\", \"prefix\": \"fe\" },\n    { \"path\": \"../backend\", \"prefix\": \"be\" },\n    { \"path\": \"../shared\", \"prefix\": \"sh\" }\n  ]\n}\n```" + `

### Workspace Navigation

| Key | Action |
|-----|--------|
| **w** | Toggle workspace picker |
| **W** | Workspace-wide search |

### Aggregated Views

In workspace mode, all views aggregate across repos:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ WORKSPACE: My Product (3 repos)                     │
├─────────────────────────────────────────────────────┤
│ fe-abc1  [P1] [bug] Frontend: Fix modal             │
│ be-def2  [P1] [bug] Backend: API timeout            │
│ sh-ghi3  [P2] [task] Shared: Update types           │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Cross-Repo Dependencies

Issues can depend on issues in other repos:

` + "```bash\nbr dep add fe-abc1 be-def2   # Frontend blocked by backend\n```" + `

The graph view shows these cross-repo relationships.

### Filtering by Repo

Press **w** to open the repo picker, then:

| Key | Effect |
|-----|--------|
| **j/k** | Navigate repos |
| **Space** | Toggle repo selection |
| **Enter** | Apply filter |
| **a** | Select all repos |

### Robot Mode

` + "```bash\nbv --robot-triage              # Workspace-wide triage\nbv --robot-plan               # Cross-repo execution plan\n```" + `

> **Note:** Workspace mode requires all repos to be accessible locally.

> Press **→** to continue.`

// advancedRecipesContent is the Recipes tutorial page.
const advancedRecipesContent = `## Recipes (R)

Recipes are **saved filter combinations** — complex queries you use repeatedly.

### Opening the Recipe Picker

Press **R** (capital R) to open recipes:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ RECIPES                                             │
├─────────────────────────────────────────────────────┤
│ ▶ Sprint Ready                                      │
│   Open + Ready + Priority ≤ P2                      │
│                                                     │
│   This Week's Focus                                 │
│   In Progress OR (Open + P0-P1)                     │
│                                                     │
│   Blocked Review                                    │
│   Blocked + Updated > 3 days ago                    │
│                                                     │
│   Tech Debt Candidates                              │
│   Labels: tech-debt + Priority ≥ P3                 │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Built-in Recipes

| Recipe | What It Shows |
|--------|---------------|
| **Sprint Ready** | Actionable work for this sprint |
| **Quick Wins** | Low-effort items (small scope) |
| **Blocked Review** | Stuck items needing attention |
| **High Impact** | Top PageRank scores (graph centrality) |
| **Stale Items** | No updates in 2+ weeks |

### Creating Custom Recipes

Recipes are stored in ` + "`.beads/recipes.json`" + `:

` + "```json\n{\n  \"recipes\": [\n    {\n      \"name\": \"My Team's Work\",\n      \"filter\": {\n        \"labels\": [\"team-alpha\"],\n        \"status\": [\"open\", \"in_progress\"],\n        \"priority_max\": 2\n      },\n      \"sort\": \"priority\"\n    }\n  ]\n}\n```" + `

### Recipe Filters

Available filter options:

| Field | Example |
|-------|---------|
| ` + "`status`" + ` | ` + "`[\"open\", \"in_progress\"]`" + ` |
| ` + "`labels`" + ` | ` + "`[\"frontend\", \"urgent\"]`" + ` |
| ` + "`labels_exclude`" + ` | ` + "`[\"wontfix\"]`" + ` |
| ` + "`priority_min`" + ` | ` + "`0`" + ` (P0 or higher) |
| ` + "`priority_max`" + ` | ` + "`2`" + ` (P2 or lower) |
| ` + "`type`" + ` | ` + "`[\"bug\", \"feature\"]`" + ` |
| ` + "`assignee`" + ` | ` + "`\"@alice\"`" + ` |

### Sharing Recipes

Since recipes live in ` + "`.beads/`" + `, they're version controlled:

- Commit your recipes to share with team
- Different branches can have different recipes
- Pull request to propose new team recipes

### Robot Mode

` + "```bash\nbv --recipe \"Sprint Ready\" --robot-triage\n```" + `

Apply any recipe as a pre-filter for robot commands.

> Press **→** to continue.`

// advancedAIAgentContent is the AI Agent Integration tutorial page.
const advancedAIAgentContent = `## AI Agent Integration

bv is designed to work with **AI coding agents** — Claude, GPT, Codex, etc.

### The Robot Mode Philosophy

Regular bv is for humans. **Robot mode** is for agents:

| Human | Agent |
|-------|-------|
| ` + "`bv`" + ` (interactive TUI) | ` + "`bv --robot-*`" + ` (JSON output) |
| Visual navigation | Structured data parsing |
| Keyboard shortcuts | Command flags |

### Key Robot Commands

` + "```bash\n# The mega-command: start here\nbv --robot-triage\n\n# Quick picks\nbv --robot-next         # Single top priority item\nbv --robot-plan         # Parallel execution tracks\n\n# Deep analysis\nbv --robot-insights     # PageRank, cycles, bottlenecks\nbv --robot-alerts       # Stale items, priority inversions\n```" + `

### What --robot-triage Returns

` + "```json\n{\n  \"quick_ref\": {\n    \"open_count\": 24,\n    \"actionable_count\": 18,\n    \"top_picks\": [...]\n  },\n  \"recommendations\": [\n    {\n      \"id\": \"bv-abc1\",\n      \"score\": 0.85,\n      \"reasons\": [\"High PageRank\", \"Unblocks 3 items\"],\n      \"action\": \"work\"\n    }\n  ],\n  \"quick_wins\": [...],\n  \"blockers_to_clear\": [...]\n}\n```" + `

### Agent Workflow Example

` + "```" + `
1. Agent calls: bv --robot-next
2. Receives: { "id": "bv-abc1", "title": "Fix auth" }
3. Agent runs: br update bv-abc1 --status=in_progress
4. Agent does the work...
5. Agent runs: br close bv-abc1
6. Agent calls: bv --robot-next (repeat)
` + "```" + `

### The Beads CLI (for Agents)

| Command | Purpose |
|---------|---------|
| ` + "`br ready`" + ` / ` + "`bd ready`" + ` | List actionable issues |
| ` + "`br update <id> --status=in_progress`" + ` / ` + "`bd update <id> --status=in_progress`" + ` | Claim work |
| ` + "`br close <id>`" + ` / ` + "`bd close <id>`" + ` | Complete work |
| ` + "`br sync`" + ` / ` + "`bd export -o .beads/issues.jsonl`" + ` | Refresh the JSONL view |

### AGENTS.md Integration

Every project should have an ` + "`AGENTS.md`" + ` file explaining:
- How to use bv robot commands
- Project-specific workflows
- Integration with other tools

See this project's AGENTS.md for a complete example.

> Press **→** to continue to Workflows section.`

// =============================================================================
// REAL-WORLD WORKFLOWS CONTENT (bv-a2rv)
// =============================================================================

// workflowNewFeatureContent is the New Feature workflow tutorial page.
const workflowNewFeatureContent = `## Workflow: Starting a New Feature

Let's walk through implementing a feature from start to finish.

### Step 1: Find Available Work

` + "```bash\nbr ready                        # Show actionable issues\nbv --robot-triage | jq '.recommendations[0]'\n```" + `

Or in bv: press **r** to filter to ready issues.

### Step 2: Review the Issue

` + "```" + `
j/k   Navigate to the feature
Enter View full details
g     See dependency graph
` + "```" + `

Check: Is anything blocking this? Are there related tasks?

### Step 3: Claim the Work

` + "```bash\nbr update bv-xyz1 --status=in_progress\n```" + `

The issue moves to "In Progress" — other agents/devs know it's claimed.

### Step 4: Discover Sub-Tasks

As you work, you realize there are sub-tasks:

` + "```bash\nbr create --title=\"Implement auth logic\" --type=task --priority=2\nbr create --title=\"Add API endpoint\" --type=task --priority=2\nbr create --title=\"Write tests\" --type=task --priority=2\n\n# Set dependencies\nbr dep add bv-tests bv-endpoint   # Tests depend on endpoint\nbr dep add bv-endpoint bv-auth    # Endpoint depends on auth\n```" + `

### Step 5: Work Through Sub-Tasks

` + "```bash\n# Start first sub-task\nbr update bv-auth --status=in_progress\n# ... do the work ...\nbr close bv-auth\n\n# Endpoint is now unblocked!\nbr update bv-endpoint --status=in_progress\n# ... continue ...\n```" + `

### Step 6: Complete and Sync

` + "```bash\nbr close bv-xyz1              # Close parent feature\nbr sync                        # Commit all changes to git\n```" + `

### Pro Tips

- **Check ` + "`br ready`" + `** after each close — new work may have unblocked
- **Use ` + "`g`" + ` (graph view)** to visualize the sub-task structure
- **Set realistic priorities** — P2 for standard work, P1 only for blockers

> Press **→** to continue.`

// workflowBugTriageContent is the Bug Triage workflow tutorial page.
const workflowBugTriageContent = `## Workflow: Triaging a Bug Report

When a bug comes in, here's how to triage it efficiently.

### Step 1: Receive the Bug

Bug arrives (from user, agent, or monitoring):
- "Login fails for users with special characters in email"

` + "```bash\nbr create --title=\"Login fails with special chars in email\" \\\n  --type=bug --priority=2\n```" + `

### Step 2: Assess Severity

In bv, select the new issue and press **S** for triage suggestions:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ TRIAGE SUGGESTIONS                                  │
├─────────────────────────────────────────────────────┤
│ Priority: P1 (blocks user workflows)                │
│ Labels: auth, bug, user-reported                    │
│ Similar: bv-def2 "Email validation issue"           │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Step 3: Set Priority Based on Severity

| Severity | Priority | When to Use |
|----------|----------|-------------|
| Critical | P0 | System down, data loss |
| High | P1 | Major feature broken |
| Medium | P2 | Feature degraded |
| Low | P3-P4 | Minor, cosmetic |

` + "```bash\nbr update bv-bug1 --priority=1   # This is P1 - blocks logins\n```" + `

### Step 4: Add Labels for Categorization

Press **L** to open label picker, select:
- ` + "`bug`" + ` - It's a bug
- ` + "`auth`" + ` - Affects authentication
- ` + "`user-reported`" + ` - External report

### Step 5: Check for Blockers

Does this bug block other work?

` + "```bash\nbr dep add bv-feature1 bv-bug1  # Feature is blocked by this bug\n```" + `

Now bv-feature1 won't show in ` + "`br ready`" + ` until the bug is fixed.

### Step 6: Assign or Leave for Pickup

Option A: Assign to someone
` + "```bash\nbr update bv-bug1 --assignee=@alice\n```" + `

Option B: Leave unassigned
- High-priority bugs surface in ` + "`br ready`" + ` automatically
- The triage system will recommend them

### Summary Checklist

` + "```" + `
[ ] Create issue with descriptive title
[ ] Set priority based on severity
[ ] Add relevant labels
[ ] Check if it blocks other work
[ ] Assign or leave for ready queue
` + "```" + `

> Press **→** to continue.`

// workflowSprintPlanningContent is the Sprint Planning workflow tutorial page.
const workflowSprintPlanningContent = `## Workflow: Sprint Planning Session

Use bv's analytics to make data-driven sprint decisions.

### Step 1: Review Project Health

Open the Insights panel with **i**:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ PROJECT HEALTH                                      │
├─────────────────────────────────────────────────────┤
│ Open: 45   In Progress: 8   Blocked: 12            │
│                                                     │
│ Top Blockers (unblock most work):                   │
│   bv-auth  → would unblock 5 items                  │
│   bv-api   → would unblock 3 items                  │
│                                                     │
│ Priority Distribution:                              │
│   P0: 2   P1: 8   P2: 25   P3+: 18                  │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Step 2: Identify Dependencies

Press **g** for the graph view to see the dependency structure:

- **Tall chains** = sequential work (can't parallelize)
- **Wide clusters** = parallel opportunities
- **Bottlenecks** = single nodes blocking many

### Step 3: Filter to Ready Work

Press **r** to show only unblocked issues:

` + "```" + `
┌─────────────────────────────────────────────────────┐
│ READY ISSUES (18 actionable)                        │
├─────────────────────────────────────────────────────┤
│ ▶ [P1] bv-abc1  Fix auth timeout                    │
│   [P1] bv-def2  API rate limiting                   │
│   [P2] bv-ghi3  Dashboard redesign                  │
│   ...                                               │
└─────────────────────────────────────────────────────┘
` + "```" + `

### Step 4: Discuss and Assign

For each sprint candidate:
1. Review in detail view (Enter)
2. Discuss scope and estimates
3. Add sprint label: **L** → "sprint-42"
4. Optionally assign: update with --assignee

### Step 5: Export Sprint Plan

Press **x** to export the filtered list to markdown:

` + "```markdown\n# Sprint 42 Plan\n\n## P1 - Must Complete\n- [ ] bv-abc1: Fix auth timeout\n- [ ] bv-def2: API rate limiting\n\n## P2 - Should Complete  \n- [ ] bv-ghi3: Dashboard redesign\n...\n```" + `

Share in Slack, email, or sprint planning doc.

### Step 6: Time-Travel at Sprint End

After the sprint, compare progress:

` + "```bash\n# Press t, enter: HEAD~50 (start of sprint)\n# Or use robot mode:\nbv --robot-diff --diff-since HEAD~50\n```" + `

See exactly how many issues closed, what unblocked, velocity achieved.

> Press **→** to continue.`

// workflowOnboardingContent is the Onboarding workflow tutorial page.
const workflowOnboardingContent = `## Workflow: Onboarding a New Team Member

bv makes onboarding fast because it's embedded in the repo.

### Step 1: It's Already There

When they clone the repo:

` + "```bash\ngit clone https://github.com/your-org/project\ncd project\nbv                    # Tutorial launches automatically!\n```" + `

No separate tool installation. No access requests to external systems.

### Step 2: Point to Help Resources

Tell them about the help system:

| Key | What They Get |
|-----|---------------|
| **?** | Quick reference overlay |
| **` + "`" + `** | Full interactive tutorial |
| **;** | Shortcuts sidebar |

> "If you forget anything, just press ? or ` + "`" + `"

### Step 3: First Task Assignment

Find a good starter issue:

` + "```bash\n# In bv: press L, select \"good-first-issue\" label\nbr list --label=good-first-issue --status=open\n```" + `

Starter issues should:
- Have clear scope
- Minimal dependencies
- Not be on critical path

### Step 4: Walk Through Their First Workflow

Guide them through:

1. **Find the issue**: Use filters (o/r) and search (/)
2. **Review details**: Press Enter to see full description
3. **Check dependencies**: Press g for graph view
4. **Claim it**: ` + "`br update ID --status=in_progress`" + `
5. **Do the work**: Regular development process
6. **Close it**: ` + "`br close ID`" + `
7. **Sync**: ` + "`br sync`" + ` commits everything

### Step 5: Explain the Mental Model

Key concepts for new team members:

- **Beads = issues** stored in the repo itself
- **Dependencies** are first-class — graph shows what blocks what
- **Ready filter** (r) shows only actionable work
- **Everything syncs** via git — no external databases

### Onboarding Checklist

` + "```" + `
[ ] Clone repo, verify bv runs
[ ] Complete tutorial (or at least Quick Start)
[ ] Assign first issue (good-first-issue label)
[ ] Walk through claim → work → close cycle
[ ] Verify br sync works
` + "```" + `

> Press **→** to continue.`

// workflowStakeholderReviewContent is the Stakeholder Review workflow tutorial page.
const workflowStakeholderReviewContent = `## Workflow: Weekly Review with Stakeholders

Non-technical stakeholders can't use the terminal. The solution: static pages.

### Step 1: Generate the Dashboard

` + "```bash\nbv --pages                    # Interactive wizard\n# Or direct export:\nbv --export-pages ./dashboard --pages-title \"Sprint 42 Status\"\n```" + `

This creates a **self-contained HTML bundle**:
- Triage recommendations
- Dependency graph visualization
- Full-text search
- Works offline after load

### Step 2: Deploy for Access

**Option A: GitHub Pages** (recommended)

` + "```bash\nbv --pages\n# Follow wizard prompts:\n# → Select GitHub Pages\n# → Choose target repo/branch\n# → Auto-deploys!\n```" + `

**Option B: Share Link**

After CI/CD deployment, share the URL:
> "Here's our project status: https://your-org.github.io/project-status/"

**Option C: Local/Email**

` + "```bash\nbv --export-pages ./status\nzip -r status.zip ./status\n# Email the zip, or serve locally\n```" + `

### Step 3: During the Meeting

The dashboard shows:

- **Triage recommendations** — What to work on next
- **Blocked items** — What's stuck and why
- **Priority distribution** — Balance of work
- **Dependency graph** — Visual structure

### Step 4: Link to Specific Issues

Each issue has a stable URL in the dashboard:
> "Let's look at the auth migration: [link to specific issue]"

Click to see full details, dependencies, labels.

### Step 5: Automate Updates

For continuous visibility, add to CI/CD:

` + "```yaml\n# .github/workflows/dashboard.yml\non:\n  push:\n    paths: ['.beads/**']\njobs:\n  deploy:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - run: bv --export-pages ./pages\n      - uses: peaceiris/actions-gh-pages@v3\n        with:\n          publish_dir: ./pages\n```" + `

Dashboard updates automatically when beads change!

### Benefits for Stakeholders

- **No login required** — Just a URL
- **Always current** — Auto-deployed from git
- **Self-serve** — They can browse on their own
- **Professional** — Clean, searchable interface

> Press **→** to continue to Reference section.`
