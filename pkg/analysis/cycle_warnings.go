package analysis

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// CycleWarningConfig configures cycle warning generation
type CycleWarningConfig struct {
	// MaxCycles is the maximum number of cycles to report
	// Default: 10
	MaxCycles int

	// IncludeSelfLoops whether to report self-referencing dependencies
	// Default: true
	IncludeSelfLoops bool
}

// DefaultCycleWarningConfig returns sensible defaults
func DefaultCycleWarningConfig() CycleWarningConfig {
	return CycleWarningConfig{
		MaxCycles:        10,
		IncludeSelfLoops: true,
	}
}

// DetectCycleWarnings generates suggestions for dependency cycles in the graph
func DetectCycleWarnings(issues []model.Issue, config CycleWarningConfig) []Suggestion {
	if len(issues) == 0 || config.MaxCycles <= 0 {
		return nil
	}

	// Build the analyzer to access cycle detection
	analyzer := NewAnalyzer(issues)

	// Only compute cycles for performance
	maxCyclesToStore := config.MaxCycles
	if !config.IncludeSelfLoops && maxCyclesToStore < len(issues) {
		// Cycle detection returns at most one cycle per SCC. Scan every SCC when
		// self-loops are filtered so skipped cycles cannot hide a later reportable
		// cycle merely because the output limit is small.
		maxCyclesToStore = len(issues)
	}
	analysisConfig := ApplyEnvOverrides(AnalysisConfig{
		ComputeCycles:    true,
		CyclesTimeout:    500 * time.Millisecond,
		MaxCyclesToStore: maxCyclesToStore,
	})
	stats := analyzer.AnalyzeWithConfig(analysisConfig)

	// Get cycles from the stats
	cycles := stats.Cycles()
	if len(cycles) == 0 {
		return nil
	}

	var suggestions []Suggestion
	byID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	for _, cycle := range cycles {
		// Skip self-loops if configured
		if len(cycle) == 2 && cycle[0] == cycle[1] && !config.IncludeSelfLoops {
			continue
		}
		if len(suggestions) >= config.MaxCycles {
			break
		}

		// Create cycle path string
		cyclePath := formatCyclePath(cycle)
		cycleLen := len(cycle) - 1 // Exclude the closing node

		// Determine confidence based on cycle length
		// Shorter cycles are more problematic (higher confidence)
		confidence := 1.0 - (float64(cycleLen-2) * 0.1)
		if confidence > 1.0 {
			confidence = 1.0 // Cap at 1.0 for self-loops (cycleLen=1)
		}
		if confidence < 0.5 {
			confidence = 0.5
		}

		// Generate summary based on cycle type
		var summary string
		if cycleLen == 1 {
			summary = fmt.Sprintf("Self-loop: %s depends on itself", cycle[0])
		} else if cycleLen == 2 {
			summary = fmt.Sprintf("Direct cycle between %s and %s", cycle[0], cycle[1])
		} else {
			summary = fmt.Sprintf("Dependency cycle of %d issues", cycleLen)
		}

		// First issue in cycle is the target
		targetBead := cycle[0]

		sug := NewSuggestion(
			SuggestionCycleWarning,
			targetBead,
			summary,
			fmt.Sprintf("Cycle path: %s", cyclePath),
			confidence,
		).WithMetadata("cycle_length", cycleLen).
			WithMetadata("cycle_path", cycle[:len(cycle)-1]) // Store path without closing node

		// Add action command to break the cycle using the lowest-collateral edge.
		// Start from the closing edge (cycle[len-1] -> cycle[0]) so equal-collateral
		// cycles preserve the existing closing-edge mutation routing contract, while
		// strictly lower-collateral edges always win (breaking ties lexicographically).
		if cycleLen >= 2 {
			bestFrom := cycle[cycleLen-1]
			bestTo := cycle[0]
			bestCollateral := analyzer.countDependents(bestTo)
			improvedFromClosing := false
			for j := 0; j < cycleLen; j++ {
				candFrom := cycle[j]
				candTo := cycle[j+1]
				if candFrom == "" || candTo == "" {
					continue
				}
				candCol := analyzer.countDependents(candTo)
				if candCol < bestCollateral {
					bestFrom = candFrom
					bestTo = candTo
					bestCollateral = candCol
					improvedFromClosing = true
				} else if improvedFromClosing && candCol == bestCollateral &&
					(candFrom < bestFrom || (candFrom == bestFrom && candTo < bestTo)) {
					bestFrom = candFrom
					bestTo = candTo
				}
			}
			peer := byID[bestTo]
			sug = sug.withMutationAction(byID[bestFrom], model.MutationRemoveDependency, &peer, "").
				WithMetadata("recommended_cut", map[string]any{
					"from":       bestFrom,
					"to":         bestTo,
					"collateral": bestCollateral,
				})
		}

		// If there's a second issue, mark it as related
		if cycleLen >= 2 {
			sug = sug.WithRelatedBead(cycle[1])
		}

		suggestions = append(suggestions, sug)
	}

	return suggestions
}

// formatCyclePath creates a readable cycle path string
func formatCyclePath(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}
	return strings.Join(cycle, " → ")
}

// WouldCreateCycle checks if adding a dependency from->to would create a cycle
func WouldCreateCycle(issues []model.Issue, fromID, toID string) (bool, []string) {
	// Build adjacency map
	adj := make(map[string][]string)
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			adj[issue.ID] = append(adj[issue.ID], dep.DependsOnID)
		}
	}

	// Add the proposed edge
	adj[fromID] = append(adj[fromID], toID)

	// Sort adjacency lists for determinism
	for k := range adj {
		sort.Strings(adj[k])
	}

	// DFS from toID to see if we can reach fromID
	visited := make(map[string]bool)
	path := []string{}

	var dfs func(node string) bool
	dfs = func(node string) bool {
		if node == fromID {
			path = append(path, node)
			return true
		}
		if visited[node] {
			return false
		}
		visited[node] = true
		path = append(path, node)

		for _, next := range adj[node] {
			if dfs(next) {
				return true
			}
		}

		path = path[:len(path)-1]
		return false
	}

	if dfs(toID) {
		// Cycle found: path goes from toID back to fromID
		// Prepend fromID to show the complete proposed cycle
		cyclePath := append([]string{fromID}, path...)
		return true, cyclePath
	}

	return false, nil
}

// CheckDependencyAddition validates if a dependency can be added without creating a cycle
// Returns (canAdd, cyclePath, warning)
func CheckDependencyAddition(issues []model.Issue, fromID, toID string) (bool, []string, string) {
	wouldCycle, path := WouldCreateCycle(issues, fromID, toID)
	if wouldCycle {
		cyclePath := formatCyclePath(path)
		warning := fmt.Sprintf("Adding dependency %s → %s would create cycle: %s", fromID, toID, cyclePath)
		return false, path, warning
	}
	return true, nil, ""
}

// DependencyRemovalCheck reports the pre-flight impact of removing blocking edge fromID -> toID.
type DependencyRemovalCheck struct {
	FromID              string   `json:"from_id"`
	ToID                string   `json:"to_id"`
	Exists              bool     `json:"exists"`
	CyclesBefore        int      `json:"cycles_before"`
	CyclesAfter         int      `json:"cycles_after"`
	CyclesBroken        int      `json:"cycles_broken"`
	CyclesTruncated     bool     `json:"cycles_truncated,omitempty"`
	WouldBreakAllCycles bool     `json:"would_break_all_cycles"`
	NewlyUnblocked      []string `json:"newly_unblocked,omitempty"`
	RemainingCycles     int      `json:"remaining_cycles"`
	Warning             string   `json:"warning,omitempty"`
}

// CheckDependencyRemoval simulates removing the blocking dependency fromID -> toID
// and reports cycle reduction and newly actionable issues (W32-2).
func CheckDependencyRemoval(issues []model.Issue, fromID, toID string, now time.Time) DependencyRemovalCheck {
	return CheckDependencyRemovalWithScope(issues, fromID, toID, nil, now)
}

// CheckDependencyRemovalWithScope simulates removing the blocking dependency fromID -> toID
// while preserving full-graph readiness authority and scoping reported cycles and newly unblocked
// issues strictly to candidateIDs when non-nil (P24A-3).
func CheckDependencyRemovalWithScope(issues []model.Issue, fromID, toID string, candidateIDs map[string]bool, now time.Time) DependencyRemovalCheck {
	res := DependencyRemovalCheck{
		FromID: fromID,
		ToID:   toID,
	}
	if fromID == "" || toID == "" || len(issues) == 0 {
		res.Warning = fmt.Sprintf("Blocking dependency %s → %s does not exist", fromID, toID)
		return res
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Collapse append-only duplicate records with last-record-wins semantics matching
	// NewAnalyzer / NewReadinessIndex so superseded edges never trigger false positives
	// or emit duplicate IDs into NewlyUnblocked (P22A-3).
	deduped := make([]model.Issue, 0, len(issues))
	dedupIdx := make(map[string]int, len(issues))
	for _, iss := range issues {
		if prev, ok := dedupIdx[iss.ID]; ok {
			deduped[prev] = iss
		} else {
			dedupIdx[iss.ID] = len(deduped)
			deduped = append(deduped, iss)
		}
	}

	exists := false
	modified := make([]model.Issue, len(deduped))
	for i, iss := range deduped {
		cloned := iss
		if len(iss.Dependencies) > 0 {
			if iss.ID == fromID {
				filtered := make([]*model.Dependency, 0, len(iss.Dependencies))
				for _, dep := range iss.Dependencies {
					if dep == nil {
						continue
					}
					if dep.Type.IsBlocking() && dep.DependsOnID == toID {
						exists = true
						continue
					}
					dc := *dep
					filtered = append(filtered, &dc)
				}
				cloned.Dependencies = filtered
			} else {
				depsCopy := make([]*model.Dependency, len(iss.Dependencies))
				for j, dep := range iss.Dependencies {
					if dep != nil {
						dc := *dep
						depsCopy[j] = &dc
					}
				}
				cloned.Dependencies = depsCopy
			}
		}
		modified[i] = cloned
	}
	if !exists {
		res.Warning = fmt.Sprintf("Blocking dependency %s → %s does not exist", fromID, toID)
		return res
	}
	res.Exists = true

	const maxCycles = 100
	cycleCfg := ApplyEnvOverrides(AnalysisConfig{
		ComputeCycles:    true,
		CyclesTimeout:    500 * time.Millisecond,
		MaxCyclesToStore: maxCycles,
	})
	beforeAn := NewAnalyzer(deduped)
	beforeAn.SetNow(now)
	beforeStats := beforeAn.AnalyzeWithConfig(cycleCfg)
	beforeReady := beforeAn.Readiness()

	afterAn := NewAnalyzer(modified)
	afterAn.SetNow(now)
	afterStats := afterAn.AnalyzeWithConfig(cycleCfg)
	afterReady := afterAn.Readiness()

	beforeStatus := beforeStats.Status().Cycles
	afterStatus := afterStats.Status().Cycles
	beforeState := beforeStatus.State
	afterState := afterStatus.State
	beforeTimedOut := beforeState == "timeout"
	afterTimedOut := afterState == "timeout"
	beforeSkipped := beforeState == "skipped"
	afterSkipped := afterState == "skipped"
	beforeTruncated := strings.Contains(strings.ToLower(beforeStatus.Reason), "truncated")
	afterTruncated := strings.Contains(strings.ToLower(afterStatus.Reason), "truncated")
	countScopedCycles := func(cycles [][]string) int {
		if candidateIDs == nil {
			return len(cycles)
		}
		count := 0
		for _, cyc := range cycles {
			if len(cyc) == 0 {
				continue
			}
			inScope := true
			for _, id := range cyc {
				if !candidateIDs[id] {
					inScope = false
					break
				}
			}
			if inScope {
				count++
			}
		}
		return count
	}
	res.CyclesBefore = countScopedCycles(beforeStats.Cycles())
	res.CyclesAfter = countScopedCycles(afterStats.Cycles())
	res.RemainingCycles = res.CyclesAfter
	res.CyclesTruncated = beforeTruncated || afterTruncated
	if beforeTimedOut || afterTimedOut {
		res.Warning = "Cycle detection timed out during dependency removal preflight; cycle counts are inconclusive"
		res.CyclesBroken = 0
		res.WouldBreakAllCycles = false
	} else if beforeSkipped || afterSkipped {
		res.Warning = "Cycle detection was skipped during dependency removal preflight; cycle counts are inconclusive"
		res.CyclesBroken = 0
		res.WouldBreakAllCycles = false
	} else if res.CyclesTruncated {
		if !afterTruncated && res.CyclesAfter == 0 && res.CyclesBefore > 0 {
			res.CyclesBroken = res.CyclesBefore
			res.WouldBreakAllCycles = true
			if len(afterStats.Cycles()) == 0 {
				res.Warning = fmt.Sprintf("Cycle enumeration reached cap (%d) before removal; removal leaves 0 active cycles (acyclic)", maxCycles)
			} else {
				res.Warning = fmt.Sprintf("Cycle enumeration reached cap (%d) before removal; removal leaves 0 active cycles in scope", maxCycles)
			}
		} else if afterTruncated || res.CyclesBefore == 0 || res.CyclesAfter >= res.CyclesBefore {
			res.CyclesBroken = 0
			res.WouldBreakAllCycles = false
			res.Warning = fmt.Sprintf("Cycle enumeration reached cap (%d); exact cycle reduction is inconclusive", maxCycles)
		} else {
			res.CyclesBroken = res.CyclesBefore - res.CyclesAfter
			res.WouldBreakAllCycles = false
			res.Warning = fmt.Sprintf("Cycle enumeration reached cap (%d); exact cycle reduction is approximate (>= %d broken, %d remaining)", maxCycles, res.CyclesBroken, res.CyclesAfter)
		}
	} else {
		if res.CyclesBefore > res.CyclesAfter {
			res.CyclesBroken = res.CyclesBefore - res.CyclesAfter
		}
		res.WouldBreakAllCycles = res.CyclesBefore > 0 && res.CyclesAfter == 0
	}

	var newly []string
	for _, iss := range deduped {
		if candidateIDs != nil && !candidateIDs[iss.ID] {
			continue
		}
		if !beforeReady.Ready(iss.ID, now) && afterReady.Ready(iss.ID, now) {
			newly = append(newly, iss.ID)
		}
	}
	sort.Strings(newly)
	res.NewlyUnblocked = newly
	return res
}

// CycleWarningDetector provides stateful cycle warning detection
type CycleWarningDetector struct {
	config CycleWarningConfig
}

// NewCycleWarningDetector creates a new detector with the given config
func NewCycleWarningDetector(config CycleWarningConfig) *CycleWarningDetector {
	return &CycleWarningDetector{
		config: config,
	}
}

// Detect finds cycle warnings
func (d *CycleWarningDetector) Detect(issues []model.Issue) []Suggestion {
	return DetectCycleWarnings(issues, d.config)
}

// ValidateNewDependency checks if a proposed dependency would create a cycle
func (d *CycleWarningDetector) ValidateNewDependency(issues []model.Issue, fromID, toID string) (bool, string) {
	canAdd, _, warning := CheckDependencyAddition(issues, fromID, toID)
	return canAdd, warning
}

// GetCycleFreeSuggestions filters out any missing-dependency suggestions that
// would create a blocking cycle in issues (or among previously accepted
// suggestions in the returned slice). Non-dependency suggestions pass through
// unchanged.
func GetCycleFreeSuggestions(issues []model.Issue, suggestions []Suggestion) []Suggestion {
	if len(suggestions) == 0 {
		return nil
	}

	adj := make(map[string][]string, len(issues))
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			adj[issue.ID] = append(adj[issue.ID], dep.DependsOnID)
		}
	}

	visited := make(map[string]bool, len(issues))
	canReach := func(start, goal string) bool {
		if start == goal {
			return true
		}
		clear(visited)
		stack := []string{start}
		visited[start] = true
		for len(stack) > 0 {
			curr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, next := range adj[curr] {
				if next == goal {
					return true
				}
				if !visited[next] {
					visited[next] = true
					stack = append(stack, next)
				}
			}
		}
		return false
	}

	out := make([]Suggestion, 0, len(suggestions))
	for _, sug := range suggestions {
		if sug.Type != SuggestionMissingDependency {
			out = append(out, sug)
			continue
		}
		fromID := sug.TargetBead
		toID := sug.RelatedBead
		if fromID == "" || toID == "" {
			out = append(out, sug)
			continue
		}
		if canReach(toID, fromID) {
			continue
		}
		adj[fromID] = append(adj[fromID], toID)
		out = append(out, sug)
	}
	return out
}
