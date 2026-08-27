package correlation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheKey_String(t *testing.T) {
	key := CacheKey{
		HeadSHA:      "abc123",
		HistoryState: coCommitHistoryStateFull,
		BeadsHash:    "def456",
		Options:      "opt789",
	}

	expected := "abc123:" + coCommitHistoryStateFull + ":def456:opt789"
	if got := key.String(); got != expected {
		t.Errorf("CacheKey.String() = %q, want %q", got, expected)
	}
}

func TestNewHistoryCache(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")

	if cache.maxAge != DefaultCacheMaxAge {
		t.Errorf("maxAge = %v, want %v", cache.maxAge, DefaultCacheMaxAge)
	}
	if cache.maxSize != DefaultCacheMaxSize {
		t.Errorf("maxSize = %d, want %d", cache.maxSize, DefaultCacheMaxSize)
	}
	if cache.Size() != 0 {
		t.Errorf("Size() = %d, want 0", cache.Size())
	}
}

func TestNewHistoryCacheWithOptions(t *testing.T) {
	maxAge := 10 * time.Minute
	maxSize := 5

	cache := NewHistoryCacheWithOptions("/tmp/test", maxAge, maxSize)

	if cache.maxAge != maxAge {
		t.Errorf("maxAge = %v, want %v", cache.maxAge, maxAge)
	}
	if cache.maxSize != maxSize {
		t.Errorf("maxSize = %d, want %d", cache.maxSize, maxSize)
	}
}

func TestNewHistoryCacheWithOptions_DefaultsOnInvalid(t *testing.T) {
	cache := NewHistoryCacheWithOptions("/tmp/test", 0, 0)

	if cache.maxAge != DefaultCacheMaxAge {
		t.Errorf("maxAge with 0 = %v, want %v", cache.maxAge, DefaultCacheMaxAge)
	}
	if cache.maxSize != DefaultCacheMaxSize {
		t.Errorf("maxSize with 0 = %d, want %d", cache.maxSize, DefaultCacheMaxSize)
	}
}

func TestHistoryCache_PutAndGet(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")

	key := CacheKey{HeadSHA: "abc", BeadsHash: "def", Options: "ghi"}
	report := &HistoryReport{
		Stats: HistoryStats{TotalBeads: 5},
	}

	// Get on empty cache should miss
	if _, ok := cache.Get(key); ok {
		t.Error("Get on empty cache should return false")
	}

	// Put and Get
	cache.Put(key, report)

	got, ok := cache.Get(key)
	if !ok {
		t.Fatal("Get after Put should return true")
	}
	if got.Stats.TotalBeads != 5 {
		t.Errorf("Got report with TotalBeads = %d, want 5", got.Stats.TotalBeads)
	}

	if cache.Size() != 1 {
		t.Errorf("Size() = %d, want 1", cache.Size())
	}
}

func TestHistoryCache_PutUpdate(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")

	key := CacheKey{HeadSHA: "abc", BeadsHash: "def", Options: "ghi"}
	report1 := &HistoryReport{Stats: HistoryStats{TotalBeads: 5}}
	report2 := &HistoryReport{Stats: HistoryStats{TotalBeads: 10}}

	cache.Put(key, report1)
	cache.Put(key, report2) // Update same key

	got, ok := cache.Get(key)
	if !ok {
		t.Fatal("Get after update should return true")
	}
	if got.Stats.TotalBeads != 10 {
		t.Errorf("Got TotalBeads = %d, want 10 (updated value)", got.Stats.TotalBeads)
	}

	// Size should still be 1 (not 2)
	if cache.Size() != 1 {
		t.Errorf("Size() after update = %d, want 1", cache.Size())
	}
}

func TestHistoryCache_LRUEviction(t *testing.T) {
	cache := NewHistoryCacheWithOptions("/tmp/test", 5*time.Minute, 3)

	// Add 3 entries
	for i := 0; i < 3; i++ {
		key := CacheKey{HeadSHA: string(rune('a' + i)), BeadsHash: "b", Options: "c"}
		cache.Put(key, &HistoryReport{Stats: HistoryStats{TotalBeads: i}})
	}

	if cache.Size() != 3 {
		t.Errorf("Size() = %d, want 3", cache.Size())
	}

	// Add 4th entry - should evict oldest (key 'a')
	key4 := CacheKey{HeadSHA: "d", BeadsHash: "b", Options: "c"}
	cache.Put(key4, &HistoryReport{Stats: HistoryStats{TotalBeads: 99}})

	if cache.Size() != 3 {
		t.Errorf("Size() after eviction = %d, want 3", cache.Size())
	}

	// First key should be evicted
	keyFirst := CacheKey{HeadSHA: "a", BeadsHash: "b", Options: "c"}
	if _, ok := cache.Get(keyFirst); ok {
		t.Error("First entry should have been evicted")
	}

	// Fourth key should exist
	if _, ok := cache.Get(key4); !ok {
		t.Error("Fourth entry should exist")
	}
}

func TestHistoryCache_LRUAccessOrder(t *testing.T) {
	cache := NewHistoryCacheWithOptions("/tmp/test", 5*time.Minute, 3)

	// Add 3 entries
	key1 := CacheKey{HeadSHA: "a", BeadsHash: "b", Options: "c"}
	key2 := CacheKey{HeadSHA: "b", BeadsHash: "b", Options: "c"}
	key3 := CacheKey{HeadSHA: "c", BeadsHash: "b", Options: "c"}

	cache.Put(key1, &HistoryReport{})
	cache.Put(key2, &HistoryReport{})
	cache.Put(key3, &HistoryReport{})

	// Access key1 to move it to end
	cache.Get(key1)

	// Add 4th entry - should evict key2 (oldest accessed)
	key4 := CacheKey{HeadSHA: "d", BeadsHash: "b", Options: "c"}
	cache.Put(key4, &HistoryReport{})

	// key2 should be evicted (was oldest accessed)
	if _, ok := cache.Get(key2); ok {
		t.Error("key2 should have been evicted (was oldest accessed)")
	}

	// key1 should still exist (was recently accessed)
	if _, ok := cache.Get(key1); !ok {
		t.Error("key1 should still exist (was recently accessed)")
	}
}

func TestHistoryCache_Expiration(t *testing.T) {
	// Use very short maxAge for testing
	cache := NewHistoryCacheWithOptions("/tmp/test", 10*time.Millisecond, 10)

	key := CacheKey{HeadSHA: "abc", BeadsHash: "def", Options: "ghi"}
	cache.Put(key, &HistoryReport{})

	// Should hit immediately
	if _, ok := cache.Get(key); !ok {
		t.Error("Get immediately after Put should hit")
	}

	// Wait for expiration
	time.Sleep(20 * time.Millisecond)

	// Should miss after expiration
	if _, ok := cache.Get(key); ok {
		t.Error("Get after expiration should miss")
	}

	// Entry should be removed
	if cache.Size() != 0 {
		t.Errorf("Size() after expiration get = %d, want 0", cache.Size())
	}
}

func TestHistoryCacheRejectsFutureTimestamp(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")
	key := CacheKey{HeadSHA: "future", BeadsHash: "beads", Options: "opts"}
	cache.Put(key, &HistoryReport{})

	cache.mu.Lock()
	cache.entries[key.String()].CreatedAt = time.Now().Add(time.Hour)
	cache.mu.Unlock()
	if _, ok := cache.Get(key); ok {
		t.Fatal("Get accepted future-dated cache entry")
	}
	if cache.Size() != 0 {
		t.Fatal("Get did not remove future-dated cache entry")
	}

	cache.Put(key, &HistoryReport{})
	cache.mu.Lock()
	cache.entries[key.String()].CreatedAt = time.Now().Add(time.Hour)
	cache.mu.Unlock()
	if _, _, _, ok := cache.GetWithMeta(key); ok {
		t.Fatal("GetWithMeta accepted future-dated cache entry")
	}
}

func TestHistoryCache_Invalidate(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")

	// Add some entries
	for i := 0; i < 5; i++ {
		key := CacheKey{HeadSHA: string(rune('a' + i)), BeadsHash: "b", Options: "c"}
		cache.Put(key, &HistoryReport{})
	}

	if cache.Size() != 5 {
		t.Errorf("Size() before invalidate = %d, want 5", cache.Size())
	}

	cache.Invalidate()

	if cache.Size() != 0 {
		t.Errorf("Size() after invalidate = %d, want 0", cache.Size())
	}
}

func TestHistoryCache_InvalidateForHead(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")

	// Add entries with different HEAD SHAs
	key1 := CacheKey{HeadSHA: "head1", BeadsHash: "b", Options: "c"}
	key2 := CacheKey{HeadSHA: "head1", BeadsHash: "d", Options: "c"}
	key3 := CacheKey{HeadSHA: "head2", BeadsHash: "b", Options: "c"}

	cache.Put(key1, &HistoryReport{})
	cache.Put(key2, &HistoryReport{})
	cache.Put(key3, &HistoryReport{})

	// Invalidate for head2
	cache.InvalidateForHead("head1")

	// head2 entry should be removed
	if _, ok := cache.Get(key3); ok {
		t.Error("head2 entry should have been invalidated")
	}

	// head1 entries should remain
	if _, ok := cache.Get(key1); !ok {
		t.Error("head1 entry 1 should remain")
	}
	if _, ok := cache.Get(key2); !ok {
		t.Error("head1 entry 2 should remain")
	}
}

func TestHistoryCache_Stats(t *testing.T) {
	cache := NewHistoryCacheWithOptions("/tmp/test", 5*time.Minute, 10)

	// Initially empty
	stats := cache.Stats()
	if stats.Size != 0 {
		t.Errorf("Stats.Size = %d, want 0", stats.Size)
	}
	if stats.MaxSize != 10 {
		t.Errorf("Stats.MaxSize = %d, want 10", stats.MaxSize)
	}
	if stats.OldestEntry != nil {
		t.Error("Stats.OldestEntry should be nil when empty")
	}

	// Add entries
	cache.Put(CacheKey{HeadSHA: "a"}, &HistoryReport{})
	time.Sleep(time.Millisecond)
	cache.Put(CacheKey{HeadSHA: "b"}, &HistoryReport{})

	stats = cache.Stats()
	if stats.Size != 2 {
		t.Errorf("Stats.Size = %d, want 2", stats.Size)
	}
	if stats.OldestEntry == nil {
		t.Error("Stats.OldestEntry should not be nil")
	}
	if stats.NewestEntry == nil {
		t.Error("Stats.NewestEntry should not be nil")
	}
}

func TestHashBeads(t *testing.T) {
	beads1 := []BeadInfo{
		{ID: "bv-1", Title: "First", Status: "open"},
		{ID: "bv-2", Title: "Second", Status: "closed"},
	}
	beads2 := []BeadInfo{
		{ID: "bv-1", Title: "First", Status: "open"},
		{ID: "bv-2", Title: "Second", Status: "closed"},
	}
	beads3 := []BeadInfo{
		{ID: "bv-1", Title: "First", Status: "closed"}, // Different status
		{ID: "bv-2", Title: "Second", Status: "closed"},
	}
	beads4 := []BeadInfo{
		{ID: "bv-1", Title: "First renamed", Status: "open"}, // Different title
		{ID: "bv-2", Title: "Second", Status: "closed"},
	}
	beadsReordered := []BeadInfo{
		{ID: "bv-2", Title: "Second", Status: "closed"},
		{ID: "bv-1", Title: "First", Status: "open"},
	}

	hash1 := hashBeads(beads1)
	hash2 := hashBeads(beads2)
	hash3 := hashBeads(beads3)
	hash4 := hashBeads(beads4)
	hashReordered := hashBeads(beadsReordered)

	// Same input should produce same hash
	if hash1 != hash2 {
		t.Errorf("Same beads should produce same hash: %s != %s", hash1, hash2)
	}
	if hash1 != hashReordered {
		t.Errorf("Equivalent beads in different order should produce same hash: %s != %s", hash1, hashReordered)
	}

	// Different input should produce different hash
	if hash1 == hash3 {
		t.Error("Different bead statuses should produce different hash")
	}
	if hash1 == hash4 {
		t.Error("Different bead titles should produce different hash")
	}

	// Cache identity retains the full SHA-256 digest rather than truncating a
	// correctness key for display convenience.
	if len(hash1) != sha256.Size*2 {
		t.Errorf("Hash length = %d, want %d", len(hash1), sha256.Size*2)
	}
}

func TestHashBeadsFramesRecordsWithoutAmbiguity(t *testing.T) {
	// The old NUL-delimited concatenation encoded both collections as the same
	// byte stream: "a\x00t\x00xyz\x00u\x00v". JSON framing keeps field and record
	// boundaries explicit even when adjacent values can be re-segmented.
	left := []BeadInfo{
		{ID: "a", Title: "t", Status: "x"},
		{ID: "yz", Title: "u", Status: "v"},
	}
	right := []BeadInfo{
		{ID: "a", Title: "t", Status: "xy"},
		{ID: "z", Title: "u", Status: "v"},
	}
	if gotLeft, gotRight := hashBeads(left), hashBeads(right); gotLeft == gotRight {
		t.Fatalf("distinct framed bead sets shared hash %q", gotLeft)
	}
}

func TestHashBeadsMatchesDuplicateIDLastWriteWinsSemantics(t *testing.T) {
	a := BeadInfo{ID: "bv-duplicate", Title: "first", Status: "open"}
	b := BeadInfo{ID: "bv-duplicate", Title: "second", Status: "closed"}

	firstThenSecond := hashBeads([]BeadInfo{a, b})
	secondThenFirst := hashBeads([]BeadInfo{b, a})
	if firstThenSecond == secondThenFirst {
		t.Fatal("opposite duplicate-ID orders hashed alike despite assembling different last-write-wins reports")
	}
	if got, want := firstThenSecond, hashBeads([]BeadInfo{b}); got != want {
		t.Fatalf("duplicate-ID hash=%q, want effective last entry hash %q", got, want)
	}
	if got, want := secondThenFirst, hashBeads([]BeadInfo{a}); got != want {
		t.Fatalf("reversed duplicate-ID hash=%q, want effective last entry hash %q", got, want)
	}
}

func TestHashOptions(t *testing.T) {
	now := time.Now()
	opts1 := CorrelatorOptions{BeadID: "bv-1", Limit: 100}
	opts2 := CorrelatorOptions{BeadID: "bv-1", Limit: 100}
	opts3 := CorrelatorOptions{BeadID: "bv-2", Limit: 100}
	opts4 := CorrelatorOptions{BeadID: "bv-1", Since: &now}

	hash1 := hashOptions(opts1)
	hash2 := hashOptions(opts2)
	hash3 := hashOptions(opts3)
	hash4 := hashOptions(opts4)

	// Same options should produce same hash
	if hash1 != hash2 {
		t.Errorf("Same options should produce same hash: %s != %s", hash1, hash2)
	}

	// Different options should produce different hash
	if hash1 == hash3 {
		t.Error("Different BeadID should produce different hash")
	}
	if hash1 == hash4 {
		t.Error("Different Since should produce different hash")
	}
}

func TestCachedCorrelator_CacheHitAndMiss(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = neverRefreshForTest
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	// First call should miss
	_, err := correlator.GenerateReport(beads, opts)
	if err != nil {
		t.Fatalf("First GenerateReport failed: %v", err)
	}

	stats := correlator.CacheStats()
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, want 1", stats.Misses)
	}
	if stats.Hits != 0 {
		t.Errorf("Hits = %d, want 0", stats.Hits)
	}

	// Second call with same params should hit
	_, err = correlator.GenerateReport(beads, opts)
	if err != nil {
		t.Fatalf("Second GenerateReport failed: %v", err)
	}

	stats = correlator.CacheStats()
	if stats.Misses != 1 {
		t.Errorf("Misses after hit = %d, want 1", stats.Misses)
	}
	if stats.Hits != 1 {
		t.Errorf("Hits after hit = %d, want 1", stats.Hits)
	}
	if stats.HitRate != 0.5 {
		t.Errorf("HitRate = %f, want 0.5", stats.HitRate)
	}
}

func TestCachedCorrelatorBypassesShallowCacheAcrossSameHeadDeepen(t *testing.T) {
	source := initTempGitRepo(t)
	advanceGitHead(t, source, "second")
	shallow := cloneShallowRepoForCacheTest(t, source)
	headBefore, err := getGitHead(shallow)
	if err != nil {
		t.Fatalf("resolve shallow HEAD: %v", err)
	}

	correlator := NewCachedCorrelator(shallow)
	correlator.shouldRefreshFn = neverRefreshForTest
	var calls atomic.Int32
	correlator.generateReportFn = func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error) {
		calls.Add(1)
		return &HistoryReport{GitRange: coCommitRepositoryHistoryState(nil, shallow)}, nil
	}

	first, err := correlator.GenerateReport(nil, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("first shallow report: %v", err)
	}
	second, err := correlator.GenerateReport(nil, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("second shallow report: %v", err)
	}
	if first.GitRange != coCommitHistoryStateShallow || second.GitRange != coCommitHistoryStateShallow || calls.Load() != 2 {
		t.Fatalf("shallow calls=%d first=%q second=%q, want two uncached shallow generations", calls.Load(), first.GitRange, second.GitRange)
	}
	if correlator.CacheStats().CacheSize != 0 {
		t.Fatal("CachedCorrelator retained a shallow report")
	}

	runGit(t, shallow, "fetch", "--unshallow", "origin")
	if headAfter, headErr := getGitHead(shallow); headErr != nil || headAfter != headBefore {
		t.Fatalf("deepen changed HEAD: before=%q after=%q error=%v", headBefore, headAfter, headErr)
	}
	third, err := correlator.GenerateReport(nil, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("first full-history report: %v", err)
	}
	fourth, err := correlator.GenerateReport(nil, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("cached full-history report: %v", err)
	}
	if third.GitRange != coCommitHistoryStateFull || fourth != third || calls.Load() != 3 {
		t.Fatalf("post-deepen calls=%d third=%q shared=%t, want one full generation then hit", calls.Load(), third.GitRange, fourth == third)
	}
	if correlator.CacheStats().CacheSize != 1 {
		t.Fatal("CachedCorrelator did not retain the full-history report")
	}
}

func TestCachedCorrelator_DifferentOptionsMiss(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = neverRefreshForTest
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}

	// First call
	_, err := correlator.GenerateReport(beads, CorrelatorOptions{Limit: 10})
	if err != nil {
		t.Fatalf("First GenerateReport failed: %v", err)
	}

	// Second call with different options should miss
	_, err = correlator.GenerateReport(beads, CorrelatorOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Second GenerateReport failed: %v", err)
	}

	stats := correlator.CacheStats()
	if stats.Misses != 2 {
		t.Errorf("Misses = %d, want 2 (different options should miss)", stats.Misses)
	}
}

func TestCachedCorrelator_InvalidateCache(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = neverRefreshForTest
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	// Populate cache
	_, _ = correlator.GenerateReport(beads, opts)

	stats := correlator.CacheStats()
	if stats.CacheSize != 1 {
		t.Errorf("CacheSize = %d, want 1", stats.CacheSize)
	}

	// Invalidate
	correlator.InvalidateCache()

	stats = correlator.CacheStats()
	if stats.CacheSize != 0 {
		t.Errorf("CacheSize after invalidate = %d, want 0", stats.CacheSize)
	}
}

func TestNewCachedCorrelatorWithOptions(t *testing.T) {
	correlator := NewCachedCorrelatorWithOptions("/tmp/test", 10*time.Minute, 20)

	if correlator.cache.maxAge != 10*time.Minute {
		t.Errorf("maxAge = %v, want 10m", correlator.cache.maxAge)
	}
	if correlator.cache.maxSize != 20 {
		t.Errorf("maxSize = %d, want 20", correlator.cache.maxSize)
	}
}

func TestNewCachedCorrelatorForwardsExplicitBeadsPath(t *testing.T) {
	repoPath := "/tmp/test"
	explicitPath := filepath.Join(repoPath, ".beads", "custom.jsonl")
	expectedPath := filepath.Join(".beads", "custom.jsonl")

	correlator := NewCachedCorrelator(repoPath, explicitPath)

	if got := correlator.correlator.extractor.primaryBeadsFile(); got != expectedPath {
		t.Errorf("primaryBeadsFile = %q, want %q", got, expectedPath)
	}
}

func TestNewCachedCorrelatorWithOptionsForwardsExplicitBeadsPath(t *testing.T) {
	repoPath := "/tmp/test"
	explicitPath := filepath.Join(repoPath, ".beads", "custom.jsonl")
	expectedPath := filepath.Join(".beads", "custom.jsonl")

	correlator := NewCachedCorrelatorWithOptions(repoPath, 10*time.Minute, 20, explicitPath)

	if got := correlator.correlator.extractor.primaryBeadsFile(); got != expectedPath {
		t.Errorf("primaryBeadsFile = %q, want %q", got, expectedPath)
	}
}

func TestCachedCorrelator_Singleflight(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = neverRefreshForTest
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	var calls atomic.Int32
	var started atomic.Int32
	generateStarted := make(chan struct{})
	releaseGenerate := make(chan struct{})

	correlator.generateReportFn = func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error) {
		calls.Add(1)
		select {
		case <-generateStarted:
		default:
			close(generateStarted)
		}
		<-releaseGenerate
		return &HistoryReport{
			GeneratedAt: time.Now().UTC(),
			Histories:   map[string]BeadHistory{"test-1": {BeadID: "test-1"}},
		}, nil
	}

	const workers = 16
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)

	var wg sync.WaitGroup
	wg.Add(workers)

	errCh := make(chan error, workers)
	reports := make(chan *HistoryReport, workers)

	testStart := time.Now()
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			ready.Done()
			<-start
			started.Add(1)
			goroutineStart := time.Now()

			report, err := correlator.GenerateReport(beads, opts)
			t.Logf("goroutine %d returned in %v", id, time.Since(goroutineStart))
			if err != nil {
				errCh <- err
				return
			}
			reports <- report
		}(i)
	}

	ready.Wait()
	close(start)

	select {
	case <-generateStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for report generation to start")
	}

	deadline := time.Now().Add(2 * time.Second)
	for started.Load() != workers {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for workers to start, got %d of %d", started.Load(), workers)
		}
		time.Sleep(time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)
	close(releaseGenerate)
	wg.Wait()
	t.Logf("all goroutines completed in %v", time.Since(testStart))
	close(errCh)
	close(reports)

	for err := range errCh {
		if err != nil {
			t.Fatalf("GenerateReport returned error: %v", err)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying GenerateReport calls = %d, want 1", got)
	}

	var first *HistoryReport
	count := 0
	for report := range reports {
		if report == nil {
			t.Fatal("GenerateReport returned nil report")
		}
		if first == nil {
			first = report
		} else if report != first {
			t.Fatal("expected all callers to receive the shared report instance")
		}
		count++
	}

	if count != workers {
		t.Fatalf("reports returned = %d, want %d", count, workers)
	}

	stats := correlator.CacheStats()
	if stats.Misses != 1 {
		t.Fatalf("Misses = %d, want 1", stats.Misses)
	}
	if stats.CacheSize != 1 {
		t.Fatalf("CacheSize = %d, want 1", stats.CacheSize)
	}
}

func TestCachedCorrelator_DoesNotCacheWhenHeadChangesDuringGenerate(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	started := make(chan struct{})
	releaseGenerate := make(chan struct{})
	var generateCalls atomic.Int32

	correlator.generateReportFn = func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error) {
		generateCalls.Add(1)
		close(started)
		<-releaseGenerate
		return &HistoryReport{
			GeneratedAt: time.Now().UTC(),
			Histories:   map[string]BeadHistory{"test-1": {BeadID: "test-1"}},
		}, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := correlator.GenerateReport(beads, opts)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for report generation to start")
	}

	advanceGitHead(t, repoPath, "head-shift")
	close(releaseGenerate)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GenerateReport returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for GenerateReport to finish")
	}

	if stats := correlator.CacheStats(); stats.CacheSize != 0 {
		t.Fatalf("expected cache to stay empty after HEAD changed mid-flight, got size %d", stats.CacheSize)
	}
	if got := generateCalls.Load(); got != 1 {
		t.Fatalf("generate calls = %d, want 1", got)
	}
}

func TestCachedCorrelator_XFetchUsesClonedInputs(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = func(time.Time, time.Duration, float64, time.Time) bool { return true }

	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	refreshDone := make(chan struct{})
	var callCount atomic.Int32
	var refreshStatus atomic.Value

	correlator.generateReportFn = func(in []BeadInfo, _ CorrelatorOptions) (*HistoryReport, error) {
		call := callCount.Add(1)
		if call == 2 {
			close(refreshStarted)
			<-releaseRefresh
			if len(in) > 0 {
				refreshStatus.Store(in[0].Status)
			}
			close(refreshDone)
		}
		return &HistoryReport{
			GeneratedAt: time.Now().UTC(),
			Histories:   map[string]BeadHistory{"test-1": {BeadID: "test-1"}},
		}, nil
	}

	// Prime the cache.
	if _, err := correlator.GenerateReport(beads, opts); err != nil {
		t.Fatalf("initial GenerateReport failed: %v", err)
	}
	key, err := BuildCacheKey(repoPath, beads, opts)
	if err != nil {
		t.Fatalf("BuildCacheKey failed: %v", err)
	}
	report, ok := correlator.cache.Get(key)
	if !ok {
		t.Fatal("expected primed cache entry")
	}
	correlator.cache.PutWithDuration(key, report, time.Second)

	// Trigger the xfetch refresh and then mutate caller-owned input after return.
	if _, err := correlator.GenerateReport(beads, opts); err != nil {
		t.Fatalf("cached GenerateReport failed: %v", err)
	}

	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh to start")
	}

	beads[0].Status = "closed"
	close(releaseRefresh)

	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh to finish")
	}

	got, _ := refreshStatus.Load().(string)
	if got != "open" {
		t.Fatalf("expected background refresh to observe cloned bead status %q, got %q", "open", got)
	}
}

func TestCachedCorrelator_SingleflightLogsSharedErrors(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	wantErr := errors.New("shared singleflight failure")
	var calls atomic.Int32
	var started atomic.Int32
	generateStarted := make(chan struct{})
	releaseGenerate := make(chan struct{})

	correlator.generateReportFn = func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error) {
		calls.Add(1)
		select {
		case <-generateStarted:
		default:
			close(generateStarted)
		}
		<-releaseGenerate
		return nil, wantErr
	}

	var logMu sync.Mutex
	logs := make([]string, 0, 2)
	originalLogf := correlationCacheLogf
	correlationCacheLogf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	defer func() {
		correlationCacheLogf = originalLogf
	}()

	const workers = 2
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			started.Add(1)
			_, err := correlator.GenerateReport(beads, opts)
			errCh <- err
		}()
	}

	ready.Wait()
	close(start)

	select {
	case <-generateStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for report generation to start")
	}

	deadline := time.Now().Add(2 * time.Second)
	for started.Load() != workers {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for workers to start, got %d of %d", started.Load(), workers)
		}
		time.Sleep(time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)
	close(releaseGenerate)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if !errors.Is(err, wantErr) {
			t.Fatalf("GenerateReport error = %v, want %v", err, wantErr)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying GenerateReport calls = %d, want 1", got)
	}

	logMu.Lock()
	joinedLogs := strings.Join(logs, "\n")
	logCount := len(logs)
	logMu.Unlock()
	if logCount == 0 {
		t.Fatal("expected singleflight error to be logged")
	}
	if !strings.Contains(joinedLogs, "shared=true") {
		t.Fatalf("expected shared singleflight error log, got %q", joinedLogs)
	}
	if !strings.Contains(joinedLogs, wantErr.Error()) {
		t.Fatalf("expected logged error %q, got %q", wantErr, joinedLogs)
	}
}

func TestCachedCorrelator_XFetchRefreshLogsErrors(t *testing.T) {
	repoPath := initTempGitRepo(t)
	correlator := NewCachedCorrelator(repoPath)
	correlator.shouldRefreshFn = func(time.Time, time.Duration, float64, time.Time) bool { return true }

	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}
	wantErr := errors.New("xfetch refresh failed")

	refreshAttempted := make(chan struct{})
	refreshDone := make(chan struct{})
	var callCount atomic.Int32
	correlator.generateReportFn = func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error) {
		call := callCount.Add(1)
		if call == 2 {
			close(refreshAttempted)
			defer close(refreshDone)
			return nil, wantErr
		}
		return &HistoryReport{
			GeneratedAt: time.Now().UTC(),
			Histories:   map[string]BeadHistory{"test-1": {BeadID: "test-1"}},
		}, nil
	}

	var logMu sync.Mutex
	logs := make([]string, 0, 1)
	originalLogf := correlationCacheLogf
	correlationCacheLogf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	defer func() {
		correlationCacheLogf = originalLogf
	}()

	if _, err := correlator.GenerateReport(beads, opts); err != nil {
		t.Fatalf("initial GenerateReport failed: %v", err)
	}
	key, err := BuildCacheKey(repoPath, beads, opts)
	if err != nil {
		t.Fatalf("BuildCacheKey failed: %v", err)
	}
	report, ok := correlator.cache.Get(key)
	if !ok {
		t.Fatal("expected primed cache entry")
	}
	correlator.cache.PutWithDuration(key, report, time.Second)

	if _, err := correlator.GenerateReport(beads, opts); err != nil {
		t.Fatalf("cached GenerateReport failed: %v", err)
	}

	select {
	case <-refreshAttempted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh to start")
	}

	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background refresh to finish")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		logMu.Lock()
		joinedLogs := strings.Join(logs, "\n")
		logMu.Unlock()
		if strings.Contains(joinedLogs, wantErr.Error()) {
			if !strings.Contains(joinedLogs, "background refresh") {
				t.Fatalf("expected background refresh log context, got %q", joinedLogs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected background refresh error log containing %q, got %q", wantErr, joinedLogs)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBuildCacheKey_Error(t *testing.T) {
	// Should fail without a git repo
	_, err := BuildCacheKey("/nonexistent/path", nil, CorrelatorOptions{})
	if err == nil {
		t.Error("BuildCacheKey should fail for invalid repo path")
	}
}

func initTempGitRepo(t *testing.T) string {
	t.Helper()

	repoPath := t.TempDir()
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")

	initialFile := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(initialFile, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGit(t, repoPath, "add", "README.md")
	runGit(t, repoPath, "commit", "-m", "initial commit")
	return repoPath
}

func advanceGitHead(t *testing.T, repoPath, name string) {
	t.Helper()

	filePath := filepath.Join(repoPath, name+".txt")
	if err := os.WriteFile(filePath, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
	runGit(t, repoPath, "add", filepath.Base(filePath))
	runGit(t, repoPath, "commit", "-m", name)
}

func runGit(t *testing.T, repoPath string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func cloneShallowRepoForCacheTest(t *testing.T, source string) string {
	t.Helper()
	shallow := filepath.Join(t.TempDir(), "shallow")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()
	cmd := exec.Command("git", "clone", "--depth=1", sourceURL, shallow)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create shallow clone: %v\n%s", err, out)
	}
	return shallow
}

func neverRefreshForTest(time.Time, time.Duration, float64, time.Time) bool {
	return false
}

func TestCacheKey_Empty(t *testing.T) {
	key := CacheKey{}
	if key.String() != ":::" {
		t.Errorf("Empty CacheKey.String() = %q, want ':::'", key.String())
	}
}

func TestHistoryCache_GetNonexistent(t *testing.T) {
	cache := NewHistoryCache("/tmp/test")
	key := CacheKey{HeadSHA: "nonexistent", BeadsHash: "hash", Options: "opts"}

	_, ok := cache.Get(key)
	if ok {
		t.Error("Get should return false for nonexistent key")
	}
}

func TestHistoryCache_RemoveEntryOrdering(t *testing.T) {
	cache := NewHistoryCacheWithOptions("/tmp/test", 5*time.Minute, 5)

	// Add multiple entries
	for i := 0; i < 3; i++ {
		key := CacheKey{HeadSHA: string(rune('a' + i))}
		cache.Put(key, &HistoryReport{})
	}

	// Verify order
	if len(cache.order) != 3 {
		t.Errorf("order length = %d, want 3", len(cache.order))
	}

	// Remove middle entry
	cache.Invalidate()

	if len(cache.order) != 0 {
		t.Errorf("order after invalidate = %d, want 0", len(cache.order))
	}
}
