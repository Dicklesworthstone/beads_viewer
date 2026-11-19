package analysis_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestComputeIssueFingerprintFrozenEncoding(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("source", 3*60*60))
	empty := ""
	zero := 0
	zeroTime := time.Time{}
	issue := model.Issue{
		ID: "日本語\x00id", Title: "title\x00suffix", Description: strings.Repeat("日本語 🚀 e\u0301\x00", 700),
		Design: "design", AcceptanceCriteria: "criteria", Notes: "notes", Assignee: "agent", SourceRepo: "source",
		ExternalRef: &empty, Status: model.StatusDeferred, IssueType: model.TypeTask, Priority: -3,
		EstimatedMinutes: &zero, CreatedAt: stamp, UpdatedAt: stamp.Add(time.Second),
		DueDate: &zeroTime, DeferUntil: &stamp, ClosedAt: &stamp,
		CompactionLevel: 2, CompactedAt: &stamp, CompactedAtCommit: &empty, OriginalSize: 90000,
		Labels:       []string{"z", "日本語", "a\x00b"},
		Comments:     []*model.Comment{nil, {ID: "comment", IssueID: "日本語\x00id", Author: "author", Text: "text\x00日本語", CreatedAt: stamp}},
		Dependencies: []*model.Dependency{nil, {IssueID: "日本語\x00id", DependsOnID: "other\x00id", Type: model.DepBlocks, CreatedAt: stamp, CreatedBy: "author"}},
	}
	cases := []struct {
		name  string
		issue model.Issue
		want  analysis.IssueFingerprint
	}{
		{"full", issue, analysis.IssueFingerprint{ID: issue.ID, ContentHash: "409f52831c37769a082b292147e99152f3e29f24f1e395ec5f240b628d2d261c", DependencyHash: "b89e88e96400f10e7296f0319cfba764cb78245c27bc64b7bba291c4b3c752d3"}},
		{"empty", model.Issue{ID: "empty"}, analysis.IssueFingerprint{ID: "empty", ContentHash: "015275e61fa0d0751c1d9f45541c7804c895404455470710ade3786f282f2da0", DependencyHash: "none"}},
		{"present-empty", model.Issue{ID: "empty", ExternalRef: &empty, EstimatedMinutes: &zero, DueDate: &zeroTime}, analysis.IssueFingerprint{ID: "empty", ContentHash: "a0cc0a4c136fec7227b35a22316e2378b008333354da97463122196231c33b8f", DependencyHash: "none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := analysis.ComputeIssueFingerprint(tc.issue); got != tc.want {
				t.Fatalf("canonical fingerprint changed: got %#v want %#v", got, tc.want)
			}
		})
	}

	// Freeze the aggregate encoding too, including transitions from a long
	// content/dependency digest to an empty issue and duplicate-ID ordering.
	for _, tc := range []struct {
		name   string
		issues []model.Issue
		want   string
	}{
		{"full", []model.Issue{cases[0].issue}, "353b785cb4435228d2432679d2ab999a79986b3faae1967919af4471fb8cbdc3"},
		{"empty", []model.Issue{cases[1].issue}, "3468cdfcfb0298da8c7f4a442f2312449c131fdb822f11d2c50bb952fbede552"},
		{"present-empty", []model.Issue{cases[2].issue}, "bc2a77f2da5867067daa54ae8ac7bfdcfa7e71fb48716464b14950a2f9418127"},
		{"mixed", []model.Issue{cases[0].issue, cases[1].issue, cases[2].issue}, "a03a3dd4735909b3cd7f8deaac5324d1bb1ffd442a73d3b2225db9f0dd4a99b5"},
		{"reversed-duplicates", []model.Issue{cases[2].issue, cases[0].issue, cases[1].issue}, "922ad9f2ae582c8638c34d7d64eb327c0867962894d07cd264a4642bf66c6954"},
	} {
		t.Run("aggregate/"+tc.name, func(t *testing.T) {
			t.Parallel()
			before, err := json.Marshal(tc.issues)
			if err != nil {
				t.Fatal(err)
			}
			if got := analysis.ComputeDataHash(tc.issues); got != tc.want {
				t.Fatalf("canonical aggregate changed: got %s want %s", got, tc.want)
			}
			after, err := json.Marshal(tc.issues)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("fingerprinting changed caller-owned issue data")
			}
		})
	}
}

func TestComputeDataHashAllocationBound(t *testing.T) {
	issues := make([]model.Issue, 256)
	for i := range issues {
		issues[i] = model.Issue{ID: fmt.Sprint(i), Title: "allocation bound", Description: strings.Repeat("日本語\x00", 128)}
	}
	want := analysis.ComputeDataHash(issues)
	allocs := testing.AllocsPerRun(5, func() {
		if got := analysis.ComputeDataHash(issues); got != want {
			t.Fatalf("repeated hash changed: got %s want %s", got, want)
		}
	})
	t.Logf("256-issue aggregate: %.0f allocations", allocs)
	// Allow four allocations per issue, including the aggregate's overhead.
	// Recreating both the buffered writer and SHA state for every issue exceeds
	// this bound even when the content itself is streamed without allocations.
	if allocs > 4*float64(len(issues)) {
		t.Fatalf("aggregate hashing allocated %.0f times; want at most %d", allocs, 4*len(issues))
	}
}

func BenchmarkComputeDataHash(b *testing.B) {
	for _, count := range []int{100, 10000} {
		for _, dependencies := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/dependencies=%t", count, dependencies), func(b *testing.B) {
				issues := make([]model.Issue, count)
				for i := range issues {
					issues[i] = model.Issue{ID: fmt.Sprint(i), Title: "fingerprint benchmark", Description: strings.Repeat("日本語\x00", 128), Status: model.StatusOpen}
					if dependencies && i > 0 {
						issues[i].Dependencies = []*model.Dependency{{IssueID: issues[i].ID, DependsOnID: issues[i-1].ID, Type: model.DepBlocks}}
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					analysis.ComputeDataHash(issues)
				}
			})
		}
	}
}

func BenchmarkComputeIssueFingerprintText(b *testing.B) {
	for _, size := range []int{64, 512, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			issue := model.Issue{ID: "hash", Title: "Unicode fingerprint", Description: strings.Repeat("日本語🚀\x00", size/14+1), Status: model.StatusOpen}
			b.ReportAllocs()
			b.SetBytes(int64(len(issue.Description)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				analysis.ComputeIssueFingerprint(issue)
			}
		})
	}
}

// robotCacheEntryPath mirrors the v3 per-entry disk-cache layout (issue #192):
// one JSON file per key, named by the first 16 bytes of sha256(key), under
// <cacheDir>/analysis_cache/.
func robotCacheEntryPath(cacheDir, fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return filepath.Join(cacheDir, "analysis_cache", hex.EncodeToString(sum[:16])+".json")
}

func TestComputeDataHash_Empty(t *testing.T) {
	hash := analysis.ComputeDataHash(nil)
	if hash != "empty" {
		t.Errorf("Expected 'empty' for nil issues, got %s", hash)
	}

	hash = analysis.ComputeDataHash([]model.Issue{})
	if hash != "empty" {
		t.Errorf("Expected 'empty' for empty slice, got %s", hash)
	}
}

func TestComputeDataHash_Deterministic(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "One"},
		{ID: "B", Title: "Two"},
	}

	hash1 := analysis.ComputeDataHash(issues)
	hash2 := analysis.ComputeDataHash(issues)

	if hash1 != hash2 {
		t.Errorf("Hash should be deterministic: %s != %s", hash1, hash2)
	}
	if len(hash1) != sha256.Size*2 {
		t.Errorf("Hash length = %d, want full SHA-256 hex length %d", len(hash1), sha256.Size*2)
	}
}

func TestComputeDataHash_OrderIndependent(t *testing.T) {
	issues1 := []model.Issue{
		{ID: "A", Title: "One"},
		{ID: "B", Title: "Two"},
	}
	issues2 := []model.Issue{
		{ID: "B", Title: "Two"},
		{ID: "A", Title: "One"},
	}

	hash1 := analysis.ComputeDataHash(issues1)
	hash2 := analysis.ComputeDataHash(issues2)

	if hash1 != hash2 {
		t.Errorf("Hash should be order-independent: %s != %s", hash1, hash2)
	}
}

func TestComputeDataHash_DuplicateIDOrderIsSemantic(t *testing.T) {
	issues1 := []model.Issue{
		{ID: "A", Title: "first"},
		{ID: "A", Title: "second"},
	}
	issues2 := []model.Issue{issues1[1], issues1[0]}

	if hash1, hash2 := analysis.ComputeDataHash(issues1), analysis.ComputeDataHash(issues2); hash1 == hash2 {
		t.Fatalf("duplicate-ID reorder produced a false cache hit: %s", hash1)
	}
}

func TestComputeDataHash_LengthPrefixesPreventEmbeddedNULCollision(t *testing.T) {
	issues1 := []model.Issue{{ID: "A", Title: "a\x00b", Description: "c"}}
	issues2 := []model.Issue{{ID: "A", Title: "a", Description: "b\x00c"}}

	if hash1, hash2 := analysis.ComputeDataHash(issues1), analysis.ComputeDataHash(issues2); hash1 == hash2 {
		t.Fatalf("structurally distinct embedded-NUL fields collided: %s", hash1)
	}
	fp1 := analysis.ComputeIssueFingerprint(issues1[0])
	fp2 := analysis.ComputeIssueFingerprint(issues2[0])
	if fp1.ContentHash == fp2.ContentHash {
		t.Fatalf("content fingerprints collided across embedded-NUL field boundary: %s", fp1.ContentHash)
	}
}

func TestComputeDataHash_CoversPointerPresenceAndCompactionFields(t *testing.T) {
	empty := ""
	zero := 0
	zeroTime := time.Time{}
	tests := []struct {
		name   string
		mutate func(*model.Issue)
	}{
		{name: "external ref presence", mutate: func(issue *model.Issue) { issue.ExternalRef = &empty }},
		{name: "estimated minutes presence", mutate: func(issue *model.Issue) { issue.EstimatedMinutes = &zero }},
		{name: "due date presence", mutate: func(issue *model.Issue) { issue.DueDate = &zeroTime }},
		{name: "defer until presence", mutate: func(issue *model.Issue) { issue.DeferUntil = &zeroTime }},
		{name: "closed at presence", mutate: func(issue *model.Issue) { issue.ClosedAt = &zeroTime }},
		{name: "compaction level", mutate: func(issue *model.Issue) { issue.CompactionLevel = 1 }},
		{name: "compacted at presence", mutate: func(issue *model.Issue) { issue.CompactedAt = &zeroTime }},
		{name: "compacted commit presence", mutate: func(issue *model.Issue) { issue.CompactedAtCommit = &empty }},
		{name: "original size", mutate: func(issue *model.Issue) { issue.OriginalSize = 1 }},
	}

	baseHash := analysis.ComputeDataHash([]model.Issue{{ID: "A"}})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := model.Issue{ID: "A"}
			tt.mutate(&issue)
			if got := analysis.ComputeDataHash([]model.Issue{issue}); got == baseHash {
				t.Fatalf("field change did not change aggregate data hash %q", got)
			}
		})
	}
}

func TestComputeDataHash_DifferentData(t *testing.T) {
	issues1 := []model.Issue{{ID: "A", Title: "Alpha"}}
	issues2 := []model.Issue{{ID: "A", Title: "Beta"}}  // title change
	issues3 := []model.Issue{{ID: "B", Title: "Alpha"}} // id change

	hash1 := analysis.ComputeDataHash(issues1)
	hash2 := analysis.ComputeDataHash(issues2)
	hash3 := analysis.ComputeDataHash(issues3)

	if hash1 == hash2 {
		t.Error("Different content hashes should produce different hashes")
	}
	if hash1 == hash3 {
		t.Error("Different IDs should produce different hashes")
	}
}

func TestComputeDataHash_Dependencies(t *testing.T) {
	issues1 := []model.Issue{{
		ID: "A",
		Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks},
		},
	}}
	issues2 := []model.Issue{{
		ID:           "A",
		Dependencies: nil,
	}}

	hash1 := analysis.ComputeDataHash(issues1)
	hash2 := analysis.ComputeDataHash(issues2)

	if hash1 == hash2 {
		t.Error("Different dependencies should produce different hashes")
	}
}

func TestCache_GetSet(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues := []model.Issue{{ID: "A"}}

	// Initially empty
	stats, ok := cache.Get(issues)
	if ok || stats != nil {
		t.Error("Cache should be empty initially")
	}

	// Create and cache stats
	an := analysis.NewAnalyzer(issues)
	graphStats := an.AnalyzeAsync(context.Background())
	graphStats.WaitForPhase2()

	cache.Set(issues, graphStats)

	// Should hit cache
	cached, ok := cache.Get(issues)
	if !ok {
		t.Error("Cache should hit after Set")
	}
	if cached != graphStats {
		t.Error("Cached stats should match original")
	}
}

func TestCacheRejectsCanceledPhase2AndAllowsLiveRetry(t *testing.T) {
	t.Setenv("BV_ROBOT", "0")

	cache := analysis.NewCache(5 * time.Minute)
	issues := []model.Issue{
		{
			ID:           "outer-cache-cancel-dependent",
			Status:       model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "outer-cache-cancel-blocker", Type: model.DepBlocks}},
		},
		{ID: "outer-cache-cancel-blocker", Status: model.StatusOpen},
	}
	config := analysis.AnalysisConfig{
		RunToCompletion: true,
		ComputePageRank: true,
	}

	canceledAnalyzer := analysis.NewCachedAnalyzer(issues, cache)
	canceledAnalyzer.SetConfig(&config)
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := canceledAnalyzer.AnalyzeAsync(canceledContext)
	canceled.WaitForPhase2()
	if canceled.IsPhase2Ready() {
		t.Fatal("canceled analysis unexpectedly published Phase 2")
	}

	// Exercise the public cache insertion guard deterministically as well as the
	// CachedAnalyzer's asynchronous insertion attempt.
	cacheKey := canceledAnalyzer.DataHash() + "|" + analysis.ComputeConfigHash(&config)
	cache.SetByHash(cacheKey, canceled)
	if _, ok := cache.GetByHash(cacheKey); ok {
		t.Fatal("cache returned an incomplete Phase 2 result")
	}
	if _, _, hasData := cache.Stats(); hasData {
		t.Fatal("cache stored an incomplete Phase 2 result")
	}

	liveAnalyzer := analysis.NewCachedAnalyzer(issues, cache)
	liveAnalyzer.SetConfig(&config)
	live := liveAnalyzer.AnalyzeAsync(context.Background())
	if liveAnalyzer.WasCacheHit() || live == canceled {
		t.Fatal("live retry reused canceled cache result")
	}
	live.WaitForPhase2()
	if !live.IsPhase2Ready() {
		t.Fatal("live retry did not complete Phase 2")
	}
	if len(live.PageRank()) != len(issues) {
		t.Fatalf("live retry PageRank has %d entries, want %d", len(live.PageRank()), len(issues))
	}
}

func TestCache_HashMismatch(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues1 := []model.Issue{{ID: "A"}}
	issues2 := []model.Issue{{ID: "B"}}

	an := analysis.NewAnalyzer(issues1)
	graphStats := an.AnalyzeAsync(context.Background())
	graphStats.WaitForPhase2()

	cache.Set(issues1, graphStats)

	// Different issues should miss
	cached, ok := cache.Get(issues2)
	if ok || cached != nil {
		t.Error("Cache should miss for different data")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	cache := analysis.NewCache(50 * time.Millisecond)
	issues := []model.Issue{{ID: "A"}}

	an := analysis.NewAnalyzer(issues)
	graphStats := an.AnalyzeAsync(context.Background())
	graphStats.WaitForPhase2()

	cache.Set(issues, graphStats)

	// Should hit immediately
	_, ok := cache.Get(issues)
	if !ok {
		t.Error("Cache should hit immediately after Set")
	}

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	// Should miss after TTL
	_, ok = cache.Get(issues)
	if ok {
		t.Error("Cache should miss after TTL expires")
	}
}

func TestCache_Invalidate(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues := []model.Issue{{ID: "A"}}

	an := analysis.NewAnalyzer(issues)
	graphStats := an.AnalyzeAsync(context.Background())
	graphStats.WaitForPhase2()

	cache.Set(issues, graphStats)

	// Should hit
	_, ok := cache.Get(issues)
	if !ok {
		t.Error("Cache should hit after Set")
	}

	// Invalidate
	cache.Invalidate()

	// Should miss after invalidate
	_, ok = cache.Get(issues)
	if ok {
		t.Error("Cache should miss after Invalidate")
	}
}

func TestCache_Stats(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues := []model.Issue{{ID: "A"}}

	// Initially no data
	_, _, hasData := cache.Stats()
	if hasData {
		t.Error("Should have no data initially")
	}

	an := analysis.NewAnalyzer(issues)
	graphStats := an.AnalyzeAsync(context.Background())
	graphStats.WaitForPhase2()

	cache.Set(issues, graphStats)

	hash, age, hasData := cache.Stats()
	if !hasData {
		t.Error("Should have data after Set")
	}
	if hash == "" {
		t.Error("Hash should not be empty")
	}
	if age < 0 || age > time.Second {
		t.Errorf("Age should be reasonable: %v", age)
	}
}

func TestCachedAnalyzer_CacheHit(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	// First analysis - cache miss
	ca1 := analysis.NewCachedAnalyzer(issues, cache)
	stats1 := ca1.AnalyzeAsync(context.Background())
	stats1.WaitForPhase2()

	if ca1.WasCacheHit() {
		t.Error("First analysis should be a cache miss")
	}

	// Wait a bit for cache to be populated
	time.Sleep(10 * time.Millisecond)

	// Second analysis - should hit cache
	ca2 := analysis.NewCachedAnalyzer(issues, cache)
	stats2 := ca2.AnalyzeAsync(context.Background())

	if !ca2.WasCacheHit() {
		t.Error("Second analysis should be a cache hit")
	}

	// Should return same stats pointer
	if stats1 != stats2 {
		t.Error("Cache hit should return same stats pointer")
	}
}

func TestCachedAnalyzer_CacheMiss_DifferentData(t *testing.T) {
	cache := analysis.NewCache(5 * time.Minute)
	issues1 := []model.Issue{{ID: "A"}}
	issues2 := []model.Issue{{ID: "B"}}

	// First analysis
	ca1 := analysis.NewCachedAnalyzer(issues1, cache)
	stats1 := ca1.AnalyzeAsync(context.Background())
	stats1.WaitForPhase2()

	// Wait for cache
	time.Sleep(10 * time.Millisecond)

	// Different data - should miss
	ca2 := analysis.NewCachedAnalyzer(issues2, cache)
	stats2 := ca2.AnalyzeAsync(context.Background())

	if ca2.WasCacheHit() {
		t.Error("Different data should be a cache miss")
	}

	// Should return different stats
	if stats1 == stats2 {
		t.Error("Cache miss should compute new stats")
	}
}

func TestCachedAnalyzer_DataHash(t *testing.T) {
	issues := []model.Issue{{ID: "A", ContentHash: "test"}}
	ca := analysis.NewCachedAnalyzer(issues, nil)

	hash := ca.DataHash()
	expected := analysis.ComputeDataHash(issues)

	if hash != expected {
		t.Errorf("DataHash() = %s, want %s", hash, expected)
	}
}

func TestComputeIssueFingerprint_Deterministic(t *testing.T) {
	ts := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	issueA := model.Issue{
		ID:        "A",
		Title:     "Title",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		Labels:    []string{"b", "a"},
		Dependencies: []*model.Dependency{
			{DependsOnID: "B", Type: model.DepBlocks, CreatedAt: ts, CreatedBy: "alice"},
			{DependsOnID: "A", Type: model.DepRelated, CreatedAt: ts.Add(time.Minute), CreatedBy: "bob"},
		},
		Comments: []*model.Comment{
			{ID: "2", IssueID: "A", Author: "bob", Text: "second", CreatedAt: ts.Add(2 * time.Minute)},
			{ID: "1", IssueID: "A", Author: "alice", Text: "first", CreatedAt: ts},
		},
	}
	issueB := issueA
	issueB.Labels = []string{"a", "b"}
	issueB.Dependencies = []*model.Dependency{
		issueA.Dependencies[1],
		issueA.Dependencies[0],
	}
	issueB.Comments = []*model.Comment{
		issueA.Comments[1],
		issueA.Comments[0],
	}

	fpA := analysis.ComputeIssueFingerprint(issueA)
	fpB := analysis.ComputeIssueFingerprint(issueB)

	if fpA.ContentHash != fpB.ContentHash {
		t.Fatalf("ContentHash mismatch: %s vs %s", fpA.ContentHash, fpB.ContentHash)
	}
	if fpA.DependencyHash != fpB.DependencyHash {
		t.Fatalf("DependencyHash mismatch: %s vs %s", fpA.DependencyHash, fpB.DependencyHash)
	}
}

func TestComputeIssueFingerprint_CommentTieOrderIndependent(t *testing.T) {
	ts := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	alice := &model.Comment{ID: "same", IssueID: "A", Author: "alice", Text: "first", CreatedAt: ts}
	bob := &model.Comment{ID: "same", IssueID: "A", Author: "bob", Text: "second", CreatedAt: ts}

	issueA := model.Issue{ID: "A", Comments: []*model.Comment{alice, bob}}
	issueB := model.Issue{ID: "A", Comments: []*model.Comment{bob, alice}}

	fpA := analysis.ComputeIssueFingerprint(issueA)
	fpB := analysis.ComputeIssueFingerprint(issueB)
	if fpA.ContentHash != fpB.ContentHash {
		t.Fatalf("comment input order changed ContentHash: %s vs %s", fpA.ContentHash, fpB.ContentHash)
	}
}

func TestComputeIssueFingerprint_NilDependenciesIgnored(t *testing.T) {
	withoutDependencies := analysis.ComputeIssueFingerprint(model.Issue{ID: "A"})
	withNilDependency := analysis.ComputeIssueFingerprint(model.Issue{
		ID:           "A",
		Dependencies: []*model.Dependency{nil},
	})

	if withoutDependencies.DependencyHash != withNilDependency.DependencyHash {
		t.Fatalf("nil dependency changed DependencyHash: %s vs %s", withoutDependencies.DependencyHash, withNilDependency.DependencyHash)
	}
}

func TestComputeIssueFingerprint_IncludesNestedIssueIDs(t *testing.T) {
	commentA := model.Issue{ID: "A", Comments: []*model.Comment{{ID: "comment", IssueID: "A"}}}
	commentB := model.Issue{ID: "A", Comments: []*model.Comment{{ID: "comment", IssueID: "B"}}}
	if a, b := analysis.ComputeIssueFingerprint(commentA), analysis.ComputeIssueFingerprint(commentB); a.ContentHash == b.ContentHash {
		t.Fatalf("comment IssueID change did not change content hash %q", a.ContentHash)
	}

	dependencyA := model.Issue{ID: "A", Dependencies: []*model.Dependency{{IssueID: "A", DependsOnID: "B"}}}
	dependencyB := model.Issue{ID: "A", Dependencies: []*model.Dependency{{IssueID: "C", DependsOnID: "B"}}}
	if a, b := analysis.ComputeIssueFingerprint(dependencyA), analysis.ComputeIssueFingerprint(dependencyB); a.DependencyHash == b.DependencyHash {
		t.Fatalf("dependency IssueID change did not change dependency hash %q", a.DependencyHash)
	}
}

func TestComputeIssueFingerprint_PointerPresenceChangesContentHash(t *testing.T) {
	empty := ""
	zero := 0
	zeroTime := time.Time{}
	tests := []struct {
		name   string
		mutate func(*model.Issue)
	}{
		{name: "external ref", mutate: func(issue *model.Issue) { issue.ExternalRef = &empty }},
		{name: "estimated minutes", mutate: func(issue *model.Issue) { issue.EstimatedMinutes = &zero }},
		{name: "due date", mutate: func(issue *model.Issue) { issue.DueDate = &zeroTime }},
		{name: "defer until", mutate: func(issue *model.Issue) { issue.DeferUntil = &zeroTime }},
		{name: "closed at", mutate: func(issue *model.Issue) { issue.ClosedAt = &zeroTime }},
		{name: "compacted at", mutate: func(issue *model.Issue) { issue.CompactedAt = &zeroTime }},
		{name: "compacted at commit", mutate: func(issue *model.Issue) { issue.CompactedAtCommit = &empty }},
	}

	baseHash := analysis.ComputeIssueFingerprint(model.Issue{ID: "A"}).ContentHash
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := model.Issue{ID: "A"}
			tt.mutate(&issue)
			got := analysis.ComputeIssueFingerprint(issue).ContentHash
			if got == baseHash {
				t.Fatalf("present empty/zero pointer did not change ContentHash %q", got)
			}
		})
	}
}

func TestComputeIssueDiff(t *testing.T) {
	ts := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	oldIssues := []model.Issue{
		{ID: "A", Title: "Title", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{
			ID:        "B",
			Title:     "Depends on A",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepBlocks, CreatedAt: ts, CreatedBy: "alice"},
			},
		},
		{ID: "C", Title: "Removed", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "E", Title: "Unchanged", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}
	newIssues := []model.Issue{
		{ID: "A", Title: "Title updated", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{
			ID:        "B",
			Title:     "Depends on A",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{
				{DependsOnID: "A", Type: model.DepRelated, CreatedAt: ts, CreatedBy: "alice"},
			},
		},
		{ID: "D", Title: "Added", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "E", Title: "Unchanged", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}

	diff := analysis.ComputeIssueDiff(oldIssues, newIssues)

	if got := strings.Join(diff.Added, ","); got != "D" {
		t.Fatalf("Added=%q, want %q", got, "D")
	}
	if got := strings.Join(diff.Removed, ","); got != "C" {
		t.Fatalf("Removed=%q, want %q", got, "C")
	}
	if got := strings.Join(diff.ContentChanged, ","); got != "A" {
		t.Fatalf("ContentChanged=%q, want %q", got, "A")
	}
	if got := strings.Join(diff.DependencyChanged, ","); got != "B" {
		t.Fatalf("DependencyChanged=%q, want %q", got, "B")
	}
	if got := strings.Join(diff.Modified, ","); got != "A,B" {
		t.Fatalf("Modified=%q, want %q", got, "A,B")
	}
	if got := strings.Join(diff.Unchanged, ","); got != "E" {
		t.Fatalf("Unchanged=%q, want %q", got, "E")
	}
}

func TestComputeIssueDiff_OrderIndependent(t *testing.T) {
	oldIssues := []model.Issue{
		{ID: "A", Title: "changed"},
		{ID: "B", Title: "removed"},
		{ID: "C", Title: "unchanged"},
	}
	newIssues := []model.Issue{
		{ID: "D", Title: "added"},
		{ID: "C", Title: "unchanged"},
		{ID: "A", Title: "updated"},
	}
	want := analysis.ComputeIssueDiff(oldIssues, newIssues)

	permutedOld := []model.Issue{oldIssues[2], oldIssues[0], oldIssues[1]}
	permutedNew := []model.Issue{newIssues[2], newIssues[0], newIssues[1]}
	got := analysis.ComputeIssueDiff(permutedOld, permutedNew)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input order changed diff:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestComputeIssueDiff_ContentAndDependencyChange(t *testing.T) {
	oldIssues := []model.Issue{{
		ID:           "A",
		Title:        "before",
		Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}},
	}}
	newIssues := []model.Issue{{
		ID:           "A",
		Title:        "after",
		Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}},
	}}

	diff := analysis.ComputeIssueDiff(oldIssues, newIssues)
	if !reflect.DeepEqual(diff.Modified, []string{"A"}) {
		t.Fatalf("Modified=%v, want [A] exactly once", diff.Modified)
	}
	if !reflect.DeepEqual(diff.ContentChanged, []string{"A"}) {
		t.Fatalf("ContentChanged=%v, want [A]", diff.ContentChanged)
	}
	if !reflect.DeepEqual(diff.DependencyChanged, []string{"A"}) {
		t.Fatalf("DependencyChanged=%v, want [A]", diff.DependencyChanged)
	}
}

func TestComputeIssueDiff_DuplicateIDsForceFullRebuild(t *testing.T) {
	tests := []struct {
		name string
		old  []model.Issue
		new  []model.Issue
	}{
		{
			name: "duplicate added",
			old:  []model.Issue{{ID: "A", Title: "old"}},
			new: []model.Issue{
				{ID: "A", Title: "old"},
				{ID: "B", Title: "first"},
				{ID: "B", Title: "second"},
			},
		},
		{
			name: "duplicate removed",
			old: []model.Issue{
				{ID: "A", Title: "old"},
				{ID: "A", Title: "shadow"},
			},
			new: []model.Issue{{ID: "A", Title: "old"}},
		},
		{
			name: "duplicate unchanged",
			old: []model.Issue{
				{ID: "A", Title: "first"},
				{ID: "A", Title: "second"},
			},
			new: []model.Issue{
				{ID: "A", Title: "first"},
				{ID: "A", Title: "second"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := analysis.ComputeIssueDiff(tt.old, tt.new)
			if !diff.HasDuplicateIDs {
				t.Fatalf("duplicate IDs were not reported: %#v", diff)
			}
		})
	}
}

func TestGlobalCache(t *testing.T) {
	cache := analysis.GetGlobalCache()
	if cache == nil {
		t.Error("Global cache should not be nil")
	}

	// Clear any existing state
	cache.Invalidate()

	issues := []model.Issue{{ID: "test-global"}}
	an := analysis.NewAnalyzer(issues)
	stats := an.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	cache.Set(issues, stats)

	// Should be accessible
	cached, ok := cache.Get(issues)
	if !ok {
		t.Error("Global cache should return cached stats")
	}
	if cached != stats {
		t.Error("Global cache should return same stats")
	}

	// Clean up
	cache.Invalidate()
}

func TestRobotDiskCache_WritesAndHits(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	config := analysis.ConfigForSize(2, 1)
	stats1 := an.AnalyzeAsyncWithConfig(context.Background(), config)
	stats1.WaitForPhase2()

	dataHash := analysis.ComputeDataHash(issues)
	configHash := analysis.ComputeConfigHash(&config)
	fullKey := dataHash + "|" + configHash

	cachePath := robotCacheEntryPath(cacheDir, fullKey)
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cache entry file: %v", err)
	}
	var entry struct {
		Version int    `json:"version"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("parsing cache entry json: %v", err)
	}
	// Must track robotAnalysisDiskCacheVersion in cache.go (unexported; this is
	// an external test package). Bumped to 6 for active-only critical path/slack fallback.
	if entry.Version != 6 {
		t.Fatalf("cache entry version: got %d, want %d", entry.Version, 6)
	}
	if entry.Key != fullKey {
		t.Fatalf("cache entry key: got %q, want %q", entry.Key, fullKey)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	an2 := analysis.NewAnalyzer(issues)
	stats2 := an2.AnalyzeAsyncWithConfig(ctx, config)
	stats2.WaitForPhase2()

	if !stats2.IsPhase2Ready() {
		t.Fatalf("expected phase2 ready on cache hit")
	}
	if !reflect.DeepEqual(stats1.PageRank(), stats2.PageRank()) {
		t.Fatalf("pagerank mismatch on cache hit")
	}
	if !reflect.DeepEqual(stats1.Betweenness(), stats2.Betweenness()) {
		t.Fatalf("betweenness mismatch on cache hit")
	}
	if !reflect.DeepEqual(stats1.Cycles(), stats2.Cycles()) {
		t.Fatalf("cycles mismatch on cache hit")
	}
}

func TestRobotDiskCache_BeadsDBDirectoryUsesChildModTime(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"A","title":"A","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirModTime := time.Now().Add(-2 * time.Hour).UTC()
	staleCreatedAt := dirModTime.Add(time.Hour)
	childModTime := staleCreatedAt.Add(10 * time.Minute)
	if err := os.Chtimes(beadsDir, dirModTime, dirModTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(jsonlPath, childModTime, childModTime); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirInfo.ModTime().Before(staleCreatedAt) {
		t.Fatalf("test setup requires directory mtime before stale cache entry: got %v, want before %v", dirInfo.ModTime(), staleCreatedAt)
	}
	childInfo, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !childInfo.ModTime().After(staleCreatedAt) {
		t.Fatalf("test setup requires child mtime after stale cache entry: got %v, want after %v", childInfo.ModTime(), staleCreatedAt)
	}
	t.Setenv("BEADS_DB", beadsDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}
	config := analysis.ConfigForSize(2, 1)
	dataHash := analysis.ComputeDataHash(issues)
	configHash := analysis.ComputeConfigHash(&config)
	fullKey := dataHash + "|" + configHash

	an := analysis.NewAnalyzer(issues)
	stats1 := an.AnalyzeAsyncWithConfig(context.Background(), config)
	stats1.WaitForPhase2()

	cachePath := robotCacheEntryPath(cacheDir, fullKey)
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cache entry file: %v", err)
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("parsing cache entry json: %v", err)
	}
	createdAtRaw, err := json.Marshal(staleCreatedAt)
	if err != nil {
		t.Fatalf("marshalling stale timestamp: %v", err)
	}
	zeroDurationRaw, err := json.Marshal(int64(0))
	if err != nil {
		t.Fatalf("marshalling compute duration: %v", err)
	}
	entry["created_at"] = createdAtRaw
	entry["compute_duration"] = zeroDurationRaw
	raw, err = json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshalling cache entry json: %v", err)
	}
	if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
		t.Fatalf("writing cache entry file: %v", err)
	}

	an2 := analysis.NewAnalyzer(issues)
	stats2 := an2.AnalyzeAsyncWithConfig(context.Background(), config)
	stats2.WaitForPhase2()
	if !stats2.IsPhase2Ready() {
		t.Fatal("expected recomputed stats to reach phase2 ready")
	}

	raw, err = os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading rewritten cache entry file: %v", err)
	}
	var updated struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("parsing rewritten cache entry json: %v", err)
	}
	if !updated.CreatedAt.After(staleCreatedAt) {
		t.Fatalf("expected child file mtime to invalidate stale cache entry, got CreatedAt %v", updated.CreatedAt)
	}
}

func TestRobotDiskCache_ExpiredEntryRecomputes(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}

	an := analysis.NewAnalyzer(issues)
	config := analysis.ConfigForSize(2, 1)
	stats1 := an.AnalyzeAsyncWithConfig(context.Background(), config)
	stats1.WaitForPhase2()

	dataHash := analysis.ComputeDataHash(issues)
	configHash := analysis.ComputeConfigHash(&config)
	fullKey := dataHash + "|" + configHash
	cachePath := robotCacheEntryPath(cacheDir, fullKey)

	readEntry := func() map[string]json.RawMessage {
		t.Helper()
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatalf("reading cache entry file: %v", err)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("parsing cache entry json: %v", err)
		}
		return entry
	}

	entry := readEntry()
	// The cache TTL is 24 hours. A one-hour-old entry is still fresh; age it
	// past its actual expiry to require recomputation independently of RNG.
	staleCreatedAt := time.Now().Add(-25 * time.Hour).UTC()
	createdAtRaw, err := json.Marshal(staleCreatedAt)
	if err != nil {
		t.Fatalf("marshalling stale timestamp: %v", err)
	}
	durationRaw, err := json.Marshal(int64(time.Millisecond))
	if err != nil {
		t.Fatalf("marshalling compute duration: %v", err)
	}
	entry["created_at"] = createdAtRaw
	entry["compute_duration"] = durationRaw

	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshalling cache entry json: %v", err)
	}
	if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
		t.Fatalf("writing cache entry file: %v", err)
	}

	an2 := analysis.NewAnalyzer(issues)
	stats2 := an2.AnalyzeAsyncWithConfig(context.Background(), config)
	stats2.WaitForPhase2()
	if !stats2.IsPhase2Ready() {
		t.Fatal("expected recomputed stats to reach phase2 ready")
	}

	var refreshed struct {
		CreatedAt time.Time `json:"created_at"`
	}
	raw, err = os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading refreshed cache entry file: %v", err)
	}
	if err := json.Unmarshal(raw, &refreshed); err != nil {
		t.Fatalf("parsing refreshed cache entry json: %v", err)
	}
	if !refreshed.CreatedAt.After(staleCreatedAt) {
		t.Fatalf("expected expired entry to rewrite CreatedAt, got %v", refreshed.CreatedAt)
	}
}

func TestRobotDiskCache_EvictsToMaxEntries(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)

	config := analysis.ConfigForSize(1, 0)
	for i := 0; i < 11; i++ {
		issues := []model.Issue{{ID: fmt.Sprintf("I%02d", i), Status: model.StatusOpen}}
		an := analysis.NewAnalyzer(issues)
		stats := an.AnalyzeAsyncWithConfig(context.Background(), config)
		stats.WaitForPhase2()
	}

	entries, err := os.ReadDir(filepath.Join(cacheDir, "analysis_cache"))
	if err != nil {
		t.Fatalf("reading cache dir: %v", err)
	}
	count := 0
	for _, de := range entries {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".json") {
			count++
		}
	}
	if count != 10 {
		t.Fatalf("expected 10 entry files after eviction, got %d", count)
	}
}

// BenchmarkRobotDiskCache_ReadHit measures the steady-state read-hit path: a
// large graph's stats are already cached and analyzer/key/context setup is
// complete before the timer starts. The cancelled context guarantees XFetch
// cannot turn a hit into a recompute. The timed region therefore measures the
// entry read, JSON decode, and GraphStats reconstruction rather than rebuilding
// and hashing the 4,000-issue analyzer on every iteration.
func BenchmarkRobotDiskCache_ReadHit(b *testing.B) {
	b.Setenv("BV_ROBOT", "1")
	cacheDir := b.TempDir()
	b.Setenv("BV_CACHE_DIR", cacheDir)

	// A large dependency graph so the cached GraphStats payload (PageRank,
	// betweenness, etc. maps) is hundreds of KB, like a large real cache entry.
	const n = 4000
	issues := make([]model.Issue, 0, n)
	for i := 0; i < n; i++ {
		iss := model.Issue{ID: fmt.Sprintf("ISSUE-%05d", i), Status: model.StatusOpen}
		if i > 0 {
			iss.Dependencies = []*model.Dependency{
				{DependsOnID: fmt.Sprintf("ISSUE-%05d", i-1), Type: model.DepBlocks},
			}
		}
		issues = append(issues, iss)
	}

	// Populate the single target entry via the real compute+put path.
	config := analysis.ConfigForSize(n, n-1)
	an := analysis.NewAnalyzer(issues)
	stats1 := an.AnalyzeAsyncWithConfig(context.Background(), config)
	stats1.WaitForPhase2()

	dataHash := analysis.ComputeDataHash(issues)
	configHash := analysis.ComputeConfigHash(&config)
	if fi, err := os.Stat(robotCacheEntryPath(cacheDir, dataHash+"|"+configHash)); err == nil {
		b.Logf("cache entry size: %d bytes", fi.Size())
	}

	// Prepare the read-side analyzer, key, and context outside the timed region.
	// Seeding uses the same hash that names the populated cache entry.
	readAnalyzer := analysis.NewAnalyzer(issues)
	readAnalyzer.SeedDataHash(dataHash)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const sampleID = "ISSUE-02000"
	wantPageRank := stats1.GetPageRankScore(sampleID)
	wantBetweenness := stats1.GetBetweennessScore(sampleID)
	if wantPageRank == 0 || wantBetweenness == 0 {
		b.Fatalf("benchmark setup produced empty sample scores: pagerank=%v betweenness=%v", wantPageRank, wantBetweenness)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := readAnalyzer.AnalyzeAsyncWithConfig(ctx, config)
		s.WaitForPhase2()
		if !s.IsPhase2Ready() || s.NodeCount != n || s.EdgeCount != n-1 {
			b.Fatalf("invalid cache result: ready=%v nodes=%d edges=%d", s.IsPhase2Ready(), s.NodeCount, s.EdgeCount)
		}
		if got := s.GetPageRankScore(sampleID); got != wantPageRank {
			b.Fatalf("cached pagerank[%s] = %v, want %v", sampleID, got, wantPageRank)
		}
		if got := s.GetBetweennessScore(sampleID); got != wantBetweenness {
			b.Fatalf("cached betweenness[%s] = %v, want %v", sampleID, got, wantBetweenness)
		}
	}
}

func TestComputeDataHashAndFingerprint_FiveNewIssueFields(t *testing.T) {
	base := model.Issue{
		ID:        "ISS-1",
		Title:     "Canonical issue",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Priority:  2,
	}

	variants := []struct {
		name   string
		mutate func(*model.Issue)
	}{
		{"created_by", func(i *model.Issue) { i.CreatedBy = "alice" }},
		{"close_reason", func(i *model.Issue) { i.CloseReason = "shipped in v1" }},
		{"ephemeral", func(i *model.Issue) { i.Ephemeral = true }},
		{"pinned", func(i *model.Issue) { i.Pinned = true }},
		{"is_template", func(i *model.Issue) { i.IsTemplate = true }},
	}

	seenFP := map[analysis.IssueFingerprint]string{
		analysis.ComputeIssueFingerprint(base): "base",
	}
	seenDataHash := map[string]string{
		analysis.ComputeDataHash([]model.Issue{base}): "base",
	}

	for _, v := range variants {
		iss := base
		v.mutate(&iss)

		fp := analysis.ComputeIssueFingerprint(iss)
		if prev, dup := seenFP[fp]; dup {
			t.Fatalf("%s fingerprint collided with %s: %+v", v.name, prev, fp)
		}
		seenFP[fp] = v.name

		dh := analysis.ComputeDataHash([]model.Issue{iss})
		if prev, dup := seenDataHash[dh]; dup {
			t.Fatalf("%s DataHash collided with %s: %s", v.name, prev, dh)
		}
		seenDataHash[dh] = v.name

		diff := analysis.ComputeIssueDiff([]model.Issue{base}, []model.Issue{iss})
		if len(diff.ContentChanged) != 1 || diff.ContentChanged[0] != "ISS-1" {
			t.Fatalf("%s not detected in ComputeIssueDiff.ContentChanged: %+v", v.name, diff)
		}
	}
}

func TestRobotDiskCache_IgnoresNonIssueSidecarsAndFollowsRedirectedBeadsDir(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DIR", "")

	// Set up workspace with subdirectory + .beads/redirect -> centralStore/.beads
	repoRoot := t.TempDir()
	localBeads := filepath.Join(repoRoot, ".beads")
	subDir := filepath.Join(repoRoot, "services", "worker")
	centralBeads := filepath.Join(t.TempDir(), "central", ".beads")
	for _, d := range []string{localBeads, subDir, centralBeads, filepath.Join(centralBeads, ".br_history")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	redirectPath := filepath.Join(localBeads, "redirect")
	if err := os.WriteFile(redirectPath, []byte(centralBeads+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	issuesPath := filepath.Join(centralBeads, "issues.jsonl")
	dbPath := filepath.Join(centralBeads, "beads.db")
	walPath := filepath.Join(centralBeads, "beads.db-wal")
	corrFeedbackPath := filepath.Join(centralBeads, "correlation_feedback.jsonl")

	for _, p := range []string{issuesPath, dbPath, walPath, corrFeedbackPath} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	cacheCreatedAt := base.Add(time.Hour)

	for _, p := range []string{localBeads, redirectPath, centralBeads, filepath.Join(centralBeads, ".br_history"), issuesPath, dbPath, walPath, corrFeedbackPath} {
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(subDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	config := analysis.ConfigForSize(2, 1)
	fullKey := analysis.ComputeDataHash(issues) + "|" + analysis.ComputeConfigHash(&config)

	stats1 := analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	stats1.WaitForPhase2()

	cachePath := robotCacheEntryPath(cacheDir, fullKey)
	pinEntryCreatedAt := func(ts time.Time) []byte {
		t.Helper()
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatalf("read cache entry: %v", err)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		tsRaw, _ := json.Marshal(ts)
		zeroRaw, _ := json.Marshal(int64(0))
		entry["created_at"] = tsRaw
		entry["compute_duration"] = zeroRaw
		out, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cachePath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return out
	}

	pinnedBytes := pinEntryCreatedAt(cacheCreatedAt)

	// Exercise REAL atomic CreateTemp+Rename sidecar writers (FeedbackData.Save &
	// loader.SaveSprintsToFile) plus newly created *.lock, beads.db-shm,
	// correlation_feedback.jsonl, and .br_history entries AFTER cacheCreatedAt —
	// WITHOUT resetting centralBeads directory inode mtime!
	fb := analysis.DefaultFeedbackData()
	if err := fb.RecordFeedback("A", "accept", 0.9, analysis.ScoreBreakdown{PageRankNorm: 0.8}); err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	if err := fb.Save(centralBeads); err != nil {
		t.Fatalf("FeedbackData.Save: %v", err)
	}
	if err := loader.SaveSprintsToFile(filepath.Join(centralBeads, loader.SprintsFileName), []model.Sprint{
		{ID: "sp-1", Name: "Sprint 1", StartDate: base, EndDate: base.Add(24 * time.Hour)},
	}); err != nil {
		t.Fatalf("SaveSprintsToFile: %v", err)
	}
	shmPath := filepath.Join(centralBeads, "beads.db-shm")
	lockTmp := filepath.Join(centralBeads, "export.lock.tmp")
	lockPath := filepath.Join(centralBeads, "export.lock")
	if err := os.WriteFile(shmPath, []byte("shm-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockTmp, []byte("pid=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(lockTmp, lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrFeedbackPath, []byte(`{"bead":"A"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(centralBeads, ".br_history", "snap.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(centralBeads)
	if err != nil {
		t.Fatal(err)
	}
	if !dirInfo.ModTime().After(cacheCreatedAt) {
		t.Fatalf("expected atomic sidecar writes to advance directory inode mtime past %v, got %v", cacheCreatedAt, dirInfo.ModTime())
	}

	// Positive observable: even though centralBeads directory inode mtime and all
	// sidecar files are strictly newer than cacheCreatedAt, the cache entry MUST hit!
	stats2 := analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	stats2.WaitForPhase2()
	afterSidecars, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache entry unexpectedly removed on sidecar mtime bump: %v", err)
	}
	if string(afterSidecars) != string(pinnedBytes) {
		t.Fatal("non-issue sidecar CreateTemp+Rename / beads.db-shm / *.lock thrashed robot disk cache")
	}

	// Planted negative control 1: bumping beads.db-wal on the redirected store MUST invalidate and rewrite the cache entry!
	walNewer := cacheCreatedAt.Add(45 * time.Minute)
	if err := os.Chtimes(walPath, walNewer, walNewer); err != nil {
		t.Fatal(err)
	}
	stats3 := analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	stats3.WaitForPhase2()
	afterWAL, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("expected regenerated cache entry after beads.db-wal bump: %v", err)
	}
	if string(afterWAL) == string(pinnedBytes) {
		t.Fatal("beads.db-wal mtime bump on redirected store failed to invalidate stale robot disk cache")
	}

	// Planted negative control 2: bumping issues.jsonl on the redirected store MUST also invalidate and rewrite!
	if err := os.Chtimes(walPath, base, base); err != nil {
		t.Fatal(err)
	}
	pinnedBytes2 := pinEntryCreatedAt(cacheCreatedAt)
	jsonlNewer := cacheCreatedAt.Add(50 * time.Minute)
	if err := os.Chtimes(issuesPath, jsonlNewer, jsonlNewer); err != nil {
		t.Fatal(err)
	}
	stats4 := analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	stats4.WaitForPhase2()
	afterJSONL, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("expected regenerated cache entry after issues.jsonl bump: %v", err)
	}
	if string(afterJSONL) == string(pinnedBytes2) {
		t.Fatal("issues.jsonl mtime bump on redirected store failed to invalidate stale robot disk cache")
	}
}

func TestRobotDiskCache_SymlinkedDBWALEmbeddedDoltAndMetadataTargets(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	t.Setenv("BEADS_DIR", "")

	base := time.Now().Add(-4 * time.Hour).UTC().Truncate(time.Second)
	cacheCreatedAt := base.Add(time.Hour)
	newer := cacheCreatedAt.Add(30 * time.Minute)

	issues := []model.Issue{
		{ID: "S1", Status: model.StatusOpen},
		{ID: "S2", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "S1", Type: model.DepBlocks}}},
	}
	config := analysis.ConfigForSize(2, 1)
	fullKey := analysis.ComputeDataHash(issues) + "|" + analysis.ComputeConfigHash(&config)
	cachePath := robotCacheEntryPath(cacheDir, fullKey)

	pinCreated := func() []byte {
		t.Helper()
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		tsRaw, _ := json.Marshal(cacheCreatedAt)
		zeroRaw, _ := json.Marshal(int64(0))
		entry["created_at"] = tsRaw
		entry["compute_duration"] = zeroRaw
		out, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cachePath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 1. Symlinked beads.db -> external shared.db: WAL next to symlink target invalidates; -shm does not!
	extDir := t.TempDir()
	sharedDB := filepath.Join(extDir, "shared.db")
	sharedWAL := sharedDB + "-wal"
	sharedSHM := sharedDB + "-shm"
	for _, p := range []string{sharedDB, sharedWAL, sharedSHM} {
		if err := os.WriteFile(p, []byte("sqlite"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
	}
	beads1 := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beads1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedDB, filepath.Join(beads1, "beads.db")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beads1)

	s := analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	pinned := pinCreated()

	// Positive control: bumping shared.db-shm next to symlink target must NOT invalidate.
	if err := os.Chtimes(sharedSHM, newer, newer); err != nil {
		t.Fatal(err)
	}
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	if got, err := os.ReadFile(cachePath); err != nil || string(got) != string(pinned) {
		t.Fatal("symlink target -shm bump falsely invalidated robot disk cache")
	}

	// Planted negative: bumping shared.db-wal next to symlink target MUST invalidate!
	if err := os.Chtimes(sharedWAL, newer, newer); err != nil {
		t.Fatal(err)
	}
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	if got, err := os.ReadFile(cachePath); err != nil || string(got) == string(pinned) {
		t.Fatal("symlink target -wal bump failed to invalidate robot disk cache")
	}

	// 2. embeddeddolt nested store file update MUST invalidate!
	beads2 := filepath.Join(t.TempDir(), ".beads")
	embManifest := filepath.Join(beads2, "embeddeddolt", "noms", "manifest")
	if err := os.MkdirAll(filepath.Dir(embManifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(embManifest, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{embManifest, filepath.Dir(embManifest), filepath.Join(beads2, "embeddeddolt"), beads2} {
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DB", beads2)
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	pinnedEmb := pinCreated()
	if err := os.Chtimes(embManifest, newer, newer); err != nil {
		t.Fatal(err)
	}
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	if got, err := os.ReadFile(cachePath); err != nil || string(got) == string(pinnedEmb) {
		t.Fatal("embeddeddolt nested file mtime bump failed to invalidate robot disk cache")
	}

	// 3. metadata.json custom database + jsonl_export outside standard filenames MUST invalidate!
	root3 := t.TempDir()
	beads3 := filepath.Join(root3, ".beads")
	altDir := filepath.Join(root3, "alt")
	if err := os.MkdirAll(beads3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(altDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customDB := filepath.Join(altDir, "tracker.data")
	customWAL := customDB + "-wal"
	customExport := filepath.Join(altDir, "export.ndjson")
	metaJSON := filepath.Join(beads3, "metadata.json")
	for _, p := range []string{customDB, customWAL, customExport} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(metaJSON, []byte(`{"backend":"sqlite","database":"../alt/tracker.data","jsonl_export":"../alt/export.ndjson"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(metaJSON, base, base); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beads3)
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	pinnedMeta := pinCreated()

	if err := os.Chtimes(customWAL, newer, newer); err != nil {
		t.Fatal(err)
	}
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	if got, err := os.ReadFile(cachePath); err != nil || string(got) == string(pinnedMeta) {
		t.Fatal("metadata.json custom database -wal bump failed to invalidate robot disk cache")
	}

	if err := os.Chtimes(customWAL, base, base); err != nil {
		t.Fatal(err)
	}
	pinnedMeta2 := pinCreated()
	if err := os.Chtimes(customExport, newer, newer); err != nil {
		t.Fatal(err)
	}
	s = analysis.NewAnalyzer(issues).AnalyzeAsyncWithConfig(context.Background(), config)
	s.WaitForPhase2()
	if got, err := os.ReadFile(cachePath); err != nil || string(got) == string(pinnedMeta2) {
		t.Fatal("metadata.json custom jsonl_export bump failed to invalidate robot disk cache")
	}
}
