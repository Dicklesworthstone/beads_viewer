// Package correlation provides caching for history reports to avoid expensive git operations.
package correlation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"github.com/Dicklesworthstone/beads_viewer/pkg/xfetch"
	"golang.org/x/sync/singleflight"
)

// CacheKey uniquely identifies a cached history report
type CacheKey struct {
	HeadSHA   string // Current git HEAD
	BeadsHash string // Hash of beads content
	Options   string // Serialized options
	Feedback  string // FeedbackStore fingerprint ("" when no store is attached)
}

// String returns a string representation of the cache key
func (k CacheKey) String() string {
	s := k.HeadSHA + ":" + k.BeadsHash + ":" + k.Options
	if k.Feedback != "" {
		s += ":" + k.Feedback
	}
	return s
}

// CacheEntry holds a cached report with metadata
type CacheEntry struct {
	Key             CacheKey
	Report          *HistoryReport
	CreatedAt       time.Time
	AccessedAt      time.Time
	ComputeDuration time.Duration // How long it took to generate this report
}

// HistoryCache provides thread-safe caching of history reports with LRU eviction
type HistoryCache struct {
	mu       sync.RWMutex
	entries  map[string]*CacheEntry
	order    []string // LRU order (oldest first)
	maxAge   time.Duration
	maxSize  int
	repoPath string
}

// DefaultCacheMaxAge is the default maximum age for cache entries
const DefaultCacheMaxAge = 5 * time.Minute

// DefaultCacheMaxSize is the default maximum number of cache entries
const DefaultCacheMaxSize = 10

var correlationCacheLogf = log.Printf

// NewHistoryCache creates a new cache with default settings
func NewHistoryCache(repoPath string) *HistoryCache {
	return &HistoryCache{
		entries:  make(map[string]*CacheEntry),
		order:    make([]string, 0, DefaultCacheMaxSize),
		maxAge:   DefaultCacheMaxAge,
		maxSize:  DefaultCacheMaxSize,
		repoPath: repoPath,
	}
}

// NewHistoryCacheWithOptions creates a cache with custom settings
func NewHistoryCacheWithOptions(repoPath string, maxAge time.Duration, maxSize int) *HistoryCache {
	if maxAge <= 0 {
		maxAge = DefaultCacheMaxAge
	}
	if maxSize <= 0 {
		maxSize = DefaultCacheMaxSize
	}
	return &HistoryCache{
		entries:  make(map[string]*CacheEntry),
		order:    make([]string, 0, maxSize),
		maxAge:   maxAge,
		maxSize:  maxSize,
		repoPath: repoPath,
	}
}

// Get retrieves a cached report if available and valid
func (c *HistoryCache) Get(key CacheKey) (*HistoryReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keyStr := key.String()
	entry, ok := c.entries[keyStr]
	if !ok {
		return nil, false
	}

	// Check if entry is expired
	if time.Since(entry.CreatedAt) > c.maxAge {
		c.removeEntryLocked(keyStr)
		return nil, false
	}

	// Update access time and move to end of LRU order
	entry.AccessedAt = time.Now()
	c.moveToEndLocked(keyStr)

	return entry.Report, true
}

// Put stores a report in the cache
func (c *HistoryCache) Put(key CacheKey, report *HistoryReport) {
	c.PutWithDuration(key, report, 0)
}

// PutWithDuration stores a report with its compute duration for XFetch
func (c *HistoryCache) PutWithDuration(key CacheKey, report *HistoryReport, computeDuration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keyStr := key.String()
	now := time.Now()

	// If key already exists, update it
	if _, exists := c.entries[keyStr]; exists {
		c.entries[keyStr].Report = report
		c.entries[keyStr].CreatedAt = now
		c.entries[keyStr].AccessedAt = now
		c.entries[keyStr].ComputeDuration = computeDuration
		c.moveToEndLocked(keyStr)
		return
	}

	// Evict oldest entry if at capacity
	if len(c.entries) >= c.maxSize {
		c.evictOldestLocked()
	}

	// Add new entry
	c.entries[keyStr] = &CacheEntry{
		Key:             key,
		Report:          report,
		CreatedAt:       now,
		AccessedAt:      now,
		ComputeDuration: computeDuration,
	}
	c.order = append(c.order, keyStr)
}

// GetWithMeta retrieves a cached report with its metadata for XFetch decisions
func (c *HistoryCache) GetWithMeta(key CacheKey) (*HistoryReport, time.Time, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keyStr := key.String()
	entry, ok := c.entries[keyStr]
	if !ok {
		return nil, time.Time{}, 0, false
	}

	// Check if entry is expired
	if time.Since(entry.CreatedAt) > c.maxAge {
		c.removeEntryLocked(keyStr)
		return nil, time.Time{}, 0, false
	}

	// Update access time and LRU order
	entry.AccessedAt = time.Now()
	c.moveToEndLocked(keyStr)

	return entry.Report, entry.CreatedAt, entry.ComputeDuration, true
}

// Invalidate removes all entries (called when HEAD or beads change)
func (c *HistoryCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*CacheEntry)
	c.order = make([]string, 0, c.maxSize)
}

// InvalidateForHead removes entries that don't match the current HEAD
func (c *HistoryCache) InvalidateForHead(currentHead string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var toRemove []string
	for keyStr, entry := range c.entries {
		if entry.Key.HeadSHA != currentHead {
			toRemove = append(toRemove, keyStr)
		}
	}

	for _, keyStr := range toRemove {
		c.removeEntryLocked(keyStr)
	}
}

// Size returns the number of cached entries
func (c *HistoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Stats returns cache statistics
func (c *HistoryCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := CacheStats{
		Size:    len(c.entries),
		MaxSize: c.maxSize,
		MaxAge:  c.maxAge,
	}

	var oldestTime, newestTime time.Time
	for _, entry := range c.entries {
		if oldestTime.IsZero() || entry.CreatedAt.Before(oldestTime) {
			oldestTime = entry.CreatedAt
		}
		if newestTime.IsZero() || entry.CreatedAt.After(newestTime) {
			newestTime = entry.CreatedAt
		}
	}

	if !oldestTime.IsZero() {
		stats.OldestEntry = &oldestTime
	}
	if !newestTime.IsZero() {
		stats.NewestEntry = &newestTime
	}

	return stats
}

// CacheStats provides information about cache state
type CacheStats struct {
	Size        int
	MaxSize     int
	MaxAge      time.Duration
	OldestEntry *time.Time
	NewestEntry *time.Time
}

// removeEntryLocked removes an entry (caller must hold lock)
func (c *HistoryCache) removeEntryLocked(keyStr string) {
	delete(c.entries, keyStr)

	// Remove from order slice
	for i, k := range c.order {
		if k == keyStr {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// moveToEndLocked moves an entry to the end of the LRU order (caller must hold lock)
func (c *HistoryCache) moveToEndLocked(keyStr string) {
	// Find and remove from current position
	for i, k := range c.order {
		if k == keyStr {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	// Add to end
	c.order = append(c.order, keyStr)
}

// evictOldestLocked removes the oldest entry (caller must hold lock)
func (c *HistoryCache) evictOldestLocked() {
	if len(c.order) == 0 {
		return
	}

	oldestKey := c.order[0]
	c.order = c.order[1:]
	delete(c.entries, oldestKey)
}

// BuildCacheKey creates a cache key for the given parameters
func BuildCacheKey(repoPath string, beads []BeadInfo, opts CorrelatorOptions) (CacheKey, error) {
	headSHA, err := getGitHead(repoPath)
	if err != nil {
		return CacheKey{}, err
	}

	beadsHash := hashBeads(beads)
	optsHash := hashOptions(opts)

	return CacheKey{
		HeadSHA:   headSHA,
		BeadsHash: beadsHash,
		Options:   optsHash,
	}, nil
}

// getGitHead returns the current HEAD SHA
func getGitHead(repoPath string) (string, error) {
	return getGitHeadContext(context.Background(), repoPath)
}

// getGitHeadContext returns the current HEAD SHA, bounding the git subprocess
// by ctx (#166). A nil ctx means context.Background().
func getGitHeadContext(ctx context.Context, repoPath string) (string, error) {
	cmd := gitCommand(ctx, "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// hashBeads creates a hash of all bead fields that are embedded in reports.
func hashBeads(beads []BeadInfo) string {
	if len(beads) == 0 {
		return hex.EncodeToString(sha256.New().Sum(nil))[:12]
	}

	entries := make([]string, 0, len(beads))
	for _, b := range beads {
		entries = append(entries, b.ID+"\x00"+b.Title+"\x00"+b.Status)
	}
	sort.Strings(entries)

	h := sha256.New()
	for _, entry := range entries {
		h.Write([]byte(entry))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// hashOptions creates a hash of correlator options
func hashOptions(opts CorrelatorOptions) string {
	// Serialize options to JSON for consistent hashing
	data, err := json.Marshal(struct {
		Revision        string
		BeadID          string
		Since           *time.Time
		Until           *time.Time
		Limit           int
		CausalityBeadID string
	}{
		Revision:        opts.Revision,
		BeadID:          opts.BeadID,
		Since:           opts.Since,
		Until:           opts.Until,
		Limit:           opts.Limit,
		CausalityBeadID: opts.CausalityBeadID,
	})
	if err != nil {
		return "default"
	}

	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// cacheOptionsHash adds the actual matcher policy to extraction options. Both
// persistent cache layers and in-memory wrappers need this identity: changing
// --id-pattern changes the committed references without changing HEAD or beads.
// Using the matcher's snapshot also keeps later global pattern registration
// from changing the identity of an already-constructed correlator.
func (c *Correlator) cacheOptionsHash(opts CorrelatorOptions) string {
	return hashOptions(opts) + ":matcher:" + c.explicit.policyFingerprint()
}

// CachedCorrelator wraps a Correlator with caching support
type CachedCorrelator struct {
	correlator       *Correlator
	cache            *HistoryCache
	sf               singleflight.Group
	generateReportFn func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error)
	shouldRefreshFn  func(time.Time, time.Duration, float64, time.Time) bool
	hits             int64 // Cache hit count (for stats)
	misses           int64 // Cache miss count (for stats)
	mu               sync.Mutex
}

// NewCachedCorrelator creates a correlator with caching enabled.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewCachedCorrelator(repoPath string, beadsFilePath ...string) *CachedCorrelator {
	correlator := NewCorrelator(repoPath, beadsFilePath...)
	return &CachedCorrelator{
		correlator:       correlator,
		cache:            NewHistoryCache(repoPath),
		generateReportFn: correlator.GenerateReport,
		shouldRefreshFn:  xfetch.ShouldRefresh,
	}
}

// NewCachedCorrelatorWithOptions creates a correlator with custom cache settings.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewCachedCorrelatorWithOptions(repoPath string, maxAge time.Duration, maxSize int, beadsFilePath ...string) *CachedCorrelator {
	correlator := NewCorrelator(repoPath, beadsFilePath...)
	return &CachedCorrelator{
		correlator:       correlator,
		cache:            NewHistoryCacheWithOptions(repoPath, maxAge, maxSize),
		generateReportFn: correlator.GenerateReport,
		shouldRefreshFn:  xfetch.ShouldRefresh,
	}
}

// WithFeedbackStore attaches the confirm/reject store to the underlying
// correlator; the store's fingerprint becomes part of the in-memory cache key
// so a new decision is never served a stale pre-feedback report.
func (c *CachedCorrelator) WithFeedbackStore(store *FeedbackStore) *CachedCorrelator {
	c.correlator.WithFeedbackStore(store)
	return c
}

// buildKey extends BuildCacheKey with the wrapped correlator's matcher policy
// and feedback fingerprint. Both are inputs to the assembled report.
func (c *CachedCorrelator) buildKey(beads []BeadInfo, opts CorrelatorOptions) (CacheKey, error) {
	key, err := BuildCacheKey(c.cache.repoPath, beads, opts)
	if err != nil {
		return CacheKey{}, err
	}
	key.Options = c.correlator.cacheOptionsHash(opts)
	key.Feedback = c.correlator.feedbackFingerprint()
	return key, nil
}

// GenerateReport generates a history report, using cache when possible
func (c *CachedCorrelator) GenerateReport(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	// Build cache key
	key, err := c.buildKey(beads, opts)
	if err != nil {
		// If we can't build a cache key, fall back to uncached
		return c.generate(beads, opts)
	}

	// Check cache with XFetch probabilistic early refresh
	if report, createdAt, computeDuration, ok := c.cache.GetWithMeta(key); ok {
		c.recordHit()

		// XFetch: probabilistically trigger background refresh before expiry
		// This prevents all clients from refreshing simultaneously
		shouldRefresh := xfetch.ShouldRefresh
		if c.shouldRefreshFn != nil {
			shouldRefresh = c.shouldRefreshFn
		}
		if computeDuration > 0 && shouldRefresh(createdAt.Add(c.cache.maxAge), computeDuration, 1.0, time.Now()) {
			refreshBeads, refreshOpts := cloneCorrelatorInputs(beads, opts)
			// Trigger background refresh (non-blocking)
			go func() {
				_, err, shared := c.sf.Do(key.String(), func() (interface{}, error) {
					if existing, existingCreatedAt, _, ok := c.cache.GetWithMeta(key); ok && existingCreatedAt.After(createdAt) {
						return existing, nil
					}

					start := time.Now()
					freshReport, err := c.generate(refreshBeads, refreshOpts)
					if err != nil {
						return nil, err
					}
					c.cacheReportIfCurrent(key, refreshBeads, refreshOpts, freshReport, time.Since(start))
					return freshReport, nil
				})
				if err != nil {
					logCorrelationCacheSingleflightError("background refresh", key, shared, err)
				}
			}()
		}

		return report, nil
	}

	result, err, shared := c.sf.Do(key.String(), func() (interface{}, error) {
		// Another caller may have filled the cache while we waited to enter the flight.
		if report, _, _, ok := c.cache.GetWithMeta(key); ok {
			c.recordHit()
			return report, nil
		}

		c.recordMiss()

		start := time.Now()
		report, err := c.generate(beads, opts)
		if err != nil {
			return nil, err
		}

		c.cacheReportIfCurrent(key, beads, opts, report, time.Since(start))
		return report, nil
	})
	if err != nil {
		logCorrelationCacheSingleflightError("report generation", key, shared, err)
		return nil, err
	}

	report, ok := result.(*HistoryReport)
	if !ok {
		return nil, fmt.Errorf("unexpected singleflight result type %T", result)
	}

	return report, nil
}

func logCorrelationCacheSingleflightError(operation string, key CacheKey, shared bool, err error) {
	if err == nil {
		return
	}
	correlationCacheLogf(
		"correlation cache singleflight %s failed for key %s (shared=%t): %v",
		operation,
		key.String(),
		shared,
		err,
	)
}

// InvalidateCache clears all cached entries
func (c *CachedCorrelator) InvalidateCache() {
	c.cache.Invalidate()
}

// CacheStats returns cache statistics
func (c *CachedCorrelator) CacheStats() CachedCorrelatorStats {
	c.mu.Lock()
	hits := c.hits
	misses := c.misses
	c.mu.Unlock()

	cacheStats := c.cache.Stats()

	var hitRate float64
	total := hits + misses
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	return CachedCorrelatorStats{
		Hits:      hits,
		Misses:    misses,
		HitRate:   hitRate,
		CacheSize: cacheStats.Size,
		MaxSize:   cacheStats.MaxSize,
		MaxAge:    cacheStats.MaxAge,
	}
}

// CachedCorrelatorStats provides statistics about cache performance
type CachedCorrelatorStats struct {
	Hits      int64
	Misses    int64
	HitRate   float64
	CacheSize int
	MaxSize   int
	MaxAge    time.Duration
}

func (c *CachedCorrelator) generate(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	if c.generateReportFn != nil {
		return c.generateReportFn(beads, opts)
	}
	return c.correlator.GenerateReport(beads, opts)
}

func (c *CachedCorrelator) recordHit() {
	metrics.CorrelationCache.Hit()
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
}

func (c *CachedCorrelator) recordMiss() {
	metrics.CorrelationCache.Miss()
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()
}

func (c *CachedCorrelator) cacheReportIfCurrent(expectedKey CacheKey, beads []BeadInfo, opts CorrelatorOptions, report *HistoryReport, computeDuration time.Duration) bool {
	currentKey, err := c.buildKey(beads, opts)
	if err != nil {
		return false
	}
	if currentKey != expectedKey {
		return false
	}

	c.cache.PutWithDuration(expectedKey, report, computeDuration)
	return true
}

func cloneCorrelatorInputs(beads []BeadInfo, opts CorrelatorOptions) ([]BeadInfo, CorrelatorOptions) {
	clonedBeads := append([]BeadInfo(nil), beads...)
	clonedOpts := opts
	if opts.Since != nil {
		since := *opts.Since
		clonedOpts.Since = &since
	}
	if opts.Until != nil {
		until := *opts.Until
		clonedOpts.Until = &until
	}
	return clonedBeads, clonedOpts
}
