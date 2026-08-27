package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"
)

// ============================================================================
// Label Health Types (bv-100)
// Foundation for all label-centric analysis features
// ============================================================================

// LabelHealth represents the overall health assessment of a single label
// Health is a composite score based on velocity, freshness, flow, and criticality
type LabelHealth struct {
	Label       string             `json:"label"`            // The label name
	IssueCount  int                `json:"issue_count"`      // Total issues with this label
	OpenCount   int                `json:"open_count"`       // Open issues with this label
	ClosedCount int                `json:"closed_count"`     // Closed issues with this label
	Blocked     int                `json:"blocked_count"`    // Blocked issues with this label
	Health      int                `json:"health"`           // Composite health score 0-100
	HealthLevel string             `json:"health_level"`     // "healthy", "warning", "critical"
	Velocity    VelocityMetrics    `json:"velocity"`         // Work completion rate
	Freshness   FreshnessMetrics   `json:"freshness"`        // How recently updated
	Flow        FlowMetrics        `json:"flow"`             // Cross-label dependencies
	Criticality CriticalityMetrics `json:"criticality"`      // Graph-based importance
	Issues      []string           `json:"issues,omitempty"` // Issue IDs with this label
}

// VelocityMetrics tracks the rate of work completion for a label
type VelocityMetrics struct {
	ClosedLast7Days  int     `json:"closed_last_7_days"`  // Issues closed in past week
	ClosedLast30Days int     `json:"closed_last_30_days"` // Issues closed in past month
	AvgDaysToClose   float64 `json:"avg_days_to_close"`   // Average time from open to close
	TrendDirection   string  `json:"trend_direction"`     // "improving", "stable", "declining"
	TrendPercent     float64 `json:"trend_percent"`       // Percent change vs prior period
	VelocityScore    int     `json:"velocity_score"`      // Normalized 0-100 score
}

// HistoricalVelocity captures velocity data across multiple time periods (bv-123)
type HistoricalVelocity struct {
	Label            string           `json:"label"`                       // Label this data is for
	WeeklyVelocity   []WeeklySnapshot `json:"weekly_velocity"`             // Per-week closure counts
	WeeksAnalyzed    int              `json:"weeks_analyzed"`              // Number of weeks with data
	MovingAvg4Week   float64          `json:"moving_avg_4_week"`           // 4-week moving average
	MovingAvg8Week   float64          `json:"moving_avg_8_week,omitempty"` // 8-week moving average (if data available)
	PeakWeek         int              `json:"peak_week"`                   // Index of highest velocity week
	PeakVelocity     int              `json:"peak_velocity"`               // Highest weekly closure count
	TroughWeek       int              `json:"trough_week"`                 // Index of lowest velocity week (with >0)
	TroughVelocity   int              `json:"trough_velocity"`             // Lowest weekly closure count (with >0)
	Variance         float64          `json:"variance"`                    // Statistical variance in velocity
	ConsistencyScore int              `json:"consistency_score"`           // 0-100, higher = more consistent output
}

// WeeklySnapshot captures closure data for a single week
type WeeklySnapshot struct {
	WeekStart  time.Time `json:"week_start"` // Start of the week (Monday)
	WeekEnd    time.Time `json:"week_end"`   // End of the week (Sunday)
	Closed     int       `json:"closed"`     // Issues closed this week
	WeeksAgo   int       `json:"weeks_ago"`  // 0 = current week, 1 = last week, etc.
	IssueIDs   []string  `json:"issue_ids,omitempty"`
	Cumulative int       `json:"cumulative"` // Running total up to this week
}

// FreshnessMetrics tracks how recently issues in a label have been updated
type FreshnessMetrics struct {
	MostRecentUpdate   time.Time `json:"most_recent_update"`    // When was any issue last updated
	OldestOpenIssue    time.Time `json:"oldest_open_issue"`     // Created date of oldest open issue
	AvgDaysSinceUpdate float64   `json:"avg_days_since_update"` // Average staleness
	StaleCount         int       `json:"stale_count"`           // Issues with no updates > threshold
	StaleThresholdDays int       `json:"stale_threshold_days"`  // What we consider stale (default 14)
	FreshnessScore     int       `json:"freshness_score"`       // Normalized 0-100 score (higher = fresher)
}

// FlowMetrics captures cross-label dependency relationships
type FlowMetrics struct {
	IncomingDeps      int      `json:"incoming_deps"`       // Other labels blocking this one
	OutgoingDeps      int      `json:"outgoing_deps"`       // Labels this one blocks
	IncomingLabels    []string `json:"incoming_labels"`     // Which labels block this one
	OutgoingLabels    []string `json:"outgoing_labels"`     // Which labels this one blocks
	BlockedByExternal int      `json:"blocked_by_external"` // Issues blocked by other labels
	BlockingExternal  int      `json:"blocking_external"`   // Issues blocking other labels
	FlowScore         int      `json:"flow_score"`          // 0-100, higher = better flow (less blocked)
}

// CriticalityMetrics measures the importance of a label in the dependency graph
type CriticalityMetrics struct {
	AvgPageRank       float64 `json:"avg_pagerank"`        // Average PageRank of issues in label
	AvgBetweenness    float64 `json:"avg_betweenness"`     // Average betweenness centrality
	MaxBetweenness    float64 `json:"max_betweenness"`     // Highest betweenness (bottleneck indicator)
	CriticalPathCount int     `json:"critical_path_count"` // Issues on critical path
	BottleneckCount   int     `json:"bottleneck_count"`    // Issues identified as bottlenecks
	CriticalityScore  int     `json:"criticality_score"`   // 0-100, higher = more critical
}

// LabelDependency represents a dependency relationship between two labels
type LabelDependency struct {
	FromLabel     string         `json:"from_label"`               // Blocking label
	ToLabel       string         `json:"to_label"`                 // Blocked label
	IssueCount    int            `json:"issue_count"`              // Number of cross-label dependencies
	IssueIDs      []string       `json:"issue_ids,omitempty"`      // Specific issue pairs
	BlockingPairs []BlockingPair `json:"blocking_pairs,omitempty"` // Individual blocking relationships
}

// BlockingPair represents a single issue blocking another across labels
type BlockingPair struct {
	BlockerID    string `json:"blocker_id"`    // Issue doing the blocking
	BlockedID    string `json:"blocked_id"`    // Issue being blocked
	BlockerLabel string `json:"blocker_label"` // Label of blocker
	BlockedLabel string `json:"blocked_label"` // Label of blocked
}

// CrossLabelFlow captures the complete flow of work between labels
type CrossLabelFlow struct {
	Labels              []string          `json:"labels"`                 // All labels in analysis
	FlowMatrix          [][]int           `json:"flow_matrix"`            // [from][to] dependency counts
	Dependencies        []LabelDependency `json:"dependencies"`           // Detailed dependency list
	CriticalPaths       []LabelPath       `json:"critical_paths"`         // Label-level critical paths
	BottleneckLabels    []string          `json:"bottleneck_labels"`      // Labels causing most blockage
	TotalCrossLabelDeps int               `json:"total_cross_label_deps"` // Total inter-label dependencies
}

// ============================================================================
// Blockage Impact Cascade (bv-112)
// Compute transitive downstream impact of blocked labels
// ============================================================================

// BlockageCascadeResult shows the transitive downstream impact of blocked issues
type BlockageCascadeResult struct {
	SourceLabel     string                  `json:"source_label"`    // The label with blocked issues
	BlockedCount    int                     `json:"blocked_count"`   // Number of blocked issues in source
	CascadeLevels   []CascadeLevel          `json:"cascade_levels"`  // Downstream impact by depth
	TotalImpact     int                     `json:"total_impact"`    // Total downstream issues affected
	AffectedLabels  []string                `json:"affected_labels"` // All labels in the cascade
	Recommendations []UnblockRecommendation `json:"recommendations"` // What to unblock first
}

// CascadeLevel represents one depth level in the cascade tree
type CascadeLevel struct {
	Level         int                 `json:"level"`          // Depth (1 = direct, 2 = indirect, etc.)
	Labels        []LabelCascadeEntry `json:"labels"`         // Labels affected at this level
	TotalAffected int                 `json:"total_affected"` // Total issues at this level
}

// LabelCascadeEntry shows a label's impact in the cascade
type LabelCascadeEntry struct {
	Label        string   `json:"label"`
	WaitingCount int      `json:"waiting_count"` // Issues waiting due to cascade
	FromLabels   []string `json:"from_labels"`   // Which labels are blocking this one
}

// UnblockRecommendation suggests which issue to unblock for maximum impact
type UnblockRecommendation struct {
	IssueID       string `json:"issue_id"`
	IssueTitle    string `json:"issue_title,omitempty"`
	Label         string `json:"label"`
	UnblocksCount int    `json:"unblocks_count"` // How many issues this unblocks
	CascadeDepth  int    `json:"cascade_depth"`  // How deep the cascade goes
	Reason        string `json:"reason"`
}

// BlockageCascadeAnalysis holds all cascade results for a project
type BlockageCascadeAnalysis struct {
	GeneratedAt   time.Time               `json:"generated_at"`
	TotalBlocked  int                     `json:"total_blocked"`  // Total blocked issues
	Cascades      []BlockageCascadeResult `json:"cascades"`       // Per-source-label cascades
	TopUnblocks   []UnblockRecommendation `json:"top_unblocks"`   // Global top recommendations
	CriticalChain []string                `json:"critical_chain"` // Longest cascade chain (labels)
}

// LabelPath represents a sequence of labels in a dependency chain
type LabelPath struct {
	Labels      []string `json:"labels"`       // Ordered sequence of labels
	Length      int      `json:"length"`       // Number of label transitions
	IssueCount  int      `json:"issue_count"`  // Total issues in this path
	TotalWeight float64  `json:"total_weight"` // Sum of dependency weights
}

// LabelSummary provides a quick overview for display
type LabelSummary struct {
	Label          string `json:"label"`
	IssueCount     int    `json:"issue_count"`
	OpenCount      int    `json:"open_count"`
	Health         int    `json:"health"`              // 0-100
	HealthLevel    string `json:"health_level"`        // "healthy", "warning", "critical"
	TopIssue       string `json:"top_issue,omitempty"` // Highest priority open issue
	NeedsAttention bool   `json:"needs_attention"`     // Flag for labels requiring action
}

// LabelAnalysisResult is the top-level result for label analysis
type LabelAnalysisResult struct {
	GeneratedAt     time.Time       `json:"generated_at"`
	TotalLabels     int             `json:"total_labels"`
	HealthyCount    int             `json:"healthy_count"`              // Labels with health >= 70
	WarningCount    int             `json:"warning_count"`              // Labels with health 40-69
	CriticalCount   int             `json:"critical_count"`             // Labels with health < 40
	Labels          []LabelHealth   `json:"labels"`                     // Detailed per-label health
	Summaries       []LabelSummary  `json:"summaries"`                  // Quick overview list
	CrossLabelFlow  *CrossLabelFlow `json:"cross_label_flow,omitempty"` // Inter-label analysis
	AttentionNeeded []string        `json:"attention_needed"`           // Labels requiring attention
}

// ComputeCrossLabelFlow analyzes blocking dependencies between labels and returns counts.
// It respects cfg.IncludeClosedInFlow: when false, closed issues are ignored.
func ComputeCrossLabelFlow(issues []model.Issue, cfg LabelHealthConfig) CrossLabelFlow {
	labels := ExtractLabels(issues)
	labelList := make([]string, len(labels.Labels))
	copy(labelList, labels.Labels)
	sort.Strings(labelList)

	n := len(labelList)
	matrix := make([][]int, n)
	for i := range matrix {
		matrix[i] = make([]int, n)
	}

	index := make(map[string]int, n)
	for i, l := range labelList {
		index[l] = i
	}

	// Build issue map for lookup
	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	// Dependency aggregation
	type pairKey struct{ from, to string }
	depMap := make(map[pairKey]*LabelDependency)
	totalDeps := 0

	for _, blocked := range issues {
		if !cfg.IncludeClosedInFlow && isClosedLikeStatus(blocked.Status) {
			continue
		}
		for _, dep := range blocked.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blocker, ok := issueMap[dep.DependsOnID]
			if !ok {
				continue
			}
			if !cfg.IncludeClosedInFlow && isClosedLikeStatus(blocker.Status) {
				continue
			}
			// Cross-product of labels
			for _, from := range blocker.Labels {
				for _, to := range blocked.Labels {
					if from == "" || to == "" || from == to {
						continue // skip empty/self
					}
					iFrom, okFrom := index[from]
					iTo, okTo := index[to]
					if !okFrom || !okTo {
						continue
					}
					matrix[iFrom][iTo]++
					totalDeps++
					key := pairKey{from: from, to: to}
					entry, exists := depMap[key]
					if !exists {
						entry = &LabelDependency{
							FromLabel: key.from,
							ToLabel:   key.to,
							IssueIDs:  []string{},
						}
						depMap[key] = entry
					}
					entry.IssueCount++
					entry.IssueIDs = append(entry.IssueIDs, blocked.ID)
					entry.BlockingPairs = append(entry.BlockingPairs, BlockingPair{
						BlockerID:    blocker.ID,
						BlockedID:    blocked.ID,
						BlockerLabel: from,
						BlockedLabel: to,
					})
				}
			}
		}
	}

	// Build dependency list deterministically
	deps := make([]LabelDependency, 0, len(depMap))
	for _, d := range depMap {
		deps = append(deps, *d)
	}
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].FromLabel != deps[j].FromLabel {
			return deps[i].FromLabel < deps[j].FromLabel
		}
		if deps[i].ToLabel != deps[j].ToLabel {
			return deps[i].ToLabel < deps[j].ToLabel
		}
		return deps[i].IssueCount > deps[j].IssueCount
	})

	// Bottleneck labels: highest outgoing deps
	outCounts := make(map[string]int, n)
	maxOut := 0
	for i, row := range matrix {
		sum := 0
		for _, v := range row {
			sum += v
		}
		outCounts[labelList[i]] = sum
		if sum > maxOut {
			maxOut = sum
		}
	}
	bottlenecks := make([]string, 0)
	for label, c := range outCounts {
		if c == maxOut && c > 0 {
			bottlenecks = append(bottlenecks, label)
		}
	}
	sort.Strings(bottlenecks)

	return CrossLabelFlow{
		Labels:              labelList,
		FlowMatrix:          matrix,
		Dependencies:        deps,
		CriticalPaths:       []LabelPath{},
		BottleneckLabels:    bottlenecks,
		TotalCrossLabelDeps: totalDeps,
	}
}

// ComputeVelocityMetrics calculates simple velocity stats for a label.
// It looks at closed issues and recent closures to give a quick pulse.
func ComputeVelocityMetrics(issues []model.Issue, now time.Time) VelocityMetrics {
	const day = 24 * time.Hour
	var closed7, closed30 int
	var totalCloseDur time.Duration
	var closeSamples int

	// Rolling windows
	weekAgo := now.Add(-7 * day)
	monthAgo := now.Add(-30 * day)
	prevWeekStart := now.Add(-14 * day)

	var prevWeek, currentWeek int

	for _, iss := range issues {
		if !isClosedLikeStatus(iss.Status) {
			continue
		}
		if iss.ClosedAt == nil {
			continue
		}
		closedAt := *iss.ClosedAt
		if closedAt.After(weekAgo) {
			closed7++
		}
		if closedAt.After(monthAgo) {
			closed30++
		}
		if closedAt.After(prevWeekStart) && closedAt.Before(weekAgo) {
			prevWeek++
		} else if closedAt.After(weekAgo) {
			currentWeek++
		}
		if !iss.CreatedAt.IsZero() {
			totalCloseDur += closedAt.Sub(iss.CreatedAt)
			closeSamples++
		}
	}

	avgDays := 0.0
	if closeSamples > 0 {
		avgDays = totalCloseDur.Hours() / 24.0 / float64(closeSamples)
	}

	trendPercent := 0.0
	trendDir := "stable"
	if prevWeek > 0 {
		trendPercent = (float64(currentWeek-prevWeek) / float64(prevWeek)) * 100
		switch {
		case trendPercent > 10:
			trendDir = "improving"
		case trendPercent < -10:
			trendDir = "declining"
		}
	} else if currentWeek > 0 {
		trendDir = "improving"
		trendPercent = 100
	}

	// Simple score: closed in last month scaled plus recency bonus
	velocityScore := 0
	if closed30 > 0 {
		velocityScore = int(min(100.0, float64(closed30)*10))
	}
	// Bonus if trend improving
	if trendDir == "improving" && velocityScore < 100 {
		velocityScore = clampScore(velocityScore + 10)
	}

	return VelocityMetrics{
		ClosedLast7Days:  closed7,
		ClosedLast30Days: closed30,
		AvgDaysToClose:   avgDays,
		TrendDirection:   trendDir,
		TrendPercent:     trendPercent,
		VelocityScore:    velocityScore,
	}
}

// ComputeFreshnessMetrics calculates freshness and staleness for a label.
func ComputeFreshnessMetrics(issues []model.Issue, now time.Time, staleDays int) FreshnessMetrics {
	if staleDays <= 0 {
		staleDays = DefaultStaleThresholdDays
	}
	var mostRecent time.Time
	var oldestOpen time.Time
	var totalStaleness float64
	var count int
	staleCount := 0
	threshold := float64(staleDays)

	for _, iss := range issues {
		if iss.UpdatedAt.After(mostRecent) {
			mostRecent = iss.UpdatedAt
		}
		if !isClosedLikeStatus(iss.Status) {
			// Only consider issues with valid CreatedAt for oldest calculation
			if !iss.CreatedAt.IsZero() && (oldestOpen.IsZero() || iss.CreatedAt.Before(oldestOpen)) {
				oldestOpen = iss.CreatedAt
			}
		}
		if !iss.UpdatedAt.IsZero() {
			days := now.Sub(iss.UpdatedAt).Hours() / 24.0
			totalStaleness += days
			count++
			if days >= threshold {
				staleCount++
			}
		}
	}

	avgStaleness := 0.0
	if count > 0 {
		avgStaleness = totalStaleness / float64(count)
	}
	// Freshness score: 100 when avg=0, declines linearly to 0 at 2x threshold
	freshnessScore := int(max(0.0, 100-(avgStaleness/(threshold*2))*100))

	return FreshnessMetrics{
		MostRecentUpdate:   mostRecent,
		OldestOpenIssue:    oldestOpen,
		AvgDaysSinceUpdate: avgStaleness,
		StaleCount:         staleCount,
		StaleThresholdDays: staleDays,
		FreshnessScore:     clampScore(freshnessScore),
	}
}

// ComputeLabelHealthForLabel computes health for a single label.
// If stats is nil, it will compute graph stats once for the provided issues.
func ComputeLabelHealthForLabel(label string, issues []model.Issue, cfg LabelHealthConfig, now time.Time, stats *GraphStats) LabelHealth {
	health := NewLabelHealth(label)
	health.Issues = []string{}

	// Collect issues with this label
	var labeled []model.Issue
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l == label {
				labeled = append(labeled, iss)
				health.Issues = append(health.Issues, iss.ID)
				break
			}
		}
	}

	health.IssueCount = len(labeled)
	if health.IssueCount == 0 {
		health.Health = 0
		health.HealthLevel = HealthLevelCritical
		return health
	}

	// Status counts
	for _, iss := range labeled {
		switch iss.Status {
		case model.StatusClosed, model.StatusTombstone:
			health.ClosedCount++
		case model.StatusInProgress:
			health.OpenCount++
		case model.StatusBlocked:
			health.Blocked++
		default:
			health.OpenCount++
		}
	}

	velocity := ComputeVelocityMetrics(labeled, now)
	freshness := ComputeFreshnessMetrics(labeled, now, cfg.StaleThresholdDays)

	// Flow: count cross-label deps
	flow := FlowMetrics{}
	seenIn := make(map[string]struct{})
	seenOut := make(map[string]struct{})
	labeledSet := make(map[string]bool, len(labeled))
	for _, iss := range labeled {
		labeledSet[iss.ID] = true
	}
	// Incoming: other labels' issues block this label's issues
	for _, iss := range labeled {
		hasExternalBlocker := false
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blockerLabels := GetLabelsForIssue(issues, dep.DependsOnID)
			for _, bl := range blockerLabels {
				if bl != label {
					flow.IncomingDeps++
					seenIn[bl] = struct{}{}
					hasExternalBlocker = true
				}
			}
		}
		if hasExternalBlocker {
			flow.BlockedByExternal++
		}
	}
	// Outgoing: this label's issues block other labels' issues
	for _, other := range issues {
		if labeledSet[other.ID] {
			continue
		}
		for _, dep := range other.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if labeledSet[dep.DependsOnID] {
				otherLabels := other.Labels
				for _, ol := range otherLabels {
					if ol != label {
						flow.OutgoingDeps++
						seenOut[ol] = struct{}{}
					}
				}
				flow.BlockingExternal++
				break // count this other issue once
			}
		}
	}
	for l := range seenIn {
		flow.IncomingLabels = append(flow.IncomingLabels, l)
	}
	for l := range seenOut {
		flow.OutgoingLabels = append(flow.OutgoingLabels, l)
	}
	sort.Strings(flow.IncomingLabels)
	sort.Strings(flow.OutgoingLabels)
	flow.FlowScore = clampScore(100 - (flow.IncomingDeps * 5))

	// Criticality: derive from graph metrics (reuse precomputed stats when supplied)
	if stats == nil {
		analyzer := NewAnalyzer(issues)
		s := analyzer.Analyze()
		stats = &s
	}
	pr := stats.PageRank()
	bw := stats.Betweenness()
	maxPR := findMax(pr)
	maxBW := findMax(bw)

	var prSum, bwSum float64
	maxBwLabel := 0.0
	var critCount, bottleneckCount int
	for _, iss := range labeled {
		prSum += pr[iss.ID]
		bwVal := bw[iss.ID]
		bwSum += bwVal
		if bwVal > maxBwLabel {
			maxBwLabel = bwVal
		}
		if stats.GetCriticalPathScore(iss.ID) > 0 {
			critCount++
		}
		if bwVal > 0 {
			bottleneckCount++
		}
	}
	avgPR := 0.0
	avgBW := 0.0
	if health.IssueCount > 0 {
		avgPR = prSum / float64(health.IssueCount)
		avgBW = bwSum / float64(health.IssueCount)
	}
	critScore := 0
	if maxPR > 0 {
		critScore += int((avgPR / maxPR) * 50)
	}
	if maxBW > 0 {
		critScore += int((maxBwLabel / maxBW) * 50)
	}
	critScore = clampScore(critScore)

	health.Velocity = velocity
	health.Freshness = freshness
	health.Flow = flow
	health.Criticality = CriticalityMetrics{
		AvgPageRank:       avgPR,
		AvgBetweenness:    avgBW,
		MaxBetweenness:    maxBwLabel,
		CriticalPathCount: critCount,
		BottleneckCount:   bottleneckCount,
		CriticalityScore:  critScore,
	}

	health.Health = ComputeCompositeHealth(velocity.VelocityScore, freshness.FreshnessScore, flow.FlowScore, critScore, cfg)
	health.HealthLevel = HealthLevelFromScore(health.Health)
	return health
}

// ComputeAllLabelHealth computes health for all labels in the issue set.
func ComputeAllLabelHealth(issues []model.Issue, cfg LabelHealthConfig, now time.Time, stats *GraphStats) LabelAnalysisResult {
	labels := ExtractLabels(issues)
	result := LabelAnalysisResult{
		GeneratedAt:     now,
		TotalLabels:     labels.LabelCount,
		Labels:          []LabelHealth{},
		Summaries:       []LabelSummary{},
		AttentionNeeded: []string{},
	}

	// Deterministic traversal
	sort.Strings(labels.Labels)

	// Precompute stats once for efficiency if not provided
	var fullStats *GraphStats
	if stats != nil {
		fullStats = stats
	} else {
		analyzer := NewAnalyzer(issues)
		s := analyzer.Analyze()
		fullStats = &s
	}

	for _, label := range labels.Labels {
		health := ComputeLabelHealthForLabel(label, issues, cfg, now, fullStats)
		result.Labels = append(result.Labels, health)
		summary := LabelSummary{
			Label:          label,
			IssueCount:     health.IssueCount,
			OpenCount:      health.OpenCount,
			Health:         health.Health,
			HealthLevel:    health.HealthLevel,
			NeedsAttention: NeedsAttention(health),
		}
		if len(health.Issues) > 0 {
			summary.TopIssue = health.Issues[0]
		}
		result.Summaries = append(result.Summaries, summary)
		switch health.HealthLevel {
		case HealthLevelHealthy:
			result.HealthyCount++
		case HealthLevelWarning:
			result.WarningCount++
			result.AttentionNeeded = append(result.AttentionNeeded, label)
		case HealthLevelCritical:
			result.CriticalCount++
			result.AttentionNeeded = append(result.AttentionNeeded, label)
		}
	}

	sort.Slice(result.Summaries, func(i, j int) bool {
		if result.Summaries[i].Health != result.Summaries[j].Health {
			return result.Summaries[i].Health > result.Summaries[j].Health
		}
		return result.Summaries[i].Label < result.Summaries[j].Label
	})

	return result
}

func clampScore(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ============================================================================
// Health Score Constants and Thresholds
// ============================================================================

// HealthLevel constants for categorizing label health
const (
	HealthLevelHealthy  = "healthy"  // Health >= 70
	HealthLevelWarning  = "warning"  // Health 40-69
	HealthLevelCritical = "critical" // Health < 40
)

// Default thresholds for health calculations
const (
	DefaultStaleThresholdDays = 14   // Days without update to consider stale
	HealthyThreshold          = 70   // Min health score for "healthy"
	WarningThreshold          = 40   // Min health score for "warning"
	VelocityWeight            = 0.25 // Weight for velocity in composite score
	FreshnessWeight           = 0.25 // Weight for freshness in composite score
	FlowWeight                = 0.25 // Weight for flow in composite score
	CriticalityWeight         = 0.25 // Weight for criticality in composite score
)

// ============================================================================
// Configuration Types
// ============================================================================

// LabelHealthConfig configures label health computation
type LabelHealthConfig struct {
	StaleThresholdDays  int     `json:"stale_threshold_days"`   // Days to consider issue stale
	VelocityWeight      float64 `json:"velocity_weight"`        // Weight for velocity component
	FreshnessWeight     float64 `json:"freshness_weight"`       // Weight for freshness component
	FlowWeight          float64 `json:"flow_weight"`            // Weight for flow component
	CriticalityWeight   float64 `json:"criticality_weight"`     // Weight for criticality component
	MinIssuesForHealth  int     `json:"min_issues_for_health"`  // Min issues to compute health
	IncludeClosedInFlow bool    `json:"include_closed_in_flow"` // Include closed issues in flow analysis
}

// DefaultLabelHealthConfig returns sensible defaults
func DefaultLabelHealthConfig() LabelHealthConfig {
	return LabelHealthConfig{
		StaleThresholdDays:  DefaultStaleThresholdDays,
		VelocityWeight:      VelocityWeight,
		FreshnessWeight:     FreshnessWeight,
		FlowWeight:          FlowWeight,
		CriticalityWeight:   CriticalityWeight,
		MinIssuesForHealth:  1,
		IncludeClosedInFlow: false,
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

// HealthLevelFromScore returns the health level string for a score
func HealthLevelFromScore(score int) string {
	if score >= HealthyThreshold {
		return HealthLevelHealthy
	}
	if score >= WarningThreshold {
		return HealthLevelWarning
	}
	return HealthLevelCritical
}

// NeedsAttention returns true if a label needs attention based on health
func NeedsAttention(health LabelHealth) bool {
	return health.Health < HealthyThreshold
}

// ComputeCompositeHealth calculates the overall health score from components
func ComputeCompositeHealth(velocity, freshness, flow, criticality int, cfg LabelHealthConfig) int {
	weighted := float64(velocity)*cfg.VelocityWeight +
		float64(freshness)*cfg.FreshnessWeight +
		float64(flow)*cfg.FlowWeight +
		float64(criticality)*cfg.CriticalityWeight

	// Normalize to 0-100 and clamp
	return clampScore(int(weighted + 0.5))
}

// NewLabelHealth creates a new LabelHealth with default values
func NewLabelHealth(label string) LabelHealth {
	return LabelHealth{
		Label:       label,
		Health:      100, // Start healthy, reduce based on issues
		HealthLevel: HealthLevelHealthy,
		Velocity: VelocityMetrics{
			TrendDirection: "stable",
			VelocityScore:  100,
		},
		Freshness: FreshnessMetrics{
			StaleThresholdDays: DefaultStaleThresholdDays,
			FreshnessScore:     100,
		},
		Flow: FlowMetrics{
			FlowScore: 100,
		},
		Criticality: CriticalityMetrics{
			CriticalityScore: 50, // Neutral starting point
		},
	}
}

// ============================================================================
// Label Extraction (bv-101)
// Functions to extract and aggregate labels from issues
// ============================================================================

// LabelStats provides basic statistics about a label
type LabelStats struct {
	Label       string         `json:"label"`
	TotalCount  int            `json:"total_count"`  // Total issues with this label
	OpenCount   int            `json:"open_count"`   // Open issues
	ClosedCount int            `json:"closed_count"` // Closed issues
	InProgress  int            `json:"in_progress"`  // In-progress issues
	Blocked     int            `json:"blocked"`      // Issues blocked by dependencies
	ByPriority  map[int]int    `json:"by_priority"`  // Count by priority level
	ByType      map[string]int `json:"by_type"`      // Count by issue type
	IssueIDs    []string       `json:"issue_ids"`    // All issue IDs with this label
}

// LabelExtractionResult contains all extracted label data
type LabelExtractionResult struct {
	Labels         []string               `json:"labels"`          // Unique labels in sorted order
	LabelCount     int                    `json:"label_count"`     // Number of unique labels
	Stats          map[string]*LabelStats `json:"stats"`           // Per-label statistics
	IssueCount     int                    `json:"issue_count"`     // Total issues analyzed
	UnlabeledCount int                    `json:"unlabeled_count"` // Issues without labels
	TopLabels      []string               `json:"top_labels"`      // Labels sorted by issue count
}

// ExtractLabels extracts unique labels from a slice of issues with statistics
// Handles edge cases: nil issues, empty labels, duplicate labels
func ExtractLabels(issues []model.Issue) LabelExtractionResult {
	result := LabelExtractionResult{
		Stats:     make(map[string]*LabelStats),
		Labels:    []string{},
		TopLabels: []string{},
	}

	if len(issues) == 0 {
		return result
	}

	result.IssueCount = len(issues)
	labelSet := make(map[string]bool)

	for _, issue := range issues {
		// Track issues without labels
		if len(issue.Labels) == 0 {
			result.UnlabeledCount++
		}

		// Process each label on the issue
		for _, label := range issue.Labels {
			// Skip empty labels
			if label == "" {
				continue
			}

			// Track unique labels
			labelSet[label] = true

			// Initialize stats if needed
			stats, exists := result.Stats[label]
			if !exists {
				stats = &LabelStats{
					Label:      label,
					ByPriority: make(map[int]int),
					ByType:     make(map[string]int),
					IssueIDs:   []string{},
				}
				result.Stats[label] = stats
			}

			// Update counts
			stats.TotalCount++
			stats.IssueIDs = append(stats.IssueIDs, issue.ID)

			// Count by status
			switch issue.Status {
			case model.StatusClosed, model.StatusTombstone:
				stats.ClosedCount++
			case model.StatusInProgress:
				stats.InProgress++
			case model.StatusBlocked:
				stats.Blocked++
			default:
				// Open plus other non-closed statuses (deferred, draft, pinned, hooked, review)
				stats.OpenCount++
			}

			// Count by priority
			stats.ByPriority[issue.Priority]++

			// Count by type
			stats.ByType[string(issue.IssueType)]++
		}
	}

	// Build sorted label list
	for label := range labelSet {
		result.Labels = append(result.Labels, label)
	}
	sort.Strings(result.Labels)
	result.LabelCount = len(result.Labels)

	// Build top labels by issue count
	result.TopLabels = sortLabelsByCount(result.Stats)

	return result
}

// sortLabelsByCount returns labels sorted by total issue count (descending)
func sortLabelsByCount(stats map[string]*LabelStats) []string {
	type labelCount struct {
		label string
		count int
	}

	var lc []labelCount
	for label, s := range stats {
		lc = append(lc, labelCount{label: label, count: s.TotalCount})
	}

	sort.Slice(lc, func(i, j int) bool {
		if lc[i].count != lc[j].count {
			return lc[i].count > lc[j].count
		}
		return lc[i].label < lc[j].label // Alphabetical for ties
	})

	result := make([]string, len(lc))
	for i, l := range lc {
		result[i] = l.label
	}
	return result
}

// GetLabelIssues returns all issues that have a specific label
func GetLabelIssues(issues []model.Issue, label string) []model.Issue {
	var result []model.Issue
	for _, issue := range issues {
		for _, l := range issue.Labels {
			if l == label {
				result = append(result, issue)
				break
			}
		}
	}
	return result
}

// GetLabelsForIssue returns all labels for a specific issue ID
func GetLabelsForIssue(issues []model.Issue, issueID string) []string {
	for _, issue := range issues {
		if issue.ID == issueID {
			return issue.Labels
		}
	}
	return nil
}

// GetCommonLabels returns labels that appear in multiple provided label sets
func GetCommonLabels(labelSets ...[]string) []string {
	if len(labelSets) == 0 {
		return nil
	}

	// Count occurrences
	counts := make(map[string]int)
	for _, labels := range labelSets {
		seen := make(map[string]bool)
		for _, label := range labels {
			if !seen[label] {
				counts[label]++
				seen[label] = true
			}
		}
	}

	// Find labels in all sets
	var common []string
	for label, count := range counts {
		if count == len(labelSets) {
			common = append(common, label)
		}
	}
	sort.Strings(common)
	return common
}

// GetLabelCooccurrence builds a co-occurrence matrix showing which labels appear together
func GetLabelCooccurrence(issues []model.Issue) map[string]map[string]int {
	cooc := make(map[string]map[string]int)

	for _, issue := range issues {
		labels := issue.Labels
		// For each pair of labels on the same issue
		for i := 0; i < len(labels); i++ {
			for j := i + 1; j < len(labels); j++ {
				l1, l2 := labels[i], labels[j]
				// Ensure consistent ordering
				if l1 > l2 {
					l1, l2 = l2, l1
				}

				// Initialize maps if needed
				if cooc[l1] == nil {
					cooc[l1] = make(map[string]int)
				}
				if cooc[l2] == nil {
					cooc[l2] = make(map[string]int)
				}

				// Increment both directions
				cooc[l1][l2]++
				cooc[l2][l1]++
			}
		}
	}

	return cooc
}

// ComputeBlockedByLabel determines which issues are blocked, grouped by label
// Returns a map of label -> count of blocked issues with that label
func ComputeBlockedByLabel(issues []model.Issue, analyzer *Analyzer) map[string]int {
	blocked := make(map[string]int)

	for _, issue := range issues {
		if isClosedLikeStatus(issue.Status) {
			continue
		}

		// Check if issue is blocked
		blockers := analyzer.GetOpenBlockers(issue.ID)
		if len(blockers) > 0 {
			// This issue is blocked - count for each of its labels
			for _, label := range issue.Labels {
				blocked[label]++
			}
		}
	}

	return blocked
}

// ComputeBlockageCascade computes the transitive downstream impact when labels have blocked issues.
// For each label with blocked issues, it shows which other labels are waiting (transitively).
// Example output: database(4 blocked) -> backend: 3 waiting -> testing: 2 waiting
func ComputeBlockageCascade(issues []model.Issue, flow CrossLabelFlow, cfg LabelHealthConfig) BlockageCascadeAnalysis {
	result := BlockageCascadeAnalysis{
		GeneratedAt: time.Now(),
		Cascades:    []BlockageCascadeResult{},
		TopUnblocks: []UnblockRecommendation{},
	}

	if len(flow.Labels) == 0 {
		return result
	}

	// Build label index for matrix lookups
	labelIndex := make(map[string]int, len(flow.Labels))
	for i, label := range flow.Labels {
		labelIndex[label] = i
	}

	// Build issue map and count blocked per label
	issueMap := make(map[string]model.Issue, len(issues))
	blockedByLabel := make(map[string][]model.Issue)
	blockedIssueIDs := make(map[string]bool) // Track unique blocked issues
	for _, iss := range issues {
		issueMap[iss.ID] = iss
		if iss.Status == model.StatusBlocked {
			blockedIssueIDs[iss.ID] = true // Count each blocked issue once
			for _, label := range iss.Labels {
				blockedByLabel[label] = append(blockedByLabel[label], iss)
			}
		}
	}

	// Count total blocked (unique issues, not double-counting multi-label issues)
	result.TotalBlocked = len(blockedIssueIDs)

	// For each label with blocked issues, compute cascade
	var allCascades []BlockageCascadeResult
	for sourceLabel, blockedIssues := range blockedByLabel {
		if len(blockedIssues) == 0 {
			continue
		}

		cascade := computeSingleCascade(sourceLabel, blockedIssues, flow, labelIndex, issueMap)
		if cascade.TotalImpact > 0 || cascade.BlockedCount > 0 {
			allCascades = append(allCascades, cascade)
		}
	}

	// Sort cascades by total impact (highest first)
	sort.Slice(allCascades, func(i, j int) bool {
		return allCascades[i].TotalImpact > allCascades[j].TotalImpact
	})
	result.Cascades = allCascades

	// Collect all recommendations and sort by unblock count
	var allRecs []UnblockRecommendation
	for _, cascade := range allCascades {
		allRecs = append(allRecs, cascade.Recommendations...)
	}
	sort.Slice(allRecs, func(i, j int) bool {
		if allRecs[i].UnblocksCount != allRecs[j].UnblocksCount {
			return allRecs[i].UnblocksCount > allRecs[j].UnblocksCount
		}
		return allRecs[i].CascadeDepth > allRecs[j].CascadeDepth
	})

	// Take top 10 recommendations
	if len(allRecs) > 10 {
		allRecs = allRecs[:10]
	}
	result.TopUnblocks = allRecs

	// Find critical chain (longest cascade)
	var longestChain []string
	for _, cascade := range allCascades {
		chain := []string{cascade.SourceLabel}
		for _, level := range cascade.CascadeLevels {
			for _, entry := range level.Labels {
				chain = append(chain, entry.Label)
			}
		}
		if len(chain) > len(longestChain) {
			longestChain = chain
		}
	}
	result.CriticalChain = longestChain

	return result
}

// computeSingleCascade computes the cascade for a single source label
func computeSingleCascade(sourceLabel string, blockedIssues []model.Issue, flow CrossLabelFlow, labelIndex map[string]int, issueMap map[string]model.Issue) BlockageCascadeResult {
	result := BlockageCascadeResult{
		SourceLabel:     sourceLabel,
		BlockedCount:    len(blockedIssues),
		CascadeLevels:   []CascadeLevel{},
		AffectedLabels:  []string{},
		Recommendations: []UnblockRecommendation{},
	}

	if _, ok := labelIndex[sourceLabel]; !ok {
		return result
	}

	// BFS to find transitive downstream labels
	visited := make(map[string]bool)
	visited[sourceLabel] = true
	currentLevel := []string{sourceLabel}
	level := 0
	totalImpact := 0
	affectedSet := make(map[string]bool)

	for len(currentLevel) > 0 && level < 10 { // Cap at 10 levels to prevent infinite loops
		level++
		var nextLevel []string
		levelEntries := []LabelCascadeEntry{}
		levelTotal := 0

		for _, fromLabel := range currentLevel {
			fromIdx, hasFrom := labelIndex[fromLabel]
			if !hasFrom {
				continue
			}

			// Find labels that this label blocks (outgoing edges in flow matrix)
			for toIdx, count := range flow.FlowMatrix[fromIdx] {
				if count == 0 {
					continue
				}
				toLabel := flow.Labels[toIdx]
				if visited[toLabel] {
					continue
				}
				visited[toLabel] = true
				nextLevel = append(nextLevel, toLabel)
				affectedSet[toLabel] = true

				// Find which labels block this one at this level
				fromLabels := []string{}
				for _, fl := range currentLevel {
					fIdx, ok := labelIndex[fl]
					if ok && flow.FlowMatrix[fIdx][toIdx] > 0 {
						fromLabels = append(fromLabels, fl)
					}
				}
				sort.Strings(fromLabels)

				levelEntries = append(levelEntries, LabelCascadeEntry{
					Label:        toLabel,
					WaitingCount: count,
					FromLabels:   fromLabels,
				})
				levelTotal += count
			}
		}

		if len(levelEntries) > 0 {
			// Sort entries by waiting count (highest first)
			sort.Slice(levelEntries, func(i, j int) bool {
				return levelEntries[i].WaitingCount > levelEntries[j].WaitingCount
			})

			result.CascadeLevels = append(result.CascadeLevels, CascadeLevel{
				Level:         level,
				Labels:        levelEntries,
				TotalAffected: levelTotal,
			})
			totalImpact += levelTotal
		}

		currentLevel = nextLevel
	}

	result.TotalImpact = totalImpact

	// Convert affected set to sorted slice
	for label := range affectedSet {
		result.AffectedLabels = append(result.AffectedLabels, label)
	}
	sort.Strings(result.AffectedLabels)

	// Generate recommendations - find blockers that would unblock the most
	blockerImpact := make(map[string]int) // issueID -> transitive unblock count
	for _, blockedIssue := range blockedIssues {
		for _, dep := range blockedIssue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blocker, exists := issueMap[dep.DependsOnID]
			if !exists || isClosedLikeStatus(blocker.Status) {
				continue
			}
			// Count how many issues this blocker transitively affects
			impact := 1 + totalImpact/max(1, len(blockedIssues))
			blockerImpact[blocker.ID] += impact
		}
	}

	// Sort blockers by impact
	type blockerRec struct {
		id     string
		impact int
	}
	var blockers []blockerRec
	for id, impact := range blockerImpact {
		blockers = append(blockers, blockerRec{id: id, impact: impact})
	}
	sort.Slice(blockers, func(i, j int) bool {
		return blockers[i].impact > blockers[j].impact
	})

	// Take top 5 recommendations for this cascade
	for i, b := range blockers {
		if i >= 5 {
			break
		}
		iss := issueMap[b.id]
		label := sourceLabel
		if len(iss.Labels) > 0 {
			label = iss.Labels[0]
		}
		result.Recommendations = append(result.Recommendations, UnblockRecommendation{
			IssueID:       b.id,
			IssueTitle:    iss.Title,
			Label:         label,
			UnblocksCount: b.impact,
			CascadeDepth:  len(result.CascadeLevels),
			Reason:        fmt.Sprintf("Unblocks %d issues across %d downstream labels", b.impact, len(result.AffectedLabels)),
		})
	}

	return result
}

// GetCascadeForLabel returns the cascade result for a specific label
func (r *BlockageCascadeAnalysis) GetCascadeForLabel(label string) *BlockageCascadeResult {
	for i := range r.Cascades {
		if r.Cascades[i].SourceLabel == label {
			return &r.Cascades[i]
		}
	}
	return nil
}

// GetMostImpactfulCascade returns the cascade with the highest total impact
func (r *BlockageCascadeAnalysis) GetMostImpactfulCascade() *BlockageCascadeResult {
	if len(r.Cascades) == 0 {
		return nil
	}
	return &r.Cascades[0] // Already sorted by impact
}

// FormatCascadeTree returns a human-readable tree representation of a cascade
func (c *BlockageCascadeResult) FormatCascadeTree() string {
	if c == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s (%d blocked)\n", c.SourceLabel, c.BlockedCount))

	for _, level := range c.CascadeLevels {
		indent := strings.Repeat("  ", level.Level)
		for _, entry := range level.Labels {
			sb.WriteString(fmt.Sprintf("%s└─> %s: %d waiting\n", indent, entry.Label, entry.WaitingCount))
		}
	}

	return sb.String()
}

// ============================================================================
// Label Subgraph Extraction (bv-113)
// Extract a subgraph for label-scoped graph analysis
// ============================================================================

// LabelSubgraph represents a subgraph of issues filtered by label.
// It includes core issues (those with the label) plus their direct dependencies
// (even if outside the label) to enable meaningful graph analysis within label context.
type LabelSubgraph struct {
	Label            string                 `json:"label"`             // The filter label
	CoreIssues       []string               `json:"core_issues"`       // Issue IDs with this label (sorted)
	DependencyIssues []string               `json:"dependency_issues"` // Direct dependencies outside label
	AllIssues        []string               `json:"all_issues"`        // CoreIssues + DependencyIssues
	IssueCount       int                    `json:"issue_count"`       // len(AllIssues)
	CoreCount        int                    `json:"core_count"`        // len(CoreIssues)
	EdgeCount        int                    `json:"edge_count"`        // Total dependency edges in subgraph
	Adjacency        map[string][]string    `json:"adjacency"`         // from -> [to] (blocking relationships)
	InDegree         map[string]int         `json:"in_degree"`         // blocked_by count per issue
	OutDegree        map[string]int         `json:"out_degree"`        // blocks count per issue
	IssueMap         map[string]model.Issue `json:"-"`                 // Quick lookup for issues in subgraph
}

// ComputeLabelSubgraph extracts a subgraph for issues with a given label.
// This function:
// 1. Finds all issues with the target label (core issues)
// 2. Includes direct dependencies of core issues (even if they don't have the label)
// 3. Builds an adjacency structure for label-scoped graph analysis
//
// The resulting subgraph can be used to run PageRank, critical path, and other
// graph algorithms within the context of a specific label.
func ComputeLabelSubgraph(issues []model.Issue, label string) LabelSubgraph {
	result := LabelSubgraph{
		Label:            label,
		CoreIssues:       []string{},
		DependencyIssues: []string{},
		AllIssues:        []string{},
		Adjacency:        make(map[string][]string),
		InDegree:         make(map[string]int),
		OutDegree:        make(map[string]int),
		IssueMap:         make(map[string]model.Issue),
	}

	if label == "" || len(issues) == 0 {
		return result
	}

	// Build issue lookup map for the full issue set
	fullIssueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		fullIssueMap[iss.ID] = iss
	}

	// Find core issues (those with the target label)
	coreSet := make(map[string]bool)
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l == label {
				coreSet[iss.ID] = true
				result.IssueMap[iss.ID] = iss
				break
			}
		}
	}

	// Find dependency issues (direct dependencies of core issues, even if outside label)
	depSet := make(map[string]bool)
	for coreID := range coreSet {
		coreIssue := result.IssueMap[coreID]

		// Add issues that this core issue depends on (blockers)
		for _, dep := range coreIssue.Dependencies {
			if dep == nil {
				continue
			}
			blockerID := dep.DependsOnID
			if _, inCore := coreSet[blockerID]; !inCore {
				if blockerIssue, exists := fullIssueMap[blockerID]; exists {
					depSet[blockerID] = true
					result.IssueMap[blockerID] = blockerIssue
				}
			}
		}

		// Add issues that depend on this core issue (blocked by it)
		for _, iss := range issues {
			for _, dep := range iss.Dependencies {
				if dep != nil && dep.DependsOnID == coreID {
					if _, inCore := coreSet[iss.ID]; !inCore {
						depSet[iss.ID] = true
						result.IssueMap[iss.ID] = iss
					}
				}
			}
		}
	}

	// Build sorted issue ID lists
	for id := range coreSet {
		result.CoreIssues = append(result.CoreIssues, id)
	}
	sort.Strings(result.CoreIssues)

	for id := range depSet {
		result.DependencyIssues = append(result.DependencyIssues, id)
	}
	sort.Strings(result.DependencyIssues)

	// Combine into AllIssues
	result.AllIssues = make([]string, 0, len(coreSet)+len(depSet))
	result.AllIssues = append(result.AllIssues, result.CoreIssues...)
	result.AllIssues = append(result.AllIssues, result.DependencyIssues...)
	sort.Strings(result.AllIssues)

	result.CoreCount = len(result.CoreIssues)
	result.IssueCount = len(result.AllIssues)

	// Build adjacency structure for issues in the subgraph
	subgraphSet := make(map[string]bool, len(result.AllIssues))
	for _, id := range result.AllIssues {
		subgraphSet[id] = true
	}

	for _, id := range result.AllIssues {
		iss := result.IssueMap[id]
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blockerID := dep.DependsOnID
			// Only include edges where both ends are in the subgraph
			if subgraphSet[blockerID] {
				// Edge: blockerID -> id (blocker blocks this issue)
				result.Adjacency[blockerID] = append(result.Adjacency[blockerID], id)
				result.OutDegree[blockerID]++
				result.InDegree[id]++
				result.EdgeCount++
			}
		}
	}

	// Sort adjacency lists for deterministic output
	for from := range result.Adjacency {
		sort.Strings(result.Adjacency[from])
	}

	return result
}

// HasLabel checks if an issue has a specific label
func HasLabel(issue model.Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// GetSubgraphRoots returns issues in the subgraph with no incoming edges (not blocked)
func (sg *LabelSubgraph) GetSubgraphRoots() []string {
	var roots []string
	for _, id := range sg.AllIssues {
		if sg.InDegree[id] == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	return roots
}

// GetSubgraphLeaves returns issues in the subgraph with no outgoing edges (not blocking anything)
func (sg *LabelSubgraph) GetSubgraphLeaves() []string {
	var leaves []string
	for _, id := range sg.AllIssues {
		if sg.OutDegree[id] == 0 {
			leaves = append(leaves, id)
		}
	}
	sort.Strings(leaves)
	return leaves
}

// GetCoreIssueSet returns the core issues as a set for O(1) lookup
func (sg *LabelSubgraph) GetCoreIssueSet() map[string]bool {
	set := make(map[string]bool, len(sg.CoreIssues))
	for _, id := range sg.CoreIssues {
		set[id] = true
	}
	return set
}

// IsEmpty returns true if the subgraph has no issues
func (sg *LabelSubgraph) IsEmpty() bool {
	return sg.IssueCount == 0
}

// ============================================================================
// Label-Specific PageRank (bv-114)
// Run PageRank on a label subgraph for label-scoped centrality analysis
// ============================================================================

// LabelPageRankResult contains PageRank scores for a label subgraph
type LabelPageRankResult struct {
	Label      string             `json:"label"`       // The label analyzed
	Scores     map[string]float64 `json:"scores"`      // Issue ID -> PageRank score
	Normalized map[string]float64 `json:"normalized"`  // Scores normalized to 0-1 range
	TopIssues  []RankedIssue      `json:"top_issues"`  // Top issues by PageRank, sorted
	CoreOnly   map[string]float64 `json:"core_only"`   // Scores for core issues only (with label)
	IssueCount int                `json:"issue_count"` // Total issues in subgraph
	CoreCount  int                `json:"core_count"`  // Issues with the label
	MaxScore   float64            `json:"max_score"`   // Highest score in subgraph
	MinScore   float64            `json:"min_score"`   // Lowest score in subgraph
}

// RankedIssue represents an issue with its ranking information
type RankedIssue struct {
	ID     string  `json:"id"`
	Score  float64 `json:"score"`
	Rank   int     `json:"rank"`
	IsCore bool    `json:"is_core"` // True if issue has the target label
	Title  string  `json:"title,omitempty"`
}

// ComputeLabelPageRank runs PageRank on a label subgraph.
// This provides label-scoped centrality analysis, useful for:
// - Finding the most important issues within a label
// - Identifying bottlenecks in label-specific workflows
// - Comparing relative importance of issues within a feature area
//
// The damping factor is 0.85 (standard PageRank value).
// The tolerance is 1e-6 for convergence.
func ComputeLabelPageRank(sg LabelSubgraph) LabelPageRankResult {
	result := LabelPageRankResult{
		Label:      sg.Label,
		Scores:     make(map[string]float64),
		Normalized: make(map[string]float64),
		CoreOnly:   make(map[string]float64),
		TopIssues:  []RankedIssue{},
		IssueCount: sg.IssueCount,
		CoreCount:  sg.CoreCount,
	}

	if sg.IsEmpty() {
		return result
	}

	// Build gonum graph from subgraph adjacency
	g := simple.NewDirectedGraph()
	idToNode := make(map[string]int64, sg.IssueCount)
	nodeToID := make(map[int64]string, sg.IssueCount)

	// Add nodes
	for _, id := range sg.AllIssues {
		n := g.NewNode()
		g.AddNode(n)
		idToNode[id] = n.ID()
		nodeToID[n.ID()] = id
	}

	// Add edges from adjacency (blocker -> blocked)
	for from, toList := range sg.Adjacency {
		fromNode, ok := idToNode[from]
		if !ok {
			continue
		}
		for _, to := range toList {
			toNode, exists := idToNode[to]
			if !exists {
				continue
			}
			// Edge direction: blocker -> blocked (from blocks to)
			g.SetEdge(g.NewEdge(g.Node(fromNode), g.Node(toNode)))
		}
	}

	// Run deterministic PageRank (damping 0.85, tolerance 1e-6)
	pr := computePageRank(g, 0.85, 1e-6)

	// Convert to string IDs and find min/max
	var maxScore, minScore float64
	first := true
	for nodeID, score := range pr {
		issueID := nodeToID[nodeID]
		result.Scores[issueID] = score
		if first {
			maxScore = score
			minScore = score
			first = false
		} else {
			if score > maxScore {
				maxScore = score
			}
			if score < minScore {
				minScore = score
			}
		}
	}

	result.MaxScore = maxScore
	result.MinScore = minScore

	// Normalize scores to 0-1 range
	scoreRange := maxScore - minScore
	if scoreRange > 0 {
		for id, score := range result.Scores {
			result.Normalized[id] = (score - minScore) / scoreRange
		}
	} else {
		// All scores equal, normalize to 0.5
		for id := range result.Scores {
			result.Normalized[id] = 0.5
		}
	}

	// Extract core-only scores
	coreSet := sg.GetCoreIssueSet()
	for id, score := range result.Scores {
		if coreSet[id] {
			result.CoreOnly[id] = score
		}
	}

	// Build ranked list
	type scorePair struct {
		id    string
		score float64
	}
	var pairs []scorePair
	for id, score := range result.Scores {
		pairs = append(pairs, scorePair{id: id, score: score})
	}
	// Sort by score descending, then by ID for determinism
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		return pairs[i].id < pairs[j].id
	})

	for rank, p := range pairs {
		title := ""
		if iss, ok := sg.IssueMap[p.id]; ok {
			title = iss.Title
		}
		result.TopIssues = append(result.TopIssues, RankedIssue{
			ID:     p.id,
			Score:  p.score,
			Rank:   rank + 1,
			IsCore: coreSet[p.id],
			Title:  title,
		})
	}

	return result
}

// ComputeLabelPageRankFromIssues is a convenience function that creates the subgraph
// and runs PageRank in one call.
func ComputeLabelPageRankFromIssues(issues []model.Issue, label string) LabelPageRankResult {
	sg := ComputeLabelSubgraph(issues, label)
	return ComputeLabelPageRank(sg)
}

// GetTopCoreIssues returns the top N core issues (those with the label) by PageRank
func (r *LabelPageRankResult) GetTopCoreIssues(n int) []RankedIssue {
	var coreIssues []RankedIssue
	for _, ri := range r.TopIssues {
		if ri.IsCore {
			coreIssues = append(coreIssues, ri)
			if len(coreIssues) >= n {
				break
			}
		}
	}
	return coreIssues
}

// GetNormalizedScore returns the normalized (0-1) score for an issue
func (r *LabelPageRankResult) GetNormalizedScore(id string) float64 {
	return r.Normalized[id]
}

// ============================================================================
// Label Critical Path (bv-115)
// Find the longest dependency chain within a label's subgraph
// ============================================================================

// LabelCriticalPathResult contains the critical path for a label subgraph
type LabelCriticalPathResult struct {
	Label      string         `json:"label"`       // The label analyzed
	Path       []string       `json:"path"`        // Issue IDs in critical path order (root -> leaf)
	PathLength int            `json:"path_length"` // Number of issues in the path
	PathTitles []string       `json:"path_titles"` // Titles corresponding to path IDs
	AllHeights map[string]int `json:"all_heights"` // Heights for all issues in subgraph
	MaxHeight  int            `json:"max_height"`  // Maximum height in the subgraph
	IssueCount int            `json:"issue_count"` // Total issues in subgraph
	HasCycle   bool           `json:"has_cycle"`   // True if cycle detected (path unreliable)
}

// ComputeLabelCriticalPath finds the longest dependency chain in a label subgraph.
// The critical path represents the longest sequence of blocking dependencies,
// useful for identifying bottleneck structures within a label's issue set.
//
// Returns the path from root (blocker with no parents in subgraph) to leaf
// (issue blocking nothing in subgraph).
func ComputeLabelCriticalPath(sg LabelSubgraph) LabelCriticalPathResult {
	result := LabelCriticalPathResult{
		Label:      sg.Label,
		Path:       []string{},
		PathTitles: []string{},
		AllHeights: make(map[string]int),
		IssueCount: sg.IssueCount,
	}

	if sg.IsEmpty() {
		return result
	}

	// Build gonum graph from subgraph adjacency
	g := simple.NewDirectedGraph()
	idToNode := make(map[string]int64, sg.IssueCount)
	nodeToID := make(map[int64]string, sg.IssueCount)

	// Add nodes
	for _, id := range sg.AllIssues {
		n := g.NewNode()
		g.AddNode(n)
		idToNode[id] = n.ID()
		nodeToID[n.ID()] = id
	}

	// Add edges from adjacency (blocker -> blocked)
	for from, toList := range sg.Adjacency {
		fromNode, ok := idToNode[from]
		if !ok {
			continue
		}
		for _, to := range toList {
			toNode, exists := idToNode[to]
			if !exists {
				continue
			}
			g.SetEdge(g.NewEdge(g.Node(fromNode), g.Node(toNode)))
		}
	}

	// Topological sort - handles cycles gracefully
	sorted, err := topo.Sort(g)
	if err != nil {
		// Cycle detected - mark it and return early
		result.HasCycle = true
		return result
	}

	// Compute heights using dynamic programming
	// Height = 1 + max(parent heights)
	// We process in topological order so parents are computed first
	heights := make(map[int64]int)
	parent := make(map[int64]int64) // Track the parent that gave max height

	for _, n := range sorted {
		nid := n.ID()
		maxParentHeight := 0
		var bestParent int64 = -1

		// Look at incoming edges (parents/blockers)
		to := g.To(nid)
		for to.Next() {
			p := to.Node()
			if h, ok := heights[p.ID()]; ok {
				if h > maxParentHeight {
					maxParentHeight = h
					bestParent = p.ID()
				}
			}
		}

		heights[nid] = 1 + maxParentHeight
		if bestParent >= 0 {
			parent[nid] = bestParent
		}
	}

	// Convert heights to string IDs and find max
	var maxNode int64 = -1
	maxHeight := 0
	for nodeID, h := range heights {
		issueID := nodeToID[nodeID]
		result.AllHeights[issueID] = h
		if h > maxHeight {
			maxHeight = h
			maxNode = nodeID
		}
	}
	result.MaxHeight = maxHeight

	// Reconstruct the critical path from max node back to root
	if maxNode >= 0 {
		// Build path in reverse (from leaf to root)
		var pathReverse []int64
		current := maxNode
		for {
			pathReverse = append(pathReverse, current)
			p, hasParent := parent[current]
			if !hasParent {
				break
			}
			current = p
		}

		// Reverse to get root -> leaf order
		for i := len(pathReverse) - 1; i >= 0; i-- {
			issueID := nodeToID[pathReverse[i]]
			result.Path = append(result.Path, issueID)
			title := ""
			if iss, ok := sg.IssueMap[issueID]; ok {
				title = iss.Title
			}
			result.PathTitles = append(result.PathTitles, title)
		}
	}

	result.PathLength = len(result.Path)
	return result
}

// ComputeLabelCriticalPathFromIssues is a convenience function that creates
// the subgraph and computes the critical path in one call.
func ComputeLabelCriticalPathFromIssues(issues []model.Issue, label string) LabelCriticalPathResult {
	sg := ComputeLabelSubgraph(issues, label)
	return ComputeLabelCriticalPath(sg)
}

// GetCriticalPathIssues returns the full issues on the critical path
func (r *LabelCriticalPathResult) GetCriticalPathIssues(sg LabelSubgraph) []model.Issue {
	var issues []model.Issue
	for _, id := range r.Path {
		if iss, ok := sg.IssueMap[id]; ok {
			issues = append(issues, iss)
		}
	}
	return issues
}

// IsCriticalPathMember returns true if the given issue ID is on the critical path
func (r *LabelCriticalPathResult) IsCriticalPathMember(id string) bool {
	for _, pid := range r.Path {
		if pid == id {
			return true
		}
	}
	return false
}

// ============================================================================
// Label Attention Score (bv-116)
// Compute attention needed for labels based on PageRank, staleness, and velocity
// ============================================================================

// LabelAttentionScore represents how much attention a label needs
type LabelAttentionScore struct {
	Label           string  `json:"label"`
	AttentionScore  float64 `json:"attention_score"`  // Higher = needs more attention
	NormalizedScore float64 `json:"normalized_score"` // 0-1 normalized
	Rank            int     `json:"rank"`             // 1-based rank

	// Component factors
	PageRankSum     float64 `json:"pagerank_sum"`     // Sum of PageRank scores
	StalenessFactor float64 `json:"staleness_factor"` // Higher = more stale
	BlockImpact     float64 `json:"block_impact"`     // Issues blocked by this label
	VelocityFactor  float64 `json:"velocity_factor"`  // Higher = more velocity (good)

	// Context
	OpenCount    int `json:"open_count"`
	BlockedCount int `json:"blocked_count"`
	StaleCount   int `json:"stale_count"`
}

// LabelAttentionResult contains attention scores for all labels
type LabelAttentionResult struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Labels       []LabelAttentionScore `json:"labels"`        // Sorted by attention (descending)
	TopAttention []string              `json:"top_attention"` // Labels needing most attention
	LowAttention []string              `json:"low_attention"` // Labels with least attention needed
	MaxScore     float64               `json:"max_score"`
	MinScore     float64               `json:"min_score"`
	TotalLabels  int                   `json:"total_labels"`
}

// ComputeLabelAttentionScores calculates attention needed for all labels.
// Formula: attention = (pagerank_sum * staleness_factor * block_impact) / velocity
// Higher score = needs more attention.
//
// Factors:
// - pagerank_sum: Centrality importance of issues in this label
// - staleness_factor: 1 + (stale_count / open_count), higher if issues are stale
// - block_impact: Number of issues blocked by this label
// - velocity: Recent closures (higher = healthier, less attention needed)
func ComputeLabelAttentionScores(issues []model.Issue, cfg LabelHealthConfig, now time.Time) LabelAttentionResult {
	result := LabelAttentionResult{
		GeneratedAt: now,
		Labels:      []LabelAttentionScore{},
	}

	labels := ExtractLabels(issues)
	if labels.LabelCount == 0 {
		return result
	}

	// Build issue map for lookups
	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	// Compute attention for each label
	var scores []LabelAttentionScore
	for _, label := range labels.Labels {
		score := computeLabelAttention(label, issues, issueMap, cfg, now)
		scores = append(scores, score)
	}

	// Find min/max for normalization
	var maxScore, minScore float64
	first := true
	for _, s := range scores {
		if first {
			maxScore = s.AttentionScore
			minScore = s.AttentionScore
			first = false
		} else {
			if s.AttentionScore > maxScore {
				maxScore = s.AttentionScore
			}
			if s.AttentionScore < minScore {
				minScore = s.AttentionScore
			}
		}
	}

	result.MaxScore = maxScore
	result.MinScore = minScore

	// Normalize scores
	scoreRange := maxScore - minScore
	for i := range scores {
		if scoreRange > 0 {
			scores[i].NormalizedScore = (scores[i].AttentionScore - minScore) / scoreRange
		} else {
			scores[i].NormalizedScore = 0.5
		}
	}

	// Sort by attention score descending, then by label for determinism.
	// Use an epsilon so near-identical scores don't cause unstable ordering due to
	// floating point noise (e.g., PageRank power-iteration over map-backed graphs).
	sort.Slice(scores, func(i, j int) bool {
		const eps = 1e-6
		if diff := scores[i].AttentionScore - scores[j].AttentionScore; math.Abs(diff) > eps {
			return diff > 0
		}
		return scores[i].Label < scores[j].Label
	})

	// Assign ranks
	for i := range scores {
		scores[i].Rank = i + 1
	}

	result.Labels = scores
	result.TotalLabels = len(scores)

	// Extract top/low attention labels (avoid overlap for small sets)
	topN := min(3, len(scores))
	for i := 0; i < topN; i++ {
		result.TopAttention = append(result.TopAttention, scores[i].Label)
	}
	lowStart := len(scores) - topN
	if lowStart < topN {
		lowStart = topN // Don't overlap with top attention labels
	}
	for i := lowStart; i < len(scores); i++ {
		result.LowAttention = append(result.LowAttention, scores[i].Label)
	}

	return result
}

// computeLabelAttention calculates attention score for a single label
func computeLabelAttention(label string, issues []model.Issue, issueMap map[string]model.Issue, cfg LabelHealthConfig, now time.Time) LabelAttentionScore {
	score := LabelAttentionScore{
		Label: label,
	}

	// Get issues with this label
	var labeledIssues []model.Issue
	for _, iss := range issues {
		if HasLabel(iss, label) {
			labeledIssues = append(labeledIssues, iss)
		}
	}

	if len(labeledIssues) == 0 {
		return score
	}

	// Count open and blocked issues
	for _, iss := range labeledIssues {
		if !isClosedLikeStatus(iss.Status) {
			score.OpenCount++
		}
	}

	// Compute PageRank sum for this label
	sg := ComputeLabelSubgraph(issues, label)
	if !sg.IsEmpty() {
		pr := ComputeLabelPageRank(sg)
		for _, s := range pr.CoreOnly {
			score.PageRankSum += s
		}
	}

	// Compute staleness factor
	freshness := ComputeFreshnessMetrics(labeledIssues, now, cfg.StaleThresholdDays)
	score.StaleCount = freshness.StaleCount
	if score.OpenCount > 0 {
		score.StalenessFactor = 1.0 + float64(score.StaleCount)/float64(score.OpenCount)
	} else {
		score.StalenessFactor = 1.0
	}

	// Compute block impact (how many issues are blocked by this label)
	blockImpact := 0
	for _, iss := range labeledIssues {
		// Count how many other issues depend on this one
		for _, other := range issues {
			if other.ID == iss.ID {
				continue
			}
			for _, dep := range other.Dependencies {
				if dep != nil && dep.DependsOnID == iss.ID && dep.Type.IsBlocking() {
					blockImpact++
				}
			}
		}
	}
	score.BlockImpact = float64(blockImpact)
	score.BlockedCount = blockImpact

	// Compute velocity factor
	velocity := ComputeVelocityMetrics(labeledIssues, now)
	// Use closed in last 30 days + 1 to avoid division by zero
	score.VelocityFactor = float64(velocity.ClosedLast30Days) + 1.0

	// Compute attention score
	// attention = (pagerank_sum * staleness_factor * (1 + block_impact)) / velocity
	// Higher = needs more attention
	numerator := score.PageRankSum * score.StalenessFactor * (1.0 + score.BlockImpact)
	score.AttentionScore = numerator / score.VelocityFactor

	return score
}

// GetTopAttentionLabels returns the top N labels needing attention
func (r *LabelAttentionResult) GetTopAttentionLabels(n int) []LabelAttentionScore {
	if n > len(r.Labels) {
		n = len(r.Labels)
	}
	return r.Labels[:n]
}

// GetLabelAttention returns the attention score for a specific label
func (r *LabelAttentionResult) GetLabelAttention(label string) *LabelAttentionScore {
	for i := range r.Labels {
		if r.Labels[i].Label == label {
			return &r.Labels[i]
		}
	}
	return nil
}

// ============================================================================
// Historical Velocity Computation (bv-123)
// ============================================================================

// ComputeHistoricalVelocity calculates velocity per week for past N weeks.
// This enables trend analysis, anomaly detection, and forecasting.
// Uses ClosedAt timestamps from issues to bucket closures into weeks.
func ComputeHistoricalVelocity(issues []model.Issue, label string, numWeeks int, now time.Time) HistoricalVelocity {
	if numWeeks <= 0 {
		numWeeks = 8
	}
	result := HistoricalVelocity{
		Label:          label,
		WeeklyVelocity: make([]WeeklySnapshot, numWeeks),
		WeeksAnalyzed:  numWeeks,
	}

	// Filter to labeled issues
	var labeled []model.Issue
	for _, iss := range issues {
		for _, l := range iss.Labels {
			if l == label {
				labeled = append(labeled, iss)
				break
			}
		}
	}

	// Calculate week boundaries (weeks start on Monday)
	// weekStart aligns to the Monday of the current week
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	// Go back to Monday of current week
	currentWeekStart := now.AddDate(0, 0, -(weekday - 1)).Truncate(24 * time.Hour)

	// Build week buckets
	for i := 0; i < numWeeks; i++ {
		weekStart := currentWeekStart.AddDate(0, 0, -7*i)
		weekEnd := weekStart.AddDate(0, 0, 7)
		result.WeeklyVelocity[i] = WeeklySnapshot{
			WeekStart: weekStart,
			WeekEnd:   weekEnd,
			WeeksAgo:  i,
		}
	}

	// Bucket closed issues by week
	cumulative := 0
	for _, iss := range labeled {
		if !isClosedLikeStatus(iss.Status) {
			continue
		}
		if iss.ClosedAt == nil {
			continue
		}
		closedAt := *iss.ClosedAt

		// Find the appropriate week bucket
		for i := range result.WeeklyVelocity {
			snap := &result.WeeklyVelocity[i]
			if !closedAt.Before(snap.WeekStart) && closedAt.Before(snap.WeekEnd) {
				snap.Closed++
				snap.IssueIDs = append(snap.IssueIDs, iss.ID)
				break
			}
		}
	}

	// Calculate cumulative totals (from oldest to newest)
	for i := numWeeks - 1; i >= 0; i-- {
		cumulative += result.WeeklyVelocity[i].Closed
		result.WeeklyVelocity[i].Cumulative = cumulative
	}

	// Find peak and trough weeks
	peakVel := 0
	troughVel := int(^uint(0) >> 1) // Max int
	peakIdx := 0
	troughIdx := 0
	hasNonZero := false

	for i, snap := range result.WeeklyVelocity {
		if snap.Closed > peakVel {
			peakVel = snap.Closed
			peakIdx = i
		}
		if snap.Closed > 0 && snap.Closed < troughVel {
			troughVel = snap.Closed
			troughIdx = i
			hasNonZero = true
		}
	}

	result.PeakWeek = peakIdx
	result.PeakVelocity = peakVel
	if hasNonZero {
		result.TroughWeek = troughIdx
		result.TroughVelocity = troughVel
	}

	// Calculate 4-week moving average (most recent 4 weeks)
	if numWeeks >= 4 {
		sum4 := 0
		for i := 0; i < 4; i++ {
			sum4 += result.WeeklyVelocity[i].Closed
		}
		result.MovingAvg4Week = float64(sum4) / 4.0
	}

	// Calculate 8-week moving average if available
	if numWeeks >= 8 {
		sum8 := 0
		for i := 0; i < 8; i++ {
			sum8 += result.WeeklyVelocity[i].Closed
		}
		result.MovingAvg8Week = float64(sum8) / 8.0
	}

	// Calculate variance for consistency score
	if numWeeks > 0 {
		var sum float64
		for _, snap := range result.WeeklyVelocity {
			sum += float64(snap.Closed)
		}
		mean := sum / float64(numWeeks)

		var variance float64
		for _, snap := range result.WeeklyVelocity {
			diff := float64(snap.Closed) - mean
			variance += diff * diff
		}
		variance /= float64(numWeeks)
		result.Variance = variance

		// Consistency score: low variance relative to mean = high consistency
		// Use coefficient of variation (CV) - lower is better
		// CV = stddev / mean, so we invert it for the score
		if mean > 0 {
			stddev := math.Sqrt(variance)
			cv := stddev / mean
			// CV of 0 = 100 score, CV of 1+ = 0 score
			result.ConsistencyScore = clampScore(int(100 * (1 - cv)))
		} else {
			result.ConsistencyScore = 0 // No closures = no consistency score
		}
	}

	return result
}

// ComputeAllHistoricalVelocity computes historical velocity for all labels
func ComputeAllHistoricalVelocity(issues []model.Issue, numWeeks int, now time.Time) map[string]HistoricalVelocity {
	labels := ExtractLabels(issues)
	result := make(map[string]HistoricalVelocity, labels.LabelCount)

	for _, label := range labels.Labels {
		result[label] = ComputeHistoricalVelocity(issues, label, numWeeks, now)
	}

	return result
}

// GetVelocityTrend analyzes the historical velocity to detect trends
// Returns "accelerating", "decelerating", "stable", or "erratic"
func (hv *HistoricalVelocity) GetVelocityTrend() string {
	n := len(hv.WeeklyVelocity)
	if n < 4 {
		return "insufficient_data"
	}

	// Compare first half vs second half of the period
	halfPoint := n / 2
	var recentSum, olderSum int

	for i := 0; i < halfPoint; i++ {
		recentSum += hv.WeeklyVelocity[i].Closed
	}
	for i := halfPoint; i < n; i++ {
		olderSum += hv.WeeklyVelocity[i].Closed
	}

	if olderSum == 0 && recentSum > 0 {
		return "accelerating"
	}
	if olderSum == 0 && recentSum == 0 {
		return "stable"
	}

	ratio := float64(recentSum) / float64(olderSum)

	switch {
	case ratio > 1.3:
		return "accelerating"
	case ratio < 0.7:
		return "decelerating"
	case hv.Variance > float64(hv.PeakVelocity)*0.5:
		return "erratic"
	default:
		return "stable"
	}
}

// GetWeeklyAverage returns the average closures per week
func (hv *HistoricalVelocity) GetWeeklyAverage() float64 {
	if hv.WeeksAnalyzed == 0 {
		return 0
	}
	var total int
	for _, snap := range hv.WeeklyVelocity {
		total += snap.Closed
	}
	return float64(total) / float64(hv.WeeksAnalyzed)
}
