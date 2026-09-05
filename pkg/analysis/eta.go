package analysis

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ETAEstimate is a lightweight, deterministic ETA prediction for a single issue.
// It is designed to be surfaced by robot outputs (bv-158) and used by capacity simulation (bv-160).
type ETAEstimate struct {
	IssueID               string    `json:"issue_id"`
	EstimatedMinutes      int       `json:"estimated_minutes"`
	EstimatedDays         float64   `json:"estimated_days"`
	ETADate               time.Time `json:"eta_date"`
	ETADateLow            time.Time `json:"eta_date_low,omitempty"`
	ETADateHigh           time.Time `json:"eta_date_high,omitempty"`
	Confidence            float64   `json:"confidence"` // 0..1
	VelocityMinutesPerDay float64   `json:"velocity_minutes_per_day"`
	Agents                int       `json:"agents"`
	Factors               []string  `json:"factors,omitempty"`
}

// EstimateETAForIssue estimates an ETA for a single issue using:
// - Complexity minutes: explicit estimate or median base, then type × depth × description-length multipliers for either base.
// - Velocity minutes/day: derived from recent closures of issues sharing labels (fallback to global, then default).
// - ETA days = minutes / (velocity * agents), with a simple confidence interval.
func EstimateETAForIssue(issues []model.Issue, stats *GraphStats, issueID string, agents int, now time.Time) (ETAEstimate, error) {
	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}
	issue, ok := issueMap[issueID]
	if !ok {
		return ETAEstimate{}, fmt.Errorf("issue %q not found", issueID)
	}

	if agents <= 0 {
		agents = 1
	}

	medianMinutes := computeMedianEstimatedMinutes(issues)
	complexityMinutes, complexityFactors := estimateComplexityMinutes(issue, stats, medianMinutes)

	velocityPerDay, velocitySamples, velocityFactors := estimateVelocityMinutesPerDay(issues, issue, now, medianMinutes)
	if velocityPerDay <= 0 {
		// Conservative default: one median-sized issue per (work) week.
		velocityPerDay = float64(medianMinutes) / 5.0
		if velocityPerDay <= 0 {
			velocityPerDay = 60 // final fallback: 1h/day
		}
		velocityFactors = append(velocityFactors, "velocity: no recent closures; using default")
	}

	capacityPerDay := velocityPerDay * float64(agents)
	estimatedDays := float64(complexityMinutes) / capacityPerDay
	if estimatedDays < 0 {
		estimatedDays = 0
	}

	confidence := estimateETAConfidence(issue, velocitySamples)
	deltaDays := max(0.5, estimatedDays*(1.0-confidence)*0.8)

	eta := now.Add(durationDays(estimatedDays))
	etaLow := now.Add(durationDays(max(0.0, estimatedDays-deltaDays)))
	etaHigh := now.Add(durationDays(estimatedDays + deltaDays))

	factors := append([]string{}, complexityFactors...)
	factors = append(factors, velocityFactors...)
	factors = append(factors, fmt.Sprintf("agents: %d", agents))

	// Keep factors deterministic and small.
	if len(factors) > 8 {
		factors = factors[:8]
	}

	return ETAEstimate{
		IssueID:               issueID,
		EstimatedMinutes:      complexityMinutes,
		EstimatedDays:         estimatedDays,
		ETADate:               eta,
		ETADateLow:            etaLow,
		ETADateHigh:           etaHigh,
		Confidence:            confidence,
		VelocityMinutesPerDay: velocityPerDay,
		Agents:                agents,
		Factors:               factors,
	}, nil
}

func estimateComplexityMinutes(issue model.Issue, stats *GraphStats, medianMinutes int) (int, []string) {
	var factors []string

	baseMinutes := medianMinutes
	estimateSource := "median"
	if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
		baseMinutes = *issue.EstimatedMinutes
		estimateSource = "explicit"
	}
	if baseMinutes <= 0 {
		baseMinutes = DefaultEstimatedMinutes
		estimateSource = "default"
	}
	factors = append(factors, fmt.Sprintf("estimate: %s (%dm)", estimateSource, baseMinutes))

	// Type weight
	typeWeight := 1.0
	switch issue.IssueType {
	case model.TypeBug:
		typeWeight = 1.0
	case model.TypeTask:
		typeWeight = 1.0
	case model.TypeChore:
		typeWeight = 0.8
	case model.TypeFeature:
		typeWeight = 1.3
	case model.TypeEpic:
		typeWeight = 2.0
	default:
		typeWeight = 1.0
	}
	factors = append(factors, fmt.Sprintf("type: %s×%.1f", issue.IssueType, typeWeight))

	// Dependency depth (critical path depth) — deeper issues tend to carry more coordination cost.
	depth := 0.0
	if stats != nil {
		depth = stats.GetCriticalPathScore(issue.ID)
	}
	depthFactor := 1.0 + min(1.0, depth/10.0) // up to 2×
	factors = append(factors, fmt.Sprintf("depth: %.0f×%.2f", depth, depthFactor))

	// Description length proxy — long specs often hide complexity.
	descRunes := len([]rune(issue.Description))
	descFactor := 1.0 + min(1.0, float64(descRunes)/2000.0) // up to 2×
	if descRunes > 0 {
		factors = append(factors, fmt.Sprintf("desc: %dr×%.2f", descRunes, descFactor))
	} else {
		factors = append(factors, "desc: empty×1.00")
	}

	derived := int(float64(baseMinutes) * typeWeight * depthFactor * descFactor)
	if derived <= 0 {
		derived = baseMinutes
	}
	return derived, factors
}

func estimateVelocityMinutesPerDay(issues []model.Issue, issue model.Issue, now time.Time, medianMinutes int) (float64, int, []string) {
	const windowDays = 30
	since := now.Add(-time.Duration(windowDays) * 24 * time.Hour)

	labels := issue.Labels
	if len(labels) == 0 {
		v, n := velocityMinutesPerDayForLabel(issues, "", since, medianMinutes)
		return v, n, []string{fmt.Sprintf("velocity: global (%d samples/30d)", n)}
	}

	// Use the slowest non-zero label velocity for conservative ETA (deterministic).
	bestLabel := ""
	bestV := 0.0
	bestN := 0
	for _, label := range labels {
		v, n := velocityMinutesPerDayForLabel(issues, label, since, medianMinutes)
		if n == 0 || v <= 0 {
			continue
		}
		if bestV == 0 || v < bestV || (v == bestV && strings.ToLower(label) < strings.ToLower(bestLabel)) {
			bestLabel = label
			bestV = v
			bestN = n
		}
	}
	if bestV > 0 {
		return bestV, bestN, []string{fmt.Sprintf("velocity: label=%s (%.0f min/day, %d samples/30d)", bestLabel, bestV, bestN)}
	}

	// Fallback: global velocity.
	v, n := velocityMinutesPerDayForLabel(issues, "", since, medianMinutes)
	return v, n, []string{fmt.Sprintf("velocity: global (%d samples/30d)", n)}
}

func velocityMinutesPerDayForLabel(issues []model.Issue, label string, since time.Time, medianMinutes int) (float64, int) {
	total := 0
	samples := 0

	for _, iss := range issues {
		if iss.Status != model.StatusClosed {
			continue
		}

		// Robust closure time: use ClosedAt if available, else UpdatedAt
		closedAt := iss.UpdatedAt
		if iss.ClosedAt != nil {
			closedAt = *iss.ClosedAt
		}

		if closedAt.Before(since) {
			continue
		}
		if label != "" && !hasLabel(iss.Labels, label) {
			continue
		}

		minutes := medianMinutes
		if iss.EstimatedMinutes != nil && *iss.EstimatedMinutes > 0 {
			minutes = *iss.EstimatedMinutes
		}
		if minutes <= 0 {
			minutes = DefaultEstimatedMinutes
		}
		total += minutes
		samples++
	}

	if samples == 0 {
		return 0, 0
	}
	return float64(total) / 30.0, samples
}

func hasLabel(labels []string, target string) bool {
	if target == "" {
		return false
	}
	targetLower := strings.ToLower(target)
	for _, l := range labels {
		if strings.ToLower(l) == targetLower {
			return true
		}
	}
	return false
}

func estimateETAConfidence(issue model.Issue, velocitySamples int) float64 {
	conf := 0.25

	if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
		conf += 0.25
	}
	switch {
	case velocitySamples >= 15:
		conf += 0.30
	case velocitySamples >= 5:
		conf += 0.20
	case velocitySamples >= 1:
		conf += 0.10
	default:
		conf -= 0.05
	}
	if len(issue.Labels) == 0 {
		conf -= 0.05
	}

	return clampFloat(conf, 0.10, 0.90)
}

// computeMedianEstimatedMinutes calculates the median estimated_minutes from a list of issues
func computeMedianEstimatedMinutes(issues []model.Issue) int {
	var estimates []int
	for _, issue := range issues {
		if issue.EstimatedMinutes != nil && *issue.EstimatedMinutes > 0 {
			estimates = append(estimates, *issue.EstimatedMinutes)
		}
	}

	if len(estimates) == 0 {
		return DefaultEstimatedMinutes
	}

	// Sort for median calculation
	sort.Ints(estimates)

	mid := len(estimates) / 2
	if len(estimates)%2 == 0 {
		return (estimates[mid-1] + estimates[mid]) / 2
	}
	return estimates[mid]
}

func durationDays(days float64) time.Duration {
	if days <= 0 {
		return 0
	}
	return time.Duration(days * float64(24*time.Hour))
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
