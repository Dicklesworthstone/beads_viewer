package analysis

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// AtRiskSignal names one reason a sprint bead is flagged as at risk.
type AtRiskSignal string

const (
	// AtRiskBlockedTooLong fires when a bead has been blocked (status blocked,
	// or waiting on an open blocking dependency) for at least
	// AtRiskThresholds.BlockedDays.
	AtRiskBlockedTooLong AtRiskSignal = "blocked_too_long"
	// AtRiskNoActivity fires when a non-closed bead has had no update for at
	// least AtRiskThresholds.InactiveDays.
	AtRiskNoActivity AtRiskSignal = "no_activity"
	// AtRiskCriticalBlocked fires when a P0/P1 bead is blocked at all,
	// regardless of duration.
	AtRiskCriticalBlocked AtRiskSignal = "critical_blocked"
	// AtRiskBlockersNotClosing fires when at least one open blocker of the
	// bead has itself been inactive for AtRiskThresholds.InactiveDays: the
	// bead cannot close because its dependencies are not moving.
	AtRiskBlockersNotClosing AtRiskSignal = "blockers_not_closing"
)

// AllAtRiskSignals returns every signal DetectAtRisk can emit, in the order
// they are evaluated.
func AllAtRiskSignals() []AtRiskSignal {
	return []AtRiskSignal{
		AtRiskBlockedTooLong,
		AtRiskNoActivity,
		AtRiskCriticalBlocked,
		AtRiskBlockersNotClosing,
	}
}

// AtRiskThresholds configures the day counts behind the time-based signals.
// They are surfaced in .bv/drift.yaml as sprint_blocked_days and
// sprint_inactive_days (see pkg/drift.Config).
type AtRiskThresholds struct {
	// BlockedDays is the minimum blocked duration for blocked_too_long.
	BlockedDays int
	// InactiveDays is the minimum inactivity for no_activity and for a
	// blocker to count as stalled in blockers_not_closing.
	InactiveDays int
}

// DefaultAtRiskThresholds returns the documented defaults: blocked for 2+
// days, inactive for 4+ days.
func DefaultAtRiskThresholds() AtRiskThresholds {
	return AtRiskThresholds{BlockedDays: 2, InactiveDays: 4}
}

// normalized substitutes the defaults for unset (non-positive) thresholds.
func (t AtRiskThresholds) normalized() AtRiskThresholds {
	def := DefaultAtRiskThresholds()
	if t.BlockedDays <= 0 {
		t.BlockedDays = def.BlockedDays
	}
	if t.InactiveDays <= 0 {
		t.InactiveDays = def.InactiveDays
	}
	return t
}

// AtRiskItem is one sprint bead flagged by DetectAtRisk.
type AtRiskItem struct {
	ID       string         `json:"id"`
	Title    string         `json:"title,omitempty"`
	Status   string         `json:"status"`
	Priority int            `json:"priority"`
	Signals  []AtRiskSignal `json:"signals"`
	// Since is the earliest instant that triggered any of the signals: the
	// start of the blocked period, the last activity, or the last activity of
	// a stalled blocker.
	Since time.Time `json:"since"`
	// Detail is a human-readable explanation of each signal.
	Detail string `json:"detail"`
}

// DetectAtRisk evaluates the four at-risk signals for every non-closed bead in
// the sprint and returns the flagged beads sorted by ID. Blockers are resolved
// against the full issue list, so a blocker outside the sprint still counts.
//
// bv keeps no status-transition history, so "blocked since" is approximated by
// the most recent known change relevant to the block: the bead's own last
// update or the creation of an open blocking dependency, whichever is later.
// This is a lower bound on the blocked duration; it never overstates it.
//
// A nil sprint, an empty sprint, or an empty issue list yields an empty
// (non-nil) slice.
func DetectAtRisk(issues []model.Issue, sprint *model.Sprint, now time.Time, thresholds AtRiskThresholds) []AtRiskItem {
	return DetectAtRiskWithReadiness(issues, sprint, nil, now, thresholds)
}

// DetectAtRiskWithReadiness evaluates the four at-risk signals using a caller-supplied
// ReadinessIndex (preserving TombstoneIDs and cross-repository blockers when scoped),
// falling back to model.NewReadinessIndex(issues) when readiness is nil.
func DetectAtRiskWithReadiness(issues []model.Issue, sprint *model.Sprint, readiness *model.ReadinessIndex, now time.Time, thresholds AtRiskThresholds) []AtRiskItem {
	items := []AtRiskItem{}
	if sprint == nil || len(sprint.BeadIDs) == 0 || len(issues) == 0 {
		return items
	}
	thresholds = thresholds.normalized()
	now = now.UTC()

	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}

	seen := make(map[string]bool, len(sprint.BeadIDs))
	for _, beadID := range sprint.BeadIDs {
		if seen[beadID] {
			continue
		}
		seen[beadID] = true
		iss, ok := issueMap[beadID]
		if !ok || iss.IsParkedForStalenessAt(now) {
			continue
		}
		if item, flagged := evaluateAtRisk(iss, issueMap, readiness, now, thresholds); flagged {
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// evaluateAtRisk applies the four signals to a single non-closed bead.
func evaluateAtRisk(iss model.Issue, issueMap map[string]model.Issue, readiness *model.ReadinessIndex, now time.Time, thresholds AtRiskThresholds) (AtRiskItem, bool) {
	lastActivity := issueLastActivity(iss)

	// Consult ReadinessIndex so transitive DepParentChild epic blockers and
	// DependenciesUnknown missing blocker IDs are recognized alongside direct
	// open blocking dependencies, while open unblocked parents remain non-blocking.
	// Parent epics themselves stay open until all children finish — they do NOT
	// close to unblock a child, so exclude DepParentChild-only targets from
	// openBlockers and track their actual IsBlocking() blockers in ancestorBlockers.
	type blocker struct {
		id           string
		lastActivity time.Time
	}
	directBlockingTargets := make(map[string]bool, len(iss.Dependencies))
	parentChildTargets := make(map[string]bool, len(iss.Dependencies))
	for _, dep := range iss.Dependencies {
		if dep == nil {
			continue
		}
		if dep.Type.IsBlocking() {
			directBlockingTargets[dep.DependsOnID] = true
		}
		if dep.Type == model.DepParentChild {
			parentChildTargets[dep.DependsOnID] = true
		}
	}

	blockerIDs := readiness.Blockers(iss.ID)
	blockerSet := make(map[string]bool, len(blockerIDs))
	var openBlockers []blocker
	for _, bID := range blockerIDs {
		blockerSet[bID] = true
		if parentChildTargets[bID] && !directBlockingTargets[bID] {
			continue
		}
		if target, ok := issueMap[bID]; ok {
			openBlockers = append(openBlockers, blocker{id: bID, lastActivity: issueLastActivity(target)})
		} else {
			openBlockers = append(openBlockers, blocker{id: bID})
		}
	}

	blockedSince := lastActivity
	hasBlockedParent := false
	for _, dep := range iss.Dependencies {
		if dep == nil || !(dep.Type.IsBlocking() || dep.Type == model.DepParentChild) {
			continue
		}
		if !blockerSet[dep.DependsOnID] {
			continue
		}
		if dep.Type == model.DepParentChild {
			hasBlockedParent = true
		}
		if !dep.CreatedAt.IsZero() && dep.CreatedAt.UTC().After(blockedSince) {
			blockedSince = dep.CreatedAt.UTC()
		}
	}

	var ancestorBlockers []blocker
	if hasBlockedParent {
		visitedParents := map[string]bool{iss.ID: true}
		var parentQueue []string
		for _, dep := range iss.Dependencies {
			if dep != nil && dep.Type == model.DepParentChild && blockerSet[dep.DependsOnID] && !visitedParents[dep.DependsOnID] {
				visitedParents[dep.DependsOnID] = true
				parentQueue = append(parentQueue, dep.DependsOnID)
			}
		}
		seenAncBlocker := make(map[string]bool, len(openBlockers))
		for _, b := range openBlockers {
			seenAncBlocker[b.id] = true
		}
		for head := 0; head < len(parentQueue); head++ {
			pID := parentQueue[head]
			parentIss, ok := issueMap[pID]
			if !ok || isClosedLikeStatus(parentIss.Status) {
				continue
			}
			pBlockerSet := make(map[string]bool)
			for _, pb := range readiness.Blockers(pID) {
				pBlockerSet[pb] = true
			}
			for _, dep := range parentIss.Dependencies {
				if dep == nil || !pBlockerSet[dep.DependsOnID] {
					continue
				}
				if dep.Type == model.DepParentChild {
					if !visitedParents[dep.DependsOnID] {
						visitedParents[dep.DependsOnID] = true
						parentQueue = append(parentQueue, dep.DependsOnID)
					}
				} else if dep.Type.IsBlocking() {
					if !dep.CreatedAt.IsZero() && dep.CreatedAt.UTC().After(blockedSince) {
						blockedSince = dep.CreatedAt.UTC()
					}
					if !seenAncBlocker[dep.DependsOnID] {
						seenAncBlocker[dep.DependsOnID] = true
						if target, ok := issueMap[dep.DependsOnID]; ok {
							ancestorBlockers = append(ancestorBlockers, blocker{id: dep.DependsOnID, lastActivity: issueLastActivity(target)})
						} else {
							ancestorBlockers = append(ancestorBlockers, blocker{id: dep.DependsOnID})
						}
					}
				}
			}
		}
		sort.Slice(ancestorBlockers, func(i, j int) bool { return ancestorBlockers[i].id < ancestorBlockers[j].id })
	}

	sort.Slice(openBlockers, func(i, j int) bool { return openBlockers[i].id < openBlockers[j].id })

	blocked := iss.Status == model.StatusBlocked ||
		readiness.DependencyState(iss.ID) != model.DependenciesSatisfied ||
		len(openBlockers) > 0

	var signals []AtRiskSignal
	var details []string
	var since time.Time
	noteSince := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if since.IsZero() || t.Before(since) {
			since = t
		}
	}

	// blocked_too_long
	if blocked && !blockedSince.IsZero() {
		blockedDays := daysBetween(blockedSince, now)
		if blockedDays >= float64(thresholds.BlockedDays) {
			signals = append(signals, AtRiskBlockedTooLong)
			details = append(details, fmt.Sprintf("blocked for %.0fd (threshold %dd)", blockedDays, thresholds.BlockedDays))
			noteSince(blockedSince)
		}
	}

	// no_activity
	if !lastActivity.IsZero() {
		idleDays := daysBetween(lastActivity, now)
		if idleDays >= float64(thresholds.InactiveDays) {
			signals = append(signals, AtRiskNoActivity)
			details = append(details, fmt.Sprintf("no activity for %.0fd (threshold %dd)", idleDays, thresholds.InactiveDays))
			noteSince(lastActivity)
		}
	}

	// critical_blocked
	if blocked && iss.Priority <= 1 {
		signals = append(signals, AtRiskCriticalBlocked)
		by := "status=blocked"
		if len(openBlockers) > 0 {
			ids := make([]string, len(openBlockers))
			for i, b := range openBlockers {
				ids[i] = b.id
			}
			by = "blocked by " + strings.Join(ids, ", ")
		} else if len(ancestorBlockers) > 0 {
			ids := make([]string, len(ancestorBlockers))
			for i, b := range ancestorBlockers {
				ids[i] = b.id
			}
			by = "blocked by " + strings.Join(ids, ", ")
		} else if len(blockerIDs) > 0 {
			by = "blocked by " + strings.Join(blockerIDs, ", ")
		}
		details = append(details, fmt.Sprintf("P%d %s", iss.Priority, by))
		if !blockedSince.IsZero() {
			noteSince(blockedSince)
		} else {
			noteSince(lastActivity)
		}
	}

	// blockers_not_closing
	if len(openBlockers) > 0 || len(ancestorBlockers) > 0 {
		stalledCandidates := make([]blocker, 0, len(openBlockers)+len(ancestorBlockers))
		stalledCandidates = append(stalledCandidates, openBlockers...)
		stalledCandidates = append(stalledCandidates, ancestorBlockers...)
		sort.Slice(stalledCandidates, func(i, j int) bool { return stalledCandidates[i].id < stalledCandidates[j].id })
		var stalled []string
		for _, b := range stalledCandidates {
			if b.lastActivity.IsZero() {
				continue
			}
			idle := daysBetween(b.lastActivity, now)
			if idle >= float64(thresholds.InactiveDays) {
				stalled = append(stalled, fmt.Sprintf("%s (%.0fd idle)", b.id, idle))
				noteSince(b.lastActivity)
			}
		}
		if len(stalled) > 0 {
			signals = append(signals, AtRiskBlockersNotClosing)
			details = append(details, "blockers stalled: "+strings.Join(stalled, ", "))
		}
	}

	if len(signals) == 0 {
		return AtRiskItem{}, false
	}
	if since.IsZero() {
		since = lastActivity
	}
	return AtRiskItem{
		ID:       iss.ID,
		Title:    iss.Title,
		Status:   string(iss.Status),
		Priority: iss.Priority,
		Signals:  signals,
		Since:    since,
		Detail:   strings.Join(details, "; "),
	}, true
}

// issueLastActivity returns the bead's last known change: updated_at, falling
// back to created_at. Zero when neither is set.
func issueLastActivity(iss model.Issue) time.Time {
	if !iss.UpdatedAt.IsZero() {
		return iss.UpdatedAt.UTC()
	}
	if !iss.CreatedAt.IsZero() {
		return iss.CreatedAt.UTC()
	}
	return time.Time{}
}

// daysBetween returns the elapsed days from t to now as a float; negative when
// t is in the future.
func daysBetween(t, now time.Time) float64 {
	return now.Sub(t).Hours() / 24.0
}

// SprintBurndown summarizes sprint progress, daily closure curve, burn rates,
// and completion projection shared by --robot-burndown and the TUI Sprint View.
type SprintBurndown struct {
	TotalDays         int                   `json:"total_days"`
	ElapsedDays       int                   `json:"elapsed_days"`
	RemainingDays     int                   `json:"remaining_days"`
	TotalIssues       int                   `json:"total_issues"`
	CompletedIssues   int                   `json:"completed_issues"`
	RemainingIssues   int                   `json:"remaining_issues"`
	IdealBurnRate     float64               `json:"ideal_burn_rate"`
	ActualBurnRate    float64               `json:"actual_burn_rate"`
	ProjectedComplete *time.Time            `json:"projected_complete,omitempty"`
	OnTrack           bool                  `json:"on_track"`
	DailyPoints       []model.BurndownPoint `json:"daily_points"`
	IdealLine         []model.BurndownPoint `json:"ideal_line"`
}

// ComputeSprintBurndown computes deterministic daily burndown points, ideal line,
// burn rates, and completion projection for the provided sprint and its issues.
func ComputeSprintBurndown(sprint *model.Sprint, sprintIssues []model.Issue, now time.Time) SprintBurndown {
	if sprint == nil {
		return SprintBurndown{OnTrack: true}
	}

	totalIssues := len(sprintIssues)
	completedIssues := 0
	for _, iss := range sprintIssues {
		if isClosedLikeStatus(iss.Status) {
			completedIssues++
		}
	}
	remainingIssues := totalIssues - completedIssues

	totalDays := 0
	elapsedDays := 0
	remainingDays := 0
	if !sprint.StartDate.IsZero() && !sprint.EndDate.IsZero() && !sprint.EndDate.Before(sprint.StartDate) {
		totalDays = inclusiveSprintDays(sprint.StartDate, sprint.EndDate)
		if now.Before(sprint.StartDate) {
			elapsedDays = 0
			remainingDays = totalDays
		} else if now.After(sprint.EndDate) {
			elapsedDays = totalDays
			remainingDays = 0
		} else {
			elapsedDays = min(inclusiveSprintDays(sprint.StartDate, now), totalDays)
			remainingDays = max(totalDays-elapsedDays, 0)
		}
	}

	idealBurnRate := 0.0
	if totalDays > 0 {
		idealBurnRate = float64(totalIssues) / float64(totalDays)
	}

	actualBurnRate := 0.0
	if elapsedDays > 0 {
		actualBurnRate = float64(completedIssues) / float64(elapsedDays)
	}

	var projectedComplete *time.Time
	onTrack := true
	if actualBurnRate > 0 && remainingIssues > 0 {
		daysToComplete := float64(remainingIssues) / actualBurnRate
		projected := now.AddDate(0, 0, int(daysToComplete)+1)
		projectedComplete = &projected
		if !sprint.EndDate.IsZero() {
			onTrack = !projected.After(sprint.EndDate)
		}
	} else if remainingIssues == 0 {
		onTrack = true
	} else if elapsedDays > 0 && completedIssues == 0 {
		onTrack = false
	}

	return SprintBurndown{
		TotalDays:         totalDays,
		ElapsedDays:       elapsedDays,
		RemainingDays:     remainingDays,
		TotalIssues:       totalIssues,
		CompletedIssues:   completedIssues,
		RemainingIssues:   remainingIssues,
		IdealBurnRate:     idealBurnRate,
		ActualBurnRate:    actualBurnRate,
		ProjectedComplete: projectedComplete,
		OnTrack:           onTrack,
		DailyPoints:       GenerateDailyBurndown(sprint, sprintIssues, now),
		IdealLine:         GenerateIdealBurndownLine(sprint, totalIssues),
	}
}

// inclusiveSprintDays counts the exact number of calendar-day steps produced by
// iterating d = start; !d.After(end); d = d.AddDate(0, 0, 1) in O(1), safely
// handling DST transitions (23h/25h days) and returning 0 when end < start.
func inclusiveSprintDays(start, end time.Time) int {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	endInStartLoc := end.In(start.Location())
	startDay := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endDay := time.Date(endInStartLoc.Year(), endInStartLoc.Month(), endInStartLoc.Day(), 0, 0, 0, 0, time.UTC)
	k := int(endDay.Sub(startDay) / (24 * time.Hour))
	if k < 0 {
		return 0
	}
	for k >= 0 && start.AddDate(0, 0, k).After(end) {
		k--
	}
	if k < 0 {
		return 0
	}
	for !start.AddDate(0, 0, k+1).After(end) {
		k++
	}
	return k + 1
}

// GenerateDailyBurndown creates actual day-by-day remaining/completed points
// from issue closure timestamps (falling back to UpdatedAt, then CreatedAt,
// and counting timeless closed/tombstone issues as completed on day 1 so
// DailyPoints always reconciles with CompletedIssues).
func GenerateDailyBurndown(sprint *model.Sprint, issues []model.Issue, now time.Time) []model.BurndownPoint {
	if sprint == nil || sprint.StartDate.IsZero() || sprint.EndDate.IsZero() || sprint.EndDate.Before(sprint.StartDate) {
		return nil
	}

	var points []model.BurndownPoint
	totalIssues := len(issues)

	for d := sprint.StartDate; !d.After(sprint.EndDate) && !d.After(now); d = d.AddDate(0, 0, 1) {
		nextDay := d.AddDate(0, 0, 1)
		completed := 0

		for _, iss := range issues {
			if !isClosedLikeStatus(iss.Status) {
				continue
			}
			closedTime := time.Time{}
			if iss.ClosedAt != nil && !iss.ClosedAt.IsZero() {
				closedTime = *iss.ClosedAt
			} else if !iss.UpdatedAt.IsZero() {
				closedTime = iss.UpdatedAt
			} else if !iss.CreatedAt.IsZero() {
				closedTime = iss.CreatedAt
			}
			if closedTime.IsZero() || closedTime.Before(nextDay) {
				completed++
			}
		}

		points = append(points, model.BurndownPoint{
			Date:      d,
			Remaining: totalIssues - completed,
			Completed: completed,
		})
	}

	return points
}

// GenerateIdealBurndownLine creates the linear ideal burndown curve for a sprint.
func GenerateIdealBurndownLine(sprint *model.Sprint, totalIssues int) []model.BurndownPoint {
	if sprint == nil || sprint.StartDate.IsZero() || sprint.EndDate.IsZero() || sprint.EndDate.Before(sprint.StartDate) || totalIssues == 0 {
		return nil
	}

	totalDays := inclusiveSprintDays(sprint.StartDate, sprint.EndDate)
	if totalDays <= 0 {
		return nil
	}
	burnPerDay := float64(totalIssues) / float64(totalDays)

	var points []model.BurndownPoint
	for i := 0; i <= totalDays; i++ {
		d := sprint.StartDate.AddDate(0, 0, i)
		remaining := totalIssues - int(float64(i)*burnPerDay)
		if remaining < 0 {
			remaining = 0
		}
		points = append(points, model.BurndownPoint{
			Date:      d,
			Remaining: remaining,
			Completed: totalIssues - remaining,
		})
	}

	return points
}
