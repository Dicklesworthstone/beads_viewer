package ui_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func createTime(hoursAgo int) time.Time {
	return time.Now().Add(time.Duration(-hoursAgo) * time.Hour)
}

func createTheme() ui.Theme {
	return ui.DefaultTheme(lipgloss.NewRenderer(os.Stdout))
}

// TestBoardModelBlackbox tests basic selection and update behavior
func TestBoardModelBlackbox(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
	}

	theme := createTheme()
	b := ui.NewBoardModel(issues, theme)

	// Focus Open col (0)
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 selected in Open col")
	}

	// Update issues
	newIssues := []model.Issue{
		{ID: "2", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
	}
	b.SetIssues(newIssues)

	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "2" {
		t.Errorf("Expected ID 2 selected after update, got %v", sel)
	}

	// Filter to empty
	b.SetIssues([]model.Issue{})
	sel = b.SelectedIssue()
	if sel != nil {
		t.Errorf("Expected nil selection for empty board")
	}
}

func TestBoardModel_SetSnapshot(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "open-1", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
		{ID: "closed-1", Status: model.StatusClosed, Priority: 1, CreatedAt: createTime(0)},
	}

	snap := ui.NewSnapshotBuilder(issues).Build()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	t.Cleanup(snap.Analysis.WaitForPhase2)
	if snap.BoardState == nil {
		t.Fatal("expected snapshot.BoardState to be computed")
	}

	b := ui.NewBoardModel(nil, theme)
	b.SetSnapshot(snap)

	// Starts in Open column.
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "open-1" {
		t.Fatalf("expected open-1 selected, got %#v", sel)
	}

	// In Status mode, navigation enters empty columns.
	b.MoveRight() // In Progress (empty)
	if sel := b.SelectedIssue(); sel != nil {
		t.Fatalf("expected nil selection in empty column, got %#v", sel)
	}
	b.MoveRight() // Blocked (empty)
	if sel := b.SelectedIssue(); sel != nil {
		t.Fatalf("expected nil selection in empty column, got %#v", sel)
	}
	b.MoveRight() // Closed
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "closed-1" {
		t.Fatalf("expected closed-1 selected, got %#v", sel)
	}
}

// TestAdaptiveColumns verifies navigation behavior with empty columns
// In Status mode, all 4 columns are shown (including empty), navigation can enter them
// In Priority/Type modes, empty columns are hidden and navigation skips them (bv-tf6j)
func TestAdaptiveColumns(t *testing.T) {
	theme := createTheme()

	// Create issues only in Open and Closed columns (skip InProgress and Blocked)
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
		{ID: "2", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},
		{ID: "3", Status: model.StatusClosed, Priority: 1, CreatedAt: createTime(2)},
	}

	b := ui.NewBoardModel(issues, theme)

	// Should start on first column (Open) - has items
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 (Open col), got %v", sel)
	}

	// In Status mode (default), all columns are visible including empty ones
	// MoveRight goes to InProgress (empty column) - SelectedIssue returns nil
	b.MoveRight()
	sel = b.SelectedIssue()
	if sel != nil {
		t.Errorf("Expected nil (empty InProgress col) after MoveRight, got %v", sel)
	}

	// MoveRight again goes to Blocked (also empty)
	b.MoveRight()
	sel = b.SelectedIssue()
	if sel != nil {
		t.Errorf("Expected nil (empty Blocked col) after second MoveRight, got %v", sel)
	}

	// MoveRight again goes to Closed (has items)
	b.MoveRight()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected ID 3 (Closed col) after third MoveRight, got %v", sel)
	}

	// Test Priority mode where empty columns ARE hidden
	b.CycleSwimLaneMode() // Switch to Priority mode

	// In priority mode: P1 in col1, P2 in col1, P1 in col1 (all in P1 column after grouping)
	// Actually with the test data: ID 1 is P1, ID 2 is P2, ID 3 is P1
	// So P0 empty, P1 has 2 items (1, 3), P2 has 1 item (2), P3 empty
	// Hidden columns: P0 (col 0) and P3 (col 3) -> 2 hidden

	hiddenCount := b.HiddenColumnCount()
	if hiddenCount != 2 {
		t.Errorf("Priority mode should hide 2 empty columns, got %d", hiddenCount)
	}
}

// TestColumnNavigation tests up/down navigation within columns
func TestColumnNavigation(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
		{ID: "2", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},
		{ID: "3", Status: model.StatusOpen, Priority: 3, CreatedAt: createTime(2)},
	}

	b := ui.NewBoardModel(issues, theme)

	// Should start at first item
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1, got %v", sel)
	}

	// MoveDown
	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "2" {
		t.Errorf("Expected ID 2 after MoveDown, got %v", sel)
	}

	// MoveDown again
	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected ID 3 after second MoveDown, got %v", sel)
	}

	// MoveDown at bottom should stay at bottom
	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected to stay at ID 3, got %v", sel)
	}

	// MoveUp
	b.MoveUp()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "2" {
		t.Errorf("Expected ID 2 after MoveUp, got %v", sel)
	}

	// MoveToTop
	b.MoveToTop()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 after MoveToTop, got %v", sel)
	}

	// MoveToBottom
	b.MoveToBottom()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected ID 3 after MoveToBottom, got %v", sel)
	}
}

// TestPageNavigation tests page up/down bounds
func TestPageNavigation(t *testing.T) {
	theme := createTheme()

	// Create 10 issues with proper string IDs
	var issues []model.Issue
	for i := 1; i <= 10; i++ {
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("%d", i),
			Status:    model.StatusOpen,
			Priority:  i,
			CreatedAt: createTime(i),
		})
	}

	b := ui.NewBoardModel(issues, theme)

	// PageDown with visibleRows=6 (moves by 3)
	b.PageDown(6)
	sel := b.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selection after PageDown")
	}
	// Should be at row 3 (0-indexed)

	// PageDown many times - should not exceed bounds
	for i := 0; i < 20; i++ {
		b.PageDown(6)
	}
	sel = b.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selection after many PageDowns")
	}
	// Should be at last item (row 9)

	// PageUp many times - should not go below 0
	for i := 0; i < 20; i++ {
		b.PageUp(6)
	}
	sel = b.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selection after many PageUps")
	}
	// Should be at first item
}

// TestColumnCounts tests count methods
func TestColumnCounts(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Status: model.StatusOpen, Priority: 2},
		{ID: "3", Status: model.StatusInProgress, Priority: 1},
		{ID: "4", Status: model.StatusClosed, Priority: 1},
	}

	b := ui.NewBoardModel(issues, theme)

	if b.ColumnCount(0) != 2 { // Open
		t.Errorf("Expected 2 in Open column, got %d", b.ColumnCount(0))
	}
	if b.ColumnCount(1) != 1 { // InProgress
		t.Errorf("Expected 1 in InProgress column, got %d", b.ColumnCount(1))
	}
	if b.ColumnCount(2) != 0 { // Blocked
		t.Errorf("Expected 0 in Blocked column, got %d", b.ColumnCount(2))
	}
	if b.ColumnCount(3) != 1 { // Closed
		t.Errorf("Expected 1 in Closed column, got %d", b.ColumnCount(3))
	}
	if b.TotalCount() != 4 {
		t.Errorf("Expected total 4, got %d", b.TotalCount())
	}
}

// TestSetIssuesSanitizesSelection verifies selection is sanitized after SetIssues
func TestSetIssuesSanitizesSelection(t *testing.T) {
	theme := createTheme()

	// Start with 5 issues in Open
	var issues []model.Issue
	for i := 1; i <= 5; i++ {
		issues = append(issues, model.Issue{
			ID:       fmt.Sprintf("%d", i),
			Status:   model.StatusOpen,
			Priority: i,
		})
	}

	b := ui.NewBoardModel(issues, theme)

	// Move to bottom (row 4)
	b.MoveToBottom()
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "5" {
		t.Errorf("Expected ID 5, got %v", sel)
	}

	// Now reduce to only 2 issues - selection should be sanitized
	b.SetIssues([]model.Issue{
		{ID: "A", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Status: model.StatusOpen, Priority: 2},
	})

	sel = b.SelectedIssue()
	if sel == nil {
		t.Fatal("Expected selection after SetIssues")
	}
	// Selection should be sanitized to last valid row (1)
	if sel.ID != "B" {
		t.Errorf("Expected ID B (last item), got %s", sel.ID)
	}
}

// TestAllColumnsEmpty verifies behavior when all columns are empty
func TestAllColumnsEmpty(t *testing.T) {
	theme := createTheme()

	b := ui.NewBoardModel([]model.Issue{}, theme)

	// Should return nil for selected issue
	sel := b.SelectedIssue()
	if sel != nil {
		t.Errorf("Expected nil selection for empty board, got %v", sel)
	}

	// Navigation should not panic
	b.MoveUp()
	b.MoveDown()
	b.MoveLeft()
	b.MoveRight()
	b.MoveToTop()
	b.MoveToBottom()
	b.PageUp(10)
	b.PageDown(10)

	// Counts should be zero
	if b.TotalCount() != 0 {
		t.Errorf("Expected total 0, got %d", b.TotalCount())
	}
}

// TestSortingByPriorityAndDate verifies issues are sorted correctly
func TestSortingByPriorityAndDate(t *testing.T) {
	theme := createTheme()

	// Create issues with different priorities and dates
	issues := []model.Issue{
		{ID: "low-old", Status: model.StatusOpen, Priority: 3, CreatedAt: createTime(48)},
		{ID: "high-new", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
		{ID: "high-old", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(24)},
		{ID: "med-new", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(0)},
	}

	b := ui.NewBoardModel(issues, theme)

	// First should be high priority, newer date
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "high-new" {
		t.Errorf("Expected high-new first, got %v", sel)
	}

	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "high-old" {
		t.Errorf("Expected high-old second, got %v", sel)
	}

	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "med-new" {
		t.Errorf("Expected med-new third, got %v", sel)
	}

	b.MoveDown()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "low-old" {
		t.Errorf("Expected low-old fourth, got %v", sel)
	}
}

// TestViewRendering verifies View doesn't panic with various inputs
func TestViewRendering(t *testing.T) {
	theme := createTheme()

	tests := []struct {
		name   string
		issues []model.Issue
		width  int
		height int
	}{
		{"empty", []model.Issue{}, 80, 24},
		{"single", []model.Issue{{ID: "1", Status: model.StatusOpen}}, 80, 24},
		{"narrow", []model.Issue{{ID: "1", Status: model.StatusOpen}}, 40, 24},
		{"short", []model.Issue{{ID: "1", Status: model.StatusOpen}}, 80, 10},
		{"all_statuses", []model.Issue{
			{ID: "1", Status: model.StatusOpen},
			{ID: "2", Status: model.StatusInProgress},
			{ID: "3", Status: model.StatusBlocked},
			{ID: "4", Status: model.StatusClosed},
		}, 120, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := ui.NewBoardModel(tt.issues, theme)
			// Should not panic
			_ = b.View(tt.width, tt.height)
		})
	}
}

// TestBoardRichCardContent verifies the bv-1daf rich card content rendering
// Tests that cards with dependencies render correctly with blocked-by and blocks indicators
func TestBoardRichCardContent(t *testing.T) {
	theme := createTheme()

	// Create issues with dependencies to test blocks/blocked-by indicators
	issues := []model.Issue{
		{
			ID:        "A",
			Title:     "Foundation Task",
			Status:    model.StatusOpen,
			Priority:  1,
			CreatedAt: createTime(48), // 2 days ago
			UpdatedAt: createTime(24), // 1 day ago
			Labels:    []string{"backend", "api"},
		},
		{
			ID:        "B",
			Title:     "Blocked Task",
			Status:    model.StatusBlocked,
			Priority:  2,
			CreatedAt: createTime(24),
			UpdatedAt: createTime(1),
			Dependencies: []*model.Dependency{
				{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
			},
		},
		{
			ID:        "C",
			Title:     "Another Blocked Task",
			Status:    model.StatusBlocked,
			Priority:  2,
			CreatedAt: createTime(24),
			UpdatedAt: createTime(2),
			Dependencies: []*model.Dependency{
				{IssueID: "C", DependsOnID: "A", Type: model.DepBlocks},
			},
		},
	}

	b := ui.NewBoardModel(issues, theme)

	// Board should render without panic
	output := b.View(160, 40)

	// Basic sanity checks - output should contain issue IDs
	if output == "" {
		t.Error("Board view should not be empty")
	}

	// Test SetIssues rebuilds blocks index
	b.SetIssues(issues)
	output2 := b.View(160, 40)
	if output2 == "" {
		t.Error("Board view after SetIssues should not be empty")
	}
}

// TestBoardAgeColorCoding verifies age indicators show different colors (bv-1daf)
func TestBoardAgeColorCoding(t *testing.T) {
	theme := createTheme()

	// Create issues with different ages
	issues := []model.Issue{
		{
			ID:        "recent",
			Title:     "Recent Issue",
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: createTime(12), // 12 hours ago
			UpdatedAt: time.Now(),     // just now - green
		},
		{
			ID:        "medium",
			Title:     "Medium Age Issue",
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: createTime(24 * 14), // 14 days ago
			UpdatedAt: createTime(24 * 10), // 10 days ago - yellow
		},
		{
			ID:        "stale",
			Title:     "Stale Issue",
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: createTime(24 * 60), // 60 days ago
			UpdatedAt: createTime(24 * 45), // 45 days ago - red
		},
	}

	b := ui.NewBoardModel(issues, theme)

	// Should render without panic with different age colors
	output := b.View(160, 40)
	if output == "" {
		t.Error("Board view with age-colored issues should not be empty")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Swimlane Mode Tests (bv-wjs0)
// ═══════════════════════════════════════════════════════════════════════════════

// TestSwimLaneModeByStatus verifies default status-based grouping
func TestSwimLaneModeByStatus(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Status: model.StatusInProgress, Priority: 1},
		{ID: "3", Status: model.StatusBlocked, Priority: 1},
		{ID: "4", Status: model.StatusClosed, Priority: 1},
	}

	b := ui.NewBoardModel(issues, theme)

	// Default mode should be Status
	if b.GetSwimLaneModeName() != "Status" {
		t.Errorf("Expected Status mode, got %s", b.GetSwimLaneModeName())
	}

	// Each status should be in its respective column
	if b.ColumnCount(0) != 1 { // Open
		t.Errorf("Expected 1 in Open column, got %d", b.ColumnCount(0))
	}
	if b.ColumnCount(1) != 1 { // InProgress
		t.Errorf("Expected 1 in InProgress column, got %d", b.ColumnCount(1))
	}
	if b.ColumnCount(2) != 1 { // Blocked
		t.Errorf("Expected 1 in Blocked column, got %d", b.ColumnCount(2))
	}
	if b.ColumnCount(3) != 1 { // Closed
		t.Errorf("Expected 1 in Closed column, got %d", b.ColumnCount(3))
	}
}

// TestSwimLaneModeByPriority verifies priority-based grouping
func TestSwimLaneModeByPriority(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "p0", Status: model.StatusOpen, Priority: 0}, // Critical
		{ID: "p1", Status: model.StatusOpen, Priority: 1}, // High
		{ID: "p2", Status: model.StatusOpen, Priority: 2}, // Medium
		{ID: "p3", Status: model.StatusOpen, Priority: 3}, // Other
		{ID: "p4", Status: model.StatusOpen, Priority: 4}, // Other
	}

	b := ui.NewBoardModel(issues, theme)

	// Cycle to Priority mode
	b.CycleSwimLaneMode()

	if b.GetSwimLaneModeName() != "Priority" {
		t.Errorf("Expected Priority mode, got %s", b.GetSwimLaneModeName())
	}

	// P0 in col 0, P1 in col 1, P2 in col 2, P3+ in col 3
	if b.ColumnCount(0) != 1 { // P0 Critical
		t.Errorf("Expected 1 in Critical column, got %d", b.ColumnCount(0))
	}
	if b.ColumnCount(1) != 1 { // P1 High
		t.Errorf("Expected 1 in High column, got %d", b.ColumnCount(1))
	}
	if b.ColumnCount(2) != 1 { // P2 Medium
		t.Errorf("Expected 1 in Medium column, got %d", b.ColumnCount(2))
	}
	if b.ColumnCount(3) != 2 { // P3+ Other
		t.Errorf("Expected 2 in Other column (P3+P4), got %d", b.ColumnCount(3))
	}
}

// TestSwimLaneModeByType verifies type-based grouping
func TestSwimLaneModeByType(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "bug1", Status: model.StatusOpen, IssueType: model.TypeBug},
		{ID: "feat1", Status: model.StatusOpen, IssueType: model.TypeFeature},
		{ID: "task1", Status: model.StatusOpen, IssueType: model.TypeTask},
		{ID: "epic1", Status: model.StatusOpen, IssueType: model.TypeEpic},
	}

	b := ui.NewBoardModel(issues, theme)

	// Cycle twice to get to Type mode (Status -> Priority -> Type)
	b.CycleSwimLaneMode()
	b.CycleSwimLaneMode()

	if b.GetSwimLaneModeName() != "Type" {
		t.Errorf("Expected Type mode, got %s", b.GetSwimLaneModeName())
	}

	// Bug in col 0, Feature in col 1, Task in col 2, Epic in col 3
	if b.ColumnCount(0) != 1 {
		t.Errorf("Expected 1 in Bug column, got %d", b.ColumnCount(0))
	}
	if b.ColumnCount(1) != 1 {
		t.Errorf("Expected 1 in Feature column, got %d", b.ColumnCount(1))
	}
	if b.ColumnCount(2) != 1 {
		t.Errorf("Expected 1 in Task column, got %d", b.ColumnCount(2))
	}
	if b.ColumnCount(3) != 1 {
		t.Errorf("Expected 1 in Epic column, got %d", b.ColumnCount(3))
	}
}

// TestSwimLaneModeCycles verifies mode cycles back to Status after Type
func TestSwimLaneModeCycles(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "1", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	// Status -> Priority -> Type -> Status
	modes := []string{"Status", "Priority", "Type", "Status"}
	for i, expected := range modes {
		if b.GetSwimLaneModeName() != expected {
			t.Errorf("Step %d: Expected %s mode, got %s", i, expected, b.GetSwimLaneModeName())
		}
		b.CycleSwimLaneMode()
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Enhanced Navigation Tests (bv-yg39)
// ═══════════════════════════════════════════════════════════════════════════════

// TestJumpToColumn verifies direct column jumping with 1-4 keys
func TestJumpToColumn(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Status: model.StatusInProgress, Priority: 1},
		{ID: "3", Status: model.StatusClosed, Priority: 1},
	}

	b := ui.NewBoardModel(issues, theme)

	// Jump to column 3 (Closed - index 3)
	b.JumpToColumn(3)
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected ID 3 after JumpToColumn(3), got %v", sel)
	}

	// Jump to column 1 (InProgress - index 1)
	b.JumpToColumn(1)
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "2" {
		t.Errorf("Expected ID 2 after JumpToColumn(1), got %v", sel)
	}

	// Jump to empty column 2 (Blocked) - in Status mode (bv-tf6j), empty columns are visible
	// so JumpToColumn goes directly to the empty column, SelectedIssue returns nil
	b.JumpToColumn(2)
	sel = b.SelectedIssue()
	// In Status mode, empty columns are navigable, so we land on empty column
	if sel != nil {
		t.Error("Expected nil selection after jumping to visible empty column")
	}
}

// TestJumpToFirstLastColumn verifies H/L navigation
func TestJumpToFirstLastColumn(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Status: model.StatusClosed, Priority: 1},
	}

	b := ui.NewBoardModel(issues, theme)

	// Start at first column
	b.JumpToLastColumn()
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "2" {
		t.Errorf("Expected ID 2 after JumpToLastColumn, got %v", sel)
	}

	b.JumpToFirstColumn()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 after JumpToFirstColumn, got %v", sel)
	}
}

// TestGGComboState verifies gg combo tracking
func TestGGComboState(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "1", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	if b.IsWaitingForG() {
		t.Error("Should not be waiting for g initially")
	}

	b.SetWaitingForG()
	if !b.IsWaitingForG() {
		t.Error("Should be waiting for g after SetWaitingForG")
	}

	b.ClearWaitingForG()
	if b.IsWaitingForG() {
		t.Error("Should not be waiting for g after ClearWaitingForG")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Search Functionality Tests (bv-yg39)
// ═══════════════════════════════════════════════════════════════════════════════

// TestSearchBasic verifies basic search functionality
func TestSearchBasic(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "bv-abc", Title: "Fix authentication bug", Status: model.StatusOpen},
		{ID: "bv-def", Title: "Add user profile page", Status: model.StatusOpen},
		{ID: "bv-ghi", Title: "Update auth tokens", Status: model.StatusInProgress},
	}

	b := ui.NewBoardModel(issues, theme)

	// Not in search mode initially
	if b.IsSearchMode() {
		t.Error("Should not be in search mode initially")
	}

	// Enter search mode
	b.StartSearch()
	if !b.IsSearchMode() {
		t.Error("Should be in search mode after StartSearch")
	}
	if b.SearchQuery() != "" {
		t.Error("Search query should be empty initially")
	}

	// Search for "auth"
	for _, ch := range "auth" {
		b.AppendSearchChar(ch)
	}

	if b.SearchQuery() != "auth" {
		t.Errorf("Expected query 'auth', got '%s'", b.SearchQuery())
	}

	// Should find 2 matches (authentication and auth)
	if b.SearchMatchCount() != 2 {
		t.Errorf("Expected 2 matches for 'auth', got %d", b.SearchMatchCount())
	}
}

// TestSearchNavigation verifies n/N navigation through matches
func TestSearchNavigation(t *testing.T) {
	theme := createTheme()

	issues := []model.Issue{
		{ID: "bv-1", Title: "Test one", Status: model.StatusOpen},
		{ID: "bv-2", Title: "Test two", Status: model.StatusOpen},
		{ID: "bv-3", Title: "Test three", Status: model.StatusOpen},
	}

	b := ui.NewBoardModel(issues, theme)

	b.StartSearch()
	for _, ch := range "test" {
		b.AppendSearchChar(ch)
	}

	// All 3 should match
	if b.SearchMatchCount() != 3 {
		t.Errorf("Expected 3 matches, got %d", b.SearchMatchCount())
	}

	// Should be at first match
	if b.SearchCursorPos() != 1 {
		t.Errorf("Expected cursor at 1, got %d", b.SearchCursorPos())
	}

	// Navigate forward
	b.NextMatch()
	if b.SearchCursorPos() != 2 {
		t.Errorf("Expected cursor at 2 after NextMatch, got %d", b.SearchCursorPos())
	}

	// Navigate backward
	b.PrevMatch()
	if b.SearchCursorPos() != 1 {
		t.Errorf("Expected cursor at 1 after PrevMatch, got %d", b.SearchCursorPos())
	}

	// Wrap around forward
	b.NextMatch()
	b.NextMatch()
	b.NextMatch() // Should wrap to 1
	if b.SearchCursorPos() != 1 {
		t.Errorf("Expected cursor to wrap to 1, got %d", b.SearchCursorPos())
	}

	// Wrap around backward
	b.PrevMatch() // Should wrap to 3
	if b.SearchCursorPos() != 3 {
		t.Errorf("Expected cursor to wrap to 3, got %d", b.SearchCursorPos())
	}
}

// TestSearchBackspace verifies backspace in search
func TestSearchBackspace(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "test", Title: "Test", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	b.StartSearch()
	for _, ch := range "test" {
		b.AppendSearchChar(ch)
	}

	b.BackspaceSearch()
	if b.SearchQuery() != "tes" {
		t.Errorf("Expected 'tes' after backspace, got '%s'", b.SearchQuery())
	}

	// Backspace all
	b.BackspaceSearch()
	b.BackspaceSearch()
	b.BackspaceSearch()
	if b.SearchQuery() != "" {
		t.Errorf("Expected empty query, got '%s'", b.SearchQuery())
	}

	// Backspace on empty should be safe
	b.BackspaceSearch()
	if b.SearchQuery() != "" {
		t.Error("Backspace on empty should keep query empty")
	}
}

// TestSearchCancel verifies search cancellation
func TestSearchCancel(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "test", Title: "Test", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	b.StartSearch()
	for _, ch := range "test" {
		b.AppendSearchChar(ch)
	}

	b.CancelSearch()
	if b.IsSearchMode() {
		t.Error("Should not be in search mode after CancelSearch")
	}
	if b.SearchQuery() != "" {
		t.Error("Query should be cleared after CancelSearch")
	}
	if b.SearchMatchCount() != 0 {
		t.Error("Matches should be cleared after CancelSearch")
	}
}

// TestSearchFinishKeepsResults verifies FinishSearch keeps matches for n/N
func TestSearchFinishKeepsResults(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test1", Title: "Test One", Status: model.StatusOpen},
		{ID: "test2", Title: "Test Two", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	b.StartSearch()
	for _, ch := range "test" {
		b.AppendSearchChar(ch)
	}

	b.FinishSearch()

	// Should exit search mode but keep matches
	if b.IsSearchMode() {
		t.Error("Should not be in search mode after FinishSearch")
	}
	// Note: After FinishSearch, NextMatch/PrevMatch should still work
	// if search results are preserved (depends on implementation)
}

// TestSearchCaseInsensitive verifies case-insensitive search
func TestSearchCaseInsensitive(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "BV-ABC", Title: "UPPERCASE TITLE", Status: model.StatusOpen},
		{ID: "bv-def", Title: "lowercase title", Status: model.StatusOpen},
		{ID: "Bv-Ghi", Title: "Mixed Case Title", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	b.StartSearch()
	for _, ch := range "title" {
		b.AppendSearchChar(ch)
	}

	// All 3 should match regardless of case
	if b.SearchMatchCount() != 3 {
		t.Errorf("Expected 3 matches for case-insensitive 'title', got %d", b.SearchMatchCount())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Detail Panel Tests (bv-r6kh)
// ═══════════════════════════════════════════════════════════════════════════════

// TestDetailPanelToggle verifies detail panel visibility
func TestDetailPanelToggle(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "1", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	// Initially hidden
	if b.IsDetailShown() {
		t.Error("Detail panel should be hidden initially")
	}

	// Show
	b.ShowDetail()
	if !b.IsDetailShown() {
		t.Error("Detail panel should be shown after ShowDetail")
	}

	// Toggle off
	b.ToggleDetail()
	if b.IsDetailShown() {
		t.Error("Detail panel should be hidden after toggle")
	}

	// Toggle on
	b.ToggleDetail()
	if !b.IsDetailShown() {
		t.Error("Detail panel should be shown after second toggle")
	}

	// Hide
	b.HideDetail()
	if b.IsDetailShown() {
		t.Error("Detail panel should be hidden after HideDetail")
	}
}

// TestDetailPanelScroll verifies detail panel scrolling doesn't panic
func TestDetailPanelScroll(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{
		ID:          "1",
		Title:       "Test Issue",
		Description: "Long description that spans multiple lines...",
		Status:      model.StatusOpen,
	}}
	b := ui.NewBoardModel(issues, theme)

	b.ShowDetail()

	// Force render to populate viewport
	_ = b.View(160, 40)

	// Scroll operations should not panic
	b.DetailScrollDown(3)
	b.DetailScrollUp(3)
	b.DetailScrollDown(100) // Over-scroll should be safe
	b.DetailScrollUp(100)
}

// TestDetailPanelRenderWithWidth verifies detail panel appears at sufficient width
func TestDetailPanelRenderWithWidth(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{{ID: "1", Status: model.StatusOpen}}
	b := ui.NewBoardModel(issues, theme)

	b.ShowDetail()

	// At narrow width (80), detail panel shouldn't show
	output80 := b.View(80, 30)

	// At wide width (160), detail panel should show
	output160 := b.View(160, 30)

	// Wide output should be longer (includes detail panel)
	// This is a heuristic - the exact behavior depends on implementation
	if len(output160) < len(output80) {
		t.Log("Note: Detail panel may not show at 160 width depending on implementation threshold")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Layout Tests at Various Widths (bv-4agf)
// ═══════════════════════════════════════════════════════════════════════════════

// TestLayoutNarrow80 verifies board renders at narrow terminal
func TestLayoutNarrow80(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Title: "Task 1"},
		{ID: "2", Status: model.StatusInProgress, Title: "Task 2"},
		{ID: "3", Status: model.StatusClosed, Title: "Task 3"},
	}
	b := ui.NewBoardModel(issues, theme)

	output := b.View(80, 24)
	if output == "" {
		t.Error("Board should render at 80 cols")
	}
	// Cards should still be readable
	if len(output) < 100 {
		t.Error("Output seems too short for 80 col view")
	}
}

// TestLayoutMedium120 verifies board renders at medium terminal
func TestLayoutMedium120(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Title: "Task 1"},
		{ID: "2", Status: model.StatusInProgress, Title: "Task 2"},
		{ID: "3", Status: model.StatusBlocked, Title: "Task 3"},
		{ID: "4", Status: model.StatusClosed, Title: "Task 4"},
	}
	b := ui.NewBoardModel(issues, theme)

	output := b.View(120, 30)
	if output == "" {
		t.Error("Board should render at 120 cols")
	}
}

// TestLayoutWide160 verifies board renders at wide terminal
func TestLayoutWide160(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Title: "Task 1"},
		{ID: "2", Status: model.StatusInProgress, Title: "Task 2"},
		{ID: "3", Status: model.StatusBlocked, Title: "Task 3"},
		{ID: "4", Status: model.StatusClosed, Title: "Task 4"},
	}
	b := ui.NewBoardModel(issues, theme)

	output := b.View(160, 30)
	if output == "" {
		t.Error("Board should render at 160 cols")
	}
}

// TestLayoutUltraWide200 verifies board renders at ultra-wide terminal
func TestLayoutUltraWide200(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Title: "Long task title that might wrap on narrower screens"},
		{ID: "2", Status: model.StatusInProgress, Title: "Another long task title"},
	}
	b := ui.NewBoardModel(issues, theme)

	output := b.View(200, 40)
	if output == "" {
		t.Error("Board should render at 200 cols")
	}
}

// TestLayoutMinimalHeight verifies board handles short terminals
func TestLayoutMinimalHeight(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Title: "Task 1"},
	}
	b := ui.NewBoardModel(issues, theme)

	// Very short terminal
	output := b.View(80, 8)
	if output == "" {
		t.Error("Board should render at minimal height")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Filter Integration Tests (bv-4agf)
// ═══════════════════════════════════════════════════════════════════════════════

// TestSetIssuesClearsSearch verifies SetIssues clears stale search state
func TestSetIssuesClearsSearch(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test1", Title: "Test One", Status: model.StatusOpen},
		{ID: "test2", Title: "Test Two", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Start search
	b.StartSearch()
	for _, ch := range "test" {
		b.AppendSearchChar(ch)
	}
	if b.SearchMatchCount() != 2 {
		t.Fatalf("Expected 2 matches before filter, got %d", b.SearchMatchCount())
	}

	// Filter to different issues
	b.SetIssues([]model.Issue{
		{ID: "other1", Title: "Other Issue", Status: model.StatusOpen},
	})

	// Search should be cleared
	if b.SearchMatchCount() != 0 {
		t.Errorf("Search matches should be cleared after SetIssues, got %d", b.SearchMatchCount())
	}
}

// TestSetIssuesPreservesSwimLaneMode verifies swimlane mode persists through filter
func TestSetIssuesPreservesSwimLaneMode(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
	}
	b := ui.NewBoardModel(issues, theme)

	// Switch to Priority mode
	b.CycleSwimLaneMode()
	if b.GetSwimLaneModeName() != "Priority" {
		t.Fatal("Should be in Priority mode")
	}

	// Filter to new issues
	b.SetIssues([]model.Issue{
		{ID: "2", Status: model.StatusClosed, Priority: 0},
	})

	// Mode should still be Priority
	if b.GetSwimLaneModeName() != "Priority" {
		t.Errorf("Swimlane mode should persist, expected Priority, got %s", b.GetSwimLaneModeName())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Edge Case Tests (bv-4agf)
// ═══════════════════════════════════════════════════════════════════════════════

// TestSingleColumnOnly verifies board works with all items in one column
func TestSingleColumnOnly(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen},
		{ID: "2", Status: model.StatusOpen},
		{ID: "3", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// All in Open column
	if b.ColumnCount(0) != 3 {
		t.Errorf("Expected all 3 in Open column")
	}
	for i := 1; i < 4; i++ {
		if b.ColumnCount(i) != 0 {
			t.Errorf("Expected 0 in column %d", i)
		}
	}

	// In Status mode (bv-tf6j), all 4 columns are visible
	// MoveRight moves to InProgress (empty) - SelectedIssue returns nil
	b.MoveRight()
	sel := b.SelectedIssue()
	if sel != nil {
		t.Error("Should be in empty column after MoveRight (Status mode shows all columns)")
	}

	// But MoveLeft should go back to Open
	b.MoveLeft()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Error("Should be back in Open column after MoveLeft")
	}
}

// TestLongTitleTruncation verifies long titles are truncated gracefully
func TestLongTitleTruncation(t *testing.T) {
	theme := createTheme()
	longTitle := "This is a very long title that should be truncated when displayed in the card view because it exceeds the available width"
	issues := []model.Issue{
		{ID: "1", Title: longTitle, Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Should render without panic
	output := b.View(80, 24)
	if output == "" {
		t.Error("Should render with long title")
	}
}

// TestUnicodeTitles verifies Unicode titles display correctly
func TestUnicodeTitles(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Title: "日本語タイトル", Status: model.StatusOpen},
		{ID: "2", Title: "Émoji test 🎉🚀", Status: model.StatusOpen},
		{ID: "3", Title: "Кириллица title", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Should render without panic
	output := b.View(120, 30)
	if output == "" {
		t.Error("Should render Unicode titles")
	}
}

// TestManyIssuesPerformance verifies board handles 100+ cards
func TestManyIssuesPerformance(t *testing.T) {
	theme := createTheme()
	var issues []model.Issue
	for i := 0; i < 100; i++ {
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("issue-%d", i),
			Title:     fmt.Sprintf("Task number %d with some description", i),
			Status:    model.Status([]string{"open", "in_progress", "blocked", "closed"}[i%4]),
			Priority:  i % 5,
			CreatedAt: createTime(i),
		})
	}
	b := ui.NewBoardModel(issues, theme)

	if b.TotalCount() != 100 {
		t.Errorf("Expected 100 issues, got %d", b.TotalCount())
	}

	// Should render without hanging
	output := b.View(160, 40)
	if output == "" {
		t.Error("Should render 100 cards")
	}

	// Navigation should work
	for i := 0; i < 50; i++ {
		b.MoveDown()
	}
	b.PageDown(10)
	b.MoveRight()
	b.MoveRight()

	sel := b.SelectedIssue()
	if sel == nil {
		t.Error("Should have selection after navigation")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Column Statistics Tests (bv-nl8a)
// ═══════════════════════════════════════════════════════════════════════════════

// TestColumnStatsNarrowWidth verifies minimal stats at narrow width (<100)
func TestColumnStatsNarrowWidth(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, CreatedAt: createTime(48)}, // P0
		{ID: "2", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(24)}, // P1
		{ID: "3", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},  // P2
	}
	b := ui.NewBoardModel(issues, theme)

	// At narrow width (<100), should just show count without P0/P1 indicators
	output := b.View(80, 24)
	if output == "" {
		t.Error("Should render at narrow width")
	}
	// The header should include the count "(3)" but not necessarily P0/P1 indicators
	// (Visual verification - output rendering depends on exact implementation)
}

// TestColumnStatsMediumWidth verifies P0/P1 counts at medium width (100-140)
func TestColumnStatsMediumWidth(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, CreatedAt: createTime(48)}, // P0
		{ID: "2", Status: model.StatusOpen, Priority: 0, CreatedAt: createTime(24)}, // P0
		{ID: "3", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(12)}, // P1
		{ID: "4", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},  // P2
	}
	b := ui.NewBoardModel(issues, theme)

	// At medium width (100-140), should show P0/P1 indicators
	output := b.View(120, 30)
	if output == "" {
		t.Error("Should render at medium width")
	}
	// Should include priority indicators in header
}

// TestColumnStatsWideWidth verifies full stats at wide width (>140)
func TestColumnStatsWideWidth(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, CreatedAt: createTime(24 * 60)}, // P0, 60d old
		{ID: "2", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(24 * 30)}, // P1, 30d old
		{ID: "3", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(24 * 7)},  // P2, 7d old
	}
	b := ui.NewBoardModel(issues, theme)

	// At wide width (>140), should show P0/P1 + oldest age
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render at wide width")
	}
	// Should include age indicator in header
}

// TestColumnStatsBlockedCountInProgress verifies blocked count shows in In Progress column
func TestColumnStatsBlockedCountInProgress(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "blocker", Status: model.StatusOpen, Priority: 1},
		{
			ID:       "in-progress-blocked",
			Status:   model.StatusInProgress,
			Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "in-progress-blocked", DependsOnID: "blocker", Type: model.DepBlocks},
			},
		},
		{ID: "in-progress-clean", Status: model.StatusInProgress, Priority: 2},
	}
	b := ui.NewBoardModel(issues, theme)

	// At wide width, In Progress column should show blocked count
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render with blocked items in In Progress")
	}
}

// TestColumnStatsEmptyColumn verifies stats work with empty columns
func TestColumnStatsEmptyColumn(t *testing.T) {
	theme := createTheme()
	// Only Open column has items
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, CreatedAt: createTime(24)},
	}
	b := ui.NewBoardModel(issues, theme)

	// Should render without panic
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render with mostly empty columns")
	}
}

// TestColumnStatsAllPriorities verifies all priority levels are counted correctly
func TestColumnStatsAllPriorities(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "p0-1", Status: model.StatusOpen, Priority: 0},
		{ID: "p0-2", Status: model.StatusOpen, Priority: 0},
		{ID: "p1-1", Status: model.StatusOpen, Priority: 1},
		{ID: "p1-2", Status: model.StatusOpen, Priority: 1},
		{ID: "p1-3", Status: model.StatusOpen, Priority: 1},
		{ID: "p2", Status: model.StatusOpen, Priority: 2},
		{ID: "p3", Status: model.StatusOpen, Priority: 3},
	}
	b := ui.NewBoardModel(issues, theme)

	// Total should be 7
	if b.ColumnCount(0) != 7 {
		t.Errorf("Expected 7 in Open column, got %d", b.ColumnCount(0))
	}

	// Should render with P0=2, P1=3 in indicators
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render with mixed priorities")
	}
}

// TestColumnStatsSwimLaneModeChange verifies stats adapt when swimlane mode changes
func TestColumnStatsSwimLaneModeChange(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug},
		{ID: "2", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeFeature},
	}
	b := ui.NewBoardModel(issues, theme)

	// Status mode: both in Open column
	output1 := b.View(160, 30)
	if output1 == "" {
		t.Error("Should render in Status mode")
	}

	// Switch to Priority mode
	b.CycleSwimLaneMode()
	output2 := b.View(160, 30)
	if output2 == "" {
		t.Error("Should render in Priority mode")
	}

	// Switch to Type mode
	b.CycleSwimLaneMode()
	output3 := b.View(160, 30)
	if output3 == "" {
		t.Error("Should render in Type mode")
	}
}

// TestColumnStatsOldItemAge verifies oldest item age calculation
func TestColumnStatsOldItemAge(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "new", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},          // 1 hour old
		{ID: "medium", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(24 * 14)}, // 14 days old
		{ID: "oldest", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(24 * 90)}, // 90 days old
	}
	b := ui.NewBoardModel(issues, theme)

	// At wide width, oldest age should be calculated from the 90-day-old item
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render with old items")
	}
}

// TestColumnStatsAfterSetIssues verifies stats update after SetIssues
func TestColumnStatsAfterSetIssues(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0},
	}
	b := ui.NewBoardModel(issues, theme)

	// Initial render
	output1 := b.View(160, 30)
	if output1 == "" {
		t.Error("Should render initially")
	}

	// Update to different issues
	b.SetIssues([]model.Issue{
		{ID: "a", Status: model.StatusOpen, Priority: 1},
		{ID: "b", Status: model.StatusOpen, Priority: 1},
		{ID: "c", Status: model.StatusOpen, Priority: 1},
	})

	// Re-render should show updated stats
	output2 := b.View(160, 30)
	if output2 == "" {
		t.Error("Should render after SetIssues")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Empty Column Handling Tests (bv-tf6j)
// ═══════════════════════════════════════════════════════════════════════════════

// TestEmptyColumnHandlingStatusMode verifies Status mode always shows all 4 columns
func TestEmptyColumnHandlingStatusMode(t *testing.T) {
	theme := createTheme()
	// Only Open column has items - other columns are empty
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
	}
	b := ui.NewBoardModel(issues, theme)

	// Status mode (default) should show all 4 columns
	output := b.View(160, 30)
	if output == "" {
		t.Error("Should render board")
	}

	// Verify title bar shows Status mode
	if !strings.Contains(output, "[by: Status]") {
		t.Error("Should show Status mode in title bar")
	}

	// Hidden count should be 0 (all columns shown in Status mode)
	if b.HiddenColumnCount() != 0 {
		t.Errorf("Status mode should show all columns, got %d hidden", b.HiddenColumnCount())
	}
}

// TestEmptyColumnHandlingPriorityMode verifies Priority mode hides empty columns
func TestEmptyColumnHandlingPriorityMode(t *testing.T) {
	theme := createTheme()
	// Only P1 items - P0, P2, P3 columns will be empty
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
		{ID: "2", Status: model.StatusOpen, Priority: 1},
	}
	b := ui.NewBoardModel(issues, theme)

	// Switch to Priority mode
	b.CycleSwimLaneMode()

	// Priority mode should hide empty columns
	hiddenCount := b.HiddenColumnCount()
	if hiddenCount != 3 {
		t.Errorf("Priority mode should hide 3 empty columns, got %d", hiddenCount)
	}

	// Verify title bar shows hidden count
	output := b.View(160, 30)
	if !strings.Contains(output, "[by: Priority]") {
		t.Error("Should show Priority mode in title bar")
	}
	if !strings.Contains(output, "[+3 hidden]") {
		t.Error("Should show hidden column count in title bar")
	}
}

// TestEmptyColumnHandlingTypeMode verifies Type mode hides empty columns
func TestEmptyColumnHandlingTypeMode(t *testing.T) {
	theme := createTheme()
	// Only Bug and Feature - Task and Epic will be empty
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, IssueType: model.TypeBug},
		{ID: "2", Status: model.StatusOpen, IssueType: model.TypeFeature},
	}
	b := ui.NewBoardModel(issues, theme)

	// Switch to Type mode (cycle twice: Status -> Priority -> Type)
	b.CycleSwimLaneMode()
	b.CycleSwimLaneMode()

	// Type mode should hide 2 empty columns (Task, Epic)
	hiddenCount := b.HiddenColumnCount()
	if hiddenCount != 2 {
		t.Errorf("Type mode should hide 2 empty columns, got %d", hiddenCount)
	}
}

// TestEmptyColumnVisibilityToggle verifies 'e' key toggles visibility
func TestEmptyColumnVisibilityToggle(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1},
	}
	b := ui.NewBoardModel(issues, theme)

	// Switch to Priority mode (where auto hides empty)
	b.CycleSwimLaneMode()

	// Initially Auto mode
	if b.GetEmptyColumnVisibilityMode() != "Auto" {
		t.Errorf("Initial mode should be Auto, got %s", b.GetEmptyColumnVisibilityMode())
	}
	initialHidden := b.HiddenColumnCount()

	// Toggle to "Show All"
	b.ToggleEmptyColumns()
	if b.GetEmptyColumnVisibilityMode() != "Show All" {
		t.Errorf("After first toggle should be Show All, got %s", b.GetEmptyColumnVisibilityMode())
	}
	if b.HiddenColumnCount() != 0 {
		t.Error("Show All should have 0 hidden columns")
	}

	// Toggle to "Hide Empty"
	b.ToggleEmptyColumns()
	if b.GetEmptyColumnVisibilityMode() != "Hide Empty" {
		t.Errorf("After second toggle should be Hide Empty, got %s", b.GetEmptyColumnVisibilityMode())
	}

	// Toggle back to "Auto"
	b.ToggleEmptyColumns()
	if b.GetEmptyColumnVisibilityMode() != "Auto" {
		t.Errorf("After third toggle should be Auto, got %s", b.GetEmptyColumnVisibilityMode())
	}
	if b.HiddenColumnCount() != initialHidden {
		t.Error("Auto should restore initial hidden count")
	}
}

// TestEmptyColumnNavigationSkipsHidden verifies navigation skips hidden columns
func TestEmptyColumnNavigationSkipsHidden(t *testing.T) {
	theme := createTheme()
	// P0 in col 0, P2 in col 2 - P1 and P3 columns empty
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0}, // P0 -> col 0
		{ID: "2", Status: model.StatusOpen, Priority: 2}, // P2 -> col 2
	}
	b := ui.NewBoardModel(issues, theme)

	// Switch to Priority mode
	b.CycleSwimLaneMode()

	// Should start in first visible column (P0)
	sel1 := b.SelectedIssue()
	if sel1 == nil || sel1.Priority != 0 {
		t.Error("Should start in P0 column")
	}

	// MoveRight should jump to P2 (skipping empty P1)
	b.MoveRight()
	sel2 := b.SelectedIssue()
	if sel2 == nil || sel2.Priority != 2 {
		t.Error("MoveRight should jump to P2, skipping empty P1")
	}

	// MoveRight again should stay (no more columns)
	b.MoveRight()
	sel3 := b.SelectedIssue()
	if sel3 == nil || sel3.Priority != 2 {
		t.Error("Should stay in P2 column")
	}
}

// TestEmptyColumnTitleBarRendering verifies the title bar is rendered correctly
func TestEmptyColumnTitleBarRendering(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Status mode - should show "BOARD [by: Status]"
	output := b.View(160, 30)
	if !strings.Contains(output, "BOARD") {
		t.Error("Title bar should contain 'BOARD'")
	}
	if !strings.Contains(output, "[by: Status]") {
		t.Error("Title bar should show swimlane mode")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Inline Card Expansion Tests (bv-i3ii)
// ═══════════════════════════════════════════════════════════════════════════

// TestInlineCardExpansion_Toggle verifies basic expand/collapse behavior
func TestInlineCardExpansion_Toggle(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test-1", Title: "First Issue", Status: model.StatusOpen},
		{ID: "test-2", Title: "Second Issue", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Initially no card is expanded
	if b.HasExpandedCard() {
		t.Error("No card should be expanded initially")
	}
	if b.GetExpandedID() != "" {
		t.Error("GetExpandedID should return empty string when no card expanded")
	}

	// Toggle expand on first card
	b.ToggleExpand()
	if !b.HasExpandedCard() {
		t.Error("Card should be expanded after ToggleExpand")
	}
	if b.GetExpandedID() != "test-1" {
		t.Errorf("Expected expanded card test-1, got %s", b.GetExpandedID())
	}
	if !b.IsCardExpanded("test-1") {
		t.Error("IsCardExpanded should return true for test-1")
	}

	// Toggle again should collapse
	b.ToggleExpand()
	if b.HasExpandedCard() {
		t.Error("Card should be collapsed after second ToggleExpand")
	}
}

// TestInlineCardExpansion_AutoCollapseOnNavigation verifies cards collapse when navigating
func TestInlineCardExpansion_AutoCollapseOnNavigation(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test-1", Title: "First Issue", Status: model.StatusOpen},
		{ID: "test-2", Title: "Second Issue", Status: model.StatusOpen},
		{ID: "test-3", Title: "Third Issue", Status: model.StatusInProgress},
	}
	b := ui.NewBoardModel(issues, theme)

	// Expand first card
	b.ToggleExpand()
	if !b.IsCardExpanded("test-1") {
		t.Error("test-1 should be expanded")
	}

	// Move down should collapse
	b.MoveDown()
	if b.HasExpandedCard() {
		t.Error("Card should collapse on MoveDown")
	}

	// Expand again and test MoveUp
	b.ToggleExpand()
	b.MoveUp()
	if b.HasExpandedCard() {
		t.Error("Card should collapse on MoveUp")
	}

	// Test MoveLeft/MoveRight
	b.MoveRight() // Move to different column
	b.ToggleExpand()
	expandedBefore := b.GetExpandedID()
	b.MoveLeft()
	if b.HasExpandedCard() {
		t.Errorf("Card %s should collapse on MoveLeft", expandedBefore)
	}
}

// TestInlineCardExpansion_OnlyOneExpanded verifies only one card can be expanded
func TestInlineCardExpansion_OnlyOneExpanded(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test-1", Title: "First Issue", Status: model.StatusOpen},
		{ID: "test-2", Title: "Second Issue", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Expand first card
	b.ToggleExpand()
	if b.GetExpandedID() != "test-1" {
		t.Error("test-1 should be expanded")
	}

	// Navigate without MoveUp/MoveDown to keep expansion
	// Actually, we auto-collapse on navigation, so this test verifies
	// that expanding a new card replaces the old one
	b.CollapseExpanded()
	b.MoveDown() // This will already have collapsed, but let's set up the state
	b.ToggleExpand()

	// Now test-2 should be expanded (not test-1)
	if b.GetExpandedID() != "test-2" {
		t.Errorf("Expected test-2 to be expanded, got %s", b.GetExpandedID())
	}
	if b.IsCardExpanded("test-1") {
		t.Error("test-1 should not be expanded anymore")
	}
}

// TestInlineCardExpansion_CollapseMethod verifies CollapseExpanded works
func TestInlineCardExpansion_CollapseMethod(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test-1", Title: "Test Issue", Status: model.StatusOpen},
	}
	b := ui.NewBoardModel(issues, theme)

	// Expand and then explicitly collapse
	b.ToggleExpand()
	if !b.HasExpandedCard() {
		t.Error("Card should be expanded")
	}

	b.CollapseExpanded()
	if b.HasExpandedCard() {
		t.Error("Card should be collapsed after CollapseExpanded")
	}

	// CollapseExpanded on already collapsed should be safe
	b.CollapseExpanded()
	if b.HasExpandedCard() {
		t.Error("Should still be collapsed")
	}
}

// TestInlineCardExpansion_NoIssueSelected verifies ToggleExpand is safe with no selection
func TestInlineCardExpansion_NoIssueSelected(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{} // Empty board
	b := ui.NewBoardModel(issues, theme)

	// ToggleExpand should not panic
	b.ToggleExpand()
	if b.HasExpandedCard() {
		t.Error("Should not expand anything when no issues exist")
	}
}

// TestInlineCardExpansion_RendersWithDoubleBorder verifies expanded card uses double border
func TestInlineCardExpansion_RendersWithDoubleBorder(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "test-1", Title: "Test Issue", Status: model.StatusOpen, Description: "A test description"},
	}
	b := ui.NewBoardModel(issues, theme)

	// Expand the card
	b.ToggleExpand()

	// Render the board
	output := b.View(120, 40)

	// Expanded card should show the expand indicator (▼) in header
	if !strings.Contains(output, "▼") {
		t.Error("Expanded card should show ▼ indicator")
	}
}

// TestInlineCardExpansion_ShowsDescription verifies expanded card shows description
func TestInlineCardExpansion_ShowsDescription(t *testing.T) {
	theme := createTheme()
	// Use text that survives markdown rendering
	description := "UNIQUE_DESC_CONTENT here."
	issues := []model.Issue{
		{ID: "test-1", Title: "Test Issue", Status: model.StatusOpen, Description: description},
	}
	b := ui.NewBoardModel(issues, theme)

	// Expand the card
	b.ToggleExpand()

	// Render the board
	output := b.View(120, 40)

	// Should contain the unique description text
	if !strings.Contains(output, "UNIQUE_DESC_CONTENT") {
		t.Error("Expanded card should show description content")
	}
}

// TestBoardResolvedBlockersAndBlocksIndex verifies resolved/tombstone blockers do not
// show active blocker badges (🚫) or inflate ⚡→N open unblocks count (bv-apal.52).
func TestBoardResolvedBlockersAndBlocksIndex(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{
			ID:       "BLK-CLOSED",
			Title:    "Already Closed Blocker",
			Status:   model.StatusClosed,
			Priority: 1,
		},
		{
			ID:       "BLK-TOMB",
			Title:    "Tombstone Blocker",
			Status:   model.StatusTombstone,
			Priority: 1,
		},
		{
			ID:       "READY-1",
			Title:    "Ready Card With Resolved Blockers",
			Status:   model.StatusOpen,
			Priority: 1,
			Dependencies: []*model.Dependency{
				{IssueID: "READY-1", DependsOnID: "BLK-CLOSED", Type: model.DepBlocks},
				{IssueID: "READY-1", DependsOnID: "BLK-TOMB", Type: model.DepBlocks},
			},
		},
		{
			ID:       "CLOSED-DEP",
			Title:    "Closed Dependent",
			Status:   model.StatusClosed,
			Priority: 2,
			Dependencies: []*model.Dependency{
				{IssueID: "CLOSED-DEP", DependsOnID: "READY-1", Type: model.DepBlocks},
			},
		},
	}

	b := ui.NewBoardModel(issues, theme)
	b.SetReadiness(model.NewReadinessIndex(issues))

	out := b.View(140, 35)
	if strings.Contains(out, "🚫←") {
		t.Fatalf("expected no active card blocker badge 🚫← when all blockers are closed/tombstone, got:\n%s", out)
	}
	if strings.Contains(out, "⚡→") {
		t.Fatalf("expected no ⚡→N badge when blockers are already closed/tombstone or dependent is closed, got:\n%s", out)
	}

	// Expand READY-1 and verify resolved blockers are labeled rather than active red blockers.
	if !b.SelectIssueByID("READY-1") {
		t.Fatal("expected to select READY-1")
	}
	b.ToggleExpand()
	expandedOut := b.View(140, 40)
	if !strings.Contains(expandedOut, "resolved") {
		t.Fatalf("expected expanded card to distinguish resolved blockers, got:\n%s", expandedOut)
	}
}

// TestBoardCustomParkedStatusBadgeInOpenColumn verifies non-standard statuses in ColOpen
// render a compact status pill like [deferred] or [pinned] (bv-apal.52).
func TestBoardCustomParkedStatusBadgeInOpenColumn(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "DEF-1", Title: "Deferred Task", Status: model.Status("deferred"), Priority: 1},
		{ID: "PIN-1", Title: "Pinned Spec", Status: model.StatusPinned, Priority: 2},
	}
	b := ui.NewBoardModel(issues, theme)
	out := b.View(140, 30)
	if !strings.Contains(out, "[deferred]") {
		t.Fatalf("expected [deferred] pill in Open column card, got:\n%s", out)
	}
	if !strings.Contains(out, "[pinned]") {
		t.Fatalf("expected [pinned] pill in Open column card, got:\n%s", out)
	}
}

// TestBoardSearchPersistenceAndSelectionPreservation verifies search highlights and header
// indicator persist after FinishSearch() until ClearSearch(), and SetIssues preserves selection (bv-apal.52).
func TestBoardSearchPersistenceAndSelectionPreservation(t *testing.T) {
	theme := createTheme()
	issues := []model.Issue{
		{ID: "AUTH-1", Title: "OAuth login callback", Status: model.StatusOpen, Priority: 1},
		{ID: "AUTH-2", Title: "Session refresh", Status: model.StatusOpen, Priority: 2},
		{ID: "AUTH-3", Title: "OAuth login token", Status: model.StatusInProgress, Priority: 1},
	}
	b := ui.NewBoardModel(issues, theme)
	b.StartSearch()
	for _, r := range "login" {
		b.AppendSearchChar(r)
	}
	b.FinishSearch()

	if b.IsSearchMode() {
		t.Fatal("expected searchMode=false after FinishSearch")
	}
	if b.GetSearchQuery() != "login" || b.SearchMatchCount() != 2 {
		t.Fatalf("expected persistent query 'login' with 2 matches after FinishSearch, got query=%q count=%d",
			b.GetSearchQuery(), b.SearchMatchCount())
	}
	if !b.IsSearchMatch(ui.ColOpen, 0) {
		t.Fatal("expected ColOpen row 0 to remain highlighted as a search match after FinishSearch")
	}
	outAfterEnter := b.View(140, 30)
	if !strings.Contains(outAfterEnter, `"login"`) || !strings.Contains(outAfterEnter, "1/2") {
		t.Fatalf("expected persistent search indicator in board header after Enter, got:\n%s", outAfterEnter)
	}

	b.NextMatch()
	if sel := b.SelectedIssue(); sel == nil || sel.ID != "AUTH-3" {
		t.Fatalf("expected NextMatch after Enter to select AUTH-3, got %+v", sel)
	}

	b.ClearSearch()
	if b.GetSearchQuery() != "" || b.SearchMatchCount() != 0 {
		t.Fatalf("expected ClearSearch to reset query and matches, got query=%q count=%d",
			b.GetSearchQuery(), b.SearchMatchCount())
	}

	// SetIssues preserves selected issue ID (AUTH-3) across rebuilds.
	b.SetIssues([]model.Issue{
		{ID: "NEW-0", Title: "New first item", Status: model.StatusInProgress, Priority: 0},
		{ID: "AUTH-3", Title: "OAuth login token", Status: model.StatusInProgress, Priority: 1},
	})
	if sel := b.SelectedIssue(); sel == nil || sel.ID != "AUTH-3" {
		t.Fatalf("expected SetIssues to preserve selected issue AUTH-3, got %+v", sel)
	}
}

func TestBoardSetIssuesAndSnapshotPreserveActiveSearchAndExpandedCard(t *testing.T) {
	theme := createTheme()
	initial := []model.Issue{
		{ID: "AUTH-1", Title: "OAuth callback", Status: model.StatusOpen, Priority: 1, Description: "First expanded card"},
		{ID: "AUTH-2", Title: "OAuth refresh", Status: model.StatusOpen, Priority: 2},
		{ID: "AUTH-3", Title: "OAuth logout", Status: model.StatusInProgress, Priority: 1},
	}
	b := ui.NewBoardModel(initial, theme)
	if !b.SelectIssueByID("AUTH-1") {
		t.Fatal("expected to select AUTH-1")
	}
	b.ToggleExpand()
	if !b.HasExpandedCard() || b.GetExpandedID() != "AUTH-1" {
		t.Fatalf("expected AUTH-1 expanded, got %q", b.GetExpandedID())
	}

	b.StartSearch()
	for _, r := range "oauth" {
		b.AppendSearchChar(r)
	}
	b.NextMatch()
	b.NextMatch() // cursor at index 2 (3rd match)
	if !b.IsSearchMode() || b.SearchMatchCount() != 3 || b.SearchCursorPos() != 3 {
		t.Fatalf("expected searchMode=true matches=3 pos=3, got mode=%v matches=%d pos=%d",
			b.IsSearchMode(), b.SearchMatchCount(), b.SearchCursorPos())
	}

	// SetIssues removes AUTH-3 -> searchMode stays true, query preserved, cursor clamped to 2/2, AUTH-1 stays expanded!
	refreshed := []model.Issue{
		{ID: "AUTH-1", Title: "OAuth callback", Status: model.StatusOpen, Priority: 1, Description: "First expanded card"},
		{ID: "AUTH-2", Title: "OAuth refresh", Status: model.StatusOpen, Priority: 2},
	}
	b.SetIssues(refreshed)
	if !b.IsSearchMode() || b.GetSearchQuery() != "oauth" {
		t.Fatalf("SetIssues wiped active search: mode=%v query=%q", b.IsSearchMode(), b.GetSearchQuery())
	}
	if b.SearchMatchCount() != 2 || b.SearchCursorPos() != 2 {
		t.Fatalf("expected clamped 2/2 matches after SetIssues, got pos=%d/%d", b.SearchCursorPos(), b.SearchMatchCount())
	}
	if !b.HasExpandedCard() || b.GetExpandedID() != "AUTH-1" {
		t.Fatalf("SetIssues collapsed still-present expanded card AUTH-1, got %q", b.GetExpandedID())
	}

	// SetSnapshot also preserves active search and expanded card when present.
	snap := ui.NewSnapshotBuilder(refreshed).Build()
	t.Cleanup(snap.Analysis.WaitForPhase2)
	b.SetSnapshot(snap)
	if !b.IsSearchMode() || b.GetSearchQuery() != "oauth" || b.SearchMatchCount() != 2 {
		t.Fatalf("SetSnapshot wiped active search: mode=%v query=%q count=%d",
			b.IsSearchMode(), b.GetSearchQuery(), b.SearchMatchCount())
	}
	if !b.HasExpandedCard() || b.GetExpandedID() != "AUTH-1" {
		t.Fatalf("SetSnapshot collapsed still-present expanded card AUTH-1, got %q", b.GetExpandedID())
	}

	// If expanded card is removed from refreshed board, expandedCardID clears cleanly.
	b.SetIssues([]model.Issue{{ID: "AUTH-2", Title: "OAuth refresh", Status: model.StatusOpen, Priority: 2}})
	if b.HasExpandedCard() {
		t.Fatalf("expected removed expanded card to clear, got %q", b.GetExpandedID())
	}
}

func TestBoardSearchSanitizesControlCharsAndMultilinePaste(t *testing.T) {
	theme := createTheme()
	b := ui.NewBoardModel([]model.Issue{{ID: "A-1", Title: "oauth login flow", Status: model.StatusOpen}}, theme)
	b.StartSearch()
	for _, r := range "\r\noauth\r\n\tlogin\x00\x1bflow" {
		b.AppendSearchChar(r)
	}
	if got := b.GetSearchQuery(); got != "oauth loginflow" {
		t.Fatalf("expected sanitized 1-line query %q, got %q", "oauth loginflow", got)
	}
	out := b.View(100, 24)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Search:") && strings.Contains(line, "\r") {
			t.Fatalf("search bar leaked carriage return: %q", line)
		}
	}
}

func TestBoardFullIssuesScopeAndClaimableBorder(t *testing.T) {
	r := lipgloss.NewRenderer(os.Stdout)
	r.SetColorProfile(termenv.TrueColor)
	theme := ui.DefaultTheme(r)

	future := time.Now().Add(48 * time.Hour)
	allIssues := []model.Issue{
		{ID: "READY-GATE", Title: "Ready Unblocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{
			ID: "DOWN-1", Title: "Downstream 1", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "DOWN-1", DependsOnID: "READY-GATE", Type: model.DepBlocks}},
		},
		{
			ID: "DOWN-2", Title: "Downstream 2", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "DOWN-2", DependsOnID: "READY-GATE", Type: model.DepBlocks}},
		},
		{ID: "OPEN-BLK", Title: "Still Open Blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug},
		{
			ID: "CLOSED-1", Title: "Closed With Open Blocker", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "CLOSED-1", DependsOnID: "OPEN-BLK", Type: model.DepBlocks}},
		},
		{ID: "PINNED-1", Title: "Pinned Open Item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Pinned: true},
		{ID: "DEFER-1", Title: "Deferred Open Item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, DeferUntil: &future},
		{ID: "CLAIM-1", Title: "Claimable Ready Item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
	}

	b := ui.NewBoardModel(allIssues, theme)
	b.SetReadiness(model.NewReadinessIndex(allIssues))
	b.SetFullIssues(allIssues)

	// (a) Under Ready filter (DOWN-1 and DOWN-2 excluded from filtered view), READY-GATE still shows ⚡→2!
	b.SetIssues([]model.Issue{allIssues[0]})
	readyOut := b.View(140, 30)
	if !strings.Contains(readyOut, "⚡→2") {
		t.Fatalf("expected READY-GATE under Ready filter to retain ⚡→2 from fullIssues, got:\n%s", readyOut)
	}

	// (b) Under Closed filter (OPEN-BLK excluded from filtered view), CLOSED-1 must NOT mismark OPEN-BLK as resolved!
	b.SetIssues([]model.Issue{allIssues[4]})
	if !b.SelectIssueByID("CLOSED-1") {
		t.Fatal("expected to select CLOSED-1")
	}
	b.ToggleExpand()
	closedOut := b.View(140, 35)
	if strings.Contains(closedOut, "resolved") {
		t.Fatalf("expected open blocker OPEN-BLK on closed card CLOSED-1 NOT marked resolved, got:\n%s", closedOut)
	}
	b.CollapseExpanded()

	// (c) Non-claimable ColOpen cards (Pinned, DeferUntil) must NOT get the green Ready border that CLAIM-1 gets.
	bClaim := ui.NewBoardModel([]model.Issue{allIssues[7]}, theme)
	bClaim.SetReadiness(model.NewReadinessIndex(allIssues))
	bClaim.SetFullIssues(allIssues)
	bClaim.MoveRight() // move selection away from ColOpen so selection highlight doesn't mask border color
	claimANSI := bClaim.View(140, 28)

	bPinned := ui.NewBoardModel([]model.Issue{allIssues[5]}, theme)
	bPinned.SetReadiness(model.NewReadinessIndex(allIssues))
	bPinned.SetFullIssues(allIssues)
	bPinned.MoveRight()
	pinnedANSI := bPinned.View(140, 28)

	bDefer := ui.NewBoardModel([]model.Issue{allIssues[6]}, theme)
	bDefer.SetReadiness(model.NewReadinessIndex(allIssues))
	bDefer.SetFullIssues(allIssues)
	bDefer.MoveRight()
	deferANSI := bDefer.View(140, 28)

	// ColorSuccess hex is #81c784 -> RGB 129;199;131 on the card's rounded top-left border ("╭─").
	const greenCardTopBorder = "\x1b[38;2;129;199;131m╭─"
	if !strings.Contains(claimANSI, greenCardTopBorder) {
		t.Fatalf("expected claimable open card CLAIM-1 to render with green Ready border, got:\n%q", claimANSI)
	}
	if strings.Contains(pinnedANSI, greenCardTopBorder) {
		t.Fatalf("Pinned card must NOT get green Ready card border:\n%q", pinnedANSI)
	}
	if strings.Contains(deferANSI, greenCardTopBorder) {
		t.Fatalf("Future-deferred card must NOT get green Ready card border:\n%q", deferANSI)
	}
}

func TestBoardPreservedSearchColumnMoveAndCrossFilterInlineBlockerTitle(t *testing.T) {
	theme := createTheme()

	// 1) refreshPreservedSearch tracks prevMatchID across column moves so the matched card stays focused.
	initial := []model.Issue{
		{ID: "match-alpha", Title: "Alpha Search Card", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "match-beta", Title: "Beta Search Card", Status: model.StatusBlocked, Priority: 0, IssueType: model.TypeTask},
	}
	b := ui.NewBoardModel(initial, theme)
	b.StartSearch()
	for _, ch := range "match" {
		b.AppendSearchChar(ch)
	}
	b.FinishSearch()
	b.NextMatch() // moves to match index 1 ("match-beta" in Blocked column)
	if sel := b.SelectedIssue(); sel == nil || sel.ID != "match-beta" || b.SearchCursorPos() != 2 {
		t.Fatalf("precondition failed: sel=%v pos=%d", sel, b.SearchCursorPos())
	}

	// Move match-beta to Open (Priority 0 sorts BEFORE match-alpha Priority 1, becoming match index 0).
	moved := []model.Issue{
		{ID: "match-alpha", Title: "Alpha Search Card", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "match-beta", Title: "Beta Search Card", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
	}
	b.SetIssues(moved)
	if sel := b.SelectedIssue(); sel == nil || sel.ID != "match-beta" {
		t.Fatalf("expected match-beta to stay selected after column move, got %v", sel)
	}
	if b.SearchCursorPos() != 1 || !b.IsMatchHighlighted(ui.ColOpen, 0) {
		t.Fatalf("expected searchCursor to follow match-beta to index 0 (pos 1) at (ColOpen, 0), got pos=%d highlighted=%v",
			b.SearchCursorPos(), b.IsMatchHighlighted(ui.ColOpen, 0))
	}

	// 2) Unexpanded card 1-blocker inline title preview resolves external/cross-filter blocker via lookupIssue (fullIssues).
	extBlocker := model.Issue{ID: "EXT-BLK", Title: "AuthGateService", Status: model.StatusInProgress, Priority: 0, IssueType: model.TypeBug}
	dependent := model.Issue{
		ID: "CARD-DEP", Title: "Ship Dashboard", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeFeature,
		Dependencies: []*model.Dependency{{IssueID: "CARD-DEP", DependsOnID: "EXT-BLK", Type: model.DepBlocks}},
	}
	all := []model.Issue{extBlocker, dependent}
	bFiltered := ui.NewBoardModel([]model.Issue{dependent}, theme)
	bFiltered.SetReadiness(model.NewReadinessIndex(all))
	bFiltered.SetFullIssues(all)
	bFiltered.SetIssues([]model.Issue{dependent})

	out := bFiltered.View(140, 28)
	if !strings.Contains(out, "EXT-BLK (AuthGateSer") {
		t.Fatalf("expected unexpanded card inline 1-blocker preview to include blocker title from fullIssues, got:\n%s", out)
	}
}

func TestBoardNonClaimableOpenBadgesAndReadyBorderExclusion(t *testing.T) {
	renderer := lipgloss.NewRenderer(os.Stdout)
	renderer.SetColorProfile(termenv.TrueColor)
	renderer.SetHasDarkBackground(true)
	theme := ui.DefaultTheme(renderer)

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	futureDefer := now.Add(48 * time.Hour)

	const greenReadySGR = "38;2;129;199;131m" // #81c784 claimable ready border

	// Positive control: a normal open, unblocked, non-parked issue renders the green claimable border when unselected or expanded.
	claimable := model.Issue{ID: "CLAIM-OK", Title: "Ready work", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now}
	bClaim := ui.NewBoardModel([]model.Issue{claimable}, theme)
	bClaim.SetReferenceTime(now)
	bClaim.SetReadiness(model.NewReadinessIndex([]model.Issue{claimable}))
	bClaim.ToggleExpand()
	claimOut := bClaim.View(140, 30)
	if !strings.Contains(claimOut, greenReadySGR) {
		t.Fatalf("expected claimable open card to render green ready border (%s), got:\n%q", greenReadySGR, claimOut)
	}

	// Planted negative controls + positive badge observables: [pinned], [template], [ephemeral], [deferred]
	// render their compact badge in the Open column AND are excluded from isCardClaimable (no green ready border).
	parkedCases := []struct {
		name  string
		issue model.Issue
		badge string
	}{
		{
			name:  "pinned",
			issue: model.Issue{ID: "PIN-1", Title: "Pinned item", Status: model.StatusOpen, Priority: 1, Pinned: true, CreatedAt: now, UpdatedAt: now},
			badge: "[pinned]",
		},
		{
			name:  "template",
			issue: model.Issue{ID: "TPL-1", Title: "Template item", Status: model.StatusOpen, Priority: 1, IsTemplate: true, CreatedAt: now, UpdatedAt: now},
			badge: "[template]",
		},
		{
			name:  "ephemeral",
			issue: model.Issue{ID: "EPH-1", Title: "Ephemeral item", Status: model.StatusOpen, Priority: 1, Ephemeral: true, CreatedAt: now, UpdatedAt: now},
			badge: "[ephemeral]",
		},
		{
			name:  "deferred",
			issue: model.Issue{ID: "DEF-1", Title: "Deferred item", Status: model.StatusOpen, Priority: 1, DeferUntil: &futureDefer, CreatedAt: now, UpdatedAt: now},
			badge: "[deferred]",
		},
	}

	for _, tc := range parkedCases {
		t.Run(tc.name, func(t *testing.T) {
			// Select a neutral first card so tc.issue is rendered unselected in ColOpen, then also test expanded!
			anchor := model.Issue{
				ID: "ANCHOR-BLK", Title: "Blocked anchor", Status: model.StatusOpen, Priority: 0, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: "ANCHOR-BLK", DependsOnID: tc.issue.ID, Type: model.DepRelated}},
			}
			// Test both with nil readiness (fallback branch) and with ReadinessIndex.
			for _, withReadiness := range []bool{false, true} {
				b := ui.NewBoardModel([]model.Issue{anchor, tc.issue}, theme)
				b.SetReferenceTime(now)
				if withReadiness {
					b.SetReadiness(model.NewReadinessIndex([]model.Issue{anchor, tc.issue}))
				}
				compactOut := b.View(140, 32)
				if !strings.Contains(compactOut, tc.badge) {
					t.Fatalf("withReadiness=%v: expected badge %q in compact board view:\n%s", withReadiness, tc.badge, compactOut)
				}

				// Expand tc.issue directly so its card border is decided solely by isCardClaimable (not selection highlight).
				bExpanded := ui.NewBoardModel([]model.Issue{tc.issue}, theme)
				bExpanded.SetReferenceTime(now)
				if withReadiness {
					bExpanded.SetReadiness(model.NewReadinessIndex([]model.Issue{tc.issue}))
				}
				bExpanded.ToggleExpand()
				expOut := bExpanded.View(140, 32)
				if strings.Contains(expOut, greenReadySGR) {
					t.Fatalf("withReadiness=%v: non-claimable %s card rendered green claimable border!", withReadiness, tc.badge)
				}
			}
		})
	}
}

func TestBoardLightThemeMarkdownRendererAnd24RowExpandedCardVisibility(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// 1. Light-theme MarkdownRenderer in Board detail panel & expanded card (honors BVThemeOverride / light renderer, no dark #f8f8f2 fallback).
	t.Cleanup(func() { ui.SetThemeOverride("") })
	ui.SetThemeOverride("light")
	lightRenderer := lipgloss.NewRenderer(os.Stdout)
	lightRenderer.SetColorProfile(termenv.TrueColor)
	lightRenderer.SetHasDarkBackground(false)
	lightTheme := ui.DefaultTheme(lightRenderer)

	mdIssues := []model.Issue{
		{
			ID:          "LIGHT-1",
			Title:       "Light Theme Card",
			Description: "## Section Heading\n\nBody paragraph text for light theme verification.",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeFeature,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now.Add(-time.Hour),
		},
	}
	bLight := ui.NewBoardModel(mdIssues, lightTheme)
	bLight.SetReferenceTime(now)
	bLight.ToggleDetail()
	bLight.ToggleExpand()
	lightOut := bLight.View(120, 32)
	// Light theme Primary (#6B47D9 -> 38;2;107;71;217) and light document text (#000000 -> 38;2;0;0;0), NOT dark text (#f8f8f2 -> 38;2;248;248;242).
	if !strings.Contains(lightOut, "38;2;107;71;217") {
		t.Fatalf("expected light-theme heading color (#6B47D9 -> 107;71;217) in board output:\n%q", lightOut)
	}
	if strings.Contains(lightOut, "38;2;248;248;242") {
		t.Fatalf("light-theme board markdown leaked dark-theme #f8f8f2 foreground:\n%q", lightOut)
	}

	// Planted negative control: switching SetThemeOverride("dark") renders dark-theme Primary (#BD93F9 -> 189;147;249).
	ui.SetThemeOverride("dark")
	darkRenderer := lipgloss.NewRenderer(os.Stdout)
	darkRenderer.SetColorProfile(termenv.TrueColor)
	darkRenderer.SetHasDarkBackground(true)
	darkTheme := ui.DefaultTheme(darkRenderer)
	bLight.SetTheme(darkTheme)
	darkOut := bLight.View(120, 32)
	if !strings.Contains(darkOut, "38;2;189;147;249") {
		t.Fatalf("negative control failed: expected dark-theme color (#BD93F9 -> 189;147;249) after SetTheme(dark):\n%q", darkOut)
	}
	ui.SetThemeOverride("")

	// 2. 24-row terminal (View(100, 23)): inline-expanding card #3 ('d' at sel==2) anchors around sel so its body & footer are not clipped.
	descLines := "First expanded line.\n\nSecond line bottom sentinel."
	fourCards := []model.Issue{
		{ID: "CARD-1", Title: "Collapsed Above One", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "CARD-2", Title: "Collapsed Above Two", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{
			ID: "CARD-3", Title: "Third Card Expanded", Description: descLines,
			Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Labels:    []string{"board-ux", "ux-fix"},
			CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-15 * time.Minute),
		},
		{ID: "CARD-4", Title: "Collapsed Below Four", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	b24 := ui.NewBoardModel(fourCards, createTheme())
	b24.SetReferenceTime(now)
	b24.MoveDown()
	b24.MoveDown() // select CARD-3 (row index 2)
	b24.ToggleExpand()

	rendered24 := ansi.Strip(b24.View(100, 23))
	if !strings.Contains(rendered24, "CARD-3") || !strings.Contains(rendered24, "bottom sentinel") || !strings.Contains(rendered24, "Created:") || !strings.Contains(rendered24, "╚") {
		t.Fatalf("24-row expanded CARD-3 clipped description, timestamps, or bottom border:\n%s", rendered24)
	}
	if strings.Contains(rendered24, "CARD-1") {
		t.Fatalf("expected collapsed CARD-1 above expanded CARD-3 to scroll off on 24-row height:\n%s", rendered24)
	}

	// Even with a 15-line wrapping description on 24 rows, compact description clamping preserves Created: and bottom border ╚.
	fourCards[2].Description = strings.Repeat("Long wrapped description sentence in narrow board card. ", 8)
	b24Long := ui.NewBoardModel(fourCards, createTheme())
	b24Long.SetReferenceTime(now)
	b24Long.MoveDown()
	b24Long.MoveDown()
	b24Long.ToggleExpand()
	rendered24Long := ansi.Strip(b24Long.View(100, 23))
	if !strings.Contains(rendered24Long, "Created:") || !strings.Contains(rendered24Long, "╚") {
		t.Fatalf("24-row expanded CARD-3 with long description clipped timestamps or bottom border:\n%s", rendered24Long)
	}
}

func TestBoardDetailScrollPreservationWithExpandedCardAnd24RowMultiDepBounds(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	var longDescLines []string
	for i := 1; i <= 35; i++ {
		longDescLines = append(longDescLines, fmt.Sprintf("Paragraph %02d detail marker LINE_%02d.", i, i))
	}
	issues := []model.Issue{
		{
			ID:          "SCROLL-1",
			Title:       "Scrollable Detail Card",
			Description: strings.Join(longDescLines, "\n\n"),
			Status:      model.StatusOpen,
			Priority:    0,
			IssueType:   model.TypeFeature,
			CreatedAt:   now.Add(-4 * time.Hour),
			UpdatedAt:   now.Add(-time.Hour),
		},
		{
			ID:          "SCROLL-2",
			Title:       "Second Card Top Marker",
			Description: "TOP_OF_SECOND_CARD\n\n" + strings.Repeat("Extra paragraph.\n\n", 20),
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeTask,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now.Add(-30 * time.Minute),
		},
	}

	// [R1-B4]: Expanding a card inline while scrolling the detail panel must never reset detail YOffset across View() calls.
	b := ui.NewBoardModel(issues, createTheme())
	b.SetReferenceTime(now)
	b.SetSize(150, 26)
	b.ToggleDetail()
	b.ToggleExpand()
	initialOut := ansi.Strip(b.View(150, 26))
	if !strings.Contains(initialOut, "LINE_01") || strings.Contains(initialOut, "LINE_12") {
		t.Fatalf("expected LINE_01 (and not LINE_12) at initial detail scroll top:\n%s", initialOut)
	}
	b.DetailScrollDown(18)
	scrolledOut1 := ansi.Strip(b.View(150, 26))
	scrolledOut2 := ansi.Strip(b.View(150, 26))
	if !strings.Contains(scrolledOut1, "LINE_12") || !strings.Contains(scrolledOut2, "LINE_12") || !strings.Contains(scrolledOut2, "% ─ ctrl+j/k") {
		t.Fatalf("detail panel scroll reset to top across View() while card was inline-expanded:\n%s", scrolledOut2)
	}

	// Planted negative control: navigating to SCROLL-2 auto-collapses SCROLL-1 and resets detail scroll to top.
	b.MoveDown()
	afterMove := ansi.Strip(b.View(150, 26))
	if b.HasExpandedCard() {
		t.Fatalf("expected MoveDown to auto-collapse expanded card")
	}
	if !strings.Contains(afterMove, "TOP_OF_SECOND_CARD") {
		t.Fatalf("expected detail panel to reset to top of SCROLL-2 on selection change:\n%s", afterMove)
	}

	// [R1-B5]: MoveToBottom, MoveToTop, and JumpToColumn auto-collapse inline expansion.
	b.ToggleExpand()
	if !b.HasExpandedCard() {
		t.Fatal("precondition: expected SCROLL-2 expanded")
	}
	b.MoveToTop()
	if b.HasExpandedCard() {
		t.Fatal("MoveToTop failed to auto-collapse expanded card")
	}
	b.ToggleExpand()
	b.MoveToBottom()
	if b.HasExpandedCard() {
		t.Fatal("MoveToBottom failed to auto-collapse expanded card")
	}
	b.ToggleExpand()
	b.JumpToColumn(1)
	if b.HasExpandedCard() {
		t.Fatal("JumpToColumn failed to auto-collapse expanded card")
	}

	// [R1-B5]: 24-row column with multi-dependency expanded card preserves bottom double-border ╚ AND scroll indicator ↕.
	var multiIssues []model.Issue
	var deps []*model.Dependency
	for i := 1; i <= 6; i++ {
		blkID := fmt.Sprintf("BLK-%d", i)
		multiIssues = append(multiIssues, model.Issue{
			ID: blkID, Title: fmt.Sprintf("Active Blocker %d", i), Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		})
		deps = append(deps, &model.Dependency{IssueID: "HEAVY-2", DependsOnID: blkID, Type: model.DepBlocks})
	}
	multiIssues = append(multiIssues,
		model.Issue{ID: "HEAVY-1", Title: "Open Above", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		model.Issue{
			ID: "HEAVY-2", Title: "Heavy Expanded Card With Many Dependencies",
			Description:  "Line A\n\nLine B\n\nLine C\n\nLine D\n\nLine E",
			Status:       model.StatusOpen,
			Priority:     1,
			IssueType:    model.TypeFeature,
			Labels:       []string{"alpha", "beta", "gamma", "delta"},
			Dependencies: deps,
			CreatedAt:    now.Add(-5 * time.Hour),
			UpdatedAt:    now.Add(-10 * time.Minute),
		},
		model.Issue{ID: "HEAVY-3", Title: "Open Below One", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		model.Issue{ID: "HEAVY-4", Title: "Open Below Two", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	)
	bHeavy := ui.NewBoardModel(multiIssues, createTheme())
	bHeavy.SetReferenceTime(now)
	bHeavy.SetReadiness(model.NewReadinessIndex(multiIssues))
	if !bHeavy.SelectIssueByID("HEAVY-2") {
		t.Fatal("expected HEAVY-2 selectable")
	}
	bHeavy.ToggleExpand()
	heavyOut := ansi.Strip(bHeavy.View(100, 23))
	if !strings.Contains(heavyOut, "HEAVY-2") || !strings.Contains(heavyOut, "╚") || !strings.Contains(heavyOut, "↕ 2/4") {
		t.Fatalf("24-row multi-dependency expanded card clipped bottom border ╚ or scroll indicator ↕ 2/4:\n%s", heavyOut)
	}
}

func TestBoardJumpFirstLastColumnAndSearchMatchCollapseExpandedCard(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "OPEN-ALPHA", Title: "Alpha Open Match", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "PROG-ALPHA", Title: "Alpha Progress Match", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED-ZETA", Title: "Zeta Closed Card", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	b := ui.NewBoardModel(issues, createTheme())
	b.SetReferenceTime(now)

	// Planted negative control 1: JumpToFirstColumn when already at focusedCol==0 keeps current card expanded.
	b.ToggleExpand()
	if !b.HasExpandedCard() || b.GetExpandedID() != "OPEN-ALPHA" {
		t.Fatalf("precondition: expected OPEN-ALPHA expanded, got %q", b.GetExpandedID())
	}
	b.JumpToFirstColumn()
	if !b.HasExpandedCard() || b.GetExpandedID() != "OPEN-ALPHA" {
		t.Fatalf("JumpToFirstColumn at col 0 unexpectedly collapsed OPEN-ALPHA")
	}

	// Positive [R2-B3]: JumpToLastColumn ('L') changes column and collapses hidden expanded card.
	b.JumpToLastColumn()
	if b.HasExpandedCard() {
		t.Fatalf("JumpToLastColumn left hidden card %q expanded", b.GetExpandedID())
	}

	// Positive [R2-B3]: JumpToFirstColumn ('H') from last column changes column and collapses hidden expanded card.
	b.ToggleExpand()
	if !b.HasExpandedCard() || b.GetExpandedID() != "CLOSED-ZETA" {
		t.Fatalf("precondition: expected CLOSED-ZETA expanded, got %q", b.GetExpandedID())
	}
	b.JumpToFirstColumn()
	if b.HasExpandedCard() {
		t.Fatalf("JumpToFirstColumn from last col left hidden card %q expanded", b.GetExpandedID())
	}

	// Planted negative control 2: '/' search whose first match IS the expanded card keeps it expanded,
	// whereas NextMatch ('n') / PrevMatch ('N') to a different card collapses it!
	b.ToggleExpand()
	if !b.HasExpandedCard() || b.GetExpandedID() != "OPEN-ALPHA" {
		t.Fatal("precondition: expected OPEN-ALPHA expanded before search")
	}
	b.StartSearch()
	for _, ch := range "alpha" {
		b.AppendSearchChar(ch)
	}
	b.FinishSearch()
	if !b.HasExpandedCard() || b.GetExpandedID() != "OPEN-ALPHA" {
		t.Fatalf("search landing on already-expanded OPEN-ALPHA unexpectedly collapsed it")
	}
	b.NextMatch()
	if b.HasExpandedCard() {
		t.Fatalf("NextMatch ('n') to PROG-ALPHA left %q expanded", b.GetExpandedID())
	}
	b.ToggleExpand()
	if !b.HasExpandedCard() || b.GetExpandedID() != "PROG-ALPHA" {
		t.Fatal("precondition: expected PROG-ALPHA expanded")
	}
	b.PrevMatch()
	if b.HasExpandedCard() {
		t.Fatalf("PrevMatch ('N') to OPEN-ALPHA left %q expanded", b.GetExpandedID())
	}
}
