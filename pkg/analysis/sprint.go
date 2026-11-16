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
	readiness := model.NewReadinessIndex(issues)

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
	type blocker struct {
		id           string
		lastActivity time.Time
	}
	blockerIDs := readiness.Blockers(iss.ID)
	blockerSet := make(map[string]bool, len(blockerIDs))
	var openBlockers []blocker
	for _, bID := range blockerIDs {
		blockerSet[bID] = true
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
		seenAncBlocker := make(map[string]bool, len(blockerSet))
		for k := range blockerSet {
			seenAncBlocker[k] = true
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
