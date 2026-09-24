package datasource

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

// ErrNoValidSources is returned when no valid sources are found
var ErrNoValidSources = errors.New("no valid data sources found")

// SelectionOptions configures source selection behavior
type SelectionOptions struct {
	// PreferFreshest prioritizes ModTime over Priority
	// Default: true
	PreferFreshest bool
	// MinimumValidSources requires at least N valid sources to proceed
	// Default: 1
	MinimumValidSources int
	// MaxAgeDelta ignores sources older than this duration compared to the newest
	// Default: 0 (no limit)
	MaxAgeDelta time.Duration
	// Verbose enables detailed logging during selection
	Verbose bool
	// Logger receives log messages when Verbose is true
	Logger func(msg string)
}

// DefaultSelectionOptions returns sensible default selection options
func DefaultSelectionOptions() SelectionOptions {
	return SelectionOptions{
		PreferFreshest:      true,
		MinimumValidSources: 1,
		MaxAgeDelta:         0,
		Verbose:             false,
		Logger:              func(string) {},
	}
}

// SelectionResult contains the selected source and metadata about the selection
type SelectionResult struct {
	// Selected is the chosen data source
	Selected DataSource
	// Candidates is the list of all valid sources considered
	Candidates []DataSource
	// Reason explains why this source was selected
	Reason string
	// SelectionTime is when the selection was made
	SelectionTime time.Time
}

// sortByFreshnessThenPriority orders sources in place the same way the default
// selection does: freshest ModTime first, then higher Priority, then the
// canonical JSONL filename order. The fused load path uses this to try
// candidates in selection order.
func sortByFreshnessThenPriority(sources []DataSource) {
	sortBySelectionPreference(sources, true)
}

func sortBySelectionPreference(sources []DataSource, preferFreshest bool) {
	sort.Slice(sources, func(i, j int) bool {
		left, right := sources[i], sources[j]
		if preferFreshest {
			if !left.ModTime.Equal(right.ModTime) {
				return left.ModTime.After(right.ModTime)
			}
			if left.Priority != right.Priority {
				return left.Priority > right.Priority
			}
		} else {
			if left.Priority != right.Priority {
				return left.Priority > right.Priority
			}
			if !left.ModTime.Equal(right.ModTime) {
				return left.ModTime.After(right.ModTime)
			}
		}

		leftRank, rightRank := jsonlFilenameAuthorityRank(left), jsonlFilenameAuthorityRank(right)
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		return left.Path < right.Path
	})
}

func jsonlFilenameAuthorityRank(source DataSource) int {
	if source.Type != SourceTypeJSONLLocal && source.Type != SourceTypeJSONLWorktree {
		return 0
	}

	name := filepath.Base(source.Path)
	for i, preferred := range loader.PreferredJSONLNames {
		if name == preferred {
			return len(loader.PreferredJSONLNames) - i
		}
	}
	return 0
}

// SelectBestSource chooses the best data source from the given list
func SelectBestSource(sources []DataSource) (DataSource, error) {
	return SelectBestSourceWithOptions(sources, DefaultSelectionOptions())
}

// SelectBestSourceWithOptions chooses the best data source with custom options
func SelectBestSourceWithOptions(sources []DataSource, opts SelectionOptions) (DataSource, error) {
	result, err := SelectBestSourceDetailed(sources, opts)
	if err != nil {
		return DataSource{}, err
	}
	return result.Selected, nil
}

// SelectBestSourceDetailed chooses the best data source with full details
func SelectBestSourceDetailed(sources []DataSource, opts SelectionOptions) (*SelectionResult, error) {
	if opts.Logger == nil {
		opts.Logger = func(string) {}
	}

	// Filter to valid sources only
	var valid []DataSource
	for _, s := range sources {
		if s.Valid {
			valid = append(valid, s)
		}
	}

	if len(valid) == 0 {
		return nil, ErrNoValidSources
	}

	if len(valid) < opts.MinimumValidSources {
		return nil, fmt.Errorf("only %d valid sources, need %d", len(valid), opts.MinimumValidSources)
	}

	// Sort by preference
	sortBySelectionPreference(valid, opts.PreferFreshest)

	// Apply age delta filter if specified
	if opts.MaxAgeDelta > 0 && len(valid) > 0 {
		newestTime := newestModTime(valid)
		cutoff := newestTime.Add(-opts.MaxAgeDelta)
		var filtered []DataSource
		for _, s := range valid {
			if s.ModTime.After(cutoff) || s.ModTime.Equal(cutoff) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			valid = filtered
		}
	}

	selected := valid[0]

	// Build reason string
	reason := buildSelectionReason(selected, valid, opts)

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("Selected: %s (%s)", selected.Path, reason))
	}

	return &SelectionResult{
		Selected:      selected,
		Candidates:    valid,
		Reason:        reason,
		SelectionTime: time.Now(),
	}, nil
}

func newestModTime(sources []DataSource) time.Time {
	var newest time.Time
	for _, source := range sources {
		if source.ModTime.After(newest) {
			newest = source.ModTime
		}
	}
	return newest
}

// buildSelectionReason creates a human-readable explanation for the selection
func buildSelectionReason(selected DataSource, candidates []DataSource, opts SelectionOptions) string {
	if len(candidates) == 1 {
		return "only valid source available"
	}

	reasons := []string{}

	// Check if newest
	isNewest := true
	for _, c := range candidates {
		if c.ModTime.After(selected.ModTime) {
			isNewest = false
			break
		}
	}
	if isNewest {
		reasons = append(reasons, "freshest modification time")
	}

	// Check if highest priority
	isHighestPriority := true
	for _, c := range candidates {
		if c.Priority > selected.Priority {
			isHighestPriority = false
			break
		}
	}
	if isHighestPriority {
		reasons = append(reasons, fmt.Sprintf("highest priority (%d)", selected.Priority))
	}

	// Check source type
	switch selected.Type {
	case SourceTypeSQLite:
		reasons = append(reasons, "SQLite is most authoritative")
	case SourceTypeJSONLWorktree:
		reasons = append(reasons, "synced worktree data")
	case SourceTypeJSONLLocal:
		reasons = append(reasons, "local JSONL file")
	}

	if len(reasons) == 0 {
		return "best available source"
	}

	return fmt.Sprintf("%s", reasons[0])
}

// SelectWithFallback tries sources in order until one succeeds validation and loading
func SelectWithFallback(sources []DataSource, loadFunc func(DataSource) error, opts SelectionOptions) (*DataSource, error) {
	if opts.Logger == nil {
		opts.Logger = func(string) {}
	}

	// Sort sources by preference first
	sorted := make([]DataSource, len(sources))
	copy(sorted, sources)

	sortBySelectionPreference(sorted, opts.PreferFreshest)

	// Try each source in order
	var lastErr error
	for i := range sorted {
		source := &sorted[i]

		// Skip if already known invalid
		if !source.Valid && source.ValidationError != "" {
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Skipping invalid source: %s (%s)", source.Path, source.ValidationError))
			}
			continue
		}

		// Validate if not already validated
		if !source.Valid {
			if err := ValidateSource(source); err != nil {
				if opts.Verbose {
					opts.Logger(fmt.Sprintf("Validation failed for %s: %v", source.Path, err))
				}
				lastErr = err
				continue
			}
		}

		// Try loading
		if err := loadFunc(*source); err != nil {
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Load failed for %s: %v", source.Path, err))
			}
			lastErr = err
			continue
		}

		if opts.Verbose {
			opts.Logger(fmt.Sprintf("Successfully loaded from: %s", source.Path))
		}
		return source, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all sources failed, last error: %w", lastErr)
	}
	return nil, ErrNoValidSources
}
