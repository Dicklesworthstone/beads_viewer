package analysis

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestComputeTriage_Empty(t *testing.T) {
	triage := ComputeTriage(nil)

	if triage.Meta.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", triage.Meta.Version)
	}
	if triage.QuickRef.OpenCount != 0 {
		t.Errorf("expected 0 open count, got %d", triage.QuickRef.OpenCount)
	}
	if len(triage.Recommendations) != 0 {
		t.Errorf("expected 0 recommendations, got %d", len(triage.Recommendations))
	}
}

func TestComputeTriage_BasicIssues(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "test-1",
			Title:     "First issue",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			UpdatedAt: time.Now().Add(-24 * time.Hour),
		},
		{
			ID:        "test-2",
			Title:     "Second issue",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeBug,
			UpdatedAt: time.Now().Add(-48 * time.Hour),
		},
		{
			ID:        "test-3",
			Title:     "Closed issue",
			Status:    model.StatusClosed,
			Priority:  1,
			IssueType: model.TypeTask,
		},
	}

	triage := ComputeTriage(issues)

	// Check counts
	if triage.QuickRef.OpenCount != 2 {
		t.Errorf("expected 2 open, got %d", triage.QuickRef.OpenCount)
	}
	if triage.ProjectHealth.Counts.Closed != 1 {
		t.Errorf("expected 1 closed, got %d", triage.ProjectHealth.Counts.Closed)
	}
	if triage.ProjectHealth.Counts.Total != 3 {
		t.Errorf("expected 3 total, got %d", triage.ProjectHealth.Counts.Total)
	}

	// Should have recommendations for open issues
	if len(triage.Recommendations) == 0 {
		t.Error("expected at least one recommendation")
	}

	// Commands should be populated
	if triage.Commands.ListReady != "CI=1 br ready --json" {
		t.Errorf("expected 'CI=1 br ready --json' command, got %s", triage.Commands.ListReady)
	}
}

// TestComputeTriage_CountSemantics pins the strict count semantics from
// issue #165 on a mixed-status fixture: open_count/blocked_count must equal
// the canonical by_status counts, the legacy aggregates live on
// not_closed/not_actionable, and the partition invariant
// not_closed == actionable + not_actionable holds.
func TestComputeTriage_CountSemantics(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		// 2 strictly open: one actionable, one dependency-blocked.
		{ID: "open-1", Title: "Open actionable", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "open-2", Title: "Open but dep-blocked", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "open-1", Type: model.DepBlocks}}},
		// 1 in_progress.
		{ID: "wip-1", Title: "In progress", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeBug, UpdatedAt: now},
		// 1 status-blocked (also dependency-blocked).
		{ID: "blk-1", Title: "Status blocked", Status: model.StatusBlocked, Priority: 2, IssueType: model.TypeTask, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "open-1", Type: model.DepBlocks}}},
		// 1 deferred.
		{ID: "def-1", Title: "Deferred", Status: model.StatusDeferred, Priority: 3, IssueType: model.TypeTask, UpdatedAt: now},
		// The remaining recognized live statuses also belong to not_closed.
		{ID: "draft-1", Title: "Draft", Status: model.StatusDraft, Priority: 3, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "pinned-1", Title: "Pinned", Status: model.StatusPinned, Priority: 3, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "hooked-1", Title: "Hooked", Status: model.StatusHooked, Priority: 3, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "review-1", Title: "Review", Status: model.StatusReview, Priority: 3, IssueType: model.TypeTask, UpdatedAt: now},
		// 1 closed + 1 tombstone (both closed-like).
		{ID: "done-1", Title: "Closed", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
		{ID: "ghost-1", Title: "Tombstone", Status: model.StatusTombstone, Priority: 1, IssueType: model.TypeTask, UpdatedAt: now},
	}

	triage := ComputeTriage(issues)
	counts := triage.ProjectHealth.Counts
	qr := triage.QuickRef

	// Strict status counts must mirror by_status exactly.
	if counts.Open != counts.ByStatus["open"] {
		t.Errorf("counts.open=%d must equal by_status.open=%d", counts.Open, counts.ByStatus["open"])
	}
	if counts.Blocked != counts.ByStatus["blocked"] {
		t.Errorf("counts.blocked=%d must equal by_status.blocked=%d", counts.Blocked, counts.ByStatus["blocked"])
	}
	if qr.OpenCount != counts.ByStatus["open"] {
		t.Errorf("quick_ref.open_count=%d must equal by_status.open=%d", qr.OpenCount, counts.ByStatus["open"])
	}
	if qr.BlockedCount != counts.ByStatus["blocked"] {
		t.Errorf("quick_ref.blocked_count=%d must equal by_status.blocked=%d", qr.BlockedCount, counts.ByStatus["blocked"])
	}

	// Absolute expectations for this fixture.
	if counts.Open != 2 {
		t.Errorf("expected 2 strictly open, got %d", counts.Open)
	}
	if counts.Blocked != 1 {
		t.Errorf("expected 1 status-blocked, got %d", counts.Blocked)
	}
	if counts.Closed != 2 {
		t.Errorf("expected 2 closed-like (closed+tombstone), got %d", counts.Closed)
	}
	if counts.NotClosed != 9 {
		t.Errorf("expected 9 not closed, got %d", counts.NotClosed)
	}
	// Legacy aggregates survive under the new names: not_closed is the old
	// "open" (every status except closed/tombstone) and dependency_blocked
	// is the old "blocked" (non-closed && !actionable).
	if counts.NotClosed != counts.Total-counts.Closed {
		t.Errorf("not_closed=%d must equal total-closed=%d", counts.NotClosed, counts.Total-counts.Closed)
	}

	// Partition invariant: not_closed == actionable + not_actionable.
	if counts.NotClosed != counts.Actionable+counts.DependencyBlocked {
		t.Errorf("partition invariant violated: not_closed=%d, actionable=%d, dependency_blocked=%d",
			counts.NotClosed, counts.Actionable, counts.DependencyBlocked)
	}
	if qr.NotClosedCount != qr.ActionableCount+qr.NotActionableCount {
		t.Errorf("quick_ref partition invariant violated: not_closed_count=%d, actionable_count=%d, not_actionable_count=%d",
			qr.NotClosedCount, qr.ActionableCount, qr.NotActionableCount)
	}

	// QuickRef mirrors HealthCounts.
	if qr.NotClosedCount != counts.NotClosed {
		t.Errorf("quick_ref.not_closed_count=%d must equal counts.not_closed=%d", qr.NotClosedCount, counts.NotClosed)
	}
	if qr.NotActionableCount != counts.DependencyBlocked {
		t.Errorf("quick_ref.not_actionable_count=%d must equal counts.dependency_blocked=%d", qr.NotActionableCount, counts.DependencyBlocked)
	}
	if qr.InProgressCount != counts.ByStatus["in_progress"] {
		t.Errorf("quick_ref.in_progress_count=%d must equal by_status.in_progress=%d", qr.InProgressCount, counts.ByStatus["in_progress"])
	}
}

func TestComputeTriage_IgnoresTombstoneIssues(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "ghost",
			Title:     "Deleted issue",
			Status:    model.StatusTombstone,
			Priority:  0,
			IssueType: model.TypeTask,
		},
		{
			ID:        "live",
			Title:     "Depends on ghost",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			UpdatedAt: time.Now().Add(-24 * time.Hour),
			Dependencies: []*model.Dependency{
				{DependsOnID: "ghost", Type: model.DepBlocks},
			},
		},
	}

	triage := ComputeTriage(issues)

	if triage.QuickRef.OpenCount != 1 {
		t.Errorf("expected 1 open (tombstones excluded), got %d", triage.QuickRef.OpenCount)
	}
	if triage.QuickRef.ActionableCount != 1 {
		t.Errorf("expected 1 actionable (tombstone blockers ignored), got %d", triage.QuickRef.ActionableCount)
	}
	if triage.QuickRef.BlockedCount != 0 {
		t.Errorf("expected 0 blocked (tombstone blockers ignored), got %d", triage.QuickRef.BlockedCount)
	}
	if triage.QuickRef.NotActionableCount != 0 {
		t.Errorf("expected 0 not actionable (tombstone blockers ignored), got %d", triage.QuickRef.NotActionableCount)
	}

	for _, rec := range triage.Recommendations {
		if rec.ID == "ghost" {
			t.Fatalf("tombstone issue should never be recommended")
		}
	}
	for _, pick := range triage.QuickRef.TopPicks {
		if pick.ID == "ghost" {
			t.Fatalf("tombstone issue should never appear in top_picks")
		}
	}
	for _, b := range triage.BlockersToClear {
		if b.ID == "ghost" {
			t.Fatalf("tombstone issue should never appear in blockers_to_clear")
		}
	}
}

func TestComputeTriage_WithDependencies(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "blocker",
			Title:     "Blocker issue",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			UpdatedAt: time.Now(),
		},
		{
			ID:        "blocked",
			Title:     "Blocked issue",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			UpdatedAt: time.Now(),
			Dependencies: []*model.Dependency{
				{DependsOnID: "blocker", Type: model.DepBlocks},
			},
		},
	}

	triage := ComputeTriage(issues)

	// One should be dependency-blocked (not actionable). Both issues have
	// status "open", so the strict blocked_count stays 0 (#165).
	if triage.QuickRef.NotActionableCount != 1 {
		t.Errorf("expected 1 not actionable, got %d", triage.QuickRef.NotActionableCount)
	}
	if triage.QuickRef.BlockedCount != 0 {
		t.Errorf("expected 0 status-blocked, got %d", triage.QuickRef.BlockedCount)
	}
	if triage.QuickRef.ActionableCount != 1 {
		t.Errorf("expected 1 actionable, got %d", triage.QuickRef.ActionableCount)
	}

	// Blocker should appear in blockers_to_clear
	found := false
	for _, b := range triage.BlockersToClear {
		if b.ID == "blocker" && b.UnblocksCount == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected blocker to appear in blockers_to_clear")
	}
}

func TestComputeTriage_TopPicks(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 2, UpdatedAt: time.Now()},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, UpdatedAt: time.Now()},
		{ID: "c", Title: "C", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{ID: "d", Title: "D", Status: model.StatusOpen, Priority: 3, UpdatedAt: time.Now()},
	}

	triage := ComputeTriage(issues)

	// Should have top picks
	if len(triage.QuickRef.TopPicks) == 0 {
		t.Error("expected top picks")
	}
	if len(triage.QuickRef.TopPicks) > 3 {
		t.Errorf("expected max 3 top picks, got %d", len(triage.QuickRef.TopPicks))
	}
}

func TestClaimableRecommendationRejectsCommandUnsafeID(t *testing.T) {
	rec := Recommendation{
		ID:     "--help",
		Status: string(model.StatusOpen),
		Type:   string(model.TypeTask),
	}
	if isClaimableRecommendation(rec, time.Time{}, nil, nil) {
		t.Fatal("option-shaped bead ID was exposed as a claimable recommendation")
	}
	commands := buildCommands(rec.ID)
	if commands.ClaimTop != "CI=1 br ready --json  # No top pick available" ||
		commands.ShowTop != "CI=1 br ready --json  # No top pick available" {
		t.Fatalf("unsafe bead ID leaked into commands: %+v", commands)
	}
}

func TestComputeTriageWithOptions(t *testing.T) {
	issues := make([]model.Issue, 20)
	for i := 0; i < 20; i++ {
		issues[i] = model.Issue{
			ID:        string(rune('a' + i)),
			Title:     "Issue " + string(rune('A'+i)),
			Status:    model.StatusOpen,
			Priority:  i % 4,
			UpdatedAt: time.Now().Add(-time.Duration(i) * 24 * time.Hour),
		}
	}

	opts := TriageOptions{
		TopN:      5,
		QuickWinN: 3,
		BlockerN:  2,
	}

	triage := ComputeTriageWithOptions(issues, opts)

	if len(triage.Recommendations) > 5 {
		t.Errorf("expected max 5 recommendations, got %d", len(triage.Recommendations))
	}
	if len(triage.QuickWins) > 3 {
		t.Errorf("expected max 3 quick wins, got %d", len(triage.QuickWins))
	}
}

func TestTriageRecommendation_Action(t *testing.T) {
	// Issue in progress for a long time should suggest review
	issues := []model.Issue{
		{
			ID:        "stale-wip",
			Title:     "Stale work in progress",
			Status:    model.StatusInProgress,
			Priority:  1,
			UpdatedAt: time.Now().Add(-20 * 24 * time.Hour), // 20 days old
		},
	}

	triage := ComputeTriage(issues)

	if len(triage.Recommendations) == 0 {
		t.Fatal("expected recommendations")
	}

	rec := triage.Recommendations[0]
	expectedAction := "Check if this is stuck and needs help"
	if rec.Action != expectedAction {
		t.Errorf("expected action '%s' for stale in_progress, got %s", expectedAction, rec.Action)
	}
}

func TestTriageHealthCounts(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeBug},
		{ID: "2", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeBug},
		{ID: "3", Status: model.StatusInProgress, Priority: 1, IssueType: model.TypeTask},
		{ID: "4", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeFeature},
		{ID: "5", Status: model.StatusBlocked, Priority: 2, IssueType: model.TypeTask},
	}

	triage := ComputeTriage(issues)
	counts := triage.ProjectHealth.Counts

	if counts.ByType["bug"] != 2 {
		t.Errorf("expected 2 bugs, got %d", counts.ByType["bug"])
	}
	if counts.ByType["task"] != 2 {
		t.Errorf("expected 2 tasks, got %d", counts.ByType["task"])
	}
	if counts.ByPriority[1] != 2 {
		t.Errorf("expected 2 P1, got %d", counts.ByPriority[1])
	}
}

func TestTriageGraphHealth(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Status: model.StatusOpen},
		{ID: "b", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}}},
		{ID: "c", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "b", Type: model.DepBlocks}}},
	}

	triage := ComputeTriage(issues)
	graph := triage.ProjectHealth.Graph

	if graph.NodeCount != 3 {
		t.Errorf("expected 3 nodes, got %d", graph.NodeCount)
	}
	if graph.EdgeCount != 2 {
		t.Errorf("expected 2 edges, got %d", graph.EdgeCount)
	}
	if graph.HasCycles {
		t.Error("expected no cycles")
	}
}

func TestTriageWithCycles(t *testing.T) {
	// Create a cycle: a -> b -> a
	issues := []model.Issue{
		{ID: "a", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "b", Type: model.DepBlocks}}},
		{ID: "b", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}}},
	}

	opts := TriageOptions{WaitForPhase2: true}
	triage := ComputeTriageWithOptions(issues, opts)
	graph := triage.ProjectHealth.Graph

	if !graph.HasCycles {
		t.Error("expected cycles to be detected")
	}
	if graph.CycleCount == 0 {
		t.Error("expected cycle count > 0")
	}
}

func TestProjectVelocityComputed(t *testing.T) {
	now := time.Date(2025, 12, 16, 0, 0, 0, 0, time.UTC)
	closed := now.Add(-3 * 24 * time.Hour)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusClosed, CreatedAt: now.Add(-10 * 24 * time.Hour), ClosedAt: &closed},
		{ID: "B", Status: model.StatusOpen},
	}
	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{}, now)
	if triage.ProjectHealth.Velocity == nil {
		t.Fatalf("expected velocity data")
	}
	v := triage.ProjectHealth.Velocity
	if v.ClosedLast7Days != 1 || len(v.Weekly) == 0 {
		t.Fatalf("unexpected velocity %+v", v)
	}
	if v.AvgDaysToClose <= 0 {
		t.Fatalf("expected avg days to close > 0, got %.2f", v.AvgDaysToClose)
	}
}

func TestComputeProjectVelocity_BoundaryInclusivity(t *testing.T) {
	now := time.Date(2025, 12, 16, 0, 0, 0, 0, time.UTC)
	weekAgo := now.Add(-7 * 24 * time.Hour)
	monthAgo := now.Add(-30 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "week-boundary", Status: model.StatusClosed, CreatedAt: weekAgo.Add(-24 * time.Hour), ClosedAt: &weekAgo},
		{ID: "month-boundary", Status: model.StatusClosed, CreatedAt: monthAgo.Add(-24 * time.Hour), ClosedAt: &monthAgo},
	}

	v := ComputeProjectVelocity(issues, now, 4)
	if v == nil {
		t.Fatal("expected velocity, got nil")
	}
	if v.ClosedLast7Days != 1 {
		t.Fatalf("ClosedLast7Days: expected 1, got %d", v.ClosedLast7Days)
	}
	if v.ClosedLast30Days != 2 {
		t.Fatalf("ClosedLast30Days: expected 2, got %d", v.ClosedLast30Days)
	}
}

func TestTriageEmptyCommands(t *testing.T) {
	// When there are no open issues, commands should be gracefully handled
	issues := []model.Issue{
		{ID: "closed-1", Status: model.StatusClosed},
	}

	triage := ComputeTriage(issues)

	if triage.Commands.ClaimTop != "CI=1 br ready --json  # No top pick available" {
		t.Errorf("unexpected ClaimTop fallback: %q", triage.Commands.ClaimTop)
	}
}

func TestTriageNoRecommendationsCommands(t *testing.T) {
	// Empty project
	triage := ComputeTriage(nil)

	// Commands should be valid even with no recommendations
	if triage.Commands.ListReady != "CI=1 br ready --json" {
		t.Errorf("expected 'CI=1 br ready --json', got %s", triage.Commands.ListReady)
	}
	if triage.Commands.ClaimTop != "CI=1 br ready --json  # No top pick available" {
		t.Errorf("unexpected ClaimTop fallback: %q", triage.Commands.ClaimTop)
	}
}

func TestComputeTriage_ParentBlockedChildNotTopPickOrClaim(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "blocker",
			Title:     "Root blocker",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			UpdatedAt: now,
		},
		{
			ID:        "parent",
			Title:     "Blocked parent",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "blocker", Type: model.DepBlocks},
			},
		},
		{
			ID:        "child",
			Title:     "High-priority child",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "parent", Type: model.DepParentChild},
			},
		},
	}

	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{WaitForPhase2: true}, now)

	if triage.QuickRef.ActionableCount != 1 {
		t.Fatalf("expected only the root blocker to be actionable, got %d", triage.QuickRef.ActionableCount)
	}
	// Parent and child are dependency-blocked but their status is "open",
	// so they count under not_actionable_count, not the strict
	// blocked_count (#165).
	if triage.QuickRef.NotActionableCount != 2 {
		t.Fatalf("expected parent and child not actionable, got %d", triage.QuickRef.NotActionableCount)
	}
	if triage.QuickRef.BlockedCount != 0 {
		t.Fatalf("expected 0 status-blocked, got %d", triage.QuickRef.BlockedCount)
	}

	for _, pick := range triage.QuickRef.TopPicks {
		if pick.ID == "parent" || pick.ID == "child" {
			t.Fatalf("blocked issue %q must not appear in top picks", pick.ID)
		}
	}
	if len(triage.QuickRef.TopPicks) == 0 || triage.QuickRef.TopPicks[0].ID != "blocker" {
		t.Fatalf("expected blocker to be the top actionable pick, got %+v", triage.QuickRef.TopPicks)
	}

	expectedClaim := "CI=1 br update blocker --status in_progress --json"
	if triage.Commands.ClaimTop != expectedClaim {
		t.Fatalf("expected claim_top %q, got %q", expectedClaim, triage.Commands.ClaimTop)
	}

	var childRec *Recommendation
	var parentRec *Recommendation
	for i := range triage.Recommendations {
		switch triage.Recommendations[i].ID {
		case "child":
			childRec = &triage.Recommendations[i]
		case "parent":
			parentRec = &triage.Recommendations[i]
		}
	}
	if childRec == nil {
		t.Fatal("expected child recommendation to remain visible with blocked_by context")
	}
	if len(childRec.BlockedBy) != 1 || childRec.BlockedBy[0] != "parent" {
		t.Fatalf("expected child blocked_by parent, got %v", childRec.BlockedBy)
	}
	if parentRec == nil {
		t.Fatal("expected parent recommendation to remain visible with blocked_by context")
	}
	if len(parentRec.BlockedBy) != 1 || parentRec.BlockedBy[0] != "blocker" {
		t.Fatalf("expected parent blocked_by blocker, got %v", parentRec.BlockedBy)
	}
}

// TestComputeTriage_ParentChildOpenParentDoesNotBlockChild verifies that
// when a child has ONLY a parent-child rollup edge to an OPEN, UNBLOCKED
// parent, the child is treated as ready work — not as "blocked by parent".
// This matches `br ready`'s semantics (beads_rust's
// compute_blocked_issues_map_impl excludes parent-child from direct blocker
// edges; an open parent only propagates blocking down to its descendants
// when the parent ITSELF is transitively blocked).
//
// History:
//
//   - bv-96 / a010545 added an aggressive rule that treated every open
//     parent-child parent as a claim blocker; this test originally asserted
//     that the child must not appear in top picks while the parent was
//     open, encoding that aggressive behavior.
//   - beads_viewer#158 reported this as the inverse of `br ready` — a
//     standalone open parent (rollup container) never gates its child's
//     readiness, and the inverted advice ("Work on parent first to unblock
//     child") was actively misleading agents.
//
// The transitive-blocked case (parent has a real `blocks` predecessor) is
// covered by TestComputeTriage_ParentBlockedChildNotTopPickOrClaim.
func TestComputeTriage_ParentChildOpenParentDoesNotBlockChild(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "parent",
			Title:     "Parent issue",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			UpdatedAt: now,
		},
		{
			ID:        "child",
			Title:     "Higher-priority child",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{DependsOnID: "parent", Type: model.DepParentChild},
			},
		},
	}

	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{WaitForPhase2: true}, now)

	// Both parent and child are actionable: the parent has no edges at all,
	// and the child's only edge is a rollup that never gates it.
	if triage.QuickRef.ActionableCount != 2 {
		t.Fatalf("expected both parent and child actionable, got %d", triage.QuickRef.ActionableCount)
	}
	if triage.QuickRef.BlockedCount != 0 {
		t.Fatalf("expected zero blocked issues, got %d", triage.QuickRef.BlockedCount)
	}
	if triage.QuickRef.NotActionableCount != 0 {
		t.Fatalf("expected zero not-actionable issues, got %d", triage.QuickRef.NotActionableCount)
	}

	var childRec *Recommendation
	for i := range triage.Recommendations {
		if triage.Recommendations[i].ID == "child" {
			childRec = &triage.Recommendations[i]
			break
		}
	}
	if childRec == nil {
		t.Fatal("expected child recommendation to be present")
	}
	if len(childRec.BlockedBy) != 0 {
		t.Fatalf("child.BlockedBy = %v, want empty (parent-child to open parent is not a blocker)", childRec.BlockedBy)
	}
}

func TestTriageInProgressAction(t *testing.T) {
	// Test the different staleness thresholds for in_progress items
	tests := []struct {
		name           string
		daysOld        int
		expectedAction string
	}{
		{"fresh in_progress", 5, "Continue work on this issue"},            // < 7 days - in_progress gets "Continue"
		{"moderate in_progress", 12, "Continue work on this issue"},        // 7-14 days - in_progress gets "Continue"
		{"stale in_progress", 20, "Check if this is stuck and needs help"}, // > 14 days - check if stuck
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := []model.Issue{
				{
					ID:        "wip",
					Title:     tt.name,
					Status:    model.StatusInProgress,
					Priority:  1,
					UpdatedAt: time.Now().Add(-time.Duration(tt.daysOld) * 24 * time.Hour),
				},
			}

			triage := ComputeTriage(issues)
			if len(triage.Recommendations) == 0 {
				t.Fatal("expected recommendations")
			}

			rec := triage.Recommendations[0]
			if rec.Action != tt.expectedAction {
				t.Errorf("expected action %q, got %q (staleness: %.2f)",
					tt.expectedAction, rec.Action, rec.Breakdown.StalenessNorm)
			}
		})
	}
}

// ============================================================================
// Tests for bv-147 Triage Scoring
// ============================================================================

func TestComputeTriageScores_Empty(t *testing.T) {
	scores := ComputeTriageScores(nil)
	if scores != nil {
		t.Errorf("expected nil for empty issues, got %d scores", len(scores))
	}
}

func TestComputeTriageScores_BasicIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "High priority", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{ID: "b", Title: "Medium priority", Status: model.StatusOpen, Priority: 2, UpdatedAt: time.Now()},
		{ID: "c", Title: "Low priority", Status: model.StatusOpen, Priority: 4, UpdatedAt: time.Now()},
	}

	scores := ComputeTriageScores(issues)

	if len(scores) != 3 {
		t.Errorf("expected 3 scores, got %d", len(scores))
	}

	// Should be sorted by triage score descending
	for i := 0; i < len(scores)-1; i++ {
		if scores[i].TriageScore < scores[i+1].TriageScore {
			t.Errorf("scores not sorted descending: %f < %f", scores[i].TriageScore, scores[i+1].TriageScore)
		}
	}

	// Check factors applied includes base and quick_win (no unblock for isolated issues)
	for _, score := range scores {
		hasBase := false
		for _, f := range score.FactorsApplied {
			if f == "base" {
				hasBase = true
			}
		}
		if !hasBase {
			t.Errorf("score for %s missing 'base' factor", score.IssueID)
		}
	}
}

func TestComputeTriageScores_WithUnblocks(t *testing.T) {
	// Create a chain: blocker -> blocked
	issues := []model.Issue{
		{ID: "blocker", Title: "Blocker", Status: model.StatusOpen, Priority: 1, UpdatedAt: time.Now()},
		{
			ID:        "blocked",
			Title:     "Blocked",
			Status:    model.StatusOpen,
			Priority:  0,
			UpdatedAt: time.Now(),
			Dependencies: []*model.Dependency{
				{DependsOnID: "blocker", Type: model.DepBlocks},
			},
		},
	}

	scores := ComputeTriageScores(issues)

	// Find the blocker score
	var blockerScore *TriageScore
	for i := range scores {
		if scores[i].IssueID == "blocker" {
			blockerScore = &scores[i]
			break
		}
	}

	if blockerScore == nil {
		t.Fatal("blocker score not found")
	}

	// Blocker should have unblock boost
	if blockerScore.TriageFactors.UnblockBoost <= 0 {
		t.Errorf("blocker should have positive unblock boost, got %f", blockerScore.TriageFactors.UnblockBoost)
	}

	// Check unblock is in factors applied
	hasUnblock := false
	for _, f := range blockerScore.FactorsApplied {
		if f == "unblock" {
			hasUnblock = true
		}
	}
	if !hasUnblock {
		t.Error("blocker should have 'unblock' in factors applied")
	}
}

func TestComputeTriageScores_QuickWin(t *testing.T) {
	// Issue with no blockers should get quick win boost
	issues := []model.Issue{
		{ID: "easy", Title: "Easy win", Status: model.StatusOpen, Priority: 2, UpdatedAt: time.Now()},
	}

	scores := ComputeTriageScores(issues)

	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}

	// Should have quick_win factor
	hasQuickWin := false
	for _, f := range scores[0].FactorsApplied {
		if f == "quick_win" {
			hasQuickWin = true
		}
	}
	if !hasQuickWin {
		t.Error("isolated issue should have 'quick_win' in factors applied")
	}
}

func TestComputeTriageScores_PendingFactors(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "Test", Status: model.StatusOpen, Priority: 1, UpdatedAt: time.Now()},
	}

	scores := ComputeTriageScores(issues)

	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}

	// Should have pending factors for features not yet enabled
	expectedPending := []string{"label_health", "claim_penalty", "attention_score"}
	for _, expected := range expectedPending {
		found := false
		for _, p := range scores[0].FactorsPending {
			if p == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected '%s' in factors pending", expected)
		}
	}
}

func TestComputeTriageScoresWithOptions_CustomWeights(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "Test", Status: model.StatusOpen, Priority: 1, UpdatedAt: time.Now()},
	}

	opts := TriageScoringOptions{
		BaseScoreWeight:    0.5,
		UnblockBoostWeight: 0.25,
		QuickWinWeight:     0.25,
		UnblockThreshold:   3,
		QuickWinMaxDepth:   1,
	}

	scores := ComputeTriageScoresWithOptions(issues, opts)

	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}

	// Triage score should be different from base score (due to quick win)
	if scores[0].TriageScore == scores[0].BaseScore {
		t.Error("triage score should differ from base score when quick_win applied")
	}
}

func TestGetBlockerDepth_NoBlockers(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "No blockers", Status: model.StatusOpen},
	}
	analyzer := NewAnalyzer(issues)

	depth := analyzer.GetBlockerDepth("a")
	if depth != 0 {
		t.Errorf("expected depth 0 for issue with no blockers, got %d", depth)
	}
}

func TestGetBlockerDepth_OneLevel(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "Blocker", Status: model.StatusOpen},
		{
			ID:           "b",
			Title:        "Blocked",
			Status:       model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}},
		},
	}
	analyzer := NewAnalyzer(issues)

	depth := analyzer.GetBlockerDepth("b")
	if depth != 1 {
		t.Errorf("expected depth 1 for issue blocked by one, got %d", depth)
	}
}

func TestGetBlockerDepth_Cycle(t *testing.T) {
	issues := []model.Issue{
		{
			ID:           "a",
			Title:        "A",
			Status:       model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "b", Type: model.DepBlocks}},
		},
		{
			ID:           "b",
			Title:        "B",
			Status:       model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}},
		},
	}
	analyzer := NewAnalyzer(issues)

	depth := analyzer.GetBlockerDepth("a")
	if depth != -1 {
		t.Errorf("expected depth -1 for cyclic dependency, got %d", depth)
	}
}

func TestGetTopTriageScores(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{ID: "b", Status: model.StatusOpen, Priority: 1, UpdatedAt: time.Now()},
		{ID: "c", Status: model.StatusOpen, Priority: 2, UpdatedAt: time.Now()},
		{ID: "d", Status: model.StatusOpen, Priority: 3, UpdatedAt: time.Now()},
		{ID: "e", Status: model.StatusOpen, Priority: 4, UpdatedAt: time.Now()},
	}

	top3 := GetTopTriageScores(issues, 3)

	if len(top3) != 3 {
		t.Errorf("expected 3 top scores, got %d", len(top3))
	}

	// Request more than available
	top10 := GetTopTriageScores(issues, 10)
	if len(top10) != 5 {
		t.Errorf("expected 5 scores when requesting 10 from 5, got %d", len(top10))
	}

	if got := GetTopTriageScores(issues, -1); len(got) != 0 {
		t.Errorf("expected no scores for a negative limit, got %d", len(got))
	}
}

func TestDefaultTriageScoringOptions(t *testing.T) {
	opts := DefaultTriageScoringOptions()

	// Weights should sum to 1.0
	totalWeight := opts.BaseScoreWeight + opts.UnblockBoostWeight + opts.QuickWinWeight
	if totalWeight != 1.0 {
		t.Errorf("weights should sum to 1.0, got %f", totalWeight)
	}

	// MVP mode: all optional features off
	if opts.EnableLabelHealth || opts.EnableClaimPenalty || opts.EnableAttentionScore {
		t.Error("MVP mode should have all optional features disabled")
	}
}

// ============================================================================
// Tests for bv-148 Reason Generation
// ============================================================================

func TestGenerateTriageReasons_EmptyContext(t *testing.T) {
	ctx := TriageReasonContext{}
	reasons := GenerateTriageReasons(ctx)

	if reasons.Primary == "" {
		t.Error("expected non-empty primary reason")
	}
	if len(reasons.All) == 0 {
		t.Error("expected at least one reason")
	}
	if reasons.ActionHint == "" {
		t.Error("expected non-empty action hint")
	}
}

func TestGenerateTriageReasonsAcceptsExplicitZeroEpoch(t *testing.T) {
	deferUntil := time.Time{}.Add(time.Hour)
	reasons := GenerateTriageReasons(TriageReasonContext{
		Issue:  &model.Issue{ID: "future", Status: model.StatusOpen, DeferUntil: &deferUntil},
		Now:    time.Time{},
		HasNow: true,
	})
	if !strings.Contains(reasons.ActionHint, "Deferred until") {
		t.Fatalf("zero-epoch action hint=%q, want deferred", reasons.ActionHint)
	}
}

func TestGenerateTriageReasons_UnblockCascade(t *testing.T) {
	ctx := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.StatusOpen,
		},
		UnblocksIDs: []string{"bv-1", "bv-2", "bv-3", "bv-4", "bv-5"},
	}
	reasons := GenerateTriageReasons(ctx)

	// Should have unblock cascade as primary (>=3 unblocks)
	if reasons.Primary == "" {
		t.Error("expected primary reason for unblock cascade")
	}
	if len(reasons.All) < 2 {
		t.Errorf("expected at least 2 reasons (unblock + unclaimed), got %d", len(reasons.All))
	}

	// Check that primary contains unblock info
	foundUnblock := false
	for _, r := range reasons.All {
		if contains(r, "unblocks") || contains(r, "Unblocks") {
			foundUnblock = true
			break
		}
	}
	if !foundUnblock {
		t.Error("expected a reason about unblocking")
	}
}

func TestGenerateTriageReasons_LabelHealth(t *testing.T) {
	issue := &model.Issue{
		ID:     "test-1",
		Labels: []string{"backend", "database"},
	}
	ctx := TriageReasonContext{
		Issue: issue,
		LabelHealth: map[string]int{
			"backend":  45, // Below threshold
			"frontend": 80, // Above threshold
		},
	}
	reasons := GenerateTriageReasons(ctx)

	// Should have label attention reason for backend (health < 60)
	foundLabelReason := false
	for _, r := range reasons.All {
		if contains(r, "backend") && contains(r, "attention") {
			foundLabelReason = true
			break
		}
	}
	if !foundLabelReason {
		t.Error("expected reason about label 'backend' needing attention")
	}
}

func TestGenerateTriageReasons_Staleness(t *testing.T) {
	ctx := TriageReasonContext{
		DaysSinceUpdate: 15,
	}
	reasons := GenerateTriageReasons(ctx)

	// Should have staleness reason
	foundStale := false
	for _, r := range reasons.All {
		if contains(r, "15 days") || contains(r, "activity") {
			foundStale = true
			break
		}
	}
	if !foundStale {
		t.Error("expected reason about staleness")
	}
}

func TestGenerateTriageReasons_QuickWin(t *testing.T) {
	ctx := TriageReasonContext{
		IsQuickWin: true,
	}
	reasons := GenerateTriageReasons(ctx)

	// Should have quick win reason
	foundQuickWin := false
	for _, r := range reasons.All {
		if contains(r, "Low effort") || contains(r, "quick win") {
			foundQuickWin = true
			break
		}
	}
	if !foundQuickWin {
		t.Error("expected reason about quick win")
	}

	// Action hint should mention quick win
	if !contains(reasons.ActionHint, "Quick win") {
		t.Errorf("expected action hint to mention quick win, got: %s", reasons.ActionHint)
	}
}

func TestGenerateTriageReasons_ClaimStatus(t *testing.T) {
	// Unclaimed — requires positive evidence the issue is Open (#149 fix).
	ctx := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.StatusOpen,
		},
		ClaimedByAgent: "",
	}
	reasons := GenerateTriageReasons(ctx)

	foundUnclaimed := false
	for _, r := range reasons.All {
		if contains(r, "unclaimed") {
			foundUnclaimed = true
			break
		}
	}
	if !foundUnclaimed {
		t.Error("expected reason about being unclaimed on Open issue")
	}

	// Open + claimed
	ctx2 := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.StatusOpen,
		},
		ClaimedByAgent: "OtherAgent",
	}
	reasons2 := GenerateTriageReasons(ctx2)

	foundClaimed := false
	for _, r := range reasons2.All {
		if contains(r, "OtherAgent") {
			foundClaimed = true
			break
		}
	}
	if !foundClaimed {
		t.Error("expected reason about being claimed by OtherAgent")
	}

	// Nil-issue defensive contract (#149): with no issue record we must
	// stay silent on claim status rather than guess "unclaimed". The
	// previous behavior happily emitted the unclaimed hint with no
	// context, which (under stale snapshot conditions) was the most
	// plausible source of the IN_PROGRESS + "currently unclaimed"
	// contradiction the user reported.
	ctxNil := TriageReasonContext{ClaimedByAgent: ""}
	reasonsNil := GenerateTriageReasons(ctxNil)
	for _, r := range reasonsNil.All {
		if contains(r, "unclaimed") {
			t.Fatalf("nil-issue context must not emit unclaimed hint; got: %v", reasonsNil.All)
		}
	}

	// Custom non-enum status: the #149 fix tightens the unclaimed hint
	// to require positive Open, but the "Status is X - not ready to
	// claim" message must still fire for any non-empty unrecognized
	// status. Silencing it would be a regression for projects that
	// use custom Status values like "in_review" or "needs_approval".
	ctxCustom := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.Status("needs_approval"),
		},
	}
	reasonsCustom := GenerateTriageReasons(ctxCustom)
	foundStatusMessage := false
	for _, r := range reasonsCustom.All {
		if contains(r, "needs_approval") {
			foundStatusMessage = true
			break
		}
	}
	if !foundStatusMessage {
		t.Fatalf(
			"custom Status value must surface as \"Status is needs_approval\" reason; got: %v",
			reasonsCustom.All,
		)
	}
	for _, r := range reasonsCustom.All {
		if contains(r, "unclaimed") {
			t.Fatalf("custom-status issue must not be described as unclaimed; got: %v", reasonsCustom.All)
		}
	}

	// In progress should not be described as unclaimed.
	ctx3 := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.StatusInProgress,
		},
	}
	reasons3 := GenerateTriageReasons(ctx3)
	for _, r := range reasons3.All {
		if contains(r, "unclaimed") {
			t.Fatalf("did not expect in-progress issue to be described as unclaimed; reasons=%v", reasons3.All)
		}
	}

	foundInProgress := false
	for _, r := range reasons3.All {
		if contains(r, "In progress") {
			foundInProgress = true
			break
		}
	}
	if !foundInProgress {
		t.Fatalf("expected in-progress reason, got: %v", reasons3.All)
	}

	// Blocked and assigned should never be described as unclaimed/available.
	ctx4 := TriageReasonContext{
		Issue: &model.Issue{
			Status: model.StatusBlocked,
		},
		ClaimedByAgent: "CoralBeaver",
	}
	reasons4 := GenerateTriageReasons(ctx4)
	for _, r := range reasons4.All {
		if contains(r, "unclaimed") || contains(r, "available for work") {
			t.Fatalf("did not expect blocked assigned issue to be described as claimable; reasons=%v", reasons4.All)
		}
	}
	if !contains(reasons4.ActionHint, "CoralBeaver") {
		t.Fatalf("expected action hint to point at assignee/blocker resolution, got %q", reasons4.ActionHint)
	}
}

func TestGenerateTriageReasons_BlockedBy(t *testing.T) {
	ctx := TriageReasonContext{
		BlockedByIDs: []string{"bv-10", "bv-11"},
	}
	reasons := GenerateTriageReasons(ctx)

	foundBlocked := false
	for _, r := range reasons.All {
		if contains(r, "Blocked by") {
			foundBlocked = true
			break
		}
	}
	if !foundBlocked {
		t.Error("expected reason about being blocked")
	}

	// Action hint should mention working on blocker
	if !contains(reasons.ActionHint, "bv-10") {
		t.Errorf("expected action hint to mention first blocker, got: %s", reasons.ActionHint)
	}
}

func TestGenerateTriageReasons_HighPriority(t *testing.T) {
	ctx := TriageReasonContext{
		Issue: &model.Issue{
			ID:       "test-1",
			Priority: 0,
		},
	}
	reasons := GenerateTriageReasons(ctx)

	foundPriority := false
	for _, r := range reasons.All {
		if contains(r, "P0") || contains(r, "High priority") {
			foundPriority = true
			break
		}
	}
	if !foundPriority {
		t.Error("expected reason about high priority")
	}
}

func TestGenerateTriageReasons_GraphMetrics(t *testing.T) {
	ctx := TriageReasonContext{
		TriageScore: &TriageScore{
			Breakdown: ScoreBreakdown{
				PageRankNorm:    0.5, // Above 0.3 threshold
				BetweennessNorm: 0.7, // Above 0.5 threshold
			},
		},
	}
	reasons := GenerateTriageReasons(ctx)

	foundBottleneck := false
	foundCentrality := false
	for _, r := range reasons.All {
		if contains(r, "bottleneck") {
			foundBottleneck = true
		}
		if contains(r, "centrality") || contains(r, "PageRank") {
			foundCentrality = true
		}
	}
	if !foundBottleneck {
		t.Error("expected reason about being a bottleneck")
	}
	if !foundCentrality {
		t.Error("expected reason about high centrality")
	}
}

func TestFormatUnblockList_Empty(t *testing.T) {
	result := formatUnblockList(nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
}

func TestFormatUnblockList_Short(t *testing.T) {
	result := formatUnblockList([]string{"bv-1", "bv-2"})
	if result != "bv-1, bv-2" {
		t.Errorf("expected 'bv-1, bv-2', got %q", result)
	}
}

func TestFormatUnblockList_Long(t *testing.T) {
	result := formatUnblockList([]string{"bv-1", "bv-2", "bv-3", "bv-4", "bv-5"})
	if !contains(result, "+3 more") {
		t.Errorf("expected '+3 more' in result, got %q", result)
	}
}

func TestGenerateTriageReasonsForScore(t *testing.T) {
	issues := []model.Issue{
		{
			ID:        "blocker",
			Title:     "Blocker",
			Status:    model.StatusOpen,
			Priority:  1,
			UpdatedAt: time.Now().Add(-10 * 24 * time.Hour), // 10 days old
		},
		{
			ID:        "blocked",
			Title:     "Blocked",
			Status:    model.StatusOpen,
			Priority:  2,
			UpdatedAt: time.Now(),
			Dependencies: []*model.Dependency{
				{DependsOnID: "blocker", Type: model.DepBlocks},
			},
		},
	}

	analyzer := NewAnalyzer(issues)
	triageCtx := NewTriageContext(analyzer)

	// Get triage scores
	scores := ComputeTriageScores(issues)

	// Find blocker score
	var blockerScore TriageScore
	for _, s := range scores {
		if s.IssueID == "blocker" {
			blockerScore = s
			break
		}
	}

	reasons := GenerateTriageReasonsForScore(blockerScore, triageCtx)

	// Should have reasons
	if len(reasons.All) == 0 {
		t.Error("expected at least one reason")
	}
	if reasons.Primary == "" {
		t.Error("expected non-empty primary reason")
	}
	if reasons.ActionHint == "" {
		t.Error("expected non-empty action hint")
	}
}

func TestEnhanceRecommendationWithTriageReasons(t *testing.T) {
	rec := &Recommendation{
		ID:      "test-1",
		Title:   "Test",
		Reasons: []string{"old reason"},
	}

	triageReasons := TriageReasons{
		Primary:    "🎯 Primary reason",
		All:        []string{"🎯 Primary reason", "📊 Secondary reason"},
		ActionHint: "Do this",
	}

	EnhanceRecommendationWithTriageReasons(rec, triageReasons)

	if len(rec.Reasons) != 2 {
		t.Errorf("expected 2 reasons after enhancement, got %d", len(rec.Reasons))
	}
	if rec.Reasons[0] != "🎯 Primary reason" {
		t.Errorf("expected first reason to be primary, got %s", rec.Reasons[0])
	}
}

func TestEnhanceRecommendationWithTriageReasons_NilRec(t *testing.T) {
	// Should not panic
	EnhanceRecommendationWithTriageReasons(nil, TriageReasons{})
}

// Helper function for tests
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============================================================================
// Tests for bv-87 Track/Label-aware Recommendation Grouping
// ============================================================================

func TestTriageGroupByTrack_Empty(t *testing.T) {
	opts := TriageOptions{GroupByTrack: true}
	triage := ComputeTriageWithOptions(nil, opts)

	if len(triage.RecommendationsByTrack) > 0 {
		t.Errorf("expected empty track groups for nil issues, got %d", len(triage.RecommendationsByTrack))
	}
}

func TestTriageGroupByTrack_SingleTrack(t *testing.T) {
	// All issues in one connected component
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{
			ID:           "b",
			Title:        "B",
			Status:       model.StatusOpen,
			Priority:     1,
			UpdatedAt:    time.Now(),
			Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}},
		},
	}

	opts := TriageOptions{GroupByTrack: true}
	triage := ComputeTriageWithOptions(issues, opts)

	if len(triage.RecommendationsByTrack) == 0 {
		t.Fatal("expected at least one track group")
	}

	// All recommendations should be in one track (connected component)
	totalRecs := 0
	for _, g := range triage.RecommendationsByTrack {
		totalRecs += len(g.Recommendations)
		hasClaimableRec := false
		for _, rec := range g.Recommendations {
			if isClaimableRecommendation(rec, time.Time{}, nil, nil) {
				hasClaimableRec = true
				break
			}
		}
		if hasClaimableRec && g.TopPick == nil {
			t.Errorf("track %s missing top pick", g.TrackID)
		}
	}

	// At least one actionable issue should be in recommendations
	if totalRecs == 0 {
		t.Error("expected recommendations in track groups")
	}
}

func TestTriageGroupByTrack_MultipleTracks(t *testing.T) {
	// Two disconnected components
	issues := []model.Issue{
		{ID: "a1", Title: "A1", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{
			ID:           "a2",
			Title:        "A2",
			Status:       model.StatusOpen,
			Priority:     1,
			UpdatedAt:    time.Now(),
			Dependencies: []*model.Dependency{{DependsOnID: "a1", Type: model.DepBlocks}},
		},
		{ID: "b1", Title: "B1", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
		{
			ID:           "b2",
			Title:        "B2",
			Status:       model.StatusOpen,
			Priority:     1,
			UpdatedAt:    time.Now(),
			Dependencies: []*model.Dependency{{DependsOnID: "b1", Type: model.DepBlocks}},
		},
	}

	opts := TriageOptions{GroupByTrack: true}
	triage := ComputeTriageWithOptions(issues, opts)

	// Should have at least 2 tracks (two disconnected components)
	if len(triage.RecommendationsByTrack) < 2 {
		t.Errorf("expected at least 2 track groups for disconnected issues, got %d", len(triage.RecommendationsByTrack))
	}
}

func TestTriageGroupByLabel_Empty(t *testing.T) {
	opts := TriageOptions{GroupByLabel: true}
	triage := ComputeTriageWithOptions(nil, opts)

	if len(triage.RecommendationsByLabel) > 0 {
		t.Errorf("expected empty label groups for nil issues, got %d", len(triage.RecommendationsByLabel))
	}
}

func TestTriageGroupByLabel_SingleLabel(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, Labels: []string{"api"}, UpdatedAt: time.Now()},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Labels: []string{"api"}, UpdatedAt: time.Now()},
	}

	opts := TriageOptions{GroupByLabel: true}
	triage := ComputeTriageWithOptions(issues, opts)

	// Should have one label group for "api"
	foundAPI := false
	for _, g := range triage.RecommendationsByLabel {
		if g.Label == "api" {
			foundAPI = true
			if len(g.Recommendations) != 2 {
				t.Errorf("expected 2 recommendations for 'api' label, got %d", len(g.Recommendations))
			}
			if g.TopPick == nil {
				t.Error("api label group missing top pick")
			}
		}
	}
	if !foundAPI {
		t.Error("expected 'api' label group")
	}
}

func TestTriageGroupByLabel_MultipleLabels(t *testing.T) {
	// Note: The label grouping uses only the PRIMARY label (first label)
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, Labels: []string{"api"}, UpdatedAt: time.Now()},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Labels: []string{"frontend"}, UpdatedAt: time.Now()},
		{ID: "c", Title: "C", Status: model.StatusOpen, Priority: 2, Labels: []string{"database"}, UpdatedAt: time.Now()},
	}

	opts := TriageOptions{GroupByLabel: true}
	triage := ComputeTriageWithOptions(issues, opts)

	// Should have 3 label groups (api, frontend, database) - each uses primary label
	labels := make(map[string]bool)
	for _, g := range triage.RecommendationsByLabel {
		labels[g.Label] = true
	}

	if !labels["api"] {
		t.Error("expected 'api' label group")
	}
	if !labels["frontend"] {
		t.Error("expected 'frontend' label group")
	}
	if !labels["database"] {
		t.Error("expected 'database' label group")
	}
}

func TestTriageGroupByLabel_UnlabeledIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, Labels: []string{}, UpdatedAt: time.Now()},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Labels: []string{"api"}, UpdatedAt: time.Now()},
	}

	opts := TriageOptions{GroupByLabel: true}
	triage := ComputeTriageWithOptions(issues, opts)

	// Should have an "unlabeled" group
	foundUnlabeled := false
	for _, g := range triage.RecommendationsByLabel {
		if g.Label == "unlabeled" {
			foundUnlabeled = true
			break
		}
	}
	if !foundUnlabeled {
		t.Error("expected 'unlabeled' group for issues without labels")
	}
}

func TestTriageGroupByTrackAndLabel_Both(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, Labels: []string{"api"}, UpdatedAt: time.Now()},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, Labels: []string{"frontend"}, UpdatedAt: time.Now()},
	}

	opts := TriageOptions{GroupByTrack: true, GroupByLabel: true}
	triage := ComputeTriageWithOptions(issues, opts)

	// Both should be populated
	if len(triage.RecommendationsByTrack) == 0 {
		t.Error("expected track groups when GroupByTrack is true")
	}
	if len(triage.RecommendationsByLabel) == 0 {
		t.Error("expected label groups when GroupByLabel is true")
	}
}

func TestTriageGroupByTrack_TopPickHasHighestScore(t *testing.T) {
	issues := []model.Issue{
		{ID: "low", Title: "Low priority", Status: model.StatusOpen, Priority: 4, UpdatedAt: time.Now()},
		{ID: "high", Title: "High priority", Status: model.StatusOpen, Priority: 0, UpdatedAt: time.Now()},
	}

	opts := TriageOptions{GroupByTrack: true}
	triage := ComputeTriageWithOptions(issues, opts)

	for _, g := range triage.RecommendationsByTrack {
		if g.TopPick == nil || len(g.Recommendations) == 0 {
			continue
		}
		// Top pick should have the highest score in the group
		for _, rec := range g.Recommendations {
			if !isClaimableRecommendation(rec, time.Time{}, nil, nil) {
				continue
			}
			if rec.Score > g.TopPick.Score {
				t.Errorf("track %s: recommendation %s has higher score (%.4f) than top pick %s (%.4f)",
					g.TrackID, rec.ID, rec.Score, g.TopPick.ID, g.TopPick.Score)
			}
		}
	}
}

// ============================================================================
// Tests for bv-runn.11 ComputeTriageFromAnalyzer
// ============================================================================

func TestComputeTriageFromAnalyzer_EquivalentToStandard(t *testing.T) {
	// Fixed time for determinism
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "blocker", Title: "Blocker", Status: model.StatusOpen, Priority: 0, UpdatedAt: now},
		{ID: "blocked1", Title: "Blocked 1", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "blocked2", Title: "Blocked 2", Status: model.StatusOpen, Priority: 2, UpdatedAt: now, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "standalone", Title: "Standalone", Status: model.StatusOpen, Priority: 2, UpdatedAt: now},
	}

	opts := TriageOptions{TopN: 5, QuickWinN: 3, BlockerN: 3}

	// Method 1: Standard entrypoint
	standard := ComputeTriageWithOptionsAndTime(issues, opts, now)

	// Method 2: Using ComputeTriageFromAnalyzer with pre-built analyzer
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsync(context.Background())
	reused := ComputeTriageFromAnalyzer(analyzer, stats, issues, opts, now)

	// Verify outputs are equivalent (comparing key fields)
	if standard.QuickRef.OpenCount != reused.QuickRef.OpenCount {
		t.Errorf("OpenCount mismatch: standard=%d, reused=%d", standard.QuickRef.OpenCount, reused.QuickRef.OpenCount)
	}
	if standard.QuickRef.ActionableCount != reused.QuickRef.ActionableCount {
		t.Errorf("ActionableCount mismatch: standard=%d, reused=%d", standard.QuickRef.ActionableCount, reused.QuickRef.ActionableCount)
	}
	if standard.QuickRef.BlockedCount != reused.QuickRef.BlockedCount {
		t.Errorf("BlockedCount mismatch: standard=%d, reused=%d", standard.QuickRef.BlockedCount, reused.QuickRef.BlockedCount)
	}
	if len(standard.Recommendations) != len(reused.Recommendations) {
		t.Errorf("Recommendations count mismatch: standard=%d, reused=%d", len(standard.Recommendations), len(reused.Recommendations))
	}
	// Check recommendation order
	for i := range standard.Recommendations {
		if i >= len(reused.Recommendations) {
			break
		}
		if standard.Recommendations[i].ID != reused.Recommendations[i].ID {
			t.Errorf("Recommendation %d ID mismatch: standard=%s, reused=%s", i, standard.Recommendations[i].ID, reused.Recommendations[i].ID)
		}
	}
	if len(standard.BlockersToClear) != len(reused.BlockersToClear) {
		t.Errorf("BlockersToClear count mismatch: standard=%d, reused=%d", len(standard.BlockersToClear), len(reused.BlockersToClear))
	}
}

func TestComputeTriageFromAnalyzer_WithPhase2(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen, Priority: 0, UpdatedAt: now},
		{ID: "b", Title: "B", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{
			{DependsOnID: "a", Type: model.DepBlocks},
		}},
	}

	// Create analyzer and wait for Phase 2
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	opts := TriageOptions{TopN: 3}
	triage := ComputeTriageFromAnalyzer(analyzer, stats, issues, opts, now)

	// Should have Phase 2 ready
	if !triage.Meta.Phase2Ready {
		t.Error("expected Phase2Ready=true after waiting for Phase 2")
	}

	// Should have recommendations
	if len(triage.Recommendations) == 0 {
		t.Error("expected recommendations")
	}
}

func TestComputeTriageFromAnalyzer_Empty(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	analyzer := NewAnalyzer(nil)
	stats := analyzer.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	triage := ComputeTriageFromAnalyzer(analyzer, stats, nil, TriageOptions{}, now)

	if triage.QuickRef.OpenCount != 0 {
		t.Errorf("expected 0 open count, got %d", triage.QuickRef.OpenCount)
	}
	if len(triage.Recommendations) != 0 {
		t.Errorf("expected 0 recommendations, got %d", len(triage.Recommendations))
	}
}

// TestBuildTopPicks_FiltersBlockedItems verifies that blocked items are excluded from TopPicks.
// This is critical for --robot-next which should only return actionable items.
// Fixes: https://github.com/Dicklesworthstone/beads_viewer/issues/53
func TestBuildTopPicks_FiltersBlockedItems(t *testing.T) {
	recommendations := []Recommendation{
		{
			ID:          "blocked-high-score",
			Title:       "Blocked but high score",
			Status:      string(model.StatusOpen),
			Score:       100.0,
			BlockedBy:   []string{"blocker-1"},
			UnblocksIDs: []string{},
		},
		{
			ID:          "actionable-1",
			Title:       "Actionable item 1",
			Status:      string(model.StatusOpen),
			Score:       80.0,
			BlockedBy:   nil, // Not blocked
			UnblocksIDs: []string{"downstream-1"},
		},
		{
			ID:          "blocked-medium-score",
			Title:       "Another blocked item",
			Status:      string(model.StatusOpen),
			Score:       70.0,
			BlockedBy:   []string{"blocker-2", "blocker-3"},
			UnblocksIDs: []string{},
		},
		{
			ID:          "actionable-2",
			Title:       "Actionable item 2",
			Status:      string(model.StatusOpen),
			Score:       60.0,
			BlockedBy:   []string{}, // Empty slice = not blocked
			UnblocksIDs: []string{},
		},
		{
			ID:          "actionable-3",
			Title:       "Actionable item 3",
			Status:      string(model.StatusOpen),
			Score:       50.0,
			BlockedBy:   nil,
			UnblocksIDs: []string{"downstream-2", "downstream-3"},
		},
	}

	// Test with limit of 3
	picks := buildTopPicks(recommendations, 3, time.Time{}, nil, nil)

	// Should have exactly 3 picks (all actionable items)
	if len(picks) != 3 {
		t.Errorf("expected 3 picks, got %d", len(picks))
	}

	// Verify blocked items are excluded
	for _, pick := range picks {
		if pick.ID == "blocked-high-score" || pick.ID == "blocked-medium-score" {
			t.Errorf("blocked item %q should not be in TopPicks", pick.ID)
		}
	}

	// Verify actionable items are included in order
	expectedIDs := []string{"actionable-1", "actionable-2", "actionable-3"}
	for i, expected := range expectedIDs {
		if picks[i].ID != expected {
			t.Errorf("picks[%d].ID = %q, want %q", i, picks[i].ID, expected)
		}
	}

	// Verify unblocks count is correct
	if picks[0].Unblocks != 1 {
		t.Errorf("picks[0].Unblocks = %d, want 1", picks[0].Unblocks)
	}
	if picks[2].Unblocks != 2 {
		t.Errorf("picks[2].Unblocks = %d, want 2", picks[2].Unblocks)
	}
}

// TestComputeTriage_TopPicksReachActionableBeyondTopN is a regression
// test for issue #146 / PR #147: when the top opts.TopN scored
// recommendations are all blocked, top_picks must still surface
// actionable work from further down the scored set, not return empty.
// Pre-fix the buildRecommendations call truncated to opts.TopN before
// buildTopPicks ran its blocked-filter, so a workload of "8 blocked
// urgent bugs + 1 ready low-priority task" caused `bv --robot-next`
// to report no work.
func TestComputeTriage_TopPicksReachActionableBeyondTopN(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "ready-low-impact",
			Title:     "Ready low impact",
			Status:    model.StatusOpen,
			Priority:  4,
			IssueType: model.TypeTask,
			UpdatedAt: now,
		},
	}
	for i := 0; i < 8; i++ {
		blockedID := "blocked-urgent-" + string(rune('a'+i))
		gateID := "zz-gate-" + string(rune('a'+i))
		issues = append(issues,
			model.Issue{
				ID:        blockedID,
				Title:     "Blocked urgent bug",
				Status:    model.StatusOpen,
				Priority:  0,
				IssueType: model.TypeBug,
				Labels:    []string{"urgent"},
				UpdatedAt: now.Add(-30 * 24 * time.Hour),
				Dependencies: []*model.Dependency{
					{IssueID: blockedID, DependsOnID: gateID, Type: model.DepBlocks},
				},
			},
			model.Issue{
				ID:        gateID,
				Title:     "Gate",
				Status:    model.StatusOpen,
				Priority:  4,
				IssueType: model.TypeTask,
				UpdatedAt: now,
			},
		)
	}

	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	// TopN=3 — the top 3 scored items are all the blocked-urgent
	// bugs. Pre-fix, top_picks would be empty because buildTopPicks
	// filtered them out and never saw the actionable item further
	// down the list.
	triage := ComputeTriageFromAnalyzer(analyzer, stats, issues, TriageOptions{TopN: 3}, now)
	if len(triage.Recommendations) != 3 {
		t.Fatalf("recommendations should still be capped at TopN=3, got %d", len(triage.Recommendations))
	}
	if len(triage.QuickRef.TopPicks) == 0 {
		t.Fatal("top picks must surface actionable work that lies past the capped recommendations slice")
	}
	for _, pick := range triage.QuickRef.TopPicks {
		if blockers := analyzer.GetOpenBlockers(pick.ID); len(blockers) > 0 {
			t.Fatalf("top pick %q is blocked by %v — buildTopPicks must filter blockers", pick.ID, blockers)
		}
	}
}

func TestComputeTriage_BlockedAssignedHighPriorityNotClaimTarget(t *testing.T) {
	now := time.Date(2026, 5, 5, 15, 30, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "blocked-owned",
			Title:     "Blocked owned but important",
			Status:    model.StatusBlocked,
			Assignee:  "CoralBeaver",
			Priority:  0,
			IssueType: model.TypeBug,
			UpdatedAt: now,
		},
		{
			ID:        "ready-open",
			Title:     "Ready open work",
			Status:    model.StatusOpen,
			Priority:  4,
			IssueType: model.TypeTask,
			UpdatedAt: now,
		},
	}

	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{WaitForPhase2: true}, now)
	if len(triage.QuickRef.TopPicks) == 0 {
		t.Fatal("expected a claimable top pick")
	}
	if triage.QuickRef.TopPicks[0].ID != "ready-open" {
		t.Fatalf("expected robot top pick to skip blocked assigned work and choose ready-open, got %q", triage.QuickRef.TopPicks[0].ID)
	}

	var blockedRec *Recommendation
	for i := range triage.Recommendations {
		if triage.Recommendations[i].ID == "blocked-owned" {
			blockedRec = &triage.Recommendations[i]
			break
		}
	}
	if blockedRec == nil {
		t.Fatal("expected blocked-owned to remain present as an important recommendation")
	}
	if blockedRec.Action == "Start work on this issue" {
		t.Fatalf("blocked assigned work must not get a start-work action: %#v", blockedRec)
	}
	for _, reason := range blockedRec.Reasons {
		if contains(reason, "unclaimed") || contains(reason, "available for work") {
			t.Fatalf("blocked assigned work must not be described as unclaimed/available: %v", blockedRec.Reasons)
		}
	}
}

// TestBuildTopPicks_LimitRespected verifies the limit is respected when filtering.
func TestBuildTopPicks_LimitRespected(t *testing.T) {
	recommendations := []Recommendation{
		{ID: "a1", Title: "Actionable 1", Status: string(model.StatusOpen), Score: 100.0},
		{ID: "a2", Title: "Actionable 2", Status: string(model.StatusOpen), Score: 90.0},
		{ID: "a3", Title: "Actionable 3", Status: string(model.StatusOpen), Score: 80.0},
		{ID: "a4", Title: "Actionable 4", Status: string(model.StatusOpen), Score: 70.0},
		{ID: "a5", Title: "Actionable 5", Status: string(model.StatusOpen), Score: 60.0},
	}

	// Limit of 2
	picks := buildTopPicks(recommendations, 2, time.Time{}, nil, nil)
	if len(picks) != 2 {
		t.Errorf("expected 2 picks with limit=2, got %d", len(picks))
	}

	// Should be the top 2 by score order
	if picks[0].ID != "a1" || picks[1].ID != "a2" {
		t.Errorf("expected picks [a1, a2], got [%s, %s]", picks[0].ID, picks[1].ID)
	}
}

// TestBuildTopPicks_AllBlocked verifies empty result when all items are blocked.
func TestBuildTopPicks_AllBlocked(t *testing.T) {
	recommendations := []Recommendation{
		{ID: "b1", Title: "Blocked 1", Status: string(model.StatusOpen), Score: 100.0, BlockedBy: []string{"x"}},
		{ID: "b2", Title: "Blocked 2", Status: string(model.StatusOpen), Score: 90.0, BlockedBy: []string{"y"}},
	}

	picks := buildTopPicks(recommendations, 10, time.Time{}, nil, nil)
	if len(picks) != 0 {
		t.Errorf("expected 0 picks when all are blocked, got %d", len(picks))
	}
}

func TestBuildTopPicks_SkipsBlockedStatusAndAssigned(t *testing.T) {
	recommendations := []Recommendation{
		{
			ID:       "blocked-status",
			Title:    "Blocked status",
			Status:   string(model.StatusBlocked),
			Assignee: "CoralBeaver",
			Score:    100.0,
		},
		{
			ID:       "assigned-open",
			Title:    "Assigned open",
			Status:   string(model.StatusOpen),
			Assignee: "MistyBay",
			Score:    90.0,
		},
		{
			ID:     "open-epic",
			Title:  "Open epic wrapper",
			Type:   string(model.TypeEpic),
			Status: string(model.StatusOpen),
			Score:  80.0,
		},
		{
			ID:     "ready-open",
			Title:  "Ready open",
			Type:   string(model.TypeTask),
			Status: string(model.StatusOpen),
			Score:  10.0,
		},
	}

	picks := buildTopPicks(recommendations, 3, time.Time{}, nil, nil)
	if len(picks) != 1 {
		t.Fatalf("expected only the claimable open issue, got %d picks: %#v", len(picks), picks)
	}
	if picks[0].ID != "ready-open" {
		t.Fatalf("expected ready-open top pick, got %q", picks[0].ID)
	}
}

// TestComputeTriage_NotReadyLabelExcludedFromTopPicks covers issue #173: an
// open, unblocked, non-epic leaf carrying a configured not-ready label must not
// be the claimable top pick, while behavior is unchanged when no labels are
// configured.
func TestComputeTriage_NotReadyLabelExcludedFromTopPicks(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "RAW-1",
			Title:     "Raw reopened leaf (not ready)",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Labels:    []string{"needs-polish"},
			UpdatedAt: now,
		},
		{
			ID:        "READY-1",
			Title:     "Ready leaf",
			Status:    model.StatusOpen,
			Priority:  4,
			IssueType: model.TypeTask,
			UpdatedAt: now,
		},
	}

	// Default (no not-ready labels): RAW-1 is the higher-priority top pick.
	base := ComputeTriageWithOptionsAndTime(issues, TriageOptions{WaitForPhase2: true}, now)
	if len(base.QuickRef.TopPicks) == 0 || base.QuickRef.TopPicks[0].ID != "RAW-1" {
		t.Fatalf("default: expected RAW-1 as top pick, got %#v", base.QuickRef.TopPicks)
	}

	// With needs-polish configured (case-insensitive): RAW-1 is excluded.
	got := ComputeTriageWithOptionsAndTime(issues, TriageOptions{
		WaitForPhase2:  true,
		NotReadyLabels: []string{"NEEDS-POLISH"},
	}, now)
	if len(got.QuickRef.TopPicks) == 0 {
		t.Fatalf("expected READY-1 top pick, got none")
	}
	if got.QuickRef.TopPicks[0].ID != "READY-1" {
		t.Fatalf("expected READY-1 top pick after excluding needs-polish, got %q", got.QuickRef.TopPicks[0].ID)
	}
	for _, pick := range got.QuickRef.TopPicks {
		if pick.ID == "RAW-1" {
			t.Fatalf("RAW-1 (needs-polish) must not appear in claimable top picks")
		}
	}
}

// TestComputeTriage_ParentWithOpenChildrenNotTopPick covers issue #17 parity:
// a non-epic parent with an OPEN child must not be the claimable top pick (the
// epic-type gate alone misses non-epic parents). It remains actionable in the
// count (beads_viewer#158 contract) and re-becomes claimable once its child
// closes.
func TestComputeTriage_ParentWithOpenChildrenNotTopPick(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	parent := model.Issue{
		ID:        "PARENT-1",
		Title:     "Non-epic parent",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		UpdatedAt: now,
	}
	child := model.Issue{
		ID:        "PARENT-1.1",
		Title:     "Open child",
		Status:    model.StatusOpen,
		Priority:  2,
		IssueType: model.TypeTask,
		UpdatedAt: now,
		Dependencies: []*model.Dependency{
			{DependsOnID: "PARENT-1", Type: model.DepParentChild},
		},
	}

	openChild := ComputeTriageWithOptionsAndTime([]model.Issue{parent, child}, TriageOptions{WaitForPhase2: true}, now)
	// Both remain in the actionable count (rollup edge doesn't gate readiness).
	if openChild.QuickRef.ActionableCount != 2 {
		t.Fatalf("expected both parent and child actionable, got %d", openChild.QuickRef.ActionableCount)
	}
	if len(openChild.QuickRef.TopPicks) == 0 || openChild.QuickRef.TopPicks[0].ID != "PARENT-1.1" {
		t.Fatalf("expected child PARENT-1.1 as claimable top pick, got %#v", openChild.QuickRef.TopPicks)
	}
	for _, pick := range openChild.QuickRef.TopPicks {
		if pick.ID == "PARENT-1" {
			t.Fatalf("parent with open children must not be a claimable top pick")
		}
	}

	// Close the child: the parent becomes claimable again.
	child.Status = model.StatusClosed
	closedChild := ComputeTriageWithOptionsAndTime([]model.Issue{parent, child}, TriageOptions{WaitForPhase2: true}, now)
	if len(closedChild.QuickRef.TopPicks) == 0 || closedChild.QuickRef.TopPicks[0].ID != "PARENT-1" {
		t.Fatalf("expected PARENT-1 claimable after child closed, got %#v", closedChild.QuickRef.TopPicks)
	}
}

// bv-140: Tests for extractDescendantSubgraph and RootIssueID triage scoping

func TestExtractDescendantSubgraph_Basic(t *testing.T) {
	issues := []model.Issue{
		{ID: "epic-1", Title: "Epic", Status: model.StatusOpen},
		{ID: "task-a", Title: "Task A", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "task-a", DependsOnID: "epic-1", Type: model.DepBlocks}}},
		{ID: "task-b", Title: "Task B", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "task-b", DependsOnID: "task-a", Type: model.DepBlocks}}},
		{ID: "unrelated", Title: "Unrelated", Status: model.StatusOpen},
	}

	result := extractDescendantSubgraph(issues, "epic-1")
	if len(result) != 3 {
		t.Fatalf("expected 3 issues (epic-1 + task-a + task-b), got %d", len(result))
	}

	ids := make(map[string]bool)
	for _, iss := range result {
		ids[iss.ID] = true
	}
	for _, expected := range []string{"epic-1", "task-a", "task-b"} {
		if !ids[expected] {
			t.Errorf("expected %s in subgraph, not found", expected)
		}
	}
	if ids["unrelated"] {
		t.Errorf("unrelated issue should not be in subgraph")
	}
}

func TestExtractDescendantSubgraph_RootOnly(t *testing.T) {
	issues := []model.Issue{
		{ID: "solo", Title: "Solo Issue", Status: model.StatusOpen},
		{ID: "other", Title: "Other Issue", Status: model.StatusOpen},
	}
	result := extractDescendantSubgraph(issues, "solo")
	if len(result) != 1 || result[0].ID != "solo" {
		t.Errorf("expected only the root issue, got %d issues", len(result))
	}
}

func TestExtractDescendantSubgraph_NonexistentRoot(t *testing.T) {
	issues := []model.Issue{
		{ID: "a", Title: "A", Status: model.StatusOpen},
	}
	result := extractDescendantSubgraph(issues, "nonexistent")
	if result != nil {
		t.Errorf("expected nil for nonexistent root, got %d issues", len(result))
	}
}

func TestComputeTriageWithOptions_RootIssueID(t *testing.T) {
	issues := []model.Issue{
		{ID: "epic-1", Title: "Epic", Status: model.StatusOpen},
		{ID: "task-a", Title: "Task A", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "task-a", DependsOnID: "epic-1", Type: model.DepBlocks}}},
		{ID: "task-b", Title: "Task B", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "task-b", DependsOnID: "task-a", Type: model.DepBlocks}}},
		{ID: "unrelated", Title: "Unrelated", Status: model.StatusOpen},
	}

	// Without scoping: all 4 issues counted
	fullTriage := ComputeTriageWithOptions(issues, TriageOptions{})
	if fullTriage.ProjectHealth.Counts.Total != 4 {
		t.Errorf("expected 4 total issues without scoping, got %d", fullTriage.ProjectHealth.Counts.Total)
	}

	// With scoping to epic-1: only 3 issues (epic-1 + task-a + task-b)
	scopedTriage := ComputeTriageWithOptions(issues, TriageOptions{RootIssueID: "epic-1"})
	if scopedTriage.ProjectHealth.Counts.Total != 3 {
		t.Errorf("expected 3 total issues with RootIssueID=epic-1, got %d", scopedTriage.ProjectHealth.Counts.Total)
	}
}
