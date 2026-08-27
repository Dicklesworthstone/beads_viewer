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

		// Add action command to break the cycle
		if cycleLen >= 2 {
			// Suggest removing the last edge in the cycle
			from := cycle[cycleLen-1]
			to := cycle[0]
			fromArg, fromOK := quoteBeadsCommandID(from)
			toArg, toOK := quoteBeadsCommandID(to)
			if fromOK && toOK {
				sug = sug.WithAction(fmt.Sprintf("br dep remove %s %s", fromArg, toArg))
			}
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
