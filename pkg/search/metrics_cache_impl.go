package search

import (
	"context"
	"fmt"
	"sync"

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
	issues []model.Issue
	cache  *analysis.Cache
	config *analysis.AnalysisConfig

	// issues is a private deep clone and never changes after construction.
	// Hash it once, including for a zero-value/empty loader. Mutable loader
	// implementations still recompute their hash through ensureFresh.
	hashOnce sync.Once
	dataHash string
}

// NewAnalyzerMetricsLoader creates a loader that derives metrics from issues.
func NewAnalyzerMetricsLoader(issues []model.Issue) *AnalyzerMetricsLoader {
	return &AnalyzerMetricsLoader{issues: cloneMetricsLoaderIssues(issues)}
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

// LoadMetrics computes metrics for all issues using the analysis engine.
func (l *AnalyzerMetricsLoader) LoadMetrics() (map[string]IssueMetrics, error) {
	if len(l.issues) == 0 {
		return map[string]IssueMetrics{}, nil
	}

	cached := analysis.NewCachedAnalyzer(l.issues, l.cache)
	if l.config != nil {
		cached.SetConfig(l.config)
	}

	stats := cached.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	pageRank := stats.PageRank()
	metrics := make(map[string]IssueMetrics, len(l.issues))

	for _, issue := range l.issues {
		pr, ok := pageRank[issue.ID]
		if !ok {
			pr = defaultPageRank
		}
		metrics[issue.ID] = IssueMetrics{
			IssueID:      issue.ID,
			PageRank:     pr,
			Status:       string(issue.Status),
			Priority:     issue.Priority,
			BlockerCount: stats.InDegree[issue.ID],
			UpdatedAt:    issue.UpdatedAt,
		}
	}

	return metrics, nil
}

// ComputeDataHash returns the data hash for the loader's issue set.
func (l *AnalyzerMetricsLoader) ComputeDataHash() (string, error) {
	return l.snapshotHash(), nil
}

func (l *AnalyzerMetricsLoader) snapshotHash() string {
	l.hashOnce.Do(func() {
		l.dataHash = analysis.ComputeDataHash(l.issues)
	})
	return l.dataHash
}

// LoadMetricsWithHash computes metrics and hash atomically from the same issue set.
// This prevents TOCTOU race conditions where the underlying data could change
// between separate LoadMetrics and ComputeDataHash calls.
func (l *AnalyzerMetricsLoader) LoadMetricsWithHash() (map[string]IssueMetrics, string, error) {
	if len(l.issues) == 0 {
		return map[string]IssueMetrics{}, l.snapshotHash(), nil
	}

	cached := analysis.NewCachedAnalyzer(l.issues, l.cache)
	if l.config != nil {
		cached.SetConfig(l.config)
	}

	stats := cached.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()

	pageRank := stats.PageRank()
	metrics := make(map[string]IssueMetrics, len(l.issues))

	for _, issue := range l.issues {
		pr, ok := pageRank[issue.ID]
		if !ok {
			pr = defaultPageRank
		}
		metrics[issue.ID] = IssueMetrics{
			IssueID:      issue.ID,
			PageRank:     pr,
			Status:       string(issue.Status),
			Priority:     issue.Priority,
			BlockerCount: stats.InDegree[issue.ID],
			UpdatedAt:    issue.UpdatedAt,
		}
	}

	// Compute hash from the exact same issues used for metrics
	hash := l.snapshotHash()

	return metrics, hash, nil
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
	// calls, defeating the stampede guard. After Refresh() runs once, every
	// coalesced caller observes the latest snapshot in the cache.
	_, err, _ = c.sf.Do(metricsCacheRefreshFlightKey, func() (interface{}, error) {
		if c.isFreshForHash(hash) {
			return nil, nil
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
