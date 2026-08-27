package correlation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// repoRootForTest returns this repository's top-level directory (which carries a
// real beads history under .beads/issues.jsonl), or skips if git can't resolve
// it (e.g. a packaged module copy without history).
func repoRootForTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not in a git checkout with history; skipping real-repo cache test")
	}
	root := strings.TrimSpace(string(out))
	if _, statErr := exec.Command("git", "-C", root, "cat-file", "-e", "HEAD:.beads/issues.jsonl").Output(); statErr != nil {
		t.Skip("repo has no .beads/issues.jsonl history; skipping")
	}
	return root
}

func eventKey(ev BeadEvent) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s",
		ev.CommitSHA, ev.BeadID, ev.EventType,
		ev.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
		ev.Author, ev.AuthorEmail, ev.CommitMsg)
}

// assertEventsByteIdentical asserts two []BeadEvent are identical in length,
// order, and every field (not just a sorted multiset) — this is the strong
// guarantee the incremental path must preserve.
func assertEventsByteIdentical(t *testing.T, want, got []BeadEvent, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: length mismatch full=%d incremental=%d", label, len(want), len(got))
	}
	for i := range want {
		if eventKey(want[i]) != eventKey(got[i]) {
			t.Fatalf("%s: event %d differs:\n full        = %s\n incremental = %s",
				label, i, eventKey(want[i]), eventKey(got[i]))
		}
	}
}

// TestPerCommitCacheDifferential is the mandatory correctness test: the
// incremental (per-commit-cache) extraction must produce a byte-identical (same
// events, fields, ORDER) []BeadEvent as a full extraction, across the cold
// (empty cache), partially-warm (k new commits), and fully-warm (0 new) regimes.
// It also proves the incremental path reads only the NEW commits' blobs.
func TestPerCommitCacheDifferential(t *testing.T) {
	root := repoRootForTest(t)

	// Robot mode + isolated cache dir so the disk cache is exercised but cannot
	// touch the user's real cache.
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	e := NewExtractor(root)
	opts := ExtractOptions{}
	namespace := perCommitEventCacheNamespace(e.primaryBeadsFile(), opts.BeadID)

	// Ground truth: a full extraction with the per-commit cache disabled, so it
	// always reads all blobs and never consults the cache.
	t.Setenv("BV_NO_CACHE", "1")
	full, err := e.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("full extraction: %v", err)
	}
	if len(full) == 0 {
		t.Skip("repo produced no bead events; nothing to differentially test")
	}
	t.Logf("full extraction: %d events", len(full))

	commits, err := e.snapshotCommits(opts)
	if err != nil {
		t.Fatalf("snapshotCommits: %v", err)
	}
	t.Logf("history commits: %d", len(commits))

	// Re-enable the cache for the incremental runs.
	t.Setenv("BV_NO_CACHE", "")

	// --- COLD: empty per-commit cache. Must equal full and read all blobs. ---
	atomic.StoreInt64(&blobsReadCounter, 0)
	cold, err := e.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("cold incremental: %v", err)
	}
	coldBlobs := atomic.LoadInt64(&blobsReadCounter)
	assertEventsByteIdentical(t, full, cold, "cold")
	t.Logf("cold: %d events, %d blobs read", len(cold), coldBlobs)
	if coldBlobs == 0 {
		t.Fatalf("cold extraction read 0 blobs; expected to read all uncached blobs")
	}

	// --- FULLY WARM: cache now holds every commit. Must equal full, ~0 blobs. ---
	atomic.StoreInt64(&blobsReadCounter, 0)
	warm, err := e.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("warm incremental: %v", err)
	}
	warmBlobs := atomic.LoadInt64(&blobsReadCounter)
	assertEventsByteIdentical(t, full, warm, "fully-warm")
	t.Logf("fully-warm: %d events, %d blobs read", len(warm), warmBlobs)
	if warmBlobs != 0 {
		t.Fatalf("fully-warm extraction read %d blobs; expected 0 (all commits cached)", warmBlobs)
	}

	// --- PARTIALLY WARM: drop the k newest commits from the cache (simulating a
	// HEAD that advanced by k commits since the cache was built), then re-extract.
	// Must STILL equal full, and read only the k-new commits' blobs (a small
	// number bounded by 2k, far below the full count). ---
	for _, k := range []int{1, 3, 10} {
		if k > len(commits) {
			continue
		}
		evictNewestKFromCache(t, namespace, commits, k)

		atomic.StoreInt64(&blobsReadCounter, 0)
		partial, err := e.extractViaSnapshots(opts)
		if err != nil {
			t.Fatalf("k=%d incremental: %v", k, err)
		}
		kBlobs := atomic.LoadInt64(&blobsReadCounter)
		assertEventsByteIdentical(t, full, partial, fmt.Sprintf("k=%d", k))
		t.Logf("k=%d new: %d events, %d blobs read (full would read ~%d)", k, len(partial), kBlobs, coldBlobs)
		if kBlobs == 0 {
			t.Fatalf("k=%d: read 0 blobs but %d commits were evicted; cache validation likely served stale", k, k)
		}
		if kBlobs > int64(2*k) {
			t.Fatalf("k=%d: read %d blobs; expected at most %d (2 per new commit)", k, kBlobs, 2*k)
		}
		// After this re-extraction the cache is fully warm again (the k commits
		// were re-stored), so the next k iteration starts from a known state.
	}
}

// evictNewestKFromCache deletes the k newest commits (commits[0:k] in git-log
// newest-first order) from the per-commit cache namespace, simulating a cache
// that predates the k most recent commits. It reads, mutates, and rewrites the
// on-disk cache directly using the package's own primitives.
func evictNewestKFromCache(t *testing.T, namespace string, commits []snapshotCommit, k int) {
	t.Helper()
	cached := loadPerCommitEvents(namespace)
	if cached == nil {
		t.Fatalf("expected a warm cache before eviction")
	}
	// Build a fresh full bucket minus the k newest, and overwrite the file.
	path, err := perCommitEventCachePath(true)
	if err != nil {
		t.Fatalf("cache path: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer func() { _ = unlockFile(f) }()

	cf := readPerCommitEventCacheLocked(f)
	bucket := cf.Entries[namespace]
	for i := 0; i < k; i++ {
		delete(bucket.Commits, commits[i].info.SHA)
	}
	cf.Entries[namespace] = bucket
	if err := writePerCommitEventCacheLocked(f, cf); err != nil {
		t.Fatalf("rewrite cache: %v", err)
	}
}

// TestPerCommitCacheNamespaceIsolation verifies BeadID-filtered and unfiltered
// extractions do not share cached entries (their parseDiff output differs).
func TestPerCommitCacheNamespaceIsolation(t *testing.T) {
	e := NewExtractor("/tmp/repo")
	a := perCommitEventCacheNamespace(e.primaryBeadsFile(), "")
	b := perCommitEventCacheNamespace(e.primaryBeadsFile(), "bv-123")
	if a == b {
		t.Fatalf("namespaces collide for different BeadID filters: %q", a)
	}
}

func TestReadPerCommitEventCacheRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "per-commit-events.json")
	fixture := []byte(fmt.Sprintf(`{"version":%d,"entries":{"ns":{"commits":{"sha":{"created_at":"2026-08-26T12:00:00Z","old_sha":"old","new_sha":"new","events":[]}}}}}`, perCommitEventCacheVersion))
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cache fixture: %v", err)
	}
	defer f.Close()

	valid := readPerCommitEventCacheLockedWithLimit(f, int64(len(fixture)))
	if _, ok := valid.Entries["ns"].Commits["sha"]; !ok {
		t.Fatalf("valid cache fixture did not decode: %+v", valid)
	}
	got := readPerCommitEventCacheLockedWithLimit(f, 8)
	if got.Version != perCommitEventCacheVersion || len(got.Entries) != 0 {
		t.Fatalf("oversized cache should fail closed to an empty current-version cache: %+v", got)
	}
}

func TestReadPerCommitEventCacheRejectsPreOversizedRecordFixVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "per-commit-events-v1.json")
	fixture := []byte(`{"version":1,"entries":{"ns":{"commits":{"sha":{"created_at":"2026-08-26T12:00:00Z","old_sha":"old","new_sha":"new","events":[]}}}}}`)
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatalf("write legacy cache fixture: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open legacy cache fixture: %v", err)
	}
	defer f.Close()

	got := readPerCommitEventCacheLockedWithLimit(f, int64(len(fixture)))
	if got.Version != perCommitEventCacheVersion || len(got.Entries) != 0 {
		t.Fatalf("pre-parser-fix cache should fail closed to an empty v%d cache: %+v", perCommitEventCacheVersion, got)
	}
}

func TestPrunePerCommitEventsRejectsFutureTimestamp(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	entries := map[string]perCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitEventEntry{
			"fresh":  {CreatedAt: now.Add(-time.Hour)},
			"future": {CreatedAt: now.Add(time.Nanosecond)},
		}},
	}
	pruneAndBoundPerCommitEntries(now, entries)
	if _, ok := entries["ns"].Commits["fresh"]; !ok {
		t.Fatal("fresh per-commit event entry was removed")
	}
	if _, ok := entries["ns"].Commits["future"]; ok {
		t.Fatal("future-dated per-commit event entry was retained")
	}
}

// TestPruneAndBoundPerCommitEntries verifies the count cap evicts oldest-first.
func TestPruneAndBoundPerCommitEntries(t *testing.T) {
	now := time.Now().UTC()
	entries := map[string]perCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitEventEntry{}},
	}
	// Insert more than the cap; older CreatedAt should be evicted first.
	total := perCommitEventCacheMaxCommits + 50
	for i := 0; i < total; i++ {
		entries["ns"].Commits[fmt.Sprintf("sha-%05d", i)] = perCommitEventEntry{
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
	}
	pruneAndBoundPerCommitEntries(now.Add(time.Hour), entries)
	got := len(entries["ns"].Commits)
	if got != perCommitEventCacheMaxCommits {
		t.Fatalf("after bound: %d commits, want %d", got, perCommitEventCacheMaxCommits)
	}
	// The newest entries (highest index) must survive.
	keys := make([]string, 0, got)
	for k := range entries["ns"].Commits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// The first 50 (oldest CreatedAt = lowest index) must be the ones evicted, so
	// the oldest surviving key is at least index 50.
	if keys[0] < fmt.Sprintf("sha-%05d", 50) {
		t.Fatalf("oldest surviving key %q suggests wrong entries evicted", keys[0])
	}
}

// TestEvictOldestPerCommitEventsPreservesNewest guards the size-overflow path:
// the writer evicts the OLDEST commits (keeping newest) instead of wiping the
// whole file, and returns false only when nothing remains.
func TestEvictOldestPerCommitEventsPreservesNewest(t *testing.T) {
	base := time.Now().UTC()
	ents := map[string]perCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitEventEntry{
			"old": {CreatedAt: base.Add(-3 * time.Hour)},
			"mid": {CreatedAt: base.Add(-2 * time.Hour)},
			"new": {CreatedAt: base.Add(-1 * time.Hour)},
		}},
	}
	if !evictOldestPerCommitEvents(ents) {
		t.Fatal("expected eviction to occur")
	}
	if _, ok := ents["ns"].Commits["old"]; ok {
		t.Error("oldest commit should have been evicted first")
	}
	if _, ok := ents["ns"].Commits["new"]; !ok {
		t.Error("newest commit must be preserved")
	}
	for evictOldestPerCommitEvents(ents) {
	}
	if evictOldestPerCommitEvents(ents) {
		t.Error("evict on empty must return false (loop terminates)")
	}
}
