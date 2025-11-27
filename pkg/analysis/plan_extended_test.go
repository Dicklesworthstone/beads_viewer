package analysis_test

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TestPlan_DiamondDependency tests a diamond graph structure
// A -> B, A -> C, B -> D, C -> D
// A is the only initial actionable item
func TestPlan_DiamondDependency(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "D", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{
			{DependsOnID: "D", Type: model.DepBlocks},
		}},
		{ID: "D", Status: model.StatusOpen, Priority: 1},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// Only D should be actionable
	if plan.TotalActionable != 1 {
		t.Fatalf("Expected 1 actionable item, got %d", plan.TotalActionable)
	}

	item := plan.Tracks[0].Items[0]
	if item.ID != "D" {
		t.Errorf("Expected D to be actionable, got %s", item.ID)
	}

	// D should unblock B and C
	if len(item.UnblocksIDs) != 2 {
		t.Errorf("Expected D to unblock 2 items (B, C), got %d", len(item.UnblocksIDs))
	}
}

// TestPlan_DisconnectedComponents tests multiple disconnected subgraphs
func TestPlan_DisconnectedComponents(t *testing.T) {
	// Component 1: A -> B
	// Component 2: C (isolated)
	// Component 3: D -> E -> F
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen},

		{ID: "C", Status: model.StatusOpen},

		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "E", Type: model.DepBlocks}}},
		{ID: "E", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "F", Type: model.DepBlocks}}},
		{ID: "F", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// Actionable: B, C, F
	if plan.TotalActionable != 3 {
		t.Errorf("Expected 3 actionable items, got %d", plan.TotalActionable)
	}

	// Should have 3 tracks (one for each component)
	if len(plan.Tracks) != 3 {
		t.Errorf("Expected 3 parallel tracks, got %d", len(plan.Tracks))
	}
}

// TestPlan_WithClosedBlockers tests mixed open/closed states in a chain
func TestPlan_WithClosedBlockers(t *testing.T) {
	// Chain: A -> B -> C -> D
	// B and C are closed.
	// A depends on B (closed) -> A is actionable (effecitvely)
	// B depends on C (closed)
	// C depends on D (open)
	// D is open

	// Wait, if B is closed, A's dependency on B is satisfied.
	// A is actionable.
	// D is actionable (leaf).

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusClosed, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusClosed, Dependencies: []*model.Dependency{{DependsOnID: "D", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	// Actionable: A (blocker B closed) and D (leaf)
	if plan.TotalActionable != 2 {
		t.Errorf("Expected 2 actionable items (A, D), got %d", plan.TotalActionable)
	}
}

// TestPlan_ComplexPriorities tests priority sorting within tracks
func TestPlan_ComplexPriorities(t *testing.T) {
	// A depends on {P1, P0, P2, P3}
	// All dependencies are leaves (actionable)
	// They should be returned in priority order: P0, P1, P2, P3
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "p1", Type: model.DepBlocks},
			{DependsOnID: "p0", Type: model.DepBlocks},
			{DependsOnID: "p2", Type: model.DepBlocks},
			{DependsOnID: "p3", Type: model.DepBlocks},
		}},
		{ID: "p1", Status: model.StatusOpen, Priority: 1},
		{ID: "p0", Status: model.StatusOpen, Priority: 0},
		{ID: "p2", Status: model.StatusOpen, Priority: 2},
		{ID: "p3", Status: model.StatusOpen, Priority: 3},
	}

	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	if len(plan.Tracks) != 1 {
		t.Fatalf("Expected 1 track, got %d", len(plan.Tracks))
	}

	items := plan.Tracks[0].Items
	if len(items) != 4 {
		t.Fatalf("Expected 4 items, got %d", len(items))
	}

	// Check order
	if items[0].ID != "p0" {
		t.Errorf("Expected p0 first, got %s", items[0].ID)
	}
	if items[1].ID != "p1" {
		t.Errorf("Expected p1 second, got %s", items[1].ID)
	}
	if items[2].ID != "p2" {
		t.Errorf("Expected p2 third, got %s", items[2].ID)
	}
	if items[3].ID != "p3" {
		t.Errorf("Expected p3 fourth, got %s", items[3].ID)
	}
}
