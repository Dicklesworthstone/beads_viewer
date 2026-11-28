package analysis

import (
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// PlanItem represents a single actionable item in the execution plan
type PlanItem struct {
	ID                 string              `json:"id"`
	Title              string              `json:"title"`
	Priority           int                 `json:"priority"`
	Status             string              `json:"status"`
	Assignee           string              `json:"assignee,omitempty"`
	Claimable          bool                `json:"claimable"`
	NotReady           bool                `json:"not_ready,omitempty"`
	UnblocksIDs        []string            `json:"unblocks"` // Issues that become actionable when this is done
	TransitiveUnblocks int                 `json:"transitive_unblocks,omitempty"`
	EstimatedDaysSaved float64             `json:"estimated_days_saved,omitempty"`
	ETADays            *float64            `json:"eta_days,omitempty"`
	ETADate            string              `json:"eta_date,omitempty"`
	ETAConfidence      *float64            `json:"eta_confidence,omitempty"`
	RiskScore          float64             `json:"risk_score,omitempty"`
	RiskExplanation    string              `json:"risk_explanation,omitempty"`
	Actions            *model.IssueActions `json:"actions,omitempty"`
}

// PlanCapacity summarizes Graham multi-agent scheduling bounds over a scoped open issue set (W37-3/W37-4).
type PlanCapacity struct {
	OpenCount           int      `json:"open_count"`
	TotalMinutes        int      `json:"total_minutes"`
	CriticalPathMinutes int      `json:"critical_path_minutes"`
	ParallelizablePct   float64  `json:"parallelizable_pct"`
	Days1Agent          float64  `json:"days_1_agent"`
	Days2AgentsMin      float64  `json:"days_2_agents_min"`
	Days2AgentsMax      float64  `json:"days_2_agents_max"`
	Days4AgentsMin      float64  `json:"days_4_agents_min"`
	Days4AgentsMax      float64  `json:"days_4_agents_max"`
	CriticalPathIDs     []string `json:"critical_path_ids,omitempty"`
}

// ExecutionTrack represents a group of related actionable items
type ExecutionTrack struct {
	TrackID string     `json:"track_id"`
	Items   []PlanItem `json:"items"`
	Reason  string     `json:"reason"` // Why these are grouped
}

// SelectionMode controls candidate selection across triage, priority, what-if,
// plan, script emission, briefs, and exports (Choke-Point 1).
type SelectionMode int

const (
	// SelectionReadyLeaf selects scoped candidate issues that are actionable now
	// (open or in_progress, not future-deferred, no unsatisfied blocking
	// dependencies) and are not open container epics with open parent-child
	// children.
	SelectionReadyLeaf SelectionMode = iota
	// SelectionActionable selects all scoped candidate issues that are
	// actionable now (including container epics when needed by callers).
	SelectionActionable
	// SelectionAllScopedCandidates selects all non-closed candidate issues in
	// scope (including blocked, deferred, parked, and container epics) for
	// --robot-include-inactive diagnostics.
	SelectionAllScopedCandidates
)

// CandidateIssueSet returns the canonical set of issue IDs matching mode under
// the analyzer's CandidateIDs scope, ReadinessIndex, and reference clock.
func (a *Analyzer) CandidateIssueSet(mode SelectionMode) map[string]bool {
	allowed := make(map[string]bool)
	if a == nil || len(a.issueMap) == 0 {
		return allowed
	}
	readiness := a.Readiness()
	now := a.Now()
	var parentsWithOpenChildren map[string]bool
	if mode == SelectionReadyLeaf {
		parentsWithOpenChildren = a.ParentsWithOpenChildren()
	}
	for id, issue := range a.issueMap {
		if !a.IsCandidate(id) || isClosedLikeStatus(issue.Status) {
			continue
		}
		switch mode {
		case SelectionAllScopedCandidates:
			allowed[id] = true
		case SelectionActionable:
			if readiness.Ready(id, now) {
				allowed[id] = true
			}
		case SelectionReadyLeaf:
			if !readiness.Ready(id, now) {
				continue
			}
			if issue.IssueType == model.TypeEpic && parentsWithOpenChildren[id] {
				continue
			}
			allowed[id] = true
		}
	}
	return allowed
}

// CandidateIssues returns the deterministic ID-sorted slice of issues matching
// mode under the analyzer's CandidateIDs scope, ReadinessIndex, and clock.
func (a *Analyzer) CandidateIssues(mode SelectionMode) []model.Issue {
	allowed := a.CandidateIssueSet(mode)
	if len(allowed) == 0 {
		return []model.Issue{}
	}
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := a.issueMap[id]; ok {
			out = append(out, issue)
		}
	}
	return out
}

// StatusPartition partitions candidate issues into mutually exclusive readiness
// buckets: Closed + Actionable + Blocked + DeferredOrParked == Total.
type StatusPartition struct {
	Total            int `json:"total"`
	Closed           int `json:"closed"`
	NotClosed        int `json:"not_closed"`
	Actionable       int `json:"actionable"`
	Blocked          int `json:"blocked"`
	DeferredOrParked int `json:"deferred_or_parked"`
	InProgress       int `json:"in_progress"`
	OpenStrict       int `json:"open_strict"`
	BlockedStrict    int `json:"blocked_strict"`
}

// CountStatusPartition computes canonical readiness and status counts across all
// candidate issues owned by the analyzer. Dependency-blocked open/in_progress
// issues and status="blocked" issues count under Blocked, while deferred,
// future defer_until, draft, pinned, hooked, review, and custom parked statuses
// count under DeferredOrParked.
func (a *Analyzer) CountStatusPartition() StatusPartition {
	var p StatusPartition
	if a == nil {
		return p
	}
	readiness := a.Readiness()
	now := a.Now()
	for id, issue := range a.issueMap {
		if !a.IsCandidate(id) {
			continue
		}
		p.Total++
		if issue.Status == model.StatusOpen {
			p.OpenStrict++
		}
		if issue.Status == model.StatusBlocked {
			p.BlockedStrict++
		}
		if issue.Status == model.StatusInProgress {
			p.InProgress++
		}
		if isClosedLikeStatus(issue.Status) {
			p.Closed++
			continue
		}
		p.NotClosed++
		switch {
		case readiness.Ready(id, now):
			p.Actionable++
		case issue.Status == model.StatusBlocked:
			p.Blocked++
		case !isActionableStatus(issue.Status) || issue.IsDeferredAt(now) || isWithheldIssue(&issue):
			p.DeferredOrParked++
		default:
			p.Blocked++
		}
	}
	return p
}

// ExecutionPlan is the complete work plan with parallel tracks
type ExecutionPlan struct {
	Tracks                []ExecutionTrack `json:"tracks"`
	TotalActionable       int              `json:"total_actionable"`
	TotalBlocked          int              `json:"total_blocked"`
	TotalDeferredOrParked int              `json:"total_deferred_or_parked,omitempty"`
	TotalInProgress       int              `json:"total_in_progress,omitempty"`
	Capacity              *PlanCapacity    `json:"capacity,omitempty"`
	Summary               PlanSummary      `json:"summary"`
}

// PlanSummary provides quick insights about the plan
type PlanSummary struct {
	HighestImpact string `json:"highest_impact"` // Issue ID that unblocks the most
	ImpactReason  string `json:"impact_reason"`  // Why it's highest impact
	UnblocksCount int    `json:"unblocks_count"` // How many it unblocks
}

// GetExecutionPlan generates a dependency-respecting execution plan
// with parallel tracks identified for concurrent work.
func (a *Analyzer) GetExecutionPlan() ExecutionPlan {
	return a.GetExecutionPlanWithOptions(nil)
}

// GetExecutionPlanWithOptions builds the dependency-respecting execution plan
// and annotates each actionable track item with claimability and opt-in
// NotReadyLabels status without dropping track topology (W31-5).
func (a *Analyzer) GetExecutionPlanWithOptions(notReadyLabels []string) ExecutionPlan {
	actionable := a.getActionableIssuesAfterCompletions(nil)

	// Build set of actionable IDs for quick lookup
	actionableSet := make(map[string]bool, len(actionable))
	for _, issue := range actionable {
		actionableSet[issue.ID] = true
	}

	// Create a TriageContext to correctly calculate what each actionable issue unblocks,
	// properly accounting for parent-child blocking relationships (bv-96).
	triageCtx := NewTriageContext(a)
	unblocksMap := triageCtx.UnblocksMap()

	// Find connected components among all issues (not just actionable)
	// This groups actionable issues that belong to the same work stream
	components := a.findConnectedComponents()

	// Build tracks from components, filtering to actionable issues only
	tracks := a.buildTracks(components, actionableSet, unblocksMap, notReadyLabels)

	// Partition candidate issues into actionable, blocked, and deferred/parked
	partition := a.CountStatusPartition()

	// Find highest impact issue
	summary := a.computePlanSummary(actionable, unblocksMap)

	return ExecutionPlan{
		Tracks:                tracks,
		TotalActionable:       len(actionable),
		TotalBlocked:          partition.Blocked,
		TotalDeferredOrParked: partition.DeferredOrParked,
		TotalInProgress:       partition.InProgress,
		Summary:               summary,
	}
}

// computeUnblocks finds in-graph issues whose readiness transitions from false
// to true if issueID were closed in a single step, including DepParentChild
// descendants of an unblocked parent whose own blockers are otherwise satisfied.
// Already-ready issues (e.g., children of an already-ready parent) and
// scheduler-deferred successors are never reported as newly unblocked.
func (a *Analyzer) computeUnblocks(issueID string) []string {
	if _, ok := a.idToNode[issueID]; !ok {
		return nil
	}
	blocker, ok := a.issueMap[issueID]
	if !ok || isClosedLikeStatus(blocker.Status) {
		// Closing an already-closed issue cannot change readiness.
		return nil
	}

	frontier := a.directUnlockFrontier(issueID)
	if len(frontier) == 0 {
		return nil
	}

	var unblocks []string
	for _, candID := range frontier {
		if candID == issueID {
			continue
		}
		if !a.isActionableAfterSingleCompletion(candID, "") && a.isActionableAfterSingleCompletion(candID, issueID) {
			unblocks = append(unblocks, candID)
		}
	}

	sort.Strings(unblocks)
	return unblocks
}

// isActionableAfterCompletions applies the same readiness contract as
// GetActionableIssues to one issue without rebuilding the whole actionable set.
// It is used by bounded what-if traversals where only a small affected frontier
// needs to be checked.
func (a *Analyzer) isActionableAfterCompletions(issueID string, completed map[string]bool) bool {
	_, exists := a.issueMap[issueID]
	return exists && a.IsUnblockCandidate(issueID) && a.Readiness().ReadyAfter(issueID, a.now, completed)
}

// isActionableAfterSingleCompletion evaluates isActionableAfterCompletions for
// a single simulated completion without allocating a map per call.
func (a *Analyzer) isActionableAfterSingleCompletion(issueID, completedID string) bool {
	_, exists := a.issueMap[issueID]
	return exists && a.IsUnblockCandidate(issueID) && a.Readiness().ReadyAfterSingle(issueID, a.now, completedID)
}

// ComputeUnblocks is an exported wrapper for computeUnblocks for consumers outside analysis.
func (a *Analyzer) ComputeUnblocks(issueID string) []string {
	return a.computeUnblocks(issueID)
}

// findConnectedComponents uses union-find to group related issues
func (a *Analyzer) findConnectedComponents() map[string][]string {
	// Simple union-find
	parent := make(map[string]string)

	var find func(x string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}

	union := func(x, y string) {
		px, py := find(x), find(y)
		if px != py {
			// Deterministic merge: always attach larger ID to smaller ID (or vice versa)
			// This ensures the root choice doesn't depend on visitation order,
			// though finding the MST still requires stable iteration.
			if px < py {
				parent[py] = px
			} else {
				parent[px] = py
			}
		}
	}

	// Initialize candidate issues (or all issues when unscoped)
	// Use sorted IDs for deterministic iteration order
	var ids []string
	for id := range a.issueMap {
		if !a.IsCandidate(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		parent[id] = id
	}

	// Union issues connected by dependencies (ignoring direction)
	// Iterate in sorted order to ensure deterministic tree structure
	for _, id := range ids {
		issue := a.issueMap[id]
		for _, dep := range issue.Dependencies {
			if dep != nil && (dep.Type.IsBlocking() || dep.Type == model.DepParentChild) {
				if !a.IsCandidate(dep.DependsOnID) {
					continue
				}
				if _, exists := a.issueMap[dep.DependsOnID]; exists {
					union(issue.ID, dep.DependsOnID)
				}
			}
		}
	}

	// Group by root
	components := make(map[string][]string)
	for _, id := range ids {
		root := find(id)
		components[root] = append(components[root], id)
	}

	return components
}

// buildTracks creates execution tracks from connected components
func (a *Analyzer) buildTracks(components map[string][]string, actionableSet map[string]bool, unblocksMap map[string][]string, notReadyLabels []string) []ExecutionTrack {
	tracks := make([]ExecutionTrack, 0)
	trackNum := 1
	readiness := a.Readiness()
	now := a.Now()
	parentsWithOpenChildren := a.ParentsWithOpenChildren()

	// Sort component roots for deterministic output
	var roots []string
	for root := range components {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	for _, root := range roots {
		members := components[root]

		// Filter to actionable issues only
		var actionableMembers []model.Issue
		for _, id := range members {
			if actionableSet[id] {
				actionableMembers = append(actionableMembers, a.issueMap[id])
			}
		}

		if len(actionableMembers) == 0 {
			continue
		}

		// Sort by priority (ascending = higher priority first), then by ID
		sort.Slice(actionableMembers, func(i, j int) bool {
			if actionableMembers[i].Priority != actionableMembers[j].Priority {
				return actionableMembers[i].Priority < actionableMembers[j].Priority
			}
			return actionableMembers[i].ID < actionableMembers[j].ID
		})

		// Build plan items
		items := make([]PlanItem, len(actionableMembers))
		for i, issue := range actionableMembers {
			isNotReady := hasAnyLabel(issue.Labels, notReadyLabels)
			_, safeID := quoteBeadsCommandID(issue.ID)
			isClaimable := safeID &&
				readiness.Claimable(issue.ID, now) &&
				!isNotReady &&
				!parentsWithOpenChildren[issue.ID] &&
				issue.IssueType != model.TypeEpic &&
				strings.TrimSpace(issue.Assignee) == ""
			items[i] = PlanItem{
				ID:          issue.ID,
				Title:       issue.Title,
				Priority:    issue.Priority,
				Status:      string(issue.Status),
				Assignee:    issue.Assignee,
				Claimable:   isClaimable,
				NotReady:    isNotReady,
				UnblocksIDs: append([]string{}, unblocksMap[issue.ID]...),
			}
		}

		// Determine track reason
		reason := "Independent work stream"
		if len(actionableMembers) == 1 {
			reason = "Single actionable item"
		} else if len(components) == 1 {
			reason = "All issues in connected graph"
		}

		tracks = append(tracks, ExecutionTrack{
			TrackID: generateTrackID(trackNum),
			Items:   items,
			Reason:  reason,
		})
		trackNum++
	}

	return tracks
}

// computePlanSummary finds the highest-impact actionable issue
func (a *Analyzer) computePlanSummary(actionable []model.Issue, unblocksMap map[string][]string) PlanSummary {
	if len(actionable) == 0 {
		return PlanSummary{}
	}

	// Sort actionable issues by ID for deterministic tie-breaking
	// We make a shallow copy to avoid modifying the caller's slice order,
	// although in GetExecutionPlan it wouldn't matter much.
	sortedActionable := make([]model.Issue, len(actionable))
	copy(sortedActionable, actionable)
	sort.Slice(sortedActionable, func(i, j int) bool {
		return sortedActionable[i].ID < sortedActionable[j].ID
	})

	// Tie-break order: most unblocks, then highest priority (lowest number),
	// then lowest ID (the slice is already ID-sorted, so the first winner on
	// (count, priority) is the lowest ID).
	highestID := ""
	highestCount := -1
	highestPriority := 0

	for _, issue := range sortedActionable {
		count := len(unblocksMap[issue.ID])
		if count > highestCount || (count == highestCount && issue.Priority < highestPriority) {
			highestCount = count
			highestPriority = issue.Priority
			highestID = issue.ID
		}
	}

	reason := "No downstream dependencies"
	if highestCount == 1 {
		reason = "Unblocks 1 task"
	} else if highestCount > 1 {
		reason = "Unblocks multiple tasks"
	}

	return PlanSummary{
		HighestImpact: highestID,
		ImpactReason:  reason,
		UnblocksCount: highestCount,
	}
}

func generateTrackID(n int) string {
	// Convert 1-based n to base-26 alphabetic (A, B, ..., Z, AA, AB, ...).
	// Works for arbitrarily many tracks.
	if n <= 0 {
		return "track-?"
	}

	n-- // switch to 0-based for conversion
	var letters []rune
	for n >= 0 {
		letters = append([]rune{rune('A' + (n % 26))}, letters...)
		n = n/26 - 1
	}

	return "track-" + string(letters)
}

// GenerateTrackIDForTest is exposed for tests only.
// Do not use outside tests; keep generateTrackID unexported in production.
func GenerateTrackIDForTest(n int) string {
	return generateTrackID(n)
}
