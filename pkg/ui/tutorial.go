package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
		case "esc", "q", "~", "`":
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

	case tea.MouseMsg:
		pages := m.visiblePages()
		if m.focus == focusTutorialTOC && m.tocVisible {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				if m.tocCursor > 0 {
					m.tocCursor--
				}
			case tea.MouseButtonWheelDown:
				if m.tocCursor < len(pages)-1 {
					m.tocCursor++
				}
			}
			return m, nil
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollOffset -= 3
			if m.scrollOffset < 0 {
				m.scrollOffset = 0
			}
		case tea.MouseButtonWheelDown:
			m.scrollOffset += 3
			if maxScroll := m.maxScrollOffset(); m.scrollOffset > maxScroll {
				m.scrollOffset = maxScroll
			}
		}
		return m, nil
	}
	return m, nil
}

// maxScrollOffset computes the real maximum scroll offset for the active page
// at the current modal dimensions, matching renderContent / windowContentLines.
func (m TutorialModel) maxScrollOffset() int {
	pages := m.visiblePages()
	if len(pages) == 0 || m.currentPage < 0 || m.currentPage >= len(pages) {
		return 0
	}
	page := pages[m.currentPage]
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	modalWidth := min(width-4, 110)
	if modalWidth < 40 {
		modalWidth = max(width-2, 20)
	}
	contentWidth := modalWidth - 6
	if m.tocVisible && modalWidth >= 70 {
		contentWidth = modalWidth - 28
	}
	if contentWidth < 20 {
		contentWidth = 20
	}
	availHeight := max(height-11, 5)

	var lines []string
	if structuredPage := getStructuredPage(page.ID); structuredPage != nil {
		renderedContent := RenderStructuredPage(*structuredPage, m.theme, contentWidth)
		lines = strings.Split(renderedContent, "\n")
	} else {
		renderedContent := page.Content
		if m.markdownRenderer != nil {
			if rendered, err := m.markdownRenderer.Render(page.Content); err == nil {
				renderedContent = strings.TrimSpace(rendered)
			}
		}
		rawLines := strings.Split(renderedContent, "\n")
		blankCount := 0
		for _, line := range rawLines {
			if strings.TrimSpace(line) == "" {
				blankCount++
				if blankCount <= 2 {
					lines = append(lines, line)
				}
			} else {
				blankCount = 0
				lines = append(lines, line)
			}
		}
	}
	if len(lines) <= availHeight {
		return 0
	}
	// When scrolled to the bottom, the "↑ more above" indicator uses 1 slot.
	slots := max(availHeight-1, 1)
	return max(0, len(lines)-slots)
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
		if maxScroll := m.maxScrollOffset(); m.scrollOffset < maxScroll {
			m.scrollOffset++
		}
	case "k", "up":
		if maxScroll := m.maxScrollOffset(); m.scrollOffset > maxScroll {
			m.scrollOffset = maxScroll
		}
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
		if maxScroll := m.maxScrollOffset(); m.scrollOffset > maxScroll {
			m.scrollOffset = maxScroll
		}
	case "ctrl+u":
		visibleHeight := m.height - 11
		if visibleHeight < 5 {
			visibleHeight = 5
		}
		if maxScroll := m.maxScrollOffset(); m.scrollOffset > maxScroll {
			m.scrollOffset = maxScroll
		}
		m.scrollOffset -= visibleHeight / 2
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}

	// Jump to top/bottom
	case "g", "home":
		m.scrollOffset = 0
	case "G", "end":
		m.scrollOffset = m.maxScrollOffset()

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
		if len(pages) > 0 {
			m.tocCursor = len(pages) - 1
		}
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

// ViewForBodyHeight renders the tutorial constrained to the available body height
// (e.g. m.height-1 above Model's global footer) without mutating stored terminal height.
func (m TutorialModel) ViewForBodyHeight(bodyHeight int) string {
	if bodyHeight > 0 {
		m.height = bodyHeight
	}
	return m.View()
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

	// Calculate dimensions within m.width (accounting for 2 border columns and horizontal padding)
	boxWidth := 78
	horizPad := 2
	if m.width > 0 {
		boxWidth = max(1, m.width-2)
		if boxWidth <= 6 {
			horizPad = 0
		} else if boxWidth <= 10 {
			horizPad = 1
		}
	}
	innerWidth := max(1, boxWidth-2*horizPad)
	vertPad := 1
	if m.height > 0 && m.height <= 26 {
		vertPad = 0
	}
	narrowSinglePaneTOC := m.tocVisible && innerWidth < 46
	contentWidth := innerWidth
	if m.tocVisible && !narrowSinglePaneTOC {
		contentWidth = max(innerWidth-26, 20) // TOC sidebar (24 cols) + 2-space gap
	}

	clampWidthLines := func(s string, maxW int) string {
		if maxW <= 0 {
			return ""
		}
		suffix := "…"
		if maxW <= 2 {
			suffix = ""
		}
		raw := strings.Split(s, "\n")
		for i, ln := range raw {
			if lipgloss.Width(ln) > maxW {
				raw[i] = ansi.Truncate(ln, maxW, suffix)
			}
		}
		return strings.Join(raw, "\n")
	}

	// Header + separator + page title
	header := m.renderHeader(currentPage, len(pages))
	sepStyle := r.NewStyle().Foreground(m.theme.Border)
	sep := sepStyle.Render(strings.Repeat("─", innerWidth))

	pageTitleStyle := r.NewStyle().Bold(true).Foreground(m.theme.Primary)
	sectionStyle := r.NewStyle().Foreground(m.theme.Subtext).Italic(true)
	pageTitle := pageTitleStyle.Render(currentPage.Title)
	if currentPage.Section != "" {
		pageTitle += sectionStyle.Render(" — " + currentPage.Section)
	}
	headerBlock := header + "\n" + sep + "\n" + pageTitle

	// Footer with navigation hints
	footer := m.renderFooter(len(pages))

	wrapMeasure := r.NewStyle().Width(innerWidth)
	headerRendered := clampWidthLines(wrapMeasure.Render(headerBlock), innerWidth)
	footerRendered := clampWidthLines(wrapMeasure.Render(footer), innerWidth)
	headerH := lipgloss.Height(headerRendered)
	footerH := lipgloss.Height(footerRendered)
	borderH := 2 + 2*vertPad
	availBody := 12
	if m.height > 0 {
		availBody = max(1, m.height-headerH-footerH-borderH)
	}

	var bodySection string
	if m.tocVisible {
		if narrowSinglePaneTOC {
			if m.focus == focusTutorialTOC {
				bodySection = m.renderTOCWithWidth(pages, availBody, max(1, innerWidth-2))
			} else {
				bodySection = m.renderContent(currentPage, innerWidth, availBody)
			}
		} else {
			toc := m.renderTOCWithWidth(pages, availBody, 22)
			content := m.renderContent(currentPage, contentWidth, availBody)
			bodySection = lipgloss.JoinHorizontal(lipgloss.Top, toc, "  ", content)
		}
	} else {
		bodySection = m.renderContent(currentPage, contentWidth, availBody)
	}
	bodySection = clampWidthLines(bodySection, innerWidth)

	if m.height > 0 && m.height < 3 {
		return fitBlock(headerRendered, max(1, m.width), m.height)
	}

	assembled := headerRendered + "\n" + bodySection + "\n" + footerRendered
	if m.height > 0 {
		maxInner := max(1, m.height-borderH)
		allLines := strings.Split(assembled, "\n")
		if len(allLines) > maxInner {
			assembled = strings.Join(allLines[:maxInner], "\n")
		}
	}

	// Wrap in modal style
	modalStyle := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(vertPad, horizPad).
		Width(boxWidth)
	if m.height > 0 {
		modalStyle = modalStyle.MaxHeight(m.height)
	}

	return modalStyle.Render(assembled)
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
func (m TutorialModel) renderContent(page TutorialPage, width, availHeight int) string {
	// Check if we have a structured page for this ID (preferred)
	if structuredPage := getStructuredPage(page.ID); structuredPage != nil {
		// Use native lipgloss component rendering
		return m.renderStructuredContent(*structuredPage, width, availHeight)
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
	return m.windowContentLines(compressedLines, width, availHeight)
}

// renderStructuredContent renders a structured tutorial page with native lipgloss components.
func (m TutorialModel) renderStructuredContent(page StructuredTutorialPage, width, availHeight int) string {
	renderedContent := RenderStructuredPage(page, m.theme, width)
	lines := strings.Split(renderedContent, "\n")
	return m.windowContentLines(lines, width, availHeight)
}

func (m TutorialModel) windowContentLines(lines []string, width, availHeight int) string {
	if availHeight <= 0 {
		availHeight = max(m.height-11, 5)
	}
	if width > 0 {
		for i, ln := range lines {
			if lipgloss.Width(ln) > width {
				lines[i] = ansi.Truncate(ln, width, "…")
			}
		}
	}
	if len(lines) <= availHeight && m.scrollOffset == 0 {
		return strings.Join(lines, "\n")
	}

	r := m.theme.Renderer
	hintStyle := r.NewStyle().Foreground(m.theme.Muted)

	hasUp := m.scrollOffset > 0
	slots := availHeight
	if hasUp && slots > 1 {
		slots--
	}
	if m.scrollOffset+slots < len(lines) && slots > 1 {
		slots--
	}
	maxScroll := max(0, len(lines)-slots)
	offset := m.scrollOffset
	if offset > maxScroll {
		offset = maxScroll
	}
	if offset < 0 {
		offset = 0
	}
	hasUp = offset > 0

	// Recompute exact slot budget once offset is clamped so total lines <= availHeight.
	slots = availHeight
	if hasUp && slots > 1 {
		slots--
	}
	hasDown := offset+slots < len(lines)
	if hasDown && slots > 1 {
		slots--
	}
	endLine := min(len(lines), offset+slots)

	var out []string
	if hasUp && availHeight > 1 {
		out = append(out, hintStyle.Render("↑ more above"))
	}
	out = append(out, lines[offset:endLine]...)
	if endLine < len(lines) && len(out) < availHeight {
		out = append(out, hintStyle.Render("↓ more below"))
	}
	return strings.Join(out, "\n")
}

// renderTOC renders the table of contents sidebar with focus indication (bv-wdsd).
func (m TutorialModel) renderTOC(pages []TutorialPage, availHeight int) string {
	return m.renderTOCWithWidth(pages, availHeight, 22)
}

func (m TutorialModel) renderTOCWithWidth(pages []TutorialPage, availHeight, tocBoxWidth int) string {
	r := m.theme.Renderer
	if tocBoxWidth < 1 {
		tocBoxWidth = 22
	}
	tocHPad := 1
	if tocBoxWidth < 4 {
		tocHPad = 0
	}
	tocInnerW := max(1, tocBoxWidth-2*tocHPad)

	// Use different border style when TOC has focus
	borderColor := m.theme.Border
	if m.focus == focusTutorialTOC {
		borderColor = m.theme.Primary
	}

	tocStyle := r.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		Padding(0, tocHPad).
		Width(tocBoxWidth)

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

	headerLine := headerStyle.Render("Contents")
	if m.focus == focusTutorialTOC {
		headerLine += r.NewStyle().Foreground(m.theme.Primary).Render(" ●")
	}
	headerLine = ansi.Truncate(headerLine, tocInnerW, "…")

	anchorIdx := m.currentPage
	if m.focus == focusTutorialTOC {
		anchorIdx = m.tocCursor
	}
	if anchorIdx < 0 {
		anchorIdx = 0
	} else if len(pages) > 0 && anchorIdx >= len(pages) {
		anchorIdx = len(pages) - 1
	}

	titleMaxRunes := max(4, tocInnerW-6)
	var tocLines []string
	activeLineIdx := 0
	currentSection := ""
	for i, page := range pages {
		// Show section header if changed
		if page.Section != currentSection && page.Section != "" {
			if len(tocLines) > 0 {
				tocLines = append(tocLines, "")
			}
			currentSection = page.Section
			secLine := sectionStyle.Render("▸ " + truncateRunesHelper(currentSection, max(2, tocInnerW-2), "…"))
			tocLines = append(tocLines, ansi.Truncate(secLine, tocInnerW, "…"))
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
		title := truncateRunesHelper(page.Title, titleMaxRunes, "…")

		// Viewed indicator
		viewed := ""
		if m.progress[page.ID] {
			viewed = viewedStyle.Render(" ✓")
		}

		if i == anchorIdx {
			activeLineIdx = len(tocLines)
		}
		tocLines = append(tocLines, ansi.Truncate(style.Render(prefix+title)+viewed, tocInnerW, "…"))
	}

	if availHeight <= 0 {
		availHeight = max(m.height-8, 6)
	}
	if availHeight < 4 {
		return r.NewStyle().Width(min(24, tocBoxWidth+2)).MaxHeight(max(availHeight, 1)).Render(headerLine)
	}
	// NormalBorder uses 2 vertical rows and headerLine uses 1 row.
	availBody := max(availHeight-3, 1)
	var visibleLines []string
	if len(tocLines) <= availBody {
		visibleLines = tocLines
	} else if availBody == 1 {
		visibleLines = []string{tocLines[activeLineIdx]}
	} else if availBody == 2 {
		hintStyle := r.NewStyle().Foreground(m.theme.Muted)
		if activeLineIdx < len(tocLines)-1 {
			visibleLines = []string{tocLines[activeLineIdx], hintStyle.Render("  ↓ more")}
		} else {
			visibleLines = []string{hintStyle.Render("  ↑ more"), tocLines[activeLineIdx]}
		}
	} else {
		hintStyle := r.NewStyle().Foreground(m.theme.Muted)
		winSize := max(availBody-2, 1)
		start := activeLineIdx - winSize/2
		if start < 0 {
			start = 0
		}
		if start > len(tocLines)-winSize {
			start = max(0, len(tocLines)-winSize)
		}
		if start == 0 {
			end := min(len(tocLines), availBody-1)
			visibleLines = append(visibleLines, tocLines[:end]...)
			if end < len(tocLines) {
				visibleLines = append(visibleLines, hintStyle.Render("  ↓ more"))
			}
		} else if activeLineIdx >= len(tocLines)-(availBody-1) {
			start = max(1, len(tocLines)-(availBody-1))
			visibleLines = append(visibleLines, hintStyle.Render("  ↑ more"))
			visibleLines = append(visibleLines, tocLines[start:]...)
		} else {
			end := min(len(tocLines), start+winSize)
			visibleLines = append(visibleLines, hintStyle.Render("  ↑ more"))
			visibleLines = append(visibleLines, tocLines[start:end]...)
			if end < len(tocLines) {
				visibleLines = append(visibleLines, hintStyle.Render("  ↓ more"))
			}
		}
	}

	var b strings.Builder
	b.WriteString(headerLine)
	for _, ln := range visibleLines {
		b.WriteString("\n")
		b.WriteString(ln)
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
	msg := "No tutorial pages available for this context."
	if m.height > 0 && m.height < 3 {
		w := 40
		if m.width > 0 {
			w = m.width
		}
		return fitBlock(msg, max(1, w), m.height)
	}
	boxW := 78
	if m.width > 0 {
		boxW = max(1, m.width-2)
	}
	hPad := 4
	if boxW <= 8 {
		hPad = 0
	} else if boxW <= 16 {
		hPad = 1
	}
	vPad := 2
	if m.height > 0 && m.height <= 8 {
		vPad = 0
	}
	innerW := max(1, boxW-2*hPad)
	wrapped := r.NewStyle().Width(innerW).Render(msg)
	lines := strings.Split(wrapped, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) > innerW {
			suffix := "…"
			if innerW <= 2 {
				suffix = ""
			}
			lines[i] = ansi.Truncate(ln, innerW, suffix)
		}
	}
	if m.height > 0 {
		maxInner := max(1, m.height-2-2*vPad)
		if len(lines) > maxInner {
			lines = lines[:maxInner]
		}
	}

	style := r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Primary).
		Padding(vPad, hPad).
		Width(boxW)
	if m.height > 0 {
		style = style.MaxHeight(m.height)
	}

	return style.Render(strings.Join(lines, "\n"))
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
