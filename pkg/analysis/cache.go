package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/xfetch"
)

const (
	// v5 changes cycle semantics to ignore closed/tombstoned issues. Reject v4
	// entries so an upgrade cannot resurrect historical cycles from disk.
	robotAnalysisDiskCacheVersion = 5
	// robotAnalysisDiskCacheSubdirName holds one file per cache entry. The
	// pre-v3 layout kept every entry in a single analysis_cache.json, which
	// made each lookup decode — and each store rewrite + fsync — the entire
	// multi-MB, multi-repo cache just to touch one entry (issue #192).
	robotAnalysisDiskCacheSubdirName = "analysis_cache"
	// robotAnalysisLegacyCacheFileName is the retired single-file layout,
	// removed opportunistically on the write path.
	robotAnalysisLegacyCacheFileName   = "analysis_cache.json"
	robotAnalysisDiskCacheDirName      = "bv"
	robotAnalysisDiskCacheMaxEntries   = 10
	robotAnalysisDiskCacheMaxAge       = 24 * time.Hour
	robotAnalysisDiskCacheMaxEntrySize = 10 << 20 // 10MB
)

// Cache holds cached analysis results keyed by data hash.
// Thread-safe for concurrent access.
type Cache struct {
	mu         sync.RWMutex
	dataHash   string
	stats      *GraphStats
	computedAt time.Time
	ttl        time.Duration
}

// DefaultCacheTTL is the default time-to-live for cached results.
const DefaultCacheTTL = 5 * time.Minute

// globalCache is the package-level cache instance.
var globalCache = &Cache{
	ttl: DefaultCacheTTL,
}

// GetGlobalCache returns the global cache instance.
func GetGlobalCache() *Cache {
	return globalCache
}

// NewCache creates a new cache with the specified TTL.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl: ttl,
	}
}

// Get retrieves cached stats if the data hash matches and TTL hasn't expired.
// Returns (stats, true) on cache hit, (nil, false) on cache miss.
func (c *Cache) Get(issues []model.Issue) (*GraphStats, bool) {
	// Compute hash outside the lock (expensive operation)
	hash := ComputeDataHash(issues)
	return c.GetByHash(hash)
}

// GetByHash retrieves cached stats if the hash matches and TTL hasn't expired.
// This is more efficient when the hash has already been computed.
func (c *Cache) GetByHash(hash string) (*GraphStats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !graphStatsReadyForCache(c.stats) {
		return nil, false
	}

	now := time.Now()
	if hash == c.dataHash && cacheTimestampIsFresh(c.computedAt, now, c.ttl) {
		return c.stats, true
	}
	return nil, false
}

func cacheTimestampIsFresh(timestamp, now time.Time, maxAge time.Duration) bool {
	return !timestamp.IsZero() && !timestamp.After(now) && now.Sub(timestamp) < maxAge
}

// Set stores complete analysis results in the cache. Incomplete Phase 2
// results are ignored so cancellation cannot poison a later live analysis.
func (c *Cache) Set(issues []model.Issue, stats *GraphStats) {
	// Compute hash outside the lock (expensive operation)
	hash := ComputeDataHash(issues)
	c.SetByHash(hash, stats)
}

// SetByHash stores complete analysis results with a pre-computed hash.
func (c *Cache) SetByHash(hash string, stats *GraphStats) {
	if !graphStatsReadyForCache(stats) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.dataHash = hash
	c.stats = stats
	c.computedAt = time.Now()
}

// Invalidate clears the cache.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dataHash = ""
	c.stats = nil
	c.computedAt = time.Time{}
}

// SetTTL updates the cache TTL.
func (c *Cache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// Hash returns the current data hash, or empty string if no cached data.
func (c *Cache) Hash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dataHash
}

// Stats returns cache statistics for debugging.
func (c *Cache) Stats() (hash string, age time.Duration, hasData bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.stats == nil {
		return "", 0, false
	}
	return c.dataHash, time.Since(c.computedAt), true
}

// ComputeDataHash generates a deterministic, collision-resistant hash of all
// modeled issue data. Per-issue fingerprints provide one canonical encoding for
// both snapshot diffs and the aggregate data hash. Valid input has unique IDs,
// so sorting by ID keeps its hash independent of input order. Malformed duplicate
// IDs retain encounter order because ID-keyed graph consumers otherwise observe
// last-record-wins semantics.
func ComputeDataHash(issues []model.Issue) string {
	if len(issues) == 0 {
		return "empty"
	}

	type orderedFingerprint struct {
		IssueFingerprint
		position int
	}
	fingerprints := make([]orderedFingerprint, len(issues))
	for i := range issues {
		fingerprints[i] = orderedFingerprint{
			IssueFingerprint: ComputeIssueFingerprint(issues[i]),
			position:         i,
		}
	}
	sort.Slice(fingerprints, func(i, j int) bool {
		if fingerprints[i].ID != fingerprints[j].ID {
			return fingerprints[i].ID < fingerprints[j].ID
		}
		// Valid datasets have unique IDs and remain order-independent. For
		// malformed duplicate IDs, preserve encounter order because analyzers use
		// last-record-wins maps and therefore observe that order semantically.
		return fingerprints[i].position < fingerprints[j].position
	})

	h := newFingerprintWriter()
	writeUintHash(h, uint64(len(fingerprints)))
	for _, fingerprint := range fingerprints {
		writeStringHash(h, fingerprint.ID)
		writeStringHash(h, fingerprint.ContentHash)
		writeStringHash(h, fingerprint.DependencyHash)
	}

	return h.sumHex()
}

// IssueFingerprint represents a per-issue hash split across content and dependencies.
// It supports fast diffing between snapshots without a full rebuild.
type IssueFingerprint struct {
	ID             string
	ContentHash    string
	DependencyHash string
}

// IssueDiff captures a per-issue diff between two snapshots.
type IssueDiff struct {
	Added             []string
	Removed           []string
	Modified          []string
	ContentChanged    []string
	DependencyChanged []string
	Unchanged         []string
	HasDuplicateIDs   bool
}

// ComputeIssueFingerprint returns the fingerprint for a single issue.
func ComputeIssueFingerprint(issue model.Issue) IssueFingerprint {
	return IssueFingerprint{
		ID:             issue.ID,
		ContentHash:    computeIssueContentHash(issue),
		DependencyHash: computeIssueDependencyHash(issue),
	}
}

// ComputeIssueDiff compares old and new issue slices and returns an IssueDiff.
func ComputeIssueDiff(oldIssues, newIssues []model.Issue) IssueDiff {
	oldFP := make(map[string]IssueFingerprint, len(oldIssues))
	oldCounts := make(map[string]int, len(oldIssues))
	for i := range oldIssues {
		fp := ComputeIssueFingerprint(oldIssues[i])
		oldFP[fp.ID] = fp
		oldCounts[fp.ID]++
	}
	newFP := make(map[string]IssueFingerprint, len(newIssues))
	newCounts := make(map[string]int, len(newIssues))
	for i := range newIssues {
		fp := ComputeIssueFingerprint(newIssues[i])
		newFP[fp.ID] = fp
		newCounts[fp.ID]++
	}

	var diff IssueDiff
	for id, newIssue := range newFP {
		if newCounts[id] > 1 {
			diff.HasDuplicateIDs = true
		}
		oldIssue, exists := oldFP[id]
		if !exists {
			diff.Added = append(diff.Added, id)
			continue
		}
		if oldCounts[id] > 1 || newCounts[id] > 1 {
			diff.HasDuplicateIDs = true
			diff.Modified = append(diff.Modified, id)
			continue
		}
		contentChanged := oldIssue.ContentHash != newIssue.ContentHash
		dependencyChanged := oldIssue.DependencyHash != newIssue.DependencyHash
		if contentChanged || dependencyChanged {
			diff.Modified = append(diff.Modified, id)
			if contentChanged {
				diff.ContentChanged = append(diff.ContentChanged, id)
			}
			if dependencyChanged {
				diff.DependencyChanged = append(diff.DependencyChanged, id)
			}
			continue
		}
		diff.Unchanged = append(diff.Unchanged, id)
	}

	for id := range oldFP {
		if _, exists := newFP[id]; !exists {
			diff.Removed = append(diff.Removed, id)
			if oldCounts[id] > 1 {
				diff.HasDuplicateIDs = true
			}
		}
	}

	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Modified)
	sort.Strings(diff.ContentChanged)
	sort.Strings(diff.DependencyChanged)
	sort.Strings(diff.Unchanged)
	return diff
}

func computeIssueContentHash(issue model.Issue) string {
	h := newFingerprintWriter()

	writeStringHash(h, issue.Title)
	writeStringHash(h, issue.Description)
	writeStringHash(h, issue.Design)
	writeStringHash(h, issue.AcceptanceCriteria)
	writeStringHash(h, issue.Notes)
	writeStringHash(h, issue.Assignee)
	writeStringHash(h, issue.SourceRepo)
	writeStringPtrHash(h, issue.ExternalRef)

	writeStringHash(h, string(issue.Status))
	writeStringHash(h, string(issue.IssueType))
	writeIntHash(h, issue.Priority)
	writeIntPtrHash(h, issue.EstimatedMinutes)
	writeTimeHash(h, issue.CreatedAt)
	writeTimeHash(h, issue.UpdatedAt)
	writeTimePtrHash(h, issue.DueDate)
	writeTimePtrHash(h, issue.DeferUntil)
	writeTimePtrHash(h, issue.ClosedAt)

	writeIntHash(h, issue.CompactionLevel)
	writeTimePtrHash(h, issue.CompactedAt)
	writeStringPtrHash(h, issue.CompactedAtCommit)
	writeIntHash(h, issue.OriginalSize)

	if len(issue.Labels) > 0 {
		labels := append([]string(nil), issue.Labels...)
		sort.Strings(labels)
		writeUintHash(h, uint64(len(labels)))
		for _, label := range labels {
			writeStringHash(h, label)
		}
	} else {
		writeUintHash(h, 0)
	}

	comments := make([]*model.Comment, 0, len(issue.Comments))
	if len(issue.Comments) > 0 {
		for _, comment := range issue.Comments {
			if comment != nil {
				comments = append(comments, comment)
			}
		}
		sort.Slice(comments, func(i, j int) bool {
			if comments[i].ID != comments[j].ID {
				return comments[i].ID < comments[j].ID
			}
			if !comments[i].CreatedAt.Equal(comments[j].CreatedAt) {
				return comments[i].CreatedAt.Before(comments[j].CreatedAt)
			}
			if comments[i].IssueID != comments[j].IssueID {
				return comments[i].IssueID < comments[j].IssueID
			}
			if comments[i].Author != comments[j].Author {
				return comments[i].Author < comments[j].Author
			}
			return comments[i].Text < comments[j].Text
		})
	}
	writeUintHash(h, uint64(len(comments)))
	for _, comment := range comments {
		writeStringHash(h, comment.ID)
		writeStringHash(h, comment.IssueID)
		writeStringHash(h, comment.Author)
		writeStringHash(h, comment.Text)
		writeTimeHash(h, comment.CreatedAt)
	}

	return h.sumHex()
}

func computeIssueDependencyHash(issue model.Issue) string {
	if len(issue.Dependencies) == 0 {
		return "none"
	}
	type depKey struct {
		issueID   string
		dependsOn string
		depType   string
		createdAt string
		createdBy string
	}
	deps := make([]depKey, 0, len(issue.Dependencies))
	for _, dep := range issue.Dependencies {
		if dep == nil {
			continue
		}
		deps = append(deps, depKey{
			issueID:   dep.IssueID,
			dependsOn: dep.DependsOnID,
			depType:   string(dep.Type),
			createdAt: dep.CreatedAt.UTC().Format(time.RFC3339Nano),
			createdBy: dep.CreatedBy,
		})
	}
	if len(deps) == 0 {
		return "none"
	}
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].issueID != deps[j].issueID {
			return deps[i].issueID < deps[j].issueID
		}
		if deps[i].dependsOn != deps[j].dependsOn {
			return deps[i].dependsOn < deps[j].dependsOn
		}
		if deps[i].depType != deps[j].depType {
			return deps[i].depType < deps[j].depType
		}
		if deps[i].createdAt != deps[j].createdAt {
			return deps[i].createdAt < deps[j].createdAt
		}
		return deps[i].createdBy < deps[j].createdBy
	})

	h := newFingerprintWriter()
	writeUintHash(h, uint64(len(deps)))
	for _, dep := range deps {
		writeStringHash(h, dep.issueID)
		writeStringHash(h, dep.dependsOn)
		writeStringHash(h, dep.depType)
		writeStringHash(h, dep.createdAt)
		writeStringHash(h, dep.createdBy)
	}
	return h.sumHex()
}

// fingerprintWriter streams the canonical encoding through one small buffer.
// In particular, long descriptions never allocate a temporary []byte, and
// scalar fields do not each allocate a slice merely to cross io.Writer.
// This changes only write batching; field bytes and SHA256 remain unchanged.
type fingerprintWriter struct {
	hash   hash.Hash
	buffer [256]byte
	used   int
}

func newFingerprintWriter() *fingerprintWriter {
	return &fingerprintWriter{hash: sha256.New()}
}

func (w *fingerprintWriter) flush() {
	if w.used > 0 {
		_, _ = w.hash.Write(w.buffer[:w.used])
		w.used = 0
	}
}

func (w *fingerprintWriter) writeByte(v byte) {
	if w.used == len(w.buffer) {
		w.flush()
	}
	w.buffer[w.used] = v
	w.used++
}

func (w *fingerprintWriter) sumHex() string {
	w.flush()
	return hex.EncodeToString(w.hash.Sum(nil))
}

func writeStringHash(w *fingerprintWriter, v string) {
	writeUintHash(w, uint64(len(v)))
	for len(v) > 0 {
		if w.used == len(w.buffer) {
			w.flush()
		}
		n := copy(w.buffer[w.used:], v)
		w.used += n
		v = v[n:]
	}
}

func writeStringPtrHash(w *fingerprintWriter, v *string) {
	if v == nil {
		w.writeByte(0)
		return
	}
	w.writeByte(1)
	writeStringHash(w, *v)
}

func writeIntHash(w *fingerprintWriter, v int) {
	writeInt64Hash(w, int64(v))
}

func writeIntPtrHash(w *fingerprintWriter, v *int) {
	if v == nil {
		w.writeByte(0)
		return
	}
	w.writeByte(1)
	writeIntHash(w, *v)
}

func writeInt64Hash(w *fingerprintWriter, v int64) {
	if len(w.buffer)-w.used < binary.MaxVarintLen64 {
		w.flush()
	}
	w.used += binary.PutVarint(w.buffer[w.used:], v)
}

func writeUintHash(w *fingerprintWriter, v uint64) {
	if len(w.buffer)-w.used < binary.MaxVarintLen64 {
		w.flush()
	}
	w.used += binary.PutUvarint(w.buffer[w.used:], v)
}

func writeTimeHash(w *fingerprintWriter, t time.Time) {
	formatted := ""
	if !t.IsZero() {
		formatted = t.UTC().Format(time.RFC3339Nano)
	}
	writeStringHash(w, formatted)
}

func writeTimePtrHash(w *fingerprintWriter, t *time.Time) {
	if t == nil {
		w.writeByte(0)
		return
	}
	w.writeByte(1)
	writeTimeHash(w, *t)
}

// ComputeConfigHash generates a deterministic hash of the analysis configuration.
func ComputeConfigHash(config *AnalysisConfig) string {
	if config == nil {
		return "dynamic"
	}
	h := sha256.New()
	// Using %#v is stable enough for configuration struct
	h.Write([]byte(fmt.Sprintf("%#v", *config)))
	return hex.EncodeToString(h.Sum(nil))
}

// CachedAnalyzer wraps an Analyzer with caching support.
type CachedAnalyzer struct {
	*Analyzer
	cache      *Cache
	issues     []model.Issue
	dataHash   string // Hash of the issue data
	configHash string // Hash of the configuration
	cacheHit   bool   // Set by AnalyzeAsync to track if it was a cache hit
}

// NewCachedAnalyzer creates an analyzer that checks the cache before computing.
// The Analyzer is always created because it may be needed for GenerateRecommendations
// even on cache hit. Creating the Analyzer (graph building) is O(V+E) which is fast;
// the expensive part is the analysis itself, which we skip on cache hit.
func NewCachedAnalyzer(issues []model.Issue, cache *Cache) *CachedAnalyzer {
	if cache == nil {
		cache = globalCache
	}
	analyzer := NewAnalyzer(issues)
	return &CachedAnalyzer{
		Analyzer: analyzer,
		cache:    cache,
		issues:   issues,
		// Reuse the analyzer's memoized data hash so the disk-cache key path
		// (AnalyzeAsyncWithConfig) and this struct share one SHA256 computation.
		dataHash:   analyzer.DataHash(),
		configHash: "dynamic",
	}
}

// SetConfig updates the analyzer configuration and the configuration hash.
func (ca *CachedAnalyzer) SetConfig(config *AnalysisConfig) {
	ca.Analyzer.SetConfig(config)
	ca.configHash = ComputeConfigHash(config)
}

// cacheDisabled reports whether the analyzer config opts out of caching.
func (ca *CachedAnalyzer) cacheDisabled() bool {
	return ca.Analyzer.config != nil && ca.Analyzer.config.DisableCache
}

// cacheKey is the combined in-memory cache key: dataHash|configHash.
func (ca *CachedAnalyzer) cacheKey() string {
	return ca.dataHash + "|" + ca.configHash
}

// AnalyzeAsync returns cached stats if available, otherwise computes and caches.
func (ca *CachedAnalyzer) AnalyzeAsync(ctx context.Context) *GraphStats {
	if ca.cacheDisabled() {
		ca.cacheHit = false
		return ca.Analyzer.AnalyzeAsync(ctx)
	}

	fullHash := ca.cacheKey()

	// Check cache first
	if stats, ok := ca.cache.GetByHash(fullHash); ok {
		ca.cacheHit = true
		metrics.GraphCache.Hit()
		return stats
	}

	// Cache miss - compute fresh
	ca.cacheHit = false
	metrics.GraphCache.Miss()
	stats := ca.Analyzer.AnalyzeAsync(ctx)

	// Store in cache when Phase 2 completes
	go func() {
		stats.WaitForPhase2()
		ca.cache.SetByHash(fullHash, stats)
	}()

	return stats
}

// Analyze returns cached stats if available, otherwise computes synchronously.
// Note: This returns a value copy that shares map references with the original.
// This is safe because the maps are immutable after Phase 2 completion.
func (ca *CachedAnalyzer) Analyze() GraphStats {
	stats := ca.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	// AnalyzeAsync publishes to the cache from a goroutine once Phase 2 is
	// done; a synchronous caller must not race it, so a fresh computation is
	// stored here before returning (SetByHash is idempotent and still refuses
	// incomplete results). Otherwise a second Analyze started immediately
	// after this one would recompute instead of hitting the cache.
	if !ca.cacheHit && !ca.cacheDisabled() {
		ca.cache.SetByHash(ca.cacheKey(), stats)
	}
	return GraphStats{
		OutDegree:         stats.OutDegree,
		InDegree:          stats.InDegree,
		TopologicalOrder:  stats.TopologicalOrder,
		Density:           stats.Density,
		NodeCount:         stats.NodeCount,
		EdgeCount:         stats.EdgeCount,
		Config:            stats.Config,
		pageRank:          stats.pageRank,
		betweenness:       stats.betweenness,
		eigenvector:       stats.eigenvector,
		hubs:              stats.hubs,
		authorities:       stats.authorities,
		criticalPathScore: stats.criticalPathScore,
		pageRankRank:      stats.pageRankRank,
		betweennessRank:   stats.betweennessRank,
		eigenvectorRank:   stats.eigenvectorRank,
		hubsRank:          stats.hubsRank,
		authoritiesRank:   stats.authoritiesRank,
		criticalPathRank:  stats.criticalPathRank,
		inDegreeRank:      stats.inDegreeRank,
		outDegreeRank:     stats.outDegreeRank,
		coreNumber:        stats.coreNumber,
		articulation:      stats.articulation,
		slack:             stats.slack,
		cycles:            stats.cycles,
		phase2Ready:       true,
		status:            stats.status,
	}
}

// DataHash returns the computed hash for the analyzer's issue data.
func (ca *CachedAnalyzer) DataHash() string {
	return ca.dataHash
}

// WasCacheHit returns true if the last AnalyzeAsync call was a cache hit.
func (ca *CachedAnalyzer) WasCacheHit() bool {
	return ca.cacheHit
}

// robotAnalysisDiskCacheEntry is the on-disk unit of the v3 per-entry cache
// layout: one JSON file per (dataHash, configHash) key under the
// analysis_cache/ directory. Key is stored inside the file so a filename
// collision or foreign file can never satisfy a lookup.
type robotAnalysisDiskCacheEntry struct {
	Version         int                 `json:"version"`
	Key             string              `json:"key"`
	CreatedAt       time.Time           `json:"created_at"`
	DataHash        string              `json:"data_hash"`
	ConfigHash      string              `json:"config_hash"`
	ComputeDuration time.Duration       `json:"compute_duration"` // For XFetch probabilistic refresh
	Result          graphStatsCacheBlob `json:"result"`
}

type graphStatsCacheBlob struct {
	OutDegree        map[string]int `json:"out_degree"`
	InDegree         map[string]int `json:"in_degree"`
	TopologicalOrder []string       `json:"topological_order"`
	Density          float64        `json:"density"`
	NodeCount        int            `json:"node_count"`
	EdgeCount        int            `json:"edge_count"`
	Config           AnalysisConfig `json:"config"`

	PageRank          map[string]float64 `json:"page_rank"`
	Betweenness       map[string]float64 `json:"betweenness"`
	Eigenvector       map[string]float64 `json:"eigenvector"`
	Hubs              map[string]float64 `json:"hubs"`
	Authorities       map[string]float64 `json:"authorities"`
	CriticalPathScore map[string]float64 `json:"critical_path_score"`
	CoreNumber        map[string]int     `json:"core_number"`
	Articulation      []string           `json:"articulation"`
	Slack             map[string]float64 `json:"slack"`
	Cycles            [][]string         `json:"cycles"`
	Status            MetricStatus       `json:"status"`
	decoded           bool
}

// UnmarshalJSON is the inverse of statusEntry.MarshalJSON. statusEntry's
// Elapsed field is intentionally emitted as fractional milliseconds under the
// wire name "ms"; without a matching decoder every disk-cache hit silently
// reset all metric timings to zero.
func (s *statusEntry) UnmarshalJSON(data []byte) error {
	type statusEntryWire struct {
		State   string  `json:"state"`
		Reason  string  `json:"reason,omitempty"`
		Sample  int     `json:"sample,omitempty"`
		Elapsed float64 `json:"ms,omitempty"`
	}

	var wire statusEntryWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	elapsedNanos := wire.Elapsed * float64(time.Millisecond)
	// float64(1<<63) is exact. Reject that boundary before converting to
	// time.Duration: the largest int64 itself rounds up to 1<<63 as a float64,
	// and an out-of-range float-to-int conversion is implementation-dependent.
	if math.IsNaN(wire.Elapsed) || math.IsInf(wire.Elapsed, 0) || wire.Elapsed < 0 ||
		elapsedNanos >= float64(1<<63) || math.Round(elapsedNanos) >= float64(1<<63) {
		return fmt.Errorf("invalid metric elapsed time %gms", wire.Elapsed)
	}

	s.State = wire.State
	s.Reason = wire.Reason
	s.Sample = wire.Sample
	s.Elapsed = time.Duration(math.Round(elapsedNanos))
	return nil
}

// graphStatsCacheSoA is the on-disk (serialized) form of graphStatsCacheBlob.
//
// Instead of ~10 separate map[string]T objects that each repeat every node-ID
// string as a JSON key (~10×N repeated strings + per-map rehash on decode), it
// uses a struct-of-arrays / dictionary-encoding layout: the node-ID strings are
// stored exactly once in Nodes, and every per-node metric is a positional array
// aligned to Nodes (index i → value for Nodes[i]).
//
// Nodes is the sorted union of the key sets of all per-node maps. Per-node
// metrics are dense over this set in practice (PageRank/InDegree/... all cover
// the whole graph), so each *Vals array is full length and a metric needs no
// presence info in the common case. To remain exactly round-trippable for the
// rare partial/nil map, each metric also carries:
//   - a *Set bool: distinguishes a non-nil map (possibly empty) from a nil map,
//     so toGraphStats() restores nil-ness exactly.
//   - an optional *Idx []int32: when a non-nil map does NOT cover every node in
//     Nodes, Idx lists the Nodes indices that ARE present and *Vals is aligned
//     to Idx instead of Nodes. When Idx is nil the metric is dense (covers all
//     of Nodes) and *Vals is aligned to Nodes directly. This keeps absent vs.
//     present-zero distinct without a per-node presence string.
//
// This is purely the serialized shape: graphStatsCacheBlob and the in-memory
// GraphStats it expands to are unchanged.
type graphStatsCacheSoA struct {
	Version int `json:"v"` // SoA payload version (matches robotAnalysisDiskCacheVersion intent)

	Nodes []string `json:"nodes"`

	TopologicalOrder []string       `json:"topological_order"`
	Density          float64        `json:"density"`
	NodeCount        int            `json:"node_count"`
	EdgeCount        int            `json:"edge_count"`
	Config           AnalysisConfig `json:"config"`
	RunToCompletion  bool           `json:"run_to_completion,omitempty"`
	Articulation     []string       `json:"articulation"`
	Cycles           [][]string     `json:"cycles"`
	Status           MetricStatus   `json:"status"`

	// Float metrics (positional, aligned to Nodes unless *Idx present).
	PageRankSet bool      `json:"pr_set,omitempty"`
	PageRankIdx []int32   `json:"pr_idx"`
	PageRank    []float64 `json:"pr,omitempty"`

	BetweennessSet bool      `json:"bt_set,omitempty"`
	BetweennessIdx []int32   `json:"bt_idx"`
	Betweenness    []float64 `json:"bt,omitempty"`

	EigenvectorSet bool      `json:"ev_set,omitempty"`
	EigenvectorIdx []int32   `json:"ev_idx"`
	Eigenvector    []float64 `json:"ev,omitempty"`

	HubsSet bool      `json:"hub_set,omitempty"`
	HubsIdx []int32   `json:"hub_idx"`
	Hubs    []float64 `json:"hub,omitempty"`

	AuthoritiesSet bool      `json:"auth_set,omitempty"`
	AuthoritiesIdx []int32   `json:"auth_idx"`
	Authorities    []float64 `json:"auth,omitempty"`

	CriticalPathScoreSet bool      `json:"cp_set,omitempty"`
	CriticalPathScoreIdx []int32   `json:"cp_idx"`
	CriticalPathScore    []float64 `json:"cp,omitempty"`

	SlackSet bool      `json:"sl_set,omitempty"`
	SlackIdx []int32   `json:"sl_idx"`
	Slack    []float64 `json:"sl,omitempty"`

	// Int metrics (positional, aligned to Nodes unless *Idx present).
	OutDegreeSet bool    `json:"od_set,omitempty"`
	OutDegreeIdx []int32 `json:"od_idx"`
	OutDegree    []int   `json:"od,omitempty"`

	InDegreeSet bool    `json:"id_set,omitempty"`
	InDegreeIdx []int32 `json:"id_idx"`
	InDegree    []int   `json:"id,omitempty"`

	CoreNumberSet bool    `json:"kc_set,omitempty"`
	CoreNumberIdx []int32 `json:"kc_idx"`
	CoreNumber    []int   `json:"kc,omitempty"`
}

// MarshalJSON flattens the string-keyed maps into the compact SoA layout.
func (b graphStatsCacheBlob) MarshalJSON() ([]byte, error) {
	// Build the shared node index: sorted union of every per-node map's keys.
	nodeSet := make(map[string]struct{})
	addFloatKeys := func(m map[string]float64) {
		for k := range m {
			nodeSet[k] = struct{}{}
		}
	}
	addIntKeys := func(m map[string]int) {
		for k := range m {
			nodeSet[k] = struct{}{}
		}
	}
	addFloatKeys(b.PageRank)
	addFloatKeys(b.Betweenness)
	addFloatKeys(b.Eigenvector)
	addFloatKeys(b.Hubs)
	addFloatKeys(b.Authorities)
	addFloatKeys(b.CriticalPathScore)
	addFloatKeys(b.Slack)
	addIntKeys(b.OutDegree)
	addIntKeys(b.InDegree)
	addIntKeys(b.CoreNumber)

	nodes := make([]string, 0, len(nodeSet))
	for k := range nodeSet {
		nodes = append(nodes, k)
	}
	sort.Strings(nodes)

	soa := graphStatsCacheSoA{
		Version:          robotAnalysisDiskCacheVersion,
		Nodes:            nodes,
		TopologicalOrder: b.TopologicalOrder,
		Density:          b.Density,
		NodeCount:        b.NodeCount,
		EdgeCount:        b.EdgeCount,
		Config:           b.Config,
		RunToCompletion:  b.Config.RunToCompletion,
		Articulation:     b.Articulation,
		Cycles:           b.Cycles,
		Status:           b.Status,
	}

	soa.PageRankSet, soa.PageRankIdx, soa.PageRank = flattenFloat(b.PageRank, nodes)
	soa.BetweennessSet, soa.BetweennessIdx, soa.Betweenness = flattenFloat(b.Betweenness, nodes)
	soa.EigenvectorSet, soa.EigenvectorIdx, soa.Eigenvector = flattenFloat(b.Eigenvector, nodes)
	soa.HubsSet, soa.HubsIdx, soa.Hubs = flattenFloat(b.Hubs, nodes)
	soa.AuthoritiesSet, soa.AuthoritiesIdx, soa.Authorities = flattenFloat(b.Authorities, nodes)
	soa.CriticalPathScoreSet, soa.CriticalPathScoreIdx, soa.CriticalPathScore = flattenFloat(b.CriticalPathScore, nodes)
	soa.SlackSet, soa.SlackIdx, soa.Slack = flattenFloat(b.Slack, nodes)

	soa.OutDegreeSet, soa.OutDegreeIdx, soa.OutDegree = flattenInt(b.OutDegree, nodes)
	soa.InDegreeSet, soa.InDegreeIdx, soa.InDegree = flattenInt(b.InDegree, nodes)
	soa.CoreNumberSet, soa.CoreNumberIdx, soa.CoreNumber = flattenInt(b.CoreNumber, nodes)

	return json.Marshal(soa)
}

// UnmarshalJSON expands the compact SoA layout back into the string-keyed maps.
func (b *graphStatsCacheBlob) UnmarshalJSON(data []byte) error {
	var soa graphStatsCacheSoA
	if err := json.Unmarshal(data, &soa); err != nil {
		return err
	}
	if err := soa.validate(); err != nil {
		return fmt.Errorf("invalid graph stats cache payload: %w", err)
	}

	b.TopologicalOrder = soa.TopologicalOrder
	b.Density = soa.Density
	b.NodeCount = soa.NodeCount
	b.EdgeCount = soa.EdgeCount
	b.Config = soa.Config
	b.Config.RunToCompletion = soa.RunToCompletion
	b.Articulation = soa.Articulation
	b.Cycles = soa.Cycles
	b.Status = soa.Status

	b.PageRank = expandFloat(soa.PageRankSet, soa.PageRankIdx, soa.PageRank, soa.Nodes)
	b.Betweenness = expandFloat(soa.BetweennessSet, soa.BetweennessIdx, soa.Betweenness, soa.Nodes)
	b.Eigenvector = expandFloat(soa.EigenvectorSet, soa.EigenvectorIdx, soa.Eigenvector, soa.Nodes)
	b.Hubs = expandFloat(soa.HubsSet, soa.HubsIdx, soa.Hubs, soa.Nodes)
	b.Authorities = expandFloat(soa.AuthoritiesSet, soa.AuthoritiesIdx, soa.Authorities, soa.Nodes)
	b.CriticalPathScore = expandFloat(soa.CriticalPathScoreSet, soa.CriticalPathScoreIdx, soa.CriticalPathScore, soa.Nodes)
	b.Slack = expandFloat(soa.SlackSet, soa.SlackIdx, soa.Slack, soa.Nodes)

	b.OutDegree = expandInt(soa.OutDegreeSet, soa.OutDegreeIdx, soa.OutDegree, soa.Nodes)
	b.InDegree = expandInt(soa.InDegreeSet, soa.InDegreeIdx, soa.InDegree, soa.Nodes)
	b.CoreNumber = expandInt(soa.CoreNumberSet, soa.CoreNumberIdx, soa.CoreNumber, soa.Nodes)
	b.decoded = true

	return nil
}

func (s graphStatsCacheSoA) validate() error {
	if s.Version != robotAnalysisDiskCacheVersion {
		return fmt.Errorf("version %d, want %d", s.Version, robotAnalysisDiskCacheVersion)
	}
	if s.NodeCount < 0 || s.EdgeCount < 0 {
		return fmt.Errorf("negative graph size nodes=%d edges=%d", s.NodeCount, s.EdgeCount)
	}
	if len(s.Nodes) != s.NodeCount {
		return fmt.Errorf("node dictionary length %d does not match node_count %d", len(s.Nodes), s.NodeCount)
	}
	// Self-loops add edges without adding ordered node pairs, so a legitimate
	// writer can produce density above 1 (density = e / (n * (n - 1))). Only
	// reject values that no writer can produce: negative or non-finite.
	if math.IsNaN(s.Density) || math.IsInf(s.Density, 0) || s.Density < 0 {
		return fmt.Errorf("density %g is negative or non-finite", s.Density)
	}
	nodeSet := make(map[string]struct{}, len(s.Nodes))
	for i, node := range s.Nodes {
		if node == "" {
			return fmt.Errorf("empty node ID at dictionary index %d", i)
		}
		if i > 0 && s.Nodes[i-1] >= node {
			return fmt.Errorf("node dictionary is not strictly sorted at index %d", i)
		}
		nodeSet[node] = struct{}{}
	}

	columns := []struct {
		name     string
		set      bool
		idx      []int32
		valueLen int
	}{
		{name: "page_rank", set: s.PageRankSet, idx: s.PageRankIdx, valueLen: len(s.PageRank)},
		{name: "betweenness", set: s.BetweennessSet, idx: s.BetweennessIdx, valueLen: len(s.Betweenness)},
		{name: "eigenvector", set: s.EigenvectorSet, idx: s.EigenvectorIdx, valueLen: len(s.Eigenvector)},
		{name: "hubs", set: s.HubsSet, idx: s.HubsIdx, valueLen: len(s.Hubs)},
		{name: "authorities", set: s.AuthoritiesSet, idx: s.AuthoritiesIdx, valueLen: len(s.Authorities)},
		{name: "critical_path_score", set: s.CriticalPathScoreSet, idx: s.CriticalPathScoreIdx, valueLen: len(s.CriticalPathScore)},
		{name: "slack", set: s.SlackSet, idx: s.SlackIdx, valueLen: len(s.Slack)},
		{name: "out_degree", set: s.OutDegreeSet, idx: s.OutDegreeIdx, valueLen: len(s.OutDegree)},
		{name: "in_degree", set: s.InDegreeSet, idx: s.InDegreeIdx, valueLen: len(s.InDegree)},
		{name: "core_number", set: s.CoreNumberSet, idx: s.CoreNumberIdx, valueLen: len(s.CoreNumber)},
	}
	for _, column := range columns {
		if err := validateGraphStatsCacheColumn(column.name, column.set, column.idx, column.valueLen, len(s.Nodes)); err != nil {
			return err
		}
	}
	// Phase 1 degree maps are initialized densely for every graph, including
	// the empty graph. They are also the authoritative columns used below to
	// verify the persisted node/edge counts and density.
	if !s.OutDegreeSet || s.OutDegreeIdx != nil || !s.InDegreeSet || s.InDegreeIdx != nil {
		return fmt.Errorf("phase-1 degree columns must both be present and dense")
	}
	if err := s.validateGraphShape(); err != nil {
		return err
	}
	if len(s.TopologicalOrder) != 0 && len(s.TopologicalOrder) != s.NodeCount {
		return fmt.Errorf("topological order length %d is neither zero nor node_count %d", len(s.TopologicalOrder), s.NodeCount)
	}
	seenTopo := make(map[string]struct{}, len(s.TopologicalOrder))
	for _, node := range s.TopologicalOrder {
		if _, exists := nodeSet[node]; !exists {
			return fmt.Errorf("topological order references unknown node %q", node)
		}
		if _, duplicate := seenTopo[node]; duplicate {
			return fmt.Errorf("topological order repeats node %q", node)
		}
		seenTopo[node] = struct{}{}
	}
	seenArticulation := make(map[string]struct{}, len(s.Articulation))
	for _, node := range s.Articulation {
		if _, exists := nodeSet[node]; !exists {
			return fmt.Errorf("articulation list references unknown node %q", node)
		}
		if _, duplicate := seenArticulation[node]; duplicate {
			return fmt.Errorf("articulation list repeats node %q", node)
		}
		seenArticulation[node] = struct{}{}
	}
	for cycleIndex, cycle := range s.Cycles {
		if len(cycle) < 2 {
			return fmt.Errorf("cycle %d has length %d; want a closed path", cycleIndex, len(cycle))
		}
		if cycle[0] != cycle[len(cycle)-1] {
			return fmt.Errorf("cycle %d is not closed", cycleIndex)
		}
		seenCycleNode := make(map[string]struct{}, len(cycle)-1)
		for position, node := range cycle {
			if _, exists := nodeSet[node]; !exists {
				return fmt.Errorf("cycle %d references unknown node %q", cycleIndex, node)
			}
			if position == len(cycle)-1 {
				continue
			}
			if _, duplicate := seenCycleNode[node]; duplicate {
				return fmt.Errorf("cycle %d repeats interior node %q", cycleIndex, node)
			}
			seenCycleNode[node] = struct{}{}
		}
	}
	return nil
}

func (s graphStatsCacheSoA) validateGraphShape() error {
	maxEdges := int64(s.NodeCount) * int64(s.NodeCount)
	if int64(s.EdgeCount) > maxEdges {
		return fmt.Errorf("edge_count %d exceeds maximum %d for %d nodes", s.EdgeCount, maxEdges, s.NodeCount)
	}

	var outSum, inSum int64
	for i, degree := range s.OutDegree {
		if degree < 0 || degree > s.NodeCount {
			return fmt.Errorf("out_degree[%d]=%d is outside [0,%d]", i, degree, s.NodeCount)
		}
		outSum += int64(degree)
	}
	for i, degree := range s.InDegree {
		if degree < 0 || degree > s.NodeCount {
			return fmt.Errorf("in_degree[%d]=%d is outside [0,%d]", i, degree, s.NodeCount)
		}
		inSum += int64(degree)
	}
	if outSum != int64(s.EdgeCount) || inSum != int64(s.EdgeCount) {
		return fmt.Errorf("degree sums out=%d in=%d do not both match edge_count %d", outSum, inSum, s.EdgeCount)
	}

	wantDensity := 0.0
	if s.NodeCount > 1 {
		wantDensity = float64(s.EdgeCount) / (float64(s.NodeCount) * float64(s.NodeCount-1))
	}
	tolerance := 1e-12 * math.Max(1, math.Abs(wantDensity))
	if math.IsNaN(s.Density) || math.IsInf(s.Density, 0) || s.Density < 0 || math.Abs(s.Density-wantDensity) > tolerance {
		return fmt.Errorf("density %g does not match node/edge counts (want %g)", s.Density, wantDensity)
	}
	return nil
}

// validateForCacheHit checks invariants that are specific to completed analyzer
// output. The generic SoA codec deliberately supports nil metric maps for exact
// round-trip tests, but the analyzer always installs these six maps before it
// marks phase 2 ready and writes a disk entry. Omitting any of them must
// therefore degrade to a miss rather than fabricate a phase2Ready GraphStats.
func (b graphStatsCacheBlob) validateForCacheHit() error {
	if !b.decoded {
		return fmt.Errorf("result payload was not decoded")
	}
	requiredMaps := []struct {
		name    string
		present bool
	}{
		{name: "page_rank", present: b.PageRank != nil},
		{name: "betweenness", present: b.Betweenness != nil},
		{name: "eigenvector", present: b.Eigenvector != nil},
		{name: "hubs", present: b.Hubs != nil},
		{name: "authorities", present: b.Authorities != nil},
		{name: "critical_path_score", present: b.CriticalPathScore != nil},
	}
	for _, metric := range requiredMaps {
		if !metric.present {
			return fmt.Errorf("completed result is missing %s", metric.name)
		}
	}

	statuses := []struct {
		name  string
		entry statusEntry
	}{
		{name: "page_rank", entry: b.Status.PageRank},
		{name: "betweenness", entry: b.Status.Betweenness},
		{name: "eigenvector", entry: b.Status.Eigenvector},
		{name: "hits", entry: b.Status.HITS},
		{name: "critical", entry: b.Status.Critical},
		{name: "cycles", entry: b.Status.Cycles},
		{name: "k_core", entry: b.Status.KCore},
		{name: "articulation", entry: b.Status.Articulation},
		{name: "slack", entry: b.Status.Slack},
	}
	for _, metric := range statuses {
		switch metric.entry.State {
		case "computed", "approx", "skipped":
		default:
			return fmt.Errorf("completed result has invalid %s status %q", metric.name, metric.entry.State)
		}
		if metric.entry.Sample < 0 || metric.entry.Elapsed < 0 {
			return fmt.Errorf("completed result has invalid %s status metadata", metric.name)
		}
	}

	// PageRank and betweenness are the two claim-critical triage inputs. Bind
	// their status to the persisted config so a corrupt entry cannot mark a
	// disabled or incomplete metric "computed" and bypass robot-next's
	// fail-closed status check.
	if err := validateConfiguredCacheMetricStatus("page_rank", b.Config.ComputePageRank, b.Status.PageRank); err != nil {
		return err
	}
	if err := validateConfiguredCacheMetricStatus("betweenness", b.Config.ComputeBetweenness, b.Status.Betweenness); err != nil {
		return err
	}

	// The producer emits PageRank for every node both on success and on timeout
	// (the timeout fallback is uniform). A merely non-nil sparse/empty map is
	// therefore not a completed enabled result. Disabled metric maps are
	// initialized non-nil but remain empty.
	if b.Config.ComputePageRank {
		if len(b.PageRank) != b.NodeCount {
			return fmt.Errorf("enabled page_rank has %d values, want node_count %d", len(b.PageRank), b.NodeCount)
		}
		pageRankSum := 0.0
		for node, value := range b.PageRank {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return fmt.Errorf("page_rank[%q]=%g is not finite and nonnegative", node, value)
			}
			pageRankSum += value
		}
		wantSum := 0.0
		if b.NodeCount > 0 {
			wantSum = 1
		}
		if math.IsNaN(pageRankSum) || math.IsInf(pageRankSum, 0) || math.Abs(pageRankSum-wantSum) > 1e-9 {
			return fmt.Errorf("page_rank sum %g does not match normalized total %g", pageRankSum, wantSum)
		}
	} else if len(b.PageRank) != 0 {
		return fmt.Errorf("disabled page_rank unexpectedly has %d values", len(b.PageRank))
	}
	if !b.Config.ComputeBetweenness && len(b.Betweenness) != 0 {
		return fmt.Errorf("disabled betweenness unexpectedly has %d values", len(b.Betweenness))
	}
	if b.Config.ComputeBetweenness {
		for node, value := range b.Betweenness {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return fmt.Errorf("betweenness[%q]=%g is not finite and nonnegative", node, value)
			}
		}
	}
	return nil
}

func validateConfiguredCacheMetricStatus(name string, enabled bool, entry statusEntry) error {
	if enabled {
		switch entry.State {
		case "computed":
			return nil
		default:
			return fmt.Errorf("enabled %s has status %q", name, entry.State)
		}
	}
	if entry.State != "skipped" {
		return fmt.Errorf("disabled %s has status %q", name, entry.State)
	}
	return nil
}

func validateGraphStatsCacheColumn(name string, set bool, idx []int32, valueLen, nodeCount int) error {
	if !set {
		if idx != nil || valueLen != 0 {
			return fmt.Errorf("%s has data while its set flag is false", name)
		}
		return nil
	}
	if idx == nil {
		if valueLen != nodeCount {
			return fmt.Errorf("%s dense value length %d does not match node count %d", name, valueLen, nodeCount)
		}
		return nil
	}
	if len(idx) != valueLen {
		return fmt.Errorf("%s sparse index/value lengths differ: %d/%d", name, len(idx), valueLen)
	}
	previous := int32(-1)
	for i, nodeIndex := range idx {
		if nodeIndex < 0 || int(nodeIndex) >= nodeCount {
			return fmt.Errorf("%s sparse index %d at position %d is outside [0,%d)", name, nodeIndex, i, nodeCount)
		}
		if nodeIndex <= previous {
			return fmt.Errorf("%s sparse indexes are not strictly increasing at position %d", name, i)
		}
		previous = nodeIndex
	}
	return nil
}

// flattenFloat columnarizes a string-keyed float map against the shared Nodes
// index. Returns (set, idx, vals): set=false ⇒ nil map; idx=nil ⇒ vals is dense
// over Nodes; idx non-nil ⇒ vals[i] is the value for Nodes[idx[i]].
func flattenFloat(m map[string]float64, nodes []string) (bool, []int32, []float64) {
	if m == nil {
		return false, nil, nil
	}
	if len(m) == len(nodes) {
		// Dense: every node in the shared index has a value (the union is built
		// from these maps' keys, so equal length ⇒ identical key set).
		vals := make([]float64, len(nodes))
		for i, n := range nodes {
			vals[i] = m[n]
		}
		return true, nil, vals
	}
	// Sparse: emit (index, value) pairs in Nodes order for determinism.
	idx := make([]int32, 0, len(m))
	vals := make([]float64, 0, len(m))
	for i, n := range nodes {
		if v, ok := m[n]; ok {
			idx = append(idx, int32(i))
			vals = append(vals, v)
		}
	}
	return true, idx, vals
}

func flattenInt(m map[string]int, nodes []string) (bool, []int32, []int) {
	if m == nil {
		return false, nil, nil
	}
	if len(m) == len(nodes) {
		vals := make([]int, len(nodes))
		for i, n := range nodes {
			vals[i] = m[n]
		}
		return true, nil, vals
	}
	idx := make([]int32, 0, len(m))
	vals := make([]int, 0, len(m))
	for i, n := range nodes {
		if v, ok := m[n]; ok {
			idx = append(idx, int32(i))
			vals = append(vals, v)
		}
	}
	return true, idx, vals
}

func expandFloat(set bool, idx []int32, vals []float64, nodes []string) map[string]float64 {
	if !set {
		return nil
	}
	if idx == nil {
		m := make(map[string]float64, len(vals))
		for i := range vals {
			if i < len(nodes) {
				m[nodes[i]] = vals[i]
			}
		}
		return m
	}
	m := make(map[string]float64, len(idx))
	for i, ni := range idx {
		// ni >= 0 guards a corrupt/hand-edited cache file with a negative sparse
		// index: nodes[-1] would panic and crash the whole bv command. A bad cache
		// must degrade to a miss, never panic.
		if ni >= 0 && int(ni) < len(nodes) && i < len(vals) {
			m[nodes[ni]] = vals[i]
		}
	}
	return m
}

func expandInt(set bool, idx []int32, vals []int, nodes []string) map[string]int {
	if !set {
		return nil
	}
	if idx == nil {
		m := make(map[string]int, len(vals))
		for i := range vals {
			if i < len(nodes) {
				m[nodes[i]] = vals[i]
			}
		}
		return m
	}
	m := make(map[string]int, len(idx))
	for i, ni := range idx {
		// ni >= 0: see expandFloat — guards against a negative index in a corrupt
		// cache file panicking instead of degrading to a miss.
		if ni >= 0 && int(ni) < len(nodes) && i < len(vals) {
			m[nodes[ni]] = vals[i]
		}
	}
	return m
}

func (b graphStatsCacheBlob) toGraphStats() *GraphStats {
	stats := &GraphStats{
		OutDegree:        b.OutDegree,
		InDegree:         b.InDegree,
		TopologicalOrder: b.TopologicalOrder,
		Density:          b.Density,
		NodeCount:        b.NodeCount,
		EdgeCount:        b.EdgeCount,
		Config:           b.Config,

		phase2Ready: true,
		phase2Done:  make(chan struct{}),

		pageRank:          b.PageRank,
		betweenness:       b.Betweenness,
		eigenvector:       b.Eigenvector,
		hubs:              b.Hubs,
		authorities:       b.Authorities,
		criticalPathScore: b.CriticalPathScore,
		coreNumber:        b.CoreNumber,
		slack:             b.Slack,
		cycles:            b.Cycles,
		status:            b.Status,
	}

	if len(b.Articulation) > 0 {
		art := make(map[string]bool, len(b.Articulation))
		for _, id := range b.Articulation {
			art[id] = true
		}
		stats.articulation = art
	}

	// Rank maps are derived for UI optimization, so recompute rather than persist.
	stats.inDegreeRank = computeIntRanks(stats.InDegree)
	stats.outDegreeRank = computeIntRanks(stats.OutDegree)
	stats.pageRankRank = computeFloatRanks(stats.pageRank)
	stats.betweennessRank = computeFloatRanks(stats.betweenness)
	stats.eigenvectorRank = computeFloatRanks(stats.eigenvector)
	stats.hubsRank = computeFloatRanks(stats.hubs)
	stats.authoritiesRank = computeFloatRanks(stats.authorities)
	stats.criticalPathRank = computeFloatRanks(stats.criticalPathScore)

	close(stats.phase2Done)
	return stats
}

func robotDiskCacheEnabled() bool {
	return env.Robot.Bool() && !env.NoCache.Bool()
}

// beadsDirModTime returns the most recent modification time of the .beads/
// directory. This is used as a staleness signal: if the directory has been
// modified more recently than a cache entry was created, the entry is stale
// because bead data may have changed (e.g., a bead was closed in br).
// Returns zero time on any error (which disables the mtime check).
func beadsDirModTime() time.Time {
	// Check BEADS_DB first, then BEADS_DIR, then cwd/.beads
	beadsDir := ""
	if dbPath := env.BeadsDB.Get(); dbPath != "" {
		info, err := os.Stat(dbPath)
		if err == nil {
			if !info.IsDir() {
				return info.ModTime()
			}
			beadsDir = dbPath
		} else if looksLikeBeadsDBFile(dbPath) {
			beadsDir = filepath.Dir(dbPath)
		}
	}

	if beadsDir == "" {
		beadsDir = env.BeadsDir.Get()
		if beadsDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return time.Time{}
			}
			beadsDir = filepath.Join(cwd, ".beads")
		}
	}

	return beadsTreeModTime(beadsDir)
}

func looksLikeBeadsDBFile(dbPath string) bool {
	switch strings.ToLower(filepath.Ext(dbPath)) {
	case ".jsonl", ".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

// beadsTreeModTime returns the most recent modification time among the
// .beads/ directory itself and the regular files directly inside it.
//
// Only the top level is consulted, deliberately. Every data file bv loads
// (issues.jsonl, beads.jsonl, beads.db and its -wal sidecar) lives directly in
// .beads/, while its subdirectories (.br_history/, .br_recovery/, history/,
// snapshot trees) hold append-only journals that never feed the graph and can
// hold thousands of files. Recursing into them made every cache *validation*
// cost O(files in .beads/) of stat calls — on medium repos that was several
// times slower than simply recomputing the graph, so the "cached" path lost to
// --no-cache (issue #192). Individual files are still stat'ed because a file
// rewritten in place does not bump its parent directory's mtime.
//
// A file that vanishes between ReadDir and Info (br's transient *.lock files)
// is skipped rather than disabling the check.
func beadsTreeModTime(beadsDir string) time.Time {
	info, err := os.Stat(beadsDir)
	if err != nil {
		return time.Time{}
	}
	if !info.IsDir() {
		return time.Time{}
	}

	latest := info.ModTime()
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return time.Time{}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		finfo, err := entry.Info()
		if err != nil {
			continue
		}
		if finfo.ModTime().After(latest) {
			latest = finfo.ModTime()
		}
	}
	return latest
}

// robotAnalysisDiskCacheDir resolves (and optionally creates) the directory
// holding the per-entry cache files.
func robotAnalysisDiskCacheDir(create bool) (string, error) {
	base := env.CacheDir.Get()
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("getting user cache dir: %w", err)
		}
		base = filepath.Join(dir, robotAnalysisDiskCacheDirName)
	}
	dir := filepath.Join(base, robotAnalysisDiskCacheSubdirName)
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("creating cache dir: %w", err)
		}
	}
	return dir, nil
}

// robotAnalysisEntryFileName maps a cache key to its entry file. The key (a
// dataHash|configHash pair of hex digests) is itself hashed so the filename is
// fixed-length and free of separator characters; the full key is verified
// against the entry body on read.
func robotAnalysisEntryFileName(fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(sum[:16]) + ".json"
}

// pruneAndEvictRobotDiskCacheDir removes expired entry files (older than
// robotAnalysisDiskCacheMaxAge) and, if more than
// robotAnalysisDiskCacheMaxEntries remain, the oldest extras. File mtime is
// the recency signal: entries are written once and never touched on read, so
// mtime equals creation time and eviction is effectively FIFO by CreatedAt —
// the same approximation the v2 layout used once read-hits stopped persisting
// AccessedAt. Stale tmp-* files from crashed writers are reaped on the same
// schedule. Runs only on the write path, under the writer lock.
func pruneAndEvictRobotDiskCacheDir(dir string, now time.Time) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		name  string
		mtime time.Time
	}
	var files []item
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		info, err := de.Info()
		if err != nil {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			if strings.HasPrefix(name, "tmp-") && now.Sub(info.ModTime()) > robotAnalysisDiskCacheMaxAge {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if now.Sub(info.ModTime()) > robotAnalysisDiskCacheMaxAge {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		files = append(files, item{name: name, mtime: info.ModTime()})
	}
	if len(files) <= robotAnalysisDiskCacheMaxEntries {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mtime.Equal(files[j].mtime) {
			return files[i].name < files[j].name
		}
		return files[i].mtime.Before(files[j].mtime)
	})
	for i := 0; i < len(files)-robotAnalysisDiskCacheMaxEntries; i++ {
		_ = os.Remove(filepath.Join(dir, files[i].name))
	}
}

// getRobotDiskCachedStats returns cached stats, whether XFetch suggests early refresh, and cache hit.
// The xfetchRefresh flag uses probabilistic early refresh to prevent cache stampedes:
// if true, the caller should consider recomputing in the background while still using the cached result.
func getRobotDiskCachedStats(fullKey string) (stats *GraphStats, xfetchRefresh bool, cacheHit bool) {
	if !robotDiskCacheEnabled() {
		return nil, false, false
	}

	dir, err := robotAnalysisDiskCacheDir(false)
	if err != nil {
		return nil, false, false
	}
	path := filepath.Join(dir, robotAnalysisEntryFileName(fullKey))

	// Lock-free read: entries are written atomically (temp file + rename), so
	// a reader sees either the old complete entry or the new complete entry,
	// never a torn write. Decoding touches exactly one entry — the whole point
	// of the v3 layout (issue #192): the v2 single file made every lookup
	// decode every entry for every repo this user runs bv in.
	f, err := os.Open(path)
	if err != nil {
		return nil, false, false
	}
	info, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return nil, false, false
	}
	if info.Size() > robotAnalysisDiskCacheMaxEntrySize {
		_ = f.Close()
		removeRobotDiskCacheEntryIfSame(path, info)
		return nil, false, false
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, robotAnalysisDiskCacheMaxEntrySize+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return nil, false, false
	}
	if len(raw) > robotAnalysisDiskCacheMaxEntrySize {
		removeRobotDiskCacheEntryIfSame(path, info)
		return nil, false, false
	}

	var entry robotAnalysisDiskCacheEntry
	decodeErr := json.Unmarshal(raw, &entry)
	var resultErr error
	if decodeErr == nil {
		resultErr = entry.Result.validateForCacheHit()
	}
	now := time.Now().UTC()
	if decodeErr != nil ||
		entry.Version != robotAnalysisDiskCacheVersion ||
		entry.Key != fullKey ||
		entry.DataHash+"|"+entry.ConfigHash != fullKey ||
		ComputeConfigHash(&entry.Result.Config) != entry.ConfigHash ||
		entry.CreatedAt.IsZero() || entry.CreatedAt.After(now) ||
		entry.ComputeDuration < 0 || entry.ComputeDuration > robotAnalysisDiskCacheMaxAge ||
		resultErr != nil {
		// Corrupt, foreign, or filename-collision content can never satisfy a
		// lookup; drop the regenerable file so it stops costing reads.
		removeRobotDiskCacheEntryIfSame(path, info)
		return nil, false, false
	}

	if now.Sub(entry.CreatedAt) > robotAnalysisDiskCacheMaxAge {
		removeRobotDiskCacheEntryIfSame(path, info)
		return nil, false, false
	}

	// Mtime-based staleness check: if the .beads/ directory (or any file
	// inside it) has been modified after this cache entry was created, the
	// bead data may have changed (e.g., a bead was closed in br). In that
	// case the cached GraphStats are stale and must be recomputed.
	dirMtime := beadsDirModTime()
	if !dirMtime.IsZero() && dirMtime.After(entry.CreatedAt) {
		removeRobotDiskCacheEntryIfSame(path, info)
		return nil, false, false
	}

	// XFetch: probabilistically suggest early refresh to prevent cache stampedes.
	// Do not refresh again before at least one prior compute-duration window has
	// elapsed, otherwise newly written entries can get selected immediately. A
	// reproducible run must never turn the same warm hit into a random recompute.
	shouldXFetchRefresh := !entry.Result.Config.RunToCompletion &&
		entry.ComputeDuration > 0 &&
		!now.Before(entry.CreatedAt.Add(entry.ComputeDuration)) &&
		xfetch.ShouldRefresh(entry.CreatedAt, entry.ComputeDuration, 1.0, now)

	return entry.Result.toGraphStats(), shouldXFetchRefresh, true
}

// removeRobotDiskCacheEntryIfSame reaps only the inode this reader inspected.
// Cleanup opportunistically takes the same directory lock as writers so the
// identity check and Remove are one critical section. If a writer is active,
// cleanup is skipped: deleting a regenerable corrupt entry is never important
// enough to block the read/miss path behind a slow fsync or prune.
func removeRobotDiskCacheEntryIfSame(path string, openedInfo os.FileInfo) {
	if openedInfo == nil {
		return
	}
	lockPath := filepath.Join(filepath.Dir(path), ".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	locked, err := tryLockFile(lock)
	if err != nil || !locked {
		return
	}
	defer func() { _ = unlockFile(lock) }()

	currentInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(openedInfo, currentInfo) {
		return
	}
	_ = os.Remove(path)
}

func putRobotDiskCachedStats(fullKey, dataHash, configHash string, stats *GraphStats, computeDuration time.Duration) {
	if !robotDiskCacheEnabled() {
		return
	}
	if !graphStatsReadyForCache(stats) {
		return
	}

	stats.mu.RLock()
	blob := graphStatsCacheBlob{
		OutDegree:        stats.OutDegree,
		InDegree:         stats.InDegree,
		TopologicalOrder: stats.TopologicalOrder,
		Density:          stats.Density,
		NodeCount:        stats.NodeCount,
		EdgeCount:        stats.EdgeCount,
		Config:           stats.Config,

		PageRank:          stats.pageRank,
		Betweenness:       stats.betweenness,
		Eigenvector:       stats.eigenvector,
		Hubs:              stats.hubs,
		Authorities:       stats.authorities,
		CriticalPathScore: stats.criticalPathScore,
		CoreNumber:        stats.coreNumber,
		Slack:             stats.slack,
		Cycles:            stats.cycles,
		Status:            stats.status,
	}
	if stats.articulation != nil {
		blob.Articulation = make([]string, 0, len(stats.articulation))
		for id := range stats.articulation {
			blob.Articulation = append(blob.Articulation, id)
		}
		sort.Strings(blob.Articulation)
	}
	stats.mu.RUnlock()

	now := time.Now().UTC()
	entry := robotAnalysisDiskCacheEntry{
		Version:         robotAnalysisDiskCacheVersion,
		Key:             fullKey,
		CreatedAt:       now,
		DataHash:        dataHash,
		ConfigHash:      configHash,
		ComputeDuration: computeDuration,
		Result:          blob,
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > robotAnalysisDiskCacheMaxEntrySize {
		return
	}

	dir, err := robotAnalysisDiskCacheDir(true)
	if err != nil {
		return
	}

	// Serialize writers and evictions across processes with a directory-level
	// lock file; readers stay lock-free (atomic rename gives them a complete
	// entry either way). This bounds the cost of a store to this one entry —
	// under v2 every store rewrote and fsynced the entire multi-entry file.
	lockPath := filepath.Join(dir, ".lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer lf.Close()
	if err := lockFile(lf); err != nil {
		return
	}
	defer func() { _ = unlockFile(lf) }()

	tmp, err := os.CreateTemp(dir, "tmp-")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(raw)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return
	}
	final := filepath.Join(dir, robotAnalysisEntryFileName(fullKey))
	if err := os.Rename(tmpName, final); err != nil {
		// Windows cannot rename onto a file another process holds open; retry
		// once after removing the destination, then give up — the cache is
		// best-effort.
		_ = os.Remove(final)
		if err := os.Rename(tmpName, final); err != nil {
			_ = os.Remove(tmpName)
			return
		}
	}

	pruneAndEvictRobotDiskCacheDir(dir, time.Now())

	// Retire the pre-v3 single-file layout once a v3 entry exists, so the
	// obsolete multi-MB blob doesn't linger in the cache dir indefinitely.
	_ = os.Remove(filepath.Join(filepath.Dir(dir), robotAnalysisLegacyCacheFileName))
}
