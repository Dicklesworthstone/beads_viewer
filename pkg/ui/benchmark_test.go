package ui

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
	json "github.com/goccy/go-json"
)

func copyIssues(in []model.Issue) []model.Issue {
	if in == nil {
		return nil
	}
	out := make([]model.Issue, len(in))
	copy(out, in)
	return out
}

func BenchmarkPerformanceBoardGrouping(b *testing.B) {
	issues, err := testutil.PerformanceIssues("unicode", 10000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	for _, mode := range []SwimLaneMode{SwimByStatus, SwimByPriority, SwimByType} {
		b.Run(fmt.Sprint(mode), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				groupIssuesByMode(issues, mode)
			}
		})
	}
}

func BenchmarkPerformanceFooterAlerts(b *testing.B) {
	issues, err := testutil.PerformanceIssues("unicode", 10000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	m := settledPerformanceModel(b, issues)
	if len(m.alerts) == 0 {
		b.Fatal("fixture must exercise actual computed alert badge")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.renderFooter()
	}
}

func TestPerformanceFooterAlertDismissalParity(t *testing.T) {
	issues, err := testutil.PerformanceIssues("realistic", 128, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	m := settledPerformanceModel(t, issues)
	if len(m.alerts) == 0 {
		t.Fatal("fixture must exercise actual computed alerts")
	}
	wantCount := len(m.alerts)
	empty := m.renderFooter()
	if !strings.Contains(ansi.Strip(empty), fmt.Sprintf("%d alerts", wantCount)) {
		t.Fatal("empty dismissal map lost actual active alert count")
	}
	m.dismissedAlerts = map[string]bool{"not-a-current-alert": true}
	if got := m.renderFooter(); got != empty {
		t.Fatal("empty dismissal fast path changed exact footer compared with active map lookup")
	}
	firstKey := alertKey(m.alerts[0])
	m.dismissedAlerts[firstKey] = true
	for _, alert := range m.alerts {
		if alertKey(alert) == firstKey {
			wantCount--
		}
	}
	if got := ansi.Strip(m.renderFooter()); !strings.Contains(got, fmt.Sprintf("%d alerts", wantCount)) {
		t.Fatalf("dismissed alert still counted in footer: %s", got)
	}
	for _, alert := range m.alerts {
		m.dismissedAlerts[alertKey(alert)] = true
	}
	if got := ansi.Strip(m.renderFooter()); strings.Contains(got, " alerts (!)") {
		t.Fatal("all-dismissed alerts still showed a badge")
	}
}

func TestPerformanceBoardGroupingPreservesFallbackColumns(t *testing.T) {
	issues := []model.Issue{
		{ID: "custom", Status: "custom", IssueType: "custom", Priority: -1},
		{ID: "deleted", Status: model.StatusTombstone, IssueType: model.TypeEpic, Priority: 4},
		{ID: "closed", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 3},
		{ID: "blocked", Status: model.StatusBlocked, IssueType: model.TypeBug, Priority: 2},
		{ID: "progress", Status: model.StatusInProgress, IssueType: model.TypeFeature, Priority: 1},
		{ID: "open", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 0},
	}
	for _, tc := range []struct {
		mode SwimLaneMode
		want [4]string
	}{
		{SwimByStatus, [4]string{"custom,open", "progress", "blocked", "closed,deleted"}},
		{SwimByPriority, [4]string{"open", "progress", "blocked", "custom,closed,deleted"}},
		{SwimByType, [4]string{"blocked", "progress", "custom,open,closed", "deleted"}},
		{SwimLaneMode(99), [4]string{"custom,open,progress,blocked,closed,deleted", "", "", ""}},
	} {
		columns := groupIssuesByMode(issues, tc.mode)
		for i, column := range columns {
			ids := make([]string, len(column))
			for j := range column {
				ids[j] = column[j].ID
			}
			if strings.Join(ids, ",") != tc.want[i] {
				t.Fatalf("mode%d column%d got%v want%s", tc.mode, i, ids, tc.want[i])
			}
		}
	}
	if issues[0].ID != "custom" || issues[5].ID != "open" {
		t.Fatal("grouping sorted the caller's source slice")
	}
}

func BenchmarkSnapshotSwap(b *testing.B) {
	for _, size := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("issues=%d", size), func(b *testing.B) {
			issues := testutil.QuickRandom(size, 0.01)
			modifiedIssues := copyIssues(issues)
			modifiedID := modifiedIssues[len(modifiedIssues)/2].ID
			modifiedIssues[len(modifiedIssues)/2].Title += " updated"

			m := NewModel(copyIssues(issues), nil, "")
			snapshots := [2]*DataSnapshot{
				NewSnapshotBuilder(copyIssues(issues)).Build(),
				NewSnapshotBuilder(modifiedIssues).Build(),
			}
			for _, snapshot := range snapshots {
				snapshot.IssueDiff = &analysis.IssueDiff{Modified: []string{modifiedID}}
			}

			tm, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshots[0]})
			m = tm.(*Model)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tm, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshots[i&1]})
				m = tm.(*Model)
			}
		})
	}
}

func BenchmarkDuplicateSnapshotDelivery(b *testing.B) {
	issues := testutil.QuickRandom(1000, 0.01)
	m := NewModel(copyIssues(issues), nil, "")
	snapshot := NewSnapshotBuilder(copyIssues(issues)).Build()
	tm, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshot})
	m = tm.(*Model)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tm, _ = m.Update(SnapshotReadyMsg{Snapshot: snapshot})
		m = tm.(*Model)
	}
}

func BenchmarkSnapshotViewSyncComponents(b *testing.B) {
	issues := testutil.QuickRandom(1000, 0.01)
	m := NewModel(copyIssues(issues), nil, "")
	snapshot := NewSnapshotBuilder(copyIssues(issues)).Build()

	b.Run("list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.installSnapshotListItems(snapshot)
		}
	})
	b.Run("board", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.board.SetSnapshot(snapshot)
		}
	})
	b.Run("graph", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.graphView.SetSnapshot(snapshot)
		}
	})
	b.Run("insights", func(b *testing.B) {
		insights := snapshot.GetInsights()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.insightsPanel.SetInsights(insights)
		}
	})
}

func BenchmarkKeyPressLatency(b *testing.B) {
	issues := testutil.QuickRandom(1000, 0.01)
	m := NewModel(issues, nil, "")
	durations := make([]time.Duration, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(*Model)
		if view := m.View(); view == "" {
			b.Fatal("View returned empty output")
		}
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99Index := (99*len(durations)+99)/100 - 1
	b.ReportMetric(float64(durations[p99Index].Nanoseconds()), "p99-ns/op")
}

// Keep the settled workload separate from the original tracked benchmark so
// release comparisons retain identical inputs, dimensions, and key sequences.
func BenchmarkSettledKeyPressLatency(b *testing.B) {
	issues, err := testutil.PerformanceIssues("realistic", 1000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	m := settledPerformanceModel(b, issues)
	durations := make([]time.Duration, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		elapsed, err := performanceNavigationStep(m, i, 0, true)
		if err != nil {
			b.Fatal(err)
		}
		durations = append(durations, elapsed)
	}
	b.StopTimer()

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99Index := (99*len(durations)+99)/100 - 1
	b.ReportMetric(float64(durations[p99Index].Nanoseconds()), "p99-ns/op")
}

func BenchmarkKeyPressUpdateLatency(b *testing.B) {
	issues := testutil.QuickRandom(1000, 0.01)
	m := NewModel(issues, nil, "")
	durations := make([]time.Duration, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(*Model)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99Index := (99*len(durations)+99)/100 - 1
	b.ReportMetric(float64(durations[p99Index].Nanoseconds()), "p99-ns/op")
}

func BenchmarkSettledKeyPressUpdateLatency(b *testing.B) {
	issues, err := testutil.PerformanceIssues("realistic", 1000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	m := settledPerformanceModel(b, issues)
	durations := make([]time.Duration, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		elapsed, err := performanceNavigationStep(m, i, 0, false)
		if err != nil {
			b.Fatal(err)
		}
		durations = append(durations, elapsed)
	}
	b.StopTimer()

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99Index := (99*len(durations)+99)/100 - 1
	b.ReportMetric(float64(durations[p99Index].Nanoseconds()), "p99-ns/op")
}

func settledPerformanceModel(t testing.TB, issues []model.Issue) *Model {
	t.Helper()
	m := NewModel(copyIssues(issues), nil, "")
	m.workDir = t.TempDir()
	if output, err := exec.Command("git", "-C", m.workDir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("initializing isolated performance history source: %v\n%s", err, output)
	}
	// Pin scoring semantics through the analyzer's existing clock seam while
	// preserving production size-tiered metric timeouts and wall-clock timing.
	m.analyzer.SetNow(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	t.Cleanup(m.Stop)
	completion, ok := m.preparePhase2Cmd()().(Phase2ReadyMsg)
	if !ok || completion.prepared == nil {
		t.Fatal("performance setup did not prepare a Phase2 snapshot")
	}
	m.Update(completion)
	if m.snapshot != completion.prepared || !m.snapshot.IsPhase2Ready() ||
		!m.snapshot.Analyzer.Now().Equal(m.analyzer.Now()) || !completion.sourceNow.Equal(m.analyzer.Now()) {
		t.Fatal("performance setup did not install the completed snapshot at the captured clock")
	}
	m.list.Select(0)
	if !m.ready || !m.analysis.IsPhase2Ready() || len(m.list.Items()) < 2 {
		t.Fatal("performance model must be sized, settled, and navigable")
	}
	id := m.selectedListIssueID(false, "")
	if id == "" || !strings.Contains(m.View(), id) {
		t.Fatalf("settled View does not render selected issue %q", id)
	}
	return m
}

// Execute the actual completion command returned by Update, including its
// Bubble Tea batch. An unexpected message means the cohort no longer covers
// the real command path and must be adapted instead of synthesizing success.
func performancePhase2Command(cmd tea.Cmd) ([]tea.Msg, error) {
	if cmd == nil {
		return nil, fmt.Errorf("snapshot did not return a Phase2 command")
	}
	switch msg := cmd().(type) {
	case Phase2ReadyMsg, HistoryLoadedMsg:
		return []tea.Msg{msg}, nil
	case tea.BatchMsg:
		results := make([][]tea.Msg, len(msg))
		errors := make([]error, len(msg))
		var workers sync.WaitGroup
		for i, child := range msg {
			workers.Add(1)
			go func() {
				defer workers.Done()
				results[i], errors[i] = performancePhase2Command(child)
			}()
		}
		workers.Wait()
		var messages []tea.Msg
		for i := range results {
			if errors[i] != nil {
				return nil, errors[i]
			}
			messages = append(messages, results[i]...)
		}
		return messages, nil
	default:
		return nil, fmt.Errorf("unexpected snapshot completion message %T", msg)
	}
}

// Walk the list in both directions so every sample changes selection, even
// after b.N exceeds the issue count. Visiting distinct issues also prevents a
// tiny two-entry render cache from looking like general navigation performance.
func performanceNavigationStep(m *Model, sample int, delay time.Duration, render bool) (time.Duration, error) {
	before := m.selectedListIssueID(false, "")
	key := tea.KeyDown
	span := len(m.list.Items()) - 1
	if span < 1 {
		return 0, fmt.Errorf("navigation requires at least two list items")
	}
	if sample%(2*span) >= span {
		key = tea.KeyUp
	}
	// A snapshot can preserve the selected ID at a different row index.
	if m.list.Index() == 0 {
		key = tea.KeyDown
	} else if m.list.Index() == span {
		key = tea.KeyUp
	}
	started := time.Now()
	if delay > 0 {
		time.Sleep(delay) // Deliberate slow-handler negative control only.
	}
	m.Update(tea.KeyMsg{Type: key})
	var view string
	if render {
		view = m.View()
	}
	elapsed := time.Since(started)
	after := m.selectedListIssueID(false, "")
	if after == "" || after == before {
		return elapsed, fmt.Errorf("sample %d did not move selection: %q -> %q", sample, before, after)
	}
	if render && !strings.Contains(view, after) {
		return elapsed, fmt.Errorf("sample %d View does not render selected issue %q", sample, after)
	}
	return elapsed, nil
}

func performanceListIDs(m *Model) []string {
	ids := make([]string, 0, len(m.list.Items()))
	for _, entry := range m.list.Items() {
		if issue, ok := entry.(IssueItem); ok {
			ids = append(ids, issue.Issue.ID)
		}
	}
	return ids
}

func TestPerformanceNavigationControls(t *testing.T) {
	issues, err := testutil.PerformanceIssues("realistic", 20, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	m := settledPerformanceModel(t, issues)
	for i := 0; i < 2*len(issues); i++ {
		if _, err := performanceNavigationStep(m, i, 0, true); err != nil {
			t.Fatal(err)
		}
	}
	m.list.Select(0)
	// The same production Update/View path, with an injected 60ms handler,
	// must trip the 50ms gate. This is a controlled negative, not a host sample.
	var slow []time.Duration
	for i := 0; i < 3; i++ {
		elapsed, err := performanceNavigationStep(m, i, 60*time.Millisecond, true)
		if err != nil {
			t.Fatal(err)
		}
		slow = append(slow, elapsed)
	}
	if err := testutil.CheckInteractionLatency(slow); err == nil {
		t.Fatal("slow production handler passed the interaction gate")
	}
}

func TestPerformanceSnapshotRefreshRendersChangedSelectedBody(t *testing.T) {
	issues, err := testutil.PerformanceIssues("realistic", 20, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	m := settledPerformanceModel(t, issues)
	selected := m.selectedListIssueID(false, "")
	changed := copyIssues(issues)
	for i := range changed {
		if changed[i].ID == selected {
			changed[i].Description = "SNAPSHOT_UPDATED_BODY"
		}
	}
	snapshot := NewSnapshotBuilder(changed).Build()
	snapshot.Analysis.WaitForPhase2()
	m.Update(SnapshotReadyMsg{Snapshot: snapshot})
	if got := m.selectedListIssueID(false, ""); got != selected {
		t.Fatalf("snapshot changed selection: got %q want %q", got, selected)
	}
	m.viewport.GotoBottom()
	if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "SNAPSHOT_UPDATED_BODY") {
		t.Fatalf("same-graph selected body change reused stale render: %s", view)
	}
}

// TestPerformanceNavigationCohorts is explicitly invoked by benchmark.sh latency.
// Normal go test runs retain the fast positive/negative controls above. Results
// contain every sample; no best-of-N selection or terminal-paint claim is made.
func TestPerformanceNavigationCohorts(t *testing.T) {
	outDir := os.Getenv("BV_PERF_DIR")
	if outDir == "" {
		t.Skip("opt-in measurement: scripts/benchmark.sh latency")
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	binaryHash := sha256.New()
	_, hashErr := io.Copy(binaryHash, binary)
	closeErr := binary.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash measurement executable: %v; close: %v", hashErr, closeErr)
	}
	executableHash := fmt.Sprintf("%x", binaryHash.Sum(nil))
	samples := 1000
	if value := os.Getenv("BV_PERF_UI_SAMPLES"); value != "" {
		var err error
		samples, err = strconv.Atoi(value)
		if err != nil || samples < 200 {
			t.Fatal("BV_PERF_UI_SAMPLES must be at least 200; p99 is descriptive")
		}
	}
	for _, kind := range testutil.PerformanceWorkloadNames() {
		for _, size := range []int{1000, 5000, 10000} {
			t.Run(fmt.Sprintf("%s/%d", kind, size), func(t *testing.T) {
				issues, err := testutil.PerformanceIssues(kind, size, 20260904)
				if err != nil {
					t.Fatal(err)
				}
				fixture, err := json.Marshal(issues)
				if err != nil {
					t.Fatal(err)
				}
				for _, refresh := range []bool{false, true} {
					mode := "navigation"
					if refresh {
						mode = "refresh"
					}
					t.Run(mode, func(t *testing.T) {
						setupStarted := time.Now()
						m := settledPerformanceModel(t, issues)
						setupElapsed := time.Since(setupStarted)
						configHash := analysis.ComputeConfigHash(&m.analysis.Config)
						priorityHints := make([]analysis.PriorityRecommendation, 0, len(m.priorityHints))
						for _, rec := range m.priorityHints {
							priorityHints = append(priorityHints, *rec)
						}
						sort.Slice(priorityHints, func(i, j int) bool { return priorityHints[i].IssueID < priorityHints[j].IssueID })
						statusJSON, err := json.Marshal(m.analysis.Status())
						if err != nil {
							t.Fatal(err)
						}
						status, err := testutil.PerformanceMetricStates(statusJSON)
						if err != nil {
							t.Fatal(err)
						}
						// Build real immutable snapshots concurrently with the UI loop.
						// No fsnotify timing is claimed: delivery enters at SnapshotReadyMsg.
						type refreshDelivery struct {
							snapshot   *DataSnapshot
							buildTime  time.Duration
							generation int
						}
						ready := make(chan refreshDelivery, 1)
						stop := make(chan struct{})
						done := make(chan struct{})
						var firstDelivery refreshDelivery
						if refresh {
							go func() {
								defer close(done)
								var previous *DataSnapshot
								for generation := 1; ; generation++ {
									select {
									case <-stop:
										return
									default:
									}
									changed := copyIssues(issues)
									changed[len(changed)-1].Title += fmt.Sprintf(" refresh %d", generation)
									buildStarted := time.Now()
									builder := NewSnapshotBuilder(changed).
										WithBuildConfig(snapshotBuildConfigForTier(datasetTierForIssueCount(size)))
									builder.analyzer.SetNow(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
									if previous != nil {
										diff := analysis.ComputeIssueDiff(previous.Issues, changed)
										builder.WithPreviousSnapshot(previous, &diff)
									}
									snapshot := builder.Build()
									snapshot.Analysis.WaitForPhase2()
									previous = snapshot
									select {
									case ready <- refreshDelivery{snapshot: snapshot, buildTime: time.Since(buildStarted), generation: generation}:
									case <-stop:
										return
									}
								}
							}()
							t.Cleanup(func() { close(stop); <-done })
							// Prime one real delivery before sampling. The builder starts
							// its next generation concurrently; setup wait is not latency.
							firstDelivery = <-ready
						}
						listIDs := performanceListIDs(m)
						var before, after runtime.MemStats
						runtime.ReadMemStats(&before)
						durations := make([]time.Duration, 0, samples)
						selected := make([]string, 0, samples)
						var swaps []time.Duration
						var phase2Handlers []time.Duration
						var buildTimes, commandTimes []time.Duration
						var refreshStatuses []json.RawMessage
						var refreshOrders []string
						var refreshDecisions []string
						var refreshGenerations []int
						type phase2Result struct {
							messages []tea.Msg
							err      error
							elapsed  time.Duration
						}
						phase2Ready := make(chan phase2Result, 1)
						var pendingSnapshot *DataSnapshot
						var pendingGeneration int
						for i := 0; i < samples; i++ {
							var swapTime time.Duration
							handledPhase2 := false
							if pendingSnapshot != nil {
								var result phase2Result
								completed := false
								if i == samples-1 {
									// Finish the last delivered generation before the final
									// sample. Waiting off-loop is not handler latency.
									result, completed = <-phase2Ready, true
								} else {
									select {
									case result = <-phase2Ready:
										completed = true
									default:
									}
								}
								if completed {
									if result.err != nil {
										t.Fatal(result.err)
									}
									started := time.Now()
									phase2Count := 0
									var installed *DataSnapshot
									for _, msg := range result.messages {
										if completion, ok := msg.(Phase2ReadyMsg); ok {
											phase2Count++
											installed = completion.prepared
										}
										m.Update(msg)
									}
									swapTime = time.Since(started)
									if phase2Count != 1 {
										t.Fatalf("snapshot command returned %d Phase2 completions, want 1", phase2Count)
									}
									// A historical binary can do preparation in Update and emit
									// a raw Phase2ReadyMsg. It must still install a new completed
									// snapshot for this actual source, rather than merely emit
									// the message or retain its Phase1 snapshot.
									if m.snapshot == nil || m.snapshot == pendingSnapshot || !m.snapshot.IsPhase2Ready() ||
										m.analysis != pendingSnapshot.Analysis || m.snapshot.Analysis != pendingSnapshot.Analysis ||
										(installed != nil && m.snapshot != installed) {
										t.Fatal("actual Phase2 command did not install the delivered source generation")
									}
									changedID := issues[len(issues)-1].ID
									if current := m.snapshot.IssueMap[changedID]; current == nil || current.Title != pendingSnapshot.IssueMap[changedID].Title {
										t.Fatal("completed Phase2 snapshot lost the actual generation's changed source title")
									}
									phase2Handlers = append(phase2Handlers, swapTime)
									commandTimes = append(commandTimes, result.elapsed)
									stateJSON, err := json.Marshal(m.analysis.Status())
									if err != nil {
										t.Fatal(err)
									}
									state, err := testutil.PerformanceMetricStates(stateJSON)
									if err != nil {
										t.Fatal(err)
									}
									refreshStatuses = append(refreshStatuses, state)
									if analysis.ComputeConfigHash(&m.analysis.Config) != configHash {
										t.Fatal("refresh changed the effective analysis configuration")
									}
									orderJSON, err := json.Marshal(performanceListIDs(m))
									if err != nil {
										t.Fatal(err)
									}
									refreshOrders = append(refreshOrders, fmt.Sprintf("%x", sha256.Sum256(orderJSON)))
									decisionJSON, err := json.Marshal(map[string]any{"priorities": m.priorityHints, "triage_scores": m.snapshot.TriageScores, "unblocks": m.snapshot.UnblocksMap})
									if err != nil {
										t.Fatal(err)
									}
									refreshDecisions = append(refreshDecisions, fmt.Sprintf("%x", sha256.Sum256(decisionJSON)))
									refreshGenerations = append(refreshGenerations, pendingGeneration)
									pendingSnapshot = nil
									handledPhase2 = true
								}
							}
							var delivery refreshDelivery
							if i == 0 {
								delivery = firstDelivery
							} else if pendingSnapshot == nil && !handledPhase2 && i < samples-1 {
								select {
								case delivery = <-ready:
								default:
								}
							}
							if snapshot := delivery.snapshot; snapshot != nil {
								started := time.Now()
								_, phase2Cmd := m.Update(SnapshotReadyMsg{Snapshot: snapshot})
								swapTime = time.Since(started)
								swaps = append(swaps, swapTime)
								buildTimes = append(buildTimes, delivery.buildTime)
								pendingSnapshot = snapshot
								pendingGeneration = delivery.generation
								go func() {
									started := time.Now()
									messages, err := performancePhase2Command(phase2Cmd)
									phase2Ready <- phase2Result{messages: messages, err: err, elapsed: time.Since(started)}
								}()
							}
							elapsed, err := performanceNavigationStep(m, i, 0, true)
							if err != nil {
								t.Fatal(err)
							}
							durations = append(durations, elapsed+swapTime)
							selected = append(selected, m.selectedListIssueID(false, ""))
						}
						runtime.ReadMemStats(&after)
						summary, err := testutil.SummarizeLatency(durations)
						if err != nil {
							t.Fatal(err)
						}
						record := map[string]any{
							"workload": kind, "issues": size, "seed": 20260904, "fixture_sha256": fmt.Sprintf("%x", sha256.Sum256(fixture)),
							"loaded_issues": len(m.issues), "host": host, "binary_sha256": executableHash,
							"analysis_config_hash": configHash,
							"mode":                 mode, "terminal_columns": 140, "terminal_rows": 45, "distribution": summary,
							"settled_setup_ns": setupElapsed, "priority_recommendations": priorityHints,
							"priority_reference_time": "2026-09-01T00:00:00Z",
							"sample_ns":               durations, "snapshot_swap_ns": swaps, "phase2_handler_ns": phase2Handlers,
							"snapshot_build_ns": buildTimes, "phase2_command_ns": commandTimes,
							"selected_ids": selected, "list_ids": listIDs, "refresh_order_sha256": refreshOrders,
							"refresh_decisions_sha256": refreshDecisions,
							"refresh_generations":      refreshGenerations,
							"metric_status":            status, "refresh_metric_status": refreshStatuses,
							"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "allocations": after.Mallocs - before.Mallocs,
							"heap_before_bytes": before.HeapAlloc, "heap_after_bytes": after.HeapAlloc,
							"gc_cycles": after.NumGC - before.NumGC, "gc_pause_ns": after.PauseTotalNs - before.PauseTotalNs,
							"go_version": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "gomaxprocs": runtime.GOMAXPROCS(0),
							"interpretation": "empirical Update+View latency; no terminal paint or population p99 guarantee",
						}
						data, err := json.MarshalIndent(record, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(outDir, fmt.Sprintf("ui-%s-%d-%s.json", kind, size, mode))
						if err := os.WriteFile(path, data, 0o600); err != nil {
							t.Fatal(err)
						}
						t.Logf("%s samples=%d p50=%.3fms p95=%.3fms p99=%.3fms swaps=%d", path, samples, summary.P50MS, summary.P95MS, summary.P99MS, len(swaps))
						if refresh && len(swaps) == 0 {
							t.Error("no concurrent snapshot reached the UI; refresh cohort is not valid")
						}
						if os.Getenv("BV_PERF_ENFORCE_SLO") == "1" {
							if err := testutil.CheckInteractionLatency(durations); err != nil {
								t.Error(err)
							}
							for _, elapsed := range swaps {
								if elapsed > 50*time.Millisecond {
									t.Errorf("snapshot delivery blocks event loop for %s >50ms; sparse deliveries cannot be hidden below the p99 rank", elapsed)
								}
							}
							for _, elapsed := range phase2Handlers {
								if elapsed > 50*time.Millisecond {
									t.Errorf("Phase2Ready handler blocks event loop for %s >50ms", elapsed)
								}
							}
						}
					})
				}
			})
		}
	}
}

func BenchmarkSnapshotBuilderBuild(b *testing.B) {
	cfg := analysis.AnalysisConfig{}

	for _, size := range []int{100, 500, 1000, 5000} {
		b.Run(fmt.Sprintf("issues=%d", size), func(b *testing.B) {
			base := testutil.QuickRandom(size, 0.01)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				issues := copyIssues(base)
				b.StartTimer()

				builder := NewSnapshotBuilder(issues)
				stats := builder.analyzer.AnalyzeWithConfig(cfg)
				builder.WithAnalysis(&stats)

				snap := builder.Build()
				if snap == nil {
					b.Fatalf("unexpected snapshot: nil")
				}
				if len(snap.Issues) != len(base) {
					b.Fatalf("unexpected snapshot issue count: got=%d want=%d", len(snap.Issues), len(base))
				}
			}
		})
	}
}

func BenchmarkListItemBuild(b *testing.B) {
	issues := testutil.QuickRandom(1000, 0.01)
	prev := NewSnapshotBuilder(copyIssues(issues)).Build()
	updated := copyIssues(prev.Issues)
	updated[len(updated)/2].Title += " updated"
	diff := analysis.ComputeIssueDiff(prev.Issues, updated)

	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			items := buildListItems(updated, nil)
			if len(items) != len(updated) {
				b.Fatal("incomplete full list build")
			}
		}
	})
	b.Run("one-of-1000-incremental", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			items := buildListItemsIncremental(updated, nil, prev, &diff)
			if len(items) != len(updated) {
				b.Fatal("incomplete incremental list build")
			}
		}
	})
}

func BenchmarkSnapshotSearchDocuments(b *testing.B) {
	issues, err := testutil.PerformanceIssues("unicode", 10000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	items := buildListItems(issues, nil)
	_, previousDocs := buildSnapshotSearchDocuments(items, nil, nil)
	previous := &DataSnapshot{semanticDocs: previousDocs, IssueMap: make(map[string]*model.Issue, len(issues))}
	for i := range issues {
		previous.IssueMap[issues[i].ID] = &issues[i]
	}
	changed := copyIssues(issues)
	changed[len(changed)-1].Title += " changed"
	diff := analysis.ComputeIssueDiff(issues, changed)
	items = buildListItems(changed, nil)
	wantIDs, wantDocs := buildSnapshotSearchDocuments(items, nil, nil)
	for _, incremental := range []bool{false, true} {
		name := "full"
		var prev *DataSnapshot
		var changes *analysis.IssueDiff
		if incremental {
			name, prev, changes = "one-of-10000-changed", previous, &diff
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var ids []string
			var docs map[string]string
			for i := 0; i < b.N; i++ {
				ids, docs = buildSnapshotSearchDocuments(items, prev, changes)
			}
			b.StopTimer()
			if !reflect.DeepEqual(ids, wantIDs) || !reflect.DeepEqual(docs, wantDocs) {
				b.Fatal("search documents differ from the complete current-source rebuild")
			}
		})
	}
}

func BenchmarkBackgroundWorkerBuildSnapshot(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("issues=%d", size), func(b *testing.B) {
			issues := testutil.QuickRandom(size, 0.01)
			beadsPath := writeBenchmarkIssues(b, issues)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				worker, err := NewBackgroundWorker(WorkerConfig{
					BeadsPath: beadsPath,
					IdleGC:    &IdleGCConfig{Enabled: false},
				})
				if err != nil {
					b.Fatalf("new background worker: %v", err)
				}
				b.StartTimer()

				snapshot := worker.buildSnapshot(true)

				b.StopTimer()
				if snapshot == nil {
					b.Fatal("buildSnapshot returned nil")
				}
				if got := len(snapshot.Issues); got != size {
					b.Fatalf("snapshot issue count=%d, want %d", got, size)
				}
				if snapshot.Analysis != nil {
					snapshot.Analysis.WaitForPhase2()
				}
				worker.cancel()
				snapshot.releasePooledIssues()
				b.StartTimer()
			}
		})
	}
}

func writeBenchmarkIssues(b *testing.B, issues []model.Issue) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "issues.jsonl")
	file, err := os.Create(path)
	if err != nil {
		b.Fatalf("create benchmark issues: %v", err)
	}
	w := bufio.NewWriter(file)
	for i := range issues {
		line, err := json.Marshal(issues[i])
		if err != nil {
			_ = file.Close()
			b.Fatalf("marshal benchmark issue: %v", err)
		}
		if _, err := w.Write(line); err != nil {
			_ = file.Close()
			b.Fatalf("write benchmark issue: %v", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			_ = file.Close()
			b.Fatalf("terminate benchmark issue: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = file.Close()
		b.Fatalf("flush benchmark issues: %v", err)
	}
	if err := file.Close(); err != nil {
		b.Fatalf("close benchmark issues: %v", err)
	}
	return path
}
