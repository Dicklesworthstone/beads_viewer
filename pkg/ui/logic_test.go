package ui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/instance"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// White-box testing of UI model logic

func TestFlowMatrixParentRefreshAndDetails(t *testing.T) {
	t.Chdir(t.TempDir())
	issues := []model.Issue{
		{ID: "blocker", Title: "Database migration", Status: model.StatusOpen, Labels: []string{"database"}},
		{ID: "unrelated", Title: "Unrelated database task", Status: model.StatusOpen, Labels: []string{"database"}},
		{ID: "dependent", Title: "API rollout", Status: model.StatusOpen, Labels: []string{"api"}, Dependencies: []*model.Dependency{{DependsOnID: "blocker", Type: model.DepBlocks}}},
	}
	m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues), CandidateIDs: map[string]bool{"dependent": true}})
	defer m.Stop()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	r := &recipe.Recipe{Name: "api-only", Filters: recipe.FilterConfig{Tags: []string{"api"}}}
	m.activeRecipe = r
	m.applyRecipe(r)
	press := func(key tea.KeyMsg) { t.Helper(); m.Update(key) }
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focused != focusFlowMatrix || !strings.Contains(m.View(), "blocks") || strings.Contains(m.flowMatrix.View(), "unrelated") {
		t.Fatalf("flow key journey lost real relationship:\n%s", m.View())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter}) // The blocker is hidden by the recipe.
	view := ansi.Strip(m.viewport.View())
	if m.focused != focusFlowMatrix || m.flowDetailID != "blocker" || !strings.Contains(view, "Database migration") ||
		(strings.Contains(view, "API rollout") && strings.Index(view, "Database migration") > strings.Index(view, "API rollout")) {
		t.Fatalf("wrong endpoint detail, focus=%v:\n%s", m.focused, m.viewport.View())
	}
	if got := m.FilteredIssues(); len(got) != 1 || got[0].ID != "dependent" || m.analyzer.IsCandidate("blocker") {
		t.Fatalf("context navigation changed selected work: %v", got)
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	if m.focused != focusFlowMatrix || !m.flowMatrix.showDrilldown {
		t.Fatal("Escape did not return to selected relationship")
	}
	press(tea.KeyMsg{Type: tea.KeyDown})
	issues[2].Title = "REFRESHED API rollout"
	m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(issues)).Build(), SnapshotVer: 1})
	if got := m.flowMatrix.SelectedDrilldownIssue(); got == nil || got.ID != "dependent" || got.Title != issues[2].Title {
		t.Fatalf("parent snapshot kept stale/wrong endpoint: %+v\n%s", got, m.flowMatrix.View())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "REFRESHED API rollout") {
		t.Fatalf("dependent detail is stale:\n%s", m.viewport.View())
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	issues[0].Status = model.StatusClosed
	m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(issues)).Build(), SnapshotVer: 2})
	if m.flowMatrix.SelectedDrilldownIssue() != nil || strings.Contains(m.flowMatrix.View(), "API rollout") {
		t.Fatalf("parent retained closed relationship:\n%s", m.flowMatrix.View())
	}
}

func TestFlowMatrixFileReloadWhileInspectingEndpoint(t *testing.T) {
	t.Setenv("BV_BACKGROUND_MODE", "0")
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "issues.jsonl")
	issues := []model.Issue{
		{ID: "blocker", Title: "Original blocker", Status: model.StatusOpen, IssueType: model.TypeTask, Labels: []string{"database"}},
		{ID: "dependent", Title: "API rollout", Status: model.StatusOpen, IssueType: model.TypeTask, Labels: []string{"api"}, Dependencies: []*model.Dependency{{DependsOnID: "blocker", Type: model.DepBlocks}}},
	}
	write := func() {
		t.Helper()
		var data []byte
		for _, issue := range issues {
			line, err := json.Marshal(issue)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, line...)
			data = append(data, '\n')
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	m := NewModel(copyIssues(issues), nil, path)
	defer m.Stop()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 35})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	issues[0].Title = "Reloaded blocker title"
	write()
	m.Update(FileChangedMsg{})
	if m.flowDetailID != "blocker" || !strings.Contains(ansi.Strip(m.View()), "Reloaded blocker title") {
		t.Fatalf("file reload did not refresh open endpoint detail:\n%s", ansi.Strip(m.View()))
	}
	issues[0].Status = model.StatusClosed
	write()
	m.Update(FileChangedMsg{})
	if m.flowDetailID != "" || m.flowMatrix.SelectedDrilldownIssue() != nil || strings.Contains(m.flowMatrix.View(), "Reloaded blocker title") {
		t.Fatalf("closed relationship remained inspectable:\n%s", ansi.Strip(m.View()))
	}
}

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

func TestComputeAlertsForDir_RealBaselineAndCycleSuppression(t *testing.T) {
	otherCWD := t.TempDir()
	t.Chdir(otherCWD)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	bl := &baseline.Baseline{
		Version:   baseline.CurrentVersion,
		CreatedAt: now.Add(-7 * 24 * time.Hour),
		Stats: baseline.GraphStats{
			NodeCount:       2,
			EdgeCount:       1,
			Density:         0.05,
			OpenCount:       2,
			ClosedCount:     0,
			BlockedCount:    0,
			CycleCount:      1,
			ActionableCount: 10,
		},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{
				{ID: "LEGACY-PR", Value: 0.9},
			},
		},
		Cycles: [][]string{
			{"A", "B", "A"},
		},
	}
	if err := bl.Save(baseline.DefaultPath(projectDir)); err != nil {
		t.Fatalf("save baseline: %v", err)
	}

	issues := []model.Issue{
		{
			ID: "ROOT", Title: "Root unblocker", Status: model.StatusOpen, Priority: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "A", Title: "Pre-existing cycle A", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}},
		},
		{
			ID: "B", Title: "Pre-existing cycle B", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}},
		},
		{
			ID: "BLK-1", Title: "Blocked 1", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "ROOT", Type: model.DepBlocks}},
		},
		{
			ID: "BLK-2", Title: "Blocked 2", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "ROOT", Type: model.DepBlocks},
				{DependsOnID: "BLK-1", Type: model.DepBlocks},
			},
		},
		{
			ID: "BLK-3", Title: "Blocked 3", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "ROOT", Type: model.DepBlocks}},
		},
		{
			ID: "BLK-4", Title: "Blocked 4", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "ROOT", Type: model.DepBlocks}},
		},
		{
			ID: "BLK-5", Title: "Blocked 5", Status: model.StatusOpen, Priority: 2,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "ROOT", Type: model.DepBlocks}},
		},
	}

	az := analysis.NewAnalyzer(issues)
	az.SetNow(now)
	stats := az.AnalyzeAsync(nil)
	stats.WaitForPhase2()

	alerts, _, _, _ := computeAlertsForDir(issues, stats, az, projectDir)
	seen := make(map[drift.AlertType]drift.Alert)
	for _, a := range alerts {
		seen[a.Type] = a
	}

	// Negative control: A <-> B was already in .bv/baseline.json, so it must NOT fire new_cycle.
	if unexpected, ok := seen[drift.AlertNewCycle]; ok {
		t.Fatalf("pre-existing baseline cycle A<->B must not trigger new_cycle, got: %+v", unexpected)
	}

	// All 7 baseline-drift alert types must fire against the loaded .bv/baseline.json.
	wantDriftTypes := []drift.AlertType{
		drift.AlertDensityGrowth,
		drift.AlertScopeCreep,
		drift.AlertBlockedIncrease,
		drift.AlertActionableChange,
		drift.AlertPageRankChange,
		drift.AlertNodeCountChange,
		drift.AlertEdgeCountChange,
	}
	for _, want := range wantDriftTypes {
		if _, ok := seen[want]; !ok {
			t.Errorf("expected baseline drift alert %q from .bv/baseline.json, seen=%v", want, seen)
		}
	}

	// Positive control: adding a genuinely new cycle C <-> D fires new_cycle with Delta == 1 (only C<->D).
	withNewCycle := append(copyIssues(issues),
		model.Issue{
			ID: "C", Title: "New cycle C", Status: model.StatusOpen, Priority: 1,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "D", Type: model.DepBlocks}},
		},
		model.Issue{
			ID: "D", Title: "New cycle D", Status: model.StatusOpen, Priority: 1,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}},
		},
	)
	az2 := analysis.NewAnalyzer(withNewCycle)
	az2.SetNow(now)
	stats2 := az2.AnalyzeAsync(nil)
	stats2.WaitForPhase2()

	alerts2, critical2, _, _ := computeAlertsForDir(withNewCycle, stats2, az2, projectDir)
	var newCycleAlert *drift.Alert
	for i := range alerts2 {
		if alerts2[i].Type == drift.AlertNewCycle {
			newCycleAlert = &alerts2[i]
			break
		}
	}
	if newCycleAlert == nil || critical2 == 0 {
		t.Fatalf("expected critical new_cycle alert for C<->D, got alerts=%v", alerts2)
	}
	if newCycleAlert.Delta != 1 {
		t.Fatalf("expected new_cycle Delta=1 (suppressing baseline A<->B), got Delta=%v details=%v", newCycleAlert.Delta, newCycleAlert.Details)
	}
}

func TestExportToMarkdown_FilterRecipeSearchAndWorkDir(t *testing.T) {
	otherCWD := t.TempDir()
	t.Chdir(otherCWD)

	projectDir := filepath.Join(t.TempDir(), "custom-workspace")
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "OPEN-API", Title: "Open API endpoint", Status: model.StatusOpen, Priority: 1, Labels: []string{"api"}, CreatedAt: now, UpdatedAt: now},
		{ID: "OPEN-UI", Title: "Open UI polish", Status: model.StatusOpen, Priority: 2, Labels: []string{"ui"}, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED-DB", Title: "Closed database task", Status: model.StatusClosed, Priority: 2, Labels: []string{"db"}, CreatedAt: now, UpdatedAt: now},
	}

	m := NewModel(copyIssues(issues), nil, beadsPath)
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})

	expectedPath := filepath.Join(projectDir, m.generateExportFilename())
	if !strings.Contains(filepath.Base(expectedPath), "custom-workspace") {
		t.Fatalf("expected export filename to derive project name from m.workDir, got %q", filepath.Base(expectedPath))
	}

	// 1. Unfiltered export writes all issues into m.workDir (NOT otherCWD).
	m.exportToMarkdown()
	if m.statusIsError {
		t.Fatalf("unexpected export error: %s", m.statusMsg)
	}
	body, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("expected export in m.workDir (%s), err: %v", expectedPath, err)
	}
	if entries, _ := os.ReadDir(otherCWD); len(entries) != 0 {
		t.Fatalf("export must not write to CWD when m.workDir is set, found: %v", entries)
	}
	content := string(body)
	if !strings.Contains(content, "OPEN-API") || !strings.Contains(content, "OPEN-UI") || !strings.Contains(content, "CLOSED-DB") {
		t.Fatalf("unfiltered export must include all 3 issues:\n%s", content)
	}

	// 2. Active status filter ("open") exports only open issues.
	m.currentFilter = "open"
	m.applyFilter()
	m.exportToMarkdown()
	body, err = os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	content = string(body)
	if !strings.Contains(content, "OPEN-API") || !strings.Contains(content, "OPEN-UI") || strings.Contains(content, "CLOSED-DB") {
		t.Fatalf("open-filtered export must omit CLOSED-DB:\n%s", content)
	}

	// 3. Active recipe ("api" tag) scopes export to matching recipe issues.
	m.currentFilter = "all"
	r := &recipe.Recipe{Name: "api-only", Filters: recipe.FilterConfig{Tags: []string{"api"}}}
	m.applyRecipe(r)
	m.exportToMarkdown()
	body, err = os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	content = string(body)
	if !strings.Contains(content, "OPEN-API") || strings.Contains(content, "OPEN-UI") || strings.Contains(content, "CLOSED-DB") {
		t.Fatalf("recipe-scoped export must include only OPEN-API:\n%s", content)
	}
}

func TestPriorityZeroRenderingInGraphAndInsights(t *testing.T) {
	if got := getPriorityIcon(0); got != "🚨" {
		t.Fatalf("getPriorityIcon(0) = %q, want 🚨", got)
	}
	issues := []model.Issue{
		{ID: "CRIT-0", Title: "Production outage", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(WaitForPhase2Cmd(m.analysis)())

	graphOut := ansi.Strip(m.graphView.View(120, 30))
	if !strings.Contains(graphOut, "🚨") {
		t.Fatalf("expected 🚨 P0 icon in GraphModel.View():\n%s", graphOut)
	}

	drillLine := ansi.Strip(m.insightsPanel.renderDrillDownIssue("CRIT-0", true, 80, m.theme))
	if !strings.Contains(drillLine, "P0 ") {
		t.Fatalf("expected P0 priority badge in InsightsModel.renderDrillDownIssue, got %q", drillLine)
	}
}

func TestHelpOverlayDynamicScrollAndClamping(t *testing.T) {
	issues := []model.Issue{{ID: "H-1", Title: "Help scroll test", Status: model.StatusOpen, Priority: 1}}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	// Narrow + short terminal forces single-column help body > 40 lines, so maxHelpScroll() > 20.
	m.Update(tea.WindowSizeMsg{Width: 68, Height: 16})

	maxScroll := m.maxHelpScroll()
	if maxScroll <= 20 {
		t.Fatalf("expected dynamic maxHelpScroll() > 20 on 68x16 terminal, got %d", maxScroll)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = updated.(*Model)
	if !m.showHelp || m.focused != focusHelp {
		t.Fatalf("expected help overlay open, showHelp=%v focused=%v", m.showHelp, m.focused)
	}

	topView := ansi.Strip(m.renderHelpOverlay())
	if lines := strings.Split(topView, "\n"); len(lines) > 16 {
		t.Fatalf("renderHelpOverlay() overflowed terminal height 16: got %d lines", len(lines))
	}
	if !strings.Contains(topView, "scroll") {
		t.Fatalf("expected scroll hint in help overlay when content exceeds height:\n%s", topView)
	}

	// Scroll down past the old hardcoded 20 limit all the way to the bottom and ensure dynamic clamping.
	for i := 0; i < maxScroll+15; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = updated.(*Model)
	}
	if m.helpScroll != maxScroll {
		t.Fatalf("helpScroll = %d, want dynamically clamped maxHelpScroll %d", m.helpScroll, maxScroll)
	}
	bottomView := ansi.Strip(m.renderHelpOverlay())
	if bottomView == topView {
		t.Fatal("expected scrolling help overlay to reveal lower sections")
	}
}

func TestSelectedIssueForActionAcrossAllViewsAndTreeCollapseKey(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "ACT-1", Title: "Parent backend blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeEpic, Labels: []string{"backend"}, CreatedAt: now, UpdatedAt: now},
		{
			ID: "ACT-2", Title: "Child frontend task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"frontend"}, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "ACT-2", DependsOnID: "ACT-1", Type: model.DepBlocks},
				{IssueID: "ACT-2", DependsOnID: "ACT-1", Type: model.DepParentChild},
			},
		},
		{
			ID: "ACT-3", Title: "Downstream consumer", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, Labels: []string{"frontend"}, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "ACT-3", DependsOnID: "ACT-2", Type: model.DepBlocks},
				{IssueID: "ACT-3", DependsOnID: "ACT-1", Type: model.DepParentChild},
			},
		},
	}
	m := NewModel(copyIssues(issues), nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	m.Update(WaitForPhase2Cmd(m.analysis)())

	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	// 1. focusList on IssueItem
	if got := m.selectedIssueIDForEditor(); got == "" {
		t.Fatal("expected non-empty selectedIssueIDForEditor in focusList")
	}

	// 2. focusList on IssueGroupItem (collapsed & expanded recipe group)
	grpRecipe := &recipe.Recipe{Name: "grouped", View: recipe.ViewConfig{GroupBy: "status", Collapsed: true}}
	m.setActiveRecipe(grpRecipe)
	m.applyRecipe(grpRecipe)
	m.list.Select(0)
	if _, isGroup := m.list.SelectedItem().(IssueGroupItem); !isGroup {
		t.Fatalf("expected IssueGroupItem at index 0, got %T", m.list.SelectedItem())
	}
	if got := m.selectedIssueIDForEditor(); got == "" {
		t.Fatal("expected selectedIssueIDForEditor to resolve first issue of collapsed IssueGroupItem")
	}
	m.setActiveRecipe(nil)
	m.currentFilter = "all"
	m.applyFilter()

	// 3. focusBoard ('b')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.focused != focusBoard || m.selectedIssueIDForEditor() == "" {
		t.Fatalf("focusBoard failed to resolve selected issue: focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")}) // exit board

	// 4. focusGraph ('g')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.focused != focusGraph || m.selectedIssueIDForEditor() == "" {
		t.Fatalf("focusGraph failed to resolve selected issue: focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 5. focusActionable ('a')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.focused != focusActionable || m.selectedIssueIDForEditor() != "ACT-1" {
		t.Fatalf("focusActionable expected ACT-1, got focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 6. focusInsights ('i')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m.focused != focusInsights || m.selectedIssueIDForEditor() == "" {
		t.Fatalf("focusInsights failed to resolve selected issue: focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 7. focusFlowMatrix ('f' + Tab to right pane)
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	send(tea.KeyMsg{Type: tea.KeyTab})
	if m.focused != focusFlowMatrix || m.selectedIssueIDForEditor() == "" {
		t.Fatalf("focusFlowMatrix right pane failed to resolve selected issue: focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 8. focusTree ('E'): verify selectedIssueIDForEditor AND 'Z' collapses all without 'O' collapsing
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
	if m.focused != focusTree || m.selectedIssueIDForEditor() == "" {
		t.Fatalf("focusTree failed to resolve selected issue: focused=%v id=%q", m.focused, m.selectedIssueIDForEditor())
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")}) // expand all -> ACT-2 visible
	expandedTree := ansi.Strip(m.tree.View())
	if !strings.Contains(expandedTree, "ACT-2") {
		t.Fatalf("expected ACT-2 visible after expand-all 'X':\n%s", expandedTree)
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Z")}) // collapse all -> ACT-2 hidden
	collapsedTree := ansi.Strip(m.tree.View())
	if strings.Contains(collapsedTree, "ACT-2") {
		t.Fatalf("expected 'Z' to collapse all tree nodes hiding ACT-2:\n%s", collapsedTree)
	}
}

func TestHistoryToGraphRefreshesCurrentFilter(t *testing.T) {
	issues := []model.Issue{
		{ID: "HG-OPEN-1", Title: "Open 1", Status: model.StatusOpen, Priority: 1},
		{ID: "HG-OPEN-2", Title: "Open 2", Status: model.StatusOpen, Priority: 2},
		{ID: "HG-CLOSED-3", Title: "Closed 3", Status: model.StatusClosed, Priority: 2},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Filter to open issues (2 of 3)
	m.SetFilter("open")
	// Force stale 3-issue graph before entering History to prove History -> 'g' refreshes it!
	m.graphView.SetIssues(issues, nil)
	if m.graphView.TotalCount() != 3 {
		t.Fatalf("precondition: expected 3 issues in graph before History->Graph, got %d", m.graphView.TotalCount())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = updated.(*Model)
	if m.focused != focusHistory {
		t.Fatalf("expected focusHistory, got %v", m.focused)
	}
	makeHistoryReportCurrent(m, &correlation.HistoryReport{
		GeneratedAt: time.Now(),
		Histories: map[string]correlation.BeadHistory{
			"HG-OPEN-1": {
				BeadID: "HG-OPEN-1",
				Title:  "Open 1",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{SHA: "abcdef1234567890", ShortSHA: "abcdef1", Message: "feat: open 1", Confidence: 0.9, Timestamp: time.Now()},
				},
			},
		},
	})

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = updated.(*Model)
	if m.focused != focusGraph || !m.isGraphView {
		t.Fatalf("expected History -> 'g' to enter focusGraph, got focused=%v isGraphView=%v", m.focused, m.isGraphView)
	}
	if m.graphView.TotalCount() != 2 {
		t.Fatalf("expected History -> 'g' to refresh graph for 'open' filter (2 issues), got %d", m.graphView.TotalCount())
	}
}

func TestWorkspaceRepoScopePropagatesToAllSecondaryViews(t *testing.T) {
	now := time.Now().UTC()
	closedAt := now.Add(-24 * time.Hour)
	issues := []model.Issue{
		{ID: "api-1", Title: "API auth endpoint", Status: model.StatusOpen, Priority: 1, SourceRepo: "api", Labels: []string{"backend"}, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now},
		{ID: "api-2", Title: "API rate limit", Status: model.StatusClosed, Priority: 2, SourceRepo: "api", Labels: []string{"security"}, CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
		{ID: "web-1", Title: "Web navbar", Status: model.StatusOpen, Priority: 1, SourceRepo: "web", Labels: []string{"frontend"}, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now},
		{
			ID: "web-2", Title: "Web login page", Status: model.StatusOpen, Priority: 2, SourceRepo: "web", Labels: []string{"ui-flow"}, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "web-2", DependsOnID: "web-1", Type: model.DepBlocks}},
		},
	}

	m := NewModel(copyIssues(issues), nil, "")
	t.Cleanup(m.Stop)
	m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
	m.sprints = []model.Sprint{{
		ID:        "sprint-1",
		Name:      "Sprint 1",
		StartDate: now.Add(-7 * 24 * time.Hour),
		EndDate:   now.Add(7 * 24 * time.Hour),
		BeadIDs:   []string{"api-1", "web-1"},
	}}
	m.selectedSprint = &m.sprints[0]
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	m.Update(WaitForPhase2Cmd(m.analysis)())

	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}

	// Scope workspace to repo "api" only
	m.activeRepos = map[string]bool{"api": true}
	m.applyFilter()
	m.syncSecondaryViewsForRepoScope(m.focused)

	scoped := m.repoScopedIssues()
	if len(scoped) != 2 {
		t.Fatalf("repoScopedIssues() returned %d issues, want 2 (api-1, api-2)", len(scoped))
	}

	// 1. Tree ('E')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
	treeOut := ansi.Strip(m.tree.View())
	if !strings.Contains(treeOut, "api-1") || strings.Contains(treeOut, "web-1") {
		t.Fatalf("Tree view ignored activeRepos:\n%s", treeOut)
	}

	// Live re-sync test: press 'w' while in Tree view, toggle 'api' off and 'web' on, press Enter!
	// RepoPicker starts with activeRepos={"api": true}: cursor 0 is "api", cursor 1 is "web".
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if !m.showRepoPicker || m.focused != focusRepoPicker {
		t.Fatalf("expected repo picker open from Tree view, got focused=%v", m.focused)
	}
	send(tea.KeyMsg{Type: tea.KeySpace})                     // toggle "api" off
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // move to "web"
	send(tea.KeyMsg{Type: tea.KeySpace})                     // toggle "web" on
	send(tea.KeyMsg{Type: tea.KeyEnter})                     // apply!
	if m.focused != focusTree {
		t.Fatalf("expected closing repo picker to restore focusTree, got %v", m.focused)
	}
	treeWebOut := ansi.Strip(m.tree.View())
	if !strings.Contains(treeWebOut, "web-1") || strings.Contains(treeWebOut, "api-1") {
		t.Fatalf("Tree view did not live-sync after 'w' repo change:\n%s", treeWebOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc}) // back to list

	// Switch back to {"api": true} and verify Actionable, Insights, LabelDashboard, Attention, FlowMatrix, Velocity, LabelPicker, Sprint
	m.activeRepos = map[string]bool{"api": true}
	m.applyFilter()
	m.syncSecondaryViewsForRepoScope(m.focused)

	// 2. Actionable ('a')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	actOut := ansi.Strip(m.View())
	if !strings.Contains(actOut, "api-1") || strings.Contains(actOut, "web-1") {
		t.Fatalf("Actionable view leaked web repo issues:\n%s", actOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 3. Insights ('i')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if _, leaked := m.insightsPanel.issueMap["web-1"]; leaked {
		t.Fatal("Insights view issueMap leaked excluded repo issue web-1")
	}
	if _, ok := m.insightsPanel.issueMap["api-1"]; !ok {
		t.Fatal("Insights view issueMap missing scoped repo issue api-1")
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 4. Label Dashboard ('[') & 5. Velocity ('v')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	ldOut := ansi.Strip(m.labelDashboard.View())
	if !strings.Contains(ldOut, "backend") || strings.Contains(ldOut, "frontend") {
		t.Fatalf("LabelDashboard leaked frontend label from excluded repo:\n%s", ldOut)
	}
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	velOut := ansi.Strip(m.velocityComparison.View())
	if strings.Contains(velOut, "frontend") {
		t.Fatalf("VelocityComparison leaked frontend label from excluded repo:\n%s", velOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc}) // close velocity
	send(tea.KeyMsg{Type: tea.KeyEsc}) // close label dashboard

	// 6. Attention (']')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	attOut := ansi.Strip(m.attentionView.View())
	if !strings.Contains(attOut, "backend") || strings.Contains(attOut, "frontend") {
		t.Fatalf("Attention view leaked frontend label from excluded repo:\n%s", attOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 7. Flow Matrix ('f') — api has no cross-label blocking edge, whereas web had frontend -> ui-flow
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	flowOut := ansi.Strip(m.flowMatrix.View())
	if strings.Contains(flowOut, "ui-flow") || strings.Contains(flowOut, "frontend") {
		t.Fatalf("FlowMatrix leaked web cross-label flow:\n%s", flowOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 8. Label Picker ('l')
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	lpOut := ansi.Strip(m.labelPicker.View())
	if !strings.Contains(lpOut, "backend") || strings.Contains(lpOut, "frontend") {
		t.Fatalf("LabelPicker leaked frontend label from excluded repo:\n%s", lpOut)
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	// 9. Sprint ('P' / renderSprintDashboard)
	sprintOut := ansi.Strip(m.renderSprintDashboard())
	if !strings.Contains(sprintOut, "api-1") || strings.Contains(sprintOut, "web-1") {
		t.Fatalf("Sprint dashboard leaked web-1 from excluded repo:\n%s", sprintOut)
	}
}

func TestTimeTravelUsesModelWorkDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// Set CWD to a completely unrelated non-git directory!
	nonGitDir := t.TempDir()
	t.Chdir(nonGitDir)

	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")

	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	v1 := "{\"id\":\"TT-1\",\"title\":\"First\",\"status\":\"open\",\"priority\":1,\"issue_type\":\"task\"}\n"
	if err := os.WriteFile(beadsPath, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "initial beads")

	v2 := v1 + "{\"id\":\"TT-2\",\"title\":\"Second\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\"}\n"
	if err := os.WriteFile(beadsPath, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".beads/issues.jsonl")
	runGit("commit", "-m", "add TT-2")

	issues := []model.Issue{
		{ID: "TT-1", Title: "First", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "TT-2", Title: "Second", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
	}
	m := NewModel(issues, nil, beadsPath)
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	m.enterTimeTravelMode("HEAD~1")
	if !m.IsTimeTravelMode() || m.statusIsError {
		t.Fatalf("expected enterTimeTravelMode to use m.workDir (%s) instead of non-git CWD; status=%q err=%v",
			m.workDir, m.statusMsg, m.statusIsError)
	}
	if diff := m.TimeTravelDiff(); diff == nil || diff.Summary.IssuesAdded != 1 {
		t.Fatalf("expected diff with 1 added issue (TT-2), got %+v", diff)
	}
}

func TestRecipeExportJSONAndMermaidDependencyGraph(t *testing.T) {
	otherCWD := t.TempDir()
	t.Chdir(otherCWD)

	projectDir := filepath.Join(t.TempDir(), "export-proj")
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "EXP-1", Title: "Foundation API", Status: model.StatusOpen, Priority: 1, Labels: []string{"api"}, CreatedAt: now, UpdatedAt: now},
		{
			ID: "EXP-2", Title: "Consumer service", Status: model.StatusOpen, Priority: 2, Labels: []string{"api"}, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "EXP-2", DependsOnID: "EXP-1", Type: model.DepBlocks}},
		},
		{ID: "EXP-3", Title: "Closed unrelated", Status: model.StatusClosed, Priority: 3, Labels: []string{"other"}, CreatedAt: now, UpdatedAt: now},
	}

	m := NewModel(copyIssues(issues), nil, beadsPath)
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	// 1. Recipe with Export.Format = "json"
	jsonRecipe := &recipe.Recipe{
		Name:    "api-json",
		Filters: recipe.FilterConfig{Status: []string{"open"}},
		Export:  recipe.ExportConfig{Format: "json"},
	}
	m.setActiveRecipe(jsonRecipe)
	m.applyRecipe(jsonRecipe)
	m.exportToMarkdown()
	if m.statusIsError {
		t.Fatalf("JSON recipe export failed: %s", m.statusMsg)
	}
	jsonPath := filepath.Join(projectDir, "beads_export_"+time.Now().Format("2006-01-02")+".json")
	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("expected JSON export at %s: %v", jsonPath, err)
	}
	var exportedIssues []model.Issue
	if err := json.Unmarshal(jsonBytes, &exportedIssues); err != nil {
		t.Fatalf("invalid JSON export payload: %v\n%s", err, string(jsonBytes))
	}
	if len(exportedIssues) != 2 {
		t.Fatalf("expected 2 open issues in JSON export, got %d", len(exportedIssues))
	}

	// 2. Recipe with Export.IncludeGraph = true (Markdown + Mermaid graph TD)
	incGraph := true
	mdRecipe := &recipe.Recipe{
		Name:    "api-md-graph",
		Filters: recipe.FilterConfig{Status: []string{"open"}},
		Export:  recipe.ExportConfig{Format: "markdown", IncludeGraph: &incGraph},
	}
	m.setActiveRecipe(mdRecipe)
	m.applyRecipe(mdRecipe)
	m.exportToMarkdown()
	if m.statusIsError {
		t.Fatalf("Markdown+Graph recipe export failed: %s", m.statusMsg)
	}
	mdPath := filepath.Join(projectDir, m.generateExportFilename())
	mdBytes, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("expected Markdown export at %s: %v", mdPath, err)
	}
	mdContent := string(mdBytes)
	if !strings.Contains(mdContent, "## Dependency Graph") ||
		!strings.Contains(mdContent, "```mermaid\ngraph TD") ||
		!strings.Contains(mdContent, "EXP-1 --> EXP-2") ||
		strings.Contains(mdContent, "EXP-3") {
		t.Fatalf("expected Mermaid dependency graph with EXP-1 --> EXP-2 and without EXP-3:\n%s", mdContent)
	}
}

func TestShortcutsSidebarTutorialContextAndForeignHostFooter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	GetTutorialProgressManager().Reset()
	t.Cleanup(func() { GetTutorialProgressManager().Reset() })

	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	// 1. ShortcutsSidebar: shift+tab alignment, deterministic section order, ctrl+j/ctrl+k scroll footer
	sbModel := NewModel([]model.Issue{{ID: "SB-1", Title: "Sidebar test", Status: model.StatusOpen, Priority: 1}}, nil, "")
	t.Cleanup(sbModel.Stop)
	sidebar := sbModel.shortcutsSidebar
	sidebar.SetFocus(focusInsights)
	sidebar.SetSize(38, 18) // Short height forces scrolling footer
	sbView := ansi.Strip(sidebar.View())
	if !strings.Contains(sbView, "ctrl+j/ctrl+k scroll") {
		t.Fatalf("expected 'ctrl+j/ctrl+k scroll' in sidebar footer:\n%s", sbView)
	}
	// Expand height to inspect full section order and 'shift+tab' rendering
	sidebar.SetSize(38, 120)
	fullSB := ansi.Strip(sidebar.View())
	if !strings.Contains(fullSB, "shift+tab") {
		t.Fatalf("expected 'shift+tab' intact without truncation in sidebar:\n%s", fullSB)
	}
	viewsIdx := strings.Index(fullSB, "Views")
	navIdx := strings.Index(fullSB, "Navigation")
	actIdx := strings.Index(fullSB, "Actions")
	insIdx := strings.Index(fullSB, "Insights")
	if viewsIdx < 0 || navIdx < 0 || actIdx < 0 || insIdx < 0 || !(viewsIdx < navIdx && navIdx < actIdx && actIdx < insIdx) {
		t.Fatalf("expected deterministic section order Views < Navigation < Actions < Insights, got indices %d, %d, %d, %d",
			viewsIdx, navIdx, actIdx, insIdx)
	}

	// Also verify custom canonical category priority order (Global < Views < Navigation < Filtering & Search < Export & Actions)
	customReg := NewKeyRegistry()
	customReg.RegisterBinding(KeyBinding{Focus: focusList, Key: "x", Desc: "Export", Category: "Export & Actions"})
	customReg.RegisterBinding(KeyBinding{Focus: focusList, Key: "/", Desc: "Search", Category: "Filtering & Search"})
	customReg.RegisterBinding(KeyBinding{Focus: focusList, Key: "j", Desc: "Down", Category: "Navigation"})
	customReg.RegisterBinding(KeyBinding{Focus: focusList, Key: "g", Desc: "Graph", Category: "Views"})
	customReg.RegisterBinding(KeyBinding{Focus: focusList, Key: "?", Desc: "Help", Category: "Global"})
	customSB := NewShortcutsSidebar(theme)
	customSB.SetKeyRegistry(customReg)
	customSB.SetFocus(focusList)
	customSB.SetSize(38, 60)
	customView := ansi.Strip(customSB.View())
	gIdx := strings.Index(customView, "Global")
	vIdx := strings.Index(customView, "Views")
	nIdx := strings.Index(customView, "Navigation")
	fIdx := strings.Index(customView, "Filtering & Search")
	eIdx := strings.Index(customView, "Export & Actions")
	if gIdx < 0 || vIdx < 0 || nIdx < 0 || fIdx < 0 || eIdx < 0 || !(gIdx < vIdx && vIdx < nIdx && nIdx < fIdx && fIdx < eIdx) {
		t.Fatalf("expected Global < Views < Navigation < Filtering & Search < Export & Actions, got %d, %d, %d, %d, %d",
			gIdx, vIdx, nIdx, fIdx, eIdx)
	}

	// 2. LabelDashboard 'g' jumps back to top
	ld := NewLabelDashboardModel(theme)
	ld.SetData([]analysis.LabelHealth{
		{Label: "alpha", HealthLevel: analysis.HealthLevelHealthy, Health: 90},
		{Label: "beta", HealthLevel: analysis.HealthLevelHealthy, Health: 85},
		{Label: "gamma", HealthLevel: analysis.HealthLevelHealthy, Health: 80},
	})
	ld.SetSize(80, 20)
	_, _ = ld.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if ld.cursor != 2 {
		t.Fatalf("expected 'G' to move LabelDashboard cursor to 2, got %d", ld.cursor)
	}
	_, _ = ld.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if ld.cursor != 0 {
		t.Fatalf("expected 'g' to reset LabelDashboard cursor to 0, got %d", ld.cursor)
	}
	// Also verify 'g' through parent Model.Update while focused == focusLabelDashboard (must NOT jump to Graph view!).
	ldModel := NewModel([]model.Issue{
		{ID: "L-1", Title: "Alpha issue", Status: model.StatusOpen, Labels: []string{"alpha"}},
		{ID: "L-2", Title: "Beta issue", Status: model.StatusOpen, Labels: []string{"beta"}},
		{ID: "L-3", Title: "Gamma issue", Status: model.StatusOpen, Labels: []string{"gamma"}},
	}, nil, "")
	t.Cleanup(ldModel.Stop)
	ldModel.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	ldModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	if ldModel.focused != focusLabelDashboard {
		t.Fatalf("expected '[' to enter focusLabelDashboard, got %v", ldModel.focused)
	}
	ldModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if ldModel.labelDashboard.cursor != 2 {
		t.Fatalf("expected 'G' via Model.Update to move LabelDashboard cursor to 2, got %d", ldModel.labelDashboard.cursor)
	}
	ldModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if ldModel.focused != focusLabelDashboard || ldModel.isGraphView || ldModel.labelDashboard.cursor != 0 {
		t.Fatalf("pressing 'g' in focusLabelDashboard must stay in LabelDashboard at cursor 0, got focused=%v isGraphView=%v cursor=%d",
			ldModel.focused, ldModel.isGraphView, ldModel.labelDashboard.cursor)
	}

	// 3. TutorialModel: backtick '`' closes tutorial, and SaveProgress() records visible filtered page ID!
	tut := NewTutorialModel(theme)
	tut.SetContext("graph")
	tut.SetContextMode(true)
	visible := tut.visiblePages()
	if len(visible) == 0 || visible[0].ID == tut.pages[0].ID {
		// Advance or verify filtered visible slice differs from raw m.pages at index 0 or 1
		if len(visible) > 1 && visible[1].ID != tut.pages[1].ID {
			tut.currentPage = 1
		}
	}
	expectedPageID := visible[tut.currentPage].ID
	rawPageID := tut.pages[tut.currentPage].ID
	if expectedPageID == rawPageID && len(visible) > 1 {
		for idx := range visible {
			if idx < len(tut.pages) && visible[idx].ID != tut.pages[idx].ID {
				tut.currentPage = idx
				expectedPageID = visible[idx].ID
				rawPageID = tut.pages[idx].ID
				break
			}
		}
	}
	if err := tut.SaveProgress(); err != nil {
		t.Fatalf("SaveProgress() failed: %v", err)
	}
	pm := GetTutorialProgressManager()
	if !pm.IsPageViewed(expectedPageID) {
		t.Fatalf("SaveProgress() did not mark visible filtered page %q as viewed", expectedPageID)
	}
	if expectedPageID != rawPageID && pm.IsPageViewed(rawPageID) {
		t.Fatalf("SaveProgress() wrongly marked raw unfiltered m.pages[%d] (%q) instead of visible page %q",
			tut.currentPage, rawPageID, expectedPageID)
	}
	tut, _ = tut.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("`")})
	if !tut.ShouldClose() {
		t.Fatal("expected backtick '`' to close tutorial")
	}

	// 4. ContextUpdateModal + foreign-host lock badge 🔒PID@host in renderFooter()
	lockDir := t.TempDir()
	foreignLockJSON, err := json.Marshal(instance.LockInfo{
		PID:       7788,
		StartedAt: time.Now(),
		Hostname:  "buildbox-9",
		OwnerID:   "foreign-test-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, instance.LockFileName), foreignLockJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	foreignLock, err := instance.NewLock(lockDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	defer foreignLock.Release()

	m := NewModel([]model.Issue{{ID: "F-1", Title: "Footer test", Status: model.StatusOpen, Priority: 1}}, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m.instanceLock = foreignLock
	m.showShortcutsSidebar = true
	m.showUpdateModal = true

	if gotCtx := m.CurrentContext(); gotCtx != ContextUpdateModal {
		t.Fatalf("CurrentContext() with showUpdateModal=true = %q, want %q", gotCtx, ContextUpdateModal)
	}
	if m.canScrollShortcutsSidebar("ctrl+j") {
		t.Fatal("canScrollShortcutsSidebar must be false while UpdateModal is open")
	}

	m.showUpdateModal = false
	footer := ansi.Strip(m.renderFooter())
	if !strings.Contains(footer, "🔒7788@buildbox-9") {
		t.Fatalf("expected foreign-host lock badge 🔒7788@buildbox-9 in footer:\n%s", footer)
	}
}

func TestRound1AuditRegressions(t *testing.T) {
	// 1. FileChangedMsg preserves activeRepos in Tree view (both directly in focusTree
	// and while focusRepoPicker is open over focusTree via 'w').
	t.Run("FileChangedMsgPreservesTreeRepoScopeAndUnwrapsRepoPicker", func(t *testing.T) {
		tmp := t.TempDir()
		beadsDir := filepath.Join(tmp, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		initialIssues := []model.Issue{
			{ID: "api-1", Title: "API root task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
			{ID: "web-1", Title: "Web root task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		}
		writeIssuesJSONL := func(list []model.Issue) {
			t.Helper()
			var lines []string
			for _, iss := range list {
				raw, err := json.Marshal(iss)
				if err != nil {
					t.Fatal(err)
				}
				lines = append(lines, string(raw))
			}
			if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeIssuesJSONL(initialIssues)

		m := NewModel(copyIssues(initialIssues), nil, jsonlPath)
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m.workspaceMode = true
		m.availableRepos = []string{"api", "web"}
		m.activeRepos = map[string]bool{"api": true}
		m.applyFilter()

		// Enter Tree view ('E')
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
		if m.focused != focusTree {
			t.Fatalf("expected focusTree after 'E', got %v", m.focused)
		}
		treeBefore := ansi.Strip(m.tree.View())
		if !strings.Contains(treeBefore, "api-1") || strings.Contains(treeBefore, "web-1") {
			t.Fatalf("expected initial tree scoped to api-1 only:\n%s", treeBefore)
		}

		// Reload via FileChangedMsg while directly in focusTree
		reloaded := append(copyIssues(initialIssues),
			model.Issue{ID: "api-2", Title: "API follow-up", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
			model.Issue{ID: "web-2", Title: "Web follow-up", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		)
		writeIssuesJSONL(reloaded)
		m.Update(FileChangedMsg{})
		treeAfterDirect := ansi.Strip(m.tree.View())
		if !strings.Contains(treeAfterDirect, "api-2") || strings.Contains(treeAfterDirect, "web-1") || strings.Contains(treeAfterDirect, "web-2") {
			t.Fatalf("FileChangedMsg in focusTree leaked filtered-out repo into tree:\n%s", treeAfterDirect)
		}

		// Open RepoPicker ('w') on top of focusTree and trigger another FileChangedMsg
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
		if m.focused != focusRepoPicker {
			t.Fatalf("expected focusRepoPicker after 'w', got %v", m.focused)
		}
		reloaded2 := append(copyIssues(reloaded),
			model.Issue{ID: "api-3", Title: "API third item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
			model.Issue{ID: "web-3", Title: "Web third item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		)
		writeIssuesJSONL(reloaded2)
		m.Update(FileChangedMsg{})
		treeAfterOverlay := ansi.Strip(m.tree.View())
		if !strings.Contains(treeAfterOverlay, "api-3") || strings.Contains(treeAfterOverlay, "web-3") {
			t.Fatalf("FileChangedMsg under focusRepoPicker failed to refresh repo-scoped tree:\n%s", treeAfterOverlay)
		}
	})

	// 2. rebuildInsightsPanel wires SetReadinessScope for cross-repo dependencies and clamps selections.
	t.Run("RebuildInsightsPanelCrossRepoReadinessAndClampSelections", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "web-closed", Title: "Completed web prereq", Status: model.StatusClosed, Priority: 1},
			{ID: "web-open", Title: "Unfinished web blocker", Status: model.StatusOpen, Priority: 1},
			{
				ID:       "api-ready",
				Title:    "API task unblocked by closed web issue",
				Status:   model.StatusOpen,
				Priority: 1,
				Dependencies: []*model.Dependency{
					{IssueID: "api-ready", DependsOnID: "web-closed", Type: model.DepBlocks},
				},
			},
			{
				ID:       "api-blocked",
				Title:    "API task still blocked by open web issue",
				Status:   model.StatusOpen,
				Priority: 2,
				Dependencies: []*model.Dependency{
					{IssueID: "api-blocked", DependsOnID: "web-open", Type: model.DepBlocks},
				},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		m.workspaceMode = true
		m.availableRepos = []string{"api", "web"}
		m.activeRepos = map[string]bool{"api": true}
		// Seed out-of-bounds navigation indices before rebuild to verify ClampSelections().
		m.insightsPanel.focusedPanel = MetricPanel(999)
		for p := MetricPanel(0); p < PanelCount; p++ {
			m.insightsPanel.selectedIndex[p] = 500
			m.insightsPanel.scrollOffset[p] = 500
		}

		m.rebuildInsightsPanel()

		if m.insightsPanel.focusedPanel < 0 || m.insightsPanel.focusedPanel >= PanelCount {
			t.Fatalf("ClampSelections failed to clamp focusedPanel, got %d", m.insightsPanel.focusedPanel)
		}
		for p := MetricPanel(0); p < PanelCount; p++ {
			cnt := m.insightsPanel.panelItemCount(p)
			if cnt == 0 && m.insightsPanel.selectedIndex[p] != 0 {
				t.Fatalf("panel %d has 0 items but selectedIndex=%d", p, m.insightsPanel.selectedIndex[p])
			}
			if cnt > 0 && m.insightsPanel.selectedIndex[p] >= cnt {
				t.Fatalf("panel %d has %d items but selectedIndex=%d", p, cnt, m.insightsPanel.selectedIndex[p])
			}
		}

		foundReady := false
		for _, pick := range m.insightsPanel.topPicks {
			if pick.ID == "api-ready" {
				foundReady = true
			}
			if pick.ID == "api-blocked" || strings.HasPrefix(pick.ID, "web-") {
				t.Fatalf("unexpected pick %q in repo-scoped topPicks: %+v", pick.ID, m.insightsPanel.topPicks)
			}
		}
		if !foundReady {
			t.Fatalf("expected 'api-ready' (blocked only by closed cross-repo 'web-closed') in topPicks, got %+v", m.insightsPanel.topPicks)
		}
	})

	// 3. exportToMarkdown ('x'): standalone mermaid (.mmd), nil IncludeGraph preserving default graph,
	// and collision-free Mermaid node IDs for dotted vs underscored IDs ("bv-apal.46" vs "bv-apal_46").
	t.Run("ExportToMarkdownMermaidFormatDefaultGraphAndCollisionFreeIDs", func(t *testing.T) {
		exportDir := t.TempDir()
		issues := []model.Issue{
			{ID: "bv-apal.46", Title: "Dotted bead ID", Status: model.StatusOpen, Priority: 0},
			{
				ID:       "bv-apal_46",
				Title:    "Underscored bead ID",
				Status:   model.StatusOpen,
				Priority: 1,
				Dependencies: []*model.Dependency{
					{IssueID: "bv-apal_46", DependsOnID: "bv-apal.46", Type: model.DepBlocks},
				},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.workDir = exportDir
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// (a) Recipe with Export.Format == "mermaid" writes standalone .mmd file.
		mmdRecipe := &recipe.Recipe{
			Name:   "mermaid-only",
			Export: recipe.ExportConfig{Format: "mermaid"},
		}
		m.setActiveRecipe(mmdRecipe)
		m.applyRecipe(mmdRecipe)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("mermaid export failed: %s", m.statusMsg)
		}
		mmdPath := filepath.Join(exportDir, fmt.Sprintf("beads_export_%s.mmd", time.Now().Format("2006-01-02")))
		mmdBytes, err := os.ReadFile(mmdPath)
		if err != nil {
			t.Fatalf("expected standalone .mmd file at %s: %v", mmdPath, err)
		}
		mmdText := string(mmdBytes)
		if !strings.HasPrefix(mmdText, "graph TD\n") {
			t.Fatalf("expected standalone .mmd to start with 'graph TD', got:\n%s", mmdText)
		}
		// Verify "bv-apal.46" and "bv-apal_46" do NOT collide onto a self-loop `bv-apal_46 --> bv-apal_46`!
		if strings.Contains(mmdText, "bv-apal_46 --> bv-apal_46") {
			t.Fatalf("Mermaid node IDs collided into self-loop:\n%s", mmdText)
		}
		if !strings.Contains(mmdText, `["bv-apal.46"] --> bv-apal_46`) {
			t.Fatalf("expected distinct hashed node ref for 'bv-apal.46' pointing to 'bv-apal_46', got:\n%s", mmdText)
		}

		// (b) Recipe with Export.Format == "markdown" and Export.IncludeGraph == nil MUST preserve default IncludeGraph == true.
		mdRecipe := &recipe.Recipe{
			Name:   "md-default-graph",
			Export: recipe.ExportConfig{Format: "markdown", IncludeGraph: nil},
		}
		m.setActiveRecipe(mdRecipe)
		m.applyRecipe(mdRecipe)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("markdown export failed: %s", m.statusMsg)
		}
		mdFiles, err := filepath.Glob(filepath.Join(exportDir, "*.md"))
		if err != nil || len(mdFiles) == 0 {
			t.Fatalf("expected exported .md file in %s, files=%v err=%v", exportDir, mdFiles, err)
		}
		mdBytes, err := os.ReadFile(mdFiles[0])
		if err != nil {
			t.Fatal(err)
		}
		mdText := string(mdBytes)
		if !strings.Contains(mdText, "## Dependency Graph") || !strings.Contains(mdText, "```mermaid") {
			t.Fatalf("expected nil Export.IncludeGraph to preserve default Dependency Graph section:\n%s", mdText)
		}
		if !strings.Contains(mdText, `["bv-apal.46"] --> bv-apal_46`) {
			t.Fatalf("expected collision-free Mermaid edge in Markdown export:\n%s", mdText)
		}
	})

	// 4. FlowMatrix detail modal ('O' resolves flowDetailID instead of viewport swallowing),
	// explicit non-issue view 'O' errors, BV_TEST_MODE unset-$EDITOR guard, and helpScroll dynamic clamp.
	t.Run("FlowDetailEditorNonIssueViewsTestModeGuardAndHelpClamp", func(t *testing.T) {
		trackedIssue, beadsFile, _ := installEditorTracker(t, "br")
		trackedIssue.Labels = []string{"frontend"}
		blocker := model.Issue{
			ID:        "B-1",
			Title:     "Backend blocker",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Labels:    []string{"backend"},
		}
		trackedIssue.Dependencies = []*model.Dependency{
			{IssueID: trackedIssue.ID, DependsOnID: blocker.ID, Type: model.DepBlocks},
		}
		issues := []model.Issue{blocker, trackedIssue}

		// Put a fake `code` and `xdg-open` on PATH that would write a sentinel file if invoked.
		guiBinDir := t.TempDir()
		sentinelPath := filepath.Join(guiBinDir, "gui_launched.txt")
		fakeGUIScript := "#!/bin/sh\necho launched > " + sentinelPath + "\n"
		for _, binName := range []string{"xdg-open", "code"} {
			p := filepath.Join(guiBinDir, binName)
			if err := os.WriteFile(p, []byte(fakeGUIScript), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", guiBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("EDITOR", "")
		t.Setenv("VISUAL", "")
		t.Setenv("BV_TEST_MODE", "1")

		m := NewModel(copyIssues(issues), nil, beadsFile)
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

		// Enter FlowMatrix ('f'), focus right pane ('Tab'), advance to trackedIssue ('j'), open flowDetailID ('Enter').
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.flowDetailID == "" {
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		}
		if m.flowDetailID != trackedIssue.ID {
			t.Fatalf("expected flowDetailID=%q, got %q", trackedIssue.ID, m.flowDetailID)
		}
		selIss, reason := m.selectedIssueForActionWithReason()
		if selIss == nil || selIss.ID != trackedIssue.ID || reason != "" {
			t.Fatalf("expected selectedIssueForActionWithReason in flowDetailID to return %q, got iss=%+v reason=%q",
				trackedIssue.ID, selIss, reason)
		}

		// Pressing 'O' while flowDetailID != "" must invoke openInEditor (not viewport.Update),
		// AND under BV_TEST_MODE=1 with unset EDITOR/VISUAL must NOT execute xdg-open/code on PATH!
		m.statusMsg = ""
		_, edCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
		if edCmd != nil {
			t.Fatal("expected nil cmd when EDITOR/VISUAL unset under BV_TEST_MODE=1")
		}
		if !m.statusIsError || !strings.Contains(m.statusMsg, "No GUI editor found") {
			t.Fatalf("expected 'O' in flowDetailID to hit BV_TEST_MODE GUI guard, got statusIsError=%v msg=%q",
				m.statusIsError, m.statusMsg)
		}
		if _, statErr := os.Stat(sentinelPath); statErr == nil {
			t.Fatal("BV_TEST_MODE=1 failed to suppress implicit GUI editor execution from PATH!")
		}

		// With terminal EDITOR="vim", pressing 'O' in flowDetailID dispatches terminal editor for trackedIssue.ID!
		t.Setenv("EDITOR", "vim")
		m.statusMsg = ""
		_, vimCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
		if vimCmd == nil || m.statusIsError || !strings.Contains(m.statusMsg, trackedIssue.ID) {
			t.Fatalf("expected 'O' in flowDetailID with EDITOR=vim to return ExecProcess cmd for %s, got cmd=%v err=%v msg=%q",
				trackedIssue.ID, vimCmd != nil, m.statusIsError, m.statusMsg)
		}

		// Close flowDetailID
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// Explicit non-issue views must NOT fall back to m.list.SelectedItem() on 'O'!
		for _, nonIssueFocus := range []focus{focusSprint, focusLabelDashboard, focusAttention, focusVelocityComparison} {
			m.focused = nonIssueFocus
			m.statusMsg = ""
			m.statusIsError = false
			iss, why := m.selectedIssueForActionWithReason()
			if iss != nil || why == "" {
				t.Fatalf("focus=%v must return nil issue + explicit reason, got iss=%+v why=%q", nonIssueFocus, iss, why)
			}
			cmd := m.openInEditor()
			if cmd != nil || !m.statusIsError || m.statusMsg == "" {
				t.Fatalf("openInEditor in focus=%v should set explicit error statusMsg, got err=%v msg=%q",
					nonIssueFocus, m.statusIsError, m.statusMsg)
			}
		}

		// Help overlay dynamic scroll clamp: setting exaggerated helpScroll then pressing 'k' or resizing
		// clamps immediately to maxHelpScroll() so a single 'k' moves visibly!
		m.focused = focusHelp
		m.showHelp = true
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 18})
		maxScroll := m.maxHelpScroll()
		m.helpScroll = maxScroll + 500
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		expectedAfterK := max(0, maxScroll-1)
		if m.helpScroll != expectedAfterK {
			t.Fatalf("expected 'k' to clamp helpScroll to maxScroll (%d) before decrementing to %d, got %d",
				maxScroll, expectedAfterK, m.helpScroll)
		}
		m.helpScroll = maxScroll + 500
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 45})
		if m.helpScroll > m.maxHelpScroll() {
			t.Fatalf("WindowSizeMsg failed to clamp helpScroll: got %d > max %d", m.helpScroll, m.maxHelpScroll())
		}
	})
}

func TestRound2AuditRegressions(t *testing.T) {
	// Defect 3.1: SnapshotReadyMsg with stale recipe-capped ListItems after m.setActiveRecipe(nil)
	// under both m.currentFilter == "all" and m.currentFilter == "closed".
	t.Run("Defect3_1_SnapshotReadyMsgStaleRecipeListItemsRace", func(t *testing.T) {
		allIssues := []model.Issue{
			{ID: "open-1", Title: "Top triage open issue", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
			{ID: "open-2", Title: "Second open issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
			{ID: "closed-1", Title: "Closed issue alpha", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask},
			{ID: "closed-2", Title: "Closed issue beta", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask},
		}
		triageTop := &recipe.Recipe{
			Name:    "triage-top",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
			View:    recipe.ViewConfig{MaxItems: 1},
		}

		// Case A: User clears recipe and stays on currentFilter == "all" before stale recipe SnapshotReadyMsg arrives.
		mAll := NewModel(copyIssues(allIssues[:2]), nil, "")
		t.Cleanup(mAll.Stop)
		mAll.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		mAll.setActiveRecipe(triageTop)
		mAll.applyRecipe(triageTop)

		staleSnapAll := NewSnapshotBuilder(copyIssues(allIssues)).WithRecipe(triageTop).Build()
		if staleSnapAll.RecipeName != "triage-top" || len(staleSnapAll.ListItems) != 1 {
			t.Fatalf("precondition: expected stale recipe snapshot with RecipeName=triage-top and 1 ListItem, got name=%q len=%d",
				staleSnapAll.RecipeName, len(staleSnapAll.ListItems))
		}

		// User clears recipe before SnapshotReadyMsg arrives.
		mAll.setActiveRecipe(nil)
		mAll.currentFilter = "all"
		mAll.applyFilter()

		mAll.Update(SnapshotReadyMsg{Snapshot: staleSnapAll})
		if got := len(mAll.list.VisibleItems()); got != 4 {
			t.Fatalf("currentFilter='all' after clearing recipe: expected all 4 issues, got %d (clobbered by stale recipe ListItems!)", got)
		}
		if gotBoard := mAll.board.TotalCount(); gotBoard != 4 {
			t.Fatalf("currentFilter='all' board count: expected 4, got %d", gotBoard)
		}

		// Case B: User clears recipe and switches to currentFilter == "closed" before stale open-only recipe SnapshotReadyMsg arrives.
		mClosed := NewModel(copyIssues(allIssues[:2]), nil, "")
		t.Cleanup(mClosed.Stop)
		mClosed.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		mClosed.setActiveRecipe(triageTop)
		mClosed.applyRecipe(triageTop)

		staleSnapClosed := NewSnapshotBuilder(copyIssues(allIssues)).WithRecipe(triageTop).Build()
		mClosed.setActiveRecipe(nil)
		mClosed.currentFilter = "closed"
		mClosed.applyFilter()

		mClosed.Update(SnapshotReadyMsg{Snapshot: staleSnapClosed})
		visibleClosed := mClosed.list.VisibleItems()
		if len(visibleClosed) != 2 {
			t.Fatalf("currentFilter='closed' after clearing recipe: expected 2 closed issues, got %d", len(visibleClosed))
		}
		seenClosed := map[string]bool{}
		for _, raw := range visibleClosed {
			if item, ok := raw.(IssueItem); ok {
				seenClosed[item.Issue.ID] = true
			}
		}
		if !seenClosed["closed-1"] || !seenClosed["closed-2"] {
			t.Fatalf("expected closed-1 and closed-2 in list, got %v", seenClosed)
		}
	})

	// Defect 3.2: exportToMarkdown CSV support, ResolveReportOptions validation + closed exclusion on json/mermaid/csv,
	// and Mermaid node IDs starting with ASCII letter and containing zero consecutive '--'.
	t.Run("Defect3_2_ExportCSVResolveOptionsAndMermaidDoubleDashIDs", func(t *testing.T) {
		exportDir := t.TempDir()
		issues := []model.Issue{
			{ID: "proj--1", Title: "Double-dash blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
			{
				ID:        "123-abc",
				Title:     "Digit-leading dependent",
				Status:    model.StatusOpen,
				Priority:  1,
				IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{
					{IssueID: "123-abc", DependsOnID: "proj--1", Type: model.DepBlocks},
				},
			},
			{ID: "closed--9", Title: "Closed double-dash issue", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.workDir = exportDir
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// (a) Export.Format == "csv" writes .csv via ResolveReportOptions + export.GenerateReport (excluding closed when filtered).
		csvRecipe := &recipe.Recipe{
			Name:    "open-csv",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
			Export:  recipe.ExportConfig{Format: "csv"},
		}
		m.setActiveRecipe(csvRecipe)
		m.applyRecipe(csvRecipe)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("CSV export failed: %s", m.statusMsg)
		}
		csvFiles, err := filepath.Glob(filepath.Join(exportDir, "*.csv"))
		if err != nil || len(csvFiles) != 1 {
			t.Fatalf("expected 1 .csv file in %s, got %v (err=%v)", exportDir, csvFiles, err)
		}
		csvRaw, err := os.ReadFile(csvFiles[0])
		if err != nil {
			t.Fatal(err)
		}
		records, err := csv.NewReader(strings.NewReader(string(csvRaw))).ReadAll()
		if err != nil {
			t.Fatalf("invalid CSV output: %v\n%s", err, string(csvRaw))
		}
		if len(records) != 3 { // header + 2 open issues
			t.Fatalf("expected CSV header + 2 open rows (closed--9 excluded), got %d rows:\n%s", len(records), string(csvRaw))
		}
		if strings.Contains(string(csvRaw), "closed--9") {
			t.Fatalf("CSV export leaked closed issue closed--9:\n%s", string(csvRaw))
		}

		// (b) ResolveReportOptions + Sort validation runs BEFORE json and mermaid dispatches:
		// Mermaid with IncludeGraph=false must fail via ResolveReportOptions, and invalid Sort.Field must fail on JSON.
		incFalse := false
		badMmdRecipe := &recipe.Recipe{
			Name:   "bad-mermaid",
			Export: recipe.ExportConfig{Format: "mermaid", IncludeGraph: &incFalse},
		}
		m.setActiveRecipe(badMmdRecipe)
		m.exportToMarkdown()
		if !m.statusIsError || !strings.Contains(m.statusMsg, "include_graph") {
			t.Fatalf("expected ResolveReportOptions error for mermaid IncludeGraph=false, got err=%v msg=%q", m.statusIsError, m.statusMsg)
		}

		badSortJSON := &recipe.Recipe{
			Name:   "bad-sort-json",
			Sort:   recipe.SortConfig{Field: "not_a_valid_field"},
			Export: recipe.ExportConfig{Format: "json"},
		}
		m.setActiveRecipe(badSortJSON)
		m.exportToMarkdown()
		if !m.statusIsError {
			t.Fatalf("expected invalid Sort.Field on json export to fail validation, got msg=%q", m.statusMsg)
		}

		// (c) Open-only JSON and Mermaid exports exclude closed issues AND sanitize "proj--1" / "123-abc" node IDs.
		openJSON := &recipe.Recipe{
			Name:    "open-json",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
			Export:  recipe.ExportConfig{Format: "json"},
		}
		m.setActiveRecipe(openJSON)
		m.applyRecipe(openJSON)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("open JSON export failed: %s", m.statusMsg)
		}
		jsonPath := filepath.Join(exportDir, fmt.Sprintf("beads_export_%s.json", time.Now().Format("2006-01-02")))
		jsonBytes, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(jsonBytes), "closed--9") {
			t.Fatalf("open JSON export leaked closed--9:\n%s", string(jsonBytes))
		}

		openMermaid := &recipe.Recipe{
			Name:    "open-mermaid",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
			Export:  recipe.ExportConfig{Format: "mermaid"},
		}
		m.setActiveRecipe(openMermaid)
		m.applyRecipe(openMermaid)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("open Mermaid export failed: %s", m.statusMsg)
		}
		mmdPath := filepath.Join(exportDir, fmt.Sprintf("beads_export_%s.mmd", time.Now().Format("2006-01-02")))
		mmdBytes, err := os.ReadFile(mmdPath)
		if err != nil {
			t.Fatal(err)
		}
		mmdText := string(mmdBytes)
		if strings.Contains(mmdText, "closed--9") {
			t.Fatalf("open Mermaid export leaked closed--9:\n%s", mmdText)
		}
		if isPureMermaidNodeID("proj--1") || isPureMermaidNodeID("123-abc") || isPureMermaidNodeID("-lead") {
			t.Fatal("isPureMermaidNodeID must reject consecutive '--' and leading digits/'-'")
		}
		// Every node token before '["' in the Mermaid edge must start with ASCII letter and contain zero '--'.
		refs := buildMermaidNodeRefs(issues[:2])
		for origID, ref := range refs {
			idx := strings.Index(ref, `["`)
			if idx <= 0 {
				t.Fatalf("expected aliased ref for %q, got %q", origID, ref)
			}
			alias := ref[:idx]
			if !((alias[0] >= 'a' && alias[0] <= 'z') || (alias[0] >= 'A' && alias[0] <= 'Z')) {
				t.Fatalf("Mermaid alias %q for %q does not start with ASCII letter", alias, origID)
			}
			if strings.Contains(alias, "--") {
				t.Fatalf("Mermaid alias %q for %q contains consecutive '--'", alias, origID)
			}
		}
		if !strings.Contains(mmdText, `["proj--1"] --> `) || !strings.Contains(mmdText, `["123-abc"]`) {
			t.Fatalf("expected sanitized Mermaid edge between proj--1 and 123-abc, got:\n%s", mmdText)
		}
	})

	// Defect 3.4: Ambient $EDITOR="xdg-open" and $VISUAL="code --wait" (without installTestGUIEditor)
	// must NOT spawn any external process under BV_TEST_MODE=1.
	t.Run("Defect3_4_AmbientGUIEditorBlockedUnderTestMode", func(t *testing.T) {
		trackedIssue, beadsFile, _ := installEditorTracker(t, "br")
		fakeBin := t.TempDir()
		sentinelPath := filepath.Join(t.TempDir(), "gui_editor_spawned")
		for _, binName := range []string{"xdg-open", "code"} {
			script := fmt.Sprintf("#!/bin/sh\necho spawned > %q\n", sentinelPath)
			binPath := filepath.Join(fakeBin, binName)
			if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Prepend fakeBin while keeping installEditorTracker's PATH entries (br) intact.
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

		m := NewModel([]model.Issue{trackedIssue}, nil, beadsFile)
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// 1) EDITOR="xdg-open"
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "xdg-open")
		if cmd := m.openInEditor(); cmd != nil {
			t.Fatalf("expected nil cmd for GUI EDITOR=xdg-open in BV_TEST_MODE, got non-nil")
		}
		if !m.statusIsError {
			t.Fatalf("expected error status when EDITOR=xdg-open is blocked in BV_TEST_MODE, got %q", m.statusMsg)
		}
		if _, statErr := os.Stat(sentinelPath); statErr == nil {
			t.Fatal("SECURITY/SAFETY VIOLATION: EDITOR=xdg-open spawned external process under BV_TEST_MODE=1!")
		}

		// 2) VISUAL="code --wait"
		t.Setenv("EDITOR", "")
		t.Setenv("VISUAL", "code --wait")
		if cmd := m.openInEditor(); cmd != nil {
			t.Fatalf("expected nil cmd for GUI VISUAL='code --wait' in BV_TEST_MODE, got non-nil")
		}
		if !m.statusIsError {
			t.Fatalf("expected error status when VISUAL='code --wait' is blocked in BV_TEST_MODE, got %q", m.statusMsg)
		}
		if _, statErr := os.Stat(sentinelPath); statErr == nil {
			t.Fatal("SECURITY/SAFETY VIOLATION: VISUAL='code --wait' spawned external process under BV_TEST_MODE=1!")
		}
	})

	// Defect 3.5: Modal Z-order vs key-routing, Ctrl+C never swallowed by modals,
	// Help over AgentPrompt never mutating AGENTS.md on Enter/y, and Tutorial -> ? -> Space -> Esc no loop.
	t.Run("Defect3_5_ModalZOrderCtrlCAndHelpTutorialEsc", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		GetTutorialProgressManager().Reset()
		t.Cleanup(func() { GetTutorialProgressManager().Reset() })

		workDir := t.TempDir()
		agentsPath := filepath.Join(workDir, "AGENTS.md")
		const initialAgentsContent = "# Original AGENTS.md\nDo not touch while Help/Tutorial is on top.\n"
		if err := os.WriteFile(agentsPath, []byte(initialAgentsContent), 0o644); err != nil {
			t.Fatal(err)
		}

		issues := []model.Issue{{ID: "MOD-1", Title: "Modal test", Status: model.StatusOpen, Priority: 1}}
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
		m.workDir = workDir
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

		// (a) Ctrl+C returns non-nil quit command in Tutorial, Help, AgentPrompt, and CassModal.
		for _, tc := range []struct {
			name  string
			setup func(*Model)
		}{
			{
				name: "Help",
				setup: func(m *Model) {
					m.showHelp = true
					m.focused = focusHelp
				},
			},
			{
				name: "Tutorial",
				setup: func(m *Model) {
					m.showTutorial = true
					m.focused = focusTutorial
				},
			},
			{
				name: "AgentPrompt",
				setup: func(m *Model) {
					m.showAgentPrompt = true
					m.agentPromptModal = NewAgentPromptModal(agentsPath, "AGENTS.md", m.theme)
					m.focused = focusAgentPrompt
				},
			},
			{
				name: "CassModal",
				setup: func(m *Model) {
					m.showCassModal = true
					m.focused = focusCassModal
				},
			},
		} {
			mCheck := NewModel(copyIssues(issues), nil, "")
			t.Cleanup(mCheck.Stop)
			mCheck.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			tc.setup(mCheck)
			_, quitCmd := mCheck.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if quitCmd == nil {
				t.Fatalf("Ctrl+C was swallowed in %s modal (expected non-nil quit cmd)", tc.name)
			}
		}

		// (b) Enter and 'y' while showHelp (and showTutorial) is open on top of showAgentPrompt must NEVER mutate AGENTS.md!
		m.Update(AgentFileCheckMsg{ShouldPrompt: true, FilePath: agentsPath, FileType: "AGENTS.md"})
		if !m.showAgentPrompt || m.focused != focusAgentPrompt {
			t.Fatalf("expected AgentPrompt active, got show=%v focused=%v", m.showAgentPrompt, m.focused)
		}
		// Open Help over AgentPrompt via '?' and press Enter: dismisses Help, keeps AgentPrompt intact, does NOT touch AGENTS.md!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		if !m.showHelp || m.focused != focusHelp {
			t.Fatalf("expected '?' to open Help over AgentPrompt, got showHelp=%v focused=%v", m.showHelp, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		afterEnter, err := os.ReadFile(agentsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(afterEnter) != initialAgentsContent || !m.showAgentPrompt {
			t.Fatalf("Enter while Help was visible over AgentPrompt mutated AGENTS.md or closed AgentPrompt! showAgentPrompt=%v content=%q",
				m.showAgentPrompt, string(afterEnter))
		}

		// Re-open Help over AgentPrompt and press 'y': dismisses Help, does NOT touch AGENTS.md!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		afterY, err := os.ReadFile(agentsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(afterY) != initialAgentsContent || !m.showAgentPrompt {
			t.Fatalf("'y' while Help was visible over AgentPrompt mutated AGENTS.md or closed AgentPrompt! showAgentPrompt=%v content=%q",
				m.showAgentPrompt, string(afterY))
		}

		// Open context Tutorial over AgentPrompt via '~' and press 'y' / Enter: does NOT touch AGENTS.md!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("~")})
		if !m.showTutorial || m.focused != focusTutorial {
			t.Fatalf("expected '~' to open Tutorial over AgentPrompt, got showTutorial=%v focused=%v", m.showTutorial, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		afterTut, err := os.ReadFile(agentsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(afterTut) != initialAgentsContent || !m.showAgentPrompt {
			t.Fatalf("y/Enter while Tutorial was visible over AgentPrompt mutated AGENTS.md! content=%q", string(afterTut))
		}
		// Close Tutorial then decline AgentPrompt
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showTutorial || m.showAgentPrompt || m.focused != focusList {
			t.Fatalf("expected Esc twice to close Tutorial then AgentPrompt back to focusList, got tut=%v agent=%v focused=%v",
				m.showTutorial, m.showAgentPrompt, m.focused)
		}

		// (c) From focusList: '`' (Tutorial) -> '?' (Help) -> Space (back to Tutorial) -> Esc closes Tutorial to focusList (no loop!).
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("`")})
		if !m.showTutorial || m.focused != focusTutorial {
			t.Fatalf("expected Tutorial open after '`', got showTutorial=%v focused=%v", m.showTutorial, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		if !m.showHelp || m.focused != focusHelp {
			t.Fatalf("expected Help open over Tutorial after '?', got showHelp=%v focused=%v", m.showHelp, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
		if m.showHelp || !m.showTutorial || m.focused != focusTutorial {
			t.Fatalf("expected Space in Help to return to Tutorial, got showHelp=%v showTutorial=%v focused=%v",
				m.showHelp, m.showTutorial, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showTutorial || m.showHelp || m.focused != focusList {
			t.Fatalf("expected single Esc in Tutorial after ?->Space to restore focusList, got showTutorial=%v focused=%v",
				m.showTutorial, m.focused)
		}
	})
}

func TestRound3ModalZOrderAndWedgeRegressions(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "R3-1", Title: "Alpha issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"alpha"}, CreatedAt: now, UpdatedAt: now},
		{ID: "R3-2", Title: "Beta issue", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"beta"}, CreatedAt: now, UpdatedAt: now},
		{ID: "R3-3", Title: "Gamma issue", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, Labels: []string{"gamma"}, CreatedAt: now, UpdatedAt: now},
	}

	// 1. [BUG R3-3A] focusAttention (']') -> showAlertsPanel ('!'):
	// AlertsPanel renders above Attention in renderMainView, so j/k/Enter/Esc/q MUST hit AlertsPanel first!
	t.Run("R3_3A_AlertsPanelRoutesAboveAttentionView", func(t *testing.T) {
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		m.alerts = []drift.Alert{
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "R3-1", Message: "Stale R3-1"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "R3-2", Message: "Critical R3-2"},
		}
		m.dismissedAlerts = make(map[string]bool)

		// Open Attention view via ']'
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
		if m.focused != focusAttention {
			t.Fatalf("expected focusAttention after ']', got %v", m.focused)
		}
		initialAttnLabel := m.attentionView.SelectedLabel()

		// Open AlertsPanel over Attention via '!'
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
		if !m.showAlertsPanel {
			t.Fatal("expected '!' in focusAttention to open showAlertsPanel")
		}
		_, isOverlay := m.renderMainView()
		if !isOverlay {
			t.Fatal("expected renderMainView to draw showAlertsPanel as top overlay")
		}

		// 'j' moves alertsCursor (0 -> 1) and must NOT move hidden attentionView cursor!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if m.alertsCursor != 1 {
			t.Fatalf("expected 'j' to move alertsCursor to 1, got %d", m.alertsCursor)
		}
		if gotLabel := m.attentionView.SelectedLabel(); gotLabel != initialAttnLabel {
			t.Fatalf("'j' while showAlertsPanel was open mutated hidden attentionView selection: got %q, want %q", gotLabel, initialAttnLabel)
		}

		// 'Esc' closes showAlertsPanel (not leaving it stuck!) and keeps underlying focusAttention!
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showAlertsPanel {
			t.Fatal("Esc failed to close showAlertsPanel while focusAttention was active underneath")
		}
		if m.focused != focusAttention {
			t.Fatalf("expected Esc on showAlertsPanel to return to underlying focusAttention, got %v", m.focused)
		}

		// Re-open AlertsPanel and dismiss with 'q': closes showAlertsPanel and stays in focusAttention!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
		if m.showAlertsPanel || m.focused != focusAttention {
			t.Fatalf("expected 'q' on showAlertsPanel to close panel and keep focusAttention, got showAlertsPanel=%v focused=%v",
				m.showAlertsPanel, m.focused)
		}

		// Re-open AlertsPanel, select second alert (R3-2), and press Enter:
		// must jump to R3-2 in list (NOT open showLabelDrilldown for hidden Attention label!).
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.showAlertsPanel || m.showLabelDrilldown {
			t.Fatalf("Enter in showAlertsPanel left modal open or triggered hidden label drilldown: alerts=%v drilldown=%v",
				m.showAlertsPanel, m.showLabelDrilldown)
		}
		if m.focused != focusList {
			t.Fatalf("expected Enter in showAlertsPanel to jump to list focus, got %v", m.focused)
		}
		sel, ok := m.list.SelectedItem().(IssueItem)
		if !ok || sel.Issue.ID != "R3-2" {
			t.Fatalf("expected Enter on R3-2 alert to select R3-2 in list, got %#v", m.list.SelectedItem())
		}
	})

	// 2. [BUG R3-3B] Dismissing higher modals over showTimeTravelPrompt ('t') or showLabelPicker ('l')
	// must NEVER wedge the TUI, and F2/F5/Ctrl+R must not fire underneath them.
	t.Run("R3_3B_TimeTravelAndLabelPickerNeverWedgeAfterHigherModalDismiss", func(t *testing.T) {
		agentsDir := t.TempDir()
		agentsPath := filepath.Join(agentsDir, "AGENTS.md")
		if err := os.WriteFile(agentsPath, []byte("# Test Agents\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		prompts := []struct {
			name      string
			openKey   string
			wantFocus focus
			isOpen    func(*Model) bool
			inputVal  func(*Model) string
		}{
			{
				name:      "time_travel_prompt",
				openKey:   "t",
				wantFocus: focusTimeTravelInput,
				isOpen:    func(m *Model) bool { return m.showTimeTravelPrompt },
				inputVal:  func(m *Model) string { return m.timeTravelInput.Value() },
			},
			{
				name:      "label_picker",
				openKey:   "l",
				wantFocus: focusLabelPicker,
				isOpen:    func(m *Model) bool { return m.showLabelPicker },
				inputVal:  func(m *Model) string { return m.labelPicker.InputValue() },
			},
		}

		interruptions := []struct {
			name    string
			trigger func(*Model)
			dismiss tea.KeyMsg
		}{
			{
				name: "AgentPrompt_Esc",
				trigger: func(m *Model) {
					m.Update(AgentFileCheckMsg{ShouldPrompt: true, FilePath: agentsPath, FileType: "AGENTS.md"})
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "UpdateModal_Esc",
				trigger: func(m *Model) {
					m.updateModal = NewUpdateModal("v9.9.9", "https://example.com", m.theme)
					m.showUpdateModal = true
					m.focused = focusUpdateModal
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "QuitConfirm_Cancel",
				trigger: func(m *Model) {
					m.showQuitConfirm = true
					m.focused = focusQuitConfirm
				},
				dismiss: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")},
			},
			{
				name: "Help_Esc",
				trigger: func(m *Model) {
					m.showHelp = true
					m.focused = focusHelp
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "Tutorial_Esc",
				trigger: func(m *Model) {
					m.showTutorial = true
					m.focused = focusTutorial
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "CassModal_Esc",
				trigger: func(m *Model) {
					m.showCassModal = true
					m.focused = focusCassModal
					m.cassReturnFocus = focusList
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "LabelDrilldown_Esc",
				trigger: func(m *Model) {
					m.labelDrilldownLabel = "alpha"
					m.labelDrilldownIssues = copyIssues(issues[:1])
					m.showLabelDrilldown = true
				},
				dismiss: tea.KeyMsg{Type: tea.KeyEsc},
			},
			{
				name: "DesynchronizedFocusList",
				trigger: func(m *Model) {
					// Simulate any path leaving m.focused = focusList while overlay bool stays true.
					m.focused = focusList
				},
				dismiss: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")},
			},
		}

		for _, pr := range prompts {
			for _, intr := range interruptions {
				t.Run(pr.name+"_after_"+intr.name, func(t *testing.T) {
					m := NewModel(copyIssues(issues), nil, "")
					t.Cleanup(m.Stop)
					m.Update(tea.WindowSizeMsg{Width: 140, Height: 32})

					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pr.openKey)})
					if !pr.isOpen(m) || m.focused != pr.wantFocus {
						t.Fatalf("failed to open %s: open=%v focused=%v", pr.name, pr.isOpen(m), m.focused)
					}

					// F2 / F5 / Ctrl+R must not toggle sidebar or trigger refresh while prompt/picker is active.
					m.Update(tea.KeyMsg{Type: tea.KeyF2})
					m.Update(tea.KeyMsg{Type: tea.KeyF5})
					m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
					if m.showShortcutsSidebar || m.statusMsg == "Refreshing…" {
						t.Fatalf("F2/F5/Ctrl+R leaked past %s: sidebar=%v status=%q", pr.name, m.showShortcutsSidebar, m.statusMsg)
					}

					intr.trigger(m)
					m.Update(intr.dismiss)

					if !pr.isOpen(m) || m.focused != pr.wantFocus {
						t.Fatalf("%s+%s: expected prompt still open with focus=%v, got open=%v focused=%v",
							pr.name, intr.name, pr.wantFocus, pr.isOpen(m), m.focused)
					}

					// Typing works immediately after modal dismissal.
					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
					if !strings.HasSuffix(pr.inputVal(m), "a") {
						t.Fatalf("%s+%s: typing 'a' after dismiss failed, input=%q", pr.name, intr.name, pr.inputVal(m))
					}

					// Esc cleanly dismisses the prompt/picker with zero wedge!
					m.Update(tea.KeyMsg{Type: tea.KeyEsc})
					if pr.isOpen(m) || m.focused != focusList {
						t.Fatalf("%s+%s: Esc failed to close prompt/picker! open=%v focused=%v",
							pr.name, intr.name, pr.isOpen(m), m.focused)
					}
				})
			}
		}
	})

	// 3. [BUG R3-3B] RecipePicker (7870) vs RepoPicker (7872) Z-order consistency + underlying Board focus preservation
	t.Run("R3_3B_RecipePickerRoutesAboveRepoPickerAndPreservesBoardFocus", func(t *testing.T) {
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		m.workspaceMode = true
		m.availableRepos = []string{"R3", "WEB"}

		// Enter Board view ('b'), then open RepoPicker ('w'), then also stack RecipePicker.
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		if !m.isBoardView || m.focused != focusBoard {
			t.Fatalf("expected focusBoard after 'b', got isBoard=%v focused=%v", m.isBoardView, m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
		if !m.showRepoPicker || m.focused != focusRepoPicker {
			t.Fatalf("expected RepoPicker open, got show=%v focused=%v", m.showRepoPicker, m.focused)
		}

		m.recipePicker = NewRecipePickerModel([]recipe.Recipe{
			{Name: "r1", Description: "First"},
			{Name: "r2", Description: "Second"},
		}, m.theme)
		m.showRecipePicker = true
		m.focused = focusRecipePicker

		// Both showRecipePicker and showRepoPicker are true: renderMainView draws RecipePicker,
		// so 'j' MUST move recipePicker (not repoPicker) and first Esc MUST close RecipePicker first!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if m.recipePicker.SelectedIndex() != 1 || m.repoPicker.selectedIndex != 0 {
			t.Fatalf("expected 'j' to move RecipePicker (got %d) and leave RepoPicker selectedIndex at 0 (got %d)",
				m.recipePicker.SelectedIndex(), m.repoPicker.selectedIndex)
		}

		// First Esc closes RecipePicker and restores focusRepoPicker!
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showRecipePicker || !m.showRepoPicker || m.focused != focusRepoPicker {
			t.Fatalf("first Esc should close RecipePicker and restore focusRepoPicker: recipe=%v repo=%v focused=%v",
				m.showRecipePicker, m.showRepoPicker, m.focused)
		}

		// Second Esc closes RepoPicker and restores underlying focusBoard!
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showRepoPicker || !m.isBoardView || m.focused != focusBoard {
			t.Fatalf("second Esc should close RepoPicker and restore focusBoard: repo=%v board=%v focused=%v",
				m.showRepoPicker, m.isBoardView, m.focused)
		}

		// Also verify AgentFileCheckMsg arriving over focusBoard restores focusBoard on decline!
		agentsPath := filepath.Join(t.TempDir(), "AGENTS.md")
		if err := os.WriteFile(agentsPath, []byte("# Agents\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Update(AgentFileCheckMsg{ShouldPrompt: true, FilePath: agentsPath, FileType: "AGENTS.md"})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.showAgentPrompt || !m.isBoardView || m.focused != focusBoard {
			t.Fatalf("expected declining AgentPrompt over Board view to restore focusBoard, got agent=%v board=%v focused=%v",
				m.showAgentPrompt, m.isBoardView, m.focused)
		}
	})
}

func TestRound4FocusValidationAndRecipeTimeTravelRegressions(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "R4-1", Title: "Alpha issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"alpha"}, CreatedAt: now, UpdatedAt: now},
		{ID: "R4-2", Title: "Beta issue", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"beta"}, CreatedAt: now, UpdatedAt: now},
		{ID: "R4-3", Title: "Gamma issue", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, Labels: []string{"gamma"}, CreatedAt: now, UpdatedAt: now},
	}

	// 1. [BUG R4-3A] Split-View Tab + narrow resize must unwedge focusDetail -> focusList
	t.Run("R4_3A_SplitViewTabNarrowResizeUnwedgesToList", func(t *testing.T) {
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		if !m.isSplitView || m.showDetails {
			t.Fatalf("expected split view with showDetails=false, got isSplit=%v showDetails=%v", m.isSplitView, m.showDetails)
		}

		// Tab focuses the right-hand detail pane in Split View
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		if m.focused != focusDetail || m.showDetails {
			t.Fatalf("expected Tab in split view to set focusDetail with showDetails=false, got focused=%v showDetails=%v",
				m.focused, m.showDetails)
		}

		// Resize narrower than SplitViewThreshold: detail pane disappears, so focus MUST validate back to focusList!
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		if m.isSplitView {
			t.Fatal("expected Width=80 to disable split view")
		}
		if m.focused != focusList {
			t.Fatalf("narrow resize from split-view focusDetail left TUI wedged on %v (want focusList)", m.focused)
		}

		// Verify 'j' navigates the visible list rather than sinking into an invisible viewport
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if m.list.Index() != 1 {
			t.Fatalf("expected 'j' after narrow resize to advance list cursor to 1, got %d", m.list.Index())
		}
	})

	// 2. [BUG R4-3A] Split-View Tab + Help/Tutorial/CassModal + narrow resize + Esc must unwedge to focusList
	t.Run("R4_3A_SplitViewTabModalNarrowResizeEscUnwedgesToList", func(t *testing.T) {
		modals := []struct {
			name   string
			open   func(*Model)
			isOpen func(*Model) bool
		}{
			{
				name: "Help",
				open: func(m *Model) {
					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
				},
				isOpen: func(m *Model) bool { return m.showHelp },
			},
			{
				name: "Tutorial",
				open: func(m *Model) {
					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("`")})
				},
				isOpen: func(m *Model) bool { return m.showTutorial },
			},
			{
				name: "CassModal",
				open: func(m *Model) {
					m.recordSavedViewFocus()
					m.cassReturnFocus = m.focused
					m.showCassModal = true
					m.focused = focusCassModal
				},
				isOpen: func(m *Model) bool { return m.showCassModal },
			},
		}

		for _, tc := range modals {
			t.Run(tc.name, func(t *testing.T) {
				m := NewModel(copyIssues(issues), nil, "")
				t.Cleanup(m.Stop)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

				m.Update(tea.KeyMsg{Type: tea.KeyTab})
				if m.focused != focusDetail {
					t.Fatalf("expected focusDetail after Tab, got %v", m.focused)
				}

				tc.open(m)
				if !tc.isOpen(m) {
					t.Fatalf("expected %s modal to open", tc.name)
				}

				m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
				m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				if tc.isOpen(m) {
					t.Fatalf("expected Esc to close %s modal", tc.name)
				}
				if m.focused != focusList {
					t.Fatalf("%s dismiss after narrow resize restored stale focus %v (want focusList)", tc.name, m.focused)
				}

				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
				if m.list.Index() != 1 {
					t.Fatalf("%s: expected 'j' after dismiss to move list index to 1, got %d", tc.name, m.list.Index())
				}
			})
		}
	})

	// 3. [BUG R4-3A] Board (and Tree) + RepoPicker + stacked Alerts/LabelDrilldown Enter + RepoPicker Esc
	t.Run("R4_3A_BoardRepoPickerStackedAlertsAndLabelDrilldownEnterThenEsc", func(t *testing.T) {
		t.Run("Board_RepoPicker_AlertsEnter_Esc", func(t *testing.T) {
			m := NewModel(copyIssues(issues), nil, "")
			t.Cleanup(m.Stop)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			m.workspaceMode = true
			m.availableRepos = []string{"R4", "WEB"}
			m.alerts = []drift.Alert{
				{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "R4-2", Message: "Critical R4-2"},
			}
			m.dismissedAlerts = make(map[string]bool)

			// Open Board ('b') -> open RepoPicker ('w')
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
			if !m.isBoardView || !m.showRepoPicker || m.focused != focusRepoPicker || m.focusBeforeRepoPicker != focusBoard {
				t.Fatalf("unexpected setup state: isBoard=%v showRepo=%v focused=%v beforeRepo=%v",
					m.isBoardView, m.showRepoPicker, m.focused, m.focusBeforeRepoPicker)
			}

			// Stack AlertsPanel above RepoPicker and press Enter to jump to R4-2
			m.showAlertsPanel = true
			m.alertsCursor = 0
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.showAlertsPanel {
				t.Fatal("expected Enter to close showAlertsPanel")
			}
			if m.isBoardView {
				t.Fatal("expected AlertsPanel Enter jump to exit Board view")
			}
			if !m.showRepoPicker || m.focused != focusRepoPicker {
				t.Fatalf("expected lower RepoPicker to stay focused while still open, got showRepo=%v focused=%v",
					m.showRepoPicker, m.focused)
			}

			// Esc in RepoPicker must validate focusBeforeRepoPicker against live view flags -> focusList (NOT focusBoard!)
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if m.showRepoPicker || m.isBoardView || m.focused != focusList {
				t.Fatalf("expected RepoPicker Esc to land on focusList with isBoardView=false, got showRepo=%v isBoard=%v focused=%v",
					m.showRepoPicker, m.isBoardView, m.focused)
			}
			sel, ok := m.list.SelectedItem().(IssueItem)
			if !ok || sel.Issue.ID != "R4-2" {
				t.Fatalf("expected R4-2 selected in list, got %#v", m.list.SelectedItem())
			}
		})

		t.Run("BoardAndTree_RepoPicker_LabelDrilldownEnter_Esc", func(t *testing.T) {
			for _, startKey := range []string{"b", "E"} {
				m := NewModel(copyIssues(issues), nil, "")
				t.Cleanup(m.Stop)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
				m.workspaceMode = true
				m.availableRepos = []string{"R4", "WEB"}

				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(startKey)})
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
				if !m.showRepoPicker || m.focused != focusRepoPicker {
					t.Fatalf("startKey=%q: expected RepoPicker open, got show=%v focused=%v", startKey, m.showRepoPicker, m.focused)
				}

				m.labelDrilldownLabel = "beta"
				m.labelDrilldownIssues = copyIssues(issues[1:2])
				m.showLabelDrilldown = true

				m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if m.showLabelDrilldown || m.currentFilter != "label:beta" {
					t.Fatalf("startKey=%q: expected LabelDrilldown Enter to apply label:beta filter, show=%v filter=%q",
						startKey, m.showLabelDrilldown, m.currentFilter)
				}
				if !m.showRepoPicker || m.focused != focusRepoPicker {
					t.Fatalf("startKey=%q: expected lower RepoPicker to stay focused, got show=%v focused=%v",
						startKey, m.showRepoPicker, m.focused)
				}

				m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				if m.showRepoPicker || m.isBoardView || m.focused != focusList {
					t.Fatalf("startKey=%q: expected RepoPicker Esc to land on focusList, got showRepo=%v isBoard=%v focused=%v",
						startKey, m.showRepoPicker, m.isBoardView, m.focused)
				}
			}
		})
	})

	// 4. [BUG R4-3B] VelocityComparison (and Sprint) + ShowGraph recipe + Enter + Esc
	t.Run("R4_3B_VelocityComparisonAndSprintShowGraphRecipeEnterAndEsc", func(t *testing.T) {
		graphRecipe := recipe.Recipe{
			Name:        "dep-graph",
			Description: "Dependency graph view",
			View:        recipe.ViewConfig{ShowGraph: true},
		}

		t.Run("VelocityComparison_ShowGraphRecipe", func(t *testing.T) {
			m := NewModel(copyIssues(issues), nil, "")
			t.Cleanup(m.Stop)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			m.recipePicker = NewRecipePickerModel([]recipe.Recipe{graphRecipe}, m.theme)

			// '[' -> LabelDashboard, 'v' -> VelocityComparison
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
			if !m.showVelocityComparison || m.focused != focusVelocityComparison {
				t.Fatalf("expected focusVelocityComparison, got showVel=%v focused=%v", m.showVelocityComparison, m.focused)
			}

			// Open RecipePicker ("'") and press Enter on ShowGraph: true recipe
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("'")})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.showRecipePicker || m.showVelocityComparison || m.isSprintView || !m.isGraphView || m.focused != focusGraph {
				t.Fatalf("expected ShowGraph recipe Enter to switch cleanly to focusGraph: picker=%v vel=%v sprint=%v graph=%v focused=%v",
					m.showRecipePicker, m.showVelocityComparison, m.isSprintView, m.isGraphView, m.focused)
			}

			// Esc exits Graph view back to main list with zero VelocityComparison/LabelDashboard desync
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if m.isGraphView || m.showVelocityComparison || m.focused != focusList {
				t.Fatalf("expected Esc from recipe Graph view to land on focusList: graph=%v vel=%v focused=%v",
					m.isGraphView, m.showVelocityComparison, m.focused)
			}
		})

		t.Run("Sprint_ShowGraphRecipe", func(t *testing.T) {
			m := NewModel(copyIssues(issues), nil, "")
			t.Cleanup(m.Stop)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			m.sprints = []model.Sprint{
				{ID: "S1", Name: "Sprint 1", StartDate: now.Add(-24 * time.Hour), EndDate: now.Add(24 * time.Hour), BeadIDs: []string{"R4-1"}},
			}
			m.recipePicker = NewRecipePickerModel([]recipe.Recipe{graphRecipe}, m.theme)

			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
			if !m.isSprintView || m.focused != focusSprint {
				t.Fatalf("expected focusSprint after 'P', got isSprint=%v focused=%v", m.isSprintView, m.focused)
			}

			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("'")})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.showRecipePicker || m.isSprintView || !m.isGraphView || m.focused != focusGraph {
				t.Fatalf("expected ShowGraph recipe from Sprint to land on focusGraph: picker=%v sprint=%v graph=%v focused=%v",
					m.showRecipePicker, m.isSprintView, m.isGraphView, m.focused)
			}

			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if m.isGraphView || m.isSprintView || m.focused != focusList {
				t.Fatalf("expected Esc from Graph view to land on focusList: graph=%v sprint=%v focused=%v",
					m.isGraphView, m.isSprintView, m.focused)
			}
		})
	})

	// 5. [BUG R4-3B] Insights/FlowMatrix (and secondary views) + non-graph Recipe Enter vs Time-Travel 't' + Esc cancel
	t.Run("R4_3B_SecondaryViewsNonGraphRecipeEnterVsTimeTravelCancel", func(t *testing.T) {
		listRecipe := recipe.Recipe{
			Name:        "open-only",
			Description: "Open list recipe",
			Filters:     recipe.FilterConfig{Status: []string{"open"}},
		}

		secondaryViews := []struct {
			name      string
			enterKeys []string
			wantFocus focus
		}{
			{name: "Insights", enterKeys: []string{"i"}, wantFocus: focusInsights},
			{name: "FlowMatrix", enterKeys: []string{"f"}, wantFocus: focusFlowMatrix},
			{name: "Attention", enterKeys: []string{"]"}, wantFocus: focusAttention},
			{name: "Tree", enterKeys: []string{"E"}, wantFocus: focusTree},
			{name: "LabelDashboard", enterKeys: []string{"["}, wantFocus: focusLabelDashboard},
		}

		for _, sv := range secondaryViews {
			t.Run(sv.name+"_TimeTravelCancelPreservesFocus", func(t *testing.T) {
				m := NewModel(copyIssues(issues), nil, "")
				t.Cleanup(m.Stop)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

				for _, k := range sv.enterKeys {
					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
				}
				if m.focused != sv.wantFocus {
					t.Fatalf("%s: expected focus %v, got %v", sv.name, sv.wantFocus, m.focused)
				}

				// Press 't' to open Time-Travel prompt, then 'Esc' to cancel: MUST stay in sv.wantFocus!
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
				if !m.showTimeTravelPrompt || m.focused != focusTimeTravelInput {
					t.Fatalf("%s: expected Time-Travel prompt open, got show=%v focused=%v",
						sv.name, m.showTimeTravelPrompt, m.focused)
				}
				m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				if m.showTimeTravelPrompt || m.focused != sv.wantFocus {
					t.Fatalf("%s: Time-Travel Esc cancel yanked focus to %v (want %v)",
						sv.name, m.focused, sv.wantFocus)
				}
			})

			t.Run(sv.name+"_NonGraphRecipeEnterSwitchesToList", func(t *testing.T) {
				m := NewModel(copyIssues(issues), nil, "")
				t.Cleanup(m.Stop)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
				m.recipePicker = NewRecipePickerModel([]recipe.Recipe{listRecipe}, m.theme)

				for _, k := range sv.enterKeys {
					m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
				}
				if m.focused != sv.wantFocus {
					t.Fatalf("%s: expected focus %v, got %v", sv.name, sv.wantFocus, m.focused)
				}

				// Open RecipePicker ("'") and press Enter on a non-graph list recipe: MUST land on focusList!
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("'")})
				m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if m.showRecipePicker || m.focused != focusList || m.savedViewFocus != focusList {
					t.Fatalf("%s: non-graph Recipe Enter left show=%v focused=%v savedViewFocus=%v (want focusList)",
						sv.name, m.showRecipePicker, m.focused, m.savedViewFocus)
				}
			})
		}
	})
}
