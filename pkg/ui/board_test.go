package ui_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"

	"github.com/charmbracelet/lipgloss"
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

// TestAdaptiveColumns verifies that only non-empty columns are navigable
func TestAdaptiveColumns(t *testing.T) {
	theme := createTheme()

	// Create issues only in Open and Closed columns (skip InProgress and Blocked)
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 1, CreatedAt: createTime(0)},
		{ID: "2", Status: model.StatusOpen, Priority: 2, CreatedAt: createTime(1)},
		{ID: "3", Status: model.StatusClosed, Priority: 1, CreatedAt: createTime(2)},
	}

	b := ui.NewBoardModel(issues, theme)

	// Should start on first non-empty column (Open)
	sel := b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 (Open col), got %v", sel)
	}

	// MoveRight should skip InProgress and Blocked (empty), go to Closed
	b.MoveRight()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected ID 3 (Closed col) after MoveRight, got %v", sel)
	}

	// MoveRight again should stay on Closed (last non-empty column)
	b.MoveRight()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "3" {
		t.Errorf("Expected to stay on ID 3, got %v", sel)
	}

	// MoveLeft should go back to Open (skipping empty columns)
	b.MoveLeft()
	sel = b.SelectedIssue()
	if sel == nil || sel.ID != "1" {
		t.Errorf("Expected ID 1 (Open col) after MoveLeft, got %v", sel)
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
