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

func TestCompareSnapshots_TombstoneCountsAsClosed(t *testing.T) {
	fromIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusOpen},
	}
	toIssues := []model.Issue{
		{ID: "ISSUE-1", Title: "First", Status: model.StatusTombstone},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if len(diff.ClosedIssues) != 1 {
		t.Fatalf("expected 1 closed issue (tombstone), got %d", len(diff.ClosedIssues))
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
	// Key now includes type (which is empty string in this test case)
	if !set["A:"] {
		t.Fatalf("expected dependency 'A:' to be present")
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
	closed1 := []string{"A", "B", "C", "A"}
	closed2 := []string{"B", "C", "A", "B"}
	closed3 := []string{"C", "A", "B", "C"}

	norm1 := normalizeCycle(cycle1)
	norm2 := normalizeCycle(cycle2)
	norm3 := normalizeCycle(cycle3)

	if norm1 != norm2 || norm2 != norm3 {
		t.Errorf("cycle normalization failed: %s, %s, %s", norm1, norm2, norm3)
	}
	if normalizeCycle(closed1) != norm1 || normalizeCycle(closed2) != norm1 || normalizeCycle(closed3) != norm1 {
		t.Errorf("closed cycle normalization failed: %q vs %q vs %q (want %q)",
			normalizeCycle(closed1), normalizeCycle(closed2), normalizeCycle(closed3), norm1)
	}
}

func TestMetricDeltas_BlockedEdgesAndComponents(t *testing.T) {
	// From: 3 isolated open issues (0 edges, 3 components, 0 blocked)
	fromIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}
	// To: B depends on A (open), C is explicitly StatusBlocked and depends on A (2 edges, 1 component, 2 blocked without double-counting C)
	toIssues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusBlocked, Dependencies: []*model.Dependency{{IssueID: "C", DependsOnID: "A", Type: model.DepBlocks}}},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	if to.BlockedCount != 2 {
		t.Fatalf("expected to.BlockedCount == 2 (B dependency-blocked + C status-blocked without double counting), got %d", to.BlockedCount)
	}
	if diff.MetricDeltas.BlockedIssues != 2 {
		t.Fatalf("expected BlockedIssues delta 2, got %d", diff.MetricDeltas.BlockedIssues)
	}
	if diff.MetricDeltas.TotalEdges != 2 {
		t.Fatalf("expected TotalEdges delta 2, got %d", diff.MetricDeltas.TotalEdges)
	}
	if diff.MetricDeltas.ComponentCount != -2 {
		t.Fatalf("expected ComponentCount delta -2 (3 -> 1), got %d", diff.MetricDeltas.ComponentCount)
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

func TestCompareSnapshots_DependencyTypeChange(t *testing.T) {
	// Issue A relates to B
	fromIssues := []model.Issue{
		{
			ID:     "A",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "B", Type: model.DepRelated},
			},
		},
		{ID: "B", Status: model.StatusOpen},
	}

	// Issue A now BLOCKS B (Type change)
	toIssues := []model.Issue{
		{
			ID:     "A",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "B", Type: model.DepBlocks},
			},
		},
		{ID: "B", Status: model.StatusOpen},
	}

	from := NewSnapshot(fromIssues)
	to := NewSnapshot(toIssues)
	diff := CompareSnapshots(from, to)

	// We expect this to be detected as a modified issue
	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("expected 1 modified issue (dependency type change), got %d", len(diff.ModifiedIssues))
	}

	mod := diff.ModifiedIssues[0]
	foundDepChange := false
	for _, c := range mod.Changes {
		if c.Field == "dependencies" {
			foundDepChange = true
			break
		}
	}

	if !foundDepChange {
		t.Error("expected dependency change to be detected when Type changes from related to blocks")
	}
}

func TestCompareSnapshots_FiveNewIssueFields(t *testing.T) {
	fromIssues := []model.Issue{
		{
			ID:          "ISS-5",
			Title:       "Template Seed",
			Status:      model.StatusOpen,
			IssueType:   model.TypeTask,
			CreatedBy:   "alice",
			CloseReason: "",
			Ephemeral:   false,
			Pinned:      false,
			IsTemplate:  false,
		},
	}
	toIssues := []model.Issue{
		{
			ID:          "ISS-5",
			Title:       "Template Seed",
			Status:      model.StatusOpen,
			IssueType:   model.TypeTask,
			CreatedBy:   "bob",
			CloseReason: "wontfix",
			Ephemeral:   true,
			Pinned:      true,
			IsTemplate:  true,
		},
	}

	diff := CompareSnapshots(NewSnapshot(fromIssues), NewSnapshot(toIssues))
	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("expected 1 modified issue, got %d", len(diff.ModifiedIssues))
	}

	gotFields := make(map[string]FieldChange, len(diff.ModifiedIssues[0].Changes))
	for _, ch := range diff.ModifiedIssues[0].Changes {
		gotFields[ch.Field] = ch
	}

	for _, wantField := range []string{"created_by", "close_reason", "ephemeral", "pinned", "is_template"} {
		if _, ok := gotFields[wantField]; !ok {
			t.Errorf("missing %q in ModifiedIssues[0].Changes: %+v", wantField, diff.ModifiedIssues[0].Changes)
		}
	}
}

func TestNewSnapshot_ActionableAndBlockedCountsWithWithheldBeads(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	future := now.Add(7 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "READY-OPEN", Status: model.StatusOpen},
		{ID: "READY-IP", Status: model.StatusInProgress},
		{
			ID:     "DEP-BLOCKED",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "DEP-BLOCKED", DependsOnID: "READY-OPEN", Type: model.DepBlocks},
			},
		},
		{ID: "STATUS-BLOCKED", Status: model.StatusBlocked},
		{ID: "PIN-OPEN", Status: model.StatusOpen, Pinned: true},
		{
			ID:     "PIN-WITH-DEP",
			Status: model.StatusOpen,
			Pinned: true,
			Dependencies: []*model.Dependency{
				{IssueID: "PIN-WITH-DEP", DependsOnID: "READY-OPEN", Type: model.DepBlocks},
			},
		},
		{ID: "TMPL-OPEN", Status: model.StatusOpen, IsTemplate: true},
		{ID: "EPH-OPEN", Status: model.StatusOpen, Ephemeral: true},
		{ID: "FUTURE-DEF", Status: model.StatusOpen, DeferUntil: &future},
		{ID: "CLOSED-1", Status: model.StatusClosed},
		{ID: "TOMB-1", Status: model.StatusTombstone},
	}

	snap := NewSnapshotAt(issues, now, "HEAD")
	an := NewAnalyzer(issues)
	an.SetNow(now)
	partition := an.CountStatusPartition()

	if snap.ActionableCount != partition.Actionable || snap.ActionableCount != 2 {
		t.Fatalf("Snapshot.ActionableCount = %d, want %d (CountStatusPartition.Actionable=%d)",
			snap.ActionableCount, 2, partition.Actionable)
	}
	if snap.BlockedCount != partition.Blocked || snap.BlockedCount != 2 {
		t.Fatalf("Snapshot.BlockedCount = %d, want %d (CountStatusPartition.Blocked=%d)",
			snap.BlockedCount, 2, partition.Blocked)
	}
	if snap.OpenCount != partition.NotClosed {
		t.Fatalf("Snapshot.OpenCount = %d, want %d", snap.OpenCount, partition.NotClosed)
	}
	if snap.ClosedCount != partition.Closed {
		t.Fatalf("Snapshot.ClosedCount = %d, want %d", snap.ClosedCount, partition.Closed)
	}
}

func TestSnapshot_ComputeScopedCounts(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)

	issues := []model.Issue{
		{ID: "SCOPED-OPEN", Status: model.StatusOpen},
		{ID: "SCOPED-IP", Status: model.StatusInProgress},
		{
			ID:     "SCOPED-DEP-BLOCKED",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "SCOPED-DEP-BLOCKED", DependsOnID: "OUTSIDE-BLOCKER", Type: model.DepBlocks},
			},
		},
		{ID: "SCOPED-STATUS-BLOCKED", Status: model.StatusBlocked},
		{ID: "SCOPED-PARKED-DEFERRED", Status: model.StatusDeferred},
		{ID: "SCOPED-PARKED-DRAFT", Status: model.StatusDraft},
		{ID: "SCOPED-PARKED-PINNED", Status: model.StatusPinned},
		{ID: "SCOPED-PARKED-HOOKED", Status: model.StatusHooked},
		{ID: "SCOPED-PARKED-REVIEW", Status: model.StatusReview},
		{
			ID:     "SCOPED-WITHHELD-PIN",
			Status: model.StatusOpen,
			Pinned: true,
			Dependencies: []*model.Dependency{
				{IssueID: "SCOPED-WITHHELD-PIN", DependsOnID: "OUTSIDE-BLOCKER", Type: model.DepBlocks},
			},
		},
		{ID: "SCOPED-WITHHELD-TMPL", Status: model.StatusOpen, IsTemplate: true},
		{ID: "SCOPED-WITHHELD-EPH", Status: model.StatusOpen, Ephemeral: true},
		{
			ID:         "SCOPED-FUTURE-DEF",
			Status:     model.StatusOpen,
			DeferUntil: &future,
			Dependencies: []*model.Dependency{
				{IssueID: "SCOPED-FUTURE-DEF", DependsOnID: "OUTSIDE-BLOCKER", Type: model.DepBlocks},
			},
		},
		{ID: "SCOPED-CLOSED", Status: model.StatusClosed},
		{ID: "SCOPED-TOMBSTONE", Status: model.StatusTombstone},
		{ID: "OUTSIDE-BLOCKER", Status: model.StatusOpen},
		{ID: "OUTSIDE-CLOSED", Status: model.StatusClosed},
	}

	fullSnap := NewSnapshotAt(issues, now, "HEAD")
	readiness := model.NewReadinessIndex(issues)

	// 1. Nil predicate matches NewSnapshotAt identically across all 5 counts.
	allScoped := &Snapshot{Issues: issues, Timestamp: now}
	allScoped.ComputeScopedCounts(readiness, now, nil)
	if allScoped.TotalCount != fullSnap.TotalCount ||
		allScoped.OpenCount != fullSnap.OpenCount ||
		allScoped.ClosedCount != fullSnap.ClosedCount ||
		allScoped.BlockedCount != fullSnap.BlockedCount ||
		allScoped.ActionableCount != fullSnap.ActionableCount {
		t.Fatalf("ComputeScopedCounts(..., nil) = (%d,%d,%d,%d,%d), want NewSnapshotAt (%d,%d,%d,%d,%d)",
			allScoped.TotalCount, allScoped.OpenCount, allScoped.ClosedCount, allScoped.BlockedCount, allScoped.ActionableCount,
			fullSnap.TotalCount, fullSnap.OpenCount, fullSnap.ClosedCount, fullSnap.BlockedCount, fullSnap.ActionableCount)
	}

	// 2. Scoped predicate counts only SCOPED-* issues while honoring cross-scope blockers
	// and excluding parked, future-deferred, and withheld beads from BlockedCount & ActionableCount.
	subSnap := &Snapshot{Issues: issues, Timestamp: now}
	subSnap.ComputeScopedCounts(readiness, now, func(id string) bool {
		return len(id) >= 7 && id[:7] == "SCOPED-"
	})
	if subSnap.TotalCount != 15 {
		t.Fatalf("scoped TotalCount = %d, want 15", subSnap.TotalCount)
	}
	if subSnap.ClosedCount != 2 {
		t.Fatalf("scoped ClosedCount = %d, want 2 (closed + tombstone)", subSnap.ClosedCount)
	}
	if subSnap.OpenCount != 13 {
		t.Fatalf("scoped OpenCount = %d, want 13", subSnap.OpenCount)
	}
	if subSnap.ActionableCount != 2 {
		t.Fatalf("scoped ActionableCount = %d, want 2 (SCOPED-OPEN + SCOPED-IP)", subSnap.ActionableCount)
	}
	if subSnap.BlockedCount != 2 {
		t.Fatalf("scoped BlockedCount = %d, want 2 (SCOPED-DEP-BLOCKED + SCOPED-STATUS-BLOCKED; parked/deferred/withheld excluded)", subSnap.BlockedCount)
	}
}

func TestDiffDetectsScheduleAndEstimateMutations(t *testing.T) {
	t1 := time.Date(2026, 11, 20, 15, 30, 0, 123456789, time.UTC)
	t2 := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	ist := time.FixedZone("IST", 5*3600+1800)
	t1IST := t1.In(ist)
	zeroTime := time.Time{}

	est45 := 45
	est90 := 90
	est60A := 60
	est60B := 60
	refGH := "GH-42"
	refJira := "JIRA-99"
	emptyRef := ""

	from := NewSnapshot([]model.Issue{
		{
			ID:               "MUTATED-1",
			Title:            "Schedule & estimate tracked",
			Status:           model.StatusOpen,
			Priority:         2,
			DeferUntil:       nil,
			DueDate:          &t1,
			EstimatedMinutes: &est45,
			ExternalRef:      &refGH,
		},
		{
			ID:               "UNCHANGED-CTRL",
			Title:            "Equivalent representations",
			Status:           model.StatusOpen,
			Priority:         2,
			DeferUntil:       &t1,
			DueDate:          nil,
			EstimatedMinutes: &est60A,
			ExternalRef:      nil,
		},
	})

	to := NewSnapshot([]model.Issue{
		{
			ID:               "MUTATED-1",
			Title:            "Schedule & estimate tracked",
			Status:           model.StatusOpen,
			Priority:         2,
			DeferUntil:       &t2,
			DueDate:          nil,
			EstimatedMinutes: &est90,
			ExternalRef:      &refJira,
		},
		{
			ID:               "UNCHANGED-CTRL",
			Title:            "Equivalent representations",
			Status:           model.StatusOpen,
			Priority:         2,
			DeferUntil:       &t1IST,    // same UTC instant in different timezone
			DueDate:          &zeroTime, // zero time equivalent to nil
			EstimatedMinutes: &est60B,   // distinct pointer with identical value
			ExternalRef:      &emptyRef, // empty string equivalent to nil
		},
	})

	diff := CompareSnapshots(from, to)
	if len(diff.ModifiedIssues) != 1 {
		t.Fatalf("ModifiedIssues = %+v, want exactly [MUTATED-1] (UNCHANGED-CTRL must produce 0 diffs)", diff.ModifiedIssues)
	}
	mod := diff.ModifiedIssues[0]
	if mod.IssueID != "MUTATED-1" {
		t.Fatalf("ModifiedIssues[0].IssueID = %q, want MUTATED-1", mod.IssueID)
	}

	byField := make(map[string]FieldChange, len(mod.Changes))
	for _, ch := range mod.Changes {
		byField[ch.Field] = ch
	}

	wantChanges := map[string][2]string{
		"defer_until":       {"", t2.UTC().Format(time.RFC3339Nano)},
		"due_date":          {t1.UTC().Format(time.RFC3339Nano), ""},
		"estimated_minutes": {"45", "90"},
		"external_ref":      {"GH-42", "JIRA-99"},
	}
	if len(byField) != len(wantChanges) {
		t.Fatalf("Changes = %+v, want %d fields", mod.Changes, len(wantChanges))
	}
	for field, want := range wantChanges {
		got, ok := byField[field]
		if !ok {
			t.Fatalf("missing FieldChange for %q in %+v", field, mod.Changes)
		}
		if got.OldValue != want[0] || got.NewValue != want[1] {
			t.Fatalf("FieldChange[%s] = (%q -> %q), want (%q -> %q)", field, got.OldValue, got.NewValue, want[0], want[1])
		}
	}
}
