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
	actionable := a.GetActionableIssues()

	// Build set of actionable IDs for quick lookup
	actionableSet := make(map[string]bool, len(actionable))
	for _, issue := range actionable {
		actionableSet[issue.ID] = true
	}

	// Calculate what each actionable issue unblocks
	unblocksMap := make(map[string][]string)
	for _, issue := range actionable {
		unblocksMap[issue.ID] = a.computeUnblocks(issue.ID)
	}

	// Find connected components among all issues (not just actionable)
	// This groups actionable issues that belong to the same work stream
	components := a.findConnectedComponents()

	// Build tracks from components, filtering to actionable issues only
	tracks := a.buildTracks(components, actionableSet, unblocksMap)

	// Calculate totals
	totalOpen := 0
	for _, issue := range a.issueMap {
		if issue.Status != model.StatusClosed {
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

// computeUnblocks finds issues that would become actionable if the given issue is closed
func (a *Analyzer) computeUnblocks(issueID string) []string {
	var unblocks []string

	for _, issue := range a.issueMap {
		// Skip closed issues
		if issue.Status == model.StatusClosed {
			continue
		}

		// Skip if this issue would still be blocked by other open issues
		wouldBeBlocked := false
		hasThisBlocker := false

		for _, dep := range issue.Dependencies {
			if dep.Type != model.DepBlocks {
				continue
			}

			if dep.DependsOnID == issueID {
				hasThisBlocker = true
				continue
			}

			// Check if there's another open blocker
			if blocker, exists := a.issueMap[dep.DependsOnID]; exists {
				if blocker.Status != model.StatusClosed {
					wouldBeBlocked = true
					break
				}
			}
		}

		// If this issue depends on issueID and would become unblocked
		if hasThisBlocker && !wouldBeBlocked {
			unblocks = append(unblocks, issue.ID)
		}
	}

	return unblocks
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
			parent[px] = py
		}
	}

	// Initialize all issues
	for id := range a.issueMap {
		parent[id] = id
	}

	// Union issues connected by dependencies (ignoring direction)
	for _, issue := range a.issueMap {
		for _, dep := range issue.Dependencies {
			if dep.Type == model.DepBlocks {
				if _, exists := a.issueMap[dep.DependsOnID]; exists {
					union(issue.ID, dep.DependsOnID)
				}
			}
		}
	}

	// Group by root
	components := make(map[string][]string)
	for id := range a.issueMap {
		root := find(id)
		components[root] = append(components[root], id)
	}

	return components
}

// buildTracks creates execution tracks from connected components
func (a *Analyzer) buildTracks(components map[string][]string, actionableSet map[string]bool, unblocksMap map[string][]string) []ExecutionTrack {
	var tracks []ExecutionTrack
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
				UnblocksIDs: unblocksMap[issue.ID],
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

	highestID := ""
	highestCount := -1

	for _, issue := range actionable {
		count := len(unblocksMap[issue.ID])
		if count > highestCount {
			highestCount = count
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
	// Handle tracks beyond 26 with AA, AB, etc.
	if n <= 26 {
		return "track-" + string(rune('A'+n-1))
	}
	// For n > 26, use two-letter IDs (AA, AB, ..., ZZ)
	first := (n - 1) / 26
	second := (n - 1) % 26
	return "track-" + string(rune('A'+first-1)) + string(rune('A'+second))
}
