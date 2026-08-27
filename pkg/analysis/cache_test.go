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
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

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
	if entry.Version != 4 {
		t.Fatalf("cache entry version: got %d, want %d", entry.Version, 4)
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

func TestRobotDiskCache_XFetchRefreshRecomputes(t *testing.T) {
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
	// A year-old CreatedAt would trip the max-age prune, which reaps the entry
	// instead of serving it; XFetch needs a *served* entry whose refresh window
	// has certainly elapsed, so age it one hour with a 1ms compute duration.
	staleCreatedAt := time.Now().Add(-time.Hour).UTC()
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

	time.Sleep(10 * time.Millisecond)

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
		t.Fatalf("expected xfetch refresh to rewrite CreatedAt, got %v", refreshed.CreatedAt)
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
