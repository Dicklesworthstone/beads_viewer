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

	velocityPerDay, velocitySamples, velocityFactors := estimateVelocityMinutesPerDayWithStats(issues, stats, issue, now, medianMinutes)
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

// buehlmannStraubPriorWeight is the empirical-Bayes credibility prior strength k
// used to shrink noisy small-sample label velocities toward global project velocity:
//
//	v_shrunk = (n / (n + k)) * v_label + (k / (n + k)) * v_global
const buehlmannStraubPriorWeight = 5.0

func estimateVelocityMinutesPerDay(issues []model.Issue, issue model.Issue, now time.Time, medianMinutes int) (float64, int, []string) {
	return estimateVelocityMinutesPerDayWithStats(issues, nil, issue, now, medianMinutes)
}

func estimateVelocityMinutesPerDayWithStats(issues []model.Issue, stats *GraphStats, issue model.Issue, now time.Time, medianMinutes int) (float64, int, []string) {
	const windowDays = 30
	since := now.Add(-time.Duration(windowDays) * 24 * time.Hour)

	globalV, globalN := velocityMinutesPerDayForLabelWithStats(issues, stats, "", since, medianMinutes)
	labels := issue.Labels
	if len(labels) == 0 {
		return globalV, globalN, []string{fmt.Sprintf("velocity: global (%d samples/30d)", globalN)}
	}

	// Shrink each observed label velocity toward global velocity via Bühlmann-Straub
	// credibility weighting, then pick the slowest non-zero shrunk label velocity
	// for conservative, deterministic ETA estimation.
	bestLabel := ""
	bestV := 0.0
	bestN := 0
	for _, label := range labels {
		rawV, n := velocityMinutesPerDayForLabelWithStats(issues, stats, label, since, medianMinutes)
		if n == 0 || rawV <= 0 {
			continue
		}
		priorV := rawV
		if globalN > 0 && globalV > 0 {
			priorV = globalV
		}
		nf := float64(n)
		shrunkV := (nf*rawV + buehlmannStraubPriorWeight*priorV) / (nf + buehlmannStraubPriorWeight)
		if bestV == 0 || shrunkV < bestV || (shrunkV == bestV && strings.ToLower(label) < strings.ToLower(bestLabel)) {
			bestLabel = label
			bestV = shrunkV
			bestN = n
		}
	}
	if bestV > 0 {
		return bestV, bestN, []string{fmt.Sprintf("velocity: label=%s (%.0f min/day, %d samples/30d)", bestLabel, bestV, bestN)}
	}

	// Fallback: global velocity.
	return globalV, globalN, []string{fmt.Sprintf("velocity: global (%d samples/30d)", globalN)}
}

func velocityMinutesPerDayForLabel(issues []model.Issue, label string, since time.Time, medianMinutes int) (float64, int) {
	return velocityMinutesPerDayForLabelWithStats(issues, nil, label, since, medianMinutes)
}

func velocityMinutesPerDayForLabelWithStats(issues []model.Issue, stats *GraphStats, label string, since time.Time, medianMinutes int) (float64, int) {
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

		// Measure closed work with the exact same complexity scale used for open work
		// so numerator and denominator share identical units (eliminating 5.4x-10.8x bias).
		minutes, _ := estimateComplexityMinutes(iss, stats, medianMinutes)
		total += minutes
		samples++
	}

	if samples == 0 {
		return 0, 0
	}
	return float64(total) / 30.0, samples
}

// GrahamBounds captures Graham (1969) parallel list-scheduling makespan bounds:
//
//	T_min(m) = max(L_crit, W / m)
//	T_max(m) = L_crit + max(0, W - L_crit) / m
//
// expressed both in raw work-minutes and in velocity-calibrated days.
type GrahamBounds struct {
	TotalMinutes          int     `json:"total_minutes"`
	CriticalPathMinutes   int     `json:"critical_path_minutes"`
	GrahamMinMinutes      float64 `json:"graham_min_minutes"`
	GrahamMaxMinutes      float64 `json:"graham_max_minutes"`
	VelocityMinutesPerDay float64 `json:"velocity_minutes_per_day"`
	GrahamMinDays         float64 `json:"graham_min_days"`
	GrahamMaxDays         float64 `json:"graham_max_days"`
}

// ComputeGrahamBounds computes Graham (1969) lower and upper makespan bounds for
// executing openIssues across agents parallel workers, calibrated against the
// project's effective closure velocity over allIssues.
func ComputeGrahamBounds(openIssues []model.Issue, allIssues []model.Issue, stats *GraphStats, agents int, now time.Time) GrahamBounds {
	if agents <= 0 {
		agents = 1
	}
	medianMinutes := computeMedianEstimatedMinutes(allIssues)
	since := now.Add(-30 * 24 * time.Hour)
	velocityPerDay, _ := velocityMinutesPerDayForLabelWithStats(allIssues, stats, "", since, medianMinutes)
	if velocityPerDay <= 0 {
		velocityPerDay = 480.0 // Default 8h workday when project has no 30d closure history
	}

	if len(openIssues) == 0 {
		return GrahamBounds{VelocityMinutesPerDay: velocityPerDay}
	}

	weights := make(map[string]int, len(openIssues))
	openSet := make(map[string]model.Issue, len(openIssues))
	totalMinutes := 0
	for _, iss := range openIssues {
		w, _ := estimateComplexityMinutes(iss, stats, medianMinutes)
		weights[iss.ID] = w
		openSet[iss.ID] = iss
		totalMinutes += w
	}

	// Build prerequisite -> dependent adjacency on the unresolved subgraph.
	dependents := make(map[string][]string, len(openIssues))
	for _, iss := range openIssues {
		seen := make(map[string]bool)
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() || seen[dep.DependsOnID] || dep.DependsOnID == iss.ID {
				continue
			}
			if _, ok := openSet[dep.DependsOnID]; ok {
				seen[dep.DependsOnID] = true
				dependents[dep.DependsOnID] = append(dependents[dep.DependsOnID], iss.ID)
			}
		}
	}
	for k := range dependents {
		sort.Strings(dependents[k])
	}

	// Longest work-weighted simple path (memoized on acyclic subgraphs, cycle-safe).
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(openIssues))
	memo := make(map[string]int, len(openIssues))
	hasCycle := false

	var dfsDAG func(id string) int
	dfsDAG = func(id string) int {
		if state[id] == visited {
			return memo[id]
		}
		if state[id] == visiting {
			hasCycle = true
			return 0
		}
		state[id] = visiting
		bestSuffix := 0
		for _, next := range dependents[id] {
			if cand := dfsDAG(next); cand > bestSuffix {
				bestSuffix = cand
			}
		}
		state[id] = visited
		memo[id] = weights[id] + bestSuffix
		return memo[id]
	}

	ids := make([]string, 0, len(openIssues))
	for id := range openSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	critMinutes := 0
	for _, id := range ids {
		if cand := dfsDAG(id); cand > critMinutes {
			critMinutes = cand
		}
	}

	if hasCycle {
		// Cycle fallback: bounded simple-path search so cyclic components remain exact/finite.
		critMinutes = 0
		onPath := make(map[string]bool, len(ids))
		var dfsSimple func(id string, depth int) int
		dfsSimple = func(id string, depth int) int {
			if depth > 64 {
				return weights[id]
			}
			onPath[id] = true
			bestSuffix := 0
			for _, next := range dependents[id] {
				if onPath[next] {
					continue
				}
				if cand := dfsSimple(next, depth+1); cand > bestSuffix {
					bestSuffix = cand
				}
			}
			onPath[id] = false
			return weights[id] + bestSuffix
		}
		for _, id := range ids {
			if cand := dfsSimple(id, 0); cand > critMinutes {
				critMinutes = cand
			}
		}
	}
	if critMinutes > totalMinutes {
		critMinutes = totalMinutes
	}

	m := float64(agents)
	wFloat := float64(totalMinutes)
	lCrit := float64(critMinutes)
	minMinutes := max(lCrit, wFloat/m)
	maxMinutes := lCrit + max(0.0, wFloat-lCrit)/m

	return GrahamBounds{
		TotalMinutes:          totalMinutes,
		CriticalPathMinutes:   critMinutes,
		GrahamMinMinutes:      minMinutes,
		GrahamMaxMinutes:      maxMinutes,
		VelocityMinutesPerDay: velocityPerDay,
		GrahamMinDays:         minMinutes / velocityPerDay,
		GrahamMaxDays:         maxMinutes / velocityPerDay,
	}
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
