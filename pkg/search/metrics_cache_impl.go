package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"golang.org/x/sync/singleflight"
)

const defaultPageRank = 0.5
const metricsCacheRefreshFlightKey = "refresh"

// metricsCache is the default MetricsCache implementation.
type metricsCache struct {
	mu              sync.RWMutex
	sf              singleflight.Group // Prevents cache stampede on concurrent refresh
	refreshGen      atomic.Uint64
	metrics         map[string]IssueMetrics
	dataHash        string
	maxBlockerCount int
	loader          MetricsLoader
}

// NewMetricsCache creates a MetricsCache backed by the provided loader.
func NewMetricsCache(loader MetricsLoader) MetricsCache {
	return &metricsCache{
		metrics: make(map[string]IssueMetrics),
		loader:  loader,
	}
}

// AnalyzerMetricsLoader loads metrics from the analysis engine.
type AnalyzerMetricsLoader struct {
	issues    []model.Issue
	readiness *model.ReadinessIndex
	cache     *analysis.Cache
	config    *analysis.AnalysisConfig

	hashMu          sync.Mutex
	now             time.Time
	hasNow          bool
	baseComputed    bool
	baseHash        string
	deferIndices    []int
	cachedHash      string
	cachedDeferMask uint64
	cachedDeferBits []byte
	scratchBits     []byte
}

// NewAnalyzerMetricsLoader creates a loader that derives metrics from issues.
func NewAnalyzerMetricsLoader(issues []model.Issue) *AnalyzerMetricsLoader {
	return NewAnalyzerMetricsLoaderWithReadiness(issues, nil)
}

// NewAnalyzerMetricsLoaderWithReadiness creates a loader that derives metrics
// from issues while reusing a prebuilt ReadinessIndex (preserving TombstoneIDs
// and external cross-scope blockers rather than rebuilding from l.issues alone).
func NewAnalyzerMetricsLoaderWithReadiness(issues []model.Issue, readiness *model.ReadinessIndex) *AnalyzerMetricsLoader {
	return &AnalyzerMetricsLoader{
		issues:    cloneMetricsLoaderIssues(issues),
		readiness: readiness,
	}
}

// WithCache configures a custom analysis cache for this loader.
func (l *AnalyzerMetricsLoader) WithCache(cache *analysis.Cache) *AnalyzerMetricsLoader {
	l.cache = cache
	return l
}

// WithConfig overrides the analysis configuration.
func (l *AnalyzerMetricsLoader) WithConfig(config *analysis.AnalysisConfig) *AnalyzerMetricsLoader {
	l.config = config
	return l
}

// WithNow overrides the reference clock used to evaluate future defer_until timestamps.
func (l *AnalyzerMetricsLoader) WithNow(now time.Time) *AnalyzerMetricsLoader {
	if l == nil {
		return nil
	}
	l.hashMu.Lock()
	l.now = now
	l.hasNow = !now.IsZero()
	l.cachedHash = ""
	l.hashMu.Unlock()
	return l
}

func (l *AnalyzerMetricsLoader) referenceNowLocked() time.Time {
	if l != nil && l.hasNow {
		return l.now
	}
	if raw := env.SourceDateEpoch.Get(); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func (l *AnalyzerMetricsLoader) referenceNow() time.Time {
	if l == nil {
		return time.Now().UTC()
	}
	l.hashMu.Lock()
	defer l.hashMu.Unlock()
	return l.referenceNowLocked()
}

// LoadMetrics computes metrics for all issues using the analysis engine.
func (l *AnalyzerMetricsLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	if len(l.issues) == 0 {
		return map[string]IssueMetrics{}, nil
	}

	metrics, _ := l.computeMetricsAndHash(false)
	return metrics, nil
}

// ComputeDataHash returns the data hash for the loader's issue set.
func (l *AnalyzerMetricsLoader) ComputeDataHash() (string, error) {
	return l.snapshotHash(), nil
}

func (l *AnalyzerMetricsLoader) ensureBaseLocked() {
	if l.baseComputed {
		return
	}
	rawHash := analysis.ComputeDataHash(l.issues)
	for i := range l.issues {
		if l.issues[i].DeferUntil != nil {
			l.deferIndices = append(l.deferIndices, i)
		}
	}
	if len(l.deferIndices) > 1 {
		sort.Slice(l.deferIndices, func(i, j int) bool {
			return l.issues[l.deferIndices[i]].ID < l.issues[l.deferIndices[j]].ID
		})
	}
	if l.readiness == nil || len(l.issues) == 0 {
		l.baseHash = rawHash
	} else {
		ids := make([]string, len(l.issues))
		ephemeralByID := make(map[string]bool, len(l.issues))
		for i := range l.issues {
			ids[i] = l.issues[i].ID
			if l.issues[i].Ephemeral {
				ephemeralByID[l.issues[i].ID] = true
			}
		}
		sort.Strings(ids)
		h := sha256.New()
		h.Write([]byte(rawHash))
		for _, id := range ids {
			h.Write([]byte{0})
			h.Write([]byte(id))
			h.Write([]byte{':'})
			h.Write([]byte(string(l.readiness.DependencyState(id))))
			if ephemeralByID[id] {
				h.Write([]byte(":eph"))
			}
		}
		l.baseHash = "v" + hex.EncodeToString(h.Sum(nil)[:8])
	}
	l.baseComputed = true
}

func (l *AnalyzerMetricsLoader) snapshotHash() string {
	return l.snapshotHashAt(time.Time{}, false)
}

func (l *AnalyzerMetricsLoader) snapshotHashAt(refNow time.Time, hasRefNow bool) string {
	if l == nil {
		return analysis.ComputeDataHash(nil)
	}
	l.hashMu.Lock()
	defer l.hashMu.Unlock()

	l.ensureBaseLocked()
	if len(l.deferIndices) == 0 {
		if l.cachedHash == "" {
			l.cachedHash = l.baseHash
		}
		return l.cachedHash
	}

	now := refNow
	if !hasRefNow {
		now = l.referenceNowLocked()
	}

	if len(l.deferIndices) <= 64 {
		var mask uint64
		for bit, idx := range l.deferIndices {
			if l.issues[idx].IsDeferredAt(now) {
				mask |= 1 << uint(bit)
			}
		}
		if l.cachedHash != "" && mask == l.cachedDeferMask {
			return l.cachedHash
		}
		l.cachedDeferMask = mask
	} else {
		if cap(l.scratchBits) < len(l.deferIndices) {
			l.scratchBits = make([]byte, len(l.deferIndices))
		} else {
			l.scratchBits = l.scratchBits[:len(l.deferIndices)]
		}
		for k, idx := range l.deferIndices {
			if l.issues[idx].IsDeferredAt(now) {
				l.scratchBits[k] = '1'
			} else {
				l.scratchBits[k] = '0'
			}
		}
		if l.cachedHash != "" && bytes.Equal(l.scratchBits, l.cachedDeferBits) {
			return l.cachedHash
		}
		if cap(l.cachedDeferBits) < len(l.scratchBits) {
			l.cachedDeferBits = make([]byte, len(l.scratchBits))
		} else {
			l.cachedDeferBits = l.cachedDeferBits[:len(l.scratchBits)]
		}
		copy(l.cachedDeferBits, l.scratchBits)
	}

	h := sha256.New()
	h.Write([]byte(l.baseHash))
	for _, idx := range l.deferIndices {
		h.Write([]byte{0, 'd', ':'})
		h.Write([]byte(l.issues[idx].ID))
		if l.issues[idx].IsDeferredAt(now) {
			h.Write([]byte{':', '1'})
		} else {
			h.Write([]byte{':', '0'})
		}
	}
	l.cachedHash = "v" + hex.EncodeToString(h.Sum(nil)[:8])
	return l.cachedHash
}

// LoadMetricsWithHash computes metrics and hash atomically from the same issue set.
// This prevents TOCTOU race conditions where the underlying data could change
// between separate LoadMetrics and ComputeDataHash calls.
func (l *AnalyzerMetricsLoader) LoadMetricsWithHash() (map[string]IssueMetrics, string, error) {
	if len(l.issues) == 0 {
		return map[string]IssueMetrics{}, l.snapshotHash(), nil
	}

	metrics, hash := l.computeMetricsAndHash(true)
	return metrics, hash, nil
}

func (l *AnalyzerMetricsLoader) computeMetricsAndHash(includeHash bool) (map[string]IssueMetrics, string) {
	cached := analysis.NewCachedAnalyzer(l.issues, l.cache)
	if l.config != nil {
		cached.SetConfig(l.config)
	}

	stats := cached.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	pageRank := stats.PageRank()
	hasPageRank := len(pageRank) > 0
	maxActivePR := 0.0
	maxAnyPR := 0.0
	if hasPageRank {
		for _, issue := range l.issues {
			pr := pageRank[issue.ID]
			if pr > maxAnyPR {
				maxAnyPR = pr
			}
			norm := issue.Status.Normalize()
			if norm != model.StatusClosed && !norm.IsTombstone() && pr > maxActivePR {
				maxActivePR = pr
			}
		}
	}
	normDenom := maxActivePR
	if normDenom <= 0 {
		normDenom = maxAnyPR
	}

	readiness := l.readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(l.issues)
	}
	refNow := l.referenceNow()
	metrics := make(map[string]IssueMetrics, len(l.issues))
	for _, issue := range l.issues {
		pr := defaultPageRank
		if hasPageRank {
			rawPR, ok := pageRank[issue.ID]
			switch {
			case !ok:
				pr = defaultPageRank
			case normDenom > 0:
				pr = rawPR / normDenom
				if pr > 1.0 {
					pr = 1.0
				}
			default:
				pr = 0
			}
		}
		normStatus := issue.Status.Normalize()
		effectiveStatus := issue.Status
		if normStatus != model.StatusClosed && !normStatus.IsTombstone() {
			switch {
			case issue.IsTemplate || issue.Pinned || issue.Ephemeral || normStatus == model.StatusPinned:
				effectiveStatus = model.StatusDeferred
			case (normStatus == model.StatusOpen || normStatus == model.StatusInProgress) && issue.IsDeferredAt(refNow):
				effectiveStatus = model.StatusDeferred
			case (normStatus == model.StatusOpen || normStatus == model.StatusInProgress) &&
				readiness.DependencyState(issue.ID) != model.DependenciesSatisfied:
				effectiveStatus = model.StatusBlocked
			}
		}
		metrics[issue.ID] = IssueMetrics{
			IssueID:      issue.ID,
			PageRank:     pr,
			Status:       string(effectiveStatus),
			Priority:     issue.Priority,
			BlockerCount: cached.OpenBlockerCount(issue.ID),
			UpdatedAt:    issue.UpdatedAt,
		}
	}

	var hash string
	if includeHash {
		hash = l.snapshotHashAt(refNow, true)
	}
	return metrics, hash
}

// Get returns metrics for an issue, computing/loading if needed.
func (c *metricsCache) Get(issueID string) (IssueMetrics, bool) {
	if issueID == "" {
		return defaultIssueMetrics(issueID), false
	}

	if err := c.ensureFresh(); err != nil {
		c.mu.RLock()
		metric, ok := c.metrics[issueID]
		c.mu.RUnlock()
		if ok {
			return metric, true
		}
		return defaultIssueMetrics(issueID), false
	}

	c.mu.RLock()
	metric, ok := c.metrics[issueID]
	c.mu.RUnlock()
	if !ok {
		return defaultIssueMetrics(issueID), false
	}
	return metric, true
}

// GetBatch returns metrics for multiple issues efficiently.
func (c *metricsCache) GetBatch(issueIDs []string) map[string]IssueMetrics {
	results := make(map[string]IssueMetrics, len(issueIDs))
	if len(issueIDs) == 0 {
		return results
	}

	if err := c.ensureFresh(); err != nil {
		c.mu.RLock()
		for _, id := range issueIDs {
			metric, ok := c.metrics[id]
			if !ok {
				metric = defaultIssueMetrics(id)
			}
			results[id] = metric
		}
		c.mu.RUnlock()
		return results
	}

	c.mu.RLock()
	for _, id := range issueIDs {
		metric, ok := c.metrics[id]
		if !ok {
			metric = defaultIssueMetrics(id)
		}
		results[id] = metric
	}
	c.mu.RUnlock()

	return results
}

// Refresh recomputes the cache from source data.
// When the loader implements MetricsLoaderAtomic, metrics and hash are loaded
// together to prevent TOCTOU race conditions.
func (c *metricsCache) Refresh() error {
	if c.loader == nil {
		return fmt.Errorf("metrics loader is nil")
	}

	var metrics map[string]IssueMetrics
	var hash string
	var err error

	// Prefer atomic load to prevent TOCTOU race between LoadMetrics and ComputeDataHash
	if atomic, ok := c.loader.(MetricsLoaderAtomic); ok {
		metrics, hash, err = atomic.LoadMetricsWithHash()
		if err != nil {
			return err
		}
	} else {
		// Fallback for simple loaders (e.g., test stubs)
		metrics, err = c.loader.LoadMetrics()
		if err != nil {
			return err
		}
		hash, err = c.loader.ComputeDataHash()
		if err != nil {
			return err
		}
	}

	copied := make(map[string]IssueMetrics, len(metrics))
	maxBlocker := 0
	for id, metric := range metrics {
		copied[id] = metric
		if metric.BlockerCount > maxBlocker {
			maxBlocker = metric.BlockerCount
		}
	}

	c.mu.Lock()
	c.metrics = copied
	c.dataHash = hash
	c.maxBlockerCount = maxBlocker
	c.refreshGen.Add(1)
	c.mu.Unlock()

	return nil
}

// DataHash returns the hash of source data for cache validation.
func (c *metricsCache) DataHash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dataHash
}

// MaxBlockerCount returns the maximum blocker count for normalization.
func (c *metricsCache) MaxBlockerCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxBlockerCount
}

func (c *metricsCache) ensureFresh() error {
	if c.loader == nil {
		return fmt.Errorf("metrics loader is nil")
	}

	genBefore := c.refreshGen.Load()
	hash, err := c.loader.ComputeDataHash()
	if err != nil {
		return err
	}

	if c.isFreshForHash(hash) {
		return nil
	}

	// Use singleflight to prevent cache stampede. Key is constant (not
	// hash-suffixed) so concurrent refreshes coalesce even when callers
	// observed different source hashes mid-drift — otherwise two workers that
	// each compute a slightly different hash trigger two parallel Refresh()
	// calls, defeating the stampede guard. If another caller completed a
	// Refresh() while this caller was queued before singleflight, re-check the
	// loader's latest hash before triggering a redundant refresh.
	_, err, _ = c.sf.Do(metricsCacheRefreshFlightKey, func() (interface{}, error) {
		if c.isFreshForHash(hash) {
			return nil, nil
		}
		if c.refreshGen.Load() != genBefore {
			latestHash, hashErr := c.loader.ComputeDataHash()
			if hashErr != nil {
				return nil, hashErr
			}
			if c.isFreshForHash(latestHash) {
				return nil, nil
			}
		}
		return nil, c.Refresh()
	})
	return err
}

func defaultIssueMetrics(issueID string) IssueMetrics {
	return IssueMetrics{
		IssueID:      issueID,
		PageRank:     defaultPageRank,
		Priority:     2,
		BlockerCount: 0,
	}
}

func cloneMetricsLoaderIssues(issues []model.Issue) []model.Issue {
	if len(issues) == 0 {
		return nil
	}

	clones := make([]model.Issue, len(issues))
	for i := range issues {
		clones[i] = issues[i].Clone()
	}
	return clones
}

func (c *metricsCache) isFreshForHash(hash string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metrics != nil && c.dataHash != "" && c.dataHash == hash
}
