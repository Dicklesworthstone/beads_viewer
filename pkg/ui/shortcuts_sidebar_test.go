package ui

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestNewShortcutsSidebar(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	sidebar := NewShortcutsSidebar(theme)

	if sidebar.width != 34 {
		t.Errorf("Expected width 34, got %d", sidebar.width)
	}
	if sidebar.context != "list" {
		t.Errorf("Expected context 'list', got %q", sidebar.context)
	}
}

func TestShortcutsSidebarSetContext(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	sidebar := NewShortcutsSidebar(theme)

	sidebar.SetContext("graph")
	if sidebar.context != "graph" {
		t.Errorf("Expected context 'graph', got %q", sidebar.context)
	}

	sidebar.SetContext("insights")
	if sidebar.context != "insights" {
		t.Errorf("Expected context 'insights', got %q", sidebar.context)
	}
}

func TestShortcutsSidebarScrolling(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	sidebar := NewShortcutsSidebar(theme)

	// Initial scroll offset should be 0
	if sidebar.scrollOffset != 0 {
		t.Errorf("Expected initial scroll 0, got %d", sidebar.scrollOffset)
	}

	// Scroll down
	sidebar.ScrollDown()
	if sidebar.scrollOffset != 1 {
		t.Errorf("Expected scroll 1 after ScrollDown, got %d", sidebar.scrollOffset)
	}

	// Scroll up
	sidebar.ScrollUp()
	if sidebar.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 after ScrollUp, got %d", sidebar.scrollOffset)
	}

	// Scroll up at top should stay at 0
	sidebar.ScrollUp()
	if sidebar.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 at top, got %d", sidebar.scrollOffset)
	}

	// Page down
	sidebar.ScrollPageDown()
	if sidebar.scrollOffset != 10 {
		t.Errorf("Expected scroll 10 after PageDown, got %d", sidebar.scrollOffset)
	}

	// Page up
	sidebar.ScrollPageUp()
	if sidebar.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 after PageUp, got %d", sidebar.scrollOffset)
	}

	// Reset
	sidebar.scrollOffset = 5
	sidebar.ResetScroll()
	if sidebar.scrollOffset != 0 {
		t.Errorf("Expected scroll 0 after Reset, got %d", sidebar.scrollOffset)
	}
}

func TestShortcutsSidebarView(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Base:      lipgloss.NewStyle(),
	}
	sidebar := NewShortcutsSidebar(theme)
	sidebar.SetSize(28, 30)

	view := sidebar.View()
	if view == "" {
		t.Error("Expected non-empty view")
	}

	// Should contain title
	if !strings.Contains(view, "Shortcuts") {
		t.Error("Expected view to contain 'Shortcuts'")
	}

	// Should contain Navigation section
	if !strings.Contains(view, "Navigation") {
		t.Error("Expected view to contain 'Navigation'")
	}
}

func TestShortcutsSidebarContextFiltering(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Base:      lipgloss.NewStyle(),
	}

	// Test graph context
	sidebar := NewShortcutsSidebar(theme)
	sidebar.SetSize(28, 50)
	sidebar.SetContext("graph")
	view := sidebar.View()

	if !strings.Contains(view, "Graph") {
		t.Error("Expected graph context to show Graph section")
	}

	// Test insights context
	sidebar.SetContext("insights")
	view = sidebar.View()

	if !strings.Contains(view, "Insights") {
		t.Error("Expected insights context to show Insights section")
	}
}

func TestContextFromFocus(t *testing.T) {
	tests := []struct {
		focus    focus
		expected string
	}{
		{focusList, "list"},
		{focusDetail, "detail"},
		{focusBoard, "board"},
		{focusGraph, "graph"},
		{focusInsights, "insights"},
		{focusHistory, "history"},
		{focusActionable, "actionable"},
		{focusLabelDashboard, "label"},
		{focusTree, "tree"},
		{focusFlowMatrix, "flow"},
		{focusSprint, "sprint"},
		{focusAttention, "attention"},
		{focusVelocityComparison, "velocity"},
		{focusHelp, "list"}, // Default fallback
	}

	for _, tt := range tests {
		got := ContextFromFocus(tt.focus)
		if got != tt.expected {
			t.Errorf("ContextFromFocus(%v) = %q, want %q", tt.focus, got, tt.expected)
		}
	}
}

func TestShortcutsSidebarWidth(t *testing.T) {
	theme := Theme{Renderer: lipgloss.DefaultRenderer()}
	sidebar := NewShortcutsSidebar(theme)

	if sidebar.Width() != 34 {
		t.Errorf("Expected Width() = 34, got %d", sidebar.Width())
	}
}

// TestShortcutsSidebar_MatchesRegistry verifies that sidebar uses registry bindings
// when available, falling back to hardcoded data when registry is empty (bv-xl6g).
func TestShortcutsSidebar_MatchesRegistry(t *testing.T) {
	theme := Theme{
		Renderer:  lipgloss.DefaultRenderer(),
		Primary:   lipgloss.AdaptiveColor{Light: "#00ff00", Dark: "#00ff00"},
		Secondary: lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"},
		Base:      lipgloss.NewStyle(),
	}

	t.Run("uses hardcoded when registry empty", func(t *testing.T) {
		sidebar := NewShortcutsSidebar(theme)
		sidebar.SetSize(34, 40)
		registry := NewKeyRegistry() // Empty registry
		sidebar.SetKeyRegistry(registry)
		sidebar.SetFocus(focusList)

		view := sidebar.View()
		// Should use hardcoded sections - expect Navigation
		if !strings.Contains(view, "Navigation") {
			t.Error("Expected hardcoded 'Navigation' section when registry empty")
		}
	})

	t.Run("uses registry when bindings exist", func(t *testing.T) {
		sidebar := NewShortcutsSidebar(theme)
		sidebar.SetSize(34, 40)
		registry := NewKeyRegistry()

		// Register test bindings with a unique category
		registry.RegisterBinding(KeyBinding{
			Focus:    focusList,
			Key:      "test-key",
			Desc:     "Test action",
			Category: "TestCategory",
		})

		sidebar.SetKeyRegistry(registry)
		sidebar.SetFocus(focusList)

		view := sidebar.View()
		// Should use registry bindings - expect TestCategory
		if !strings.Contains(view, "TestCategory") {
			t.Error("Expected registry 'TestCategory' section when bindings registered")
		}
		if !strings.Contains(view, "test-key") {
			t.Error("Expected 'test-key' from registry bindings")
		}
	})

	t.Run("SetFocus updates both focus and context", func(t *testing.T) {
		sidebar := NewShortcutsSidebar(theme)
		sidebar.SetFocus(focusGraph)

		if sidebar.focusHint != focusGraph {
			t.Errorf("Expected focusHint = focusGraph, got %v", sidebar.focusHint)
		}
		if sidebar.context != "graph" {
			t.Errorf("Expected context = 'graph', got %q", sidebar.context)
		}
	})
}

// TestShortcutsSidebarReservesLayoutWidth is the regression test for issue #168:
// toggling the shortcuts sidebar (`;`) must reserve its own fixed-width column so
// the main list/detail panes reflow into the remaining width. Previously the body
// was rendered at the full terminal width and the sidebar was appended after it,
// producing a composed layout wider than the terminal — which a real terminal
// then wraps back into the panes, interleaving the sidebar with issue rows.
//
// It is driven through the real key path + full View() so it catches any future
// drift between the body sizing and the sidebar width. The invariant asserted is
// layout-level (no rendered line exceeds m.width) and so does not depend on a
// live TTY.
func TestShortcutsSidebarReservesLayoutWidth(t *testing.T) {
	maxLineWidth := func(s string) int {
		mx := 0
		for _, ln := range strings.Split(s, "\n") {
			if w := lipgloss.Width(ln); w > mx {
				mx = w
			}
		}
		return mx
	}

	cases := []struct {
		name      string
		w, h      int
		wantSplit bool
	}{
		{"split_narrow_110", 110, 30, true},
		{"split_120", 120, 30, true},
		{"split_wide_200", 200, 40, true},
		{"mobile_80", 80, 30, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedModel(t, mouseTestIssues(40), tc.w, tc.h)
			if m.isSplitView != tc.wantSplit {
				t.Fatalf("w=%d isSplitView=%v want %v", tc.w, m.isSplitView, tc.wantSplit)
			}

			// Before toggling, the layout must already fit (baseline).
			if mw := maxLineWidth(m.View()); mw > m.width {
				t.Fatalf("baseline (no sidebar): max line width %d > terminal width %d", mw, m.width)
			}

			// Toggle the sidebar on via the real `;` key path.
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";")})
			m = updated.(*Model)
			if !m.showShortcutsSidebar {
				t.Fatalf("`;` did not enable the shortcuts sidebar")
			}

			view := m.View()
			if mw := maxLineWidth(view); mw > m.width {
				t.Errorf("with sidebar open: max line width %d exceeds terminal width %d (#168 overflow)", mw, m.width)
			}

			// The reflow must keep the sidebar content actually visible rather
			// than clipping it off the right edge: the body should have shrunk by
			// at least the sidebar's reserved column.
			wantBodyWidth := m.width - m.shortcutsSidebar.Width()
			if m.isSplitView {
				// list inner width + 4 (border+padding both sides) is the list
				// panel; it must be well within the reserved body width.
				if m.list.Width()+4 > wantBodyWidth {
					t.Errorf("list panel width %d does not leave room for sidebar (reserved body %d)", m.list.Width()+4, wantBodyWidth)
				}
			} else if m.list.Width() > wantBodyWidth {
				t.Errorf("mobile list width %d exceeds reserved body width %d", m.list.Width(), wantBodyWidth)
			}

			// Toggling off restores the full-width layout.
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";")})
			m = updated.(*Model)
			if m.showShortcutsSidebar {
				t.Fatalf("`;` did not disable the shortcuts sidebar")
			}
			if mw := maxLineWidth(m.View()); mw > m.width {
				t.Errorf("after closing sidebar: max line width %d exceeds terminal width %d", mw, m.width)
			}
		})
	}
}

func TestShortcutsSidebar_CtrlJKGuardBoardDetailAndPrompts(t *testing.T) {
	var longDesc strings.Builder
	for i := 0; i < 60; i++ {
		longDesc.WriteString("Line of markdown detail content for board detail scrolling verification.\n")
	}
	issues := []model.Issue{
		{
			ID:          "BOARD-1",
			Title:       "Board issue with scrollable detail",
			Description: longDesc.String(),
			Status:      model.StatusOpen,
			Priority:    1,
		},
		{
			ID:       "BOARD-2",
			Title:    "Second board issue",
			Status:   model.StatusInProgress,
			Priority: 2,
		},
	}

	m := sizedModel(t, issues, 180, 24)
	t.Cleanup(m.Stop)

	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	// Open shortcuts sidebar via ';'
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";")})
	if !m.showShortcutsSidebar {
		t.Fatal("expected shortcuts sidebar to be open")
	}

	// Open Board view ('b') and show Board detail ('tab'), then render once to populate detailVP.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.focused != focusBoard || !m.board.IsDetailShown() {
		t.Fatalf("expected focusBoard with detail shown, focused=%v detail=%v", m.focused, m.board.IsDetailShown())
	}
	_ = m.View()

	// 1. Negative control: ctrl+j must scroll Board detail and NOT scroll shortcuts sidebar.
	send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.shortcutsSidebar.scrollOffset != 0 {
		t.Fatalf("ctrl+j with Board detail open must not scroll sidebar, got sidebar scrollOffset=%d", m.shortcutsSidebar.scrollOffset)
	}
	if m.board.detailVP.YOffset == 0 {
		t.Fatal("expected ctrl+j to scroll Board detail viewport down when sidebar is open")
	}

	// 2. Positive control: hide Board detail ('tab'); now ctrl+j / pgdown / pgup / ctrl+k scroll sidebar.
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.board.IsDetailShown() {
		t.Fatal("expected Board detail to hide after second tab")
	}
	send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.shortcutsSidebar.scrollOffset != 1 {
		t.Fatalf("expected ctrl+j with Board detail hidden to scroll sidebar to 1, got %d", m.shortcutsSidebar.scrollOffset)
	}
	send(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.shortcutsSidebar.scrollOffset <= 1 {
		t.Fatalf("expected pgdown to advance sidebar scrollOffset, got %d", m.shortcutsSidebar.scrollOffset)
	}
	send(tea.KeyMsg{Type: tea.KeyPgUp})
	send(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.shortcutsSidebar.scrollOffset != 0 {
		t.Fatalf("expected pgup + ctrl+k to return sidebar scrollOffset to 0, got %d", m.shortcutsSidebar.scrollOffset)
	}

	// 3. Negative controls: active History search, time-travel prompt, and label picker must block sidebar ctrl+j.
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")}) // Exit Board back to list so 'h' opens History
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	makeHistoryReportCurrent(m, createTestHistoryReport())
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.historyView.IsSearching() {
		t.Fatal("expected history search input to be active")
	}
	send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.shortcutsSidebar.scrollOffset != 0 {
		t.Fatalf("ctrl+j during History search must not scroll sidebar, got %d", m.shortcutsSidebar.scrollOffset)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	m.showTimeTravelPrompt = true
	send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.shortcutsSidebar.scrollOffset != 0 {
		t.Fatalf("ctrl+j during time-travel prompt must not scroll sidebar, got %d", m.shortcutsSidebar.scrollOffset)
	}
	m.showTimeTravelPrompt = false

	m.showLabelPicker = true
	send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.shortcutsSidebar.scrollOffset != 0 {
		t.Fatalf("ctrl+j during label picker must not scroll sidebar, got %d", m.shortcutsSidebar.scrollOffset)
	}
	m.showLabelPicker = false
}
