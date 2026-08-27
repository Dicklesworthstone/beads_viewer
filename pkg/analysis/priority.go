package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ImpactScore represents the composite priority score for an issue
type ImpactScore struct {
	IssueID   string         `json:"issue_id"`
	Title     string         `json:"title"`
	Score     float64        `json:"score"`     // Composite 0-1 score
	Breakdown ScoreBreakdown `json:"breakdown"` // Individual components
	Priority  int            `json:"priority"`  // Original priority
	Status    string         `json:"status"`
}

// ScoreBreakdown shows the weighted contribution of each component
type ScoreBreakdown struct {
	PageRank      float64 `json:"pagerank"`       // 0.22 weight
	Betweenness   float64 `json:"betweenness"`    // 0.20 weight
	BlockerRatio  float64 `json:"blocker_ratio"`  // 0.13 weight
	Staleness     float64 `json:"staleness"`      // 0.05 weight
	PriorityBoost float64 `json:"priority_boost"` // 0.10 weight
	TimeToImpact  float64 `json:"time_to_impact"` // 0.10 weight - critical path depth + estimated time
	Urgency       float64 `json:"urgency"`        // 0.10 weight - urgent labels + decay
	Risk          float64 `json:"risk"`           // 0.10 weight - volatility/risk signals (bv-82)

	// Raw normalized values (before weighting)
	PageRankNorm      float64 `json:"pagerank_norm"`
	BetweennessNorm   float64 `json:"betweenness_norm"`
	BlockerRatioNorm  float64 `json:"blocker_ratio_norm"`
	StalenessNorm     float64 `json:"staleness_norm"`
	PriorityBoostNorm float64 `json:"priority_boost_norm"`
	TimeToImpactNorm  float64 `json:"time_to_impact_norm"`
	UrgencyNorm       float64 `json:"urgency_norm"`
	RiskNorm          float64 `json:"risk_norm"`

	// Explanation text for signals
	TimeToImpactExplanation string `json:"time_to_impact_explanation,omitempty"`
	UrgencyExplanation      string `json:"urgency_explanation,omitempty"`
	RiskExplanation         string `json:"risk_explanation,omitempty"`

	// Detailed risk signals (bv-82)
	RiskSignals *RiskSignals `json:"risk_signals,omitempty"`
}

// Weights for composite score (total = 1.0)
const (
	WeightPageRank      = 0.22 // Fundamental dependency importance
	WeightBetweenness   = 0.20 // Bottleneck/bridging importance
	WeightBlockerRatio  = 0.13 // Direct blocking count
	WeightStaleness     = 0.05 // Age-based surfacing (reduced)
	WeightPriorityBoost = 0.10 // Explicit priority
	WeightTimeToImpact  = 0.10 // Time-based urgency from depth + estimates
	WeightUrgency       = 0.10 // Label-based urgency + decay
	WeightRisk          = 0.10 // Volatility/risk signals (bv-82)
)

// UrgencyLabels are labels that indicate high urgency
var UrgencyLabels = []string{"urgent", "critical", "blocker", "hotfix", "asap"}

// DefaultEstimatedMinutes is used when no estimate is provided
const DefaultEstimatedMinutes = 60

// MaxCriticalPathDepth caps the critical path depth for normalization
const MaxCriticalPathDepth = 10.0

// UrgencyDecayDays is the half-life for urgency decay (after this many days, urgency doubles)
const UrgencyDecayDays = 7.0

// ComputeImpactScores calculates impact scores for all open issues
func (a *Analyzer) ComputeImpactScores() []ImpactScore {
	return a.ComputeImpactScoresAt(a.Now())
}

// ComputeImpactScoresAt calculates impact scores as of a specific time
func (a *Analyzer) ComputeImpactScoresAt(now time.Time) []ImpactScore {
	stats := a.Analyze()
	return a.ComputeImpactScoresFromStats(&stats, now)
}

// ComputeImpactScoresFromStats calculates impact scores using provided graph stats
func (a *Analyzer) ComputeImpactScoresFromStats(stats *GraphStats, now time.Time) []ImpactScore {
	// Handle empty issue set
	if len(a.issueMap) == 0 {
		return nil
	}

	// Get Phase 2 data.
	//
	// If Phase 2 is complete, the underlying maps are immutable and safe to read
	// directly (avoids copying O(N) maps on every triage/priority run).
	// If Phase 2 is still computing, fall back to the safe copy accessors.
	var pageRank map[string]float64
	var betweenness map[string]float64
	var criticalPath map[string]float64
	if stats.IsPhase2Ready() {
		stats.mu.RLock()
		pageRank = stats.pageRank
		betweenness = stats.betweenness
		criticalPath = stats.criticalPathScore
		stats.mu.RUnlock()
	} else {
		pageRank = stats.PageRank()
		betweenness = stats.Betweenness()
		criticalPath = stats.CriticalPathScore()
	}

	// Precomputed blocker counts (computed in NewAnalyzer for impact scoring).
	blockerCounts := a.blockerCounts
	maxBlockers := a.blockerCountsMax

	// Find max values for normalization
	maxPR := findMax(pageRank)
	maxBW := findMax(betweenness)

	// Compute median estimated minutes for issues without estimates
	medianMinutes := a.computeMedianEstimatedMinutes()

	// Compute impact scores from stats
	var scores []ImpactScore

	for id, issue := range a.issueMap {
		// Skip closed/tombstone issues
		if isClosedLikeStatus(issue.Status) {
			continue
		}

		// Normalize metrics to 0-1
		prNorm := normalize(pageRank[id], maxPR)
		bwNorm := normalize(betweenness[id], maxBW)
		blockerNorm := 0.0
		if nodeID, ok := a.idToNode[id]; ok {
			if nodeID >= 0 && int(nodeID) < len(blockerCounts) {
				blockerNorm = normalizeInt(blockerCounts[nodeID], maxBlockers)
			}
		}
		stalenessNorm := computeStaleness(issue.UpdatedAt, now)
		priorityNorm := computePriorityBoost(issue.Priority)

		// Compute time-to-impact signal
		timeToImpactNorm, timeToImpactExplanation := computeTimeToImpact(
			criticalPath[id],
			issue.EstimatedMinutes,
			medianMinutes,
		)

		// Compute urgency signal
		urgencyNorm, urgencyExplanation := computeUrgency(&issue, now)

		// Compute risk signals (bv-82)
		riskSignals := ComputeRiskSignals(&issue, stats, a.issueMap, now)

		// Compute weighted score
		breakdown := ScoreBreakdown{
			PageRank:      prNorm * WeightPageRank,
			Betweenness:   bwNorm * WeightBetweenness,
			BlockerRatio:  blockerNorm * WeightBlockerRatio,
			Staleness:     stalenessNorm * WeightStaleness,
			PriorityBoost: priorityNorm * WeightPriorityBoost,
			TimeToImpact:  timeToImpactNorm * WeightTimeToImpact,
			Urgency:       urgencyNorm * WeightUrgency,
			Risk:          riskSignals.CompositeRisk * WeightRisk,

			PageRankNorm:      prNorm,
			BetweennessNorm:   bwNorm,
			BlockerRatioNorm:  blockerNorm,
			StalenessNorm:     stalenessNorm,
			PriorityBoostNorm: priorityNorm,
			TimeToImpactNorm:  timeToImpactNorm,
			UrgencyNorm:       urgencyNorm,
			RiskNorm:          riskSignals.CompositeRisk,

			TimeToImpactExplanation: timeToImpactExplanation,
			UrgencyExplanation:      urgencyExplanation,
			RiskExplanation:         riskSignals.Explanation,

			RiskSignals: &riskSignals,
		}

		score := breakdown.PageRank +
			breakdown.Betweenness +
			breakdown.BlockerRatio +
			breakdown.Staleness +
			breakdown.PriorityBoost +
			breakdown.TimeToImpact +
			breakdown.Urgency +
			breakdown.Risk

		scores = append(scores, ImpactScore{
			IssueID:   id,
			Title:     issue.Title,
			Score:     score,
			Breakdown: breakdown,
			Priority:  issue.Priority,
			Status:    string(issue.Status),
		})
	}

	// Sort by score descending, then by IssueID ascending for stability
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].IssueID < scores[j].IssueID
	})

	return scores
}

// ComputeImpactScore returns the impact score for a single issue
func (a *Analyzer) ComputeImpactScore(issueID string) *ImpactScore {
	scores := a.ComputeImpactScores()
	for i := range scores {
		if scores[i].IssueID == issueID {
			return &scores[i]
		}
	}
	return nil
}

// TopImpactScores returns the top N impact scores
func (a *Analyzer) TopImpactScores(n int) []ImpactScore {
	if n <= 0 {
		return nil
	}
	scores := a.ComputeImpactScores()
	if n > len(scores) {
		n = len(scores)
	}
	return scores[:n]
}

// computeStaleness returns a 0-1 score based on days since update
// Older items get higher staleness to surface them
func computeStaleness(updatedAt time.Time, now time.Time) float64 {
	if updatedAt.IsZero() {
		return 0.5 // Unknown = moderate staleness
	}

	daysSinceUpdate := now.Sub(updatedAt).Hours() / 24

	// Normalize: items older than 30 days get max staleness (1.0)
	// This is a surfacing mechanism - stale items get slightly boosted
	staleness := daysSinceUpdate / 30.0
	if staleness > 1.0 {
		staleness = 1.0
	}
	if staleness < 0 {
		staleness = 0
	}

	return staleness
}

// computePriorityBoost returns a 0-1 boost based on priority
// P0=1.0, P1=0.75, P2=0.5, P3=0.25, P4+=0.0
func computePriorityBoost(priority int) float64 {
	switch priority {
	case 0:
		return 1.0
	case 1:
		return 0.75
	case 2:
		return 0.5
	case 3:
		return 0.25
	default:
		return 0.0
	}
}

// normalize returns v/max, handling zero max
func normalize(v, max float64) float64 {
	if max == 0 {
		return 0
	}
	return v / max
}

// normalizeInt normalizes an int value
func normalizeInt(v, max int) float64 {
	if max == 0 {
		return 0
	}
	return float64(v) / float64(max)
}

// findMax finds the maximum value in a map
func findMax(m map[string]float64) float64 {
	max := 0.0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// computeMedianEstimatedMinutes calculates the median estimated_minutes across all issues
func (a *Analyzer) computeMedianEstimatedMinutes() int {
	var estimates []int
	for _, issue := range a.issueMap {
		if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
			estimates = append(estimates, *issue.EstimatedMinutes)
		}
	}

	if len(estimates) == 0 {
		return DefaultEstimatedMinutes
	}

	sort.Ints(estimates)
	mid := len(estimates) / 2
	if len(estimates)%2 == 0 {
		lower, upper := estimates[mid-1], estimates[mid]
		return lower + (upper-lower)/2
	}
	return estimates[mid]
}

// computeTimeToImpact calculates a normalized time-to-impact score
// based on critical path depth and estimated completion time.
// Returns a 0-1 score where higher means faster/higher impact.
func computeTimeToImpact(criticalPathDepth float64, estimatedMinutes *int, medianMinutes int) (float64, string) {
	// Get effective estimate
	effectiveMinutes := medianMinutes
	estimateSource := "median"
	if estimatedMinutes != nil && *estimatedMinutes > 0 {
		effectiveMinutes = *estimatedMinutes
		estimateSource = "explicit"
	}

	// Compute depth factor (0-1, higher depth = more impact when completed)
	// Cap at MaxCriticalPathDepth to avoid extreme values
	depthNorm := criticalPathDepth / MaxCriticalPathDepth
	if depthNorm > 1.0 {
		depthNorm = 1.0
	}

	// Compute time factor (0-1, shorter time = faster impact)
	// Normalize against 8-hour workday (480 minutes) as baseline
	const maxMinutes = 480.0
	timeFactor := 1.0 - (float64(effectiveMinutes) / maxMinutes)
	if timeFactor < 0 {
		timeFactor = 0
	}
	if timeFactor > 1 {
		timeFactor = 1
	}

	// Combined score: weight depth more heavily since it represents structural importance
	// Depth contributes 70%, time efficiency contributes 30%
	score := depthNorm*0.7 + timeFactor*0.3

	// Generate explanation
	var explanation string
	if criticalPathDepth >= 3 {
		explanation = fmt.Sprintf("Deep in critical path (depth %.0f), %s estimate %dm", criticalPathDepth, estimateSource, effectiveMinutes)
	} else if criticalPathDepth >= 1 {
		explanation = fmt.Sprintf("On dependency chain (depth %.0f), %s estimate %dm", criticalPathDepth, estimateSource, effectiveMinutes)
	} else {
		explanation = fmt.Sprintf("Leaf node, %s estimate %dm", estimateSource, effectiveMinutes)
	}

	return score, explanation
}

// computeUrgency calculates a normalized urgency score based on labels and time decay.
// Returns a 0-1 score where higher means more urgent.
func computeUrgency(issue *model.Issue, now time.Time) (float64, string) {
	var score float64
	var reasons []string

	// Check for urgency labels
	urgentLabelFound := ""
	for _, label := range issue.Labels {
		lowerLabel := strings.ToLower(label)
		for _, urgentLabel := range UrgencyLabels {
			if strings.Contains(lowerLabel, urgentLabel) {
				urgentLabelFound = label
				// Different labels have different urgency weights
				switch urgentLabel {
				case "critical", "blocker":
					score += 1.0
				case "urgent", "hotfix":
					score += 0.8
				case "asap":
					score += 0.6
				}
				break
			}
		}
		if urgentLabelFound != "" {
			break
		}
	}

	if urgentLabelFound != "" {
		reasons = append(reasons, fmt.Sprintf("has '%s' label", urgentLabelFound))
	}

	// Apply time decay: urgency increases as issue ages without resolution
	// Uses exponential growth with half-life of UrgencyDecayDays
	// Handle zero CreatedAt (unknown creation date)
	if issue.CreatedAt.IsZero() {
		// Unknown creation date - return moderate urgency based on labels only
		if score > 1.0 {
			score = 1.0
		}
		explanation := ""
		if len(reasons) > 0 {
			explanation = strings.Join(reasons, ", ")
		}
		return score, explanation
	}

	daysSinceCreated := now.Sub(issue.CreatedAt).Hours() / 24
	if daysSinceCreated > 0 {
		// Decay factor: 0.0 at creation, grows toward 0.5 max contribution
		// Formula: 0.5 * (1 - e^(-days/halfLife))
		decayFactor := 0.5 * (1.0 - math.Exp(-daysSinceCreated/UrgencyDecayDays))
		score += decayFactor

		if daysSinceCreated >= 14 {
			reasons = append(reasons, fmt.Sprintf("aging (%.0f days)", daysSinceCreated))
		}
	}

	// Cap at 1.0
	if score > 1.0 {
		score = 1.0
	}

	// Build explanation
	explanation := ""
	if len(reasons) > 0 {
		explanation = strings.Join(reasons, ", ")
	} else if score > 0.1 {
		explanation = "moderate time pressure"
	}

	return score, explanation
}

// WhatIfDelta shows the impact of completing an issue
type WhatIfDelta struct {
	// DirectUnblocks is the count of issues directly unblocked by completing this
	DirectUnblocks int `json:"direct_unblocks"`
	// TransitiveUnblocks is the total count including downstream cascades
	TransitiveUnblocks int `json:"transitive_unblocks"`
	// BlockedReduction is how many fewer issues would be blocked
	BlockedReduction int `json:"blocked_reduction"`
	// DepthReduction estimates the critical path depth reduction
	DepthReduction float64 `json:"depth_reduction"`
	// EstimatedDaysSaved estimates days saved based on unblocked work
	EstimatedDaysSaved float64 `json:"estimated_days_saved,omitempty"`
	// UnblockedIssueIDs lists the IDs that would be unblocked (capped at 10)
	UnblockedIssueIDs []string `json:"unblocked_issue_ids,omitempty"`
	// ParallelizationGain is the net change in actionable work capacity (bv-129)
	// Positive = more parallel work possible after completion; nil = not computed (below top-N)
	ParallelizationGain *int `json:"parallelization_gain,omitempty"`
	// Explanation summarizes the what-if impact
	Explanation string `json:"explanation"`
}

// PriorityRecommendation represents a suggested priority change
type PriorityRecommendation struct {
	IssueID           string       `json:"issue_id"`
	Title             string       `json:"title"`
	CurrentPriority   int          `json:"current_priority"`
	SuggestedPriority int          `json:"suggested_priority"`
	ImpactScore       float64      `json:"impact_score"`
	Confidence        float64      `json:"confidence"`        // 0-1, higher when evidence is strong
	Reasoning         []string     `json:"reasoning"`         // Human-readable explanations (top 3)
	Direction         string       `json:"direction"`         // "increase" or "decrease"
	WhatIf            *WhatIfDelta `json:"what_if,omitempty"` // Impact of completing this issue
}

// RecommendationThresholds configure when to suggest priority changes
type RecommendationThresholds struct {
	HighPageRank     float64 // Normalized PageRank above this suggests high priority
	HighBetweenness  float64 // Normalized Betweenness above this suggests high priority
	StalenessDays    int     // Days since update to mention staleness
	MinConfidence    float64 // Minimum confidence to include recommendation
	SignificantDelta float64 // Score difference to suggest priority change
}

// DefaultThresholds returns sensible default thresholds
func DefaultThresholds() RecommendationThresholds {
	return RecommendationThresholds{
		HighPageRank:     0.3,
		HighBetweenness:  0.5,
		StalenessDays:    14,
		MinConfidence:    0.3,
		SignificantDelta: 0.15,
	}
}

// GenerateRecommendations analyzes impact scores and suggests priority adjustments
func (a *Analyzer) GenerateRecommendations() []PriorityRecommendation {
	return a.GenerateRecommendationsWithThresholds(DefaultThresholds())
}

// GenerateRecommendationsWithThresholds generates recommendations with custom thresholds
func (a *Analyzer) GenerateRecommendationsWithThresholds(thresholds RecommendationThresholds) []PriorityRecommendation {
	scores := a.ComputeImpactScoresAt(a.Now())
	if len(scores) == 0 {
		return nil
	}

	stats := a.Analyze()
	coreMap := stats.CoreNumber()
	slackMap := stats.Slack()
	artSet := make(map[string]bool)
	for _, id := range stats.ArticulationPoints() {
		artSet[id] = true
	}
	maxCore := 0
	for _, v := range coreMap {
		if v > maxCore {
			maxCore = v
		}
	}

	// Compute unblocks for reasoning
	unblocksMap := make(map[string]int)
	for _, score := range scores {
		unblocks := a.computeUnblocks(score.IssueID)
		unblocksMap[score.IssueID] = len(unblocks)
	}

	var recommendations []PriorityRecommendation

	for _, score := range scores {
		rec := generateRecommendation(
			score,
			unblocksMap[score.IssueID],
			coreMap[score.IssueID],
			artSet[score.IssueID],
			slackMap[score.IssueID],
			maxCore,
			thresholds,
		)
		if rec != nil {
			if rec.Confidence >= thresholds.MinConfidence {
				// Compute what-if delta for this recommendation (bv-83)
				rec.WhatIf = a.computeWhatIfDelta(score.IssueID)
				recommendations = append(recommendations, *rec)
			}
		}
	}

	// Sort by confidence descending, then by impact score, then by ID for determinism (bv-83)
	sort.Slice(recommendations, func(i, j int) bool {
		if recommendations[i].Confidence != recommendations[j].Confidence {
			return recommendations[i].Confidence > recommendations[j].Confidence
		}
		if recommendations[i].ImpactScore != recommendations[j].ImpactScore {
			return recommendations[i].ImpactScore > recommendations[j].ImpactScore
		}
		return recommendations[i].IssueID < recommendations[j].IssueID
	})

	return recommendations
}

// generateRecommendation creates a recommendation for a single issue
func generateRecommendation(score ImpactScore, unblocksCount int, core int, isArt bool, slack float64, maxCore int, thresholds RecommendationThresholds) *PriorityRecommendation {
	var reasoning []string
	var signals int
	var signalStrength float64

	// Check PageRank (fundamental dependency)
	if score.Breakdown.PageRankNorm > thresholds.HighPageRank {
		reasoning = append(reasoning, "High centrality in dependency graph")
		signals++
		signalStrength += score.Breakdown.PageRankNorm
	}

	// Check Betweenness (bottleneck)
	if score.Breakdown.BetweennessNorm > thresholds.HighBetweenness {
		reasoning = append(reasoning, "Critical path bottleneck")
		signals++
		signalStrength += score.Breakdown.BetweennessNorm
	}

	// Check unblocks count
	if unblocksCount >= 3 {
		reasoning = append(reasoning, fmt.Sprintf("Blocks %d other items", unblocksCount))
		signals++
		signalStrength += 0.5 + float64(unblocksCount)/10.0
	} else if unblocksCount == 2 {
		reasoning = append(reasoning, "Blocks 2 other items")
		signals++
		signalStrength += 0.3
	} else if unblocksCount == 1 {
		reasoning = append(reasoning, "Blocks 1 other item")
		signals++
		signalStrength += 0.2
	}

	// Check staleness
	if score.Breakdown.StalenessNorm >= float64(thresholds.StalenessDays)/30.0 {
		days := int(score.Breakdown.StalenessNorm * 30)
		reasoning = append(reasoning, fmt.Sprintf("Stale for %d+ days", days))
		signals++
		signalStrength += 0.2
	}

	// Check time-to-impact signal
	if score.Breakdown.TimeToImpactNorm > 0.5 {
		if score.Breakdown.TimeToImpactExplanation != "" {
			reasoning = append(reasoning, score.Breakdown.TimeToImpactExplanation)
		} else {
			reasoning = append(reasoning, "High time-to-impact score")
		}
		signals++
		signalStrength += score.Breakdown.TimeToImpactNorm
	}

	// Check urgency signal
	if score.Breakdown.UrgencyNorm > 0.3 {
		if score.Breakdown.UrgencyExplanation != "" {
			reasoning = append(reasoning, score.Breakdown.UrgencyExplanation)
		} else {
			reasoning = append(reasoning, "Elevated urgency")
		}
		signals++
		signalStrength += score.Breakdown.UrgencyNorm
	}

	// Check risk signal (bv-82)
	if score.Breakdown.RiskNorm > 0.4 {
		if score.Breakdown.RiskExplanation != "" {
			reasoning = append(reasoning, score.Breakdown.RiskExplanation)
		} else {
			reasoning = append(reasoning, "Elevated risk/volatility")
		}
		signals++
		signalStrength += score.Breakdown.RiskNorm
	}

	// Structural signals (bv-85)
	if isArt {
		reasoning = append(reasoning, "Articulation point (disconnects graph)")
		signals++
		signalStrength += 0.35
	}
	if maxCore > 0 && core == maxCore {
		reasoning = append(reasoning, fmt.Sprintf("High cohesion (k-core %d)", core))
		signals++
		signalStrength += 0.3
	}
	if slack == 0 {
		reasoning = append(reasoning, "Zero slack on critical chain")
		signals++
		signalStrength += 0.25
	} else if slack > 2 {
		// Parallel-friendly; softer weight so it doesn't overshadow bottlenecks
		reasoning = append(reasoning, "Parallel-friendly (slack available)")
		signals++
		signalStrength += 0.15
	}

	// No signals = no recommendation needed
	if signals == 0 {
		return nil
	}

	// Calculate suggested priority based on impact score
	suggestedPriority := scoreToPriority(score.Score)

	// If no change suggested, skip
	if suggestedPriority == score.Priority {
		return nil
	}

	// Calculate confidence based on signals and delta
	scoreDelta := abs(score.Score - priorityToScore(score.Priority))
	confidence := calculateConfidence(signals, signalStrength, scoreDelta, thresholds)

	direction := "increase"
	if suggestedPriority > score.Priority {
		direction = "decrease"
	}

	// Cap reasoning at top 3 for conciseness (bv-83)
	if len(reasoning) > 3 {
		reasoning = reasoning[:3]
	}

	return &PriorityRecommendation{
		IssueID:           score.IssueID,
		Title:             score.Title,
		CurrentPriority:   score.Priority,
		SuggestedPriority: suggestedPriority,
		ImpactScore:       score.Score,
		Confidence:        confidence,
		Reasoning:         reasoning,
		Direction:         direction,
	}
}

// scoreToPriority converts an impact score (0-1) to a priority (0-4)
func scoreToPriority(score float64) int {
	switch {
	case score >= 0.7:
		return 0 // P0 - Critical
	case score >= 0.5:
		return 1 // P1 - High
	case score >= 0.3:
		return 2 // P2 - Medium
	case score >= 0.15:
		return 3 // P3 - Low
	default:
		return 4 // P4 - Very Low
	}
}

// priorityToScore converts a priority to an expected score
func priorityToScore(priority int) float64 {
	switch priority {
	case 0:
		return 0.8
	case 1:
		return 0.6
	case 2:
		return 0.4
	case 3:
		return 0.2
	default:
		return 0.1
	}
}

// calculateConfidence determines how confident we are in the recommendation
func calculateConfidence(signals int, strength float64, scoreDelta float64, thresholds RecommendationThresholds) float64 {
	// Base confidence from number of signals (max 10: PageRank, Betweenness, unblocks,
	// staleness, time-to-impact, urgency, risk, articulation, k-core, slack)
	signalConfidence := float64(signals) / 10.0
	if signalConfidence > 1.0 {
		signalConfidence = 1.0
	}

	// Boost from signal strength
	strengthBoost := strength / 2.0
	if strengthBoost > 0.3 {
		strengthBoost = 0.3
	}

	// Boost from score delta (bigger mismatch = higher confidence)
	deltaBoost := 0.0
	if scoreDelta >= thresholds.SignificantDelta {
		deltaBoost = 0.2
	}

	confidence := signalConfidence + strengthBoost + deltaBoost
	if confidence > 1.0 {
		confidence = 1.0
	}

	return confidence
}

// abs returns absolute value of float64
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// MaxUnblockedIDsShown caps the number of unblocked issue IDs shown in what-if
const MaxUnblockedIDsShown = 10

// computeWhatIfDelta calculates the impact of completing an issue (bv-83)
func (a *Analyzer) computeWhatIfDelta(issueID string) *WhatIfDelta {
	stats := a.Analyze()
	return a.computeWhatIfDeltaFromStats(issueID, &stats)
}

// computeWhatIfDeltaFromStats calculates the impact of completing an issue
// using graph statistics already owned by the caller.
func (a *Analyzer) computeWhatIfDeltaFromStats(issueID string, stats *GraphStats) *WhatIfDelta {
	if stats == nil {
		return a.computeWhatIfDelta(issueID)
	}

	// Get direct unblocks using existing method
	directUnblocks := a.computeUnblocks(issueID)
	directCount := len(directUnblocks)

	// Compute transitive unblocks (cascade effect)
	transitiveCount := a.countTransitiveUnblocks(issueID)

	// Compute blocked reduction (how many items in blocked status would become unblocked)
	blockedReduction := 0
	for _, unblockID := range directUnblocks {
		if issue, ok := a.issueMap[unblockID]; ok {
			if issue.Status == model.StatusBlocked {
				blockedReduction++
			}
		}
	}

	// Compute depth reduction based on critical path (O(1) lookup via bv-77ec)
	depthReduction := 0.0
	currentDepth, _ := stats.CriticalPathValue(issueID)
	if currentDepth > 0 {
		// Estimate depth reduction as a fraction of current depth
		depthReduction = currentDepth / MaxCriticalPathDepth
		if depthReduction > 1.0 {
			depthReduction = 1.0
		}
	}

	// Estimate days saved based on unblocked work
	estimatedDaysSaved := estimateDaysSaved(directUnblocks, a.issueMap)

	// Cap unblocked IDs for output
	unblockedIDs := directUnblocks
	if len(unblockedIDs) > MaxUnblockedIDsShown {
		unblockedIDs = unblockedIDs[:MaxUnblockedIDsShown]
	}

	// Generate explanation
	explanation := generateWhatIfExplanation(directCount, transitiveCount, blockedReduction, estimatedDaysSaved)

	// Compute parallelization gain (bv-129)
	// Net change in actionable work capacity: completing 1 issue, gaining N unblocked
	parallelGain := directCount - 1

	return &WhatIfDelta{
		DirectUnblocks:      directCount,
		TransitiveUnblocks:  transitiveCount,
		BlockedReduction:    blockedReduction,
		DepthReduction:      depthReduction,
		EstimatedDaysSaved:  estimatedDaysSaved,
		UnblockedIssueIDs:   unblockedIDs,
		ParallelizationGain: &parallelGain,
		Explanation:         explanation,
	}
}

// countTransitiveUnblocks counts total issues unblocked by a hypothetical completion of issueID,
// including cascading effects (diamonds, chains) via simulation.
func (a *Analyzer) countTransitiveUnblocks(issueID string) int {
	issue, exists := a.issueMap[issueID]
	if !exists || isClosedLikeStatus(issue.Status) {
		return 0
	}
	nodeID := a.idToNode[issueID]
	if a.g.To(nodeID).Len() == 0 && len(a.childrenByParent[issueID]) == 0 {
		return 0
	}

	// Set of "conceptually closed" issues: initially just the starting issue
	simulatedClosed := map[string]bool{issueID: true}

	// Existing actionable work is not an effect of this hypothetical completion
	// and must not be counted or auto-completed as part of the cascade.
	baselineActionable := make(map[string]bool)
	for _, actionable := range a.getActionableIssuesAfterCompletions(nil) {
		baselineActionable[actionable.ID] = true
	}

	queue := []string{issueID}
	count := 0

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		candidateSet := make(map[string]bool)
		if nodeID, ok := a.idToNode[curr]; ok {
			dependents := a.g.To(nodeID)
			for dependents.Next() {
				candidateSet[a.nodeToID[dependents.Node().ID()]] = true
			}
		}
		for _, childID := range a.childrenByParent[curr] {
			candidateSet[childID] = true
		}

		candidateIDs := make([]string, 0, len(candidateSet))
		for candidateID := range candidateSet {
			candidateIDs = append(candidateIDs, candidateID)
		}
		sort.Strings(candidateIDs)

		for _, candidateID := range candidateIDs {
			if simulatedClosed[candidateID] || baselineActionable[candidateID] {
				continue
			}
			if a.isActionableAfterCompletions(candidateID, simulatedClosed) {
				simulatedClosed[candidateID] = true
				queue = append(queue, candidateID)
				count++
			}
		}
	}

	return count
}

// estimateDaysSaved estimates work-days saved by unblocking issues
func estimateDaysSaved(unblockedIDs []string, issueMap map[string]model.Issue) float64 {
	if len(unblockedIDs) == 0 {
		return 0
	}

	totalMinutes := 0.0
	counted := 0

	for _, id := range unblockedIDs {
		if issue, ok := issueMap[id]; ok {
			if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
				totalMinutes += float64(*issue.EstimatedMinutes)
				counted++
			} else {
				// Use default estimate for unestimated work
				totalMinutes += DefaultEstimatedMinutes
				counted++
			}
		}
	}

	if counted == 0 {
		return 0
	}

	// Convert to days (8-hour workday = 480 minutes)
	return totalMinutes / 480.0
}

// generateWhatIfExplanation creates a human-readable what-if summary
func generateWhatIfExplanation(direct, transitive, blockedReduction int, daysSaved float64) string {
	if direct == 0 {
		return "No immediate downstream impact"
	}

	explanation := fmt.Sprintf("Completing this directly unblocks %d item", direct)
	if direct != 1 {
		explanation += "s"
	}

	if transitive > direct {
		explanation += fmt.Sprintf(" (%d total including cascades)", transitive)
	}

	if blockedReduction > 0 {
		explanation += fmt.Sprintf(", clears %d blocked", blockedReduction)
	}

	if daysSaved >= 0.5 {
		explanation += fmt.Sprintf(", enabling ~%.1f days of work", daysSaved)
	}

	return explanation
}
