package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Issue #192: cache validation must stay O(top-level entries). Journals and
// snapshots under .beads/ subdirectories (.br_history/, .br_recovery/, …) can
// hold thousands of files and never feed the graph, so their mtimes must not
// be consulted — and must not be able to invalidate a valid entry.
func TestBeadsTreeModTime_OnlyConsultsTopLevelFiles(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, ".br_history"), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	dataMtime := base.Add(time.Hour)
	journalMtime := base.Add(3 * time.Hour) // newer than everything at top level

	dataFile := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(dataFile, []byte(`{"id":"A","title":"A","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		p := filepath.Join(beadsDir, ".br_history", fmt.Sprintf("snapshot-%02d.jsonl", i))
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, journalMtime, journalMtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(dataFile, dataMtime, dataMtime); err != nil {
		t.Fatal(err)
	}
	// Directory mtimes (including the journal dir) are older than the data file.
	if err := os.Chtimes(filepath.Join(beadsDir, ".br_history"), base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(beadsDir, base, base); err != nil {
		t.Fatal(err)
	}

	got := beadsTreeModTime(beadsDir)
	if !got.Equal(dataMtime) {
		t.Fatalf("beadsTreeModTime = %v, want top-level data mtime %v (journal mtime %v must be ignored)", got, dataMtime, journalMtime)
	}

	// A top-level data file rewritten in place (directory mtime unchanged)
	// still moves the result forward.
	newer := dataMtime.Add(30 * time.Minute)
	if err := os.Chtimes(dataFile, newer, newer); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(beadsDir, base, base); err != nil {
		t.Fatal(err)
	}
	if got := beadsTreeModTime(beadsDir); !got.Equal(newer) {
		t.Fatalf("after in-place rewrite beadsTreeModTime = %v, want %v", got, newer)
	}
}

func TestMemoryCachesRejectFutureTimestamps(t *testing.T) {
	stats := NewGraphStatsForTest(nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, nil)
	now := time.Now()

	cache := NewCache(time.Minute)
	cache.SetByHash("future", stats)
	cache.mu.Lock()
	cache.computedAt = now.Add(time.Hour)
	cache.mu.Unlock()
	if _, ok := cache.GetByHash("future"); ok {
		t.Fatal("ordinary cache accepted a future-dated entry")
	}

	const key = "future-incremental-entry"
	incrementalGraphStatsCacheMu.Lock()
	incrementalGraphStatsCache[key] = incrementalGraphStatsCacheEntry{
		stats:      stats,
		insertedAt: now.Add(time.Hour),
	}
	incrementalGraphStatsCacheMu.Unlock()
	if _, ok := getIncrementalGraphStatsCache(key); ok {
		t.Fatal("incremental graph cache accepted a future-dated entry")
	}
}

func TestMemoryCachesRejectIncompleteAnalysisStates(t *testing.T) {
	for _, state := range []string{"timeout", ""} {
		stats := NewGraphStatsForTest(nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, nil)
		stats.mu.Lock()
		stats.status.PageRank = statusEntry{State: state}
		stats.mu.Unlock()

		key := "unusable-" + state
		cache := NewCache(time.Minute)
		cache.SetByHash(key, stats)
		if _, ok := cache.GetByHash(key); ok {
			t.Fatalf("ordinary cache returned analysis with metric state %q", state)
		}
		if _, _, hasData := cache.Stats(); hasData {
			t.Fatalf("ordinary cache stored analysis with metric state %q", state)
		}

		putIncrementalGraphStatsCache(key, stats)
		if _, ok := getIncrementalGraphStatsCache(key); ok {
			t.Fatalf("incremental graph cache stored analysis with metric state %q", state)
		}
	}
}

// Issue #192: a plain cache miss (key absent) must be a pure read — it must
// not touch other entries, create files, or fsync anything. The put after the
// upcoming recompute is the only write on that path.
func TestRobotDiskCache_MissWithoutPruneDoesNotRewriteFile(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	// Point staleness at an isolated, static .beads so the host cwd cannot
	// influence the check.
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	config := ConfigForSize(2, 1)
	an := NewAnalyzer(issues)
	stats := an.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()

	key := an.DataHash() + "|" + ComputeConfigHash(&config)
	entryDir := filepath.Join(cacheDir, robotAnalysisDiskCacheSubdirName)
	entryPath := filepath.Join(entryDir, robotAnalysisEntryFileName(key))
	before, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("expected cache entry file after first analysis: %v", err)
	}
	// Pin the entry file's mtime in the past so any rewrite is unambiguous.
	// (Recent enough that the mtime-based max-age prune cannot reap it.)
	pinned := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(entryPath, pinned, pinned); err != nil {
		t.Fatal(err)
	}
	filesBefore, err := os.ReadDir(entryDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, hit := getRobotDiskCachedStats("no-such-data-hash|no-such-config-hash"); hit {
		t.Fatal("unexpected hit for an absent key")
	}

	after, err := os.Stat(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(pinned) || after.Size() != before.Size() {
		t.Fatalf("plain miss disturbed the existing entry: mtime %v→%v size %d→%d", pinned, after.ModTime(), before.Size(), after.Size())
	}
	filesAfter, err := os.ReadDir(entryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(filesAfter) != len(filesBefore) {
		t.Fatalf("plain miss changed the cache dir: %d files → %d files", len(filesBefore), len(filesAfter))
	}

	// The genuine entry still hits (the miss above did not disturb it).
	if cached, _, hit := getRobotDiskCachedStats(key); !hit || cached == nil {
		t.Fatalf("expected hit for %q after a miss on another key", key)
	}
}

func TestRobotDiskCacheAcceptsWriterProducedSelfLoopDensityAboveOne(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
			{DependsOnID: "B", Type: model.DepBlocks},
		}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{
			{DependsOnID: "A", Type: model.DepBlocks},
		}},
	}
	config := ConfigForSize(2, 3)
	config.DisableCache = false
	config.PageRankTimeout = time.Second
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()
	if stats.Density != 1.5 {
		t.Fatalf("writer density=%g, want 1.5 for three unique edges over two nodes", stats.Density)
	}

	key := analyzer.DataHash() + "|" + ComputeConfigHash(&config)
	cached, _, hit := getRobotDiskCachedStats(key)
	if !hit || cached == nil {
		t.Fatalf("writer-produced self-loop entry was rejected: hit=%v stats=%p", hit, cached)
	}
	if cached.Density != stats.Density || cached.EdgeCount != 3 {
		t.Fatalf("cached graph shape density=%g edges=%d, want density=%g edges=3", cached.Density, cached.EdgeCount, stats.Density)
	}
}

// Expired entry files are reaped by the write path's prune (any repo's put
// clears the shared dir), the legacy v2 single-file cache is retired, and a
// direct lookup of an expired key reaps its own file.
func TestRobotDiskCache_PutPrunesExpiredEntriesAndLegacyFile(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	entryDir, err := robotAnalysisDiskCacheDir(true)
	if err != nil {
		t.Fatal(err)
	}

	// Start from a real writer-produced v3 entry, then age both its body and
	// mtime. This keeps the key/config/result/status invariants valid so the
	// direct-lookup assertion below genuinely reaches the expiry branch rather
	// than passing because an independently corrupt fixture was reaped earlier.
	oldIssues := []model.Issue{{ID: "OLD", Status: model.StatusOpen}}
	oldConfig := ConfigForSize(1, 0)
	oldConfig.DisableCache = false
	oldConfig.ComputePageRank = true
	oldConfig.PageRankTimeout = time.Second
	oldAnalyzer := NewAnalyzer(oldIssues)
	oldStats := oldAnalyzer.AnalyzeAsyncWithConfig(context.Background(), oldConfig)
	oldStats.WaitForPhase2()
	oldKey := oldAnalyzer.DataHash() + "|" + ComputeConfigHash(&oldConfig)
	oldPath := filepath.Join(entryDir, robotAnalysisEntryFileName(oldKey))
	raw, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read writer-produced cache entry: %v", err)
	}
	var oldEntry robotAnalysisDiskCacheEntry
	if err := json.Unmarshal(raw, &oldEntry); err != nil {
		t.Fatalf("decode writer-produced cache entry: %v", err)
	}
	if got, _, hit := getRobotDiskCachedStats(oldKey); !hit || got == nil {
		t.Fatalf("writer-produced control entry did not hit: hit=%v stats=%p", hit, got)
	}
	oldEntry.CreatedAt = time.Now().Add(-2 * robotAnalysisDiskCacheMaxAge).UTC()
	raw, err = json.Marshal(oldEntry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-2 * robotAnalysisDiskCacheMaxAge)
	if err := os.Chtimes(oldPath, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(cacheDir, robotAnalysisLegacyCacheFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"version":2,"entries":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A put for unrelated data prunes the expired entry and retires the legacy file.
	issues := []model.Issue{{ID: "FRESH", Status: model.StatusOpen}}
	freshConfig := ConfigForSize(1, 0)
	freshConfig.DisableCache = false
	freshConfig.ComputePageRank = true
	freshConfig.PageRankTimeout = time.Second
	an := NewAnalyzer(issues)
	stats := an.AnalyzeAsyncWithConfig(context.Background(), freshConfig)
	stats.WaitForPhase2()

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired entry file should be pruned on put, stat err = %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy analysis_cache.json should be retired on put, stat err = %v", err)
	}

	// A direct lookup of an expired key reaps its own file too.
	if err := os.WriteFile(oldPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, hit := getRobotDiskCachedStats(oldKey); hit {
		t.Fatal("expired entry must not hit")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired entry file should be reaped on lookup, stat err = %v", err)
	}
}
