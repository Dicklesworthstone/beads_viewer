package analysis

import (
	"fmt"
	"sort"
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
	PageRank      float64 `json:"pagerank"`       // 0.30 weight
	Betweenness   float64 `json:"betweenness"`    // 0.30 weight
	BlockerRatio  float64 `json:"blocker_ratio"`  // 0.20 weight
	Staleness     float64 `json:"staleness"`      // 0.10 weight
	PriorityBoost float64 `json:"priority_boost"` // 0.10 weight

	// Raw normalized values (before weighting)
	PageRankNorm      float64 `json:"pagerank_norm"`
	BetweennessNorm   float64 `json:"betweenness_norm"`
	BlockerRatioNorm  float64 `json:"blocker_ratio_norm"`
	StalenessNorm     float64 `json:"staleness_norm"`
	PriorityBoostNorm float64 `json:"priority_boost_norm"`
}

// Weights for composite score
const (
	WeightPageRank      = 0.30
	WeightBetweenness   = 0.30
	WeightBlockerRatio  = 0.20
	WeightStaleness     = 0.10
	WeightPriorityBoost = 0.10
)

// ComputeImpactScores calculates impact scores for all open issues
func (a *Analyzer) ComputeImpactScores() []ImpactScore {
	return a.ComputeImpactScoresAt(time.Now())
}

// ComputeImpactScoresAt calculates impact scores as of a specific time
func (a *Analyzer) ComputeImpactScoresAt(now time.Time) []ImpactScore {
	// Handle empty issue set
	if len(a.issueMap) == 0 {
		return nil
	}

	stats := a.Analyze()

	// Find max values for normalization
	maxPR := findMax(stats.PageRank)
	maxBW := findMax(stats.Betweenness)
	maxBlockers := findMaxInt(stats.InDegree)

	var scores []ImpactScore

	for id, issue := range a.issueMap {
		// Skip closed issues
		if issue.Status == model.StatusClosed {
			continue
		}

		// Normalize metrics to 0-1
		prNorm := normalize(stats.PageRank[id], maxPR)
		bwNorm := normalize(stats.Betweenness[id], maxBW)
		blockerNorm := normalizeInt(stats.InDegree[id], maxBlockers)
		stalenessNorm := computeStaleness(issue.UpdatedAt, now)
		priorityNorm := computePriorityBoost(issue.Priority)

		// Compute weighted score
		breakdown := ScoreBreakdown{
			PageRank:      prNorm * WeightPageRank,
			Betweenness:   bwNorm * WeightBetweenness,
			BlockerRatio:  blockerNorm * WeightBlockerRatio,
			Staleness:     stalenessNorm * WeightStaleness,
			PriorityBoost: priorityNorm * WeightPriorityBoost,

			PageRankNorm:      prNorm,
			BetweennessNorm:   bwNorm,
			BlockerRatioNorm:  blockerNorm,
			StalenessNorm:     stalenessNorm,
			PriorityBoostNorm: priorityNorm,
		}

		score := breakdown.PageRank +
			breakdown.Betweenness +
			breakdown.BlockerRatio +
			breakdown.Staleness +
			breakdown.PriorityBoost

		scores = append(scores, ImpactScore{
			IssueID:   id,
			Title:     issue.Title,
			Score:     score,
			Breakdown: breakdown,
			Priority:  issue.Priority,
			Status:    string(issue.Status),
		})
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
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

// findMaxInt finds the maximum int value in a map
func findMaxInt(m map[string]int) int {
	max := 0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// PriorityRecommendation represents a suggested priority change
type PriorityRecommendation struct {
	IssueID           string   `json:"issue_id"`
	Title             string   `json:"title"`
	CurrentPriority   int      `json:"current_priority"`
	SuggestedPriority int      `json:"suggested_priority"`
	ImpactScore       float64  `json:"impact_score"`
	Confidence        float64  `json:"confidence"` // 0-1, higher when evidence is strong
	Reasoning         []string `json:"reasoning"`  // Human-readable explanations
	Direction         string   `json:"direction"`  // "increase" or "decrease"
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
	scores := a.ComputeImpactScores()
	if len(scores) == 0 {
		return nil
	}

	// Compute unblocks for reasoning
	unblocksMap := make(map[string]int)
	for _, score := range scores {
		unblocks := a.computeUnblocks(score.IssueID)
		unblocksMap[score.IssueID] = len(unblocks)
	}

	var recommendations []PriorityRecommendation

	for _, score := range scores {
		rec := generateRecommendation(score, unblocksMap[score.IssueID], thresholds)
		if rec != nil && rec.Confidence >= thresholds.MinConfidence {
			recommendations = append(recommendations, *rec)
		}
	}

	// Sort by confidence descending
	sort.Slice(recommendations, func(i, j int) bool {
		return recommendations[i].Confidence > recommendations[j].Confidence
	})

	return recommendations
}

// generateRecommendation creates a recommendation for a single issue
func generateRecommendation(score ImpactScore, unblocksCount int, thresholds RecommendationThresholds) *PriorityRecommendation {
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
	// Base confidence from number of signals
	signalConfidence := float64(signals) / 4.0 // Max 4 signals
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
