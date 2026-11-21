package ui_test

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

// ═══════════════════════════════════════════════════════════════════════════════
// View Transition Integration Tests (bv-i3ls)
// Tests verifying state preservation and behavior across view switches
// ═══════════════════════════════════════════════════════════════════════════════

// Helper to create a KeyMsg for a string key
func integrationKeyMsg(key string) tea.KeyMsg {
	return tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(key),
	}
}

// Helper to create special key messages
func integrationSpecialKey(k tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: k}
}

// createTestIssues creates a set of test issues for integration tests
func createTestIssues(count int) []model.Issue {
	issues := make([]model.Issue, count)
	statuses := []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusBlocked, model.StatusClosed}
	priorities := []int{0, 1, 2, 3}

	for i := 0; i < count; i++ {
		issues[i] = model.Issue{
			ID:        "test-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Title:     "Test Issue",
			Status:    statuses[i%len(statuses)],
			Priority:  priorities[i%len(priorities)],
			IssueType: model.TypeTask,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
		}
	}
	return issues
}

// ═══════════════════════════════════════════════════════════════════════════════
// Basic View Switching Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestViewTransitionListToTree verifies List → Tree → List transition
func TestViewTransitionListToTree(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Should start in list view
	if m.FocusState() != "list" {
		t.Errorf("Expected initial focus 'list', got %q", m.FocusState())
	}

	// Press 'E' to toggle tree view
	newM, _ := m.Update(integrationKeyMsg("E"))
	m = newM.(*ui.Model)

	if m.FocusState() != "tree" {
		t.Errorf("After 'E', expected focus 'tree', got %q", m.FocusState())
	}

	// Press 'E' again to toggle back to list
	newM, _ = m.Update(integrationKeyMsg("E"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'E', expected focus 'list', got %q", m.FocusState())
	}
}

// TestViewTransitionListToBoard verifies List → Board → List transition
func TestViewTransitionListToBoard(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Press 'b' to toggle board view
	newM, _ := m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)

	if !m.IsBoardView() {
		t.Error("IsBoardView should be true after 'b'")
	}

	// Press 'b' again to toggle back
	newM, _ = m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)

	if m.IsBoardView() {
		t.Error("IsBoardView should be false after second 'b'")
	}
	if m.FocusState() != "list" {
		t.Errorf("Expected focus 'list' after board toggle, got %q", m.FocusState())
	}
}

// TestViewTransitionListToGraph verifies List → Graph → List transition
func TestViewTransitionListToGraph(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Press 'g' to toggle graph view
	newM, _ := m.Update(integrationKeyMsg("g"))
	m = newM.(*ui.Model)

	if !m.IsGraphView() {
		t.Error("IsGraphView should be true after 'g'")
	}

	// Press 'g' again to toggle back
	newM, _ = m.Update(integrationKeyMsg("g"))
	m = newM.(*ui.Model)

	if m.IsGraphView() {
		t.Error("IsGraphView should be false after second 'g'")
	}
}

// TestViewTransitionFullCycle verifies List → Graph → Board → Tree → List cycle
// Note: 'g' in board uses gg-combo (async timeout), so we test graph→board instead.
func TestViewTransitionFullCycle(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Enter graph view from list (immediate toggle)
	newM, _ := m.Update(integrationKeyMsg("g"))
	m = newM.(*ui.Model)
	if !m.IsGraphView() {
		t.Error("Should be in graph view")
	}

	// Enter board view (clears graph)
	newM, _ = m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)
	if !m.IsBoardView() {
		t.Error("Should be in board view")
	}

	// Enter tree view (clears board)
	newM, _ = m.Update(integrationKeyMsg("E"))
	m = newM.(*ui.Model)
	if m.FocusState() != "tree" {
		t.Errorf("Should be in tree view, got %q", m.FocusState())
	}

	// Return to list via 'E' toggle (tree specific exit key)
	newM, _ = m.Update(integrationKeyMsg("E"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After 'E' from tree, expected 'list', got %q", m.FocusState())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// State Preservation Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestViewTransitionClearsOtherViews verifies entering one view clears others
// Note: 'g' in board uses gg-combo (async timeout), so we test graph→board instead.
func TestViewTransitionClearsOtherViews(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Enter graph view from list (immediate toggle)
	newM, _ := m.Update(integrationKeyMsg("g"))
	m = newM.(*ui.Model)

	if !m.IsGraphView() {
		t.Error("Should be in graph view")
	}

	// Enter board view (should clear graph)
	newM, _ = m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)

	if m.IsGraphView() {
		t.Error("Graph view should be cleared when entering board")
	}
	if !m.IsBoardView() {
		t.Error("Should be in board view")
	}

	// Enter tree view (should clear board)
	newM, _ = m.Update(integrationKeyMsg("E"))
	m = newM.(*ui.Model)

	if m.IsBoardView() {
		t.Error("Board view should be cleared when entering tree")
	}
	if m.FocusState() != "tree" {
		t.Error("Should be in tree view")
	}
}

// TestViewTransitionFilterPreserved verifies filter state is preserved across views
func TestViewTransitionFilterPreserved(t *testing.T) {
	issues := createTestIssues(10)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	// Apply a filter
	m.SetFilter("open")
	initialCount := len(m.FilteredIssues())

	// Switch to board and back
	newM, _ := m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)

	newM, _ = m.Update(integrationKeyMsg("b"))
	m = newM.(*ui.Model)

	// Filter should still be active
	afterCount := len(m.FilteredIssues())
	if afterCount != initialCount {
		t.Errorf("Filter not preserved: before=%d, after=%d", initialCount, afterCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Edge Case Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestViewTransitionEmptyIssues verifies view switching with no issues renders cleanly and settles in help
func TestViewTransitionEmptyIssues(t *testing.T) {
	m := ui.NewModel([]model.Issue{}, nil, "")
	t.Cleanup(func() { m.Stop() })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(*ui.Model)

	keys := []string{"E", "b", "g", "a", "i", "?"}
	for _, k := range keys {
		newM, _ = m.Update(integrationKeyMsg(k))
		m = newM.(*ui.Model)
		if m.View() == "" {
			t.Fatalf("View() returned empty string after key %q on empty issue list", k)
		}
	}

	if got := m.FocusState(); got != "help" {
		t.Errorf("expected final focus state %q after '?', got %q", "help", got)
	}
}

// TestViewTransitionEscBehavior verifies Esc behavior varies by view
func TestViewTransitionEscBehavior(t *testing.T) {
	issues := createTestIssues(10)

	// Each view has specific exit behavior
	// Note: In tree view, 'E' is the toggle key; 'esc' may trigger quit confirm
	t.Run("tree_E_returns_to_list", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("E"))
		m = newM.(*ui.Model)

		// 'E' from tree should return to list (toggle behavior)
		newM, _ = m.Update(integrationKeyMsg("E"))
		m = newM.(*ui.Model)

		if m.FocusState() != "list" {
			t.Errorf("'E' from tree should return to list, got %q", m.FocusState())
		}
	})

	t.Run("board_toggle_exits_board", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("b"))
		m = newM.(*ui.Model)

		// Press 'b' again to toggle off board
		newM, _ = m.Update(integrationKeyMsg("b"))
		m = newM.(*ui.Model)

		if m.IsBoardView() {
			t.Error("'b' should toggle off board view")
		}
	})

	t.Run("graph_toggle_exits_graph", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("g"))
		m = newM.(*ui.Model)

		// Press 'g' again to toggle off graph
		newM, _ = m.Update(integrationKeyMsg("g"))
		m = newM.(*ui.Model)

		if m.IsGraphView() {
			t.Error("'g' should toggle off graph view")
		}
	})

	t.Run("actionable_toggle_exits", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("a"))
		m = newM.(*ui.Model)

		// Press 'a' again to toggle off
		newM, _ = m.Update(integrationKeyMsg("a"))
		m = newM.(*ui.Model)

		if m.IsActionableView() {
			t.Error("'a' should toggle off actionable view")
		}
	})
}

// TestViewToggleExitBehavior verifies toggle keys exit their respective views
func TestViewToggleExitBehavior(t *testing.T) {
	issues := createTestIssues(10)

	// Tree view uses 'E' to toggle in/out (q would trigger quit confirm)
	t.Run("tree_E_toggle", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		// Enter tree
		newM, _ := m.Update(integrationKeyMsg("E"))
		m = newM.(*ui.Model)
		if m.FocusState() != "tree" {
			t.Errorf("Expected tree, got %q", m.FocusState())
		}
		// Exit with E
		newM, _ = m.Update(integrationKeyMsg("E"))
		m = newM.(*ui.Model)
		if m.FocusState() != "list" {
			t.Errorf("'E' should toggle back to list, got %q", m.FocusState())
		}
	})

	// Board view uses 'b' to toggle
	t.Run("board_b_toggle", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("b"))
		m = newM.(*ui.Model)
		if !m.IsBoardView() {
			t.Error("Should be in board view")
		}
		newM, _ = m.Update(integrationKeyMsg("b"))
		m = newM.(*ui.Model)
		if m.IsBoardView() {
			t.Error("'b' should toggle off board")
		}
	})

	// Graph view uses 'g' to toggle
	t.Run("graph_g_toggle", func(t *testing.T) {
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(func() { m.Stop() })
		newM, _ := m.Update(integrationKeyMsg("g"))
		m = newM.(*ui.Model)
		if !m.IsGraphView() {
			t.Error("Should be in graph view")
		}
		newM, _ = m.Update(integrationKeyMsg("g"))
		m = newM.(*ui.Model)
		if m.IsGraphView() {
			t.Error("'g' should toggle off graph")
		}
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// Rapid Switching Stress Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestRapidViewSwitching verifies invariants hold during rapid view changes
func TestRapidViewSwitching(t *testing.T) {
	issues := createTestIssues(50)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(*ui.Model)

	keys := []string{"E", "b", "g", "a", "i", "E", "b", "g"}

	// Perform 100 iterations of view switching
	for i := 0; i < 100; i++ {
		for _, k := range keys {
			newM, _ = m.Update(integrationKeyMsg(k))
			m = newM.(*ui.Model)
		}
	}

	if m.FocusState() == "" {
		t.Fatal("FocusState() empty after rapid view switching")
	}
	if m.View() == "" {
		t.Fatal("View() empty after rapid view switching")
	}
	for i := 0; i < 4 && m.FocusState() != "list"; i++ {
		newM, _ = m.Update(integrationSpecialKey(tea.KeyEsc))
		m = newM.(*ui.Model)
	}
	if got := m.FocusState(); got != "list" {
		t.Errorf("expected Esc unwinding to return to list after rapid switching, got %q", got)
	}
}

// TestRapidViewSwitchingWithNavigation verifies navigation and list recovery during rapid switches
func TestRapidViewSwitchingWithNavigation(t *testing.T) {
	issues := createTestIssues(50)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(*ui.Model)

	// Mix view switches with navigation
	actions := []tea.KeyMsg{
		integrationKeyMsg("E"),            // Enter tree
		integrationKeyMsg("j"),            // Move down
		integrationKeyMsg("j"),            // Move down
		integrationKeyMsg("b"),            // Enter board (from tree)
		integrationKeyMsg("l"),            // Move right in board
		integrationKeyMsg("g"),            // Enter graph
		integrationKeyMsg("j"),            // Move down in graph
		integrationSpecialKey(tea.KeyEsc), // Exit to list
		integrationKeyMsg("j"),            // Move down in list
	}

	for i := 0; i < 50; i++ {
		for _, k := range actions {
			newM, _ = m.Update(k)
			m = newM.(*ui.Model)
		}
	}

	if got := m.FocusState(); got != "list" {
		t.Errorf("expected final focus state %q after navigation loop, got %q", "list", got)
	}
	if m.IsBoardView() || m.IsGraphView() || m.IsActionableView() {
		t.Errorf("expected secondary view flags cleared after returning to list: board=%v graph=%v actionable=%v",
			m.IsBoardView(), m.IsGraphView(), m.IsActionableView())
	}
	if m.View() == "" {
		t.Error("View() returned empty output after rapid switching + navigation")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Performance Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestViewSwitchingPerformance verifies reasonable performance for view switching
func TestViewSwitchingPerformance(t *testing.T) {
	issues := createTestIssues(100)
	m := ui.NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })

	keys := []string{"E", "b", "g", "E", "b", "g"}

	start := time.Now()

	// 100 full cycles = 600 view switches
	for i := 0; i < 100; i++ {
		for _, k := range keys {
			newM, _ := m.Update(integrationKeyMsg(k))
			m = newM.(*ui.Model)
		}
	}

	elapsed := time.Since(start)

	// Should complete quickly, but allow some headroom when `go test ./...` runs
	// packages in parallel and the machine is under load.
	if elapsed > 2*time.Second {
		t.Errorf("View switching too slow: %v for 600 switches", elapsed)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Help View Integration Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestHelpViewTransition verifies help view can be opened from any view
func TestHelpViewTransition(t *testing.T) {
	issues := createTestIssues(10)

	views := []struct {
		name          string
		enterKey      string
		expectedFocus string
	}{
		{"list", "", "list"},
		{"tree", "E", "tree"},
		{"board", "b", "board"},
		{"graph", "g", "graph"},
	}

	for _, v := range views {
		t.Run(v.name, func(t *testing.T) {
			m := ui.NewModel(issues, nil, "")
			t.Cleanup(func() { m.Stop() })

			// Enter the base view
			if v.enterKey != "" {
				newM, _ := m.Update(integrationKeyMsg(v.enterKey))
				m = newM.(*ui.Model)
			}

			// Open help with '?'
			newM, _ := m.Update(integrationKeyMsg("?"))
			m = newM.(*ui.Model)

			if m.FocusState() != "help" {
				t.Errorf("Expected help focus from %s view, got %q", v.name, m.FocusState())
			}

			// Exit help with Esc
			newM, _ = m.Update(integrationSpecialKey(tea.KeyEsc))
			m = newM.(*ui.Model)

			if got := m.FocusState(); got != v.expectedFocus {
				t.Errorf("Focus after help from %s = %q, want %q", v.name, got, v.expectedFocus)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// View Rendering Integration Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestAllViewsRenderWithoutPanic verifies all views can render without panic
func TestAllViewsRenderWithoutPanic(t *testing.T) {
	issues := createTestIssues(20)

	views := []struct {
		name     string
		enterKey string
	}{
		{"list", ""},
		{"tree", "E"},
		{"board", "b"},
		{"graph", "g"},
		{"actionable", "a"},
		{"insights", "i"},
		{"help", "?"},
	}

	for _, v := range views {
		t.Run(v.name, func(t *testing.T) {
			m := ui.NewModel(issues, nil, "")
			t.Cleanup(func() { m.Stop() })

			// Enter the view
			if v.enterKey != "" {
				newM, _ := m.Update(integrationKeyMsg(v.enterKey))
				m = newM.(*ui.Model)
			}

			// Render should not panic
			output := m.View()
			if output == "" {
				t.Errorf("View() returned empty for %s view", v.name)
			}
		})
	}
}

// TestViewRenderingAtDifferentSizes verifies views render at various terminal sizes
func TestViewRenderingAtDifferentSizes(t *testing.T) {
	issues := createTestIssues(20)

	sizes := []struct {
		width, height int
	}{
		{80, 24},
		{120, 30},
		{160, 40},
		{40, 15},  // Narrow
		{200, 50}, // Wide
	}

	views := []string{"", "E", "b", "g"}

	for _, size := range sizes {
		for _, viewKey := range views {
			name := "list"
			if viewKey != "" {
				name = viewKey
			}

			t.Run(fmt.Sprintf("%s_%dx%d", name, size.width, size.height), func(t *testing.T) {
				m := ui.NewModel(issues, nil, "")
				t.Cleanup(func() { m.Stop() })

				// Set size
				newM, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				m = newM.(*ui.Model)

				// Enter view
				if viewKey != "" {
					newM, _ = m.Update(integrationKeyMsg(viewKey))
					m = newM.(*ui.Model)
				}

				// Render should not panic
				output := m.View()
				if output == "" {
					t.Errorf("View() returned empty for %s at %dx%d", name, size.width, size.height)
				}
			})
		}
	}
}
