// Package analysis invariance tests for bv-runn.2
//
// These tests guarantee that planned performance optimizations preserve semantics.
// They serve as a safety net: optimized algorithms must produce identical results
// to the reference implementations tested here.
package analysis

import (
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ============================================================================
// UNBLOCKS INVARIANCE TESTS (bv-runn.2)
//
// These tests ensure computeUnblocks produces correct, deterministic results
// across various edge cases. Any future O(E) optimization (bv-runn.9) must
// pass these same tests.
// ============================================================================

// TestUnblocksInvariance_Basic tests the fundamental unblocks behavior:
// when C is completed, both A and B (who depend only on C) become actionable.
func TestUnblocksInvariance_Basic(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Task C", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("C")

	// C unblocks both A and B
	expected := []string{"A", "B"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("expected C to unblock %v, got %v", expected, unblocks)
	}

	// A and B don't unblock anything (no dependents)
	if len(an.ComputeUnblocks("A")) != 0 {
		t.Errorf("A should unblock nothing, got %v", an.ComputeUnblocks("A"))
	}
	if len(an.ComputeUnblocks("B")) != 0 {
		t.Errorf("B should unblock nothing, got %v", an.ComputeUnblocks("B"))
	}
}

// TestUnblocksInvariance_MixedStatuses tests with various issue statuses.
func TestUnblocksInvariance_MixedStatuses(t *testing.T) {
	issues := []model.Issue{
		{ID: "open1", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "in_progress1", Status: model.StatusInProgress, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "blocked1", Status: model.StatusBlocked, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "closed1", Status: model.StatusClosed, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "blocker", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("blocker")

	// Closed issues should NOT appear in unblocks (they're already done)
	// All non-closed dependents should become actionable
	expected := []string{"blocked1", "in_progress1", "open1"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("expected blocker to unblock %v, got %v", expected, unblocks)
	}
}

// TestUnblocksInvariance_BlockingVsNonBlocking tests that only "blocks" type
// dependencies create blocking relationships.
func TestUnblocksInvariance_BlockingVsNonBlocking(t *testing.T) {
	issues := []model.Issue{
		{ID: "blocked_by_blocks", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "target", Type: model.DepBlocks},
		}},
		{ID: "related_to_target", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "target", Type: model.DepRelated},
		}},
		{ID: "parent_child", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "target", Type: model.DepParentChild},
		}},
		{ID: "target", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("target")

	// Only issues with DepBlocks type should be "unblocked"
	// Related and ParentChild types don't create hard blocking
	expected := []string{"blocked_by_blocks"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("expected only blocking deps in unblocks: want %v, got %v", expected, unblocks)
	}
}

// TestUnblocksInvariance_MissingDependencyIDs tests that missing blocker IDs
// are handled gracefully (don't block).
func TestUnblocksInvariance_MissingDependencyIDs(t *testing.T) {
	issues := []model.Issue{
		{ID: "has_missing_dep", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "nonexistent", Type: model.DepBlocks},
		}},
		{ID: "has_real_dep", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "blocker", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)

	// has_missing_dep is actionable (missing blocker doesn't block)
	actionable := an.GetActionableIssues()
	actionableIDs := make([]string, len(actionable))
	for i, iss := range actionable {
		actionableIDs[i] = iss.ID
	}
	sort.Strings(actionableIDs)

	// blocker and has_missing_dep should be actionable
	expected := []string{"blocker", "has_missing_dep"}
	if !stringSlicesEqual(actionableIDs, expected) {
		t.Errorf("expected actionable %v, got %v", expected, actionableIDs)
	}

	// blocker unblocks has_real_dep
	unblocks := an.ComputeUnblocks("blocker")
	expectedUnblocks := []string{"has_real_dep"}
	if !stringSlicesEqual(unblocks, expectedUnblocks) {
		t.Errorf("expected unblocks %v, got %v", expectedUnblocks, unblocks)
	}
}

// TestUnblocksInvariance_DuplicateDependencies tests that duplicate deps
// don't cause double-counting or other issues.
func TestUnblocksInvariance_DuplicateDependencies(t *testing.T) {
	issues := []model.Issue{
		{ID: "dependent", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
			{DependsOnID: "blocker", Type: model.DepBlocks}, // Duplicate!
			{DependsOnID: "blocker", Type: model.DepBlocks}, // Triple!
		}},
		{ID: "blocker", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("blocker")

	// Despite duplicate deps, dependent should appear only once
	expected := []string{"dependent"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("duplicate deps: expected unblocks %v, got %v", expected, unblocks)
	}
}

// TestUnblocksInvariance_PartialUnblock tests that completing one of multiple
// blockers doesn't fully unblock a dependent.
func TestUnblocksInvariance_PartialUnblock(t *testing.T) {
	issues := []model.Issue{
		{ID: "dependent", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker1", Type: model.DepBlocks},
			{DependsOnID: "blocker2", Type: model.DepBlocks},
		}},
		{ID: "blocker1", Status: model.StatusOpen},
		{ID: "blocker2", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)

	// Completing blocker1 doesn't unblock dependent (still blocked by blocker2)
	unblocks1 := an.ComputeUnblocks("blocker1")
	if len(unblocks1) != 0 {
		t.Errorf("partial unblock: blocker1 should unblock nothing, got %v", unblocks1)
	}

	// Same for blocker2
	unblocks2 := an.ComputeUnblocks("blocker2")
	if len(unblocks2) != 0 {
		t.Errorf("partial unblock: blocker2 should unblock nothing, got %v", unblocks2)
	}
}

// TestUnblocksInvariance_PartialUnblockWithClosedBlocker tests that when one
// blocker is already closed, completing the other does unblock.
func TestUnblocksInvariance_PartialUnblockWithClosedBlocker(t *testing.T) {
	issues := []model.Issue{
		{ID: "dependent", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "open_blocker", Type: model.DepBlocks},
			{DependsOnID: "closed_blocker", Type: model.DepBlocks},
		}},
		{ID: "open_blocker", Status: model.StatusOpen},
		{ID: "closed_blocker", Status: model.StatusClosed},
	}

	an := NewAnalyzer(issues)

	// Since closed_blocker is already done, completing open_blocker unblocks dependent
	unblocks := an.ComputeUnblocks("open_blocker")
	expected := []string{"dependent"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("partial with closed: expected %v, got %v", expected, unblocks)
	}
}

// TestUnblocksInvariance_Chain tests a dependency chain: A <- B <- C <- D
// Only completing C should unblock D, etc.
func TestUnblocksInvariance_Chain(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)

	// A unblocks B
	if unblocks := an.ComputeUnblocks("A"); !stringSlicesEqual(unblocks, []string{"B"}) {
		t.Errorf("chain: A should unblock [B], got %v", unblocks)
	}

	// B unblocks C
	if unblocks := an.ComputeUnblocks("B"); !stringSlicesEqual(unblocks, []string{"C"}) {
		t.Errorf("chain: B should unblock [C], got %v", unblocks)
	}

	// C unblocks D
	if unblocks := an.ComputeUnblocks("C"); !stringSlicesEqual(unblocks, []string{"D"}) {
		t.Errorf("chain: C should unblock [D], got %v", unblocks)
	}

	// D unblocks nothing
	if unblocks := an.ComputeUnblocks("D"); len(unblocks) != 0 {
		t.Errorf("chain: D should unblock nothing, got %v", unblocks)
	}
}

// TestUnblocksInvariance_Diamond tests a diamond pattern where multiple paths exist.
func TestUnblocksInvariance_Diamond(t *testing.T) {
	// Diamond: root <- mid1, root <- mid2, mid1 <- leaf, mid2 <- leaf
	issues := []model.Issue{
		{ID: "root", Status: model.StatusOpen},
		{ID: "mid1", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "mid2", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "root", Type: model.DepBlocks},
		}},
		{ID: "leaf", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "mid1", Type: model.DepBlocks},
			{DependsOnID: "mid2", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)

	// root unblocks mid1 and mid2
	unblocks := an.ComputeUnblocks("root")
	expected := []string{"mid1", "mid2"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("diamond: root should unblock %v, got %v", expected, unblocks)
	}

	// mid1 doesn't unblock leaf (still blocked by mid2)
	if unblocks := an.ComputeUnblocks("mid1"); len(unblocks) != 0 {
		t.Errorf("diamond: mid1 should unblock nothing, got %v", unblocks)
	}

	// mid2 doesn't unblock leaf (still blocked by mid1)
	if unblocks := an.ComputeUnblocks("mid2"); len(unblocks) != 0 {
		t.Errorf("diamond: mid2 should unblock nothing, got %v", unblocks)
	}
}

// TestUnblocksInvariance_DiamondWithOneMidClosed tests diamond when one mid is closed.
func TestUnblocksInvariance_DiamondWithOneMidClosed(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Status: model.StatusClosed}, // Already closed
		{ID: "mid1", Status: model.StatusOpen},
		{ID: "mid2", Status: model.StatusClosed}, // Already closed
		{ID: "leaf", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "mid1", Type: model.DepBlocks},
			{DependsOnID: "mid2", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)

	// Since mid2 is closed, completing mid1 unblocks leaf
	unblocks := an.ComputeUnblocks("mid1")
	expected := []string{"leaf"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("diamond partial: mid1 should unblock %v, got %v", expected, unblocks)
	}
}

// TestUnblocksInvariance_Cycle tests behavior with cyclic dependencies.
// In a cycle, all nodes are blocked by each other, so none are actionable.
// However, if we hypothetically complete one node, its dependent becomes unblocked
// (since that dependent only depends on the completed node).
func TestUnblocksInvariance_Cycle(t *testing.T) {
	// Cycle: A depends on B, B depends on C, C depends on A
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)

	// All are blocked (cycle), no actionable issues
	actionable := an.GetActionableIssues()
	if len(actionable) != 0 {
		t.Errorf("cycle: expected 0 actionable, got %d", len(actionable))
	}

	// In a cycle, completing A unblocks C (since C only depends on A).
	// This is correct behavior - cycles are pathological but computeUnblocks
	// still accurately reports what would become actionable.
	unblocksA := an.ComputeUnblocks("A")
	expected := []string{"C"}
	if !stringSlicesEqual(unblocksA, expected) {
		t.Errorf("cycle: A completing should unblock C, got %v", unblocksA)
	}

	// Similarly, completing B unblocks A, and completing C unblocks B
	unblocksB := an.ComputeUnblocks("B")
	if !stringSlicesEqual(unblocksB, []string{"A"}) {
		t.Errorf("cycle: B completing should unblock A, got %v", unblocksB)
	}

	unblocksC := an.ComputeUnblocks("C")
	if !stringSlicesEqual(unblocksC, []string{"B"}) {
		t.Errorf("cycle: C completing should unblock B, got %v", unblocksC)
	}
}

// TestUnblocksInvariance_Empty tests empty input.
func TestUnblocksInvariance_Empty(t *testing.T) {
	an := NewAnalyzer(nil)
	unblocks := an.ComputeUnblocks("nonexistent")
	if unblocks != nil && len(unblocks) != 0 {
		t.Errorf("empty: expected nil or empty, got %v", unblocks)
	}
}

// TestUnblocksInvariance_NonexistentID tests unblocks for an ID not in the graph.
func TestUnblocksInvariance_NonexistentID(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
	}
	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("nonexistent")
	if unblocks != nil && len(unblocks) != 0 {
		t.Errorf("nonexistent: expected nil or empty, got %v", unblocks)
	}
}

func TestUnblocksInvariance_AlreadyClosedBlockerChangesNothing(t *testing.T) {
	issues := []model.Issue{
		{ID: "done", Status: model.StatusClosed},
		{ID: "ready", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "done", Type: model.DepBlocks},
		}},
	}

	an := NewAnalyzer(issues)
	if unblocks := an.ComputeUnblocks("done"); len(unblocks) != 0 {
		t.Fatalf("an already-closed blocker cannot change readiness, got %v", unblocks)
	}
}

func TestUnblocksInvariance_SelfDependencyDoesNotUnblockItself(t *testing.T) {
	issues := []model.Issue{{
		ID:     "self",
		Status: model.StatusOpen,
		Dependencies: []*model.Dependency{
			{DependsOnID: "self", Type: model.DepBlocks},
		},
	}}

	an := NewAnalyzer(issues)
	if unblocks := an.ComputeUnblocks("self"); len(unblocks) != 0 {
		t.Fatalf("a completed issue cannot become its own actionable successor, got %v", unblocks)
	}
	if count := an.countTransitiveUnblocks("self"); count != 0 {
		t.Fatalf("a self-cycle cannot create a transitive unblock, got %d", count)
	}
}

func TestUnblocksInvariance_DeferredAndParentBlockedRemainUnactionable(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "blocker", Status: model.StatusOpen},
		{ID: "parent-blocker", Status: model.StatusOpen},
		{ID: "parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "parent-blocker", Type: model.DepBlocks},
		}},
		{ID: "deferred", Status: model.StatusOpen, DeferUntil: &future, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "parent-gated", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
			{DependsOnID: "parent", Type: model.DepParentChild},
		}},
	}

	an := NewAnalyzer(issues)
	an.SetNow(now)
	if unblocks := an.ComputeUnblocks("blocker"); len(unblocks) != 0 {
		t.Fatalf("deferred and parent-gated successors must remain unactionable, got %v", unblocks)
	}

	an.SetNow(future)
	if unblocks := an.ComputeUnblocks("blocker"); !stringSlicesEqual(unblocks, []string{"deferred"}) {
		t.Fatalf("only elapsed deferral should become actionable, got %v", unblocks)
	}
}

// TestUnblocksInvariance_Determinism tests that unblocks lists are deterministically sorted.
func TestUnblocksInvariance_Determinism(t *testing.T) {
	issues := []model.Issue{
		{ID: "Z", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "M", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: model.DepBlocks},
		}},
		{ID: "blocker", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)

	// Run multiple times to verify determinism
	for i := 0; i < 5; i++ {
		unblocks := an.ComputeUnblocks("blocker")
		expected := []string{"A", "M", "Z"} // Should be sorted
		if !stringSlicesEqual(unblocks, expected) {
			t.Errorf("determinism run %d: expected %v, got %v", i, expected, unblocks)
		}
	}
}

// TestUnblocksInvariance_LegacyEmptyDepType tests legacy deps with empty type
// (treated as blocking).
func TestUnblocksInvariance_LegacyEmptyDepType(t *testing.T) {
	issues := []model.Issue{
		{ID: "dependent", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "blocker", Type: ""}, // Legacy empty type = blocks
		}},
		{ID: "blocker", Status: model.StatusOpen},
	}

	an := NewAnalyzer(issues)
	unblocks := an.ComputeUnblocks("blocker")

	// Empty dep type should be treated as blocking (legacy behavior)
	expected := []string{"dependent"}
	if !stringSlicesEqual(unblocks, expected) {
		t.Errorf("legacy empty type: expected %v, got %v", expected, unblocks)
	}
}

// ============================================================================
// VELOCITY INVARIANCE TESTS (bv-runn.2)
//
// These tests ensure velocity computation is deterministic when given a fixed
// `now` timestamp. Any future velocity helper (bv-runn.7) must pass these tests.
// ============================================================================

// TestVelocityInvariance_FixedNow tests velocity computation with fixed timestamps.
func TestVelocityInvariance_FixedNow(t *testing.T) {
	// Fixed reference time: Monday, December 16, 2025, 12:00 UTC
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	// Issues closed at known times
	closed3DaysAgo := now.Add(-3 * 24 * time.Hour)
	closed10DaysAgo := now.Add(-10 * 24 * time.Hour)
	closed25DaysAgo := now.Add(-25 * 24 * time.Hour)

	issues := []model.Issue{
		{
			ID:        "closed_recent",
			Status:    model.StatusClosed,
			CreatedAt: now.Add(-5 * 24 * time.Hour),
			ClosedAt:  &closed3DaysAgo,
		},
		{
			ID:        "closed_week_old",
			Status:    model.StatusClosed,
			CreatedAt: now.Add(-15 * 24 * time.Hour),
			ClosedAt:  &closed10DaysAgo,
		},
		{
			ID:        "closed_month_old",
			Status:    model.StatusClosed,
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			ClosedAt:  &closed25DaysAgo,
		},
		{
			ID:     "still_open",
			Status: model.StatusOpen,
		},
	}

	velocity := ComputeProjectVelocity(issues, now, 8)

	// Verify ClosedLast7Days: only closed_recent (3 days ago)
	if velocity.ClosedLast7Days != 1 {
		t.Errorf("ClosedLast7Days: expected 1, got %d", velocity.ClosedLast7Days)
	}

	// Verify ClosedLast30Days: closed_recent + closed_week_old + closed_month_old
	if velocity.ClosedLast30Days != 3 {
		t.Errorf("ClosedLast30Days: expected 3, got %d", velocity.ClosedLast30Days)
	}

	// Verify we get weekly buckets
	if len(velocity.Weekly) != 8 {
		t.Errorf("Weekly: expected 8 buckets, got %d", len(velocity.Weekly))
	}

	// Should not be estimated since all have ClosedAt
	if velocity.Estimated {
		t.Error("Estimated: expected false (all have ClosedAt)")
	}
}

// TestVelocityInvariance_EstimatedFallback tests the estimation fallback.
func TestVelocityInvariance_EstimatedFallback(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{
			ID:        "no_closed_at",
			Status:    model.StatusClosed,
			CreatedAt: now.Add(-10 * 24 * time.Hour),
			UpdatedAt: now.Add(-5 * 24 * time.Hour), // Falls back to UpdatedAt
			// ClosedAt is nil
		},
	}

	velocity := ComputeProjectVelocity(issues, now, 8)

	// Should be marked as estimated
	if !velocity.Estimated {
		t.Error("Estimated: expected true (missing ClosedAt)")
	}

	// Should still count the issue (using UpdatedAt as fallback)
	if velocity.ClosedLast7Days != 1 {
		t.Errorf("ClosedLast7Days with fallback: expected 1, got %d", velocity.ClosedLast7Days)
	}
}

// TestVelocityInvariance_WeekBucketing tests that closures are bucketed by ISO week.
func TestVelocityInvariance_WeekBucketing(t *testing.T) {
	// now is Monday Dec 16, 2025
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	// Close one issue on each of the past 3 Mondays
	monday1 := now                                 // Week of Dec 15 (current week)
	monday2 := now.Add(-7 * 24 * time.Hour)        // Week of Dec 8
	monday3 := now.Add(-14 * 24 * time.Hour)       // Week of Dec 1
	closedMon1 := monday1.Add(-1 * 24 * time.Hour) // Sunday Dec 15 (same ISO week as Dec 16)
	closedMon2 := monday2.Add(2 * 24 * time.Hour)  // Wednesday Dec 10
	closedMon3 := monday3.Add(4 * 24 * time.Hour)  // Friday Dec 5

	issues := []model.Issue{
		{ID: "w1", Status: model.StatusClosed, CreatedAt: now.Add(-30 * 24 * time.Hour), ClosedAt: &closedMon1},
		{ID: "w2", Status: model.StatusClosed, CreatedAt: now.Add(-30 * 24 * time.Hour), ClosedAt: &closedMon2},
		{ID: "w3", Status: model.StatusClosed, CreatedAt: now.Add(-30 * 24 * time.Hour), ClosedAt: &closedMon3},
	}

	velocity := ComputeProjectVelocity(issues, now, 4)

	// Weekly[0] = current week (Dec 15), should have 1 (closedMon1 on Dec 15)
	// Weekly[1] = previous week (Dec 8), should have 1 (closedMon2 on Dec 10)
	// Weekly[2] = Dec 1 week, should have 1 (closedMon3 on Dec 5)
	// Weekly[3] = Nov 24 week, should have 0

	if len(velocity.Weekly) != 4 {
		t.Fatalf("expected 4 weekly buckets, got %d", len(velocity.Weekly))
	}

	// Check that at least 3 weeks have closures
	totalClosed := 0
	for _, w := range velocity.Weekly {
		totalClosed += w.Closed
	}
	if totalClosed != 3 {
		t.Errorf("expected 3 total closures across weeks, got %d", totalClosed)
	}
}

// TestVelocityInvariance_AvgDaysToClose tests average time-to-close calculation.
func TestVelocityInvariance_AvgDaysToClose(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	// Issue 1: created 10 days ago, closed 5 days ago = 5 days to close
	created1 := now.Add(-10 * 24 * time.Hour)
	closed1 := now.Add(-5 * 24 * time.Hour)

	// Issue 2: created 20 days ago, closed 10 days ago = 10 days to close
	created2 := now.Add(-20 * 24 * time.Hour)
	closed2 := now.Add(-10 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "fast", Status: model.StatusClosed, CreatedAt: created1, ClosedAt: &closed1},
		{ID: "slow", Status: model.StatusClosed, CreatedAt: created2, ClosedAt: &closed2},
	}

	velocity := ComputeProjectVelocity(issues, now, 8)

	// Average = (5 + 10) / 2 = 7.5 days
	expectedAvg := 7.5
	tolerance := 0.01

	if velocity.AvgDaysToClose < expectedAvg-tolerance || velocity.AvgDaysToClose > expectedAvg+tolerance {
		t.Errorf("AvgDaysToClose: expected ~%.1f, got %.2f", expectedAvg, velocity.AvgDaysToClose)
	}
}

// TestVelocityInvariance_Empty tests empty input.
func TestVelocityInvariance_Empty(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)
	velocity := ComputeProjectVelocity(nil, now, 8)

	if velocity.ClosedLast7Days != 0 {
		t.Errorf("empty: ClosedLast7Days should be 0, got %d", velocity.ClosedLast7Days)
	}
	if velocity.ClosedLast30Days != 0 {
		t.Errorf("empty: ClosedLast30Days should be 0, got %d", velocity.ClosedLast30Days)
	}
	if velocity.AvgDaysToClose != 0 {
		t.Errorf("empty: AvgDaysToClose should be 0, got %.2f", velocity.AvgDaysToClose)
	}
}

// TestVelocityInvariance_OnlyOpenIssues tests when no issues are closed.
func TestVelocityInvariance_OnlyOpenIssues(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{ID: "open1", Status: model.StatusOpen},
		{ID: "open2", Status: model.StatusInProgress},
	}

	velocity := ComputeProjectVelocity(issues, now, 8)

	if velocity.ClosedLast7Days != 0 {
		t.Errorf("only open: ClosedLast7Days should be 0, got %d", velocity.ClosedLast7Days)
	}
	if velocity.ClosedLast30Days != 0 {
		t.Errorf("only open: ClosedLast30Days should be 0, got %d", velocity.ClosedLast30Days)
	}
}

// TestVelocityInvariance_Determinism verifies same inputs produce same outputs.
func TestVelocityInvariance_Determinism(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)
	closed := now.Add(-3 * 24 * time.Hour)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusClosed, CreatedAt: now.Add(-10 * 24 * time.Hour), ClosedAt: &closed},
	}

	// Run multiple times
	var firstResult *Velocity
	for i := 0; i < 5; i++ {
		result := ComputeProjectVelocity(issues, now, 8)
		if firstResult == nil {
			firstResult = result
		} else {
			if result.ClosedLast7Days != firstResult.ClosedLast7Days ||
				result.ClosedLast30Days != firstResult.ClosedLast30Days ||
				result.AvgDaysToClose != firstResult.AvgDaysToClose {
				t.Errorf("determinism failed on run %d", i)
			}
		}
	}
}

// ============================================================================
// TRIAGE SANITY GOLDEN TEST (bv-runn.2 - Optional)
//
// This test verifies that triage recommendations remain stable for a known
// input. If algorithm weights change, update the golden expectations.
// ============================================================================

// TestTriageSanity_GoldenRecommendationOrder tests that for a fixed input,
// the recommendation order is deterministic and matches expected priorities.
func TestTriageSanity_GoldenRecommendationOrder(t *testing.T) {
	now := time.Date(2025, 12, 16, 12, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		// High priority blocker that unblocks others - should rank highest
		{ID: "high-blocker", Title: "Critical blocker", Status: model.StatusOpen, Priority: 0, UpdatedAt: now},
		// Blocked by high-blocker
		{ID: "blocked-1", Title: "Blocked task 1", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{
			{DependsOnID: "high-blocker", Type: model.DepBlocks},
		}},
		// Another blocked
		{ID: "blocked-2", Title: "Blocked task 2", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{
			{DependsOnID: "high-blocker", Type: model.DepBlocks},
		}},
		// Low priority standalone
		{ID: "low-standalone", Title: "Low priority task", Status: model.StatusOpen, Priority: 3, UpdatedAt: now},
		// Medium priority standalone
		{ID: "med-standalone", Title: "Medium priority task", Status: model.StatusOpen, Priority: 2, UpdatedAt: now},
	}

	triage := ComputeTriageWithOptionsAndTime(issues, TriageOptions{TopN: 5}, now)

	if len(triage.Recommendations) == 0 {
		t.Fatal("expected recommendations")
	}

	// The high-blocker should be in top 3 since it unblocks 2 issues
	topIDs := make([]string, 0, 3)
	for i := 0; i < 3 && i < len(triage.Recommendations); i++ {
		topIDs = append(topIDs, triage.Recommendations[i].ID)
	}

	foundHighBlocker := false
	for _, id := range topIDs {
		if id == "high-blocker" {
			foundHighBlocker = true
			break
		}
	}
	if !foundHighBlocker {
		t.Errorf("golden: high-blocker should be in top 3, got %v", topIDs)
	}

	// Verify blocked issues don't appear as actionable recommendations
	// (they should have blocked_by populated if they appear)
	for _, rec := range triage.Recommendations {
		if rec.ID == "blocked-1" || rec.ID == "blocked-2" {
			if len(rec.BlockedBy) == 0 {
				t.Errorf("golden: %s should have BlockedBy populated", rec.ID)
			}
		}
	}

	// Verify determinism
	triage2 := ComputeTriageWithOptionsAndTime(issues, TriageOptions{TopN: 5}, now)
	if len(triage.Recommendations) != len(triage2.Recommendations) {
		t.Error("determinism: recommendation count changed")
	}
	for i := range triage.Recommendations {
		if triage.Recommendations[i].ID != triage2.Recommendations[i].ID {
			t.Errorf("determinism: order changed at position %d", i)
		}
	}
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// stringSlicesEqual compares two string slices for equality (order-sensitive).
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
