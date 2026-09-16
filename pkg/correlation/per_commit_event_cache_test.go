package correlation

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// TestPerCommitCacheNamespaceIsolation verifies filtered contributions cannot
// overwrite the full extraction's namespace. Reuse from full to filtered must
// explicitly select the requested bead's events.
func TestPerCommitCacheNamespaceIsolation(t *testing.T) {
	e := NewExtractor("/tmp/repo")
	a := perCommitEventCacheNamespace(e.primaryBeadsFile(), "")
	b := perCommitEventCacheNamespace(e.primaryBeadsFile(), "bv-123")
	if a == b {
		t.Fatalf("namespaces collide for different BeadID filters: %q", a)
	}
}

func TestPerCommitCacheFilteredReuse(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Setenv("BV_ROBOT", "1")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the actual dispatcher on the snapshot path. Padding belongs to an
	// unrelated record and is not part of the extracted event payload.
	padding := strings.Repeat("x", snapshotBlobSizeThreshold)
	steps := []struct {
		target, other, deps, malformed string
	}{
		{"open", "open", "[]", ""},
		{"in_progress", "open", `[{"depends_on_id":"other","type":"blocks"}]`, ""},
		{"in_progress", "closed", `[{"depends_on_id":"other","type":"blocks"}]`, ""},
		{"closed", "closed", `[{"depends_on_id":"other","type":"blocks"}]`, "{\"broken\":\n"},
		{"open", "closed", "[]", "{\"broken\":\n"},
	}
	for i, step := range steps {
		content := fmt.Sprintf("{\"id\":\"target\",\"title\":\"Target\",\"status\":%q,\"dependencies\":%s}\n{\"id\":\"other\",\"title\":\"Other\",\"status\":%q}\n{\"id\":\"padding\",\"status\":\"open\",\"description\":%q}\n%s", step.target, step.deps, step.other, padding, step.malformed)
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", ".beads/issues.jsonl")
		runGit(t, repo, "commit", "-m", fmt.Sprintf("lifecycle step %d", i))
	}
	e := NewExtractor(repo)
	t.Setenv("BV_NO_CACHE", "1")
	oracles := make(map[string][]BeadEvent)
	for _, filter := range []string{"", "target", "absent"} {
		var err error
		oracles[filter], err = e.Extract(ExtractOptions{BeadID: filter})
		if err != nil {
			t.Fatalf("uncached filter=%q: %v", filter, err)
		}
	}
	var eventTypes []EventType
	for _, event := range oracles["target"] {
		eventTypes = append(eventTypes, event.EventType)
	}
	if !reflect.DeepEqual(eventTypes, []EventType{EventCreated, EventClaimed, EventClosed, EventReopened}) {
		t.Fatalf("fixture lifecycle=%v", eventTypes)
	}
	closed := oracles["target"][2]
	if closed.TransitionObserved || closed.Before == nil || closed.After == nil ||
		closed.Before.Status != "in_progress" || closed.After.Status != "closed" ||
		len(closed.After.Dependencies) != 1 || closed.After.Dependencies[0].DependsOnID != "other" {
		t.Fatalf("fixture must retain before/after/dependency evidence and unrelated malformed-record incompleteness: %+v", closed)
	}
	if len(oracles[""]) <= len(oracles["target"]) || len(oracles["absent"]) != 0 {
		t.Fatal("fixture must distinguish full, filtered, and empty results")
	}
	for _, tc := range []struct {
		name, warmFilter, filter, corrupt string
		wantZeroBlobs                     bool
	}{
		{"full to target", "", "target", "", true},
		{"full to absent", "", "absent", "", true},
		{"expired full entry", "", "target", "expired", false},
		{"future full entry", "", "target", "future", false},
		{"zero timestamp", "", "target", "zero", false},
		{"wrong parent blob", "", "target", "old_oid", false},
		{"wrong child blob", "", "target", "new_oid", false},
		{"different source file", "", "target", "source", false},
		{"filtered cannot fill full", "target", "", "", false},
		{"truncated full cache", "", "", "truncated", false},
		{"empty full cache", "", "", "empty", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BV_CACHE_DIR", t.TempDir())
			t.Setenv("BV_NO_CACHE", "")
			if _, err := e.Extract(ExtractOptions{BeadID: tc.warmFilter}); err != nil {
				t.Fatalf("populate actual cache: %v", err)
			}
			path, err := perCommitEventCachePath(false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.corrupt != "" {
				f, err := os.OpenFile(path, os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				cf := readPerCommitEventCacheLocked(f)
				namespace := perCommitEventCacheNamespace(e.primaryBeadsFile(), "")
				bucket := cf.Entries[namespace]
				if len(bucket.Commits) != len(steps) {
					_ = f.Close()
					t.Fatalf("actual cache has %d commits, want %d", len(bucket.Commits), len(steps))
				}
				for sha, entry := range bucket.Commits {
					switch tc.corrupt {
					case "expired":
						entry.CreatedAt = time.Now().Add(-perCommitEventCacheMaxAge - time.Hour)
					case "future":
						entry.CreatedAt = time.Now().Add(time.Hour)
					case "zero":
						entry.CreatedAt = time.Time{}
					case "old_oid":
						entry.OldSHA = strings.Repeat("f", 40)
					case "new_oid":
						entry.NewSHA = strings.Repeat("f", 40)
					}
					bucket.Commits[sha] = entry
				}
				if tc.corrupt == "source" {
					cf.Entries = map[string]perCommitNamespaceBucket{perCommitEventCacheNamespace(".beads/different.jsonl", ""): bucket}
				}
				switch tc.corrupt {
				case "truncated", "empty":
					// Simulate an interrupted cache rewrite using an actual
					// previously populated file, not fabricated event data.
					info, statErr := f.Stat()
					if statErr != nil {
						_ = f.Close()
						t.Fatal(statErr)
					}
					length := info.Size() / 2
					if tc.corrupt == "empty" {
						length = 0
					}
					err = f.Truncate(length)
				default:
					err = writePerCommitEventCacheLocked(f, cf)
				}
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("plant cache negative: write=%v close=%v", err, closeErr)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			atomic.StoreInt64(&blobsReadCounter, 0)
			got, err := e.Extract(ExtractOptions{BeadID: tc.filter})
			reads := atomic.LoadInt64(&blobsReadCounter)
			if err != nil {
				t.Fatal(err)
			}
			// Compare EVERY field, including Before/After and TransitionObserved;
			// the older eventKey helper intentionally does not cover those fields.
			if !reflect.DeepEqual(got, oracles[tc.filter]) {
				t.Fatalf("filtered extraction changed event contents/order: got=%+v want=%+v", got, oracles[tc.filter])
			}
			t.Logf("filter=%q warm_filter=%q negative=%q events=%d blobs_read=%d want_zero=%v", tc.filter, tc.warmFilter, tc.corrupt, len(got), reads, tc.wantZeroBlobs)
			if (reads == 0) != tc.wantZeroBlobs {
				t.Fatalf("blob reads=%d, want_zero=%v", reads, tc.wantZeroBlobs)
			}
			if tc.wantZeroBlobs {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("pure full-cache hit rewrote the persisted cache")
				}
			}
			if tc.corrupt == "truncated" || tc.corrupt == "empty" {
				atomic.StoreInt64(&blobsReadCounter, 0)
				recovered, err := e.Extract(ExtractOptions{BeadID: tc.filter})
				if err != nil {
					t.Fatalf("read rebuilt cache: %v", err)
				}
				if !reflect.DeepEqual(recovered, oracles[tc.filter]) {
					t.Fatal("rebuilt cache changed event contents/order")
				}
				if reads := atomic.LoadInt64(&blobsReadCounter); reads != 0 {
					t.Fatalf("rebuilt cache read %d blobs, want 0", reads)
				}
			}
		})
	}
}

func TestPerCommitEventCacheWriter(t *testing.T) {
	cache := perCommitEventCacheFile{
		Version: perCommitEventCacheVersion,
		Entries: map[string]perCommitNamespaceBucket{
			"namespace": {Commits: map[string]perCommitEventEntry{
				"commit": {
					CreatedAt: time.Now().UTC(), OldSHA: "parent", NewSHA: "child",
					Events: []BeadEvent{{BeadID: "target", EventType: EventClosed}},
				},
			}},
		},
	}
	for _, state := range []string{"writable", "read-only", "closed"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.json")
			original := []byte("existing cache contents")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			mode := os.O_RDWR
			if state == "read-only" {
				mode = os.O_RDONLY
			}
			f, err := os.OpenFile(path, mode, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if state == "closed" {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				defer f.Close()
			}
			err = writePerCommitEventCacheLocked(f, cache)
			if state == "writable" {
				if err != nil {
					t.Fatalf("write: %v", err)
				}
				if got := readPerCommitEventCacheLocked(f); !reflect.DeepEqual(got, cache) {
					t.Fatalf("cache round trip changed contents: got=%+v want=%+v", got, cache)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s descriptor must return a write error", state)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("%s descriptor changed file despite failed write", state)
			}
		})
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
	base := now.Add(-time.Duration(total) * time.Second)
	for i := 0; i < total; i++ {
		entries["ns"].Commits[fmt.Sprintf("sha-%05d", i)] = perCommitEventEntry{
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	pruneAndBoundPerCommitEntries(now, entries)
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

func TestPruneAndBoundPerCommitEntriesRejectsInvalidFreshness(t *testing.T) {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	entries := map[string]perCommitNamespaceBucket{
		"ns": {Commits: map[string]perCommitEventEntry{
			"fresh":  {CreatedAt: now.Add(-time.Hour)},
			"future": {CreatedAt: now.Add(time.Nanosecond)},
			"stale":  {CreatedAt: now.Add(-perCommitEventCacheMaxAge - time.Nanosecond)},
			"zero":   {},
		}},
	}

	pruneAndBoundPerCommitEntries(now, entries)

	bucket, ok := entries["ns"]
	if !ok || len(bucket.Commits) != 1 {
		t.Fatalf("prune retained %+v, want only fresh entry", entries)
	}
	if _, ok := bucket.Commits["fresh"]; !ok {
		t.Fatal("prune removed fresh entry")
	}
}

func TestLoadPerCommitEventsRejectsInvalidFreshness(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	now := time.Now().UTC()
	namespace := "freshness"
	path, err := perCommitEventCachePath(true)
	if err != nil {
		t.Fatalf("cache path: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	cacheFile := perCommitEventCacheFile{
		Version: perCommitEventCacheVersion,
		Entries: map[string]perCommitNamespaceBucket{
			namespace: {Commits: map[string]perCommitEventEntry{
				"fresh":  {CreatedAt: now.Add(-time.Hour)},
				"future": {CreatedAt: now.Add(time.Hour)},
				"stale":  {CreatedAt: now.Add(-perCommitEventCacheMaxAge - time.Hour)},
			}},
		},
	}
	if err := writePerCommitEventCacheLocked(f, cacheFile); err != nil {
		_ = f.Close()
		t.Fatalf("write cache: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close cache: %v", err)
	}

	loaded := loadPerCommitEvents(namespace)
	if len(loaded) != 1 {
		t.Fatalf("load returned %+v, want only fresh entry", loaded)
	}
	if _, ok := loaded["fresh"]; !ok {
		t.Fatal("load removed fresh entry")
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
