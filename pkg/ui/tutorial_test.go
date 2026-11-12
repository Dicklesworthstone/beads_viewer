package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func newTestTutorialModel() TutorialModel {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	return NewTutorialModel(theme)
}

func TestNewTutorialModel(t *testing.T) {
	m := newTestTutorialModel()

	if m.currentPage != 0 {
		t.Errorf("Expected initial page 0, got %d", m.currentPage)
	}
	if m.scrollOffset != 0 {
		t.Errorf("Expected initial scroll 0, got %d", m.scrollOffset)
	}
	if m.tocVisible {
		t.Error("Expected TOC to be hidden initially")
	}
	if m.contextMode {
		t.Error("Expected context mode to be disabled initially")
	}
	if len(m.pages) == 0 {
		t.Error("Expected default pages to be loaded")
	}
	if m.progress == nil {
		t.Error("Expected progress map to be initialized")
	}
}

func TestTutorialNavigation(t *testing.T) {
	m := newTestTutorialModel()
	totalPages := len(m.pages)

	// Test next page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.currentPage != 1 {
		t.Errorf("Expected page 1 after 'n', got %d", m.currentPage)
	}

	// Test right arrow
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.currentPage != 2 {
		t.Errorf("Expected page 2 after right arrow, got %d", m.currentPage)
	}

	// Test previous page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.currentPage != 1 {
		t.Errorf("Expected page 1 after 'p', got %d", m.currentPage)
	}

	// Test left arrow
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.currentPage != 0 {
		t.Errorf("Expected page 0 after left arrow, got %d", m.currentPage)
	}

	// Test boundary - can't go below 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.currentPage != 0 {
		t.Errorf("Expected page to stay at 0, got %d", m.currentPage)
	}

	// Go to last page
	for i := 0; i < totalPages; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if m.currentPage != totalPages-1 {
		t.Errorf("Expected to be at last page %d, got %d", totalPages-1, m.currentPage)
	}

	// Test boundary - can't go above max
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.currentPage != totalPages-1 {
		t.Errorf("Expected to stay at last page, got %d", m.currentPage)
	}
}

func TestTutorialScrolling(t *testing.T) {
	m := newTestTutorialModel()

	// Test scroll down
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.scrollOffset != 1 {
		t.Errorf("Expected scroll 1 after 'j', got %d", m.scrollOffset)
	}

	// Test scroll up
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 after 'k', got %d", m.scrollOffset)
	}

	// Can't scroll below 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll to stay at 0, got %d", m.scrollOffset)
	}

	// Test home
	m.scrollOffset = 5
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 after 'g', got %d", m.scrollOffset)
	}

	// Test end (will be clamped in View)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.scrollOffset == 0 {
		t.Error("Expected scroll to increase after 'G'")
	}
}

func TestTutorialTOCToggle(t *testing.T) {
	m := newTestTutorialModel()

	if m.tocVisible {
		t.Error("TOC should be hidden initially")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.tocVisible {
		t.Error("TOC should be visible after 't'")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.tocVisible {
		t.Error("TOC should be hidden after second 't'")
	}
}

func TestTutorialJumpToPage(t *testing.T) {
	m := newTestTutorialModel()

	// Jump to page 3 using number key
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.currentPage != 2 { // 0-indexed
		t.Errorf("Expected page 2 after '3', got %d", m.currentPage)
	}

	// Jump to page 1
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.currentPage != 0 {
		t.Errorf("Expected page 0 after '1', got %d", m.currentPage)
	}

	// Invalid page number (beyond available pages)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	// Should not change if page doesn't exist
}

func TestTutorialJumpMethods(t *testing.T) {
	m := newTestTutorialModel()

	// JumpToPage
	m.JumpToPage(3)
	if m.currentPage != 3 {
		t.Errorf("Expected page 3, got %d", m.currentPage)
	}
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll reset to 0, got %d", m.scrollOffset)
	}

	// JumpToPage with invalid index
	m.JumpToPage(-1)
	if m.currentPage != 3 {
		t.Error("JumpToPage with negative index should not change page")
	}

	m.JumpToPage(9999)
	if m.currentPage != 3 {
		t.Error("JumpToPage with too-large index should not change page")
	}

	// JumpToSection by section name
	m.JumpToSection("Core Concepts")
	if m.currentPage == 3 {
		t.Error("JumpToSection('Core Concepts') should have changed page")
	}

	// JumpToSection by exact page ID
	m.JumpToSection("intro-welcome")
	if m.currentPage != 0 {
		t.Errorf("JumpToSection('intro-welcome') should go to page 0, got %d", m.currentPage)
	}

	// JumpToSection with non-existent section (should not change)
	m.currentPage = 5
	m.JumpToSection("nonexistent-section")
	if m.currentPage != 5 {
		t.Error("JumpToSection with invalid section should not change page")
	}
}

func TestTutorialContextFiltering(t *testing.T) {
	m := newTestTutorialModel()

	// Initially all pages visible
	allPages := m.visiblePages()
	if len(allPages) == 0 {
		t.Error("Expected some pages")
	}

	// Enable context mode
	m.SetContextMode(true)
	m.SetContext("list")

	// Now only list-context pages should be visible
	filteredPages := m.visiblePages()
	for _, page := range filteredPages {
		if len(page.Contexts) > 0 {
			found := false
			for _, ctx := range page.Contexts {
				if ctx == "list" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Page %s should not be visible in list context", page.ID)
			}
		}
	}

	// Disable context mode - all pages visible again
	m.SetContextMode(false)
	allPagesAgain := m.visiblePages()
	if len(allPagesAgain) != len(allPages) {
		t.Errorf("Expected %d pages without context mode, got %d", len(allPages), len(allPagesAgain))
	}
}

func TestTutorialProgress(t *testing.T) {
	m := newTestTutorialModel()

	// Initially no progress
	if m.IsComplete() {
		t.Error("Tutorial should not be complete initially")
	}

	// Mark first page viewed
	m.MarkViewed("intro")
	if !m.progress["intro"] {
		t.Error("Page 'intro' should be marked as viewed")
	}

	// Check progress getter
	progress := m.Progress()
	if !progress["intro"] {
		t.Error("Progress getter should return viewed pages")
	}

	// Set progress from external source (persistence)
	newProgress := map[string]bool{
		"intro":      true,
		"navigation": true,
	}
	m.SetProgress(newProgress)
	if !m.progress["navigation"] {
		t.Error("SetProgress should restore progress")
	}

	// Mark all pages viewed
	for _, page := range m.pages {
		m.MarkViewed(page.ID)
	}
	if !m.IsComplete() {
		t.Error("Tutorial should be complete when all pages viewed")
	}
}

func TestTutorialView(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 24)

	view := m.View()

	// Should contain title
	if !strings.Contains(view, "Welcome") {
		t.Error("View should contain first page title")
	}

	// Should contain navigation hints
	if !strings.Contains(view, "pages") {
		t.Error("View should contain navigation hints")
	}

	// Test with TOC visible
	m.tocVisible = true
	viewWithTOC := m.View()
	if !strings.Contains(viewWithTOC, "Contents") {
		t.Error("View with TOC should contain Contents header")
	}
}

func TestTutorialSetSize(t *testing.T) {
	m := newTestTutorialModel()

	m.SetSize(100, 30)
	if m.width != 100 {
		t.Errorf("Expected width 100, got %d", m.width)
	}
	if m.height != 30 {
		t.Errorf("Expected height 30, got %d", m.height)
	}
}

func TestTutorialCurrentPageID(t *testing.T) {
	m := newTestTutorialModel()

	id := m.CurrentPageID()
	if id != "intro-welcome" {
		t.Errorf("Expected 'intro-welcome', got %s", id)
	}

	m.NextPage()
	id = m.CurrentPageID()
	if id != "intro-philosophy" {
		t.Errorf("Expected 'intro-philosophy', got %s", id)
	}
}

func TestTutorialCenterTutorial(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(60, 20)

	centered := m.CenterTutorial(100, 40)

	// Should not be empty
	if centered == "" {
		t.Error("Centered tutorial should not be empty")
	}

	// Should still contain content
	if !strings.Contains(centered, "Welcome") {
		t.Error("Centered tutorial should contain content")
	}
}

func TestTutorialEmptyState(t *testing.T) {
	m := newTestTutorialModel()
	m.pages = []TutorialPage{} // Clear all pages

	view := m.View()
	if !strings.Contains(view, "No tutorial pages") {
		t.Error("Empty state should show appropriate message")
	}
}

func TestTutorialInit(t *testing.T) {
	m := newTestTutorialModel()
	cmd := m.Init()

	// Init should return nil (no initial command)
	if cmd != nil {
		t.Error("Init should return nil")
	}
}

func TestTutorialPageNavResetsScroll(t *testing.T) {
	m := newTestTutorialModel()

	// Scroll down on first page
	m.scrollOffset = 10

	// Navigate to next page
	m.NextPage()

	// Scroll should reset
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll to reset on page change, got %d", m.scrollOffset)
	}

	// Same for PrevPage
	m.scrollOffset = 5
	m.PrevPage()
	if m.scrollOffset != 0 {
		t.Errorf("Expected scroll to reset on PrevPage, got %d", m.scrollOffset)
	}
}

func TestTutorialAlternativeKeys(t *testing.T) {
	m := newTestTutorialModel()

	// Test 'l' for next page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.currentPage != 1 {
		t.Error("'l' should navigate to next page")
	}

	// Test 'h' for prev page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.currentPage != 0 {
		t.Error("'h' should navigate to previous page")
	}

	// Test Tab for next page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.currentPage != 1 {
		t.Error("Tab should navigate to next page")
	}

	// Test Shift+Tab for prev page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.currentPage != 0 {
		t.Error("Shift+Tab should navigate to previous page")
	}

	// Test down arrow for scroll
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.scrollOffset != 1 {
		t.Error("Down arrow should scroll down")
	}

	// Test up arrow for scroll
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.scrollOffset != 0 {
		t.Error("Up arrow should scroll up")
	}

	// Test Home for scroll
	m.scrollOffset = 10
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.scrollOffset != 0 {
		t.Error("Home should scroll to top")
	}

	// Test End for scroll
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.scrollOffset == 0 {
		t.Error("End should scroll down")
	}
}

func TestTutorialProgressPersistence(t *testing.T) {
	m := newTestTutorialModel()

	// Simulate viewing pages
	m.MarkViewed("intro")
	m.MarkViewed("navigation")

	// Get progress for persistence
	progress := m.Progress()

	// Create new tutorial model
	m2 := newTestTutorialModel()

	// Restore progress
	m2.SetProgress(progress)

	// Verify restored
	if !m2.progress["intro"] {
		t.Error("Progress should be restored for 'intro'")
	}
	if !m2.progress["navigation"] {
		t.Error("Progress should be restored for 'navigation'")
	}

	// Test nil progress doesn't crash
	m2.SetProgress(nil)
}

func TestDefaultTutorialPages(t *testing.T) {
	pages := defaultTutorialPages()

	if len(pages) == 0 {
		t.Error("Should have default pages")
	}

	// Check required pages exist (using new page IDs from bv-kdv2)
	requiredIDs := []string{"intro-welcome", "intro-philosophy", "ref-keyboard"}
	for _, id := range requiredIDs {
		found := false
		for _, page := range pages {
			if page.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing required page: %s", id)
		}
	}

	// Check all pages have required fields and backing structured elements
	for _, page := range pages {
		if page.ID == "" {
			t.Error("Page missing ID")
		}
		if page.Title == "" {
			t.Error("Page missing Title")
		}
		sp := getStructuredPage(page.ID)
		if sp == nil || len(sp.Elements) == 0 {
			t.Errorf("Page %s missing structured content elements", page.ID)
		}
	}
}

// Tests for UI Layout & Chrome (bv-h6rq)

func TestTutorialViewProgressBar(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(100, 60) // Larger to ensure footer isn't clipped

	view := m.View()

	// Should contain the documented progress indicator "Page N/M · X%"
	if !strings.Contains(view, "Page 1/") {
		t.Error("View should contain progress indicator 'Page 1/M' format")
	}

	// Should contain progress bar characters
	if !strings.Contains(view, "█") {
		t.Error("View should contain filled progress bar character")
	}

	// Navigate to page 2 and verify progress updates
	m.NextPage()
	view = m.View()
	if !strings.Contains(view, "Page 2/") {
		t.Error("View should show 'Page 2/M' on second page")
	}
}

func TestTutorialViewHeader(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 24)

	view := m.View()

	// Should contain app title
	if !strings.Contains(view, "beads_viewer Tutorial") {
		t.Error("View should contain app title 'beads_viewer Tutorial'")
	}

	// Should contain separator line
	if !strings.Contains(view, "─") {
		t.Error("View should contain separator line")
	}
}

func TestTutorialViewFooter(t *testing.T) {
	m := newTestTutorialModel()
	// Use large dimensions to ensure footer isn't clipped
	m.SetSize(100, 60)

	view := m.View()

	// Should contain styled key hints
	if !strings.Contains(view, "←/→") {
		t.Error("View should contain page navigation hint")
	}
	if !strings.Contains(view, "j/k") {
		t.Error("View should contain scroll hint")
	}
	if !strings.Contains(view, "TOC") {
		t.Error("View should contain TOC hint")
	}
	// Footer shows "q close" not "Esc close"
	if !strings.Contains(view, "close") {
		t.Error("View should contain close hint")
	}
}

func TestTutorialTOCSectionIndicators(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 24)
	m.tocVisible = true

	view := m.View()

	// Should contain section indicator
	if !strings.Contains(view, "▸") {
		t.Error("TOC should contain section indicator ▸")
	}

	// Should contain current page indicator
	if !strings.Contains(view, "▶") {
		t.Error("TOC should contain current page indicator ▶")
	}
}

func TestTutorialTOCProgressCheckmarks(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 24)
	m.tocVisible = true

	// Mark intro as viewed
	m.MarkViewed("intro")

	view := m.View()

	// Should contain checkmark for viewed page
	if !strings.Contains(view, "✓") {
		t.Error("TOC should show checkmark for viewed pages")
	}
}

func TestTutorialPageTitleDisplay(t *testing.T) {
	m := newTestTutorialModel()
	// Use large height to ensure content isn't clipped
	m.SetSize(100, 60)

	view := m.View()

	// Should show current page title (now "Welcome" from bv-kdv2)
	if !strings.Contains(view, "Welcome") {
		t.Error("View should contain current page title")
	}

	// Should show section info (now "Introduction" from bv-kdv2)
	if !strings.Contains(view, "Introduction") {
		t.Error("View should contain section name")
	}
}

// Tests for Glamour Markdown Rendering (bv-lb0h)

func TestTutorialMarkdownRendererInitialized(t *testing.T) {
	m := newTestTutorialModel()

	// Markdown renderer should be initialized
	if m.markdownRenderer == nil {
		t.Error("Markdown renderer should be initialized")
	}
}

func TestTutorialMarkdownContent(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 30)

	view := m.View()

	// Should contain rendered markdown elements
	// Bold text should be rendered (though exact ANSI codes vary)
	if !strings.Contains(view, "beads_viewer") {
		t.Error("View should contain beads_viewer text")
	}

	// Bullet points from markdown should be present
	if !strings.Contains(view, "•") || !strings.Contains(view, "-") {
		// Glamour renders bullets as •
		// Check if content has list items
	}
}

func TestTutorialSetSizeUpdatesMarkdownRenderer(t *testing.T) {
	m := newTestTutorialModel()

	// Change size
	m.SetSize(120, 40)

	// The markdown renderer should be updated (not nil)
	if m.markdownRenderer == nil {
		t.Error("Markdown renderer should still exist after SetSize")
	}

	// Width should be updated
	if m.width != 120 {
		t.Errorf("Expected width 120, got %d", m.width)
	}
}

func TestTutorialMarkdownWithCodeBlocks(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(100, 60) // Larger to show more content

	// Navigate to the "AI Agent Integration" page which has code blocks
	// Page indices: intro x4, concepts x5, views x8, advanced x7 (semantic-search, time-travel, label-analytics, export, workspace, recipes, ai)
	m.JumpToPage(23) // Index 23 is "advanced-ai"

	view := m.View()

	// Code blocks should be present (content includes bash commands)
	// The exact rendering depends on Glamour, but the content should include command text
	if !strings.Contains(view, "robot") {
		t.Error("View should contain 'robot' command from code blocks")
	}
}

func TestTutorialMarkdownWithTables(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(100, 60) // Wide and tall enough for tables

	// Navigate to Quick Start page which has key bindings
	m.JumpToPage(3) // Index 3 is "intro-quickstart"

	view := m.View()

	// Key bindings content should be present (structured components or markdown)
	// Check for common keys that appear in Quick Start
	hasKeyBindings := strings.Contains(view, "j") || strings.Contains(view, "Enter") ||
		strings.Contains(view, "Move") || strings.Contains(view, "down")

	if !hasKeyBindings {
		t.Error("View should contain key binding content")
	}
}

// Tests for Keyboard Navigation (bv-wdsd)

func TestTutorialExitKeys(t *testing.T) {
	// Test 'q' key closes
	m := newTestTutorialModel()
	if m.ShouldClose() {
		t.Error("Should not close initially")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !m.ShouldClose() {
		t.Error("'q' should trigger close")
	}

	// Test Esc key closes
	m = newTestTutorialModel()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.ShouldClose() {
		t.Error("Esc should trigger close")
	}
}

func TestTutorialSpaceNavigates(t *testing.T) {
	m := newTestTutorialModel()

	// Space should advance to next page
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if m.currentPage != 1 {
		t.Error("Space should navigate to next page")
	}
}

func TestTutorialHalfPageScroll(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 30)

	// Ctrl+d should scroll half page down
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.scrollOffset == 0 {
		t.Error("Ctrl+d should scroll down")
	}

	savedOffset := m.scrollOffset

	// Ctrl+u should scroll half page up
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.scrollOffset >= savedOffset {
		t.Error("Ctrl+u should scroll up")
	}
}

func TestTutorialFocusManagement(t *testing.T) {
	m := newTestTutorialModel()

	// Initially content has focus
	if m.focus != focusTutorialContent {
		t.Error("Content should have initial focus")
	}

	// Toggle TOC with 't'
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.tocVisible {
		t.Error("TOC should be visible after 't'")
	}
	if m.focus != focusTutorialTOC {
		t.Error("TOC should have focus after 't'")
	}

	// Tab switches focus back to content
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.focus != focusTutorialContent {
		t.Error("Tab should switch focus to content")
	}

	// Tab again switches back to TOC (when visible)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.focus != focusTutorialTOC {
		t.Error("Tab should switch focus back to TOC")
	}
}

func TestTutorialTOCNavigation(t *testing.T) {
	m := newTestTutorialModel()
	m.tocVisible = true
	m.focus = focusTutorialTOC
	m.tocCursor = 0

	// j/down moves cursor down
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.tocCursor != 1 {
		t.Error("'j' should move TOC cursor down")
	}

	// k/up moves cursor up
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.tocCursor != 0 {
		t.Error("'k' should move TOC cursor up")
	}

	// Enter jumps to selected page
	m.tocCursor = 2
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentPage != 2 {
		t.Error("Enter should jump to TOC cursor position")
	}
	if m.focus != focusTutorialContent {
		t.Error("Enter should switch focus to content")
	}
}

func TestTutorialTOCCursorIndicator(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 24)
	m.tocVisible = true
	m.focus = focusTutorialTOC
	m.tocCursor = 1

	view := m.View()

	// Should contain cursor indicator
	if !strings.Contains(view, "→") {
		t.Error("TOC should show cursor indicator when focused")
	}

	// Should contain focus indicator
	if !strings.Contains(view, "●") {
		t.Error("TOC should show focus indicator")
	}
}

func TestTutorialContextSensitiveFooter(t *testing.T) {
	m := newTestTutorialModel()
	// Use very large dimensions to ensure footer isn't clipped by MaxHeight
	m.SetSize(120, 100)

	// Content focus footer
	view := m.View()
	if !strings.Contains(view, "Space") {
		t.Error("Content footer should show Space hint")
	}
	if !strings.Contains(view, "Ctrl+d") {
		t.Error("Content footer should show Ctrl+d hint")
	}

	// TOC focus footer
	m.tocVisible = true
	m.focus = focusTutorialTOC
	view = m.View()
	if !strings.Contains(view, "Enter") {
		t.Error("TOC footer should show Enter hint")
	}
	if !strings.Contains(view, "back to content") {
		t.Error("TOC footer should show back to content hint")
	}
}

func TestTutorialResetClose(t *testing.T) {
	m := newTestTutorialModel()
	m.shouldClose = true

	if !m.ShouldClose() {
		t.Error("ShouldClose should return true")
	}

	m.ResetClose()
	if m.ShouldClose() {
		t.Error("ResetClose should clear shouldClose flag")
	}
}

// =============================================================================
// ADDITIONAL TESTS FOR BV-0D11: Tutorial Page Rendering Validation
// =============================================================================

func TestAllPagesRenderWithoutError(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 30)

	// Iterate through all pages and verify each renders
	for i, page := range m.pages {
		t.Run(page.ID, func(t *testing.T) {
			m.JumpToPage(i)
			view := m.View()

			// Should not be empty
			if view == "" {
				t.Errorf("Page %s rendered empty view", page.ID)
			}

			// Should not contain error indicators
			if strings.Contains(view, "error") && strings.Contains(strings.ToLower(view), "render") {
				t.Errorf("Page %s may have rendering error", page.ID)
			}

			// Should contain page title
			if !strings.Contains(view, page.Title) {
				t.Errorf("Page %s view should contain title %q", page.ID, page.Title)
			}
		})
	}
}

func TestAllPagesHaveValidContent(t *testing.T) {
	pages := defaultTutorialPages()
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}

	for _, page := range pages {
		t.Run(page.ID, func(t *testing.T) {
			sp := getStructuredPage(page.ID)
			if sp == nil {
				t.Fatalf("Page %s missing from structuredTutorialPages()", page.ID)
			}
			rendered := ansi.Strip(RenderStructuredPage(*sp, theme, 80))

			// Rendered content should not be empty
			if strings.TrimSpace(rendered) == "" {
				t.Errorf("Page %s rendered empty content", page.ID)
			}

			// Rendered content should have minimum length
			if len(rendered) < 50 {
				t.Errorf("Page %s content too short (%d chars)", page.ID, len(rendered))
			}

			// Should have at least one Section header element
			hasSection := false
			for _, el := range sp.Elements {
				if sec, ok := el.(Section); ok && strings.TrimSpace(sec.Title) != "" {
					hasSection = true
					break
				}
			}
			if !hasSection {
				t.Errorf("Page %s should have at least one non-empty Section header", page.ID)
			}

			obviousPlaceholders := []string{
				"TODO:",
				"FIXME:",
				"TBD:",
				"coming soon",
				"[placeholder",
				"NOT IMPLEMENTED",
			}
			contentLower := strings.ToLower(rendered)
			for _, ph := range obviousPlaceholders {
				if strings.Contains(contentLower, strings.ToLower(ph)) {
					t.Errorf("Page %s contains placeholder text %q", page.ID, ph)
				}
			}
		})
	}
}

func TestAllPagesHaveValidStructure(t *testing.T) {
	pages := defaultTutorialPages()

	for _, page := range pages {
		t.Run(page.ID, func(t *testing.T) {
			// Required fields
			if page.ID == "" {
				t.Error("Page missing ID")
			}
			if page.Title == "" {
				t.Error("Page missing Title")
			}
			if page.Section == "" {
				t.Error("Page missing Section")
			}

			// ID should be lowercase and use hyphens
			if page.ID != strings.ToLower(page.ID) {
				t.Errorf("Page ID %q should be lowercase", page.ID)
			}
			if strings.Contains(page.ID, " ") {
				t.Errorf("Page ID %q should not contain spaces", page.ID)
			}
		})
	}
}

func TestPageContentLineWidth(t *testing.T) {
	pages := structuredTutorialPages()
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	targetWidth := 80

	for _, page := range pages {
		t.Run(page.ID, func(t *testing.T) {
			rendered := RenderStructuredPage(page, theme, targetWidth)
			if strings.TrimSpace(rendered) == "" {
				t.Fatalf("Page %s rendered empty output at width %d", page.ID, targetWidth)
			}
			for i, line := range strings.Split(rendered, "\n") {
				if w := lipgloss.Width(line); w > targetWidth+8 {
					t.Errorf("Page %s line %d width %d overflows target width %d: %q",
						page.ID, i+1, w, targetWidth, ansi.Strip(line))
				}
			}
		})
	}
}

func TestNarrowTerminalRendering(t *testing.T) {
	m := newTestTutorialModel()

	testWidths := []int{40, 50, 60, 80, 100, 120}

	for _, width := range testWidths {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m.SetSize(width, 30)
			m.JumpToPage(0) // Reset to first page

			view := m.View()

			// Should render without panic
			if view == "" {
				t.Errorf("Empty view at width %d", width)
			}

			// Should still contain title
			if !strings.Contains(view, "Welcome") {
				t.Errorf("View at width %d should contain page title", width)
			}
		})
	}
}

func TestShortTerminalRendering(t *testing.T) {
	m := newTestTutorialModel()

	testHeights := []int{10, 15, 20, 24, 30, 50}

	for _, height := range testHeights {
		t.Run(fmt.Sprintf("height_%d", height), func(t *testing.T) {
			m.SetSize(80, height)
			m.JumpToPage(0)

			view := m.View()

			// Should render without panic
			if view == "" {
				t.Errorf("Empty view at height %d", height)
			}
		})
	}
}

func TestMinimalTerminalDimensions(t *testing.T) {
	m := newTestTutorialModel()

	// Test very small terminal
	m.SetSize(20, 5)
	view := m.View()

	if view == "" {
		t.Error("Should render something even with minimal dimensions")
	}
}

func TestAllPagesContentMarkdownValid(t *testing.T) {
	for _, page := range structuredTutorialPages() {
		t.Run(page.ID, func(t *testing.T) {
			if len(page.Elements) == 0 {
				t.Fatalf("Page %s has zero elements", page.ID)
			}
			for idx, el := range page.Elements {
				switch v := el.(type) {
				case Paragraph:
					if strings.TrimSpace(v.Text) == "" {
						t.Errorf("Page %s element %d has empty Paragraph", page.ID, idx)
					}
				case Code:
					if strings.TrimSpace(v.Text) == "" {
						t.Errorf("Page %s element %d has empty Code block", page.ID, idx)
					}
				case KeyTable:
					if len(v.Bindings) == 0 {
						t.Errorf("Page %s element %d has empty KeyTable", page.ID, idx)
					}
				}
			}
		})
	}
}

func TestTOCCoversAllPages(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(120, 200) // Very large to show entire TOC
	m.tocVisible = true

	view := m.View()

	// Verify critical sections appear in TOC
	// (TOC rendering may truncate based on height, so we only check key sections)
	criticalSections := []string{"Introduction", "Core Concepts"}
	for _, section := range criticalSections {
		if !strings.Contains(view, section) {
			t.Errorf("TOC should contain section %q", section)
		}
	}
}

func TestScrollingWithLongContent(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(80, 15) // Short terminal to force scrolling

	// Find a structured page whose rendered output exceeds 20 lines
	foundLongPage := false
	for i, page := range m.pages {
		sp := getStructuredPage(page.ID)
		if sp == nil {
			continue
		}
		rendered := RenderStructuredPage(*sp, m.theme, 72)
		if strings.Count(rendered, "\n") > 20 {
			m.JumpToPage(i)
			foundLongPage = true
			break
		}
	}

	if !foundLongPage {
		t.Fatal("No structured tutorial page with >20 lines found for scroll testing")
	}

	// Scroll down multiple times
	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	}

	// Scroll offset should have increased
	if m.scrollOffset == 0 {
		t.Error("Scrolling should increase scroll offset for long content")
	}

	// Scroll to end
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})

	// Scroll up
	for i := 0; i < 5; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	}
}

func TestPageSections(t *testing.T) {
	pages := defaultTutorialPages()

	// Verify expected sections exist
	expectedSections := []string{"Introduction", "Core Concepts", "Reference"}
	sectionCounts := make(map[string]int)

	for _, page := range pages {
		sectionCounts[page.Section]++
	}

	for _, section := range expectedSections {
		if sectionCounts[section] == 0 {
			t.Errorf("Expected section %q to have pages", section)
		}
	}
}

func TestPageContextFiltering(t *testing.T) {
	m := newTestTutorialModel()

	// Test filtering by context
	contexts := []string{"list", "board", "graph", "history"}

	for _, ctx := range contexts {
		t.Run(ctx, func(t *testing.T) {
			m.SetContextMode(true)
			m.SetContext(ctx)

			visiblePages := m.visiblePages()

			// Should have some visible pages (or pages with empty contexts)
			// Every page with empty Contexts should be visible
			for _, page := range visiblePages {
				if len(page.Contexts) > 0 {
					found := false
					for _, c := range page.Contexts {
						if c == ctx {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Page %s visible in context %s but doesn't include it", page.ID, ctx)
					}
				}
			}
		})
	}

	// Reset
	m.SetContextMode(false)
}

func TestGlamourRenderingAllPages(t *testing.T) {
	m := newTestTutorialModel()
	m.SetSize(100, 60) // Large dimensions for full rendering

	// Verify markdown renderer is working
	if m.markdownRenderer == nil {
		t.Fatal("Markdown renderer should be initialized")
	}

	// Render each page and check for basic structure
	for i, page := range m.pages {
		t.Run(page.ID, func(t *testing.T) {
			m.JumpToPage(i)
			view := m.View()

			// View should not be empty
			if view == "" {
				t.Errorf("Page %s rendered empty view", page.ID)
			}

			// View should include the page title
			if !strings.Contains(view, page.Title) {
				t.Errorf("Page %s view missing title %q", page.ID, page.Title)
			}
		})
	}
}

func TestTutorialContent_PathsAndKeysMatchRuntime(t *testing.T) {
	allowedPaths := map[string]bool{
		".bv/workspace.yaml":  true,
		".bv/recipes.yaml":    true,
		".bv/hooks.yaml":      true,
		".bv/drift.yaml":      true,
		".bv/baseline.json":   true,
		".bv/semantic":        true,
		".beads/issues.jsonl": true,
		".beads/beads.jsonl":  true,
		".beads/beads.db":     true,
		".beads/config.yaml":  true,
	}

	pathRe := regexp.MustCompile(`(?:\.bv|\.beads)/[A-Za-z0-9_.-]+`)
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}

	// 1. Scan rendered output of every structured page AND raw tutorial_content.go source
	// for any .bv/... or .beads/... file paths and reject any unrecognised filename.
	seenPaths := map[string]bool{}
	var allRendered strings.Builder
	for _, page := range structuredTutorialPages() {
		rendered := ansi.Strip(RenderStructuredPage(page, theme, 120))
		allRendered.WriteString(rendered)
		allRendered.WriteByte('\n')
		for _, rawMatch := range pathRe.FindAllString(rendered, -1) {
			cleaned := strings.TrimRight(rawMatch, ".,;:)")
			seenPaths[cleaned] = true
			if !allowedPaths[cleaned] {
				t.Errorf("page %s references non-canonical path %q (allowed: %v)", page.ID, cleaned, allowedPaths)
			}
		}
	}

	srcBytes, err := os.ReadFile("tutorial_content.go")
	if err != nil {
		t.Fatalf("read tutorial_content.go: %v", err)
	}
	srcText := string(srcBytes)
	for _, rawMatch := range pathRe.FindAllString(srcText, -1) {
		cleaned := strings.TrimRight(rawMatch, ".,;:)")
		seenPaths[cleaned] = true
		if !allowedPaths[cleaned] {
			t.Errorf("tutorial_content.go references non-canonical path %q", cleaned)
		}
	}
	if !seenPaths[".bv/workspace.yaml"] || !seenPaths[".bv/recipes.yaml"] || !seenPaths[".beads/issues.jsonl"] {
		t.Errorf("expected tutorial_content.go to document .bv/workspace.yaml, .bv/recipes.yaml, and .beads/issues.jsonl; saw %v", seenPaths)
	}

	// 2. Guard against factual regressions in tutorial_content.go.
	forbiddenClaims := []string{
		".beads/workspace.json",
		".beads/recipes.json",
		"Shift+L",
		"Workspace-wide search",
		"Sprint Ready",
		"Blocked Review",
		"Stale Items",
		"AI-powered prioritization",
		"Focus on subgraph",
		"o/c/r/a",
		"All (reset filter)",
	}
	renderedCorpus := allRendered.String()
	for _, bad := range forbiddenClaims {
		if strings.Contains(srcText, bad) || strings.Contains(renderedCorpus, bad) {
			t.Errorf("tutorial_content.go still contains factual falsehood %q", bad)
		}
	}

	// 3. Assert every runtime Model.CurrentContext() state matches at least one
	// explicitly tagged tutorial page in context mode.
	for _, ctx := range AllContexts() {
		tut := newTestTutorialModel()
		tut.SetContext(string(ctx))
		tut.SetContextMode(true)
		visible := tut.visiblePages()
		if len(visible) == 0 {
			t.Fatalf("context %q has zero visible pages", ctx)
		}
		matched := false
		for _, c := range visible[0].Contexts {
			if c == string(ctx) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("context %q fell back to untagged page %q instead of a context-tagged page", ctx, visible[0].ID)
		}
	}

	// 4. Assert shortcuts_sidebar.go produces distinct, non-"list" contexts and
	// view-specific bindings for Tree, FlowMatrix, Sprint, Attention, and VelocityComparison.
	targetFocuses := []struct {
		name         string
		focus        focus
		wantContext  string
		wantCategory string
		wantKey      string
	}{
		{"Tree", focusTree, "tree", "Tree", "E"},
		{"FlowMatrix", focusFlowMatrix, "flow", "Flow", "f"},
		{"Sprint", focusSprint, "sprint", "Sprint", "P"},
		{"Attention", focusAttention, "attention", "Attention", "1-9"},
		{"VelocityComparison", focusVelocityComparison, "velocity", "Velocity", "v"},
	}

	seenContexts := map[string]string{}
	model := setupTestModel(t)
	listBindings := model.keyRegistry.AllBindingsForFocus(focusList)

	for _, tf := range targetFocuses {
		ctx := ContextFromFocus(tf.focus)
		if ctx == "list" || ctx == "" {
			t.Errorf("%s (%v) mapped to generic context %q; want %q", tf.name, tf.focus, ctx, tf.wantContext)
		}
		if ctx != tf.wantContext {
			t.Errorf("%s ContextFromFocus=%q, want %q", tf.name, ctx, tf.wantContext)
		}
		if prev, dup := seenContexts[ctx]; dup {
			t.Errorf("%s shares context %q with %s; must be distinct", tf.name, ctx, prev)
		}
		seenContexts[ctx] = tf.name

		// Check registry-backed sidebar sections
		sidebar := NewShortcutsSidebar(theme)
		sidebar.SetSize(34, 60)
		sidebar.SetKeyRegistry(model.keyRegistry)
		sidebar.SetFocus(tf.focus)
		regRendered := ansi.Strip(sidebar.View())
		if !strings.Contains(regRendered, tf.wantCategory) {
			t.Errorf("%s registry-backed sidebar missing category %q:\n%s", tf.name, tf.wantCategory, regRendered)
		}
		if !model.keyRegistry.HasBinding(tf.focus, tf.wantKey) {
			t.Errorf("%s missing registered key %q in KeyRegistry", tf.name, tf.wantKey)
		}
		focusBindings := model.keyRegistry.AllBindingsForFocus(tf.focus)
		if len(focusBindings) == len(listBindings) {
			same := true
			for i := range focusBindings {
				if focusBindings[i].Key != listBindings[i].Key || focusBindings[i].Desc != listBindings[i].Desc {
					same = false
					break
				}
			}
			if same {
				t.Errorf("%s has identical bindings to focusList", tf.name)
			}
		}

		// Also check fallback hardcoded sidebar sections
		fallback := NewShortcutsSidebar(theme)
		fallback.SetSize(34, 60)
		fallback.SetFocus(tf.focus)
		fallbackRendered := ansi.Strip(fallback.View())
		if strings.Contains(fallbackRendered, "Recipe picker") {
			t.Errorf("%s fallback sidebar leaked list-only 'Recipe picker' action:\n%s", tf.name, fallbackRendered)
		}
	}
}
