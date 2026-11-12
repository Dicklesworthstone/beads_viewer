package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// The KeyRegistry is the help index behind the shortcuts sidebar: it holds
// documented bindings per focus and never dispatches. Runtime dispatch is
// Model.Update, exercised by the TestKeyDispatch_* tests below.

// TestKeyRegistry_RegisterBindingReplacesSameKey: registering the same key
// twice for one focus keeps a single entry carrying the latest description.
func TestKeyRegistry_RegisterBindingReplacesSameKey(t *testing.T) {
	r := NewKeyRegistry()
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Desc: "old", Category: "Navigation"})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Desc: "new", Category: "Navigation"})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "j", Desc: "board", Category: "Navigation"})

	list := r.AllBindingsForFocus(focusList)
	if len(list) != 1 || list[0].Desc != "new" {
		t.Fatalf("expected one list binding with the replaced description, got %+v", list)
	}
	if got := len(r.AllBindings()); got != 2 {
		t.Fatalf("expected 2 bindings across focuses, got %d", got)
	}
}

// TestKeyRegistryAllBindings verifies that AllBindings returns every binding
// sorted by focus, then category, then key.
func TestKeyRegistryAllBindings(t *testing.T) {
	r := NewKeyRegistry()
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "j", Desc: "Down", Category: "Navigation"})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Desc: "Up", Category: "Navigation"})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "enter", Desc: "Select", Category: "Actions"})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "h", Desc: "Left", Category: "Navigation"})

	bindings := r.AllBindings()
	if len(bindings) != 4 {
		t.Fatalf("AllBindings: expected 4 bindings, got %d", len(bindings))
	}
	for i := 1; i < len(bindings); i++ {
		a, b := bindings[i-1], bindings[i]
		ordered := a.Focus < b.Focus ||
			(a.Focus == b.Focus && a.Category < b.Category) ||
			(a.Focus == b.Focus && a.Category == b.Category && a.Key < b.Key)
		if !ordered {
			t.Errorf("AllBindings not sorted at %d: %+v before %+v", i, a, b)
		}
	}
}

// TestKeyRegistryAllBindingsForFocus verifies that AllBindingsForFocus returns
// only bindings for the specified focus context.
func TestKeyRegistryAllBindingsForFocus(t *testing.T) {
	r := NewKeyRegistry()
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Desc: "Down", Category: "Nav"})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Desc: "Up", Category: "Nav"})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "l", Desc: "Right", Category: "Nav"})

	if got := len(r.AllBindingsForFocus(focusList)); got != 2 {
		t.Errorf("AllBindingsForFocus(focusList): expected 2, got %d", got)
	}
	if got := len(r.AllBindingsForFocus(focusBoard)); got != 1 {
		t.Errorf("AllBindingsForFocus(focusBoard): expected 1, got %d", got)
	}
	if got := len(r.AllBindingsForFocus(focusGraph)); got != 0 {
		t.Errorf("AllBindingsForFocus(focusGraph): expected 0, got %d", got)
	}
}

// TestKeyRegistryHasBinding verifies the HasBinding lookup.
func TestKeyRegistryHasBinding(t *testing.T) {
	r := NewKeyRegistry()
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j"})

	if !r.HasBinding(focusList, "j") {
		t.Error("HasBinding: expected true for registered binding")
	}
	if r.HasBinding(focusList, "k") {
		t.Error("HasBinding: expected false for unregistered key")
	}
	if r.HasBinding(focusBoard, "j") {
		t.Error("HasBinding: expected false for wrong focus")
	}
}

// TestKeyBindingDocs_EveryContextResolves guards the Context strings in
// GetKeyBindingDocs: a typo there would silently drop the key from every
// sidebar and from the README parity check.
func TestKeyBindingDocs_EveryContextResolves(t *testing.T) {
	for _, doc := range GetKeyBindingDocs() {
		if len(focusesForBindingDoc(doc)) == 0 {
			t.Errorf("key %q has context %q which resolves to no focus", doc.Key, doc.Context)
		}
	}
}

func TestNewModelRegistersDocumentedBindings(t *testing.T) {
	m := setupTestModel(t)

	if m.keyRegistry == nil {
		t.Fatal("expected NewModel to initialize keyRegistry")
	}
	if len(m.keyRegistry.AllBindings()) == 0 {
		t.Fatal("expected NewModel to populate keyRegistry from documented bindings")
	}

	tests := []struct {
		focus focus
		key   string
	}{
		{focus: focusList, key: "j"},
		{focus: focusDetail, key: "enter"},
		{focus: focusBoard, key: "h"},
		{focus: focusGraph, key: "PgDn"},
		{focus: focusHistory, key: "v"},
		// Keys that were handled but undocumented before bv-3n9s.8.
		{focus: focusList, key: "E"},
		{focus: focusList, key: "f"},
		{focus: focusList, key: "!"},
		{focus: focusList, key: "w"},
		{focus: focusList, key: "s"},
		{focus: focusList, key: "S"},
		{focus: focusBoard, key: "H"},
		{focus: focusBoard, key: "L"},
		{focus: focusBoard, key: "s"},
		{focus: focusTree, key: "E"},
	}

	for _, tc := range tests {
		if !m.keyRegistry.HasBinding(tc.focus, tc.key) {
			t.Errorf("expected documented binding focus=%v key=%q to be registered", tc.focus, tc.key)
		}
	}
}

// =============================================================================
// Key Dispatch Integration Tests
// =============================================================================
//
// These tests verify that key events are correctly routed to view handlers
// based on the current focus context. They use the actual Update() dispatch
// mechanism rather than the KeyRegistry directly.

// keyMsg creates a tea.KeyMsg for a given key string.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// setupTestModel creates a ready model with test data for key dispatch tests.
func setupTestModel(t *testing.T) *Model {
	t.Helper()
	issues := testIssuesForKeyDispatch()
	return NewModel(issues, nil, "")
}

// testIssuesForKeyDispatch creates a minimal issue set for testing.
func testIssuesForKeyDispatch() []model.Issue {
	return []model.Issue{
		{ID: "kd-1", Title: "Test Issue 1", Status: model.StatusOpen},
		{ID: "kd-2", Title: "Test Issue 2", Status: model.StatusOpen},
		{ID: "kd-3", Title: "Test Issue 3", Status: model.StatusClosed},
	}
}

// TestKeyDispatch_BoardNavigation tests board view key handling.
func TestKeyDispatch_BoardNavigation(t *testing.T) {
	m := setupTestModel(t)

	// Switch to board view
	updated, _ := m.Update(keyMsg("b"))
	m = updated.(*Model)

	if m.focused != focusBoard {
		t.Fatalf("Expected focusBoard after 'b' key, got %v", m.focused)
	}

	tests := []struct {
		key      string
		desc     string
		checkFn  func(*Model) bool
		expected string
	}{
		{"h", "left navigation", func(m *Model) bool { return m.focused == focusBoard }, "stays in board"},
		{"l", "right navigation", func(m *Model) bool { return m.focused == focusBoard }, "stays in board"},
		{"j", "down navigation", func(m *Model) bool { return m.focused == focusBoard }, "stays in board"},
		{"k", "up navigation", func(m *Model) bool { return m.focused == focusBoard }, "stays in board"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			updated, _ := m.Update(keyMsg(tc.key))
			result := updated.(*Model)
			if !tc.checkFn(result) {
				t.Errorf("focus=%v key=%s expected=%s actual=focus:%v", focusBoard, tc.key, tc.expected, result.focused)
			}
			t.Logf("focus=%v key=%s expected=%s actual=focus:%v", focusBoard, tc.key, tc.expected, result.focused)
		})
	}
}

// TestKeyDispatch_DocumentedKeysChangeState drives the keys documented by
// bv-3n9s.8 through Update and asserts the state each one is documented to
// change, so the sidebar never advertises a key the view ignores.
func TestKeyDispatch_DocumentedKeysChangeState(t *testing.T) {
	t.Run("s cycles the list sort mode", func(t *testing.T) {
		m := setupTestModel(t)
		before := m.sortMode
		updated, _ := m.Update(keyMsg("s"))
		m = updated.(*Model)
		if m.sortMode == before {
			t.Fatalf("sort mode did not change from %v", before)
		}
	})

	t.Run("S applies the triage recipe", func(t *testing.T) {
		m := setupTestModel(t)
		if m.activeRecipe != nil && m.activeRecipe.Name == "triage" {
			t.Fatal("triage recipe must not be active before S")
		}
		updated, _ := m.Update(keyMsg("S"))
		m = updated.(*Model)
		if m.activeRecipe == nil || m.activeRecipe.Name != "triage" {
			t.Fatalf("expected the built-in triage recipe to be active, got %+v", m.activeRecipe)
		}
	})

	t.Run("w without workspace mode explains itself", func(t *testing.T) {
		m := setupTestModel(t)
		updated, _ := m.Update(keyMsg("w"))
		m = updated.(*Model)
		if !strings.Contains(m.statusMsg, "workspace mode") {
			t.Fatalf("expected a workspace-mode status message, got %q", m.statusMsg)
		}
	})

	t.Run("H and L jump to the first and last board column", func(t *testing.T) {
		m := setupTestModel(t)
		updated, _ := m.Update(keyMsg("b"))
		m = updated.(*Model)
		if m.focused != focusBoard {
			t.Fatalf("expected board focus, got %v", m.focused)
		}
		updated, _ = m.Update(keyMsg("L"))
		m = updated.(*Model)
		last := len(m.board.activeColIdx) - 1
		if last < 1 {
			t.Skipf("fixture yields %d populated columns; need two to observe a jump", last+1)
		}
		if m.board.focusedCol != last {
			t.Fatalf("L: focusedCol=%d, want %d", m.board.focusedCol, last)
		}
		updated, _ = m.Update(keyMsg("H"))
		m = updated.(*Model)
		if m.board.focusedCol != 0 {
			t.Fatalf("H: focusedCol=%d, want 0", m.board.focusedCol)
		}
	})

	t.Run("s cycles the board swimlane mode", func(t *testing.T) {
		m := setupTestModel(t)
		updated, _ := m.Update(keyMsg("b"))
		m = updated.(*Model)
		before := m.board.GetSwimLaneModeName()
		updated, _ = m.Update(keyMsg("s"))
		m = updated.(*Model)
		if got := m.board.GetSwimLaneModeName(); got == before {
			t.Fatalf("swimlane mode did not change from %q", before)
		}
	})

	t.Run("E leaves the tree view", func(t *testing.T) {
		m := setupTestModel(t)
		updated, _ := m.Update(keyMsg("E"))
		m = updated.(*Model)
		if m.focused != focusTree {
			t.Fatalf("expected tree focus after E, got %v", m.focused)
		}
		updated, _ = m.Update(keyMsg("E"))
		m = updated.(*Model)
		if m.focused != focusList {
			t.Fatalf("expected list focus after second E, got %v", m.focused)
		}
	})
}

// TestKeyDispatch_GraphNavigation tests graph view key handling.
func TestKeyDispatch_GraphNavigation(t *testing.T) {
	m := setupTestModel(t)

	// Switch to graph view
	updated, _ := m.Update(keyMsg("g"))
	m = updated.(*Model)

	if m.focused != focusGraph {
		t.Fatalf("Expected focusGraph after 'g' key, got %v", m.focused)
	}

	tests := []struct {
		key      string
		desc     string
		checkFn  func(*Model) bool
		expected string
	}{
		{"h", "left navigation", func(m *Model) bool { return m.focused == focusGraph }, "stays in graph"},
		{"l", "right navigation", func(m *Model) bool { return m.focused == focusGraph }, "stays in graph"},
		{"j", "down navigation", func(m *Model) bool { return m.focused == focusGraph }, "stays in graph"},
		{"k", "up navigation", func(m *Model) bool { return m.focused == focusGraph }, "stays in graph"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			updated, _ := m.Update(keyMsg(tc.key))
			result := updated.(*Model)
			if !tc.checkFn(result) {
				t.Errorf("focus=%v key=%s expected=%s actual=focus:%v", focusGraph, tc.key, tc.expected, result.focused)
			}
			t.Logf("focus=%v key=%s expected=%s actual=focus:%v", focusGraph, tc.key, tc.expected, result.focused)
		})
	}
}

// TestKeyDispatch_GInBoardStartsCombo verifies that 'g' in board view
// starts the gg-combo timer (doesn't immediately toggle to graph).
// The actual graph toggle happens asynchronously via comboTickMsg.
func TestKeyDispatch_GInBoardStartsCombo(t *testing.T) {
	m := setupTestModel(t)

	// Switch to board view
	updated, _ := m.Update(keyMsg("b"))
	m = updated.(*Model)

	if m.focused != focusBoard {
		t.Fatalf("Expected focusBoard after 'b', got %v", m.focused)
	}

	// 'g' starts combo timer - should NOT immediately toggle to graph
	updated, _ = m.Update(keyMsg("g"))
	m = updated.(*Model)

	// Should still be in board (combo timer started, not yet expired)
	if m.focused != focusBoard {
		t.Errorf("focus=board key=g expected=still_board (combo pending) actual=focus:%v", m.focused)
	}
	// Pending combo key should be set
	if m.pendingComboKey != "g" {
		t.Errorf("pendingComboKey expected=g actual=%s", m.pendingComboKey)
	}
	t.Logf("focus=board key=g expected=combo_pending actual=focus:%v pendingCombo:%s", m.focused, m.pendingComboKey)
}

// TestKeyDispatch_GInTreeStartsCombo verifies that 'g' in tree view starts gg-combo (bv-6fm0).
// First 'g' sets pending combo, second 'g' within window jumps to top.
// The actual graph toggle happens asynchronously via comboTickMsg.
func TestKeyDispatch_GInTreeStartsCombo(t *testing.T) {
	m := setupTestModel(t)

	// Switch to tree view
	updated, _ := m.Update(keyMsg("E"))
	m = updated.(*Model)

	if m.focused != focusTree {
		t.Fatalf("Expected focusTree after 'E', got %v", m.focused)
	}

	// First 'g' starts combo timer - should NOT immediately toggle to graph
	updated, _ = m.Update(keyMsg("g"))
	m = updated.(*Model)

	// Should still be in tree (combo timer started, not yet expired)
	if m.focused != focusTree {
		t.Errorf("focus=tree key=g expected=still_tree (combo pending) actual=focus:%v", m.focused)
	}
	// Pending combo key should be set
	if m.pendingComboKey != "g" {
		t.Errorf("pendingComboKey expected=g actual=%s", m.pendingComboKey)
	}

	// Second 'g' within combo window triggers gg-combo (jump to top), stays in tree
	updated, _ = m.Update(keyMsg("g"))
	m = updated.(*Model)

	if m.focused != focusTree {
		t.Errorf("focus=tree key=gg expected=tree (gg-combo) actual=focus:%v", m.focused)
	}
	if m.pendingComboKey != "" {
		t.Errorf("expected pendingComboKey cleared after gg-combo, got %q", m.pendingComboKey)
	}
}

// TestKeyDispatch_ComboCancelledByOtherKey verifies that pressing another key
// cancels a pending gg-combo (bv-6fm0 bug fix).
func TestKeyDispatch_ComboCancelledByOtherKey(t *testing.T) {
	m := setupTestModel(t)

	// Switch to board view
	updated, _ := m.Update(keyMsg("b"))
	m = updated.(*Model)

	if m.focused != focusBoard {
		t.Fatalf("Expected focusBoard after 'b', got %v", m.focused)
	}

	// First 'g' starts combo timer
	updated, _ = m.Update(keyMsg("g"))
	m = updated.(*Model)

	if m.pendingComboKey != "g" {
		t.Fatalf("Expected pendingComboKey='g' after first g, got %q", m.pendingComboKey)
	}

	// Press 'j' (navigation) - should CANCEL the pending combo
	updated, _ = m.Update(keyMsg("j"))
	m = updated.(*Model)

	// pendingComboKey should be cleared (combo cancelled)
	if m.pendingComboKey != "" {
		t.Errorf("Expected pendingComboKey cleared after 'j', got %q", m.pendingComboKey)
	}
	// Should still be in board view
	if m.focused != focusBoard {
		t.Errorf("Expected to stay in board after 'j', got focus:%v", m.focused)
	}
}

// TestKeyDispatch_Regression_QInHistoryClosesHistory verifies that 'q' in history view
// closes history and returns to list without opening quit confirmation.
func TestKeyDispatch_Regression_QInHistoryClosesHistory(t *testing.T) {
	m := setupTestModel(t)

	// Toggle history view on
	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)

	if !m.isHistoryView || m.focused != focusHistory {
		t.Fatalf("Expected history view after 'h', got isHistoryView=%v focused=%v", m.isHistoryView, m.focused)
	}

	// Press 'q' - must close history cleanly back to focusList without quit modal
	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(*Model)

	if m.isHistoryView {
		t.Fatalf("expected 'q' in history view to clear isHistoryView, got true")
	}
	if m.focused != focusList {
		t.Fatalf("expected 'q' in history view to return to focusList, got %v", m.focused)
	}
	if m.showQuitConfirm {
		t.Fatalf("expected 'q' in history view not to trigger showQuitConfirm")
	}
	if cmd != nil {
		t.Fatalf("expected 'q' in history view not to emit a quit command")
	}
}

// TestKeyDispatch_Regression_EscInTreeReturnsList verifies that ESC in tree view
// returns cleanly to focusList while preserving any active list filter.
func TestKeyDispatch_Regression_EscInTreeReturnsList(t *testing.T) {
	m := setupTestModel(t)

	// Activate a status filter in list view before entering tree view
	updated, _ := m.Update(keyMsg("o"))
	m = updated.(*Model)
	if m.currentFilter != "open" {
		t.Fatalf("expected currentFilter=%q after 'o', got %q", "open", m.currentFilter)
	}

	// Toggle tree view on (E, not f - f is FlowMatrix)
	updated, _ = m.Update(keyMsg("E"))
	m = updated.(*Model)

	if m.focused != focusTree {
		t.Fatalf("Expected focusTree after 'E', got %v", m.focused)
	}

	// Press ESC - must exit focusTree back to focusList and preserve currentFilter
	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)

	if m.focused != focusList {
		t.Fatalf("expected ESC in tree view to return to focusList, got %v", m.focused)
	}
	if m.currentFilter != "open" {
		t.Fatalf("expected ESC in tree view to preserve list filter %q, got %q", "open", m.currentFilter)
	}
	if m.showQuitConfirm {
		t.Fatalf("expected ESC in tree view not to open quit confirm")
	}
}

// TestKeyDispatch_Regression_FInHistoryTogglesFileTree verifies that 'f' in history view
// toggles the file tree within history rather than switching to Flow Matrix.
func TestKeyDispatch_Regression_FInHistoryTogglesFileTree(t *testing.T) {
	m := setupTestModel(t)

	// Toggle history view on and mark a history report with files as current
	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("Expected focusHistory after 'h', got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	report := createTestHistoryReportWithFiles()
	m.historyView = NewHistoryModel(report, testTheme())
	makeHistoryReportCurrent(m, report)

	// First 'f' opens the file tree panel inside history view; Tab cycles focus into it
	updated, _ = m.Update(keyMsg("f"))
	m = updated.(*Model)

	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("expected 'f' in history view to stay in focusHistory, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	if !m.historyView.IsFileTreeVisible() {
		t.Fatalf("expected 'f' in history view to make file tree visible")
	}

	// Second 'f' hides the file tree while staying in history view
	updated, _ = m.Update(keyMsg("f"))
	m = updated.(*Model)

	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("expected second 'f' in history view to stay in focusHistory, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	if m.historyView.IsFileTreeVisible() {
		t.Fatalf("expected second 'f' in history view to hide file tree")
	}
}

func TestKeyDispatch_Regression_BoardSearchConsumesInput(t *testing.T) {
	m := setupTestModel(t)

	updated, _ := m.Update(keyMsg("b"))
	m = updated.(*Model)
	if m.focused != focusBoard || !m.isBoardView {
		t.Fatalf("Expected board view after 'b', got focused=%v isBoardView=%v", m.focused, m.isBoardView)
	}

	updated, _ = m.Update(keyMsg("/"))
	m = updated.(*Model)
	if !m.board.IsSearchMode() {
		t.Fatal("expected board search mode after '/'")
	}

	updated, _ = m.Update(keyMsg("b"))
	m = updated.(*Model)
	if got := m.board.SearchQuery(); got != "b" {
		t.Fatalf("expected board search query %q, got %q", "b", got)
	}
	if m.focused != focusBoard || !m.isBoardView {
		t.Fatalf("expected board search input to stay in board view, got focused=%v isBoardView=%v", m.focused, m.isBoardView)
	}

	updated, _ = m.Update(keyMsg("backspace"))
	m = updated.(*Model)
	if got := m.board.SearchQuery(); got != "" {
		t.Fatalf("expected board search query to clear after backspace, got %q", got)
	}

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)
	if m.board.IsSearchMode() {
		t.Fatal("expected esc to cancel board search mode")
	}
	if m.focused != focusBoard || !m.isBoardView {
		t.Fatalf("expected esc to remain in board view, got focused=%v isBoardView=%v", m.focused, m.isBoardView)
	}
}

func TestKeyDispatch_Regression_HistorySearchConsumesGlobalKeys(t *testing.T) {
	m := setupTestModel(t)

	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("Expected history view after 'h', got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	makeHistoryReportCurrent(m, createTestHistoryReport())

	updated, _ = m.Update(keyMsg("/"))
	m = updated.(*Model)
	if !m.historyView.IsSearchActive() {
		t.Fatal("expected history search mode after '/'")
	}

	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)
	if got := m.historyView.SearchQuery(); got != "q" {
		t.Fatalf("expected history search query %q, got %q", "q", got)
	}
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("expected history search input to stay in history view, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)
	if m.historyView.IsSearchActive() {
		t.Fatal("expected esc to cancel history search mode")
	}
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("expected esc to remain in history view, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
}

// TestKeyDispatch_Regression_LabelPickerConsumesQKey guards against issue #176:
// the "Filter by Label" picker has an always-focused text input, so a lowercase
// q must be typed into the filter rather than triggering the global quit/back
// shortcut and closing the picker. Esc remains the way to cancel.
func TestKeyDispatch_Regression_LabelPickerConsumesQKey(t *testing.T) {
	m := setupTestModel(t)

	updated, _ := m.Update(keyMsg("l"))
	m = updated.(*Model)
	if m.focused != focusLabelPicker || !m.showLabelPicker {
		t.Fatalf("expected label picker after 'l', got focused=%v showLabelPicker=%v", m.focused, m.showLabelPicker)
	}

	// Typing a lowercase q must append to the filter, not quit/close the picker.
	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)
	if !m.showLabelPicker || m.focused != focusLabelPicker {
		t.Fatalf("expected label picker to stay open after typing 'q', got focused=%v showLabelPicker=%v", m.focused, m.showLabelPicker)
	}
	if got := m.labelPicker.InputValue(); got != "q" {
		t.Fatalf("expected label filter input %q after typing 'q', got %q", "q", got)
	}

	// Subsequent printable chars keep appending (e.g. building "required").
	for _, k := range []string{"u", "e"} {
		updated, _ = m.Update(keyMsg(k))
		m = updated.(*Model)
	}
	if got := m.labelPicker.InputValue(); got != "que" {
		t.Fatalf("expected label filter input %q, got %q", "que", got)
	}

	// Esc still cancels the picker and returns to the list.
	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)
	if m.showLabelPicker || m.focused != focusList {
		t.Fatalf("expected esc to cancel label picker, got focused=%v showLabelPicker=%v", m.focused, m.showLabelPicker)
	}
}

// openLabelPickerWithLabels returns a model whose label picker is open over
// issues carrying the given labels (one issue per label, equal counts, so the
// picker lists them alphabetically).
func openLabelPickerWithLabels(t *testing.T, labels ...string) *Model {
	t.Helper()
	issues := make([]model.Issue, 0, len(labels))
	for i, label := range labels {
		issues = append(issues, model.Issue{
			ID:     "lp-" + string(rune('a'+i)),
			Title:  "Label picker issue " + label,
			Status: model.StatusOpen,
			Labels: []string{label},
		})
	}
	m := NewModel(issues, nil, "")
	updated, _ := m.Update(keyMsg("l"))
	m = updated.(*Model)
	if m.focused != focusLabelPicker || !m.showLabelPicker {
		t.Fatalf("expected label picker after 'l', got focused=%v showLabelPicker=%v", m.focused, m.showLabelPicker)
	}
	return m
}

// TestKeyDispatch_Regression_LabelPickerTypesJAndK guards issue #213: the
// picker's filter input is always focused, so lowercase j and k must be typed
// into the query (labels such as "backend" contain them) instead of moving
// the selection. The arrow keys and ctrl+n/ctrl+p still navigate.
func TestKeyDispatch_Regression_LabelPickerTypesJAndK(t *testing.T) {
	m := openLabelPickerWithLabels(t, "api", "backend", "jobs")

	for _, r := range "backend" {
		updated, _ := m.Update(keyMsg(string(r)))
		m = updated.(*Model)
	}
	if got := m.labelPicker.InputValue(); got != "backend" {
		t.Fatalf("label filter input = %q, want %q", got, "backend")
	}
	if got := m.labelPicker.SelectedLabel(); got != "backend" {
		t.Fatalf("selected label = %q, want %q", got, "backend")
	}

	// Clear the query, then check that j is typed as well.
	for range "backend" {
		updated, _ := m.Update(keyMsg("backspace"))
		m = updated.(*Model)
	}
	updated, _ := m.Update(keyMsg("j"))
	m = updated.(*Model)
	if got := m.labelPicker.InputValue(); got != "j" {
		t.Fatalf("label filter input = %q after typing j, want %q", got, "j")
	}
	if got := m.labelPicker.SelectedLabel(); got != "jobs" {
		t.Fatalf("selected label = %q after typing j, want %q", got, "jobs")
	}

	// Navigation keys still move the selection without editing the query.
	updated, _ = m.Update(keyMsg("backspace"))
	m = updated.(*Model)
	for _, key := range []tea.KeyMsg{keyMsg("down"), {Type: tea.KeyCtrlN}} {
		updated, _ = m.Update(key)
		m = updated.(*Model)
	}
	if got := m.labelPicker.SelectedLabel(); got != "jobs" {
		t.Fatalf("selected label = %q after down, ctrl+n; want %q", got, "jobs")
	}
	for _, key := range []tea.KeyMsg{keyMsg("up"), {Type: tea.KeyCtrlP}} {
		updated, _ = m.Update(key)
		m = updated.(*Model)
	}
	if got := m.labelPicker.SelectedLabel(); got != "api" {
		t.Fatalf("selected label = %q after up, ctrl+p; want %q", got, "api")
	}
	if got := m.labelPicker.InputValue(); got != "" {
		t.Fatalf("navigation edited the query: %q", got)
	}
}

// TestLabelPickerNonKeyUpdatesKeepSelection guards issue #213: cursor-blink
// messages reach the picker's text input while it is focused. With an empty
// query they used to re-filter and snap the selection back to the first label.
func TestLabelPickerNonKeyUpdatesKeepSelection(t *testing.T) {
	m := openLabelPickerWithLabels(t, "api", "backend", "jobs")

	updated, _ := m.Update(keyMsg("down"))
	m = updated.(*Model)
	updated, _ = m.Update(keyMsg("down"))
	m = updated.(*Model)
	if got := m.labelPicker.SelectedLabel(); got != "jobs" {
		t.Fatalf("selected label = %q after two downs, want %q", got, "jobs")
	}

	for i := 0; i < 3; i++ {
		updated, _ = m.Update(embeddedTextInputMsg{session: m.embeddedTextInputSession, msg: textinput.Blink()})
		m = updated.(*Model)
	}
	if got := m.labelPicker.SelectedLabel(); got != "jobs" {
		t.Fatalf("cursor blink moved the selection to %q, want %q", got, "jobs")
	}
}

// TestKeyDispatch_InsightsResizesDetailPanel covers #214: in Insights, `<`
// and `>` resize the detail panel instead of the hidden list/detail split.
func TestKeyDispatch_InsightsResizesDetailPanel(t *testing.T) {
	m := setupTestModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = updated.(*Model)
	updated, _ = m.Update(keyMsg("i"))
	m = updated.(*Model)
	if m.focused != focusInsights {
		t.Fatalf("expected insights focus after 'i', got %v", m.focused)
	}
	split := m.splitPaneRatio
	initial := m.insightsPanel.detailPanelWidth()

	updated, _ = m.Update(keyMsg("<"))
	m = updated.(*Model)
	if got := m.insightsPanel.detailPanelWidth(); got <= initial {
		t.Fatalf("'<' left the detail panel at %d columns (was %d)", got, initial)
	}
	updated, _ = m.Update(keyMsg(">"))
	m = updated.(*Model)
	updated, _ = m.Update(keyMsg(">"))
	m = updated.(*Model)
	if got := m.insightsPanel.detailPanelWidth(); got >= initial {
		t.Fatalf("'>' left the detail panel at %d columns (was %d)", got, initial)
	}
	if m.splitPaneRatio != split {
		t.Fatalf("Insights resize changed the list split ratio: %v -> %v", split, m.splitPaneRatio)
	}

	// A rebuilt panel (snapshot refresh) keeps the chosen width.
	chosen := m.insightsPanel.detailPanelWidth()
	m.rebuildInsightsPanel()
	if got := m.insightsPanel.detailPanelWidth(); got != chosen {
		t.Fatalf("rebuild reset the detail panel to %d columns, want %d", got, chosen)
	}
}

func TestKeyDispatch_Regression_HistorySearchEnterKeepsFilter(t *testing.T) {
	m := setupTestModel(t)

	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("Expected history view after 'h', got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	makeHistoryReportCurrent(m, createTestHistoryReport())

	updated, _ = m.Update(keyMsg("/"))
	m = updated.(*Model)
	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)

	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(*Model)

	if m.historyView.IsSearchActive() {
		t.Fatal("expected enter to exit active history search input")
	}
	if got := m.historyView.SearchQuery(); got != "q" {
		t.Fatalf("expected enter to preserve history search query %q, got %q", "q", got)
	}
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("expected enter to remain in history view, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
}

func TestKeyDispatch_Regression_HistoryFileTreeEscStaysInHistory(t *testing.T) {
	m := setupTestModel(t)
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyView = NewHistoryModel(createTestHistoryReportWithFiles(), testTheme())
	makeHistoryReportCurrent(m, createTestHistoryReportWithFiles())
	m.historyView.ToggleFileTree()
	m.historyView.SetFileTreeFocus(true)

	updated, _ := m.Update(keyMsg("esc"))
	m = updated.(*Model)

	if !m.isHistoryView || m.focused != focusHistory {
		t.Fatalf("expected esc in history file tree to stay in history view, got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}
	if m.historyView.FileTreeHasFocus() {
		t.Fatal("expected esc in history file tree to leave file-tree focus")
	}
}

// TestKeyDispatch_ModalConsumesAllKeys verifies that modals consume view-toggle keys
// and never pass them through to underlying view switches.
func TestKeyDispatch_ModalConsumesAllKeys(t *testing.T) {
	viewToggleKeys := []string{"b", "g", "h", "f", "i", "a", "E", "[", "]", "P"}

	t.Run("help_overlay_consumes_without_view_passthrough", func(t *testing.T) {
		for _, key := range viewToggleKeys {
			m := setupTestModel(t)
			updated, _ := m.Update(keyMsg("?"))
			m = updated.(*Model)
			if !m.showHelp || m.focused != focusHelp {
				t.Fatalf("expected help modal after '?', got showHelp=%v focused=%v", m.showHelp, m.focused)
			}

			updated, _ = m.Update(keyMsg(key))
			m = updated.(*Model)

			if key == "g" {
				// 'g' scrolls help to top and keeps help open.
				if !m.showHelp || m.focused != focusHelp {
					t.Fatalf("expected 'g' in help modal to stay in focusHelp, got showHelp=%v focused=%v", m.showHelp, m.focused)
				}
			} else {
				// Non-navigation keys dismiss help back to focusBeforeHelp (focusList)
				// and MUST NOT pass through to open any underlying view.
				if m.showHelp {
					t.Fatalf("expected %q in help modal to dismiss help, got showHelp=true", key)
				}
				if m.focused != focusList {
					t.Fatalf("key %q leaked through help modal: focused=%v, want focusList", key, m.focused)
				}
				if m.isBoardView || m.isGraphView || m.isHistoryView || m.isActionableView || m.isSprintView {
					t.Fatalf("key %q leaked view flags through help modal: board=%v graph=%v history=%v actionable=%v sprint=%v",
						key, m.isBoardView, m.isGraphView, m.isHistoryView, m.isActionableView, m.isSprintView)
				}
			}
		}
	})

	t.Run("tutorial_modal_stays_open_on_view_keys", func(t *testing.T) {
		m := setupTestModel(t)
		updated, _ := m.Update(keyMsg("`"))
		m = updated.(*Model)
		if !m.showTutorial || m.focused != focusTutorial {
			t.Fatalf("expected tutorial modal after '`', got showTutorial=%v focused=%v", m.showTutorial, m.focused)
		}

		for _, key := range []string{"b", "g", "h", "f", "i", "E", "[", "]", "P"} {
			updated, _ = m.Update(keyMsg(key))
			m = updated.(*Model)
			if !m.showTutorial || m.focused != focusTutorial {
				t.Fatalf("key %q escaped tutorial modal: showTutorial=%v focused=%v", key, m.showTutorial, m.focused)
			}
		}
	})

	t.Run("quit_confirm_consumes_without_view_passthrough", func(t *testing.T) {
		for _, key := range viewToggleKeys {
			m := setupTestModel(t)
			updated, _ := m.Update(keyMsg("esc"))
			m = updated.(*Model)
			if !m.showQuitConfirm || m.focused != focusQuitConfirm {
				t.Fatalf("expected quit confirm after ESC in unfiltered list, got showQuitConfirm=%v focused=%v", m.showQuitConfirm, m.focused)
			}

			updated, _ = m.Update(keyMsg(key))
			m = updated.(*Model)
			if m.showQuitConfirm || m.focused != focusList {
				t.Fatalf("key %q in quit confirm: expected cancel back to focusList, got showQuitConfirm=%v focused=%v",
					key, m.showQuitConfirm, m.focused)
			}
			if m.isBoardView || m.isGraphView || m.isHistoryView || m.isActionableView || m.isSprintView {
				t.Fatalf("key %q leaked through quit confirm into view toggle", key)
			}
		}
	})

	t.Run("label_health_detail_modal_blocks_cross_view_leakage", func(t *testing.T) {
		now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		issues := []model.Issue{
			{ID: "LH-1", Title: "Labeled issue", Status: model.StatusOpen, Priority: 1, Labels: []string{"backend"}, CreatedAt: now, UpdatedAt: now},
		}
		m := NewModel(issues, nil, "")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		m = updated.(*Model)

		updated, _ = m.Update(keyMsg("["))
		m = updated.(*Model)
		updated, _ = m.Update(keyMsg("h"))
		m = updated.(*Model)
		if !m.showLabelHealthDetail || m.focused != focusLabelDashboard {
			t.Fatalf("expected label health detail modal after '[' then 'h', got show=%v focused=%v", m.showLabelHealthDetail, m.focused)
		}

		for _, key := range []string{"b", "g", "f", "i", "a", "E", "[", "]", "P"} {
			updated, _ = m.Update(keyMsg(key))
			m = updated.(*Model)
			if !m.showLabelHealthDetail || m.focused != focusLabelDashboard {
				t.Fatalf("key %q leaked out of label health detail modal: show=%v focused=%v",
					key, m.showLabelHealthDetail, m.focused)
			}
		}
	})
}

// TestKeyDispatch_ViewToggleTable tests critical view toggle combinations.
// Tests h/g/f in various focus contexts to verify correct routing.
func TestKeyDispatch_ViewToggleTable(t *testing.T) {
	type testCase struct {
		startFocus focus
		key        string
		expectFn   func(*Model) (bool, string)
	}

	cases := []testCase{
		// From list view
		{focusList, "b", func(m *Model) (bool, string) { return m.focused == focusBoard, "should toggle to board" }},
		{focusList, "g", func(m *Model) (bool, string) { return m.focused == focusGraph, "should toggle to graph" }},
		{focusList, "h", func(m *Model) (bool, string) { return m.isHistoryView, "should toggle to history" }},
		{focusList, "f", func(m *Model) (bool, string) { return m.focused == focusFlowMatrix, "should toggle to flow matrix" }},
		{focusList, "E", func(m *Model) (bool, string) { return m.focused == focusTree, "should toggle to tree" }},
		{focusList, "i", func(m *Model) (bool, string) { return m.focused == focusInsights, "should toggle to insights" }},
		{focusList, "a", func(m *Model) (bool, string) { return m.focused == focusActionable, "should toggle to actionable" }},

		// Note: 'g' in board uses gg-combo mechanism (async timeout before graph toggle),
		// so it can't be tested with simple synchronous Update() calls.
	}

	focusNames := map[focus]string{
		focusList:       "list",
		focusBoard:      "board",
		focusGraph:      "graph",
		focusTree:       "tree",
		focusHistory:    "history",
		focusInsights:   "insights",
		focusActionable: "actionable",
		focusFlowMatrix: "flowMatrix",
	}

	for _, tc := range cases {
		name := focusNames[tc.startFocus] + "_" + tc.key
		t.Run(name, func(t *testing.T) {
			m := setupTestModel(t)

			// Navigate to start focus if not list
			switch tc.startFocus {
			case focusBoard:
				updated, _ := m.Update(keyMsg("b"))
				m = updated.(*Model)
			case focusGraph:
				updated, _ := m.Update(keyMsg("g"))
				m = updated.(*Model)
			case focusTree:
				updated, _ := m.Update(keyMsg("E"))
				m = updated.(*Model)
			case focusHistory:
				updated, _ := m.Update(keyMsg("h"))
				m = updated.(*Model)
			case focusInsights:
				updated, _ := m.Update(keyMsg("i"))
				m = updated.(*Model)
			}

			if m.focused != tc.startFocus && tc.startFocus != focusHistory {
				t.Fatalf("Failed to set up start focus: expected %v, got %v", tc.startFocus, m.focused)
			}

			// Send the test key
			updated, _ := m.Update(keyMsg(tc.key))
			result := updated.(*Model)

			ok, expected := tc.expectFn(result)
			if !ok {
				t.Errorf("focus=%v key=%s expected=%s actual=focus:%v", tc.startFocus, tc.key, expected, result.focused)
			}
			t.Logf("focus=%v key=%s expected=%s actual=focus:%v", tc.startFocus, tc.key, expected, result.focused)
		})
	}
}

// setupFSMModel constructs a fully populated Model suitable for exercising every
// primary focus state (including Sprint, Insights heatmap drilldown, History with
// file tree, Label Dashboard, Velocity Comparison, Flow Matrix, Attention, and Tree).
func setupFSMModel(t *testing.T, beadsPath string) *Model {
	t.Helper()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-24 * time.Hour)
	issues := []model.Issue{
		{
			ID:        "FSM-1",
			Title:     "Root epic backend",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeEpic,
			Labels:    []string{"backend"},
			CreatedAt: now.Add(-7 * 24 * time.Hour),
			UpdatedAt: now,
		},
		{
			ID:        "FSM-2",
			Title:     "Child frontend task",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
			Labels:    []string{"frontend"},
			CreatedAt: now.Add(-5 * 24 * time.Hour),
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "FSM-2", DependsOnID: "FSM-1", Type: model.DepParentChild},
				{IssueID: "FSM-2", DependsOnID: "FSM-1", Type: model.DepBlocks},
			},
		},
		{
			ID:        "FSM-3",
			Title:     "Completed backend bug",
			Status:    model.StatusClosed,
			Priority:  1,
			IssueType: model.TypeBug,
			Labels:    []string{"backend"},
			CreatedAt: now.Add(-6 * 24 * time.Hour),
			UpdatedAt: closedAt,
			ClosedAt:  &closedAt,
		},
	}

	m := NewModel(issues, nil, beadsPath)
	m.sprints = []model.Sprint{
		{
			ID:        "sprint-1",
			Name:      "Sprint 1",
			StartDate: now.AddDate(0, 0, -2),
			EndDate:   now.AddDate(0, 0, 5),
			BeadIDs:   []string{"FSM-1", "FSM-2"},
		},
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	m = updated.(*Model)
	return m
}

// enterInsightsHeatmapDrilldown transitions m into focusInsights with the priority
// heatmap active and drilled down into a populated cell via real Model.Update keys.
func enterInsightsHeatmapDrilldown(t *testing.T, m *Model) *Model {
	t.Helper()
	updated, _ := m.Update(keyMsg("i"))
	m = updated.(*Model)
	if m.focused != focusInsights {
		t.Fatalf("expected focusInsights after 'i', got %v", m.focused)
	}

	// Populate Phase-2 insights + top picks AFTER pressing 'i' (which calls rebuildInsightsPanel).
	stats := analysis.NewAnalyzer(m.issues).AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	m.insightsPanel = NewInsightsModel(stats.GenerateInsights(len(m.issues)), m.issueMap, m.theme)
	m.insightsPanel.SetTopPicks([]analysis.TopPick{
		{ID: "FSM-1", Title: "Root epic backend", Score: 0.90},
		{ID: "FSM-2", Title: "Child frontend task", Score: 0.50},
	})
	m.insightsPanel.SetSize(150, 42)
	m.insightsPanel.focusedPanel = PanelPriority

	updated, _ = m.Update(keyMsg("H"))
	m = updated.(*Model)
	if !m.insightsPanel.IsHeatmapActive() {
		t.Fatalf("expected heatmap active after 'H'")
	}

	targetRow, targetCol := -1, -1
	for r := range m.insightsPanel.heatmapIssueMap {
		for c := range m.insightsPanel.heatmapIssueMap[r] {
			if slices.Contains(m.insightsPanel.heatmapIssueMap[r][c], "FSM-1") {
				targetRow, targetCol = r, c
			}
		}
	}
	if targetRow < 0 || targetCol < 0 {
		t.Fatalf("FSM-1 not found in heatmapIssueMap")
	}
	for m.insightsPanel.heatmapRow < targetRow {
		updated, _ = m.Update(keyMsg("j"))
		m = updated.(*Model)
	}
	for m.insightsPanel.heatmapCol < targetCol {
		updated, _ = m.Update(keyMsg("l"))
		m = updated.(*Model)
	}
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(*Model)
	if m.focused != focusInsights || !m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected focusInsights heatmap drilldown, got focused=%v drill=%v",
			m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}
	return m
}

// TestFocusKeyStateMachine exhaustively exercises Model.Update across all 13 primary
// focus states against Esc, q, ?, ~, ;, `, cross-view toggles (b, g, i, E, a, h, P,
// [, ], f), preserved list filters on sub-view exit, isTextInputActive() guards, and
// TreeModel.saveState persistence gates.
func TestFocusKeyStateMachine(t *testing.T) {
	type fsmStateSpec struct {
		name      string
		wantFocus focus
		enter     func(t *testing.T, m *Model) *Model
	}

	states := []fsmStateSpec{
		{
			name:      "list",
			wantFocus: focusList,
			enter:     func(_ *testing.T, m *Model) *Model { return m },
		},
		{
			name:      "board",
			wantFocus: focusBoard,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("b"))
				return u.(*Model)
			},
		},
		{
			name:      "graph",
			wantFocus: focusGraph,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("g"))
				return u.(*Model)
			},
		},
		{
			name:      "insights",
			wantFocus: focusInsights,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("i"))
				return u.(*Model)
			},
		},
		{
			name:      "insights_heatmap_drilldown",
			wantFocus: focusInsights,
			enter:     enterInsightsHeatmapDrilldown,
		},
		{
			name:      "tree",
			wantFocus: focusTree,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("E"))
				return u.(*Model)
			},
		},
		{
			name:      "actionable",
			wantFocus: focusActionable,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("a"))
				return u.(*Model)
			},
		},
		{
			name:      "history",
			wantFocus: focusHistory,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("h"))
				m = u.(*Model)
				report := createTestHistoryReportWithFiles()
				m.historyView = NewHistoryModel(report, testTheme())
				makeHistoryReportCurrent(m, report)
				return m
			},
		},
		{
			name:      "sprint",
			wantFocus: focusSprint,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("P"))
				return u.(*Model)
			},
		},
		{
			name:      "flow_matrix",
			wantFocus: focusFlowMatrix,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("f"))
				return u.(*Model)
			},
		},
		{
			name:      "attention",
			wantFocus: focusAttention,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("]"))
				return u.(*Model)
			},
		},
		{
			name:      "label_dashboard",
			wantFocus: focusLabelDashboard,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("["))
				return u.(*Model)
			},
		},
		{
			name:      "velocity_comparison",
			wantFocus: focusVelocityComparison,
			enter: func(t *testing.T, m *Model) *Model {
				u, _ := m.Update(keyMsg("["))
				m = u.(*Model)
				u, _ = m.Update(keyMsg("v"))
				return u.(*Model)
			},
		},
	}

	keys := []string{"esc", "q", "?", "~", ";", "`", "b", "g", "i", "E", "a", "h", "P", "[", "]", "f"}

	t.Run("FocusByKeyMatrix", func(t *testing.T) {
		for _, st := range states {
			st := st
			for _, key := range keys {
				key := key
				t.Run(st.name+"_"+key, func(t *testing.T) {
					m := setupFSMModel(t, "")
					m = st.enter(t, m)
					if m.focused != st.wantFocus {
						t.Fatalf("enter %s: focused=%v, want %v", st.name, m.focused, st.wantFocus)
					}

					priorFocus := m.focused
					priorCtx := m.CurrentContext()

					updated, cmd := m.Update(keyMsg(key))
					m = updated.(*Model)

					switch key {
					case "esc":
						switch st.name {
						case "list":
							if m.focused != focusQuitConfirm || !m.showQuitConfirm {
								t.Fatalf("list+esc: focused=%v showQuitConfirm=%v, want focusQuitConfirm/true", m.focused, m.showQuitConfirm)
							}
						case "insights_heatmap_drilldown":
							if m.focused != focusInsights || !m.insightsPanel.IsHeatmapActive() || m.insightsPanel.IsHeatmapDrillDown() {
								t.Fatalf("insights_heatmap_drilldown+esc: expected exit drilldown staying in focusInsights, got focused=%v heatmap=%v drill=%v",
									m.focused, m.insightsPanel.IsHeatmapActive(), m.insightsPanel.IsHeatmapDrillDown())
							}
							// Second Esc exits focusInsights back to focusList.
							u2, _ := m.Update(keyMsg("esc"))
							m2 := u2.(*Model)
							if m2.focused != focusList {
								t.Fatalf("second esc after heatmap drilldown: focused=%v, want focusList", m2.focused)
							}
						case "velocity_comparison":
							if m.focused != focusLabelDashboard || m.showVelocityComparison {
								t.Fatalf("velocity_comparison+esc: focused=%v showVelocity=%v, want focusLabelDashboard/false",
									m.focused, m.showVelocityComparison)
							}
						default:
							if m.focused != focusList || m.showQuitConfirm {
								t.Fatalf("%s+esc: focused=%v showQuitConfirm=%v, want focusList/false", st.name, m.focused, m.showQuitConfirm)
							}
						}

					case "q":
						switch st.name {
						case "list":
							if m.focused != focusList || cmd == nil {
								t.Fatalf("list+q: focused=%v cmd=%v, want focusList with non-nil quit cmd", m.focused, cmd)
							}
						case "insights_heatmap_drilldown":
							if m.focused != focusInsights || !m.insightsPanel.IsHeatmapActive() || m.insightsPanel.IsHeatmapDrillDown() {
								t.Fatalf("insights_heatmap_drilldown+q: expected exit drilldown staying in focusInsights, got focused=%v heatmap=%v drill=%v",
									m.focused, m.insightsPanel.IsHeatmapActive(), m.insightsPanel.IsHeatmapDrillDown())
							}
						case "velocity_comparison":
							if m.focused != focusLabelDashboard || m.showVelocityComparison {
								t.Fatalf("velocity_comparison+q: focused=%v showVelocity=%v, want focusLabelDashboard/false",
									m.focused, m.showVelocityComparison)
							}
						default:
							if m.focused != focusList || cmd != nil {
								t.Fatalf("%s+q: focused=%v cmd=%v, want focusList with nil cmd", st.name, m.focused, cmd)
							}
						}

					case "?":
						if !m.showHelp || m.focused != focusHelp {
							t.Fatalf("%s+?: showHelp=%v focused=%v, want true/focusHelp", st.name, m.showHelp, m.focused)
						}
						u2, _ := m.Update(keyMsg("esc"))
						m = u2.(*Model)
						if m.showHelp || m.focused != priorFocus {
							t.Fatalf("%s+? then esc: showHelp=%v focused=%v, want false/%v", st.name, m.showHelp, m.focused, priorFocus)
						}

					case "~":
						if !m.showTutorial || m.focused != focusTutorial || !m.tutorialModel.IsContextMode() {
							t.Fatalf("%s+~: showTutorial=%v focused=%v contextMode=%v, want true/focusTutorial/true",
								st.name, m.showTutorial, m.focused, m.tutorialModel.IsContextMode())
						}
						visible := m.tutorialModel.visiblePages()
						if len(visible) == 0 {
							t.Fatalf("%s+~: visiblePages() is empty for context %q", st.name, priorCtx)
						}
						if !slices.Contains(visible[0].Contexts, string(priorCtx)) {
							t.Fatalf("%s+~: first visible page %q Contexts=%v does not contain active context %q",
								st.name, visible[0].ID, visible[0].Contexts, priorCtx)
						}
						if m.tutorialModel.currentPage != 0 {
							t.Fatalf("%s+~: currentPage=%d, want 0 (first filtered context page)",
								st.name, m.tutorialModel.currentPage)
						}
						u2, _ := m.Update(keyMsg("esc"))
						m = u2.(*Model)
						if m.showTutorial || m.focused != priorFocus {
							t.Fatalf("%s+~ then esc: showTutorial=%v focused=%v, want false/%v",
								st.name, m.showTutorial, m.focused, priorFocus)
						}

					case ";":
						if !m.showShortcutsSidebar || m.focused != priorFocus {
							t.Fatalf("%s+;: showShortcutsSidebar=%v focused=%v, want true/%v",
								st.name, m.showShortcutsSidebar, m.focused, priorFocus)
						}

					case "`":
						if !m.showTutorial || m.focused != focusTutorial || m.tutorialModel.IsContextMode() {
							t.Fatalf("%s+`: showTutorial=%v focused=%v contextMode=%v, want true/focusTutorial/false",
								st.name, m.showTutorial, m.focused, m.tutorialModel.IsContextMode())
						}
						u2, _ := m.Update(keyMsg("esc"))
						m = u2.(*Model)
						if m.showTutorial || m.focused != priorFocus {
							t.Fatalf("%s+` then esc: showTutorial=%v focused=%v, want false/%v",
								st.name, m.showTutorial, m.focused, priorFocus)
						}

					case "b":
						if st.name == "board" {
							if m.focused != focusList || m.isBoardView {
								t.Fatalf("board+b: focused=%v isBoardView=%v, want focusList/false", m.focused, m.isBoardView)
							}
						} else if m.focused != focusBoard || !m.isBoardView || m.isSprintView || m.showVelocityComparison {
							t.Fatalf("%s+b: focused=%v isBoardView=%v sprint=%v vel=%v, want focusBoard/true/false/false",
								st.name, m.focused, m.isBoardView, m.isSprintView, m.showVelocityComparison)
						}

					case "g":
						switch st.name {
						case "graph":
							if m.focused != focusList || m.isGraphView {
								t.Fatalf("graph+g: focused=%v isGraphView=%v, want focusList/false", m.focused, m.isGraphView)
							}
						case "board", "tree":
							if m.focused != priorFocus || m.pendingComboKey != "g" {
								t.Fatalf("%s+g: focused=%v pendingComboKey=%q, want %v/\"g\"",
									st.name, m.focused, m.pendingComboKey, priorFocus)
							}
							// Expiring the combo timer switches to focusGraph.
							u2, _ := m.Update(comboTickMsg{key: "g"})
							m = u2.(*Model)
							if m.focused != focusGraph || !m.isGraphView {
								t.Fatalf("%s+g combo expiry: focused=%v isGraphView=%v, want focusGraph/true",
									st.name, m.focused, m.isGraphView)
							}
						case "flow_matrix", "attention":
							if m.focused != priorFocus || m.pendingComboKey != "" {
								t.Fatalf("%s+g: focused=%v pendingComboKey=%q, want in-view GoToTop staying in %v",
									st.name, m.focused, m.pendingComboKey, priorFocus)
							}
						default:
							if m.focused != focusGraph || !m.isGraphView {
								t.Fatalf("%s+g: focused=%v isGraphView=%v, want focusGraph/true",
									st.name, m.focused, m.isGraphView)
							}
						}

					case "i":
						if st.name == "insights" || st.name == "insights_heatmap_drilldown" {
							if m.focused != focusList {
								t.Fatalf("%s+i: focused=%v, want focusList", st.name, m.focused)
							}
						} else if m.focused != focusInsights {
							t.Fatalf("%s+i: focused=%v, want focusInsights", st.name, m.focused)
						}

					case "E":
						if st.name == "tree" {
							if m.focused != focusList {
								t.Fatalf("tree+E: focused=%v, want focusList", m.focused)
							}
						} else if m.focused != focusTree {
							t.Fatalf("%s+E: focused=%v, want focusTree", st.name, m.focused)
						}

					case "a":
						if st.name == "actionable" {
							if m.focused != focusList || m.isActionableView {
								t.Fatalf("actionable+a: focused=%v isActionable=%v, want focusList/false", m.focused, m.isActionableView)
							}
						} else if m.focused != focusActionable || !m.isActionableView {
							t.Fatalf("%s+a: focused=%v isActionable=%v, want focusActionable/true",
								st.name, m.focused, m.isActionableView)
						}

					case "h":
						switch st.name {
						case "history":
							if m.focused != focusList || m.isHistoryView {
								t.Fatalf("history+h: focused=%v isHistoryView=%v, want focusList/false",
									m.focused, m.isHistoryView)
							}
						case "board", "graph", "insights", "insights_heatmap_drilldown", "tree":
							if m.focused != priorFocus {
								t.Fatalf("%s+h: focused=%v, want in-view navigation staying in %v", st.name, m.focused, priorFocus)
							}
						case "label_dashboard":
							if m.focused != focusLabelDashboard || !m.showLabelHealthDetail {
								t.Fatalf("label_dashboard+h: focused=%v showLabelHealthDetail=%v, want focusLabelDashboard/true",
									m.focused, m.showLabelHealthDetail)
							}
						default:
							if m.focused != focusHistory || !m.isHistoryView {
								t.Fatalf("%s+h: focused=%v isHistoryView=%v, want focusHistory/true",
									st.name, m.focused, m.isHistoryView)
							}
						}

					case "P":
						switch st.name {
						case "list":
							if m.focused != focusSprint || !m.isSprintView {
								t.Fatalf("list+P: focused=%v isSprintView=%v, want focusSprint/true", m.focused, m.isSprintView)
							}
						case "sprint":
							if m.focused != focusList || m.isSprintView {
								t.Fatalf("sprint+P: focused=%v isSprintView=%v, want focusList/false", m.focused, m.isSprintView)
							}
						default:
							if m.focused != priorFocus || m.isSprintView {
								t.Fatalf("%s+P: focused=%v isSprintView=%v, want unchanged %v/false",
									st.name, m.focused, m.isSprintView, priorFocus)
							}
						}

					case "[":
						if st.name == "label_dashboard" {
							if m.focused != focusList {
								t.Fatalf("label_dashboard+[: focused=%v, want focusList", m.focused)
							}
						} else if m.focused != focusLabelDashboard || m.showVelocityComparison {
							t.Fatalf("%s+[: focused=%v showVelocity=%v, want focusLabelDashboard/false",
								st.name, m.focused, m.showVelocityComparison)
						}

					case "]":
						if st.name == "attention" {
							if m.focused != focusList {
								t.Fatalf("attention+]: focused=%v, want focusList", m.focused)
							}
						} else if m.focused != focusAttention {
							t.Fatalf("%s+]: focused=%v, want focusAttention", st.name, m.focused)
						}

					case "f":
						switch st.name {
						case "flow_matrix":
							if m.focused != focusList {
								t.Fatalf("flow_matrix+f: focused=%v, want focusList", m.focused)
							}
						case "history":
							if m.focused != focusHistory || !m.historyView.showFileTree {
								t.Fatalf("history+f: focused=%v showFileTree=%v, want focusHistory/true",
									m.focused, m.historyView.showFileTree)
							}
						default:
							if m.focused != focusFlowMatrix {
								t.Fatalf("%s+f: focused=%v, want focusFlowMatrix", st.name, m.focused)
							}
						}
					}
				})
			}
		}
	})

	t.Run("SubViewExitPreservesActiveListFilter", func(t *testing.T) {
		subViews := []fsmStateSpec{}
		for _, st := range states {
			if st.name != "list" && st.name != "insights_heatmap_drilldown" && st.name != "velocity_comparison" {
				subViews = append(subViews, st)
			}
		}
		for _, st := range subViews {
			t.Run(st.name, func(t *testing.T) {
				m := setupFSMModel(t, "")
				u, _ := m.Update(keyMsg("o"))
				m = u.(*Model)
				if m.currentFilter != "open" {
					t.Fatalf("expected active 'open' filter before entering %s, got %q", st.name, m.currentFilter)
				}

				m = st.enter(t, m)
				if m.focused != st.wantFocus {
					t.Fatalf("enter %s: focused=%v, want %v", st.name, m.focused, st.wantFocus)
				}

				u, _ = m.Update(keyMsg("esc"))
				m = u.(*Model)
				if m.focused != focusList {
					t.Fatalf("%s+esc: focused=%v, want focusList", st.name, m.focused)
				}
				if m.currentFilter != "open" {
					t.Fatalf("%s+esc cleared active list filter: currentFilter=%q, want %q", st.name, m.currentFilter, "open")
				}
			})
		}
	})

	t.Run("IsTextInputActiveGuardsGlobalAndToggleKeys", func(t *testing.T) {
		guardKeys := []string{"?", "~", ";", "`", "q", "b", "g", "i", "E", "a", "h", "P", "[", "]", "f"}

		inputModes := []struct {
			name      string
			wantFocus focus
			enter     func(t *testing.T, m *Model) *Model
			value     func(m *Model) string
		}{
			{
				name:      "list_filtering",
				wantFocus: focusList,
				enter: func(t *testing.T, m *Model) *Model {
					u, _ := m.Update(keyMsg("/"))
					return u.(*Model)
				},
				value: func(m *Model) string { return m.list.FilterInput.Value() },
			},
			{
				name:      "board_search",
				wantFocus: focusBoard,
				enter: func(t *testing.T, m *Model) *Model {
					u, _ := m.Update(keyMsg("b"))
					m = u.(*Model)
					u, _ = m.Update(keyMsg("/"))
					return u.(*Model)
				},
				value: func(m *Model) string { return m.board.SearchQuery() },
			},
			{
				name:      "history_search",
				wantFocus: focusHistory,
				enter: func(t *testing.T, m *Model) *Model {
					u, _ := m.Update(keyMsg("h"))
					m = u.(*Model)
					report := createTestHistoryReportWithFiles()
					m.historyView = NewHistoryModel(report, testTheme())
					makeHistoryReportCurrent(m, report)
					u, _ = m.Update(keyMsg("/"))
					return u.(*Model)
				},
				value: func(m *Model) string { return m.historyView.SearchQuery() },
			},
			{
				name:      "label_picker",
				wantFocus: focusLabelPicker,
				enter: func(t *testing.T, m *Model) *Model {
					u, _ := m.Update(keyMsg("l"))
					return u.(*Model)
				},
				value: func(m *Model) string { return m.labelPicker.InputValue() },
			},
			{
				name:      "time_travel_input",
				wantFocus: focusTimeTravelInput,
				enter: func(t *testing.T, m *Model) *Model {
					u, _ := m.Update(keyMsg("t"))
					return u.(*Model)
				},
				value: func(m *Model) string { return m.timeTravelInput.Value() },
			},
		}

		for _, im := range inputModes {
			t.Run(im.name, func(t *testing.T) {
				m := setupFSMModel(t, "")
				m = im.enter(t, m)
				if !m.isTextInputActive() {
					t.Fatalf("%s: expected isTextInputActive() == true", im.name)
				}

				for _, k := range guardKeys {
					u, cmd := m.Update(keyMsg(k))
					m = u.(*Model)
					if m.focused != im.wantFocus {
						t.Fatalf("%s+%q hijacked focus: got %v, want %v", im.name, k, m.focused, im.wantFocus)
					}
					if m.showHelp || m.showTutorial || m.showShortcutsSidebar || m.showQuitConfirm {
						t.Fatalf("%s+%q opened global overlay: help=%v tut=%v sidebar=%v quit=%v",
							im.name, k, m.showHelp, m.showTutorial, m.showShortcutsSidebar, m.showQuitConfirm)
					}
					if k == "q" && cmd != nil && im.name != "list_filtering" {
						// Note: list_filtering returns textinput blink/filter cmds, never tea.Quit;
						// non-list inputs return nil on rune append.
						_ = cmd
					}
				}

				gotVal := im.value(m)
				wantVal := strings.Join(guardKeys, "")
				if gotVal != wantVal {
					t.Fatalf("%s typed value = %q, want %q", im.name, gotVal, wantVal)
				}
			})
		}
	})

	t.Run("TreeSaveState_ZeroWritesWhenNoSavedConfigOrTimeTravel", func(t *testing.T) {
		t.Run("BV_NO_SAVED_CONFIG_prevents_tree_state_write", func(t *testing.T) {
			t.Setenv("BV_NO_SAVED_CONFIG", "1")
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			beadsFile := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(beadsFile, []byte(""), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			m := setupFSMModel(t, beadsFile)
			u, _ := m.Update(keyMsg("E"))
			m = u.(*Model)
			if m.focused != focusTree {
				t.Fatalf("expected focusTree after 'E', got %v", m.focused)
			}

			// Exercise tree state-mutating keys (expand/collapse all, toggle, cycle sort, cycle mode).
			for _, k := range []string{"X", "Z", "enter", "s", "m"} {
				u, _ = m.Update(keyMsg(k))
				m = u.(*Model)
			}

			statePath := filepath.Join(beadsDir, "tree-state.json")
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Fatalf("expected zero %s writes when BV_NO_SAVED_CONFIG=1, got stat err=%v", statePath, err)
			}
		})

		t.Run("TimeTravelMode_marks_tree_read_only", func(t *testing.T) {
			t.Setenv("BV_NO_SAVED_CONFIG", "")
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			beadsFile := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(beadsFile, []byte(""), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			m := setupFSMModel(t, beadsFile)
			m.timeTravelMode = true
			m.tree.SetReadOnly(true)

			u, _ := m.Update(keyMsg("E"))
			m = u.(*Model)
			for _, k := range []string{"X", "Z", "enter"} {
				u, _ = m.Update(keyMsg(k))
				m = u.(*Model)
			}

			statePath := filepath.Join(beadsDir, "tree-state.json")
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Fatalf("expected zero %s writes in time-travel read-only mode, got stat err=%v", statePath, err)
			}
		})
	})
}
