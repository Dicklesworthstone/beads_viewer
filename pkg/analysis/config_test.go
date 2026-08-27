package analysis

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func TestConfigForSize_SmallGraph(t *testing.T) {
	// Small graphs should enable all algorithms with generous timeouts
	cfg := ConfigForSize(50, 100)

	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled for small graph")
	}
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled for small graph")
	}
	if !cfg.ComputeHITS {
		t.Error("Expected HITS enabled for small graph")
	}
	if !cfg.ComputeCycles {
		t.Error("Expected cycles enabled for small graph")
	}
	if !cfg.ComputeEigenvector {
		t.Error("Expected eigenvector enabled for small graph")
	}
	if !cfg.ComputeCriticalPath {
		t.Error("Expected critical path enabled for small graph")
	}

	// Should have generous timeouts
	if cfg.BetweennessTimeout < 1*time.Second {
		t.Errorf("Expected generous betweenness timeout for small graph, got %v", cfg.BetweennessTimeout)
	}
}

func TestConfigForSize_MediumGraph(t *testing.T) {
	// Medium graphs should enable all algorithms with standard timeouts
	cfg := ConfigForSize(300, 600)

	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled for medium graph")
	}
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled for medium graph")
	}
	if !cfg.ComputeHITS {
		t.Error("Expected HITS enabled for medium graph")
	}
	if !cfg.ComputeCycles {
		t.Error("Expected cycles enabled for medium graph")
	}

	// Should have standard timeouts
	if cfg.BetweennessTimeout != 500*time.Millisecond {
		t.Errorf("Expected 500ms betweenness timeout for medium graph, got %v", cfg.BetweennessTimeout)
	}
}

func TestConfigForSize_LargeGraph_Sparse(t *testing.T) {
	// Large sparse graph (density < 0.01)
	// 1000 nodes with ~5000 edges = density ~0.005
	cfg := ConfigForSize(1000, 5000)

	// Should enable betweenness for sparse large graphs
	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled for large sparse graph")
	}
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled for large graph")
	}
	if !cfg.ComputeCycles {
		t.Error("Expected cycles enabled for large graph")
	}
}

func TestConfigForSize_LargeGraph_Dense(t *testing.T) {
	// Large dense graph (density > 0.01)
	// 1000 nodes with ~15000 edges = density ~0.015
	cfg := ConfigForSize(1000, 15000)

	// Should skip betweenness for dense large graphs
	if cfg.ComputeBetweenness {
		t.Error("Expected betweenness disabled for large dense graph")
	}
	if cfg.BetweennessSkipReason == "" {
		t.Error("Expected skip reason for betweenness")
	}

	// Other algorithms should still be enabled
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled for large dense graph")
	}
}

func TestConfigForSize_XLGraph(t *testing.T) {
	// XL graph (>2000 nodes) should use approximate betweenness and skip cycles
	cfg := ConfigForSize(3000, 10000)

	// Should use approximate betweenness (not skip entirely)
	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled (approximate) for XL graph")
	}
	if cfg.BetweennessMode != BetweennessApproximate {
		t.Errorf("Expected approximate betweenness mode for XL graph, got %s", cfg.BetweennessMode)
	}
	if cfg.BetweennessSampleSize <= 0 {
		t.Error("Expected positive sample size for approximate betweenness")
	}

	// Should skip cycles
	if cfg.ComputeCycles {
		t.Error("Expected cycles disabled for XL graph")
	}
	if cfg.CyclesSkipReason == "" {
		t.Error("Expected skip reason for cycles")
	}

	// PageRank should still be enabled
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled for XL graph")
	}
}

func TestConfigForSize_XLGraph_VeryDense(t *testing.T) {
	// XL dense graph should skip HITS too
	// density > 0.001 for XL
	cfg := ConfigForSize(3000, 30000) // density ~0.003

	if cfg.ComputeHITS {
		t.Error("Expected HITS disabled for XL dense graph")
	}
	if cfg.HITSSkipReason == "" {
		t.Error("Expected skip reason for HITS")
	}
}

func TestConfigForSize_XLGraph_VerySparse(t *testing.T) {
	// XL sparse graph should still compute HITS
	// density < 0.001 for XL
	cfg := ConfigForSize(5000, 3000) // density ~0.0001

	if !cfg.ComputeHITS {
		t.Error("Expected HITS enabled for XL sparse graph")
	}
}

func TestConfigForSize_DensityDoesNotOverflowIntMultiplication(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("synthetic graph counts require a 64-bit int")
	}

	// Correct density is 0.002, so an XL graph must skip HITS. Multiplying
	// these node counts as ints first overflows int64 and used to make the
	// density negative, incorrectly classifying the graph as very sparse.
	nodeCount := int(int64(3_100_000_000))
	edgeCount := int(int64(19_220_000_000_000_000))
	cfg := ConfigForSize(nodeCount, edgeCount)
	if cfg.ComputeHITS {
		t.Fatal("overflowed density classified a synthetic dense XL graph as sparse")
	}
}

func TestSkippedMetrics(t *testing.T) {
	cfg := AnalysisConfig{
		ComputeBetweenness:    false,
		BetweennessSkipReason: "test reason",
		ComputePageRank:       true,
		ComputeHITS:           true,
		ComputeCycles:         false,
		CyclesSkipReason:      "cycles disabled",
	}

	skipped := cfg.SkippedMetrics()

	if len(skipped) != 2 {
		t.Errorf("Expected 2 skipped metrics, got %d", len(skipped))
	}

	// Check betweenness is in the list
	found := false
	for _, s := range skipped {
		if s.Name == "Betweenness" {
			found = true
			if s.Reason != "test reason" {
				t.Errorf("Expected reason 'test reason', got '%s'", s.Reason)
			}
		}
	}
	if !found {
		t.Error("Expected Betweenness in skipped metrics")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// All algorithms should be enabled
	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled in default config")
	}
	if !cfg.ComputePageRank {
		t.Error("Expected pagerank enabled in default config")
	}
	if !cfg.ComputeHITS {
		t.Error("Expected HITS enabled in default config")
	}
	if !cfg.ComputeCycles {
		t.Error("Expected cycles enabled in default config")
	}
	if !cfg.ComputeEigenvector {
		t.Error("Expected eigenvector enabled in default config")
	}
	if !cfg.ComputeCriticalPath {
		t.Error("Expected critical path enabled in default config")
	}
}

func TestFullAnalysisConfig(t *testing.T) {
	cfg := FullAnalysisConfig()

	// All algorithms should be enabled with generous timeouts
	if !cfg.ComputeBetweenness {
		t.Error("Expected betweenness enabled in full config")
	}
	if cfg.BetweennessTimeout < 10*time.Second {
		t.Errorf("Expected generous timeout in full config, got %v", cfg.BetweennessTimeout)
	}
	if cfg.MaxCyclesToStore < 1000 {
		t.Errorf("Expected high max cycles in full config, got %d", cfg.MaxCyclesToStore)
	}
}

func TestAnalysisConfigDisableCacheIsNotSerialized(t *testing.T) {
	encoded, err := json.Marshal(AnalysisConfig{DisableCache: true, RunToCompletion: true})
	if err != nil {
		t.Fatalf("marshalling analysis config: %v", err)
	}
	if bytes.Contains(encoded, []byte("DisableCache")) {
		t.Fatalf("DisableCache is an execution control and must not change robot output schema: %s", encoded)
	}
	if bytes.Contains(encoded, []byte("RunToCompletion")) {
		t.Fatalf("RunToCompletion is an execution control and must not change robot output schema: %s", encoded)
	}
}

func TestApplyEnvOverrides_SourceDateEpochRunToCompletion(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "positive integer", value: "1234567890", want: true},
		{name: "unix epoch", value: "0", want: true},
		{name: "signed integer", value: "-1", want: true},
		{name: "surrounding whitespace", value: " 42 ", want: true},
		{name: "empty", value: "", want: false},
		{name: "fractional", value: "1.5", want: false},
		{name: "nonnumeric", value: "tomorrow", want: false},
		{name: "int64 max is not JSON encodable", value: "9223372036854775807", want: false},
		{name: "int64 min is not JSON encodable", value: "-9223372036854775808", want: false},
		{name: "int64 overflow", value: "9223372036854775808", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvSourceDateEpoch, tt.value)
			// Start in the opposite state to prove ApplyEnvOverrides derives the
			// mode from the validated environment rather than retaining stale state.
			cfg := ApplyEnvOverrides(AnalysisConfig{RunToCompletion: !tt.want})
			if cfg.RunToCompletion != tt.want {
				t.Fatalf("RunToCompletion=%v for %q, want %v", cfg.RunToCompletion, tt.value, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_SourceDateEpochDoesNotUnboundInteractiveAnalysis(t *testing.T) {
	t.Setenv("BV_ROBOT", "0")
	t.Setenv(EnvSourceDateEpoch, "1234567890")

	cfg := ApplyEnvOverrides(AnalysisConfig{RunToCompletion: true})
	if cfg.RunToCompletion {
		t.Fatal("SOURCE_DATE_EPOCH enabled run-to-completion outside robot mode")
	}
}

func TestComputeConfigHash_DistinguishesRunToCompletion(t *testing.T) {
	regular := AnalysisConfig{ComputePageRank: true, PageRankTimeout: time.Millisecond}
	reproducible := regular
	reproducible.RunToCompletion = true

	regularHash := ComputeConfigHash(&regular)
	reproducibleHash := ComputeConfigHash(&reproducible)
	if regularHash == reproducibleHash {
		t.Fatalf("config hash ignored RunToCompletion: %s", regularHash)
	}
}

func TestDefaultConfig_EnvSkipPhase2(t *testing.T) {
	t.Setenv(EnvSkipPhase2, "1")

	cfg := DefaultConfig()

	if cfg.ComputePageRank || cfg.ComputeHITS || cfg.ComputeCycles || cfg.ComputeEigenvector || cfg.ComputeCriticalPath {
		t.Errorf("Expected most phase 2 metrics disabled when %s=1, got: %+v", EnvSkipPhase2, cfg)
	}
	if cfg.ComputeBetweenness || cfg.BetweennessMode != BetweennessSkip {
		t.Errorf("Expected betweenness skipped when %s=1, got: ComputeBetweenness=%v mode=%q", EnvSkipPhase2, cfg.ComputeBetweenness, cfg.BetweennessMode)
	}
	if cfg.BetweennessSkipReason == "" || cfg.PageRankSkipReason == "" || cfg.HITSSkipReason == "" || cfg.CyclesSkipReason == "" {
		t.Errorf("Expected skip reasons set when %s=1, got: betweenness=%q pagerank=%q hits=%q cycles=%q", EnvSkipPhase2, cfg.BetweennessSkipReason, cfg.PageRankSkipReason, cfg.HITSSkipReason, cfg.CyclesSkipReason)
	}
}

func TestDefaultConfig_EnvPhase2TimeoutOverride(t *testing.T) {
	t.Setenv(EnvPhase2TimeoutSeconds, "7")

	cfg := DefaultConfig()

	want := 7 * time.Second
	if cfg.BetweennessTimeout != want || cfg.PageRankTimeout != want || cfg.HITSTimeout != want || cfg.CyclesTimeout != want {
		t.Errorf("Expected phase 2 timeouts overridden to %v when %s=7, got: betweenness=%v pagerank=%v hits=%v cycles=%v",
			want, EnvPhase2TimeoutSeconds, cfg.BetweennessTimeout, cfg.PageRankTimeout, cfg.HITSTimeout, cfg.CyclesTimeout)
	}
}

func TestDefaultConfig_EnvPhase2TimeoutInvalidIgnored(t *testing.T) {
	t.Setenv(EnvPhase2TimeoutSeconds, "-1")

	cfg := DefaultConfig()

	// Default config uses 500ms timeouts; invalid env values should be ignored.
	want := 500 * time.Millisecond
	if cfg.BetweennessTimeout != want || cfg.PageRankTimeout != want || cfg.HITSTimeout != want || cfg.CyclesTimeout != want {
		t.Errorf("Expected invalid %s to be ignored and defaults retained, got: betweenness=%v pagerank=%v hits=%v cycles=%v",
			EnvPhase2TimeoutSeconds, cfg.BetweennessTimeout, cfg.PageRankTimeout, cfg.HITSTimeout, cfg.CyclesTimeout)
	}
}

func TestDefaultConfig_EnvPhase2TimeoutOverflowIgnored(t *testing.T) {
	t.Setenv(EnvPhase2TimeoutSeconds, "9223372036854775807")

	cfg := DefaultConfig()

	// Multiplying an unchecked seconds value by time.Second used to wrap to a
	// negative duration, turning every metric timer into an immediate timeout.
	want := 500 * time.Millisecond
	if cfg.BetweennessTimeout != want || cfg.PageRankTimeout != want || cfg.HITSTimeout != want || cfg.CyclesTimeout != want {
		t.Errorf("overflowing %s should be ignored, got: betweenness=%v pagerank=%v hits=%v cycles=%v",
			EnvPhase2TimeoutSeconds, cfg.BetweennessTimeout, cfg.PageRankTimeout, cfg.HITSTimeout, cfg.CyclesTimeout)
	}
}
