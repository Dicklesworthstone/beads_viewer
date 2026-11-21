package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/Dicklesworthstone/beads_viewer/pkg/updater"
)

func TestMain(m *testing.M) {
	if runEditorTrackerTestProcess() {
		return
	}
	// Editor dispatch tests execute a copy of this test binary under an
	// allowlisted editor name. Capture the actual argv without opening a GUI;
	// no platform shell or GUI program is required. Check the executable name
	// so an inherited capture variable cannot turn the suite into a zero-run pass.
	editorName := filepath.Base(os.Args[0])
	if capture := os.Getenv("BV_TEST_EDITOR_CAPTURE"); capture != "" && (editorName == "gedit" || editorName == "gedit.exe") {
		args, err := json.Marshal(os.Args[1:])
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(capture, args, 0o600); err != nil {
			panic(err)
		}
		os.Exit(0)
	}

	// Prevent any test from accidentally opening a browser
	os.Setenv("BV_NO_BROWSER", "1")
	os.Setenv("BV_TEST_MODE", "1")

	// Keep every test away from the developer's real ~/.config/bv: saved
	// config (tutorial progress, update-check disclosure) is disabled unless a
	// test opts back in with t.Setenv, and the XDG config dir points at a
	// throwaway directory so even opted-in tests never touch the real one.
	os.Setenv("BV_NO_SAVED_CONFIG", "1")
	isolatedConfig, err := os.MkdirTemp("", "bv-ui-test-config-*")
	if err != nil {
		panic("creating isolated XDG_CONFIG_HOME: " + err.Error())
	}
	os.Setenv("XDG_CONFIG_HOME", isolatedConfig)

	code := m.Run()
	os.RemoveAll(isolatedConfig)
	os.Exit(code)
}

func TestViewportAndInsightsDetailSurfaceDueDeferAndExternalRef(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	deferUntil := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	extRef := "JIRA-9001"
	issue := model.Issue{
		ID:          "UI-META-1",
		Title:       "Metadata visibility",
		Status:      model.StatusOpen,
		Priority:    1,
		IssueType:   model.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
		DueDate:     &due,
		DeferUntil:  &deferUntil,
		ExternalRef: &extRef,
	}

	m := NewModel([]model.Issue{issue}, nil, "")
	t.Cleanup(func() { m.Stop() })
	m.isSplitView = true
	m.updateViewportContent()
	for _, want := range []string{"2026-11-20", "2026-10-25", "JIRA-9001"} {
		if !strings.Contains(m.viewportContent, want) {
			t.Fatalf("viewportContent missing %q:\n%s", want, m.viewportContent)
		}
	}

	insModel := NewInsightsModel(analysis.Insights{
		Bottlenecks: []analysis.InsightItem{{ID: issue.ID, Value: 1.0}},
	}, map[string]*model.Issue{issue.ID: &issue}, m.theme)
	detailMD := insModel.buildDetailMarkdown(issue.ID)
	for _, want := range []string{
		"- **Due:** 2026-11-20",
		"- **Deferred Until:** 2026-10-25",
		"- **External Ref:** `JIRA-9001`",
	} {
		if !strings.Contains(detailMD, want) {
			t.Fatalf("insights buildDetailMarkdown missing %q:\n%s", want, detailMD)
		}
	}
}

func TestListVisibleBoundsClamping_GH210(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "ALPHA-1", Title: "Alpha match", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "BETA-2", Title: "Beta other", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "GAMMA-3", Title: "Gamma other", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	m.list.SetSize(80, 20)

	// Narrow visible items via a fuzzy filter to 1 row ("ALPHA-1"), then force
	// the underlying cursor to an out-of-visible-bounds index (2) as happens
	// when unfiltered Items() has 3 items but VisibleItems() has 1.
	m.refilterListSynchronously(list.FilterApplied, "Alpha", "ALPHA-1")
	if got := len(m.list.VisibleItems()); got != 1 {
		t.Fatalf("VisibleItems len = %d, want 1", got)
	}
	m.list.Select(2)

	// applyFilter must clamp selection against VisibleItems(), not filteredItems.
	m.applyFilter()
	if got := m.list.Index(); got != 0 {
		t.Fatalf("after applyFilter with narrow fuzzy filter, list.Index() = %d, want 0", got)
	}

	// Force stale index again and verify mouse wheel up clamps into [0, len(VisibleItems())-1].
	m.list.Select(2)
	_, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if got := m.list.Index(); got != 0 {
		t.Fatalf("after MouseButtonWheelUp from stale index, list.Index() = %d, want 0", got)
	}

	// Verify jumpToChangedIssue clamps from listIndexInBounds without out-of-range indexing.
	m.timeTravelMode = true
	m.timeTravelDiff = &analysis.SnapshotDiff{}
	m.newIssueIDs = map[string]bool{"ALPHA-1": true}
	m.list.Select(2)
	m.jumpToChangedIssue(true)
	if got := m.list.Index(); got != 0 {
		t.Fatalf("after jumpToChangedIssue from stale index, list.Index() = %d, want 0", got)
	}
}

func TestInsightsHeatmapNavigationAndDrilldown(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "HM-1", Title: "Root high score", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "HM-2", Title: "Dependent mid score", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "HM-2", DependsOnID: "HM-1", Type: model.DepBlocks}}},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	m = updated.(*Model)

	stats := analysis.NewAnalyzer(issues).AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	m.insightsPanel = NewInsightsModel(stats.GenerateInsights(len(issues)), m.issueMap, m.theme)
	m.insightsPanel.SetTopPicks([]analysis.TopPick{
		{ID: "HM-1", Title: "Root high score", Score: 0.85},
		{ID: "HM-2", Title: "Dependent mid score", Score: 0.45},
	})
	m.insightsPanel.SetSize(130, 40)
	m.focused = focusInsights
	m.insightsPanel.focusedPanel = PanelPriority

	// Toggle heatmap with H (as advertised in the heatmap header).
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	if !m.insightsPanel.IsHeatmapActive() {
		t.Fatalf("expected heatmap to be active on PanelPriority after pressing H")
	}

	targetRow, targetCol := -1, -1
	for r := range m.insightsPanel.heatmapIssueMap {
		for c := range m.insightsPanel.heatmapIssueMap[r] {
			for _, id := range m.insightsPanel.heatmapIssueMap[r][c] {
				if id == "HM-1" {
					targetRow, targetCol = r, c
				}
			}
		}
	}
	if targetRow < 0 || targetCol < 0 {
		t.Fatalf("HM-1 not found in heatmapIssueMap")
	}
	for m.insightsPanel.heatmapRow < targetRow {
		m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	for m.insightsPanel.heatmapCol < targetCol {
		m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	}
	if m.insightsPanel.focusedPanel != PanelPriority {
		t.Fatalf("pressing l in heatmap mode switched panels to %v instead of moving heatmap column", m.insightsPanel.focusedPanel)
	}
	if got := m.insightsPanel.SelectedIssueID(); got != "HM-1" {
		t.Fatalf("SelectedIssueID() on HM-1 cell = %q, want HM-1", got)
	}

	// Enter drills down into the selected cell instead of leaving Insights.
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focused != focusInsights || !m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected Enter on populated heatmap cell to enter drill-down inside Insights; focused=%v drill=%v", m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}

	// Esc exits drill-down back to heatmap grid without leaving Insights.
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.focused != focusInsights || m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected Esc in heatmap drill-down to exit drill-down and stay in Insights; focused=%v drill=%v", m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}

	// Re-enter drill-down and press Enter again to jump to the selected issue in detail view.
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	m = m.handleInsightsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focused != focusDetail {
		t.Fatalf("expected second Enter in heatmap drill-down to jump to focusDetail, got %v", m.focused)
	}
}

func TestContextTutorialAndFocusRestoration(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "TUT-1", Title: "Tutorial focus test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)

	// Switch to Board view and press ~ to trigger context-sensitive tutorial.
	m.isBoardView = true
	m.focused = focusBoard
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'~'}})
	m = updated.(*Model)
	if !m.showTutorial || m.focused != focusTutorial {
		t.Fatalf("expected ~ in Board view to open tutorial; showTutorial=%v focused=%v", m.showTutorial, m.focused)
	}
	if !m.tutorialModel.contextMode || m.tutorialModel.context != string(ContextBoard) {
		t.Fatalf("expected tutorial contextMode=true and context=%q, got contextMode=%v context=%q", ContextBoard, m.tutorialModel.contextMode, m.tutorialModel.context)
	}

	// Closing tutorial with q restores focusBoard.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(*Model)
	if m.showTutorial || m.focused != focusBoard {
		t.Fatalf("expected closing tutorial opened from Board view to restore focusBoard, got showTutorial=%v focused=%v", m.showTutorial, m.focused)
	}

	// Opening Help (?) from Insights view, then pressing Space to open Tutorial, then closing with ` restores focusInsights.
	m.isBoardView = false
	m.focused = focusInsights
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(*Model)
	if !m.showTutorial || m.focused != focusTutorial || m.tutorialModel.contextMode {
		t.Fatalf("expected Space in Help to open full tutorial; showTutorial=%v focused=%v contextMode=%v", m.showTutorial, m.focused, m.tutorialModel.contextMode)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'`'}})
	m = updated.(*Model)
	if m.showTutorial || m.focused != focusInsights {
		t.Fatalf("expected closing tutorial opened via Help from Insights to restore focusInsights, got showTutorial=%v focused=%v", m.showTutorial, m.focused)
	}

	// Typing ~ in time-travel input must append to timeTravelInput instead of opening the tutorial.
	m.focused = focusList
	m, _ = m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	for _, r := range "HEAD~3" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*Model)
	}
	if m.showTutorial || m.focused != focusTimeTravelInput || m.timeTravelInput.Value() != "HEAD~3" {
		t.Fatalf("expected ~ in time-travel prompt to type into input; showTutorial=%v focused=%v value=%q", m.showTutorial, m.focused, m.timeTravelInput.Value())
	}
}

func TestHistoryCassSessionsWiredFromModel(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "HIST-1", Title: "History session test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)

	sessions := []cass.ScoredResult{
		{
			SearchResult: cass.SearchResult{
				Agent:     "Claude",
				Title:     "Implemented HIST-1 correlation",
				Timestamp: now,
			},
			FinalScore: 0.92,
		},
	}
	req := &cassSessionRequest{
		cancel:         func() {},
		beadID:         "HIST-1",
		focus:          focusList,
		dataGeneration: m.semanticDataGeneration,
		workDir:        m.workDir,
	}
	m.cassRequest = req
	updated, _ = m.Update(cassSessionsLoadedMsg{
		request: req,
		status:  cass.StatusHealthy,
		result: cass.CorrelationResult{
			BeadID:      "HIST-1",
			TopSessions: sessions,
			TotalFound:  1,
		},
	})
	m = updated.(*Model)

	if !m.historyView.HasSessionsForBead("HIST-1") || len(m.historyView.GetSessionsForBead("HIST-1")) != 1 {
		t.Fatalf("expected cassSessionsLoadedMsg to populate historyView session cache for HIST-1")
	}

	// Dataset reset clears the historyView session cache.
	m.beginSemanticDatasetUpdate()
	if m.historyView.HasSessionsForBead("HIST-1") {
		t.Fatalf("expected beginSemanticDatasetUpdate to clear historyView session cache")
	}
}

func TestVelocityComparisonWiredInLabelDashboard(t *testing.T) {
	now := time.Now().UTC()
	closedAt := now.Add(-48 * time.Hour)
	issues := []model.Issue{
		{ID: "VEL-1", Title: "Closed backend task", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"}, CreatedAt: now.Add(-7 * 24 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
		{ID: "VEL-2", Title: "Open frontend task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"frontend"}, CreatedAt: now.Add(-5 * 24 * time.Hour), UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	// Wide terminal (isSplitView == true) must still render LabelDashboardModel when focused on focusLabelDashboard.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = updated.(*Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = updated.(*Model)
	if m.focused != focusLabelDashboard {
		t.Fatalf("expected focusLabelDashboard after '[', got %v", m.focused)
	}
	view := m.View()
	if !strings.Contains(view, "Velocity 7d/30d") {
		t.Fatalf("expected wide-terminal Label Dashboard view to render LabelDashboardModel table, got:\n%s", view)
	}

	// Press 'v' to open VelocityComparisonModel sub-view (bv-125).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = updated.(*Model)
	if !m.showVelocityComparison {
		t.Fatalf("expected 'v' in focusLabelDashboard to open showVelocityComparison")
	}
	if m.velocityComparison.DataCount() != 2 {
		t.Fatalf("expected velocityComparison.DataCount() == 2, got %d", m.velocityComparison.DataCount())
	}
	velView := m.View()
	if !strings.Contains(velView, "Velocity Comparison") {
		t.Fatalf("expected View() to render Velocity Comparison table when showVelocityComparison is true, got:\n%s", velView)
	}

	// Navigate down and back up, then press Enter to filter by selected label.
	firstLabel := m.velocityComparison.SelectedLabel()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*Model)
	secondLabel := m.velocityComparison.SelectedLabel()
	if firstLabel == secondLabel {
		t.Fatalf("expected 'j' to move cursor to second label, still on %q", firstLabel)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if m.showVelocityComparison || m.focused != focusList || m.currentFilter != "label:"+secondLabel {
		t.Fatalf("expected Enter in VelocityComparison to filter list by label:%s and return to focusList; show=%v focused=%v filter=%q",
			secondLabel, m.showVelocityComparison, m.focused, m.currentFilter)
	}
}

func TestInsightsHeatmapAndDrilldownViaModelUpdate(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "UPD-1", Title: "Root high score", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "UPD-2", Title: "Dependent mid score", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "UPD-2", DependsOnID: "UPD-1", Type: model.DepBlocks}}},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	m = updated.(*Model)

	stats := analysis.NewAnalyzer(issues).AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	m.insightsPanel = NewInsightsModel(stats.GenerateInsights(len(issues)), m.issueMap, m.theme)
	m.insightsPanel.SetTopPicks([]analysis.TopPick{
		{ID: "UPD-1", Title: "Root high score", Score: 0.85},
	})
	m.insightsPanel.SetSize(130, 40)
	m.focused = focusInsights
	m.insightsPanel.focusedPanel = PanelPriority

	// Dispatch 'H' through Model.Update (verifying case focusInsights forwards "H").
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = updated.(*Model)
	if !m.insightsPanel.IsHeatmapActive() {
		t.Fatalf("expected 'H' through Model.Update to activate heatmap on PanelPriority")
	}

	targetRow, targetCol := -1, -1
	for r := range m.insightsPanel.heatmapIssueMap {
		for c := range m.insightsPanel.heatmapIssueMap[r] {
			for _, id := range m.insightsPanel.heatmapIssueMap[r][c] {
				if id == "UPD-1" {
					targetRow, targetCol = r, c
				}
			}
		}
	}
	for m.insightsPanel.heatmapRow < targetRow {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = updated.(*Model)
	}
	for m.insightsPanel.heatmapCol < targetCol {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		m = updated.(*Model)
	}

	// Enter drills down into cell via Model.Update.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if m.focused != focusInsights || !m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected Enter via Model.Update to enter heatmap drill-down; focused=%v drill=%v", m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}

	// Pressing Esc via Model.Update must back out of drill-down without leaving focusInsights.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*Model)
	if m.focused != focusInsights || m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected first Esc via Model.Update to exit drill-down and remain in focusInsights; focused=%v drill=%v", m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}

	// Re-enter drill-down and test Backspace via Model.Update.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*Model)
	if m.focused != focusInsights || m.insightsPanel.IsHeatmapDrillDown() {
		t.Fatalf("expected Backspace via Model.Update to exit drill-down and remain in focusInsights; focused=%v drill=%v", m.focused, m.insightsPanel.IsHeatmapDrillDown())
	}
}

func TestLiveReloadRefreshesActionableAttentionAndLabelViews(t *testing.T) {
	now := time.Now().UTC()
	issues1 := []model.Issue{
		{ID: "LR-1", Title: "Initial task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"alpha"}, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues1, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)

	// Open Actionable view ('a'), Label Drilldown, and Velocity Comparison, then send SnapshotReadyMsg with 2 issues.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(*Model)
	if !m.isActionableView || m.actionableView.SelectedIssueID() != "LR-1" {
		t.Fatalf("expected actionable view selected LR-1, got %q", m.actionableView.SelectedIssueID())
	}
	m.showLabelDrilldown = true
	m.labelDrilldownLabel = "alpha"
	m.labelDrilldownIssues = m.filterIssuesByLabel("alpha")
	m.showVelocityComparison = true
	m.velocityComparison.SetData(m.issues)

	issues2 := []model.Issue{
		{ID: "LR-1", Title: "Initial task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"alpha"}, CreatedAt: now, UpdatedAt: now},
		{ID: "LR-2", Title: "New urgent task", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask, Labels: []string{"alpha", "beta"}, CreatedAt: now, UpdatedAt: now},
	}
	snap2 := NewSnapshotBuilder(issues2).Build()
	t.Cleanup(snap2.Analysis.WaitForPhase2)
	updated, _ = m.Update(SnapshotReadyMsg{Snapshot: snap2})
	m = updated.(*Model)

	// Actionable view must include LR-2 and preserve LR-1 selection.
	if m.actionableView.SelectedIssueID() != "LR-1" {
		t.Fatalf("expected refreshActionableView to preserve LR-1 selection, got %q", m.actionableView.SelectedIssueID())
	}
	if !m.actionableView.SelectIssueByID("LR-2") {
		t.Fatalf("expected refreshed actionableView to contain newly added issue LR-2")
	}
	if len(m.labelDrilldownIssues) != 2 {
		t.Fatalf("expected refreshed labelDrilldownIssues for 'alpha' to have 2 issues, got %d", len(m.labelDrilldownIssues))
	}
	if m.velocityComparison.DataCount() != 2 {
		t.Fatalf("expected refreshed velocityComparison to have 2 labels ('alpha', 'beta'), got %d", m.velocityComparison.DataCount())
	}
}

func TestTextInputsConsumeGlobalOverlayTriggerKeys(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "TI-1", Title: "Text input test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"alpha"}, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)

	// Open Label Picker ('l') and type '?', '`', '~', ';' — none may open Help, Tutorial, or Shortcuts Sidebar.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(*Model)
	if !m.showLabelPicker || m.focused != focusLabelPicker {
		t.Fatalf("expected focusLabelPicker after 'l', got %v", m.focused)
	}
	for _, r := range "?`~;" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(*Model)
		if m.showHelp || m.showTutorial || m.showShortcutsSidebar || m.focused != focusLabelPicker {
			t.Fatalf("typing %q in label picker triggered global overlay: help=%v tutorial=%v sidebar=%v focused=%v",
				string(r), m.showHelp, m.showTutorial, m.showShortcutsSidebar, m.focused)
		}
	}
}

func TestCrossViewPageJumpAndCopyKeybindings(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "ACT-1", Title: "First actionable", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "ACT-2", Title: "Second actionable", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "ACT-3", Title: "Third actionable", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*Model)

	// Open Actionable view ('a') and test G (GoToEnd), home (GoToStart), and y (copy issue ID).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(*Model)
	firstID := m.actionableView.SelectedIssueID()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(*Model)
	lastID := m.actionableView.SelectedIssueID()
	if firstID == lastID {
		t.Fatalf("expected 'G' in Actionable view to jump to end, still at %q", firstID)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(*Model)
	if got := m.actionableView.SelectedIssueID(); got != firstID {
		t.Fatalf("expected Home in Actionable view to jump back to %q, got %q", firstID, got)
	}
	reqBefore := m.clipboardRequestID
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*Model)
	if cmd == nil || m.clipboardRequestID != reqBefore+1 {
		t.Fatalf("expected 'y' in Actionable view to schedule clipboard copy command")
	}

	// Verify 'y' in Graph view also schedules clipboard copy command.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(*Model)
	reqBefore = m.clipboardRequestID
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*Model)
	if cmd == nil || m.clipboardRequestID != reqBefore+1 {
		t.Fatalf("expected 'y' in Graph view to schedule clipboard copy command")
	}
}

func TestBuildSemanticIndexCmdUsesProvidedWorkDir(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "SEM-1", Title: "Semantic workDir test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	cmd := BuildSemanticIndexCmd(issues, 1, 1, tmpDir)
	msg, ok := cmd().(SemanticIndexReadyMsg)
	if !ok {
		t.Fatalf("expected SemanticIndexReadyMsg")
	}
	if msg.Error != nil {
		t.Fatalf("BuildSemanticIndexCmd error: %v", msg.Error)
	}
	if !strings.HasPrefix(msg.IndexPath, tmpDir) {
		t.Fatalf("expected IndexPath %q to be rooted under workDir %q", msg.IndexPath, tmpDir)
	}
}

func TestNewModelHonorsSearchConfigFromEnvAndCustomWeights(t *testing.T) {
	t.Setenv(search.EnvSearchMode, "hybrid")
	t.Setenv(search.EnvSearchPreset, "impact-first")
	t.Setenv(search.EnvSearchWeights, `{"text":0.5,"pagerank":0.2,"status":0.1,"impact":0.1,"priority":0.05,"recency":0.05}`)

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "ENV-1", Title: "Hybrid env config", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, "/repo/.beads/issues.jsonl")
	t.Cleanup(func() { m.Stop() })
	if !m.semanticHybridEnabled {
		t.Fatalf("expected semanticHybridEnabled=true from BV_SEARCH_MODE=hybrid")
	}
	if m.semanticHybridPreset != search.PresetImpactFirst {
		t.Fatalf("semanticHybridPreset = %q, want %q", m.semanticHybridPreset, search.PresetImpactFirst)
	}
	if m.workDir != "/repo" {
		t.Fatalf("workDir = %q, want /repo", m.workDir)
	}
	cfg := m.semanticSearch.getHybridConfig()
	if !cfg.Enabled || !cfg.CustomWeights || cfg.Weights.TextRelevance != 0.5 {
		t.Fatalf("unexpected hybrid config: %+v", cfg)
	}

	// Init should schedule hybrid metric computation when hybrid mode is enabled via env.
	_ = m.Init()
	if !m.semanticHybridBuilding {
		t.Fatalf("expected Init() to start semantic hybrid metrics build when enabled via env")
	}

	// Toggling H off and back on should preserve custom weights until Alt+H cycles preset.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = updated.(*Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = updated.(*Model)
	cfg = m.semanticSearch.getHybridConfig()
	if !cfg.CustomWeights || cfg.Weights.TextRelevance != 0.5 {
		t.Fatalf("expected custom weights to survive H toggle, got %+v", cfg)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	m = updated.(*Model)
	cfg = m.semanticSearch.getHybridConfig()
	if cfg.CustomWeights {
		t.Fatalf("expected Alt+H preset cycle to replace custom weights with preset weights")
	}
}

func TestFileChangedAndPhase2PreserveFilterSortRecipeAndSprintDashboard(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	initialJSONL := strings.Join([]string{
		`{"id":"OPEN-2","title":"Open P2","status":"open","priority":2,"issue_type":"task"}`,
		`{"id":"OPEN-1","title":"Open P1","status":"open","priority":1,"issue_type":"task"}`,
		`{"id":"CLOSED-1","title":"Closed item","status":"closed","priority":1,"issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(beadsPath, []byte(initialJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "OPEN-2", Title: "Open P2", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "OPEN-1", Title: "Open P1", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "CLOSED-1", Title: "Closed item", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	m := NewModel(issues, nil, beadsPath)
	defer m.Stop()
	m.backgroundWorker = nil

	// Activate 'o' (open filter) and select OPEN-2; attach an in-memory sprint.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = updated.(*Model)
	m.selectVisibleListItemByID("OPEN-2")
	sprint := model.Sprint{
		ID:        "sprint-1",
		Name:      "Sprint 1",
		BeadIDs:   []string{"OPEN-1", "OPEN-2", "CLOSED-1"},
		StartDate: now.Add(-24 * time.Hour),
		EndDate:   now.Add(24 * time.Hour),
	}
	m.sprints = []model.Sprint{sprint}
	m.selectedSprint = &m.sprints[0]
	m.sprintViewText = m.renderSprintDashboard()

	// Update issues.jsonl so OPEN-1 becomes closed and trigger FileChangedMsg.
	updatedJSONL := strings.Join([]string{
		`{"id":"OPEN-2","title":"Open P2","status":"open","priority":2,"issue_type":"task"}`,
		`{"id":"OPEN-1","title":"Open P1","status":"closed","priority":1,"issue_type":"task"}`,
		`{"id":"CLOSED-1","title":"Closed item","status":"closed","priority":1,"issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(beadsPath, []byte(updatedJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, _ = m.Update(FileChangedMsg{})
	m = updated.(*Model)

	if len(m.list.Items()) != 1 {
		t.Fatalf("expected 'open' filter to stay active after FileChangedMsg (1 open item), got %d items", len(m.list.Items()))
	}
	if sel, ok := m.list.SelectedItem().(IssueItem); !ok || sel.Issue.ID != "OPEN-2" {
		t.Fatalf("expected selection OPEN-2 preserved after FileChangedMsg, got %#v", m.list.SelectedItem())
	}
	if !strings.Contains(m.sprintViewText, "2/3") {
		t.Fatalf("expected sprintViewText to refresh to 2/3 closed after FileChangedMsg, got:\n%s", m.sprintViewText)
	}

	// Verify DataSnapshot.WithPhase2 re-applies recipes that sort by Phase 2 PageRank.
	graphIssues := []model.Issue{
		{ID: "A-leaf", Title: "Leaf", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "Z-root", Title: "Root blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{
			ID: "M-mid", Title: "Mid", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{IssueID: "M-mid", DependsOnID: "Z-root", Type: model.DepBlocks}},
		},
	}
	r := &recipe.Recipe{
		Name: "pr-recipe",
		Sort: recipe.SortConfig{Field: "pagerank", Direction: "desc"},
	}
	snap1 := NewSnapshotBuilder(graphIssues).WithRecipe(r).Build()
	snap1.Analysis.WaitForPhase2()
	analyzer := analysis.NewAnalyzer(graphIssues)
	stats := analyzer.Analyze()
	snap2 := snap1.WithPhase2(&stats, stats.GenerateInsights(len(graphIssues)), graphIssues, analyzer)
	if len(snap2.ListItems) == 0 || snap2.ListItems[0].Issue.ID != "Z-root" {
		t.Fatalf("expected DataSnapshot.WithPhase2 to re-sort recipe by Phase 2 PageRank with Z-root first, got %+v", snap2.ListItems)
	}
}

func TestModel_StartupUpdateDisclosureShownOnce(t *testing.T) {
	cfgRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	t.Setenv("BV_NO_SAVED_CONFIG", "")

	if !updater.StartupDisclosurePending() {
		t.Fatal("expected StartupDisclosurePending to be true on fresh config dir")
	}

	issues := []model.Issue{
		{ID: "DISC-1", Title: "Disclosure test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	updated, _ := m.Update(StartupUpdateCheckMsg{TagName: "", URL: "", Err: nil})
	m = updated.(*Model)

	if !strings.Contains(m.statusMsg, "Checked github.com for updates") {
		t.Fatalf("expected one-time github.com disclosure in statusMsg, got %q", m.statusMsg)
	}
	if m.statusIsError {
		t.Fatal("startup disclosure must not be marked as an error")
	}
	if updater.StartupDisclosurePending() {
		t.Fatal("expected StartupDisclosurePending to become false after first startup check")
	}

	// Second run must not repeat the disclosure.
	m.statusMsg = ""
	updated, _ = m.Update(StartupUpdateCheckMsg{TagName: "", URL: "", Err: nil})
	m = updated.(*Model)
	if m.statusMsg != "" {
		t.Fatalf("expected second StartupUpdateCheckMsg to stay quiet, got %q", m.statusMsg)
	}

	// Under BV_NO_SAVED_CONFIG=1, nothing is shown and no config directory is created.
	noSaveRoot := filepath.Join(t.TempDir(), "unsaved-xdg")
	t.Setenv("XDG_CONFIG_HOME", noSaveRoot)
	t.Setenv("BV_NO_SAVED_CONFIG", "1")
	m2 := NewModel(issues, nil, "")
	t.Cleanup(func() { m2.Stop() })
	updated2, _ := m2.Update(StartupUpdateCheckMsg{TagName: "", URL: "", Err: nil})
	m2 = updated2.(*Model)
	if m2.statusMsg != "" {
		t.Fatalf("expected BV_NO_SAVED_CONFIG=1 to suppress disclosure, got %q", m2.statusMsg)
	}
	if _, err := os.Stat(noSaveRoot); !os.IsNotExist(err) {
		t.Fatalf("expected BV_NO_SAVED_CONFIG=1 not to create config dir, stat err=%v", err)
	}
}

func TestFlowMatrix_RendersTransitiveCascade(t *testing.T) {
	issues := []model.Issue{
		{ID: "bv-root", Title: "Schema migration", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"schema"}},
		{
			ID: "bv-db", Title: "DB layer", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"db"},
			Dependencies: []*model.Dependency{{IssueID: "bv-db", DependsOnID: "bv-root", Type: model.DepBlocks}},
		},
		{
			ID: "bv-api", Title: "API handler", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"api"},
			Dependencies: []*model.Dependency{{IssueID: "bv-api", DependsOnID: "bv-db", Type: model.DepBlocks}},
		},
		{
			ID: "bv-ui", Title: "Frontend view", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, Labels: []string{"ui"},
			Dependencies: []*model.Dependency{{IssueID: "bv-ui", DependsOnID: "bv-api", Type: model.DepBlocks}},
		},
	}
	flow := analysis.ComputeCrossLabelFlow(issues, analysis.DefaultLabelHealthConfig())
	m := NewModel(issues, nil, "")
	t.Cleanup(func() { m.Stop() })
	fm := NewFlowMatrixModel(m.theme)
	fm.SetData(&flow, issues)
	fm.SetSize(120, 35)

	// Select "db" label (which has dependency-blocked bv-db and downstream api -> ui).
	foundDB := false
	for i := 0; i < 10; i++ {
		if fm.SelectedLabel() == "db" {
			foundDB = true
			break
		}
		fm.MoveDown()
	}
	if !foundDB {
		t.Fatalf("expected to select 'db' in FlowMatrixModel, selected=%q", fm.SelectedLabel())
	}

	view := fm.View()
	for _, want := range []string{"TRANSITIVE CASCADE", "Downstream waiting: 2 (2 levels)", "Top unblock: bv-root"} {
		if !strings.Contains(view, want) {
			t.Fatalf("FlowMatrixModel.View() missing %q:\n%s", want, view)
		}
	}
}

func TestRC38_BackgroundAutoPromotionAndSilentWorkerLogging(t *testing.T) {
	// 1. Unset BV_WORKER_LOG_LEVEL must default to LogLevelNone and emit zero raw log.Printf stderr bytes.
	t.Setenv("BV_WORKER_LOG_LEVEL", "")
	if got := parseWorkerLogLevel(""); got != LogLevelNone {
		t.Fatalf("parseWorkerLogLevel(\"\") = %v, want LogLevelNone", got)
	}
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"bg-1","title":"BG test","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	bw, err := NewBackgroundWorker(WorkerConfig{BeadsPath: jsonlPath, ProjectDir: tmpDir})
	if err != nil {
		t.Fatalf("NewBackgroundWorker: %v", err)
	}
	t.Cleanup(bw.Stop)
	bw.logEvent(LogLevelWarn, "test_warning_should_be_silent", map[string]any{"k": "v"})
	if logBuf.Len() != 0 {
		t.Fatalf("expected zero raw log output when BV_WORKER_LOG_LEVEL unset, got %q", logBuf.String())
	}

	// 2. Single-repo SetSourceReload + SetBackgroundAutoPromote(true) promotes on slow reload (>=1s),
	// while BV_BACKGROUND_MODE=0 blocks auto-promotion.
	issues := []model.Issue{{ID: "bg-1", Title: "BG test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	loaderFn := func(ctx context.Context) (SourceReloadResult, error) {
		return SourceReloadResult{Issues: issues, ReloadDuration: 1200 * time.Millisecond}, nil
	}

	t.Setenv("BV_BACKGROUND_MODE", "0")
	mOptOut := NewModel(issues, nil, jsonlPath)
	t.Cleanup(mOptOut.Stop)
	mOptOut.SetSourceReload(loaderFn)
	mOptOut.SetBackgroundAutoPromote(true)
	resOptOut, _ := loaderFn(context.Background())
	mOptOut.sourceReloadRequest = 1
	updatedOptOut, _ := mOptOut.Update(FileChangedMsg{source: &resOptOut, sourceVersion: mOptOut.sourceVersion, request: 1})
	mOptOut = updatedOptOut.(*Model)
	if mOptOut.backgroundWorker != nil {
		t.Fatal("BV_BACKGROUND_MODE=0 must prevent auto-promotion to BackgroundWorker")
	}

	t.Setenv("BV_BACKGROUND_MODE", "")
	mPromote := NewModel(issues, nil, jsonlPath)
	t.Cleanup(mPromote.Stop)
	mPromote.SetSourceReload(loaderFn)
	mPromote.SetBackgroundAutoPromote(true)
	resPromote, _ := loaderFn(context.Background())
	mPromote.sourceReloadRequest = 1
	updatedPromote, _ := mPromote.Update(FileChangedMsg{source: &resPromote, sourceVersion: mPromote.sourceVersion, request: 1})
	mPromote = updatedPromote.(*Model)
	if mPromote.backgroundWorker == nil {
		t.Fatalf("expected single-repo SetSourceReload + SetBackgroundAutoPromote(true) slow reload to promote; statusMsg=%q", mPromote.statusMsg)
	}
	if mPromote.backgroundWorker.sourceReload == nil {
		t.Fatal("promoted BackgroundWorker did not inherit SourceReload callback")
	}
	if !strings.Contains(mPromote.statusMsg, "background mode auto-enabled") {
		t.Errorf("expected statusMsg to mention auto-enabled background mode, got %q", mPromote.statusMsg)
	}
}

func TestRC38_TimeTravelReopenedBadgeJumpAndSummary(t *testing.T) {
	if got := DiffStatusReopened.Badge(); got != "[REOPENED]" {
		t.Fatalf("DiffStatusReopened.Badge() = %q, want [REOPENED]", got)
	}
	issues := []model.Issue{
		{ID: "TT-1", Title: "Unchanged", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "TT-2", Title: "Reopened item", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug},
	}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = updated.(*Model)

	m.timeTravelMode = true
	m.timeTravelSince = "HEAD~3"
	m.timeTravelDensityDelta = 0.15
	m.reopenedIssueIDs = map[string]bool{"TT-2": true}
	m.timeTravelDiff = &analysis.SnapshotDiff{
		ReopenedIssues: []model.Issue{issues[1]},
		MetricDeltas:   analysis.MetricDeltas{CycleCount: -1},
		Summary: analysis.DiffSummary{
			IssuesAdded:    1,
			IssuesClosed:   2,
			IssuesModified: 3,
			IssuesReopened: 1,
			HealthTrend:    "improving",
		},
	}
	m.rebuildListWithDiffInfo()

	if got := m.getDiffStatus("TT-2"); got != DiffStatusReopened {
		t.Fatalf("getDiffStatus(TT-2) = %v, want DiffStatusReopened", got)
	}

	// Jumping with 'n' must land on reopened issue TT-2.
	m.selectListIndex(0)
	m.jumpToChangedIssue(true)
	if sel, ok := m.list.SelectedItem().(IssueItem); !ok || sel.Issue.ID != "TT-2" {
		t.Fatalf("expected jumpToChangedIssue to select reopened issue TT-2, got %v", m.list.SelectedItem())
	}

	// Rendered list and footer (after clearing transient status message) must display
	// [REOPENED], reopened count, and metric/health summary.
	m.statusMsg = ""
	fullView := ansi.Strip(m.View())
	for _, want := range []string{"[REOPENED]", "↺1", "Δdens:+0.15", "Δcyc:-1", "[improving]"} {
		if !strings.Contains(fullView, want) {
			t.Errorf("Time-Travel View() missing %q:\n%s", want, fullView)
		}
	}
}

func TestRC38_HistoryMR10FilterCommutativityAuthorAndGitTotal(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			"B-1": {
				BeadID: "B-1",
				Title:  "Auth refactor",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{SHA: "aaa1111", ShortSHA: "aaa1111", Message: "auth fix high", Author: "Alice", Timestamp: now, Confidence: 0.92},
					{SHA: "bbb2222", ShortSHA: "bbb2222", Message: "auth tweak low", Author: "Alice", Timestamp: now.Add(-time.Hour), Confidence: 0.35},
					{SHA: "ccc3333", ShortSHA: "ccc3333", Message: "auth docs bob", Author: "Bob", Timestamp: now.Add(-2 * time.Hour), Confidence: 0.90},
				},
			},
			"B-2": {
				BeadID: "B-2",
				Title:  "Other task",
				Status: "open",
				Commits: []correlation.CorrelatedCommit{
					{SHA: "ddd4444", ShortSHA: "ddd4444", Message: "misc low", Author: "Alice", Timestamp: now.Add(-3 * time.Hour), Confidence: 0.30},
				},
			},
		},
	}

	theme := DefaultTheme(lipgloss.NewRenderer(nil))

	// MR-10: Confidence filter + Search filter must commute and NEVER resurrect excluded low-confidence commits.
	h1 := NewHistoryModel(report, theme)
	h1.SetSize(120, 35)
	h1.minConfidence = 0.80
	h1.refreshAfterFilterChange()
	h1.searchInput.SetValue("low")
	h1.applySearchFilter()

	h2 := NewHistoryModel(report, theme)
	h2.SetSize(120, 35)
	h2.searchInput.SetValue("low")
	h2.applySearchFilter()
	h2.minConfidence = 0.80
	h2.refreshAfterFilterChange()

	if len(h1.histories) != 0 || len(h2.histories) != 0 {
		t.Fatalf("MR-10 violation: search 'low' with minConfidence=0.80 resurrected filtered commits: order1=%d order2=%d", len(h1.histories), len(h2.histories))
	}

	// Bead-mode Author filter must filter per-bead Commits strictly (Alice never sees Bob's commit).
	hAuthor := NewHistoryModel(report, theme)
	hAuthor.SetSize(120, 35)
	hAuthor.SetAuthorFilter("Alice")
	for _, bh := range hAuthor.histories {
		for _, c := range bh.Commits {
			if c.Author != "Alice" {
				t.Fatalf("Bead-mode author filter leaked commit by %q into bead %s", c.Author, bh.BeadID)
			}
		}
	}

	// Git-mode filter bar must show true unfiltered commit count ("Showing 2 of 4 commits"), not 2 of 2.
	hGit := NewHistoryModel(report, theme)
	hGit.SetSize(120, 35)
	hGit.ToggleViewMode() // Git mode
	hGit.minConfidence = 0.80
	hGit.refreshAfterFilterChange()
	gitView := ansi.Strip(hGit.View())
	if !strings.Contains(gitView, "Showing 2/4 commits") {
		t.Errorf("Git mode should report true unfiltered total commits (Showing 2/4 commits), got:\n%s", gitView)
	}
}

func TestRC38_TutorialScrollUnwedgeMouseAndFactualContent(t *testing.T) {
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	tm := NewTutorialModel(theme)
	tm.SetSize(100, 20) // Small enough to require scrolling

	// Jump to bottom with 'G' -> scrollOffset must equal real maxScrollOffset (never 9999),
	// and a SINGLE 'k' keypress must immediately scroll upward AND visibly change View().
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	atBottom := tm.scrollOffset
	if atBottom <= 0 || atBottom >= 9999 {
		t.Fatalf("expected realistic maxScrollOffset after 'G', got %d", atBottom)
	}
	viewAtBottom := tm.View()
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tm.scrollOffset != atBottom-1 {
		t.Fatalf("expected single 'k' after 'G' to scroll up immediately from %d to %d, got %d", atBottom, atBottom-1, tm.scrollOffset)
	}
	if tm.View() == viewAtBottom {
		t.Fatalf("expected first 'k' after 'G' to visibly change rendered View() at 100x20")
	}

	// Mouse wheel up from oversized offset must clamp first (not stay wedged above maxScrollOffset).
	tm.scrollOffset = 9999
	tm, _ = tm.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if tm.scrollOffset > atBottom {
		t.Fatalf("expected MouseButtonWheelUp to clamp oversized scrollOffset <= %d, got %d", atBottom, tm.scrollOffset)
	}

	// A page that fits cleanly within the terminal must NOT phantom-scroll on 'j' or 'G'.
	tmFit := NewTutorialModel(theme)
	tmFit.SetSize(120, 60)
	tmFit.scrollOffset = 0
	tmFit, _ = tmFit.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if tmFit.scrollOffset != 0 {
		t.Fatalf("expected fitting tutorial page at 120x60 to have maxScrollOffset=0, got %d", tmFit.scrollOffset)
	}

	// Mouse wheel down/up must scroll TutorialModel.
	tm.scrollOffset = 0
	tm, _ = tm.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if tm.scrollOffset <= 0 {
		t.Fatalf("expected MouseButtonWheelDown to scroll tutorial down, got scrollOffset=%d", tm.scrollOffset)
	}
	afterDown := tm.scrollOffset
	tm, _ = tm.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if tm.scrollOffset >= afterDown {
		t.Fatalf("expected MouseButtonWheelUp to scroll tutorial up from %d, got %d", afterDown, tm.scrollOffset)
	}

	// Verify all 5 factual tutorial_content.go fixes by rendering every tutorial page at full height.
	tm.SetSize(120, 240)
	var allContent strings.Builder
	for i := range tm.pages {
		tm.currentPage = i
		tm.scrollOffset = 0
		allContent.WriteString(ansi.Strip(tm.View()))
		allContent.WriteString("\n")
	}
	text := allContent.String()
	for _, forbidden := range []string{"Priority x Type", "14+ days", "s/f"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("tutorial_content.go still contains debunked text %q", forbidden)
		}
	}
	for _, required := range []string{"Depth × Triage Score", "30+ days", "outside /"} {
		if !strings.Contains(text, required) {
			t.Errorf("tutorial_content.go missing corrected documentation %q", required)
		}
	}
}

func TestRC38_ManualUpdateKeyDisclosureGuardAndModalFocus(t *testing.T) {
	// 1. UpdateModal with no new version and no rollback must focus Cancel (1), not Update (0).
	theme := DefaultTheme(lipgloss.NewRenderer(nil))
	um := NewUpdateModal("", "", theme)
	um.SetCanRollback(false)
	if um.confirmFocus != 1 {
		t.Fatalf("NewUpdateModal with empty tag and no rollback focused button %d, want 1 (Cancel)", um.confirmFocus)
	}

	// 2. StartupUpdateCheckMsg must NOT record disclosure when suppressed by an active error message.
	cfgRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	issues := []model.Issue{{ID: "UP-1", Title: "Update test", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	m.statusMsg = "existing fatal error"
	m.statusIsError = true
	updated, _ := m.Update(StartupUpdateCheckMsg{})
	m = updated.(*Model)
	if !updater.StartupDisclosurePending() {
		t.Fatal("StartupUpdateCheckMsg recorded disclosure even though an error status suppressed displaying it")
	}
	// Once clear, disclosure displays and records.
	m.statusMsg = ""
	m.statusIsError = false
	updated, _ = m.Update(StartupUpdateCheckMsg{})
	m = updated.(*Model)
	if m.statusMsg != updater.StartupCheckDisclosure {
		t.Fatalf("expected statusMsg == updater.StartupCheckDisclosure (%q), got %q", updater.StartupCheckDisclosure, m.statusMsg)
	}
	if updater.StartupDisclosurePending() {
		t.Fatal("expected StartupDisclosurePending to become false after displaying disclosure")
	}

	// 3. Pressing 'U' when !updateAvailable triggers async manualUpdateCheckFunc instead of claiming latest.
	origCheck := manualUpdateCheckFunc
	t.Cleanup(func() { manualUpdateCheckFunc = origCheck })
	called := false
	manualUpdateCheckFunc = func() (string, string, error) {
		called = true
		return "v99.0.0", "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v99.0.0", nil
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("pressing 'U' when !updateAvailable must return async check command")
	}
	if m.statusMsg != "Checking for updates..." {
		t.Fatalf("expected checking status message on 'U', got %q", m.statusMsg)
	}
	msg := cmd()
	if !called {
		t.Fatal("manual update check command did not invoke check function")
	}
	updated, _ = m.Update(msg)
	m = updated.(*Model)
	if !m.showUpdateModal || m.updateTag != "v99.0.0" {
		t.Fatalf("expected manualUpdateCheckMsg with newer version to open UpdateModal for v99.0.0, show=%v tag=%q", m.showUpdateModal, m.updateTag)
	}
}

func TestRC38_DisplayWidthUTF8ThemeModalsBoardCacheAndUnifiedHelp(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-24 * time.Hour)
	issues := []model.Issue{
		{ID: "DW-1", Title: "Core blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"}, CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now},
		{ID: "DW-2", Title: "Dep blocked task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, Labels: []string{"frontend"}, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "DW-2", DependsOnID: "DW-1", Type: model.DepBlocks}}},
		{ID: "DW-3", Title: "Done work", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, Labels: []string{"backend"}, CreatedAt: now.Add(-96 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
	}

	m := NewModel(issues, nil, "")
	t.Cleanup(m.Stop)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
	m = updated.(*Model)

	// 1. All 13 RenderDebugView views at (60x14, 80x24, 100x30, 140x40) with TrueColor
	// must emit valid UTF-8, fit within W display cells on every line, and preserve '─ stable'.
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prevProfile) })
	allDebugViews := []string{"list", "triage", "insights", "board", "history", "graph", "tree", "actionable", "sprint", "flow", "attention", "labels", "velocity"}
	for _, dim := range [][2]int{{60, 14}, {80, 24}, {100, 30}, {140, 40}} {
		w, h := dim[0], dim[1]
		for _, viewName := range allDebugViews {
			out := m.RenderDebugView(viewName, w, h)
			if strings.HasPrefix(out, "Unknown view:") {
				t.Fatalf("RenderDebugView(%q) unsupported: %q", viewName, out)
			}
			if !utf8.ValidString(out) {
				t.Fatalf("RenderDebugView(%q, %d, %d) emitted invalid UTF-8", viewName, w, h)
			}
			for lineIdx, line := range strings.Split(out, "\n") {
				if lineW := ansi.StringWidth(line); lineW > w {
					t.Fatalf("RenderDebugView(%q, %d, %d) line %d overflowed width: %d > %d (%q)", viewName, w, h, lineIdx+1, lineW, w, ansi.Strip(line))
				}
			}
			if viewName == "velocity" && strings.Contains(out, "─ stabl") && !strings.Contains(out, "─ stable") {
				t.Fatalf("VelocityComparison truncated UTF-8 '─ stable' into '─ stabl' at %dx%d:\n%s", w, h, ansi.Strip(out))
			}
		}
	}

	// 2. Sprint view counts dependency-blocked DW-2 in ⛔ and preserves footer on 60x14
	m.width = 60
	m.height = 14
	m.sprints = []model.Sprint{{ID: "sp-1", Name: "Sprint Alpha", StartDate: now.Add(-7 * 24 * time.Hour), EndDate: now.Add(7 * 24 * time.Hour), BeadIDs: []string{"DW-1", "DW-2", "DW-3"}}}
	m.selectedSprint = &m.sprints[0]
	sprintOut := ansi.Strip(m.renderSprintDashboard())
	if !strings.Contains(sprintOut, "⛔1") {
		t.Errorf("Sprint view should count dependency-blocked DW-2 as ⛔1, got:\n%s", sprintOut)
	}
	if !strings.Contains(sprintOut, "P/esc: close") {
		t.Errorf("Sprint view on 60x14 dropped footer hint:\n%s", sprintOut)
	}

	// 3. Label modals & Cass modal at 60x14 fit within height 14 and preserve Esc footer
	m.labelHealthDetail = &analysis.LabelHealth{Label: "backend", Health: 80, HealthLevel: analysis.HealthLevelHealthy, IssueCount: 2, OpenCount: 1, ClosedCount: 1}
	lhView := ansi.Strip(m.renderLabelHealthDetail(*m.labelHealthDetail))
	if lipgloss.Height(lhView) > 14 || !strings.Contains(lhView, "Press Esc to close") {
		t.Errorf("renderLabelHealthDetail at 60x14 overflowed (h=%d) or dropped footer:\n%s", lipgloss.Height(lhView), lhView)
	}
	m.labelDrilldownLabel = "backend"
	m.labelDrilldownIssues = issues
	ldView := ansi.Strip(m.renderLabelDrilldown())
	if lipgloss.Height(ldView) > 14 || !strings.Contains(ldView, "Press Esc to close") {
		t.Errorf("renderLabelDrilldown at 60x14 overflowed (h=%d) or dropped footer:\n%s", lipgloss.Height(ldView), ldView)
	}
	m.labelGraphAnalysisResult = &LabelGraphAnalysisResult{
		Label:    "backend",
		Subgraph: analysis.LabelSubgraph{Label: "backend", IssueCount: 2, CoreCount: 2, IssueMap: map[string]model.Issue{"DW-1": issues[0], "DW-3": issues[2]}},
	}
	lgView := ansi.Strip(m.renderLabelGraphAnalysis())
	if lipgloss.Height(lgView) > 14 || !strings.Contains(lgView, "Press Esc/q/g to close") {
		t.Errorf("renderLabelGraphAnalysis at 60x14 overflowed (h=%d) or dropped footer:\n%s", lipgloss.Height(lgView), lgView)
	}

	// 4. BoardModel value-receiver View persists rendered detail markdown in shared detailCache
	bm := NewBoardModel(issues, m.theme)
	bm.ShowDetail()
	_ = bm.View(160, 30)
	firstRenders := bm.detailCache.renders
	_ = bm.View(160, 30)
	if bm.detailCache.renders != firstRenders {
		t.Fatalf("BoardModel.View re-rendered detail markdown across unchanged frames: renders %d -> %d", firstRenders, bm.detailCache.renders)
	}

	// 5. ? Help Overlay driven by GetKeyBindingDocs covers ALL views + new registered bindings
	m.width = 160
	m.height = 80
	helpLines, _ := m.buildHelpBodyLines()
	helpText := ansi.Strip(strings.Join(helpLines, "\n"))
	for _, wantSection := range []string{"Board View", "Actionable", "Flow Matrix", "Label Dashboard", "Attention View", "Velocity View", "Sprint View", "Split View", "History", "Status"} {
		if !strings.Contains(helpText, wantSection) {
			t.Errorf("Help overlay missing section %q from GetKeyBindingDocs()", wantSection)
		}
	}
	for _, wantBinding := range []struct {
		f   focus
		key string
	}{
		{focusHistory, "g"},
		{focusBoard, "0"},
		{focusBoard, "$"},
		{focusList, "tab"},
		{focusList, "<"},
		{focusList, ">"},
	} {
		if !m.keyRegistry.HasBinding(wantBinding.f, wantBinding.key) {
			t.Errorf("keyRegistry missing binding (%v, %q)", wantBinding.f, wantBinding.key)
		}
	}
}

func TestWave22_TUIRegressions(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	// 1. BackgroundWorker with sourceReload strips tombstones, counts warnings,
	//    sets DatasetTier/LoadedOpenOnly=false, and heals m.sourceDegraded via SnapshotReadyMsg.
	t.Run("BackgroundWorkerSourceReloadAndDegradedHeal", func(t *testing.T) {
		degraded := true
		bw, err := NewBackgroundWorker(WorkerConfig{
			BeadsPath:      filepath.Join(t.TempDir(), "nonexistent.jsonl"),
			SourceDegraded: true,
			DebounceDelay:  50 * time.Millisecond,
			SourceReload: func(context.Context) (SourceReloadResult, error) {
				warns := []string{}
				if degraded {
					warns = []string{"fallback warning"}
				}
				return SourceReloadResult{
					Issues: []model.Issue{
						{ID: "LIVE-1", Title: "Visible", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
						{ID: "TOMB-1", Title: "Deleted", Status: model.StatusTombstone, Priority: 2, IssueType: model.TypeTask},
					},
					Warnings:       warns,
					SourceDegraded: degraded,
				}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer bw.Stop()

		snap1 := bw.buildSnapshot(true)
		if snap1 == nil {
			t.Fatal("buildSnapshot(degraded=true) returned nil")
		}
		if len(snap1.Issues) != 1 || snap1.Issues[0].ID != "LIVE-1" {
			t.Fatalf("expected tombstone stripped from sourceReload snapshot, got %+v", snap1.Issues)
		}
		if snap1.LoadWarningCount != 1 || snap1.AuthorityComplete || !snap1.SourceDegraded || !snap1.SourceDegradedKnown {
			t.Fatalf("unexpected degraded snapshot metadata: warn=%d complete=%v degraded=%v known=%v",
				snap1.LoadWarningCount, snap1.AuthorityComplete, snap1.SourceDegraded, snap1.SourceDegradedKnown)
		}
		if snap1.LoadedOpenOnly || snap1.DatasetTier != datasetTierSmall || snap1.SourceIssueCountHint != 2 {
			t.Fatalf("unexpected sourceReload tier metadata: openOnly=%v tier=%v hint=%d",
				snap1.LoadedOpenOnly, snap1.DatasetTier, snap1.SourceIssueCountHint)
		}

		// Now heal the source and deliver healed snapshot to a previously degraded Model.
		degraded = false
		snap2 := bw.buildSnapshot(true)
		if snap2 == nil || snap2.SourceDegraded || !snap2.AuthorityComplete || snap2.LoadWarningCount != 0 {
			t.Fatalf("expected healed snapshot to clear SourceDegraded/warnings and mark AuthorityComplete, got %+v", snap2)
		}

		m := NewModel(snap1.Issues, nil, "", ReadinessScope{
			AuthorityIncomplete: true,
			SourceDegraded:      true,
		})
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})
		if !m.sourceDegraded || !m.authorityIncomplete {
			t.Fatalf("expected initial degraded state on Model")
		}
		m.Update(SnapshotReadyMsg{Snapshot: snap2, SnapshotVer: 2})
		if m.sourceDegraded || m.authorityIncomplete {
			t.Fatalf("expected healed SnapshotReadyMsg to clear sourceDegraded and authorityIncomplete, got degraded=%v incomplete=%v",
				m.sourceDegraded, m.authorityIncomplete)
		}
	})

	// 2. SetSourceReload stops any existing BackgroundWorker and routes startSourceReload to sourceReloadCmd.
	t.Run("SetSourceReloadStopsExistingWorker", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"SR-1","title":"Initial","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m := NewModel([]model.Issue{{ID: "SR-1", Title: "Initial", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, jsonlPath)
		t.Cleanup(m.Stop)
		bw, err := NewBackgroundWorker(WorkerConfig{BeadsPath: jsonlPath, DebounceDelay: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		m.backgroundWorker = bw
		m.snapshotInitPending = true
		m.restoreBackgroundAfterEdit = true
		called := false
		m.SetSourceReload(func(context.Context) (SourceReloadResult, error) {
			called = true
			return SourceReloadResult{
				Issues: []model.Issue{{ID: "SR-2", Title: "Reloaded", Status: model.StatusOpen, IssueType: model.TypeTask}},
			}, nil
		})
		if m.backgroundWorker != nil {
			t.Fatal("expected SetSourceReload to stop and clear existing backgroundWorker")
		}
		if m.snapshotInitPending || m.restoreBackgroundAfterEdit {
			t.Fatalf("expected SetSourceReload to clear snapshotInitPending & restoreBackgroundAfterEdit, got pending=%v restore=%v",
				m.snapshotInitPending, m.restoreBackgroundAfterEdit)
		}
		cmd := m.TriggerSourceReload()
		if cmd == nil {
			t.Fatal("expected TriggerSourceReload to return sourceReloadCmd")
		}
		_ = cmd()
		if !called {
			t.Fatal("expected sourceReload callback to execute via TriggerSourceReload")
		}
	})

	// 3. SprintView: 60x14 bottom border & footer intact, dep-blocked row shows ⛔, sprintScroll resets on reopen/wheel.
	t.Run("SprintViewClampDepBlockedIconAndScrollReset", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "SP-BLK", Title: "Upstream blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
			{
				ID:        "SP-DEP",
				Title:     "Open issue blocked by upstream dependency with a very long title to test line truncation",
				Status:    model.StatusOpen,
				Priority:  2,
				IssueType: model.TypeTask,
				Dependencies: []*model.Dependency{
					{IssueID: "SP-DEP", DependsOnID: "SP-BLK", Type: model.DepBlocks},
				},
			},
		}
		sprints := []model.Sprint{
			{ID: "sp-1", Name: "Sprint 1", StartDate: now.Add(-48 * time.Hour), EndDate: now.Add(48 * time.Hour), BeadIDs: []string{"SP-BLK", "SP-DEP"}},
			{ID: "sp-2", Name: "Sprint 2", StartDate: now.Add(72 * time.Hour), EndDate: now.Add(144 * time.Hour), BeadIDs: []string{"SP-BLK"}},
		}
		m := NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
		m.SetSprints(sprints)
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m.openSprintView()
		plainTall := ansi.Strip(m.renderSprintDashboard())
		if !strings.Contains(plainTall, "⛔ SP-DEP") {
			t.Fatalf("expected dep-blocked open sprint bead to show ⛔ icon, got:\n%s", plainTall)
		}

		// Reopen resets sprintScroll even after scrolling down.
		m.sprintScroll = 8
		m.openSprintView()
		if m.sprintScroll != 0 {
			t.Fatalf("expected openSprintView to reset sprintScroll to 0, got %d", m.sprintScroll)
		}

		// Mouse wheel switching sprints resets sprintScroll.
		m.sprintScroll = 4
		m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		if m.sprintScroll != 0 {
			t.Fatalf("expected wheel sprint switch to reset sprintScroll to 0, got %d", m.sprintScroll)
		}

		// At 60x14, bottom rounded border AND footer hint must both survive intact.
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
		sprintNarrow := ansi.Strip(m.renderSprintDashboard())
		if !strings.Contains(sprintNarrow, "╰") || !strings.Contains(sprintNarrow, "╯") {
			t.Fatalf("Sprint view at 60x14 clipped bottom border:\n%s", sprintNarrow)
		}
		if !strings.Contains(sprintNarrow, "P/esc:") {
			t.Fatalf("Sprint view at 60x14 dropped footer hint:\n%s", sprintNarrow)
		}
		if lipgloss.Height(sprintNarrow) > 13 {
			t.Fatalf("Sprint view at 60x14 exceeded body height 13: got %d", lipgloss.Height(sprintNarrow))
		}
	})

	// 4. canScrollShortcutsSidebar respects sidebarVisible() & focusSprint; empty LabelDashboard 'h' stays in LabelDashboard.
	t.Run("SidebarScrollGuardAndEmptyLabelDashboardH", func(t *testing.T) {
		m := NewModel([]model.Issue{{ID: "NL-1", Title: "Unlabeled", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
		t.Cleanup(m.Stop)
		m.showShortcutsSidebar = true
		m.Update(tea.WindowSizeMsg{Width: 48, Height: 24}) // too narrow for sidebarVisible() (< 56 cols)
		if m.sidebarVisible() {
			t.Fatal("expected sidebarVisible()=false at width 48")
		}
		if m.canScrollShortcutsSidebar("ctrl+j") {
			t.Fatal("canScrollShortcutsSidebar must be false when sidebar is hidden due to narrow terminal")
		}
		m.Update(tea.WindowSizeMsg{Width: 180, Height: 30})
		m.focused = focusSprint
		if m.canScrollShortcutsSidebar("pgdown") || m.canScrollShortcutsSidebar("pgup") {
			t.Fatal("canScrollShortcutsSidebar must yield pgup/pgdown when focusSprint is active")
		}

		// Empty LabelDashboard: pressing 'h' must NOT fall through to History view.
		m.focused = focusLabelDashboard
		m.isHistoryView = false
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		m = updated.(*Model)
		if m.isHistoryView || m.focused == focusHistory {
			t.Fatalf("pressing 'h' on empty LabelDashboard unexpectedly opened History view (focused=%v)", m.focused)
		}
	})

	// 5. Width/height clamping regressions: AttentionModel width 40, FlowMatrix narrow detail,
	//    VelocityComparison narrow footer, renderHelpOverlay 60x14, UpdateModal horizontal buttons + 60x14.
	t.Run("NarrowTerminalWidthAndHeightClamping", func(t *testing.T) {
		theme := DefaultTheme(lipgloss.NewRenderer(nil))

		// AttentionModel at width 40: selected row must stay <= 40 cells.
		am := NewAttentionModel(theme)
		am.SetSize(40, 12)
		am.SetData(analysis.LabelAttentionResult{
			Labels: []analysis.LabelAttentionScore{
				{Label: "very-long-label-name-here", AttentionScore: 42.5, PageRankSum: 0.9, StalenessFactor: 1.5, BlockImpact: 3, VelocityFactor: 2},
			},
		})
		for _, ln := range strings.Split(am.View(), "\n") {
			if w := lipgloss.Width(ln); w > 40 {
				t.Errorf("AttentionModel line exceeded width 40 (got %d): %q", w, ansi.Strip(ln))
			}
		}

		// FlowMatrix narrow detail pane: lines in right column must stay within pane width.
		fm := NewFlowMatrixModel(theme)
		fm.SetSize(60, 14)
		flowIssues := []model.Issue{
			{ID: "FM-1", Title: "A", Status: model.StatusOpen, Labels: []string{"very-long-source-label-alpha"}},
			{
				ID: "FM-2", Title: "B", Status: model.StatusOpen, Labels: []string{"very-long-target-label-beta"},
				Dependencies: []*model.Dependency{{IssueID: "FM-2", DependsOnID: "FM-1", Type: model.DepBlocks}},
			},
		}
		flow := analysis.ComputeCrossLabelFlow(flowIssues, analysis.DefaultLabelHealthConfig())
		fm.SetData(&flow, flowIssues)
		for _, ln := range strings.Split(fm.View(), "\n") {
			if w := lipgloss.Width(ln); w > 60 {
				t.Errorf("FlowMatrix line exceeded width 60 (got %d): %q", w, ansi.Strip(ln))
			}
		}

		// VelocityComparison narrow footer stays <= 36 cols.
		vm := NewVelocityComparisonModel(theme)
		vm.SetSize(36, 12)
		vm.SetDataAt(flowIssues, now)
		for _, ln := range strings.Split(vm.View(), "\n") {
			if w := lipgloss.Width(ln); w > 36 {
				t.Errorf("VelocityComparison line exceeded width 36 (got %d): %q", w, ansi.Strip(ln))
			}
		}

		// Help overlay at 60x14 must fit cleanly within 60x13 without clipping borders.
		m := NewModel(flowIssues, nil, "")
		t.Cleanup(m.Stop)
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
		helpOut := m.renderHelpOverlay()
		if h := lipgloss.Height(helpOut); h > 13 {
			t.Errorf("renderHelpOverlay at 60x14 exceeded height 13: got %d", h)
		}
		for _, ln := range strings.Split(helpOut, "\n") {
			if w := lipgloss.Width(ln); w > 60 {
				t.Errorf("renderHelpOverlay at 60x14 exceeded width 60 (got %d): %q", w, ansi.Strip(ln))
			}
		}

		// UpdateModal buttons (including 3-button Update+Cancel+Rollback) sit side-by-side
		// without truncated right border or bisected button box at 60x12.
		um := NewUpdateModal("v0.1.0", "v0.2.0", theme)
		um.canRollback = true
		um.SetSize(60, 12)
		centered := um.CenterModal(60, 12)
		plainUM := ansi.Strip(centered)
		if h := lipgloss.Height(centered); h > 12 {
			t.Errorf("UpdateModal.CenterModal(60, 12) exceeded height 12: got %d\n%s", h, plainUM)
		}
		sameRow := false
		for _, ln := range strings.Split(plainUM, "\n") {
			if strings.Contains(ln, "Update") && strings.Contains(ln, "Cancel") && strings.Contains(ln, "Rollback") {
				if strings.Contains(ln, "…") {
					t.Errorf("3-button row truncated at 60 cols: %q", ln)
				}
				sameRow = true
				break
			}
		}
		if !sameRow {
			t.Errorf("expected UpdateModal Update, Cancel, and Rollback buttons on the same horizontal row:\n%s", plainUM)
		}
		if strings.Count(plainUM, "╰") < 4 {
			t.Errorf("expected intact outer modal + 3 button bottom borders at 60x12:\n%s", plainUM)
		}
	})
}
