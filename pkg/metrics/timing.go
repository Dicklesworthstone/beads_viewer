// Package metrics provides performance instrumentation for bv.
//
// This package enables visibility into performance characteristics:
// - Timing metrics for hot paths (cycle detection, triage, etc.)
// - Cache hit/miss tracking
// - Memory usage snapshots
//
// Metrics are collected in-memory with atomic operations for thread-safety.
// Collection is enabled by default but can be disabled via BV_METRICS=0.
//
// Usage:
//
//	func expensiveOperation() {
//	    defer metrics.Timer(metrics.CycleDetection)()
//	    // ... operation code
//	}
package metrics

import (
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// enabled controls whether metrics are collected.
// Defaults to true unless BV_METRICS=0 is set.
// Uses atomic.Bool for thread-safe concurrent access.
var enabled atomic.Bool

func init() {
	enabled.Store(env.Metrics.Get() != "0")
}

// Enabled returns whether metrics collection is enabled.
func Enabled() bool {
	return enabled.Load()
}

// SetEnabled allows programmatic control of metrics collection.
func SetEnabled(e bool) {
	enabled.Store(e)
}

// TimingMetric tracks timing statistics for a named operation.
// All methods are thread-safe using atomic operations.
type TimingMetric struct {
	name    string
	count   int64
	totalNs int64
	maxNs   int64
	minNs   int64 // 0 means not set
}

// newTimingMetric creates a new timing metric with the given name.
func newTimingMetric(name string) *TimingMetric {
	return &TimingMetric{name: name}
}

// Record records a single timing measurement.
// Thread-safe via atomic operations.
func (m *TimingMetric) Record(d time.Duration) {
	if !enabled.Load() {
		return
	}
	ns := d.Nanoseconds()

	atomic.AddInt64(&m.count, 1)
	atomic.AddInt64(&m.totalNs, ns)

	// Update max atomically using compare-and-swap
	for {
		old := atomic.LoadInt64(&m.maxNs)
		if ns <= old || atomic.CompareAndSwapInt64(&m.maxNs, old, ns) {
			break
		}
	}

	// Update min atomically using compare-and-swap
	for {
		old := atomic.LoadInt64(&m.minNs)
		if old != 0 && ns >= old {
			break
		}
		if atomic.CompareAndSwapInt64(&m.minNs, old, ns) {
			break
		}
	}
}

// Name returns the metric name.
func (m *TimingMetric) Name() string {
	return m.name
}

// Count returns the number of recorded measurements.
func (m *TimingMetric) Count() int64 {
	return atomic.LoadInt64(&m.count)
}

// TotalNs returns the total time in nanoseconds.
func (m *TimingMetric) TotalNs() int64 {
	return atomic.LoadInt64(&m.totalNs)
}

// MaxNs returns the maximum recorded time in nanoseconds.
func (m *TimingMetric) MaxNs() int64 {
	return atomic.LoadInt64(&m.maxNs)
}

// MinNs returns the minimum recorded time in nanoseconds.
// Returns 0 if no measurements have been recorded.
func (m *TimingMetric) MinNs() int64 {
	return atomic.LoadInt64(&m.minNs)
}

// AvgNs returns the average time in nanoseconds.
// Returns 0 if no measurements have been recorded.
func (m *TimingMetric) AvgNs() int64 {
	count := atomic.LoadInt64(&m.count)
	if count == 0 {
		return 0
	}
	total := atomic.LoadInt64(&m.totalNs)
	return total / count
}

// Stats returns all timing statistics at once.
func (m *TimingMetric) Stats() TimingStats {
	count := atomic.LoadInt64(&m.count)
	totalNs := atomic.LoadInt64(&m.totalNs)
	maxNs := atomic.LoadInt64(&m.maxNs)
	minNs := atomic.LoadInt64(&m.minNs)

	var avgNs int64
	if count > 0 {
		avgNs = totalNs / count
	}

	return TimingStats{
		Name:    m.name,
		Count:   count,
		TotalMs: float64(totalNs) / 1e6,
		AvgMs:   float64(avgNs) / 1e6,
		MaxMs:   float64(maxNs) / 1e6,
		MinMs:   float64(minNs) / 1e6,
	}
}

// Reset clears all recorded measurements.
func (m *TimingMetric) Reset() {
	atomic.StoreInt64(&m.count, 0)
	atomic.StoreInt64(&m.totalNs, 0)
	atomic.StoreInt64(&m.maxNs, 0)
	atomic.StoreInt64(&m.minNs, 0)
}

// TimingStats holds a snapshot of timing statistics.
type TimingStats struct {
	Name    string  `json:"name"`
	Count   int64   `json:"count"`
	TotalMs float64 `json:"total_ms"`
	AvgMs   float64 `json:"avg_ms"`
	MaxMs   float64 `json:"max_ms"`
	MinMs   float64 `json:"min_ms,omitempty"`
}

// Timer returns a function that records elapsed time when called.
// Use with defer for automatic timing:
//
//	func myFunc() {
//	    defer metrics.Timer(metrics.SomeMetric)()
//	    // ... function body
//	}
func Timer(m *TimingMetric) func() {
	if !enabled.Load() || m == nil {
		return func() {}
	}
	start := time.Now()
	return func() {
		m.Record(time.Since(start))
	}
}

// TimerWithCallback returns a function that records elapsed time
// and also calls the provided callback with the duration.
func TimerWithCallback(m *TimingMetric, cb func(time.Duration)) func() {
	if !enabled.Load() || m == nil {
		return func() {}
	}
	start := time.Now()
	return func() {
		d := time.Since(start)
		m.Record(d)
		if cb != nil {
			cb(d)
		}
	}
}

// Global timing metrics for various operations.
var (
	CycleDetection     = newTimingMetric("cycle_detection")
	TopologicalSort    = newTimingMetric("topological_sort")
	TriageAnalysis     = newTimingMetric("triage_analysis")
	GraphStatsAccess   = newTimingMetric("graph_stats_access")
	VectorSearch       = newTimingMetric("vector_search")
	JSONParsing        = newTimingMetric("json_parsing")
	PageRankCompute    = newTimingMetric("pagerank_compute")
	BetweennessCompute = newTimingMetric("betweenness_compute")
	HITSCompute        = newTimingMetric("hits_compute")
	GraphLoad          = newTimingMetric("graph_load")
	UIRender           = newTimingMetric("ui_render")

	// Fed by pkg/loader and pkg/analysis on every load and analysis.
	LoaderParse    = newTimingMetric("loader.parse")
	AnalysisPhase1 = newTimingMetric("analysis.phase1")
	AnalysisPhase2 = newTimingMetric("analysis.phase2")
)

// AllTimingMetrics returns all registered timing metrics.
func AllTimingMetrics() []*TimingMetric {
	return []*TimingMetric{
		CycleDetection,
		TopologicalSort,
		TriageAnalysis,
		GraphStatsAccess,
		VectorSearch,
		JSONParsing,
		PageRankCompute,
		BetweennessCompute,
		HITSCompute,
		GraphLoad,
		UIRender,
		LoaderParse,
		AnalysisPhase1,
		AnalysisPhase2,
	}
}

// ResetAll resets all timing metrics.
func ResetAll() {
	for _, m := range AllTimingMetrics() {
		m.Reset()
	}
	for _, m := range AllCacheMetrics() {
		m.Reset()
	}
}

// AllTimingStats returns stats for all timing metrics.
func AllTimingStats() []TimingStats {
	metrics := AllTimingMetrics()
	stats := make([]TimingStats, 0, len(metrics))
	for _, m := range metrics {
		if m.Count() > 0 { // Only include metrics with data
			stats = append(stats, m.Stats())
		}
	}
	return stats
}
