package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// Persistent, content-addressed per-commit cache for the co-commit extraction
// (primeBatch). This is the INCREMENTAL layer for the co-commit data, the exact
// mirror of per_commit_event_cache.go but for the diff (name-status + numstat)
// rather than the bead lifecycle events:
//
//   - primeBatch fetches each status-change commit's (file list, line stats) via
//     two batched `git log` invocations over EVERY requested SHA. Process-local
//     memoization (fileCache/statCache/batchedSHAs) avoids re-forking within one
//     process, but a fresh `bv --robot-triage` after a single new commit re-runs
//     the batched `git log` over ALL ~500 historical SHAs even though only the
//     new commit's co-commit data is actually new.
//
//   - This layer caches each individual commit's (Files, LineStats) contribution,
//     keyed by the commit's immutable SHA inside a repository-and-policy
//     namespace. The extractor pins every Git option that affects the records,
//     strips ambient repository-routing variables, and sorts Files before
//     persistence. When HEAD advances by k commits, only those k commits are
//     uncached: the batched `git log` runs over just the k missing SHAs, not all
//     ~500.
//
// CORRECTNESS — a cached contribution is bound to repository, SHA, policy, and
// exclude pathspec:
//
//	primeBatch computes, for each SHA, files[sha] = parseNameStatus(<the SHA's
//	first-parent name-status diff under excludePathspecArgs()>) and stats[sha] =
//	parseNumstat(<...numstat...>). Both git diffs are pure functions of the commit
//	object under coCommitGitConfigArgs/coCommitDiffArgs and the exclude pathspec
//	set. No cross-commit state is carried. The namespace also binds an absolute
//	repository path so clone-local state cannot cross-contaminate another clone.
//	Shallow repositories bypass this persistent layer because deepening changes a
//	boundary commit's effective parent without changing its SHA. Any policy or
//	excludedPaths change lands in a different bucket; the cache-file version
//	separately rejects entries created before this binding existed.
//
// Storage discipline mirrors per_commit_event_cache.go / disk_cache.go exactly:
// same XDG cache dir (BV_CACHE_DIR override, else UserCacheDir under "bv"), goccy
// JSON codec, flock, age bound, and the no-rewrite-on-pure-hit rule (only freshly
// fetched SHAs are persisted). The per-commit map is bounded by entry count
// (oldest CreatedAt evicted first) and a serialized-size ceiling.
//
// lineStats is an UNEXPORTED struct; goccy cannot serialize its fields. The
// on-disk form uses lineStatsWire (exported mirror) and round-trips through the
// conversion helpers below so the in-memory map[string]lineStats restored from
// disk is byte-identical to one freshly parsed from git.

const (
	perCommitCoCommitCacheVersion     = 2
	perCommitCoCommitCacheFileName    = "correlation_per_commit_cocommit_cache.json"
	perCommitCoCommitCacheMaxAge      = 30 * 24 * time.Hour // commits are immutable; keep a month
	perCommitCoCommitCacheMaxCommits  = 4000                // bound the accumulating commit map
	perCommitCoCommitCacheMaxFileSize = 96 << 20            // 96MB serialized ceiling
)

// lineStatsWire is the exported on-disk mirror of the unexported lineStats so
// goccy can (de)serialize the +/- counts. Round-tripping it reconstructs an
// identical lineStats (see toLineStatsMap / fromLineStatsMap).
type lineStatsWire struct {
	Insertions int `json:"i"`
	Deletions  int `json:"d"`
}

// perCommitCoCommitCacheFile is the on-disk form. Entries is keyed by a namespace
// (a hash of the exclude-pathspec set + schema) so an exclude-list change cannot
// serve stale data.
type perCommitCoCommitCacheFile struct {
	Version int                                         `json:"version"`
	Entries map[string]perCommitCoCommitNamespaceBucket `json:"entries"`
}

// perCommitCoCommitNamespaceBucket holds the per-commit co-commit data for one
// exclude-pathspec namespace, keyed by the immutable commit SHA.
type perCommitCoCommitNamespaceBucket struct {
	Commits map[string]perCommitCoCommitEntry `json:"commits"`
}

// perCommitCoCommitEntry is one commit's cached co-commit contribution. Files is
// the name-status list in stable path/action order; LineStats is the per-path
// numstat map.
// Either may be empty (a commit whose diff is empty under the exclude pathspec
// caches as a definitive empty result, so it is never re-fetched) — matching
// primeBatch's memoize-empty-on-absence behavior.
type perCommitCoCommitEntry struct {
	CreatedAt time.Time                `json:"created_at"`
	Files     []FileChange             `json:"files"`
	LineStats map[string]lineStatsWire `json:"line_stats"`
}

// perCommitCoCommitCacheNamespace derives the bucket key for a co-commit
// extraction. The namespace binds the repository location, the complete fixed
// Git diff policy, and the exclude pathspec set. Repository binding prevents a
// clone-local state from contaminating another clone that happens to contain
// the same commit object. The extractor separately disables persistent access
// while this repository is shallow. Policy/pathspec changes get a fresh
// namespace, while the file-version bump rejects pre-policy caches.
func perCommitCoCommitCacheNamespace(repoPath string) string {
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		absRepoPath = filepath.Clean(repoPath)
	}
	inputs := []string{filepath.Clean(absRepoPath)}
	inputs = append(inputs, coCommitGitPolicyNamespaceInputs()...)
	h := sha256.Sum256([]byte(strings.Join(inputs, "\x00")))
	return hex.EncodeToString(h[:])
}

func perCommitCoCommitCachePath(create bool) (string, error) {
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
	return filepath.Join(base, perCommitCoCommitCacheFileName), nil
}

// toLineStatsMap converts a freshly parsed map[string]lineStats into the exported
// wire form for serialization.
func toLineStatsMap(in map[string]lineStats) map[string]lineStatsWire {
	out := make(map[string]lineStatsWire, len(in))
	for k, v := range in {
		out[k] = lineStatsWire{Insertions: v.insertions, Deletions: v.deletions}
	}
	return out
}

// fromLineStatsMap reconstructs the unexported map[string]lineStats from the wire
// form. The result is identical to one parseNumstat would produce.
func fromLineStatsMap(in map[string]lineStatsWire) map[string]lineStats {
	out := make(map[string]lineStats, len(in))
	for k, v := range in {
		out[k] = lineStats{insertions: v.Insertions, deletions: v.Deletions}
	}
	return out
}

func readPerCommitCoCommitCacheLocked(f *os.File) perCommitCoCommitCacheFile {
	return readPerCommitCoCommitCacheLockedWithLimit(f, perCommitCoCommitCacheMaxFileSize)
}

func readPerCommitCoCommitCacheLockedWithLimit(f *os.File, maxBytes int64) perCommitCoCommitCacheFile {
	empty := perCommitCoCommitCacheFile{Version: perCommitCoCommitCacheVersion, Entries: map[string]perCommitCoCommitNamespaceBucket{}}
	if _, err := f.Seek(0, 0); err != nil {
		return empty
	}
	data, ok := readCacheFileBounded(f, maxBytes)
	if !ok || len(data) == 0 {
		return empty
	}
	var cf perCommitCoCommitCacheFile
	if err := json.Unmarshal(data, &cf); err != nil || cf.Version != perCommitCoCommitCacheVersion {
		return empty
	}
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitCoCommitNamespaceBucket{}
	}
	return cf
}

func writePerCommitCoCommitCacheLocked(f *os.File, cf perCommitCoCommitCacheFile) error {
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitCoCommitNamespaceBucket{}
	}
	// Marshal BEFORE truncating and evict the oldest commits until the file fits,
	// rather than wiping every namespace on overflow (which would force a full
	// co-commit re-extraction). See evictOldestPerCommitCoCommit / the matching
	// per_commit_event_cache.go rationale.
	for {
		data, err := json.Marshal(cf)
		if err != nil {
			return err
		}
		if len(data) <= perCommitCoCommitCacheMaxFileSize || !evictOldestPerCommitCoCommit(cf.Entries) {
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

// evictOldestPerCommitCoCommit drops the oldest ~10% of commits (at least one)
// across all namespaces, oldest-CreatedAt first with a deterministic (ns,sha)
// tie-break. Returns false when there is nothing left to evict.
func evictOldestPerCommitCoCommit(entries map[string]perCommitCoCommitNamespaceBucket) bool {
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

// pruneAndBoundPerCommitCoCommitEntries drops aged entries (across all
// namespaces) and, if the total commit count still exceeds the cap, evicts the
// oldest commits (by CreatedAt) first. Operates in place on cf.Entries.
func pruneAndBoundPerCommitCoCommitEntries(now time.Time, entries map[string]perCommitCoCommitNamespaceBucket) {
	type item struct {
		ns  string
		sha string
		t   time.Time
	}
	var all []item
	for ns, bucket := range entries {
		for sha, e := range bucket.Commits {
			if !cacheCreatedAtIsFresh(e.CreatedAt, now, perCommitCoCommitCacheMaxAge) {
				delete(bucket.Commits, sha)
				continue
			}
			all = append(all, item{ns: ns, sha: sha, t: e.CreatedAt})
		}
		if len(bucket.Commits) == 0 {
			delete(entries, ns)
		}
	}
	if len(all) <= perCommitCoCommitCacheMaxCommits {
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
	excess := len(all) - perCommitCoCommitCacheMaxCommits
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

// loadPerCommitCoCommit returns the cached per-commit co-commit entries for the
// given namespace as a map keyed by commit SHA. A miss (cache disabled, absent,
// corrupt, version-mismatched) returns nil so the caller transparently treats
// every commit as new. This is a pure read: it never rewrites the file.
func loadPerCommitCoCommit(namespace string) map[string]perCommitCoCommitEntry {
	if !correlationDiskCacheEnabled() {
		return nil
	}
	path, err := perCommitCoCommitCachePath(false)
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

	cf := readPerCommitCoCommitCacheLocked(f)
	bucket, ok := cf.Entries[namespace]
	if !ok {
		return nil
	}
	now := time.Now()
	// Filter aged entries out of the returned view without rewriting the file.
	out := make(map[string]perCommitCoCommitEntry, len(bucket.Commits))
	for sha, e := range bucket.Commits {
		if !cacheCreatedAtIsFresh(e.CreatedAt, now, perCommitCoCommitCacheMaxAge) {
			continue
		}
		out[sha] = e
	}
	return out
}

// storePerCommitCoCommit merges freshly computed per-commit entries into the
// cache for the given namespace. Runs only after a real batched `git log` fetched
// the uncached commits, so the rewrite is amortized against the git I/O it lets
// future invocations skip. Entries already present (pure hits) are NOT re-passed
// by the caller, preserving the no-rewrite-on-pure-hit discipline when nothing is
// new.
func storePerCommitCoCommit(namespace string, fresh map[string]perCommitCoCommitEntry) {
	if !correlationDiskCacheEnabled() || len(fresh) == 0 {
		return
	}
	path, err := perCommitCoCommitCachePath(true)
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

	cf := readPerCommitCoCommitCacheLocked(f)
	if cf.Entries == nil {
		cf.Entries = map[string]perCommitCoCommitNamespaceBucket{}
	}
	bucket, ok := cf.Entries[namespace]
	if !ok || bucket.Commits == nil {
		bucket = perCommitCoCommitNamespaceBucket{Commits: map[string]perCommitCoCommitEntry{}}
	}
	for sha, e := range fresh {
		bucket.Commits[sha] = e
	}
	cf.Entries[namespace] = bucket

	pruneAndBoundPerCommitCoCommitEntries(time.Now().UTC(), cf.Entries)
	_ = writePerCommitCoCommitCacheLocked(f, cf)
}
