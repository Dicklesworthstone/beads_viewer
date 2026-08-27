package correlation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// Persistent, content-addressed per-commit cache for the snapshot extraction
// (extractViaSnapshots). This is the INCREMENTAL layer beneath the HEAD-keyed
// artifact cache (head_artifact_cache.go):
//
//   - The HEAD-artifact cache is keyed on the whole HEAD SHA + options. A single
//     new commit changes HEAD and misses it entirely, forcing a full re-extract
//     that re-reads ALL ~200 historical blobs (232MB) even though only the new
//     commit's contribution is actually new.
//
//   - This layer caches each individual commit's []BeadEvent contribution, keyed
//     by the commit's IMMUTABLE SHA. A commit's events are a pure function of the
//     followed file's (parent blob OID, child blob OID) pair plus the commit
//     metadata (author/timestamp/message) and the BeadID filter — all of which
//     are permanently fixed once the commit exists (see the correctness note
//     below). So a commit's events NEVER change; they are stored accumulating and
//     reused forever. When HEAD advances by k commits, only those k commits are
//     uncached: we read only their blobs (not 232MB) and diff only them.
//
// CORRECTNESS — a commit's events depend ONLY on its own (parent,child) blob
// pair:
//
//	In extractViaSnapshots each commit C emits
//	    parseDiff(synthesizeRecordDiff(set(C.oldSHA), set(C.newSHA)), C.info, BeadID)
//	parseDiff is a pure function of (diffText, C.info, BeadID). The diffText is the
//	record-line set difference between exactly two blobs, C.oldSHA and C.newSHA,
//	which snapshotCommits already yields per commit (git's own --follow picks the
//	correct parent blob). No cross-snapshot state is carried between commits — the
//	loop holds no accumulator that survives an iteration. Therefore C's events are
//	fully determined by (C.SHA, C.oldSHA, C.newSHA, C.info, BeadID), and since
//	C.info is itself immutably derived from C.SHA, keying on C.SHA is sound. We
//	additionally store (oldSHA,newSHA) and re-validate them against the live
//	snapshotCommit on every hit, so even a hypothetical rename-following anomaly
//	that changed the followed blob pair for an existing SHA falls back to a miss
//	rather than serving stale events.
//
// Storage discipline mirrors disk_cache.go / head_artifact_cache.go exactly:
// same XDG cache dir (BV_CACHE_DIR override, else UserCacheDir under "bv"), goccy
// JSON codec, flock, age bound, and the pass-1 no-rewrite-on-pure-hit rule. The
// per-commit map is bounded by entry count (oldest CreatedAt evicted first) and a
// serialized-size ceiling so it cannot grow without bound.

const (
	// Version 2 invalidates entries produced before parseDiff stopped silently
	// truncating JSONL records larger than 10 MiB.
	perCommitEventCacheVersion     = 2
	perCommitEventCacheFileName    = "correlation_per_commit_event_cache.json"
	perCommitEventCacheMaxAge      = 30 * 24 * time.Hour // commits are immutable; keep a month
	perCommitEventCacheMaxCommits  = 4000                // bound the accumulating commit map
	perCommitEventCacheMaxFileSize = 96 << 20            // 96MB serialized ceiling
)

// perCommitEventCacheFile is the on-disk form. Entries is keyed by a namespace
// (BeadID + primary beads file) so a BeadID-filtered extraction does not collide
// with an unfiltered one (parseDiff's output depends on the filter).
type perCommitEventCacheFile struct {
	Version int                                 `json:"version"`
	Entries map[string]perCommitNamespaceBucket `json:"entries"`
}

// perCommitNamespaceBucket holds the per-commit events for one (BeadID, file)
// namespace, keyed by the immutable commit SHA.
type perCommitNamespaceBucket struct {
	Commits map[string]perCommitEventEntry `json:"commits"`
}

// perCommitEventEntry is one commit's cached contribution. OldSHA/NewSHA are the
// followed file's parent/child blob OIDs this entry was computed from; they are
// re-validated on every hit. Events may be empty (a commit that touched the file
// but produced no lifecycle events still caches as a definitive empty result, so
// it is never re-read).
type perCommitEventEntry struct {
	CreatedAt time.Time   `json:"created_at"`
	OldSHA    string      `json:"old_sha"`
	NewSHA    string      `json:"new_sha"`
	Events    []BeadEvent `json:"events"`
}

// perCommitEventCacheNamespace derives the bucket key for a snapshot extraction.
// Two extractions share cached per-commit events iff they filter on the same
// BeadID, follow the same primary file, and use the same pinned lifecycle Git
// policy. The policy controls the commit metadata bytes stored in BeadEvent;
// binding it here prevents a future policy change from reusing old messages.
func perCommitEventCacheNamespace(primaryFile, beadID string) string {
	inputs := lifecycleGitPolicyNamespaceInputs()
	inputs = append(inputs, primaryFile, beadID)
	return strings.Join(inputs, "\x00")
}

func perCommitEventCachePath(create bool) (string, error) {
	base := os.Getenv("BV_CACHE_DIR")
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(dir, correlationDiskCacheDirName)
	}
	if create {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, perCommitEventCacheFileName), nil
}

func readPerCommitEventCacheLocked(f *os.File) perCommitEventCacheFile {
	return readPerCommitEventCacheLockedWithLimit(f, perCommitEventCacheMaxFileSize)
}

func readPerCommitEventCacheLockedWithLimit(f *os.File, maxBytes int64) perCommitEventCacheFile {
	empty := perCommitEventCacheFile{Version: perCommitEventCacheVersion, Entries: map[string]perCommitNamespaceBucket{}}
	if _, err := f.Seek(0, 0); err != nil {
		return empty
	}
	data, ok := readCacheFileBounded(f, maxBytes)
	if !ok || len(data) == 0 {
		return empty
	}
	var cf perCommitEventCacheFile
	if err := json.Unmarshal(data, &cf); err != nil || cf.Version != perCommitEventCacheVersion {
		return empty
	}
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitNamespaceBucket{}
	}
	return cf
}

func writePerCommitEventCacheLocked(f *os.File, cf perCommitEventCacheFile) error {
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitNamespaceBucket{}
	}
	// Marshal BEFORE truncating, evicting the oldest commits until the encoded
	// file fits under the byte ceiling. Earlier this truncated first and, on
	// overflow, wrote an EMPTY file — wiping every namespace and forcing a full
	// (232MB) re-extraction, defeating the incremental cache. Evicting oldest-first
	// keeps the most recently-useful entries; only a truly pathological single
	// entry (>ceiling, impossible at ~24KB/commit) would empty it.
	for {
		data, err := json.Marshal(cf)
		if err != nil {
			return err
		}
		if len(data) <= perCommitEventCacheMaxFileSize || !evictOldestPerCommitEvents(cf.Entries) {
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.Seek(0, 0); err != nil {
				return err
			}
			if _, err := f.Write(data); err != nil {
				return err
			}
			return f.Sync()
		}
	}
}

// evictOldestPerCommitEvents drops the oldest ~10% of commits (at least one)
// across all namespaces, oldest-CreatedAt first with a deterministic (ns,sha)
// tie-break. Returns false when there is nothing left to evict.
func evictOldestPerCommitEvents(entries map[string]perCommitNamespaceBucket) bool {
	type item struct {
		ns, sha string
		t       time.Time
	}
	var all []item
	for ns, bucket := range entries {
		for sha, e := range bucket.Commits {
			all = append(all, item{ns: ns, sha: sha, t: e.CreatedAt})
		}
	}
	if len(all) == 0 {
		return false
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].t.Equal(all[j].t) {
			if all[i].ns == all[j].ns {
				return all[i].sha < all[j].sha
			}
			return all[i].ns < all[j].ns
		}
		return all[i].t.Before(all[j].t)
	})
	drop := len(all) / 10
	if drop < 1 {
		drop = 1
	}
	for i := 0; i < drop && i < len(all); i++ {
		it := all[i]
		if b, ok := entries[it.ns]; ok {
			delete(b.Commits, it.sha)
			if len(b.Commits) == 0 {
				delete(entries, it.ns)
			}
		}
	}
	return true
}

// pruneAndBoundPerCommitEntries drops aged entries (across all namespaces) and,
// if the total commit count still exceeds the cap, evicts the oldest commits
// (by CreatedAt) first. Operates in place on cf.Entries.
func pruneAndBoundPerCommitEntries(now time.Time, entries map[string]perCommitNamespaceBucket) {
	type item struct {
		ns  string
		sha string
		t   time.Time
	}
	var all []item
	for ns, bucket := range entries {
		for sha, e := range bucket.Commits {
			if !cacheCreatedAtIsFresh(e.CreatedAt, now, perCommitEventCacheMaxAge) {
				delete(bucket.Commits, sha)
				continue
			}
			all = append(all, item{ns: ns, sha: sha, t: e.CreatedAt})
		}
		if len(bucket.Commits) == 0 {
			delete(entries, ns)
		}
	}
	if len(all) <= perCommitEventCacheMaxCommits {
		return
	}
	// Evict oldest-first until under the cap. Deterministic tie-break on (ns,sha).
	sort.Slice(all, func(i, j int) bool {
		if all[i].t.Equal(all[j].t) {
			if all[i].ns == all[j].ns {
				return all[i].sha < all[j].sha
			}
			return all[i].ns < all[j].ns
		}
		return all[i].t.Before(all[j].t)
	})
	excess := len(all) - perCommitEventCacheMaxCommits
	for i := 0; i < excess; i++ {
		it := all[i]
		if b, ok := entries[it.ns]; ok {
			delete(b.Commits, it.sha)
			if len(b.Commits) == 0 {
				delete(entries, it.ns)
			}
		}
	}
}

// loadPerCommitEvents returns the cached per-commit events for the given
// namespace as a map keyed by commit SHA. A miss (cache disabled, absent,
// corrupt, version-mismatched) returns an empty map so the caller transparently
// treats every commit as new. This is a pure read: it never rewrites the file.
func loadPerCommitEvents(namespace string) map[string]perCommitEventEntry {
	if !correlationDiskCacheEnabled() {
		return nil
	}
	path, err := perCommitEventCachePath(false)
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return nil
	}
	defer func() { _ = unlockFile(f) }()

	cf := readPerCommitEventCacheLocked(f)
	bucket, ok := cf.Entries[namespace]
	if !ok {
		return nil
	}
	now := time.Now()
	// Filter aged entries out of the returned view without rewriting the file.
	out := make(map[string]perCommitEventEntry, len(bucket.Commits))
	for sha, e := range bucket.Commits {
		if !cacheCreatedAtIsFresh(e.CreatedAt, now, perCommitEventCacheMaxAge) {
			continue
		}
		out[sha] = e
	}
	return out
}

// storePerCommitEvents merges freshly computed per-commit entries into the cache
// for the given namespace. Runs only after a real extraction read blobs for the
// uncached commits, so the rewrite is amortized against the I/O it lets future
// invocations skip. Entries already present (pure hits) are NOT re-passed by the
// caller, preserving the no-rewrite-on-pure-hit discipline when nothing is new.
func storePerCommitEvents(namespace string, fresh map[string]perCommitEventEntry) {
	if !correlationDiskCacheEnabled() || len(fresh) == 0 {
		return
	}
	path, err := perCommitEventCachePath(true)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return
	}
	defer func() { _ = unlockFile(f) }()

	cf := readPerCommitEventCacheLocked(f)
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitNamespaceBucket{}
	}
	bucket, ok := cf.Entries[namespace]
	if !ok || bucket.Commits == nil {
		bucket = perCommitNamespaceBucket{Commits: map[string]perCommitEventEntry{}}
	}
	for sha, e := range fresh {
		bucket.Commits[sha] = e
	}
	cf.Entries[namespace] = bucket

	pruneAndBoundPerCommitEntries(time.Now().UTC(), cf.Entries)
	_ = writePerCommitEventCacheLocked(f, cf)
}
