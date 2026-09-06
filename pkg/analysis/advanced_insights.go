package analysis

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// intHeap implements heap.Interface for a min-heap of ints.
// Used for deterministic O(log n) extraction in Kahn's algorithm.
type intHeap []int

func (h intHeap) Len() int           { return len(h) }
func (h intHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h intHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *intHeap) Push(x any) { *h = append(*h, x.(int)) }
func (h *intHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// AdvancedInsightsConfig holds caps and limits for advanced analysis features.
// All caps ensure deterministic, bounded outputs suitable for agents.
type AdvancedInsightsConfig struct {
	// TopK caps
	TopKSetLimit     int `json:"topk_set_limit"`     // Max items in top-k unlock set (default 5)
	CoverageSetLimit int `json:"coverage_set_limit"` // Max items in coverage set (default 5)

	// Path caps
	KPathsLimit   int `json:"k_paths_limit"`   // Max number of critical paths (default 5)
	PathLengthCap int `json:"path_length_cap"` // Max path length before truncation (default 50)

	// Cycle break caps
	CycleBreakLimit int `json:"cycle_break_limit"` // Max cycle break suggestions (default 5)

	// Parallel analysis caps
	ParallelCutLimit  int `json:"parallel_cut_limit"`  // Max parallel cut suggestions (default 5)
	ParallelGainLimit int `json:"parallel_gain_limit"` // Max parallel gain metrics (default 5)
}

// DefaultAdvancedInsightsConfig returns safe defaults for all caps.
func DefaultAdvancedInsightsConfig() AdvancedInsightsConfig {
	return AdvancedInsightsConfig{
		TopKSetLimit:      5,
		CoverageSetLimit:  5,
		KPathsLimit:       5,
		PathLengthCap:     50,
		CycleBreakLimit:   5,
		ParallelCutLimit:  5,
		ParallelGainLimit: 5,
	}
}

// parallelGainMaxIssues bounds generateParallelGain: above this many issues the
// O(actionable x (V+E)) sweep is skipped with state=skipped, reason=size.
const parallelGainMaxIssues = 5000

// normalized replaces zero or negative caps with the defaults so a partially
// populated config never disables a feature by accident.
func (c AdvancedInsightsConfig) normalized() AdvancedInsightsConfig {
	defaults := DefaultAdvancedInsightsConfig()
	if c.TopKSetLimit <= 0 {
		c.TopKSetLimit = defaults.TopKSetLimit
	}
	if c.CoverageSetLimit <= 0 {
		c.CoverageSetLimit = defaults.CoverageSetLimit
	}
	if c.KPathsLimit <= 0 {
		c.KPathsLimit = defaults.KPathsLimit
	}
	if c.PathLengthCap <= 0 {
		c.PathLengthCap = defaults.PathLengthCap
	}
	if c.CycleBreakLimit <= 0 {
		c.CycleBreakLimit = defaults.CycleBreakLimit
	}
	if c.ParallelCutLimit <= 0 {
		c.ParallelCutLimit = defaults.ParallelCutLimit
	}
	if c.ParallelGainLimit <= 0 {
		c.ParallelGainLimit = defaults.ParallelGainLimit
	}
	return c
}

// AdvancedInsights provides structured, capped outputs for advanced graph analysis.
// Each feature includes status tracking and usage hints for agent consumption.
type AdvancedInsights struct {
	// TopKSet: Greedy feasible sequence for downstream unlocks
	TopKSet *TopKSetResult `json:"topk_set,omitempty"`

	// CoverageSet: Bounded greedy set covering blocking dependency edges
	CoverageSet *CoverageSetResult `json:"coverage_set,omitempty"`

	// KPaths: representative longest critical paths through the dependency graph
	KPaths *KPathsResult `json:"k_paths,omitempty"`

	// ParallelCut: Suggestions for maximizing parallel work
	ParallelCut *ParallelCutResult `json:"parallel_cut,omitempty"`

	// ParallelGain: Parallelization gain metrics for top recommendations
	ParallelGain *ParallelGainResult `json:"parallel_gain,omitempty"`

	// CycleBreak: Frequency-ranked edges from representative stored cycles
	CycleBreak *CycleBreakResult `json:"cycle_break,omitempty"`

	// Config: Caps and limits used for this analysis
	Config AdvancedInsightsConfig `json:"config"`

	// UsageHints: Agent-friendly guidance for each feature
	UsageHints map[string]string `json:"usage_hints"`
}

// FeatureStatus tracks computation state for a single advanced feature.
type FeatureStatus struct {
	State      string `json:"state"`                 // available|computed|skipped|error
	Reason     string `json:"reason,omitempty"`      // Explanation when skipped/error/capped
	Capped     bool   `json:"capped,omitempty"`      // True if results were truncated
	Count      int    `json:"count,omitempty"`       // Number of results returned
	Limited    int    `json:"limited,omitempty"`     // Original count before capping
	DurationMs int64  `json:"duration_ms,omitempty"` // Wall-clock cost of the computation, when measured
}

// TopKSetResult represents a greedy sequence of issues chosen for downstream unlock.
type TopKSetResult struct {
	Status       FeatureStatus `json:"status"`
	Items        []TopKSetItem `json:"items,omitempty"`         // Ordered by selection sequence
	TotalGain    int           `json:"total_gain"`              // Total issues unlocked by set
	MarginalGain []int         `json:"marginal_gain,omitempty"` // Gain per item added
	HowToUse     string        `json:"how_to_use"`
}

// TopKSetItem represents one issue in the top-k unlock set.
type TopKSetItem struct {
	ID           string   `json:"id"`
	Title        string   `json:"title,omitempty"`
	MarginalGain int      `json:"marginal_gain"`      // Additional actionable issues from this pick
	Unblocks     []string `json:"unblocks,omitempty"` // IDs newly actionable after this pick
}

// CoverageSetResult represents a bounded set covering dependency edges (vertex cover).
// Uses a deterministic highest-uncovered-degree heuristic (bv-152).
type CoverageSetResult struct {
	Status        FeatureStatus  `json:"status"`
	Items         []CoverageItem `json:"items,omitempty"`
	EdgesCovered  int            `json:"edges_covered"`  // Number of edges covered by this set
	TotalEdges    int            `json:"total_edges"`    // Total edges in the dependency graph
	CoverageRatio float64        `json:"coverage_ratio"` // EdgesCovered / TotalEdges (0.0-1.0)
	Rationale     string         `json:"rationale"`      // Explanation of selection strategy
	HowToUse      string         `json:"how_to_use"`
}

// CoverageItem represents one issue in the coverage set.
type CoverageItem struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	EdgesAdded   int    `json:"edges_added"`   // Edges newly covered by including this node
	TotalDegree  int    `json:"total_degree"`  // Total edges incident to this node
	SelectionSeq int    `json:"selection_seq"` // Order in which this was selected (1-indexed)
}

// KPathsResult represents representative longest critical paths.
type KPathsResult struct {
	Status   FeatureStatus  `json:"status"`
	Paths    []CriticalPath `json:"paths,omitempty"`
	HowToUse string         `json:"how_to_use"`
}

// CriticalPath represents one critical path through the graph.
type CriticalPath struct {
	Rank      int      `json:"rank"`                // 1-indexed path rank
	Length    int      `json:"length"`              // Number of nodes in path
	IssueIDs  []string `json:"issue_ids"`           // Path from source to sink
	Truncated bool     `json:"truncated,omitempty"` // True if path was capped
}

// ParallelCutResult represents suggestions for parallel work maximization.
type ParallelCutResult struct {
	Status      FeatureStatus     `json:"status"`
	Suggestions []ParallelCutItem `json:"suggestions,omitempty"`
	MaxParallel int               `json:"max_parallel"` // Projected ready width after completing returned suggestions
	HowToUse    string            `json:"how_to_use"`
}

// ParallelCutItem represents one parallel cut suggestion.
type ParallelCutItem struct {
	ID            string   `json:"id"`
	Title         string   `json:"title,omitempty"`
	ParallelGain  int      `json:"parallel_gain"`            // Additional parallel streams enabled
	EnabledTracks []string `json:"enabled_tracks,omitempty"` // Track IDs enabled
}

// ParallelGainResult reports, for each actionable issue, how many additional
// independent work tracks would open if that issue were closed now.
//
// A track is a connected component of the non-closed dependency graph (the
// union-find grouping plan.go uses) that contains at least one actionable
// issue. Gain(X) = tracks(after closing X) - tracks(now); the issues X would
// newly unblock are reported as context. Only issues with positive gain are
// listed, ordered by gain, then by unblock count, then by ID.
type ParallelGainResult struct {
	Status          FeatureStatus      `json:"status"`
	CurrentParallel int                `json:"current_parallel"` // Tracks with actionable work right now
	Metrics         []ParallelGainItem `json:"metrics,omitempty"`
	HowToUse        string             `json:"how_to_use"`
}

// ParallelGainItem represents parallelization gain for one issue.
type ParallelGainItem struct {
	ID                string   `json:"id"`
	Title             string   `json:"title,omitempty"`
	CurrentParallel   int      `json:"current_parallel"`   // Tracks with actionable work now
	PotentialParallel int      `json:"potential_parallel"` // Tracks after closing this issue
	Gain              int      `json:"gain"`               // PotentialParallel - CurrentParallel
	GainPercent       float64  `json:"gain_percent"`       // Gain relative to CurrentParallel
	Unblocks          []string `json:"unblocks,omitempty"` // Issues that become actionable
}

// CycleBreakResult provides suggestions for breaking cycles.
type CycleBreakResult struct {
	Status      FeatureStatus    `json:"status"`
	Suggestions []CycleBreakItem `json:"suggestions,omitempty"`
	CycleCount  int              `json:"cycle_count"` // Stored representative cycle records analyzed by this feature
	HowToUse    string           `json:"how_to_use"`
	Advisory    string           `json:"advisory"` // Important warning text
}

// CycleBreakItem represents one cycle break suggestion.
type CycleBreakItem struct {
	EdgeFrom   string `json:"edge_from"`  // Source node of edge to remove
	EdgeTo     string `json:"edge_to"`    // Target node of edge to remove
	Impact     int    `json:"impact"`     // Stored cycle records containing this edge
	Collateral int    `json:"collateral"` // Active blocking dependents of EdgeTo
	InCycles   []int  `json:"in_cycles"`  // Cycle indices containing this edge
	Rationale  string `json:"rationale"`  // Why this edge is suggested
}

// DefaultUsageHints returns agent-friendly guidance for each feature.
func DefaultUsageHints() map[string]string {
	return map[string]string{
		"topk_set":      "Best k issues to complete for max downstream unlock. Work these in order.",
		"coverage_set":  "Greedy dependency-edge coverage. Check coverage_ratio and capped before treating it as complete.",
		"k_paths":       "Representative longest critical paths. Focus on issues appearing in multiple paths.",
		"parallel_cut":  "Issues that enable parallel work. Complete to maximize team throughput.",
		"parallel_gain": "Independent work tracks gained by closing each actionable issue now (gain = tracks after - tracks now). Pick high-gain issues to widen parallel work; unblocks lists what opens up.",
		"cycle_break":   "Structural fix suggestions. Apply BEFORE working on cycle members.",
	}
}

// GenerateAdvancedInsights creates the advanced insights structure with current data.
func (a *Analyzer) GenerateAdvancedInsights(config AdvancedInsightsConfig) *AdvancedInsights {
	return a.GenerateAdvancedInsightsFromStats(nil, config)
}

// GenerateAdvancedInsightsFromStats creates the advanced insights structure,
// reusing completed graph statistics when supplied. A nil stats value preserves
// GenerateAdvancedInsights behavior by running analysis when cycle data is needed.
func (a *Analyzer) GenerateAdvancedInsightsFromStats(stats *GraphStats, config AdvancedInsightsConfig) *AdvancedInsights {
	config = config.normalized()
	insights := &AdvancedInsights{
		Config:     config,
		UsageHints: DefaultUsageHints(),
	}

	// TopK Set - greedy submodular selection for maximum unlock (bv-145)
	insights.TopKSet = a.generateTopKSet(config.TopKSetLimit)

	// Coverage Set - bounded greedy vertex-cover heuristic (bv-152)
	insights.CoverageSet = a.generateCoverageSet(config.CoverageSetLimit)

	// K-Paths - top k longest/critical paths through the dependency graph (bv-153)
	insights.KPaths = a.generateKPaths(config.KPathsLimit, config.PathLengthCap)

	// Parallel Cut - suggestions for maximizing parallel work (bv-154)
	insights.ParallelCut = a.generateParallelCut(config.ParallelCutLimit)

	// Parallel Gain - tracks gained per actionable issue (bv-129)
	insights.ParallelGain = a.generateParallelGain(config.ParallelGainLimit)

	// Cycle Break - implement basic version using existing cycle detection
	insights.CycleBreak = a.generateCycleBreakSuggestionsFromStats(stats, config.CycleBreakLimit)

	return insights
}

// generateCycleBreakSuggestionsFromStats creates cycle break suggestions from
// supplied cycle data, analyzing only when the caller has no reusable stats.
func (a *Analyzer) generateCycleBreakSuggestionsFromStats(stats *GraphStats, limit int) *CycleBreakResult {
	if limit <= 0 {
		limit = 5
	}

	if stats == nil {
		analyzed := a.Analyze()
		stats = &analyzed
	} else {
		stats.WaitForPhase2()
	}
	cycleStatus := stats.Status().Cycles
	if cycleStatus.State != "computed" {
		state := "error"
		switch cycleStatus.State {
		case "pending":
			state = "pending"
		case "skipped":
			state = "skipped"
		}

		reason := "cycle detection unavailable"
		if cycleStatus.State != "" {
			reason = "cycle detection " + cycleStatus.State
		}
		if cycleStatus.Reason != "" {
			reason += ": " + cycleStatus.Reason
		}
		return &CycleBreakResult{
			Status: FeatureStatus{
				State:  state,
				Reason: reason,
			},
			HowToUse: DefaultUsageHints()["cycle_break"],
			Advisory: "Cycle analysis is unavailable; do not infer that the dependency graph is acyclic.",
		}
	}
	cycles := stats.Cycles()

	if len(cycles) == 0 {
		return &CycleBreakResult{
			Status: FeatureStatus{
				State: "available",
				Count: 0,
			},
			CycleCount: 0,
			HowToUse:   DefaultUsageHints()["cycle_break"],
			Advisory:   "No cycles detected - dependency graph is a proper DAG.",
		}
	}

	// Build edge frequency map across cycles
	type edgeKey struct{ from, to string }
	edgeFreq := make(map[edgeKey][]int) // edge -> cycle indices

	for i, cycle := range cycles {
		if len(cycle) == 0 {
			continue
		}
		// Handle special markers
		if cycle[0] == "CYCLE_DETECTION_TIMEOUT" || cycle[0] == "..." {
			continue
		}

		// The graph detector returns closed paths (A,B,C,A), while test and
		// external callers may supply the compact form (A,B,C). Normalize both to
		// the same edge ring; closing an already-closed path would invent A->A.
		nodeCount := len(cycle)
		if nodeCount > 1 && cycle[0] == cycle[nodeCount-1] {
			nodeCount--
		}
		seenInCycle := make(map[edgeKey]struct{}, nodeCount)
		for j := 0; j < nodeCount; j++ {
			key := edgeKey{from: cycle[j], to: cycle[(j+1)%nodeCount]}
			if _, seen := seenInCycle[key]; seen {
				continue
			}
			seenInCycle[key] = struct{}{}
			edgeFreq[key] = append(edgeFreq[key], i)
		}
	}

	// Rank edges by frequency (breaking highest-frequency edges affects most cycles)
	type edgeRank struct {
		key    edgeKey
		cycles []int
		count  int
	}
	var ranked []edgeRank
	for k, cycs := range edgeFreq {
		ranked = append(ranked, edgeRank{key: k, cycles: cycs, count: len(cycs)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		// Deterministic tie-break by edge lexicographically
		if ranked[i].key.from != ranked[j].key.from {
			return ranked[i].key.from < ranked[j].key.from
		}
		return ranked[i].key.to < ranked[j].key.to
	})

	// Cap and build suggestions
	suggestions := make([]CycleBreakItem, 0, limit)
	for i, r := range ranked {
		if i >= limit {
			break
		}
		suggestions = append(suggestions, CycleBreakItem{
			EdgeFrom:   r.key.from,
			EdgeTo:     r.key.to,
			Impact:     r.count,
			Collateral: a.countDependents(r.key.to),
			InCycles:   r.cycles,
			Rationale:  "Appears in the most stored cycle records; review collateral before removing this dependency.",
		})
	}

	inputCapped := strings.Contains(cycleStatus.Reason, "cycle representatives") && strings.Contains(cycleStatus.Reason, "truncated")
	capped := len(ranked) > limit || inputCapped
	advisory := "Cycle detection stores one representative cycle per cyclic component; review each edge and re-run analysis after a break."
	if inputCapped {
		advisory = "Cycle detection was capped; suggestions cover only stored cycles. " + advisory
	}
	return &CycleBreakResult{
		Status: FeatureStatus{
			State:   "available",
			Reason:  cycleStatus.Reason,
			Count:   len(suggestions),
			Capped:  capped,
			Limited: len(ranked),
		},
		Suggestions: suggestions,
		CycleCount:  len(cycles),
		HowToUse:    DefaultUsageHints()["cycle_break"],
		Advisory:    advisory,
	}
}

// countDependents returns the number of issues that depend on the given issue.
func (a *Analyzer) countDependents(issueID string) int {
	count := 0
	nodeID, exists := a.idToNode[issueID]
	if !exists {
		return 0
	}
	to := a.g.To(nodeID)
	for to.Next() {
		dependentID := a.nodeToID[to.Node().ID()]
		dependent, ok := a.issueMap[dependentID]
		if ok && !isClosedLikeStatus(dependent.Status) {
			count++
		}
	}
	return count
}

// generateTopKSet implements greedy submodular selection to find a feasible
// sequence of at most k issues that maximizes downstream unlocks (bv-145).
func (a *Analyzer) generateTopKSet(k int) *TopKSetResult {
	if k <= 0 {
		k = 5 // default
	}

	// Count the non-closed, non-deferred universe for status metadata. Candidate
	// selection below is stricter: every pick must be actionable after the picks
	// before it have been simulated as complete.
	potentialCandidates := 0
	for _, issue := range a.issueMap {
		if !isClosedLikeStatus(issue.Status) && !issue.IsDeferredAt(a.now) {
			potentialCandidates++
		}
	}

	if potentialCandidates == 0 {
		return &TopKSetResult{
			Status: FeatureStatus{
				State:  "available",
				Count:  0,
				Reason: "No actionable issues",
			},
			HowToUse: DefaultUsageHints()["topk_set"],
		}
	}

	// Track which issues we've "completed" in our greedy selection
	completed := make(map[string]bool)
	var items []TopKSetItem
	var marginalGains []int
	totalGain := 0

	// Greedy selection: at each step, consider only issues that are actionable
	// after the preceding selections. This keeps "work these in order" honest:
	// a blocked high-fanout node can never precede its prerequisite.
	for i := 0; i < k; i++ {
		actionable := a.getActionableIssuesAfterCompletions(completed)
		if len(actionable) == 0 {
			break
		}
		before := issueIDSet(actionable)

		bestID := ""
		bestGain := -1
		var bestUnblocks []string

		// getActionableIssuesAfterCompletions returns ID-sorted results, so the
		// explicit tie-break below is defensive as well as deterministic.
		for _, candidate := range actionable {
			candID := candidate.ID
			unblocks := a.computeMarginalUnblocksFromBefore(candID, completed, before)
			gain := len(unblocks)
			// Tie-break by ID for determinism
			if gain > bestGain || (gain == bestGain && (bestID == "" || candID < bestID)) {
				bestID = candID
				bestGain = gain
				bestUnblocks = unblocks
			}
		}

		if bestID == "" {
			break // no more candidates
		}

		// Select this candidate
		completed[bestID] = true
		title := ""
		if issue, exists := a.issueMap[bestID]; exists {
			title = issue.Title
		}
		items = append(items, TopKSetItem{
			ID:           bestID,
			Title:        title,
			MarginalGain: bestGain,
			Unblocks:     bestUnblocks,
		})
		marginalGains = append(marginalGains, bestGain)
		totalGain += bestGain
	}

	// Reaching the output limit only means truncation when the simulated state
	// has another feasible pick. Counting every remaining open issue produced a
	// false capped claim for issues stranded in a dependency cycle.
	hasNextFeasiblePick := len(items) >= k && len(a.getActionableIssuesAfterCompletions(completed)) > 0
	status := FeatureStatus{
		State:   "available",
		Count:   len(items),
		Capped:  hasNextFeasiblePick,
		Limited: potentialCandidates,
	}
	if len(items) == 0 {
		status.Reason = "No actionable issues"
	}

	return &TopKSetResult{
		Status:       status,
		Items:        items,
		TotalGain:    totalGain,
		MarginalGain: marginalGains,
		HowToUse:     DefaultUsageHints()["topk_set"],
	}
}

// computeMarginalUnblocks computes which issues would become actionable if we complete
// the given issue, assuming the issues in 'alreadyCompleted' are also done.
func (a *Analyzer) computeMarginalUnblocks(issueID string, alreadyCompleted map[string]bool) []string {
	before := issueIDSet(a.getActionableIssuesAfterCompletions(alreadyCompleted))
	return a.computeMarginalUnblocksFromBefore(issueID, alreadyCompleted, before)
}

func issueIDSet(issues []model.Issue) map[string]bool {
	result := make(map[string]bool, len(issues))
	for _, issue := range issues {
		result[issue.ID] = true
	}
	return result
}

// computeMarginalUnblocksFromBefore is the batch form of
// computeMarginalUnblocks. Callers evaluating several candidates against the
// same completion state can reuse the baseline set instead of rebuilding the
// entire actionable graph for every candidate.
func (a *Analyzer) computeMarginalUnblocksFromBefore(issueID string, alreadyCompleted, before map[string]bool) []string {
	completedAfter := make(map[string]bool, len(alreadyCompleted)+1)
	for id, done := range alreadyCompleted {
		completedAfter[id] = done
	}
	completedAfter[issueID] = true

	var unblocks []string
	readiness := a.Readiness()
	for id := range a.issueMap {
		// Already-actionable issues cannot be new unlocks. Select only the IDs
		// we need instead of copying every ready issue for each what-if candidate.
		if !before[id] && a.IsCandidate(id) && readiness.ReadyAfter(id, a.now, completedAfter) {
			unblocks = append(unblocks, id)
		}
	}
	sort.Strings(unblocks)
	return unblocks
}

// generateCoverageSet computes a bounded greedy vertex-cover heuristic over blocking edges.
// Uses only open issues; returns deterministic ordering with caps.
func (a *Analyzer) generateCoverageSet(limit int) *CoverageSetResult {
	if limit <= 0 {
		limit = 5
	}

	// Build edge list of blocking deps between non-closed issues
	type edge struct{ from, to string }
	var edges []edge
	seenEdges := make(map[edge]struct{})
	totalDegree := make(map[string]int)
	for id, issue := range a.issueMap {
		if isClosedLikeStatus(issue.Status) {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if target, ok := a.issueMap[dep.DependsOnID]; ok && !isClosedLikeStatus(target.Status) {
				e := edge{from: id, to: dep.DependsOnID}
				if _, seen := seenEdges[e]; !seen {
					seenEdges[e] = struct{}{}
					edges = append(edges, e)
					totalDegree[e.from]++
					totalDegree[e.to]++
				}
			}
		}
	}
	totalEdges := len(edges)
	if totalEdges == 0 {
		return &CoverageSetResult{
			Status: FeatureStatus{
				State:  "available",
				Count:  0,
				Reason: "No blocking edges to cover",
			},
			EdgesCovered:  0,
			TotalEdges:    0,
			CoverageRatio: 1.0,
			Rationale:     "Graph has no blocking dependencies.",
			HowToUse:      DefaultUsageHints()["coverage_set"],
		}
	}

	// Track uncovered edges and degrees
	uncovered := make(map[int]edge, len(edges))
	for i, e := range edges {
		uncovered[i] = e
	}

	var items []CoverageItem
	selection := 0
	edgesCovered := 0

	for len(uncovered) > 0 && len(items) < limit {
		// recompute degree from uncovered edges
		deg := make(map[string]int)
		for _, e := range uncovered {
			deg[e.from]++
			deg[e.to]++
		}

		// pick node with highest degree (tie-break lexicographically)
		bestID := ""
		bestDeg := -1
		for id, d := range deg {
			if d > bestDeg || (d == bestDeg && (bestID == "" || id < bestID)) {
				bestID, bestDeg = id, d
			}
		}
		if bestID == "" {
			break
		}

		// remove all edges incident to bestID
		added := 0
		for idx, e := range uncovered {
			if e.from == bestID || e.to == bestID {
				delete(uncovered, idx)
				added++
			}
		}
		edgesCovered += added
		selection++

		title := ""
		if issue, ok := a.issueMap[bestID]; ok {
			title = issue.Title
		}

		items = append(items, CoverageItem{
			ID:           bestID,
			Title:        title,
			EdgesAdded:   added,
			TotalDegree:  totalDegree[bestID],
			SelectionSeq: selection,
		})
	}

	capped := len(uncovered) > 0
	return &CoverageSetResult{
		Status: FeatureStatus{
			State:   "available",
			Count:   len(items),
			Capped:  capped,
			Limited: len(edges),
		},
		Items:         items,
		EdgesCovered:  edgesCovered,
		TotalEdges:    totalEdges,
		CoverageRatio: float64(edgesCovered) / float64(totalEdges),
		Rationale:     "Greedy vertex-cover heuristic: iteratively pick highest uncovered degree until edges are covered or cap is reached.",
		HowToUse:      DefaultUsageHints()["coverage_set"],
	}
}

// generateKPaths finds the k longest critical paths through the dependency graph (bv-153).
// Uses topological sort with DP to compute longest path distances, then reconstructs
// paths from nodes with highest distances. Only considers blocking edges between open issues.
func (a *Analyzer) generateKPaths(k int, pathLengthCap int) *KPathsResult {
	if k <= 0 {
		k = 5
	}
	if pathLengthCap <= 0 {
		pathLengthCap = 50
	}

	// Build adjacency list of blocking deps between non-closed issues
	// adj[from] = list of nodes that depend on 'from' (i.e., from blocks them)
	type nodeInfo struct {
		id    string
		index int
	}
	var nodes []nodeInfo
	idToIndex := make(map[string]int)

	// Collect non-closed issues
	for id, issue := range a.issueMap {
		if !isClosedLikeStatus(issue.Status) {
			idToIndex[id] = len(nodes)
			nodes = append(nodes, nodeInfo{id: id, index: len(nodes)})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].id < nodes[j].id })
	// Re-index after sorting for determinism
	for i, n := range nodes {
		idToIndex[n.id] = i
	}

	n := len(nodes)
	if n == 0 {
		return &KPathsResult{
			Status: FeatureStatus{
				State:  "available",
				Count:  0,
				Reason: "No open issues",
			},
			HowToUse: DefaultUsageHints()["k_paths"],
		}
	}

	// Build adjacency: adj[i] = nodes that i blocks (i.e., they depend on i)
	adj := make([][]int, n)
	inDegree := make([]int, n)
	seenBlockerEpoch := make([]int, n)
	epoch := 0

	for _, node := range nodes {
		issue := a.issueMap[node.id]
		epoch++
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			// dep.DependsOnID blocks node.id
			fromIdx, ok := idToIndex[dep.DependsOnID]
			if !ok {
				continue // blocker is closed or not in graph
			}
			if seenBlockerEpoch[fromIdx] == epoch {
				continue
			}
			seenBlockerEpoch[fromIdx] = epoch
			toIdx := idToIndex[node.id]
			adj[fromIdx] = append(adj[fromIdx], toIdx)
			inDegree[toIdx]++
		}
	}

	// Sort adjacency lists for determinism
	for i := range adj {
		sort.Ints(adj[i])
	}

	// Kahn's algorithm for topological sort using min-heap for determinism
	// Min-heap gives O(log k) per operation vs O(k log k) for sorting each iteration
	var topoOrder []int
	pq := &intHeap{}
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			*pq = append(*pq, i)
		}
	}
	heap.Init(pq) // O(k) heapify

	tempInDegree := make([]int, n)
	copy(tempInDegree, inDegree)

	for pq.Len() > 0 {
		// Pop smallest index for deterministic processing - O(log k)
		u := heap.Pop(pq).(int)
		topoOrder = append(topoOrder, u)

		for _, v := range adj[u] {
			tempInDegree[v]--
			if tempInDegree[v] == 0 {
				heap.Push(pq, v) // O(log k)
			}
		}
	}

	// A partial topological order cannot support an honest longest-path result:
	// paths through or downstream of a cycle would be silently omitted. Fail
	// closed and direct the caller to the cycle-break feature instead.
	if len(topoOrder) != n {
		return &KPathsResult{
			Status: FeatureStatus{
				State:  "skipped",
				Reason: "Dependency graph contains a cycle; break cycles before computing critical paths",
			},
			HowToUse: DefaultUsageHints()["k_paths"],
		}
	}

	// DP for longest path distances and predecessor tracking
	dist := make([]int, n) // dist[i] = length of longest path ending at i
	pred := make([]int, n) // pred[i] = predecessor on longest path (-1 if source)
	source := make([]int, n)
	for i := range pred {
		pred[i] = -1
		source[i] = i
	}

	// Process in topological order
	for _, u := range topoOrder {
		for _, v := range adj[u] {
			if dist[u]+1 > dist[v] {
				dist[v] = dist[u] + 1
				pred[v] = u
				source[v] = source[u]
			} else if dist[u]+1 == dist[v] && (pred[v] == -1 || u < pred[v]) {
				// Tie-break: prefer smaller index predecessor for determinism
				pred[v] = u
				source[v] = source[u]
			}
		}
	}

	// Find nodes with longest paths (these are our path endpoints)
	type pathEnd struct {
		idx    int
		length int
		id     string
	}
	var pathEnds []pathEnd
	for i := 0; i < n; i++ {
		pathEnds = append(pathEnds, pathEnd{idx: i, length: dist[i], id: nodes[i].id})
	}

	// Sort by length (descending), then by ID (ascending) for determinism
	sort.Slice(pathEnds, func(i, j int) bool {
		if pathEnds[i].length != pathEnds[j].length {
			return pathEnds[i].length > pathEnds[j].length
		}
		return pathEnds[i].id < pathEnds[j].id
	})

	// Reconstruct paths from top k endpoints
	var paths []CriticalPath
	usedSources := make(map[int]bool) // Avoid returning duplicate paths (same source)
	pathLengthCapped := false

	for _, pe := range pathEnds {
		if len(paths) >= k {
			break
		}
		// Skip trivial paths (single node, no dependencies)
		if pe.length == 0 {
			continue
		}

		// Reconstruct path by walking predecessors
		var pathIndices []int
		curr := pe.idx
		for curr != -1 {
			pathIndices = append(pathIndices, curr)
			curr = pred[curr]
		}

		// Path is in reverse order (sink to source), reverse it
		for i, j := 0, len(pathIndices)-1; i < j; i, j = i+1, j-1 {
			pathIndices[i], pathIndices[j] = pathIndices[j], pathIndices[i]
		}

		// Check if we already have a path from this source
		if len(pathIndices) > 0 {
			pathSource := source[pe.idx]
			if usedSources[pathSource] {
				continue // Skip duplicate source paths
			}
			usedSources[pathSource] = true
		}

		// Convert indices to issue IDs
		truncated := false
		if len(pathIndices) > pathLengthCap {
			pathIndices = pathIndices[:pathLengthCap]
			truncated = true
			pathLengthCapped = true
		}

		issueIDs := make([]string, len(pathIndices))
		for i, idx := range pathIndices {
			issueIDs[i] = nodes[idx].id
		}

		paths = append(paths, CriticalPath{
			Rank:      len(paths) + 1,
			Length:    len(issueIDs),
			IssueIDs:  issueIDs,
			Truncated: truncated,
		})
	}

	// Count the representative paths eligible for output, one per source. Using
	// every non-trivial endpoint here would make status.capped claim omissions
	// even when those endpoints are deliberately suppressed as duplicate-source
	// variants.
	representativeSources := make(map[int]struct{})
	for _, pe := range pathEnds {
		if pe.length > 0 {
			representativeSources[source[pe.idx]] = struct{}{}
		}
	}
	representativeCount := len(representativeSources)

	return &KPathsResult{
		Status: FeatureStatus{
			State:   "available",
			Count:   len(paths),
			Capped:  pathLengthCapped || (len(paths) >= k && representativeCount > k),
			Limited: representativeCount,
		},
		Paths:    paths,
		HowToUse: DefaultUsageHints()["k_paths"],
	}
}

// generateParallelCut finds nodes that maximize parallel work opportunities (bv-154).
// A node has positive "parallel gain" if completing it would unblock more than one
// dependent, increasing the number of items that can be worked on in parallel.
func (a *Analyzer) generateParallelCut(limit int) *ParallelCutResult {
	if limit <= 0 {
		limit = 5
	}

	actionable := a.getActionableIssuesAfterCompletions(nil)
	if len(actionable) == 0 {
		return &ParallelCutResult{
			Status: FeatureStatus{
				State:  "available",
				Count:  0,
				Reason: "No actionable issues",
			},
			MaxParallel: 0,
			HowToUse:    DefaultUsageHints()["parallel_cut"],
		}
	}

	before := issueIDSet(actionable)

	// Calculate parallel gain only for work that can actually start now. The
	// canonical readiness simulation handles blocking dependency types,
	// defer_until, and transitive parent-blocked propagation together.
	type parallelCandidate struct {
		id            string
		parallelGain  int
		enabledTracks []string
	}
	var candidates []parallelCandidate

	for _, issue := range actionable {
		id := issue.ID
		newlyActionable := a.computeMarginalUnblocksFromBefore(id, nil, before)

		// Parallel gain = newly actionable - 1 (the completed node leaves the actionable pool)
		// Positive gain means net increase in parallel work opportunities
		parallelGain := len(newlyActionable) - 1

		if parallelGain > 0 {
			candidates = append(candidates, parallelCandidate{
				id:            id,
				parallelGain:  parallelGain,
				enabledTracks: newlyActionable,
			})
		}
	}

	// Sort by parallel gain descending, then by ID for determinism
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].parallelGain != candidates[j].parallelGain {
			return candidates[i].parallelGain > candidates[j].parallelGain
		}
		return candidates[i].id < candidates[j].id
	})

	originalCandidateCount := len(candidates)
	// Cap to limit
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Build suggestions
	suggestions := make([]ParallelCutItem, len(candidates))
	for i, c := range candidates {
		title := ""
		if issue, ok := a.issueMap[c.id]; ok {
			title = issue.Title
		}
		suggestions[i] = ParallelCutItem{
			ID:            c.id,
			Title:         title,
			ParallelGain:  c.parallelGain,
			EnabledTracks: c.enabledTracks,
		}
	}

	// Project the ready width after completing the returned cut as a set. Summing
	// each candidate's independent marginal gain misses issues that require two or
	// more suggested blockers to complete before becoming actionable.
	completedCut := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		completedCut[c.id] = true
	}
	maxParallel := len(a.getActionableIssuesAfterCompletions(completedCut))

	return &ParallelCutResult{
		Status: FeatureStatus{
			State:   "available",
			Count:   len(suggestions),
			Capped:  originalCandidateCount > limit,
			Limited: originalCandidateCount,
		},
		Suggestions: suggestions,
		MaxParallel: maxParallel,
		HowToUse:    DefaultUsageHints()["parallel_cut"],
	}
}

// generateParallelGain measures, for every actionable issue, how many extra
// independent work tracks would open if that issue were closed now (bv-129).
//
// A track is a connected component of the non-closed dependency graph
// (blocking and parent-child edges, the grouping plan.go uses) that contains
// at least one actionable issue. For candidate X the graph is re-grouped with
// X removed and the actionable set becomes (actionable - X) + unblocks(X).
//
// Cost is O(actionable x (V+E)). The sweep is skipped above
// parallelGainMaxIssues and stops early when the Phase-2 budget from
// parallelGainBudget is exhausted, reporting the partial list as capped.
func (a *Analyzer) generateParallelGain(limit int) *ParallelGainResult {
	start := time.Now()
	if limit <= 0 {
		limit = 5
	}
	result := &ParallelGainResult{HowToUse: DefaultUsageHints()["parallel_gain"]}

	if len(a.issueMap) > parallelGainMaxIssues {
		result.Status = FeatureStatus{
			State:  "skipped",
			Reason: fmt.Sprintf("size: %d issues exceeds the %d-issue cap", len(a.issueMap), parallelGainMaxIssues),
		}
		return result
	}

	// Open issues in sorted order so every map walk below is deterministic.
	openIDs := make([]string, 0, len(a.issueMap))
	for id, issue := range a.issueMap {
		if !isClosedLikeStatus(issue.Status) {
			openIDs = append(openIDs, id)
		}
	}
	sort.Strings(openIDs)
	openSet := make(map[string]bool, len(openIDs))
	for _, id := range openIDs {
		openSet[id] = true
	}

	// Undirected edges between open issues, matching findConnectedComponents.
	type edge struct{ from, to string }
	var edges []edge
	for _, id := range openIDs {
		for _, dep := range a.issueMap[id].Dependencies {
			if dep == nil || !(dep.Type.IsBlocking() || dep.Type == model.DepParentChild) {
				continue
			}
			if openSet[dep.DependsOnID] {
				edges = append(edges, edge{from: id, to: dep.DependsOnID})
			}
		}
	}

	actionable := a.GetActionableIssues()
	actionableSet := make(map[string]bool, len(actionable))
	actionableIDs := make([]string, 0, len(actionable))
	for _, iss := range actionable {
		actionableSet[iss.ID] = true
		actionableIDs = append(actionableIDs, iss.ID)
	}
	sort.Strings(actionableIDs)

	// countTracks groups the open graph without `excluded` and counts the
	// components that contain at least one member of `active`.
	countTracks := func(excluded string, active map[string]bool) int {
		parent := make(map[string]string, len(openIDs))
		for _, id := range openIDs {
			if id != excluded {
				parent[id] = id
			}
		}
		var find func(string) string
		find = func(x string) string {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}
		for _, e := range edges {
			if e.from == excluded || e.to == excluded {
				continue
			}
			pf, pt := find(e.from), find(e.to)
			if pf == pt {
				continue
			}
			if pf < pt {
				parent[pt] = pf
			} else {
				parent[pf] = pt
			}
		}
		roots := make(map[string]bool)
		for id := range active {
			if id == excluded {
				continue
			}
			if _, ok := parent[id]; !ok {
				continue
			}
			roots[find(id)] = true
		}
		return len(roots)
	}

	tracksNow := countTracks("", actionableSet)
	result.CurrentParallel = tracksNow

	if len(actionableIDs) == 0 {
		result.Status = FeatureStatus{
			State:      "computed",
			Count:      0,
			Reason:     "No actionable issues",
			DurationMs: time.Since(start).Milliseconds(),
		}
		return result
	}

	budget := a.parallelGainBudget()
	var deadline time.Time
	if budget > 0 {
		deadline = start.Add(budget)
	}

	none := map[string]bool{}
	var items []ParallelGainItem
	evaluated := 0
	for _, id := range actionableIDs {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		evaluated++

		unblocks := a.computeMarginalUnblocks(id, none)
		after := make(map[string]bool, len(actionableSet)+len(unblocks))
		for k := range actionableSet {
			if k != id {
				after[k] = true
			}
		}
		for _, u := range unblocks {
			after[u] = true
		}
		tracksAfter := countTracks(id, after)
		gain := tracksAfter - tracksNow
		if gain <= 0 {
			continue
		}
		pct := 0.0
		if tracksNow > 0 {
			pct = float64(gain) / float64(tracksNow) * 100
		}
		items = append(items, ParallelGainItem{
			ID:                id,
			Title:             a.issueMap[id].Title,
			CurrentParallel:   tracksNow,
			PotentialParallel: tracksAfter,
			Gain:              gain,
			GainPercent:       pct,
			Unblocks:          unblocks,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Gain != items[j].Gain {
			return items[i].Gain > items[j].Gain
		}
		if len(items[i].Unblocks) != len(items[j].Unblocks) {
			return len(items[i].Unblocks) > len(items[j].Unblocks)
		}
		return items[i].ID < items[j].ID
	})

	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	status := FeatureStatus{
		State:      "computed",
		Count:      len(items),
		Capped:     total > limit,
		Limited:    total,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if evaluated < len(actionableIDs) {
		status.Capped = true
		status.Reason = fmt.Sprintf("time budget %s exhausted after %d of %d candidates", budget, evaluated, len(actionableIDs))
	}
	result.Status = status
	result.Metrics = items
	return result
}

// parallelGainBudget returns the wall-clock budget for the parallel-gain sweep:
// the betweenness timeout of the configured (SetConfig) or size-tier
// (ConfigForSize) analysis config, since both computations are in the same
// O(V*E) cost class. RunToCompletion (reproducible output) disables the budget.
func (a *Analyzer) parallelGainBudget() time.Duration {
	var cfg AnalysisConfig
	if a.config != nil {
		cfg = *a.config
	} else {
		cfg = ConfigForSize(len(a.issueMap), a.g.Edges().Len())
	}
	if cfg.RunToCompletion {
		return 0
	}
	return cfg.BetweennessTimeout
}
