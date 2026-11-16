package datasource

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
	// MissingIssues stores cloned Issue values for IDs in MissingInA and MissingInB
	// so callers can evaluate scope filters (--label, --repo, --recipe) on secondary-only issues.
	MissingIssues map[string]model.Issue `json:"-"`
	// StatusMismatch contains issues with different status between sources
	StatusMismatch []StatusDifference
	// FieldMismatches contains issues with non-status field differences (e.g. priority, title)
	FieldMismatches []FieldDifference
	// IssueDiffs contains deterministic per-issue/per-field difference records
	IssueDiffs []IssueSourceDiff
	// CountA is the number of issues in source A
	CountA int
	// CountB is the number of issues in source B
	CountB int

	// Source modification timestamps when compared via CompareSources.
	ModTimeA time.Time `json:"-"`
	ModTimeB time.Time `json:"-"`

	// Optional single-issue fields when SourceDiff is constructed directly for one discrepancy
	IssueID    string
	Difference string
	ValueA     string
	ValueB     string
}

// StatusDifference represents a status mismatch for a single issue
type StatusDifference struct {
	ID      string `json:"id"`
	StatusA string `json:"status_a"`
	StatusB string `json:"status_b"`
}

// FieldDifference represents a field mismatch for a single issue between two sources
type FieldDifference struct {
	ID     string `json:"id"`
	Field  string `json:"field"`
	ValueA string `json:"value_a"`
	ValueB string `json:"value_b"`
}

// IssueSourceDiff represents a single deterministic discrepancy for an issue across two sources
type IssueSourceDiff struct {
	IssueID    string    `json:"issue_id"`
	Field      string    `json:"field"`
	Difference string    `json:"difference"`
	SourceA    string    `json:"source_a"`
	SourceB    string    `json:"source_b"`
	ValueA     string    `json:"value_a,omitempty"`
	ValueB     string    `json:"value_b,omitempty"`
	UpdatedAtA time.Time `json:"-"`
	UpdatedAtB time.Time `json:"-"`
	ModTimeA   time.Time `json:"-"`
	ModTimeB   time.Time `json:"-"`
}

// HasInconsistencies returns true if there are any differences between sources
func (d SourceDiff) HasInconsistencies() bool {
	return len(d.MissingInA) > 0 ||
		len(d.MissingInB) > 0 ||
		len(d.StatusMismatch) > 0 ||
		len(d.FieldMismatches) > 0 ||
		len(d.IssueDiffs) > 0 ||
		d.IssueID != "" ||
		d.Difference != ""
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

	if len(d.FieldMismatches) > 0 {
		summary += fmt.Sprintf("  - %d issues with field differences\n", len(d.FieldMismatches))
		if len(d.FieldMismatches) <= 5 {
			for _, m := range d.FieldMismatches {
				summary += fmt.Sprintf("    - %s (%s): %s vs %s\n", m.ID, m.Field, m.ValueA, m.ValueB)
			}
		}
	}

	if d.IssueID != "" && len(d.MissingInA) == 0 && len(d.MissingInB) == 0 && len(d.StatusMismatch) == 0 && len(d.FieldMismatches) == 0 && len(d.IssueDiffs) == 0 {
		summary += fmt.Sprintf("  - %s: %s\n", d.IssueID, d.Difference)
	}

	return summary
}

// DiffOptions configures the diff operation
type DiffOptions struct {
	// IncludeTombstones includes tombstone/deleted issues in comparison
	IncludeTombstones bool
	// CompareFields specifies which fields to compare (empty = DefaultDiffOptions().CompareFields)
	CompareFields []string
	// MaxDifferences limits the number of differences tracked (0 = unlimited)
	MaxDifferences int
}

var defaultSourceCompareFields = []string{
	"status",
	"priority",
	"title",
	"dependencies",
	"assignee",
	"labels",
	"defer_until",
}

// DefaultDiffOptions returns sensible default diff options
func DefaultDiffOptions() DiffOptions {
	fields := make([]string, len(defaultSourceCompareFields))
	copy(fields, defaultSourceCompareFields)
	return DiffOptions{
		IncludeTombstones: false,
		CompareFields:     fields,
		MaxDifferences:    100,
	}
}

func normalizeCompareFields(fields []string) []string {
	if len(fields) == 0 {
		fields = defaultSourceCompareFields
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, raw := range fields {
		norm := strings.ToLower(strings.TrimSpace(raw))
		switch norm {
		case "status", "priority", "title", "dependencies", "assignee", "labels", "defer_until":
			if !seen[norm] {
				seen[norm] = true
				out = append(out, norm)
			}
		}
	}
	return out
}

func canonicalSourceDependencies(deps []*model.Dependency) string {
	if len(deps) == 0 {
		return ""
	}
	type depPair struct {
		target string
		kind   string
	}
	seen := make(map[depPair]struct{}, len(deps))
	pairs := make([]depPair, 0, len(deps))
	for _, dep := range deps {
		if dep == nil {
			continue
		}
		target := strings.TrimSpace(dep.DependsOnID)
		if target == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(string(dep.Type)))
		if kind == "" {
			kind = string(model.DepBlocks)
		}
		p := depPair{target: target, kind: kind}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		pairs = append(pairs, p)
	}
	if len(pairs) == 0 {
		return ""
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].target != pairs[j].target {
			return pairs[i].target < pairs[j].target
		}
		return pairs[i].kind < pairs[j].kind
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.target + ":" + p.kind
	}
	return strings.Join(parts, ", ")
}

func canonicalSourceLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(labels))
	unique := make([]string, 0, len(labels))
	for _, raw := range labels {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	if len(unique) == 0 {
		return ""
	}
	sort.Strings(unique)
	return strings.Join(unique, ", ")
}

func canonicalSourceDeferUntil(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// DetectInconsistencies compares two sets of issues and returns differences
func DetectInconsistencies(issuesA, issuesB []model.Issue, sourceA, sourceB string, opts DiffOptions) SourceDiff {
	diff := SourceDiff{
		SourceA: sourceA,
		SourceB: sourceB,
	}

	// Build maps for fast lookup
	mapA := make(map[string]model.Issue, len(issuesA))
	for _, issue := range issuesA {
		if !opts.IncludeTombstones && issue.Status.Normalize().IsTombstone() {
			continue
		}
		mapA[issue.ID] = issue
	}

	mapB := make(map[string]model.Issue, len(issuesB))
	for _, issue := range issuesB {
		if !opts.IncludeTombstones && issue.Status.Normalize().IsTombstone() {
			continue
		}
		mapB[issue.ID] = issue
	}

	diff.CountA = len(mapA)
	diff.CountB = len(mapB)

	allIDs := make(map[string]struct{}, len(mapA)+len(mapB))
	for id := range mapA {
		allIDs[id] = struct{}{}
	}
	for id := range mapB {
		allIDs[id] = struct{}{}
	}

	sortedIDs := make([]string, 0, len(allIDs))
	for id := range allIDs {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)

	compareFields := normalizeCompareFields(opts.CompareFields)
	recorded := 0
	atLimit := func() bool {
		return opts.MaxDifferences > 0 && recorded >= opts.MaxDifferences
	}

	for _, id := range sortedIDs {
		if atLimit() {
			break
		}
		issueA, inA := mapA[id]
		issueB, inB := mapB[id]

		if inA && !inB {
			if diff.MissingIssues == nil {
				diff.MissingIssues = make(map[string]model.Issue)
			}
			diff.MissingIssues[id] = issueA.Clone()
			diff.MissingInB = append(diff.MissingInB, id)
			diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
				IssueID:    id,
				Field:      "presence",
				Difference: fmt.Sprintf("missing in %s", sourceB),
				SourceA:    sourceA,
				SourceB:    sourceB,
				ValueA:     string(issueA.Status),
				UpdatedAtA: issueA.UpdatedAt,
			})
			recorded++
			continue
		}

		if inB && !inA {
			if diff.MissingIssues == nil {
				diff.MissingIssues = make(map[string]model.Issue)
			}
			diff.MissingIssues[id] = issueB.Clone()
			diff.MissingInA = append(diff.MissingInA, id)
			diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
				IssueID:    id,
				Field:      "presence",
				Difference: fmt.Sprintf("missing in %s", sourceA),
				SourceA:    sourceA,
				SourceB:    sourceB,
				ValueB:     string(issueB.Status),
				UpdatedAtB: issueB.UpdatedAt,
			})
			recorded++
			continue
		}

		for _, field := range compareFields {
			if atLimit() {
				break
			}
			switch field {
			case "status":
				if issueA.Status.Normalize() != issueB.Status.Normalize() {
					valA := string(issueA.Status)
					valB := string(issueB.Status)
					diff.StatusMismatch = append(diff.StatusMismatch, StatusDifference{
						ID:      id,
						StatusA: valA,
						StatusB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "status",
						Difference: "status mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "priority":
				if issueA.Priority != issueB.Priority {
					valA := fmt.Sprintf("P%d", issueA.Priority)
					valB := fmt.Sprintf("P%d", issueB.Priority)
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "priority",
						ValueA: valA,
						ValueB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "priority",
						Difference: "priority mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "title":
				if issueA.Title != issueB.Title {
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "title",
						ValueA: issueA.Title,
						ValueB: issueB.Title,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "title",
						Difference: "title mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     issueA.Title,
						ValueB:     issueB.Title,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "dependencies":
				valA := canonicalSourceDependencies(issueA.Dependencies)
				valB := canonicalSourceDependencies(issueB.Dependencies)
				if valA != valB {
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "dependencies",
						ValueA: valA,
						ValueB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "dependencies",
						Difference: "dependencies mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "assignee":
				valA := strings.TrimSpace(issueA.Assignee)
				valB := strings.TrimSpace(issueB.Assignee)
				if valA != valB {
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "assignee",
						ValueA: valA,
						ValueB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "assignee",
						Difference: "assignee mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "labels":
				valA := canonicalSourceLabels(issueA.Labels)
				valB := canonicalSourceLabels(issueB.Labels)
				if valA != valB {
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "labels",
						ValueA: valA,
						ValueB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "labels",
						Difference: "labels mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
				}
			case "defer_until":
				valA := canonicalSourceDeferUntil(issueA.DeferUntil)
				valB := canonicalSourceDeferUntil(issueB.DeferUntil)
				if valA != valB {
					diff.FieldMismatches = append(diff.FieldMismatches, FieldDifference{
						ID:     id,
						Field:  "defer_until",
						ValueA: valA,
						ValueB: valB,
					})
					diff.IssueDiffs = append(diff.IssueDiffs, IssueSourceDiff{
						IssueID:    id,
						Field:      "defer_until",
						Difference: "defer_until mismatch",
						SourceA:    sourceA,
						SourceB:    sourceB,
						ValueA:     valA,
						ValueB:     valB,
						UpdatedAtA: issueA.UpdatedAt,
						UpdatedAtB: issueB.UpdatedAt,
					})
					recorded++
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
	diff.ModTimeA = sourceA.ModTime
	diff.ModTimeB = sourceB.ModTime
	for i := range diff.IssueDiffs {
		diff.IssueDiffs[i].ModTimeA = sourceA.ModTime
		diff.IssueDiffs[i].ModTimeB = sourceB.ModTime
	}
	return &diff, nil
}

// loadIssuesFromSource loads issues from any source type via the IssueReader interface.
func loadIssuesFromSource(source DataSource) ([]model.Issue, error) {
	loaded, err := LoadFromSource(source)
	return loaded.Issues, err
}

// CheckSourcesConsistent compares all valid sources across all default source-drift fields.
func CheckSourcesConsistent(sources []DataSource) ([]SourceDiff, error) {
	return CheckAllSourcesConsistent(sources, DiffOptions{
		MaxDifferences: 0,
		CompareFields:  append([]string(nil), defaultSourceCompareFields...),
	})
}

// CheckAllSourcesConsistent compares all sources and reports any inconsistencies
func CheckAllSourcesConsistent(sources []DataSource, opts DiffOptions) ([]SourceDiff, error) {
	if len(opts.CompareFields) == 0 {
		opts.CompareFields = append([]string(nil), defaultSourceCompareFields...)
	}
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
		if len(diff.IssueDiffs) > 0 {
			report.TotalInconsistencies += len(diff.IssueDiffs)
		} else {
			report.TotalInconsistencies += len(diff.MissingInA) + len(diff.MissingInB) + len(diff.StatusMismatch) + len(diff.FieldMismatches)
		}
		if len(diff.StatusMismatch) > 0 {
			report.HasCriticalInconsistencies = true
		}
	}

	return report, nil
}
