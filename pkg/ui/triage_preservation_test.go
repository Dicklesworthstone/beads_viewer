package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
