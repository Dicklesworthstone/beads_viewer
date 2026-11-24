package ui

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/instance"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
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
	snap1 := NewSnapshotBuilder(copyIssues(issues)).Build()
	snap1.Analysis.WaitForPhase2()
	m.Update(SnapshotReadyMsg{Snapshot: snap1, SnapshotVer: 1})
	if got := m.flowMatrix.SelectedDrilldownIssue(); got == nil || got.ID != "dependent" || got.Title != issues[2].Title {
		t.Fatalf("parent snapshot kept stale/wrong endpoint: %+v\n%s", got, m.flowMatrix.View())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "REFRESHED API rollout") {
		t.Fatalf("dependent detail is stale:\n%s", m.viewport.View())
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	issues[0].Status = model.StatusClosed
	snap2 := NewSnapshotBuilder(copyIssues(issues)).Build()
	snap2.Analysis.WaitForPhase2()
	m.Update(SnapshotReadyMsg{Snapshot: snap2, SnapshotVer: 2})
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
			stats.WaitForPhase2()
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
	t.Cleanup(func() { m.Stop() })

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
	t.Cleanup(func() { m.Stop() })

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
	t.Cleanup(func() { m.Stop() })

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
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })
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
	snapRecipe := NewSnapshotBuilder(copyIssues(issues)).WithRecipe(m.activeRecipe).Build()
	snapRecipe.Analysis.WaitForPhase2()
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: snapRecipe})
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
	snapAll := NewSnapshotBuilder(copyIssues(issues)).Build()
	snapAll.Analysis.WaitForPhase2()
	updated, _ = m.Update(SnapshotReadyMsg{Snapshot: snapAll})
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
	snapReloaded := NewSnapshotBuilder(reloaded).Build()
	snapReloaded.Analysis.WaitForPhase2()
	updated, _ = m.Update(SnapshotReadyMsg{Snapshot: snapReloaded})
	m = updated.(*Model)
	m.SetFilter("ready")
	if got := m.FilteredIssues(); len(got) != 2 || m.countReady != 2 {
		t.Errorf("freshly closed external blocker should permit both selected backend issues: ready=%d, rows=%v", m.countReady, got)
	}
}

func TestNewModel_EmptyReadinessSelectionDoesNotSelectContext(t *testing.T) {
	issues := []model.Issue{{ID: "context", Status: model.StatusOpen}}
	m := NewModel(copyIssues(issues), nil, "", ReadinessScope{CandidateIDs: map[string]bool{}})
	t.Cleanup(func() { m.Stop() })
	if len(m.FilteredIssues()) != 1 {
		t.Fatal("empty work selection must preserve exploration context")
	}
	m.SetFilter("ready")
	if len(m.FilteredIssues()) != 0 || m.countReady != 0 {
		t.Fatal("empty candidate map must select no ready work")
	}
	snapEmpty := NewSnapshotBuilder(copyIssues(issues)).Build()
	snapEmpty.Analysis.WaitForPhase2()
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: snapEmpty})
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
	t.Cleanup(func() { m.Stop() })
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
		snapLoaded := NewSnapshotBuilder(loaded).Build()
		snapLoaded.Analysis.WaitForPhase2()
		updated, _ := m.Update(SnapshotReadyMsg{Snapshot: snapLoaded})
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
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })

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
			m.Stop()
			t.Fatalf("Expected %d issues, got %d", len(expected), len(filtered))
		}
		for i, want := range expected {
			if filtered[i].ID != want {
				m.Stop()
				t.Fatalf("Input order %v: position %d = %s, want %s", perm, i, filtered[i].ID, want)
			}
		}
		m.Stop()
	}
}

func TestAttentionView_CloseRestoresInsightsPanel(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Alpha", Status: model.StatusOpen, Priority: 1},
		{ID: "B", Title: "Beta", Status: model.StatusOpen, Priority: 2},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })

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
		defer m.Stop()
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
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })
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
	t.Cleanup(func() { m.Stop() })
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
	fresh := func(t *testing.T) *Model {
		t.Helper()
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
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
		m := press(t, fresh(t), "a")
		if !m.isActionableView || m.focused != focusActionable {
			t.Fatalf("a: actionable=%v focused=%v", m.isActionableView, m.focused)
		}
		m = press(t, m, "a")
		if m.isActionableView || m.focused != focusList {
			t.Fatalf("second a should return to the list: actionable=%v focused=%v", m.isActionableView, m.focused)
		}
	})
	t.Run("E opens the tree view", func(t *testing.T) {
		m := press(t, fresh(t), "E")
		if m.focused != focusTree {
			t.Fatalf("E: focused=%v; want tree", m.focused)
		}
		if m = press(t, m, "E"); m.focused != focusList {
			t.Fatalf("second E should return to the list, focused=%v", m.focused)
		}
	})
	t.Run("' opens the recipe picker", func(t *testing.T) {
		m := press(t, fresh(t), "'")
		if !m.showRecipePicker || m.focused != focusRecipePicker {
			t.Fatalf("': picker=%v focused=%v", m.showRecipePicker, m.focused)
		}
	})
	t.Run("[ opens the label dashboard", func(t *testing.T) {
		m := press(t, fresh(t), "[")
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
		m := press(t, fresh(t), "x")
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
	var exportedReport struct {
		Title           string `json:"title"`
		SourceAuthority any    `json:"source_authority"`
		Issues          []struct {
			ID      string             `json:"id"`
			Actions model.IssueActions `json:"actions"`
		} `json:"issues"`
		Graph struct {
			Nodes int `json:"nodes"`
			Edges int `json:"edges"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(jsonBytes, &exportedReport); err != nil {
		t.Fatalf("invalid JSON export payload: %v\n%s", err, string(jsonBytes))
	}
	if len(exportedReport.Issues) != 2 {
		t.Fatalf("expected 2 open issues in JSON export, got %d", len(exportedReport.Issues))
	}
	if exportedReport.Graph.Nodes != 2 || exportedReport.Graph.Edges != 1 {
		t.Fatalf("expected JSON report graph with 2 nodes and 1 edge, got %+v", exportedReport.Graph)
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
		!strings.Contains(mdContent, "EXP-2 ==> EXP-1") ||
		strings.Contains(mdContent, "EXP-3") {
		t.Fatalf("expected Mermaid dependency graph with EXP-2 ==> EXP-1 and without EXP-3:\n%s", mdContent)
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
		// Verify "bv-apal.46" and "bv-apal_46" do NOT collide onto a self-loop!
		if strings.Contains(mmdText, "bv-apal_46 --> bv-apal_46") || strings.Contains(mmdText, "bv-apal_46 ==> bv-apal_46") {
			t.Fatalf("Mermaid node IDs collided into self-loop:\n%s", mmdText)
		}
		if !strings.Contains(mmdText, "bv-apal.46<br/>") || !strings.Contains(mmdText, "bv-apal_46<br/>") || !strings.Contains(mmdText, "==>") {
			t.Fatalf("expected distinct node definitions and blocking edge between 'bv-apal.46' and 'bv-apal_46', got:\n%s", mmdText)
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
		if !strings.Contains(mdText, "bv-apal.46<br/>") || !strings.Contains(mdText, "bv-apal_46<br/>") || !strings.Contains(mdText, "==>") {
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
		staleSnapAll.Analysis.WaitForPhase2()
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
		staleSnapClosed.Analysis.WaitForPhase2()
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
		if !strings.Contains(mmdText, "proj--1<br/>") || !strings.Contains(mmdText, "123-abc<br/>") || !strings.Contains(mmdText, "==>") {
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

func TestWave10BoardSearchEscTreeToggleAndSplitOverride(t *testing.T) {
	issues := []model.Issue{
		{ID: "AUTH-1", Title: "login config handler", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{
			ID: "AUTH-2", Title: "JSON token parser", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "AUTH-2", DependsOnID: "AUTH-1", Type: model.DepBlocks}},
		},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})

	// 1) Split-view user override ('\' key) survives >=80 resize and forces false <80.
	if !m.isSplitView {
		t.Fatal("expected split view enabled at width 140")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`\`)})
	if m.isSplitView || !m.splitViewUserOverride {
		t.Fatalf("expected '\\' to disable split view and set splitViewUserOverride, got split=%v override=%v",
			m.isSplitView, m.splitViewUserOverride)
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	if m.isSplitView {
		t.Fatal("expected splitViewUserOverride to prevent WindowSizeMsg(160) from re-enabling split view")
	}
	if m.tutorialModel.width != 160 || m.tutorialModel.height != 40 {
		t.Fatalf("expected WindowSizeMsg forwarded to tutorialModel, got %dx%d", m.tutorialModel.width, m.tutorialModel.height)
	}
	// Toggle split view back ON, then verify <80 forces isSplitView=false even with override set.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`\`)})
	if !m.isSplitView {
		t.Fatal("expected '\\' at width 160 to re-enable split view")
	}
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	if m.isSplitView {
		t.Fatal("expected narrow resize (<80) to force isSplitView=false despite user override")
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	if !m.isSplitView {
		t.Fatal("expected splitViewUserWant to restore isSplitView=true when resizing back to 140")
	}

	// 2) Board search accepts 'n', 'N', spaces, UTF-8/multi-rune paste, and layers Esc cleanly.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !m.isBoardView || m.focused != focusBoard {
		t.Fatalf("expected Board view active, got isBoard=%v focused=%v", m.isBoardView, m.focused)
	}
	m.board.ToggleExpand()
	if !m.board.HasExpandedCard() {
		t.Fatal("expected expanded card on board")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.board.IsSearchMode() {
		t.Fatal("expected board search mode active after '/'")
	}
	// Type 'login', space, 'config' (includes 'n' and space)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("login")})
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("config")})
	if got := m.board.GetSearchQuery(); got != "login config" {
		t.Fatalf("expected board search query 'login config' (without 'n' hijacked), got %q", got)
	}
	// Confirm search with Enter: exits searchMode, keeps query & matches.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.board.IsSearchMode() || m.board.GetSearchQuery() != "login config" {
		t.Fatalf("expected Enter to finish search while keeping query, got mode=%v query=%q",
			m.board.IsSearchMode(), m.board.GetSearchQuery())
	}
	// First Esc clears persistent search query, staying on Board with expanded card intact.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.isBoardView || m.board.GetSearchQuery() != "" || !m.board.HasExpandedCard() {
		t.Fatalf("expected 1st Esc to clear search query only, got board=%v query=%q expanded=%v",
			m.isBoardView, m.board.GetSearchQuery(), m.board.HasExpandedCard())
	}
	// Second Esc collapses expanded card, staying on Board.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.isBoardView || m.board.HasExpandedCard() {
		t.Fatalf("expected 2nd Esc to collapse expanded card only, got board=%v expanded=%v",
			m.isBoardView, m.board.HasExpandedCard())
	}
	// Third Esc exits Board back to List.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.isBoardView || m.focused != focusList {
		t.Fatalf("expected 3rd Esc to exit Board to focusList, got board=%v focused=%v", m.isBoardView, m.focused)
	}

	// 3) Tree view 'b'/'m' toggles Hierarchy <-> Blocking Chain while preserving selection.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
	if m.focused != focusTree || m.tree.Mode() != TreeModeHierarchy {
		t.Fatalf("expected focusTree in Hierarchy mode, got focused=%v mode=%v", m.focused, m.tree.Mode())
	}
	m.tree.SelectByID("AUTH-2")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.tree.Mode() != TreeModeBlocking {
		t.Fatalf("expected 'b' in Tree view to switch to TreeModeBlocking, got %v", m.tree.Mode())
	}
	if sel := m.tree.SelectedIssue(); sel == nil || sel.ID != "AUTH-2" {
		t.Fatalf("expected selection AUTH-2 preserved across Tree mode toggle, got %+v", sel)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.tree.Mode() != TreeModeHierarchy {
		t.Fatalf("expected 'm' in Tree view to toggle back to TreeModeHierarchy, got %v", m.tree.Mode())
	}
}

func TestWave10SmallTerminalWindowingRecipeHeadersAndReloadSafety(t *testing.T) {
	now := time.Now().UTC()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	sprintsPath := filepath.Join(beadsDir, "sprints.jsonl")
	initialIssues := `{"id":"API-1","title":"Closed Blocker","status":"closed","priority":1,"issue_type":"task"}` + "\n" +
		`{"id":"WEB-2","title":"Dependent With Very Long Recommended Title For Narrow Actionable Banner Truncation","status":"open","priority":0,"issue_type":"feature","assignee":"alice","labels":["ux"],"dependencies":[{"issue_id":"WEB-2","depends_on_id":"API-1","type":"blocks"}]}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(initialIssues), 0o644); err != nil {
		t.Fatal(err)
	}
	sprintJSON := `{"id":"sp-1","name":"Sprint Alpha","bead_ids":["WEB-2"],"start_date":"2026-11-01T00:00:00Z","end_date":"2026-11-20T00:00:00Z"}` + "\n"
	if err := os.WriteFile(sprintsPath, []byte(sprintJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	issues := []model.Issue{
		{ID: "API-1", Title: "Closed Blocker", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{
			ID: "WEB-2", Title: "Dependent With Very Long Recommended Title For Narrow Actionable Banner Truncation",
			Status: model.StatusOpen, Priority: 0, IssueType: model.TypeFeature, Assignee: "alice", Labels: []string{"ux"},
			Dependencies: []*model.Dependency{{IssueID: "WEB-2", DependsOnID: "API-1", Type: model.DepBlocks}},
			CreatedAt:    now, UpdatedAt: now,
		},
	}

	m := NewModel(issues, nil, beadsPath)
	t.Cleanup(m.Stop)
	m.backgroundWorker = nil
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})

	if len(m.sprints) != 1 || !m.sprintsLoadedFromDisk {
		t.Fatalf("expected initial sprint loaded from sprints.jsonl, got len=%d fromDisk=%v", len(m.sprints), m.sprintsLoadedFromDisk)
	}

	// 1) Small-terminal (24 rows) modal scrolling in RecipePicker, RepoPicker, and AlertsPanel.
	var manyRecipes []recipe.Recipe
	var manyRepos []string
	var manyAlerts []drift.Alert
	for i := 0; i < 18; i++ {
		manyRecipes = append(manyRecipes, recipe.Recipe{Name: fmt.Sprintf("rec-%02d", i), Description: "Desc"})
		manyRepos = append(manyRepos, fmt.Sprintf("repo-%02d", i))
		manyAlerts = append(manyAlerts, drift.Alert{
			Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning,
			Message: fmt.Sprintf("Alert message %02d", i), IssueID: "WEB-2",
		})
	}
	rp := NewRecipePickerModel(manyRecipes, m.theme)
	rp.SetSize(80, 24)
	for i := 0; i < 15; i++ {
		rp.MoveDown()
	}
	if out := rp.View(); !strings.Contains(out, "rec-15") || !strings.Contains(out, "↑") {
		t.Fatalf("expected 24-row RecipePicker to scroll to rec-15 with ↑ indicator, got:\n%s", out)
	}

	repoP := NewRepoPickerModel(manyRepos, m.theme)
	repoP.SetSize(80, 24)
	for i := 0; i < 15; i++ {
		repoP.MoveDown()
	}
	if out := repoP.View(); !strings.Contains(out, "repo-15") || !strings.Contains(out, "↑") {
		t.Fatalf("expected 24-row RepoPicker to scroll to repo-15 with ↑ indicator, got:\n%s", out)
	}

	m.alerts = manyAlerts
	m.alertsWarning = len(manyAlerts)
	m.alertsCursor = 15
	if out := m.renderAlertsPanel(); !strings.Contains(out, "Alert message 15") || !strings.Contains(out, "↑") {
		t.Fatalf("expected 24-row AlertsPanel to window around cursor 15 with ↑ indicator, got:\n%s", out)
	}

	// 2) LabelDashboard cursor/scroll clamping on shrink & resize, and Actionable banner truncation.
	ld := NewLabelDashboardModel(m.theme)
	ld.SetSize(80, 10)
	ld.SetData([]analysis.LabelHealth{
		{Label: "l1"}, {Label: "l2"}, {Label: "l3"}, {Label: "l4"}, {Label: "l5"},
	})
	ld.cursor = 4
	ld.scrollOffset = 3
	ld.SetData([]analysis.LabelHealth{{Label: "l1"}})
	if ld.cursor != 0 || ld.scrollOffset != 0 {
		t.Fatalf("expected LabelDashboard cursor/scroll clamped on shrink, got cursor=%d offset=%d", ld.cursor, ld.scrollOffset)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	actOut := m.actionableView.Render()
	for _, line := range strings.Split(actOut, "\n") {
		if w := ansi.StringWidth(line); w > 90 {
			t.Fatalf("expected ActionableModel line width <= 90, got %d: %q", w, line)
		}
	}
	if got := m.focusedIssueForSessions(); got == nil || got.ID != "WEB-2" {
		t.Fatalf("expected focusedIssueForSessions in focusActionable to return WEB-2, got %+v", got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// 3) Workspace REPO header, Recipe Columns + ShowMetrics + GroupBy assignee, and Detail Recipe Metrics / resolved blocker label.
	m.workspaceMode = true
	customRec := &recipe.Recipe{
		Name: "metrics-by-assignee",
		View: recipe.ViewConfig{
			Columns:     []string{"id", "priority", "title", "assignee"},
			ShowMetrics: true,
			GroupBy:     "assignee",
		},
	}
	m.setActiveRecipe(customRec)
	m.applyRecipe(customRec)
	hdr := m.formatListHeader(100, false)
	for _, col := range []string{"REPO", "PRI", "ASSIGNEE", "METRICS"} {
		if !strings.Contains(hdr, col) {
			t.Fatalf("expected formatListHeader to include %s, got %q", col, hdr)
		}
	}
	if key := m.recipeGroupKey(*m.issueMap["WEB-2"]); key != "@alice" {
		t.Fatalf("expected recipeGroupKey(assignee) == '@alice', got %q", key)
	}
	m.selectVisibleListItemByID("WEB-2")
	m.isSplitView = true
	m.updateViewportContent()
	vpOut := ansi.Strip(m.viewportContent)
	if !strings.Contains(vpOut, "Recipe Metrics") {
		t.Fatalf("expected viewport content to include Recipe Metrics when ShowMetrics=true, got:\n%s", vpOut)
	}
	if !strings.Contains(vpOut, "resolved") {
		t.Fatalf("expected closed blocker API-1 marked resolved in detail pane, got:\n%s", vpOut)
	}

	// 4) Deleting sprints.jsonl clears disk-loaded sprints on FileChangedMsg without dangling pool refs.
	m.openSprintView()
	if !m.isSprintView || m.selectedSprint == nil {
		t.Fatal("expected sprint view active before deleting sprints.jsonl")
	}
	// Remove sprints.jsonl entry by truncating/emptying (Rule 1 safe: no file deletion needed; also test non-existent path via beadsPath in empty dir).
	if err := os.WriteFile(sprintsPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(FileChangedMsg{})
	if len(m.sprints) != 0 || m.selectedSprint != nil || !strings.Contains(m.sprintViewText, "No sprint selected") {
		t.Fatalf("expected emptied sprints.jsonl to clear m.sprints & m.selectedSprint, got len=%d sel=%+v text=%q",
			len(m.sprints), m.selectedSprint, m.sprintViewText)
	}
	// Guard Enter on focusSprint when selectedSprint == nil: must be a safe no-op.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.isSprintView || m.focused != focusSprint {
		t.Fatalf("expected Enter with nil selectedSprint to stay in focusSprint, got sprint=%v focused=%v", m.isSprintView, m.focused)
	}
}

func TestWave10MissingSprintsAndFocusRouting(t *testing.T) {
	now := time.Now().UTC()
	dirNoSprint := t.TempDir()
	beadsDir := filepath.Join(dirNoSprint, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	raw := `{"id":"ISS-1","title":"Blocker","status":"open","priority":0,"issue_type":"task","labels":["core"]}` + "\n" +
		`{"id":"ISS-2","title":"Dependent","status":"open","priority":1,"issue_type":"task","labels":["core"],"dependencies":[{"issue_id":"ISS-2","depends_on_id":"ISS-1","type":"blocks"}]}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	issues := []model.Issue{
		{
			ID: "ISS-1", Title: "Blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask,
			Labels: []string{"core"}, Description: strings.Repeat("line\n", 80), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "ISS-2", Title: "Dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Labels:       []string{"core"},
			Dependencies: []*model.Dependency{{IssueID: "ISS-2", DependsOnID: "ISS-1", Type: model.DepBlocks}},
			CreatedAt:    now, UpdatedAt: now,
		},
	}

	m := NewModel(issues, nil, beadsPath)
	t.Cleanup(m.Stop)
	m.backgroundWorker = nil
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})

	// 1) Simulate disk-loaded sprint whose sprints.jsonl file no longer exists on disk.
	m.sprints = []model.Sprint{{ID: "s-gone", Name: "Gone Sprint", BeadIDs: []string{"ISS-1"}}}
	m.sprintsLoadedFromDisk = true
	m.openSprintView()
	m.Update(FileChangedMsg{})
	if len(m.sprints) != 0 || m.selectedSprint != nil || m.sprintsLoadedFromDisk {
		t.Fatalf("expected missing sprints.jsonl to clear disk-loaded sprints, got len=%d sel=%+v fromDisk=%v",
			len(m.sprints), m.selectedSprint, m.sprintsLoadedFromDisk)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// 2) focusDetail g/G viewport navigation and view switches (b -> Board).
	m.focused = focusDetail
	m.updateViewportContent()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.viewport.YOffset == 0 && m.viewport.TotalLineCount() > m.viewport.Height {
		t.Fatal("expected 'G' in focusDetail to scroll viewport to bottom")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.viewport.YOffset != 0 {
		t.Fatalf("expected 'g' in focusDetail to scroll viewport to top, got YOffset=%d", m.viewport.YOffset)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !m.isBoardView || m.focused != focusBoard {
		t.Fatalf("expected 'b' in focusDetail to switch to Board view, got board=%v focused=%v", m.isBoardView, m.focused)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// 3) focusedIssueForSessions in focusGraph and focusInsights, plus g/G/ctrl+d/ctrl+u in Insights.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.focused != focusGraph {
		t.Fatalf("expected focusGraph, got %v", m.focused)
	}
	if got := m.focusedIssueForSessions(); got == nil {
		t.Fatal("expected focusedIssueForSessions() non-nil in focusGraph")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if m.focused != focusInsights {
		t.Fatalf("expected focusInsights, got %v", m.focused)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	_ = m.focusedIssueForSessions()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// 4) showVelocityComparison cleared when applying a LabelPicker selection.
	m.showVelocityComparison = true
	m.focused = focusVelocityComparison
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.showVelocityComparison {
		t.Fatal("expected showVelocityComparison cleared after applying LabelPicker")
	}

	// 5) Phase2ReadyMsg refreshes active Board/Actionable/Sprint views without panic.
	m.isActionableView = true
	m.isBoardView = true
	m.Update(Phase2ReadyMsg{})
}

func TestListPaneWidthCalculation(t *testing.T) {
	issues := []model.Issue{
		{ID: "PANE-1", Title: "Split pane width verification", Status: model.StatusOpen, Priority: 1, Description: "Detailed body content"},
		{ID: "PANE-2", Title: "Secondary list item", Status: model.StatusInProgress, Priority: 2},
	}
	m := NewModel(copyIssues(issues), nil, "")
	t.Cleanup(m.Stop)

	for _, w := range []int{70, 100, 120, 160} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		wantSplit := w > SplitViewThreshold
		if m.isSplitView != wantSplit {
			t.Fatalf("width=%d: isSplitView=%v, want %v", w, m.isSplitView, wantSplit)
		}
		fullOut := m.View()
		if strings.TrimSpace(fullOut) == "" || !strings.Contains(fullOut, "PANE-1") {
			t.Fatalf("width=%d: m.View() missing PANE-1:\n%s", w, fullOut)
		}
		for idx, ln := range strings.Split(fullOut, "\n") {
			if gotW := lipgloss.Width(ln); gotW > w {
				t.Fatalf("width=%d: m.View() line %d width=%d > %d: %q", w, idx, gotW, w, ln)
			}
		}
		if m.isSplitView {
			if m.list.Width() <= 0 || m.viewport.Width <= 0 || m.list.Width()+m.viewport.Width >= w {
				t.Fatalf("width=%d: invalid split pane widths list=%d viewport=%d", w, m.list.Width(), m.viewport.Width)
			}
			splitOut := m.renderSplitView()
			plainSplit := ansi.Strip(splitOut)
			if strings.TrimSpace(plainSplit) == "" || !strings.Contains(plainSplit, "PANE-1") || !strings.Contains(plainSplit, "Detailed body content") {
				t.Fatalf("width=%d: m.renderSplitView() missing list/detail content:\n%s", w, plainSplit)
			}
			for idx, ln := range strings.Split(splitOut, "\n") {
				if gotW := lipgloss.Width(ln); gotW > w {
					t.Fatalf("width=%d: m.renderSplitView() line %d width=%d > %d: %q", w, idx, gotW, w, ln)
				}
			}
		}
	}
}

func TestWave10Round1AuditRegressions(t *testing.T) {
	// B-5: 100 -> '\' (enable split view) -> 60 (forced false) -> 140 (restored true via splitViewUserWant!)
	t.Run("B5_SplitViewUserWantRestoresAfterNarrowThenWideResize", func(t *testing.T) {
		issues := []model.Issue{{ID: "SV-1", Title: "Split restore", Status: model.StatusOpen, Priority: 1}}
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)

		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		if m.isSplitView {
			t.Fatal("expected auto split view false at width 100 (<= SplitViewThreshold)")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`\`)})
		if !m.isSplitView || !m.splitViewUserOverride || !m.splitViewUserWant {
			t.Fatalf("expected '\\' at width 100 to enable split view and set splitViewUserWant=true, got split=%v override=%v want=%v",
				m.isSplitView, m.splitViewUserOverride, m.splitViewUserWant)
		}
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
		if m.isSplitView || !m.splitViewUserWant {
			t.Fatalf("expected width 60 to force isSplitView=false while preserving splitViewUserWant=true, got split=%v want=%v",
				m.isSplitView, m.splitViewUserWant)
		}
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
		if !m.isSplitView {
			t.Fatal("expected width 140 resize to restore m.isSplitView=true from splitViewUserWant")
		}
	})

	// B-4: Phase2ReadyMsg & Phase2UpdateMsg preserve active Board '/' search, expanded card, and repo-scoped insightsPanel.
	t.Run("B4_Phase2PreservesBoardSearchExpandedCardAndScopedInsights", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "api-1", Title: "API OAuth gateway", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, SourceRepo: "api"},
			{
				ID: "api-2", Title: "API OAuth token store", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api",
				Dependencies: []*model.Dependency{{IssueID: "api-2", DependsOnID: "api-1", Type: model.DepBlocks}},
			},
			{ID: "web-1", Title: "Web OAuth button", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, SourceRepo: "web"},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.workspaceMode = true
		m.activeRepos = map[string]bool{"api": true}
		m.Update(tea.WindowSizeMsg{Width: 130, Height: 34})
		m.applyFilter()
		m.rebuildInsightsPanel()

		// Enter Board view, expand api-1, start '/' search for "oauth", and step to 2nd match.
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		if !m.board.SelectIssueByID("api-1") {
			t.Fatal("expected api-1 on board")
		}
		m.board.ToggleExpand()
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		for _, ch := range "oauth" {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		}
		m.board.NextMatch()
		if !m.board.IsSearchMode() || m.board.GetSearchQuery() != "oauth" || m.board.SearchCursorPos() != 2 {
			t.Fatalf("precondition failed: mode=%v query=%q pos=%d",
				m.board.IsSearchMode(), m.board.GetSearchQuery(), m.board.SearchCursorPos())
		}

		// Deliver Phase2ReadyMsg: Board search + expanded card + repo-scoped insightsPanel must all survive!
		m.Update(WaitForPhase2Cmd(m.analysis)())
		if !m.board.IsSearchMode() || m.board.GetSearchQuery() != "oauth" || m.board.SearchCursorPos() != 2 {
			t.Fatalf("Phase2ReadyMsg clobbered active Board search: mode=%v query=%q pos=%d",
				m.board.IsSearchMode(), m.board.GetSearchQuery(), m.board.SearchCursorPos())
		}
		if !m.board.HasExpandedCard() || m.board.GetExpandedID() != "api-1" {
			t.Fatalf("Phase2ReadyMsg collapsed expanded card api-1: got %q", m.board.GetExpandedID())
		}
		insOut := ansi.Strip(m.insightsPanel.View())
		if strings.Contains(insOut, "web-1") {
			t.Fatalf("Phase2ReadyMsg overwrote repo-scoped insightsPanel with unscoped web-1:\n%s", insOut)
		}
	})

	// B-2: exportToMarkdown passes Readiness, AuthorityComplete, and activeRecipe.View (GroupBy + claimable actions).
	t.Run("B2_ExportToMarkdownReadinessClaimableAndRecipeGroupBy", func(t *testing.T) {
		exportDir := t.TempDir()
		beadsDir := filepath.Join(exportDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(beadsDir, "beads.db")
		now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

		mkOrigin := func(localID string) *model.IssueOrigin {
			return &model.IssueOrigin{
				LocalID:          localID,
				WorkingDirectory: exportDir,
				TrackerDirectory: beadsDir,
				Database:         dbPath,
				Tracker:          "br",
				Executable:       "br",
				SupportsClaim:    true,
			}
		}
		issues := []model.Issue{
			{
				ID: "READY-P2", Title: "Ready P2 Task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
				CreatedAt: now, UpdatedAt: now, Origin: mkOrigin("READY-P2"),
			},
			{
				ID: "BLOCKED-P0", Title: "Blocked P0 Task", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug,
				CreatedAt: now, UpdatedAt: now, Origin: mkOrigin("BLOCKED-P0"),
				Dependencies: []*model.Dependency{{IssueID: "BLOCKED-P0", DependsOnID: "READY-P2", Type: model.DepBlocks}},
			},
		}

		m := NewModel(copyIssues(issues), nil, filepath.Join(beadsDir, "issues.jsonl"))
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// 1) Default export (activeRecipe == nil) passes Readiness + AuthorityComplete:
		// claimable READY-P2 gets '--claim', whereas blocked BLOCKED-P0 does NOT!
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("default exportToMarkdown failed: %s", m.statusMsg)
		}
		outPath := filepath.Join(exportDir, m.generateExportFilename())
		defaultMDBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		defaultMD := string(defaultMDBytes)
		if !strings.Contains(defaultMD, "'--claim' '--' 'READY-P2'") {
			t.Fatalf("expected default exportToMarkdown with Readiness+AuthorityComplete to emit claim action for READY-P2:\n%s", defaultMD)
		}
		if strings.Contains(defaultMD, "'--claim' '--' 'BLOCKED-P0'") {
			t.Fatalf("blocked issue BLOCKED-P0 must NOT get claim action:\n%s", defaultMD)
		}

		// 2) Recipe export with View.GroupBy == "priority" honors GroupBy AND Readiness!
		grpRec := &recipe.Recipe{
			Name: "grouped-md",
			View: recipe.ViewConfig{
				GroupBy: "priority",
			},
			Export: recipe.ExportConfig{Format: "markdown"},
		}
		m.setActiveRecipe(grpRec)
		m.applyRecipe(grpRec)
		m.exportToMarkdown()
		if m.statusIsError {
			t.Fatalf("recipe exportToMarkdown failed: %s", m.statusMsg)
		}
		groupedMDBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		groupedMD := string(groupedMDBytes)
		if !strings.Contains(groupedMD, "## Group: 🔥 Critical (P0)") || !strings.Contains(groupedMD, "## Group: 🔹 Medium (P2)") {
			t.Fatalf("expected priority-grouped markdown to contain ## Group: P0 and P2 headings:\n%s", groupedMD)
		}
		if !strings.Contains(groupedMD, "'--claim' '--' 'READY-P2'") || strings.Contains(groupedMD, "'--claim' '--' 'BLOCKED-P0'") {
			t.Fatalf("expected recipe exportToMarkdown to preserve claimable Readiness semantics:\n%s", groupedMD)
		}
	})

	// B-6: Board Esc priority (detail -> search -> expanded -> exit), 24-row Alerts windowing, and Closed-filter open blocker.
	t.Run("B6_BoardEscDetailFirst24RowAlertsAndClosedBlocker", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "OPEN-BLK", Title: "Open Blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug},
			{
				ID: "CLOSED-1", Title: "Closed Dependent", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{{IssueID: "CLOSED-1", DependsOnID: "OPEN-BLK", Type: model.DepBlocks}},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

		// Under 'c' (Closed filter), OPEN-BLK is still open and must be counted as active (NOT resolved).
		m.currentFilter = "closed"
		m.applyFilter()
		actBlk, resBlk := m.countIssueBlockers(issues[1])
		if actBlk != 1 || resBlk != 0 {
			t.Fatalf("expected CLOSED-1 with open blocker OPEN-BLK to have active=1 resolved=0, got active=%d resolved=%d", actBlk, resBlk)
		}

		// Board Esc priority: HideDetail() FIRST before clearing search, collapsing card, or exiting Board.
		m.currentFilter = "all"
		m.applyFilter()
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		m.board.ToggleExpand()
		m.board.StartSearch()
		m.board.AppendSearchChar('o')
		m.board.FinishSearch()
		m.board.ShowDetail()
		if !m.board.IsDetailShown() || m.board.GetSearchQuery() != "o" || !m.board.HasExpandedCard() {
			t.Fatal("precondition failed for layered Board Esc")
		}

		// 1st Esc: hides detail panel, preserves search query + expanded card + Board view.
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.board.IsDetailShown() || m.board.GetSearchQuery() != "o" || !m.board.HasExpandedCard() || !m.isBoardView {
			t.Fatalf("1st Esc should only hide detail: detail=%v query=%q expanded=%v board=%v",
				m.board.IsDetailShown(), m.board.GetSearchQuery(), m.board.HasExpandedCard(), m.isBoardView)
		}
		// 2nd Esc: clears search query, preserves expanded card + Board view.
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.board.GetSearchQuery() != "" || !m.board.HasExpandedCard() || !m.isBoardView {
			t.Fatalf("2nd Esc should clear search query: query=%q expanded=%v board=%v",
				m.board.GetSearchQuery(), m.board.HasExpandedCard(), m.isBoardView)
		}
		// 3rd Esc: collapses expanded card, preserves Board view.
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.board.HasExpandedCard() || !m.isBoardView {
			t.Fatalf("3rd Esc should collapse expanded card: expanded=%v board=%v",
				m.board.HasExpandedCard(), m.isBoardView)
		}
		// 4th Esc: exits Board view.
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.isBoardView {
			t.Fatal("4th Esc should exit Board view")
		}

		// 24-row Alerts Panel with 12 multi-line alerts + full selected details must NEVER clip '▸ ' or 'Esc: close'.
		m.alerts = nil
		for i := 0; i < 12; i++ {
			m.alerts = append(m.alerts, drift.Alert{
				Type:                  drift.AlertBlockingCascade,
				Severity:              drift.SeverityCritical,
				IssueID:               fmt.Sprintf("OPEN-%02d", i),
				RelatedIssueID:        "CLOSED-1",
				UnblocksCount:         4,
				DownstreamPrioritySum: 7,
				Message:               fmt.Sprintf("Alert %02d has a long multi-clause diagnostic description wrapping across narrow columns cleanly", i),
				SuggestedAction:       "Unblock the upstream dependency and re-triage downstream priority cascade",
			})
		}
		m.alertsCritical = 12
		m.showAlertsPanel = true
		for cur := 0; cur < 12; cur++ {
			m.alertsCursor = cur
			panel := ansi.Strip(m.renderAlertsPanel())
			if !strings.Contains(panel, fmt.Sprintf("▸ ⚠ Alert %02d", cur)) {
				t.Fatalf("cursor=%d: 24-row renderAlertsPanel clipped selected cursor:\n%s", cur, panel)
			}
			if !strings.Contains(panel, "Esc: close") {
				t.Fatalf("cursor=%d: 24-row renderAlertsPanel clipped footer 'Esc: close':\n%s", cur, panel)
			}
		}
	})
}

func TestWave10Round2_ExportAuthorityTreeReloadAndBoardQuit(t *testing.T) {
	t.Run("R2_B1_ExportToMarkdownGatesAuthorityComplete", func(t *testing.T) {
		projDir := t.TempDir()
		beadsDir := filepath.Join(projDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")
		validLine := `{"id":"api-1","title":"Claimable API Task","status":"open","priority":1,"issue_type":"task"}` + "\n"
		if err := os.WriteFile(beadsPath, []byte(validLine), 0o644); err != nil {
			t.Fatal(err)
		}

		dbPath := filepath.Join(beadsDir, "beads.db")
		makeIssues := func() []model.Issue {
			return []model.Issue{
				{
					ID:        "api-1",
					Title:     "Claimable API Task",
					Status:    model.StatusOpen,
					Priority:  1,
					IssueType: model.TypeTask,
					Origin: &model.IssueOrigin{
						LocalID:          "api-1",
						WorkingDirectory: projDir,
						TrackerDirectory: beadsDir,
						Database:         dbPath,
						Tracker:          "br",
						Executable:       "br",
						SupportsClaim:    true,
					},
				},
			}
		}
		grpRec := &recipe.Recipe{
			Name:   "prio-export",
			View:   recipe.ViewConfig{GroupBy: "priority"},
			Export: recipe.ExportConfig{Format: "markdown"},
		}
		readExport := func(m *Model) string {
			t.Helper()
			m.exportToMarkdown()
			if m.statusIsError {
				t.Fatalf("exportToMarkdown failed: %s", m.statusMsg)
			}
			outPath := filepath.Join(m.workDir, m.generateExportFilename())
			raw, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}

		// Clean TUI session: AuthorityComplete == true even with list, repo, and recipe filters.
		clean := NewModel(makeIssues(), nil, beadsPath)
		t.Cleanup(clean.Stop)
		clean.workDir = t.TempDir()
		clean.currentFilter = "open"
		clean.applyFilter()
		clean.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, FailedCount: 0, RepoPrefixes: []string{"api", "web"}})
		clean.activeRepos = map[string]bool{"api": true}
		if md := readExport(clean); !strings.Contains(md, "'--claim' '--' 'api-1'") {
			t.Fatalf("clean filtered TUI session should emit --claim:\n%s", md)
		}
		clean.setActiveRecipe(grpRec)
		clean.applyRecipe(grpRec)
		if md := readExport(clean); !strings.Contains(md, "'--claim' '--' 'api-1'") {
			t.Fatalf("clean recipe-filtered TUI session should emit --claim:\n%s", md)
		}

		// 1) ReadinessScope{AuthorityIncomplete: true} (e.g., bv --as-of or degraded startup)
		asOf := NewModel(makeIssues(), nil, "", ReadinessScope{AuthorityIncomplete: true})
		t.Cleanup(asOf.Stop)
		asOf.workDir = t.TempDir()
		if md := readExport(asOf); strings.Contains(md, "--claim") {
			t.Fatalf("ReadinessScope{AuthorityIncomplete: true} must omit --claim:\n%s", md)
		}
		asOf.setActiveRecipe(grpRec)
		asOf.applyRecipe(grpRec)
		if md := readExport(asOf); strings.Contains(md, "--claim") {
			t.Fatalf("recipe export with ReadinessScope{AuthorityIncomplete: true} must omit --claim:\n%s", md)
		}

		// 2) Time-Travel mode (m.timeTravelMode == true)
		tt := NewModel(makeIssues(), nil, beadsPath)
		t.Cleanup(tt.Stop)
		tt.workDir = t.TempDir()
		tt.timeTravelMode = true
		if md := readExport(tt); strings.Contains(md, "--claim") {
			t.Fatalf("timeTravelMode must omit --claim:\n%s", md)
		}

		// 3) Partial workspace failure (WorkspaceInfo{FailedCount: 1})
		wsFail := NewModel(makeIssues(), nil, beadsPath)
		t.Cleanup(wsFail.Stop)
		wsFail.workDir = t.TempDir()
		wsFail.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, FailedCount: 1, RepoPrefixes: []string{"api", "web"}})
		if md := readExport(wsFail); strings.Contains(md, "--claim") {
			t.Fatalf("partial workspace (FailedCount=1) must omit --claim:\n%s", md)
		}

		// 4) Stale source reason (m.sourceStaleReason != "")
		stale := NewModel(makeIssues(), nil, beadsPath)
		t.Cleanup(stale.Stop)
		stale.workDir = t.TempDir()
		stale.sourceStaleReason = "reload failed"
		if md := readExport(stale); strings.Contains(md, "--claim") {
			t.Fatalf("stale source must omit --claim:\n%s", md)
		}

		// 5) Warned FileChangedMsg (corrupt line in JSONL) AND SourceReloadResult{AuthorityIncomplete: true}
		warned := NewModel(makeIssues(), nil, beadsPath)
		t.Cleanup(warned.Stop)
		warned.workDir = t.TempDir()
		if err := os.WriteFile(beadsPath, []byte(validLine+"{corrupt json line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		updated, _ := warned.Update(FileChangedMsg{})
		warned = updated.(*Model)
		if md := readExport(warned); strings.Contains(md, "--claim") {
			t.Fatalf("warned FileChangedMsg reload must omit --claim:\n%s", md)
		}

		srcReload := NewModel(makeIssues(), nil, "")
		t.Cleanup(srcReload.Stop)
		srcReload.workDir = t.TempDir()
		srcReload.SetSourceReload(func(context.Context) (SourceReloadResult, error) {
			return SourceReloadResult{
				Issues:              makeIssues(),
				Authority:           model.NewReadinessIndex(makeIssues()),
				AuthorityIncomplete: true,
			}, nil
		})
		_, cmd := srcReload.Update(FileChangedMsg{})
		if cmd == nil {
			t.Fatal("expected sourceReloadCmd")
		}
		updatedSrc, _ := srcReload.Update(cmd())
		srcReload = updatedSrc.(*Model)
		if md := readExport(srcReload); strings.Contains(md, "--claim") {
			t.Fatalf("SourceReloadResult{AuthorityIncomplete: true} must omit --claim:\n%s", md)
		}
	})

	t.Run("R2_B3_UnfocusedBuiltTreeRefreshesOnFileChangedAndSnapshotReady", func(t *testing.T) {
		projDir := t.TempDir()
		beadsDir := filepath.Join(projDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")
		initialJSONL := strings.Join([]string{
			`{"id":"TREE-1","title":"OriginalRootTitle","status":"open","priority":0,"issue_type":"epic"}`,
			`{"id":"TREE-2","title":"ChildTask","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"TREE-2","depends_on_id":"TREE-1","type":"parent-child"}]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(initialJSONL), 0o644); err != nil {
			t.Fatal(err)
		}

		loaded, err := loader.LoadIssuesFromFileWithOptionsPooled(beadsPath, loader.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		m := NewModel(loaded.Issues, nil, beadsPath)
		m.pooledIssues = loaded.PoolRefs
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})

		// Open Tree ('E'), verify built & initial title, then exit to List ('Esc').
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
		if m.focused != focusTree || !m.tree.IsBuilt() {
			t.Fatalf("expected focusTree with built tree, got focused=%v built=%v", m.focused, m.tree.IsBuilt())
		}
		if !strings.Contains(ansi.Strip(m.tree.View()), "OriginalRootTitle") {
			t.Fatalf("expected OriginalRootTitle in initial tree view:\n%s", ansi.Strip(m.tree.View()))
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.focused != focusList {
			t.Fatalf("expected focusList after Esc from Tree, got %v", m.focused)
		}

		// Reload via FileChangedMsg while Tree is unfocused (recycles oldPooled).
		updatedJSONL := strings.Join([]string{
			`{"id":"TREE-1","title":"ReloadedSyncRootTitle","status":"open","priority":0,"issue_type":"epic"}`,
			`{"id":"TREE-2","title":"ChildTask","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"TREE-2","depends_on_id":"TREE-1","type":"parent-child"}]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(updatedJSONL), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Update(FileChangedMsg{})

		// Re-open Tree ('E'): must show ReloadedSyncRootTitle with zero stale pooled pointers.
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
		treeAfterSync := ansi.Strip(m.tree.View())
		if !strings.Contains(treeAfterSync, "ReloadedSyncRootTitle") || strings.Contains(treeAfterSync, "OriginalRootTitle") {
			t.Fatalf("expected unfocused built tree refreshed on FileChangedMsg:\n%s", treeAfterSync)
		}

		// Exit to List again ('Esc') and deliver SnapshotReadyMsg with updated title.
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		snapJSONL := strings.Join([]string{
			`{"id":"TREE-1","title":"SnapshotRootTitle","status":"open","priority":0,"issue_type":"epic"}`,
			`{"id":"TREE-2","title":"ChildTask","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"TREE-2","depends_on_id":"TREE-1","type":"parent-child"}]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(snapJSONL), 0o644); err != nil {
			t.Fatal(err)
		}
		snapLoaded, err := loader.LoadIssuesFromFileWithOptionsPooled(beadsPath, loader.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		snap := NewSnapshotBuilder(snapLoaded.Issues).Build()
		snap.Analysis.WaitForPhase2()
		snap.DataHash = "snap-hash-v2"
		snap.attachPooledIssues(snapLoaded.PoolRefs)
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 2})

		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
		treeAfterSnap := ansi.Strip(m.tree.View())
		if !strings.Contains(treeAfterSnap, "SnapshotRootTitle") || strings.Contains(treeAfterSnap, "ReloadedSyncRootTitle") {
			t.Fatalf("expected unfocused built tree refreshed on SnapshotReadyMsg:\n%s", treeAfterSnap)
		}
	})

	t.Run("R2_B4_BoardQuitHidesDetailPanel", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "BRD-1", Title: "Board Quit Card", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		}
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		m.board.ShowDetail()
		m.board.ToggleExpand()
		m.board.StartSearch()
		m.board.AppendSearchChar('b')
		m.board.FinishSearch()

		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
		if m.isBoardView || m.board.IsDetailShown() || m.board.HasExpandedCard() || m.board.GetSearchQuery() != "" {
			t.Fatalf("'q' in Board must exit and clear detail/expanded/search: board=%v detail=%v expanded=%v query=%q",
				m.isBoardView, m.board.IsDetailShown(), m.board.HasExpandedCard(), m.board.GetSearchQuery())
		}
	})

	t.Run("R3_B1a_SourceDegradedRetainsAuthorityIncompleteAcrossSyncAndSnapshotReloads", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")
		raw := `{"id":"DEG-1","title":"Fallback JSONL Issue","status":"open","priority":1,"issue_type":"task"}` + "\n"
		if err := os.WriteFile(beadsPath, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		loaded, err := loader.LoadIssuesFromFileWithOptionsPooled(beadsPath, loader.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		loader.AttachIssueOrigins(loaded.Issues, beadsPath, false)
		m := NewModel(loaded.Issues, nil, beadsPath, ReadinessScope{
			AuthorityIncomplete: true,
			SourceDegraded:      true,
		})
		m.pooledIssues = loaded.PoolRefs
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})

		if !m.authorityIncomplete {
			t.Fatalf("expected initial m.authorityIncomplete=true on degraded fallback")
		}
		// 1) Sync FileChangedMsg reload on syntax-clean fallback JSONL must NOT clear authorityIncomplete or read-only Origin.
		m.Update(FileChangedMsg{})
		if !m.authorityIncomplete {
			t.Fatalf("expected m.authorityIncomplete=true after sync FileChangedMsg on degraded fallback")
		}
		if len(m.issues) == 0 || m.issues[0].Origin == nil || m.issues[0].Origin.ReadOnlyReason == "" {
			t.Fatalf("expected Origin.ReadOnlyReason retained after sync reload, got %+v", m.issues[0].Origin)
		}

		// 2) BackgroundWorker snapshot build with SourceDegraded=true must ALSO set AuthorityComplete=false and retain read-only Origin.
		bw, err := NewBackgroundWorker(WorkerConfig{
			BeadsPath:      beadsPath,
			SourceDegraded: true,
			DebounceDelay:  50 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer bw.Stop()
		snap := bw.buildSnapshot(true)
		if snap == nil {
			t.Fatalf("buildSnapshot returned nil")
		}
		if snap.AuthorityComplete {
			t.Fatalf("expected BackgroundWorker snapshot AuthorityComplete=false when SourceDegraded=true")
		}
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})
		if !m.authorityIncomplete {
			t.Fatalf("expected m.authorityIncomplete=true after SnapshotReadyMsg on degraded fallback")
		}
		if len(m.issues) == 0 || m.issues[0].Origin == nil || m.issues[0].Origin.ReadOnlyReason == "" {
			t.Fatalf("expected Origin.ReadOnlyReason retained after SnapshotReadyMsg, got %+v", m.issues[0].Origin)
		}
	})

	t.Run("R3_B1b_SnapshotErrorSetsSourceStaleReasonUntilSnapshotReady", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "BG-1", Title: "Background Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		}
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})

		m.Update(SnapshotErrorMsg{Err: fmt.Errorf("corrupt jsonl"), Recoverable: true})
		if m.sourceStaleReason == "" || !m.statusIsError {
			t.Fatalf("expected SnapshotErrorMsg to set sourceStaleReason and statusIsError, got stale=%q err=%v", m.sourceStaleReason, m.statusIsError)
		}

		snap := NewSnapshotBuilder(issues).Build()
		snap.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})
		if m.sourceStaleReason != "" || m.statusIsError {
			t.Fatalf("expected SnapshotReadyMsg to clear sourceStaleReason and statusIsError, got stale=%q err=%v", m.sourceStaleReason, m.statusIsError)
		}
	})
}

func TestWave12TrackBRegressions(t *testing.T) {
	keyRune := func(s string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}

	// Deliverable 0: Exported TriggerSourceReload() helper for aggregated/file/nil sources.
	t.Run("Deliverable0_TriggerSourceReload", func(t *testing.T) {
		initial := []model.Issue{
			{ID: "SRC-1", Title: "Initial", Status: model.StatusOpen, Priority: 1},
		}
		m := NewModel(copyIssues(initial), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// Negative control: pure in-memory model with no beadsPath/watcher/sourceReload returns nil.
		if cmd := m.TriggerSourceReload(); cmd != nil {
			t.Fatal("expected TriggerSourceReload() to return nil when no reload route is configured")
		}

		// Aggregated source reload callback route:
		reloaded := []model.Issue{
			{ID: "SRC-1", Title: "Initial", Status: model.StatusOpen, Priority: 1},
			{ID: "SRC-2", Title: "Reloaded Second", Status: model.StatusOpen, Priority: 2},
		}
		called := false
		m.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			called = true
			return SourceReloadResult{
				Issues:    copyIssues(reloaded),
				Authority: model.NewReadinessIndex(reloaded),
			}, nil
		})
		cmd := m.TriggerSourceReload()
		if cmd == nil {
			t.Fatal("expected non-nil tea.Cmd from TriggerSourceReload() when SetSourceReload is configured")
		}
		msg := cmd()
		if !called {
			t.Fatal("expected TriggerSourceReload() cmd to invoke configured sourceReload callback")
		}
		m.Update(msg)
		if len(m.issues) != 2 {
			t.Fatalf("expected 2 issues after processing TriggerSourceReload() result, got %d", len(m.issues))
		}

		// File reload route via beadsPath:
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsFile := filepath.Join(beadsDir, "issues.jsonl")
		b1, _ := json.Marshal(initial[0])
		if err := os.WriteFile(beadsFile, append(b1, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		mf := NewModel(copyIssues(initial), nil, beadsFile)
		t.Cleanup(mf.Stop)
		fileCmd := mf.TriggerSourceReload()
		if fileCmd == nil {
			t.Fatal("expected non-nil tea.Cmd from TriggerSourceReload() when beadsPath is set")
		}
		if _, ok := fileCmd().(FileChangedMsg); !ok {
			t.Fatalf("expected FileChangedMsg from file-backed TriggerSourceReload()")
		}
	})

	// Gap 1: Isolate secondary full-screen views from handleListKeys while preserving cross-view shortcuts.
	t.Run("Gap1_SecondaryViewIsolationFromHandleListKeys", func(t *testing.T) {
		now := time.Now().UTC()
		issues := []model.Issue{
			{ID: "ISO-1", Title: "Alpha issue", Status: model.StatusOpen, Priority: 1, Labels: []string{"core"}, CreatedAt: now, UpdatedAt: now},
			{
				ID: "ISO-2", Title: "Beta issue", Status: model.StatusOpen, Priority: 2, Labels: []string{"api"}, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: "ISO-2", DependsOnID: "ISO-1", Type: model.DepBlocks}},
			},
			{ID: "ISO-3", Title: "Gamma issue", Status: model.StatusOpen, Priority: 3, Labels: []string{"ui"}, CreatedAt: now, UpdatedAt: now},
		}
		sprints := []model.Sprint{
			{ID: "SP-1", Name: "Sprint 1", BeadIDs: []string{"ISO-1", "ISO-2"}, StartDate: now.Add(-24 * time.Hour), EndDate: now.Add(7 * 24 * time.Hour)},
		}

		histReport := &correlation.HistoryReport{
			GeneratedAt: now,
			Histories: map[string]correlation.BeadHistory{
				"ISO-1": {
					BeadID: "ISO-1", Title: "Alpha issue", Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "aaaa1111", ShortSHA: "aaaa111", Message: "init", Timestamp: now, Confidence: 0.9},
					},
				},
			},
		}

		secondaryViews := []struct {
			name         string
			enter        func(m *Model)
			expectLocalT bool
		}{
			{"focusTree", func(m *Model) { m.Update(keyRune("E")) }, false},
			{"focusInsights", func(m *Model) { m.Update(keyRune("i")) }, false},
			{"focusActionable", func(m *Model) { m.Update(keyRune("a")) }, false},
			{"focusHistory", func(m *Model) { m.Update(keyRune("h")); makeHistoryReportCurrent(m, histReport) }, true},
			{"focusSprint", func(m *Model) { m.sprints = sprints; m.Update(keyRune("P")) }, false},
			{"focusFlowMatrix", func(m *Model) { m.Update(keyRune("f")) }, false},
			{"focusLabelDashboard", func(m *Model) { m.Update(keyRune("[")) }, false},
			{"focusVelocityComparison", func(m *Model) { m.Update(keyRune("[")); m.Update(keyRune("v")) }, false},
			{"focusAttention", func(m *Model) { m.Update(keyRune("]")) }, false},
			{"focusGraph", func(m *Model) { m.Update(keyRune("g")) }, false},
		}

		for _, tc := range secondaryViews {
			t.Run(tc.name, func(t *testing.T) {
				m := NewModel(copyIssues(issues), nil, "")
				t.Cleanup(m.Stop)
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
				m.updateAvailable = true
				m.updateTag = "v9.9.9"
				m.updateURL = "https://example.invalid/release"

				rec := &recipe.Recipe{
					Name:    "open-all",
					Filters: recipe.FilterConfig{Status: []string{"open"}},
				}
				m.setActiveRecipe(rec)
				m.applyRecipe(rec)
				m.list.Select(1)
				origFilter := m.currentFilter
				origSort := m.sortMode
				origIndex := m.list.Index()

				tc.enter(m)
				if m.focused == focusList {
					t.Fatalf("failed to enter secondary view %s (still focusList)", tc.name)
				}

				for _, k := range []tea.KeyMsg{
					keyRune("o"),
					keyRune("c"),
					keyRune("r"),
					keyRune("s"),
					keyRune("S"),
					keyRune("G"),
					{Type: tea.KeyHome},
					{Type: tea.KeyEnd},
					{Type: tea.KeyCtrlD},
					{Type: tea.KeyCtrlU},
				} {
					m.Update(k)
				}

				if m.activeRecipe == nil || m.activeRecipe.Name != "open-all" {
					t.Fatalf("%s: list keys wiped activeRecipe (got %v)", tc.name, m.activeRecipe)
				}
				if m.currentFilter != origFilter {
					t.Fatalf("%s: list keys mutated currentFilter from %q to %q", tc.name, origFilter, m.currentFilter)
				}
				if m.sortMode != origSort {
					t.Fatalf("%s: list keys mutated sortMode from %v to %v", tc.name, origSort, m.sortMode)
				}
				if m.list.Index() != origIndex {
					t.Fatalf("%s: list keys moved hidden list cursor from %d to %d", tc.name, origIndex, m.list.Index())
				}

				// Cross-view shortcuts ('t' where not view-local, plus 'U' everywhere) must remain reachable.
				if !tc.expectLocalT {
					m.Update(keyRune("t"))
					if !m.showTimeTravelPrompt {
						t.Fatalf("%s: expected cross-view 't' to open showTimeTravelPrompt", tc.name)
					}
					m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				}
				m.Update(keyRune("U"))
				if !m.showUpdateModal {
					t.Fatalf("%s: expected cross-view 'U' to open showUpdateModal", tc.name)
				}
			})
		}

		// Positive control: in focusList, 'o', 's', and 'G' DO mutate filter, sort, and list index.
		ml := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(ml.Stop)
		ml.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		ml.Update(keyRune("o"))
		if ml.currentFilter != "open" {
			t.Fatalf("focusList positive control: expected 'o' to set currentFilter='open', got %q", ml.currentFilter)
		}
		ml.Update(keyRune("s"))
		if ml.sortMode == SortDefault {
			t.Fatalf("focusList positive control: expected 's' to cycle sortMode from SortDefault")
		}
		ml.Update(keyRune("G"))
		if ml.list.Index() != len(issues)-1 {
			t.Fatalf("focusList positive control: expected 'G' to jump to last item %d, got %d", len(issues)-1, ml.list.Index())
		}
	})

	// Gap 2: Insights calculation proof filters non-blocking edges, ToggleCalculation refreshes immediately,
	// and AdvancedInsights populate lazily only when focusInsights is active and Phase 2 is ready.
	t.Run("Gap2_InsightsBlockingEdgeCalculationAndAdvancedInsights", func(t *testing.T) {
		now := time.Now().UTC()
		issues := []model.Issue{
			{
				ID: "ROOT-1", Title: "Root blocker", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{
					{IssueID: "ROOT-1", DependsOnID: "REL-1", Type: model.DepRelated},
					{IssueID: "ROOT-1", DependsOnID: "DISC-1", Type: model.DepDiscoveredFrom},
				},
			},
			{
				ID: "BOTTLE-1", Title: "Central bottleneck", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{
					{IssueID: "BOTTLE-1", DependsOnID: "ROOT-1", Type: model.DepBlocks},
				},
			},
			{
				ID: "LEAF-1", Title: "Leaf consumer A", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{
					{IssueID: "LEAF-1", DependsOnID: "BOTTLE-1", Type: model.DepBlocks},
				},
			},
			{
				ID: "LEAF-2", Title: "Leaf consumer B", Status: model.StatusOpen, Priority: 2, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{
					{IssueID: "LEAF-2", DependsOnID: "BOTTLE-1", Type: model.DepBlocks},
				},
			},
			{ID: "REL-1", Title: "Related non-blocking", Status: model.StatusOpen, Priority: 3, CreatedAt: now, UpdatedAt: now},
			{ID: "DISC-1", Title: "Discovered non-blocking", Status: model.StatusOpen, Priority: 3, CreatedAt: now, UpdatedAt: now},
			{
				ID: "CHILD-1", Title: "Parent-child non-blocking", Status: model.StatusOpen, Priority: 3, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{
					{IssueID: "CHILD-1", DependsOnID: "ROOT-1", Type: model.DepParentChild},
				},
			},
			{
				ID: "CYC-1", Title: "Cycle node 1", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: "CYC-1", DependsOnID: "CYC-2", Type: model.DepBlocks}},
			},
			{
				ID: "CYC-2", Title: "Cycle node 2", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: "CYC-2", DependsOnID: "CYC-1", Type: model.DepBlocks}},
			},
		}

		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
		m.analysis.WaitForPhase2()

		// Negative control (benchmark safety): SnapshotReadyMsg while focusList is active leaves advancedInsights nil.
		snap := NewSnapshotBuilder(copyIssues(issues)).Build()
		snap.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})
		if m.insightsPanel.advancedInsights != nil {
			t.Fatal("expected insightsPanel.advancedInsights to remain nil while focused == focusList")
		}

		// Enter Insights view ('i') -> rebuildInsightsPanel computes AdvancedInsights lazily.
		m.Update(keyRune("i"))
		if m.focused != focusInsights {
			t.Fatalf("expected focusInsights, got %v", m.focused)
		}
		if m.insightsPanel.advancedInsights == nil {
			t.Fatal("expected insightsPanel.advancedInsights to be populated upon entering focusInsights with Phase 2 ready")
		}

		// Verify findDependents / findDependencies exclude non-blocking edges (DepRelated, DepDiscoveredFrom, DepParentChild).
		depsIn := m.insightsPanel.findDependents("ROOT-1")
		if len(depsIn) != 1 || depsIn[0] != "BOTTLE-1" {
			t.Fatalf("expected findDependents(ROOT-1) == [BOTTLE-1] (excluding CHILD-1), got %v", depsIn)
		}
		depsOut := m.insightsPanel.findDependencies("ROOT-1")
		if len(depsOut) != 0 {
			t.Fatalf("expected findDependencies(ROOT-1) == [] (excluding REL-1 and DISC-1), got %v", depsOut)
		}

		// Verify ToggleCalculation ('x') refreshes detailContent immediately without requiring panel navigation.
		m.insightsPanel.focusedPanel = PanelBottlenecks
		m.insightsPanel.selectedIndex[PanelBottlenecks] = 0
		m.insightsPanel.updateDetailContent()
		plainDetail := ansi.Strip(m.insightsPanel.detailContent)
		if !strings.Contains(plainDetail, "Calculation Proof") {
			t.Fatalf("expected Calculation Proof in initial detailContent, got:\n%s", plainDetail)
		}
		m.Update(keyRune("x"))
		if strings.Contains(ansi.Strip(m.insightsPanel.detailContent), "Calculation Proof") {
			t.Fatalf("expected 'x' ToggleCalculation to immediately remove Calculation Proof, got:\n%s", ansi.Strip(m.insightsPanel.detailContent))
		}
		m.Update(keyRune("x"))
		plainDetail = ansi.Strip(m.insightsPanel.detailContent)
		if !strings.Contains(plainDetail, "Calculation Proof") {
			t.Fatalf("expected second 'x' ToggleCalculation to immediately restore Calculation Proof")
		}

		// Verify AdvancedInsights sections render in PanelBottlenecks, PanelCycles, PanelPriority, and PanelArticulation.
		if !strings.Contains(plainDetail, "Parallel Cut Suggestions") &&
			m.insightsPanel.advancedInsights.ParallelCut != nil &&
			len(m.insightsPanel.advancedInsights.ParallelCut.Suggestions) > 0 {
			t.Fatalf("expected Parallel Cut Suggestions in PanelBottlenecks detailContent:\n%s", plainDetail)
		}

		m.insightsPanel.focusedPanel = PanelCycles
		m.insightsPanel.selectedIndex[PanelCycles] = 0
		m.insightsPanel.updateDetailContent()
		if !strings.Contains(ansi.Strip(m.insightsPanel.detailContent), "Cycle Break Suggestions") {
			t.Fatalf("expected Cycle Break Suggestions in PanelCycles detailContent:\n%s", ansi.Strip(m.insightsPanel.detailContent))
		}

		m.insightsPanel.focusedPanel = PanelPriority
		m.insightsPanel.selectedIndex[PanelPriority] = 0
		m.insightsPanel.updateDetailContent()
		plainPrio := ansi.Strip(m.insightsPanel.detailContent)
		if !strings.Contains(plainPrio, "Greedy Unlock Set") ||
			!strings.Contains(plainPrio, "Parallel Gain") {
			t.Fatalf("expected Greedy Unlock Set and Parallel Gain in PanelPriority detailContent:\n%s", plainPrio)
		}

		m.insightsPanel.insights.Articulation = []string{"BOTTLE-1"}
		m.insightsPanel.focusedPanel = PanelArticulation
		m.insightsPanel.selectedIndex[PanelArticulation] = 0
		m.insightsPanel.updateDetailContent()
		if !strings.Contains(ansi.Strip(m.insightsPanel.detailContent), "Vertex Coverage Set") {
			t.Fatalf("expected Vertex Coverage Set in PanelArticulation detailContent:\n%s", ansi.Strip(m.insightsPanel.detailContent))
		}
	})

	// Gap 3: Navigation parity (home/G/end, ctrl+d/ctrl+u/pgdown/pgup, LabelDashboard mouse wheel, Help & KeyDocs).
	t.Run("Gap3_NavigationParityMouseWheelHelpOverlayAndKeyDocs", func(t *testing.T) {
		var issues []model.Issue
		now := time.Now().UTC()
		for i := 0; i < 20; i++ {
			lbl := fmt.Sprintf("lbl-%02d", i)
			iss := model.Issue{
				ID:        fmt.Sprintf("NAV-%02d", i),
				Title:     fmt.Sprintf("Navigation issue %02d", i),
				Status:    model.StatusOpen,
				Priority:  1 + (i % 3),
				Labels:    []string{lbl},
				CreatedAt: now,
				UpdatedAt: now,
			}
			if i > 0 {
				iss.Dependencies = []*model.Dependency{
					{IssueID: iss.ID, DependsOnID: fmt.Sprintf("NAV-%02d", i-1), Type: model.DepParentChild},
				}
			}
			issues = append(issues, iss)
		}

		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 14})

		// 1) focusGraph: G / home / end
		m.Update(keyRune("g"))
		m.Update(keyRune("G"))
		if m.graphView.selectedIdx != len(issues)-1 {
			t.Fatalf("focusGraph 'G': expected selectedIdx %d, got %d", len(issues)-1, m.graphView.selectedIdx)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyHome})
		if m.graphView.selectedIdx != 0 {
			t.Fatalf("focusGraph 'home': expected selectedIdx 0, got %d", m.graphView.selectedIdx)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnd})
		if m.graphView.selectedIdx != len(issues)-1 {
			t.Fatalf("focusGraph 'end': expected selectedIdx %d, got %d", len(issues)-1, m.graphView.selectedIdx)
		}

		// 2) focusTree: G / home / end
		m.Update(keyRune("E"))
		m.Update(keyRune("X")) // expand all
		lastTreeIdx := len(m.tree.flatList) - 1
		m.Update(tea.KeyMsg{Type: tea.KeyEnd})
		if m.tree.cursor != lastTreeIdx {
			t.Fatalf("focusTree 'end': expected cursor %d, got %d", lastTreeIdx, m.tree.cursor)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyHome})
		if m.tree.cursor != 0 {
			t.Fatalf("focusTree 'home': expected cursor 0, got %d", m.tree.cursor)
		}

		// 3) focusHistory: bead mode, git mode, and file tree focus
		report := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"NAV-00": {
					BeadID: "NAV-00", Title: "Nav 0", Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "1111111111", ShortSHA: "1111111", Message: "c1", Timestamp: now, Confidence: 0.9, Files: []correlation.FileChange{{Path: "pkg/a.go"}}},
					},
				},
				"NAV-01": {
					BeadID: "NAV-01", Title: "Nav 1", Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "2222222222", ShortSHA: "2222222", Message: "c2", Timestamp: now.Add(-time.Hour), Confidence: 0.9, Files: []correlation.FileChange{{Path: "pkg/b.go"}}},
					},
				},
				"NAV-02": {
					BeadID: "NAV-02", Title: "Nav 2", Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "3333333333", ShortSHA: "3333333", Message: "c3", Timestamp: now.Add(-2 * time.Hour), Confidence: 0.9, Files: []correlation.FileChange{{Path: "pkg/c.go"}}},
					},
				},
			},
			CommitIndex: correlation.CommitIndex{
				"1111111111": {"NAV-00"},
				"2222222222": {"NAV-01"},
				"3333333333": {"NAV-02"},
			},
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // exit focusTree back to focusList before pressing 'h'
		m.Update(keyRune("h"))
		makeHistoryReportCurrent(m, report)
		m.historyView.SetSize(140, 13)

		m.Update(keyRune("G"))
		if m.historyView.selectedBead != 2 {
			t.Fatalf("focusHistory bead mode 'G': expected selectedBead 2, got %d", m.historyView.selectedBead)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyHome})
		if m.historyView.selectedBead != 0 {
			t.Fatalf("focusHistory bead mode 'home': expected selectedBead 0, got %d", m.historyView.selectedBead)
		}

		m.Update(keyRune("v")) // Git mode
		m.Update(tea.KeyMsg{Type: tea.KeyEnd})
		if m.historyView.selectedGitCommit != 2 {
			t.Fatalf("focusHistory git mode 'end': expected selectedGitCommit 2, got %d", m.historyView.selectedGitCommit)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyHome})
		if m.historyView.selectedGitCommit != 0 {
			t.Fatalf("focusHistory git mode 'home': expected selectedGitCommit 0, got %d", m.historyView.selectedGitCommit)
		}

		m.historyView.ToggleFileTree()
		m.historyView.SetFileTreeFocus(true)
		lastFileIdx := len(m.historyView.flatFileList) - 1
		m.Update(keyRune("G"))
		if m.historyView.selectedFileIdx != lastFileIdx {
			t.Fatalf("focusHistory file tree 'G': expected selectedFileIdx %d, got %d", lastFileIdx, m.historyView.selectedFileIdx)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyHome})
		if m.historyView.selectedFileIdx != 0 {
			t.Fatalf("focusHistory file tree 'home': expected selectedFileIdx 0, got %d", m.historyView.selectedFileIdx)
		}
		m.isHistoryView = false
		m.focused = focusList

		// 4) focusAttention: ctrl+d / ctrl+u / pgdown / pgup
		m.Update(keyRune("]"))
		if m.focused != focusAttention {
			t.Fatalf("expected focusAttention, got %v", m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		afterAttnDown := m.attentionView.Cursor()
		if afterAttnDown <= 1 {
			t.Fatalf("focusAttention ctrl+d: expected half-page step > 1, got %d", afterAttnDown)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		afterPgDown := m.attentionView.Cursor()
		if afterPgDown <= afterAttnDown {
			t.Fatalf("focusAttention pgdown: expected cursor > %d, got %d", afterAttnDown, afterPgDown)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		expectedAfterCtrlU := max(0, afterPgDown-afterAttnDown)
		if m.attentionView.Cursor() != expectedAfterCtrlU {
			t.Fatalf("focusAttention ctrl+u: expected cursor %d, got %d", expectedAfterCtrlU, m.attentionView.Cursor())
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		if m.attentionView.Cursor() != 0 {
			t.Fatalf("focusAttention pgup: expected cursor 0, got %d", m.attentionView.Cursor())
		}

		// 5) focusLabelDashboard: ctrl+d / ctrl+u / pgdown / pgup + mouse wheel up/down
		m.Update(keyRune("["))
		if m.focused != focusLabelDashboard {
			t.Fatalf("expected focusLabelDashboard, got %v", m.focused)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		afterLblDown := m.labelDashboard.cursor
		if afterLblDown <= 1 {
			t.Fatalf("focusLabelDashboard ctrl+d: expected half-page step > 1, got %d", afterLblDown)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		if m.labelDashboard.cursor != 0 {
			t.Fatalf("focusLabelDashboard ctrl+u: expected cursor 0, got %d", m.labelDashboard.cursor)
		}
		m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		if m.labelDashboard.cursor != 1 {
			t.Fatalf("focusLabelDashboard wheel down: expected cursor 1, got %d", m.labelDashboard.cursor)
		}
		m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		if m.labelDashboard.cursor != 0 {
			t.Fatalf("focusLabelDashboard wheel up: expected cursor 0, got %d", m.labelDashboard.cursor)
		}

		// 6) renderHelpOverlay() + GetKeyBindingDocs() parity
		rawHelpLines, _ := m.buildHelpBodyLines()
		helpLines := strings.Join(rawHelpLines, "\n")
		for _, expectedToken := range []string{"E", "P", `\`, "~", "`", "y", "V", "U", "Self-update"} {
			if !strings.Contains(helpLines, expectedToken) {
				t.Fatalf("expected help overlay body to document %q, got:\n%s", expectedToken, helpLines)
			}
		}
		docs := GetKeyBindingDocs()
		foundDocs := make(map[string]bool)
		foundInsightsCalc := false
		for _, d := range docs {
			foundDocs[d.Key] = true
			if d.Category == "Insights" && d.Key == "x" && strings.Contains(strings.ToLower(d.Desc), "calculation") {
				foundInsightsCalc = true
			}
		}
		for _, key := range []string{"E", "P", `\`, "`", "~", "U", "V", "y"} {
			if !foundDocs[key] {
				t.Fatalf("expected GetKeyBindingDocs() to include %q", key)
			}
		}
		if !foundInsightsCalc {
			t.Fatal("expected GetKeyBindingDocs() Insights 'x' to document Toggle calculation proof")
		}
	})

	// Gap 4: focusedIssueForSessions alignment, FlowMatrix detail V/C/y actions, and resetUnderlyingViewToMainList viewport width.
	t.Run("Gap4_FocusedIssueForSessionsFlowDetailActionsAndResetUnderlyingView", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "OTHER-1", Title: "Hidden list selection", Status: model.StatusOpen, Priority: 1, Labels: []string{"misc"}},
			{ID: "BLK-1", Title: "Backend blocker", Status: model.StatusOpen, Priority: 1, Labels: []string{"backend"}},
			{
				ID: "DEP-1", Title: "Frontend consumer", Status: model.StatusOpen, Priority: 2, Labels: []string{"frontend"},
				Dependencies: []*model.Dependency{{IssueID: "DEP-1", DependsOnID: "BLK-1", Type: model.DepBlocks}},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 36}) // Split view active
		if !m.isSplitView {
			t.Fatal("expected split view at width 160")
		}
		splitViewportWidth := m.viewport.Width
		m.selectListIssueByID("OTHER-1")

		// Non-issue views and showLabelDrilldown must return nil from focusedIssueForSessions().
		m.showLabelDrilldown = true
		m.labelDrilldownLabel = "misc"
		if got := m.focusedIssueForSessions(); got != nil {
			t.Fatalf("expected nil focusedIssueForSessions when showLabelDrilldown=true, got %v", got.ID)
		}
		m.showLabelDrilldown = false
		m.labelDrilldownLabel = ""

		for _, nonIssueFocus := range []focus{focusSprint, focusLabelDashboard, focusAttention, focusVelocityComparison} {
			m.focused = nonIssueFocus
			if got := m.focusedIssueForSessions(); got != nil {
				t.Fatalf("expected nil focusedIssueForSessions for %v, got %s", nonIssueFocus, got.ID)
			}
		}

		// FlowMatrix detail view: focusedIssueForSessions() + y/C/V must target flowDetailID ("DEP-1"), NOT "OTHER-1".
		m.focused = focusList
		m.Update(keyRune("f"))
		m.flowDetailID = "DEP-1"
		m.viewport.Width = m.width - 4
		if got := m.focusedIssueForSessions(); got == nil || got.ID != "DEP-1" {
			t.Fatalf("expected focusedIssueForSessions() == DEP-1 in FlowMatrix detail, got %v", got)
		}

		_, yCmd := m.Update(keyRune("y"))
		if yCmd == nil {
			t.Fatal("expected 'y' in FlowMatrix detail to return clipboard copy cmd")
		}
		_, cCmd := m.Update(keyRune("C"))
		if cCmd == nil {
			t.Fatal("expected 'C' in FlowMatrix detail to return context copy cmd")
		}
		m.Update(keyRune("V"))
		if m.cassRequest != nil && m.cassRequest.beadID != "DEP-1" {
			t.Fatalf("expected 'V' in FlowMatrix detail to look up DEP-1, got %q", m.cassRequest.beadID)
		}
		if m.showCassModal && m.cassModal.beadID != "DEP-1" {
			t.Fatalf("expected 'V' modal in FlowMatrix detail to target DEP-1, got %q", m.cassModal.beadID)
		}
		m.cancelCassLookup()
		m.showCassModal = false

		// resetUnderlyingViewToMainList() must clear flowDetailID and restore split-view m.viewport.Width.
		m.resetUnderlyingViewToMainList()
		if m.flowDetailID != "" {
			t.Fatalf("expected resetUnderlyingViewToMainList to clear flowDetailID, got %q", m.flowDetailID)
		}
		if m.viewport.Width != splitViewportWidth {
			t.Fatalf("expected resetUnderlyingViewToMainList to restore split viewport width %d, got %d", splitViewportWidth, m.viewport.Width)
		}
	})

	// Gap 5: Alerts scope (activeRepos + candidateIDs), dynamic dismiss header counts, and candidate-scoped footer counts.
	t.Run("Gap5_AlertsScopeDynamicCountsAndCandidateScopedFooterCounts", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "api:IN-1", Title: "API open ready", Status: model.StatusOpen, Priority: 1},
			{
				ID: "api:IN-2", Title: "API blocked by external", Status: model.StatusBlocked, Priority: 1,
				Dependencies: []*model.Dependency{{IssueID: "api:IN-2", DependsOnID: "web:EXT-BLK", Type: model.DepBlocks}},
			},
			{ID: "api:IN-CLOSED", Title: "API closed", Status: model.StatusClosed, Priority: 2},
			{ID: "api:OUT-1", Title: "API open outside candidateIDs", Status: model.StatusOpen, Priority: 2},
			{ID: "api:OUT-BLK", Title: "API blocked outside candidateIDs", Status: model.StatusBlocked, Priority: 2},
			{ID: "web:OUT-CLOSED", Title: "Web closed outside candidateIDs", Status: model.StatusClosed, Priority: 3},
			{ID: "web:EXT-BLK", Title: "Web open blocker outside candidateIDs", Status: model.StatusOpen, Priority: 1},
		}
		candidates := map[string]bool{
			"api:IN-1":      true,
			"api:IN-2":      true,
			"api:IN-CLOSED": true,
		}
		scope := ReadinessScope{
			Authority:    model.NewReadinessIndex(issues),
			CandidateIDs: candidates,
		}
		m := NewModel(copyIssues(issues), nil, "", scope)
		t.Cleanup(m.Stop)
		m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
		m.activeRepos = map[string]bool{"api": true}
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 32})

		// Candidate-scoped countOpen (2: IN-1 + IN-2) / countBlocked (1: IN-2) / countClosed (1: IN-CLOSED) in NewModel:
		if m.countOpen != 2 || m.countBlocked != 1 || m.countClosed != 1 {
			t.Fatalf("NewModel candidate counts: want open=2 blocked=1 closed=1, got open=%d blocked=%d closed=%d",
				m.countOpen, m.countBlocked, m.countClosed)
		}

		// Candidate-scoped counts after SnapshotReadyMsg:
		snap := NewSnapshotBuilder(copyIssues(issues), scope.Authority).Build()
		snap.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})
		if m.countOpen != 2 || m.countBlocked != 1 || m.countClosed != 1 {
			t.Fatalf("SnapshotReadyMsg candidate counts: want open=2 blocked=1 closed=1, got open=%d blocked=%d closed=%d",
				m.countOpen, m.countBlocked, m.countClosed)
		}

		// Candidate-scoped counts after FileChangedMsg (via SourceReloadResult):
		m.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			return SourceReloadResult{
				Issues:       copyIssues(issues),
				Authority:    model.NewReadinessIndex(issues),
				CandidateIDs: candidates,
			}, nil
		})
		reloadCmd := m.TriggerSourceReload()
		m.Update(reloadCmd())
		if m.countOpen != 2 || m.countBlocked != 1 || m.countClosed != 1 {
			t.Fatalf("FileChangedMsg candidate counts: want open=2 blocked=1 closed=1, got open=%d blocked=%d closed=%d",
				m.countOpen, m.countBlocked, m.countClosed)
		}

		// Alerts scoping across activeRepos + candidateIDs + dynamic dismiss header counts:
		m.alerts = []drift.Alert{
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "api:IN-1", Message: "Crit in scope"},
			{Type: drift.AlertBlockingCascade, Severity: drift.SeverityWarning, IssueID: "api:IN-2", Message: "Warn in scope"},
			{Type: drift.AlertDensityGrowth, Severity: drift.SeverityInfo, IssueID: "", Message: "Global info"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "api:OUT-1", Message: "Excluded by candidateIDs"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "web:EXT-BLK", Message: "Excluded by activeRepos"},
		}
		m.alertsCritical = 3
		m.alertsWarning = 1
		m.alertsInfo = 1

		scoped := m.activeScopedAlerts()
		if len(scoped) != 3 {
			t.Fatalf("expected 3 activeScopedAlerts (excluding api:OUT-1 and web:EXT-BLK), got %d: %+v", len(scoped), scoped)
		}

		m.Update(keyRune("!"))
		if !m.showAlertsPanel {
			t.Fatal("expected '!' to open showAlertsPanel when scoped alerts exist")
		}
		panelBefore := ansi.Strip(m.renderAlertsPanel())
		if !strings.Contains(panelBefore, "3 total") || !strings.Contains(panelBefore, "1 critical") || !strings.Contains(panelBefore, "1 warning") || !strings.Contains(panelBefore, "1 info") {
			t.Fatalf("expected scoped dynamic counts (3 total: 1 critical, 1 warning, 1 info) before dismiss, got:\n%s", panelBefore)
		}

		// Press 'd' to dismiss the critical alert ("api:IN-1") -> header counts MUST decrement immediately!
		m.Update(keyRune("d"))
		panelAfter := ansi.Strip(m.renderAlertsPanel())
		if !strings.Contains(panelAfter, "2 total") || strings.Contains(panelAfter, "critical") || !strings.Contains(panelAfter, "1 warning") || !strings.Contains(panelAfter, "1 info") {
			t.Fatalf("expected critical badge removed and count decremented to 2 total after 'd' dismiss, got:\n%s", panelAfter)
		}

		// Dismiss global alert too, then switch activeRepos to {"other": true} so remaining scoped alerts == 0 -> panel auto-closes!
		m.alerts = []drift.Alert{
			{Type: drift.AlertBlockingCascade, Severity: drift.SeverityWarning, IssueID: "api:IN-2", Message: "Warn in api"},
		}
		m.alertsCursor = 0
		m.activeRepos = map[string]bool{"web": true}
		m.syncSecondaryViewsForRepoScope(m.focused)
		if m.showAlertsPanel {
			t.Fatal("expected syncSecondaryViewsForRepoScope to close showAlertsPanel when 0 scoped alerts remain")
		}
		m.Update(keyRune("!"))
		if m.showAlertsPanel || !strings.Contains(m.statusMsg, "No active alerts") {
			t.Fatalf("expected '!' with 0 scoped alerts to stay closed with 'No active alerts', got open=%v status=%q", m.showAlertsPanel, m.statusMsg)
		}
	})

	// Gap 6: AgentPromptDecline persists decline for current BlurbVersion via agents.RecordDecline(..., false).
	t.Run("Gap6_AgentPromptDeclinePersistsForCurrentBlurbVersion", func(t *testing.T) {
		cfgHome := t.TempDir()
		t.Setenv("HOME", cfgHome)
		t.Setenv("XDG_CONFIG_HOME", cfgHome)
		t.Setenv("BV_NO_SAVED_CONFIG", "")

		workDir := t.TempDir()
		agentsPath := filepath.Join(workDir, "AGENTS.md")
		if err := os.WriteFile(agentsPath, []byte("# Project Rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !agents.ShouldPromptForAgentFile(workDir) {
			t.Fatal("precondition: expected ShouldPromptForAgentFile(workDir) == true before decline")
		}

		m := NewModel([]model.Issue{{ID: "AG-1", Title: "Agent test", Status: model.StatusOpen, Priority: 1}}, nil, "")
		t.Cleanup(m.Stop)
		m.workDir = workDir
		m.agentPromptModal = NewAgentPromptModal(agentsPath, "AGENTS.md", m.theme)
		m.showAgentPrompt = true

		m.Update(keyRune("n"))
		if m.showAgentPrompt {
			t.Fatal("expected 'n' to close showAgentPrompt")
		}
		if agents.ShouldPromptForAgentFile(workDir) {
			t.Fatal("expected ShouldPromptForAgentFile(workDir) == false after declining current BlurbVersion")
		}
		pref, err := agents.LoadAgentPromptPreference(workDir)
		if err != nil || pref == nil || pref.DontAskAgain {
			t.Fatalf("expected RecordDecline(..., false) with DontAskAgain=false, got pref=%+v err=%v", pref, err)
		}
		if !agents.ShouldPromptForAgentUpgrade(workDir, agents.BlurbVersion+1) {
			t.Fatal("expected ShouldPromptForAgentUpgrade(workDir, BlurbVersion+1) == true after temporary decline")
		}
	})

	// Gap 7: Narrow-pane CJK/emoji TreeModel clamping, BuildFromSnapshot deep-copy isolation, and deterministic ID tie-breakers.
	t.Run("Gap7_TreeNarrowViewportClampingDeepCopyAndDeterministicSortTieBreakers", func(t *testing.T) {
		sameTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		cjkIssues := []model.Issue{
			{ID: "ROOT-CJK", Title: "🚀🔥日本語タイトル非常に長いテストケース幅制限確認", Status: model.StatusOpen, Priority: 1, CreatedAt: sameTime},
			{
				ID: "CHILD-CJK-1", Title: "🎉サブタスク長い説明文字列オーバーフロー防止テスト", Status: model.StatusInProgress, Priority: 1, CreatedAt: sameTime,
				Dependencies: []*model.Dependency{{IssueID: "CHILD-CJK-1", DependsOnID: "ROOT-CJK", Type: model.DepParentChild}},
			},
			{
				ID: "CHILD-CJK-2", Title: "🔧もう一つの長い日本語タイトル行折り返し防止", Status: model.StatusBlocked, Priority: 2, CreatedAt: sameTime,
				Dependencies: []*model.Dependency{{IssueID: "CHILD-CJK-2", DependsOnID: "ROOT-CJK", Type: model.DepParentChild}},
			},
		}

		theme := DefaultTheme(lipgloss.NewRenderer(nil))
		tm := NewTreeModel(theme)
		tm.Build(copyIssues(cjkIssues))
		tm.ExpandAll()
		tm.SetSize(26, 6)
		rendered := tm.View()
		lines := strings.Split(rendered, "\n")
		if len(lines) > 6 {
			t.Fatalf("expected narrow TreeModel height <= 6 lines, got %d:\n%s", len(lines), rendered)
		}
		for idx, line := range lines {
			if w := lipgloss.Width(line); w > 26 {
				t.Fatalf("line %d exceeded narrow TreeModel width 26 (got %d): %q", idx, w, line)
			}
		}

		// BuildFromSnapshot deep-copy isolation: mutating TreeModel must NOT mutate snapshot.TreeRoots!
		snap := NewSnapshotBuilder(copyIssues(cjkIssues)).Build()
		snap.Analysis.WaitForPhase2()
		if len(snap.TreeRoots) == 0 || !snap.TreeRoots[0].Expanded {
			t.Fatalf("precondition: expected expanded root in snap.TreeRoots, got %+v", snap.TreeRoots)
		}
		tmSnap := NewTreeModel(theme)
		tmSnap.BuildFromSnapshot(snap)
		tmSnap.CollapseAll()
		if !snap.TreeRoots[0].Expanded {
			t.Fatal("CollapseAll on TreeModel mutated shared DataSnapshot.TreeRoots[0].Expanded")
		}

		// Deterministic ID tie-breakers in SnapshotBuilder.Build, BoardModel, and FileChangedMsg:
		tieInput := []model.Issue{
			{ID: "TIE-B", Title: "Second ID", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: sameTime, UpdatedAt: sameTime},
			{ID: "TIE-A", Title: "First ID", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: sameTime, UpdatedAt: sameTime},
		}
		tieSnap := NewSnapshotBuilder(copyIssues(tieInput)).Build()
		tieSnap.Analysis.WaitForPhase2()
		if len(tieSnap.Issues) != 2 || tieSnap.Issues[0].ID != "TIE-A" || tieSnap.Issues[1].ID != "TIE-B" {
			t.Fatalf("expected SnapshotBuilder deterministic ID tie-breaker [TIE-A, TIE-B], got [%s, %s]",
				tieSnap.Issues[0].ID, tieSnap.Issues[1].ID)
		}

		bm := NewBoardModel(copyIssues(tieInput), theme)
		if sel := bm.SelectedIssue(); sel == nil || sel.ID != "TIE-A" {
			t.Fatalf("expected BoardModel deterministic ID tie-breaker to put TIE-A first, got %v", sel)
		}

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsFile := filepath.Join(beadsDir, "issues.jsonl")
		lineB, _ := json.Marshal(tieInput[0])
		lineA, _ := json.Marshal(tieInput[1])
		if err := os.WriteFile(beadsFile, []byte(string(lineB)+"\n"+string(lineA)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mf := NewModel(copyIssues(tieInput), nil, beadsFile)
		t.Cleanup(mf.Stop)
		mf.Update(FileChangedMsg{})
		if len(mf.issues) != 2 || mf.issues[0].ID != "TIE-A" || mf.issues[1].ID != "TIE-B" {
			t.Fatalf("expected FileChangedMsg deterministic ID tie-breaker [TIE-A, TIE-B], got [%s, %s]",
				mf.issues[0].ID, mf.issues[1].ID)
		}
	})

	// Gap 8: Unified exportToMarkdown via export.GenerateReport + CapsLockTracker / TutorialKeyBindings / ShowTutorialMsg.
	t.Run("Gap8_UnifiedExportGenerateReportAndCapsLockTutorialBindings", func(t *testing.T) {
		exportDir := t.TempDir()
		issues := []model.Issue{
			{ID: "CLOSED-BLK", Title: "Resolved upstream blocker", Status: model.StatusClosed, Priority: 1},
			{
				ID: "OPEN-DEP", Title: "Open downstream task", Status: model.StatusOpen, Priority: 1,
				Dependencies: []*model.Dependency{{IssueID: "OPEN-DEP", DependsOnID: "CLOSED-BLK", Type: model.DepBlocks}},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.workDir = exportDir
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

		// CSV recipe export through export.GenerateReport:
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
		csvPath := filepath.Join(exportDir, strings.TrimSuffix(m.generateExportFilename(), ".md")+".csv")
		csvBytes, err := os.ReadFile(csvPath)
		if err != nil {
			t.Fatalf("expected CSV export at %s: %v", csvPath, err)
		}
		rows, err := csv.NewReader(strings.NewReader(string(csvBytes))).ReadAll()
		if err != nil || len(rows) != 2 {
			t.Fatalf("expected CSV header + 1 open row, got rows=%v err=%v", rows, err)
		}

		// CapsLockTracker single-tap ('caps_lock') -> timer expired -> full tutorial:
		mt := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(mt.Stop)
		mt.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

		_, tickCmd := mt.Update(keyRune("caps_lock"))
		if mt.showTutorial {
			t.Fatal("expected first 'caps_lock' with DoubleTapEnable=true to wait for double-tap window before opening tutorial")
		}
		if tickCmd == nil || !mt.capsLockTracker.IsPending() {
			t.Fatal("expected pending CapsLockTracker timer after single 'caps_lock'")
		}
		mt.Update(CapsLockTimerExpiredMsg{})
		if !mt.showTutorial || mt.IsTutorialContextMode() {
			t.Fatalf("expected CapsLockTimerExpiredMsg to open full tutorial, got show=%v ctx=%v", mt.showTutorial, mt.IsTutorialContextMode())
		}
		mt.Update(keyRune("`")) // toggle close
		if mt.showTutorial {
			t.Fatal("expected '`' inside tutorial to close tutorial")
		}

		// CapsLockTracker double-tap -> context-specific tutorial:
		mt.Update(keyRune("`"))
		mt.Update(keyRune("`"))
		if !mt.showTutorial || !mt.IsTutorialContextMode() {
			t.Fatalf("expected double-tap '`' to open context-specific tutorial, got show=%v ctx=%v", mt.showTutorial, mt.IsTutorialContextMode())
		}
		mt.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// ShowTutorialMsg message handling:
		mt.Update(ShowTutorialMsg{ContextOnly: true, Context: "insights"})
		if !mt.showTutorial || !mt.IsTutorialContextMode() {
			t.Fatal("expected ShowTutorialMsg{ContextOnly: true} to open tutorial in context mode")
		}
		mt.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// Custom TutorialKeyBindings (HelpModalSpace=false, DoubleTapEnable=false):
		mt.SetTutorialKeyBindings(TutorialKeyBindings{
			DirectTutorial:  "f2",
			ContextHelp:     "f3",
			HelpModalSpace:  false,
			DoubleTapEnable: false,
		})
		mt.Update(keyRune("?"))
		if !mt.showHelp {
			t.Fatal("expected '?' to open help modal")
		}
		mt.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		if mt.showTutorial {
			t.Fatal("expected Space in help modal NOT to open tutorial when HelpModalSpace=false")
		}
		mt.Update(tea.KeyMsg{Type: tea.KeyF3})
		if !mt.showTutorial || !mt.IsTutorialContextMode() {
			t.Fatal("expected custom ContextHelp='f3' to open context tutorial")
		}
	})
}

func TestWave13Round1AuditRemediations(t *testing.T) {
	keyRune := func(s string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}

	// [A-6] TUI Semantic Search Scope Key
	t.Run("A6_SemanticSearchScopeKey", func(t *testing.T) {
		t.Setenv("BV_SEMANTIC_EMBEDDER", "hash")
		workDir := t.TempDir()
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		canonicalJSONL := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(canonicalJSONL, []byte(`{"id":"SEM-1","title":"Semantic scope test","status":"open","priority":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		issues := []model.Issue{{ID: "SEM-1", Title: "Semantic scope test", Status: model.StatusOpen, Priority: 1}}
		cfg := search.EmbeddingConfigFromEnv()

		// 1) Canonical <workDir>/.beads/issues.jsonl -> scopeKey == "" -> DefaultIndexPath unchanged.
		mCanon := NewModel(copyIssues(issues), nil, canonicalJSONL)
		t.Cleanup(mCanon.Stop)
		mCanon.SetWorkDir(workDir)
		if gotKey := mCanon.semanticIndexScopeKey(); gotKey != "" {
			t.Fatalf("expected empty scopeKey for canonical .beads/issues.jsonl, got %q", gotKey)
		}
		cmdCanon := mCanon.startSemanticIndexBuild()
		if cmdCanon == nil {
			t.Fatal("expected non-nil startSemanticIndexBuild cmd")
		}
		readyCanon, ok := cmdCanon().(SemanticIndexReadyMsg)
		if !ok || readyCanon.Error != nil {
			t.Fatalf("BuildSemanticIndexCmd canonical failed: %+v", readyCanon)
		}
		wantDefaultPath := search.DefaultIndexPath(workDir, cfg)
		if readyCanon.IndexPath != wantDefaultPath {
			t.Fatalf("canonical IndexPath = %q, want %q", readyCanon.IndexPath, wantDefaultPath)
		}

		// 2) Concrete file outside <workDir>/.beads -> "db:<cleanAbsBeads>" -> scoped .bvvi path.
		extDir := t.TempDir()
		externalBeads := filepath.Join(extDir, "external.db")
		mExt := NewModel(copyIssues(issues), nil, externalBeads)
		t.Cleanup(mExt.Stop)
		mExt.SetWorkDir(workDir)
		wantExtKey := "db:" + filepath.Clean(externalBeads)
		if gotKey := mExt.semanticIndexScopeKey(); gotKey != wantExtKey {
			t.Fatalf("external scopeKey = %q, want %q", gotKey, wantExtKey)
		}
		cmdExt := mExt.startSemanticIndexBuild()
		readyExt := cmdExt().(SemanticIndexReadyMsg)
		wantScopedPath := search.IndexPathForScope(workDir, cfg, wantExtKey)
		if readyExt.IndexPath != wantScopedPath || readyExt.IndexPath == wantDefaultPath {
			t.Fatalf("external IndexPath = %q, want scoped %q (!= %q)", readyExt.IndexPath, wantScopedPath, wantDefaultPath)
		}

		// 3) Non-canonical custom .sqlite inside <workDir>/.beads -> "db:<cleanAbsBeads>".
		customInBeads := filepath.Join(beadsDir, "custom.sqlite")
		if got := semanticIndexScopeKey(workDir, customInBeads, false); got != "db:"+filepath.Clean(customInBeads) {
			t.Fatalf("custom sqlite inside .beads scopeKey = %q, want %q", got, "db:"+filepath.Clean(customInBeads))
		}

		// 4) git: historical reference or empty path -> scopeKey == "" and in-memory IndexPath == "".
		mGit := NewModel(copyIssues(issues), nil, "git:HEAD~1:.beads/issues.jsonl")
		t.Cleanup(mGit.Stop)
		mGit.SetWorkDir(workDir)
		if gotKey := mGit.semanticIndexScopeKey(); gotKey != "" {
			t.Fatalf("git: beadsPath scopeKey = %q, want empty", gotKey)
		}
		readyGit := mGit.startSemanticIndexBuild()().(SemanticIndexReadyMsg)
		if readyGit.IndexPath != "" {
			t.Fatalf("expected git: historical build to stay in-memory (empty IndexPath), got %q", readyGit.IndexPath)
		}
	})

	// [B-1] SourceReloadResult Path/Degraded/Duration Propagation & Slow-Reload Auto-Background Promotion
	t.Run("B1_SourceReloadResultPathDegradedDurationAndAutoPromotion", func(t *testing.T) {
		liveIssue, jsonlPath, _ := installEditorTracker(t, "br")
		t.Setenv("BV_BACKGROUND_MODE", "")
		t.Setenv("BEADS_DB", "")
		beadsDir := filepath.Dir(jsonlPath)
		realProjDir := filepath.Dir(beadsDir)
		dbPath := filepath.Join(beadsDir, "beads.db")

		makeLiveIssues := func(complete bool) []model.Issue {
			cloned := liveIssue.Clone()
			if !complete && cloned.Origin != nil {
				cloned.Origin.ReadOnlyReason = "incomplete source"
			}
			return []model.Issue{cloned}
		}

		m := NewModel(makeLiveIssues(true), nil, jsonlPath)
		t.Cleanup(m.Stop)
		m.SetWorkDir(realProjDir)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		wantJSONLPath := jsonlPath
		if resolved, err := filepath.EvalSymlinks(jsonlPath); err == nil && resolved != "" {
			wantJSONLPath = resolved
		}
		if gotPath, guiErr := m.guiEditorPath(); guiErr != nil || gotPath != wantJSONLPath {
			t.Fatalf("initial guiEditorPath() = (%q, %v), want (%q, nil)", gotPath, guiErr, wantJSONLPath)
		}

		// 1) Reload switches .jsonl -> .db with SourceDegraded=true: guiEditorPath() must immediately reject .db,
		// and sourceReloadCmd() must measure ReloadDuration > 0 when callback leaves ReloadDuration == 0.
		m.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			time.Sleep(5 * time.Millisecond)
			degraded := makeLiveIssues(false)
			return SourceReloadResult{
				Issues:         degraded,
				Authority:      model.NewReadinessIndex(degraded),
				SourcePath:     dbPath,
				SourceDegraded: true,
				Warnings:       []string{"switched to db"},
			}, nil
		})
		_, cmd1 := m.Update(FileChangedMsg{})
		msg1 := cmd1().(FileChangedMsg)
		if msg1.source == nil || msg1.source.ReloadDuration <= 0 {
			t.Fatalf("expected sourceReloadCmd to populate ReloadDuration > 0, got %+v", msg1.source)
		}
		m.Update(msg1)
		if m.beadsPath != dbPath || !m.sourceDegraded || !m.authorityIncomplete {
			t.Fatalf("after .jsonl->.db reload: beadsPath=%q degraded=%v incomplete=%v", m.beadsPath, m.sourceDegraded, m.authorityIncomplete)
		}
		if gotPath, guiErr := m.guiEditorPath(); guiErr == nil {
			t.Fatalf("expected guiEditorPath() to reject .db source, got path=%q", gotPath)
		}

		// 2) Reload switches .db -> .jsonl with SourceDegraded=false, AuthorityIncomplete=false, and ReloadDuration >= 1s:
		// guiEditorPath() must immediately succeed again, while sourceReload != nil (auto-discovered sibling SQLite coexistence)
		// must NOT auto-promote to JSONL-only BackgroundWorker unless restoreBackgroundAfterEdit was explicitly active!
		m.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			live := makeLiveIssues(true)
			return SourceReloadResult{
				Issues:              live,
				Authority:           model.NewReadinessIndex(live),
				SourcePath:          jsonlPath,
				SourceDegraded:      false,
				AuthorityIncomplete: false,
				Warnings:            []string{"non-authority informational note"},
				ReloadDuration:      1200 * time.Millisecond,
			}, nil
		})
		_, cmd2 := m.Update(FileChangedMsg{})
		m.Update(cmd2())
		if m.beadsPath != jsonlPath || m.sourceDegraded || m.authorityIncomplete {
			t.Fatalf("after .db->.jsonl reload: beadsPath=%q degraded=%v incomplete=%v", m.beadsPath, m.sourceDegraded, m.authorityIncomplete)
		}
		if gotPath, guiErr := m.guiEditorPath(); guiErr != nil || gotPath != wantJSONLPath {
			t.Fatalf("after .db->.jsonl reload, guiEditorPath() = (%q, %v), want (%q, nil)", gotPath, guiErr, wantJSONLPath)
		}
		if m.backgroundWorker != nil {
			t.Fatalf("expected sourceReload != nil NOT to auto-promote to JSONL-only BackgroundWorker; status=%q", m.statusMsg)
		}
		// Explicit restoreBackgroundAfterEdit=true DOES restore BackgroundWorker and still routes msg.source==nil through sourceReloadCmd!
		m.restoreBackgroundAfterEdit = true
		_, cmd2b := m.Update(FileChangedMsg{})
		m.Update(cmd2b())
		if m.backgroundWorker == nil {
			t.Fatalf("expected restoreBackgroundAfterEdit=true to restore BackgroundWorker; status=%q", m.statusMsg)
		}
		if _, cmd2c := m.Update(FileChangedMsg{}); cmd2c == nil {
			t.Fatalf("expected FileChangedMsg with sourceReload != nil and backgroundWorker != nil to still invoke sourceReloadCmd")
		}

		// 3) Negative control: explicit --db (SetExplicitDB(true)) must NOT auto-promote to JSONL BackgroundWorker.
		mExplicit := NewModel(makeLiveIssues(true), nil, jsonlPath, ReadinessScope{ExplicitDB: true})
		t.Cleanup(mExplicit.Stop)
		mExplicit.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			live := makeLiveIssues(true)
			return SourceReloadResult{
				Issues:         live,
				Authority:      model.NewReadinessIndex(live),
				SourcePath:     jsonlPath,
				SourceDegraded: true,
				ReloadDuration: 1200 * time.Millisecond,
			}, nil
		})
		_, cmd3 := mExplicit.Update(FileChangedMsg{})
		mExplicit.Update(cmd3())
		if mExplicit.backgroundWorker != nil {
			t.Fatal("expected ExplicitDB model NOT to auto-promote to JSONL BackgroundWorker")
		}
		if !strings.Contains(mExplicit.statusMsg, "consider BV_BACKGROUND_MODE=1") {
			t.Fatalf("expected ExplicitDB slow reload to suggest BV_BACKGROUND_MODE=1, got %q", mExplicit.statusMsg)
		}
	})

	// [B-2] Preserve flowDetailID Full-Width Sizing & Issue Content Across Modal Overlays
	t.Run("B2_FlowDetailFullWidthAndIssuePreservedAcrossModals", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "LIST-TOP", Title: "Unrelated Top List Issue", Description: "UnrelatedTopListMarkerBody", Status: model.StatusOpen, Priority: 0},
			{ID: "FLOW-BLK", Title: "Flow Blocker Issue", Status: model.StatusOpen, Priority: 1, Labels: []string{"ops"}},
			{
				ID: "FLOW-END", Title: "Flow Endpoint Target Issue", Description: "UniqueFlowDetailMarkerBody",
				Status: model.StatusOpen, Priority: 2, Labels: []string{"api"},
				Dependencies: []*model.Dependency{{IssueID: "FLOW-END", DependsOnID: "FLOW-BLK", Type: model.DepBlocks}},
			},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
		if !m.isSplitView {
			t.Fatal("precondition: expected split view at width 140")
		}
		// Ensure list cursor is on LIST-TOP, NOT FLOW-END.
		m.list.Select(0)

		// Open Flow Matrix ('f'), enter drilldown ('Enter'), select FLOW-END ('Down'), and open flowDetailID ('Enter').
		m.Update(keyRune("f"))
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.flowDetailID != "FLOW-END" {
			t.Fatalf("expected flowDetailID=FLOW-END after drilldown Enter, got %q", m.flowDetailID)
		}
		if m.viewport.Width != 140 {
			t.Fatalf("expected full-width viewport 140 in flowDetailID, got %d", m.viewport.Width)
		}
		if !strings.Contains(ansi.Strip(m.viewport.View()), "UniqueFlowDetailMarkerBody") {
			t.Fatalf("expected FLOW-END content in viewport, got:\n%s", ansi.Strip(m.viewport.View()))
		}

		// Press 'V' over flowDetailID and deliver cassSessionsLoadedMsg to open CassModal.
		_ = m.showCassSessionModal()
		if m.cassRequest == nil || m.cassRequest.beadID != "FLOW-END" {
			t.Fatalf("expected cassRequest for FLOW-END, got %+v", m.cassRequest)
		}
		m.Update(cassSessionsLoadedMsg{
			request: m.cassRequest,
			status:  cass.StatusHealthy,
			result: cass.CorrelationResult{
				BeadID: "FLOW-END",
				TopSessions: []cass.ScoredResult{
					{SearchResult: cass.SearchResult{Title: "Flow Debug Session", SourcePath: "/tmp/session.jsonl"}},
				},
			},
		})
		if !m.showCassModal || m.focused != focusCassModal {
			t.Fatalf("expected focusCassModal open over flowDetailID, got show=%v focus=%v", m.showCassModal, m.focused)
		}

		// Resize terminal AND deliver SnapshotReadyMsg while CassModal is open!
		m.Update(tea.WindowSizeMsg{Width: 152, Height: 38})
		snap := NewSnapshotBuilder(copyIssues(issues)).Build()
		snap.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})

		// Dismiss CassModal with Esc: flowDetailID must retain full width (152) and FLOW-END content!
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.focused != focusFlowMatrix || m.flowDetailID != "FLOW-END" {
			t.Fatalf("expected return to focusFlowMatrix with flowDetailID=FLOW-END, got focus=%v flowDetailID=%q", m.focused, m.flowDetailID)
		}
		if m.viewport.Width != 152 {
			t.Fatalf("expected full-width viewport 152 preserved across V -> WindowSizeMsg -> Esc, got %d", m.viewport.Width)
		}
		vpPlain := ansi.Strip(m.viewport.View())
		if !strings.Contains(vpPlain, "UniqueFlowDetailMarkerBody") || strings.Contains(vpPlain, "Unrelated Top List Issue") {
			t.Fatalf("expected FLOW-END content preserved (not overwritten by list selection), got:\n%s", vpPlain)
		}

		// Also verify Help ('?') and AgentPrompt modals over flowDetailID + WindowSizeMsg + Esc.
		m.Update(keyRune("?"))
		if !m.showHelp || m.focused != focusHelp {
			t.Fatalf("expected Help modal open over flowDetailID, got show=%v focus=%v", m.showHelp, m.focused)
		}
		m.Update(tea.WindowSizeMsg{Width: 148, Height: 36})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.viewport.Width != 148 || !strings.Contains(ansi.Strip(m.viewport.View()), "UniqueFlowDetailMarkerBody") {
			t.Fatalf("expected full width 148 & FLOW-END content preserved across Help modal, got width=%d", m.viewport.Width)
		}

		m.Update(AgentFileCheckMsg{ShouldPrompt: true, FilePath: "/tmp/AGENTS.md", FileType: "AGENTS.md"})
		m.Update(tea.WindowSizeMsg{Width: 146, Height: 36})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.viewport.Width != 146 || !strings.Contains(ansi.Strip(m.viewport.View()), "UniqueFlowDetailMarkerBody") {
			t.Fatalf("expected full width 146 & FLOW-END content preserved across AgentPrompt, got width=%d", m.viewport.Width)
		}
	})

	// [B-3] SnapshotReadyMsg Candidate-Scoped Alerts & RelatedIssueID Scope Filtering
	t.Run("B3_SnapshotReadyCandidateScopedAlertsAndRelatedIssueIDScope", func(t *testing.T) {
		now := time.Now().UTC()
		staleTime := now.Add(-45 * 24 * time.Hour)
		issues := []model.Issue{
			{ID: "cand-1", Title: "Fresh candidate issue", Status: model.StatusOpen, Priority: 1, CreatedAt: now, UpdatedAt: now},
			// Non-candidate stale issue that triggers stale alert in unscoped SnapshotBuilder:
			{ID: "ctx-stale", Title: "Ancient context issue", Status: model.StatusOpen, Priority: 1, CreatedAt: staleTime, UpdatedAt: staleTime},
		}
		auth := model.NewReadinessIndex(issues)
		cands := map[string]bool{"cand-1": true}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: auth, CandidateIDs: cands})
		t.Cleanup(m.Stop)
		m.SetWorkDir(t.TempDir())

		// Unscoped SnapshotBuilder includes stale alert for ctx-stale (and any global unscoped alerts).
		snap := NewSnapshotBuilder(copyIssues(issues), auth).Build()
		snap.Analysis.WaitForPhase2()
		snap.alerts = append(snap.alerts,
			drift.Alert{Type: drift.AlertBlockingCascade, Severity: drift.SeverityCritical, Message: "Unscoped global cascade"},
			drift.Alert{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "ctx-stale", Message: "Stale context"},
		)
		snap.alertsCritical = 1
		snap.alertsWarning = 1

		m.Update(SnapshotReadyMsg{Snapshot: snap, SnapshotVer: 1})
		for _, a := range m.activeScopedAlerts() {
			if a.Message == "Unscoped global cascade" || a.IssueID == "ctx-stale" {
				t.Fatalf("unscoped snapshot alert leaked into candidate-scoped model: %+v", a)
			}
		}
		if m.alertsCritical != 0 || m.alertsWarning != 0 {
			t.Fatalf("expected recomputed visible alert counts (0, 0), got critical=%d warning=%d", m.alertsCritical, m.alertsWarning)
		}

		// RelatedIssueID scope filtering & distinct alertKey dismissal for AlertPotentialDuplicate:
		dupIssues := []model.Issue{
			{ID: "api-1", Title: "Auth token validation", Status: model.StatusOpen, Priority: 1},
			{ID: "api-2", Title: "Auth token validator", Status: model.StatusOpen, Priority: 1},
			{ID: "api-3", Title: "Auth token check", Status: model.StatusOpen, Priority: 1},
			{ID: "web-1", Title: "Auth token validation UI", Status: model.StatusOpen, Priority: 1},
		}
		mDup := NewModel(copyIssues(dupIssues), nil, "")
		t.Cleanup(mDup.Stop)
		mDup.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
		mDup.activeRepos = map[string]bool{"api": true}
		mDup.alerts = []drift.Alert{
			{Type: drift.AlertPotentialDuplicate, Severity: drift.SeverityInfo, IssueID: "api-1", RelatedIssueID: "web-1", Message: "cross-repo dup"},
			{Type: drift.AlertPotentialDuplicate, Severity: drift.SeverityInfo, IssueID: "api-1", RelatedIssueID: "api-2", Message: "in-repo dup 1-2"},
			{Type: drift.AlertPotentialDuplicate, Severity: drift.SeverityInfo, IssueID: "api-1", RelatedIssueID: "api-3", Message: "in-repo dup 1-3"},
		}
		mDup.recomputeVisibleAlertCounts()

		scopedAlerts := mDup.activeScopedAlerts()
		if len(scopedAlerts) != 2 || mDup.alertsInfo != 2 {
			t.Fatalf("expected cross-repo RelatedIssueID=web-1 filtered out leaving 2 alerts, got %d (info=%d): %+v",
				len(scopedAlerts), mDup.alertsInfo, scopedAlerts)
		}
		if alertKey(scopedAlerts[0]) == alertKey(scopedAlerts[1]) {
			t.Fatalf("expected distinct alertKeys for distinct RelatedIssueIDs on api-1, got %q", alertKey(scopedAlerts[0]))
		}

		// Open Alerts panel ('!') and press 'd' once: only (api-1, api-2) is dismissed; (api-1, api-3) remains!
		mDup.Update(keyRune("!"))
		if !mDup.showAlertsPanel {
			t.Fatal("expected '!' to open alerts panel")
		}
		mDup.Update(keyRune("d"))
		remaining := mDup.activeScopedAlerts()
		if len(remaining) != 1 || remaining[0].RelatedIssueID != "api-3" || mDup.alertsInfo != 1 {
			t.Fatalf("expected only (api-1, api-3) remaining after single 'd', got remaining=%+v info=%d", remaining, mDup.alertsInfo)
		}
	})

	// [B-4] Unified TUI exportToMarkdown() GeneratedAt, Canonical Metrics, & Repo-Scoped GraphStats
	t.Run("B4_ExportGeneratedAtCanonicalMetricsAndRepoScopedGraphStats", func(t *testing.T) {
		histRef := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		deferUntil := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC) // Future at histRef, past at real time.Now()!
		issues := []model.Issue{
			{
				ID:         "EXP-DEFER",
				Title:      "Deferred at historical timestamp",
				Status:     model.StatusOpen,
				Priority:   1,
				IssueType:  model.TypeTask,
				CreatedAt:  histRef.Add(-24 * time.Hour),
				UpdatedAt:  histRef,
				DeferUntil: &deferUntil,
			},
		}

		// 1) Default Markdown export stamps historical GeneratedAt and evaluates Claimable at GeneratedAt.
		outDir1 := t.TempDir()
		beadsFile1 := filepath.Join(outDir1, ".beads", "issues.jsonl")
		if err := os.MkdirAll(filepath.Dir(beadsFile1), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(beadsFile1, []byte(`{"id":"EXP-DEFER","title":"Deferred","status":"open","priority":1}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mHist := NewModel(copyIssues(issues), nil, beadsFile1)
		t.Cleanup(mHist.Stop)
		mHist.SetWorkDir(outDir1)
		mHist.analyzer.SetNow(histRef)
		mHist.exportToMarkdown()
		mdBytes, err := os.ReadFile(filepath.Join(outDir1, mHist.generateExportFilename()))
		if err != nil {
			t.Fatalf("reading default markdown export: %v", err)
		}
		mdText := string(mdBytes)
		wantStamp := fmt.Sprintf("*Generated: %s*", histRef.Format(time.RFC1123))
		if !strings.Contains(mdText, wantStamp) {
			t.Fatalf("expected historical Generated stamp %q in Markdown export, got:\n%s", wantStamp, mdText)
		}
		if strings.Contains(mdText, "--claim") {
			t.Fatalf("expected EXP-DEFER NOT claimable at historical GeneratedAt %s, got:\n%s", histRef, mdText)
		}

		// 2) Recipe JSON export with ShowMetrics=true and empty Metrics uses canonical 8-key metric set + historical GeneratedAt.
		outDir2 := t.TempDir()
		jsonRecipe := &recipe.Recipe{
			Name:   "canonical-metrics",
			View:   recipe.ViewConfig{ShowMetrics: true},
			Export: recipe.ExportConfig{Format: "json"},
		}
		mMetrics := NewModel(copyIssues(issues), jsonRecipe, beadsFile1)
		t.Cleanup(mMetrics.Stop)
		mMetrics.SetWorkDir(outDir2)
		mMetrics.analyzer.SetNow(histRef)
		mMetrics.analysis.WaitForPhase2()
		mMetrics.exportToMarkdown()

		jsonPath2 := filepath.Join(outDir2, fmt.Sprintf("beads_export_%s.json", histRef.Format("2006-01-02")))
		rawJSON2, err := os.ReadFile(jsonPath2)
		if err != nil {
			t.Fatalf("reading recipe JSON export: %v", err)
		}
		var parsed2 struct {
			GeneratedAt time.Time `json:"generated_at"`
			Issues      []struct {
				ID      string             `json:"id"`
				Metrics map[string]float64 `json:"metrics"`
				Actions struct {
					ClaimCommand string `json:"claim_command"`
				} `json:"actions"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(rawJSON2, &parsed2); err != nil {
			t.Fatalf("unmarshaling recipe JSON export: %v", err)
		}
		if !parsed2.GeneratedAt.Equal(histRef) {
			t.Fatalf("JSON generated_at = %v, want %v", parsed2.GeneratedAt, histRef)
		}
		if len(parsed2.Issues) != 1 || parsed2.Issues[0].Actions.ClaimCommand != "" {
			t.Fatalf("expected empty claim_command at historical ref time, got %+v", parsed2.Issues)
		}
		want8Keys := []string{"pagerank", "betweenness", "critical_path", "eigenvector", "hubs", "authorities", "in_degree", "out_degree"}
		for _, k := range want8Keys {
			if _, exists := parsed2.Issues[0].Metrics[k]; !exists {
				t.Fatalf("missing canonical metric key %q in %v", k, parsed2.Issues[0].Metrics)
			}
		}

		// 3) Workspace repo-filtered export passes repo-scoped GraphStats (not unscoped multi-repo m.analysis).
		outDir3 := t.TempDir()
		wsIssues := []model.Issue{
			{ID: "api-1", Title: "API root", Status: model.StatusOpen, Priority: 1},
			{ID: "api-2", Title: "API leaf", Status: model.StatusOpen, Priority: 2, Dependencies: []*model.Dependency{{IssueID: "api-2", DependsOnID: "api-1", Type: model.DepBlocks}}},
			{ID: "web-1", Title: "Web 1", Status: model.StatusOpen, Priority: 1},
			{ID: "web-2", Title: "Web 2", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{{IssueID: "web-2", DependsOnID: "web-1", Type: model.DepBlocks}}},
			{ID: "web-3", Title: "Web 3", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{{IssueID: "web-3", DependsOnID: "web-2", Type: model.DepBlocks}}},
		}
		wsRecipe := &recipe.Recipe{
			Name:   "ws-json",
			Export: recipe.ExportConfig{Format: "json"},
		}
		mWS := NewModel(copyIssues(wsIssues), wsRecipe, "")
		t.Cleanup(mWS.Stop)
		mWS.SetWorkDir(outDir3)
		mWS.analysis.WaitForPhase2()
		mWS.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
		mWS.activeRepos = map[string]bool{"api": true}
		mWS.applyRecipe(wsRecipe)
		mWS.exportToMarkdown()

		rawWS, err := os.ReadFile(filepath.Join(outDir3, fmt.Sprintf("beads_export_%s.json", time.Now().Format("2006-01-02"))))
		if err != nil {
			t.Fatalf("reading workspace JSON export: %v", err)
		}
		var parsedWS struct {
			Graph struct {
				Nodes int `json:"nodes"`
				Edges int `json:"edges"`
			} `json:"graph"`
		}
		if err := json.Unmarshal(rawWS, &parsedWS); err != nil {
			t.Fatalf("unmarshaling workspace JSON export: %v", err)
		}
		if parsedWS.Graph.Nodes != 2 || parsedWS.Graph.Edges != 1 {
			t.Fatalf("expected repo-scoped graph (2 nodes, 1 edge), got nodes=%d edges=%d:\n%s",
				parsedWS.Graph.Nodes, parsedWS.Graph.Edges, string(rawWS))
		}
	})

	// [B-5], [B-6], [B-7] rebuildInsightsPanel() Refresh, Lazy Gate, ClampSelections, & O(1) Caching
	t.Run("B5_B6_B7_InsightsRefreshLazyGateClampAndCache", func(t *testing.T) {
		makeGraphIssues := func(count int) []model.Issue {
			out := make([]model.Issue, 0, count)
			for idx := 0; idx < count; idx++ {
				id := fmt.Sprintf("api-%d", idx+1)
				iss := model.Issue{ID: id, Title: fmt.Sprintf("Issue %d", idx+1), Status: model.StatusOpen, Priority: 1}
				if idx > 0 {
					prev := fmt.Sprintf("api-%d", idx)
					iss.Dependencies = []*model.Dependency{{IssueID: id, DependsOnID: prev, Type: model.DepBlocks}}
				}
				out = append(out, iss)
			}
			return out
		}

		// 1) [B-6] Lazy gate in workspace repo-scoped mode while focus != focusInsights:
		m := NewModel(append(makeGraphIssues(4), model.Issue{ID: "web-1", Title: "Web", Status: model.StatusOpen, Priority: 1}), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
		m.analysis.WaitForPhase2()
		m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, RepoPrefixes: []string{"api", "web"}})
		m.activeRepos = map[string]bool{"api": true}

		snap4 := NewSnapshotBuilder(append(makeGraphIssues(4), model.Issue{ID: "web-1", Title: "Web", Status: model.StatusOpen, Priority: 1})).Build()
		snap4.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snap4, SnapshotVer: 1})
		if m.insightsPanel.advancedInsights != nil || m.advancedInsightsComputes != 0 {
			t.Fatalf("expected lazy gate to skip GenerateAdvancedInsightsFromStats while focusList active, got adv=%v computes=%d",
				m.insightsPanel.advancedInsights, m.advancedInsightsComputes)
		}

		// 2) [B-7] O(1) cache hit across i -> Esc -> i (in workspace repo-scoped mode AND single-repo mode):
		m.Update(keyRune("i"))
		if m.focused != focusInsights || m.insightsPanel.advancedInsights == nil || m.advancedInsightsComputes != 1 {
			t.Fatalf("expected 1st 'i' to compute AdvancedInsights once, got focus=%v adv=%v computes=%d",
				m.focused, m.insightsPanel.advancedInsights, m.advancedInsightsComputes)
		}
		firstAdvPtr := m.insightsPanel.advancedInsights

		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.focused != focusList {
			t.Fatalf("expected focusList after Esc, got %v", m.focused)
		}
		m.Update(keyRune("i"))
		if m.advancedInsightsComputes != 1 || m.insightsPanel.advancedInsights != firstAdvPtr {
			t.Fatalf("expected 2nd 'i' to reuse cached AdvancedInsights in O(1), got computes=%d samePtr=%v",
				m.advancedInsightsComputes, m.insightsPanel.advancedInsights == firstAdvPtr)
		}

		// 3) [B-5] SnapshotReadyMsg & Phase2ReadyMsg while focusInsights (even under Help modal) rebuild & ClampSelections on shrink:
		m.insightsPanel.focusedPanel = PanelBottlenecks
		m.insightsPanel.selectedIndex[PanelBottlenecks] = 99
		m.Update(keyRune("?")) // Help modal over focusInsights -> effectiveUnderlyingFocus() == focusInsights
		if !m.showHelp || m.effectiveUnderlyingFocus() != focusInsights {
			t.Fatalf("expected Help modal over focusInsights, got showHelp=%v underlying=%v", m.showHelp, m.effectiveUnderlyingFocus())
		}

		shrunk := []model.Issue{{ID: "api-1", Title: "Only issue left", Status: model.StatusOpen, Priority: 1}}
		snapShrunk := NewSnapshotBuilder(copyIssues(shrunk)).Build()
		snapShrunk.Analysis.WaitForPhase2()
		m.Update(SnapshotReadyMsg{Snapshot: snapShrunk, SnapshotVer: 2})

		cnt := m.insightsPanel.panelItemCount(PanelBottlenecks)
		maxAllowed := 0
		if cnt > 0 {
			maxAllowed = cnt - 1
		}
		if m.insightsPanel.selectedIndex[PanelBottlenecks] > maxAllowed {
			t.Fatalf("expected SnapshotReadyMsg to run rebuildInsightsPanel+ClampSelections (max %d), got %d",
				maxAllowed, m.insightsPanel.selectedIndex[PanelBottlenecks])
		}
		if m.advancedInsightsComputes != 2 {
			t.Fatalf("expected new snapshot in focusInsights to recompute AdvancedInsights (computes=2), got %d", m.advancedInsightsComputes)
		}
	})

	// [B-8] & [B-9] canScrollShortcutsSidebar() & CapsLockTimerExpiredMsg guards
	t.Run("B8_B9_ShortcutsSidebarScrollGuardsAndCapsLockTimerReset", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "SB-1", Title: "Scroll guard test", Status: model.StatusOpen, Priority: 1, Labels: []string{"core"}},
		}
		m := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 36})
		m.showShortcutsSidebar = true

		// [B-8] PgUp/PgDown must NOT be hijacked by shortcuts sidebar in scrollable views:
		for _, f := range []focus{focusInsights, focusAttention, focusLabelDashboard, focusVelocityComparison} {
			m.focused = f
			if m.canScrollShortcutsSidebar("pgup") || m.canScrollShortcutsSidebar("pgdown") {
				t.Fatalf("canScrollShortcutsSidebar must be false for pgup/pgdown in focus %v", f)
			}
		}

		// [B-9] Pressing any non-CapsLock key while CapsLockTracker.IsPending() resets the pending timer:
		m.focused = focusList
		_, _ = m.Update(keyRune("caps_lock"))
		if !m.capsLockTracker.IsPending() {
			t.Fatal("precondition: expected pending capsLockTracker after single 'caps_lock'")
		}
		m.Update(keyRune("j"))
		if m.capsLockTracker.IsPending() {
			t.Fatal("expected non-CapsLock key 'j' to reset pending capsLockTracker")
		}
		m.Update(CapsLockTimerExpiredMsg{})
		if m.showTutorial {
			t.Fatal("expected CapsLockTimerExpiredMsg after reset NOT to open tutorial")
		}

		// [B-9] Guard CapsLockTimerExpiredMsg when text input is active ('/' filtering):
		m.Update(keyRune("/"))
		if !m.isTextInputActive() {
			t.Fatal("precondition: expected isTextInputActive() after '/'")
		}
		_, _ = m.capsLockTracker.HandlePress()
		m.Update(CapsLockTimerExpiredMsg{})
		if m.showTutorial {
			t.Fatal("expected CapsLockTimerExpiredMsg NOT to open tutorial while text input is active")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// [B-9] Guard CapsLockTimerExpiredMsg when showQuitConfirm is active:
		m.showQuitConfirm = true
		_, _ = m.capsLockTracker.HandlePress()
		m.Update(CapsLockTimerExpiredMsg{})
		if m.showTutorial {
			t.Fatal("expected CapsLockTimerExpiredMsg NOT to open tutorial while showQuitConfirm is active")
		}
	})

	// [R2-B1] & [R2-B2] DisableInternalWatcher round-trip & candidate-scoped background promotion guard
	t.Run("R2_B1_B2_DisableInternalWatcherAndCandidateScopedNoBackgroundPromotion", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		dbPath := filepath.Join(beadsDir, "beads.db")
		line := `{"id":"ISS-1","title":"Live reload issue","status":"open","priority":1,"issue_type":"task"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}

		seed := []model.Issue{{ID: "ISS-1", Title: "Live reload issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
		m := NewModel(copyIssues(seed), nil, jsonlPath, ReadinessScope{
			Authority:    model.NewReadinessIndex(seed),
			CandidateIDs: map[string]bool{"ISS-1": true},
		})
		t.Cleanup(m.Stop)
		m.DisableInternalWatcher()
		if m.watcher != nil {
			t.Fatal("expected DisableInternalWatcher() to stop and nil m.watcher")
		}

		reloadCalls := 0
		nextSource := dbPath
		m.SetSourceReload(func(ctx context.Context) (SourceReloadResult, error) {
			reloadCalls++
			live := copyIssues(seed)
			live[0].Title = fmt.Sprintf("Reload #%d", reloadCalls)
			return SourceReloadResult{
				Issues:         live,
				Authority:      model.NewReadinessIndex(live),
				CandidateIDs:   map[string]bool{"ISS-1": true},
				SourcePath:     nextSource,
				ReloadDuration: 1200 * time.Millisecond,
			}, nil
		})

		// Round-trip 1: .jsonl -> .db
		_, cmd1 := m.Update(FileChangedMsg{})
		m.Update(cmd1())
		if m.beadsPath != dbPath || m.watcher != nil {
			t.Fatalf("after .jsonl->.db reload: beadsPath=%q watcher=%v", m.beadsPath, m.watcher)
		}

		// Round-trip 2: .db -> .jsonl (slow >=1s, with CandidateIDs != nil)
		nextSource = jsonlPath
		_, cmd2 := m.Update(FileChangedMsg{})
		m.Update(cmd2())
		if m.beadsPath != jsonlPath || m.watcher != nil {
			t.Fatalf("after .db->.jsonl reload: beadsPath=%q watcher=%v", m.beadsPath, m.watcher)
		}
		if m.backgroundWorker != nil {
			t.Fatalf("expected candidate-scoped Model NOT to auto-promote to BackgroundWorker; status=%q", m.statusMsg)
		}

		// Round-trip 3: subsequent FileChangedMsg must still invoke m.sourceReload!
		_, cmd3 := m.Update(FileChangedMsg{})
		m.Update(cmd3())
		if reloadCalls != 3 {
			t.Fatalf("expected 3 sourceReload invocations across round-trips, got %d", reloadCalls)
		}
	})
}

func TestWave14TrackB(t *testing.T) {
	t.Run("FullCorpusTriageScoresAndUnblocksAcrossAll4Paths", func(t *testing.T) {
		now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
		var issues []model.Issue
		// 12 high-priority issues in repo "api" (8 blocked + 4 actionable) so top-10 Recommendations
		// are dominated by "api-*", leaving "web-*" issues outside global top-10.
		issues = append(issues, model.Issue{
			ID: "api-root", Title: "API Root Blocker", Status: model.StatusOpen, Priority: 0,
			IssueType: model.TypeTask, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now,
		})
		for i := 1; i <= 8; i++ {
			issues = append(issues, model.Issue{
				ID: fmt.Sprintf("api-blocked-%02d", i), Title: fmt.Sprintf("Blocked API %d", i),
				Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask,
				CreatedAt: now.Add(-time.Duration(i) * time.Hour), UpdatedAt: now,
				Dependencies: []*model.Dependency{{DependsOnID: "api-root", Type: model.DepBlocks}},
			})
		}
		for i := 1; i <= 10; i++ {
			issues = append(issues, model.Issue{
				ID: fmt.Sprintf("api-ready-%02d", i), Title: fmt.Sprintf("Ready API %d", i),
				Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: now.Add(-time.Duration(i) * time.Hour), UpdatedAt: now,
			})
		}
		// Lower-priority "web-*" issues (outside top-10 Recommendations), where web-hub unblocks 2 issues.
		issues = append(issues,
			model.Issue{
				ID: "web-hub", Title: "Web Hub Foundation", Status: model.StatusOpen, Priority: 3,
				IssueType: model.TypeTask, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now,
			},
			model.Issue{
				ID: "web-leaf-1", Title: "Web Leaf 1", Status: model.StatusOpen, Priority: 4,
				IssueType: model.TypeTask, CreatedAt: now.Add(-47 * time.Hour), UpdatedAt: now,
				Dependencies: []*model.Dependency{{DependsOnID: "web-hub", Type: model.DepBlocks}},
			},
			model.Issue{
				ID: "web-leaf-2", Title: "Web Leaf 2", Status: model.StatusOpen, Priority: 4,
				IssueType: model.TypeTask, CreatedAt: now.Add(-46 * time.Hour), UpdatedAt: now,
				Dependencies: []*model.Dependency{{DependsOnID: "web-hub", Type: model.DepBlocks}},
			},
		)
		for i := 1; i <= 10; i++ {
			issues = append(issues, model.Issue{
				ID: fmt.Sprintf("web-extra-%02d", i), Title: fmt.Sprintf("Web Extra %d", i),
				Status: model.StatusOpen, Priority: 4, IssueType: model.TypeTask,
				CreatedAt: now.Add(-time.Duration(50+i) * time.Hour), UpdatedAt: now,
			})
		}

		// Path 1: NewModel initial triage population (>25 open issues)
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: now})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})

		if len(m.triageScores) < len(issues) {
			t.Fatalf("Path 1 (NewModel): expected full-corpus triageScores for all %d open issues, got %d", len(issues), len(m.triageScores))
		}
		if m.triageScores["web-hub"] <= 0 {
			t.Fatalf("Path 1 (NewModel): expected non-zero triageScore for outside-top-10 issue web-hub, got %v", m.triageScores["web-hub"])
		}
		if got := len(m.unblocksMap["web-hub"]); got != 2 {
			t.Fatalf("Path 1 (NewModel): expected web-hub unblocksMap len=2, got %d (%v)", got, m.unblocksMap["web-hub"])
		}

		// Built-in triage recipe (max_items: 20) must give non-zero scores to ranks 11..20!
		triageRecipe := m.recipeLoader.Get("triage")
		if triageRecipe == nil {
			t.Fatalf("expected built-in triage recipe")
		}
		m.setActiveRecipe(triageRecipe)
		m.applyRecipe(triageRecipe)
		if got := len(m.list.Items()); got != 20 {
			t.Fatalf("triage recipe max_items=20: got %d items", got)
		}
		for idx, raw := range m.list.Items() {
			it, ok := raw.(IssueItem)
			if !ok {
				t.Fatalf("item %d: expected IssueItem, got %T", idx, raw)
			}
			if it.TriageScore <= 0 {
				t.Fatalf("triage recipe rank %d (%s): expected non-zero TriageScore, got %v", idx+1, it.Issue.ID, it.TriageScore)
			}
		}

		// Workspace repo filter ('w') to "web" must retain non-zero TriageScore & UnblocksCount=2 on web-hub!
		m.setActiveRecipe(nil)
		m.currentFilter = "all"
		m.workspaceMode = true
		m.availableRepos = []string{"api", "web"}
		m.activeRepos = map[string]bool{"web": true}
		m.applyFilter()
		foundWebHub := false
		for _, raw := range m.list.Items() {
			it, ok := raw.(IssueItem)
			if !ok {
				continue
			}
			if it.Issue.ID == "web-hub" {
				foundWebHub = true
				if it.TriageScore <= 0 || it.UnblocksCount != 2 {
					t.Fatalf("workspace repo filter 'web': web-hub TriageScore=%v UnblocksCount=%d, want >0 and 2", it.TriageScore, it.UnblocksCount)
				}
			}
		}
		if !foundWebHub {
			t.Fatalf("expected web-hub in workspace repo-filtered list")
		}

		// Path 2: refreshScopedTriageData()
		cands := make(map[string]bool, len(issues))
		for _, iss := range issues {
			cands[iss.ID] = true
		}
		mScoped := NewModel(copyIssues(issues), nil, "", ReadinessScope{CandidateIDs: cands, ReferenceNow: now})
		t.Cleanup(mScoped.Stop)
		mScoped.analysis.WaitForPhase2()
		mScoped.refreshScopedTriageData()
		if mScoped.triageScores["web-hub"] <= 0 || len(mScoped.unblocksMap["web-hub"]) != 2 {
			t.Fatalf("Path 2 (refreshScopedTriageData): web-hub score=%v unblocks=%v", mScoped.triageScores["web-hub"], mScoped.unblocksMap["web-hub"])
		}

		// Path 3 & Path 4: SnapshotBuilder.Build() & DataSnapshot.WithPhase2()
		sb := NewSnapshotBuilder(copyIssues(issues)).WithRecipe(triageRecipe)
		snap1 := sb.Build()
		if len(snap1.TriageScores) < len(issues) || snap1.TriageScores["web-hub"] <= 0 || len(snap1.UnblocksMap["web-hub"]) != 2 {
			t.Fatalf("Path 3 (SnapshotBuilder.Build): len=%d web-hub score=%v unblocks=%v", len(snap1.TriageScores), snap1.TriageScores["web-hub"], snap1.UnblocksMap["web-hub"])
		}
		snap1.Analysis.WaitForPhase2()
		ins := snap1.Analysis.GenerateInsights(len(issues))
		snap2 := snap1.WithPhase2(snap1.Analysis, ins, snap1.Issues, snap1.Analyzer)
		if len(snap2.TriageScores) < len(issues) || snap2.TriageScores["web-hub"] <= 0 || len(snap2.UnblocksMap["web-hub"]) != 2 {
			t.Fatalf("Path 4 (DataSnapshot.WithPhase2): len=%d web-hub score=%v unblocks=%v", len(snap2.TriageScores), snap2.TriageScores["web-hub"], snap2.UnblocksMap["web-hub"])
		}

		// Fallback negative control: synthetic TriageResult without AllScores/UnblocksMap still populates from Recommendations.
		synthScores, _, _, _, synthUnblocks := extractTriageMaps(analysis.TriageResult{
			Recommendations: []analysis.Recommendation{
				{ID: "synth-1", Score: 0.77, UnblocksIDs: []string{"synth-2"}},
			},
		})
		if synthScores["synth-1"] != 0.77 || len(synthUnblocks["synth-1"]) != 1 {
			t.Fatalf("fallback extractTriageMaps failed: scores=%v unblocks=%v", synthScores, synthUnblocks)
		}
	})

	t.Run("SeamlessWorkerPromotion_ExternalProjectDir_AndFallbackWatcher", func(t *testing.T) {
		projectDir := t.TempDir()
		extBeadsDir := t.TempDir()
		jsonlPath := filepath.Join(extBeadsDir, "issues.jsonl")
		rawLine := `{"id":"W14-1","title":"Open issue","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(rawLine), 0o644); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		// Configure custom drift config in projectDir/.bv/drift.yaml (separate from extBeadsDir)
		bvDir := filepath.Join(projectDir, ".bv")
		if err := os.MkdirAll(bvDir, 0o755); err != nil {
			t.Fatalf("mkdir .bv: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bvDir, "drift.yaml"), []byte("stale_threshold_days: 1\n"), 0o644); err != nil {
			t.Fatalf("write drift.yaml: %v", err)
		}

		initIssues := []model.Issue{{
			ID: "W14-1", Title: "Open issue", Status: model.StatusOpen, Priority: 1,
			IssueType: model.TypeTask,
			CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
			UpdatedAt: time.Now().Add(-10 * 24 * time.Hour),
		}}
		t.Setenv("BV_BACKGROUND_MODE", "0")
		m := NewModel(initIssues, nil, jsonlPath)
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.SetWorkDir(projectDir)
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

		// Force explicit restoreBackgroundAfterEdit promotion on an already-rendered TUI (sourceReload == nil)
		t.Setenv("BV_BACKGROUND_MODE", "")
		activeRec := m.recipeLoader.Get("triage")
		m.setActiveRecipe(activeRec)
		m.restoreBackgroundAfterEdit = true
		_, _ = m.Update(FileChangedMsg{})

		if m.backgroundWorker == nil {
			t.Fatalf("expected BackgroundWorker promotion")
		}
		if m.snapshotInitPending {
			t.Fatalf("expected snapshotInitPending=false when len(m.issues)>0 && m.ready so TUI never flashes Loading beads")
		}
		if view := m.View(); strings.Contains(view, "Loading beads") {
			t.Fatalf("expected rendered TUI view to stay visible instead of cold-start Loading beads screen")
		}
		if m.backgroundWorker.projectDir != projectDir {
			t.Fatalf("expected BackgroundWorker.projectDir=%q, got %q", projectDir, m.backgroundWorker.projectDir)
		}
		if m.backgroundWorker.currentRecipe != activeRec {
			t.Fatalf("expected promoted BackgroundWorker to inherit activeRecipe")
		}

		// Terminal SnapshotErrorMsg must recover live reload via fallback watcher when beadsPath is a real file!
		_, errCmd := m.Update(SnapshotErrorMsg{
			Err:              fmt.Errorf("fatal snapshot failure"),
			Recoverable:      false,
			WorkerGeneration: m.backgroundWorker.Generation(),
		})
		if m.backgroundWorker != nil {
			t.Fatalf("expected backgroundWorker cleared after unrecoverable SnapshotErrorMsg")
		}
		if m.watcher == nil || errCmd == nil {
			t.Fatalf("expected fallback watcher and WatchFileCmd after unrecoverable SnapshotErrorMsg; watcher=%v cmd=%v", m.watcher, errCmd)
		}

		// Negative control: git: beadsPath must NOT start a filesystem watcher on SnapshotErrorMsg
		mGit := NewModel(initIssues, nil, "")
		t.Cleanup(mGit.Stop)
		mGit.analysis.WaitForPhase2()
		mGit.beadsPath = "git:HEAD~1"
		_, gitCmd := mGit.Update(SnapshotErrorMsg{Err: fmt.Errorf("boom"), Recoverable: false})
		if mGit.watcher != nil || gitCmd != nil {
			t.Fatalf("expected git: beadsPath not to create fallback watcher; watcher=%v cmd=%v", mGit.watcher, gitCmd)
		}
	})

	t.Run("AsOfReferenceNowAcrossSubViewsAndSetSearchConfig", func(t *testing.T) {
		refNow := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		deferUntil := time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC) // Future at refNow, expired at wall-clock 2026!
		closedRecent := time.Date(2025, 1, 14, 10, 0, 0, 0, time.UTC)

		issues := []model.Issue{
			{
				ID: "ASOF-DEF", Title: "Deferred at refNow", Status: model.StatusOpen, Priority: 1,
				IssueType: model.TypeTask, Labels: []string{"core"},
				CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-24 * time.Hour),
				DeferUntil: &deferUntil,
			},
			{
				ID: "ASOF-RDY", Title: "Ready login auth", Status: model.StatusOpen, Priority: 2,
				IssueType: model.TypeFeature, Labels: []string{"core"},
				CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: refNow.Add(-2 * time.Hour),
			},
			{
				ID: "ASOF-DONE", Title: "Closed yesterday at refNow", Status: model.StatusClosed, Priority: 1,
				IssueType: model.TypeBug, Labels: []string{"core"},
				CreatedAt: refNow.Add(-96 * time.Hour), UpdatedAt: closedRecent, ClosedAt: &closedRecent,
			},
		}

		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})

		if !m.analysisReferenceTime().Equal(refNow) {
			t.Fatalf("analysisReferenceTime() = %v, want %v", m.analysisReferenceTime(), refNow)
		}

		// Board: ASOF-DEF must be deferred ([deferred], NOT claimable) and show relative age anchored to refNow,
		// whereas wall-clock Board treats past 2025 deferral as expired.
		boardAtRef := m.board.View(120, 30)
		if !strings.Contains(boardAtRef, "[deferred]") || !strings.Contains(boardAtRef, "1d ago") {
			t.Fatalf("expected Board at refNow to render [deferred] badge and 1d ago age for ASOF-DEF:\n%s", boardAtRef)
		}
		mWall := NewModel(copyIssues(issues), nil, "")
		t.Cleanup(mWall.Stop)
		mWall.analysis.WaitForPhase2()
		boardAtWall := mWall.board.View(120, 30)
		if strings.Contains(boardAtWall, "[deferred]") {
			t.Fatalf("negative control: expected wall-clock Board NOT to render [deferred] for past 2025 deferral")
		}

		// Actionable view: ASOF-DEF must not appear as ready actionable work at refNow
		m.refreshActionableView()
		actRendered := m.actionableView.Render()
		if strings.Contains(actRendered, "ASOF-DEF") {
			t.Fatalf("expected deferred ASOF-DEF excluded from Actionable view at refNow:\n%s", actRendered)
		}

		// Label Dashboard ('[') and Velocity Comparison ('v') use refNow (closed yesterday -> Velocity7d > 0)
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
		if len(m.labelHealthCache.Labels) == 0 || m.labelHealthCache.Labels[0].Velocity.ClosedLast7Days == 0 {
			t.Fatalf("expected LabelHealth at refNow to count ASOF-DONE in ClosedLast7Days, got %+v", m.labelHealthCache.Labels)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		velView := m.velocityComparison.View()
		if strings.Contains(velView, "No labels with closed issues") {
			t.Fatalf("expected VelocityComparison at refNow to show velocity data:\n%s", velView)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// Sprint Dashboard ('P') uses refNow (4 days remaining at refNow vs Ended on wall-clock control)
		m.sprints = []model.Sprint{{
			ID:        "sprint-hist",
			Name:      "Historical Sprint",
			StartDate: time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC),
			BeadIDs:   []string{"ASOF-RDY", "ASOF-DONE"},
		}}
		m.openSprintView()
		if !strings.Contains(m.sprintViewText, "Remaining: 4 days") {
			t.Fatalf("expected Sprint dashboard at refNow to show 'Remaining: 4 days', got:\n%s", m.sprintViewText)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		mWall.sprints = m.sprints
		mWall.openSprintView()
		if strings.Contains(mWall.sprintViewText, "Remaining: 4 days") {
			t.Fatalf("negative control: wall-clock Sprint dashboard should not show 'Remaining: 4 days':\n%s", mWall.sprintViewText)
		}
		mWall.Update(tea.KeyMsg{Type: tea.KeyEsc})

		// SetSearchConfig + Init() + 'H' auto-enabling semanticSearchEnabled
		customW := search.Weights{TextRelevance: 0.5, PageRank: 0.2, Status: 0.1, Impact: 0.1, Priority: 0.05, Recency: 0.05}
		m.SetSearchConfig(search.SearchConfig{
			Mode:       search.SearchModeHybrid,
			Preset:     search.PresetImpactFirst,
			Weights:    customW,
			HasWeights: true,
		})
		if !m.semanticHybridEnabled || !m.semanticSearchEnabled || m.semanticHybridPreset != search.PresetImpactFirst {
			t.Fatalf("SetSearchConfig(hybrid) failed: hybrid=%v semantic=%v preset=%q", m.semanticHybridEnabled, m.semanticSearchEnabled, m.semanticHybridPreset)
		}
		if !m.semanticSearch.GetReferenceTime().Equal(refNow) {
			t.Fatalf("expected semanticSearch ReferenceTime=%v, got %v", refNow, m.semanticSearch.GetReferenceTime())
		}
		if initCmd := m.Init(); initCmd == nil {
			t.Fatalf("expected Init() to schedule index+hybrid build commands")
		}
		// Negative control: SetSearchConfig(text) disables hybrid/semantic
		m.SetSearchConfig(search.SearchConfig{Mode: search.SearchModeText, Preset: search.PresetTextOnly})
		if m.semanticHybridEnabled || m.semanticSearchEnabled {
			t.Fatalf("SetSearchConfig(text) should disable hybrid & semantic, got hybrid=%v semantic=%v", m.semanticHybridEnabled, m.semanticSearchEnabled)
		}
		// Pressing 'H' in focusList auto-enables semanticSearchEnabled!
		m.focused = focusList
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
		if !m.semanticHybridEnabled || !m.semanticSearchEnabled {
			t.Fatalf("pressing 'H' should enable both semanticHybridEnabled and semanticSearchEnabled")
		}
	})

	t.Run("LabelCandidateScopingInRecipesAndMarkdownExport", func(t *testing.T) {
		now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
		issues := []model.Issue{
			{
				ID: "CTX-UNLABELED", Title: "Unlabeled Blocker Neighbor", Status: model.StatusOpen,
				Priority: 0, IssueType: model.TypeTask,
				CreatedAt: now.Add(-60 * 24 * time.Hour), UpdatedAt: now.Add(-45 * 24 * time.Hour),
			},
			{
				ID: "LAB-BLOCKED", Title: "Labeled Blocked", Status: model.StatusOpen,
				Priority: 2, IssueType: model.TypeTask, Labels: []string{"api"},
				CreatedAt: now.Add(-50 * 24 * time.Hour), UpdatedAt: now.Add(-40 * 24 * time.Hour),
				Dependencies: []*model.Dependency{{DependsOnID: "CTX-UNLABELED", Type: model.DepBlocks}},
			},
			{
				ID: "LAB-READY", Title: "Labeled Ready", Status: model.StatusOpen,
				Priority: 1, IssueType: model.TypeTask, Labels: []string{"api"},
				CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
			},
		}
		cands := map[string]bool{"LAB-BLOCKED": true, "LAB-READY": true}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{
			Authority:    model.NewReadinessIndex(issues),
			CandidateIDs: cands,
			ReferenceNow: now,
		})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()

		for _, rName := range []string{"default", "high-impact", "blocked", "stale"} {
			r := m.recipeLoader.Get(rName)
			if r == nil {
				continue
			}
			got, err := applyRecipeToIssues(m.issues, m.analyzer, m.analysis, m.triageScores, r, now)
			if err != nil {
				t.Fatalf("recipe %q error: %v", rName, err)
			}
			for _, iss := range got {
				if iss.ID == "CTX-UNLABELED" {
					t.Fatalf("recipe %q leaked unlabeled 1-hop context neighbor CTX-UNLABELED!", rName)
				}
			}
		}

		// Export under --label (m.candidateIDs != nil, no active recipe/filter) must exclude CTX-UNLABELED
		exported := m.exportIssuesForCurrentView()
		for _, iss := range exported {
			if iss.ID == "CTX-UNLABELED" {
				t.Fatalf("exportIssuesForCurrentView() leaked unlabeled neighbor CTX-UNLABELED")
			}
		}
		if len(exported) != 2 {
			t.Fatalf("expected 2 candidate issues exported under --label, got %d", len(exported))
		}

		// Export under non-default sort mode ('s') must preserve user's active sort order!
		mUnscoped := NewModel([]model.Issue{
			{ID: "B-OLD", Title: "Old High Prio", Status: model.StatusOpen, Priority: 0, CreatedAt: now.Add(-10 * time.Hour), UpdatedAt: now},
			{ID: "A-NEW", Title: "New Low Prio", Status: model.StatusOpen, Priority: 3, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now},
		}, nil, "", ReadinessScope{ReferenceNow: now})
		t.Cleanup(mUnscoped.Stop)
		mUnscoped.analysis.WaitForPhase2()
		mUnscoped.sortMode = SortCreatedAsc
		mUnscoped.applyFilter()
		sortedExport := mUnscoped.exportIssuesForCurrentView()
		if len(sortedExport) != 2 || sortedExport[0].ID != "B-OLD" {
			t.Fatalf("expected SortCreatedAsc export order [B-OLD, A-NEW], got [%s, %s]", sortedExport[0].ID, sortedExport[1].ID)
		}
		mUnscoped.sortMode = SortCreatedDesc
		mUnscoped.applyFilter()
		sortedDescExport := mUnscoped.exportIssuesForCurrentView()
		if len(sortedDescExport) != 2 || sortedDescExport[0].ID != "A-NEW" {
			t.Fatalf("expected SortCreatedDesc export order [A-NEW, B-OLD], got [%s, %s]", sortedDescExport[0].ID, sortedDescExport[1].ID)
		}
	})

	t.Run("History_MouseZOrder_SidebarClickGuard_ModalResize_DupV8_RecipeFooterTheme", func(t *testing.T) {
		now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
		var issues []model.Issue
		for i := 1; i <= 15; i++ {
			issues = append(issues, model.Issue{
				ID: fmt.Sprintf("UX-%02d", i), Title: fmt.Sprintf("UX Issue %02d", i),
				Status: model.StatusOpen, Priority: (i % 3) + 1, IssueType: model.TypeTask,
				Labels: []string{"ux"}, CreatedAt: now.Add(-time.Duration(i) * time.Hour), UpdatedAt: now,
			})
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: now})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

		// 1) History: preserve Tab-selected searchMode across '/', clear filters on first Esc, page with pgdown/pgup
		histMap := make(map[string]correlation.BeadHistory, 15)
		var commits []correlation.CorrelatedCommit
		for i := 1; i <= 15; i++ {
			id := fmt.Sprintf("UX-%02d", i)
			sha := fmt.Sprintf("%040d", i)
			cc := correlation.CorrelatedCommit{
				BeadID: id, SHA: sha, ShortSHA: sha[:7],
				Message: fmt.Sprintf("commit %d", i), Author: "alice", Timestamp: now.Add(-time.Duration(i) * time.Minute),
				Confidence: 0.9,
			}
			histMap[id] = correlation.BeadHistory{BeadID: id, Title: id, Status: "open", Commits: []correlation.CorrelatedCommit{cc}}
			commits = append(commits, cc)
		}
		report := &correlation.HistoryReport{Histories: histMap, CommitIndex: make(correlation.CommitIndex)}
		for _, cc := range commits {
			report.CommitIndex[cc.SHA] = []string{cc.BeadID}
		}
		m.historyView = NewHistoryModel(report, m.theme)
		m.historyView.SetSize(120, 28)
		m.historyLoading = false
		m.historyLoadFailed = false
		m.historyReportDataGeneration = m.semanticDataGeneration
		m.isHistoryView = true
		m.focused = focusHistory

		// Start search ('/'), cycle mode with Tab twice (-> Author), Enter to finish, '/' again -> must preserve Author!
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		wantMode := m.historyView.GetSearchModeName()
		if wantMode == "All" {
			t.Fatalf("expected Tab to advance searchMode from All, got %q", wantMode)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
		if got := m.historyView.GetSearchModeName(); got != wantMode {
			t.Fatalf("StartSearch() reset searchMode: got %q, want %q", got, wantMode)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm active query "a"

		// First Esc in History clears active filter without exiting History view; second Esc exits History view!
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if !m.isHistoryView || m.historyView.SearchQuery() != "" || m.historyView.HasActiveFilters() {
			t.Fatalf("first Esc should clear History filters and stay in History view: isHistory=%v query=%q", m.isHistoryView, m.historyView.SearchQuery())
		}
		// PageDown / PageUp in focusHistory even when shortcuts sidebar is open!
		m.showShortcutsSidebar = true
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if m.historyView.selectedBead == 0 {
			t.Fatalf("expected PgDown in focusHistory to advance selection even with shortcuts sidebar open")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		m.showShortcutsSidebar = false
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.isHistoryView || m.focused != focusList {
			t.Fatalf("second Esc should exit History view to focusList; isHistory=%v focused=%v", m.isHistoryView, m.focused)
		}

		// Board PgDown / PgUp
		m.isBoardView = true
		m.focused = focusBoard
		m.board.SetIssues(m.issues)
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if m.board.selectedRow[ColOpen] == 0 {
			t.Fatalf("expected PgDown in focusBoard to advance Open column selection")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		m.isBoardView = false
		m.focused = focusList

		// 2) Mouse wheel Z-order + Shortcuts Sidebar / Footer click guard
		m.alerts = []drift.Alert{
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "UX-01", Message: "a1"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "UX-02", Message: "a2"},
		}
		m.showAlertsPanel = true
		m.alertsCursor = 0
		listBefore := m.list.Index()
		m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		if m.alertsCursor != 1 || m.list.Index() != listBefore {
			t.Fatalf("wheel over showAlertsPanel: alertsCursor=%d (want 1), listIndex=%d (want %d)", m.alertsCursor, m.list.Index(), listBefore)
		}
		m.showAlertsPanel = false

		// CassSessionModal wheel navigation
		m.cassModal = NewCassSessionModal("UX-01", cass.CorrelationResult{
			TopSessions: []cass.ScoredResult{
				{SearchResult: cass.SearchResult{Agent: "claude"}},
				{SearchResult: cass.SearchResult{Agent: "codex"}},
			},
		}, m.theme)
		m.showCassModal = true
		m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		if m.cassModal.selected != 1 {
			t.Fatalf("expected CassSessionModal wheel down to select index 1, got %d", m.cassModal.selected)
		}
		m.showCassModal = false

		// Sidebar & footer left-click guard vs valid list row click
		m.showShortcutsSidebar = true
		m.applyContentSizing()
		m.selectListIndex(0)
		sidebarX := m.mainContentWidth() + 2
		m.Update(tea.MouseMsg{X: sidebarX, Y: m.listChromeLines() + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if m.list.Index() != 0 || m.focused != focusList {
			t.Fatalf("click inside shortcuts sidebar mutated state: index=%d focused=%v", m.list.Index(), m.focused)
		}
		m.Update(tea.MouseMsg{X: 5, Y: m.height - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if m.list.Index() != 0 {
			t.Fatalf("click on footer row mutated list index: %d", m.list.Index())
		}
		// Positive control: clicking row 2 inside list panel selects index 2
		m.Update(tea.MouseMsg{X: 5, Y: m.listChromeLines() + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if m.list.Index() != 2 {
			t.Fatalf("positive control: click inside list panel row 2 got index=%d, want 2", m.list.Index())
		}
		m.showShortcutsSidebar = false

		// 3) Duplicate v8 blurb consolidation copy + narrow modal resize (<64 cols)
		agentDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		dupContent := "# Repo\n\n" + agents.AgentBlurb + "\n\n" + agents.AgentBlurb + "\n"
		agentFile := filepath.Join(agentDir, "AGENTS.md")
		if err := os.WriteFile(agentFile, []byte(dupContent), 0o644); err != nil {
			t.Fatalf("write duplicate AGENTS.md: %v", err)
		}
		_ = agents.RecordAccept(agentDir) // Even after accepting v8 previously, duplicate v8 blurbs must prompt to consolidate!
		checkMsg, ok := CheckAgentFileCmd(agentDir)().(AgentFileCheckMsg)
		if !ok || !checkMsg.ShouldPrompt || !checkMsg.IsUpgrade || checkMsg.ExistingVersion != agents.BlurbVersion {
			t.Fatalf("expected CheckAgentFileCmd on duplicate v8 blurbs to prompt upgrade/consolidation, got %+v", checkMsg)
		}
		m.workDir = agentDir
		m.Update(checkMsg)
		m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
		modalView := m.agentPromptModal.View()
		if !strings.Contains(modalView, "duplicate") || strings.Contains(modalView, "outdated") {
			t.Fatalf("expected duplicate-v8 consolidation copy without claiming outdated:\n%s", modalView)
		}
		for _, line := range strings.Split(modalView, "\n") {
			if ansi.StringWidth(line) > 48 {
				t.Fatalf("narrow 48-col AgentPromptModal line overflowed (%d > 48): %q", ansi.StringWidth(line), line)
			}
		}
		// Accept consolidates duplicate v8 blurbs down to 1
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		afterBytes, err := os.ReadFile(agentFile)
		if err != nil {
			t.Fatalf("read consolidated AGENTS.md: %v", err)
		}
		if agents.NeedsUpdate(string(afterBytes)) {
			t.Fatalf("expected consolidated AGENTS.md to pass !NeedsUpdate()")
		}
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})

		// 4) Recipe normalized GroupBy + canonical Blockers + configured-row badges + footer count excluding IssueGroupItem headers + ◌ metrics… + clearAllFilters repo scope
		customRec := &recipe.Recipe{
			Name: "grouped-custom",
			View: recipe.ViewConfig{
				GroupBy: " Status ",
				Columns: []string{"id", "title", " Blockers "},
			},
		}
		m.statusMsg = ""
		m.setActiveRecipe(customRec)
		m.applyRecipe(customRec)
		if !m.recipeGroupingActive() {
			t.Fatalf("expected recipeGroupingActive() true for GroupBy=' Status '")
		}
		if m.recipeBlockerCounts == nil {
			t.Fatalf("expected canonical column ' Blockers ' to populate recipeBlockerCounts")
		}
		if gotCount := m.displayedIssueCount(); gotCount != len(issues) {
			t.Fatalf("displayedIssueCount() = %d, want %d (must exclude IssueGroupItem headers)", gotCount, len(issues))
		}
		footer := m.renderFooter()
		if !strings.Contains(footer, fmt.Sprintf("%d issues", len(issues))) {
			t.Fatalf("expected footer count badge '%d issues', got:\n%s", len(issues), footer)
		}
		// Phase 2 progress badge when m.snapshot == nil && !m.analysis.IsPhase2Ready()
		savedAnalysis := m.analysis
		m.analysis = &analysis.GraphStats{}
		if p2Footer := m.renderFooter(); !strings.Contains(p2Footer, "◌ metrics…") {
			t.Fatalf("expected '◌ metrics…' in footer when m.snapshot == nil && !m.analysis.IsPhase2Ready(), got:\n%s", p2Footer)
		}
		m.analysis = savedAnalysis

		// delegate renderConfiguredRow preserves search/priority/diff/triage badges
		del := IssueDelegate{
			Theme:             m.theme,
			ShowPriorityHints: true,
			PriorityHints:     map[string]*analysis.PriorityRecommendation{"UX-01": {IssueID: "UX-01", Direction: "increase"}},
			ShowSearchScores:  true,
			WorkspaceMode:     true,
			Columns:           []string{"id", "title", "blockers"},
			BlockerCounts:     map[string]int{"UX-01": 3},
		}
		var rowBuf strings.Builder
		del.renderConfiguredRow(&rowBuf, m.list, 0, IssueItem{
			Issue:          issues[0],
			RepoPrefix:     "ux",
			DiffStatus:     DiffStatusNew,
			SearchScore:    0.88,
			SearchScoreSet: true,
			UnblocksCount:  2,
			IsBlocker:      true,
		})
		rowOut := rowBuf.String()
		for _, token := range []string{"0.88", "↑", "🔓2", DiffStatusNew.Badge()} {
			if !strings.Contains(rowOut, token) {
				t.Fatalf("expected renderConfiguredRow to preserve badge %q, got: %q", token, rowOut)
			}
		}

		// clearAllFilters clears workspace activeRepos too
		m.workspaceMode = true
		m.activeRepos = map[string]bool{"ux": true}
		if !m.hasActiveFilters() {
			t.Fatalf("expected hasActiveFilters() true when workspace activeRepos != nil")
		}
		m.clearAllFilters()
		if m.activeRepos != nil || m.activeRecipe != nil {
			t.Fatalf("expected clearAllFilters() to clear activeRepos and activeRecipe")
		}

		// 5) Help Graph View text, Tree/Sprint footer hints, LabelDashboard single-line truncation, and Light-theme AdaptiveColor
		m.showHelp = true
		helpBody, _ := m.buildHelpBodyLines()
		joinedHelp := strings.Join(helpBody, "\n")
		if !strings.Contains(joinedHelp, "J/K") || !strings.Contains(joinedHelp, "Page nodes") {
			t.Fatalf("expected ? Help Graph View section to list J/K Scroll up/down and PgUp/Dn Page nodes:\n%s", joinedHelp)
		}
		helpFooter := m.renderFooter()
		if !strings.Contains(helpFooter, "tutorial") || strings.Contains(helpFooter, "Press any key") {
			t.Fatalf("expected accurate showHelp footer hints, got: %s", helpFooter)
		}
		m.showHelp = false

		m.focused = focusTree
		if treeFooter := m.renderFooter(); !strings.Contains(treeFooter, "expand") {
			t.Fatalf("expected focusTree footer hint, got: %s", treeFooter)
		}
		m.focused = focusSprint
		m.isSprintView = true
		if sprintFooter := m.renderFooter(); !strings.Contains(sprintFooter, "P/esc") {
			t.Fatalf("expected focusSprint footer hint, got: %s", sprintFooter)
		}
		m.isSprintView = false
		m.focused = focusList

		// LabelDashboard single-line cell truncation with newline in label name
		ld := NewLabelDashboardModel(m.theme)
		ld.SetSize(70, 20)
		ld.SetData([]analysis.LabelHealth{{
			Label:       "super-long-multiline\nlabel-name-that-must-not-wrap",
			HealthLevel: analysis.HealthLevelHealthy,
			Health:      95,
		}})
		for _, ln := range strings.Split(ld.View(), "\n") {
			if ansi.StringWidth(ln) > 70 {
				t.Fatalf("LabelDashboard row exceeded width 70 (%d): %q", ansi.StringWidth(ln), ln)
			}
		}

		// Light-theme contrast: TriageStar, TriageUnblocks, PriorityDownArrow use adaptive Light/Dark colors
		for name, fg := range map[string]lipgloss.TerminalColor{
			"TriageStar":        m.theme.TriageStar.GetForeground(),
			"TriageUnblocks":    m.theme.TriageUnblocks.GetForeground(),
			"PriorityDownArrow": m.theme.PriorityDownArrow.GetForeground(),
		} {
			switch ac := fg.(type) {
			case lipgloss.CompleteAdaptiveColor:
				if ac.Light.TrueColor == "" || ac.Dark.TrueColor == "" || ac.Light.TrueColor == ac.Dark.TrueColor {
					t.Fatalf("%s expected distinct Light/Dark CompleteAdaptiveColor, got %#v", name, ac)
				}
			case lipgloss.AdaptiveColor:
				if ac.Light == "" || ac.Dark == "" || ac.Light == ac.Dark {
					t.Fatalf("%s expected distinct Light/Dark AdaptiveColor, got %#v", name, ac)
				}
			default:
				t.Fatalf("%s expected adaptive Light/Dark color, got %#v", name, fg)
			}
		}
	})

	t.Run("Wave15_1_AuditRemediations_B2_Through_B6", func(t *testing.T) {
		// [B-2] Unstarted BackgroundWorker.SetRecipe must NOT spawn background goroutines,
		// and SetProjectDir must invalidate loadMetadataUnchanged dedup!
		dirA := t.TempDir()
		dirB := t.TempDir()
		extBeads := t.TempDir()
		jsonlPath := filepath.Join(extBeads, "issues.jsonl")
		oldLine := `{"id":"B2-1","title":"Stale candidate","status":"open","priority":1,"issue_type":"task","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(oldLine), 0o644); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(dirB, ".bv"), 0o755); err != nil {
			t.Fatalf("mkdir .bv: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dirB, ".bv", "drift.yaml"), []byte("stale_threshold_days: 99999\n"), 0o644); err != nil {
			t.Fatalf("write drift.yaml: %v", err)
		}

		bw, err := NewBackgroundWorker(WorkerConfig{
			BeadsPath:     jsonlPath,
			ProjectDir:    dirA,
			DebounceDelay: 50 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewBackgroundWorker: %v", err)
		}
		recDefault := &recipe.Recipe{Name: "default"}
		bw.SetRecipe(recDefault)
		bw.mu.RLock()
		scheduledBeforeStart := bw.processScheduled
		forcedBeforeStart := bw.forceNext
		bw.mu.RUnlock()
		if scheduledBeforeStart || !forcedBeforeStart {
			bw.Stop()
			t.Fatalf("[B-2] unstarted SetRecipe spawned goroutine or missed forceNext: scheduled=%v forceNext=%v", scheduledBeforeStart, forcedBeforeStart)
		}
		resA := bw.buildSnapshotResultContext(t.Context(), false)
		if resA.err != nil || resA.snapshot == nil {
			bw.Stop()
			t.Fatalf("[B-2] buildSnapshotResultContext dirA failed: %v", resA.err)
		}
		if resA.snapshot.Analysis != nil {
			resA.snapshot.Analysis.WaitForPhase2()
		}
		bw.mu.Lock()
		bw.snapshot = resA.snapshot
		bw.lastHash = resA.snapshot.DataHash
		bw.projectDir = dirB // simulate projectDir change even if forceNext were false
		bw.mu.Unlock()
		resB := bw.buildSnapshotResultContext(t.Context(), false)
		if resB.err != nil || resB.snapshot == nil {
			bw.Stop()
			t.Fatalf("[B-2] expected loadMetadataUnchanged=false when projectDir changed from dirA to dirB, got snapshot=%v err=%v", resB.snapshot, resB.err)
		}
		if resB.snapshot.Analysis != nil {
			resB.snapshot.Analysis.WaitForPhase2()
		}
		bw.Stop()

		// [B-3] BuildHybridMetricsAtCmd / startSemanticHybridBuild honors ReferenceNow for DeferUntil
		refNow := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		deferFutureAtRef := refNow.Add(48 * time.Hour) // 2025-01-17: deferred at refNow, expired at wall-clock 2026!
		hybIssues := []model.Issue{{
			ID: "HYB-DEF", Title: "Deferred OAuth", Status: model.StatusOpen, Priority: 1,
			IssueType: model.TypeTask, CreatedAt: refNow.Add(-24 * time.Hour), UpdatedAt: refNow.Add(-2 * time.Hour),
			DeferUntil: &deferFutureAtRef,
			Comments:   []*model.Comment{{Author: "alice", Text: "check", CreatedAt: refNow.Add(-2 * time.Hour)}},
		}}
		mHyb := NewModel(copyIssues(hybIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mHyb.Stop)
		mHyb.analysis.WaitForPhase2()

		hybCmd := mHyb.startSemanticHybridBuild()
		if hybCmd == nil {
			t.Fatalf("[B-3] expected non-nil hybrid build cmd")
		}
		hybMsg, ok := hybCmd().(HybridMetricsReadyMsg)
		if !ok || hybMsg.Error != nil || hybMsg.Cache == nil {
			t.Fatalf("[B-3] HybridMetricsReadyMsg error: %+v", hybMsg)
		}
		mAtRef, foundRef := hybMsg.Cache.Get("HYB-DEF")
		if !foundRef || mAtRef.Status != "deferred" {
			t.Fatalf("[B-3] expected HYB-DEF metric Status='deferred' at refNow, got found=%v status=%q", foundRef, mAtRef.Status)
		}
		statusOnlyW := search.Weights{Status: 1.0}
		scoreAtRef, err := search.NewHybridScorerAt(statusOnlyW, hybMsg.Cache, refNow).Score("HYB-DEF", 0)
		if err != nil || scoreAtRef.ComponentScores["status"] != 0.2 {
			t.Fatalf("[B-3] expected deferred status score 0.2 at refNow, got %+v (err=%v)", scoreAtRef, err)
		}
		// Negative control: wall-clock BuildHybridMetricsCmd treats 2025-01-17 deferral as expired ("open" -> 1.0)
		t.Setenv("SOURCE_DATE_EPOCH", "")
		wallMsg := BuildHybridMetricsCmd(copyIssues(hybIssues), 1, 1)().(HybridMetricsReadyMsg)
		mAtWall, _ := wallMsg.Cache.Get("HYB-DEF")
		if mAtWall.Status != "open" {
			t.Fatalf("[B-3] negative control: expected wall-clock status='open', got %q", mAtWall.Status)
		}

		// [B-5] + [R2-B1] SetSearchConfig custom weights via real ParseSearchConfig (which sets Preset="default")
		// persist across 'H' toggles, display 'custom', and cycle to 'default' first on 'alt+h'!
		parsedCustomCfg, parseErr := search.ParseSearchConfig(
			string(search.SearchModeHybrid),
			"",
			`{"text":0.1,"pagerank":0.9,"status":0,"impact":0,"priority":0,"recency":0}`,
		)
		if parseErr != nil {
			t.Fatalf("[R2-B1] ParseSearchConfig failed: %v", parseErr)
		}
		customW := parsedCustomCfg.Weights
		mHyb.SetSearchConfig(parsedCustomCfg)
		mHyb.Update(hybMsg) // mark hybrid metrics ready
		if mHyb.semanticHybridPreset != "custom" || mHyb.semanticHybridLabel() != "custom" {
			t.Fatalf("[B-5/R2-B1] expected preset/label 'custom', got preset=%q label=%q", mHyb.semanticHybridPreset, mHyb.semanticHybridLabel())
		}
		if !strings.Contains(mHyb.statusMsg, "(custom)") {
			t.Fatalf("[B-5/R2-B1] expected ready statusMsg to display '(custom)', got %q", mHyb.statusMsg)
		}
		// Toggle 'H' off and back on: custom weights must survive!
		mHyb.focused = focusList
		mHyb.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
		mHyb.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
		cfgAfterToggle := mHyb.semanticSearch.getHybridConfig()
		if !cfgAfterToggle.CustomWeights || cfgAfterToggle.Weights != customW.Normalize() {
			t.Fatalf("[B-5] custom weights clobbered by 'H' toggle: %+v", cfgAfterToggle)
		}
		if !strings.Contains(mHyb.statusMsg, "(custom)") {
			t.Fatalf("[B-5] expected statusMsg after 'H' re-enable to say '(custom)', got %q", mHyb.statusMsg)
		}
		// Pressing 'alt+h' explicitly cycles to 'default' first (never skipping 'default') and clears custom weights!
		mHyb.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
		if mHyb.semanticHybridHasCustomWeights || mHyb.semanticSearch.getHybridConfig().CustomWeights {
			t.Fatalf("[B-5] expected alt+h to clear custom weights")
		}
		if mHyb.semanticHybridPreset != search.PresetDefault {
			t.Fatalf("[R2-B1] expected alt+h from 'custom' to land on 'default', got %q", mHyb.semanticHybridPreset)
		}

		// [B-4] SnapshotReadyMsg with active non-actionable recipe ('default') under CandidateIDs must NOT leak unlabeled neighbors
		candIssues := []model.Issue{
			{ID: "CTX-0", Title: "Unlabeled Blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{
				ID: "CAND-1", Title: "Labeled Candidate", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				Labels: []string{"api"}, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{DependsOnID: "CTX-0", Type: model.DepBlocks}},
			},
		}
		defRec := mHyb.recipeLoader.Get("default")
		snapUnscoped := NewSnapshotBuilder(copyIssues(candIssues)).WithRecipe(defRec).Build()
		snapUnscoped.Analysis.WaitForPhase2()
		mCand := NewModel(copyIssues(candIssues), defRec, "", ReadinessScope{
			Authority:    model.NewReadinessIndex(candIssues),
			CandidateIDs: map[string]bool{"CAND-1": true},
			ReferenceNow: refNow,
		})
		t.Cleanup(mCand.Stop)
		mCand.analysis.WaitForPhase2()
		mCand.Update(SnapshotReadyMsg{Snapshot: snapUnscoped})
		for _, raw := range mCand.list.Items() {
			if it, ok := raw.(IssueItem); ok && it.Issue.ID == "CTX-0" {
				t.Fatalf("[B-4] SnapshotReadyMsg leaked non-candidate neighbor CTX-0 into m.list!")
			}
		}
		if gotBoardTotal := mCand.board.TotalCount(); gotBoardTotal != 1 {
			t.Fatalf("[B-4] expected exactly 1 candidate card on m.board, got %d", gotBoardTotal)
		}
		for colIdx, colIssues := range mCand.board.columns {
			for _, iss := range colIssues {
				if iss.ID == "CTX-0" {
					t.Fatalf("[B-4] SnapshotReadyMsg leaked non-candidate neighbor CTX-0 as a card into m.board column %d!", colIdx)
				}
			}
		}

		// [B-6] Delegate/Comment/History/Export ReferenceNow, FileChangedMsg closed-issue triage guard, <52-col modals, loading click guard
		var delBuf strings.Builder
		mHyb.updateListDelegate()
		delRef := IssueDelegate{Theme: mHyb.theme, ReferenceNow: refNow}
		delRef.Render(&delBuf, mHyb.list, 0, IssueItem{Issue: hybIssues[0]})
		if !strings.Contains(delBuf.String(), "1d ago") {
			t.Fatalf("[B-6] expected IssueDelegate with ReferenceNow to render '1d ago', got: %q", delBuf.String())
		}
		mHyb.isSplitView = true
		mHyb.Update(tea.WindowSizeMsg{Width: 120, Height: 45})
		mHyb.updateViewportContent()
		if strippedVP := ansi.Strip(mHyb.viewport.View()); !strings.Contains(strippedVP, "2h ago") {
			t.Fatalf("[B-6] expected comment timestamp in detail viewport to render '2h ago' at refNow, got:\n%s", strippedVP)
		}
		if expName := mHyb.generateExportFilename(); !strings.Contains(expName, "2025-01-15") {
			t.Fatalf("[B-6] expected generateExportFilename() to use refNow date 2025-01-15, got %q", expName)
		}

		// HistoryModel SetReferenceTime
		ccHist := correlation.CorrelatedCommit{
			BeadID: "HYB-DEF", SHA: strings.Repeat("a", 40), ShortSHA: "aaaaaaa",
			Message: "fix auth", Author: "Bob", Timestamp: refNow.Add(-3 * time.Hour), Confidence: 0.9,
		}
		hRep := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"HYB-DEF": {
					BeadID: "HYB-DEF", Title: "Deferred OAuth", Status: "open",
					Commits: []correlation.CorrelatedCommit{ccHist},
					Events:  []correlation.BeadEvent{{BeadID: "HYB-DEF", EventType: correlation.EventCreated, Timestamp: refNow.Add(-3 * time.Hour), Author: "Bob"}},
				},
			},
			CommitIndex: correlation.CommitIndex{ccHist.SHA: []string{"HYB-DEF"}},
		}
		mHyb.historyView.SetReport(hRep)
		mHyb.historyView.SetSize(120, 28)
		histCommitLines := strings.Join(mHyb.historyView.renderCommitDetail(ccHist, 60, true), "\n")
		if !strings.Contains(histCommitLines, "3h ago") {
			t.Fatalf("[B-6] expected HistoryModel commit detail to render '3h ago' at refNow, got:\n%s", histCommitLines)
		}

		// FileChangedMsg must NOT copy stale open-issue triage badges onto newly closed issues during Phase 1!
		reloadDir := t.TempDir()
		reloadJSONL := filepath.Join(reloadDir, "issues.jsonl")
		closedLine := `{"id":"CLS-1","title":"Just closed","status":"closed","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}` + "\n"
		if err := os.WriteFile(reloadJSONL, []byte(closedLine), 0o644); err != nil {
			t.Fatalf("write reloadJSONL: %v", err)
		}
		t.Setenv("BV_BACKGROUND_MODE", "0")
		mReload := NewModel([]model.Issue{{
			ID: "CLS-1", Title: "Previously open", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			CreatedAt: refNow, UpdatedAt: refNow,
		}}, nil, reloadJSONL)
		t.Cleanup(mReload.Stop)
		mReload.analysis.WaitForPhase2()
		mReload.triageScores["CLS-1"] = 0.95
		mReload.quickWinSet["CLS-1"] = true
		mReload.blockerSet["CLS-1"] = true
		mReload.unblocksMap["CLS-1"] = []string{"OTHER-1"}
		_, _ = mReload.Update(FileChangedMsg{})
		mReload.analysis.WaitForPhase2()
		for _, raw := range mReload.list.Items() {
			if it, ok := raw.(IssueItem); ok && it.Issue.ID == "CLS-1" {
				if it.TriageScore != 0 || it.IsQuickWin || it.IsBlocker || it.UnblocksCount != 0 {
					t.Fatalf("[B-6] newly closed issue CLS-1 retained stale Phase-1 triage badges: %+v", it)
				}
			}
		}

		// Narrow <52-col modal clamp on CassSessionModal & UpdateModal
		cm := NewCassSessionModal("HYB-DEF", cass.CorrelationResult{}, mHyb.theme)
		cm.SetSize(48, 22)
		if cm.effectiveWidth() > 44 {
			t.Fatalf("[B-6] expected CassSessionModal effectiveWidth <= 44 at 48 cols, got %d", cm.effectiveWidth())
		}
		for _, line := range strings.Split(cm.CenterModal(48, 22), "\n") {
			if w := ansi.StringWidth(line); w > 48 {
				t.Fatalf("[B-6] CassSessionModal overflowed 48 cols (width=%d): %q", w, line)
			}
		}
		um := NewUpdateModal("v1.2.3", "", mHyb.theme)
		um.SetSize(48, 22)
		if um.effectiveWidth() > 44 {
			t.Fatalf("[B-6] expected UpdateModal effectiveWidth <= 44 at 48 cols, got %d", um.effectiveWidth())
		}
		for _, line := range strings.Split(um.CenterModal(48, 22), "\n") {
			if w := ansi.StringWidth(line); w > 48 {
				t.Fatalf("[B-6] UpdateModal overflowed 48 cols (width=%d): %q", w, line)
			}
		}

		// Loading screen click guard (snapshotInitPending && snapshot == nil)
		mLoadClick := NewModel([]model.Issue{
			{ID: "L-1", Title: "One", Status: model.StatusOpen, Priority: 1, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "L-2", Title: "Two", Status: model.StatusOpen, Priority: 2, CreatedAt: refNow, UpdatedAt: refNow},
		}, nil, "")
		t.Cleanup(mLoadClick.Stop)
		mLoadClick.analysis.WaitForPhase2()
		mLoadClick.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		mLoadClick.selectListIndex(0)
		mLoadClick.snapshotInitPending = true
		mLoadClick.snapshot = nil
		mLoadClick.Update(tea.MouseMsg{X: 5, Y: mLoadClick.listChromeLines() + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if mLoadClick.list.Index() != 0 {
			t.Fatalf("[B-6] click during cold-start loading screen mutated list index to %d", mLoadClick.list.Index())
		}
	})
}

func TestTimeTravelCustomBeadsDirMultiRepoWorkspaceAndCandidateScope(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitExec := func(t *testing.T, dir string, extraEnv []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(loader.SanitizedGitEnv(), extraEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
	}
	initGitRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		gitExec(t, dir, nil, "init")
		gitExec(t, dir, nil, "config", "user.email", "test@example.com")
		gitExec(t, dir, nil, "config", "user.name", "Test User")
		return dir
	}

	// 1. Custom beadsDir (<repo>/custom-beads/issues.jsonl) + candidateIDs + CommitTime/ReferenceNow.
	repoDir := initGitRepo(t)
	customBeadsDir := filepath.Join(repoDir, "custom-beads")
	if err := os.MkdirAll(customBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customBeadsPath := filepath.Join(customBeadsDir, "issues.jsonl")

	// Commit 1 at 2025-01-10: C-1 (open, deferred until 2025-03-01 -> NOT ready at commit 1!), EXT-1 (non-candidate open).
	commit1Raw := strings.Join([]string{
		`{"id":"C-1","title":"Cand one","status":"open","priority":1,"issue_type":"task","defer_until":"2025-03-01T00:00:00Z"}`,
		`{"id":"EXT-1","title":"Non-cand old","status":"open","priority":2,"issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(customBeadsPath, []byte(commit1Raw), 0o644); err != nil {
		t.Fatal(err)
	}
	commit1Date := "2025-01-10T09:00:00Z"
	gitExec(t, repoDir, nil, "add", "custom-beads/issues.jsonl")
	gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=" + commit1Date, "GIT_COMMITTER_DATE=" + commit1Date}, "commit", "-m", "commit 1")

	// Commit 2 at 2025-04-01: C-1 closed, C-2 added (open), EXT-1 closed, EXT-2 added (non-candidate).
	commit2Raw := strings.Join([]string{
		`{"id":"C-1","title":"Cand one","status":"closed","priority":1,"issue_type":"task"}`,
		`{"id":"C-2","title":"Cand two","status":"open","priority":1,"issue_type":"task"}`,
		`{"id":"EXT-1","title":"Non-cand old","status":"closed","priority":2,"issue_type":"task"}`,
		`{"id":"EXT-2","title":"Non-cand new","status":"open","priority":2,"issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(customBeadsPath, []byte(commit2Raw), 0o644); err != nil {
		t.Fatal(err)
	}
	commit2Date := "2025-04-01T09:00:00Z"
	gitExec(t, repoDir, nil, "add", "custom-beads/issues.jsonl")
	gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=" + commit2Date, "GIT_COMMITTER_DATE=" + commit2Date}, "commit", "-m", "commit 2")

	loaded2, err := loader.LoadIssuesFromFile(customBeadsPath)
	if err != nil {
		t.Fatal(err)
	}
	refNow := time.Date(2025, 4, 15, 12, 0, 0, 0, time.UTC)
	cands := map[string]bool{"C-1": true, "C-2": true}
	mCustom := NewModel(copyIssues(loaded2), nil, customBeadsPath, ReadinessScope{
		Authority:    model.NewReadinessIndex(loaded2),
		CandidateIDs: cands,
		ReferenceNow: refNow,
	})
	t.Cleanup(mCustom.Stop)
	mCustom.SetWorkDir(repoDir)

	mCustom.enterTimeTravelMode("HEAD~1")
	if !mCustom.IsTimeTravelMode() || mCustom.statusIsError {
		t.Fatalf("enterTimeTravelMode(custom-beads) failed: mode=%v err=%v status=%q",
			mCustom.IsTimeTravelMode(), mCustom.statusIsError, mCustom.statusMsg)
	}
	diff := mCustom.TimeTravelDiff()
	if diff == nil || diff.Summary.IssuesAdded != 1 || diff.Summary.IssuesClosed != 1 {
		t.Fatalf("expected candidate-scoped diff (+1 added C-2, +1 closed C-1), got %+v", diff)
	}
	if !mCustom.newIssueIDs["C-2"] || mCustom.newIssueIDs["EXT-2"] || !mCustom.closedIssueIDs["C-1"] || mCustom.closedIssueIDs["EXT-1"] {
		t.Fatalf("candidate filtering leaked non-candidates: new=%v closed=%v", mCustom.newIssueIDs, mCustom.closedIssueIDs)
	}
	wantCommit1Time := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	if !diff.FromTimestamp.Equal(wantCommit1Time) || !diff.ToTimestamp.Equal(refNow) {
		t.Fatalf("expected FromTimestamp=%v (CommitTime) and ToTimestamp=%v (ReferenceNow), got from=%v to=%v",
			wantCommit1Time, refNow, diff.FromTimestamp, diff.ToTimestamp)
	}

	// Planted negative control: unscoped model on the same customBeadsPath sees all 2 added and 2 closed issues.
	mUnscoped := NewModel(copyIssues(loaded2), nil, customBeadsPath, ReadinessScope{ReferenceNow: refNow})
	t.Cleanup(mUnscoped.Stop)
	mUnscoped.SetWorkDir(repoDir)
	mUnscoped.enterTimeTravelMode("HEAD~1")
	if uDiff := mUnscoped.TimeTravelDiff(); uDiff == nil || uDiff.Summary.IssuesAdded != 2 || uDiff.Summary.IssuesClosed != 2 {
		t.Fatalf("negative control failed: unscoped custom-beads diff = %+v, want +2 added / +2 closed", uDiff)
	}

	// 2. Multi-repo workspace time-travel with activeRepos filtering.
	repoAPI := initGitRepo(t)
	apiBeads := filepath.Join(repoAPI, ".beads")
	_ = os.MkdirAll(apiBeads, 0o755)
	_ = os.WriteFile(filepath.Join(apiBeads, "issues.jsonl"), []byte(`{"id":"1","title":"API base","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644)
	gitExec(t, repoAPI, nil, "add", ".beads/issues.jsonl")
	gitExec(t, repoAPI, []string{"GIT_AUTHOR_DATE=" + commit1Date, "GIT_COMMITTER_DATE=" + commit1Date}, "commit", "-m", "api c1")
	_ = os.WriteFile(filepath.Join(apiBeads, "issues.jsonl"), []byte(
		`{"id":"1","title":"API base","status":"open","priority":1,"issue_type":"task"}`+"\n"+
			`{"id":"2","title":"API new","status":"open","priority":1,"issue_type":"task"}`+"\n",
	), 0o644)
	gitExec(t, repoAPI, nil, "add", ".beads/issues.jsonl")
	gitExec(t, repoAPI, []string{"GIT_AUTHOR_DATE=" + commit2Date, "GIT_COMMITTER_DATE=" + commit2Date}, "commit", "-m", "api c2")

	repoWeb := initGitRepo(t)
	webBeads := filepath.Join(repoWeb, ".beads")
	_ = os.MkdirAll(webBeads, 0o755)
	_ = os.WriteFile(filepath.Join(webBeads, "issues.jsonl"), []byte(`{"id":"1","title":"Web base","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644)
	gitExec(t, repoWeb, nil, "add", ".beads/issues.jsonl")
	gitExec(t, repoWeb, []string{"GIT_AUTHOR_DATE=" + commit1Date, "GIT_COMMITTER_DATE=" + commit1Date}, "commit", "-m", "web c1")
	_ = os.WriteFile(filepath.Join(webBeads, "issues.jsonl"), []byte(
		`{"id":"1","title":"Web base","status":"open","priority":1,"issue_type":"task"}`+"\n"+
			`{"id":"2","title":"Web new","status":"open","priority":1,"issue_type":"task"}`+"\n",
	), 0o644)
	gitExec(t, repoWeb, nil, "add", ".beads/issues.jsonl")
	gitExec(t, repoWeb, []string{"GIT_AUTHOR_DATE=" + commit2Date, "GIT_COMMITTER_DATE=" + commit2Date}, "commit", "-m", "web c2")

	wsIssues := []model.Issue{
		{ID: "api-1", Title: "API base", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api", Origin: &model.IssueOrigin{LocalID: "1", WorkingDirectory: repoAPI, TrackerDirectory: apiBeads}},
		{ID: "api-2", Title: "API new", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api", Origin: &model.IssueOrigin{LocalID: "2", WorkingDirectory: repoAPI, TrackerDirectory: apiBeads}},
		{ID: "web-1", Title: "Web base", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web", Origin: &model.IssueOrigin{LocalID: "1", WorkingDirectory: repoWeb, TrackerDirectory: webBeads}},
		{ID: "web-2", Title: "Web new", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web", Origin: &model.IssueOrigin{LocalID: "2", WorkingDirectory: repoWeb, TrackerDirectory: webBeads}},
	}
	mWS := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
	t.Cleanup(mWS.Stop)
	mWS.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, TotalIssues: 4, RepoPrefixes: []string{"api-", "web-"}})
	mWS.activeRepos = map[string]bool{"api": true}

	mWS.enterTimeTravelMode("HEAD~1")
	wsDiff := mWS.TimeTravelDiff()
	if !mWS.IsTimeTravelMode() || wsDiff == nil || wsDiff.Summary.IssuesAdded != 1 || !mWS.newIssueIDs["api-2"] || mWS.newIssueIDs["web-2"] {
		t.Fatalf("expected activeRepos={api} workspace time-travel to report only api-2 (+1), got diff=%+v new=%v status=%q",
			wsDiff, mWS.newIssueIDs, mWS.statusMsg)
	}

	// Planted control: clearing activeRepos aggregates both repos (+2 added: api-2 and web-2).
	mWS.exitTimeTravelMode()
	mWS.activeRepos = nil
	mWS.enterTimeTravelMode("HEAD~1")
	if allDiff := mWS.TimeTravelDiff(); allDiff == nil || allDiff.Summary.IssuesAdded != 2 || !mWS.newIssueIDs["api-2"] || !mWS.newIssueIDs["web-2"] {
		t.Fatalf("expected unfiltered multi-repo time-travel to report api-2 and web-2 (+2), got diff=%+v new=%v", allDiff, mWS.newIssueIDs)
	}
}

func TestScopedLabelViewsFlowMatrixSubgraphPickerAndVelocityExcludeContextIssues(t *testing.T) {
	refNow := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	closedAt := refNow.Add(-24 * time.Hour)

	issues := []model.Issue{
		{
			ID: "CAND-1", Title: "Candidate blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask,
			Labels: []string{"cand-core"}, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: refNow.Add(-2 * time.Hour),
		},
		{
			ID: "CAND-2", Title: "Candidate dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Labels:    []string{"cand-ui", "cand-core"},
			CreatedAt: refNow.Add(-4 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "CAND-2", DependsOnID: "CAND-1", Type: model.DepBlocks}},
		},
		{
			ID: "CAND-3", Title: "Candidate closed", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
			Labels: []string{"cand-ui"}, CreatedAt: refNow.Add(-6 * 24 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt,
		},
		// Non-candidate context issues with disjoint cross-label dependency ghost-db -> ghost-api AND extra node on cand-core:
		{
			ID: "GHOST-1", Title: "Context ghost blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask,
			Labels: []string{"ghost-db"}, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
		},
		{
			ID: "GHOST-2", Title: "Context ghost dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Labels: []string{"ghost-api", "cand-core"}, CreatedAt: refNow.Add(-3 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "GHOST-2", DependsOnID: "GHOST-1", Type: model.DepBlocks}},
		},
	}

	cands := map[string]bool{"CAND-1": true, "CAND-2": true, "CAND-3": true}
	auth := model.NewReadinessIndex(issues)

	mScoped := NewModel(copyIssues(issues), nil, "", ReadinessScope{
		Authority:    auth,
		CandidateIDs: cands,
		ReferenceNow: refNow,
	})
	t.Cleanup(mScoped.Stop)
	mScoped.analysis.WaitForPhase2()
	u, _ := mScoped.Update(tea.WindowSizeMsg{Width: 130, Height: 38})
	mScoped = u.(*Model)

	// 1. Initial & opened LabelPicker ('l') excludes ghost-db / ghost-api.
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	mScoped = u.(*Model)
	pickerView := ansi.Strip(mScoped.labelPicker.View())
	if !strings.Contains(pickerView, "cand-core") || !strings.Contains(pickerView, "cand-ui") {
		t.Fatalf("scoped labelPicker missing candidate labels:\n%s", pickerView)
	}
	if strings.Contains(pickerView, "ghost-db") || strings.Contains(pickerView, "ghost-api") {
		t.Fatalf("scoped labelPicker leaked non-candidate labels:\n%s", pickerView)
	}
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mScoped = u.(*Model)

	// 2. LabelDashboard ('['), VelocityComparison ('v'), LabelHealthDetail ('h'), and LabelSubgraph ('enter' -> 'g').
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	mScoped = u.(*Model)
	if mScoped.labelHealthCache.TotalLabels != 2 {
		t.Fatalf("scoped LabelDashboard TotalLabels = %d, want 2 (cand-core, cand-ui)", mScoped.labelHealthCache.TotalLabels)
	}
	dashView := ansi.Strip(mScoped.View())
	if strings.Contains(dashView, "ghost-db") || strings.Contains(dashView, "ghost-api") {
		t.Fatalf("scoped LabelDashboard leaked ghost labels:\n%s", dashView)
	}

	// Cross-flows on cand-core must NOT include GHOST-1(ghost-db) -> GHOST-2(cand-core) since neither endpoint is in candidateIDs!
	coreFlow := mScoped.getCrossFlowsForLabel("cand-core")
	for _, inc := range coreFlow.Incoming {
		if inc.Label == "ghost-db" {
			t.Fatalf("scoped getCrossFlowsForLabel(cand-core) leaked non-candidate edge from ghost-db: %+v", coreFlow)
		}
	}

	// VelocityComparison ('v')
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	mScoped = u.(*Model)
	if mScoped.velocityComparison.DataCount() != 2 {
		t.Fatalf("scoped VelocityComparison DataCount = %d, want 2", mScoped.velocityComparison.DataCount())
	}
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mScoped = u.(*Model)

	// Open drilldown on cand-core and press 'g' for LabelSubgraph: GHOST-2 must be excluded from Subgraph!
	mScoped.showLabelDrilldown = true
	mScoped.labelDrilldownLabel = "cand-core"
	mScoped.labelDrilldownIssues = mScoped.filterIssuesByLabel("cand-core")
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	mScoped = u.(*Model)
	if mScoped.labelGraphAnalysisResult == nil {
		t.Fatal("expected labelGraphAnalysisResult after 'g' in drilldown")
	}
	for _, id := range mScoped.labelGraphAnalysisResult.Subgraph.CoreIssues {
		if id == "GHOST-2" {
			t.Fatalf("scoped ComputeLabelSubgraph leaked non-candidate GHOST-2 into CoreIssues: %v", mScoped.labelGraphAnalysisResult.Subgraph.CoreIssues)
		}
	}
	mScoped.showLabelGraphAnalysis = false
	mScoped.showLabelDrilldown = false
	mScoped.focused = focusList

	// 3. AttentionView (']') excludes ghost-db / ghost-api.
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	mScoped = u.(*Model)
	if mScoped.attentionView.Len() != 2 {
		t.Fatalf("scoped AttentionView Len = %d, want 2", mScoped.attentionView.Len())
	}
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mScoped = u.(*Model)

	// 4. FlowMatrix ('f' + refreshFlowMatrix) uses ComputeCrossLabelFlowWithScope and ComputeBlockageCascadeAtWithScope,
	// excluding ghost-db -> ghost-api AND non-candidate GHOST-2 from cand-core's cascade count.
	u, _ = mScoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	mScoped = u.(*Model)
	for _, st := range mScoped.flowMatrix.labelStats {
		if st.Label == "ghost-db" || st.Label == "ghost-api" {
			t.Fatalf("scoped FlowMatrix leaked non-candidate label %q: %+v", st.Label, mScoped.flowMatrix.labelStats)
		}
	}
	scopedCascade := mScoped.flowMatrix.cascade.GetCascadeForLabel("cand-core")
	if scopedCascade == nil || scopedCascade.BlockedCount != 1 {
		t.Fatalf("scoped FlowMatrix cand-core cascade = %+v, want BlockedCount=1 (excluding GHOST-2)", scopedCascade)
	}
	scopedFlowView := ansi.Strip(mScoped.flowMatrix.View())
	if !strings.Contains(scopedFlowView, "Blocked in cand-core: 1") || strings.Contains(scopedFlowView, "Blocked in cand-core: 2") || strings.Contains(scopedFlowView, "GHOST-2") {
		t.Fatalf("scoped FlowMatrix View() leaked non-candidate GHOST-2 into cascade:\n%s", scopedFlowView)
	}
	mScoped.refreshFlowMatrix()
	for _, st := range mScoped.flowMatrix.labelStats {
		if st.Label == "ghost-db" || st.Label == "ghost-api" {
			t.Fatalf("scoped refreshFlowMatrix leaked non-candidate label %q", st.Label)
		}
	}
	if refreshedCascade := mScoped.flowMatrix.cascade.GetCascadeForLabel("cand-core"); refreshedCascade == nil || refreshedCascade.BlockedCount != 1 {
		t.Fatalf("scoped refreshFlowMatrix cand-core cascade = %+v, want BlockedCount=1", refreshedCascade)
	}

	// Planted negative control: unscoped Model includes all 4 labels and counts GHOST-2 in cand-core cascade (BlockedCount == 2).
	mUnscoped := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
	t.Cleanup(mUnscoped.Stop)
	mUnscoped.analysis.WaitForPhase2()
	uUn, _ := mUnscoped.Update(tea.WindowSizeMsg{Width: 130, Height: 38})
	mUnscoped = uUn.(*Model)
	uUn, _ = mUnscoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	mUnscoped = uUn.(*Model)
	if mUnscoped.labelHealthCache.TotalLabels != 4 {
		t.Fatalf("negative control failed: unscoped LabelDashboard TotalLabels = %d, want 4", mUnscoped.labelHealthCache.TotalLabels)
	}
	uUn, _ = mUnscoped.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mUnscoped = uUn.(*Model)
	uUn, _ = mUnscoped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	mUnscoped = uUn.(*Model)
	foundGhostFlow := false
	for _, st := range mUnscoped.flowMatrix.labelStats {
		if st.Label == "ghost-db" {
			foundGhostFlow = true
		}
	}
	if !foundGhostFlow {
		t.Fatalf("negative control failed: unscoped FlowMatrix missing ghost-db: %+v", mUnscoped.flowMatrix.labelStats)
	}
	if unscopedCascade := mUnscoped.flowMatrix.cascade.GetCascadeForLabel("cand-core"); unscopedCascade == nil || unscopedCascade.BlockedCount != 2 {
		t.Fatalf("negative control failed: unscoped FlowMatrix cand-core cascade = %+v, want BlockedCount=2 (CAND-2 + GHOST-2)", unscopedCascade)
	}
}

func TestSprintsReloadAsOfGraphSizingClipboardAndExportMetadata(t *testing.T) {
	refNow := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	t.Run("SetSprints_SetAsOfRevision_SourceReloadSprints_and_reloadSprintsFromDisk_fallback", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(beadsPath, []byte(`{"id":"SP-1","title":"Sprint task","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Write single-repo sprints.jsonl on disk with DISK-ONLY sprint to prove SourceReloadResult{SprintsLoaded: true} wins!
		sprintsPath := filepath.Join(beadsDir, "sprints.jsonl")
		diskSprintJSON := `{"id":"disk-sprint","name":"Single Repo Disk Sprint","start_date":"2026-07-01T00:00:00Z","end_date":"2026-07-20T00:00:00Z","bead_ids":["SP-1"]}` + "\n"
		if err := os.WriteFile(sprintsPath, []byte(diskSprintJSON), 0o644); err != nil {
			t.Fatal(err)
		}

		issues := []model.Issue{{ID: "SP-1", Title: "Sprint task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow}}
		m := NewModel(copyIssues(issues), nil, beadsPath, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()

		// 1. SetSprints clones BeadIDs, selects active sprint at refNow, and clears sprintViewText when emptied outside sprint view.
		callerIDs := []string{"SP-1"}
		sActive := model.Sprint{ID: "ws-active", Name: "Active WS Sprint", StartDate: refNow.Add(-24 * time.Hour), EndDate: refNow.Add(5 * 24 * time.Hour), BeadIDs: callerIDs}
		sFuture := model.Sprint{ID: "ws-future", Name: "Future WS Sprint", StartDate: refNow.Add(10 * 24 * time.Hour), EndDate: refNow.Add(20 * 24 * time.Hour), BeadIDs: []string{"SP-1"}}
		m.SetSprints([]model.Sprint{sFuture, sActive})
		callerIDs[0] = "MUTATED"
		if m.selectedSprint == nil || m.selectedSprint.ID != "ws-active" || m.sprints[1].BeadIDs[0] != "SP-1" {
			t.Fatalf("SetSprints failed active selection or defensive clone: sel=%+v sprints=%+v", m.selectedSprint, m.sprints)
		}
		if m.sprintViewText == "" {
			t.Fatal("expected SetSprints with non-empty sprints to render sprintViewText")
		}
		m.SetSprints(nil)
		if m.sprints != nil || m.selectedSprint != nil || m.sprintViewText != "" {
			t.Fatalf("SetSprints(nil) outside sprint view left stale state: sprints=%v sel=%v text=%q",
				m.sprints, m.selectedSprint, m.sprintViewText)
		}

		// 2. SetAsOfRevision resets history report & generations when changed, and is a no-op when repeated with same revision.
		m.historyView.SetReport(createTestHistoryReport())
		m.historyReportDataGeneration = 7
		m.SetAsOfRevision("  HEAD~3  ")
		if m.asOfRevision != "HEAD~3" || m.historyView.report != nil || m.historyReportDataGeneration != 0 {
			t.Fatalf("SetAsOfRevision did not trim/reset history state: rev=%q report=%v gen=%d",
				m.asOfRevision, m.historyView.report, m.historyReportDataGeneration)
		}
		// Planted negative control: calling SetAsOfRevision with identical trimmed revision preserves existing report.
		rep := createTestHistoryReport()
		m.historyView.SetReport(rep)
		m.SetAsOfRevision("HEAD~3")
		if m.historyView.report != rep {
			t.Fatal("same-revision SetAsOfRevision unexpectedly cleared historyView report")
		}

		// 3. SourceReloadResult{Sprints, SprintsLoaded: true} preserves selected sprint ID when present,
		// falls back to activeSprintIndex when removed, and is NOT overwritten by disk-sprint!
		m.SetSprints([]model.Sprint{sActive, sFuture})
		m.selectedSprint = &m.sprints[1] // user manually selected ws-future
		reloadSprints := []model.Sprint{sActive, sFuture}
		m.SetSourceReload(func(context.Context) (SourceReloadResult, error) {
			return SourceReloadResult{
				Issues:        copyIssues(issues),
				Sprints:       reloadSprints,
				SprintsLoaded: true,
			}, nil
		})
		cmd := m.TriggerSourceReload()
		u, _ := m.Update(cmd())
		m = u.(*Model)
		m.analysis.WaitForPhase2()
		if m.selectedSprint == nil || m.selectedSprint.ID != "ws-future" || !strings.Contains(m.sprintViewText, "Future WS Sprint") {
			t.Fatalf("SourceReloadResult{SprintsLoaded:true} lost selected sprint ws-future or got overwritten by disk: sel=%+v text=%q",
				m.selectedSprint, m.sprintViewText)
		}
		// Now drop ws-future from reloadSprints -> falls back to activeSprintIndex (ws-active).
		reloadSprints = []model.Sprint{
			{ID: "ws-past", Name: "Past WS Sprint", StartDate: refNow.Add(-20 * 24 * time.Hour), EndDate: refNow.Add(-10 * 24 * time.Hour)},
			sActive,
		}
		cmd = m.TriggerSourceReload()
		u, _ = m.Update(cmd())
		m = u.(*Model)
		m.analysis.WaitForPhase2()
		if m.selectedSprint == nil || m.selectedSprint.ID != "ws-active" {
			t.Fatalf("expected SourceReloadResult sprint fallback to activeSprintIndex (ws-active), got %+v", m.selectedSprint)
		}

		// 4. reloadSprintsFromDisk activeSprintIndex fallback when disk sprints.jsonl replaces the selected sprint ID.
		mDisk := NewModel(copyIssues(issues), nil, beadsPath, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mDisk.Stop)
		mDisk.analysis.WaitForPhase2()
		if mDisk.selectedSprint != nil && mDisk.selectedSprint.ID != "disk-sprint" {
			t.Fatalf("unexpected initial disk sprint: %+v", mDisk.selectedSprint)
		}
		newDiskSprints := strings.Join([]string{
			`{"id":"old-ended","name":"Old Ended","start_date":"2026-06-01T00:00:00Z","end_date":"2026-06-15T00:00:00Z","bead_ids":["SP-1"]}`,
			`{"id":"now-current","name":"Now Current","start_date":"2026-07-05T00:00:00Z","end_date":"2026-07-25T00:00:00Z","bead_ids":["SP-1"]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(sprintsPath, []byte(newDiskSprints), 0o644); err != nil {
			t.Fatal(err)
		}
		mDisk.reloadSprintsFromDisk(focusSprint)
		if mDisk.selectedSprint == nil || mDisk.selectedSprint.ID != "now-current" {
			t.Fatalf("expected reloadSprintsFromDisk fallback to active sprint 'now-current', got %+v", mDisk.selectedSprint)
		}
	})

	t.Run("GraphModel_SetSize_enables_ScrollDown_and_ScrollRight_before_first_View", func(t *testing.T) {
		// Build a 2-node graph (1 blocker -> 22 cols wide, ~14 lines tall) that fits inside default 120x40
		// (where viewport is 89x38, so maxScrollX==0 and maxScrollY==0) but overflows a resized 36x6 terminal!
		graphIssues := []model.Issue{
			{ID: "BLK-1", Title: "Blocker One", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask},
			{
				ID: "HUB", Title: "Hub Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{{IssueID: "HUB", DependsOnID: "BLK-1", Type: model.DepBlocks}},
			},
		}

		// Planted negative control: without WindowSizeMsg/SetSize, g.width==0 && g.height==0 fall back to 120x40 where HUB fits (scrollX==0, scrollY==0).
		mDefault := NewModel(copyIssues(graphIssues), nil, "")
		t.Cleanup(mDefault.Stop)
		mDefault.analysis.WaitForPhase2()
		mDefault.graphView.SelectByID("HUB")
		mDefault.graphView.ScrollDown()
		mDefault.graphView.ScrollRight()
		if mDefault.graphView.scrollY != 0 || mDefault.graphView.scrollX != 0 {
			t.Fatalf("negative control failed: expected scrollX==0 && scrollY==0 at default 120x40 before resize, got (%d, %d)",
				mDefault.graphView.scrollX, mDefault.graphView.scrollY)
		}

		// Positive observable: WindowSizeMsg{Width: 36, Height: 6} calls applyContentSizing -> graphView.SetSize BEFORE any View() call!
		mResized := NewModel(copyIssues(graphIssues), nil, "")
		t.Cleanup(mResized.Stop)
		mResized.analysis.WaitForPhase2()
		u, _ := mResized.Update(tea.WindowSizeMsg{Width: 36, Height: 6})
		mResized = u.(*Model)
		mResized.graphView.SelectByID("HUB")
		mResized.graphView.ScrollDown()
		mResized.graphView.ScrollRight()
		if mResized.graphView.scrollY <= 0 || mResized.graphView.scrollX <= 0 {
			t.Fatalf("expected ScrollDown & ScrollRight > 0 after WindowSizeMsg before first View(), got scrollX=%d scrollY=%d (max=%d,%d)",
				mResized.graphView.scrollX, mResized.graphView.scrollY, mResized.graphView.maxScrollX, mResized.graphView.maxScrollY)
		}
	})

	t.Run("copyIssueToClipboard_Design_Notes_Comments_and_exportToMarkdown_hashes", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			clipBinDir := t.TempDir()
			clipOut := filepath.Join(clipBinDir, "clipboard.txt")
			helperName := "wl-copy"
			if runtime.GOOS == "darwin" {
				helperName = "pbcopy"
			}
			script := "#!/bin/sh\ncat > \"$BV_TEST_CLIP_OUT\"\n"
			if err := os.WriteFile(filepath.Join(clipBinDir, helperName), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", clipBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("WAYLAND_DISPLAY", "wayland-test")
			t.Setenv("BV_TEST_CLIP_OUT", clipOut)

			commentTime := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
			richIssue := model.Issue{
				ID:          "CLIP-1",
				Title:       "Rich Clipboard Issue",
				Status:      model.StatusOpen,
				Priority:    1,
				IssueType:   model.TypeFeature,
				CreatedAt:   refNow,
				UpdatedAt:   refNow,
				Description: "Core description body.",
				Design:      "Architecture uses immutable snapshots.",
				Notes:       "Roll out behind feature flag.",
				Comments: []*model.Comment{
					nil,
					{Author: "ghost", Text: "   \n\t  ", CreatedAt: commentTime},
					{Author: "alice", Text: "Verified parity on staging.", CreatedAt: commentTime},
					{Author: "", Text: "Anonymous follow-up."},
				},
			}
			blankCommentsIssue := model.Issue{
				ID:        "CLIP-BLANK",
				Title:     "Only blank comments",
				Status:    model.StatusOpen,
				Priority:  2,
				IssueType: model.TypeTask,
				CreatedAt: refNow,
				Comments: []*model.Comment{
					nil,
					{Author: "nobody", Text: "   "},
				},
			}

			mClip := NewModel([]model.Issue{richIssue, blankCommentsIssue}, nil, "")
			t.Cleanup(mClip.Stop)
			mClip.selectVisibleListItemByID("CLIP-1")
			copyCmd := mClip.copyIssueToClipboard()
			if copyCmd == nil {
				t.Fatalf("expected non-nil copyIssueToClipboard cmd, status=%q", mClip.statusMsg)
			}
			_ = copyCmd()
			copiedBytes, err := os.ReadFile(clipOut)
			if err != nil {
				t.Fatalf("read captured clipboard: %v", err)
			}
			copied := string(copiedBytes)
			for _, want := range []string{
				"## Design\n\nArchitecture uses immutable snapshots.",
				"## Notes\n\nRoll out behind feature flag.",
				"## Comments",
				"- **alice (2026-07-09):** Verified parity on staging.",
				"- **Comment:** Anonymous follow-up.",
			} {
				if !strings.Contains(copied, want) {
					t.Fatalf("copied markdown missing %q:\n%s", want, copied)
				}
			}
			if strings.Contains(copied, "ghost") {
				t.Fatalf("copied markdown failed to skip whitespace-only comment:\n%s", copied)
			}

			// Planted negative control: issue with ONLY nil/blank comments omits "## Comments" section completely.
			mClip.selectVisibleListItemByID("CLIP-BLANK")
			blankCmd := mClip.copyIssueToClipboard()
			_ = blankCmd()
			blankCopiedBytes, err := os.ReadFile(clipOut)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(blankCopiedBytes), "## Comments") {
				t.Fatalf("expected blank-only comments to omit ## Comments section, got:\n%s", string(blankCopiedBytes))
			}
		}

		// exportToMarkdown: verifies WaitForPhase2() + DataHash / AuthorityHash / SourcePath propagation.
		exportWorkDir := t.TempDir()
		beadsSourcePath := filepath.Join(exportWorkDir, ".beads", "issues.jsonl")
		if err := os.MkdirAll(filepath.Dir(beadsSourcePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(beadsSourcePath, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}

		allExportIssues := []model.Issue{
			{ID: "EXP-ROOT", Title: "Root blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, Labels: []string{"ship"}, CreatedAt: refNow, UpdatedAt: refNow},
			{
				ID: "EXP-DEP", Title: "Scoped target", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"ship"}, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "EXP-DEP", DependsOnID: "EXP-ROOT", Type: model.DepBlocks}},
			},
			{ID: "EXP-OTHER", Title: "Out-of-filter context", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		jsonRecipe := &recipe.Recipe{
			Name:    "ship-json",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
			Metrics: []string{"pagerank", "kcore"},
			Export:  recipe.ExportConfig{Format: "json"},
		}
		mExp := NewModel(copyIssues(allExportIssues), jsonRecipe, beadsSourcePath, ReadinessScope{
			Authority:    model.NewReadinessIndex(allExportIssues),
			ReferenceNow: refNow,
		})
		t.Cleanup(mExp.Stop)
		mExp.SetWorkDir(exportWorkDir)
		// Do NOT pre-call WaitForPhase2 — exportToMarkdown must wait for Phase 2 itself!
		mExp.exportToMarkdown()
		if mExp.statusIsError || !mExp.analysis.IsPhase2Ready() {
			t.Fatalf("exportToMarkdown failed or did not WaitForPhase2: err=%v status=%q phase2=%v",
				mExp.statusIsError, mExp.statusMsg, mExp.analysis.IsPhase2Ready())
		}

		jsonPath := filepath.Join(exportWorkDir, "beads_export_2026-07-10.json")
		rawJSON, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatalf("read exported JSON report %s: %v", jsonPath, err)
		}
		var payload struct {
			AuthorityHash string `json:"authority_hash"`
			DataHash      string `json:"data_hash"`
			SourcePath    string `json:"source_path"`
			Issues        []struct {
				ID string `json:"id"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(rawJSON, &payload); err != nil {
			t.Fatalf("unmarshal exported JSON: %v", err)
		}
		wantAuthHash := analysis.ComputeDataHash(allExportIssues)
		wantDataHash := analysis.ComputeDataHash(mExp.exportIssuesForCurrentView())
		if payload.SourcePath != beadsSourcePath {
			t.Fatalf("SourcePath = %q, want %q", payload.SourcePath, beadsSourcePath)
		}
		if payload.AuthorityHash != wantAuthHash || payload.DataHash != wantDataHash || payload.AuthorityHash == payload.DataHash {
			t.Fatalf("unexpected hashes: authority_hash=%q (want %q), data_hash=%q (want %q; must differ because EXP-OTHER is filtered out)",
				payload.AuthorityHash, wantAuthHash, payload.DataHash, wantDataHash)
		}
		if len(payload.Issues) != 2 {
			t.Fatalf("expected 2 open exported issues, got %d", len(payload.Issues))
		}
	})
}

func TestTimeTravelDynamicHistoricalScopeAndWorkspaceRepoTargets(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitExec := func(t *testing.T, dir string, extraEnv []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(loader.SanitizedGitEnv(), extraEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
	}
	initGitRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		gitExec(t, dir, nil, "init")
		gitExec(t, dir, nil, "config", "user.email", "test@example.com")
		gitExec(t, dir, nil, "config", "user.name", "Test User")
		return dir
	}

	refNow := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	t.Run("a_label_filter_surfaces_deleted_labeled_issue_and_excludes_unlabeled", func(t *testing.T) {
		repoDir := initGitRepo(t)
		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")

		c1Raw := strings.Join([]string{
			`{"id":"DEL-1","title":"Deleted backend","status":"open","priority":1,"issue_type":"task","labels":["backend"]}`,
			`{"id":"OTHER-DEL","title":"Deleted other","status":"open","priority":1,"issue_type":"task"}`,
			`{"id":"KEEP-1","title":"Kept backend","status":"open","priority":1,"issue_type":"task","labels":["backend"]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(c1Raw), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExec(t, repoDir, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=2026-02-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-02-01T10:00:00Z"}, "commit", "-m", "c1")

		c2Raw := `{"id":"KEEP-1","title":"Kept backend","status":"open","priority":1,"issue_type":"task","labels":["backend"]}` + "\n"
		if err := os.WriteFile(beadsPath, []byte(c2Raw), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExec(t, repoDir, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=2026-02-15T10:00:00Z", "GIT_COMMITTER_DATE=2026-02-15T10:00:00Z"}, "commit", "-m", "c2")

		headIssues, err := loader.LoadIssuesFromFile(beadsPath)
		if err != nil {
			t.Fatal(err)
		}
		mScoped := NewModel(copyIssues(headIssues), nil, beadsPath, ReadinessScope{
			Authority:    model.NewReadinessIndex(headIssues),
			CandidateIDs: map[string]bool{"KEEP-1": true},
			LabelFilter:  "backend",
			ReferenceNow: refNow,
		})
		t.Cleanup(mScoped.Stop)
		mScoped.SetWorkDir(repoDir)

		mScoped.enterTimeTravelMode("HEAD~1")
		diff := mScoped.TimeTravelDiff()
		if !mScoped.IsTimeTravelMode() || diff == nil || len(diff.RemovedIssues) != 1 || diff.RemovedIssues[0].ID != "DEL-1" {
			t.Fatalf("expected LabelFilter=backend to report RemovedIssues=[DEL-1], got diff=%+v status=%q", diff, mScoped.statusMsg)
		}

		// Planted negative control: unscoped model reports BOTH DEL-1 and OTHER-DEL as removed.
		mUnscoped := NewModel(copyIssues(headIssues), nil, beadsPath, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mUnscoped.Stop)
		mUnscoped.SetWorkDir(repoDir)
		mUnscoped.enterTimeTravelMode("HEAD~1")
		if uDiff := mUnscoped.TimeTravelDiff(); uDiff == nil || len(uDiff.RemovedIssues) != 2 {
			t.Fatalf("negative control failed: expected unscoped RemovedIssues len=2, got %+v", uDiff)
		}
	})

	t.Run("b_actionable_recipe_counts_actionable_issue_closed_at_head", func(t *testing.T) {
		repoDir := initGitRepo(t)
		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")

		c1Raw := strings.Join([]string{
			`{"id":"ACT-1","title":"Actionable then closed","status":"open","priority":1,"issue_type":"task"}`,
			`{"id":"OPEN-DEP","title":"Still open blocker","status":"open","priority":0,"issue_type":"task"}`,
			`{"id":"BLK-1","title":"Blocked then closed","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"BLK-1","depends_on_id":"OPEN-DEP","type":"blocks"}]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(c1Raw), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExec(t, repoDir, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=2026-03-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-03-01T10:00:00Z"}, "commit", "-m", "c1")

		c2Raw := strings.Join([]string{
			`{"id":"ACT-1","title":"Actionable then closed","status":"closed","priority":1,"issue_type":"task"}`,
			`{"id":"OPEN-DEP","title":"Still open blocker","status":"open","priority":0,"issue_type":"task"}`,
			`{"id":"BLK-1","title":"Blocked then closed","status":"closed","priority":1,"issue_type":"task","dependencies":[{"issue_id":"BLK-1","depends_on_id":"OPEN-DEP","type":"blocks"}]}`,
		}, "\n") + "\n"
		if err := os.WriteFile(beadsPath, []byte(c2Raw), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExec(t, repoDir, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoDir, []string{"GIT_AUTHOR_DATE=2026-03-10T10:00:00Z", "GIT_COMMITTER_DATE=2026-03-10T10:00:00Z"}, "commit", "-m", "c2")

		headIssues, err := loader.LoadIssuesFromFile(beadsPath)
		if err != nil {
			t.Fatal(err)
		}
		tr := true
		actionableRecipe := &recipe.Recipe{
			Name: "actionable",
			Filters: recipe.FilterConfig{
				Status:     []string{"open", "in_progress"},
				Actionable: &tr,
			},
		}
		m := NewModel(copyIssues(headIssues), actionableRecipe, beadsPath, ReadinessScope{
			Authority:    model.NewReadinessIndex(headIssues),
			CandidateIDs: map[string]bool{"OPEN-DEP": true},
			ReferenceNow: refNow,
		})
		t.Cleanup(m.Stop)
		m.SetWorkDir(repoDir)

		m.enterTimeTravelMode("HEAD~1")
		diff := m.TimeTravelDiff()
		if !m.IsTimeTravelMode() || diff == nil || len(diff.ClosedIssues) != 1 || diff.ClosedIssues[0].ID != "ACT-1" {
			t.Fatalf("expected actionable recipe ClosedIssues=[ACT-1] (excluding non-actionable BLK-1), got diff=%+v status=%q", diff, m.statusMsg)
		}
		if len(diff.RemovedIssues) != 0 || !m.closedIssueIDs["ACT-1"] || m.closedIssueIDs["BLK-1"] {
			t.Fatalf("unexpected removed/closed sets under actionable recipe: removed=%+v closedIDs=%v", diff.RemovedIssues, m.closedIssueIDs)
		}
	})

	t.Run("c_workspace_repo_targets_anchor_from_time_and_preserve_cross_repo_readiness", func(t *testing.T) {
		repoWeb := initGitRepo(t)
		webBeads := filepath.Join(repoWeb, ".beads")
		if err := os.MkdirAll(webBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		// web-1 depends on closed api-1 (ready at HEAD~1 only if api is loaded into histAuthIssues).
		// web-2 is deferred until 2026-06-01 (deferred at web's 2026-03-01, NOT deferred at api's 2026-09-01!).
		webC1 := strings.Join([]string{
			`{"id":"1","title":"Web cross-repo ready","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"1","depends_on_id":"api-1","type":"blocks"}]}`,
			`{"id":"2","title":"Web deferred until June","status":"open","priority":1,"issue_type":"task","defer_until":"2026-06-01T00:00:00Z"}`,
		}, "\n") + "\n"
		_ = os.WriteFile(filepath.Join(webBeads, "issues.jsonl"), []byte(webC1), 0o644)
		gitExec(t, repoWeb, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoWeb, []string{"GIT_AUTHOR_DATE=2026-03-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-03-01T12:00:00Z"}, "commit", "-m", "web c1")

		webC2 := strings.Join([]string{
			`{"id":"1","title":"Web cross-repo ready","status":"closed","priority":1,"issue_type":"task","dependencies":[{"issue_id":"1","depends_on_id":"api-1","type":"blocks"}]}`,
			`{"id":"2","title":"Web deferred until June","status":"closed","priority":1,"issue_type":"task","defer_until":"2026-06-01T00:00:00Z"}`,
		}, "\n") + "\n"
		_ = os.WriteFile(filepath.Join(webBeads, "issues.jsonl"), []byte(webC2), 0o644)
		gitExec(t, repoWeb, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoWeb, []string{"GIT_AUTHOR_DATE=2026-03-15T12:00:00Z", "GIT_COMMITTER_DATE=2026-03-15T12:00:00Z"}, "commit", "-m", "web c2")

		repoAPI := initGitRepo(t)
		apiBeads := filepath.Join(repoAPI, ".beads")
		if err := os.MkdirAll(apiBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		apiC1 := strings.Join([]string{
			`{"id":"1","title":"API closed blocker","status":"closed","priority":0,"issue_type":"task"}`,
			`{"id":"2","title":"API open work","status":"open","priority":1,"issue_type":"task"}`,
		}, "\n") + "\n"
		_ = os.WriteFile(filepath.Join(apiBeads, "issues.jsonl"), []byte(apiC1), 0o644)
		gitExec(t, repoAPI, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoAPI, []string{"GIT_AUTHOR_DATE=2026-09-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-09-01T12:00:00Z"}, "commit", "-m", "api c1")

		apiC2 := strings.Join([]string{
			`{"id":"1","title":"API closed blocker","status":"closed","priority":0,"issue_type":"task"}`,
			`{"id":"2","title":"API open work","status":"closed","priority":1,"issue_type":"task"}`,
		}, "\n") + "\n"
		_ = os.WriteFile(filepath.Join(apiBeads, "issues.jsonl"), []byte(apiC2), 0o644)
		gitExec(t, repoAPI, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoAPI, []string{"GIT_AUTHOR_DATE=2026-09-15T12:00:00Z", "GIT_COMMITTER_DATE=2026-09-15T12:00:00Z"}, "commit", "-m", "api c2")

		repoLegacy := initGitRepo(t)
		legBeads := filepath.Join(repoLegacy, ".beads")
		if err := os.MkdirAll(legBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(legBeads, "issues.jsonl"), []byte(`{"id":"1","title":"Legacy deleted at HEAD","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o644)
		gitExec(t, repoLegacy, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoLegacy, []string{"GIT_AUTHOR_DATE=2026-02-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-02-01T12:00:00Z"}, "commit", "-m", "legacy c1")
		_ = os.WriteFile(filepath.Join(legBeads, "issues.jsonl"), []byte(""), 0o644)
		gitExec(t, repoLegacy, nil, "add", ".beads/issues.jsonl")
		gitExec(t, repoLegacy, []string{"GIT_AUTHOR_DATE=2026-02-15T12:00:00Z", "GIT_COMMITTER_DATE=2026-02-15T12:00:00Z"}, "commit", "-m", "legacy c2")

		deferJune := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		liveIssues := []model.Issue{
			{
				ID: "web-1", Title: "Web cross-repo ready", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web",
				Dependencies: []*model.Dependency{{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks}},
				Origin:       &model.IssueOrigin{LocalID: "1", WorkingDirectory: repoWeb, TrackerDirectory: webBeads},
			},
			{
				ID: "web-2", Title: "Web deferred until June", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web",
				DeferUntil: &deferJune,
				Origin:     &model.IssueOrigin{LocalID: "2", WorkingDirectory: repoWeb, TrackerDirectory: webBeads},
			},
			{
				ID: "api-1", Title: "API closed blocker", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask, SourceRepo: "api",
				Origin: &model.IssueOrigin{LocalID: "1", WorkingDirectory: repoAPI, TrackerDirectory: apiBeads},
			},
			{
				ID: "api-2", Title: "API open work", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api",
				Origin: &model.IssueOrigin{LocalID: "2", WorkingDirectory: repoAPI, TrackerDirectory: apiBeads},
			},
		}

		tr := true
		actionableRecipe := &recipe.Recipe{
			Name: "actionable",
			Filters: recipe.FilterConfig{
				Status:     []string{"open", "in_progress"},
				Actionable: &tr,
			},
		}
		mWS := NewModel(copyIssues(liveIssues), actionableRecipe, "", ReadinessScope{
			Authority:    model.NewReadinessIndex(liveIssues),
			ReferenceNow: refNow,
		})
		t.Cleanup(mWS.Stop)
		mWS.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:      true,
			RepoCount:    3,
			TotalIssues:  len(liveIssues),
			RepoPrefixes: []string{"web-", "api-", "my-leg-"},
			Repos: []WorkspaceRepoTarget{
				{Name: "web", Path: repoWeb, BeadsDir: webBeads, Prefix: "web-"},
				{Name: "api", Path: repoAPI, BeadsDir: apiBeads, Prefix: "api-"},
				{Name: "legacy", Path: repoLegacy, BeadsDir: legBeads, Prefix: "my-leg-"},
			},
		})
		mWS.activeRepos = map[string]bool{"web": true}

		mWS.enterTimeTravelMode("HEAD~1")
		diff := mWS.TimeTravelDiff()
		wantWebCommitTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		if !mWS.IsTimeTravelMode() || diff == nil || !diff.FromTimestamp.Equal(wantWebCommitTime) {
			t.Fatalf("expected activeRepos={web} FromTimestamp=%v (NOT api's 2026-09-01), got diff=%+v status=%q",
				wantWebCommitTime, diff, mWS.statusMsg)
		}
		// web-1 is actionable at 2026-03-01 (because closed api-1 was loaded into histAuthIssues!) -> counted in ClosedIssues.
		// web-2 is still deferred at 2026-03-01 (would falsely be ready if FromTimestamp leaked 2026-09-01!) -> excluded!
		if len(diff.ClosedIssues) != 1 || diff.ClosedIssues[0].ID != "web-1" {
			t.Fatalf("expected ClosedIssues=[web-1] (cross-repo api-1 satisfied, web-2 still deferred at 2026-03-01), got %+v", diff.ClosedIssues)
		}

		// Deleted-at-HEAD repo "legacy" (0 live issues at HEAD, multi-hyphen prefix "my-leg-") is still discovered via WorkspaceInfo.Repos!
		mWS.exitTimeTravelMode()
		mWS.setActiveRecipe(nil)
		mWS.activeRepos = map[string]bool{"my-leg": true}
		mWS.enterTimeTravelMode("HEAD~1")
		legDiff := mWS.TimeTravelDiff()
		if legDiff == nil || len(legDiff.RemovedIssues) != 1 || legDiff.RemovedIssues[0].ID != "my-leg-1" {
			t.Fatalf("expected legacy repo (0 HEAD issues, prefix my-leg-) RemovedIssues=[my-leg-1], got %+v status=%q", legDiff, mWS.statusMsg)
		}

		// Planted negative control: clearing activeRepos=nil advances FromTimestamp to api's 2026-09-01.
		mWS.exitTimeTravelMode()
		mWS.activeRepos = nil
		mWS.enterTimeTravelMode("HEAD~1")
		wantAPICommitTime := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		if allDiff := mWS.TimeTravelDiff(); allDiff == nil || !allDiff.FromTimestamp.Equal(wantAPICommitTime) {
			t.Fatalf("negative control failed: expected unfiltered workspace FromTimestamp=%v, got %+v", wantAPICommitTime, allDiff)
		}

		// [R2-B1] Standalone JSONL issue with unvalidated Origin.TrackerDirectory="/tmp" (no .beads/_beads, Tracker=="")
		// in workspace mode must NOT derive repoWorkDir="/" and abort enterTimeTravelMode!
		mWS.exitTimeTravelMode()
		withTmpOrigin := append(copyIssues(liveIssues), model.Issue{
			ID:        "tmp-standalone-1",
			Title:     "Standalone export row",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
			Origin:    &model.IssueOrigin{TrackerDirectory: "/tmp", ReadOnlyReason: "source has no readable tracker metadata"},
		})
		mWSTmp := NewModel(withTmpOrigin, nil, "", ReadinessScope{
			Authority:    model.NewReadinessIndex(withTmpOrigin),
			ReferenceNow: refNow,
		})
		t.Cleanup(mWSTmp.Stop)
		mWSTmp.SetWorkDir(repoWeb)
		mWSTmp.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:   true,
			RepoCount: 1,
			Repos:     []WorkspaceRepoTarget{{Name: "web", Path: repoWeb, BeadsDir: webBeads, Prefix: "web-"}},
		})
		mWSTmp.enterTimeTravelMode("HEAD~1")
		if !mWSTmp.IsTimeTravelMode() || mWSTmp.statusIsError {
			t.Fatalf("[R2-B1] standalone TrackerDirectory=/tmp broke workspace enterTimeTravelMode: mode=%v err=%v status=%q",
				mWSTmp.IsTimeTravelMode(), mWSTmp.statusIsError, mWSTmp.statusMsg)
		}
	})
}

func TestWave18ScopedActiveInsightsWorkspaceHistoryFooterSprintHistoryAndDebugViews(t *testing.T) {
	refNow := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	t.Run("1_scoped_active_insights_and_active_betweenness_rank_with_closed_filter_negative_control", func(t *testing.T) {
		closedAt := refNow.Add(-48 * time.Hour)
		// CLOSED-BRIDGE sits on 4 paths (CL-SRC1, CL-SRC2 -> CLOSED-BRIDGE -> CL-DST1, CL-DST2) so it dominates raw betweenness,
		// while OPEN-BRIDGE sits on active path OPEN-SRC -> OPEN-BRIDGE -> OPEN-DST.
		issues := []model.Issue{
			{ID: "CL-DST1", Title: "Closed dst 1", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, ClosedAt: &closedAt, CreatedAt: refNow.Add(-10 * 24 * time.Hour), UpdatedAt: closedAt},
			{ID: "CL-DST2", Title: "Closed dst 2", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, ClosedAt: &closedAt, CreatedAt: refNow.Add(-10 * 24 * time.Hour), UpdatedAt: closedAt},
			{
				ID: "CLOSED-BRIDGE", Title: "Historical closed bottleneck", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask, ClosedAt: &closedAt,
				CreatedAt: refNow.Add(-9 * 24 * time.Hour), UpdatedAt: closedAt,
				Dependencies: []*model.Dependency{
					{IssueID: "CLOSED-BRIDGE", DependsOnID: "CL-DST1", Type: model.DepBlocks},
					{IssueID: "CLOSED-BRIDGE", DependsOnID: "CL-DST2", Type: model.DepBlocks},
				},
			},
			{
				ID: "CL-SRC1", Title: "Closed src 1", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, ClosedAt: &closedAt,
				CreatedAt: refNow.Add(-8 * 24 * time.Hour), UpdatedAt: closedAt,
				Dependencies: []*model.Dependency{{IssueID: "CL-SRC1", DependsOnID: "CLOSED-BRIDGE", Type: model.DepBlocks}},
			},
			{
				ID: "CL-SRC2", Title: "Closed src 2", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, ClosedAt: &closedAt,
				CreatedAt: refNow.Add(-8 * 24 * time.Hour), UpdatedAt: closedAt,
				Dependencies: []*model.Dependency{{IssueID: "CL-SRC2", DependsOnID: "CLOSED-BRIDGE", Type: model.DepBlocks}},
			},
			{ID: "OPEN-DST", Title: "Active open foundation", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-4 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour)},
			{
				ID: "OPEN-BRIDGE", Title: "Active open bridge", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-3 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "OPEN-BRIDGE", DependsOnID: "OPEN-DST", Type: model.DepBlocks}},
			},
			{
				ID: "OPEN-SRC", Title: "Active open leaf", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-2 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "OPEN-SRC", DependsOnID: "OPEN-BRIDGE", Type: model.DepBlocks}},
			},
		}

		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.rebuildInsightsPanel()

		hasInsightID := func(items []analysis.InsightItem, id string) bool {
			for _, it := range items {
				if it.ID == id {
					return true
				}
			}
			return false
		}

		ins := m.insightsPanel.insights
		if hasInsightID(ins.Bottlenecks, "CLOSED-BRIDGE") || hasInsightID(ins.Keystones, "CLOSED-BRIDGE") ||
			hasInsightID(ins.Influencers, "CLOSED-BRIDGE") || hasInsightID(ins.Hubs, "CLOSED-BRIDGE") ||
			hasInsightID(ins.Authorities, "CLOSED-BRIDGE") || hasInsightID(ins.Cores, "CLOSED-BRIDGE") {
			t.Fatalf("default TUI Insights leaked closed historical bead CLOSED-BRIDGE: %+v", ins)
		}
		if !hasInsightID(ins.Bottlenecks, "OPEN-BRIDGE") {
			t.Fatalf("expected active OPEN-BRIDGE in Bottlenecks, got %+v", ins.Bottlenecks)
		}

		// Verify GraphModel and SnapshotBuilder use ActiveBetweennessRank (OPEN-BRIDGE ranks #1, NOT CLOSED-BRIDGE).
		m.graphView.SetIssues(m.issues, &ins)
		if gotRank := m.graphView.rankBetweenness["OPEN-BRIDGE"]; gotRank != 1 {
			t.Fatalf("expected GraphModel rankBetweenness[OPEN-BRIDGE]=1 via ActiveBetweennessRank, got %d (map=%v)", gotRank, m.graphView.rankBetweenness)
		}
		sb := NewSnapshotBuilder(copyIssues(issues))
		sb.analyzer.SetNow(refNow)
		snap := sb.Build()
		snap.Analysis.WaitForPhase2()
		snap.GetGraphLayout().UpdatePhase2Ranks(snap.Analysis)
		if snap.GetGraphLayout().RankBetweenness["OPEN-BRIDGE"] != 1 {
			t.Fatalf("expected Snapshot GraphLayout RankBetweenness[OPEN-BRIDGE]=1, got %v", snap.GetGraphLayout().RankBetweenness)
		}
		if hasInsightID(snap.GetInsights().Cores, "CLOSED-BRIDGE") {
			t.Fatalf("SnapshotBuilder PrecomputeInsights leaked CLOSED-BRIDGE: %+v", snap.GetInsights().Cores)
		}

		// Planted negative control: m.currentFilter == "closed" explicitly includes closed issues in Insights.
		m.currentFilter = "closed"
		m.applyFilter()
		m.rebuildInsightsPanel()
		closedIns := m.insightsPanel.insights
		if !hasInsightID(closedIns.Bottlenecks, "CLOSED-BRIDGE") {
			t.Fatalf("negative control failed: expected m.currentFilter==\"closed\" to include CLOSED-BRIDGE in Bottlenecks, got %+v", closedIns.Bottlenecks)
		}
	})

	t.Run("2_workspace_multi_repo_history_detail_commits_repo_filter_and_commit_url", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		wsRoot := t.TempDir() // non-git parent directory (polyrepo workspace)
		repoAPI := filepath.Join(wsRoot, "api")
		repoWeb := filepath.Join(wsRoot, "web")
		for _, d := range []string{repoAPI, repoWeb} {
			if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		gitRun := func(dir string, args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(loader.SanitizedGitEnv(),
				"GIT_AUTHOR_DATE=2026-08-10T10:00:00Z",
				"GIT_COMMITTER_DATE=2026-08-10T10:00:00Z",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
			}
			return strings.TrimSpace(string(out))
		}

		gitRun(repoAPI, "init")
		gitRun(repoAPI, "config", "user.email", "dev@acme.test")
		gitRun(repoAPI, "config", "user.name", "Acme Dev")
		gitRun(repoAPI, "remote", "add", "origin", "https://github.com/acme/api-service.git")
		if err := os.WriteFile(filepath.Join(repoAPI, ".beads", "issues.jsonl"), []byte(`{"id":"bv-101","title":"API auth handler","status":"in_progress","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repoAPI, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoAPI, "pkg", "auth.go"), []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoAPI, "add", ".beads/issues.jsonl", "pkg/auth.go")
		gitRun(repoAPI, "commit", "-m", "feat(bv-101): add auth handler")
		apiSHA := gitRun(repoAPI, "rev-parse", "HEAD")

		gitRun(repoWeb, "init")
		gitRun(repoWeb, "config", "user.email", "dev@acme.test")
		gitRun(repoWeb, "config", "user.name", "Acme Dev")
		gitRun(repoWeb, "remote", "add", "origin", "git@github.com:acme/web-app.git")
		if err := os.WriteFile(filepath.Join(repoWeb, ".beads", "issues.jsonl"), []byte(`{"id":"bv-202","title":"Web button layout","status":"in_progress","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repoWeb, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoWeb, "src", "btn.tsx"), []byte("export const Btn = () => null;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoWeb, "add", ".beads/issues.jsonl", "src/btn.tsx")
		gitRun(repoWeb, "commit", "-m", "fix(web-bv-202): button layout")
		webSHA := gitRun(repoWeb, "rev-parse", "HEAD")

		wsIssues := []model.Issue{
			{
				ID: "api-bv-101", Title: "API auth handler", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api",
				Origin: &model.IssueOrigin{LocalID: "bv-101", WorkingDirectory: repoAPI, TrackerDirectory: filepath.Join(repoAPI, ".beads")},
			},
			{
				ID: "web-bv-202", Title: "Web button layout", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web",
				Origin: &model.IssueOrigin{LocalID: "bv-202", WorkingDirectory: repoWeb, TrackerDirectory: filepath.Join(repoWeb, ".beads")},
			},
		}
		mWS := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mWS.Stop)
		mWS.analysis.WaitForPhase2()
		mWS.SetWorkDir(wsRoot) // non-git root!
		mWS.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:      true,
			RepoCount:    2,
			TotalIssues:  2,
			RepoPrefixes: []string{"api-", "web-"},
			Repos: []WorkspaceRepoTarget{
				{Name: "api", Path: repoAPI, BeadsDir: filepath.Join(repoAPI, ".beads"), Prefix: "api-"},
				{Name: "web", Path: repoWeb, BeadsDir: filepath.Join(repoWeb, ".beads"), Prefix: "web-"},
			},
		})

		cmd := mWS.startHistoryLoad()
		if cmd == nil {
			t.Fatal("expected non-nil workspace startHistoryLoad command")
		}
		u, _ := mWS.Update(cmd())
		mWS = u.(*Model)
		if mWS.historyLoadFailed || !mWS.historyReportIsCurrent() {
			t.Fatalf("workspace history load failed: status=%q", mWS.statusMsg)
		}

		apiHist := mWS.historyView.GetHistoryForBead("api-bv-101")
		webHist := mWS.historyView.GetHistoryForBead("web-bv-202")
		if apiHist == nil || len(apiHist.Commits) == 0 || apiHist.Commits[0].SHA != apiSHA {
			t.Fatalf("expected correlated commit %s on api-bv-101, got %+v", apiSHA, apiHist)
		}
		if webHist == nil || len(webHist.Commits) == 0 || webHist.Commits[0].SHA != webSHA {
			t.Fatalf("expected correlated commit %s on web-bv-202, got %+v", webSHA, webHist)
		}
		foundAPIPrefixedFile := false
		for _, fc := range apiHist.Commits[0].Files {
			if fc.Path == "api/pkg/auth.go" {
				foundAPIPrefixedFile = true
			}
		}
		foundWebPrefixedFile := false
		for _, fc := range webHist.Commits[0].Files {
			if fc.Path == "web/src/btn.tsx" {
				foundWebPrefixedFile = true
			}
		}
		if !foundAPIPrefixedFile || !foundWebPrefixedFile {
			t.Fatalf("expected repo-prefixed FileChange paths, got api=%+v web=%+v", apiHist.Commits[0].Files, webHist.Commits[0].Files)
		}

		// Detail-pane commit markdown renders correlated commits for workspace-prefixed IDs.
		detailMD := mWS.renderBeadHistoryMD("api-bv-101")
		if !strings.Contains(detailMD, "Related Commits (1):") {
			t.Fatalf("expected detail-pane Related Commits for api-bv-101, got:\n%s", detailMD)
		}

		// getCommitURL resolves member repo origin URLs even in History Git Mode ('v') without relying on wsRoot/.git.
		mWS.isHistoryView = true
		mWS.focused = focusHistory
		mWS.historyView.ToggleViewMode()
		if gotAPIURL := mWS.getCommitURL(apiSHA); gotAPIURL != "https://github.com/acme/api-service/commit/"+apiSHA {
			t.Fatalf("getCommitURL(apiSHA) = %q, want https://github.com/acme/api-service/commit/%s", gotAPIURL, apiSHA)
		}
		if gotWebURL := mWS.getCommitURL(webSHA[:8]); gotWebURL != "https://github.com/acme/web-app/commit/"+webSHA[:8] {
			t.Fatalf("getCommitURL(webShortSHA) = %q, want https://github.com/acme/web-app/commit/%s", gotWebURL, webSHA[:8])
		}

		// Active repo filter ('w') scopes workspace HistoryView to activeRepos={"api": true}.
		mWS.activeRepos = map[string]bool{"api": true}
		mWS.syncSecondaryViewsForRepoScope(focusHistory)
		if mWS.historyView.GetHistoryForBead("api-bv-101") == nil || mWS.historyView.GetHistoryForBead("web-bv-202") != nil {
			t.Fatal("expected activeRepos={api} to retain api-bv-101 and filter out web-bv-202 in historyView")
		}
		// Planted negative control: clearing activeRepos restores web-bv-202.
		mWS.activeRepos = nil
		mWS.syncSecondaryViewsForRepoScope(focusHistory)
		if mWS.historyView.GetHistoryForBead("web-bv-202") == nil {
			t.Fatal("negative control failed: clearing activeRepos did not restore web-bv-202 in historyView")
		}
	})

	t.Run("3_filtered_count_split_footers_sprint_scoping_wheel_history_timeline_and_label_cursors", func(t *testing.T) {
		closedAt := refNow.Add(-24 * time.Hour)
		issues := []model.Issue{
			{ID: "SP-CAND", Title: "Alpha Candidate", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"}, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-time.Hour)},
			{ID: "SP-CAND-2", Title: "Beta Candidate", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, Labels: []string{"backend"}, ClosedAt: &closedAt, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: closedAt},
			{ID: "SP-CTX", Title: "Gamma Context NonCandidate", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, Labels: []string{"infra"}, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-time.Hour)},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{
			Authority:    model.NewReadinessIndex(issues),
			CandidateIDs: map[string]bool{"SP-CAND": true, "SP-CAND-2": true},
			LabelFilter:  "backend",
			ReferenceNow: refNow,
		})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		u, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
		m = u.(*Model)

		// Help, ShortcutsSidebar, & GetKeyBindingDocs parity for Tree o/X/Z + b/m & Sprint Enter jump.
		helpBodyLines, _ := m.buildHelpBodyLines()
		helpBody := strings.Join(helpBodyLines, "\n")
		if !strings.Contains(helpBody, "o/X/Z") || !strings.Contains(helpBody, "b/m") {
			t.Fatalf("? Help missing Tree o/X/Z or b/m:\n%s", helpBody)
		}
		sb := NewShortcutsSidebar(m.theme)
		sb.SetSize(34, 40)
		sb.SetContext("tree")
		treeSB := ansi.Strip(sb.View())
		if !strings.Contains(treeSB, "o/X/Z") || !strings.Contains(treeSB, "b/m") {
			t.Fatalf("Tree ShortcutsSidebar missing o/X/Z or b/m:\n%s", treeSB)
		}
		sb.SetContext("sprint")
		sprintSB := ansi.Strip(sb.View())
		if !strings.Contains(sprintSB, "Enter") {
			t.Fatalf("Sprint ShortcutsSidebar missing Enter jump:\n%s", sprintSB)
		}

		// '/' filtered displayedIssueCount(): unfiltered has 3 items in m.issues, filtering "Alpha" leaves 1 visible item.
		if unfilteredCount := m.displayedIssueCount(); unfilteredCount != 3 {
			t.Fatalf("precondition: expected unfiltered displayedIssueCount=3, got %d", unfilteredCount)
		}
		m.list.SetFilterText("Alpha")
		if filteredCount := m.displayedIssueCount(); filteredCount != 1 {
			t.Fatalf("expected '/' filtered displayedIssueCount=1, got %d (state=%v)", filteredCount, m.list.FilterState())
		}
		m.list.ResetFilter()

		// Split-view secondary-view footers must NOT fall through to generic split-view hints ("tab focus").
		if !m.isSplitView {
			t.Fatal("precondition: expected isSplitView=true at width 140")
		}
		m.focused = focusLabelDashboard
		lblFooter := ansi.Strip(m.renderFooter())
		if !strings.Contains(lblFooter, "h health") || !strings.Contains(lblFooter, "d drill") || !strings.Contains(lblFooter, "v velocity") || strings.Contains(lblFooter, "tab focus") {
			t.Fatalf("focusLabelDashboard split-view footer wrong: %q", lblFooter)
		}
		m.focused = focusVelocityComparison
		velFooter := ansi.Strip(m.renderFooter())
		if !strings.Contains(velFooter, "[/esc back") || strings.Contains(velFooter, "tab focus") {
			t.Fatalf("focusVelocityComparison split-view footer wrong: %q", velFooter)
		}
		m.focused = focusAttention
		attFooter := ansi.Strip(m.renderFooter())
		if !strings.Contains(attFooter, "1-9 filter label") || !strings.Contains(attFooter, "]/esc close") || strings.Contains(attFooter, "tab focus") {
			t.Fatalf("focusAttention split-view footer wrong: %q", attFooter)
		}
		m.isSprintView = true
		m.focused = focusSprint
		spFooter := ansi.Strip(m.renderFooter())
		if !strings.Contains(spFooter, "⏎ jump") {
			t.Fatalf("focusSprint footer missing '⏎ jump': %q", spFooter)
		}
		m.isSprintView = false
		m.focused = focusList

		// Sprint candidate scoping + mouse wheel stepping: SP-CTX is NOT in CandidateIDs, so sprint counts only SP-CAND & SP-CAND-2 (1/2 = 50%, NOT 1/3 = 33%),
		// and Enter jumps to SP-CAND (skipping first BeadID SP-CTX!).
		m.SetSprints([]model.Sprint{
			{ID: "sp-1", Name: "Sprint Alpha", StartDate: refNow.Add(-5 * 24 * time.Hour), EndDate: refNow.Add(5 * 24 * time.Hour), BeadIDs: []string{"SP-CTX", "SP-CAND", "SP-CAND-2"}},
			{ID: "sp-2", Name: "Sprint Beta", StartDate: refNow.Add(6 * 24 * time.Hour), EndDate: refNow.Add(15 * 24 * time.Hour), BeadIDs: []string{"SP-CAND"}},
		})
		m.isSprintView = true
		m.focused = focusSprint
		spDash := ansi.Strip(m.renderSprintDashboard())
		if !strings.Contains(spDash, "1/2 (50%)") || strings.Contains(spDash, "1/3") || strings.Contains(spDash, "2/3") {
			t.Fatalf("Sprint dashboard leaked non-candidate SP-CTX into bead totals:\n%s", spDash)
		}
		u, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
		m = u.(*Model)
		if m.selectedSprint == nil || m.selectedSprint.ID != "sp-2" || !strings.Contains(ansi.Strip(m.sprintViewText), "Sprint Beta") {
			t.Fatalf("MouseButtonWheelDown did not advance to sp-2: selected=%+v", m.selectedSprint)
		}
		u, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		m = u.(*Model)
		if m.selectedSprint == nil || m.selectedSprint.ID != "sp-1" {
			t.Fatalf("MouseButtonWheelUp did not step back to sp-1: selected=%+v", m.selectedSprint)
		}
		// Enter jumps to SP-CAND (skipping non-candidate SP-CTX).
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = u.(*Model)
		if sel := m.selectedIssueForAction(); sel == nil || sel.ID != "SP-CAND" {
			t.Fatalf("Sprint Enter jumped to %+v, want SP-CAND (skipping non-candidate SP-CTX)", sel)
		}

		// HistoryModel: SetCassLookup populates buildTimeline() sessions without SetSessionsForBead,
		// and renderFilterLine() compares against non-empty-commit histories.
		rep := &correlation.HistoryReport{
			GeneratedAt: refNow,
			Histories: map[string]correlation.BeadHistory{
				"SP-CAND": {
					BeadID:  "SP-CAND",
					Title:   "Alpha Candidate",
					Status:  "open",
					Commits: []correlation.CorrelatedCommit{{SHA: "abcdef123456", ShortSHA: "abcdef1", Message: "feat: alpha", Timestamp: refNow.Add(-2 * time.Hour), Confidence: 0.95}},
				},
				"EMPTY-1": {BeadID: "EMPTY-1", Title: "No commits 1", Status: "open"},
				"EMPTY-2": {BeadID: "EMPTY-2", Title: "No commits 2", Status: "open"},
			},
			CommitIndex: correlation.CommitIndex{"abcdef123456": {"SP-CAND"}},
		}
		hm := NewHistoryModel(rep, m.theme)
		hm.SetSize(120, 32)
		hm.SetCassLookup(func(beadID string) []cass.ScoredResult {
			if beadID == "SP-CAND" {
				return []cass.ScoredResult{{SearchResult: cass.SearchResult{Agent: "codex", Title: "Lookup Session Proof", Timestamp: refNow.Add(-time.Hour)}, FinalScore: 0.9}}
			}
			return nil
		})
		entries := hm.buildTimeline(rep.Histories["SP-CAND"])
		foundSessionEntry := false
		for _, e := range entries {
			if e.EntryType == timelineEntrySession && strings.Contains(e.Detail, "Lookup Session Proof") {
				foundSessionEntry = true
			}
		}
		if !foundSessionEntry {
			t.Fatalf("buildTimeline did not include session from SetCassLookup: %+v", entries)
		}
		unfilteredLine := ansi.Strip(hm.renderFilterLine())
		if !strings.Contains(unfilteredLine, "Showing all 1 beads with commits") {
			t.Fatalf("expected unfiltered renderFilterLine 'Showing all 1 beads with commits', got %q", unfilteredLine)
		}
		hm.SetAuthorFilter("nonexistent-author")
		filteredLine := ansi.Strip(hm.renderFilterLine())
		if !strings.Contains(filteredLine, "Showing 0/1 beads with commits") {
			t.Fatalf("expected filtered renderFilterLine 'Showing 0/1 beads with commits', got %q", filteredLine)
		}

		// Live-reload label cursor preservation in LabelDashboardModel, AttentionModel, and VelocityComparisonModel.
		ld := NewLabelDashboardModel(m.theme)
		ld.SetSize(100, 20)
		ld.SetData([]analysis.LabelHealth{
			{Label: "alpha", HealthLevel: analysis.HealthLevelWarning, Blocked: 2, Health: 60},
			{Label: "zeta", HealthLevel: analysis.HealthLevelHealthy, Blocked: 0, Health: 95},
		})
		ld.MoveDown() // select "zeta" (index 1)
		if ld.SelectedLabel() != "zeta" {
			t.Fatalf("precondition: expected LabelDashboard SelectedLabel=zeta, got %q", ld.SelectedLabel())
		}
		// Refresh where "zeta" becomes critical (sorts to index 0) and "beta" is inserted: cursor must track "zeta"!
		ld.SetData([]analysis.LabelHealth{
			{Label: "alpha", HealthLevel: analysis.HealthLevelHealthy, Blocked: 0, Health: 95},
			{Label: "beta", HealthLevel: analysis.HealthLevelWarning, Blocked: 1, Health: 70},
			{Label: "zeta", HealthLevel: analysis.HealthLevelCritical, Blocked: 5, Health: 20},
		})
		if ld.SelectedLabel() != "zeta" || ld.cursor != 0 {
			t.Fatalf("LabelDashboard SetData lost selected label 'zeta': got %q at cursor %d", ld.SelectedLabel(), ld.cursor)
		}

		att := NewAttentionModel(m.theme)
		att.SetSize(100, 20)
		att.SetData(analysis.LabelAttentionResult{Labels: []analysis.LabelAttentionScore{
			{Label: "first", AttentionScore: 9.0},
			{Label: "target-lbl", AttentionScore: 5.0},
		}})
		att.MoveDown()
		att.SetData(analysis.LabelAttentionResult{Labels: []analysis.LabelAttentionScore{
			{Label: "new-top", AttentionScore: 10.0},
			{Label: "second", AttentionScore: 8.0},
			{Label: "target-lbl", AttentionScore: 7.0},
		}})
		if att.SelectedLabel() != "target-lbl" || att.Cursor() != 2 {
			t.Fatalf("AttentionModel SetData lost selected label 'target-lbl': got %q at cursor %d", att.SelectedLabel(), att.Cursor())
		}

		vc := NewVelocityComparisonModel(m.theme)
		vc.SetSize(100, 20)
		w1 := refNow.Add(-2 * 24 * time.Hour)
		vc.SetDataAt([]model.Issue{
			{ID: "V1", Status: model.StatusClosed, Labels: []string{"fast"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
			{ID: "V2", Status: model.StatusClosed, Labels: []string{"fast"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
			{ID: "V3", Status: model.StatusClosed, Labels: []string{"steady"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
		}, refNow)
		vc.MoveDown() // select "steady" (index 1)
		if vc.SelectedLabel() != "steady" {
			t.Fatalf("precondition: expected VelocityComparison SelectedLabel=steady, got %q", vc.SelectedLabel())
		}
		vc.SetDataAt([]model.Issue{
			{ID: "V1", Status: model.StatusClosed, Labels: []string{"fast"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
			{ID: "V3", Status: model.StatusClosed, Labels: []string{"steady"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
			{ID: "V4", Status: model.StatusClosed, Labels: []string{"steady"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
			{ID: "V5", Status: model.StatusClosed, Labels: []string{"steady"}, ClosedAt: &w1, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: w1},
		}, refNow)
		if vc.SelectedLabel() != "steady" || vc.cursor != 0 {
			t.Fatalf("VelocityComparison SetDataAt lost selected label 'steady': got %q at cursor %d", vc.SelectedLabel(), vc.cursor)
		}
	})

	t.Run("4_render_debug_view_supports_all_12_views", func(t *testing.T) {
		dbgIssues := []model.Issue{
			{ID: "DBG-1", Title: "Debug Foundation", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, Labels: []string{"core"}, CreatedAt: refNow.Add(-24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour)},
			{
				ID: "DBG-2", Title: "Debug Dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeFeature, Labels: []string{"ui"},
				CreatedAt: refNow.Add(-12 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "DBG-2", DependsOnID: "DBG-1", Type: model.DepBlocks}},
			},
		}
		for _, viewName := range []string{
			"list", "triage", "insights", "board", "history", "graph",
			"tree", "actionable", "sprint", "flow", "attention", "labels",
		} {
			mDbg := NewModel(copyIssues(dbgIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
			t.Cleanup(mDbg.Stop)
			mDbg.SetSprints([]model.Sprint{{ID: "sp-dbg", Name: "Debug Sprint", StartDate: refNow.Add(-24 * time.Hour), EndDate: refNow.Add(24 * time.Hour), BeadIDs: []string{"DBG-1", "DBG-2"}}})
			out := ansi.Strip(mDbg.RenderDebugView(viewName, 120, 32))
			if strings.TrimSpace(out) == "" || strings.HasPrefix(out, "Unknown view:") {
				t.Fatalf("RenderDebugView(%q) failed: %q", viewName, out)
			}
		}

		// Planted negative control: unknown view name still reports "Unknown view: bogus".
		mBogus := NewModel(copyIssues(dbgIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mBogus.Stop)
		if got := mBogus.RenderDebugView("bogus", 120, 32); got != "Unknown view: bogus" {
			t.Fatalf("negative control failed: RenderDebugView(\"bogus\") = %q, want \"Unknown view: bogus\"", got)
		}
	})
}

func TestWave19Round1AuditRemediationsR1B1ToR1B7(t *testing.T) {
	refNow := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	t.Run("R1_B1_closed_to_open_insight_transition_keeps_snapshot_insights_pure", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "CL-ROOT", Title: "Closed Root", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-90 * 24 * time.Hour), UpdatedAt: refNow.Add(-60 * 24 * time.Hour)},
			{
				ID: "CL-BRIDGE", Title: "Closed Historical Bridge", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-85 * 24 * time.Hour), UpdatedAt: refNow.Add(-55 * 24 * time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "CL-BRIDGE", DependsOnID: "CL-ROOT", Type: model.DepBlocks}},
			},
			{
				ID: "CL-LEAF-1", Title: "Closed Leaf 1", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-80 * 24 * time.Hour), UpdatedAt: refNow.Add(-50 * 24 * time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "CL-LEAF-1", DependsOnID: "CL-BRIDGE", Type: model.DepBlocks}},
			},
			{
				ID: "CL-LEAF-2", Title: "Closed Leaf 2", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-75 * 24 * time.Hour), UpdatedAt: refNow.Add(-45 * 24 * time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "CL-LEAF-2", DependsOnID: "CL-BRIDGE", Type: model.DepBlocks}},
			},
			{ID: "OP-ROOT", Title: "Open Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: refNow.Add(-5 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour)},
			{
				ID: "OP-BRIDGE", Title: "Open Active Bridge", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-4 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "OP-BRIDGE", DependsOnID: "OP-ROOT", Type: model.DepBlocks}},
			},
			{
				ID: "OP-LEAF", Title: "Open Leaf", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				CreatedAt: refNow.Add(-3 * 24 * time.Hour), UpdatedAt: refNow.Add(-time.Hour),
				Dependencies: []*model.Dependency{{IssueID: "OP-LEAF", DependsOnID: "OP-BRIDGE", Type: model.DepBlocks}},
			},
		}
		hasBottleneck := func(ins analysis.Insights, id string) bool {
			for _, it := range ins.Bottlenecks {
				if it.ID == id {
					return true
				}
			}
			return false
		}

		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = u.(*Model)

		// Switch to "closed" BEFORE Phase 2 preparation completes!
		m.currentFilter = "closed"
		m.applyFilter()
		prepCmd := m.preparePhase2Cmd()
		if prepCmd == nil {
			t.Fatal("expected preparePhase2Cmd non-nil")
		}
		if msg := prepCmd(); msg != nil {
			u, _ = m.Update(msg)
			m = u.(*Model)
		}

		// Planted negative control: while m.currentFilter == "closed", insightsPanel shows CL-BRIDGE,
		// BUT m.snapshot.GetInsights() MUST remain active-only (not poisoned by transient closed filter!).
		if !hasBottleneck(m.insightsPanel.insights, "CL-BRIDGE") {
			t.Fatalf("expected CL-BRIDGE in insightsPanel while currentFilter==\"closed\", got %+v", m.insightsPanel.insights.Bottlenecks)
		}
		if m.snapshot == nil || hasBottleneck(m.snapshot.GetInsights(), "CL-BRIDGE") {
			t.Fatalf("transient closed filter poisoned DataSnapshot.insights with CL-BRIDGE: %+v", m.snapshot.GetInsights().Bottlenecks)
		}

		// Positive observable: switching back to "open" / "all" immediately drops CL-BRIDGE!
		m.currentFilter = "open"
		m.applyFilter()
		m.rebuildInsightsPanel()
		if hasBottleneck(m.insightsPanel.insights, "CL-BRIDGE") || !hasBottleneck(m.insightsPanel.insights, "OP-BRIDGE") {
			t.Fatalf("switching closed->open leaked CL-BRIDGE or dropped OP-BRIDGE: %+v", m.insightsPanel.insights.Bottlenecks)
		}
	})

	t.Run("R1_B2_B3_prefix_overlap_shared_git_monorepo_per_repo_revision_and_repo_keys", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		gitRun := func(dir string, args ...string) string {
			t.Helper()
			cmd := exec.CommandContext(context.Background(), "git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=Tester",
				"GIT_AUTHOR_EMAIL=tester@example.com",
				"GIT_COMMITTER_NAME=Tester",
				"GIT_COMMITTER_EMAIL=tester@example.com",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
			}
			return strings.TrimSpace(string(out))
		}

		// Part A: Polyrepo with overlapping prefixes ("api-" vs "api-v2-"), per-repo Revision, >10-char hyphenated repo key, and scopeRepoFilter.
		polyRoot := t.TempDir()
		repoAPI := filepath.Join(polyRoot, "api")
		repoAPIV2 := filepath.Join(polyRoot, "api-v2")
		repoAuth := filepath.Join(polyRoot, "authentication-service")
		for _, rDir := range []string{repoAPI, repoAPIV2, repoAuth} {
			if err := os.MkdirAll(filepath.Join(rDir, ".beads"), 0o755); err != nil {
				t.Fatal(err)
			}
			gitRun(rDir, "init")
		}
		gitRun(repoAPI, "remote", "add", "origin", "https://github.com/acme/api-v1.git")
		gitRun(repoAPIV2, "remote", "add", "origin", "https://github.com/acme/api-v2.git")
		gitRun(repoAuth, "remote", "add", "origin", "https://github.com/acme/authentication-service.git")

		if err := os.WriteFile(filepath.Join(repoAPI, ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"V1 route","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoAPI, "v1.go"), []byte("package api\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoAPI, "add", ".")
		gitRun(repoAPI, "commit", "-m", "feat(1): v1 route")
		shaV1 := gitRun(repoAPI, "rev-parse", "HEAD")

		if err := os.WriteFile(filepath.Join(repoAPIV2, ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"V2 route","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoAPIV2, "v2.go"), []byte("package apiv2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoAPIV2, "add", ".")
		gitRun(repoAPIV2, "commit", "-m", "feat(api-v2-1): v2 route pinned")
		revV2Pinned := gitRun(repoAPIV2, "rev-parse", "HEAD")

		// Later commit on api-v2 AFTER revV2Pinned (planted negative control for per-repo Revision).
		if err := os.WriteFile(filepath.Join(repoAPIV2, "v2_future.go"), []byte("package apiv2\nvar future = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoAPIV2, "add", ".")
		gitRun(repoAPIV2, "commit", "-m", "feat(api-v2-1): v2 future commit after pinned rev")
		shaV2Future := gitRun(repoAPIV2, "rev-parse", "HEAD")

		if err := os.WriteFile(filepath.Join(repoAuth, ".beads", "issues.jsonl"), []byte(`{"id":"bv-99","title":"Auth token","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoAuth, "token.go"), []byte("package auth\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(repoAuth, "add", ".")
		gitRun(repoAuth, "commit", "-m", "feat(bv-99): auth token")

		polyIssues := []model.Issue{
			{ID: "api-1", Title: "V1 route", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api"},
			// Deliberately leave SourceRepo empty on api-v2-1 to prove longest-prefix matching ("api-v2-" beats "api-")!
			{ID: "api-v2-1", Title: "V2 route", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
			{ID: "authentication-service-bv-99", Title: "Auth token", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "authentication-service"},
		}
		mPoly := NewModel(copyIssues(polyIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mPoly.Stop)
		mPoly.workDir = polyRoot
		mPoly.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:   true,
			RepoCount: 3,
			Repos: []WorkspaceRepoTarget{
				{Name: "api", Path: repoAPI, BeadsDir: filepath.Join(repoAPI, ".beads"), Prefix: "api-"},
				{Name: "api-v2", Path: repoAPIV2, BeadsDir: filepath.Join(repoAPIV2, ".beads"), Prefix: "api-v2-", Revision: revV2Pinned},
				{Name: "authentication-service", Path: repoAuth, BeadsDir: filepath.Join(repoAuth, ".beads"), Prefix: "authentication-service-"},
			},
		})
		if cmd := mPoly.startHistoryLoad(); cmd != nil {
			if msg := cmd(); msg != nil {
				u, _ := mPoly.Update(msg)
				mPoly = u.(*Model)
			}
		}
		rep := mPoly.historyFullReport
		if rep == nil {
			t.Fatal("expected non-nil polyrepo history report")
		}
		hV2, okV2 := rep.Histories["api-v2-1"]
		if !okV2 || len(hV2.Commits) == 0 {
			t.Fatalf("api-v2-1 was hijacked by api- or failed correlation: %+v", rep.Histories)
		}
		sawPinned, sawFuture := false, false
		for _, cc := range hV2.Commits {
			if cc.SHA == revV2Pinned {
				sawPinned = true
			}
			if cc.SHA == shaV2Future {
				sawFuture = true
			}
		}
		if !sawPinned || sawFuture {
			t.Fatalf("per-repo Revision failed: sawPinned=%v sawFuture=%v commits=%+v", sawPinned, sawFuture, hV2.Commits)
		}

		// Hyphenated >10-char repo filter ("authentication-service") via m.historyBeadRepoKeys:
		mPoly.activeRepos = map[string]bool{"authentication-service": true}
		mPoly.syncSecondaryViewsForRepoScope(focusHistory)
		if _, ok := mPoly.historyView.report.Histories["authentication-service-bv-99"]; !ok {
			t.Fatalf("hyphenated >10-char repo key dropped authentication-service-bv-99: %+v", mPoly.historyView.report.Histories)
		}
		if _, leaked := mPoly.historyView.report.Histories["api-1"]; leaked {
			t.Fatalf("repo filter leaked api-1 into authentication-service scope")
		}
		mPoly.activeRepos = nil

		// Deterministic resolveCommitRepoDir across 20 calls:
		firstDir := mPoly.resolveCommitRepoDir(shaV1[:7])
		if firstDir == "" {
			t.Fatal("expected non-empty resolveCommitRepoDir")
		}
		for range 20 {
			if got := mPoly.resolveCommitRepoDir(shaV1[:7]); got != firstDir {
				t.Fatalf("non-deterministic resolveCommitRepoDir: %q vs %q", got, firstDir)
			}
		}

		// scopeRepoFilter="api" walks ONLY api, skipping api-v2 and authentication-service:
		mScoped := NewModel(copyIssues(polyIssues), nil, "", ReadinessScope{ReferenceNow: refNow, RepoFilter: "api"})
		t.Cleanup(mScoped.Stop)
		mScoped.workDir = polyRoot
		mScoped.EnableWorkspaceMode(mPoly.workspaceInfo)
		if cmd := mScoped.startHistoryLoad(); cmd != nil {
			if msg := cmd(); msg != nil {
				u, _ := mScoped.Update(msg)
				mScoped = u.(*Model)
			}
		}
		if _, leakedV2 := mScoped.historyFullReport.Histories["api-v2-1"]; leakedV2 {
			t.Fatalf("scopeRepoFilter=\"api\" walked api-v2: %+v", mScoped.historyFullReport.Histories)
		}

		// Part B: Shared-.git monorepo (/mono/.git with services/api/.beads + services/web/.beads and colliding local ID "bd-1").
		monoRoot := t.TempDir()
		monoAPI := filepath.Join(monoRoot, "services", "api")
		monoWeb := filepath.Join(monoRoot, "services", "web")
		if err := os.MkdirAll(filepath.Join(monoAPI, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(monoWeb, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(monoRoot, "init")
		if err := os.WriteFile(filepath.Join(monoAPI, ".beads", "issues.jsonl"), []byte(`{"id":"bd-1","title":"API auth","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(monoWeb, ".beads", "issues.jsonl"), []byte(`{"id":"bd-1","title":"Web UI","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(monoRoot, "add", ".")
		gitRun(monoRoot, "commit", "-m", "chore: seed monorepo stores")

		if err := os.WriteFile(filepath.Join(monoAPI, "auth.go"), []byte("package api\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(monoRoot, "add", ".")
		gitRun(monoRoot, "commit", "-m", "feat(bd-1): api auth implementation")
		shaMonoAPI := gitRun(monoRoot, "rev-parse", "HEAD")

		if err := os.WriteFile(filepath.Join(monoWeb, "ui.tsx"), []byte("export const x = 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(monoRoot, "add", ".")
		gitRun(monoRoot, "commit", "-m", "feat(bd-1): web ui implementation")
		shaMonoWeb := gitRun(monoRoot, "rev-parse", "HEAD")

		monoIssues := []model.Issue{
			{ID: "api-bd-1", Title: "API auth", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api", Origin: &model.IssueOrigin{LocalID: "bd-1", WorkingDirectory: monoAPI, TrackerDirectory: filepath.Join(monoAPI, ".beads")}},
			{ID: "web-bd-1", Title: "Web UI", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web", Origin: &model.IssueOrigin{LocalID: "bd-1", WorkingDirectory: monoWeb, TrackerDirectory: filepath.Join(monoWeb, ".beads")}},
		}
		mMono := NewModel(copyIssues(monoIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mMono.Stop)
		mMono.workDir = monoRoot
		mMono.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:   true,
			RepoCount: 2,
			Repos: []WorkspaceRepoTarget{
				{Name: "api", Path: monoAPI, BeadsDir: filepath.Join(monoAPI, ".beads"), Prefix: "api-"},
				{Name: "web", Path: monoWeb, BeadsDir: filepath.Join(monoWeb, ".beads"), Prefix: "web-"},
			},
		})
		if cmd := mMono.startHistoryLoad(); cmd != nil {
			if msg := cmd(); msg != nil {
				u, _ := mMono.Update(msg)
				mMono = u.(*Model)
			}
		}
		monoRep := mMono.historyFullReport
		if monoRep == nil || monoRep.Window == nil || monoRep.Window.Commits != 3 {
			t.Fatalf("shared-.git monorepo double-counted Window.Commits: got %+v, want 3", monoRep.Window)
		}
		apiHist := monoRep.Histories["api-bd-1"]
		foundAPIOnly, leakedWeb, badDoublePrefix := false, false, false
		for _, cc := range apiHist.Commits {
			if cc.SHA == shaMonoAPI {
				foundAPIOnly = true
				for _, f := range cc.Files {
					if f.Path != "services/api/auth.go" {
						badDoublePrefix = true
					}
				}
			}
			if cc.SHA == shaMonoWeb {
				leakedWeb = true
			}
		}
		if !foundAPIOnly || leakedWeb || badDoublePrefix {
			t.Fatalf("shared-.git monorepo disambiguation/path failed: foundAPI=%v leakedWeb=%v badPrefix=%v commits=%+v",
				foundAPIOnly, leakedWeb, badDoublePrefix, apiHist.Commits)
		}
	})

	t.Run("R1_B6_full_model_view_80x24_and_80x26_tutorial_toc_and_cass_modal_borders", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "MODAL-1", Title: "Modal Fit Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		for _, termH := range []int{24, 26} {
			m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
			t.Cleanup(m.Stop)
			u, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: termH})
			m = u.(*Model)

			// 1. Tutorial TOC at 80x24 / 80x26 through full m.View():
			m.openContextTutorialWithContext(focusList, "list")
			m.tutorialModel.SetContextMode(false)
			m.tutorialModel, _ = m.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			m.tutorialModel, _ = m.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
			m.tutorialModel, _ = m.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.tutorialModel, _ = m.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			tutFull := ansi.Strip(m.View())
			if gotH := lipgloss.Height(tutFull); gotH != termH {
				t.Fatalf("80x%d Tutorial m.View() height = %d, want %d:\n%s", termH, gotH, termH, tutFull)
			}
			if !strings.Contains(tutFull, "╭") || !strings.Contains(tutFull, "╰") || !strings.Contains(tutFull, "Page 30/30") {
				t.Fatalf("80x%d Tutorial m.View() clipped border or page 30:\n%s", termH, tutFull)
			}
			m.closeTutorial()

			// 2. CassSessionModal with 5 multi-line sessions + long --workspace path at 80x24 / 80x26 through full m.View():
			var sessions []cass.ScoredResult
			for i := 1; i <= 5; i++ {
				sessions = append(sessions, cass.ScoredResult{
					SearchResult: cass.SearchResult{
						Agent:     fmt.Sprintf("claude-%d", i),
						Timestamp: refNow.Add(-time.Duration(i) * time.Hour),
						Snippet:   "Line one of session preview\nLine two of session preview\nLine three of session preview",
					},
					Strategy: cass.StrategyKeywords,
					Keywords: []string{"authentication", "workspace", "correlation", "modal-overflow-guard"},
				})
			}
			m.cassModal = NewCassSessionModalWithOptions("MODAL-1", cass.CorrelationResult{BeadID: "MODAL-1", TopSessions: sessions}, m.theme, CassSessionModalOptions{
				Workspace:    "/very/long/workspace/directory/path/that/would/otherwise/wrap/search/cmd",
				ReferenceNow: refNow,
			})
			m.cassModal.SetSize(m.width, m.height)
			m.showCassModal = true
			m.focused = focusCassModal
			for range 4 {
				m.cassModal, _ = m.cassModal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
			}
			cassFull := ansi.Strip(m.View())
			if gotH := lipgloss.Height(cassFull); gotH != termH {
				t.Fatalf("80x%d CassSessionModal m.View() height = %d, want %d:\n%s", termH, gotH, termH, cassFull)
			}
			if !strings.Contains(cassFull, "[5]") || !strings.Contains(cassFull, "╭") || !strings.Contains(cassFull, "╰") {
				t.Fatalf("80x%d CassSessionModal m.View() clipped session #5 or modal border:\n%s", termH, cassFull)
			}
		}
	})

	t.Run("R1_B7_render_debug_view_mode_reset_active_recipe_graph_and_tiny_10x1_sprint", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "REC-OPEN", Title: "Recipe Visible Open", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "REC-CLOSED", Title: "Excluded Closed Issue", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		openOnly := &recipe.Recipe{
			Name:    "open-only",
			Filters: recipe.FilterConfig{Status: []string{"open"}},
		}
		m := NewModel(copyIssues(issues), openOnly, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.SetSprints([]model.Sprint{{ID: "sp-tiny", Name: "Tiny Sprint", StartDate: refNow.Add(-24 * time.Hour), EndDate: refNow.Add(24 * time.Hour), BeadIDs: []string{"REC-OPEN"}}})

		// Dirty competing view booleans + user split-view override before RenderDebugView:
		m.isBoardView = true
		m.isSprintView = true
		m.showVelocityComparison = true
		m.splitViewUserOverride = true
		m.splitViewUserWant = false

		graphOut := ansi.Strip(m.RenderDebugView("graph", 140, 30))
		if m.isBoardView || m.isSprintView || m.showVelocityComparison || !m.isGraphView {
			t.Fatalf("RenderDebugView(\"graph\") failed to reset competing view flags: board=%v sprint=%v vel=%v graph=%v",
				m.isBoardView, m.isSprintView, m.showVelocityComparison, m.isGraphView)
		}
		if !strings.Contains(graphOut, "REC-OPEN") || strings.Contains(graphOut, "REC-CLOSED") {
			t.Fatalf("RenderDebugView(\"graph\") ignored active recipe open-only:\n%s", graphOut)
		}

		_ = m.RenderDebugView("triage", 140, 30)
		if m.isGraphView || m.isSplitView {
			t.Fatalf("RenderDebugView(\"triage\") failed to clear isGraphView (%v) or honor splitViewUserOverride (%v)", m.isGraphView, m.isSplitView)
		}

		// Tiny 10x1 sprint render must not panic or pass negative height to Lipgloss.
		tinySprint := m.RenderDebugView("sprint", 10, 1)
		if strings.TrimSpace(ansi.Strip(tinySprint)) == "" {
			t.Fatalf("RenderDebugView(\"sprint\", 10, 1) returned empty output")
		}
	})
}

func TestWave19Round2AuditRemediationsR2B1ToR2B3(t *testing.T) {
	refNow := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	t.Run("R2_B1_sqlite_metadata_deleted_subrepo_nongit_sibling_and_date_as_of_revision", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		monoRoot := t.TempDir()
		nonGitDir := t.TempDir() // Outside any git repository -> resolveGitRepoRoot MUST return ""
		dbOnlyDir := filepath.Join(monoRoot, "services", "dbonly")
		legacyDir := filepath.Join(monoRoot, "legacy") // Never created on disk: simulates subrepo deleted before HEAD!
		if err := os.MkdirAll(filepath.Join(dbOnlyDir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(nonGitDir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}

		gitExecIn := func(dateISO string, stdin string, args ...string) string {
			t.Helper()
			cmd := exec.CommandContext(context.Background(), "git", args...)
			cmd.Dir = monoRoot
			if stdin != "" {
				cmd.Stdin = strings.NewReader(stdin)
			}
			env := append(os.Environ(),
				"GIT_AUTHOR_NAME=Tester",
				"GIT_AUTHOR_EMAIL=tester@example.com",
				"GIT_COMMITTER_NAME=Tester",
				"GIT_COMMITTER_EMAIL=tester@example.com",
			)
			if dateISO != "" {
				env = append(env, "GIT_AUTHOR_DATE="+dateISO, "GIT_COMMITTER_DATE="+dateISO)
			}
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
			}
			return strings.TrimSpace(string(out))
		}
		stageIndexContent := func(relPath, content string) {
			t.Helper()
			blob := gitExecIn("", content, "hash-object", "-w", "--stdin")
			gitExecIn("", "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+relPath)
		}
		commitIndexAt := func(dateISO, msg string) string {
			t.Helper()
			gitExecIn(dateISO, "", "commit", "-m", msg)
			return gitExecIn("", "", "rev-parse", "HEAD")
		}

		gitExecIn("", "", "init")

		// Seed dbonly (SQLite-only on disk: metadata.json + beads.db on disk, custom_export.jsonl ONLY in git history!)
		// and legacy (deleted depth-1 subrepo: legacy/.beads/issues.jsonl ONLY in git history, never on disk!).
		if err := os.WriteFile(filepath.Join(dbOnlyDir, ".beads", "metadata.json"), []byte(`{"backend":"sqlite","database":"beads.db","jsonl_export":"custom_export.jsonl"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dbOnlyDir, ".beads", "beads.db"), []byte("SQLite format 3\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExecIn("2026-01-05T10:00:00Z", "", "add", "services/dbonly/.beads/metadata.json")
		stageIndexContent("services/dbonly/.beads/custom_export.jsonl", `{"id":"bd-1","title":"DBOnly service","status":"open","priority":1,"issue_type":"task"}`+"\n")
		stageIndexContent("legacy/.beads/issues.jsonl", `{"id":"bd-1","title":"Legacy depth-1 subrepo","status":"open","priority":1,"issue_type":"task"}`+"\n")
		commitIndexAt("2026-01-05T10:00:00Z", "chore: seed dbonly and legacy stores")

		// 2026-01-10: One code commit in services/dbonly/store.go and one in legacy/old.go, both mentioning colliding "bd-1".
		stageIndexContent("services/dbonly/store.go", "package dbonly\n")
		shaDBOnlyJan10 := commitIndexAt("2026-01-10T12:00:00Z", "feat(bd-1): dbonly persistence")

		stageIndexContent("legacy/old.go", "package legacy\n")
		shaLegacyJan10 := commitIndexAt("2026-01-10T14:00:00Z", "feat(bd-1): legacy handler")

		// 2026-02-01 (AFTER 2026-01-15 as-of date!): legacy removed from HEAD index and future commit added on dbonly.
		gitExecIn("2026-02-01T12:00:00Z", "", "update-index", "--force-remove", "legacy/.beads/issues.jsonl", "legacy/old.go")
		stageIndexContent("services/dbonly/future.go", "package dbonly\nvar feb = true\n")
		shaFebFuture := commitIndexAt("2026-02-01T12:00:00Z", "feat(bd-1): february future change")

		// Sanity preconditions: neither custom_export.jsonl nor legacyDir exists on disk!
		if _, err := os.Stat(filepath.Join(dbOnlyDir, ".beads", "custom_export.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("precondition failed: custom_export.jsonl should not exist on disk")
		}
		if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
			t.Fatalf("precondition failed: legacyDir should not exist on disk")
		}

		// Verify resolveHistoryCorrelationPath on a non-existent deleted subrepo .beads dir AND on SQLite metadata.json dir returns a .jsonl file path (NEVER a directory):
		ghostBeadsDir := filepath.Join(monoRoot, "ghost", ".beads")
		if gotGhost := resolveHistoryCorrelationPath(ghostBeadsDir, filepath.Join(monoRoot, "ghost")); gotGhost != filepath.Join(ghostBeadsDir, "issues.jsonl") {
			t.Fatalf("resolveHistoryCorrelationPath on deleted subrepo dir = %q, want %q", gotGhost, filepath.Join(ghostBeadsDir, "issues.jsonl"))
		}
		sqlOnlyDir := t.TempDir()
		sqlBeads := filepath.Join(sqlOnlyDir, ".beads")
		if err := os.MkdirAll(sqlBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sqlBeads, "metadata.json"), []byte(`{"backend":"sqlite","database":"beads.db","jsonl_export":"exported_issues.jsonl"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sqlBeads, "beads.db"), []byte("SQLite format 3\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		if gotSQL := resolveHistoryCorrelationPath(filepath.Join(sqlBeads, "beads.db"), sqlOnlyDir); gotSQL != filepath.Join(sqlBeads, "exported_issues.jsonl") {
			t.Fatalf("resolveHistoryCorrelationPath with metadata.json jsonl_export = %q, want %q", gotSQL, filepath.Join(sqlBeads, "exported_issues.jsonl"))
		}

		wsIssues := []model.Issue{
			{ID: "dbonly-bd-1", Title: "DBOnly service", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "dbonly"},
			{ID: "legacy-bd-1", Title: "Legacy depth-1 subrepo", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "legacy"},
			{ID: "nongit-bd-9", Title: "Scratch non-git note", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, SourceRepo: "nongit"},
		}
		m := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.workDir = monoRoot
		m.SetAsOfRevision("2026-01-15")
		m.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:   true,
			RepoCount: 3,
			Repos: []WorkspaceRepoTarget{
				{Name: "dbonly", Path: dbOnlyDir, BeadsDir: filepath.Join(dbOnlyDir, ".beads", "beads.db"), Prefix: "dbonly-"},
				{Name: "legacy", Path: legacyDir, BeadsDir: filepath.Join(legacyDir, ".beads"), Prefix: "legacy-"},
				{Name: "nongit", Path: nonGitDir, BeadsDir: filepath.Join(nonGitDir, ".beads"), Prefix: "nongit-"},
			},
		})
		if cmd := m.startHistoryLoad(); cmd != nil {
			if msg := cmd(); msg != nil {
				u, _ := m.Update(msg)
				m = u.(*Model)
			}
		}
		rep := m.historyFullReport
		if rep == nil {
			t.Fatalf("expected history report with date SetAsOfRevision(\"2026-01-15\"), status=%q", m.statusMsg)
		}

		// Positive observables:
		// 1. Date revision "2026-01-15" resolved to 2026-01-10 commit (including shaDBOnlyJan10 and shaLegacyJan10), excluding shaFebFuture!
		// 2. Non-git sibling target did NOT inflate distinctRoots from 1 to 2, so file paths stay repo-root-relative ("services/dbonly/store.go" and "legacy/old.go").
		// 3. Depth-1 "legacy" subtreePrefix ("legacy/") disambiguated colliding "bd-1" from "dbonly".
		dbHist := rep.Histories["dbonly-bd-1"]
		sawDBJan10, sawFebOnDB, badDBPath := false, false, false
		for _, cc := range dbHist.Commits {
			if cc.SHA == shaDBOnlyJan10 {
				sawDBJan10 = true
				for _, f := range cc.Files {
					if f.Path != "services/dbonly/store.go" {
						badDBPath = true
					}
				}
			}
			if cc.SHA == shaFebFuture || cc.SHA == shaLegacyJan10 {
				sawFebOnDB = true
			}
		}
		if !sawDBJan10 || sawFebOnDB || badDBPath {
			t.Fatalf("dbonly correlation failed: sawJan10=%v sawFebOrLegacy=%v badPath=%v commits=%+v", sawDBJan10, sawFebOnDB, badDBPath, dbHist.Commits)
		}

		legHist := rep.Histories["legacy-bd-1"]
		sawLegJan10, leakedDBOntoLeg, badLegPath := false, false, false
		for _, cc := range legHist.Commits {
			if cc.SHA == shaLegacyJan10 {
				sawLegJan10 = true
				for _, f := range cc.Files {
					if f.Path != "legacy/old.go" {
						badLegPath = true
					}
				}
			}
			if cc.SHA == shaDBOnlyJan10 || cc.SHA == shaFebFuture {
				leakedDBOntoLeg = true
			}
		}
		if !sawLegJan10 || leakedDBOntoLeg || badLegPath {
			t.Fatalf("legacy depth-1 correlation failed: sawLeg=%v leaked=%v badPath=%v commits=%+v", sawLegJan10, leakedDBOntoLeg, badLegPath, legHist.Commits)
		}
	})

	t.Run("R2_B2_narrow_40x24_toc_verbose_80x32_35_36_cass_and_ultra_tiny_10x5_bounds", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "FIT-1", Title: "Boundary Fit Issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}

		// 1. 40x24 Tutorial with tocVisible=true (both TOC focus and Content focus):
		m40 := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m40.Stop)
		u, _ := m40.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
		m40 = u.(*Model)
		m40.openFullTutorialFromFocus(focusList)
		m40.tutorialModel, _ = m40.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // tocVisible=true, focus=TOC
		tocFocusedOut := ansi.Strip(m40.View())
		if gotW, gotH := lipgloss.Width(tocFocusedOut), lipgloss.Height(tocFocusedOut); gotW > 40 || gotH != 24 {
			t.Fatalf("40x24 TOC-focused m.View() = %dx%d, want <=40x24:\n%s", gotW, gotH, tocFocusedOut)
		}
		if !strings.Contains(tocFocusedOut, "╭") || !strings.Contains(tocFocusedOut, "╰") || !strings.Contains(tocFocusedOut, "Contents") {
			t.Fatalf("40x24 TOC-focused Tutorial clipped border or TOC:\n%s", tocFocusedOut)
		}
		m40.tutorialModel, _ = m40.tutorialModel.Update(tea.KeyMsg{Type: tea.KeyTab}) // tocVisible=true, focus=Content
		contentFocusedOut := ansi.Strip(m40.View())
		if gotW, gotH := lipgloss.Width(contentFocusedOut), lipgloss.Height(contentFocusedOut); gotW > 40 || gotH != 24 {
			t.Fatalf("40x24 Content-focused (tocVisible=true) m.View() = %dx%d, want <=40x24:\n%s", gotW, gotH, contentFocusedOut)
		}
		if !strings.Contains(contentFocusedOut, "╭") || !strings.Contains(contentFocusedOut, "╰") {
			t.Fatalf("40x24 Content-focused Tutorial clipped border:\n%s", contentFocusedOut)
		}

		// 2. 80x32, 80x35, and 80x36 CassSessionModal with 5 verbose sessions:
		var verboseSessions []cass.ScoredResult
		for i := 1; i <= 5; i++ {
			verboseSessions = append(verboseSessions, cass.ScoredResult{
				SearchResult: cass.SearchResult{
					Agent:     fmt.Sprintf("claude-opus-%d", i),
					Timestamp: refNow.Add(-time.Duration(i) * time.Hour),
					Snippet:   "First verbose preview line with details\nSecond verbose preview line with details\nThird verbose preview line with details",
				},
				Strategy: cass.StrategyKeywords,
				Keywords: []string{"workspace-correlation", "monorepo-subtree", "sliding-window-modal", "overflow-protection", "extra-long-keyword-token"},
			})
		}
		for _, h := range []int{32, 35, 36} {
			mCass := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
			t.Cleanup(mCass.Stop)
			u, _ = mCass.Update(tea.WindowSizeMsg{Width: 80, Height: h})
			mCass = u.(*Model)
			mCass.cassModal = NewCassSessionModalWithOptions("FIT-1", cass.CorrelationResult{BeadID: "FIT-1", TopSessions: verboseSessions}, mCass.theme, CassSessionModalOptions{
				Workspace:    "/very/deep/polyrepo/workspace/directory/path/for/cass/command/wrapping/check",
				ReferenceNow: refNow,
			})
			mCass.cassModal.SetSize(80, h)
			directModal := ansi.Strip(mCass.cassModal.View())
			if dmW, dmH := lipgloss.Width(directModal), lipgloss.Height(directModal); dmW > 80 || dmH > h || !strings.Contains(directModal, "╭") || !strings.Contains(directModal, "╰") {
				t.Fatalf("direct CassSessionModal.View() at 80x%d = %dx%d clipped border:\n%s", h, dmW, dmH, directModal)
			}
			mCass.showCassModal = true
			mCass.focused = focusCassModal
			fullCass := ansi.Strip(mCass.View())
			if fcW, fcH := lipgloss.Width(fullCass), lipgloss.Height(fullCass); fcW > 80 || fcH != h || !strings.Contains(fullCass, "╭") || !strings.Contains(fullCass, "╰") {
				t.Fatalf("full m.View() CassSessionModal at 80x%d = %dx%d clipped border:\n%s", h, fcW, fcH, fullCass)
			}
		}

		// 3. 10x5 ultra-tiny TutorialModel (normal + empty-state) and CassSessionModal:
		tutTiny := NewTutorialModel(DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		tutTiny.SetSize(10, 5)
		tinyTutView := ansi.Strip(tutTiny.View())
		if w, h := lipgloss.Width(tinyTutView), lipgloss.Height(tinyTutView); w > 10 || h > 5 {
			t.Fatalf("10x5 TutorialModel.View() = %dx%d, want <=10x5:\n%q", w, h, tinyTutView)
		}
		tutTiny.pages = nil
		tinyEmptyView := ansi.Strip(tutTiny.View())
		if w, h := lipgloss.Width(tinyEmptyView), lipgloss.Height(tinyEmptyView); w > 10 || h > 5 {
			t.Fatalf("10x5 TutorialModel empty state = %dx%d, want <=10x5:\n%q", w, h, tinyEmptyView)
		}

		cassTiny := NewCassSessionModalWithOptions("FIT-1", cass.CorrelationResult{BeadID: "FIT-1", TopSessions: verboseSessions}, DefaultTheme(lipgloss.NewRenderer(os.Stdout)), CassSessionModalOptions{
			Workspace:    "/very/long/path",
			ReferenceNow: refNow,
		})
		cassTiny.SetSize(10, 5)
		tinyCassView := ansi.Strip(cassTiny.View())
		if w, h := lipgloss.Width(tinyCassView), lipgloss.Height(tinyCassView); w > 10 || h > 5 {
			t.Fatalf("10x5 CassSessionModal.View() = %dx%d, want <=10x5:\n%q", w, h, tinyCassView)
		}
	})

	t.Run("R2_B3_board_H_L_and_search_n_collapse_hidden_expanded_card_so_esc_exits_cleanly", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "B-OPEN", Title: "Match Open", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "B-CLOSED", Title: "Match Closed", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = u.(*Model)

		// Enter Board ('b'), expand B-OPEN ('d'), press 'L' to jump to Closed column -> Esc must exit Board immediately (not swallowed by hidden expandedCardID!).
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
		m = u.(*Model)
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
		m = u.(*Model)
		if !m.board.HasExpandedCard() {
			t.Fatal("precondition: expected B-OPEN expanded")
		}
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
		m = u.(*Model)
		if m.board.HasExpandedCard() {
			t.Fatal("expected 'L' column jump to collapse hidden expanded card")
		}
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = u.(*Model)
		if m.isBoardView {
			t.Fatal("stale expandedCardID swallowed Esc after 'L' column jump")
		}
	})
}

func TestWave26_AlertsSuggestionsCompactFit_LazyRepoScope_RootBlockers_Capacity_AndFeedback(t *testing.T) {
	refNow := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	t.Run("A9_3_SuggestionsTab_CompactAndNarrow_NeverClipsFooter", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "S-1", Title: "First\nmultiline title", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow},
		}
		m := NewModel(issues, nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.showAlertsPanel = true
		m.alertsTab = 1
		m.suggestionsCacheLoaded = true
		m.suggestionsCache = []analysis.Suggestion{
			{
				Type:          analysis.SuggestionMissingDependency,
				Confidence:    0.92,
				Summary:       "First line\nSecond line\r\nThird line of a very long hygiene summary",
				Reason:        "Reason line 1\nReason line 2",
				TargetBead:    "S-1",
				RelatedBead:   "S-2",
				ActionCommand: "br dep add S-1 S-2 --note 'multiline\ncmd'",
			},
			{
				Type:       analysis.SuggestionPotentialDuplicate,
				Confidence: 0.85,
				Summary:    "Second suggestion",
				TargetBead: "S-1",
			},
		}
		for _, sz := range [][2]int{{80, 18}, {80, 16}, {36, 24}, {36, 18}} {
			m.suggestionsCursor = 1
			u, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m = u.(*Model)
			out := ansi.Strip(m.renderAlertsPanel())
			if !strings.Contains(out, "Esc: close") {
				t.Fatalf("A9-3/A10-P1-5 regression at %dx%d: Suggestions tab clipped footer 'Esc: close':\n%s", sz[0], sz[1], out)
			}
		}
	})

	t.Run("A9_4_RepoScopeToggle_KeepsSuggestionsLazyWhenAlertsRemain", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "R1-1", Title: "Repo 1 item", Status: model.StatusOpen, Priority: 1, SourceRepo: "repo1", CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "R2-1", Title: "Repo 2 item", Status: model.StatusOpen, Priority: 1, SourceRepo: "repo2", CreatedAt: refNow, UpdatedAt: refNow},
		}
		m := NewModel(issues, nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.workspaceMode = true
		m.availableRepos = []string{"repo1", "repo2"}
		m.alerts = []drift.Alert{{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "R1-1", Message: "stale"}}
		m.showAlertsPanel = true
		m.alertsTab = 0
		m.suggestionsCacheLoaded = false
		m.activeRepos = map[string]bool{"repo1": true}
		m.syncSecondaryViewsForRepoScope(focusList)
		if m.suggestionsCacheLoaded {
			t.Fatal("A9-4 regression: syncSecondaryViewsForRepoScope eagerly loaded suggestions while Alerts tab had remaining alerts")
		}
	})

	t.Run("W26_3_And_W26_4_RootBlockers_AndGlobalCapacity", func(t *testing.T) {
		est := 120
		issues := []model.Issue{
			{ID: "RB-ROOT", Title: "Actionable root blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, EstimatedMinutes: &est, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "RB-MID", Title: "Middle blocked item", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, EstimatedMinutes: &est,
				Dependencies: []*model.Dependency{{IssueID: "RB-MID", DependsOnID: "RB-ROOT", Type: model.DepBlocks}}, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "RB-LEAF", Title: "Leaf blocked item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, EstimatedMinutes: &est,
				Dependencies: []*model.Dependency{{IssueID: "RB-LEAF", DependsOnID: "RB-MID", Type: model.DepBlocks}}, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "RB-CLOSED", Title: "Closed item", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{{IssueID: "RB-CLOSED", DependsOnID: "RB-MID", Type: model.DepBlocks}}, CreatedAt: refNow, UpdatedAt: refNow},
		}
		m := NewModel(issues, nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
		m = u.(*Model)

		selectByID := func(mod *Model, id string) {
			t.Helper()
			for i, it := range mod.list.Items() {
				if ii, ok := it.(IssueItem); ok && ii.Issue.ID == id {
					mod.list.Select(i)
					return
				}
			}
			t.Fatalf("issue %s not found in list", id)
		}

		// DetailPane on RB-LEAF must show "Start Here: Root Blockers" pointing at RB-ROOT.
		selectByID(m, "RB-LEAF")
		m.updateViewportContent()
		leafPlain := ansi.Strip(m.viewportContent)
		if !strings.Contains(leafPlain, "Start Here: Root Blockers") || !strings.Contains(leafPlain, "RB-ROOT") {
			t.Fatalf("W26-3 regression: DetailPane for blocked RB-LEAF missing Start Here root blocker RB-ROOT:\n%s", leafPlain)
		}

		// DetailPane on closed RB-CLOSED must NOT show Start Here: Root Blockers.
		selectByID(m, "RB-CLOSED")
		m.updateViewportContent()
		closedPlain := ansi.Strip(m.viewportContent)
		if strings.Contains(closedPlain, "Start Here: Root Blockers") {
			t.Fatalf("W26-3 regression: closed RB-CLOSED should suppress Start Here: Root Blockers:\n%s", closedPlain)
		}

		// GraphView inspector on RB-LEAF must surface Start here root blocker RB-ROOT.
		m.refreshBoardAndGraphForCurrentFilter()
		m.graphView.SelectByID("RB-LEAF")
		graphDetail := ansi.Strip(m.graphView.renderMetricsPanel("RB-LEAF", 60, m.theme))
		if !strings.Contains(graphDetail, "Start here") || !strings.Contains(graphDetail, "RB-ROOT") {
			t.Fatalf("W26-3 regression: GraphView inspector missing Start here root blocker RB-ROOT:\n%s", graphDetail)
		}

		// Insights PanelPriority must render global Backlog Capacity (Graham Bounds) without sprints.jsonl.
		m.focused = focusInsights
		m.insightsPanel.focusedPanel = PanelPriority
		m.rebuildInsightsPanel()
		m.insightsPanel.updateDetailContent()
		prioPlain := ansi.Strip(m.insightsPanel.detailContent)
		if !strings.Contains(prioPlain, "Backlog Capacity (Graham Bounds)") || !strings.Contains(prioPlain, "Makespan") {
			t.Fatalf("W26-4 regression: Insights PanelPriority missing global Backlog Capacity:\n%s", prioPlain)
		}

		// A10-P1-2: computeGlobalCapacitySummary must exclude non-candidate context neighbors.
		m.candidateIDs = map[string]bool{"RB-ROOT": true}
		capCand := m.computeGlobalCapacitySummary(m.issues, nil)
		if capCand == nil || capCand.OpenCount != 1 {
			t.Fatalf("A10-P1-2 regression: expected candidate-scoped OpenCount=1, got %+v", capCand)
		}
		m.candidateIDs = nil
	})

	t.Run("W26_5_InteractiveFeedback_PersistsAdaptsAndRespectsGuards", func(t *testing.T) {
		t.Setenv("BV_NO_SAVED_CONFIG", "") // TestMain disables persistence; opt back in for this test
		tmp := t.TempDir()
		beadsDir := filepath.Join(tmp, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsFile := filepath.Join(beadsDir, "issues.jsonl")
		issues := []model.Issue{
			{ID: "FB-1", Title: "High priority item", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "FB-2", Title: "Secondary item", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		m := NewModel(issues, nil, beadsFile, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		u, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
		m = u.(*Model)
		selectByID := func(mod *Model, id string) {
			t.Helper()
			for i, it := range mod.list.Items() {
				if ii, ok := it.(IssueItem); ok && ii.Issue.ID == id {
					mod.list.Select(i)
					return
				}
			}
			t.Fatalf("issue %s not found in list", id)
		}
		selectByID(m, "FB-1")

		// Press '+' twice (collecting 2/3) then '-' once (3 events -> active!).
		for _, k := range []string{"+", "+", "-"} {
			u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
			m = u.(*Model)
		}
		fb, err := analysis.LoadFeedback(beadsDir)
		if err != nil {
			t.Fatalf("LoadFeedback failed: %v", err)
		}
		if len(fb.Events) != 3 || fb.Stats.TotalAccepted != 2 || fb.Stats.TotalIgnored != 1 || !fb.Applies() {
			t.Fatalf("W26-5 regression: expected 3 persisted feedback events (2 accept, 1 ignore, applied=true), got %+v", fb)
		}
		if !strings.Contains(m.statusMsg, "adaptive weights active") {
			t.Fatalf("expected active adaptive weights toast, got %q", m.statusMsg)
		}

		// Guard 1: historical git:HEAD~1 beadsPath must refuse without writing.
		mHist := NewModel(copyIssues(issues), nil, "git:HEAD~1", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mHist.Stop)
		mHist.analysis.WaitForPhase2()
		selectByID(mHist, "FB-1")
		u, _ = mHist.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
		mHist = u.(*Model)
		if !strings.Contains(mHist.statusMsg, "historical") {
			t.Fatalf("expected historical refusal message, got %q", mHist.statusMsg)
		}

		// Guard 3 (A10-P1-6): Insights 2D heatmap without drilldown must NOT silently record feedback.
		m.focused = focusInsights
		m.insightsPanel.focusedPanel = PanelPriority
		m.insightsPanel.showHeatmap = true
		m.insightsPanel.heatmapDrill = false
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
		m = u.(*Model)
		fbAfterHeatmap, _ := analysis.LoadFeedback(beadsDir)
		if len(fbAfterHeatmap.Events) != 3 {
			t.Fatalf("A10-P1-6 regression: 2D heatmap '+' recorded feedback on hidden issue (events=%d)", len(fbAfterHeatmap.Events))
		}

		// Guard 4 (A10-P2-4): standalone explicit --db outside .beads must refuse without writing.
		standaloneDB := filepath.Join(t.TempDir(), "custom.db")
		mDB := NewModel(copyIssues(issues), nil, standaloneDB, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mDB.Stop)
		mDB.isExplicitDB = true
		mDB.analysis.WaitForPhase2()
		selectByID(mDB, "FB-1")
		u, _ = mDB.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
		mDB = u.(*Model)
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(standaloneDB), analysis.FeedbackFile)); !os.IsNotExist(statErr) {
			t.Fatalf("A10-P2-4 regression: explicit standalone --db wrote feedback.json outside .beads")
		}

		// Guard 2: BV_NO_SAVED_CONFIG=1 must refuse without writing.
		t.Setenv("BV_NO_SAVED_CONFIG", "1")
		noSaveDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(noSaveDir, 0o755); err != nil {
			t.Fatal(err)
		}
		mNoSave := NewModel(copyIssues(issues), nil, filepath.Join(noSaveDir, "issues.jsonl"), ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mNoSave.Stop)
		mNoSave.analysis.WaitForPhase2()
		selectByID(mNoSave, "FB-1")
		u, _ = mNoSave.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
		mNoSave = u.(*Model)
		if _, statErr := os.Stat(filepath.Join(noSaveDir, analysis.FeedbackFile)); !os.IsNotExist(statErr) {
			t.Fatalf("BV_NO_SAVED_CONFIG guard violated: feedback.json was created")
		}
	})
}

func TestWave27_Pass11Fixes_And_BoardActionableHistoryParity(t *testing.T) {
	refNow := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	t.Run("A11_1_AlertsTabCompactOverflowGuard", func(t *testing.T) {
		m := NewModel([]model.Issue{
			{ID: "A-1", Title: "One", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}, nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.alerts = []drift.Alert{
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "A-1", Message: "Alert zero"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "A-1", Message: "Alert one"},
			{
				Type: drift.AlertBlockingCascade, Severity: drift.SeverityCritical, IssueID: "A-1",
				RelatedIssueID: "A-2", UnblocksCount: 4, DownstreamPrioritySum: 6,
				Message:         "Middle alert with rich multi-line metadata that must never clip footer",
				SuggestedAction: "Prioritize clearing A-1 immediately to unblock downstream tracks",
			},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityInfo, IssueID: "A-1", Message: "Alert three"},
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityInfo, IssueID: "A-1", Message: "Alert four"},
		}
		m.showAlertsPanel = true
		m.alertsTab = 0
		m.alertsCursor = 2

		for _, sz := range [][2]int{{80, 16}, {36, 18}} {
			m.width, m.height = sz[0], sz[1]
			rendered := ansi.Strip(m.renderAlertsPanel())
			if !strings.Contains(rendered, "Esc: close") {
				t.Fatalf("A11-1 regression (%dx%d): Alerts tab clipped footer 'Esc: close':\n%s", sz[0], sz[1], rendered)
			}
			if !strings.Contains(rendered, "▸ ") {
				t.Fatalf("A11-1 regression (%dx%d): Alerts tab missing selected cursor row:\n%s", sz[0], sz[1], rendered)
			}
		}
	})

	t.Run("A11_2_DetachedAnalyzerRetriesInFlightPhase2", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "P2-1", Title: "Root", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		snap := NewSnapshotBuilder(copyIssues(issues)).Build()
		snap.Analysis.WaitForPhase2()
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.snapshot = snap
		m.analysis = snap.Analysis
		// Simulate recordTriageFeedback detaching m.analyzer from m.snapshot.Analyzer while Phase2ReadyMsg was in flight.
		detached := analysis.NewAnalyzer(copyIssues(issues))
		detached.SetNow(refNow)
		m.analyzer = detached
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		_, cmd := m.Update(Phase2ReadyMsg{
			Stats:          snap.Analysis,
			prepared:       snap,
			preparationCtx: ctx,
			sourceSnapshot: snap,
			sourceAnalyzer: snap.Analyzer,
			sourceWeights:  snap.Analyzer.Weights(),
			sourceNow:      snap.Analyzer.Now(),
		})
		if cmd == nil {
			t.Fatalf("A11-2 regression: Phase2ReadyMsg with detached m.analyzer stalled instead of scheduling preparePhase2Cmd retry")
		}
	})

	t.Run("A11_5_UnscopedSnapshotReadyAppliesDivergedFeedbackWeights", func(t *testing.T) {
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		beadsPath := filepath.Join(beadsDir, "issues.jsonl")
		issues := []model.Issue{
			{ID: "W-1", Title: "Alpha", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "W-2", Title: "Beta", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		fb, err := analysis.LoadFeedback(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < analysis.MinFeedbackSamples; i++ {
			if err := fb.RecordFeedback("W-1", "accept", 0.9, analysis.ScoreBreakdown{
				PageRank: 0.9, PageRankNorm: 0.95,
				Betweenness: 0.05, BetweennessNorm: 0.05,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := fb.Save(beadsDir); err != nil {
			t.Fatal(err)
		}
		if !fb.Applies() {
			t.Fatalf("expected saved feedback to apply")
		}
		// Build a worker snapshot with default weights (simulating in-flight snapshot built before feedback save).
		snap := NewSnapshotBuilder(copyIssues(issues)).Build()
		snap.Analysis.WaitForPhase2()
		defaultW := snap.Analyzer.Weights()
		if defaultW == fb.Weights() {
			t.Fatalf("expected saved feedback weights to diverge from default weights")
		}

		m := NewModel(copyIssues(issues), nil, beadsPath)
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		u, _ := m.Update(SnapshotReadyMsg{Snapshot: snap})
		m = u.(*Model)
		if m.analyzer == snap.Analyzer {
			t.Fatalf("A11-5 regression: unscoped SnapshotReadyMsg failed to detach analyzer when feedback weights diverged")
		}
		if triageWeightsDiffer(m.analyzer.Weights(), fb.Weights()) {
			t.Fatalf("A11-5 regression: unscoped SnapshotReadyMsg kept default weights %+v instead of reloaded %+v", m.analyzer.Weights(), fb.Weights())
		}
	})

	t.Run("A11_6_DeduplicatedCycleIDs_And_DetailPaneScopeNo2HopLeak", func(t *testing.T) {
		// 1) Multi-back-edge cycle deduplicates CycleIDs in GetBlockerChain.
		cycIssues := []model.Issue{
			{
				ID: "C-A", Title: "A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{
					{IssueID: "C-A", DependsOnID: "C-B", Type: model.DepBlocks},
					{IssueID: "C-A", DependsOnID: "C-C", Type: model.DepBlocks},
				},
			},
			{
				ID: "C-B", Title: "B", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{{IssueID: "C-B", DependsOnID: "C-A", Type: model.DepBlocks}},
			},
			{
				ID: "C-C", Title: "C", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{{IssueID: "C-C", DependsOnID: "C-A", Type: model.DepBlocks}},
			},
		}
		an := analysis.NewAnalyzer(cycIssues)
		an.SetNow(refNow)
		chain := an.GetBlockerChain("C-A")
		if chain == nil || !chain.HasCycle || len(chain.CycleIDs) != 1 || chain.CycleIDs[0] != "C-A" {
			t.Fatalf("A11-6 regression: expected deduplicated CycleIDs=[C-A], got %+v", chain)
		}

		// 2) DetailPane under --label scope (FE-1 -> API-1 [1-hop context] -> DB-SECRET [2-hop out-of-scope]) must NOT leak DB-SECRET as "missing".
		allIssues := []model.Issue{
			{ID: "DB-SECRET", Title: "Hidden DB root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, Labels: []string{"db"}},
			{ID: "API-1", Title: "1-hop API neighbor", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"api"},
				Dependencies: []*model.Dependency{{IssueID: "API-1", DependsOnID: "DB-SECRET", Type: model.DepBlocks}}},
			{ID: "FE-1", Title: "Frontend target", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"frontend"},
				Dependencies: []*model.Dependency{{IssueID: "FE-1", DependsOnID: "API-1", Type: model.DepBlocks}}},
		}
		fullAuth := model.NewReadinessIndex(allIssues)
		// Pass only FE-1 and API-1 (1-hop neighbor) to the TUI, just like --label frontend does.
		loadedScoped := []model.Issue{allIssues[1], allIssues[2]}
		m := NewModel(loadedScoped, nil, "", ReadinessScope{
			Authority:    fullAuth,
			CandidateIDs: map[string]bool{"FE-1": true},
			ReferenceNow: refNow,
		})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 45})
		m.isSplitView = true
		m.selectVisibleListItemByID("FE-1")
		m.updateViewportContent()
		md := ansi.Strip(m.viewport.View())
		if strings.Contains(md, "DB-SECRET") {
			t.Fatalf("A11-6 regression: DetailPane leaked 2-hop out-of-scope ID DB-SECRET:\n%s", md)
		}
		if !strings.Contains(md, "out-of-scope upstream blocker") {
			t.Fatalf("A11-6 regression: DetailPane should summarize hidden upstream blocker count:\n%s", md)
		}
	})

	t.Run("A11_7_A11_8_A11_9_InsightsCapacityRelocationAndEmptyDetail", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "I-1", Title: "First", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "I-2", Title: "Second", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})

		// A11-8 / A13-14: Relocate prevSelectedID when PanelPriority topPicks genuinely reorder
		// during rebuildInsightsPanel (I-2 at index 0 before rebuild -> I-2 moves to index 1 after rebuild),
		// AND verify cross-cell wasHeatmapDrill relocation.
		m.focused = focusInsights
		m.insightsPanel.focusedPanel = PanelPriority
		// Intentionally place I-2 at index 0 (opposite of real score order where I-1 is index 0)
		m.insightsPanel.SetTopPicks([]analysis.TopPick{{ID: "I-2", Title: "Second", Score: 0.9}, {ID: "I-1", Title: "First", Score: 0.5}})
		m.insightsPanel.selectedIndex[PanelPriority] = 0 // I-2 at index 0
		if got := m.insightsPanel.SelectedIssueID(); got != "I-2" {
			t.Fatalf("precondition failed: got %q, want I-2", got)
		}
		m.rebuildInsightsPanel()
		if got := m.insightsPanel.SelectedIssueID(); got != "I-2" {
			t.Fatalf("A11-8 regression: rebuildInsightsPanel jumped selection from I-2 to %q", got)
		}
		if len(m.insightsPanel.topPicks) < 2 || m.insightsPanel.topPicks[0].ID != "I-1" || m.insightsPanel.selectedIndex[PanelPriority] != 1 {
			t.Fatalf("A13-14 regression: expected I-1 at index 0 and I-2 relocated to index 1 after reorder, got len=%d top0=%q idx=%d",
				len(m.insightsPanel.topPicks), func() string {
					if len(m.insightsPanel.topPicks) > 0 {
						return m.insightsPanel.topPicks[0].ID
					}
					return ""
				}(), m.insightsPanel.selectedIndex[PanelPriority])
		}
		// Cross-cell heatmap drilldown relocation: set heatmapDrill on cell (0,0) with I-2 while real heatmap puts I-2 elsewhere.
		m.insightsPanel.showHeatmap = true
		m.insightsPanel.heatmapDrill = true
		m.insightsPanel.heatmapRow, m.insightsPanel.heatmapCol = 0, 0
		m.insightsPanel.heatmapIssues = []string{"I-2"}
		m.insightsPanel.heatmapDrillIdx = 0
		m.rebuildInsightsPanel()
		if !m.insightsPanel.heatmapDrill || m.insightsPanel.SelectedIssueID() != "I-2" {
			t.Fatalf("A12-6/A13-14 regression: heatmap drilldown lost I-2 across rebuild (drill=%v sel=%q)", m.insightsPanel.heatmapDrill, m.insightsPanel.SelectedIssueID())
		}

		// A11-9: Empty selection detail panel still renders globalCapacity & feedbackSummary.
		emptyIns := NewInsightsModel(analysis.Insights{}, map[string]*model.Issue{}, m.theme)
		emptyIns.focusedPanel = PanelPriority
		emptyIns.SetGlobalCapacity(&GlobalCapacitySummary{
			OpenCount: 2, TotalMinutes: 120, CriticalPathMinutes: 60, ParallelizablePct: 50,
			Days1Agent: 2.0, Days2Min: 1.0, Days2Max: 1.5, Days4Min: 0.5, Days4Max: 1.0,
		})
		emptyIns.SetSize(120, 30)
		detailOut := ansi.Strip(emptyIns.renderDetailPanel(60, 26, m.theme))
		if !strings.Contains(detailOut, "Backlog Capacity") {
			t.Fatalf("A11-9 regression: empty-selection detail panel omitted Backlog Capacity:\n%s", detailOut)
		}

		// A11-7: FileChangedMsg when unfocused clears stale globalCapacity on insightsPanel.
		reloadDir := t.TempDir()
		reloadJSONL := filepath.Join(reloadDir, "issues.jsonl")
		if err := os.WriteFile(reloadJSONL, []byte(`{"id":"I-1","title":"First","status":"open","priority":1,"issue_type":"task","created_at":"2026-11-20T00:00:00Z","updated_at":"2026-11-20T00:00:00Z"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BV_BACKGROUND_MODE", "0")
		mReload := NewModel(copyIssues(issues), nil, reloadJSONL, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mReload.Stop)
		mReload.analysis.WaitForPhase2()
		mReload.focused = focusList
		mReload.insightsPanel.SetGlobalCapacity(&GlobalCapacitySummary{OpenCount: 99, TotalMinutes: 999})
		_, _ = mReload.Update(FileChangedMsg{})
		if mReload.insightsPanel.globalCapacity != nil {
			t.Fatalf("A11-7 regression: FileChangedMsg left stale globalCapacity on unfocused insightsPanel")
		}
	})

	t.Run("W27_1_HistoryHotspotsAndRelatedBeads", func(t *testing.T) {
		sha1 := strings.Repeat("1", 40)
		sha2 := strings.Repeat("2", 40)
		report := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"H-1": {
					BeadID: "H-1", Title: "Auth handler", Status: "open",
					Commits: []correlation.CorrelatedCommit{{
						BeadID: "H-1", SHA: sha1, ShortSHA: sha1[:7], Message: "touch auth", Author: "alice",
						Timestamp: refNow.Add(-2 * time.Hour), Confidence: 0.9,
						Files: []correlation.FileChange{{Path: "pkg/auth/login.go", Insertions: 10}},
					}},
				},
				"H-2": {
					BeadID: "H-2", Title: "Auth rate limit", Status: "open",
					Commits: []correlation.CorrelatedCommit{{
						BeadID: "H-2", SHA: sha2, ShortSHA: sha2[:7], Message: "rate limit auth", Author: "bob",
						Timestamp: refNow.Add(-1 * time.Hour), Confidence: 0.9,
						Files: []correlation.FileChange{{Path: "pkg/auth/login.go", Insertions: 8}},
					}},
				},
			},
			CommitIndex: correlation.CommitIndex{sha1: {"H-1"}, sha2: {"H-2"}},
		}
		hm := NewHistoryModel(report, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		hm.SetReferenceTime(refNow)
		hm.SetSize(140, 32)
		hm.ToggleFileTree()
		// Expand directories to expose pkg/auth/login.go
		var expandAll func([]*FileTreeNode)
		expandAll = func(nodes []*FileTreeNode) {
			for _, n := range nodes {
				if n.IsDir {
					n.Expanded = true
					expandAll(n.Children)
				}
			}
		}
		expandAll(hm.fileTree)
		hm.rebuildFlatFileList()
		treeView := ansi.Strip(hm.renderFileTreePanel(40, 24))
		if !strings.Contains(treeView, "🔥2") {
			t.Fatalf("W27-1 regression: History file tree missing 🔥2 hotspot badge:\n%s", treeView)
		}
		detailView := ansi.Strip(hm.renderDetailPanel(60, 24))
		if !strings.Contains(detailView, "🔗 Related:") || !strings.Contains(detailView, "H-2") {
			t.Fatalf("W27-1 regression: History detail panel missing Related beads line:\n%s", detailView)
		}
	})

	t.Run("W27_3_BoardDetailIntelligence_CachePreserved_And_ActionableParity", func(t *testing.T) {
		est60 := 60
		closedAt := refNow.Add(-24 * time.Hour)
		issues := []model.Issue{
			{ID: "B-ROOT", Title: "Root blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow},
			{ID: "B-MID", Title: "Middle blocked", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "B-MID", DependsOnID: "B-ROOT", Type: model.DepBlocks}}},
			{ID: "B-LEAF", Title: "Leaf blocked", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "B-LEAF", DependsOnID: "B-MID", Type: model.DepBlocks}}},
			{ID: "B-DONE", Title: "Closed velocity sample", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
		}
		an := analysis.NewAnalyzer(issues)
		an.SetNow(refNow)
		stats := an.Analyze()
		etaCtx := analysis.NewETAContext(issues, &stats, refNow)

		bm := NewBoardModel(issues, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		bm.SetReferenceTime(refNow)
		bm.SetReadiness(an.Readiness())
		bm.SetAnalyzer(an)
		bm.SetAnalysis(&stats)
		bm.SetETAContext(etaCtx)
		bm.ToggleDetail()
		bm.SetSize(160, 32)
		// Focus B-ROOT (Open column): should show ETA Forecast, Graph metrics, and What-If Close (1 direct / 2 total)
		rootView := ansi.Strip(bm.View(160, 32))
		rendersAfterFirst := bm.detailCache.renders
		// Re-attaching identical pointers on subsequent frames must NOT bust detailCache.
		bm.SetAnalyzer(an)
		bm.SetAnalysis(&stats)
		bm.SetETAContext(etaCtx)
		_ = bm.View(160, 32)
		if bm.detailCache.renders != rendersAfterFirst {
			t.Fatalf("W27-3 regression: identical SetAnalyzer/SetAnalysis/SetETAContext busted board detailCache (%d -> %d)", rendersAfterFirst, bm.detailCache.renders)
		}
		for _, want := range []string{"ETA Forecast:", "What-If Close:", "1 direct / 2 total"} {
			if !strings.Contains(rootView, want) {
				t.Fatalf("W27-3 regression: Board detail for B-ROOT missing %q:\n%s", want, rootView)
			}
		}

		// Navigate to Blocked column (B-LEAF) and verify Start Here (Root Blockers) points to B-ROOT without leaking hidden items.
		if !bm.SelectIssueByID("B-LEAF") {
			t.Fatalf("failed to select B-LEAF on board")
		}
		leafView := ansi.Strip(bm.View(160, 32))
		if !strings.Contains(leafView, "Start Here") || !strings.Contains(leafView, "B-ROOT") {
			t.Fatalf("W27-3 regression: Board detail for B-LEAF missing Start Here -> B-ROOT:\n%s", leafView)
		}

		// ActionableModel via Model.refreshActionableView: inline capacity, ETA badge, and cascade detail.
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.width, m.height = 140, 30
		m.refreshActionableView()
		actView := ansi.Strip(m.actionableView.Render())
		for _, want := range []string{"1a ", "2a ", "4a ", "par", "2 total cascade"} {
			if !strings.Contains(actView, want) {
				t.Fatalf("W27-3 regression: Actionable view missing %q:\n%s", want, actView)
			}
		}
		// A12-11 / A13-7: narrow 36-col Actionable view with long workspace-prefixed IDs must
		// keep every item strictly single-line (no lipgloss wrap) and stay within visibleLines.
		longPlan := analysis.ExecutionPlan{
			TotalActionable: 3,
			Tracks: []analysis.ExecutionTrack{{
				TrackID: "track-A",
				Reason:  "Independent long-ID workspace track for narrow width testing",
				Items: []analysis.PlanItem{
					{ID: "beads_viewer-very-long-id-alpha-1234", Title: "First very long title that must truncate cleanly", Priority: 1, UnblocksIDs: []string{"U1", "U2"}},
					{ID: "beads_viewer-very-long-id-beta-5678", Title: "Second very long title", Priority: 2},
					{ID: "beads_viewer-very-long-id-gamma-9012", Title: "Third very long title", Priority: 2},
				},
			}},
		}
		narrowAM := NewActionableModel(longPlan, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		narrowAM.SetSize(36, 12)
		narrowAct := ansi.Strip(narrowAM.Render())
		narrowLines := strings.Split(narrowAct, "\n")
		if len(narrowLines) > 12 {
			t.Fatalf("A13-7 regression: narrow Actionable rendered %d physical lines > height 12:\n%s", len(narrowLines), narrowAct)
		}
		for _, ln := range narrowLines {
			if w := ansi.StringWidth(ln); w > 36 {
				t.Fatalf("A13-7 regression: narrow Actionable line exceeded 36 cols (%d): %q", w, ln)
			}
		}
		// Count tree-connector glyphs vs total item lines: every item must occupy exactly 1 line.
		connCount := 0
		for _, ln := range narrowLines {
			if strings.Contains(ln, "├─") || strings.Contains(ln, "└─") {
				connCount++
			}
		}
		if connCount != 3 {
			t.Fatalf("A13-7 regression: expected all 3 single-line cards visible (connectors=%d):\n%s", connCount, narrowAct)
		}

		// A12-5: Workspace repo-filtered Board What-If must NOT leak hidden-repo unblock counts.
		wsIssues := []model.Issue{
			{ID: "api-1", Title: "API Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, SourceRepo: "api", CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "web-1", Title: "Web Dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web", CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks}}},
		}
		mWS := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(mWS.Stop)
		mWS.analysis.WaitForPhase2()
		mWS.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, TotalIssues: 2, RepoPrefixes: []string{"api-", "web-"}})
		mWS.activeRepos = map[string]bool{"api": true}
		mWS.syncBoardReadinessAndFullIssues()
		mWS.board.SetIssues(mWS.repoScopedIssues())
		mWS.board.ToggleDetail()
		boardAn, boardStats, boardETA := mWS.activeScopeAnalysis()
		mWS.board.SetAnalyzer(boardAn)
		mWS.board.SetAnalysis(boardStats)
		mWS.board.SetETAContext(boardETA)
		mWS.board.SetSize(140, 30)
		wsBoardOut := ansi.Strip(mWS.board.View(140, 30))
		if strings.Contains(wsBoardOut, "What-If Close:") || strings.Contains(wsBoardOut, "web-1") {
			t.Fatalf("A12-5 regression: repo-filtered Board detail leaked hidden-repo web-1 unblock:\n%s", wsBoardOut)
		}
	})
}

func TestWave28_Pass13Fixes_And_W28_TUIParity(t *testing.T) {
	refNow := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	t.Run("A13_4_A13_9_WorkspaceScopeIsolation_DetailAndGraph", func(t *testing.T) {
		wsIssues := []model.Issue{
			{ID: "api-1", Title: "Secret API Root", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, SourceRepo: "api", CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow},
			{ID: "web-1", Title: "Web Dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web", CreatedAt: refNow.Add(-24 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks}}},
		}
		m := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, TotalIssues: 2, RepoPrefixes: []string{"api-", "web-"}})
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 34})

		// Filter to api only: Detail Pane What-If and Graph View What-If must NOT leak web-1.
		m.activeRepos = map[string]bool{"api": true}
		m.applyFilter()
		m.selectListIssueByID("api-1")
		m.updateViewportContent()
		apiDetail := ansi.Strip(m.viewportContent)
		if strings.Contains(apiDetail, "web-1") {
			t.Fatalf("A13-4 regression: repo-filtered Detail Pane What-If leaked hidden web-1:\n%s", apiDetail)
		}
		graphOut := ansi.Strip(m.RenderDebugView("graph", 140, 34))
		if strings.Contains(graphOut, "web-1") {
			t.Fatalf("A13-4 regression: repo-filtered Graph View leaked hidden web-1:\n%s", graphOut)
		}

		// Filter to web only: Blocking Dependencies & Dependency Tree must NOT leak Secret API Root or api-1 (not found).
		m.activeRepos = map[string]bool{"web": true}
		m.applyFilter()
		m.selectListIssueByID("web-1")
		m.updateViewportContent()
		webDetail := ansi.Strip(m.viewportContent)
		if strings.Contains(webDetail, "Secret API Root") || strings.Contains(webDetail, "api-1") {
			t.Fatalf("A13-4 regression: repo-filtered Detail Pane leaked hidden api-1 blocker:\n%s", webDetail)
		}
		if !strings.Contains(webDetail, "out-of-scope") {
			t.Fatalf("A13-4 regression: repo-filtered Detail Pane should summarize hidden blocker count:\n%s", webDetail)
		}

		// A13-9: etaScopeKey detects membership change of equal-sized candidateIDs maps.
		m.candidateIDs = map[string]bool{"api-1": true}
		k1 := m.etaScopeKey()
		m.candidateIDs = map[string]bool{"web-1": true}
		k2 := m.etaScopeKey()
		if k1 == k2 {
			t.Fatalf("A13-9 regression: etaScopeKey collided for distinct candidate sets (%q == %q)", k1, k2)
		}
	})

	t.Run("A13_5_A13_6_A13_11_BoardZeroWhatIf_ActiveCore_EmptyDarkMode", func(t *testing.T) {
		closedAt := refNow.Add(-24 * time.Hour)
		issues := []model.Issue{
			{ID: "A", Title: "Blocker A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "C", Title: "Co-Blocker C", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "B", Title: "Blocked by A and C", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{
					{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
					{IssueID: "B", DependsOnID: "C", Type: model.DepBlocks},
				}},
			// Closed triangle + open node O1 to verify ActiveCoreNumber excludes closed archive.
			{ID: "C1", Title: "Closed 1", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: closedAt, ClosedAt: &closedAt},
			{ID: "C2", Title: "Closed 2", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: closedAt, ClosedAt: &closedAt,
				Dependencies: []*model.Dependency{{IssueID: "C2", DependsOnID: "C1", Type: model.DepBlocks}}},
			{ID: "O1", Title: "Open leaf", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{
					{IssueID: "O1", DependsOnID: "C1", Type: model.DepBlocks},
					{IssueID: "O1", DependsOnID: "C2", Type: model.DepBlocks},
				}},
		}
		an := analysis.NewAnalyzer(issues)
		an.SetNow(refNow)
		stats := an.Analyze()
		bm := NewBoardModel(issues, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		bm.SetReferenceTime(refNow)
		bm.SetReadiness(an.Readiness())
		bm.SetAnalyzer(an)
		bm.SetAnalysis(&stats)
		bm.ToggleDetail()
		bm.SetSize(150, 30)

		// A13-5: A shares dependent B with co-blocker C -> What-If proves 0 unblocks -> must NOT print false fallback.
		if !bm.SelectIssueByID("A") {
			t.Fatal("failed to select A on board")
		}
		aView := ansi.Strip(bm.View(150, 30))
		if strings.Contains(aView, "Completing this would unblock") {
			t.Fatalf("A13-5 regression: Board printed false raw unblock fallback when What-If proved 0:\n%s", aView)
		}

		// A13-6: O1 only touches closed C1 & C2 -> ActiveCoreNumber is 0, so K-Core k=2 must NOT appear.
		if !bm.SelectIssueByID("O1") {
			t.Fatal("failed to select O1 on board")
		}
		o1View := ansi.Strip(bm.View(150, 30))
		if strings.Contains(o1View, "K-Core k=2") {
			t.Fatalf("A13-6 regression: Board leaked unfiltered closed-archive CoreNumber (K-Core k=2):\n%s", o1View)
		}

		// A13-11: Empty board dark/light mode flip must re-render and update lastDetailDark/detailCache.darkMode.
		darkRenderer := lipgloss.NewRenderer(os.Stdout)
		darkRenderer.SetHasDarkBackground(true)
		lightRenderer := lipgloss.NewRenderer(os.Stdout)
		lightRenderer.SetHasDarkBackground(false)
		emptyBM := NewBoardModel(nil, DefaultTheme(darkRenderer))
		emptyBM.ToggleDetail()
		emptyBM.SetSize(120, 24)
		emptyBM.syncDetailContent()
		if !emptyBM.lastDetailDark || !emptyBM.detailCache.darkMode || emptyBM.detailCache.renders != 1 {
			t.Fatalf("A13-11 setup: expected dark empty detail render=1, got lastDark=%v cacheDark=%v renders=%d",
				emptyBM.lastDetailDark, emptyBM.detailCache.darkMode, emptyBM.detailCache.renders)
		}
		emptyBM.theme = DefaultTheme(lightRenderer)
		emptyBM.syncDetailContent()
		if emptyBM.lastDetailDark || emptyBM.detailCache.darkMode || emptyBM.detailCache.renders != 2 {
			t.Fatalf("A13-11 regression: empty board syncDetailContent failed to re-render on light theme flip (lastDark=%v cacheDark=%v renders=%d)",
				emptyBM.lastDetailDark, emptyBM.detailCache.darkMode, emptyBM.detailCache.renders)
		}
		emptyBM.syncDetailContent()
		if emptyBM.detailCache.renders != 2 {
			t.Fatalf("A13-11 regression: unchanged empty board should hit cache (renders=%d, want 2)", emptyBM.detailCache.renders)
		}
	})

	t.Run("A13_8_AlertsPanelCompactNarrowFooter", func(t *testing.T) {
		m := NewModel([]model.Issue{
			{ID: "AL-1", Title: "Alert issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug, CreatedAt: refNow, UpdatedAt: refNow},
		}, nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.alerts = []drift.Alert{
			{Type: drift.AlertStaleIssue, Severity: drift.SeverityCritical, IssueID: "AL-1", Message: "Very long alert message that wraps across multiple narrow lines", SuggestedAction: "Inspect and resolve immediately"},
			{Type: drift.AlertBlockingCascade, Severity: drift.SeverityWarning, IssueID: "AL-1", Message: "Second warning alert"},
			{Type: drift.AlertPotentialDuplicate, Severity: drift.SeverityInfo, IssueID: "AL-1", Message: "Third info alert"},
		}
		m.suggestionsCache = []analysis.Suggestion{
			{Type: analysis.SuggestionMissingDependency, Confidence: 0.9, TargetBead: "AL-1", Summary: "Very long hygiene suggestion summary", Reason: "Mentioned in description", ActionCommand: "br dep add AL-1 AL-2"},
		}
		m.suggestionsCacheLoaded = true
		for _, dims := range [][2]int{{36, 16}, {24, 14}} {
			m.width, m.height = dims[0], dims[1]
			for _, tab := range []int{0, 1} {
				m.alertsTab = tab
				out := ansi.Strip(m.renderAlertsPanel())
				if !strings.Contains(out, "Esc: close") {
					t.Fatalf("A13-8 regression: Alerts panel (tab=%d, %dx%d) clipped Esc: close footer:\n%s", tab, dims[0], dims[1], out)
				}
			}
		}
		// Empty states must also preserve Esc: close at ultra-narrow 24x14.
		m.alerts = nil
		m.suggestionsCache = nil
		m.width, m.height = 24, 14
		for _, tab := range []int{0, 1} {
			m.alertsTab = tab
			out := ansi.Strip(m.renderAlertsPanel())
			if !strings.Contains(out, "Esc: close") {
				t.Fatalf("A13-8 regression: empty Alerts panel (tab=%d, 24x14) clipped Esc: close footer:\n%s", tab, out)
			}
		}
	})

	t.Run("W28_1_HistoryCorrelationFeedback_And_FileTreeIntel", func(t *testing.T) {
		sha1 := strings.Repeat("a", 40)
		sha2 := strings.Repeat("b", 40)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"FB-1","title":"Auth core","status":"open","priority":1,"issue_type":"task","created_at":"2026-11-20T00:00:00Z","updated_at":"2026-11-20T00:00:00Z"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		report := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"FB-1": {
					BeadID: "FB-1", Title: "Auth core", Status: "open",
					Commits: []correlation.CorrelatedCommit{{
						BeadID: "FB-1", SHA: sha1, ShortSHA: sha1[:7], Message: "feat: auth core", Author: "alice",
						Timestamp: refNow.Add(-2 * time.Hour), Confidence: 0.75, Confirmed: true,
						Files: []correlation.FileChange{
							{Path: "pkg/auth/core.go", Insertions: 12},
							{Path: "pkg/auth/tokens.go", Insertions: 5},
						},
					}},
				},
				"FB-2": {
					BeadID: "FB-2", Title: "Auth refresh", Status: "open",
					Commits: []correlation.CorrelatedCommit{{
						BeadID: "FB-2", SHA: sha2, ShortSHA: sha2[:7], Message: "fix: token refresh", Author: "bob",
						Timestamp: refNow.Add(-1 * time.Hour), Confidence: 0.80,
						Files: []correlation.FileChange{
							{Path: "pkg/auth/core.go", Insertions: 6},
							{Path: "pkg/auth/tokens.go", Insertions: 4},
						},
					}},
				},
			},
			CommitIndex: correlation.CommitIndex{sha1: {"FB-1"}, sha2: {"FB-2"}},
		}

		// 1. Confirmed badge + File Tree Blast/Co-change footer rendering.
		hm := NewHistoryModel(report, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		hm.SetReferenceTime(refNow)
		hm.SetSize(140, 32)
		detailStr := ansi.Strip(hm.renderDetailPanel(70, 24))
		if !strings.Contains(detailStr, "✓ confirmed") || !strings.Contains(detailStr, "+/-:confirm/reject") {
			t.Fatalf("W28-1 regression: History detail missing confirmed badge or feedback hint:\n%s", detailStr)
		}
		hm.ToggleFileTree()
		var expandAll func([]*FileTreeNode)
		expandAll = func(nodes []*FileTreeNode) {
			for _, n := range nodes {
				if n.IsDir {
					n.Expanded = true
					expandAll(n.Children)
				}
			}
		}
		expandAll(hm.fileTree)
		hm.rebuildFlatFileList()
		for i, n := range hm.flatFileList {
			if !n.IsDir && strings.HasSuffix(n.Path, "core.go") {
				hm.selectedFileIdx = i
				break
			}
		}
		treeOut := ansi.Strip(hm.renderFileTreePanel(48, 24))
		if !strings.Contains(treeOut, "Blast:") || !strings.Contains(treeOut, "Co-change:") || !strings.Contains(treeOut, "tokens.go") {
			t.Fatalf("W28-1 regression: History file tree missing Blast/Co-change footer:\n%s", treeOut)
		}

		// 2. Interactive '+' confirm persists .beads/correlation_feedback.json and honors read-only guard.
		m := NewModel([]model.Issue{{ID: "FB-1", Title: "Auth core", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow}}, nil, jsonlPath, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.workDir = tmpDir
		m.historyView = hm
		m.focused = focusHistory
		m.isHistoryView = true
		// Guard check 1: BV_NO_SAVED_CONFIG=1 (set by pkg/ui TestMain) disables persistence cleanly.
		_ = m.recordCorrelationFeedback("confirm")
		if !strings.Contains(m.statusMsg, "disabled") {
			t.Fatalf("W28-1 regression: expected BV_NO_SAVED_CONFIG to disable correlation feedback, got %q", m.statusMsg)
		}
		// Allow persistence inside temp dir for the remaining checks.
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		// Guard check 2: time-travel disables persistence cleanly.
		m.timeTravelMode = true
		_ = m.recordCorrelationFeedback("confirm")
		if !strings.Contains(m.statusMsg, "disabled") {
			t.Fatalf("W28-1 regression: expected timeTravelMode to disable correlation feedback, got %q", m.statusMsg)
		}
		m.timeTravelMode = false
		_ = m.recordCorrelationFeedback("confirm")
		if m.statusIsError || !strings.Contains(m.statusMsg, "Confirmed") {
			t.Fatalf("W28-1 regression: confirm correlation feedback failed: %q (err=%v)", m.statusMsg, m.statusIsError)
		}
		fs := correlation.NewFeedbackStore(beadsDir)
		if err := fs.Load(); err != nil {
			t.Fatalf("load saved correlation feedback: %v", err)
		}
		fb, ok := fs.Get(sha1, "FB-1")
		if !ok || fb.Type != correlation.FeedbackConfirm {
			t.Fatalf("W28-1 regression: expected persisted confirm entry for (%s, FB-1), got ok=%v fb=%+v", sha1, ok, fb)
		}
	})

	t.Run("W28_2_Explainability_W28_3_Tree_And_W28_5_Labels", func(t *testing.T) {
		est60 := 60
		closedAt := refNow.Add(-48 * time.Hour)
		issues := []model.Issue{
			{ID: "EP-1", Title: "Auth Epic", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeEpic, Labels: []string{"db"}, SourceRepo: "api", EstimatedMinutes: &est60, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: refNow},
			{ID: "T-1", Title: "DB Migration", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, Labels: []string{"db"}, SourceRepo: "api", EstimatedMinutes: &est60, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "T-1", DependsOnID: "EP-1", Type: model.DepParentChild}}},
			{ID: "T-2", Title: "API Endpoint", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"}, SourceRepo: "api", EstimatedMinutes: &est60, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{
					{IssueID: "T-2", DependsOnID: "EP-1", Type: model.DepParentChild},
					{IssueID: "T-2", DependsOnID: "T-1", Type: model.DepBlocks},
				}},
			{ID: "T-3", Title: "UI View", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"frontend"}, SourceRepo: "web", EstimatedMinutes: &est60, CreatedAt: refNow.Add(-24 * time.Hour), UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "T-3", DependsOnID: "T-2", Type: model.DepBlocks}}},
			{ID: "T-DONE", Title: "Done Subtask", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, Labels: []string{"db"}, SourceRepo: "api", EstimatedMinutes: &est60, CreatedAt: refNow.Add(-96 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt,
				Dependencies: []*model.Dependency{{IssueID: "T-DONE", DependsOnID: "EP-1", Type: model.DepParentChild}}},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(m.Stop)
		m.analysis.WaitForPhase2()
		m.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, TotalIssues: len(issues), RepoPrefixes: []string{"api-", "web-"}})
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})

		// Inject a deterministic priority hint to verify W28-2 across DetailPane, Insights, Graph, and Tree.
		m.priorityHints["T-1"] = &analysis.PriorityRecommendation{
			IssueID: "T-1", Title: "DB Migration", CurrentPriority: 3, SuggestedPriority: 1,
			Direction: "increase", Confidence: 0.85, Reasoning: []string{"Blocks critical chain"},
		}
		m.showPriorityHints = true
		m.selectListIssueByID("T-1")
		m.updateViewportContent()
		detailMD := ansi.Strip(m.viewport.View())
		if !strings.Contains(detailMD, "Priority Hint:") || !strings.Contains(detailMD, "P3 → P1") {
			t.Fatalf("W28-2 regression: DetailPane missing Priority Hint reasoning:\n%s", detailMD)
		}
		// T-3 has cross-repo dependency on T-2 (web -> api) -> Risk Signals breakdown must render.
		m.selectListIssueByID("T-3")
		m.updateViewportContent()
		riskMD := ansi.Strip(m.viewport.View())
		if !strings.Contains(riskMD, "Risk Signals:") || !strings.Contains(riskMD, "cross-repo") {
			t.Fatalf("W28-2 regression: DetailPane missing Risk Signals breakdown for cross-repo blocked issue:\n%s", riskMD)
		}

		// W28-2: Insights PanelKeystones renders KPaths + Priority Hint on T-1.
		m.focused = focusInsights
		m.insightsPanel.focusedPanel = PanelKeystones
		m.rebuildInsightsPanel()
		m.insightsPanel.SetPriorityHints(m.priorityHints)
		m.insightsPanel.updateDetailContent()
		insDetail := ansi.Strip(m.insightsPanel.detailContent)
		if !strings.Contains(insDetail, "Top Critical Paths") {
			t.Fatalf("W28-2 regression: Insights PanelKeystones missing KPaths Top Critical Paths:\n%s", insDetail)
		}

		// W28-2: Graph view renders Priority hint for T-1.
		m.graphView.SelectByID("T-1")
		graphOut := ansi.Strip(m.RenderDebugView("graph", 140, 48))
		if !strings.Contains(graphOut, "Priority hint:") || !strings.Contains(graphOut, "P3→P1") {
			t.Fatalf("W28-2 regression: Graph view missing Priority hint:\n%s", graphOut)
		}

		// W28-3: Tree view renders [1/3] epic completion rollup, [API] repo badge, ⚡+1, ↑, and stays bounded at width 44.
		m.focused = focusTree
		m.refreshTreeView()
		m.tree.ExpandAll()
		treeWide := ansi.Strip(m.tree.View())
		for _, want := range []string{"[1/3]", "[API]", "⚡+1", "↑"} {
			if !strings.Contains(treeWide, want) {
				t.Fatalf("W28-3 regression: Tree view missing %q:\n%s", want, treeWide)
			}
		}
		m.tree.SetSize(44, 16)
		treeNarrow := ansi.Strip(m.tree.View())
		for _, ln := range strings.Split(treeNarrow, "\n") {
			if w := ansi.StringWidth(ln); w > 44 {
				t.Fatalf("W28-3 regression: narrow 44-col Tree row overflowed (%d): %q", w, ln)
			}
		}

		// W28-5 + A14-1: Opening Label Dashboard interactively via '[' MUST populate Trend 4w & Attn immediately
		// (without calling refreshLabelViews directly), fall back to 5-col at width 68, and refresh on repo scope change.
		m.focused = focusList
		m.width, m.height = 110, 30
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
		lblWide := ansi.Strip(m.labelDashboard.View())
		for _, want := range []string{"Trend 4w", "Attn", "#1"} {
			if !strings.Contains(lblWide, want) {
				t.Fatalf("W28-5/A14-1 regression: '[' interactive Label Dashboard missing %q:\n%s", want, lblWide)
			}
		}
		m.labelDashboard.SetSize(68, 20)
		lblNarrow := ansi.Strip(m.labelDashboard.View())
		if strings.Contains(lblNarrow, "Trend 4w") {
			t.Fatalf("W28-5 regression: narrow 68-col Label Dashboard should use 5-column layout:\n%s", lblNarrow)
		}
		// Open health detail for "db" (improving 4w trend) and "backend" (blocked T-2 cascading to frontend).
		var dbLH, backendLH analysis.LabelHealth
		for _, lh := range m.labelHealthCache.Labels {
			switch lh.Label {
			case "db":
				dbLH = lh
			case "backend":
				backendLH = lh
			}
		}
		m.labelHealthDetail = &dbLH
		m.showLabelHealthDetail = true
		m.populateLabelHealthDetailExtras("db")
		dbModal := ansi.Strip(m.renderLabelHealthDetail(dbLH))
		for _, want := range []string{"4w Trend:", "↗", "Attention:"} {
			if !strings.Contains(dbModal, want) {
				t.Fatalf("W28-5 regression: db Label Health Detail modal missing %q:\n%s", want, dbModal)
			}
		}
		m.labelHealthDetail = &backendLH
		m.populateLabelHealthDetailExtras("backend")
		backendModal := ansi.Strip(m.renderLabelHealthDetail(backendLH))
		for _, want := range []string{"4w Trend:", "Attention:", "Blockage Cascade:", "frontend"} {
			if !strings.Contains(backendModal, want) {
				t.Fatalf("W28-5 regression: backend Label Health Detail modal missing %q:\n%s", want, backendModal)
			}
		}
	})

	t.Run("Pass14_A14_2_A14_3_A14_4_A14_8_A14_9_AuditRemediations", func(t *testing.T) {
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		// A14-2: Multi-repo feedback target resolution (file-path BeadsDir, Prefix!=Name precedence,
		// pinned-revision read-only guard, workspace empty fallback refusal, single-repo explicit --db isolation).
		wsRoot := t.TempDir()
		apiRepo := filepath.Join(wsRoot, "api-svc")
		apiBeads := filepath.Join(apiRepo, ".beads")
		if err := os.MkdirAll(apiBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		otherRepo := filepath.Join(wsRoot, "other-repo")
		otherBeads := filepath.Join(otherRepo, ".beads")
		if err := os.MkdirAll(otherBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		cwdRepo := filepath.Join(wsRoot, "cwd-repo")
		cwdBeads := filepath.Join(cwdRepo, ".beads")
		if err := os.MkdirAll(cwdBeads, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwdBeads, "issues.jsonl"), []byte(`{"id":"CWD-1","title":"Wrong repo","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		wsIssues := []model.Issue{
			{ID: "api-42", Title: "API Task", SourceRepo: "api", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "pin-9", Title: "Pinned Rev Task", SourceRepo: "pin", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow},
		}
		wsModel := NewModel(copyIssues(wsIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(wsModel.Stop)
		wsModel.workDir = wsRoot
		wsModel.EnableWorkspaceMode(WorkspaceInfo{
			Enabled:   true,
			RepoCount: 2,
			Repos: []WorkspaceRepoTarget{
				{Name: "backend-service", Prefix: "api-", Path: apiRepo, BeadsDir: filepath.Join(apiBeads, "beads.db")},
				{Name: "pinned-svc", Prefix: "pin-", Path: otherRepo, BeadsDir: otherBeads, Revision: "HEAD~1"},
			},
		})
		gotDir, gotLocal := wsModel.resolveCorrelationFeedbackTarget("api-42")
		if gotDir != apiBeads || gotLocal != "42" {
			t.Fatalf("A14-2 regression: expected (%q, %q) for file-path BeadsDir & Prefix!=Name, got (%q, %q)", apiBeads, "42", gotDir, gotLocal)
		}
		if pDir, _ := wsModel.resolveCorrelationFeedbackTarget("pin-9"); pDir != "" {
			t.Fatalf("A14-2 regression: pinned historical workspace repo should refuse feedback target, got %q", pDir)
		}
		// Empty workspace Repos + no Origins must refuse rather than fall through to m.workDir/.beads.
		wsModel.workspaceInfo.Repos = nil
		if emptyDir, _ := wsModel.resolveCorrelationFeedbackTarget("api-42"); emptyDir != "" {
			t.Fatalf("A14-2 regression: workspace mode with no repos/origins fell through to %q", emptyDir)
		}

		// Single-repo explicit --db in otherRepo while workDir is cwdRepo must target otherBeads, NEVER cwdBeads.
		otherDB := filepath.Join(otherBeads, "beads.db")
		if err := os.WriteFile(otherDB, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		srModel := NewModel([]model.Issue{{ID: "O-1", Title: "Other", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow, UpdatedAt: refNow}}, nil, otherDB, ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(srModel.Stop)
		srModel.workDir = cwdRepo
		srDir, srLocal := srModel.resolveCorrelationFeedbackTarget("O-1")
		if srDir != otherBeads || srLocal != "O-1" {
			t.Fatalf("A14-2 regression: single-repo explicit --db wrote to wrong repo (%q, want %q)", srDir, otherBeads)
		}

		// A14-3: In-flight history load must be force-restarted (non-nil tea.Cmd & bumped request gen) on feedback save.
		sha := strings.Repeat("c", 40)
		rep := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"O-1": {BeadID: "O-1", Title: "Other", Status: "open", Commits: []correlation.CorrelatedCommit{{BeadID: "O-1", SHA: sha, ShortSHA: sha[:7], Message: "feat: O-1", Author: "alice", Timestamp: refNow, Confidence: 0.7}}},
			},
			CommitIndex: correlation.CommitIndex{sha: {"O-1"}},
		}
		srModel.historyView = NewHistoryModel(rep, DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		srModel.historyView.SetSize(120, 28)
		srModel.historyLoading = true
		srModel.historyLoadDataGeneration = srModel.semanticDataGeneration
		canceled := false
		srModel.historyLoadCancel = func() { canceled = true }
		prevGen := srModel.historyLoadRequestGeneration
		cmd := srModel.recordCorrelationFeedback("confirm")
		if cmd == nil || !canceled || srModel.historyLoadRequestGeneration <= prevGen {
			t.Fatalf("A14-3 regression: feedback during in-flight load must cancel old walk and return reload cmd (cmd=%v canceled=%v gen=%d->%d)", cmd != nil, canceled, prevGen, srModel.historyLoadRequestGeneration)
		}
		// A14-9: History Git mode footer advertises +/-:confirm/reject x:ignore.
		srModel.historyView.ToggleViewMode()
		gitDetail := ansi.Strip(srModel.historyView.renderGitDetailPanel(70, 24))
		if !strings.Contains(gitDetail, "+/-:confirm/reject") || !strings.Contains(gitDetail, "x:ignore") {
			t.Fatalf("A14-9 regression: History Git mode footer missing feedback hints:\n%s", gitDetail)
		}

		// A14-4: Repo-filtered activeScopePriorityHints & Structural Metrics isolate hidden-repo misalignment & cut-vertex.
		// api-root (P3) blocks 4 P0 web-* issues (giving it a global priority hint P3->P1 + global cut-vertex),
		// while inside repo=api alone it is an isolated P3 node with zero in-repo dependents (no hint, no cut-vertex).
		est60 := 60
		isoIssues := []model.Issue{
			{ID: "api-root", Title: "API Root", SourceRepo: "api", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "api-leaf", Title: "API Leaf", SourceRepo: "api", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "web-1", Title: "Web P0 1", SourceRepo: "web", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-1", DependsOnID: "api-root", Type: model.DepBlocks}}},
			{ID: "web-2", Title: "Web P0 2", SourceRepo: "web", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-2", DependsOnID: "api-root", Type: model.DepBlocks}}},
			{ID: "web-3", Title: "Web P0 3", SourceRepo: "web", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-3", DependsOnID: "api-root", Type: model.DepBlocks}}},
			{ID: "web-4", Title: "Web P0 4", SourceRepo: "web", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow,
				Dependencies: []*model.Dependency{{IssueID: "web-4", DependsOnID: "api-root", Type: model.DepBlocks}}},
			{ID: "api-tmpl", Title: "Template", SourceRepo: "api", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, IsTemplate: true, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow},
			{ID: "api-pin", Title: "Pinned", SourceRepo: "api", Status: model.StatusPinned, Priority: 2, IssueType: model.TypeTask, Pinned: true, EstimatedMinutes: &est60, CreatedAt: refNow, UpdatedAt: refNow},
		}
		isoM := NewModel(copyIssues(isoIssues), nil, "", ReadinessScope{ReferenceNow: refNow})
		t.Cleanup(isoM.Stop)
		isoM.analysis.WaitForPhase2()
		for _, rec := range isoM.analyzer.GenerateRecommendationsFromStats(isoM.analysis, analysis.DefaultThresholds()) {
			r := rec
			isoM.priorityHints[r.IssueID] = &r
		}
		isoM.EnableWorkspaceMode(WorkspaceInfo{Enabled: true, RepoCount: 2, TotalIssues: len(isoIssues), RepoPrefixes: []string{"api-", "web-"}})
		isoM.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
		if isoM.priorityHints["api-root"] == nil {
			t.Fatalf("A14-4 precondition failed: expected global priority hint for api-root blocking 4 P0 web issues")
		}
		isoM.activeRepos = map[string]bool{"api": true}
		isoM.applyFilter()
		if scopedHint := isoM.activeScopePriorityHints()["api-root"]; scopedHint != nil {
			t.Fatalf("A14-4 regression: repo-filtered activeScopePriorityHints leaked hidden-web priority hint: %+v", scopedHint)
		}
		isoM.selectListIssueByID("api-root")
		isoM.updateViewportContent()
		isoDetail := ansi.Strip(isoM.viewportContent)
		if strings.Contains(isoDetail, "Priority Hint:") || strings.Contains(isoDetail, "Articulation Point") {
			t.Fatalf("A14-4 regression: repo-filtered Detail Pane leaked hidden-web Priority Hint or Cut Vertex:\n%s", isoDetail)
		}

		// A14-8: Insights KPaths must NOT splice A -> HIDDEN -> B into A -> B; currentAdvancedInsightsKey uses membership hash.
		insM := NewInsightsModel(analysis.Insights{}, map[string]*model.Issue{"A": {ID: "A"}, "B": {ID: "B"}}, isoM.theme)
		insM.focusedPanel = PanelKeystones
		insM.SetAdvancedInsights(&analysis.AdvancedInsights{
			KPaths: &analysis.KPathsResult{Paths: []analysis.CriticalPath{
				{IssueIDs: []string{"A", "HIDDEN", "B"}, Length: 3},
			}},
		})
		insM.updateDetailContent()
		if strings.Contains(ansi.Strip(insM.detailContent), "A` → `B") || strings.Contains(ansi.Strip(insM.detailContent), "A → B") {
			t.Fatalf("A14-8 regression: Insights KPaths spliced hidden intermediate node into fabricated direct edge:\n%s", ansi.Strip(insM.detailContent))
		}
		isoM.candidateIDs = map[string]bool{"api-root": true}
		ak1 := isoM.currentAdvancedInsightsKey()
		isoM.candidateIDs = map[string]bool{"api-leaf": true}
		ak2 := isoM.currentAdvancedInsightsKey()
		if ak1 == ak2 {
			t.Fatalf("A14-8 regression: currentAdvancedInsightsKey collided for distinct equal-size candidateIDs")
		}

		// A14-9: Templates & pinned issues suppress ETA Forecast in Detail Pane, Board, and Graph.
		isoM.activeRepos = nil
		isoM.candidateIDs = nil
		isoM.applyFilter()
		for _, id := range []string{"api-tmpl", "api-pin"} {
			isoM.selectListIssueByID(id)
			isoM.updateViewportContent()
			if strings.Contains(ansi.Strip(isoM.viewportContent), "ETA Forecast:") {
				t.Fatalf("A14-9 regression: Detail Pane emitted ETA Forecast for %s:\n%s", id, ansi.Strip(isoM.viewportContent))
			}
			isoM.graphView.SelectByID(id)
			if strings.Contains(ansi.Strip(isoM.RenderDebugView("graph", 140, 48)), "Est. ETA:") {
				t.Fatalf("A14-9 regression: Graph View emitted Est. ETA for %s", id)
			}
		}
		bmCheck := NewBoardModel(copyIssues(isoIssues), DefaultTheme(lipgloss.NewRenderer(os.Stdout)))
		bmCheck.SetReferenceTime(refNow)
		bmCheck.SetAnalyzer(isoM.analyzer)
		bmCheck.SetAnalysis(isoM.analysis)
		bmCheck.SetETAContext(isoM.etaContext())
		bmCheck.ToggleDetail()
		bmCheck.SetSize(140, 30)
		if bmCheck.SelectIssueByID("api-tmpl") {
			if strings.Contains(ansi.Strip(bmCheck.View(140, 30)), "ETA Forecast:") {
				t.Fatalf("A14-9 regression: Board View emitted ETA Forecast for api-tmpl")
			}
		}
	})
}

func TestWave29_Pass15Fixes_And_W29_TUI(t *testing.T) {
	refNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	t.Run("A15-1_A15-2_A15-7_A15-11_PriorityHintsAndRepoScoping", func(t *testing.T) {
		t.Chdir(t.TempDir())
		// Build a 2-repo workspace where "api" has a high-impact P4 hub blocking 3 P0 bugs
		// (triggering a priority increase hint) while "web" has an isolated P2 leaf.
		issues := []model.Issue{
			{ID: "api-hub", Title: "Core API bottleneck", Status: model.StatusOpen, Priority: 4, IssueType: model.TypeTask, SourceRepo: "api", Labels: []string{"backend"}, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: refNow.Add(-24 * time.Hour)},
			{ID: "api-b1", Title: "Critical auth outage", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, SourceRepo: "api", Labels: []string{"backend"}, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-12 * time.Hour), Dependencies: []*model.Dependency{{IssueID: "api-b1", DependsOnID: "api-hub", Type: model.DepBlocks}}},
			{ID: "api-b2", Title: "Critical billing outage", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, SourceRepo: "api", Labels: []string{"backend"}, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-12 * time.Hour), Dependencies: []*model.Dependency{{IssueID: "api-b2", DependsOnID: "api-hub", Type: model.DepBlocks}}},
			{ID: "api-b3", Title: "Critical webhook outage", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, SourceRepo: "api", Labels: []string{"backend"}, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow.Add(-12 * time.Hour), Dependencies: []*model.Dependency{{IssueID: "api-b3", DependsOnID: "api-hub", Type: model.DepBlocks}}},
			{ID: "web-leaf", Title: "Isolated web tweak", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, SourceRepo: "web", Labels: []string{"frontend"}, CreatedAt: refNow.Add(-24 * time.Hour), UpdatedAt: refNow.Add(-6 * time.Hour)},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues)})
		defer m.Stop()
		m.referenceNow = refNow
		m.workspaceMode = true
		m.availableRepos = []string{"api", "web"}
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})

		// A15-1: Phase2ReadyMsg must populate priorityHints BEFORE pushing to insightsPanel and update list delegate.
		stats := m.analyzer.AnalyzeWithConfig(analysis.FullAnalysisConfig())
		m.analysis = &stats
		m.priorityHints = nil
		updated, _ := m.Update(Phase2ReadyMsg{Stats: &stats, Insights: stats.GenerateInsights(10)})
		m = updated.(*Model)
		if len(m.priorityHints) == 0 {
			t.Fatalf("A15-1 regression: Phase2ReadyMsg did not populate priorityHints for high-impact P4 hub")
		}
		// RenderDebugView("insights") should also populate hints cleanly when starting fresh.
		m.priorityHints = nil
		_ = m.RenderDebugView("insights", 140, 42)
		if len(m.priorityHints) == 0 {
			t.Fatalf("A15-1 regression: RenderDebugView(insights) did not populate priorityHints")
		}

		// A15-2: When filtering to "web" repo, activeScopePriorityHints() must exclude "api-hub"
		// and toggling 'p' must report 0 suggestions for "web" (not unscoped global hints).
		m.activeRepos = map[string]bool{"web": true}
		m.applyFilter()
		m.updateListDelegate()
		if scopedHints := m.activeScopePriorityHints(); len(scopedHints) != 0 {
			t.Fatalf("A15-2 regression: web-scoped priorityHints leaked api hints: %+v", scopedHints)
		}
		m.showPriorityHints = false
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		m = updated.(*Model)
		if strings.Contains(m.statusMsg, "1 suggestion") || strings.Contains(m.statusMsg, "suggestions") && !strings.Contains(m.statusMsg, "No priority") && !strings.Contains(m.statusMsg, "0 ") {
			t.Fatalf("A15-2 regression: 'p' toggle in web-only scope reported unscoped api hints: %q", m.statusMsg)
		}

		// A15-7: Label Dashboard must use repo-scoped analysis (only frontend label in web scope).
		m.refreshLabelViews(focusLabelDashboard)
		for _, lh := range m.labelDashboard.labels {
			if lh.Label == "backend" {
				t.Fatalf("A15-7 regression: web-scoped Label Dashboard leaked backend label: %+v", m.labelDashboard.labels)
			}
		}

		// A15-11: Detail Pane Graph Analysis must use repo-scoped stats (web-leaf has PageRank 1.0 in single-node web subgraph).
		m.selectListIssueByID("web-leaf")
		m.updateViewportContent()
		detailPlain := ansi.Strip(m.viewportContent)
		if !strings.Contains(detailPlain, "Graph Analysis") || !strings.Contains(detailPlain, "1.000") {
			t.Fatalf("A15-11 regression: Detail Pane Graph Analysis not scoped to single-node web repo:\n%s", detailPlain)
		}
	})

	t.Run("A15-5_CorrelationFeedbackTargetHardening", func(t *testing.T) {
		tmp := t.TempDir()
		realRepo := filepath.Join(tmp, "real_repo")
		beadsDir := filepath.Join(realRepo, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// metadata.json points jsonl_export to a custom JSONL file inside .beads
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"jsonl_export":"custom_export.jsonl"}`), 0o644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "custom_export.jsonl"), []byte(`{"id":"A-1","title":"Test","status":"open"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		linkRepo := filepath.Join(tmp, "link_repo")
		if err := os.Symlink(realRepo, linkRepo); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}

		wsIssues := []model.Issue{{
			ID:         "api-A-1",
			Title:      "Test",
			Status:     model.StatusOpen,
			SourceRepo: "api",
			Origin: &model.IssueOrigin{
				LocalID:          "A-1",
				WorkingDirectory: realRepo,
				TrackerDirectory: beadsDir,
			},
		}}
		m := NewModel(wsIssues, nil, filepath.Join(beadsDir, "custom_export.jsonl"), ReadinessScope{})
		defer m.Stop()
		m.workspaceMode = true
		// Pinned historical revision MUST be refused BEFORE falling back to origin path!
		m.workspaceInfo = WorkspaceInfo{
			Repos: []WorkspaceRepoTarget{
				{Name: "api", Prefix: "api-", Path: realRepo, BeadsDir: beadsDir, Revision: "HEAD~1"},
			},
		}
		m.beadsPath = filepath.Join(beadsDir, "custom_export.jsonl")
		if dir, _ := m.resolveCorrelationFeedbackTarget("api-A-1"); dir != "" {
			t.Fatalf("A15-5 regression: pinned revision workspace repo must refuse feedback write, got dir=%q", dir)
		}

		// Live workspace repo via symlinked path resolves cleanly to beadsDir honoring metadata.json!
		m.workspaceInfo = WorkspaceInfo{
			Repos: []WorkspaceRepoTarget{
				{Name: "api", Prefix: "api-", Path: linkRepo, BeadsDir: filepath.Join(linkRepo, ".beads"), Revision: ""},
			},
		}
		dir, localID := m.resolveCorrelationFeedbackTarget("api-A-1")
		if dir == "" || localID != "A-1" {
			t.Fatalf("A15-5 regression: symlinked live repo failed to resolve feedback target: dir=%q localID=%q", dir, localID)
		}
	})

	t.Run("W29-1_TimeTravelDetailDiffsAndCycleDeltas", func(t *testing.T) {
		t.Chdir(t.TempDir())
		issues := []model.Issue{
			{ID: "TT-1", Title: "Modified Task", Status: model.StatusInProgress, Priority: 0, IssueType: model.TypeTask, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow},
			{ID: "TT-2", Title: "Cycle Partner", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow},
		}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues)})
		defer m.Stop()
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
		m.timeTravelMode = true
		m.timeTravelDiff = &analysis.SnapshotDiff{
			FromRevision: "HEAD~3",
			ToRevision:   "HEAD",
			ModifiedIssues: []analysis.ModifiedIssue{
				{
					IssueID: "TT-1",
					Title:   "Modified Task",
					Changes: []analysis.FieldChange{
						{Field: "priority", OldValue: "P2", NewValue: "P0"},
						{Field: "status", OldValue: "open", NewValue: "in_progress"},
					},
				},
			},
			RemovedIssues: []model.Issue{
				{ID: "TT-OLD", Title: "Deleted Legacy Task", Status: model.StatusOpen, Priority: 2},
			},
			NewCycles: [][]string{{"TT-1", "TT-2"}},
		}
		m.timeTravelModMap = map[string]*analysis.ModifiedIssue{
			"TT-1": &m.timeTravelDiff.ModifiedIssues[0],
		}
		m.selectListIssueByID("TT-1")
		m.updateViewportContent()
		plain := ansi.Strip(m.viewportContent)
		for _, want := range []string{"priority", "P2", "P0", "Introduced Cycle", "TT-OLD"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("W29-1 regression: Time-Travel detail view missing %q:\n%s", want, plain)
			}
		}
	})

	t.Run("A15-6_And_W29-3_HistorySignalsStatsClusterAndOrphansMode", func(t *testing.T) {
		t.Chdir(t.TempDir())
		theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))
		hv := NewHistoryModel(&correlation.HistoryReport{
			GeneratedAt: refNow,
			DataHash:    "h1",
			Stats: correlation.HistoryStats{
				TotalBeads:        2,
				BeadsWithCommits:  1,
				TotalCommits:      2,
				UniqueAuthors:     2,
				AvgCommitsPerBead: 1.0,
				FeedbackApplied:   &correlation.FeedbackApplied{Confirmed: 2, Rejected: 1, Ignored: 1},
			},
			Histories: map[string]correlation.BeadHistory{
				"H-1": {
					BeadID: "H-1",
					Title:  "Auth refactor",
					Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{
							SHA:        "1111111111111111111111111111111111111111",
							ShortSHA:   "1111111",
							BeadID:     "H-1",
							Message:    "fix(H-1): harden auth token validation",
							Author:     "alice",
							Timestamp:  refNow.Add(-2 * time.Hour),
							Method:     correlation.MethodExplicitID,
							Confidence: 0.92,
							Files:      []correlation.FileChange{{Path: "token.go", Action: "M", Insertions: 12, Deletions: 3}},
						},
					},
				},
				"H-CLOSED": {
					BeadID: "H-CLOSED",
					Title:  "Old closed cleanup",
					Status: "closed",
					Commits: []correlation.CorrelatedCommit{
						{
							SHA:        "2222222222222222222222222222222222222222",
							ShortSHA:   "2222222",
							BeadID:     "H-CLOSED",
							Message:    "cleanup token.go",
							Author:     "bob",
							Timestamp:  refNow.Add(-4 * time.Hour),
							Method:     correlation.MethodCoCommitted,
							Confidence: 0.70,
							Files:      []correlation.FileChange{{Path: "token.go", Action: "M", Insertions: 2, Deletions: 1}},
						},
					},
				},
			},
			CommitIndex: map[string][]string{
				"1111111111111111111111111111111111111111": {"H-1"},
				"2222222222222222222222222222222222222222": {"H-CLOSED"},
			},
		}, theme)
		hv.SetSize(220, 40)
		hv.SetOrphans([]correlation.OrphanCandidate{
			{
				SHA:            "3333333333333333333333333333333333333333",
				ShortSHA:       "3333333",
				Message:        "hotfix token leak without bead tag",
				Author:         "carol",
				Timestamp:      refNow.Add(-1 * time.Hour),
				Files:          []string{"token.go"},
				SuspicionScore: 84,
				ProbableBeads: []correlation.ProbableBead{
					{BeadID: "H-1", BeadTitle: "Auth refactor", Confidence: 78},
				},
			},
		}, 0.33)

		// W29-3: Stats line shows feedback + orphans; detail shows Signals explanation.
		beadView := ansi.Strip(hv.View())
		for _, want := range []string{"✓2", "1 orphan", "Signals:"} {
			if !strings.Contains(beadView, want) {
				t.Fatalf("W29-3 regression: History bead view missing %q:\n%s", want, beadView)
			}
		}

		// A15-6: File tree blast-radius footer must label H-CLOSED as "recent", not "open".
		hv.ToggleFileTree()
		treeView := ansi.Strip(hv.View())
		if strings.Contains(treeView, "2 open") {
			t.Fatalf("A15-6 regression: File tree blast-radius mislabeled closed bead as open:\n%s", treeView)
		}
		if !strings.Contains(treeView, "recent") {
			t.Fatalf("A15-6 regression: File tree blast-radius missing 'recent' closed count:\n%s", treeView)
		}
		hv.ToggleFileTree()

		// W29-3: ToggleOrphansMode ('O') switches to Orphans view while 'v' stays Bead<->Git.
		hv.ToggleOrphansMode()
		if !hv.IsOrphansMode() {
			t.Fatalf("W29-3 regression: ToggleOrphansMode failed to activate historyModeOrphans")
		}
		orphView := ansi.Strip(hv.View())
		for _, want := range []string{"ORPHAN COMMIT", "3333333", "84", "H-1"} {
			if !strings.Contains(orphView, want) {
				t.Fatalf("W29-3 regression: Orphans view missing %q:\n%s", want, orphView)
			}
		}
		if pb := hv.SelectedOrphanProbableBead(); pb == nil || pb.BeadID != "H-1" {
			t.Fatalf("W29-3 regression: SelectedOrphanProbableBead expected H-1, got %+v", pb)
		}
	})

	t.Run("W29-6_AlertsPanelBaselineSaveAndGuards", func(t *testing.T) {
		tmp := t.TempDir()
		beadsDir := filepath.Join(tmp, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"B-1","title":"Task","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}
		t.Chdir(tmp)

		issues := []model.Issue{{ID: "B-1", Title: "Task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
		m := NewModel(copyIssues(issues), nil, jsonlPath, ReadinessScope{Authority: model.NewReadinessIndex(issues)})
		defer m.Stop()
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})

		// Guard: BV_NO_SAVED_CONFIG blocks baseline save (default in pkg/ui TestMain).
		t.Setenv("BV_NO_SAVED_CONFIG", "1")
		if reason := m.baselineSaveBlockedReason(); !strings.Contains(reason, "BV_NO_SAVED_CONFIG") {
			t.Fatalf("W29-6 regression: expected BV_NO_SAVED_CONFIG guard, got %q", reason)
		}
		t.Setenv("BV_NO_SAVED_CONFIG", "")

		// Guard: time-travel mode blocks baseline save.
		m.timeTravelMode = true
		if reason := m.baselineSaveBlockedReason(); reason == "" {
			t.Fatalf("W29-6 regression: expected timeTravelMode to block baseline save")
		}
		m.timeTravelMode = false

		// Open Alerts panel (Alerts tab = 0) and press 'S' -> writes .bv/baseline.json.
		m.showAlertsPanel = true
		m.alertsTab = 0
		panelBefore := ansi.Strip(m.renderAlertsPanel())
		if !strings.Contains(panelBefore, "Baseline:") || !strings.Contains(panelBefore, "S: save") {
			t.Fatalf("W29-6 regression: Alerts panel missing baseline status or S footer hint:\n%s", panelBefore)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
		m = updated.(*Model)
		baselinePath := filepath.Join(tmp, ".bv", "baseline.json")
		if _, err := os.Stat(baselinePath); err != nil {
			t.Fatalf("W29-6 regression: pressing 'S' on Alerts tab did not create %s (status=%q): %v", baselinePath, m.statusMsg, err)
		}
		// Suggestions tab (alertsTab == 1) must NOT overwrite baseline on 'S'.
		m.alertsTab = 1
		m.statusMsg = ""
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
		m = updated.(*Model)
		if strings.Contains(m.statusMsg, "Baseline saved") {
			t.Fatalf("W29-6 regression: pressing 'S' on Suggestions tab must not trigger baseline save: %q", m.statusMsg)
		}
	})

	t.Run("MicroWinA_SplitDetailCassSessionHint", func(t *testing.T) {
		t.Chdir(t.TempDir())
		issues := []model.Issue{{ID: "CASS-1", Title: "Cache invalidation bug", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug}}
		m := NewModel(copyIssues(issues), nil, "", ReadinessScope{Authority: model.NewReadinessIndex(issues)})
		defer m.Stop()
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})

		cache := cass.NewCache()
		cache.Set("CASS-1", &cass.CorrelationHint{
			BeadID:      "CASS-1",
			ResultCount: 3,
			Results:     []cass.ScoredResult{{SearchResult: cass.SearchResult{Title: "Debug cache"}}},
		})
		m.cassCorrelator = cass.NewCorrelator(nil, cache, t.TempDir())
		m.selectListIssueByID("CASS-1")
		updated, _ := m.Update(cassPrefetchDoneMsg{
			gen:    m.cassPrefetchGen,
			beadID: "CASS-1",
			result: cass.CorrelationResult{
				TopSessions: []cass.ScoredResult{{SearchResult: cass.SearchResult{Title: "Debug cache"}}},
			},
		})
		m = updated.(*Model)
		plain := ansi.Strip(m.viewportContent)
		if !strings.Contains(plain, "Agent Sessions:") || !strings.Contains(plain, "3 correlated cass session") {
			t.Fatalf("Micro-Win A regression: Detail Pane missing cached CASS session hint:\n%s", plain)
		}

		// P16A-12: Empty cached Results must NOT advertise "Agent Sessions:" even if ResultCount > 0.
		cache.Set("CASS-1", &cass.CorrelationHint{BeadID: "CASS-1", ResultCount: 2, Results: nil})
		m.updateViewportContent()
		if strings.Contains(ansi.Strip(m.viewportContent), "Agent Sessions:") {
			t.Fatalf("P16A-12 regression: empty cached Results leaked Agent Sessions hint")
		}
	})

	t.Run("Pass16_TrackA_Regressions", func(t *testing.T) {
		tmp := t.TempDir()
		beadsDir := filepath.Join(tmp, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		closedAt := refNow.Add(-12 * time.Hour)
		issues := []model.Issue{
			{ID: "P16-CLOSED", Title: "Closed prereq", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-72 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
			{ID: "P16-OPEN", Title: "Active child", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: refNow.Add(-48 * time.Hour), UpdatedAt: refNow, Dependencies: []*model.Dependency{{IssueID: "P16-OPEN", DependsOnID: "P16-CLOSED", Type: model.DepBlocks}}},
		}
		var jb strings.Builder
		for _, iss := range issues {
			raw, _ := json.Marshal(iss)
			jb.Write(raw)
			jb.WriteByte('\n')
		}
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jb.String()), 0o644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}
		t.Chdir(tmp)
		t.Setenv("BV_NO_SAVED_CONFIG", "")

		m := NewModel(copyIssues(issues), nil, jsonlPath, ReadinessScope{Authority: model.NewReadinessIndex(issues)})
		defer m.Stop()
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})

		// P16A-1 + P16A-7: Saving baseline with Phase 2 complete uses CriticalPathScore()
		// so immediately recomputed alerts have ZERO false-positive critical-path drift!
		m.analysis.WaitForPhase2()
		m.alertsCursor = 99
		m.showAlertsPanel = true
		m.alertsTab = 0
		m.saveBaselineInteractive()
		if !strings.Contains(m.statusMsg, "Baseline saved") {
			t.Fatalf("P16A-1 setup: saveBaselineInteractive failed: %q", m.statusMsg)
		}
		for _, a := range m.alerts {
			if strings.Contains(strings.ToLower(a.Message), "critical") {
				t.Fatalf("P16A-1 regression: fresh baseline save triggered false-positive CriticalPath drift alert: %+v", a)
			}
		}
		if len(m.activeScopedAlerts()) == 0 && m.alertsCursor != 0 {
			t.Fatalf("P16A-7 regression: alertsCursor not clamped after saveBaselineInteractive: %d", m.alertsCursor)
		}

		// P16A-5 + P16A-6 + P16A-8: Orphans mode disables FileTree/Timeline, clears on SetReport(nil),
		// filters out inactive-repo orphans, and handles MoveToStart/MoveToEnd cleanly.
		theme := DefaultTheme(lipgloss.NewRenderer(os.Stdout))
		hv := NewHistoryModel(&correlation.HistoryReport{GeneratedAt: refNow}, theme)
		hv.SetSize(140, 30)
		hv.ToggleFileTree()
		hv.SetOrphans([]correlation.OrphanCandidate{
			{SHA: "aaa1111", ShortSHA: "aaa1111", Author: "alice", Message: "first", SuspicionScore: 90, ProbableBeads: []correlation.ProbableBead{{BeadID: "A-1", Confidence: 80}}},
			{SHA: "bbb2222", ShortSHA: "bbb2222", Author: "bob", Message: "second", SuspicionScore: 60},
		}, 0.5)
		hv.ToggleOrphansMode()
		if hv.FileTreeRendered() || hv.TimelineAvailable() {
			t.Fatalf("P16A-5 regression: Orphans mode must disable FileTreeRendered and TimelineAvailable")
		}
		hv.MoveToEnd()
		if sel := hv.SelectedOrphan(); sel == nil || sel.ShortSHA != "bbb2222" {
			t.Fatalf("P16A-8 regression: MoveToEnd in Orphans mode got %+v", sel)
		}
		hv.MoveToStart()
		if sel := hv.SelectedOrphan(); sel == nil || sel.ShortSHA != "aaa1111" {
			t.Fatalf("P16A-8 regression: MoveToStart in Orphans mode got %+v", sel)
		}
		hv.SetAuthorFilter("bob")
		if sel := hv.SelectedOrphan(); sel == nil || sel.ShortSHA != "bbb2222" {
			t.Fatalf("P16A-8 regression: author filter in Orphans mode got %+v", sel)
		}
		hv.SetReport(nil)
		if hv.OrphanCount() != 0 {
			t.Fatalf("P16A-6 regression: SetReport(nil) retained stale orphans (%d)", hv.OrphanCount())
		}
		m.workspaceMode = true
		m.activeRepos = map[string]bool{"api": true}
		m.historyOrphanRepoKeys = map[string]string{"aaa1111": "api", "bbb2222": "web"}
		scopedOrphs, _ := m.scopeHistoryOrphansForActiveRepos([]correlation.OrphanCandidate{
			{SHA: "aaa1111"}, {SHA: "bbb2222"},
		})
		if len(scopedOrphs) != 1 || scopedOrphs[0].SHA != "aaa1111" {
			t.Fatalf("P16A-6 regression: inactive-repo orphan leaked through scopeHistoryOrphansForActiveRepos: %+v", scopedOrphs)
		}

		// P16A-10: Empty/backtick/newline field changes render clean markdown & open cycle closes cleanly.
		m.workspaceMode = false
		m.activeRepos = nil
		m.timeTravelMode = true
		m.timeTravelDiff = &analysis.SnapshotDiff{
			FromRevision: "HEAD~1",
			ModifiedIssues: []analysis.ModifiedIssue{{
				IssueID: "P16-OPEN",
				Changes: []analysis.FieldChange{
					{Field: "assignee", OldValue: "", NewValue: "alice"},
					{Field: "title", OldValue: "Fix `foo`\nbar", NewValue: "Fix `baz`"},
				},
			}},
			NewCycles: [][]string{{"P16-OPEN", "P16-CLOSED"}},
		}
		m.timeTravelModMap = map[string]*analysis.ModifiedIssue{"P16-OPEN": &m.timeTravelDiff.ModifiedIssues[0]}
		var ttBuf strings.Builder
		m.appendTimeTravelIssueDiffMarkdown(&ttBuf, "P16-OPEN")
		ttOut := ttBuf.String()
		if strings.Contains(ttOut, "**assignee:** ``") || !strings.Contains(ttOut, "_(none)_") || !strings.Contains(ttOut, "P16-OPEN` → `P16-CLOSED` → `P16-OPEN") {
			t.Fatalf("P16A-10 regression: Time-Travel markdown formatting broken:\n%s", ttOut)
		}
	})
}

func TestWave30_Pass17AndTUIPowerUpgrades(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	// 1. P17-2: Interactive recipe switch never corrupts baseline Scope.Recipe (pinned to startup scopeRecipeFilter).
	t.Run("P17-2_BaselineScopeRecipeImmutableOnInteractiveSwitch", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "B-1", Title: "Alpha", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
			{ID: "B-2", Title: "Beta", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		if opts := m.alertOptions(); opts.Scope != nil && opts.Scope.Recipe != "" {
			t.Fatalf("expected unscoped startup alertOptions().Scope.Recipe == \"\", got %+v", opts.Scope)
		}
		// Simulate interactive recipe picker switch: m.activeRecipe changes, m.candidateIDs does not.
		m.setActiveRecipe(&recipe.Recipe{Name: "high-impact"})
		if opts := m.alertOptions(); opts.Scope != nil && opts.Scope.Recipe != "" {
			t.Fatalf("P17-2 regression: interactive recipe switch polluted alertOptions().Scope.Recipe: %+v", opts.Scope)
		}
		// Startup with an active recipe pins scopeRecipeFilter.
		mScoped := NewModel(issues, &recipe.Recipe{Name: "actionable"}, "")
		defer mScoped.Stop()
		mScoped.setActiveRecipe(&recipe.Recipe{Name: "high-impact"})
		if opts := mScoped.alertOptions(); opts.Scope == nil || opts.Scope.Recipe != "actionable" {
			t.Fatalf("P17-2 regression: startup scopeRecipeFilter overwritten on interactive switch: %+v", opts.Scope)
		}
	})

	// 2. P17-3: Workspace repo picker Enter exits stale time-travel when newly selected repo has no git history.
	t.Run("P17-3_RepoPickerTimeTravelFailureClearsStaleDiff", func(t *testing.T) {
		noGitDir := t.TempDir()
		issues := []model.Issue{
			{ID: "api-1", Title: "API work", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api",
				Origin: &model.IssueOrigin{LocalID: "1", WorkingDirectory: noGitDir, TrackerDirectory: filepath.Join(noGitDir, ".beads")}},
			{ID: "web-1", Title: "Web work", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "web",
				Origin: &model.IssueOrigin{LocalID: "1", WorkingDirectory: noGitDir, TrackerDirectory: filepath.Join(noGitDir, ".beads")}},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.workspaceMode = true
		m.availableRepos = []string{"api", "web"}
		m.repoPicker = NewRepoPickerModel([]string{"api", "web"}, m.theme)
		m.showRepoPicker = true
		m.focused = focusRepoPicker
		// Plant active time-travel diff that must be cleared when re-entry fails on non-git repo.
		m.timeTravelMode = true
		m.timeTravelSince = "HEAD~1"
		m.timeTravelDiff = &analysis.SnapshotDiff{FromRevision: "HEAD~1", Summary: analysis.DiffSummary{IssuesAdded: 2}}
		m.newIssueIDs = map[string]bool{"api-1": true}

		m.handleRepoPickerKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if m.timeTravelMode || m.timeTravelDiff != nil || len(m.newIssueIDs) > 0 {
			t.Fatalf("P17-3 regression: failed time-travel re-entry left stale timeTravelMode=%v diff=%v newIDs=%v",
				m.timeTravelMode, m.timeTravelDiff, m.newIssueIDs)
		}
		if !m.statusIsError || !strings.Contains(m.statusMsg, "Time-travel") {
			t.Fatalf("P17-3 regression: expected descriptive error status on failed re-entry, got err=%v msg=%q", m.statusIsError, m.statusMsg)
		}
	})

	// 3. P17-7 & W30-2: History Orphans mode isolation + Causality & Wait panel (honoring *Known flags).
	t.Run("P17-7_And_W30-2_HistoryOrphansAndCausality", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "H-1", Title: "History primary", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
			{ID: "H-2", Title: "Orphan probable match", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.width = 130
		m.height = 40
		m.historyView.SetSize(128, 38)

		// Unloaded history + 0 orphans even if in Orphans mode renders empty state cleanly.
		m.historyView.ToggleOrphansMode()
		if v := ansi.Strip(m.historyView.View()); !strings.Contains(v, "No history data loaded") {
			t.Fatalf("P17-7 regression: unloaded Orphans view should render 'No history data loaded', got:\n%s", v)
		}
		m.historyView.ToggleOrphansMode() // back to Bead mode

		rep := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"H-1": {
					BeadID: "H-1", Title: "History primary", Status: "open",
					Commits: []correlation.CorrelatedCommit{{SHA: "111111111111", ShortSHA: "1111111", Message: "feat H-1", Author: "alice", Timestamp: now.Add(-2 * time.Hour), Confidence: 0.9}},
				},
			},
		}
		m.historyFullReport = rep
		m.historyReportDataGeneration = m.semanticDataGeneration
		m.historyView.SetReport(rep)
		m.historyView.SetOrphans([]correlation.OrphanCandidate{
			{
				SHA: "222222222222", ShortSHA: "2222222", Message: "orphan commit", Author: "bob", SuspicionScore: 65,
				ProbableBeads: []correlation.ProbableBead{{BeadID: "H-2", BeadTitle: "Orphan probable match", Confidence: 70}},
			},
		}, 0.50)

		m.isHistoryView = true
		m.focused = focusHistory
		m.historyView.ToggleOrphansMode()

		// P17-7: Focused issue / actions resolve orphan probable bead H-2 (NOT hidden Bead-mode H-1).
		if sessIss := m.focusedIssueForSessions(); sessIss == nil || sessIss.ID != "H-2" {
			t.Fatalf("P17-7 regression: focusedIssueForSessions in Orphans mode = %+v, want H-2", sessIss)
		}
		if actIss, _ := m.selectedIssueForActionWithReason(); actIss == nil || actIss.ID != "H-2" {
			t.Fatalf("P17-7 regression: selectedIssueForActionWithReason in Orphans mode = %+v, want H-2", actIss)
		}
		// Filter line in Orphans mode shows orphan counts, NOT bead counts.
		filterLine := ansi.Strip(m.historyView.renderFilterLine())
		if !strings.Contains(filterLine, "orphan commits") || strings.Contains(filterLine, "beads with commits") {
			t.Fatalf("P17-7 regression: Orphans mode renderFilterLine leaked bead counts: %q", filterLine)
		}
		// Pressing 'c' in Orphans mode must NOT mutate hidden minConfidence.
		m.historyView.SetMinConfidence(0)
		m, _ = m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		if m.historyView.GetMinConfidence() != 0 {
			t.Fatalf("P17-7 regression: 'c' in Orphans mode mutated hidden minConfidence to %v", m.historyView.GetMinConfidence())
		}
		// W in Orphans mode informs user instead of starting causality.
		m, cmdOrph := m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
		if cmdOrph != nil || !strings.Contains(m.statusMsg, "Causality requires Bead or Git") {
			t.Fatalf("W30-2 regression: W in Orphans mode should refuse cleanly, got cmd=%v msg=%q", cmdOrph, m.statusMsg)
		}

		// Return to Bead mode and test W30-2 Causality & Wait rendering honoring *Known flags.
		m.historyView.ToggleOrphansMode()
		longest := 4 * time.Hour
		m.historyView.SetCausality("H-1", &correlation.CausalityResult{
			Chain: &correlation.CausalChain{BeadID: "H-1", Title: "History primary", Status: "open"},
			Insights: &correlation.CausalInsights{
				Coverage:                "partial",
				DurationKnown:           true,
				TotalDuration:           10 * time.Hour,
				BlockedDurationKnown:    false, // Must NOT render fabricated "Blocked: 0s"!
				BlockedDuration:         0,
				DependencyDurationKnown: true,
				DependencyWaitDuration:  3 * time.Hour,
				LongestGap:              &longest,
				LongestGapDesc:          "waiting on upstream",
				Recommendations:         []string{"Resolve dependency wait earlier"},
				Limitations:             []string{"Explicit blocked window incomplete"},
			},
		})
		m, cmdCached := m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
		if cmdCached != nil || !m.historyView.ShowCausality() {
			t.Fatalf("W30-2 regression: W with cached causality should open immediately without async cmd")
		}
		rendered := ansi.Strip(m.historyView.View())
		for _, want := range []string{"CAUSALITY & WAIT [partial]", "Total: 10.0h", "Dep-Wait: 3.0h", "Longest gap: 4.0h", "Explicit blocked window incomplete"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("W30-2 regression: History detail missing %q:\n%s", want, rendered)
			}
		}
		if strings.Contains(rendered, "Blocked: 0s") {
			t.Fatalf("W30-2 regression: fabricated Blocked: 0s when BlockedDurationKnown=false:\n%s", rendered)
		}
		// Second W toggles panel off.
		m, _ = m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
		if m.historyView.ShowCausality() {
			t.Fatalf("W30-2 regression: second W did not toggle causality panel off")
		}
	})

	// 4. P17-9 & P17-10: Workspace scoped orphan ratio & safe markdown code-span fencing.
	t.Run("P17-9_ScopedOrphanRatio_And_P17-10_MarkdownFencing", func(t *testing.T) {
		m := NewModel(nil, nil, "")
		defer m.Stop()
		m.workspaceMode = true
		m.historyOrphanRatio = 0.30
		m.historyOrphanRootStats = []HistoryOrphanRootStat{
			{RepoKeys: []string{"api"}, OrphanCount: 4, TotalCommits: 10},
			{RepoKeys: []string{"web"}, OrphanCount: 2, TotalCommits: 10},
		}
		m.historyOrphanRepoKeys = map[string]string{"aaa": "api", "bbb": "web"}
		orphs := []correlation.OrphanCandidate{{SHA: "aaa"}, {SHA: "bbb"}}

		m.activeRepos = map[string]bool{"api": true}
		gotOrphs, gotRatio := m.scopeHistoryOrphansForActiveRepos(orphs)
		if len(gotOrphs) != 1 || gotOrphs[0].SHA != "aaa" || gotRatio < 0.399 || gotRatio > 0.401 {
			t.Fatalf("P17-9 regression: api scoped orphans=%d ratio=%v, want 1 & 0.40", len(gotOrphs), gotRatio)
		}
		m.activeRepos = map[string]bool{"web": true}
		gotOrphs, gotRatio = m.scopeHistoryOrphansForActiveRepos(orphs)
		if len(gotOrphs) != 1 || gotOrphs[0].SHA != "bbb" || gotRatio < 0.199 || gotRatio > 0.201 {
			t.Fatalf("P17-9 regression: web scoped orphans=%d ratio=%v, want 1 & 0.20", len(gotOrphs), gotRatio)
		}

		// P17-10: backtick & newline in since / removed / cycles safely fenced.
		m.timeTravelSince = "v1`bad\nref"
		m.timeTravelDiff = &analysis.SnapshotDiff{
			RemovedIssues:  []model.Issue{{ID: "rem`1"}},
			NewCycles:      [][]string{{"c`1", "c`2"}},
			ResolvedCycles: [][]string{{"r`1", "r`2"}},
		}
		var sb strings.Builder
		m.appendTimeTravelOverviewMarkdown(&sb)
		out := sb.String()
		if !strings.Contains(out, "``v1`bad ref``") || !strings.Contains(out, "``rem`1``") || !strings.Contains(out, "``c`1`` → ``c`2`` → ``c`1``") {
			t.Fatalf("P17-10 regression: backtick/newline fencing failed:\n%s", out)
		}
	})

	// 5. W30-3: Graph-aware List Sorting (Triage, Unblocks, Risk, ETA) + Graph View Structural/Risk/Cycle-Break Parity.
	t.Run("W30-3_GraphAwareListSortAndGraphViewParity", func(t *testing.T) {
		estFast := 60
		estSlow := 480
		oldTime := now.Add(-90 * 24 * time.Hour)
		closedAt := now.Add(-24 * time.Hour)
		issues := []model.Issue{
			{ID: "S-ROOT", Title: "Keystone blocker", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"core", "backend"}, CreatedAt: oldTime, UpdatedAt: oldTime, EstimatedMinutes: &estFast},
			{ID: "S-DEP1", Title: "Blocked slow high-risk", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug, Labels: []string{"core", "frontend"}, SourceRepo: "web", CreatedAt: oldTime, UpdatedAt: oldTime, EstimatedMinutes: &estSlow,
				Dependencies: []*model.Dependency{{IssueID: "S-DEP1", DependsOnID: "S-ROOT", Type: model.DepBlocks}}},
			{ID: "S-DEP2", Title: "Second dependent", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: "S-DEP2", DependsOnID: "S-ROOT", Type: model.DepBlocks}}},
			{ID: "S-CLOSED", Title: "Done item", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: closedAt, ClosedAt: &closedAt},
			{ID: "S-TMPL", Title: "Template item", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, IsTemplate: true, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.referenceNow = now
		m.analysis.WaitForPhase2()
		m.triageScores = map[string]float64{"S-ROOT": 0.95, "S-DEP1": 0.40, "S-DEP2": 0.60, "S-CLOSED": 0.99, "S-TMPL": 0.99}
		m.unblocksMap = map[string][]string{"S-ROOT": {"S-DEP1", "S-DEP2"}, "S-DEP2": {"S-DEP1"}}

		orderedIDs := func(mode SortMode) []string {
			m.sortMode = mode
			m.applyFilter()
			var ids []string
			for _, it := range m.list.Items() {
				if ii, ok := it.(IssueItem); ok {
					ids = append(ids, ii.Issue.ID)
				}
			}
			return ids
		}
		if got := orderedIDs(SortTriage); got[0] != "S-ROOT" || got[len(got)-1] != "S-TMPL" && got[len(got)-2] != "S-CLOSED" {
			t.Fatalf("W30-3 SortTriage expected S-ROOT first and closed/template last, got %v", got)
		}
		if got := orderedIDs(SortUnblocks); got[0] != "S-ROOT" || got[1] != "S-DEP2" {
			t.Fatalf("W30-3 SortUnblocks expected [S-ROOT S-DEP2 ...], got %v", got)
		}
		if got := orderedIDs(SortETA); got[0] != "S-ROOT" {
			t.Fatalf("W30-3 SortETA expected fastest active issue S-ROOT first, got %v", got)
		}
		riskOrder := orderedIDs(SortRisk)
		// Closed and template issues must rank after all active issues in SortRisk.
		for idx, id := range riskOrder[:3] {
			if id == "S-CLOSED" || id == "S-TMPL" {
				t.Fatalf("W30-3 SortRisk ranked ineligible issue %s at index %d: %v", id, idx, riskOrder)
			}
		}

		// Graph View structural & risk parity: K-Core, Slack (acyclic), Cut Vertex, Operational Risk.
		ins := m.generateModelInsights(m.analysis, len(issues))
		gm := NewGraphModel(issues, &ins, m.theme)
		gm.SetAnalyzer(m.analyzer, func() time.Time { return now })
		gm.SetETAContext(m.etaContext())
		panel := ansi.Strip(gm.renderMetricsPanel("S-ROOT", 64, m.theme))
		for _, want := range []string{"Connections & Structure", "K-Core:", "Slack:", "Cut Vertex"} {
			if !strings.Contains(panel, want) {
				t.Fatalf("W30-3 GraphView metrics panel missing %q:\n%s", want, panel)
			}
		}

		// Cyclic graph view shows scoped Cycle-Break cut suggestion and suppresses Slack.
		cycIssues := []model.Issue{
			{ID: "C-A", Title: "Cycle A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime,
				Dependencies: []*model.Dependency{{IssueID: "C-A", DependsOnID: "C-B", Type: model.DepBlocks}}},
			{ID: "C-B", Title: "Cycle B", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime,
				Dependencies: []*model.Dependency{{IssueID: "C-B", DependsOnID: "C-A", Type: model.DepBlocks}}},
		}
		cycAn := analysis.NewAnalyzer(cycIssues)
		cycAn.SetNow(now)
		cycStats := cycAn.Analyze()
		cycIns := cycStats.GenerateInsights(10)
		gmCyc := NewGraphModel(cycIssues, &cycIns, m.theme)
		gmCyc.SetAnalyzer(cycAn, func() time.Time { return now })
		cycPanel := ansi.Strip(gmCyc.renderMetricsPanel("C-A", 72, m.theme))
		if strings.Contains(cycPanel, "Slack:") {
			t.Fatalf("W30-3 regression: cyclic GraphView must suppress Slack:\n%s", cycPanel)
		}
		if !strings.Contains(cycPanel, "Cut cycle:") {
			t.Fatalf("W30-3 regression: cyclic GraphView missing Cycle-Break suggestion:\n%s", cycPanel)
		}

		// W30-5: TUI Label Health Detail surfaces co-occurring labels.
		m.labelHealthDetail = &analysis.LabelHealth{Label: "core", Health: 75, HealthLevel: analysis.HealthLevelWarning, IssueCount: 2, OpenCount: 2}
		m.populateLabelHealthDetailExtras("core")
		lhOut := ansi.Strip(m.renderLabelHealthDetail(*m.labelHealthDetail))
		if !strings.Contains(lhOut, "Co-occurs with:") || !strings.Contains(lhOut, "backend") {
			t.Fatalf("W30-5 regression: TUI Label Health Detail missing Co-occurs with:\n%s", lhOut)
		}
	})
}

func TestPass18_TrackARegressions(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-10 * 24 * time.Hour)

	t.Run("P18A-1_FocusHistoryRealUpdateDispatch", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "H-1", Title: "History issue", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.width = 140
		m.height = 40
		m.isHistoryView = true
		m.focused = focusHistory
		m.historyView.SetSize(140, 39)
		report := &correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"H-1": {
					BeadID: "H-1",
					Title:  "History issue",
					Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "abc1234567890", ShortSHA: "abc1234", Message: "feat: H-1", Author: "alice", Timestamp: oldTime, Confidence: 0.9},
					},
				},
			},
			CommitIndex: map[string][]string{"abc1234567890": {"H-1"}},
		}
		m.historyFullReport = report
		m.historyLoading = false
		m.historyReportDataGeneration = m.semanticDataGeneration
		m.historyView.SetReport(report)
		m.historyView.SetOrphans([]correlation.OrphanCandidate{
			{SHA: "def5678901234", ShortSHA: "def5678", Message: "unlinked fix", Author: "bob", Timestamp: oldTime, SuspicionScore: 60},
		}, 0.5)

		// Press O via real m.Update -> must toggle Orphans mode (NOT launch $EDITOR).
		um, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
		m = um.(*Model)
		if !m.historyView.IsOrphansMode() {
			t.Fatalf("P18A-1 regression: pressing O via m.Update in focusHistory did not toggle Orphans mode (status=%q)", m.statusMsg)
		}
		if cmd != nil {
			t.Fatalf("P18A-1 regression: pressing O in focusHistory returned unexpected command (likely $EDITOR hijacked O)")
		}
		// Exit orphans mode with O again.
		um, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
		m = um.(*Model)
		if m.historyView.IsOrphansMode() {
			t.Fatalf("P18A-1 regression: second O did not exit Orphans mode")
		}

		// Press W via real m.Update -> must enter causality loading and return an async tea.Cmd.
		um, wCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
		m = um.(*Model)
		if !m.historyView.ShowCausality() || wCmd == nil {
			t.Fatalf("P18A-1 regression: pressing W via m.Update in focusHistory did not trigger causality load (show=%v cmdNil=%v)", m.historyView.ShowCausality(), wCmd == nil)
		}
		m.cancelCausalityLoad()

		// Press x via real m.Update -> must route to history correlation ignore, NOT open Markdown export modal.
		um, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		m = um.(*Model)
		if m.focused != focusHistory {
			t.Fatalf("P18A-1 regression: pressing x via m.Update in focusHistory hijacked focus=%v", m.focused)
		}

		// Verify keybindings registry does not advertise O -> Open in $EDITOR for history view.
		for _, kb := range GetKeyBindingDocs() {
			if kb.Key == "O" && strings.Contains(kb.Desc, "$EDITOR") && strings.Contains(kb.Context, "history") {
				t.Fatalf("P18A-1 regression: keybindings registry still lists O ($EDITOR) in history view: %+v", kb)
			}
		}
	})

	t.Run("P18A-2_GitModeDetailPanelRendersCausality", func(t *testing.T) {
		h := NewHistoryModel(&correlation.HistoryReport{
			Histories: map[string]correlation.BeadHistory{
				"G-1": {
					BeadID: "G-1",
					Title:  "Git mode bead",
					Status: "open",
					Commits: []correlation.CorrelatedCommit{
						{SHA: "aaa1111222233", ShortSHA: "aaa1111", Message: "feat: G-1", Author: "alice", Timestamp: oldTime, Confidence: 0.9},
					},
				},
			},
			CommitIndex: map[string][]string{"aaa1111222233": {"G-1"}},
		}, DefaultTheme(lipgloss.NewRenderer(&strings.Builder{})))
		h.SetSize(120, 36)
		h.ToggleViewMode()
		if !h.IsGitMode() {
			t.Fatalf("expected git mode active")
		}
		h.SetCausality("G-1", &correlation.CausalityResult{
			Insights: &correlation.CausalInsights{
				Coverage:             "complete",
				DurationKnown:        true,
				TotalDuration:        48 * time.Hour,
				BlockedDurationKnown: true,
				BlockedDuration:      12 * time.Hour,
				BlockedPercentage:    25,
			},
		})
		h.SetShowCausality(true)
		panel := ansi.Strip(h.renderGitDetailPanel(72, 34))
		if !strings.Contains(panel, "CAUSALITY & WAIT") || !strings.Contains(panel, "Blocked: 12.0h") {
			t.Fatalf("P18A-2 regression: Git mode detail panel did not render causality section:\n%s", panel)
		}
		if !strings.Contains(panel, "COMMIT DETAILS") {
			t.Fatalf("P18A-2 regression: Git mode detail panel dropped COMMIT DETAILS:\n%s", panel)
		}
	})

	t.Run("P18A-7_ScopeRecipeFilterBypassesPreparedGlobalAlerts", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A-1", Title: "Alpha", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.scopeRecipeFilter = "actionable"
		m.activeRecipe = nil
		sentinel := []drift.Alert{{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, Message: "UNSCOPED_SENTINEL"}}
		um, _ := m.Update(Phase2ReadyMsg{
			Stats:    m.analysis,
			prepared: &DataSnapshot{alerts: sentinel, alertsWarning: 1},
		})
		m = um.(*Model)
		for _, a := range m.alerts {
			if a.Message == "UNSCOPED_SENTINEL" {
				t.Fatalf("P18A-7 regression: Phase2ReadyMsg installed unscoped prepared alerts while scopeRecipeFilter=%q", m.scopeRecipeFilter)
			}
		}
	})

	t.Run("P18A-8_CausalityErrorResetsShowFlagAndRefTimeClearsCache", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "C-1", Title: "Causality bead", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.historyView.SetShowCausality(true)
		m.causalityRequestGen = 7
		um, _ := m.Update(CausalityLoadedMsg{
			DataGeneration:    m.semanticDataGeneration,
			RequestGeneration: 7,
			BeadID:            "C-1",
			Error:             fmt.Errorf("git unavailable"),
		})
		m = um.(*Model)
		if m.historyView.ShowCausality() {
			t.Fatalf("P18A-8 regression: CausalityLoadedMsg error left showCausality stuck true")
		}
		// Verify SetReferenceTime invalidates causalityCache.
		m.historyView.SetCausality("C-1", &correlation.CausalityResult{Insights: &correlation.CausalInsights{Coverage: "complete"}})
		m.historyView.SetReferenceTime(now.Add(24 * time.Hour))
		if m.historyView.GetCausality("C-1") != nil {
			t.Fatalf("P18A-8 regression: HistoryModel.SetReferenceTime did not invalidate causalityCache")
		}
	})

	t.Run("P18A-9_Phase2ResortKeepsSelectionAndRecipeItemsSynced", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "P-A", Title: "Alpha", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
			{ID: "P-B", Title: "Beta", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.sortMode = SortTriage
		m.triageScores = map[string]float64{"P-A": 0.9, "P-B": 0.1}
		m.applyFilter()
		// Select P-A (currently index 0).
		m.selectVisibleListItemByID("P-A")
		// Now Phase 2 flips scores so P-B ranks first.
		m.triageScores = map[string]float64{"P-A": 0.1, "P-B": 0.95}
		m.refreshListItemsPhase2()
		items := m.list.Items()
		if len(items) != 2 || items[0].(IssueItem).Issue.ID != "P-B" {
			t.Fatalf("P18A-9 regression: refreshListItemsPhase2 did not re-sort items, got %v", items)
		}
		if sel, ok := m.list.SelectedItem().(IssueItem); !ok || sel.Issue.ID != "P-A" {
			t.Fatalf("P18A-9 regression: selection jumped away from P-A after Phase 2 resort, got %+v", m.list.SelectedItem())
		}
		if len(m.recipeListItems) != 2 || m.recipeListItems[0].(IssueItem).Issue.ID != "P-B" {
			t.Fatalf("P18A-9 regression: recipeListItems out of sync with re-sorted list: %v", m.recipeListItems)
		}
	})

	t.Run("P18A-10_ClosedNodeNotFlaggedAsCutVertex", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "CV-A", Title: "Open A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime,
				Dependencies: []*model.Dependency{{IssueID: "CV-A", DependsOnID: "CV-MID", Type: model.DepBlocks}}},
			{ID: "CV-MID", Title: "Closed bridge", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime,
				Dependencies: []*model.Dependency{{IssueID: "CV-MID", DependsOnID: "CV-C", Type: model.DepBlocks}}},
			{ID: "CV-C", Title: "Open C", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: oldTime, UpdatedAt: oldTime},
		}
		an := analysis.NewAnalyzer(issues)
		an.SetNow(now)
		stats := an.Analyze()
		ins := stats.GenerateInsights(10)
		gm := NewGraphModel(issues, &ins, DefaultTheme(lipgloss.NewRenderer(&strings.Builder{})))
		gm.SetAnalyzer(an, func() time.Time { return now })
		panel := ansi.Strip(gm.renderMetricsPanel("CV-MID", 72, gm.theme))
		if strings.Contains(panel, "Cut Vertex") {
			t.Fatalf("P18A-10 regression: closed node CV-MID must not be flagged as Cut Vertex:\n%s", panel)
		}
	})

	t.Run("P18A-12_EmptyCandidateScopeDoesNotLeakLabelExtras", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "L-1", Title: "Root", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"core", "backend"}, CreatedAt: oldTime, UpdatedAt: oldTime},
			{ID: "L-2", Title: "Child", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"frontend"}, CreatedAt: oldTime, UpdatedAt: oldTime,
				Dependencies: []*model.Dependency{{IssueID: "L-2", DependsOnID: "L-1", Type: model.DepBlocks}}},
		}
		m := NewModel(issues, nil, "")
		defer m.Stop()
		m.candidateIDs = map[string]bool{} // Explicit empty scope!
		m.populateLabelHealthDetailExtras("core")
		if len(m.labelHealthDetailCooccurrence) != 0 {
			t.Fatalf("P18A-12 regression: empty candidateIDs leaked cooccurrence: %+v", m.labelHealthDetailCooccurrence)
		}
		if len(m.labelHealthDetailTopUnblocks) != 0 {
			t.Fatalf("P18A-12 regression: empty candidateIDs leaked top unblocks: %+v", m.labelHealthDetailTopUnblocks)
		}
	})
}
