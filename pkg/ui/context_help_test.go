package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestAllContextsMapToContextTaggedTutorialPages(t *testing.T) {
	pages := defaultTutorialPages()
	if len(pages) == 0 {
		t.Fatal("defaultTutorialPages() returned empty slice")
	}

	for _, ctx := range AllContexts() {
		t.Run(string(ctx), func(t *testing.T) {
			indices := ctx.TutorialPages()
			if len(indices) == 0 {
				t.Fatalf("context %q returned zero tutorial page indices", ctx)
			}

			tut := NewTutorialModel(DefaultTheme(lipgloss.NewRenderer(nil)))
			tut.SetContext(string(ctx))
			tut.SetContextMode(true)

			visible := tut.visiblePages()
			if len(visible) == 0 {
				t.Fatalf("context %q produced zero visible tutorial pages", ctx)
			}
			if tut.currentPage != 0 {
				t.Fatalf("context %q: expected currentPage=0, got %d", ctx, tut.currentPage)
			}

			hasExplicitContext := false
			for _, c := range visible[0].Contexts {
				if c == string(ctx) {
					hasExplicitContext = true
					break
				}
			}
			if !hasExplicitContext {
				t.Errorf("context %q first visible page %q (contexts=%v) does not explicitly tag %q",
					ctx, visible[0].ID, visible[0].Contexts, ctx)
			}
		})
	}
}

func TestContextTutorial_ToggleFullTOCAndCloseKeys(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	tut := NewTutorialModel(theme)
	tut.SetSize(100, 40)
	tut.SetContext(string(ContextTree))
	tut.SetContextMode(true)

	filtered := tut.visiblePages()
	if len(filtered) == 0 || len(filtered) >= len(tut.pages) {
		t.Fatalf("expected strict subset of pages in tree context mode, got %d of %d", len(filtered), len(tut.pages))
	}
	firstFilteredID := tut.CurrentPageID()
	if firstFilteredID != "views-graph" {
		t.Fatalf("expected tree context to open 'views-graph', got %q", firstFilteredID)
	}

	view := tut.View()
	if !strings.Contains(view, "[context: tree]") {
		t.Errorf("expected context badge '[context: tree]' in header, got:\n%s", view)
	}
	if !strings.Contains(view, "all pages") {
		t.Errorf("expected footer toggle hint 'a all pages' in context mode, got:\n%s", view)
	}

	// Pressing 'a' expands to the full tutorial TOC while keeping the current page selected.
	tut, _ = tut.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if tut.IsContextMode() {
		t.Fatal("expected 'a' to disable context filter")
	}
	if len(tut.visiblePages()) != len(tut.pages) {
		t.Fatalf("expected all %d pages visible after 'a', got %d", len(tut.pages), len(tut.visiblePages()))
	}
	if got := tut.CurrentPageID(); got != firstFilteredID {
		t.Errorf("expected current page ID %q preserved when expanding to all pages, got %q", firstFilteredID, got)
	}

	// Pressing 'a' again re-filters back to the active context.
	tut, _ = tut.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !tut.IsContextMode() {
		t.Fatal("expected second 'a' to re-enable context filter")
	}
	if len(tut.visiblePages()) != len(filtered) {
		t.Fatalf("expected %d filtered pages, got %d", len(filtered), len(tut.visiblePages()))
	}

	// Verify '~', 'q', and Esc all close the context tutorial cleanly.
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("~")},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEsc},
	} {
		clone := tut
		clone.ResetClose()
		clone, _ = clone.Update(key)
		if !clone.ShouldClose() {
			t.Errorf("expected key %q to close TutorialModel", key.String())
		}
	}
}

func TestContextTutorial_UnknownContextFallsBackGracefully(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	tut := NewTutorialModel(theme)
	tut.SetSize(80, 30)
	tut.SetContext("nonexistent-custom-context")
	tut.SetContextMode(true)

	visible := tut.visiblePages()
	if len(visible) != len(tut.pages) {
		t.Fatalf("unknown context should fall back to all %d pages, got %d", len(tut.pages), len(visible))
	}
	if out := tut.View(); out == "" {
		t.Fatal("expected non-empty rendered output on fallback")
	}
}

func TestContextTutorial_RendersAcrossDimensions(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	dims := []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{50, 20},
		{30, 10},
	}

	for _, d := range dims {
		t.Run(fmt.Sprintf("%dx%d", d.w, d.h), func(t *testing.T) {
			tut := NewTutorialModel(theme)
			tut.SetContext(string(ContextBoard))
			tut.SetContextMode(true)
			tut.SetSize(d.w, d.h)
			if out := tut.View(); out == "" {
				t.Errorf("empty tutorial render at %dx%d", d.w, d.h)
			}
		})
	}
}
