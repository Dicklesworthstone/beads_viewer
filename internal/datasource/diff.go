package datasource

import (
	"fmt"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// SourceDiff represents differences between two data sources
type SourceDiff struct {
	// SourceA is the path of the first source
	SourceA string
	// SourceB is the path of the second source
	SourceB string
	// MissingInA contains issue IDs present in B but not in A
	MissingInA []string
	// MissingInB contains issue IDs present in A but not in B
	MissingInB []string
	// StatusMismatch contains issues with different status between sources
	StatusMismatch []StatusDifference
	// CountA is the number of issues in source A
	CountA int
	// CountB is the number of issues in source B
	CountB int
}

// StatusDifference represents a status mismatch for a single issue
type StatusDifference struct {
	ID      string `json:"id"`
	StatusA string `json:"status_a"`
	StatusB string `json:"status_b"`
}

// HasInconsistencies returns true if there are any differences between sources
func (d SourceDiff) HasInconsistencies() bool {
	return len(d.MissingInA) > 0 || len(d.MissingInB) > 0 || len(d.StatusMismatch) > 0
}

// Summary returns a human-readable summary of the differences
func (d SourceDiff) Summary() string {
	if !d.HasInconsistencies() {
		return fmt.Sprintf("Sources match (%d issues each)", d.CountA)
	}

	summary := fmt.Sprintf("Inconsistencies found between %s and %s:\n", d.SourceA, d.SourceB)

	if d.CountA != d.CountB {
		summary += fmt.Sprintf("  - Count mismatch: %d vs %d\n", d.CountA, d.CountB)
	}

	if len(d.MissingInA) > 0 {
		summary += fmt.Sprintf("  - %d issues in %s but not %s\n", len(d.MissingInA), d.SourceB, d.SourceA)
		if len(d.MissingInA) <= 5 {
			for _, id := range d.MissingInA {
				summary += fmt.Sprintf("    - %s\n", id)
			}
		}
	}

	if len(d.MissingInB) > 0 {
		summary += fmt.Sprintf("  - %d issues in %s but not %s\n", len(d.MissingInB), d.SourceA, d.SourceB)
		if len(d.MissingInB) <= 5 {
			for _, id := range d.MissingInB {
				summary += fmt.Sprintf("    - %s\n", id)
			}
		}
	}

	if len(d.StatusMismatch) > 0 {
		summary += fmt.Sprintf("  - %d issues with different status\n", len(d.StatusMismatch))
		if len(d.StatusMismatch) <= 5 {
			for _, m := range d.StatusMismatch {
				summary += fmt.Sprintf("    - %s: %s vs %s\n", m.ID, m.StatusA, m.StatusB)
			}
		}
	}

	return summary
}

// DiffOptions configures the diff operation
type DiffOptions struct {
	// IncludeTombstones includes tombstone/deleted issues in comparison
	IncludeTombstones bool
	// CompareFields specifies which fields to compare (empty = just status)
	CompareFields []string
	// MaxDifferences limits the number of differences tracked (0 = unlimited)
	MaxDifferences int
}

// DefaultDiffOptions returns sensible default diff options
func DefaultDiffOptions() DiffOptions {
	return DiffOptions{
		IncludeTombstones: false,
		CompareFields:     []string{"status"},
		MaxDifferences:    100,
	}
}

// DetectInconsistencies compares two sets of issues and returns differences
func DetectInconsistencies(issuesA, issuesB []model.Issue, sourceA, sourceB string, opts DiffOptions) SourceDiff {
	diff := SourceDiff{
		SourceA: sourceA,
		SourceB: sourceB,
	}

	// Build maps for fast lookup
	mapA := make(map[string]model.Issue)
	for _, issue := range issuesA {
		if !opts.IncludeTombstones && issue.Status.IsTombstone() {
			continue
		}
		mapA[issue.ID] = issue
	}

	mapB := make(map[string]model.Issue)
	for _, issue := range issuesB {
		if !opts.IncludeTombstones && issue.Status.IsTombstone() {
			continue
		}
		mapB[issue.ID] = issue
	}

	diff.CountA = len(mapA)
	diff.CountB = len(mapB)

	// Find issues in A but not in B
	for id := range mapA {
		if _, exists := mapB[id]; !exists {
			if opts.MaxDifferences == 0 || len(diff.MissingInB) < opts.MaxDifferences {
				diff.MissingInB = append(diff.MissingInB, id)
			}
		}
	}

	// Find issues in B but not in A, and status mismatches
	for id, issueB := range mapB {
		issueA, exists := mapA[id]
		if !exists {
			if opts.MaxDifferences == 0 || len(diff.MissingInA) < opts.MaxDifferences {
				diff.MissingInA = append(diff.MissingInA, id)
			}
		} else {
			// Check for status mismatch
			if issueA.Status != issueB.Status {
				if opts.MaxDifferences == 0 || len(diff.StatusMismatch) < opts.MaxDifferences {
					diff.StatusMismatch = append(diff.StatusMismatch, StatusDifference{
						ID:      id,
						StatusA: string(issueA.Status),
						StatusB: string(issueB.Status),
					})
				}
			}
		}
	}

	return diff
}

// CompareSources loads and compares two data sources
func CompareSources(sourceA, sourceB DataSource, opts DiffOptions) (*SourceDiff, error) {
	// Load issues from source A
	issuesA, err := loadIssuesFromSource(sourceA)
	if err != nil {
		return nil, fmt.Errorf("failed to load source A (%s): %w", sourceA.Path, err)
	}

	// Load issues from source B
	issuesB, err := loadIssuesFromSource(sourceB)
	if err != nil {
		return nil, fmt.Errorf("failed to load source B (%s): %w", sourceB.Path, err)
	}

	diff := DetectInconsistencies(issuesA, issuesB, sourceA.Path, sourceB.Path, opts)
	return &diff, nil
}

// loadIssuesFromSource loads issues from any source type via the IssueReader interface.
func loadIssuesFromSource(source DataSource) ([]model.Issue, error) {
	return LoadFromSource(source)
}

// CheckAllSourcesConsistent compares all sources and reports any inconsistencies
func CheckAllSourcesConsistent(sources []DataSource, opts DiffOptions) ([]SourceDiff, error) {
	var diffs []SourceDiff

	// Compare each valid source with every other valid source
	for i := 0; i < len(sources); i++ {
		if !sources[i].Valid {
			continue
		}
		for j := i + 1; j < len(sources); j++ {
			if !sources[j].Valid {
				continue
			}

			diff, err := CompareSources(sources[i], sources[j], opts)
			if err != nil {
				// Log error but continue
				continue
			}

			if diff.HasInconsistencies() {
				diffs = append(diffs, *diff)
			}
		}
	}

	return diffs, nil
}

// InconsistencyReport provides a comprehensive report of all source inconsistencies
type InconsistencyReport struct {
	// Sources is the list of all sources checked
	Sources []DataSource
	// Diffs contains all detected differences
	Diffs []SourceDiff
	// TotalInconsistencies is the total number of inconsistencies found
	TotalInconsistencies int
	// HasCriticalInconsistencies indicates severe problems (status differences)
	HasCriticalInconsistencies bool
}

// GenerateInconsistencyReport creates a comprehensive report
func GenerateInconsistencyReport(sources []DataSource, opts DiffOptions) (*InconsistencyReport, error) {
	diffs, err := CheckAllSourcesConsistent(sources, opts)
	if err != nil {
		return nil, err
	}

	report := &InconsistencyReport{
		Sources: sources,
		Diffs:   diffs,
	}

	for _, diff := range diffs {
		report.TotalInconsistencies += len(diff.MissingInA) + len(diff.MissingInB) + len(diff.StatusMismatch)
		if len(diff.StatusMismatch) > 0 {
			report.HasCriticalInconsistencies = true
		}
	}

	return report, nil
}
