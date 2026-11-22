package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHandleSprintKeys_Exit(t *testing.T) {
	m := &Model{
		isSprintView: true,
		focused:      focusDetail,
		theme:        DefaultTheme(lipgloss.NewRenderer(nil)),
		width:        100,
		height:       40,
	}
	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	if m.isSprintView {
		t.Fatalf("expected sprint view to exit")
	}
	if m.focused != focusList {
		t.Fatalf("focused=%v; want focusList", m.focused)
	}
}

func TestHandleSprintKeys_NextPrevSprint(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
		{ID: "s2", Name: "Sprint 2", StartDate: now.AddDate(0, 0, -1), EndDate: now.AddDate(0, 0, 7), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		issues:         []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}},
		sprints:        sprints,
		selectedSprint: &sprints[0],
	}

	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s2" {
		t.Fatalf("after j: selected=%v; want s2", m.selectedSprint)
	}
	if m.sprintViewText == "" {
		t.Fatalf("expected sprintViewText to be populated after navigation")
	}

	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s1" {
		t.Fatalf("after k: selected=%v; want s1", m.selectedSprint)
	}
}

// =============================================================================
// truncateStrSprint Tests
// =============================================================================

func TestTruncateStrSprint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "long string truncated",
			input:    "hello world",
			maxLen:   8,
			expected: "hello w…",
		},
		{
			name:     "maxLen 3 no ellipsis",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen 2 no ellipsis",
			input:    "hello",
			maxLen:   2,
			expected: "he",
		},
		{
			name:     "maxLen 1 no ellipsis",
			input:    "hello",
			maxLen:   1,
			expected: "h",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
		{
			name:     "maxLen 0",
			input:    "hello",
			maxLen:   0,
			expected: "",
		},
		{
			name:     "unicode string truncation",
			input:    "日本語テスト",
			maxLen:   4,
			expected: "日本語…",
		},
		{
			name:     "mixed unicode",
			input:    "hello世界",
			maxLen:   6,
			expected: "hello…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateStrSprint(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateStrSprint(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// renderSprintDashboard Tests
// =============================================================================

func TestRenderSprintDashboard_NoSprintSelected(t *testing.T) {
	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		selectedSprint: nil,
	}

	result := m.renderSprintDashboard()
	if result != "No sprint selected" {
		t.Errorf("renderSprintDashboard() = %q, want 'No sprint selected'", result)
	}
}

func TestRenderSprintDashboard_BasicSprint(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Sprint 1",
		StartDate: now.AddDate(0, 0, -7),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   []string{"A", "B"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Issue A", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask},
			{ID: "B", Title: "Issue B", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		},
	}

	result := m.renderSprintDashboard()

	// Should contain sprint name
	if !containsStr(result, "Sprint 1") {
		t.Error("Should contain sprint name 'Sprint 1'")
	}

	// Should contain progress elements
	if !containsStr(result, "Progress") {
		t.Error("Should contain 'Progress' label")
	}

	// Should contain status breakdown
	if !containsStr(result, "Status") {
		t.Error("Should contain 'Status' label")
	}
}

func TestRenderSprintDashboard_AllStatusTypes(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Test Sprint",
		StartDate: now.AddDate(0, 0, -14),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   []string{"A", "B", "C", "D"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         50,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Closed Issue", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask},
			{ID: "B", Title: "Open Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
			{ID: "C", Title: "In Progress", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
			{ID: "D", Title: "Blocked Issue", Status: model.StatusBlocked, Priority: 1, IssueType: model.TypeTask},
		},
	}

	result := m.renderSprintDashboard()

	// Should not panic and should produce output
	if result == "" {
		t.Error("Should produce non-empty output")
	}

	// Should contain burndown section
	if !containsStr(result, "Burndown") {
		t.Error("Should contain 'Burndown' label")
	}
}

func TestRenderSprintDashboard_AtRiskItems(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Sprint With Stale",
		StartDate: now.AddDate(0, 0, -14),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   []string{"A", "B"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         50,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Fresh In Progress", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
			{ID: "B", Title: "Stale In Progress", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now.AddDate(0, 0, -5)},
		},
	}

	result := m.renderSprintDashboard()

	// Should contain at-risk section
	if !containsStr(result, "At Risk") {
		t.Error("Should contain 'At Risk' label")
	}

	// Should show the stale item (B was updated 5 days ago, threshold is 3)
	if !containsStr(result, "Stale In Progress") {
		t.Error("Should show stale item in at-risk section")
	}
}

func TestRenderSprintDashboard_NoAtRiskItems(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Sprint All Fresh",
		StartDate: now.AddDate(0, 0, -7),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   []string{"A"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         50,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Fresh Item", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		},
	}

	result := m.renderSprintDashboard()

	// Should show "No at-risk items"
	if !containsStr(result, "No at-risk items") {
		t.Error("Should show 'No at-risk items' message")
	}
}

func TestRenderSprintDashboard_NarrowWidth(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Narrow Sprint",
		StartDate: now.AddDate(0, 0, -7),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   []string{"A"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          30, // Very narrow
		height:         40,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Test Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		},
	}

	// Should not panic with narrow width
	result := m.renderSprintDashboard()
	if result == "" {
		t.Error("Should produce output even with narrow width")
	}
}

func TestRenderSprintDashboard_ZeroDaysRemaining(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Sprint Ending",
		StartDate: now.AddDate(0, 0, -14),
		EndDate:   now, // Ending today
		BeadIDs:   []string{"A"},
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		selectedSprint: &sprint,
		issues: []model.Issue{
			{ID: "A", Title: "Test Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		},
	}

	result := m.renderSprintDashboard()

	// Should show 0 days remaining
	if !containsStr(result, "0 days") {
		t.Error("Should show '0 days' remaining")
	}
}

func TestRenderSprintDashboard_ManyBeads(t *testing.T) {
	now := time.Now().UTC()
	beadIDs := make([]string, 15)
	issues := make([]model.Issue, 15)
	for i := 0; i < 15; i++ {
		beadIDs[i] = string(rune('A' + i))
		issues[i] = model.Issue{
			ID:        beadIDs[i],
			Title:     "Issue " + beadIDs[i],
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
		}
	}

	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Large Sprint",
		StartDate: now.AddDate(0, 0, -7),
		EndDate:   now.AddDate(0, 0, 7),
		BeadIDs:   beadIDs,
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         50,
		selectedSprint: &sprint,
		issues:         issues,
	}

	result := m.renderSprintDashboard()

	// Should show truncated list with "+X more"
	if !containsStr(result, "more") {
		t.Error("Should show '+X more' for large bead lists")
	}
}

func TestRenderSprintDashboard_InsufficientData(t *testing.T) {
	now := time.Now().UTC()
	sprint := model.Sprint{
		ID:      "s1",
		Name:    "Sprint No Dates",
		BeadIDs: []string{},
		// No start/end dates
	}

	m := &Model{
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		selectedSprint: &sprint,
		issues:         []model.Issue{},
	}
	_ = now // unused but kept for consistency

	result := m.renderSprintDashboard()

	// Should show "insufficient data" for burndown
	if !containsStr(result, "insufficient data") {
		t.Error("Should show 'insufficient data' message")
	}
}

func TestHandleSprintKeys_EscExit(t *testing.T) {
	m := &Model{
		isSprintView: true,
		focused:      focusDetail,
		theme:        DefaultTheme(lipgloss.NewRenderer(nil)),
		width:        100,
		height:       40,
	}

	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.isSprintView {
		t.Error("Esc should exit sprint view")
	}
	if m.focused != focusList {
		t.Errorf("focused=%v; want focusList", m.focused)
	}
}

func TestHandleSprintKeys_DownArrow(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
		{ID: "s2", Name: "Sprint 2", StartDate: now.AddDate(0, 0, -1), EndDate: now.AddDate(0, 0, 7), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		issues:         []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}},
		sprints:        sprints,
		selectedSprint: &sprints[0],
	}

	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s2" {
		t.Errorf("after down: selected=%v; want s2", m.selectedSprint)
	}
}

func TestHandleSprintKeys_UpArrow(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
		{ID: "s2", Name: "Sprint 2", StartDate: now.AddDate(0, 0, -1), EndDate: now.AddDate(0, 0, 7), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		issues:         []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}},
		sprints:        sprints,
		selectedSprint: &sprints[1],
	}

	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyUp})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s1" {
		t.Errorf("after up: selected=%v; want s1", m.selectedSprint)
	}
}

func TestHandleSprintKeys_BoundaryAtFirst(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		issues:         []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}},
		sprints:        sprints,
		selectedSprint: &sprints[0],
	}

	// Try to go up when already at first
	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyUp})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s1" {
		t.Errorf("Should stay at s1 when at boundary")
	}
}

func TestHandleSprintKeys_BoundaryAtLast(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		issues:         []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}},
		sprints:        sprints,
		selectedSprint: &sprints[0],
	}

	// Try to go down when already at last
	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedSprint == nil || m.selectedSprint.ID != "s1" {
		t.Errorf("Should stay at s1 when at boundary")
	}
}

func TestHandleSprintKeys_NilSelectedSprint(t *testing.T) {
	now := time.Now().UTC()
	sprints := []model.Sprint{
		{ID: "s1", Name: "Sprint 1", StartDate: now.AddDate(0, 0, -7), EndDate: now.AddDate(0, 0, -1), BeadIDs: []string{"A"}},
	}

	m := &Model{
		isSprintView:   true,
		theme:          DefaultTheme(lipgloss.NewRenderer(nil)),
		width:          100,
		height:         40,
		sprints:        sprints,
		selectedSprint: nil, // No sprint selected
	}

	// Should not panic
	m = m.handleSprintKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedSprint != nil {
		t.Errorf("Should remain nil when no sprint selected")
	}
}

// Helper function
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// asModelPtr unwraps the tea.Model returned by Update so tests can inspect
// internal state.
func asModelPtr(t *testing.T, tm tea.Model) *Model {
	t.Helper()
	m, ok := tm.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", tm)
	}
	return m
}

func TestModel_PKeyOpensActiveSprintAndEscCloses(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now}}
	m := NewModel(issues, nil, "")
	m.sprints = []model.Sprint{
		{ID: "old", Name: "Old Sprint", StartDate: now.AddDate(0, 0, -20), EndDate: now.AddDate(0, 0, -10), BeadIDs: []string{"A"}},
		{ID: "cur", Name: "Current Sprint", StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 5), BeadIDs: []string{"A"}},
	}
	m.focused = focusList

	got := asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})))
	if !got.isSprintView || got.focused != focusSprint {
		t.Fatalf("after P: isSprintView=%v focused=%v; want sprint view focused", got.isSprintView, got.focused)
	}
	if got.selectedSprint == nil || got.selectedSprint.ID != "cur" {
		t.Fatalf("after P: selectedSprint=%+v; want the sprint active today", got.selectedSprint)
	}
	if !strings.Contains(got.sprintViewText, "Current Sprint") {
		t.Fatalf("sprint dashboard text missing sprint name:\n%s", got.sprintViewText)
	}
	if view := got.View(); !strings.Contains(view, "Current Sprint") {
		t.Fatalf("View() should render the sprint dashboard while it is open")
	}

	got = asModelPtr(t, must2(got.Update(tea.KeyMsg{Type: tea.KeyEsc})))
	if got.isSprintView || got.focused != focusList {
		t.Fatalf("after esc: isSprintView=%v focused=%v; want list", got.isSprintView, got.focused)
	}
}

func TestModel_PKeyWithoutSprintsReportsStatus(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.focused = focusList

	got := asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})))
	if got.isSprintView {
		t.Fatalf("P with no sprints must not open the sprint view")
	}
	if got.statusMsg != noSprintsStatus {
		t.Fatalf("statusMsg=%q; want %q", got.statusMsg, noSprintsStatus)
	}
	if got.statusIsError {
		t.Fatalf("missing sprints is informational, not an error")
	}
}

func TestModel_PKeyIgnoredOutsideListAndDetail(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.sprints = []model.Sprint{{ID: "cur", Name: "Current Sprint", StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 5), BeadIDs: []string{"A"}}}
	m.isBoardView = true
	m.focused = focusBoard

	got := asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})))
	if got.isSprintView {
		t.Fatalf("P from the board view must not open the sprint dashboard")
	}
}

// must2 drops the tea.Cmd half of an Update result.
func must2(tm tea.Model, _ tea.Cmd) tea.Model { return tm }

func TestRenderSprintDashboard_RespectsDriftConfigThresholds(t *testing.T) {
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	now := time.Now().UTC()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	// Issue blocked for 3 days (P2): at-risk under default (2d) but safe under sprint_blocked_days: 10.
	issues := []model.Issue{{
		ID:        "SPRINT-BLK-1",
		Title:     "Blocked three days",
		Status:    model.StatusBlocked,
		Priority:  2,
		IssueType: model.TypeTask,
		UpdatedAt: now.Add(-3 * 24 * time.Hour),
	}}
	sprint := model.Sprint{
		ID:        "s1",
		Name:      "Sprint Thresholds",
		StartDate: now.Add(-5 * 24 * time.Hour),
		EndDate:   now.Add(9 * 24 * time.Hour),
		BeadIDs:   []string{"SPRINT-BLK-1"},
	}

	mDefault := NewModel(issues, nil, beadsPath)
	t.Cleanup(mDefault.Stop)
	mDefault.selectedSprint = &sprint
	outDefault := mDefault.renderSprintDashboard()
	if !strings.Contains(outDefault, "blocked") {
		t.Fatalf("expected default sprint dashboard to flag 3-day blocked issue:\n%s", outDefault)
	}

	if err := os.WriteFile(filepath.Join(bvDir, "drift.yaml"), []byte("sprint_blocked_days: 10\nsprint_inactive_days: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mCustom := NewModel(issues, nil, beadsPath)
	t.Cleanup(mCustom.Stop)
	mCustom.selectedSprint = &sprint
	outCustom := mCustom.renderSprintDashboard()
	if !strings.Contains(outCustom, "No at-risk items") {
		t.Fatalf("expected sprint_blocked_days=10 from .bv/drift.yaml to suppress 3-day blocked warning:\n%s", outCustom)
	}
}

func TestSprintView_RealDailyBurndownVelocityAndBeadCursorJump(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	closedDay2 := now.AddDate(0, 0, -4)
	closedDay4 := now.AddDate(0, 0, -2)
	issues := []model.Issue{
		{ID: "SP-1", Title: "Closed Day 2", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, ClosedAt: &closedDay2, UpdatedAt: closedDay2},
		{ID: "SP-2", Title: "Closed Day 4", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, ClosedAt: &closedDay4, UpdatedAt: closedDay4},
		{ID: "SP-3", Title: "Open Target Bead", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "SP-4", Title: "Open Trailing Bead", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now},
	}
	sprints := []model.Sprint{
		{
			ID:        "sprint-real",
			Name:      "Sprint Real Burndown",
			StartDate: now.AddDate(0, 0, -5),
			EndDate:   now.AddDate(0, 0, 5),
			BeadIDs:   []string{"SP-1", "SP-2", "SP-3", "SP-4"},
		},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.referenceNow = now
	m.width = 120
	m.height = 40
	m.sprints = sprints
	m.selectedSprint = &sprints[0]
	m.isSprintView = true
	m.focused = focusSprint

	out := m.renderSprintDashboard()
	if !strings.Contains(out, "Velocity:") || !strings.Contains(out, "ideal") || !strings.Contains(out, "Projected") {
		t.Fatalf("expected Sprint View to show real Velocity/Projection line, got:\n%s", out)
	}
	// Stepped daily burndown must place ● on at least 2 distinct vertical rows (not a single flat horizontal line).
	bulletRows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "●") && !strings.Contains(line, "actual") {
			bulletRows++
		}
	}
	if bulletRows < 2 {
		t.Fatalf("expected stepped multi-row daily burndown curve, got %d bullet rows:\n%s", bulletRows, out)
	}

	// Navigate sprint bead cursor with Tab / n / p through m.Update (verifying live TUI key routing
	// and proving `p` does not fall through to toggle m.showPriorityHints) and Enter jumps to cursor.
	displayed := m.sprintScopedIssues()
	if len(displayed) < 3 || displayed[0].ID == displayed[2].ID {
		t.Fatalf("expected >=3 distinct displayed sprint beads, got %+v", displayed)
	}
	wantTargetID := displayed[2].ID
	hintsBefore := m.showPriorityHints
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyTab})))
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})))
	if m.sprintBeadCursor != 2 {
		t.Fatalf("expected sprintBeadCursor=2 after Update(Tab+n), got %d", m.sprintBeadCursor)
	}
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})))
	if m.sprintBeadCursor != 1 {
		t.Fatalf("expected sprintBeadCursor=1 after Update(p), got %d", m.sprintBeadCursor)
	}
	if m.showPriorityHints != hintsBefore {
		t.Fatalf("pressing p in Sprint View leaked to toggle showPriorityHints (%v -> %v)", hintsBefore, m.showPriorityHints)
	}
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyTab})))
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyEnter})))
	if m.isSprintView {
		t.Fatalf("expected Enter to exit Sprint View and jump to bead")
	}
	sel, ok := m.list.SelectedItem().(IssueItem)
	if !ok || sel.Issue.ID != wantTargetID {
		t.Fatalf("expected Enter with cursor=2 to jump to %s (not index-0 %s), got %+v (ok=%v)", wantTargetID, displayed[0].ID, sel.Issue.ID, ok)
	}
}

func TestSprintView_CompactTerminalKeepsBeadCursorVisibleAndResetsOnSetSprints(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "SP-1", Title: "First Bead", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "SP-2", Title: "Second Bead", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "SP-3", Title: "Third Bead", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now},
	}
	sprints := []model.Sprint{{
		ID:        "sprint-compact",
		Name:      "Compact Terminal Sprint",
		StartDate: now.AddDate(0, 0, -5),
		EndDate:   now.AddDate(0, 0, 5),
		BeadIDs:   []string{"SP-1", "SP-2", "SP-3"},
	}}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.referenceNow = now
	m.width = 80
	m.height = 18 // compact viewport where Beads in Sprint starts below the fold
	m.SetSprints(sprints)
	m.isSprintView = true
	m.focused = focusSprint

	// Pressing Tab must auto-scroll m.sprintScroll so the highlighted ▸ bead line is visible.
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyTab})))
	if m.sprintBeadCursor != 1 {
		t.Fatalf("expected sprintBeadCursor=1, got %d", m.sprintBeadCursor)
	}
	if !strings.Contains(m.sprintViewText, "▸ ") || m.sprintScroll == 0 {
		t.Fatalf("expected Tab on compact 80x18 terminal to scroll ▸ bead into view (scroll=%d):\n%s", m.sprintScroll, m.sprintViewText)
	}

	// SetSprints must reset cursor and scroll.
	m.SetSprints(sprints)
	if m.sprintBeadCursor != 0 || m.sprintScroll != 0 {
		t.Fatalf("expected SetSprints to reset sprintBeadCursor/sprintScroll to 0, got cursor=%d scroll=%d", m.sprintBeadCursor, m.sprintScroll)
	}
}

func TestModel_ETAContextInvalidatesOnPhase2AndRepoScope(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	closed := now.AddDate(0, 0, -3)
	est60 := 60
	est600 := 600
	issues := []model.Issue{
		{ID: "api-1", Title: "API closed", Status: model.StatusClosed, IssueType: model.TypeTask, SourceRepo: "api", EstimatedMinutes: &est60, ClosedAt: &closed, UpdatedAt: closed},
		{ID: "api-2", Title: "API open", Status: model.StatusOpen, IssueType: model.TypeTask, SourceRepo: "api", EstimatedMinutes: &est60, UpdatedAt: now},
		{ID: "web-1", Title: "Web closed", Status: model.StatusClosed, IssueType: model.TypeTask, SourceRepo: "web", EstimatedMinutes: &est600, ClosedAt: &closed, UpdatedAt: closed},
		{ID: "web-2", Title: "Web open", Status: model.StatusOpen, IssueType: model.TypeTask, SourceRepo: "web", EstimatedMinutes: &est60, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.referenceNow = now
	m.workspaceMode = true

	ctxAll := m.etaContext()
	if ctxAll == nil {
		t.Fatal("expected non-nil ETAContext")
	}
	etaAll := ctxAll.EstimateForIssue(issues[1], 1)

	// Narrow workspace activeRepos to "api" only: velocity must exclude web-1's 600m closure!
	m.activeRepos = map[string]bool{"api": true}
	ctxAPI := m.etaContext()
	if ctxAPI == nil || ctxAPI == ctxAll {
		t.Fatalf("expected ETAContext cache miss when activeRepos changes")
	}
	etaAPI := ctxAPI.EstimateForIssue(issues[1], 1)
	if etaAPI.EstimatedDays <= etaAll.EstimatedDays {
		t.Fatalf("expected api-only scope (60m/30d) to estimate longer than combined scope (660m/30d): api=%.2fd all=%.2fd",
			etaAPI.EstimatedDays, etaAll.EstimatedDays)
	}

	// Ensure Phase 2 completion invalidates cached ETAContext even when stats pointer is unchanged.
	m.activeRepos = nil
	m.analysis.WaitForPhase2()
	m.etaCtx = ctxAll
	m.etaCtxStats = m.analysis
	m.etaCtxPhase2Ready = false
	ctxPhase2 := m.etaContext()
	if ctxPhase2 == ctxAll {
		t.Fatalf("expected ETAContext cache miss when Phase2Ready flips false->true on same stats pointer")
	}
}

func TestAlertsPanel_TabSwitchesToHygieneSuggestionsAndSupportsJumpAndCopy(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "HYG-1", Title: "Fix OAuth login callback timeout failure in production", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug, CreatedAt: now, UpdatedAt: now},
		{ID: "HYG-2", Title: "Fix OAuth login callback timeout failure in production", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug, CreatedAt: now, UpdatedAt: now},
		{ID: "HYG-3", Title: "Unrelated observability metric cleanup", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.referenceNow = now
	m.width = 120
	m.height = 36
	m.alerts = []drift.Alert{
		{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "HYG-3", Message: "Sample alert"},
	}

	// Open with '!' -> starts on Alerts tab (0).
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})))
	if !m.showAlertsPanel || m.alertsTab != 0 {
		t.Fatalf("expected '!' with active alert to open Alerts tab 0, got open=%v tab=%d", m.showAlertsPanel, m.alertsTab)
	}
	// Press Tab -> switches to Hygiene Suggestions tab (1).
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyTab})))
	if m.alertsTab != 1 {
		t.Fatalf("expected Tab to switch alerts panel to Suggestions tab 1, got %d", m.alertsTab)
	}
	sugs := m.activeScopedSuggestions()
	if len(sugs) == 0 {
		t.Fatal("expected duplicate-title fixture to produce >=1 hygiene suggestion")
	}
	rendered := m.renderAlertsPanel()
	if !strings.Contains(rendered, "Suggestions") || !strings.Contains(rendered, sugs[0].TargetBead) {
		t.Fatalf("expected Suggestions tab rendering to contain header and target bead %s:\n%s", sugs[0].TargetBead, rendered)
	}
	// Press 'c' -> attempts copy of ActionCommand (populates statusMsg when ActionCommand is present).
	if sugs[0].ActionCommand != "" {
		m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})))
		if m.statusMsg == "" {
			t.Fatal("expected pressing 'c' on a suggestion with ActionCommand to set statusMsg")
		}
	}
	// Press Enter -> closes panel and jumps to TargetBead.
	targetID := sugs[0].TargetBead
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyEnter})))
	if m.showAlertsPanel {
		t.Fatal("expected Enter on Suggestions tab to close overlay")
	}
	sel, ok := m.list.SelectedItem().(IssueItem)
	if !ok || sel.Issue.ID != targetID {
		t.Fatalf("expected Enter to jump to %s, got %+v (ok=%v)", targetID, sel.Issue.ID, ok)
	}

	// When 0 alerts remain but suggestions exist, '!' opens directly on Suggestions tab (1).
	m.alerts = nil
	m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})))
	if !m.showAlertsPanel || m.alertsTab != 1 {
		t.Fatalf("expected '!' with 0 alerts and >0 suggestions to open directly on tab 1, got open=%v tab=%d", m.showAlertsPanel, m.alertsTab)
	}
}

func TestSprintView_GrahamCapacityAndCrossSurfaceMetadataMetrics(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	est120 := 120
	est240 := 240
	due := now.AddDate(0, 0, 3)
	closedAt := now.AddDate(0, 0, -1)
	issues := []model.Issue{
		{
			ID:               "META-1",
			Title:            "Root Platform Migration",
			Description:      "Primary description line",
			Status:           model.StatusOpen,
			Priority:         1,
			IssueType:        model.TypeFeature,
			SourceRepo:       "core",
			EstimatedMinutes: &est120,
			CreatedBy:        "architect",
			DueDate:          &due,
			CreatedAt:        now.AddDate(0, 0, -5),
			UpdatedAt:        now,
		},
		{
			ID:               "META-2",
			Title:            "Bridge Service Rollout",
			Status:           model.StatusOpen,
			Priority:         1,
			IssueType:        model.TypeTask,
			SourceRepo:       "core",
			EstimatedMinutes: &est240,
			Dependencies:     []*model.Dependency{{IssueID: "META-2", DependsOnID: "META-1", Type: model.DepBlocks}},
			CreatedAt:        now.AddDate(0, 0, -4),
			UpdatedAt:        now,
		},
		{
			ID:           "META-3",
			Title:        "Downstream Consumer",
			Status:       model.StatusOpen,
			Priority:     2,
			IssueType:    model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "META-3", DependsOnID: "META-2", Type: model.DepBlocks}},
			CreatedAt:    now.AddDate(0, 0, -3),
			UpdatedAt:    now,
		},
		{
			ID:          "META-CLOSED",
			Title:       "Retired Legacy Worker",
			Status:      model.StatusClosed,
			Priority:    3,
			IssueType:   model.TypeChore,
			SourceRepo:  "legacy",
			CreatedBy:   "ops",
			ClosedAt:    &closedAt,
			CloseReason: "Replaced by META-1",
			CreatedAt:   now.AddDate(0, 0, -10),
			UpdatedAt:   closedAt,
		},
	}
	sprints := []model.Sprint{{
		ID:        "sp-cap",
		Name:      "Capacity Sprint",
		StartDate: now.AddDate(0, 0, -4),
		EndDate:   now.AddDate(0, 0, 10),
		BeadIDs:   []string{"META-1", "META-2", "META-3"},
	}}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.referenceNow = now
	m.width = 120
	m.height = 44
	m.analysis.WaitForPhase2()
	m.SetSprints(sprints)
	m.isSprintView = true

	// 1. Sprint View renders Graham Capacity Bounds line.
	sprintOut := m.renderSprintDashboard()
	if !strings.Contains(sprintOut, "Capacity:") || !strings.Contains(sprintOut, "1a:") || !strings.Contains(sprintOut, "2a:") || !strings.Contains(sprintOut, "4a:") {
		t.Fatalf("expected SprintView to render Graham Capacity Bounds line, got:\n%s", sprintOut)
	}

	// 2. DetailPane renders Estimate, Created By, Repo, and Structural Metrics.
	m.isSprintView = false
	m.viewport.Width = 100
	m.viewport.Height = 120
	m.selectListIssueByID("META-2")
	m.updateViewportContent()
	detailOut := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Repo:", "core", "Estimate:", "4h", "Structural Metrics", "K-Core: 1", "Slack: 0.0", "Articulation Point"} {
		if !strings.Contains(detailOut, want) {
			t.Errorf("DetailPane for META-2 missing %q:\n%s", want, detailOut)
		}
	}
	m.selectListIssueByID("META-CLOSED")
	m.updateViewportContent()
	closedDetail := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Created By:", "ops", "Close Reason:", "Replaced by META-1", "Repo:", "legacy"} {
		if !strings.Contains(closedDetail, want) {
			t.Errorf("DetailPane for META-CLOSED missing %q:\n%s", want, closedDetail)
		}
	}

	// 3. InsightsModel buildDetailMarkdown includes metadata and structural metrics.
	ins := NewInsightsModel(m.analysis.GenerateInsights(10), m.issueMap, m.theme)
	insMD := ins.buildDetailMarkdown("META-1")
	for _, want := range []string{"**Repo:**", "core", "**Estimate:**", "2h", "**Created By:**", "architect", "**Structure:**", "K-Core", "Slack"} {
		if !strings.Contains(insMD, want) {
			t.Errorf("Insights detail markdown for META-1 missing %q:\n%s", want, insMD)
		}
	}

	// 4. BoardModel syncDetailContent includes Estimate, Created By, Close Reason, Repo.
	bm := NewBoardModel(issues, m.theme)
	bm.showDetail = true
	bm.JumpToColumn(ColClosed)
	bm.detailVP.Width = 60
	bm.detailVP.Height = 30
	bm.syncDetailContent()
	boardDetail := ansi.Strip(bm.detailVP.View())
	for _, want := range []string{"legacy", "ops", "Replaced by META-1"} {
		if !strings.Contains(boardDetail, want) {
			t.Errorf("Board detail pane for META-CLOSED missing %q:\n%s", want, boardDetail)
		}
	}
}
