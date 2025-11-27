package analysis

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestNewSnapshot(t *testing.T) {
	issues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen, Priority: 1},
		{ID: "ISSUE-2", Title: "Second", Status: model.StatusClosed, Priority: 2},
	}

	snap := NewSnapshot(issues)

	if snap.TotalCount != 2 {
		t.Errorf("expected TotalCount 2, got %d", snap.TotalCount)
	}
	if snap.OpenCount != 1 {
		t.Errorf("expected OpenCount 1, got %d", snap.OpenCount)
	}
	if snap.ClosedCount != 1 {
		t.Errorf("expected ClosedCount 1, got %d", snap.ClosedCount)
	}
	if snap.Stats == nil {
		t.Error("expected Stats to be set")
	}
}

func TestNewSnapshotAt(t *testing.T) {
	issues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}
	ts := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	snap := NewSnapshotAt(issues, ts, "abc123")

	if snap.Revision != "abc123" {
		t.Errorf("expected revision abc123, got %s", snap.Revision)
	}
	if !snap.Timestamp.Equal(ts) {
		t.Errorf("expected timestamp %v, got %v", ts, snap.Timestamp)
	}
}

func TestCompareSnapshots_NewIssues(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
		{ID: "ISSUE-2", Title: "Second", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.NewIssues) != 1 {
		t.Fatalf("expected 1 new issue, got %d", len(diff.NewIssues))
	}
	if diff.NewIssues[0].ID != "ISSUE-2" {
		t.Errorf("expected new issue ISSUE-2, got %s", diff.NewIssues[0].ID)
	}
	if diff.Summary.IssuesAdded != 1 {
		t.Errorf("expected IssuesAdded 1, got %d", diff.Summary.IssuesAdded)
	}
}

func TestCompareSnapshots_RemovedIssues(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
		{ID: "ISSUE-2", Title: "Second", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.RemovedIssues) != 1 {
		t.Fatalf("expected 1 removed issue, got %d", len(diff.RemovedIssues))
	}
	if diff.RemovedIssues[0].ID != "ISSUE-2" {
		t.Errorf("expected removed issue ISSUE-2, got %s", diff.RemovedIssues[0].ID)
	}
}

func TestCompareSnapshots_ClosedIssues(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusClosed},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ClosedIssues) != 1 {
		t.Fatalf("expected 1 closed issue, got %d", len(diff.ClosedIssues))
	}
	if diff.Summary.IssuesClosed != 1 {
		t.Errorf("expected IssuesClosed 1, got %d", diff.Summary.IssuesClosed)
	}
}

func TestCompareSnapshots_ReopenedIssues(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusClosed},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ReopenedIssues) != 1 {
		t.Fatalf("expected 1 reopened issue, got %d", len(diff.ReopenedIssues))
	}
}

func TestDependencySetIgnoresNilAndEmpty(t *testing.T) {
	deps := []*model.Dependency{
		nil,
		{DependsOnID: ""},
		{DependsOnID: "A"},
	}

	set := dependencySet(deps)

	if len(set) != 1 {
		t.Fatalf("expected only non-empty dependencies to be counted, got %d", len(set))
	}
	if !set["A"] {
		t.Fatalf("expected dependency 'A' to be present")
	}
}

func TestCompareSnapshots_ModifiedIssues(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "Original Title", Status: model.StatusOpen, Priority: 2},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "Updated Title", Status: model.StatusOpen, Priority: 1},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("expected 1 modified issue, got %d", len(diff.ModifiedIssues))
	}

	mod := diff.ModifiedIssues[0]
	if mod.IssueID != "ISSUE-1" {
		t.Errorf("expected modified ISSUE-1, got %s", mod.IssueID)
	}
	if len(mod.Changes) < 2 {
		t.Fatalf("expected at least 2 changes, got %d", len(mod.Changes))
	}
}

func TestCompareSnapshots_CycleChanges(t *testing.T) {
	// Create issues with a cycle: A -> B -> A
	fromIssues := []model.Issue{
		{ID: "A", Title: "Issue A", Status: model.StatusOpen},
		{ID: "B", Title: "Issue B", Status: model.StatusOpen},
	}
	// Add cycle in to snapshot
	toIssues := []model.Issue{
		{ID: "A", Title: "Issue A", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "A", DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "Issue B", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks}}},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	// New cycle should be detected
	if len(diff.NewCycles) != 1 {
		t.Errorf("expected 1 new cycle, got %d", len(diff.NewCycles))
	}
}

func TestDetectChanges(t *testing.T) {
	from := model.Issue{
		ID:       "TEST-1",
		Title:    "Old Title",
		Status:   model.StatusOpen,
		Priority: 2,
		Labels:   []string{"bug"},
	}
	to := model.Issue{
		ID:       "TEST-1",
		Title:    "New Title",
		Status:   model.StatusInProgress,
		Priority: 1,
		Labels:   []string{"bug", "urgent"},
	}

	changes := detectChanges(from, to)

	// Should detect: title, status, priority, labels
	if len(changes) != 4 {
		t.Errorf("expected 4 changes, got %d: %+v", len(changes), changes)
	}

	// Verify specific changes
	changeMap := make(map[string]FieldChange)
	for _, c := range changes {
		changeMap[c.Field] = c
	}

	if c, ok := changeMap["title"]; !ok || c.OldValue != "Old Title" || c.NewValue != "New Title" {
		t.Error("title change not detected correctly")
	}
	if c, ok := changeMap["priority"]; !ok || c.OldValue != "P2" || c.NewValue != "P1" {
		t.Error("priority change not detected correctly")
	}
}

func TestNormalizeCycle(t *testing.T) {
	// Same cycle in different orders should normalize the same
	cycle1 := []string{"A", "B", "C"}
	cycle2 := []string{"B", "C", "A"}
	cycle3 := []string{"C", "A", "B"}

	norm1 := normalizeCycle(cycle1)
	norm2 := normalizeCycle(cycle2)
	norm3 := normalizeCycle(cycle3)

	if norm1 != norm2 || norm2 != norm3 {
		t.Errorf("cycle normalization failed: %s, %s, %s", norm1, norm2, norm3)
	}
}

func TestMetricDeltas(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Status: model.StatusOpen},
		{ID: "ISSUE-2", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Status: model.StatusClosed},
		{ID: "ISSUE-2", Status: model.StatusOpen},
		{ID: "ISSUE-3", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if diff.MetricDeltas.TotalIssues != 1 {
		t.Errorf("expected TotalIssues delta 1, got %d", diff.MetricDeltas.TotalIssues)
	}
	if diff.MetricDeltas.OpenIssues != 0 {
		t.Errorf("expected OpenIssues delta 0 (2->2), got %d", diff.MetricDeltas.OpenIssues)
	}
	if diff.MetricDeltas.ClosedIssues != 1 {
		t.Errorf("expected ClosedIssues delta 1 (0->1), got %d", diff.MetricDeltas.ClosedIssues)
	}
}

func TestDiffSummary_HealthTrend(t *testing.T) {
	tests := []struct {
		name     string
		from     []model.Issue
		to       []model.Issue
		expected string
	}{
		{
			name: "improving - closing issues",
			from: []model.Issue{
				{ID: "A", Status: model.StatusOpen},
				{ID: "B", Status: model.StatusOpen},
				{ID: "C", Status: model.StatusOpen},
			},
			to: []model.Issue{
				{ID: "A", Status: model.StatusClosed},
				{ID: "B", Status: model.StatusClosed},
				{ID: "C", Status: model.StatusOpen},
			},
			expected: "improving",
		},
		{
			name: "stable - no significant changes",
			from: []model.Issue{
				{ID: "A", Status: model.StatusOpen},
			},
			to: []model.Issue{
				{ID: "A", Status: model.StatusOpen, Title: "Updated"},
			},
			expected: "stable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from := NewSnapshot(tt.from)
			to := NewSnapshot(tt.to)
			diff := CompareSnapshots(from, to)

			if diff.Summary.HealthTrend != tt.expected {
				t.Errorf("expected health trend %q, got %q", tt.expected, diff.Summary.HealthTrend)
			}
		})
	}
}

func TestSnapshotDiff_IsEmpty(t *testing.T) {
	// Identical snapshots
	issues := []model.Issue{
		{ID: "ISSUE-1", Title: "Test", Status: model.StatusOpen},
	}
	from := NewSnapshot(issues)
	to := NewSnapshot(issues)

	diff := CompareSnapshots(from, to)
	if !diff.IsEmpty() {
		t.Error("expected diff to be empty for identical snapshots")
	}
}

func TestSnapshotDiff_HasSignificantChanges(t *testing.T) {
	from := NewSnapshot([]model.Issue{
		{ID: "A", Status: model.StatusOpen},
	})
	to := NewSnapshot([]model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
	})

	diff := CompareSnapshots(from, to)
	if !diff.HasSignificantChanges() {
		t.Error("expected significant changes when new issues added")
	}
}

func TestAvgMapValue(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]float64
		expected float64
	}{
		{"empty", map[string]float64{}, 0},
		{"single", map[string]float64{"a": 10}, 10},
		{"multiple", map[string]float64{"a": 10, "b": 20, "c": 30}, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := avgMapValue(tt.input)
			if result != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, result)
			}
		})
	}
}
