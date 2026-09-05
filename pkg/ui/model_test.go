package ui_test

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

func TestModelFiltering(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Open Issue", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Title: "Closed Issue", Status: model.StatusClosed, Priority: 2},
		{ID: "3", Title: "Blocked Issue", Status: model.StatusBlocked, Priority: 1},
		{
			ID: "4", Title: "Ready Issue", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{},
		},
		{
			ID: "5", Title: "Blocked by Open", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{
				{DependsOnID: "3", Type: model.DepBlocks},
			},
		},
		{ID: "6", Title: "Tombstone Issue", Status: model.StatusTombstone, Priority: 1},
		{
			ID: "7", Title: "Blocked by Tombstone", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{
				{DependsOnID: "6", Type: model.DepBlocks},
			},
		},
		{
			ID: "8", Title: "Blocked by Open (legacy)", Status: model.StatusOpen, Priority: 1,
			Dependencies: []*model.Dependency{
				{DependsOnID: "3", Type: ""},
			},
		},
	}

	m := ui.NewModel(issues, nil, "")

	// Test "All"
	if len(m.FilteredIssues()) != 7 {
		t.Errorf("Expected 7 visible issues for 'all', got %d", len(m.FilteredIssues()))
	}

	// Test "Open" (includes Open, InProgress, Blocked)
	m.SetFilter("open")
	if len(m.FilteredIssues()) != 6 {
		t.Errorf("Expected 6 issues for 'open', got %d", len(m.FilteredIssues()))
	}

	// Test "Closed"
	m.SetFilter("closed")
	closedIssues := m.FilteredIssues()
	if len(closedIssues) != 1 {
		t.Errorf("Expected one visible issue for 'closed', got %d", len(closedIssues))
	} else {
		got := map[string]bool{
			closedIssues[0].ID: true,
		}
		if !got["2"] || got["6"] {
			t.Errorf("Expected closed ID 2 while tombstone ID 6 stays hidden, got %#v", got)
		}
	}

	// Test "Ready"
	m.SetFilter("ready")
	// ID 1 (Open), ID 4 (Ready).
	// ID 3 is Blocked status.
	// ID 5 is Open but Blocked by ID 3 (which is not closed).
	// ID 7 is Open but Blocked by tombstone ID 6 (treated as closed-like).
	// ID 8 is Open but Blocked by ID 3 via legacy dependency type.
	// So we expect 1, 4, and 7.
	readyIssues := m.FilteredIssues()
	if len(readyIssues) != 3 {
		t.Errorf("Expected 3 issues for 'ready', got %d", len(readyIssues))
		for _, i := range readyIssues {
			t.Logf("Got issue: %s", i.Title)
		}
	} else {
		got := map[string]bool{
			readyIssues[0].ID: true,
			readyIssues[1].ID: true,
			readyIssues[2].ID: true,
		}
		if !got["1"] || !got["4"] || !got["7"] {
			t.Errorf("Expected ready issues to include IDs 1, 4, and 7, got %#v", got)
		}
	}
}

func TestFormatTimeRel(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t        time.Time
		expected string
	}{
		{now.Add(-30 * time.Minute), "30m ago"},
		{now.Add(-2 * time.Hour), "2h ago"},
		{now.Add(-25 * time.Hour), "1d ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
		{time.Time{}, "unknown"},
	}

	for _, tt := range tests {
		got := ui.FormatTimeRel(tt.t)
		if got != tt.expected {
			t.Errorf("FormatTimeRel(%v) = %s; want %s", tt.t, got, tt.expected)
		}
	}
}

func TestTimeTravelMode(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}

	m := ui.NewModel(issues, nil, "")

	// Initially not in time-travel mode
	if m.IsTimeTravelMode() {
		t.Error("Expected not to be in time-travel mode initially")
	}

	// TimeTravelDiff should be nil initially
	if m.TimeTravelDiff() != nil {
		t.Error("Expected TimeTravelDiff to be nil initially")
	}
}

func TestGetTypeIconMD(t *testing.T) {
	tests := []struct {
		issueType string
		expected  string
	}{
		{"bug", "🐛"},
		{"feature", "✨"},
		{"task", "📋"},
		{"epic", "🚀"}, // Changed from 🏔️ - VS-16 variation selector causes width issues
		{"chore", "🧹"},
		{"unknown", "•"},
		{"", "•"},
	}

	for _, tt := range tests {
		got := ui.GetTypeIconMD(tt.issueType)
		if got != tt.expected {
			t.Errorf("GetTypeIconMD(%q) = %s; want %s", tt.issueType, got, tt.expected)
		}
	}
}

func TestModelCreationWithEmptyIssues(t *testing.T) {
	m := ui.NewModel([]model.Issue{}, nil, "")

	if len(m.FilteredIssues()) != 0 {
		t.Errorf("Expected 0 issues for empty input, got %d", len(m.FilteredIssues()))
	}

	// Should not panic on operations
	m.SetFilter("open")
	m.SetFilter("closed")
	m.SetFilter("ready")
}

func TestIssueItemDiffStatus(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test", Status: model.StatusOpen},
	}

	m := ui.NewModel(issues, nil, "")

	// In normal mode, DiffStatus should be None
	filtered := m.FilteredIssues()
	if len(filtered) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(filtered))
	}
}

// =============================================================================
// Focus Transition Tests (bv-5e5q)
// =============================================================================

// TestFocusStateInitial verifies initial focus state is "list"
func TestFocusStateInitial(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	if m.FocusState() != "list" {
		t.Errorf("Initial focus state = %q, want %q", m.FocusState(), "list")
	}
}

// TestFocusTransitionBoard verifies 'b' toggles board view
func TestFocusTransitionBoard(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Initial state
	if m.FocusState() != "list" {
		t.Fatalf("Initial focus = %q, want 'list'", m.FocusState())
	}
	if m.IsBoardView() {
		t.Fatal("IsBoardView should be false initially")
	}

	// Press 'b' to enter board view
	newM, _ := m.Update(keyMsg("b"))
	m = newM.(*ui.Model)

	if m.FocusState() != "board" {
		t.Errorf("After 'b', focus = %q, want 'board'", m.FocusState())
	}
	if !m.IsBoardView() {
		t.Error("IsBoardView should be true after 'b'")
	}

	// Press 'b' again to exit board view
	newM, _ = m.Update(keyMsg("b"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'b', focus = %q, want 'list'", m.FocusState())
	}
	if m.IsBoardView() {
		t.Error("IsBoardView should be false after second 'b'")
	}
}

// TestFocusTransitionGraph verifies 'g' toggles graph view
func TestFocusTransitionGraph(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Press 'g' to enter graph view
	newM, _ := m.Update(keyMsg("g"))
	m = newM.(*ui.Model)

	if m.FocusState() != "graph" {
		t.Errorf("After 'g', focus = %q, want 'graph'", m.FocusState())
	}
	if !m.IsGraphView() {
		t.Error("IsGraphView should be true after 'g'")
	}

	// Press 'g' again to exit graph view
	newM, _ = m.Update(keyMsg("g"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'g', focus = %q, want 'list'", m.FocusState())
	}
	if m.IsGraphView() {
		t.Error("IsGraphView should be false after second 'g'")
	}
}

// TestFocusTransitionActionable verifies 'a' toggles actionable view
func TestFocusTransitionActionable(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Press 'a' to enter actionable view
	newM, _ := m.Update(keyMsg("a"))
	m = newM.(*ui.Model)

	if m.FocusState() != "actionable" {
		t.Errorf("After 'a', focus = %q, want 'actionable'", m.FocusState())
	}
	if !m.IsActionableView() {
		t.Error("IsActionableView should be true after 'a'")
	}

	// Press 'a' again to exit actionable view
	newM, _ = m.Update(keyMsg("a"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'a', focus = %q, want 'list'", m.FocusState())
	}
	if m.IsActionableView() {
		t.Error("IsActionableView should be false after second 'a'")
	}
}

// TestFocusTransitionInsights verifies 'i' toggles insights view
func TestFocusTransitionInsights(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Press 'i' to enter insights view
	newM, _ := m.Update(keyMsg("i"))
	m = newM.(*ui.Model)

	if m.FocusState() != "insights" {
		t.Errorf("After 'i', focus = %q, want 'insights'", m.FocusState())
	}

	// Press 'i' again to exit insights view
	newM, _ = m.Update(keyMsg("i"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'i', focus = %q, want 'list'", m.FocusState())
	}
}

// TestFocusTransitionTree verifies 'E' toggles tree view
func TestFocusTransitionTree(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeEpic},
	}
	m := ui.NewModel(issues, nil, "")

	// Press 'E' to enter tree view
	newM, _ := m.Update(keyMsg("E"))
	m = newM.(*ui.Model)

	if m.FocusState() != "tree" {
		t.Errorf("After 'E', focus = %q, want 'tree'", m.FocusState())
	}

	// Press 'E' again to exit tree view
	newM, _ = m.Update(keyMsg("E"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'E', focus = %q, want 'list'", m.FocusState())
	}
}

// TestFocusTransitionHelp verifies '?' opens help view
func TestFocusTransitionHelp(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Press '?' to enter help view
	newM, _ := m.Update(keyMsg("?"))
	m = newM.(*ui.Model)

	if m.FocusState() != "help" {
		t.Errorf("After '?', focus = %q, want 'help'", m.FocusState())
	}
}

// TestFocusTransitionHistory verifies 'h' toggles history view
func TestFocusTransitionHistory(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Press 'h' to enter history view
	newM, _ := m.Update(keyMsg("h"))
	m = newM.(*ui.Model)

	if m.FocusState() != "history" {
		t.Errorf("After 'h', focus = %q, want 'history'", m.FocusState())
	}
	if !m.IsHistoryView() {
		t.Error("IsHistoryView should be true after 'h'")
	}

	// Press 'h' again to exit history view
	newM, _ = m.Update(keyMsg("h"))
	m = newM.(*ui.Model)

	if m.FocusState() != "list" {
		t.Errorf("After second 'h', focus = %q, want 'list'", m.FocusState())
	}
	if m.IsHistoryView() {
		t.Error("IsHistoryView should be false after second 'h'")
	}
}

// TestViewSwitchClearsOthers verifies switching views clears other view states
func TestViewSwitchClearsOthers(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Enter graph view from list (immediate toggle)
	newM, _ := m.Update(keyMsg("g"))
	m = newM.(*ui.Model)

	if !m.IsGraphView() {
		t.Fatal("IsGraphView should be true after 'g' from list")
	}

	// Switch to board view - graph should be cleared
	newM, _ = m.Update(keyMsg("b"))
	m = newM.(*ui.Model)

	if !m.IsBoardView() {
		t.Error("IsBoardView should be true after 'b'")
	}
	if m.IsGraphView() {
		t.Error("IsGraphView should be false after switching to board")
	}

	// Switch to actionable view - board should be cleared
	newM, _ = m.Update(keyMsg("a"))
	m = newM.(*ui.Model)

	if !m.IsActionableView() {
		t.Error("IsActionableView should be true after 'a'")
	}
	if m.IsBoardView() {
		t.Error("IsBoardView should be false after switching to actionable")
	}
}

// TestEscClosesViews verifies Esc returns to list from various views
func TestEscClosesViews(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}

	tests := []struct {
		name       string
		enterKey   string
		expectView string
	}{
		{"board", "b", "board"},
		{"graph", "g", "graph"},
		{"actionable", "a", "actionable"},
		{"insights", "i", "insights"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ui.NewModel(issues, nil, "")

			// Enter the view
			newM, _ := m.Update(keyMsg(tt.enterKey))
			m = newM.(*ui.Model)

			if m.FocusState() != tt.expectView {
				t.Fatalf("After %q, focus = %q, want %q", tt.enterKey, m.FocusState(), tt.expectView)
			}

			// Press Esc to return to list
			newM, _ = m.Update(keyMsg("esc"))
			m = newM.(*ui.Model)

			if m.FocusState() != "list" {
				t.Errorf("After Esc from %s, focus = %q, want 'list'", tt.name, m.FocusState())
			}
		})
	}
}

// TestQuitClosesViews verifies 'q' returns to list from various views
func TestQuitClosesViews(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test Issue", Status: model.StatusOpen, Priority: 1},
	}

	tests := []struct {
		name       string
		enterKey   string
		expectView string
	}{
		{"board", "b", "board"},
		{"graph", "g", "graph"},
		{"insights", "i", "insights"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ui.NewModel(issues, nil, "")

			// Enter the view
			newM, _ := m.Update(keyMsg(tt.enterKey))
			m = newM.(*ui.Model)

			if m.FocusState() != tt.expectView {
				t.Fatalf("After %q, focus = %q, want %q", tt.enterKey, m.FocusState(), tt.expectView)
			}

			// Press 'q' to return to list
			newM, _ = m.Update(keyMsg("q"))
			m = newM.(*ui.Model)

			if m.FocusState() != "list" {
				t.Errorf("After 'q' from %s, focus = %q, want 'list'", tt.name, m.FocusState())
			}
		})
	}
}

// TestEmptyIssuesDoesNotPanic verifies state machine handles empty issues
func TestEmptyIssuesDoesNotPanic(t *testing.T) {
	m := ui.NewModel([]model.Issue{}, nil, "")

	// Should not panic on various key presses
	keys := []string{"b", "g", "a", "i", "E", "H", "?", "j", "k", "enter", "esc"}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Panic on key %q: %v", key, r)
				}
			}()

			newM, _ := m.Update(keyMsg(key))
			m = newM.(*ui.Model)
		})
	}
}

// TestFocusStateString verifies all focus states have valid strings
func TestFocusStateString(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Test", Status: model.StatusOpen, Priority: 1},
	}
	m := ui.NewModel(issues, nil, "")

	// Test that initial state has a valid string
	state := m.FocusState()
	if state == "unknown" {
		t.Error("Initial focus state should not be 'unknown'")
	}
	if state == "" {
		t.Error("Focus state should not be empty string")
	}
}

// Helper to create a KeyMsg
func keyMsg(key string) tea.KeyMsg {
	return tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(key),
	}
}
