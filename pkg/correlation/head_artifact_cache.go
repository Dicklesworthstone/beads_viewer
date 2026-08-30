package correlation

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	json "github.com/goccy/go-json"
)

// Persistent on-disk cache for the HEAD-only historyArtifact (extracted
// lifecycle events + co-commit correlations). This is the inner of the two
// correlation cache layers introduced for the `br update X; bv --robot-triage`
// agent loop:
//
//   - The OUTER layer (disk_cache.go) caches the fully assembled HistoryReport,
//     keyed on repository/primary-history namespace + HEAD SHA +
//     hashBeads(beads) + options. It is the fast path when NOTHING changed
//     between invocations.
//
//   - This INNER layer caches only the expensive, purely-history-derived
//     artifact, keyed on repository/primary-history namespace + HEAD SHA +
//     options + schema version — NOT hashBeads.
//     A working-tree bead edit (`br update`/`br close`, even uncommitted) flips
//     hashBeads and misses the outer report cache, but does NOT touch committed
//     history, so the artifact is unchanged. On that path we load the cached
//     artifact here (cheap) and re-run only the cheap report assembly against
//     the current beads, skipping the 232MB git-blob extraction entirely.
//
// Correctness: Extract reads only committed history and ExtractAllCoCommits is a
// pure function of its events, so the artifact depends solely on the repository,
// selected history path, HEAD, and extract options (see historyArtifact /
// extractHistoryArtifact). A genuine HEAD change (a new commit) changes the SHA
// and invalidates the entry. The schema version bumps invalidate stale entries
// on format change.
//
// Storage discipline mirrors disk_cache.go exactly: same XDG cache dir
// convention (BV_CACHE_DIR override, else UserCacheDir under "bv"), goccy JSON
// codec, flock, age/size/LRU bounds, and the pass-1 no-rewrite-on-read-hit rule
// (a pure read does not rewrite the file just to bump AccessedAt).

const (
	headArtifactCacheVersion            = 2
	headArtifactCacheFileName           = "correlation_head_artifact_cache.json"
	headArtifactCacheMaxEntries         = 6
	headArtifactCacheMaxAge             = 24 * time.Hour
	headArtifactCacheMaxEntrySize       = 64 << 20 // 64MB serialized artifact ceiling
	headArtifactCacheMaxFileSize  int64 = headArtifactCacheMaxEntries*headArtifactCacheMaxEntrySize + (1 << 20)
)

type headArtifactCacheFile struct {
	Version int                               `json:"version"`
	Entries map[string]headArtifactCacheEntry `json:"entries"`
}

type headArtifactCacheEntry struct {
	CreatedAt  time.Time        `json:"created_at"`
	AccessedAt time.Time        `json:"accessed_at"`
	Namespace  string           `json:"namespace"`
	HeadSHA    string           `json:"head_sha"`
	OptsHash   string           `json:"opts_hash"`
	Artifact   *historyArtifact `json:"artifact"`
}

// headArtifactCachePath resolves the cache file location, honoring the same
// conventions as the report disk cache (BV_CACHE_DIR override, otherwise the
// user cache dir which respects XDG_CACHE_HOME, under the shared "bv" subdir).
func headArtifactCachePath(create bool) (string, error) {
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
	return filepath.Join(base, headArtifactCacheFileName), nil
}

// headArtifactCacheKey deliberately excludes hashBeads so working-tree bead
// edits reuse committed history, but includes the repository/primary-path
// namespace because those select which committed history is extracted.
func headArtifactCacheKey(namespace, headSHA, optsHash string) string {
	return namespace + ":" + headSHA + ":" + optsHash
}

func readHeadArtifactCacheLocked(f *os.File) headArtifactCacheFile {
	empty := headArtifactCacheFile{Version: headArtifactCacheVersion, Entries: map[string]headArtifactCacheEntry{}}
	if _, err := f.Seek(0, 0); err != nil {
		return empty
	}
	data, ok := readCacheFileBounded(f, headArtifactCacheMaxFileSize)
	if !ok || len(data) == 0 {
		return empty
	}
	var cf headArtifactCacheFile
	if err := json.Unmarshal(data, &cf); err != nil || cf.Version != headArtifactCacheVersion {
		return empty
	}
	if cf.Entries == nil {
		cf.Entries = map[string]headArtifactCacheEntry{}
	}
	return cf
}

func writeHeadArtifactCacheLocked(f *os.File, cf headArtifactCacheFile) error {
	if cf.Entries == nil {
		cf.Entries = map[string]headArtifactCacheEntry{}
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func pruneHeadArtifactCacheEntries(now time.Time, entries map[string]headArtifactCacheEntry) {
	for k, e := range entries {
		if !cacheCreatedAtIsFresh(e.CreatedAt, now, headArtifactCacheMaxAge) {
			delete(entries, k)
		}
	}
}

func evictHeadArtifactCacheLRU(entries map[string]headArtifactCacheEntry) {
	if len(entries) <= headArtifactCacheMaxEntries {
		return
	}
	type item struct {
		key string
		t   time.Time
	}
	items := make([]item, 0, len(entries))
	for k, e := range entries {
		t := e.AccessedAt
		if t.IsZero() {
			t = e.CreatedAt
		}
		items = append(items, item{key: k, t: t})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].t.Equal(items[j].t) {
			return items[i].key < items[j].key
		}
		return items[i].t.Before(items[j].t)
	})
	for len(entries) > headArtifactCacheMaxEntries && len(items) > 0 {
		delete(entries, items[0].key)
		items = items[1:]
	}
}

// getHeadArtifactCached returns a cached history artifact for the given key, if
// present and fresh, spawning no extraction git subprocesses. Like the report
// cache, a pure read hit does NOT rewrite the file just to bump AccessedAt
// (pass-1 discipline): the artifact is multi-MB and the AccessedAt bookkeeping
// is not load-bearing (eviction falls back to CreatedAt). Returns a deep-enough
// copy is unnecessary because the caller (assembleReport) only reads the slices.
func getHeadArtifactCached(namespace, headSHA, optsHash string) (*historyArtifact, bool) {
	if !correlationDiskCacheEnabled() {
		return nil, false
	}
	path, err := headArtifactCachePath(false)
	if err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return nil, false
	}
	defer func() { _ = unlockFile(f) }()

	cf := readHeadArtifactCacheLocked(f)
	entry, ok := cf.Entries[headArtifactCacheKey(namespace, headSHA, optsHash)]
	if !ok || entry.Artifact == nil {
		return nil, false
	}
	if entry.Namespace != namespace || entry.HeadSHA != headSHA || entry.OptsHash != optsHash {
		return nil, false
	}
	now := time.Now().UTC()
	if !cacheCreatedAtIsFresh(entry.CreatedAt, now, headArtifactCacheMaxAge) {
		return nil, false
	}
	return entry.Artifact, true
}

// putHeadArtifactCached persists a freshly extracted artifact. Runs only after a
// real extraction (a miss), so the rewrite cost is amortized against the
// expensive git extraction it lets future bead-edit invocations skip.
func putHeadArtifactCached(namespace, headSHA, optsHash string, art *historyArtifact) {
	if !correlationDiskCacheEnabled() || art == nil {
		return
	}
	data, err := json.Marshal(art)
	if err != nil || len(data) > headArtifactCacheMaxEntrySize {
		return
	}

	path, err := headArtifactCachePath(true)
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

	now := time.Now().UTC()
	cf := readHeadArtifactCacheLocked(f)
	pruneHeadArtifactCacheEntries(now, cf.Entries)
	if cf.Entries == nil {
		cf.Entries = map[string]headArtifactCacheEntry{}
	}
	cf.Entries[headArtifactCacheKey(namespace, headSHA, optsHash)] = headArtifactCacheEntry{
		CreatedAt:  now,
		AccessedAt: now,
		Namespace:  namespace,
		HeadSHA:    headSHA,
		OptsHash:   optsHash,
		Artifact:   art,
	}
	evictHeadArtifactCacheLRU(cf.Entries)
	_ = writeHeadArtifactCacheLocked(f, cf)
}
