package search

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestAnalyzerMetricsLoader_ImmutableHashHasNoPerLookupAllocation(t *testing.T) {
	issues, err := testutil.PerformanceIssues("realistic", 1000, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	loader := NewAnalyzerMetricsLoader(issues)
	want := analysis.ComputeDataHash(issues)
	if got, err := loader.ComputeDataHash(); err != nil || got != want {
		t.Fatalf("snapshot hash=%q want=%q error=%v", got, want, err)
	}
	// A warm lookup of an immutable snapshot must not rebuild O(N) issue
	// fingerprints. The real loader used to allocate ~80k objects per lookup.
	allocations := testing.AllocsPerRun(3, func() {
		if got, err := loader.ComputeDataHash(); err != nil || got != want {
			t.Fatalf("warm snapshot hash=%q want=%q error=%v", got, want, err)
		}
	})
	if allocations != 0 {
		t.Fatalf("immutable snapshot lookup allocated %.0f objects; want 0", allocations)
	}
	issues[0].Title += " changed source"
	changed := NewAnalyzerMetricsLoader(issues)
	newHash, err := changed.ComputeDataHash()
	if err != nil || newHash == want {
		t.Fatalf("new snapshot must identify changed source: hash=%q error=%v", newHash, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := loader.ComputeDataHash(); err != nil || got != want {
				t.Errorf("concurrent original snapshot hash=%q want=%q error=%v", got, want, err)
			}
		}()
	}
	wg.Wait()
}

type stubMetricsLoader struct {
	metrics   map[string]IssueMetrics
	hash      string
	loadErr   error
	hashErr   error
	loadCalls int
}

func (s *stubMetricsLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.metrics, nil
}

func (s *stubMetricsLoader) ComputeDataHash() (string, error) {
	if s.hashErr != nil {
		return "", s.hashErr
	}
	return s.hash, nil
}

func TestMetricsCache_Get_RefreshesOnHashChange(t *testing.T) {
	loader := &stubMetricsLoader{
		hash: "hash1",
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.1, BlockerCount: 2},
		},
	}
	cache := NewMetricsCache(loader)

	metric, ok := cache.Get("A")
	if !ok {
		t.Fatal("expected metric to be found")
	}
	if metric.PageRank != 0.1 {
		t.Fatalf("expected PageRank 0.1, got %f", metric.PageRank)
	}
	if loader.loadCalls != 1 {
		t.Fatalf("expected 1 load call, got %d", loader.loadCalls)
	}

	loader.metrics["A"] = IssueMetrics{IssueID: "A", PageRank: 0.2}
	metric, ok = cache.Get("A")
	if !ok {
		t.Fatal("expected metric to be found on cache hit")
	}
	if metric.PageRank != 0.1 {
		t.Fatalf("expected cached PageRank 0.1, got %f", metric.PageRank)
	}
	if loader.loadCalls != 1 {
		t.Fatalf("expected no additional load calls, got %d", loader.loadCalls)
	}

	loader.hash = "hash2"
	loader.metrics = map[string]IssueMetrics{
		"A": {IssueID: "A", PageRank: 0.2, BlockerCount: 1},
	}
	metric, ok = cache.Get("A")
	if !ok {
		t.Fatal("expected metric to be found after refresh")
	}
	if metric.PageRank != 0.2 {
		t.Fatalf("expected refreshed PageRank 0.2, got %f", metric.PageRank)
	}
	if loader.loadCalls != 2 {
		t.Fatalf("expected 2 load calls after refresh, got %d", loader.loadCalls)
	}
}

func TestMetricsCache_GetBatch_DefaultsForMissing(t *testing.T) {
	loader := &stubMetricsLoader{
		hash: "hash1",
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.3},
		},
	}
	cache := NewMetricsCache(loader)

	results := cache.GetBatch([]string{"A", "B"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results["A"].PageRank != 0.3 {
		t.Fatalf("expected A PageRank 0.3, got %f", results["A"].PageRank)
	}
	if results["B"].PageRank != defaultPageRank {
		t.Fatalf("expected B default PageRank %f, got %f", defaultPageRank, results["B"].PageRank)
	}
	if results["B"].Priority != 2 {
		t.Fatalf("expected B default priority 2, got %d", results["B"].Priority)
	}
}

func TestMetricsCache_Get_ReturnsDefaultOnError(t *testing.T) {
	loader := &stubMetricsLoader{hashErr: errors.New("boom")}
	cache := NewMetricsCache(loader)

	metric, ok := cache.Get("A")
	if ok {
		t.Fatal("expected ok=false on loader error")
	}
	if metric.PageRank != defaultPageRank {
		t.Fatalf("expected default PageRank %f, got %f", defaultPageRank, metric.PageRank)
	}
}

func TestMetricsCache_GetAndBatch_UseStaleCacheOnHashError(t *testing.T) {
	loader := &stubMetricsLoader{
		hash: "hash1",
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.42, BlockerCount: 3},
		},
	}
	cache := NewMetricsCache(loader)

	if err := cache.Refresh(); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}

	loader.hashErr = errors.New("transient hash failure")
	loader.metrics["A"] = IssueMetrics{IssueID: "A", PageRank: 0.99, BlockerCount: 9}

	metric, ok := cache.Get("A")
	if !ok {
		t.Fatal("expected stale metric to remain available")
	}
	if metric.PageRank != 0.42 {
		t.Fatalf("expected stale cached PageRank 0.42, got %f", metric.PageRank)
	}

	batch := cache.GetBatch([]string{"A", "B"})
	if batch["A"].PageRank != 0.42 {
		t.Fatalf("expected stale cached batch PageRank 0.42, got %f", batch["A"].PageRank)
	}
	if batch["B"].PageRank != defaultPageRank {
		t.Fatalf("expected default PageRank for uncached issue, got %f", batch["B"].PageRank)
	}
}

func TestAnalyzerMetricsLoader_LoadMetrics(t *testing.T) {
	now := time.Date(2025, 12, 18, 12, 0, 0, 0, time.UTC)
	dep := &model.Dependency{
		IssueID:     "A",
		DependsOnID: "B",
		Type:        model.DepBlocks,
	}
	issueA := model.Issue{
		ID:           "A",
		Title:        "Issue A",
		Status:       model.StatusOpen,
		IssueType:    model.TypeTask,
		Priority:     2,
		CreatedAt:    now,
		UpdatedAt:    now,
		Dependencies: []*model.Dependency{dep},
	}
	issueB := model.Issue{
		ID:        "B",
		Title:     "Issue B",
		Status:    model.StatusBlocked,
		IssueType: model.TypeTask,
		Priority:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	loader := NewAnalyzerMetricsLoader([]model.Issue{issueA, issueB})
	metrics, err := loader.LoadMetrics()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics entries, got %d", len(metrics))
	}

	metricA := metrics["A"]
	metricB := metrics["B"]

	if metricA.BlockerCount != 0 {
		t.Fatalf("expected A blocker count 0, got %d", metricA.BlockerCount)
	}
	if metricB.BlockerCount != 1 {
		t.Fatalf("expected B blocker count 1, got %d", metricB.BlockerCount)
	}
	if metricA.Status != string(model.StatusOpen) {
		t.Fatalf("expected A status open, got %q", metricA.Status)
	}
	if metricA.Priority != 2 {
		t.Fatalf("expected A priority 2, got %d", metricA.Priority)
	}
	if !metricA.UpdatedAt.Equal(now) {
		t.Fatalf("expected A UpdatedAt %v, got %v", now, metricA.UpdatedAt)
	}

	hash, err := loader.ComputeDataHash()
	if err != nil {
		t.Fatalf("unexpected hash error: %v", err)
	}
	expectedHash := analysis.ComputeDataHash([]model.Issue{issueA, issueB})
	if hash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, hash)
	}
}

// atomicMetricsLoader implements MetricsLoaderAtomic for testing atomic hash-and-load.
type atomicMetricsLoader struct {
	// metrics and hash for atomic loads
	atomicMetrics map[string]IssueMetrics
	atomicHash    string

	// Separate values for non-atomic path to simulate TOCTOU race
	loadMetrics map[string]IssueMetrics
	hashValue   string

	atomicCalls    int
	nonAtomicCalls int
}

func (a *atomicMetricsLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	a.nonAtomicCalls++
	return a.loadMetrics, nil
}

func (a *atomicMetricsLoader) ComputeDataHash() (string, error) {
	return a.hashValue, nil
}

func (a *atomicMetricsLoader) LoadMetricsWithHash() (map[string]IssueMetrics, string, error) {
	a.atomicCalls++
	return a.atomicMetrics, a.atomicHash, nil
}

func TestMetricsCacheRefresh_AtomicHash(t *testing.T) {
	// Scenario: loader returns different data atomically vs non-atomically
	// to simulate TOCTOU race condition. The atomic path should be used.
	loader := &atomicMetricsLoader{
		// Atomic path returns consistent data
		atomicMetrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.8, BlockerCount: 3},
		},
		atomicHash: "atomic-hash-001",

		// Non-atomic path would return different (stale) data - simulating race
		loadMetrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.5, BlockerCount: 1},
		},
		hashValue: "non-atomic-hash-002", // different from atomicHash
	}

	cache := NewMetricsCache(loader)

	// Refresh should use the atomic path
	err := cache.Refresh()
	if err != nil {
		t.Fatalf("unexpected refresh error: %v", err)
	}

	// Verify atomic path was used
	if loader.atomicCalls != 1 {
		t.Fatalf("expected 1 atomic call, got %d", loader.atomicCalls)
	}
	if loader.nonAtomicCalls != 0 {
		t.Fatalf("expected 0 non-atomic calls, got %d", loader.nonAtomicCalls)
	}

	// Verify the hash matches the atomic hash (not the stale one)
	if cache.DataHash() != "atomic-hash-001" {
		t.Fatalf("expected atomic hash %q, got %q", "atomic-hash-001", cache.DataHash())
	}

	// Verify the metrics match the atomic metrics
	metric, ok := cache.Get("A")
	if !ok {
		t.Fatal("expected metric A to be found")
	}
	if metric.PageRank != 0.8 {
		t.Fatalf("expected PageRank 0.8, got %f", metric.PageRank)
	}
	if metric.BlockerCount != 3 {
		t.Fatalf("expected BlockerCount 3, got %d", metric.BlockerCount)
	}
}

func TestMetricsCacheRefresh_FallbackToNonAtomic(t *testing.T) {
	// Verify that loaders NOT implementing MetricsLoaderAtomic
	// still work via the fallback path
	loader := &stubMetricsLoader{
		metrics: map[string]IssueMetrics{
			"B": {IssueID: "B", PageRank: 0.4, BlockerCount: 2},
		},
		hash: "stub-hash-123",
	}

	cache := NewMetricsCache(loader)

	err := cache.Refresh()
	if err != nil {
		t.Fatalf("unexpected refresh error: %v", err)
	}

	// Verify fallback path was used
	if loader.loadCalls != 1 {
		t.Fatalf("expected 1 load call, got %d", loader.loadCalls)
	}

	// Verify the hash and metrics
	if cache.DataHash() != "stub-hash-123" {
		t.Fatalf("expected hash %q, got %q", "stub-hash-123", cache.DataHash())
	}

	metric, ok := cache.Get("B")
	if !ok {
		t.Fatal("expected metric B to be found")
	}
	if metric.PageRank != 0.4 {
		t.Fatalf("expected PageRank 0.4, got %f", metric.PageRank)
	}
}

func TestAnalyzerMetricsLoader_LoadMetricsWithHash(t *testing.T) {
	now := time.Date(2025, 12, 18, 12, 0, 0, 0, time.UTC)
	issueA := model.Issue{
		ID:        "A",
		Title:     "Issue A",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}
	issueB := model.Issue{
		ID:        "B",
		Title:     "Issue B",
		Status:    model.StatusBlocked,
		IssueType: model.TypeTask,
		Priority:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	loader := NewAnalyzerMetricsLoader([]model.Issue{issueA, issueB})
	metrics, hash, err := loader.LoadMetricsWithHash()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify metrics were loaded
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(metrics))
	}
	if metrics["A"].Status != string(model.StatusOpen) {
		t.Fatalf("expected A status open, got %q", metrics["A"].Status)
	}

	// Verify hash matches what ComputeDataHash would return for same data
	expectedHash := analysis.ComputeDataHash([]model.Issue{issueA, issueB})
	if hash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, hash)
	}

	// Verify atomic consistency: hash from LoadMetricsWithHash equals ComputeDataHash
	separateHash, _ := loader.ComputeDataHash()
	if hash != separateHash {
		t.Fatalf("atomic hash %q != separate hash %q", hash, separateHash)
	}
}

// singleflightMetricsLoader tracks load calls with an atomic counter for concurrent testing.
type singleflightMetricsLoader struct {
	loadCalls int64 // atomic counter
	metrics   map[string]IssueMetrics
	hash      string
}

func (s *singleflightMetricsLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	atomic.AddInt64(&s.loadCalls, 1)
	// Simulate some work to increase chance of concurrent calls overlapping
	time.Sleep(10 * time.Millisecond)
	return s.metrics, nil
}

func (s *singleflightMetricsLoader) ComputeDataHash() (string, error) {
	return s.hash, nil
}

type driftingHashAtomicLoader struct {
	hashes       []string
	hashCalls    atomic.Int32
	loadCalls    atomic.Int32
	loadStarted  chan struct{}
	releaseLoad  chan struct{}
	loadStarted1 sync.Once
	metrics      map[string]IssueMetrics
	finalHash    string
}

func (d *driftingHashAtomicLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	return nil, errors.New("unexpected non-atomic load")
}

func (d *driftingHashAtomicLoader) ComputeDataHash() (string, error) {
	idx := int(d.hashCalls.Add(1) - 1)
	if idx >= len(d.hashes) {
		return d.hashes[len(d.hashes)-1], nil
	}
	return d.hashes[idx], nil
}

func (d *driftingHashAtomicLoader) LoadMetricsWithHash() (map[string]IssueMetrics, string, error) {
	d.loadCalls.Add(1)
	d.loadStarted1.Do(func() { close(d.loadStarted) })
	<-d.releaseLoad
	return d.metrics, d.finalHash, nil
}

type hashOnceAtomicLoader struct {
	hash      string
	hashCalls atomic.Int32
	loadCalls atomic.Int32
	metrics   map[string]IssueMetrics
}

func (h *hashOnceAtomicLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	return nil, errors.New("unexpected non-atomic load")
}

func (h *hashOnceAtomicLoader) ComputeDataHash() (string, error) {
	if h.hashCalls.Add(1) > 1 {
		return "", errors.New("ComputeDataHash called more than once")
	}
	return h.hash, nil
}

func (h *hashOnceAtomicLoader) LoadMetricsWithHash() (map[string]IssueMetrics, string, error) {
	h.loadCalls.Add(1)
	return h.metrics, h.hash, nil
}

func TestMetricsCacheEnsureFresh_Singleflight(t *testing.T) {
	loader := &singleflightMetricsLoader{
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.7, BlockerCount: 2},
		},
		hash: "singleflight-test-hash",
	}

	cache := NewMetricsCache(loader)

	// Launch 10 goroutines that all try to Get concurrently
	// This should trigger ensureFresh which should use singleflight
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Barrier to ensure all goroutines start at approximately the same time
	start := make(chan struct{})
	testStart := time.Now()

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			<-start // Wait for signal
			goroutineStart := time.Now()
			_, _ = cache.Get("A")
			t.Logf("goroutine %d returned in %v", id, time.Since(goroutineStart))
		}(i)
	}

	// Release all goroutines at once
	close(start)
	wg.Wait()
	t.Logf("all goroutines completed in %v", time.Since(testStart))

	// With singleflight, LoadMetrics should be called exactly once
	// (or at most a small number if timing is unlucky, but typically 1)
	calls := atomic.LoadInt64(&loader.loadCalls)
	if calls > 2 {
		t.Fatalf("expected LoadMetrics to be called at most 2 times (singleflight), got %d", calls)
	}
	if calls == 0 {
		t.Fatal("expected LoadMetrics to be called at least once")
	}

	// Verify the cache was populated
	if cache.DataHash() != "singleflight-test-hash" {
		t.Fatalf("expected hash %q, got %q", "singleflight-test-hash", cache.DataHash())
	}

	// Verify subsequent calls don't trigger loads (cache is fresh)
	initialCalls := calls
	for i := 0; i < 5; i++ {
		_, _ = cache.Get("A")
	}
	finalCalls := atomic.LoadInt64(&loader.loadCalls)
	if finalCalls != initialCalls {
		t.Fatalf("expected no additional loads for fresh cache, got %d more", finalCalls-initialCalls)
	}
}

func TestMetricsCacheEnsureFresh_SingleflightAcrossHashDrift(t *testing.T) {
	loader := &driftingHashAtomicLoader{
		hashes:      []string{"hash-a", "hash-b", "hash-b"},
		loadStarted: make(chan struct{}),
		releaseLoad: make(chan struct{}),
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.9, BlockerCount: 4},
		},
		finalHash: "hash-b",
	}

	cache := NewMetricsCache(loader)

	const workers = 2
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			_, _ = cache.Get("A")
		}()
	}

	ready.Wait()
	close(start)

	select {
	case <-loader.loadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for singleflight refresh to start")
	}

	close(loader.releaseLoad)
	wg.Wait()

	if got := loader.loadCalls.Load(); got != 1 {
		t.Fatalf("expected exactly one coalesced refresh despite hash drift, got %d", got)
	}
	if got := cache.DataHash(); got != loader.finalHash {
		t.Fatalf("expected final cache hash %q, got %q", loader.finalHash, got)
	}
}

func TestMetricsCacheEnsureFresh_UsesSingleObservedHash(t *testing.T) {
	loader := &hashOnceAtomicLoader{
		hash: "hash-once",
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", PageRank: 0.6, BlockerCount: 3},
		},
	}

	cache := NewMetricsCache(loader)

	metric, ok := cache.Get("A")
	if !ok {
		t.Fatal("expected metric to be found")
	}
	if metric.PageRank != 0.6 {
		t.Fatalf("expected PageRank 0.6, got %f", metric.PageRank)
	}
	if got := loader.hashCalls.Load(); got != 1 {
		t.Fatalf("expected ComputeDataHash to be called exactly once, got %d", got)
	}
	if got := loader.loadCalls.Load(); got != 1 {
		t.Fatalf("expected a single refresh load, got %d", got)
	}
	if got := cache.DataHash(); got != loader.hash {
		t.Fatalf("expected cache hash %q, got %q", loader.hash, got)
	}
}

func TestAnalyzerMetricsLoader_LoadMetricsWithHash_EmptyConsistent(t *testing.T) {
	loader := NewAnalyzerMetricsLoader(nil)

	metrics, hash, err := loader.LoadMetricsWithHash()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 0 {
		t.Fatalf("expected empty metrics, got %d entries", len(metrics))
	}

	separateHash, err := loader.ComputeDataHash()
	if err != nil {
		t.Fatalf("unexpected hash error: %v", err)
	}
	if hash != separateHash {
		t.Fatalf("expected atomic empty hash %q to match ComputeDataHash %q", hash, separateHash)
	}
}

func TestNewAnalyzerMetricsLoader_ClonesIssues(t *testing.T) {
	now := time.Date(2025, 12, 18, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:        "A",
			Title:     "Original",
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Priority:  2,
			CreatedAt: now,
			UpdatedAt: now,
			Labels:    []string{"alpha"},
		},
	}

	expectedHash := analysis.ComputeDataHash(cloneMetricsLoaderIssues(issues))
	loader := NewAnalyzerMetricsLoader(issues)

	issues[0].Title = "Mutated"
	issues[0].Status = model.StatusClosed
	issues[0].Labels[0] = "beta"

	hash, err := loader.ComputeDataHash()
	if err != nil {
		t.Fatalf("unexpected hash error: %v", err)
	}
	if hash != expectedHash {
		t.Fatalf("expected loader hash %q to remain bound to the original issue snapshot, got %q", expectedHash, hash)
	}

	metrics, err := loader.LoadMetrics()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if got := metrics["A"].Status; got != string(model.StatusOpen) {
		t.Fatalf("expected cloned loader status %q, got %q", model.StatusOpen, got)
	}
}
