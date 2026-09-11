package analysis

import (
	"sort"
	"time"
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
	// Keep the whole batch on one completed snapshot. Reanalyzing each score
	// rereads and reconstructs the same robot disk cache for every issue.
	stats := a.Analyze()
	scores := a.ComputeImpactScoresFromStats(&stats, a.Now())
	if len(scores) == 0 {
		return nil
	}

	// Get basic recommendations
	basicRecs := a.GenerateRecommendationsFromStats(&stats, thresholds)

	// Create a map for quick lookup
	recMap := make(map[string]*PriorityRecommendation)
	for i := range basicRecs {
		recMap[basicRecs[i].IssueID] = &basicRecs[i]
	}

	var enhanced []EnhancedPriorityRecommendation
	now := a.Now().UTC()

	// Enhance each score with what-if deltas
	for _, score := range scores {
		rec, hasRec := recMap[score.IssueID]

		// Generate what-if for all scores (not just those with recommendations)
		whatIf := a.computeWhatIfDeltaFromStats(score.IssueID, &stats)
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
				Deterministic: true,
				Phase2Ready:   true, // Assume Phase 2 is ready
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

	// Sort by impact score descending
	sort.Slice(enhanced, func(i, j int) bool {
		return enhanced[i].ImpactScore > enhanced[j].ImpactScore
	})

	// Cap at 10 items
	if len(enhanced) > 10 {
		enhanced = enhanced[:10]
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
