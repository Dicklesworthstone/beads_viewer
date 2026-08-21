package analysis

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func isClosedLikeStatus(status model.Status) bool {
	return status == model.StatusClosed || status == model.StatusTombstone
}

// TriageResult is the unified output for --robot-triage
// Designed as a single entry point for AI agents to get everything they need
type TriageResult struct {
	Meta            TriageMeta       `json:"meta"`
	Status          MetricStatus     `json:"status,omitempty"`
	QuickRef        QuickRef         `json:"quick_ref"`
	Recommendations []Recommendation `json:"recommendations"`
	QuickWins       []QuickWin       `json:"quick_wins"`
	BlockersToClear []BlockerItem    `json:"blockers_to_clear"`
	ProjectHealth   ProjectHealth    `json:"project_health"`
	Alerts          []Alert          `json:"alerts,omitempty"`
	Commands        CommandHelpers   `json:"commands"`

	// bv-87: Track/label-aware groupings for multi-agent coordination
	// These allow multiple agents to grab their own top-N without collision
	RecommendationsByTrack []TrackRecommendationGroup `json:"recommendations_by_track,omitempty"`
	RecommendationsByLabel []LabelRecommendationGroup `json:"recommendations_by_label,omitempty"`
}

// TriageMeta contains metadata about the triage computation
type TriageMeta struct {
	Version       string    `json:"version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Phase2Ready   bool      `json:"phase2_ready"`
	IssueCount    int       `json:"issue_count"`
	ComputeTimeMs int64     `json:"compute_time_ms"`
	// HistoryStatus reports the outcome of the optional git-history
	// correlation prologue used by --robot-triage (issue #166):
	// "ok" (history report generated), "error" (generation failed),
	// "timeout" (generation exceeded the configured budget and was
	// cancelled; triage proceeded without history), or empty when history
	// generation was not attempted (no git repo / no open issues / callers
	// outside the robot-triage path).
	HistoryStatus string `json:"history_status,omitempty"`
}

// QuickRef provides at-a-glance summary for fast decisions.
//
// Count semantics (issue #165): open_count and blocked_count are STRICT
// status counts and always equal project_health.counts.by_status["open"] and
// by_status["blocked"]. The legacy aggregate values (every non-closed issue,
// and every non-closed-but-not-actionable issue) moved to not_closed_count
// and not_actionable_count. Partition invariant:
//
//	not_closed_count == actionable_count + not_actionable_count
type QuickRef struct {
	// OpenCount counts issues whose status is exactly "open"
	// (== by_status["open"]; excludes in_progress/blocked/deferred).
	OpenCount int `json:"open_count"`
	// ActionableCount counts non-closed issues that are ready to work on
	// (no open blocking dependencies).
	ActionableCount int `json:"actionable_count"`
	// BlockedCount counts issues whose status is exactly "blocked"
	// (== by_status["blocked"]). For dependency-blocked work regardless of
	// status, see NotActionableCount.
	BlockedCount int `json:"blocked_count"`
	// InProgressCount counts issues whose status is exactly "in_progress".
	InProgressCount int `json:"in_progress_count"`
	// NotClosedCount counts every issue that is not closed-like (status is
	// not "closed"/"tombstone"). This carries the pre-#165 open_count
	// semantics.
	NotClosedCount int `json:"not_closed_count"`
	// NotActionableCount counts non-closed issues that are NOT actionable
	// (blocked by open dependencies, whatever their status). This carries
	// the pre-#165 blocked_count semantics.
	NotActionableCount int       `json:"not_actionable_count"`
	TopPicks           []TopPick `json:"top_picks"` // Top 3 recommended items
}

// TopPick is a condensed recommendation for quick reference
type TopPick struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
	Unblocks int      `json:"unblocks"` // How many items this unblocks
}

// Recommendation is an actionable item with full context
type Recommendation struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	Assignee    string         `json:"assignee,omitempty"`
	Priority    int            `json:"priority"`
	Labels      []string       `json:"labels"`
	DeferUntil  *time.Time     `json:"defer_until,omitempty"`
	Score       float64        `json:"score"`
	Breakdown   ScoreBreakdown `json:"breakdown"`
	Action      string         `json:"action"` // Suggested next action (human-readable)
	Reasons     []string       `json:"reasons"`
	UnblocksIDs []string       `json:"unblocks_ids,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
}

// QuickWin represents a low-effort, high-impact item
type QuickWin struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Score       float64  `json:"score"`
	Reason      string   `json:"reason"`
	UnblocksIDs []string `json:"unblocks_ids,omitempty"`
}

// BlockerItem represents an item that blocks significant downstream work
type BlockerItem struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	UnblocksCount int      `json:"unblocks_count"`
	UnblocksIDs   []string `json:"unblocks_ids"`
	Actionable    bool     `json:"actionable"` // Can we work on this now?
	BlockedBy     []string `json:"blocked_by,omitempty"`
}

// ProjectHealth provides overall project status
type ProjectHealth struct {
	Counts    HealthCounts `json:"counts"`
	Graph     GraphHealth  `json:"graph"`
	Velocity  *Velocity    `json:"velocity,omitempty"`  // nil until labels view ready
	Staleness *Staleness   `json:"staleness,omitempty"` // nil until history ready
}

// HealthCounts is basic issue statistics.
//
// Count semantics (issue #165): Open and Blocked are STRICT status counts
// (always equal to ByStatus["open"] / ByStatus["blocked"]). The pre-#165
// aggregate values are preserved under the semantically accurate names
// NotClosed and DependencyBlocked. Partition invariant:
//
//	not_closed == actionable + dependency_blocked
type HealthCounts struct {
	// Total counts every issue, including closed and tombstone.
	Total int `json:"total"`
	// Open counts issues whose status is exactly "open" (== ByStatus["open"]).
	// Before #165 this counted every non-closed issue; that value is now
	// NotClosed.
	Open int `json:"open"`
	// Closed counts closed-like issues (status "closed" or "tombstone").
	Closed int `json:"closed"`
	// Blocked counts issues whose status is exactly "blocked"
	// (== ByStatus["blocked"]). Before #165 this counted non-closed issues
	// that were not actionable; that value is now DependencyBlocked.
	Blocked int `json:"blocked"`
	// Actionable counts non-closed issues with no open blocking
	// dependencies (ready to work on now).
	Actionable int `json:"actionable"`
	// NotClosed counts every issue that is not closed-like (status is not
	// "closed"/"tombstone"): open + in_progress + blocked + deferred + any
	// other live status. Pre-#165 "open" semantics.
	NotClosed int `json:"not_closed"`
	// DependencyBlocked counts non-closed issues that are NOT actionable
	// (blocked by open dependencies, whatever their status). Pre-#165
	// "blocked" semantics.
	DependencyBlocked int            `json:"dependency_blocked"`
	ByStatus          map[string]int `json:"by_status"`
	ByType            map[string]int `json:"by_type"`
	ByPriority        map[int]int    `json:"by_priority"`
}

// GraphHealth summarizes dependency graph metrics
type GraphHealth struct {
	NodeCount   int     `json:"node_count"`
	EdgeCount   int     `json:"edge_count"`
	Density     float64 `json:"density"`
	HasCycles   bool    `json:"has_cycles"`
	CycleCount  int     `json:"cycle_count,omitempty"`
	Phase2Ready bool    `json:"phase2_ready"`
}

// Velocity tracks work completion rate (future: from labels view)
type Velocity struct {
	ClosedLast7Days  int            `json:"closed_last_7_days"`
	ClosedLast30Days int            `json:"closed_last_30_days"`
	AvgDaysToClose   float64        `json:"avg_days_to_close"`
	Weekly           []VelocityWeek `json:"weekly,omitempty"`    // Buckets of closed issues per ISO week
	Estimated        bool           `json:"estimated,omitempty"` // True when computed from current snapshot only
}

// VelocityWeek captures closure count for a single week (UTC-based).
type VelocityWeek struct {
	WeekStart time.Time `json:"week_start"`
	Closed    int       `json:"closed"`
}

// ComputeProjectVelocity rolls up closure velocity for the whole project.
// It looks back `weeks` ISO weeks (default 8) using closed_at timestamps when
// available; if missing, it marks the result as estimated.
//
// This is the canonical velocity computation used by triage. It returns:
//   - ClosedLast7Days: issues closed in the last 7 days
//   - ClosedLast30Days: issues closed in the last 30 days
//   - AvgDaysToClose: average time from creation to closure
//   - Weekly: per-week closure counts (newest first)
//   - Estimated: true if any closure dates were approximated
//
// Use a fixed `now` for deterministic/testable results.
func ComputeProjectVelocity(issues []model.Issue, now time.Time, weeks int) *Velocity {
	if weeks <= 0 {
		weeks = 8
	}

	// Group closures by ISO week starting Monday.
	weekBuckets := make(map[time.Time]int)
	closedLast7, closedLast30 := 0, 0
	var totalCloseDur time.Duration
	var closeSamples int
	estimated := false

	weekAgo := now.Add(-7 * 24 * time.Hour)
	monthAgo := now.Add(-30 * 24 * time.Hour)

	for _, iss := range issues {
		if iss.Status != model.StatusClosed {
			continue
		}

		var closedAt time.Time
		switch {
		case iss.ClosedAt != nil:
			closedAt = iss.ClosedAt.UTC()
		case !iss.UpdatedAt.IsZero():
			// Fallback: approximate closure using updated_at when closed_at missing
			closedAt = iss.UpdatedAt.UTC()
			estimated = true
		default:
			// Last resort: approximate with now; counts become estimated
			closedAt = now
			estimated = true
		}

		// Count rolling windows
		if !closedAt.Before(weekAgo) {
			closedLast7++
		}
		if !closedAt.Before(monthAgo) {
			closedLast30++
		}

		// Bucket by ISO week
		year, week := closedAt.ISOWeek()
		// Reconstruct the Monday of that ISO week
		weekStart := isoWeekStart(year, week)
		weekBuckets[weekStart]++

		// Average time-to-close if created date present
		if !iss.CreatedAt.IsZero() {
			totalCloseDur += closedAt.Sub(iss.CreatedAt)
			closeSamples++
		}
	}

	// Build ordered weekly slices (newest first)
	weekly := make([]VelocityWeek, 0, weeks)
	cursor := truncateToMonday(now)
	for i := 0; i < weeks; i++ {
		count := weekBuckets[cursor]
		weekly = append(weekly, VelocityWeek{
			WeekStart: cursor,
			Closed:    count,
		})
		cursor = cursor.Add(-7 * 24 * time.Hour)
	}

	avgDays := 0.0
	if closeSamples > 0 {
		avgDays = totalCloseDur.Hours() / 24.0 / float64(closeSamples)
	}

	return &Velocity{
		ClosedLast7Days:  closedLast7,
		ClosedLast30Days: closedLast30,
		AvgDaysToClose:   avgDays,
		Weekly:           weekly,
		Estimated:        estimated,
	}
}

// isoWeekStart returns the Monday (00:00 UTC) for the given ISO year/week.
func isoWeekStart(year, isoWeek int) time.Time {
	// Start from Jan 4th which is always in week 1, then move to requested week.
	t := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	_, isoW := t.ISOWeek()
	// Shift to Monday of that week (t currently Jan 4). Weekday can be Sunday -> negative.
	offset := int(time.Monday - t.Weekday())
	if offset > 0 { // Sunday should go back 6 days, not forward
		offset -= 7
	}
	t = t.AddDate(0, 0, offset)
	weekDelta := isoWeek - isoW
	return t.AddDate(0, 0, weekDelta*7)
}

// truncateToMonday normalizes a time to Monday 00:00 UTC of its ISO week.
func truncateToMonday(t time.Time) time.Time {
	t = t.UTC()
	offset := int(time.Monday - t.Weekday())
	if offset > 0 { // Sunday adjustment
		offset -= 7
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

// Staleness tracks stale issues (future: from history)
type Staleness struct {
	StaleCount       int    `json:"stale_count"` // Issues with no activity > threshold
	StalestIssueID   string `json:"stalest_issue_id"`
	StalestIssueDays int    `json:"stalest_issue_days"`
	ThresholdDays    int    `json:"threshold_days"`
}

// Alert represents a proactive warning (future: from alerts engine)
type Alert struct {
	Type     string   `json:"type"`     // "stale", "velocity_drop", "cycle", "duplicate"
	Severity string   `json:"severity"` // "info", "warning", "error"
	Message  string   `json:"message"`
	IssueID  string   `json:"issue_id,omitempty"`
	IssueIDs []string `json:"issue_ids,omitempty"`
}

// CommandHelpers provides copy-paste commands for common actions
type CommandHelpers struct {
	ClaimTop      string `json:"claim_top"`      // CI=1 br update <id> --status in_progress --json
	ShowTop       string `json:"show_top"`       // CI=1 br show <id> --json
	ListReady     string `json:"list_ready"`     // CI=1 br ready --json
	ListBlocked   string `json:"list_blocked"`   // CI=1 br blocked --json
	RefreshTriage string `json:"refresh_triage"` // bv --robot-triage
}

// ComputeTriage generates a unified triage result from issues
func ComputeTriage(issues []model.Issue) TriageResult {
	return ComputeTriageWithOptions(issues, TriageOptions{})
}

// TriageOptions configures triage computation
type TriageOptions struct {
	TopN          int  // Number of recommendations (default 10)
	QuickWinN     int  // Number of quick wins (default 5)
	BlockerN      int  // Number of blockers to show (default 5)
	WaitForPhase2 bool // Block until Phase 2 metrics ready
	UseFastConfig bool // Use TriageConfig for faster Phase 2 (bv-t1js optimization)

	// bv-87: Track/label-aware recommendation grouping for multi-agent coordination
	GroupByTrack bool // Group recommendations by execution track (connected component)
	GroupByLabel bool // Group recommendations by primary label

	// bv-140: Scope triage to subgraph rooted at a specific epic/issue
	RootIssueID string // If set, filter issues to the subgraph rooted at this ID

	// History report for staleness analysis
	History *correlation.HistoryReport

	// SeedDataHash, when non-empty, is a pre-computed ComputeDataHash(issues)
	// the caller has already calculated for the same issue set. It is used to
	// seed the analyzer's disk-cache key so the identical SHA256 is not run
	// again. It is ignored when RootIssueID scopes the issue set (the seed would
	// then describe a different slice).
	SeedDataHash string

	// NotReadyLabels (issue #173) is an opt-in label-class that marks a bead as
	// graph-ready but NOT work-ready. Any OPEN, unblocked, unassigned, non-epic
	// leaf carrying one of these labels (case-insensitive) is excluded from the
	// claimable top picks fed to --robot-next / --robot-triage, so an agent is
	// never handed a deliberately-not-ready bead as its next claim.
	//
	// Beads labels are free-form, so there is no canonical not-ready label to
	// hardcode; this defaults empty (zero behavior change unless opted in via
	// --robot-not-ready-labels or BV_ROBOT_NOT_READY_LABELS). It complements the
	// existing not-ready *status* path (draft/deferred/review are already
	// excluded because claimability requires status=="open"); this covers
	// workflows that keep a bead open to stay visible but signal not-ready with
	// a label.
	NotReadyLabels []string
}

// TrackRecommendationGroup groups recommendations by execution track (bv-87)
type TrackRecommendationGroup struct {
	TrackID         string           `json:"track_id"`
	Reason          string           `json:"reason"`                  // Why these are grouped (e.g., "Independent work stream")
	Recommendations []Recommendation `json:"recommendations"`         // Recommendations in this track
	TopPick         *TopPick         `json:"top_pick,omitempty"`      // Best item in this track
	ClaimCommand    string           `json:"claim_command,omitempty"` // CI=1 br update <top_pick_id> --status in_progress --json
	TotalUnblocks   int              `json:"total_unblocks"`          // Sum of unblocks in this track
}

// LabelRecommendationGroup groups recommendations by label (bv-87)
type LabelRecommendationGroup struct {
	Label           string           `json:"label"`
	Recommendations []Recommendation `json:"recommendations"`         // Recommendations with this label
	TopPick         *TopPick         `json:"top_pick,omitempty"`      // Best item with this label
	ClaimCommand    string           `json:"claim_command,omitempty"` // CI=1 br update <top_pick_id> --status in_progress --json
	TotalUnblocks   int              `json:"total_unblocks"`          // Sum of unblocks for this label
}

// ComputeTriageWithOptions generates triage with custom options
func ComputeTriageWithOptions(issues []model.Issue, opts TriageOptions) TriageResult {
	return ComputeTriageWithOptionsAndTime(issues, opts, time.Now())
}

// ComputeTriageWithOptionsAndTime generates triage with a deterministic clock (testing).
func ComputeTriageWithOptionsAndTime(issues []model.Issue, opts TriageOptions, now time.Time) TriageResult {
	// bv-140: If a root issue is specified, filter to the subgraph rooted at that issue.
	// "Rooted at" means the root itself plus all issues that transitively depend on it
	// (descendants in the dependency DAG). This scopes triage to a specific epic.
	if opts.RootIssueID != "" {
		issues = extractDescendantSubgraph(issues, opts.RootIssueID)
	}

	// Build analyzer and stats
	analyzer := NewAnalyzer(issues)
	// Reuse a caller-supplied data hash when it still describes this exact issue
	// set (i.e. no root-subgraph scoping happened above).
	if opts.SeedDataHash != "" && opts.RootIssueID == "" {
		analyzer.SeedDataHash(opts.SeedDataHash)
	}

	// bv-perf: Check if there are any open issues before computing Phase 2
	// Phase 2 metrics (PageRank, Betweenness) are only used for scoring open issues.
	// If all issues are closed, we can skip Phase 2 entirely.
	hasOpenIssues := false
	for _, issue := range issues {
		if !isClosedLikeStatus(issue.Status) {
			hasOpenIssues = true
			break
		}
	}

	// Use fast config for triage-only analysis (bv-t1js optimization)
	// bv-perf: When no open issues, use NoPhase2Config to skip the goroutine entirely.
	var stats *GraphStats
	if !hasOpenIssues {
		// All issues closed - skip Phase 2 completely (no scoring needed)
		stats = analyzer.AnalyzeAsyncWithConfig(context.Background(), NoPhase2Config())
	} else if opts.UseFastConfig {
		stats = analyzer.AnalyzeAsyncWithConfig(context.Background(), TriageConfig())
	} else {
		stats = analyzer.AnalyzeAsync(context.Background())
	}

	// Triage requires advanced metrics (PageRank, etc.) for scoring.
	// If requested, wait for Phase 2 to complete - but only if there are open issues.
	if opts.WaitForPhase2 && hasOpenIssues {
		stats.WaitForPhase2()
	}

	return ComputeTriageFromAnalyzer(analyzer, stats, issues, opts, now)
}

// extractDescendantSubgraph returns the subgraph rooted at rootID: the root
// itself plus all issues that transitively depend on it (its descendants).
// This is the reverse of following DependsOnID — we build an adjacency from
// blocker -> dependents and BFS downward.
func extractDescendantSubgraph(issues []model.Issue, rootID string) []model.Issue {
	// Build reverse adjacency: parentID -> []childIDs
	// An issue with DependsOnID=X means X is the parent/blocker, so X -> issue.ID
	children := make(map[string][]string, len(issues))
	issueMap := make(map[string]struct{}, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = struct{}{}
		for _, dep := range iss.Dependencies {
			if dep != nil {
				children[dep.DependsOnID] = append(children[dep.DependsOnID], iss.ID)
			}
		}
	}

	// Verify root exists
	if _, ok := issueMap[rootID]; !ok {
		return nil
	}

	// BFS from root through reverse edges (parent -> children)
	visited := make(map[string]bool, len(issues))
	queue := []string{rootID}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr] {
			continue
		}
		visited[curr] = true
		for _, childID := range children[curr] {
			if !visited[childID] {
				queue = append(queue, childID)
			}
		}
	}

	// Collect visited issues preserving original order
	result := make([]model.Issue, 0, len(visited))
	for _, iss := range issues {
		if visited[iss.ID] {
			result = append(result, iss)
		}
	}
	return result
}

// ComputeTriageFromAnalyzer generates triage reusing an existing analyzer and stats.
// This is the canonical implementation used by all triage entrypoints.
//
// Use this function when you already have an Analyzer and GraphStats instance
// (e.g., TUI startup where analysis was already performed). This avoids the
// O(V+E) cost of rebuilding the dependency graph.
//
// Parameters:
//   - analyzer: Pre-built Analyzer with issue graph loaded
//   - stats: GraphStats from analyzer.Analyze() or analyzer.AnalyzeAsync()
//   - issues: The issue slice (needed for counts and iteration)
//   - opts: Triage configuration options
//   - now: Reference time for scoring calculations (use fixed value for testing)
//
// The outputs match ComputeTriageWithOptionsAndTime given equivalent inputs.
func ComputeTriageFromAnalyzer(analyzer *Analyzer, stats *GraphStats, issues []model.Issue, opts TriageOptions, now time.Time) TriageResult {
	start := time.Now()

	// Set defaults
	if opts.TopN <= 0 {
		opts.TopN = 10
	}
	if opts.QuickWinN <= 0 {
		opts.QuickWinN = 5
	}
	if opts.BlockerN <= 0 {
		opts.BlockerN = 5
	}

	// Create TriageContext for unified caching (bv-oko3)
	// This caches actionable issues, blocker depths, etc. across all sub-functions
	triageCtx := NewTriageContext(analyzer)

	// Get unblocks map from context (bv-wjs0)
	unblocksMap := triageCtx.UnblocksMap()

	// Compute impact scores using the already-computed stats.
	impactScores := analyzer.ComputeImpactScoresFromStats(stats, now)

	// Compute counts (uses cached actionable issues)
	counts := computeCountsWithContext(issues, triageCtx)

	// Compute enhanced triage scores (bv-147)
	triageScores := computeTriageScoresFromImpact(impactScores, triageCtx, DefaultTriageScoringOptions())

	// Build recommendations using enhanced scores (bv-148).
	// Top picks need to search the *full* scored set, not the
	// length-capped recommendations slice — otherwise, when the top
	// opts.TopN scored items happen to all be blocked, buildTopPicks
	// filters them out and returns an empty list. `bv --robot-next`
	// then reports "no actionable work" even though plenty exists
	// further down the score list (issue #146 / PR #147).
	//
	// So: build the recommendations against the full triageScores set
	// first, slice to opts.TopN for the user-visible recommendations
	// list, and feed the *unsliced* set into buildTopPicks.
	allRecommendations := buildRecommendationsFromTriageScores(triageScores, triageCtx, len(triageScores), now)
	recommendations := allRecommendations
	if len(recommendations) > opts.TopN {
		recommendations = recommendations[:opts.TopN]
	}

	// Build quick wins
	quickWins := buildQuickWins(impactScores, unblocksMap, opts.QuickWinN)

	// Build blockers to clear (uses cached actionable issues)
	blockersToClear := buildBlockersToClearWithContext(triageCtx, unblocksMap, opts.BlockerN)

	// Parents with open children are excluded from claimable top picks (issue
	// #17 parity): such a parent is a planning container, not directly claimable
	// work, even when it passes the epic-type / blocker checks (e.g. a non-epic
	// parent). The parent re-becomes claimable once its children are all closed.
	parentsWithOpenChildren := analyzer.ParentsWithOpenChildren()

	// Build top picks for quick ref. Pass the full set so blocked
	// high-priority items don't crowd genuine actionable work out of
	// the picks (issue #146).
	topPicks := buildTopPicks(allRecommendations, 3, now, opts.NotReadyLabels, parentsWithOpenChildren)

	// Determine top issue for commands
	topID := ""
	if len(topPicks) > 0 {
		topID = topPicks[0].ID
	}

	elapsed := time.Since(start)
	projectVelocity := ComputeProjectVelocity(issues, now.UTC(), 8)

	// bv-87: Build grouped recommendations if requested
	var recsByTrack []TrackRecommendationGroup
	var recsByLabel []LabelRecommendationGroup
	if opts.GroupByTrack {
		recsByTrack = buildRecommendationsByTrack(recommendations, analyzer, unblocksMap, now, opts.NotReadyLabels, parentsWithOpenChildren)
	}
	if opts.GroupByLabel {
		recsByLabel = buildRecommendationsByLabel(recommendations, unblocksMap, now, opts.NotReadyLabels, parentsWithOpenChildren)
	}

	// Calculate staleness if history is available
	var staleness *Staleness
	if opts.History != nil {
		staleness = ComputeStaleness(opts.History, issues, now)
	}

	return TriageResult{
		Meta: TriageMeta{
			Version:       "1.0.0",
			GeneratedAt:   now,
			Phase2Ready:   stats.IsPhase2Ready(),
			IssueCount:    len(issues),
			ComputeTimeMs: elapsed.Milliseconds(),
		},
		Status: stats.Status(),
		QuickRef: QuickRef{
			OpenCount:          counts.Open,
			ActionableCount:    counts.Actionable,
			BlockedCount:       counts.Blocked,
			InProgressCount:    counts.ByStatus["in_progress"],
			NotClosedCount:     counts.NotClosed,
			NotActionableCount: counts.DependencyBlocked,
			TopPicks:           topPicks,
		},
		Recommendations:        recommendations,
		QuickWins:              quickWins,
		BlockersToClear:        blockersToClear,
		RecommendationsByTrack: recsByTrack,
		RecommendationsByLabel: recsByLabel,
		ProjectHealth: ProjectHealth{
			Counts:    counts,
			Graph:     buildGraphHealth(stats),
			Velocity:  projectVelocity,
			Staleness: staleness,
		},
		Commands: buildCommands(topID),
	}
}

// ComputeStaleness calculates staleness metrics from history
func ComputeStaleness(history *correlation.HistoryReport, issues []model.Issue, now time.Time) *Staleness {
	const thresholdDays = 14
	threshold := now.AddDate(0, 0, -thresholdDays)
	staleCount := 0
	maxDays := 0
	stalestID := ""

	for _, issue := range issues {
		if issue.Status == model.StatusClosed || issue.Status == model.StatusTombstone {
			continue
		}

		// Use history events to find true last activity
		var lastActivity time.Time
		if h, ok := history.Histories[issue.ID]; ok {
			if len(h.Events) > 0 {
				lastActivity = h.Events[len(h.Events)-1].Timestamp
			}
			if len(h.Commits) > 0 {
				lastCommit := h.Commits[len(h.Commits)-1].Timestamp
				if lastCommit.After(lastActivity) {
					lastActivity = lastCommit
				}
			}
		}

		// Fallback to issue UpdatedAt if history missing
		if lastActivity.IsZero() {
			lastActivity = issue.UpdatedAt
		}

		if lastActivity.Before(threshold) {
			staleCount++
			days := int(now.Sub(lastActivity).Hours() / 24)
			if days > maxDays {
				maxDays = days
				stalestID = issue.ID
			}
		}
	}

	if staleCount == 0 {
		return nil
	}

	return &Staleness{
		StaleCount:       staleCount,
		StalestIssueID:   stalestID,
		StalestIssueDays: maxDays,
		ThresholdDays:    thresholdDays,
	}
}

// buildUnblocksMap computes what each issue unblocks
func buildUnblocksMap(ctx *TriageContext) map[string][]string {
	unblocksMap := make(map[string][]string)

	// We only care about blocking dependencies
	for _, issue := range ctx.analyzer.issueMap {
		if isClosedLikeStatus(issue.Status) {
			continue
		}

		if _, ok := unblocksMap[issue.ID]; !ok {
			unblocksMap[issue.ID] = nil
		}

		// Get all open blockers for this issue (including parents)
		openBlockers := ctx.getOpenBlockersInternal(issue.ID)

		// If this issue is blocked by exactly ONE issue, completing that
		// one issue will unblock this one.
		if len(openBlockers) == 1 {
			blockerID := openBlockers[0]
			unblocksMap[blockerID] = append(unblocksMap[blockerID], issue.ID)
		}
	}

	// Sort for determinism
	for _, unblocks := range unblocksMap {
		sort.Strings(unblocks)
	}

	return unblocksMap
}

// computeCounts tallies issues by various dimensions
// Deprecated: Use computeCountsWithContext for better performance via caching.
func computeCounts(issues []model.Issue, analyzer *Analyzer) HealthCounts {
	counts := HealthCounts{
		Total:      len(issues),
		ByStatus:   make(map[string]int),
		ByType:     make(map[string]int),
		ByPriority: make(map[int]int),
	}

	actionable := analyzer.GetActionableIssues()
	actionableSet := make(map[string]bool, len(actionable))
	for _, a := range actionable {
		actionableSet[a.ID] = true
	}

	for _, issue := range issues {
		counts.ByStatus[string(issue.Status)]++
		counts.ByType[string(issue.IssueType)]++
		counts.ByPriority[issue.Priority]++

		if isClosedLikeStatus(issue.Status) {
			counts.Closed++
		} else {
			counts.NotClosed++
			if actionableSet[issue.ID] {
				counts.Actionable++
			} else {
				counts.DependencyBlocked++
			}
		}
	}

	// Strict status counts (issue #165): keep Open/Blocked in lockstep with
	// ByStatus so counts.open == by_status.open always holds.
	counts.Open = counts.ByStatus[string(model.StatusOpen)]
	counts.Blocked = counts.ByStatus[string(model.StatusBlocked)]

	return counts
}

// computeCountsWithContext tallies issues by various dimensions using cached actionable data.
// This is more efficient than computeCounts when called multiple times in the same triage pass.
func computeCountsWithContext(issues []model.Issue, ctx *TriageContext) HealthCounts {
	counts := HealthCounts{
		Total:      len(issues),
		ByStatus:   make(map[string]int),
		ByType:     make(map[string]int),
		ByPriority: make(map[int]int),
	}

	for _, issue := range issues {
		counts.ByStatus[string(issue.Status)]++
		counts.ByType[string(issue.IssueType)]++
		counts.ByPriority[issue.Priority]++

		if isClosedLikeStatus(issue.Status) {
			counts.Closed++
		} else {
			counts.NotClosed++
			// Use cached IsActionable lookup - O(1) after first computation
			if ctx.IsActionable(issue.ID) {
				counts.Actionable++
			} else {
				counts.DependencyBlocked++
			}
		}
	}

	// Strict status counts (issue #165): keep Open/Blocked in lockstep with
	// ByStatus so counts.open == by_status.open always holds.
	counts.Open = counts.ByStatus[string(model.StatusOpen)]
	counts.Blocked = counts.ByStatus[string(model.StatusBlocked)]

	return counts
}

// buildRecommendationsFromTriageScores creates recommendations using enhanced triage scores.
// Uses TriageContext for cached blocker lookups (bv-k4az optimization).
func buildRecommendationsFromTriageScores(scores []TriageScore, ctx *TriageContext, limit int, now time.Time) []Recommendation {
	if len(scores) > limit {
		scores = scores[:limit]
	}

	analyzer := ctx.Analyzer()
	unblocksMap := ctx.UnblocksMap()

	recommendations := make([]Recommendation, 0, len(scores))
	for _, score := range scores {
		issue := analyzer.GetIssue(score.IssueID)
		if issue == nil {
			continue
		}

		// Generate reasons using the new logic (cached via TriageContext)
		reasons := generateTriageReasonsForScoreAt(score, ctx, now)

		// Get blocked by (cached via TriageContext)
		blockedBy := ctx.OpenBlockers(score.IssueID)

		rec := Recommendation{
			ID:          score.IssueID,
			Title:       score.Title,
			Type:        string(issue.IssueType),
			Status:      score.Status,
			Assignee:    issue.Assignee,
			Priority:    score.Priority,
			Labels:      issue.Labels,
			DeferUntil:  issue.DeferUntil,
			Score:       score.TriageScore,
			Breakdown:   score.Breakdown,
			Action:      reasons.ActionHint,
			Reasons:     reasons.All,
			UnblocksIDs: unblocksMap[score.IssueID],
		}
		if len(blockedBy) > 0 {
			rec.BlockedBy = blockedBy
		}

		recommendations = append(recommendations, rec)
	}

	return recommendations
}

// buildQuickWins finds low-complexity, high-impact items
func buildQuickWins(scores []ImpactScore, unblocksMap map[string][]string, limit int) []QuickWin {
	// Quick wins: high score but likely simple (no deep dependency chains)
	// Heuristic: items that unblock others but have low blocker ratio themselves

	type candidate struct {
		score         ImpactScore
		unblocks      []string
		quickWinScore float64
	}

	candidates := make([]candidate, 0, len(scores))
	for _, score := range scores {
		unblocks := unblocksMap[score.IssueID]
		// Quick win score formula: Balance Impact vs Effort
		// 1. Unblocks Impact: Logarithmic scale to prevent domination by huge fan-outs
		//    log2(1)=0, log2(2)=1, log2(5)≈2.3, log2(10)≈3.3
		unblockImpact := math.Log2(float64(len(unblocks)) + 1)

		// 2. Simplicity Bonus (inverse complexity)
		//    If not a bottleneck (low BlockerRatio), it's simpler.
		simplicity := 0.0
		if score.Breakdown.BlockerRatioNorm < 0.2 {
			simplicity += 1.0
		} else if score.Breakdown.BlockerRatioNorm < 0.4 {
			simplicity += 0.5
		}

		// 3. Priority Bonus (P0/P1 are more urgent "wins")
		priorityBonus := 0.0
		if score.Priority <= 1 {
			priorityBonus = 0.5
		}

		// Combined Score
		// Impact * 0.4 + Simplicity * 0.4 + Priority * 0.2
		qwScore := (unblockImpact * 0.4) + (simplicity * 0.4) + (priorityBonus * 0.2)

		candidates = append(candidates, candidate{score, unblocks, qwScore})
	}

	// Sort by quick win score
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].quickWinScore != candidates[j].quickWinScore {
			return candidates[i].quickWinScore > candidates[j].quickWinScore
		}
		return candidates[i].score.IssueID < candidates[j].score.IssueID
	})

	quickWins := make([]QuickWin, 0, limit)
	for i := 0; i < len(candidates) && i < limit; i++ {
		c := candidates[i]
		reason := "Low complexity"
		if len(c.unblocks) > 0 {
			reason = fmt.Sprintf("Unblocks %d items", len(c.unblocks))
		}
		if c.score.Priority <= 1 {
			reason += ", high priority"
		}

		quickWins = append(quickWins, QuickWin{
			ID:          c.score.IssueID,
			Title:       c.score.Title,
			Score:       c.quickWinScore,
			Reason:      reason,
			UnblocksIDs: c.unblocks,
		})
	}

	return quickWins
}

// buildBlockersToClear finds items that block the most downstream work
// Deprecated: Use buildBlockersToClearWithContext for better performance via caching.
func buildBlockersToClear(analyzer *Analyzer, unblocksMap map[string][]string, limit int) []BlockerItem {
	type blocker struct {
		id       string
		title    string
		unblocks []string
	}

	actionable := analyzer.GetActionableIssues()
	actionableSet := make(map[string]bool, len(actionable))
	for _, a := range actionable {
		actionableSet[a.ID] = true
	}

	var blockers []blocker
	for id, unblocks := range unblocksMap {
		if len(unblocks) == 0 {
			continue
		}
		issue := analyzer.GetIssue(id)
		if issue == nil || isClosedLikeStatus(issue.Status) {
			continue
		}
		blockers = append(blockers, blocker{
			id:       id,
			title:    issue.Title,
			unblocks: unblocks,
		})
	}

	// Sort by unblocks count descending
	sort.Slice(blockers, func(i, j int) bool {
		if len(blockers[i].unblocks) != len(blockers[j].unblocks) {
			return len(blockers[i].unblocks) > len(blockers[j].unblocks)
		}
		// Stable tie-breaker for deterministic robot output.
		return blockers[i].id < blockers[j].id
	})

	result := make([]BlockerItem, 0, limit)
	for i := 0; i < len(blockers) && i < limit; i++ {
		b := blockers[i]
		item := BlockerItem{
			ID:            b.id,
			Title:         b.title,
			UnblocksCount: len(b.unblocks),
			UnblocksIDs:   b.unblocks,
			Actionable:    actionableSet[b.id],
		}
		if !item.Actionable {
			item.BlockedBy = analyzer.GetOpenBlockers(b.id)
		}
		result = append(result, item)
	}

	return result
}

// buildBlockersToClearWithContext finds items that block the most downstream work.
// This version uses TriageContext for cached actionable lookups and open blockers.
func buildBlockersToClearWithContext(ctx *TriageContext, unblocksMap map[string][]string, limit int) []BlockerItem {
	type blocker struct {
		id       string
		title    string
		unblocks []string
	}

	var blockers []blocker
	for id, unblocks := range unblocksMap {
		if len(unblocks) == 0 {
			continue
		}
		issue := ctx.GetIssue(id)
		if issue == nil || isClosedLikeStatus(issue.Status) {
			continue
		}
		blockers = append(blockers, blocker{
			id:       id,
			title:    issue.Title,
			unblocks: unblocks,
		})
	}

	// Sort by unblocks count descending
	sort.Slice(blockers, func(i, j int) bool {
		if len(blockers[i].unblocks) != len(blockers[j].unblocks) {
			return len(blockers[i].unblocks) > len(blockers[j].unblocks)
		}
		// Stable tie-breaker for deterministic robot output.
		return blockers[i].id < blockers[j].id
	})

	result := make([]BlockerItem, 0, limit)
	for i := 0; i < len(blockers) && i < limit; i++ {
		b := blockers[i]
		item := BlockerItem{
			ID:            b.id,
			Title:         b.title,
			UnblocksCount: len(b.unblocks),
			UnblocksIDs:   b.unblocks,
			Actionable:    ctx.IsActionable(b.id), // Cached O(1) lookup
		}
		if !item.Actionable {
			item.BlockedBy = ctx.OpenBlockers(b.id) // Cached O(1) lookup
		}
		result = append(result, item)
	}

	return result
}

// buildTopPicks creates condensed top picks from recommendations.
// Only includes actionable (non-blocked) items since TopPicks are used
// for "what should I work on next" queries (e.g., --robot-next).
func buildTopPicks(recommendations []Recommendation, limit int, now time.Time, notReadyLabels []string, parentsWithOpenChildren map[string]bool) []TopPick {
	picks := make([]TopPick, 0, limit)
	for _, rec := range recommendations {
		if !isClaimableRecommendation(rec, now, notReadyLabels, parentsWithOpenChildren) {
			continue
		}
		picks = append(picks, TopPick{
			ID:       rec.ID,
			Title:    rec.Title,
			Score:    rec.Score,
			Reasons:  rec.Reasons,
			Unblocks: len(rec.UnblocksIDs),
		})
		if len(picks) >= limit {
			break
		}
	}

	return picks
}

// isClaimableRecommendation reports whether a recommendation is directly
// claimable as a robot top pick.
//
// A bead is claimable iff it is open, not an epic, unassigned, has no open
// blockers, (issue #173) carries none of the opt-in not-ready labels, and (for
// parity with the Rust viewer's #17 fix) is not a parent that still has open
// child work. The parent check is type-agnostic, so it also withholds a
// non-epic parent with open children — which the epic-type gate alone misses —
// while never withholding a genuinely-leaf epic.
//
// notReadyLabels may be empty (the default), in which case the label gate is a
// no-op. parentsWithOpenChildren may be nil, in which case the parent gate is
// skipped (used by unit tests that exercise the predicate in isolation).
func isClaimableRecommendation(rec Recommendation, now time.Time, notReadyLabels []string, parentsWithOpenChildren map[string]bool) bool {
	if parentsWithOpenChildren[rec.ID] {
		return false
	}
	return rec.Status == string(model.StatusOpen) &&
		rec.Type != string(model.TypeEpic) &&
		rec.Assignee == "" &&
		len(rec.BlockedBy) == 0 &&
		(rec.DeferUntil == nil || !rec.DeferUntil.After(now)) &&
		!hasAnyLabel(rec.Labels, notReadyLabels)
}

// hasAnyLabel reports whether any of issueLabels matches a label in wanted,
// case-insensitively (and trimming surrounding whitespace). Returns false when
// wanted is empty, so an unconfigured not-ready set never excludes anything.
func hasAnyLabel(issueLabels, wanted []string) bool {
	if len(wanted) == 0 || len(issueLabels) == 0 {
		return false
	}
	for _, have := range issueLabels {
		haveNorm := strings.ToLower(strings.TrimSpace(have))
		for _, want := range wanted {
			if haveNorm == strings.ToLower(strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

// buildGraphHealth constructs graph health metrics from stats
func buildGraphHealth(stats *GraphStats) GraphHealth {
	// Call Cycles() once to avoid duplicate work (it makes a copy each time)
	cycles := stats.Cycles()
	cycleCount := 0
	if cycles != nil {
		cycleCount = len(cycles)
	}

	return GraphHealth{
		NodeCount:   stats.NodeCount,
		EdgeCount:   stats.EdgeCount,
		Density:     stats.Density,
		HasCycles:   cycleCount > 0,
		CycleCount:  cycleCount,
		Phase2Ready: stats.IsPhase2Ready(),
	}
}

// buildCommands constructs helper commands, handling empty topID gracefully
func buildCommands(topID string) CommandHelpers {
	base := "CI=1 "
	listReady := base + "br ready --json"
	listBlocked := base + "br blocked --json"

	claimTop := listReady + "  # No top pick available"
	showTop := listReady + "  # No top pick available"
	if topID != "" {
		claimTop = fmt.Sprintf("%sbr update %s --status in_progress --json", base, topID)
		showTop = fmt.Sprintf("%sbr show %s --json", base, topID)
	}

	return CommandHelpers{
		ClaimTop:      claimTop,
		ShowTop:       showTop,
		ListReady:     listReady,
		ListBlocked:   listBlocked,
		RefreshTriage: "bv --robot-triage",
	}
}

// ============================================================================
// Unified Triage Scoring (bv-147)
// Extends base impact scoring with triage-specific factors
// ============================================================================

// TriageScore represents a triage-specific score with factors applied
type TriageScore struct {
	IssueID        string         `json:"issue_id"`
	Title          string         `json:"title"`
	BaseScore      float64        `json:"base_score"`      // From ComputeImpactScores
	TriageScore    float64        `json:"triage_score"`    // Final triage-adjusted score
	Breakdown      ScoreBreakdown `json:"breakdown"`       // Original breakdown
	TriageFactors  TriageFactors  `json:"triage_factors"`  // Triage-specific factors
	FactorsApplied []string       `json:"factors_applied"` // Which factors were used
	FactorsPending []string       `json:"factors_pending"` // Which factors are not yet available
	Priority       int            `json:"priority"`
	Status         string         `json:"status"`
}

// TriageFactors holds the triage-specific score modifiers
type TriageFactors struct {
	UnblockBoost   float64 `json:"unblock_boost"`             // Boost for items that unblock many others
	QuickWinBoost  float64 `json:"quick_win_boost"`           // Boost for low-effort high-impact items
	LabelHealth    float64 `json:"label_health,omitempty"`    // Phase 2: Label health factor
	ClaimPenalty   float64 `json:"claim_penalty,omitempty"`   // Phase 3: Penalty for claimed items
	AttentionScore float64 `json:"attention_score,omitempty"` // Phase 4: Attention-weighted health
}

// TriageScoringOptions configures triage scoring behavior
type TriageScoringOptions struct {
	// Weight configuration
	BaseScoreWeight    float64 // Default 0.70
	UnblockBoostWeight float64 // Default 0.15
	QuickWinWeight     float64 // Default 0.15

	// Thresholds
	UnblockThreshold int // Min unblocks to get full boost (default 5)
	QuickWinMaxDepth int // Max dependency depth for quick win (default 2)

	// Feature flags (for graceful degradation)
	EnableLabelHealth    bool   // Phase 2 feature
	EnableClaimPenalty   bool   // Phase 3 feature
	EnableAttentionScore bool   // Phase 4 feature
	ClaimedByAgent       string // Current agent for claim penalty calculation
}

// DefaultTriageScoringOptions returns sensible defaults
func DefaultTriageScoringOptions() TriageScoringOptions {
	return TriageScoringOptions{
		BaseScoreWeight:    0.70,
		UnblockBoostWeight: 0.15,
		QuickWinWeight:     0.15,
		UnblockThreshold:   5,
		QuickWinMaxDepth:   2,
		// All optional features off by default (MVP mode)
		EnableLabelHealth:    false,
		EnableClaimPenalty:   false,
		EnableAttentionScore: false,
	}
}

// ComputeTriageScores calculates triage-optimized scores for all open issues
func ComputeTriageScores(issues []model.Issue) []TriageScore {
	return ComputeTriageScoresWithOptions(issues, DefaultTriageScoringOptions())
}

// ComputeTriageScoresWithOptions calculates triage scores with custom options
func ComputeTriageScoresWithOptions(issues []model.Issue, opts TriageScoringOptions) []TriageScore {
	if len(issues) == 0 {
		return nil
	}

	// Build analyzer for base scoring and graph analysis
	analyzer := NewAnalyzer(issues)
	baseScores := analyzer.ComputeImpactScores()

	triageCtx := NewTriageContext(analyzer)

	return computeTriageScoresFromImpact(baseScores, triageCtx, opts)
}

// computeTriageScoresFromImpact calculates triage scores from base impact scores
func computeTriageScoresFromImpact(baseScores []ImpactScore, triageCtx *TriageContext, opts TriageScoringOptions) []TriageScore {
	unblocksMap := triageCtx.UnblocksMap()

	// Calculate max unblocks for normalization
	maxUnblocks := 0
	for _, unblocks := range unblocksMap {
		if len(unblocks) > maxUnblocks {
			maxUnblocks = len(unblocks)
		}
	}

	// Build triage scores
	triageScores := make([]TriageScore, 0, len(baseScores))
	for _, base := range baseScores {
		ts := computeSingleTriageScore(base, unblocksMap, maxUnblocks, triageCtx.Analyzer(), opts, triageCtx.BlockerDepth(base.IssueID))
		triageScores = append(triageScores, ts)
	}

	// Sort by triage score descending
	sort.Slice(triageScores, func(i, j int) bool {
		if triageScores[i].TriageScore != triageScores[j].TriageScore {
			return triageScores[i].TriageScore > triageScores[j].TriageScore
		}
		return triageScores[i].IssueID < triageScores[j].IssueID
	})

	return triageScores
}

// computeSingleTriageScore calculates the triage score for a single issue
func computeSingleTriageScore(base ImpactScore, unblocksMap map[string][]string, maxUnblocks int, analyzer *Analyzer, opts TriageScoringOptions, blockerDepth int) TriageScore {
	factors := TriageFactors{}
	applied := []string{"base"}
	pending := []string{}

	// Calculate unblock boost
	unblocks := unblocksMap[base.IssueID]
	if len(unblocks) > 0 {
		// Normalize unblocks: items that unblock more get higher boost
		unblocksNorm := float64(len(unblocks)) / float64(maxOf(maxUnblocks, opts.UnblockThreshold))
		if unblocksNorm > 1.0 {
			unblocksNorm = 1.0
		}
		factors.UnblockBoost = unblocksNorm * opts.UnblockBoostWeight
		applied = append(applied, "unblock")
	}

	// Calculate quick-win boost
	// Quick wins are items with low blocker depth but high impact
	if issue := analyzer.GetIssue(base.IssueID); issue == nil || issue.Status != model.StatusInProgress {
		if blockerDepth <= opts.QuickWinMaxDepth && blockerDepth >= 0 {
			// Lower depth = higher quick win potential
			depthFactor := 1.0 - float64(blockerDepth)/float64(opts.QuickWinMaxDepth+1)
			// Combine with base score for impact consideration
			factors.QuickWinBoost = depthFactor * base.Score * opts.QuickWinWeight
			if factors.QuickWinBoost > opts.QuickWinWeight {
				factors.QuickWinBoost = opts.QuickWinWeight // Cap at max weight
			}
			applied = append(applied, "quick_win")
		}
	}

	// Track pending features
	if !opts.EnableLabelHealth {
		pending = append(pending, "label_health")
	}
	if !opts.EnableClaimPenalty {
		pending = append(pending, "claim_penalty")
	}
	if !opts.EnableAttentionScore {
		pending = append(pending, "attention_score")
	}

	// Calculate final triage score
	triageScore := base.Score*opts.BaseScoreWeight + factors.UnblockBoost + factors.QuickWinBoost

	// Future phases (when enabled):
	// Phase 2: triageScore += factors.LabelHealth * labelHealthWeight
	// Phase 3: if claimedByOther { triageScore *= 0.1 }
	// Phase 4: Replace label health with attention-weighted health

	return TriageScore{
		IssueID:        base.IssueID,
		Title:          base.Title,
		BaseScore:      base.Score,
		TriageScore:    triageScore,
		Breakdown:      base.Breakdown,
		TriageFactors:  factors,
		FactorsApplied: applied,
		FactorsPending: pending,
		Priority:       base.Priority,
		Status:         base.Status,
	}
}

// GetBlockerDepth returns the depth of the blocker chain for an issue
// Returns 0 if no blockers, 1 if blocked by one level, etc.
// Returns -1 if the issue is part of a cycle
func (a *Analyzer) GetBlockerDepth(issueID string) int {
	visited := make(map[string]bool)
	memo := make(map[string]int)
	return a.getBlockerDepthRecursive(issueID, visited, memo)
}

func (a *Analyzer) getBlockerDepthRecursive(issueID string, visited map[string]bool, memo map[string]int) int {
	if val, ok := memo[issueID]; ok {
		return val
	}
	if visited[issueID] {
		return -1 // Cycle detected
	}
	visited[issueID] = true

	blockers := a.GetOpenBlockers(issueID)
	if len(blockers) == 0 {
		visited[issueID] = false
		memo[issueID] = 0
		return 0
	}

	maxChain := 0
	for _, blockerID := range blockers {
		depth := a.getBlockerDepthRecursive(blockerID, visited, memo)
		if depth == -1 {
			visited[issueID] = false
			// Do not memoize cycle results to allow other paths to be checked?
			// Actually if a cycle is reachable, it's a cycle.
			return -1
		}
		if depth+1 > maxChain {
			maxChain = depth + 1
		}
	}

	visited[issueID] = false
	memo[issueID] = maxChain
	return maxChain
}

// maxOf returns the maximum of two integers
func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GetTopTriageScores returns the top N triage scores
func GetTopTriageScores(issues []model.Issue, n int) []TriageScore {
	scores := ComputeTriageScores(issues)
	if n > len(scores) {
		n = len(scores)
	}
	return scores[:n]
}

// ============================================================================
// Reason Generation (bv-148)
// Actionable, emoji-prefixed reasons for AI agents
// ============================================================================

// TriageReasonContext provides context for generating triage reasons
type TriageReasonContext struct {
	Issue           *model.Issue
	TriageScore     *TriageScore
	UnblocksIDs     []string
	BlockedByIDs    []string
	LabelHealth     map[string]int // Label -> health score (0-100)
	ClaimedByAgent  string         // Empty if unclaimed
	DaysSinceUpdate int
	IsQuickWin      bool
	BlockerDepth    int
}

// TriageReasons contains all generated reasons for an issue
type TriageReasons struct {
	Primary    string   `json:"primary"`     // Single most important reason
	All        []string `json:"all"`         // All reasons in priority order
	ActionHint string   `json:"action_hint"` // Suggested next action
}

// GenerateTriageReasons creates actionable reasons for a triage recommendation
// These are emoji-prefixed, human-readable explanations that tell agents what to DO
func GenerateTriageReasons(ctx TriageReasonContext) TriageReasons {
	return generateTriageReasonsAt(ctx, time.Now())
}

func generateTriageReasonsAt(ctx TriageReasonContext, now time.Time) TriageReasons {
	var reasons []string
	primary := ""
	actionHint := "Start work on this issue"
	isFutureDeferred := ctx.Issue != nil && ctx.Issue.IsDeferredAt(now)
	if isFutureDeferred {
		actionHint = fmt.Sprintf("Wait until %s before claiming", ctx.Issue.DeferUntil.UTC().Format(time.RFC3339))
	} else if ctx.Issue != nil && ctx.Issue.Status == model.StatusInProgress {
		actionHint = "Continue work on this issue"
	} else if ctx.Issue != nil && ctx.Issue.Status == model.StatusBlocked {
		actionHint = "Resolve blocked status before claiming this issue"
	} else if ctx.Issue != nil && ctx.Issue.Status != "" && ctx.Issue.Status != model.StatusOpen {
		actionHint = fmt.Sprintf("Wait for status %s to become open before claiming", ctx.Issue.Status)
	}

	// 1. Unblock cascade (highest priority - most actionable)
	if len(ctx.UnblocksIDs) >= 3 {
		reason := fmt.Sprintf("🎯 Completing this unblocks %d downstream issues (%s)",
			len(ctx.UnblocksIDs), formatUnblockList(ctx.UnblocksIDs))
		reasons = append(reasons, reason)
		if primary == "" {
			primary = reason
		}
	} else if len(ctx.UnblocksIDs) > 0 {
		reason := fmt.Sprintf("🔓 Unblocks %d item(s): %s",
			len(ctx.UnblocksIDs), formatUnblockList(ctx.UnblocksIDs))
		reasons = append(reasons, reason)
	}

	// 2. Label health (shows context for labels needing attention)
	if len(ctx.LabelHealth) > 0 && ctx.Issue != nil {
		for _, label := range ctx.Issue.Labels {
			health, exists := ctx.LabelHealth[label]
			if exists && health < 60 {
				reason := fmt.Sprintf("⚠️ Label '%s' needs attention (health: %d/100)", label, health)
				reasons = append(reasons, reason)
			}
		}
	}

	// 3. Graph metrics (bottleneck/centrality)
	if ctx.TriageScore != nil {
		bd := ctx.TriageScore.Breakdown
		if bd.BetweennessNorm > 0.5 {
			reason := fmt.Sprintf("🔀 Critical path bottleneck (betweenness: %.0f%%)", bd.BetweennessNorm*100)
			reasons = append(reasons, reason)
			if primary == "" {
				primary = reason
			}
		}
		if bd.PageRankNorm > 0.3 {
			reason := fmt.Sprintf("📊 High centrality in dependency graph (PageRank: %.0f%%)", bd.PageRankNorm*100)
			reasons = append(reasons, reason)
		}
	}

	// 4. Staleness alert
	if ctx.DaysSinceUpdate > 14 {
		reason := fmt.Sprintf("🕐 No activity in %d days - may need review", ctx.DaysSinceUpdate)
		reasons = append(reasons, reason)
		if ctx.Issue != nil && ctx.Issue.Status == model.StatusInProgress {
			actionHint = "Check if this is stuck and needs help"
		}
	} else if ctx.DaysSinceUpdate > 7 {
		reason := fmt.Sprintf("📅 Last updated %d days ago", ctx.DaysSinceUpdate)
		reasons = append(reasons, reason)
		if ctx.Issue != nil && ctx.Issue.Status == model.StatusInProgress {
			actionHint = "Continue work on this issue"
		}
	}

	// 5. Quick-win identification
	if ctx.IsQuickWin {
		reason := "⚡ Low effort, high impact - good starting point"
		reasons = append(reasons, reason)
		if primary == "" && len(ctx.UnblocksIDs) > 0 {
			primary = reason
		}

		// Update action hint unless in-progress (keep work/review guidance) or critically stale
		isInProgress := ctx.Issue != nil && ctx.Issue.Status == model.StatusInProgress
		isCriticalStale := isInProgress && ctx.DaysSinceUpdate > 14
		if !isInProgress && !isCriticalStale && !isFutureDeferred {
			actionHint = "Quick win - start here for fast progress"
		}
	}

	// 6. Agent claim status
	//
	// Defensive contract (#149): we only emit the "✅ Currently unclaimed"
	// hint when we have actual evidence the issue is open AND unclaimed.
	// The previous logic fell through to "unclaimed" whenever Issue was
	// nil or Status was empty/unrecognized — that produced misleading
	// reasons on issues we couldn't resolve through the analyzer, and
	// (combined with stale snapshot data) was the most plausible source
	// of the IN_PROGRESS + "currently unclaimed" contradiction the user
	// reported.
	hasIssueRecord := ctx.Issue != nil
	hasRecordedStatus := hasIssueRecord && ctx.Issue.Status != ""
	isInProgress := hasIssueRecord && ctx.Issue.Status == model.StatusInProgress
	isBlockedStatus := hasIssueRecord && ctx.Issue.Status == model.StatusBlocked
	isOpenStatus := hasIssueRecord && ctx.Issue.Status == model.StatusOpen
	// isNonOpenStatus matches the pre-#149 contract: any issue record
	// with a recorded non-empty Status that isn't Open/InProgress/Blocked
	// gets the "Status is X - not ready to claim" message. That covers
	// both enumerated statuses we know about (Deferred, Draft, Pinned,
	// Hooked, Review, Closed, Tombstone) AND custom-string statuses
	// projects may use that we don't recognize — silencing those would
	// be a regression. The unclaimed hint stays tighter: only emitted
	// when we positively know the issue is Open.
	isNonOpenStatus := hasRecordedStatus &&
		!isInProgress &&
		!isBlockedStatus &&
		!isOpenStatus
	if isFutureDeferred {
		reasons = append(reasons, fmt.Sprintf("⏸️ Deferred until %s - not ready to claim", ctx.Issue.DeferUntil.UTC().Format(time.RFC3339)))
		if ctx.ClaimedByAgent != "" {
			reasons = append(reasons, fmt.Sprintf("👤 Claimed by %s", ctx.ClaimedByAgent))
		}
	} else if isInProgress {
		if ctx.ClaimedByAgent != "" {
			reason := fmt.Sprintf("👤 Claimed by %s", ctx.ClaimedByAgent)
			reasons = append(reasons, reason)
			actionHint = fmt.Sprintf("Contact %s if you want to help", ctx.ClaimedByAgent)
		} else {
			reasons = append(reasons, "🚧 In progress - already being worked")
		}
	} else if isBlockedStatus {
		reasons = append(reasons, "⛔ Status is blocked - not ready to claim")
		if ctx.ClaimedByAgent != "" {
			reason := fmt.Sprintf("👤 Claimed by %s", ctx.ClaimedByAgent)
			reasons = append(reasons, reason)
			actionHint = fmt.Sprintf("Contact %s or resolve blockers before claiming", ctx.ClaimedByAgent)
		}
	} else if isNonOpenStatus {
		reason := fmt.Sprintf("⏸️ Status is %s - not ready to claim", ctx.Issue.Status)
		reasons = append(reasons, reason)
		if ctx.ClaimedByAgent != "" {
			claimReason := fmt.Sprintf("👤 Claimed by %s", ctx.ClaimedByAgent)
			reasons = append(reasons, claimReason)
			actionHint = fmt.Sprintf("Contact %s if you want to help", ctx.ClaimedByAgent)
		}
	} else if isOpenStatus && ctx.ClaimedByAgent == "" {
		// Only emit the "available for work" hint when we positively know
		// the issue is Open and has no assignee. Falling back to this
		// branch on missing/unknown status surfaces a misleading hint.
		reasons = append(reasons, "✅ Currently unclaimed - available for work")
	} else if ctx.ClaimedByAgent != "" {
		reason := fmt.Sprintf("👤 Claimed by %s", ctx.ClaimedByAgent)
		reasons = append(reasons, reason)
		actionHint = fmt.Sprintf("Contact %s if you want to help", ctx.ClaimedByAgent)
	}
	// Else: no issue record or empty status with no assignee — stay
	// silent on claim status rather than guess.

	// 7. Blocked status context
	if len(ctx.BlockedByIDs) > 0 {
		if len(ctx.BlockedByIDs) == 1 {
			reason := fmt.Sprintf("⏳ Blocked by %s - complete that first", ctx.BlockedByIDs[0])
			reasons = append(reasons, reason)
		} else {
			reason := fmt.Sprintf("⏳ Blocked by %d items - need to clear dependencies", len(ctx.BlockedByIDs))
			reasons = append(reasons, reason)
		}
		actionHint = fmt.Sprintf("Work on %s first to unblock this", ctx.BlockedByIDs[0])
	}

	// 8. Priority context
	if ctx.Issue != nil && ctx.Issue.Priority <= 1 {
		reason := fmt.Sprintf("🚨 High priority (P%d) - prioritize this work", ctx.Issue.Priority)
		reasons = append(reasons, reason)
	}

	// Default primary reason
	if primary == "" && len(reasons) > 0 {
		primary = reasons[0]
	} else if primary == "" {
		primary = "Good candidate for work"
		reasons = append(reasons, primary)
	}

	return TriageReasons{
		Primary:    primary,
		All:        reasons,
		ActionHint: actionHint,
	}
}

// formatUnblockList creates a comma-separated list of issue IDs, truncating if needed
func formatUnblockList(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= 3 {
		// joinStrings is defined in diff.go
		return joinStrings(ids, ", ")
	}
	return fmt.Sprintf("%s, %s, +%d more", ids[0], ids[1], len(ids)-2)
}

// GenerateTriageReasonsForScore generates reasons from a TriageScore and TriageContext.
// Uses cached blocker lookups for better performance (bv-k4az optimization).
func GenerateTriageReasonsForScore(score TriageScore, triageCtx *TriageContext) TriageReasons {
	return generateTriageReasonsForScoreAt(score, triageCtx, time.Now())
}

func generateTriageReasonsForScoreAt(score TriageScore, triageCtx *TriageContext, now time.Time) TriageReasons {
	analyzer := triageCtx.Analyzer()
	unblocksMap := triageCtx.UnblocksMap()
	issue := analyzer.GetIssue(score.IssueID)

	daysSinceUpdate := 0
	if issue != nil && !issue.UpdatedAt.IsZero() {
		daysSinceUpdate = int(time.Since(issue.UpdatedAt).Hours() / 24)
	}

	// Determine if this is a quick win based on factors
	isQuickWin := score.TriageFactors.QuickWinBoost > 0.05
	claimedByAgent := ""
	if issue != nil {
		claimedByAgent = issue.Assignee
	}

	ctx := TriageReasonContext{
		Issue:           issue,
		TriageScore:     &score,
		UnblocksIDs:     unblocksMap[score.IssueID],
		BlockedByIDs:    triageCtx.OpenBlockers(score.IssueID), // cached
		ClaimedByAgent:  claimedByAgent,
		DaysSinceUpdate: daysSinceUpdate,
		IsQuickWin:      isQuickWin,
		BlockerDepth:    triageCtx.BlockerDepth(score.IssueID), // cached
	}

	return generateTriageReasonsAt(ctx, now)
}

// EnhanceRecommendationWithTriageReasons updates a Recommendation with triage-specific reasons
func EnhanceRecommendationWithTriageReasons(rec *Recommendation, triageReasons TriageReasons) {
	if rec == nil {
		return
	}
	// Replace base reasons with enhanced triage reasons
	rec.Reasons = triageReasons.All
}

// buildRecommendationsByTrack groups recommendations by execution layer (topological depth).
// This enables multi-agent parallelization: all items in the same layer can be worked concurrently.
//
// Layering algorithm (BFS-based topological sort):
//   - Layer 0 ("track-A"): All currently actionable items (no open blockers)
//   - Layer 1 ("track-B"): Items blocked only by layer-0 items
//   - Layer N: Items blocked only by layer-(N-1) items
//
// This differs from the previous connected-components approach which created
// one track per disconnected work stream (issue #68).
func buildRecommendationsByTrack(recs []Recommendation, analyzer *Analyzer, unblocksMap map[string][]string, now time.Time, notReadyLabels []string, parentsWithOpenChildren map[string]bool) []TrackRecommendationGroup {
	// Compute blocker depth for all recommendations.
	// Depth 0 = actionable now (no blockers), Depth N = blocked by depth-(N-1) items.
	blockerDepths := make(map[string]int, len(recs))
	recByID := make(map[string]*Recommendation, len(recs))

	for i := range recs {
		recByID[recs[i].ID] = &recs[i]
	}

	// BFS-based depth computation.
	// Items with no blockers are depth 0. Items blocked by depth-0 items are depth 1, etc.
	var dfs func(id string, visited map[string]bool) int
	dfs = func(id string, visited map[string]bool) int {
		if depth, ok := blockerDepths[id]; ok {
			return depth
		}
		if visited[id] {
			return -1 // Cycle detected
		}
		visited[id] = true

		rec := recByID[id]
		if rec == nil || len(rec.BlockedBy) == 0 {
			visited[id] = false
			blockerDepths[id] = 0
			return 0
		}

		maxBlockerDepth := 0
		for _, blockerID := range rec.BlockedBy {
			blockerDepth := dfs(blockerID, visited)
			if blockerDepth == -1 {
				visited[id] = false
				blockerDepths[id] = -1
				return -1
			}
			if blockerDepth+1 > maxBlockerDepth {
				maxBlockerDepth = blockerDepth + 1
			}
		}

		visited[id] = false
		blockerDepths[id] = maxBlockerDepth
		return maxBlockerDepth
	}

	// Compute depths for all recommendations
	for _, rec := range recs {
		visited := make(map[string]bool)
		dfs(rec.ID, visited)
	}

	// Group recommendations by depth into tracks
	groups := make(map[int]*TrackRecommendationGroup)

	for _, rec := range recs {
		depth := blockerDepths[rec.ID]
		if depth < 0 {
			depth = 999 // Put cyclic items in a special track
		}

		if _, exists := groups[depth]; !exists {
			trackID := generateTrackID(depth + 1) // depth 0 -> track-A, depth 1 -> track-B
			reason := layerReason(depth, len(recs))
			groups[depth] = &TrackRecommendationGroup{
				TrackID: trackID,
				Reason:  reason,
			}
		}

		group := groups[depth]
		group.Recommendations = append(group.Recommendations, rec)
		group.TotalUnblocks += len(unblocksMap[rec.ID])

		// Update top pick (highest score in this layer)
		if isClaimableRecommendation(rec, now, notReadyLabels, parentsWithOpenChildren) && (group.TopPick == nil || rec.Score > group.TopPick.Score) {
			group.TopPick = &TopPick{
				ID:       rec.ID,
				Title:    rec.Title,
				Score:    rec.Score,
				Reasons:  rec.Reasons,
				Unblocks: len(unblocksMap[rec.ID]),
			}
			group.ClaimCommand = fmt.Sprintf("CI=1 br update %s --status in_progress --json", rec.ID)
		}
	}

	// Convert map to sorted slice (by depth/track order)
	var depths []int
	for d := range groups {
		depths = append(depths, d)
	}
	sort.Ints(depths)

	result := make([]TrackRecommendationGroup, 0, len(groups))
	for _, d := range depths {
		result = append(result, *groups[d])
	}

	return result
}

// layerReason returns a human-readable reason for a track's grouping.
func layerReason(depth int, totalRecs int) string {
	switch {
	case depth == 0:
		return "Actionable now - can work in parallel"
	case depth == 1:
		return "Becomes actionable after layer 0 completes"
	case depth >= 999:
		return "Cyclic dependencies detected"
	default:
		return fmt.Sprintf("Becomes actionable after layer %d completes", depth-1)
	}
}

// buildRecommendationsByLabel groups recommendations by label
func buildRecommendationsByLabel(recs []Recommendation, unblocksMap map[string][]string, now time.Time, notReadyLabels []string, parentsWithOpenChildren map[string]bool) []LabelRecommendationGroup {
	groups := make(map[string]*LabelRecommendationGroup)

	for _, rec := range recs {
		label := "unlabeled"
		if len(rec.Labels) > 0 {
			label = rec.Labels[0] // Primary label
		}

		if _, exists := groups[label]; !exists {
			groups[label] = &LabelRecommendationGroup{
				Label: label,
			}
		}
		group := groups[label]
		group.Recommendations = append(group.Recommendations, rec)
		group.TotalUnblocks += len(unblocksMap[rec.ID])

		if isClaimableRecommendation(rec, now, notReadyLabels, parentsWithOpenChildren) && (group.TopPick == nil || rec.Score > group.TopPick.Score) {
			group.TopPick = &TopPick{
				ID:       rec.ID,
				Title:    rec.Title,
				Score:    rec.Score,
				Reasons:  rec.Reasons,
				Unblocks: len(unblocksMap[rec.ID]),
			}
			group.ClaimCommand = fmt.Sprintf("CI=1 br update %s --status in_progress --json", rec.ID)
		}
	}

	var result []LabelRecommendationGroup
	for _, g := range groups {
		result = append(result, *g)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Label < result[j].Label
	})

	return result
}
