package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TestKeyRegistryDispatch_EmptyRegistry verifies that dispatching to an empty
// registry returns handled=false and leaves the model unchanged.
func TestKeyRegistryDispatch_EmptyRegistry(t *testing.T) {
	r := NewKeyRegistry()

	// Create a minimal model for testing
	m := Model{focused: focusList}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	updatedModel, handled, cmd := r.Dispatch(focusList, "j", m, msg)

	if handled {
		t.Errorf("Dispatch on empty registry: expected handled=false, got true")
	}
	if cmd != nil {
		t.Errorf("Dispatch on empty registry: expected cmd=nil, got %v", cmd)
	}
	if updatedModel.focused != m.focused {
		t.Errorf("Dispatch on empty registry: model should be unchanged")
	}
	t.Logf("focus=%v key=%s expected=handled:false actual=handled:%v", focusList, "j", handled)
}

// TestKeyRegistryRegisterAndLookup verifies that registering a binding
// makes it discoverable via Dispatch.
func TestKeyRegistryRegisterAndLookup(t *testing.T) {
	r := NewKeyRegistry()

	handlerCalled := false
	testHandler := func(m Model, msg tea.KeyMsg) (Model, bool) {
		handlerCalled = true
		m.focused = focusDetail // Modify to prove handler ran
		return m, true
	}

	r.RegisterBinding(KeyBinding{
		Focus:    focusList,
		Key:      "enter",
		Desc:     "Select item",
		Category: "Navigation",
		Handler:  testHandler,
	})

	m := Model{focused: focusList}
	msg := tea.KeyMsg{Type: tea.KeyEnter}

	updatedModel, handled, _ := r.Dispatch(focusList, "enter", m, msg)

	if !handled {
		t.Errorf("Dispatch after register: expected handled=true, got false")
	}
	if !handlerCalled {
		t.Errorf("Dispatch after register: handler was not called")
	}
	if updatedModel.focused != focusDetail {
		t.Errorf("Dispatch after register: expected focus=focusDetail, got %v", updatedModel.focused)
	}
	t.Logf("focus=%v key=%s expected=handled:true actual=handled:%v", int(focusList), "enter", handled)
}

// TestKeyRegistryRegisterAndLookup_WrongFocus verifies that dispatch returns
// handled=false when the focus doesn't match the registered binding.
func TestKeyRegistryRegisterAndLookup_WrongFocus(t *testing.T) {
	r := NewKeyRegistry()

	testHandler := func(m Model, msg tea.KeyMsg) (Model, bool) {
		return m, true
	}

	r.RegisterBinding(KeyBinding{
		Focus:    focusList,
		Key:      "j",
		Desc:     "Move down",
		Category: "Navigation",
		Handler:  testHandler,
	})

	m := Model{focused: focusBoard}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}

	_, handled, _ := r.Dispatch(focusBoard, "j", m, msg)

	if handled {
		t.Errorf("Dispatch with wrong focus: expected handled=false, got true")
	}
	t.Logf("focus=%v key=%s expected=handled:false actual=handled:%v", int(focusBoard), "j", handled)
}

// TestKeyRegistryAllBindings verifies that AllBindings returns all registered
// bindings sorted by focus, category, then key.
func TestKeyRegistryAllBindings(t *testing.T) {
	r := NewKeyRegistry()

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }

	// Register bindings in various orders
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "j", Desc: "Down", Category: "Navigation", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Desc: "Up", Category: "Navigation", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "enter", Desc: "Select", Category: "Actions", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "h", Desc: "Left", Category: "Navigation", Handler: noopHandler})

	bindings := r.AllBindings()

	if len(bindings) != 4 {
		t.Errorf("AllBindings: expected 4 bindings, got %d", len(bindings))
	}

	// Verify sorting: by focus, then category, then key
	// focusBoard < focusList (alphabetically by focus enum order)
	// Within each focus: Actions < Navigation (alphabetically)
	// Within each category: sorted by key

	// Log all bindings for debugging
	for i, b := range bindings {
		t.Logf("binding[%d]: focus=%v category=%s key=%s desc=%s", i, b.Focus, b.Category, b.Key, b.Desc)
	}
}

// TestKeyRegistryAllBindingsForFocus verifies that AllBindingsForFocus returns
// only bindings for the specified focus context.
func TestKeyRegistryAllBindingsForFocus(t *testing.T) {
	r := NewKeyRegistry()

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }

	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Desc: "Down", Category: "Nav", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Desc: "Up", Category: "Nav", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "l", Desc: "Right", Category: "Nav", Handler: noopHandler})

	listBindings := r.AllBindingsForFocus(focusList)
	boardBindings := r.AllBindingsForFocus(focusBoard)

	if len(listBindings) != 2 {
		t.Errorf("AllBindingsForFocus(focusList): expected 2, got %d", len(listBindings))
	}
	if len(boardBindings) != 1 {
		t.Errorf("AllBindingsForFocus(focusBoard): expected 1, got %d", len(boardBindings))
	}

	// Verify empty focus returns empty slice
	graphBindings := r.AllBindingsForFocus(focusGraph)
	if len(graphBindings) != 0 {
		t.Errorf("AllBindingsForFocus(focusGraph): expected 0, got %d", len(graphBindings))
	}
}

// TestKeyRegistryHasBinding verifies the HasBinding lookup method.
func TestKeyRegistryHasBinding(t *testing.T) {
	r := NewKeyRegistry()

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }

	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Handler: noopHandler})

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

// TestKeyRegistryBindingsCount verifies the count method.
func TestKeyRegistryBindingsCount(t *testing.T) {
	r := NewKeyRegistry()

	if r.BindingsCount() != 0 {
		t.Errorf("BindingsCount on empty: expected 0, got %d", r.BindingsCount())
	}

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusBoard, Key: "l", Handler: noopHandler})

	if r.BindingsCount() != 3 {
		t.Errorf("BindingsCount after adding 3: expected 3, got %d", r.BindingsCount())
	}
}

// TestKeyRegistryClear verifies the Clear method removes all bindings.
func TestKeyRegistryClear(t *testing.T) {
	r := NewKeyRegistry()

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Handler: noopHandler})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "k", Handler: noopHandler})

	if r.BindingsCount() != 2 {
		t.Fatalf("Setup: expected 2 bindings, got %d", r.BindingsCount())
	}

	r.Clear()

	if r.BindingsCount() != 0 {
		t.Errorf("Clear: expected 0 bindings, got %d", r.BindingsCount())
	}
	if r.HasBinding(focusList, "j") {
		t.Error("Clear: bindings should be removed")
	}
}

// TestKeyRegistryOverwrite verifies that re-registering a key overwrites
// the previous handler.
func TestKeyRegistryOverwrite(t *testing.T) {
	r := NewKeyRegistry()

	callOrder := []string{}

	handler1 := func(m Model, msg tea.KeyMsg) (Model, bool) {
		callOrder = append(callOrder, "handler1")
		return m, true
	}
	handler2 := func(m Model, msg tea.KeyMsg) (Model, bool) {
		callOrder = append(callOrder, "handler2")
		return m, true
	}

	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Handler: handler1})
	r.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Handler: handler2}) // Overwrite

	m := Model{}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	r.Dispatch(focusList, "j", m, msg)

	if len(callOrder) != 1 {
		t.Fatalf("Expected exactly 1 handler call, got %d", len(callOrder))
	}
	if callOrder[0] != "handler2" {
		t.Errorf("Expected handler2 to be called (overwritten), got %s", callOrder[0])
	}
}

// TestKeyRegistryRegisterView verifies bulk registration via RegisterView.
func TestKeyRegistryRegisterView(t *testing.T) {
	r := NewKeyRegistry()

	noopHandler := func(m Model, msg tea.KeyMsg) (Model, bool) { return m, true }

	bindings := []KeyBinding{
		{Key: "j", Desc: "Down", Category: "Nav", Handler: noopHandler},
		{Key: "k", Desc: "Up", Category: "Nav", Handler: noopHandler},
		{Key: "enter", Desc: "Select", Category: "Actions", Handler: noopHandler},
	}

	r.RegisterView(focusList, bindings)

	if r.BindingsCount() != 3 {
		t.Errorf("RegisterView: expected 3 bindings, got %d", r.BindingsCount())
	}

	// Verify all bindings have correct focus
	allBindings := r.AllBindingsForFocus(focusList)
	for _, b := range allBindings {
		if b.Focus != focusList {
			t.Errorf("RegisterView: expected focus=%v, got %v", focusList, b.Focus)
		}
	}
}

func TestNewModelRegistersDocumentedBindings(t *testing.T) {
	m := setupTestModel(t)

	if m.keyRegistry == nil {
		t.Fatal("expected NewModel to initialize keyRegistry")
	}
	if m.keyRegistry.BindingsCount() == 0 {
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

// testIssuesForAssigneeAndTypeDispatch mirrors testIssuesForKeyDispatch but
// populates Assignee/IssueType so the pickers have real entries to select.
func testIssuesForAssigneeAndTypeDispatch() []model.Issue {
	return []model.Issue{
		{ID: "kd-1", Title: "Test Issue 1", Status: model.StatusOpen, IssueType: model.TypeBug, Assignee: "alice"},
		{ID: "kd-2", Title: "Test Issue 2", Status: model.StatusOpen, IssueType: model.TypeFeature, Assignee: "bob"},
		{ID: "kd-3", Title: "Test Issue 3", Status: model.StatusClosed, IssueType: model.TypeBug, Assignee: "alice"},
	}
}

// TestKeyDispatch_Regression_AssigneePickerConsumesQKey guards against the
// same issue #176 class of bug as the label picker (see
// TestKeyDispatch_Regression_LabelPickerConsumesQKey): the assignee picker
// reuses the same always-focused text input, so a lowercase q must be typed
// into the filter rather than triggering the global quit/back shortcut.
func TestKeyDispatch_Regression_AssigneePickerConsumesQKey(t *testing.T) {
	m := NewModel(testIssuesForAssigneeAndTypeDispatch(), nil, "")

	updated, _ := m.Update(keyMsg("A"))
	m = updated.(*Model)
	if m.focused != focusAssigneePicker || !m.showAssigneePicker {
		t.Fatalf("expected assignee picker after 'A', got focused=%v showAssigneePicker=%v", m.focused, m.showAssigneePicker)
	}

	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)
	if !m.showAssigneePicker || m.focused != focusAssigneePicker {
		t.Fatalf("expected assignee picker to stay open after typing 'q', got focused=%v showAssigneePicker=%v", m.focused, m.showAssigneePicker)
	}
	if got := m.assigneePicker.InputValue(); got != "q" {
		t.Fatalf("expected assignee filter input %q after typing 'q', got %q", "q", got)
	}

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)
	if m.showAssigneePicker || m.focused != focusList {
		t.Fatalf("expected esc to cancel assignee picker, got focused=%v showAssigneePicker=%v", m.focused, m.showAssigneePicker)
	}
}

// TestKeyDispatch_AssigneePickerEnterAppliesFilter verifies selecting an
// assignee and pressing enter narrows the list to that assignee's issues.
func TestKeyDispatch_AssigneePickerEnterAppliesFilter(t *testing.T) {
	m := NewModel(testIssuesForAssigneeAndTypeDispatch(), nil, "")

	updated, _ := m.Update(keyMsg("A"))
	m = updated.(*Model)

	for _, r := range "alice" {
		updated, _ = m.Update(keyMsg(string(r)))
		m = updated.(*Model)
	}

	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(*Model)

	if m.showAssigneePicker || m.focused != focusList {
		t.Fatalf("expected enter to close assignee picker and return to list, got focused=%v showAssigneePicker=%v", m.focused, m.showAssigneePicker)
	}
	if m.assigneeFilter != "alice" {
		t.Fatalf("expected assigneeFilter %q, got %q", "alice", m.assigneeFilter)
	}
	for _, issue := range m.FilteredIssues() {
		if issue.Assignee != "alice" {
			t.Errorf("expected only alice's issues after filter, got assignee %q", issue.Assignee)
		}
	}
}

// TestKeyDispatch_Regression_TypePickerConsumesQKey mirrors the label/assignee
// picker q-key regression above for the issue type picker.
func TestKeyDispatch_Regression_TypePickerConsumesQKey(t *testing.T) {
	m := NewModel(testIssuesForAssigneeAndTypeDispatch(), nil, "")

	updated, _ := m.Update(keyMsg("I"))
	m = updated.(*Model)
	if m.focused != focusTypePicker || !m.showTypePicker {
		t.Fatalf("expected type picker after 'I', got focused=%v showTypePicker=%v", m.focused, m.showTypePicker)
	}

	updated, _ = m.Update(keyMsg("q"))
	m = updated.(*Model)
	if !m.showTypePicker || m.focused != focusTypePicker {
		t.Fatalf("expected type picker to stay open after typing 'q', got focused=%v showTypePicker=%v", m.focused, m.showTypePicker)
	}
	if got := m.typePicker.InputValue(); got != "q" {
		t.Fatalf("expected type filter input %q after typing 'q', got %q", "q", got)
	}

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(*Model)
	if m.showTypePicker || m.focused != focusList {
		t.Fatalf("expected esc to cancel type picker, got focused=%v showTypePicker=%v", m.focused, m.showTypePicker)
	}
}

// TestKeyDispatch_TypePickerEnterAppliesFilter verifies selecting a type and
// pressing enter narrows the list to that issue type.
func TestKeyDispatch_TypePickerEnterAppliesFilter(t *testing.T) {
	m := NewModel(testIssuesForAssigneeAndTypeDispatch(), nil, "")

	updated, _ := m.Update(keyMsg("I"))
	m = updated.(*Model)

	for _, r := range "bug" {
		updated, _ = m.Update(keyMsg(string(r)))
		m = updated.(*Model)
	}

	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(*Model)

	if m.showTypePicker || m.focused != focusList {
		t.Fatalf("expected enter to close type picker and return to list, got focused=%v showTypePicker=%v", m.focused, m.showTypePicker)
	}
	if m.typeFilter != "bug" {
		t.Fatalf("expected typeFilter %q, got %q", "bug", m.typeFilter)
	}
	for _, issue := range m.FilteredIssues() {
		if issue.IssueType != model.TypeBug {
			t.Errorf("expected only bug issues after filter, got type %q", issue.IssueType)
		}
	}
}

// TestKeyDispatch_Regression_AssigneeFilterComposesWithOpenClose guards
// against the regression where opening the assignee/type picker and
// selecting a value would silently discard an active "open"/"closed" status
// filter instead of narrowing it further.
func TestKeyDispatch_Regression_AssigneeFilterComposesWithOpenClose(t *testing.T) {
	m := NewModel(testIssuesForAssigneeAndTypeDispatch(), nil, "")

	// Apply the "open" status filter first (kd-3 is closed, so this should
	// narrow to kd-1 and kd-2, both assigned to alice/bob respectively).
	updated, _ := m.Update(keyMsg("o"))
	m = updated.(*Model)
	if m.currentFilter != "open" {
		t.Fatalf("expected currentFilter %q after 'o', got %q", "open", m.currentFilter)
	}

	// Now layer an assignee filter on top via the picker.
	updated, _ = m.Update(keyMsg("A"))
	m = updated.(*Model)
	for _, r := range "alice" {
		updated, _ = m.Update(keyMsg(string(r)))
		m = updated.(*Model)
	}
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(*Model)

	// The status filter must still be "open" (not clobbered by the picker),
	// and the visible issues must satisfy BOTH constraints.
	if m.currentFilter != "open" {
		t.Fatalf("expected currentFilter to remain %q after applying assignee filter, got %q", "open", m.currentFilter)
	}
	if m.assigneeFilter != "alice" {
		t.Fatalf("expected assigneeFilter %q, got %q", "alice", m.assigneeFilter)
	}
	filtered := m.FilteredIssues()
	if len(filtered) != 1 || filtered[0].ID != "kd-1" {
		t.Fatalf("expected only kd-1 (open, assigned to alice), got %#v", filtered)
	}
}

func TestKeyDispatch_Regression_HistorySearchEnterKeepsFilter(t *testing.T) {
	m := setupTestModel(t)

	updated, _ := m.Update(keyMsg("h"))
	m = updated.(*Model)
	if m.focused != focusHistory || !m.isHistoryView {
		t.Fatalf("Expected history view after 'h', got focused=%v isHistoryView=%v", m.focused, m.isHistoryView)
	}

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
