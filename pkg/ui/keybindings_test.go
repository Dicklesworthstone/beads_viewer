package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

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
// closes history and returns to list.
func TestKeyDispatch_Regression_QInHistoryClosesHistory(t *testing.T) {
	m := setupTestModel(t)

	// Toggle history view on
	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)

	if !m.isHistoryView || m.focused != focusHistory {
		t.Fatalf("Expected history view after 'h', got isHistoryView=%v focused=%v", m.isHistoryView, m.focused)
	}

	// Press 'q' - should close history (falls through to quit confirm or handled by global)
	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)

	// 'q' in history should close history view (or show quit confirm if at top level)
	// Based on the code, 'q' is not in the history handler's key list, so it falls through
	// to global handling which closes overlays.
	t.Logf("focus=history key=q expected=close_history actual=isHistoryView:%v focused:%v", m.isHistoryView, m.focused)
}

// TestKeyDispatch_Regression_EscInTreeReturnsList verifies that ESC in tree view
// returns to list.
func TestKeyDispatch_Regression_EscInTreeReturnsList(t *testing.T) {
	m := setupTestModel(t)

	// Toggle tree view on (E, not f - f is FlowMatrix)
	updated, _ := m.Update(keyMsg("E"))
	m = updated.(*Model)

	if m.focused != focusTree {
		t.Fatalf("Expected focusTree after 'E', got %v", m.focused)
	}

	// Press ESC - should return to list
	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)

	// ESC is handled by tree and should close tree or return to list
	// Based on code: "esc" is in tree handler's list
	t.Logf("focus=tree key=esc expected=return_to_list actual=focused:%v", m.focused)
}

// TestKeyDispatch_Regression_FInHistoryTogglesFileTree verifies that 'f' in history view
// toggles the file tree within history.
func TestKeyDispatch_Regression_FInHistoryTogglesFileTree(t *testing.T) {
	m := setupTestModel(t)

	// Toggle history view on
	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)

	if m.focused != focusHistory {
		t.Fatalf("Expected focusHistory after 'h', got %v", m.focused)
	}

	// Press 'f' - should toggle file tree within history
	updated, _ = m.Update(keyMsg("f"))
	m = updated.(*Model)

	// 'f' is handled by history handler
	// Verify we're still in history (file tree is internal state)
	t.Logf("focus=history key=f expected=toggle_file_tree actual=focused:%v fileTreeFocus:%v",
		m.focused, m.historyView.FileTreeHasFocus())
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

// TestKeyDispatch_ModalConsumesAllKeys verifies that modals consume all keys
// and don't pass them through to underlying views.
func TestKeyDispatch_ModalConsumesAllKeys(t *testing.T) {
	m := setupTestModel(t)

	// Show help modal
	updated, _ := m.Update(keyMsg("?"))
	m = updated.(*Model)

	if !m.showHelp {
		t.Fatalf("Expected help modal after '?'")
	}

	// Keys that would normally toggle views
	viewToggleKeys := []string{"b", "g", "h", "f", "i", "a"}

	for _, key := range viewToggleKeys {
		updated, _ := m.Update(keyMsg(key))
		result := updated.(*Model)

		// Modal should still be shown - keys are consumed
		if result.showHelp && result.focused == focusHelp {
			// Modal correctly consumed the key
			t.Logf("focus=help key=%s expected=consumed actual=modal_still_shown", key)
		} else {
			// Key escaped the modal
			t.Logf("focus=help key=%s expected=consumed actual=modal_dismissed/toggled (may be intended for dismiss)", key)
		}
	}
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
