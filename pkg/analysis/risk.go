package analysis

import (
	"math"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// RiskSignals contains volatility and risk metrics for an issue
type RiskSignals struct {
	// FanVariance measures the variance in blocker fan-in/out in the neighborhood
	// High variance = more unpredictable dependency structure = higher risk
	FanVariance float64 `json:"fan_variance"`

	// ActivityChurn measures edit/comment activity relative to age
	// High churn = volatile issue that may need more attention
	ActivityChurn float64 `json:"activity_churn"`

	// CrossRepoRisk indicates risk from dependencies spanning repositories
	// Higher when dependencies cross repo boundaries (in workspace mode)
	CrossRepoRisk float64 `json:"cross_repo_risk"`

	// StatusRisk indicates risk from current status (blocked = higher risk)
	StatusRisk float64 `json:"status_risk"`

	// CompositeRisk is the weighted combination of all risk signals (0-1)
	CompositeRisk float64 `json:"composite_risk"`

	// Explanation provides human-readable risk assessment
	Explanation string `json:"explanation,omitempty"`
}

// RiskWeights configure the relative importance of risk signals
type RiskWeights struct {
	FanVariance   float64
	ActivityChurn float64
	CrossRepoRisk float64
	StatusRisk    float64
}

// DefaultRiskWeights returns balanced risk weights
func DefaultRiskWeights() RiskWeights {
	return RiskWeights{
		FanVariance:   0.30,
		ActivityChurn: 0.30,
		CrossRepoRisk: 0.20,
		StatusRisk:    0.20,
	}
}

// ComputeRiskSignals calculates risk metrics for a single issue
func ComputeRiskSignals(
	issue *model.Issue,
	stats *GraphStats,
	issues map[string]model.Issue,
	now time.Time,
) RiskSignals {
	return ComputeRiskSignalsWithWeights(issue, stats, issues, now, DefaultRiskWeights())
}

// ComputeRiskSignalsWithWeights calculates risk with custom weights
func ComputeRiskSignalsWithWeights(
	issue *model.Issue,
	stats *GraphStats,
	issues map[string]model.Issue,
	now time.Time,
	weights RiskWeights,
) RiskSignals {
	return ComputeRiskSignalsWithReadiness(issue, stats, issues, nil, now, weights)
}

// ComputeRiskSignalsWithReadiness calculates risk reusing a prebuilt ReadinessIndex
// (such as Analyzer.Readiness()) so batch/scoped evaluation is O(1) per issue.
func ComputeRiskSignalsWithReadiness(
	issue *model.Issue,
	stats *GraphStats,
	issues map[string]model.Issue,
	readiness *model.ReadinessIndex,
	now time.Time,
	weights RiskWeights,
) RiskSignals {
	if issue == nil {
		return RiskSignals{}
	}
	signals := RiskSignals{}

	// 1. Fan variance - measure spread in dependency degrees
	signals.FanVariance = computeFanVariance(issue, stats)

	// 2. Activity churn - comment/edit frequency relative to age
	signals.ActivityChurn = computeActivityChurn(issue, now)

	// 3. Cross-repo risk - dependencies spanning repositories
	signals.CrossRepoRisk = computeCrossRepoRisk(issue, issues)

	// 4. Status risk - blocked/in_progress without recent activity
	signals.StatusRisk = computeStatusRiskWithReadiness(issue, issues, readiness, now)

	// Compute weighted composite
	signals.CompositeRisk = signals.FanVariance*weights.FanVariance +
		signals.ActivityChurn*weights.ActivityChurn +
		signals.CrossRepoRisk*weights.CrossRepoRisk +
		signals.StatusRisk*weights.StatusRisk

	// Cap at 1.0
	if signals.CompositeRisk > 1.0 {
		signals.CompositeRisk = 1.0
	}

	// Generate explanation
	signals.Explanation = generateRiskExplanation(signals)

	return signals
}

// computeFanVariance measures variance in blocker In-Degrees (upstream stability variance)
func computeFanVariance(issue *model.Issue, stats *GraphStats) float64 {
	if issue == nil || stats == nil || len(stats.InDegree) == 0 || len(issue.Dependencies) < 2 {
		return 0
	}

	count := 0
	sum := 0.0
	for _, dep := range issue.Dependencies {
		if dep == nil || !dep.Type.IsBlocking() || dep.DependsOnID == "" {
			continue
		}
		// We use InDegree as a proxy for "Stability/Popularity" of the dependency
		sum += float64(stats.InDegree[dep.DependsOnID])
		count++
	}

	// We need at least 2 dependencies to calculate variance
	if count < 2 || sum == 0 {
		return 0
	}

	// Compute coefficient of variation (std/mean) - normalized variance
	mean := sum / float64(count)
	sumSquares := 0.0
	for _, dep := range issue.Dependencies {
		if dep == nil || !dep.Type.IsBlocking() || dep.DependsOnID == "" {
			continue
		}
		diff := float64(stats.InDegree[dep.DependsOnID]) - mean
		sumSquares += diff * diff
	}
	stdDev := math.Sqrt(sumSquares / float64(count))
	cv := stdDev / mean

	// Normalize: CV > 2 is considered high variance
	normalized := cv / 2.0
	if normalized > 1.0 {
		normalized = 1.0
	}

	return normalized
}

// computeActivityChurn measures edit/comment activity relative to issue age
func computeActivityChurn(issue *model.Issue, now time.Time) float64 {
	if issue.CreatedAt.IsZero() {
		return 0
	}

	// Age in days
	ageHours := now.Sub(issue.CreatedAt).Hours()
	ageDays := ageHours / 24
	if ageDays < 1 {
		ageDays = 1 // Minimum 1 day to avoid division issues
	}

	// Activity signals:
	// - Number of comments
	// - Recent updates (UpdatedAt close to now)
	commentCount := len(issue.Comments)

	// Comments per day (normalized: 1+ comment/day = high churn)
	commentsPerDay := float64(commentCount) / ageDays
	commentChurn := commentsPerDay // Already normalized around 1

	// Update recency (if updated recently relative to creation, more active)
	var updateRecency float64
	if !issue.UpdatedAt.IsZero() && !issue.CreatedAt.IsZero() {
		updateSpan := issue.UpdatedAt.Sub(issue.CreatedAt).Hours() / 24
		if updateSpan > 0 && ageDays > 1 {
			// If updates span most of the issue's lifetime, it's actively being worked
			updateRecency = updateSpan / ageDays
		}
	}

	// Combine signals
	churn := (commentChurn*0.6 + updateRecency*0.4)

	// Normalize to 0-1 range (cap at 1.0)
	if churn > 1.0 {
		churn = 1.0
	}

	return churn
}

// computeCrossRepoRisk measures risk from dependencies spanning repositories
func computeCrossRepoRisk(issue *model.Issue, issues map[string]model.Issue) float64 {
	if issue.SourceRepo == "" || len(issue.Dependencies) == 0 {
		return 0
	}

	thisRepo := issue.SourceRepo
	crossRepoCount := 0
	totalBlockingDeps := 0

	for _, dep := range issue.Dependencies {
		if dep == nil || !dep.Type.IsBlocking() {
			continue
		}
		totalBlockingDeps++

		// Check if dependency is in a different repo
		if depIssue, ok := issues[dep.DependsOnID]; ok {
			if depIssue.SourceRepo != "" && depIssue.SourceRepo != thisRepo {
				crossRepoCount++
			}
		}
	}

	if totalBlockingDeps == 0 {
		return 0
	}

	// Ratio of cross-repo dependencies
	return float64(crossRepoCount) / float64(totalBlockingDeps)
}

// computeStatusRisk measures risk based on current status and activity
func computeStatusRisk(issue *model.Issue, issues map[string]model.Issue, now time.Time) float64 {
	return computeStatusRiskWithReadiness(issue, issues, nil, now)
}

func computeStatusRiskWithReadiness(
	issue *model.Issue,
	issues map[string]model.Issue,
	readiness *model.ReadinessIndex,
	now time.Time,
) float64 {
	var risk float64

	effectiveStatus := issue.Status
	if (effectiveStatus == model.StatusOpen || effectiveStatus == model.StatusInProgress) &&
		isIssueDependencyBlockedWithReadiness(issue, issues, readiness) {
		effectiveStatus = model.StatusBlocked
	}

	switch effectiveStatus {
	case model.StatusClosed, model.StatusTombstone:
		risk = 0
	case model.StatusBlocked:
		// Blocked items have inherent risk
		risk = 0.7
		// Higher risk if blocked for a long time
		if !issue.UpdatedAt.IsZero() {
			daysSinceUpdate := now.Sub(issue.UpdatedAt).Hours() / 24
			if daysSinceUpdate > 7 {
				risk = 0.9
			}
		}

	case model.StatusInProgress:
		// In-progress items have moderate risk if stale
		if !issue.UpdatedAt.IsZero() {
			daysSinceUpdate := now.Sub(issue.UpdatedAt).Hours() / 24
			if daysSinceUpdate > 14 {
				// In progress but no updates in 2 weeks = stuck
				risk = 0.8
			} else if daysSinceUpdate > 7 {
				risk = 0.4
			} else {
				risk = 0.1 // Active work, low risk
			}
		} else {
			risk = 0.3
		}

	case model.StatusOpen:
		// Open items have low base risk
		risk = 0.1
		// But higher if very old
		if !issue.CreatedAt.IsZero() {
			daysSinceCreation := now.Sub(issue.CreatedAt).Hours() / 24
			if daysSinceCreation > 30 {
				risk = 0.3 // Old open issues
			}
		}

	default:
		risk = 0
	}

	return risk
}

func isIssueDependencyBlockedWithReadiness(
	issue *model.Issue,
	issues map[string]model.Issue,
	readiness *model.ReadinessIndex,
) bool {
	if issue == nil {
		return false
	}
	if readiness != nil {
		return readiness.DependencyState(issue.ID) != model.DependenciesSatisfied
	}
	if len(issue.Dependencies) == 0 {
		return false
	}
	r := buildReadinessIndexForMap(issues, issue)
	return r.DependencyState(issue.ID) != model.DependenciesSatisfied
}

func buildReadinessIndexForMap(issues map[string]model.Issue, extra *model.Issue) *model.ReadinessIndex {
	return model.NewReadinessIndexFromMap(issues, extra)
}

// generateRiskExplanation creates a human-readable risk assessment
func generateRiskExplanation(signals RiskSignals) string {
	if signals.CompositeRisk < 0.2 {
		return "Low risk - stable dependency structure"
	}

	var explanationsBuf [4]string
	explanations := explanationsBuf[:0]

	if signals.FanVariance > 0.5 {
		explanations = append(explanations, "high dependency variance")
	}
	if signals.ActivityChurn > 0.6 {
		explanations = append(explanations, "high activity churn")
	}
	if signals.CrossRepoRisk > 0.3 {
		explanations = append(explanations, "cross-repo dependencies")
	}
	if signals.StatusRisk > 0.5 {
		explanations = append(explanations, "status indicates potential blockers")
	}

	if len(explanations) == 0 {
		return "Moderate risk"
	}

	return "Risk factors: " + joinRiskFactors(explanations)
}

// joinRiskFactors joins factors with proper grammar
func joinRiskFactors(factors []string) string {
	if len(factors) == 0 {
		return ""
	}
	if len(factors) == 1 {
		return factors[0]
	}
	if len(factors) == 2 {
		return factors[0] + " and " + factors[1]
	}
	result := factors[0]
	for i := 1; i < len(factors)-1; i++ {
		result += ", " + factors[i]
	}
	result += ", and " + factors[len(factors)-1]
	return result
}

// computeMean calculates the arithmetic mean
func computeMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// computeStdDev calculates the standard deviation
func computeStdDev(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	sumSquares := 0.0
	for _, v := range values {
		diff := v - mean
		sumSquares += diff * diff
	}
	variance := sumSquares / float64(len(values))
	return math.Sqrt(variance)
}

// ComputeAllRiskSignals calculates risk for all issues in the map in O(N + E)
// by building a single ReadinessIndex across the batch.
func ComputeAllRiskSignals(
	issues map[string]model.Issue,
	stats *GraphStats,
	now time.Time,
) map[string]RiskSignals {
	return ComputeAllRiskSignalsWithReadiness(issues, stats, buildReadinessIndexForMap(issues, nil), now)
}

// ComputeAllRiskSignalsWithReadiness calculates risk for all non-closed issues
// in issues reusing a caller-supplied ReadinessIndex.
func ComputeAllRiskSignalsWithReadiness(
	issues map[string]model.Issue,
	stats *GraphStats,
	readiness *model.ReadinessIndex,
	now time.Time,
) map[string]RiskSignals {
	result := make(map[string]RiskSignals, len(issues))
	weights := DefaultRiskWeights()
	if readiness == nil {
		readiness = buildReadinessIndexForMap(issues, nil)
	}

	for id, issue := range issues {
		if isClosedLikeStatus(issue.Status) {
			continue // Skip closed issues
		}
		result[id] = ComputeRiskSignalsWithReadiness(&issue, stats, issues, readiness, now, weights)
	}

	return result
}
