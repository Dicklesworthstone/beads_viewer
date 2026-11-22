package analysis

import (
	"fmt"
	"math"
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

const (
	etaVelocityWindowDays = 30
	etaDefaultWeekDays    = 7.0
)

func defaultVelocityMinutesPerDay(medianMinutes int) float64 {
	if medianMinutes > 0 {
		return float64(medianMinutes) / etaDefaultWeekDays
	}
	return float64(DefaultEstimatedMinutes) / etaDefaultWeekDays
}

// ETAContext caches dataset-wide median estimated minutes and 30-day global
// and per-label closure velocity buckets so interactive callers (such as the
// TUI Detail Pane and Graph View) can forecast individual issues in
// O(1 + len(issue.Labels)) per selection instead of rescanning all N issues.
type ETAContext struct {
	stats         *GraphStats
	now           time.Time
	medianMinutes int
	globalVel     float64
	globalSamples int
	labelVel      map[string]float64
	labelSamples  map[string]int
}

// NewETAContext precomputes dataset-level ETA statistics once for a snapshot.
func NewETAContext(issues []model.Issue, stats *GraphStats, now time.Time) *ETAContext {
	medianMinutes := computeMedianEstimatedMinutes(issues)
	since := now.Add(-time.Duration(etaVelocityWindowDays) * 24 * time.Hour)
	labelTotals := make(map[string]int)
	labelSamples := make(map[string]int)
	globalTotal := 0
	globalSamples := 0

	for _, iss := range issues {
		closedAt, ok := resolveClosureTime(&iss, now)
		if !ok || closedAt.Before(since) {
			continue
		}
		minutes, _ := estimateComplexityMinutes(iss, stats, medianMinutes)
		globalTotal += minutes
		globalSamples++
		seen := make(map[string]bool, len(iss.Labels))
		for _, lbl := range iss.Labels {
			norm := strings.ToLower(lbl)
			if norm == "" || seen[norm] {
				continue
			}
			seen[norm] = true
			labelTotals[norm] += minutes
			labelSamples[norm]++
		}
	}

	globalVel := 0.0
	if globalSamples > 0 {
		globalVel = float64(globalTotal) / float64(etaVelocityWindowDays)
	}
	labelVel := make(map[string]float64, len(labelTotals))
	for norm, total := range labelTotals {
		if labelSamples[norm] > 0 {
			labelVel[norm] = float64(total) / float64(etaVelocityWindowDays)
		}
	}

	return &ETAContext{
		stats:         stats,
		now:           now,
		medianMinutes: medianMinutes,
		globalVel:     globalVel,
		globalSamples: globalSamples,
		labelVel:      labelVel,
		labelSamples:  labelSamples,
	}
}

// EstimateForIssue computes an ETAEstimate matching EstimateETAForIssue in
// O(1 + len(issue.Labels)) time using precomputed dataset velocity buckets.
func (c *ETAContext) EstimateForIssue(issue model.Issue, agents int) ETAEstimate {
	if c == nil {
		return ETAEstimate{}
	}
	if agents <= 0 {
		agents = 1
	}

	complexityMinutes, complexityFactors := estimateComplexityMinutes(issue, c.stats, c.medianMinutes)

	velocityPerDay := c.globalVel
	velocitySamples := c.globalSamples
	velocityFactors := []string{fmt.Sprintf("velocity: global (%d samples/30d)", c.globalSamples)}

	if len(issue.Labels) > 0 {
		bestLabel := ""
		bestV := 0.0
		bestN := 0
		for _, label := range issue.Labels {
			norm := strings.ToLower(label)
			n := c.labelSamples[norm]
			rawV := c.labelVel[norm]
			if n == 0 || rawV <= 0 {
				continue
			}
			priorV := rawV
			if c.globalSamples > 0 && c.globalVel > 0 {
				priorV = c.globalVel
			}
			nf := float64(n)
			shrunkV := (nf*rawV + buehlmannStraubPriorWeight*priorV) / (nf + buehlmannStraubPriorWeight)
			if bestV == 0 || shrunkV < bestV || (shrunkV == bestV && norm < strings.ToLower(bestLabel)) {
				bestLabel = label
				bestV = shrunkV
				bestN = n
			}
		}
		if bestV > 0 {
			velocityPerDay = bestV
			velocitySamples = bestN
			velocityFactors = []string{fmt.Sprintf("velocity: label=%s (%.0f min/day, %d samples/30d)", bestLabel, bestV, bestN)}
		}
	}

	if velocityPerDay <= 0 {
		velocityPerDay = defaultVelocityMinutesPerDay(c.medianMinutes)
		velocityFactors = append(velocityFactors, "velocity: no recent closures; using default")
	}

	capacityPerDay := velocityPerDay * float64(agents)
	estimatedDays := float64(complexityMinutes) / capacityPerDay
	if estimatedDays < 0 {
		estimatedDays = 0
	}

	confidence := estimateETAConfidence(issue, velocitySamples)
	deltaDays := max(0.5, estimatedDays*(1.0-confidence)*0.8)

	eta := c.now.Add(durationDays(estimatedDays))
	etaLow := c.now.Add(durationDays(max(0.0, estimatedDays-deltaDays)))
	etaHigh := c.now.Add(durationDays(estimatedDays + deltaDays))

	factors := append([]string{}, complexityFactors...)
	factors = append(factors, velocityFactors...)
	factors = append(factors, fmt.Sprintf("agents: %d", agents))
	if len(factors) > 8 {
		factors = factors[:8]
	}

	return ETAEstimate{
		IssueID:               issue.ID,
		EstimatedMinutes:      complexityMinutes,
		EstimatedDays:         estimatedDays,
		ETADate:               eta,
		ETADateLow:            etaLow,
		ETADateHigh:           etaHigh,
		Confidence:            confidence,
		VelocityMinutesPerDay: velocityPerDay,
		Agents:                agents,
		Factors:               factors,
	}
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
		// Conservative calendar-day default: one median-sized issue per 7-day calendar week,
		// matching the 30-calendar-day closure window and 24h durationDays conversion.
		velocityPerDay = defaultVelocityMinutesPerDay(medianMinutes)
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

	globalV, globalN := velocityMinutesPerDayForLabelWithStats(issues, stats, "", since, now, medianMinutes)
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
		if label == "" {
			continue
		}
		rawV, n := velocityMinutesPerDayForLabelWithStats(issues, stats, label, since, now, medianMinutes)
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
	now := since.Add(time.Duration(etaVelocityWindowDays) * 24 * time.Hour)
	return velocityMinutesPerDayForLabelWithStats(issues, nil, label, since, now, medianMinutes)
}

func velocityMinutesPerDayForLabelWithStats(issues []model.Issue, stats *GraphStats, label string, since, now time.Time, medianMinutes int) (float64, int) {
	total := 0
	samples := 0

	for _, iss := range issues {
		closedAt, ok := resolveClosureTime(&iss, now)
		if !ok || closedAt.Before(since) {
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
	return float64(total) / float64(etaVelocityWindowDays), samples
}

// GrahamBounds captures Graham (1969) parallel list-scheduling makespan bounds:
//
//	T_min(m) = max(L_crit, W / m)
//	T_max(m) = L_crit + max(0, W - L_crit) / m
//
// expressed both in raw work-minutes and in velocity-calibrated days.
type GrahamBounds struct {
	TotalMinutes          int      `json:"total_minutes"`
	CriticalPathMinutes   int      `json:"critical_path_minutes"`
	CriticalPathIDs       []string `json:"-"`
	GrahamMinMinutes      float64  `json:"graham_min_minutes"`
	GrahamMaxMinutes      float64  `json:"graham_max_minutes"`
	VelocityMinutesPerDay float64  `json:"velocity_minutes_per_day"`
	GrahamMinDays         float64  `json:"graham_min_days"`
	GrahamMaxDays         float64  `json:"graham_max_days"`
}

// ComputeGrahamBounds computes Graham (1969) lower and upper makespan bounds for
// executing openIssues across agents parallel workers, calibrated against the
// project's effective closure velocity over allIssues.
func ComputeGrahamBounds(openIssues []model.Issue, allIssues []model.Issue, stats *GraphStats, agents int, now time.Time) GrahamBounds {
	if agents <= 0 {
		agents = 1
	}
	medianMinutes := computeMedianEstimatedMinutes(allIssues)
	since := now.Add(-time.Duration(etaVelocityWindowDays) * 24 * time.Hour)
	velocityPerDay, _ := velocityMinutesPerDayForLabelWithStats(allIssues, stats, "", since, now, medianMinutes)
	if velocityPerDay <= 0 {
		velocityPerDay = defaultVelocityMinutesPerDay(medianMinutes)
	}

	if len(openIssues) == 0 {
		return GrahamBounds{VelocityMinutesPerDay: velocityPerDay}
	}

	weights := make(map[string]int, len(openIssues))
	totalMinutes := 0
	for _, iss := range openIssues {
		w, _ := estimateComplexityMinutes(iss, stats, medianMinutes)
		weights[iss.ID] = w
		totalMinutes += w
	}

	critPath, critMinutes := longestWeightedChainWithContext(openIssues, allIssues, weights)
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
		CriticalPathIDs:       critPath,
		GrahamMinMinutes:      minMinutes,
		GrahamMaxMinutes:      maxMinutes,
		VelocityMinutesPerDay: velocityPerDay,
		GrahamMinDays:         minMinutes / velocityPerDay,
		GrahamMaxDays:         maxMinutes / velocityPerDay,
	}
}

// LongestWeightedChain returns the highest work-minute simple blocking chain
// across openIssues (ignoring self-loops) using Tarjan strongly-connected-component
// condensation followed by reverse-topological DAG dynamic programming.
// Acyclic diamonds run in strict O(V + E) time without exponential backtracking,
// and disconnected cyclic components are evaluated symmetrically alongside roots.
// Direct DepParentChild edges remain non-blocking, while blocking dependencies
// declared on a DepParentChild ancestor are inherited by descendants to match
// model.ReadinessIndex serial execution constraints.
func LongestWeightedChain(openIssues []model.Issue, weights map[string]int) ([]string, int) {
	return longestWeightedChainWithContext(openIssues, openIssues, weights)
}

func longestWeightedChainWithContext(openIssues []model.Issue, contextIssues []model.Issue, weights map[string]int) ([]string, int) {
	if len(openIssues) == 0 {
		return []string{}, 0
	}
	openSet := make(map[string]bool, len(openIssues))
	for _, iss := range openIssues {
		if iss.ID != "" {
			openSet[iss.ID] = true
		}
	}
	ids := make([]string, 0, len(openSet))
	for id := range openSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return []string{}, 0
	}

	// Build ancestor lookup over contextIssues (falling back to openIssues) so
	// children inherit unsatisfied open blockers declared on parent-child ancestors.
	issueByID := make(map[string]model.Issue, len(openIssues)+len(contextIssues))
	for _, iss := range contextIssues {
		if iss.ID != "" {
			issueByID[iss.ID] = iss
		}
	}
	for _, iss := range openIssues {
		if iss.ID != "" {
			issueByID[iss.ID] = iss
		}
	}

	dependents := make(map[string][]string, len(ids))
	for _, iss := range openIssues {
		if !openSet[iss.ID] {
			continue
		}
		seenBlocker := make(map[string]bool)
		addBlocker := func(blockerID string) {
			if blockerID == "" || blockerID == iss.ID || seenBlocker[blockerID] || !openSet[blockerID] {
				return
			}
			seenBlocker[blockerID] = true
			dependents[blockerID] = append(dependents[blockerID], iss.ID)
		}

		// Direct blocking dependencies
		var parentQueue []string
		for _, dep := range iss.Dependencies {
			if dep == nil {
				continue
			}
			if dep.Type.IsBlocking() {
				addBlocker(dep.DependsOnID)
			} else if dep.Type == model.DepParentChild && dep.DependsOnID != "" && dep.DependsOnID != iss.ID {
				parentQueue = append(parentQueue, dep.DependsOnID)
			}
		}

		// Inherited blocking dependencies from DepParentChild ancestors
		visitedAnc := map[string]bool{iss.ID: true}
		for len(parentQueue) > 0 {
			ancID := parentQueue[0]
			parentQueue = parentQueue[1:]
			if visitedAnc[ancID] {
				continue
			}
			visitedAnc[ancID] = true
			anc, ok := issueByID[ancID]
			if !ok || isClosedLikeStatus(anc.Status) {
				continue
			}
			for _, dep := range anc.Dependencies {
				if dep == nil {
					continue
				}
				if dep.Type.IsBlocking() {
					addBlocker(dep.DependsOnID)
				} else if dep.Type == model.DepParentChild && dep.DependsOnID != "" && !visitedAnc[dep.DependsOnID] {
					parentQueue = append(parentQueue, dep.DependsOnID)
				}
			}
		}
	}
	for k := range dependents {
		sort.Strings(dependents[k])
	}

	// Tarjan SCC emits components in reverse topological order (sink SCCs first).
	index := 0
	indices := make(map[string]int, len(ids))
	lowlink := make(map[string]int, len(ids))
	onStack := make(map[string]bool, len(ids))
	stack := make([]string, 0, len(ids))
	sccOf := make(map[string]int, len(ids))
	var sccs [][]string

	var strongConnect func(v string)
	strongConnect = func(v string) {
		index++
		indices[v] = index
		lowlink[v] = index
		stack = append(stack, v)
		onStack[v] = true

		for _, w := range dependents[v] {
			if indices[w] == 0 {
				strongConnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] && indices[w] < lowlink[v] {
				lowlink[v] = indices[w]
			}
		}

		if lowlink[v] == indices[v] {
			cid := len(sccs)
			var comp []string
			for {
				n := len(stack) - 1
				w := stack[n]
				stack = stack[:n]
				onStack[w] = false
				sccOf[w] = cid
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sort.Strings(comp)
			sccs = append(sccs, comp)
		}
	}

	for _, id := range ids {
		if indices[id] == 0 {
			strongConnect(id)
		}
	}

	bestMinFrom := make(map[string]int, len(ids))
	bestPathFrom := make(map[string][]string, len(ids))

	for cid, comp := range sccs {
		// Precompute best downstream cross-SCC exit from each node in this SCC.
		extMin := make(map[string]int, len(comp))
		extPath := make(map[string][]string, len(comp))
		for _, u := range comp {
			bestW := 0
			var bestP []string
			for _, v := range dependents[u] {
				if sccOf[v] == cid {
					continue
				}
				candW := bestMinFrom[v]
				candP := bestPathFrom[v]
				if candW > bestW || (candW == bestW && len(candP) > len(bestP)) {
					bestW = candW
					bestP = candP
				}
			}
			extMin[u] = bestW
			extPath[u] = bestP
		}

		if len(comp) == 1 {
			u := comp[0]
			bestMinFrom[u] = weights[u] + extMin[u]
			path := make([]string, 1+len(extPath[u]))
			path[0] = u
			copy(path[1:], extPath[u])
			bestPathFrom[u] = path
			continue
		}

		// Non-trivial cyclic SCC: bounded intra-SCC simple-path search per entry node.
		for _, start := range comp {
			visited := make(map[string]bool, len(comp))
			budget := 512
			bestTotal := -1
			var bestSeq []string

			var dfsInSCC func(u string, cur []string, sum int)
			dfsInSCC = func(u string, cur []string, sum int) {
				if budget <= 0 {
					return
				}
				budget--
				visited[u] = true
				cur = append(cur, u)
				sum += weights[u]

				candTotal := sum + extMin[u]
				candLen := len(cur) + len(extPath[u])
				if candTotal > bestTotal || (candTotal == bestTotal && candLen > len(bestSeq)) {
					bestTotal = candTotal
					seq := make([]string, candLen)
					copy(seq, cur)
					copy(seq[len(cur):], extPath[u])
					bestSeq = seq
				}

				for _, next := range dependents[u] {
					if sccOf[next] != cid || visited[next] {
						continue
					}
					dfsInSCC(next, cur, sum)
				}
				visited[u] = false
			}

			dfsInSCC(start, nil, 0)
			if bestTotal < 0 {
				bestTotal = weights[start]
				bestSeq = []string{start}
			}
			bestMinFrom[start] = bestTotal
			bestPathFrom[start] = bestSeq
		}
	}

	globalBestMin := -1
	var globalBestPath []string
	for _, id := range ids {
		w := bestMinFrom[id]
		p := bestPathFrom[id]
		if w > globalBestMin || (w == globalBestMin && len(p) > len(globalBestPath)) {
			globalBestMin = w
			globalBestPath = p
		}
	}
	if globalBestMin < 0 {
		return []string{}, 0
	}
	return globalBestPath, globalBestMin
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
	if math.IsNaN(days) || days <= 0 {
		return 0
	}
	const maxDurationDays = float64(math.MaxInt64) / float64(24*time.Hour)
	if math.IsInf(days, 1) || days >= maxDurationDays {
		return time.Duration(math.MaxInt64)
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
