package analysis_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Helper to extract IDs from issues and sort them for comparison
func getIDs(issues []model.Issue) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	sort.Strings(ids)
	return ids
}

// Helper to check if slice contains a value
func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func TestGetActionableIssuesEmpty(t *testing.T) {
	an := analysis.NewAnalyzer([]model.Issue{})
	actionable := an.GetActionableIssues()

	if len(actionable) != 0 {
		t.Errorf("Expected 0 actionable issues for empty list, got %d", len(actionable))
	}
}

// TestAnalyzeEmptyIssues ensures Analyze() doesn't panic with empty input.
// This is critical: gonum's PageRank panics on zero-length matrices.
func TestAnalyzeEmptyIssues(t *testing.T) {
	an := analysis.NewAnalyzer([]model.Issue{})

	// This should NOT panic
	stats := an.Analyze()

	// All maps should be initialized but empty
	if len(stats.PageRank) != 0 {
		t.Errorf("Expected empty PageRank, got %d", len(stats.PageRank))
	}
	if len(stats.Betweenness) != 0 {
		t.Errorf("Expected empty Betweenness, got %d", len(stats.Betweenness))
	}
	if len(stats.CriticalPathScore) != 0 {
		t.Errorf("Expected empty CriticalPathScore, got %d", len(stats.CriticalPathScore))
	}
}

func TestGetActionableIssuesAllClosed(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusClosed},
		{ID: "B", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	if len(actionable) != 0 {
		t.Errorf("Expected 0 actionable issues (all closed), got %d", len(actionable))
	}
}

func TestGetActionableIssuesSingleNoDeps(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	if len(actionable) != 1 {
		t.Fatalf("Expected 1 actionable issue, got %d", len(actionable))
	}
	if actionable[0].ID != "A" {
		t.Errorf("Expected issue A, got %s", actionable[0].ID)
	}
}

func TestGetActionableIssuesChainAllOpen(t *testing.T) {
	// A depends on B, B depends on C (all open)
	// Only C should be actionable (no blocking deps)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "C" {
		t.Errorf("Expected only C actionable, got %v", ids)
	}
}

func TestGetActionableIssuesChainLeafClosed(t *testing.T) {
	// A depends on B, B depends on C
	// C is closed → B is actionable
	// A is still blocked by B (open)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "B" {
		t.Errorf("Expected only B actionable (C closed), got %v", ids)
	}
}

func TestGetActionableIssuesChainTwoClosed(t *testing.T) {
	// A depends on B, B depends on C
	// B and C closed → A is actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusClosed, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "A" {
		t.Errorf("Expected only A actionable (B,C closed), got %v", ids)
	}
}

func TestGetActionableIssuesParallelTracks(t *testing.T) {
	// Two independent chains:
	// A depends on B (both open) → only B actionable
	// C depends on D (D closed) → C actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "D", Type: model.DepBlocks},
		}},
		{ID: "D", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 2 {
		t.Fatalf("Expected 2 actionable issues, got %d: %v", len(ids), ids)
	}
	if !contains(ids, "B") || !contains(ids, "C") {
		t.Errorf("Expected B and C actionable, got %v", ids)
	}
}

func TestGetActionableIssuesMissingBlocker(t *testing.T) {
	// A depends on "missing" (doesn't exist) → A is actionable
	// Missing blockers don't block (graceful degradation)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "missing", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "A" {
		t.Errorf("Expected A actionable (missing blocker), got %v", ids)
	}
}

func TestAnalyzeIgnoresNonBlockingDependencies(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepRelated}, // non-blocking edge
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepRelated}, // back edge would create cycle if counted
		}},
	}

	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()

	// Non-blocking deps should not introduce graph edges
	if got := stats.InDegree["A"]; got != 0 {
		t.Fatalf("expected A indegree 0 when only related edges exist, got %d", got)
	}
	if got := stats.OutDegree["A"]; got != 0 {
		t.Fatalf("expected A outdegree 0 when only related edges exist, got %d", got)
	}

	// Topological order should include both nodes (no cycles introduced)
	if len(stats.TopologicalOrder) != 2 {
		t.Fatalf("expected topological order length 2, got %d", len(stats.TopologicalOrder))
	}
	if len(stats.Cycles) != 0 {
		t.Fatalf("expected no cycles from non-blocking edges, got %d", len(stats.Cycles))
	}
}

func TestGetActionableIssuesRelatedDoesntBlock(t *testing.T) {
	// A has "related" dep on B (open)
	// Related deps don't block → A is actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepRelated},
		}},
		{ID: "B", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 2 {
		t.Fatalf("Expected 2 actionable (related doesn't block), got %d: %v", len(ids), ids)
	}
}

func TestGetActionableIssuesParentChildDoesntBlock(t *testing.T) {
	// A has "parent-child" dep on B (open)
	// Parent-child deps don't block → A is actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepParentChild},
		}},
		{ID: "B", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 2 {
		t.Fatalf("Expected 2 actionable (parent-child doesn't block), got %d: %v", len(ids), ids)
	}
}

func TestGetActionableIssuesCycle(t *testing.T) {
	// Cycle: A -> B -> C -> A (all block each other)
	// All are blocked (none actionable)
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

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	if len(actionable) != 0 {
		t.Errorf("Expected 0 actionable in cycle, got %d: %v", len(actionable), getIDs(actionable))
	}
}

func TestGetActionableIssuesCycleWithOneClosed(t *testing.T) {
	// Cycle: A -> B -> C -> A
	// C is closed → A is blocked by B, B is actionable (C closed)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "C", Status: model.StatusClosed, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "B" {
		t.Errorf("Expected B actionable (C closed breaks cycle), got %v", ids)
	}
}

func TestGetActionableIssuesMultipleBlockers(t *testing.T) {
	// A depends on B AND C (both must be closed)
	// B is closed, C is open → A is blocked
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusClosed},
		{ID: "C", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "C" {
		t.Errorf("Expected only C actionable, got %v", ids)
	}
}

func TestGetActionableIssuesMultipleBlockersAllClosed(t *testing.T) {
	// A depends on B AND C (both closed) → A is actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusClosed},
		{ID: "C", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	ids := getIDs(actionable)
	if len(ids) != 1 || ids[0] != "A" {
		t.Errorf("Expected A actionable (all blockers closed), got %v", ids)
	}
}

func TestGetActionableIssuesInProgressStatus(t *testing.T) {
	// In-progress issues should still be returned if actionable
	issues := []model.Issue{
		{ID: "A", Status: model.StatusInProgress},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	if len(actionable) != 1 || actionable[0].ID != "A" {
		t.Errorf("Expected in_progress issue to be actionable, got %v", getIDs(actionable))
	}
}

func TestGetActionableIssuesBlockedStatus(t *testing.T) {
	// "Blocked" status issues are still returned if no blocking deps
	// (status is informational, deps are structural)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusBlocked},
	}

	an := analysis.NewAnalyzer(issues)
	actionable := an.GetActionableIssues()

	if len(actionable) != 1 || actionable[0].ID != "A" {
		t.Errorf("Expected blocked-status issue (no deps) to be actionable, got %v", getIDs(actionable))
	}
}

func TestGetBlockers(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepRelated},      // Not a blocker
			{DependsOnID: "missing", Type: model.DepBlocks}, // Missing
		}},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)
	blockers := an.GetBlockers("A")

	// Should only include B (exists and is "blocks" type)
	if len(blockers) != 1 || blockers[0] != "B" {
		t.Errorf("Expected blockers [B], got %v", blockers)
	}
}

func TestGetOpenBlockers(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
			{DependsOnID: "C", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusClosed},
	}

	an := analysis.NewAnalyzer(issues)
	openBlockers := an.GetOpenBlockers("A")

	// Should only include B (C is closed)
	if len(openBlockers) != 1 || openBlockers[0] != "B" {
		t.Errorf("Expected open blockers [B], got %v", openBlockers)
	}
}

// TestAnalyzeCompletesWithinTimeout ensures that Analyze() does not hang
// even on graphs that might cause HITS or cycle detection to take a long time.
// This test creates a sparse graph structure that could cause convergence issues
// and verifies the analysis completes within a reasonable time.
func TestAnalyzeCompletesWithinTimeout(t *testing.T) {
	// Create a graph with many disconnected nodes plus some edges
	// This can cause HITS to struggle with convergence
	var issues []model.Issue
	for i := 0; i < 100; i++ {
		issue := model.Issue{
			ID:     fmt.Sprintf("ISSUE-%d", i),
			Status: model.StatusOpen,
		}
		// Create some sparse dependencies that might cause issues
		if i > 0 && i%10 == 0 {
			issue.Dependencies = []*model.Dependency{
				{DependsOnID: fmt.Sprintf("ISSUE-%d", i-1), Type: model.DepBlocks},
			}
		}
		issues = append(issues, issue)
	}

	an := analysis.NewAnalyzer(issues)

	// Use a channel to detect if Analyze() completes
	done := make(chan struct{})
	go func() {
		_ = an.Analyze()
		close(done)
	}()

	select {
	case <-done:
		// Success - completed within time limit
	case <-time.After(3 * time.Second):
		t.Fatal("Analyze() did not complete within 3 seconds - possible hang in HITS or cycle detection")
	}
}

// TestAnalyzeNoEdgesGraph ensures analysis completes on graphs with nodes but no edges.
// HITS in particular has historically hung on such graphs.
func TestAnalyzeNoEdgesGraph(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{ID: "C", Status: model.StatusOpen},
	}

	an := analysis.NewAnalyzer(issues)

	done := make(chan struct{})
	go func() {
		_ = an.Analyze()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Analyze() hung on graph with no edges")
	}
}

// TestAnalyzeSparseDisconnectedGraph tests a worst-case scenario for HITS convergence.
func TestAnalyzeSparseDisconnectedGraph(t *testing.T) {
	// Create multiple disconnected components
	var issues []model.Issue
	for component := 0; component < 5; component++ {
		base := component * 10
		for i := 0; i < 10; i++ {
			issue := model.Issue{
				ID:     fmt.Sprintf("C%d-ISSUE-%d", component, i),
				Status: model.StatusOpen,
			}
			if i > 0 {
				issue.Dependencies = []*model.Dependency{
					{DependsOnID: fmt.Sprintf("C%d-ISSUE-%d", component, i-1), Type: model.DepBlocks},
				}
			}
			_ = base // unused but documents structure
			issues = append(issues, issue)
		}
	}

	an := analysis.NewAnalyzer(issues)

	done := make(chan struct{})
	go func() {
		stats := an.Analyze()
		// Verify we got reasonable results
		if len(stats.PageRank) != 50 {
			t.Errorf("Expected 50 PageRank entries, got %d", len(stats.PageRank))
		}
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Analyze() hung on sparse disconnected graph")
	}
}

func TestImpactScore(t *testing.T) {
	// Chain: A -> B -> C (A depends on B, B depends on C)
	// Edges: A->B, B->C
	// In Graph: A->B, B->C (u -> v)
	// Impact Depth logic:
	// C (Leaf): Should have Impact 1 (It's a root dependency).
	// B: Impact 1 + Impact(C) = 2.
	// A: Impact 1 + Impact(B) = 3.
	// Wait.
	// If A->B->C.
	// B is "upstream" of A?
	// If B breaks, A breaks.
	// If C breaks, B breaks, A breaks.
	// So C has highest impact (3).
	// A has lowest impact (1).

	// Let's verify my implementation.
	// Forward iteration of Topo Sort.
	// A->B->C.
	// Sort: A, B, C.
	// Loop:
	// A: To(A)? None. Impact = 1.
	// B: To(B)? A. Impact = 1 + 1 = 2.
	// C: To(C)? B. Impact = 1 + 2 = 3.
	// Correct. C has score 3.

	issues := []model.Issue{
		{ID: "A", Dependencies: []*model.Dependency{{DependsOnID: "B"}}},
		{ID: "B", Dependencies: []*model.Dependency{{DependsOnID: "C"}}},
		{ID: "C"},
	}

	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()

	if stats.CriticalPathScore["C"] != 3 {
		t.Errorf("Expected C to have score 3, got %f", stats.CriticalPathScore["C"])
	}
	if stats.CriticalPathScore["B"] != 2 {
		t.Errorf("Expected B to have score 2, got %f", stats.CriticalPathScore["B"])
	}
	if stats.CriticalPathScore["A"] != 1 {
		t.Errorf("Expected A to have score 1, got %f", stats.CriticalPathScore["A"])
	}
}
