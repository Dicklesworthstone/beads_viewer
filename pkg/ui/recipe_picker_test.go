package ui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestRecipePickerSelection(t *testing.T) {
	recipes := []recipe.Recipe{
		{Name: "Triage", Description: "Focus on blockers"},
		{Name: "Release", Description: "Prep for release"},
		{Name: "Cleanup", Description: "Debt sweep"},
	}

	m := NewRecipePickerModel(recipes, DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(80, 24)

	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Triage" {
		t.Fatalf("expected initial selection Triage, got %+v", sel)
	}

	m.MoveDown()
	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Release" {
		t.Fatalf("expected selection Release after MoveDown, got %+v", sel)
	}

	m.MoveUp()
	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Triage" {
		t.Fatalf("expected back to Triage after MoveUp, got %+v", sel)
	}
}

func TestRecipePickerViewContainsNames(t *testing.T) {
	recipes := []recipe.Recipe{
		{Name: "Alpha", Description: "First"},
	}
	m := NewRecipePickerModel(recipes, DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(60, 20)

	out := m.View()
	if !strings.Contains(out, "Alpha") {
		t.Fatalf("expected view to contain recipe name, got:\n%s", out)
	}
	if !strings.Contains(out, "Select Recipe") {
		t.Fatalf("expected view title, got:\n%s", out)
	}
}

func TestFormatRecipeInfo(t *testing.T) {
	if got := FormatRecipeInfo(nil); got != "" {
		t.Fatalf("expected empty string for nil recipe, got %q", got)
	}
	r := recipe.Recipe{Name: "Demo"}
	if got := FormatRecipeInfo(&r); got != "Recipe: Demo" {
		t.Fatalf("unexpected format: %s", got)
	}
}

func recipePresentationIssues() []model.Issue {
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return []model.Issue{
		{ID: "view-1", Title: "界界界界界 Alpha", Description: "First issue details", Status: model.StatusOpen, Priority: 1, Labels: []string{"zeta", "alpha"}, CreatedAt: created, UpdatedAt: created.Add(24 * time.Hour)},
		{ID: "view-2", Title: "Beta searchable", Description: "Second issue details", Status: model.StatusClosed, Priority: 2, Labels: []string{"beta"}, CreatedAt: created, UpdatedAt: created.Add(48 * time.Hour)},
		{ID: "view-3", Title: "Gamma searchable", Description: "Third issue details", Status: model.StatusOpen, Priority: 1, CreatedAt: created, UpdatedAt: created.Add(72 * time.Hour), Dependencies: []*model.Dependency{{IssueID: "view-3", DependsOnID: "view-1", Type: model.DepBlocks}}},
	}
}

func newRecipePresentationModel(t *testing.T, r *recipe.Recipe) *Model {
	t.Helper()
	m := NewModel(recipePresentationIssues(), r, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	m.Update(WaitForPhase2Cmd(m.analysis)())
	return m
}

// This control uses only the pre-existing Recipe/Model surface, so it can be
// replayed unchanged against the version that accepted but ignored view fields.
func TestRecipePresentationObservable(t *testing.T) {
	r := &recipe.Recipe{Name: "presentation", View: recipe.ViewConfig{Columns: []string{"title"}, TruncateTitle: 5}}
	m := newRecipePresentationModel(t, r)
	row := m.list.View()
	if !strings.Contains(row, "界界…") || strings.Contains(row, "界界界界界 Alpha") || strings.Contains(row, "view-1") {
		t.Fatalf("configured title-only row with five display cells was not applied:\n%s", row)
	}
	if !strings.Contains(m.View(), "界界…") {
		t.Fatalf("actual model view lost configured list row:\n%s", m.View())
	}
	m.setActiveRecipe(nil)
	m.currentFilter = "all"
	m.applyFilter()
	if !strings.Contains(m.list.View(), "view-1") {
		t.Fatal("clearing recipe did not restore the ordinary issue row")
	}
}

func TestRecipePresentationGroupingNavigationAndReload(t *testing.T) {
	for _, group := range []struct{ by, first string }{{"status", "closed"}, {"priority", "P1"}, {"tag", "alpha"}} {
		t.Run(group.by, func(t *testing.T) {
			r := &recipe.Recipe{Name: "grouped", Sort: recipe.SortConfig{Field: "id"}, View: recipe.ViewConfig{GroupBy: group.by, Collapsed: true}}
			m := newRecipePresentationModel(t, r)
			header, ok := m.list.SelectedItem().(IssueGroupItem)
			if !ok || header.Key != group.first || !header.Collapsed {
				t.Fatalf("initial collapsed group = %#v", m.list.SelectedItem())
			}
			if !strings.Contains(m.View(), group.first) || !strings.Contains(m.viewport.View(), "Enter") {
				t.Fatal("group label or navigation instruction absent from actual view")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.Update(tea.KeyMsg{Type: tea.KeyDown})
			selected, ok := m.list.SelectedItem().(IssueItem)
			if !ok {
				t.Fatalf("Enter/Down did not reach a group issue: %#v", m.list.SelectedItem())
			}
			m.updateViewportContent()
			if !strings.Contains(ansi.Strip(m.viewport.View()), selected.Issue.Title) {
				t.Fatalf("expanded issue details are inaccessible: want %q, got:\n%s", selected.Issue.Title, ansi.Strip(m.viewport.View()))
			}
			// A real background-built snapshot updates rows without resetting the
			// user's expanded group or the selected issue's identity.
			issues := recipePresentationIssues()
			for i := range issues {
				issues[i].Description += " Reloaded"
			}
			snapshot := NewSnapshotBuilder(issues).WithRecipe(r).Build()
			snapshot.Analysis.WaitForPhase2()
			m.Update(SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 1})
			if got, ok := m.list.SelectedItem().(IssueItem); !ok || got.Issue.ID != selected.Issue.ID || m.recipeCollapsed[group.first] {
				t.Fatalf("reload lost selection/collapse: selected=%#v groups=%v", m.list.SelectedItem(), m.recipeCollapsed)
			}
			m.Update(tea.KeyMsg{Type: tea.KeyUp})
			m.Update(tea.KeyMsg{Type: tea.KeySpace})
			if got, ok := m.list.SelectedItem().(IssueGroupItem); !ok || !got.Collapsed {
				t.Fatal("Space did not collapse the group")
			}
		})
	}
}

func TestRecipePresentationSearchIncludesCollapsedRows(t *testing.T) {
	r := &recipe.Recipe{Name: "collapsed", View: recipe.ViewConfig{GroupBy: "status", Collapsed: true}}
	m := newRecipePresentationModel(t, r)
	m.semanticSearchEnabled = false
	m.list.Filter = list.DefaultFilter
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if m.list.FilterState() != list.Filtering || len(m.list.Items()) != 3 {
		t.Fatalf("search did not expose all recipe rows: state=%v items=%d", m.list.FilterState(), len(m.list.Items()))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Beta")})
	var deliver func(tea.Cmd)
	deliver = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				deliver(child)
			}
			return
		}
		m.Update(msg)
	}
	deliver(cmd)
	visible := m.list.VisibleItems()
	if len(visible) != 1 || visible[0].(IssueItem).Issue.ID != "view-2" {
		t.Fatalf("collapsed Beta issue not found: %#v", visible)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.list.FilterState() != list.Unfiltered {
		t.Fatal("Escape did not clear search")
	}
	for _, raw := range m.list.Items() {
		if header, ok := raw.(IssueGroupItem); !ok || !header.Collapsed {
			t.Fatalf("search exit lost collapsed presentation: %#v", raw)
		}
	}
}

func TestRecipePresentationGraphOwnershipAndPicker(t *testing.T) {
	r := &recipe.Recipe{Name: "graph", View: recipe.ViewConfig{ShowGraph: true}}
	m := newRecipePresentationModel(t, r)
	if !m.isGraphView || m.focused != focusGraph {
		t.Fatal("show_graph did not select the graph")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !m.isBoardView || m.isGraphView {
		t.Fatal("user could not navigate from recipe graph to board")
	}
	m.Update(WaitForPhase2Cmd(m.analysis)())
	snapshot := NewSnapshotBuilder(recipePresentationIssues()).WithRecipe(r).Build()
	m.Update(SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 1})
	if !m.isBoardView || m.focused != focusBoard || m.isGraphView {
		t.Fatal("background completion overrode deliberate navigation")
	}
	m.setActiveRecipe(nil)
	m.applyFilter()
	if !m.isBoardView {
		t.Fatal("clearing recipe reset user-owned board navigation")
	}
	m.recipePicker = NewRecipePickerModel([]recipe.Recipe{*r}, m.theme)
	m.showRecipePicker, m.focused = true, focusRecipePicker
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.isGraphView || m.focused != focusGraph || m.showRecipePicker {
		t.Fatal("picker did not transfer focus to recipe graph")
	}
	m.setActiveRecipe(nil)
	m.applyFilter()
	if m.isGraphView || m.focused != focusList {
		t.Fatal("clearing recipe left recipe-owned graph active")
	}
}

func TestRecipePresentationSharedMembershipAndSort(t *testing.T) {
	r := &recipe.Recipe{Name: "combined", Filters: recipe.FilterConfig{ExcludeTags: []string{"beta"}, CreatedAfter: "2026-01-01", UpdatedBefore: "2026-01-05", IDPrefix: "view-", TitleContains: "a"}, Sort: recipe.SortConfig{Field: "priority", Secondary: &recipe.SortConfig{Field: "updated", Direction: "desc"}}, View: recipe.ViewConfig{MaxItems: 1}}
	m := newRecipePresentationModel(t, r)
	if len(m.issues) != 3 || len(m.FilteredIssues()) != 1 || m.FilteredIssues()[0].ID != "view-3" {
		t.Fatalf("recipe must retain source and apply all predicates/sort before limit: %#v", m.FilteredIssues())
	}
	snapshot := NewSnapshotBuilder(recipePresentationIssues()).WithRecipe(r).Build()
	m.Update(SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 1})
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "view-3" {
		t.Fatalf("snapshot membership differs: %#v", got)
	}
	m.setActiveRecipe(nil)
	m.currentFilter = "all"
	m.applyFilter()
	if len(m.FilteredIssues()) != 3 {
		t.Fatal("recipe switch could not recover excluded rows")
	}
	r.Filters.IDPrefix = "absent-"
	m.setActiveRecipe(r)
	m.applyRecipe(r)
	if len(m.list.Items()) != 0 || m.View() == "" {
		t.Fatal("empty recipe result is not usable")
	}
}

func TestRecipePresentationMetrics(t *testing.T) {
	for _, name := range recipe.ViewMetrics {
		t.Run(name, func(t *testing.T) {
			r := &recipe.Recipe{Name: "metrics", Metrics: []string{name}, View: recipe.ViewConfig{Columns: []string{"id"}}}
			m := newRecipePresentationModel(t, r)
			if !strings.Contains(m.list.View(), name+":") || !strings.Contains(m.viewport.View(), name+":") {
				t.Fatalf("metric %s absent from settled rows/details:\n%s\n%s", name, m.list.View(), m.viewport.View())
			}
			if _, ok := m.recipeMetricValues[name]["view-1"]; !ok {
				t.Fatalf("settled %s has no value for open issue; values=%v status=%+v", name, m.recipeMetricValues, m.analysis.Status())
			}
		})
	}
	m := newRecipePresentationModel(t, &recipe.Recipe{Name: "default metrics", View: recipe.ViewConfig{ShowMetrics: true}})
	if got := recipeMetricNames(m.activeRecipe); !reflect.DeepEqual(got, []string{"pagerank", "impact", "triage"}) {
		t.Fatalf("default metrics=%v", got)
	}
	for _, name := range recipeMetricNames(m.activeRecipe) {
		if !strings.Contains(m.viewport.View(), name+":") {
			t.Fatalf("default metric %s not visible in details", name)
		}
	}
}

func TestRecipePresentationCombinedGraphToCollapsedDetail(t *testing.T) {
	r := &recipe.Recipe{Name: "combined", View: recipe.ViewConfig{Columns: []string{"id", "title", "blockers"}, ShowGraph: true, ShowMetrics: true, GroupBy: "status", Collapsed: true, MaxItems: 2, TruncateTitle: 5}, Metrics: []string{"pagerank", "betweenness"}, Sort: recipe.SortConfig{Field: "id"}}
	m := newRecipePresentationModel(t, r)
	selected := m.graphView.SelectedIssue()
	if selected == nil {
		t.Fatal("configured graph contains no selectable issue")
	}
	id := selected.ID
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	item, ok := m.list.SelectedItem().(IssueItem)
	if !ok || item.Issue.ID != id || !strings.Contains(ansi.Strip(m.viewport.View()), item.Issue.Title) {
		t.Fatalf("graph jump failed to reveal selected issue in collapsed group: wanted %s, got %#v", id, m.list.SelectedItem())
	}
	m.Update(tea.WindowSizeMsg{Width: 36, Height: 16})
	if m.View() == "" || len(m.recipeListItems) != 2 {
		t.Fatal("combined narrow view lost recipe rows")
	}
}

func TestRecipePresentationCollapsedCrossViewNavigation(t *testing.T) {
	for _, collapsed := range []bool{false, true} {
		for _, view := range []string{"b", "a", "E"} {
			t.Run(view+map[bool]string{false: "/expanded", true: "/collapsed"}[collapsed], func(t *testing.T) {
				issues := []model.Issue{{ID: "alpha", Title: "Reach the exact selected issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}, {ID: "beta", Title: "Closed separate issue", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask}}
				r := &recipe.Recipe{Name: "groups", View: recipe.ViewConfig{GroupBy: "status", Collapsed: collapsed}}
				m := NewModel(issues, r, "")
				defer m.Stop()
				m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
				m.Update(WaitForPhase2Cmd(m.analysis)())
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(view)})
				var selectedID string
				jump := tea.KeyMsg{Type: tea.KeyEnter}
				switch view {
				case "b":
					if issue := m.board.SelectedIssue(); issue != nil {
						selectedID = issue.ID
					}
				case "a":
					selectedID = m.actionableView.SelectedIssueID()
				case "E":
					if issue := m.tree.SelectedIssue(); issue != nil {
						selectedID = issue.ID
					}
					jump = tea.KeyMsg{Type: tea.KeyTab}
				}
				if selectedID == "" {
					t.Fatal("positive control has no selectable source issue")
				}
				m.Update(jump)
				item, ok := m.list.SelectedItem().(IssueItem)
				if !ok || item.Issue.ID != selectedID || !strings.Contains(ansi.Strip(m.viewport.View()), item.Issue.Title) {
					t.Fatalf("view %s selected %s, but detail jump landed on %#v; viewport=%s", view, selectedID, m.list.SelectedItem(), ansi.Strip(m.viewport.View()))
				}
			})
		}
	}
}
