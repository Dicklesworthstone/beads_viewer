package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newTestTheme() Theme {
	return DefaultTheme(lipgloss.NewRenderer(nil))
}

func TestActionableRenderEmpty(t *testing.T) {
	m := NewActionableModel(analysis.ExecutionPlan{}, newTestTheme())
	m.SetSize(80, 20)

	out := m.Render()
	if !strings.Contains(out, "No actionable items") {
		t.Fatalf("expected empty state message, got:\n%s", out)
	}
}

func TestActionableNavigationAcrossTracks(t *testing.T) {
	plan := analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{
			{TrackID: "track-A", Items: []analysis.PlanItem{{ID: "A1", Title: "First"}}},
			{TrackID: "track-B", Items: []analysis.PlanItem{{ID: "B1", Title: "Second"}}},
		},
	}

	m := NewActionableModel(plan, newTestTheme())
	m.SetSize(80, 20)

	if got := m.SelectedIssueID(); got != "A1" {
		t.Fatalf("expected initial selection A1, got %s", got)
	}

	m.MoveDown() // should move to next track/item
	if got := m.SelectedIssueID(); got != "B1" {
		t.Fatalf("expected selection B1 after MoveDown, got %s", got)
	}

	m.MoveUp()
	if got := m.SelectedIssueID(); got != "A1" {
		t.Fatalf("expected selection back to A1 after MoveUp, got %s", got)
	}
}

func TestActionableRenderShowsSummary(t *testing.T) {
	plan := analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{
			{
				TrackID: "track-A",
				Items:   []analysis.PlanItem{{ID: "ROOT", Title: "Root", Priority: 1, UnblocksIDs: []string{"X", "Y"}}},
			},
		},
		Summary: analysis.PlanSummary{
			HighestImpact: "ROOT",
			ImpactReason:  "Unblocks multiple tasks",
			UnblocksCount: 2,
		},
	}

	m := NewActionableModel(plan, newTestTheme())
	m.SetSize(100, 30)

	out := m.Render()
	if !strings.Contains(out, "Start with ROOT") {
		t.Fatalf("expected summary callout for ROOT, got:\n%s", out)
	}
	if !strings.Contains(out, "→2") {
		t.Fatalf("expected unblocks count badge, got:\n%s", out)
	}
}

// newFilteredListModel builds a split-view model with 80 issues whose fuzzy
// list filter "zebra" leaves only issues 72-77 visible, so Items() indexes and
// VisibleItems() indexes disagree (GH #210).
func newFilteredListModel(t *testing.T) *Model {
	t.Helper()
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	issues := make([]model.Issue, 80)
	for i := range issues {
		title := fmt.Sprintf("Row %04d", i)
		if i >= 72 && i <= 77 {
			title = fmt.Sprintf("zebra %04d", i)
		}
		issues[i] = model.Issue{ID: fmt.Sprintf("row-%04d", i), Title: title, Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now}
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	if !m.isSplitView {
		t.Fatal("expected split view at width 140")
	}
	m.list.SetFilterText("zebra")
	if got := len(m.list.VisibleItems()); got != 6 {
		t.Fatalf("visible items=%d, want 6", got)
	}
	if got := len(m.list.Items()); got != 80 {
		t.Fatalf("items=%d, want 80", got)
	}
	return m
}

func selectedListID(t *testing.T, m *Model) string {
	t.Helper()
	item, ok := m.list.SelectedItem().(IssueItem)
	if !ok {
		t.Fatalf("no issue selected (index=%d, page=%d, perPage=%d, visible=%d)",
			m.list.Index(), m.list.Paginator.Page, m.list.Paginator.PerPage, len(m.list.VisibleItems()))
	}
	return item.Issue.ID
}

func TestActionableEnterWithFilteredListDoesNotPanic(t *testing.T) {
	m := newFilteredListModel(t)
	m.actionableView = NewActionableModel(analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{{TrackID: "t", Items: []analysis.PlanItem{{ID: "row-0077", Title: "zebra 0077"}}}},
	}, newTestTheme())
	m.isActionableView = true
	m.handleActionableKeys(keyMsg("enter"))
	_ = m.View()
	if got := selectedListID(t, m); got != "row-0077" {
		t.Fatalf("selected %s, want row-0077", got)
	}
}

func TestActionableEnterClearsFuzzyFilterHidingTheTarget(t *testing.T) {
	m := newFilteredListModel(t)
	m.actionableView = NewActionableModel(analysis.ExecutionPlan{
		Tracks: []analysis.ExecutionTrack{{TrackID: "t", Items: []analysis.PlanItem{{ID: "row-0010", Title: "Row 0010"}}}},
	}, newTestTheme())
	m.isActionableView = true
	m.handleActionableKeys(keyMsg("enter"))
	_ = m.View()
	if m.list.FilterState() != list.Unfiltered {
		t.Fatalf("filter state=%v, want unfiltered after jumping to a filtered-out issue", m.list.FilterState())
	}
	if got := selectedListID(t, m); got != "row-0010" {
		t.Fatalf("selected %s, want row-0010", got)
	}
}

func TestJumpToIssueOutsideListKeepsSelection(t *testing.T) {
	m := newFilteredListModel(t)
	m.list.Select(2)
	if m.selectListIssueByID("no-such-issue") {
		t.Fatal("selected an issue that is not in the list")
	}
	if m.list.FilterState() == list.Unfiltered {
		t.Fatal("cleared the fuzzy filter for an issue that is not in the list")
	}
	if got := selectedListID(t, m); got != "row-0074" {
		t.Fatalf("selected %s, want row-0074", got)
	}
}

func TestListNavigationWithFilteredListStaysInBounds(t *testing.T) {
	// Wide split view, narrow list-only view, and a short terminal whose
	// pages hold fewer rows than the filter leaves visible.
	for _, size := range []struct{ width, height int }{{140, 45}, {60, 45}, {140, 12}, {60, 10}} {
		for _, key := range []string{"G", "end", "ctrl+d", "wheel"} {
			t.Run(fmt.Sprintf("%s/%dx%d", key, size.width, size.height), func(t *testing.T) {
				m := newFilteredListModel(t)
				m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				m.focused = focusList
				m.list.Select(5)
				switch key {
				case "wheel":
					m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
				case "end":
					m.Update(tea.KeyMsg{Type: tea.KeyEnd})
				case "ctrl+d":
					m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
				default:
					m.Update(keyMsg(key))
				}
				_ = m.View()
				if got := selectedListID(t, m); got != "row-0077" {
					t.Fatalf("selected %s, want row-0077", got)
				}
			})
		}
	}
}

func TestListViewInBoundsClampsStalePage(t *testing.T) {
	m := newFilteredListModel(t)
	// The state GH #210 reached: a page past the six visible rows.
	m.list.Select(77)
	out := m.listViewInBounds()
	if !strings.Contains(out, "zebra 0077") {
		t.Fatalf("clamped list view does not show the last visible row:\n%s", out)
	}
	_ = m.View()
}
