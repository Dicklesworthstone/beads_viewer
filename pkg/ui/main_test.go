package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
)

func TestMain(m *testing.M) {
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
		"| **Due** | 2026-11-20 |",
		"| **Deferred Until** | 2026-10-25 |",
		"| **External Ref** | `JIRA-9001` |",
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
	analyzer := analysis.NewAnalyzer(graphIssues)
	stats := analyzer.Analyze()
	snap2 := snap1.WithPhase2(&stats, stats.GenerateInsights(len(graphIssues)), graphIssues, analyzer)
	if len(snap2.ListItems) == 0 || snap2.ListItems[0].Issue.ID != "Z-root" {
		t.Fatalf("expected DataSnapshot.WithPhase2 to re-sort recipe by Phase 2 PageRank with Z-root first, got %+v", snap2.ListItems)
	}
}
