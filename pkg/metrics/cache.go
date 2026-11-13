package metrics

import (
	"runtime"
	"sync/atomic"
)

// CacheMetric tracks cache hit/miss statistics.
// All methods are thread-safe using atomic operations.
type CacheMetric struct {
	name   string
	hits   int64
	misses int64
}

// newCacheMetric creates a new cache metric with the given name.
func newCacheMetric(name string) *CacheMetric {
	return &CacheMetric{name: name}
}

// Hit records a cache hit.
func (m *CacheMetric) Hit() {
	if !enabled.Load() {
		return
	}
	atomic.AddInt64(&m.hits, 1)
}

// Miss records a cache miss.
func (m *CacheMetric) Miss() {
	if !enabled.Load() {
		return
	}
	atomic.AddInt64(&m.misses, 1)
}

// Name returns the metric name.
func (m *CacheMetric) Name() string {
	return m.name
}

// Hits returns the number of cache hits.
func (m *CacheMetric) Hits() int64 {
	return atomic.LoadInt64(&m.hits)
}

// Misses returns the number of cache misses.
func (m *CacheMetric) Misses() int64 {
	return atomic.LoadInt64(&m.misses)
}

// Total returns the total number of cache accesses.
func (m *CacheMetric) Total() int64 {
	return m.Hits() + m.Misses()
}

// HitRate returns the cache hit rate as a fraction [0.0, 1.0].
// Returns 0.0 if no accesses have been recorded.
func (m *CacheMetric) HitRate() float64 {
	hits := atomic.LoadInt64(&m.hits)
	misses := atomic.LoadInt64(&m.misses)
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// Stats returns a snapshot of cache statistics.
func (m *CacheMetric) Stats() CacheStats {
	hits := atomic.LoadInt64(&m.hits)
	misses := atomic.LoadInt64(&m.misses)
	total := hits + misses
	var hitRate float64
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}
	return CacheStats{
		Name:    m.name,
		Hits:    hits,
		Misses:  misses,
		Total:   total,
		HitRate: hitRate,
	}
}

// Reset clears all recorded cache statistics.
func (m *CacheMetric) Reset() {
	atomic.StoreInt64(&m.hits, 0)
	atomic.StoreInt64(&m.misses, 0)
}

// CacheStats holds a snapshot of cache statistics.
type CacheStats struct {
	Name    string  `json:"name"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Total   int64   `json:"total"`
	HitRate float64 `json:"hit_rate"`
}

// Global cache metrics for various caches.
// Every counter here has a producer: GraphCache is fed by the analysis
// in-memory and disk caches (pkg/analysis), CorrelationCache by the
// correlation report/artifact caches (pkg/correlation), SearchCache by the
// on-disk vector index (pkg/search), TriageCache by pkg/analysis.TriageContext.
var (
	GraphCache       = newCacheMetric("graph_cache")
	TriageCache      = newCacheMetric("triage_cache")
	SearchCache      = newCacheMetric("search_cache")
	CorrelationCache = newCacheMetric("correlation_cache")
)

// AllCacheMetrics returns all registered cache metrics.
func AllCacheMetrics() []*CacheMetric {
	return []*CacheMetric{
		GraphCache,
		TriageCache,
		SearchCache,
		CorrelationCache,
	}
}

// AllCacheStats returns stats for every registered cache metric, including
// the ones this process never consulted (hits and misses both 0): a reader
// of --robot-metrics should see which caches exist and that they were idle,
// not an empty list that looks like "no caches".
func AllCacheStats() []CacheStats {
	metrics := AllCacheMetrics()
	stats := make([]CacheStats, 0, len(metrics))
	for _, m := range metrics {
		stats = append(stats, m.Stats())
	}
	return stats
}

// MemoryStats holds a snapshot of memory statistics.
type MemoryStats struct {
	HeapAllocMB    float64 `json:"heap_alloc_mb"`
	HeapSysMB      float64 `json:"heap_sys_mb"`
	HeapObjectsK   float64 `json:"heap_objects_k"`
	GCCycles       uint32  `json:"gc_cycles"`
	GCPauseMs      float64 `json:"gc_pause_ms"`
	GoroutineCount int     `json:"goroutine_count"`
}

// GetMemoryStats returns current memory statistics.
func GetMemoryStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return MemoryStats{
		HeapAllocMB:    float64(m.HeapAlloc) / (1024 * 1024),
		HeapSysMB:      float64(m.HeapSys) / (1024 * 1024),
		HeapObjectsK:   float64(m.HeapObjects) / 1000,
		GCCycles:       m.NumGC,
		GCPauseMs:      float64(m.PauseTotalNs) / 1e6,
		GoroutineCount: runtime.NumGoroutine(),
	}
}

// MetricsOutput is the complete metrics output structure for --robot-metrics.
type MetricsOutput struct {
	Timing []TimingStats `json:"timing,omitempty"`
	Cache  []CacheStats  `json:"cache,omitempty"`
	Memory MemoryStats   `json:"memory"`
}

// GetAllMetrics returns a complete metrics snapshot.
func GetAllMetrics() MetricsOutput {
	return MetricsOutput{
		Timing: AllTimingStats(),
		Cache:  AllCacheStats(),
		Memory: GetMemoryStats(),
	}
}
