package analysis_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

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

func TestGenerateTrackID_Unbounded(t *testing.T) {
	// Spot-check boundaries: 1 -> A, 26 -> Z, 27 -> AA, 52 -> AZ, 53 -> BA, 702 -> ZZ, 703 -> AAA
	cases := map[int]string{
		1:   "track-A",
		26:  "track-Z",
		27:  "track-AA",
		52:  "track-AZ",
		53:  "track-BA",
		702: "track-ZZ",
		703: "track-AAA",
	}

	for n, expected := range cases {
		if got := analysis.GenerateTrackIDForTest(n); got != expected {
			t.Fatalf("n=%d expected %s got %s", n, expected, got)
		}
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
	// Unknown dependency state must withhold A from an execution plan.
	issues := []model.Issue{
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "nonexistent", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if plan.TotalActionable != 0 {
		t.Errorf("Expected 0 actionable with an unknown blocker, got %d", plan.TotalActionable)
	}
	if plan.TotalBlocked != 1 {
		t.Errorf("Expected 1 withheld issue, got %d", plan.TotalBlocked)
	}
	if len(plan.Tracks) != 0 || plan.Summary.HighestImpact != "" {
		t.Errorf("Unknown blocker must not produce an execution item: %+v", plan)
	}
}

func TestReadinessScopeUsesAuthorityAndSelectedCandidates(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	dep := func(id string, typ model.DependencyType) []*model.Dependency {
		return []*model.Dependency{{DependsOnID: id, Type: typ}}
	}
	issues := []model.Issue{
		{ID: "api-chain", Status: model.StatusOpen, Dependencies: dep("web", model.DepBlocks)},
		{ID: "web", Status: model.StatusOpen, Dependencies: dep("ops", model.DepBlocks)},
		{ID: "ops", Status: model.StatusOpen},
		{ID: "api-ready", Status: model.StatusOpen, Dependencies: dep("done", model.DepBlocks)},
		{ID: "done", Status: model.StatusClosed},
		{ID: "api-child", Status: model.StatusOpen, Dependencies: dep("parent", model.DepParentChild)},
		{ID: "parent", Status: model.StatusOpen, Dependencies: dep("ops", model.DepBlocks)},
		{ID: "api-free-child", Status: model.StatusOpen, Dependencies: dep("free-parent", model.DepParentChild)},
		{ID: "free-parent", Status: model.StatusOpen},
		{ID: "api-missing", Status: model.StatusOpen, Dependencies: dep("missing", model.DepBlocks)},
		{ID: "api-parked", Status: model.StatusBlocked},
		{ID: "api-ongoing", Status: model.StatusInProgress},
		{ID: "api-epic", Status: model.StatusOpen, IssueType: model.TypeEpic},
		{ID: "api-assigned", Status: model.StatusOpen, Assignee: "owner"},
	}
	authority := model.NewReadinessIndex(issues)
	selected := map[string]bool{}
	var projected []model.Issue
	for _, issue := range issues {
		if len(issue.ID) >= 4 && issue.ID[:4] == "api-" {
			selected[issue.ID] = true
			projected = append(projected, issue)
		}
	}
	global := analysis.NewAnalyzer(issues)
	global.SetNow(now)
	globalReady := getIDs(global.GetActionableIssues())
	sort.Strings(globalReady)
	wantGlobal := []string{"api-assigned", "api-epic", "api-free-child", "api-ongoing", "api-ready", "free-parent", "ops"}
	if !reflect.DeepEqual(globalReady, wantGlobal) {
		t.Fatalf("global fixture readiness = %v, want %v", globalReady, wantGlobal)
	}

	for _, tc := range []struct {
		name       string
		visible    []model.Issue
		candidates map[string]bool
	}{
		{"full graph retains exploratory context", issues, selected},
		{"projection hides all external dependencies", projected, selected},
		{"empty selection retains only exploratory context", issues, map[string]bool{}},
		{"selection contains one blocked and one ready task", issues, map[string]bool{"api-chain": true, "api-ready": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			an := analysis.NewAnalyzer(tc.visible)
			an.SetNow(now)
			an.SetReadinessScope(authority, tc.candidates)
			var want []string
			for _, id := range globalReady {
				if tc.candidates[id] {
					want = append(want, id)
				}
			}
			var got []string
			plan := an.GetExecutionPlan()
			for _, track := range plan.Tracks {
				for _, item := range track.Items {
					got = append(got, item.ID)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("scoped plan = %v, want global readiness intersect selection %v", got, want)
			}
			if plan.TotalActionable != len(want) || plan.TotalBlocked != len(tc.candidates)-len(want) {
				t.Errorf("plan counts include context or lose withheld candidates: %+v", plan)
			}
			triage := analysis.ComputeTriageWithOptionsAndTime(tc.visible, analysis.TriageOptions{
				Readiness: authority, CandidateIDs: tc.candidates, TopN: len(issues),
			}, now)
			if triage.QuickRef.ActionableCount != len(want) {
				t.Errorf("triage ready count = %d, want %d", triage.QuickRef.ActionableCount, len(want))
			}
			var claimable []string
			for _, recommendation := range triage.Recommendations {
				if !tc.candidates[recommendation.ID] {
					t.Errorf("context-only recommendation escaped scope: %s", recommendation.ID)
				}
				if recommendation.Claimable {
					claimable = append(claimable, recommendation.ID)
				}
			}
			sort.Strings(claimable)
			var wantClaims []string
			for _, id := range []string{"api-free-child", "api-ready"} {
				if tc.candidates[id] {
					wantClaims = append(wantClaims, id)
				}
			}
			if !reflect.DeepEqual(claimable, wantClaims) {
				t.Errorf("claimable recommendations = %v, want %v", claimable, wantClaims)
			}
			for _, pick := range triage.QuickRef.TopPicks {
				if !contains(wantClaims, pick.ID) {
					t.Errorf("top pick %s is outside proven selected claimable set %v", pick.ID, wantClaims)
				}
			}
		})
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

func TestGetExecutionPlanLegacyDependencyGrouping(t *testing.T) {
	// A depends on B with empty dependency type (legacy "blocks")
	// Both A and B are open. B is actionable, A is blocked.
	// B should be actionable.
	// Since A depends on B, they form a connected component.
	// Therefore, B should appear in a track that represents this component.
	// We verify that the tracks logic respects this legacy dependency grouping.

	// Scenario:
	// X -> A (legacy). X -> B (legacy).
	// X is the common ancestor/dependent.
	// X is Closed.
	// A is Open. B is Open.
	// A depends on X (closed) -> A is actionable.
	// B depends on X (closed) -> B is actionable.
	// Connection: A -> X <- B (implicitly connected via X).
	// If connection logic works, {A, B, X} is one component.
	// We get 1 track with {A, B}.
	// If connection logic FAILS (ignoring legacy), we get {A}, {B}, {X}.
	// We get 2 tracks: Track 1 {A}, Track 2 {B}.

	issues := []model.Issue{
		{ID: "X", Title: "Common Root", Status: model.StatusClosed, Priority: 1},
		{ID: "A", Title: "Task A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "X", Type: ""}, // Legacy dependency (should connect A to X)
		}},
		{ID: "B", Title: "Task B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "X", Type: ""}, // Legacy dependency (should connect B to X)
		}},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// If legacy deps are ignored, A and B will be in separate components -> 2 tracks.
	// If legacy deps are respected, A and B are connected via X -> 1 track.
	if len(plan.Tracks) != 1 {
		t.Errorf("Expected 1 track (grouped via legacy dependency), got %d tracks", len(plan.Tracks))
	}
}

// The documented tie-break for the plan summary is: most unblocks, then
// priority (P0 first), then ID. Two actionable issues that each unblock two
// tasks must be separated by priority, and equal priority by lowest ID.
func TestExecutionPlan_HighestImpactTieBreaksByPriority(t *testing.T) {
	build := func(prioLow, prioHigh int) []model.Issue {
		return []model.Issue{
			// "zz" sorts after "aa" by ID; give it the better priority so a
			// priority-aware tie-break must pick it over the ID order.
			{ID: "aa", Title: "A", Status: model.StatusOpen, Priority: prioLow},
			{ID: "zz", Title: "Z", Status: model.StatusOpen, Priority: prioHigh},
			{ID: "a1", Title: "A1", Status: model.StatusOpen, Priority: 2, Dependencies: []*model.Dependency{{DependsOnID: "aa", Type: model.DepBlocks}}},
			{ID: "a2", Title: "A2", Status: model.StatusOpen, Priority: 2, Dependencies: []*model.Dependency{{DependsOnID: "aa", Type: model.DepBlocks}}},
			{ID: "z1", Title: "Z1", Status: model.StatusOpen, Priority: 2, Dependencies: []*model.Dependency{{DependsOnID: "zz", Type: model.DepBlocks}}},
			{ID: "z2", Title: "Z2", Status: model.StatusOpen, Priority: 2, Dependencies: []*model.Dependency{{DependsOnID: "zz", Type: model.DepBlocks}}},
		}
	}

	// aa is P3, zz is P1: equal unblocks (2 each) -> zz wins on priority.
	plan := analysis.NewAnalyzer(build(3, 1)).GetExecutionPlan()
	if plan.Summary.HighestImpact != "zz" {
		t.Fatalf("priority tie-break: highest_impact = %q, want zz (P1 over P3 with equal unblocks)", plan.Summary.HighestImpact)
	}
	if plan.Summary.UnblocksCount != 2 {
		t.Fatalf("unblocks_count = %d, want 2", plan.Summary.UnblocksCount)
	}

	// Equal priority -> lowest ID wins.
	plan = analysis.NewAnalyzer(build(2, 2)).GetExecutionPlan()
	if plan.Summary.HighestImpact != "aa" {
		t.Fatalf("ID tie-break: highest_impact = %q, want aa", plan.Summary.HighestImpact)
	}
}
