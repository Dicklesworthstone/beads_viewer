package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func writeTempBeadsFile(t *testing.T, dir string, issue model.Issue) string {
	t.Helper()

	created := time.Now().UTC().Format(time.RFC3339)
	line := fmt.Sprintf(
		`{"id":"%s","title":"%s","status":"%s","priority":%d,"issue_type":"%s","created_at":"%s","updated_at":"%s"}`,
		issue.ID,
		issue.Title,
		issue.Status,
		issue.Priority,
		issue.IssueType,
		created,
		created,
	)
	path := filepath.Join(dir, "beads.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return path
}

func TestFileChangedPreservesTriageData(t *testing.T) {
	issue := model.Issue{
		ID:        "A-1",
		Title:     "Test Issue",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
	}
	beadsPath := writeTempBeadsFile(t, t.TempDir(), issue)

	m := NewModel([]model.Issue{issue}, nil, beadsPath)
	t.Cleanup(m.Stop)

	m.triageScores = map[string]float64{issue.ID: 0.9}
	m.triageReasons = map[string]analysis.TriageReasons{
		issue.ID: {Primary: "Keep", All: []string{"Keep"}},
	}
	m.quickWinSet = map[string]bool{issue.ID: true}
	m.blockerSet = map[string]bool{issue.ID: true}
	m.unblocksMap = map[string][]string{issue.ID: {"B-2"}}

	rec := analysis.Recommendation{ID: issue.ID, Title: issue.Title, Score: 0.9}
	m.insightsPanel.topPicks = []analysis.TopPick{{ID: issue.ID, Title: issue.Title, Score: 0.9}}
	m.insightsPanel.recommendations = []analysis.Recommendation{rec}
	m.insightsPanel.recommendationMap = map[string]*analysis.Recommendation{issue.ID: &rec}
	m.insightsPanel.triageDataHash = "keep"

	next, _ := m.Update(FileChangedMsg{})
	updated := next.(*Model)

	items := updated.list.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item after reload, got %d", len(items))
	}
	item := items[0].(IssueItem)
	if item.TriageScore != 0.9 {
		t.Errorf("triage score not preserved: got %v, want 0.9", item.TriageScore)
	}
	if item.TriageReason != "Keep" {
		t.Errorf("triage reason not preserved: got %q", item.TriageReason)
	}
	if !item.IsQuickWin {
		t.Error("quick win flag not preserved")
	}
	if !item.IsBlocker {
		t.Error("blocker flag not preserved")
	}
	if item.UnblocksCount != 1 {
		t.Errorf("unblocks count not preserved: got %d, want 1", item.UnblocksCount)
	}

	if len(updated.insightsPanel.topPicks) != 1 || updated.insightsPanel.topPicks[0].ID != issue.ID {
		t.Errorf("top picks not preserved: got %+v", updated.insightsPanel.topPicks)
	}
	if len(updated.insightsPanel.recommendations) != 1 || updated.insightsPanel.recommendations[0].ID != issue.ID {
		t.Errorf("recommendations not preserved: got %+v", updated.insightsPanel.recommendations)
	}
	if updated.insightsPanel.triageDataHash != "keep" {
		t.Errorf("triage hash not preserved: got %q", updated.insightsPanel.triageDataHash)
	}
}

func TestDataSnapshotPreservesTriageWhenPhase1(t *testing.T) {
	issue := model.Issue{
		ID:        "A-2",
		Title:     "Snapshot Issue",
		Status:    model.StatusOpen,
		Priority:  2,
		IssueType: model.TypeTask,
	}
	m := NewModel([]model.Issue{issue}, nil, "")

	m.triageScores = map[string]float64{issue.ID: 0.75}
	m.triageReasons = map[string]analysis.TriageReasons{
		issue.ID: {Primary: "Stay", All: []string{"Stay"}},
	}
	m.quickWinSet = map[string]bool{issue.ID: true}

	var listItems []IssueItem
	for _, it := range m.list.Items() {
		listItems = append(listItems, it.(IssueItem))
	}

	snapshot := &DataSnapshot{
		Issues:       m.issues,
		IssueMap:     m.issueMap,
		Analyzer:     m.analyzer,
		Analysis:     m.analysis,
		insights:     m.analysis.GenerateInsights(len(m.issues)),
		CountOpen:    m.countOpen,
		CountReady:   m.countReady,
		CountBlocked: m.countBlocked,
		CountClosed:  m.countClosed,
		ListItems:    listItems,
		// Phase 1 snapshot: no triage data yet.
		TriageScores:  map[string]float64{},
		TriageReasons: map[string]analysis.TriageReasons{},
		QuickWinSet:   map[string]bool{},
		BlockerSet:    map[string]bool{},
		UnblocksMap:   map[string][]string{},
		phase2Ready:   false,
	}

	nextModel, _ := m.Update(SnapshotReadyMsg{Snapshot: snapshot})
	updated := nextModel.(*Model)

	if updated.triageScores[issue.ID] != 0.75 {
		t.Errorf("triage score should be preserved: got %v, want 0.75", updated.triageScores[issue.ID])
	}
	if updated.triageReasons[issue.ID].Primary != "Stay" {
		t.Errorf("triage reason should be preserved: got %q", updated.triageReasons[issue.ID].Primary)
	}
	if !updated.quickWinSet[issue.ID] {
		t.Error("quick win flag should be preserved")
	}
}

// TestReloadKeepsPriorityHintUntilPhase2 guards #217. A reload delivers Phase 1
// data first; until Phase 2 recomputes the priority hints, the detail pane must
// keep showing a hint that is still valid instead of dropping the line and
// shifting the rest of the pane on every reload. Both reload paths are covered:
// the background worker's snapshot and the synchronous file reload (default).
func TestReloadKeepsPriorityHintUntilPhase2(t *testing.T) {
	issues := []model.Issue{
		{ID: "H-1", Title: "Hinted task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "H-2", Title: "Reprioritized task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "H-3", Title: "Closing task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "H-4", Title: "Removed task", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "H-5", Title: "Unrelated task", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}
	// Another writer edits other issues; the reload arrives before Phase 2.
	next := copyIssues(issues)
	next[1].Priority = 1                // H-2: hint was computed for P2, now stale
	next[2].Status = model.StatusClosed // H-3: closed issues get no hint
	next[4].Title = "Unrelated task (edited)"
	next = append(next[:3], next[4]) // H-4 removed

	writeJSONL := func(t *testing.T, path string, list []model.Issue) {
		t.Helper()
		var data []byte
		for _, issue := range list {
			line, err := json.Marshal(issue)
			if err != nil {
				t.Fatal(err)
			}
			data = append(append(data, line...), '\n')
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T) *Model
		reload func(t *testing.T, m *Model)
	}{
		{
			name:  "background_snapshot",
			setup: func(t *testing.T) *Model { return NewModel(copyIssues(issues), nil, "") },
			reload: func(t *testing.T, m *Model) {
				m.Update(SnapshotReadyMsg{Snapshot: NewSnapshotBuilder(copyIssues(next)).Build(), SnapshotVer: 1})
			},
		},
		{
			name: "file_reload",
			setup: func(t *testing.T) *Model {
				t.Setenv("BV_BACKGROUND_MODE", "0")
				dir := t.TempDir()
				t.Chdir(dir)
				path := filepath.Join(dir, "issues.jsonl")
				writeJSONL(t, path, issues)
				return NewModel(copyIssues(issues), nil, path)
			},
			reload: func(t *testing.T, m *Model) {
				writeJSONL(t, m.beadsPath, next)
				m.Update(FileChangedMsg{})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			t.Cleanup(m.Stop)
			m.analysis.WaitForPhase2()
			m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})

			hint := func(id string) *analysis.PriorityRecommendation {
				return &analysis.PriorityRecommendation{
					IssueID: id, CurrentPriority: 2, SuggestedPriority: 3, Direction: "decrease",
					Confidence: 0.38, Reasoning: []string{"Parallel-friendly (slack available)"},
				}
			}
			injected := hint("H-1")
			m.priorityHints = map[string]*analysis.PriorityRecommendation{
				"H-1": injected, "H-2": hint("H-2"), "H-3": hint("H-3"), "H-4": hint("H-4"),
			}
			m.updateListDelegate()
			m.insightsPanel.SetPriorityHints(m.priorityHints)
			if !m.selectListIssueByID("H-1") {
				t.Fatal("could not select H-1")
			}
			m.updateViewportContent()
			if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "Priority Hint:") {
				t.Fatalf("setup: detail pane has no Priority Hint before the reload:\n%s", view)
			}

			tc.reload(t, m)

			if m.issueMap["H-5"] == nil || m.issueMap["H-5"].Title != "Unrelated task (edited)" {
				t.Fatalf("reload was not applied: %+v", m.issueMap["H-5"])
			}
			if sel := m.list.SelectedItem(); sel == nil || sel.(IssueItem).Issue.ID != "H-1" {
				t.Fatalf("reload lost the H-1 selection: %+v", sel)
			}
			if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "Priority Hint:") || !strings.Contains(view, "P2 → P3") {
				t.Fatalf("Priority Hint vanished from the detail pane on reload (#217):\n%s", view)
			}
			// The insights panel keeps the map it was given until its next
			// refresh; it must see the same pruned hints.
			for name, hints := range map[string]map[string]*analysis.PriorityRecommendation{
				"model": m.priorityHints, "insights panel": m.insightsPanel.priorityHints,
			} {
				if hints["H-1"] != injected {
					t.Fatalf("%s: still-valid hint for H-1 was dropped: %+v", name, hints)
				}
				for _, id := range []string{"H-2", "H-3", "H-4"} {
					if h := hints[id]; h != nil {
						t.Fatalf("%s: stale hint for %s survived the reload: %+v", name, id, h)
					}
				}
			}

			// Phase 2 still owns the hints: its result replaces the retained map.
			phase2, ok := m.preparePhase2Cmd()().(Phase2ReadyMsg)
			if !ok {
				t.Fatal("Phase 2 preparation returned no Phase2ReadyMsg")
			}
			m.Update(phase2)
			if m.priorityHints["H-1"] == injected {
				t.Fatal("Phase 2 did not replace the hints retained across the reload")
			}
		})
	}
}
