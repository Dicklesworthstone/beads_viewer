package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// White-box testing of UI model logic

func TestComputeAlertsPreservesSourceAuthorityScopeAndClock(t *testing.T) {
	t.Chdir(t.TempDir())
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		name       string
		hidden     bool
		visible    bool
		deferred   bool
		candidates map[string]bool
		now        time.Time
		wantGate   bool
	}{
		{"hidden-tombstone", true, false, false, nil, now, true},
		{"missing-predecessor", false, false, false, nil, now, false},
		{"excluded-candidate", false, true, false, map[string]bool{}, now, false},
		{"future-deferral", false, true, true, nil, now, false},
		{"deferral-boundary", false, true, true, nil, future, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := []model.Issue{{ID: "gate", Status: model.StatusOpen, Priority: 1, UpdatedAt: now,
				Dependencies: []*model.Dependency{{DependsOnID: "hidden", Type: model.DepBlocks}}}}
			if tc.deferred {
				issues[0].DeferUntil = &future
			}
			for _, id := range []string{"third", "first", "second"} {
				issues = append(issues, model.Issue{ID: id, Status: model.StatusOpen, Priority: 0, UpdatedAt: now,
					Dependencies: []*model.Dependency{{DependsOnID: "gate", Type: model.DepBlocks}}})
			}
			if tc.visible {
				issues = append(issues, model.Issue{ID: "hidden", Status: model.StatusClosed, UpdatedAt: now})
			}
			authorityIssues := append([]model.Issue(nil), issues...)
			if tc.hidden {
				authorityIssues = append(authorityIssues, model.Issue{ID: "hidden", Status: model.StatusTombstone})
			}
			a := analysis.NewAnalyzer(issues)
			a.SetReadinessScope(model.NewReadinessIndex(authorityIssues), tc.candidates)
			a.SetNow(tc.now)
			stats := a.AnalyzeWithConfig(analysis.AnalysisConfig{})
			alerts, _, _, _ := computeAlerts(issues, &stats, a)
			gate := false
			for _, alert := range alerts {
				if alert.Type == drift.AlertBlockingCascade && alert.IssueID == "gate" {
					gate = true
					if alert.UnblocksCount != 3 {
						t.Errorf("gate unblocks=%d, want 3", alert.UnblocksCount)
					}
				}
				if alert.Type == drift.AlertStaleIssue {
					t.Errorf("fresh issue became stale at source clock %v: %#v", tc.now, alert)
				}
				if !alert.DetectedAt.Equal(tc.now) {
					t.Errorf("alert clock=%v, want source %v", alert.DetectedAt, tc.now)
				}
			}
			if gate != tc.wantGate {
				t.Errorf("gate blocking-cascade alert=%v, want %v", gate, tc.wantGate)
			}
		})
	}
}

func TestApplyRecipe_StatusFilter(t *testing.T) {
	issues := []model.Issue{
		{ID: "open", Status: model.StatusOpen},
		{ID: "closed", Status: model.StatusClosed},
		{ID: "tombstone", Status: model.StatusTombstone},
		{ID: "blocked", Status: model.StatusBlocked},
	}
	m := NewModel(issues, nil, "")

	r := &recipe.Recipe{
		Name: "closed-only",
		Filters: recipe.FilterConfig{
			Status: []string{"closed"},
		},
	}

	m.applyRecipe(r)

	filtered := m.FilteredIssues()
	if len(filtered) != 1 {
		t.Fatalf("Expected one visible closed issue, got %d", len(filtered))
	}
	got := map[string]bool{}
	for _, iss := range filtered {
		got[iss.ID] = true
	}
	if !got["closed"] || got["tombstone"] {
		t.Errorf("Expected 'closed' only; deleted records must stay hidden, got %+v", got)
	}
}

func TestApplyRecipe_PriorityFilter(t *testing.T) {
	issues := []model.Issue{
		{ID: "p1", Status: model.StatusOpen, Priority: 1},
		{ID: "p2", Status: model.StatusOpen, Priority: 2},
	}
	m := NewModel(issues, nil, "")

	r := &recipe.Recipe{
		Filters: recipe.FilterConfig{
			Priority: []int{1},
		},
	}

	m.applyRecipe(r)

	filtered := m.FilteredIssues()
	if len(filtered) != 1 {
		t.Fatalf("Expected 1 issue, got %d", len(filtered))
	}
	if filtered[0].ID != "p1" {
		t.Errorf("Expected p1, got %s", filtered[0].ID)
	}
}

func TestApplyRecipe_ActionableFilter(t *testing.T) {
	// A blocks B. B is blocked. A is open.
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusBlocked, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}
	m := NewModel(issues, nil, "")

	yes := true
	r := &recipe.Recipe{
		Filters: recipe.FilterConfig{
			Actionable: &yes,
		},
	}

	m.applyRecipe(r)

	filtered := m.FilteredIssues()
	if len(filtered) != 1 {
		t.Fatalf("Expected 1 actionable issue, got %d", len(filtered))
	}
	if filtered[0].ID != "A" {
		t.Errorf("Expected A (actionable), got %s", filtered[0].ID)
	}
}

func TestNewModel_ScopedReadinessAuthority(t *testing.T) {
	full := []model.Issue{
		{ID: "closed-external", Status: model.StatusClosed},
		{ID: "open-external", Status: model.StatusOpen},
		{ID: "parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "closed-external", Type: model.DepBlocks}}},
		{ID: "allowed", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "parent", Type: model.DepParentChild}}},
		{ID: "blocked", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "open-external", Type: model.DepBlocks}}},
		{ID: "unknown", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "absent", Type: model.DepBlocks}}},
	}
	selected := full[3:]
	yes := true
	r := &recipe.Recipe{Name: "actionable", Filters: recipe.FilterConfig{Actionable: &yes}}
	m := NewModel(copyIssues(selected), r, "", ReadinessScope{Authority: model.NewReadinessIndex(full)})
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "allowed" {
		t.Errorf("initial actionable recipe = %v, want allowed only", got)
	}
	if m.countReady != 1 {
		t.Errorf("ready count = %d, want 1", m.countReady)
	}
	m.SetFilter("ready")
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "allowed" {
		t.Errorf("ready filter = %v, want allowed only", got)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*Model)
	plan := m.actionableView.plan
	if plan.TotalActionable != 1 || len(plan.Tracks) != 1 || len(plan.Tracks[0].Items) != 1 || plan.Tracks[0].Items[0].ID != "allowed" {
		t.Errorf("actionable panel lost full dependency authority: %+v", plan)
	}
}

func TestNewModel_LabelContextIsNotCandidateWork(t *testing.T) {
	issues := []model.Issue{
		{ID: "api-blocked", Status: model.StatusOpen, Labels: []string{"backend"}, Dependencies: []*model.Dependency{{DependsOnID: "web-ready", Type: model.DepBlocks}}},
		{ID: "web-ready", Status: model.StatusOpen, Labels: []string{"frontend"}},
		{ID: "api-ready", Status: model.StatusOpen, Labels: []string{"backend"}},
	}
	candidates := map[string]bool{"api-blocked": true, "api-ready": true}
	m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues), CandidateIDs: candidates})
	if m.countReady != 1 {
		t.Errorf("ready count includes graph context: %d, want 1", m.countReady)
	}
	if m.issueMap["web-ready"] == nil || len(m.FilteredIssues()) != 3 {
		t.Fatal("dependency context must remain available for exploration")
	}
	assertSelectedWork := func(t *testing.T, m *Model) {
		t.Helper()
		m.SetFilter("ready")
		if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "api-ready" {
			t.Errorf("ready filter included context: %v", got)
		}
		m.rebuildInsightsPanel()
		if got := m.insightsPanel.topPicks; len(got) != 1 || got[0].ID != "api-ready" {
			t.Errorf("triage top picks included context: %v", got)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		m = updated.(*Model)
		plan := m.actionableView.plan
		if plan.TotalActionable != 1 || len(plan.Tracks) != 1 || len(plan.Tracks[0].Items) != 1 || plan.Tracks[0].Items[0].ID != "api-ready" {
			t.Errorf("actionable panel included context: %+v", plan)
		}
		m.isActionableView = false
		m.focused = focusList
		yes := true
		r := &recipe.Recipe{Name: "actionable", Filters: recipe.FilterConfig{Actionable: &yes}}
		m.activeRecipe = r
		m.applyRecipe(r)
		if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "api-ready" {
			t.Errorf("actionable recipe included context: %v", got)
		}
		if got := m.filteredIssuesForActiveView(); len(got) != 1 || got[0].ID != "api-ready" {
			t.Errorf("actionable board/graph included context: %v", got)
		}
	}
	assertSelectedWork(t, m)
	// Mutating the caller's map must not widen the model's owned selection.
	candidates["web-ready"] = true
	assertSelectedWork(t, m)
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(issues)).WithRecipe(m.activeRecipe).Build()})
	m = updated.(*Model)
	if m.countReady != 1 {
		t.Errorf("reloaded ready count includes graph context: %d, want 1", m.countReady)
	}
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "api-ready" {
		t.Errorf("reloaded actionable recipe included context: %v", got)
	}
	assertSelectedWork(t, m)

	m.setActiveRecipe(nil)
	m.SetFilter("all")
	updated, _ = m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(issues)).Build()})
	m = updated.(*Model)
	if len(m.FilteredIssues()) != 3 {
		t.Fatal("reloaded graph context should remain available in the all view")
	}
	for _, row := range m.list.Items() {
		item := row.(IssueItem)
		if item.Issue.ID == "web-ready" && (item.IsQuickWin || item.TriageScore != 0 || len(item.TriageReasons) > 0) {
			t.Errorf("context retained recommendation badges from unscoped worker: %+v", item)
		}
	}
	if got := m.insightsPanel.topPicks; len(got) != 1 || got[0].ID != "api-ready" {
		t.Errorf("snapshot triage included context before phase 2: %v", got)
	}
	m.analysis.WaitForPhase2()
	updated, _ = m.Update(Phase2ReadyMsg{Stats: m.analysis, Insights: m.analysis.GenerateInsights(len(issues))})
	m = updated.(*Model)
	if got := m.insightsPanel.topPicks; len(got) != 1 || got[0].ID != "api-ready" {
		t.Errorf("phase 2 triage included context: %v", got)
	}
	for _, row := range m.list.Items() {
		item := row.(IssueItem)
		if item.Issue.ID == "web-ready" && (item.IsQuickWin || item.TriageScore != 0 || len(item.TriageReasons) > 0) {
			t.Errorf("phase 2 restored recommendation badges for context: %+v", item)
		}
	}

	reloaded := cloneIssuesForAsync(issues)
	for i := range reloaded {
		if reloaded[i].ID == "web-ready" {
			reloaded[i].Status = model.StatusClosed
		}
	}
	updated, _ = m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(reloaded).Build()})
	m = updated.(*Model)
	m.SetFilter("ready")
	if got := m.FilteredIssues(); len(got) != 2 || m.countReady != 2 {
		t.Errorf("freshly closed external blocker should permit both selected backend issues: ready=%d, rows=%v", m.countReady, got)
	}
}

func TestNewModel_EmptyReadinessSelectionDoesNotSelectContext(t *testing.T) {
	issues := []model.Issue{{ID: "context", Status: model.StatusOpen}}
	m := NewModel(copyIssues(issues), nil, "", ReadinessScope{CandidateIDs: map[string]bool{}})
	if len(m.FilteredIssues()) != 1 {
		t.Fatal("empty work selection must preserve exploration context")
	}
	m.SetFilter("ready")
	if len(m.FilteredIssues()) != 0 || m.countReady != 0 {
		t.Fatal("empty candidate map must select no ready work")
	}
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(issues)).Build()})
	m = updated.(*Model)
	if len(m.FilteredIssues()) != 0 || m.countReady != 0 || len(m.insightsPanel.topPicks) != 0 {
		t.Fatal("reload widened empty candidate selection")
	}
}

func TestNewModel_RepoSelectionSurvivesFullSourceReload(t *testing.T) {
	issues := []model.Issue{
		{ID: "api-ready", Status: model.StatusOpen, SourceRepo: "api"},
		{ID: "api-blocked", Status: model.StatusOpen, SourceRepo: "api", Dependencies: []*model.Dependency{{DependsOnID: "web-ready", Type: model.DepBlocks}}},
		{ID: "web-ready", Status: model.StatusOpen, SourceRepo: "web"},
		{ID: "ops-ready", Status: model.StatusOpen, SourceRepo: "ops"},
	}
	// --repo initially supplies only selected rows to NewModel, while its
	// watcher later reloads the complete source containing other repositories.
	selected := copyIssues(issues[:2])
	candidateIDs := map[string]bool{"api-ready": true, "api-blocked": true}
	m := NewModel(selected, nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues), CandidateIDs: candidateIDs})
	m.SetFilter("ready")
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "api-ready" {
		t.Fatalf("initial repository selection = %v, want api-ready", got)
	}
	for _, closeExternalBlocker := range []bool{false, true} {
		loaded := cloneIssuesForAsync(issues)
		wantReady := 1
		if closeExternalBlocker {
			loaded[2].Status = model.StatusClosed
			wantReady = 2
		}
		updated, _ := m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(loaded).Build()})
		m = updated.(*Model)
		if len(m.issues) != len(issues) {
			t.Fatal("regression must exercise a complete source reload")
		}
		got := m.FilteredIssues()
		if len(got) != wantReady || m.countReady != wantReady {
			t.Errorf("closed external blocker=%t: ready count=%d, rows=%v, want %d selected issues", closeExternalBlocker, m.countReady, got, wantReady)
		}
		for _, issue := range got {
			if !candidateIDs[issue.ID] {
				t.Errorf("external repository became ready work after reload: %s", issue.ID)
			}
		}
		if len(m.insightsPanel.topPicks) != wantReady {
			t.Errorf("closed external blocker=%t: triage picks=%v, want %d selected issues", closeExternalBlocker, m.insightsPanel.topPicks, wantReady)
		}
		for _, pick := range m.insightsPanel.topPicks {
			if !candidateIDs[pick.ID] {
				t.Errorf("external repository became a triage pick after reload: %s", pick.ID)
			}
		}
	}
}

func TestReadinessFilters_UseOneClockIncludingDeferralBoundary(t *testing.T) {
	deadline := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	issue := model.Issue{ID: "scheduled", Status: model.StatusOpen, DeferUntil: &deadline}
	m := NewModel([]model.Issue{issue}, nil, "")
	m.currentFilter = "ready"
	for _, now := range []time.Time{deadline.Add(-time.Nanosecond), deadline, deadline.Add(time.Nanosecond)} {
		want := !now.Before(deadline)
		if got := m.matchesCurrentFilter(issue, now); got != want {
			t.Errorf("ready at %v = %t, want %t", now, got, want)
		}
		for _, actionable := range []bool{false, true} {
			r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: &actionable}}
			selected, err := applyRecipeToIssues([]model.Issue{issue}, m.analyzer, m.analysis, nil, r, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(selected) == 1; got != (actionable == want) {
				t.Errorf("recipe actionable=%t at %v = %t, want %t", actionable, now, got, actionable == want)
			}
		}
	}
}

func TestReadyViewsPreserveSnapshotDeferralClock(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, year := range []int{2001, 2030} {
		deadline := time.Date(year, 1, 2, 3, 4, 5, 0, time.UTC)
		for _, atBoundary := range []bool{false, true} {
			name := deadline.Format("2006") + "/before"
			now, wantCount := deadline.Add(-time.Nanosecond), 1
			if atBoundary {
				name = deadline.Format("2006") + "/at-boundary"
				now, wantCount = deadline, 2
			}
			t.Run(name, func(t *testing.T) {
				expectScheduled := atBoundary
				rows := []model.Issue{
					{ID: "ready", Title: "Ready now", Status: model.StatusOpen, UpdatedAt: now},
					{ID: "scheduled", Title: "Scheduled work", Status: model.StatusOpen, UpdatedAt: now, DeferUntil: &deadline},
				}
				builder := NewSnapshotBuilder(rows)
				builder.analyzer.SetNow(now)
				snapshot := builder.Build()
				snapshot.Analysis.WaitForPhase2()
				m := NewModel(rows, nil, "")
				t.Cleanup(m.Stop)
				m.currentFilter = "ready"
				m.Update(SnapshotReadyMsg{Snapshot: snapshot})
				check := func(stage string, visible []model.Issue) {
					t.Helper()
					if m.countReady != wantCount || len(visible) != wantCount {
						t.Errorf("%s: snapshot ready=%d, visible=%v, want %d at %v", stage, m.countReady, visible, wantCount, now)
					}
					for _, row := range visible {
						if row.ID != "ready" && (row.ID != "scheduled" || !expectScheduled) {
							t.Errorf("%s: unexpected ready issue %s", stage, row.ID)
						}
					}
				}
				check("snapshot delivery", m.FilteredIssues())
				m.applyFilter()
				check("ready filter", m.FilteredIssues())
				check("active view", m.filteredIssuesForActiveView())
				actionable := true
				r := &recipe.Recipe{Name: "ready-clock", Filters: recipe.FilterConfig{Actionable: &actionable}}
				m.applyRecipe(r)
				check("actionable recipe", m.FilteredIssues())
				m.activeRecipe = r
				check("active recipe view", m.filteredIssuesForActiveView())
				if !atBoundary {
					// A new snapshot advances the same visible view to the exact
					// boundary; no wall-clock wait or source metadata edit is needed.
					builder := NewSnapshotBuilder(rows)
					builder.analyzer.SetNow(deadline)
					next := builder.Build()
					next.Analysis.WaitForPhase2()
					m.Update(SnapshotReadyMsg{Snapshot: next})
					now, wantCount, expectScheduled = deadline, 2, true
					check("new snapshot at boundary", m.FilteredIssues())
					cmd := m.preparePhase2Cmd()
					if cmd == nil {
						t.Fatal("new snapshot did not prepare Phase 2")
					}
					m.Update(cmd())
					check("completed Phase 2 at boundary", m.FilteredIssues())
				}
			})
		}
	}
}

func TestApplyRecipe_Sorting(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Priority: 2},
		{ID: "B", Priority: 1},
		{ID: "C", Priority: 3},
	}
	m := NewModel(issues, nil, "")

	r := &recipe.Recipe{
		Sort: recipe.SortConfig{
			Field:     "priority",
			Direction: "asc",
		},
	}

	m.applyRecipe(r)

	filtered := m.FilteredIssues()
	if len(filtered) != 3 {
		t.Fatal("Expected 3 issues")
	}

	// Expect B(1), A(2), C(3)
	if filtered[0].ID != "B" {
		t.Errorf("Expected B first, got %s", filtered[0].ID)
	}
	if filtered[1].ID != "A" {
		t.Errorf("Expected A second, got %s", filtered[1].ID)
	}
	if filtered[2].ID != "C" {
		t.Errorf("Expected C third, got %s", filtered[2].ID)
	}
}

func TestNewModel_RecipeSortDescendingTieBreaksByID(t *testing.T) {
	issues := []model.Issue{
		{ID: "d", Priority: 1},
		{ID: "c", Priority: 1},
		{ID: "b", Priority: 1},
		{ID: "a", Priority: 1},
		{ID: "z", Priority: 2},
	}

	r := &recipe.Recipe{
		Sort: recipe.SortConfig{
			Field:     "priority",
			Direction: "desc",
		},
	}

	expected := []string{"z", "a", "b", "c", "d"}
	for _, perm := range [][]model.Issue{
		issues,
		{issues[4], issues[0], issues[1], issues[2], issues[3]},
		{issues[3], issues[2], issues[1], issues[0], issues[4]},
	} {
		m := NewModel(append([]model.Issue(nil), perm...), r, "")
		filtered := m.FilteredIssues()
		if len(filtered) != len(expected) {
			t.Fatalf("Expected %d issues, got %d", len(expected), len(filtered))
		}
		for i, want := range expected {
			if filtered[i].ID != want {
				t.Fatalf("Input order %v: position %d = %s, want %s", perm, i, filtered[i].ID, want)
			}
		}
	}
}

func TestAttentionView_CloseRestoresInsightsPanel(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Alpha", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Beta", Status: model.StatusOpen, Priority: 2},
	}

	m := NewModel(issues, nil, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(*Model)
	m.insightsPanel.focusedPanel = PanelCycles

	// The attention view is a first-class focus (like the label dashboard):
	// ] opens it from insights, Esc returns to the list, and the insights
	// panel keeps its own cursor for when the user returns to it.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	m = updated.(*Model)
	if m.focused != focusAttention {
		t.Fatalf("Expected attention view to take focus, got %v", m.focused)
	}
	if !strings.Contains(m.View(), "Rank") {
		t.Fatal("Expected attention view to render its table")
	}
	if m.insightsPanel.focusedPanel != PanelCycles {
		t.Fatalf("Expected attention view to preserve the insights cursor %v, got %v", PanelCycles, m.insightsPanel.focusedPanel)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.focused == focusAttention {
		t.Fatal("Expected attention view to close")
	}
	if m.insightsPanel.focusedPanel != PanelCycles {
		t.Fatalf("Expected insights panel cursor preserved at %v, got %v", PanelCycles, m.insightsPanel.focusedPanel)
	}
	if m.insightsPanel.insights.Stats == nil {
		t.Fatal("Expected insights panel data restored after closing attention view")
	}
}

func TestTimeTravel_DiffBadgePropagation(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, "")

	// Manually inject diff state (simulating enterTimeTravelMode)
	m.timeTravelMode = true
	m.newIssueIDs = map[string]bool{"A": true}
	m.closedIssueIDs = map[string]bool{}
	m.modifiedIssueIDs = map[string]bool{}

	// Test getDiffStatus logic
	status := m.getDiffStatus("A")
	if status != DiffStatusNew {
		t.Errorf("Expected DiffStatusNew, got %v", status)
	}

	// Test propagation to list items via rebuild
	m.rebuildListWithDiffInfo()

	items := m.list.Items()
	if len(items) != 1 {
		t.Fatal("Expected 1 item")
	}

	item := items[0].(IssueItem)
	if item.DiffStatus != DiffStatusNew {
		t.Errorf("List item missing DiffStatusNew, got %v", item.DiffStatus)
	}
}

func TestFormatTimeRel(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t        time.Time
		expected string
	}{
		{now, "now"},
		{now.Add(-10 * time.Minute), "10m ago"},
		{now.Add(-2 * time.Hour), "2h ago"},
		{now.Add(-25 * time.Hour), "1d ago"},
		{now.Add(-8 * 24 * time.Hour), "1w ago"},
		{now.Add(-60 * 24 * time.Hour), "2mo ago"},
		{time.Time{}, "unknown"},
	}

	for _, tt := range tests {
		got := FormatTimeRel(tt.t)
		if got != tt.expected {
			t.Errorf("FormatTimeRel(%v): expected %s, got %s", tt.t, tt.expected, got)
		}
	}
}

// TestModel_InitSkipsUpdateCheckWhenDisabled proves the startup release check
// honours the updater preference (#197): with BV_NO_UPDATE_CHECK set, Init
// schedules exactly one command fewer than with the default preferences.
func TestModel_InitSkipsUpdateCheckWhenDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no saved config: defaults apply
	t.Setenv("BV_NO_SAVED_CONFIG", "")

	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	initCmdCount := func() int {
		m := NewModel(issues, nil, "")
		m.analysis = nil // WaitForPhase2Cmd then returns immediately if executed
		cmd := m.Init()
		if cmd == nil {
			return 0
		}
		// tea.Batch collapses a single command to itself; executing it is safe
		// here because every remaining startup command is non-blocking.
		if batch, ok := cmd().(tea.BatchMsg); ok {
			return len(batch)
		}
		return 1
	}

	t.Setenv("BV_NO_UPDATE_CHECK", "")
	enabled := initCmdCount()
	t.Setenv("BV_NO_UPDATE_CHECK", "1")
	disabled := initCmdCount()

	if enabled != disabled+1 {
		t.Fatalf("Init scheduled %d commands with the check enabled and %d disabled; want exactly one fewer when disabled", enabled, disabled)
	}
}

// TestRenderAlertsPanel_ShowsEverySeverityAndSuggestedAction renders the `!`
// panel with one alert per severity and checks the selected alert exposes
// its issue, partner, and suggested action while dismissed alerts vanish.
func TestRenderAlertsPanel_ShowsEverySeverityAndSuggestedAction(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 40
	m.alerts = []drift.Alert{
		{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, Message: "Issue A inactive for 45 days", IssueID: "A", SuggestedAction: "Update, close, or re-triage the issue"},
		{Type: drift.AlertPotentialDuplicate, Severity: drift.SeverityInfo, Message: "A looks like B", IssueID: "A", RelatedIssueID: "B", SuggestedAction: "Compare the two issues"},
		{Type: drift.AlertVelocityDrop, Severity: drift.SeverityWarning, Message: "Closed 1 issue(s) in the last 7 days vs 6 before", SuggestedAction: "Check for blocked work"},
	}
	m.alertsCritical, m.alertsWarning, m.alertsInfo = 1, 1, 1
	m.alertsCursor = 0

	view := m.renderAlertsPanel()
	for _, want := range []string{"3 total", "1 critical", "1 warning", "1 info", "⚠", "⚡", "ℹ", "Issue A inactive for 45 days", "Issue: A (press Enter to jump)", "Suggested: Update, close, or re-triage the issue"} {
		if !strings.Contains(view, want) {
			t.Fatalf("alerts panel missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Related: B") {
		t.Fatalf("partner of an unselected alert must not render:\n%s", view)
	}

	m.alertsCursor = 1
	view = m.renderAlertsPanel()
	for _, want := range []string{"Related: B", "Suggested: Compare the two issues"} {
		if !strings.Contains(view, want) {
			t.Fatalf("selected duplicate alert missing %q:\n%s", want, view)
		}
	}

	m.dismissedAlerts = map[string]bool{alertKey(m.alerts[0]): true}
	view = m.renderAlertsPanel()
	if strings.Contains(view, "inactive for 45 days") {
		t.Fatalf("dismissed alert still rendered:\n%s", view)
	}

	m.alerts = nil
	if view := m.renderAlertsPanel(); !strings.Contains(view, "No active alerts") {
		t.Fatalf("empty panel should say so:\n%s", view)
	}
}

// The three bindings README documents that E5 added: Shift+Tab in Insights,
// n/N while time-travelling, and t in History.

func TestInsightsKeys_ShiftTabAndTabCyclePanels(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.focused = focusInsights
	m.insightsPanel.focusedPanel = 1

	got := asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})))
	if got.insightsPanel.focusedPanel != 0 {
		t.Fatalf("shift+tab: focusedPanel=%d; want 0 (previous panel)", got.insightsPanel.focusedPanel)
	}
	got = asModelPtr(t, must2(got.Update(tea.KeyMsg{Type: tea.KeyShiftTab})))
	if got.insightsPanel.focusedPanel != PanelCount-1 {
		t.Fatalf("shift+tab from the first panel should wrap to %d, got %d", PanelCount-1, got.insightsPanel.focusedPanel)
	}
	got = asModelPtr(t, must2(got.Update(tea.KeyMsg{Type: tea.KeyTab})))
	if got.insightsPanel.focusedPanel != 0 {
		t.Fatalf("tab should wrap forward to 0, got %d", got.insightsPanel.focusedPanel)
	}
}

func TestTimeTravelKeys_NextPrevChangedIssue(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "B", Title: "B", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "C", Title: "C", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "D", Title: "D", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}
	m := NewModel(issues, nil, "")
	m.focused = focusList
	idAt := func(i int) string { return m.list.Items()[i].(IssueItem).Issue.ID }
	changed := map[string]bool{idAt(1): true, idAt(3): true}
	m.timeTravelMode = true
	m.timeTravelDiff = &analysis.SnapshotDiff{}
	m.modifiedIssueIDs = changed
	m.list.Select(0)

	press := func(key string) *Model {
		t.Helper()
		return asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})))
	}
	if got := press("n"); got.list.Index() != 1 {
		t.Fatalf("n: index=%d; want 1 (first changed issue after the cursor)", got.list.Index())
	}
	if got := press("n"); got.list.Index() != 3 {
		t.Fatalf("second n: index=%d; want 3", got.list.Index())
	}
	if got := press("n"); got.list.Index() != 1 {
		t.Fatalf("n past the last changed issue should wrap to 1, got %d", got.list.Index())
	}
	if got := press("N"); got.list.Index() != 3 {
		t.Fatalf("N should step backwards (wrapping) to 3, got %d", got.list.Index())
	}
	if !strings.Contains(m.statusMsg, "2") {
		t.Fatalf("status should report position among changed issues, got %q", m.statusMsg)
	}

	m.timeTravelMode = false
	m.list.Select(0)
	if got := press("n"); got.list.Index() != 0 {
		t.Fatalf("outside time-travel n must not move the cursor, got %d", got.list.Index())
	}
}

func TestHistoryKeys_TTogglesTimelinePane(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.focused = focusHistory
	m.historyView.SetSize(160, 40) // wide: timeline on by default
	if !m.historyView.TimelineVisible() {
		t.Fatalf("precondition: timeline should be visible at 160 columns")
	}
	tKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}

	m, _ = m.handleHistoryKeys(tKey)
	if m.historyView.TimelineVisible() || !strings.Contains(m.statusMsg, "hidden") {
		t.Fatalf("t should hide the timeline: visible=%v status=%q", m.historyView.TimelineVisible(), m.statusMsg)
	}
	m, _ = m.handleHistoryKeys(tKey)
	if !m.historyView.TimelineVisible() || !strings.Contains(m.statusMsg, "shown") {
		t.Fatalf("t again should show the timeline: visible=%v status=%q", m.historyView.TimelineVisible(), m.statusMsg)
	}

	m.historyView.SetSize(90, 40) // narrow: no timeline possible
	m, _ = m.handleHistoryKeys(tKey)
	if !m.statusIsError || !strings.Contains(m.statusMsg, "100 columns") {
		t.Fatalf("narrow terminals should explain why t does nothing: err=%v status=%q", m.statusIsError, m.statusMsg)
	}
}

// TestListKeys_DocumentedViewToggles (E6): the bindings the README documents
// for the list view do what it says: a opens the actionable view, E the
// tree, ' the recipe picker, [ the label dashboard, x exports Markdown.
func TestListKeys_DocumentedViewToggles(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"core"}},
		{ID: "B", Title: "Issue B", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"ui"}, Dependencies: []*model.Dependency{{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks}}},
	}
	fresh := func() *Model {
		m := NewModel(issues, nil, "")
		m.width, m.height = 120, 40
		m.focused = focusList
		return m
	}
	press := func(t *testing.T, m *Model, key string) *Model {
		t.Helper()
		var msg tea.KeyMsg
		if key == "esc" {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		return asModelPtr(t, must2(m.Update(msg)))
	}

	t.Run("a opens the actionable view and toggles back", func(t *testing.T) {
		m := press(t, fresh(), "a")
		if !m.isActionableView || m.focused != focusActionable {
			t.Fatalf("a: actionable=%v focused=%v", m.isActionableView, m.focused)
		}
		m = press(t, m, "a")
		if m.isActionableView || m.focused != focusList {
			t.Fatalf("second a should return to the list: actionable=%v focused=%v", m.isActionableView, m.focused)
		}
	})
	t.Run("E opens the tree view", func(t *testing.T) {
		m := press(t, fresh(), "E")
		if m.focused != focusTree {
			t.Fatalf("E: focused=%v; want tree", m.focused)
		}
		if m = press(t, m, "E"); m.focused != focusList {
			t.Fatalf("second E should return to the list, focused=%v", m.focused)
		}
	})
	t.Run("' opens the recipe picker", func(t *testing.T) {
		m := press(t, fresh(), "'")
		if !m.showRecipePicker || m.focused != focusRecipePicker {
			t.Fatalf("': picker=%v focused=%v", m.showRecipePicker, m.focused)
		}
	})
	t.Run("[ opens the label dashboard", func(t *testing.T) {
		m := press(t, fresh(), "[")
		if m.focused != focusLabelDashboard {
			t.Fatalf("[: focused=%v; want label dashboard", m.focused)
		}
		if m = press(t, m, "esc"); m.focused != focusList {
			t.Fatalf("esc should close the label dashboard, focused=%v", m.focused)
		}
	})
	t.Run("x exports Markdown into the working directory", func(t *testing.T) {
		dir := t.TempDir()
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(wd) })
		m := press(t, fresh(), "x")
		if !strings.HasPrefix(m.statusMsg, "✅ Exported") {
			t.Fatalf("x should export and confirm, status=%q", m.statusMsg)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var md bool
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				md = true
			}
		}
		if !md {
			t.Fatalf("no markdown file written to %s: %v", dir, entries)
		}
	})
}
