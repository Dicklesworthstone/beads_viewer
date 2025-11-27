package analysis

import (
	"fmt"
	"sort"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Snapshot represents the state of issues at a point in time
type Snapshot struct {
	Timestamp time.Time     `json:"timestamp"`
	Revision  string        `json:"revision,omitempty"` // Git SHA or ref
	Issues    []model.Issue `json:"issues"`
	Stats     *GraphStats   `json:"stats,omitempty"`

	// Computed counts
	TotalCount   int `json:"total_count"`
	OpenCount    int `json:"open_count"`
	ClosedCount  int `json:"closed_count"`
	BlockedCount int `json:"blocked_count"`
}

// NewSnapshot creates a snapshot from issues
func NewSnapshot(issues []model.Issue) *Snapshot {
	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()

	snap := &Snapshot{
		Timestamp: time.Now(),
		Issues:    issues,
		Stats:     &stats,
	}
	snap.computeCounts()
	return snap
}

// NewSnapshotAt creates a snapshot with timestamp metadata
func NewSnapshotAt(issues []model.Issue, timestamp time.Time, revision string) *Snapshot {
	analyzer := NewAnalyzer(issues)
	stats := analyzer.Analyze()

	snap := &Snapshot{
		Timestamp: timestamp,
		Revision:  revision,
		Issues:    issues,
		Stats:     &stats,
	}
	snap.computeCounts()
	return snap
}

// computeCounts calculates issue counts by status
func (s *Snapshot) computeCounts() {
	s.TotalCount = len(s.Issues)
	for _, issue := range s.Issues {
		switch issue.Status {
		case model.StatusClosed:
			s.ClosedCount++
		case model.StatusBlocked:
			s.BlockedCount++
			s.OpenCount++ // Blocked is a type of open
		case model.StatusOpen, model.StatusInProgress:
			s.OpenCount++
		}
	}
}

// SnapshotDiff represents the differences between two snapshots
type SnapshotDiff struct {
	// Time range
	FromTimestamp time.Time `json:"from_timestamp"`
	ToTimestamp   time.Time `json:"to_timestamp"`
	FromRevision  string    `json:"from_revision,omitempty"`
	ToRevision    string    `json:"to_revision,omitempty"`

	// Issue changes
	NewIssues      []model.Issue   `json:"new_issues"`      // In To but not From
	ClosedIssues   []model.Issue   `json:"closed_issues"`   // Status changed to closed
	RemovedIssues  []model.Issue   `json:"removed_issues"`  // In From but not To
	ReopenedIssues []model.Issue   `json:"reopened_issues"` // Status changed from closed to open
	ModifiedIssues []ModifiedIssue `json:"modified_issues"` // Changed between snapshots

	// Graph changes
	NewCycles      [][]string `json:"new_cycles"`      // Cycles appearing in To
	ResolvedCycles [][]string `json:"resolved_cycles"` // Cycles resolved (were in From, not in To)

	// Metric deltas
	MetricDeltas MetricDeltas `json:"metric_deltas"`

	// Summary statistics
	Summary DiffSummary `json:"summary"`
}

// ModifiedIssue captures what changed in an issue
type ModifiedIssue struct {
	IssueID  string        `json:"issue_id"`
	Title    string        `json:"title"`
	Changes  []FieldChange `json:"changes"`
	OldIssue model.Issue   `json:"-"` // Full old state (not serialized to keep diff concise)
	NewIssue model.Issue   `json:"-"` // Full new state
}

// FieldChange describes a single field change
type FieldChange struct {
	Field    string `json:"field"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
}

// MetricDeltas tracks changes in key metrics
type MetricDeltas struct {
	TotalIssues    int     `json:"total_issues"`
	OpenIssues     int     `json:"open_issues"`
	ClosedIssues   int     `json:"closed_issues"`
	BlockedIssues  int     `json:"blocked_issues"`
	TotalEdges     int     `json:"total_edges"`
	CycleCount     int     `json:"cycle_count"`
	ComponentCount int     `json:"component_count"`
	AvgPageRank    float64 `json:"avg_pagerank"`
	AvgBetweenness float64 `json:"avg_betweenness"`
}

// DiffSummary provides quick overview of changes
type DiffSummary struct {
	TotalChanges     int    `json:"total_changes"`
	IssuesAdded      int    `json:"issues_added"`
	IssuesClosed     int    `json:"issues_closed"`
	IssuesRemoved    int    `json:"issues_removed"`
	IssuesReopened   int    `json:"issues_reopened"`
	IssuesModified   int    `json:"issues_modified"`
	CyclesIntroduced int    `json:"cycles_introduced"`
	CyclesResolved   int    `json:"cycles_resolved"`
	NetIssueChange   int    `json:"net_issue_change"`
	HealthTrend      string `json:"health_trend"` // "improving", "degrading", "stable"
}

// CompareSnapshots computes the diff between two snapshots
func CompareSnapshots(from, to *Snapshot) *SnapshotDiff {
	diff := &SnapshotDiff{
		FromTimestamp: from.Timestamp,
		ToTimestamp:   to.Timestamp,
		FromRevision:  from.Revision,
		ToRevision:    to.Revision,
	}

	// Build issue maps for quick lookup
	fromMap := make(map[string]model.Issue)
	for _, issue := range from.Issues {
		fromMap[issue.ID] = issue
	}

	toMap := make(map[string]model.Issue)
	for _, issue := range to.Issues {
		toMap[issue.ID] = issue
	}

	// Find new, removed, closed, reopened, and modified issues
	for id, toIssue := range toMap {
		fromIssue, existed := fromMap[id]
		if !existed {
			diff.NewIssues = append(diff.NewIssues, toIssue)
			continue
		}

		// Check for status changes
		isStatusChange := false
		if fromIssue.Status != model.StatusClosed && toIssue.Status == model.StatusClosed {
			diff.ClosedIssues = append(diff.ClosedIssues, toIssue)
			isStatusChange = true
		} else if fromIssue.Status == model.StatusClosed && toIssue.Status != model.StatusClosed {
			diff.ReopenedIssues = append(diff.ReopenedIssues, toIssue)
			isStatusChange = true
		}

		// Check for other modifications
		// If status changed (Closed/Reopened), we treat it as a status transition, not a "Modification"
		// unless there are OTHER field changes. For simplicity in this diff, we exclude them from ModifiedIssues
		// to keep the lists disjoint and clearer for the user.
		if !isStatusChange {
			changes := detectChanges(fromIssue, toIssue)
			if len(changes) > 0 {
				diff.ModifiedIssues = append(diff.ModifiedIssues, ModifiedIssue{
					IssueID:  id,
					Title:    toIssue.Title,
					Changes:  changes,
					OldIssue: fromIssue,
					NewIssue: toIssue,
				})
			}
		}
	}

	// Find removed issues
	for id, fromIssue := range fromMap {
		if _, exists := toMap[id]; !exists {
			diff.RemovedIssues = append(diff.RemovedIssues, fromIssue)
		}
	}

	// Compare cycles
	diff.NewCycles, diff.ResolvedCycles = compareCycles(from.Stats, to.Stats)

	// Calculate metric deltas
	diff.MetricDeltas = calculateMetricDeltas(from, to)

	// Calculate summary
	diff.Summary = calculateSummary(diff)

	// Sort lists for consistent output
	sortIssuesByID(diff.NewIssues)
	sortIssuesByID(diff.ClosedIssues)
	sortIssuesByID(diff.RemovedIssues)
	sortIssuesByID(diff.ReopenedIssues)
	sortModifiedByID(diff.ModifiedIssues)

	return diff
}

// detectChanges identifies what fields changed between two issues
func detectChanges(from, to model.Issue) []FieldChange {
	var changes []FieldChange

	if from.Title != to.Title {
		changes = append(changes, FieldChange{
			Field:    "title",
			OldValue: from.Title,
			NewValue: to.Title,
		})
	}

	if from.Status != to.Status {
		changes = append(changes, FieldChange{
			Field:    "status",
			OldValue: string(from.Status),
			NewValue: string(to.Status),
		})
	}

	if from.Priority != to.Priority {
		changes = append(changes, FieldChange{
			Field:    "priority",
			OldValue: priorityString(from.Priority),
			NewValue: priorityString(to.Priority),
		})
	}

	if from.Assignee != to.Assignee {
		changes = append(changes, FieldChange{
			Field:    "assignee",
			OldValue: from.Assignee,
			NewValue: to.Assignee,
		})
	}

	if from.IssueType != to.IssueType {
		changes = append(changes, FieldChange{
			Field:    "type",
			OldValue: string(from.IssueType),
			NewValue: string(to.IssueType),
		})
	}

	// Check for text field changes (Description, Design, AC, Notes)
	if from.Description != to.Description {
		changes = append(changes, FieldChange{
			Field:    "description",
			OldValue: "(modified)",
			NewValue: "(modified)",
		})
	}
	if from.Design != to.Design {
		changes = append(changes, FieldChange{
			Field:    "design",
			OldValue: "(modified)",
			NewValue: "(modified)",
		})
	}
	if from.AcceptanceCriteria != to.AcceptanceCriteria {
		changes = append(changes, FieldChange{
			Field:    "acceptance_criteria",
			OldValue: "(modified)",
			NewValue: "(modified)",
		})
	}
	if from.Notes != to.Notes {
		changes = append(changes, FieldChange{
			Field:    "notes",
			OldValue: "(modified)",
			NewValue: "(modified)",
		})
	}

	// Check for dependency changes
	fromDeps := dependencySet(from.Dependencies)
	toDeps := dependencySet(to.Dependencies)
	if !equalStringSet(fromDeps, toDeps) {
		changes = append(changes, FieldChange{
			Field:    "dependencies",
			OldValue: formatDeps(fromDeps),
			NewValue: formatDeps(toDeps),
		})
	}

	// Check for label changes
	fromLabels := stringSet(from.Labels)
	toLabels := stringSet(to.Labels)
	if !equalStringSet(fromLabels, toLabels) {
		changes = append(changes, FieldChange{
			Field:    "labels",
			OldValue: formatLabels(from.Labels),
			NewValue: formatLabels(to.Labels),
		})
	}

	return changes
}

// compareCycles finds new and resolved cycles between stats
func compareCycles(from, to *GraphStats) (newCycles, resolvedCycles [][]string) {
	if from == nil && to == nil {
		return nil, nil
	}

	// Normalize cycle representations for comparison
	fromCycleSet := make(map[string][]string)
	if from != nil {
		for _, cycle := range from.Cycles {
			key := normalizeCycle(cycle)
			fromCycleSet[key] = cycle
		}
	}

	toCycleSet := make(map[string][]string)
	if to != nil {
		for _, cycle := range to.Cycles {
			key := normalizeCycle(cycle)
			toCycleSet[key] = cycle
		}
	}

	// Find new cycles (in to but not from)
	for key, cycle := range toCycleSet {
		if _, exists := fromCycleSet[key]; !exists {
			newCycles = append(newCycles, cycle)
		}
	}

	// Find resolved cycles (in from but not to)
	for key, cycle := range fromCycleSet {
		if _, exists := toCycleSet[key]; !exists {
			resolvedCycles = append(resolvedCycles, cycle)
		}
	}

	return newCycles, resolvedCycles
}

// normalizeCycle creates a canonical string representation of a cycle
func normalizeCycle(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}

	// Find the smallest element to start
	minIdx := 0
	for i, id := range cycle {
		if id < cycle[minIdx] {
			minIdx = i
		}
	}

	// Rotate to start from minimum
	rotated := make([]string, len(cycle))
	for i := range cycle {
		rotated[i] = cycle[(minIdx+i)%len(cycle)]
	}

	// Join for comparison
	result := ""
	for i, id := range rotated {
		if i > 0 {
			result += "->"
		}
		result += id
	}
	return result
}

// calculateMetricDeltas computes the difference in key metrics
func calculateMetricDeltas(from, to *Snapshot) MetricDeltas {
	deltas := MetricDeltas{}

	if from == nil || to == nil {
		return deltas
	}

	deltas.TotalIssues = to.TotalCount - from.TotalCount
	deltas.OpenIssues = to.OpenCount - from.OpenCount
	deltas.ClosedIssues = to.ClosedCount - from.ClosedCount
	deltas.BlockedIssues = to.BlockedCount - from.BlockedCount

	// Graph-level metrics from Stats
	if from.Stats != nil && to.Stats != nil {
		deltas.CycleCount = len(to.Stats.Cycles) - len(from.Stats.Cycles)
		deltas.AvgPageRank = avgMapValue(to.Stats.PageRank) - avgMapValue(from.Stats.PageRank)
		deltas.AvgBetweenness = avgMapValue(to.Stats.Betweenness) - avgMapValue(from.Stats.Betweenness)
	}

	return deltas
}

// avgMapValue computes the average value in a map
func avgMapValue(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	var sum float64
	for _, v := range m {
		sum += v
	}
	return sum / float64(len(m))
}

// calculateSummary generates summary statistics
func calculateSummary(diff *SnapshotDiff) DiffSummary {
	summary := DiffSummary{
		IssuesAdded:      len(diff.NewIssues),
		IssuesClosed:     len(diff.ClosedIssues),
		IssuesRemoved:    len(diff.RemovedIssues),
		IssuesReopened:   len(diff.ReopenedIssues),
		IssuesModified:   len(diff.ModifiedIssues),
		CyclesIntroduced: len(diff.NewCycles),
		CyclesResolved:   len(diff.ResolvedCycles),
	}

	summary.TotalChanges = summary.IssuesAdded + summary.IssuesClosed +
		summary.IssuesRemoved + summary.IssuesReopened + summary.IssuesModified

	summary.NetIssueChange = summary.IssuesAdded - summary.IssuesRemoved

	// Determine health trend
	score := 0

	// Resolving cycles is good
	score += summary.CyclesResolved * 2
	// Introducing cycles is bad
	score -= summary.CyclesIntroduced * 3

	// Closing issues is good
	score += summary.IssuesClosed

	// Reopening issues is mildly concerning
	score -= summary.IssuesReopened

	// Net decrease in blocked issues is good
	if diff.MetricDeltas.BlockedIssues < 0 {
		score += 2
	} else if diff.MetricDeltas.BlockedIssues > 0 {
		score -= 1
	}

	if score > 1 {
		summary.HealthTrend = "improving"
	} else if score < -1 {
		summary.HealthTrend = "degrading"
	} else {
		summary.HealthTrend = "stable"
	}

	return summary
}

// Helper functions

func priorityString(p int) string {
	if p >= 0 && p <= 9 {
		return "P" + string(rune('0'+p))
	}
	// For priority >= 10, use standard formatting
	return "P" + fmt.Sprintf("%d", p)
}

func dependencySet(deps []*model.Dependency) map[string]bool {
	set := make(map[string]bool)
	for _, dep := range deps {
		if dep == nil || dep.DependsOnID == "" {
			continue
		}
		set[dep.DependsOnID] = true
	}
	return set
}

func stringSet(strs []string) map[string]bool {
	set := make(map[string]bool)
	for _, s := range strs {
		set[s] = true
	}
	return set
}

func equalStringSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func formatDeps(deps map[string]bool) string {
	if len(deps) == 0 {
		return "(none)"
	}
	var list []string
	for dep := range deps {
		list = append(list, dep)
	}
	sort.Strings(list)
	return joinStrings(list, ", ")
}

func formatLabels(labels []string) string {
	if len(labels) == 0 {
		return "(none)"
	}
	sorted := make([]string, len(labels))
	copy(sorted, labels)
	sort.Strings(sorted)
	return joinStrings(sorted, ", ")
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func sortIssuesByID(issues []model.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})
}

func sortModifiedByID(modified []ModifiedIssue) {
	sort.Slice(modified, func(i, j int) bool {
		return modified[i].IssueID < modified[j].IssueID
	})
}

// IsEmpty returns true if there are no changes
func (d *SnapshotDiff) IsEmpty() bool {
	return d.Summary.TotalChanges == 0 &&
		d.Summary.CyclesIntroduced == 0 &&
		d.Summary.CyclesResolved == 0
}

// HasSignificantChanges returns true if there are important changes to review
func (d *SnapshotDiff) HasSignificantChanges() bool {
	return len(d.NewIssues) > 0 ||
		len(d.ClosedIssues) > 0 ||
		len(d.ReopenedIssues) > 0 ||
		len(d.NewCycles) > 0 ||
		len(d.ResolvedCycles) > 0 ||
		d.Summary.HealthTrend == "degrading"
}
