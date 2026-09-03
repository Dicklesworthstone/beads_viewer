package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	json "github.com/goccy/go-json"
)

// diskCacheForced lets a non-robot caller (notably the `--export-pages` /
// `--watch-export` path) opt the correlation caches in for its own process
// without setting BV_ROBOT=1 (which also flips unrelated robot-mode output).
// The history export re-runs the same HEAD-keyed extraction on every re-export;
// enabling the content-addressed per-commit / HEAD-artifact / report caches for
// it means a long-lived watcher pays the full git-blob materialization once and
// then serves incrementally (#182). It is set once at startup and only ever
// read, so a plain atomic bool is sufficient.
var diskCacheForced atomic.Bool

// SetDiskCacheEnabled force-enables (or disables) the persistent correlation
// caches for the current process regardless of BV_ROBOT. BV_NO_CACHE=1 still
// wins as a hard off switch. Intended for the export path, which needs the
// caches' incrementality but does not run in robot mode.
func SetDiskCacheEnabled(on bool) { diskCacheForced.Store(on) }

// Persistent on-disk cache for the correlation HistoryReport consumed by the
// robot triage/next/history paths. The expensive part of GenerateReport is the
// git blob I/O (git log --raw --follow, git cat-file --batch streaming the
// historical .beads/issues.jsonl blobs, plus the batched co-commit git logs).
// Agents call `bv --robot-triage` repeatedly in a loop and between calls
// usually NOTHING relevant has changed, so the entire report is recomputable-
// identical and can be served from disk without spawning any extraction git
// subprocesses.
//
// The cache key captures every input the report depends on:
//   - repository/primary-history namespace: prevents two repositories or two
//     selected Beads JSONL paths with the same HEAD from sharing artifacts.
//   - HEAD commit SHA: invalidates when commits land (history changes).
//   - hashBeads(beads): the ID/Title/Status of the beads embedded in the report
//     (changes when beads are added/closed/retitled, including uncommitted
//     working-tree edits, since the bead slice is loaded from the working tree).
//   - hashOptions(opts): Limit / BeadID / Since / Until.
//   - schema version: bumps invalidate every stale entry on format changes.
//
// Working-tree note: the git-extracted portion of the report only reflects
// committed history (git log / cat-file never see uncommitted edits), so an
// uncommitted change to .beads/issues.jsonl cannot alter the extracted events.
// The only working-tree-visible inputs are the bead ID/Title/Status, which are
// captured by hashBeads. Together with the canonical repository/history
// namespace, the key is complete; a dirty tree still produces a correct
// hit/miss.

const (
	// correlationDiskCacheVersion: 3 = reports carry per-commit methods, the
	// walked window, strategy timings and feedback_applied (v2 reports lack
	// them and must not be served as-is).
	correlationDiskCacheVersion      = 3
	correlationDiskCacheFileName     = "correlation_report_cache.json"
	correlationDiskCacheDirName      = "bv"
	correlationDiskCacheMaxEntries   = 6
	correlationDiskCacheMaxAge       = 24 * time.Hour
	correlationDiskCacheMaxEntrySize = 64 << 20 // 64MB serialized report ceiling
	// Six maximum-size entries plus bounded JSON/metadata overhead. Reads use
	// both an fstat precheck and a limiting reader so a corrupt or concurrently
	// growing cache file cannot drive unbounded allocation.
	correlationDiskCacheMaxFileSize int64 = correlationDiskCacheMaxEntries*correlationDiskCacheMaxEntrySize + (1 << 20)
)

type correlationDiskCacheFile struct {
	Version int                                  `json:"version"`
	Entries map[string]correlationDiskCacheEntry `json:"entries"`
}

type correlationDiskCacheEntry struct {
	CreatedAt  time.Time      `json:"created_at"`
	AccessedAt time.Time      `json:"accessed_at"`
	Namespace  string         `json:"namespace"`
	HeadSHA    string         `json:"head_sha"`
	BeadsHash  string         `json:"beads_hash"`
	OptsHash   string         `json:"opts_hash"`
	Report     *HistoryReport `json:"report"`
}

// correlationDiskCacheEnabled reports whether the persistent report cache is
// active. It mirrors the analysis disk cache: on in robot mode, off when the
// caller asked to bypass caches.
func correlationDiskCacheEnabled() bool {
	if env.NoCache.Bool() {
		return false
	}
	return env.Robot.Bool() || diskCacheForced.Load()
}

// correlationDiskCachePath resolves the cache file location, honoring the same
// conventions as the analysis disk cache: BV_CACHE_DIR override, otherwise the
// user cache dir (which respects XDG_CACHE_HOME), under a shared "bv" subdir.
func correlationDiskCachePath(create bool) (string, error) {
	base := env.CacheDir.Get()
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
	return filepath.Join(base, correlationDiskCacheFileName), nil
}

func correlationDiskCacheKey(namespace, headSHA, beadsHash, optsHash string) string {
	return namespace + ":" + headSHA + ":" + beadsHash + ":" + optsHash
}

// correlationCacheNamespace isolates persistent history caches by both the
// repository and the selected Beads history path. HEAD alone is not a complete
// namespace: two workspaces can share a commit while selecting different JSONL
// histories, and the extractor's primaryBeadsFile is a real artifact input.
func correlationCacheNamespace(repoPath, primaryBeadsFile string) string {
	repoInput := absoluteCleanPath(repoPath)
	repoCanonical := repoInput
	if resolved, err := filepath.EvalSymlinks(repoInput); err == nil {
		repoCanonical = filepath.Clean(resolved)
	}

	if primaryBeadsFile == "" {
		primaryBeadsFile = defaultBeadsFiles[0]
	}
	// Keep the primary file as the lexical, repo-relative Git pathspec used by
	// Extractor. Resolving this path through the filesystem is incorrect: two
	// different tracked pathspecs may currently be symlinks to the same target,
	// yet `git log --follow -- <pathspec>` observes different histories.
	primaryPathspec := filepath.Clean(primaryBeadsFile)
	if filepath.IsAbs(primaryPathspec) {
		if rel, err := filepath.Rel(repoInput, primaryPathspec); err == nil && pathIsWithinRepo(rel) {
			primaryPathspec = filepath.Clean(rel)
		} else if rel, err := filepath.Rel(repoCanonical, primaryPathspec); err == nil && pathIsWithinRepo(rel) {
			primaryPathspec = filepath.Clean(rel)
		}
	}

	identity := filepath.ToSlash(repoCanonical) + "\x00" + filepath.ToSlash(primaryPathspec)
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func absoluteCleanPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func pathIsWithinRepo(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cacheCreatedAtIsFresh(createdAt, now time.Time, maxAge time.Duration) bool {
	return !createdAt.IsZero() && !createdAt.After(now) && now.Sub(createdAt) <= maxAge
}

func (c *Correlator) persistentCacheNamespace() string {
	primary := ""
	if c.extractor != nil {
		primary = c.extractor.primaryBeadsFile()
	}
	return correlationCacheNamespace(c.repoPath, primary)
}

func readCacheFileBounded(f *os.File, maxBytes int64) ([]byte, bool) {
	if maxBytes < 0 {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || info.Size() < 0 || info.Size() > maxBytes {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, false
	}
	return data, true
}

func readCorrelationDiskCacheLocked(f *os.File) correlationDiskCacheFile {
	empty := correlationDiskCacheFile{Version: correlationDiskCacheVersion, Entries: map[string]correlationDiskCacheEntry{}}
	if _, err := f.Seek(0, 0); err != nil {
		return empty
	}
	data, ok := readCacheFileBounded(f, correlationDiskCacheMaxFileSize)
	if !ok || len(data) == 0 {
		return empty
	}
	var cf correlationDiskCacheFile
	if err := json.Unmarshal(data, &cf); err != nil || cf.Version != correlationDiskCacheVersion {
		return empty
	}
	if cf.Entries == nil {
		cf.Entries = map[string]correlationDiskCacheEntry{}
	}
	return cf
}

func writeCorrelationDiskCacheLocked(f *os.File, cf correlationDiskCacheFile) error {
	if cf.Entries == nil {
		cf.Entries = map[string]correlationDiskCacheEntry{}
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

func pruneCorrelationDiskCacheEntries(now time.Time, entries map[string]correlationDiskCacheEntry) {
	for k, e := range entries {
		if !cacheCreatedAtIsFresh(e.CreatedAt, now, correlationDiskCacheMaxAge) {
			delete(entries, k)
		}
	}
}

func evictCorrelationDiskCacheLRU(entries map[string]correlationDiskCacheEntry) {
	if len(entries) <= correlationDiskCacheMaxEntries {
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
	for len(entries) > correlationDiskCacheMaxEntries && len(items) > 0 {
		delete(entries, items[0].key)
		items = items[1:]
	}
}

// getCorrelationDiskCachedReport returns a cached report for the given key, if
// present and fresh. It performs no git extraction subprocesses; the only git
// call the caller makes to reach this is the rev-parse HEAD used to build the
// key. Following the pass-1 lesson, a pure read hit does NOT rewrite the cache
// file just to bump the LRU AccessedAt timestamp: rewriting a multi-MB report
// file on every robot invocation would dominate the cost of a hit, and the
// AccessedAt bookkeeping is not load-bearing for correctness (eviction falls
// back to CreatedAt for never-rewritten entries, an acceptable LRU
// approximation). Prunes are likewise only persisted on the write path.
func getCorrelationDiskCachedReport(namespace, headSHA, beadsHash, optsHash string) (*HistoryReport, bool) {
	if !correlationDiskCacheEnabled() {
		return nil, false
	}
	path, err := correlationDiskCachePath(false)
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

	cf := readCorrelationDiskCacheLocked(f)
	key := correlationDiskCacheKey(namespace, headSHA, beadsHash, optsHash)
	entry, ok := cf.Entries[key]
	if !ok || entry.Report == nil {
		return nil, false
	}
	if entry.Namespace != namespace || entry.HeadSHA != headSHA || entry.BeadsHash != beadsHash || entry.OptsHash != optsHash || entry.Report.DataHash != beadsHash {
		return nil, false
	}
	now := time.Now().UTC()
	if !cacheCreatedAtIsFresh(entry.CreatedAt, now, correlationDiskCacheMaxAge) {
		return nil, false
	}
	return entry.Report, true
}

// putCorrelationDiskCachedReport persists a freshly computed report. This runs
// only after a real recompute (a cache miss), so the full rewrite cost is
// amortized against the expensive git extraction it just avoided next time.
func putCorrelationDiskCachedReport(namespace, headSHA, beadsHash, optsHash string, report *HistoryReport) {
	if !correlationDiskCacheEnabled() || report == nil {
		return
	}
	// Bound the serialized size: do not persist pathologically large reports.
	data, err := json.Marshal(report)
	if err != nil || len(data) > correlationDiskCacheMaxEntrySize {
		return
	}

	path, err := correlationDiskCachePath(true)
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
	cf := readCorrelationDiskCacheLocked(f)
	pruneCorrelationDiskCacheEntries(now, cf.Entries)
	if cf.Entries == nil {
		cf.Entries = map[string]correlationDiskCacheEntry{}
	}
	cf.Entries[correlationDiskCacheKey(namespace, headSHA, beadsHash, optsHash)] = correlationDiskCacheEntry{
		CreatedAt:  now,
		AccessedAt: now,
		Namespace:  namespace,
		HeadSHA:    headSHA,
		BeadsHash:  beadsHash,
		OptsHash:   optsHash,
		Report:     report,
	}
	evictCorrelationDiskCacheLRU(cf.Entries)
	_ = writeCorrelationDiskCacheLocked(f, cf)
}

// GenerateReportCached wraps Correlator.GenerateReport with a two-layer
// persistent disk cache, both keyed off the repository/history namespace and
// repository HEAD:
//
//  1. OUTER report cache (this file), keyed on namespace + HEAD + hashBeads +
//     options.
//     A hit means NOTHING relevant changed; the fully assembled report is
//     returned with no git extraction and no report re-assembly.
//
//  2. INNER HEAD-artifact cache (head_artifact_cache.go), keyed on namespace +
//     HEAD + options ONLY (no hashBeads). When the outer cache misses *because
//     beads changed* but HEAD is unchanged (the `br update X;
//     bv --robot-triage` loop), the cached history artifact — the expensive,
//     purely-history-derived []BeadEvent + co-commit data — is loaded cheaply
//     and the report is re-assembled against the *current* beads via
//     assembleReport, skipping the 232MB git-blob extraction entirely. The
//     freshly assembled report is then written back to the outer cache for the
//     new bead-hash.
//
// On a full miss (HEAD changed, or cold) it extracts once, persists BOTH the
// artifact and the report, and returns. The assembled report is byte-identical
// (modulo the always-fresh GeneratedAt timestamp, as with the pre-existing
// outer cache) whether served fresh, from the outer cache, or rebuilt from the
// artifact, because assembleReport is a pure function of (beads, opts, artifact)
// and the artifact is reproduced exactly from the same HEAD+options.
//
// When the cache is disabled (non-robot mode, BV_NO_CACHE=1) or the HEAD cannot
// be resolved, it falls straight through to GenerateReport unchanged.
func (c *Correlator) GenerateReportCached(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	if !correlationDiskCacheEnabled() {
		return c.GenerateReport(beads, opts)
	}

	headSHA, err := getGitHeadContext(c.ctx, c.repoPath)
	if err != nil {
		// Can't key the cache without a stable HEAD; compute uncached.
		return c.GenerateReport(beads, opts)
	}
	beadsHash := hashBeads(beads)
	optsHash := hashOptions(opts)
	namespace := c.persistentCacheNamespace()
	// The assembled REPORT depends on the feedback store (rejections remove
	// commits, confirmations pin confidence), the HEAD-only ARTIFACT does not.
	// Fold the store fingerprint into the outer key only, so a new confirm or
	// reject misses layer 1, reuses the layer-2 artifact, and re-assembles.
	reportOptsHash := c.reportOptsHash(optsHash)

	// Layer 1: fully assembled report for this exact (HEAD, beads, opts, feedback).
	if report, ok := getCorrelationDiskCachedReport(namespace, headSHA, beadsHash, reportOptsHash); ok {
		metrics.CorrelationCache.Hit()
		return report, nil
	}

	// Layer 2: HEAD-only artifact. A hit here means the expensive extraction is
	// reusable; only the cheap bead-dependent assembly must run.
	if art, ok := getHeadArtifactCached(namespace, headSHA, optsHash); ok {
		metrics.CorrelationCache.Hit()
		report := c.assembleReport(beads, opts, art)
		putCorrelationDiskCachedReport(namespace, headSHA, beadsHash, reportOptsHash, report)
		return report, nil
	}

	// Full miss: extract once, then assemble. Persist both layers only if HEAD
	// still matches the pre-extraction key; extraction spans multiple git calls,
	// so a concurrent commit could otherwise poison the old HEAD's cache entry.
	metrics.CorrelationCache.Miss()
	art, err := c.extractHistoryArtifact(opts)
	if err != nil {
		return nil, err
	}
	report := c.assembleReport(beads, opts, art)
	c.putExtractedHistoryCachesIfHeadUnchanged(namespace, headSHA, beadsHash, optsHash, art, report)
	return report, nil
}

// reportOptsHash extends the artifact options hash with the feedback store
// fingerprint for the assembled-report cache layer. Without a store it is
// exactly optsHash.
func (c *Correlator) reportOptsHash(optsHash string) string {
	fp := c.feedbackFingerprint()
	if fp == "" {
		return optsHash
	}
	return optsHash + ":fb" + fp
}

// putExtractedHistoryCachesIfHeadUnchanged closes the only correctness gap
// between the pre-extraction HEAD cache key and a multi-command extraction. A
// lookup failure is treated conservatively as drift: the caller still returns
// its computed report, but no persistent entry is published under an
// unverified key. optsHash is the ARTIFACT key; the report entry is stored
// under the feedback-extended reportOptsHash.
func (c *Correlator) putExtractedHistoryCachesIfHeadUnchanged(namespace, headSHA, beadsHash, optsHash string, art *historyArtifact, report *HistoryReport) bool {
	currentHead, err := getGitHeadContext(c.ctx, c.repoPath)
	if err != nil || currentHead != headSHA {
		return false
	}
	putHeadArtifactCached(namespace, headSHA, optsHash, art)
	putCorrelationDiskCachedReport(namespace, headSHA, beadsHash, c.reportOptsHash(optsHash), report)
	return true
}
