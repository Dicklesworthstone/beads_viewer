package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	_ "modernc.org/sqlite"
)

// exercise Phase2Ready and FileChanged branches of Update for coverage.
func TestModelUpdatePhase2AndFileChanged(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Alpha", Status: model.StatusOpen}}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 40

	// Phase2ReadyMsg should rebuild insights/graph without error
	ins := m.analysis.GenerateInsights(len(issues))
	updated, _ := m.Update(Phase2ReadyMsg{Stats: m.analysis, Insights: ins})
	m2 := updated.(*Model)
	if m2.insightsPanel.insights.Stats == nil {
		t.Fatalf("expected insights to be regenerated")
	}
	if len(m2.priorityHints) == 0 {
		t.Fatalf("expected priority hints populated after Phase2Ready")
	}

	// FileChangedMsg with empty beadsPath should simply re-arm watcher (no panic)
	if updated2, cmd := m2.Update(FileChangedMsg{}); updated2.(*Model).statusMsg != m2.statusMsg {
		_ = cmd // command may be nil; just ensure no panic and type matches
	}
}

type badItem struct{}

func (badItem) Title() string       { return "bad" }
func (badItem) Description() string { return "bad" }
func (badItem) FilterValue() string { return "bad" }

func TestCopyIssueToClipboardInvalidItem(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.list.SetItems([]list.Item{badItem{}})
	m.list.Select(0)
	m.copyIssueToClipboard()
	if !m.statusIsError || m.statusMsg == "" {
		t.Fatalf("expected error copying invalid item, got %q", m.statusMsg)
	}
}

func TestEnterTimeTravelModeGracefulFailure(t *testing.T) {
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	_ = os.Chdir(tmp)

	m := NewModel(nil, nil, "")
	m.enterTimeTravelMode("HEAD")
	if !m.statusIsError {
		t.Fatalf("expected error when not in git repo")
	}
}

func TestInsightsCurrentPanelItemCount(t *testing.T) {
	ins := analysis.Insights{
		Bottlenecks:  []analysis.InsightItem{{ID: "B"}},
		Keystones:    []analysis.InsightItem{{ID: "K"}},
		Influencers:  []analysis.InsightItem{{ID: "I"}},
		Hubs:         []analysis.InsightItem{{ID: "H"}},
		Authorities:  []analysis.InsightItem{{ID: "A"}},
		Cores:        []analysis.InsightItem{{ID: "C"}},
		Articulation: []string{"ART"},
		Slack:        []analysis.InsightItem{{ID: "S"}},
		Cycles:       [][]string{{"X", "Y"}},
		Stats:        analysis.NewGraphStatsForTest(nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, nil),
	}
	m := NewInsightsModel(ins, map[string]*model.Issue{}, DefaultTheme(nil))
	m.SetTopPicks([]analysis.TopPick{{ID: "P1", Score: 1.0}})
	counts := []int{m.currentPanelItemCount()}
	for i := 0; i < int(PanelCount)-1; i++ {
		m.NextPanel()
		counts = append(counts, m.currentPanelItemCount())
	}
	for idx, c := range counts {
		if c == 0 {
			t.Fatalf("panel %d reported zero items unexpectedly", idx)
		}
	}
}

func TestUpdateFileChangedReloadsSelection(t *testing.T) {
	t.Setenv("BV_BACKGROUND_MODE", "0")
	data := `{"id":"ONE","title":"One","status":"open","issue_type":"task"}`
	tmp := t.TempDir()
	beads := filepath.Join(tmp, "beads.jsonl")
	if err := os.WriteFile(beads, []byte(data), 0644); err != nil {
		t.Fatalf("write beads: %v", err)
	}
	m := NewModel(nil, nil, beads)
	m.list.SetItems([]list.Item{IssueItem{Issue: model.Issue{ID: "ONE", Title: "One", Status: model.StatusOpen}}})
	m.list.Select(0)

	updated, cmd := m.Update(FileChangedMsg{})
	_ = cmd
	m2 := updated.(*Model)
	if m2.statusIsError {
		t.Fatalf("expected successful reload, got error %q", m2.statusMsg)
	}
	if m2.historyLoading || m2.historyLoadRequestGeneration != 0 || m2.historyLoadDataGeneration != 0 {
		t.Fatalf("sync reload eagerly started unused history work: loading=%v data=%d request=%d",
			m2.historyLoading, m2.historyLoadDataGeneration, m2.historyLoadRequestGeneration)
	}
}

func TestInitDoesNotEagerlyLoadHistory(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	created := 0
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		created++
		return func() tea.Msg { return nil }
	}

	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init omitted its ordinary startup commands")
	}
	if created != 0 || m.historyLoading || m.historyLoadCancel != nil {
		t.Fatalf("Init eagerly started history: factories=%d loading=%v cancel=%v", created, m.historyLoading, m.historyLoadCancel != nil)
	}
}

func TestSnapshotSwapDoesNotEagerlyLoadHistoryWhenHidden(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "OLD", Status: model.StatusOpen}}, nil, "")
	m.backgroundWorker = nil
	created := 0
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		created++
		return func() tea.Msg { return nil }
	}

	snapshot := NewSnapshotBuilder([]model.Issue{{ID: "NEW", Status: model.StatusOpen}}).Build()
	snapshot.Analysis = nil
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 1})
	m = updated.(*Model)

	if created != 0 || m.historyLoading || m.historyLoadCancel != nil {
		t.Fatalf("hidden snapshot swap eagerly started history: factories=%d loading=%v cancel=%v",
			created, m.historyLoading, m.historyLoadCancel != nil)
	}
}

func historyReportWithIssue(id, title string) *correlation.HistoryReport {
	return &correlation.HistoryReport{
		Histories: map[string]correlation.BeadHistory{
			id: {
				BeadID: id,
				Title:  title,
				Commits: []correlation.CorrelatedCommit{
					{SHA: "abc123", ShortSHA: "abc123", Message: title},
				},
			},
		},
	}
}

func TestHistoryLoadRejectsStaleCompletionAfterSnapshotSwap(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "OLD", Title: "Old", Status: model.StatusOpen}}, nil, "")
	m.isHistoryView = true
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("initial history request was not scheduled")
	}
	oldDataGeneration := m.historyLoadDataGeneration
	oldRequestGeneration := m.historyLoadRequestGeneration

	snapshot := NewSnapshotBuilder([]model.Issue{{ID: "NEW", Title: "New", Status: model.StatusOpen}}).Build()
	updated, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 1})
	m = updated.(*Model)
	if !m.historyLoading {
		t.Fatal("snapshot swap did not start a current history request")
	}
	if m.historyLoadRequestGeneration == oldRequestGeneration {
		t.Fatal("snapshot swap reused the previous history request generation")
	}

	staleReport := historyReportWithIssue("OLD", "Old")
	updated, _ = m.Update(HistoryLoadedMsg{
		DataGeneration:    oldDataGeneration,
		RequestGeneration: oldRequestGeneration,
		Report:            staleReport,
	})
	m = updated.(*Model)
	if m.historyView.report == staleReport {
		t.Fatal("stale history completion replaced the current history view")
	}
	if !m.historyLoading {
		t.Fatal("stale history completion cleared current request ownership")
	}

	currentReport := historyReportWithIssue("NEW", "New")
	updated, _ = m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Report:            currentReport,
	})
	m = updated.(*Model)
	if m.historyLoading {
		t.Fatal("accepted history completion left the request active")
	}
	if m.historyView.report != currentReport {
		t.Fatal("current history completion was not installed")
	}
}

func TestHistoryLoadCancelsInjectedBlockingLoader(t *testing.T) {
	type historyRequest struct {
		ctx               context.Context
		dataGeneration    uint64
		requestGeneration uint64
	}

	created := make(chan historyRequest, 2)
	started := make(chan uint64, 2)
	completed := make(chan tea.Msg, 2)
	m := NewModel([]model.Issue{{ID: "OLD", Title: "Old", Status: model.StatusOpen}}, nil, "")
	defer m.cancelHistoryLoad()
	m.historyLoadCommand = func(
		ctx context.Context,
		_ []model.Issue,
		_ string,
		dataGeneration, requestGeneration uint64,
	) tea.Cmd {
		created <- historyRequest{
			ctx:               ctx,
			dataGeneration:    dataGeneration,
			requestGeneration: requestGeneration,
		}
		return func() tea.Msg {
			started <- requestGeneration
			<-ctx.Done()
			return HistoryLoadedMsg{
				DataGeneration:    dataGeneration,
				RequestGeneration: requestGeneration,
				Error:             ctx.Err(),
			}
		}
	}

	firstCmd := m.startHistoryLoad()
	if firstCmd == nil {
		t.Fatal("initial history command was not scheduled")
	}
	first := <-created
	go func() { completed <- firstCmd() }()
	select {
	case got := <-started:
		if got != first.requestGeneration {
			t.Fatalf("started request = %d, want %d", got, first.requestGeneration)
		}
	case <-time.After(time.Second):
		t.Fatal("initial blocking history command did not start")
	}

	requestGeneration := m.historyLoadRequestGeneration
	if cmd := m.startHistoryLoad(); cmd != nil {
		t.Fatal("same-dataset history request was not coalesced")
	}
	if m.historyLoadRequestGeneration != requestGeneration {
		t.Fatal("coalesced history request consumed a request generation")
	}
	select {
	case <-first.ctx.Done():
		t.Fatal("same-dataset coalescing cancelled the shared history request")
	default:
	}
	select {
	case request := <-created:
		t.Fatalf("coalesced history request created command %d", request.requestGeneration)
	default:
	}

	m.beginSemanticDatasetUpdate()
	secondCmd := m.startHistoryLoad()
	if secondCmd == nil {
		t.Fatal("replacement history command was not scheduled")
	}
	second := <-created
	if second.dataGeneration != m.semanticDataGeneration ||
		second.requestGeneration == first.requestGeneration {
		t.Fatalf(
			"replacement ownership = data %d/request %d, current data %d/old request %d",
			second.dataGeneration,
			second.requestGeneration,
			m.semanticDataGeneration,
			first.requestGeneration,
		)
	}
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the superseded history context")
	}
	go func() { completed <- secondCmd() }()
	select {
	case got := <-started:
		if got != second.requestGeneration {
			t.Fatalf("started request = %d, want %d", got, second.requestGeneration)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement blocking history command did not start")
	}

	var stale tea.Msg
	select {
	case stale = <-completed:
	case <-time.After(time.Second):
		t.Fatal("superseded history command was not cancelled")
	}
	updated, _ := m.Update(stale)
	m = updated.(*Model)
	if !m.historyLoading || m.historyLoadRequestGeneration != second.requestGeneration {
		t.Fatal("cancelled stale completion cleared replacement ownership")
	}

	m.historyView.SetReport(historyReportWithIssue("OLD", "Old"))
	m.issues = nil
	m.beginSemanticDatasetUpdate()
	if cmd := m.startHistoryLoad(); cmd != nil {
		t.Fatal("zero-issue dataset scheduled a history command")
	}
	if m.historyLoading || m.historyLoadCancel != nil || m.historyView.report != nil {
		t.Fatal("zero-issue dataset did not cancel and clear history ownership")
	}
	select {
	case <-second.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("zero-issue update did not cancel the replacement history context")
	}
	select {
	case cancelled := <-completed:
		updated, _ = m.Update(cancelled)
		m = updated.(*Model)
		if m.historyView.report != nil || m.historyLoading {
			t.Fatal("cancelled completion resurrected history after zero-issue update")
		}
	case <-time.After(time.Second):
		t.Fatal("zero-issue update did not cancel replacement history command")
	}
}

func TestQuitCommandCancelsHistoryLoad(t *testing.T) {
	var owned context.Context
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadCommand = func(
		ctx context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		owned = ctx
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil || owned == nil {
		t.Fatal("history load did not acquire cancellable ownership")
	}

	quit := m.quitCommand()
	select {
	case <-owned.Done():
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel the active history load")
	}
	if m.historyLoadCancel != nil {
		t.Fatal("quit retained a consumed history cancellation function")
	}
	quitMsg := quit()
	if _, ok := quitMsg.(tea.QuitMsg); !ok {
		t.Fatalf("quit command returned %T, want tea.QuitMsg", quitMsg)
	}
}

func TestStopCancelsHistoryLoad(t *testing.T) {
	var owned context.Context
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadCommand = func(
		ctx context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		owned = ctx
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil || owned == nil {
		t.Fatal("history load did not acquire cancellable ownership")
	}

	m.Stop()
	select {
	case <-owned.Done():
	case <-time.After(time.Second):
		t.Fatal("Model.Stop did not cancel the active history load")
	}
	if m.historyLoadCancel != nil {
		t.Fatal("Model.Stop retained a consumed history cancellation function")
	}
}

func TestEnterHistoryViewSchedulesAsyncRetryWithoutGitWorkOnUpdateLoop(t *testing.T) {
	created := 0
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoading = false
	m.historyLoadFailed = true
	m.historyLoadCommand = func(
		_ context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		created++
		return func() tea.Msg { return nil }
	}

	cmd := m.enterHistoryView()
	if cmd == nil || created != 1 {
		t.Fatalf("enterHistoryView command=%v factories=%d, want one asynchronous retry", cmd != nil, created)
	}
	if !m.isHistoryView || m.focused != focusHistory || !m.historyLoading {
		t.Fatalf("history view state visible=%v focus=%v loading=%v", m.isHistoryView, m.focused, m.historyLoading)
	}
	m.cancelHistoryLoad()
}

func TestEnterHistoryViewTreatsNilLoadCommandAsRetryableFailure(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		return nil
	}

	if cmd := m.enterHistoryView(); cmd != nil {
		t.Fatal("nil factory result unexpectedly produced a command")
	}
	if m.historyLoading || m.historyLoadCancel != nil || !m.historyLoadFailed {
		t.Fatalf("nil command state: loading=%v cancel=%v failed=%v",
			m.historyLoading, m.historyLoadCancel != nil, m.historyLoadFailed)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "no command") {
		t.Fatalf("nil command status=%q error=%v", m.statusMsg, m.statusIsError)
	}
	if view := m.View(); !strings.Contains(view, "History unavailable") {
		t.Fatalf("nil command did not expose retry state: %q", view)
	}
}

func TestGlobalHistoryToggleSchedulesRetryAndKeepsTinyTerminalUsable(t *testing.T) {
	created := 0
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.height = 2
	m.historyLoading = false
	m.historyLoadFailed = true
	m.historyLoadCommand = func(
		_ context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		created++
		return func() tea.Msg { return nil }
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(*Model)
	if cmd == nil || created != 1 || !m.isHistoryView || m.focused != focusHistory {
		t.Fatalf("global h command=%v factories=%d visible=%v focus=%v", cmd != nil, created, m.isHistoryView, m.focused)
	}
	if m.historyView.height < 5 {
		t.Fatalf("tiny-terminal history height=%d, want floor 5", m.historyView.height)
	}
	m.cancelHistoryLoad()
}

func TestHistoryCompletionKeepsTinyTerminalUsableAndClearsLoadingStatus(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.height = 2
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("history load was not scheduled")
	}

	report := historyReportWithIssue("A", "Alpha")
	report.Stats.BeadsWithCommits = 1
	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Report:            report,
	})
	m = updated.(*Model)

	if m.historyView.height < 5 {
		t.Fatalf("completed history height=%d, want floor 5", m.historyView.height)
	}
	if m.statusIsError || m.statusMsg != "Loaded history: 1 beads with commits" {
		t.Fatalf("completed history status=%q error=%v", m.statusMsg, m.statusIsError)
	}
	_ = m.View()
	if m.historyView.height < 5 {
		t.Fatalf("render reset completed history height=%d below floor 5", m.historyView.height)
	}
}

func TestHiddenHistoryCompletionClearsOnlyItsOwnLoadingStatus(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}
	if cmd := m.enterHistoryView(); cmd == nil {
		t.Fatal("history load was not scheduled")
	}
	m.isHistoryView = false
	m.focused = focusList

	report := historyReportWithIssue("A", "Alpha")
	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Report:            report,
	})
	m = updated.(*Model)
	if m.statusMsg != "" || m.statusIsError {
		t.Fatalf("hidden completion retained loading status=%q error=%v", m.statusMsg, m.statusIsError)
	}

	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("second history load was not scheduled")
	}
	m.statusMsg = "Newer unrelated status"
	updated, _ = m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Report:            report,
	})
	m = updated.(*Model)
	if m.statusMsg != "Newer unrelated status" {
		t.Fatalf("hidden completion overwrote newer status: %q", m.statusMsg)
	}
}

func TestHiddenHistoryFailurePreservesNewerStatusAndClearsOnlyItsOwnLoadingStatus(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}
	if cmd := m.enterHistoryView(); cmd == nil {
		t.Fatal("history load was not scheduled")
	}
	m.isHistoryView = false
	m.focused = focusList
	m.statusMsg = "Newer unrelated status"

	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Error:             errors.New("background failure"),
	})
	m = updated.(*Model)
	if !m.historyLoadFailed || m.historyLoading {
		t.Fatalf("hidden failure state: failed=%v loading=%v", m.historyLoadFailed, m.historyLoading)
	}
	if m.statusMsg != "Newer unrelated status" || m.statusIsError {
		t.Fatalf("hidden failure overwrote newer status=%q error=%v", m.statusMsg, m.statusIsError)
	}

	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("second history load was not scheduled")
	}
	m.statusMsg = "History is loading…"
	updated, _ = m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Error:             errors.New("background failure"),
	})
	m = updated.(*Model)
	if m.statusMsg != "" || m.statusIsError || !m.historyLoadFailed {
		t.Fatalf("hidden failure retained owned loading status=%q error=%v failed=%v",
			m.statusMsg, m.statusIsError, m.historyLoadFailed)
	}
}

func TestHistoryCompletionRejectsNilReportAsRetryableFailure(t *testing.T) {
	created := 0
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		created++
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("history load was not scheduled")
	}

	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
	})
	m = updated.(*Model)
	if !m.historyLoadFailed || m.historyLoading || !m.statusIsError ||
		!strings.Contains(m.statusMsg, "no report") {
		t.Fatalf("nil report state: failed=%v loading=%v status=%q error=%v",
			m.historyLoadFailed, m.historyLoading, m.statusMsg, m.statusIsError)
	}
	if view := m.View(); !strings.Contains(view, "History unavailable") {
		t.Fatalf("nil report view did not expose retry state: %q", view)
	}

	updated, retryCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(*Model)
	if retryCmd == nil || created != 2 || !m.historyLoading {
		t.Fatalf("nil report retry command=%v factories=%d loading=%v", retryCmd != nil, created, m.historyLoading)
	}
	m.cancelHistoryLoad()
}

func TestHistoryFailureHidesRetainedReportAtGenerationZero(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.semanticDataGeneration = 0
	if m.semanticDataGeneration != 0 {
		t.Fatalf("fixture generation=%d, want zero-value generation", m.semanticDataGeneration)
	}
	m.historyView.SetReport(historyReportWithIssue("A", "Old report"))
	m.historyReportDataGeneration = 0
	m.historyLoadCommand = func(
		context.Context,
		[]model.Issue,
		string,
		uint64,
		uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("history refresh was not scheduled")
	}

	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Error:             errors.New("refresh failed"),
	})
	m = updated.(*Model)
	if m.historyReportIsCurrent() {
		t.Fatal("failed generation-zero refresh left retained report current")
	}
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("generation-zero retry was not scheduled")
	}
	if m.historyReportIsCurrent() {
		t.Fatal("generation-zero retry exposed retained report while replacement was loading")
	}
	m.cancelHistoryLoad()
}

func TestHistoryViewExplainsEmptyDataset(t *testing.T) {
	m := NewModel(nil, nil, "")
	if cmd := m.enterHistoryView(); cmd != nil {
		t.Fatal("empty dataset scheduled history work")
	}
	if view := m.View(); !strings.Contains(view, "No issue history available") || strings.Contains(view, "Loading history") {
		t.Fatalf("empty history view did not explain terminal state: %q", view)
	}
}

func TestHistoryFailureDoesNotCarryIntoReplacementEmptyDataset(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyLoadFailed = true
	m.issues = nil
	m.beginSemanticDatasetUpdate()

	if cmd := m.enterHistoryView(); cmd != nil {
		t.Fatal("empty replacement dataset scheduled history work")
	}
	if m.historyLoadFailed {
		t.Fatal("replacement dataset retained prior history failure")
	}
	if view := m.View(); !strings.Contains(view, "No issue history available") || strings.Contains(view, "History unavailable") {
		t.Fatalf("replacement empty history rendered stale failure: %q", view)
	}
}

func TestHistoryFileSelectionReportsFilterToggleAccurately(t *testing.T) {
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID: "A",
			Commits: []correlation.CorrelatedCommit{{
				SHA:   "commit-a",
				Files: []correlation.FileChange{{Path: "target.go"}},
			}},
		},
	}}
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyView.ToggleFileTree()
	m.historyView.SetFileTreeFocus(true)

	m, _ = m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.historyView.GetFileFilter() != "target.go" || !strings.Contains(m.statusMsg, "Filtering by") {
		t.Fatalf("setting file filter produced filter=%q status=%q", m.historyView.GetFileFilter(), m.statusMsg)
	}
	m, _ = m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.historyView.GetFileFilter() != "" || !strings.Contains(m.statusMsg, "cleared") {
		t.Fatalf("clearing file filter produced filter=%q status=%q", m.historyView.GetFileFilter(), m.statusMsg)
	}
}

func TestHistoryRefreshHidesAndDisablesPreviousDatasetReport(t *testing.T) {
	const oldTitle = "OLD-DATASET-SENTINEL"
	m := NewModel([]model.Issue{{ID: "OLD", Title: oldTitle, Status: model.StatusOpen}}, nil, "")
	oldReport := historyReportWithIssue("OLD", oldTitle)
	m.historyView.SetReport(oldReport)
	m.historyReportDataGeneration = m.semanticDataGeneration
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyLoading = false
	m.historyLoadCommand = func(
		_ context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}

	m.issues = []model.Issue{{ID: "NEW", Title: "New", Status: model.StatusOpen}}
	m.beginSemanticDatasetUpdate()
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("new dataset did not start a history load")
	}
	if m.historyReportIsCurrent() {
		t.Fatal("previous dataset report remained current after generation advance")
	}
	view := m.View()
	if strings.Contains(view, oldTitle) || !strings.Contains(view, "Loading history") {
		t.Fatalf("history refresh view exposed stale report or omitted loading state: %q", view)
	}
	selectedBefore := m.historyView.SelectedBeadID()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(*Model)
	if got := m.historyView.SelectedBeadID(); got != selectedBefore {
		t.Fatalf("stale history accepted navigation: got %q, want %q", got, selectedBefore)
	}
	m.cancelHistoryLoad()
}

func TestNonCurrentHistoryAllowsGlobalViewTransitions(t *testing.T) {
	type expectedView int
	const (
		expectBoard expectedView = iota
		expectActionable
		expectInsights
		expectTree
	)

	states := []struct {
		name  string
		setup func(*Model)
	}{
		{
			name: "loading",
			setup: func(m *Model) {
				m.historyLoading = true
			},
		},
		{
			name: "failed",
			setup: func(m *Model) {
				m.historyLoadFailed = true
			},
		},
		{
			name: "stale_generation",
			setup: func(m *Model) {
				m.semanticDataGeneration++
			},
		},
	}
	transitions := []struct {
		key  rune
		want expectedView
	}{
		{key: 'b', want: expectBoard},
		{key: 'a', want: expectActionable},
		{key: 'i', want: expectInsights},
		{key: 'E', want: expectTree},
	}

	for _, state := range states {
		for _, transition := range transitions {
			t.Run(fmt.Sprintf("%s_%c", state.name, transition.key), func(t *testing.T) {
				m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
				m.historyView.SetReport(historyReportWithIssue("A", "Alpha"))
				m.historyReportDataGeneration = m.semanticDataGeneration
				m.isHistoryView = true
				m.focused = focusHistory
				state.setup(m)
				if m.historyReportIsCurrent() {
					t.Fatal("fixture unexpectedly has a current History report")
				}

				updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{transition.key}})
				m = updated.(*Model)
				if m.isHistoryView {
					t.Fatalf("global %q left non-current History visible", transition.key)
				}
				switch transition.want {
				case expectBoard:
					if !m.isBoardView || m.focused != focusBoard {
						t.Fatalf("global b transition: board=%v focus=%v", m.isBoardView, m.focused)
					}
				case expectActionable:
					if !m.isActionableView || m.focused != focusActionable {
						t.Fatalf("global a transition: actionable=%v focus=%v", m.isActionableView, m.focused)
					}
				case expectInsights:
					if m.focused != focusInsights {
						t.Fatalf("global i transition focus=%v, want insights", m.focused)
					}
				case expectTree:
					if m.focused != focusTree {
						t.Fatalf("global E transition focus=%v, want tree", m.focused)
					}
				}
			})
		}
	}
}

func TestNonCurrentHistoryKeepsHistoryLocalGraphActionConsumed(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.historyView.SetReport(historyReportWithIssue("A", "Alpha"))
	m.historyReportDataGeneration = m.semanticDataGeneration
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyLoading = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(*Model)
	if !m.isHistoryView || m.isGraphView || m.focused != focusHistory {
		t.Fatalf("non-current History g leaked globally: history=%v graph=%v focus=%v",
			m.isHistoryView, m.isGraphView, m.focused)
	}
}

func TestHistoryViewKeepsFinalFooterRowVisible(t *testing.T) {
	const footerSentinel = "FINAL-HISTORY-FOOTER-SENTINEL"
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	commits := make([]correlation.CorrelatedCommit, 10)
	for i := range commits {
		commits[i] = correlation.CorrelatedCommit{
			SHA:        fmt.Sprintf("commit-%02d", i),
			ShortSHA:   fmt.Sprintf("c-%02d", i),
			Message:    fmt.Sprintf("two-row timeline message %d", i),
			Timestamp:  now.Add(time.Duration(i) * time.Minute),
			Confidence: 0.9,
		}
	}
	cycle := 72 * time.Hour
	report := &correlation.HistoryReport{Histories: map[string]correlation.BeadHistory{
		"A": {
			BeadID:    "A",
			Title:     "Footer clipping regression",
			Commits:   commits,
			CycleTime: &correlation.CycleTime{CreateToClose: &cycle},
		},
	}}

	m := NewModel([]model.Issue{{ID: "A", Status: model.StatusOpen}}, nil, "")
	m.ready = true
	m.snapshotInitPending = false
	m.width = 220
	m.height = 24
	m.historyView = NewHistoryModel(report, m.theme)
	m.historyReportDataGeneration = m.semanticDataGeneration
	m.isHistoryView = true
	m.focused = focusHistory
	m.statusMsg = footerSentinel

	view := m.View()
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("full History view height=%d, want terminal height %d", got, m.height)
	}
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[len(lines)-1], footerSentinel) {
		t.Fatalf("final rendered row omitted footer sentinel: %q", lines[len(lines)-1])
	}
}

func TestStaleHistoryDoesNotLeakIntoIssueDetail(t *testing.T) {
	const staleCommit = "OLD-HISTORY-COMMIT-SENTINEL"
	m := NewModel([]model.Issue{{ID: "A", Title: "Current issue", Status: model.StatusOpen}}, nil, "")
	report := historyReportWithIssue("A", staleCommit)
	m.historyView.SetReport(report)
	m.historyReportDataGeneration = m.semanticDataGeneration

	if current := m.renderBeadHistoryMD("A"); !strings.Contains(current, staleCommit) {
		t.Fatalf("current history was not rendered: %q", current)
	}
	m.beginSemanticDatasetUpdate()
	if stale := m.renderBeadHistoryMD("A"); stale != "" {
		t.Fatalf("stale history leaked into issue detail after generation advance: %q", stale)
	}
}

func TestHistoryFailureRetainsHiddenSelectionAndHRetriesInPlace(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "OLD", Status: model.StatusOpen}}, nil, "")
	oldReport := historyReportWithIssue("OLD", "Old")
	m.historyView.SetReport(oldReport)
	m.historyReportDataGeneration = m.semanticDataGeneration
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyLoading = false
	m.issues = []model.Issue{{ID: "NEW", Status: model.StatusOpen}}
	m.beginSemanticDatasetUpdate()
	m.historyLoadCommand = func(
		_ context.Context,
		_ []model.Issue,
		_ string,
		_, _ uint64,
	) tea.Cmd {
		return func() tea.Msg { return nil }
	}
	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("new dataset did not start a history load")
	}
	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Error:             errors.New("temporary git failure"),
	})
	m = updated.(*Model)
	if m.historyView.report != oldReport || m.historyView.SelectedBeadID() != "OLD" {
		t.Fatal("failed refresh discarded hidden report identity")
	}
	if m.historyReportIsCurrent() {
		t.Fatal("failed refresh exposed the previous generation as current")
	}
	updated, retryCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(*Model)
	if retryCmd == nil || !m.isHistoryView || !m.historyLoading {
		t.Fatalf("one-key retry command=%v visible=%v loading=%v", retryCmd != nil, m.isHistoryView, m.historyLoading)
	}
	m.cancelHistoryLoad()
}

func TestHistoryRefreshPreservesActiveSearchSession(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "NEW", Title: "Beta work", Status: model.StatusOpen}}, nil, "")
	m.historyView = NewHistoryModel(historyReportWithIssue("OLD", "Alpha work"), m.theme)
	m.isHistoryView = true
	m.focused = focusHistory
	focusCmd := m.historyView.StartSearch()
	m.historyView.searchInput.SetValue("beta")
	m.historyView.lastSearchQuery = "beta"
	m.historyView.applySearchFilter()
	_ = m.beginEmbeddedTextInputSession(embeddedTextInputHistorySearch, focusCmd)
	session := m.embeddedTextInputSession

	if cmd := m.startHistoryLoad(); cmd == nil {
		t.Fatal("history refresh was not scheduled")
	}
	refreshed := historyReportWithIssue("NEW", "Beta work")
	updated, _ := m.Update(HistoryLoadedMsg{
		DataGeneration:    m.historyLoadDataGeneration,
		RequestGeneration: m.historyLoadRequestGeneration,
		Report:            refreshed,
	})
	m = updated.(*Model)

	if got := m.historyView.SearchQuery(); got != "beta" {
		t.Fatalf("search query = %q, want beta", got)
	}
	if !m.historyView.IsSearchActive() || !m.historyView.searchInput.Focused() {
		t.Fatal("history refresh dropped active search focus")
	}
	if !m.embeddedTextInputSessionIsActive(session) {
		t.Fatal("history refresh invalidated the active embedded-input session")
	}
	if len(m.historyView.beadIDs) != 1 || m.historyView.beadIDs[0] != "NEW" {
		t.Fatalf("refreshed search results = %#v, want [NEW]", m.historyView.beadIDs)
	}
}

func TestUpdateFileChangedReloadsSQLiteSource(t *testing.T) {
	t.Setenv("BV_BACKGROUND_MODE", "0")

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL
		);
		INSERT INTO issues (id, title, status) VALUES ('SQLITE-1', 'SQLite issue', 'open');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	m := NewModel(nil, nil, dbPath)
	if m.watcher != nil {
		defer m.watcher.Stop()
	}

	updated, _ := m.Update(FileChangedMsg{})
	m2 := updated.(*Model)
	if m2.statusIsError {
		t.Fatalf("expected successful sqlite reload, got error %q", m2.statusMsg)
	}
	if len(m2.issues) != 1 || m2.issues[0].ID != "SQLITE-1" {
		t.Fatalf("unexpected sqlite reload issues: %#v", m2.issues)
	}
}

func TestLoadIssuesForReloadSQLiteHonorsIssueFilter(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL
		);
		INSERT INTO issues (id, title, status) VALUES
			('OPEN-1', 'Open issue', 'open'),
			('CLOSED-1', 'Closed issue', 'closed');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	loaded, err := loadIssuesForReload(dbPath, loader.ParseOptions{
		IssueFilter: func(issue *model.Issue) bool {
			return issue.Status != model.StatusClosed
		},
	})
	if err != nil {
		t.Fatalf("load sqlite reload issues: %v", err)
	}
	if len(loaded.Issues) != 1 || loaded.Issues[0].ID != "OPEN-1" {
		t.Fatalf("unexpected filtered sqlite issues: %#v", loaded.Issues)
	}
}

func TestBackgroundWorkerCountsSQLiteRowsInsteadOfBinaryNewlines(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL
		);
		CREATE TABLE binary_noise (payload BLOB);
		INSERT INTO issues (id, title, status) VALUES
			('OPEN-1', 'Open issue', 'open'),
			('CLOSED-1', 'Closed issue', 'closed');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed sqlite: %v", err)
	}
	if _, err := db.Exec("INSERT INTO binary_noise(payload) VALUES (?)", strings.Repeat("\n", 20_050)); err != nil {
		_ = db.Close()
		t.Fatalf("seed sqlite newline noise: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	if binaryLines, err := countJSONLLines(dbPath); err != nil || binaryLines < 20_000 {
		t.Fatalf("fixture does not expose binary-newline miscount: lines=%d err=%v", binaryLines, err)
	}
	if issueCount, err := countIssuesForReload(dbPath); err != nil || issueCount != 2 {
		t.Fatalf("SQLite issue count=%d err=%v, want 2", issueCount, err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: dbPath})
	if err != nil {
		t.Fatalf("NewBackgroundWorker: %v", err)
	}
	defer worker.Stop()
	snapshot := worker.buildSnapshot(false)
	if snapshot == nil {
		t.Fatal("SQLite background snapshot is nil")
	}
	defer snapshot.releasePooledIssues()
	if snapshot.DatasetTier != datasetTierSmall || snapshot.SourceIssueCountHint != 2 {
		t.Fatalf("SQLite snapshot tier/count=%v/%d, want small/2", snapshot.DatasetTier, snapshot.SourceIssueCountHint)
	}
	if snapshot.LoadedOpenOnly || snapshot.TruncatedCount != 0 {
		t.Fatalf("SQLite snapshot was spuriously truncated: openOnly=%v truncated=%d", snapshot.LoadedOpenOnly, snapshot.TruncatedCount)
	}
	if len(snapshot.Issues) != 2 {
		t.Fatalf("SQLite snapshot loaded %d issues, want both open and closed rows", len(snapshot.Issues))
	}
}

func TestNewModel_SetsTreeBeadsDirFromBeadsPath(t *testing.T) {
	tmp := t.TempDir()
	beads := filepath.Join(tmp, "beads.jsonl")
	if err := os.WriteFile(beads, []byte(`{"id":"ONE","title":"One","status":"open"}`+"\n"), 0644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	m := NewModel(nil, nil, beads)
	if m.watcher != nil {
		m.watcher.Stop()
	}

	if got, want := m.tree.beadsDir, filepath.Dir(beads); got != want {
		t.Fatalf("expected tree beadsDir %q, got %q", want, got)
	}
}
