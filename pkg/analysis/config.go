package analysis

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// AnalysisConfig controls which metrics to compute and their timeouts.
// This enables size-based algorithm selection for optimal performance.
type AnalysisConfig struct {
	// DisableCache forces this analysis call to recompute instead of consulting
	// or populating either the in-process incremental cache or the robot disk
	// cache. This is useful for profiling and validation that require fresh work.
	DisableCache bool `json:"-"`

	// RunToCompletion removes wall-clock races from otherwise deterministic
	// metric algorithms. ApplyEnvOverrides enables it when SOURCE_DATE_EPOCH is
	// a valid base-10 int64, which is the CLI's signal for reproducible output.
	// It is execution state rather than robot output, so the disk-cache codec
	// persists it separately while ComputeConfigHash still binds it into the key.
	RunToCompletion bool `json:"-"`

	// Betweenness centrality (expensive: O(V*E))
	ComputeBetweenness       bool
	BetweennessTimeout       time.Duration
	BetweennessSkipReason    string          // Set when skipped, explains why
	BetweennessMode          BetweennessMode // "exact", "approximate", or "skip"
	BetweennessSampleSize    int             // Sample size for approximate mode
	BetweennessIsApproximate bool            // True if approximation was used (set after computation)

	// PageRank
	ComputePageRank    bool
	PageRankTimeout    time.Duration
	PageRankSkipReason string

	// HITS (Hubs and Authorities)
	ComputeHITS    bool
	HITSTimeout    time.Duration
	HITSSkipReason string

	// Cycle detection (potentially exponential)
	ComputeCycles    bool
	CyclesTimeout    time.Duration
	MaxCyclesToStore int
	CyclesSkipReason string

	// Eigenvector centrality (usually fast)
	ComputeEigenvector bool

	// Critical path scoring (fast, O(V+E))
	ComputeCriticalPath bool

	// Advanced graph signals (bv-t1js optimization)
	// These can be skipped for triage-only mode to reduce latency
	ComputeKCore        bool // k-core decomposition
	ComputeArticulation bool // Articulation points
	ComputeSlack        bool // Scheduling slack
}

// DefaultConfig returns the default analysis configuration.
// All metrics enabled with standard timeouts. Uses exact betweenness.
func DefaultConfig() AnalysisConfig {
	cfg := AnalysisConfig{
		ComputeBetweenness: true,
		BetweennessMode:    BetweennessExact,
		BetweennessTimeout: 500 * time.Millisecond,

		ComputePageRank: true,
		PageRankTimeout: 500 * time.Millisecond,

		ComputeHITS: true,
		HITSTimeout: 500 * time.Millisecond,

		ComputeCycles:    true,
		CyclesTimeout:    500 * time.Millisecond,
		MaxCyclesToStore: 100,

		ComputeEigenvector:  true,
		ComputeCriticalPath: true,

		ComputeKCore:        true,
		ComputeArticulation: true,
		ComputeSlack:        true,
	}
	return ApplyEnvOverrides(cfg)
}

// ConfigForSize returns an appropriate configuration based on graph size.
// Larger graphs get more aggressive timeouts and may use approximate algorithms.
//
// Size tiers:
//   - Small (<100 nodes): Full analysis with exact algorithms, generous timeouts
//   - Medium (100-500 nodes): Exact algorithms with standard timeouts
//   - Large (500-2000 nodes): Approximate betweenness for sparse graphs, skip for dense
//   - XL (>2000 nodes): Approximate betweenness, skip cycles and HITS for dense graphs
func ConfigForSize(nodeCount, edgeCount int) AnalysisConfig {
	density := 0.0
	if nodeCount > 1 {
		// Convert before multiplying. The graph can never contain enough nodes
		// to overflow an int in practice, but ConfigForSize is a public helper
		// and callers may pass synthetic counts (for planning or tests).
		density = float64(edgeCount) / (float64(nodeCount) * float64(nodeCount-1))
	}

	var cfg AnalysisConfig
	switch {
	case nodeCount < 100:
		// Small graph: run everything with generous timeouts, exact betweenness
		cfg = AnalysisConfig{
			ComputeBetweenness: true,
			BetweennessMode:    BetweennessExact,
			BetweennessTimeout: 2 * time.Second,

			ComputePageRank: true,
			PageRankTimeout: 2 * time.Second,

			ComputeHITS: true,
			HITSTimeout: 2 * time.Second,

			ComputeCycles:    true,
			CyclesTimeout:    2 * time.Second,
			MaxCyclesToStore: 1000,

			ComputeEigenvector:  true,
			ComputeCriticalPath: true,

			ComputeKCore:        true,
			ComputeArticulation: true,
			ComputeSlack:        true,
		}

	case nodeCount < 500:
		// Medium graph: standard timeouts, exact betweenness
		cfg = AnalysisConfig{
			ComputeBetweenness: true,
			BetweennessMode:    BetweennessExact,
			BetweennessTimeout: 500 * time.Millisecond,

			ComputePageRank: true,
			PageRankTimeout: 500 * time.Millisecond,

			ComputeHITS: true,
			HITSTimeout: 500 * time.Millisecond,

			ComputeCycles:    true,
			CyclesTimeout:    500 * time.Millisecond,
			MaxCyclesToStore: 100,

			ComputeEigenvector:  true,
			ComputeCriticalPath: true,

			ComputeKCore:        true,
			ComputeArticulation: true,
			ComputeSlack:        true,
		}

	case nodeCount < 2000:
		// Large graph: use approximate betweenness, shorter timeouts
		cfg = AnalysisConfig{
			ComputePageRank: true,
			PageRankTimeout: 300 * time.Millisecond,

			ComputeHITS: true,
			HITSTimeout: 300 * time.Millisecond,

			ComputeCycles:    true,
			CyclesTimeout:    300 * time.Millisecond,
			MaxCyclesToStore: 50,

			ComputeEigenvector:  true,
			ComputeCriticalPath: true,

			ComputeKCore:        true,
			ComputeArticulation: true,
			ComputeSlack:        true,
		}

		// Use approximate betweenness for large sparse graphs, skip for dense
		if density < 0.01 {
			cfg.ComputeBetweenness = true
			cfg.BetweennessMode = BetweennessApproximate
			cfg.BetweennessSampleSize = RecommendSampleSize(nodeCount, edgeCount)
			cfg.BetweennessTimeout = 500 * time.Millisecond // More time for sampling
		} else {
			cfg.ComputeBetweenness = false
			cfg.BetweennessMode = BetweennessSkip
			cfg.BetweennessSkipReason = "graph too dense (density > 0.01)"
		}

	default:
		// XL graph (>2000 nodes): use approximate betweenness with larger sample
		cfg = AnalysisConfig{
			// Use approximate betweenness for XL graphs
			ComputeBetweenness:    true,
			BetweennessMode:       BetweennessApproximate,
			BetweennessSampleSize: RecommendSampleSize(nodeCount, edgeCount),
			BetweennessTimeout:    500 * time.Millisecond,

			ComputePageRank: true,
			PageRankTimeout: 200 * time.Millisecond,

			ComputeCycles:    false,
			CyclesSkipReason: "graph too large (>2000 nodes)",
			MaxCyclesToStore: 10,

			ComputeEigenvector:  true,
			ComputeCriticalPath: true,

			ComputeKCore:        true,
			ComputeArticulation: true,
			ComputeSlack:        true,
		}

		// Only compute HITS for very sparse XL graphs
		if density < 0.001 {
			cfg.ComputeHITS = true
			cfg.HITSTimeout = 200 * time.Millisecond
		} else {
			cfg.ComputeHITS = false
			cfg.HITSSkipReason = "graph too large and dense"
		}
	}
	return ApplyEnvOverrides(cfg)
}

// FullAnalysisConfig returns a config that computes all metrics regardless of size.
// Useful when --force-full-analysis is specified. Uses exact betweenness.
func FullAnalysisConfig() AnalysisConfig {
	cfg := AnalysisConfig{
		ComputeBetweenness: true,
		BetweennessMode:    BetweennessExact, // Force exact for full analysis
		BetweennessTimeout: 30 * time.Second, // Very generous for forced full analysis

		ComputePageRank: true,
		PageRankTimeout: 30 * time.Second,

		ComputeHITS: true,
		HITSTimeout: 30 * time.Second,

		ComputeCycles:    true,
		CyclesTimeout:    30 * time.Second,
		MaxCyclesToStore: 10000,

		ComputeEigenvector:  true,
		ComputeCriticalPath: true,

		ComputeKCore:        true,
		ComputeArticulation: true,
		ComputeSlack:        true,
	}
	return ApplyEnvOverrides(cfg)
}

// TriageConfig returns a minimal config optimized for triage operations.
// Only computes PageRank and Betweenness which are needed for triage scoring.
// Skips Eigenvector, HITS, Cycles, k-core, articulation, and slack for 50-200ms savings.
// (bv-t1js optimization)
func TriageConfig() AnalysisConfig {
	cfg := AnalysisConfig{
		ComputeBetweenness:    true,
		BetweennessMode:       BetweennessApproximate,
		BetweennessSampleSize: 50, // Fast approximation
		BetweennessTimeout:    200 * time.Millisecond,

		ComputePageRank: true,
		PageRankTimeout: 200 * time.Millisecond,

		// Disable metrics not needed for triage
		ComputeHITS:         false,
		ComputeCycles:       false,
		ComputeEigenvector:  false,
		ComputeCriticalPath: false,
		ComputeKCore:        false,
		ComputeArticulation: false,
		ComputeSlack:        false,
	}
	return ApplyEnvOverrides(cfg)
}

// AllPhase2Disabled returns true if all Phase 2 metrics are disabled.
// When this returns true, the Phase 2 goroutine can be skipped entirely.
func (c AnalysisConfig) AllPhase2Disabled() bool {
	return !c.ComputeBetweenness &&
		!c.ComputePageRank &&
		!c.ComputeHITS &&
		!c.ComputeCycles &&
		!c.ComputeEigenvector &&
		!c.ComputeCriticalPath &&
		!c.ComputeKCore &&
		!c.ComputeArticulation &&
		!c.ComputeSlack
}

// NoPhase2Config returns a config with all Phase 2 metrics disabled.
// Use this when Phase 2 metrics are not needed (e.g., all issues closed).
func NoPhase2Config() AnalysisConfig {
	return AnalysisConfig{
		// All Phase 2 metrics disabled
		ComputeBetweenness:  false,
		ComputePageRank:     false,
		ComputeHITS:         false,
		ComputeCycles:       false,
		ComputeEigenvector:  false,
		ComputeCriticalPath: false,
		ComputeKCore:        false,
		ComputeArticulation: false,
		ComputeSlack:        false,
	}
}

// SkippedMetrics returns a list of metrics that are configured to be skipped.
func (c AnalysisConfig) SkippedMetrics() []SkippedMetric {
	var skipped []SkippedMetric

	if !c.ComputeBetweenness {
		skipped = append(skipped, SkippedMetric{
			Name:   "Betweenness",
			Reason: c.BetweennessSkipReason,
		})
	}
	if !c.ComputePageRank {
		skipped = append(skipped, SkippedMetric{
			Name:   "PageRank",
			Reason: c.PageRankSkipReason,
		})
	}
	if !c.ComputeHITS {
		skipped = append(skipped, SkippedMetric{
			Name:   "HITS",
			Reason: c.HITSSkipReason,
		})
	}
	if !c.ComputeCycles {
		skipped = append(skipped, SkippedMetric{
			Name:   "Cycles",
			Reason: c.CyclesSkipReason,
		})
	}

	return skipped
}

// SkippedMetric describes a metric that was skipped and why.
type SkippedMetric struct {
	Name   string
	Reason string
}

const (
	// EnvSkipPhase2 disables most Phase 2 metrics (centrality, cycles, critical path).
	EnvSkipPhase2 = "BV_SKIP_PHASE2"
	// EnvPhase2TimeoutSeconds overrides per-metric Phase 2 timeouts when set (>0).
	EnvPhase2TimeoutSeconds = "BV_PHASE2_TIMEOUT_S"
	// EnvSourceDateEpoch requests reproducible output when it contains a valid
	// base-10 int64 Unix timestamp, matching the robot CLI clock contract.
	EnvSourceDateEpoch = "SOURCE_DATE_EPOCH"
)

// ApplyEnvOverrides applies environment-variable tunables to the analysis config.
//
// Supported:
//   - BV_SKIP_PHASE2=1: skip expensive Phase 2 metrics (PageRank, Betweenness, HITS, Cycles,
//     Eigenvector, Critical Path). (k-core/articulation/slack remain enabled.)
//   - BV_PHASE2_TIMEOUT_S=N: override per-metric timeouts to N seconds (must be >0).
//   - SOURCE_DATE_EPOCH=N: in robot mode, run timeout-raced metrics to completion
//     when N is a valid base-10 int64, making their result state reproducible.
func ApplyEnvOverrides(cfg AnalysisConfig) AnalysisConfig {
	// SOURCE_DATE_EPOCH is a robot-output reproducibility contract. Do not let a
	// process-wide build timestamp silently remove the TUI/library latency bounds.
	cfg.RunToCompletion = envBool("BV_ROBOT") && validSourceDateEpoch()

	if envBool(EnvSkipPhase2) {
		cfg.ComputeBetweenness = false
		cfg.BetweennessMode = BetweennessSkip
		cfg.BetweennessSkipReason = "BV_SKIP_PHASE2 set"

		cfg.ComputePageRank = false
		cfg.PageRankSkipReason = "BV_SKIP_PHASE2 set"

		cfg.ComputeHITS = false
		cfg.HITSSkipReason = "BV_SKIP_PHASE2 set"

		cfg.ComputeCycles = false
		cfg.CyclesSkipReason = "BV_SKIP_PHASE2 set"

		cfg.ComputeEigenvector = false
		cfg.ComputeCriticalPath = false
	}

	if timeout, ok := envPositiveSeconds(EnvPhase2TimeoutSeconds); ok {
		if cfg.ComputeBetweenness {
			cfg.BetweennessTimeout = timeout
		}
		if cfg.ComputePageRank {
			cfg.PageRankTimeout = timeout
		}
		if cfg.ComputeHITS {
			cfg.HITSTimeout = timeout
		}
		if cfg.ComputeCycles {
			cfg.CyclesTimeout = timeout
		}
	}

	return cfg
}

func validSourceDateEpoch() bool {
	v := strings.TrimSpace(os.Getenv(EnvSourceDateEpoch))
	if v == "" {
		return false
	}
	seconds, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return false
	}
	// Keep this activation contract aligned with cmd/bv's pinned robot clock:
	// RFC3339 and time.Time's JSON encoding require a four-digit year. An
	// unencodable epoch must not silently remove every analysis timeout while
	// the rest of the robot output falls back to wall-clock time.
	year := time.Unix(seconds, 0).UTC().Year()
	return year >= 0 && year < 10000
}

func envPositiveSeconds(name string) (time.Duration, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	seconds, err := strconv.ParseInt(v, 10, 64)
	maxSeconds := int64(time.Duration(1<<63-1) / time.Second)
	if err != nil || seconds <= 0 || seconds > maxSeconds {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func envBool(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
