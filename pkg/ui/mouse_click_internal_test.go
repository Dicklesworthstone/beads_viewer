package ui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// leftClick builds a left-button press MouseMsg at (x, y), matching what
// bubbletea delivers under WithMouseCellMotion when the user clicks.
func leftClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{
		X:      x,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
}

func mouseTestIssues(n int) []model.Issue {
	issues := make([]model.Issue, 0, n)
	for i := 0; i < n; i++ {
		issues = append(issues, model.Issue{
			ID:       fmt.Sprintf("bv-%03d", i),
			Title:    fmt.Sprintf("Issue number %d", i),
			Status:   model.StatusOpen,
			Priority: 1,
		})
	}
	return issues
}

// sizedModel returns a Model that has processed a WindowSizeMsg so the list
// dimensions / pagination are initialized exactly as they would be at runtime.
func sizedModel(t *testing.T, issues []model.Issue, width, height int) *Model {
	t.Helper()
	m := NewModel(issues, nil, "")
	if m.analysis != nil {
		m.analysis.WaitForPhase2()
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*Model)
}

// TestLeftClickSelectsListRowInSplitView verifies that a left-click inside the
// list panel of the split view (a) keeps/sets focus on the list and (b) selects
// the row under the cursor. width=120 (>SplitViewThreshold=100) forces split.
func TestLeftClickSelectsListRowInSplitView(t *testing.T) {
	m := sizedModel(t, mouseTestIssues(30), 120, 30)

	if !m.isSplitView {
		t.Fatalf("expected split view at width 120, got isSplitView=false")
	}

	// Sanity: start on row 0.
	m.list.Select(0)
	if got := m.list.Index(); got != 0 {
		t.Fatalf("setup: expected list index 0, got %d", got)
	}

	// The list panel occupies x in [0, listInnerWidth+4). The lines above the
	// first row are border + header + filter bar (listChromeLines()), so the
	// first row is at y == listChromeLines(). Click row index 3.
	x := 5 // well inside the left panel
	chrome := m.listChromeLines()
	clickedRow := 3
	updated := m.handleLeftClick(x, chrome+clickedRow)

	if updated.focused != focusList {
		t.Errorf("after click in list panel, focused = %v, want focusList", updated.focused)
	}
	if got := updated.list.Index(); got != clickedRow {
		t.Errorf("after click on row %d, list index = %d, want %d", clickedRow, got, clickedRow)
	}
}

// TestLeftClickFocusesDetailPanel verifies a click on the right (detail) side of
// the split view moves focus to the detail pane without changing selection.
func TestLeftClickFocusesDetailPanel(t *testing.T) {
	m := sizedModel(t, mouseTestIssues(30), 120, 30)
	if !m.isSplitView {
		t.Fatalf("expected split view at width 120")
	}
	m.list.Select(4)
	m.focused = focusList

	// listInnerWidth+4 is the panel boundary; click past it (far right).
	x := m.list.Width() + 4 + 10
	updated := m.handleLeftClick(x, 5)

	if updated.focused != focusDetail {
		t.Errorf("after click in detail panel, focused = %v, want focusDetail", updated.focused)
	}
	if got := updated.list.Index(); got != 4 {
		t.Errorf("detail-panel click must not change list selection: index = %d, want 4", got)
	}
}

// TestLeftClickSelectsListRowMobile verifies single-column (mobile) list view:
// no border, so y0=header and y1=first row.
func TestLeftClickSelectsListRowMobile(t *testing.T) {
	// width below SplitViewThreshold => mobile layout.
	m := sizedModel(t, mouseTestIssues(30), 80, 30)
	if m.isSplitView {
		t.Fatalf("expected mobile (non-split) view at width 80")
	}
	m.showDetails = false
	m.list.Select(0)

	// Mobile (no border): lines above first row are header + filter bar
	// (listChromeLines()), so the first row is at y == listChromeLines().
	chrome := m.listChromeLines()
	clickedRow := 2
	updated := m.handleLeftClick(3, chrome+clickedRow)

	if updated.focused != focusList {
		t.Errorf("after mobile list click, focused = %v, want focusList", updated.focused)
	}
	if got := updated.list.Index(); got != clickedRow {
		t.Errorf("after mobile click on row %d, index = %d, want %d", clickedRow, got, clickedRow)
	}
}

// TestLeftClickHeaderRowIsNoop verifies clicking the column-header row (y==1 in
// split view) does not select a phantom row.
func TestLeftClickHeaderRowIsNoop(t *testing.T) {
	m := sizedModel(t, mouseTestIssues(30), 120, 30)
	m.list.Select(7)
	m.focused = focusList

	// y==1 falls within the chrome (border/header/filter bar) above the first
	// row -> rowOffset 1-listChromeLines() < 0 -> ignored.
	updated := m.handleLeftClick(5, 1)
	if got := updated.list.Index(); got != 7 {
		t.Errorf("click on header row changed selection: index = %d, want 7", got)
	}
	if updated.focused != focusList {
		t.Errorf("focus should remain on list panel, got %v", updated.focused)
	}
}

// TestLeftClickOutOfRangeRowIgnored verifies clicking below the last item (in
// the empty padding / page-indicator region) does not move the selection.
func TestLeftClickOutOfRangeRowIgnored(t *testing.T) {
	// Only 3 items but a tall list -> rows 3..N are empty padding.
	m := sizedModel(t, mouseTestIssues(3), 120, 40)
	m.list.Select(1)
	m.focused = focusList

	// Click far down where no item exists (well past the 3 real rows).
	updated := m.handleLeftClick(5, m.listChromeLines()+20)
	if got := updated.list.Index(); got != 1 {
		t.Errorf("out-of-range click changed selection: index = %d, want 1", got)
	}
}

// TestLeftClickNoopInFullScreenView verifies that clicks in a full-screen
// single-panel view (board) are a no-op and don't panic.
func TestLeftClickNoopInFullScreenView(t *testing.T) {
	m := sizedModel(t, mouseTestIssues(10), 120, 30)
	// Enter board view.
	updated, _ := m.Update(keyMsgRune('b'))
	m = updated.(*Model)
	if !m.isBoardView {
		t.Fatalf("expected board view after 'b'")
	}

	before := m.focused
	after := m.handleLeftClick(5, 5)
	if after.focused != before {
		t.Errorf("board click changed focus: got %v, want %v", after.focused, before)
	}
}

// TestMouseLeftClickThroughUpdate exercises the full Update() path with a
// MouseMsg to ensure the new case is wired in and motion events are ignored.
func TestMouseLeftClickThroughUpdate(t *testing.T) {
	m := sizedModel(t, mouseTestIssues(30), 120, 30)
	m.list.Select(0)

	// A motion event (no button) must NOT change selection.
	motion := tea.MouseMsg{X: 5, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
	updated, _ := m.Update(motion)
	m = updated.(*Model)
	if m.list.Index() != 0 {
		t.Errorf("motion event changed selection to %d, want 0", m.list.Index())
	}

	// A genuine press selects the row under the cursor. The first row sits at
	// y == listChromeLines(); row 5 is that plus 5.
	updated, _ = m.Update(leftClick(5, m.listChromeLines()+5))
	m = updated.(*Model)
	if m.list.Index() != 5 {
		t.Errorf("press event: index = %d, want 5", m.list.Index())
	}
}

func keyMsgRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}
