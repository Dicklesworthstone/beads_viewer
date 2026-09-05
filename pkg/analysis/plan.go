package analysis

import (
	"sort"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// PlanItem represents a single actionable item in the execution plan
type PlanItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Priority    int      `json:"priority"`
	Status      string   `json:"status"`
	UnblocksIDs []string `json:"unblocks"` // Issues that become actionable when this is done
}

// ExecutionTrack represents a group of related actionable items
type ExecutionTrack struct {
	TrackID string     `json:"track_id"`
	Items   []PlanItem `json:"items"`
	Reason  string     `json:"reason"` // Why these are grouped
}

// ExecutionPlan is the complete work plan with parallel tracks
type ExecutionPlan struct {
	Tracks          []ExecutionTrack `json:"tracks"`
	TotalActionable int              `json:"total_actionable"`
	TotalBlocked    int              `json:"total_blocked"`
	Summary         PlanSummary      `json:"summary"`
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
	tracks := a.buildTracks(components, actionableSet, unblocksMap)

	// Calculate totals
	totalOpen := 0
	for _, issue := range a.issueMap {
		if a.IsCandidate(issue.ID) && !isClosedLikeStatus(issue.Status) {
			totalOpen++
		}
	}

	// Find highest impact issue
	summary := a.computePlanSummary(actionable, unblocksMap)

	return ExecutionPlan{
		Tracks:          tracks,
		TotalActionable: len(actionable),
		TotalBlocked:    totalOpen - len(actionable),
		Summary:         summary,
	}
}

// computeUnblocks finds direct hard-dependency successors that would become
// actionable if the given issue were closed. Parent-child edges are not direct
// successors, but a successor whose parent chain remains blocked is not reported
// as actionable. Scheduler-deferred successors are withheld as well.
func (a *Analyzer) computeUnblocks(issueID string) []string {
	var unblocks []string

	blockerNodeID, ok := a.idToNode[issueID]
	if !ok {
		return nil
	}
	blocker, ok := a.issueMap[issueID]
	if !ok || isClosedLikeStatus(blocker.Status) {
		// Closing an already-closed issue cannot change readiness.
		return nil
	}

	completed := map[string]bool{issueID: true}

	dependents := a.g.To(blockerNodeID)

	for dependents.Next() {
		dependentNode := dependents.Node()
		dependentID := a.nodeToID[dependentNode.ID()]
		if a.isActionableAfterCompletions(dependentID, completed) {
			unblocks = append(unblocks, dependentID)
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
	return exists && a.IsCandidate(issueID) && a.Readiness().ReadyAfter(issueID, a.now, completed)
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

	// Initialize all issues
	// Use sorted IDs for deterministic iteration order
	var ids []string
	for id := range a.issueMap {
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
func (a *Analyzer) buildTracks(components map[string][]string, actionableSet map[string]bool, unblocksMap map[string][]string) []ExecutionTrack {
	tracks := make([]ExecutionTrack, 0)
	trackNum := 1

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
			items[i] = PlanItem{
				ID:          issue.ID,
				Title:       issue.Title,
				Priority:    issue.Priority,
				Status:      string(issue.Status),
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
