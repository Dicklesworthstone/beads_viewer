package analysis

import (
	"sync"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestNewTriageContext(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	if ctx.analyzer != analyzer {
		t.Error("NewTriageContext should store the analyzer")
	}
	if ctx.mu != nil {
		t.Error("NewTriageContext should not have mutex (not thread-safe)")
	}
	if ctx.actionableComputed {
		t.Error("actionableComputed should start false")
	}
}

func TestNewTriageContextThreadSafe(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContextThreadSafe(analyzer)

	if ctx.mu == nil {
		t.Error("NewTriageContextThreadSafe should have mutex")
	}
}

func TestTriageContext_ActionableIssues(t *testing.T) {
	t.Run("no blockers - all actionable", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{ID: "B", Status: model.StatusOpen},
			{ID: "C", Status: model.StatusClosed},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		actionable := ctx.ActionableIssues()

		if len(actionable) != 2 {
			t.Errorf("Expected 2 actionable, got %d", len(actionable))
		}

		// Verify caching
		if !ctx.actionableComputed {
			t.Error("actionableComputed should be true after first call")
		}

		// Second call should return same slice
		actionable2 := ctx.ActionableIssues()
		if len(actionable2) != len(actionable) {
			t.Error("Cached result should match")
		}
	})

	t.Run("with blocking dependencies", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		actionable := ctx.ActionableIssues()

		// Only A is actionable (B is blocked by A)
		if len(actionable) != 1 {
			t.Errorf("Expected 1 actionable, got %d", len(actionable))
		}
		if len(actionable) > 0 && actionable[0].ID != "A" {
			t.Errorf("Expected A to be actionable, got %s", actionable[0].ID)
		}
	})
}

func TestTriageContext_IsActionable(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{
			ID:     "B",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks},
			},
		},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	if !ctx.IsActionable("A") {
		t.Error("A should be actionable")
	}
	if ctx.IsActionable("B") {
		t.Error("B should not be actionable (blocked by A)")
	}
	if ctx.IsActionable("nonexistent") {
		t.Error("nonexistent should not be actionable")
	}
}

func TestTriageContext_BlockerDepth(t *testing.T) {
	t.Run("no blockers - depth 0", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		depth := ctx.BlockerDepth("A")
		if depth != 0 {
			t.Errorf("Expected depth 0, got %d", depth)
		}
	})

	t.Run("single blocker - depth 1", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		if ctx.BlockerDepth("A") != 0 {
			t.Errorf("A should have depth 0, got %d", ctx.BlockerDepth("A"))
		}
		if ctx.BlockerDepth("B") != 1 {
			t.Errorf("B should have depth 1, got %d", ctx.BlockerDepth("B"))
		}
	})

	t.Run("chain of blockers - depth matches chain length", func(t *testing.T) {
		// C -> B -> A (C depends on B which depends on A)
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
			{
				ID:     "C",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "B", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		if ctx.BlockerDepth("A") != 0 {
			t.Errorf("A should have depth 0, got %d", ctx.BlockerDepth("A"))
		}
		if ctx.BlockerDepth("B") != 1 {
			t.Errorf("B should have depth 1, got %d", ctx.BlockerDepth("B"))
		}
		if ctx.BlockerDepth("C") != 2 {
			t.Errorf("C should have depth 2, got %d", ctx.BlockerDepth("C"))
		}
	})

	t.Run("closed blockers don't count", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusClosed},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		// A is closed, so B is not blocked
		if ctx.BlockerDepth("B") != 0 {
			t.Errorf("B should have depth 0 (A is closed), got %d", ctx.BlockerDepth("B"))
		}
	})

	t.Run("cycle detection returns -1", func(t *testing.T) {
		// A -> B -> A (cycle)
		issues := []model.Issue{
			{
				ID:     "A",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "B", Type: model.DepBlocks},
				},
			},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		if ctx.BlockerDepth("A") != -1 {
			t.Errorf("A should have depth -1 (cycle), got %d", ctx.BlockerDepth("A"))
		}
		if ctx.BlockerDepth("B") != -1 {
			t.Errorf("B should have depth -1 (cycle), got %d", ctx.BlockerDepth("B"))
		}
	})

	t.Run("caching works", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		// First call
		depth1 := ctx.BlockerDepth("B")
		// Second call (should be cached)
		depth2 := ctx.BlockerDepth("B")

		if depth1 != depth2 {
			t.Error("Cached depth should match")
		}
		if _, ok := ctx.blockerDepth["B"]; !ok {
			t.Error("Depth should be cached")
		}
	})
}

func TestTriageContext_OpenBlockers(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen},
		{
			ID:     "C",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks},
				{DependsOnID: "B", Type: model.DepBlocks},
			},
		},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	blockers := ctx.OpenBlockers("C")
	if len(blockers) != 2 {
		t.Errorf("Expected 2 blockers, got %d", len(blockers))
	}

	// Verify caching
	blockers2 := ctx.OpenBlockers("C")
	if len(blockers2) != len(blockers) {
		t.Error("Cached blockers should match")
	}

	// No blockers
	blockersA := ctx.OpenBlockers("A")
	if len(blockersA) != 0 {
		t.Errorf("A should have no blockers, got %d", len(blockersA))
	}
}

func TestTriageContext_UnblocksMap(t *testing.T) {
	t.Run("simple unblock", func(t *testing.T) {
		// B depends on A; completing A unblocks B
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{
				ID:     "B",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		unblocksMap := ctx.UnblocksMap()

		unblocks := unblocksMap["A"]
		if len(unblocks) != 1 || unblocks[0] != "B" {
			t.Errorf("A should unblock B, got %v", unblocks)
		}
	})

	t.Run("multiple blockers - only unblocks when last", func(t *testing.T) {
		// C depends on both A and B; completing A alone doesn't unblock C
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
			{ID: "B", Status: model.StatusOpen},
			{
				ID:     "C",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
					{DependsOnID: "B", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		unblocksMap := ctx.UnblocksMap()

		// Neither A nor B alone unblocks C
		if len(unblocksMap["A"]) != 0 {
			t.Errorf("A should not unblock anything, got %v", unblocksMap["A"])
		}
		if len(unblocksMap["B"]) != 0 {
			t.Errorf("B should not unblock anything, got %v", unblocksMap["B"])
		}
	})

	t.Run("one blocker closed - completing other unblocks", func(t *testing.T) {
		// C depends on A (closed) and B (open); completing B unblocks C
		issues := []model.Issue{
			{ID: "A", Status: model.StatusClosed},
			{ID: "B", Status: model.StatusOpen},
			{
				ID:     "C",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{DependsOnID: "A", Type: model.DepBlocks},
					{DependsOnID: "B", Type: model.DepBlocks},
				},
			},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		unblocksMap := ctx.UnblocksMap()

		// B is the last open blocker, so completing B unblocks C
		if len(unblocksMap["B"]) != 1 || unblocksMap["B"][0] != "C" {
			t.Errorf("B should unblock C, got %v", unblocksMap["B"])
		}
	})

	t.Run("caching works", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Status: model.StatusOpen},
		}
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)

		map1 := ctx.UnblocksMap()
		map2 := ctx.UnblocksMap()

		if !ctx.unblocksComputed {
			t.Error("unblocksComputed should be true")
		}

		// Should be same map reference (cached)
		if len(map1) != len(map2) {
			t.Error("Cached map should match")
		}
	})
}

func TestTriageContext_Unblocks(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{
			ID:     "B",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks},
			},
		},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	unblocks := ctx.Unblocks("A")
	if len(unblocks) != 1 || unblocks[0] != "B" {
		t.Errorf("A should unblock B, got %v", unblocks)
	}

	count := ctx.UnblocksCount("A")
	if count != 1 {
		t.Errorf("UnblocksCount should be 1, got %d", count)
	}
}

func TestTriageContext_Reset(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{
			ID:     "B",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks},
			},
		},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	// Populate caches
	_ = ctx.ActionableIssues()
	_ = ctx.BlockerDepth("B")
	_ = ctx.UnblocksMap()

	if !ctx.actionableComputed {
		t.Error("actionableComputed should be true before reset")
	}
	if len(ctx.blockerDepth) == 0 {
		t.Error("blockerDepth should have entries before reset")
	}

	ctx.Reset()

	if ctx.actionableComputed {
		t.Error("actionableComputed should be false after reset")
	}
	if ctx.actionable != nil {
		t.Error("actionable should be nil after reset")
	}
	if len(ctx.blockerDepth) != 0 {
		t.Error("blockerDepth should be empty after reset")
	}
	if ctx.unblocksComputed {
		t.Error("unblocksComputed should be false after reset")
	}
}

func TestTriageContext_AllBlockerDepths(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{
			ID:     "B",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks},
			},
		},
		{
			ID:     "C",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "B", Type: model.DepBlocks},
			},
		},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	depths := ctx.AllBlockerDepths()

	if depths["A"] != 0 {
		t.Errorf("A depth should be 0, got %d", depths["A"])
	}
	if depths["B"] != 1 {
		t.Errorf("B depth should be 1, got %d", depths["B"])
	}
	if depths["C"] != 2 {
		t.Errorf("C depth should be 2, got %d", depths["C"])
	}
}

func TestTriageContext_ThreadSafe(t *testing.T) {
	issues := make([]model.Issue, 100)
	for i := 0; i < 100; i++ {
		issues[i] = model.Issue{
			ID:     string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Status: model.StatusOpen,
		}
	}
	// Add some dependencies
	for i := 10; i < 100; i++ {
		issues[i].Dependencies = []*model.Dependency{
			{DependsOnID: issues[i%10].ID, Type: model.DepBlocks},
		}
	}

	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContextThreadSafe(analyzer)

	// Concurrent access from multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ctx.ActionableIssues()
			for j := 0; j < 100; j++ {
				_ = ctx.BlockerDepth(issues[j].ID)
				_ = ctx.IsActionable(issues[j].ID)
			}
			_ = ctx.UnblocksMap()
		}()
	}
	wg.Wait()

	// If we get here without race detector complaining, thread safety works
	if !ctx.actionableComputed {
		t.Error("actionableComputed should be true")
	}
}

func TestTriageContext_Convenience(t *testing.T) {
	issues := []model.Issue{
		{
			ID:           "A",
			Status:       model.StatusOpen,
			Title:        "Issue A",
			Labels:       []string{"stable"},
			Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepRelated}},
		},
		{ID: "B", Status: model.StatusOpen, Title: "Issue B"},
	}
	analyzer := NewAnalyzer(issues)
	ctx := NewTriageContext(analyzer)

	// Test GetIssue
	issue := ctx.GetIssue("A")
	if issue == nil || issue.Title != "Issue A" {
		t.Error("GetIssue should return correct issue")
	}

	// Test IssueCount
	if ctx.IssueCount() != 2 {
		t.Errorf("IssueCount should be 2, got %d", ctx.IssueCount())
	}

	// Test ActionableCount
	if ctx.ActionableCount() != 2 {
		t.Errorf("ActionableCount should be 2, got %d", ctx.ActionableCount())
	}

	// Test Analyzer()
	if ctx.Analyzer() != analyzer {
		t.Error("Analyzer() should return the underlying analyzer")
	}

	// Test Issues()
	allIssues := ctx.Issues()
	if len(allIssues) != 2 {
		t.Errorf("Issues() should return 2, got %d", len(allIssues))
	}
	for i := range allIssues {
		if allIssues[i].ID == "A" {
			allIssues[i].Labels[0] = "mutated"
			allIssues[i].Dependencies[0].DependsOnID = "missing"
		}
	}
	stable := ctx.GetIssue("A")
	if stable == nil || stable.Labels[0] != "stable" || stable.Dependencies[0].DependsOnID != "B" {
		t.Fatalf("Issues() exposed analyzer-owned nested data: %+v", stable)
	}
}

// Benchmark to verify caching provides speedup
func BenchmarkTriageContext_ActionableIssues(b *testing.B) {
	// Create issues with varying dependencies
	issues := make([]model.Issue, 500)
	for i := 0; i < 500; i++ {
		issue := model.Issue{
			ID:     string(rune('A'+i%26)) + string(rune('0'+i/26)) + string(rune('0'+i%10)),
			Status: model.StatusOpen,
		}
		if i > 0 && i%5 == 0 {
			issue.Dependencies = []*model.Dependency{
				{DependsOnID: issues[i-1].ID, Type: model.DepBlocks},
			}
		}
		issues[i] = issue
	}

	b.Run("Uncached", func(b *testing.B) {
		analyzer := NewAnalyzer(issues)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = analyzer.GetActionableIssues()
		}
	})

	b.Run("Cached", func(b *testing.B) {
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = ctx.ActionableIssues()
		}
	})
}

func BenchmarkTriageContext_BlockerDepth(b *testing.B) {
	// Create a chain of dependencies
	issues := make([]model.Issue, 100)
	for i := 0; i < 100; i++ {
		issue := model.Issue{
			ID:     string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Status: model.StatusOpen,
		}
		if i > 0 {
			issue.Dependencies = []*model.Dependency{
				{DependsOnID: issues[i-1].ID, Type: model.DepBlocks},
			}
		}
		issues[i] = issue
	}

	b.Run("Uncached", func(b *testing.B) {
		analyzer := NewAnalyzer(issues)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := 0; j < 100; j++ {
				_ = analyzer.GetBlockerDepth(issues[j].ID)
			}
		}
	})

	b.Run("Cached", func(b *testing.B) {
		analyzer := NewAnalyzer(issues)
		ctx := NewTriageContext(analyzer)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := 0; j < 100; j++ {
				_ = ctx.BlockerDepth(issues[j].ID)
			}
		}
	})
}
