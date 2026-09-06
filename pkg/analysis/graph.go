package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/network"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"
)

// StartupProfile captures detailed timing information for startup diagnostics.
// Use AnalyzeWithProfile to populate this structure.
type StartupProfile struct {
	// Data characteristics
	NodeCount int     `json:"node_count"`
	EdgeCount int     `json:"edge_count"`
	Density   float64 `json:"density"`

	// Phase 1 timings
	BuildGraph time.Duration `json:"build_graph"`
	Degree     time.Duration `json:"degree"`
	TopoSort   time.Duration `json:"topo_sort"`
	Phase1     time.Duration `json:"phase1_total"`

	// Phase 2 timings (zero if skipped)
	PageRank      time.Duration `json:"pagerank"`
	PageRankTO    bool          `json:"pagerank_timeout"`
	Betweenness   time.Duration `json:"betweenness"`
	BetweennessTO bool          `json:"betweenness_timeout"`
	Eigenvector   time.Duration `json:"eigenvector"`
	HITS          time.Duration `json:"hits"`
	HITSTO        bool          `json:"hits_timeout"`
	CriticalPath  time.Duration `json:"critical_path"`
	Cycles        time.Duration `json:"cycles"`
	CyclesTO      bool          `json:"cycles_timeout"`
	CycleCount    int           `json:"cycle_count"`  // One stored representative per cyclic component
	KCore         time.Duration `json:"kcore"`        // bv-85
	Articulation  time.Duration `json:"articulation"` // bv-85
	Slack         time.Duration `json:"slack"`        // bv-85
	Phase2        time.Duration `json:"phase2_total"`

	// Configuration used
	Config AnalysisConfig `json:"config"`

	// Totals
	Total time.Duration `json:"total"`
}

// GraphStats holds the results of graph analysis.
// Phase 1 fields (OutDegree, InDegree, TopologicalOrder, Density) are populated
// immediately and can be read without synchronization after AnalyzeAsync returns.
// Phase 2 fields (centrality metrics, cycles) are computed in background and
// must be accessed via thread-safe accessor methods.
type GraphStats struct {
	// Phase 1 - Available immediately after AnalyzeAsync returns (read-only after init)
	OutDegree        map[string]int // Number of dependencies this issue has (edges out)
	InDegree         map[string]int // Number of issues that depend on this issue (edges in)
	TopologicalOrder []string
	Density          float64
	NodeCount        int // Number of nodes in graph
	EdgeCount        int // Number of edges in graph

	// Configuration used for this analysis (read-only after init)
	Config AnalysisConfig

	// Phase 2 - Computed in background, access via thread-safe methods only
	mu                sync.RWMutex
	phase2Ready       bool
	phase2Done        chan struct{} // Closed when Phase 2 completes
	pageRank          map[string]float64
	betweenness       map[string]float64
	eigenvector       map[string]float64
	hubs              map[string]float64
	authorities       map[string]float64
	criticalPathScore map[string]float64
	coreNumber        map[string]int
	articulation      map[string]bool
	slack             map[string]float64
	cycles            [][]string

	// Ranks (1-based, computed for UI optimization)
	pageRankRank     map[string]int
	betweennessRank  map[string]int
	eigenvectorRank  map[string]int
	hubsRank         map[string]int
	authoritiesRank  map[string]int
	criticalPathRank map[string]int
	inDegreeRank     map[string]int
	outDegreeRank    map[string]int

	// Phase 2 status flags for robot visibility
	status MetricStatus
}

// metricStatus captures per-metric computation outcome for transparency.
type MetricStatus struct {
	PageRank     statusEntry
	Betweenness  statusEntry
	Eigenvector  statusEntry
	HITS         statusEntry
	Critical     statusEntry
	Cycles       statusEntry
	KCore        statusEntry // bv-85: k-core decomposition
	Articulation statusEntry // bv-85: articulation points (cut vertices)
	Slack        statusEntry // bv-85: longest-path slack per node
}

// statusEntry records computation state for a single metric.
type statusEntry struct {
	State   string        `json:"state"`            // computed|approx|timeout|skipped
	Reason  string        `json:"reason,omitempty"` // explanation when skipped/timeout/approx
	Sample  int           `json:"sample,omitempty"` // sample size when approximate
	Elapsed time.Duration `json:"-"`                // serialized in ms via MarshalJSON
}

// MarshalJSON encodes Elapsed as milliseconds to match the JSON field name.
func (s statusEntry) MarshalJSON() ([]byte, error) {
	type out struct {
		State   string  `json:"state"`
		Reason  string  `json:"reason,omitempty"`
		Sample  int     `json:"sample,omitempty"`
		Elapsed float64 `json:"ms,omitempty"`
	}
	payload := out{
		State:  s.State,
		Reason: s.Reason,
		Sample: s.Sample,
	}
	if s.Elapsed != 0 {
		payload.Elapsed = float64(s.Elapsed) / float64(time.Millisecond)
	}
	return json.Marshal(payload)
}

// IsPhase2Ready returns true if Phase 2 metrics have been computed.
func (s *GraphStats) IsPhase2Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase2Ready
}

// graphStatsReadyForCache is the publication gate shared by the in-memory
// caches. WaitForPhase2 only means the worker exited: cancellation can close
// phase2Done without publishing Phase 2 results. The readiness bit distinguishes
// that case, while explicit pending/error states keep failed results out too.
func graphStatsReadyForCache(stats *GraphStats) bool {
	if stats == nil {
		return false
	}

	stats.mu.RLock()
	defer stats.mu.RUnlock()
	if !stats.phase2Ready {
		return false
	}
	for _, entry := range []statusEntry{
		stats.status.PageRank,
		stats.status.Betweenness,
		stats.status.Eigenvector,
		stats.status.HITS,
		stats.status.Critical,
		stats.status.Cycles,
		stats.status.KCore,
		stats.status.Articulation,
		stats.status.Slack,
	} {
		switch entry.State {
		case "computed", "approx", "skipped":
			continue
		default:
			return false
		}
	}
	return true
}

// Status returns a copy of metric status flags.
func (s *GraphStats) Status() MetricStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// ClaimUnsafeReasons returns metric-state reasons that make it unsafe for
// robot-next to emit a direct claim command. Triage scoring depends on PageRank
// and Betweenness; if either is incomplete, timed out, or disabled, robot
// output must fail closed even when a diagnostic top pick exists.
func (s MetricStatus) ClaimUnsafeReasons() []string {
	var reasons []string
	for _, metric := range []struct {
		name  string
		entry statusEntry
	}{
		{name: "PageRank", entry: s.PageRank},
		{name: "Betweenness", entry: s.Betweenness},
	} {
		switch metric.entry.State {
		case "computed", "approx":
			continue
		default:
			reason := metric.entry.State
			if reason == "" {
				reason = "unknown"
			}
			if metric.entry.Reason != "" {
				reason += ": " + metric.entry.Reason
			}
			reasons = append(reasons, metric.name+" "+reason)
		}
	}
	return reasons
}

// stateFromTiming converts config flags/timeouts to a user-facing state string.
func stateFromTiming(enabled bool, timedOut bool) string {
	switch {
	case !enabled:
		return "skipped"
	case timedOut:
		return "timeout"
	default:
		return "computed"
	}
}

func emptyGraphMetricStatus(config AnalysisConfig) MetricStatus {
	kcoreComputed := config.ComputeKCore || config.ComputeArticulation
	articulationComputed := config.ComputeArticulation || config.ComputeKCore
	return MetricStatus{
		PageRank:     statusEntry{State: stateFromTiming(config.ComputePageRank, false)},
		Betweenness:  statusEntry{State: stateFromTiming(config.ComputeBetweenness, false)},
		Eigenvector:  statusEntry{State: stateFromTiming(config.ComputeEigenvector, false)},
		HITS:         statusEntry{State: stateFromTiming(config.ComputeHITS, false)},
		Critical:     statusEntry{State: stateFromTiming(config.ComputeCriticalPath, false)},
		Cycles:       statusEntry{State: stateFromTiming(config.ComputeCycles, false)},
		KCore:        statusEntry{State: stateFromTiming(kcoreComputed, false)},
		Articulation: statusEntry{State: stateFromTiming(articulationComputed, false)},
		Slack:        statusEntry{State: stateFromTiming(config.ComputeSlack, false)},
	}
}

func betweennessReason(cfg AnalysisConfig, isApprox bool) string {
	if cfg.BetweennessSkipReason != "" {
		return cfg.BetweennessSkipReason
	}
	if cfg.BetweennessMode == BetweennessApproximate || isApprox {
		return "approximate"
	}
	return ""
}

// WaitForPhase2 blocks until Phase 2 computation completes.
func (s *GraphStats) WaitForPhase2() {
	if s.phase2Done != nil {
		<-s.phase2Done
	}
}

// WaitForPhase2Context waits for completion without keeping a UI command alive
// after its source is replaced or the application shuts down. Completion alone
// does not imply every metric succeeded; callers must still inspect Status.
func (s *GraphStats) WaitForPhase2Context(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := s.phase2Done
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ctx.Err()
	}
}

// GetPageRankScore returns the PageRank score for a single issue.
// Returns 0 if Phase 2 is not yet complete or if the issue is not found.
func (s *GraphStats) GetPageRankScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pageRank == nil {
		return 0
	}
	return s.pageRank[id]
}

// GetBetweennessScore returns the betweenness centrality for a single issue.
func (s *GraphStats) GetBetweennessScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.betweenness == nil {
		return 0
	}
	return s.betweenness[id]
}

// GetEigenvectorScore returns the eigenvector centrality for a single issue.
func (s *GraphStats) GetEigenvectorScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.eigenvector == nil {
		return 0
	}
	return s.eigenvector[id]
}

// GetHubScore returns the hub score for a single issue.
func (s *GraphStats) GetHubScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hubs == nil {
		return 0
	}
	return s.hubs[id]
}

// GetAuthorityScore returns the authority score for a single issue.
func (s *GraphStats) GetAuthorityScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authorities == nil {
		return 0
	}
	return s.authorities[id]
}

// GetCriticalPathScore returns the critical path score for a single issue.
func (s *GraphStats) GetCriticalPathScore(id string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.criticalPathScore == nil {
		return 0
	}
	return s.criticalPathScore[id]
}

// -----------------------------------------------------------------------------
// Single-Value Accessor Pattern (bv-4jfr)
//
// These accessors provide O(1) lookups for individual values without copying
// the entire map. Use these when you need a single value.
//
// Pattern:
//   - *Value(id) (T, bool) - Returns value and existence flag
//   - *All(fn) - Iterator over all values (caller decides when to stop)
//
// The older map-copy methods (PageRank(), Betweenness(), etc.) are retained
// for backward compatibility but should be avoided in hot paths.
// -----------------------------------------------------------------------------

// PageRankValue returns the PageRank score for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
// Time complexity: O(1)
// Thread-safe: Yes (uses RLock)
func (s *GraphStats) PageRankValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pageRank == nil {
		return 0, false
	}
	v, ok := s.pageRank[id]
	return v, ok
}

// Phase-2 metric maps are built off-lock and published once under mu. They are
// immutable after publication, so iterator methods can capture a map pointer
// under RLock and invoke caller-controlled callbacks after releasing the lock.

// PageRankAll iterates over all PageRank scores.
// The callback receives each (id, score) pair. Return false to stop iteration.
// Time complexity: O(n) for full iteration
// Thread-safe: Yes (captures the immutable published map under RLock)
func (s *GraphStats) PageRankAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.pageRank
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// BetweennessValue returns the betweenness centrality for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
// Time complexity: O(1)
func (s *GraphStats) BetweennessValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.betweenness == nil {
		return 0, false
	}
	v, ok := s.betweenness[id]
	return v, ok
}

// BetweennessAll iterates over all betweenness scores.
func (s *GraphStats) BetweennessAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.betweenness
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// EigenvectorValue returns the eigenvector centrality for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) EigenvectorValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.eigenvector == nil {
		return 0, false
	}
	v, ok := s.eigenvector[id]
	return v, ok
}

// EigenvectorAll iterates over all eigenvector scores.
func (s *GraphStats) EigenvectorAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.eigenvector
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// HubValue returns the hub score for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) HubValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hubs == nil {
		return 0, false
	}
	v, ok := s.hubs[id]
	return v, ok
}

// HubsAll iterates over all hub scores.
func (s *GraphStats) HubsAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.hubs
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// AuthorityValue returns the authority score for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) AuthorityValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authorities == nil {
		return 0, false
	}
	v, ok := s.authorities[id]
	return v, ok
}

// AuthoritiesAll iterates over all authority scores.
func (s *GraphStats) AuthoritiesAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.authorities
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// CriticalPathValue returns the critical path score for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) CriticalPathValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.criticalPathScore == nil {
		return 0, false
	}
	v, ok := s.criticalPathScore[id]
	return v, ok
}

// CriticalPathAll iterates over all critical path scores.
func (s *GraphStats) CriticalPathAll(fn func(id string, score float64) bool) {
	s.mu.RLock()
	values := s.criticalPathScore
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, score := range values {
		if !fn(id, score) {
			return
		}
	}
}

// CoreNumberValue returns the k-core number for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) CoreNumberValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.coreNumber == nil {
		return 0, false
	}
	v, ok := s.coreNumber[id]
	return v, ok
}

// CoreNumberAll iterates over all k-core numbers.
func (s *GraphStats) CoreNumberAll(fn func(id string, core int) bool) {
	s.mu.RLock()
	values := s.coreNumber
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, core := range values {
		if !fn(id, core) {
			return
		}
	}
}

// SlackValue returns the slack value for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) SlackValue(id string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.slack == nil {
		return 0, false
	}
	v, ok := s.slack[id]
	return v, ok
}

// SlackAll iterates over all slack values.
func (s *GraphStats) SlackAll(fn func(id string, slack float64) bool) {
	s.mu.RLock()
	values := s.slack
	s.mu.RUnlock()
	if values == nil {
		return
	}
	for id, slack := range values {
		if !fn(id, slack) {
			return
		}
	}
}

// IsArticulationPoint returns whether the issue is an articulation point.
// Returns (false, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) IsArticulationPoint(id string) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.articulation == nil {
		return false, false
	}
	v, ok := s.articulation[id]
	return v, ok
}

// Rank accessor Value methods

// PageRankRankValue returns the PageRank rank (1-based) for a single issue.
// Returns (0, false) if the issue is not found or Phase 2 is not complete.
func (s *GraphStats) PageRankRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pageRankRank == nil {
		return 0, false
	}
	v, ok := s.pageRankRank[id]
	return v, ok
}

// BetweennessRankValue returns the betweenness rank (1-based) for a single issue.
func (s *GraphStats) BetweennessRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.betweennessRank == nil {
		return 0, false
	}
	v, ok := s.betweennessRank[id]
	return v, ok
}

// EigenvectorRankValue returns the eigenvector rank (1-based) for a single issue.
func (s *GraphStats) EigenvectorRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.eigenvectorRank == nil {
		return 0, false
	}
	v, ok := s.eigenvectorRank[id]
	return v, ok
}

// HubsRankValue returns the hub rank (1-based) for a single issue.
func (s *GraphStats) HubsRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hubsRank == nil {
		return 0, false
	}
	v, ok := s.hubsRank[id]
	return v, ok
}

// AuthoritiesRankValue returns the authority rank (1-based) for a single issue.
func (s *GraphStats) AuthoritiesRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authoritiesRank == nil {
		return 0, false
	}
	v, ok := s.authoritiesRank[id]
	return v, ok
}

// CriticalPathRankValue returns the critical path rank (1-based) for a single issue.
func (s *GraphStats) CriticalPathRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.criticalPathRank == nil {
		return 0, false
	}
	v, ok := s.criticalPathRank[id]
	return v, ok
}

// InDegreeRankValue returns the in-degree rank (1-based) for a single issue.
func (s *GraphStats) InDegreeRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.inDegreeRank == nil {
		return 0, false
	}
	v, ok := s.inDegreeRank[id]
	return v, ok
}

// OutDegreeRankValue returns the out-degree rank (1-based) for a single issue.
func (s *GraphStats) OutDegreeRankValue(id string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.outDegreeRank == nil {
		return 0, false
	}
	v, ok := s.outDegreeRank[id]
	return v, ok
}

// -----------------------------------------------------------------------------
// Map Copy Accessors (Legacy)
//
// These methods return full copies of internal maps. They are retained for
// backward compatibility but should be avoided in hot paths where only a
// single value is needed. Prefer the *Value() accessors above.
// -----------------------------------------------------------------------------

// PageRank returns a copy of the PageRank map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
//
// Deprecated: For single-value lookups, use PageRankValue() instead.
// For iteration, use PageRankAll(). This method copies O(n) data.
func (s *GraphStats) PageRank() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pageRank == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.pageRank))
	for k, v := range s.pageRank {
		cp[k] = v
	}
	return cp
}

// Betweenness returns a copy of the Betweenness map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
func (s *GraphStats) Betweenness() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.betweenness == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.betweenness))
	for k, v := range s.betweenness {
		cp[k] = v
	}
	return cp
}

// Eigenvector returns a copy of the Eigenvector map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
func (s *GraphStats) Eigenvector() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.eigenvector == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.eigenvector))
	for k, v := range s.eigenvector {
		cp[k] = v
	}
	return cp
}

// Hubs returns a copy of the Hubs map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
func (s *GraphStats) Hubs() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hubs == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.hubs))
	for k, v := range s.hubs {
		cp[k] = v
	}
	return cp
}

// Authorities returns a copy of the Authorities map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
func (s *GraphStats) Authorities() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authorities == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.authorities))
	for k, v := range s.authorities {
		cp[k] = v
	}
	return cp
}

// CriticalPathScore returns a copy of the CriticalPathScore map. Safe for concurrent iteration.
// Returns an empty map if Phase 2 is not yet complete.
func (s *GraphStats) CriticalPathScore() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.criticalPathScore == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.criticalPathScore))
	for k, v := range s.criticalPathScore {
		cp[k] = v
	}
	return cp
}

// CoreNumber returns k-core numbers per node (undirected view).
func (s *GraphStats) CoreNumber() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.coreNumber == nil {
		return nil
	}
	cp := make(map[string]int, len(s.coreNumber))
	for k, v := range s.coreNumber {
		cp[k] = v
	}
	return cp
}

// ArticulationPoints returns articulation points (cut vertices) detected on the undirected view.
func (s *GraphStats) ArticulationPoints() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.articulation == nil {
		return nil
	}
	var points []string
	for k, v := range s.articulation {
		if v {
			points = append(points, k)
		}
	}
	sort.Strings(points)
	return points
}

// Slack returns per-node slack (longest-path slack; 0 on critical path).
func (s *GraphStats) Slack() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.slack == nil {
		return nil
	}
	cp := make(map[string]float64, len(s.slack))
	for k, v := range s.slack {
		cp[k] = v
	}
	return cp
}

// Ranks accessors

func (s *GraphStats) PageRankRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pageRankRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.pageRankRank))
	for k, v := range s.pageRankRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) BetweennessRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.betweennessRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.betweennessRank))
	for k, v := range s.betweennessRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) EigenvectorRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.eigenvectorRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.eigenvectorRank))
	for k, v := range s.eigenvectorRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) HubsRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hubsRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.hubsRank))
	for k, v := range s.hubsRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) AuthoritiesRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authoritiesRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.authoritiesRank))
	for k, v := range s.authoritiesRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) CriticalPathRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.criticalPathRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.criticalPathRank))
	for k, v := range s.criticalPathRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) InDegreeRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.inDegreeRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.inDegreeRank))
	for k, v := range s.inDegreeRank {
		cp[k] = v
	}
	return cp
}

func (s *GraphStats) OutDegreeRank() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.outDegreeRank == nil {
		return nil
	}
	cp := make(map[string]int, len(s.outDegreeRank))
	for k, v := range s.outDegreeRank {
		cp[k] = v
	}
	return cp
}

// Cycles returns the stored representative cycle for each detected cyclic
// component, subject to the configured storage cap. Safe for concurrent
// iteration.
// Returns nil if Phase 2 is not yet complete.
func (s *GraphStats) Cycles() [][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cycles == nil {
		return nil
	}
	cp := make([][]string, len(s.cycles))
	for i, c := range s.cycles {
		cp[i] = append([]string(nil), c...)
	}
	return cp
}

// NewGraphStatsForTest creates a GraphStats with the given data for testing.
// This allows tests to create GraphStats with specific values without needing
// to run the full analyzer.
func NewGraphStatsForTest(
	pageRank, betweenness, eigenvector, hubs, authorities, criticalPathScore map[string]float64,
	outDegree, inDegree map[string]int,
	cycles [][]string,
	density float64,
	topologicalOrder []string,
) *GraphStats {
	stats := &GraphStats{
		OutDegree:         outDegree,
		InDegree:          inDegree,
		TopologicalOrder:  topologicalOrder,
		Density:           density,
		phase2Done:        make(chan struct{}),
		pageRank:          pageRank,
		betweenness:       betweenness,
		eigenvector:       eigenvector,
		hubs:              hubs,
		authorities:       authorities,
		criticalPathScore: criticalPathScore,
		cycles:            cycles,
		phase2Ready:       true,
		status: MetricStatus{
			PageRank:     testMetricStatus(pageRank != nil),
			Betweenness:  testMetricStatus(betweenness != nil),
			Eigenvector:  testMetricStatus(eigenvector != nil),
			HITS:         testMetricStatus(hubs != nil || authorities != nil),
			Critical:     testMetricStatus(criticalPathScore != nil),
			Cycles:       testMetricStatus(cycles != nil),
			KCore:        statusEntry{State: "skipped"},
			Articulation: statusEntry{State: "skipped"},
			Slack:        statusEntry{State: "skipped"},
		},
	}
	close(stats.phase2Done)
	return stats
}

func testMetricStatus(computed bool) statusEntry {
	if computed {
		return statusEntry{State: "computed"}
	}
	return statusEntry{State: "skipped"}
}

const (
	incrementalGraphStatsCacheTTL        = 5 * time.Minute
	incrementalGraphStatsCacheMaxEntries = 8
)

type incrementalGraphStatsCacheEntry struct {
	stats      *GraphStats
	insertedAt time.Time
}

var (
	incrementalGraphStatsCacheMu sync.Mutex
	incrementalGraphStatsCache   = make(map[string]incrementalGraphStatsCacheEntry)
)

func getIncrementalGraphStatsCache(key string) (*GraphStats, bool) {
	now := time.Now()

	incrementalGraphStatsCacheMu.Lock()
	defer incrementalGraphStatsCacheMu.Unlock()

	pruneIncrementalGraphStatsCacheLocked(now)

	entry, ok := incrementalGraphStatsCache[key]
	if !ok || !graphStatsReadyForCache(entry.stats) {
		delete(incrementalGraphStatsCache, key)
		return nil, false
	}
	if !cacheTimestampIsFresh(entry.insertedAt, now, incrementalGraphStatsCacheTTL) {
		delete(incrementalGraphStatsCache, key)
		return nil, false
	}
	return entry.stats, true
}

func putIncrementalGraphStatsCache(key string, stats *GraphStats) {
	if key == "" || !graphStatsReadyForCache(stats) {
		return
	}

	incrementalGraphStatsCacheMu.Lock()
	defer incrementalGraphStatsCacheMu.Unlock()

	now := time.Now()
	incrementalGraphStatsCache[key] = incrementalGraphStatsCacheEntry{
		stats:      stats,
		insertedAt: now,
	}
	pruneIncrementalGraphStatsCacheLocked(now)
}

func pruneIncrementalGraphStatsCacheLocked(now time.Time) {
	for k, entry := range incrementalGraphStatsCache {
		if entry.stats == nil || !cacheTimestampIsFresh(entry.insertedAt, now, incrementalGraphStatsCacheTTL) {
			delete(incrementalGraphStatsCache, k)
		}
	}

	for len(incrementalGraphStatsCache) > incrementalGraphStatsCacheMaxEntries {
		var (
			oldestKey string
			oldestAt  time.Time
			hasOldest bool
		)
		for k, entry := range incrementalGraphStatsCache {
			if !hasOldest || entry.insertedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = entry.insertedAt
				hasOldest = true
			}
		}
		if !hasOldest || oldestKey == "" {
			return
		}
		delete(incrementalGraphStatsCache, oldestKey)
	}
}

// directedGraph captures the graph capabilities needed by the analyzer.
// Gonum's graph.Directed interface doesn't include Edges(), so we add it here.
type directedGraph interface {
	graph.Directed
	Edges() graph.Edges
}

// compactDirectedGraph is a low-allocation directed graph optimized for
// immutable analysis workloads. It uses adjacency lists instead of map-backed
// edge sets to reduce allocation overhead during graph construction.
type compactDirectedGraph struct {
	nodes     []graph.Node
	out       [][]int64
	in        [][]int64
	edgeCount int
}

func newCompactDirectedGraph(nodeCount int) *compactDirectedGraph {
	nodes := make([]graph.Node, nodeCount)
	for i := range nodes {
		nodes[i] = simple.Node(int64(i))
	}
	return &compactDirectedGraph{
		nodes: nodes,
		out:   make([][]int64, nodeCount),
		in:    make([][]int64, nodeCount),
	}
}

func (g *compactDirectedGraph) addEdge(from, to int64) {
	if from < 0 || to < 0 {
		return
	}
	if int(from) >= len(g.nodes) || int(to) >= len(g.nodes) {
		return
	}
	g.out[from] = append(g.out[from], to)
	g.in[to] = append(g.in[to], from)
	g.edgeCount++
}

func (g *compactDirectedGraph) Node(id int64) graph.Node {
	if id < 0 || int(id) >= len(g.nodes) {
		return nil
	}
	return g.nodes[id]
}

func (g *compactDirectedGraph) Nodes() graph.Nodes {
	if len(g.nodes) == 0 {
		return graph.Empty
	}
	return &compactNodes{nodes: g.nodes, idx: -1}
}

func (g *compactDirectedGraph) From(id int64) graph.Nodes {
	if id < 0 || int(id) >= len(g.out) {
		return graph.Empty
	}
	ids := g.out[id]
	if len(ids) == 0 {
		return graph.Empty
	}
	return &compactIDNodes{nodes: g.nodes, ids: ids, idx: -1}
}

func (g *compactDirectedGraph) To(id int64) graph.Nodes {
	if id < 0 || int(id) >= len(g.in) {
		return graph.Empty
	}
	ids := g.in[id]
	if len(ids) == 0 {
		return graph.Empty
	}
	return &compactIDNodes{nodes: g.nodes, ids: ids, idx: -1}
}

func (g *compactDirectedGraph) HasEdgeFromTo(uid, vid int64) bool {
	if uid < 0 || int(uid) >= len(g.out) {
		return false
	}
	for _, to := range g.out[uid] {
		if to == vid {
			return true
		}
	}
	return false
}

func (g *compactDirectedGraph) HasEdgeBetween(xid, yid int64) bool {
	return g.HasEdgeFromTo(xid, yid) || g.HasEdgeFromTo(yid, xid)
}

func (g *compactDirectedGraph) Edge(uid, vid int64) graph.Edge {
	if !g.HasEdgeFromTo(uid, vid) {
		return nil
	}
	if uid < 0 || vid < 0 || int(uid) >= len(g.nodes) || int(vid) >= len(g.nodes) {
		return nil
	}
	return simple.Edge{F: g.nodes[uid], T: g.nodes[vid]}
}

func (g *compactDirectedGraph) Edges() graph.Edges {
	if g.edgeCount == 0 {
		return graph.Empty
	}
	return &compactEdges{g: g, fromIdx: 0, toIdx: -1}
}

type compactNodes struct {
	nodes []graph.Node
	idx   int
}

func (it *compactNodes) Next() bool {
	if it.idx+1 >= len(it.nodes) {
		return false
	}
	it.idx++
	return true
}

func (it *compactNodes) Node() graph.Node {
	if it.idx < 0 || it.idx >= len(it.nodes) {
		return nil
	}
	return it.nodes[it.idx]
}

func (it *compactNodes) Len() int {
	return len(it.nodes)
}

func (it *compactNodes) Reset() {
	it.idx = -1
}

type compactIDNodes struct {
	nodes []graph.Node
	ids   []int64
	idx   int
}

func (it *compactIDNodes) Next() bool {
	if it.idx+1 >= len(it.ids) {
		return false
	}
	it.idx++
	return true
}

func (it *compactIDNodes) Node() graph.Node {
	if it.idx < 0 || it.idx >= len(it.ids) {
		return nil
	}
	id := it.ids[it.idx]
	if id < 0 || int(id) >= len(it.nodes) {
		return nil
	}
	return it.nodes[id]
}

func (it *compactIDNodes) Len() int {
	return len(it.ids)
}

func (it *compactIDNodes) Reset() {
	it.idx = -1
}

type compactEdges struct {
	g       *compactDirectedGraph
	fromIdx int
	toIdx   int
}

func (it *compactEdges) Next() bool {
	for it.fromIdx < len(it.g.out) {
		it.toIdx++
		if it.toIdx < len(it.g.out[it.fromIdx]) {
			return true
		}
		it.fromIdx++
		it.toIdx = -1
	}
	return false
}

func (it *compactEdges) Edge() graph.Edge {
	if it.fromIdx < 0 || it.fromIdx >= len(it.g.out) {
		return nil
	}
	if it.toIdx < 0 || it.toIdx >= len(it.g.out[it.fromIdx]) {
		return nil
	}
	toID := it.g.out[it.fromIdx][it.toIdx]
	if toID < 0 || int(toID) >= len(it.g.nodes) {
		return nil
	}
	fromID := int64(it.fromIdx)
	return simple.Edge{F: it.g.nodes[fromID], T: it.g.nodes[toID]}
}

func (it *compactEdges) Len() int {
	return it.g.edgeCount
}

func (it *compactEdges) Reset() {
	it.fromIdx = 0
	it.toIdx = -1
}

// Analyzer encapsulates the graph logic
type Analyzer struct {
	g                directedGraph
	idToNode         map[string]int64
	nodeToID         map[int64]string
	issueMap         map[string]model.Issue
	issues           []model.Issue // Analyzer-owned snapshot retained for data-hash memoization
	childrenByParent map[string][]string
	blockerCounts    []int
	blockerCountsMax int
	config           *AnalysisConfig // Optional custom config, nil means use size-based defaults
	readinessOnce    sync.Once
	readiness        *model.ReadinessIndex
	candidateIDs     map[string]bool // nil = every analysis issue; empty = no candidates

	// now is the reference instant for time-gated readiness and scoring.
	// Defaults to wall-clock time at construction; robot entrypoints with a
	// pinned clock (SOURCE_DATE_EPOCH) override it via SetNow so their output
	// stays deterministic.
	now time.Time

	// weights are the composite-score factor weights (priority.go). Unset
	// means DefaultWeights; SetWeights installs feedback-adjusted values.
	weights    Weights
	weightsSet bool

	dataHashOnce sync.Once
	dataHash     string
}

// SetReadinessScope separates dependency authority and candidate eligibility
// from the graph used for scoped metrics. Set it before starting analysis.
func (a *Analyzer) SetReadinessScope(authority *model.ReadinessIndex, candidateIDs map[string]bool) {
	a.readinessOnce.Do(func() {
		if authority == nil {
			authority = model.NewReadinessIndex(a.issues)
		}
		a.readiness = authority
	})
	if candidateIDs != nil {
		a.candidateIDs = make(map[string]bool, len(candidateIDs))
		for id, selected := range candidateIDs {
			a.candidateIDs[id] = selected
		}
	}
}

func (a *Analyzer) Readiness() *model.ReadinessIndex {
	a.readinessOnce.Do(func() { a.readiness = model.NewReadinessIndex(a.issues) })
	return a.readiness
}

// IsCandidate reports output eligibility independently of dependency state.
func (a *Analyzer) IsCandidate(id string) bool {
	return a.candidateIDs == nil || a.candidateIDs[id]
}

// SetNow overrides the reference instant used for time-gated readiness checks
// and time-sensitive scoring. The zero Go time is valid because
// SOURCE_DATE_EPOCH=-62135596800 maps to it exactly.
func (a *Analyzer) SetNow(now time.Time) {
	a.now = now
}

// Now returns the analyzer's reference instant for readiness and scoring.
func (a *Analyzer) Now() time.Time {
	return a.now
}

// MatchesIssues checks exact source rows independently of their outer ordering.
// Unlike DataHash, this preserves nested ordering and nil/empty distinctions.
// Duplicate IDs are rejected because they cannot identify one unambiguous row.
// Callers must not mutate issues while this check runs.
func (a *Analyzer) MatchesIssues(issues []model.Issue) bool {
	if a == nil || len(issues) != len(a.issues) {
		return false
	}
	rows := make(map[string]*model.Issue, len(a.issues))
	for i := range a.issues {
		row := &a.issues[i]
		if _, exists := rows[row.ID]; exists {
			return false
		}
		rows[row.ID] = row
	}
	for i := range issues {
		row := &issues[i]
		source, exists := rows[row.ID]
		if !exists || !reflect.DeepEqual(source, row) {
			return false
		}
		delete(rows, row.ID)
	}
	return true
}

// SeedDataHash records a pre-computed ComputeDataHash for the analyzer's input
// issues, so the disk-cache key path reuses it instead of running the SHA256
// again. The caller MUST guarantee hash == ComputeDataHash(theInputIssues);
// passing a hash for a different issue set would corrupt the analysis cache key.
// An empty hash is ignored (DataHash then falls back to computing it).
func (a *Analyzer) SeedDataHash(hash string) {
	if hash == "" {
		return
	}
	a.dataHashOnce.Do(func() {
		a.dataHash = hash
	})
}

// DataHash returns ComputeDataHash for the analyzer's input issues, computed at
// most once per Analyzer. The analyzer owns its issue set for its lifetime, so
// the value is stable; callers that recompute the same hash (e.g. the robot
// disk-cache key path) reuse this result instead of re-running the SHA256.
//
// The emitted value is byte-identical to ComputeDataHash(issues): ComputeDataHash
// sorts by ID, so hashing the retained slice (or, as a fallback, a slice rebuilt
// from issueMap) yields the same digest.
func (a *Analyzer) DataHash() string {
	a.dataHashOnce.Do(func() {
		issues := a.issues
		if issues == nil {
			issues = make([]model.Issue, 0, len(a.issueMap))
			for _, issue := range a.issueMap {
				issues = append(issues, issue)
			}
		}
		a.dataHash = ComputeDataHash(issues)
	})
	return a.dataHash
}

// SetConfig sets a custom analysis configuration.
// Pass nil to use size-based automatic configuration.
func (a *Analyzer) SetConfig(config *AnalysisConfig) {
	a.config = config
}

func (a *Analyzer) graphStructureHash() string {
	if a == nil || a.g == nil {
		return "none"
	}

	nodesIt := a.g.Nodes()
	ids := make([]string, 0, nodesIt.Len())
	for nodesIt.Next() {
		id, ok := a.nodeToID[nodesIt.Node().ID()]
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	type edgeKey struct {
		from string
		to   string
	}
	edgesIt := a.g.Edges()
	edges := make([]edgeKey, 0, edgesIt.Len())
	for edgesIt.Next() {
		e := edgesIt.Edge()
		from := a.nodeToID[e.From().ID()]
		to := a.nodeToID[e.To().ID()]
		if from == "" || to == "" {
			continue
		}
		edges = append(edges, edgeKey{from: from, to: to})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	edgesDedup := edges[:0]
	for i := range edges {
		if i == 0 || edges[i] != edges[i-1] {
			edgesDedup = append(edgesDedup, edges[i])
		}
	}

	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	h.Write([]byte{1}) // nodes/edges separator
	for _, e := range edgesDedup {
		h.Write([]byte(e.from))
		h.Write([]byte{0})
		h.Write([]byte(e.to))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}

// cycleEligibilityHash fingerprints the set of issues that can participate in
// an execution-blocking cycle. The general graph cache otherwise keys only on
// nodes and edges, but closing one member changes cycle semantics without
// changing that topology.
func (a *Analyzer) cycleEligibilityHash() string {
	ids := make([]string, 0, len(a.issueMap))
	for id, issue := range a.issueMap {
		if !isClosedLikeStatus(issue.Status) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// activeCycleGraph returns the blocking graph induced by open-like issues.
// Closed and tombstoned issues remain in a.g for historical centrality, but
// they no longer block execution and therefore must break operational cycles.
func (a *Analyzer) activeCycleGraph() directedGraph {
	hasClosed := false
	for _, issue := range a.issueMap {
		if isClosedLikeStatus(issue.Status) {
			hasClosed = true
			break
		}
	}
	if !hasClosed {
		return a.g
	}

	active := newCompactDirectedGraph(len(a.nodeToID))
	edges := a.g.Edges()
	for edges.Next() {
		edge := edges.Edge()
		fromID := a.nodeToID[edge.From().ID()]
		toID := a.nodeToID[edge.To().ID()]
		from, fromExists := a.issueMap[fromID]
		to, toExists := a.issueMap[toID]
		if !fromExists || !toExists || isClosedLikeStatus(from.Status) || isClosedLikeStatus(to.Status) {
			continue
		}
		active.addEdge(edge.From().ID(), edge.To().ID())
	}
	return active
}

func NewAnalyzer(issues []model.Issue) *Analyzer {
	// Issue IDs are the graph's semantic identity. Assign compact numeric IDs in
	// sorted issue-ID order so cache-equivalent input permutations cannot change
	// sampled pivots, traversal order, floating-point reduction, or cycle choice.
	// Malformed duplicate IDs retain the loader's last-record-wins semantics.
	ownedIssues := make([]model.Issue, len(issues))
	issueMap := make(map[string]model.Issue, len(issues))
	for i, issue := range issues {
		ownedIssues[i] = issue.Clone()
		issueMap[ownedIssues[i].ID] = ownedIssues[i]
	}
	sortedIssueIDs := make([]string, 0, len(issueMap))
	for id := range issueMap {
		sortedIssueIDs = append(sortedIssueIDs, id)
	}
	sort.Strings(sortedIssueIDs)

	g := newCompactDirectedGraph(len(sortedIssueIDs))
	idToNode := make(map[string]int64, len(sortedIssueIDs))
	nodeToID := make(map[int64]string, len(sortedIssueIDs))
	childrenByParent := make(map[string][]string)
	blockerCounts := make([]int, len(sortedIssueIDs))

	// 1. Add Nodes
	for idx, id := range sortedIssueIDs {
		nodeID := int64(idx)
		idToNode[id] = nodeID
		nodeToID[nodeID] = id
	}

	// 2. Add blocking edges and index parent-child rollups.
	// We only model *blocking* relationships in the analysis graph. Non-blocking
	// links such as "related" should not influence centrality metrics or cycle
	// detection because they do not gate execution order.
	seenByBlocker := make([]int, len(sortedIssueIDs))
	epoch := 0
	for _, issueID := range sortedIssueIDs {
		issue := issueMap[issueID]
		epoch++
		u, ok := idToNode[issueID]
		if !ok {
			continue
		}
		if len(issue.Dependencies) == 0 {
			continue
		}

		blockingTargets := make([]int64, 0, len(issue.Dependencies))
		var parentTargets []string
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}

			if dep.Type == model.DepParentChild {
				if _, exists := issueMap[dep.DependsOnID]; exists {
					parentTargets = append(parentTargets, dep.DependsOnID)
				}
			}

			// Only model blocking relationships in the analysis graph
			if !dep.Type.IsBlocking() {
				continue
			}

			v, exists := idToNode[dep.DependsOnID]
			if !exists {
				continue
			}
			blockingTargets = append(blockingTargets, v)
		}
		sort.Strings(parentTargets)
		for i, parentID := range parentTargets {
			if i == 0 || parentID != parentTargets[i-1] {
				childrenByParent[parentID] = append(childrenByParent[parentID], issueID)
			}
		}
		slices.Sort(blockingTargets)
		for _, v := range blockingTargets {
			if v < 0 || int(v) >= len(blockerCounts) {
				continue
			}
			if seenByBlocker[v] == epoch {
				continue
			}
			seenByBlocker[v] = epoch
			// Blocker ratio is defined as direct dependents (graph in-degree),
			// so repeated dependency records from one issue count only once.
			blockerCounts[v]++

			// Issue (u) depends on v → edge u -> v
			g.addEdge(u, v)
		}
	}

	maxBlockers := 0
	for _, count := range blockerCounts {
		if count > maxBlockers {
			maxBlockers = count
		}
	}

	return &Analyzer{
		g:                g,
		idToNode:         idToNode,
		nodeToID:         nodeToID,
		issueMap:         issueMap,
		issues:           ownedIssues,
		childrenByParent: childrenByParent,
		blockerCounts:    blockerCounts,
		blockerCountsMax: maxBlockers,
		now:              time.Now(),
	}
}

// AnalyzeAsync performs graph analysis in two phases for fast startup.
// Phase 1 (instant): Degree centrality, topological order, density
// Phase 2 (background): PageRank, Betweenness, Eigenvector, HITS, Cycles
// Returns immediately with Phase 1 data. Use IsPhase2Ready() or WaitForPhase2() for Phase 2.
//
// If SetConfig was called, uses that config. Otherwise uses ConfigForSize() to
// automatically select appropriate algorithms based on graph size.
func (a *Analyzer) AnalyzeAsync(ctx context.Context) *GraphStats {
	var config AnalysisConfig
	if a.config != nil {
		config = *a.config
	} else {
		nodeCount := len(a.issueMap)
		edgeCount := a.g.Edges().Len()
		config = ConfigForSize(nodeCount, edgeCount)
	}
	return a.AnalyzeAsyncWithConfig(ctx, config)
}

// AnalyzeAsyncWithConfig performs graph analysis with a custom configuration.
// This allows callers to override the default size-based algorithm selection.
func (a *Analyzer) AnalyzeAsyncWithConfig(ctx context.Context, config AnalysisConfig) *GraphStats {
	if ctx == nil {
		ctx = context.Background()
	}

	nodeCount := len(a.issueMap)
	edgeCount := a.g.Edges().Len()

	var configHash, incCacheKey, robotCacheKey, dataHash string
	if !config.DisableCache {
		configHash = ComputeConfigHash(&config)
		if robotDiskCacheEnabled() {
			// Reuse the analyzer-scoped memoized hash instead of rebuilding a slice
			// from issueMap and re-running the SHA256 on every analysis. Identical
			// value (ComputeDataHash sorts by ID), one computation per analyzer.
			dataHash = a.DataHash()
			robotCacheKey = dataHash + "|" + configHash

			if cached, xfetchRefresh, ok := getRobotDiskCachedStats(robotCacheKey); ok {
				if !xfetchRefresh || ctx.Err() != nil {
					metrics.GraphCache.Hit()
					return cached
				}
				// XFetch selected this caller to refresh early. Fall through and
				// recompute now so the cache is actually renewed instead of serving
				// the stale entry forever.
			}
		} else {
			incCacheKey = a.graphStructureHash()
			if config.ComputeCycles {
				incCacheKey += "|cycles:" + a.cycleEligibilityHash()
			}
			incCacheKey += "|" + configHash
			if cached, ok := getIncrementalGraphStatsCache(incCacheKey); ok {
				return cached
			}
		}
	}

	stats := &GraphStats{
		OutDegree:         make(map[string]int),
		InDegree:          make(map[string]int),
		NodeCount:         nodeCount,
		EdgeCount:         edgeCount,
		Config:            config,
		phase2Done:        make(chan struct{}),
		pageRank:          make(map[string]float64),
		betweenness:       make(map[string]float64),
		eigenvector:       make(map[string]float64),
		hubs:              make(map[string]float64),
		authorities:       make(map[string]float64),
		criticalPathScore: make(map[string]float64),
		status: MetricStatus{
			PageRank:     statusEntry{State: "pending"},
			Betweenness:  statusEntry{State: "pending"},
			Eigenvector:  statusEntry{State: "pending"},
			HITS:         statusEntry{State: "pending"},
			Critical:     statusEntry{State: "pending"},
			Cycles:       statusEntry{State: "pending"},
			KCore:        statusEntry{State: "pending"},
			Articulation: statusEntry{State: "pending"},
			Slack:        statusEntry{State: "pending"},
		},
	}

	// Handle empty graph - mark phase 2 ready immediately
	if nodeCount == 0 {
		stats.status = emptyGraphMetricStatus(config)
		stats.phase2Ready = true
		close(stats.phase2Done)
		return stats
	}

	// Phase 1: Fast metrics (degree centrality, topo sort, density)
	if robotCacheKey != "" {
		metrics.GraphCache.Miss() // the disk cache had no usable entry
	}
	stopPhase1 := metrics.Timer(metrics.AnalysisPhase1)
	a.computePhase1(stats)
	stopPhase1()

	// bv-perf: Skip Phase 2 entirely when all metrics are disabled.
	// This avoids goroutine overhead when Phase 2 results aren't needed
	// (e.g., all issues are closed, so no triage scoring required).
	if config.AllPhase2Disabled() {
		stats.status = MetricStatus{
			PageRank:     statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Betweenness:  statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Eigenvector:  statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			HITS:         statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Critical:     statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Cycles:       statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			KCore:        statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Articulation: statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
			Slack:        statusEntry{State: "skipped", Reason: "all phase 2 disabled"},
		}
		stats.phase2Ready = true
		close(stats.phase2Done)
		putIncrementalGraphStatsCache(incCacheKey, stats)
		return stats
	}

	// Phase 2: Expensive metrics in background goroutine
	go a.computePhase2(ctx, stats, config, incCacheKey, robotCacheKey, dataHash, configHash)

	return stats
}

// Analyze performs synchronous graph analysis (for backward compatibility).
// Blocks until all metrics are computed.
func (a *Analyzer) Analyze() GraphStats {
	stats := a.AnalyzeAsync(context.Background())
	stats.WaitForPhase2()
	// Return a copy with public fields populated for backward compatibility
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

// AnalyzeWithConfig performs synchronous graph analysis with a custom configuration.
func (a *Analyzer) AnalyzeWithConfig(config AnalysisConfig) GraphStats {
	stats := a.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()
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

// AnalyzeWithProfile performs synchronous graph analysis and returns detailed timing profile.
// This is intended for diagnostics and the --profile-startup CLI flag.
func (a *Analyzer) AnalyzeWithProfile(config AnalysisConfig) (*GraphStats, *StartupProfile) {
	profile := &StartupProfile{
		Config: config,
	}

	totalStart := time.Now()

	nodeCount := len(a.issueMap)
	edgeCount := a.g.Edges().Len()

	profile.NodeCount = nodeCount
	profile.EdgeCount = edgeCount

	stats := &GraphStats{
		OutDegree:         make(map[string]int),
		InDegree:          make(map[string]int),
		NodeCount:         nodeCount,
		EdgeCount:         edgeCount,
		Config:            config,
		phase2Done:        make(chan struct{}),
		pageRank:          make(map[string]float64),
		betweenness:       make(map[string]float64),
		eigenvector:       make(map[string]float64),
		hubs:              make(map[string]float64),
		authorities:       make(map[string]float64),
		criticalPathScore: make(map[string]float64),
	}

	// Handle empty graph
	if nodeCount == 0 {
		stats.status = emptyGraphMetricStatus(config)
		stats.phase2Ready = true
		close(stats.phase2Done)
		profile.Total = time.Since(totalStart)
		return stats, profile
	}

	// Phase 1: Fast metrics with timing
	phase1Start := time.Now()
	a.computePhase1WithProfile(stats, profile)
	profile.Phase1 = time.Since(phase1Start)

	profile.Density = stats.Density

	// Phase 2: Expensive metrics synchronously with timing
	phase2Start := time.Now()
	a.computePhase2WithProfile(context.Background(), stats, config, profile)
	profile.Phase2 = time.Since(phase2Start)

	stats.phase2Ready = true
	close(stats.phase2Done)

	profile.Total = time.Since(totalStart)
	return stats, profile
}

// computePhase1WithProfile calculates fast metrics with timing instrumentation.
func (a *Analyzer) computePhase1WithProfile(stats *GraphStats, profile *StartupProfile) {
	// Degree centrality
	degreeStart := time.Now()
	nodes := a.g.Nodes()
	for nodes.Next() {
		n := nodes.Node()
		id := a.nodeToID[n.ID()]
		to := a.g.To(n.ID())
		stats.InDegree[id] = to.Len()
		from := a.g.From(n.ID())
		stats.OutDegree[id] = from.Len()
	}
	profile.Degree = time.Since(degreeStart)

	// Topological Sort
	topoStart := time.Now()
	sorted, err := topo.Sort(a.g)
	if err == nil {
		for i := len(sorted) - 1; i >= 0; i-- {
			stats.TopologicalOrder = append(stats.TopologicalOrder, a.nodeToID[sorted[i].ID()])
		}
	}
	profile.TopoSort = time.Since(topoStart)

	// Density
	n := float64(len(a.issueMap))
	e := float64(a.g.Edges().Len())
	if n > 1 {
		stats.Density = e / (n * (n - 1))
	}
}

// computePhase2WithProfile calculates expensive metrics with timing instrumentation.
func (a *Analyzer) computePhase2WithProfile(ctx context.Context, stats *GraphStats, config AnalysisConfig, profile *StartupProfile) {
	if ctx.Err() != nil {
		return
	}

	localPageRank := make(map[string]float64)
	localBetweenness := make(map[string]float64)
	localEigenvector := make(map[string]float64)
	localHubs := make(map[string]float64)
	localAuthorities := make(map[string]float64)
	localCriticalPath := make(map[string]float64)
	var localCore map[string]int
	var localArticulation map[string]bool
	var localSlack map[string]float64
	var localCycles [][]string

	betweennessIsApprox := false
	actualBetweennessSample := 0
	cyclesTruncated := false
	criticalPathUnavailableReason := ""
	slackUnavailableReason := ""
	// RunToCompletion calls the four normally deadline-raced algorithms directly.
	// Their inputs and iteration order are deterministic; removing the scheduler
	// versus timer race makes the resulting values and status deterministic too.

	// PageRank
	if ctx.Err() == nil && config.ComputePageRank {
		prStart := time.Now()
		if config.RunToCompletion {
			if pr, ok := runMetricSafely(func() map[int64]float64 {
				return computePageRank(a.g, 0.85, 1e-6)
			}); ok {
				for id, score := range pr {
					localPageRank[a.nodeToID[id]] = score
				}
			} else {
				// Match the legacy goroutine path: a recovered worker panic
				// eventually presents as a timed-out metric with uniform fallback.
				profile.PageRankTO = true
				uniform := 1.0 / float64(len(a.issueMap))
				for id := range a.issueMap {
					localPageRank[id] = uniform
				}
			}
		} else {
			prDone := make(chan map[int64]float64, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic -> implicitly causes timeout in parent
					}
				}()
				prDone <- computePageRank(a.g, 0.85, 1e-6)
			}()

			timer := time.NewTimer(config.PageRankTimeout)
			select {
			case pr := <-prDone:
				timer.Stop()
				for id, score := range pr {
					localPageRank[a.nodeToID[id]] = score
				}
			case <-timer.C:
				profile.PageRankTO = true
				if len(a.issueMap) > 0 {
					uniform := 1.0 / float64(len(a.issueMap))
					for id := range a.issueMap {
						localPageRank[id] = uniform
					}
				}
			case <-ctx.Done():
				timer.Stop()
				// Abort immediately
				return
			}
		}
		profile.PageRank = time.Since(prStart)
	}

	// Betweenness
	if ctx.Err() == nil && config.ComputeBetweenness {
		bwStart := time.Now()
		computeBetweenness := func() BetweennessResult {
			// Choose algorithm based on mode
			if config.BetweennessMode == BetweennessApproximate && config.BetweennessSampleSize > 0 {
				if config.RunToCompletion {
					return approxBetweennessDeterministic(a.g, config.BetweennessSampleSize, 1)
				}
				return ApproxBetweenness(a.g, config.BetweennessSampleSize, 1)
			}
			// Exact mode or mode not set (default to exact)
			return BetweennessResult{
				Scores:     network.Betweenness(a.g),
				Mode:       BetweennessExact,
				TotalNodes: a.g.Nodes().Len(),
			}
		}

		consumeBetweenness := func(result BetweennessResult) {
			for id, score := range result.Scores {
				localBetweenness[a.nodeToID[id]] = score
			}
			// Track if approximation was used
			if result.Mode == BetweennessApproximate {
				betweennessIsApprox = true
				actualBetweennessSample = result.SampleSize
			}
		}

		if config.RunToCompletion {
			if result, ok := runMetricSafely(computeBetweenness); ok {
				consumeBetweenness(result)
			} else {
				profile.BetweennessTO = true
			}
		} else {
			bwDone := make(chan BetweennessResult, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic -> implicitly causes timeout in parent
					}
				}()
				bwDone <- computeBetweenness()
			}()

			timer := time.NewTimer(config.BetweennessTimeout)
			select {
			case result := <-bwDone:
				timer.Stop()
				consumeBetweenness(result)
			case <-timer.C:
				profile.BetweennessTO = true
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		profile.Betweenness = time.Since(bwStart)
	}

	// Eigenvector
	if ctx.Err() == nil && config.ComputeEigenvector {
		evStart := time.Now()
		for id, score := range computeEigenvector(a.g) {
			localEigenvector[a.nodeToID[id]] = score
		}
		profile.Eigenvector = time.Since(evStart)
	}

	// HITS
	if ctx.Err() == nil && config.ComputeHITS && a.g.Edges().Len() > 0 {
		hitsStart := time.Now()
		consumeHITS := func(hubAuth map[int64]network.HubAuthority) {
			for id, ha := range hubAuth {
				localHubs[a.nodeToID[id]] = ha.Hub
				localAuthorities[a.nodeToID[id]] = ha.Authority
			}
		}
		if config.RunToCompletion {
			if hubAuth, ok := runMetricSafely(func() map[int64]network.HubAuthority {
				return network.HITS(a.g, 1e-3)
			}); ok {
				consumeHITS(hubAuth)
			} else {
				profile.HITSTO = true
			}
		} else {
			hitsDone := make(chan map[int64]network.HubAuthority, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic -> implicitly causes timeout in parent
					}
				}()
				hitsDone <- network.HITS(a.g, 1e-3)
			}()

			timer := time.NewTimer(config.HITSTimeout)
			select {
			case hubAuth := <-hitsDone:
				timer.Stop()
				consumeHITS(hubAuth)
			case <-timer.C:
				profile.HITSTO = true
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		profile.HITS = time.Since(hitsStart)
	}

	// Critical Path
	if ctx.Err() == nil && config.ComputeCriticalPath {
		cpStart := time.Now()
		if len(stats.TopologicalOrder) == len(a.issueMap) {
			// Phase 1 already paid for the topological sort. Its public order is
			// dependencies-first, so reverse it back to the orientation expected
			// by computeHeights instead of sorting the full graph a second time.
			sorted := make([]graph.Node, 0, len(stats.TopologicalOrder))
			for i := len(stats.TopologicalOrder) - 1; i >= 0; i-- {
				nodeID, ok := a.idToNode[stats.TopologicalOrder[i]]
				if !ok {
					continue
				}
				sorted = append(sorted, a.g.Node(nodeID))
			}
			localCriticalPath = a.computeHeights(sorted)
		} else {
			criticalPathUnavailableReason = "dependency graph contains a cycle; topological order unavailable"
		}
		profile.CriticalPath = time.Since(cpStart)
	}

	// Cycles
	if ctx.Err() == nil && config.ComputeCycles {
		cyclesStart := time.Now()
		maxCycles := config.MaxCyclesToStore
		if maxCycles <= 0 {
			maxCycles = 100
		}

		cycleGraph := a.activeCycleGraph()
		sccs := topo.TarjanSCC(cycleGraph)
		hasCycles := false
		for _, scc := range sccs {
			if len(scc) > 1 {
				hasCycles = true
				break
			}
			if len(scc) == 1 {
				n := scc[0]
				if cycleGraph.HasEdgeFromTo(n.ID(), n.ID()) {
					hasCycles = true
					break
				}
			}
		}

		if hasCycles {
			consumeCycles := func(result cycleDetectionResult) {
				profile.CycleCount = result.total
				cyclesTruncated = result.truncated

				for _, cycle := range result.cycles {
					var cycleIDs []string
					for _, n := range cycle {
						cycleIDs = append(cycleIDs, a.nodeToID[n.ID()])
					}
					localCycles = append(localCycles, cycleIDs)
				}
			}
			if config.RunToCompletion {
				if cycles, ok := runMetricSafely(func() cycleDetectionResult {
					return findCyclesSafe(cycleGraph, maxCycles)
				}); ok {
					consumeCycles(cycles)
				} else {
					profile.CyclesTO = true
				}
			} else {
				cyclesDone := make(chan cycleDetectionResult, 1)
				go func() {
					defer func() {
						if r := recover(); r != nil {
							// Panic -> implicitly causes timeout in parent
						}
					}()
					cyclesDone <- findCyclesSafe(cycleGraph, maxCycles)
				}()

				timer := time.NewTimer(config.CyclesTimeout)
				select {
				case cycles := <-cyclesDone:
					timer.Stop()
					consumeCycles(cycles)
				case <-timer.C:
					profile.CyclesTO = true
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
		}
		profile.Cycles = time.Since(cyclesStart)
	}

	// Check cancellation before advanced signals
	if ctx.Err() != nil {
		return
	}

	// Advanced graph signals: k-core, articulation points (undirected), slack (bv-85)
	// These can be skipped for triage-only mode (bv-t1js optimization)
	if config.ComputeKCore || config.ComputeArticulation {
		kcoreStart := time.Now()
		localCore, localArticulation = a.computeCoreAndArticulation()
		profile.KCore = time.Since(kcoreStart)
		profile.Articulation = 0 // Computed together with k-core
	}

	if config.ComputeSlack {
		slackStart := time.Now()
		if len(stats.TopologicalOrder) == len(a.issueMap) {
			localSlack = a.computeSlack(stats.TopologicalOrder)
		} else {
			slackUnavailableReason = "dependency graph contains a cycle; topological order unavailable"
		}
		profile.Slack = time.Since(slackStart)
	}
	if ctx.Err() != nil {
		return
	}

	// Compute ranks (background optimization)
	localPageRankRank := computeFloatRanks(localPageRank)
	localBetweennessRank := computeFloatRanks(localBetweenness)
	localEigenvectorRank := computeFloatRanks(localEigenvector)
	localHubsRank := computeFloatRanks(localHubs)
	localAuthoritiesRank := computeFloatRanks(localAuthorities)
	localCriticalPathRank := computeFloatRanks(localCriticalPath)

	// Atomic assignment
	stats.mu.Lock()
	// Rank construction can itself be material on a large graph. Honor a
	// cancellation that arrived after the preceding check instead of publishing
	// a completed-looking result after the caller abandoned the analysis.
	if ctx.Err() != nil {
		stats.mu.Unlock()
		return
	}
	stats.pageRank = localPageRank
	stats.betweenness = localBetweenness
	stats.eigenvector = localEigenvector
	stats.hubs = localHubs
	stats.authorities = localAuthorities
	stats.criticalPathScore = localCriticalPath
	stats.coreNumber = localCore
	stats.articulation = localArticulation
	stats.slack = localSlack
	stats.cycles = localCycles

	// Assign ranks
	stats.pageRankRank = localPageRankRank
	stats.betweennessRank = localBetweennessRank
	stats.eigenvectorRank = localEigenvectorRank
	stats.hubsRank = localHubsRank
	stats.authoritiesRank = localAuthoritiesRank
	stats.criticalPathRank = localCriticalPathRank

	stats.phase2Ready = true

	cycleReason := config.CyclesSkipReason
	if cyclesTruncated {
		if cycleReason != "" {
			cycleReason += "; "
		}
		cycleReason += fmt.Sprintf("truncated to %d of %d cycle representatives", len(localCycles), profile.CycleCount)
	}

	// record status snapshot
	kcoreComputed := config.ComputeKCore || config.ComputeArticulation
	articulationComputed := config.ComputeArticulation || config.ComputeKCore
	slackComputed := config.ComputeSlack

	criticalStatus := statusEntry{State: stateFromTiming(config.ComputeCriticalPath, false), Elapsed: profile.CriticalPath}
	if config.ComputeCriticalPath && criticalPathUnavailableReason != "" {
		criticalStatus.State = "skipped"
		criticalStatus.Reason = criticalPathUnavailableReason
	}
	slackStatus := statusEntry{State: stateFromTiming(slackComputed, false), Elapsed: profile.Slack}
	if slackComputed && slackUnavailableReason != "" {
		slackStatus.State = "skipped"
		slackStatus.Reason = slackUnavailableReason
	}

	stats.status = MetricStatus{
		PageRank: statusEntry{State: stateFromTiming(config.ComputePageRank, profile.PageRankTO), Elapsed: profile.PageRank},
		Betweenness: statusEntry{
			State:   stateFromTiming(config.ComputeBetweenness, profile.BetweennessTO),
			Reason:  betweennessReason(config, betweennessIsApprox),
			Sample:  actualBetweennessSample,
			Elapsed: profile.Betweenness,
		},
		Eigenvector:  statusEntry{State: stateFromTiming(config.ComputeEigenvector, false), Elapsed: profile.Eigenvector},
		HITS:         statusEntry{State: stateFromTiming(config.ComputeHITS, profile.HITSTO), Reason: config.HITSSkipReason, Elapsed: profile.HITS},
		Critical:     criticalStatus,
		Cycles:       statusEntry{State: stateFromTiming(config.ComputeCycles, profile.CyclesTO), Reason: cycleReason, Elapsed: profile.Cycles},
		KCore:        statusEntry{State: stateFromTiming(kcoreComputed, false), Elapsed: profile.KCore},
		Articulation: statusEntry{State: stateFromTiming(articulationComputed, false), Elapsed: profile.Articulation},
		Slack:        slackStatus,
	}
	stats.mu.Unlock()
}

// runMetricSafely preserves the panic containment of the ordinary goroutine
// path when reproducible mode executes a metric synchronously. A panic is
// reported as an incomplete computation so the caller can apply the same
// deterministic fallback/status it would use after the worker failed to send.
func runMetricSafely[T any](compute func() T) (result T, completed bool) {
	defer func() {
		if recover() != nil {
			completed = false
		}
	}()
	return compute(), true
}

// computePhase1 calculates fast metrics synchronously.
func (a *Analyzer) computePhase1(stats *GraphStats) {
	nodes := a.g.Nodes()

	// Basic Degree Centrality
	for nodes.Next() {
		n := nodes.Node()
		id := a.nodeToID[n.ID()]

		// Edge direction: dependent -> dependency (A -> B means A depends on B)
		// To(n) = nodes pointing TO n = issues that depend on n = n blocks them
		to := a.g.To(n.ID())
		stats.InDegree[id] = to.Len() // Issues depending on me

		// From(n) = nodes n points TO = issues n depends on
		from := a.g.From(n.ID())
		stats.OutDegree[id] = from.Len() // Issues I depend on
	}

	// Topological Sort (execution order)
	// Note: In our graph model, edge u -> v means u depends on v, so we reverse
	// topo.Sort's output to get dependencies-first ordering.
	sorted, err := topo.Sort(a.g)
	if err == nil {
		for i := len(sorted) - 1; i >= 0; i-- {
			stats.TopologicalOrder = append(stats.TopologicalOrder, a.nodeToID[sorted[i].ID()])
		}
	}

	// Density
	n := float64(len(a.issueMap))
	e := float64(a.g.Edges().Len())
	if n > 1 {
		stats.Density = e / (n * (n - 1))
	}

	// Compute Phase 1 Ranks
	stats.inDegreeRank = computeIntRanks(stats.InDegree)
	stats.outDegreeRank = computeIntRanks(stats.OutDegree)
}

// computePhase2 calculates expensive metrics in background.
// Computes to local variables first, then atomically assigns under lock.
// Respects the config to skip expensive algorithms for large graphs.
func (a *Analyzer) computePhase2(ctx context.Context, stats *GraphStats, config AnalysisConfig, incrementalCacheKey, robotCacheKey, dataHash, configHash string) {
	defer close(stats.phase2Done)
	defer metrics.Timer(metrics.AnalysisPhase2)()

	// Recover from panics to prevent crashing the entire application
	defer func() {
		if r := recover(); r != nil {
			stats.mu.Lock()
			defer stats.mu.Unlock()

			reason := fmt.Sprintf("panic: %v", r)
			failEntry := statusEntry{State: "panic", Reason: reason}

			// Mark all as failed so UI knows
			stats.status = MetricStatus{
				PageRank:     failEntry,
				Betweenness:  failEntry,
				Eigenvector:  failEntry,
				HITS:         failEntry,
				Critical:     failEntry,
				Cycles:       failEntry,
				KCore:        failEntry,
				Articulation: failEntry,
				Slack:        failEntry,
			}
			stats.phase2Ready = true
		}
	}()

	// Use the profiled version logic to avoid duplication
	// We discard the profile data as this is the standard run
	dummyProfile := &StartupProfile{}
	computeStart := time.Now()
	a.computePhase2WithProfile(ctx, stats, config, dummyProfile)
	computeDuration := time.Since(computeStart)

	if incrementalCacheKey != "" {
		putIncrementalGraphStatsCache(incrementalCacheKey, stats)
	}
	if robotCacheKey != "" {
		putRobotDiskCachedStats(robotCacheKey, dataHash, configHash, stats, computeDuration)
	}
}

func (a *Analyzer) computeHeights(sorted []graph.Node) map[string]float64 {
	heights := make(map[int64]float64)
	impactScores := make(map[string]float64)

	for _, n := range sorted {
		nid := n.ID()
		maxParentHeight := 0.0

		to := a.g.To(nid)
		for to.Next() {
			p := to.Node()
			if h, ok := heights[p.ID()]; ok {
				if h > maxParentHeight {
					maxParentHeight = h
				}
			}
		}
		heights[nid] = 1.0 + maxParentHeight
		impactScores[a.nodeToID[nid]] = heights[nid]
	}

	return impactScores
}

type undirectedAdjacency struct {
	nodes     []int64
	neighbors [][]int64
}

func newUndirectedAdjacency(g directedGraph) undirectedAdjacency {
	nodesIt := g.Nodes()
	nodes := make([]int64, 0, nodesIt.Len())
	var maxID int64
	for nodesIt.Next() {
		id := nodesIt.Node().ID()
		nodes = append(nodes, id)
		if id > maxID {
			maxID = id
		}
	}

	neighbors := make([][]int64, int(maxID)+1)
	edges := g.Edges()
	for edges.Next() {
		e := edges.Edge()
		from, to := e.From().ID(), e.To().ID()
		if from == to {
			continue
		}

		neighbors[from] = append(neighbors[from], to)
		neighbors[to] = append(neighbors[to], from)
	}

	// De-dup (two directed edges may map to the same undirected neighbor).
	for _, id := range nodes {
		nbrs := neighbors[id]
		if len(nbrs) < 2 {
			continue
		}
		sort.Slice(nbrs, func(i, j int) bool { return nbrs[i] < nbrs[j] })
		writeIdx := 1
		last := nbrs[0]
		for _, v := range nbrs[1:] {
			if v == last {
				continue
			}
			nbrs[writeIdx] = v
			writeIdx++
			last = v
		}
		neighbors[id] = nbrs[:writeIdx]
	}

	return undirectedAdjacency{
		nodes:     nodes,
		neighbors: neighbors,
	}
}

func (a undirectedAdjacency) neighborsOf(id int64) []int64 {
	if id < 0 || int(id) >= len(a.neighbors) {
		return nil
	}
	return a.neighbors[id]
}

func (a undirectedAdjacency) degree(id int64) int {
	return len(a.neighborsOf(id))
}

// computeCoreAndArticulation builds an undirected view to derive k-core numbers and articulation points.
func (a *Analyzer) computeCoreAndArticulation() (map[string]int, map[string]bool) {
	adj := newUndirectedAdjacency(a.g)
	core := computeKCore(adj)
	art := findArticulationPoints(adj)

	coreByID := make(map[string]int, len(core))
	artByID := make(map[string]bool, len(art))
	for id, val := range core {
		coreByID[a.nodeToID[id]] = val
	}
	for id := range art {
		artByID[a.nodeToID[id]] = true
	}
	return coreByID, artByID
}

// computeSlack calculates longest-path slack per node (0 on critical path).
// Edges are interpreted in execution order (prereq -> dependent), i.e., reversed from the stored direction.
func (a *Analyzer) computeSlack(order []string) map[string]float64 {
	if len(a.issueMap) == 0 {
		return nil
	}

	if len(order) == 0 {
		return nil
	}

	// Node IDs are created via g.NewNode() during analyzer build and are
	// densely allocated (0..n-1). Use slice-based indexing to avoid per-run map
	// allocations and repeated prereq slice building.
	nodeOrder := make([]int, 0, len(order))
	maxNode := -1
	for _, id := range order {
		nodeID, ok := a.idToNode[id]
		if !ok {
			continue
		}
		n := int(nodeID)
		nodeOrder = append(nodeOrder, n)
		if n > maxNode {
			maxNode = n
		}
	}
	if len(nodeOrder) == 0 || maxNode < 0 {
		return nil
	}

	size := maxNode + 1
	prereqs := make([][]int, size)
	for _, node := range nodeOrder {
		from := a.g.From(int64(node))
		for from.Next() {
			prereqs[node] = append(prereqs[node], int(from.Node().ID()))
		}
	}

	distFromStart := make([]int, size)
	distToEnd := make([]int, size)

	// Forward pass: longest distance from any start to each node
	// Propagate from u to v (u -> v): dist[v] = max(dist[v], dist[u] + 1)
	for i := len(nodeOrder) - 1; i >= 0; i-- {
		node := nodeOrder[i]
		for _, dep := range prereqs[node] {
			if distFromStart[dep] < distFromStart[node]+1 {
				distFromStart[dep] = distFromStart[node] + 1
			}
		}
	}

	// Reverse pass: longest distance from node to any end
	// Propagate from v to u (u -> v): dist[u] = max(dist[u], dist[v] + 1)
	for _, node := range nodeOrder {
		for _, dep := range prereqs[node] {
			if distToEnd[node] < distToEnd[dep]+1 {
				distToEnd[node] = distToEnd[dep] + 1
			}
		}
	}

	longest := 0
	for _, node := range nodeOrder {
		if d := distFromStart[node] + distToEnd[node]; d > longest {
			longest = d
		}
	}

	slack := make(map[string]float64, len(nodeOrder))
	for _, node := range nodeOrder {
		id, ok := a.nodeToID[int64(node)]
		if !ok {
			continue
		}
		slack[id] = float64(longest - distFromStart[node] - distToEnd[node])
	}
	return slack
}

// computeKCore returns per-node k-core numbers on the undirected view.
// Uses the linear-time Batagelj–Zaveršnik algorithm (O(V+E)).
func computeKCore(adj undirectedAdjacency) map[int64]int {
	n := len(adj.nodes)
	if n == 0 {
		return nil
	}

	// Use node IDs as direct indices. In this codebase, node IDs are densely
	// allocated by gonum (0..n-1), so this provides fast array access.
	// If IDs become sparse in the future, we can revisit this for a dense remap.
	var maxID int64
	for _, id := range adj.nodes {
		if id > maxID {
			maxID = id
		}
	}
	size := int(maxID) + 1

	deg := make([]int, size)
	pos := make([]int, size)
	present := make([]bool, size)

	maxDeg := 0
	for _, id := range adj.nodes {
		present[id] = true
		d := adj.degree(id)
		deg[id] = d
		if d > maxDeg {
			maxDeg = d
		}
	}

	// Bin-sort vertices by degree.
	bin := make([]int, maxDeg+1)
	for _, id := range adj.nodes {
		bin[deg[id]]++
	}

	start := 0
	for d := 0; d <= maxDeg; d++ {
		num := bin[d]
		bin[d] = start
		start += num
	}

	vert := make([]int64, n)
	for _, id := range adj.nodes {
		d := deg[id]
		i := bin[d]
		pos[id] = i
		vert[i] = id
		bin[d]++
	}

	// Restore bin[] to the start positions.
	for d := maxDeg; d >= 1; d-- {
		bin[d] = bin[d-1]
	}
	bin[0] = 0

	// Core decomposition in-place. Final deg[v] is the core number.
	for i := 0; i < n; i++ {
		v := vert[i]
		for _, u := range adj.neighborsOf(v) {
			if !present[u] {
				continue
			}
			if deg[u] > deg[v] {
				du := deg[u]
				pu := pos[u]
				pw := bin[du]
				w := vert[pw]
				if u != w {
					vert[pu] = w
					vert[pw] = u
					pos[u] = pw
					pos[w] = pu
				}
				bin[du]++
				deg[u]--
			}
		}
	}

	core := make(map[int64]int, n)
	for _, id := range adj.nodes {
		core[id] = deg[id]
	}

	return core
}

// findArticulationPoints runs Tarjan to find cut vertices in an undirected graph.
func findArticulationPoints(adj undirectedAdjacency) map[int64]bool {
	if len(adj.nodes) == 0 {
		return nil
	}

	var timeIdx int
	disc := make([]int, len(adj.neighbors))
	low := make([]int, len(adj.neighbors))
	parent := make([]int64, len(adj.neighbors))
	ap := make([]bool, len(adj.neighbors))

	const noParent int64 = -1

	var dfs func(v int64)
	dfs = func(v int64) {
		timeIdx++
		disc[v] = timeIdx
		low[v] = timeIdx
		childCount := 0

		for _, u := range adj.neighborsOf(v) {
			if disc[u] == 0 {
				parent[u] = v
				childCount++
				dfs(u)
				low[v] = min(low[v], low[u])

				// Root with >1 child OR low[u] >= disc[v]
				if parent[v] == noParent && childCount > 1 {
					ap[v] = true
				}
				if parent[v] != noParent && low[u] >= disc[v] {
					ap[v] = true
				}
			} else if u != parent[v] {
				low[v] = min(low[v], disc[u])
			}
		}
	}

	for _, id := range adj.nodes {
		if disc[id] == 0 {
			parent[id] = noParent
			dfs(id)
		}
	}

	out := make(map[int64]bool)
	for _, id := range adj.nodes {
		if ap[id] {
			out[id] = true
		}
	}
	return out
}

// GetActionableIssues returns owned copies of issues that can be worked on immediately.
// An issue is actionable if:
//  1. Its status is actionable (open or in_progress — see isActionableStatus;
//     blocked/deferred/draft/pinned/hooked/review/custom/closed are not)
//  2. All its blocking dependencies (type "blocks") are closed or tombstone
//  3. None of its parent issues (via "parent-child" deps) are themselves blocked
//     (transitive parent-blocked propagation, matching br's behavior)
//  4. It is not scheduler-deferred (defer_until in the future)
//
// Missing blockers or unresolved parent chains withhold readiness.
// Returns list sorted by ID for determinism.
func (a *Analyzer) GetActionableIssues() []model.Issue {
	issues := a.getActionableIssuesAfterCompletions(nil)
	for i := range issues {
		issues[i] = issues[i].Clone()
	}
	return issues
}

// CountActionableIssues uses the same readiness and candidate contract without
// sorting IDs or cloning issue bodies for consumers that only need a count.
func (a *Analyzer) CountActionableIssues() int {
	count := 0
	for id := range a.issueMap {
		if a.isActionableAfterCompletions(id, nil) {
			count++
		}
	}
	return count
}

// getActionableIssuesAfterCompletions applies the canonical readiness rules
// while treating IDs in completed as closed. Advanced planning features use it
// to simulate a sequence of completions without mutating the analyzer's source
// issues or maintaining a second, subtly different definition of actionable.
func (a *Analyzer) getActionableIssuesAfterCompletions(completed map[string]bool) []model.Issue {
	var ids []string
	for id := range a.issueMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var actionable []model.Issue
	readiness := a.Readiness()
	for _, id := range ids {
		if a.IsCandidate(id) && readiness.ReadyAfter(id, a.now, completed) {
			actionable = append(actionable, a.issueMap[id])
		}
	}

	return actionable
}

// ParentsWithOpenChildren returns the set of issue IDs that are a parent (via a
// "parent-child" dependency) of at least one open-like (not closed/tombstone)
// child. Such a parent is a planning container, not directly claimable work
// while its children are open — it must not be surfaced as a robot claimable
// top pick (issue #17 parity with the Rust viewer). This is type-agnostic: it
// catches any parent with open children, not only issue_type=="epic", so it
// also covers non-epic parents the epic-type claimability gate alone misses,
// while never withholding a genuinely-leaf epic.
func (a *Analyzer) ParentsWithOpenChildren() map[string]bool {
	result := make(map[string]bool)
	for id := range a.issueMap {
		if a.Readiness().HasOpenChildren(id) {
			result[id] = true
		}
	}
	return result
}

// getIssue returns a read-only view of an issue for internal analysis code.
// Callers must not mutate the returned issue or any of its nested data.
func (a *Analyzer) getIssue(id string) *model.Issue {
	if issue, ok := a.issueMap[id]; ok {
		return &issue
	}
	return nil
}

// GetIssue returns an owned copy of a single issue by ID, or nil if not found.
// Mutating the result cannot change the analyzer's immutable input snapshot.
func (a *Analyzer) GetIssue(id string) *model.Issue {
	issue := a.getIssue(id)
	if issue == nil {
		return nil
	}
	clone := issue.Clone()
	return &clone
}

// GetBlockers returns the IDs of issues that block the given issue
func (a *Analyzer) GetBlockers(issueID string) []string {
	issue, ok := a.issueMap[issueID]
	if !ok {
		return nil
	}

	var blockers []string
	for _, dep := range issue.Dependencies {
		if dep != nil && dep.Type.IsBlocking() {
			if _, exists := a.issueMap[dep.DependsOnID]; exists {
				blockers = append(blockers, dep.DependsOnID)
			}
		}
	}
	return sortedUniqueIssueIDs(blockers)
}

// GetOpenBlockers returns blocking or unresolved predecessors, including
// inherited parent blockage, from the full readiness authority.
func (a *Analyzer) GetOpenBlockers(issueID string) []string {
	return a.Readiness().Blockers(issueID)
}

func sortedUniqueIssueIDs(ids []string) []string {
	if len(ids) < 2 {
		return ids
	}
	sort.Strings(ids)
	write := 1
	for read := 1; read < len(ids); read++ {
		if ids[read] == ids[write-1] {
			continue
		}
		ids[write] = ids[read]
		write++
	}
	return ids[:write]
}

// BlockerChainEntry represents a single entry in a blocker chain.
type BlockerChainEntry struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	Depth       int    `json:"depth"`        // 0 = target, 1 = direct blocker, 2 = blocker's blocker, etc.
	IsRoot      bool   `json:"is_root"`      // True if this is the root blocker (has no open blockers)
	Actionable  bool   `json:"actionable"`   // True if this can be worked on (no open blockers)
	BlocksCount int    `json:"blocks_count"` // Number of issues this blocks
}

// BlockerChainResult contains the full blocker chain analysis.
type BlockerChainResult struct {
	TargetID     string              `json:"target_id"`
	TargetTitle  string              `json:"target_title"`
	IsBlocked    bool                `json:"is_blocked"`
	ChainLength  int                 `json:"chain_length"`  // Number of blockers in chain
	RootBlockers []BlockerChainEntry `json:"root_blockers"` // The root(s) that need to be done first
	Chain        []BlockerChainEntry `json:"chain"`         // Full chain from target to roots
	HasCycle     bool                `json:"has_cycle"`     // True if cycle detected
	CycleIDs     []string            `json:"cycle_ids,omitempty"`
}

// GetBlockerChain returns the full dependency chain explaining why an issue is blocked.
// It traverses the blocker graph to find all root blockers (issues with no open blockers).
// Handles cycles gracefully by detecting and reporting them.
func (a *Analyzer) GetBlockerChain(issueID string) *BlockerChainResult {
	issue, ok := a.issueMap[issueID]
	if !ok {
		return nil
	}
	triageCtx := NewTriageContext(a)

	result := &BlockerChainResult{
		TargetID:     issueID,
		TargetTitle:  issue.Title,
		IsBlocked:    false,
		ChainLength:  0,
		RootBlockers: []BlockerChainEntry{},
		Chain:        []BlockerChainEntry{},
	}

	// Add target as first entry (depth 0)
	targetEntry := BlockerChainEntry{
		ID:          issueID,
		Title:       issue.Title,
		Status:      string(issue.Status),
		Priority:    issue.Priority,
		Depth:       0,
		IsRoot:      false,
		Actionable:  triageCtx.IsActionable(issueID),
		BlocksCount: a.countBlockedBy(issueID),
	}
	result.Chain = append(result.Chain, targetEntry)

	// Include both direct predecessor blockers and parent-blocked propagation,
	// matching the readiness contract used by triage and execution planning.
	openBlockers := triageCtx.OpenBlockers(issueID)
	if len(openBlockers) == 0 {
		targetEntry.IsRoot = true
		result.Chain[0] = targetEntry
		return result
	}

	result.IsBlocked = true

	// DFS to find all blockers, detect cycles, and collect roots
	visited := make(map[string]bool)  // fully processed
	visiting := make(map[string]bool) // in current path

	var dfs func(id string, depth int)
	dfs = func(id string, depth int) {
		if visiting[id] {
			result.HasCycle = true
			result.CycleIDs = append(result.CycleIDs, id)
			return
		}
		if visited[id] {
			return
		}
		visiting[id] = true

		blocker, exists := a.issueMap[id]
		if !exists {
			visiting[id] = false
			visited[id] = true
			return
		}

		blockerOpenBlockers := triageCtx.OpenBlockers(id)
		isRoot := len(blockerOpenBlockers) == 0

		if id != issueID {
			entry := BlockerChainEntry{
				ID:          id,
				Title:       blocker.Title,
				Status:      string(blocker.Status),
				Priority:    blocker.Priority,
				Depth:       depth,
				IsRoot:      isRoot,
				Actionable:  triageCtx.IsActionable(id),
				BlocksCount: a.countBlockedBy(id),
			}
			result.Chain = append(result.Chain, entry)

			if isRoot {
				result.RootBlockers = append(result.RootBlockers, entry)
			}
		}

		for _, nextID := range blockerOpenBlockers {
			dfs(nextID, depth+1)
		}

		visiting[id] = false
		visited[id] = true
	}

	dfs(issueID, 0)

	result.ChainLength = len(result.Chain) - 1 // Exclude target itself

	// Sort root blockers by priority (lower = higher priority)
	sort.Slice(result.RootBlockers, func(i, j int) bool {
		if result.RootBlockers[i].Priority != result.RootBlockers[j].Priority {
			return result.RootBlockers[i].Priority < result.RootBlockers[j].Priority
		}
		return result.RootBlockers[i].ID < result.RootBlockers[j].ID
	})

	return result
}

// countBlockedBy returns the number of open issues that are blocked by the given issue.
func (a *Analyzer) countBlockedBy(issueID string) int {
	count := 0
	for _, issue := range a.issueMap {
		if isClosedLikeStatus(issue.Status) {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID == issueID {
				count++
				break
			}
		}
	}
	return count
}

// computePageRank returns PageRank weights for nodes of g.
//
// It uses a deterministic power iteration with damping factor damp and terminates
// when the L2 norm of the delta is below tol (or after a hard iteration cap).
func computePageRank(g graph.Directed, damp, tol float64) map[int64]float64 {
	nodes := graph.NodesOf(g.Nodes())
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID() < nodes[j].ID() })
	if len(nodes) == 0 {
		return map[int64]float64{}
	}
	if tol <= 0 {
		tol = 1e-6
	}

	// In this codebase, node IDs are densely allocated by gonum (0..n-1), so we
	// can avoid map-based indexing. Keep a fallback slice map for safety.
	dense := nodes[len(nodes)-1].ID() == int64(len(nodes)-1)
	if dense {
		for i, n := range nodes {
			if n.ID() != int64(i) {
				dense = false
				break
			}
		}
	}

	var idToIndex []int
	if !dense {
		maxID := nodes[len(nodes)-1].ID()
		idToIndex = make([]int, int(maxID)+1)
		for i := range idToIndex {
			idToIndex[i] = -1
		}
		for i, n := range nodes {
			idToIndex[int(n.ID())] = i
		}
	}

	out := make([][]int, len(nodes))
	for j, u := range nodes {
		to := graph.NodesOf(g.From(u.ID()))
		sort.Slice(to, func(i, j int) bool { return to[i].ID() < to[j].ID() })

		if len(to) == 0 {
			continue
		}

		adj := make([]int, len(to))
		if dense {
			for k, v := range to {
				adj[k] = int(v.ID())
			}
		} else {
			for k, v := range to {
				adj[k] = idToIndex[int(v.ID())]
			}
		}
		out[j] = adj
	}

	n := float64(len(nodes))
	rank := make([]float64, len(nodes))
	uniform := 1.0 / n
	for i := range rank {
		rank[i] = uniform
	}
	next := make([]float64, len(nodes))

	base := (1 - damp) / n
	const maxIterations = 1000
	for iter := 0; iter < maxIterations; iter++ {
		for i := range next {
			next[i] = base
		}

		dangling := 0.0
		for j := range nodes {
			outdeg := len(out[j])
			if outdeg == 0 {
				dangling += rank[j]
				continue
			}
			share := damp * rank[j] / float64(outdeg)
			for _, i := range out[j] {
				next[i] += share
			}
		}
		if dangling != 0 {
			add := damp * dangling / n
			for i := range next {
				next[i] += add
			}
		}

		diff := 0.0
		for i := range rank {
			d := next[i] - rank[i]
			diff += d * d
		}

		rank, next = next, rank
		if math.Sqrt(diff) < tol {
			break
		}
	}

	ranks := make(map[int64]float64, len(nodes))
	for i, node := range nodes {
		ranks[node.ID()] = rank[i]
	}

	return ranks
}

// computeEigenvector runs a simple power-iteration to estimate eigenvector centrality.
func computeEigenvector(g graph.Directed) map[int64]float64 {
	nodeList := graph.NodesOf(g.Nodes())
	sort.Slice(nodeList, func(i, j int) bool {
		return nodeList[i].ID() < nodeList[j].ID()
	})
	if len(nodeList) == 0 {
		return nil
	}

	// In this codebase, node IDs are densely allocated by gonum (0..n-1), so we
	// can avoid map-based indexing. Keep a fallback slice map for safety.
	dense := nodeList[len(nodeList)-1].ID() == int64(len(nodeList)-1)
	if dense {
		for i, n := range nodeList {
			if n.ID() != int64(i) {
				dense = false
				break
			}
		}
	}

	var idToIndex []int
	if !dense {
		maxID := nodeList[len(nodeList)-1].ID()
		idToIndex = make([]int, int(maxID)+1)
		for i := range idToIndex {
			idToIndex[i] = -1
		}
		for i, n := range nodeList {
			idToIndex[int(n.ID())] = i
		}
	}

	// Pre-calculate incoming neighbor indices for every node.
	incomingIdx := make([][]int, len(nodeList))
	for i, node := range nodeList {
		incoming := graph.NodesOf(g.To(node.ID()))
		sort.Slice(incoming, func(a, b int) bool {
			return incoming[a].ID() < incoming[b].ID()
		})

		if len(incoming) == 0 {
			continue
		}

		indices := make([]int, len(incoming))
		if dense {
			for k, nbr := range incoming {
				indices[k] = int(nbr.ID())
			}
		} else {
			for k, nbr := range incoming {
				indices[k] = idToIndex[int(nbr.ID())]
			}
		}
		incomingIdx[i] = indices
	}

	n := len(nodeList)
	vec := make([]float64, n)
	for i := range vec {
		vec[i] = 1.0 / float64(n)
	}
	work := make([]float64, n)

	const iterations = 50
	for iter := 0; iter < iterations; iter++ {
		for i := range work {
			work[i] = 0
		}
		for i := range nodeList {
			for _, j := range incomingIdx[i] {
				work[i] += vec[j]
			}
		}
		sum := 0.0
		for _, v := range work {
			sum += v * v
		}
		if sum == 0 {
			break
		}
		norm := 1 / math.Sqrt(sum)
		for i := range work {
			vec[i] = work[i] * norm
		}
	}

	res := make(map[int64]float64, n)
	for i, node := range nodeList {
		res[node.ID()] = vec[i]
	}
	return res
}

// computeFloatRanks computes rankings for a float map (descending).
func computeFloatRanks(m map[string]float64) map[string]int {
	if m == nil {
		return nil
	}
	ranks := make(map[string]int, len(m))
	type kv struct {
		k string
		v float64
	}
	var sorted []kv
	for k, v := range m {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].v == sorted[j].v {
			return sorted[i].k < sorted[j].k
		}
		return sorted[i].v > sorted[j].v // Descending
	})
	for i, item := range sorted {
		ranks[item.k] = i + 1
	}
	return ranks
}

// computeIntRanks computes rankings for an int map (descending).
func computeIntRanks(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	ranks := make(map[string]int, len(m))
	type kv struct {
		k string
		v int
	}
	var sorted []kv
	for k, v := range m {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].v == sorted[j].v {
			return sorted[i].k < sorted[j].k
		}
		return sorted[i].v > sorted[j].v
	})
	for i, item := range sorted {
		ranks[item.k] = i + 1
	}
	return ranks
}
