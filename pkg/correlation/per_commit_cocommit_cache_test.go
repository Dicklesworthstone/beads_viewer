package correlation

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// coCommitKey renders a CorrelatedCommit's full identity (every field the
// extraction can vary, including the Files slice in order) so two []CorrelatedCommit
// can be compared byte-for-byte, not as a sorted multiset.
func coCommitKey(cc CorrelatedCommit) string {
	parts := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%v|%d|%.6f|%s",
		cc.BeadID, cc.SHA, cc.ShortSHA, cc.Message, cc.Author, cc.AuthorEmail,
		cc.Timestamp.Format(time.RFC3339Nano), cc.Method, len(cc.Files), cc.Confidence, cc.Reason)
	for _, f := range cc.Files {
		parts += fmt.Sprintf("\n  %s|%s|%d|%d", f.Action, f.Path, f.Insertions, f.Deletions)
	}
	return parts
}

func assertCoCommitsByteIdentical(t *testing.T, want, got []CorrelatedCommit, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: length mismatch full=%d incremental=%d", label, len(want), len(got))
	}
	for i := range want {
		if coCommitKey(want[i]) != coCommitKey(got[i]) {
			t.Fatalf("%s: correlated commit %d differs:\n full        = %s\n incremental = %s",
				label, i, coCommitKey(want[i]), coCommitKey(got[i]))
		}
	}
}

// statusChangeSHAs returns the distinct SHAs primeBatch would actually fetch for
// the given events (status-change events only), in first-seen order — the same
// set ExtractAllCoCommits collects.
func statusChangeSHAs(events []BeadEvent) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, ev := range events {
		if ev.EventType != EventClaimed && ev.EventType != EventClosed {
			continue
		}
		if ev.CommitSHA == "" {
			continue
		}
		if _, ok := seen[ev.CommitSHA]; ok {
			continue
		}
		seen[ev.CommitSHA] = struct{}{}
		out = append(out, ev.CommitSHA)
	}
	return out
}

// TestPerCommitCoCommitDifferential is the mandatory correctness test for the
// incremental co-commit cache: ExtractAllCoCommits must produce a byte-identical
// []CorrelatedCommit across the full (cache disabled), cold (empty disk cache),
// fully-warm (0 new SHAs → 0 git-log fetches), and k=1/3/10 new-SHA regimes. It
// also asserts the git-fetch SHA count equals the uncached count cold, is 0 fully
// warm, and is exactly k for k evicted SHAs.
func TestPerCommitCoCommitDifferential(t *testing.T) {
	root := repoRootForTest(t)

	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	e := NewExtractor(root)
	opts := ExtractOptions{}
	events, err := e.Extract(opts)
	if err != nil {
		t.Fatalf("extract events: %v", err)
	}
	shas := statusChangeSHAs(events)
	if len(shas) == 0 {
		t.Skip("repo produced no status-change co-commit SHAs; nothing to differentially test")
	}
	t.Logf("status-change SHAs: %d", len(shas))

	namespace := perCommitCoCommitCacheNamespace(root)

	run := func() ([]CorrelatedCommit, int64) {
		atomic.StoreInt64(&coCommitFetchedSHAsCounter, 0)
		cc := NewCoCommitExtractor(root)
		out, err := cc.ExtractAllCoCommits(events)
		if err != nil {
			t.Fatalf("ExtractAllCoCommits: %v", err)
		}
		return out, atomic.LoadInt64(&coCommitFetchedSHAsCounter)
	}

	// Ground truth: cache disabled, always fetches every SHA from git.
	t.Setenv("BV_NO_CACHE", "1")
	full, fullFetched := run()
	if int(fullFetched) != len(shas) {
		t.Fatalf("full (cache off): fetched %d SHAs, want %d", fullFetched, len(shas))
	}
	t.Logf("full: %d correlated commits, %d SHAs fetched", len(full), fullFetched)

	t.Setenv("BV_NO_CACHE", "")

	// COLD: empty disk cache. Must equal full and fetch every SHA.
	cold, coldFetched := run()
	assertCoCommitsByteIdentical(t, full, cold, "cold")
	if int(coldFetched) != len(shas) {
		t.Fatalf("cold: fetched %d SHAs, want %d (all uncached)", coldFetched, len(shas))
	}
	t.Logf("cold: %d commits, %d SHAs fetched", len(cold), coldFetched)

	// FULLY WARM: every SHA on disk → 0 git-log fetches.
	warm, warmFetched := run()
	assertCoCommitsByteIdentical(t, full, warm, "fully-warm")
	if warmFetched != 0 {
		t.Fatalf("fully-warm: fetched %d SHAs, want 0 (all cached)", warmFetched)
	}
	t.Logf("fully-warm: %d commits, %d SHAs fetched", len(warm), warmFetched)

	// PARTIALLY WARM: evict k SHAs, re-extract, must equal full and fetch only k.
	for _, k := range []int{1, 3, 10} {
		if k > len(shas) {
			continue
		}
		evictCoCommitSHAs(t, namespace, shas[:k])

		partial, kFetched := run()
		assertCoCommitsByteIdentical(t, full, partial, fmt.Sprintf("k=%d", k))
		if int(kFetched) != k {
			t.Fatalf("k=%d: fetched %d SHAs, want exactly %d", k, kFetched, k)
		}
		t.Logf("k=%d: %d commits, %d SHAs fetched (full would fetch %d)", k, len(partial), kFetched, len(shas))
		// The re-extraction re-stored the k SHAs, so the cache is warm again.
	}
}

// evictCoCommitSHAs deletes the given SHAs from the per-commit co-commit cache
// namespace, simulating a cache that predates those commits.
func evictCoCommitSHAs(t *testing.T, namespace string, shas []string) {
	t.Helper()
	path, err := perCommitCoCommitCachePath(true)
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

	cf := readPerCommitCoCommitCacheLocked(f)
	bucket := cf.Entries[namespace]
	for _, sha := range shas {
		delete(bucket.Commits, sha)
	}
	cf.Entries[namespace] = bucket
	if err := writePerCommitCoCommitCacheLocked(f, cf); err != nil {
		t.Fatalf("rewrite cache: %v", err)
	}
}

// TestLineStatsWireRoundTrip verifies the unexported map[string]lineStats
// survives the exported-wire serialization round-trip exactly.
func TestLineStatsWireRoundTrip(t *testing.T) {
	in := map[string]lineStats{
		"a.go":            {insertions: 12, deletions: 3},
		"pkg/b.go":        {insertions: 0, deletions: 0},
		"c with space.go": {insertions: 9999, deletions: 1},
	}
	got := fromLineStatsMap(toLineStatsMap(in))
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("lineStats round-trip mismatch:\n in  = %#v\n got = %#v", in, got)
	}
}

// TestPerCommitCoCommitNamespaceStable verifies the namespace is a stable,
// non-empty hash of the repository and fixed extraction policy.
func TestPerCommitCoCommitNamespaceStable(t *testing.T) {
	repo := t.TempDir()
	a := perCommitCoCommitCacheNamespace(repo)
	b := perCommitCoCommitCacheNamespace(repo)
	if a == "" || a != b {
		t.Fatalf("namespace not stable/non-empty: %q vs %q", a, b)
	}
	if other := perCommitCoCommitCacheNamespace(t.TempDir()); other == a {
		t.Fatalf("distinct repository paths shared namespace %q", a)
	}
}

func TestReadPerCommitCoCommitCacheRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "per-commit-cocommits.json")
	fixture := []byte(fmt.Sprintf(`{"version":%d,"entries":{"ns":{"commits":{"sha":{"created_at":"2026-08-26T12:00:00Z","files":[],"line_stats":{}}}}}}`, perCommitCoCommitCacheVersion))
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cache fixture: %v", err)
	}
	defer f.Close()

	valid := readPerCommitCoCommitCacheLockedWithLimit(f, int64(len(fixture)))
	if _, ok := valid.Entries["ns"].Commits["sha"]; !ok {
		t.Fatalf("valid cache fixture did not decode: %+v", valid)
	}
	got := readPerCommitCoCommitCacheLockedWithLimit(f, 8)
	if got.Version != perCommitCoCommitCacheVersion || len(got.Entries) != 0 {
		t.Fatalf("oversized cache should fail closed to an empty current-version cache: %+v", got)
	}
}

func TestPrunePerCommitCoCommitsRejectsFutureTimestamp(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	entries := map[string]perCommitCoCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitCoCommitEntry{
			"fresh":  {CreatedAt: now.Add(-time.Hour)},
			"future": {CreatedAt: now.Add(time.Nanosecond)},
		}},
	}
	pruneAndBoundPerCommitCoCommitEntries(now, entries)
	if _, ok := entries["ns"].Commits["fresh"]; !ok {
		t.Fatal("fresh per-commit co-commit entry was removed")
	}
	if _, ok := entries["ns"].Commits["future"]; ok {
		t.Fatal("future-dated per-commit co-commit entry was retained")
	}
}

// TestPruneAndBoundPerCommitCoCommitEntries verifies the count cap evicts
// oldest-first.
func TestPruneAndBoundPerCommitCoCommitEntries(t *testing.T) {
	now := time.Now().UTC()
	entries := map[string]perCommitCoCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitCoCommitEntry{}},
	}
	total := perCommitCoCommitCacheMaxCommits + 50
	for i := 0; i < total; i++ {
		entries["ns"].Commits[fmt.Sprintf("sha-%05d", i)] = perCommitCoCommitEntry{
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
	}
	pruneAndBoundPerCommitCoCommitEntries(now.Add(time.Hour), entries)
	got := len(entries["ns"].Commits)
	if got != perCommitCoCommitCacheMaxCommits {
		t.Fatalf("after bound: %d commits, want %d", got, perCommitCoCommitCacheMaxCommits)
	}
	keys := make([]string, 0, got)
	for k := range entries["ns"].Commits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if keys[0] < fmt.Sprintf("sha-%05d", 50) {
		t.Fatalf("oldest surviving key %q suggests wrong entries evicted", keys[0])
	}
}
