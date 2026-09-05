// Package drift provides drift detection by comparing current metrics to a baseline.
// It identifies changes in graph structure, cycles, and key metrics.
package drift

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Severity represents the severity level of a drift alert
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// AlertType categorizes different kinds of drift alerts
type AlertType string

const (
	AlertNewCycle           AlertType = "new_cycle"
	AlertPageRankChange     AlertType = "pagerank_change"
	AlertDensityGrowth      AlertType = "density_growth"
	AlertNodeCountChange    AlertType = "node_count_change"
	AlertEdgeCountChange    AlertType = "edge_count_change"
	AlertBlockedIncrease    AlertType = "blocked_increase"
	AlertActionableChange   AlertType = "actionable_change"
	AlertStaleIssue         AlertType = "stale_issue"
	AlertVelocityDrop       AlertType = "velocity_drop"
	AlertBlockingCascade    AlertType = "blocking_cascade"
	AlertHighImpactUnblock  AlertType = "high_impact_unblock"
	AlertAbandonedClaim     AlertType = "abandoned_claim"
	AlertPotentialDuplicate AlertType = "potential_duplicate"
	AlertPriorityMismatch   AlertType = "priority_mismatch"
	AlertScopeCreep         AlertType = "scope_creep"
)

// AllAlertTypes returns every alert type Calculate can emit. Tests iterate it
// to prove each type has an emitter, and the README table is checked against
// it, so adding a type here without an emitter and a docs row fails the build
// gates rather than shipping a declared-but-dead alert.
func AllAlertTypes() []AlertType {
	return []AlertType{
		AlertStaleIssue,
		AlertBlockingCascade,
		AlertHighImpactUnblock,
		AlertAbandonedClaim,
		AlertPotentialDuplicate,
		AlertPriorityMismatch,
		AlertVelocityDrop,
		AlertNewCycle,
		AlertDensityGrowth,
		AlertNodeCountChange,
		AlertEdgeCountChange,
		AlertScopeCreep,
		AlertBlockedIncrease,
		AlertActionableChange,
		AlertPageRankChange,
	}
}

// Alert represents a single drift detection alert
type Alert struct {
	Type        AlertType `json:"type"`
	Severity    Severity  `json:"severity"`
	Message     string    `json:"message"`
	BaselineVal float64   `json:"baseline_value,omitempty"`
	CurrentVal  float64   `json:"current_value,omitempty"`
	Delta       float64   `json:"delta,omitempty"`
	Details     []string  `json:"details,omitempty"`
	IssueID     string    `json:"issue_id,omitempty"`
	Label       string    `json:"label,omitempty"`
	DetectedAt  time.Time `json:"detected_at,omitempty"`

	// RelatedIssueID names the second issue of a pairwise alert (potential_duplicate).
	RelatedIssueID string `json:"related_issue_id,omitempty"`
	// Labels carries the flagged issue's labels so --alert-label can filter on them.
	Labels []string `json:"labels,omitempty"`
	// SuggestedAction is the one-line remedy an agent or human should consider.
	SuggestedAction string `json:"suggested_action,omitempty"`

	// Blocking cascade specific fields (bv-165)
	UnblocksCount         int `json:"unblocks_count,omitempty"`
	DownstreamPrioritySum int `json:"downstream_priority_sum,omitempty"`
}

// Result contains the complete drift analysis
type Result struct {
	// HasDrift is true if any alerts were generated
	HasDrift bool `json:"has_drift"`

	// Alerts lists all detected drift issues
	Alerts []Alert `json:"alerts"`

	// Summary statistics
	CriticalCount int `json:"critical_count"`
	WarningCount  int `json:"warning_count"`
	InfoCount     int `json:"info_count"`

	// SkippedChecks lists alert types that were not evaluated and why (for
	// example the graph exceeds proactive_max_issues), so silence is never
	// mistaken for health.
	SkippedChecks []SkippedCheck `json:"skipped_checks,omitempty"`
}

// SkippedCheck records one alert type that Calculate did not run.
type SkippedCheck struct {
	Type   AlertType `json:"type"`
	Reason string    `json:"reason"`
}

// expensiveCheckAllowed reports whether the graph is small enough for the
// checks that re-run whole-graph analysis, recording a SkippedCheck otherwise.
func (c *Calculator) expensiveCheckAllowed(result *Result, typ AlertType) bool {
	limit := c.config.ProactiveMaxIssues
	if limit <= 0 || len(c.issues) <= limit {
		return true
	}
	result.SkippedChecks = append(result.SkippedChecks, SkippedCheck{
		Type:   typ,
		Reason: fmt.Sprintf("%d issues exceed proactive_max_issues=%d", len(c.issues), limit),
	})
	return false
}

// Calculator performs drift detection
type Calculator struct {
	config   *Config
	baseline *baseline.Baseline
	current  *baseline.Baseline
	issues   []model.Issue
	now      time.Time

	// analyzerCache is the graph analyzer over issues, built on first use so
	// the issue-level checks share unblock/actionable/impact computations.
	analyzerCache *analysis.Analyzer

	// A borrowed analyzer is used only while its captured scope and scoring
	// remain unchanged. Issues are owned, in the caller's order; readiness is
	// immutable. A later source mutation detaches the cache without changing
	// this calculator's captured inputs or mutating the source analyzer.
	analyzerSource     *analysis.Analyzer
	analyzerReadiness  *model.ReadinessIndex
	analyzerCandidates map[string]bool
	analyzerScoring    analysis.ScoringSnapshot
}

// NewCalculator creates a drift calculator with the given baseline and current snapshot.
// Both bl and current must be non-nil; passing nil for either will cause Calculate() to
// return an empty result with no alerts.
func NewCalculator(bl *baseline.Baseline, current *baseline.Baseline, cfg *Config) *Calculator {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &Calculator{
		config:   cfg,
		baseline: bl,
		current:  current,
		now:      time.Now().UTC(),
	}
}

// SetNow overrides the reference instant used for timestamps and time-based
// drift checks. The zero Go time is valid because SOURCE_DATE_EPOCH can map to
// year 1 exactly.
func (c *Calculator) SetNow(now time.Time) {
	if !c.now.Equal(now) {
		c.analyzerCache = nil
		c.analyzerSource = nil
	}
	c.now = now.UTC()
}

func (c *Calculator) nowUTC() time.Time {
	return c.now.UTC()
}

// SetIssues attaches the current issue list for issue-level alerts (e.g., staleness).
// Optional: drift detection still works without issues attached.
func (c *Calculator) SetIssues(issues []model.Issue) {
	c.issues = issues
	c.analyzerCache = nil
	c.analyzerSource = nil
	c.analyzerReadiness = nil
	c.analyzerCandidates = nil
	c.analyzerScoring = analysis.ScoringSnapshot{}
}

// ReuseAnalyzer borrows an analyzer for the exact attached issue rows and
// reference instant, preserving the attached rows' order. On success the
// calculator owns those rows and captures the analyzer's dependency authority,
// candidate eligibility, and scoring state. A mismatch leaves the ordinary
// SetIssues path intact. Call from the analyzer's owning goroutine; neither its
// scope nor the attached rows may change concurrently with this calculator.
func (c *Calculator) ReuseAnalyzer(a *analysis.Analyzer) bool {
	if a == nil || !c.now.Equal(a.Now()) || !a.MatchesIssues(c.issues) {
		return false
	}
	issues := make([]model.Issue, len(c.issues))
	candidates := make(map[string]bool, len(c.issues))
	for i := range c.issues {
		issues[i] = c.issues[i].Clone()
		candidates[issues[i].ID] = a.IsCandidate(issues[i].ID)
	}
	c.issues = issues
	c.analyzerReadiness = a.Readiness()
	c.analyzerCandidates = candidates
	c.analyzerScoring = a.CaptureScoring()
	c.analyzerSource = a
	c.analyzerCache = a
	return true
}

func (c *Calculator) validateBorrowedAnalyzer() {
	a := c.analyzerSource
	if a == nil {
		return
	}
	valid := a.CaptureScoring() == c.analyzerScoring && a.Readiness() == c.analyzerReadiness
	if valid {
		for id, selected := range c.analyzerCandidates {
			if a.IsCandidate(id) != selected {
				valid = false
				break
			}
		}
	}
	if !valid {
		c.analyzerSource = nil
		c.analyzerCache = nil
	}
}

// analyzer returns the shared graph analyzer over the attached issues (nil
// when no issues are attached).
func (c *Calculator) analyzer() *analysis.Analyzer {
	if c.analyzerCache == nil && len(c.issues) > 0 {
		a := analysis.NewAnalyzer(c.issues)
		if c.analyzerReadiness != nil {
			a.SetReadinessScope(c.analyzerReadiness, c.analyzerCandidates)
			a.RestoreScoring(c.analyzerScoring)
		}
		a.SetNow(c.nowUTC())
		c.analyzerCache = a
	}
	return c.analyzerCache
}

// Calculate performs drift detection and returns results
func (c *Calculator) Calculate() *Result {
	result := &Result{
		Alerts: make([]Alert, 0),
	}

	// Guard against nil baseline or current snapshot
	if c.baseline == nil || c.current == nil {
		return result
	}
	c.validateBorrowedAnalyzer()

	// Check for new cycles (critical)
	c.checkCycles(result)

	// Check density growth (info/warning)
	c.checkDensity(result)

	// Check node/edge count changes (info)
	c.checkGraphSize(result)

	// Check blocked issues increase (warning)
	c.checkBlocked(result)

	// Check actionable count changes (info)
	c.checkActionable(result)

	// Check PageRank changes (warning)
	c.checkPageRankChanges(result)

	// Check staleness (uses current issues if provided)
	c.checkStaleness(result)

	// Check blocking cascades (uses current issues if provided)
	c.checkBlockingCascade(result)

	// Scope creep against the baseline's open count (info)
	c.checkScopeCreep(result)

	// Issue-level proactive checks (all need attached issues)
	c.checkVelocityDrop(result)
	c.checkHighImpactUnblock(result)
	c.checkAbandonedClaim(result)
	c.checkPotentialDuplicate(result)
	c.checkPriorityMismatch(result)

	// Compute summary
	for _, alert := range result.Alerts {
		switch alert.Severity {
		case SeverityCritical:
			result.CriticalCount++
		case SeverityWarning:
			result.WarningCount++
		case SeverityInfo:
			result.InfoCount++
		}
	}
	result.HasDrift = len(result.Alerts) > 0

	return result
}

// checkCycles detects new cycles that weren't in the baseline
func (c *Calculator) checkCycles(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertNewCycle)) {
		return
	}

	baselineCycles := make(map[string]bool)
	for _, cycle := range c.baseline.Cycles {
		key := cycleKey(cycle)
		baselineCycles[key] = true
	}

	var newCycles [][]string
	for _, cycle := range c.current.Cycles {
		key := cycleKey(cycle)
		if !baselineCycles[key] {
			newCycles = append(newCycles, cycle)
		}
	}

	if len(newCycles) > 0 {
		details := make([]string, 0, len(newCycles))
		for _, cycle := range newCycles {
			details = append(details, strings.Join(cycle, " → "))
		}

		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertNewCycle,
			Severity:        SeverityCritical,
			SuggestedAction: "Break the cycle by removing or reversing one dependency edge (bv --robot-suggest lists cycle-break candidates)",
			Message:         fmt.Sprintf("%d new cycle(s) detected", len(newCycles)),
			BaselineVal:     float64(len(c.baseline.Cycles)),
			CurrentVal:      float64(len(c.current.Cycles)),
			Delta:           float64(len(newCycles)),
			Details:         details,
			DetectedAt:      c.nowUTC(),
		})
	}
}

// checkDensity checks for significant density changes
func (c *Calculator) checkDensity(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertDensityGrowth)) {
		return
	}

	blDensity := c.baseline.Stats.Density
	curDensity := c.current.Stats.Density

	if blDensity == 0 {
		return // No baseline to compare
	}

	delta := curDensity - blDensity
	pctChange := (delta / blDensity) * 100

	if pctChange >= c.config.DensityWarningPct {
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertDensityGrowth,
			Severity:        SeverityWarning,
			SuggestedAction: "Check whether new dependencies are real blockers; over-linking hides the true critical path",
			Message:         fmt.Sprintf("Graph density increased by %.1f%%", pctChange),
			BaselineVal:     blDensity,
			CurrentVal:      curDensity,
			Delta:           delta,
			DetectedAt:      c.nowUTC(),
		})
	} else if pctChange >= c.config.DensityInfoPct {
		result.Alerts = append(result.Alerts, Alert{
			Type:        AlertDensityGrowth,
			Severity:    SeverityInfo,
			Message:     fmt.Sprintf("Graph density increased by %.1f%%", pctChange),
			BaselineVal: blDensity,
			CurrentVal:  curDensity,
			Delta:       delta,
			DetectedAt:  c.nowUTC(),
		})
	}
}

// checkGraphSize checks for significant node/edge count changes
func (c *Calculator) checkGraphSize(result *Result) {
	// Check if alert types are disabled (bv-167)
	nodeDisabled := c.config.IsAlertDisabled(string(AlertNodeCountChange))
	edgeDisabled := c.config.IsAlertDisabled(string(AlertEdgeCountChange))
	if nodeDisabled && edgeDisabled {
		return
	}

	blNodes := c.baseline.Stats.NodeCount
	curNodes := c.current.Stats.NodeCount
	nodeDelta := curNodes - blNodes

	if !nodeDisabled && blNodes > 0 {
		nodePct := float64(nodeDelta) / float64(blNodes) * 100
		if nodePct >= c.config.NodeGrowthInfoPct || nodePct <= -c.config.NodeGrowthInfoPct {
			result.Alerts = append(result.Alerts, Alert{
				Type:            AlertNodeCountChange,
				Severity:        SeverityInfo,
				SuggestedAction: "Confirm the graph change is intended (bv --robot-diff --diff-since <baseline commit> lists it)",
				Message:         fmt.Sprintf("Node count changed by %+d (%.1f%%)", nodeDelta, nodePct),
				BaselineVal:     float64(blNodes),
				CurrentVal:      float64(curNodes),
				Delta:           float64(nodeDelta),
				DetectedAt:      c.nowUTC(),
			})
		}
	}

	blEdges := c.baseline.Stats.EdgeCount
	curEdges := c.current.Stats.EdgeCount
	edgeDelta := curEdges - blEdges

	if !edgeDisabled && blEdges > 0 {
		edgePct := float64(edgeDelta) / float64(blEdges) * 100
		if edgePct >= c.config.EdgeGrowthInfoPct || edgePct <= -c.config.EdgeGrowthInfoPct {
			result.Alerts = append(result.Alerts, Alert{
				Type:            AlertEdgeCountChange,
				Severity:        SeverityInfo,
				SuggestedAction: "Review recently added or removed dependencies for accidental blockers",
				Message:         fmt.Sprintf("Edge count changed by %+d (%.1f%%)", edgeDelta, edgePct),
				BaselineVal:     float64(blEdges),
				CurrentVal:      float64(curEdges),
				Delta:           float64(edgeDelta),
				DetectedAt:      c.nowUTC(),
			})
		}
	}
}

// checkBlocked checks for increases in blocked issues
func (c *Calculator) checkBlocked(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertBlockedIncrease)) {
		return
	}

	blBlocked := c.baseline.Stats.BlockedCount
	curBlocked := c.current.Stats.BlockedCount
	delta := curBlocked - blBlocked

	if delta > 0 && delta >= c.config.BlockedIncreaseThreshold {
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertBlockedIncrease,
			Severity:        SeverityWarning,
			SuggestedAction: "Clear the top blockers first: bv --robot-triage lists blockers_to_clear",
			Message:         fmt.Sprintf("Blocked issues increased by %d", delta),
			BaselineVal:     float64(blBlocked),
			CurrentVal:      float64(curBlocked),
			Delta:           float64(delta),
			DetectedAt:      c.nowUTC(),
		})
	}
}

// checkActionable checks for significant changes in actionable issues
func (c *Calculator) checkActionable(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertActionableChange)) {
		return
	}

	blAction := c.baseline.Stats.ActionableCount
	curAction := c.current.Stats.ActionableCount
	delta := curAction - blAction

	if blAction > 0 {
		pct := float64(delta) / float64(blAction) * 100
		if pct <= -c.config.ActionableDecreaseWarningPct {
			result.Alerts = append(result.Alerts, Alert{
				Type:            AlertActionableChange,
				Severity:        SeverityWarning,
				SuggestedAction: "Fewer ready items means work is piling up behind blockers; unblock before starting new work",
				Message:         fmt.Sprintf("Actionable issues decreased by %d (%.1f%%)", -delta, -pct),
				BaselineVal:     float64(blAction),
				CurrentVal:      float64(curAction),
				Delta:           float64(delta),
				DetectedAt:      c.nowUTC(),
			})
		} else if pct >= c.config.ActionableIncreaseInfoPct || pct <= -c.config.ActionableIncreaseInfoPct {
			result.Alerts = append(result.Alerts, Alert{
				Type:        AlertActionableChange,
				Severity:    SeverityInfo,
				Message:     fmt.Sprintf("Actionable issues changed by %+d (%.1f%%)", delta, pct),
				BaselineVal: float64(blAction),
				CurrentVal:  float64(curAction),
				Delta:       float64(delta),
				DetectedAt:  c.nowUTC(),
			})
		}
	}
}

// checkPageRankChanges detects significant changes in top PageRank items
func (c *Calculator) checkPageRankChanges(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertPageRankChange)) {
		return
	}

	blPR := make(map[string]float64)
	for _, item := range c.baseline.TopMetrics.PageRank {
		blPR[item.ID] = item.Value
	}

	curPR := make(map[string]float64)
	for _, item := range c.current.TopMetrics.PageRank {
		curPR[item.ID] = item.Value
	}

	var changes []string

	// Check for significant changes in existing items
	for id, blVal := range blPR {
		curVal, exists := curPR[id]
		if !exists {
			changes = append(changes, fmt.Sprintf("%s dropped from top", id))
			continue
		}
		if blVal > 0 {
			pctChange := ((curVal - blVal) / blVal) * 100
			if pctChange >= c.config.PageRankChangeWarningPct || pctChange <= -c.config.PageRankChangeWarningPct {
				changes = append(changes, fmt.Sprintf("%s: %.1f%% change", id, pctChange))
			}
		}
	}

	// Check for new entries in top
	for id := range curPR {
		if _, exists := blPR[id]; !exists {
			changes = append(changes, fmt.Sprintf("%s entered top", id))
		}
	}
	sort.Strings(changes)

	if len(changes) > 0 {
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertPageRankChange,
			Severity:        SeverityWarning,
			SuggestedAction: "Re-check the priority of the issues whose structural importance moved",
			Message:         fmt.Sprintf("%d PageRank changes detected", len(changes)),
			Details:         changes,
			DetectedAt:      c.nowUTC(),
		})
	}
}

// checkStaleness emits alerts for issues that have been inactive beyond thresholds.
// Relies on attached issues; no-op if issues were not provided.
// Uses per-label threshold overrides when configured (bv-167).
func (c *Calculator) checkStaleness(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertStaleIssue)) {
		return
	}

	if len(c.issues) == 0 {
		return
	}
	now := c.nowUTC()
	staleCount := 0
	for i := range c.issues {
		if severity, _, _ := c.stalenessSeverity(&c.issues[i], now); severity != "" {
			staleCount++
		}
	}
	if staleCount == 0 {
		return
	}
	// Alert values are large. Reserve only eligible rows, preserving any
	// preceding drift alerts and avoiding repeated backing-array copies.
	if required := len(result.Alerts) + staleCount; cap(result.Alerts) < required {
		alerts := make([]Alert, len(result.Alerts), required)
		copy(alerts, result.Alerts)
		result.Alerts = alerts
	}
	for i := range c.issues {
		issue := &c.issues[i]
		severity, lastActive, days := c.stalenessSeverity(issue, now)
		if severity == "" {
			continue
		}

		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertStaleIssue,
			Severity:        severity,
			Labels:          issue.Labels,
			SuggestedAction: "Update, close, or re-triage the issue; stale work hides real priorities",
			Message:         fmt.Sprintf("Issue %s inactive for %.0f days", issue.ID, days),
			IssueID:         issue.ID,
			DetectedAt:      now,
			Details: []string{
				fmt.Sprintf("status=%s", issue.Status),
				fmt.Sprintf("last_update=%s", lastActive.Format(time.RFC3339)),
			},
		})
	}
}

// stalenessSeverity is shared by capacity counting and emission so label
// overrides, status multipliers and inclusive time boundaries stay identical.
func (c *Calculator) stalenessSeverity(issue *model.Issue, now time.Time) (Severity, time.Time, float64) {
	if issue.Status == model.StatusClosed || issue.Status == model.StatusTombstone {
		return "", time.Time{}, 0
	}
	lastActive := issue.UpdatedAt
	if lastActive.IsZero() {
		lastActive = issue.CreatedAt
	}
	if lastActive.IsZero() {
		return "", time.Time{}, 0
	}
	warnDays, critDays, inProgressMult := c.config.GetStalenessThresholds(issue.Labels)
	warn := float64(warnDays)
	crit := float64(critDays)
	if issue.Status == model.StatusInProgress && inProgressMult > 0 {
		warn *= inProgressMult
		crit *= inProgressMult
	}
	days := now.Sub(lastActive).Hours() / 24.0
	if days >= crit {
		return SeverityCritical, lastActive, days
	}
	if days >= warn {
		return SeverityWarning, lastActive, days
	}
	return "", lastActive, days
}

// checkBlockingCascade raises alerts for issues whose completion would unblock many dependents.
// Uses existing dependency graph; no alert if issues not provided.
// Includes urgency scoring via downstream priority sum (bv-165).
func (c *Calculator) checkBlockingCascade(result *Result) {
	// Check if alert type is disabled (bv-167)
	if c.config.IsAlertDisabled(string(AlertBlockingCascade)) {
		return
	}

	if len(c.issues) == 0 {
		return
	}
	infoThresh := c.config.BlockingCascadeInfo
	warnThresh := c.config.BlockingCascadeWarning
	if infoThresh <= 0 && warnThresh <= 0 {
		return
	}

	// Build issue lookup map for priority calculation (bv-165)
	issueMap := c.issueMap()

	analyzer := c.analyzer()
	actionable := sortedByID(analyzer.GetActionableIssues())
	if len(actionable) == 0 {
		return
	}

	for _, iss := range actionable {
		unblocks := analyzer.ComputeUnblocks(iss.ID)
		count := len(unblocks)
		if count == 0 {
			continue
		}
		severity := SeverityInfo
		if warnThresh > 0 && count >= warnThresh {
			severity = SeverityWarning
		} else if infoThresh > 0 && count < infoThresh {
			continue
		}

		// Calculate downstream priority sum for urgency scoring (bv-165)
		// Lower priority values = higher importance (P0=critical, P4=backlog)
		prioritySum := 0
		for _, unblockedID := range unblocks {
			if unblockedIssue, ok := issueMap[unblockedID]; ok {
				prioritySum += unblockedIssue.Priority
			}
		}

		result.Alerts = append(result.Alerts, Alert{
			Type:                  AlertBlockingCascade,
			Severity:              severity,
			Labels:                iss.Labels,
			SuggestedAction:       "Prioritize this issue: closing it releases the listed downstream items",
			Message:               fmt.Sprintf("Completing %s unblocks %d downstream item(s)", iss.ID, count),
			IssueID:               iss.ID,
			DetectedAt:            c.nowUTC(),
			Details:               unblocks,
			UnblocksCount:         count,
			DownstreamPrioritySum: prioritySum,
		})
	}
}

// cycleKey creates a normalized key for a cycle for comparison.
// It rotates the cycle so the lexicographically smallest element is first,
// preserving the order (direction) of elements.
// Handles cycles represented as [A, B, C, A] by treating the repeated end as implicit.
func cycleKey(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}

	// Work with the unique sequence of nodes (exclude repeated end)
	unique := cycle
	if len(cycle) > 1 && cycle[0] == cycle[len(cycle)-1] {
		unique = cycle[:len(cycle)-1]
	}

	if len(unique) == 0 {
		return ""
	}

	// Find index of smallest element
	minIdx := 0
	minVal := unique[0]
	for i, val := range unique {
		if val < minVal {
			minVal = val
			minIdx = i
		}
	}

	// Rotate so min element is first
	rotated := make([]string, len(unique))
	copy(rotated, unique[minIdx:])
	copy(rotated[len(unique)-minIdx:], unique[:minIdx])

	// Use null byte as separator to avoid collisions with ID characters
	return strings.Join(rotated, "\x00")
}

// Summary returns a human-readable summary of drift results
func (r *Result) Summary() string {
	if !r.HasDrift {
		return "No drift detected. Project metrics are within baseline thresholds.\n"
	}

	var sb strings.Builder
	sb.WriteString("Drift Analysis Summary\n")
	sb.WriteString("======================\n\n")

	if r.CriticalCount > 0 {
		sb.WriteString(fmt.Sprintf("🔴 CRITICAL: %d issue(s)\n", r.CriticalCount))
	}
	if r.WarningCount > 0 {
		sb.WriteString(fmt.Sprintf("🟡 WARNING: %d issue(s)\n", r.WarningCount))
	}
	if r.InfoCount > 0 {
		sb.WriteString(fmt.Sprintf("🔵 INFO: %d issue(s)\n", r.InfoCount))
	}

	sb.WriteString("\nDetails:\n")
	for _, alert := range r.Alerts {
		icon := "ℹ️"
		switch alert.Severity {
		case SeverityCritical:
			icon = "🔴"
		case SeverityWarning:
			icon = "🟡"
		}
		sb.WriteString(fmt.Sprintf("  %s [%s] %s\n", icon, alert.Type, alert.Message))
		for _, detail := range alert.Details {
			sb.WriteString(fmt.Sprintf("      - %s\n", detail))
		}
	}
	sb.WriteString("\n")

	return sb.String()
}

// HasCritical returns true if there are any critical alerts
func (r *Result) HasCritical() bool {
	return r.CriticalCount > 0
}

// HasWarnings returns true if there are any warning or critical alerts
func (r *Result) HasWarnings() bool {
	return r.CriticalCount > 0 || r.WarningCount > 0
}

// ExitCode returns suggested exit code for CI use
// 0 = no drift, 1 = critical, 2 = warning, 0 = info only
func (r *Result) ExitCode() int {
	if r.CriticalCount > 0 {
		return 1
	}
	if r.WarningCount > 0 {
		return 2
	}
	return 0
}

// sortedByID returns a copy of issues ordered by ID so alert order is
// deterministic regardless of how the analyzer enumerates them.
func sortedByID(issues []model.Issue) []model.Issue {
	out := append([]model.Issue(nil), issues...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// issueMap borrows rows for read-only lookups during one calculation. The
// calculator keeps the slice stable; duplicate IDs retain the last row.
func (c *Calculator) issueMap() map[string]*model.Issue {
	m := make(map[string]*model.Issue, len(c.issues))
	for i := range c.issues {
		m[c.issues[i].ID] = &c.issues[i]
	}
	return m
}

// checkScopeCreep flags open-issue growth against the baseline (README's
// "scope_creep"): the plan grew by ScopeCreepPct or more since the baseline
// was saved. node_count_change stays as the raw graph-size signal; this one
// is about unfinished work, not nodes.
func (c *Calculator) checkScopeCreep(result *Result) {
	if c.config.IsAlertDisabled(string(AlertScopeCreep)) || c.config.ScopeCreepPct <= 0 {
		return
	}
	blOpen := c.baseline.Stats.OpenCount
	curOpen := c.current.Stats.OpenCount
	if blOpen <= 0 || curOpen <= blOpen {
		return
	}
	growthPct := float64(curOpen-blOpen) / float64(blOpen) * 100
	if growthPct < c.config.ScopeCreepPct {
		return
	}
	result.Alerts = append(result.Alerts, Alert{
		Type:            AlertScopeCreep,
		Severity:        SeverityInfo,
		Message:         fmt.Sprintf("Open issues grew %.0f%% since the baseline (%d → %d)", growthPct, blOpen, curOpen),
		BaselineVal:     float64(blOpen),
		CurrentVal:      float64(curOpen),
		Delta:           float64(curOpen - blOpen),
		DetectedAt:      c.nowUTC(),
		SuggestedAction: "Review the issues opened since the baseline; defer or split work that was not planned",
	})
}

// checkVelocityDrop compares closes in the most recent VelocityWindowDays
// against the window before it. It only speaks when the prior window had at
// least VelocityMinBaseline closes, so a quiet project does not alarm.
func (c *Calculator) checkVelocityDrop(result *Result) {
	if c.config.IsAlertDisabled(string(AlertVelocityDrop)) || len(c.issues) == 0 {
		return
	}
	window := c.config.VelocityWindowDays
	if window <= 0 || c.config.VelocityDropPct <= 0 {
		return
	}
	now := c.nowUTC()
	recentStart := now.AddDate(0, 0, -window)
	priorStart := now.AddDate(0, 0, -2*window)
	recent, prior := 0, 0
	for _, iss := range c.issues {
		if iss.ClosedAt == nil {
			continue
		}
		closed := iss.ClosedAt.UTC()
		switch {
		case closed.After(recentStart) && !closed.After(now):
			recent++
		case closed.After(priorStart) && !closed.After(recentStart):
			prior++
		}
	}
	if prior == 0 || prior < c.config.VelocityMinBaseline {
		return
	}
	dropPct := float64(prior-recent) / float64(prior) * 100
	if dropPct < c.config.VelocityDropPct {
		return
	}
	result.Alerts = append(result.Alerts, Alert{
		Type:        AlertVelocityDrop,
		Severity:    SeverityWarning,
		Message:     fmt.Sprintf("Closed %d issue(s) in the last %d days vs %d in the %d days before (-%.0f%%)", recent, window, prior, window, dropPct),
		BaselineVal: float64(prior),
		CurrentVal:  float64(recent),
		Delta:       float64(recent - prior),
		DetectedAt:  now,
		Details: []string{
			fmt.Sprintf("window_days=%d", window),
			fmt.Sprintf("prior_window_closed=%d", prior),
			fmt.Sprintf("recent_window_closed=%d", recent),
		},
		SuggestedAction: "Check for blocked or abandoned in-progress work; bv --robot-triage shows what is actually ready",
	})
}

// checkHighImpactUnblock is blocking_cascade's priority-aware sibling: an
// actionable issue that unblocks HighImpactUnblockMin or more items of which
// at least one is at HighImpactPriorityMax or more urgent. Two or more urgent
// downstream items escalate to warning.
func (c *Calculator) checkHighImpactUnblock(result *Result) {
	if c.config.IsAlertDisabled(string(AlertHighImpactUnblock)) || len(c.issues) == 0 {
		return
	}
	minUnblocks := c.config.HighImpactUnblockMin
	if minUnblocks <= 0 {
		return
	}
	maxPriority := c.config.HighImpactPriorityMax
	issueMap := c.issueMap()
	analyzer := c.analyzer()
	for _, iss := range sortedByID(analyzer.GetActionableIssues()) {
		unblocks := analyzer.ComputeUnblocks(iss.ID)
		if len(unblocks) < minUnblocks {
			continue
		}
		var urgent []string
		for _, id := range unblocks {
			if downstream, ok := issueMap[id]; ok && downstream.Priority <= maxPriority {
				urgent = append(urgent, id)
			}
		}
		if len(urgent) == 0 {
			continue
		}
		sort.Strings(urgent)
		severity := SeverityInfo
		if len(urgent) >= 2 {
			severity = SeverityWarning
		}
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertHighImpactUnblock,
			Severity:        severity,
			Message:         fmt.Sprintf("Completing %s unblocks %d item(s), %d of them at P%d or higher", iss.ID, len(unblocks), len(urgent), maxPriority),
			IssueID:         iss.ID,
			Labels:          iss.Labels,
			DetectedAt:      c.nowUTC(),
			Details:         urgent,
			UnblocksCount:   len(unblocks),
			SuggestedAction: "Schedule this issue next; it releases high-priority downstream work",
		})
	}
}

// checkAbandonedClaim flags in_progress issues that carry an assignee but
// have not been touched for longer than the in-progress stale threshold times
// AbandonedClaimMultiplier. stale_issue already warns at the in-progress
// threshold; this later, claim-specific signal says "release it".
func (c *Calculator) checkAbandonedClaim(result *Result) {
	if c.config.IsAlertDisabled(string(AlertAbandonedClaim)) || len(c.issues) == 0 {
		return
	}
	now := c.nowUTC()
	for _, iss := range sortedByID(c.issues) {
		if iss.Status != model.StatusInProgress || strings.TrimSpace(iss.Assignee) == "" {
			continue
		}
		lastActive := iss.UpdatedAt
		if lastActive.IsZero() {
			lastActive = iss.CreatedAt
		}
		if lastActive.IsZero() {
			continue
		}
		warnDays, _, inProgressMult := c.config.GetStalenessThresholds(iss.Labels)
		threshold := float64(warnDays)
		if inProgressMult > 0 {
			threshold *= inProgressMult
		}
		if c.config.AbandonedClaimMultiplier > 0 {
			threshold *= c.config.AbandonedClaimMultiplier
		}
		days := now.Sub(lastActive).Hours() / 24.0
		if days <= threshold {
			continue
		}
		result.Alerts = append(result.Alerts, Alert{
			Type:       AlertAbandonedClaim,
			Severity:   SeverityWarning,
			Message:    fmt.Sprintf("Claim on %s by %s idle for %.0f days", iss.ID, iss.Assignee, days),
			IssueID:    iss.ID,
			Labels:     iss.Labels,
			DetectedAt: now,
			Details: []string{
				fmt.Sprintf("assignee=%s", iss.Assignee),
				fmt.Sprintf("last_update=%s", lastActive.Format(time.RFC3339)),
				fmt.Sprintf("threshold_days=%.0f", threshold),
			},
			SuggestedAction: "Ask the assignee for status, or release the claim so the issue returns to the ready queue",
		})
	}
}

// checkPotentialDuplicate reuses the analysis package's keyword Jaccard
// detector (the same one behind --robot-suggest) and emits one info alert per
// pair, capped at DuplicateMaxAlerts.
func (c *Calculator) checkPotentialDuplicate(result *Result) {
	if c.config.IsAlertDisabled(string(AlertPotentialDuplicate)) || len(c.issues) < 2 {
		return
	}
	if !c.expensiveCheckAllowed(result, AlertPotentialDuplicate) {
		return
	}
	cfg := analysis.DefaultDuplicateConfig()
	if c.config.DuplicateJaccardThreshold > 0 {
		cfg.JaccardThreshold = c.config.DuplicateJaccardThreshold
	}
	maxAlerts := c.config.DuplicateMaxAlerts
	if maxAlerts > 0 {
		cfg.MaxSuggestions = maxAlerts
	}
	// Closed and tombstoned issues cannot be consolidated any more; pairing
	// them only buries the live duplicates under history.
	live := make([]model.Issue, 0, len(c.issues))
	for _, iss := range c.issues {
		if iss.Status == model.StatusClosed || iss.Status == model.StatusTombstone {
			continue
		}
		live = append(live, iss)
	}
	if len(live) < 2 {
		return
	}
	for i, suggestion := range analysis.DetectDuplicates(live, cfg) {
		if maxAlerts > 0 && i >= maxAlerts {
			break
		}
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertPotentialDuplicate,
			Severity:        SeverityInfo,
			Message:         suggestion.Summary,
			IssueID:         suggestion.TargetBead,
			RelatedIssueID:  suggestion.RelatedBead,
			DetectedAt:      c.nowUTC(),
			Details:         []string{suggestion.Reason},
			SuggestedAction: "Compare the two issues; close one as a duplicate or link them with a related dependency",
		})
	}
}

// checkPriorityMismatch surfaces --robot-priority's recommendations whose
// confidence reaches PriorityMismatchMinConfidence as warnings, so a stale
// P3 that the graph says is load-bearing shows up without a separate command.
func (c *Calculator) checkPriorityMismatch(result *Result) {
	if c.config.IsAlertDisabled(string(AlertPriorityMismatch)) || len(c.issues) == 0 {
		return
	}
	if !c.expensiveCheckAllowed(result, AlertPriorityMismatch) {
		return
	}
	minConfidence := c.config.PriorityMismatchMinConfidence
	if minConfidence <= 0 {
		return
	}
	thresholds := analysis.DefaultThresholds()
	if minConfidence > thresholds.MinConfidence {
		thresholds.MinConfidence = minConfidence
	}
	issueMap := c.issueMap()
	for _, rec := range c.analyzer().GenerateRecommendationsWithThresholds(thresholds) {
		// Only under-prioritised load-bearing issues are alerts; "could be
		// lower" recommendations are hygiene for --robot-priority, and on
		// small graphs they fire for nearly every leaf.
		if rec.Confidence < minConfidence || rec.Direction != "increase" {
			continue
		}
		var labels []string
		if issue := issueMap[rec.IssueID]; issue != nil {
			labels = issue.Labels
		}
		result.Alerts = append(result.Alerts, Alert{
			Type:            AlertPriorityMismatch,
			Severity:        SeverityWarning,
			Message:         fmt.Sprintf("%s is P%d but graph impact suggests P%d (confidence %.2f)", rec.IssueID, rec.CurrentPriority, rec.SuggestedPriority, rec.Confidence),
			IssueID:         rec.IssueID,
			Labels:          labels,
			BaselineVal:     float64(rec.CurrentPriority),
			CurrentVal:      float64(rec.SuggestedPriority),
			Delta:           float64(rec.SuggestedPriority - rec.CurrentPriority),
			DetectedAt:      c.nowUTC(),
			Details:         rec.Reasoning,
			SuggestedAction: fmt.Sprintf("Review with bv --robot-priority; if it holds, set the priority to P%d", rec.SuggestedPriority),
		})
	}
}
