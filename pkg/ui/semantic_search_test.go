package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// =============================================================================
// Mock Embedder for Testing
// =============================================================================

type mockEmbedder struct {
	dim       int
	embedFunc func(ctx context.Context, texts []string) ([][]float32, error)
}

func (m *mockEmbedder) Provider() search.Provider {
	return search.ProviderOpenAI
}

func (m *mockEmbedder) Dim() int {
	return m.dim
}

func (m *mockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if m.embedFunc != nil {
		return m.embedFunc(ctx, texts)
	}
	// Default: return zero vectors
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = make([]float32, m.dim)
	}
	return result, nil
}

// =============================================================================
// SemanticSearch Constructor Tests
// =============================================================================

func TestNewSemanticSearch(t *testing.T) {
	ss := NewSemanticSearch()
	if ss == nil {
		t.Fatal("NewSemanticSearch() returned nil")
	}

	snap := ss.Snapshot()
	if snap.Ready {
		t.Error("New SemanticSearch should not be ready")
	}
	if snap.Index != nil {
		t.Error("New SemanticSearch should have nil Index")
	}
	if snap.Embedder != nil {
		t.Error("New SemanticSearch should have nil Embedder")
	}
	if snap.IDs != nil {
		t.Error("New SemanticSearch should have nil IDs")
	}
}

// =============================================================================
// Snapshot Tests
// =============================================================================

func TestSemanticSearchSnapshot(t *testing.T) {
	ss := NewSemanticSearch()
	snap := ss.Snapshot()

	// Should return empty snapshot initially
	if snap.Ready {
		t.Error("Initial snapshot should not be ready")
	}
}

// =============================================================================
// SetIndex Tests
// =============================================================================

func TestSemanticSearchSetIndex(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(384)
	embedder := &mockEmbedder{dim: 384}

	ss.SetIndex(idx, embedder)
	snap := ss.Snapshot()

	if !snap.Ready {
		t.Error("Snapshot should be ready after SetIndex with non-nil values")
	}
	if snap.Index != idx {
		t.Error("Snapshot.Index should match set index")
	}
	if snap.Embedder != embedder {
		t.Error("Snapshot.Embedder should match set embedder")
	}
}

func TestSemanticSearchSetIndexNilIndex(t *testing.T) {
	ss := NewSemanticSearch()
	embedder := &mockEmbedder{dim: 384}

	ss.SetIndex(nil, embedder)
	snap := ss.Snapshot()

	if snap.Ready {
		t.Error("Snapshot should not be ready with nil index")
	}
}

func TestSemanticSearchSetIndexNilEmbedder(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(384)

	ss.SetIndex(idx, nil)
	snap := ss.Snapshot()

	if snap.Ready {
		t.Error("Snapshot should not be ready with nil embedder")
	}
}

func TestSemanticSearchSetIndexBothNil(t *testing.T) {
	ss := NewSemanticSearch()

	ss.SetIndex(nil, nil)
	snap := ss.Snapshot()

	if snap.Ready {
		t.Error("Snapshot should not be ready with both nil")
	}
}

// =============================================================================
// SetIDs Tests
// =============================================================================

func TestSemanticSearchSetIDs(t *testing.T) {
	ss := NewSemanticSearch()

	ids := []string{"issue-1", "issue-2", "issue-3"}
	ss.SetIDs(ids)

	snap := ss.Snapshot()
	if len(snap.IDs) != 3 {
		t.Errorf("Expected 3 IDs, got %d", len(snap.IDs))
	}

	// Verify IDs are correct
	for i, id := range ids {
		if snap.IDs[i] != id {
			t.Errorf("ID[%d] = %q, want %q", i, snap.IDs[i], id)
		}
	}
}

func TestSemanticSearchSetIDsCopiesSlice(t *testing.T) {
	ss := NewSemanticSearch()

	ids := []string{"issue-1", "issue-2"}
	ss.SetIDs(ids)

	// Modify original slice
	ids[0] = "modified"

	snap := ss.Snapshot()
	if snap.IDs[0] == "modified" {
		t.Error("SetIDs should copy the slice, not reference it")
	}
}

func TestSemanticSearchSetIDsEmpty(t *testing.T) {
	ss := NewSemanticSearch()

	ss.SetIDs([]string{})
	snap := ss.Snapshot()

	if len(snap.IDs) != 0 {
		t.Errorf("Expected 0 IDs, got %d", len(snap.IDs))
	}
}

func TestSemanticSearchSetIDsNil(t *testing.T) {
	ss := NewSemanticSearch()

	ss.SetIDs(nil)
	snap := ss.Snapshot()

	if len(snap.IDs) != 0 {
		t.Errorf("Expected 0 IDs after nil, got %d", len(snap.IDs))
	}
}

// =============================================================================
// Filter Tests
// =============================================================================

func TestSemanticSearchFilterEmptyTerm(t *testing.T) {
	ss := NewSemanticSearch()

	targets := []string{"fix bug", "add feature", "update docs"}
	ranks := ss.Filter("", targets)

	// Empty term returns default filter results
	// DefaultFilter with empty term returns empty slice (no matches)
	// This is the expected behavior - empty search shows all without ranking
	_ = ranks // Result depends on DefaultFilter behavior
}

func TestSemanticSearchFilterNotReady(t *testing.T) {
	ss := NewSemanticSearch()
	// Not calling SetIndex, so not ready

	targets := []string{"fix bug", "add feature"}
	ranks := ss.Filter("bug", targets)

	// When not ready, should fall back to default filter
	// Default filter should return some results for "bug"
	if len(ranks) == 0 {
		t.Error("Expected some ranks from default filter")
	}
}

func TestSemanticSearchFilterIDMismatch(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(384)
	embedder := &mockEmbedder{dim: 384}
	ss.SetIndex(idx, embedder)

	// Set only 2 IDs but 3 targets - mismatch
	ss.SetIDs([]string{"id-1", "id-2"})

	targets := []string{"fix bug", "add feature", "update docs"}
	ranks := ss.Filter("bug", targets)

	// ID mismatch should fall back to default filter
	if len(ranks) == 0 {
		t.Error("Expected some ranks from default filter due to ID mismatch")
	}
}

func TestSemanticSearchFilterWithValidSetup(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3) // Small dimension for testing
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			// Return a simple vector for the query
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Add vectors to the index
	idx.Upsert("id-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0}) // Similar to query
	idx.Upsert("id-2", search.ContentHash{}, []float32{0.0, 1.0, 0.0}) // Orthogonal
	idx.Upsert("id-3", search.ContentHash{}, []float32{0.5, 0.5, 0.0}) // Partially similar

	ss.SetIDs([]string{"id-1", "id-2", "id-3"})

	// Use ComputeSemanticResults for synchronous computation (testing)
	ranks := ss.ComputeSemanticResults("search query")

	// Should return ranked results
	if len(ranks) == 0 {
		t.Error("Expected some ranks from semantic search")
	}

	// First result should be id-1 (index 0) since it's most similar to query vector
	if len(ranks) > 0 && ranks[0].Index != 0 {
		t.Errorf("Expected first rank to be index 0 (most similar), got %d", ranks[0].Index)
	}
}

func TestSemanticSearchFilterSortsByScore(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Add vectors with different similarities
	idx.Upsert("id-a", search.ContentHash{}, []float32{0.0, 1.0, 0.0}) // Low similarity
	idx.Upsert("id-b", search.ContentHash{}, []float32{1.0, 0.0, 0.0}) // High similarity
	idx.Upsert("id-c", search.ContentHash{}, []float32{0.5, 0.5, 0.0}) // Medium similarity

	ss.SetIDs([]string{"id-a", "id-b", "id-c"})

	// Use ComputeSemanticResults for synchronous computation (testing)
	ranks := ss.ComputeSemanticResults("query")

	if len(ranks) < 3 {
		t.Fatalf("Expected at least 3 ranks, got %d", len(ranks))
	}

	// id-b (index 1) should be first (highest similarity)
	if ranks[0].Index != 1 {
		t.Errorf("Expected first rank index 1 (id-b), got %d", ranks[0].Index)
	}
}

func TestSemanticSearchFilterLimit(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Create 100 items
	ids := make([]string, 100)
	targets := make([]string, 100)
	for i := 0; i < 100; i++ {
		ids[i] = "id-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
		targets[i] = "target " + ids[i]
		idx.Upsert(ids[i], search.ContentHash{}, []float32{float32(i) / 100, 0.0, 0.0})
	}

	ss.SetIDs(ids)
	ranks := ss.Filter("query", targets)

	// Should be limited to 75 results
	if len(ranks) > 75 {
		t.Errorf("Expected max 75 ranks, got %d", len(ranks))
	}
}

func TestSemanticSearchExactOpaqueIDSurvivesLimitAndHybridCandidateCap(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(2)
	embedder := &mockEmbedder{
		dim: 2,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			return [][]float32{{1, 0}}, nil
		},
	}

	const ordinaryCount = 300
	ids := make([]string, 0, ordinaryCount+1)
	metrics := make(map[string]search.IssueMetrics, ordinaryCount+1)
	for i := 0; i < ordinaryCount; i++ {
		id := fmt.Sprintf("issue-%03d", i)
		ids = append(ids, id)
		if err := idx.Upsert(id, search.ContentHash{}, []float32{1, 0}); err != nil {
			t.Fatalf("Upsert(%q): %v", id, err)
		}
		metrics[id] = search.IssueMetrics{}
	}
	exactID := "bv-9gf.3"
	ids = append(ids, exactID)
	if err := idx.Upsert(exactID, search.ContentHash{}, []float32{0, 1}); err != nil {
		t.Fatalf("Upsert exact ID: %v", err)
	}
	metrics[exactID] = search.IssueMetrics{}

	ss.SetIndex(idx, embedder)
	ss.SetIDs(ids)
	ss.SetHybridConfig(true, search.PresetDefault)
	ss.SetMetricsCache(&staticMetricsCache{metrics: metrics})

	ranks := ss.ComputeSemanticResults(" BV-9GF.3 ")
	if len(ranks) != 75 {
		t.Fatalf("rank count = %d, want bounded 75", len(ranks))
	}
	if ranks[0].Index != ordinaryCount {
		t.Fatalf("exact opaque ID rank = %#v, want source index %d first", ranks[0], ordinaryCount)
	}
	scores, ok := ss.Scores(" BV-9GF.3 ")
	if !ok || scores[exactID].Components == nil {
		t.Fatalf("exact ID was promoted without hybrid scoring: %#v, %t", scores[exactID], ok)
	}
}

func TestSemanticSearchExactIDCaseFoldCollisionIsNotArbitrarilyPromoted(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(2)
	embedder := &mockEmbedder{
		dim: 2,
		embedFunc: func(context.Context, []string) ([][]float32, error) {
			return [][]float32{{1, 0}}, nil
		},
	}

	ids := []string{"Task-1", "task-1"}
	if err := idx.Upsert(ids[0], search.ContentHash{}, []float32{0, 1}); err != nil {
		t.Fatalf("Upsert(%q): %v", ids[0], err)
	}
	if err := idx.Upsert(ids[1], search.ContentHash{}, []float32{1, 0}); err != nil {
		t.Fatalf("Upsert(%q): %v", ids[1], err)
	}
	ss.SetIndex(idx, embedder)
	ss.SetIDs(ids)

	// With no exact-case match, the case-fold collision is ambiguous and normal
	// semantic ranking must decide; iteration order must not force Task-1 first.
	ranks := ss.ComputeSemanticResults("TASK-1")
	if len(ranks) != 2 || ranks[0].Index != 1 {
		t.Fatalf("ambiguous folded-ID ranks = %#v, want semantically stronger source index 1 first", ranks)
	}

	// A byte-exact match remains an unambiguous navigation intent even when a
	// differently-cased ID also exists.
	ranks = ss.ComputeSemanticResults("Task-1")
	if len(ranks) != 2 || ranks[0].Index != 0 {
		t.Fatalf("exact-case ID ranks = %#v, want exact source index 0 first", ranks)
	}
}

func TestSemanticSearchFilterMissingID(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Only add one vector but set two IDs
	idx.Upsert("id-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})

	ss.SetIDs([]string{"id-1", "id-missing"})

	// Use ComputeSemanticResults for synchronous computation (testing)
	ranks := ss.ComputeSemanticResults("query")

	// Should return results for both valid and missing IDs
	// Missing IDs are assigned a low score but included
	if len(ranks) != 2 {
		t.Errorf("Expected 2 ranks (valid + missing), got %d", len(ranks))
	}

	// First result should be id-1 (index 0) as it has a positive score
	if len(ranks) > 0 && ranks[0].Index != 0 {
		t.Errorf("Expected first rank index 0 (id-1), got %d", ranks[0].Index)
	}
}

func TestSemanticSearchFilterEmbedError(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			// Return wrong number of vectors
			return [][]float32{}, nil
		},
	}

	ss.SetIndex(idx, embedder)
	idx.Upsert("id-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})
	ss.SetIDs([]string{"id-1"})

	targets := []string{"a"}
	ranks := ss.Filter("query", targets)

	// When embed returns wrong count, falls back to DefaultFilter
	// DefaultFilter with a non-matching query may return empty
	// The important thing is it doesn't panic
	_ = ranks
}

// =============================================================================
// Non-blocking Filter Tests (async pattern)
// =============================================================================

func TestSemanticSearchFilterNonBlocking(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)
	idx.Upsert("id-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})
	idx.Upsert("id-2", search.ContentHash{}, []float32{0.0, 1.0, 0.0})
	ss.SetIDs([]string{"id-1", "id-2"})

	targets := []string{"a", "b"}

	// First call to Filter should return fuzzy results (non-blocking)
	// and mark the term as pending
	ranks := ss.Filter("query", targets)

	// Should get fuzzy results immediately (list.DefaultFilter behavior)
	// The exact result depends on DefaultFilter, but it should be non-empty
	if len(ranks) == 0 {
		// DefaultFilter with non-matching term returns empty, which is fine
	}

	// Should have a pending term
	pendingTerm := ss.GetPendingTerm()
	if pendingTerm != "query" {
		t.Errorf("Expected pending term 'query', got %q", pendingTerm)
	}

	// Now simulate async computation completing
	results := ss.ComputeSemanticResults("query")
	ss.SetCachedResults("query", results)

	// Pending should be cleared
	if ss.GetPendingTerm() != "" {
		t.Errorf("Expected pending term to be cleared, got %q", ss.GetPendingTerm())
	}

	// Second call to Filter should return cached semantic results
	ranks2 := ss.Filter("query", targets)
	if len(ranks2) != 2 {
		t.Errorf("Expected 2 cached ranks, got %d", len(ranks2))
	}
}

func TestSemanticSearchCacheManagement(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)
	idx.Upsert("id-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})
	ss.SetIDs([]string{"id-1"})

	// Test SetCachedResults
	results := ss.ComputeSemanticResults("test")
	ss.SetCachedResults("test", results)

	// Check that results are cached
	targets := []string{"a"}
	cachedRanks := ss.Filter("test", targets)
	if len(cachedRanks) != 1 {
		t.Errorf("Expected 1 cached rank, got %d", len(cachedRanks))
	}

	// Test ClearPending
	ss.Filter("new query", targets) // Mark as pending
	if ss.GetPendingTerm() != "new query" {
		t.Errorf("Expected pending term 'new query', got %q", ss.GetPendingTerm())
	}
	ss.ClearPending()
	if ss.GetPendingTerm() != "" {
		t.Errorf("Expected pending term to be cleared, got %q", ss.GetPendingTerm())
	}
}

func TestSemanticSearchCachesShareDistinctTermEvictionBoundary(t *testing.T) {
	ss := NewSemanticSearch()
	for i := 0; i < semanticCacheMaxTerms; i++ {
		term := fmt.Sprintf("term-%02d", i)
		ss.SetScores(term, map[string]SemanticScore{"issue": {Score: float64(i)}})
		ss.SetCachedResults(term, []list.Rank{{Index: i}})
	}

	// Replacing one of the twenty distinct terms must not evict either cache.
	ss.SetScores("term-00", map[string]SemanticScore{"issue": {Score: 99}})
	ss.SetCachedResults("term-00", []list.Rank{{Index: 99}})
	if got := len(ss.getScores().byTerm); got != semanticCacheMaxTerms {
		t.Fatalf("score cache size after replacement=%d, want %d", got, semanticCacheMaxTerms)
	}
	if got := len(ss.getCache().results); got != semanticCacheMaxTerms {
		t.Fatalf("result cache size after replacement=%d, want %d", got, semanticCacheMaxTerms)
	}
	if got := ss.getScores().byTerm["term-00"]["issue"].Score; got != 99 {
		t.Fatalf("replacement score=%v, want 99", got)
	}
	if got := ss.getCache().results["term-00"][0].Index; got != 99 {
		t.Fatalf("replacement rank index=%d, want 99", got)
	}

	// The twenty-first distinct term resets both caches at the same boundary.
	ss.SetScores("overflow", map[string]SemanticScore{"issue": {Score: 101}})
	if got := len(ss.getScores().byTerm); got != 1 {
		t.Fatalf("score cache size immediately after paired eviction=%d, want 1", got)
	}
	if got := len(ss.getCache().results); got != 0 {
		t.Fatalf("result cache size immediately after paired eviction=%d, want 0", got)
	}
	ss.SetCachedResults("overflow", []list.Rank{{Index: 101}})
	if got := len(ss.getScores().byTerm); got != 1 {
		t.Fatalf("score cache size after overflow=%d, want 1", got)
	}
	if got := len(ss.getCache().results); got != 1 {
		t.Fatalf("result cache size after overflow=%d, want 1", got)
	}
	if _, ok := ss.getScores().byTerm["term-00"]; ok {
		t.Fatal("score cache retained an evicted term")
	}
	if _, ok := ss.getCache().results["term-00"]; ok {
		t.Fatal("result cache retained an evicted term")
	}
}

func TestSemanticSearchBlankTermsDoNotEvictUsefulCaches(t *testing.T) {
	ss := NewSemanticSearch()
	for i := 0; i < semanticCacheMaxTerms; i++ {
		term := fmt.Sprintf("term-%02d", i)
		ss.SetScores(term, map[string]SemanticScore{"issue": {Score: float64(i)}})
		ss.SetCachedResults(term, []list.Rank{{Index: i}})
	}

	ss.SetScores("", map[string]SemanticScore{"blank": {Score: 1}})
	ss.SetCachedResults(" \t", []list.Rank{{Index: 99}})
	if got := len(ss.getScores().byTerm); got != semanticCacheMaxTerms {
		t.Fatalf("blank score term changed cache size=%d, want %d", got, semanticCacheMaxTerms)
	}
	if got := len(ss.getCache().results); got != semanticCacheMaxTerms {
		t.Fatalf("blank result term changed cache size=%d, want %d", got, semanticCacheMaxTerms)
	}
	if _, ok := ss.getScores().byTerm[""]; ok {
		t.Fatal("empty score term was cached")
	}
	if _, ok := ss.getCache().results[" \t"]; ok {
		t.Fatal("whitespace result term was cached")
	}

	targets := []string{"second", "first"}
	ranks := ss.Filter(" \t", targets)
	if len(ranks) != len(targets) {
		t.Fatalf("whitespace filter returned %d ranks, want all %d targets", len(ranks), len(targets))
	}
	for i, rank := range ranks {
		if rank.Index != i {
			t.Fatalf("whitespace filter rank %d index=%d, want original index %d", i, rank.Index, i)
		}
	}
	if pending := ss.GetPendingTerm(); pending != "" {
		t.Fatalf("whitespace filter left pending semantic term %q", pending)
	}
}

func TestSemanticSearchCachesResultsAndScoresPerTermAcrossABA(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(2)
	embedder := &mockEmbedder{
		dim: 2,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i, text := range texts {
				switch text {
				case "alpha":
					result[i] = []float32{1.0, 0.0}
				case "beta":
					result[i] = []float32{0.0, 1.0}
				default:
					result[i] = []float32{0.0, 0.0}
				}
			}
			return result, nil
		},
	}
	idx.Upsert("issue-alpha", search.ContentHash{}, []float32{1.0, 0.0})
	idx.Upsert("issue-beta", search.ContentHash{}, []float32{0.0, 1.0})
	ss.SetIndex(idx, embedder)
	ss.SetIDs([]string{"issue-alpha", "issue-beta"})

	alphaResults := ss.ComputeSemanticResults("alpha")
	ss.SetCachedResults("alpha", alphaResults)
	if len(alphaResults) != 2 || alphaResults[0].Index != 0 {
		t.Fatalf("alpha results = %#v, want issue-alpha first", alphaResults)
	}

	betaResults := ss.ComputeSemanticResults("beta")
	ss.SetCachedResults("beta", betaResults)
	if len(betaResults) != 2 || betaResults[0].Index != 1 {
		t.Fatalf("beta results = %#v, want issue-beta first", betaResults)
	}

	alphaAgain := ss.Filter("alpha", []string{"alpha target", "beta target"})
	if len(alphaAgain) != 2 || alphaAgain[0].Index != 0 {
		t.Fatalf("cached alpha results after A-B-A = %#v, want issue-alpha first", alphaAgain)
	}
	alphaScores, ok := ss.Scores("alpha")
	if !ok || alphaScores["issue-alpha"].Score != 1.0 {
		t.Fatalf("alpha scores after A-B-A = %#v, %t; want issue-alpha score 1.0", alphaScores, ok)
	}
	betaScores, ok := ss.Scores("beta")
	if !ok || betaScores["issue-beta"].Score != 1.0 {
		t.Fatalf("beta scores after A-B-A = %#v, %t; want issue-beta score 1.0", betaScores, ok)
	}
}

func TestSemanticSearchStateMutationsInvalidateCachedTerms(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*SemanticSearch)
	}{
		{
			name: "IDs",
			mutate: func(ss *SemanticSearch) {
				ss.SetIDs([]string{"new-id"})
			},
		},
		{
			name: "docs",
			mutate: func(ss *SemanticSearch) {
				ss.SetDocs(map[string]string{"issue-1": "new document"})
			},
		},
		{
			name: "documents",
			mutate: func(ss *SemanticSearch) {
				ss.SetDocuments([]string{"new-id"}, map[string]string{"new-id": "new document"})
			},
		},
		{
			name: "snapshot documents",
			mutate: func(ss *SemanticSearch) {
				ss.setSnapshotDocuments([]string{"new-id"}, map[string]string{"new-id": "new document"})
			},
		},
		{
			name: "hybrid config",
			mutate: func(ss *SemanticSearch) {
				ss.SetHybridConfig(true, search.PresetDefault)
			},
		},
		{
			name: "index",
			mutate: func(ss *SemanticSearch) {
				ss.SetIndex(search.NewVectorIndex(3), &mockEmbedder{dim: 3})
			},
		},
		{
			name: "metrics",
			mutate: func(ss *SemanticSearch) {
				ss.SetMetricsCache(&staticMetricsCache{metrics: map[string]search.IssueMetrics{"issue-1": {}}})
			},
		},
	}

	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			ss := NewSemanticSearch()
			ss.SetCachedResults("term", []list.Rank{{Index: 0}})
			ss.SetScores("term", map[string]SemanticScore{"issue-1": {Score: 0.75}})
			ss.MarkPending("term")

			tt.mutate(ss)

			if ss.HasCachedResults("term") {
				t.Fatal("cached ranks survived state mutation")
			}
			if scores, ok := ss.Scores("term"); ok {
				t.Fatalf("cached scores survived state mutation: %#v", scores)
			}
			if pending := ss.GetPendingTerm(); pending != "" {
				t.Fatalf("pending term survived state mutation: %q", pending)
			}
		})
	}
}

// dotFloat32 Tests
// =============================================================================

func TestDotFloat32(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float64
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 0.0,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{-1.0, 0.0},
			expected: -1.0,
		},
		{
			name:     "mixed values",
			a:        []float32{1.0, 2.0, 3.0},
			b:        []float32{4.0, 5.0, 6.0},
			expected: 32.0, // 1*4 + 2*5 + 3*6 = 4 + 10 + 18 = 32
		},
		{
			name:     "empty vectors",
			a:        []float32{},
			b:        []float32{},
			expected: 0.0,
		},
		{
			name:     "mismatched lengths",
			a:        []float32{1.0, 2.0},
			b:        []float32{1.0},
			expected: 0.0,
		},
		{
			name:     "single element",
			a:        []float32{3.0},
			b:        []float32{4.0},
			expected: 12.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dotFloat32(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("dotFloat32(%v, %v) = %v, want %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// SemanticIndexReadyMsg Tests
// =============================================================================

func TestSemanticIndexReadyMsg(t *testing.T) {
	// Test that the message struct works correctly
	msg := SemanticIndexReadyMsg{
		DataGeneration:  17,
		BuildGeneration: 19,
		Embedder:        &mockEmbedder{dim: 384},
		Index:           search.NewVectorIndex(384),
		IndexPath:       "/path/to/index",
		Loaded:          true,
		Stats:           search.IndexSyncStats{},
		Error:           nil,
	}

	if msg.DataGeneration != 17 {
		t.Errorf("DataGeneration = %d, want 17", msg.DataGeneration)
	}
	if msg.BuildGeneration != 19 {
		t.Errorf("BuildGeneration = %d, want 19", msg.BuildGeneration)
	}
	if msg.Embedder == nil {
		t.Error("Embedder should not be nil")
	}
	if msg.Index == nil {
		t.Error("Index should not be nil")
	}
	if msg.IndexPath != "/path/to/index" {
		t.Errorf("IndexPath = %q, want %q", msg.IndexPath, "/path/to/index")
	}
	if !msg.Loaded {
		t.Error("Loaded should be true")
	}
	if msg.Error != nil {
		t.Errorf("Error should be nil, got %v", msg.Error)
	}
}

func TestSemanticIndexReadyMsgWithError(t *testing.T) {
	testErr := context.DeadlineExceeded
	msg := SemanticIndexReadyMsg{
		DataGeneration:  23,
		BuildGeneration: 29,
		Error:           testErr,
	}

	if msg.DataGeneration != 23 {
		t.Errorf("DataGeneration = %d, want 23", msg.DataGeneration)
	}
	if msg.BuildGeneration != 29 {
		t.Errorf("BuildGeneration = %d, want 29", msg.BuildGeneration)
	}
	if msg.Error != testErr {
		t.Errorf("Error = %v, want %v", msg.Error, testErr)
	}
}

func TestComputeSemanticFilterCmdCarriesGenerationsWithoutMutatingScores(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			return [][]float32{{1.0, 0.0, 0.0}}, nil
		},
	}
	idx.Upsert("issue-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})
	ss.SetIndex(idx, embedder)
	ss.SetIDs([]string{"issue-1"})
	ss.SetScores("previous", map[string]SemanticScore{
		"issue-1": {Score: 0.25, TextScore: 0.25},
	})

	raw := ComputeSemanticFilterCmd(ss, "query", 31, 47)()
	msg, ok := raw.(SemanticFilterResultMsg)
	if !ok {
		t.Fatalf("ComputeSemanticFilterCmd returned %T, want SemanticFilterResultMsg", raw)
	}
	if msg.DataGeneration != 31 {
		t.Errorf("DataGeneration = %d, want 31", msg.DataGeneration)
	}
	if msg.QueryGeneration != 47 {
		t.Errorf("QueryGeneration = %d, want 47", msg.QueryGeneration)
	}
	if msg.Term != "query" {
		t.Errorf("Term = %q, want %q", msg.Term, "query")
	}
	if len(msg.Results) != 1 || msg.Results[0].Index != 0 {
		t.Fatalf("Results = %#v, want one rank for index 0", msg.Results)
	}
	if score, ok := msg.Scores["issue-1"]; !ok || score.Score != 1.0 {
		t.Errorf("Scores[issue-1] = %#v, %t; want score 1.0", score, ok)
	}

	if _, ok := ss.Scores("query"); ok {
		t.Fatal("async computation installed query scores before generation fencing")
	}
	if scores, ok := ss.Scores("previous"); !ok || scores["issue-1"].Score != 0.25 {
		t.Fatalf("previous scores were mutated: %#v, %t", scores, ok)
	}
}

func TestComputeSemanticResultsDirectStoresScores(t *testing.T) {
	ss := NewSemanticSearch()
	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			return [][]float32{{1.0, 0.0, 0.0}}, nil
		},
	}
	idx.Upsert("issue-1", search.ContentHash{}, []float32{1.0, 0.0, 0.0})
	ss.SetIndex(idx, embedder)
	ss.SetIDs([]string{"issue-1"})

	results := ss.ComputeSemanticResults("query")
	if len(results) != 1 || results[0].Index != 0 {
		t.Fatalf("ComputeSemanticResults() = %#v, want one rank for index 0", results)
	}
	scores, ok := ss.Scores("query")
	if !ok {
		t.Fatal("direct ComputeSemanticResults did not install scores")
	}
	if score := scores["issue-1"]; score.Score != 1.0 {
		t.Errorf("stored score = %#v, want score 1.0", score)
	}
}

func TestBuildHybridMetricsCmdCarriesGenerations(t *testing.T) {
	raw := BuildHybridMetricsCmd(nil, 59, 67)()
	msg, ok := raw.(HybridMetricsReadyMsg)
	if !ok {
		t.Fatalf("BuildHybridMetricsCmd returned %T, want HybridMetricsReadyMsg", raw)
	}
	if msg.DataGeneration != 59 {
		t.Errorf("DataGeneration = %d, want 59", msg.DataGeneration)
	}
	if msg.BuildGeneration != 67 {
		t.Errorf("BuildGeneration = %d, want 67", msg.BuildGeneration)
	}
	if msg.Error != nil {
		t.Fatalf("BuildHybridMetricsCmd returned error: %v", msg.Error)
	}
	if msg.Cache == nil {
		t.Fatal("BuildHybridMetricsCmd returned a nil cache")
	}
}

func TestBuildSemanticIndexCmdErrorCarriesGenerations(t *testing.T) {
	t.Setenv(search.EnvSemanticEmbedder, string(search.ProviderOpenAI))

	raw := BuildSemanticIndexCmd(nil, 61, 71)()
	msg, ok := raw.(SemanticIndexReadyMsg)
	if !ok {
		t.Fatalf("BuildSemanticIndexCmd returned %T, want SemanticIndexReadyMsg", raw)
	}
	if msg.DataGeneration != 61 {
		t.Errorf("DataGeneration = %d, want 61", msg.DataGeneration)
	}
	if msg.BuildGeneration != 71 {
		t.Errorf("BuildGeneration = %d, want 71", msg.BuildGeneration)
	}
	if msg.Error == nil {
		t.Fatal("BuildSemanticIndexCmd unexpectedly succeeded with placeholder provider")
	}
}

func TestNewModelInitializesSemanticDocuments(t *testing.T) {
	issue := model.Issue{
		ID:          "issue-1",
		Title:       "Semantic document title",
		Description: "Semantic document description",
		Status:      model.StatusOpen,
		Labels:      []string{"search", "regression"},
	}
	m := NewModel([]model.Issue{issue}, nil, "")

	snap := m.semanticSearch.Snapshot()
	if len(snap.IDs) != 1 || snap.IDs[0] != issue.ID {
		t.Fatalf("initial semantic IDs = %#v, want [%q]", snap.IDs, issue.ID)
	}
	wantDoc := search.IssueDocument(issue)
	if got := snap.Docs[issue.ID]; got != wantDoc {
		t.Fatalf("initial semantic document = %q, want %q", got, wantDoc)
	}
}

func TestModelClearsPendingSemanticFilterAfterEmbedError(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Alpha", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.list.Filter = m.semanticSearch.Filter
	m.list.SetFilterText("alpha")
	m.list.SetFilterState(list.FilterApplied)

	idx := search.NewVectorIndex(3)
	failingEmbedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			return nil, context.DeadlineExceeded
		},
	}
	m.semanticSearch.SetIndex(idx, failingEmbedder)
	m.semanticSearch.MarkPending("alpha")

	cmd := m.startSemanticFilter("alpha")
	if cmd == nil {
		t.Fatal("startSemanticFilter returned nil for a ready active query")
	}
	raw := cmd()
	msg, ok := raw.(SemanticFilterResultMsg)
	if !ok {
		t.Fatalf("semantic filter command returned %T, want SemanticFilterResultMsg", raw)
	}
	if msg.Error == nil {
		t.Fatal("semantic filter command unexpectedly succeeded with failing embedder")
	}

	updated, _ := m.Update(msg)
	m = updated.(*Model)
	if m.semanticFilterBuilding {
		t.Fatal("embed error left semantic filter build pending")
	}
	if pending := m.semanticSearch.GetPendingTerm(); pending != "" {
		t.Fatalf("embed error left pending term %q", pending)
	}
	if !m.statusIsError {
		t.Fatal("embed error did not set error status")
	}
	if cmd := m.pendingSemanticFilterCmd(); cmd != nil {
		t.Fatal("embed error immediately rescheduled the failed query")
	}
}

func TestStartSemanticFilterRejectsWhitespaceQuery(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Alpha", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.list.Filter = m.semanticSearch.Filter
	m.list.SetFilterText(" \t")
	m.list.SetFilterState(list.FilterApplied)
	m.semanticSearch.SetIndex(search.NewVectorIndex(3), &mockEmbedder{dim: 3})

	if cmd := m.startSemanticFilter(" \t"); cmd != nil {
		t.Fatal("whitespace-only query scheduled a semantic embedding")
	}
	if m.semanticFilterBuilding {
		t.Fatal("whitespace-only query marked a semantic filter build active")
	}
}

func newProgrammaticSemanticRefilterModel(t *testing.T) *Model {
	t.Helper()
	issues := []model.Issue{
		{ID: "repo-a-1", Title: "Alpha backend", Status: model.StatusOpen, IssueType: model.TypeTask, Labels: []string{"backend"}, SourceRepo: "repo-a"},
		{ID: "repo-b-1", Title: "Alpha frontend", Status: model.StatusOpen, IssueType: model.TypeTask, Labels: []string{"frontend"}, SourceRepo: "repo-b"},
	}
	m := NewModel(issues, nil, "")
	idx := search.NewVectorIndex(3)
	for _, issue := range issues {
		if err := idx.Upsert(issue.ID, search.ContentHash{}, []float32{1, 0, 0}); err != nil {
			t.Fatalf("upsert semantic fixture %s: %v", issue.ID, err)
		}
	}
	m.semanticSearch.SetIndex(idx, &mockEmbedder{dim: 3})
	m.semanticSearchEnabled = true
	m.list.Filter = m.semanticSearch.Filter
	m.list.SetFilterText("alpha")
	m.list.SetFilterState(list.FilterApplied)
	m.lastSearchTerm = "alpha"
	m.semanticSearch.ClearPending()
	return m
}

func TestProgrammaticSemanticRefiltersSchedulePendingWorkBeforeEarlyReturn(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
		key   tea.KeyMsg
	}{
		{
			name: "ctrl+s toggle",
			setup: func(m *Model) {
				m.semanticSearchEnabled = false
				m.list.Filter = list.DefaultFilter
				m.list.SetFilterText("alpha")
				m.list.SetFilterState(list.FilterApplied)
				m.semanticSearch.ClearPending()
			},
			key: tea.KeyMsg{Type: tea.KeyCtrlS},
		},
		{
			name: "recipe picker",
			setup: func(m *Model) {
				m.recipePicker = NewRecipePickerModel([]recipe.Recipe{{Name: "everything"}}, m.theme)
				m.showRecipePicker = true
				m.focused = focusRecipePicker
			},
			key: tea.KeyMsg{Type: tea.KeyEnter},
		},
		{
			name: "repo picker",
			setup: func(m *Model) {
				m.workspaceMode = true
				m.availableRepos = []string{"repo-a", "repo-b"}
				m.repoPicker = NewRepoPickerModel(m.availableRepos, m.theme)
				m.repoPicker.selected["repo-b"] = false
				m.showRepoPicker = true
				m.focused = focusRepoPicker
			},
			key: tea.KeyMsg{Type: tea.KeyEnter},
		},
		{
			name: "label picker",
			setup: func(m *Model) {
				m.labelPicker = NewLabelPickerModel([]string{"backend", "frontend"}, map[string]int{"backend": 1, "frontend": 1}, m.theme)
				m.showLabelPicker = true
				m.focused = focusLabelPicker
			},
			key: tea.KeyMsg{Type: tea.KeyEnter},
		},
		{
			name: "time travel input",
			setup: func(m *Model) {
				// A successful time-travel submission rebuilds the list before
				// this input-specific branch returns. Seed that same pending state
				// without depending on the test checkout's git history.
				m.applyFilter()
				m.showTimeTravelPrompt = true
				m.focused = focusTimeTravelInput
			},
			key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProgrammaticSemanticRefilterModel(t)
			tt.setup(m)

			updated, cmd := m.Update(tt.key)
			m = updated.(*Model)
			if pending := m.semanticSearch.GetPendingTerm(); pending != "alpha" {
				t.Fatalf("pending semantic term=%q, want alpha", pending)
			}
			if cmd == nil {
				t.Fatal("programmatic refilter returned before scheduling pending semantic work")
			}
		})
	}
}

func TestTimeTravelInputPreservesTextInputCommandWithoutSemanticWork(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.showTimeTravelPrompt = true
	m.focused = focusTimeTravelInput
	m.timeTravelInput.Focus()
	if m.semanticSearch != nil {
		m.semanticSearch.ClearPending()
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("time-travel input discarded the textinput paste command")
	}
}

func TestLabelPickerPreservesTextInputCommandWithoutSemanticWork(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.labelPicker = NewLabelPickerModel([]string{"backend"}, map[string]int{"backend": 1}, m.theme)
	m.showLabelPicker = true
	m.focused = focusLabelPicker
	_ = m.labelPicker.Focus()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("label picker discarded the textinput paste command")
	}
}

func TestHistorySearchPreservesTextInputCommandWithoutSemanticWork(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.isHistoryView = true
	m.focused = focusHistory
	m.historyView.StartSearch()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("history search discarded the textinput paste command")
	}
}

func TestEmbeddedTextInputNonKeyFollowUpsAreRouted(t *testing.T) {
	tests := []struct {
		name     string
		target   embeddedTextInputTarget
		activate func(*Model)
	}{
		{
			name:   "time travel",
			target: embeddedTextInputTimeTravel,
			activate: func(m *Model) {
				m.showTimeTravelPrompt = true
				m.focused = focusTimeTravelInput
				_ = m.timeTravelInput.Focus()
			},
		},
		{
			name:   "label picker",
			target: embeddedTextInputLabelPicker,
			activate: func(m *Model) {
				m.labelPicker = NewLabelPickerModel([]string{"backend"}, map[string]int{"backend": 1}, m.theme)
				m.showLabelPicker = true
				m.focused = focusLabelPicker
				_ = m.labelPicker.Focus()
			},
		},
		{
			name:   "history search",
			target: embeddedTextInputHistorySearch,
			activate: func(m *Model) {
				m.isHistoryView = true
				m.focused = focusHistory
				_ = m.historyView.StartSearch()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(nil, nil, "")
			tt.activate(m)

			initialCmd := m.beginEmbeddedTextInputSession(tt.target, func() tea.Msg {
				return textinput.Blink()
			})
			if initialCmd == nil {
				t.Fatal("scoped non-key command is nil")
			}
			initialMsg := initialCmd()
			msg, ok := initialMsg.(embeddedTextInputMsg)
			if !ok {
				t.Fatalf("scoped command returned %T, want embeddedTextInputMsg", initialMsg)
			}

			updated, followUp := m.Update(msg)
			m = updated.(*Model)
			if followUp == nil {
				t.Fatal("active input dropped the non-key blink message or its follow-up command")
			}
			if m.embeddedTextInputSession != msg.session {
				t.Fatalf("active session changed: got %+v, want %+v", m.embeddedTextInputSession, msg.session)
			}
		})
	}
}

func TestEmbeddedTextInputBatchChildrenRetainSessionRecursively(t *testing.T) {
	session := embeddedTextInputSession{target: embeddedTextInputTimeTravel, generation: 7}
	wrapped := wrapEmbeddedTextInputCmd(session, func() tea.Msg {
		return tea.BatchMsg{
			func() tea.Msg { return "first" },
			func() tea.Msg {
				return tea.BatchMsg{
					func() tea.Msg { return "nested" },
				}
			},
		}
	})

	wrappedMsg := wrapped()
	batch, ok := wrappedMsg.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("wrapped command returned %#v, want two-command tea.BatchMsg", wrappedMsg)
	}
	first, ok := batch[0]().(embeddedTextInputMsg)
	if !ok || first.session != session || first.msg != "first" {
		t.Fatalf("first child lost session: %#v", first)
	}
	nestedResult := batch[1]()
	nested, ok := nestedResult.(tea.BatchMsg)
	if !ok || len(nested) != 1 {
		t.Fatalf("nested child returned %#v, want one-command tea.BatchMsg", nestedResult)
	}
	nestedMsg, ok := nested[0]().(embeddedTextInputMsg)
	if !ok || nestedMsg.session != session || nestedMsg.msg != "nested" {
		t.Fatalf("nested child lost session: %#v", nestedMsg)
	}
}

func TestEmbeddedTextInputDropsMessageFromEarlierActivation(t *testing.T) {
	m := NewModel(nil, nil, "")
	m.showTimeTravelPrompt = true
	m.focused = focusTimeTravelInput
	_ = m.timeTravelInput.Focus()
	_ = m.beginEmbeddedTextInputSession(embeddedTextInputTimeTravel, nil)
	staleSession := m.embeddedTextInputSession

	m.timeTravelInput.Blur()
	m.showTimeTravelPrompt = false
	m.focused = focusList
	m.endEmbeddedTextInputSession(embeddedTextInputTimeTravel)

	m.showTimeTravelPrompt = true
	m.focused = focusTimeTravelInput
	_ = m.beginEmbeddedTextInputSession(embeddedTextInputTimeTravel, m.timeTravelInput.Focus())
	if m.embeddedTextInputSession == staleSession {
		t.Fatal("reopened input reused its previous session")
	}
	m.timeTravelInput.SetValue("fresh")

	updated, cmd := m.Update(embeddedTextInputMsg{
		session: staleSession,
		msg:     tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("stale")},
	})
	m = updated.(*Model)
	if cmd != nil {
		t.Fatal("stale input message returned a follow-up command")
	}
	if got := m.timeTravelInput.Value(); got != "fresh" {
		t.Fatalf("stale input message mutated reopened input: got %q", got)
	}
}

func TestEmbeddedTextInputOpenPathsReturnFocusCommands(t *testing.T) {
	t.Run("time travel", func(t *testing.T) {
		m := NewModel(nil, nil, "")
		m.focused = focusList
		_, cmd := m.handleListKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
		if cmd == nil {
			t.Fatal("time-travel open discarded its focus command")
		}
	})

	t.Run("history search", func(t *testing.T) {
		m := NewModel(nil, nil, "")
		m.isHistoryView = true
		m.focused = focusHistory
		_, cmd := m.handleHistoryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		if cmd == nil {
			t.Fatal("history search open discarded its focus command")
		}
	})

	t.Run("label picker", func(t *testing.T) {
		m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Labels: []string{"backend"}}}, nil, "")
		m.focused = focusList
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		m = updated.(*Model)
		if !m.showLabelPicker || m.focused != focusLabelPicker {
			t.Fatal("label picker did not open")
		}
		if cmd == nil {
			t.Fatal("label picker open discarded its focus command")
		}
	})
}

func TestSemanticIndexPersistenceIsAsyncSerializedAndGenerationSafe(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.semanticDataGeneration = 7
	m.semanticIndexBuilding = true
	m.semanticIndexBuildData = 7
	m.semanticIndexBuildGen = 11

	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	started := make(chan string, 2)
	m.semanticIndexSaver = func(_ *search.VectorIndex, path string) error {
		started <- path
		switch path {
		case "first-index":
			<-firstRelease
			return context.DeadlineExceeded
		case "second-index":
			<-secondRelease
		}
		return nil
	}

	firstIndex := search.NewVectorIndex(3)
	updated, firstSaveCmd := m.Update(SemanticIndexReadyMsg{
		DataGeneration:  7,
		BuildGeneration: 11,
		Index:           firstIndex,
		Embedder:        &mockEmbedder{dim: 3},
		IndexPath:       "first-index",
		NeedsSave:       true,
	})
	m = updated.(*Model)
	if firstSaveCmd == nil {
		t.Fatal("accepted index did not schedule asynchronous persistence")
	}
	select {
	case path := <-started:
		t.Fatalf("Update invoked blocking saver synchronously for %q", path)
	default:
	}
	if m.semanticSearch.Snapshot().Index != firstIndex {
		t.Fatal("accepted in-memory index was not installed before persistence")
	}

	firstDone := make(chan tea.Msg, 1)
	go func() { firstDone <- firstSaveCmd() }()
	select {
	case path := <-started:
		if path != "first-index" {
			t.Fatalf("first saver path=%q, want first-index", path)
		}
	case <-time.After(time.Second):
		t.Fatal("first asynchronous saver did not start")
	}

	// Accept a newer build while the first physical write is blocked. It must
	// replace the pending request without starting a concurrent disk write.
	m.semanticIndexBuilding = true
	m.semanticIndexBuildData = 7
	m.semanticIndexBuildGen = 12
	secondIndex := search.NewVectorIndex(3)
	updated, _ = m.Update(SemanticIndexReadyMsg{
		DataGeneration:  7,
		BuildGeneration: 12,
		Index:           secondIndex,
		Embedder:        &mockEmbedder{dim: 3},
		IndexPath:       "second-index",
		// This build observed an up-to-date disk before the older save finished.
		// It still has to be queued so the older physical write cannot finish last.
		NeedsSave: false,
	})
	m = updated.(*Model)
	if m.semanticSearch.Snapshot().Index != secondIndex {
		t.Fatal("newer accepted in-memory index was not installed")
	}
	if m.semanticIndexSavePending == nil || m.semanticIndexSavePending.BuildGeneration != 12 {
		t.Fatal("newer accepted save did not replace the pending request")
	}
	select {
	case path := <-started:
		t.Fatalf("newer saver %q started concurrently with the blocked older save", path)
	default:
	}

	close(firstRelease)
	var firstResult tea.Msg
	select {
	case firstResult = <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first saver did not finish after release")
	}
	updated, secondSaveCmd := m.Update(firstResult)
	m = updated.(*Model)
	if !m.semanticSearchEnabled {
		t.Fatal("stale save error disabled the newer accepted semantic index")
	}
	if secondSaveCmd == nil {
		t.Fatal("completion of first save did not schedule the pending newer save")
	}

	secondDone := make(chan tea.Msg, 1)
	go func() { secondDone <- secondSaveCmd() }()
	select {
	case path := <-started:
		if path != "second-index" {
			t.Fatalf("second saver path=%q, want second-index", path)
		}
	case <-time.After(time.Second):
		t.Fatal("second saver did not start after first completed")
	}
	close(secondRelease)
	select {
	case secondResult := <-secondDone:
		updated, _ = m.Update(secondResult)
		m = updated.(*Model)
	case <-time.After(time.Second):
		t.Fatal("second saver did not finish after release")
	}
	if m.semanticIndexSaveActive != nil || m.semanticIndexSavePending != nil {
		t.Fatal("semantic save queue did not drain")
	}
}

func TestModelIgnoresOlderSemanticBuildAttemptForSameData(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.semanticHybridEnabled = true
	m.semanticDataGeneration = 9
	m.semanticIndexBuilding = true
	m.semanticIndexBuildData = 9
	m.semanticIndexBuildGen = 42
	m.semanticHybridBuilding = true
	m.semanticHybridBuildData = 9
	m.semanticHybridBuildGen = 52

	oldIndex := search.NewVectorIndex(3)
	updated, _ := m.Update(SemanticIndexReadyMsg{
		DataGeneration:  9,
		BuildGeneration: 41,
		Index:           oldIndex,
		Embedder:        &mockEmbedder{dim: 3},
		Error:           context.DeadlineExceeded,
	})
	m = updated.(*Model)
	if !m.semanticIndexBuilding {
		t.Fatal("older same-data index attempt cleared the current build state")
	}
	if !m.semanticSearchEnabled {
		t.Fatal("older same-data index error disabled semantic search")
	}
	if m.semanticSearch.Snapshot().Index == oldIndex {
		t.Fatal("older same-data index attempt was installed")
	}

	oldCache := &staticMetricsCache{metrics: map[string]search.IssueMetrics{"old": {}}}
	updated, _ = m.Update(HybridMetricsReadyMsg{
		DataGeneration:  9,
		BuildGeneration: 51,
		Cache:           oldCache,
		Error:           context.DeadlineExceeded,
	})
	m = updated.(*Model)
	if !m.semanticHybridBuilding {
		t.Fatal("older same-data hybrid attempt cleared the current build state")
	}
	if !m.semanticHybridEnabled {
		t.Fatal("older same-data hybrid error disabled hybrid search")
	}
	if m.semanticSearch.getMetricsCache() == oldCache {
		t.Fatal("older same-data hybrid attempt was installed")
	}

	currentIndex := search.NewVectorIndex(3)
	updated, _ = m.Update(SemanticIndexReadyMsg{
		DataGeneration:  9,
		BuildGeneration: 42,
		Index:           currentIndex,
		Embedder:        &mockEmbedder{dim: 3},
	})
	m = updated.(*Model)
	if m.semanticIndexBuilding || m.semanticSearch.Snapshot().Index != currentIndex {
		t.Fatal("current same-data index attempt was not accepted")
	}

	currentCache := &staticMetricsCache{metrics: map[string]search.IssueMetrics{"current": {}}}
	updated, _ = m.Update(HybridMetricsReadyMsg{
		DataGeneration:  9,
		BuildGeneration: 52,
		Cache:           currentCache,
	})
	m = updated.(*Model)
	if m.semanticHybridBuilding || m.semanticSearch.getMetricsCache() != currentCache {
		t.Fatal("current same-data hybrid attempt was not accepted")
	}
}

func TestModelIgnoresStaleSemanticBuildResults(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.semanticHybridEnabled = true
	m.semanticDataGeneration = 9
	m.semanticIndexBuilding = true
	m.semanticIndexBuildData = 9
	m.semanticIndexBuildGen = 17
	m.semanticHybridBuilding = true
	m.semanticHybridBuildData = 9
	m.semanticHybridBuildGen = 23

	staleIndex := search.NewVectorIndex(3)
	updated, _ := m.Update(SemanticIndexReadyMsg{
		DataGeneration:  8,
		BuildGeneration: 17,
		Index:           staleIndex,
		Embedder:        &mockEmbedder{dim: 3},
	})
	m = updated.(*Model)
	if !m.semanticIndexBuilding {
		t.Fatal("stale index result cleared the current build state")
	}
	if m.semanticSearch.Snapshot().Ready {
		t.Fatal("stale index result was installed")
	}

	staleCache := &staticMetricsCache{metrics: map[string]search.IssueMetrics{"stale": {}}}
	updated, _ = m.Update(HybridMetricsReadyMsg{DataGeneration: 8, BuildGeneration: 23, Cache: staleCache})
	m = updated.(*Model)
	if !m.semanticHybridBuilding {
		t.Fatal("stale hybrid result cleared the current build state")
	}
	if m.semanticSearch.getMetricsCache() != nil {
		t.Fatal("stale hybrid metrics were installed")
	}

	currentIndex := search.NewVectorIndex(3)
	updated, _ = m.Update(SemanticIndexReadyMsg{
		DataGeneration:  9,
		BuildGeneration: 17,
		Index:           currentIndex,
		Embedder:        &mockEmbedder{dim: 3},
	})
	m = updated.(*Model)
	if m.semanticIndexBuilding || m.semanticSearch.Snapshot().Index != currentIndex {
		t.Fatal("current index result was not accepted")
	}

	currentCache := &staticMetricsCache{metrics: map[string]search.IssueMetrics{"current": {}}}
	updated, _ = m.Update(HybridMetricsReadyMsg{DataGeneration: 9, BuildGeneration: 23, Cache: currentCache})
	m = updated.(*Model)
	if m.semanticHybridBuilding || m.semanticSearch.getMetricsCache() != currentCache {
		t.Fatal("current hybrid result was not accepted")
	}
}

func TestModelIgnoresStaleSemanticFilterResults(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Alpha", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.list.Filter = m.semanticSearch.Filter
	m.list.SetFilterText("alpha")
	m.semanticDataGeneration = 5
	m.semanticQueryGeneration = 12
	m.semanticFilterBuilding = true
	m.semanticFilterDataGen = 5
	m.semanticFilterQueryGen = 12
	m.semanticFilterTerm = "alpha"
	m.lastSearchTerm = "alpha"

	updated, _ := m.Update(SemanticFilterResultMsg{
		DataGeneration:  4,
		QueryGeneration: 11,
		Term:            "alpha",
		Results:         []list.Rank{{Index: 0}},
		Scores:          map[string]SemanticScore{"issue-1": {Score: 0.1}},
	})
	m = updated.(*Model)
	if !m.semanticFilterBuilding {
		t.Fatal("stale filter result cleared the current filter build")
	}
	if _, ok := m.semanticSearch.Scores("alpha"); ok {
		t.Fatal("stale filter scores were installed")
	}

	updated, _ = m.Update(SemanticFilterResultMsg{
		DataGeneration:  5,
		QueryGeneration: 12,
		Term:            "alpha",
		Results:         []list.Rank{{Index: 0}},
		Scores:          map[string]SemanticScore{"issue-1": {Score: 0.9}},
	})
	m = updated.(*Model)
	if m.semanticFilterBuilding {
		t.Fatal("current filter result did not clear build state")
	}
	if scores, ok := m.semanticSearch.Scores("alpha"); !ok || scores["issue-1"].Score != 0.9 {
		t.Fatalf("current filter scores not installed: %#v, %t", scores, ok)
	}
}

func TestSnapshotReloadStartsCurrentSemanticBuildGeneration(t *testing.T) {
	m := NewModel([]model.Issue{{ID: "old", Title: "Old", Status: model.StatusOpen}}, nil, "")
	m.semanticSearchEnabled = true
	m.semanticHybridEnabled = true
	m.semanticIndexBuilding = true
	m.semanticIndexBuildData = m.semanticDataGeneration
	m.semanticIndexBuildGen = 7
	m.semanticHybridBuilding = true
	m.semanticHybridBuildData = m.semanticDataGeneration
	m.semanticHybridBuildGen = 11
	oldIndexBuildGen := m.semanticIndexBuildGen
	oldHybridBuildGen := m.semanticHybridBuildGen

	next := NewSnapshotBuilder([]model.Issue{{ID: "new", Title: "New", Status: model.StatusOpen}}).Build()
	next.Analysis = nil
	updated, cmd := m.Update(SnapshotReadyMsg{Snapshot: next})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("reload did not schedule current semantic builds")
	}
	if m.semanticDataGeneration != 2 {
		t.Fatalf("semantic data generation=%d, want 2", m.semanticDataGeneration)
	}
	if !m.semanticIndexBuilding || m.semanticIndexBuildData != m.semanticDataGeneration {
		t.Fatalf("index build state=(%t, data %d), want current data generation %d", m.semanticIndexBuilding, m.semanticIndexBuildData, m.semanticDataGeneration)
	}
	if m.semanticIndexBuildGen <= oldIndexBuildGen {
		t.Fatalf("index build generation=%d, want newer than %d", m.semanticIndexBuildGen, oldIndexBuildGen)
	}
	if !m.semanticHybridBuilding || m.semanticHybridBuildData != m.semanticDataGeneration {
		t.Fatalf("hybrid build state=(%t, data %d), want current data generation %d", m.semanticHybridBuilding, m.semanticHybridBuildData, m.semanticDataGeneration)
	}
	if m.semanticHybridBuildGen <= oldHybridBuildGen {
		t.Fatalf("hybrid build generation=%d, want newer than %d", m.semanticHybridBuildGen, oldHybridBuildGen)
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestSemanticSearchFullWorkflow(t *testing.T) {
	// Create semantic search
	ss := NewSemanticSearch()

	// Verify initial state
	snap := ss.Snapshot()
	if snap.Ready {
		t.Error("Should not be ready initially")
	}

	// Set up index and embedder
	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{
		dim: 3,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				// Return consistent query vector
				result[i] = []float32{1.0, 0.0, 0.0}
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Add some vectors
	idx.Upsert("bug-1", search.ContentHash{}, []float32{0.9, 0.1, 0.0})
	idx.Upsert("feat-1", search.ContentHash{}, []float32{0.1, 0.9, 0.0})
	idx.Upsert("bug-2", search.ContentHash{}, []float32{0.8, 0.2, 0.0})

	ss.SetIDs([]string{"bug-1", "feat-1", "bug-2"})

	// Verify ready state
	snap = ss.Snapshot()
	if !snap.Ready {
		t.Error("Should be ready after setup")
	}

	// Test filtering
	targets := []string{"Fix login bug", "Add dark mode", "Fix crash bug"}
	ranks := ss.Filter("bug", targets)

	if len(ranks) == 0 {
		t.Fatal("Expected some filter results")
	}

	// bug-1 should rank highest (0.9 similarity with query)
	if ranks[0].Index != 0 {
		t.Errorf("Expected bug-1 (index 0) to rank first, got index %d", ranks[0].Index)
	}
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestSemanticSearchConcurrentAccess(t *testing.T) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(3)
	embedder := &mockEmbedder{dim: 3}
	ss.SetIndex(idx, embedder)

	// Concurrent reads and writes
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			ss.SetIDs([]string{"id-1", "id-2"})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = ss.Snapshot()
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// If we get here without panic, the test passes
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSemanticSearchFilter(b *testing.B) {
	ss := NewSemanticSearch()

	idx := search.NewVectorIndex(384)
	embedder := &mockEmbedder{
		dim: 384,
		embedFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = make([]float32, 384)
				result[i][0] = 1.0
			}
			return result, nil
		},
	}

	ss.SetIndex(idx, embedder)

	// Add some vectors
	ids := make([]string, 100)
	targets := make([]string, 100)
	for i := 0; i < 100; i++ {
		ids[i] = "id-" + string(rune('A'+i%26))
		targets[i] = "target " + ids[i]
		vec := make([]float32, 384)
		vec[i%384] = 1.0
		idx.Upsert(ids[i], search.ContentHash{}, vec)
	}
	ss.SetIDs(ids)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ss.Filter("search query", targets)
	}
}

func BenchmarkDotFloat32(b *testing.B) {
	a := make([]float32, 384)
	bVec := make([]float32, 384)
	for i := range a {
		a[i] = float32(i) / 384.0
		bVec[i] = float32(384-i) / 384.0
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dotFloat32(a, bVec)
	}
}
