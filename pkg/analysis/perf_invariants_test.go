package analysis

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// referenceUnblocksMap is a deterministic, data-only reference implementation of the
// "unblocks" semantics used by triage/execution planning.
//
// Spec (must match production semantics):
// - Only blocking deps count (dep.Type.IsBlocking()).
// - Missing blockers withhold readiness; their completion is not assumed.
// - Duplicate deps must not double-count (graph edges are unique).
// - Closing blocker B unblocks dependent D iff all other blocking deps of D are closed/tombstoned.
// - Only open/in-progress dependents are eligible; parked status is not changed.
// - These fixtures exercise direct dependencies; parent inheritance has separate tests.
// - Output lists are sorted for determinism.
func referenceUnblocksMap(issues []model.Issue) map[string][]string {
	issueByID := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueByID[iss.ID] = iss
	}

	// blockerID -> set(dependentID)
	dependents := make(map[string]map[string]struct{}, len(issues))
	for _, iss := range issues {
		seen := make(map[string]struct{})
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blockerID := dep.DependsOnID
			if blockerID == "" {
				continue
			}
			if _, exists := issueByID[blockerID]; !exists {
				continue // there is no known blocker whose completion can be simulated
			}
			if _, dup := seen[blockerID]; dup {
				continue
			}
			seen[blockerID] = struct{}{}

			if dependents[blockerID] == nil {
				dependents[blockerID] = make(map[string]struct{})
			}
			dependents[blockerID][iss.ID] = struct{}{}
		}
	}

	out := make(map[string][]string, len(issues))
	for _, blocker := range issues {
		if blocker.Status == model.StatusClosed || blocker.Status == model.StatusTombstone {
			continue
		}

		var unblocks []string
		for dependentID := range dependents[blocker.ID] {
			dependent := issueByID[dependentID]
			if dependent.Status != model.StatusOpen && dependent.Status != model.StatusInProgress {
				continue
			}

			stillBlocked := false
			seenOther := make(map[string]struct{})
			for _, dep := range dependent.Dependencies {
				if dep == nil || !dep.Type.IsBlocking() {
					continue
				}
				otherID := dep.DependsOnID
				if otherID == blocker.ID {
					continue
				}
				if _, exists := issueByID[otherID]; !exists {
					stillBlocked = true
					break
				}
				if _, dup := seenOther[otherID]; dup {
					continue
				}
				seenOther[otherID] = struct{}{}

				if status := issueByID[otherID].Status; status != model.StatusClosed && status != model.StatusTombstone {
					stillBlocked = true
					break
				}
			}

			if !stillBlocked {
				unblocks = append(unblocks, dependentID)
			}
		}

		sort.Strings(unblocks)
		out[blocker.ID] = unblocks
	}

	return out
}

func TestBuildUnblocksMap_MatchesReference(t *testing.T) {
	mkDep := func(issueID, dependsOnID string, typ model.DependencyType) *model.Dependency {
		return &model.Dependency{IssueID: issueID, DependsOnID: dependsOnID, Type: typ}
	}

	issues := []model.Issue{
		{ID: "A", Title: "Blocker A", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2},
		{ID: "X", Title: "Blocker X", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2},
		{ID: "Y", Title: "Closed blocker Y", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2},

		// Simple dependent: unblocked by A.
		{ID: "B", Title: "Depends on A", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("B", "A", model.DepBlocks)}},

		// Still blocked after A closes because X is also open.
		{ID: "C", Title: "Depends on A and X", Status: model.StatusInProgress, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("C", "A", model.DepBlocks), mkDep("C", "X", model.DepBlocks)}},

		// Completing A does not prove the other missing blocker satisfied.
		{ID: "D", Title: "Depends on A and missing", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("D", "A", model.DepBlocks), mkDep("D", "MISSING", model.DepBlocks)}},

		// Non-blocking dep should not count as a dependent of A.
		{ID: "E", Title: "Related to A only", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("E", "A", model.DepRelated)}},

		// Duplicate dep should not double-count; closed other blocker should not block.
		{ID: "F", Title: "Depends on A twice and closed Y", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{
				mkDep("F", "A", model.DepBlocks),
				mkDep("F", "A", model.DepBlocks),
				mkDep("F", "Y", model.DepBlocks),
			}},

		// Closed dependent should never be returned in unblocks lists.
		{ID: "G", Title: "Closed dependent", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("G", "A", model.DepBlocks)}},

		// Completing A does not move explicitly parked work back to open.
		{ID: "H", Title: "Parked dependent", Status: model.StatusBlocked, IssueType: model.TypeTask, Priority: 2,
			Dependencies: []*model.Dependency{mkDep("H", "A", model.DepBlocks)}},
	}

	analyzer := NewAnalyzer(issues)
	triageCtx := NewTriageContext(analyzer)

	// Build map using the optimized path
	got := buildUnblocksMap(triageCtx)
	want := referenceUnblocksMap(issues)

	// Sanity-check the reference expectations (guards the guardrail).
	if !reflect.DeepEqual(want["A"], []string{"B", "F"}) {
		t.Fatalf("reference sanity failed for A: got %v", want["A"])
	}
	if want["X"] != nil && len(want["X"]) != 0 {
		t.Fatalf("reference sanity failed for X: expected no unblocks, got %v", want["X"])
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unblocks map mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestComputeProjectVelocity_BucketsAndEstimated(t *testing.T) {
	now := time.Date(2025, time.December, 17, 12, 0, 0, 0, time.UTC)

	tm := func(t time.Time) *time.Time { return &t }

	// Choose timestamps strictly after the 7/30 day boundaries to avoid fencepost ambiguity.
	closed1 := now.Add(-1 * time.Hour)                       // within 7d
	closed2 := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC) // within 7d
	closed3 := time.Date(2025, 12, 9, 0, 0, 0, 0, time.UTC)  // outside 7d, within 30d

	created2DaysBefore := func(t time.Time) time.Time { return t.Add(-48 * time.Hour) }
	updated4 := time.Date(2025, 12, 16, 0, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "o1", Title: "open", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2},

		{ID: "c1", Title: "closed1", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			CreatedAt: created2DaysBefore(closed1), ClosedAt: tm(closed1)},
		{ID: "c2", Title: "closed2", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			CreatedAt: created2DaysBefore(closed2), ClosedAt: tm(closed2)},
		{ID: "c3", Title: "closed3", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			CreatedAt: created2DaysBefore(closed3), ClosedAt: tm(closed3)},

		// Missing closed_at -> use updated_at, mark Estimated.
		{ID: "c4", Title: "closed4 (no closed_at)", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			CreatedAt: created2DaysBefore(updated4),
			UpdatedAt: updated4},

		// Missing closed_at and updated_at -> fallback to now, mark Estimated.
		{ID: "c5", Title: "closed5 (no timestamps)", Status: model.StatusClosed, IssueType: model.TypeTask, Priority: 2,
			CreatedAt: created2DaysBefore(now)},
	}

	v := ComputeProjectVelocity(issues, now, 3)
	if v == nil {
		t.Fatal("expected velocity, got nil")
	}

	if v.ClosedLast7Days != 4 {
		t.Fatalf("ClosedLast7Days: expected 4, got %d", v.ClosedLast7Days)
	}
	if v.ClosedLast30Days != 5 {
		t.Fatalf("ClosedLast30Days: expected 5, got %d", v.ClosedLast30Days)
	}
	if !v.Estimated {
		t.Fatal("expected Estimated=true due to missing closed_at timestamps")
	}

	// Weekly buckets: Monday starts for ISO weeks containing now.
	wantWeeks := []VelocityWeek{
		{WeekStart: time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC), Closed: 4},
		{WeekStart: time.Date(2025, 12, 8, 0, 0, 0, 0, time.UTC), Closed: 1},
		{WeekStart: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), Closed: 0},
	}
	if !reflect.DeepEqual(v.Weekly, wantWeeks) {
		t.Fatalf("Weekly mismatch\n got: %#v\nwant: %#v", v.Weekly, wantWeeks)
	}

	// AvgDaysToClose: constructed to be exactly 2.0 days.
	const eps = 1e-9
	if diff := v.AvgDaysToClose - 2.0; diff < -eps || diff > eps {
		t.Fatalf("AvgDaysToClose: expected 2.0, got %v", v.AvgDaysToClose)
	}

	// Guardrail: triage must keep exposing velocity consistent with ComputeProjectVelocity.
	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{TopN: 1, QuickWinN: 1, BlockerN: 1}, now)
	tv := triage.ProjectHealth.Velocity
	if tv == nil {
		t.Fatal("expected triage velocity, got nil")
	}
	if tv.ClosedLast7Days != v.ClosedLast7Days || tv.ClosedLast30Days != v.ClosedLast30Days || tv.Estimated != v.Estimated {
		t.Fatalf("triage velocity mismatch: %+v vs %+v", *tv, *v)
	}
	if len(tv.Weekly) < len(v.Weekly) {
		t.Fatalf("triage weekly shorter than expected: %d < %d", len(tv.Weekly), len(v.Weekly))
	}
	if !reflect.DeepEqual(tv.Weekly[:len(v.Weekly)], v.Weekly) {
		t.Fatalf("triage weekly prefix mismatch\n got: %#v\nwant: %#v", tv.Weekly[:len(v.Weekly)], v.Weekly)
	}
}
