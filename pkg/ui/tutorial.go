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

// NewTutorialModel creates a new tutorial model with default pages. Unless
// BV_NO_SAVED_CONFIG is set it restores the persisted progress (viewed pages
// for the TOC check marks) and resumes at the page the user last left.
func NewTutorialModel(theme Theme) TutorialModel {
	// Calculate initial content width for markdown renderer
	contentWidth := 80 - 6 // default width minus padding
	if contentWidth < 40 {
		contentWidth = 40
	}

	m := TutorialModel{
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
	if !savedConfigDisabled() {
		m.LoadProgress()
		m.ResumeLastPage()
	}
	return m
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
		case "esc", "q", "~":
			// Mark current page as viewed before closing
			pages := m.visiblePages()
			if m.currentPage >= 0 && m.currentPage < len(pages) {
				m.progress[pages[m.currentPage].ID] = true
			}
			m.shouldClose = true
			return m, nil

		case "a":
			// Toggle between context-filtered pages and full tutorial TOC when a context is set
			if m.context != "" {
				if m.contextMode {
					m.SetContextMode(false)
					m.tocVisible = true
					m.focus = focusTutorialTOC
					m.tocCursor = m.currentPage
				} else {
					m.SetContextMode(true)
					m.markCurrentPageViewed()
				}
				return m, nil
			}

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

	// Progress indicator: "Page 3/30 · 10%" followed by a bar. The percentage
	// is the share of pages viewed (persisted across sessions), not the
	// position, so it keeps growing as the user explores out of order.
	pageNum := m.currentPage + 1
	viewedPercent := m.viewedPercent()
	progressText := r.NewStyle().
		Foreground(m.theme.Subtext).
		Render(fmt.Sprintf("Page %d/%d · %d%%", pageNum, totalPages, viewedPercent))

	// Visual progress bar
	barWidth := 10
	filledWidth := (viewedPercent * barWidth) / 100
	// Ensure at least 1 filled bar when on any page
	if filledWidth < 1 && pageNum > 0 {
		filledWidth = 1
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
	if m.contextMode && m.context != "" {
		ctxBadge := r.NewStyle().
			Foreground(m.theme.Secondary).
			Render(fmt.Sprintf(" [context: %s]", m.context))
		title += ctxBadge
	}

	// Calculate spacing to align progress to the right
	headerContent := title + "  " + progressText + " " + progressBar

	return headerContent
}

// viewedPercent returns the share (0-100) of visible pages the user has
// viewed, counting the current page.
func (m TutorialModel) viewedPercent() int {
	pages := m.visiblePages()
	if len(pages) == 0 {
		return 0
	}
	viewed := 0
	for i, page := range pages {
		if m.progress[page.ID] || i == m.currentPage {
			viewed++
		}
	}
	return (viewed * 100) / len(pages)
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

		// Truncate long titles (rune-safe)
		title := truncateRunesHelper(page.Title, 14, "…")

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
		}
		if m.context != "" {
			if m.contextMode {
				hints = append(hints, keyStyle.Render("a")+descStyle.Render(" all pages"))
			} else {
				hints = append(hints, keyStyle.Render("a")+descStyle.Render(" context only"))
			}
		}
		hints = append(hints, keyStyle.Render("Esc/q")+descStyle.Render(" close"))
	} else {
		// Content-focused hints
		hints = []string{
			keyStyle.Render("←/→/Space") + descStyle.Render(" pages"),
			keyStyle.Render("j/k") + descStyle.Render(" scroll"),
			keyStyle.Render("Ctrl+d/u") + descStyle.Render(" half-page"),
			keyStyle.Render("t") + descStyle.Render(" TOC"),
		}
		if m.context != "" {
			if m.contextMode {
				hints = append(hints, keyStyle.Render("a")+descStyle.Render(" all pages"))
			} else {
				hints = append(hints, keyStyle.Render("a")+descStyle.Render(" context only"))
			}
		}
		hints = append(hints, keyStyle.Render("Esc/q")+descStyle.Render(" close"))
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
		m.markCurrentPageViewed()
	}
}

// PrevPage goes to the previous page.
func (m *TutorialModel) PrevPage() {
	if m.currentPage > 0 {
		m.currentPage--
		m.scrollOffset = 0
		m.markCurrentPageViewed()
	}
}

// JumpToPage jumps to a specific page index.
func (m *TutorialModel) JumpToPage(index int) {
	pages := m.visiblePages()
	if index >= 0 && index < len(pages) {
		m.currentPage = index
		m.scrollOffset = 0
		m.markCurrentPageViewed()
	}
}

// JumpToSection jumps to the first page in a section.
func (m *TutorialModel) JumpToSection(sectionID string) {
	pages := m.visiblePages()
	for i, page := range pages {
		if page.ID == sectionID || page.Section == sectionID {
			m.currentPage = i
			m.scrollOffset = 0
			m.markCurrentPageViewed()
			return
		}
	}
}

// SetContext sets the current view context for filtering.
func (m *TutorialModel) SetContext(ctx string) {
	m.context = ctx
	// Reset to first page when context changes
	m.currentPage = 0
	m.tocCursor = 0
	m.scrollOffset = 0
}

// SetContextMode enables or disables context-based filtering.
func (m *TutorialModel) SetContextMode(enabled bool) {
	if enabled == m.contextMode {
		if enabled {
			m.currentPage = 0
			m.tocCursor = 0
			m.scrollOffset = 0
		}
		return
	}
	if !enabled {
		// Preserve the currently viewed page's index in the full tutorial list
		// when expanding out of context mode or closing context help.
		currentID := m.CurrentPageID()
		m.contextMode = false
		if currentID != "" {
			for i, page := range m.pages {
				if page.ID == currentID {
					m.currentPage = i
					m.tocCursor = i
					break
				}
			}
		}
		return
	}
	m.contextMode = true
	m.currentPage = 0
	m.tocCursor = 0
	m.scrollOffset = 0
}

// IsContextMode reports whether context-based page filtering is currently active.
func (m TutorialModel) IsContextMode() bool {
	return m.contextMode
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

// visiblePages returns pages matching the active context when contextMode is enabled,
// falling back to all pages only if no page matches the active context.
func (m TutorialModel) visiblePages() []TutorialPage {
	if !m.contextMode || m.context == "" {
		return m.pages
	}

	var filtered []TutorialPage
	for _, page := range m.pages {
		for _, ctx := range page.Contexts {
			if ctx == m.context {
				filtered = append(filtered, page)
				break
			}
		}
	}
	if len(filtered) == 0 {
		return m.pages
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

// defaultTutorialPages returns the built-in tutorial pages derived from structuredTutorialPages().
func defaultTutorialPages() []TutorialPage {
	structured := structuredTutorialPages()
	pages := make([]TutorialPage, 0, len(structured))
	for _, sp := range structured {
		pages = append(pages, TutorialPage{
			ID:       sp.ID,
			Title:    sp.Title,
			Section:  sp.Section,
			Contexts: append([]string(nil), sp.Contexts...),
		})
	}
	return pages
}
