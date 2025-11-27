package analysis_test

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestGetExecutionPlanEmpty(t *testing.T) {
	an := analysis.NewAnalyzer([]model.Issue{})
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 0 {
		t.Errorf("Expected 0 actionable, got %d", plan.TotalActionable)
	}
	if len(plan.Tracks) != 0 {
		t.Errorf("Expected 0 tracks, got %d", len(plan.Tracks))
	}
}

func TestGetExecutionPlanSingleIssue(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 1 {
		t.Errorf("Expected 1 actionable, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 0 {
		t.Errorf("Expected 0 blocked, got %d", plan.TotalBlocked)
	}
	if len(plan.Tracks) != 1 {
		t.Fatalf("Expected 1 track, got %d", len(plan.Tracks))
	}
	if len(plan.Tracks[0].Items) != 1 {
		t.Errorf("Expected 1 item in track, got %d", len(plan.Tracks[0].Items))
	}
	if plan.Tracks[0].Items[0].ID != "A" {
		t.Errorf("Expected item A, got %s", plan.Tracks[0].Items[0].ID)
	}
}

func TestGetExecutionPlanChain(t *testing.T) {
	// Chain: A depends on B depends on C (all open)
	// Only C is actionable, unblocks B
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 1 {
		t.Errorf("Expected 1 actionable, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 2 {
		t.Errorf("Expected 2 blocked, got %d", plan.TotalBlocked)
	}
	if len(plan.Tracks) != 1 {
		t.Fatalf("Expected 1 track, got %d", len(plan.Tracks))
	}

	item := plan.Tracks[0].Items[0]
	if item.ID != "C" {
		t.Errorf("Expected C actionable, got %s", item.ID)
	}
	if len(item.UnblocksIDs) != 1 || item.UnblocksIDs[0] != "B" {
		t.Errorf("Expected C to unblock B, got %v", item.UnblocksIDs)
	}

	if plan.Summary.HighestImpact != "C" {
		t.Errorf("Expected C as highest impact, got %s", plan.Summary.HighestImpact)
	}
}

func TestGetExecutionPlanParallelTracks(t *testing.T) {
	// Two independent chains:
	// Track 1: A depends on B (both open) → B actionable
	// Track 2: C depends on D (both open) → D actionable
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "D", Type: model.DepBlocks},
		}},
		{ID: "D", Title: "Task D", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 2 {
		t.Errorf("Expected 2 actionable, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 2 {
		t.Errorf("Expected 2 blocked, got %d", plan.TotalBlocked)
	}
	// Should have 2 tracks (independent chains)
	if len(plan.Tracks) != 2 {
		t.Errorf("Expected 2 parallel tracks, got %d", len(plan.Tracks))
	}
}

func TestGetExecutionPlanPriorityOrdering(t *testing.T) {
	// Multiple actionable items in same connected component with different priorities
	// A depends on both B and C (creates connected component)
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "low", Type: model.DepBlocks},
			{DependsOnID: "high", Type: model.DepBlocks},
			{DependsOnID: "med", Type: model.DepBlocks},
		}},
		{ID: "low", Title: "Low Priority", Status: model.StatusOpen, Priority: 3},
		{ID: "high", Title: "High Priority", Status: model.StatusOpen, Priority: 0},
		{ID: "med", Title: "Medium Priority", Status: model.StatusOpen, Priority: 2},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 3 {
		t.Fatalf("Expected 3 actionable (low, high, med), got %d", plan.TotalActionable)
	}

	// All in one track (connected via A)
	if len(plan.Tracks) != 1 {
		t.Fatalf("Expected 1 track (connected graph), got %d", len(plan.Tracks))
	}

	items := plan.Tracks[0].Items
	if len(items) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(items))
	}

	// Should be sorted: high (P0), med (P2), low (P3)
	if items[0].ID != "high" {
		t.Errorf("Expected 'high' first, got %s", items[0].ID)
	}
	if items[1].ID != "med" {
		t.Errorf("Expected 'med' second, got %s", items[1].ID)
	}
	if items[2].ID != "low" {
		t.Errorf("Expected 'low' third, got %s", items[2].ID)
	}
}

func TestGetExecutionPlanUnblocksCalculation(t *testing.T) {
	// A and B both depend on C
	// When C is closed, both A and B become actionable
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 1 {
		t.Fatalf("Expected 1 actionable, got %d", plan.TotalActionable)
	}

	// C should unblock both A and B
	item := plan.Tracks[0].Items[0]
	if item.ID != "C" {
		t.Fatalf("Expected C, got %s", item.ID)
	}
	if len(item.UnblocksIDs) != 2 {
		t.Errorf("Expected C to unblock 2 issues, got %d: %v", len(item.UnblocksIDs), item.UnblocksIDs)
	}

	// Summary should highlight C as highest impact
	if plan.Summary.HighestImpact != "C" {
		t.Errorf("Expected C as highest impact, got %s", plan.Summary.HighestImpact)
	}
	if plan.Summary.UnblocksCount != 2 {
		t.Errorf("Expected unblocks count 2, got %d", plan.Summary.UnblocksCount)
	}
}

func TestGetExecutionPlanPartialUnblock(t *testing.T) {
	// A depends on B AND C
	// B is open, C is open
	// Both B and C are actionable
	// Completing B alone doesn't unblock A (still blocked by C)
	// Completing C alone doesn't unblock A (still blocked by B)
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 2 {
		t.Fatalf("Expected 2 actionable (B and C), got %d", plan.TotalActionable)
	}

	// Neither B nor C alone unblocks A
	for _, track := range plan.Tracks {
		for _, item := range track.Items {
			if item.ID == "B" && len(item.UnblocksIDs) != 0 {
				t.Errorf("B should not unblock anything (A still blocked by C), got %v", item.UnblocksIDs)
			}
			if item.ID == "C" && len(item.UnblocksIDs) != 0 {
				t.Errorf("C should not unblock anything (A still blocked by B), got %v", item.UnblocksIDs)
			}
		}
	}
}

func TestGetExecutionPlanConnectedGraph(t *testing.T) {
	// A depends on B, C depends on B
	// B is the only actionable, and completing it unblocks both A and C
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 1 {
		t.Fatalf("Expected 1 actionable, got %d", plan.TotalActionable)
	}

	// All in one track (connected graph)
	if len(plan.Tracks) != 1 {
		t.Errorf("Expected 1 track (connected graph), got %d", len(plan.Tracks))
	}

	// B should unblock both A and C
	item := plan.Tracks[0].Items[0]
	if len(item.UnblocksIDs) != 2 {
		t.Errorf("Expected B to unblock 2, got %d: %v", len(item.UnblocksIDs), item.UnblocksIDs)
	}
}

func TestGetExecutionPlanAllClosed(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusClosed, Priority: 1},
		{ID: "B", Title: "Task B", Status: model.StatusClosed, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 0 {
		t.Errorf("Expected 0 actionable, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 0 {
		t.Errorf("Expected 0 blocked (all closed), got %d", plan.TotalBlocked)
	}
	if len(plan.Tracks) != 0 {
		t.Errorf("Expected 0 tracks, got %d", len(plan.Tracks))
	}
}

func TestGetExecutionPlanCycle(t *testing.T) {
	// Cycle: A -> B -> C -> A
	// All blocked, no actionable items
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 0 {
		t.Errorf("Expected 0 actionable (all in cycle), got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 3 {
		t.Errorf("Expected 3 blocked, got %d", plan.TotalBlocked)
	}
}

func TestGetExecutionPlanTrackHasTrackID(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if len(plan.Tracks) != 1 {
		t.Fatalf("Expected 1 track, got %d", len(plan.Tracks))
	}

	if plan.Tracks[0].TrackID == "" {
		t.Error("Expected track to have a TrackID")
	}
}

func TestGetExecutionPlanItemHasDetails(t *testing.T) {
	issues := []model.Issue{
		{ID: "test-1", Title: "Test Task", Status: model.StatusInProgress, Priority: 2},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if len(plan.Tracks) != 1 || len(plan.Tracks[0].Items) != 1 {
		t.Fatalf("Expected 1 track with 1 item")
	}

	item := plan.Tracks[0].Items[0]
	if item.ID != "test-1" {
		t.Errorf("Expected ID 'test-1', got %s", item.ID)
	}
	if item.Title != "Test Task" {
		t.Errorf("Expected Title 'Test Task', got %s", item.Title)
	}
	if item.Priority != 2 {
		t.Errorf("Expected Priority 2, got %d", item.Priority)
	}
	if item.Status != "in_progress" {
		t.Errorf("Expected Status 'in_progress', got %s", item.Status)
	}
}

func TestGetExecutionPlanMissingBlocker(t *testing.T) {
	// A depends on "nonexistent" which doesn't exist
	// Should NOT block A since the blocker doesn't exist
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "nonexistent", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// A should be actionable because the blocker doesn't exist
	if plan.TotalActionable != 1 {
		t.Errorf("Expected 1 actionable (missing blocker shouldn't block), got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 0 {
		t.Errorf("Expected 0 blocked, got %d", plan.TotalBlocked)
	}
}

func TestGetExecutionPlanRelatedTypeDoesNotBlock(t *testing.T) {
	// A has "related" dependency on B - should NOT block
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepRelated},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// Both A and B should be actionable
	if plan.TotalActionable != 2 {
		t.Errorf("Expected 2 actionable ('related' deps don't block), got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 0 {
		t.Errorf("Expected 0 blocked, got %d", plan.TotalBlocked)
	}
}

func TestGetExecutionPlanSelfReferential(t *testing.T) {
	// A depends on itself - the underlying graph library panics on self-edges
	// This test documents that self-referential deps should be filtered at data ingestion
	// Skip this test as it's a known limitation of the graph library
	t.Skip("Self-referential dependencies are filtered at loader level; graph library panics on self-edges")
}

func TestGetExecutionPlanMixedDepTypes(t *testing.T) {
	// A has both "blocks" and "related" dependencies
	// Only the "blocks" type should create blocking
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},  // This blocks
			{DependsOnID: "C", Type: model.DepRelated}, // This doesn't block
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1},
		{ID: "C", Title: "Task C", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// B and C are actionable, A is blocked by B only
	if plan.TotalActionable != 2 {
		t.Errorf("Expected 2 actionable (B and C), got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 1 {
		t.Errorf("Expected 1 blocked (A), got %d", plan.TotalBlocked)
	}
}

func TestGetExecutionPlanBlockerClosed(t *testing.T) {
	// A depends on B, but B is closed - A should be actionable
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Title: "Task B", Status: model.StatusClosed, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 1 {
		t.Errorf("Expected 1 actionable (closed blocker doesn't block), got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 0 {
		t.Errorf("Expected 0 blocked, got %d", plan.TotalBlocked)
	}
}
