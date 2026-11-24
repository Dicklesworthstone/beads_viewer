package analysis

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// PriorityExplanation provides detailed reasoning for a priority recommendation
type PriorityExplanation struct {
	// TopReasons are the top 3 most important reasons (deterministically ordered)
	TopReasons []PriorityReason `json:"top_reasons"`

	// WhatIf describes what happens if this issue is completed
	WhatIf *WhatIfDelta `json:"what_if"`

	// Status provides inline status context
	Status ExplanationStatus `json:"status"`

	// FieldDescriptions provides footer explanations for agents
	FieldDescriptions map[string]string `json:"field_descriptions,omitempty"`
}

// PriorityReason is a single reason with weight and explanation
type PriorityReason struct {
	Factor      string  `json:"factor"`      // e.g., "pagerank", "betweenness", "unblocks"
	Weight      float64 `json:"weight"`      // Contribution to score (0-1)
	Explanation string  `json:"explanation"` // Human-readable explanation
	Emoji       string  `json:"emoji"`       // Visual indicator
}

// ExplanationStatus provides inline status context
type ExplanationStatus struct {
	ComputedAt    string `json:"computed_at"`   // ISO timestamp
	DataHash      string `json:"data_hash"`     // Hash of input data
	Phase2Ready   bool   `json:"phase2_ready"`  // Whether advanced metrics are available
	Deterministic bool   `json:"deterministic"` // Whether output is deterministic
	Capped        bool   `json:"capped"`        // Whether any caps were applied
	CappedFields  string `json:"capped_fields,omitempty"`
}

// DefaultFieldDescriptions returns standard field descriptions for agents
func DefaultFieldDescriptions() map[string]string {
	return map[string]string{
		"top_reasons":                  "Top 3 factors contributing to priority score, ordered by weight",
		"what_if.unblocks":             "Number of issues directly waiting on this one",
		"what_if.cascade":              "Total issues transitively unblocked (including indirect)",
		"what_if.depth":                "Critical path depth reduction if completed",
		"what_if.days_saved":           "Estimated days saved based on issue estimates",
		"what_if.parallelization_gain": "Net change in parallel work capacity (direct_unblocks - 1); positive = more parallel work possible",
		"status.phase2":                "Whether expensive graph metrics (PageRank, betweenness) are included",
		"status.capped":                "Whether results were truncated to prevent overload",
	}
}

// GenerateTopReasons extracts the top 3 reasons from an ImpactScore
func GenerateTopReasons(score ImpactScore) []PriorityReason {
	reasons := []PriorityReason{}

	// Collect all factors with their contributions
	factors := []struct {
		name        string
		weight      float64
		norm        float64
		explanation string
		emoji       string
	}{
		{"pagerank", score.Breakdown.PageRank, score.Breakdown.PageRankNorm, "Central in dependency graph", "🎯"},
		{"betweenness", score.Breakdown.Betweenness, score.Breakdown.BetweennessNorm, "Critical path bottleneck", "🔀"},
		{"blockers", score.Breakdown.BlockerRatio, score.Breakdown.BlockerRatioNorm, "High blocker count", "🚧"},
		{"staleness", score.Breakdown.Staleness, score.Breakdown.StalenessNorm, "Needs attention (aging)", "⏰"},
		{"priority", score.Breakdown.PriorityBoost, score.Breakdown.PriorityBoostNorm, "Explicit priority set", "⭐"},
		{"time_to_impact", score.Breakdown.TimeToImpact, score.Breakdown.TimeToImpactNorm, "Fast impact potential", "⚡"},
		{"urgency", score.Breakdown.Urgency, score.Breakdown.UrgencyNorm, "Urgent labels/timing", "🔥"},
		{"risk", score.Breakdown.Risk, score.Breakdown.RiskNorm, "Risk/volatility factors", "⚠️"},
	}

	// Sort by weighted contribution (descending)
	sort.Slice(factors, func(i, j int) bool {
		return factors[i].weight > factors[j].weight
	})

	// Take top 3 with significant contribution
	for _, f := range factors {
		if f.weight < 0.01 {
			continue // Skip negligible factors
		}
		if len(reasons) >= 3 {
			break
		}

		explanation := f.explanation
		if f.norm > 0.7 {
			explanation = "Very high: " + explanation
		} else if f.norm > 0.4 {
			explanation = "High: " + explanation
		} else if f.norm > 0.2 {
			explanation = "Moderate: " + explanation
		}

		reasons = append(reasons, PriorityReason{
			Factor:      f.name,
			Weight:      f.weight,
			Explanation: explanation,
			Emoji:       f.emoji,
		})
	}

	return reasons
}

// EnhancedPriorityRecommendation extends PriorityRecommendation with what-if deltas
type EnhancedPriorityRecommendation struct {
	PriorityRecommendation

	// Explanation provides detailed reasoning
	Explanation PriorityExplanation `json:"explanation"`
}

// GenerateEnhancedRecommendations generates recommendations with what-if deltas
func (a *Analyzer) GenerateEnhancedRecommendations() []EnhancedPriorityRecommendation {
	return a.GenerateEnhancedRecommendationsWithThresholds(DefaultThresholds())
}

// GenerateEnhancedRecommendationsWithThresholds generates enhanced recommendations
func (a *Analyzer) GenerateEnhancedRecommendationsWithThresholds(thresholds RecommendationThresholds) []EnhancedPriorityRecommendation {
	return a.GenerateEnhancedRecommendationsFromStats(nil, thresholds, 10, true)
}

// GenerateEnhancedRecommendationsFromStats generates enhanced recommendations reusing
// caller-supplied GraphStats. When includeInactive is false, parked or deferred
// statuses (blocked, deferred, future defer_until, draft, pinned, hooked, review)
// are excluded so default --robot-priority surfaces only live open/in_progress
// candidate backlog items. Pass maxResults <= 0 for uncapped output before caller
// post-filtering.
func (a *Analyzer) GenerateEnhancedRecommendationsFromStats(stats *GraphStats, thresholds RecommendationThresholds, maxResults int, includeInactive bool) []EnhancedPriorityRecommendation {
	if stats == nil {
		analyzed := a.Analyze()
		stats = &analyzed
	}
	rawNow := a.Now()
	scores := a.ComputeImpactScoresFromStats(stats, rawNow)
	if len(scores) == 0 {
		return nil
	}

	// Get basic recommendations reusing precomputed scores
	basicRecs := a.generateRecommendationsFromStatsAndScores(stats, scores, rawNow, thresholds)

	// Create a map for quick lookup
	recMap := make(map[string]*PriorityRecommendation)
	for i := range basicRecs {
		recMap[basicRecs[i].IssueID] = &basicRecs[i]
	}

	var enhanced []EnhancedPriorityRecommendation
	now := rawNow.UTC()
	dataHash := a.DataHash()
	phase2Ready := stats.IsPhase2Ready()

	selectionMode := SelectionReadyLeaf
	if includeInactive {
		selectionMode = SelectionAllScopedCandidates
	}
	eligibleIDs := a.CandidateIssueSet(selectionMode)

	// Enhance each score with what-if deltas
	for _, score := range scores {
		if !eligibleIDs[score.IssueID] {
			continue
		}

		rec, hasRec := recMap[score.IssueID]

		// Reuse what-if delta when already computed on basic recommendation
		var whatIf *WhatIfDelta
		if hasRec && rec.WhatIf != nil {
			whatIf = rec.WhatIf
		} else {
			whatIf = a.computeWhatIfDeltaFromStats(score.IssueID, stats)
			if hasRec {
				rec.WhatIf = whatIf
			}
		}
		topReasons := GenerateTopReasons(score)

		// Determine if caps were applied
		capped := false
		cappedFields := ""
		if whatIf != nil && len(whatIf.UnblockedIssueIDs) < whatIf.DirectUnblocks {
			capped = true
			cappedFields = "unblocked_issue_ids"
		}

		explanation := PriorityExplanation{
			TopReasons: topReasons,
			WhatIf:     whatIf,
			Status: ExplanationStatus{
				ComputedAt:    now.Format(time.RFC3339),
				DataHash:      dataHash,
				Deterministic: true,
				Phase2Ready:   phase2Ready,
				Capped:        capped,
				CappedFields:  cappedFields,
			},
		}

		if hasRec {
			// Enhance existing recommendation
			enhanced = append(enhanced, EnhancedPriorityRecommendation{
				PriorityRecommendation: *rec,
				Explanation:            explanation,
			})
		} else if whatIf != nil && (whatIf.DirectUnblocks > 0 || whatIf.TransitiveUnblocks > 2) {
			// Add items with significant impact even without priority change rec
			enhanced = append(enhanced, EnhancedPriorityRecommendation{
				PriorityRecommendation: PriorityRecommendation{
					IssueID:           score.IssueID,
					Title:             score.Title,
					Labels:            append([]string{}, score.Labels...),
					CurrentPriority:   score.Priority,
					SuggestedPriority: score.Priority, // No change
					ImpactScore:       score.Score,
					Confidence:        0.5,
					Reasoning:         extractReasoningStrings(topReasons),
					Direction:         "none",
					WhatIf:            whatIf, // bv-129: populate top-level WhatIf for consistency
				},
				Explanation: explanation,
			})
		}
	}

	// Sort by impact score descending, then IssueID ascending for determinism
	sort.Slice(enhanced, func(i, j int) bool {
		if enhanced[i].ImpactScore != enhanced[j].ImpactScore {
			return enhanced[i].ImpactScore > enhanced[j].ImpactScore
		}
		return enhanced[i].IssueID < enhanced[j].IssueID
	})

	if maxResults > 0 && len(enhanced) > maxResults {
		enhanced = enhanced[:maxResults]
	}

	return enhanced
}

// extractReasoningStrings converts PriorityReasons to string slice
func extractReasoningStrings(reasons []PriorityReason) []string {
	result := make([]string, len(reasons))
	for i, r := range reasons {
		result[i] = r.Emoji + " " + r.Explanation
	}
	return result
}

// WhatIfEntry represents a single issue with its what-if delta
type WhatIfEntry struct {
	IssueID string      `json:"issue_id"`
	Title   string      `json:"title"`
	Delta   WhatIfDelta `json:"delta"`
}

// WhatIfResult represents the single-issue completion simulation result for --robot-whatif.
type WhatIfResult struct {
	IssueID  string      `json:"issue_id"`
	Title    string      `json:"title"`
	Status   string      `json:"status"`
	Priority int         `json:"priority"`
	Delta    WhatIfDelta `json:"delta"`
}

// WhatIfBatchItem summarizes one input issue in a multi-issue batch what-if simulation (W31-2).
type WhatIfBatchItem struct {
	IssueID       string `json:"issue_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Priority      int    `json:"priority"`
	AlreadyClosed bool   `json:"already_closed,omitempty"`
}

// WhatIfBatchResult represents the joint completion simulation result for multiple issues (W31-2).
type WhatIfBatchResult struct {
	IssueIDs        []string          `json:"issue_ids"`
	Issues          []WhatIfBatchItem `json:"issues"`
	MissingIDs      []string          `json:"missing_ids,omitempty"`
	ActiveCount     int               `json:"active_count"`
	SynergyUnblocks int               `json:"synergy_unblocks"`
	SynergyIssueIDs []string          `json:"synergy_issue_ids,omitempty"`
	Delta           WhatIfDelta       `json:"delta"`
}

// ComputeWhatIf simulates completing issueID and returns its downstream impact
// summary, or nil when issueID does not exist in the active candidate scope.
func (a *Analyzer) ComputeWhatIf(issueID string, stats *GraphStats) *WhatIfResult {
	if a == nil {
		return nil
	}
	issue, ok := a.issueMap[issueID]
	if !ok || !a.IsCandidate(issueID) {
		return nil
	}
	if stats == nil {
		analyzed := a.Analyze()
		stats = &analyzed
	}
	delta := a.computeWhatIfDeltaFromStats(issueID, stats)
	if delta == nil {
		return nil
	}
	return &WhatIfResult{
		IssueID:  issue.ID,
		Title:    issue.Title,
		Status:   string(issue.Status),
		Priority: issue.Priority,
		Delta:    *delta,
	}
}

// ComputeWhatIfBatch simulates completing multiple issues simultaneously, exposing
// joint diamond-dependency unlocks (SynergyUnblocks), transitive cascades, and net
// parallelization gain across all active input issues (W31-2).
func (a *Analyzer) ComputeWhatIfBatch(issueIDs []string, stats *GraphStats) *WhatIfBatchResult {
	if a == nil || len(issueIDs) == 0 {
		return nil
	}
	deduped := make([]string, 0, len(issueIDs))
	seenInput := make(map[string]bool, len(issueIDs))
	for _, raw := range issueIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seenInput[id] {
			continue
		}
		seenInput[id] = true
		deduped = append(deduped, id)
	}
	if len(deduped) == 0 {
		return nil
	}

	items := make([]WhatIfBatchItem, 0, len(deduped))
	var missing []string
	batchClosed := make(map[string]bool, len(deduped))
	activeIDs := make([]string, 0, len(deduped))
	for _, id := range deduped {
		iss, ok := a.issueMap[id]
		if !ok || !a.IsCandidate(id) {
			missing = append(missing, id)
			continue
		}
		closed := isClosedLikeStatus(iss.Status)
		items = append(items, WhatIfBatchItem{
			IssueID:       iss.ID,
			Title:         iss.Title,
			Status:        string(iss.Status),
			Priority:      iss.Priority,
			AlreadyClosed: closed,
		})
		if !closed {
			batchClosed[iss.ID] = true
			activeIDs = append(activeIDs, iss.ID)
		}
	}
	if len(missing) > 0 {
		return &WhatIfBatchResult{
			IssueIDs:   deduped,
			Issues:     items,
			MissingIDs: missing,
		}
	}
	if stats == nil {
		analyzed := a.Analyze()
		stats = &analyzed
	}

	readiness := a.Readiness()
	directSeen := make(map[string]bool)
	var directUnblocks []string
	checkDirectCandidate := func(candID string) {
		if candID == "" || batchClosed[candID] || directSeen[candID] {
			return
		}
		directSeen[candID] = true
		if !readiness.Ready(candID, a.now) && a.isActionableAfterCompletions(candID, batchClosed) {
			directUnblocks = append(directUnblocks, candID)
		}
	}
	singleDirectUnion := make(map[string]bool)
	maxDepth := 0.0
	for _, id := range activeIDs {
		if nodeID, ok := a.idToNode[id]; ok {
			dependents := a.g.To(nodeID)
			for dependents.Next() {
				checkDirectCandidate(a.nodeToID[dependents.Node().ID()])
			}
		}
		for _, childID := range a.childrenByParent[id] {
			checkDirectCandidate(childID)
		}
		for _, singleID := range a.computeDirectBlockingUnblocks(id) {
			singleDirectUnion[singleID] = true
		}
		if d, _ := stats.CriticalPathValue(id); d > maxDepth {
			maxDepth = d
		}
	}
	sort.Strings(directUnblocks)

	var synergyIDs []string
	for _, id := range directUnblocks {
		if !singleDirectUnion[id] {
			synergyIDs = append(synergyIDs, id)
		}
	}
	synergyCount := len(synergyIDs)

	// Transitive cascade seeded with all active batch inputs completed simultaneously.
	simulatedClosed := make(map[string]bool, len(batchClosed))
	queue := make([]string, 0, len(activeIDs))
	for _, id := range activeIDs {
		simulatedClosed[id] = true
		queue = append(queue, id)
	}
	transitiveCount := 0
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, candID := range a.directUnlockFrontier(curr) {
			if simulatedClosed[candID] {
				continue
			}
			if _, exists := a.issueMap[candID]; !exists {
				continue
			}
			if readiness.Ready(candID, a.now) {
				continue
			}
			if a.isActionableAfterCompletions(candID, simulatedClosed) {
				transitiveCount++
				simulatedClosed[candID] = true
				queue = append(queue, candID)
			}
		}
	}

	blockedReduction := 0
	for _, uid := range directUnblocks {
		if readiness.DependencyState(uid) != model.DependenciesSatisfied {
			blockedReduction++
		}
	}
	depthReduction := 0.0
	if maxDepth > 0 {
		depthReduction = maxDepth / MaxCriticalPathDepth
		if depthReduction > 1.0 {
			depthReduction = 1.0
		}
	}
	estimatedDaysSaved := estimateDaysSaved(directUnblocks, a.issueMap)
	unblockedShown := directUnblocks
	if len(unblockedShown) > MaxUnblockedIDsShown {
		unblockedShown = unblockedShown[:MaxUnblockedIDsShown]
	}
	synergyShown := synergyIDs
	if len(synergyShown) > MaxUnblockedIDsShown {
		synergyShown = synergyShown[:MaxUnblockedIDsShown]
	}
	parallelGain := len(directUnblocks) - len(activeIDs)
	explanation := generateWhatIfExplanation(len(directUnblocks), transitiveCount, blockedReduction, estimatedDaysSaved)
	if synergyCount > 0 {
		explanation = fmt.Sprintf("%s (%d unlocked only jointly by completing this batch together)", explanation, synergyCount)
	}

	return &WhatIfBatchResult{
		IssueIDs:        deduped,
		Issues:          items,
		ActiveCount:     len(activeIDs),
		SynergyUnblocks: synergyCount,
		SynergyIssueIDs: synergyShown,
		Delta: WhatIfDelta{
			DirectUnblocks:      len(directUnblocks),
			TransitiveUnblocks:  transitiveCount,
			BlockedReduction:    blockedReduction,
			DepthReduction:      depthReduction,
			EstimatedDaysSaved:  estimatedDaysSaved,
			UnblockedIssueIDs:   unblockedShown,
			ParallelizationGain: &parallelGain,
			Explanation:         explanation,
		},
	}
}

// TopWhatIfDeltas returns the top N issues with highest downstream impact (bv-83)
func (a *Analyzer) TopWhatIfDeltas(n int) []WhatIfEntry {
	stats := a.Analyze()
	return a.TopWhatIfDeltasFromStats(&stats, n)
}

// TopWhatIfDeltasFromStats returns the top N issues with highest downstream
// impact among selected candidates using graph statistics already computed by
// the caller. Context-only issues remain in the graph and readiness authority.
// A nil stats pointer falls back to one synchronous analysis for the whole batch.
func (a *Analyzer) TopWhatIfDeltasFromStats(stats *GraphStats, n int) []WhatIfEntry {
	if stats == nil {
		analyzed := a.Analyze()
		stats = &analyzed
	}

	if n <= 0 {
		n = 10
	}

	var results []WhatIfEntry

	for id, issue := range a.issueMap {
		if !a.IsCandidate(id) || isClosedLikeStatus(issue.Status) {
			continue
		}
		delta := a.computeWhatIfDeltaFromStats(id, stats)
		if delta == nil {
			continue
		}
		if delta.DirectUnblocks > 0 || delta.TransitiveUnblocks > 0 {
			results = append(results, WhatIfEntry{
				IssueID: id,
				Title:   issue.Title,
				Delta:   *delta,
			})
		}
	}

	// Sort by transitive unblocks descending, then by direct unblocks, then by ID
	sort.Slice(results, func(i, j int) bool {
		if results[i].Delta.TransitiveUnblocks != results[j].Delta.TransitiveUnblocks {
			return results[i].Delta.TransitiveUnblocks > results[j].Delta.TransitiveUnblocks
		}
		if results[i].Delta.DirectUnblocks != results[j].Delta.DirectUnblocks {
			return results[i].Delta.DirectUnblocks > results[j].Delta.DirectUnblocks
		}
		return results[i].IssueID < results[j].IssueID
	})

	if n > len(results) {
		n = len(results)
	}

	return results[:n]
}
